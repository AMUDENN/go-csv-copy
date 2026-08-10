package decode

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

// person is a row in the shape the package expects: every field a string, typed
// later.
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

/*
rowSource is copyfrom.RowSource, restated.

Restated rather than imported: this package does not know that one exists, and a
test that reached for it would be the first thing to make that untrue. That Typed
satisfies the real interface is asserted over in copyfrom, which is the package
whose contract it is.
*/
type rowSource[T any] interface {
	Next() bool
	Value() T
	Err() error
}

func collect[D any](t *testing.T, src rowSource[D]) []D {
	t.Helper()

	var values []D
	for src.Next() {
		values = append(values, src.Value())
	}

	return values
}

func TestTypedMatchesColumnsByName(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

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
	t.Parallel()

	_, err := NewTyped(strings.NewReader("surname;patronymic\n"), toEntity)
	if !errors.Is(err, ErrMissingColumns) {
		t.Fatalf("error = %v, want ErrMissingColumns", err)
	}
	if !errors.Is(err, csvcopy.ErrParse) {
		t.Error("ErrMissingColumns must wrap csvcopy.ErrParse - callers key their file-level handling off it")
	}
	for _, name := range []string{"id", "name"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name the missing column %q", err, name)
		}
	}
}

func TestTypedAllowMissingColumns(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	t.Run("not a struct", func(t *testing.T) {
		t.Parallel()

		_, err := NewTyped(strings.NewReader("id\n"), func(*int) (int, error) { return 0, nil })
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
		if errors.Is(err, csvcopy.ErrParse) {
			t.Error("csvcopy.ErrSchema must not wrap csvcopy.ErrParse: no input file will ever fix it")
		}
	})

	t.Run("tagged field is not a string", func(t *testing.T) {
		t.Parallel()

		type bad struct {
			ID int `csv:"id"`
		}
		_, err := NewTyped(strings.NewReader("id\n"), func(*bad) (int, error) { return 0, nil })
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
	})

	t.Run("tagged field is unexported", func(t *testing.T) {
		t.Parallel()

		type bad struct {
			id string `csv:"id"` //nolint:unused // the tag on an unexported field is the point
		}
		_, err := NewTyped(strings.NewReader("id\n"), func(*bad) (int, error) { return 0, nil })
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
	})

	// An empty file must not hide a broken struct until a file with rows arrives.
	t.Run("reported for an empty file too", func(t *testing.T) {
		t.Parallel()

		type bad struct {
			ID int `csv:"id"`
		}
		_, err := NewTyped(strings.NewReader(""), func(*bad) (int, error) { return 0, nil })
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
	})

	t.Run("nil convert", func(t *testing.T) {
		t.Parallel()

		_, err := NewTyped[person, *entity](strings.NewReader("id;name\n"), nil)
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
	})
}

/*
A struct with no tag at all is ErrMissingColumns taken to the limit: every field
binds to nothing instead of one of them, so every row decodes as empty and nothing
objects. Rows would count the file's rows, Err would stay nil, and CopyFrom would
write a full table of empty values over a good one.

`json:"id"` where `csv:"id"` was meant is how this happens, and it is not a rare
slip.
*/
func TestTypedStructWithNoTagsIsRefused(t *testing.T) {
	t.Parallel()

	type noTags struct {
		A string `json:"a"` // json where csv was meant: the whole point of the test
		B string
	}

	convert := func(r *noTags) (string, error) { return r.A + r.B, nil }

	for _, input := range []string{"a;b\n1;2\n", ""} {
		_, err := NewTyped(strings.NewReader(input), convert)
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("NewTyped(%q) = %v, want csvcopy.ErrSchema", input, err)
		}
		if errors.Is(err, csvcopy.ErrParse) {
			t.Error("csvcopy.ErrSchema must not wrap csvcopy.ErrParse: no input file will ever fix a missing tag")
		}
		if !strings.Contains(err.Error(), "csv") {
			t.Errorf("error %q does not name the tag that is missing", err)
		}
	}
}

// WithTag("") is the same failure with an emptier cause: StructTag.Get("") answers
// "" for every field, so a fully tagged struct would bind nothing either.
func TestEmptyTagIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := NewTyped(strings.NewReader("id;name\n"), toEntity, WithTag("")); !errors.Is(err, csvcopy.ErrSchema) {
		t.Errorf("NewTyped = %v, want csvcopy.ErrSchema", err)
	}
	if _, err := NewReader(strings.NewReader("id;name\n"), WithTag("")); !errors.Is(err, csvcopy.ErrSchema) {
		t.Errorf("NewReader = %v, want csvcopy.ErrSchema", err)
	}
}

/*
A tag of nothing but whitespace is the empty tag arriving by another route.

The raw tag is checked before normalizing, so `csv:"   "` gets past that check and
comes out of NormalizeSpace as "" - which then matches a header column that
normalizes to "" as well. Two things nobody named, bound to each other, quietly.
Every neighbouring decision here goes the other way: WithTag("") is refused, an
empty column name is refused by ValidateColumns, so an empty name produced by the
normalizer cannot be the one that passes.
*/
func TestTagThatNormalizesToNothingIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []Option
	}{
		{name: "whitespace under the default normalizer"},
		{
			// Any normalizer can produce it, not just NormalizeSpace - a caller's
			// own strings.TrimSpace-and-lower does the same to a tag of spaces.
			name: "a custom normalizer that empties the tag",
			opts: []Option{WithNormalizeHeader(func(string) string { return "" })},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			type row struct {
				Value string `csv:"   "`
			}

			convert := func(r *row) (string, error) { return r.Value, nil }

			_, err := NewTyped(strings.NewReader("   ;b\nx;y\n"), convert, test.opts...)
			if !errors.Is(err, csvcopy.ErrSchema) {
				t.Fatalf("NewTyped = %v, want csvcopy.ErrSchema", err)
			}
			if errors.Is(err, csvcopy.ErrParse) {
				t.Error("csvcopy.ErrSchema must not wrap csvcopy.ErrParse: no file fixes a tag")
			}
			if !strings.Contains(err.Error(), "Value") {
				t.Errorf("error %q does not name the field", err)
			}
		})
	}
}

/*
convert is the caller's code, and it does not fail only because of the cell it
was given: it reaches lookup tables, caches and contexts. The package splits
errors by who can fix them, so it must not file "the network blinked" under "the
file is malformed" - a caller that quarantines on csvcopy.ErrParse would
quarantine a good file.
*/
func TestTypedConvertKeepsItsOwnErrorCategory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
		not  error
	}{
		{
			name: "csvcopy.ErrIO survives",
			err:  fmt.Errorf("%w: lookup table is down", csvcopy.ErrIO),
			want: csvcopy.ErrIO,
			not:  csvcopy.ErrParse,
		},
		{
			name: "csvcopy.ErrSchema survives",
			err:  fmt.Errorf("%w: the mapping is wrong", csvcopy.ErrSchema),
			want: csvcopy.ErrSchema,
			not:  csvcopy.ErrParse,
		},
		{
			name: "a cancelled context is csvcopy.ErrIO without being wrapped",
			err:  context.Canceled,
			want: csvcopy.ErrIO,
			not:  csvcopy.ErrParse,
		},
		{
			name: "a deadline is csvcopy.ErrIO too",
			err:  fmt.Errorf("call the pricing service: %w", context.DeadlineExceeded),
			want: csvcopy.ErrIO,
			not:  csvcopy.ErrParse,
		},
		{
			name: "anything else is the file's fault, as before",
			err:  errors.New("id is not a number"),
			want: csvcopy.ErrParse,
			not:  csvcopy.ErrIO,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			convert := func(*person) (*entity, error) { return nil, test.err }

			src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n"), convert)
			if err != nil {
				t.Fatalf("NewTyped: %v", err)
			}

			collect(t, src)

			got := src.Err()
			if !errors.Is(got, test.want) {
				t.Fatalf("Err() = %v, want it to wrap %v", got, test.want)
			}
			if errors.Is(got, test.not) {
				t.Errorf("Err() = %v, must not wrap %v", got, test.not)
			}
			if !errors.Is(got, test.err) {
				t.Errorf("Err() = %v, lost the convert error", got)
			}
			if !strings.Contains(got.Error(), "line 2") {
				t.Errorf("Err() = %q, does not name line 2", got)
			}
		})
	}
}

func TestTypedConvertErrorNamesTheLine(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n;Bob\n3;Carol\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	if got := collect(t, src); len(got) != 1 {
		t.Errorf("got %d rows before the error, want 1", len(got))
	}

	err = src.Err()
	if !errors.Is(err, csvcopy.ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping csvcopy.ErrParse", err)
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
	t.Parallel()

	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n2\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	if got := collect(t, src); len(got) != 1 {
		t.Errorf("got %d rows before the error, want 1", len(got))
	}

	err = src.Err()
	if !errors.Is(err, csvcopy.ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping csvcopy.ErrParse", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("Err() = %q, does not name line 3", err)
	}
	if src.Next() {
		t.Error("Next() returned true after an error")
	}
}

/*
A convert error names the physical line too.

It goes through Reader.wrap rather than through encoding/csv, so it is the path
where a record count would surface instead - and it is the path a caller is most
likely to act on, because a rejected value is something they have to go and look
at in the file.

	1  id;name
	2  "1
	3  x";Alice
	4  ;Bob      <- toEntity rejects an empty id
*/
func TestTypedConvertErrorNamesThePhysicalLine(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(strings.NewReader("id;name\n\"1\nx\";Alice\n;Bob\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	if got := collect(t, src); len(got) != 1 {
		t.Fatalf("got %d rows before the error, want 1", len(got))
	}

	err = src.Err()
	if !strings.Contains(err.Error(), "line 4") {
		t.Errorf("Err() = %q, want it to name line 4", err)
	}
	if got, want := src.Line(), 4; got != want {
		t.Errorf("Line() = %d, want %d", got, want)
	}
}

/*
A tag inside an embedded struct is refused rather than ignored.

taggedFields walks the fields of S and nothing else, so a tag one level down never
reaches the header matcher: no error, the field is never written, and the column
it named reaches the database as NULL on every row. That is exactly what
ErrMissingColumns exists to prevent, except silent - the only trace is the column
turning up in Unused.

Decoding into embedded structs would be a feature. Until it is one, the failure
has to be loud.
*/
func TestTypedEmbeddedTagsAreRefused(t *testing.T) {
	t.Parallel()

	type identity struct {
		ID string `csv:"id"`
	}
	type deeper struct {
		identity
	}

	t.Run("embedded struct", func(t *testing.T) {
		t.Parallel()

		type row struct {
			identity
			Name string `csv:"name"`
		}
		_, err := NewTyped(strings.NewReader("id;name\n"), func(*row) (int, error) { return 0, nil })
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
		if !strings.Contains(err.Error(), "identity") {
			t.Errorf("error %q does not name the embedded type", err)
		}
	})

	t.Run("embedded pointer", func(t *testing.T) {
		t.Parallel()

		type row struct {
			*identity
			Name string `csv:"name"`
		}
		_, err := NewTyped(strings.NewReader("id;name\n"), func(*row) (int, error) { return 0, nil })
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
	})

	// The tag may be any number of levels down; each one hides it just as well.
	t.Run("embedded two levels down", func(t *testing.T) {
		t.Parallel()

		type row struct {
			deeper
			Name string `csv:"name"`
		}
		_, err := NewTyped(strings.NewReader("id;name\n"), func(*row) (int, error) { return 0, nil })
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
		}
	})

	// Only a tag is refused. Embedding is otherwise the caller's business, and a
	// struct embedded for its methods must keep working.
	t.Run("embedded without tags is fine", func(t *testing.T) {
		t.Parallel()

		type plain struct {
			Note string
		}
		type row struct {
			plain
			Name string `csv:"name"`
		}
		if _, err := NewTyped(strings.NewReader("name\n"), func(*row) (int, error) { return 0, nil }); err != nil {
			t.Fatalf("NewTyped: %v", err)
		}
	})
}

// A struct that embeds itself through a pointer must not send the walk into an
// endless recursion.
func TestTypedSelfEmbeddingDoesNotRecurse(t *testing.T) {
	t.Parallel()

	type row struct {
		*row        //nolint:unused // embedding itself is the cycle the walk has to survive
		Name string `csv:"name"`
	}

	if _, err := NewTyped(strings.NewReader("name\n"), func(*row) (int, error) { return 0, nil }); err != nil {
		t.Fatalf("NewTyped: %v", err)
	}
}

// Two fields asking for one column is a copy-paste slip far more often than an
// intent, and nothing downstream can tell the two apart.
func TestTypedDuplicateTagIsRefused(t *testing.T) {
	t.Parallel()

	type row struct {
		A string `csv:"id"`
		B string `csv:"id"`
	}

	_, err := NewTyped(strings.NewReader("id\n"), func(*row) (int, error) { return 0, nil })
	if !errors.Is(err, csvcopy.ErrSchema) {
		t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
	}
	for _, name := range []string{"A", "B", "id"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %q", err, name)
		}
	}
}

// The normalizer runs before the duplicate check, so two tags that differ only in
// whitespace still collide - they name one column.
func TestTypedDuplicateTagAfterNormalization(t *testing.T) {
	t.Parallel()

	type row struct {
		A string `csv:"date of birth"`
		B string `csv:"date  of\tbirth"`
	}

	_, err := NewTyped(strings.NewReader("date of birth\n"), func(*row) (int, error) { return 0, nil })
	if !errors.Is(err, csvcopy.ErrSchema) {
		t.Fatalf("error = %v, want csvcopy.ErrSchema", err)
	}
}

/*
Truncated separates "the value was absent" from "the value was empty".

A tagged field is a string and cannot hold nil, so both arrive as "" - and in
Postgres a NULL and an empty string are different values. Without this the caller
has no way to know which one it is looking at.
*/
func TestTypedTruncatedFlag(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(
		strings.NewReader("id;name\n1;Alice\n2;\n3\n"),
		func(p *person) (person, error) { return *p, nil },
		WithVariableColumns(true),
	)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	want := []struct {
		name      string
		truncated bool
	}{
		{name: "Alice", truncated: false}, // both fields present
		{name: "", truncated: false},      // name present and empty
		{name: "", truncated: true},       // the record stopped before name
	}

	for i, expected := range want {
		if !src.Next() {
			t.Fatalf("row %d: Next() = false, Err() = %v", i+1, src.Err())
		}
		if got := src.Value().Name; got != expected.name {
			t.Errorf("row %d: Name = %q, want %q", i+1, got, expected.name)
		}
		if got := src.Truncated(); got != expected.truncated {
			t.Errorf("row %d: Truncated() = %v, want %v", i+1, got, expected.truncated)
		}
	}

	if src.Next() {
		t.Error("Next() = true after the last row")
	}
	if err = src.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// Without WithVariableColumns a short record is an error, so the flag can never be
// true - the reader stops before Typed ever sees the record.
func TestTypedTruncatedNeverTrueOnFixedWidth(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n2\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	for src.Next() {
		if src.Truncated() {
			t.Error("Truncated() = true without WithVariableColumns")
		}
	}
	if !errors.Is(src.Err(), csvcopy.ErrParse) {
		t.Errorf("Err() = %v, want csvcopy.ErrParse for the short record", src.Err())
	}
}

func TestTypedUnused(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(strings.NewReader("id;name;renamed_away;spare\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	want := []string{"renamed_away", "spare"}
	if got := src.Unused(); !reflect.DeepEqual(got, want) {
		t.Errorf("Unused() = %q, want %q", got, want)
	}
}

/*
A duplicated column a tag asks for is fatal. Binding to the first occurrence would
let the file's column order decide which values are loaded, and nothing downstream
could notice: both columns are called "id" and both hold plausible values.

The mirror image - two fields asking for one column - is already
csvcopy.ErrSchema. This is the same ambiguity from the file's side, so it is
csvcopy.ErrParse.
*/
func TestTypedDuplicateHeaderColumnIsFatal(t *testing.T) {
	t.Parallel()

	_, err := NewTyped(strings.NewReader("id;name;id\n1;Alice;2\n"), toEntity)
	if !errors.Is(err, ErrDuplicateColumns) {
		t.Fatalf("NewTyped = %v, want ErrDuplicateColumns", err)
	}
	if !errors.Is(err, csvcopy.ErrParse) {
		t.Error("ErrDuplicateColumns must wrap csvcopy.ErrParse: the file is what is ambiguous")
	}
	// Both positions, because "which two" is the question the caller opens the file
	// to answer.
	if !strings.Contains(err.Error(), "1, 3") {
		t.Errorf("error %q does not name both columns", err)
	}
}

/*
Normalizing is what makes this reachable without a literally duplicated header:
"a  b" and "a b" are one name after NormalizeSpace, and the collapse happens inside
this package rather than in the file.
*/
func TestTypedColumnsCollapsedByNormalizingAreFatal(t *testing.T) {
	t.Parallel()

	type row struct {
		Value string `csv:"a b"`
	}

	convert := func(r *row) (string, error) { return r.Value, nil }

	_, err := NewTyped(strings.NewReader("a  b;a b\n1;2\n"), convert)
	if !errors.Is(err, ErrDuplicateColumns) {
		t.Fatalf("NewTyped = %v, want ErrDuplicateColumns", err)
	}
}

// A duplicate no tag asks for is not ambiguous: nothing binds to it, and it stays
// in Unused exactly as it always did.
func TestTypedDuplicateColumnNoTagAsksForIsUnused(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(strings.NewReader("id;name;spare;spare\n1;Alice;x;y\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	got := collect(t, src)
	want := []*entity{{ID: "1", Name: "Alice"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values = %#v, want %#v", got, want)
	}
	if unused := src.Unused(); !reflect.DeepEqual(unused, []string{"spare", "spare"}) {
		t.Errorf("Unused() = %q, want [spare spare]", unused)
	}
}

// The struct is reused across the file, so a row that leaves a field out must not
// inherit the previous row's value.
func TestTypedDoesNotLeakValuesBetweenRows(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

// Columns are matched by name, so the file may hold them in any order. Handing the
// result to pgx is copyfrom's business, not this package's - see ExampleNewCopy.
func ExampleNewTyped() {
	const file = "name;id\nAlice;1\nBob;2\n"

	rows, err := NewTyped(strings.NewReader(file), toEntity)
	if err != nil {
		panic(err)
	}

	for e := range rows.All() {
		fmt.Println(e.ID, e.Name)
	}
	if err = rows.Err(); err != nil {
		panic(err)
	}

	// Output:
	// 1 Alice
	// 2 Bob
}

/*
A record wider than the header loses its tail here too, and unlike in Raw there is
no trace of it in the decoded value at all.

A tag can only ask for a column the header names, so values past the last one bind
to nothing: the struct looks exactly as it would for a well-formed row, and
Truncated is about the other end of the record. Since a record that is too wide is
the usual shape of a misread delimiter - the very signal WithVariableColumns turns
off - the count has to be available from somewhere.
*/
func TestTypedExtra(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		want      []*entity
		truncated []bool
		extra     []int
	}{
		{
			name:      "wide record",
			input:     "id;name\n1;Alice;junk;more\n",
			want:      []*entity{{ID: "1", Name: "Alice"}},
			truncated: []bool{false},
			extra:     []int{2},
		},
		{
			name:      "short record reports the other end",
			input:     "id;name\n1\n",
			want:      []*entity{{ID: "1"}},
			truncated: []bool{true},
			extra:     []int{0},
		},
		{
			name:      "exact record reports neither",
			input:     "id;name\n1;Alice\n",
			want:      []*entity{{ID: "1", Name: "Alice"}},
			truncated: []bool{false},
			extra:     []int{0},
		},
		{
			// Both flags belong to the current row and must not survive into the
			// next one.
			name:      "flags do not leak between rows",
			input:     "id;name\n1;Alice;junk\n2\n3;Carol\n",
			want:      []*entity{{ID: "1", Name: "Alice"}, {ID: "2"}, {ID: "3", Name: "Carol"}},
			truncated: []bool{false, true, false},
			extra:     []int{1, 0, 0},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			src, err := NewTyped(strings.NewReader(test.input), toEntity, WithVariableColumns(true))
			if err != nil {
				t.Fatalf("NewTyped: %v", err)
			}

			var got []*entity
			for row := 0; src.Next(); row++ {
				got = append(got, src.Value())

				if n := src.Extra(); n != test.extra[row] {
					t.Errorf("row %d: Extra() = %d, want %d", row, n, test.extra[row])
				}
				if v := src.Truncated(); v != test.truncated[row] {
					t.Errorf("row %d: Truncated() = %v, want %v", row, v, test.truncated[row])
				}
			}

			if err = src.Err(); err != nil {
				t.Fatalf("Err() = %v, want nil", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("values = %#v, want %#v", got, test.want)
			}
		})
	}
}

// Without the option a record of the wrong width is an error, so Extra never has
// anything to report - the same shape as Truncated.
func TestTypedExtraIsQuietOnFixedWidth(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n2;Bob;junk\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	for src.Next() {
		if got := src.Extra(); got != 0 {
			t.Errorf("Extra() = %d on a fixed-width read", got)
		}
	}
	if !errors.Is(src.Err(), csvcopy.ErrParse) {
		t.Errorf("Err() = %v, want csvcopy.ErrParse for the wide record", src.Err())
	}
}
