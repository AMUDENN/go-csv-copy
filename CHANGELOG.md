# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html) — with the usual caveat that
below `v1.0.0` the API may still move.

## [0.1.0] — Unreleased

### Breaking

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
