package evaluation

import (
	"fmt"
	"slices"
	"strings"
)

// This file is the evaluation policy: the versioned settings that bound
// authorized review work, and the vocabularies the read surfaces validate
// against.
//
// What is deliberately *not* here is the role applicability decision. Roles,
// ValidRole, RolesForKind and RoleApplies belong to the canonical model
// (model.go), because a role a store will refuse to accept a record for is not
// a view concern. This file explains those refusals — RoleUnsupportedReason —
// and never decides them, so there is exactly one table saying which roles a
// kind can receive.
//
// Two rules from docs/evaluation-lifecycle.md §E4 shape the numbers below.
//
// Nothing here starts compute. DefaultPolicy is disabled, and saving a policy
// is a record rather than a scheduler command; SPEC §5.8 is explicit that no
// budget is increased and no compute launched merely because a backlog exists.
//
// The defaults are conservative but not zero. A share of zero for exploration
// or protected discovery is refused by ValidatePolicy rather than accepted,
// because §E4 requires a *positive* exploration share and a protected
// discovery share: a policy that reserved neither would be a pure backlog
// drain that can never notice anything it is not already looking at, and an
// operator who wants that has asked for a different system.

// PolicyVersion is the version stamped on DefaultPolicy.
//
// It is a string rather than an integer because a fleet's policy versions are
// compared for equality and printed, never ordered arithmetically: two
// deployments may legitimately run "1" and "2025-09-operator-tuned" at once,
// and a numeric type would invite code that assumed the larger one wins. Which
// policy is effective is decided by recency of the operator record that carries
// it (Service.Policy), not by parsing this.
const PolicyVersion = "1"

// The default policy's settings. They are named constants rather than literals
// inside DefaultPolicy so that a test asserting a bound and the policy stating
// it cannot disagree, and so each number's reason has somewhere to live.
const (
	// defaultCadenceSeconds is how often a coverage check runs. Hourly is
	// chosen against the thing being inventoried: analysis records arrive in
	// runs that take minutes, and a check is a scan of a projection rather
	// than any model call, so an hour keeps "never reviewed" honest within
	// one working session at no compute cost.
	defaultCadenceSeconds = 3600
	// defaultOverdueSeconds is how long an eligible artifact may go without
	// its initial review before it is overdue. Fourteen days is the span
	// over which the recorded context behind a candidate is still
	// recognisable to the operator who recorded it; past that the review is
	// archaeology, so it is reported as overdue rather than silently
	// pending.
	defaultOverdueSeconds = 14 * 24 * 3600
	// defaultInitialReviews is how many independent assessments a role needs
	// before coverage stops calling the revision never-reviewed. Two, not
	// one: a single assessment cannot show disagreement, and disagreement is
	// the signal SPEC §4.12 says reception actually carries.
	defaultInitialReviews = 2
	// defaultCooldownSeconds is how long a settled opinion rests. Seven days
	// against a fourteen-day overdue bound means a stable-reception revision
	// is asked at most twice per overdue window, which is what "reduce
	// repetitive voting at both stable extremes" costs when expressed as a
	// number.
	defaultCooldownSeconds = 7 * 24 * 3600
	// defaultMaxItemReviews bounds total assessments per revision across all
	// roles, which is what stops persistent disagreement from becoming an
	// obligation to vote until consensus. Six is two initial reviews plus
	// one bounded challenge and comparison pair per side.
	defaultMaxItemReviews = 6
	// defaultLeaseSeconds is how long a claim survives a worker that stops
	// answering. Fifteen minutes is longer than any single review invocation
	// - the four reviews measured on 2026-09-12 took 386s, 461s, 556s and
	// 630s end to end - and short enough that a crashed worker's subject is
	// drawable again within one cadence period.
	defaultLeaseSeconds = 900
	// defaultBatchSize bounds one cycle's assignments, so a cadence tick can
	// never turn the whole eligible set into concurrent work.
	defaultBatchSize = 4
	// leaseSecondsPerSubject and leaseFloorSeconds are the floor a lease has
	// to clear for the batch it is granted against. Both are measured rather
	// than chosen.
	//
	// This deployment lost four review runs on 2026-09-12 under a policy of
	// lease 240s and batch 24. Measured from the live store - each
	// evaluation_claim's created_at against the evaluation_settlement that
	// recorded its failure - they ran 386s, 461s, 556s and 630s, a mean of
	// 508s, which is 16s to 26s of wall clock per subject in the batch and a
	// mean of 21s. Every one of them died at the same place: the claim
	// expired during the corpus scan and scope fixing that precede the
	// worker, and the review context was then refused as a lapsed lease. So
	// twenty seconds per subject is that measurement rounded down to a number
	// an operator can hold in their head, and five minutes is the floor under
	// any batch size at all, because a batch of one still has to cover that
	// same preparation.
	//
	// The floor stands beside renewal rather than instead of it. What carries
	// a long review past its lease is Service.RenewClaim, ticked at a third
	// of the lease while the review runs, in both modes since migration
	// 0014; what the floor refuses is the policy that needs those renewals
	// to have worked at all. A renewal is one more write that can be lost to
	// a busy database or a worker that stalls, and a lease shorter than the
	// preparation it has to cover would hand out claims that depend on the
	// first tick landing before the grant lapses.
	leaseSecondsPerSubject = 20
	leaseFloorSeconds      = 300
)

// leaseFloor is the shortest lease a policy may grant for one batch size.
func leaseFloor(batchSize int) int {
	if floor := leaseSecondsPerSubject * batchSize; floor > leaseFloorSeconds {
		return floor
	}
	return leaseFloorSeconds
}

// The default policy's shares and ceilings.
const (
	// DefaultCoverageShare reserves half of one cycle for the oldest-due
	// initial reviews. Half rather than all: §E4 reserves attention for
	// coverage *and* allocates the remainder by weight, and a coverage share
	// of one would make the weighted policy, the exploration share and the
	// bounded challenge work unreachable code.
	DefaultCoverageShare = 0.5
	// DefaultExplorationShare is the random share that is not justified by
	// weight. It is what lets a revision nothing recommends still be seen.
	DefaultExplorationShare = 0.15
	// DefaultDiscoveryShare is SPEC §5.8's protected discovery share:
	// attention reserved for artifacts that have never been evaluated in any
	// role, so a deployment that is busy arguing about its favourite
	// proposal still notices the observation nobody has read.
	DefaultDiscoveryShare = 0.10
	// DefaultPerCycleCost and DefaultDailyCost are the authorized spend, in
	// the same cost unit internal/conductor's budget accounting uses. They
	// are deliberately small: an operator raising them is an explicit act,
	// and a default that could spend a day's allowance in one cycle would
	// make the per-cycle bound decorative.
	DefaultPerCycleCost = 0.25
	DefaultDailyCost    = 2.0
)

// DefaultPolicy is the versioned policy a deployment runs before an operator
// configures one.
//
// Enabled is false. That is SPEC §14's activation gate expressed as a value
// rather than as a comment: every other setting here is a real, validated
// bound, so turning evaluation on is one recorded operator decision and not a
// migration — and until that decision exists, Draw refuses with ErrNoWork and
// says which policy refused.
func DefaultPolicy() Policy {
	return Policy{
		Version:          PolicyVersion,
		Enabled:          false,
		CadenceSeconds:   defaultCadenceSeconds,
		OverdueSeconds:   defaultOverdueSeconds,
		InitialReviews:   defaultInitialReviews,
		CooldownSeconds:  defaultCooldownSeconds,
		CoverageShare:    DefaultCoverageShare,
		ExplorationShare: DefaultExplorationShare,
		DiscoveryShare:   DefaultDiscoveryShare,
		MaxItemReviews:   defaultMaxItemReviews,
		PerCycleCost:     DefaultPerCycleCost,
		DailyCost:        DefaultDailyCost,
		LeaseSeconds:     defaultLeaseSeconds,
		BatchSize:        defaultBatchSize,
	}
}

// ValidatePolicy refuses a policy that cannot be honoured.
//
// Every refusal below is a setting that would make some other part of the
// system lie rather than merely behave oddly, which is the line for what
// belongs in a validator instead of in documentation:
//
//   - An unversioned policy cannot be replayed against, so a draw taken under
//     it could never be re-derived.
//   - A zero exploration or discovery share removes §E4's protected shares.
//   - Shares summing above one would over-commit a cycle, so one lane's
//     reservation would silently come out of another's.
//   - MaxItemReviews below InitialReviews makes a role permanently
//     under-reviewed while the cap reports the item as finished.
//   - A daily ceiling below one cycle's makes the per-cycle bound decorative.
//   - A lease too short for the batch it is granted against hands out claims
//     that expire before the work they authorize can start, which is the
//     failure four of this deployment's review runs actually had: the claim
//     lapsed during the preparation that precedes the worker, and the review
//     context was then refused as a conflicting claim. See leaseFloor.
func ValidatePolicy(p Policy) error {
	if strings.TrimSpace(p.Version) == "" {
		return fmt.Errorf("%w: policy has no version", ErrInvalid)
	}
	if p.Version != strings.TrimSpace(p.Version) {
		return fmt.Errorf("%w: policy version %q has surrounding whitespace", ErrInvalid, p.Version)
	}
	if p.CadenceSeconds <= 0 {
		return fmt.Errorf("%w: cadence %ds must be positive", ErrInvalid, p.CadenceSeconds)
	}
	if p.OverdueSeconds <= 0 {
		return fmt.Errorf("%w: overdue threshold %ds must be positive", ErrInvalid, p.OverdueSeconds)
	}
	if p.InitialReviews < 1 {
		return fmt.Errorf("%w: initial reviews %d must be at least one", ErrInvalid, p.InitialReviews)
	}
	if p.CooldownSeconds < 0 {
		return fmt.Errorf("%w: cooldown %ds is negative", ErrInvalid, p.CooldownSeconds)
	}
	for _, share := range []struct {
		name  string
		value float64
		// protected marks the shares §E4 protects, where zero is a
		// refusal rather than a setting.
		protected bool
	}{
		{"coverage share", p.CoverageShare, false},
		{"exploration share", p.ExplorationShare, true},
		{"discovery share", p.DiscoveryShare, true},
	} {
		if share.value < 0 || share.value > 1 {
			return fmt.Errorf("%w: %s %v is outside [0,1]", ErrInvalid, share.name, share.value)
		}
		if share.protected && share.value <= 0 {
			return fmt.Errorf("%w: %s must stay positive; a zero share removes a protected allocation",
				ErrInvalid, share.name)
		}
	}
	if total := p.CoverageShare + p.ExplorationShare + p.DiscoveryShare; total > 1 {
		return fmt.Errorf("%w: reserved shares total %v and over-commit one cycle", ErrInvalid, total)
	}
	if p.MaxItemReviews < p.InitialReviews {
		return fmt.Errorf("%w: max item reviews %d is below initial reviews %d",
			ErrInvalid, p.MaxItemReviews, p.InitialReviews)
	}
	if p.PerCycleCost <= 0 {
		return fmt.Errorf("%w: per-cycle cost %v must be positive", ErrInvalid, p.PerCycleCost)
	}
	if p.DailyCost < p.PerCycleCost {
		return fmt.Errorf("%w: daily cost %v is below the per-cycle cost %v",
			ErrInvalid, p.DailyCost, p.PerCycleCost)
	}
	if p.LeaseSeconds <= 0 {
		return fmt.Errorf("%w: lease %ds must be positive", ErrInvalid, p.LeaseSeconds)
	}
	if p.BatchSize < 1 {
		return fmt.Errorf("%w: batch size %d must be at least one", ErrInvalid, p.BatchSize)
	}
	if floor := leaseFloor(p.BatchSize); p.LeaseSeconds < floor {
		return fmt.Errorf("%w: lease %ds cannot cover a batch of %d: a lease must allow at least "+
			"%ds per assignment and never less than %ds, so this batch needs %ds",
			ErrInvalid, p.LeaseSeconds, p.BatchSize, leaseSecondsPerSubject, leaseFloorSeconds, floor)
	}
	return nil
}

// Invalidation reports what installing one policy over another makes stale.
//
// §E4 requires an implementation to define what a policy change invalidates and
// what is retained for replay, and this is that definition. It is deliberately
// narrow in one direction and broad in another.
//
// Nothing in it invalidates a *record*. An assessment was made under a named
// policy version against a named input digest, and a later policy cannot
// un-make it; that is why Assignment carries Seed, InputDigest and
// PolicyVersion, and why a historical draw stays replayable after this returns
// Selection true. What a change does invalidate is the derived work that used
// the old settings as an input: a ranking computed with a different cooldown, a
// coverage inventory computed with a different overdue bound, and a cycle's
// remaining reservations.
//
// In-flight assignments are retained rather than revoked. A worker holding a
// valid claim issued under the superseded version completes it and its record
// keeps that version; the new version governs the next draw. Revoking instead
// would throw away work already paid for and would make a policy edit a way to
// lose a worker's output.
type Invalidation struct {
	// Selection is true when the next draw must be recomputed: shares,
	// weights, cooldowns, caps, batch or budget moved.
	Selection bool
	// Coverage is true when the coverage inventory's own arithmetic moved:
	// what counts as reviewed, or as overdue.
	Coverage bool
	// Ranking is true when the operator's ordering must be recomputed,
	// which follows from any coverage change and from the review targets the
	// ranking reads.
	Ranking bool
	// Reasons name the settings that moved, in a stable order, so a status
	// line can say why a projection is being rebuilt.
	Reasons []string
}

// Changed reports whether anything was invalidated at all.
func (in Invalidation) Changed() bool { return in.Selection || in.Coverage || in.Ranking }

// Invalidates reports what replacing prev with p makes stale.
func (p Policy) Invalidates(prev Policy) Invalidation {
	var out Invalidation
	note := func(name string, selection, coverage, ranking bool) {
		out.Reasons = append(out.Reasons, name)
		out.Selection = out.Selection || selection
		out.Coverage = out.Coverage || coverage
		out.Ranking = out.Ranking || ranking
	}
	if p.Version != prev.Version {
		note("version", true, true, true)
	}
	if p.Enabled != prev.Enabled {
		note("enabled", true, false, false)
	}
	if p.CadenceSeconds != prev.CadenceSeconds {
		note("cadence", true, true, false)
	}
	if p.OverdueSeconds != prev.OverdueSeconds {
		note("overdue threshold", true, true, true)
	}
	if p.InitialReviews != prev.InitialReviews {
		note("initial reviews", true, true, true)
	}
	if p.CooldownSeconds != prev.CooldownSeconds {
		note("cooldown", true, false, true)
	}
	if p.CoverageShare != prev.CoverageShare {
		note("coverage share", true, false, false)
	}
	if p.ExplorationShare != prev.ExplorationShare {
		note("exploration share", true, false, false)
	}
	if p.DiscoveryShare != prev.DiscoveryShare {
		note("discovery share", true, false, false)
	}
	if p.MaxItemReviews != prev.MaxItemReviews {
		note("max item reviews", true, true, true)
	}
	if p.PerCycleCost != prev.PerCycleCost || p.DailyCost != prev.DailyCost {
		note("budget", true, false, false)
	}
	if p.LeaseSeconds != prev.LeaseSeconds {
		note("lease", true, false, false)
	}
	if p.BatchSize != prev.BatchSize {
		note("batch size", true, false, false)
	}
	return out
}

// The subject kinds evaluation covers.
//
// The first four are internal/frontier's records and are spelled the same,
// because they are the same records: a Subject.Kind is that package's
// EntityType value, so a subject identifies a row rather than a translation of
// one. SubjectKindEvaluation is not a frontier record — it is one of this
// package's own published assessments, admitted as a subject so that §E1's
// bounded meta-review has something to point at.
const (
	SubjectKindHypothesis  = "hypothesis"
	SubjectKindObservation = "observation"
	SubjectKindFinding     = "finding"
	SubjectKindProposal    = "proposal"
	SubjectKindEvaluation  = "evaluation"
)

// Kinds lists the covered subject kinds in a stable order.
//
// Coverage spans all of them, including observations and findings, because
// docs/evaluation-lifecycle.md §1 is explicit that coverage is not the
// hypothesis-and-proposal subset the baseline triage path happened to reach: an
// un-enrolled observation must not disappear from the inventory, and its
// reception, evidence and relevance roles are implemented rather than reported
// as unsupported.
func Kinds() []string {
	return []string{
		SubjectKindHypothesis, SubjectKindObservation,
		SubjectKindFinding, SubjectKindProposal, SubjectKindEvaluation,
	}
}

// KindApplicabilityPolicy names the versioned decision about which produced
// record kinds evaluation reviews at all.
//
// It is a second named policy rather than a paragraph in RoleApplicabilityPolicy
// because the two answer different questions and an operator acts on them
// differently. Role applicability says a reviewable artifact cannot receive one
// particular role; this says a whole record kind is not review work. Conflating
// them would make "a finding has no outcome to verify" and "a run receipt is
// machine bookkeeping" the same sentence.
const KindApplicabilityPolicy = "kind-applicability-1"

// nonReviewableKinds are the record kinds a Babel deployment produces that
// evaluation does not review, each with the reason it does not.
//
// The inventory enumerates them rather than omitting them, and that is the
// whole point of the table existing. docs/evaluation-lifecycle.md §E1 requires
// a `not applicable` to be an intentional named policy with a reason and
// forbids a missing evaluator from reading as reviewed; a coverage inventory
// that simply did not mention receipts would satisfy neither — an operator
// could not tell whether receipts were reviewed, exempt, or forgotten.
//
// None of these is ever counted as reviewed and none blocks an evaluator. They
// are counted under Coverage.NotApplicable, beside the named reason.
var nonReviewableKinds = map[string]string{
	"complaint": "an operator's recorded complaint is an input to analysis, not a model-produced " +
		"claim; reviewing it would have Babel vote on what its operator told it",
	"context": "attributed operator guidance is a decision, not a claim offered for review",
	"receipt": "a run receipt is mechanical accounting of an invocation and asserts nothing " +
		"about the corpus",
	"preparation": "a preparation records what a run was given, not a claim it made",
	"link": "a typed relationship between two records carries no claim of its own; the records " +
		"it joins are each reviewed on their own terms",
	"disposition": "an operator's review decision is the authority evaluation reports to; " +
		"reception cannot sit in judgement of the disposition vocabulary it feeds",
	"review-answer": "a recorded disposition or refinement request is the operator's own answer, " +
		"and historical triage advice stays advice rather than becoming reception",
	"edge": "a citation edge is derived graph structure with no assertion to assess",
}

// NonReviewableKinds lists the produced record kinds evaluation does not
// review, in a stable order.
func NonReviewableKinds() []string {
	out := make([]string, 0, len(nonReviewableKinds))
	for kind := range nonReviewableKinds {
		out = append(out, kind)
	}
	slices.Sort(out)
	return out
}

// KindUnreviewableReason explains why a produced kind is not review work, and
// returns empty for a kind evaluation does review.
//
// An unrecognised kind gets the unsupported sentence rather than a
// not-applicable one. The difference is load-bearing: not-applicable is a
// decision an operator can read and disagree with, while unsupported is a gap
// in this build, and §E1 requires the second to stay visible as a gap instead
// of resolving itself into the first.
func KindUnreviewableReason(kind string) string {
	if ValidKind(kind) {
		return ""
	}
	if reason, ok := nonReviewableKinds[kind]; ok {
		return fmt.Sprintf("%s: %s", KindApplicabilityPolicy, reason)
	}
	return fmt.Sprintf("%s: record kind %q has no registered evaluator and no recorded "+
		"exemption, so its coverage is unsupported rather than complete",
		KindApplicabilityPolicy, kind)
}

// ValidKind reports whether kind is a subject kind evaluation covers.
func ValidKind(kind string) bool { return slices.Contains(Kinds(), kind) }

// BlindedRoles are the roles served without prior evaluations.
//
// The membership rule is "could seeing the tally change this answer", not "is
// this the first review". Reception, evidence, outcome and relevance are all
// answers about the artifact, and §E3 requires the served content and the
// brokered reads to withhold existing tallies and earlier evaluations for them.
// Challenge and comparison are answers *about* the disagreement, so withholding
// it would make the role unperformable — and their reveal is attributed, which
// is what keeps it a disclosure rather than a leak.
func BlindedRoles() []string {
	return []string{RoleReception, RoleEvidence, RoleOutcome, RoleRelevance}
}

// Blinded reports whether a role is served without prior evaluations.
func Blinded(role string) bool { return slices.Contains(BlindedRoles(), role) }

// RoleApplicabilityPolicy names the versioned applicability decision this build
// ships, so a recorded `not-applicable` coverage state points at a policy an
// operator can read rather than at an implementation detail.
//
// §E1 requires `not applicable` to carry an intentional named policy and a
// reason. This is the name; RoleUnsupportedReason is the reason.
const RoleApplicabilityPolicy = "role-applicability-1"

// roleRefusals explains the model's applicability refusals in words.
//
// It decides nothing: RoleUnsupportedReason consults RoleApplies first and only
// reaches this table for a pair the model already refused. Keeping the sentences
// out of the decision is what stops this from becoming a second applicability
// table that could disagree with the first — a missing entry here degrades to a
// generic sentence, never to a different verdict.
var roleRefusals = map[string]map[string]string{
	SubjectKindHypothesis: {
		RoleOutcome: "a hypothesis states what is the case and names no change to implement, " +
			"so there is no promised outcome to verify against it",
	},
	SubjectKindObservation: {
		RoleChallenge: "an observation is the evidence a finding consolidates; " +
			"its disagreement is argued on the finding that rests on it",
		RoleComparison: "observations are not alternative remedies for one problem",
		RoleOutcome: "an observation reports what was seen and promises no outcome, " +
			"so it cannot be verified as implemented",
	},
	SubjectKindFinding: {
		RoleComparison: "a finding consolidates evidence rather than proposing a remedy; " +
			"the remedies that answer it are compared as proposals",
		RoleOutcome: "a finding explains a pattern and names no change to implement",
	},
	SubjectKindEvaluation: {
		RoleEvidence: "an assessment's evidence is checked by challenging the assessment, " +
			"not by a second evidence pass on it",
		RoleComparison: "assessments are not alternative remedies for one problem",
		RoleOutcome:    "an assessment promises no outcome to verify",
		RoleRelevance:  "relevance is a property of the artifact, not of an opinion about it",
	},
}

// RoleUnsupportedReason explains why a subject kind cannot receive a role, and
// returns empty when it can.
//
// Three different sentences, because the three states call for three different
// operator responses and a single "unavailable" would send them all to the
// wrong place:
//
//   - An unrecognised kind is an unsupported gap. No evaluator is registered, so
//     coverage stays visibly incomplete rather than becoming complete by
//     omission — §E1's rule that a missing adapter is a gap and not a review.
//   - An unrecognised role is a caller error stated as such.
//   - A recognised pair the model refuses is a named policy decision with a
//     reason an operator can argue with.
func RoleUnsupportedReason(subjectKind, role string) string {
	if RoleApplies(subjectKind, role) {
		return ""
	}
	if !ValidRole(role) {
		return fmt.Sprintf("%s: %q is not a review role this build implements",
			RoleApplicabilityPolicy, role)
	}
	if !ValidKind(subjectKind) {
		return fmt.Sprintf("%s: no evaluator is registered for subject kind %q, so its review "+
			"coverage is unsupported rather than complete", RoleApplicabilityPolicy, subjectKind)
	}
	if reason, ok := roleRefusals[subjectKind][role]; ok {
		return fmt.Sprintf("%s: %s", RoleApplicabilityPolicy, reason)
	}
	return fmt.Sprintf("%s: role %q is not applicable to a %s and no reason is recorded, "+
		"which is a registry gap rather than a decision", RoleApplicabilityPolicy, role, subjectKind)
}

// MetaReviewDepth is how many levels of meta-review the registry admits.
//
// One. An assessment of an artifact may itself be challenged; the challenge of
// that challenge may not. The bound is read off the parent subject chain rather
// than off a role name, a recipe name or a stored counter, because every level
// of nesting shares one subject kind — "evaluation" — so a name-based test
// would admit level three while claiming to stop at level one.
const MetaReviewDepth = 1

// MetaReviewSubject reports whether an evaluation record may itself be
// reviewed, and why not when it may not.
//
// Two structural refusals, and neither consults anything mutable:
//
// A record that is not an assessment carries no judgement to review. That
// excludes the operator's criteria, feedback, policy and reconsider decisions —
// reviewing those would have Babel vote on its operator — and it excludes the
// assignment, attempt and checkpoint journal, which is bookkeeping. This is
// also what stops a coverage sweep from feeding itself: a checkpoint record
// published by one sweep is not a subject, so the next sweep does not discover
// an immediately-due artifact that the sweep itself created.
//
// A record whose own Subject.Kind is already "evaluation" *is* a meta-review,
// and reviewing it would be level two. Walking the parent link is the whole
// test: level one's subject is an artifact, level two's subject is level one,
// and comparing the subject kind terminates the recursion at MetaReviewDepth
// however the nested records are named or which recipe produced them.
func MetaReviewSubject(rec Record, role string) (bool, string) {
	if rec.Kind != KindAssessment {
		return false, fmt.Sprintf("%s: record kind %q is not an assessment, so it carries no "+
			"reviewable judgement", RoleApplicabilityPolicy, rec.Kind)
	}
	if reason := RoleUnsupportedReason(SubjectKindEvaluation, role); reason != "" {
		return false, reason
	}
	if rec.Subject.Kind == SubjectKindEvaluation {
		return false, fmt.Sprintf("%s: record %s already reviews an evaluation, and meta-review "+
			"is bounded at depth %d; reviewing it would require reviewing every review",
			RoleApplicabilityPolicy, rec.ID, MetaReviewDepth)
	}
	return true, ""
}
