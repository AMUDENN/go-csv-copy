package copyfrom

import (
	"errors"
	"strings"
	"testing"

	csvcopy "github.com/AMUDENN/go-csv-copy"
	"github.com/AMUDENN/go-csv-copy/decode"
)

func TestValidateColumns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		columns []string
		ok      bool
		// mentions are substrings the message has to carry, so a caller can see
		// which column is at fault without reading the file again.
		mentions []string
	}{
		{
			name:    "plain names",
			columns: []string{"id", "last_name", "Birth_Year", "col9"},
			ok:      true,
		},
		{
			name:    "empty header",
			columns: nil,
			ok:      true,
		},
		{
			name:     "empty name",
			columns:  []string{"id", ""},
			mentions: []string{"column 2", "empty"},
		},
		{
			name:     "duplicate",
			columns:  []string{"id", "name", "id"},
			mentions: []string{"column 3", "duplicates column 1", `"id"`},
		},
		// Two columns quoted, one column unquoted - and the unquoted reading is the
		// one this function is asked about.
		{
			name:     "differs only in case",
			columns:  []string{"ID", "id"},
			mentions: []string{"column 2", `"id"`, "collides with column 1", `"ID"`},
		},
		{
			name:     "case collision is worded differently from an exact duplicate",
			columns:  []string{"Name", "name"},
			mentions: []string{"unless both are quoted"},
		},
		{
			name:     "over 63 bytes",
			columns:  []string{strings.Repeat("a", 64)},
			mentions: []string{"64 bytes", "63"},
		},
		{
			name:    "exactly 63 bytes is fine",
			columns: []string{strings.Repeat("a", 63)},
			ok:      true,
		},
		{
			name:     "double quote",
			columns:  []string{`x" ); DROP TABLE clients; --`},
			mentions: []string{"column 1", `'"'`},
		},
		// Passes every character rule and still cannot open a column definition:
		// CREATE TABLE t (123 TEXT) is a syntax error.
		{
			name:     "starts with a digit",
			columns:  []string{"123"},
			mentions: []string{"column 1", `"123"`, "starts with a digit"},
		},
		{
			name:     "digit prefix on an otherwise ordinary name",
			columns:  []string{"2023_q1"},
			mentions: []string{"starts with a digit"},
		},
		{
			name:    "a digit anywhere else is fine",
			columns: []string{"q1_2023", "col9"},
			ok:      true,
		},
		{
			name:     "semicolon",
			columns:  []string{"id;name"},
			mentions: []string{"';'"},
		},
		{
			name:     "space",
			columns:  []string{"date of birth"},
			mentions: []string{"' '"},
		},
		// Rejecting non-ASCII is a documented decision, not an oversight: a header
		// in prose should be mapped explicitly rather than quoted and hoped for.
		{
			name:     "cyrillic",
			columns:  []string{"фамилия"},
			mentions: []string{"column 1"},
		},
		{
			name:     "newline",
			columns:  []string{"id\nname"},
			mentions: []string{"column 1"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateColumns(test.columns)

			if test.ok {
				if err != nil {
					t.Fatalf("ValidateColumns = %v, want nil", err)
				}

				return
			}

			if !errors.Is(err, ErrInvalidColumns) {
				t.Fatalf("error = %v, want ErrInvalidColumns", err)
			}
			if !errors.Is(err, csvcopy.ErrParse) {
				t.Error("ErrInvalidColumns must wrap csvcopy.ErrParse: it is a property of the file")
			}
			for _, want := range test.mentions {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %s", err, want)
				}
			}
		})
	}
}

/*
Every violation is reported at once.

One run should tell the whole story of the file. Reporting the first problem only
turns fixing an export into one round trip per column.
*/
func TestValidateColumnsReportsEveryProblem(t *testing.T) {
	t.Parallel()

	err := ValidateColumns([]string{"", "id;drop", "id", "id", strings.Repeat("z", 70), "9lives"})
	if !errors.Is(err, ErrInvalidColumns) {
		t.Fatalf("error = %v, want ErrInvalidColumns", err)
	}

	for _, want := range []string{
		"column 1 is empty",
		"column 2",
		"column 4 duplicates column 3",
		"70 bytes",
		"column 6",
		"starts with a digit",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}

// The header a Raw source hands out is exactly what goes into ValidateColumns, so
// the two have to agree about what a column name is after normalization.
func TestValidateColumnsOverRawHeader(t *testing.T) {
	t.Parallel()

	t.Run("clean file", func(t *testing.T) {
		t.Parallel()

		src, err := decode.NewRaw(strings.NewReader("id;last_name\n1;Smith\n"))
		if err != nil {
			t.Fatalf("decode.NewRaw: %v", err)
		}
		if err = ValidateColumns(src.Columns()); err != nil {
			t.Errorf("ValidateColumns = %v, want nil", err)
		}
	})

	t.Run("hostile file", func(t *testing.T) {
		t.Parallel()

		src, err := decode.NewRaw(strings.NewReader(`id;"x"" ); DROP TABLE clients; --"` + "\n1;2\n"))
		if err != nil {
			t.Fatalf("decode.NewRaw: %v", err)
		}
		if err = ValidateColumns(src.Columns()); !errors.Is(err, ErrInvalidColumns) {
			t.Fatalf("ValidateColumns = %v, want ErrInvalidColumns for %q", err, src.Columns())
		}
	})
}
