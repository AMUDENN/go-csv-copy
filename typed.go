package csvcopy

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"reflect"
)

/*
Typed streams a file whose shape is fixed by a struct.

S is the row as it appears in the file - every field a string, tagged with the
column name it comes from. D is what the rest of the program works with, produced
by convert. The split is deliberate: only the caller can decide whether an empty
cell is a NULL or a zero, and whether a bad value fails the file or the field, so
this package never converts anything itself.

Typed satisfies RowSource[D], so decoding and the layout of a COPY row can live in
different layers: one hands out a RowSource, and the layer that owns the database
decides which columns it lands in.
*/
type Typed[S any, D any] struct {
	reader  *Reader
	plan    decodePlan
	unused  []string
	convert func(*S) (D, error)

	row     S
	rowVal  reflect.Value
	current D
	rows    int64
	err     error
	done    bool
}

/*
NewTyped reads the header, binds it to S's tags and prepares a source over the
rows after it.

convert is called once per row with a pointer to a struct that is reused for the
whole file, so it must not keep that pointer. Returning an error from it stops the
stream, and the error is reported with the line it came from.

Only the fields a column bound to are written before each call. A field no tag
asked for - untagged, or tagged "-" - is never touched, so whatever convert leaves
in one it will see again on the next row.
*/
func NewTyped[S any, D any](r io.Reader, convert func(*S) (D, error), opts ...Option) (*Typed[S, D], error) {
	if convert == nil {
		return nil, fmt.Errorf("%w: convert is nil", ErrSchema)
	}

	reader, err := NewReader(r, opts...)
	if err != nil {
		return nil, err
	}

	source := &Typed[S, D]{reader: reader, convert: convert}
	source.rowVal = reflect.ValueOf(&source.row).Elem()

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

	source.plan, source.unused, err = buildPlan[S](reader.Columns(), reader.settings)
	if err != nil {
		return nil, err
	}

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

	s.plan.apply(record, s.rowVal)

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
