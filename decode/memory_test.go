package decode

import (
	"errors"
	"io"
	"runtime"
	"testing"
)

/*
runawayField is a header followed by a field that opens a quote and never closes
it, size bytes long in total. It allocates nothing itself, so whatever the
measurement below sees was allocated by the parser.

Finite on purpose. An endless one would let a regression hang the suite instead of
failing it, and a test that hangs when the thing it guards breaks is worse than no
test.
*/
type runawayField struct {
	head []byte
	left int
}

func (r *runawayField) Read(p []byte) (int, error) {
	if len(r.head) > 0 {
		n := copy(p, r.head)
		r.head = r.head[n:]

		return n, nil
	}
	if r.left <= 0 {
		return 0, io.EOF
	}
	if len(p) > r.left {
		p = p[:r.left]
	}
	for i := range p {
		p[i] = 'x'
	}
	r.left -= len(p)

	return len(p), nil
}

/*
The package's central claim, measured rather than asserted in prose.

"Memory is bounded by a multiple of WithMaxRecordBytes and not by the size of the
file" is stated in four places - the package doc, the README, WithMaxRecordBytes
and the CHANGELOG - and the CHANGELOG records what happened last time it lived
only in prose: it said the bound was 1x the cap for months while it was really 4x.
The number belongs somewhere a machine checks it.

What is asserted is TotalAlloc rather than peak heap, because TotalAlloc is exact
and reproducible while a peak has to be sampled and can be missed. The two are
related and both were measured: for this input the parser allocates about 7x the
cap in total and about 4x of it is live at the moment of the last doubling -
encoding/csv grows the line buffer and the record buffer separately, and a doubling
holds the old array and the new one at once.

The bound here is an order of magnitude, not a byte count. It is set well above
what was measured so that a slightly different growth strategy in encoding/csv does
not fail it, and well below what a regression would produce: if the cap stopped
working, the whole 64 MiB would be buffered and this would come out in the
hundreds.

Not parallel, unlike everything else in this package: it reads process-wide heap
counters, and a parallel test allocating alongside it would be measured too.
*/
func TestMemoryStaysAMultipleOfTheCap(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates tens of megabytes and stops the world twice")
	}

	const (
		limit    = 1 << 20
		fileSize = 64 << 20
		// 7x measured; 24x leaves room for a different growth strategy upstream
		// while still being an order of magnitude below a file-sized regression.
		allowed = 24 * limit
	)

	reader, err := NewReader(&runawayField{head: []byte("a;b\n\""), left: fileSize}, WithMaxRecordBytes(limit))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	runtime.GC()

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)

	_, err = reader.Read()

	runtime.ReadMemStats(&after)

	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatalf("Read = %v, want ErrRecordTooLarge - the cap did not fire", err)
	}

	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > allowed {
		t.Errorf("reading one oversized record allocated %d bytes, over the %d-byte bound (%.1fx the %d-byte cap); a 64 MiB file was the input",
			allocated, allowed, float64(allocated)/limit, limit)
	}
}
