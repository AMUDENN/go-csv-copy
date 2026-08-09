package csvcopy

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

/*
Memory has to stay flat as rows go by - that is the reason this package exists
rather than a ReadAll into a slice. These benchmarks exist to make a regression
visible.

Measured on go1.26.1 windows/amd64, one run at -benchtime=3x, 100k rows of 5
columns:

	BenchmarkRaw-6                18.7 ms   11.2 MB   600033 allocs   (6 per row)
	BenchmarkRawAll-6             20.6 ms   11.2 MB   600032 allocs   (6 per row)
	BenchmarkRawPointerValues-6   11.2 ms    3.2 MB   100033 allocs   (1 per row)
	BenchmarkTyped-6              13.6 ms    3.2 MB   100038 allocs   (1 per row)
	BenchmarkTypedAll-6           14.4 ms    3.2 MB   100038 allocs   (1 per row)
	BenchmarkTypedCopy-6          22.2 ms   11.2 MB   600042 allocs   (6 per row)
	BenchmarkNewTyped-6            4.0 us    6.0 kB       38 allocs   (per file)

Three runs is far too few to say anything about ns/op - treat those as an order
of magnitude and compare allocs/op, which is stable to the allocation. The point
of -benchtime=3x is that each iteration walks 100k rows already.

Live-memory is constant; these are allocations over the whole pass, and the
per-row counts are accounted for:

  - 1 per row everywhere is encoding/csv, which allocates one string per record
    even with ReuseRecord. That is the floor short of unsafe tricks.

  - The other 5 are the columns. Putting a string into an []any boxes it, and
    boxing a string always allocates 16 bytes for its header. So Raw and Copy
    cost one allocation per column per row, by the shape of Values() ([]any, error).

The columns term is avoidable: WithPointerValues hands out *string taken from an
array allocated once per file, and a pointer is pointer-shaped, so boxing it is
free. BenchmarkRawPointerValues is what that costs instead.

The All variants sit on the same numbers as the Next-driven ones, to the
allocation. Ranging is a way of writing the loop, not a second cost.

Watch for: allocs/op climbing above (columns + 1) per row, an All benchmark
drifting away from its Next counterpart, or any growth in BenchmarkNewTyped,
which is pure per-file setup and the only place a new check on the struct can
show up. Its 38 allocations are reflect and the plan, paid once per file.
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

func BenchmarkRaw(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		src, err := NewRaw(bytes.NewReader(file))
		if err != nil {
			b.Fatal(err)
		}
		for src.Next() {
			if _, err = src.Values(); err != nil {
				b.Fatal(err)
			}
		}
		if err = src.Err(); err != nil {
			b.Fatal(err)
		}
		if src.Rows() != benchRows {
			b.Fatalf("read %d rows, want %d", src.Rows(), benchRows)
		}
	}
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

func BenchmarkTyped(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		src, err := NewTyped(bytes.NewReader(file), benchConvert)
		if err != nil {
			b.Fatal(err)
		}
		for src.Next() {
			_ = src.Value()
		}
		if err = src.Err(); err != nil {
			b.Fatal(err)
		}
		if src.Rows() != benchRows {
			b.Fatalf("read %d rows, want %d", src.Rows(), benchRows)
		}
	}
}

func BenchmarkTypedCopy(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		rows, err := NewTyped(bytes.NewReader(file), benchConvert)
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

// The counterpart to BenchmarkRaw: the per-cell boxing is gone, leaving only the
// one allocation per row that encoding/csv imposes.
func BenchmarkRawPointerValues(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		src, err := NewRaw(bytes.NewReader(file), WithPointerValues(true))
		if err != nil {
			b.Fatal(err)
		}
		for src.Next() {
			if _, err = src.Values(); err != nil {
				b.Fatal(err)
			}
		}
		if err = src.Err(); err != nil {
			b.Fatal(err)
		}
		if src.Rows() != benchRows {
			b.Fatalf("read %d rows, want %d", src.Rows(), benchRows)
		}
	}
}

/*
The iterators must cost nothing per row.

An iter.Seq returned from a method closes over the source, which is one allocation
for the whole pass - but only if the loop body does not make the closure escape
per iteration. These pin that against the Next-driven benchmarks above: same
allocs/op, to the allocation.
*/
func BenchmarkRawAll(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		src, err := NewRaw(bytes.NewReader(file))
		if err != nil {
			b.Fatal(err)
		}
		for values := range src.All() {
			_ = values
		}
		if err = src.Err(); err != nil {
			b.Fatal(err)
		}
		if src.Rows() != benchRows {
			b.Fatalf("read %d rows, want %d", src.Rows(), benchRows)
		}
	}
}

func BenchmarkTypedAll(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		src, err := NewTyped(bytes.NewReader(file), benchConvert)
		if err != nil {
			b.Fatal(err)
		}
		for row := range src.All() {
			_ = row
		}
		if err = src.Err(); err != nil {
			b.Fatal(err)
		}
		if src.Rows() != benchRows {
			b.Fatalf("read %d rows, want %d", src.Rows(), benchRows)
		}
	}
}

func BenchmarkNewTyped(b *testing.B) {
	header := "id;last_name;first_name;birthdate;address_id\n"

	b.ReportAllocs()

	for b.Loop() {
		if _, err := NewTyped(strings.NewReader(header), benchConvert); err != nil {
			b.Fatal(err)
		}
	}
}
