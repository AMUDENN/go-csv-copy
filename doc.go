/*
Package csvcopy holds the error vocabulary the layers below share. The code lives
in two packages under it, and which one you import says what your code does.

	decode    CSV in, your values out: Reader, Raw, Typed, and the options.
	copyfrom  values in, a pgx.CopyFrom source out: RowSource, Copy, ValidateColumns.

They do not import each other. A parser imports decode and never mentions a
database; a repository imports copyfrom and never mentions CSV, because a
RowSource is a RowSource whether it came from a file, an XLSX sheet or an API. The
separation is in the import graph, not only in the prose.

This package is what both of them agree on: ErrParse, ErrIO and ErrSchema, the
three answers to "who can fix this". Nothing else is here, so importing it costs
nothing and an errors.Is check reads the same wherever the error was raised.

# Streaming

A row is read, handed over and forgotten, so memory does not depend on the size of
the file. There are no dependencies outside the standard library.

Bulk-loading into PostgreSQL is what the shape is for, and the reason the row
sources expose Next/Values/Err: that method set is pgx.CopyFromSource, satisfied
structurally rather than by importing pgx, so the version of pgx stays the
application's choice. Nothing here requires a database, though - decode.Reader is a
plain CSV reader and decode.Typed decodes into your own types. Both have All for
ranging.

# The staging-table case

decode.Raw takes the shape from the file's own header - whatever columns arrived,
in their order, all as text - and copyfrom.ValidateColumns says whether those names
can be built into a statement:

	src, err := decode.NewRaw(file)
	if err != nil {
		return err
	}
	if len(src.Columns()) == 0 {
		return nil // empty file, nothing to load
	}
	// The names came out of the file, so they are untrusted on the way to DDL.
	if err := copyfrom.ValidateColumns(src.Columns()); err != nil {
		return err
	}

	_, err = tx.CopyFrom(ctx, pgx.Identifier{table}, src.Columns(), src)
	// The source's error first: it names the line, pgx's does not.
	if srcErr := src.Err(); srcErr != nil {
		return fmt.Errorf("read input at line %d: %w", src.Line(), srcErr)
	}
	if err != nil {
		return err
	}

# The typed case

decode.Typed takes the shape from struct tags and hands out whatever your convert
func returns; copyfrom.NewCopy lays that value out across the columns of a COPY.
The two halves can sit in different layers, and usually should:

	type row struct {
		ID   string `csv:"id"`
		Name string `csv:"name"`
	}

	rows, err := decode.NewTyped(file, (*row).toEntity)
	if err != nil {
		return err
	}

	source, err := copyfrom.NewCopy(rows, len(columns), func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})
	if err != nil {
		return err
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{table}, columns, source)
	if srcErr := source.Err(); srcErr != nil {
		return fmt.Errorf("read input at line %d: %w", source.Line(), srcErr)
	}
	if err != nil {
		return err
	}

Both examples check the source before pgx, and that order is not stylistic. When a
row fails, pgx reports that its source stopped; the source reports what went wrong
and on which line. Reading pgx's error first throws the useful one away.

# Errors

Errors are split by who can fix them, because the three answers differ. Errors the
file's content causes wrap ErrParse, so an application can alias its own sentinel
to it and quarantine the file. Errors the stream causes - a dropped connection, a
cancelled context - wrap ErrIO instead and deserve a retry, not a quarantine: the
file is fine. Errors the calling code causes wrap ErrSchema, because no file will
ever fix them.

The named sentinels live with the code that raises them - decode.ErrMissingColumns,
decode.ErrDuplicateColumns, decode.ErrRecordTooLarge, copyfrom.ErrInvalidColumns -
and every one of them wraps one of the three here.

A convert func can put an error in the first two categories itself: wrap ErrIO or
ErrSchema and that is what it stays, and a cancelled context is read as ErrIO
without being asked.

# Guarantees

An empty input is not an error: no columns, no rows, no error. The caller decides
what that means.

Memory is bounded by the largest single record rather than by the file, and the
knob is decode.WithMaxRecordBytes - 64 MiB by default. The cap is not the peak:
reading one record that size takes up to about 4x it, because encoding/csv keeps
the physical line in one buffer and the assembled record in another and grows each
by doubling. So memory is bounded by a multiple of the cap and not by the size of
the file, and the multiple is about four.

A UTF-8 BOM is stripped, read with io.ReadFull so a slow reader cannot leave it in
place.

The first bad row stops the stream for good. Rows are not skipped: a partly loaded
table is worse than a failed load. The error is available from Err, says where it
happened, and stays there - a source that has failed yields nothing more, so
ranging All again cannot resume past the row that broke. Breaking out of a loop is
not an error, and the next pull carries on from where it stopped.

Record and Line name the row that failed, so an error message can carry it. Line is
the physical line of the file, taken from encoding/csv, so it stays right across
blank lines and quoted fields spanning several lines. Only encoding/csv can say
that, and it only says it for a parse error: when the stream fails or a record
outgrows the cap there is no parse error to ask, and both Line and the message fall
back to the last record read in full - "after line N", not "line N".

Values reuses one slice between rows. That is safe under pgx.CopyFrom, which
encodes a row before asking for the next; a caller driving a source by hand must
not retain it, and must not ask for it before the first Next.

Nothing here is safe for concurrent use, and neither is pgx.CopyFrom.
*/
package csvcopy
