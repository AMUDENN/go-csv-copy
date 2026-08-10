package csvcopy

import (
	"errors"
	"io"
	"iter"
)

/*
Raw streams a file whose shape is only known from its own header.

Every column arrives as text, in the file's order, under the file's names - the
shape of a staging table that a SQL script types afterwards. Nothing here knows
or cares what the columns mean.

It satisfies pgx.CopyFromSource. That interface is not imported: Go interfaces are
structural, so the method set alone is enough and the version of pgx stays the
application's choice.
*/
type Raw struct {
	reader *Reader
	vals   []any
	// strs backs vals under WithPointerValues, and is nil otherwise.
	strs []string
	rows int64
	err  error
	done bool
}

// NewRaw reads the header and prepares a source over the rows after it.
func NewRaw(r io.Reader, opts ...Option) (*Raw, error) {
	reader, err := NewReader(r, opts...)
	if err != nil {
		return nil, err
	}

	source := &Raw{
		reader: reader,
		vals:   make([]any, len(reader.Columns())),
	}

	if reader.settings.pointerValues {
		// Pointed at once, here: from now on a row only writes through them, and
		// no cell is ever boxed again.
		source.strs = make([]string, len(source.vals))
		for i := range source.vals {
			source.vals[i] = &source.strs[i]
		}
	}

	return source, nil
}

/*
Columns is the column list to pass to pgx.CopyFrom, taken from the header.

Empty when the file was empty - check it before creating a table, there is nothing
to load.

The names come out of the file, so they are untrusted input, and the staging-table
pattern puts them on the path to CREATE TABLE. A column called

	x" ); DROP TABLE clients; --

is just a text file someone wrote. Quote every name that reaches a statement with
pgx.Identifier{name}.Sanitize(), or reject the header up front with
ValidateColumns. For CopyFrom itself, pgx quotes them.
*/
func (s *Raw) Columns() []string {
	return s.reader.Columns()
}

// Next advances to the next row, stopping at the end of the file or at the first
// malformed record.
func (s *Raw) Next() bool {
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

	// A record longer than the header is truncated; a shorter one is padded with
	// NULL rather than the empty string, so a column the file stopped short of
	// stays distinguishable from one it left blank.
	for i := range s.vals {
		switch {
		case i >= len(record):
			s.vals[i] = nil
		case s.strs != nil:
			s.strs[i] = record[i]
			s.vals[i] = &s.strs[i]
		default:
			s.vals[i] = record[i]
		}
	}
	s.rows++

	return true
}

/*
Values returns the current row.

The slice is reused between rows, which is safe for pgx.CopyFrom because it
encodes each row before asking for the next. A caller driving the source by hand
must not hold on to it.
*/
func (s *Raw) Values() ([]any, error) {
	return s.vals, nil
}

// Err returns the error that stopped the stream, if any. Check it before the
// error pgx.CopyFrom returns: this one names the line.
func (s *Raw) Err() error {
	return s.err
}

// Rows is the number of rows handed out so far.
func (s *Raw) Rows() int64 {
	return s.rows
}

/*
All iterates the rows, for driving the source by hand rather than handing it to
pgx.CopyFrom.

Stops at the first bad row; Err reports it afterwards. The yielded slice is reused
by the next iteration.

If you want the values as text rather than as []any, range Reader.All instead -
there is no reason to go through the boxing.
*/
func (s *Raw) All() iter.Seq[[]any] {
	return func(yield func([]any) bool) {
		for s.Next() {
			// Values cannot fail. The error in its signature is the shape
			// pgx.CopyFromSource asks for, not something this source can produce.
			values, _ := s.Values()
			if !yield(values) {
				return
			}
		}
	}
}

// Record returns the raw record behind the current row, for error messages.
func (s *Raw) Record() []string {
	return s.reader.Record()
}

// Line is the 1-based line number of the current row.
func (s *Raw) Line() int {
	return s.reader.Line()
}
