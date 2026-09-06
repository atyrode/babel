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
 lifecycleReceipt(t,s,"race",Running)
 host, err := os.Hostname(); if err != nil { t.Fatal(err) }
 stale := reconcileCandidate{id:"race",pid:2147483647,beat:formatTime(time.Now().Add(-time.Hour))}
 if _, err := s.db.Exec(`INSERT INTO run_lease VALUES(?,?,?,?)`,stale.id,host,stale.pid,stale.beat); err != nil { t.Fatal(err) }
 // Two reconcilers observed this same dead lease. The first must retain its
 // claim even while publication is slow; no controller may launch then.
 published := false
 first, err := s.reconcileCandidate(t.Context(),host,stale,ReconcileOptions{Publish:func(ctx context.Context,id string)error{
  release, err := s.BeginAttempt(ctx,id)
  if err==nil { release(); t.Fatal("reconciliation released ownership before publication") }
  if !errors.Is(err,ErrAttemptOwned) { t.Fatal(err) }
  published=true
  return nil
 }})
 if err != nil || first==nil || !published { t.Fatalf("first recovery: %v %v",first,err) }
 release, err := s.BeginAttempt(t.Context(),"race"); if err != nil { t.Fatal(err) }; defer release()
 resumed, err := s.Transition(t.Context(),*first,Resumed,"operator resumed the run")
 if err != nil { t.Fatal(err) }
 // The second reconciler now acts on its obsolete observation. It must lose
 // its compare-and-swap before touching the newer receipt or publication.
 second, err := s.reconcileCandidate(t.Context(),host,stale,ReconcileOptions{Publish:func(context.Context,string)error{
  t.Fatal("stale reconciler published a live attempt")
  return nil
 }})
 if err != nil || second!=nil { t.Fatalf("stale recovery = %v %v",second,err) }
 latest, err := s.Latest(t.Context(),"race"); if err != nil { t.Fatal(err) }
 if latest.Header.ID!=resumed.Header.ID || latest.Body.Checkpoint.State!=Resumed { t.Fatal("stale evidence interrupted the new live attempt") }
 owned, err := s.AttemptOwned(t.Context(),"race")
 if err != nil || !owned { t.Fatal("stale reconciler released the live attempt's ownership") }
}
