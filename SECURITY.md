# Security policy

## Reporting

Use GitHub's private vulnerability reporting: **Security → Advisories → Report a vulnerability** on
[this repository](https://github.com/AMUDENN/go-csv-copy/security/advisories/new). That keeps the
report private until a fix exists.

Please do not open a public issue for something exploitable. For anything that is not — a crash on
input you can share, a wrong line number — a normal issue is the right place and is easier to work
with.

A useful report has the input that triggers it, or a description of its shape, and the options the
reader was constructed with. Defaults matter here: several behaviours change with
`WithLazyQuotes`, `WithVariableColumns` and `WithMaxRecordBytes`.

## Supported versions

| Version | Supported |
|---|---|
| latest `0.x` | yes |
| older `0.x` | no — upgrade |

Before `v1.0.0` there is one supported version: the latest tag. Fixes go there, not into older
tags.

## What counts as a vulnerability here

This library parses bytes. It opens no sockets, reads no files it was not handed, executes no SQL,
runs no code from its input, and has no dependencies outside the standard library. That rules out
most categories outright, and leaves two that are real.

**1. Unbounded resource consumption on prepared input.** The library's central promise is that
memory does not depend on the size of the file. Any input that breaks it is a vulnerability, not
merely a bug, because the input usually arrives from somewhere the operator does not control.

One such input existed and is fixed: a field that opens a quote and never closes it made
`encoding/csv` read to the end of the file, and the whole file became a single value. The cap is
`WithMaxRecordBytes`, 64 MiB by default, reported as `ErrRecordTooLarge`. If you find another shape
that makes memory or time grow beyond one record, that is in scope. Setting
`WithMaxRecordBytes(0)` removes the cap deliberately and is out of scope.

**2. Anything that helps an injection in the calling code.** The library builds no statements, but
`Raw` hands back column names that came out of the file, and the staging-table pattern puts them on
the way to `CREATE TABLE`. A column called

```
x" ); DROP TABLE clients; --
```

is a text file anybody can write. `ValidateColumns` exists to refuse headers like it, and both
`Columns()` methods document that their result is untrusted. A documented example or a helper that
leads a caller into building a statement from unquoted input is in scope — the damage lands in
someone else's code, but the invitation would be ours.

## Out of scope

- A malformed file being rejected. That is the library working: `ErrParse` with a line number.
- Options that loosen a default when the caller asks. `WithLazyQuotes(true)` accepting damaged
  quoting, `WithAllowMissingColumns(true)` letting a bound field read as empty, and
  `WithMaxRecordBytes(0)` removing the cap are all documented, and each godoc says what it costs.
- SQL injection through a value rather than an identifier. Values reach the database through
  `pgx.CopyFrom`, which does not interpolate them into a statement.
- Anything requiring the caller to pass a struct they did not write. A `csv` tag is code, and code
  is trusted; that is `ErrSchema` territory, not a boundary.
