package decode

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

/*
Memory has to stay flat as rows go by - that is the reason this package exists
rather than a ReadAll into a slice. These benchmarks exist to make a regression
visible.

Measured on go1.26.1 darwin/arm64 (Apple M1), median of -count=6 at -benchtime=3x,
100k rows of 5 columns:

	BenchmarkRaw-8                14.7 ms    3.2 MB   100034 allocs   (1 per row)
	BenchmarkRawAll-8             14.5 ms    3.2 MB   100034 allocs   (1 per row)
	BenchmarkRawStringValues-8    22.1 ms   11.2 MB   600033 allocs   (6 per row)
	BenchmarkTyped-8              14.3 ms    3.2 MB   100040 allocs   (1 per row)
	BenchmarkTypedAll-8           14.3 ms    3.2 MB   100040 allocs   (1 per row)
	BenchmarkNewTyped-8            5.2 us    6.2 kB       40 allocs   (per file)

Three iterations is far too few to say anything about ns/op from a single run -
compare allocs/op, which is stable to the allocation, and treat the times as an
order of magnitude unless -count is high enough for benchstat to have an opinion.
The point of -benchtime=3x is that each iteration walks 100k rows already.

BenchmarkDecodePlanApply is the exception and must be run at the default
-benchtime: three iterations of a nine-nanosecond function measure nothing.

Live-memory is constant; these are allocations over the whole pass, and the
per-row counts are accounted for:

  - 1 per row everywhere is encoding/csv, which allocates one string per record
    even with ReuseRecord. That is the floor short of unsafe tricks.

  - The extra 5 in the string-valued runs are the columns. Putting a string into
    an []any boxes it, and boxing a string always allocates 16 bytes for its
    header, so one allocation per cell.

That term is why WithPointerValues is the default: *string taken from an array
allocated once per file is pointer-shaped, so boxing it is free.
BenchmarkRawStringValues is what asking for plain strings costs instead, and Copy
pays the same because the boxing there happens inside the caller's encode.

The All variants sit on the same numbers as the Next-driven ones, to the
allocation. Ranging is a way of writing the loop, not a second cost.

WithMaxRecordBytes costs one allocation per file - the budgetReader itself, made
once in NewReader - and none per row. It sits under the bufio.Reader that
csv.Reader already owns, so a row does not reach it at all; only the buffer refills
do, and those cost an integer subtraction.

On a wide file the boxing is the whole story. 20k rows of 30 columns:

	BenchmarkTypedWide-8              15.1 ms    5.3 MB    20100 allocs   (1 per row)
	BenchmarkRawWide-8                15.2 ms    5.3 MB    20067 allocs   (1 per row)
	BenchmarkRawWideStringValues-8    23.6 ms   14.9 MB   620065 allocs  (31 per row)

Six times the columns, thirty-one times the allocations once the values are strings,
while both the default and Typed stay flat at one per row - apply is linear in bound
fields but writes through pointers and allocates nothing. Which is the argument for
the default: what it saves grows with the width of the file, 31x here against 6x on
five columns.

Typed sits level with Raw on both widths, which it did not before the plan resolved
to field pointers: apply used to reach for reflect.Value.Field on every field of
every row, and that was the whole of the gap.

What the layout on top of this costs - BenchmarkTypedCopy - and the encode mistake
that makes it worse are in copyfrom, which is where Copy is. They run on a file
generated identically, so they compare directly against BenchmarkTyped here.

Watch for: allocs/op climbing above (columns + 1) per row, an All benchmark
drifting away from its Next counterpart, or any growth in BenchmarkNewTyped,
which is pure per-file setup and the only place a new check on the struct can
show up. Its 40 allocations are reflect, the bindings and the bound plan, paid once
per file.
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

	for range b.N {
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

	for range b.N {
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

// The counterpart to BenchmarkRaw, which now uses pointer values by default: this
// is what asking for plain strings costs instead, one allocation per cell.
func BenchmarkRawStringValues(b *testing.B) {
	file := benchFile(benchRows)

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
		src, err := NewRaw(bytes.NewReader(file), WithPointerValues(false))
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

	for range b.N {
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

	for range b.N {
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

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
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

	for range b.N {
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

	for range b.N {
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

// And the same again with plain strings, to show what the default is worth as the
// file gets wider.
func BenchmarkRawWideStringValues(b *testing.B) {
	file := wideFile(wideRows)

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
		src, err := NewRaw(bytes.NewReader(file), WithPointerValues(false))
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
BenchmarkDecodePlanApply is the row path of Typed on its own, with the reader and
convert taken out of the picture.

It exists because apply is where the one interesting decision in this package's
hot loop lives: the plan resolves the struct's field addresses once per file, so a
row is a walk over pointers rather than reflect.Value.Field followed by SetString.
End-to-end numbers bury that under encoding/csv, which allocates a string per
record and dominates everything; here it is the only thing being measured, and a
regression shows up as a multiple rather than as a few percent.

Ten fields, all bound, no allocation on any path.
*/
func BenchmarkDecodePlanApply(b *testing.B) {
	var row struct {
		C0 string `csv:"c0"`
		C1 string `csv:"c1"`
		C2 string `csv:"c2"`
		C3 string `csv:"c3"`
		C4 string `csv:"c4"`
		C5 string `csv:"c5"`
		C6 string `csv:"c6"`
		C7 string `csv:"c7"`
		C8 string `csv:"c8"`
		C9 string `csv:"c9"`
	}

	bindings := make([]binding, 10)
	record := make([]string, 10)
	for i := range bindings {
		bindings[i] = binding{field: i, column: i}
		record[i] = "value"
	}

	plan := bindPlan(bindings, reflect.ValueOf(&row).Elem())

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
		if plan.apply(record) {
			b.Fatal("apply reported a truncated record on a full one")
		}
	}
}

/*
NewReader is per-file setup: the option checks, the BOM read and the header.

BenchmarkNewTyped covers the same ground plus the struct work, so this one exists
to say which half a change landed in. A new validation in the constructor is
cheap by definition here - the point of watching it is that per-file work is where
a "cheap" check can quietly become a per-file allocation.
*/
func BenchmarkNewReader(b *testing.B) {
	header := "id;last_name;first_name;birthdate;address_id\n"

	b.ResetTimer()
	b.ReportAllocs()

	for range b.N {
		if _, err := NewReader(strings.NewReader(header)); err != nil {
			b.Fatal(err)
		}
	}
}
