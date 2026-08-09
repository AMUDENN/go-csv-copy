package csvcopy

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// person is a row in the shape the package expects: every field a string, typed later.
type person struct {
	ID      string `csv:"id"`
	Name    string `csv:"name"`
	Comment string `csv:"-"`
	// An unexported field with no tag must be skipped, not rejected - only a tag
	// on one is an error.
	skipped string //nolint:unused // its presence is what the test checks
	Ignored string
}

type entity struct {
	ID   string
	Name string
}

func toEntity(p *person) (*entity, error) {
	if p.ID == "" {
		return nil, errors.New("id is required")
	}

	return &entity{ID: p.ID, Name: p.Name}, nil
}

func collect[D any](t *testing.T, src RowSource[D]) []D {
	t.Helper()

	var values []D
	for src.Next() {
		values = append(values, src.Value())
	}

	return values
}

func TestTypedMatchesColumnsByName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []*entity
	}{
		{
			name:  "in order",
			input: "id;name\n1;Alice\n2;Bob\n",
			want:  []*entity{{ID: "1", Name: "Alice"}, {ID: "2", Name: "Bob"}},
		},
		{
			name:  "reordered",
			input: "name;id\nAlice;1\n",
			want:  []*entity{{ID: "1", Name: "Alice"}},
		},
		{
			name:  "extra column is ignored",
			input: "id;extra;name\n1;junk;Alice\n",
			want:  []*entity{{ID: "1", Name: "Alice"}},
		},
		{
			name:  "header only",
			input: "id;name\n",
			want:  nil,
		},
		{
			name:  "empty file",
			input: "",
			want:  nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src, err := NewTyped(strings.NewReader(test.input), toEntity)
			if err != nil {
				t.Fatalf("NewTyped: %v", err)
			}

			got := collect(t, src)
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("values = %#v, want %#v", got, test.want)
			}
			if err = src.Err(); err != nil {
				t.Errorf("Err() = %v, want nil", err)
			}
			if got, want := src.Rows(), int64(len(test.want)); got != want {
				t.Errorf("Rows() = %d, want %d", got, want)
			}
		})
	}
}

/*
A tagged column the header lacks must fail, and fail naming every one of them:
otherwise the field reads as empty on every row and reaches the database as NULL,
wiping the column it was meant to fill.
*/
func TestTypedMissingColumns(t *testing.T) {
	_, err := NewTyped(strings.NewReader("surname;patronymic\n"), toEntity)
	if !errors.Is(err, ErrMissingColumns) {
		t.Fatalf("error = %v, want ErrMissingColumns", err)
	}
	if !errors.Is(err, ErrParse) {
		t.Error("ErrMissingColumns must wrap ErrParse - callers key their file-level handling off it")
	}
	for _, name := range []string{"id", "name"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name the missing column %q", err, name)
		}
	}
}

func TestTypedAllowMissingColumns(t *testing.T) {
	src, err := NewTyped(strings.NewReader("id\n7\n"), toEntity, WithAllowMissingColumns(true))
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	got := collect(t, src)
	want := []*entity{{ID: "7", Name: ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values = %#v, want %#v", got, want)
	}
}

func TestTypedSchemaErrors(t *testing.T) {
	t.Run("not a struct", func(t *testing.T) {
		_, err := NewTyped(strings.NewReader("id\n"), func(*int) (int, error) { return 0, nil })
		if !errors.Is(err, ErrSchema) {
			t.Fatalf("error = %v, want ErrSchema", err)
		}
		if errors.Is(err, ErrParse) {
			t.Error("ErrSchema must not wrap ErrParse: no input file will ever fix it")
		}
	})

	t.Run("tagged field is not a string", func(t *testing.T) {
		type bad struct {
			ID int `csv:"id"`
		}
		_, err := NewTyped(strings.NewReader("id\n"), func(*bad) (int, error) { return 0, nil })
		if !errors.Is(err, ErrSchema) {
			t.Fatalf("error = %v, want ErrSchema", err)
		}
	})

	t.Run("tagged field is unexported", func(t *testing.T) {
		type bad struct {
			id string `csv:"id"` //nolint:unused // the tag on an unexported field is the point
		}
		_, err := NewTyped(strings.NewReader("id\n"), func(*bad) (int, error) { return 0, nil })
		if !errors.Is(err, ErrSchema) {
			t.Fatalf("error = %v, want ErrSchema", err)
		}
	})

	// An empty file must not hide a broken struct until a file with rows arrives.
	t.Run("reported for an empty file too", func(t *testing.T) {
		type bad struct {
			ID int `csv:"id"`
		}
		_, err := NewTyped(strings.NewReader(""), func(*bad) (int, error) { return 0, nil })
		if !errors.Is(err, ErrSchema) {
			t.Fatalf("error = %v, want ErrSchema", err)
		}
	})

	t.Run("nil convert", func(t *testing.T) {
		_, err := NewTyped[person, *entity](strings.NewReader("id;name\n"), nil)
		if !errors.Is(err, ErrSchema) {
			t.Fatalf("error = %v, want ErrSchema", err)
		}
	})
}

func TestTypedConvertErrorNamesTheLine(t *testing.T) {
	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n;Bob\n3;Carol\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	if got := collect(t, src); len(got) != 1 {
		t.Errorf("got %d rows before the error, want 1", len(got))
	}

	err = src.Err()
	if !errors.Is(err, ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping ErrParse", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("Err() = %q, does not name line 3", err)
	}
	if !strings.Contains(err.Error(), "id is required") {
		t.Errorf("Err() = %q, lost the convert error", err)
	}
	if src.Next() {
		t.Error("Next() returned true after an error")
	}
}

// A malformed record reaches the caller through Typed too, not only a convert
// failure: the reader's error has to survive the decode step untouched.
func TestTypedMalformedRecord(t *testing.T) {
	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n2\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	if got := collect(t, src); len(got) != 1 {
		t.Errorf("got %d rows before the error, want 1", len(got))
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

func TestTypedUnused(t *testing.T) {
	src, err := NewTyped(strings.NewReader("id;name;renamed_away;spare\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	want := []string{"renamed_away", "spare"}
	if got := src.Unused(); !reflect.DeepEqual(got, want) {
		t.Errorf("Unused() = %q, want %q", got, want)
	}
}

// A duplicated header column binds once; the second copy shows up as unused rather
// than silently shadowing the first.
func TestTypedDuplicateHeaderColumn(t *testing.T) {
	src, err := NewTyped(strings.NewReader("id;name;id\n1;Alice;2\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	got := collect(t, src)
	want := []*entity{{ID: "1", Name: "Alice"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values = %#v, want %#v", got, want)
	}
	if unused := src.Unused(); !reflect.DeepEqual(unused, []string{"id"}) {
		t.Errorf("Unused() = %q, want [id]", unused)
	}
}

// The struct is reused across the file, so a row that leaves a field out must not
// inherit the previous row's value.
func TestTypedDoesNotLeakValuesBetweenRows(t *testing.T) {
	src, err := NewTyped(
		strings.NewReader("id;name\n1;Alice\n2;\n"),
		func(p *person) (person, error) { return *p, nil },
	)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	got := collect(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[1].Name != "" {
		t.Errorf("second row Name = %q, want empty - it inherited the first row's value", got[1].Name)
	}
}

/*
The mirror image of the test above: a field no column bound to is never written by
the package, so what convert leaves in one it sees again on the next row.

Pinned rather than fixed. Zeroing the struct per row would take away the only
scratch space convert has between rows, and callers who use it that way would find
out at runtime. The documented rule is that the package writes bound fields and
touches nothing else.
*/
func TestTypedLeavesUnboundFieldsToConvert(t *testing.T) {
	src, err := NewTyped(
		strings.NewReader("id;name\n1;Alice\n2;Bob\n"),
		func(p *person) (person, error) {
			if p.Comment == "" {
				p.Comment = "first seen at " + p.ID
			}

			return *p, nil
		},
	)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	got := collect(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if want := "first seen at 1"; got[1].Comment != want {
		t.Errorf("second row Comment = %q, want %q - an unbound field must survive the row",
			got[1].Comment, want)
	}
}

func TestTypedHeaderAndTag(t *testing.T) {
	type row struct {
		A string `column:"a"`
		B string `csv:"a"`
	}

	src, err := NewTyped(
		strings.NewReader("a\nx\n"),
		func(r *row) (row, error) { return *r, nil },
		WithTag("column"),
	)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}
	if got, want := src.Header(), []string{"a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Header() = %q, want %q", got, want)
	}

	got := collect(t, src)
	if len(got) != 1 || got[0].A != "x" || got[0].B != "" {
		t.Errorf("values = %#v, want A=x B=\"\"", got)
	}
}

// Tags go through the same normalizer as the header, so a wrapped column name in
// the file still matches a tag written on one line.
func TestTypedNormalizesTags(t *testing.T) {
	type row struct {
		Birth string `csv:"date of birth"`
	}

	src, err := NewTyped(
		strings.NewReader("\"date of \n birth\"\n1990-01-01\n"),
		func(r *row) (string, error) { return r.Birth, nil },
	)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}
	if got := collect(t, src); !reflect.DeepEqual(got, []string{"1990-01-01"}) {
		t.Errorf("values = %q, want [1990-01-01]", got)
	}
}

func ExampleNewTyped() {
	const file = "name;id\nAlice;1\nBob;2\n"

	rows, err := NewTyped(strings.NewReader(file), toEntity)
	if err != nil {
		panic(err)
	}

	source := NewCopy(rows, 2, func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})

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
