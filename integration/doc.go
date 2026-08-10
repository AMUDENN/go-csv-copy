/*
Package integration holds the tests that need a real PostgreSQL.

It is a separate module on purpose. The root module must never gain a require -
that invariant is what lets every consumer take csvcopy without inheriting
anything, and CI fails the build if go.mod grows a require block. Importing pgx to
test against a database would break it, so the dependency lives here instead, with
a replace pointing at the parent. Running go test ./... from the root does not
descend into a nested module, so the root stays clean and this stays runnable.

What it is for: everything about csvcopy that only a database can answer. Whether
WithPointerValues really is transparent to pgx, whether a short record under
WithVariableColumns lands as NULL rather than an empty string, and whether a
failure mid-file leaves the table empty. Those are claims the package makes about
what reaches Postgres, and a mock cannot check any of them.

Set POSTGRES_DSN to run them; without it every test skips, so this module is
harmless in an environment that has no database.

	POSTGRES_DSN=postgres://postgres:postgres@localhost:5432/postgres go test ./...
*/
package integration
