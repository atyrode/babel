package sync

import (
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// Append is the one call a durable writer makes, and the rule it applies is the
// part of this package a writer could otherwise get wrong at every write site.
// These tests pin the rule rather than the plumbing: which closure a record
// joins, when a closure is declared, and what happens to a job identity that
// names a stage of a run rather than a run.

func TestAppendJoinsAnOpenRunAndPublishesNothing(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx := f.writerTx(t)
	closure, publish, err := f.pub.Append(ctx, tx, "run-open", Record{
		EntityID: "run-open-hyp", Kind: sharedcatalog.KindHypothesis,
		Schema: 1, Payload: []byte(`{"claim":"one"}`),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if publish {
		t.Errorf("a record joining an open closure reported %+v as publishable", closure)
	}
	if closure != (Closure{}) {
		t.Errorf("closure = %+v, want the zero closure", closure)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// It is staged, and it is staged under the run rather than under itself:
	// the run is the commit unit, and the run declares it when it ends.
	if got := f.journalState(t, "run-open-hyp"); got != sharedcatalog.SyncPending {
		t.Errorf("journal state = %q, want %q", got, sharedcatalog.SyncPending)
	}
	if n, err := f.journal.UndeclaredRecords(ctx); err != nil || n != 1 {
		t.Errorf("undeclared records = %d (%v), want 1", n, err)
	}
	if _, err := f.journal.run(ctx, "run-open"); err == nil {
		t.Error("joining an open closure declared it")
	}
}

// A record no run produced is its own closure of one, declared inside the
// writer's transaction. Nobody resumes an operator's decision, so a closure
// declared after the commit could be lost forever to a crash in between.
func TestAppendWithNoRunDeclaresItsOwnClosureInTheTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx := f.writerTx(t)
	closure, publish, err := f.pub.Append(ctx, tx, "", Record{
		EntityID: "operator-decision-1", Kind: sharedcatalog.KindDisposition,
		Schema: 1, Payload: []byte(`{"note":"accepted"}`),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !publish || closure.RunID != "operator-decision-1" {
		t.Fatalf("closure = %+v, publish = %v, want a closure of one for the record", closure, publish)
	}
	if closure.ContinuesRunID != "" {
		t.Errorf("closure continues %q, want nothing", closure.ContinuesRunID)
	}

	// The declaration is inside the writer's transaction, so it is not visible
	// to the journal's own connection until the writer commits - and it is
	// gone entirely if the writer rolls back.
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := f.journal.run(ctx, "operator-decision-1"); err == nil {
		t.Error("a rolled-back write left a declared closure behind")
	}
	if got := f.journalState(t, "operator-decision-1"); got != "" {
		t.Errorf("a rolled-back write left %q staged", got)
	}
}

// A record produced by a run whose closure is already declared is not part of
// that run's output - the run is over - so it becomes its own closure linked to
// the run it came after. That is what an operator's later decision is, and what
// an amended receipt is.
func TestAppendAfterAClosedClosureContinuesIt(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageAndDeclare(t, "run-done", record("run-done", "run-done-rcp", sharedcatalog.KindReceipt))
	if err := f.pub.CommitInline(ctx, Closure{RunID: "run-done"}); err != nil {
		t.Fatalf("commit inline: %v", err)
	}

	tx := f.writerTx(t)
	closure, publish, err := f.pub.Append(ctx, tx, "run-done", Record{
		EntityID: "run-done-rcp-2", Kind: sharedcatalog.KindReceipt,
		Schema: 1, Payload: []byte(`{"amended":true}`),
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if !publish {
		t.Fatal("a record appended after a closed closure reported nothing to publish")
	}
	if closure.RunID != "run-done-rcp-2" || closure.ContinuesRunID != "run-done" {
		t.Errorf("closure = %+v, want run-done-rcp-2 continuing run-done", closure)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := f.pub.CommitInline(ctx, closure); err != nil {
		t.Fatalf("commit inline: %v", err)
	}

	// The continuation reached the catalog and says what it continues, which is
	// the lineage a second instance reads.
	run := f.remoteRun(t, "run-done-rcp-2")
	if run.SyncState != sharedcatalog.SyncCommitted {
		t.Errorf("continuation state = %q, want %q", run.SyncState, sharedcatalog.SyncCommitted)
	}
	if run.ContinuesRunID != "run-done" {
		t.Errorf("continuation continues %q, want run-done", run.ContinuesRunID)
	}
}

// SPEC.md §5.4's challenger and synthesizer have their own run identity,
// spelled `<run>/<stage>` by internal/explore. Those records belong to the
// exploration's closure: the compound id could not be a publication run id at
// all, and treating it as one would make a single exploration become globally
// reviewable in three unrelated pieces.
func TestAppendReducesAStageIdentityToItsRun(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx := f.writerTx(t)
	for _, from := range []struct{ producedBy, id string }{
		{"run-x", "run-x-hyp"},
		{"run-x/challenge", "run-x-obs"},
		{"run-x/synthesize", "run-x-fnd"},
	} {
		if _, publish, err := f.pub.Append(ctx, tx, from.producedBy, Record{
			EntityID: from.id, Kind: sharedcatalog.KindHypothesis,
			Schema: 1, Payload: []byte(`{"claim":"one"}`),
		}); err != nil {
			t.Fatalf("append %s: %v", from.id, err)
		} else if publish {
			t.Errorf("record %s from %q published on its own", from.id, from.producedBy)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// One closure, three records, declared once by the control plane that knows
	// the run ended.
	if err := f.pub.CommitInline(ctx, Closure{RunID: "run-x"}); err != nil {
		t.Fatalf("commit inline: %v", err)
	}
	run := f.remoteRun(t, "run-x")
	if run.RecordCount != 3 || run.RecordsPresent != 3 {
		t.Errorf("run holds %d of %d records, want 3 of 3", run.RecordsPresent, run.RecordCount)
	}
	if run.SyncState != sharedcatalog.SyncCommitted {
		t.Errorf("run state = %q, want %q", run.SyncState, sharedcatalog.SyncCommitted)
	}
	for _, id := range []string{"run-x-hyp", "run-x-obs", "run-x-fnd"} {
		if got := f.journalState(t, id); got != sharedcatalog.SyncCommitted {
			t.Errorf("journal state for %s = %q, want %q", id, got, sharedcatalog.SyncCommitted)
		}
	}
	// And no closure was ever declared under a stage identity.
	for _, stage := range []string{"run-x/challenge", "run-x/synthesize"} {
		if _, err := f.journal.run(ctx, stage); err == nil {
			t.Errorf("a closure was declared for the stage identity %q", stage)
		}
	}
}

func TestPublicationRunReducesOnlyAtTheFirstSeparator(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"run-1", "run-1"},
		{"run-1/challenge", "run-1"},
		{"run-1/synthesize", "run-1"},
		{"run-1/a/b", "run-1"},
		{"/leading", ""},
	}
	for _, tc := range cases {
		if got := publicationRun(tc.in); got != tc.want {
			t.Errorf("publicationRun(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Append validates before it stages, so a record the shared catalog could never
// carry is a caller bug caught by the writer's own test rather than a permanent
// pending row with no remedy.
func TestAppendRefusesAnUnusableRecord(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx := f.writerTx(t)
	defer tx.Rollback()
	_, _, err := f.pub.Append(ctx, tx, "run-1", Record{
		EntityID: "not a valid id", Kind: sharedcatalog.KindHypothesis,
		Schema: 1, Payload: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("Append accepted a malformed entity id")
	}
}

// One entity id belongs to one closure. Two closures claiming it would attach a
// record to a lineage it does not belong to, and no retry resolves that.
func TestAppendRefusesAnIDStagedElsewhere(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	tx := f.writerTx(t)
	if _, _, err := f.pub.Append(ctx, tx, "run-a", Record{
		EntityID: "shared", Kind: sharedcatalog.KindHypothesis,
		Schema: 1, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	_, _, err := f.pub.Append(ctx, tx, "run-b", Record{
		EntityID: "shared", Kind: sharedcatalog.KindHypothesis,
		Schema: 1, Payload: []byte(`{}`),
	})
	if !errors.Is(err, ErrRecordConflict) {
		t.Fatalf("append under a second run: %v, want ErrRecordConflict", err)
	}
}
