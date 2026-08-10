package copyfrom

import (
	"fmt"
	"strings"

	csvcopy "github.com/AMUDENN/go-csv-copy"
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

Wraps csvcopy.ErrParse: it is a property of the file, and the caller who
quarantines bad files wants this one quarantined too.
*/
var ErrInvalidColumns = fmt.Errorf("%w: header has unusable column names", csvcopy.ErrParse)

/*
ValidateColumns rejects a header whose names cannot safely be used to build a
statement.

A staging table is created from the file's own column names, which makes those
names untrusted input on the path to CREATE TABLE. A file with a column called

	x" ); DROP TABLE clients; --

is not a hypothetical; it is a text file, and anyone can write one. This refuses
the shapes that break a statement or change its meaning even after quoting is
considered: an empty name, a duplicate, two names differing only in case, a name
over 63 bytes, one starting with a digit, and anything outside [A-Za-z0-9_].

The case rule is the one that needs a word. Quoted, "ID" and "id" are two legal
columns; unquoted, Postgres folds both to id and the CREATE TABLE fails with
`column "id" specified more than once` - which is the statement this function is
asked about. A header that stays unambiguous only as long as nobody drops the
quotes is a header to map explicitly, the same argument that refuses prose names.

Every violation is reported at once rather than the first one, so one run tells the
whole story of the file instead of one name per attempt.

What it does not know is the reserved words of your Postgres version - select,
table, user and the rest, which are a moving list this package would have to carry
and keep current for a gain quoting already provides. That is one more reason the
rule below holds rather than an exception to it.

Deliberately strict otherwise, and it rejects more than injection: "date of birth"
and any non-ASCII name are refused too. A file whose column names are prose is a
file whose names should be mapped explicitly, not quoted and hoped for. Passing
this is not a licence to skip quoting - build identifiers with
pgx.Identifier{name}.Sanitize() regardless. For CopyFrom, pgx quotes them itself.

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

		// A bare 123 passes every other rule here and still cannot start a column
		// definition: CREATE TABLE t (123 TEXT) is a syntax error, quoted or not,
		// unless the quotes are there - which is the rule below, restated.
		if first := name[0]; first >= '0' && first <= '9' {
			problems = append(problems, fmt.Sprintf(
				"column %d (%q) starts with a digit", position, name))
		}

		if bad, ok := firstUnusableRune(name); ok {
			problems = append(problems, fmt.Sprintf(
				"column %d (%q) contains %q", position, name, bad))
		}

		// Folded, not compared byte for byte. An unquoted identifier is lowered by
		// Postgres, so "ID" and "id" are one column to a CREATE TABLE that does not
		// quote - and CREATE TABLE t (ID TEXT, id TEXT) fails with "column \"id\"
		// specified more than once", which is the statement this function is asked
		// about. Quoted they are two legal columns, but a header that needs the
		// quoting to stay unambiguous is a header to map explicitly, which is the
		// same rule that refuses prose names.
		//
		// Only ASCII case folds here: everything outside [A-Za-z0-9_] was already
		// reported above, so there is nothing left for a Unicode-aware fold to do.
		folded := strings.ToLower(name)
		if first, taken := seen[folded]; taken {
			problems = append(problems, describeCollision(position, first, name, columns[first-1]))
		} else {
			seen[folded] = position
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidColumns, strings.Join(problems, "; "))
	}

	return nil
}

/*
describeCollision words the two ways a column can be taken twice differently,
because the fixes are different.

An exact duplicate is a duplicate in anyone's reading. Two names that differ only
in case are two columns until something drops the quotes, and a reader who is told
"duplicates column 2" while looking at "ID" and "id" will think the check is
broken rather than that Postgres folds.
*/
func describeCollision(position, first int, name, existing string) string {
	if name == existing {
		return fmt.Sprintf("column %d duplicates column %d (%q)", position, first, name)
	}

	return fmt.Sprintf("column %d (%q) collides with column %d (%q) unless both are quoted",
		position, name, first, existing)
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
