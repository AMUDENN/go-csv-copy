package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	csvcopy "github.com/AMUDENN/go-csv-copy"
	"github.com/AMUDENN/go-csv-copy/copyfrom"
	"github.com/AMUDENN/go-csv-copy/decode"
	"github.com/jackc/pgx/v5"
)

/*
connect opens a connection, or skips the test.

Skipping rather than failing is what keeps this module runnable in a checkout with
no database: the point is to be able to answer these questions, not to force
everyone to.
*/
func connect(t *testing.T) *pgx.Conn {
	t.Helper()

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_DSN is not set")
	}

	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Logf("close: %v", err)
		}
	})

	return conn
}

/*
begin starts a transaction that is always rolled back.

Nothing these tests do outlives them, which matters because the database they run
against may well be somebody's real one. Combined with a temporary table, the
visible footprint is nil.
*/
func begin(t *testing.T, conn *pgx.Conn) pgx.Tx {
	t.Helper()

	ctx := context.Background()

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() {
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Logf("rollback: %v", err)
		}
	})

	return tx
}

// createTextTable makes a temporary all-text table, the staging-table shape Raw is
// built for.
func createTextTable(t *testing.T, tx pgx.Tx, name string, columns []string) {
	t.Helper()

	quoted := make([]string, len(columns))
	for i, column := range columns {
		quoted[i] = pgx.Identifier{column}.Sanitize() + " TEXT"
	}

	ddl := fmt.Sprintf("CREATE TEMP TABLE %s (%s) ON COMMIT DROP",
		pgx.Identifier{name}.Sanitize(), strings.Join(quoted, ", "))

	if _, err := tx.Exec(context.Background(), ddl); err != nil {
		t.Fatalf("create table: %v", err)
	}
}

const sampleFile = "id;name;note\n" +
	"1;Alice;first\n" +
	"2;Bob;\n" +
	"3;Carol;third\n"

/*
The question WithPointerValues was written for and never answered.

The option hands pgx *string instead of string, which removes one allocation per
cell - measured, and on a wide file worth 31x. It has stayed off by default only
because "pgx dereferences *T through its pointer encode plan" was reasoning, not a
result. If the two loads are identical, the reasoning holds.
*/
func TestPointerValuesLoadIdentically(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	/*
		Each load owns its transaction and ends it before returning.

		Deferring the rollback to t.Cleanup instead would leave the first
		transaction open across the second call, and pgx would run the second load
		inside it - where the temporary table already exists.
	*/
	digest := func(pointers bool) string {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() {
			if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Logf("rollback: %v", err)
			}
		}()

		src, err := decode.NewRaw(strings.NewReader(sampleFile), decode.WithPointerValues(pointers))
		if err != nil {
			t.Fatalf("NewRaw: %v", err)
		}

		createTextTable(t, tx, "pointer_values", src.Columns())

		n, err := tx.CopyFrom(ctx, pgx.Identifier{"pointer_values"}, src.Columns(), src)
		if srcErr := src.Err(); srcErr != nil {
			t.Fatalf("source failed at line %d: %v", src.Line(), srcErr)
		}
		if err != nil {
			t.Fatalf("copy from (pointers=%v): %v", pointers, err)
		}
		if n != 3 {
			t.Fatalf("copied %d rows, want 3", n)
		}

		// md5 over the ordered rows: one value that changes if any cell does,
		// including a NULL turning into an empty string.
		var sum string
		err = tx.QueryRow(ctx, `
			SELECT md5(string_agg(t::text, '|' ORDER BY id))
			FROM pointer_values t
		`).Scan(&sum)
		if err != nil {
			t.Fatalf("digest: %v", err)
		}

		return sum
	}

	withPointers := digest(true)
	withStrings := digest(false)

	if withPointers != withStrings {
		t.Errorf("pointer values changed what was loaded:\n *string: %s\n  string: %s",
			withPointers, withStrings)
	}
}

/*
The other half of the pointer question: a typed destination.

The digest test above loads into an all-TEXT staging table, which is what Raw is
built for - but nothing stops a caller pointing it at a real schema, and there pgx
has to turn a *string into a bigint, a numeric and a date. Whether it will is the
thing that decides if this option can be the default rather than an opt-in.
*/
func TestPointerValuesIntoTypedColumns(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()

	load := func(pointers bool) error {
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() {
			if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Logf("rollback: %v", err)
			}
		}()

		_, err = tx.Exec(ctx, `
			CREATE TEMP TABLE pointer_typed (
				id BIGINT PRIMARY KEY,
				balance NUMERIC(12,2) NOT NULL,
				joined DATE NOT NULL
			) ON COMMIT DROP
		`)
		if err != nil {
			t.Fatalf("create table: %v", err)
		}

		const file = "id;balance;joined\n1;10.50;2024-01-31\n2;0.00;2024-02-01\n"

		src, err := decode.NewRaw(strings.NewReader(file), decode.WithPointerValues(pointers))
		if err != nil {
			t.Fatalf("NewRaw: %v", err)
		}

		if _, err = tx.CopyFrom(ctx, pgx.Identifier{"pointer_typed"}, src.Columns(), src); err != nil {
			return err
		}
		if srcErr := src.Err(); srcErr != nil {
			t.Fatalf("source: %v", srcErr)
		}

		var total float64
		if err = tx.QueryRow(ctx, `SELECT sum(balance) FROM pointer_typed`).Scan(&total); err != nil {
			t.Fatalf("query: %v", err)
		}
		if total != 10.5 {
			t.Errorf("pointers=%v: sum(balance) = %v, want 10.5", pointers, total)
		}

		return nil
	}

	if err := load(false); err != nil {
		t.Fatalf("plain strings into typed columns failed: %v", err)
	}

	// Reported rather than asserted: if pgx will not do this, the option stays
	// opt-in and the godoc says so, which is a result either way.
	if err := load(true); err != nil {
		t.Errorf("pointer values into typed columns failed: %v", err)
	}
}

/*
A short record has to arrive as NULL, not as an empty string.

This is the guarantee Raw's padding exists for, and in Postgres the two are
different values. It cannot be checked anywhere but here: at the Go level both are
just a slot in an []any.
*/
func TestShortRecordLandsAsNull(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	tx := begin(t, conn)

	// Row 2 stops after two fields; row 3 has an explicit empty third field.
	const file = "id;name;note\n1;Alice;first\n2;Bob\n3;Carol;\n"

	src, err := decode.NewRaw(strings.NewReader(file), decode.WithVariableColumns(true))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	createTextTable(t, tx, "short_record", src.Columns())

	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"short_record"}, src.Columns(), src); err != nil {
		t.Fatalf("copy from: %v", err)
	}
	if srcErr := src.Err(); srcErr != nil {
		t.Fatalf("source: %v", srcErr)
	}

	var absent, empty bool
	err = tx.QueryRow(ctx, `
		SELECT
			(SELECT note IS NULL FROM short_record WHERE id = '2'),
			(SELECT note = '' FROM short_record WHERE id = '3')
	`).Scan(&absent, &empty)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if !absent {
		t.Error("a value the record never reached is not NULL")
	}
	if !empty {
		t.Error("an explicitly empty value is not the empty string")
	}
}

type clientRow struct {
	ID      string `csv:"id"`
	Balance string `csv:"balance"`
	Joined  string `csv:"joined"`
}

type client struct {
	id      int64
	balance float64
	joined  string
}

// Typed and Copy into a table with real column types, which is the whole reason
// convert exists: the package will not type a value, and the caller must.
func TestTypedCopyIntoTypedColumns(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	tx := begin(t, conn)

	_, err := tx.Exec(ctx, `
		CREATE TEMP TABLE typed_clients (
			id BIGINT PRIMARY KEY,
			balance NUMERIC(12,2) NOT NULL,
			joined DATE NOT NULL
		) ON COMMIT DROP
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	const file = "id;balance;joined\n1;10.50;2024-01-31\n2;0.00;2024-02-01\n"

	rows, err := decode.NewTyped(strings.NewReader(file), (*clientRow).toClient)
	if err != nil {
		t.Fatalf("NewTyped: %v", err)
	}

	columns := []string{"id", "balance", "joined"}

	source, err := copyfrom.NewCopy(rows, len(columns), func(dst []any, c *client) []any {
		return append(dst, c.id, c.balance, c.joined)
	})
	if err != nil {
		t.Fatalf("NewCopy: %v", err)
	}

	n, err := tx.CopyFrom(ctx, pgx.Identifier{"typed_clients"}, columns, source)
	if srcErr := source.Err(); srcErr != nil {
		t.Fatalf("source failed at line %d: %v", source.Line(), srcErr)
	}
	if err != nil {
		t.Fatalf("copy from: %v", err)
	}
	if n != 2 {
		t.Fatalf("copied %d rows, want 2", n)
	}

	var total float64
	if err = tx.QueryRow(ctx, `SELECT sum(balance) FROM typed_clients`).Scan(&total); err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 10.5 {
		t.Errorf("sum(balance) = %v, want 10.5", total)
	}
}

func (r *clientRow) toClient() (*client, error) {
	var c client

	if _, err := fmt.Sscanf(r.ID, "%d", &c.id); err != nil {
		return nil, fmt.Errorf("id %q: %w", r.ID, err)
	}
	if _, err := fmt.Sscanf(r.Balance, "%f", &c.balance); err != nil {
		return nil, fmt.Errorf("balance %q: %w", r.Balance, err)
	}
	c.joined = r.Joined

	return &c, nil
}

/*
A failure part way through has to leave nothing behind.

pgx aborts the COPY and the transaction rolls back, so the table must be empty
rather than holding the rows that made it through - and the error the caller reports
has to be the source's, which names the line, not pgx's, which does not.
*/
func TestFailureMidFileLeavesNothing(t *testing.T) {
	conn := connect(t)
	ctx := context.Background()
	tx := begin(t, conn)

	// Row 3 is short, which is an error without WithVariableColumns.
	const file = "id;name;note\n1;Alice;first\n2;Bob;second\n3;Carol\n4;Dave;fourth\n"

	src, err := decode.NewRaw(strings.NewReader(file))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}

	createTextTable(t, tx, "mid_failure", src.Columns())

	_, copyErr := tx.CopyFrom(ctx, pgx.Identifier{"mid_failure"}, src.Columns(), src)
	if copyErr == nil {
		t.Fatal("CopyFrom succeeded on a file with a short record")
	}

	srcErr := src.Err()
	if !errors.Is(srcErr, csvcopy.ErrParse) {
		t.Fatalf("Err() = %v, want an error wrapping ErrParse", srcErr)
	}
	if !strings.Contains(srcErr.Error(), "line 4") {
		t.Errorf("Err() = %q, does not name line 4", srcErr)
	}
	if got := src.Line(); got != 4 {
		t.Errorf("Line() = %d, want 4", got)
	}

	// The transaction is aborted, so counting has to happen in a fresh one.
	if err = tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var count int64
	err = conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_tables WHERE tablename = 'mid_failure'
	`).Scan(&count)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 0 {
		t.Errorf("the table outlived the rolled-back transaction (%d rows in pg_tables)", count)
	}
}

// An empty file means there is nothing to do, and the caller can see that before
// touching the database at all.
func TestEmptyFileNeverReachesTheDatabase(t *testing.T) {
	conn := connect(t)
	tx := begin(t, conn)

	src, err := decode.NewRaw(strings.NewReader(""))
	if err != nil {
		t.Fatalf("NewRaw: %v", err)
	}
	if len(src.Columns()) != 0 {
		t.Fatalf("Columns() = %q, want none", src.Columns())
	}
	if src.Err() != nil {
		t.Errorf("Err() = %v, want nil", src.Err())
	}

	// Nothing to create and nothing to copy: the caller returns before pgx is
	// involved. Asserted by the fact that this transaction stays usable.
	var one int
	if err = tx.QueryRow(context.Background(), `SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("the transaction was disturbed: %v", err)
	}
}
