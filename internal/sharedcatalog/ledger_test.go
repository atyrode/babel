package sharedcatalog_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The migration ledger is created by the runner rather than by 0001, because
// the runner must be able to read it before deciding what to apply. That makes
// its existence a property of Migrate, not of the SQL, so it is asserted here
// against the live schema rather than inferred from the migration text.
func TestMigrationLedgerIsCreatedByTheRunner(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// Before migrating, the table must not exist: otherwise this test could
	// pass because something else created it.
	var existsBefore bool
	if err := db.QueryRowContext(ctx,
		`SELECT exists(SELECT 1 FROM information_schema.tables
		  WHERE table_schema = current_schema() AND table_name = 'schema_migrations')`).
		Scan(&existsBefore); err != nil {
		t.Fatalf("probe ledger: %v", err)
	}
	if existsBefore {
		t.Fatal("schema_migrations existed before Migrate ran")
	}

	mustMigrate(t, db)

	// PostgreSQL's own view of the table, not the SQL text.
	rows, err := db.QueryContext(ctx,
		`SELECT column_name, is_nullable FROM information_schema.columns
		 WHERE table_schema = current_schema() AND table_name = 'schema_migrations'
		 ORDER BY column_name`)
	if err != nil {
		t.Fatalf("read ledger columns: %v", err)
	}
	defer rows.Close()

	got := map[string]string{}
	for rows.Next() {
		var name, nullable string
		if err := rows.Scan(&name, &nullable); err != nil {
			t.Fatalf("scan ledger column: %v", err)
		}
		got[name] = nullable
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read ledger columns: %v", err)
	}

	for _, want := range []string{"version", "applied_at"} {
		if _, ok := got[want]; !ok {
			t.Errorf("ledger is missing column %q; live columns: %v", want, got)
		}
		if got[want] == "YES" {
			t.Errorf("ledger column %q must be NOT NULL", want)
		}
	}
	if len(got) != 2 {
		t.Errorf("ledger has %d columns, want exactly version and applied_at: %v", len(got), got)
	}
}

// The recorded version must survive the process that wrote it, otherwise a
// restarted instance would reapply migrations.
func TestMigrationLedgerPersistsAcrossConnections(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	applied := mustMigrate(t, db)
	if len(applied) == 0 {
		t.Fatal("nothing applied")
	}

	var database string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		t.Fatalf("read database name: %v", err)
	}
	db.Close()

	// A completely separate connection, as a restarted instance would make.
	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse base DSN: %v", err)
	}
	u.Path = "/" + database
	fresh, err := sharedcatalog.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	defer fresh.Close()

	var version string
	var hasTime bool
	if err := fresh.QueryRowContext(ctx,
		`SELECT version, applied_at IS NOT NULL FROM schema_migrations ORDER BY version`).
		Scan(&version, &hasTime); err != nil {
		t.Fatalf("read persisted ledger: %v", err)
	}
	if version != applied[0] {
		t.Errorf("persisted version = %q, want %q", version, applied[0])
	}
	if !hasTime {
		t.Error("persisted ledger row has no applied_at")
	}

	// And a second Migrate on the fresh connection must apply nothing, which is
	// the behaviour the persisted ledger exists to produce.
	again, err := sharedcatalog.Migrate(ctx, fresh)
	if err != nil {
		t.Fatalf("migrate on fresh connection: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("reapplied %v despite a persisted ledger", again)
	}
}

// The ledger is inside the enforced contract, not beside it: losing it is a
// schema discrepancy Verify must report.
func TestVerifyRejectsAMissingLedger(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)

	if _, err := db.Exec(`DROP TABLE schema_migrations`); err != nil {
		t.Fatalf("drop ledger: %v", err)
	}
	err := sharedcatalog.Verify(context.Background(), db)
	if err == nil {
		t.Fatal("Verify accepted a schema with no migration ledger")
	}
	if !strings.Contains(err.Error(), "schema_migrations") {
		t.Fatalf("error must name the missing ledger, got: %v", err)
	}
}
