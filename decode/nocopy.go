package decode

/*
noCopy makes go vet's copylocks check refuse a copy of the type that embeds it.

Typed resolves the addresses of its own struct fields once and writes through them
for the rest of the file, so a copy decodes into the original's row while convert
reads the copy's - every field empty, on every row, with no error anywhere. That is
the one failure shape this package is written against, and it is the one the
compiler cannot see: there is no lock here to protect, and vet's check is simply
the only mechanism that reports a copy at all.

Zero-width, so it costs the struct nothing.
*/
type noCopy struct{}

func (*noCopy) Lock()   {}
func (*noCopy) Unlock() {}
