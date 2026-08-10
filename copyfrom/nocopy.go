package copyfrom

/*
noCopy makes go vet's copylocks check refuse a copy of the type that embeds it.

Copy keeps one []any that every row is encoded into, so a copy of it shares that
slice with the original: two sources, one buffer, each overwriting the other's row
between the moment pgx asks for it and the moment pgx encodes it. The row counter
splits in two the same way. Nothing panics, which is the problem.

Deliberately a duplicate of decode.noCopy rather than a shared one. Importing it
would make this package depend on the CSV reader for six lines, and that
dependency is the one thing the split into two packages exists to prevent - a
RowSource over an API or an XLSX sheet must be able to reach Copy without pulling
a CSV parser behind it. If a third package ever seems like the answer, this
comment is the argument against it.

Zero-width, so it costs the struct nothing.
*/
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
