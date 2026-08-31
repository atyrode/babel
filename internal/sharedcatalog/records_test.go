package sharedcatalog

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"
)

// These tests defend the fleet read path (issue #109 items 3 and 5): that any
// authorized instance can browse every host's committed Phase B records with
// host, actor and sync state attributed, and that nothing a record says travels
// in the clear to get there.

// seedFleet registers two hosts, gives each an instance, and pairs the two so a
// committed record resolves to the machine that produced it (migrations/0007).
//
// inst-c is deliberately left unpaired: it is the instance that last registered
// before host_id existed, and its records must render as unattributed rather
// than be filed under a guessed host.
func seedFleet(t *testing.T, db *sql.DB) {
	t.Helper()
	seedHost(t, db, "h1")
	seedHost(t, db, "h2")
	if _, err := db.Exec(
		`INSERT INTO hosts (host_id, deployment_id, display_name) VALUES ('h1', 'd1', 'workstation')
		 ON CONFLICT (host_id) DO UPDATE SET display_name = excluded.display_name`); err != nil {
		t.Fatalf("name host h1: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO instances (instance_id, deployment_id, host_id) VALUES ('inst-c', 'd1', NULL)
		 ON CONFLICT (instance_id) DO NOTHING`); err != nil {
		t.Fatalf("seed unattributed instance: %v", err)
	}
	for instance, host := range map[string]string{"inst-a": "h1", "inst-b": "h2"} {
		if _, err := db.Exec(
			`UPDATE instances SET host_id = $2 WHERE instance_id = $1`, instance, host); err != nil {
			t.Fatalf("pair %s with %s: %v", instance, host, err)
		}
	}
}

// closureFrom builds a run belonging to one instance, so a test can commit
// records that attribute to different machines.
func closureFrom(runID, instance string, kinds ...RecordKind) RunClosure {
	c := sampleClosure(runID, kinds...)
	c.OriginInstanceID = instance
	return c
}

// The fleet read is the whole point of committing globally: a record another
// machine produced must be readable here, attributed to that machine, without
// that machine being reachable.
func TestRecordsReadEveryHostWithAttribution(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	mustSync(t, db, store, ring, closureFrom("run-a", "inst-a", KindHypothesis, KindFinding))
	mustSync(t, db, store, ring, closureFrom("run-b", "inst-b", KindObservation))
	mustSync(t, db, store, ring, closureFrom("run-c", "inst-c", KindProposal))

	got, err := Records(ctx, db, RecordFilter{DeploymentID: "d1"})
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("read %d records, want 4 from three hosts", len(got))
	}

	byID := map[string]FleetRecord{}
	for _, rec := range got {
		byID[rec.Record.RecordID] = rec
		if !rec.Committed() {
			t.Errorf("record %s: sync state %q, want committed",
				rec.Record.RecordID, rec.SyncState)
		}
		if rec.CommittedAt == nil {
			t.Errorf("record %s: committed with no commit time", rec.Record.RecordID)
		}
		if rec.OriginInstanceID == "" {
			t.Errorf("record %s: no actor attribution", rec.Record.RecordID)
		}
	}

	// The two attributed hosts, one of which has a display name and one of
	// which has not. An absent name must not become an absent host.
	if rec := byID["run-a-rec-0"]; rec.HostID != "h1" || rec.HostDisplayName != "workstation" {
		t.Errorf("run-a record attributed to host %q (%q), want h1 (workstation)",
			rec.HostID, rec.HostDisplayName)
	}
	if rec := byID["run-b-rec-0"]; rec.HostID != "h2" || rec.HostDisplayName != "" {
		t.Errorf("run-b record attributed to host %q (%q), want h2 with no display name",
			rec.HostID, rec.HostDisplayName)
	}

	// The unattributed instance. Absence is the answer: a record filed under a
	// guessed host is worse than one filed nowhere, because the guess is
	// invisible and the gap is not.
	if rec := byID["run-c-rec-0"]; rec.HostID != "" {
		t.Errorf("a record from an instance with no registered host resolved to %q, want no host",
			rec.HostID)
	}

	// An execution pin is not authorship. A run pinned to h2 but produced by
	// inst-a on h1 must attribute to h1, or the two meanings have merged.
	pinned := closureFrom("run-pin", "inst-a", KindReceipt)
	pinned.ExecutionHostID = "h2"
	mustSync(t, db, store, ring, pinned)
	pins, err := Records(ctx, db, RecordFilter{DeploymentID: "d1", RunIDs: []string{"run-pin"}})
	if err != nil {
		t.Fatalf("Records for the pinned run: %v", err)
	}
	if len(pins) != 1 {
		t.Fatalf("read %d records for the pinned run, want 1", len(pins))
	}
	if pins[0].HostID != "h1" || pins[0].ExecutionHostID != "h2" {
		t.Errorf("pinned run: host %q, execution host %q; want h1 produced it and h2 pinned",
			pins[0].HostID, pins[0].ExecutionHostID)
	}
}

// A host filter is what the CLI's --host flag and the web host chip pass, and
// it must narrow on the machine that produced the record rather than on the
// instance id that happens to resemble a host name.
func TestRecordsFilters(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	mustSync(t, db, store, ring, closureFrom("run-a", "inst-a", KindHypothesis, KindFinding))
	mustSync(t, db, store, ring, closureFrom("run-b", "inst-b", KindObservation))

	for _, tc := range []struct {
		name   string
		filter RecordFilter
		want   []string
	}{
		{"every host", RecordFilter{}, []string{"run-a-rec-0", "run-a-rec-1", "run-b-rec-0"}},
		{"one host", RecordFilter{Hosts: []string{"h2"}}, []string{"run-b-rec-0"}},
		{"both hosts named", RecordFilter{Hosts: []string{"h1", "h2"}},
			[]string{"run-a-rec-0", "run-a-rec-1", "run-b-rec-0"}},
		{"a host that has committed nothing", RecordFilter{Hosts: []string{"h-absent"}}, nil},
		{"one kind", RecordFilter{Kinds: []RecordKind{KindObservation}}, []string{"run-b-rec-0"}},
		{"one instance", RecordFilter{Instances: []string{"inst-a"}},
			[]string{"run-a-rec-0", "run-a-rec-1"}},
		{"one record", RecordFilter{RecordIDs: []string{"run-a-rec-1"}}, []string{"run-a-rec-1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.filter.DeploymentID = "d1"
			got, err := Records(ctx, db, tc.filter)
			if err != nil {
				t.Fatalf("Records: %v", err)
			}
			var ids []string
			for _, rec := range got {
				ids = append(ids, rec.Record.RecordID)
			}
			if fmt.Sprint(sortedStrings(ids)) != fmt.Sprint(sortedStrings(tc.want)) {
				t.Errorf("read %v, want %v", sortedStrings(ids), sortedStrings(tc.want))
			}
		})
	}

	// A deployment id is required rather than defaulted: it is what keeps two
	// unrelated fleets in one database unrelated.
	if _, err := Records(ctx, db, RecordFilter{}); !errors.Is(err, ErrRecordFilter) {
		t.Errorf("a filter with no deployment gave %v, want ErrRecordFilter", err)
	}
	if _, err := Records(ctx, db, RecordFilter{
		DeploymentID: "d1", Kinds: []RecordKind{"gossip"},
	}); !errors.Is(err, ErrRecordFilter) {
		t.Errorf("an unknown kind gave %v, want ErrRecordFilter", err)
	}
	if _, err := Records(ctx, db, RecordFilter{
		DeploymentID: "d1",
		Since:        time.Now(), Until: time.Now().Add(-time.Hour),
	}); !errors.Is(err, ErrRecordFilter) {
		t.Errorf("an inverted window gave %v, want ErrRecordFilter", err)
	}
}

// SPEC.md 9 states pending-sync output is not globally reviewable, so the browse
// default must exclude it - and an operator asking why their record is not
// visible must still be able to see it, with its state.
func TestRecordsExcludePendingUnlessAsked(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	// A run that declares two records and offers one stays pending.
	partial := closureFrom("run-partial", "inst-a", KindHypothesis)
	partial.RecordCount = 2
	if _, err := SyncRun(ctx, db, store, ring, partial); !errors.Is(err, ErrIncompleteClosure) {
		t.Fatalf("partial sync gave %v, want ErrIncompleteClosure", err)
	}

	committed, err := Records(ctx, db, RecordFilter{DeploymentID: "d1"})
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(committed) != 0 {
		t.Errorf("a browse of committed output returned %d staged records", len(committed))
	}

	staged, err := Records(ctx, db, RecordFilter{DeploymentID: "d1", IncludePending: true})
	if err != nil {
		t.Fatalf("Records with pending: %v", err)
	}
	if len(staged) != 1 {
		t.Fatalf("read %d records with pending admitted, want 1", len(staged))
	}
	if staged[0].SyncState != SyncPending || staged[0].Committed() {
		t.Errorf("staged record reports %q (committed=%v), want pending-sync",
			staged[0].SyncState, staged[0].Committed())
	}
	if staged[0].CommittedAt != nil {
		t.Error("a pending record carries a commit time")
	}
}

// The host filter's own vocabulary. It must be built from what has actually
// committed, and it must report the unattributed group rather than hiding it.
func TestRecordHostsReportsTheFilterVocabulary(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	mustSync(t, db, store, ring, closureFrom("run-a", "inst-a", KindHypothesis, KindFinding))
	mustSync(t, db, store, ring, closureFrom("run-c", "inst-c", KindProposal))

	hosts, err := RecordHosts(ctx, db, RecordFilter{DeploymentID: "d1"})
	if err != nil {
		t.Fatalf("RecordHosts: %v", err)
	}
	got := map[string]RecordHost{}
	for _, h := range hosts {
		got[h.HostID] = h
	}
	if len(got) != 2 {
		t.Fatalf("reported %d host groups, want 2 (h1 and the unattributed group): %+v", len(got), hosts)
	}
	if h := got["h1"]; h.Records != 2 || h.DisplayName != "workstation" || h.NewestCommit == nil {
		t.Errorf("h1 = %+v, want 2 records named workstation with a commit time", h)
	}
	if h, present := got[""]; !present || h.Records != 1 {
		t.Errorf("the unattributed group = %+v (present=%v), want 1 record", h, present)
	}
	// A host that has published archives but committed no analysis is absent:
	// offering it as a filter option would offer an option that matches
	// nothing.
	if _, present := got["h2"]; present {
		t.Error("a host that has committed no analysis was offered as a filter option")
	}
}

// Per-record sync state is what SPEC.md line 646 requires listings to keep
// rendering, and the shared catalog is its authority: a record has committed
// remotely exactly when the catalog says so.
func TestRecordSyncStates(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	mustSync(t, db, store, ring, closureFrom("run-a", "inst-a", KindHypothesis))
	partial := closureFrom("run-partial", "inst-a", KindFinding)
	partial.RecordCount = 2
	if _, err := SyncRun(ctx, db, store, ring, partial); !errors.Is(err, ErrIncompleteClosure) {
		t.Fatalf("partial sync gave %v, want ErrIncompleteClosure", err)
	}

	states, err := RecordSyncStates(ctx, db,
		[]string{"run-a-rec-0", "run-partial-rec-0", "never-staged"})
	if err != nil {
		t.Fatalf("RecordSyncStates: %v", err)
	}
	if states["run-a-rec-0"] != SyncCommitted {
		t.Errorf("committed record reports %q", states["run-a-rec-0"])
	}
	if states["run-partial-rec-0"] != SyncPending {
		t.Errorf("record of a pending run reports %q", states["run-partial-rec-0"])
	}
	// An id the catalog does not hold is absent rather than defaulted. The
	// caller resolves absence against its own journal, which is the only thing
	// that can tell "staged while PostgreSQL was unreachable" from "never
	// staged at all".
	if state, present := states["never-staged"]; present {
		t.Errorf("an unknown id reported %q, want absence", state)
	}

	if states, err := RecordSyncStates(ctx, db, nil); err != nil || len(states) != 0 {
		t.Errorf("an empty request gave %v, %v; want an empty map and no query", states, err)
	}
}

// Issue #109 item 5, and SPEC.md 14's open item closed: only identifiers,
// timestamps, kinds, host/actor and sync metadata may sit in a Phase B
// plaintext column, and the enforcement lives in one place.
func TestPhaseBPlaintextEligibilityIsEnforcedInOnePlace(t *testing.T) {
	// Every column the fleet read actually selects passes the gate. This is
	// pointed at the projection rather than at a comment about the projection,
	// so a query that grew a content column fails here even if nobody updated
	// this test's expectations.
	for table, columns := range RecordProjection() {
		if err := AssertPhaseBPlaintext(table, columns...); err != nil {
			t.Errorf("the fleet read projection is not plaintext-eligible: %v", err)
		}
	}

	// The gate refuses a table that holds no Phase B output, because a gate
	// that silently approves whatever it does not recognize is not a gate.
	if err := AssertPhaseBPlaintext("sessions", "title"); err == nil {
		t.Error("the Phase B gate accepted a Phase A table")
	}

	// The bite: `title` is admitted by the general allowlist since
	// migrations/0004, and Phase B must still refuse a column of that class.
	// The check is on the class, so this is asserted through a class the
	// allowlist maps and Phase B excludes.
	refusedClasses := []Class{
		ClassSessionLabel, ClassWorkspacePath, ClassSessionGrade,
		ClassHostIdentity, ClassSpendMeasure,
	}
	admitted := map[Class]bool{}
	for _, class := range PhaseBPlaintextClasses() {
		admitted[class] = true
	}
	for _, class := range refusedClasses {
		if admitted[class] {
			t.Errorf("Phase B admits %q in plaintext; a record's content would follow", class)
		}
	}
	if len(admitted) != 6 {
		t.Errorf("Phase B admits %d classes, want exactly the six SPEC.md 9 names: %v",
			len(admitted), PhaseBPlaintextClasses())
	}

	// A column that is not in the allowlist at all is refused too, so the two
	// questions cannot be answered by two different lists.
	if err := AssertPhaseBPlaintext("analysis_records", "statement"); err == nil {
		t.Error("the Phase B gate accepted a column no allowlist entry describes")
	}

	// And the live schema passes, which is what Migrate calls Verify for.
	db := newInternalDB(t)
	if err := Verify(context.Background(), db); err != nil {
		t.Errorf("the migrated schema fails the plaintext gate: %v", err)
	}
}

// The fleet read must not become the leak the payload column was removed to
// prevent. This drives the whole read path - the browse, the host vocabulary,
// the sync states - and then scans every text-shaped value in Babel's schema
// for the sentinel those records' payloads carry.
func TestFleetReadNeverPutsRecordContentInPostgres(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	mustSync(t, db, store, ring, closureFrom("run-a", "inst-a", KindHypothesis, KindFinding))
	mustSync(t, db, store, ring, closureFrom("run-b", "inst-b", KindObservation))

	records, err := Records(ctx, db, RecordFilter{DeploymentID: "d1"})
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("read %d records, want 3", len(records))
	}
	if _, err := RecordHosts(ctx, db, RecordFilter{DeploymentID: "d1"}); err != nil {
		t.Fatalf("RecordHosts: %v", err)
	}
	ids := make([]string, len(records))
	for i, rec := range records {
		ids[i] = rec.Record.RecordID
	}
	if _, err := RecordSyncStates(ctx, db, ids); err != nil {
		t.Fatalf("RecordSyncStates: %v", err)
	}

	for _, hit := range scanSchemaForText(t, db, sentinel) {
		t.Errorf("a record's statement bytes are in PostgreSQL in %s", hit)
	}

	// Positive control. The scan's silence means nothing unless the scan reads
	// row values, and the value it must find is one written into a column the
	// fleet read itself selects.
	const control = "FLEET-READ-SCAN-CONTROL"
	if _, err := db.Exec(
		`INSERT INTO hosts (host_id, deployment_id) VALUES ($1, 'd1')`, control); err != nil {
		t.Fatalf("seed control marker: %v", err)
	}
	if hits := scanSchemaForText(t, db, control); len(hits) == 0 {
		t.Fatal("the plaintext scan found no marker written straight into a column; " +
			"it is not reading row values, so its verdict above is vacuous")
	}

	// What the read does hand back is a reference, and the bytes behind it are
	// ciphertext. A reader needs the object store and a key, which is the whole
	// shape of "decrypted client-side".
	for _, rec := range records {
		object, err := store.Get(ctx, rec.Record.ObjectKey)
		if err != nil {
			t.Fatalf("read the object the fleet read names: %v", err)
		}
		if string(object) == "" {
			t.Fatalf("record %s names an empty object", rec.Record.RecordID)
		}
		if contains(object, sentinel) {
			t.Errorf("record %s: the stored object contains plaintext", rec.Record.RecordID)
		}
		plaintext, err := OpenRecord(ctx, store, ring, rec.Record)
		if err != nil {
			t.Fatalf("open record %s: %v", rec.Record.RecordID, err)
		}
		if !contains(plaintext, sentinel) {
			t.Errorf("record %s decrypted without its own payload; the read is vacuous",
				rec.Record.RecordID)
		}
	}
}

func contains(haystack []byte, needle string) bool {
	return bytes.Contains(haystack, []byte(needle))
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
