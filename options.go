package csvcopy

import "strings"

// Option configures a Reader and everything built on top of it.
type Option func(*settings)

type settings struct {
	comma            rune
	lazyQuotes       bool
	trimLeadingSpace bool
	trimValues       bool
	headerRow        int
	normalizeHeader  func(string) string
	tag              string
	variableColumns  bool
	allowMissing     bool
	pointerValues    bool
	maxRecordBytes   int64
}

/*
defaultMaxRecordBytes bounds one record at 64 MiB.

No honest row comes near it - a thousand columns of 64 KiB each would still fit -
and an unclosed quote runs into it immediately. Sized to be invisible in normal
use and to catch the one input that is not normal.
*/
const defaultMaxRecordBytes = 64 << 20

func newSettings(opts []Option) settings {
	set := settings{
		comma:            ';',
		lazyQuotes:       false,
		trimLeadingSpace: true,
		trimValues:       true,
		headerRow:        1,
		normalizeHeader:  NormalizeSpace,
		tag:              "csv",
		maxRecordBytes:   defaultMaxRecordBytes,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&set)
		}
	}
	if set.headerRow < 1 {
		set.headerRow = 1
	}
	if set.normalizeHeader == nil {
		set.normalizeHeader = func(s string) string { return s }
	}
	return set
}

/*
NormalizeSpace is the default header normalizer: it collapses every run of
whitespace into a single space and trims the ends.

Exported column names arrive wrapped across lines or padded for alignment, so
"date of\n  birth" and "date of birth" have to resolve to the same column.
*/
func NormalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

/*
WithComma sets the field delimiter. Defaults to ';'.

A quote, a carriage return, a newline and an invalid rune cannot delimit anything
encoding/csv is willing to read, so the constructor rejects them with ErrSchema
rather than letting the first row fail with ErrParse.
*/
func WithComma(comma rune) Option {
	return func(s *settings) { s.comma = comma }
}

/*
WithLazyQuotes tolerates quoting encoding/csv would otherwise reject: a bare quote
inside an unquoted field, and a quoted field that never closes. Defaults to false.

Turning it on trades a precise error for silent damage. `1;"2"3;4` becomes the
field `2"3` with the right number of fields, so nothing objects. An unclosed quote
is worse: the parser reads to EOF looking for the closing one and the entire rest
of the file arrives as a single value. What surfaces then is a field-count error
naming the line the file ended on rather than the line the quote opened on - and
with WithVariableColumns there is no error at all.

Off, the same input is a parse error naming the line and the column of the quote.
*/
func WithLazyQuotes(lazy bool) Option {
	return func(s *settings) { s.lazyQuotes = lazy }
}

// WithTrimLeadingSpace drops white space at the start of a field. Defaults to true.
func WithTrimLeadingSpace(trim bool) Option {
	return func(s *settings) { s.trimLeadingSpace = trim }
}

// WithTrimValues applies strings.TrimSpace to every value. Defaults to true.
func WithTrimValues(trim bool) Option {
	return func(s *settings) { s.trimValues = trim }
}

/*
headerRowLimit caps WithHeaderRow so the count stays a usable int on every
platform, 32-bit ones included. A header a million rows down is the empty-file
case long before the cap could matter.
*/
const headerRowLimit = 1 << 20

/*
WithHeaderRow sets which row holds the header, counting from 1. Rows before it
are read and dropped, so a file may carry a title or a note above its table.
Defaults to 1.

A header past the end of the file is the empty-file case, not an error.
*/
func WithHeaderRow(row uint) Option {
	return func(s *settings) { s.headerRow = int(min(row, headerRowLimit)) }
}

// WithNormalizeHeader replaces the header normalizer. Passing nil restores
// identity. Defaults to NormalizeSpace.
func WithNormalizeHeader(fn func(string) string) Option {
	return func(s *settings) { s.normalizeHeader = fn }
}

/*
WithTag sets the struct tag Typed reads column names from. Defaults to "csv".

The whole tag is the column name. There are no comma-separated options - a field
tagged `csv:"name,omitempty"` asks for a column literally called
"name,omitempty", which no header will have. The only value with a meaning of its
own is "-", which drops the field.
*/
func WithTag(tag string) Option {
	return func(s *settings) { s.tag = tag }
}

/*
WithVariableColumns accepts rows whose field count differs from the header's.
Missing trailing values become NULL and extra ones are dropped.

Off by default: a row that is the wrong width usually means the delimiter or the
quoting is misread, and failing beats loading shifted data.
*/
func WithVariableColumns(variable bool) Option {
	return func(s *settings) { s.variableColumns = variable }
}

/*
WithAllowMissingColumns downgrades a missing tagged column from an error to
silence.

Dangerous: the field will read as empty on every row and reach the database as
NULL, wiping whatever that column held. Only use it where that is acceptable.
*/
func WithAllowMissingColumns(allow bool) Option {
	return func(s *settings) { s.allowMissing = allow }
}

/*
WithPointerValues makes Raw hand out *string instead of string, which removes one
allocation per column per row.

Putting a string into an []any boxes it, and boxing a string always allocates 16
bytes for its header - so the default costs one allocation per cell, which on a
wide file dwarfs everything else. A pointer is pointer-shaped: the interface holds
it directly and boxing is free. The strings live in one array allocated per file,
and a value the row stopped short of is a nil interface, still NULL.

Off by default on purpose. pgx dereferences *T through its pointer encode plan and
*string is the ordinary way to pass a nullable text value, so this should be
transparent - but "should" is not "measured against a real database". Turn it on,
load a real file, compare the result, then make it the default.

Affects Raw only. In Copy the boxing happens inside your own encode func.
*/
func WithPointerValues(pointers bool) Option {
	return func(s *settings) { s.pointerValues = pointers }
}

/*
WithMaxRecordBytes caps how large one record may be. Zero, or anything negative,
removes the cap. Defaults to 64 MiB.

The cap is what makes "memory does not depend on the size of the file" true for
input nobody checked. encoding/csv assembles a record in one buffer and has no
limit of its own, so a field that opens a quote and never closes it is read to the
end of the file and the whole file becomes one value. Exceeding the cap is
ErrRecordTooLarge.

The bound is approximate: csv.Reader buffers ahead, so the accounting is off by up
to one buffer. It is an upper bound on memory, not a byte count to assert against.
*/
func WithMaxRecordBytes(n int64) Option {
	return func(s *settings) {
		if n < 0 {
			n = 0
		}
		s.maxRecordBytes = n
	}
}
