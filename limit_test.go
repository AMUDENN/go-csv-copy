package csvcopy

import (
	"errors"
	"strings"
	"testing"
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
	if !errors.Is(err, ErrParse) {
		t.Error("ErrRecordTooLarge must also wrap ErrParse: it is a property of the file")
	}

	// Sticky, like every other failure: no resuming past it.
	if got := reader.Err(); !errors.Is(got, ErrRecordTooLarge) {
		t.Errorf("Err() = %v, want the same error", got)
	}
	if _, err = reader.Read(); !errors.Is(err, ErrRecordTooLarge) {
		t.Errorf("second Read = %v, want the stored error", err)
	}
}

// The line has to name where the record started - where the quote opened - not
// where the file ran out.
func TestMaxRecordBytesNamesTheOpeningLine(t *testing.T) {
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
	if !strings.Contains(err.Error(), "line 4") {
		t.Errorf("error %q does not name line 4, where the quote opened", err)
	}
}

// An ordinary file must never trip the cap.
func TestMaxRecordBytesLeavesNormalFilesAlone(t *testing.T) {
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

// A negative limit is a caller slip, not a request for a negative budget: treat it
// as no cap rather than as a cap of zero that rejects everything.
func TestMaxRecordBytesNegativeMeansNoCap(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b\n1;2\n"), WithMaxRecordBytes(-1))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if got := len(readAll(t, reader)); got != 1 {
		t.Errorf("read %d records, want 1", got)
	}
	if err = reader.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// The default has to be high enough to be invisible.
func TestMaxRecordBytesDefaultIsGenerous(t *testing.T) {
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
