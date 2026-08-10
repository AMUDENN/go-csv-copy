# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project follows
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

From `v1.0.0` on, the exported API is frozen for the whole `1.x` line: nothing is removed or
given a different meaning without a `v2`, which would carry its own import path. Everything below
`v1.0.0` moved freely, and the `0.x` entries read the way they do because of it.

## [1.0.0] — 2026-08-10

The API is frozen here. Everything in this entry is the difference against `v0.1.0`, and it is the
last set of breaking changes on this import path.

The theme of it, if there is one: the package had a rule — an error names who can fix it, and
nothing fails quietly — and several places did not follow it. A struct with no tags decoded a whole
file into empty values without a word. An error with no line of its own printed a plausible number
anyway. A negative byte cap turned the memory bound off. Those are all the same bug, and they are
fixed the same way.

### Breaking

- **Split into three packages.** Everything moved; the root package keeps only the error vocabulary.

  | Was | Is now |
  |---|---|
  | `csvcopy.NewReader` / `NewRaw` / `NewTyped`, every `With*` option, `NormalizeSpace` | `csvcopy/decode` |
  | `csvcopy.RowSource` / `NewCopy` / `Copy` / `ValidateColumns` | `csvcopy/copyfrom` |
  | `csvcopy.ErrParse` / `ErrIO` / `ErrSchema` | unchanged |
  | `csvcopy.ErrMissingColumns` / `ErrDuplicateColumns` / `ErrRecordTooLarge` | `decode.` |
  | `csvcopy.ErrInvalidColumns` | `copyfrom.` |

  The point is the import graph: `decode` and `copyfrom` do not import each other, so a parser's
  import block cannot mention a database and a repository's cannot mention CSV. A `RowSource` is a
  `RowSource` whether it came from a file, an XLSX sheet or a paged API, and `copyfrom` is now the
  package that has no way of knowing which. One package could only say that in a comment.

  The three categories stayed in the root rather than moving into `decode`, so that
  `errors.Is(err, csvcopy.ErrIO)` reads the same whichever layer raised it and a `copyfrom` user
  does not import a CSV reader to check an error. The README's `var ErrBadFile = csvcopy.ErrParse`
  is unchanged. The named sentinels moved to the package that raises them, and each still wraps one
  of the three.

  `decode.Raw` satisfies `pgx.CopyFromSource` while living in `decode`, which is not a slip: nothing
  about it knows pgx exists, it only has the right method set, and its header and options belong
  with the reader. `copyfrom` is for values that did not come from a CSV file.

  Migration is import-path work, no behaviour changed by the move. Fuzzing now runs against
  `./decode` rather than `./...`, because `-fuzz` refuses more than one package.

- **A negative `WithMaxRecordBytes` is now `csvcopy.ErrSchema` from the constructor**, where it used
  to be a second spelling of zero — that is, of "remove the cap". A negative byte count reaches that
  option as arithmetic gone wrong (a size computed from a field nobody set, a subtraction, an
  overflow) far more often than as a request, and the old reading turned that slip into an unbounded
  read: the package's only defence against a runaway record, switched off in silence. Zero still
  means it, explicitly, and is still out of scope for the security policy. `SECURITY.md` says which
  of the two is which now.

  While checking that claim, the README turned out to promise more than the constructors deliver, so
  it now lists both halves exhaustively: what is refused (nil wiring, an unusable delimiter or
  comment rune, `WithTag("")`, a struct with no tags or a tag that normalizes to nothing, a tagged
  field that is not an exported string, two fields on one column, a negative cap, `Values()` before
  `Next()`) and what is quietly clamped instead (`WithHeaderRow(0)` → 1, an absurd header row → the
  empty-file case, `NewCopy(..., -5, ...)` → 0, `WithNormalizeHeader(nil)` → identity, a `nil` in
  `opts...` → skipped).

- **`at`, the internal line-attribution type, lost its `exact` flag** — invisible from outside, but
  worth recording because coverage found it: the "exact" branch could never be printed, since the
  only error that has an exact line is `*csv.ParseError`, which carries the line in its own message.
  What is left is `after`, a lower bound that renders as `after line N` or, before any record has
  been read, `at the start of the file`.

### Fixed

- **A struct with no `csv` tag at all is now `ErrSchema`**, from the constructor and on an empty
  file too. It was accepted in silence: nothing bound, every row decoded as empty, `Err()` returned
  `nil`, `Rows()` counted the file's rows honestly, and `CopyFrom` wrote a full table of empty
  values over a good one — with `Unused()` holding the entire header as the only trace, for whoever
  thought to look. This is exactly the damage `ErrMissingColumns` is fatal to prevent, in its
  complete form: there one field binds to nothing, here all of them do. `json:"id"` where
  `csv:"id"` was meant is how it happens, and that is not a rare slip.

  `WithTag("")` is the same failure from the other side — `reflect.StructTag.Get("")` answers `""`
  for every field — and is now rejected next to the delimiter checks.

- **A column a tag asks for that the header carries twice is now `ErrDuplicateColumns`** (wrapping
  `ErrParse`), instead of binding to the first occurrence. Which values got loaded was decided by
  the file's column order, silently, and both columns hold plausible data so nothing downstream
  could notice. The mirror image — two fields asking for one column — has been `ErrSchema` since
  0.0.2; this is the same ambiguity seen from the file's side rather than the struct's.

  Normalizing is what makes it reachable without a literally duplicated header: `"a  b"` and
  `"a b"` are one name after `NormalizeSpace`, so the collapse happens inside this package. A
  duplicate no tag asks for is not ambiguous and still shows up in `Unused()`.

- **An error with no line of its own no longer invents one.** `ErrIO` and `ErrRecordTooLarge` do not
  arrive as a `*csv.ParseError`, and the fallback was a record counter — which drifts the moment a
  record spans several lines. A stream that broke on line 6, after a quoted record covering lines
  2 to 5, was reported as `line 3`; the caller opened the file there and found nothing related to
  the failure. Both now fall back to the last record read in full and say what that is:

  ```
  csv read: after line 2: connection reset by peer
  csv parse: record exceeds the byte limit: after line 2
  ```

  `after line N` is a bound, not a location. `Line()` reports the same number and its godoc says
  when it is which. `ErrParse` is unaffected: `encoding/csv` names the physical line, and that is
  still what is printed.

- **An error from `convert` keeps a category it named itself.** It was wrapped in `ErrParse`
  unconditionally, so a callback that failed because a lookup service was down sent a perfectly
  good file to quarantine — the exact mistake `ErrIO` was introduced to stop, reintroduced one
  layer up. An error wrapping `csvcopy.ErrIO` or `csvcopy.ErrSchema` now stays what it says it is,
  and one carrying `context.Canceled` or `context.DeadlineExceeded` is read as `ErrIO` without
  being asked: a cancelled load is not a bad file, and nobody wraps a context error by hand.
  Everything else is still `ErrParse`.

- **A `csv` tag of nothing but whitespace is now `csvcopy.ErrSchema`.** The `tag == ""` check runs
  on the raw tag and normalizing happens after it, so `csv:"   "` got through and arrived as `""` —
  and then matched a header column that normalizes to `""` as well. Two things nobody named, bound
  to each other, on every row, with no error. Every neighbouring decision already went the other
  way: `WithTag("")` is refused, an empty column name is refused by `ValidateColumns`, and an empty
  name produced by the normalizer cannot be the one that passes.

- **`ValidateColumns` refuses two names that differ only in case.** `[]string{"ID", "id"}` passed:
  the duplicate check compared bytes. Quoted, those are two legal columns; unquoted, Postgres folds
  both to `id` and the `CREATE TABLE` fails with `column "id" specified more than once` — which is
  the statement this function is asked about. The message is worded apart from an exact duplicate
  ("collides with column 1 (\"ID\") unless both are quoted"), because a reader told "duplicates"
  while looking at two visibly different strings concludes the check is broken. Tightened before
  `1.0` on purpose: a header that passes today and fails tomorrow is not something to do to people
  afterwards.

- **`ValidateColumns` refuses a name starting with a digit.** `123` and `2023_q1` pass every other
  rule it has and still cannot open a column definition — `CREATE TABLE t (123 TEXT)` is a syntax
  error — while the function's own first sentence promised to reject names that "cannot safely be
  used to build a statement". The godoc no longer promises more than it delivers either: it now says
  outright that it does not know your Postgres version's reserved words, so `select` and `table`
  pass, which is one more reason quoting is not optional after it succeeds. The README's staging
  example shows the quoting it was previously hiding inside an unshown `createStagingTable`.

- **`Values()` before the first `Next()` is `ErrSchema`** on `Raw` and `Copy`. It used to encode the
  zero value and hand back a row of empty values with a `nil` error — a plausible-looking row that
  nothing would reject. Unreachable under `pgx.CopyFrom`, which always calls `Next()` first, and
  reachable in any hand-written loop.

### Added

- **`Raw.Extra()`**, reporting how many values the current record had past the header — all of them
  dropped. Under `WithVariableColumns` the short case is visible in the data, because the trailing
  values arrive as NULL; the wide case left nothing behind at all. `Values()` is sized by the
  header, the cells past it never reached anyone, and a record that is too wide is the usual shape
  of a misread delimiter — exactly the signal `WithVariableColumns` suppresses when it is turned on.

- **`Raw.Truncated()`**, the counterpart to `Typed.Truncated()`, so the two sources answer the same
  question the same way rather than one of them making the caller dig through `[]any` for it.

- **`Typed.Extra()`**, the same count on the typed side. It was left out at first, which made the
  gap worse rather than smaller: in `Raw` a wide record is at least visible by comparing `Record()`
  with `Columns()`, while a `Typed` caller looks at their own `D` and at `Truncated()`, which is
  about the other end of the record and reads `false`. So exactly one of the two sources was
  defended against the thing both docs describe as the usual meaning of a wide row — a misread
  delimiter. Both are now.

### Documentation

- **The memory bound is stated as what it is: about 4× `WithMaxRecordBytes`, not 1×.** Measured —
  a 64 MiB cap peaks at 263 MiB of heap, a 16 MiB cap at 64 MiB. `encoding/csv` holds the physical
  line in one growing buffer and the assembled record in another, and grows each by doubling, so
  the old array and the new one are live at the same moment. The package doc, the README and
  `WithMaxRecordBytes` all said "bounded by the largest single record, and that bound is
  `WithMaxRecordBytes`", which anyone could disprove in five minutes with an unclosed quote. The
  approximation that *was* documented is a different one, and both are now stated: read-ahead makes
  the accounting off by one `bufio` buffer, doubling makes the peak a multiple.

- `WithMaxRecordBytes` now mentions that a small budget also caps the size of the reads underneath
  it: a `Read` that would overrun what is left is trimmed to it. Invisible at 64 MiB, visible at
  64 KiB over a network.

- **`WithTrimValues` says that it does not exempt quoted fields.** It is on by default and applies
  `strings.TrimSpace` to every value, quoted or not — so `"  x  "` arrives as `"x"`, and a field of
  three deliberate spaces arrives as `""`. Quoting is how a CSV says "these spaces are data", and in
  a nullable column the result is a different value from what the file held, which is the exact
  distinction this package explains in four other places. The default stands (spreadsheet exports
  pad for alignment, and one call turns it off) but it is no longer a surprise, and a test now pins
  it in both directions instead of leaving it to prose.

- **`ErrRecordTooLarge`'s own godoc now carries the caveat**, not only `WithMaxRecordBytes`. It
  wraps `ErrParse`, the README tells you to quarantine files on `ErrParse`, and there is one input
  where those two combine against a good file — see the next entry. The caveat belongs where the
  person deciding to quarantine is reading, which is the sentinel.

- **The record budget is spent by blank and comment lines too.** It is reset per `Read`, and
  `encoding/csv` skips those lines *inside* one, so a run of them longer than the cap comes back as
  `ErrRecordTooLarge` when no record in the file is oversized — and since that wraps `ErrParse`, a
  caller following this package's own advice would quarantine a good file. Measured: 200 blank lines
  or 40 comment lines against a 64-byte cap. Documented rather than fixed, and `budgetReader` now
  carries the reason in a comment so the obvious fix does not get written later: it sees raw bytes
  and cannot tell a comment line from the same bytes inside an unclosed quoted field, so resetting
  per line would let `"` followed by endless newlines refill the budget forever — reopening the hole
  the type exists to close. Pinned by a test that says as much.

- **The `copyfrom` package example returned pgx's error instead of the source's**, one line under a
  comment explaining why you must not. That is the mistake the whole ordering convention exists to
  prevent, sitting in the godoc that teaches it. It also declared an `n` it never used, so it did
  not compile if you copied it — as did both examples in the root package doc and the staging
  example in the README. All four now compile as written.

- `NewReader` says why it validates options a `Reader` never reads, such as `WithTag`: one settings
  type serves all three layers, and a mistyped option should surface from the constructor the caller
  reached for rather than from the one they reach for next week.

- `integration/go.mod` records why it declares `go 1.25.0` while the library declares `go 1.23`:
  pgx/v5 v5.10.0 requires it, and `go mod tidy` restores the line whenever it is lowered. The
  compatibility matrix builds the root module at 1.23 and never enters that directory.

### Internal

- **The decode plan resolves to field pointers once per file.** `apply` called
  `reflect.Value.Field` followed by `SetString` for every bound field of every row, although the
  plan is built once per file and the struct it writes into never moves. It now takes the field
  addresses when the plan is bound and the row path is a walk over `*string`. Measured on ten bound
  fields: **43 ns → 9 ns per row**, no allocation on either. End-to-end that is about **-11%**
  (geomean over `Typed` and `TypedWide`, `-count=6`), and it puts `Typed` level with `Raw` on both
  widths, where the README's tables previously showed it 2 ms behind — that gap was this.

  Cost: one allocation per file for the bound plan (`BenchmarkNewTyped` 39 → 40), and `Typed` is now
  genuinely uncopyable, which is the next entry. A new `BenchmarkDecodePlanApply` measures the row
  path on its own, because end-to-end numbers bury it under `encoding/csv`.

- **`Raw` and `copyfrom.Copy[T]` embed a `noCopy` too.** Both constructors already warned against
  copying the value in their godoc — `Raw` shares a `Reader` and a string array, `Copy` shares the
  row buffer that every `Values()` writes into and the counter it reports — and `vet` said nothing
  about either, while the identical hazard on `Typed` was enforced. A mechanism that exists in the
  package and is applied to one type of three is a forgotten step, not a decision. `copyfrom` gets
  its own six-line copy of the type rather than importing `decode` for it: that import is the one
  thing the package split exists to prevent, and the file says so, so that nobody later
  "deduplicates" it into a shared package.

- **`Typed` embeds a `noCopy`, so `go vet` refuses to copy it.** It holds pointers into its own
  `row`, so `t := *typedPtr` produced a value whose plan decoded into the *original's* struct while
  `convert` read the copy's — every field empty, on every row, with no error anywhere. `copylocks`
  is the only mechanism that reports a copy at all, and there is no lock here to protect; the type
  exists purely to borrow that check. Verified against a throwaway file: `assignment copies lock
  value`. The godoc of all three constructors now says to use the pointer they return, and why.

- **The peak-memory claim has a test.** "About 4× the cap, not 1×" lived only in prose, in the four
  places that state it — and this file records what that cost last time. The new
  `TestMemoryStaysAMultipleOfTheCap` reads one oversized record out of a 64 MiB input against a 1
  MiB cap and asserts total allocation stays under 24× it (measured: 7×, stable across cap sizes;
  peak live heap is about 4×, but `TotalAlloc` is exact where a peak has to be sampled). The input
  is finite on purpose, so a regression fails the suite instead of hanging it — checked by disabling
  the cap, which turns the failure into a parse error at column 67108866. Skipped under `-short`,
  and the one test in the package that is not `t.Parallel()`: it reads process-wide counters.

- **CI fails on a source file that is not in the index.** The three-package split left six new
  files untracked, including the one defining `decode`'s sentinels and the one defining `noCopy` —
  without them the package does not compile at all. Every other gate runs against the checkout and
  so could not see it, and a tag is immutable in the module proxy: a version published in that
  state never builds and never can be repaired, only superseded. One `git ls-files -o` closes it.

- `BenchmarkValidateColumns` and `BenchmarkNewReader` cover the two per-file paths that had none.
  Neither is hot; they are there so that a future check reaching for a regexp or a list of reserved
  words shows up as an order of magnitude rather than as a support ticket.

- `Copy` resolves the `Line()` and `Record()` interface assertions once in `NewCopy` instead of on
  every call. Whether a source can name a line is a property of its type, so asking again could only
  ever produce the same answer.

- **The `go` directive drops to 1.23**, widening the support window by a release. Nothing in the
  library needed 1.24: the real floor is `iter.Seq`, which `All()` returns and which arrived in
  1.23. The only thing that wanted 1.24 was `testing.B.Loop` in the benchmarks — code a consumer
  never compiles. A `go` directive gates consumers, so a package whose whole claim is that it brings
  nothing with it should not cost them a toolchain over a test-only convenience.

  What that costs: the eleven benchmark loops go back to `for range b.N`, and `b.Loop` is better at
  the job than `b.N` is — it keeps the loop body from being optimised away and excludes setup from
  the timer without an explicit `b.ResetTimer`. Every one of these benchmarks already called
  `ResetTimer` after its setup except `BenchmarkNewTyped`, which now does too, and every loop body
  checks an error or a row count, so nothing here is elidable. Allocation counts are unchanged,
  which is what these benchmarks are actually watched for.

  The compatibility matrix now builds on 1.23 rather than 1.24, so the directive keeps a machine
  behind it.

- Every test now calls `t.Parallel()`, top-level and subtests alike. They are all in-memory and
  share nothing, and the suite is roughly twice as fast under `-race`.

## [0.1.0] — 2026-08-10

### Added

- **`Typed.Truncated()`**, reporting whether the current record ran out before a bound column.

  The README claimed `WithVariableColumns` turned missing trailing values into `nil`. True for
  `Raw`, which hands pgx an `[]any`; false for `Typed`, where a tagged field is declared `string`,
  has no `nil` to hold, and gets `""`. In Postgres a NULL and an empty string are different values,
  so the documentation was wrong about something that changes what lands in the database. Both the
  README and the option's godoc now state the asymmetry and why it is forced rather than chosen.

  `convert` cannot see the flag: it is called inside `Next` with the struct as its only argument,
  and widening that signature would change every caller's code. Read it in the loop instead.

- **`WithComment`**, forwarding `csv.Reader.Comment`. `WithHeaderRow` drops a fixed number of lines
  at the top, which does not help with notes scattered through a file. Validated like the
  delimiter — a comment rune equal to it, or one `encoding/csv` will not accept, is `ErrSchema`
  from the constructor rather than a failure on the first row. Skipped lines do not shift the line
  numbers in errors.

- **`Copy.Line()` and `Copy.Record()`**, forwarded to the wrapped source when it can answer and
  `0` / `nil` when it cannot. The documented recovery for a failed `CopyFrom` is to read the
  source's error first, because that is the one naming the line — but the obvious code passes the
  source straight into `NewCopy` and keeps no other reference to it, so there was nothing left to
  ask. Queried through an inline interface assertion rather than by widening `RowSource`, so a
  source over an API or a generator does not have to declare methods it cannot implement.

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

- **`Raw` now yields `*string` instead of `string` by default** (`WithPointerValues` defaults to
  `true`).

  Boxing a `string` into an `any` always allocates 16 bytes for its header, so plain strings cost
  one allocation per cell. On five columns that is 6 allocations per row against 1; on thirty it is
  31 against 1, and it makes `Raw` the fastest of the three layers instead of the slowest.

  The option existed before and was off, because "pgx dereferences `*T` through its pointer encode
  plan" was an argument rather than a result. It is now a result: the integration tests load the
  same file both ways into an all-TEXT table and compare an `md5` of the loaded rows, and load both
  ways into a table of `bigint`, `numeric` and `date`. Both agree. A value the record never reached
  is still a nil interface, so still `NULL`.

  *Migration.* Nothing changes if the source goes to `pgx.CopyFrom` — which is the case the package
  is for. If you drive `Raw` yourself and type-switch on `string`, either handle `*string` or pass
  `WithPointerValues(false)`. `Reader.All()` still yields `[]string` and is the better path for
  reading without a database. `Typed` and `Copy` are unaffected.

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

[1.0.0]: https://github.com/AMUDENN/go-csv-copy/compare/v0.1.0...v1.0.0
[0.1.0]: https://github.com/AMUDENN/go-csv-copy/compare/v0.0.2...v0.1.0
[0.0.2]: https://github.com/AMUDENN/go-csv-copy/compare/v0.0.1...v0.0.2
[0.0.1]: https://github.com/AMUDENN/go-csv-copy/releases/tag/v0.0.1
