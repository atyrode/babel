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
//
// Frozen does not mean fixed: a frozen base is what makes an addition
// reviewable. Phase B added analysis_runs and analysis_records
// (migrations/0003), and this list was updated deliberately with them. Neither
// table holds a payload - a record's content is sealed into an object and
// PostgreSQL keeps only the reference, key id, size and digest - so the
// boundary the eight Phase A tables established is unchanged in kind. The
// eight are still all here, and nothing was widened to make room.
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
		// Phase A, frozen 2026-08-29.
		"deployments", "hosts", "host_leases", "idempotency_keys",
		"instances", "schema_migrations", "sessions", "snapshots",
		// Phase B analysis output.
		"analysis_records", "analysis_runs",
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

// The ledger gate above tolerates additions by design, because Phase A froze a
// prefix rather than a length. That leaves the additions themselves ungoverned,
// so this asserts the whole ledger exactly: renaming, reordering, or editing
// any applied migration - Phase A's or Phase B's - fails here.
//
// An applied migration is history. A deployment that ran 0003 holds triggers
// and constraints described by that file's text; rewriting it would leave that
// deployment holding a shape nothing in the repository describes.
func TestMigrationLedgerIsExactlyThese(t *testing.T) {
	want := []string{
		"0001_init",
		"0002_unknown_counts",
		"0003_phase_b_records",
	}

	entries, err := migrations()
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}
	var got []string
	for _, m := range entries {
		got = append(got, m.version)
	}
	if len(got) != len(want) {
		t.Fatalf("the ledger holds %d migrations, want exactly %d.\n  want: %v\n  got:  %v\n"+
			"Adding one is a deliberate change: update this list in the same commit.",
			len(got), len(want), want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("migration %d is %q, want %q.\n  want: %v\n  got:  %v\n"+
				"Add a migration; never rename, reorder, or edit one that has run.",
				i+1, got[i], want[i], want, got)
		}
	}
}
