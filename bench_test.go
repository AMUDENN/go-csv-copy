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

	BenchmarkRaw-6                20.1 ms   11.2 MB   600036 allocs   (6 per row)
	BenchmarkRawAll-6             19.6 ms   11.2 MB   600034 allocs   (6 per row)
	BenchmarkRawPointerValues-6   11.9 ms    3.2 MB   100034 allocs   (1 per row)
	BenchmarkTyped-6              15.7 ms    3.2 MB   100039 allocs   (1 per row)
	BenchmarkTypedAll-6           14.9 ms    3.2 MB   100039 allocs   (1 per row)
	BenchmarkTypedCopy-6          23.5 ms   11.2 MB   600042 allocs   (6 per row)
	BenchmarkNewTyped-6            6.3 us    6.1 kB       39 allocs   (per file)

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

WithMaxRecordBytes costs one allocation per file - the budgetReader itself, made
once in NewReader - and none per row. It sits under the bufio.Reader that
csv.Reader already owns, so a row does not reach it at all; only the buffer refills
do, and those cost an integer subtraction.

On a wide file the boxing is the whole story. 20k rows of 30 columns:

	BenchmarkTypedWide-6                15.1 ms    5.3 MB    20099 allocs   (1 per row)
	BenchmarkRawWide-6                  20.9 ms   14.9 MB   620065 allocs  (31 per row)
	BenchmarkRawWidePointerValues-6     13.7 ms    5.3 MB    20066 allocs   (1 per row)

Six times the columns, thirty-one times the allocations for Raw, and Typed flat at
one per row - apply is linear in bound fields but writes into a struct and allocates
nothing. So WithPointerValues is worth more the wider the file gets: 31x fewer
allocations here against 6x on five columns, and faster than Typed.

And the mistake that undoes it, against BenchmarkTypedCopy on the same file:

	BenchmarkTypedCopy-6      23.5 ms   11.2 MB   600042 allocs   (6 per row)
	BenchmarkCopyBadEncode-6  26.4 ms   19.2 MB   700043 allocs   (7 per row)

One extra allocation per row and 8 MB more, for an encode that returns a fresh
slice instead of appending into dst. It reads perfectly naturally and nothing stops
you writing it.

Watch for: allocs/op climbing above (columns + 1) per row, an All benchmark
drifting away from its Next counterpart, or any growth in BenchmarkNewTyped,
which is pure per-file setup and the only place a new check on the struct can
show up. Its 39 allocations are reflect and the plan, paid once per file.
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

/*
wideRow is 30 bound fields, six times the width of benchRow.

decodePlan.apply is linear in the number of bound fields, and Raw's per-cell boxing
is linear in the number of columns. Which of the two dominates on a wide file was
simply unknown - the other benchmarks are all five columns wide.
*/
type wideRow struct {
	C00 string `csv:"c00"`
	C01 string `csv:"c01"`
	C02 string `csv:"c02"`
	C03 string `csv:"c03"`
	C04 string `csv:"c04"`
	C05 string `csv:"c05"`
	C06 string `csv:"c06"`
	C07 string `csv:"c07"`
	C08 string `csv:"c08"`
	C09 string `csv:"c09"`
	C10 string `csv:"c10"`
	C11 string `csv:"c11"`
	C12 string `csv:"c12"`
	C13 string `csv:"c13"`
	C14 string `csv:"c14"`
	C15 string `csv:"c15"`
	C16 string `csv:"c16"`
	C17 string `csv:"c17"`
	C18 string `csv:"c18"`
	C19 string `csv:"c19"`
	C20 string `csv:"c20"`
	C21 string `csv:"c21"`
	C22 string `csv:"c22"`
	C23 string `csv:"c23"`
	C24 string `csv:"c24"`
	C25 string `csv:"c25"`
	C26 string `csv:"c26"`
	C27 string `csv:"c27"`
	C28 string `csv:"c28"`
	C29 string `csv:"c29"`
}

const wideColumns = 30

// wideFile has fewer rows than benchFile: the same number would spend most of the
// run generating the fixture.
const wideRows = 20_000

func wideFile(rows int) []byte {
	var buf bytes.Buffer

	for col := range wideColumns {
		if col > 0 {
			buf.WriteByte(';')
		}
		fmt.Fprintf(&buf, "c%02d", col)
	}
	buf.WriteByte('\n')

	for row := range rows {
		for col := range wideColumns {
			if col > 0 {
				buf.WriteByte(';')
			}
			fmt.Fprintf(&buf, "v%d_%d", row, col)
		}
		buf.WriteByte('\n')
	}

	return buf.Bytes()
}

func benchWideConvert(r *wideRow) (wideRow, error) { return *r, nil }

func BenchmarkTypedWide(b *testing.B) {
	file := wideFile(wideRows)

	b.ResetTimer()
	b.ReportAllocs()

	for b.Loop() {
		src, err := NewTyped(bytes.NewReader(file), benchWideConvert)
		if err != nil {
			b.Fatal(err)
		}
		for src.Next() {
			_ = src.Value()
		}
		if err = src.Err(); err != nil {
			b.Fatal(err)
		}
		if src.Rows() != wideRows {
			b.Fatalf("read %d rows, want %d", src.Rows(), wideRows)
		}
	}
}

// The counterpart: same file, same rows, through Raw. The gap between the two is
// the per-cell boxing, and on 30 columns it is the whole story.
func BenchmarkRawWide(b *testing.B) {
	file := wideFile(wideRows)

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
	}
}

// And the same again with the boxing removed, to show the option is worth more the
// wider the file gets.
func BenchmarkRawWidePointerValues(b *testing.B) {
	file := wideFile(wideRows)

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

	for b.Loop() {
		rows, err := NewTyped(bytes.NewReader(file), benchConvert)
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
