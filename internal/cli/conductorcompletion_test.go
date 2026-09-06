package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/explore"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

type completionBlockedLedger struct{ err error }

func (l completionBlockedLedger) SpentSince(context.Context, time.Time, string) (conductor.Spend, error) {
	return conductor.Spend{}, l.err
}

type completionUnusedRung struct{}

func (completionUnusedRung) Name() string { return conductor.RungInvitation }
func (completionUnusedRung) Depth(context.Context) (conductor.Depth, error) {
	return conductor.Depth{}, nil
}
func (completionUnusedRung) Draw(context.Context, conductor.DrawRequest) (conductor.Assignment, error) {
	panic("completed receipt recovery must not draw work")
}

func TestConductorRecoversReceiptBeforeCycleFinalizationWithoutInference(t *testing.T) {
	for _, tc := range []struct {
		name, failure string
		cancelled     bool
		outcome       conductor.Outcome
	}{
		{"success-with-warning", "", false, conductor.OutcomeRan},
		{"failed", "recorded execution failure", false, conductor.OutcomeFailed},
		{"deliberately-closed-interruption", "operator cancellation", true, conductor.OutcomeFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			started, finished := now.Add(-2*time.Minute), now.Add(-time.Minute)
			store, err := runstore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			prep, err := runstore.NewPreparation(started, []runstore.Selected{{
				Host: "test-host", Harness: event.HarnessOMP, SourceID: "test-session",
				CaptureDigest: digest.Bytes([]byte("capture")), SourceDigest: digest.Bytes([]byte("source")),
				Adapter: runstore.AdapterRef{Schema: 1, Version: "test"},
			}}, runstore.PreparationContext{})
			if err != nil {
				t.Fatal(err)
			}
			authority := runstore.Authority{Kind: runstore.AuthorityOperator, Ref: "invitation:crash-window"}
			receipt, err := runstore.NewReceipt(runstore.NewReceiptID(), "completed-cycle", prep, authority, runstore.Body{
				Cookbook:   []runstore.CookbookAsset{{Kind: runstore.AssetLens, Ref: worker.RecipeRef{ID: "test", Version: 1}}},
				Job:        runstore.JobVersions{Job: 1, Prompt: "test", Schema: worker.ResultSchema},
				Policy:     runstore.PolicyVersions{Redaction: "test", Disclosure: "test"},
				Worker:     &worker.Receipt{Cost: worker.Cost{EstimatedRun: 0.25, Currency: "USD"}},
				Timing:     runstore.Timing{StartedAt: started, FinishedAt: finished},
				Failures:   []runstore.Failure{{Stage: "explore", Code: explore.FailureAuthority, Message: "recorded diagnostic", At: finished}},
				Checkpoint: &runstore.Checkpoint{State: runstore.Closed, Verdict: &runstore.Verdict{Failure: tc.failure, Cancelled: tc.cancelled}},
			}, finished)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.PutReceipt(t.Context(), receipt); err != nil {
				t.Fatal(err)
			}
			journal, err := conductor.OpenJournal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Record(conductor.Cycle{
				Seq: 1, StartedAt: started, Outcome: conductor.OutcomeRunning, RunID: receipt.Header.RunID,
				Rung: conductor.RungInvitation, Authority: authority, PID: 2147483647,
			}); err != nil {
				t.Fatal(err)
			}
			blocked := errors.New("next cycle budget unavailable")
			loop, err := conductor.New(conductor.Config{
				Ceilings: conductor.Ceilings{PerCycle: 1, PerDay: 2, Currency: "USD"},
				Ladder:   []conductor.Rung{completionUnusedRung{}},
				// No app, worker or corpus exists. Calling Run would fail; only
				// the real durable completion projection can repair this cycle.
				Runner: &conductorRunner{state: &analysisState{runs: store}},
				Ledger: completionBlockedLedger{err: blocked}, Journal: journal, Now: func() time.Time { return now },
			})
			if err != nil {
				t.Fatal(err)
			}
			cycle, err := loop.Once(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if cycle.Outcome != tc.outcome || cycle.Reason != tc.failure || cycle.Cancelled != tc.cancelled || cycle.Failures != 1 {
				t.Fatalf("recovered verdict changed: %+v", cycle)
			}
			if cycle.Cost != 0.25 || cycle.Currency != "USD" || cycle.ReceiptID != string(receipt.Header.ID) || cycle.PreparationID != string(prep.ID) || !cycle.FinishedAt.Equal(finished) || !cycle.StartedAt.Equal(started) {
				t.Fatalf("recovery changed the durable result: %+v", cycle)
			}
			if _, err := loop.Once(t.Context()); !errors.Is(err, blocked) {
				t.Fatalf("next cycle should enforce its own budget: %v", err)
			}
			last, ok := journal.Last()
			if !ok || last.Seq != cycle.Seq || last.Outcome != cycle.Outcome || last.ReceiptID != cycle.ReceiptID {
				t.Fatal("a second recovery rewrote or reran the finalized cycle")
			}
		})
	}
}
