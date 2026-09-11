package conductor

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/run"
)

// evaluationPrefix is what an evaluation cycle's authority reference begins
// with, so a receipt's why reads as an evaluation draw without a lookup table
// and the journal can be searched for the cycles spent reviewing rather than
// producing.
const evaluationPrefix = "evaluation:"

// DefaultEvaluationCadence is how often the coverage inventory is swept when
// no cadence was configured.
//
// An hour, which is shorter than a standing duty's day and longer than a
// cycle. The sweep is a projection refresh over records this machine already
// holds — no inference, no worker, no provider — and what it discovers is new
// artifacts, finished revisions and initial reviews that have come due. An
// hour keeps a fresh proposal reviewable within the same working session
// without re-reading the corpus between every cycle.
const DefaultEvaluationCadence = time.Hour

// ReviewCoverage is the evaluation coverage inventory as the ladder reads it.
//
// It is this package's own value rather than internal/evaluation's, for the
// reason Runner, Ledger and Focus are interfaces: the conductor is told what
// is outstanding and has no path to the projection that computed it. The
// counts it carries are the ones a scheduling decision and a status view need,
// which is deliberately fewer than the projection holds.
type ReviewCoverage struct {
	// Unreviewed and Due are the drawable backlog: output nothing has
	// assessed yet, and output whose assessment has come due again because
	// its revision or its recorded context moved.
	Unreviewed int
	Due        int
	// Overdue counts the reviews past their initial-review threshold. It is
	// reported separately from the backlog because it is the number that says
	// the reserved allocation is not keeping up.
	Overdue int
	// Unsupported and Blocked are the gaps: a kind or role this build has no
	// evaluator for, and work an explicit policy withholds. They are carried
	// so a status view can show them beside the backlog — §4.12 makes a
	// missing evaluator a visible gap rather than a reason to call something
	// reviewed.
	Unsupported int
	Blocked     int
	// Active counts the assignments claimed and not yet finished.
	Active int
	// LastCheck is when the sweep that produced these numbers finished.
	// "Coverage inspection completed" and "all eligible output reviewed" are
	// separate facts, and this is the first one.
	LastCheck time.Time
	// Reason explains a degraded or partial inventory, and is empty when
	// there is nothing to explain.
	Reason string
}

// backlog is the work the evaluation share could draw right now.
func (c ReviewCoverage) backlog() int { return c.Unreviewed + c.Due }

// ReviewDraw is one review the evaluation service handed over: the claimed
// assignment, the replay inputs, and what the loop needs to gate and journal
// it.
type ReviewDraw struct {
	// AssignmentID is the claimed assignment. It is deterministic from the
	// subject revision, the role, the context and policy versions and the
	// sample ordinal, so two workers drawing the same next review contend on
	// one claim and exactly one wins.
	AssignmentID string
	// SubjectKind and SubjectID name the exact record revision under review.
	SubjectKind string
	SubjectID   string
	// RunID is the run identity the claim was taken in the name of, which is
	// what a submission is validated against. It is carried rather than
	// recomputed because a resumed cycle must give the claim back under the
	// identity that took it, not under a fresh one.
	RunID string
	// Role is the review role: reception, evidence, challenge, comparison,
	// outcome or relevance.
	Role string
	// Lane is the allocation lane the draw came from — reserved coverage,
	// weighted, exploration, discovery or challenge — which is what makes the
	// policy's shares auditable after the fact.
	Lane string
	// PolicyVersion, ContextVersion, Seed and InputDigest are the replay
	// inputs. A seed alone is not sufficient: the captured input identity and
	// the policy version are what make a recorded draw reproducible.
	PolicyVersion  string
	ContextVersion string
	Seed           uint64
	InputDigest    string
	// Fence and ReservedCost are the claim: the token a stale owner cannot
	// validate with, and the allowance this cycle has reserved against the
	// fleet-wide ceiling.
	Fence        int64
	ReservedCost float64
	ExpiresAt    time.Time
	// Subjects are the names the record under review states about itself, so
	// the recorded expenditure policy can be consulted about it before a
	// worker is launched. Empty names no subject and withholds nothing, which
	// is the same failure direction the consolidation rung takes.
	Subjects []string
	// Note states what was drawn and why, in the operator's terms.
	Note string
}

// Reviews is the evaluation service as the ladder uses it.
//
// Three calls, and each is one the loop is allowed to make: sweep the coverage
// inventory, take one review, and give one back. Everything else about
// evaluation — the projection, the weighting, the shared claim, the record —
// is on the other side of this boundary, so scheduling cannot widen any of it.
type Reviews interface {
	// Sweep refreshes the coverage inventory and records that the check
	// happened, durably, whether or not any review budget remains.
	Sweep(ctx context.Context) (ReviewCoverage, error)
	// Draw reserves and claims one review. It reports ErrNoWork, wrapped with
	// the reason, when nothing is drawable — nothing eligible, the policy
	// disabled, the allowance spent, or shared coordination unreachable — so
	// a rung with nothing to do is never a loop that failed.
	Draw(ctx context.Context, runID string, seed uint64) (ReviewDraw, error)
	// Skip finishes a claimed assignment the loop decided not to spend on,
	// reconciling its reservation. It is what an expenditure refusal does
	// with a claim it has already taken.
	Skip(ctx context.Context, d ReviewDraw, reason string) error
}

// EvaluationRung draws the authorized review work of SPEC §5.8.
//
// It is a rung rather than a duty because a duty names a recipe and runs it
// over a corpus slice, and a review is not that shape: the work is chosen from
// a coverage inventory over already-durable output, bounded by a versioned
// policy and a fleet-wide allowance, and the assignment carries a subject and
// a role rather than a scope. The v1 proposal-triage duty was the shape this
// replaces, and it is not kept beside it: one active policy, not two.
//
// Backlog-first lives on the other side of Reviews. Oldest-due initial reviews
// have reserved attention there, and the rung's job is to put a cycle at the
// service's disposal and to refuse to spend one where the recorded expenditure
// policy says nothing may be spent.
type EvaluationRung struct {
	reviews Reviews
	focus   Focus
	rng     *rand.Rand
	now     func() time.Time
	cadence time.Duration

	// mu guards the memo of the last sweep. Depth and Draw are both callers
	// and a status view can run beside a cycle.
	mu        sync.Mutex
	coverage  ReviewCoverage
	lastSweep time.Time
	// reason is why the last draw took nothing, so a status view can say
	// "the allowance is spent" rather than reporting an empty queue.
	reason string
}

// NewEvaluationRung builds the evaluation share's rung over the service, the
// recorded expenditure policy, and the generator its draws are seeded from.
//
// The generator is supplied for the serendipity floor's reason: a draw an
// operator cannot replay is a weaker claim than it looks, and the seed the
// rung hands the service is recorded with the assignment. A nil clock is the
// real one, a non-positive cadence is DefaultEvaluationCadence, and a nil
// focus is a machine with no recorded policy, which withholds nothing.
func NewEvaluationRung(reviews Reviews, focus Focus, rng *rand.Rand,
	now func() time.Time, cadence time.Duration) *EvaluationRung {
	if now == nil {
		now = time.Now
	}
	if cadence <= 0 {
		cadence = DefaultEvaluationCadence
	}
	return &EvaluationRung{reviews: reviews, focus: focus, rng: rng, now: now, cadence: cadence}
}

// Name reports this rung's stable name.
func (r *EvaluationRung) Name() string { return RungEvaluation }

// Depth reports the drawable review backlog and the gaps beside it.
//
// The note carries the gaps and the last check deliberately. A depth of zero
// means different things — everything eligible is reviewed, no evaluator
// exists for what is left, an explicit policy withholds it, or the sweep has
// not run — and a status view that printed the same zero for all four would
// hide the one an operator has to act on.
//
// Nothing is drawn or recorded here beyond the sweep itself, and the sweep is
// bounded by the cadence, so opening a status view repeatedly does not turn
// into a projection rebuild per keystroke.
func (r *EvaluationRung) Depth(ctx context.Context) (Depth, error) {
	coverage, err := r.sweep(ctx)
	if err != nil {
		return Depth{Implemented: true, Note: "the coverage inventory could not be read: " + err.Error()}, nil
	}
	return Depth{Waiting: coverage.backlog(), Implemented: true, Note: r.note(coverage)}, nil
}

// Coverage reports the inventory the last sweep produced, for the surfaces
// that render the numbers rather than the one line Depth carries. It performs
// no I/O: a caller that wants a fresh sweep calls Depth or Draw, which is
// where the cadence lives.
func (r *EvaluationRung) Coverage() ReviewCoverage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.coverage
}

// note renders one line about the inventory behind a depth.
func (r *EvaluationRung) note(coverage ReviewCoverage) string {
	parts := make([]string, 0, 6)
	parts = append(parts, fmt.Sprintf("%d never reviewed, %d due again",
		coverage.Unreviewed, coverage.Due))
	if coverage.Overdue > 0 {
		parts = append(parts, fmt.Sprintf("%d overdue", coverage.Overdue))
	}
	if coverage.Unsupported > 0 {
		parts = append(parts, fmt.Sprintf("%d with no evaluator in this build", coverage.Unsupported))
	}
	if coverage.Blocked > 0 {
		parts = append(parts, fmt.Sprintf("%d withheld by policy", coverage.Blocked))
	}
	if coverage.Active > 0 {
		parts = append(parts, fmt.Sprintf("%d being reviewed now", coverage.Active))
	}
	switch {
	case coverage.LastCheck.IsZero():
		parts = append(parts, "no coverage check has finished yet")
	default:
		parts = append(parts, "coverage checked "+coverage.LastCheck.UTC().Format(time.RFC3339))
	}
	if coverage.Reason != "" {
		parts = append(parts, coverage.Reason)
	}
	r.mu.Lock()
	reason := r.reason
	r.mu.Unlock()
	if reason != "" {
		parts = append(parts, reason)
	}
	return strings.Join(parts, "; ")
}

// sweep refreshes the coverage inventory at most once per cadence.
//
// The sweep is the durable coverage check: it is the thing that records that
// an inspection finished, and it records it whether or not a review is drawn
// afterwards. That separation is the point — a machine whose review allowance
// is spent still has to be able to say when it last looked, and a check that
// only happened as a side effect of spending would report a coverage age that
// tracked the budget instead of the corpus.
func (r *EvaluationRung) sweep(ctx context.Context) (ReviewCoverage, error) {
	now := r.now()
	r.mu.Lock()
	if !r.lastSweep.IsZero() && now.Sub(r.lastSweep) < r.cadence {
		coverage := r.coverage
		r.mu.Unlock()
		return coverage, nil
	}
	r.mu.Unlock()

	coverage, err := r.reviews.Sweep(ctx)
	if err != nil {
		return ReviewCoverage{}, err
	}
	r.mu.Lock()
	r.coverage, r.lastSweep = coverage, now
	r.mu.Unlock()
	return coverage, nil
}

// Draw sweeps the inventory when the cadence is due, then takes one review.
//
// The order is the contract. The sweep runs first so that a cycle which then
// finds nothing drawable has still advanced the durable coverage check, and so
// that a review that came due since the last cycle is drawable in this one.
//
// The recorded expenditure policy is consulted after the claim rather than
// before it, and that is not an oversight: the service chooses which review to
// draw and only the claim says which record that turned out to be. A refused
// claim is given back as a skip, so the reservation is reconciled, the
// assignment stays a visible gap, and the record receives no negative vote for
// having been withheld — §4.8 and §4.12 both forbid turning a policy refusal
// into a judgement.
func (r *EvaluationRung) Draw(ctx context.Context, d DrawRequest) (Assignment, error) {
	if _, err := r.sweep(ctx); err != nil {
		// A sweep that failed is not a reason to skip the draw: the
		// projection may be stale while the assignments it already holds are
		// perfectly claimable, and refusing to review anything because a
		// refresh failed would let one unwritable projection stop the whole
		// share. The reason travels to the status view.
		r.setReason("the last coverage check failed: " + err.Error())
	}

	draw, err := r.reviews.Draw(ctx, d.RunID, r.seed())
	switch {
	case errors.Is(err, ErrNoWork):
		r.setReason(strings.TrimPrefix(err.Error(), ErrNoWork.Error()+": "))
		return Assignment{}, ErrNoWork
	case err != nil:
		return Assignment{}, err
	}
	r.setReason("")

	admission, err := r.admit(ctx, draw, d.At)
	if err != nil {
		// The policy could not be read. The claim is given back rather than
		// held: a machine that cannot reach its policy has not been told to
		// withhold anything, but it also must not sit on a reservation while
		// it works that out.
		r.give(ctx, draw, "the recorded expenditure policy could not be read: "+err.Error())
		return Assignment{}, ErrNoWork
	}
	if !admission.Permitted {
		reason := fmt.Sprintf("the recorded expenditure policy withholds %s work on this subject (allowance %s, policy version %d)",
			admission.Work, admission.Allowance, admission.Policy)
		r.give(ctx, draw, reason)
		r.setReason(reason)
		return Assignment{}, ErrNoWork
	}

	return Assignment{
		Rung: RungEvaluation,
		Authority: run.Authority{
			Kind: run.AuthorityPolicy,
			Ref:  evaluationPrefix + draw.Role + ":" + draw.AssignmentID,
		},
		Note:       TrimNote(drawNote(draw)),
		Evaluation: &draw,
	}, nil
}

// drawNote states what the loop decided, in the operator's terms.
func drawNote(draw ReviewDraw) string {
	note := fmt.Sprintf("%s review of %s %s from the %s allocation",
		draw.Role, draw.SubjectKind, draw.SubjectID, draw.Lane)
	if draw.Note != "" {
		note += ": " + draw.Note
	}
	return note
}

// admit consults the recorded expenditure policy about the drawn record.
//
// A review is subject-specific work. It is attention spent on one named
// record about one named subject, which is exactly what §4.8's deferral list
// is about, and classifying it as corpus reading — the allowance that exists
// so a withheld subject can still teach cross-cutting lessons — would let a
// learn-only allowance authorize an opinion about the subject itself.
func (r *EvaluationRung) admit(ctx context.Context, draw ReviewDraw, at time.Time) (reality.Admission, error) {
	if r.focus == nil {
		return reality.Admission{Work: reality.WorkSubjectSpecific, Permitted: true,
			Allowance: reality.AllowanceFull}, nil
	}
	return r.focus.Admit(ctx, reality.AdmitRequest{
		Names: draw.Subjects,
		Work:  reality.WorkSubjectSpecific,
		Note:  "evaluate " + draw.SubjectKind + " " + draw.SubjectID + " in the " + draw.Role + " role",
		AsOf:  at,
	})
}

// give hands a claimed assignment back, reconciling its reservation.
//
// A failure to give it back is recorded in the rung's reason rather than
// failing the cycle. The claim has a lease and the store's own reconciliation
// is what expires it; a loop that stopped because it could not return one
// assignment would turn a recoverable accounting gap into an outage.
func (r *EvaluationRung) give(ctx context.Context, draw ReviewDraw, reason string) {
	if err := r.reviews.Skip(context.WithoutCancel(ctx), draw, reason); err != nil {
		r.setReason(fmt.Sprintf("assignment %s could not be given back: %v", draw.AssignmentID, err))
	}
}

func (r *EvaluationRung) setReason(reason string) {
	r.mu.Lock()
	r.reason = reason
	r.mu.Unlock()
}

// seed is the replay input one draw is made under. A rung with no generator
// draws with a zero seed, which is a deterministic draw rather than an
// undeclared one: the service records whatever it was given.
func (r *EvaluationRung) seed() uint64 {
	if r.rng == nil {
		return 0
	}
	return r.rng.Uint64()
}

// Evaluation is the share of cycles spent reviewing already-durable output
// rather than producing more of it, and the rung that draws them.
//
// It is a protected fraction rather than a ladder position, for the reason the
// serendipity floor and the consolidation share are. Below the operator's
// invitations it would be starved by a busy queue; above them it would outrank
// a person. What it actually competes with is the loop's appetite for new
// output, and the honest way to state that is a ratio the operator sets.
//
// Off unless the operator authorized it. Saving an evaluation policy through
// the browser configures what a review would do and starts nothing: the share
// is what schedules compute, and it is set here, in the document that holds
// every other scheduling decision.
type Evaluation struct {
	// OneIn is the share: one cycle in N evaluates. Zero is off, which is
	// this build's default and the state of every machine whose operator has
	// not asked for review cycles.
	OneIn int
	// Rung draws the reviews. Nil with a positive share is a configuration
	// error rather than a silent no-op: a loop that reported an evaluation
	// share it could not draw would claim to be reviewing.
	Rung *EvaluationRung
}

// scheduled reports whether this build draws evaluation cycles at all.
func (e Evaluation) scheduled() bool { return e.OneIn > 0 && e.Rung != nil }

func (e Evaluation) validate() error {
	switch {
	case e.OneIn < 0:
		return errors.New("conductor: an evaluation share of one cycle in a negative number is not a share")
	case e.OneIn > 0 && e.Rung == nil:
		return errors.New("conductor: an evaluation share needs the rung that draws it")
	case e.OneIn == 0 && e.Rung != nil:
		return errors.New("conductor: an evaluation rung was supplied with no share of cycles to draw")
	}
	return nil
}

// due reports whether the protected evaluation fraction requires this cycle to
// review.
//
// It counts the cycles since the last evaluation cycle drawn, exactly as the
// serendipity floor and the consolidation share count their own, so the
// guarantee is a property of the journal rather than of one process's memory
// and survives a restart.
func (e Evaluation) due(history History) bool {
	if !e.scheduled() {
		return false
	}
	if e.OneIn <= 1 {
		return true
	}
	other := 0
	for _, cycle := range history.Reverse() {
		if !cycle.counts() {
			continue
		}
		if cycle.Rung == RungEvaluation {
			return false
		}
		other++
		if other >= e.OneIn-1 {
			return true
		}
	}
	return other >= e.OneIn-1
}
