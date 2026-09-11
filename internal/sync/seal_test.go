package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// What this file defends is the one debt no other recovery path can see.
//
// On 2026-09-06 two runs were killed badly enough that every entry point built
// to recover them was blind: Store.Reconcile scans a lease that is deleted on
// release, Store.DeclareFinished selects a receipt those runs never wrote, and
// Store.RecoverHistorical needs a presence row and a preparation that a run
// with no receipt has no link to. Retry itself iterated declared closures and
// merely counted the rest. The staged records were the only durable evidence
// the debt existed, and 1,022 of them - analysis that exists nowhere else -
// stayed unpublishable for five days while every automatic publish path ran
// (issue #152; CHANGELOG's 2026-09-06 entry is the same shape striking twice).
//
// So these cases start from exactly that state - records staged, no closure, no
// lease, no receipt - and they assert the two halves that make the fix a fix
// rather than a workaround: the records reach the catalog with no operator
// action, and the run row says why Babel had to abandon it. The second case is
// migration 0003's guard: a run that is still live must not be sealed, because
// record_count is fixed at declaration and a run sealed early would be
// permanently short of its own output.

// stageOnly stages records and declares nothing, which is what a run killed
// between its last record and its own declaration leaves behind.
func (f *fixture) stageOnly(t *testing.T, recs ...Record) {
	t.Helper()
	tx := f.writerTx(t)
	defer tx.Rollback()
	for _, rec := range recs {
		if err := f.pub.StageTx(t.Context(), tx, rec); err != nil {
			t.Fatalf("stage %s: %v", rec.EntityID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit writer transaction: %v", err)
	}
}

// declaredRun reads the journal's run row, or reports that there is none.
func (f *fixture) declaredRun(t *testing.T, runID string) (stagedRun, bool) {
	t.Helper()
	run, err := f.journal.run(t.Context(), runID)
	if err != nil {
		return stagedRun{}, false
	}
	return run, true
}

// over is the liveness oracle for a run whose evidence is gone, in the words
// run.Store.RunLiveness uses for that exact state.
func over() Liveness {
	return func(context.Context, string) (bool, string, error) {
		return false, "no lease and no receipt survive this run, so nothing links it to a " +
			"preparation either; only the staged records remain", nil
	}
}

func TestStrandedRecordsAreSealedAtWhatTheRunReachedAndPublish(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	// The operator's situation: three records staged under one run, no closure
	// declared for them, and nothing else anywhere that says the run existed.
	f.stageOnly(t,
		record("run-stranded", "stranded-link", sharedcatalog.KindLink),
		record("run-stranded", "stranded-obs", sharedcatalog.KindObservation),
		record("run-stranded", "stranded-hyp", sharedcatalog.KindHypothesis))
	if _, declared := f.declaredRun(t, "run-stranded"); declared {
		t.Fatal("the fixture declared a closure; this case is about a run that never did")
	}
	f.pub.Live = over()

	// One ordinary publish, which is what every automatic path already calls.
	// No new command, and nothing an operator had to notice.
	rep, err := f.pub.Retry(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(f.failures) != 0 {
		t.Fatalf("sealing reported %d diagnostics: %v", len(f.failures), f.failures)
	}

	if len(rep.Sealed) != 1 {
		t.Fatalf("report sealed %d runs, want the one that was abandoned: %+v", len(rep.Sealed), rep.Sealed)
	}
	sealed := rep.Sealed[0]
	if sealed.RunID != "run-stranded" || sealed.Records != 3 {
		t.Errorf("sealed %s at %d records, want run-stranded at 3", sealed.RunID, sealed.Records)
	}
	if sealed.Reason == "" {
		t.Error("the seal reported no cause, which is the measurement it exists to record")
	}
	if rep.RunsCommitted != 1 {
		t.Errorf("runs committed = %d, want the sealed closure published on this same attempt", rep.RunsCommitted)
	}
	if rep.Undeclared != 0 {
		t.Errorf("undeclared records = %d after the seal, want none still owed with nobody to declare", rep.Undeclared)
	}

	// The records are in the shared catalog, which is the whole point: the
	// analysis is no longer only on this disk.
	run := f.remoteRun(t, "run-stranded")
	if run.SyncState != sharedcatalog.SyncCommitted {
		t.Errorf("remote run state = %q, want %q", run.SyncState, sharedcatalog.SyncCommitted)
	}
	if run.RecordCount != 3 || run.RecordsPresent != 3 {
		t.Errorf("remote run holds %d of %d records, want 3 of 3", run.RecordsPresent, run.RecordCount)
	}
	if rows := f.remoteRecords(t, "run-stranded"); len(rows) != 3 {
		t.Errorf("the catalog holds %d records, want the 3 that were stranded", len(rows))
	}
	for _, id := range []string{"stranded-link", "stranded-obs", "stranded-hyp"} {
		if got := f.journalState(t, id); got != sharedcatalog.SyncCommitted {
			t.Errorf("%s reports %q locally, want %q", id, got, sharedcatalog.SyncCommitted)
		}
	}

	// And the abandonment is data rather than a line that scrolled past: the
	// run row carries the cause, so an operator can measure why Babel had to
	// abandon a run without having kept the log that said so.
	local, declared := f.declaredRun(t, "run-stranded")
	if !declared {
		t.Fatal("the sealed run has no journal row")
	}
	if local.abandonedReason != sealed.Reason {
		t.Errorf("the run row records %q, want the reported cause %q", local.abandonedReason, sealed.Reason)
	}
}

// A live run is the case migration 0003 makes permanent: its record_count would
// be fixed at whatever it happened to have staged, and the records it went on to
// produce could never join the closure. So liveness is proof that must be
// absent, not merely evidence that is old.
func TestALiveRunIsLeftStagedAndUndeclared(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageOnly(t,
		record("run-live", "live-obs", sharedcatalog.KindObservation),
		record("run-live", "live-hyp", sharedcatalog.KindHypothesis))
	// The run still holds its lease, which is what internal/run reports as a
	// live attempt; a live attempt names no cause, because there is none.
	f.pub.Live = func(context.Context, string) (bool, string, error) { return true, "", nil }

	rep, err := f.pub.Retry(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(rep.Sealed) != 0 {
		t.Fatalf("a live run was sealed: %+v", rep.Sealed)
	}
	if rep.Undeclared != 2 {
		t.Errorf("undeclared records = %d, want the 2 still waiting for their run", rep.Undeclared)
	}
	if _, declared := f.declaredRun(t, "run-live"); declared {
		t.Error("a live run's closure was declared, which 0003 would then never let grow")
	}
	for _, id := range []string{"live-obs", "live-hyp"} {
		if got := f.journalState(t, id); got != sharedcatalog.SyncPending {
			t.Errorf("%s reports %q, want it still visibly %q", id, got, sharedcatalog.SyncPending)
		}
	}
	if rows := f.remoteRecords(t, "run-live"); len(rows) != 0 {
		t.Errorf("the catalog holds %d records of a run that is still producing", len(rows))
	}
}

// A deployment that can prove nothing seals nothing. Guessing would be the one
// mistake 0003 makes unrecoverable, so the absence of an oracle has to read as
// "no evidence" rather than as "no lease, therefore dead".
func TestWithoutALivenessOracleNothingIsSealed(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageOnly(t, record("run-unproven", "unproven-obs", sharedcatalog.KindObservation))

	sealed, err := f.pub.SealAbandoned(ctx)
	if err != nil {
		t.Fatalf("seal with no oracle: %v", err)
	}
	if len(sealed) != 0 {
		t.Fatalf("a publisher that can prove nothing sealed %d runs", len(sealed))
	}
	if _, declared := f.declaredRun(t, "run-unproven"); declared {
		t.Error("a closure was declared on no evidence at all")
	}
}

// One run whose evidence cannot be read must not strand the output of the
// others, which is the same discipline Retry applies to a failed publication.
func TestALivenessFailureOnOneRunDoesNotStopTheRest(t *testing.T) {
	f := newFixture(t)
	ctx := t.Context()

	f.stageOnly(t, record("run-unreadable", "unreadable-obs", sharedcatalog.KindObservation))
	f.stageOnly(t, record("run-readable", "readable-obs", sharedcatalog.KindObservation))
	f.pub.Live = func(_ context.Context, runID string) (bool, string, error) {
		if runID == "run-unreadable" {
			return false, "", errors.New("the durable file could not be read")
		}
		return false, "the receipt reached interrupted without declaring a closure, and no lease survives to reconcile", nil
	}

	rep, err := f.pub.Retry(ctx)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(rep.Sealed) != 1 || rep.Sealed[0].RunID != "run-readable" {
		t.Fatalf("sealed %+v, want only the run whose evidence could be read", rep.Sealed)
	}
	if len(f.failures) != 1 {
		t.Errorf("the unreadable run produced %d diagnostics, want exactly one", len(f.failures))
	}
	if _, declared := f.declaredRun(t, "run-unreadable"); declared {
		t.Error("a run whose liveness could not be established was sealed anyway")
	}
	if got := f.journalState(t, "readable-obs"); got != sharedcatalog.SyncCommitted {
		t.Errorf("the readable run's record reports %q, want %q", got, sharedcatalog.SyncCommitted)
	}
}
