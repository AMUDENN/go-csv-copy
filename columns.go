package csvcopy

import (
	"fmt"
	"strings"
)

/*
maxIdentifierBytes is what Postgres allows an identifier to be, NAMEDATALEN - 1.

Anything longer is truncated silently, which is worse than it sounds: two column
names that differ only past byte 63 become one column, and the second COPY value
lands in the first column.
*/
const maxIdentifierBytes = 63

/*
ErrInvalidColumns reports a header that must not reach a DDL statement.

Wraps ErrParse: it is a property of the file, and the caller who quarantines bad
files wants this one quarantined too.
*/
var ErrInvalidColumns = fmt.Errorf("%w: header has unusable column names", ErrParse)

/*
ValidateColumns rejects a header whose names cannot safely be used to build a
statement.

A staging table is created from the file's own column names, which makes those
names untrusted input on the path to CREATE TABLE. A file with a column called

	x" ); DROP TABLE clients; --

is not a hypothetical; it is a text file, and anyone can write one. This refuses
the four shapes that either break a statement or change its meaning: an empty
name, a duplicate, a name over 63 bytes, and anything outside [A-Za-z0-9_].

Every violation is reported at once rather than the first one, so one run tells the
whole story of the file instead of one name per attempt.

Deliberately strict, and it rejects more than injection: "date of birth" and any
non-ASCII name are refused too. A file whose column names are prose is a file whose
names should be mapped explicitly, not quoted and hoped for. Passing this is not a
licence to skip quoting - build identifiers with pgx.Identifier{name}.Sanitize()
regardless. For CopyFrom, pgx quotes them itself.

An empty header - the empty-file case - passes: there is nothing to build from and
nothing to be unsafe with.
*/
func ValidateColumns(columns []string) error {
	var problems []string

	seen := make(map[string]int, len(columns))

	for i, name := range columns {
		position := i + 1

		if name == "" {
			problems = append(problems, fmt.Sprintf("column %d is empty", position))

			continue
		}

		if n := len(name); n > maxIdentifierBytes {
			problems = append(problems, fmt.Sprintf(
				"column %d (%q) is %d bytes, over the %d-byte limit",
				position, name, n, maxIdentifierBytes))
		}

		if bad, ok := firstUnusableRune(name); ok {
			problems = append(problems, fmt.Sprintf(
				"column %d (%q) contains %q", position, name, bad))
		}

		if first, duplicate := seen[name]; duplicate {
			problems = append(problems, fmt.Sprintf(
				"column %d duplicates column %d (%q)", position, first, name))
		} else {
			seen[name] = position
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidColumns, strings.Join(problems, "; "))
	}

	return nil
}

// firstUnusableRune reports the first rune outside [A-Za-z0-9_], so the error can
// name the character rather than only the column.
func firstUnusableRune(name string) (rune, bool) {
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_':
		default:
			return r, true
		}
	}

	return 0, false
}
