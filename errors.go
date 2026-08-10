package csvcopy

import "errors"

/*
ErrParse is wrapped by every error that a file can cause: a malformed record, a
header that does not match the struct, a header name that cannot be used to build
a statement, a failing convert. An application can therefore alias its own sentinel
to it and keep its existing errors.Is checks working:

	var ErrBadFile = csvcopy.ErrParse

A failing convert is the one case where the category can be overridden: an error
from convert that already wraps ErrIO or ErrSchema keeps it. See decode.NewTyped.
*/
var ErrParse = errors.New("csv parse")

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
ErrSchema reports wiring that cannot work whatever the file holds: a struct that
cannot be decoded into at all - not a struct, no tags at all, a tagged field that
is not a string or not exported, two fields asking for one column, a tag inside an
embedded struct where it would bind to nothing - a nil reader, convert, src or
encode, an empty tag name, a delimiter encoding/csv will not accept, or a value
asked of a source before it produced one.

It deliberately does not wrap ErrParse. No input file will ever fix it, so a
caller that retries or quarantines files on ErrParse should not treat it as a bad
file - it is a bug in the calling code.
*/
var ErrSchema = errors.New("csv schema")
