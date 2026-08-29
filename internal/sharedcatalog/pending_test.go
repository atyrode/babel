package sharedcatalog_test

import (
	"context"
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
