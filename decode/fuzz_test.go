package decode

import (
	"errors"
	"io"
	"strings"
	"testing"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

/*
The package promises it never panics and always reports a line. Until now that was
held up by review rather than by a test, and there are three places where it could
go wrong on input nobody wrote by hand: cr.FieldPos(0), which panics if the index is
out of range; dst.Field(b.field), which panics if a plan and a struct disagree; and
the arithmetic that turns a record into a line number.

These targets assert the contract rather than only the absence of a crash. "It did
not panic" would pass on a reader that silently returned no rows.

	go test -run=^$ -fuzz=FuzzReader -fuzztime=30s

Corpora that find something belong in testdata/fuzz/, committed.
*/
var fuzzSeeds = []string{
	"a;b\n1;2\n",
	"\xEF\xBB\xBFa;b\n",
	"a;b\n\"1\n2\";3\n",
	"a;b\n\"unclosed;x\n",
	"a;b;c\n1;2\n",
	"a;b\n\n\n1;2\n",
	"#c\na;b\n1;2\n",
	"a;a\n1;2\n",
	"",
	"\n",
	"\"",
	";",
}

func addFuzzSeeds(f *testing.F) {
	f.Helper()

	for _, seed := range fuzzSeeds {
		f.Add(seed)
	}
}

func FuzzReader(f *testing.F) {
	addFuzzSeeds(f)

	f.Fuzz(func(t *testing.T, in string) {
		reader, err := NewReader(strings.NewReader(in))
		if err != nil {
			assertClassified(t, err)

			return
		}

		for record := range reader.All() {
			if line := reader.Line(); line < 1 {
				t.Fatalf("Line() = %d for record %q", line, record)
			}
			if reader.Record() == nil {
				t.Fatalf("Record() = nil while holding %q", record)
			}
		}

		assertStreamEnded(t, reader.Err(), func() error {
			_, err := reader.Read()

			return err
		})
	})
}

func FuzzRaw(f *testing.F) {
	addFuzzSeeds(f)

	f.Fuzz(func(t *testing.T, in string) {
		src, err := NewRaw(strings.NewReader(in), WithVariableColumns(true))
		if err != nil {
			assertClassified(t, err)

			return
		}

		columns := len(src.Columns())

		for src.Next() {
			values, err := src.Values()
			if err != nil {
				t.Fatalf("Values: %v", err)
			}
			// pgx.CopyFrom is handed the column list and then this slice. If they
			// ever disagree the load is silently wrong, or pgx errors out far from
			// the cause.
			if len(values) != columns {
				t.Fatalf("Values() has %d entries, Columns() has %d", len(values), columns)
			}
			if line := src.Line(); line < 1 {
				t.Fatalf("Line() = %d", line)
			}
		}

		if src.Err() != nil && src.Next() {
			t.Fatal("Next() = true after an error")
		}
	})
}

type fuzzRow struct {
	A string `csv:"a"`
	B string `csv:"b"`
}

func FuzzTyped(f *testing.F) {
	addFuzzSeeds(f)

	f.Fuzz(func(t *testing.T, in string) {
		src, err := NewTyped(
			strings.NewReader(in),
			func(r *fuzzRow) (fuzzRow, error) { return *r, nil },
			WithAllowMissingColumns(true),
			WithVariableColumns(true),
		)
		if err != nil {
			assertClassified(t, err)

			return
		}

		var rows int64
		for range src.All() {
			rows++
			if line := src.Line(); line < 1 {
				t.Fatalf("Line() = %d", line)
			}
		}

		if got := src.Rows(); got != rows {
			t.Fatalf("Rows() = %d, iterated %d", got, rows)
		}
		if src.Err() != nil && src.Next() {
			t.Fatal("Next() = true after an error")
		}
	})
}

/*
assertClassified is the taxonomy as an assertion: every error this package produces
has to match one of the three sentinels, because that is what a caller branches on.

An unclassified error is not a crash, so no amount of fuzzing for panics would
find it - and a caller would silently take the wrong recovery path for it.
ErrMissingColumns, ErrInvalidColumns and ErrRecordTooLarge all wrap
csvcopy.ErrParse, so they are covered here too.
*/
func assertClassified(t *testing.T, err error) {
	t.Helper()

	if errors.Is(err, csvcopy.ErrParse) || errors.Is(err, csvcopy.ErrIO) || errors.Is(err, csvcopy.ErrSchema) {
		return
	}

	t.Fatalf("error %v matches none of csvcopy.ErrParse, csvcopy.ErrIO, csvcopy.ErrSchema", err)
}

/*
assertStreamEnded checks the other half of the contract: a stream that ended without
an error ended at EOF, and one that ended with an error stays ended.

Without this a target would pass on a reader that quietly stopped early, which is
the failure mode that matters most - a truncated load nobody notices.
*/
func assertStreamEnded(t *testing.T, streamErr error, next func() error) {
	t.Helper()

	err := next()

	if streamErr == nil {
		if !errors.Is(err, io.EOF) {
			t.Fatalf("Err() is nil but the next read gave %v, want io.EOF", err)
		}

		return
	}

	if !errors.Is(err, streamErr) {
		t.Fatalf("after an error the next read gave %v, want the stored %v", err, streamErr)
	}
}
