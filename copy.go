package csvcopy

import "fmt"

/*
RowSource is anything that yields values one at a time: Typed, or a source of your
own over XLSX, an API or a generator.

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
	src    RowSource[T]
	encode func(dst []any, item T) []any
	row    []any
	rows   int64
}

/*
NewCopy wraps src, laying each value out with encode.

columns is how many values encode appends; it only sizes the backing slice, so
being wrong costs an allocation rather than correctness. encode must append in the
same order as the column list passed to pgx.CopyFrom - the two are a pair, and
Postgres cannot notice when they disagree if the types are compatible.

A nil src or encode is ErrSchema, the same as a nil reader or convert elsewhere in
the package: it is wiring the calling code got wrong, and no input file will fix
it.
*/
func NewCopy[T any](src RowSource[T], columns int, encode func(dst []any, item T) []any) (*Copy[T], error) {
	// A typed nil - (*yourSource)(nil) - is not caught here: an interface holding
	// a type descriptor is not nil. It fails on the first Next instead, which is
	// where a nil receiver would fail anyway.
	if src == nil {
		return nil, fmt.Errorf("%w: src is nil", ErrSchema)
	}
	if encode == nil {
		return nil, fmt.Errorf("%w: encode is nil", ErrSchema)
	}
	if columns < 0 {
		columns = 0
	}

	return &Copy[T]{
		src:    src,
		encode: encode,
		row:    make([]any, 0, columns),
	}, nil
}

// Next advances the underlying source.
func (c *Copy[T]) Next() bool {
	if !c.src.Next() {
		return false
	}
	c.rows++

	return true
}

/*
Values lays out the current value.

The result is stored back, so a row wider than columns grows the slice once
instead of reallocating on every row. Safe to reuse because pgx encodes the row
before pulling the next one.
*/
func (c *Copy[T]) Values() ([]any, error) {
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
meant keeping the Typed value in a second variable purely to ask it - and the
obvious code, which passes the source straight into NewCopy, could not.
*/
func (c *Copy[T]) Line() int {
	if source, ok := c.src.(interface{ Line() int }); ok {
		return source.Line()
	}

	return 0
}

// Record is the raw record behind the current row, or nil when the source does not
// keep one. The counterpart to Line, and asked for the same way.
func (c *Copy[T]) Record() []string {
	if source, ok := c.src.(interface{ Record() []string }); ok {
		return source.Record()
	}

	return nil
}
