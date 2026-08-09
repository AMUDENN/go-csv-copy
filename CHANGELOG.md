# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the usual caveat that
below `v1.0.0` the API may still move.

## [0.1.0] — Unreleased

### Added

- **`ValidateColumns` and `ErrInvalidColumns`**, for the header names a staging table is built
  from.

  `Columns()` returns names that came out of the file, and the documented staging-table pattern
  fed them straight into `CREATE TABLE`. A column called `x" ); DROP TABLE clients; --` is just a
  text file someone wrote, so the pattern as documented was an injection in anyone's code who
  copied it. The README example now validates before building DDL, and the godoc on both
  `Columns()` methods says plainly that the names are untrusted.

  `ValidateColumns` rejects empty names, duplicates, names over 63 bytes — Postgres truncates at
  `NAMEDATALEN` and two names differing only past byte 63 become one column — and anything outside
  `[A-Za-z0-9_]`, reporting every violation in one message. It is deliberately stricter than
  injection safety: prose names and non-ASCII names are refused too, because a header like that
  should be mapped explicitly rather than quoted and hoped for. Passing it does not replace
  quoting.

- **`WithMaxRecordBytes`, capping one record at 64 MiB by default**, reported as the new
  `ErrRecordTooLarge` (which wraps `ErrParse`).

  `encoding/csv` assembles a record in a single buffer and has no limit of its own, so a field
  that opens a quote and never closes it is read to the end of the file and the whole file becomes
  one value — measured, not assumed: a 1 MiB payload came back as one 1 048 577-byte field. That is
  the one input that broke the documented guarantee that memory does not depend on the size of the
  file, and it is reachable by accident, so it was also a denial of service from a single file.
  Lazy quoting is not the cause: the parser reads to the end either way and only then decides
  whether to report the quote.

  The budget is per record, not per file, so a legitimate multi-line quoted field gets the whole
  budget of its own. The bound is approximate by up to one `bufio` buffer, because `csv.Reader`
  reads ahead. Pass `WithMaxRecordBytes(0)` to remove the cap. Cost: one allocation per file and
  none per row.

### Breaking

- **A failing input stream is now `ErrIO`, not `ErrParse`.**

  `ErrIO` deliberately does not wrap `ErrParse`. Until now every non-EOF error from the underlying
  `io.Reader` — a dropped connection, a cancelled context, a failing disk — was reported as
  `ErrParse`. The README tells callers to quarantine files on `ErrParse`, so a transient network
  blip sent a perfectly good file to quarantine forever. The file was never the problem.

  The rule is `encoding/csv`'s own reporting, verified rather than assumed: whatever the parser
  objects to arrives as a `*csv.ParseError`, and anything else it hands back came from the reader
  unchanged. The original cause stays in the chain, so `errors.Is(err, context.Canceled)` answers.

  *Migration.* Read failures now need their own branch. `errors.Is(err, ErrIO)` → retry;
  `errors.Is(err, ErrParse)` → the file is bad; `errors.Is(err, ErrSchema)` → fix the code. Code
  that only checked `ErrParse` will stop catching read failures — which is the point, but it does
  mean the retry path has to exist.

- **`WithLazyQuotes` now defaults to `false`.** Quoting that `encoding/csv` would reject is
  rejected again by default.

  Verified against the standard library rather than assumed: with lazy quoting on, a quoted
  field that never closes makes the parser read to EOF looking for the closing quote, and the
  entire rest of the file arrives as one value. What surfaces then is a field-count error naming
  the line the file ended on, not the line the quote opened on — and under
  `WithVariableColumns(true)` there is no error at all, so the file is lost silently. Off, the
  same input is a parse error naming the line and column of the quote.

  *Migration.* If a real input needs the old tolerance, pass `WithLazyQuotes(true)` explicitly.
  Prefer fixing the producer: with the option on, `1;"2"3;4` is accepted as the field `2"3` and
  nothing will ever tell you.

## [0.0.2] — 2026-08-10

### Fixed

- A `csv` tag on a field of an embedded struct is now `ErrSchema` instead of being ignored.
  Embedded fields were not walked into, so the tag never reached the header matcher: no error was
  raised, the field was never written, and the column it named arrived as NULL on every row.
- Error and record line numbers are now always the physical line in the file, taken from
  `csv.ParseError.Line` and `csv.Reader.FieldPos`, never a record count. A count drifts as soon
  as the file contains a blank line, which `encoding/csv` skips, or a quoted field spanning
  several lines.
- Two fields asking for the same column are now `ErrSchema`. Nothing downstream could tell that
  apart from intent, and it is a copy-paste slip far more often than a choice.

## [0.0.1] — 2026-08-09

### Added

- First tagged version: `Reader`, `Raw`, `Typed[S, D]` and `Copy[T]`, `All()` iterators,
  `ErrParse` / `ErrMissingColumns` / `ErrSchema`, and no dependencies outside the standard
  library.

[0.1.0]: https://github.com/AMUDENN/go-csv-copy/compare/v0.0.2...HEAD
[0.0.2]: https://github.com/AMUDENN/go-csv-copy/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/AMUDENN/go-csv-copy/releases/tag/v0.0.1
