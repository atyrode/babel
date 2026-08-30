// Package review is the service layer over Babel's append-only review and
// refinement state (SPEC.md §4.7 and §6.7) and the Phase B raw export of
// §6.7's second paragraph.
//
// internal/frontier already owns the durable records and the two writes that
// have to be atomic: a disposition is an appended row, and reject-and-refine
// appends the rejection and creates the refinement request it authorizes in
// one transaction. What is missing above it, and what this package supplies,
// is the part a reviewer actually touches: which records may be decided in
// which state, the attributed operator context a decision cites, the
// structured durable-learning assessment a refinement worker must emit before
// it produces anything, and the queue, history, and lineage queries that make
// an append-only log readable.
//
// Four properties shape the design.
//
// Guidance is not evidence. §4.7 states that operator context is attributed
// guidance rather than independent evidence, so Context carries an author and
// no locator, and the Evidence interface this package requires wherever
// evidence is required is one Context cannot implement. A refinement
// assessment that cites contexts and no evidence is refused by name
// (ErrContextIsNotEvidence) rather than accepted with a thinner justification.
//
// Proposing is not authorizing. §4.7 says the refinement agent may propose but
// never authorize lasting context. That is a signature, not a convention: a
// refinement outcome is recorded through an Agent identity, a durable-learning
// proposal is disposed of through an Authority identity, and the two are
// distinct named types with unexported fields, so no refinement path holds a
// value it could pass where an operator is required.
//
// Nothing is implicitly decided. §4.7 requires the rejection, the assessment,
// the revised output, the memory proposal, and each disposition to carry
// separate immutable IDs and lineage. Here they are also separate rows in
// separate tables reached by separate methods: accepting a revision writes a
// frontier disposition against the revision, accepting a memory proposal
// writes a review disposition against the proposal, and no code path writes
// both. The independence is structural rather than asserted.
//
// Nothing publishes. §4.6 and decision 13 are absolute. Export renders a raw
// private record for a human to read; it opens nothing, writes nothing
// outside the file the caller names, and says in its own first paragraph that
// it is fallible analytical output rather than an audit.
//
// Storage joins the durable database internal/frontier and internal/run
// already share: one `durable.db`, one `schema_migration(component, version)`
// ledger, and a `review_` table prefix. The §9 split is mirrored the same way,
// one payload_json column per table beside allowlisted identifiers, kinds,
// counts, and timestamps, so the later sync slice replaces one column per
// table rather than auditing a field list.
package review

import (
	"errors"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
)

// RecordSchema is the version stamped on every record this build writes. §9
// requires every row to carry a schema version; it describes the envelope
// rather than its contents, so it is plaintext-eligible.
const RecordSchema = 1

// Sentinel errors for the refusals a caller is expected to handle rather than
// merely report. They are distinguished from one another because "the review
// service said no" is not an answer a reviewer can act on: whether a record
// was the wrong kind, in a closed state, or repeating a decision it already
// carries calls for three different responses.
var (
	// ErrUnknownRecord reports a reference to a record no store holds.
	// Nothing here is ever deleted, so this always means the reference was
	// wrong rather than that the target went away.
	ErrUnknownRecord = errors.New("review: unknown record")
	// ErrNotReviewable reports a disposition aimed at something §6.7 does
	// not make reviewable. Observations are the evidence a finding
	// consolidates, not an artifact an operator accepts or rejects.
	ErrNotReviewable = errors.New("review: record is not reviewable")
	// ErrTerminalStatus reports a disposition against a record whose review
	// status accepts no further decision, because the decision belongs
	// somewhere else: on the original a duplicate points at, or on the
	// descendant a rejection authorized.
	ErrTerminalStatus = errors.New("review: review status accepts no further disposition")
	// ErrNoChange reports a disposition identical to the one the record
	// already stands at. The history is the audit record of how a reviewer's
	// mind moved, and an event that moved nothing makes it say something
	// false.
	ErrNoChange = errors.New("review: disposition repeats the record's standing decision")
	// ErrContextIsNotEvidence reports an evidence requirement a caller tried
	// to satisfy with attributed operator context. §4.7 makes context
	// guidance; a claim backed only by someone saying so has no locator
	// behind it.
	ErrContextIsNotEvidence = errors.New("review: attributed operator context is guidance and cannot satisfy an evidence requirement")
	// ErrModeMismatch reports refinement descendants that do not match the
	// assessment's mode. §4.7 fixes exactly which descendants each mode
	// produces, so a mismatch is a worker that did not do what it assessed.
	ErrModeMismatch = errors.New("review: refinement descendants do not match the assessment mode")
	// ErrAlreadyRecorded reports a second outcome for one refinement
	// request. A request is answered once; a further correction is a further
	// rejection with its own request.
	ErrAlreadyRecorded = errors.New("review: refinement outcome already recorded")
	// ErrInvalidValue reports a value outside a closed vocabulary this
	// package controls, or a required field left empty.
	ErrInvalidValue = errors.New("review: invalid value")
)

// Kind names a record a review or lineage query can address. The four analysis
// kinds are defined from internal/frontier's rather than restated, so the two
// vocabularies cannot drift apart; the rest are records this package owns, plus
// the run receipt and the refinement request that lineage has to be able to
// point at.
type Kind string

// The addressable record kinds.
const (
	KindHypothesis        Kind = Kind(frontier.EntityHypothesis)
	KindObservation       Kind = Kind(frontier.EntityObservation)
	KindFinding           Kind = Kind(frontier.EntityFinding)
	KindProposal          Kind = Kind(frontier.EntityProposal)
	KindRun               Kind = "run"
	KindRefinementRequest Kind = "refinement-request"
	KindAssessment        Kind = "assessment"
	KindMemoryProposal    Kind = "memory-proposal"
)

func (k Kind) valid() bool {
	switch k {
	case KindHypothesis, KindObservation, KindFinding, KindProposal,
		KindRun, KindRefinementRequest, KindAssessment, KindMemoryProposal:
		return true
	}
	return false
}

// entity maps a Kind back to a frontier record kind, reporting false for the
// kinds this package owns. It exists so one lineage vocabulary can address
// both stores without either side guessing at the other's strings.
func (k Kind) entity() (frontier.EntityType, bool) {
	switch k {
	case KindHypothesis, KindObservation, KindFinding, KindProposal:
		return frontier.EntityType(k), true
	}
	return "", false
}

// Node addresses one record for lineage and export. It pairs the kind with the
// ID because lineage crosses two stores and several tables, and a bare ID
// would not say which one.
type Node struct {
	Kind Kind
	ID   string
}

// node renders a frontier reference as a lineage node.
func node(ref frontier.Ref) Node { return Node{Kind: Kind(ref.Type), ID: ref.ID} }

// Relation is a typed lineage edge between two records. The vocabulary is
// §4.7's five: a refinement run "creates immutable descendants through
// `refines`, `responds-to`, `supersedes`, `splits`, or `merges` links". It is
// separate from internal/frontier's hypothesis link types because those relate
// two candidates, while these relate whatever a refinement produced to
// whatever provoked it, across kinds.
type Relation string

// The §4.7 lineage relations.
const (
	RelationRefines    Relation = "refines"
	RelationRespondsTo Relation = "responds-to"
	RelationSupersedes Relation = "supersedes"
	RelationSplits     Relation = "splits"
	RelationMerges     Relation = "merges"
)

func (r Relation) valid() bool {
	switch r {
	case RelationRefines, RelationRespondsTo, RelationSupersedes, RelationSplits, RelationMerges:
		return true
	}
	return false
}

// Evidence is what this package accepts wherever §4.3 requires evidence: a
// value carrying a locator that can recover the bytes behind a claim, and the
// note saying what those bytes show.
//
// It is exported so that "operator context is not evidence" is checkable
// rather than merely documented. internal/frontier's Evidence implements it;
// Context does not and cannot, because guidance is attributed to a person
// rather than to bytes in the archive, and there is no locator a person's
// opinion could carry. A reviewer looking for the rule finds it in a type
// assertion instead of in a comment.
type Evidence interface {
	Locator() event.Locator
	Note() string
}

// frontier.Evidence is the only implementation this package stores. The
// assertion is here so that a change to either side is a compile failure
// rather than a silent divergence between the interface a caller reads and
// the concrete type a payload holds.
var _ Evidence = frontier.Evidence{}

// Context is attributed operator guidance. SPEC §4.7: it is guidance, never
// independent evidence, so it can never satisfy an evidence requirement.
//
// The shape carries the enforcement. There is no locator field and no method
// returning one, so Context does not implement Evidence and cannot be passed
// where evidence is required; an assessment that cites contexts and no
// evidence is refused with ErrContextIsNotEvidence rather than accepted with
// a person's word standing in for the archive. Author is required for the
// same reason §4.7 calls the guidance attributed: unattributed guidance is
// indistinguishable from the model's own prose once it is in a prompt.
type Context struct {
	ID     string
	Author string // the operator identity that supplied it
	At     time.Time
	Text   string
}

// contextPayload is the §9 encryption-bound part of an operator context. The
// text is what a reviewer wrote about the corpus, which §9 names among the
// sensitive payloads; the author and the timestamp are allowlisted.
type contextPayload struct {
	Text string `json:"text"`
}

// Authority is the operator identity permitted to decide a reviewable record
// or a proposed durable-learning artifact.
//
// It is a distinct named type with an unexported field, and that is the point.
// §4.7 says the refinement agent may propose but never authorize lasting
// context; here the compiler enforces it, because a refinement outcome is
// recorded through an Agent and a disposition is recorded through an
// Authority, so the refinement path holds no value of the type the
// authorizing methods accept. A rule expressed as a signature cannot be
// forgotten by the next caller.
//
// The field is named differently from Agent's on purpose. Two struct types
// with the same field names and types are identical underlying types and
// therefore freely convertible, which would have left `Authority(agent)` as a
// one-word way around the rule; different field names make the conversion
// impossible rather than merely impolite.
type Authority struct{ operator string }

// NewAuthority names the operator recording a decision. An empty identity is
// refused: §4.7's dispositions are attributed, and an anonymous acceptance
// records that something was accepted without recording that anyone accepted
// it.
func NewAuthority(id string) (Authority, error) {
	if id == "" {
		return Authority{}, errInvalid("operator identity is empty")
	}
	return Authority{operator: id}, nil
}

// ID reports the operator identity.
func (a Authority) ID() string { return a.operator }

// Agent is the identity of a refinement worker. It is deliberately not an
// Authority, and deliberately not convertible to one: an agent produces
// assessments, revisions, and proposals, and none of those is a decision.
type Agent struct{ worker string }

// NewAgent names the refinement worker recording an outcome. An empty identity
// is refused so a descendant can always be traced to what produced it.
func NewAgent(id string) (Agent, error) {
	if id == "" {
		return Agent{}, errInvalid("refinement agent identity is empty")
	}
	return Agent{worker: id}, nil
}

// ID reports the agent identity.
func (a Agent) ID() string { return a.worker }
