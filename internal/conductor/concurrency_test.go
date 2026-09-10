package conductor_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/atyrode/babel/internal/conductor"
	runstore "github.com/atyrode/babel/internal/run"
)

// gatedRunner records every run under a lock and holds each one inside the
// runner until the test releases it, so a test can observe how many cycles a
// loop has in flight at once rather than inferring it from timing.
type gatedRunner struct {
	mu       sync.Mutex
	started  int
	inFlight int
	peak     int
	runIDs   []string
	// admitted is closed once want cycles are in flight together. The close
	// is guarded because the count is a live gauge, not a high-water mark:
	// once the gate opens, running cycles drain and later ones can push it
	// back through want, which would close an already-closed channel.
	want     int
	admitted chan struct{}
	announce sync.Once
	// gate holds every run until the test releases it.
	gate chan struct{}
}

func newGatedRunner(want int) *gatedRunner {
	return &gatedRunner{want: want, admitted: make(chan struct{}), gate: make(chan struct{})}
}

func (r *gatedRunner) Run(_ context.Context, runID string, _ conductor.Assignment) (conductor.Result, error) {
	r.mu.Lock()
	r.started++
	r.inFlight++
	r.peak = max(r.peak, r.inFlight)
	r.runIDs = append(r.runIDs, runID)
	if r.inFlight == r.want {
		r.announce.Do(func() { close(r.admitted) })
	}
	r.mu.Unlock()
	<-r.gate
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
	return conductor.Result{ReceiptID: "rcpt-" + runID}, nil
}

func (r *gatedRunner) stats() (started, peak int, runIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started, r.peak, append([]string(nil), r.runIDs...)
}

// concurrentLoop is a loop whose only rung always has work, so what a test
// observes is the scheduling and never an empty ladder.
func concurrentLoop(t *testing.T, runner conductor.Runner, journal *conductor.Journal, ceilings conductor.Ceilings) *conductor.Conductor {
	t.Helper()
	loop, err := conductor.New(conductor.Config{
		Ceilings: ceilings,
		Floor:    conductor.Floor{OneIn: 1},
		Ladder: []conductor.Rung{&stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
			Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:concurrent"},
		}}},
		Runner: runner, Ledger: fakeLedger{}, Journal: journal, Now: (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

// Concurrent cycles draw against one budget rather than a copy each.
//
// This is the property the whole feature turns on. A ledger can only see what
// a receipt recorded, and a cycle writes one when it ends, so four cycles
// starting together would each be told the whole day's ceiling was free — and
// a loop that spent four times its operator's limit because it was asked to
// go faster would make the ceilings advisory.
func TestConcurrentCyclesDrawAgainstOneBudget(t *testing.T) {
	// Two cycles fit inside the day: each may cost one, and a third cannot be
	// afforded until the first two have reported what they actually spent.
	ceilings := conductor.Ceilings{Currency: "USD", PerCycle: 1, PerDay: 2}
	runner := newGatedRunner(2)
	journal := testJournal(t)
	loop := concurrentLoop(t, runner, journal, ceilings)

	// The gate must not open until every cycle has tried to claim. Releasing
	// it as soon as two are in flight lets the running pair finish and free
	// the day's ceiling before the other two have asked for it, so they are
	// admitted rather than refused and the test observes scheduling that
	// never happened. The two the budget refuses return without touching the
	// gate, so reading two results is exactly the barrier that says everyone
	// has asked.
	done := make(chan conductor.Cycle, 4)
	for range 4 {
		go func() {
			cycle, err := loop.Once(t.Context())
			if err != nil {
				t.Errorf("Once: %v", err)
			}
			done <- cycle
		}()
	}
	<-runner.admitted
	outcomes := make([]conductor.Cycle, 0, 4)
	outcomes = append(outcomes, <-done, <-done)
	close(runner.gate)
	outcomes = append(outcomes, <-done, <-done)

	started, peak, runIDs := runner.stats()
	if peak != 2 || started != 2 {
		t.Errorf("%d cycles ran, %d at once; the day's ceiling affords exactly 2", started, peak)
	}
	parked := 0
	for _, cycle := range outcomes {
		if cycle.Outcome == conductor.OutcomeParked {
			parked++
		}
	}
	if parked != 2 {
		t.Errorf("%d cycles parked, want the 2 the budget refused", parked)
	}
	// Each cycle is its own run, or two of them would amend one receipt chain
	// and inherit one authority.
	if runIDs[0] == runIDs[1] {
		t.Errorf("both cycles ran as %q", runIDs[0])
	}
	seqs := map[int]bool{}
	for _, cycle := range journal.Recent(0) {
		if seqs[cycle.Seq] {
			t.Fatalf("two cycles share sequence number %d", cycle.Seq)
		}
		seqs[cycle.Seq] = true
	}
}

// The first stop lets every cycle in flight finish and starts none after it.
// Concurrency must not turn "clean at the cycle boundary" into "clean for
// whichever cycle noticed".
func TestConcurrentCyclesStopCleanlyOnTheStopFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stop")
	runner := newGatedRunner(3)
	journal := testJournal(t)
	loop := concurrentLoop(t, runner, journal, testCeilings)

	done := make(chan error, 1)
	go func() {
		done <- loop.Run(t.Context(), conductor.RunOptions{Concurrency: 3, StopFile: path})
	}()
	// Every worker is inside a run before the stop exists, so what is under
	// test is what happens to cycles in flight rather than which worker
	// noticed the file first.
	<-runner.admitted
	if err := os.WriteFile(path, []byte("stop"), 0600); err != nil {
		t.Fatal(err)
	}
	close(runner.gate)
	if err := <-done; err != nil {
		t.Fatalf("a stop file is not a failure: %v", err)
	}

	started, _, _ := runner.stats()
	if started != 3 {
		t.Errorf("%d cycles ran, want the 3 that were in flight when the stop arrived", started)
	}
	cycles := journal.Recent(0)
	if len(cycles) != 3 {
		t.Fatalf("the journal holds %d cycles, want 3", len(cycles))
	}
	for _, cycle := range cycles {
		if cycle.Outcome != conductor.OutcomeRan {
			t.Errorf("cycle %d ended as %q; a stop lets a cycle in flight finish and be receipted",
				cycle.Seq, cycle.Outcome)
		}
		// A sibling still running is not work a dead conductor left behind.
		// Reconciling one would run a live assignment twice under one
		// identity, which is the failure concurrency invites.
		if cycle.Resumed {
			t.Errorf("cycle %d resumed a sibling that was still in flight", cycle.Seq)
		}
	}
}

// A concurrency dial cannot turn --once into several cycles: it is the flag
// that exists to run exactly one.
func TestOnceRunsOneCycleWhateverTheConcurrency(t *testing.T) {
	runner := newGatedRunner(1)
	close(runner.gate)
	loop := concurrentLoop(t, runner, testJournal(t), testCeilings)
	if err := loop.Run(t.Context(), conductor.RunOptions{Once: true, Concurrency: 4}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if started, _, _ := runner.stats(); started != 1 {
		t.Errorf("--once with --concurrent 4 ran %d cycles", started)
	}
}
