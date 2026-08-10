package decode

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"unicode/utf8"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

const bomLen = 3

var bomPrefix = [bomLen]byte{0xEF, 0xBB, 0xBF}

/*
Reader is the bottom layer both sources are built on: it strips a UTF-8 BOM,
configures encoding/csv, and reads the header.

It reads one record at a time and never holds more than the current one, which is
what keeps the whole pipeline to a constant amount of memory no matter how large
the file is. It does not close the underlying io.Reader.
*/
type Reader struct {
	cr       *csv.Reader
	budget   *budgetReader
	settings settings
	columns  []string
	record   []string
	line     int
	err      error
}

/*
NewReader reads the header and prepares the reader for row-by-row use.

An empty input is not an error: Columns returns nil and Read returns io.EOF at
once. What an empty file means is the caller's decision, not this package's.

Every option is validated here, including the ones only a Typed will read: a
Reader has no use for WithTag, but one settings type serves all three layers, and
a caller who mistyped an option should learn it from the constructor they called
rather than from the one they call next week.
*/
func NewReader(r io.Reader, opts ...Option) (*Reader, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: reader is nil", csvcopy.ErrSchema)
	}

	set := newSettings(opts)

	if !validDelim(set.comma) {
		return nil, fmt.Errorf("%w: %q is not a usable delimiter", csvcopy.ErrSchema, set.comma)
	}
	// reflect.StructTag.Get("") answers "" for every field, so an empty tag name
	// would bind nothing and read as an untagged struct - the same silent failure
	// taggedFields refuses, one step earlier and with a clearer cause.
	if set.tag == "" {
		return nil, fmt.Errorf("%w: the struct tag name is empty", csvcopy.ErrSchema)
	}
	// Not clamped to "no cap" the way a bad header row is clamped to 1: removing
	// the only bound on this package's memory is not a safe reading of a number
	// that arithmetic produced by accident.
	if set.maxRecordBytes < 0 {
		return nil, fmt.Errorf("%w: the record byte cap is negative (%d); pass 0 to remove it",
			csvcopy.ErrSchema, set.maxRecordBytes)
	}
	if set.comment != 0 {
		if !validDelim(set.comment) {
			return nil, fmt.Errorf("%w: %q is not a usable comment rune", csvcopy.ErrSchema, set.comment)
		}
		if set.comment == set.comma {
			return nil, fmt.Errorf("%w: the comment rune and the delimiter are both %q",
				csvcopy.ErrSchema, set.comma)
		}
	}

	body, err := skipBOM(r)
	if err != nil {
		// The stream failed before a byte of content existed to be malformed, so
		// this is csvcopy.ErrIO and not a bad file.
		return nil, fmt.Errorf("%w: read bom: %w", csvcopy.ErrIO, err)
	}

	var budget *budgetReader
	if set.maxRecordBytes > 0 {
		budget = &budgetReader{r: body, max: set.maxRecordBytes}
		body = budget
	}

	cr := csv.NewReader(body)
	cr.Comma = set.comma
	cr.Comment = set.comment
	cr.LazyQuotes = set.lazyQuotes
	cr.TrimLeadingSpace = set.trimLeadingSpace
	cr.ReuseRecord = true
	// A title above the table is usually narrower than the table itself, so the
	// field count is only pinned once the header has been read.
	cr.FieldsPerRecord = -1

	reader := &Reader{settings: set}

	// The physical line of the last record read in full, which is where an error
	// with no line of its own is attributed to - zero until one has been read.
	line := 0

	for row := 1; row < set.headerRow; row++ {
		record, err := readRecord(cr, budget)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return reader, nil
			}
			return nil, classify(errorLine(line, err), err)
		}
		line = recordLine(cr, record, line+1)
	}

	header, err := readRecord(cr, budget)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return reader, nil
		}
		return nil, classify(errorLine(line, err), err)
	}

	reader.columns = make([]string, len(header))
	for i, name := range header {
		reader.columns[i] = set.normalizeHeader(name)
	}
	reader.cr = cr
	reader.budget = budget
	reader.line = recordLine(cr, header, set.headerRow)

	if !set.variableColumns {
		cr.FieldsPerRecord = len(header)
	}

	return reader, nil
}

/*
Columns returns the header as it was read and normalized, or nil if the input was
empty. The slice is shared, not copied - it is handed straight to pgx.CopyFrom,
which does not modify it.

These names come out of the file and are untrusted input. Normalizing collapses
whitespace; it does not make a name safe to paste into a statement. Before one
reaches DDL, quote it - pgx.Identifier{name}.Sanitize() - or put the header through
copyfrom.ValidateColumns. For CopyFrom, pgx quotes them itself.
*/
func (r *Reader) Columns() []string {
	return r.columns
}

/*
Line is the 1-based line of the file the current record starts on.

It is the physical line, taken from encoding/csv rather than counted here, so it
stays right across the two things that make a record count drift from it: blank
lines, which encoding/csv skips, and a quoted field spanning several lines. That
number is the one an error message must carry - the caller opens the file at it.

After a failed Read it names the record that failed, when encoding/csv could say
which one that was - that is, for csvcopy.ErrParse. It cannot for csvcopy.ErrIO or
ErrRecordTooLarge, since neither arrives as a *csv.ParseError, and there this stays
on the last record read in full: the record that failed starts somewhere after it.
The error message says so in as many words, "after line N" rather than "line N", so
the two cases are told apart in the text and not only here.
*/
func (r *Reader) Line() int {
	return r.line
}

/*
Record returns the record last read, for error messages that need the offending
row rather than just its number.

After a failed Read it holds that record as far as encoding/csv got with it, or
nil where it could not produce one at all.

Only valid until the next Read: the backing slice is reused.
*/
func (r *Reader) Record() []string {
	return r.record
}

/*
Read returns the next record, or io.EOF when there are none left.

The first bad record ends the reader: the error is returned again by every later
call and stays in Err. Rows are never skipped, so a caller cannot resume past a
bad record and mistake a truncated file for a whole one.

The returned slice is reused by the next call. Every error other than io.EOF
says where it happened - see Line - and wraps one of three sentinels:
ErrRecordTooLarge if the record outgrew WithMaxRecordBytes, csvcopy.ErrParse if
the content is malformed, or csvcopy.ErrIO if the stream underneath failed.
*/
func (r *Reader) Read() ([]string, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.cr == nil {
		return nil, io.EOF
	}

	record, err := readRecord(r.cr, r.budget)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		// encoding/csv hands the partial record back along with ErrFieldCount, and
		// its backing array is the previous record's. Keeping the old one here
		// would leave Record holding a row that was never in the file: the fields
		// this record did have, padded out with the last one's leftovers.
		r.line = errorLine(r.line, err)
		r.record = record
		r.err = classify(r.line, err)

		return nil, r.err
	}
	r.line = recordLine(r.cr, record, r.line+1)

	if r.settings.trimValues {
		for i, value := range record {
			record[i] = strings.TrimSpace(value)
		}
	}
	r.record = record

	return record, nil
}

/*
All iterates the records left in the file.

Ranging cannot carry an error out, so the loop stops at the first bad record and
Err reports it afterwards - the same shape as bufio.Scanner:

	for record := range reader.All() {
		...
	}
	if err := reader.Err(); err != nil {
		return err
	}

Breaking out of the loop leaves the reader usable, and the next pull picks up
where the range left off. An error does not: the reader is done, and ranging it
again yields nothing rather than resuming past the record that failed.

The yielded slice is reused by the next iteration. Copy it if it has to outlive
one turn of the loop.
*/
func (r *Reader) All() iter.Seq[[]string] {
	return func(yield func([]string) bool) {
		for {
			record, err := r.Read()
			if err != nil || !yield(record) {
				return
			}
		}
	}
}

// Err returns the error that stopped reading, if any. End of file is not one.
func (r *Reader) Err() error {
	return r.err
}

/*
wrap attributes an error raised while handling the current record to that record's
line.

The default is csvcopy.ErrParse, because the usual reason a convert func fails
is the cell it was given. But convert is the caller's code and can fail for
reasons the file is not responsible for: a lookup table that is unreachable, a
cache that is down, a context that was cancelled while the load ran. Filing
those under csvcopy.ErrParse would send a good file to quarantine on a caller
that follows the package's own advice.

So a category convert already named survives, and a cancelled context is read as
csvcopy.ErrIO without being asked to say so - nobody wraps context.Canceled by
hand, and it is never the file's fault.
*/
func (r *Reader) wrap(err error) error {
	if errors.Is(err, csvcopy.ErrIO) || errors.Is(err, csvcopy.ErrSchema) {
		return fmt.Errorf("line %d: %w", r.line, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: line %d: %w", csvcopy.ErrIO, r.line, err)
	}

	return fmt.Errorf("%w: line %d: %w", csvcopy.ErrParse, r.line, err)
}

/*
validDelim repeats the check encoding/csv makes when it reads its first record. It
governs both the field delimiter and the comment rune, which the standard library
holds to the same rule.

Done here so that an unusable rune is an csvcopy.ErrSchema from the constructor
rather than an csvcopy.ErrParse on the first row: it is a bug in the calling
code, no input file will ever fix it, and a caller that quarantines files on
csvcopy.ErrParse must not act on it. Doing it up front also keeps an empty input
from turning into an error, which the package promises it never is.
*/
func validDelim(delim rune) bool {
	switch delim {
	case 0, '"', '\r', '\n', utf8.RuneError:
		return false
	}

	return utf8.ValidRune(delim)
}

/*
recordLine is the physical line the record just read starts on.

encoding/csv tracks it through blank lines, which it skips, and through a quoted
field spanning several lines - neither of which a record counter here can see.
fallback covers the record that has no field to ask about, which encoding/csv
does not currently produce: a blank line is skipped rather than returned.
*/
func recordLine(cr *csv.Reader, record []string, fallback int) int {
	if len(record) == 0 {
		return fallback
	}

	line, _ := cr.FieldPos(0)

	return line
}

/*
after is the last record read in full, printed as the bound it is.

The distinction is not pedantry. Only encoding/csv can name the line a record
starts on, and it only does so through *csv.ParseError - which carries the line in
its own message, so nothing here has to print it. Every other error leaves the
reader with nothing but the last record it read in full, and the record that failed
starts somewhere after that one: how far after depends on blank lines and on how
many lines the previous record spanned, neither of which is countable here.
Printing that guess as "line N" would send the caller to a line that has nothing to
do with the failure.

Zero means no record was ever read, so there is not even a bound to give.
*/
type after int

func (a after) String() string {
	if a > 0 {
		return fmt.Sprintf("after line %d", int(a))
	}

	return "at the start of the file"
}

// errorLine prefers the line encoding/csv reports, which is accurate even when a
// quoted field spans several physical lines. fallback is the last record read in
// full, which is a lower bound rather than an answer - classify prints it as one.
func errorLine(fallback int, err error) int {
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		return parseErr.Line
	}

	return fallback
}

/*
readRecord pulls one record, giving it a fresh byte budget first.

Per record, not per file: a legitimate multi-line quoted field gets the whole
budget of its own, and only a record that never ends runs out.
*/
func readRecord(cr *csv.Reader, budget *budgetReader) ([]string, error) {
	if budget != nil {
		budget.reset()
	}

	return cr.Read()
}

/*
classify decides whose fault an error from encoding/csv is, and attributes it to a
line.

The rule rests on how encoding/csv reports: everything the parser itself objects to
arrives as a *csv.ParseError, and anything else it hands back came from the
underlying io.Reader unchanged. So the shape of the error is the evidence, and the
three outcomes are the three sentinels.

The order matters. The byte budget is enforced by a reader, which makes it look like
an I/O failure, but a record too large to read is a property of the file - it goes
to ErrRecordTooLarge, not csvcopy.ErrIO.

A csv.ParseError already names the line in its own message, so repeating it here
would print it twice - "line 12: record on line 12: ..." - for the error type that
produces most of these. The other two carry whatever the reader can honestly say
about where they happened, which is usually "after line N" rather than a line.
*/
func classify(line int, err error) error {
	if errors.Is(err, errRecordTooLarge) {
		return fmt.Errorf("%w: %s", ErrRecordTooLarge, after(line))
	}

	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) {
		return fmt.Errorf("%w: %w", csvcopy.ErrParse, err)
	}

	return fmt.Errorf("%w: %s: %w", csvcopy.ErrIO, after(line), err)
}

/*
skipBOM drops a leading UTF-8 BOM.

io.ReadFull, not Read: a Reader is allowed to return fewer bytes than asked for
without an error, and a plain Read would then leave the BOM in place and hand a
first column name starting with U+FEFF to the header matcher. That column matches
no tag, and the failure surfaces far from its cause.
*/
func skipBOM(r io.Reader) (io.Reader, error) {
	var prefix [bomLen]byte

	n, err := io.ReadFull(r, prefix[:])
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		// Fewer than three bytes in total: too short to be a BOM with data after it.
		return bytes.NewReader(prefix[:n]), nil
	case err != nil:
		return nil, err
	}

	if prefix == bomPrefix {
		return r, nil
	}

	return io.MultiReader(bytes.NewReader(prefix[:]), r), nil
}
