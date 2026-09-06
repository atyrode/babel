package cli

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

type completionAllowedLedger struct{}

func (completionAllowedLedger) SpentSince(context.Context, time.Time, string) (conductor.Spend, error) {
	return conductor.Spend{}, nil
}

func TestConductorRestoresOwnedAttemptWithoutPreparingAgain(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fakeengine")
	build := exec.Command("go", "build", "-o", binary, "github.com/atyrode/babel/internal/worker/testdata/fakeengine")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build synthetic worker: %v\n%s", err, output)
	}
	for _, mode := range []string{"dead-stale", "live-stale", "dead-fresh", "already-reconciled"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			f.threeSessions()
			output, _ := f.ok("prepare", "--json")
			prepared := decode[prepareResult](t, output)
			state, err := openAnalysisState()
			if err != nil {
				t.Fatal(err)
			}
			defer state.Close()
			prep, err := state.runs.Preparation(t.Context(), runstore.PreparationID(prepared.PreparationID))
			if err != nil {
				t.Fatal(err)
			}
			set, err := recipeSet([]string{"outcome-integrity"})
			if err != nil {
				t.Fatal(err)
			}
			assets := []runstore.CookbookAsset{}
			for _, recipe := range set.All() {
				assets = append(assets, runstore.CookbookAsset{Kind: runstore.AssetLens, Ref: worker.RecipeRef{ID: recipe.ID, Version: recipe.Version}})
			}
			started := time.Now().UTC().Add(-time.Hour)
			authority := runstore.Authority{Kind: runstore.AuthorityOperator, Ref: "invitation:resume-original"}
			phase := runstore.Running
			if mode == "already-reconciled" {
				phase = runstore.Interrupted
			}
			recordedProfile := worker.ProfileRef{ID: "recorded-profile", Revision: 7}
			receipt, err := runstore.NewReceipt(runstore.NewReceiptID(), "owned-cycle", prep, authority, runstore.Body{
				Cookbook: assets, Job: runstore.JobVersions{Job: 1, Prompt: "original", Schema: worker.ResultSchema},
				Policy:     runstore.PolicyVersions{Redaction: "original", Disclosure: "original"},
				Timing:     runstore.Timing{StartedAt: started, FinishedAt: started},
				Checkpoint: &runstore.Checkpoint{State: phase, Launch: &runstore.Launch{Profile: recordedProfile, Recipes: set.IDs(), Develop: 2, Retrievals: 3}},
			}, started)
			if err != nil {
				t.Fatal(err)
			}
			if err := state.runs.PutReceipt(t.Context(), receipt); err != nil {
				t.Fatal(err)
			}
			if mode != "already-reconciled" {
				db, err := sql.Open("sqlite", state.runs.Path())
				if err != nil {
					t.Fatal(err)
				}
				host, err := os.Hostname()
				if err != nil {
					t.Fatal(err)
				}
				pid, beat := 2147483647, started
				if mode == "live-stale" {
					pid = os.Getpid()
				}
				if mode == "dead-fresh" {
					beat = time.Now().UTC()
				}
				_, err = db.Exec(`INSERT INTO run_lease VALUES(?,?,?,?)`, receipt.Header.RunID, host, pid, beat.Format("2006-01-02T15:04:05.000000000Z07:00"))
				db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			journal, err := conductor.OpenJournal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := journal.Record(conductor.Cycle{Seq: 1, StartedAt: started, Outcome: conductor.OutcomeRunning, RunID: receipt.Header.RunID,
				Rung: conductor.RungInvitation, Authority: authority, PID: 2147483647,
				Sessions: []string{"omp/not-the-original-scope"}, Recipes: []string{"not-the-recorded-recipe"},
			}); err != nil {
				t.Fatal(err)
			}
			payload, prompt := filepath.Join(t.TempDir(), "result.json"), filepath.Join(t.TempDir(), "prompt.json")
			if err := os.WriteFile(payload, []byte(`{"candidates":[]}`), 0600); err != nil {
				t.Fatal(err)
			}
			runner := &conductorRunner{app: &app{stdout: io.Discard, stderr: io.Discard}, state: state,
				worker:  worker.Config{Binary: binary, Args: []string{"-submit", payload, "-prompt-file", prompt}},
				profile: worker.ProfileRef{ID: "changed-current-profile", Revision: 1}, host: "different-current-host",
			}
			loop, err := conductor.New(conductor.Config{Ceilings: conductor.Ceilings{PerCycle: 1, PerDay: 2, Currency: "USD"},
				Ladder: []conductor.Rung{completionUnusedRung{}}, Runner: runner, Ledger: completionAllowedLedger{}, Journal: journal,
			})
			if err != nil {
				t.Fatal(err)
			}
			cycle, err := loop.Once(t.Context())
			if mode == "live-stale" || mode == "dead-fresh" {
				if !errors.Is(err, runstore.ErrAttemptOwned) {
					t.Fatalf("owned run must remain pending: %v", err)
				}
				latest, readErr := state.runs.Latest(t.Context(), receipt.Header.RunID)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if latest.Header.ID != receipt.Header.ID {
					t.Fatal("owned attempt's receipt was amended")
				}
				last, ok := journal.Last()
				if !ok || last.Outcome != conductor.OutcomeRunning || last.RunID != receipt.Header.RunID {
					t.Fatal("pending claimed cycle was finalized")
				}
				if _, err := os.Stat(prompt); !os.IsNotExist(err) {
					t.Fatal("owned attempt launched another worker")
				}
				if _, err := loop.Once(t.Context()); !errors.Is(err, runstore.ErrAttemptOwned) {
					t.Fatal("retry lost the pending ownership boundary")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cycle.Outcome != conductor.OutcomeRan || cycle.PreparationID != string(prep.ID) {
				t.Fatalf("recovered cycle did not retain its preparation: %+v", cycle)
			}
			latest, err := state.runs.Latest(t.Context(), receipt.Header.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if latest.Body.Checkpoint.State != runstore.Closed || latest.Header.PreparationID != prep.ID || latest.Body.Worker == nil || latest.Body.Worker.Profile != recordedProfile {
				t.Fatal("resumed worker did not execute the original scope and profile")
			}
			if len(latest.Body.Worker.Recipes) != len(assets) {
				t.Fatal("resume substituted the current scheduling recipes")
			}
			for i, ref := range latest.Body.Worker.Recipes {
				if ref != assets[i].Ref {
					t.Fatal("resume changed a recorded recipe version")
				}
			}
		})
	}
}
