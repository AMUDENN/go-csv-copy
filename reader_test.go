package csvcopy

import (
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
			name:    "bare quote is tolerated by default",
			input:   `a;b` + "\n" + `2";3` + "\n",
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
