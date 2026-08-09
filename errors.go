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
ErrSchema reports wiring that cannot work whatever the file holds: a struct that
cannot be decoded into at all - not a struct, a tagged field that is not a string,
a tagged field that is not exported - a nil reader or convert, or a delimiter
encoding/csv will not accept.

It deliberately does not wrap ErrParse. No input file will ever fix it, so a
caller that retries or quarantines files on ErrParse should not treat it as a bad
file - it is a bug in the calling code.
*/
var ErrSchema = errors.New("csv schema")
