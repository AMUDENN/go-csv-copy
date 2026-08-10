package copyfrom

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	csvcopy "github.com/AMUDENN/go-csv-copy"
	"github.com/AMUDENN/go-csv-copy/decode"
)

/*
copyFromSource is pgx.CopyFromSource, restated.

The assertions below are the point of this package: it satisfies pgx without
importing it - Go interfaces are structural, so the method set is the whole
contract. If pgx ever changes that method set, these break and the README stops
being true.

decode.Typed satisfying RowSource is asserted here rather than there for the same
reason the two packages are separate: decode does not know this package exists,
and nothing about it should have to.
*/
type copyFromSource interface {
	Next() bool
	Values() ([]any, error)
	Err() error
}

var (
	_ copyFromSource = (*Copy[int])(nil)
	_ RowSource[int] = (*decode.Typed[struct{}, int])(nil)
)

func drain(t *testing.T, src copyFromSource) [][]any {
	t.Helper()

	var rows [][]any
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			t.Fatalf("Values: %v", err)
		}
		rows = append(rows, append([]any(nil), values...))
	}

	return rows
}

// entity stands in for whatever the program works with once a row has been typed.
// Copy never sees the CSV; it only lays a value out across columns.
type entity struct {
	ID   string
	Name string
}

type person struct {
	ID   string `csv:"id"`
	Name string `csv:"name"`
}

func toEntity(p *person) (*entity, error) {
	if p.ID == "" {
		return nil, errors.New("id is required")
	}

	return &entity{ID: p.ID, Name: p.Name}, nil
}

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

// newCopy is NewCopy for the cases whose wiring is known good, so that the tests
// below stay about what Copy does rather than about its constructor.
func newCopy[T any](t *testing.T, src RowSource[T], columns int, encode func(dst []any, item T) []any) *Copy[T] {
	t.Helper()

	source, err := NewCopy(src, columns, encode)
	if err != nil {
		t.Fatalf("NewCopy: %v", err)
	}

	return source
}

func TestCopy(t *testing.T) {
	t.Parallel()

	src := &slice[*entity]{items: []*entity{{ID: "1", Name: "Alice"}, {ID: "2", Name: "Bob"}}}

	source := newCopy(t, src, 2, func(dst []any, e *entity) []any {
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
	t.Parallel()

	want := errors.New("boom")
	source := newCopy(t, &slice[int]{err: want}, 1, func(dst []any, i int) []any {
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
	t.Parallel()

	src := &slice[int]{items: []int{1, 2, 3, 4}}

	source := newCopy(t, src, 1, func(dst []any, i int) []any {
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
	t.Parallel()

	source := newCopy(t, &slice[int]{items: []int{7}}, -1, func(dst []any, i int) []any {
		return append(dst, i)
	})

	if got := drain(t, source); !reflect.DeepEqual(got, [][]any{{7}}) {
		t.Errorf("rows = %#v, want [[7]]", got)
	}
}

/*
Nil wiring is csvcopy.ErrSchema, not a panic.

A nil src or encode is the same class of mistake as a nil reader or convert, and
the package has one answer for that class. Reporting it two different ways would
force a caller to guard the constructors two different ways.
*/
func TestCopyNilArguments(t *testing.T) {
	t.Parallel()

	t.Run("nil src", func(t *testing.T) {
		t.Parallel()

		_, err := NewCopy[int](nil, 1, func(dst []any, i int) []any { return dst })
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
		if errors.Is(err, csvcopy.ErrParse) {
			t.Error("csvcopy.ErrSchema must not wrap csvcopy.ErrParse: no input file will ever fix it")
		}
	})

	t.Run("nil encode", func(t *testing.T) {
		t.Parallel()

		_, err := NewCopy[int](&slice[int]{}, 1, nil)
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
	})
}

/*
Values before the first Next is csvcopy.ErrSchema, not a row.

There is no current value then, so encode runs on the zero one and hands back a
row of empty values that looks exactly like a real one - and Values returns an
error, so a caller doing everything right sees nothing wrong. pgx.CopyFrom never
gets here because it calls Next first, but Copy is exported and can be driven by
hand, which is the only reason it is reachable at all.
*/
func TestCopyValuesBeforeNext(t *testing.T) {
	t.Parallel()

	source := newCopy(t, &slice[*entity]{items: []*entity{{ID: "1"}}}, 1,
		func(dst []any, e *entity) []any { return append(dst, e.ID) })

	values, err := source.Values()
	if !errors.Is(err, csvcopy.ErrSchema) {
		t.Fatalf("Values() error = %v, want csvcopy.ErrSchema", err)
	}
	if values != nil {
		t.Errorf("Values() = %#v, want nil", values)
	}

	if !source.Next() {
		t.Fatal("Next() = false, want true")
	}
	if values, err = source.Values(); err != nil {
		t.Fatalf("Values() after Next: %v", err)
	}
	if !reflect.DeepEqual(values, []any{"1"}) {
		t.Errorf("Values() = %#v, want [1]", values)
	}
}

/*
Line and Record reach through to the source.

The advice for a failed CopyFrom is to read the source's error first, because that
is the one that names the line. That advice only works if the object you have can
be asked, and the obvious code hands the Typed straight to NewCopy and keeps no
other reference to it.
*/
func TestCopyForwardsLineAndRecord(t *testing.T) {
	t.Parallel()

	rows, err := decode.NewTyped(strings.NewReader("id;name\n1;Alice\n2;Bob\n"), toEntity)
	if err != nil {
		t.Fatalf("decode.NewTyped: %v", err)
	}

	source := newCopy(t, rows, 2, func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})

	if !source.Next() {
		t.Fatalf("Next() = false, Err() = %v", source.Err())
	}
	if got, want := source.Line(), rows.Line(); got != want {
		t.Errorf("Line() = %d, want %d - the same line Typed reports", got, want)
	}
	if got, want := source.Line(), 2; got != want {
		t.Errorf("Line() = %d, want %d", got, want)
	}
	if got, want := source.Record(), []string{"1", "Alice"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Record() = %q, want %q", got, want)
	}
}

// A source with no lines answers zero and nil rather than forcing every source to
// declare methods it cannot implement.
func TestCopyLineAndRecordWithoutAWillingSource(t *testing.T) {
	t.Parallel()

	source := newCopy(t, &slice[int]{items: []int{1}}, 1, func(dst []any, i int) []any {
		return append(dst, i)
	})

	if !source.Next() {
		t.Fatal("Next() = false, want a row")
	}
	if got := source.Line(); got != 0 {
		t.Errorf("Line() = %d, want 0", got)
	}
	if got := source.Record(); got != nil {
		t.Errorf("Record() = %q, want nil", got)
	}
}

// The whole point of the layering: Typed decodes, Copy lays out, and neither knows
// about the other's concerns.
func TestCopyOverTyped(t *testing.T) {
	t.Parallel()

	rows, err := decode.NewTyped(strings.NewReader("id;name\n1;Alice\n"), toEntity)
	if err != nil {
		t.Fatalf("decode.NewTyped: %v", err)
	}

	source := newCopy(t, rows, 2, func(dst []any, e *entity) []any {
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
	t.Parallel()

	rows, err := decode.NewTyped(strings.NewReader("id;name\n;Alice\n"), toEntity)
	if err != nil {
		t.Fatalf("decode.NewTyped: %v", err)
	}

	source := newCopy(t, rows, 2, func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})

	drain(t, source)

	if err = source.Err(); !errors.Is(err, csvcopy.ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping csvcopy.ErrParse", err)
	}
	if got, want := source.Rows(), int64(0); got != want {
		t.Errorf("Rows() = %d, want %d", got, want)
	}
}

// The two layers meeting: decode turns the file into values, this package lays a
// value out across the columns pgx.CopyFrom is given. Neither knows the other's
// concerns - swap decode for a source over XLSX and nothing here changes.
func ExampleNewCopy() {
	const file = "name;id\nAlice;1\nBob;2\n"

	rows, err := decode.NewTyped(strings.NewReader(file), toEntity)
	if err != nil {
		panic(err)
	}

	source, err := NewCopy(rows, 2, func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})
	if err != nil {
		panic(err)
	}

	// tx.CopyFrom(ctx, pgx.Identifier{"people"}, []string{"id", "name"}, source)
	for source.Next() {
		values, _ := source.Values()
		fmt.Println(values...)
	}
	if err = source.Err(); err != nil {
		panic(err)
	}

	// Output:
	// 1 Alice
	// 2 Bob
}
