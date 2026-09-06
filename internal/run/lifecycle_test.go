package run

import (
	"bytes"
	"context"
	"github.com/atyrode/babel/internal/sync"
	"os"
	"testing"
	"time"
)

func lifecycleReceipt(t *testing.T, s *Store, id string, state Lifecycle) Receipt {
	t.Helper()
	body := testBody(t)
	body.Checkpoint = &Checkpoint{State: state, Stage: "explore", Launch: &Launch{Profile: testWorkerReceipt().Profile, Recipes: []string{"outcome-integrity"}}}
	r, err := NewReceipt(NewReceiptID(), id, mustPreparation(t, preparedAt, testSelection()), testAuthority(), body, recorded)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutReceipt(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestReconcileDeadOwnerExcludesLiveAndTerminal(t *testing.T) {
	s := testStore(t)
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Hour)
	lost := lifecycleReceipt(t, s, "lost", Running)
	lifecycleReceipt(t, s, "live", Running)
	lifecycleReceipt(t, s, "terminal", Closed)
	lifecycleReceipt(t, s, "fresh", Running)
	for _, c := range []struct {
		id  string
		pid int
		at  time.Time
	}{
		{"lost", 2147483647, stale}, {"live", os.Getpid(), stale}, {"terminal", 2147483647, stale}, {"fresh", 2147483647, time.Now()},
	} {
		if _, err := s.db.Exec(`INSERT INTO run_lease VALUES(?,?,?,?)`, c.id, host, c.pid, formatTime(c.at)); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := lost.MarshalBody()
	recovered, err := s.Reconcile(t.Context(), time.Now().Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].Header.RunID != "lost" || recovered[0].Body.Checkpoint.State != Interrupted {
		t.Fatalf("recovered = %+v", recovered)
	}
	prior, err := s.Receipt(t.Context(), lost.Header.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := prior.MarshalBody()
	if !bytes.Equal(before, after) {
		t.Fatal("reconciliation rewrote the original receipt")
	}
	again, err := s.Reconcile(t.Context(), time.Now().Add(-5*time.Minute))
	if err != nil || len(again) != 0 {
		t.Fatalf("reconcile is not idempotent: %v %v", again, err)
	}
	live, err := s.Latest(t.Context(), "live")
	if err != nil {
		t.Fatal(err)
	}
	if live.Body.Checkpoint.State != Running {
		t.Fatal("a live process was interrupted")
	}
}

func TestInterruptedCloseListsAndExcludesConcurrentResume(t *testing.T) {
	s := testStore(t)
	prior := lifecycleReceipt(t, s, "partial", Interrupted)
	release, err := s.BeginAttempt(t.Context(), "partial")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseInterrupted(t.Context(), "partial"); err == nil {
		t.Fatal("close raced an owned attempt")
	}
	release()
	listed, err := s.Interrupted(t.Context())
	if err != nil || len(listed) != 1 {
		t.Fatalf("list = %v %v", listed, err)
	}
	closed, err := s.CloseInterrupted(context.Background(), "partial")
	if err != nil {
		t.Fatal(err)
	}
	if closed.Header.Supersedes != prior.Header.ID || closed.Body.Checkpoint.State != Closed {
		t.Fatal("close did not append a terminal amendment")
	}
	listed, err = s.Interrupted(t.Context())
	if err != nil || len(listed) != 0 {
		t.Fatalf("closed run still interrupted: %v %v", listed, err)
	}
	if _, err := s.CloseInterrupted(t.Context(), "partial"); err == nil {
		t.Fatal("closed a terminal run twice")
	}
}

func TestHistoricalRecoveryPreservesUnknownProvenance(t *testing.T) {
	s := testStore(t)
	prep := mustPreparation(t, preparedAt, testSelection())
	if err := s.PutPreparation(t.Context(), prep); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE explore_commit(run_id TEXT,stage TEXT,ref TEXT,entity_id TEXT,recorded_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO explore_commit VALUES('old','explore','candidate','hyp-old','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
	receipt, err := s.RecoverHistorical(t.Context(), "old", prep.ID, Authority{}, "old-recipe", time.Now().Add(-time.Hour))
	if err != nil || receipt == nil {
		t.Fatalf("recover historical: %v %v", receipt, err)
	}
	cp := receipt.Body.Checkpoint
	if !cp.Historical || cp.Launch != nil || len(receipt.Body.Cookbook) != 0 || receipt.Header.Authority.Recorded() || len(cp.Records) != 1 || cp.Records[0] != "hyp-old" {
		t.Fatal("historical recovery invented or dropped provenance")
	}
	again, err := s.RecoverHistorical(t.Context(), "old", prep.ID, Authority{}, "old-recipe", time.Now().Add(-time.Hour))
	if err != nil || again != nil {
		t.Fatal("historical recovery was not idempotent")
	}
	if _, err := s.CloseInterrupted(t.Context(), "old"); err != nil {
		t.Fatal(err)
	}
}

func TestRunningReceiptCannotPublishAsFinished(t *testing.T) {
	hook := sync.NewStager()
	s, err := Open(t.TempDir(), WithSync(hook))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prior := lifecycleReceipt(t, s, "still-running", Running)
	declared, _, err := s.DeclareFinished(t.Context(), hook)
	if err != nil || len(declared) != 0 {
		t.Fatalf("running receipt declared a closure: %v %v", declared, err)
	}
	interrupted, err := s.Transition(t.Context(), prior, Interrupted, "operator stop")
	if err != nil {
		t.Fatal(err)
	}
	declared, _, err = s.DeclareFinished(t.Context(), hook)
	if err != nil || len(declared) != 1 || declared[0] != "still-running" {
		t.Fatalf("partial receipt not publishable: %v %v", declared, err)
	}
	closed, err := s.CloseInterrupted(t.Context(), "still-running")
	if err != nil {
		t.Fatal(err)
	}
	var continues string
	if err := s.db.QueryRow(`SELECT continues_run_id FROM sync_run WHERE run_id=?`, closed.Header.ID).Scan(&continues); err != nil {
		t.Fatal(err)
	}
	if continues != "still-running" || closed.Header.Supersedes != interrupted.Header.ID {
		t.Fatal("intentional close lost the immutable continuation lineage")
	}
	if _, err := s.Transition(t.Context(), closed, Resumed, "attempt to reopen"); err == nil {
		t.Fatal("closed run was resumed")
	}
}
