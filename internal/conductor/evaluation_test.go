package conductor_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/reality"
	runstore "github.com/atyrode/babel/internal/run"
)

// These cases are about SPEC §5.8: the protected share of cycles spent
// reviewing output that already exists. What they defend is the shape of the
// authorization and the accounting rather than the judgement — a review drawn
// without authorization, a reservation abandoned on a refusal, or a coverage
// check that only happens when there is budget to spend would each be the
// loop quietly doing something other than what was authorized.

// fakeReviews is the evaluation service with planted answers.
type fakeReviews struct {
	coverage conductor.ReviewCoverage
	sweepErr error
	sweeps   int

	draws   int
	draw    *conductor.ReviewDraw
	drawErr error

	// skips records every claim handed back, with the reason, which is what a
	// reconciliation test reads.
	skips   []string
	skipErr error
}

func (r *fakeReviews) Sweep(context.Context) (conductor.ReviewCoverage, error) {
	r.sweeps++
	if r.sweepErr != nil {
		return conductor.ReviewCoverage{}, r.sweepErr
	}
	return r.coverage, nil
}

func (r *fakeReviews) Draw(_ context.Context, runID string, seed uint64) (conductor.ReviewDraw, error) {
	r.draws++
	if r.drawErr != nil {
		return conductor.ReviewDraw{}, r.drawErr
	}
	draw := *r.draw
	draw.RunID = runID
	draw.Seed = seed
	return draw, nil
}

func (r *fakeReviews) Skip(_ context.Context, d conductor.ReviewDraw, reason string) error {
	r.skips = append(r.skips, d.AssignmentID+": "+reason)
	return r.skipErr
}

// fakeFocus is the recorded expenditure policy with a planted verdict.
type fakeFocus struct {
	permitted bool
	err       error
	requests  []reality.AdmitRequest
}

func (f *fakeFocus) Admit(_ context.Context, in reality.AdmitRequest) (reality.Admission, error) {
	f.requests = append(f.requests, in)
	if f.err != nil {
		return reality.Admission{}, f.err
	}
	return reality.Admission{
		Work:      in.Work,
		Permitted: f.permitted,
		Allowance: reality.AllowanceFull,
		Policy:    3,
	}, nil
}

// aDraw is a claimed review with every replay input populated, so a test that
// asserts the assignment survived the journal is asserting the whole claim
// rather than an identifier.
func aDraw() *conductor.ReviewDraw {
	return &conductor.ReviewDraw{
		AssignmentID:   "asg-1",
		SubjectKind:    "proposal",
		SubjectID:      "prop-7",
		Role:           "reception",
		Lane:           "coverage",
		PolicyVersion:  "policy-2",
		ContextVersion: "ctx-9",
		InputDigest:    "sha256:abc",
		Fence:          4,
		ReservedCost:   0.05,
		ExpiresAt:      day.Add(10 * time.Minute),
		Subjects:       []string{"babel"},
		Note:           "oldest due initial review",
	}
}

// evaluationLoop assembles a loop whose ladder has no work at all and whose
// only source of cycles is the evaluation share, so a drawn cycle can only
// have come from the share under test.
type evaluationLoop struct {
	loop    *conductor.Conductor
	reviews *fakeReviews
	focus   *fakeFocus
	rung    *conductor.EvaluationRung
	journal *conductor.Journal
	clk     *dutyClock
	runner  *fakeRunner
}

func newEvaluationLoop(t *testing.T, oneIn int, reviews *fakeReviews, focus conductor.Focus) *evaluationLoop {
	t.Helper()
	e := &evaluationLoop{
		reviews: reviews,
		journal: testJournal(t),
		clk:     &dutyClock{now: day},
		runner:  &fakeRunner{},
	}
	if f, ok := focus.(*fakeFocus); ok {
		e.focus = f
	}
	e.rung = conductor.NewEvaluationRung(reviews, focus, nil, e.clk.Now, time.Hour)
	share := conductor.Evaluation{OneIn: oneIn}
	if oneIn > 0 {
		// A rung with no share is refused at construction, which is its own
		// case below; the harness models the ordinary pairing.
		share.Rung = e.rung
	}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		// The floor is effectively off so that a cycle the evaluation share
		// did not draw is an idle cycle rather than a serendipity one.
		Floor:      conductor.Floor{OneIn: 1000},
		Ladder:     []conductor.Rung{&stubRung{name: conductor.RungInvitation}, &stubRung{name: conductor.RungSerendipity}},
		Runner:     e.runner,
		Ledger:     fakeLedger{},
		Journal:    e.journal,
		Now:        e.clk.Now,
		Evaluation: share,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.loop = loop
	return e
}

func (e *evaluationLoop) once(t *testing.T) conductor.Cycle {
	t.Helper()
	cycle, err := e.loop.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	return cycle
}

// A share of zero draws no review however much is outstanding, and a rung
// supplied without a share is refused at construction rather than sitting
// there claiming the loop reviews.
func TestEvaluationShareIsOffUntilAllocated(t *testing.T) {
	reviews := &fakeReviews{coverage: conductor.ReviewCoverage{Unreviewed: 40}, draw: aDraw()}
	e := newEvaluationLoop(t, 0, reviews, nil)

	cycle := e.once(t)
	if cycle.Outcome != conductor.OutcomeIdle {
		t.Fatalf("cycle = %+v, want idle with no evaluation share", cycle)
	}
	if reviews.draws != 0 {
		t.Errorf("the service was asked for %d reviews with no share allocated", reviews.draws)
	}
	if reviews.sweeps != 0 {
		t.Errorf("the coverage inventory was swept %d times with no share allocated", reviews.sweeps)
	}

	rung := conductor.NewEvaluationRung(reviews, nil, nil, nil, 0)
	_, err := conductor.New(conductor.Config{
		Ceilings: testCeilings, Ladder: []conductor.Rung{&stubRung{name: conductor.RungSerendipity}},
		Runner: e.runner, Ledger: fakeLedger{}, Journal: e.journal,
		Evaluation: conductor.Evaluation{Rung: rung},
	})
	if err == nil {
		t.Fatal("New accepted an evaluation rung with no share of cycles to draw")
	}
}

// A drawn review reaches the runner as the whole claim, and the journal keeps
// it: fence, seed, policy and context versions included. That is what lets an
// interrupted evaluation cycle resume against the claim it already took
// instead of drawing a second review of the same revision.
func TestDrawnReviewSurvivesTheJournal(t *testing.T) {
	reviews := &fakeReviews{
		coverage: conductor.ReviewCoverage{Unreviewed: 3, Due: 1, LastCheck: day},
		draw:     aDraw(),
	}
	e := newEvaluationLoop(t, 1, reviews, &fakeFocus{permitted: true})

	cycle := e.once(t)
	if cycle.Rung != conductor.RungEvaluation {
		t.Fatalf("cycle rung = %q, want the evaluation share", cycle.Rung)
	}
	if cycle.Authority.Kind != runstore.AuthorityPolicy {
		t.Errorf("cycle authority = %+v, want a policy authority", cycle.Authority)
	}
	if !strings.Contains(cycle.Authority.Ref, "asg-1") {
		t.Errorf("cycle authority %q does not name the assignment it drew", cycle.Authority.Ref)
	}
	if cycle.Evaluation == nil {
		t.Fatal("the journalled cycle carries no drawn review")
	}
	if cycle.Evaluation.Fence != 4 || cycle.Evaluation.PolicyVersion != "policy-2" ||
		cycle.Evaluation.ContextVersion != "ctx-9" || cycle.Evaluation.InputDigest != "sha256:abc" {
		t.Errorf("the journalled claim lost a replay input: %+v", cycle.Evaluation)
	}
	if len(e.runner.runs) != 1 {
		t.Fatalf("the runner was called %d times, want once", len(e.runner.runs))
	}
	got := e.runner.runs[0].assignment
	if got.Evaluation == nil || got.Evaluation.AssignmentID != "asg-1" {
		t.Fatalf("the runner was handed %+v, want the drawn assignment", got.Evaluation)
	}
	if got.Evaluation.RunID != e.runner.runs[0].runID {
		t.Errorf("the claim names run %q, the cycle ran as %q",
			got.Evaluation.RunID, e.runner.runs[0].runID)
	}
	// The replayed assignment is the journalled one, which is what a resume
	// hands the runner: no second draw is needed to reconstruct it.
	replayed := e.journal.Reverse()[0]
	if replayed.Evaluation == nil || replayed.Evaluation.Fence != 4 {
		t.Errorf("the journal's own row lost the claim: %+v", replayed.Evaluation)
	}
}

// The recorded expenditure policy is consulted about the record that was
// actually drawn, and a refusal gives the claim back with the reason instead
// of holding a reservation or voting against the record.
func TestFocusRefusalGivesTheClaimBack(t *testing.T) {
	reviews := &fakeReviews{coverage: conductor.ReviewCoverage{Unreviewed: 2}, draw: aDraw()}
	focus := &fakeFocus{permitted: false}
	e := newEvaluationLoop(t, 1, reviews, focus)

	cycle := e.once(t)
	if cycle.Outcome != conductor.OutcomeIdle {
		t.Fatalf("cycle = %+v, want idle after the policy withheld the drawn review", cycle)
	}
	if len(e.runner.runs) != 0 {
		t.Fatalf("a worker ran for a review the policy withheld")
	}
	if len(reviews.skips) != 1 {
		t.Fatalf("the claim was given back %d times, want exactly once: %v", len(reviews.skips), reviews.skips)
	}
	if !strings.Contains(reviews.skips[0], "asg-1") ||
		!strings.Contains(reviews.skips[0], "expenditure policy") {
		t.Errorf("the skip reason %q does not say which claim was withheld and why", reviews.skips[0])
	}
	if len(focus.requests) != 1 {
		t.Fatalf("the policy was consulted %d times, want once", len(focus.requests))
	}
	req := focus.requests[0]
	if req.Work != reality.WorkSubjectSpecific {
		t.Errorf("the policy was asked about %q work; a review is subject-specific", req.Work)
	}
	if len(req.Names) != 1 || req.Names[0] != "babel" {
		t.Errorf("the policy was asked about %v, want the subject's own recorded names", req.Names)
	}
}

// Budget exhaustion and an empty inventory are both "nothing to draw" rather
// than a failed cycle, and each stays legible in the rung's own depth note: an
// operator asking why the loop stopped reviewing must be able to tell a spent
// allowance from a corpus that is fully reviewed.
func TestBudgetAndIdleAreBothNoWorkWithDistinctReasons(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		note  string
		draws int
	}{
		{
			name: "allowance spent",
			err:  fmt.Errorf("%w: the authorized review allowance is spent", conductor.ErrNoWork),
			note: "allowance is spent",
		},
		{
			name: "nothing eligible",
			err:  fmt.Errorf("%w: every activated obligation is satisfied", conductor.ErrNoWork),
			note: "obligation is satisfied",
		},
		{
			name: "coordination unreachable",
			err:  fmt.Errorf("%w: evaluation coordination is unreachable", conductor.ErrNoWork),
			note: "coordination is unreachable",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reviews := &fakeReviews{
				coverage: conductor.ReviewCoverage{Unreviewed: 5, Overdue: 2, LastCheck: day},
				drawErr:  tc.err,
			}
			e := newEvaluationLoop(t, 1, reviews, &fakeFocus{permitted: true})

			cycle := e.once(t)
			if cycle.Outcome != conductor.OutcomeIdle {
				t.Fatalf("cycle = %+v, want idle rather than a failed cycle", cycle)
			}
			if len(reviews.skips) != 0 {
				t.Errorf("a claim was given back for a draw that took nothing: %v", reviews.skips)
			}
			depth, err := e.rung.Depth(context.Background())
			if err != nil {
				t.Fatalf("Depth: %v", err)
			}
			if !strings.Contains(depth.Note, tc.note) {
				t.Errorf("depth note = %q, want it to name %q", depth.Note, tc.note)
			}
			// The backlog is still reported: nothing became reviewed because
			// the loop could not spend on it.
			if depth.Waiting != 5 {
				t.Errorf("depth waiting = %d, want the 5 still outstanding", depth.Waiting)
			}
			if !strings.Contains(depth.Note, "2 overdue") {
				t.Errorf("depth note = %q, want the overdue count beside the backlog", depth.Note)
			}
		})
	}
}

// The coverage check and "everything is reviewed" are separate facts. A sweep
// runs before the draw, so a cycle that then finds nothing drawable has still
// advanced the durable inspection — and the sweep is bounded by its cadence so
// a status view does not rebuild the projection per call.
func TestCoverageCheckHappensEvenWithNothingToDraw(t *testing.T) {
	reviews := &fakeReviews{
		coverage: conductor.ReviewCoverage{Unreviewed: 9, Overdue: 9, LastCheck: day},
		drawErr:  fmt.Errorf("%w: the authorized review allowance is spent", conductor.ErrNoWork),
	}
	e := newEvaluationLoop(t, 1, reviews, &fakeFocus{permitted: true})

	e.once(t)
	if reviews.sweeps != 1 {
		t.Fatalf("the inventory was swept %d times, want once before the draw", reviews.sweeps)
	}
	if reviews.draws != 1 {
		t.Fatalf("the service was asked for %d reviews, want one attempt", reviews.draws)
	}
	// Inside the cadence, neither a second cycle nor a status view sweeps
	// again.
	e.once(t)
	if _, err := e.rung.Depth(context.Background()); err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if reviews.sweeps != 1 {
		t.Errorf("the inventory was swept %d times inside one cadence window", reviews.sweeps)
	}
	e.clk.advance(time.Hour + time.Minute)
	if _, err := e.rung.Depth(context.Background()); err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if reviews.sweeps != 2 {
		t.Errorf("the inventory was swept %d times after the cadence elapsed, want 2", reviews.sweeps)
	}
}

// A sweep that fails does not stop the draw, and it does not pretend the check
// happened either: the assignments already in the inventory stay claimable
// while the failure travels to the status view.
func TestFailedSweepStillDrawsAndSaysSo(t *testing.T) {
	reviews := &fakeReviews{sweepErr: errors.New("projection is unwritable"), draw: aDraw()}
	e := newEvaluationLoop(t, 1, reviews, &fakeFocus{permitted: true})

	cycle := e.once(t)
	if cycle.Rung != conductor.RungEvaluation {
		t.Fatalf("cycle rung = %q, want the review to be drawn despite the failed sweep", cycle.Rung)
	}
	depth, err := e.rung.Depth(context.Background())
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if !strings.Contains(depth.Note, "could not be read") {
		t.Errorf("depth note = %q, want it to report the failed check", depth.Note)
	}
	if coverage := e.rung.Coverage(); coverage.LastCheck != (time.Time{}) {
		t.Errorf("a failed sweep recorded a check at %v", coverage.LastCheck)
	}
}

// The protected share does not starve the operator and is not starved by them:
// invitations still outrank it, and it still gets its cycle when the ladder
// above has work every time.
func TestEvaluationShareKeepsItsFractionUnderABusyLadder(t *testing.T) {
	reviews := &fakeReviews{coverage: conductor.ReviewCoverage{Unreviewed: 50}, draw: aDraw()}
	e := &evaluationLoop{
		reviews: reviews,
		journal: testJournal(t),
		clk:     &dutyClock{now: day},
		runner:  &fakeRunner{},
	}
	e.rung = conductor.NewEvaluationRung(reviews, &fakeFocus{permitted: true}, nil, e.clk.Now, time.Hour)
	invitations := &stubRung{
		name: conductor.RungInvitation,
		work: &conductor.Assignment{Note: "an operator asked"},
	}
	loop, err := conductor.New(conductor.Config{
		Ceilings:   testCeilings,
		Floor:      conductor.Floor{OneIn: 1000},
		Ladder:     []conductor.Rung{invitations, &stubRung{name: conductor.RungSerendipity}},
		Runner:     e.runner,
		Ledger:     fakeLedger{},
		Journal:    e.journal,
		Now:        e.clk.Now,
		Evaluation: conductor.Evaluation{OneIn: 3, Rung: e.rung},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	e.loop = loop

	rungs := make([]string, 0, 6)
	for range 6 {
		rungs = append(rungs, e.once(t).Rung)
	}
	// The share counts the cycles since the last review, exactly as the
	// serendipity floor and the consolidation share count their own, so it
	// becomes due on the third cycle rather than pre-empting the first.
	// That is the boundary in both directions: the operator's queue is drawn
	// on every cycle the share does not take, and the share still gets its
	// fraction against a queue that never empties.
	want := []string{
		conductor.RungInvitation, conductor.RungInvitation, conductor.RungEvaluation,
		conductor.RungInvitation, conductor.RungInvitation, conductor.RungEvaluation,
	}
	for i, got := range rungs {
		if got != want[i] {
			t.Fatalf("cycle %d drew %q, want %q (whole run %v)", i+1, got, want[i], rungs)
		}
	}
}

// A cancelled cycle leaves the claim recorded rather than drawing a second
// review: the journal holds the assignment, and the runner's failure is the
// cycle's verdict rather than a reason to draw again.
func TestCancelledReviewDoesNotDrawASecond(t *testing.T) {
	reviews := &fakeReviews{coverage: conductor.ReviewCoverage{Unreviewed: 4}, draw: aDraw()}
	e := newEvaluationLoop(t, 1, reviews, &fakeFocus{permitted: true})
	e.runner.err = context.Canceled
	e.runner.result = conductor.Result{Cancelled: true, ReceiptID: "rcpt-cancel"}

	cycle, err := e.loop.Once(context.Background())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeFailed || !cycle.Cancelled {
		t.Fatalf("cycle = %+v, want a recorded cancelled cycle", cycle)
	}
	if cycle.Evaluation == nil || cycle.Evaluation.AssignmentID != "asg-1" {
		t.Fatalf("the cancelled cycle lost its claim: %+v", cycle.Evaluation)
	}
	if reviews.draws != 1 {
		t.Errorf("the service was asked for %d reviews across one cancelled cycle", reviews.draws)
	}
	if len(reviews.skips) != 0 {
		t.Errorf("a cancelled review's claim was given back by the scheduler: %v", reviews.skips)
	}
}
