package sharedcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		// Phase B analysis output, and the citation graph over it
		// (migrations/0008).
		"analysis_edges", "analysis_records", "analysis_runs",
		// Fleet presence (migrations/0009). It is neither archive metadata
		// nor analysis output but ephemeral status, and it is the one table
		// here that admits UPDATE - narrowed by its own trigger to the four
		// liveness columns. Nothing was widened to make room for it: every
		// column is an identifier, a closed vocabulary or a timestamp.
		"presence",
		// A proposal's provenance (migrations/0010, issue #114). It is the
		// second Phase B table whose columns say something structural about a
		// record rather than only identifying it, and it was added for the
		// reason the first was: relationship IDs are what SPEC.md 9 admits, and
		// a fleet host with no payload key that cannot tell a want from a
		// finding-backed conclusion renders the first with the authority of the
		// second. No content column came with it, and the Phase B class gate is
		// what keeps one from arriving later.
		"analysis_proposal_subjects",
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
		"0004_fleet_metadata",
		"0005_title_provenance",
		"0006_usage_metadata",
		"0007_instance_host",
		"0008_reference_edges",
		"0009_fleet_presence",
		"0010_proposal_subjects",
		"0011_complaint_records",
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

// Both ledger gates above pin migrations by NAME. A name is not a migration.
// Editing migrations/0001_init.sql in place, under its existing filename,
// leaves the ledger identical and satisfies every assertion in this file -
// while changing the text that already ran against the managed PostgreSQL.
// That is the precise failure the freeze exists to prevent (SPEC.md §14): an
// applied migration is history, and rewriting it would leave every deployment
// that ran the old text holding a shape nothing describes.
//
// So this hashes the bodies. The digests are SHA-256 over each migration's
// exact embedded bytes, which are the file's exact bytes, so any of them can
// be recomputed with `sha256sum internal/sharedcatalog/migrations/<file>.sql`.
func TestAppliedMigrationBodiesAreFrozen(t *testing.T) {
	// Frozen 2026-08-30. Every migration in the ledger is pinned; an entry is
	// added here only when its migration is added, never when its text moves.
	frozen := map[string]string{
		"0001_init":              "fa5cca80104ea0d2216293492593f55e45371229927391cade1d17199b35ae57",
		"0002_unknown_counts":    "655ce49ea51d1eb371bcd54835455d6b0f6ae8d6de9a2eb9d126a39b4a138f85",
		"0003_phase_b_records":   "6d25e2945cacd27369ee8085b12fb69264b8feecff9476367c9b3d49acc1c0e3",
		"0004_fleet_metadata":    "d32ea349fba87e00af31237e0f9d523e4167d9d99ba894e3ba53b03bfdcf0fe8",
		"0005_title_provenance":  "16252f11ec1e69ce0eda55f247b7199320f1b36ecd7c790e4e929b99587b36cf",
		"0006_usage_metadata":    "d44410ff6885b93b2a964eee95a838a64e9e6a8ba6d3af79c6a51f845b68f66d",
		"0007_instance_host":     "613485056617e5e7b0a2a8210e4699ec3b7e281f779190e1ad4758d58853da64",
		"0008_reference_edges":   "f99bdd8f847118c30693fa7c3d5f8e66a4025ce61fb4ab3114276e9ccd24d6ba",
		"0009_fleet_presence":    "dea82160aaeebb5bb38dae80395b2129850339633317f750948b032d97a3b562",
		"0010_proposal_subjects": "0db9e9a935b0eaa776fb94ece018ba048c8378ca5d4a1c10bea4ac8af2b44f3e",
		// Added 2026-08-31 with the migration itself (issue #115), so the
		// freeze covers it from the commit that introduced it rather than
		// from whenever somebody next remembered.
		"0011_complaint_records": "5c24692e21a7aca9e296c8144afc5031f6863d9691de6ad339f718de745e96a4",
	}

	const remedy = "Two changes are legitimate here, and they are not the same " +
		"one. If the schema needs to differ, add a NEW migration: deployments " +
		"that ran the old text stay described. If the edit was intentional and " +
		"no deployment has run this migration, update the pinned digest in the " +
		"same commit and say so in the message. Silently refreshing the constant " +
		"to make the test green is the third option, and it is the one this " +
		"guard exists to refuse."

	entries, err := migrations()
	if err != nil {
		t.Fatalf("read embedded migrations: %v", err)
	}

	seen := map[string]bool{}
	for _, m := range entries {
		sum := sha256.Sum256([]byte(m.body))
		got := hex.EncodeToString(sum[:])
		want, pinned := frozen[m.version]
		if !pinned {
			t.Errorf("migration %q has no pinned body digest.\n"+
				"Add it to the frozen map in the same commit that adds the "+
				"migration, or the freeze quietly stops covering it:\n"+
				"  %q: %q,", m.version, m.version, got)
			continue
		}
		seen[m.version] = true
		if got != want {
			t.Errorf("migration %q was edited in place.\n  frozen: %s\n  now:    %s\n%s",
				m.version, want, got, remedy)
		}
	}

	// A pinned migration that stopped existing is the same loss of history as
	// an edited one, and it must not read as "nothing to check".
	for version := range frozen {
		if !seen[version] {
			t.Errorf("migration %q is pinned here but is no longer embedded.\n%s",
				version, remedy)
		}
	}
}
