package conductor_test

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	runstore "github.com/atyrode/babel/internal/run"
)

// day is the instant every test's clock starts at, and the day the per-day
// ceiling covers.
var day = time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

// testCeilings are ceilings wide enough that the budget is never the thing
// under test. The tests that are about the budget state their own.
var testCeilings = conductor.Ceilings{Currency: "USD", PerCycle: 0.50, PerDay: 5.00}

// recordedRun is one call the loop made into the runner.
type recordedRun struct {
	runID      string
	assignment conductor.Assignment
}

// fakeRunner stands in for the prepare-and-explore path. Every test in this
// file is about scheduling, and a real run would make the thing under test the
// slowest part of the test.
type fakeRunner struct {
	runs   []recordedRun
	result conductor.Result
	err    error
	// hook, when set, replaces the canned answer. It is how a crash mid-run is
	// simulated: the journal has already recorded the cycle as running, so a
	// panic here leaves exactly the state a killed conductor leaves.
	hook func(runID string, a conductor.Assignment) (conductor.Result, error)
}

func (r *fakeRunner) Run(_ context.Context, runID string, a conductor.Assignment) (conductor.Result, error) {
	r.runs = append(r.runs, recordedRun{runID: runID, assignment: a})
	if r.hook != nil {
		return r.hook(runID, a)
	}
	result := r.result
	if result.ReceiptID == "" {
		result.ReceiptID = fmt.Sprintf("rcpt-%d", len(r.runs))
	}
	return result, r.err
}

// fakeLedger reports a planted day's spend.
type fakeLedger struct {
	spend conductor.Spend
	err   error
}

func (l fakeLedger) SpentSince(context.Context, time.Time, string) (conductor.Spend, error) {
	return l.spend, l.err
}

// stubRung is a rung with a planted answer, so a ladder test can state exactly
// which rungs have work.
type stubRung struct {
	name  string
	work  *conductor.Assignment
	draws int
}

func (s *stubRung) Name() string { return s.name }

func (s *stubRung) Depth(context.Context) (conductor.Depth, error) {
	waiting := 0
	if s.work != nil {
		waiting = 1
	}
	return conductor.Depth{Waiting: waiting, Implemented: true, Note: "planted"}, nil
}

func (s *stubRung) Draw(_ context.Context, d conductor.DrawRequest) (conductor.Assignment, error) {
	s.draws++
	if s.work == nil {
		return conductor.Assignment{}, conductor.ErrNoWork
	}
	a := *s.work
	a.Rung = s.name
	if a.Authority.Ref == "" {
		a.Authority = runstore.Authority{Kind: runstore.AuthorityOperator, Ref: "invitation:" + d.RunID}
	}
	return a, nil
}

func testJournal(t *testing.T) *conductor.Journal {
	t.Helper()
	j, err := conductor.OpenJournal(t.TempDir())
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	return j
}

// clock is a hand-advanced clock, so a test can place cycles in a day without
// waiting for one.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time {
	c.now = c.now.Add(time.Minute)
	return c.now
}

// The ladder is ordered, and the operator outranks the loop. Rung one is the
// operator's invitations; a rung is reached only when everything above it is
// empty; and an unimplemented rung contributes nothing rather than swallowing a
// cycle.
func TestLadderIsConsultedInPrecedenceOrder(t *testing.T) {
	ctx := context.Background()
	invitations := &stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{
		Invitation: "inv-1",
		Note:       "an operator asked",
	}}
	floor := &stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
		Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d-1"},
		Note:      "no aim",
	}}
	runner := &fakeRunner{}
	clk := &clock{now: day}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Ladder:   []conductor.Rung{invitations, conductor.PolicyRung(), floor},
		Runner:   runner,
		Ledger:   fakeLedger{},
		Journal:  testJournal(t),
		Now:      clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// With work on rung one, that is what runs — the loop's own chaos does not
	// outrank a person, and the floor is not yet due.
	cycle, err := loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Rung != conductor.RungInvitation {
		t.Fatalf("first cycle drew from %q, want the operator's invitations", cycle.Rung)
	}
	if cycle.Authority.Kind != runstore.AuthorityOperator || cycle.Invitation != "inv-1" {
		t.Errorf("invitation cycle authority = %+v, invitation %q", cycle.Authority, cycle.Invitation)
	}
	if cycle.Outcome != conductor.OutcomeRan || cycle.ReceiptID == "" {
		t.Errorf("cycle = %+v, want a completed run with a receipt", cycle)
	}

	// Empty the operator's queue and the floor is where the cycle lands. The
	// absent policy rung between them changes nothing, which is the point of
	// declaring it.
	invitations.work = nil
	cycle, err = loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Rung != conductor.RungSerendipity {
		t.Fatalf("second cycle drew from %q, want the serendipity floor", cycle.Rung)
	}
	if cycle.Authority.Kind != runstore.AuthoritySerendipity {
		t.Errorf("floor cycle authority = %+v, want a declared draw", cycle.Authority)
	}

	// Nothing anywhere: the loop says so rather than inventing work.
	floor.work = nil
	cycle, err = loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeIdle || cycle.Rung != "" {
		t.Errorf("cycle with an empty ladder = %+v, want idle", cycle)
	}
	if len(runner.runs) != 2 {
		t.Errorf("the runner was called %d times, want 2: an idle cycle runs nothing", len(runner.runs))
	}
}

// An unimplemented rung reports its absence rather than a queue depth of zero.
// "No policy is waiting" and "this build has no policies" are different answers
// to why the loop is doing what it is doing.
func TestAbsentRungIsVisibleRatherThanEmpty(t *testing.T) {
	rungs, err := conductor.Describe(context.Background(), []conductor.Rung{
		&stubRung{name: conductor.RungInvitation},
		conductor.PolicyRung(),
	})
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if len(rungs) != 2 {
		t.Fatalf("Describe returned %d rungs", len(rungs))
	}
	if !rungs[0].Depth.Implemented {
		t.Error("the invitation rung reported itself unimplemented")
	}
	policy := rungs[1]
	if policy.Name != conductor.RungPolicy || policy.Depth.Implemented {
		t.Errorf("policy rung = %+v, want a declared absence", policy)
	}
	if !strings.Contains(policy.Depth.Note, "not implemented") &&
		!strings.Contains(policy.String(), "not implemented") {
		t.Errorf("policy rung renders as %q, which does not say it is absent", policy.String())
	}
}

// The serendipity floor is a protected fraction, not a last resort. With rung
// one permanently full, the loop must still spend the configured share of its
// cycles on chaos — and must never let more than the configured number of
// dutiful cycles pass in a row.
func TestSerendipityFloorHoldsItsRatioAgainstAFullQueue(t *testing.T) {
	ctx := context.Background()
	for _, oneIn := range []int{2, 4, 8} {
		t.Run(fmt.Sprintf("one in %d", oneIn), func(t *testing.T) {
			invitations := &stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{
				Invitation: "inv-always",
			}}
			floor := &stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
				Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d"},
			}}
			clk := &clock{now: day}
			journal := testJournal(t)
			loop, err := conductor.New(conductor.Config{
				Ceilings: conductor.Ceilings{Currency: "USD", PerCycle: 0.01, PerDay: 1000},
				Floor:    conductor.Floor{OneIn: oneIn},
				Ladder:   []conductor.Rung{invitations, conductor.PolicyRung(), floor},
				Runner:   &fakeRunner{},
				Ledger:   fakeLedger{},
				Journal:  journal,
				Now:      clk.Now,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			const cycles = 64
			chaotic, dutifulRun, worstRun := 0, 0, 0
			for range cycles {
				cycle, err := loop.Once(ctx)
				if err != nil {
					t.Fatalf("Once: %v", err)
				}
				if cycle.Rung == conductor.RungSerendipity {
					chaotic++
					dutifulRun = 0
					continue
				}
				if cycle.Rung != conductor.RungInvitation {
					t.Fatalf("cycle drew from %q with a full queue", cycle.Rung)
				}
				dutifulRun++
				worstRun = max(worstRun, dutifulRun)
			}

			// The guarantee: at least one chaotic cycle in every oneIn.
			if worstRun > oneIn-1 {
				t.Errorf("%d dutiful cycles ran in a row, which breaks a one-in-%d floor",
					worstRun, oneIn)
			}
			want := cycles / oneIn
			if chaotic < want {
				t.Errorf("%d of %d cycles were chaotic, below the one-in-%d floor of %d",
					chaotic, cycles, oneIn, want)
			}
			// And it is a floor rather than a mode: a full queue still gets
			// most of the loop's attention.
			if oneIn > 1 && chaotic > cycles/2 {
				t.Errorf("%d of %d cycles were chaotic with a full queue, which is a mode, not a floor",
					chaotic, cycles)
			}
		})
	}
}

// The floor's arithmetic is read from the journal, so the ratio survives a
// restart. A conductor that counted in memory would give every restart three
// free dutiful cycles.
func TestFloorSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	invitations := &stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{Invitation: "inv-1"}}
	floor := &stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
		Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d"},
	}}
	clk := &clock{now: day}

	drawn := make([]string, 0, 6)
	for range 6 {
		// A fresh journal handle and a fresh conductor every cycle: this is a
		// process that is restarted between every cycle.
		journal, err := conductor.OpenJournal(dir)
		if err != nil {
			t.Fatalf("OpenJournal: %v", err)
		}
		loop, err := conductor.New(conductor.Config{
			Ceilings: testCeilings,
			Floor:    conductor.Floor{OneIn: 3},
			Ladder:   []conductor.Rung{invitations, conductor.PolicyRung(), floor},
			Runner:   &fakeRunner{},
			Ledger:   fakeLedger{},
			Journal:  journal,
			Now:      clk.Now,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		cycle, err := loop.Once(ctx)
		if err != nil {
			t.Fatalf("Once: %v", err)
		}
		drawn = append(drawn, cycle.Rung)
	}
	// One in three, held across six restarts: no three dutiful cycles in a row.
	run := 0
	chaotic := 0
	for _, rung := range drawn {
		if rung == conductor.RungSerendipity {
			chaotic++
			run = 0
			continue
		}
		run++
		if run >= 3 {
			t.Fatalf("restarting reset the floor: cycles were %v", drawn)
		}
	}
	if chaotic < 2 {
		t.Errorf("only %d of 6 restarted cycles were chaotic: %v", chaotic, drawn)
	}
}

// A seeded draw is reproducible: the slice, the recipe and the identity the
// draw is recorded under all come from the same generator. A declared draw
// nobody can replay is a weaker claim than it looks.
func TestSerendipityDrawIsReproducibleFromItsSeed(t *testing.T) {
	ctx := context.Background()
	corpus := fixedCorpus{"omp/a", "omp/b", "codex/c", "claude/d"}
	recipes := fixedRecipes{"effective-patterns", "outcome-integrity"}

	draw := func() conductor.Assignment {
		rung := conductor.NewSerendipityRung(corpus, recipes, rand.New(rand.NewPCG(7, 11)), 3)
		a, err := rung.Draw(ctx, conductor.DrawRequest{RunID: "run-1", At: day})
		if err != nil {
			t.Fatalf("Draw: %v", err)
		}
		return a
	}
	first, second := draw(), draw()
	if first.Authority != second.Authority {
		t.Errorf("the same seed drew authorities %+v and %+v", first.Authority, second.Authority)
	}
	if strings.Join(first.Sessions, " ") != strings.Join(second.Sessions, " ") {
		t.Errorf("the same seed drew slices %v and %v", first.Sessions, second.Sessions)
	}
	if strings.Join(first.Recipes, " ") != strings.Join(second.Recipes, " ") {
		t.Errorf("the same seed drew recipes %v and %v", first.Recipes, second.Recipes)
	}

	// The draw is bounded, declared, and names its own identity.
	if len(first.Sessions) == 0 || len(first.Sessions) > 3 {
		t.Errorf("drew %d sessions, want between 1 and the bound of 3", len(first.Sessions))
	}
	if first.Authority.Kind != runstore.AuthoritySerendipity {
		t.Errorf("draw authority kind = %q", first.Authority.Kind)
	}
	id, ok := strings.CutPrefix(first.Authority.Ref, "draw:")
	if !ok || len(id) != 26 {
		t.Errorf("draw reference = %q, want a 26-character ULID behind \"draw:\"", first.Authority.Ref)
	}
	if len(first.Recipes) != 1 {
		t.Errorf("drew %d recipes, want exactly one", len(first.Recipes))
	}

	// A different seed draws differently, or the seed is not doing anything.
	other := conductor.NewSerendipityRung(corpus, recipes, rand.New(rand.NewPCG(99, 5)), 3)
	a, err := other.Draw(ctx, conductor.DrawRequest{RunID: "run-1", At: day})
	if err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if a.Authority.Ref == first.Authority.Ref {
		t.Error("two seeds produced the same draw identity")
	}
}

// An empty corpus or an empty cookbook is a floor with nothing to draw from,
// which is idleness rather than an invented run.
func TestSerendipityNeedsACorpusAndARecipe(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		corpus  fixedCorpus
		recipes fixedRecipes
	}{
		{"no sessions", nil, fixedRecipes{"effective-patterns"}},
		{"no default-enabled recipe", fixedCorpus{"omp/a"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rung := conductor.NewSerendipityRung(tc.corpus, tc.recipes, rand.New(rand.NewPCG(1, 2)), 0)
			if _, err := rung.Draw(ctx, conductor.DrawRequest{RunID: "run-1", At: day}); !errors.Is(err, conductor.ErrNoWork) {
				t.Errorf("Draw error = %v, want ErrNoWork", err)
			}
		})
	}
}

type fixedCorpus []string

func (c fixedCorpus) Sessions(context.Context) ([]string, error) { return c, nil }

type fixedRecipes []string

func (r fixedRecipes) Defaults(context.Context) ([]string, error) { return r, nil }

// The budget refuses a cycle it could not afford to finish, and refuses it
// before anything is drawn — so a parked cycle cannot have spent an operator's
// invitation on the way to being refused.
func TestBudgetParksBeforeDrawingWork(t *testing.T) {
	ctx := context.Background()
	invitations := &stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{Invitation: "inv-1"}}
	runner := &fakeRunner{}
	journal := testJournal(t)
	loop, err := conductor.New(conductor.Config{
		Ceilings: conductor.Ceilings{Currency: "USD", PerCycle: 1.00, PerDay: 4.00},
		Ladder:   []conductor.Rung{invitations, conductor.PolicyRung()},
		Runner:   runner,
		// Today has already estimated 3.50 of a 4.00 ceiling, which leaves
		// less than the 1.00 a cycle may cost.
		Ledger:  fakeLedger{spend: conductor.Spend{Amount: 3.50, Runs: 7}},
		Journal: journal,
		Now:     (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cycle, err := loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeParked {
		t.Fatalf("cycle = %+v, want a park", cycle)
	}
	if !strings.Contains(cycle.Reason, "4.00") || !strings.Contains(cycle.Reason, "USD") {
		t.Errorf("park reason = %q, which does not name the ceiling it hit", cycle.Reason)
	}
	if invitations.draws != 0 {
		t.Errorf("the invitation queue was drawn from %d times while parked", invitations.draws)
	}
	if len(runner.runs) != 0 {
		t.Errorf("a parked cycle ran %d runs", len(runner.runs))
	}
	// The park is in the record, which is what makes it visible.
	if state, last := journal.Observe(); state != conductor.StateParked || last.Reason != cycle.Reason {
		t.Errorf("journal state = %q with reason %q", state, last.Reason)
	}

	// And the loop stops rather than spinning against a limit that cannot
	// change until the day does.
	runErr := loop.Run(ctx, conductor.RunOptions{})
	if !errors.Is(runErr, conductor.ErrParked) {
		t.Errorf("Run error = %v, want ErrParked", runErr)
	}
}

// A cycle that overran the per-cycle ceiling parks the loop rather than being
// noted and repeated: the ceiling is a statement about what one cycle may cost.
func TestACycleOverTheCeilingParksTheLoop(t *testing.T) {
	ctx := context.Background()
	floor := &stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
		Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d-1"},
	}}
	runner := &fakeRunner{result: conductor.Result{Cost: 0.90, Currency: "USD", ReceiptID: "rcpt-1"}}
	journal := testJournal(t)
	loop, err := conductor.New(conductor.Config{
		Ceilings: conductor.Ceilings{Currency: "USD", PerCycle: 0.50, PerDay: 5.00},
		Floor:    conductor.Floor{OneIn: 1},
		Ladder:   []conductor.Rung{floor},
		Runner:   runner,
		Ledger:   fakeLedger{},
		Journal:  journal,
		Now:      (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cycle, err := loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeParked {
		t.Fatalf("cycle = %+v, want the overrun to park the loop", cycle)
	}
	if !strings.Contains(cycle.Reason, "0.90") || !strings.Contains(cycle.Reason, "0.50") {
		t.Errorf("park reason = %q, which does not compare the cost to the ceiling", cycle.Reason)
	}
	// The run itself still happened and is still recorded: parking is about
	// what happens next, never about erasing what a run produced.
	ran := false
	for _, c := range journal.Recent(0) {
		if c.Outcome == conductor.OutcomeRan && c.ReceiptID == "rcpt-1" {
			ran = true
		}
	}
	if !ran {
		t.Errorf("the overrunning cycle is not in the journal: %+v", journal.Recent(0))
	}
}

// A conductor refuses to exist without ceilings. Autonomy is budget-bounded,
// not trust-bounded, and a default ceiling would be a limit nobody chose.
func TestConductorRefusesWithoutCeilings(t *testing.T) {
	base := conductor.Config{
		Ladder:  []conductor.Rung{&stubRung{name: "x"}},
		Runner:  &fakeRunner{},
		Ledger:  fakeLedger{},
		Journal: testJournal(t),
	}
	for _, tc := range []struct {
		name     string
		ceilings conductor.Ceilings
	}{
		{"none at all", conductor.Ceilings{}},
		{"no currency", conductor.Ceilings{PerCycle: 1, PerDay: 2}},
		{"no per-cycle ceiling", conductor.Ceilings{Currency: "USD", PerDay: 2}},
		{"no per-day ceiling", conductor.Ceilings{Currency: "USD", PerCycle: 1}},
		{"a per-cycle ceiling above the day", conductor.Ceilings{Currency: "USD", PerCycle: 3, PerDay: 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.Ceilings = tc.ceilings
			if _, err := conductor.New(cfg); err == nil {
				t.Error("a conductor was built with no usable ceilings")
			}
		})
	}
}

// --once runs exactly one cycle, which is what makes the loop exercisable
// without waiting for an interval.
func TestOnceRunsExactlyOneCycle(t *testing.T) {
	runner := &fakeRunner{}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Floor:    conductor.Floor{OneIn: 1},
		Ladder: []conductor.Rung{&stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
			Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d-1"},
		}}},
		Runner:   runner,
		Ledger:   fakeLedger{},
		Journal:  testJournal(t),
		Interval: time.Hour,
		Now:      (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := loop.Run(context.Background(), conductor.RunOptions{Once: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runner.runs) != 1 {
		t.Errorf("--once ran %d cycles", len(runner.runs))
	}
}

// The loop stops at a cycle boundary when asked, and does not cancel the run in
// flight to do it.
func TestStopEndsTheLoopAtACycleBoundary(t *testing.T) {
	stop := make(chan struct{})
	finished := false
	runner := &fakeRunner{hook: func(string, conductor.Assignment) (conductor.Result, error) {
		// The stop arrives while a run is in flight, as a signal would.
		close(stop)
		finished = true
		return conductor.Result{ReceiptID: "rcpt-1"}, nil
	}}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Floor:    conductor.Floor{OneIn: 1},
		Ladder: []conductor.Rung{&stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
			Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d-1"},
		}}},
		Runner:  runner,
		Ledger:  fakeLedger{},
		Journal: testJournal(t),
		Now:     (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := loop.Run(context.Background(), conductor.RunOptions{Stop: stop}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !finished {
		t.Error("the run in flight was abandoned rather than finished")
	}
	if len(runner.runs) != 1 {
		t.Errorf("the loop ran %d cycles after being asked to stop", len(runner.runs))
	}
}

// --until stops the loop at a wall-clock time without running a cycle past it.
func TestUntilStopsTheLoop(t *testing.T) {
	runner := &fakeRunner{}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Floor:    conductor.Floor{OneIn: 1},
		Ladder: []conductor.Rung{&stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
			Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d-1"},
		}}},
		Runner:  runner,
		Ledger:  fakeLedger{},
		Journal: testJournal(t),
		Now:     (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The clock advances a minute per reading, so a three-minute window is a
	// handful of cycles and then a stop.
	if err := loop.Run(context.Background(), conductor.RunOptions{Until: day.Add(3 * time.Minute)}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(runner.runs) == 0 {
		t.Error("--until ran no cycles at all")
	}
	if len(runner.runs) > 3 {
		t.Errorf("--until ran %d cycles past its deadline", len(runner.runs))
	}
}

// The journal is what a status view reads, and it must not claim a loop is
// working when the process holding that claim is gone.
func TestJournalObservesTheLoopTruthfully(t *testing.T) {
	dir := t.TempDir()
	journal, err := conductor.OpenJournal(dir)
	if err != nil {
		t.Fatalf("OpenJournal: %v", err)
	}
	if state, _ := journal.Observe(); state != conductor.StateIdle {
		t.Errorf("a journal with no cycles observed as %q", state)
	}

	// A cycle in flight whose conductor still exists is running.
	live := conductor.Cycle{
		Seq: 1, StartedAt: day, Outcome: conductor.OutcomeRunning,
		Rung: conductor.RungInvitation, RunID: "run-1", PID: os.Getpid(),
		Authority: runstore.Authority{Kind: runstore.AuthorityOperator, Ref: "invitation:inv-1"},
	}
	if err := journal.Record(live); err != nil {
		t.Fatalf("Record: %v", err)
	}
	state, current := journal.Observe()
	if state != conductor.StateRunning || current.RunID != "run-1" {
		t.Errorf("state = %q, current = %+v, want a running cycle", state, current)
	}

	// The same record with a process that does not exist is an interruption,
	// not a running loop.
	dead := live
	dead.Seq = 2
	dead.PID = deadPID(t)
	if err := journal.Record(dead); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if state, _ := journal.Observe(); state != conductor.StateInterrupted {
		t.Errorf("a cycle held by a dead process observed as %q", state)
	}

	// Reopening reads the same history back, which is what makes the journal
	// the loop's memory across restarts.
	reopened, err := conductor.OpenJournal(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := reopened.NextSeq(); got != 3 {
		t.Errorf("NextSeq after reopening = %d, want 3", got)
	}
	if state, _ := reopened.Observe(); state != conductor.StateInterrupted {
		t.Errorf("reopened state = %q", state)
	}
}

// deadPID returns a process id that has certainly exited: a child that has
// already been reaped. Picking a number and hoping is how a flaky test is
// written.
func deadPID(t *testing.T) int {
	t.Helper()
	proc, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit 0"}, &os.ProcAttr{})
	if err != nil {
		t.Skipf("cannot start a throwaway process: %v", err)
	}
	if _, err := proc.Wait(); err != nil {
		t.Skipf("cannot reap a throwaway process: %v", err)
	}
	return proc.Pid
}

// The journal is bounded: a loop that ran for months must not turn its own
// bookkeeping into the biggest file Babel writes.
func TestJournalIsBounded(t *testing.T) {
	journal := testJournal(t)
	for i := 1; i <= 250; i++ {
		if err := journal.Record(conductor.Cycle{
			Seq: i, StartedAt: day, FinishedAt: day, Outcome: conductor.OutcomeRan,
			Rung: conductor.RungSerendipity,
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	if got := len(journal.Recent(0)); got != 200 {
		t.Errorf("the journal holds %d cycles, want the cap of 200", got)
	}
	if last, _ := journal.Last(); last.Seq != 250 {
		t.Errorf("the newest cycle is %d, want 250", last.Seq)
	}
	if got := journal.NextSeq(); got != 251 {
		t.Errorf("NextSeq = %d, want 251", got)
	}
}
