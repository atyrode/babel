package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/reality"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// This file is the command layer's half of SPEC §4.12: it opens the evaluation
// service beside the analysis stores, adapts it to the scheduler's narrow
// seam, and runs a drawn review through internal/explore's reviewer under the
// same profile, grant and ceilings an exploration cycle runs under.
//
// Nothing here decides anything about evaluation. The policy is the operator's
// and lives in the evaluation store; the selection is the service's; the
// blinding, the result contract and the record are internal/explore's and
// internal/evaluation's. What this file owns is the wiring and the two
// refusals that belong to a command surface: an unconfigured machine, and an
// operator authorization that has not been given.

// EvaluationRecipe is the cookbook asset a review runs under.
//
// It is the recipe the v1 proposal-triage duty used, at its new version. That
// is the cutover rather than a coincidence: the operator's authorization, the
// recipe identity and the surface are the same three things they were, and
// what changed is the contract the recipe states — reception, evidence,
// relevance and observed outcomes over every kind of reviewable output,
// instead of a ranked pass with a mandatory counterargument over unruled
// proposals. A second recipe beside it would be the parallel active policy
// this cutover exists to remove.
const EvaluationRecipe = conductor.DutyTriagesTheQueue

// evaluationServices is one open evaluation service and everything opened
// beneath it.
type evaluationServices struct {
	service *evaluation.Service
	reality *reality.Store
	fleet   *fleet.Reader
	close   func()
}

// Close releases the service and the handles this opener owns, in reverse
// order. The analysis state is not among them: it is borrowed.
func (s *evaluationServices) Close() {
	if s == nil {
		return
	}
	if s.close != nil {
		s.close()
	}
	if s.fleet != nil {
		s.fleet.Close()
	}
	if s.reality != nil {
		s.reality.Close()
	}
}

// openEvaluation opens the evaluation service over an already-open analysis
// state.
//
// The fleet reader is opened when this machine has one and left nil when it
// does not, which internal/evaluation reads as intentional local mode. A
// configured fleet that will not open is a different statement and is reported
// rather than silently becoming local mode: a machine that cannot reach the
// shared catalog must not review under a local budget while believing it is
// sharing one.
func (a *app) openEvaluation(ctx context.Context, state *analysisState) (*evaluationServices, error) {
	ledger, err := openReality()
	if err != nil {
		return nil, fmt.Errorf("evaluation: the Reality Ledger is what grounds recorded work and pain, so it is required: %w", err)
	}
	out := &evaluationServices{reality: ledger}
	reader, err := a.fleetReader(ctx)
	switch {
	case err == nil:
		out.fleet = reader
	case fleet.NotConfigured(err):
		a.diagf("evaluation: no shared catalog is configured; this machine evaluates its own records only\n")
	default:
		out.Close()
		return nil, fmt.Errorf("evaluation: the shared catalog is configured and would not open: %w", err)
	}

	service, cleanup, err := a.openEvaluationServices(ctx, state.frontier, ledger, state.sync,
		out.fleet, nil, evaluationSourceOptions(state)...)
	if err != nil {
		out.Close()
		return nil, err
	}
	out.service, out.close = service, cleanup
	return out, nil
}

// reviewsAdapter is the evaluation service as internal/conductor's ladder
// needs it: sweep, draw, give back.
//
// It is an adapter rather than the service satisfying conductor.Reviews
// directly, for the reason every other seam into the conductor is one: the
// scheduler is told what is outstanding in its own vocabulary and has no path
// to the projection, the claim or the record behind it. The translation it
// performs is exactly two things — counts into the loop's coverage value, and
// the service's sentinels into the loop's ErrNoWork with the reason attached.
type reviewsAdapter struct {
	service *evaluation.Service
	// diag narrates a degraded sweep or a refused draw on the operator's
	// diagnostic stream, because an autonomous loop that silently stopped
	// reviewing is the opaque behaviour #96 exists to replace.
	diag func(format string, args ...any)
}

// Sweep refreshes the coverage inventory and records that the check happened.
func (r *reviewsAdapter) Sweep(ctx context.Context) (conductor.ReviewCoverage, error) {
	coverage, err := r.service.Check(ctx)
	if err != nil {
		return conductor.ReviewCoverage{}, err
	}
	return conductor.ReviewCoverage{
		Unreviewed:  coverage.Unreviewed,
		Due:         coverage.Due,
		Overdue:     coverage.Overdue,
		Unsupported: coverage.Unsupported,
		Blocked:     coverage.Blocked,
		Active:      coverage.Active,
		LastCheck:   coverage.LastCheck,
		Reason:      coverage.Reason,
	}, nil
}

// Draw reserves and claims one review.
//
// Every reason there is nothing to draw becomes ErrNoWork carrying that
// reason, and the four reasons stay distinguishable in the text because they
// call for different answers: nothing eligible is a loop that is keeping up, a
// spent allowance is the operator's ceiling working, a lost claim is another
// worker having won the same deterministic assignment, and an unreachable
// coordinator is an outage. None of them is a failed cycle, and none of them
// falls back to a local budget — an unreachable coordinator draws nothing at
// all rather than drawing against an allowance it cannot account for.
func (r *reviewsAdapter) Draw(ctx context.Context, runID string, seed uint64) (conductor.ReviewDraw, error) {
	a, err := r.service.Draw(ctx, runID, seed)
	switch {
	case err == nil:
	case errors.Is(err, evaluation.ErrNoWork):
		return conductor.ReviewDraw{}, fmt.Errorf("%w: %v", conductor.ErrNoWork, err)
	case errors.Is(err, evaluation.ErrBudget):
		return conductor.ReviewDraw{}, fmt.Errorf("%w: the authorized review allowance is spent: %v",
			conductor.ErrNoWork, err)
	case errors.Is(err, evaluation.ErrConflict):
		return conductor.ReviewDraw{}, fmt.Errorf("%w: another worker took the same review: %v",
			conductor.ErrNoWork, err)
	case errors.Is(err, evaluation.ErrUnavailable):
		// Shared coordination is configured and unreachable. Nothing is
		// drawn, and nothing degrades to a local allowance: the share simply
		// does not run while the fleet cannot be accounted to, and the reason
		// reaches the status view and the operator's stream.
		r.diag("conductor: evaluation coordination is unreachable, so no review is drawn: %s\n",
			Sanitize(err.Error()))
		return conductor.ReviewDraw{}, fmt.Errorf("%w: evaluation coordination is unreachable: %v",
			conductor.ErrNoWork, err)
	default:
		return conductor.ReviewDraw{}, err
	}
	return conductor.ReviewDraw{
		AssignmentID:   a.ID,
		SubjectKind:    a.Subject.Kind,
		SubjectID:      a.Subject.ID,
		RunID:          a.RunID,
		Role:           a.Role,
		Lane:           a.Lane,
		PolicyVersion:  a.PolicyVersion,
		ContextVersion: a.ContextVersion,
		Seed:           a.Seed,
		InputDigest:    a.InputDigest,
		Fence:          a.Fence,
		ReservedCost:   a.ReservedCost,
		ExpiresAt:      a.ExpiresAt,
		Subjects:       a.Subjects,
	}, nil
}

// Skip gives a claimed assignment back with the reason it was not spent on.
//
// It is a submission rather than a release, because that is what reconciles
// the reservation: the store journals the attempt as skipped, finishes the
// shared claim with the cost actually incurred — nothing, here, since no
// worker ran — and records no vote. A refused subject stays a visible gap
// instead of collecting a negative judgement it never received.
func (r *reviewsAdapter) Skip(ctx context.Context, d conductor.ReviewDraw, reason string) error {
	_, err := r.service.Submit(ctx, evaluation.Submission{
		AssignmentID: d.AssignmentID,
		RunID:        d.RunID,
		Fence:        d.Fence,
		SkipReason:   reason,
	})
	return err
}

// evaluationShare is the protected evaluation share and the rung that draws
// it, built only when the operator has authorized review cycles and asked for
// a share of them.
//
// Both are required, and the asymmetry is the point. A share with no
// authorization is a dial nobody consented to act on; an authorization with no
// share is consent to review with no cycles allocated to it, which is a
// legitimate state an operator reaches by turning the toggle on before
// deciding the ratio. Neither starts compute, and the stored evaluation policy
// being disabled stops it a third time on the service's own side.
func evaluationShare(oneIn int, authorized bool, reviews conductor.Reviews,
	focus conductor.Focus, cadence time.Duration, diag func(string, ...any)) conductor.Evaluation {
	if !authorized || oneIn <= 0 || reviews == nil {
		return conductor.Evaluation{}
	}
	if diag != nil {
		diag("conductor: evaluating one cycle in %d, drawn from the coverage inventory\n", oneIn)
	}
	return conductor.Evaluation{
		OneIn: oneIn,
		Rung:  conductor.NewEvaluationRung(reviews, focus, drawGenerator(), nil, cadence),
	}
}

// evaluationRunner turns one drawn review into a supervised worker run.
//
// It is the review counterpart of conductorRunner and deliberately the same
// shape: it fixes a corpus scope through fixScope and runs one job through
// internal/explore, adding no authority of its own. The grant, the profile and
// the ceilings are the loop's, and the blinding and the result contract are
// internal/explore's.
type evaluationRunner struct {
	app     *app
	state   *analysisState
	service *evaluation.Service
	worker  worker.Config
	profile worker.ProfileRef
	host    string

	adapters  []adapter.Adapter
	scanRoots []string
	presence  presence.Announcer
	// topics is §4.13's filing surface, nil on a machine that could not open
	// the ledger. A filing assignment drawn without it is refused before a
	// worker starts rather than producing an answer nothing can record.
	topics explore.TopicService
	// budget bounds one review's retrieval and egress, on the same terms an
	// exploration's does.
	budget explore.Budget
}

// Run carries out one drawn review and reports what the cycle spent.
//
// The corpus scope is this host's whole reachable corpus rather than the
// sessions the target's evidence happens to name, and that is deliberate. An
// evidence check looks for what would contradict a claim as much as for what
// supports it, and contrary evidence is by definition not in the sessions the
// claim cited; narrowing the scope to the citation would let a record's own
// choice of evidence bound the review of it. Disclosure is unchanged either
// way — the same grant, the same redaction, the same brokered index — and the
// preparation records exactly what was read.
func (r *evaluationRunner) Run(ctx context.Context, runID string, draw conductor.ReviewDraw,
	authority runstore.Authority) (conductor.Result, *explore.ReviewRun, error) {
	assignment := evaluation.Assignment{
		ID:             draw.AssignmentID,
		Subject:        evaluation.Subject{Kind: draw.SubjectKind, ID: draw.SubjectID},
		RunID:          runID,
		Role:           draw.Role,
		PolicyVersion:  draw.PolicyVersion,
		ContextVersion: draw.ContextVersion,
		Seed:           draw.Seed,
		InputDigest:    draw.InputDigest,
		ExpiresAt:      draw.ExpiresAt,
		Fence:          draw.Fence,
		ReservedCost:   draw.ReservedCost,
		Lane:           draw.Lane,
		Subjects:       draw.Subjects,
	}
	// The lease is kept alive from here rather than from the launch, because
	// the claim is already held here and the corpus scan and scope fixing
	// below are what actually consumed it: the four reviews this deployment
	// lost on 2026-09-12 never reached a model, they were refused a review
	// context for a claim that lapsed during this preparation. Stopping is
	// deferred, so the renewals end with this review rather than outliving it.
	defer r.keepLease(ctx, assignment)()

	sessions, _ := r.app.scanCorpus(ctx, r.adapters, r.scanRoots, true)
	if len(sessions) == 0 {
		// A review with no corpus cannot check a locator, so it is not run at
		// all: the claim is given back as a skip and the gap stays visible.
		// Submitting an assessment formed with no ability to verify anything
		// would be the manufactured judgement §4.12 forbids.
		return conductor.Result{}, nil, r.giveBack(ctx, draw,
			"this host has no sessions to check the record's evidence against")
	}
	scoped, err := r.app.fixScope(ctx, r.state.runs, sessions, r.host, false)
	if err != nil {
		return conductor.Result{}, nil, err
	}
	return r.carry(ctx, assignment, scoped, authority)
}

// carry runs one already-claimed assignment, drawn or named.
//
// A correction reaches the worker through here rather than through the rung:
// its claim is the service's, it is already reserved, and what differs is only
// that the assignment names the statement it supersedes.
func (r *evaluationRunner) carry(ctx context.Context, assignment evaluation.Assignment,
	scoped scopedCorpus, authority runstore.Authority) (conductor.Result, *explore.ReviewRun, error) {
	reviewer, release, err := r.reviewer(assignment.Role)
	if err != nil {
		return conductor.Result{}, nil, err
	}
	defer release()
	runID := assignment.RunID
	draw := conductor.ReviewDraw{
		SubjectKind: assignment.Subject.Kind,
		SubjectID:   assignment.Subject.ID,
		Role:        assignment.Role,
	}
	r.app.diagf("reviewing %s %s in the %s role as run %s (%s)...\n",
		Sanitize(draw.SubjectKind), Sanitize(draw.SubjectID), Sanitize(draw.Role),
		Sanitize(runID), Sanitize(authority.String()))

	out, runErr := reviewer.Review(ctx, explore.ReviewOptions{
		Assignment:  assignment,
		Corrects:    assignment.Corrects,
		Preparation: scoped.prep,
		Authority:   authority,
		Budget:      r.budget,
		OnProgress: func(p worker.ProgressRecord) {
			r.app.diagf("review: %s %s\n", Sanitize(p.Stage), Sanitize(p.Message))
		},
	})
	result := conductor.Result{PreparationID: string(scoped.prep.ID)}
	if out == nil {
		return result, nil, runErr
	}
	result.Failures = len(out.Failures)
	result.Cancelled = out.Cancelled
	// An unpriced boundary reports no figure to the journal: a reservation
	// with no currency beside it would read as a measurement nobody made.
	if !out.Unpriced {
		result.Cost, result.Currency = out.Cost, out.Currency
	}
	if out.Receipt != nil {
		result.ReceiptID = string(out.Receipt.Header.ID)
	}
	r.app.reportReview(out)
	return result, out, runErr
}

// leaseRenewalDivisor is how many renewals fit in one lease window.
//
// Three: the first renewal lands a third of the way in, so two consecutive
// ticks can be lost - a slow write, a busy database - before the lease the
// worker is holding lapses. Renewing at half the lease leaves one chance and
// renewing every few seconds writes to the claim table for no more safety.
const leaseRenewalDivisor = 3

// keepLease renews this review's claim while it runs, and returns the stop
// that ends the renewals.
//
// The ticker is the counterpart of what a lease is for: expiry exists to
// release the claim of a worker that stopped answering, so a worker that is
// still answering has to say so. Nothing here extends the work - the
// reservation, the ceilings and the allowance are untouched - it extends only
// the window in which this run is the holder.
func (r *evaluationRunner) keepLease(ctx context.Context, a evaluation.Assignment) func() {
	every := renewalInterval(r.leaseWindow(ctx, a))
	if every <= 0 {
		// An assignment with neither a readable policy nor a window left is
		// not a claim this runner can extend, and a ticker on a zero interval
		// is a busy loop.
		r.app.diagf("review: the lease on %s cannot be renewed, so it runs on the window it was granted\n",
			Sanitize(a.ID))
		return func() {}
	}
	return leaseKeeper{
		id:    a.ID,
		every: every,
		diag:  r.app.diagf,
		renew: func(c context.Context) (time.Time, error) {
			return r.service.RenewClaim(c, a.ID, a.RunID, a.Fence)
		},
	}.start(ctx)
}

// leaseKeeper is one claim's renewal loop.
//
// It holds the renewal as a function rather than a service handle because what
// this type owns is a lifetime, not an authority: who may extend a lease and
// by how much is internal/evaluation's, and keeping the two apart is what
// makes the loop's stop-and-join testable without a deployment.
type leaseKeeper struct {
	id    string
	every time.Duration
	diag  func(string, ...any)
	renew func(context.Context) (time.Time, error)
}

// start begins the renewals and returns the stop that ends them.
//
// stop cancels the loop and waits for it, so no renewal can outlive the review
// that asked for it: a renewal arriving after the run has finished would be
// this process holding an assignment nothing is working on, which is the state
// expiry exists to release.
func (k leaseKeeper) start(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(k.every)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				expires, err := k.renew(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					k.diag("review: the lease on %s was not extended: %s\n",
						Sanitize(k.id), Sanitize(err.Error()))
					if unrenewable(err) {
						// The authority is gone, or this deployment cannot
						// extend a lease at all. Ticking on would repeat one
						// sentence every interval for the rest of the review
						// and never regain what was lost.
						return
					}
					continue
				}
				k.diag("review: the lease on %s now runs to %s\n",
					Sanitize(k.id), expires.UTC().Format(time.RFC3339))
			}
		}
	}()
	return func() { cancel(); <-done }
}

// unrenewable reports whether a refused renewal will stay refused. A takeover,
// an unknown assignment and an authority that cannot extend a lease at all are
// final; anything else - a busy database, a slow write - is this tick's
// problem and not the review's.
func unrenewable(err error) bool {
	return errors.Is(err, evaluation.ErrConflict) || errors.Is(err, evaluation.ErrNotFound) ||
		errors.Is(err, evaluation.ErrUnavailable) || errors.Is(err, evaluation.ErrInvalid)
}

// leaseWindow is how long a lease this review may renew for.
//
// The effective policy's lease is the authorized window. A policy this
// instance cannot read falls back to what is left of the window the grant
// itself carries, which is a lower bound on the same number: a tick too often
// is a wasted write, while a tick too rarely is the failure renewal exists to
// fix.
func (r *evaluationRunner) leaseWindow(ctx context.Context, a evaluation.Assignment) time.Duration {
	if policy, err := r.service.Policy(ctx); err == nil && policy.LeaseSeconds > 0 {
		return time.Duration(policy.LeaseSeconds) * time.Second
	}
	if a.ExpiresAt.IsZero() {
		return 0
	}
	return time.Until(a.ExpiresAt)
}

// renewalInterval is how often a lease of this length is renewed: a third of
// it, never under a second.
func renewalInterval(lease time.Duration) time.Duration {
	if lease <= 0 {
		return 0
	}
	return max(lease/leaseRenewalDivisor, time.Second)
}

// review is the conductor runner's evaluation path: one drawn review carried
// out under the cycle's own run identity and authority.
//
// It builds the review runner from the loop's own handles rather than opening
// its own, which is what keeps a scheduled review and a headless one the same
// review: the same service, the same profile, the same ceilings, the same
// grant. What differs is the authority the receipt records, which is the only
// thing that should.
func (r *conductorRunner) review(ctx context.Context, runID string,
	a conductor.Assignment) (conductor.Result, error) {
	runner := &evaluationRunner{
		app:       r.app,
		state:     r.state,
		service:   r.evaluation,
		worker:    r.worker,
		profile:   r.profile,
		host:      r.host,
		adapters:  r.adapters,
		scanRoots: r.scanRoots,
		presence:  r.presence,
		topics:    r.topics,
	}
	result, _, err := runner.Run(ctx, runID, *a.Evaluation, a.Authority)
	return result, err
}

// giveBack reconciles a claim this runner declined to spend on.
func (r *evaluationRunner) giveBack(ctx context.Context, draw conductor.ReviewDraw, reason string) error {
	r.app.diagf("review: %s\n", Sanitize(reason))
	adapter := &reviewsAdapter{service: r.service, diag: r.app.diagf}
	if err := adapter.Skip(context.WithoutCancel(ctx), draw, reason); err != nil {
		return fmt.Errorf("give assignment %s back: %w", draw.AssignmentID, err)
	}
	return nil
}

// reviewer builds the review runner for one assignment.
//
// The grant is corpus search alone. A review reads Babel's archive to check
// what a record cited and to look for what contradicts it; it materializes no
// repository, executes nothing, and reaches no network — so granting a
// capability this build cannot serve would name a facility with no version to
// record, which is the same rule an exploration's grant follows.
//
// The recipe is chosen by the assignment's role, because §4.13's filing pass
// and §4.12's review are two methods rather than one: a filing decides what a
// record is about and may create nothing, and a reviewer handed the filing
// recipe's body would be reading instructions for an authority it does not
// have. Both recipes are loaded either way, so the receipt records the
// cookbook this build carries rather than the subset one assignment used.
func (r *evaluationRunner) reviewer(role string) (*explore.Reviewer, func(), error) {
	d, err := babelDirs()
	if err != nil {
		return nil, nil, err
	}
	recipes, err := recipeSet([]string{EvaluationRecipe, FilingRecipe})
	if err != nil {
		return nil, nil, err
	}
	recipe := EvaluationRecipe
	if role == evaluation.RoleFiling {
		recipe = FilingRecipe
	}
	idx, err := index.Open(d.indexDir())
	if err != nil {
		return nil, nil, err
	}
	ledger, err := explore.OpenLedger(r.state.dir)
	if err != nil {
		idx.Close()
		return nil, nil, err
	}
	// A reviewer is built per assignment because a review is one job. The two
	// handles it borrows are released when the review ends rather than held
	// for the loop's whole life: a share that draws one cycle in N would
	// otherwise keep a database open for hours to use it for minutes.
	release := func() {
		ledger.Close()
		idx.Close()
	}
	wcfg := r.worker
	wcfg.Diagnostics = &sanitizingWriter{w: r.app.stderr, prefix: "worker: "}
	reviewer, err := explore.NewReviewer(explore.ReviewConfig{
		Service: r.service,
		Recipes: recipes,
		Recipe:  recipe,
		Topics:  r.topics,
		Grant: worker.Grant{
			Capabilities: []worker.Capability{worker.CapabilityCorpusSearch},
			Disclosure:   worker.DisclosureLocal,
		},
		Capabilities: runstore.CapabilityVersions{Tool: "babel/" + readBuildIdentity().Version},
		Profile:      r.profile,
		Worker:       wcfg,
		Runs:         r.state.runs,
		Ledger:       ledger,
		Index:        idx,
		Presence:     r.presence,
		// The conversation is archived as a Babel session under the review's
		// own profile and recipe, which is what makes a later reader able to
		// tell which contract the judgement was formed under.
		Transcript: r.app.analysisTranscripts(explorePlan{profile: r.profile, recipes: recipes}),
	})
	if err != nil {
		release()
		return nil, nil, err
	}
	return reviewer, release, nil
}

// reportReview narrates one finished review on the operator's stream.
//
// It says what was recorded rather than what was judged. A vote is one word
// and an operator reading a loop's output needs to know that an assessment
// exists and is attributed; the assessment itself belongs on the evaluation
// page beside the record it is about.
func (a *app) reportReview(out *explore.ReviewRun) {
	switch {
	case out.Reused:
		a.diagf("review: assignment %s was already assessed by this run; nothing was submitted again\n",
			Sanitize(out.AssignmentID))
	case out.Failed != "":
		a.diagf("review: assignment %s failed and its reservation was reconciled: %s\n",
			Sanitize(out.AssignmentID), Sanitize(out.Failed))
	case out.Skipped != "":
		a.diagf("review: assignment %s was skipped: %s\n",
			Sanitize(out.AssignmentID), Sanitize(out.Skipped))
	default:
		parts := make([]string, 0, 3)
		if out.Assessment != nil {
			if out.Assessment.Vote != "" {
				parts = append(parts, "vote "+out.Assessment.Vote)
			}
			if out.Assessment.Outcome != "" {
				parts = append(parts, "outcome "+out.Assessment.Outcome)
			}
			if n := len(out.Assessment.Contributions); n > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", n, plural(n, "contribution", "contributions")))
			}
		}
		if len(parts) == 0 {
			parts = append(parts, "no judgement")
		}
		a.diagf("review: recorded %s as %s (%s)\n", Sanitize(out.Record.ID),
			Sanitize(strings.Join(parts, ", ")), blindedLabel(out.Blinded))
	}
}

func blindedLabel(blinded bool) string {
	if blinded {
		return "taken blind"
	}
	return "with prior evaluations revealed"
}

const evaluateUsage = `Usage: babel evaluate [flags]

Draw one authorized review from the evaluation coverage inventory and carry it
out, then stop. It is the headless form of one evaluation cycle: the same
service, the same claim, the same blinding and the same record the conductor's
evaluation share produces, without the loop around it.

The machine must be configured for it. ` + "`babel conductor configure`" + ` states the
ceilings a review spends against and authorizes review work with
--` + conductor.DutyTriagesTheQueue + `; the evaluation policy itself is the
operator's and is configured in the browser. Neither saving that policy nor
setting a share starts compute: this command, and the conductor's own share,
are the only two things that do.

Flags:
      --correct ID     re-review one record this machine recorded, superseding it
      --retrievals N   cap the corpus searches this review serves
      --fetches N      cap the public documents this review fetches
      --json           emit what the review recorded as JSON
`

// evaluateResult is `babel evaluate --json`.
type evaluateResult struct {
	// Drawn reports whether a review was available at all. False with an
	// empty Reason cannot happen: a draw that took nothing always says why.
	Drawn  bool   `json:"drawn"`
	Reason string `json:"reason,omitempty"`

	AssignmentID string `json:"assignment_id,omitempty"`
	RunID        string `json:"run_id,omitempty"`
	SubjectKind  string `json:"subject_kind,omitempty"`
	SubjectID    string `json:"subject_id,omitempty"`
	// Corrects names the statement this review superseded, empty for a draw.
	Corrects string `json:"corrects,omitempty"`
	Role     string `json:"role,omitempty"`
	Lane     string `json:"lane,omitempty"`
	Blinded  bool   `json:"blinded,omitempty"`

	// Recipe and RecipeVersion state the contract the judgement was formed
	// under, which is what a later reader compares a re-review against (§7).
	Recipe        string `json:"recipe,omitempty"`
	RecipeVersion int    `json:"recipe_version,omitempty"`

	RecordID      string  `json:"record_id,omitempty"`
	Vote          string  `json:"vote,omitempty"`
	Outcome       string  `json:"outcome,omitempty"`
	Contributions int     `json:"contributions,omitempty"`
	Skipped       string  `json:"skipped,omitempty"`
	Failed        string  `json:"failed,omitempty"`
	ReceiptID     string  `json:"receipt_id,omitempty"`
	Cost          float64 `json:"cost,omitempty"`
	Currency      string  `json:"currency,omitempty"`

	// Coverage is the inventory as the sweep this command performed left it,
	// so a run that drew nothing still reports what is outstanding.
	Coverage evaluateCoverage `json:"coverage"`
}

// evaluateCoverage is the coverage inventory in machine-readable form.
type evaluateCoverage struct {
	Unreviewed  int    `json:"unreviewed"`
	Due         int    `json:"due"`
	Overdue     int    `json:"overdue"`
	Unsupported int    `json:"unsupported"`
	Blocked     int    `json:"blocked"`
	Active      int    `json:"active"`
	LastCheck   string `json:"last_check,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// evaluate implements `babel evaluate`: one review, headless.
//
// It shares every mechanism with the conductor's evaluation share rather than
// reimplementing one, which is the same rule `babel explore` and a conductor
// cycle follow: a scheduled review and a summoned one are the same review in
// every respect except the authority they record.
func (a *app) evaluate(ctx context.Context, args []string) error {
	c := newCmd("evaluate", evaluateUsage)
	var wf workerFlags
	var sf scanFlags
	wf.bind(c.fs)
	sf.bindRoots(c)
	retrievals := c.fs.Int("retrievals", 0, "cap the corpus searches this review serves")
	fetches := c.fs.Int("fetches", 0, "cap the public documents this review fetches")
	asJSON := c.fs.Bool("json", false, "emit what the review recorded as JSON")
	correct := c.fs.String("correct", "", "re-review one record and supersede its statement")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	settings, err := loadConductorSettings()
	if err != nil {
		return err
	}
	if settings.Ceilings == nil {
		return a.reportUnconfiguredConductor()
	}
	if !settings.BabelTriagesTheQueue {
		// The authorization is the operator's and this command cannot stand
		// in for it. Reviewing is Babel forming attributed judgements about
		// records the operator has not ruled on, which is exactly the act
		// the toggle exists to consent to, and a headless command that
		// bypassed it would make the toggle advisory.
		fmt.Fprintf(a.stdout,
			"review work is not authorized on this machine.\n\n"+
				"Babel forms attributed judgements about records you have not ruled on,\n"+
				"so it asks first. Authorize it with:\n\n"+
				"  babel conductor configure --%s\n\n"+
				"Nothing is scheduled by that alone: the evaluation policy stays yours,\n"+
				"and a share of cycles is what the loop spends.\n", conductor.DutyTriagesTheQueue)
		return nil
	}
	analysis, err := loadAnalysisSettings()
	if err != nil {
		return err
	}
	wcfg, ok := wf.resolve(analysis)
	if !ok {
		return a.reportNoWorker()
	}
	profileRef, err := storedProfile(c, analysis)
	if err != nil {
		return err
	}
	host, err := (&repoFlags{}).hostID(c)
	if err != nil {
		return err
	}

	state, err := openAnalysisState()
	if err != nil {
		return err
	}
	// See explore's: registered before the handles it must follow, so the
	// review's own closure and anything a sibling lane stranded publish as
	// this command exits rather than waiting for `babel sync` (SPEC.md §9.1).
	defer a.drainOnExit(ctx)
	defer state.Close()
	services, err := a.openEvaluation(ctx, state)
	if err != nil {
		return err
	}
	defer services.Close()

	// A correction is claimed by naming the statement it supersedes rather
	// than drawn: the worker that wrote it is blinded to its own history and
	// could not ask for the revisit, and the extra pass is paid work that
	// has to be reserved before any of it happens.
	if *correct != "" {
		return a.correctReview(ctx, services, state, *correct, evaluationRunnerConfig{
			worker: wcfg, profile: profileRef, host: host,
			scanRoots: sf.rootList(),
			budget:    explore.Budget{Retrievals: *retrievals, Fetches: *fetches},
		}, *asJSON)
	}

	reviews := &reviewsAdapter{service: services.service, diag: a.diagf}
	rung := conductor.NewEvaluationRung(reviews, conductorFocus(services.reality),
		drawGenerator(), nil, settings.evaluateCadence())

	// One command, one draw, through the same rung the loop uses — so the
	// coverage sweep, the expenditure gate and the reservation reconciliation
	// this command performs are the loop's, not a second implementation of
	// them that could decide differently.
	runID := newEvaluationRunID(time.Now())
	assignment, drawErr := rung.Draw(ctx, conductor.DrawRequest{RunID: runID, At: time.Now()})
	depth, _ := rung.Depth(ctx)
	res := evaluateResult{Coverage: coverageDocument(rung)}
	if errors.Is(drawErr, conductor.ErrNoWork) {
		res.Reason = depth.Note
		if err := a.emitEvaluate(res, *asJSON); err != nil {
			return err
		}
		a.reportEvaluationPolicy(ctx, services, *asJSON)
		return nil
	}
	if drawErr != nil {
		return drawErr
	}

	draw := *assignment.Evaluation
	res.Drawn = true
	res.AssignmentID, res.RunID = draw.AssignmentID, draw.RunID
	res.SubjectKind, res.SubjectID = draw.SubjectKind, draw.SubjectID
	res.Role, res.Lane = draw.Role, draw.Lane

	runner := &evaluationRunner{
		app:       a,
		state:     state,
		service:   services.service,
		worker:    wcfg,
		profile:   profileRef,
		host:      host,
		adapters:  adapters(),
		scanRoots: sf.rootList(),
		presence:  nil,
		topics:    topicService(state.frontier, services.reality, state.sessionCatalog),
		budget:    explore.Budget{Retrievals: *retrievals, Fetches: *fetches},
	}
	announcer, closePresence := a.openPresence(ctx)
	defer closePresence()
	runner.presence = announcer

	result, out, runErr := runner.Run(ctx, draw.RunID, draw, assignment.Authority)
	res.ReceiptID, res.Cost, res.Currency = result.ReceiptID, result.Cost, result.Currency
	res.Recipe = EvaluationRecipe
	if draw.Role == evaluation.RoleFiling {
		res.Recipe = FilingRecipe
	}
	if version, ok := recipeVersion(res.Recipe); ok {
		res.RecipeVersion = version
	}
	if out != nil {
		res.Blinded = out.Blinded
		res.RecordID = out.Record.ID
		res.Skipped, res.Failed = out.Skipped, out.Failed
		if out.Assessment != nil {
			res.Vote, res.Outcome = out.Assessment.Vote, out.Assessment.Outcome
			res.Contributions = len(out.Assessment.Contributions)
		}
	}
	res.Coverage = coverageDocument(rung)
	if err := a.emitEvaluate(res, *asJSON); err != nil {
		return err
	}
	return runErr
}

// evaluationRunnerConfig is what a review needs from the command line, apart
// from the handles the command already opened.
type evaluationRunnerConfig struct {
	worker    worker.Config
	profile   worker.ProfileRef
	host      string
	scanRoots []string
	budget    explore.Budget
}

// authorityCommandEvaluateCorrect attributes a correction to the operator who
// asked for it, distinctly from a review the loop drew.
const authorityCommandEvaluateCorrect = "babel evaluate --correct"

// correctReview re-reviews one recorded statement and supersedes it.
//
// The run identity is the one that authored the statement rather than a fresh
// one, because a correction is that run changing its own mind: a different
// identity writing it would be a second reviewer's opinion wearing a link to
// somebody else's. The claim is the service's, reserved before the worker
// starts, so the second pass is paid for out of the same allowance as the
// first.
func (a *app) correctReview(ctx context.Context, services *evaluationServices, state *analysisState,
	recordID string, cfg evaluationRunnerConfig, asJSON bool) error {
	target, err := services.service.Record(ctx, recordID)
	if err != nil {
		return err
	}
	assignment, err := services.service.Correction(ctx, recordID, target.Provenance.RunID,
		drawGenerator().Uint64())
	if err != nil {
		return err
	}
	runner := &evaluationRunner{
		app:       a,
		state:     state,
		service:   services.service,
		worker:    cfg.worker,
		profile:   cfg.profile,
		host:      cfg.host,
		adapters:  adapters(),
		scanRoots: cfg.scanRoots,
		topics:    topicService(state.frontier, services.reality, state.sessionCatalog),
		budget:    cfg.budget,
	}
	// The correction's claim is reserved above and is held from here, so its
	// lease is kept alive from here too - the scan below is the same
	// preparation that outlived a 240s lease four times on this machine.
	defer runner.keepLease(ctx, assignment)()

	sessions, _ := a.scanCorpus(ctx, adapters(), cfg.scanRoots, true)
	if len(sessions) == 0 {
		return fmt.Errorf("babel: this host has no sessions to check the record's evidence against")
	}
	scoped, err := a.fixScope(ctx, state.runs, sessions, cfg.host, false)
	if err != nil {
		return err
	}
	announcer, closePresence := a.openPresence(ctx)
	defer closePresence()
	runner.presence = announcer

	result, out, runErr := runner.carry(ctx, assignment, scoped, runstore.Authority{
		Kind: runstore.AuthorityOperator,
		Ref:  authorityCommandEvaluateCorrect + " " + recordID,
	})
	res := evaluateResult{
		Drawn:        true,
		AssignmentID: assignment.ID,
		RunID:        assignment.RunID,
		SubjectKind:  assignment.Subject.Kind,
		SubjectID:    assignment.Subject.ID,
		Role:         assignment.Role,
		Lane:         assignment.Lane,
		Corrects:     recordID,
		Recipe:       EvaluationRecipe,
		ReceiptID:    result.ReceiptID,
		Cost:         result.Cost,
		Currency:     result.Currency,
	}
	if version, ok := evaluationRecipeVersion(); ok {
		res.RecipeVersion = version
	}
	if out != nil {
		res.Blinded = out.Blinded
		res.RecordID = out.Record.ID
		res.Skipped, res.Failed = out.Skipped, out.Failed
		if out.Assessment != nil {
			res.Vote, res.Outcome = out.Assessment.Vote, out.Assessment.Outcome
			res.Contributions = len(out.Assessment.Contributions)
		}
	}
	if coverage, err := services.service.Coverage(ctx); err == nil {
		res.Coverage = evaluateCoverage{
			Unreviewed: coverage.Unreviewed, Due: coverage.Due, Overdue: coverage.Overdue,
			Unsupported: coverage.Unsupported, Blocked: coverage.Blocked,
			Active: coverage.Active, Reason: coverage.Reason,
		}
		if !coverage.LastCheck.IsZero() {
			res.Coverage.LastCheck = coverage.LastCheck.UTC().Format(time.RFC3339)
		}
	}
	if err := a.emitEvaluate(res, asJSON); err != nil {
		return err
	}
	return runErr
}

// coverageDocument renders the rung's last swept inventory.
func coverageDocument(rung *conductor.EvaluationRung) evaluateCoverage {
	coverage := rung.Coverage()
	doc := evaluateCoverage{
		Unreviewed:  coverage.Unreviewed,
		Due:         coverage.Due,
		Overdue:     coverage.Overdue,
		Unsupported: coverage.Unsupported,
		Blocked:     coverage.Blocked,
		Active:      coverage.Active,
		Reason:      coverage.Reason,
	}
	if !coverage.LastCheck.IsZero() {
		doc.LastCheck = formatTime(coverage.LastCheck)
	}
	return doc
}

// reportEvaluationPolicy names the remaining activation step when a machine
// that is authorized and allocated still draws nothing.
//
// The deployment's evaluation policy is a separate consent from this
// machine's: §5.8 refuses to start compute because a backlog exists, so an
// authorized share over a disabled policy is a legitimate state rather than a
// fault. It is also the state a machine is in the first time it is set up,
// and an operator reading "no review was drawn" deserves to be told which of
// the three switches is still off rather than left to guess.
//
// It is advice on the human stream only. The JSON document is a contract and
// already carries the stopping reason; a second sentence in it would be a
// field nobody asked for.
func (a *app) reportEvaluationPolicy(ctx context.Context, services *evaluationServices, asJSON bool) {
	if asJSON {
		return
	}
	policy, err := services.service.Policy(ctx)
	switch {
	case errors.Is(err, evaluation.ErrUnavailable):
		fmt.Fprintf(a.stdout,
			"the deployment's evaluation policy could not be read from shared storage, so this "+
				"machine does not know whether review work is authorized.\n")
		return
	case err != nil:
		return
	case policy.Enabled:
		return
	}
	fmt.Fprintf(a.stdout,
		"\nthe deployment's evaluation policy is not enabled, so nothing is authorized to run.\n\n"+
			"It is the operator's and it is set in the browser, once for the deployment:\n\n"+
			"  babel web\n"+
			"  then Evaluation -> Review policy -> enable, and save\n\n"+
			"Saving it starts nothing by itself. This command, and the conductor's evaluation\n"+
			"share on a machine that authorized one, are the only two things that spend it.\n")
}

func (a *app) emitEvaluate(res evaluateResult, asJSON bool) error {
	if asJSON {
		return a.emitJSON(res)
	}
	if !res.Drawn {
		fmt.Fprintf(a.stdout, "no review was drawn: %s\n", Sanitize(res.Reason))
		return nil
	}
	fmt.Fprintf(a.stdout, "reviewed %s %s in the %s role (%s allocation)\n",
		Sanitize(res.SubjectKind), Sanitize(res.SubjectID), Sanitize(res.Role), Sanitize(res.Lane))
	if res.Coverage.Reason != "" {
		fmt.Fprintf(a.stdout, "coverage: %s\n", Sanitize(res.Coverage.Reason))
	}
	return nil
}

// newEvaluationRunID mints the run identity one headless review records under.
// It is prefixed so a receipt listing tells a review apart from an exploration
// without opening either.
func newEvaluationRunID(at time.Time) string {
	return fmt.Sprintf("eval-%s-%04d", at.UTC().Format("20060102T150405"), rand.N(10000))
}

// evaluationRecipeVersion reports the version of the recipe a review runs
// under, for the operator-facing surfaces that state which contract applied.
func evaluationRecipeVersion() (int, bool) { return recipeVersion(EvaluationRecipe) }

// recipeVersion reports one embedded recipe's version.
//
// It is the version a receipt, a provenance record and a topic question record
// as the contract that produced them: §5.1 makes a semantic change a version
// increment, so a stored artifact naming the version stays readable under the
// method that actually applied.
func recipeVersion(id string) (int, bool) {
	set, err := cookbook.Embedded()
	if err != nil {
		return 0, false
	}
	recipe, ok := set.ByID(id)
	if !ok {
		return 0, false
	}
	return recipe.Version, true
}
