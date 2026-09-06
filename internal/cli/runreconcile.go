package cli

import (
	"context"
	"time"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/presence"
	runstore "github.com/atyrode/babel/internal/run"
)

// Repeated announcements are considered together: even one newer/live row
// excludes a run. Recovery never claims another host's locally cached records.
func (a *app) reconcileHistorical(ctx context.Context, runs *runstore.Store, age time.Duration) ([]runstore.Receipt, error) {
 cfg, _, err := config.Load()
 if err != nil { return nil, err }
 if cfg.Mode != config.ModeShared { return nil, nil }
 host, err := localHostID()
 if err != nil { return nil, err }
 store, err := presence.Open(ctx, cfg, host, a.presenceDiag)
 if err != nil { return nil, err }; defer store.Close()
 rows, err := store.Fleet(ctx)
 if err != nil { return nil, err }
 if age < presence.LostAfter { age=presence.LostAfter }
 excluded := map[string]bool{}
 newest := map[string]presence.Row{}
 for _, row := range rows {
  if !row.Local || row.State != presence.StateRunning || row.HeartbeatAge < age || row.ReceiptRecordID!="" { excluded[row.RunID]=true }
  if row.Kind != presence.KindExplore { continue }
  prior, exists := newest[row.RunID]
  if !exists || row.HeartbeatAt.After(prior.HeartbeatAt) { newest[row.RunID]=row }
 }
 out := []runstore.Receipt{}
 for id, row := range newest {
  if excluded[id] || row.PreparationID=="" { continue }
  receipt, err := runs.RecoverHistorical(ctx, id, runstore.PreparationID(row.PreparationID), row.Authority, row.Recipe, row.StartedAt, runstore.ReconcileOptions{
   Publish: func(commit context.Context, id string) error { a.publishLifecycle(commit, id); return nil },
  })
  if err != nil { return out, err }
  if receipt != nil { out=append(out,*receipt) }
 }
 return out, nil
}
