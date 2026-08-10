package decode

import (
	"errors"
	"fmt"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

/*
ErrMissingColumns reports columns that a csv tag asked for and the header does
not have.

This is fatal rather than a warning: a field bound to nothing would read as the
empty string on every row and reach the database as NULL, quietly wiping the
column it was supposed to fill. WithAllowMissingColumns lifts the check for
callers who accept that risk.
*/
var ErrMissingColumns = fmt.Errorf("%w: header is missing columns", csvcopy.ErrParse)

/*
ErrDuplicateColumns reports a column a csv tag asked for that the header carries
more than once.

The mirror image of two fields asking for one column, which is csvcopy.ErrSchema:
there the struct is ambiguous, here the file is, and neither can be resolved by
picking one. Binding to the first occurrence would let the file's column order
decide which values are loaded, silently.

Normalizing makes this reachable without a literally duplicated header: the
default NormalizeSpace collapses "a  b" and "a b" into one name. A duplicate no
tag asks for is not an error - it stays in Unused, as it always did.
*/
var ErrDuplicateColumns = fmt.Errorf("%w: header has duplicate columns", csvcopy.ErrParse)

/*
errRecordTooLarge is the raw signal budgetReader hands to encoding/csv. It travels
through the parser and is turned into ErrRecordTooLarge, with a location, by the
reader.
*/
var errRecordTooLarge = errors.New("record exceeds the byte limit")

/*
ErrRecordTooLarge reports a single record longer than WithMaxRecordBytes allows.

Almost always an unclosed quote: encoding/csv then reads to the end of the file
looking for the closing one, and the whole file becomes a single field. The limit
is what keeps memory bounded on input the caller did not write.

It wraps csvcopy.ErrParse, and there is one case where that is generous to itself:
the budget covers a whole Read, and encoding/csv skips blank and comment lines
inside one, so a run of them longer than the cap arrives here although no record in
the file is oversized. Unreachable at the 64 MiB default and reachable if you
tighten the cap - see WithMaxRecordBytes, which explains why the obvious fix would
reopen the hole the budget closes. If you quarantine files on ErrParse and run a
small cap, this is the one error to think about before doing so.
*/
var ErrRecordTooLarge = fmt.Errorf("%w: %w", csvcopy.ErrParse, errRecordTooLarge)
