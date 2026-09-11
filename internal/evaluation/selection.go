package evaluation

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"time"
)

// This file is the worker queue: which review, of which revision, in which
// role, is worth spending authorized attention on next.
//
// It is built apart from ranking.go on purpose (§E5) and answers a different
// question. Ranking asks what decision deserves a person's attention; this asks
// where review is still useful, which is a question about what Babel does not
// know rather than about what the operator wants.
//
// Five rules from docs/evaluation-lifecycle.md §E4 shape it, and each is
// visible in the code below rather than only in this comment.
//
// Coverage is reserved, not hoped for. A share of every cycle goes to the
// oldest-due initial reviews across all covered kinds, chosen deterministically
// by age; the weighted sampler never competes for that share. Without the
// reservation, a deployment arguing about its favourite proposal would starve
// the observation nobody has read, which is the exact failure the reserved lane
// exists to prevent.
//
// Randomness is recorded, not hidden. Every draw is a pure function of the seed,
// the policy version and the captured input digest, and all three are stored
// with the assignment — §E4 says outright that a seed alone is not sufficient.
// A replay re-derives the same draw; what it does not promise is that any
// individual draw preferred the highest-weight candidate, because a weighted
// sample that always picked the maximum would not be a sample.
//
// Attention is bounded in four independent ways: the per-cycle batch, the daily
// and per-cycle cost ceilings, the per-revision review cap, and the per-role
// skip bound. Each bounds a different runaway, and a subject that exhausts one
// of them becomes a visible gap rather than receiving a negative vote.
//
// Settled opinion rests. A revision whose reception has stopped moving is not
// asked again until its cooldown elapses, at either extreme — agreement and
// disagreement both — because repeating a vote adds nothing either way.
//
// Nothing here spends anything. The draw produces an assignment for the caller
// to claim through the store and the shared coordinator; the claim is what
// reserves budget, and a claim that loses to another worker is a conflict rather
// than a double spend.

// The allocation lanes one cycle's attention is divided into.
//
// This is NOT Item.Lane's vocabulary. Item.Lane is where an artifact sits in its
// lifecycle; this is which reservation paid for a review. Two vocabularies
// because they answer different questions, and one filter offering both would
// invite a query for "accepted coverage work" that means nothing.
const (
	// LaneCoverage is the reserved oldest-due initial review.
	LaneCoverage = "coverage"
	// LaneWeighted is the weighted sample over the remaining eligible work.
	LaneWeighted = "weighted"
	// LaneExploration is the share deliberately not justified by weight.
	LaneExploration = "exploration"
	// LaneDiscovery is SPEC §5.8's protected discovery share: artifacts with
	// no assessment in any role.
	LaneDiscovery = "discovery"
	// LaneChallenge is the bounded diagnostic work on persistent
	// disagreement. It is a lane of its own so that a cycle's accounting can
	// say how much was spent arguing rather than reviewing.
	LaneChallenge = "challenge"
)

// AllocationLanes lists the allocation lanes in a stable order.
func AllocationLanes() []string {
	return []string{LaneCoverage, LaneWeighted, LaneExploration, LaneDiscovery, LaneChallenge}
}

// ValidAllocationLane reports whether lane is an allocation lane.
func ValidAllocationLane(lane string) bool { return slices.Contains(AllocationLanes(), lane) }

// maxSkipAttempts bounds how many times one subject and role may be skipped or
// fail before it stops being drawn.
//
// Three, and the bound exists because §E4 requires unsupported sources and
// repeated skips to consume bounded attention and stay visible as gaps rather
// than receiving negative votes. Without it, a subject whose content no
// evaluator can process would be drawn forever, and the deployment's whole
// allowance would go to one unreadable record. With it, the third skip turns the
// subject into a reported gap and the budget moves on.
const maxSkipAttempts = 3

// settledMargin is the reception margin at which an opinion counts as settled.
//
// Eighty percent one-sided, in either direction. It is the same number for
// agreement and disagreement because the reason to stop asking is the same: the
// next vote is predictable, so it buys no information. What it does not do is
// close the question — a material change clears the cooldown, and the bounded
// challenge lane still examines a contested item.
const settledMargin = 0.8

// materialChangeWeight is how much a changed input multiplies a candidate's
// weight.
//
// Two, which is the largest single multiplier in the weight function: §E4
// requires attention to be restored on material changes, and a factor that
// merely nudged would leave a re-opened revision behind every lightly reviewed
// one.
const materialChangeWeight = 2.0

// drawInput is everything one draw reads.
//
// It is a value rather than a set of store calls because a draw must be
// reproducible: given this struct, the policy and the seed, selectDraw is a pure
// function, which is what makes a recorded draw replayable and what lets the
// regression tests exercise weighting and reservation without a database.
type drawInput struct {
	Policy Policy
	// Items is the complete eligible set from the current projection
	// snapshot. The whole set rather than a page, because the reserved lane
	// picks the oldest-due across all of it.
	Items []projected
	// Assignments are every assignment this instance knows about, local and
	// remote. They serve two purposes: an active claim removes a candidate,
	// and the count per subject/role/context/policy is the sample ordinal
	// that makes the next assignment id deterministic.
	Assignments []Assignment
	// Attempts are the exposure/completion/skip/failure journal, keyed by
	// assignment id.
	Attempts []Attempt
	// InputDigest identifies the captured input set this draw was taken
	// against, which is half of what makes it replayable.
	InputDigest string
	// SpentToday and SpentCycle are the authorized spend already accounted
	// for. They come from the attempt journal rather than from a counter, so
	// a restarted process cannot forget what it spent.
	SpentToday float64
	SpentCycle float64
	// ActiveClaims is how many assignments are claimed and unsettled, which
	// is what the batch bounds.
	ActiveClaims int
	Now          time.Time
}

// drawResult is one selection decision.
type drawResult struct {
	Assignment Assignment
	Lane       string
	// StopReason is why no assignment was produced, empty on success. It is
	// recorded either way, because "why is nothing being reviewed" is the
	// question an operator asks when the answer is not visible.
	StopReason string
	// Gaps are the candidates this draw declined and why, so an exhausted
	// draw is explainable without re-deriving it.
	Gaps []string
}

// candidate is one drawable unit of work: a revision, in a role, with the
// weight and age the sampler needs.
type candidate struct {
	Item  *projected
	Role  string
	State string
	// DueAt is when this obligation became due. For a never-reviewed
	// obligation it is the artifact's creation time, which is what makes
	// "oldest due" mean "longest unreviewed" rather than "oldest record with
	// a recent review".
	DueAt time.Time
	// Weight is the sampler's relative preference, always positive.
	Weight float64
	// Ordinal is the next sample ordinal for this subject and role, which
	// makes the assignment id deterministic across workers.
	Ordinal int
	// Initial reports an obligation nobody has met yet, which is what the
	// reserved coverage lane draws.
	Initial bool
	// Untouched reports an artifact with no assessment in any role, which is
	// what the protected discovery lane draws.
	Untouched bool
	// Revisit reports a candidate that is not an outstanding obligation: a
	// well-reviewed open idea whose cooldown has elapsed. Only the
	// exploration lane draws one, so a revisit can never displace due work
	// — and its existence is why an empty backlog is not the same as
	// nothing to sample.
	Revisit bool
}

// selectDraw chooses the next assignment.
//
// The order of the gates is the order of the rules they enforce, and it matters:
// a disabled policy refuses before any budget is consulted, the budget refuses
// before any candidate is built, and the candidate filter refuses before any
// randomness is drawn. Reading it the other way round would let a disabled
// deployment report "nothing eligible" and a budget-exhausted one report "no
// work", which are the two states a scheduler must never confuse.
func selectDraw(in drawInput, runID string, seed uint64) (drawResult, error) {
	policy := in.Policy
	if err := ValidatePolicy(policy); err != nil {
		return drawResult{}, err
	}
	if !policy.Enabled {
		return drawResult{StopReason: fmt.Sprintf(
				"policy %s has authorized evaluation disabled", policy.Version)}, fmt.Errorf(
				"%w: policy %s has authorized evaluation disabled", ErrNoWork, policy.Version)
	}
	if runID == "" {
		return drawResult{}, fmt.Errorf("%w: draw names no run", ErrInvalid)
	}

	reserved, reason, err := admitSpend(policy, in.ActiveClaims, in.SpentCycle, in.SpentToday)
	if err != nil {
		return drawResult{StopReason: reason}, err
	}

	candidates, gaps := buildCandidates(in)
	if len(candidates) == 0 {
		reason := "no eligible review remains: every activated obligation is satisfied, " +
			"in cooldown, capped, or withheld by recorded policy"
		return drawResult{StopReason: reason, Gaps: gaps},
			fmt.Errorf("%w: %s", ErrNoWork, reason)
	}

	lane, chosen := sample(candidates, policy, seed)
	if chosen == nil {
		reason := "no lane could be satisfied from the eligible set"
		return drawResult{StopReason: reason, Gaps: gaps},
			fmt.Errorf("%w: %s", ErrNoWork, reason)
	}
	// A challenge is accounted to its own lane whichever reservation drew
	// it, so a cycle's accounting can say how much went to arguing about
	// disagreement rather than to reviewing.
	if chosen.Role == RoleChallenge {
		lane = LaneChallenge
	}

	subject := chosen.Item.Artifact.Subject
	assignment := Assignment{
		ID: assignmentID(subject, chosen.Role, chosen.Item.Artifact.ContextVersion,
			policy.Version, chosen.Ordinal),
		Subject:        subject,
		RunID:          runID,
		Role:           chosen.Role,
		PolicyVersion:  policy.Version,
		ContextVersion: chosen.Item.Artifact.ContextVersion,
		Seed:           seed,
		InputDigest:    in.InputDigest,
		CreatedAt:      in.Now,
		ExpiresAt:      in.Now.Add(time.Duration(policy.LeaseSeconds) * time.Second),
		ReservedCost:   reserved,
		Lane:           lane,
		Subjects:       subjectNames(*chosen.Item),
	}
	return drawResult{Assignment: assignment, Lane: lane, Gaps: gaps}, nil
}

// reservedCost is what one review reserves against the cycle's allowance.
//
// The per-cycle ceiling divided by the batch, so a full batch reserves exactly
// one cycle's allowance and no more. Dividing rather than reserving the whole
// ceiling per review is what lets concurrent workers share one authorized
// allowance instead of each treating it as theirs — which is the failure §E4
// names explicitly.
func reservedCost(policy Policy) float64 {
	if policy.BatchSize <= 0 {
		return policy.PerCycleCost
	}
	return policy.PerCycleCost / float64(policy.BatchSize)
}

// admitSpend applies the three independent bounds on one cycle's attention and
// reports what a review reserves.
//
// It is one function because every path that claims work has to pass exactly
// these gates: the cycle batch, the per-cycle ceiling and the daily ceiling. A
// second copy for a correction or any other follow-up would be a second answer
// to "may this deployment spend now", and the two would drift the first time a
// bound was tuned.
//
// The reason is returned beside the error so a caller can record why it stopped
// without re-deriving it, and the error is always ErrBudget: none of these is
// "nothing to do".
func admitSpend(policy Policy, activeClaims int, spentCycle, spentToday float64) (float64, string, error) {
	reserved := reservedCost(policy)
	if activeClaims >= policy.BatchSize {
		reason := fmt.Sprintf("cycle batch of %d assignments is already claimed", policy.BatchSize)
		return reserved, reason, fmt.Errorf("%w: %s", ErrBudget, reason)
	}
	if spentCycle+reserved > policy.PerCycleCost {
		reason := fmt.Sprintf("per-cycle ceiling %.4f reached: %.4f spent, %.4f reserved per review",
			policy.PerCycleCost, spentCycle, reserved)
		return reserved, reason, fmt.Errorf("%w: %s", ErrBudget, reason)
	}
	if spentToday+reserved > policy.DailyCost {
		reason := fmt.Sprintf("daily ceiling %.4f reached: %.4f spent, %.4f reserved per review",
			policy.DailyCost, spentToday, reserved)
		return reserved, reason, fmt.Errorf("%w: %s", ErrBudget, reason)
	}
	return reserved, "", nil
}

// correctionID is the deployment-wide identity of one correction claim.
//
// Derived from the record being superseded plus the policy and context versions
// in force, and deliberately not from the run: two attempts to correct one
// statement under one policy are one claim, so a retried or duplicated
// correction contends rather than producing a second superseding record. The
// target id is what makes it distinct from the original assignment, which is
// required — a correction is a new claim with its own budget reservation.
func correctionID(targetRecordID, policyVersion, contextVersion string) string {
	return "eval-c-" + digestOf(targetRecordID, policyVersion, contextVersion)
}

// buildCandidates turns the projection into drawable work, and explains every
// exclusion.
//
// The gaps it returns are not diagnostics for their own sake. §E4 requires
// unsupported sources and repeatedly skipped subjects to stay visible as gaps
// rather than receiving negative votes, and an exhausted draw that could not say
// why would leave an operator with a silent queue and no way to tell a satisfied
// deployment from a stuck one.
func buildCandidates(in drawInput) ([]candidate, []string) {
	policy := in.Policy
	attemptsByAssignment := make(map[string][]Attempt, len(in.Attempts))
	for _, attempt := range in.Attempts {
		attemptsByAssignment[attempt.AssignmentID] = append(
			attemptsByAssignment[attempt.AssignmentID], attempt)
	}

	type roleKey struct {
		subject Subject
		role    string
	}
	var (
		active   = make(map[roleKey]int)
		ordinals = make(map[roleKey]int)
		setbacks = make(map[roleKey]int)
	)
	for _, assignment := range in.Assignments {
		key := roleKey{subject: assignment.Subject, role: assignment.Role}
		ordinals[key]++
		settled := false
		for _, attempt := range attemptsByAssignment[assignment.ID] {
			switch attempt.State {
			case AttemptCompleted:
				settled = true
			case AttemptSkipped, AttemptFailed:
				settled = true
				setbacks[key]++
			}
		}
		if !settled && assignment.ExpiresAt.After(in.Now) {
			active[key]++
		}
	}

	var (
		out  []candidate
		gaps []string
	)
	for i := range in.Items {
		item := &in.Items[i]
		subject := item.Artifact.Subject
		if item.Artifact.Context.Blocked || item.Artifact.Context.Allowance == "excluded" {
			// A recorded restriction withholds the work and keeps the
			// record. §4.8 and §5.2 both refuse to delete a
			// restricted subject, so this is a reported gap rather
			// than a disappearance — and rescinding the policy makes
			// the same subject drawable again with nothing to undo.
			gaps = append(gaps, fmt.Sprintf("%s %s: recorded policy withholds subject-specific work (%s)",
				subject.Kind, subject.ID, item.Artifact.Context.Allowance))
			continue
		}
		if item.Assessments >= policy.MaxItemReviews {
			gaps = append(gaps, fmt.Sprintf("%s %s: per-revision cap of %d reviews reached",
				subject.Kind, subject.ID, policy.MaxItemReviews))
			continue
		}
		for _, coverage := range item.Roles {
			key := roleKey{subject: subject, role: coverage.Role}
			// revisit marks a candidate that is not an outstanding
			// obligation: reception on a well-reviewed OPEN idea,
			// past its cooldown. SPEC §5.8 keeps those sampleable
			// through the small positive exploration share, so paid
			// coverage and weighted backlog work can be completely
			// empty while the exploration pool is not. It is bounded
			// by exactly the same things everything else is — the
			// per-revision cap above, the cooldown below, and the
			// budget — and it is admitted to the exploration lane
			// only, so it never competes with due work.
			revisit := false
			switch coverage.State {
			case CoverageUnreviewed, CoverageDue:
			case CoverageReviewed:
				if coverage.Role != RoleReception ||
					decided(item.Artifact.ReviewStatus) {
					continue
				}
				revisit = true
			case CoverageUnsupported:
				gaps = append(gaps, fmt.Sprintf("%s %s role %s: %s",
					subject.Kind, subject.ID, coverage.Role, coverage.Reason))
				continue
			default:
				continue
			}
			if !revisit && !item.Required[coverage.Role] {
				// Supported but not activated. It is a visible
				// gap on the page and not work: running every
				// role on every artifact is the mandatory
				// deep-review policy this product does not have.
				continue
			}
			if active[key] > 0 {
				gaps = append(gaps, fmt.Sprintf("%s %s role %s: already claimed by a live worker",
					subject.Kind, subject.ID, coverage.Role))
				continue
			}
			if setbacks[key] >= maxSkipAttempts {
				gaps = append(gaps, fmt.Sprintf(
					"%s %s role %s: %d skips or failures; bounded attention spent and "+
						"reported as a gap rather than a negative vote",
					subject.Kind, subject.ID, coverage.Role, setbacks[key]))
				continue
			}
			if reason, resting := cooling(item, coverage, policy, in.Now); resting {
				gaps = append(gaps, fmt.Sprintf("%s %s role %s: %s",
					subject.Kind, subject.ID, coverage.Role, reason))
				continue
			}
			due := coverage.LastReviewed
			if due.IsZero() {
				due = item.Artifact.CreatedAt
			}
			out = append(out, candidate{
				Item:      item,
				Role:      coverage.Role,
				State:     coverage.State,
				DueAt:     due,
				Weight:    weigh(item, coverage, policy, in.Now),
				Ordinal:   ordinals[key] + 1,
				Initial:   coverage.Reviews == 0,
				Untouched: item.Assessments == 0,
				Revisit:   revisit,
			})
		}
	}
	sortCandidates(out)
	sort.Strings(gaps)
	gaps = slices.Compact(gaps)
	return out, gaps
}

// cooling reports whether a settled opinion is still resting.
//
// Two escapes, and both are deliberate. A material change — a superseded
// revision, or recorded context that moved — clears the cooldown, because §E4
// requires attention to be restored on material changes and a cooldown that
// survived one would freeze an item's opinion against a situation that no longer
// holds. And a role with no reviews at all never cools: there is no opinion to
// have settled.
func cooling(item *projected, coverage RoleCoverage, policy Policy, now time.Time) (string, bool) {
	if coverage.Reviews == 0 || coverage.LastReviewed.IsZero() {
		return "", false
	}
	if item.StaleRevision || item.ContextChanged || item.Reopened {
		return "", false
	}
	elapsed := now.Sub(coverage.LastReviewed)
	cooldown := time.Duration(policy.CooldownSeconds) * time.Second
	if elapsed >= cooldown {
		return "", false
	}
	if coverage.Role == RoleReception && coverage.Reviews >= policy.InitialReviews {
		reception := item.Reception
		if reception.Reviews > 0 {
			margin := math.Abs(float64(reception.Support-reception.Oppose)) /
				float64(reception.Reviews)
			if margin >= settledMargin {
				return fmt.Sprintf(
					"reception is settled at %.0f%% one-sided; resting for another %s",
					margin*100, (cooldown - elapsed).Truncate(time.Minute)), true
			}
		}
	}
	if coverage.State == CoverageDue {
		return fmt.Sprintf("reassessed %s ago; resting for another %s",
			elapsed.Truncate(time.Minute), (cooldown - elapsed).Truncate(time.Minute)), true
	}
	return "", false
}

// weigh computes a candidate's relative preference.
//
// Every factor is a multiplier and every multiplier is positive, so no candidate
// can be weighted to zero and silently excluded — an exclusion is a filter with
// a stated reason, never a weight that rounds away. The factors:
//
//   - Lightly reviewed work is favoured, which is §E4's first weighting rule.
//   - Settled reception is damped at both extremes, so the sampler stops paying
//     to confirm what it already recorded without ever refusing to.
//   - A material change doubles the weight, restoring attention.
//   - Recorded current work and priority raise it, because review of work the
//     operator is actually doing is worth more than review in the abstract.
//   - Age raises it up to the overdue threshold, after which more age adds
//     nothing: past due is past due, and the reserved coverage lane is what
//     actually clears it.
func weigh(item *projected, coverage RoleCoverage, policy Policy, now time.Time) float64 {
	weight := 1.0 / (1.0 + float64(coverage.Reviews))

	if coverage.Role == RoleReception && item.Reception.Reviews > 0 {
		margin := math.Abs(float64(item.Reception.Support-item.Reception.Oppose)) /
			float64(item.Reception.Reviews)
		weight *= 1.0 - 0.75*margin
	}
	if item.StaleRevision || item.ContextChanged || item.Reopened {
		weight *= materialChangeWeight
	}
	ctx := item.Artifact.Context
	if ctx.CurrentWork {
		weight *= 1.5
	}
	if ctx.Priority > 0 {
		weight *= 1.0 + 0.25*float64(ctx.Priority)
	}
	reference := coverage.LastReviewed
	if reference.IsZero() {
		reference = item.Artifact.CreatedAt
	}
	overdue := time.Duration(policy.OverdueSeconds) * time.Second
	if age := now.Sub(reference); age > 0 && overdue > 0 {
		weight *= 1.0 + math.Min(float64(age)/float64(overdue), 1.0)
	}
	// A weight can be damped close to zero by a settled margin; the floor
	// keeps it drawable, because a settled opinion is resting rather than
	// permanently closed.
	return math.Max(weight, 1e-6)
}

// sample chooses a lane from the seeded stream and then a candidate within it.
//
// The lane comes from one uniform draw against the policy's cumulative shares,
// which is what makes the reservations actual reservations: over many cycles the
// coverage lane receives its share whatever the weights say, and the exploration
// share is spent on a uniform pick that no weight can capture.
//
// Lanes fall through in a fixed order when the chosen one is empty. Falling
// through rather than refusing is correct — an empty coverage lane means every
// initial review is done, which is a reason to spend the share on weighted work
// and not a reason to idle — and the order is fixed so a replay lands the same
// way.
func sample(candidates []candidate, policy Policy, seed uint64) (string, *candidate) {
	rng := rand.New(rand.NewPCG(seed, seed^seedStream))
	roll := rng.Float64()

	coverageEdge := policy.CoverageShare
	discoveryEdge := coverageEdge + policy.DiscoveryShare
	explorationEdge := discoveryEdge + policy.ExplorationShare

	order := []string{LaneWeighted, LaneCoverage, LaneDiscovery, LaneExploration}
	switch {
	case roll < coverageEdge:
		order = []string{LaneCoverage, LaneDiscovery, LaneWeighted, LaneExploration}
	case roll < discoveryEdge:
		order = []string{LaneDiscovery, LaneCoverage, LaneWeighted, LaneExploration}
	case roll < explorationEdge:
		order = []string{LaneExploration, LaneWeighted, LaneCoverage, LaneDiscovery}
	}
	for _, lane := range order {
		if chosen := pick(candidates, lane, rng); chosen != nil {
			return lane, chosen
		}
	}
	return "", nil
}

// seedStream separates the two halves of the PCG state from one seed.
//
// A single uint64 seed has to produce two independent 64-bit words, and seeding
// both halves identically is a documented way to get a degenerate stream. The
// constant is arbitrary and fixed: what matters is that it never changes, since
// changing it would make every previously recorded draw unreplayable.
const seedStream = 0x9e3779b97f4a7c15

// pick selects one candidate from a lane.
//
// Coverage and discovery are deterministic: they draw the oldest-due and the
// oldest untouched respectively, because a reservation whose target was chosen
// at random would not reliably clear the backlog it exists to clear. Exploration
// is uniform. Weighted is a cumulative-weight sample, which is the only lane
// where a higher weight means a higher probability rather than a guarantee.
func pick(candidates []candidate, lane string, rng *rand.Rand) *candidate {
	switch lane {
	case LaneCoverage:
		var best *candidate
		for i := range candidates {
			if candidates[i].Revisit || !candidates[i].Initial {
				continue
			}
			if best == nil || candidates[i].DueAt.Before(best.DueAt) {
				best = &candidates[i]
			}
		}
		return best
	case LaneDiscovery:
		var best *candidate
		for i := range candidates {
			if candidates[i].Revisit || !candidates[i].Untouched {
				continue
			}
			if best == nil || candidates[i].DueAt.Before(best.DueAt) {
				best = &candidates[i]
			}
		}
		return best
	case LaneExploration:
		// The only lane that draws a revisit, and it draws uniformly
		// over everything eligible: an exploration share spent by weight
		// would be the weighted lane under another name, and the point of
		// the share is that something nothing recommends still gets seen.
		if len(candidates) == 0 {
			return nil
		}
		return &candidates[rng.IntN(len(candidates))]
	case LaneWeighted:
		// Outstanding obligations only. A revisit is not backlog, so
		// admitting it here would let well-reviewed open ideas compete
		// with due work for the paid share.
		total := 0.0
		for i := range candidates {
			if candidates[i].Revisit {
				continue
			}
			total += candidates[i].Weight
		}
		if total <= 0 {
			return nil
		}
		target := rng.Float64() * total
		for i := range candidates {
			if candidates[i].Revisit {
				continue
			}
			target -= candidates[i].Weight
			if target <= 0 {
				return &candidates[i]
			}
		}
		for i := len(candidates) - 1; i >= 0; i-- {
			if !candidates[i].Revisit {
				return &candidates[i]
			}
		}
		return nil
	}
	return nil
}

// sortCandidates puts the eligible set in a total order.
//
// It is not a preference order; it is the order the samplers walk, and it has to
// be total so that one seed produces one draw on every instance. Oldest due
// first, then kind, id and role: the same relation the reserved lane reads, so a
// tie in the weighted lane breaks toward the older work rather than toward
// whichever row the projection happened to return first.
func sortCandidates(candidates []candidate) {
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if !a.DueAt.Equal(b.DueAt) {
			return a.DueAt.Before(b.DueAt)
		}
		if a.Item.Artifact.Subject.Kind != b.Item.Artifact.Subject.Kind {
			return a.Item.Artifact.Subject.Kind < b.Item.Artifact.Subject.Kind
		}
		if a.Item.Artifact.Subject.ID != b.Item.Artifact.Subject.ID {
			return a.Item.Artifact.Subject.ID < b.Item.Artifact.Subject.ID
		}
		return a.Role < b.Role
	})
}

// assignmentID is the deployment-wide identity of one review.
//
// It is a digest of the exact revision, the role, the context version, the
// policy version and the sample ordinal — and deliberately not of the run that
// drew it. That is what makes cross-worker fencing work at all: two workers that
// independently decide the same review is next derive the same id, contend for
// one claim, and exactly one wins. An id containing the run would give each
// worker its own assignment for the same review, and the shared allowance would
// be spent twice on one vote.
//
// The ordinal is what keeps a legitimate later review from colliding with an
// earlier one. §E1 permits independently assigned later assessments, even from
// the same model, so the second reception review of one revision under one
// policy is ordinal two and a different assignment — while a retry of the first
// is the same id and therefore the same claim.
func assignmentID(subject Subject, role, contextVersion, policyVersion string, ordinal int) string {
	return "eval-a-" + digestOf(
		subject.Kind, subject.ID, role, contextVersion, policyVersion, strconv.Itoa(ordinal))
}

// subjectNames are the recorded names the drawn subject answers to.
//
// They travel on the assignment because the conductor consults §4.8's
// expenditure policy before it launches a worker, and the alternative — asking
// the service for the review context in order to decide whether to ask for it —
// would record an exposure for a review that is then refused.
//
// They are read off the same structured fields Source resolved the context with
// and never off prose: a name matched against a model-authored title would be
// this package inventing the alias mapping the ledger exists to own.
func subjectNames(item projected) []string {
	return append([]string{}, item.Names...)
}
