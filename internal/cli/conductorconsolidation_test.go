package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// The consolidation share is what turns a frontier into findings. Until it
// existed a loop left running explored fresh sessions forever: every cycle
// added candidates, no cycle attacked them, and the operator's frontier held
// sixteen hundred deferred hypotheses and no findings at all.
//
// What this case drives is the whole path through the shipped command layer:
// candidates a finite run deferred, the corpus resolver that says which
// sessions they came out of, the real preparation, and the real worker
// protocol. What it asserts is that the cycle started from those candidates
// and reached the synthesizer, because a consolidation cycle that stopped at
// discovery would defer them again and report success.
func TestConsolidationCycleSeedsFromTheFrontierAndReachesTheSynthesizer(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "fakeengine")
	build := exec.Command("go", "build", "-o", binary, "github.com/atyrode/babel/internal/worker/testdata/fakeengine")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build synthetic worker: %v\n%s", err, output)
	}
	f := newFixture(t)
	f.threeSessions()
	output, _ := f.ok("prepare", "--json")
	prepared := decode[prepareResult](t, output)

	state, err := openAnalysisState()
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := t.Context()
	prep, err := state.runs.Preparation(ctx, runstore.PreparationID(prepared.PreparationID))
	if err != nil {
		t.Fatal(err)
	}

	// The receipt of the run that produced the candidates is what makes their
	// corpus recoverable: a consolidation cycle reads the sessions its roots
	// came out of rather than the whole host.
	started := time.Now().UTC().Add(-time.Hour)
	set, err := recipeSet([]string{"outcome-integrity"})
	if err != nil {
		t.Fatal(err)
	}
	assets := []runstore.CookbookAsset{}
	for _, recipe := range set.All() {
		assets = append(assets, runstore.CookbookAsset{Kind: runstore.AssetLens,
			Ref: worker.RecipeRef{ID: recipe.ID, Version: recipe.Version}})
	}
	origin, err := runstore.NewReceipt(runstore.NewReceiptID(), "run-origin", prep,
		runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:origin"}, runstore.Body{
			Cookbook:   assets,
			Job:        runstore.JobVersions{Job: 1, Prompt: "origin", Schema: worker.ResultSchema},
			Policy:     runstore.PolicyVersions{Redaction: "origin", Disclosure: "origin"},
			Timing:     runstore.Timing{StartedAt: started, FinishedAt: started},
			Checkpoint: &runstore.Checkpoint{State: runstore.Closed},
		}, started)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.runs.PutReceipt(ctx, origin); err != nil {
		t.Fatal(err)
	}
	var deferred []string
	for _, statement := range []string{"the first deferred candidate", "the second deferred candidate"} {
		h, err := state.frontier.CreateHypothesis(ctx, frontier.HypothesisInput{
			RunID:   "run-origin",
			Payload: frontier.HypothesisPayload{Statement: statement, Priority: 0.9},
		})
		if err != nil {
			t.Fatal(err)
		}
		deferred = append(deferred, h.ID)
	}
	if _, err := state.frontier.DeferFrontier(ctx, "run-origin", deferred,
		"the finite pass ran out of budget"); err != nil {
		t.Fatal(err)
	}

	payload := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(payload, []byte(`{"candidates":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	prompts := filepath.Join(t.TempDir(), "prompts.jsonl")
	journal, err := conductor.OpenJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := &conductorRunner{
		app:   &app{stdout: io.Discard, stderr: io.Discard},
		state: state,
		worker: worker.Config{Binary: binary,
			Args: []string{"-submit", payload, "-record", prompts}},
		profile:    worker.ProfileRef{ID: "synthetic-profile", Revision: 1},
		host:       testHostID,
		adapters:   adapters(),
		challenge:  true,
		synthesize: true,
	}
	loop, err := conductor.New(conductor.Config{
		Ceilings: conductor.Ceilings{Currency: "USD", PerCycle: 1, PerDay: 2},
		// A floor this wide never comes due, so the cycle under test is the
		// consolidation share rather than a chaotic draw.
		Floor:  conductor.Floor{OneIn: 100},
		Ladder: []conductor.Rung{conductor.NewAbsentRung(conductor.RungInvitation, "planted empty")},
		Consolidation: conductor.Consolidation{OneIn: 1,
			Rung: conductor.NewConsolidationRung(state.frontier,
				conductor.NewRecordOrigins(state.frontier, state.runs), nil, 5)},
		Runner: runner, Ledger: completionAllowedLedger{}, Journal: journal,
	})
	if err != nil {
		t.Fatal(err)
	}

	cycle, err := loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeRan || cycle.Rung != conductor.RungConsolidation {
		t.Fatalf("cycle = %+v, want a consolidation cycle that ran", cycle)
	}
	if !slices.Equal(cycle.Roots, deferred) {
		t.Errorf("cycle seeded from %v, want the deferred candidates %v", cycle.Roots, deferred)
	}
	if len(cycle.Sessions) == 0 {
		t.Error("the cycle read the whole host rather than the corpus its candidates came from")
	}
	receipt, err := state.runs.Latest(ctx, cycle.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(receipt.Body.Checkpoint.Launch.Roots, deferred) {
		t.Errorf("the run recorded roots %v, want %v", receipt.Body.Checkpoint.Launch.Roots, deferred)
	}

	// The synthesizer is the only writer of findings and proposals, so a
	// consolidation cycle that never reaches it consolidates nothing. Its
	// job is visible in what the run actually sent the worker.
	recorded, err := os.ReadFile(prompts)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []explore.Stage{explore.StageExplore, explore.StageChallenge, explore.StageSynthesize} {
		// internal/explore states a job's stage in the prompt's own
		// [babel-params] block, which is what the recorded stdin holds.
		want := explore.ParamStage + " = " + string(stage)
		if !strings.Contains(string(recorded), want) {
			t.Errorf("no worker job ran the %s stage", stage)
		}
	}
}

// A consolidation cycle refuses when the challenger is not authorized, rather
// than degrading into another discovery pass. §5.4 promotes nothing a
// skeptical pass has not attacked, and a cycle that silently explored instead
// would answer "drain the frontier" by adding to it.
func TestConsolidationRefusesWithoutTheChallenger(t *testing.T) {
	runner := &conductorRunner{app: &app{stdout: io.Discard, stderr: io.Discard}}
	for _, tc := range []struct {
		name                  string
		challenge, synthesize bool
	}{
		{"neither stage", false, false},
		{"the challenger alone", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner.challenge, runner.synthesize = tc.challenge, tc.synthesize
			_, err := runner.Run(context.Background(), "run-consolidate",
				conductor.Assignment{Rung: conductor.RungConsolidation, Roots: []string{"h-1"}})
			if err == nil {
				t.Fatal("the cycle ran as a discovery pass instead of refusing")
			}
			if !strings.Contains(err.Error(), "challenger") || !strings.Contains(err.Error(), "synthesizer") {
				t.Errorf("the refusal does not name what is missing: %v", err)
			}
		})
	}
}

// The command refuses the same thing at the door, where an operator can still
// fix it by naming the two flags. A loop that started and failed every
// consolidation cycle would have been technically correct and useless.
func TestConductorRunRefusesAConsolidationShareWithoutBothStages(t *testing.T) {
	f := newFixture(t)
	f.ok("conductor", "configure", "--per-cycle", "0.50", "--per-day", "5.00", "--consolidate", "3")
	_, stderr := f.mustExit(exitUsage, "conductor", "run", "--once")
	if !strings.Contains(stderr, "--challenge") || !strings.Contains(stderr, "--synthesize") {
		t.Errorf("the refusal does not say how to run a consolidation cycle: %q", stderr)
	}

	// And the share is a dial the operator can turn back down for one
	// invocation rather than a configuration they have to rewrite.
	_, stderr, code := f.run("conductor", "run", "--once", "--consolidate", "0")
	if code == exitUsage {
		t.Errorf("--consolidate 0 was refused as if it scheduled consolidation: %q", stderr)
	}
}

// A share the operator can raise and never withdraw would be a dial that
// turns one way. Zero is off and off has to be sayable, which is why the flag
// is read as named rather than as non-zero.
func TestConductorConfigureWithdrawsTheConsolidationShare(t *testing.T) {
	f := newFixture(t)
	stdout, _ := f.ok("conductor", "configure", "--per-cycle", "0.50", "--per-day", "5.00",
		"--consolidate", "3", "--json")
	if got := decode[conductorConfigResult](t, stdout).ConsolidateOneIn; got != 3 {
		t.Fatalf("stored share = %d, want 3", got)
	}
	// An invocation that adjusts an unrelated dial leaves the share alone.
	stdout, _ = f.ok("conductor", "configure", "--interval", "10m", "--json")
	if got := decode[conductorConfigResult](t, stdout).ConsolidateOneIn; got != 3 {
		t.Errorf("an unrelated change moved the share to %d", got)
	}
	stdout, _ = f.ok("conductor", "configure", "--consolidate", "0", "--json")
	if got := decode[conductorConfigResult](t, stdout).ConsolidateOneIn; got != 0 {
		t.Errorf("share = %d after being withdrawn", got)
	}
}
