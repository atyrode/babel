package reality

// Topics: what a record is about (SPEC.md §4.13).
//
// A topic is a Reality Ledger entity and nothing else — a repository, a
// project, a machine, a concept — so this file adds no topic record, no topic
// table of subjects and no second identity space. What it adds is the one
// thing §4.13 needs that §4.8 did not already have: the *proposal*, and the
// acceptance that turns a proposal into an entity.
//
// The rule that shapes all of it is §4.8's, unchanged: Babel proposes
// identity, only the operator creates it. A run that meets a name the ledger
// cannot resolve raises a question rather than minting a subject, and a topic
// question is exactly that question with the proposal attached — kind,
// binding, aliases, why, the records it would file, and the existing entities
// it weighed and rejected. It lands in the Reality Inbox beside every other
// question, it ranks by the same factors, and declining it keeps the refusal
// verbatim and suppresses the same proposal until materially new evidence
// exists.
//
// Two properties are worth stating because they are refused writes rather than
// conventions.
//
// A topic question has no target entity. Every other question is about
// entities that exist and is deduplicated by them; this one is about something
// the ledger does not name, so its subject matter is the proposed *identity* —
// a normalized remote, a common directory, a slug — and two runs proposing one
// repository raise one question however differently they worded it. The
// identity is sealed with the rest of the proposal and keyed by digest in the
// clear, for the reason §9 gives about alias values.
//
// Nothing here files anything. A filing is an edge in internal/frontier and
// this package holds no frontier rows; an acceptance hands the records to an
// injected Filer after its own transaction has committed, which is the same
// seam HypothesisSink already describes and for the same reason.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// Provenance identifies what raised a proposal: which run, under which recipe
// and version, and which kind of author.
//
// It is recorded rather than inferred because §4.13 has the triage recipe read
// its own history — why topics were retired, split and declined — as evidence
// for its next proposals, and a proposal that cannot say what produced it is
// evidence about nothing. It is also what decides the author of the filings an
// acceptance performs: a run judged each record's membership, a heuristic did
// not, and §4.13 requires the second to be labelled as such so the recipe
// knows to revisit it.
type Provenance struct {
	// RunID is the run that raised the proposal, empty for a heuristic
	// seeding or an operator's own proposal.
	RunID string
	// RecipeID and Version name the cookbook asset the run executed.
	RecipeID string
	Version  int
	// Actor is who the state event is attributed to. Empty means the run
	// when there is one, and this component otherwise.
	Actor string
}

// actor names the author of the question's state events.
func (p Provenance) actor() string {
	if p.Actor != "" {
		return p.Actor
	}
	if p.RunID != "" {
		return p.RunID
	}
	return component
}

// heuristic reports whether the filings this proposal's acceptance performs
// are heuristic. A proposal with no run behind it was derived from repository
// identity alone, which is §4.13's seeding: observable without a model, and
// labelled so the recipe revisits it.
func (p Provenance) heuristic() bool { return p.RunID == "" }

// TopicProposal is the entity a run would have created, had it been allowed
// to.
//
// Considered is the part that is easy to leave out and worth the field. §4.13
// requires a proposal to name the existing entities it weighed and rejected,
// because "this is new" is a claim about the whole ledger and an operator
// cannot check it against a proposal that only says what it wants.
type TopicProposal struct {
	// Name is what the operator would call it.
	Name string
	// Kind is the entity kind. §4.13 is explicit that nothing may assume a
	// topic is a repository, so this is stated by every proposal.
	Kind EntityKind
	// Aliases are the typed names the subject should answer to: the
	// workspace paths a repository was seen at, the terms a conversation
	// used. The identity is attached as an identifier alias by the
	// acceptance, so a caller does not repeat it here.
	Aliases []AliasInput
	// Binding is the facts that bind the subject to something real — a
	// repository remote, the checkouts it lives in. They arrive without a
	// subject and without an authority: the accepting operator supplies
	// both, which is why a proposal cannot assert anything by being stored.
	Binding []FactInput
	// Reasoning is why this is one thing, in the proposer's words, and
	// includes why each considered entity was rejected.
	Reasoning string
	// Records are the frontier records the proposal would file under the
	// new topic once it exists.
	Records []frontier.Ref
	// Considered are the entity IDs weighed and rejected.
	Considered []string
	// Identity is the dedup key and the binding a second proposal of the
	// same thing would arrive with: a normalized remote, the common
	// directory every worktree shares, or a slug for a concept.
	Identity string
	// Sessions is how much evidence stands behind the proposal — sessions,
	// records, citations, whatever the proposer counted. It is what
	// "materially new evidence" is measured against after a decline: §4.13
	// suppresses a refused proposal until the world says more than it did
	// when the operator refused it.
	Sessions int
}

func (in TopicProposal) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return fmt.Errorf("%w: topic proposal has no name", ErrInvalidValue)
	}
	if !in.Kind.valid() {
		return fmt.Errorf("%w: topic kind %q", ErrInvalidValue, in.Kind)
	}
	if strings.TrimSpace(in.Identity) == "" {
		return fmt.Errorf("%w: topic proposal has no identity to bind", ErrInvalidValue)
	}
	if strings.TrimSpace(in.Reasoning) == "" {
		return fmt.Errorf("%w: topic proposal does not say why", ErrInvalidValue)
	}
	if in.Sessions < 0 {
		return fmt.Errorf("%w: topic proposal counts %d sessions", ErrInvalidValue, in.Sessions)
	}
	if err := checkNoCredential("topic name", in.Name); err != nil {
		return err
	}
	if err := checkNoCredential("topic reasoning", in.Reasoning); err != nil {
		return err
	}
	for _, alias := range in.Aliases {
		if !alias.Kind.valid() {
			return fmt.Errorf("%w: alias kind %q", ErrInvalidValue, alias.Kind)
		}
		if err := alias.Payload.validate(); err != nil {
			return err
		}
	}
	for _, fact := range in.Binding {
		if fact.SubjectID != "" {
			return fmt.Errorf("%w: a binding fact names subject %q before the subject exists",
				ErrInvalidValue, fact.SubjectID)
		}
		if err := fact.validateBinding(); err != nil {
			return err
		}
	}
	// The record kinds are internal/frontier's closed vocabulary and this
	// package deliberately keeps no second copy of it — two lists is how
	// one of them ends up missing a kind. A reference with no kind or no ID
	// names nothing and is refused here; a kind the frontier does not know
	// is refused by the Filer, at the acceptance that would have used it.
	for _, record := range in.Records {
		if record.ID == "" || record.Type == "" {
			return fmt.Errorf("%w: record reference %q/%q", ErrInvalidValue, record.Type, record.ID)
		}
	}
	return nil
}

// validateBinding checks a fact a proposal carries, which arrives with neither
// a subject nor a time: both belong to the acceptance, which is the only act
// that can supply them. Everything else — the predicate, the value's type and
// vocabulary, the sensitivity, the prose — is checked now, so a proposal
// cannot be stored carrying a fact that could never be asserted.
func (in FactInput) validateBinding() error {
	in.SubjectID = "pending-entity"
	if in.ValidFrom.IsZero() {
		in.ValidFrom = time.Unix(0, 0).UTC()
	}
	if in.ObservedAt.IsZero() {
		in.ObservedAt = in.ValidFrom
	}
	if in.Confidence == "" {
		in.Confidence = ConfidenceHigh
	}
	if in.Sensitivity == "" {
		in.Sensitivity = SensitivityRoutine
	}
	return in.validateProposed()
}

// validate checks the subject a create-entity action would mint. The aliases
// are checked here rather than at acceptance because a plan the operator is
// asked to accept must be one that can actually be applied.
func (in *EntityDraft) validate() error {
	if in == nil {
		return fmt.Errorf("%w: a create-entity action carries no subject", ErrInvalidValue)
	}
	if !in.Subject.Kind.valid() {
		return fmt.Errorf("%w: entity kind %q", ErrInvalidValue, in.Subject.Kind)
	}
	if err := (EntityPayload{DisplayName: in.Subject.DisplayName, Notes: in.Subject.Notes}).validate(); err != nil {
		return err
	}
	for _, alias := range in.Subject.Aliases {
		if !alias.Kind.valid() {
			return fmt.Errorf("%w: alias kind %q", ErrInvalidValue, alias.Kind)
		}
		if err := alias.Payload.validate(); err != nil {
			return err
		}
	}
	for _, fact := range in.Binding {
		if fact.Authority.Kind != "" {
			return fmt.Errorf("%w: an interpretation may not attribute a fact; the accepting operator does",
				ErrNotAuthoritative)
		}
		if err := fact.validateBinding(); err != nil {
			return err
		}
	}
	return nil
}

// validate checks one filing a file-records action would perform. The record
// reference and the rationale are this package's business; whether the author
// may file, and whether the record exists, is internal/frontier's, and the
// Filer refuses what it will not accept.
func (in FilingDraft) validate() error {
	if in.Record.ID == "" || in.Record.Type == "" {
		return fmt.Errorf("%w: filing names no record", ErrInvalidValue)
	}
	if strings.TrimSpace(in.Rationale) == "" {
		return fmt.Errorf("%w: filing of record %s states no rationale", ErrInvalidValue, in.Record.ID)
	}
	return checkNoCredential("filing rationale", in.Rationale)
}

// TopicQuestion is one proposal with the question carrying it.
//
// The two travel together because neither is usable alone: the question holds
// the state, the ranking and the refusal history, and the proposal holds what
// would be created. EntityID is set once an acceptance created it, which is
// what makes an answered topic question say what it produced.
type TopicQuestion struct {
	Question Question
	Proposal TopicProposal
	By       Provenance
	EntityID string
}

// topicPayload is the §9 encryption-bound half of a proposal. Everything in it
// is operator- or corpus-derived vocabulary — a name, a path, a remote, a
// reason — so none of it may sit in a plaintext column; the row's own columns
// are the identity's digest, the kind, the evidence weight and the time.
type topicPayload struct {
	Name       string         `json:"name"`
	Identity   string         `json:"identity"`
	Aliases    []AliasInput   `json:"aliases,omitempty"`
	Binding    []FactInput    `json:"binding,omitempty"`
	Reasoning  string         `json:"reasoning"`
	Records    []frontier.Ref `json:"records,omitempty"`
	Considered []string       `json:"considered,omitempty"`
	Sessions   int            `json:"sessions"`
	By         Provenance     `json:"by"`
}

// EntityDraft is the subject a create-entity action would mint, with the facts
// that bind it. It is NewSubject plus the binding because the two are one act:
// an entity created without what binds it to something real is a name, and
// §4.13 is about the difference.
type EntityDraft struct {
	Subject NewSubject  `json:"subject"`
	Binding []FactInput `json:"binding,omitempty"`
}

// FilingDraft is one record a file-records action would file.
//
// EntityID is empty when the filing belongs to the entity the same acceptance
// creates, which is what a topic proposal always says: the topic does not
// exist when the proposal is written, so it cannot be named.
type FilingDraft struct {
	Record    frontier.Ref          `json:"record"`
	EntityID  string                `json:"entity_id,omitempty"`
	Rationale string                `json:"rationale,omitempty"`
	Author    frontier.FilingAuthor `json:"author,omitempty"`
	AuthorID  string                `json:"author_id,omitempty"`
	Heuristic bool                  `json:"heuristic,omitempty"`
}

// Filer files a record under a topic. It is internal/frontier's File, injected
// rather than called directly, for HypothesisSink's reason: the frontier is a
// separate component with its own connection to the same durable file, so this
// package's transaction must not be open while it writes.
type Filer interface {
	File(ctx context.Context, in frontier.FilingInput) (frontier.Filing, error)
}

// TopicAcceptance is what accepting a topic proposal produced.
//
// It reports the whole act rather than an identifier, because the act is
// several records across two components and a caller that had to re-read them
// could not tell a partial acceptance from a complete one. Filings is what the
// Filer actually accepted; when it is shorter than the proposal's records, the
// error says which are missing and the records are unfiled — which is the
// triage backlog §4.13 already has a recipe for, and not a lost acceptance.
type TopicAcceptance struct {
	ID         string
	QuestionID string
	EntityID   string
	Entity     Entity
	Aliases    []Alias
	Facts      []Fact
	Filings    []frontier.Filing
	Actor      string
	RecordedAt time.Time
}

// Binding is what a topic is bound to, read back from the facts in force.
//
// It is derived rather than stored for §4.8's reason: the binding *is* the
// facts, and a second copy of it on the entity row would be a field that can
// disagree with the ledger. Identity prefers the remote, because that is the
// identity that survives the same repository being cloned on another machine.
type Binding struct {
	Kind     EntityKind
	Identity string
	Remote   string
	Paths    []string
}
