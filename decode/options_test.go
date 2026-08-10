package decode

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

// failingReader stands in for a stream that breaks before any data arrives.
type failingReader struct {
	err error
}

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestWithLazyQuotes(t *testing.T) {
	t.Parallel()

	const input = "a;b\nx\"y;z\n"

	t.Run("rejected by default", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(strings.NewReader(input))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if _, err = reader.Read(); !errors.Is(err, csvcopy.ErrParse) {
			t.Fatalf("Read error = %v, want an error wrapping csvcopy.ErrParse", err)
		}
	})

	t.Run("tolerated when on", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(strings.NewReader(input), WithLazyQuotes(true))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got := readAll(t, reader); len(got) != 1 {
			t.Errorf("records = %q, want one record", got)
		}
	})
}

/*
An unclosed quote is the reason lazy quoting is off by default.

Verified against encoding/csv, not assumed: with lazy quoting on, the parser reads
to EOF looking for the closing quote and the rest of the file becomes one field.
What the caller then sees depends on the field count, which is why the default
matters so much - the two subtests below are the same input with three different
outcomes.
*/
func TestWithLazyQuotesUnclosedQuote(t *testing.T) {
	t.Parallel()

	const input = "a;b\n\"unclosed;still going\nnext;row\n"

	t.Run("default reports the quote", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(strings.NewReader(input))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}

		_, err = reader.Read()
		if !errors.Is(err, csvcopy.ErrParse) {
			t.Fatalf("Read error = %v, want an error wrapping csvcopy.ErrParse", err)
		}
		if !strings.Contains(err.Error(), "quote") {
			t.Errorf("error %q does not mention the quote - it names the wrong cause", err)
		}
		if !strings.Contains(err.Error(), "line 2") {
			t.Errorf("error %q does not name line 2, where the quote opened", err)
		}
		if reader.Err() == nil {
			t.Error("Err() = nil after a failed Read")
		}
	})

	/*
		With lazy quoting on the diagnosis is lost. The record swallows the rest of
		the file, and the only complaint left is about the field count - on the line
		the file ended on, not the line the quote opened on.
	*/
	t.Run("lazy quoting hides it behind a field count", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(strings.NewReader(input), WithLazyQuotes(true))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}

		_, err = reader.Read()
		if !errors.Is(err, csvcopy.ErrParse) {
			t.Fatalf("Read error = %v, want an error wrapping csvcopy.ErrParse", err)
		}
		if strings.Contains(err.Error(), "quote") {
			t.Errorf("error %q mentions the quote; this subtest exists because it does not", err)
		}
	})

	/*
		And with a variable width there is nothing left to complain about: the rest
		of the file is one value and the load succeeds. This is the silent data loss
		the default exists to prevent.
	*/
	t.Run("lazy quoting plus variable columns loses the file silently", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(
			strings.NewReader(input),
			WithLazyQuotes(true),
			WithVariableColumns(true),
		)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}

		records := readAll(t, reader)
		if len(records) != 1 {
			t.Fatalf("records = %q, want the rest of the file collapsed into one", records)
		}
		if !strings.Contains(records[0][0], "next;row") {
			t.Errorf("record = %q, want it to have swallowed the following row", records[0])
		}
		if err = reader.Err(); err != nil {
			t.Errorf("Err() = %v; this subtest exists because there is no error", err)
		}
	})
}

/*
A delimiter encoding/csv will not take is a bug in the calling code, not a bad
file, so it must be csvcopy.ErrSchema and it must not wait for the first row.

Left as csvcopy.ErrParse it would fire the caller's quarantine-the-file path on an
argument no file can influence, and it would turn an empty input into an error -
the one thing the package promises never to do.
*/
func TestWithCommaRejectsUnusableDelimiters(t *testing.T) {
	t.Parallel()

	for _, comma := range []rune{0, '"', '\r', '\n', utf8.RuneError} {
		t.Run(strconv.QuoteRune(comma), func(t *testing.T) {
			t.Parallel()

			for _, input := range []string{"a;b\n1;2\n", ""} {
				_, err := NewReader(strings.NewReader(input), WithComma(comma))
				if !errors.Is(err, csvcopy.ErrSchema) {
					t.Fatalf("input %q: error = %v, want csvcopy.ErrSchema", input, err)
				}
				if errors.Is(err, csvcopy.ErrParse) {
					t.Errorf("input %q: csvcopy.ErrSchema must not wrap csvcopy.ErrParse", input)
				}
			}
		})
	}
}

// A multibyte delimiter is unusual but legal, and rejecting it would be a
// regression: only the runes encoding/csv refuses are refused.
func TestWithCommaAcceptsMultibyte(t *testing.T) {
	t.Parallel()

	reader, err := NewReader(strings.NewReader("a→b\n1→2\n"), WithComma('→'))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got, want := reader.Columns(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Columns() = %q, want %q", got, want)
	}
}

func TestWithComment(t *testing.T) {
	t.Parallel()

	t.Run("above the header", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(
			strings.NewReader("# generated by something\na;b\n1;2\n"),
			WithComment('#'),
		)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got, want := reader.Columns(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Columns() = %q, want %q", got, want)
		}
		if got, want := readAll(t, reader), [][]string{{"1", "2"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("records = %q, want %q", got, want)
		}
	})

	// This is what WithHeaderRow cannot do: it drops a fixed count at the top.
	t.Run("between rows", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(
			strings.NewReader("a;b\n1;2\n# a note\n3;4\n"),
			WithComment('#'),
		)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}

		want := [][]string{{"1", "2"}, {"3", "4"}}
		if got := readAll(t, reader); !reflect.DeepEqual(got, want) {
			t.Errorf("records = %q, want %q", got, want)
		}
	})

	// A skipped line still occupies a line of the file, and the number in an error
	// has to be the one the caller opens the file at.
	t.Run("skipped lines do not shift the line number", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(
			strings.NewReader("a;b\n# one\n# two\n1;2\n# three\n3\n"),
			WithComment('#'),
		)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}

		if _, err = reader.Read(); err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got, want := reader.Line(), 4; got != want {
			t.Errorf("Line() = %d, want %d - the physical line", got, want)
		}

		_, err = reader.Read()
		if err == nil {
			t.Fatal("Read: expected an error for the short record")
		}
		if !strings.Contains(err.Error(), "line 6") {
			t.Errorf("error %q does not name line 6", err)
		}
	})

	t.Run("no comments by default", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(strings.NewReader("a;b\n#x;2\n"))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}

		want := [][]string{{"#x", "2"}}
		if got := readAll(t, reader); !reflect.DeepEqual(got, want) {
			t.Errorf("records = %q, want %q - a lone # is data", got, want)
		}
	})
}

/*
A comment rune the parser cannot use is a wiring bug, so it has to be
csvcopy.ErrSchema from the constructor.

Left to encoding/csv it would surface as a failure on the first row, which means an
empty file would stop being the harmless case the package promises it is.
*/
func TestWithCommentRejectsUnusableRunes(t *testing.T) {
	t.Parallel()

	t.Run("same as the delimiter", func(t *testing.T) {
		t.Parallel()

		for _, input := range []string{"a;b\n1;2\n", ""} {
			_, err := NewReader(strings.NewReader(input), WithComment(';'))
			if !errors.Is(err, csvcopy.ErrSchema) {
				t.Fatalf("input %q: error = %v, want csvcopy.ErrSchema", input, err)
			}
			if errors.Is(err, csvcopy.ErrParse) {
				t.Errorf("input %q: csvcopy.ErrSchema must not wrap csvcopy.ErrParse", input)
			}
		}
	})

	t.Run("unusable rune", func(t *testing.T) {
		t.Parallel()

		for _, comment := range []rune{'"', '\r', '\n', utf8.RuneError} {
			_, err := NewReader(strings.NewReader("a;b\n"), WithComment(comment))
			if !errors.Is(err, csvcopy.ErrSchema) {
				t.Errorf("comment %q: error = %v, want csvcopy.ErrSchema", comment, err)
			}
		}
	})

	// Zero is not "unusable", it is the default: comments are off.
	t.Run("zero is the default", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(strings.NewReader("a;b\n1;2\n"), WithComment(0))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got := len(readAll(t, reader)); got != 1 {
			t.Errorf("read %d records, want 1", got)
		}
	})
}

func TestWithTrimLeadingSpace(t *testing.T) {
	t.Parallel()

	reader, err := NewReader(
		strings.NewReader("a;b\n  x;y\n"),
		WithTrimLeadingSpace(false),
		WithTrimValues(false),
	)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	want := [][]string{{"  x", "y"}}
	if got := readAll(t, reader); !reflect.DeepEqual(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
}

/*
Trimming does not exempt quoted fields, which is the surprising half of the
default: quoting is how a CSV says "these spaces are data", and a field of three
deliberate spaces still arrives as "".

Pinned in both directions because the behaviour is a documented trade-off rather
than an accident - the doc that describes it is only worth something if the code
cannot drift away from it silently.
*/
func TestWithTrimValuesDoesNotExemptQuotedFields(t *testing.T) {
	t.Parallel()

	const input = "a;b;c\n\"  padded  \";\"   \";  bare  \n"

	tests := []struct {
		name string
		opts []Option
		want [][]string
	}{
		{
			name: "on by default",
			want: [][]string{{"padded", "", "bare"}},
		},
		{
			name: "off leaves every byte alone",
			opts: []Option{WithTrimValues(false), WithTrimLeadingSpace(false)},
			want: [][]string{{"  padded  ", "   ", "  bare  "}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			reader, err := NewReader(strings.NewReader(input), test.opts...)
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			if got := readAll(t, reader); !reflect.DeepEqual(got, test.want) {
				t.Errorf("records = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWithNormalizeHeader(t *testing.T) {
	t.Parallel()

	t.Run("custom", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(
			strings.NewReader("ID;Name\n"),
			WithNormalizeHeader(strings.ToLower),
		)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got, want := reader.Columns(), []string{"id", "name"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Columns() = %q, want %q", got, want)
		}
	})

	// nil means identity, not a panic and not the default.
	t.Run("nil restores identity", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(
			strings.NewReader("  a  b ;c\n"),
			WithNormalizeHeader(nil),
			WithTrimLeadingSpace(false),
		)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got, want := reader.Columns(), []string{"  a  b ", "c"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Columns() = %q, want %q", got, want)
		}
	})
}

func TestWithHeaderRow(t *testing.T) {
	t.Parallel()

	t.Run("zero means the first row", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(strings.NewReader("a;b\n1;2\n"), WithHeaderRow(0))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got, want := reader.Columns(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
			t.Errorf("Columns() = %q, want %q", got, want)
		}
	})

	// Asking for a header past the end of the file is the empty-file case, not an
	// error: there is simply nothing to load.
	t.Run("past end of file", func(t *testing.T) {
		t.Parallel()

		reader, err := NewReader(strings.NewReader("only;one\n"), WithHeaderRow(5))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got := reader.Columns(); got != nil {
			t.Errorf("Columns() = %q, want nil", got)
		}
		if got := readAll(t, reader); got != nil {
			t.Errorf("records = %q, want none", got)
		}
	})

	t.Run("malformed row above the header", func(t *testing.T) {
		t.Parallel()

		_, err := NewReader(
			strings.NewReader("x\"y;z\na;b\n"),
			WithHeaderRow(2),
			WithLazyQuotes(false),
		)
		if !errors.Is(err, csvcopy.ErrParse) {
			t.Fatalf("error = %v, want an error wrapping csvcopy.ErrParse", err)
		}
		if !strings.Contains(err.Error(), "line 1") {
			t.Errorf("error %q does not name line 1", err)
		}
	})
}

func TestNewReaderMalformedHeader(t *testing.T) {
	t.Parallel()

	_, err := NewReader(strings.NewReader("a\"b;c\n1;2\n"), WithLazyQuotes(false))
	if !errors.Is(err, csvcopy.ErrParse) {
		t.Fatalf("error = %v, want an error wrapping csvcopy.ErrParse", err)
	}
}

// A stream that fails before the header exists is csvcopy.ErrIO: there was no
// content to be malformed.
func TestNewReaderPropagatesReadError(t *testing.T) {
	t.Parallel()

	want := errors.New("network is down")

	_, err := NewReader(failingReader{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want it to wrap %v", err, want)
	}
	if errors.Is(err, csvcopy.ErrParse) {
		t.Error("a read failure on the header must not wrap csvcopy.ErrParse")
	}
	if !errors.Is(err, csvcopy.ErrIO) {
		t.Errorf("error %v does not wrap csvcopy.ErrIO", err)
	}
}

func TestNewRawPropagatesHeaderError(t *testing.T) {
	t.Parallel()

	if _, err := NewRaw(strings.NewReader("a\"b\n"), WithLazyQuotes(false)); !errors.Is(err, csvcopy.ErrParse) {
		t.Fatalf("error = %v, want an error wrapping csvcopy.ErrParse", err)
	}
}

func TestNewTypedPropagatesHeaderError(t *testing.T) {
	t.Parallel()

	_, err := NewTyped(strings.NewReader("a\"b\n"), toEntity, WithLazyQuotes(false))
	if !errors.Is(err, csvcopy.ErrParse) {
		t.Fatalf("error = %v, want an error wrapping csvcopy.ErrParse", err)
	}
}

func TestRawRecordAndLine(t *testing.T) {
	t.Parallel()

	src, err := NewRaw(strings.NewReader("a;b\n1;2\n"))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}
	if !src.Next() {
		t.Fatal("Next() = false, want a row")
	}
	if got, want := src.Record(), []string{"1", "2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Record() = %q, want %q", got, want)
	}
	if got, want := src.Line(), 2; got != want {
		t.Errorf("Line() = %d, want %d", got, want)
	}
}

func TestTypedRecordAndLine(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}
	if !src.Next() {
		t.Fatal("Next() = false, want a row")
	}
	if got, want := src.Record(), []string{"1", "Alice"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Record() = %q, want %q", got, want)
	}
	if got, want := src.Line(), 2; got != want {
		t.Errorf("Line() = %d, want %d", got, want)
	}
}

// A record that stops before a bound column leaves that field empty, not holding
// the previous row's value - the struct is reused for the whole file.
func TestTypedShortRecordEmptiesTrailingFields(t *testing.T) {
	t.Parallel()

	src, err := NewTyped(
		strings.NewReader("id;name\n1;Alice\n2\n"),
		func(p *person) (person, error) { return *p, nil },
		WithVariableColumns(true),
	)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	got := collect(t, src)
	if len(got) != 2 {
		t.Fatalf("got %d rows, want 2", len(got))
	}
	if got[1].ID != "2" || got[1].Name != "" {
		t.Errorf("second row = %+v, want ID=2 and an empty Name", got[1])
	}
}
