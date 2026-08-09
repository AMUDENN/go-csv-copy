package csvcopy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestReaderAll(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b\n1;2\n3;4\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var got [][]string
	for record := range reader.All() {
		got = append(got, append([]string(nil), record...))
	}

	want := [][]string{{"1", "2"}, {"3", "4"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("records = %q, want %q", got, want)
	}
	if err = reader.Err(); err != nil {
		t.Errorf("Err() = %v, want nil - end of file is not an error", err)
	}
}

// Ranging cannot carry an error out, so the loop has to stop and Err has to hold
// the reason. Without that the caller silently processes a truncated file.
func TestReaderAllStopsOnErrorAndReportsIt(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a;b;c\n1;2;3\n4;5\n6;7;8\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var count int
	for range reader.All() {
		count++
	}

	if count != 1 {
		t.Errorf("iterated %d records before the error, want 1", count)
	}

	err = reader.Err()
	if !errors.Is(err, ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping ErrParse", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("Err() = %q, does not name line 3", err)
	}
}

func TestReaderAllEmptyFile(t *testing.T) {
	reader, err := NewReader(strings.NewReader(""))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	for range reader.All() {
		t.Fatal("iterated a record from an empty file")
	}
	if err = reader.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// Breaking out early must not leave the source wedged: whatever the loop did, the
// next pull still behaves.
func TestReaderAllBreakEarly(t *testing.T) {
	reader, err := NewReader(strings.NewReader("a\n1\n2\n3\n"))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var seen []string
	for record := range reader.All() {
		seen = append(seen, record[0])

		break
	}

	if !reflect.DeepEqual(seen, []string{"1"}) {
		t.Fatalf("seen = %q, want [1]", seen)
	}

	record, err := reader.Read()
	if err != nil {
		t.Fatalf("Read after break: %v", err)
	}
	if record[0] != "2" {
		t.Errorf("Read after break = %q, want 2 - the iterator lost a record", record[0])
	}
}

func TestRawAll(t *testing.T) {
	src, err := NewRaw(strings.NewReader("a;b\n1;2\n3;4\n"))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	var got [][]any
	for values := range src.All() {
		got = append(got, append([]any(nil), values...))
	}

	want := [][]any{{"1", "2"}, {"3", "4"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rows = %#v, want %#v", got, want)
	}
	if err = src.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
	if got, want := src.Rows(), int64(2); got != want {
		t.Errorf("Rows() = %d, want %d", got, want)
	}
}

func TestRawAllStopsOnError(t *testing.T) {
	src, err := NewRaw(strings.NewReader("a;b;c\n1;2;3\n4;5\n"))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	var count int
	for range src.All() {
		count++
	}

	if count != 1 {
		t.Errorf("iterated %d rows before the error, want 1", count)
	}
	if err = src.Err(); !errors.Is(err, ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping ErrParse", err)
	}
}

func TestRawAllBreakEarly(t *testing.T) {
	src, err := NewRaw(strings.NewReader("a;b\n1;2\n3;4\n5;6\n"))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	var count int
	for range src.All() {
		count++

		break
	}

	if count != 1 {
		t.Fatalf("iterated %d rows, want 1", count)
	}
	if got, want := src.Rows(), int64(1); got != want {
		t.Errorf("Rows() = %d, want %d", got, want)
	}

	// The source is still usable: breaking is not an error.
	if !src.Next() {
		t.Fatalf("Next() after break = false, Err() = %v", src.Err())
	}
	values, err := src.Values()
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if values[0] != "3" {
		t.Errorf("row after break = %v, want 3 - the iterator lost a row", values[0])
	}
}

func TestTypedAll(t *testing.T) {
	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n2;Bob\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	var got []*entity
	for value := range src.All() {
		got = append(got, value)
	}

	want := []*entity{{ID: "1", Name: "Alice"}, {ID: "2", Name: "Bob"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values = %#v, want %#v", got, want)
	}
	if err = src.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

func TestTypedAllStopsOnConvertError(t *testing.T) {
	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n;Bob\n3;Carol\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	var count int
	for range src.All() {
		count++
	}

	if count != 1 {
		t.Errorf("iterated %d rows before the error, want 1", count)
	}

	err = src.Err()
	if !errors.Is(err, ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping ErrParse", err)
	}
	if !strings.Contains(err.Error(), "line 3") {
		t.Errorf("Err() = %q, does not name line 3", err)
	}
}

func TestTypedAllBreakEarly(t *testing.T) {
	src, err := NewTyped(strings.NewReader("id;name\n1;Alice\n2;Bob\n3;Carol\n"), toEntity)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	var seen []string
	for value := range src.All() {
		seen = append(seen, value.ID)

		break
	}

	if !reflect.DeepEqual(seen, []string{"1"}) {
		t.Fatalf("seen = %q, want [1]", seen)
	}
	if !src.Next() {
		t.Fatalf("Next() after break = false, Err() = %v", src.Err())
	}
	if got := src.Value().ID; got != "2" {
		t.Errorf("row after break = %q, want 2 - the iterator lost a row", got)
	}
}

/*
Unlike the other iterators, Typed yields whatever convert returned, so a value kept
after the loop stays valid. This is the property that makes the typed layer usable
with no database at all.
*/
func TestTypedAllValuesSurviveTheLoop(t *testing.T) {
	src, err := NewTyped(
		strings.NewReader("id;name\n1;Alice\n2;Bob\n"),
		func(p *person) (person, error) { return *p, nil },
	)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	var kept []person
	for value := range src.All() {
		kept = append(kept, value)
	}

	if len(kept) != 2 {
		t.Fatalf("kept %d values, want 2", len(kept))
	}
	if kept[0].Name != "Alice" || kept[1].Name != "Bob" {
		t.Errorf("kept = %+v, want Alice then Bob - the reused struct leaked", kept)
	}
}
