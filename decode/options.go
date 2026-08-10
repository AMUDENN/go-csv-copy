package decode

import "strings"

// Option configures a Reader and everything built on top of it.
type Option func(*settings)

type settings struct {
	comma            rune
	comment          rune
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
		pointerValues:    true,
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

A quote, a carriage return, a newline and an invalid rune cannot delimit
anything encoding/csv is willing to read, so the constructor rejects them with
csvcopy.ErrSchema rather than letting the first row fail with csvcopy.ErrParse.
*/
func WithComma(comma rune) Option {
	return func(s *settings) { s.comma = comma }
}

/*
WithComment sets a rune that starts a comment line. Zero, the default, means the
file has no comments.

A line whose first rune is this one is skipped entirely, wherever it appears -
which is what WithHeaderRow cannot do, since that only drops a fixed number of
lines at the top.

Validated like the delimiter, and for the same reason: a comment rune equal to
the delimiter, or one encoding/csv will not accept, is csvcopy.ErrSchema from
the constructor rather than csvcopy.ErrParse on the first row. Skipped lines do
not shift the line numbers in errors - those come from encoding/csv, which
counts the physical file.
*/
func WithComment(comment rune) Option {
	return func(s *settings) { s.comment = comment }
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

/*
WithTrimValues applies strings.TrimSpace to every value. Defaults to true.

It does not exempt quoted fields, and that is the one surprise in it. Quoting is
how a CSV says "these spaces are data", so `"  x  "` arrives as "x" and a field of
three deliberate spaces arrives as "" - which, in a column the caller treats as
nullable, is a different value from what the file held.

The default follows the target case: these files come out of spreadsheet exports
where padding is alignment rather than content. Turn it off where the padding is
data - a fixed-width export, a column of codes - and values come through byte for
byte.
*/
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

/*
WithNormalizeHeader replaces the header normalizer. Passing nil restores identity.
Defaults to NormalizeSpace.

It runs on both sides of the match: on every column name read from the file, and on
every csv tag value read from a struct. That is what keeps a normalizer like
strings.ToLower working - lowering only the file's names would stop them matching
tags written in any other case.

So it has to be pure and idempotent. It is called once per column and once per
tagged field when a plan is built, never on the row path, and a function that
returns different answers for the same input turns column matching into a
coin toss.
*/
func WithNormalizeHeader(fn func(string) string) Option {
	return func(s *settings) { s.normalizeHeader = fn }
}

/*
WithTag sets the struct tag Typed reads column names from. Defaults to "csv".

The whole tag is the column name. There are no comma-separated options - a field
tagged `csv:"name,omitempty"` asks for a column literally called
"name,omitempty", which no header will have. The only value with a meaning of its
own is "-", which drops the field.

An empty name is csvcopy.ErrSchema from the constructor:
reflect.StructTag.Get("") answers "" for every field, so nothing would bind and
every row would decode as empty. A struct with no field carrying this tag is
csvcopy.ErrSchema too - the same failure, from the other side, and typically
`json:"..."` where `csv:"..."` was meant.
*/
func WithTag(tag string) Option {
	return func(s *settings) { s.tag = tag }
}

/*
WithVariableColumns accepts rows whose field count differs from the header's.
Extra values are dropped; missing trailing ones become NULL in Raw and "" in Typed.

That difference is real, not a wording slip. Raw hands pgx an []any and can put nil
there, which is SQL NULL. A tagged field in Typed is declared string, so there is no
nil to assign and an absent value is indistinguishable from an empty one - and in
Postgres a NULL and an empty string are different values. Typed.Truncated reports
which case the current row is, since convert cannot see it.

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
WithPointerValues controls whether Raw hands out *string or string. Defaults to
true, which is *string.

Putting a string into an []any boxes it, and boxing a string always allocates 16
bytes for its header, so plain strings cost one allocation per cell. A pointer is
pointer-shaped: the interface holds it directly and boxing is free. The strings live
in one array allocated per file, and a value the row stopped short of is a nil
interface, still NULL. On five columns that is 6 allocations per row against 1; on
thirty it is 31 against 1.

The default is *string because pgx cannot tell the difference, and that is a
measured result rather than an argument: the integration tests load the same file
both ways into an all-TEXT table and compare an md5 of the rows, and load both ways
into a table of bigint, numeric and date. Both pass.

Pass false if you drive the source yourself and want plain strings - a type switch
over []any is easier to write against string than *string. Nothing else in the
package is affected: Typed decodes into your struct fields, and in copyfrom.Copy
the boxing happens inside your own encode.
*/
func WithPointerValues(pointers bool) Option {
	return func(s *settings) { s.pointerValues = pointers }
}

/*
WithMaxRecordBytes caps how large one record may be. Zero removes the cap.
Defaults to 64 MiB.

A negative value is csvcopy.ErrSchema from the constructor, not a second way of
spelling zero. It is almost always arithmetic on a config gone wrong - a byte count
computed from an unset field, a subtraction, an overflow - and reading that as
"remove the only bound on this package's memory" is not a safe thing to do
silently. Pass 0 to mean it.

The cap is what makes "memory does not depend on the size of the file" true for
input nobody checked. encoding/csv assembles a record in one buffer and has no
limit of its own, so a field that opens a quote and never closes it is read to the
end of the file and the whole file becomes one value. Exceeding the cap is
ErrRecordTooLarge.

The cap is on the record, not on the process: peak memory while one oversized
record is being read is up to about 4x it - 263 MiB measured against the 64 MiB
default. encoding/csv holds the physical line in one buffer and the assembled
record in another, both grown by doubling, and a doubling has the old array and
the new one live at the same time. Size the cap against the memory you can afford
divided by four, not against the memory you can afford.

The count itself is approximate too, in the other direction: csv.Reader buffers
ahead, so bytes drawn from the input and bytes that ended up in the record differ
by up to one buffer. It is a bound on memory, not a byte count to assert against.

A small cap also caps the read sizes underneath it - a Read that would overrun the
remaining budget is trimmed to what is left. Irrelevant at 64 MiB; at something
like 64 KiB it means the reader below sees short reads, which is worth knowing if
it is a network connection.

The budget is per Read rather than per line, and encoding/csv skips blank lines and
comment lines inside one - so a run of them longer than the cap is reported as
ErrRecordTooLarge even though no single record is oversized. Since that error wraps
csvcopy.ErrParse, a caller that quarantines files on ErrParse would quarantine a
good one. Invisible at 64 MiB; worth knowing before tightening the cap on a file
that carries comment blocks or long runs of blank lines. See budgetReader for why
resetting the budget per line would be a worse trade than this is.
*/
func WithMaxRecordBytes(n int64) Option {
	return func(s *settings) { s.maxRecordBytes = n }
}
