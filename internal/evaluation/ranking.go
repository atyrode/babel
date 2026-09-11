package evaluation

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// This file is the operator's ordering, and it is deliberately separate from
// selection.go's.
//
// docs/evaluation-lifecycle.md §E5 requires the two to be built apart, and the
// reason is that they answer different questions. Selection asks where review
// attention is still useful, which is a question about what Babel does not know
// yet. Ranking asks what decision is worth the operator's attention now, which
// is a question about recorded work, pain, dependencies and permission — and
// SPEC §4.12 is explicit that reception is not the answer to it. One scoring
// function serving both would make the queue that spends compute and the queue
// that spends a person's afternoon the same list, which is exactly how a
// popularity signal becomes a work plan.
//
// Three rules constrain everything below.
//
// Recorded restrictions beat popularity. An excluded subject, a blocked
// dependency and an operator's explicit feedback each outrank a reception
// tally, because §E5 says explicit restrictions win and each allowance's actual
// semantics are preserved: `learn-only` demotes a remedy about the subject
// without excluding the subject from being learned from.
//
// The same votes may yield a different recommendation. Nothing here caches a
// score against a record: a score is computed from the votes *and* the context
// version they were cast under, so a changed current-work or pain reading
// re-ranks an item whose votes never moved — and says so in Reasons, with the
// provenance and freshness §E5 asks for.
//
// Unknown is a legal answer. A bare vote carries no rationale, and §E5 forbids
// requiring a model to fabricate one; so Reasons, Objections and WouldChange
// are each allowed to be empty, and an empty WouldChange renders as "not
// recorded" rather than as "nothing would change the recommendation".

// The sorts SPEC §8.5 names, plus the two lifecycle views it requires to be
// reachable.
const (
	// SortRecommended is the context-first ordering: recorded work, pain,
	// permission and outstanding obligations before reception.
	SortRecommended = "recommended"
	// SortRecent is §8.5's New: newest revision first, with no judgement in
	// it at all.
	SortRecent = "recent"
	// SortStrengthened is §8.5's Recently strengthened. It orders by the
	// newest *substantive* contribution — an assessment carrying argument or
	// new evidence, an operator criteria record, an outcome assessment — and
	// ignores bare votes entirely, because §8.5 defines the sort as
	// requiring substantive material rather than another vote.
	SortStrengthened = "strengthened"
	// SortContested surfaces disagreement: both sides present, or a
	// preponderance of recorded uncertainty.
	SortContested = "contested"
	// SortUnreviewed is §8.5's Under-reviewed: the largest outstanding
	// obligation first, oldest first within it.
	SortUnreviewed = "unreviewed"
	// SortOverdue orders by how long an activated obligation has been due.
	SortOverdue = "overdue"
	// SortReconsider lists the items a material change reopened for
	// consideration, newest change first.
	SortReconsider = "reconsider"
)

// Sorts lists the available orderings in a stable order.
func Sorts() []string {
	return []string{
		SortRecommended, SortRecent, SortStrengthened,
		SortContested, SortUnreviewed, SortOverdue, SortReconsider,
	}
}

// ValidSort reports whether sort is an ordering this build computes.
func ValidSort(sort string) bool { return slices.Contains(Sorts(), sort) }

// The weights of the recommended ordering.
//
// They are constants with reasons rather than tuned magic numbers, and the
// relations between them are what matter: any single recorded restriction
// outweighs the largest reception term, so no tally can promote work the
// operator restricted; and the reconsider and changed-context terms are smaller
// than a restriction but larger than a tally, so a material change moves an
// item without overriding a decision.
const (
	// weightPriority scales the ledger's recorded priority, which is a small
	// integer derived from lifecycle, ownership and deployment facts.
	weightPriority = 0.6
	// weightCurrentWork is the boost for a subject the operator records as
	// active work they own. It is the largest positive term because §E5 puts
	// recorded current work ahead of reception.
	weightCurrentWork = 1.5
	// weightPain scales recorded, entity-targeted friction: unresolved
	// Reality questions and disputed facts.
	weightPain = 0.3
	// weightObligation is the boost per outstanding activated obligation, so
	// a decision the operator cannot yet make on an unread item does not
	// outrank one they can.
	weightObligation = 0.4
	// weightReception scales the net reception fraction into [-0.5, 0.5]. It
	// is deliberately the smallest signed term in the function.
	weightReception = 0.5
	// weightChangedContext is the boost for an item whose recorded context
	// moved after its newest assessment: the votes are stale evidence about
	// a different situation.
	weightChangedContext = 0.7
	// weightStaleRevision is the boost for an item whose reviewed revision
	// has been superseded, which is the other way recorded opinion stops
	// describing the current wording.
	weightStaleRevision = 0.5
	// weightReconsider is the boost for an item a material change reopened.
	weightReconsider = 1.2
	// penaltyBlocked, penaltyExcluded and penaltyLearnOnly are the recorded
	// restrictions. Each is larger than the whole positive reception range,
	// which is the arithmetic form of "explicit restrictions win over
	// popularity".
	penaltyBlocked     = 3.0
	penaltyExcluded    = 4.0
	penaltyLearnOnly   = 1.0
	penaltyNotNow      = 1.0
	penaltyWrongRemedy = 1.5
	// penaltyGroupPosition demotes each further member of one alternative
	// group, so near-identical proposals cannot occupy the recommended view
	// by volume (§E5). It applies per position after the first.
	penaltyGroupPosition = 0.9
	// penaltyUnknownContext demotes an item whose context could not be read
	// at all. It is small: missing context is a gap to show, not a reason to
	// bury the record.
	penaltyUnknownContext = 0.2
)

// The feedback reasons §E5 names. They are suggestions rather than a closed
// vocabulary — the store keeps Reason as required free text — so these are the
// spellings the ranking recognises, and an unrecognised reason still demotes as
// a recorded objection without pretending to know which kind it is.
const (
	feedbackNotNow       = "not-now"
	feedbackWrongProblem = "wrong-problem"
	feedbackWrongRemedy  = "wrong-remedy"
)

// projected is one artifact with everything the read surfaces derive from it.
//
// It is the unit the projection stores and the unit every sort orders. Building
// it once per refresh is what makes a page read a bounded indexed query instead
// of a walk over the deployment's evaluation history: §E5 requires page reads
// not to scan or decrypt every evaluation, and the only way to honour that is
// for the scanning to have happened already.
type projected struct {
	Artifact  Artifact
	Reception Reception
	// Roles is one entry per applicable role, in RolesForKind order, so a
	// bare reception vote can never be rendered as a satisfied evidence
	// check.
	Roles []RoleCoverage
	// Required marks the roles something actually activated. A supported but
	// unactivated role is a visible gap and not an obligation: running all
	// six roles on every artifact is the mandatory deep-review policy the
	// product deliberately does not have.
	Required map[string]bool
	// Coverage is the combined overview over *activated* roles only, which
	// is why an item with reception votes and an unactivated evidence role
	// reads `reviewed` rather than claiming nobody has read it.
	Coverage       string
	CoverageReason string
	Lane           string
	// Group keys the alternatives addressing one problem together.
	Group        string
	GroupIndex   int
	Alternatives []Subject
	Reconsider   bool
	ReconsiderAt time.Time
	Outcomes     []outcomeState
	Feedback     []feedbackNote
	Reasons      []string
	Objections   []string
	WouldChange  []string
	Score        float64
	// StrengthenedAt is when substantive material last arrived. A bare vote
	// never moves it.
	StrengthenedAt time.Time
	LastReviewAt   time.Time
	// DueAt is the earliest moment an activated obligation became due, zero
	// when none is.
	DueAt time.Time
	// Overdue reports that an activated obligation has passed the policy's
	// overdue threshold.
	Overdue bool
	// ContextChanged reports that the recorded context moved after the
	// newest assessment, so the same votes now describe a different
	// situation.
	ContextChanged bool
	// StaleRevision reports that the reviewed revision has been superseded.
	StaleRevision bool
	// Assessments counts every completed assessment across roles, which is
	// what MaxItemReviews bounds.
	Assessments int
	// Names are the subject's recorded names, as resolveNames derived them.
	// They are stored so the worker's pre-launch expenditure gate can read
	// them off the assignment rather than asking for a review context in
	// order to decide whether to ask for one.
	Names []string
	// Reopened reports that an operator explicitly reopened this after a
	// material change. It is set only by a reconsider decision carrying the
	// `reopen` value: a retained decision resolves the reconsider item and
	// changes nothing about the prior disposition, and free text is never
	// parsed for the difference.
	Reopened bool
}

// outcomeState is one recorded implementation or outcome assessment.
//
// Contrary is not a correction of Verified and does not replace it. §E6
// requires contrary evidence to be displayed *beside* earlier verification with
// its scope and date rather than resolved by last-writer-wins, so both survive
// here and the lane derivation below reads the newest without deleting the
// older.
type outcomeState struct {
	RecordID    string    `json:"record_id"`
	Outcome     string    `json:"outcome"`
	CriteriaID  string    `json:"criteria_id,omitempty"`
	Environment string    `json:"environment,omitempty"`
	Uncertainty string    `json:"uncertainty,omitempty"`
	AsOf        time.Time `json:"as_of,omitzero"`
	// Contrary marks an outcome that contradicts an earlier one on the same
	// subject.
	Contrary bool `json:"contrary,omitempty"`
}

// feedbackNote is one scoped operator feedback reason.
//
// Subject is the exact revision the reason was given about, which is what lets
// a descendant addressing the refusal be judged on its new merits (§E5): the
// penalty follows the revision, not the chain.
type feedbackNote struct {
	RecordID string    `json:"record_id"`
	Subject  Subject   `json:"subject"`
	Operator string    `json:"operator,omitempty"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at,omitzero"`
	// RelatedID names the existing operator decision the reason is scoped
	// to, empty when it stands alone. Collecting the reason never creates a
	// disposition, so an unrelated reason is recorded and changes no
	// lifecycle state.
	RelatedID string `json:"related_id,omitempty"`
}

// The lifecycle lanes an item can be in.
//
// They do not replace the disposition vocabulary and are not allowed to: §8.5
// requires the existing duplicate and refine-requested dispositions to stay
// reachable with their relationships, so each maps to a lane of its own rather
// than collapsing into "rejected".
const (
	LaneOpen            = "open"
	LaneAccepted        = "accepted"
	LaneDeferred        = "deferred"
	LaneRejected        = "rejected"
	LaneDuplicate       = "duplicate"
	LaneRefineRequested = "refine-requested"
	LaneImplemented     = "implemented"
	LaneVerified        = "verified"
	LanePartial         = "partial"
	LaneContradicted    = "contradicted"
	LaneUnverifiable    = "unverifiable"
	LaneReconsider      = "reconsider"
)

// Lanes lists the lifecycle lanes in a stable order.
func Lanes() []string {
	return []string{
		LaneOpen, LaneAccepted, LaneDeferred, LaneRejected, LaneDuplicate,
		LaneRefineRequested, LaneImplemented, LaneVerified, LanePartial,
		LaneContradicted, LaneUnverifiable, LaneReconsider,
	}
}

// ValidLane reports whether lane is one this build derives.
func ValidLane(lane string) bool { return slices.Contains(Lanes(), lane) }

// CoverageFilters lists the coverage values a query may filter on.
//
// It is the canonical state vocabulary plus `overdue`, and it is derived from
// CoverageStates rather than restated so the two cannot drift. Overdue is a
// filter and never a state: RoleCoverage carries it as a boolean beside the
// state because an obligation can be reviewed and overdue at once, which a
// single-valued state cannot express.
func CoverageFilters() []string {
	return append(CoverageStates(), "overdue")
}

// ValidCoverageFilter reports whether value is a coverage filter.
func ValidCoverageFilter(value string) bool { return slices.Contains(CoverageFilters(), value) }

// deriveLane resolves an item's lifecycle lane.
//
// Precedence, highest first, and each step is a rule from the docs rather than
// a convenience:
//
//  1. An open Reconsider item wins, because §E6 requires a material change on
//     decided work to be visible as its own lane while the prior decision
//     stands. It does not erase the decision: the disposition is still in
//     Reasons and the history, and only an explicit operator action reopens it.
//  2. A recorded outcome wins over the review status, because once
//     implementation has been observed, "accepted" is no longer the most
//     specific true thing about the item. The newest outcome names the lane;
//     older ones and contrary evidence remain readable beside it.
//  3. Otherwise the operator's derived review status maps one-to-one, with
//     `new` reading as Open.
func deriveLane(item *projected) string {
	if item.Reconsider {
		return LaneReconsider
	}
	if newest, ok := newestOutcome(item.Outcomes); ok {
		switch newest.Outcome {
		case OutcomeImplemented:
			return LaneImplemented
		case OutcomeVerified:
			return LaneVerified
		case OutcomePartial:
			return LanePartial
		case OutcomeContradicted:
			return LaneContradicted
		case OutcomeUnverifiable:
			return LaneUnverifiable
		}
	}
	switch item.Artifact.ReviewStatus {
	case "accepted":
		return LaneAccepted
	case "deferred":
		return LaneDeferred
	case "rejected":
		return LaneRejected
	case "duplicate":
		return LaneDuplicate
	case "refine-requested":
		return LaneRefineRequested
	}
	return LaneOpen
}

// newestOutcome reports the most recent outcome assessment.
//
// Most recent by the assessment's own AsOf rather than by when the record was
// written, because §E6 makes the observation's time the scope of the claim: an
// outcome observed last month and recorded today describes last month.
func newestOutcome(outcomes []outcomeState) (outcomeState, bool) {
	var (
		best  outcomeState
		found bool
	)
	for _, outcome := range outcomes {
		if outcome.Outcome == "" {
			continue
		}
		if !found || outcome.AsOf.After(best.AsOf) ||
			(outcome.AsOf.Equal(best.AsOf) && outcome.RecordID > best.RecordID) {
			best, found = outcome, true
		}
	}
	return best, found
}

// deriveCoverage resolves the combined coverage overview over activated roles.
//
// Precedence is by how much the state obstructs a decision: an unsupported role
// is a gap in this build, a blocked one is a gap in permission, an unreviewed
// one is work nobody has done, a due one is work that needs doing again, and a
// not-applicable one is a closed question. Reviewed is what remains.
//
// Only activated roles participate. That is the difference between "this item
// has outstanding obligations" and "this item has not had every conceivable
// review", and reporting the second as the first would tell an operator that an
// item with two reception votes has never been read.
func deriveCoverage(item *projected) (string, string) {
	precedence := []string{
		CoverageUnsupported, CoverageBlocked, CoverageUnreviewed,
		CoverageDue, CoverageNotApplicable, CoverageReviewed,
	}
	byState := make(map[string]RoleCoverage, len(item.Roles))
	for _, role := range item.Roles {
		if !item.Required[role.Role] && role.State != CoverageUnsupported &&
			role.State != CoverageNotApplicable && role.State != CoverageBlocked {
			continue
		}
		if _, seen := byState[role.State]; !seen {
			byState[role.State] = role
		}
	}
	for _, state := range precedence {
		role, ok := byState[state]
		if !ok {
			continue
		}
		reason := role.Reason
		if reason == "" && requiresReason(state) {
			// The store requires a reason for these three states, and
			// an empty one here would be this package's bug rather
			// than an absent explanation. Saying so is better than
			// shipping a blank.
			reason = fmt.Sprintf("%s: %s role %q reports %s without a recorded reason",
				RoleApplicabilityPolicy, item.Artifact.Subject.Kind, role.Role, state)
		}
		return state, reason
	}
	// No applicable role at all: the kind itself is unsupported, which is a
	// visible gap and never a completed review.
	return CoverageUnsupported, KindUnreviewableReason(item.Artifact.Subject.Kind)
}

// requiresReason reports the coverage states that may not be reported bare.
func requiresReason(state string) bool {
	switch state {
	case CoverageUnsupported, CoverageBlocked, CoverageNotApplicable:
		return true
	}
	return false
}

// score computes the recommended ordering's value for one item.
//
// Every term appends its own sentence to Reasons when it fires, so the ordering
// explains itself on the page rather than in a comment. Two properties are
// worth stating outright:
//
// A restriction can drive the score negative and never removes the item. §4.8
// and §5.2 both forbid deleting a restricted subject, so an excluded item sorts
// last and stays readable with the reason it sorted there.
//
// Reception enters last and smallest. It is the only term derived from
// popularity, and §4.12 is explicit that reception is not evidence strength,
// corroboration, or a probability that the idea is correct.
func score(item *projected, policy Policy) float64 {
	ctx := item.Artifact.Context
	value := 0.0

	if ctx.Priority != 0 {
		value += weightPriority * float64(ctx.Priority)
		item.Reasons = append(item.Reasons, fmt.Sprintf(
			"recorded priority %+d from the Reality ledger", ctx.Priority))
	}
	if ctx.CurrentWork {
		value += weightCurrentWork
		item.Reasons = append(item.Reasons, "the operator records this subject as current work")
	}
	if ctx.Pain > 0 {
		value += weightPain * float64(ctx.Pain)
		item.Reasons = append(item.Reasons, fmt.Sprintf(
			"%d recorded unresolved question(s) or disputed fact(s) about this subject", ctx.Pain))
	}
	if ctx.Blocked {
		value -= penaltyBlocked
		item.Reasons = append(item.Reasons, "recorded policy does not permit subject-specific work here")
	}
	switch ctx.Allowance {
	case "excluded":
		value -= penaltyExcluded
		item.Reasons = append(item.Reasons,
			"the operator recorded this subject as excluded; it is kept and ranked last, not removed")
	case "learn-only":
		// learn-only is not "exclude from all learning". A remedy about
		// the subject is demoted; an observation or finding that learns
		// *from* it is not, which is the distinction §E5 requires each
		// allowance's semantics to preserve.
		if item.Artifact.Subject.Kind == SubjectKindProposal {
			value -= penaltyLearnOnly
			item.Reasons = append(item.Reasons,
				"recorded allowance is learn-only, so a remedy about this subject is demoted "+
					"while material learned from it is not")
		}
	}
	if len(ctx.Unknown) > 0 {
		value -= penaltyUnknownContext
		item.Reasons = append(item.Reasons, fmt.Sprintf(
			"%d piece(s) of context are missing or conflicting and stay visible rather than assumed",
			len(ctx.Unknown)))
	}

	if item.Reconsider {
		value += weightReconsider
		item.Reasons = append(item.Reasons,
			"a material change reopened this for consideration; the prior decision still stands")
	}
	if item.Reopened {
		value += weightReconsider
		item.Reasons = append(item.Reasons,
			"the operator explicitly reopened this after a material change")
	}
	if item.ContextChanged {
		value += weightChangedContext
		item.Reasons = append(item.Reasons,
			"the recorded work or reality behind this changed after its newest assessment, "+
				"so the existing votes describe a different situation")
	}
	if item.StaleRevision {
		value += weightStaleRevision
		item.Reasons = append(item.Reasons,
			"the reviewed revision has been superseded, so recorded opinion does not describe "+
				"the current wording")
	}
	if outstanding := item.outstanding(); outstanding > 0 {
		value += weightObligation * float64(outstanding)
		item.Reasons = append(item.Reasons, fmt.Sprintf(
			"%d activated review obligation(s) outstanding", outstanding))
	}

	for _, note := range item.Feedback {
		if note.Subject != item.Artifact.Subject {
			// The reason was given about a different revision. §E5
			// makes a descendant addressing an old refusal a record
			// judged on its new merits, so it is reported and not
			// charged.
			item.Reasons = append(item.Reasons, fmt.Sprintf(
				"a prior revision was refused (%s); this revision is judged on its own merits",
				note.Reason))
			continue
		}
		switch classifyFeedback(note.Reason) {
		case feedbackNotNow:
			value -= penaltyNotNow
		case feedbackWrongProblem, feedbackWrongRemedy:
			value -= penaltyWrongRemedy
		default:
			value -= penaltyNotNow
		}
		item.Objections = append(item.Objections, fmt.Sprintf("%s (recorded by %s)",
			note.Reason, operatorOrUnknown(note.Operator)))
	}

	if item.Reception.Reviews > 0 {
		net := float64(item.Reception.Support-item.Reception.Oppose) / float64(item.Reception.Reviews)
		value += weightReception * net
	}
	if item.GroupIndex > 0 {
		value -= penaltyGroupPosition * float64(item.GroupIndex)
		item.Reasons = append(item.Reasons, fmt.Sprintf(
			"one of %s's grouped alternatives; ranked %d within the group so near-identical "+
				"proposals cannot fill the view", item.Group, item.GroupIndex+1))
	}
	if !policy.Enabled {
		item.Reasons = append(item.Reasons,
			"authorized evaluation is disabled, so these counts are whatever was recorded before")
	}
	sortReasons(item)
	return value
}

// outstanding counts the activated obligations an item has not satisfied.
func (item *projected) outstanding() int {
	count := 0
	for _, role := range item.Roles {
		if !item.Required[role.Role] {
			continue
		}
		switch role.State {
		case CoverageUnreviewed, CoverageDue:
			count++
		}
	}
	return count
}

// classifyFeedback maps a recorded reason onto the ones §E5 names.
//
// It is prefix-insensitive matching over a suggestion list, not a validator:
// the store keeps Reason as required free text because the docs say "reasons
// such as", and an unrecognised reason is still an objection. What it must not
// do is invent a disposition — none of these changes a lifecycle state.
func classifyFeedback(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	for _, known := range []string{feedbackNotNow, feedbackWrongProblem, feedbackWrongRemedy} {
		if strings.HasPrefix(normalized, known) {
			return known
		}
	}
	return ""
}

// operatorOrUnknown renders an attribution that may be absent.
func operatorOrUnknown(operator string) string {
	if operator == "" {
		return "an unnamed operator"
	}
	return operator
}

// sortReasons puts an item's explanations in a stable order and drops
// duplicates, so two refreshes over unchanged inputs produce byte-identical
// projections and a page does not reshuffle its own prose.
func sortReasons(item *projected) {
	for _, list := range []*[]string{&item.Reasons, &item.Objections, &item.WouldChange} {
		sort.Strings(*list)
		*list = slices.Compact(*list)
	}
}

// rankAll orders the complete eligible set for one sort.
//
// The whole set, before any pagination. §E5 requires the represented eligible
// set to be ordered before it is paged, because a rank computed per page is not
// a rank at all: two pages of a locally sorted list can show the same record
// twice and omit another entirely.
//
// Every ordering ends in kind then id. A total order is what makes the stored
// positions reproducible, and reproducible positions are what make a snapshot a
// usable pagination contract.
func rankAll(items []projected, sortName string) ([]int, error) {
	if !ValidSort(sortName) {
		return nil, fmt.Errorf("%w: sort %q", ErrInvalid, sortName)
	}
	order := make([]int, len(items))
	for i := range order {
		order[i] = i
	}
	less := func(a, b int) bool { return false }
	switch sortName {
	case SortRecommended:
		less = func(a, b int) bool {
			if items[a].Score != items[b].Score {
				return items[a].Score > items[b].Score
			}
			return items[a].Artifact.CreatedAt.After(items[b].Artifact.CreatedAt)
		}
	case SortRecent:
		less = func(a, b int) bool {
			return items[a].Artifact.CreatedAt.After(items[b].Artifact.CreatedAt)
		}
	case SortStrengthened:
		less = func(a, b int) bool {
			// A record that was never strengthened has a zero time and
			// sorts last, which is the honest reading: this sort is
			// "what gained substantive material recently", and a bare
			// vote is not that.
			return items[a].StrengthenedAt.After(items[b].StrengthenedAt)
		}
	case SortContested:
		less = func(a, b int) bool {
			ca, cb := contestation(items[a]), contestation(items[b])
			if ca != cb {
				return ca > cb
			}
			return items[a].Score > items[b].Score
		}
	case SortUnreviewed:
		less = func(a, b int) bool {
			oa, ob := items[a].outstanding(), items[b].outstanding()
			if oa != ob {
				return oa > ob
			}
			// Oldest first within one obligation count: the reserved
			// coverage lane draws the oldest due work, and a sort
			// that disagreed with it would show an operator a queue
			// the worker is not working.
			return items[a].Artifact.CreatedAt.Before(items[b].Artifact.CreatedAt)
		}
	case SortOverdue:
		less = func(a, b int) bool {
			da, db := items[a].DueAt, items[b].DueAt
			switch {
			case da.IsZero() && db.IsZero():
				return false
			case da.IsZero():
				return false
			case db.IsZero():
				return true
			}
			return da.Before(db)
		}
	case SortReconsider:
		less = func(a, b int) bool {
			if items[a].Reconsider != items[b].Reconsider {
				return items[a].Reconsider
			}
			return items[a].ReconsiderAt.After(items[b].ReconsiderAt)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if less(a, b) {
			return true
		}
		if less(b, a) {
			return false
		}
		if items[a].Artifact.Subject.Kind != items[b].Artifact.Subject.Kind {
			return items[a].Artifact.Subject.Kind < items[b].Artifact.Subject.Kind
		}
		return items[a].Artifact.Subject.ID < items[b].Artifact.Subject.ID
	})
	return order, nil
}

// contestation measures recorded disagreement.
//
// Both sides present is the strong signal and is weighted as such; recorded
// uncertainty is the weaker one. Neither is a probability and neither is
// evidence strength: what this orders is "somebody should look at the argument",
// which is the one thing a split tally does say.
func contestation(item projected) float64 {
	support, oppose := float64(item.Reception.Support), float64(item.Reception.Oppose)
	both := math.Min(support, oppose)
	return 2*both + float64(item.Reception.Unsure)
}

// groupAlternatives keys the items that are alternative remedies for one
// problem, and orders each group.
//
// The key is the set of claims a proposal addresses, which is an asserted
// relation rather than a similarity measure: #114 makes competing remedies
// explicit by having each proposal name the hypotheses it answers, so two
// remedies for one claim group together without anybody comparing their text.
// Nothing is merged — §E5 requires individual votes, records and decisions to
// survive the grouping, so this only sets a key and a position.
//
// Items with no addressed claim are their own group, which is correct: an
// observation is not an alternative to anything, and a candidate proposal that
// addresses nothing would otherwise group with every other one.
func groupAlternatives(items []projected) {
	keys := make(map[string][]int)
	for i := range items {
		key := groupKey(items[i])
		items[i].Group = key
		keys[key] = append(keys[key], i)
	}
	for key, members := range keys {
		if len(members) < 2 {
			// A group of one is not a comparison. Clearing the key
			// keeps `Group` meaning "this has alternatives", which is
			// what a grouped view filters on.
			items[members[0]].Group = ""
			continue
		}
		sort.Slice(members, func(i, j int) bool {
			a, b := members[i], members[j]
			if items[a].Score != items[b].Score {
				return items[a].Score > items[b].Score
			}
			return items[a].Artifact.Subject.ID < items[b].Artifact.Subject.ID
		})
		for position, index := range members {
			items[index].GroupIndex = position
			items[index].Group = key
			for _, other := range members {
				if other == index {
					continue
				}
				items[index].Alternatives = append(items[index].Alternatives,
					items[other].Artifact.Subject)
			}
		}
	}
}

// groupKey is the identity of the problem an item addresses.
func groupKey(item projected) string {
	if item.Artifact.Subject.Kind != SubjectKindProposal {
		return "solo-" + item.Artifact.Subject.Kind + "-" + item.Artifact.Subject.ID
	}
	addressed := make([]string, 0, len(item.Artifact.Related))
	for _, related := range item.Artifact.Related {
		if related.Kind == SubjectKindHypothesis || related.Kind == SubjectKindFinding {
			addressed = append(addressed, related.Kind+":"+related.ID)
		}
	}
	if len(addressed) == 0 {
		return "solo-" + item.Artifact.Subject.Kind + "-" + item.Artifact.Subject.ID
	}
	sort.Strings(addressed)
	return "grp-" + digestOf(addressed...)
}

// explainWouldChange collects what the recorded assessments said would change
// their conclusion.
//
// It is assembled rather than generated, and the absence is preserved. §E5
// allows unknown explanations and forbids requiring a model to fabricate a
// rationale for a bare vote, so an item whose reviewers said nothing about what
// would change their minds has an empty list — which a renderer shows as "not
// recorded" and never as "nothing".
func explainWouldChange(item *projected, records []Record) {
	for _, record := range records {
		if record.Assessment == nil {
			continue
		}
		for _, contribution := range record.Assessment.Contributions {
			if text := strings.TrimSpace(contribution.WouldChange); text != "" {
				item.WouldChange = append(item.WouldChange, text)
			}
			if contribution.Kind == ContributionObjection {
				if text := strings.TrimSpace(contribution.Text); text != "" {
					item.Objections = append(item.Objections, text)
				}
			}
		}
		if uncertainty := strings.TrimSpace(record.Assessment.Uncertainty); uncertainty != "" {
			item.Reasons = append(item.Reasons, "recorded uncertainty: "+uncertainty)
		}
	}
	sortReasons(item)
}
