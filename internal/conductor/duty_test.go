package conductor_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	runstore "github.com/atyrode/babel/internal/run"
)

// These cases are about issues #88 and #94: the standing duties rung two draws
// from. What they defend is the shape of the authorization rather than the
// analysis — a duty that ran without being authorized, ran twice in a day, or
// outranked the operator would each be a loop spending an operator's budget on
// something they did not ask for.

// dutyClock is a clock a test advances by hand, so a day of cadence can pass
// between two cycles without a test waiting for one.
type dutyClock struct{ now time.Time }

func (c *dutyClock) Now() time.Time { return c.now }

func (c *dutyClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// dutyLoop assembles a loop whose ladder is an invitation stub, a real duty
// rung over the real journal, and a floor stub with no work. The journal is
// real because the cadence is computed from it: a duty rung tested against a
// planted history would not be testing the thing that makes the cadence
// survive a restart.
type dutyLoop struct {
	loop        *conductor.Conductor
	invitations *stubRung
	floor       *stubRung
	duties      *conductor.DutyRung
	journal     *conductor.Journal
	clk         *dutyClock
	runner      *fakeRunner
}

func newDutyLoop(t *testing.T, toggles conductor.Duties, floorOneIn int) *dutyLoop {
	t.Helper()
	d := &dutyLoop{
		invitations: &stubRung{name: conductor.RungInvitation},
		floor:       &stubRung{name: conductor.RungSerendipity},
		journal:     testJournal(t),
		clk:         &dutyClock{now: day},
		runner:      &fakeRunner{},
	}
	d.duties = conductor.NewDutyRung(toggles, d.journal, d.clk.Now, 0)
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Floor:    conductor.Floor{OneIn: floorOneIn},
		Ladder:   []conductor.Rung{d.invitations, d.duties, d.floor},
		Runner:   d.runner,
		Ledger:   fakeLedger{},
		Journal:  d.journal,
		Now:      d.clk.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d.loop = loop
	return d
}

func (d *dutyLoop) once(t *testing.T) conductor.Cycle {
	t.Helper()
	cycle, err := d.loop.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	return cycle
}

// A duty nobody authorized is not drawn, and the rung says so as a depth of
// zero rather than as an absence: #88's toggles default off, so this is the
// ordinary state of a configured machine.
func TestUnauthorizedDutiesAreNeverDrawn(t *testing.T) {
	ctx := context.Background()
	d := newDutyLoop(t, conductor.Duties{}, 100)

	_, err := d.duties.Draw(ctx, conductor.DrawRequest{RunID: "run-1", At: day})
	if !errors.Is(err, conductor.ErrNoWork) {
		t.Fatalf("Draw with no authorized duty = %v, want ErrNoWork", err)
	}
	depth, err := d.duties.Depth(ctx)
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if !depth.Implemented {
		t.Error("the duty rung reported itself unimplemented; the duties exist, they are unauthorized")
	}
	if depth.Waiting != 0 {
		t.Errorf("depth = %+v, want nothing due", depth)
	}
	if !strings.Contains(depth.Note, "attention policy") {
		t.Errorf("the rung's note %q does not say what rung two still lacks", depth.Note)
	}

	// Every duty is reported, off, naming the flag that would turn it on. An
	// absent line and an unauthorized one would otherwise read the same.
	states := d.duties.States(day)
	if len(states) != 3 {
		t.Fatalf("States reports %d duties, want the three this build knows", len(states))
	}
	for _, s := range states {
		if s.Enabled || s.Due {
			t.Errorf("duty %q = enabled %t due %t with no authorization", s.Name, s.Enabled, s.Due)
		}
		if !strings.Contains(s.Note, "--"+s.Toggle) {
			t.Errorf("duty %q note %q does not name the flag that authorizes it", s.Name, s.Note)
		}
	}

	// And the loop as a whole draws nothing: the floor has no work either, so
	// an unauthorized machine idles rather than inventing a duty cycle.
	if cycle := d.once(t); cycle.Outcome != conductor.OutcomeIdle {
		t.Errorf("cycle = %+v, want idle with no authorized duty and an empty floor", cycle)
	}
	if len(d.runner.runs) != 0 {
		t.Errorf("the runner was called %d times for an unauthorized duty", len(d.runner.runs))
	}
}

// One toggle authorizes its whole dimension, each duty is drawn at most once
// per cadence, and the cadence is measured on the clock rather than per
// process. #94's audit rides the product toggle because #94 places it in that
// dimension.
func TestAuthorizedDutyIsDrawnOncePerCadence(t *testing.T) {
	d := newDutyLoop(t, conductor.Duties{ImprovesBabel: true}, 100)

	first := d.once(t)
	if first.Rung != conductor.RungPolicy {
		t.Fatalf("first cycle drew from %q, want the duty rung", first.Rung)
	}
	if first.Authority.Kind != runstore.AuthorityPolicy ||
		first.Authority.Ref != conductor.DutyRef(conductor.DutyImprovesBabel) {
		t.Errorf("duty authority = %+v, want policy/%s",
			first.Authority, conductor.DutyRef(conductor.DutyImprovesBabel))
	}
	if len(first.Recipes) != 1 || first.Recipes[0] != conductor.DutyImprovesBabel {
		t.Errorf("duty cycle ran %v, want exactly the duty's own recipe", first.Recipes)
	}
	if len(first.Sessions) != 0 {
		t.Errorf("duty cycle was scoped to %v; a duty reads whatever corpus this host has", first.Sessions)
	}
	if !strings.Contains(first.Note, "standing duty") || !strings.Contains(first.Note, "never drawn") {
		t.Errorf("duty cycle note = %q, want it to state the duty and its first draw", first.Note)
	}

	// The second duty of the same dimension follows, because it is the next one
	// due — one toggle, two duties.
	second := d.once(t)
	if second.Authority.Ref != conductor.DutyRef(conductor.DutyMechanizationAudit) {
		t.Fatalf("second cycle authority = %+v, want the audit duty", second.Authority)
	}

	// Both are inside their cadence now, and the personal duty is unauthorized,
	// so there is nothing left to draw: the rung does not repeat a duty within
	// the day just because it is the only rung with anything to offer.
	third := d.once(t)
	if third.Outcome != conductor.OutcomeIdle {
		t.Fatalf("third cycle = %+v, want idle: both authorized duties ran today", third)
	}
	for _, s := range d.duties.States(d.clk.now) {
		if s.Name == conductor.DutyTunesItself {
			continue
		}
		if s.Due {
			t.Errorf("duty %q is due again within its cadence", s.Name)
		}
		if !strings.Contains(s.Note, "next draw after") {
			t.Errorf("duty %q note = %q, want it to say when it comes back", s.Name, s.Note)
		}
	}

	// A day later the first duty is due again, and says when it last ran.
	d.clk.advance(conductor.DefaultDutyCadence)
	fourth := d.once(t)
	if fourth.Authority.Ref != conductor.DutyRef(conductor.DutyImprovesBabel) {
		t.Fatalf("cycle after the cadence = %+v, want the first duty again", fourth.Authority)
	}
	if !strings.Contains(fourth.Note, "last drawn") {
		t.Errorf("note = %q, want it to name the previous draw", fourth.Note)
	}
	if len(d.runner.runs) != 3 {
		t.Errorf("the runner was called %d times, want 3: one per drawn duty", len(d.runner.runs))
	}
}

// The cadence is a property of the journal, so a restarted conductor does not
// hand its duties a fresh day. This is the same rule the serendipity floor's
// ratio follows, and for the same reason: scheduling state kept anywhere but
// the record disagrees with the record after a crash.
func TestDutyCadenceSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	d := newDutyLoop(t, conductor.Duties{TunesItself: true}, 100)

	drawn := d.once(t)
	if drawn.Authority.Ref != conductor.DutyRef(conductor.DutyTunesItself) {
		t.Fatalf("cycle = %+v, want the personal duty", drawn.Authority)
	}

	// A second conductor over the same journal, with its own rung and its own
	// idea of now: the duty is still not due.
	restarted := conductor.NewDutyRung(conductor.Duties{TunesItself: true}, d.journal, d.clk.Now, 0)
	_, err := restarted.Draw(ctx, conductor.DrawRequest{RunID: "run-after-restart", At: d.clk.now})
	if !errors.Is(err, conductor.ErrNoWork) {
		t.Fatalf("Draw after a restart = %v, want ErrNoWork within the cadence", err)
	}
	states := restarted.States(d.clk.now)
	var seen bool
	for _, s := range states {
		if s.Name != conductor.DutyTunesItself {
			continue
		}
		seen = true
		if s.Due {
			t.Error("the personal duty is due again immediately after a restart")
		}
		if s.LastDrawnAt != drawn.StartedAt {
			t.Errorf("last drawn = %s, want the journalled cycle's start %s", s.LastDrawnAt, drawn.StartedAt)
		}
	}
	if !seen {
		t.Fatalf("States does not report the personal duty: %+v", states)
	}

	// Past the cadence it comes back, on the restarted rung, without any of the
	// original process's memory.
	if _, err := restarted.Draw(ctx, conductor.DrawRequest{
		RunID: "run-tomorrow",
		At:    d.clk.now.Add(conductor.DefaultDutyCadence),
	}); err != nil {
		t.Fatalf("Draw past the cadence = %v, want the duty", err)
	}
}

// The operator outranks the loop's dutifulness. A due duty waits behind an
// invitation, whatever the cadence says.
func TestOperatorInvitationOutranksADueDuty(t *testing.T) {
	d := newDutyLoop(t, conductor.Duties{ImprovesBabel: true, TunesItself: true}, 100)
	d.invitations.work = &conductor.Assignment{Invitation: "inv-1", Note: "an operator asked"}

	cycle := d.once(t)
	if cycle.Rung != conductor.RungInvitation {
		t.Fatalf("cycle drew from %q with an invitation waiting and duties due", cycle.Rung)
	}

	// With the queue drained the duty follows, so waiting behind the operator
	// is a delay rather than a cancellation.
	d.invitations.work = nil
	if next := d.once(t); next.Rung != conductor.RungPolicy {
		t.Fatalf("cycle after the invitation drew from %q, want the duty rung", next.Rung)
	}
}

// The protected serendipity fraction still binds against a rung that has
// standing work. Duties are exactly the pressure that would otherwise converge
// the loop into pure dutifulness, so the floor is checked before them.
func TestSerendipityFloorStillBindsAgainstDueDuties(t *testing.T) {
	d := newDutyLoop(t, conductor.Duties{ImprovesBabel: true, TunesItself: true}, 2)
	d.floor.work = &conductor.Assignment{
		Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d-1"},
		Note:      "no aim",
	}

	// Three duties are due on day one, so without a floor the first three
	// cycles would all be dutiful. One cycle in two must be chaotic instead.
	var rungs []string
	for range 4 {
		rungs = append(rungs, d.once(t).Rung)
	}
	chaotic := 0
	for i, rung := range rungs {
		if rung == conductor.RungSerendipity {
			chaotic++
			continue
		}
		if i > 0 && rungs[i-1] != conductor.RungSerendipity {
			t.Errorf("cycles %v ran two dutiful cycles in a row under a one-in-two floor", rungs)
			break
		}
	}
	if chaotic < 2 {
		t.Errorf("cycles = %v, want at least half of them chaotic", rungs)
	}
}
