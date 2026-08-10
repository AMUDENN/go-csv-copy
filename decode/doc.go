/*
Package decode reads CSV one row at a time and turns it into your values.

A row is read, handed over and forgotten, so memory does not depend on the size of
the file. Nothing here knows about a database: the sources satisfy
pgx.CopyFromSource by having the right method set, and pgx is not imported. Laying
a value out across the columns of a COPY is copyfrom's job, and this package does
not know that package exists.

Three layers, from the file up:

	Reader  records as []string, straight from encoding/csv.
	Raw     one value per column of the file's own header, all text.
	Typed   a struct fixed by csv tags, converted by a func you supply.

Raw is the staging-table case: whatever columns arrived, in their order, typed
later by a SQL script. Typed is the case where the program knows what a row means -
columns are matched by name, so an export may reorder them or add new ones, and a
column a tag asks for and the file lacks is an error rather than a silent NULL.

Every tagged field is a string on purpose. Only the caller can tell an empty cell
from a zero value, or decide whether a bad one fails the file or the field, so this
package converts nothing itself.

All three have All for ranging, and stop at the first bad row with the reason in
Err - the same shape as bufio.Scanner:

	rows, err := decode.NewTyped(file, (*row).toEntity)
	if err != nil {
		return err
	}

	for entity := range rows.All() {
		send(entity)
	}
	if err := rows.Err(); err != nil {
		return err
	}

Errors wrap csvcopy.ErrParse, csvcopy.ErrIO or csvcopy.ErrSchema, by who can fix
them. See the csvcopy package doc for the guarantees this one is held to.
*/
package decode
