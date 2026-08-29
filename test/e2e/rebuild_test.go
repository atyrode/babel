package e2e_test

import (
	"context"
	"strings"
	"testing"
)

// `babel storage rebuild` exposes the catalog rebuild SPEC.md §12 lists as a
// Phase A deliverable. The library had it, tested, with no command able to
// invoke it — a deliverable nothing can reach is not delivered.
//
// It is the repair path rather than the recovery path: an empty catalog is fixed
// by `storage migrate` plus each host's next push, which TestCatalogRebuilds-
// FromTheRepository covers. This covers what that cannot — rows for a host that
// are present but wrong, which no push corrects, because a push adds its own
// snapshot rather than auditing the ones already recorded.
func TestStorageRebuildRepairsWrongCatalogRows(t *testing.T) {
	dep := newDeployment(t)
	a := dep.newInstance(t, "instance-a", hostA, instanceA)
	b := dep.newInstance(t, "instance-b", hostB, instanceB)

	a.writeOMPSession(t, ompSpec{
		project: ompProjectB, stem: ompStemB,
		id: "00000000-0000-4000-8000-0000000000b1", title: titleB, workspace: workspaceB,
	})
	b.writeOMPSession(t, ompSpec{
		project: "-synthetic-e2e-repair", stem: "2026-05-06T07-08-09-123Z_eeeeeeee-0000-4000-8000-0000000000e1",
		id: "00000000-0000-4000-8000-0000000000e1", title: "Synthetic e2e repair", workspace: "/synthetic/workspace/repair",
	})

	a.configure(t)
	a.ok(t, "storage", "migrate")
	a.ok(t, a.with("archive", "init")...)
	b.configure(t)

	instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	instJSON[pushResult](t, b, b.with("archive", "push", "--json")...)

	before := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	beforeA, beforeB := before.catalogHost(t, hostA), before.catalogHost(t, hostB)
	if beforeA.Snapshots != 2 || beforeB.Snapshots != 1 {
		t.Fatalf("setup is wrong: host A %+v, host B %+v", beforeA, beforeB)
	}

	// Corrupt host A's rows in a way no push repairs: a real snapshot recorded
	// with the wrong publication order. A push appends; it never re-examines what
	// is already there, which is exactly the gap this command fills.
	db := dep.open(t)
	res, err := db.ExecContext(context.Background(),
		`UPDATE snapshots SET publication_order = 99 WHERE host_id = $1 AND publication_order = 1`, hostA)
	if err != nil {
		t.Fatalf("corrupt the catalog: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		t.Fatalf("expected to corrupt one row, changed %d", n)
	}
	corrupted := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if got := corrupted.catalogHost(t, hostA); got.NewestOrder != 99 {
		t.Fatalf("the corruption did not take: %+v", got)
	}

	// --host and --yes are both required, because the command discards rows.
	if _, stderr, code := a.run(t, a.with("storage", "rebuild", "--yes")...); code == exitOK {
		t.Fatal("rebuild ran without --host")
	} else if !strings.Contains(stderr, "--host") {
		t.Fatalf("the refusal does not name --host: %s", stderr)
	}
	if _, stderr, code := a.run(t, a.with("storage", "rebuild", "--host", hostA)...); code == exitOK {
		t.Fatal("rebuild ran without --yes")
	} else if !strings.Contains(stderr, "--yes") {
		t.Fatalf("the refusal does not name --yes: %s", stderr)
	}
	// A mistyped host must not rebuild anything to empty.
	if _, stderr, code := a.run(t,
		a.with("storage", "rebuild", "--host", "no-such-host", "--yes")...); code == exitOK {
		t.Fatal("rebuild accepted an unknown host")
	} else if !strings.Contains(stderr, hostA) {
		t.Fatalf("the refusal does not name the hosts that exist: %s", stderr)
	}

	rebuilt := instJSON[rebuildResultDoc](t, a,
		a.with("storage", "rebuild", "--host", hostA, "--yes", "--json")...)
	if rebuilt.Rebuilt != 2 {
		t.Fatalf("rebuild reported %d snapshots, want host A's two: %+v", rebuilt.Rebuilt, rebuilt)
	}

	after := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	afterA := after.catalogHost(t, hostA)
	// Ordering is rederived from restic's recorded times, so the fabricated 99 is
	// gone and both snapshots are still present.
	if afterA.Snapshots != 2 || afterA.NewestOrder != 2 {
		t.Fatalf("host A was not repaired: %+v", afterA)
	}
	// Session rows are not derivable from a listing, so they are discarded rather
	// than invented, and the snapshots come back catalog-pending.
	if afterA.Sessions != 0 || afterA.Pending != 2 {
		t.Fatalf("rebuild invented session detail it could not have read: %+v", afterA)
	}
	// Scoped to one host: B's rows are untouched.
	if got := after.catalogHost(t, hostB); got != beforeB {
		t.Fatalf("rebuilding host A changed host B:\nbefore: %+v\nafter:  %+v", beforeB, got)
	}
	// And no repository snapshot lost its row.
	if after.Catalog.Uncatalogued == nil || *after.Catalog.Uncatalogued != 0 {
		t.Fatalf("rebuild left snapshots uncatalogued: %+v", after.Catalog)
	}

	// The owning host's next push restores its session identity, which is the
	// documented completion rather than something the rebuild claimed to do.
	instJSON[pushResult](t, a, a.with("archive", "push", "--json")...)
	settled := instJSON[sharedStatusResult](t, a, a.with("archive", "status", "--json")...)
	if got := settled.catalogHost(t, hostA); got.Sessions != 1 {
		t.Fatalf("host A's next push did not restore session identity: %+v", got)
	}
}

// rebuildResultDoc mirrors `storage rebuild --json`.
type rebuildResultDoc struct {
	Host     string `json:"host"`
	Rebuilt  int    `json:"rebuilt"`
	Sessions int    `json:"sessions"`
}
