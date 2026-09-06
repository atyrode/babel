package run

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestStaleReconcilerCannotInterruptNewLiveAttempt(t *testing.T) {
	s := testStore(t)
	lifecycleReceipt(t, s, "race", Running)
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	stale := reconcileCandidate{id: "race", pid: 2147483647, beat: formatTime(time.Now().Add(-time.Hour))}
	if _, err := s.db.Exec(`INSERT INTO run_lease VALUES(?,?,?,?)`, stale.id, host, stale.pid, stale.beat); err != nil {
		t.Fatal(err)
	}
	// Two reconcilers observed this same dead lease. The first must retain its
	// claim even while publication is slow; no controller may launch then.
	published := false
	first, err := s.reconcileCandidate(t.Context(), host, stale, ReconcileOptions{Publish: func(ctx context.Context, id string) error {
		release, err := s.BeginAttempt(ctx, id)
		if err == nil {
			release()
			t.Fatal("reconciliation released ownership before publication")
		}
		if !errors.Is(err, ErrAttemptOwned) {
			t.Fatal(err)
		}
		published = true
		return nil
	}})
	if err != nil || first == nil || !published {
		t.Fatalf("first recovery: %v %v", first, err)
	}
	release, err := s.BeginAttempt(t.Context(), "race")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	resumed, err := s.Transition(t.Context(), *first, Resumed, "operator resumed the run")
	if err != nil {
		t.Fatal(err)
	}
	// The second reconciler now acts on its obsolete observation. It must lose
	// its compare-and-swap before touching the newer receipt or publication.
	second, err := s.reconcileCandidate(t.Context(), host, stale, ReconcileOptions{Publish: func(context.Context, string) error {
		t.Fatal("stale reconciler published a live attempt")
		return nil
	}})
	if err != nil || second != nil {
		t.Fatalf("stale recovery = %v %v", second, err)
	}
	latest, err := s.Latest(t.Context(), "race")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Header.ID != resumed.Header.ID || latest.Body.Checkpoint.State != Resumed {
		t.Fatal("stale evidence interrupted the new live attempt")
	}
	owned, err := s.AttemptOwned(t.Context(), "race")
	if err != nil || !owned {
		t.Fatal("stale reconciler released the live attempt's ownership")
	}
}

func TestReconciliationRetainsRecoveryAfterStorageAndPublicationFailures(t *testing.T) {
	s := testStore(t)
	prior := lifecycleReceipt(t, s, "recoverable", Running)
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now().Add(-time.Minute)
	if _, err := s.db.Exec(`INSERT INTO run_lease VALUES(?,?,?,?)`, "recoverable", host, 2147483647, formatTime(before.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_amendment BEFORE INSERT ON run_receipt BEGIN SELECT RAISE(ABORT, 'receipt storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reconcile(t.Context(), before, ReconcileOptions{}); err == nil {
		t.Fatal("receipt storage failure was hidden")
	}
	latest, err := s.Latest(t.Context(), "recoverable")
	if err != nil || latest.Header.ID != prior.Header.ID {
		t.Fatalf("failed amendment changed the durable receipt: %v", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER refuse_amendment`); err != nil {
		t.Fatal(err)
	}
	publicationErr := errors.New("publication unavailable")
	if _, err := s.Reconcile(t.Context(), before, ReconcileOptions{Publish: func(context.Context, string) error {
		return publicationErr
	}}); !errors.Is(err, publicationErr) {
		t.Fatalf("storage repair did not resume reconciliation: %v", err)
	}
	interrupted, err := s.Latest(t.Context(), "recoverable")
	if err != nil || interrupted.Body.Checkpoint.State != Interrupted {
		t.Fatalf("partial receipt was not retained: %v", err)
	}
	recovered, err := s.Reconcile(t.Context(), before, ReconcileOptions{})
	if err != nil || len(recovered) != 1 || recovered[0].Header.ID != interrupted.Header.ID {
		t.Fatalf("publication recovery lost or duplicated the receipt: %v %v", recovered, err)
	}
	release, err := s.BeginAttempt(t.Context(), "recoverable")
	if err != nil {
		t.Fatalf("successful recovery retained stale ownership: %v", err)
	}
	release()
}

func TestAttemptAcquisitionPreservesStorageAndContextErrors(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.BeginAttempt(ctx, "cancelled"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrAttemptOwned) {
		t.Fatalf("cancelled acquisition was classified as ownership: %v", err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER refuse_lease BEFORE INSERT ON run_lease BEGIN SELECT RAISE(ABORT, 'lease storage unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginAttempt(t.Context(), "storage"); err == nil || errors.Is(err, ErrAttemptOwned) {
		t.Fatalf("storage failure was classified as ownership: %v", err)
	}
}

func TestFailedAttemptFinalizationRetainsOwnerEvidence(t *testing.T) {
	s := testStore(t)
	func() {
		release, err := s.BeginAttempt(t.Context(), "unfinished")
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		prior := lifecycleReceipt(t, s, "unfinished", Running)
		if _, err := s.db.Exec(`CREATE TRIGGER refuse_final_receipt BEFORE INSERT ON run_receipt BEGIN SELECT RAISE(ABORT, 'receipt storage unavailable'); END`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Transition(t.Context(), prior, Closed, "completed"); err == nil {
			t.Fatal("final receipt storage failure was hidden")
		}
	}()
	if owned, err := s.AttemptOwned(t.Context(), "unfinished"); err != nil || !owned {
		t.Fatalf("failed finalization erased recovery ownership: %v", err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER refuse_final_receipt`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE run_lease SET pid=?, heartbeat=? WHERE run_id=?`, 2147483647, formatTime(time.Now().Add(-time.Hour)), "unfinished"); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Reconcile(t.Context(), time.Now().Add(-time.Minute), ReconcileOptions{})
	if err != nil || len(recovered) != 1 || recovered[0].Body.Checkpoint.State != Interrupted {
		t.Fatalf("failed attempt could not be recovered after owner loss: %v %v", recovered, err)
	}
}
