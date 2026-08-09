package csvcopy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// slice is a RowSource over a slice, so Copy can be tested without a file.
type slice[T any] struct {
	items []T
	pos   int
	err   error
}

func (s *slice[T]) Next() bool {
	if s.pos >= len(s.items) {
		return false
	}
	s.pos++

	return true
}

func (s *slice[T]) Value() T { return s.items[s.pos-1] }

func (s *slice[T]) Err() error { return s.err }

func TestCopy(t *testing.T) {
	src := &slice[*entity]{items: []*entity{{ID: "1", Name: "Alice"}, {ID: "2", Name: "Bob"}}}

	source := NewCopy(src, 2, func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})

	got := drain(t, source)
	want := [][]any{{"1", "Alice"}, {"2", "Bob"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %#v, want %#v", got, want)
	}
	if got, want := source.Rows(), int64(2); got != want {
		t.Errorf("Rows() = %d, want %d", got, want)
	}
	if err := source.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

func TestCopyPropagatesSourceError(t *testing.T) {
	want := errors.New("boom")
	source := NewCopy(&slice[int]{err: want}, 1, func(dst []any, i int) []any {
		return append(dst, i)
	})

	if source.Next() {
		t.Error("Next() returned true for an empty source")
	}
	if got := source.Err(); !errors.Is(got, want) {
		t.Errorf("Err() = %v, want %v", got, want)
	}
}

/*
The encoded row is stored back into the struct, so a column count that was guessed
too low grows the slice once instead of reallocating on every single row. Dropping
the result of append here would pay that allocation forever.
*/
func TestCopyGrowsBackingSliceOnce(t *testing.T) {
	src := &slice[int]{items: []int{1, 2, 3, 4}}

	source := NewCopy(src, 1, func(dst []any, i int) []any {
		return append(dst, i, i, i, i, i)
	})

	var previous []any
	for source.Next() {
		values, err := source.Values()
		if err != nil {
			t.Fatalf("Values: %v", err)
		}
		if previous != nil && &values[0] != &previous[0] {
			t.Fatal("Values() reallocated after the first row instead of keeping the grown slice")
		}
		previous = values
	}
}

// A nonsense column count must not panic on make(); it only sizes a buffer.
func TestCopyNegativeColumns(t *testing.T) {
	source := NewCopy(&slice[int]{items: []int{7}}, -1, func(dst []any, i int) []any {
		return append(dst, i)
	})

	if got := drain(t, source); !reflect.DeepEqual(got, [][]any{{7}}) {
		t.Errorf("rows = %#v, want [[7]]", got)
	}
}

func TestCopyNilArguments(t *testing.T) {
	t.Run("nil src", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewCopy did not panic on a nil source")
			}
		}()
		NewCopy[int](nil, 1, func(dst []any, i int) []any { return dst })
	})

	t.Run("nil encode", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewCopy did not panic on a nil encode")
			}
		}()
		NewCopy[int](&slice[int]{}, 1, nil)
	})
}

// The whole point of the layering: Typed decodes, Copy lays out, and neither knows
// about the other's concerns.
func TestCopyOverTyped(t *testing.T) {
	rows, err := NewTyped(strings.NewReader("id;name\n1;Alice\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	source := NewCopy(rows, 2, func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})

	got := drain(t, source)
	if want := [][]any{{"1", "Alice"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %#v, want %#v", got, want)
	}
}

// A convert failure has to survive the trip through Copy, otherwise the caller sees
// pgx's error and never learns which line broke.
func TestCopyOverTypedSurfacesParseError(t *testing.T) {
	rows, err := NewTyped(strings.NewReader("id;name\n;Alice\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	source := NewCopy(rows, 2, func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})

	drain(t, source)

	if err = source.Err(); !errors.Is(err, ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping ErrParse", err)
	}
	if got, want := source.Rows(), int64(0); got != want {
		t.Errorf("Rows() = %d, want %d", got, want)
	}
}
