package reality

// Topics: what a record is about (SPEC.md §4.13).
//
// A topic is a Reality Ledger entity and nothing else — a repository, a
// project, a machine, a concept — so this file adds no topic record, no topic
// table of subjects and no second identity space. What it adds is the one
// thing §4.13 needs that §4.8 did not already have: the *plan* a run attaches
// to the output it published, and the application that turns an accepted
// output into an entity, a merge, a split or a retirement.
//
// Everything about a topic goes through Babel (operator direction 2026-09-12,
// second reading). A new topic, a split, a merge and a retirement are one
// output kind — a topic proposal — produced by a run through the ordinary
// chain and published as an ordinary frontier proposal record. It is reviewed
// by Babel's reviewers, it is voted on, it sits in the feed with everything
// else, and the operator rules on it with the ruling he gives any other
// proposal. So there is no topic question here and no second inbox: the plan
// below hangs off the *proposal record's* identifier, and the operator's
// acceptance of that record is what applies it.
//
// Three properties are worth stating because they are refused writes rather
// than conventions.
//
// A plan is keyed by the proposal it explains, one plan per proposal, and it
// is immutable. A run that changes its mind writes another proposal, which is
// what the frontier's own revision chain is for.
//
// Two runs that propose the same thing collide. The subject matter of a
// create or a split is the *identity* it would bind — a normalized remote, a
// common directory, a slug — and of a merge or a retirement the targets it
// names; either way it is sealed with the rest of the plan and keyed by
// digest in the clear, for the reason §9 gives about alias values. A plan
// whose identity the ledger already binds is refused outright, and one the
// operator declined stays refused until the evidence behind it grows.
//
// Nothing here files anything. A filing is an edge in internal/frontier and
// this package holds no frontier rows; an application hands the records to an
// injected Filer after its own transaction has committed, which is the same
// seam HypothesisSink already describes and for the same reason.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// Provenance identifies what produced a plan: which run, under which recipe
// and version, and which kind of author.
//
// It is recorded rather than inferred because §4.13 has the triage recipe read
// its own history — why topics were retired, split and declined — as evidence
// for its next proposals, and a proposal that cannot say what produced it is
// evidence about nothing. It is also what decides the author of the filings an
// application performs: a run judged each record's membership, a heuristic did
// not, and §4.13 requires the second to be labelled as such so the recipe
// knows to revisit it.
type Provenance struct {
	// RunID is the run that produced the plan, empty for a plan with no run
	// behind it.
	RunID string
	// RecipeID and Version name the cookbook asset the run executed.
	RecipeID string
	Version  int
	// Actor is who the plan's own record is attributed to. Empty means the
	// run when there is one, and this component otherwise.
	Actor string
}

// actor names the author of the plan record.
func (p Provenance) actor() string {
	if p.Actor != "" {
		return p.Actor
	}
	if p.RunID != "" {
		return p.RunID
	}
	return component
}

// heuristic reports whether the filings this plan's application performs are
// heuristic. A plan with no run behind it was derived from repository identity
// alone, which is §4.13's seeding: observable without a model, and labelled so
// the recipe revisits it.
func (p Provenance) heuristic() bool { return p.RunID == "" }

// TopicOperation is what a topic proposal would do to the ledger.
//
// The four are one output kind rather than four features, which is §4.13's
// last paragraph: a new topic, a split, a merge and a retirement are all
// "Babel says the naming is wrong and here is what it should be", and the
// operator's ruling on the proposal is what applies whichever it is.
type TopicOperation string

// The topic operations.
const (
	// TopicCreate names something the ledger does not hold.
	TopicCreate TopicOperation = "create"
	// TopicSplit says one topic covers two things and names the second.
	TopicSplit TopicOperation = "split"
	// TopicMerge says two topics are one thing.
	TopicMerge TopicOperation = "merge"
	// TopicRetire says a topic should never have existed.
	TopicRetire TopicOperation = "retire"
)

// Valid reports whether this is one of the four operations.
func (o TopicOperation) Valid() bool {
	switch o {
	case TopicCreate, TopicSplit, TopicMerge, TopicRetire:
		return true
	}
	return false
}

// TopicOperations is the closed vocabulary, in the order a reader meets them.
func TopicOperations() []TopicOperation {
	return []TopicOperation{TopicCreate, TopicSplit, TopicMerge, TopicRetire}
}

// creates reports whether applying this operation mints an entity, which is
// what decides whether a plan needs a draft and an identity.
func (o TopicOperation) creates() bool { return o == TopicCreate || o == TopicSplit }

// RulingState is where a plan the operator rules on stands: proposed and
// unruled, applied by him, or declined by him.
//
// It is derived from the ruling rather than stored on the plan, for §4.8's
// reason: the plan row is immutable, and a state column on it would be a field
// that can disagree with the append-only record of what the operator did.
//
// One vocabulary serves every plan kind — a topic plan and a backlog plan —
// because the operator's act is the same act: he reads the proposal that
// carries it and rules on it. A second set of three words for the second plan
// kind would be two names for one state.
type RulingState string

// The plan states.
const (
	RulingOpen     RulingState = "open"
	RulingApplied  RulingState = "applied"
	RulingDeclined RulingState = "declined"
)

// TopicPlan is what a topic proposal would do, attached to the proposal record
// the operator rules on.
//
// Considered is the part that is easy to leave out and worth the field. §4.13
// requires a proposal to name the existing entities it weighed and rejected,
// because "this is new" is a claim about the whole ledger and an operator
// cannot check it against a proposal that only says what it wants.
type TopicPlan struct {
	// ProposalID is the frontier proposal record this plan explains. It is
	// the key: the operator's ruling on that record is what applies or
	// declines this.
	ProposalID string
	// Operation is which of the four acts the ruling would perform.
	Operation TopicOperation
	// Targets are the entities the operation acts on, as canonical ids: the
	// topic to split or retire in Targets[0], and for a merge the source in
	// Targets[0] and the identity that survives in Targets[1].
	Targets []string
	// Identity is the dedup key and the binding a second plan for the same
	// thing would arrive with: a normalized remote, the common directory
	// every worktree shares, or a slug for a concept. It belongs to a
	// create or a split, which are the operations that mint a subject; a
	// merge and a retirement are keyed by their targets instead.
	Identity string
	// Entity is the subject a create would mint, or the new part a split
	// carves out. It is nil for a merge and a retirement, which create
	// nothing.
	Entity *EntityDraft
	// Filings are the records the application files: under the created
	// entity for a create, under the new part for a split. A merge and a
	// retirement carry none — a merge's filings follow the resolution, and
	// a retirement returns its topic's filings to the backlog.
	Filings []FilingDraft
	// Considered are the entity IDs weighed and rejected.
	Considered []string
	// Reasoning is why this is the right act, in the run's words, and
	// includes why each considered entity was rejected.
	Reasoning string
	// Sessions is how much evidence stands behind the plan. It is what
	// "materially new evidence" is measured against after a decline: §4.13
	// suppresses a refused proposal until the world says more than it did
	// when the operator refused it.
	Sessions int
	// By is what produced the plan.
	By Provenance

	// The read-back half. None of it is supplied by a proposer; the store
	// fills it from the plan row and the ruling recorded against it.

	// State is open until the operator rules, and then what he ruled.
	State RulingState
	// CreatedAt is when the plan was recorded.
	CreatedAt time.Time
	// RuledBy and RuledAt attribute the ruling, and Reason keeps the
	// operator's own words verbatim — which is the evidence §4.13 has the
	// triage recipe read before it proposes again.
	RuledBy string
	RuledAt time.Time
	Reason  string
	// EntityID is what an applied create or split produced, and
	// ResolutionID the merge or split history an application appended.
	EntityID     string
	ResolutionID string
}

// Name is what the operator would call the topic this plan is about, and is
// empty for the two operations that name nothing new.
func (p TopicPlan) Name() string {
	if p.Entity == nil {
		return ""
	}
	return p.Entity.Subject.DisplayName
}

// Kind is the entity kind a create or a split would mint, empty otherwise.
func (p TopicPlan) Kind() EntityKind {
	if p.Entity == nil {
		return ""
	}
	return p.Entity.Subject.Kind
}

// Records are the frontier records the plan would file, which is what a
// surface counts to say how much a ruling would move.
func (p TopicPlan) Records() []frontier.Ref {
	out := make([]frontier.Ref, 0, len(p.Filings))
	for _, filing := range p.Filings {
		out = append(out, filing.Record)
	}
	return out
}

func (in TopicPlan) validate() error {
	if strings.TrimSpace(in.ProposalID) == "" {
		return fmt.Errorf("%w: a topic plan names no proposal record", ErrInvalidValue)
	}
	if !in.Operation.Valid() {
		return fmt.Errorf("%w: topic operation %q", ErrInvalidValue, in.Operation)
	}
	if strings.TrimSpace(in.Reasoning) == "" {
		return fmt.Errorf("%w: topic plan for %s does not say why", ErrInvalidValue, in.ProposalID)
	}
	if in.Sessions < 0 {
		return fmt.Errorf("%w: topic plan counts %d sessions", ErrInvalidValue, in.Sessions)
	}
	if err := checkNoCredential("topic reasoning", in.Reasoning); err != nil {
		return err
	}
	if err := in.validateShape(); err != nil {
		return err
	}
	for _, target := range in.Targets {
		if strings.TrimSpace(target) == "" {
			return fmt.Errorf("%w: topic plan names an empty target", ErrInvalidValue)
		}
	}
	if in.Entity != nil {
		if err := in.Entity.validate(); err != nil {
			return err
		}
		if err := checkNoCredential("topic name", in.Entity.Subject.DisplayName); err != nil {
			return err
		}
	}
	for _, filing := range in.Filings {
		if err := filing.validate(); err != nil {
			return err
		}
		if filing.EntityID != "" {
			// The entity a filing belongs to is the one this
			// application creates, and naming another would be a way
			// to file a record anywhere from a plan the operator
			// accepted for a different reason.
			return fmt.Errorf("%w: a topic plan's filing names entity %s; "+
				"the entity it applies to is the one it creates", ErrInvalidValue, filing.EntityID)
		}
	}
	return nil
}

// validateShape checks the fields each operation must and must not carry.
//
// It is stated per operation rather than as a union of optional fields
// because the four are genuinely different acts: a merge with a proposed
// entity draft and a create with two targets are both plans that could never
// be applied, and refusing them at the door is what keeps the application
// free of "this cannot happen" branches.
func (in TopicPlan) validateShape() error {
	switch in.Operation {
	case TopicCreate:
		if len(in.Targets) != 0 {
			return fmt.Errorf("%w: a create names %d existing topics; it names none",
				ErrInvalidValue, len(in.Targets))
		}
	case TopicSplit:
		if len(in.Targets) != 1 {
			return fmt.Errorf("%w: a split names the one topic it divides, and this names %d",
				ErrInvalidValue, len(in.Targets))
		}
	case TopicMerge:
		if len(in.Targets) != 2 {
			return fmt.Errorf("%w: a merge names the topic to fold and the topic to keep, "+
				"and this names %d", ErrInvalidValue, len(in.Targets))
		}
		if in.Targets[0] == in.Targets[1] {
			return fmt.Errorf("%w: a merge folds a topic into itself", ErrInvalidValue)
		}
	case TopicRetire:
		if len(in.Targets) != 1 {
			return fmt.Errorf("%w: a retirement names the one topic it retires, and this names %d",
				ErrInvalidValue, len(in.Targets))
		}
	}
	if in.Operation.creates() {
		if strings.TrimSpace(in.Identity) == "" {
			return fmt.Errorf("%w: a %s topic plan has no identity to bind",
				ErrInvalidValue, in.Operation)
		}
		if in.Entity == nil {
			return fmt.Errorf("%w: a %s topic plan describes no entity",
				ErrInvalidValue, in.Operation)
		}
		return nil
	}
	if strings.TrimSpace(in.Identity) != "" {
		return fmt.Errorf("%w: a %s topic plan binds no identity, and this carries one",
			ErrInvalidValue, in.Operation)
	}
	if in.Entity != nil {
		return fmt.Errorf("%w: a %s topic plan creates no entity, and this describes one",
			ErrInvalidValue, in.Operation)
	}
	if len(in.Filings) != 0 {
		return fmt.Errorf("%w: a %s topic plan files nothing; a merge's filings follow the "+
			"resolution and a retirement's return to the backlog", ErrInvalidValue, in.Operation)
	}
	return nil
}

// subjectMatter is what two plans about the same thing agree on, and is the
// column a duplicate and a decline are found by.
//
// A create and a split are about the identity they would bind, because that is
// the thing the ledger does not name yet. A merge and a retirement are about
// the entities they act on, in the order the act reads, so that "fold a into
// b" and "fold b into a" are two different claims and each deduplicates
// against itself.
func (in TopicPlan) subjectMatter() string {
	if in.Operation.creates() {
		return string(in.Operation) + ":" + normalizeAlias(in.Identity)
	}
	return string(in.Operation) + ":" + strings.Join(in.Targets, ">")
}

// validateBinding checks a fact a plan carries, which arrives with neither a
// subject nor a time: both belong to the application, which is the only act
// that can supply them. Everything else — the predicate, the value's type and
// vocabulary, the sensitivity, the prose — is checked now, so a plan cannot be
// stored carrying a fact that could never be asserted.
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

// validate checks the subject a plan would mint. The aliases are checked here
// rather than at application because a plan the operator is asked to accept
// must be one that can actually be applied.
func (in *EntityDraft) validate() error {
	if in == nil {
		return fmt.Errorf("%w: a topic plan carries no subject", ErrInvalidValue)
	}
	if !in.Subject.Kind.valid() {
		return fmt.Errorf("%w: entity kind %q", ErrInvalidValue, in.Subject.Kind)
	}
	if strings.TrimSpace(in.Subject.DisplayName) == "" {
		return fmt.Errorf("%w: a proposed topic has no name", ErrInvalidValue)
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
		if fact.SubjectID != "" {
			return fmt.Errorf("%w: a binding fact names subject %q before the subject exists",
				ErrInvalidValue, fact.SubjectID)
		}
		if fact.Authority.Kind != "" {
			return fmt.Errorf("%w: a proposal may not attribute a fact; the accepting operator does",
				ErrNotAuthoritative)
		}
		if err := fact.validateBinding(); err != nil {
			return err
		}
	}
	return nil
}

// validate checks one filing a plan would perform. The record reference and
// the rationale are this package's business; whether the author may file, and
// whether the record exists, is internal/frontier's, and the Filer refuses
// what it will not accept.
func (in FilingDraft) validate() error {
	if in.Record.ID == "" || in.Record.Type == "" {
		return fmt.Errorf("%w: filing names no record", ErrInvalidValue)
	}
	if strings.TrimSpace(in.Rationale) == "" {
		return fmt.Errorf("%w: filing of record %s states no rationale", ErrInvalidValue, in.Record.ID)
	}
	return checkNoCredential("filing rationale", in.Rationale)
}

// topicPayload is the §9 encryption-bound half of a plan. Everything in it is
// operator- or corpus-derived vocabulary — a name, a path, a remote, a reason
// — so none of it may sit in a plaintext column; the row's own columns are the
// subject matter's digest, the operation, the evidence weight and the time.
type topicPayload struct {
	Identity   string         `json:"identity,omitempty"`
	Targets    []string       `json:"targets,omitempty"`
	Entity     *EntityDraft   `json:"entity,omitempty"`
	Filings    []FilingDraft  `json:"filings,omitempty"`
	Considered []string       `json:"considered,omitempty"`
	Reasoning  string         `json:"reasoning"`
	Records    []frontier.Ref `json:"records,omitempty"`
	By         Provenance     `json:"by"`
}

// EntityDraft is the subject a topic plan would mint, with the facts that bind
// it. It is NewSubject plus the binding because the two are one act: an entity
// created without what binds it to something real is a name, and §4.13 is
// about the difference.
type EntityDraft struct {
	Subject NewSubject  `json:"subject"`
	Binding []FactInput `json:"binding,omitempty"`
}

// FilingDraft is one record a topic plan would file.
//
// EntityID is empty in a plan: the topic does not exist when the plan is
// written, so it cannot be named. The application fills it in with the entity
// it created.
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

// TopicAcceptance is what applying a topic plan produced.
//
// It reports the whole act rather than an identifier, because the act is
// several records across two components and a caller that had to re-read them
// could not tell a partial application from a complete one. Filings is what
// the Filer actually accepted; when it is shorter than the plan's records, the
// error says which are missing and the records are unfiled — which is the
// triage backlog §4.13 already has a recipe for, and not a lost acceptance.
type TopicAcceptance struct {
	ID         string
	ProposalID string
	// Operation is what was applied, so a caller that answers a ruling can
	// say what the ruling did without re-reading the plan.
	Operation TopicOperation
	// EntityID is the entity a create minted or the part a split carved
	// out, empty for a merge and a retirement. Targets are the entities the
	// act touched.
	EntityID string
	Targets  []string
	Entity   Entity
	Aliases  []Alias
	Facts    []Fact
	// Resolution is the §4.8 identity history a merge or a split appended,
	// nil for a create and a retirement.
	Resolution *Resolution
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

// TopicObservation is one repository identity a host observed, with what
// stands behind it.
//
// It is evidence and nothing else. §4.13 makes the repository's own identity
// the one binding observable without a model, so a scan may hand this to the
// filing run as "here is what this machine sees and the ledger does not name";
// what it may not do is turn it into a proposal, because a proposal is a run's
// output and a scan is not a run.
//
// It is the catalog's vocabulary rather than the ledger's on purpose: this
// package cannot see a session row — internal/web and internal/cli own those,
// and the ledger must not import the corpus — so the observer states what it
// observed.
type TopicObservation struct {
	// Identity is the repository's own identity: the normalized remote when
	// there is one, and the common directory every worktree shares when
	// there is not.
	Identity string
	// Remote is the normalized remote, empty for a checkout with no origin.
	// It is separate from Identity because a repository with a remote is
	// bound by it *and* lives in directories, and the binding facts differ.
	Remote string
	// Name is what the operator calls it.
	Name string
	// Kind is the entity kind the identity suggests. Empty means
	// repository, which is the only kind this deployment can observe
	// without a model — and §4.13 forbids assuming it anywhere else.
	Kind EntityKind
	// Paths are the workspaces that resolved to this identity. They are
	// evidence and would become typed aliases, never the topic itself.
	Paths []string
	// Sessions and Checkouts are how much stands behind the identity.
	Sessions  int
	Checkouts int
	// Records are the frontier records whose evidence walks back to this
	// identity — what a run would consider filing under the topic it names.
	Records []frontier.Ref
}
