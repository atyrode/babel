package sharedcatalog

import (
	"context"
	"testing"
)

// Migrate must apply exactly the migrations that are embedded, in ordinal
// order, on a fresh database.
//
// The expectation is derived from the embedded set rather than hardcoded, so it
// remains an exact-set gate as migrations are added: a migration that silently
// fails to apply, or one applied out of order, fails here rather than surfacing
// later as a column a writer expects and cannot find.
func TestMigrateAppliesExactlyTheEmbeddedMigrations(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	embedded, err := migrations()
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	if len(embedded) < 2 {
		t.Fatalf("embedded migrations = %d; this gate is meaningless below two", len(embedded))
	}

	applied, err := Migrate(ctx, db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if len(applied) != len(embedded) {
		t.Fatalf("applied %v, want all %d embedded migrations", applied, len(embedded))
	}
	for i, m := range embedded {
		if applied[i] != m.version {
			t.Errorf("applied[%d] = %q, want %q", i, applied[i], m.version)
		}
	}

	// The ledger must record the same set, so a restart reapplies nothing.
	recorded, err := appliedVersions(ctx, db)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if len(recorded) != len(embedded) {
		t.Errorf("ledger holds %d versions, want %d", len(recorded), len(embedded))
	}
	for _, m := range embedded {
		if !recorded[m.version] {
			t.Errorf("ledger is missing %q", m.version)
		}
	}

	if err := Verify(ctx, db); err != nil {
		t.Fatalf("freshly migrated schema must satisfy its own allowlist: %v", err)
	}
}
