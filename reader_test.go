package csvcopy

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

const bom = "\xEF\xBB\xBF"

/*
trickleReader hands out one byte per Read without ever returning an error, which a
Reader is allowed to do. It is the whole reason skipBOM uses io.ReadFull: a plain
Read here would come back with one byte, the BOM would survive, and the first
column name would carry a U+FEFF that matches no tag.
*/
type trickleReader struct {
	data []byte
	pos  int
}

func (t *trickleReader) Read(p []byte) (int, error) {
	if t.pos >= len(t.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = t.data[t.pos]
	t.pos++

	return 1, nil
}

// brokenReader hands out its data and then fails, standing in for a stream that
// breaks after the header has already been read.
type brokenReader struct {
	data []byte
	pos  int
	err  error
}

func (b *brokenReader) Read(p []byte) (int, error) {
	if b.pos >= len(b.data) {
		return 0, b.err
	}
	n := copy(p, b.data[b.pos:])
	b.pos += n

	return n, nil
}

func readAll(t *testing.T, r *Reader) [][]string {
	t.Helper()

	var records [][]string
	for {
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		records = append(records, append([]string(nil), record...))
	}
}

// readUntilError is readAll for the cases that are meant to fail: it stops at the
// first error instead of failing the test, leaving it in Err.
func readUntilError(t *testing.T, r *Reader) [][]string {
	t.Helper()

	var records [][]string
	for {
		record, err := r.Read()
		if err != nil {
			return records
		}
		records = append(records, append([]string(nil), record...))
	}
}

func TestReaderHeader(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		opts    []Option
		columns []string
		records [][]string
	}{
		{
			name:    "plain",
			input:   "a;b;c\n1;2;3\n",
			columns: []string{"a", "b", "c"},
			records: [][]string{{"1", "2", "3"}},
		},
		{
			name:    "bom is stripped",
			input:   bom + "a;b\n1;2\n",
			columns: []string{"a", "b"},
			records: [][]string{{"1", "2"}},
		},
		{
			name:    "crlf",
			input:   "a;b\r\n1;2\r\n",
			columns: []string{"a", "b"},
			records: [][]string{{"1", "2"}},
		},
		{
			name:    "no trailing newline",
			input:   "a;b\n1;2",
			columns: []string{"a", "b"},
			records: [][]string{{"1", "2"}},
		},
		{
			name:    "empty input",
			input:   "",
			columns: nil,
			records: nil,
		},
		{
			name:    "bom only",
			input:   bom,
			columns: nil,
			records: nil,
		},
		{
			name:    "shorter than a bom",
			input:   "a",
			columns: []string{"a"},
			records: nil,
		},
		{
			name:    "header only",
			input:   "a;b\n",
			columns: []string{"a", "b"},
			records: nil,
		},
		{
			name:    "header names are normalized",
			input:   "  first  name ;\tsecond\n\nx;y\n",
			columns: []string{"first name", "second"},
			records: [][]string{{"x", "y"}},
		},
		{
			name:    "quoted delimiter and escaped quote",
			input:   "a;b\n\"x;y\";\"say \"\"hi\"\"\"\n",
			columns: []string{"a", "b"},
			records: [][]string{{"x;y", `say "hi"`}},
		},
		{
			name:    "quoted field spanning lines",
			input:   "a;b\n\"line1\nline2\";z\n",
			columns: []string{"a", "b"},
			records: [][]string{{"line1\nline2", "z"}},
		},
		{
			name:    "bare quote is tolerated when asked for",
			input:   `a;b` + "\n" + `2";3` + "\n",
			opts:    []Option{WithLazyQuotes(true)},
			columns: []string{"a", "b"},
			records: [][]string{{`2"`, "3"}},
		},
		{
			name:    "comma separator",
			input:   "a,b\n1,2\n",
			opts:    []Option{WithComma(',')},
			columns: []string{"a", "b"},
			records: [][]string{{"1", "2"}},
		},
		{
			name:    "header below a title",
			input:   "Monthly report\n\na;b\n1;2\n",
			opts:    []Option{WithHeaderRow(2)},
			columns: []string{"a", "b"},
			records: [][]string{{"1", "2"}},
		},
		{
			name:    "values are trimmed",
			input:   "a;b\n x  ;  y \n",
			columns: []string{"a", "b"},
			records: [][]string{{"x", "y"}},
		},
		{
			name:    "trailing space kept when trimming is off",
			input:   "a;b\nx  ;y\n",
			opts:    []Option{WithTrimValues(false)},
			columns: []string{"a", "b"},
			records: [][]string{{"x  ", "y"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, err := NewReader(strings.NewReader(test.input), test.opts...)
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			if got := reader.Columns(); !reflect.DeepEqual(got, test.columns) {
				t.Errorf("Columns() = %q, want %q", got, test.columns)
			}
			if got := readAll(t, reader); !reflect.DeepEqual(got, test.records) {
				t.Errorf("records = %q, want %q", got, test.records)
			}
		})
	}
}

// A blank line above the header is skipped by encoding/csv, so counting the header
// row cannot rely on physical lines.
func TestReaderHeaderRowSkipsBlankLines(t *testing.T) {
	reader, err := NewReader(strings.NewReader("title\na;b\n1;2\n"), WithHeaderRow(2))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got, want := reader.Columns(), []string{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Columns() = %q, want %q", got, want)
	}
}

func TestReaderStripsBOMFromSlowReader(t *testing.T) {
	reader, err := NewReader(&trickleReader{data: []byte(bom + "id;name\n1;Alice\n")})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	want := []string{"id", "name"}
	if got := reader.Columns(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Columns() = %q, want %q - the BOM survived a one-byte-at-a-time reader", got, want)
	}
	if got, want := readAll(t, reader), [][]string{{"1", "Alice"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
}

/*
Nothing in the package may index into a value by byte: the BOM strip, the header
normalizer and the value trimmer all have to stay rune-safe.

Every other fixture here is ASCII, where a byte-indexing bug is invisible, so this
one carries the multibyte coverage on its own.
*/
func TestReaderMultibyteValues(t *testing.T) {
	reader, err := NewReader(strings.NewReader(bom + "\"café \n au lait\";日本語\n  naïve  ; Ünicode \n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	wantColumns := []string{"café au lait", "日本語"}
	if got := reader.Columns(); !reflect.DeepEqual(got, wantColumns) {
		t.Errorf("Columns() = %q, want %q", got, wantColumns)
	}

	wantRecords := [][]string{{"naïve", "Ünicode"}}
	if got := readAll(t, reader); !reflect.DeepEqual(got, wantRecords) {
		t.Errorf("records = %q, want %q", got, wantRecords)
	}
}

func TestReaderNilReader(t *testing.T) {
	if _, err := NewReader(nil); !errors.Is(err, ErrSchema) {
		t.Fatalf("NewReader(nil) error = %v, want ErrSchema", err)
	}
}

func TestReaderFieldCountErrorNamesTheLine(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b;c\n1;2;3\n4;5\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err = reader.Read(); err != nil {
		t.Fatalf("Read row 1: %v", err)
	}

	_, err = reader.Read()
	if err == nil {
		t.Fatal("Read: expected an error for a short record")
	}
	if !errors.Is(err, ErrParse) {
		t.Errorf("error %v does not wrap ErrParse", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error %q does not name line 3", err)
	}
}

func TestReaderRecordAndLine(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b\n1;2\n3;4\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got, want := reader.Line(), 1; got != want {
		t.Errorf("Line() after header = %d, want %d", got, want)
	}

	if _, err = reader.Read(); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got, want := reader.Line(), 2; got != want {
		t.Errorf("Line() = %d, want %d", got, want)
	}
	if got, want := reader.Record(), []string{"1", "2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Record() = %q, want %q", got, want)
	}
}

/*
Record has to name the record that failed, and only that one.

encoding/csv reuses one backing array, so a short record leaves the previous
record's trailing fields visible past its own end. Holding on to the old slice
header would put a row that was never in the file into the error message: here,
["4" "5" "3"] - the "3" left over from line 2.
*/
func TestReaderRecordAfterFailedReadIsTheFailingOne(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b;c\n1;2;3\n4;5\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err = reader.Read(); err != nil {
		t.Fatalf("Read row 1: %v", err)
	}
	if _, err = reader.Read(); err == nil {
		t.Fatal("Read: expected an error for a short record")
	}

	if got, want := reader.Record(), []string{"4", "5"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Record() = %q, want %q", got, want)
	}
	if got, want := reader.Line(), 3; got != want {
		t.Errorf("Line() = %d, want %d - it must name the record that failed", got, want)
	}
}

/*
The first bad record ends the reader for good.

Without this a second pass resumes after the record that failed, quietly dropping
it: the caller gets rows, no error on that pass, and a table short one line. Raw
and Typed latch on their own err, and Reader has to agree with them.
*/
func TestReaderStopsForGoodAfterAnError(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b;c\n1;2;3\n4;5\n6;7;8\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	if got := len(readUntilError(t, reader)); got != 1 {
		t.Errorf("read %d records before the error, want 1", got)
	}

	first := reader.Err()
	if !errors.Is(first, ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping ErrParse", first)
	}

	for record := range reader.All() {
		t.Fatalf("ranging again resumed past the bad record and yielded %q", record)
	}
	if got := reader.Err(); !errors.Is(got, first) {
		t.Errorf("Err() = %v, want the error that stopped the reader", got)
	}
}

// Err must not outlive the failure it describes - and it cannot, because the
// reader never reads on after one.
func TestReaderErrIsNilUntilSomethingFails(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b\n1;2\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := reader.Err(); got != nil {
		t.Errorf("Err() before reading = %v, want nil", got)
	}

	readAll(t, reader)

	if got := reader.Err(); got != nil {
		t.Errorf("Err() after a clean pass = %v, want nil - end of file is not an error", got)
	}
}

/*
Line is the physical line of the file, not a count of records.

Two things pull the two apart: encoding/csv skips blank lines, and a quoted field
may span several of them. The caller opens the file at whatever number an error
carries, so it has to be the number an editor shows - counting records would send
them to the wrong row, and the further into the file, the further off.

	1  a;b
	2
	3  1;2
	4  "x
	5  y
	6  z";4
	7  5        <- one field where the header has two
*/
func TestReaderLineIsPhysicalNotARecordCount(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b\n\n1;2\n\"x\ny\nz\";4\n5\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got, want := reader.Line(), 1; got != want {
		t.Errorf("Line() after the header = %d, want %d", got, want)
	}

	for _, want := range []int{3, 4} {
		if _, err = reader.Read(); err != nil {
			t.Fatalf("Read: %v", err)
		}
		if got := reader.Line(); got != want {
			t.Errorf("Line() = %d, want %d", got, want)
		}
	}

	if _, err = reader.Read(); err == nil {
		t.Fatal("Read: expected an error for a short record")
	}
	if got, want := reader.Line(), 7; got != want {
		t.Errorf("Line() after the failure = %d, want %d", got, want)
	}
	if !strings.Contains(err.Error(), "line 7") {
		t.Errorf("error %q does not name line 7", err)
	}
}

// csv.ParseError names the line in its own message, so the wrapper must not name
// it again: "line 3: record on line 3: ..." reads like two different lines.
func TestReaderParseErrorNamesTheLineOnce(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b\n1\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	_, err = reader.Read()
	if err == nil {
		t.Fatal("Read: expected an error for a short record")
	}
	if got := strings.Count(err.Error(), "line 2"); got != 1 {
		t.Errorf("error %q names the line %d times, want once", err, got)
	}
}

/*
A stream that breaks mid-file is ErrIO, not ErrParse, and still carries a line.

The distinction is the point: a caller that quarantines files on ErrParse would
otherwise quarantine a perfectly good file because the network blinked. The file is
fine, the read is not, and the right answer is to try again.

encoding/csv passes an I/O error through as itself rather than wrapping it in a
csv.ParseError, so there is no line to take from it and the count kept here is what
names the record. That is the one case where the two ways of numbering cannot
agree, and a count is the best available answer.
*/
func TestReaderMidStreamReadErrorIsErrIO(t *testing.T) {
	want := errors.New("network is down")

	reader, err := NewReader(&brokenReader{data: []byte("a;b\n1;2\n"), err: want})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err = reader.Read(); err != nil {
		t.Fatalf("Read row 1: %v", err)
	}

	_, err = reader.Read()
	if !errors.Is(err, want) {
		t.Fatalf("Read = %v, want it to wrap %v", err, want)
	}
	if !errors.Is(err, ErrIO) {
		t.Errorf("error %v does not wrap ErrIO", err)
	}
	if errors.Is(err, ErrParse) {
		t.Error("a read failure must not wrap ErrParse: the file is not the problem")
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("error %q does not name line 3", err)
	}
	if got, want := reader.Line(), 3; got != want {
		t.Errorf("Line() = %d, want %d", got, want)
	}
}

// A cancelled context has to stay recognisable through the wrapping, or a caller
// cannot tell "we gave up" from "the disk died".
func TestReaderCancelledContextIsErrIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reader, err := NewReader(&brokenReader{data: []byte("a;b\n1;2\n"), err: ctx.Err()})
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if _, err = reader.Read(); err != nil {
		t.Fatalf("Read row 1: %v", err)
	}

	_, err = reader.Read()
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error %v does not unwrap to context.Canceled", err)
	}
	if !errors.Is(err, ErrIO) {
		t.Errorf("error %v does not wrap ErrIO", err)
	}
	if errors.Is(err, ErrParse) {
		t.Error("a cancelled read must not look like a bad file")
	}
}

// Malformed content stays ErrParse. This is the other half of the classification,
// and the regression that keeps ErrIO from swallowing everything.
func TestReaderMalformedContentStaysErrParse(t *testing.T) {
	tests := map[string]string{
		"short record":   "a;b;c\n1;2\n",
		"unclosed quote": "a;b\n\"open;x\n",
	}

	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			reader, err := NewReader(strings.NewReader(input))
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}

			_, err = reader.Read()
			if !errors.Is(err, ErrParse) {
				t.Fatalf("Read = %v, want an error wrapping ErrParse", err)
			}
			if errors.Is(err, ErrIO) {
				t.Error("malformed content must not be reported as a read failure")
			}
		})
	}
}

/*
recordLine's guard is for a record with no fields at all.

encoding/csv does not produce one today - a blank line is skipped rather than
returned - so nothing above can reach this branch. The guard stays because
FieldPos panics on a field it does not have, and a future version of encoding/csv
is not the right place to find that out.
*/
func TestRecordLineWithoutFields(t *testing.T) {
	cr := csv.NewReader(strings.NewReader("a\n"))

	if got, want := recordLine(cr, nil, 42), 42; got != want {
		t.Errorf("recordLine(nil) = %d, want the fallback %d", got, want)
	}
}

func TestReaderReadAfterEOFStaysEOF(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a\n1\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	readAll(t, reader)

	for range 3 {
		if _, err = reader.Read(); !errors.Is(err, io.EOF) {
			t.Fatalf("Read after EOF = %v, want io.EOF", err)
		}
	}
}
