package reference

import (
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// These tests are the publication matrix: what the fleet holds after an edge is
// written, and what it holds after each way the write can be interrupted.
//
// The property under test is convergence without duplication. An edge is
// staged inside the transaction that makes it durable and published immediately
// afterwards, so every interruption leaves the edge durable, visibly
// pending-sync, and publishable later - and `babel sync` must then produce
// exactly one record and exactly one citation row, however many times it runs.

// The whole bargain in one test: the citation reaches PostgreSQL in the clear
// and the note does not.
func TestAppendPublishesTheShapeAndSealsTheNote(t *testing.T) {
	f := newPublishingFixture(t, false)
	ctx := t.Context()

	edge, err := f.store.Append(ctx, operatorAsserted(sentinel))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(f.failures) != 0 {
		t.Fatalf("publication reported failures: %v", f.failures)
	}
	if got := f.syncState(t, edge.ID); got != sharedcatalog.SyncCommitted {
		t.Fatalf("journal state = %q, want %q", got, sharedcatalog.SyncCommitted)
	}

	edges := f.remoteEdges(t)
	if len(edges) != 1 {
		t.Fatalf("the catalog holds %d edges, want 1", len(edges))
	}
	published := edges[0]
	if published.RecordID != edge.ID {
		t.Errorf("published edge id = %q, want %q", published.RecordID, edge.ID)
	}
	if published.Kind != sharedcatalog.EdgeEvidence {
		t.Errorf("published kind = %q, want %q", published.Kind, sharedcatalog.EdgeEvidence)
	}
	if published.From.Kind != "finding" || published.From.ID != testFindingID {
		t.Errorf("published source = %s, want finding:%s", published.From, testFindingID)
	}
	if published.To.Kind != "session" || published.To.ID != testSessionKey {
		t.Errorf("published target = %s, want session:%s", published.To, testSessionKey)
	}
	if !published.Committed() {
		t.Errorf("published edge state = %q, want committed", published.SyncState)
	}
	if published.HostID != testHost {
		t.Errorf("published edge attributed to host %q, want %q", published.HostID, testHost)
	}

	// The note is nowhere in PostgreSQL, and the staged copy of it is gone from
	// the journal now that the record has committed.
	for _, hit := range scanForText(t, f.catalog, sentinel) {
		t.Errorf("an edge's note reached PostgreSQL in %s", hit)
	}
	if got := f.stagedEdgeRows(t); got != 0 {
		t.Errorf("the journal still holds %d staged edge rows after commit", got)
	}

	// And it is readable where it belongs: sealed in the object, with a key.
	records, err := sharedcatalog.AnalysisRecords(ctx, f.catalog, edge.ID)
	if err != nil {
		t.Fatalf("read the published record: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("the edge's closure holds %d records, want 1", len(records))
	}
	if records[0].Kind != sharedcatalog.KindLink {
		t.Errorf("an edge published as kind %q, want %q", records[0].Kind, sharedcatalog.KindLink)
	}
	plaintext, err := sharedcatalog.OpenRecord(ctx, f.objects, f.ring, records[0])
	if err != nil {
		t.Fatalf("open the published edge: %v", err)
	}
	if !contains(plaintext, sentinel) {
		t.Error("the decrypted edge carries no note; the leak check above is vacuous")
	}
}

// The crash this whole staging design exists for: the durable write committed
// and the process died before it could publish. `babel sync` must finish the
// job, once, with the endpoints the write validated - which it can only do
// because they were staged rather than left to be re-derived from a sealed
// payload.
func TestAnUnpublishedEdgeConvergesOnTheNextSync(t *testing.T) {
	f := newPublishingFixture(t, true)
	ctx := t.Context()

	edge, err := f.store.Append(ctx, operatorAsserted(sentinel))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if f.deferring.deferred != 1 {
		t.Fatalf("the fixture skipped %d publications, want the one it simulated",
			f.deferring.deferred)
	}

	// The edge is durable and visibly owed to the fleet, and nothing has
	// reached PostgreSQL.
	if got := f.syncState(t, edge.ID); got != sharedcatalog.SyncPending {
		t.Fatalf("journal state = %q, want %q", got, sharedcatalog.SyncPending)
	}
	if got := f.remoteRecordCount(t); got != 0 {
		t.Fatalf("the catalog holds %d records before any sync", got)
	}
	if got := f.stagedEdgeRows(t); got != 1 {
		t.Fatalf("the journal holds %d staged edge rows, want the one that is owed", got)
	}

	rep := f.retry(t)
	if rep.RunsCommitted != 1 || rep.RunsPending != 0 {
		t.Fatalf("sync committed %d runs with %d still pending, want 1 and 0",
			rep.RunsCommitted, rep.RunsPending)
	}
	if rep.Committed[sharedcatalog.KindLink] != 1 {
		t.Errorf("sync committed %d link records, want 1", rep.Committed[sharedcatalog.KindLink])
	}

	edges := f.remoteEdges(t)
	if len(edges) != 1 {
		t.Fatalf("the catalog holds %d edges after one sync, want exactly 1", len(edges))
	}
	if edges[0].From.ID != testFindingID || edges[0].To.ID != testSessionKey {
		t.Errorf("the recovered edge cites %s -> %s", edges[0].From, edges[0].To)
	}
	if got := f.syncState(t, edge.ID); got != sharedcatalog.SyncCommitted {
		t.Errorf("journal state after sync = %q, want %q", got, sharedcatalog.SyncCommitted)
	}

	// A second sync is a no-op: every record is present, so nothing is sealed,
	// nothing is inserted, and the graph does not double.
	objects := f.objects.count()
	again := f.retry(t)
	if again.RunsCommitted != 0 || again.ObjectsWritten != 0 {
		t.Errorf("a repeated sync committed %d runs and wrote %d objects, want none",
			again.RunsCommitted, again.ObjectsWritten)
	}
	if got := f.objects.count(); got != objects {
		t.Errorf("a repeated sync left %d objects, want the %d it started with", got, objects)
	}
	if got := len(f.remoteEdges(t)); got != 1 {
		t.Fatalf("the catalog holds %d edges after two syncs, want 1", got)
	}
	if got := f.remoteRecordCount(t); got != 1 {
		t.Errorf("the catalog holds %d records after two syncs, want 1", got)
	}
}

// The other half of convergence: the emitter, not the sync, is what retries.
// An emission site that crashed before its own commit re-derives the same edge
// on the next run, and the second append must neither duplicate the durable row
// nor publish a second record.
func TestReAppendAfterACrashPublishesNothingTwice(t *testing.T) {
	f := newPublishingFixture(t, true)
	ctx := t.Context()

	first, err := f.store.Append(ctx, operatorAsserted(sentinel))
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	f.retry(t)

	// The emitter runs again and asserts the same citation.
	second, err := f.store.Append(ctx, operatorAsserted(sentinel))
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("the re-append minted %s, want the published %s", second.ID, first.ID)
	}
	if got := f.edgeRows(t); got != 1 {
		t.Errorf("the durable file holds %d edges, want 1", got)
	}
	f.retry(t)
	if got := len(f.remoteEdges(t)); got != 1 {
		t.Errorf("the catalog holds %d edges after a re-append, want 1", got)
	}
	if got := f.remoteRecordCount(t); got != 1 {
		t.Errorf("the catalog holds %d records after a re-append, want 1", got)
	}
	// A re-append stages nothing, so the journal has nothing new to release.
	if got := f.stagedEdgeRows(t); got != 0 {
		t.Errorf("a re-append left %d staged edge rows", got)
	}
}

// Publication is never a write-path dependency (SPEC.md §6.5). An unreachable
// object store must leave the edge durable, the append successful, and the
// citation owed to the fleet - and the next sync, after the outage, must
// publish exactly one of it.
func TestAnOutageLeavesTheEdgeDurableAndOwed(t *testing.T) {
	f := newPublishingFixture(t, false)
	ctx := t.Context()

	f.objects.refuseEverything(errors.New("object store is unreachable"))
	edge, err := f.store.Append(ctx, operatorAsserted(sentinel))
	if err != nil {
		t.Fatalf("append during an outage: %v", err)
	}
	if len(f.failures) == 0 {
		t.Error("an outage published nothing and reported nothing; it must be visible")
	}
	if got := f.edgeRows(t); got != 1 {
		t.Fatalf("the durable file holds %d edges after an outage, want 1", got)
	}
	if got := f.syncState(t, edge.ID); got != sharedcatalog.SyncPending {
		t.Errorf("journal state during an outage = %q, want %q", got, sharedcatalog.SyncPending)
	}
	if got := f.remoteRecordCount(t); got != 0 {
		t.Errorf("the catalog holds %d records during an outage", got)
	}

	f.objects.acceptEverything()
	rep := f.retry(t)
	if rep.RunsCommitted != 1 {
		t.Fatalf("the sync after the outage committed %d runs, want 1", rep.RunsCommitted)
	}
	edges := f.remoteEdges(t)
	if len(edges) != 1 {
		t.Fatalf("the catalog holds %d edges after the outage cleared, want 1", len(edges))
	}
	if edges[0].RecordID != edge.ID {
		t.Errorf("published edge %q, want the durable %q", edges[0].RecordID, edge.ID)
	}
}

// An edge a run asserted belongs to that run's closure, so it publishes when
// the run does rather than opening a closure of its own. An operator's edge is
// its own closure of one, declared in the writer's transaction, because nobody
// resumes an operator's act.
func TestClosureFollowsTheActor(t *testing.T) {
	f := newPublishingFixture(t, false)
	ctx := t.Context()

	// The run's closure is still open, so the run edge stages and publishes
	// nothing yet.
	runEdge, err := f.store.Append(ctx, evidence(sentinel))
	if err != nil {
		t.Fatalf("append a run edge: %v", err)
	}
	if got := f.remoteRecordCount(t); got != 0 {
		t.Fatalf("a run's edge published on its own: the catalog holds %d records", got)
	}
	if got := f.syncState(t, runEdge.ID); got != sharedcatalog.SyncPending {
		t.Errorf("a run's edge is %q rather than staged and waiting for its run", got)
	}

	// An operator's edge, by contrast, is complete on its own and publishes
	// immediately.
	operatorEdge := Edge{
		Kind:      KindSupersedes,
		From:      RecordRef{Kind: "finding", ID: otherFindingID},
		To:        RecordRef{Kind: "finding", ID: testFindingID},
		ActorKind: ActorOperator,
		ActorRef:  "alex",
		Note:      sentinel,
	}
	appended, err := f.store.Append(ctx, operatorEdge)
	if err != nil {
		t.Fatalf("append an operator edge: %v", err)
	}
	if got := f.syncState(t, appended.ID); got != sharedcatalog.SyncCommitted {
		t.Fatalf("an operator's edge is %q, want %q", got, sharedcatalog.SyncCommitted)
	}
	edges := f.remoteEdges(t)
	if len(edges) != 1 || edges[0].RecordID != appended.ID {
		t.Fatalf("the catalog holds %+v, want only the operator's edge", edges)
	}

	// The run's edge is still owed, and `babel sync` reports it as undeclared
	// rather than losing it: its run has not ended, and a closure declared at
	// whatever it happened to reach would publish a run that later grows.
	rep := f.retry(t)
	if rep.Undeclared != 1 {
		t.Errorf("sync reports %d undeclared records, want the run's edge", rep.Undeclared)
	}
	if rep.Pending[sharedcatalog.KindLink] != 1 {
		t.Errorf("sync reports %d pending link records, want 1", rep.Pending[sharedcatalog.KindLink])
	}

	// When the run ends, its closure is declared and the edge publishes with
	// the run's other output - once, with the endpoints it was written with.
	f.endRun(t, "run-fixture")
	edges = f.remoteEdges(t)
	if len(edges) != 2 {
		t.Fatalf("the catalog holds %d edges after the run ended, want 2", len(edges))
	}
	if got := f.syncState(t, runEdge.ID); got != sharedcatalog.SyncCommitted {
		t.Errorf("the run's edge is %q after the run ended, want %q",
			got, sharedcatalog.SyncCommitted)
	}
	if got := f.remoteRecordCount(t); got != 2 {
		t.Errorf("the catalog holds %d records, want the operator's edge and the run's", got)
	}
	if got := f.stagedEdgeRows(t); got != 0 {
		t.Errorf("the journal still holds %d staged edge rows once everything committed", got)
	}
}
