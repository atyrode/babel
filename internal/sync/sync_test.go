package sync

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// What this file defends is SPEC.md §6.5's ordering across two stores and the
// two crash windows it leaves. Both are named explicitly rather than described
// as "failure handling", because they behave differently and only one of them
// leaves anything remote:
//
//	killed between the object write and the PostgreSQL commit: no row names the
//	object, the run stays pending-sync, and the retry converges on exactly one
//	row per record;
//
//	killed after the PostgreSQL commit and before the local flip: the fleet
//	already has the run, and the retry writes no object, inserts no row, and
//	flips only.
//
// The failures are injected at the boundary rather than simulated inside the
// protocol, in the same style as internal/sharedcatalog's suite: the object
// store's read-back hook is the last instruction before the row insert, so
// cancelling the context there is a genuine kill at that exact point.

func TestClosureCommitsObjectFirstDatabaseLastThenFlipsLocally(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageAndDeclare(t, "run-a",
		record("run-a", "run-a-hyp", sharedcatalog.KindHypothesis),
		record("run-a", "run-a-obs", sharedcatalog.KindObservation),
		record("run-a", "run-a-rcp", sharedcatalog.KindReceipt))

	// Before publication the records are durable and visibly owed.
	if got := f.journalState(t, "run-a-hyp"); got != sharedcatalog.SyncPending {
		t.Fatalf("staged record state = %q, want %q", got, sharedcatalog.SyncPending)
	}

	if err := f.pub.CommitInline(ctx, Closure{RunID: "run-a"}); err != nil {
		t.Fatalf("commit inline: %v", err)
	}
	if len(f.failures) != 0 {
		t.Fatalf("a clean publication reported %d diagnostics: %v", len(f.failures), f.failures)
	}

	run := f.remoteRun(t, "run-a")
	if run.SyncState != sharedcatalog.SyncCommitted {
		t.Errorf("remote run state = %q, want %q", run.SyncState, sharedcatalog.SyncCommitted)
	}
	if run.RecordCount != 3 || run.RecordsPresent != 3 {
		t.Errorf("remote run holds %d of %d records, want 3 of 3", run.RecordsPresent, run.RecordCount)
	}
	if run.OriginInstanceID != testInstance || run.DeploymentID != testDeployment {
		t.Errorf("remote run attributed to %s/%s", run.DeploymentID, run.OriginInstanceID)
	}

	rows := f.remoteRecords(t, "run-a")
	if len(rows) != 3 {
		t.Fatalf("remote records = %d, want 3", len(rows))
	}
	// Closure order is stage order, assigned by the journal so no caller can
	// compute a conflicting ordinal.
	wantOrder := []string{"run-a-hyp", "run-a-obs", "run-a-rcp"}
	for i, row := range rows {
		if row.RecordID != wantOrder[i] || row.Ordinal != int64(i) {
			t.Errorf("record %d = %s at ordinal %d, want %s at %d",
				i, row.RecordID, row.Ordinal, wantOrder[i], i)
		}
		if row.CiphertextSize == 0 || row.ObjectDigest == "" || row.KeyID == "" {
			t.Errorf("record %s names no sealed object: %+v", row.RecordID, row)
		}
	}

	// The local flip follows the remote commit, and the payload copies are
	// released with it.
	for _, id := range wantOrder {
		if got := f.journalState(t, id); got != sharedcatalog.SyncCommitted {
			t.Errorf("journal state for %s = %q, want %q", id, got, sharedcatalog.SyncCommitted)
		}
	}
	if n := f.payloadRows(t); n != 0 {
		t.Errorf("journal still holds %d staged payloads after a full commit", n)
	}
	if f.journalHoldsSentinel(t) {
		t.Error("a committed record's plaintext payload is still in the durable journal")
	}

	// Nothing plaintext reached PostgreSQL, and nothing plaintext reached the
	// object store either: the payload travels sealed.
	if f.store.contains(sentinel) {
		t.Error("plaintext payload reached the object store")
	}
	for _, hit := range scanCatalogForText(t, f, sentinel) {
		t.Errorf("plaintext sentinel reached PostgreSQL in %s", hit)
	}
}

// A kill between the verified object write and the row that names it is the
// window migration 0003's ordering exists for. The context is cancelled inside
// the object store's read-back, so the insert that follows meets a cancelled
// connection - a real failure at the exact instruction, not a simulated one.
func TestKilledBetweenObjectWriteAndDatabaseCommitRetryConverges(t *testing.T) {
	f := newFixture(t)

	f.stageAndDeclare(t, "run-b",
		record("run-b", "run-b-one", sharedcatalog.KindHypothesis),
		record("run-b", "run-b-two", sharedcatalog.KindFinding))

	kill, cancel := context.WithCancel(t.Context())
	f.store.onGet = func(key string) { cancel() }
	if err := f.pub.CommitInline(kill, Closure{RunID: "run-b"}); err != nil {
		t.Fatalf("commit inline reported a caller bug: %v", err)
	}
	f.store.onGet = nil

	// The write path survived: a publication failure is a diagnostic, never an
	// error the command fails on.
	if len(f.failures) != 1 {
		t.Fatalf("a killed publication reported %d diagnostics, want 1: %v", len(f.failures), f.failures)
	}

	// An object exists and no row names it. That is the harmless direction, and
	// the run is not visible as committed to anyone.
	if f.store.objectCount() != 1 {
		t.Errorf("object store holds %d objects, want the one that was written", f.store.objectCount())
	}
	run := f.remoteRun(t, "run-b")
	if run.SyncState != sharedcatalog.SyncPending {
		t.Errorf("remote run state = %q, want %q", run.SyncState, sharedcatalog.SyncPending)
	}
	if run.CommittedAt != nil {
		t.Error("a pending run carries a commit time")
	}
	if run.RecordsPresent != 0 {
		t.Errorf("remote run holds %d records, want none: no row may name an unwritten object", run.RecordsPresent)
	}
	if !runIsInvisible(t, f, "run-b") {
		t.Error("a pending run is offered as globally reviewable")
	}
	for _, id := range []string{"run-b-one", "run-b-two"} {
		if got := f.journalState(t, id); got != sharedcatalog.SyncPending {
			t.Errorf("journal state for %s = %q, want %q", id, got, sharedcatalog.SyncPending)
		}
	}

	// The retry converges, and it converges on exactly one row per record.
	rep, err := f.pub.Retry(t.Context())
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if rep.RunsCommitted != 1 || rep.RunsPending != 0 {
		t.Errorf("retry committed %d runs and left %d pending, want 1 and 0",
			rep.RunsCommitted, rep.RunsPending)
	}
	if got := rep.Committed[sharedcatalog.KindHypothesis]; got != 1 {
		t.Errorf("retry committed %d hypotheses, want 1", got)
	}
	if got := rep.Committed[sharedcatalog.KindFinding]; got != 1 {
		t.Errorf("retry committed %d findings, want 1", got)
	}

	rows := f.remoteRecords(t, "run-b")
	if len(rows) != 2 {
		t.Fatalf("remote records = %d, want exactly 2: a retry must not duplicate a row", len(rows))
	}
	seen := map[string]int{}
	for _, row := range rows {
		seen[row.RecordID]++
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("record %s has %d rows", id, n)
		}
	}

	// The superseded object is left behind rather than rewritten, which is what
	// content-addressed keys buy: the retry seals again under a fresh nonce and
	// writes a new object beside the orphan instead of overwriting one a
	// committed row might already name.
	if f.store.objectCount() != 3 {
		t.Errorf("object store holds %d objects, want 3: two committed and one orphan", f.store.objectCount())
	}
	if f.remoteRun(t, "run-b").SyncState != sharedcatalog.SyncCommitted {
		t.Error("the run did not reach committed after a converging retry")
	}
}

// A kill after the PostgreSQL commit and before the local flip leaves the fleet
// holding the run and this machine believing it does not. The retry must
// recognise that rather than republish: no object, no row, flip only.
//
// The state is produced by committing the closure through internal/sharedcatalog
// directly, which is exactly what the publisher did before it died, and leaving
// the journal untouched.
func TestKilledAfterDatabaseCommitBeforeLocalFlipRetryFlipsOnly(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageAndDeclare(t, "run-c",
		record("run-c", "run-c-one", sharedcatalog.KindHypothesis),
		record("run-c", "run-c-two", sharedcatalog.KindProposal))

	staged, err := f.journal.pendingRecords(ctx, "run-c")
	if err != nil {
		t.Fatalf("read staged records: %v", err)
	}
	res, err := sharedcatalog.SyncRun(ctx, f.catalog, f.store, f.ring, sharedcatalog.RunClosure{
		RunID:            "run-c",
		DeploymentID:     testDeployment,
		OriginInstanceID: testInstance,
		RecordCount:      len(staged),
		Records:          staged,
	})
	if err != nil {
		t.Fatalf("seed the remote commit: %v", err)
	}
	if res.State != sharedcatalog.SyncCommitted {
		t.Fatalf("seeded state = %q, want %q", res.State, sharedcatalog.SyncCommitted)
	}
	putsBefore := f.store.putCount()

	// The journal still says pending, which is the honest local reading: this
	// machine has no way to know the transaction committed.
	if got := f.journalState(t, "run-c-one"); got != sharedcatalog.SyncPending {
		t.Fatalf("journal state before the retry = %q, want %q", got, sharedcatalog.SyncPending)
	}

	rep, err := f.pub.Retry(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if rep.ObjectsWritten != 0 {
		t.Errorf("retry wrote %d objects, want 0: every record was already recorded", rep.ObjectsWritten)
	}
	if got := f.store.putCount() - putsBefore; got != 0 {
		t.Errorf("retry put %d objects into the store, want 0", got)
	}
	if rep.RunsCommitted != 1 || rep.RunsPending != 0 {
		t.Errorf("retry committed %d runs and left %d pending, want 1 and 0",
			rep.RunsCommitted, rep.RunsPending)
	}
	if len(rep.Pending) != 0 {
		t.Errorf("records still pending after the flip: %v", rep.Pending)
	}
	for _, id := range []string{"run-c-one", "run-c-two"} {
		if got := f.journalState(t, id); got != sharedcatalog.SyncCommitted {
			t.Errorf("journal state for %s = %q, want %q", id, got, sharedcatalog.SyncCommitted)
		}
	}
	if n := f.payloadRows(t); n != 0 {
		t.Errorf("journal still holds %d staged payloads", n)
	}
	if rows := f.remoteRecords(t, "run-c"); len(rows) != 2 {
		t.Errorf("remote records = %d, want 2: the flip must not have inserted anything", len(rows))
	}
}

// An object store that refuses every write is the outage case. Nothing is lost,
// nothing is claimed, and the command that produced the record still succeeds.
func TestPublicationFailureLeavesEverythingPendingAndTheCommandSucceeds(t *testing.T) {
	f := newFixture(t)
	f.store.failPut = func(key string) error { return errors.New("cellar is unreachable") }

	f.stageAndDeclare(t, "run-d", record("run-d", "run-d-one", sharedcatalog.KindContext))

	if err := f.pub.CommitInline(t.Context(), Closure{RunID: "run-d"}); err != nil {
		t.Fatalf("an outage must not be an error on the write path, got: %v", err)
	}
	if len(f.failures) != 1 {
		t.Fatalf("reported %d diagnostics, want exactly one line: %v", len(f.failures), f.failures)
	}
	if got := f.failures[0].Error(); !strings.Contains(got, "run-d") {
		t.Errorf("the diagnostic does not name the run it is about: %q", got)
	}
	if got := f.journalState(t, "run-d-one"); got != sharedcatalog.SyncPending {
		t.Errorf("journal state = %q, want %q", got, sharedcatalog.SyncPending)
	}
	if f.store.objectCount() != 0 {
		t.Errorf("object store holds %d objects after a refused write", f.store.objectCount())
	}
	if !runIsInvisible(t, f, "run-d") {
		t.Error("a run whose objects never landed is offered as globally reviewable")
	}

	// And the retry, once the store answers again, completes it.
	f.store.failPut = nil
	rep, err := f.pub.Retry(t.Context())
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if rep.RunsCommitted != 1 || rep.ObjectsWritten != 1 {
		t.Errorf("retry committed %d runs writing %d objects, want 1 and 1",
			rep.RunsCommitted, rep.ObjectsWritten)
	}
	if got := f.journalState(t, "run-d-one"); got != sharedcatalog.SyncCommitted {
		t.Errorf("journal state after recovery = %q, want %q", got, sharedcatalog.SyncCommitted)
	}
}

// Records staged for a run that never declared a closure are pending and
// deliberately unpublishable. Reporting the count is what keeps that visible
// instead of looking like a sync that found nothing to do.
func TestUndeclaredClosureIsVisibleAndUnpublishable(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx := f.writerTx(t)
	if err := f.pub.StageTx(ctx, tx, record("run-e", "run-e-one", sharedcatalog.KindHypothesis)); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit writer transaction: %v", err)
	}

	rep, err := f.pub.Retry(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if rep.RunsCommitted != 0 || rep.ObjectsWritten != 0 {
		t.Errorf("an undeclared run published: %+v", rep)
	}
	if rep.Undeclared != 1 {
		t.Errorf("undeclared records = %d, want 1", rep.Undeclared)
	}
	if got := rep.Pending[sharedcatalog.KindHypothesis]; got != 1 {
		t.Errorf("pending hypotheses = %d, want 1", got)
	}
	if _, err := sharedcatalog.AnalysisRun(ctx, f.catalog, "run-e"); !errors.Is(err, sharedcatalog.ErrRunNotFound) {
		t.Errorf("an undeclared run reached the catalog: %v", err)
	}

	// Declaring it later - which is what resuming the run under the same id
	// does - publishes all of it.
	if err := f.pub.CommitInline(ctx, Closure{RunID: "run-e"}); err != nil {
		t.Fatalf("commit inline after declaration: %v", err)
	}
	if got := f.journalState(t, "run-e-one"); got != sharedcatalog.SyncCommitted {
		t.Errorf("journal state = %q, want %q", got, sharedcatalog.SyncCommitted)
	}
}

// Repeating a completed sync must cost presence checks and nothing else. This
// is what makes `babel sync` safe to run on a timer and safe to attach to the
// end of every archive push.
func TestRetryIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.stageAndDeclare(t, "run-f", record("run-f", "run-f-one", sharedcatalog.KindDisposition))
	if err := f.pub.CommitInline(ctx, Closure{RunID: "run-f"}); err != nil {
		t.Fatalf("commit inline: %v", err)
	}
	putsBefore := f.store.putCount()

	for i := range 3 {
		rep, err := f.pub.Retry(ctx)
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if rep.RunsCommitted != 0 || rep.ObjectsWritten != 0 || len(rep.Failures) != 0 {
			t.Errorf("retry %d did work on a settled journal: %+v", i, rep)
		}
		if rep.RunsPending != 0 || rep.Undeclared != 0 || len(rep.Pending) != 0 {
			t.Errorf("retry %d reports outstanding work: %+v", i, rep)
		}
	}
	if got := f.store.putCount() - putsBefore; got != 0 {
		t.Errorf("repeated syncs put %d objects", got)
	}
}

// Staging shares the writer's transaction, so a rolled-back durable write
// leaves nothing staged. The inverse - a committed record with no journal row -
// is the failure this property exists to make impossible.
func TestStagingSharesTheWritersTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx := f.writerTx(t)
	if err := f.pub.StageTx(ctx, tx, record("run-g", "run-g-one", sharedcatalog.KindHypothesis)); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("roll back writer transaction: %v", err)
	}
	if got := f.journalState(t, "run-g-one"); got != "" {
		t.Errorf("a rolled-back write left %q staged", got)
	}
	if n, err := f.journal.UndeclaredRecords(ctx); err != nil || n != 0 {
		t.Errorf("undeclared records = %d (%v), want 0", n, err)
	}
}

// A declared closure is closed. A record that tried to join one would leave a
// run whose declared count can never be met, permanently pending with no
// remedy, so the write is refused where it is still fixable.
func TestARecordCannotJoinAClosedClosure(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()
	f.stageAndDeclare(t, "run-h", record("run-h", "run-h-one", sharedcatalog.KindHypothesis))

	tx := f.writerTx(t)
	defer tx.Rollback()
	err := f.pub.StageTx(ctx, tx, record("run-h", "run-h-two", sharedcatalog.KindObservation))
	if !errors.Is(err, ErrClosureConflict) {
		t.Fatalf("staging into a closed run: %v, want ErrClosureConflict", err)
	}
}

// An entity id is global. Two different records given one id would attach a
// record to a lineage it does not belong to, which no retry resolves.
func TestOneEntityIDBelongsToOneRun(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx := f.writerTx(t)
	if err := f.pub.StageTx(ctx, tx, record("run-i", "shared-id", sharedcatalog.KindHypothesis)); err != nil {
		t.Fatalf("stage: %v", err)
	}
	// Re-staging the same pair is a no-op: a writer whose transaction was
	// retried re-derives it, and refusing that would make its crash window
	// unrecoverable.
	if err := f.pub.StageTx(ctx, tx, record("run-i", "shared-id", sharedcatalog.KindHypothesis)); err != nil {
		t.Fatalf("re-staging the same record must be a no-op, got: %v", err)
	}
	err := f.pub.StageTx(ctx, tx, record("run-j", "shared-id", sharedcatalog.KindHypothesis))
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("staging one id under two runs: %v, want ErrRecordConflict", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := f.pub.CommitInline(ctx, Closure{RunID: "run-i"}); err != nil {
		t.Fatalf("commit inline: %v", err)
	}
	if rows := f.remoteRecords(t, "run-i"); len(rows) != 1 {
		t.Errorf("remote records = %d, want 1", len(rows))
	}
}

// Staging refuses at stage time what the remote protocol would refuse at
// publish time. Anything else becomes a pending row with no remedy but an edit
// to the writer, discovered by an operator rather than by a test.
func TestStagingRefusesWhatTheCatalogCannotCarry(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	cases := []struct {
		name string
		rec  Record
		want string
	}{
		{"traversal in the entity id", Record{
			RunID: "run-k", EntityID: "../escape", Kind: sharedcatalog.KindHypothesis,
			Schema: 1, Payload: []byte("{}")}, "well-formed"},
		{"empty entity id", Record{
			RunID: "run-k", EntityID: "", Kind: sharedcatalog.KindHypothesis,
			Schema: 1, Payload: []byte("{}")}, "well-formed"},
		{"unknown kind", Record{
			RunID: "run-k", EntityID: "run-k-one", Kind: sharedcatalog.RecordKind("reality-fact"),
			Schema: 1, Payload: []byte("{}")}, "record kind"},
		{"no schema version", Record{
			RunID: "run-k", EntityID: "run-k-one", Kind: sharedcatalog.KindHypothesis,
			Schema: 0, Payload: []byte("{}")}, "schema version"},
		{"empty payload", Record{
			RunID: "run-k", EntityID: "run-k-one", Kind: sharedcatalog.KindHypothesis,
			Schema: 1, Payload: nil}, "empty payload"},
		{"payload over the bound", Record{
			RunID: "run-k", EntityID: "run-k-one", Kind: sharedcatalog.KindHypothesis,
			Schema: 1, Payload: make([]byte, maxPayloadBytes+1)}, "over the"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tx := f.writerTx(t)
			defer tx.Rollback()
			err := f.pub.StageTx(ctx, tx, tc.rec)
			if err == nil {
				t.Fatalf("staged %+v", tc.rec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain the refusal (want %q)", err, tc.want)
			}
		})
	}
}

// A nil Publisher is local-only mode: every method is a silent no-op, so a
// writer needs no branch beyond the nil check it would write anyway, and a
// local deployment stages nothing.
func TestNilPublisherIsLocalMode(t *testing.T) {
	var p *Publisher
	ctx := context.Background()
	if err := p.StageTx(ctx, nil, Record{}); err != nil {
		t.Errorf("StageTx on a nil publisher: %v", err)
	}
	if err := p.DeclareTx(ctx, nil, Closure{}); err != nil {
		t.Errorf("DeclareTx on a nil publisher: %v", err)
	}
	if err := p.CommitInline(ctx, Closure{}); err != nil {
		t.Errorf("CommitInline on a nil publisher: %v", err)
	}
	rep, err := p.Retry(ctx)
	if err != nil {
		t.Errorf("Retry on a nil publisher: %v", err)
	}
	if rep.RunsCommitted != 0 || rep.Committed == nil || rep.Pending == nil {
		t.Errorf("a nil publisher's report is not an empty report: %+v", rep)
	}
	if p.Journal() != nil {
		t.Error("a nil publisher offers a journal")
	}
}

// Local mode reports a condition, not a fault: a deployment that keeps every
// byte on one machine is supported, and the caller reads this as "write locally
// and stage nothing".
func TestNewRefusesAnUnpublishableConfiguration(t *testing.T) {
	f := newFixture(t)
	base := Options{
		Config: config.Config{
			Mode: config.ModeShared, DeploymentID: testDeployment, InstanceID: testInstance,
		},
		Journal: f.journal, Catalog: f.catalog, Store: f.store, Keyring: f.ring,
	}

	local := base
	local.Config.Mode = config.ModeLocal
	if _, err := New(local); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("local mode: %v, want ErrNotConfigured", err)
	}

	noStore := base
	noStore.Store = nil
	if _, err := New(noStore); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no object store: %v, want ErrNotConfigured", err)
	}

	noRing := base
	noRing.Keyring = nil
	if _, err := New(noRing); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("no keyring: %v, want ErrNotConfigured", err)
	}

	noIdentity := base
	noIdentity.Config.InstanceID = ""
	if _, err := New(noIdentity); err == nil {
		t.Error("a shared configuration with no instance identity was accepted")
	}

	noJournal := base
	noJournal.Journal = nil
	if _, err := New(noJournal); err == nil {
		t.Error("a publisher with no journal was accepted")
	}
}

// One unreachable closure must not strand the others. They are independent
// commits, and a report that stopped at the first failure would hide work that
// would have published.
func TestOneFailedClosureDoesNotStrandTheOthers(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageAndDeclare(t, "run-m", record("run-m", "run-m-one", sharedcatalog.KindHypothesis))
	f.stageAndDeclare(t, "run-n", record("run-n", "run-n-one", sharedcatalog.KindFinding))
	f.stageAndDeclare(t, "run-o", record("run-o", "run-o-one", sharedcatalog.KindObservation))

	// Only the middle closure's object write fails.
	f.store.failPut = func(key string) error {
		if strings.Contains(key, "run-n-one") {
			return errors.New("cellar refused this object")
		}
		return nil
	}

	rep, err := f.pub.Retry(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if rep.RunsCommitted != 2 {
		t.Errorf("committed %d runs, want 2", rep.RunsCommitted)
	}
	if len(rep.Failures) != 1 || rep.Failures[0].RunID != "run-n" {
		t.Errorf("failures = %+v, want exactly run-n", rep.Failures)
	}
	if rep.RunsPending != 1 {
		t.Errorf("runs still pending = %d, want 1", rep.RunsPending)
	}
	if got := rep.Pending[sharedcatalog.KindFinding]; got != 1 {
		t.Errorf("pending findings = %d, want 1", got)
	}
	if got := rep.Committed[sharedcatalog.KindHypothesis]; got != 1 {
		t.Errorf("committed hypotheses = %d, want 1", got)
	}
	for _, id := range []string{"run-m-one", "run-o-one"} {
		if got := f.journalState(t, id); got != sharedcatalog.SyncCommitted {
			t.Errorf("journal state for %s = %q, want %q", id, got, sharedcatalog.SyncCommitted)
		}
	}
	if got := f.journalState(t, "run-n-one"); got != sharedcatalog.SyncPending {
		t.Errorf("journal state for the failed record = %q, want %q", got, sharedcatalog.SyncPending)
	}
}

// A closure is attempted in declaration order: the one owed longest first, and
// a continuation after what it continues.
func TestClosuresPublishInDeclarationOrder(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageAndDeclare(t, "run-p", record("run-p", "run-p-one", sharedcatalog.KindHypothesis))
	f.stageAndDeclare(t, "run-q", record("run-q", "run-q-one", sharedcatalog.KindHypothesis))

	runs, err := f.journal.pendingRuns(ctx)
	if err != nil {
		t.Fatalf("list pending runs: %v", err)
	}
	if len(runs) != 2 || runs[0].runID != "run-p" || runs[1].runID != "run-q" {
		t.Fatalf("pending order = %+v, want run-p then run-q", runs)
	}
}

// A continuation link and a host pin are declared with the closure and reach
// the catalog, because they are the lineage and the rerun constraint a second
// instance reads (SPEC.md §4.7, §9).
func TestDeclarationCarriesLineageAndHostPin(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageAndDeclare(t, "run-r", record("run-r", "run-r-one", sharedcatalog.KindHypothesis))
	if err := f.pub.CommitInline(ctx, Closure{RunID: "run-r"}); err != nil {
		t.Fatalf("commit inline: %v", err)
	}

	tx := f.writerTx(t)
	if err := f.pub.StageTx(ctx, tx, record("run-s", "run-s-one", sharedcatalog.KindFinding)); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if err := f.pub.DeclareTx(ctx, tx, Closure{
		RunID:           "run-s",
		ExecutionHostID: testHost,
		ContinuesRunID:  "run-r",
	}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit writer transaction: %v", err)
	}
	if err := f.pub.CommitInline(ctx, Closure{RunID: "run-s"}); err != nil {
		t.Fatalf("commit inline: %v", err)
	}

	run := f.remoteRun(t, "run-s")
	if run.ExecutionHostID != testHost {
		t.Errorf("remote run host pin = %q, want %q", run.ExecutionHostID, testHost)
	}
	if run.ContinuesRunID != "run-r" {
		t.Errorf("remote run continues %q, want run-r", run.ContinuesRunID)
	}
}

// runIsInvisible reports whether the run is absent from the browse surface for
// what is globally reviewable. A pending run must be readable by id - an
// operator asking why a run is not reviewable needs the row that says so - and
// must not be offered as committed.
func runIsInvisible(t *testing.T, f *fixture, runID string) bool {
	t.Helper()
	runs, err := sharedcatalog.CommittedAnalysisRuns(t.Context(), f.catalog, testDeployment)
	if err != nil {
		t.Fatalf("list committed runs: %v", err)
	}
	for _, run := range runs {
		if run.RunID == runID {
			return false
		}
	}
	return true
}

// scanCatalogForText reads every text-shaped value in Babel's schema looking
// for the marker, rather than the columns this test happens to know about, so a
// future column that carries plaintext fails here even if nobody updated it.
func scanCatalogForText(t *testing.T, f *fixture, marker string) []string {
	t.Helper()
	ctx := t.Context()
	rows, err := f.catalog.QueryContext(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		 WHERE table_schema = $1 AND data_type IN ('text', 'character varying', 'jsonb', 'json')
		 ORDER BY table_name, column_name`, sharedcatalog.Schema)
	if err != nil {
		t.Fatalf("reflect schema: %v", err)
	}
	type column struct{ table, name string }
	var columns []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.table, &c.name); err != nil {
			rows.Close()
			t.Fatalf("scan column: %v", err)
		}
		columns = append(columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("reflect schema: %v", err)
	}
	if len(columns) == 0 {
		t.Fatal("schema reflection found no text columns, so this scan proves nothing")
	}

	var hits []string
	for _, c := range columns {
		var n int
		query := `SELECT count(*) FROM ` + sharedcatalog.Schema + `.` + quoteIdent(c.table) +
			` WHERE ` + quoteIdent(c.name) + `::text LIKE '%' || $1 || '%'`
		if err := f.catalog.QueryRowContext(ctx, query, marker).Scan(&n); err != nil {
			t.Fatalf("scan %s.%s: %v", c.table, c.name, err)
		}
		if n > 0 {
			hits = append(hits, c.table+"."+c.name)
		}
	}
	return hits
}

// quoteIdent quotes a reflected identifier for interpolation. The values come
// from information_schema in a database this test created, so this is hygiene
// rather than a trust boundary, and doubling an embedded quote is the whole
// rule.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
