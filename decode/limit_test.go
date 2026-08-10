package decode

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

// unclosedQuote is a file whose second record opens a quote and never closes it,
// so encoding/csv reads to EOF and the rest of the file becomes one field.
func unclosedQuote(payload int) string {
	var b strings.Builder

	b.WriteString("a;b\n")
	b.WriteString("\"")
	b.WriteString(strings.Repeat("x", payload))
	b.WriteString("\n")

	return b.String()
}

/*
The record that never ends is the one input that breaks the constant-memory
promise, and the limit is the only thing that stops it. Without the cap this file
would be assembled whole in one buffer.
*/
func TestMaxRecordBytesRejectsRunawayRecord(t *testing.T) {
	t.Parallel()

	reader, err := NewReader(
		strings.NewReader(unclosedQuote(64<<10)),
		WithMaxRecordBytes(4<<10),
	)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	_, err = reader.Read()
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("Read error = %v, want an error wrapping ErrRecordTooLarge", err)
	}
	if !errors.Is(err, csvcopy.ErrParse) {
		t.Error("ErrRecordTooLarge must also wrap csvcopy.ErrParse: it is a property of the file")
	}

	// Sticky, like every other failure: no resuming past it.
	if got := reader.Err(); !errors.Is(got, ErrRecordTooLarge) {
		t.Errorf("Err() = %v, want the same error", got)
	}
	if _, err = reader.Read(); !errors.Is(err, ErrRecordTooLarge) {
		t.Errorf("second Read = %v, want the stored error", err)
	}
}

/*
The error has to point at the record that opened the quote, not at the line the
file ran out on - and it must not claim a precision it does not have. There is no
csv.ParseError to take a line from here, so the last record read in full is all the
reader knows: the offending one starts after it.
*/
func TestMaxRecordBytesPointsAtTheOpeningRecord(t *testing.T) {
	t.Parallel()

	reader, err := NewReader(
		strings.NewReader("a;b\n1;2\n3;4\n\""+strings.Repeat("x", 64<<10)+"\n"),
		WithMaxRecordBytes(4<<10),
	)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	for range 2 {
		if _, err = reader.Read(); err != nil {
			t.Fatalf("Read: %v", err)
		}
	}

	_, err = reader.Read()
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("Read error = %v, want ErrRecordTooLarge", err)
	}
	if !strings.Contains(err.Error(), "after line 3") {
		t.Errorf("error %q does not put the record after line 3, the last one read in full", err)
	}
	if got, want := reader.Line(), 3; got != want {
		t.Errorf("Line() = %d, want %d", got, want)
	}
}

/*
The reason the bound above is phrased as a bound: a record counter would say
"line 3" here, and the quote opens on line 6.

The record before it is one record spanning lines 2 to 5, which is exactly what a
count cannot see and encoding/csv can - but only through a csv.ParseError, and a
record that outgrew the cap does not produce one.
*/
func TestMaxRecordBytesAfterMultilineRecordDoesNotInventALine(t *testing.T) {
	t.Parallel()

	input := "a;b\n\"one\ntwo\nthree\nfour\";2\n\"" + strings.Repeat("x", 64<<10) + "\n"

	reader, err := NewReader(strings.NewReader(input), WithMaxRecordBytes(4<<10))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err = reader.Read(); err != nil {
		t.Fatalf("Read row 1: %v", err)
	}

	_, err = reader.Read()
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("Read error = %v, want ErrRecordTooLarge", err)
	}
	if strings.Contains(err.Error(), "line 3") {
		t.Errorf("error %q names line 3, which the record has nothing to do with", err)
	}
	if !strings.Contains(err.Error(), "after line 2") {
		t.Errorf("error %q does not put the record after line 2, where the last full record started", err)
	}
}

// An ordinary file must never trip the cap.
func TestMaxRecordBytesLeavesNormalFilesAlone(t *testing.T) {
	t.Parallel()

	var b strings.Builder

	b.WriteString("a;b\n")
	for i := range 500 {
		b.WriteString("value")
		b.WriteString(strings.Repeat("y", i%40))
		b.WriteString(";2\n")
	}

	reader, err := NewReader(strings.NewReader(b.String()), WithMaxRecordBytes(4<<10))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if got := len(readAll(t, reader)); got != 500 {
		t.Errorf("read %d records, want 500", got)
	}
	if err = reader.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

/*
The budget is per record, not per file.

A quoted field spanning many lines is legitimate and may be most of the budget on
its own; what must not happen is the budget carrying over so that a long file of
short records eventually trips it.
*/
func TestMaxRecordBytesIsPerRecord(t *testing.T) {
	t.Parallel()

	const limit = 4 << 10

	multiline := "\"" + strings.Repeat("line\n", 200) + "\";z\n"

	var b strings.Builder
	b.WriteString("a;b\n")
	for range 20 {
		b.WriteString(multiline)
	}

	reader, err := NewReader(strings.NewReader(b.String()), WithMaxRecordBytes(limit))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	records := readAll(t, reader)
	if len(records) != 20 {
		t.Fatalf("read %d records, want 20 - the budget did not reset per record", len(records))
	}
	if err = reader.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// Zero removes the cap, restoring the behaviour of a plain csv.Reader.
func TestMaxRecordBytesZeroDisablesTheCap(t *testing.T) {
	t.Parallel()

	reader, err := NewReader(
		strings.NewReader(unclosedQuote(64<<10)),
		WithMaxRecordBytes(0),
		WithLazyQuotes(true),
		WithVariableColumns(true),
	)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	record, err := reader.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(record[0]) < 64<<10 {
		t.Errorf("field is %d bytes, want the whole payload - the cap still applied", len(record[0]))
	}
}

/*
A negative limit is a caller slip, and the safe reading of a slip is not "remove
the only bound on memory".

It reaches here as arithmetic on a config - a byte count computed from a field
nobody set, a subtraction, an overflow - and the old behaviour was to treat it as
zero, which means no cap at all. Silently turning off the package's one defence
against a runaway record because a number came out negative is the kind of quiet
that everything else here is written against. Zero still means it, explicitly.
*/
func TestMaxRecordBytesNegativeIsSchemaError(t *testing.T) {
	t.Parallel()

	for _, n := range []int64{-1, math.MinInt64} {
		_, err := NewReader(strings.NewReader("a;b\n1;2\n"), WithMaxRecordBytes(n))
		if !errors.Is(err, csvcopy.ErrSchema) {
			t.Fatalf("NewReader(WithMaxRecordBytes(%d)) = %v, want csvcopy.ErrSchema", n, err)
		}
		if errors.Is(err, csvcopy.ErrParse) {
			t.Error("csvcopy.ErrSchema must not wrap csvcopy.ErrParse: no input file will ever fix it")
		}
		if !strings.Contains(err.Error(), "pass 0") {
			t.Errorf("error %q does not say how to ask for no cap", err)
		}
	}
}

// The default has to be high enough to be invisible.
func TestMaxRecordBytesDefaultIsGenerous(t *testing.T) {
	t.Parallel()

	reader, err := NewReader(strings.NewReader("a;b\n" + strings.Repeat("z", 1<<20) + ";2\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	record, err := reader.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(record[0]) != 1<<20 {
		t.Errorf("field is %d bytes, want 1 MiB through the default cap", len(record[0]))
	}
}

/*
Skipped lines spend the budget, and that is a known cost rather than an oversight.

encoding/csv skips blank lines and comment lines inside a single Read, so the
budget - which is reset per Read - covers all of them together. A long enough run
is reported as a record too large when no record in the file is oversized, and
because ErrRecordTooLarge wraps ErrParse, a caller following the package's own
advice would quarantine a good file over it.

It is pinned here rather than fixed because the fix is worse: budgetReader sees raw
bytes and cannot tell a comment line from the same bytes inside an unclosed quoted
field, so resetting per line would let `"` followed by endless newlines refill the
budget forever. If this test ever fails, the question to ask is which of the two
was traded away, not how to make it pass.
*/
func TestMaxRecordBytesCountsSkippedLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		opts  []Option
	}{
		{
			name:  "blank lines",
			input: "a;b\n1;2\n" + strings.Repeat("\n", 200) + "3;4\n",
		},
		{
			name:  "comment lines",
			input: "a;b\n1;2\n" + strings.Repeat("#note\n", 40) + "3;4\n",
			opts:  []Option{WithComment('#')},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			opts := append([]Option{WithMaxRecordBytes(64)}, test.opts...)

			reader, err := NewReader(strings.NewReader(test.input), opts...)
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			if _, err = reader.Read(); err != nil {
				t.Fatalf("Read row 1: %v", err)
			}

			if _, err = reader.Read(); !errors.Is(err, ErrRecordTooLarge) {
				t.Fatalf("Read = %v, want ErrRecordTooLarge - the documented cost of a per-Read budget", err)
			}
		})
	}
}

// The other half of the trade: the same run of skipped lines is nothing at all
// when the cap is anywhere near its default, which is why this stays a wart rather
// than a bug.
func TestMaxRecordBytesSkippedLinesFitUnderARealisticCap(t *testing.T) {
	t.Parallel()

	input := "a;b\n1;2\n" + strings.Repeat("\n", 200) + strings.Repeat("#note\n", 40) + "3;4\n"

	reader, err := NewReader(strings.NewReader(input), WithMaxRecordBytes(1<<20), WithComment('#'))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	want := [][]string{{"1", "2"}, {"3", "4"}}
	if got := readAll(t, reader); !reflect.DeepEqual(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
}
