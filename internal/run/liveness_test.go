package run

import (
	"os"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/sync"
)

// What this file defends is the evidence side of issue #152.
//
// sync.Publisher.SealAbandoned declares a closure on a run's behalf, and
// migration 0003 fixes a record_count at declaration and never lets it move. So
// the seal is only ever as safe as the proof it rests on, and that proof is
// here: a run is live while it holds a lease whose process exists, or while its
// latest receipt stands at running or resumed and `babel runs resume` could
// therefore still grow it.
//
// The causes are asserted as well as the verdicts, and deliberately: they are
// what an operator measures an abandonment by. Babel should never have to
// abandon a run, so a cause that did not say which evidence was actually
// missing would make the times it had to unanalysable - a count with no reason
// beside it.

// deadPID is a pid no process on this machine holds, which is how the existing
// reconciliation suite stages a lost owner.
const deadPID = 2147483647

func leaseFor(t *testing.T, s *Store, id string, pid int) {
	t.Helper()
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO run_lease VALUES(?,?,?,?)`, id, host, pid, formatTime(recorded)); err != nil {
		t.Fatal(err)
	}
}

func TestRunLivenessNamesTheEvidenceThatWasMissing(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()

	// A run nothing survives of but its staged records: the state that left
	// 1,022 records unpublishable, because a lease is deleted on release, a
	// receipt was never written, and the durable file links a run to its
	// preparation through that receipt alone.
	lifecycleReceipt(t, s, "run-terminal", Closed)
	lifecycleReceipt(t, s, "run-interrupted", Interrupted)
	lifecycleReceipt(t, s, "run-running", Running)
	lifecycleReceipt(t, s, "run-resumed", Resumed)
	lifecycleReceipt(t, s, "run-owned", Running)
	leaseFor(t, s, "run-owned", os.Getpid())
	leaseFor(t, s, "run-interrupted", deadPID)
	leaseFor(t, s, "run-orphan", deadPID)

	for _, tc := range []struct {
		name string
		id   string
		live bool
		// why is a fragment the cause must contain, not the whole sentence: it
		// pins which evidence was reported missing without pinning wording.
		why string
	}{
		{name: "a held lease whose process exists", id: "run-owned", live: true},
		{name: "a receipt still running is resumable", id: "run-running", live: true},
		{name: "a receipt still resumed is resumable", id: "run-resumed", live: true},
		{name: "nothing survives but the staged records", id: "run-nothing",
			why: "only the staged records remain"},
		{name: "a terminal receipt that declared no closure", id: "run-terminal",
			why: "reached closed without declaring a closure"},
		{name: "a dead owner and a receipt that stopped", id: "run-interrupted",
			why: "the owning process no longer exists"},
		{name: "a dead owner that never wrote a receipt", id: "run-orphan",
			why: "never wrote a receipt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			live, why, err := s.RunLiveness(ctx, tc.id)
			if err != nil {
				t.Fatalf("read liveness: %v", err)
			}
			if live != tc.live {
				t.Fatalf("live = %v, want %v (cause: %q)", live, tc.live, why)
			}
			if live {
				if why != "" {
					t.Errorf("a live run named a cause %q; there is none to name", why)
				}
				return
			}
			if !strings.Contains(why, tc.why) {
				t.Errorf("cause = %q, want it to name %q", why, tc.why)
			}
		})
	}

	// The causes have to be distinguishable, because one string per case is
	// the whole measurement: an operator asking why Babel abandoned runs this
	// month reads these and nothing else.
	seen := map[string]string{}
	for _, id := range []string{"run-nothing", "run-terminal", "run-interrupted", "run-orphan"} {
		_, why, err := s.RunLiveness(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if prior, dup := seen[why]; dup {
			t.Errorf("%s and %s report the same cause %q", prior, id, why)
		}
		seen[why] = id
	}
}

// A receipt with no lifecycle checkpoint is a historical completed run rather
// than evidence of interruption, so it is over - and says so as its own case,
// because "no checkpoint" and "a checkpoint that stopped" are different facts
// about what happened.
func TestARunWithNoCheckpointIsHistoricalAndOver(t *testing.T) {
	s := testStore(t)
	r := mustReceipt(t)
	if r.Body.Checkpoint != nil {
		t.Fatal("this case needs a receipt with no checkpoint")
	}
	if err := s.PutReceipt(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	live, why, err := s.RunLiveness(t.Context(), r.Header.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if live {
		t.Fatal("a historical receipt was read as a run still producing")
	}
	if !strings.Contains(why, "no lifecycle checkpoint") {
		t.Errorf("cause = %q, want it to name the missing checkpoint", why)
	}
}

// The lease is the cheapest evidence a run existed, and judging its release by
// receipt state alone deleted it for exactly the runs whose records were
// stranded: terminal receipt, no declaration, and then nothing at all for any
// recovery path to key on. It now survives until the closure is declared.
func TestALeaseSurvivesReleaseWhileAClosureIsStillOwed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, WithSync(sync.NewStager()))
	if err != nil {
		t.Fatalf("open a staging store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := t.Context()

	release, err := s.BeginAttempt(ctx, "run-owed")
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	// A run that ended with a terminal receipt and staged its output, which is
	// staged inside the write that made the receipt durable.
	lifecycleReceipt(t, s, "run-owed", Closed)
	release()

	owned, err := s.AttemptOwned(ctx, "run-owed")
	if err != nil {
		t.Fatalf("read ownership: %v", err)
	}
	if !owned {
		t.Fatal("the lease of a run that still owes a closure was deleted, stranding its records with nothing to key on")
	}
	// And the retained lease is readable as what it is: the owner is gone, so
	// the run is over and its closure may be sealed at what it reached. The
	// pid is replaced with a dead one because the owner of a real retained
	// lease has exited by the time anything sweeps for it.
	if _, err := s.db.Exec(`UPDATE run_lease SET pid = ? WHERE run_id = ?`, deadPID, "run-owed"); err != nil {
		t.Fatal(err)
	}
	live, why, err := s.RunLiveness(ctx, "run-owed")
	if err != nil {
		t.Fatalf("read liveness: %v", err)
	}
	if live || !strings.Contains(why, "the owning process no longer exists") {
		t.Errorf("live = %v, cause = %q; want the retained lease read as a lost owner", live, why)
	}

	// The control: the same run state with its closure declared releases the
	// lease as it always did. A lease retained for a run that owes nothing
	// would refuse the next attempt on it for no reason.
	release, err = s.BeginAttempt(ctx, "run-settled")
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	lifecycleReceipt(t, s, "run-settled", Closed)
	if err := s.DeclareClosure(ctx, "run-settled"); err != nil {
		t.Fatalf("declare the closure: %v", err)
	}
	release()
	owned, err = s.AttemptOwned(ctx, "run-settled")
	if err != nil {
		t.Fatalf("read ownership: %v", err)
	}
	if owned {
		t.Error("a run whose closure is declared kept its lease, which refuses the next attempt on it")
	}
}
