package conductor_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/presence"
	runstore "github.com/atyrode/babel/internal/run"
)

// recordedPresence is one call the loop made into the announcer, in order, so a
// test can assert the choreography rather than only its end state.
type recordedPresence struct {
	call    string
	id      presence.PresenceID
	ann     presence.Announcement
	outcome presence.Outcome
}

// fakeAnnouncer is a presence store that cannot fail. It is deliberately not
// the real one: what these tests are about is what the loop does and in which
// order, and a database would make the slowest thing in the test the thing not
// under test.
type fakeAnnouncer struct {
	calls []recordedPresence
	// refuse makes Announce report that nothing landed, which is what an
	// unreachable catalog looks like from the loop's side.
	refuse bool
	// delay is how long each call blocks, for the test that a degraded
	// catalog cannot slow a cycle down.
	delay time.Duration
	// err is returned by every method, which is a caller bug in the real
	// implementation and must still not reach the loop's own error path.
	err error
}

func (f *fakeAnnouncer) Announce(_ context.Context, a presence.Announcement) (presence.PresenceID, error) {
	time.Sleep(f.delay)
	f.calls = append(f.calls, recordedPresence{call: "announce", ann: a})
	if f.refuse {
		return "", f.err
	}
	return presence.PresenceID(fmt.Sprintf("prs_%d", len(f.calls))), f.err
}

func (f *fakeAnnouncer) Heartbeat(_ context.Context, id presence.PresenceID) error {
	time.Sleep(f.delay)
	f.calls = append(f.calls, recordedPresence{call: "heartbeat", id: id})
	return f.err
}

func (f *fakeAnnouncer) Finalize(_ context.Context, id presence.PresenceID, o presence.Outcome) error {
	time.Sleep(f.delay)
	f.calls = append(f.calls, recordedPresence{call: "finalize", id: id, outcome: o})
	return f.err
}

func (f *fakeAnnouncer) only(t *testing.T, call string) recordedPresence {
	t.Helper()
	var found []recordedPresence
	for _, c := range f.calls {
		if c.call == call {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d %s calls, want exactly 1; calls were %v", len(found), call, f.callNames())
	}
	return found[0]
}

func (f *fakeAnnouncer) callNames() []string {
	out := make([]string, 0, len(f.calls))
	for _, c := range f.calls {
		out = append(out, c.call)
	}
	return out
}

// A cycle that draws work announces it, with the ladder authority that allowed
// it, and finalizes it against the same row. That is the whole of what makes a
// loop on one machine legible from another before its receipt commits (#118).
func TestCycleAnnouncesAndFinalizesItsPresence(t *testing.T) {
	ctx := context.Background()
	ann := &fakeAnnouncer{}
	rung := &stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{
		Invitation: "inv-1",
		Recipes:    []string{"improves-babel", "tunes-itself"},
		Note:       "an operator asked",
	}}
	clk := &clock{now: day}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Ladder:   []conductor.Rung{rung},
		Runner:   &fakeRunner{result: conductor.Result{ReceiptID: "rcpt-7"}},
		Ledger:   fakeLedger{},
		Journal:  testJournal(t),
		Presence: ann,
		Now:      clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cycle, err := loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}

	announced := ann.only(t, "announce")
	if announced.ann.Kind != presence.KindConductor {
		t.Errorf("announced kind %q, want %q: a scheduling tick is not a run, and a fleet view must be able to tell them apart",
			announced.ann.Kind, presence.KindConductor)
	}
	if announced.ann.RunID != cycle.RunID {
		t.Errorf("announced run %q, cycle ran %q: a presence row nobody can join back to a receipt is a blinking light with no referent",
			announced.ann.RunID, cycle.RunID)
	}
	if announced.ann.Authority != cycle.Authority {
		t.Errorf("announced authority %+v, cycle authority %+v", announced.ann.Authority, cycle.Authority)
	}
	if !announced.ann.Authority.Recorded() {
		t.Error("announced no authority; no nameable authority, no run, and the fleet has to be able to read it")
	}
	if announced.ann.Recipe != "improves-babel" {
		t.Errorf("announced recipe %q, want the assignment's first; the receipt carries the whole set",
			announced.ann.Recipe)
	}
	// A cycle has no preparation until its runner has made one, and announcing
	// an empty one is honest where announcing a guess would not be.
	if announced.ann.PreparationID != "" {
		t.Errorf("announced preparation %q before the runner prepared anything", announced.ann.PreparationID)
	}

	final := ann.only(t, "finalize")
	if final.id != "prs_1" {
		t.Errorf("finalized %q, want the row announce returned", final.id)
	}
	if final.outcome.State != presence.StateFinished {
		t.Errorf("finalized as %q, want %q", final.outcome.State, presence.StateFinished)
	}
	if final.outcome.ReceiptRecordID != "rcpt-7" {
		t.Errorf("finalized with receipt %q, want rcpt-7", final.outcome.ReceiptRecordID)
	}

	// Order matters and is not incidental: the announcement precedes the run so
	// a conductor that dies mid-cycle leaves the fact behind, and the finalize
	// follows it so the last thing the fleet hears is how the cycle ended.
	names := ann.callNames()
	if len(names) < 2 || names[0] != "announce" || names[len(names)-1] != "finalize" {
		t.Errorf("call order %v, want announce first and finalize last", names)
	}
}

// The two ways a cycle ends badly are two different states, because they mean
// different things to somebody reading a fleet view: a failed cycle produced a
// receipt recording what went wrong, and a cancelled one kept everything it had
// already committed.
func TestPresenceDistinguishesFailureFromCancellation(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		result conductor.Result
		err    error
		want   presence.State
	}{
		{"a clean cycle", conductor.Result{ReceiptID: "rcpt-1"}, nil, presence.StateFinished},
		{"a failed run", conductor.Result{ReceiptID: "rcpt-2"}, errors.New("the worker died"), presence.StateFailed},
		{"a cancelled run", conductor.Result{ReceiptID: "rcpt-3", Cancelled: true}, nil, presence.StateCancelled},
		// Cancellation wins over the error the cancellation itself produced:
		// what happened is that somebody stopped the run, and rendering it as
		// a failure would misreport the most common way a long cycle ends.
		{"a cancelled run reporting an error", conductor.Result{ReceiptID: "rcpt-4", Cancelled: true},
			errors.New("context canceled"), presence.StateCancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ann := &fakeAnnouncer{}
			clk := &clock{now: day}
			loop, err := conductor.New(conductor.Config{
				Ceilings: testCeilings,
				Ladder: []conductor.Rung{&stubRung{
					name: conductor.RungInvitation,
					work: &conductor.Assignment{Invitation: "inv-1", Note: "an operator asked"},
				}},
				Runner:   &fakeRunner{result: tc.result, err: tc.err},
				Ledger:   fakeLedger{},
				Journal:  testJournal(t),
				Presence: ann,
				Now:      clk.Now,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := loop.Once(ctx); err != nil {
				t.Fatalf("Once: %v", err)
			}
			final := ann.only(t, "finalize")
			if final.outcome.State != tc.want {
				t.Errorf("finalized as %q, want %q", final.outcome.State, tc.want)
			}
			if final.outcome.ReceiptRecordID != tc.result.ReceiptID {
				t.Errorf("finalized with receipt %q, want %q", final.outcome.ReceiptRecordID, tc.result.ReceiptID)
			}
		})
	}
}

// A parked or idle cycle drew no work, has no authority to name, and has nothing
// for a heartbeat to be about. Announcing one would be the loop asserting
// presence on behalf of work that does not exist, which is exactly the kind of
// confident-but-empty signal presence is built to avoid.
func TestParkedAndIdleCyclesAnnounceNothing(t *testing.T) {
	ctx := context.Background()

	// Idle: the ladder has nothing.
	ann := &fakeAnnouncer{}
	clk := &clock{now: day}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Ladder:   []conductor.Rung{&stubRung{name: conductor.RungInvitation}},
		Runner:   &fakeRunner{},
		Ledger:   fakeLedger{},
		Journal:  testJournal(t),
		Presence: ann,
		Now:      clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cycle, err := loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeIdle {
		t.Fatalf("cycle outcome %q, want idle", cycle.Outcome)
	}
	if len(ann.calls) != 0 {
		t.Errorf("an idle cycle made %v presence calls, want none", ann.callNames())
	}

	// Parked: the day's ceiling is already spent.
	ann = &fakeAnnouncer{}
	clk = &clock{now: day}
	loop, err = conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Ladder: []conductor.Rung{&stubRung{
			name: conductor.RungInvitation,
			work: &conductor.Assignment{Invitation: "inv-1", Note: "an operator asked"},
		}},
		Runner:   &fakeRunner{},
		Ledger:   fakeLedger{spend: conductor.Spend{Amount: 99}},
		Journal:  testJournal(t),
		Presence: ann,
		Now:      clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cycle, err = loop.Once(ctx)
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeParked {
		t.Fatalf("cycle outcome %q, want parked", cycle.Outcome)
	}
	if len(ann.calls) != 0 {
		t.Errorf("a parked cycle made %v presence calls, want none", ann.callNames())
	}
}

// A dead presence store must cost the loop nothing: not an error, not a
// different outcome, and not a run the loop skipped. This is the property the
// whole feature is built around, so it is asserted against both ways the store
// can be dead - absent, and answering with failures.
func TestAPresenceStoreCannotFailACycle(t *testing.T) {
	ctx := context.Background()
	work := &conductor.Assignment{Invitation: "inv-1", Note: "an operator asked"}

	newLoop := func(t *testing.T, ann presence.Announcer, runner *fakeRunner) *conductor.Conductor {
		t.Helper()
		clk := &clock{now: day}
		loop, err := conductor.New(conductor.Config{
			Ceilings: testCeilings,
			Ladder:   []conductor.Rung{&stubRung{name: conductor.RungInvitation, work: work}},
			Runner:   runner,
			Ledger:   fakeLedger{},
			Journal:  testJournal(t),
			Presence: ann,
			Now:      clk.Now,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return loop
	}

	// The reference: no announcer at all. A loop with no fleet must schedule
	// exactly as one with a fleet does.
	absent := &fakeRunner{}
	reference, err := newLoop(t, nil, absent).Once(ctx)
	if err != nil {
		t.Fatalf("Once with no announcer: %v", err)
	}

	// Announce refuses and every method errors: a caller bug in the real
	// implementation, and the harshest thing this interface can do.
	broken := &fakeRunner{}
	ann := &fakeAnnouncer{refuse: true, err: errors.New("the catalog is gone")}
	cycle, err := newLoop(t, ann, broken).Once(ctx)
	if err != nil {
		t.Fatalf("Once with a broken announcer returned an error: %v", err)
	}
	if cycle.Outcome != reference.Outcome || cycle.Rung != reference.Rung {
		t.Errorf("a broken announcer changed the cycle: %q on %q, want %q on %q",
			cycle.Outcome, cycle.Rung, reference.Outcome, reference.Rung)
	}
	if len(broken.runs) != 1 {
		t.Errorf("the runner was called %d times with a broken announcer, want 1", len(broken.runs))
	}
	// An announce that did not land makes the rest no-ops rather than a stream
	// of failing calls: there is no row to heartbeat or finalize.
	for _, c := range ann.calls {
		if c.call != "announce" {
			t.Errorf("a refused announcement was followed by a %s call on no row", c.call)
		}
	}

	// And a slow store does not become the cycle's latency. The announcer here
	// is slower than any plausible cycle, and the loop must not wait for it
	// beyond the one call it makes before the run.
	slow := &fakeRunner{}
	slowAnn := &fakeAnnouncer{delay: 20 * time.Millisecond}
	start := time.Now()
	if _, err := newLoop(t, slowAnn, slow).Once(ctx); err != nil {
		t.Fatalf("Once with a slow announcer: %v", err)
	}
	elapsed := time.Since(start)
	// Two calls at 20ms each is the whole budget: an announce and a finalize.
	// A heartbeat inside the cycle would only add to this, and there is none,
	// because the fake runner returns immediately and the heartbeat interval is
	// thirty seconds - which is itself the assertion that the loop does not
	// heartbeat synchronously around the run.
	if elapsed > time.Second {
		t.Errorf("a cycle with a slow presence store took %s; presence must never be on the cycle's critical path", elapsed)
	}
	if got := len(slowAnn.calls); got != 2 {
		t.Errorf("a cycle made %d presence calls (%v), want exactly 2: one announcement and one finalize",
			got, slowAnn.callNames())
	}
}

// The authority a cycle announces is the one the ladder drew, whichever rung it
// came from. A fleet view's most useful column is why a machine is busy, and it
// must not be able to disagree with the receipt.
func TestEveryAuthorityKindReachesPresence(t *testing.T) {
	ctx := context.Background()
	for _, auth := range []runstore.Authority{
		{Kind: runstore.AuthorityOperator, Ref: "invitation:inv-1"},
		{Kind: runstore.AuthorityPolicy, Ref: "duty-improves-babel"},
		{Kind: runstore.AuthoritySerendipity, Ref: "draw:d-1"},
	} {
		t.Run(string(auth.Kind), func(t *testing.T) {
			ann := &fakeAnnouncer{}
			clk := &clock{now: day}
			loop, err := conductor.New(conductor.Config{
				Ceilings: testCeilings,
				Ladder: []conductor.Rung{&stubRung{
					name: conductor.RungPolicy,
					work: &conductor.Assignment{Authority: auth, Note: "planted"},
				}},
				Runner:   &fakeRunner{},
				Ledger:   fakeLedger{},
				Journal:  testJournal(t),
				Presence: ann,
				Now:      clk.Now,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if _, err := loop.Once(ctx); err != nil {
				t.Fatalf("Once: %v", err)
			}
			if got := ann.only(t, "announce").ann.Authority; got != auth {
				t.Errorf("announced authority %+v, want %+v", got, auth)
			}
		})
	}
}
