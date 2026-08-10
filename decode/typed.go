package decode

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"reflect"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

/*
Typed streams a file whose shape is fixed by a struct.

S is the row as it appears in the file - every field a string, tagged with the
column name it comes from. D is what the rest of the program works with, produced
by convert. The split is deliberate: only the caller can decide whether an empty
cell is a NULL or a zero, and whether a bad value fails the file or the field, so
this package never converts anything itself.

Typed satisfies copyfrom.RowSource[D], so decoding and the layout of a COPY row can
live in different layers: one hands out a RowSource, and the layer that owns the
database decides which columns it lands in. This package never learns which.
*/
type Typed[S any, D any] struct {
	// plan holds pointers into row, so a copy of this struct would decode into the
	// original's fields while convert read its own. noCopy makes go vet say so.
	_ noCopy

	reader  *Reader
	plan    decodePlan
	unused  []string
	convert func(*S) (D, error)

	row       S
	current   D
	rows      int64
	err       error
	done      bool
	truncated bool
	extra     int
}

/*
NewTyped reads the header, binds it to S's tags and prepares a source over the
rows after it.

convert is called once per row with a pointer to a struct that is reused for the
whole file, so it must not keep that pointer. Returning an error from it stops the
stream, and the error is reported with the line it came from.

That error is wrapped in csvcopy.ErrParse, since the usual reason a row fails to
convert is the row. Where that is wrong - convert reached a lookup table that
was down, and the file is fine - return an error wrapping csvcopy.ErrIO and it
stays csvcopy.ErrIO, so a caller that quarantines files on csvcopy.ErrParse does
not quarantine this one. csvcopy.ErrSchema is kept the same way. A cancelled
context needs no wrapping: an error carrying context.Canceled or
context.DeadlineExceeded is reported as csvcopy.ErrIO.

Only the fields a column bound to are written before each call. A field no tag
asked for - untagged, or tagged "-" - is never touched, so whatever convert leaves
in one it will see again on the next row.

Use the pointer this returns; the value behind it must not be copied. The decode
plan holds the addresses of that struct's own fields, so a copy would decode into
the original while convert read the copy - every field empty, on every row, with
nothing reporting it. go vet refuses the copy, which is why noCopy is embedded.
*/
func NewTyped[S any, D any](r io.Reader, convert func(*S) (D, error), opts ...Option) (*Typed[S, D], error) {
	if convert == nil {
		return nil, fmt.Errorf("%w: convert is nil", csvcopy.ErrSchema)
	}

	reader, err := NewReader(r, opts...)
	if err != nil {
		return nil, err
	}

	source := &Typed[S, D]{reader: reader, convert: convert}

	// An empty file has no header to bind to, but the struct is still checked:
	// a tag on a non-string field is a bug that should not wait for a file with
	// rows in it to show up.
	if reader.Columns() == nil {
		if _, err = taggedFields[S](reader.settings); err != nil {
			return nil, err
		}
		source.done = true

		return source, nil
	}

	bindings, unused, err := buildPlan[S](reader.Columns(), reader.settings)
	if err != nil {
		return nil, err
	}
	source.unused = unused
	// Resolved against source.row here rather than per row: source is on the heap
	// and its fields do not move, so the addresses are good for the whole file.
	source.plan = bindPlan(bindings, reflect.ValueOf(&source.row).Elem())

	return source, nil
}

// Next advances to the next row, stopping at the end of the file, at the first
// malformed record, or at the first row convert rejects.
func (s *Typed[S, D]) Next() bool {
	if s.err != nil || s.done {
		return false
	}

	record, err := s.reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			s.done = true
		} else {
			s.err = err
		}
		return false
	}

	s.truncated = s.plan.apply(record)
	s.extra = max(len(record)-len(s.reader.Columns()), 0)

	value, err := s.convert(&s.row)
	if err != nil {
		s.err = s.reader.wrap(err)
		return false
	}

	s.current = value
	s.rows++

	return true
}

// Value returns the row Next produced. Only meaningful after Next returned true.
func (s *Typed[S, D]) Value() D {
	return s.current
}

// Err returns the error that stopped the stream, if any.
func (s *Typed[S, D]) Err() error {
	return s.err
}

// Rows is the number of rows handed out so far. Zero after a full pass means the
// file held nothing, which is not in itself an error.
func (s *Typed[S, D]) Rows() int64 {
	return s.rows
}

/*
All iterates the decoded rows.

This is the layer that is useful with no database in sight: CSV in, your own type
out, one row at a time. Stops at the first row that fails to parse or convert, and
Err reports it afterwards:

	for client := range rows.All() {
		...
	}
	if err := rows.Err(); err != nil {
		return err
	}

Unlike the other All methods, what is yielded here is whatever convert returned,
so nothing is reused behind your back.
*/
func (s *Typed[S, D]) All() iter.Seq[D] {
	return func(yield func(D) bool) {
		for s.Next() {
			if !yield(s.Value()) {
				return
			}
		}
	}
}

/*
Truncated reports whether the record behind the current row ran out before a bound
column, so that field holds "" because the value was absent rather than empty.

Only meaningful after Next returned true, and only ever true under
WithVariableColumns - without it a short record is an error instead.

It exists because the distinction is invisible where it matters most. Raw can put
nil in an []any and get SQL NULL; a tagged field is declared string, so an absent
value and an empty one both arrive as "", and in Postgres a NULL and an empty string
are different values. Read it after Next if that difference matters:

	for source.Next() {
		if source.Truncated() {
			log.Warn("short record", "line", source.Line())
		}
	}

convert cannot see this. It is called inside Next, with the struct as its only
argument, and adding a second one would change the signature every caller writes.
If a row needs to be rejected for being short, reject it here.
*/
func (s *Typed[S, D]) Truncated() bool {
	return s.truncated
}

/*
Extra is how many values the current record had beyond the header's columns, all
of which were dropped.

The counterpart to Truncated, for the other end of the record, and the one that
cannot be seen any other way here. A tag can only ask for a column the header
names, so values past the last one bind to nothing and never reach the struct -
Record shows the raw record, but nothing in the decoded row says it was longer
than the file said it would be.

That matters because a record wider than its header almost always means the
delimiter or the quoting is being misread, which is the signal
WithVariableColumns turns off. Read it in the loop if you want it back:

	for source.Next() {
		if n := source.Extra(); n > 0 {
			log.Warn("dropped values", "line", source.Line(), "count", n)
		}
	}

Only meaningful after Next returned true, and zero without WithVariableColumns,
where a wide record is an error instead.
*/
func (s *Typed[S, D]) Extra() int {
	return s.extra
}

// Header returns the header as read and normalized, or nil if the file was empty.
func (s *Typed[S, D]) Header() []string {
	return s.reader.Columns()
}

/*
Unused returns the header columns no tag bound to.

Worth logging as a warning: when an export renames a column, the tag stops
matching it, no error is raised, and the column that was renamed away is the only
trace left.

The slice is shared, not copied. It is built once per file and never written
again, so reading it is safe; do not modify it.
*/
func (s *Typed[S, D]) Unused() []string {
	return s.unused
}

// Record returns the raw record behind the current row, for error messages.
func (s *Typed[S, D]) Record() []string {
	return s.reader.Record()
}

// Line is the 1-based line number of the current row.
func (s *Typed[S, D]) Line() int {
	return s.reader.Line()
}
