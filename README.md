# go-csv-copy

Streaming CSV reader for Go, shaped for bulk-loading into PostgreSQL. No dependencies outside the
standard library.

⸻

## 📋 Overview

The package reads CSV one row at a time: a row is read, handed over and forgotten, so memory does
not depend on the size of the file.

What it is built for is `COPY FROM` — the row sources satisfy `pgx.CopyFromSource`, so a file of any
size loads into Postgres without being buffered. That is the primary use case, not the only one:
`Reader` is a plain CSV reader and `Typed` decodes into your own types, both usable with no database
in sight. See [Without a database](#-without-a-database).

Two ways of fixing a file's shape are supported, because both turn up in practice:

- **`decode.Raw`** — the shape comes **from the file's own header**. Whatever columns arrived, in
  their order, every value as text. This is the staging-table case, where the input's structure is
  not known ahead of time and a SQL script types the data afterwards.
- **`decode.Typed[S, D]`** — the shape comes **from `csv` struct tags**. Columns are matched by
  name, so their order in the file does not matter, extra columns are ignored, and a column a tag
  asks for and the file lacks is an error. Each row passes through your `convert`, which types and
  validates the values.

### Three packages, and why

| Import | Holds | Depends on |
|---|---|---|
| `csvcopy` | `ErrParse`, `ErrIO`, `ErrSchema` — the three answers to "who can fix this" | nothing |
| `csvcopy/decode` | `Reader`, `Raw`, `Typed`, every option | `csvcopy` |
| `csvcopy/copyfrom` | `RowSource`, `Copy`, `ValidateColumns` | `csvcopy` |

`decode` and `copyfrom` **do not import each other**. A parser imports `decode` and its import block
never mentions a database; a repository imports `copyfrom` and never mentions CSV, because a
`RowSource` is a `RowSource` whether it came from a file, an XLSX sheet or a paged API. The layering
is in the import graph rather than in a convention someone has to remember.

The sentinels sit in the root so that `errors.Is(err, csvcopy.ErrIO)` reads the same whichever layer
raised it. The named ones live with the code that raises them — `decode.ErrMissingColumns`,
`decode.ErrDuplicateColumns`, `decode.ErrRecordTooLarge`, `copyfrom.ErrInvalidColumns` — and each
wraps one of the three.

### Why pgx is not imported

The package **does not depend on pgx** and is compatible with it anyway. Go interfaces are
structural, and `pgx.CopyFromSource` is nothing more than a method set:

```go
type CopyFromSource interface {
    Next() bool
    Values() ([]any, error)
    Err() error
}
```

Any type with those methods satisfies it without declaring a dependency. So the application picks
the version of pgx, not the library, and upgrading pgx never requires upgrading `csvcopy`.

⸻

## 📦 Install

```bash
go get github.com/AMUDENN/go-csv-copy
```

```go
import (
    csvcopy "github.com/AMUDENN/go-csv-copy"        // ErrParse, ErrIO, ErrSchema
    "github.com/AMUDENN/go-csv-copy/decode"         // Reader, Raw, Typed, options
    "github.com/AMUDENN/go-csv-copy/copyfrom"       // RowSource, Copy, ValidateColumns
)
```

Take only what a file needs — the root package is required for `errors.Is` checks and nothing else.
The alias on the first line is because the package is `csvcopy` while the path ends in
`go-csv-copy`; without it `goimports` adds it for you.

Go 1.23 or newer — the floor is `iter.Seq`, which `All()` returns and which arrived in 1.23. The
`go` directive names a minor version on purpose: pinning a patch would force every consumer to fetch
a toolchain, which is a strange thing for a package whose whole point is bringing nothing with it.
The same reasoning is why it is not 1.24: the only thing that wanted 1.24 was `testing.B.Loop` in
benchmarks a consumer never compiles, and a release of support window is too much to pay for it.

⸻

## 🚀 Quick start

### Shape from the file header (staging table)

```go
src, err := decode.NewRaw(file)
if err != nil {
    return fmt.Errorf("parse input: %w", err)
}

columns := src.Columns()
if len(columns) == 0 {
    return nil // an empty file is not an error
}

// The names came out of the file. This one builds DDL from them, so they are
// untrusted input: a column called `x" ); DROP TABLE clients; --` is just a text
// file someone wrote. Quote them, or refuse the header outright.
if err := copyfrom.ValidateColumns(columns); err != nil {
    return err
}

if err := createStagingTable(ctx, tx, table, columns); err != nil {
    return err
}

// ...whose body quotes every name it was given, because passing ValidateColumns
// is not a licence to skip that — it does not know your Postgres version's
// reserved words, and `select` is a legal CSV column name:
//
//     quoted := make([]string, len(columns))
//     for i, column := range columns {
//         quoted[i] = pgx.Identifier{column}.Sanitize() + " TEXT"
//     }

_, err = tx.CopyFrom(ctx, pgx.Identifier{table}, columns, src)
if srcErr := src.Err(); srcErr != nil {
    return fmt.Errorf("read input at line %d: %w", src.Line(), srcErr)
}
if err != nil {
    return fmt.Errorf("copy from: %w", err)
}
```

Check `src.Err()` **before** `err`: when the source fails on a row, pgx returns its own, less
informative error, while the cause sits in the source together with the line number.

### Shape from struct tags (typed load)

```go
// The shape of the file: all text, tags name the columns in the header.
type clientRow struct {
    ID        string `csv:"client_id"`
    Email     string `csv:"email"`
    Balance   string `csv:"balance"`
    CreatedAt string `csv:"created_at"`
}

// Your typing and validation - the package stays out of it.
func (r *clientRow) toClient() (*Client, error) { /* ... */ }

var clientColumns = []string{"id", "email", "balance", "created_at"}

rows, err := decode.NewTyped(file, (*clientRow).toClient)
if err != nil {
    return err
}

source, err := copyfrom.NewCopy(rows, len(clientColumns),
    func(dst []any, c *Client) []any {
        return append(dst, c.ID, c.Email, c.Balance, c.CreatedAt)
    })
if err != nil {
    return err
}

_, err = tx.CopyFrom(ctx, pgx.Identifier{"clients_tmp"}, clientColumns, source)
if srcErr := source.Err(); srcErr != nil {
    return fmt.Errorf("read input at line %d: %w", source.Line(), srcErr)
}
if err != nil {
    return fmt.Errorf("copy from: %w", err)
}

if source.Rows() == 0 {
    // the file was empty - nothing to load
}
```

`decode.Typed` satisfies `copyfrom.RowSource[D]`, so decoding and the layout of a COPY row live in
different layers — and now in different packages, which is what stops the two from drifting back
together. One hands out a `RowSource`; the layer that owns the database decides which columns it
lands in, and never imports `decode` to do it.

### Without a database

`Reader` and `Typed` stand on their own — no `COPY`, no pgx, no `[]any`. Ranging cannot carry an
error out, so the loop stops at the first bad row and `Err()` reports it afterwards, the same shape
as `bufio.Scanner`:

```go
rows, err := decode.NewTyped(file, (*clientRow).toClient)
if err != nil {
    return err
}

for client := range rows.All() {
    if err := send(ctx, client); err != nil {
        return err
    }
}
if err := rows.Err(); err != nil {
    return err
}
```

When no decoding is wanted either, `Reader` yields raw records:

```go
reader, err := decode.NewReader(file)
if err != nil {
    return err
}

fmt.Println(reader.Columns())

for record := range reader.All() {
    fmt.Println(record) // reused between iterations - copy it to keep it
}
return reader.Err()
```

⸻

## 🏗 API

### Layers

| Constructor | Returns | Use when |
|---|---|---|
| `decode.NewReader(r, opts...)` | `*decode.Reader` — header + row-by-row reading | you only need CSV parsing, no COPY |
| `decode.NewRaw(r, opts...)` | `*decode.Raw` — `pgx.CopyFromSource` + `Columns()` | the shape comes from the file header |
| `decode.NewTyped[S, D](r, convert, opts...)` | `*decode.Typed[S, D]` — `copyfrom.RowSource[D]` | the shape comes from `csv` tags |
| `copyfrom.NewCopy[T](src, columns, encode)` | `*copyfrom.Copy[T]` — `pgx.CopyFromSource` | adapting any `RowSource[T]` to COPY |

`decode.Raw` is a `pgx.CopyFromSource` while living in `decode`, which looks like a layering slip
and is not: nothing about it knows pgx exists, it just happens to have `Next`/`Values`/`Err`. It
belongs where the header and the options are. `copyfrom` is for values that did **not** come from a
CSV file.

Every constructor returns an error rather than panicking. What they refuse, exhaustively — all of it
`csvcopy.ErrSchema`, because no input file will ever fix any of it:

- a `nil` reader, `convert`, `src` or `encode`
- a delimiter or comment rune `encoding/csv` will not accept, or a comment rune equal to the
  delimiter
- `WithTag("")`, and a struct where no field carries the tag, or one whose tag normalizes to `""`
- a tagged field that is not an exported `string`, a tag inside an embedded struct, two fields
  asking for one column
- `WithMaxRecordBytes(n)` with `n < 0`
- `Values()` before the first `Next()`

And what is quietly clamped instead, because there is one obviously intended reading and nothing is
lost by taking it: `WithHeaderRow(0)` → row 1, `WithHeaderRow(n)` above 2²⁰ → 2²⁰ (the empty-file
case long before that), `NewCopy(src, -5, encode)` → a zero-length buffer,
`WithNormalizeHeader(nil)` → identity, and a `nil` inside `opts...` → skipped. A negative record cap
is deliberately *not* on this list: the obvious reading of it would be "remove the cap", and
removing the only bound on memory because a config computed a negative number is not a safe default.

### Interfaces

```go
// package copyfrom
//
// RowSource is anything that yields values one at a time. decode.Typed satisfies
// it, and so can a source of your own over XLSX, an API or a generator.
type RowSource[T any] interface {
    Next() bool
    Value() T
    Err() error
}
```

### Options

All of them live in `decode` and apply to `Reader`, `Raw` and `Typed` alike.

| `decode.` | Default | Effect |
|---|---|---|
| `WithComma(r rune)` | `';'` | field delimiter |
| `WithLazyQuotes(bool)` | `false` | tolerate a bare quote, and a quoted field that never closes, instead of failing |
| `WithTrimLeadingSpace(bool)` | `true` | drop white space at the start of a field |
| `WithTrimValues(bool)` | `true` | `TrimSpace` every value, **including quoted ones** — `"   "` arrives as `""` |
| `WithHeaderRow(n uint)` | `1` | which row holds the header; rows above it are dropped |
| `WithNormalizeHeader(fn)` | collapse whitespace | normalize a name before matching — applied to **both** the file's column names and the `csv` tag values, so it must be pure and idempotent |
| `WithTag(string)` | `"csv"` | which struct tag `Typed` reads column names from; an empty name is `ErrSchema` |
| `WithVariableColumns(bool)` | `false` | accept rows of a different width: extra values dropped, missing trailing ones → `nil` in `Raw`, `""` in `Typed` |
| `WithAllowMissingColumns(bool)` | `false` | do not fail when a tagged column is absent from the header |
| `WithPointerValues(bool)` | `true` | `Raw` yields `*string`, which costs no allocation per cell. `false` gives plain `string` |
| `WithMaxRecordBytes(int64)` | `64 MiB` | cap on one record; zero removes it, negative is `ErrSchema`. Exceeding it is `ErrRecordTooLarge`. Peak memory is up to ~4× the cap |
| `WithComment(rune)` | `0` (off) | a rune that starts a comment line, skipped wherever it appears |

### The `csv` tag

The whole tag is the column name. There are no comma-separated options: a field tagged
`csv:"name,omitempty"` asks for a column literally called `name,omitempty`, which no header will
have, and `ErrMissingColumns` says so. `-` is the one value with a meaning of its own — it drops the
field.

Three rules exist because breaking them loses data silently, and the package refuses all three with
`ErrSchema`:

```go
type row struct {
    base                       // ✗ tags inside an embedded struct are not matched
    A string `csv:"id"`
    B string `csv:"id"`        // ✗ two fields, one column
}

type wrong struct {
    ID   string `json:"id"`    // ✗ no csv tag anywhere in the struct
    Name string `json:"name"`
}
```

Embedded fields are not walked into. Left to pass, a tag one level down would bind to nothing, the
field would read as empty on every row, and the column it named would reach the database as `NULL` —
the same damage `ErrMissingColumns` exists to prevent, only without the error. List the columns on
the struct itself.

A struct where **no** field carries the tag is that damage in its complete form: every field binds
to nothing, every row decodes as empty, `Err()` stays `nil`, `Rows()` counts the file honestly, and
`CopyFrom` writes a full table of empty values over a good one. `json:` where `csv:` was meant is
the way this happens, so it is refused from the constructor — on an empty file too. `WithTag("")` is
the same failure from the other side: `StructTag.Get("")` answers `""` for every field.

The mirror image lives in the header rather than the struct: a column a tag asks for that appears
**twice** is `ErrDuplicateColumns`. Binding to the first occurrence would let the file's column
order decide which values are loaded, silently. Normalizing makes it reachable without a literally
duplicated header — `"a  b"` and `"a b"` are one name after `NormalizeSpace`. A duplicate no tag
asks for is not ambiguous and stays in `Unused()`.

#### Why `;` and not `,`

`encoding/csv` defaults to `,`, so this looks like gratuitous disagreement. It follows the target
case. These files come out of spreadsheet exports on machines whose locale uses `,` as the decimal
separator, where Excel and LibreOffice write `;` — and where a comma-delimited file with a single
`1,5` in it is silently one column wider than its header. Anything reading such files hits `;`
overwhelmingly more often than `,`.

Pass `WithComma(',')` for RFC 4180 files. The default is one option away either direction; what it
should not be is a surprise, hence this paragraph.

### Diagnostics

| Method | Gives |
|---|---|
| `All()` | a `range`-able iterator: `[]string` on `Reader`, `[]any` on `Raw`, `D` on `Typed` |
| `Err()` | the error that stopped the stream; end of file is not one |
| `Columns()` / `Header()` | the header as read and normalized |
| `Record()` | the raw record last read, for error messages |
| `Unused()` | header columns no tag bound to |
| `Truncated()` | on `Typed` and `Raw`: the record ran out before the last bound column |
| `Extra()` | on `Raw`: how many values past the header this record had, all dropped |
| `Line()` | the 1-based **physical** line of the file the current row starts on |
| `Rows()` | how many rows have been handed out |

`Copy` forwards `Line()` and `Record()` to whatever it wraps, so a source can go straight into
`NewCopy` without keeping a second reference to it just to ask where a failure came from. A source
with no lines — over an API, or a generator — answers `0` and `nil`.

### `nil` in `Raw`, `""` in `Typed`

Under `WithVariableColumns`, a value the record never reached becomes SQL `NULL` in `Raw` and the
empty string in `Typed`. The asymmetry is forced, not chosen: `Raw` hands pgx an `[]any` and can put
`nil` in it, while a tagged field is declared `string` and has no `nil` to hold. In Postgres `NULL`
and `''` are different values, so the difference matters.

`Typed.Truncated()` tells the two cases apart, read after `Next()`:

```go
for source.Next() {
    if source.Truncated() {
        log.Warn("short record", "line", source.Line())
    }
}
```

`convert` cannot see it — it is called inside `Next()` with the struct as its only argument, and
widening that signature would change every caller's code. Reject a short row in the loop instead.

`Raw` answers the same question with `Truncated()`, and adds `Extra()` for the case that leaves no
trace at all: a record **wider** than the header loses its tail silently, because `Values()` is
sized by the header and the cells past it never reach anyone. A row that is too wide is also the
usual shape of a misread delimiter — the signal `WithVariableColumns` suppresses when you turn it
on:

```go
for src.Next() {
    if n := src.Extra(); n > 0 {
        log.Warn("dropped values", "line", src.Line(), "count", n)
    }
}
```

⚠️ **`WithTrimValues` is on by default and does not exempt quoted fields**, which is the other way a
value the file held turns into `""`. Quoting is how a CSV says "these spaces are data", so `"  x  "`
arrives as `"x"` and a field of three deliberate spaces arrives as `""` — in a nullable column, a
different value from what was written. The default follows the target case, spreadsheet exports
where padding is alignment; pass `decode.WithTrimValues(false)` where the padding is content, such
as a fixed-width export or a column of codes.

`Unused()` is worth logging as a warning: when an export renames a column, the tag simply matches
nothing, no error is raised, and the wrong data reaches the database. The unbound column is the only
visible trace.

⚠️ **`Columns()` returns untrusted input.** The names come out of the file, and the staging-table
pattern puts them on the path to `CREATE TABLE` — a column called `x" ); DROP TABLE clients; --` is
just a text file someone wrote. Quote every name that reaches a statement with
`pgx.Identifier{name}.Sanitize()`, or refuse the header up front with
`copyfrom.ValidateColumns(columns)`, which rejects empty names, duplicates, names over 63 bytes
(Postgres truncates at `NAMEDATALEN` and two columns then collide), names differing only in case
(`ID` and `id` are one column to an unquoted `CREATE TABLE`), names starting with a digit
(`CREATE TABLE t (123 TEXT)` is a syntax error) and anything outside `[A-Za-z0-9_]`, listing every
violation at once. It does **not** know your Postgres version's reserved words — `select` and
`table` are legal CSV column names and pass — which is why quoting is not optional even after it
passes. `CopyFrom` itself quotes them.

⚠️ `WithAllowMissingColumns(true)` is dangerous. The absent field reads as the empty string on every
row and reaches the database as `NULL`, quietly wiping whatever that column held. Only use it where
that is acceptable.

### Errors

Errors are split by who can fix them.

| Sentinel | Wraps `ErrParse` | Cause |
|---|---|---|
| `csvcopy.ErrParse` | — | the file's content is wrong: a malformed record, a failing `convert` |
| `decode.ErrMissingColumns` | yes | the header lacks a column a tag asks for |
| `decode.ErrDuplicateColumns` | yes | the header carries a column a tag asks for more than once |
| `decode.ErrRecordTooLarge` | yes | one record outgrew `WithMaxRecordBytes` |
| `copyfrom.ErrInvalidColumns` | yes | `ValidateColumns` refused a header name |
| `csvcopy.ErrIO` | **no** | the stream failed, not the file: a dropped connection, a cancelled context, a bad disk |
| `csvcopy.ErrSchema` | **no** | a bug in the calling code: not a struct, no `csv` tags at all, a tag on a non-string or unexported field, a tag inside an embedded struct, two fields asking for one column, a nil reader/convert/src/encode, an empty tag name, an unusable delimiter, `Values()` before `Next()` |

The three categories live in the root package so that a check reads the same whichever layer raised
the error; the named sentinels live with the code that raises them and each wraps one of the three.

The split exists because the three answers differ: `ErrSchema` means **fix the code**, `ErrIO`
means **retry**, `ErrParse` means **the file is bad** — quarantine it. Getting this wrong is not
theoretical. Until `ErrIO` existed, a dropped TCP connection was reported as `ErrParse`, so anyone
following the advice above would quarantine a perfectly good file forever because the network
blinked once.

The classification is not guesswork: `encoding/csv` reports everything the parser objects to as a
`*csv.ParseError` and passes anything else back from the underlying reader unchanged, so the shape
of the error is the evidence. The original cause stays in the chain either way, so
`errors.Is(err, context.Canceled)` still answers.

Everything a file can cause wraps `ErrParse` and carries the line number:

```
csv parse: record on line 4213: wrong number of fields
csv parse: line 4213: balance: strconv.ParseFloat: parsing "n/a": invalid syntax
```

The line is the physical line of the file, not a count of records, so it stays right across blank
lines (`encoding/csv` skips them) and quoted fields spanning several lines. It is what `Line()`
returns, and the number to open the file at.

Only `encoding/csv` can supply that line, and only for a parse error. When the stream fails or a
record outgrows the cap there is no `*csv.ParseError` to take it from, and the reader will not
guess: the message and `Line()` both fall back to the last record read in full, and say so.

```
csv read: after line 4212: read tcp 10.0.0.4:5432: connection reset by peer
csv parse: record exceeds the byte limit: after line 4212
```

`after line N` is a bound, not a location — the record that failed starts somewhere after it. A
counter would print `line 4213` here and be wrong by however many lines the previous record spanned.

A failing `convert` is the one case where the caller chooses the category. Its error is wrapped in
`ErrParse` by default, but an error already wrapping `ErrIO` or `ErrSchema` keeps what it says, and
one carrying `context.Canceled` or `context.DeadlineExceeded` is read as `ErrIO` — a cancelled load
is not a bad file, and nobody wraps a context error by hand.

If your application already has a sentinel for a bad file, alias it and every existing `errors.Is`
check keeps working:

```go
var ErrBadFile = csvcopy.ErrParse
```

`ErrSchema` deliberately does **not** wrap `ErrParse`: no input file will ever fix it, so
quarantine-the-file logic must not fire on it. It is a code bug, and it surfaces on the very first
run — including a run on an empty file.

⸻

## 📐 Semantics and guarantees

- **Empty input** (zero bytes, or only a BOM) is not an error: `Columns() == nil`,
  `Next() == false`, `Err() == nil`, `Rows() == 0`.
- **The BOM** (`EF BB BF`) is stripped, read with `io.ReadFull` so that a short read from a slow
  `io.Reader` — which the contract allows — cannot leave a `﻿` in the first column name.
- **The first bad row stops the stream, for good.** Rows are never skipped: a partly loaded table is
  worse than a failed load. The error is available from `Err()`, and it stays there — a source that
  has failed yields nothing more, so ranging `All()` a second time cannot resume past the bad row.
  Breaking out of a loop is different: that is not an error, and the next pull carries on.
- **`Record()` names the row that failed**, as far as `encoding/csv` got with it. `Line()` names the
  physical line it starts on — for `ErrIO` and `ErrRecordTooLarge`, where `encoding/csv` supplies no
  line, it stays on the last record read in full and the message says `after line N`.
- **Memory is constant** and independent of the row count — bounded by the largest single record,
  and the knob is `WithMaxRecordBytes` (64 MiB by default). `encoding/csv` assembles a record in
  one buffer and has no limit of its own, so a field that opens a quote and never closes it is read
  to the end of the file and the whole file becomes one value; the cap is what makes the guarantee
  hold on input nobody checked.

  **The cap is not the peak: reading one record that size costs up to about 4× it.** Measured, at
  the default: 64 MiB cap → 263 MiB peak heap. `encoding/csv` keeps the physical line in one growing
  buffer and the assembled record in another, and grows each by doubling, which has the old array
  and the new one live at the same moment. Size the cap against the memory you can afford divided by
  four. It is a bound on memory and not a byte count to assert against for a second reason too:
  `csv.Reader` reads ahead, so the accounting is off by up to one `bufio` buffer.

  `Values()` reuses one slice, which is safe under `pgx.CopyFrom`
  because it encodes a row before asking for the next. If you drive a source by hand, do not retain
  the result of `Values()` between iterations.
- **`Values()` before the first `Next()` is `ErrSchema`**, on `Raw` and on `Copy` alike. There is no
  row at that point, and what the slice holds — one empty value per column — would load without
  complaint. `pgx.CopyFrom` calls `Next()` first and never sees this; hand-written loops can.
- **Not safe for concurrent use.** One source, one goroutine — the same as `pgx.CopyFrom`.
- `Reader`, `Raw` and `Typed` do not close the `io.Reader` you give them.

⸻

## ⚡ Performance

Live memory is constant, but **allocations over a pass grow linearly**, and it is worth knowing
where from. 100k rows of 5 columns, go1.26.1, Apple M1, median of `-count=6`:

| | ns/op | B/op | allocs/op | per row |
|---|---|---|---|---|
| `Raw` | 14.7 ms | 3.2 MB | 100 034 | 1 |
| `Raw` via `All()` | 14.5 ms | 3.2 MB | 100 034 | 1 |
| `Typed` | 14.3 ms | 3.2 MB | 100 040 | 1 |
| `Typed` via `All()` | 14.3 ms | 3.2 MB | 100 040 | 1 |
| `Raw` + `WithPointerValues(false)` | 22.1 ms | 11.2 MB | 600 033 | 6 |

Ranging costs nothing: an `iter.Seq` returned from a method closes over the source once per pass,
not once per row, so `All()` sits on the same allocation count as the `Next()` loop it replaces.

The one allocation per row is `encoding/csv`: it creates one string per record even with
`ReuseRecord`, and that is the floor short of `unsafe`.

The extra five in the last row are the columns. Boxing a `string` into an `any` **always** allocates
16 bytes for its header, so plain strings cost one allocation per cell — that is the shape of
`Values() ([]any, error)`. A `*string` out of an array allocated once per file is pointer-shaped:
the interface holds it directly and boxing is free.

That is why `*string` is the default, and pgx cannot tell the difference — measured, not argued. The
integration tests load the same file both ways into an all-TEXT table and compare an `md5` of the
rows, then load both ways into a table of `bigint`, `numeric` and `date`. Both agree.

Pass `WithPointerValues(false)` if you drive the source yourself and would rather type-switch over
`string` than `*string`.

### It matters more the wider the file

20k rows of 30 columns:

| | ns/op | B/op | allocs/op | per row |
|---|---|---|---|---|
| `Raw` | 15.2 ms | 5.3 MB | 20 067 | 1 |
| `Typed` | 15.1 ms | 5.3 MB | 20 100 | 1 |
| `Raw` + `WithPointerValues(false)` | 23.6 ms | 14.9 MB | 620 065 | **31** |

Six times the columns, thirty-one times the allocations once the values are strings. The default and
`Typed` both stay flat at one per row — `apply` is linear in the number of bound fields but writes
through pointers and allocates nothing. What the default saves grows with the width of the file: 31×
here against 6× on five columns.

`Typed` sits level with `Raw` on both widths. It did not before the decode plan resolved to field
pointers: `apply` reached for `reflect.Value.Field` on every bound field of every row, at roughly
43 ns per ten-field row against 9 ns now, and that was the whole of the gap.

### The `encode` mistake that undoes it

In `Copy` the boxing happens inside your own `encode`, so `WithPointerValues` does not affect it.
What does affect it is whether `encode` appends into `dst` or returns a fresh slice. The second
reads perfectly naturally and nothing stops you writing it:

```go
// Correct: appends into the buffer Copy keeps.
func(dst []any, c *Client) []any { return append(dst, c.ID, c.Email) }

// Costs one allocation per row, forever.
func(_ []any, c *Client) []any { return []any{c.ID, c.Email} }
```

| | ns/op | B/op | allocs/op |
|---|---|---|---|
| appending into `dst` | 24.2 ms | 11.2 MB | 600 041 |
| returning a new slice | 37.3 ms | 19.2 MB | 700 042 |

⸻

## 🔧 Fitting it into existing code

Both layers rely on structural typing, so the package usually drops in without reshaping the caller:

- If you already have your own row-source interface with `Next`/`Value`/`Err`, it can be replaced by
  `copyfrom.RowSource[T]` — the method sets match and the implementations do not change.
- If another format is read alongside CSV (XLSX, a stream from an API), it is enough for its source
  to implement the same `Next`/`Values`/`Err`. Both branches stay `pgx.CopyFromSource`, and choosing
  a format comes down to returning different values of one interface.
- Your own sentinel for a bad file becomes an alias of `csvcopy.ErrParse`, and `errors.Is` keeps
  working.

⸻

## 🔍 Alternatives

| Package | License | Streaming | Shape from header | `CopyFromSource` |
|---|---|---|---|---|
| **`csvcopy`** | MIT | yes, pull | yes | yes |
| [`jszwec/csvutil`](https://github.com/jszwec/csvutil) | MIT | yes, pull | no | no |
| [`gocarina/gocsv`](https://github.com/gocarina/gocsv) | MIT | yes, **push** | no | no |
| [`artonge/go-csv-tag`](https://github.com/artonge/go-csv-tag) | GPL-3.0 | no, `ReadAll` | no | no |

The difference between pull and push matters here. `pgx.CopyFrom` **pulls**: it calls `Next()`,
encodes the row into its buffer, and only then asks for the next one. Packages like `gocsv` **push**
— `UnmarshalToChan(in io.Reader, c interface{})` and `UnmarshalToCallback(in io.Reader, f
interface{})` drive the loop themselves. Connecting such a source to `CopyFrom` needs a goroutine
and a channel between them, which means concurrency, backpressure and carrying an error across a
goroutine boundary, all inside an open transaction. A pull interface of three methods needs none of
that.

If you only need CSV decoded into structs and no `COPY FROM`, use `csvutil`: it is more mature and
does more (`inline`, `omitempty`, custom unmarshalers). `csvcopy` solves the narrower problem of
loading into PostgreSQL, which is why it has a layer that takes its shape from the file header and
adapters for `CopyFrom`, and why it deliberately does no type conversion — that belongs to the
caller, who needs to tell "empty → NULL" from "zero → 0".

⸻

## 📄 License

MIT.
