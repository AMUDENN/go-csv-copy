package csvcopy

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// failingReader stands in for a stream that breaks before any data arrives.
type failingReader struct {
	err error
}

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }

func TestWithLazyQuotes(t *testing.T) {
	const input = "a;b\nx\"y;z\n"

	t.Run("tolerated by default", func(t *testing.T) {
		reader, err := NewReader(strings.NewReader(input))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if got := readAll(t, reader); len(got) != 1 {
			t.Errorf("records = %q, want one record", got)
		}
	})

	t.Run("rejected when off", func(t *testing.T) {
		reader, err := NewReader(strings.NewReader(input), WithLazyQuotes(false))
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if _, err = reader.Read(); !errors.Is(err, ErrParse) {
			t.Fatalf("Read error = %v, want an error wrapping ErrParse", err)
		}
	})
}

/*
A delimiter encoding/csv will not take is a bug in the calling code, not a bad
file, so it must be ErrSchema and it must not wait for the first row.

Left as ErrParse it would fire the caller's quarantine-the-file path on an
argument no file can influence, and it would turn an empty input into an error -
the one thing the package promises never to do.
*/
func TestWithCommaRejectsUnusableDelimiters(t *testing.T) {
	for _, comma := range []rune{0, '"', '\r', '\n', utf8.RuneError} {
		t.Run(strconv.QuoteRune(comma), func(t *testing.T) {
			for _, input := range []string{"a;b\n1;2\n", ""} {
				_, err := NewReader(strings.NewReader(input), WithComma(comma))
				if !errors.Is(err, ErrSchema) {
					t.Fatalf("input %q: error = %v, want ErrSchema", input, err)
				}
				if errors.Is(err, ErrParse) {
					t.Errorf("input %q: ErrSchema must not wrap ErrParse", input)
				}
			}
		})
	}
}

// A multibyte delimiter is unusual but legal, and rejecting it would be a
// regression: only the runes encoding/csv refuses are refused.
func TestWithCommaAcceptsMultibyte(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a→b\n1→2\n"), WithComma('→'))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got, want := reader.Columns(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Columns() = %q, want %q", got, want)
	}
}

func TestWithTrimLeadingSpace(t *testing.T) {
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

func TestWithNormalizeHeader(t *testing.T) {
	t.Run("custom", func(t *testing.T) {
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
	t.Run("zero means the first row", func(t *testing.T) {
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
		_, err := NewReader(
			strings.NewReader("x\"y;z\na;b\n"),
			WithHeaderRow(2),
			WithLazyQuotes(false),
		)
		if !errors.Is(err, ErrParse) {
			t.Fatalf("error = %v, want an error wrapping ErrParse", err)
		}
		if !strings.Contains(err.Error(), "line 1") {
			t.Errorf("error %q does not name line 1", err)
		}
	})
}

func TestNewReaderMalformedHeader(t *testing.T) {
	_, err := NewReader(strings.NewReader("a\"b;c\n1;2\n"), WithLazyQuotes(false))
	if !errors.Is(err, ErrParse) {
		t.Fatalf("error = %v, want an error wrapping ErrParse", err)
	}
}

func TestNewReaderPropagatesReadError(t *testing.T) {
	want := errors.New("network is down")

	_, err := NewReader(failingReader{err: want})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want it to wrap %v", err, want)
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("error %v does not wrap ErrParse", err)
	}
}

func TestNewRawPropagatesHeaderError(t *testing.T) {
	if _, err := NewRaw(strings.NewReader("a\"b\n"), WithLazyQuotes(false)); !errors.Is(err, ErrParse) {
		t.Fatalf("error = %v, want an error wrapping ErrParse", err)
	}
}

func TestNewTypedPropagatesHeaderError(t *testing.T) {
	_, err := NewTyped(strings.NewReader("a\"b\n"), toEntity, WithLazyQuotes(false))
	if !errors.Is(err, ErrParse) {
		t.Fatalf("error = %v, want an error wrapping ErrParse", err)
	}
}

func TestRawRecordAndLine(t *testing.T) {
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
