package explore_test

import (
 "context"
 "database/sql"
 "errors"
 "os"
 "path/filepath"
 "slices"
 "testing"

 "github.com/atyrode/babel/internal/explore"
 "github.com/atyrode/babel/internal/frontier"
 "github.com/atyrode/babel/internal/run"
 "github.com/atyrode/babel/internal/sync"
)

func TestStopFilePreventsLaunchAndPreservesResumeInputs(t *testing.T) {
 h := newHarness(t)
 stop := filepath.Join(t.TempDir(),"stop")
 if err := os.WriteFile(stop,[]byte("stop"),0600); err != nil { t.Fatal(err) }
 controller := h.controller(nil)
 outcome, err := controller.Explore(t.Context(),explore.Options{RunID:"stopped",Authority:testAuthority,StopFile:stop,Challenge:true})
 if err==nil || outcome==nil || !outcome.Cancelled { t.Fatalf("stop result: %v %v",outcome,err) }
 cp := outcome.Receipt.Body.Checkpoint
 if cp.State!=run.Interrupted || cp.Launch==nil || !cp.Launch.Challenge || outcome.Receipt.Body.Worker!=nil { t.Fatal("stop file launched work or discarded the continuation") }
 interrupted, err := h.runs.Interrupted(t.Context())
 if err != nil || len(interrupted)!=1 { t.Fatalf("interrupted list: %v %v",interrupted,err) }
}

func TestInterruptedClosureIsImmutableAndResumePublishesContinuation(t *testing.T) {
 h := newHarness(t)
 dir := filepath.Dir(h.runs.Path())
 hook := sync.NewStager()
 h.runs.Close(); h.frontier.Close()
 var err error
 h.runs, err = run.Open(dir,run.WithSync(hook)); if err != nil { t.Fatal(err) }
 h.frontier, err = frontier.Open(dir,frontier.WithSync(hook)); if err != nil { t.Fatal(err) }
 payload := h.writeResult("discovery.json",h.discovery())
 controller := h.controller(payloadArgs(map[explore.Stage]string{explore.StageExplore:payload}),func(cfg *explore.Config){cfg.Sync=hook})
 ctx, cancel := context.WithCancel(t.Context()); defer cancel()
 partial, err := controller.Explore(ctx,explore.Options{RunID:"partial-publication",Authority:testAuthority,OnRecord:func(e explore.RecordEvent){cancel()}})
 if !errors.Is(err,context.Canceled) { t.Fatalf("first attempt: %v",err) }
 db, err := sql.Open("sqlite",h.runs.Path()); if err != nil { t.Fatal(err) }; defer db.Close()
 var count int
 if err := db.QueryRow(`SELECT record_count FROM sync_run WHERE run_id='partial-publication'`).Scan(&count); err != nil { t.Fatal(err) }
 var partialBytes []byte
 if err := db.QueryRow(`SELECT payload FROM run_receipt WHERE id=?`,partial.Receipt.Header.ID).Scan(&partialBytes); err != nil { t.Fatal(err) }
 resumed, err := controller.Explore(t.Context(),explore.Options{RunID:"partial-publication",Authority:testAuthority})
 if err != nil { t.Fatal(err) }
 if !slices.Equal(partial.Hypotheses,resumed.Hypotheses) || resumed.Reused!=len(partial.Hypotheses) { t.Fatal("resume duplicated the committed candidates") }
 var after int
 if err := db.QueryRow(`SELECT record_count FROM sync_run WHERE run_id='partial-publication'`).Scan(&after); err != nil { t.Fatal(err) }
 if count!=after { t.Fatal("resume grew an already-declared partial closure") }
 var afterBytes []byte
 if err := db.QueryRow(`SELECT payload FROM run_receipt WHERE id=?`,partial.Receipt.Header.ID).Scan(&afterBytes); err != nil { t.Fatal(err) }
 if !slices.Equal(partialBytes,afterBytes) { t.Fatal("resume rewrote the interrupted receipt") }
 for _, id := range append(slices.Clone(resumed.Observations),string(resumed.Receipt.Header.ID)) {
  var continues string
  if err := db.QueryRow(`SELECT continues_run_id FROM sync_run WHERE run_id=?`,id).Scan(&continues); err != nil { t.Fatal(err) }
  if continues!="partial-publication" { t.Fatalf("record %s lost continuation lineage",id) }
 }
}
