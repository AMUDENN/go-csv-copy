package copyfrom

import (
	"fmt"

	csvcopy "github.com/AMUDENN/go-csv-copy"
)

/*
RowSource is anything that yields values one at a time: decode.Typed, or a source
of your own over XLSX, an API or a generator. Nothing in this package knows about
CSV, and that is the point of it being its own package.

The interface is a pull, not a push, and that is the point. pgx.CopyFrom drives the
loop itself - it asks for a row, encodes it into its send buffer, and only then
asks for the next one. A source that pushes rows into a channel or a callback would
need a goroutine between it and CopyFrom, and with it error handling across a
goroutine boundary inside an open transaction.
*/
type RowSource[T any] interface {
	Next() bool
	Value() T
	Err() error
}

/*
Copy adapts a RowSource to pgx.CopyFromSource.

Only the mapping of a value onto its columns lives here; where the values came
from is none of this layer's business.
*/
type Copy[T any] struct {
	// A copy shares the row buffer with the original, so two Values calls overwrite
	// each other's slice, and each copy counts its own rows while the caller holds
	// one of them. noCopy makes go vet refuse it.
	_ noCopy

	src    RowSource[T]
	encode func(dst []any, item T) []any
	// Resolved in NewCopy rather than per call: whether a source can name a line
	// is a property of its type, so asking it again on every error is work that
	// can only ever produce the same answer. nil when it cannot.
	lines   interface{ Line() int }
	records interface{ Record() []string }

	row     []any
	rows    int64
	started bool
}

/*
NewCopy wraps src, laying each value out with encode.

columns is how many values encode appends; it only sizes the backing slice, so
being wrong costs an allocation rather than correctness. encode must append in the
same order as the column list passed to pgx.CopyFrom - the two are a pair, and
Postgres cannot notice when they disagree if the types are compatible.

A nil src or encode is csvcopy.ErrSchema, the same as a nil reader or convert
elsewhere in the package: it is wiring the calling code got wrong, and no input
file will fix it.

Use the pointer it returns. Copying the value gives two sources sharing one row
buffer and one counter, so Rows stops meaning anything on either of them.
*/
func NewCopy[T any](src RowSource[T], columns int, encode func(dst []any, item T) []any) (*Copy[T], error) {
	// A typed nil - (*yourSource)(nil) - is not caught here: an interface holding
	// a type descriptor is not nil. It fails on the first Next instead, which is
	// where a nil receiver would fail anyway.
	if src == nil {
		return nil, fmt.Errorf("%w: src is nil", csvcopy.ErrSchema)
	}
	if encode == nil {
		return nil, fmt.Errorf("%w: encode is nil", csvcopy.ErrSchema)
	}
	if columns < 0 {
		columns = 0
	}

	source := &Copy[T]{
		src:    src,
		encode: encode,
		row:    make([]any, 0, columns),
	}
	source.lines, _ = src.(interface{ Line() int })
	source.records, _ = src.(interface{ Record() []string })

	return source, nil
}

// Next advances the underlying source.
func (c *Copy[T]) Next() bool {
	if !c.src.Next() {
		return false
	}
	c.rows++
	c.started = true

	return true
}

/*
Values lays out the current value.

The result is stored back, so a row wider than columns grows the slice once
instead of reallocating on every row. Safe to reuse because pgx encodes the row
before pulling the next one.

Calling it before the first Next is csvcopy.ErrSchema. There is no row to lay
out then, and encoding the zero value would hand back a plausible-looking row of
empty values with no error on it. pgx.CopyFrom always calls Next first and never
sees this; a caller driving the source by hand can, which is the only reason it
is checked.
*/
func (c *Copy[T]) Values() ([]any, error) {
	if !c.started {
		return nil, fmt.Errorf("%w: Values called before Next", csvcopy.ErrSchema)
	}
	c.row = c.encode(c.row[:0], c.src.Value())

	return c.row, nil
}

// Err reports the underlying source's error.
func (c *Copy[T]) Err() error {
	return c.src.Err()
}

// Rows is the number of rows handed out so far. Counted here rather than asked of
// the source, which need not track it.
func (c *Copy[T]) Rows() int64 {
	return c.rows
}

/*
Line is the line of the file the current row came from, or zero when the source
does not have lines - one over an API or a generator does not.

Asked of the source through an interface rather than required by RowSource, so a
source that cannot answer does not have to declare a method returning nothing
useful.

It exists because the advice for a failed CopyFrom is to read the source's error
first, since that is the one naming the line. Without this, following that advice
meant keeping the decode.Typed value in a second variable purely to ask it - and the
obvious code, which passes the source straight into NewCopy, could not.
*/
func (c *Copy[T]) Line() int {
	if c.lines == nil {
		return 0
	}

	return c.lines.Line()
}

// Record is the raw record behind the current row, or nil when the source does not
// keep one. The counterpart to Line, and asked for the same way.
func (c *Copy[T]) Record() []string {
	if c.records == nil {
		return nil
	}

	return c.records.Record()
}
