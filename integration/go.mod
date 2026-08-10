module github.com/AMUDENN/go-csv-copy/integration

go 1.25.0

// Always the checkout, never a published version: these tests exist to check the
// code next to them.
replace github.com/AMUDENN/go-csv-copy => ../

require (
	github.com/AMUDENN/go-csv-copy v0.0.0-00010101000000-000000000000
	github.com/jackc/pgx/v5 v5.10.0
)

require (
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	golang.org/x/text v0.29.0 // indirect
)
