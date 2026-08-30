package reality

import (
	"fmt"
	"strings"
	"time"
)

// QuestionState is §4.8's question state machine, in full.
//
// It is derived from an append-only event history rather than stored as a
// mutable column, for the same reason a fact's status is: the sequence of
// states a question went through is the record of how reality was acquired,
// and an overwritten column would keep only the last of it.
type QuestionState string

// The §4.8 question states.
const (
	// QuestionOpen is asked and unanswered.
	QuestionOpen QuestionState = "open"
	// QuestionAnsweredUninterpreted holds a verbatim answer that has not
	// been interpreted, which §4.8 makes the retry state when
	// interpretation is unavailable or fails.
	QuestionAnsweredUninterpreted QuestionState = "answered-uninterpreted"
	// QuestionInterpreting is in the Answer Interpreter.
	QuestionInterpreting QuestionState = "interpreting"
	// QuestionPlanReady has an interpreted plan awaiting operator
	// acceptance. Nothing authoritative has been applied yet.
	QuestionPlanReady QuestionState = "plan-ready"
	// QuestionAnswered is disposed of: either a plan was accepted, or the
	// answer was `unknown` and there was nothing to interpret.
	QuestionAnswered QuestionState = "answered"
	// QuestionSnoozed is deferred by the operator without an answer.
	QuestionSnoozed QuestionState = "snoozed"
	// QuestionDeclined is refused by the operator. §4.8 has this suppress
	// repeats until materially new evidence exists.
	QuestionDeclined QuestionState = "declined"
	// QuestionObsolete no longer matters — its subject was retired, or the
	// work that depended on it was abandoned.
	QuestionObsolete QuestionState = "obsolete"
	// QuestionSuperseded was replaced by a better-formed question, which is
	// what a re-ask backed by materially new evidence produces.
	QuestionSuperseded QuestionState = "superseded"
)

func (s QuestionState) valid() bool {
	_, ok := questionTransitions[s]
	return ok
}

// questionTransitions is the state machine.
//
// Three edges are worth their justification. `plan-ready` back to
// `answered-uninterpreted` is what a rejected plan does: §4.8 keeps the raw
// answer for retry, and the answer is still perfectly good even when the
// interpretation of it was not. `declined` has no edge back to `open`, because
// §4.8's suppression is lifted by asking a new question backed by new
// evidence, not by silently reopening the one the operator refused — the
// refusal has to stay refused. And `answered` is not terminal, because a fact
// established by an answer can later be superseded or made obsolete, and the
// question that produced it should say so.
var questionTransitions = map[QuestionState][]QuestionState{
	QuestionOpen: {
		QuestionAnsweredUninterpreted, QuestionAnswered, QuestionSnoozed,
		QuestionDeclined, QuestionObsolete, QuestionSuperseded,
	},
	QuestionAnsweredUninterpreted: {
		QuestionInterpreting, QuestionSnoozed, QuestionObsolete, QuestionSuperseded,
	},
	QuestionInterpreting: {
		QuestionPlanReady, QuestionAnsweredUninterpreted, QuestionObsolete, QuestionSuperseded,
	},
	QuestionPlanReady: {
		QuestionAnswered, QuestionAnsweredUninterpreted, QuestionObsolete, QuestionSuperseded,
	},
	QuestionAnswered:   {QuestionObsolete, QuestionSuperseded},
	QuestionSnoozed:    {QuestionOpen, QuestionAnsweredUninterpreted, QuestionObsolete, QuestionSuperseded},
	QuestionDeclined:   {QuestionObsolete, QuestionSuperseded},
	QuestionObsolete:   {},
	QuestionSuperseded: {},
}

// canTransition reports whether §4.8 permits from -> to.
func canTransition(from, to QuestionState) bool {
	for _, allowed := range questionTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// suppresses reports whether a question in this state should silence an
// equivalent new ask. §4.8 names `declined` and `unknown` outcomes as the
// suppressing ones; a question that is merely still open suppresses a
// duplicate too, but that is deduplication rather than suppression and is
// reported as a different error.
func (s QuestionState) suppresses() bool {
	return s == QuestionDeclined
}

// live reports whether an equivalent ask would merely duplicate this question.
func (s QuestionState) live() bool {
	switch s {
	case QuestionOpen, QuestionAnsweredUninterpreted, QuestionInterpreting,
		QuestionPlanReady, QuestionSnoozed:
		return true
	}
	return false
}

// QuestionKind is why the question was asked. §4.8 enumerates the reasons, and
// they are a closed vocabulary rather than prose because the inbox groups by
// them and the dedup key includes them: two questions about one entity asked
// for different reasons are different questions.
type QuestionKind string

// The §4.8 question kinds.
const (
	KindAcquireContext  QuestionKind = "acquire-context"
	KindRefreshStale    QuestionKind = "refresh-stale"
	KindResolveConflict QuestionKind = "resolve-conflict"
	KindResolveEntity   QuestionKind = "resolve-entity"
	KindSetFocus        QuestionKind = "set-focus"
	KindClarifyAnswer   QuestionKind = "clarify-answer"
	KindFactCheckDrift  QuestionKind = "fact-check-drift"
)

func (k QuestionKind) valid() bool {
	switch k {
	case KindAcquireContext, KindRefreshStale, KindResolveConflict, KindResolveEntity,
		KindSetFocus, KindClarifyAnswer, KindFactCheckDrift:
		return true
	}
	return false
}

// QuestionClass is §4.8's blocking/maintenance/curiosity distinction. It is
// recorded and filterable but contributes nothing to the inbox score: §4.8
// ranks all three classes by the same five factors, and a class-dominated
// ranking would bury a security-relevant curiosity question under every
// blocking one.
type QuestionClass string

// The question classes.
const (
	ClassBlocking    QuestionClass = "blocking"
	ClassMaintenance QuestionClass = "maintenance"
	ClassCuriosity   QuestionClass = "curiosity"
)

func (c QuestionClass) valid() bool {
	switch c {
	case ClassBlocking, ClassMaintenance, ClassCuriosity:
		return true
	}
	return false
}

// WorkKind names the kind of dependent work a question blocks. The ledger does
// not own these records — they live in internal/frontier — so a reference is
// stored as a kind and an opaque ID rather than a foreign key across a
// component boundary.
type WorkKind string

// The dependent work kinds.
const (
	WorkHypothesis WorkKind = "hypothesis"
	WorkFinding    WorkKind = "finding"
	WorkProposal   WorkKind = "proposal"
	WorkRun        WorkKind = "run"
)

func (k WorkKind) valid() bool {
	switch k {
	case WorkHypothesis, WorkFinding, WorkProposal, WorkRun:
		return true
	}
	return false
}

// WorkRef is one piece of dependent work.
type WorkRef struct {
	Kind WorkKind
	ID   string
	// Blocking distinguishes work that cannot proceed without an answer
	// from work that would merely be better with one. §4.8's first ranking
	// factor is affected work, and conflating the two makes the factor
	// meaningless.
	Blocking bool
}

// FactRole says why a fact is attached to a question.
type FactRole string

// The fact roles.
const (
	// RoleExisting is a fact the question is about: the stale one to
	// refresh, or the one suspected of drift.
	RoleExisting FactRole = "existing"
	// RoleConflicting is a fact that contradicts another, which is the
	// input to a resolve-conflict question.
	RoleConflicting FactRole = "conflicting"
)

// QuestionEvent is one entry in a question's append-only state history.
type QuestionEvent struct {
	ID         string
	QuestionID string
	Sequence   int
	State      QuestionState
	// Actor is who caused the transition: an operator identity for a
	// decision, or the component name for a mechanical one such as entering
	// interpretation.
	Actor      string
	RecordedAt time.Time
	Payload    StatusPayload
}

// QuestionStateInput records an operator decision about a question: snoozing
// it, declining it, marking it obsolete, or reopening a snoozed one.
//
// The state machine is checked against the question's current state, so an
// illegal transition is refused rather than appended and ignored. Recording a
// decline through this path suppresses equivalent re-asks exactly as a
// `declined` answer outcome does — §4.8 treats the refusal, not the mechanism,
// as the thing that suppresses.
type QuestionStateInput struct {
	QuestionID string
	State      QuestionState
	Actor      string
	Note       string
}

// Question is one durable Reality Question. Its field list is §4.8's: target
// entities and predicates, why it was asked, dependent work, existing and
// conflicting facts, sensitivity, expected authority, and state.
type Question struct {
	ID            string
	SchemaVersion int
	Kind          QuestionKind
	Class         QuestionClass
	Sensitivity   Sensitivity
	// ExpectedAuthority is who could answer authoritatively. A question
	// whose expected authority is an observation is refused: it would be an
	// unanswerable question by §4.8's own authority rule.
	ExpectedAuthority AuthorityKind
	TargetEntityIDs   []string
	TargetPredicates  []Predicate
	DependentWork     []WorkRef
	ExistingFactIDs   []string
	ConflictFactIDs   []string
	// DedupeKey identifies the question's subject matter. It is a digest of
	// kind, canonical target entities and predicates, so an equivalent
	// re-ask is recognized without comparing prose.
	DedupeKey string
	// MaterialEvidence is what justifies asking now: fact IDs, locator
	// digests, or any opaque key the asker can reproduce. §4.8's
	// suppression is lifted by materially new evidence, and this set is
	// what "materially new" is measured against.
	MaterialEvidence []string
	// AvoidedCost is the investigation expense an answer would save, in
	// caller-supplied units. It is an input to ranking rather than
	// something this package can compute: only the caller knows what the
	// blocked work would have cost.
	AvoidedCost int
	// PromptedByID links the question this one supersedes, when new
	// evidence displaced a suppressed question.
	PromptedByID string
	CreatedAt    time.Time
	State        QuestionState
	Payload      QuestionPayload
}

// QuestionPayload is the §9 encryption-bound half of a question: the prose is
// about the corpus and the entities, which §9 does not allowlist.
type QuestionPayload struct {
	// Prompt is the question as it will be shown.
	Prompt string `json:"prompt"`
	// WhyAsked is §4.8's "why it was asked", in the asker's words.
	WhyAsked string `json:"why_asked"`
}

func (p QuestionPayload) validate() error {
	if strings.TrimSpace(p.Prompt) == "" {
		return fmt.Errorf("%w: question prompt is empty", ErrInvalidValue)
	}
	if strings.TrimSpace(p.WhyAsked) == "" {
		return fmt.Errorf("%w: question does not say why it was asked", ErrInvalidValue)
	}
	if err := checkNoCredential("question prompt", p.Prompt); err != nil {
		return err
	}
	return checkNoCredential("question rationale", p.WhyAsked)
}

// QuestionInput asks a question.
type QuestionInput struct {
	Kind              QuestionKind
	Class             QuestionClass
	Sensitivity       Sensitivity
	ExpectedAuthority AuthorityKind
	TargetEntityIDs   []string
	TargetPredicates  []Predicate
	DependentWork     []WorkRef
	ExistingFactIDs   []string
	ConflictFactIDs   []string
	MaterialEvidence  []string
	AvoidedCost       int
	Payload           QuestionPayload
}

func (in QuestionInput) validate() error {
	if !in.Kind.valid() {
		return fmt.Errorf("%w: question kind %q", ErrInvalidValue, in.Kind)
	}
	if !in.Class.valid() {
		return fmt.Errorf("%w: question class %q", ErrInvalidValue, in.Class)
	}
	if !in.Sensitivity.valid() {
		return fmt.Errorf("%w: sensitivity %q", ErrInvalidValue, in.Sensitivity)
	}
	if !in.ExpectedAuthority.valid() {
		return fmt.Errorf("%w: expected authority %q", ErrInvalidValue, in.ExpectedAuthority)
	}
	if !in.ExpectedAuthority.authorizes() {
		return fmt.Errorf("%w: expected authority %s cannot authorize an answer",
			ErrNotAuthoritative, in.ExpectedAuthority)
	}
	if len(in.TargetEntityIDs) == 0 {
		return fmt.Errorf("%w: question names no target entity", ErrInvalidValue)
	}
	for _, p := range in.TargetPredicates {
		if !p.valid() {
			return fmt.Errorf("%w: predicate %q", ErrInvalidValue, p)
		}
	}
	for _, w := range in.DependentWork {
		if !w.Kind.valid() || w.ID == "" {
			return fmt.Errorf("%w: dependent work %q/%q", ErrInvalidValue, w.Kind, w.ID)
		}
	}
	if in.AvoidedCost < 0 {
		return fmt.Errorf("%w: avoided cost is negative", ErrInvalidValue)
	}
	return in.Payload.validate()
}

// dedupeKey derives the question's subject-matter key from its canonical
// targets. The caller's entity IDs are resolved first, so a question about a
// merged-away identity dedupes against one about the identity it merged into.
func dedupeKey(kind QuestionKind, entityIDs []string, predicates []Predicate) string {
	parts := []string{string(kind)}
	parts = append(parts, sortedUnique(entityIDs)...)
	names := make([]string, 0, len(predicates))
	for _, p := range predicates {
		names = append(names, string(p))
	}
	parts = append(parts, sortedUnique(names)...)
	return digestKey("babel/reality/question", parts...)
}

// AnswerOutcome is what an answer amounts to. §4.8 singles out `unknown` and
// `declined` as suppressing repeats, so they are outcomes rather than states
// inferred from the text.
type AnswerOutcome string

// The answer outcomes.
const (
	// OutcomeAnswered is a substantive answer, which goes on to
	// interpretation.
	OutcomeAnswered AnswerOutcome = "answered"
	// OutcomeUnknown is the operator stating they do not know. There is
	// nothing to interpret, and the question is disposed of as answered.
	OutcomeUnknown AnswerOutcome = "unknown"
	// OutcomeDeclined is the operator refusing to answer, which declines
	// the question and suppresses equivalent re-asks.
	OutcomeDeclined AnswerOutcome = "declined"
)

func (o AnswerOutcome) valid() bool {
	switch o {
	case OutcomeAnswered, OutcomeUnknown, OutcomeDeclined:
		return true
	}
	return false
}

// Answer is one attributed immutable answer event. §4.8 requires the answer to
// be retained verbatim, so the text is stored exactly as supplied — no
// trimming, no normalization, no rendering — and the row is immutable.
//
// Verbatim retention is also why the text is provenance and never a prompt:
// §4.8 forbids using freeform answer text as an unparsed global memory prompt,
// and the only thing that reads it is the interpreter, with the question and
// context snapshot alongside it.
type Answer struct {
	ID            string
	QuestionID    string
	SchemaVersion int
	Sequence      int
	// Author is the attributed operator identity. An unattributed answer is
	// not an answer §4.8 can treat as authority.
	Author     string
	At         time.Time
	RecordedAt time.Time
	Outcome    AnswerOutcome
	// ContextID links attributed guidance supplied with the answer.
	ContextID string
	Payload   AnswerPayload
}

// AnswerPayload is the §9 encryption-bound half of an answer: operator prose,
// which §9 names among the sensitive payloads outright.
type AnswerPayload struct {
	// Text is the answer exactly as the operator wrote it.
	Text string `json:"text"`
}

// AnswerInput records an answer.
type AnswerInput struct {
	QuestionID string
	Author     string
	At         time.Time
	Outcome    AnswerOutcome
	Text       string
	ContextID  string
}

func (in AnswerInput) validate() error {
	if in.QuestionID == "" {
		return fmt.Errorf("%w: answer names no question", ErrInvalidValue)
	}
	if in.Author == "" {
		return fmt.Errorf("%w: answer has no author", ErrInvalidValue)
	}
	if in.At.IsZero() {
		return fmt.Errorf("%w: answer timestamp is zero", ErrInvalidValue)
	}
	if !in.Outcome.valid() {
		return fmt.Errorf("%w: answer outcome %q", ErrInvalidValue, in.Outcome)
	}
	if in.Outcome == OutcomeAnswered && strings.TrimSpace(in.Text) == "" {
		return fmt.Errorf("%w: a substantive answer has no text", ErrInvalidValue)
	}
	// The answer text is deliberately not credential-checked. §4.8 requires
	// it to be retained verbatim, and refusing an operator's own words
	// would lose the answer entirely; what the ledger refuses is a
	// credential entering a *fact*, which is where the plan's assertions
	// are checked.
	return nil
}

// nextState reports the question state an answer with this outcome produces.
func (o AnswerOutcome) nextState() QuestionState {
	switch o {
	case OutcomeUnknown:
		return QuestionAnswered
	case OutcomeDeclined:
		return QuestionDeclined
	}
	return QuestionAnsweredUninterpreted
}

// suppressesRepeats reports whether §4.8 has this outcome silence equivalent
// re-asks until materially new evidence exists.
func (o AnswerOutcome) suppressesRepeats() bool {
	return o == OutcomeUnknown || o == OutcomeDeclined
}

// ActionKind is the closed vocabulary of what an Answer Interpreter plan may
// propose. §4.8 lists exactly these, and the list's closure is the guarantee
// that matters: there is no action that creates a proposal and none that
// publishes anything, so a plan cannot bypass hypothesis → observation →
// finding → proposal (§4.2, §4.6, decision 13) however it is composed.
type ActionKind string

// The §4.8 plan actions.
const (
	ActionAssertFact           ActionKind = "assert-fact"
	ActionSupersedeFact        ActionKind = "supersede-fact"
	ActionDisputeFact          ActionKind = "dispute-fact"
	ActionMergeEntities        ActionKind = "merge-entities"
	ActionSplitEntity          ActionKind = "split-entity"
	ActionChangeFocus          ActionKind = "change-focus-policy"
	ActionCreateHypothesis     ActionKind = "create-hypothesis"
	ActionRequestInvestigation ActionKind = "request-investigation"
	ActionRequestRefinement    ActionKind = "request-refinement"
	ActionAskFollowUp          ActionKind = "ask-follow-up"
	ActionNone                 ActionKind = "no-action"
)

// ActionKinds lists the vocabulary in a stable order, so a UI or a test can
// enumerate it rather than restating it.
func ActionKinds() []ActionKind {
	return []ActionKind{
		ActionAssertFact, ActionSupersedeFact, ActionDisputeFact,
		ActionMergeEntities, ActionSplitEntity, ActionChangeFocus,
		ActionCreateHypothesis, ActionRequestInvestigation,
		ActionRequestRefinement, ActionAskFollowUp, ActionNone,
	}
}

func (k ActionKind) valid() bool {
	for _, known := range ActionKinds() {
		if known == k {
			return true
		}
	}
	return false
}

// RequiresAcceptance reports whether §4.8 makes this action wait for one
// explicit operator acceptance.
//
// The rule is exactly §4.8's: any fact, entity-resolution, or focus-policy
// mutation requires acceptance; non-authoritative descendants — hypotheses,
// follow-up questions, and requests into the normal pipeline — may be retained
// immediately, because none of them asserts anything about reality.
func (k ActionKind) RequiresAcceptance() bool {
	switch k {
	case ActionAssertFact, ActionSupersedeFact, ActionDisputeFact,
		ActionMergeEntities, ActionSplitEntity, ActionChangeFocus:
		return true
	}
	return false
}

// ActionState is what has become of one action.
type ActionState string

// The action states.
const (
	// ActionPendingAcceptance is an authoritative action waiting for the
	// operator. Nothing in the ledger reflects it yet.
	ActionPendingAcceptance ActionState = "pending-acceptance"
	// ActionRetained is a non-authoritative descendant already persisted.
	ActionRetained ActionState = "retained"
	// ActionApplied is an authoritative action the operator accepted.
	ActionApplied ActionState = "applied"
	// ActionRejected is an action whose plan the operator rejected.
	ActionRejected ActionState = "rejected"
)

// Action is one step of a plan. Exactly one of the payload's option fields is
// meaningful, chosen by Kind; validation refuses the rest, so an action cannot
// smuggle a fact assertion inside a follow-up question.
type Action struct {
	ID       string
	PlanID   string
	Position int
	Kind     ActionKind
	State    ActionState
	// ResultID is what the action produced once retained or applied: a fact
	// ID, a resolution ID, a hypothesis ID, a question ID, or a request ID.
	ResultID  string
	AppliedAt time.Time
	Payload   ActionPayload
}

// ActionPayload is the §9 encryption-bound half of an action: it carries claim
// text, entity names, and the interpreter's reasoning.
type ActionPayload struct {
	// Rationale is the interpreter's stated reason, shown to the operator
	// with the plan. §4.8 requires the plan to be displayed before
	// acceptance, and an action with no reason is not reviewable.
	Rationale string `json:"rationale"`

	// Fact is the assertion for assert-fact and supersede-fact.
	Fact *FactInput `json:"fact,omitempty"`
	// PriorFactID is the revision supersede-fact replaces.
	PriorFactID string `json:"prior_fact_id,omitempty"`
	// DisputeFactIDs are the contradicting facts for dispute-fact.
	DisputeFactIDs []string `json:"dispute_fact_ids,omitempty"`
	// Merge and Split are the entity-resolution actions.
	Merge *MergeInput `json:"merge,omitempty"`
	Split *SplitInput `json:"split,omitempty"`
	// FocusRules is the new policy version for change-focus-policy. A
	// policy change installs a version rather than editing one, because a
	// version's bytes are what makes a past decision explainable.
	FocusRules *FocusRuleSet `json:"focus_rules,omitempty"`
	// Hypothesis is the candidate for create-hypothesis.
	Hypothesis *HypothesisDraft `json:"hypothesis,omitempty"`
	// FollowUp is the question for ask-follow-up.
	FollowUp *QuestionInput `json:"follow_up,omitempty"`
	// Request is the investigation or refinement request.
	Request *RequestDraft `json:"request,omitempty"`
}

// HypothesisDraft is a candidate hypothesis an interpretation produced. It is
// deliberately the frontier's vocabulary rather than a new one: the frontier
// owns candidates, and this package hands one over rather than storing a
// parallel copy of it.
type HypothesisDraft struct {
	RunID             string   `json:"run_id"`
	Statement         string   `json:"statement"`
	OriginCues        []string `json:"origin_cues,omitempty"`
	ProvisionalLabels []string `json:"provisional_labels,omitempty"`
}

// RequestKind distinguishes the two requests a plan may record.
type RequestKind string

// The request kinds.
const (
	// RequestInvestigation is §4.8's "request to investigate an issue-shaped
	// output through the normal evidence pipeline". It is a request, not an
	// output: it names the hypothesis the investigation starts from, and it
	// cannot name a destination, because rendering a destination is §4.6's
	// export step and happens only after review.
	RequestInvestigation RequestKind = "investigation"
	// RequestRefinement is a request that existing work be refined. §4.7
	// requires a refinement to be authorized by a recorded rejection, which
	// this package cannot create — it has no review authority — so the
	// request is recorded here for the review pipeline to authorize.
	RequestRefinement RequestKind = "refinement"
)

func (k RequestKind) valid() bool {
	return k == RequestInvestigation || k == RequestRefinement
}

// RequestDraft is a recorded request into the normal pipeline.
type RequestDraft struct {
	Kind RequestKind `json:"kind"`
	// HypothesisID is the candidate the investigation starts from, required
	// for an investigation request: an investigation with no hypothesis is
	// the bypass §4.2's mandatory path forbids.
	HypothesisID string `json:"hypothesis_id,omitempty"`
	// SubjectKind and SubjectID are the work to refine, required for a
	// refinement request.
	SubjectKind WorkKind `json:"subject_kind,omitempty"`
	SubjectID   string   `json:"subject_id,omitempty"`
	Guidance    string   `json:"guidance"`
}

// Request is a recorded, unapplied request. It exists so a plan's
// pipeline-bound descendants are durable without this package pretending to
// authority it does not have: nothing here starts an investigation or refines
// anything, and §4.6's publication boundary is untouched.
type Request struct {
	ID           string
	ActionID     string
	Kind         RequestKind
	HypothesisID string
	SubjectKind  WorkKind
	SubjectID    string
	CreatedAt    time.Time
	Payload      RequestPayload
}

// RequestPayload is the §9 encryption-bound half of a request.
type RequestPayload struct {
	Guidance string `json:"guidance"`
}

// PlanState is a plan's disposition.
type PlanState string

// The plan states.
const (
	PlanProposed PlanState = "proposed"
	PlanAccepted PlanState = "accepted"
	PlanRejected PlanState = "rejected"
)

// Plan is one Answer Interpreter output: a structured multi-action proposal.
// It is immutable, and it is never authority — §4.8 requires one explicit
// operator acceptance before any of its authoritative actions touch reality.
type Plan struct {
	ID            string
	QuestionID    string
	AnswerID      string
	SchemaVersion int
	// InterpreterVersion is the versioned Code→OMP interpreter that
	// produced it, which §4.8 requires to be versioned so a plan's origin
	// stays identifiable after the interpreter changes.
	InterpreterVersion int
	CreatedAt          time.Time
	State              PlanState
	Actions            []Action
	Payload            PlanPayload
}

// PlanPayload is the §9 encryption-bound half of a plan: the interpreter's
// summary is model text.
type PlanPayload struct {
	Summary string `json:"summary"`
}

// PlanInput records an interpretation.
type PlanInput struct {
	QuestionID         string
	AnswerID           string
	InterpreterVersion int
	Summary            string
	Actions            []ActionPayload
	Kinds              []ActionKind
}

// AcceptanceInput is the one explicit operator acceptance §4.8 requires.
type AcceptanceInput struct {
	PlanID string
	// Actor is the attributed operator identity. Acceptance is the act that
	// turns an interpretation into reality, so it is never anonymous.
	Actor string
	// ContextID links attributed guidance the operator gave while
	// accepting.
	ContextID string
	Note      string
}

// Acceptance is the recorded acceptance event.
type Acceptance struct {
	ID         string
	PlanID     string
	Actor      string
	ContextID  string
	RecordedAt time.Time
	Payload    StatusPayload
}

// Application reports what an acceptance applied, so a caller does not have to
// re-read the plan to learn which rows appeared.
type Application struct {
	FactIDs       []string
	DisputeIDs    []string
	ResolutionIDs []string
	// FocusVersions are the focus rule set versions the acceptance
	// installed.
	FocusVersions []int
	// QuestionState is the question's state after the atomic commit, which
	// §4.8 requires to move with the acceptance rather than after it.
	QuestionState QuestionState
}

// Retained reports what a plan retained immediately, before any acceptance.
// §4.8 permits exactly these — non-authoritative descendants — and naming them
// separately from Application is what keeps the distinction visible at the API
// rather than only in the schema.
type Retained struct {
	HypothesisIDs []string
	QuestionIDs   []string
	RequestIDs    []string
}

// InboxItem is one ranked question with the arithmetic that ranked it.
//
// The score's terms are returned rather than only the total, because §4.8's
// factors are a policy an operator will want to argue with, and a bare number
// cannot be argued with.
type InboxItem struct {
	Question Question
	Score    int
	// Terms are the five §4.8 factors' contributions, keyed by factor name.
	Terms map[string]int
}

// The inbox weights implement §4.8's ranking: affected work, avoided
// investigation cost, dependency count, staleness, and security/disclosure
// impact. They are integers so the ordering is exactly reproducible — a
// floating-point score would make two runs of the same inbox disagree on ties
// — and they are relative rather than absolute magnitudes.
//
// Blocked work dominates because a question that stops work in progress is the
// one whose answer is worth most now. Security is next, because a disclosure
// decision made without it is the expensive mistake. Avoided cost is scaled
// down hard: it is a caller-supplied estimate in arbitrary units, and letting
// an estimate outrank a real blockage would be trusting the estimate more than
// it deserves.
const (
	weightBlockedWork = 40
	weightSecurity    = 25
	weightDependency  = 8
	weightStaleness   = 5
	weightAvoidedCost = 1
	// stalenessCap bounds the staleness term so a question about a fact
	// that expired two years ago does not permanently outrank everything.
	stalenessCap = 30
)

// InboxQuery selects and ranks the inbox.
type InboxQuery struct {
	// AsOf is the instant staleness is measured from, defaulting to now.
	AsOf time.Time
	// Class optionally narrows to one class. The ranking itself is
	// class-blind, so this is a filter for a UI tab rather than part of the
	// policy.
	Class QuestionClass
	// Limit bounds the result, zero meaning no bound.
	Limit int
}
