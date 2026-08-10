package csvcopy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

/*
copyFromSource is pgx.CopyFromSource, restated.

The point of the assertions below is that this package satisfies pgx without
importing it - Go interfaces are structural, so the method set is the whole
contract. If pgx ever changes that method set, these break and the README stops
being true.
*/
type copyFromSource interface {
	Next() bool
	Values() ([]any, error)
	Err() error
}

var (
	_ copyFromSource = (*Raw)(nil)
	_ copyFromSource = (*Copy[int])(nil)
	_ RowSource[int] = (*Typed[struct{}, int])(nil)
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

/*
plain flattens a row to comparable values whatever representation the source uses.

Raw hands out *string by default and string under WithPointerValues(false). A test
about which values were read should not have to care which of the two it got; the
tests that are about the representation assert it directly.
*/
func plain(t *testing.T, values []any) []any {
	t.Helper()

	out := make([]any, len(values))
	for i, value := range values {
		switch typed := value.(type) {
		case nil:
			out[i] = nil
		case string:
			out[i] = typed
		case *string:
			if typed == nil {
				out[i] = nil

				continue
			}
			out[i] = *typed
		default:
			t.Fatalf("value %d is %T, want string, *string or nil", i, value)
		}
	}

	return out
}

// drainPlain is drain for a source of text, comparing values rather than pointers.
func drainPlain(t *testing.T, src copyFromSource) [][]any {
	t.Helper()

	var rows [][]any
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			t.Fatalf("Values: %v", err)
		}
		rows = append(rows, plain(t, values))
	}

	return rows
}

func TestRaw(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		opts    []Option
		columns []string
		rows    [][]any
	}{
		{
			name:    "schema comes from the header",
			input:   "id;name;note\n1;Alice;-\n2;Bob;\n",
			columns: []string{"id", "name", "note"},
			rows: [][]any{
				{"1", "Alice", "-"},
				{"2", "Bob", ""},
			},
		},
		{
			name:    "empty file has no columns and no rows",
			input:   "",
			columns: nil,
			rows:    nil,
		},
		{
			name:    "header only",
			input:   "id;name\n",
			columns: []string{"id", "name"},
			rows:    nil,
		},
		{
			name:    "short record pads with NULL",
			input:   "a;b;c\n1;2\n",
			opts:    []Option{WithVariableColumns(true)},
			columns: []string{"a", "b", "c"},
			rows:    [][]any{{"1", "2", nil}},
		},
		{
			name:    "long record is truncated",
			input:   "a;b\n1;2;3\n",
			opts:    []Option{WithVariableColumns(true)},
			columns: []string{"a", "b"},
			rows:    [][]any{{"1", "2"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src, err := NewRaw(strings.NewReader(test.input), test.opts...)
			if err != nil {
				t.Fatalf("NewRaw: %v", err)
			}
			if got := src.Columns(); !reflect.DeepEqual(got, test.columns) {
				t.Errorf("Columns() = %q, want %q", got, test.columns)
			}
			if got := drainPlain(t, src); !reflect.DeepEqual(got, test.rows) {
				t.Errorf("rows = %#v, want %#v", got, test.rows)
			}
			if err = src.Err(); err != nil {
				t.Errorf("Err() = %v, want nil", err)
			}
			if got, want := src.Rows(), int64(len(test.rows)); got != want {
				t.Errorf("Rows() = %d, want %d", got, want)
			}
		})
	}
}

// A short record is an error unless it was explicitly allowed: a row of the wrong
// width usually means the delimiter is misread, and loading shifted data is worse
// than failing.
func TestRawRejectsShortRecordByDefault(t *testing.T) {
	src, err := NewRaw(strings.NewReader("a;b;c\n1;2;3\n4;5\n"))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	if rows := drainPlain(t, src); len(rows) != 1 {
		t.Errorf("got %d rows before the error, want 1", len(rows))
	}

	err = src.Err()
	if !errors.Is(err, ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping ErrParse", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("Err() = %q, does not name line 3", err)
	}
	if src.Next() {
		t.Error("Next() returned true after an error")
	}
}

func TestRawPointerValues(t *testing.T) {
	src, err := NewRaw(
		strings.NewReader("a;b;c\n1;2;3\n4;5\n"),
		WithPointerValues(true),
		WithVariableColumns(true),
	)
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	want := [][]string{{"1", "2", "3"}, {"4", "5"}}

	for row := 0; src.Next(); row++ {
		values, err := src.Values()
		if err != nil {
			t.Fatalf("Values: %v", err)
		}
		if len(values) != 3 {
			t.Fatalf("row %d: %d values, want 3", row, len(values))
		}

		for i, value := range values {
			if i >= len(want[row]) {
				// A cell the record stopped short of is NULL, not a pointer to "".
				if value != nil {
					t.Errorf("row %d col %d = %#v, want nil", row, i, value)
				}
				continue
			}

			pointer, ok := value.(*string)
			if !ok {
				t.Fatalf("row %d col %d is %T, want *string", row, i, value)
			}
			if *pointer != want[row][i] {
				t.Errorf("row %d col %d = %q, want %q", row, i, *pointer, want[row][i])
			}
		}
	}

	if err = src.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// The point of the mode: the pointers are handed out once and never reboxed, so
// every row must come back through the very same pointers.
func TestRawPointerValuesReusePointers(t *testing.T) {
	src, err := NewRaw(strings.NewReader("a;b\n1;2\n3;4\n"), WithPointerValues(true))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	var first *string
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			t.Fatalf("Values: %v", err)
		}

		pointer, ok := values[0].(*string)
		if !ok {
			t.Fatalf("col 0 is %T, want *string", values[0])
		}
		if first == nil {
			first = pointer
			continue
		}
		if pointer != first {
			t.Error("Values() handed out a new pointer instead of writing through the old one")
		}
		if *pointer != "3" {
			t.Errorf("second row col 0 = %q, want 3", *pointer)
		}
	}
}

/*
The escape hatch has to stay usable.

Pointer values are the default because they are cheaper and pgx cannot tell the
difference - verified against a real Postgres, not assumed. Anyone driving the source
by hand can still ask for plain strings, and that has to keep working.
*/
func TestRawWithoutPointerValuesGivesStrings(t *testing.T) {
	src, err := NewRaw(
		strings.NewReader("a;b;c\n1;2\n"),
		WithPointerValues(false),
		WithVariableColumns(true),
	)
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}
	if !src.Next() {
		t.Fatalf("Next() = false, Err() = %v", src.Err())
	}

	values, err := src.Values()
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if got, ok := values[0].(string); !ok || got != "1" {
		t.Errorf("values[0] = %#v, want the string \"1\"", values[0])
	}
	// A value the record never reached is still NULL, not a pointer to "".
	if values[2] != nil {
		t.Errorf("values[2] = %#v, want nil", values[2])
	}
}

// pgx encodes a row before asking for the next one, so one slice is reused. This
// pins that contract: the same backing array comes back every time.
func TestRawValuesReuseOneSlice(t *testing.T) {
	src, err := NewRaw(strings.NewReader("a;b\n1;2\n3;4\n"))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	var first []any
	for src.Next() {
		values, err := src.Values()
		if err != nil {
			t.Fatalf("Values: %v", err)
		}
		if first == nil {
			first = values
			continue
		}
		if &values[0] != &first[0] {
			t.Error("Values() allocated a new slice instead of reusing one")
		}
		if got, want := plain(t, values)[0], "3"; got != want {
			t.Errorf("values[0] = %v, want %v", got, want)
		}
	}
}
