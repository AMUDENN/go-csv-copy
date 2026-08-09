package csvcopy

import (
	"errors"
	"fmt"
)

/*
ErrParse is wrapped by every error that a file can cause: a malformed record, a
header that does not match the struct, a failing convert. An application can
therefore alias its own sentinel to it and keep its existing errors.Is checks
working:

	var ErrBadFile = csvcopy.ErrParse
*/
var ErrParse = errors.New("csv parse")

/*
ErrMissingColumns reports columns that a csv tag asked for and the header does
not have.

This is fatal rather than a warning: a field bound to nothing would read as the
empty string on every row and reach the database as NULL, quietly wiping the
column it was supposed to fill. WithAllowMissingColumns lifts the check for
callers who accept that risk.
*/
var ErrMissingColumns = fmt.Errorf("%w: header is missing columns", ErrParse)

/*
ErrIO is wrapped by every error the input stream causes rather than the file's
content: a connection dropped mid-read, a cancelled context, a disk that failed.
The cause is left in the chain, so errors.Is(err, context.Canceled) still answers.

It deliberately does not wrap ErrParse. A caller that quarantines a file on
ErrParse must not quarantine a good file because the network blinked - the file is
fine, the read is not, and the right response is to try again rather than to give
up on the input.
*/
var ErrIO = errors.New("csv read")

/*
errRecordTooLarge is the raw signal budgetReader hands to encoding/csv. It travels
through the parser and is turned into ErrRecordTooLarge, with a line, by the
reader.
*/
var errRecordTooLarge = errors.New("record exceeds the byte limit")

/*
ErrRecordTooLarge reports a single record longer than WithMaxRecordBytes allows.

Almost always an unclosed quote: encoding/csv then reads to the end of the file
looking for the closing one, and the whole file becomes a single field. The limit
is what keeps memory bounded on input the caller did not write.
*/
var ErrRecordTooLarge = fmt.Errorf("%w: %w", ErrParse, errRecordTooLarge)

/*
ErrSchema reports wiring that cannot work whatever the file holds: a struct that
cannot be decoded into at all - not a struct, a tagged field that is not a string
or not exported, two fields asking for one column, a tag inside an embedded struct
where it would bind to nothing - a nil reader, convert, src or encode, or a
delimiter encoding/csv will not accept.

It deliberately does not wrap ErrParse. No input file will ever fix it, so a
caller that retries or quarantines files on ErrParse should not treat it as a bad
file - it is a bug in the calling code.
*/
var ErrSchema = errors.New("csv schema")
