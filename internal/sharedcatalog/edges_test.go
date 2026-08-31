package sharedcatalog

import (
	"context"
	"strings"
	"testing"
)

// These tests defend migrations/0008's bargain (issue #113): an edge's shape
// travels in the clear so the fleet's citation graph is navigable without a
// payload key, and an edge's note does not travel at all.
//
// Both halves need proving, and the second is the one that could rot silently.
// A leak scan that finds nothing proves nothing unless the table it is scanning
// has rows in it, so every case here publishes a real edge whose note carries
// the suite's sentinel.

// edgeClosure builds a one-record run whose record is an edge: a link record
// whose payload holds the note and whose plaintext half holds the citation.
func edgeClosure(runID, recordID string, edge RecordEdge, note string) RunClosure {
	return RunClosure{
		RunID:            runID,
		DeploymentID:     "d1",
		OriginInstanceID: "inst-a",
		RecordCount:      1,
		Records: []StagedRecord{{
			RecordID: recordID,
			Kind:     KindLink,
			Schema:   1,
			Ordinal:  0,
			Payload:  []byte(`{"schema":1,"note":"` + note + `"}`),
			Edge:     &edge,
		}},
	}
}

// sampleEdge is a citation between two synthetic analysis records. Both
// endpoint ids are opaque by construction, which is what the plaintext columns
// are allowed to carry.
func sampleEdge() RecordEdge {
	return RecordEdge{
		Kind:     EdgeEvidence,
		FromKind: "finding",
		FromID:   "fnd_0f1e2d3c4b5a69788796a5b4c3d2e1f0",
		ToKind:   "session",
		ToID:     strings.Repeat("ab", 32),
	}
}

// The point of the whole table: a host holding only the catalog credential can
// read what cites what, across every machine, and cannot read a word of why.
func TestEdgePublishesItsShapeAndSealsItsNote(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	edge := sampleEdge()
	mustSync(t, db, store, ring, edgeClosure("run-edge", "edge-1", edge, sentinel))

	got, err := RecordEdges(ctx, db, EdgeFilter{DeploymentID: "d1"})
	if err != nil {
		t.Fatalf("RecordEdges: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read %d edges, want 1", len(got))
	}
	read := got[0]
	if read.Kind != EdgeEvidence {
		t.Errorf("edge kind = %q, want %q", read.Kind, EdgeEvidence)
	}
	if read.From.Kind != edge.FromKind || read.From.ID != edge.FromID {
		t.Errorf("edge starts at %s, want %s:%s", read.From, edge.FromKind, edge.FromID)
	}
	if read.To.Kind != edge.ToKind || read.To.ID != edge.ToID {
		t.Errorf("edge ends at %s, want %s:%s", read.To, edge.ToKind, edge.ToID)
	}
	if !read.Committed() {
		t.Errorf("edge sync state = %q, want committed", read.SyncState)
	}
	if read.CommittedAt == nil {
		t.Error("a committed edge carries no commit time")
	}
	// Attribution comes off the run, exactly as a record's does: the edge says
	// which machine asserted it without the note saying anything.
	if read.OriginInstanceID != "inst-a" || read.HostID != "h1" {
		t.Errorf("edge attributed to instance %q on host %q, want inst-a on h1",
			read.OriginInstanceID, read.HostID)
	}

	// The note is not in PostgreSQL. The scan is schema-reflecting, so it
	// covers analysis_edges without being told the table exists.
	for _, hit := range scanSchemaForText(t, db, sentinel) {
		t.Errorf("an edge's note is in PostgreSQL in %s", hit)
	}

	// Non-vacuity for the new table specifically. The same scan must FIND the
	// endpoint id, or its silence about the note says nothing about
	// analysis_edges - it would be the silence of a table with no rows.
	hits := scanSchemaForText(t, db, edge.FromID)
	var found bool
	for _, hit := range hits {
		if strings.HasPrefix(hit, "analysis_edges.from_id") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the plaintext scan did not find the edge's own endpoint in analysis_edges.from_id "+
			"(hits: %v); it is not reading this table, so its verdict about the note is vacuous", hits)
	}

	// And the note is readable where it belongs: in the sealed object, with a
	// key, on this machine.
	records, err := AnalysisRecords(ctx, db, "run-edge")
	if err != nil {
		t.Fatalf("AnalysisRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("run holds %d records, want 1", len(records))
	}
	object, err := store.Get(ctx, records[0].ObjectKey)
	if err != nil {
		t.Fatalf("read the sealed object: %v", err)
	}
	if contains(object, sentinel) {
		t.Error("the stored object holds the note in the clear")
	}
	plaintext, err := OpenRecord(ctx, store, ring, records[0])
	if err != nil {
		t.Fatalf("open the edge record: %v", err)
	}
	if !contains(plaintext, sentinel) {
		t.Error("the decrypted edge carries no note; the assertions above are vacuous")
	}
}

// The projection the fleet edge read selects must be plaintext-eligible by the
// same gate the record read passes, and pointed at the query rather than at a
// comment about the query.
func TestEdgeProjectionIsPlaintextEligible(t *testing.T) {
	for table, columns := range EdgeProjection() {
		if err := AssertPhaseBPlaintext(table, columns...); err != nil {
			t.Errorf("the fleet edge projection is not plaintext-eligible: %v", err)
		}
	}
	// Every column of the new table is covered, so a migration adding one
	// cannot slip past the gate by not being selected yet.
	for column := range allowlist["analysis_edges"] {
		if err := AssertPhaseBPlaintext("analysis_edges", column); err != nil {
			t.Errorf("analysis_edges.%s: %v", column, err)
		}
	}
	// The narrower Phase B question still bites on this table: a column of a
	// class the general allowlist admits and Phase B refuses fails here.
	if err := AssertPhaseBPlaintext("analysis_edges", "note"); err == nil {
		t.Error("the Phase B gate accepted a column no allowlist entry describes")
	}
}

// A staged edge the catalog could not carry must be refused before anything is
// written, because analysis_records is insert-only and a malformed record
// cannot be corrected there.
func TestSyncRunRefusesMalformedEdges(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	cases := []struct {
		name string
		mut  func(*RunClosure)
	}{
		{
			name: "an unknown relation kind",
			mut:  func(c *RunClosure) { c.Records[0].Edge.Kind = "cites" },
		},
		{
			name: "an endpoint with no namespace",
			mut:  func(c *RunClosure) { c.Records[0].Edge.FromKind = "" },
		},
		{
			name: "a namespace that is prose rather than a vocabulary value",
			mut:  func(c *RunClosure) { c.Records[0].Edge.FromKind = "the session it came from" },
		},
		{
			name: "an edge to itself",
			mut: func(c *RunClosure) {
				c.Records[0].Edge.ToKind = c.Records[0].Edge.FromKind
				c.Records[0].Edge.ToID = c.Records[0].Edge.FromID
			},
		},
		{
			// The pairing matters: endpoint columns on a record that is not a
			// link would file a citation against a hypothesis, and
			// analysis_edges has no column that says the row is wrong.
			name: "endpoints on a record that is not a link",
			mut:  func(c *RunClosure) { c.Records[0].Kind = KindHypothesis },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edge := sampleEdge()
			closure := edgeClosure("run-bad", "edge-bad", edge, sentinel)
			tc.mut(&closure)
			if _, err := SyncRun(ctx, db, store, ring, closure); err == nil {
				t.Fatal("SyncRun accepted an edge the catalog cannot carry")
			}
			if store.putCount() != 0 {
				t.Errorf("a refused edge wrote %d objects; refusal must precede sealing",
					store.putCount())
			}
			var rows int
			if err := db.QueryRowContext(ctx,
				`SELECT count(*) FROM analysis_edges`).Scan(&rows); err != nil {
				t.Fatalf("count edges: %v", err)
			}
			if rows != 0 {
				t.Errorf("a refused edge left %d rows", rows)
			}
		})
	}
}

// An edge is append-only in the database, not merely in the writer. A wrong
// citation is answered by a later edge (SPEC.md §4.7), and a statement that
// could rewrite one would make the graph's history a claim rather than a
// record.
func TestPublishedEdgesAreAppendOnly(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	mustSync(t, db, store, ring, edgeClosure("run-frozen", "edge-frozen", sampleEdge(), sentinel))

	if _, err := db.ExecContext(ctx,
		`UPDATE analysis_edges SET edge_kind = 'refines' WHERE record_id = 'edge-frozen'`); err == nil {
		t.Error("an edge's relation kind was rewritten")
	}
	if _, err := db.ExecContext(ctx,
		`DELETE FROM analysis_edges WHERE record_id = 'edge-frozen'`); err == nil {
		t.Error("an edge was deleted")
	}

	// A second sync of the same run is a no-op rather than a second edge row:
	// the record is already present, so nothing is re-sealed and nothing is
	// re-inserted.
	res := mustSync(t, db, store, ring, edgeClosure("run-frozen", "edge-frozen", sampleEdge(), sentinel))
	if res.ObjectsWritten != 0 || res.RecordsCommitted != 0 {
		t.Errorf("a replayed edge wrote %d objects and %d rows, want none",
			res.ObjectsWritten, res.RecordsCommitted)
	}
	edges, err := RecordEdges(ctx, db, EdgeFilter{DeploymentID: "d1"})
	if err != nil {
		t.Fatalf("RecordEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("the replay left %d edges, want 1", len(edges))
	}
}

// The read is what makes the graph navigable, so its filters have to narrow the
// two questions a record page asks and refuse a query it cannot answer.
func TestRecordEdgesNarrowsByEndpointAndKind(t *testing.T) {
	db := newInternalDB(t)
	seedFleet(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	const session = "session"
	first := RecordEdge{
		Kind: EdgeEvidence, FromKind: "finding", FromID: "fnd_1",
		ToKind: session, ToID: strings.Repeat("11", 32),
	}
	second := RecordEdge{
		Kind: EdgeSupersedes, FromKind: "finding", FromID: "fnd_2",
		ToKind: "finding", ToID: "fnd_1",
	}
	mustSync(t, db, store, ring, edgeClosure("run-e1", "edge-e1", first, sentinel))
	mustSync(t, db, store, ring, edgeClosure("run-e2", "edge-e2", second, sentinel))

	// What cites fnd_1: the backlink question.
	back, err := RecordEdges(ctx, db, EdgeFilter{
		DeploymentID: "d1",
		To:           EdgeRef{Kind: "finding", ID: "fnd_1"},
	})
	if err != nil {
		t.Fatalf("RecordEdges: %v", err)
	}
	if len(back) != 1 || back[0].RecordID != "edge-e2" {
		t.Fatalf("backlinks of fnd_1 = %+v, want only edge-e2", back)
	}

	// What fnd_1 cites: the same table from the other side.
	out, err := RecordEdges(ctx, db, EdgeFilter{
		DeploymentID: "d1",
		From:         EdgeRef{Kind: "finding", ID: "fnd_1"},
	})
	if err != nil {
		t.Fatalf("RecordEdges: %v", err)
	}
	if len(out) != 1 || out[0].RecordID != "edge-e1" {
		t.Fatalf("outgoing edges of fnd_1 = %+v, want only edge-e1", out)
	}

	byKind, err := RecordEdges(ctx, db, EdgeFilter{
		DeploymentID: "d1",
		Kinds:        []EdgeKind{EdgeSupersedes},
	})
	if err != nil {
		t.Fatalf("RecordEdges: %v", err)
	}
	if len(byKind) != 1 || byKind[0].Kind != EdgeSupersedes {
		t.Fatalf("edges of kind supersedes = %+v, want only edge-e2", byKind)
	}

	// Another deployment's graph is not this one's, and a filter naming no
	// deployment is refused rather than answered fleet-wide.
	other, err := RecordEdges(ctx, db, EdgeFilter{DeploymentID: "d2"})
	if err != nil {
		t.Fatalf("RecordEdges for an empty deployment: %v", err)
	}
	if len(other) != 0 {
		t.Errorf("another deployment's read returned %d edges", len(other))
	}
	if _, err := RecordEdges(ctx, db, EdgeFilter{}); err == nil {
		t.Error("an edge read with no deployment was answered")
	}
	if _, err := RecordEdges(ctx, db, EdgeFilter{
		DeploymentID: "d1", Kinds: []EdgeKind{"cites"},
	}); err == nil {
		t.Error("an edge read with an unknown relation kind was answered")
	}
}
