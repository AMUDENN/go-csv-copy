package decode

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
record differ by up to one buffer. The point is a bound on memory, not an exact
count - and the memory it bounds is about four times the budget, since encoding/csv
buffers the line and the record separately and doubles each in place.

Trimming p to what is left, rather than letting one Read overrun and catching it
afterwards, is what keeps the bound on the byte that crosses it instead of on the
buffer that crossed it. The cost is that a caller who sets a small budget gets
short reads underneath, which WithMaxRecordBytes says out loud.

Do not reset the budget per line, however obvious it looks. encoding/csv skips
blank lines and comment lines inside one Read, so a long run of them spends the
budget and is reported as a record too large - a real wart, documented on
WithMaxRecordBytes. The fix for it is not available here: this reader sees raw
bytes and knows nothing about the parser's state, and inside an unclosed quoted
field a blank line or a line starting with the comment rune is data. Resetting
there reopens exactly the hole this type exists to close - `"` followed by endless
newlines would refill the budget forever and memory would grow without a bound.
Recognising the difference means reimplementing encoding/csv's quoting rules in
here, which costs far more than the wart does.
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
