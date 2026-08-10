package csvcopy

import "io"

/*
budgetReader caps how much can be read from the file while encoding/csv assembles
one record.

encoding/csv has no limit of its own: a field that opens a quote and never closes
it makes the parser read to EOF looking for the closing one, and the whole file
lands in one recordBuffer. That is the one input that breaks the constant-memory
promise, and it is trivial to produce by accident. Lazy quoting is not what causes
it - the parser reads to the end either way, and only then decides whether to
report the quote.

The bound is approximate on purpose. csv.Reader wraps its input in a bufio.Reader
that reads ahead, so bytes drawn from here and bytes that ended up in the current
record differ by up to one buffer. The point is an upper bound on memory, not an
exact count.
*/
type budgetReader struct {
	r    io.Reader
	max  int64
	left int64
}

// reset starts a fresh budget for the next record.
func (b *budgetReader) reset() { b.left = b.max }

func (b *budgetReader) Read(p []byte) (int, error) {
	if b.left <= 0 {
		return 0, errRecordTooLarge
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}

	n, err := b.r.Read(p)
	b.left -= int64(n)

	return n, err
}
