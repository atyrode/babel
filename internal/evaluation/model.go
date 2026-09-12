// Package evaluation stores Babel's full-lifecycle evaluation records: the
// reception, evidence, comparison, relevance and outcome assessments of
// SPEC.md §4.12, the budgeted assignments of §5.8, and the operator's own
// criteria, scoped feedback and reconsideration decisions (issue #219,
// docs/evaluation-lifecycle.md).
//
// Four properties shape everything in this file.
//
// A record is an immutable, fully attributed publication. Nothing here is
// updated and nothing is deleted: a correction is a descendant record that
// names the statement it supersedes, and the superseded statement stays
// readable. That is the same rule internal/frontier keeps for analysis
// records, for the same reason — a history that can be rewritten is a history
// nobody can audit — and it is why an assessment carries its own provenance
// rather than pointing at a mutable row.
//
// A subject is an exact revision, never a chain. Subject.ID is the identity of
// one immutable record revision, so a vote binds to the wording that was
// actually read. When the chain head moves, the vote does not follow it: a
// reader compares the subject against the artifact's current head and sees
// that the assessment is about an earlier revision, which is the honest
// reading and the only one that keeps §4.12's "votes bind to the exact wording
// read" checkable.
//
// The vocabularies are closed and the checks are role-specific. A bare
// support, opposition or uncertainty vote is a complete reception review; a
// contribution with no vote is equally valid; and an outcome is neither. An
// outcome claim requires admissible evidence, an operator-authored criteria
// version, a named environment and an as-of time, because §4.12 forbids a
// manufactured verified result and docs/evaluation-lifecycle.md §E6 is
// explicit that missing, partial or conflicting evidence is not success.
//
// Operator authority and observation stay separate. An operator authors
// criteria, feedback, reconsideration decisions and policy; a run authors
// assessments and reconsider items. Accepting, rejecting, deferring and
// shelving remain internal/review's, and no observation here can establish
// one: an outcome reports what was seen and rules on nothing.
//
// One operator act does move a review state, and it is the explicit one
// §4.12 names: a reconsideration decision that says reopen. It carries its
// polarity as a field, it is refused where there is no review writer to move,
// and it is staged in the same transaction that records it - so "reopened" and
// the record's own review state cannot disagree. A retain records the decision
// and moves nothing.
package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// RecordSchema is the version stamped on every canonical publication this
// build writes. A reader that meets a newer one refuses it explicitly rather
// than decoding optimistically; see Decode.
const RecordSchema = 1

// Sentinel errors callers are expected to handle rather than merely report.
//
// They are distinguished from each other because the remedies differ: a
// conflict is resolved by re-claiming, a budget refusal by waiting for the
// next allowance, and an unavailable dependency by retrying once it is back.
// Collapsing them would make a stale worker's refused write indistinguishable
// from a malformed one.
// SweepLease bounds how long one process may hold the right to rebuild the
// projection. It expires, so a process killed mid-sweep cannot wedge the
// deployment, and it is short enough that a genuinely stale snapshot is
// rebuilt on the next cadence rather than the one after it.
const SweepLease = 2 * time.Minute

var (
	// ErrInvalid reports input this package refuses to record: an unknown
	// vocabulary value, a missing attribution, an outcome with no admissible
	// evidence.
	ErrInvalid = errors.New("evaluation: invalid input")
	// ErrNotFound reports a record, assignment or referenced authority that
	// does not exist.
	ErrNotFound = errors.New("evaluation: not found")
	// ErrConflict reports a write refused because someone else holds the
	// authority for it: a superseded fence, a takeover, a second active vote
	// on one assignment.
	ErrConflict = errors.New("evaluation: conflicting claim")
	// ErrBudget reports that the authorized allowance is exhausted. It is not
	// a fault: §5.8 never increases a budget because work remains.
	ErrBudget = errors.New("evaluation: authorized allowance is exhausted")
	// ErrNoWork reports that no eligible work remains to hand out. Selection
	// raises it; this package defines it so every caller reads one sentinel.
	ErrNoWork = errors.New("evaluation: no eligible work")
	// ErrUnavailable reports a dependency this package needs and cannot
	// reach: shared coordination in shared mode, or the authority a
	// referenced remote record carries. It is never resolved by guessing.
	ErrUnavailable = errors.New("evaluation: required state is unavailable")
	// ErrOverrun reports that spend was accounted in full and the allowance
	// is now over its ceiling, so nothing further is admitted.
	//
	// It is separate from ErrBudget because the two happen at opposite ends
	// of one unit of work and demand opposite responses. ErrBudget refuses an
	// admission: nothing was spent and nothing is owed. ErrOverrun reports a
	// completion whose cost has already been recorded, so discarding the
	// result would throw away work the allowance has already paid for while
	// leaving the charge in place. A completion that overran is therefore
	// recorded and published; the refusal it implies arrives at the next
	// Claim, which is where admission belongs.
	ErrOverrun = errors.New("evaluation: the authorized allowance is overrun")
)

// The record kinds this package publishes.
//
// The list is closed and every value is a distinct authored act. Six carry
// judgement or authority; three - assignment, attempt and checkpoint - carry
// none at all and exist because a non-producing instance has to be able to
// rebuild coverage from what was published rather than infer exposure from
// the assessments that happened to complete. An instance that saw only
// completed assessments would report a skipped assignment and an assignment
// nobody ever opened as the same thing: nothing.
const (
	// KindAssessment is one run's evaluation of one exact revision: a vote, a
	// contribution, an outcome claim, or a lawful combination.
	KindAssessment = "assessment"
	// KindCriteria is the operator's statement of what would count as the
	// promised outcome. It is operator-authored because §4.12 forbids Babel
	// rewriting its own target and then verifying itself against the
	// replacement.
	KindCriteria = "criteria"
	// KindFeedback is an attributed, scoped operator reason - not-now,
	// wrong-problem, wrong-remedy and their like. It carries no disposition:
	// collecting a reason must not silently create a decision.
	KindFeedback = "feedback"
	// KindReconsider is a run-authored item saying what materially changed
	// about work that was already decided. It never reverses the decision.
	KindReconsider = "reconsider"
	// KindReconsiderDecision is the explicit operator act that reopens - or
	// declines to reopen - what a reconsider item raised.
	KindReconsiderDecision = "reconsider_decision"
	// KindPolicy is one versioned operator configuration of the evaluation
	// policy. Saving it starts no compute.
	KindPolicy = "policy"
	// KindAssignment is the grant of one bounded unit of review attention,
	// with its policy version, seed, captured input digest and fence.
	KindAssignment = "assignment"
	// KindAttempt is one state transition of an assignment: exposed to
	// content, completed, skipped, failed.
	KindAttempt = "attempt"
	// KindCheckpoint is the completion of one coverage sweep. It carries no
	// opinion and no author; see CoverageCheckpoint.
	KindCheckpoint = "checkpoint"
)

// RecordKinds lists the record kinds, in a stable order, so a caller that renders or
// filters them cannot drift from what this package accepts.
func RecordKinds() []string {
	return []string{KindAssessment, KindCriteria, KindFeedback, KindReconsider,
		KindReconsiderDecision, KindPolicy, KindAssignment, KindAttempt, KindCheckpoint}
}

// ValidRecordKind reports whether kind is one this package publishes.
func ValidRecordKind(kind string) bool { return slices.Contains(RecordKinds(), kind) }

// OperatorKinds lists the record kinds an operator may author directly.
//
// The subset is the authority boundary. An assessment or a reconsider item
// arriving through the operator surface would let a person mint what reads as
// a model's observation, and a checkpoint or an assignment arriving there
// would let a person claim a sweep or a grant that never happened.
func OperatorKinds() []string {
	return []string{KindCriteria, KindFeedback, KindReconsiderDecision, KindPolicy}
}

// The reconsideration vocabulary of §4.12: "the operator can reopen or retain
// the decision and record a reason from that item".
//
// The polarity is its own field rather than a reading of the reason, because
// the two acts are opposite and a consumer that had to parse prose would be
// guessing: "reopening; the new benchmark matters" and "not reopening; the
// benchmark is irrelevant" differ in one word, and a record whose meaning
// depends on that word is a record no surface can render honestly.
const (
	// ReconsiderReopen is the operator's explicit act of reopening the
	// decided work the item revisits. It reopens nothing by itself:
	// internal/review owns the disposition, and this is the attributed
	// instruction it acts on.
	ReconsiderReopen = "reopen"
	// ReconsiderRetain keeps the prior decision standing. It is a decision
	// rather than silence, which is why it is recorded: an unanswered
	// reconsider item and an item the operator considered and declined are
	// different facts.
	ReconsiderRetain = "retain"
)

// ReconsiderDecisions lists the reconsideration decisions in a stable order.
func ReconsiderDecisions() []string { return []string{ReconsiderReopen, ReconsiderRetain} }

// The reception vocabulary of §4.12's operator half: what a person says about
// a record when he is reading it, before and separately from any ruling.
//
// It is a polarity field for the same reason ReconsiderDecisions is one. A
// reason is prose and prose cannot be counted, compared or rendered as a
// position; "this is right, but not now" and "this is wrong, and not now"
// differ in one word, and a surface that had to read the word would be
// guessing at the operator's position on every record he ever commented on.
//
// It decides nothing. A stance is carried by a feedback record, which §4.12
// defines as an attributed reason that creates no disposition, so agreeing is
// not accepting and disagreeing is not rejecting - those stay internal/
// review's, behind their own confirmation. That separation is why the words
// differ from an assessment's: a run votes support, oppose or unsure about
// content it was served under a claim, and a person agrees or disagrees with
// something he chose to read. Sharing one vocabulary would invite a surface to
// sum them.
const (
	// StanceAgree is the operator saying the record is right.
	StanceAgree = "agree"
	// StanceDisagree is the operator saying it is not.
	StanceDisagree = "disagree"
	// StanceUnsure is the operator saying he has read it and cannot tell,
	// which is a recorded position rather than the absence of one: an
	// unread record and a record a person could not make up his mind about
	// are different facts about how much attention it has had.
	StanceUnsure = "unsure"
)

// Stances lists the operator reception stances in a stable order.
func Stances() []string { return []string{StanceAgree, StanceDisagree, StanceUnsure} }

// The authors a durable evaluation record can have. There is no third kind:
// every record is either a model invocation Babel launched and receipted, or a
// person.
const (
	ActorRun      = "run"
	ActorOperator = "operator"
)

// The review roles of §4.12. They exist because reception, evidence strength,
// personal relevance and observed outcomes answer different questions, and a
// vote on one of them satisfies no obligation on another.
const (
	// RoleReception is the bare support/oppose/unsure judgement. It is not
	// evidence strength, corroboration, or a probability of correctness.
	RoleReception = "reception"
	// RoleEvidence checks whether the cited evidence supports the claim. A
	// reception vote never meets this obligation.
	RoleEvidence = "evidence"
	// RoleChallenge is the bounded diagnostic argument against a record,
	// used where reception disagreement persists.
	RoleChallenge = "challenge"
	// RoleComparison weighs alternative remedies for one problem together,
	// without merging their records or minting global votes.
	RoleComparison = "comparison"
	// RoleOutcome records implementation and outcome assessments against an
	// operator-authored criteria version.
	RoleOutcome = "outcome"
	// RoleRelevance judges whether a record matters for the recorded current
	// work, pain and Reality context.
	RoleRelevance = "relevance"
)

// Roles lists the review roles in a stable order.
func Roles() []string {
	return []string{RoleReception, RoleEvidence, RoleChallenge, RoleComparison, RoleOutcome, RoleRelevance}
}

// ValidRole reports whether role is one of §4.12's review roles.
func ValidRole(role string) bool { return slices.Contains(Roles(), role) }

// RolesForKind is the applicability registry: which review roles apply to one
// subject kind, in a stable order.
//
// It lives in the core rather than in selection policy because it is a
// validation rule before it is a sampling input. A write is refused against a
// role its subject cannot carry, so an "outcome" on an observation cannot
// exist at all - rather than existing and being filtered out of one view.
//
// An empty result means the kind is not evaluable at all, which is how this
// package validates subject kinds: there is no separate list to drift from.
//
// Two decisions are worth stating. Observations and findings carry evidence
// and relevance but no outcome, because they are claims about what is the case
// and there is no promised change to verify. Evaluations themselves are
// evaluable - §4.12 admits bounded meta-review - but only for reception and
// challenge: admitting outcome or evidence roles there would require reviewing
// every review's review, which is the recursion docs/evaluation-lifecycle.md
// §E1 requires bounding.
func RolesForKind(subjectKind string) []string {
	switch subjectKind {
	case "hypothesis":
		return []string{RoleReception, RoleEvidence, RoleChallenge, RoleRelevance}
	case "observation":
		return []string{RoleReception, RoleEvidence, RoleRelevance}
	case "finding":
		return []string{RoleReception, RoleEvidence, RoleChallenge, RoleRelevance}
	case "proposal":
		return []string{RoleReception, RoleEvidence, RoleChallenge, RoleComparison, RoleOutcome, RoleRelevance}
	case "evaluation":
		return []string{RoleReception, RoleChallenge}
	}
	return nil
}

// RoleApplies reports whether role is a review this subject kind can carry.
//
// A false answer is an explicit unsupported gap, never a reviewed record and
// never an automatic not-applicable: a missing evaluator is a hole in coverage
// that stays visible.
func RoleApplies(subjectKind, role string) bool {
	return slices.Contains(RolesForKind(subjectKind), role)
}

// The coverage states one role can be in for one subject. They are core
// vocabulary rather than a view's presentation choice, because the whole point
// of role-specific coverage is that these are different facts: a record with
// three reception votes and no evidence check is reviewed for reception and
// unreviewed for evidence, and a surface that could only say "reviewed" would
// present a bare vote as a verified evidence check.
const (
	// CoverageUnreviewed means the role applies and no assessment has been
	// recorded for it.
	CoverageUnreviewed = "unreviewed"
	// CoverageReviewed means the role's obligation has been met at the
	// current revision and context.
	CoverageReviewed = "reviewed"
	// CoverageDue means the role was reviewed, and a later revision or a
	// changed context makes it due again. History is kept; see §E1.
	CoverageDue = "due"
	// CoverageUnsupported means no adapter or evaluator exists for the role
	// on this subject kind. It is a gap that stays visible and never becomes
	// reviewed or not-applicable on its own.
	CoverageUnsupported = "unsupported"
	// CoverageBlocked means review cannot proceed for a recorded reason -
	// repeated skips, an unreadable source.
	CoverageBlocked = "blocked"
	// CoverageNotApplicable is reachable only through an intentional named
	// policy with a reason (§E1), never by inference from absence.
	CoverageNotApplicable = "not_applicable"
)

// CoverageStates lists the coverage states in a stable order.
func CoverageStates() []string {
	return []string{CoverageUnreviewed, CoverageReviewed, CoverageDue,
		CoverageUnsupported, CoverageBlocked, CoverageNotApplicable}
}

// RoleCoverage is one subject's coverage for one review role.
//
// Reviews counts the assessments that met this role's obligation, and it is
// per role for the reason the type exists: reception votes do not accumulate
// towards an evidence check. Reason is required by the states that need an
// explanation - unsupported, blocked and not-applicable - so a gap can never
// be rendered without saying why.
type RoleCoverage struct {
	Role    string `json:"role"`
	State   string `json:"state"`
	Reason  string `json:"reason,omitempty"`
	Reviews int    `json:"reviews"`
	// Overdue reports that the role is past the policy's overdue threshold.
	// It is separate from State because an unreviewed role that is not yet
	// overdue and one that is are different pieces of news.
	Overdue      bool      `json:"overdue,omitempty"`
	LastReviewed time.Time `json:"last_reviewed,omitzero"`
}

// CoverageCounts is a non-recursive tally of subjects per coverage state, for
// one role or for a whole inventory.
//
// It is counts and nothing else: no scores, no ranks, no nesting. A projection
// that wanted a breakdown by role reads one of these per role, which keeps the
// shape flat and keeps a page's cost bounded as the corpus grows.
type CoverageCounts struct {
	Unreviewed    int `json:"unreviewed"`
	Reviewed      int `json:"reviewed"`
	Due           int `json:"due"`
	Unsupported   int `json:"unsupported"`
	Blocked       int `json:"blocked"`
	NotApplicable int `json:"not_applicable"`
	Overdue       int `json:"overdue"`
}

// The reception vocabulary of §4.12. The empty value is lawful and means the
// assessment carries a contribution or an outcome instead of a vote.
const (
	VoteSupport = "support"
	VoteOppose  = "oppose"
	VoteUnsure  = "unsure"
)

// Votes lists the reception values a worker may return.
func Votes() []string { return []string{VoteSupport, VoteOppose, VoteUnsure} }

// The outcome vocabulary of §4.12 and docs/evaluation-lifecycle.md §E6.
//
// The distinctions are the point. OutcomeImplemented says a change landed;
// OutcomeVerified says the operator's criteria were met and every one of them
// is evidenced; OutcomePartial says some were and some were not;
// OutcomeContradicted says evidence contradicts an earlier verification; and
// OutcomeUnverifiable says the question was asked and no answer was reachable.
// A merge is not a deployment and a deployment is not proof of the promised
// outcome, so nothing in this package promotes one value to another.
const (
	OutcomeImplemented  = "implemented"
	OutcomeVerified     = "verified"
	OutcomePartial      = "partial"
	OutcomeContradicted = "contradicted"
	OutcomeUnverifiable = "unverifiable"
)

// Outcomes lists the outcome values a worker may return.
func Outcomes() []string {
	return []string{OutcomeImplemented, OutcomeVerified, OutcomePartial,
		OutcomeContradicted, OutcomeUnverifiable}
}

// The contribution vocabulary. A contribution is optional refinement material
// beside a vote, or the whole of an assessment when there is no vote.
const (
	// ContributionComment is a neutral note: an observation about the record
	// that argues neither for nor against it.
	ContributionComment = "comment"
	// ContributionArgument is prose in favour, and ContributionObjection is
	// prose against. They are separate kinds because a reader that could not
	// tell them apart would either list a supporting argument among the
	// unresolved objections or drop the objection entirely, and §8.5 renders
	// "the unresolved objection" beside every recommendation.
	ContributionArgument   = "argument"
	ContributionObjection  = "objection"
	ContributionEvidence   = "evidence"
	ContributionRefinement = "refinement"
	ContributionComparison = "comparison"
)

// ContributionKinds lists the contribution kinds.
func ContributionKinds() []string {
	return []string{ContributionComment, ContributionArgument, ContributionObjection,
		ContributionEvidence, ContributionRefinement, ContributionComparison}
}

// The attempt states of one assignment. They are distinguishable on purpose:
// §4.12 requires an assignment, an exposure to content, a completed
// assessment, a skip and a failure to remain separate facts, because a skipped
// record is not a reviewed record and an unopened assignment is not a skip.
const (
	AttemptExposed   = "exposed"
	AttemptCompleted = "completed"
	AttemptSkipped   = "skipped"
	AttemptFailed    = "failed"
)

// AttemptStates lists the attempt states.
func AttemptStates() []string {
	return []string{AttemptExposed, AttemptCompleted, AttemptSkipped, AttemptFailed}
}

// FeedbackReasonSuggestions lists the scoped feedback reasons
// docs/evaluation-lifecycle.md §E5 names, for a surface that wants to offer
// them as buttons.
//
// It is deliberately not a validation gate. The document says "reasons such
// as", and an operator whose actual reason is none of these must be able to
// record the actual reason rather than the nearest listed one.
func FeedbackReasonSuggestions() []string {
	return []string{"not-now", "wrong-problem", "wrong-remedy"}
}

// validID bounds every identifier this package mints or accepts. It is
// internal/sync's validEntityID: an identifier the shared catalog would refuse
// is a staged record that can never publish, and refusing it here turns that
// into a caller's failed write instead of a permanent pending-sync entry.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validRunID is validID widened by the one separator internal/explore uses
// for a job within a run (`<run>/<stage>`). internal/sync reduces it to the
// publication run, so a compound identity is lawful here and refused nowhere
// downstream.
var validRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,191}$`)

// Subject addresses exactly one immutable record revision.
//
// ID is the revision's own identity, not a chain root: §4.12 binds a vote to
// the wording that was read, so a subject that named a mutable chain would let
// an endorsement migrate to a wording nobody assessed.
type Subject struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Zero reports whether this is the absent subject, which is lawful only for
// the deployment-wide records: policy and checkpoint.
func (s Subject) Zero() bool { return s.Kind == "" && s.ID == "" }

// String renders a subject for an error message and a log line.
func (s Subject) String() string { return s.Kind + ":" + s.ID }

func (s Subject) validate() error {
	if len(RolesForKind(s.Kind)) == 0 {
		return fmt.Errorf("%w: %q is not an evaluable record kind", ErrInvalid, s.Kind)
	}
	if !validID.MatchString(s.ID) {
		return fmt.Errorf("%w: subject id %q is not a well-formed record identifier", ErrInvalid, s.ID)
	}
	return nil
}

// Criterion is one condition the operator would count as the promised
// outcome. Its ID is stable so a later result can name exactly which condition
// it answers.
type Criterion struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

func (c Criterion) validate() error {
	if !validID.MatchString(c.ID) {
		return fmt.Errorf("%w: criterion id %q is not a well-formed identifier", ErrInvalid, c.ID)
	}
	if strings.TrimSpace(c.Description) == "" {
		return fmt.Errorf("%w: criterion %s has no description", ErrInvalid, c.ID)
	}
	return nil
}

// CriterionResult answers one criterion.
//
// Evidence is what makes the answer admissible. A satisfied criterion with no
// evidence is the manufactured verified result §4.12 forbids, so it is refused
// rather than stored and later discounted.
type CriterionResult struct {
	CriterionID string              `json:"criterion_id"`
	Satisfied   bool                `json:"satisfied"`
	Evidence    []frontier.Evidence `json:"evidence,omitempty"`
	Uncertainty string              `json:"uncertainty,omitempty"`
}

func (r CriterionResult) validate() error {
	if !validID.MatchString(r.CriterionID) {
		return fmt.Errorf("%w: criterion result names %q, which is not a well-formed criterion id",
			ErrInvalid, r.CriterionID)
	}
	if r.Satisfied && len(r.Evidence) == 0 {
		return fmt.Errorf("%w: criterion %s is reported satisfied with no evidence", ErrInvalid, r.CriterionID)
	}
	return validateEvidence("criterion result "+r.CriterionID, r.Evidence)
}

// Contribution is optional refinement material: a comment, new evidence, an
// objection, a refinement, or a comparison of alternatives.
//
// Preferred and Alternatives belong to a comparison and to nothing else. A
// comparison may prefer one remedy in a named context; it must not mint a
// global vote for either, which is why a preference lives here rather than in
// Vote.
type Contribution struct {
	Kind         string              `json:"kind"`
	Text         string              `json:"text,omitempty"`
	Evidence     []frontier.Evidence `json:"evidence,omitempty"`
	Alternatives []Subject           `json:"alternatives,omitempty"`
	Preferred    *Subject            `json:"preferred,omitempty"`
	// WouldChange is what could change the recommendation, where the model
	// knows. An empty value is lawful: §E5 forbids requiring a fabricated
	// rationale for a bare judgement.
	WouldChange string `json:"would_change,omitempty"`
}

func (c Contribution) validate() error {
	if !slices.Contains(ContributionKinds(), c.Kind) {
		return fmt.Errorf("%w: %q is not a contribution kind", ErrInvalid, c.Kind)
	}
	if err := validateEvidence("contribution "+c.Kind, c.Evidence); err != nil {
		return err
	}
	switch c.Kind {
	case ContributionEvidence:
		if len(c.Evidence) == 0 {
			return fmt.Errorf("%w: an evidence contribution with no evidence states nothing", ErrInvalid)
		}
	case ContributionComparison:
		if len(c.Alternatives) < 2 {
			return fmt.Errorf("%w: a comparison needs at least two alternatives", ErrInvalid)
		}
	default:
		if len(c.Alternatives) > 0 || c.Preferred != nil {
			return fmt.Errorf("%w: only a comparison carries alternatives or a preference, not a %s",
				ErrInvalid, c.Kind)
		}
		if strings.TrimSpace(c.Text) == "" && len(c.Evidence) == 0 {
			return fmt.Errorf("%w: a %s contribution carries neither text nor evidence", ErrInvalid, c.Kind)
		}
	}
	for i, alternative := range c.Alternatives {
		if err := alternative.validate(); err != nil {
			return fmt.Errorf("contribution alternative %d: %w", i, err)
		}
		if slices.Index(c.Alternatives, alternative) != i {
			// [A, A] meets "at least two alternatives" and compares nothing.
			// A comparison of one remedy with itself is a preference with no
			// alternative, which is the global vote a comparison may not mint.
			return fmt.Errorf("%w: alternative %s is compared with itself", ErrInvalid, alternative)
		}
	}
	if c.Preferred != nil {
		if err := c.Preferred.validate(); err != nil {
			return fmt.Errorf("preferred alternative: %w", err)
		}
		if !slices.Contains(c.Alternatives, *c.Preferred) {
			return fmt.Errorf("%w: the preferred alternative %s is not among the compared alternatives",
				ErrInvalid, c.Preferred)
		}
	}
	return nil
}

// Assessment is what one assignment produced.
//
// All three of its statements are optional individually and at least one is
// required together: a bare vote is a complete reception review, a
// contribution with no vote is a lawful pre-review, and an outcome is neither
// of those. An assessment that states none of them is not a modest review, it
// is a completion with no content, and a skip is the honest record of that.
type Assessment struct {
	Vote          string         `json:"vote,omitempty"`
	Contributions []Contribution `json:"contributions,omitempty"`
	Outcome       string         `json:"outcome,omitempty"`
	// CriteriaID names the operator-authored criteria record this outcome is
	// measured against. It is required for any outcome, because a verification
	// against a target Babel chose for itself is exactly what §4.12 refuses.
	CriteriaID string            `json:"criteria_id,omitempty"`
	Results    []CriterionResult `json:"results,omitempty"`
	// Environment is where the outcome was observed. Required for an outcome:
	// evidence whose scope is unknown cannot be weighed against contrary
	// evidence later.
	Environment string    `json:"environment,omitempty"`
	AsOf        time.Time `json:"as_of,omitzero"`
	Uncertainty string    `json:"uncertainty,omitempty"`
	// ContextVersion is the recorded operator/Reality context the assessment
	// was made under. It is the read context, never the current one: a stale
	// result must not be able to represent current-context coverage.
	ContextVersion string `json:"context_version,omitempty"`
}

// validateShape checks everything about an assessment that does not depend on
// the grant it came from: the closed vocabularies, the requirement that it
// state something, and what an outcome claim must carry to be admissible.
//
// It is separate from validate because a record read back from the wire has no
// assignment in hand and must still be refused if it carries an invented vote
// or an unevidenced verification. A reader that validated only structure would
// ingest "enthusiastic" as a reception value and a verified outcome with no
// evidence as a verification.
func (a Assessment) validateShape() error {
	if a.Vote != "" && !slices.Contains(Votes(), a.Vote) {
		return fmt.Errorf("%w: %q is not a reception value", ErrInvalid, a.Vote)
	}
	if a.Outcome != "" && !slices.Contains(Outcomes(), a.Outcome) {
		return fmt.Errorf("%w: %q is not an outcome value", ErrInvalid, a.Outcome)
	}
	if a.Vote == "" && len(a.Contributions) == 0 && a.Outcome == "" {
		return fmt.Errorf("%w: an assessment states no vote, no contribution and no outcome; record a skip instead",
			ErrInvalid)
	}
	for i, contribution := range a.Contributions {
		if err := contribution.validate(); err != nil {
			return fmt.Errorf("contribution %d: %w", i, err)
		}
	}
	answered := make(map[string]struct{}, len(a.Results))
	for i, result := range a.Results {
		if err := result.validate(); err != nil {
			return fmt.Errorf("criterion result %d: %w", i, err)
		}
		if _, repeated := answered[result.CriterionID]; repeated {
			// Two answers to one condition are two claims, and a coverage
			// count over them would read one criterion as two.
			return fmt.Errorf("%w: criterion %s is answered twice", ErrInvalid, result.CriterionID)
		}
		answered[result.CriterionID] = struct{}{}
	}
	if len(a.Results) > 0 && !validID.MatchString(a.CriteriaID) {
		return fmt.Errorf("%w: criterion results must name the operator-authored criteria version they "+
			"answer, or nothing binds them to a condition anybody stated", ErrInvalid)
	}
	if err := a.validateOutcome(); err != nil {
		return err
	}
	if a.Outcome == "" {
		// An evidence check answers criteria without declaring an outcome, so
		// results and the criteria version they answer are lawful here. An
		// observed environment is not: it is the scope of an outcome claim,
		// and a scope with no claim is a claim wearing a check's clothes.
		if strings.TrimSpace(a.Environment) != "" {
			return fmt.Errorf("%w: an observed environment belongs to an outcome claim", ErrInvalid)
		}
		if a.CriteriaID != "" && len(a.Results) == 0 {
			return fmt.Errorf("%w: a criteria version with no criterion result answers nothing", ErrInvalid)
		}
	}
	return nil
}

// validate checks an assessment against the role it was assigned under.
//
// The role is a parameter rather than a field because the authority comes from
// the assignment, not from the returned payload: a reception assignment that
// came back with an outcome claim would otherwise be able to grant itself the
// role it was not given. That holds for every statement an assessment can
// make and not only for outcomes - a reception grant returning a comparison
// would be performing the comparison role, including on a subject kind
// RolesForKind says cannot carry one.
//
// The floor stays low on the other side. A bare vote is a complete reception
// review and no role requires prose: what is refused is a statement the grant
// did not authorize, never a modest one.
func (a Assessment) validate(role string) error {
	if !ValidRole(role) {
		return fmt.Errorf("%w: %q is not a review role", ErrInvalid, role)
	}
	if a.Vote != "" && role != RoleReception {
		return fmt.Errorf("%w: a reception vote needs the %s role, not %q: a vote meets no other role's "+
			"obligation", ErrInvalid, RoleReception, role)
	}
	if a.Outcome != "" && role != RoleOutcome {
		return fmt.Errorf("%w: an outcome claim needs the %s role, not %q", ErrInvalid, RoleOutcome, role)
	}
	if len(a.Results) > 0 && role != RoleEvidence && role != RoleOutcome {
		return fmt.Errorf("%w: criterion results are what an evidence check or an outcome verification "+
			"produces, not a %s review", ErrInvalid, role)
	}
	for i, contribution := range a.Contributions {
		if contribution.Kind == ContributionComparison && role != RoleComparison {
			return fmt.Errorf("%w: contribution %d compares alternatives, which needs the %s role, not %q",
				ErrInvalid, i, RoleComparison, role)
		}
	}
	if err := a.validateShape(); err != nil {
		return err
	}
	return a.satisfiesRole(role)
}

// satisfiesRole refuses a completion that states nothing the granted role
// counts.
//
// It is the other half of the role check and it is not a prose requirement.
// The coverage vocabulary exists because a bare vote satisfies reception and
// stands in for nothing else (§4.12): a completion journalled against the
// evidence role therefore has to carry something an evidence check produces -
// a criterion result or a contribution - and an outcome grant that reached no
// answer says so with an unverifiable outcome rather than with silence. An
// assessment that meets no obligation at all is a skip, and recording it as a
// completion is how a role comes to read as reviewed by a review that did not
// happen.
func (a Assessment) satisfiesRole(role string) error {
	stated := true
	switch role {
	case RoleReception:
		stated = a.Vote != "" || len(a.Contributions) > 0
	case RoleEvidence:
		stated = len(a.Results) > 0 || len(a.Contributions) > 0
	case RoleChallenge, RoleRelevance:
		stated = len(a.Contributions) > 0
	case RoleComparison:
		stated = slices.ContainsFunc(a.Contributions, func(c Contribution) bool {
			return c.Kind == ContributionComparison
		})
	case RoleOutcome:
		stated = a.Outcome != ""
	}
	if !stated {
		return fmt.Errorf("%w: this assessment states nothing the %s role counts; a skip is the honest "+
			"record of that", ErrInvalid, role)
	}
	return nil
}

// validateOutcome is what an outcome claim must carry before this package will
// store it as one. The role check is validate's; everything here is a property
// of the claim itself and holds wherever it is read.
func (a Assessment) validateOutcome() error {
	if a.Outcome == "" {
		return nil
	}
	if !validID.MatchString(a.CriteriaID) {
		return fmt.Errorf("%w: an outcome claim must name the operator's criteria version it is measured against",
			ErrInvalid)
	}
	if strings.TrimSpace(a.Environment) == "" {
		return fmt.Errorf("%w: an outcome claim must name the environment it was observed in", ErrInvalid)
	}
	if a.AsOf.IsZero() {
		return fmt.Errorf("%w: an outcome claim must say when it was observed", ErrInvalid)
	}
	satisfied, unsatisfied, evidence := 0, 0, 0
	for _, result := range a.Results {
		if result.Satisfied {
			satisfied++
		} else {
			unsatisfied++
		}
		evidence += len(result.Evidence)
	}
	for _, contribution := range a.Contributions {
		evidence += len(contribution.Evidence)
	}
	switch a.Outcome {
	case OutcomeVerified:
		// Every answer satisfied and every one of them evidenced -
		// CriterionResult.validate already refuses an unevidenced
		// satisfaction, so what is added here is that an empty answer set
		// cannot be called verified. That every criterion the operator
		// actually stated was answered is checked where the criteria record
		// is in hand; see Store.validateAssessment.
		if len(a.Results) == 0 {
			return fmt.Errorf("%w: a verified outcome must answer the criteria it claims were met", ErrInvalid)
		}
		if unsatisfied > 0 {
			return fmt.Errorf("%w: %d criteria are not satisfied, which is at most a partial outcome",
				ErrInvalid, unsatisfied)
		}
	case OutcomePartial:
		if len(a.Results) == 0 {
			return fmt.Errorf("%w: a partial outcome must say which criteria it answered", ErrInvalid)
		}
		if unsatisfied == 0 && strings.TrimSpace(a.Uncertainty) == "" {
			return fmt.Errorf("%w: a partial outcome with every criterion satisfied must state what remains uncertain",
				ErrInvalid)
		}
	case OutcomeContradicted:
		contrary := false
		for _, result := range a.Results {
			if !result.Satisfied && len(result.Evidence) > 0 {
				contrary = true
			}
		}
		if !contrary {
			return fmt.Errorf("%w: a contradicted outcome needs at least one criterion with contrary evidence",
				ErrInvalid)
		}
	case OutcomeUnverifiable:
		if strings.TrimSpace(a.Uncertainty) == "" {
			return fmt.Errorf("%w: an unverifiable outcome must say what could not be reached", ErrInvalid)
		}
		if satisfied > 0 {
			return fmt.Errorf("%w: an unverifiable outcome cannot also report %d satisfied criteria",
				ErrInvalid, satisfied)
		}
		return nil
	case OutcomeImplemented:
		if satisfied > 0 && evidence == 0 {
			return fmt.Errorf("%w: an implemented outcome that claims satisfied criteria needs evidence", ErrInvalid)
		}
	}
	if evidence == 0 {
		return fmt.Errorf("%w: a %s outcome needs admissible evidence", ErrInvalid, a.Outcome)
	}
	return nil
}

// Provenance is what produced a record. §4.12 requires model, profile, run,
// recipe and context provenance to survive on every assessment, because two
// independently assigned assessments from the same model are legitimate and a
// reader has to be able to see that they are not independent corroboration.
type Provenance struct {
	RunID         string `json:"run_id,omitempty"`
	Model         string `json:"model,omitempty"`
	Profile       string `json:"profile,omitempty"`
	Recipe        string `json:"recipe,omitempty"`
	RecipeVersion int    `json:"recipe_version,omitempty"`
	// Blinded records whether the initial read withheld prior evaluations,
	// tallies and ranks. It is procedural blinding, not erased model memory,
	// and it is recorded rather than assumed so an audit can compare it
	// against what was actually served.
	Blinded        bool      `json:"blinded,omitempty"`
	ContextVersion string    `json:"context_version,omitempty"`
	Consulted      []Subject `json:"consulted,omitempty"`
}

func (p Provenance) validate() error {
	if p.RunID != "" && !validRunID.MatchString(p.RunID) {
		return fmt.Errorf("%w: provenance run id %q is not a well-formed run identifier", ErrInvalid, p.RunID)
	}
	if p.RecipeVersion < 0 {
		return fmt.Errorf("%w: recipe version %d is negative", ErrInvalid, p.RecipeVersion)
	}
	for i, subject := range p.Consulted {
		if err := subject.validate(); err != nil {
			return fmt.Errorf("consulted subject %d: %w", i, err)
		}
	}
	return nil
}

// Context is the recorded operator and Reality context a judgement was made
// under: priorities, current work, pain, blockers, permitted work and the
// reasons behind them.
//
// Unknown is explicit and load-bearing. §E5 requires missing and conflicting
// context to stay visible rather than being resolved into a confident
// recommendation, and an inferred interest must never override an explicit
// policy - so what was not known is recorded as not known.
type Context struct {
	Version     string `json:"version"`
	Priority    int    `json:"priority,omitempty"`
	CurrentWork bool   `json:"current_work,omitempty"`
	Pain        int    `json:"pain,omitempty"`
	Blocked     bool   `json:"blocked,omitempty"`
	// Allowance is the recorded work allowance, with its own semantics
	// preserved: learn-only is not the same as excluding a subject from all
	// learning.
	Allowance string              `json:"allowance,omitempty"`
	Reasons   []string            `json:"reasons,omitempty"`
	Evidence  []frontier.Evidence `json:"evidence,omitempty"`
	Unknown   []string            `json:"unknown,omitempty"`
}

func (c Context) validate() error {
	if strings.TrimSpace(c.Version) == "" {
		return fmt.Errorf("%w: a context snapshot must carry its version", ErrInvalid)
	}
	if c.Priority < 0 || c.Pain < 0 {
		return fmt.Errorf("%w: context priority and pain are not negative", ErrInvalid)
	}
	return validateEvidence("context", c.Evidence)
}

// Artifact is one evaluable record as the resolver sees it: its exact revision
// identity, its chain, what it says, and the recorded context around it.
//
// HeadID and Subject.ID are separate for the reason the whole package exists:
// an assessment binds to Subject, and a reader that wants to know whether the
// wording moved on compares the two. Collapsing them would make every vote
// look current.
type Artifact struct {
	Subject Subject `json:"subject"`
	RootID  string  `json:"root_id,omitempty"`
	HeadID  string  `json:"head_id,omitempty"`
	RunID   string  `json:"run_id,omitempty"`
	// CreatedAt is when the revision was produced, which is what an
	// oldest-due initial review reserve is ordered by.
	CreatedAt time.Time `json:"created_at,omitzero"`
	Title     string    `json:"title,omitempty"`
	// Body is the record's own payload, verbatim. It is raw JSON because the
	// producing store owns its shape and this package must not re-encode
	// model-authored text.
	Body json.RawMessage `json:"body,omitempty"`
	// ReviewStatus is the operator's review state from internal/frontier, and
	// Status the exploration lifecycle. Both are read-only here: evaluation
	// never sets a disposition.
	ReviewStatus string              `json:"review_status,omitempty"`
	Status       string              `json:"status,omitempty"`
	Related      []Subject           `json:"related,omitempty"`
	Evidence     []frontier.Evidence `json:"evidence,omitempty"`
	Criteria     []Criterion         `json:"criteria,omitempty"`
	CriteriaID   string              `json:"criteria_id,omitempty"`
	Context      Context             `json:"context,omitzero"`
	// ContextVersion is the context the artifact is currently under, which a
	// caller compares against an assessment's read context to tell a
	// current-context review from a stale one.
	ContextVersion string `json:"context_version,omitempty"`
}

// Policy is one versioned evaluation configuration. Its default values and
// validation live in policy.go, which owns the measured settings; this file
// owns only the shape, because the shape is published.
type Policy struct {
	Version          string  `json:"version"`
	Enabled          bool    `json:"enabled"`
	CadenceSeconds   int     `json:"cadence_seconds"`
	OverdueSeconds   int     `json:"overdue_seconds"`
	InitialReviews   int     `json:"initial_reviews"`
	CooldownSeconds  int     `json:"cooldown_seconds"`
	CoverageShare    float64 `json:"coverage_share"`
	ExplorationShare float64 `json:"exploration_share"`
	DiscoveryShare   float64 `json:"discovery_share"`
	MaxItemReviews   int     `json:"max_item_reviews"`
	PerCycleCost     float64 `json:"per_cycle_cost"`
	DailyCost        float64 `json:"daily_cost"`
	LeaseSeconds     int     `json:"lease_seconds"`
	BatchSize        int     `json:"batch_size"`
}

// Assignment is one bounded grant of review attention.
//
// Everything a replay needs is here and none of it is derived: the policy
// version that selected it, the seed the draw used, the digest of the captured
// inputs, the role, the lane it is accounted to, the fence that makes a stale
// worker's late result refusable, and the cost reserved against the allowance.
// A seed alone would not be enough - §E4 says so explicitly - because the
// same seed against changed inputs is a different draw.
type Assignment struct {
	ID             string    `json:"id"`
	Subject        Subject   `json:"subject"`
	RunID          string    `json:"run_id"`
	Role           string    `json:"role"`
	PolicyVersion  string    `json:"policy_version"`
	ContextVersion string    `json:"context_version,omitempty"`
	Seed           uint64    `json:"seed"`
	InputDigest    string    `json:"input_digest"`
	CreatedAt      time.Time `json:"created_at,omitzero"`
	ExpiresAt      time.Time `json:"expires_at,omitzero"`
	// Fence is the authority, not RunID: a resumed worker can present the
	// right run id with a superseded fence and must still be refused.
	Fence        int64   `json:"fence"`
	ReservedCost float64 `json:"reserved_cost"`
	// Lane is the allocation this grant is accounted to - coverage,
	// exploration, discovery, challenge. Every lane is charged to one
	// allowance; concurrent workers must not each treat the whole allowance
	// as theirs.
	Lane string `json:"lane"`
	// Subjects are the recorded self-stated entity names this work is about,
	// sorted and deduplicated. Empty is lawful and means the artifact names
	// no entity.
	//
	// They travel on the grant rather than being looked up at launch time for
	// a reason that is a correctness bug otherwise: the conductor has to admit
	// the work against the recorded Reality allowance before anything is
	// shown to a model, and the only other way to learn the names would be to
	// open the review context - which records an exposure for a review that
	// may then be refused. An exposure that never happened is exactly the
	// kind of false coverage #219 exists to prevent.
	Subjects []string `json:"subjects,omitempty"`
	// Corrects names the statement this grant exists to revise, and is empty
	// for an ordinary review.
	//
	// It is on the grant rather than on the result because it decides what
	// the model is shown before it says anything: a correction has to read
	// its own earlier statement, while §5.8's initial review is blinded to
	// prior conclusions. A grant that carries it is a paid follow-up on one
	// named statement, never a second independent reception of the subject.
	Corrects string `json:"corrects,omitempty"`
}

// validate refuses a grant that could not be replayed, accounted or fenced.
func (a Assignment) validate() error {
	if !validID.MatchString(a.ID) {
		return fmt.Errorf("%w: assignment id %q is not a well-formed identifier", ErrInvalid, a.ID)
	}
	if err := a.Subject.validate(); err != nil {
		return fmt.Errorf("assignment %s subject: %w", a.ID, err)
	}
	if !validRunID.MatchString(a.RunID) {
		return fmt.Errorf("%w: assignment %s run id %q is not a well-formed run identifier",
			ErrInvalid, a.ID, a.RunID)
	}
	if !ValidRole(a.Role) {
		return fmt.Errorf("%w: %q is not a review role", ErrInvalid, a.Role)
	}
	if !RoleApplies(a.Subject.Kind, a.Role) {
		return fmt.Errorf("%w: the %s role does not apply to a %s", ErrInvalid, a.Role, a.Subject.Kind)
	}
	if strings.TrimSpace(a.PolicyVersion) == "" {
		return fmt.Errorf("%w: assignment %s names no policy version, so its draw could not be replayed",
			ErrInvalid, a.ID)
	}
	if strings.TrimSpace(a.InputDigest) == "" {
		return fmt.Errorf("%w: assignment %s carries no captured input digest, so a replay could not tell "+
			"whether the inputs changed", ErrInvalid, a.ID)
	}
	if strings.TrimSpace(a.Lane) == "" {
		return fmt.Errorf("%w: assignment %s names no lane, so its spend could not be accounted",
			ErrInvalid, a.ID)
	}
	if a.ReservedCost < 0 {
		return fmt.Errorf("%w: assignment %s reserves a negative cost", ErrInvalid, a.ID)
	}
	if a.Corrects != "" && !validID.MatchString(a.Corrects) {
		return fmt.Errorf("%w: assignment %s names statement %q as the one it corrects, which is not a "+
			"well-formed record identifier", ErrInvalid, a.ID, a.Corrects)
	}
	for i, name := range a.Subjects {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("%w: assignment %s names an empty entity at position %d, which would admit "+
				"work against nothing", ErrInvalid, a.ID, i)
		}
	}
	return nil
}

// Attempt is one state transition of an assignment.
//
// Retries do not inflate it: the rows are keyed by the assignment, the fence
// that produced them and the state, so a redelivered exposure or a retried
// completion records the same fact once. A takeover is a new fence and
// therefore a new attempt, which is the distinction between "this was tried
// twice" and "this was delivered twice".
type Attempt struct {
	AssignmentID string    `json:"assignment_id"`
	State        string    `json:"state"`
	Reason       string    `json:"reason,omitempty"`
	Cost         float64   `json:"cost,omitempty"`
	Unpriced     bool      `json:"unpriced,omitempty"`
	RecordedAt   time.Time `json:"recorded_at"`
}

func (a Attempt) validate() error {
	if !validID.MatchString(a.AssignmentID) {
		return fmt.Errorf("%w: attempt names assignment %q, which is not a well-formed identifier",
			ErrInvalid, a.AssignmentID)
	}
	if !slices.Contains(AttemptStates(), a.State) {
		return fmt.Errorf("%w: %q is not an attempt state", ErrInvalid, a.State)
	}
	if a.Cost < 0 {
		return fmt.Errorf("%w: attempt cost %f is negative", ErrInvalid, a.Cost)
	}
	if a.RecordedAt.IsZero() {
		return fmt.Errorf("%w: attempt on %s has no timestamp", ErrInvalid, a.AssignmentID)
	}
	return nil
}

// CoverageCheckpoint is the completion of one coverage sweep.
//
// It exists because §E4 makes two facts separate and a single boolean would
// conflate them: a sweep can finish while review work remains overdue.
// "Coverage inspection completed" is what this record says, and it says
// nothing whatever about whether the eligible output has been reviewed.
//
// It carries no opinion and no author. There is no vote, tally or rank in it -
// only when the sweep ran, the digest of the inventory it examined, and how
// many subjects that inventory held - and no ActorKind, because nobody
// authored a judgement. The instance that performed the sweep is attributed by
// the catalog row that publishes it rather than by a claimed author.
type CoverageCheckpoint struct {
	At          time.Time `json:"at"`
	InputDigest string    `json:"input_digest"`
	Covered     int       `json:"covered"`
}

func (c CoverageCheckpoint) validate() error {
	if c.At.IsZero() {
		return fmt.Errorf("%w: a coverage checkpoint must say when the sweep finished", ErrInvalid)
	}
	if strings.TrimSpace(c.InputDigest) == "" {
		return fmt.Errorf("%w: a coverage checkpoint must carry the digest of the inventory it examined",
			ErrInvalid)
	}
	if c.Covered < 0 {
		return fmt.Errorf("%w: a coverage checkpoint cannot have examined %d subjects", ErrInvalid, c.Covered)
	}
	return nil
}

// Record is one canonical, fully attributed, immutable evaluation
// publication.
//
// It is the durable source and the wire form at once: the bytes this package
// stages for the fleet are this struct, and a non-producing instance
// reconstructs exactly this from them. That is why the derived facts are
// absent - tallies, ranks, coverage states and recommendations are all
// projections a reader computes, and a published copy of one would be a second
// answer to a question the records already answer.
//
// The optional payloads are mutually exclusive by kind, and validate enforces
// which one each kind carries. A record with two payloads would be two claims
// wearing one identity.
type Record struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Subject Subject `json:"subject,omitzero"`
	// AssignmentID names the grant that authorized a run's assessment, and is
	// empty for an operator's own act.
	AssignmentID string `json:"assignment_id,omitempty"`
	// SupersedesID names the statement this record corrects. The corrected
	// statement is never rewritten: both remain readable, which is what makes
	// a correction a linked record rather than an edit.
	SupersedesID string      `json:"supersedes_id,omitempty"`
	ActorKind    string      `json:"actor_kind,omitempty"`
	ActorID      string      `json:"actor_id,omitempty"`
	CreatedAt    time.Time   `json:"created_at"`
	Provenance   Provenance  `json:"provenance,omitzero"`
	Assessment   *Assessment `json:"assessment,omitempty"`
	Criteria     []Criterion `json:"criteria,omitempty"`
	// Reason is why an operator recorded this, or what materially changed for
	// a reconsider item. Explicit reasons are preserved verbatim: clicks and
	// silence are not intent.
	Reason  string   `json:"reason,omitempty"`
	Context *Context `json:"context,omitempty"`
	Policy  *Policy  `json:"policy,omitempty"`
	// RelatedID is the record this one is about: the criteria an outcome was
	// measured against, the reconsider item a decision answers, the
	// disposition a feedback reason is scoped to.
	RelatedID string `json:"related_id,omitempty"`
	// Decision is the polarity of a reconsideration decision: reopen or
	// retain. It is required on one kind and refused on every other, because
	// a decision whose meaning lives in prose is a decision every reader
	// guesses at, and the two guesses are opposites.
	Decision string `json:"decision,omitempty"`
	// Stance is the polarity of an operator's reception: agree, disagree or
	// unsure. It is required on a feedback record authored by an operator
	// and refused on every other kind, for Decision's reason - a position
	// that lives in prose is a position each reader decides for himself.
	//
	// It is a second field beside Reason rather than a replacement for it,
	// because the two are not the same statement: §4.12 keeps the reason
	// verbatim, and a stance is what a reader can count without reading.
	//
	// A changed mind is another record. Nothing here is rewritten, so a
	// subject's operator stance is the newest of these and the earlier ones
	// stay readable in order - which is what makes "he used to agree"
	// answerable at all.
	Stance string `json:"stance,omitempty"`
	// Question marks the feedback record as something the operator asked
	// rather than something he said, which §8.7's `ask` act records: "a
	// question to Babel about this record, recorded as a comment Babel's
	// next review of the record must answer". It is a marker beside the
	// reason rather than a kind of its own, because the record is the same
	// act - operator-authored prose about a subject, verbatim, deciding
	// nothing - and a second kind would make a later review read two
	// stores to find what it owes an answer to.
	//
	// It is additive and optional, and absence means exactly what it has
	// always meant on every record already written: nobody asked anything.
	// So RecordSchema does not move for it - a reader of an older build
	// loses the label rather than misreading a claim, where a bump would
	// make every ordinary record this build writes unreadable to that
	// build in exchange for one.
	Question bool `json:"question,omitempty"`
	// Assignment, Attempt and Checkpoint carry the three judgement-free
	// record families. They are published for a reason that is easy to miss:
	// without them a second instance can count completed assessments and
	// nothing else, so an assignment nobody opened, an exposure with no
	// result and a finished coverage sweep would all be invisible - and
	// coverage would silently mean "reviewed by someone who published a
	// vote".
	Assignment *Assignment         `json:"assignment,omitempty"`
	Attempt    *Attempt            `json:"attempt,omitempty"`
	Checkpoint *CoverageCheckpoint `json:"checkpoint,omitempty"`
}

// validate is the one gate every record passes: on the way into the durable
// store, and on the way back in from the wire (see Decode). A record that
// cannot pass it is refused rather than stored and later distrusted.
func (r Record) validate() error {
	if !validID.MatchString(r.ID) {
		return fmt.Errorf("%w: record id %q is not a well-formed identifier", ErrInvalid, r.ID)
	}
	if !ValidRecordKind(r.Kind) {
		return fmt.Errorf("%w: %q is not an evaluation record kind", ErrInvalid, r.Kind)
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("%w: record %s has no timestamp", ErrInvalid, r.ID)
	}
	if err := r.validateActor(); err != nil {
		return err
	}
	if err := r.validateSubject(); err != nil {
		return err
	}
	if err := r.Provenance.validate(); err != nil {
		return fmt.Errorf("record %s provenance: %w", r.ID, err)
	}
	for _, id := range []string{r.SupersedesID, r.RelatedID} {
		if id != "" && !validID.MatchString(id) {
			return fmt.Errorf("%w: record %s references %q, which is not a well-formed record identifier",
				ErrInvalid, r.ID, id)
		}
	}
	if r.AssignmentID != "" && !validID.MatchString(r.AssignmentID) {
		return fmt.Errorf("%w: record %s names assignment %q, which is not a well-formed identifier",
			ErrInvalid, r.ID, r.AssignmentID)
	}
	if r.Context != nil {
		if err := r.Context.validate(); err != nil {
			return fmt.Errorf("record %s context: %w", r.ID, err)
		}
	}
	defined := make(map[string]struct{}, len(r.Criteria))
	for i, criterion := range r.Criteria {
		if err := criterion.validate(); err != nil {
			return fmt.Errorf("record %s criterion %d: %w", r.ID, i, err)
		}
		if _, repeated := defined[criterion.ID]; repeated {
			// A criteria version with one id twice cannot be answered: a
			// result naming it would answer whichever condition the reader
			// happened to iterate to first.
			return fmt.Errorf("%w: record %s states criterion %s twice", ErrInvalid, r.ID, criterion.ID)
		}
		defined[criterion.ID] = struct{}{}
	}
	if err := r.validateBlinding(); err != nil {
		return err
	}
	return r.validatePayload()
}

// validateBlinding refuses a record whose blinding claim contradicts its own
// provenance.
//
// This package cannot check what a worker was actually served - that record
// lives in the review surface that served it - but it can check the claim
// against the rest of the same record, and two contradictions are visible from
// here.
//
// A blinded read that names a prior evaluation as consulted has both withheld
// earlier conclusions and read one. The single exception is the evaluation
// that is itself the subject: §4.12's bounded meta-review is a review of one
// evaluation's wording, and reading the thing under review is not a leak of
// the tally it was blinded to.
//
// A correction cannot claim blinding at all. It supersedes a statement its own
// author wrote and had to read to revise, and §5.8 blinds the initial vote
// rather than everything that follows it.
func (r Record) validateBlinding() error {
	if !r.Provenance.Blinded {
		return nil
	}
	if r.SupersedesID != "" {
		return fmt.Errorf("%w: record %s corrects %s and cannot also claim its read was blinded to prior "+
			"evaluations: the statement it revises is one of them", ErrInvalid, r.ID, r.SupersedesID)
	}
	for _, consulted := range r.Provenance.Consulted {
		if consulted.Kind == "evaluation" && consulted != r.Subject {
			return fmt.Errorf("%w: record %s claims a blinded read and names prior evaluation %s as "+
				"consulted", ErrInvalid, r.ID, consulted)
		}
	}
	return nil
}

// validateActor enforces the authority boundary. Who may author which kind is
// not a convention here: an assessment signed by an operator and a criteria
// record signed by a run are both refused writes.
func (r Record) validateActor() error {
	switch r.Kind {
	case KindCheckpoint:
		// A sweep has no author; see CoverageCheckpoint. Naming one would
		// claim a judgement was made.
		if r.ActorKind != "" || r.ActorID != "" {
			return fmt.Errorf("%w: a coverage checkpoint carries no author", ErrInvalid)
		}
		return nil
	case KindAssessment, KindReconsider, KindAssignment, KindAttempt:
		if r.ActorKind != ActorRun {
			return fmt.Errorf("%w: a %s record is authored by a run, not by %q", ErrInvalid, r.Kind, r.ActorKind)
		}
		if !validRunID.MatchString(r.ActorID) {
			return fmt.Errorf("%w: record %s names run %q, which is not a well-formed run identifier",
				ErrInvalid, r.ID, r.ActorID)
		}
		return nil
	default:
		if r.ActorKind != ActorOperator {
			return fmt.Errorf("%w: a %s record is authored by an operator, not by %q",
				ErrInvalid, r.Kind, r.ActorKind)
		}
		if strings.TrimSpace(r.ActorID) == "" {
			return fmt.Errorf("%w: a %s record signed by nobody is not attributable later either",
				ErrInvalid, r.Kind)
		}
		return nil
	}
}

// validateSubject enforces which kinds address a record and which address the
// deployment.
func (r Record) validateSubject() error {
	switch r.Kind {
	case KindPolicy, KindCheckpoint:
		if !r.Subject.Zero() {
			return fmt.Errorf("%w: a %s record is deployment-wide and names no subject", ErrInvalid, r.Kind)
		}
		return nil
	case KindAttempt:
		// An attempt is about an assignment, and the assignment names the
		// subject. Carrying it twice would let the two disagree.
		if !r.Subject.Zero() {
			return fmt.Errorf("%w: an attempt record names its assignment, not a subject", ErrInvalid)
		}
		return nil
	default:
		return r.Subject.validate()
	}
}

// validatePayload enforces one payload per kind, and the per-kind minimum each
// one has to state.
func (r Record) validatePayload() error {
	payloads := 0
	for _, present := range []bool{r.Assessment != nil, r.Assignment != nil, r.Attempt != nil, r.Checkpoint != nil} {
		if present {
			payloads++
		}
	}
	if payloads > 1 {
		return fmt.Errorf("%w: record %s carries more than one payload", ErrInvalid, r.ID)
	}
	if r.Policy != nil && r.Kind != KindPolicy {
		return fmt.Errorf("%w: only a policy record carries a policy, not a %s", ErrInvalid, r.Kind)
	}
	if len(r.Criteria) > 0 && r.Kind != KindCriteria {
		return fmt.Errorf("%w: only a criteria record carries criteria, not a %s", ErrInvalid, r.Kind)
	}
	if r.Decision != "" && r.Kind != KindReconsiderDecision {
		return fmt.Errorf("%w: only a reconsideration decision carries a decision, not a %s",
			ErrInvalid, r.Kind)
	}
	if r.Stance != "" && r.Kind != KindFeedback {
		return fmt.Errorf("%w: only feedback carries a reception stance, not a %s", ErrInvalid, r.Kind)
	}
	if r.Question && r.Kind != KindFeedback {
		return fmt.Errorf("%w: only feedback carries a question, not a %s", ErrInvalid, r.Kind)
	}
	switch r.Kind {
	case KindAssessment:
		if r.Assessment == nil {
			return fmt.Errorf("%w: an assessment record carries an assessment", ErrInvalid)
		}
		if r.AssignmentID == "" {
			return fmt.Errorf("%w: an assessment is authorized by an assignment", ErrInvalid)
		}
	case KindAssignment:
		if r.Assignment == nil {
			return fmt.Errorf("%w: an assignment record carries an assignment", ErrInvalid)
		}
		if err := r.Assignment.validate(); err != nil {
			return err
		}
		if r.Assignment.ID != r.AssignmentID || r.Assignment.Subject != r.Subject {
			return fmt.Errorf("%w: assignment record %s disagrees with the grant it carries", ErrInvalid, r.ID)
		}
	case KindAttempt:
		if r.Attempt == nil {
			return fmt.Errorf("%w: an attempt record carries an attempt", ErrInvalid)
		}
		if err := r.Attempt.validate(); err != nil {
			return err
		}
		if r.Attempt.AssignmentID != r.AssignmentID {
			return fmt.Errorf("%w: attempt record %s disagrees with the assignment it names", ErrInvalid, r.ID)
		}
	case KindCheckpoint:
		if r.Checkpoint == nil {
			return fmt.Errorf("%w: a checkpoint record carries a coverage checkpoint", ErrInvalid)
		}
		return r.Checkpoint.validate()
	case KindCriteria:
		if len(r.Criteria) == 0 {
			return fmt.Errorf("%w: a criteria record states at least one criterion", ErrInvalid)
		}
	case KindPolicy:
		if r.Policy == nil {
			return fmt.Errorf("%w: a policy record carries a policy", ErrInvalid)
		}
		if strings.TrimSpace(r.Policy.Version) == "" {
			return fmt.Errorf("%w: a policy record carries an unversioned policy", ErrInvalid)
		}
	case KindFeedback:
		// Feedback has to say something. A reason or a stance each does;
		// neither is required when the other is present, and a record with
		// both is the ordinary case.
		//
		// The rule was a reason alone until the browser could record a
		// reception, and what it was defending against is unchanged: an
		// empty feedback record is a click with no content, and a surface
		// that stored one would be inflating attention. A stance is not
		// that - it is the operator's position, in a closed vocabulary, and
		// making him type a sentence to record it is what #234 found the
		// reading surface had instead of an opinion.
		if strings.TrimSpace(r.Reason) == "" && r.Stance == "" {
			return fmt.Errorf("%w: feedback exists to carry an explicit reason or an explicit stance",
				ErrInvalid)
		}
		if r.Stance != "" && !slices.Contains(Stances(), r.Stance) {
			return fmt.Errorf("%w: a reception states agree, disagree or unsure, not %q",
				ErrInvalid, r.Stance)
		}
		// A question is the reason, so it cannot be a marker on its own:
		// a record that said only "he asked something" would leave a
		// later review with an obligation and no question to answer.
		if r.Question && strings.TrimSpace(r.Reason) == "" {
			return fmt.Errorf("%w: a question is the words it asks, and this one has none", ErrInvalid)
		}
	case KindReconsider:
		if strings.TrimSpace(r.Reason) == "" {
			return fmt.Errorf("%w: a reconsider item must say what materially changed", ErrInvalid)
		}
		if r.RelatedID == "" {
			return fmt.Errorf("%w: a reconsider item must link the decided work it revisits", ErrInvalid)
		}
	case KindReconsiderDecision:
		if strings.TrimSpace(r.Reason) == "" {
			return fmt.Errorf("%w: a reconsideration decision must state the operator's reason", ErrInvalid)
		}
		if r.RelatedID == "" {
			return fmt.Errorf("%w: a reconsideration decision must link the reconsider item it answers",
				ErrInvalid)
		}
		if !slices.Contains(ReconsiderDecisions(), r.Decision) {
			return fmt.Errorf("%w: a reconsideration decision states whether it reopens or retains the "+
				"prior decision, not %q", ErrInvalid, r.Decision)
		}
	}
	if r.Assessment != nil {
		if r.Kind != KindAssessment {
			return fmt.Errorf("%w: only an assessment record carries an assessment, not a %s", ErrInvalid, r.Kind)
		}
		if r.ActorKind != ActorRun {
			// This is the one overlap worth refusing twice. An outcome
			// recorded by a person would be an operator disposition wearing
			// an observation's clothes, and §4.12 keeps acceptance,
			// observation and inference distinct.
			return fmt.Errorf("%w: an assessment is a run's observation, never an operator's disposition",
				ErrInvalid)
		}
		// The assessment's own contents are checked here rather than only at
		// the write path, because a record arriving from the wire has no
		// assignment in hand: without this, an invented vote or an
		// unevidenced verification published by another build would be
		// ingested as a judgement this deployment then renders.
		if err := r.Assessment.validateShape(); err != nil {
			return fmt.Errorf("record %s: %w", r.ID, err)
		}
	}
	return nil
}

// Submission is what a worker returns for one assignment.
//
// Exactly one of the three outcomes is stated: an assessment, a skip, or a
// failure. A skip and a failure are not votes and never become one - they are
// recorded in the completion journal so repeated skips and unsupported sources
// stay visible as gaps instead of turning into negative reception.
type Submission struct {
	AssignmentID string `json:"assignment_id"`
	RunID        string `json:"run_id"`
	// Fence is the epoch the work was claimed under. A stale worker returning
	// after a takeover presents an old fence and is refused.
	Fence        int64       `json:"fence"`
	Assessment   *Assessment `json:"assessment,omitempty"`
	SkipReason   string      `json:"skip_reason,omitempty"`
	FailedReason string      `json:"failed_reason,omitempty"`
	Cost         float64     `json:"cost,omitempty"`
	// Unpriced preserves missing provider cost reporting; settlement charges
	// the reservation conservatively rather than calling unobserved work free.
	Unpriced   bool       `json:"unpriced,omitempty"`
	Provenance Provenance `json:"provenance,omitzero"`
}

// state reports which of the three outcomes this submission states, refusing
// zero and refusing two.
func (s Submission) state() (string, error) {
	stated := 0
	state := ""
	if s.Assessment != nil {
		stated++
		state = AttemptCompleted
	}
	if s.SkipReason != "" {
		stated++
		state = AttemptSkipped
	}
	if s.FailedReason != "" {
		stated++
		state = AttemptFailed
	}
	switch stated {
	case 1:
		return state, nil
	case 0:
		return "", fmt.Errorf("%w: a submission states an assessment, a skip reason or a failure reason",
			ErrInvalid)
	default:
		return "", fmt.Errorf("%w: a submission states one outcome, not %d", ErrInvalid, stated)
	}
}

func (s Submission) validate() error {
	if !validID.MatchString(s.AssignmentID) {
		return fmt.Errorf("%w: submission names assignment %q, which is not a well-formed identifier",
			ErrInvalid, s.AssignmentID)
	}
	if !validRunID.MatchString(s.RunID) {
		return fmt.Errorf("%w: submission run id %q is not a well-formed run identifier", ErrInvalid, s.RunID)
	}
	if s.Fence <= 0 {
		return fmt.Errorf("%w: submission for %s carries no fence, so a stale result could not be refused",
			ErrInvalid, s.AssignmentID)
	}
	if s.Cost < 0 {
		return fmt.Errorf("%w: submission cost %f is negative", ErrInvalid, s.Cost)
	}
	if _, err := s.state(); err != nil {
		return err
	}
	return s.Provenance.validate()
}

// OperatorInput is one operator-authored act: criteria, scoped feedback, a
// reconsideration decision, or a policy configuration.
//
// Operator is the attributed author and is required. A surface resolves it
// from the session that is actually signed in, never from a request body:
// §8.5's guards exist so a caller cannot masquerade as another operator or as
// a run.
type OperatorInput struct {
	Subject   Subject     `json:"subject,omitzero"`
	Kind      string      `json:"kind"`
	Operator  string      `json:"operator"`
	Reason    string      `json:"reason,omitempty"`
	Criteria  []Criterion `json:"criteria,omitempty"`
	Context   *Context    `json:"context,omitempty"`
	Policy    *Policy     `json:"policy,omitempty"`
	RelatedID string      `json:"related_id,omitempty"`
	// Decision is the reconsideration polarity - reopen or retain - and is
	// required on a reconsideration decision and refused on every other act.
	// A surface collects it as an explicit choice: §4.12's "the operator can
	// reopen or retain the decision" is two buttons, not a sentence a reader
	// classifies afterwards.
	Decision string `json:"decision,omitempty"`
	// Stance is the operator's reception polarity - agree, disagree or
	// unsure - and is lawful only on feedback. It is optional there, because
	// a scoped reason with no position is still the act §4.12 named: "not
	// now, the benchmark lands first" takes no side on whether the record is
	// right.
	Stance string `json:"stance,omitempty"`
	// Question marks the feedback as §8.7's `ask`: prose the operator
	// addressed to Babel about this subject, which the record's next review
	// must answer. It is lawful only on feedback and only beside a reason,
	// for the reason the record's own marker is: the question is the words.
	Question bool `json:"question,omitempty"`
}

func (in OperatorInput) validate() error {
	if !slices.Contains(OperatorKinds(), in.Kind) {
		if ValidRecordKind(in.Kind) {
			return fmt.Errorf("%w: a %s record is authored by a run, so an operator cannot record one",
				ErrInvalid, in.Kind)
		}
		return fmt.Errorf("%w: %q is not an operator-authored record kind", ErrInvalid, in.Kind)
	}
	if strings.TrimSpace(in.Operator) == "" {
		return fmt.Errorf("%w: an operator act must name its author", ErrInvalid)
	}
	if in.Kind == KindReconsiderDecision {
		if !slices.Contains(ReconsiderDecisions(), in.Decision) {
			return fmt.Errorf("%w: a reconsideration decision states reopen or retain, not %q",
				ErrInvalid, in.Decision)
		}
	} else if in.Decision != "" {
		return fmt.Errorf("%w: only a reconsideration decision carries a decision, not a %s",
			ErrInvalid, in.Kind)
	}
	if in.Stance != "" {
		if in.Kind != KindFeedback {
			return fmt.Errorf("%w: only feedback carries a reception stance, not a %s",
				ErrInvalid, in.Kind)
		}
		if !slices.Contains(Stances(), in.Stance) {
			return fmt.Errorf("%w: a reception states agree, disagree or unsure, not %q",
				ErrInvalid, in.Stance)
		}
	}
	if in.Question {
		if in.Kind != KindFeedback {
			return fmt.Errorf("%w: only feedback carries a question, not a %s", ErrInvalid, in.Kind)
		}
		if strings.TrimSpace(in.Reason) == "" {
			return fmt.Errorf("%w: a question is the words it asks, and this one has none", ErrInvalid)
		}
	}
	return nil
}

// ReconsiderInput raises one reconsider item.
//
// It is a separate entry point from OperatorInput because a reconsider item is
// run-authored: it reports that evidence or context changed, which is an
// observation. The decision that follows is the operator's, and only that
// decision reopens anything.
//
// RelatedID names the decided work being revisited - an operator decision, a
// verification, or the record that carried it. It is shape-validated rather
// than resolved, because the prior decision may live in internal/review or on
// another instance, and refusing to raise a reconsider item for work whose
// decision this store cannot see would hide exactly the change that matters.
type ReconsiderInput struct {
	Subject    Subject    `json:"subject"`
	RunID      string     `json:"run_id"`
	RelatedID  string     `json:"related_id"`
	Reason     string     `json:"reason"`
	Provenance Provenance `json:"provenance,omitzero"`
	Context    *Context   `json:"context,omitempty"`
}

func (in ReconsiderInput) validate() error {
	if err := in.Subject.validate(); err != nil {
		return err
	}
	if !validRunID.MatchString(in.RunID) {
		return fmt.Errorf("%w: reconsider run id %q is not a well-formed run identifier", ErrInvalid, in.RunID)
	}
	if !validID.MatchString(in.RelatedID) {
		return fmt.Errorf("%w: a reconsider item must link the decided work it revisits", ErrInvalid)
	}
	if strings.TrimSpace(in.Reason) == "" {
		return fmt.Errorf("%w: a reconsider item must say what materially changed", ErrInvalid)
	}
	if in.Context != nil {
		if err := in.Context.validate(); err != nil {
			return err
		}
	}
	return in.Provenance.validate()
}

// Resolver reads one evaluable artifact by its exact revision identity.
//
// It is an interface because the subject may be local or remote and this
// package must not care which: a second instance assessing a first instance's
// proposal resolves it through the same call, and a subject nothing can
// resolve is a refused write rather than an assessment of an artifact whose
// existence was assumed.
type Resolver interface {
	Artifact(ctx context.Context, subject Subject) (Artifact, error)
}

// validateEvidence refuses a citation that could not recover what it points
// at. frontier.NewEvidence already enforces this on construction, so what this
// catches is a decoded payload whose locator was truncated - which is a
// provenance claim with no provenance.
func validateEvidence(name string, items []frontier.Evidence) error {
	for i, item := range items {
		locator := item.Locator()
		if locator.Path == "" || locator.Digest == "" {
			return fmt.Errorf("%w: %s evidence %d has no recoverable locator", ErrInvalid, name, i)
		}
	}
	return nil
}

// baseRun reduces a job identity to the run it belongs to: everything before
// the first separator.
//
// internal/explore spells a challenger's or synthesizer's identity
// `<run>/<stage>`, and self-boost refusal has to see those as one run. A
// comparison against the compound id would let a run prefer its own freshly
// authored alternative merely by submitting from a different stage of itself.
func baseRun(runID string) string {
	if i := strings.IndexByte(runID, '/'); i >= 0 {
		return runID[:i]
	}
	return runID
}
