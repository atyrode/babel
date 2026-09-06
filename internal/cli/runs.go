package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

 runstore "github.com/atyrode/babel/internal/run"
 babelsync "github.com/atyrode/babel/internal/sync"
)

const runsUsage = `Usage: babel runs <interrupted|reconcile|resume ID|close ID> [flags]

interrupted lists durable partial runs; reconcile records unknown process loss
for stale, locally owned attempts whose processes no longer exist. Neither
command guesses quota exhaustion or a kill signal. resume uses the recorded
preparation and typed launch inputs with the currently configured worker;
close deliberately ends an interrupted run without launching anything.

Flags:
  --json              emit receipts/outcome as JSON
  --stale-after D     reconcile heartbeat age (default 5m; minimum 1m)
  --stop-file PATH    resume: stop at the next safe point when this file exists
`

func (a *app) runsCmd(ctx context.Context, args []string) error {
 c := newCmd("runs", runsUsage)
 if len(args)==0 { return c.usagef("runs requires a command") }
 verb := args[0]
 args = args[1:]
 var id string
 if verb=="resume" || verb=="close" {
  if len(args)==0 { return c.usagef("%s requires a run ID", verb) }
  id, args = args[0], args[1:]
 } else if verb!="interrupted" && verb!="reconcile" { return c.usagef("unknown runs command") }
 asJSON := c.fs.Bool("json", false, "emit JSON")
 stale := c.fs.Duration("stale-after", runstore.ReconcileStaleAfter, "minimum stale heartbeat age")
 stopFile := c.fs.String("stop-file", "", "stop at a safe point when this file exists")
 if err := c.parse(a, args); err != nil { return err }
 if err := c.noArgs(); err != nil { return err }
 if *stale < time.Minute { return c.usagef("stale-after must be at least one minute") }
 state, err := openAnalysisState()
 if err != nil { return err }; defer state.Close()
 var receipts []runstore.Receipt
 switch verb {
 case "interrupted": receipts, err = state.runs.Interrupted(ctx)
 case "reconcile":
  receipts, err = state.runs.Reconcile(ctx, time.Now().Add(-*stale), runstore.ReconcileOptions{
   Publish: func(commit context.Context, id string) error { a.publishLifecycle(commit, id); return nil },
  })
  if err==nil {
   var recovered []runstore.Receipt
   recovered, err = a.reconcileHistorical(ctx, state.runs, *stale)
   receipts = append(receipts, recovered...)
  }
 case "close":
  var receipt runstore.Receipt
  receipt, err = state.runs.CloseInterrupted(ctx, id)
  if err==nil { receipts = append(receipts, receipt) }
 case "resume":
  receipt, err := state.runs.Latest(ctx, id)
  if err != nil { return err }
  settings, err := loadAnalysisSettings()
  if err != nil { return err }
  var wf workerFlags
  wcfg, ok := wf.resolve(settings)
  if !ok { return a.reportNoWorker() }
  if err := refuseDials(c, wcfg.Args, true); err != nil { return err }
  ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
  defer stop()
  announcer, release := a.openPresence(ctx); defer release()
  plan, err := recordedExplorePlan(receipt, wcfg)
  if err != nil { return err }
  plan.stopFile, plan.presence = *stopFile, announcer
  res, outcome, runErr := a.runExploration(ctx, state, plan)
  if outcome==nil { return runErr }
  if *asJSON { if err := a.emitJSON(res); err != nil { return err } } else { a.writeExplore(res) }
  return runErr
 }
 if err != nil { return err }
 if verb=="close" { for _, r := range receipts { a.publishLifecycle(context.WithoutCancel(ctx), r.Header.RunID) } }
 if *asJSON { return a.emitJSON(receipts) }
 for _, r := range receipts { fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%s\n", Sanitize(r.Header.RunID), r.Body.Checkpoint.State, Sanitize(string(r.Preparation.ID)), Sanitize(r.Body.Checkpoint.Reason)) }
 return nil
}

func (a *app) publishLifecycle(ctx context.Context, id string) {
	d, err := babelDirs()
	if err != nil {
		a.diagf("babel: publish interrupted run: %s\n", Sanitize(err.Error()))
		return
	}
	pub, cleanup, err := a.openPublisher(ctx, d)
	defer cleanup()
	if err == nil {
		err = pub.CommitInline(ctx, babelsync.Closure{RunID: id})
	}
	// Append already declared resumed records and amendments as continuations.
	// The existing publication retry carries those alongside the partial closure.
	if err == nil {
		_, err = pub.Retry(ctx)
	}
	if err != nil {
		a.diagf("babel: partial run remains pending-sync: %s\n", Sanitize(err.Error()))
	}
}
