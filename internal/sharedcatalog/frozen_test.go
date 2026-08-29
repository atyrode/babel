package sharedcatalog

import (
	"context"
	"sort"
	"testing"
)

// The Phase A catalog schema is frozen (SPEC.md §14, second gate, 2026-08-29).
// This is the enforcement, not the prose.
//
// Freezing this schema means something different from freezing the storage
// document. The document is per-machine and rewritable; these rows are shared,
// and from Phase B onward they hold analysis output that is durable and
// irreplaceable. Today the catalog is rebuildable convenience state
// (decision 43), which is exactly why now is the cheap moment to fix its shape:
// a migration written against a frozen base can be reasoned about, and one
// written against a drifting base cannot.
//
// A frozen schema still changes - by adding a migration, never by editing one
// that has run. An applied migration is history: rewriting it would leave every
// deployment that already ran it holding a shape nothing describes.
func TestFrozenMigrationLedger(t *testing.T) {
	// Exactly the migrations that have run against the real managed PostgreSQL.
	frozen := []string{"0001_init", "0002_unknown_counts"}

	entries, err := migrations()
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	var got []string
	for _, m := range entries {
		got = append(got, m.version)
	}
	if len(got) < len(frozen) {
		t.Fatalf("migrations were removed.\n  frozen: %v\n  now:    %v\n"+
			"An applied migration is history: deployments that ran it would hold "+
			"a shape nothing describes.", frozen, got)
	}
	for i, want := range frozen {
		if got[i] != want {
			t.Fatalf("frozen migration %d changed identity: %q, want %q.\n"+
				"Add a migration; never rename or reorder one that has run.",
				i+1, got[i], want)
		}
	}
	if len(got) > len(frozen) {
		t.Logf("note: %d migration(s) added since the freeze: %v",
			len(got)-len(frozen), got[len(frozen):])
	}
}

// SchemaVersion is what a deployment records and what EnsureCompatible refuses
// a newer value of. It is the compatibility announcement, so it must not move
// without one.
func TestFrozenSchemaVersion(t *testing.T) {
	if SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d, frozen at 1 (SPEC.md §14). Raising it "+
			"tells every older instance to stop writing, which is a decision "+
			"rather than a bump.", SchemaVersion)
	}
}

// The plaintext allowlist is the privacy boundary: it is what makes "no
// transcript, title, or path reaches PostgreSQL" checkable rather than
// aspirational. Freezing the schema freezes this too, because a column that
// appears without being listed is precisely what Verify exists to catch.
func TestFrozenAllowlistShape(t *testing.T) {
	db := newInternalDB(t)
	if err := Verify(context.Background(), db); err != nil {
		t.Fatalf("the migrated schema no longer matches the allowlist: %v", err)
	}

	// Non-vacuity: the allowlist must actually describe the tables that exist,
	// not an empty set that trivially passes.
	tables := map[string]bool{}
	for table := range allowlist {
		tables[table] = true
	}
	want := []string{
		"deployments", "hosts", "host_leases", "idempotency_keys",
		"instances", "schema_migrations", "sessions", "snapshots",
	}
	var got []string
	for table := range tables {
		got = append(got, table)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("the allowlist covers %d tables, frozen at %d.\n  frozen: %v\n  now:    %v",
			len(got), len(want), want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allowlist tables changed.\n  frozen: %v\n  now:    %v", want, got)
		}
	}
}
