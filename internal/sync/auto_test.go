package sync

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// What this file defends is the machine nobody is watching.
//
// seal_test.go proves that one publish attempt recovers a run that died
// without declaring its closure. This proves the half that was still missing
// on 2026-09-12: that on a host with no conductor and no operator at the
// keyboard, an attempt happens at all. The same 300 staged records that were
// recoverable by `babel sync` were not recovered, for five days, because
// nothing called it.

// waitFor blocks for one attempt's report, or fails the test. The generous
// bound is a real publication over a real catalog: what is being waited on is
// that an attempt happens without being asked for, not that it is quick.
func waitFor(t *testing.T, reports <-chan Report) Report {
	t.Helper()
	select {
	case rep := <-reports:
		return rep
	case <-time.After(30 * time.Second):
		t.Fatal("no drain attempt was made; the records a dead run staged are still waiting for a person")
		return Report{}
	}
}

// TestADeadRunsRecordsPublishWithNoConductorAndNoCommand starts from the state
// a badly killed run leaves - records staged, no closure declared, no lease,
// no receipt - on a machine whose only publication path is the drainer.
//
// Nothing in this test calls Retry, SealAbandoned or anything else an operator
// could have typed. Starting the drainer is the whole of what happens, which
// is what SPEC.md §9.1 means by a record reaching the shared catalog without
// an operator action.
func TestADeadRunsRecordsPublishWithNoConductorAndNoCommand(t *testing.T) {
	f := newFixture(t)
	f.stageOnly(t,
		record("run-unattended", "unattended-obs", sharedcatalog.KindObservation),
		record("run-unattended", "unattended-hyp", sharedcatalog.KindHypothesis))
	if _, declared := f.declaredRun(t, "run-unattended"); declared {
		t.Fatal("the fixture declared a closure; this case is about a run that never did")
	}
	f.pub.Live = over()

	reports := make(chan Report, 4)
	stop := NewDrainer(f.pub, func(rep Report) { reports <- rep }, nil).
		Start(t.Context(), time.Hour)
	rep := waitFor(t, reports)
	stop()

	// The seal is in the report with the cause beside it, because an
	// abandonment Babel had to perform is a measurement rather than a cleanup:
	// a surface can say why this run was abandoned without the operator having
	// kept the log that scrolled past (issue #152).
	if len(rep.Sealed) != 1 {
		t.Fatalf("the drain sealed %d runs, want the one that was abandoned: %+v", len(rep.Sealed), rep.Sealed)
	}
	sealed := rep.Sealed[0]
	if sealed.RunID != "run-unattended" || sealed.Records != 2 {
		t.Errorf("sealed %s at %d records, want run-unattended at 2", sealed.RunID, sealed.Records)
	}
	if sealed.Reason == "" {
		t.Error("the seal reported no cause, which is the measurement it exists to record")
	}
	if rep.RunsCommitted != 1 {
		t.Errorf("runs committed = %d, want the sealed closure published on the same attempt", rep.RunsCommitted)
	}

	// And the analysis is off this disk, which is the only outcome that
	// actually closes the defect.
	run := f.remoteRun(t, "run-unattended")
	if run.SyncState != sharedcatalog.SyncCommitted {
		t.Errorf("remote run state = %q, want %q", run.SyncState, sharedcatalog.SyncCommitted)
	}
	if run.RecordCount != 2 || run.RecordsPresent != 2 {
		t.Errorf("remote run holds %d of %d records, want 2 of 2", run.RecordsPresent, run.RecordCount)
	}
	for _, id := range []string{"unattended-obs", "unattended-hyp"} {
		if got := f.journalState(t, id); got != sharedcatalog.SyncCommitted {
			t.Errorf("%s reports %q locally, want %q", id, got, sharedcatalog.SyncCommitted)
		}
	}
	if len(f.failures) != 0 {
		t.Errorf("the drain reported %d diagnostics: %v", len(f.failures), f.failures)
	}
}

// TestASlowDrainIsNeverOverlappedByTheNextAttempt runs a schedule whose every
// attempt costs far more than the interval between them, and watches what
// happens to the attempts it falls behind on.
//
// A Publisher writes the durable journal and is documented as unsafe for
// concurrent use, so an overlapping attempt is not merely a wasted one - it is
// two writers on the file §9 gives exactly one. The obvious implementation of
// a periodic task, a goroutine per tick, has precisely that bug and passes
// every test that only checks that publication eventually happens.
func TestASlowDrainIsNeverOverlappedByTheNextAttempt(t *testing.T) {
	f := newFixture(t)
	// A run that is still producing is what keeps the debt visible attempt
	// after attempt: it may not be sealed while it can still grow, so every
	// drain consults the oracle and every drain pays for it.
	f.stageOnly(t, record("run-slow", "slow-obs", sharedcatalog.KindObservation))

	var inside, attempts atomic.Int32
	var overlapped atomic.Bool
	f.pub.Live = func(context.Context, string) (bool, string, error) {
		if inside.Add(1) > 1 {
			overlapped.Store(true)
		}
		defer inside.Add(-1)
		attempts.Add(1)
		time.Sleep(20 * time.Millisecond)
		return true, "", nil
	}

	// The interval is a twentieth of what one attempt costs, so a schedule
	// that started an attempt per tick would have twenty in flight at once.
	stop := NewDrainer(f.pub, nil, nil).Start(t.Context(), time.Millisecond)
	deadline := time.Now().Add(30 * time.Second)
	for attempts.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	stop()

	// Several attempts having happened is what makes the check below worth
	// something: a schedule that quietly stopped after its first drain would
	// also never overlap.
	if got := attempts.Load(); got < 3 {
		t.Fatalf("%d attempts were made; the schedule stopped instead of falling behind", got)
	}
	if overlapped.Load() {
		t.Error("two attempts wrote the journal at once; the schedule caught up by overlapping")
	}
}
