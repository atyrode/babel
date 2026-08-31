package explore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/worker"
)

// recordedPresence is one call the run made into the announcer, in order, so a
// test can assert the choreography rather than only its end state.
type recordedPresence struct {
	call    string
	id      presence.PresenceID
	ann     presence.Announcement
	outcome presence.Outcome
}

// fakeAnnouncer is a presence store that cannot fail, plus the knobs for the
// two ways a real one can be useless: refusing to announce, and being slow.
type fakeAnnouncer struct {
	calls  []recordedPresence
	refuse bool
	delay  time.Duration
	err    error
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

func withPresence(ann presence.Announcer) func(*explore.Config) {
	return func(cfg *explore.Config) { cfg.Presence = ann }
}

// A run announces itself before it starts working and finalizes with the
// receipt that records what it did. That closes #118's actual gap: until the
// receipt commits - which for a real run is a long time after it starts -
// nothing off-host could see the run at all.
func TestRunAnnouncesItselfAndFinalizesWithItsReceipt(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	ann := &fakeAnnouncer{}
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withPresence(ann))

	outcome, err := controller.Explore(context.Background(), explore.Options{
		RunID:     "run-presence-1",
		Authority: testAuthority,
	})
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}
	if outcome.Receipt == nil {
		t.Fatal("the run wrote no receipt, so there is nothing for presence to link")
	}

	announced := ann.only(t, "announce")
	if announced.ann.Kind != presence.KindExplore {
		t.Errorf("announced kind %q, want %q", announced.ann.Kind, presence.KindExplore)
	}
	if announced.ann.RunID != "run-presence-1" {
		t.Errorf("announced run %q, want run-presence-1", announced.ann.RunID)
	}
	if announced.ann.Authority != testAuthority {
		t.Errorf("announced authority %+v, want %+v", announced.ann.Authority, testAuthority)
	}
	// The preparation is what makes a fleet row answer "over what": a run id
	// alone says a machine is busy, and the scope is the part another operator
	// needs before they decide whether to start something overlapping.
	if announced.ann.PreparationID != string(h.prep.ID) {
		t.Errorf("announced preparation %q, want %q", announced.ann.PreparationID, h.prep.ID)
	}
	if announced.ann.Recipe == "" {
		t.Error("announced no recipe; the fleet row would not say what the run is for")
	}

	final := ann.only(t, "finalize")
	if final.id != announced.idOfAnnounce() {
		t.Errorf("finalized %q, want the row the announcement returned", final.id)
	}
	if final.outcome.State != presence.StateFinished {
		t.Errorf("finalized as %q, want %q", final.outcome.State, presence.StateFinished)
	}
	if final.outcome.ReceiptRecordID != string(outcome.Receipt.Header.ID) {
		t.Errorf("finalized with receipt %q, want the run's own %q",
			final.outcome.ReceiptRecordID, outcome.Receipt.Header.ID)
	}

	names := ann.callNames()
	if len(names) < 2 || names[0] != "announce" || names[len(names)-1] != "finalize" {
		t.Errorf("call order %v, want announce first and finalize last", names)
	}
}

// idOfAnnounce is the id the fake hands back for the first announcement. It is a
// method on the record rather than a literal so the assertion above reads as
// "the row the announcement returned" instead of pinning the fake's format.
func (r recordedPresence) idOfAnnounce() presence.PresenceID { return "prs_1" }

// A cancelled run finalizes as cancelled, and it does so even though its own
// context is dead by then. Without that the row would go stale and read as a
// machine that died, when what happened is that a person pressed Ctrl-C and
// §5.2 kept every candidate the run had emitted.
func TestCancelledRunFinalizesAsCancelled(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	ann := &fakeAnnouncer{}
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withPresence(ann))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outcome, _ := controller.Explore(ctx, explore.Options{
		RunID:     "run-cancelled",
		Authority: testAuthority,
	})
	if outcome == nil {
		t.Fatal("no outcome for a cancelled run")
	}

	final := ann.only(t, "finalize")
	if final.outcome.State != presence.StateCancelled {
		t.Errorf("finalized as %q, want %q; a cancelled run kept what it committed and must not read as a failure",
			final.outcome.State, presence.StateCancelled)
	}
}

// A refused preflight is the early return that a naive wiring forgets. The run
// never reaches a worker, so a row left in `running` would sit on every other
// machine's fleet view going stale for a run that ended in milliseconds.
func TestRefusedRunStillFinalizesItsPresence(t *testing.T) {
	h := newHarness(t)
	h.plantSecret()
	payload := h.writeResult("discovery.json", h.discovery())
	ann := &fakeAnnouncer{}
	// Redaction is required by the disclosure class and not applied, which is
	// what makes preflight refuse the run before any worker starts.
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		func(cfg *explore.Config) {
			cfg.Grant.Disclosure = worker.DisclosureHosted
			cfg.Redact = false
			cfg.Presence = ann
		})

	_, err := controller.Explore(context.Background(), explore.Options{
		RunID:     "run-refused",
		Authority: testAuthority,
	})
	if !errors.Is(err, explore.ErrRedactionRequired) {
		t.Fatalf("Explore = %v, want a refusal; this test needs the early return", err)
	}

	ann.only(t, "announce")
	final := ann.only(t, "finalize")
	if final.outcome.State != presence.StateFailed {
		t.Errorf("finalized as %q, want %q", final.outcome.State, presence.StateFailed)
	}
	// A refused run still writes a receipt, and presence links it: the record
	// of a refusal is exactly when the record is needed.
	if final.outcome.ReceiptRecordID == "" {
		t.Error("a refused run's presence row names no receipt, so the fleet cannot read why it was refused")
	}
}

// A dead or absent presence store must cost a run nothing: not an error, not a
// changed outcome, not a record it failed to commit, and not measurable time.
// This is the property the whole feature is built around.
func TestAPresenceStoreCannotFailOrDelayARun(t *testing.T) {
	ctx := context.Background()
	explored := func(t *testing.T, mutate func(*explore.Config)) (*explore.Outcome, error, time.Duration) {
		t.Helper()
		h := newHarness(t)
		payload := h.writeResult("discovery.json", h.discovery())
		controller := h.controller(
			payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}), mutate)
		start := time.Now()
		outcome, err := controller.Explore(ctx, explore.Options{
			RunID:     "run-1",
			Authority: testAuthority,
		})
		return outcome, err, time.Since(start)
	}

	// The reference: no announcer at all.
	reference, err, _ := explored(t, func(*explore.Config) {})
	if err != nil {
		t.Fatalf("Explore with no announcer: %v", err)
	}

	// Every method fails and the announcement never lands - the harshest thing
	// this interface can do.
	broken := &fakeAnnouncer{refuse: true, err: errors.New("the catalog is gone")}
	outcome, err, _ := explored(t, withPresence(broken))
	if err != nil {
		t.Fatalf("Explore with a broken announcer returned an error: %v", err)
	}
	if len(outcome.Hypotheses) != len(reference.Hypotheses) ||
		len(outcome.Observations) != len(reference.Observations) ||
		len(outcome.Findings) != len(reference.Findings) {
		t.Errorf("a broken announcer changed what the run committed: %d/%d/%d, want %d/%d/%d",
			len(outcome.Hypotheses), len(outcome.Observations), len(outcome.Findings),
			len(reference.Hypotheses), len(reference.Observations), len(reference.Findings))
	}
	if len(outcome.Failures) != len(reference.Failures) {
		t.Errorf("a broken announcer added %d failures to the run's own record",
			len(outcome.Failures)-len(reference.Failures))
	}
	// A refused announcement leaves nothing to heartbeat or finalize, so the
	// failures do not multiply for the rest of the run.
	for _, c := range broken.calls {
		if c.call != "announce" {
			t.Errorf("a refused announcement was followed by a %s call on no row", c.call)
		}
	}

	// And a slow store is not the run's latency. Two calls at 50ms each is the
	// whole exposure - the announce and the finalize - because the heartbeat
	// runs on its own goroutine at a thirty-second interval.
	slow := &fakeAnnouncer{delay: 50 * time.Millisecond}
	if _, err, _ := explored(t, withPresence(slow)); err != nil {
		t.Fatalf("Explore with a slow announcer: %v", err)
	}
	if got := len(slow.calls); got != 2 {
		t.Errorf("the run made %d presence calls (%v), want exactly 2: presence is not on the run's critical path",
			got, slow.callNames())
	}
}

// A run whose closure failed to publish still finished. §6.5 keeps publication
// separate from the run's verdict, and presence must not invent a second
// judgement: a fleet row saying "failed" for a run that produced everything it
// was asked for would be worse than no row.
func TestPublicationFailureDoesNotMakeARunLookFailed(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	ann := &fakeAnnouncer{}
	// internal/sync reserves a returned error for a caller bug - a closure
	// with no staged records, or one already declared at another size - and
	// that is the one publication outcome internal/explore records as a
	// failure rather than swallowing. recordingHook stands in for it.
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withPresence(ann),
		publishing(&recordingHook{err: errors.New("declared 2, staged 3")}))

	outcome, err := controller.Explore(context.Background(), explore.Options{
		RunID:     "run-unpublished",
		Authority: testAuthority,
	})
	if err != nil {
		t.Fatalf("Explore: %v", err)
	}
	if !hasFailure(outcome.Failures, explore.FailureSyncPublish) {
		t.Fatalf("the run recorded no publication failure, so this test proves nothing: %+v", outcome.Failures)
	}
	final := ann.only(t, "finalize")
	if final.outcome.State != presence.StateFinished {
		t.Errorf("finalized as %q, want %q: whether a catalog was reachable does not decide whether a run succeeded",
			final.outcome.State, presence.StateFinished)
	}
}
