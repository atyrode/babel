package sharedcatalog_test

import (
	"context"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// This test pins a known weakness rather than a guarantee, and it is here so
// the weakness cannot be quietly misdescribed.
//
// Revocation is DML on instances, and every instance's credential holds DML on
// that table by design: an instance registers itself and refreshes
// last_seen_at. So the least-privileged credential Babel issues can revoke any
// instance id, including instances that are perfectly healthy. Revocation
// expresses an operator's decision; nothing authenticates one.
//
// If someone later makes this database-enforced - column-level grants, with
// UPDATE (last_seen_at) to application roles and revoked_at reserved to an
// operator role - this test fails, which is the point: the actor model
// documented in revoke.go, storage's --help, SPEC.md 9, and the changelog all
// have to change with it.
func TestLeastPrivilegedCredentialCanRevokeAnyInstance(t *testing.T) {
	db := newDB(t)
	mustMigrate(t, db)
	ctx := context.Background()

	const role, password = "babel_instance_actor", "actor-instance-secret"
	if err := sharedcatalog.EnsureAppRole(ctx, db, role, password); err != nil {
		t.Fatalf("EnsureAppRole: %v", err)
	}

	if _, err := db.ExecContext(ctx,
		`INSERT INTO deployments (deployment_id, schema_version) VALUES ('d1', $1)`,
		sharedcatalog.SchemaVersion); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	// Two instances: the one holding the credential, and an unrelated victim.
	for _, id := range []string{"actor", "victim"} {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO instances (instance_id, deployment_id) VALUES ($1, 'd1')`, id); err != nil {
			t.Fatalf("seed instance %s: %v", id, err)
		}
	}

	app := openAs(t, db, role, password)

	// An application credential revokes an instance that is not itself.
	if err := sharedcatalog.RevokeInstance(ctx, app, "victim"); err != nil {
		t.Fatalf("the documented actor model says this succeeds; it failed: %v", err)
	}
	revoked, err := sharedcatalog.InstanceRevoked(ctx, db, "victim")
	if err != nil {
		t.Fatalf("read victim state: %v", err)
	}
	if !revoked {
		t.Error("the victim must be revoked, or the actor model is documented wrongly")
	}

	// And clears a revocation, which is the other half of the limit: whoever
	// holds a credential can undo their own eviction.
	if err := sharedcatalog.RevokeInstance(ctx, app, "actor"); err != nil {
		t.Fatalf("revoke self: %v", err)
	}
	if _, err := app.ExecContext(ctx,
		`UPDATE instances SET revoked_at = NULL WHERE instance_id = 'actor'`); err != nil {
		t.Fatalf("the actor model says a credential holder can clear its own revocation; it could not: %v", err)
	}
	stillRevoked, err := sharedcatalog.InstanceRevoked(ctx, db, "actor")
	if err != nil {
		t.Fatalf("read actor state: %v", err)
	}
	if stillRevoked {
		t.Error("clearing revoked_at must work, or the documented limit overstates the risk")
	}
}
