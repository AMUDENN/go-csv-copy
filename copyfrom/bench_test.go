package copyfrom

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/AMUDENN/go-csv-copy/decode"
)

/*
What laying a decoded row out across columns costs, on top of decoding it.

The counterpart numbers live in decode's benchmarks; these two are here because
this is where Copy is. Same file, same convert, so BenchmarkTypedCopy is directly
comparable to decode's BenchmarkTyped - the difference between them is the boxing,
which happens inside the caller's own encode:

	BenchmarkTyped-8          14.3 ms    3.2 MB   100040 allocs   (1 per row)
	BenchmarkTypedCopy-8      24.2 ms   11.2 MB   600041 allocs   (6 per row)
	BenchmarkCopyBadEncode-8  37.3 ms   19.2 MB   700042 allocs   (7 per row)

Measured on go1.26.1 darwin/arm64 (Apple M1), median of -count=6 at -benchtime=3x,
100k rows of 5 columns.
Compare allocs/op, which is stable to the allocation; three runs say nothing about
ns/op.

The file generator is restated here rather than shared with decode: these
benchmarks have to keep producing the same input as that package's for the numbers
above to mean anything, and a copy that is visibly identical is a smaller price
than a test-only dependency between two packages that otherwise have none.
*/
const benchRows = 100_000

func benchFile(rows int) []byte {
	var buf bytes.Buffer

	buf.WriteString("id;last_name;first_name;birthdate;address_id\n")
	for i := range rows {
		fmt.Fprintf(&buf, "%d;Smith;John;1990-01-%02d;%d\n", i, i%28+1, i*7)
	}

	return buf.Bytes()
}

type benchRow struct {
	ID        string `csv:"id"`
	LastName  string `csv:"last_name"`
	FirstName string `csv:"first_name"`
	Birthdate string `csv:"birthdate"`
	AddressID string `csv:"address_id"`
}

// The convert func returns the struct by value so nothing escapes to the heap; a
// real one builds an entity and would allocate once per row by nature.
func benchConvert(r *benchRow) (benchRow, error) { return *r, nil }

func BenchmarkTypedCopy(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
		rows, err := decode.NewTyped(bytes.NewReader(file), benchConvert)
		if err != nil {
			b.Fatal(err)
		}

		source, err := NewCopy(rows, 5, func(dst []any, r benchRow) []any {
			return append(dst, r.ID, r.LastName, r.FirstName, r.Birthdate, r.AddressID)
		})
		if err != nil {
			b.Fatal(err)
		}

		for source.Next() {
			if _, err = source.Values(); err != nil {
				b.Fatal(err)
			}
		}
		if err = source.Err(); err != nil {
			b.Fatal(err)
		}
	}
}

/*
BenchmarkCopyBadEncode prices the mistake the encode contract exists to prevent.

encode is handed dst and is meant to append into it. Returning a fresh slice
instead - which reads perfectly naturally, and which nothing stops you from doing -
throws away the one buffer Copy keeps and allocates a slice per row on top of the
boxing. Against BenchmarkTypedCopy, the difference is the documentation.
*/
func BenchmarkCopyBadEncode(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
		rows, err := decode.NewTyped(bytes.NewReader(file), benchConvert)
		if err != nil {
			b.Fatal(err)
		}

		source, err := NewCopy(rows, 5, func(_ []any, r benchRow) []any {
			// The mistake: dst is ignored and a new slice is returned.
			return []any{r.ID, r.LastName, r.FirstName, r.Birthdate, r.AddressID}
		})
		if err != nil {
			b.Fatal(err)
		}

		for source.Next() {
			if _, err = source.Values(); err != nil {
				b.Fatal(err)
			}
		}
		if err = source.Err(); err != nil {
			b.Fatal(err)
		}
	}
}

/*
ValidateColumns runs once per file, on a header, so it is not a hot path - this is
here to keep it from quietly becoming one.

It is O(columns) with one map allocation, and every check inside the loop is a
scan of a short string. A future rule that reaches for a regexp or a list of
reserved words would show up here as an order of magnitude before anyone noticed
it in production.
*/
func BenchmarkValidateColumns(b *testing.B) {
	columns := make([]string, 30)
	for i := range columns {
		columns[i] = fmt.Sprintf("column_%02d", i)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
		if err := ValidateColumns(columns); err != nil {
			b.Fatal(err)
		}
	}
}
