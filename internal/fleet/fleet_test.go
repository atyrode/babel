package fleet_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/envelope"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// These tests defend issue #109 items 3 and 4 end to end below the rendering
// surfaces: a record another host committed is readable here with its
// attribution, its content is decrypted locally, per-record sync state resolves
// honestly in all four cases, and a fleet ingest reaches the rebuildable cache
// and never the durable store.

// sentinel is the statement text every seeded record carries. It must be
// readable only after decryption: if it turns up in a plaintext row, the whole
// arrangement has failed.
const sentinel = "BABEL-FLEET-READ-SENTINEL-do-not-leak"

// journal is the publication journal seam. A real one lives in internal/sync;
// this is the one method the read path asks of it.
type journal map[string]string

func (j journal) SyncState(ctx context.Context, entityID string) (string, error) {
	return j[entityID], nil
}

// failingJournal proves an error from the journal is reported rather than
// silently read as "nothing staged", which would render a staged record as
// local and tell the operator nothing is owed.
type failingJournal struct{}

func (failingJournal) SyncState(context.Context, string) (string, error) {
	return "", errors.New("journal unreadable")
}

type harness struct {
	reader *fleet.Reader
	db     *sql.DB
	store  *memStore
	ring   *envelope.Keyring
}

// newHarness builds a deployment with two attributed hosts and one instance
// that has no registered host, and a reader whose local host is h1.
//
// It hands back the connection as well as the reader. A test that seeds
// committed runs is a publisher for the length of that write, and the read
// surface has no business exposing its own connection to make that possible.
func newHarness(t *testing.T) harness {
	t.Helper()
	ctx := t.Context()
	db := newDB(t)
	store, ring := newMemStore(), newKeyring(t, "key-1")

	for _, stmt := range []string{
		`INSERT INTO deployments (deployment_id, schema_version) VALUES ('d1', 1)`,
		`INSERT INTO hosts (host_id, deployment_id, display_name) VALUES ('h1', 'd1', 'workstation')`,
		`INSERT INTO hosts (host_id, deployment_id) VALUES ('h2', 'd1')`,
		`INSERT INTO instances (instance_id, deployment_id, host_id) VALUES ('inst-a', 'd1', 'h1')`,
		`INSERT INTO instances (instance_id, deployment_id, host_id) VALUES ('inst-b', 'd1', 'h2')`,
		`INSERT INTO instances (instance_id, deployment_id) VALUES ('inst-orphan', 'd1')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}

	reader, err := fleet.NewReader(db, store, ring, "d1", "h1")
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return harness{reader: reader, db: db, store: store, ring: ring}
}

// commit publishes one run through the same object-first, database-last
// protocol a real publisher uses, so what the read path reads is what a
// publisher would have written.
func (h harness) commit(t *testing.T, runID, instance string, records ...staged) {
	t.Helper()
	closure := sharedcatalog.RunClosure{
		RunID:            runID,
		DeploymentID:     "d1",
		OriginInstanceID: instance,
		RecordCount:      len(records),
	}
	for i, rec := range records {
		payload, err := rec.published.Marshal()
		if err != nil {
			t.Fatalf("marshal %s: %v", rec.published.ID, err)
		}
		closure.Records = append(closure.Records, sharedcatalog.StagedRecord{
			RecordID: rec.published.ID,
			Kind:     rec.kind,
			Schema:   1,
			Ordinal:  int64(i),
			Payload:  payload,
		})
	}
	if _, err := sharedcatalog.SyncRun(t.Context(), h.db, h.store, h.ring, closure); err != nil {
		t.Fatalf("commit run %s: %v", runID, err)
	}
}

type staged struct {
	kind      sharedcatalog.RecordKind
	published frontier.PublishedRecord
}

// hypothesis builds one publishable candidate whose statement is the sentinel,
// so a decrypted read is provably a decrypted read.
func hypothesis(id, statement string) staged {
	payload, _ := json.Marshal(frontier.HypothesisPayload{
		Statement: statement + " " + sentinel,
		Novelty:   0.5,
		Priority:  0.5,
	})
	return staged{
		kind: sharedcatalog.KindHypothesis,
		published: frontier.PublishedRecord{
			Schema:    frontier.RecordSchema,
			Kind:      frontier.PublishedHypothesis,
			ID:        id,
			RootID:    id,
			RunID:     "run-x",
			Status:    frontier.StatusUntriaged,
			CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
			Payload:   payload,
		},
	}
}

// The four-case sync resolution, which is the one place SPEC.md line 646's
// "sync state stays visible per record" is answered. Getting case 4 wrong is
// the interesting failure: rendering a record as pending-sync when nothing will
// ever carry it is a promise the system cannot keep.
func TestSyncStatesResolvesAllFourCases(t *testing.T) {
	h := newHarness(t)
	reader, store, ring := h.reader, h.store, h.ring
	ctx := t.Context()
	db := h.db

	h.commit(t, "run-committed", "inst-a", hypothesis("hyp-committed", "one"))

	// A run that declares more than it offers stays pending, and its record is
	// therefore staged remotely rather than reviewable.
	partial := sharedcatalog.RunClosure{
		RunID: "run-partial", DeploymentID: "d1", OriginInstanceID: "inst-a", RecordCount: 2,
	}
	payload, err := hypothesis("hyp-partial", "two").published.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	partial.Records = []sharedcatalog.StagedRecord{{
		RecordID: "hyp-partial", Kind: sharedcatalog.KindHypothesis,
		Schema: 1, Ordinal: 0, Payload: payload,
	}}
	if _, err := sharedcatalog.SyncRun(ctx, db, store, ring, partial); !errors.Is(err, sharedcatalog.ErrIncompleteClosure) {
		t.Fatalf("partial sync gave %v, want ErrIncompleteClosure", err)
	}

	ids := []string{"hyp-committed", "hyp-partial", "hyp-staged-locally", "hyp-never-staged"}
	j := journal{"hyp-staged-locally": sharedcatalog.SyncPending}

	got, err := reader.SyncStates(ctx, j, ids)
	if err != nil {
		t.Fatalf("SyncStates: %v", err)
	}
	want := map[string]string{
		// 1. a remote row under a committed run
		"hyp-committed": sharedcatalog.SyncCommitted,
		// 2. a remote row under a pending run: part of the closure committed
		"hyp-partial": sharedcatalog.SyncPending,
		// 3. no remote row, but the journal is holding it - the case only the
		//    journal can answer, because a record staged while PostgreSQL was
		//    unreachable has no remote row at all
		"hyp-staged-locally": sharedcatalog.SyncPending,
		// 4. no remote row and no journal claim: local, NOT pending-sync
		"hyp-never-staged": fleet.SyncLocal,
	}
	for id, wantState := range want {
		if got[id] != wantState {
			t.Errorf("%s = %q, want %q", id, got[id], wantState)
		}
	}

	// Without a journal, case 3 collapses into case 4, which is the honest
	// answer for a build that has no journal: nothing here claims the record is
	// going anywhere.
	noJournal, err := reader.SyncStates(ctx, nil, ids)
	if err != nil {
		t.Fatalf("SyncStates with no journal: %v", err)
	}
	if noJournal["hyp-staged-locally"] != fleet.SyncLocal {
		t.Errorf("with no journal, a staged record reported %q, want %q",
			noJournal["hyp-staged-locally"], fleet.SyncLocal)
	}
	if noJournal["hyp-committed"] != sharedcatalog.SyncCommitted {
		t.Errorf("with no journal, a committed record reported %q", noJournal["hyp-committed"])
	}

	// A journal that cannot answer is an error, not a silent "local": the
	// difference is whether the operator is told something is owed.
	if _, err := reader.SyncStates(ctx, failingJournal{}, ids); err == nil {
		t.Error("an unreadable journal was reported as nothing staged")
	}

	// Local mode has no reader at all and must still render a sync column.
	local, err := fleet.LocalSyncStates(ctx, j, ids)
	if err != nil {
		t.Fatalf("LocalSyncStates: %v", err)
	}
	if local["hyp-staged-locally"] != sharedcatalog.SyncPending ||
		local["hyp-committed"] != fleet.SyncLocal {
		t.Errorf("local-mode states = %v; want the journal's answer and %q for the rest",
			local, fleet.SyncLocal)
	}
	if states, err := fleet.LocalSyncStates(ctx, nil, ids); err != nil {
		t.Fatalf("LocalSyncStates with no journal: %v", err)
	} else {
		for _, id := range ids {
			if states[id] != fleet.SyncLocal {
				t.Errorf("%s = %q with no journal, want %q", id, states[id], fleet.SyncLocal)
			}
		}
	}
}

// Content is decrypted here, on this machine, and a record this instance cannot
// open costs that record and never the read. A fleet browse that failed whole
// because one host published something this binary cannot decrypt would make
// every machine hostage to every other machine's keys.
func TestRecordsWithContentReportsPerRecordFailure(t *testing.T) {
	h := newHarness(t)
	reader, store, ring := h.reader, h.store, h.ring
	ctx := t.Context()
	db := h.db

	h.commit(t, "run-a", "inst-a",
		hypothesis("hyp-1", "the release pipeline skips verification"),
		hypothesis("hyp-2", "scheduled deployments report success"))

	records, err := reader.RecordsWithContent(ctx, sharedcatalog.RecordFilter{})
	if err != nil {
		t.Fatalf("RecordsWithContent: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("read %d records, want 2", len(records))
	}
	for _, rec := range records {
		if rec.Published == nil {
			t.Fatalf("record %s did not open: %s", rec.Record.RecordID, rec.Unopened)
		}
		out, err := rec.Published.Output()
		if err != nil {
			t.Fatalf("derive output for %s: %v", rec.Record.RecordID, err)
		}
		if !strings.Contains(out.Text, sentinel) {
			t.Errorf("record %s decrypted without its own statement; the read is vacuous",
				rec.Record.RecordID)
		}
		if rec.HostID != "h1" || rec.HostDisplayName != "workstation" {
			t.Errorf("record %s attributed to %q (%q), want h1 (workstation)",
				rec.Record.RecordID, rec.HostID, rec.HostDisplayName)
		}
	}

	// One object the store cannot return. The other record still arrives, and
	// the reason travels with the one that did not.
	store.failGet = func(key string) error {
		if strings.Contains(key, "hyp-2") {
			return errors.New("object store unavailable")
		}
		return nil
	}
	partial, err := reader.RecordsWithContent(ctx, sharedcatalog.RecordFilter{})
	if err != nil {
		t.Fatalf("RecordsWithContent after a store failure: %v", err)
	}
	var opened, unopened int
	for _, rec := range partial {
		if rec.Published != nil {
			opened++
			continue
		}
		unopened++
		if rec.Unopened == "" {
			t.Errorf("record %s is unopened with no reason", rec.Record.RecordID)
		}
	}
	if opened != 1 || unopened != 1 {
		t.Errorf("after one store failure: %d opened, %d unopened; want one of each", opened, unopened)
	}
	store.failGet = nil

	// A record sealed under a key this instance does not hold is named as a key
	// problem before any network round trip, because SPEC.md 9 makes the key id
	// plaintext for exactly that reason.
	stranger := newKeyring(t, "key-elsewhere")
	strangerReader, err := fleet.NewReader(db, store, stranger, "d1", "h1")
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	blind, err := strangerReader.RecordsWithContent(ctx, sharedcatalog.RecordFilter{})
	if err != nil {
		t.Fatalf("RecordsWithContent with a stranger's ring: %v", err)
	}
	for _, rec := range blind {
		if rec.Published != nil {
			t.Errorf("record %s opened under a key that cannot open it", rec.Record.RecordID)
		}
		if !strings.Contains(rec.Unopened, "key") {
			t.Errorf("record %s: %q does not name the missing key", rec.Record.RecordID, rec.Unopened)
		}
	}

	// NewReader refuses a configuration that is not a fleet, rather than
	// panicking three calls later.
	for _, tc := range []struct {
		name  string
		build func() (*fleet.Reader, error)
	}{
		{"no catalog", func() (*fleet.Reader, error) {
			return fleet.NewReader(nil, store, ring, "d1", "h1")
		}},
		{"no object store", func() (*fleet.Reader, error) {
			return fleet.NewReader(db, nil, ring, "d1", "h1")
		}},
		{"no keyring", func() (*fleet.Reader, error) {
			return fleet.NewReader(db, store, nil, "d1", "h1")
		}},
		{"no deployment", func() (*fleet.Reader, error) {
			return fleet.NewReader(db, store, ring, "", "h1")
		}},
	} {
		if _, err := tc.build(); !errors.Is(err, fleet.ErrNotConfigured) {
			t.Errorf("%s gave %v, want ErrNotConfigured", tc.name, err)
		}
	}
}

// The fleet ingest, which is what makes dedup answer across machines. It writes
// only to the rebuildable index, and every property below is about that being
// enough.
func TestIngestBuildsPerHostPartitions(t *testing.T) {
	h := newHarness(t)
	reader := h.reader
	ctx := t.Context()
	db := h.db

	idx, err := index.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })

	h.commit(t, "run-a", "inst-a", hypothesis("hyp-a", "pipeline skips verification"))
	h.commit(t, "run-b", "inst-b", hypothesis("hyp-b", "deployments report success"))
	h.commit(t, "run-orphan", "inst-orphan", hypothesis("hyp-orphan", "unattributed work"))

	report, err := reader.Ingest(ctx, idx, fleet.IngestOptions{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if report.Hosts["h1"].Added != 1 || report.Hosts["h2"].Added != 1 {
		t.Errorf("ingest report = %+v, want one record per attributed host", report.Hosts)
	}
	// A record whose origin instance has no registered host is counted, not
	// filed under a guess: a record on the wrong machine's shelf is invisible,
	// and a number an operator can see is not.
	if report.Unattributed != 1 {
		t.Errorf("unattributed = %d, want 1", report.Unattributed)
	}
	if len(report.Unopened) != 0 {
		t.Errorf("unopened = %v, want none", report.Unopened)
	}

	// One search, both machines, each hit attributed.
	hits, err := idx.FrontierSearch(ctx, index.FrontierQuery{Match: sentinel})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	byOrigin := map[string]string{}
	for _, hit := range hits {
		byOrigin[hit.Origin] = hit.ID
	}
	if byOrigin["h1"] != "hyp-a" || byOrigin["h2"] != "hyp-b" {
		t.Errorf("fleet search = %v, want hyp-a on h1 and hyp-b on h2", byOrigin)
	}
	if _, filed := byOrigin[index.LocalOrigin]; filed {
		t.Error("a remote record was filed in this machine's own partition")
	}

	// Idempotent: a second ingest over unchanged output writes nothing.
	again, err := reader.Ingest(ctx, idx, fleet.IngestOptions{})
	if err != nil {
		t.Fatalf("second Ingest: %v", err)
	}
	if again.Hosts["h1"].Skipped != 1 || again.Hosts["h1"].Added != 0 {
		t.Errorf("second ingest of h1 = %+v, want one skip and no write", again.Hosts["h1"])
	}

	// Rebuild-from-remote is a pure cache rebuild: it drops the remote
	// partitions and reconstructs them, and it never touches the local one.
	local := frontier.Output{
		Kind: frontier.OutputFinding, ID: "fnd-local", RootID: "fnd-local",
		CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		Summary:   "held durably here", Text: "held durably here",
	}
	if _, err := idx.IndexFrontier(ctx, []frontier.Output{local}); err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}
	rebuilt, err := reader.Ingest(ctx, idx, fleet.IngestOptions{Rebuild: true})
	if err != nil {
		t.Fatalf("Ingest with rebuild: %v", err)
	}
	if len(rebuilt.Forgotten) != 2 {
		t.Errorf("rebuild forgot %v, want both remote partitions", rebuilt.Forgotten)
	}
	origins, err := idx.FrontierOrigins(ctx)
	if err != nil {
		t.Fatalf("FrontierOrigins: %v", err)
	}
	if origins[index.LocalOrigin] != 1 {
		t.Errorf("a rebuild-from-remote disturbed the local partition: %v", origins)
	}
	if origins["h1"] != 1 || origins["h2"] != 1 {
		t.Errorf("origins after rebuild = %v, want both hosts restored", origins)
	}

	// A host narrowing forgets nothing: a caller that asked about one machine
	// has said nothing about the others.
	narrowed, err := reader.Ingest(ctx, idx, fleet.IngestOptions{Hosts: []string{"h1"}})
	if err != nil {
		t.Fatalf("narrowed Ingest: %v", err)
	}
	if len(narrowed.Forgotten) != 0 {
		t.Errorf("a narrowed ingest forgot %v", narrowed.Forgotten)
	}
	if _, ingested := narrowed.Hosts["h2"]; ingested {
		t.Error("a narrowed ingest reconciled a host it was not asked about")
	}

	// And a host the catalog no longer reports is forgotten on the next full
	// ingest, so a retired machine stops answering as though it were reporting.
	if _, err := db.ExecContext(ctx,
		`UPDATE instances SET host_id = NULL WHERE instance_id = 'inst-b'`); err != nil {
		t.Fatalf("retire h2: %v", err)
	}
	retired, err := reader.Ingest(ctx, idx, fleet.IngestOptions{})
	if err != nil {
		t.Fatalf("Ingest after retiring a host: %v", err)
	}
	if fmt.Sprint(retired.Forgotten) != "[h2]" {
		t.Errorf("forgot %v, want [h2]", retired.Forgotten)
	}
}

// Ingest must never place a record it cannot decode, and must never fail whole
// because of one. A payload from a build whose shape has moved on is authentic
// and undecodable, which is a version problem reported per record.
func TestIngestSurvivesAnUndecodableRecord(t *testing.T) {
	h := newHarness(t)
	reader, store, ring := h.reader, h.store, h.ring
	ctx := t.Context()
	db := h.db

	idx, err := index.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })

	// A record whose sealed payload is not a published record at all.
	closure := sharedcatalog.RunClosure{
		RunID: "run-garbled", DeploymentID: "d1", OriginInstanceID: "inst-a", RecordCount: 2,
		Records: []sharedcatalog.StagedRecord{
			{RecordID: "hyp-garbled", Kind: sharedcatalog.KindHypothesis,
				Schema: 1, Ordinal: 0, Payload: []byte(`{"not":"a published record"}`)},
		},
	}
	good, err := hypothesis("hyp-good", "pipeline skips verification").published.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	closure.Records = append(closure.Records, sharedcatalog.StagedRecord{
		RecordID: "hyp-good", Kind: sharedcatalog.KindHypothesis,
		Schema: 1, Ordinal: 1, Payload: good,
	})
	if _, err := sharedcatalog.SyncRun(ctx, db, store, ring, closure); err != nil {
		t.Fatalf("commit: %v", err)
	}

	report, err := reader.Ingest(ctx, idx, fleet.IngestOptions{})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if len(report.Unopened) != 1 || !strings.Contains(report.Unopened[0], "hyp-garbled") {
		t.Errorf("unopened = %v, want the garbled record named", report.Unopened)
	}
	if report.Hosts["h1"].Added != 1 {
		t.Errorf("h1 = %+v, want the decodable record indexed", report.Hosts["h1"])
	}
	if _, err := reader.Ingest(nil, nil, fleet.IngestOptions{}); err == nil {
		t.Error("an ingest with no index was accepted")
	}
}

// The host vocabulary a filter offers, and the local host label a renderer needs
// in order to mark its own rows as its own.
func TestHostsAndLocalHost(t *testing.T) {
	h := newHarness(t)
	reader := h.reader
	ctx := t.Context()

	h.commit(t, "run-a", "inst-a", hypothesis("hyp-a", "one"))
	h.commit(t, "run-orphan", "inst-orphan", hypothesis("hyp-orphan", "two"))

	if reader.LocalHost() != "h1" {
		t.Errorf("LocalHost = %q, want h1", reader.LocalHost())
	}
	hosts, err := reader.Hosts(ctx, sharedcatalog.RecordFilter{})
	if err != nil {
		t.Fatalf("Hosts: %v", err)
	}
	got := map[string]sharedcatalog.RecordHost{}
	for _, h := range hosts {
		got[h.HostID] = h
	}
	if h := got["h1"]; h.Records != 1 || h.DisplayName != "workstation" {
		t.Errorf("h1 = %+v, want one record named workstation", h)
	}
	if h, present := got[""]; !present || h.Records != 1 {
		t.Errorf("the unattributed group = %+v (present=%v), want one record", h, present)
	}
	if _, present := got["h2"]; present {
		t.Error("a host that has committed nothing was offered as a filter option")
	}
}

// A citation edge commits under the shared catalog's `link` kind, which
// internal/reference reuses on purpose rather than widening migrations/0003's
// closed vocabulary, and both packages spell their discriminator `kind`. So the
// authenticated catalog row cannot say which decoder owns the bytes, and a
// reader that assumed internal/frontier refused every edge in the fleet as
// `record ref_x is kind "inspired_by"`: a correctly committed record lost to
// the wrong decoder.
//
// The record is written by the real edge store and published through the real
// staging hook, so what the reader opens is what a producing host would have
// sealed rather than a second definition of edge JSON maintained here.
func TestOpenRoutesACitationEdgeToItsOwnDecoder(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	registry := reference.NewRegistry()
	for _, namespace := range []string{"finding", "hypothesis"} {
		if err := registry.Register(namespace, reference.ResolverFunc(
			func(context.Context, string) (bool, error) { return true, nil })); err != nil {
			t.Fatalf("register %s: %v", namespace, err)
		}
	}
	capture := &publicationCapture{}
	edges, err := reference.Open(t.TempDir(),
		reference.WithResolvers(registry), reference.WithSync(capture))
	if err != nil {
		t.Fatalf("open edge store: %v", err)
	}
	t.Cleanup(func() { edges.Close() })
	if _, err := edges.Append(ctx, reference.Edge{
		Kind:      reference.KindInspiredBy,
		From:      reference.RecordRef{Kind: "finding", ID: "fnd-cited"},
		To:        reference.RecordRef{Kind: "hypothesis", ID: "hyp-source"},
		ActorKind: reference.ActorRun,
		ActorRef:  "run-edge",
		Note:      "retrieval surfaced it " + sentinel,
	}); err != nil {
		t.Fatalf("append edge: %v", err)
	}
	if len(capture.records) != 1 {
		t.Fatalf("the edge store staged %d records, want 1", len(capture.records))
	}
	staged := capture.records[0]

	closure := sharedcatalog.RunClosure{
		RunID: "run-edge", DeploymentID: "d1", OriginInstanceID: "inst-b",
		Records: []sharedcatalog.StagedRecord{{
			RecordID: staged.EntityID, Kind: staged.Kind, Schema: staged.Schema,
			Payload: staged.Payload, Edge: staged.Edge,
		}, {
			// A `link` object in neither vocabulary. The routing must not
			// swallow it: a record no decoder claims is still a record this
			// instance could not open, and it has to say so.
			RecordID: "link-alien", Kind: sharedcatalog.KindLink, Schema: 1, Ordinal: 1,
			Payload: []byte(`{"schema":1,"kind":"cites","id":"link-alien"}`),
		}},
	}
	closure.RecordCount = len(closure.Records)
	if _, err := sharedcatalog.SyncRun(ctx, h.db, h.store, h.ring, closure); err != nil {
		t.Fatalf("commit the edge closure: %v", err)
	}

	rows, err := h.reader.RecordsWithContent(ctx, sharedcatalog.RecordFilter{})
	if err != nil {
		t.Fatalf("RecordsWithContent: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("read %d records, want 2", len(rows))
	}
	for _, row := range rows {
		if row.Record.RecordID == "link-alien" {
			if row.Unopened == "" || row.Edge != nil || row.Published != nil {
				t.Fatalf("a link in neither vocabulary opened: %+v", row)
			}
			continue
		}
		if row.Unopened != "" {
			t.Fatalf("edge %s did not open: %s", row.Record.RecordID, row.Unopened)
		}
		if row.Edge == nil {
			t.Fatalf("edge %s opened without its citation projection", row.Record.RecordID)
		}
		if row.Published != nil {
			t.Errorf("edge %s acquired a frontier projection", row.Record.RecordID)
		}
		if row.Edge.Kind != reference.KindInspiredBy {
			t.Errorf("edge kind = %q, want %q", row.Edge.Kind, reference.KindInspiredBy)
		}
		if row.Edge.From.ID != "fnd-cited" || row.Edge.To.ID != "hyp-source" {
			t.Errorf("endpoints = %s -> %s, want fnd-cited -> hyp-source",
				row.Edge.From, row.Edge.To)
		}
		// The note is the sealed half, so a summary carrying the sentinel is
		// proof the line was derived from a decrypted object rather than from
		// the plaintext citation columns.
		summary := row.Edge.Summary()
		if !strings.Contains(summary, "finding:fnd-cited inspired_by hypothesis:hyp-source") ||
			!strings.Contains(summary, sentinel) {
			t.Errorf("summary = %q; it must name the citation and come from the sealed note", summary)
		}
	}
}

// A proposed next action and an operator's ruling on one commit under the
// shared catalog's `disposition` kind, which internal/disposition reuses on
// purpose rather than widening migrations/0003's closed vocabulary. So does
// internal/frontier's review answer, and the authenticated catalog row says
// `disposition` and nothing more.
//
// A reader that sent every one of them to frontier refused this package's own
// records as `carries no schema version`: a correctly committed record lost to
// the wrong decoder, rendering in every listing as a row with no content and
// no explanation an operator could act on.
//
// The records are written by the real disposition store and published through
// the real staging hook, so what the reader opens is what a producing host
// would have sealed rather than a second definition of the wire form
// maintained here. A frontier review answer rides along in the same closure,
// because the routing has to keep answering for the publisher it already had.
func TestOpenRoutesADispositionToItsOwnDecoder(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	dir := t.TempDir()
	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() { front.Close() })
	subject, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "run-disposition",
		Payload: frontier.HypothesisPayload{Statement: "the manifest is read twice", Novelty: 0.3, Priority: 0.3},
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	capture := &publicationCapture{}
	actions, err := disposition.Open(dir, front, disposition.WithSync(capture))
	if err != nil {
		t.Fatalf("open dispositions: %v", err)
	}
	t.Cleanup(func() { actions.Close() })
	action, err := actions.Propose(ctx, disposition.ProposeInput{
		Record:     frontier.Ref{Type: frontier.EntityHypothesis, ID: subject.ID},
		Kind:       disposition.KindDevelopFurther,
		ProposedBy: frontier.Run("run-disposition"),
		Ref:        "act-1",
		Payload:    disposition.Payload{Summary: "read the other deploy manifests " + sentinel},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	if _, err := actions.Decide(ctx, disposition.DecideInput{
		DispositionID: action.ID, Ruling: disposition.RulingAccepted,
		By: "alex", Note: "the pattern recurred " + sentinel,
	}); err != nil {
		t.Fatalf("decide: %v", err)
	}
	if len(capture.records) != 2 {
		t.Fatalf("the disposition store staged %d records, want 2", len(capture.records))
	}

	answer, err := json.Marshal(frontier.PublishedRecord{
		Schema: frontier.RecordSchema, Kind: frontier.PublishedReviewAnswer,
		ID: "dis-frontier", RootID: "dis-frontier",
		Subject:   frontier.Ref{Type: frontier.EntityHypothesis, ID: subject.ID},
		Answer:    &frontier.PublishedAnswer{Decision: frontier.DispositionAccept, Reviewer: "alex"},
		CreatedAt: time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC),
		Payload:   json.RawMessage(`{"note":"reviewed"}`),
	})
	if err != nil {
		t.Fatalf("encode the frontier review answer: %v", err)
	}
	closure := sharedcatalog.RunClosure{
		RunID: "run-disposition", DeploymentID: "d1", OriginInstanceID: "inst-b",
		Records: []sharedcatalog.StagedRecord{{
			RecordID: "dis-frontier", Kind: sharedcatalog.KindDisposition,
			Schema: frontier.RecordSchema, Payload: answer,
		}},
	}
	for i, staged := range capture.records {
		closure.Records = append(closure.Records, sharedcatalog.StagedRecord{
			RecordID: staged.EntityID, Kind: staged.Kind, Schema: staged.Schema,
			Payload: staged.Payload, Ordinal: int64(i + 1),
		})
	}
	closure.RecordCount = len(closure.Records)
	if _, err := sharedcatalog.SyncRun(ctx, h.db, h.store, h.ring, closure); err != nil {
		t.Fatalf("commit the disposition closure: %v", err)
	}

	rows, err := h.reader.RecordsWithContent(ctx, sharedcatalog.RecordFilter{})
	if err != nil {
		t.Fatalf("RecordsWithContent: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("read %d records, want 3", len(rows))
	}
	summaries := map[string]string{}
	for _, row := range rows {
		if row.Unopened != "" {
			t.Fatalf("record %s did not open: %s", row.Record.RecordID, row.Unopened)
		}
		summary, err := row.Summary()
		if err != nil {
			t.Fatalf("summarize %s: %v", row.Record.RecordID, err)
		}
		summaries[row.Record.RecordID] = summary
		switch row.Record.RecordID {
		case "dis-frontier":
			if row.Disposition != nil || row.Published == nil {
				t.Fatalf("a frontier review answer left its own decoder: %+v", row)
			}
		default:
			if row.Disposition == nil || row.Published != nil {
				t.Fatalf("disposition %s opened without its own projection: %+v",
					row.Record.RecordID, row)
			}
		}
	}
	// The summary and the note are the sealed halves, so a line carrying the
	// sentinel is proof it was derived from a decrypted object rather than
	// from the plaintext catalog columns.
	proposed := summaries[action.ID]
	if !strings.Contains(proposed, "develop-further on hypothesis "+subject.ID) ||
		!strings.Contains(proposed, sentinel) {
		t.Errorf("proposed action summary = %q; it must name the action, the record and the sealed prose", proposed)
	}
	var ruled string
	for id, summary := range summaries {
		if id != action.ID && id != "dis-frontier" {
			ruled = summary
		}
	}
	if !strings.Contains(ruled, "accepted disposition "+action.ID) ||
		!strings.Contains(ruled, sentinel) {
		t.Errorf("ruling summary = %q; it must name the ruling, the action and the sealed note", ruled)
	}
	if !strings.Contains(summaries["dis-frontier"], "accept on hypothesis "+subject.ID) {
		t.Errorf("frontier review answer summary = %q", summaries["dis-frontier"])
	}
}
