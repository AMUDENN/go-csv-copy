/*
Package copyfrom turns a stream of values into a pgx.CopyFrom source.

It knows nothing about CSV. A RowSource is anything that yields values one at a
time - decode.Typed over a file, a reader over an XLSX sheet, a paged API client, a
generator - and Copy maps each value onto the columns a COPY was given:

	source, err := copyfrom.NewCopy(rows, len(columns), func(dst []any, e *entity) []any {
		return append(dst, e.ID, e.Name)
	})
	if err != nil {
		return err
	}

	_, err = tx.CopyFrom(ctx, pgx.Identifier{table}, columns, source)
	// The source's error first: it names the line, pgx's does not.
	if srcErr := source.Err(); srcErr != nil {
		return fmt.Errorf("read input at line %d: %w", source.Line(), srcErr)
	}
	if err != nil {
		return err
	}

That is the whole point of the package boundary. The layer that owns the database
imports this one and never mentions the file format; the layer that parses imports
decode and never mentions the database. Neither package imports the other, so the
separation holds whether or not anyone remembers it.

pgx is not imported either. Go interfaces are structural, so the method set
Next/Values/Err is the entire contract, and the version of pgx stays the
application's choice.

ValidateColumns is here for the same reason: a staging table built from a file's
own header puts untrusted names on the path to CREATE TABLE, and deciding what an
identifier may look like is a database question, not a parsing one.

Errors wrap csvcopy.ErrSchema for wiring the calling code got wrong, and
csvcopy.ErrParse for a header that cannot be used. See the csvcopy package doc.
*/
package copyfrom
