// Package frontier stores Babel's durable hypothesis frontier and the four
// analysis record types of SPEC.md §4.2 through §4.5, together with the
// append-only review dispositions of §4.7.
//
// Three properties shape everything here.
//
// Nothing is ever deleted. §5.2 requires every candidate and its origin to
// persist, and §4.7 states that rejection never deletes a record, so this
// package exposes no delete operation for any record type. A reviewer's
// decision is an appended event; a correction is a descendant revision that
// leaves its ancestor byte-identical. The frontier can only grow.
//
// The development path is mandatory. §4.2 permits a candidate to develop only
// through hypothesis -> one or more observations -> finding -> proposal, and
// §4.3 states that an observation cannot exist without evidence. Those are not
// warnings here: an evidence-free observation, a finding with no observations,
// and a proposal with no finding are each a refused write, enforced in the
// constructors, in foreign keys, and — for the evidence rule — in a column
// constraint that survives §9 payload encryption.
//
// Storage is split for §9. Every record carries exactly one Payload field
// holding the material §9 requires to be sealed in a randomized AEAD envelope
// before it leaves the machine: claims, titles, reviewer notes, evidence
// locators, and model-produced scores. Every other field is drawn from §9's
// plaintext allowlist — structured identifiers, entity kind and schema
// version, relationship IDs, counts, lifecycle state, and timestamps — and may
// travel to PostgreSQL in the clear. Each table mirrors that split as one
// payload_json column plus allowlisted columns, so the later sync slice
// replaces a single column per table rather than auditing a field list.
package frontier

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/event"
)

// RecordSchema is the version stamped on every record this build writes. §9
// requires every row to carry a schema version so a later reader can tell
// which shape it is decoding; it is plaintext-eligible because it describes
// the envelope rather than its contents.
const RecordSchema = 1

// Sentinel errors for the invariants callers are expected to handle rather
// than merely report. They are distinguished from each other because refusing
// a write for the wrong reason would be indistinguishable from a bug.
var (
	// ErrNoEvidence reports §4.3's rule that an observation cannot exist
	// without evidence.
	ErrNoEvidence = errors.New("observation requires at least one evidence locator")
	// ErrNoObservations reports §4.2's rule that a finding is consolidated
	// from observations and never skips them.
	ErrNoObservations = errors.New("finding requires at least one observation")
	// ErrNoFindings reports §4.5's rule that a proposal is suggested by one
	// or more findings.
	ErrNoFindings = errors.New("proposal requires at least one finding")
	// ErrCounterEvidenceUnstated reports that counter-evidence was neither
	// listed nor explicitly declared absent, which §4.3 and §4.4 require to
	// be a stated position rather than an empty field.
	ErrCounterEvidenceUnstated = errors.New("counter-evidence must be listed or explicitly declared absent")
	// ErrInvalidLocator reports evidence whose locator could not recover the
	// bytes behind it, which would make the claim unverifiable.
	ErrInvalidLocator = errors.New("evidence locator cannot recover its bytes")
	// ErrUnknownEntity reports a reference to a record this store does not
	// hold. Records are never deleted, so this always means the reference was
	// wrong rather than that the target went away.
	ErrUnknownEntity = errors.New("unknown entity")
	// ErrNotReviewable reports a disposition aimed at something §6.7 does not
	// make reviewable. Observations are evidence, not review subjects.
	ErrNotReviewable = errors.New("entity is not reviewable")
	// ErrInvalidValue reports a value outside a closed vocabulary this
	// package controls.
	ErrInvalidValue = errors.New("invalid value")
	// ErrImmutable reports an attempt to change a stored record in place.
	ErrImmutable = errors.New("records are immutable revisions")
)

// Status is a candidate hypothesis's exploration lifecycle from §4.2. It is
// deliberately separate from a reviewer's disposition: status says what
// exploration will do with the candidate next, and §5.2 is explicit that
// sorting and deferral never remove it.
type Status string

// The §4.2 statuses. StatusDeferred is the one a finite run leaves behind:
// §5.2 requires the unexplored remainder to be deferred rather than erased.
const (
	StatusUntriaged     Status = "untriaged"
	StatusQueued        Status = "queued"
	StatusInvestigating Status = "investigating"
	StatusDeferred      Status = "deferred"
	StatusRejected      Status = "rejected"
	StatusPromoted      Status = "promoted"
)

func (s Status) valid() bool {
	switch s {
	case StatusUntriaged, StatusQueued, StatusInvestigating, StatusDeferred, StatusRejected, StatusPromoted:
		return true
	}
	return false
}

// LinkType is a typed relationship between two hypotheses (§4.2). A link
// records how two ideas relate and never implies a status change: superseding
// a hypothesis states a relationship, and removing the superseded candidate
// from exploration remains a separate, explicit status event.
type LinkType string

// The §4.2 link types.
const (
	LinkDerivedFrom  LinkType = "derived-from"
	LinkCorroborates LinkType = "corroborates"
	LinkContradicts  LinkType = "contradicts"
	LinkSupersedes   LinkType = "supersedes"
	LinkSameConcept  LinkType = "same-concept"
)

func (l LinkType) valid() bool {
	switch l {
	case LinkDerivedFrom, LinkCorroborates, LinkContradicts, LinkSupersedes, LinkSameConcept:
		return true
	}
	return false
}

// Disposition is an operator review decision (§4.7). The vocabulary is closed
// at four values on purpose: §4.7 states there is no standalone `refine`
// disposition, because a refinement must be authorized by a recorded
// rejection rather than standing alone.
type Disposition string

// The §4.7 dispositions.
const (
	DispositionAccept    Disposition = "accept"
	DispositionReject    Disposition = "reject"
	DispositionDefer     Disposition = "defer"
	DispositionDuplicate Disposition = "duplicate"
)

func (d Disposition) valid() bool {
	switch d {
	case DispositionAccept, DispositionReject, DispositionDefer, DispositionDuplicate:
		return true
	}
	return false
}

// ReviewStatus is §4.5's proposal review state. It is derived from the
// append-only disposition history rather than stored, so it cannot drift from
// the events that justify it, and `refine-requested` cannot be set without the
// reject event and refinement request that authorize it.
type ReviewStatus string

// The §4.5 review statuses.
const (
	ReviewNew             ReviewStatus = "new"
	ReviewAccepted        ReviewStatus = "accepted"
	ReviewRejected        ReviewStatus = "rejected"
	ReviewDeferred        ReviewStatus = "deferred"
	ReviewDuplicate       ReviewStatus = "duplicate"
	ReviewRefineRequested ReviewStatus = "refine-requested"
)

// EntityType names a record kind. §9 lists entity kind in the PostgreSQL
// plaintext allowlist, so it is safe to store and index unsealed.
type EntityType string

// The record kinds this package stores.
const (
	EntityHypothesis  EntityType = "hypothesis"
	EntityObservation EntityType = "observation"
	EntityFinding     EntityType = "finding"
	EntityProposal    EntityType = "proposal"
)

func (e EntityType) valid() bool {
	switch e {
	case EntityHypothesis, EntityObservation, EntityFinding, EntityProposal:
		return true
	}
	return false
}

// reviewable reports whether §6.7 allows a disposition against this kind.
// Observations are excluded: they are the evidence a finding consolidates,
// not an artifact an operator accepts or rejects.
func (e EntityType) reviewable() bool {
	switch e {
	case EntityHypothesis, EntityFinding, EntityProposal:
		return true
	}
	return false
}

// Ref addresses one record. It pairs the kind with the ID because dispositions
// and refinement requests point at several tables and a bare ID would not say
// which one.
type Ref struct {
	Type EntityType
	ID   string
}

// Confidence and Impact are coarse model-supplied gradings. They are three
// valued rather than numeric because §10 warns that confidence never
// substitutes for evidence, and a spurious decimal invites exactly that.
type Confidence string

// The confidence gradings.
const (
	ConfidenceLow      Confidence = "low"
	ConfidenceModerate Confidence = "moderate"
	ConfidenceHigh     Confidence = "high"
)

func (c Confidence) valid() bool {
	switch c {
	case ConfidenceLow, ConfidenceModerate, ConfidenceHigh:
		return true
	}
	return false
}

// Impact grades how much a claim would matter if it held.
type Impact string

// The impact gradings.
const (
	ImpactLow      Impact = "low"
	ImpactModerate Impact = "moderate"
	ImpactHigh     Impact = "high"
)

func (i Impact) valid() bool {
	switch i {
	case ImpactLow, ImpactModerate, ImpactHigh:
		return true
	}
	return false
}

// TemporalStatus is §5.4's distinction between what a conversation claimed and
// what is observable now. The empty value means the question was not assessed,
// which is different from `unverifiable`, where it was assessed and no answer
// was reachable.
type TemporalStatus string

// The §5.4 temporal statuses.
const (
	TemporalHistorical      TemporalStatus = "historical"
	TemporalStillApplicable TemporalStatus = "still-applicable"
	TemporalResolved        TemporalStatus = "resolved"
	TemporalRegressed       TemporalStatus = "regressed"
	TemporalContradicted    TemporalStatus = "contradicted"
	TemporalUnverifiable    TemporalStatus = "unverifiable"
)

func (t TemporalStatus) valid() bool {
	switch t {
	case "", TemporalHistorical, TemporalStillApplicable, TemporalResolved,
		TemporalRegressed, TemporalContradicted, TemporalUnverifiable:
		return true
	}
	return false
}

// Classification is §4.5's privacy/publication classification. It has no
// default: a proposal that omitted it would have to be treated as either
// silently private, hiding a publication decision, or silently publishable,
// which is worse. Callers state it.
type Classification string

// The publication classifications. ClassificationRedactionRequired marks
// material that could be projected publicly only after §6.7's secret, path,
// and private-evidence redaction.
const (
	ClassificationPrivate           Classification = "private"
	ClassificationRedactionRequired Classification = "redaction-required"
	ClassificationPublicSafe        Classification = "public-safe"
)

func (c Classification) valid() bool {
	switch c {
	case ClassificationPrivate, ClassificationRedactionRequired, ClassificationPublicSafe:
		return true
	}
	return false
}

// Destination is a §4.6 output projection a proposal suggests. Suggesting one
// has no external effect; §8's export command is what renders it.
type Destination string

// The §4.6 projection destinations, matching `babel export projection
// --destination`.
const (
	DestinationIssue         Destination = "issue"
	DestinationBrief         Destination = "brief"
	DestinationOperatorNote  Destination = "operator-note"
	DestinationSkill         Destination = "skill"
	DestinationInvestigation Destination = "investigation"
	DestinationPattern       Destination = "pattern"
	DestinationCookbook      Destination = "cookbook"
	DestinationSecurity      Destination = "security"
)

func (d Destination) valid() bool {
	switch d {
	case DestinationIssue, DestinationBrief, DestinationOperatorNote, DestinationSkill,
		DestinationInvestigation, DestinationPattern, DestinationCookbook, DestinationSecurity:
		return true
	}
	return false
}

// Evidence is one provenance-bearing citation: a locator that recovers the
// original bytes, plus the note explaining what those bytes show.
//
// Both fields are unexported and there is no exported zero-value path to set
// them, so the only Evidence that can exist is one NewEvidence or
// UnmarshalJSON validated. §4.3 makes evidence inseparable from its locator,
// and this is where the type system can carry that rule instead of a comment:
// a caller cannot append a bare string to a claim's evidence list, and a
// corrupted stored payload fails to decode rather than yielding a claim whose
// provenance silently evaporated.
type Evidence struct {
	locator event.Locator
	note    string
}

// evidenceJSON is Evidence's wire shape. It exists because Evidence's fields
// are unexported precisely so that no other code can construct one.
type evidenceJSON struct {
	Locator event.Locator `json:"locator"`
	Note    string        `json:"note,omitempty"`
}

// NewEvidence builds a citation, refusing a locator that could not recover
// what it points at. Path and Digest are required because together they
// identify the bytes and prove they have not changed; Line and ByteOffset
// narrow the citation within them and may be zero for whole-object evidence
// such as a repository blob or a brokered research document.
func NewEvidence(locator event.Locator, note string) (Evidence, error) {
	if locator.Path == "" {
		return Evidence{}, fmt.Errorf("%w: empty path", ErrInvalidLocator)
	}
	if locator.Digest == "" {
		return Evidence{}, fmt.Errorf("%w: empty digest for %q", ErrInvalidLocator, locator.Path)
	}
	if locator.Line < 0 {
		return Evidence{}, fmt.Errorf("%w: negative line %d", ErrInvalidLocator, locator.Line)
	}
	if locator.ByteOffset < 0 {
		return Evidence{}, fmt.Errorf("%w: negative byte offset %d", ErrInvalidLocator, locator.ByteOffset)
	}
	return Evidence{locator: locator, note: note}, nil
}

// Locator reports where the cited bytes live.
func (e Evidence) Locator() event.Locator { return e.locator }

// Note reports what the citing claim says those bytes show.
func (e Evidence) Note() string { return e.note }

// MarshalJSON writes the citation. It re-validates so that an Evidence that
// somehow reached a payload without NewEvidence cannot be persisted.
func (e Evidence) MarshalJSON() ([]byte, error) {
	if _, err := NewEvidence(e.locator, e.note); err != nil {
		return nil, err
	}
	return json.Marshal(evidenceJSON{Locator: e.locator, Note: e.note})
}

// UnmarshalJSON reads a citation back through the same validation as
// NewEvidence, so a truncated or tampered payload is a decode failure instead
// of a claim with an unusable locator.
func (e *Evidence) UnmarshalJSON(data []byte) error {
	var wire evidenceJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("decode evidence: %w", err)
	}
	decoded, err := NewEvidence(wire.Locator, wire.Note)
	if err != nil {
		return err
	}
	*e = decoded
	return nil
}

// Hypothesis is one immutable revision of a §4.2 candidate. Revising it
// creates a descendant carrying AncestorID; the ancestor row is never
// rewritten, so lineage is a chain of whole records rather than a diff log.
//
// Status is not a stored column: it is the newest entry of an append-only
// status history, which is why a candidate's whole exploration lifecycle
// remains inspectable and why no code path can overwrite it.
type Hypothesis struct {
	ID            string
	AncestorID    string // "" for an original candidate
	RunID         string // the generating or refinement run (§4.2)
	SchemaVersion int
	CreatedAt     time.Time
	Status        Status
	Payload       HypothesisPayload
}

// HypothesisPayload is the §9 encryption-bound part of a candidate. It holds
// the idea in the model's original wording, which §5.2 requires to stay
// browseable, plus the sorting signals: novelty and priority are estimates
// derived from corpus content, and §9's plaintext allowlist admits identifiers
// and counts but not content-derived scores.
type HypothesisPayload struct {
	// Statement is the candidate as the model worded it. §5.2 requires the
	// original wording to survive classification and sorting.
	Statement string `json:"statement"`
	// OriginCues are the cues that provoked the candidate (§4.2).
	OriginCues []string `json:"origin_cues,omitempty"`
	// ProvisionalLabels are §4.2's provisional labels. They are provisional
	// because §5.2 permits an uncategorized candidate to remain valid.
	ProvisionalLabels []string `json:"provisional_labels,omitempty"`
	// Novelty and Priority are §5.2 sorting signals in [0,1]. §5.2 confines
	// them to ordering: they never gate whether a candidate exists.
	Novelty  float64 `json:"novelty"`
	Priority float64 `json:"priority"`
	// Notes is free investigator context about the candidate.
	Notes string `json:"notes,omitempty"`
}

func (p HypothesisPayload) validate() error {
	if p.Statement == "" {
		return fmt.Errorf("%w: hypothesis statement is empty", ErrInvalidValue)
	}
	if err := unitInterval("novelty", p.Novelty); err != nil {
		return err
	}
	return unitInterval("priority", p.Priority)
}

// Observation is one immutable revision of a §4.3 provenance-bearing claim.
// It always names the hypothesis it develops, because §4.2's path forbids an
// observation that belongs to nothing.
type Observation struct {
	ID            string
	AncestorID    string
	HypothesisID  string
	RunID         string
	RecipeID      string // §5.1 recipe provenance; the cookbook is public
	RecipeVersion int
	SchemaVersion int
	// EvidenceCount duplicates len(Payload.Evidence) as an allowlisted
	// column so that §4.3's rule stays enforceable after §9 seals the
	// payload: a store that cannot read the evidence can still refuse a row
	// claiming none.
	EvidenceCount int
	CreatedAt     time.Time
	Payload       ObservationPayload
}

// ObservationPayload is the §9 encryption-bound part of a claim. Evidence
// locators are here rather than in plaintext columns because §3 and §9 keep
// source paths out of PostgreSQL in the clear.
type ObservationPayload struct {
	// Claim is what the observation asserts.
	Claim string `json:"claim"`
	// Category is the claim category (§4.3). It is free text because §5.2
	// allows a claim that fits no existing lens.
	Category string `json:"category,omitempty"`
	// Confidence and Impact are the §4.3 gradings.
	Confidence Confidence `json:"confidence"`
	Impact     Impact     `json:"impact"`
	// Evidence is the §4.3 evidence locator set. It is never empty.
	Evidence []Evidence `json:"evidence"`
	// CounterEvidence and CounterEvidenceAbsent implement §4.3's "explicit
	// counter-evidence or absence thereof": exactly one of them is set, so
	// an empty list can never be mistaken for an unasked question.
	CounterEvidence       []Evidence `json:"counter_evidence,omitempty"`
	CounterEvidenceAbsent bool       `json:"counter_evidence_absent,omitempty"`
	// TemporalStatus is §5.4's present-reality reading, where assessed.
	TemporalStatus TemporalStatus `json:"temporal_status,omitempty"`
}

func (p ObservationPayload) validate() error {
	if p.Claim == "" {
		return fmt.Errorf("%w: observation claim is empty", ErrInvalidValue)
	}
	if !p.Confidence.valid() {
		return fmt.Errorf("%w: confidence %q", ErrInvalidValue, p.Confidence)
	}
	if !p.Impact.valid() {
		return fmt.Errorf("%w: impact %q", ErrInvalidValue, p.Impact)
	}
	if !p.TemporalStatus.valid() {
		return fmt.Errorf("%w: temporal status %q", ErrInvalidValue, p.TemporalStatus)
	}
	if len(p.Evidence) == 0 {
		return ErrNoEvidence
	}
	if err := validateEvidence("evidence", p.Evidence); err != nil {
		return err
	}
	if err := validateEvidence("counter-evidence", p.CounterEvidence); err != nil {
		return err
	}
	return counterEvidenceStated(len(p.CounterEvidence), p.CounterEvidenceAbsent)
}

// Finding is one immutable revision of a §4.4 consolidation. It exists only
// as the consolidation of observations, so its supporting observation IDs are
// part of its identity rather than an afterthought.
type Finding struct {
	ID             string
	AncestorID     string
	RunID          string
	SchemaVersion  int
	CreatedAt      time.Time
	ObservationIDs []string // at least one, in the order the caller gave
	// HypothesisIDs is derived from the supporting observations rather than
	// supplied, so the recorded lineage cannot disagree with the path the
	// finding actually took.
	HypothesisIDs []string
	Payload       FindingPayload
}

// FindingPayload is the §9 encryption-bound part of a consolidation.
type FindingPayload struct {
	// Title and Pattern are §4.4's explanation of what recurs.
	Title   string `json:"title"`
	Pattern string `json:"pattern"`
	// Significance is §4.4's "why it matters".
	Significance string `json:"significance,omitempty"`
	// Scope is the affected scope: the sessions, repositories, or systems
	// the pattern was consolidated across.
	Scope []string `json:"scope,omitempty"`
	// Recurrence counts occurrences where applicable; zero means recurrence
	// was not applicable to this finding, which §4.4 permits.
	Recurrence int `json:"recurrence,omitempty"`
	// CounterEvidence and CounterEvidenceAbsent follow §4.4's requirement
	// that a finding explains its counter-evidence, with the same
	// exactly-one rule as an observation.
	CounterEvidence       []Evidence     `json:"counter_evidence,omitempty"`
	CounterEvidenceAbsent bool           `json:"counter_evidence_absent,omitempty"`
	TemporalStatus        TemporalStatus `json:"temporal_status,omitempty"`
}

func (p FindingPayload) validate() error {
	if p.Title == "" {
		return fmt.Errorf("%w: finding title is empty", ErrInvalidValue)
	}
	if p.Pattern == "" {
		return fmt.Errorf("%w: finding pattern is empty", ErrInvalidValue)
	}
	if p.Recurrence < 0 {
		return fmt.Errorf("%w: negative recurrence %d", ErrInvalidValue, p.Recurrence)
	}
	if !p.TemporalStatus.valid() {
		return fmt.Errorf("%w: temporal status %q", ErrInvalidValue, p.TemporalStatus)
	}
	if err := validateEvidence("counter-evidence", p.CounterEvidence); err != nil {
		return err
	}
	return counterEvidenceStated(len(p.CounterEvidence), p.CounterEvidenceAbsent)
}

// Proposal is one immutable revision of §4.5's canonical private review
// artifact. It is not an issue, document, or instruction and has no external
// effect; §4.6 rendering reads it without changing it.
type Proposal struct {
	ID            string
	AncestorID    string
	RunID         string
	SchemaVersion int
	CreatedAt     time.Time
	FindingIDs    []string // at least one (§4.5)
	// HypothesisIDs is derived through the supporting findings and their
	// observations, giving §4.5's "linked hypotheses/findings" without
	// letting a caller assert a lineage the records do not support.
	HypothesisIDs []string
	// ReviewStatus is derived from the append-only disposition history, so
	// it always agrees with the events that justify it.
	ReviewStatus ReviewStatus
	Payload      ProposalPayload
}

// ProposalPayload is the §9 encryption-bound part of a proposal: §9 names
// proposals among the sensitive payloads that never reach PostgreSQL in the
// clear.
type ProposalPayload struct {
	// Title, Problem, and Outcome are §4.5's concise title, problem or
	// opportunity statement, and proposed outcome.
	Title   string `json:"title"`
	Problem string `json:"problem"`
	Outcome string `json:"outcome"`
	// Applicability and TemporalStatus are §4.5's applicability and
	// temporal status.
	Applicability  string         `json:"applicability,omitempty"`
	TemporalStatus TemporalStatus `json:"temporal_status,omitempty"`
	// Supporting and Conflicting are §4.5's supporting and conflicting
	// material, carried as locator-bearing citations so that a proposal's
	// backing can be reopened rather than believed.
	Supporting  []Evidence `json:"supporting,omitempty"`
	Conflicting []Evidence `json:"conflicting,omitempty"`
	// Uncertainty, Impact, and EstimatedScope are §4.5's uncertainty,
	// impact, and estimated scope.
	Uncertainty    string `json:"uncertainty,omitempty"`
	Impact         Impact `json:"impact"`
	EstimatedScope string `json:"estimated_scope,omitempty"`
	// Targets are §4.5's zero or more suggested target repositories or
	// systems with confidence and rationale.
	Targets []Target `json:"targets,omitempty"`
	// Risks, OpenQuestions, Prerequisites, and VerificationCriteria are
	// §4.5's risks, unresolved questions, prerequisites, and suggested
	// verification criteria.
	Risks                []string `json:"risks,omitempty"`
	OpenQuestions        []string `json:"open_questions,omitempty"`
	Prerequisites        []string `json:"prerequisites,omitempty"`
	VerificationCriteria []string `json:"verification_criteria,omitempty"`
	// Classification is §4.5's privacy/publication classification.
	Classification Classification `json:"classification"`
	// Destinations are §4.5's zero or more suggested output destinations.
	Destinations []Destination `json:"destinations,omitempty"`
}

// Target is one suggested destination system for a proposal, with the
// confidence and rationale §4.5 requires. §4.6 makes clear this is a
// suggestion for operator review, never an automatic fact.
type Target struct {
	System     string     `json:"system"`
	Confidence Confidence `json:"confidence"`
	Rationale  string     `json:"rationale,omitempty"`
}

func (p ProposalPayload) validate() error {
	if p.Title == "" {
		return fmt.Errorf("%w: proposal title is empty", ErrInvalidValue)
	}
	if p.Problem == "" {
		return fmt.Errorf("%w: proposal problem statement is empty", ErrInvalidValue)
	}
	if p.Outcome == "" {
		return fmt.Errorf("%w: proposal outcome is empty", ErrInvalidValue)
	}
	if !p.Impact.valid() {
		return fmt.Errorf("%w: impact %q", ErrInvalidValue, p.Impact)
	}
	if !p.TemporalStatus.valid() {
		return fmt.Errorf("%w: temporal status %q", ErrInvalidValue, p.TemporalStatus)
	}
	if !p.Classification.valid() {
		return fmt.Errorf("%w: classification %q", ErrInvalidValue, p.Classification)
	}
	for _, destination := range p.Destinations {
		if !destination.valid() {
			return fmt.Errorf("%w: destination %q", ErrInvalidValue, destination)
		}
	}
	for _, target := range p.Targets {
		if target.System == "" {
			return fmt.Errorf("%w: target system is empty", ErrInvalidValue)
		}
		if !target.Confidence.valid() {
			return fmt.Errorf("%w: target confidence %q", ErrInvalidValue, target.Confidence)
		}
	}
	if err := validateEvidence("supporting material", p.Supporting); err != nil {
		return err
	}
	return validateEvidence("conflicting material", p.Conflicting)
}

// Link is one typed §4.2 relationship between two hypotheses. Lineage is
// queried in both directions, so a contradicted candidate can be found from
// either side of the contradiction.
type Link struct {
	ID        string
	FromID    string
	ToID      string
	Type      LinkType
	CreatedAt time.Time
	Payload   LinkPayload
}

// LinkPayload is the §9 encryption-bound part of a link: the reason a
// relationship was asserted is investigator prose about the corpus.
type LinkPayload struct {
	Note string `json:"note,omitempty"`
}

// StatusEvent is one entry in a hypothesis's append-only lifecycle history.
// Storing the history rather than a mutable column is what makes §5.2's
// "sorting never deletes" checkable: a candidate that left the active
// frontier still shows when and in which run it did.
type StatusEvent struct {
	ID           string
	HypothesisID string
	// Sequence is per-hypothesis and strictly increasing, so history has a
	// total order even when two events share a timestamp.
	Sequence   int64
	Status     Status
	RunID      string
	RecordedAt time.Time
	Payload    StatusPayload
}

// StatusPayload is the §9 encryption-bound part of a status event.
type StatusPayload struct {
	Note string `json:"note,omitempty"`
}

// DispositionEvent is one append-only §4.7 review decision. There is no
// update or delete: reconsidering a rejection appends another event, and both
// remain in order.
type DispositionEvent struct {
	ID      string
	Subject Ref
	// Sequence is per-subject and strictly increasing.
	Sequence    int64
	Disposition Disposition
	// ReviewerID attributes the decision. §4.7 calls operator context
	// attributed guidance, so an unattributed decision is refused.
	ReviewerID string
	// ContextID references the attributed operator context that accompanied
	// the decision, and DuplicateOfID the record a `duplicate` decision
	// points at. Both are identifiers, which §9 allows in plaintext.
	ContextID     string
	DuplicateOfID string
	RecordedAt    time.Time
	Payload       DispositionPayload
}

// DispositionPayload is the §9 encryption-bound part of a decision: §9 names
// review notes among the sensitive payloads.
type DispositionPayload struct {
	Note string `json:"note,omitempty"`
}

// RefinementRequest is §4.7's authorized refinement request. It cannot exist
// without the rejection that authorized it: DispositionID is mandatory and
// unique, so a refinement can neither float free nor be attached twice to one
// rejection.
type RefinementRequest struct {
	ID string
	// DispositionID is the `reject` event that authorized this request.
	DispositionID string
	Subject       Ref
	CreatedAt     time.Time
	Payload       RefinementPayload
}

// RefinementPayload is the §9 encryption-bound part of a refinement request:
// reviewer guidance is operator context about the corpus.
type RefinementPayload struct {
	// Guidance is the attributed reviewer instruction the refinement run
	// receives in its prompt (§4.7).
	Guidance string `json:"guidance"`
	// Scope names extra sources or context the refinement may add (§4.7).
	Scope []string `json:"scope,omitempty"`
}

func (p RefinementPayload) validate() error {
	if p.Guidance == "" {
		return fmt.Errorf("%w: refinement guidance is empty", ErrInvalidValue)
	}
	return nil
}

func unitInterval(name string, value float64) error {
	if value < 0 || value > 1 {
		return fmt.Errorf("%w: %s %v is outside [0,1]", ErrInvalidValue, name, value)
	}
	return nil
}

func validateEvidence(name string, items []Evidence) error {
	for i, item := range items {
		if _, err := NewEvidence(item.locator, item.note); err != nil {
			return fmt.Errorf("%s [%d]: %w", name, i, err)
		}
	}
	return nil
}

// counterEvidenceStated enforces the exactly-one rule that turns §4.3's
// "explicit counter-evidence or absence thereof" into something a reader can
// trust: an empty list with the flag unset is an unanswered question, and a
// non-empty list with the flag set is a self-contradiction.
func counterEvidenceStated(count int, absent bool) error {
	switch {
	case count == 0 && !absent:
		return ErrCounterEvidenceUnstated
	case count > 0 && absent:
		return fmt.Errorf("%w: %d counter-evidence items are also declared absent", ErrCounterEvidenceUnstated, count)
	}
	return nil
}
