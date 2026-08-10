package decode

import (
	"errors"
	"fmt"
	"io"
	"iter"

	csvcopy "github.com/AMUDENN/go-csv-copy"
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
	// The Reader, the value slice and the string array behind it are all shared by
	// a copy, which then reads from the same stream into the same buffers as the
	// original. noCopy makes go vet refuse it - the same reason as on Typed, where
	// the damage is quieter but the shape is identical.
	_ noCopy

	reader *Reader
	vals   []any
	// strs backs vals under WithPointerValues, and is nil otherwise.
	strs      []string
	rows      int64
	err       error
	done      bool
	started   bool
	truncated bool
	extra     int
}

/*
NewRaw reads the header and prepares a source over the rows after it.

Use the pointer it returns. Copying the value would give two sources sharing one
Reader and one row buffer, which is not a shape anything here is written for.
*/
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
copyfrom.ValidateColumns. For CopyFrom itself, pgx quotes them.
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
	s.truncated = len(record) < len(s.vals)
	s.extra = max(len(record)-len(s.vals), 0)
	s.rows++
	s.started = true

	return true
}

/*
Truncated reports whether the record behind the current row ran out before the
header's last column, so the trailing values are NULL because they were absent
rather than because the file left them blank.

Only meaningful after Next returned true, and only ever true under
WithVariableColumns - without it a short record is an error instead.

Unlike Typed.Truncated, this one is not the only way to see it: a NULL in the data
says the same thing. It is here so a caller does not have to go looking through
[]any to find out, and so the two sources answer the same question the same way.
*/
func (s *Raw) Truncated() bool {
	return s.truncated
}

/*
Extra is how many values the current record had beyond the header's columns, all
of which were dropped.

This one cannot be seen any other way. A record wider than the header loses its
tail silently: Values is sized by the header, the extra cells never reach it, and
nothing in the data hints that they existed. And a row that is too wide almost
always means the delimiter or the quoting is being misread - the signal
WithVariableColumns is off by default to preserve, handed back for callers who
turned it on and still want to know:

	for src.Next() {
		if n := src.Extra(); n > 0 {
			log.Warn("dropped values", "line", src.Line(), "count", n)
		}
	}

Zero without WithVariableColumns, where a wide record is an error instead.
*/
func (s *Raw) Extra() int {
	return s.extra
}

/*
Values returns the current row.

The slice is reused between rows, which is safe for pgx.CopyFrom because it
encodes each row before asking for the next. A caller driving the source by hand
must not hold on to it.

Calling it before the first Next is csvcopy.ErrSchema: there is no row yet, and
the slice at that point is one empty value per column - a row that would load
without complaint. pgx.CopyFrom always calls Next first; a caller driving the
source by hand is the one that can get here.
*/
func (s *Raw) Values() ([]any, error) {
	if !s.started {
		return nil, fmt.Errorf("%w: Values called before Next", csvcopy.ErrSchema)
	}

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
			// Values cannot fail here: the only error it has is for being called
			// before the first Next, and this loop calls Next first. The error in
			// its signature is the shape pgx.CopyFromSource asks for.
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
