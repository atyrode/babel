package sharedcatalog_test

import (
	"context"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// A freshly migrated catalog has no deployment row until something publishes
// into it, because reconciliation writes that row rather than migration. Asking
// deployments.schema_version whether migrations are pending therefore reported a
// fully migrated database as pending, which `storage verify` printed as
// "pending migration: yes" immediately after `storage migrate` succeeded.
//
// The ledger is the source of truth for this question, and this test pins that:
// it asserts the answer at exactly the state where the two sources disagree.
func TestPendingMigrationsReadTheLedgerNotTheDeploymentRow(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	before, err := sharedcatalog.PendingMigrations(ctx, db)
	if err != nil {
		t.Fatalf("pending migrations on an empty database: %v", err)
	}
	if len(before) == 0 {
		t.Fatal("an unmigrated database must report every migration as pending")
	}

	mustMigrate(t, db)

	// The disagreement: migrated, but no deployment has published yet.
	var deployments int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM deployments`).Scan(&deployments); err != nil {
		t.Fatalf("count deployments: %v", err)
	}
	if deployments != 0 {
		t.Fatalf("this test needs the state where no deployment row exists, got %d", deployments)
	}

	after, err := sharedcatalog.PendingMigrations(ctx, db)
	if err != nil {
		t.Fatalf("pending migrations after migrating: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("a migrated catalog must report nothing pending, got %v", after)
	}

	// Ordering matters when the list is non-empty: it is what an operator
	// applies, and out-of-order application would break dependent migrations.
	for i := 1; i < len(before); i++ {
		if before[i-1] >= before[i] {
			t.Errorf("pending migrations must be in apply order, got %v", before)
			break
		}
	}
}

// An unmigrated database is where a new deployment starts, so the refusal has
// to name the command that fixes it rather than surfacing whichever query
// happened to run first. AcquireHostLease calls this before granting write
// authority, which is where an operator would otherwise meet a raw
// "relation does not exist".
func TestEnsureCompatibleGuidesAnUnmigratedDatabase(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()

	// The harness hands out a fresh database rather than a migrated one, and
	// this test means nothing if that ever changes silently.
	var present bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass('public.deployments') IS NOT NULL`).Scan(&present); err != nil {
		t.Fatalf("probe for the deployments table: %v", err)
	}
	if present {
		t.Fatal("this test requires an unmigrated database")
	}

	err := sharedcatalog.EnsureCompatible(ctx, db)
	if err == nil {
		t.Fatal("an unmigrated database must be refused")
	}
	if !strings.Contains(err.Error(), "storage migrate") {
		t.Errorf("the refusal must name the command that fixes it, got: %v", err)
	}
	if strings.Contains(err.Error(), "does not exist") {
		t.Errorf("the refusal must not surface a raw missing-relation error, got: %v", err)
	}
}
