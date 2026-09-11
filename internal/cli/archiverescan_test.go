package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/restic"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// This file is the acceptance for the two archive drains SPEC.md §9.1 requires:
// the push that already runs hourly resolves what an outage or a rebuild
// stranded, rather than an operator remembering a command that until now did
// not exist.
//
// The restore-and-rescan half is proven end to end, through the shipped
// commands, against a real restic repository in a temporary directory and a
// throwaway PostgreSQL. Nothing about it can be proven with a fake: the whole
// claim is that bytes restored out of a snapshot describe into the same session
// identity the owning host published, and only real restic and the real
// adapters can say whether that is true.

// catalogSessionUIDs lists the session identities the catalog holds for one
// host. They are the thing the rescan has to reproduce exactly: a recovered row
// under a different digest would be a new session rather than the one the
// snapshot held.
func catalogSessionUIDs(t *testing.T, d *stagingDeployment, host string) []string {
	t.Helper()
	rows, err := d.catalog(t).QueryContext(t.Context(),
		`SELECT session_uid FROM sessions WHERE host_id = $1`, host)
	if err != nil {
		t.Fatalf("read the catalog's session rows: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			t.Fatalf("scan a session row: %v", err)
		}
		out = append(out, uid)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the catalog's session rows: %v", err)
	}
	sort.Strings(out)
	return out
}

// snapshotRow is the catalog's own record of one snapshot.
//
// sessions is a pointer because migrations/0002 made session_count nullable:
// NULL is how a catalog-pending row says it does not know how many sessions
// the snapshot held, which is a different claim from holding none.
type snapshotRow struct {
	state    string
	sessions *int
}

func catalogSnapshotRow(t *testing.T, d *stagingDeployment, id string) snapshotRow {
	t.Helper()
	var row snapshotRow
	if err := d.catalog(t).QueryRowContext(t.Context(),
		`SELECT commit_state, session_count FROM snapshots WHERE snapshot_id = $1`,
		id).Scan(&row.state, &row.sessions); err != nil {
		t.Fatalf("read the catalog row of %s: %v", id, err)
	}
	return row
}

// pushedSnapshotID reads the snapshot id out of one `archive push --json`.
func pushedSnapshotID(t *testing.T, stdout string) string {
	t.Helper()
	var res pushResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decode the push summary: %v\n%s", err, stdout)
	}
	if res.SnapshotID == "" {
		t.Fatalf("the push reported no snapshot:\n%s", stdout)
	}
	return res.SnapshotID
}

// A catalog-pending snapshot recovers its session detail through
// restore-and-rescan, driven by the ordinary push.
//
// The state is produced the way a real deployment produces it - `storage
// rebuild` discards a host's derived rows and leaves its snapshots pending with
// no session rows, which is also what a PostgreSQL outage and an adoption
// leave - and the local sources are removed first, so the rescan is the only
// thing that could supply what comes back. The identities recovered are
// compared against the ones the original push published, because equality is
// the actual contract: a session's catalog identity is a digest over the
// deployment, its own host, its harness and its source id, so a rescan that
// attributed it to the restoring instance would mint a second identity for one
// session and nothing would ever reconcile them.
func TestPushRecoversACatalogPendingSnapshotByRestoreAndRescan(t *testing.T) {
	d := newStagingDeployment(t)
	f := d.f.withRepo()
	f.threeSessions()
	f.bootstrapRepo()

	// One ordinary push: the snapshot is committed and its sessions published.
	stdout, _ := f.ok(f.with("archive", "push", "--json")...)
	pending := pushedSnapshotID(t, stdout)
	published := catalogSessionUIDs(t, d, testHostID)
	if len(published) != 3 {
		t.Fatalf("the first push published %d session rows, want the fixture's three", len(published))
	}

	// Remove the machine's own sessions, keeping the source root itself so the
	// next push still has something to back up. After this the transcripts
	// exist only inside the repository, which is what makes the recovery below
	// a recovery rather than a re-publication.
	entries, err := os.ReadDir(f.sessionsDir)
	if err != nil {
		t.Fatalf("read the fixture's sessions: %v", err)
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(f.sessionsDir, entry.Name())); err != nil {
			t.Fatalf("remove %s: %v", entry.Name(), err)
		}
	}

	// Strand the snapshot exactly as the shipped repair path does.
	f.ok(f.with("storage", "rebuild", "--host", testHostID, "--yes")...)
	if row := catalogSnapshotRow(t, d, pending); row.state != sharedcatalog.CommitPending {
		t.Fatalf("after a rebuild %s is %q, want %q", pending, row.state, sharedcatalog.CommitPending)
	} else if row.sessions != nil {
		t.Fatalf("a stranded snapshot claims %d sessions; the count must be unknown", *row.sessions)
	}
	if uids := catalogSessionUIDs(t, d, testHostID); len(uids) != 0 {
		t.Fatalf("a rebuild left %d session rows; this test needs the state where it left none", len(uids))
	}

	// The next push. It publishes its own snapshot, which holds no session at
	// all now, and then drains the catalog-pending one.
	stdout, stderr := f.ok(f.with("archive", "push", "--json")...)
	var res pushResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decode the push summary: %v\n%s", err, stdout)
	}
	if res.SnapshotsCompleted != 1 {
		t.Errorf("the push completed %d catalog-pending snapshots, want 1\nstderr:\n%s",
			res.SnapshotsCompleted, stderr)
	}
	if res.SessionsRecovered != 3 {
		t.Errorf("the push recovered %d session rows, want the fixture's three\nstderr:\n%s",
			res.SessionsRecovered, stderr)
	}
	if res.SnapshotsUnrecovered != 0 {
		t.Errorf("the push reported %d unrecovered snapshots\nstderr:\n%s",
			res.SnapshotsUnrecovered, stderr)
	}
	if !strings.Contains(stderr, "left catalog-pending") {
		t.Errorf("the drain did not report itself on stderr:\n%s", stderr)
	}

	// The catalog row is committed and counts what the snapshot held, which is
	// a known count where a moment ago it was unknown.
	row := catalogSnapshotRow(t, d, pending)
	if row.state != sharedcatalog.CommitCommitted {
		t.Errorf("%s is %q, want %q", pending, row.state, sharedcatalog.CommitCommitted)
	}
	if row.sessions == nil || *row.sessions != 3 {
		t.Errorf("%s records %v sessions, want 3", pending, row.sessions)
	}
	// And the identities are the ones the owning host published, not new ones.
	recovered := catalogSessionUIDs(t, d, testHostID)
	if len(recovered) != len(published) {
		t.Fatalf("recovered %d session rows, want %d", len(recovered), len(published))
	}
	for i, uid := range published {
		if recovered[i] != uid {
			t.Errorf("recovered session %d = %s, want the published identity %s", i, recovered[i], uid)
		}
	}

	// The detail a snapshot listing cannot supply is what the rescan is for, so
	// at least one row must carry a title and a real size rather than NULLs.
	var titled int
	if err := d.catalog(t).QueryRowContext(t.Context(), `
		SELECT count(*) FROM sessions
		 WHERE host_id = $1 AND title IS NOT NULL AND primary_size > 0`,
		testHostID).Scan(&titled); err != nil {
		t.Fatalf("read the recovered metadata: %v", err)
	}
	if titled != 3 {
		t.Errorf("%d recovered rows carry a title and a size, want 3", titled)
	}

	// The restore area is disposable and must not outlive the push that made
	// it: a rescan's bytes are neither browsable nor prunable.
	if left, err := os.ReadDir(filepath.Join(f.cacheDir, "rescan")); err == nil && len(left) != 0 {
		names := make([]string, 0, len(left))
		for _, entry := range left {
			names = append(names, entry.Name())
		}
		t.Errorf("the rescan area still holds %v after the push", names)
	}

	// Nothing is left pending, so a third push has nothing to drain - which is
	// what makes the count fall rather than merely move.
	stdout, _ = f.ok(f.with("archive", "push", "--json")...)
	var third pushResult
	if err := json.Unmarshal([]byte(stdout), &third); err != nil {
		t.Fatalf("decode the third push summary: %v\n%s", err, stdout)
	}
	if third.SnapshotsCompleted != 0 || third.SnapshotsUnrecovered != 0 {
		t.Errorf("a push with nothing stranded reported %d completed and %d unrecovered",
			third.SnapshotsCompleted, third.SnapshotsUnrecovered)
	}
}

// The other half of §9.1, through the shipped command: a snapshot some other
// machine left uncatalogued is adopted by whichever host pushes next, and then
// completed by the same push.
//
// Both halves are load-bearing. Adoption used to be filtered to the pushing
// host, so a snapshot stranded by a machine that was retired, died, or is
// merely idle waited for a push that never comes; and an adopted row arrives
// catalog-pending, so adoption without the rescan would only move the state
// that no command could clear. The foreign host's snapshot is stranded by
// removing its catalog rows, which is exactly what the state means - the
// repository holds the snapshot and the catalog holds nothing about it - and
// what a PostgreSQL outage during that host's push leaves behind.
func TestPushAdoptsAndCompletesAnotherHostsStrandedSnapshot(t *testing.T) {
	const otherHost = "otherhost"

	d := newStagingDeployment(t)
	f := d.f.withRepo()
	f.threeSessions()
	f.bootstrapRepo()

	// A snapshot belonging to another machine, published normally.
	stdout, _ := f.ok(f.with("archive", "push", "--host", otherHost, "--json")...)
	stranded := pushedSnapshotID(t, stdout)
	foreignUIDs := catalogSessionUIDs(t, d, otherHost)
	if len(foreignUIDs) != 3 {
		t.Fatalf("%s published %d session rows, want three", otherHost, len(foreignUIDs))
	}

	// Strand it: the repository keeps the snapshot, the catalog forgets it.
	// Order follows the foreign keys, and the host row stays because the
	// instance row references it - what adoption has to supply is the snapshot,
	// under that host's own numbering.
	db := d.catalog(t)
	for _, statement := range []string{
		`DELETE FROM sessions WHERE host_id = $1`,
		`DELETE FROM idempotency_keys
		  WHERE snapshot_id IN (SELECT snapshot_id FROM snapshots WHERE host_id = $1)`,
		`DELETE FROM snapshots WHERE host_id = $1`,
	} {
		if _, err := db.ExecContext(t.Context(), statement, otherHost); err != nil {
			t.Fatalf("strand %s's snapshot: %v", otherHost, err)
		}
	}

	// This host pushes. Nothing about the invocation mentions the other
	// machine: draining the repository is what a push does.
	stdout, stderr := f.ok(f.with("archive", "push", "--json")...)
	var res pushResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("decode the push summary: %v\n%s", err, stdout)
	}
	if res.SnapshotsAdopted != 1 {
		t.Errorf("the push adopted %d snapshots, want the other host's one\nstderr:\n%s",
			res.SnapshotsAdopted, stderr)
	}
	if res.SnapshotsCompleted != 1 || res.SessionsRecovered != 3 {
		t.Errorf("the push completed %d snapshots with %d sessions, want 1 and 3\nstderr:\n%s",
			res.SnapshotsCompleted, res.SessionsRecovered, stderr)
	}

	// The adopted row names the host restic recorded, in that host's own order
	// space: publication_order totally orders one host's snapshots, so the
	// other machine's first snapshot is its number 1 whatever this host is at.
	var host string
	var order int64
	if err := db.QueryRowContext(t.Context(),
		`SELECT host_id, publication_order FROM snapshots WHERE snapshot_id = $1`,
		stranded).Scan(&host, &order); err != nil {
		t.Fatalf("read the adopted row: %v", err)
	}
	if host != otherHost {
		t.Errorf("the adopted snapshot is attributed to %q, want %q", host, otherHost)
	}
	if order != 1 {
		t.Errorf("the adopted snapshot took order %d, want 1 in %s's own space", order, otherHost)
	}
	if row := catalogSnapshotRow(t, d, stranded); row.state != sharedcatalog.CommitCommitted {
		t.Errorf("the adopted snapshot is %q, want it completed to %q by the same push",
			row.state, sharedcatalog.CommitCommitted)
	}

	// And its sessions came back under the other machine's identities, not
	// this one's: the digest is over the owning host, so an adopter that
	// substituted itself would mint identities that machine never published.
	recovered := catalogSessionUIDs(t, d, otherHost)
	if len(recovered) != len(foreignUIDs) {
		t.Fatalf("recovered %d of %s's sessions, want %d", len(recovered), otherHost, len(foreignUIDs))
	}
	for i, uid := range foreignUIDs {
		if recovered[i] != uid {
			t.Errorf("recovered %s's session %d as %s, want %s", otherHost, i, recovered[i], uid)
		}
	}
	for _, uid := range catalogSessionUIDs(t, d, testHostID) {
		for _, foreign := range foreignUIDs {
			if uid == foreign {
				t.Errorf("session %s is recorded for both hosts; identity is per host", uid)
			}
		}
	}
}

// The drain is part of what a push reports, which means the human summary says
// so too. An operator reading the table has to be able to tell "nothing was
// stranded" from "nothing looked", so the two headline counts appear on every
// shared-mode push and the refinements appear when they have something to
// refine.
func TestReportPushStatesWhatTheDrainsDid(t *testing.T) {
	var stdout, stderr strings.Builder
	a := &app{stdout: &stdout, stderr: &stderr}

	err := a.reportPush(pushResult{
		SnapshotID: "abc", Host: "host-a", Catalog: catalogCommitted,
		SessionsPublished: 2, SnapshotsAdopted: 3,
		SnapshotsCompleted: 1, SessionsRecovered: 7, SnapshotsUnrecovered: 2,
	}, false)
	if err != nil {
		t.Fatalf("reportPush: %v", err)
	}
	for _, want := range []string{
		"catalog                committed",
		"sessions published     2",
		"snapshots adopted      3",
		"snapshots completed    1",
		"sessions recovered     7",
		"snapshots unrecovered  2",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the push summary is missing %q:\n%s", want, stdout.String())
		}
	}

	stdout.Reset()
	if err := a.reportPush(pushResult{
		SnapshotID: "abc", Host: "host-a", Catalog: catalogCommitted,
	}, false); err != nil {
		t.Fatalf("reportPush: %v", err)
	}
	quiet := stdout.String()
	for _, want := range []string{"snapshots adopted", "snapshots completed"} {
		if !strings.Contains(quiet, want) {
			t.Errorf("an ordinary push must still state %q:\n%s", want, quiet)
		}
	}
	for _, unwanted := range []string{"sessions recovered", "snapshots unrecovered"} {
		if strings.Contains(quiet, unwanted) {
			t.Errorf("an ordinary push should not state %q:\n%s", unwanted, quiet)
		}
	}
}

// resticListing builds a repository listing from host ids to snapshot ids.
func resticListing(hosts map[string][]string) []restic.Snapshot {
	var out []restic.Snapshot
	for host, ids := range hosts {
		for _, id := range ids {
			out = append(out, restic.Snapshot{ID: id, Host: host})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// The drain's bound is the property that makes it safe on an hourly timer, and
// the fairness of how the bound is spent is the property that makes a backlog
// on one machine not starve another machine's single stranded snapshot.
func TestPickPendingIsBoundedAndFairAcrossHosts(t *testing.T) {
	listing := resticListing(map[string][]string{
		"host-a": {"a1", "a2", "a3"},
		"host-b": {"b1"},
	})
	pending := []sharedcatalog.PendingSnapshot{
		{SnapshotID: "a1", HostID: "host-a", Order: 1},
		{SnapshotID: "a2", HostID: "host-a", Order: 2},
		{SnapshotID: "a3", HostID: "host-a", Order: 3},
		{SnapshotID: "b1", HostID: "host-b", Order: 1},
		// A pending row the repository no longer lists cannot be restored, and
		// is the append-only anomaly reconciliation surfaces rather than
		// something a rescan can fix.
		{SnapshotID: "gone", HostID: "host-b", Order: 2},
	}

	chosen := pickPending(pending, listing, 2)
	if len(chosen) != 2 {
		t.Fatalf("picked %d snapshots, want the limit of 2", len(chosen))
	}
	if chosen[0].ID != "a1" || chosen[1].ID != "b1" {
		t.Errorf("picked %s and %s, want one per host oldest first (a1, b1)",
			chosen[0].ID, chosen[1].ID)
	}

	all := pickPending(pending, listing, 10)
	if len(all) != 4 {
		t.Fatalf("picked %d of the 4 restorable snapshots", len(all))
	}
	for _, snap := range all {
		if snap.ID == "gone" {
			t.Error("picked a pending snapshot the repository no longer holds")
		}
	}
	if pickPending(pending, listing, 0) != nil {
		t.Error("a zero limit must pick nothing")
	}
}
