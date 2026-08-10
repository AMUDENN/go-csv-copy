/*
Package csvcopy streams CSV one row at a time.

A row is read, handed over and forgotten, so memory does not depend on the size of
the file. There are no dependencies outside the standard library.

Bulk-loading into PostgreSQL is what it is shaped for, and the reason the row
sources expose Next/Values/Err: that method set is pgx.CopyFromSource, satisfied
structurally rather than by importing pgx, so the version of pgx stays the
application's choice. Nothing here requires a database, though - Reader is a plain
CSV reader, and Typed decodes into your own types. Both have All for ranging.

# Layers

Two ways to fix the shape of a file, because both turn up in practice.

Raw takes the shape from the file's own header - whatever columns arrived, in
their order, all as text. This is the staging table case, where a SQL script types
the data afterwards:

	src, err := csvcopy.NewRaw(file)
	if err != nil {
		return err
	}
	if len(src.Columns()) == 0 {
		return nil // empty file, nothing to load
	}
	// The names came out of the file, so they are untrusted on the way to DDL.
	if err := csvcopy.ValidateColumns(src.Columns()); err != nil {
		return err
	}

	n, err := tx.CopyFrom(ctx, pgx.Identifier{table}, src.Columns(), src)
	// The source's error first: it names the line, pgx's does not.
	if srcErr := src.Err(); srcErr != nil {
		return fmt.Errorf("read input at line %d: %w", src.Line(), srcErr)
	}
	if err != nil {
		return err
	}

Typed takes the shape from struct tags. Columns are matched by name, so the file
may reorder them or add new ones; a column a tag asks for and the file lacks is an
error. Every tagged field is a string, and convert turns the row into whatever the
program actually works with - only the caller can tell an empty cell from a zero:

	type row struct {
		ID   string `csv:"id"`
		Name string `csv:"name"`
	}

	rows, err := csvcopy.NewTyped(file, (*row).toEntity)
	if err != nil {
		return err
	}

	source, err := csvcopy.NewCopy(rows, len(columns), func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})
	if err != nil {
		return err
	}
	n, err := tx.CopyFrom(ctx, pgx.Identifier{table}, columns, source)
	if srcErr := source.Err(); srcErr != nil {
		return fmt.Errorf("read input at line %d: %w", source.Line(), srcErr)
	}
	if err != nil {
		return err
	}

NewCopy adapts any RowSource - Typed, or one of your own over XLSX or an API - to
pgx.CopyFromSource.

Both examples check the source before pgx, and that order is not stylistic. When a
row fails, pgx reports that its source stopped; the source reports what went wrong
and on which line. Reading pgx's error first throws the useful one away.

# Without a database

Reader and Typed stand on their own. Ranging stops at the first bad row and Err
reports it afterwards, the same shape as bufio.Scanner:

	rows, err := csvcopy.NewTyped(file, (*row).toEntity)
	if err != nil {
		return err
	}

	for entity := range rows.All() {
		send(entity)
	}
	if err := rows.Err(); err != nil {
		return err
	}

Reader.All yields raw records as []string, for when no decoding is wanted either.

# Guarantees

An empty input is not an error: no columns, no rows, no error. The caller decides
what that means.

Memory is bounded by the largest single record rather than by the file, and that
bound is WithMaxRecordBytes - 64 MiB by default. encoding/csv assembles a record in
one buffer and caps nothing, so a field that opens a quote and never closes it is
read to the end of the file and the whole file becomes one value. Exceeding the cap
is ErrRecordTooLarge.

A UTF-8 BOM is stripped, read with io.ReadFull so a slow reader cannot leave it in
place.

The first bad row stops the stream for good. Rows are not skipped: a partly loaded
table is worse than a failed load. The error is available from Err, names the line,
and stays there - a source that has failed yields nothing more, so ranging All
again cannot resume past the row that broke. Breaking out of a loop is not an
error, and the next pull carries on from where it stopped.

Record and Line name the row that failed, so an error message can carry it. Line
is the physical line of the file, taken from encoding/csv, so it stays right
across blank lines and quoted fields spanning several lines.

Errors are split by who can fix them, because the three answers differ. Errors the
file's content causes wrap ErrParse, so an application can alias its own sentinel
to it and quarantine the file. Errors the stream causes - a dropped connection, a
cancelled context - wrap ErrIO instead and deserve a retry, not a quarantine: the
file is fine. Errors the calling code causes wrap ErrSchema, because no file will
ever fix them.

Values reuses one slice between rows. That is safe under pgx.CopyFrom, which
encodes a row before asking for the next; a caller driving a source by hand must
not retain it.

Nothing here is safe for concurrent use, and neither is pgx.CopyFrom.
*/
package csvcopy
