// Package disposition holds Babel's actionable outputs: the typed next
// actions a record's revision carries, the attributable ledger of what an
// operator did with each, and the instruction-free invitations that let an
// operator say "look at this again" without saying how (issue #87).
//
// It is a sibling of internal/review rather than part of it, and the
// distinction is the whole reason this package exists. A review disposition
// (SPEC.md §4.7, frontier.Disposition) is a verdict on a record: accepted,
// rejected, deferred, duplicate. A disposition here is a proposed next action
// *outside* Babel — draft an issue, propose a fact, store a memory, ask the
// operator, keep going — and the operator's answer to it is not a verdict on
// the record but a decision about the action. Both are append-only ledgers
// about the same records, and conflating them would make "accepted" ambiguous
// exactly where #88 wants to read acceptance rates back as a quality signal.
//
// Three properties shape everything here, and each is enforced rather than
// documented.
//
// Nothing acts. §4.6 and decision 13 put publishing, applying, and writing to
// a source repository outside Babel entirely, so a disposition is a rendered
// proposal and an acceptance is a durable record that a person accepted it.
// Accepting a draft-issue disposition opens no issue; this package holds no
// GitHub credential and has no network path.
//
// Nothing is deleted. Declining a disposition appends a ledger entry and
// leaves it readable, reconsidering appends another, and an invitation that a
// run consumed stays in the table with the run that consumed it. Every table
// carries UPDATE and DELETE triggers for the same reason internal/frontier's
// do: an append-only ledger whose append-only-ness depends on nobody writing
// the wrong statement is not append-only.
//
// An invitation carries no instruction. #87 makes "process further" a nudge
// rather than a brief — refine, question, amend, or abandon is the model's
// call — so the table has no column an instruction could be written into. That
// is why it is the one table here with no payload_json: a §9 payload column
// would be a place for operator prose to appear later, and its absence is the
// invariant.
//
// Publication never gates a write. A store opened WithSync stages each durable
// record for the shared catalog inside the very transaction that makes it
// durable, so "this decision is recorded" and "this decision is owed to the
// fleet" are one event; but a failure to reach the fleet afterwards leaves the
// record durable and visibly pending-sync, and the operator's command still
// succeeds (SPEC.md §6.5, §9). A store opened without it publishes nothing and
// is otherwise the same store. See publish.go.
package disposition

import (
	"errors"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// RecordSchema is the version stamped on every row this build writes, on the
// same terms as frontier.RecordSchema: §9 requires a reader to be able to tell
// which shape it is decoding.
const RecordSchema = 1

// Sentinel errors callers are expected to handle rather than merely report.
var (
	// ErrInvalidValue reports a value outside a closed vocabulary this
	// package controls.
	ErrInvalidValue = errors.New("invalid value")
	// ErrUnknownDisposition reports a reference to a proposed action this
	// store does not hold. Nothing here is deleted, so it always means the
	// reference was wrong rather than that the action went away.
	ErrUnknownDisposition = errors.New("unknown disposition")
	// ErrUnknownInvitation reports the same about an invitation.
	ErrUnknownInvitation = errors.New("unknown invitation")
	// ErrAlreadyConsumed reports a second run trying to take an invitation
	// a first one already took. #96 makes invitations rung one of the
	// conductor's work ladder, and a queue entry two cycles can both claim
	// would spend the operator's budget twice on one nudge.
	ErrAlreadyConsumed = errors.New("invitation was already consumed")
	// ErrAnchorRequired reports a draft-issue disposition with no verified
	// repository behind it (issue #88).
	ErrAnchorRequired = errors.New("a draft-issue disposition binds to a verified repository")
	// ErrNoRepository reports a workspace that is not a git checkout, or
	// one with no origin remote to bind to.
	ErrNoRepository = errors.New("no git repository with an origin remote")
)

// Kind is the closed vocabulary of next actions a disposition may propose
// (#87). Each names an existing Babel surface the operator's click feeds, so
// none of them is a new output kind: the point of the vocabulary is that a
// model choosing among five known destinations cannot invent a sixth.
type Kind string

// The proposed actions.
const (
	// KindDraftIssue renders a bounded repository change as a GitHub issue
	// draft. It binds to a repository verified against a local checkout's
	// git configuration (#88) and publishes nothing: publication happens
	// operator-side, under the operator's own credentials, at click time.
	KindDraftIssue Kind = "draft-issue"
	// KindProposeRealityFact routes the record into §4.8's proposed-until-
	// authorized Reality Ledger.
	KindProposeRealityFact Kind = "propose-reality-fact"
	// KindStoreMemory proposes an operator-specific memory through the same
	// proposed-memory system internal/review already owns.
	KindStoreMemory Kind = "store-memory"
	// KindAskQuestion routes a question to the operator's review inbox.
	KindAskQuestion Kind = "ask-question"
	// KindDevelopFurther proposes another exploration pass over the record.
	// It is the disposition an invitation expresses as a one-click nudge,
	// and it is here too because a run may propose it with an argument.
	KindDevelopFurther Kind = "develop-further"
)

// Kinds returns the vocabulary in a stable order, which the CLI's usage text
// and its --kind validation both read so neither can drift from the other.
func Kinds() []Kind {
	return []Kind{KindDraftIssue, KindProposeRealityFact, KindStoreMemory, KindAskQuestion, KindDevelopFurther}
}

func (k Kind) valid() bool {
	for _, known := range Kinds() {
		if k == known {
			return true
		}
	}
	return false
}

// Ruling is an operator's answer to a proposed action. There are exactly two:
// #87's constraint is that every action is a proposal until the operator
// authorizes it, and a third value would be a way of half-authorizing one.
type Ruling string

// The rulings.
const (
	RulingAccepted Ruling = "accepted"
	RulingDeclined Ruling = "declined"
)

func (r Ruling) valid() bool {
	switch r {
	case RulingAccepted, RulingDeclined:
		return true
	}
	return false
}

// Status is a disposition's current state, derived from its ledger rather than
// stored. Deriving it removes the possibility that a status disagrees with the
// entries behind it, which is the same rule frontier.ReviewStatus follows.
type Status string

// The derived statuses. StatusProposed is what a disposition with an empty
// ledger is: nobody has answered it yet, which is different from having
// declined it.
const (
	StatusProposed Status = "proposed"
	StatusAccepted Status = "accepted"
	StatusDeclined Status = "declined"
)

// Disposition is one proposed next action attached to a record revision.
//
// It attaches to a revision, not to a chain: Record names one immutable record
// by id, so a draft-issue proposed against a candidate's third wording stays
// attached to that wording even after a fourth supersedes it. That is the
// honest binding — the action was proposed about what the record said then —
// and it is what lets #88 ask whether acceptance rates changed after a
// rewording rather than averaging the two together.
type Disposition struct {
	ID string
	// Record is the record revision this action is proposed against.
	Record frontier.Ref
	Kind   Kind
	// ProposedBy is the run that emitted the action in its result, or the
	// operator who synthesized it by hand.
	ProposedBy frontier.Actor
	// Ref is the reference the proposing run emitted this action under,
	// empty for a synthesized one. It is what makes a resumed run recognize
	// an action it already proposed instead of proposing it twice, the same
	// job internal/explore's resume ledger does for records.
	Ref           string
	SchemaVersion int
	CreatedAt     time.Time
	// Status is derived from Ledger, never stored.
	Status  Status
	Payload Payload
}

// Payload is the §9 encryption-bound part of a proposed action: a summary and
// a rationale are prose about the corpus, and the draft body is the material
// an operator would paste into a public issue, which §9 keeps sealed until
// they choose to.
type Payload struct {
	// Summary is the action in one line, which is what a listing shows.
	Summary string `json:"summary"`
	// Rationale is why the action fits this record.
	Rationale string `json:"rationale,omitempty"`
	// Anchor is the verified repository a draft-issue binds to, absent for
	// every other kind. It is in the payload rather than in plaintext
	// columns because a repository URL and a workspace path are both source
	// locators, which §3 and §9 keep out of PostgreSQL in the clear.
	Anchor *Anchor `json:"anchor,omitempty"`
}

func (p Payload) validate(kind Kind) error {
	if p.Summary == "" {
		return fmt.Errorf("%w: disposition summary is empty", ErrInvalidValue)
	}
	switch {
	case kind == KindDraftIssue && p.Anchor == nil:
		return ErrAnchorRequired
	case kind != KindDraftIssue && p.Anchor != nil:
		return fmt.Errorf("%w: a %s disposition binds to no repository", ErrInvalidValue, kind)
	}
	if p.Anchor != nil {
		return p.Anchor.validate()
	}
	return nil
}

// LedgerEntry is one durable, attributable operator action on a proposed
// action. It is #88's evidence source and #94's, so it carries who and when as
// first-class fields rather than leaving them to be inferred from a log.
type LedgerEntry struct {
	ID            string
	DispositionID string
	// Sequence is per-disposition and strictly increasing, so a
	// reconsidered decision reads in order even inside one timestamp.
	Sequence int64
	Ruling   Ruling
	// By is the operator. It is deliberately not a frontier.Actor: a run may
	// propose an action and may never answer one, because answering is the
	// authorization step #87 reserves for a person.
	By         string
	RecordedAt time.Time
	Payload    LedgerPayload
}

// LedgerPayload is the §9 encryption-bound part of a ledger entry.
type LedgerPayload struct {
	// Note is the operator's own words about the decision.
	Note string `json:"note,omitempty"`
}

// Invitation is an operator's instruction-free "process this further" against
// one record (#87), and rung one of the conductor's work ladder (#96).
//
// It carries no text at all, by construction. The operator is saying that a
// record deserves attention, not what to do about it — #87 leaves refine,
// question, amend, or abandon to the model — and a field for "just a hint"
// would collapse the distinction between an invitation and a brief within one
// release.
type Invitation struct {
	ID string
	// Record is the record revision the operator pointed at.
	Record frontier.Ref
	// By is the operator who invited. An invitation outranks the
	// conductor's own policy (#96), so it has to be attributable to the
	// person whose authority it borrows.
	By        string
	CreatedAt time.Time
	// ConsumedBy is the run that took this invitation into its scope, empty
	// while the invitation is still queued. It is derived from the
	// consumption table rather than stored on the invitation, so the row an
	// operator wrote is never rewritten by a run.
	ConsumedBy string
	ConsumedAt time.Time
}

// Open reports whether the invitation is still queued.
func (i Invitation) Open() bool { return i.ConsumedBy == "" }
