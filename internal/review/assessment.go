package review

import (
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// Mode is the durable-learning choice §4.7 requires every refinement worker to
// make before it produces anything: is this correction specific to the output
// that was rejected, or does it teach Babel something that should outlive it?
//
// The three values are not a spectrum. Each names a different set of
// descendants, and RecordRefinement refuses a set that does not match, because
// an assessment that says one thing while the run produces another is worse
// than no assessment: it would look like a considered judgement about durable
// learning while recording the opposite.
type Mode string

// The §4.7 assessment modes.
const (
	// ModeNone means the correction is specific to this output. The run
	// produces a revised descendant and nothing lasting.
	ModeNone Mode = "none"
	// ModeAlongside creates both a revised descendant and a separate
	// lasting-context proposal.
	ModeAlongside Mode = "alongside"
	// ModeInstead creates no replacement of the rejected output and
	// proposes only the lasting context. It is the mode for a rejection
	// whose real lesson is that the output should not have been attempted.
	ModeInstead Mode = "instead"
)

func (m Mode) valid() bool {
	switch m {
	case ModeNone, ModeAlongside, ModeInstead:
		return true
	}
	return false
}

// wantsRevision and wantsMemory are §4.7's rule, written once. Every check
// that a refinement produced the right descendants reads them, so the rule
// cannot be enforced differently in two places.
func (m Mode) wantsRevision() bool { return m == ModeNone || m == ModeAlongside }
func (m Mode) wantsMemory() bool   { return m == ModeAlongside || m == ModeInstead }

// Destination is where a proposed piece of lasting context would go. §4.7 is
// explicit that destinations are enumerated rather than freeform global
// memory, and this closed set is that sentence: a refinement worker cannot
// propose "remember this", only a named artifact that its destination's own
// review rules already govern.
type Destination string

// The §4.7 durable-learning destinations.
const (
	// DestinationRealityPlan is a Reality fact or focus-policy plan (§4.8).
	// It is the one destination whose acceptance is not this package's:
	// §4.7 routes it through the existing atomic operator-plan acceptance,
	// so accepting the proposal here authorizes the plan to be reviewed,
	// never the fact to become authoritative.
	DestinationRealityPlan Destination = "reality-plan"
	// DestinationCookbookChange is a change to a cookbook policy or lens.
	// §5 makes lenses cookbook assets, so one destination covers both; the
	// analyzer never edits its active cookbook (§5.7), which is exactly why
	// this is a proposal.
	DestinationCookbookChange Destination = "cookbook-change"
	// DestinationSkillDraft is a skill, runbook, or instruction draft.
	DestinationSkillDraft Destination = "skill-draft"
	// DestinationEffectivePattern is an effective-pattern note (§5.5's
	// eighth lens): a strategy worth repeating, with its enabling context.
	DestinationEffectivePattern Destination = "effective-pattern"
	// DestinationOperatorNote is a note for the operator that fits no other
	// destination.
	DestinationOperatorNote Destination = "operator-note"
)

func (d Destination) valid() bool {
	switch d {
	case DestinationRealityPlan, DestinationCookbookChange, DestinationSkillDraft,
		DestinationEffectivePattern, DestinationOperatorNote:
		return true
	}
	return false
}

// validSensitivity reports whether a classification is one of §4.5's three.
// internal/frontier owns the vocabulary and exports no validator, so this
// reads its constants rather than restating their strings.
func validSensitivity(c frontier.Classification) bool {
	switch c {
	case frontier.ClassificationPrivate,
		frontier.ClassificationRedactionRequired,
		frontier.ClassificationPublicSafe:
		return true
	}
	return false
}

// Assessment is §4.7's structured durable-learning assessment: what a
// refinement worker concluded about whether the reviewer's correction should
// outlive the output it corrected, recorded before any descendant exists.
//
// It is a record rather than a field on the refinement because §4.7 requires
// it to carry its own immutable ID and lineage. Accepting a revision must not
// implicitly accept the memory the assessment proposed, and the first step to
// making that true is that the assessment is not part of either.
type Assessment struct {
	ID string
	// RequestID is the authorized refinement request this answers. §4.7
	// gives every refinement worker the refusal and the reviewer's context;
	// this is the row that carries them.
	RequestID string
	// Subject is the record that was rejected.
	Subject       frontier.Ref
	Mode          Mode
	AgentID       string
	SchemaVersion int
	RecordedAt    time.Time
	Payload       AssessmentPayload
}

// AssessmentPayload is the §9 encryption-bound half of an assessment: a
// rationale about the corpus, an intended scope, supporting locators, and the
// destination a proposal would go to. Everything outside it — the identifiers,
// the mode, the schema version, the timestamp — is drawn from §9's plaintext
// allowlist.
type AssessmentPayload struct {
	// Rationale is why the worker chose this mode. §4.7 requires it, and it
	// is the part a reviewer reads first: the mode alone says what happened
	// but not why anyone should believe it.
	Rationale string `json:"rationale"`
	// Scope is the intended scope of the lasting context: what it would
	// apply to. For ModeNone it states how far the correction reaches,
	// which is the claim that mode makes.
	Scope string `json:"scope"`
	// Sensitivity is §4.7's sensitivity, in §4.5's classification
	// vocabulary rather than a second one.
	Sensitivity frontier.Classification `json:"sensitivity"`
	// Supporting is §4.7's supporting evidence. It is required whenever the
	// assessment proposes lasting context: a durable-learning proposal that
	// nothing in the archive supports is the model generalizing from one
	// correction, which §5.3 already refuses to let model output become.
	Supporting []frontier.Evidence `json:"supporting,omitempty"`
	// Destination is §4.7's proposed destination, set exactly when the mode
	// proposes lasting context.
	Destination Destination `json:"destination,omitempty"`
	// ContextIDs are the attributed operator contexts the worker was given
	// and read. They are recorded as guidance, and they are counted as
	// nothing else: an assessment citing contexts and no evidence is
	// refused with ErrContextIsNotEvidence.
	ContextIDs []string `json:"context_ids,omitempty"`
}

// validate enforces §4.7's rules about what an assessment must say, including
// the two that are easy to lose: a mode that proposes lasting context has to
// name where it would go and show what supports it, and a mode that does not
// must not smuggle a destination in anyway.
func (p AssessmentPayload) validate(mode Mode) error {
	if p.Rationale == "" {
		return errInvalid("assessment rationale is empty")
	}
	if p.Scope == "" {
		return errInvalid("assessment scope is empty")
	}
	if !validSensitivity(p.Sensitivity) {
		return fmt.Errorf("%w: assessment sensitivity %q", ErrInvalidValue, p.Sensitivity)
	}
	for i, ev := range p.Supporting {
		if ev.Locator().Path == "" || ev.Locator().Digest == "" {
			return fmt.Errorf("%w: supporting evidence %d cannot recover its bytes", ErrInvalidValue, i)
		}
	}
	if !mode.wantsMemory() {
		if p.Destination != "" {
			return fmt.Errorf("%w: mode %q proposes no lasting context but names destination %q",
				ErrInvalidValue, mode, p.Destination)
		}
		return nil
	}
	if !p.Destination.valid() {
		return fmt.Errorf("%w: assessment destination %q", ErrInvalidValue, p.Destination)
	}
	if len(p.Supporting) == 0 {
		if len(p.ContextIDs) > 0 {
			return fmt.Errorf("%w: assessment for mode %q cites %d operator context(s) and no evidence",
				ErrContextIsNotEvidence, mode, len(p.ContextIDs))
		}
		return fmt.Errorf("%w: assessment for mode %q proposes lasting context with no supporting evidence",
			ErrInvalidValue, mode)
	}
	return nil
}

// MemoryProposal is the lasting-context artifact an assessment proposes. §4.7
// makes it a reviewable output like any other: it follows its destination's
// normal evidence and review rules and remains a proposal until an operator
// explicitly accepts it.
//
// Nothing on this record says whether it was accepted. Status is derived from
// an append-only disposition history written only by DisposeMemory, which
// takes an Authority, so the refinement run that created the proposal has no
// way to also decide it.
type MemoryProposal struct {
	ID string
	// AssessmentID is the assessment that proposed it. One assessment
	// proposes at most one artifact, so this is unique.
	AssessmentID string
	// Destination and Sensitivity are copied from the assessment rather
	// than supplied again, so the proposal and the assessment that
	// justified it cannot disagree about where it would go or how sensitive
	// it is.
	Destination   Destination
	Sensitivity   frontier.Classification
	SchemaVersion int
	CreatedAt     time.Time
	Payload       MemoryPayload
	// Status is derived from the disposition history, not stored, so it
	// cannot drift from the events that justify it.
	Status frontier.ReviewStatus
}

// MemoryPayload is the §9 encryption-bound half of a durable-learning
// proposal: the context itself and the locators behind it.
type MemoryPayload struct {
	Title string `json:"title"`
	// Statement is the lasting context as the worker worded it. It is
	// stored verbatim for the same reason §5.2 keeps a candidate's original
	// wording: a reviewer decides on what was actually proposed.
	Statement string `json:"statement"`
	// Applicability is when and where the context would hold. §4.8 keeps
	// facts temporal, and a durable-learning note with no stated
	// applicability is the freeform global memory §4.7 rules out.
	Applicability string `json:"applicability,omitempty"`
	// Supporting carries the proposal's own locators. §4.7 makes it follow
	// its destination's normal evidence rules, and every destination's rule
	// starts with evidence that can be reopened.
	Supporting []frontier.Evidence `json:"supporting,omitempty"`
}

// MemoryInput is a durable-learning proposal as a refinement worker supplies
// it. Destination and sensitivity are absent on purpose: they come from the
// assessment.
type MemoryInput struct {
	Title         string
	Statement     string
	Applicability string
	Supporting    []frontier.Evidence
}

func (in MemoryInput) validate() error {
	if in.Title == "" {
		return errInvalid("durable-learning proposal title is empty")
	}
	if in.Statement == "" {
		return errInvalid("durable-learning proposal statement is empty")
	}
	if len(in.Supporting) == 0 {
		return errInvalid("durable-learning proposal has no supporting evidence")
	}
	for i, ev := range in.Supporting {
		if ev.Locator().Path == "" || ev.Locator().Digest == "" {
			return fmt.Errorf("%w: durable-learning supporting evidence %d cannot recover its bytes",
				ErrInvalidValue, i)
		}
	}
	return nil
}

// MemoryDisposition is one append-only decision about a durable-learning
// proposal. It is a separate event type from internal/frontier's disposition
// against an analysis record, and separateness is the requirement: §4.7 says
// accepting a revision never silently accepts the proposed memory or vice
// versa, and two event types in two tables is what makes that impossible
// rather than merely unintended.
type MemoryDisposition struct {
	ID       string
	MemoryID string
	// Sequence is per-proposal and strictly increasing, so history has a
	// total order even when two events share a timestamp.
	Sequence    int64
	Disposition frontier.Disposition
	// AuthorityID attributes the decision to the operator who made it.
	AuthorityID string
	// ContextID references the attributed operator context recorded with
	// the decision, if any.
	ContextID  string
	RecordedAt time.Time
	Note       string
}

// Refinement is one refinement request's recorded outcome: the assessment, and
// whichever descendants §4.7 lets that assessment's mode produce.
//
// The descendants are pointers because their absence is meaningful. A nil
// Revision under ModeInstead is §4.7's "creates no replacement of the rejected
// output", and a nil Memory under ModeNone is "the correction is specific to
// this output". Neither is a missing field.
type Refinement struct {
	ID         string
	RequestID  string
	Subject    frontier.Ref
	Mode       Mode
	AgentID    string
	RecordedAt time.Time
	Assessment Assessment
	Revision   *frontier.Ref
	Memory     *MemoryProposal
}

// RefinementInput records what a refinement run produced for one authorized
// request.
//
// It takes an Agent rather than an Authority, and that is the whole of §4.7's
// "may propose but never authorize": every field here is a proposal, and no
// method reachable from this type records a decision.
type RefinementInput struct {
	// Subject is the rejected record the request was authorized against.
	Subject frontier.Ref
	// RequestID is the refinement request being answered.
	RequestID string
	Agent     Agent
	Mode      Mode
	// Assessment is the structured durable-learning assessment §4.7
	// requires before any descendant is produced.
	Assessment AssessmentPayload
	// Revision names the revised descendant the run already created in the
	// frontier. It must be a revision of Subject: same kind, and its
	// ancestor is Subject, so a run cannot pass off an unrelated record as
	// a correction.
	Revision *frontier.Ref
	// Memory is the proposed lasting context.
	Memory *MemoryInput
}

// validateShape enforces §4.7's descendant rule before anything is written:
// `none` produces a revision alone, `alongside` a revision and a proposal,
// `instead` a proposal alone.
func (in RefinementInput) validateShape() error {
	if !in.Mode.valid() {
		return fmt.Errorf("%w: assessment mode %q", ErrInvalidValue, in.Mode)
	}
	if in.Mode.wantsRevision() && in.Revision == nil {
		return fmt.Errorf("%w: mode %q requires a revised descendant", ErrModeMismatch, in.Mode)
	}
	if !in.Mode.wantsRevision() && in.Revision != nil {
		return fmt.Errorf("%w: mode %q creates no replacement of the rejected output", ErrModeMismatch, in.Mode)
	}
	if in.Mode.wantsMemory() && in.Memory == nil {
		return fmt.Errorf("%w: mode %q requires a lasting-context proposal", ErrModeMismatch, in.Mode)
	}
	if !in.Mode.wantsMemory() && in.Memory != nil {
		return fmt.Errorf("%w: mode %q proposes no lasting context", ErrModeMismatch, in.Mode)
	}
	return nil
}
