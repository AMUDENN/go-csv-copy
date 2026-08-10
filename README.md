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

- **`Raw`** — the shape comes **from the file's own header**. Whatever columns arrived, in their
  order, every value as text. This is the staging-table case, where the input's structure is not
  known ahead of time and a SQL script types the data afterwards.
- **`Typed[S, D]`** — the shape comes **from `csv` struct tags**. Columns are matched by name, so
  their order in the file does not matter, extra columns are ignored, and a column a tag asks for
  and the file lacks is an error. Each row passes through your `convert`, which types and validates
  the values.

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
import "github.com/AMUDENN/go-csv-copy"   // package csvcopy
```

Go 1.24 or newer. The `go` directive names a minor version on purpose: pinning a patch would force
every consumer to fetch a toolchain, which is a strange thing for a package whose whole point is
bringing nothing with it.

⸻

## 🚀 Quick start

### Shape from the file header (staging table)

```go
src, err := csvcopy.NewRaw(file)
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
if err := csvcopy.ValidateColumns(columns); err != nil {
    return err
}

if err := createStagingTable(ctx, tx, table, columns); err != nil {
    return err
}

n, err := tx.CopyFrom(ctx, pgx.Identifier{table}, columns, src)
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

rows, err := csvcopy.NewTyped(file, (*clientRow).toClient)
if err != nil {
    return err
}

source, err := csvcopy.NewCopy(rows, len(clientColumns),
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

`Typed` satisfies `RowSource[D]`, so decoding and the layout of a COPY row can live in different
layers: one hands out a `RowSource`, and the layer that owns the database decides which columns it
lands in.

### Without a database

`Reader` and `Typed` stand on their own — no `COPY`, no pgx, no `[]any`. Ranging cannot carry an
error out, so the loop stops at the first bad row and `Err()` reports it afterwards, the same shape
as `bufio.Scanner`:

```go
rows, err := csvcopy.NewTyped(file, (*clientRow).toClient)
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
reader, err := csvcopy.NewReader(file)
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
| `NewReader(r, opts...)` | `*Reader` — header + row-by-row reading | you only need CSV parsing, no COPY |
| `NewRaw(r, opts...)` | `*Raw` — `pgx.CopyFromSource` + `Columns()` | the shape comes from the file header |
| `NewTyped[S, D](r, convert, opts...)` | `*Typed[S, D]` — `RowSource[D]` | the shape comes from `csv` tags |
| `NewCopy[T](src, columns, encode)` | `*Copy[T]` — `pgx.CopyFromSource` | adapting any `RowSource[T]` to COPY |

Every constructor returns an error rather than panicking. Nil wiring — a nil reader, `convert`, `src`
or `encode` — is `ErrSchema`.

### Interfaces

```go
// RowSource is anything that yields values one at a time. Typed satisfies it,
// and so can a source of your own over XLSX, an API or a generator.
type RowSource[T any] interface {
    Next() bool
    Value() T
    Err() error
}
```

### Options

| Option | Default | Effect |
|---|---|---|
| `WithComma(r rune)` | `';'` | field delimiter |
| `WithLazyQuotes(bool)` | `false` | tolerate a bare quote, and a quoted field that never closes, instead of failing |
| `WithTrimLeadingSpace(bool)` | `true` | drop white space at the start of a field |
| `WithTrimValues(bool)` | `true` | `TrimSpace` every value |
| `WithHeaderRow(n uint)` | `1` | which row holds the header; rows above it are dropped |
| `WithNormalizeHeader(fn)` | collapse whitespace | normalize a name before matching — applied to **both** the file's column names and the `csv` tag values, so it must be pure and idempotent |
| `WithTag(string)` | `"csv"` | which struct tag `Typed` reads column names from |
| `WithVariableColumns(bool)` | `false` | accept rows of a different width: extra values dropped, missing trailing ones → `nil` in `Raw`, `""` in `Typed` |
| `WithAllowMissingColumns(bool)` | `false` | do not fail when a tagged column is absent from the header |
| `WithPointerValues(bool)` | `false` | `Raw` yields `*string` instead of `string`, removing one allocation per cell |
| `WithMaxRecordBytes(int64)` | `64 MiB` | cap on one record; zero removes it. Exceeding it is `ErrRecordTooLarge` |
| `WithComment(rune)` | `0` (off) | a rune that starts a comment line, skipped wherever it appears |

### The `csv` tag

The whole tag is the column name. There are no comma-separated options: a field tagged
`csv:"name,omitempty"` asks for a column literally called `name,omitempty`, which no header will
have, and `ErrMissingColumns` says so. `-` is the one value with a meaning of its own — it drops the
field.

Two rules exist because breaking them loses data silently, and the package refuses both with
`ErrSchema`:

```go
type row struct {
    base                       // ✗ tags inside an embedded struct are not matched
    A string `csv:"id"`
    B string `csv:"id"`        // ✗ two fields, one column
}
```

Embedded fields are not walked into. Left to pass, a tag one level down would bind to nothing, the
field would read as empty on every row, and the column it named would reach the database as `NULL` —
the same damage `ErrMissingColumns` exists to prevent, only without the error. List the columns on
the struct itself.

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

`Unused()` is worth logging as a warning: when an export renames a column, the tag simply matches
nothing, no error is raised, and the wrong data reaches the database. The unbound column is the only
visible trace.

⚠️ **`Columns()` returns untrusted input.** The names come out of the file, and the staging-table
pattern puts them on the path to `CREATE TABLE` — a column called `x" ); DROP TABLE clients; --` is
just a text file someone wrote. Quote every name that reaches a statement with
`pgx.Identifier{name}.Sanitize()`, or refuse the header up front with
`csvcopy.ValidateColumns(columns)`, which rejects empty names, duplicates, names over 63 bytes
(Postgres truncates at `NAMEDATALEN` and two columns then collide) and anything outside
`[A-Za-z0-9_]`, listing every violation at once. `CopyFrom` itself quotes them.

⚠️ `WithAllowMissingColumns(true)` is dangerous. The absent field reads as the empty string on every
row and reaches the database as `NULL`, quietly wiping whatever that column held. Only use it where
that is acceptable.

### Errors

Errors are split by who can fix them.

| Sentinel | Wraps `ErrParse` | Cause |
|---|---|---|
| `ErrParse` | — | the file's content is wrong: a malformed record, a failing `convert` |
| `ErrMissingColumns` | yes | the header lacks a column a tag asks for |
| `ErrInvalidColumns` | yes | `ValidateColumns` refused a header name |
| `ErrRecordTooLarge` | yes | one record outgrew `WithMaxRecordBytes` |
| `ErrIO` | **no** | the stream failed, not the file: a dropped connection, a cancelled context, a bad disk |
| `ErrSchema` | **no** | a bug in the calling code: not a struct, a tag on a non-string or unexported field, a tag inside an embedded struct, two fields asking for one column, a nil reader/convert/src/encode, an unusable delimiter |

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
  physical line it starts on.
- **Memory is constant** and independent of the row count — bounded by the largest single record,
  and that bound is `WithMaxRecordBytes` (64 MiB by default). `encoding/csv` assembles a record in
  one buffer and has no limit of its own, so a field that opens a quote and never closes it is read
  to the end of the file and the whole file becomes one value; the cap is what makes the guarantee
  hold on input nobody checked. `Values()` reuses one slice, which is safe under `pgx.CopyFrom`
  because it encodes a row before asking for the next. If you drive a source by hand, do not retain
  the result of `Values()` between iterations.
- **Not safe for concurrent use.** One source, one goroutine — the same as `pgx.CopyFrom`.
- `Reader`, `Raw` and `Typed` do not close the `io.Reader` you give them.

⸻

## ⚡ Performance

Live memory is constant, but **allocations over a pass grow linearly**, and it is worth knowing
where from. 100k rows of 5 columns, go1.26.1, Ryzen 5 7500F:

| | ns/op | B/op | allocs/op | per row |
|---|---|---|---|---|
| `Typed` | 13.6 ms | 3.2 MB | 100 038 | 1 |
| `Typed` via `All()` | 14.4 ms | 3.2 MB | 100 038 | 1 |
| `Raw` | 18.7 ms | 11.2 MB | 600 033 | 6 |
| `Raw` via `All()` | 20.6 ms | 11.2 MB | 600 032 | 6 |
| `Raw` + `WithPointerValues` | 11.2 ms | 3.2 MB | 100 033 | 1 |

Ranging costs nothing: an `iter.Seq` returned from a method closes over the source once per pass,
not once per row, so `All()` sits on the same allocation count as the `Next()` loop it replaces.

The one allocation per row is `encoding/csv`: it creates one string per record even with
`ReuseRecord`, and that is the floor short of `unsafe`.

The other five are the columns. Boxing a `string` into an `any` **always** allocates 16 bytes for
its header, so `Raw` pays one allocation per cell by default — that is the shape of
`Values() ([]any, error)`. `WithPointerValues(true)` yields `*string` out of an array allocated once
per file: a pointer is pointer-shaped, the interface holds it directly, and boxing is free. On a
30-column file the difference is thirtyfold.

The option is off by default: pgx dereferences `*T` through its pointer encode plan and `*string` is
the ordinary way to pass a nullable text value, but this has not been verified against a real
Postgres. Turn it on deliberately and check the loaded result.

### It matters more the wider the file

20k rows of 30 columns:

| | ns/op | B/op | allocs/op | per row |
|---|---|---|---|---|
| `Typed` | 15.1 ms | 5.3 MB | 20 099 | 1 |
| `Raw` | 20.9 ms | 14.9 MB | 620 065 | **31** |
| `Raw` + `WithPointerValues` | 13.7 ms | 5.3 MB | 20 066 | 1 |

Six times the columns, thirty-one times the allocations for `Raw`. `Typed` stays flat at one per
row — `apply` is linear in the number of bound fields but writes into a struct and allocates
nothing. So the option is worth 31× here against 6× on a five-column file, and it makes `Raw`
faster than `Typed`.

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
| appending into `dst` | 23.5 ms | 11.2 MB | 600 042 |
| returning a new slice | 26.4 ms | 19.2 MB | 700 043 |

⸻

## 🔧 Fitting it into existing code

Both layers rely on structural typing, so the package usually drops in without reshaping the caller:

- If you already have your own row-source interface with `Next`/`Value`/`Err`, it can be replaced by
  `csvcopy.RowSource[T]` — the method sets match and the implementations do not change.
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
— `UnmarshalToChan(in io.Reader, c interface{})` and `UnmarshalToCallback(in io.Reader, f interface{})`
drive the loop themselves. Connecting such a source to `CopyFrom` needs a goroutine and a channel
between them, which means concurrency, backpressure and carrying an error across a goroutine
boundary, all inside an open transaction. A pull interface of three methods needs none of that.

If you only need CSV decoded into structs and no `COPY FROM`, use `csvutil`: it is more mature and
does more (`inline`, `omitempty`, custom unmarshalers). `csvcopy` solves the narrower problem of
loading into PostgreSQL, which is why it has a layer that takes its shape from the file header and
adapters for `CopyFrom`, and why it deliberately does no type conversion — that belongs to the
caller, who needs to tell "empty → NULL" from "zero → 0".

⸻

## 📄 License

MIT.
