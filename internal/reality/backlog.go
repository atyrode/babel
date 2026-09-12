package reality

// The backlog a deferred hypothesis leaves (SPEC.md §4.13, last paragraph).
//
// "The backlog this leaves — hypotheses deferred and never revisited — is
// worked by Babel through the chain": a recipe reads a deferred candidate with
// its observations and proposes, as an ordinary proposal, to consolidate it
// with others into a finding, to supersede it with a candidate that says the
// same thing better, to retire it with a reason a reader could check, or to
// promote one of its observations to a fact about a named entity.
//
// This file is the plan half of that, and it is topic.go's shape on purpose.
// A run publishes the chain through the frontier and attaches the plan to the
// proposal record the operator rules on; his acceptance is what applies it,
// and nothing else can. The four operations differ in what an acceptance
// performs and in nothing else: one output kind, one ruling, one act.
//
// Three properties are refused writes rather than conventions, exactly as they
// are for a topic plan. A plan is keyed by the proposal it explains, one per
// proposal, and immutable. Two runs proposing the same act on the same
// candidates collide, because the subject matter — the operation with the
// hypotheses it acts on — is sealed and keyed by digest in the clear. And a
// declined plan stays declined until something new stands behind it.
//
// Nothing here deletes anything. A consolidated, superseded or retired
// candidate keeps its row, its history and its observations; what it gains is
// one appended status event that says a later record speaks for it.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// BacklogOperation is which of §4.13's four answers a backlog proposal
// carries.
//
// They are one output kind with four operations for TopicOperation's reason:
// the operator's act on each is identical — he reads a proposal and accepting
// it applies it — so a surface offering four different controls would be
// offering him four decisions where the spec describes one.
type BacklogOperation string

// The four acts a backlog proposal may carry (§4.13).
const (
	// BacklogConsolidate folds several deferred candidates into one finding
	// that speaks for their observations.
	BacklogConsolidate BacklogOperation = "consolidate"
	// BacklogSupersede says a newer candidate states the same thing better.
	BacklogSupersede BacklogOperation = "supersede"
	// BacklogRetire says a candidate is not worth returning to, with the
	// reason a reader can check.
	BacklogRetire BacklogOperation = "retire"
	// BacklogPromote turns one observation into a fact about a named entity
	// in the Reality Ledger.
	BacklogPromote BacklogOperation = "promote"
)

// BacklogOperations is the closed vocabulary, in the order a reader meets it.
func BacklogOperations() []BacklogOperation {
	return []BacklogOperation{BacklogConsolidate, BacklogSupersede, BacklogRetire, BacklogPromote}
}

// Valid reports whether this is one of the four operations.
func (o BacklogOperation) Valid() bool { return slices.Contains(BacklogOperations(), o) }

// BacklogPlan is what accepting a backlog proposal would do, attached to the
// proposal record the operator rules on.
//
// It carries identifiers and one fact, and deliberately no prose the chain
// already holds: the argument for the act is the observation, the finding and
// the proposal the run published, and a second copy of it here would be a
// second version of the reasoning an operator read.
type BacklogPlan struct {
	// ProposalID is the frontier proposal this plan explains. It is the
	// key: the operator's ruling on that record applies or declines this.
	ProposalID string
	// Operation is which of the four acts the ruling would perform.
	Operation BacklogOperation
	// Hypotheses are the deferred candidates the act settles: the ones a
	// consolidation folds into the finding, or the single candidate a
	// supersession, a retirement or a promotion answers.
	Hypotheses []string
	// SupersededBy is the newer candidate that says it better. It belongs
	// to a supersession and to nothing else.
	SupersededBy string
	// Finding is the record the run already minted consolidating the named
	// candidates' observations. It belongs to a consolidation: the finding
	// exists before the operator rules, because §4.4 is how observations
	// are consolidated at all, and what his acceptance adds is that the
	// candidates behind it are settled.
	Finding string
	// Observation is the claim a promotion turns into a fact, and Fact is
	// the assertion itself with no subject authority: §4.8 gives the
	// accepting operator the authority for every fact a proposal carries,
	// and a run filling it would be asserting under an authority it does
	// not have.
	Observation string
	Fact        *FactInput
	// Reasoning is why the act is right, in the run's words. It is what a
	// refusal of a later identical plan is measured against and what the
	// ruling records.
	Reasoning string
	// Evidence is how much stands behind the plan — the observations the
	// act rests on — and is what "materially new" is measured against after
	// a decline, on TopicPlan.Sessions' terms.
	Evidence int
	// By is what produced the plan.
	By Provenance

	// The read-back half. None of it is supplied by a proposer; the store
	// fills it from the plan row and the ruling recorded against it.

	// State is open until the operator rules, and then what he ruled.
	State RulingState
	// CreatedAt is when the plan was recorded.
	CreatedAt time.Time
	// RuledBy, RuledAt and Reason attribute the ruling and keep the
	// operator's own words verbatim.
	RuledBy string
	RuledAt time.Time
	Reason  string
	// FactID is what an applied promotion asserted, and Settled the
	// candidates whose status the acceptance moved.
	FactID  string
	Settled []string
}

// Subject is the single candidate a supersession, a retirement or a promotion
// answers, and the first of the candidates a consolidation folds. It is what a
// surface titles the plan by.
func (p BacklogPlan) Subject() string {
	if len(p.Hypotheses) == 0 {
		return ""
	}
	return p.Hypotheses[0]
}

// Settles reports the status each named candidate takes when the operator
// accepts this plan, which is the whole of what an acceptance does to the
// frontier.
//
// A consolidation promotes them, because §4.2's `promoted` is exactly what a
// candidate that produced a durable record is; a supersession and a retirement
// take the two statuses §4.13 adds. A promotion is `promoted` for the same
// reason a consolidation is: the observation became a fact the ledger holds.
func (p BacklogPlan) Settles() frontier.Status {
	switch p.Operation {
	case BacklogSupersede:
		return frontier.StatusSuperseded
	case BacklogRetire:
		return frontier.StatusRetired
	default:
		return frontier.StatusPromoted
	}
}

// settles reports whether this candidate is one the plan settles, as opposed
// to the newer candidate a supersession names — which the act does not settle
// and which only has to still be a candidate worth pointing at.
func (p BacklogPlan) settles(id string) bool {
	return slices.Contains(p.Hypotheses, id)
}

func (in BacklogPlan) validate() error {
	if strings.TrimSpace(in.ProposalID) == "" {
		return fmt.Errorf("%w: a backlog plan names no proposal record", ErrInvalidValue)
	}
	if !in.Operation.Valid() {
		return fmt.Errorf("%w: backlog operation %q", ErrInvalidValue, in.Operation)
	}
	if strings.TrimSpace(in.Reasoning) == "" {
		return fmt.Errorf("%w: backlog plan for %s does not say why", ErrInvalidValue, in.ProposalID)
	}
	if in.Evidence < 0 {
		return fmt.Errorf("%w: backlog plan counts %d observations", ErrInvalidValue, in.Evidence)
	}
	if err := checkNoCredential("backlog reasoning", in.Reasoning); err != nil {
		return err
	}
	for _, id := range in.Hypotheses {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("%w: a backlog plan names an empty candidate", ErrInvalidValue)
		}
	}
	if len(sortedUnique(in.Hypotheses)) != len(in.Hypotheses) {
		return fmt.Errorf("%w: a backlog plan names the same candidate twice", ErrInvalidValue)
	}
	return in.validateShape()
}

// validateShape checks the fields each operation must and must not carry.
//
// Stated per operation rather than as a union of optional fields, for
// TopicPlan.validateShape's reason: a retirement carrying a fact and a
// promotion naming a superseding candidate are both plans that could never be
// applied, and refusing them at the door is what keeps the application free of
// branches for states that cannot exist.
func (in BacklogPlan) validateShape() error {
	switch in.Operation {
	case BacklogConsolidate:
		if len(in.Hypotheses) < 2 {
			return fmt.Errorf("%w: a consolidation folds at least two candidates, and this names %d",
				ErrInvalidValue, len(in.Hypotheses))
		}
		if strings.TrimSpace(in.Finding) == "" {
			return fmt.Errorf("%w: a consolidation names the finding that consolidates them",
				ErrInvalidValue)
		}
	default:
		if len(in.Hypotheses) != 1 {
			return fmt.Errorf("%w: a %s answers one candidate, and this names %d",
				ErrInvalidValue, in.Operation, len(in.Hypotheses))
		}
		if strings.TrimSpace(in.Finding) != "" {
			return fmt.Errorf("%w: a %s consolidates nothing, and this names a finding",
				ErrInvalidValue, in.Operation)
		}
	}
	if in.Operation == BacklogSupersede {
		if strings.TrimSpace(in.SupersededBy) == "" {
			return fmt.Errorf("%w: a supersession names the candidate that says it better",
				ErrInvalidValue)
		}
		if in.SupersededBy == in.Hypotheses[0] {
			return fmt.Errorf("%w: a candidate cannot supersede itself", ErrInvalidValue)
		}
	} else if strings.TrimSpace(in.SupersededBy) != "" {
		return fmt.Errorf("%w: a %s supersedes nothing, and this names a successor",
			ErrInvalidValue, in.Operation)
	}
	if in.Operation != BacklogPromote {
		if in.Fact != nil || strings.TrimSpace(in.Observation) != "" {
			return fmt.Errorf("%w: a %s asserts no fact, and this carries one",
				ErrInvalidValue, in.Operation)
		}
		return nil
	}
	if in.Fact == nil {
		return fmt.Errorf("%w: a promotion carries the fact it would record", ErrInvalidValue)
	}
	if strings.TrimSpace(in.Observation) == "" {
		return fmt.Errorf("%w: a promotion names the observation the fact comes from", ErrInvalidValue)
	}
	if strings.TrimSpace(in.Fact.SubjectID) == "" {
		return fmt.Errorf("%w: a promotion names the entity the fact is about", ErrInvalidValue)
	}
	if in.Fact.Authority.Kind != "" {
		return fmt.Errorf("%w: a proposal may not attribute a fact; the accepting operator does",
			ErrNotAuthoritative)
	}
	return in.Fact.validateProposal()
}

// validateProposal checks a fact a backlog plan carries.
//
// It arrives with a subject — the entity is one the ledger already holds, so
// there is nothing pending about it — and with no authority and no times:
// those belong to the acceptance, which is the only act that can supply them.
// Everything else is checked now, so a plan cannot be stored carrying a fact
// that could never be asserted.
func (in FactInput) validateProposal() error {
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

// subjectMatter is what two plans about the same thing agree on, and is the
// column a duplicate and a decline are found by.
//
// The operation with the candidates it settles, in a stable order, plus the
// successor of a supersession and the predicate of a promotion: "supersede h1
// with h2" and "supersede h1 with h3" are two different claims and each
// deduplicates against itself, while two runs that both want to retire h1 are
// one decision for the operator.
func (in BacklogPlan) subjectMatter() string {
	parts := []string{string(in.Operation)}
	parts = append(parts, sortedUnique(in.Hypotheses)...)
	if in.SupersededBy != "" {
		parts = append(parts, "by:"+in.SupersededBy)
	}
	if in.Fact != nil {
		parts = append(parts, "fact:"+string(in.Fact.Predicate)+":"+in.Fact.SubjectID)
	}
	return strings.Join(parts, ">")
}

// backlogPayload is the §9 encryption-bound half of a plan. The reasoning is
// model prose and the fact is operator-domain vocabulary, so neither may sit
// in a plaintext column; the row's own columns are the subject matter's
// digest, the operation, the evidence weight and the time.
type backlogPayload struct {
	Hypotheses   []string   `json:"hypotheses,omitempty"`
	SupersededBy string     `json:"superseded_by,omitempty"`
	Finding      string     `json:"finding,omitempty"`
	Observation  string     `json:"observation,omitempty"`
	Fact         *FactInput `json:"fact,omitempty"`
	Reasoning    string     `json:"reasoning"`
	By           Provenance `json:"by"`
}

// BacklogAcceptance is what applying a backlog plan produced.
//
// It reports the whole act rather than an identifier, for TopicAcceptance's
// reason: the act spans two components and a caller that had to re-read them
// could not tell a partial application from a complete one. Settled is what
// the frontier actually accepted; when it is shorter than the plan's
// candidates, the error says which are missing and those candidates are still
// deferred — which is the backlog this recipe already works, and not a lost
// acceptance.
type BacklogAcceptance struct {
	ID         string
	ProposalID string
	Operation  BacklogOperation
	// Status is what the accepted act made of the candidates it settled.
	Status frontier.Status
	// Settled are the candidates whose status the acceptance moved, and
	// FactID the fact a promotion asserted.
	Settled    []string
	FactID     string
	Fact       Fact
	Actor      string
	RecordedAt time.Time
}

// Settlement is one candidate's status change as an acceptance performs it.
//
// The operator travels because §4.2's history is attributed and this
// transition is his: Babel proposed it, he accepted it, and a status event
// that named the run would say a run settled a candidate on its own authority.
type Settlement struct {
	HypothesisID string
	Status       frontier.Status
	// SupersededBy is the candidate that now speaks for this one, empty for
	// every operation but a supersession. The link is written beside the
	// status because §4.2 keeps the two separate: the relationship is a
	// fact about two records and the status is what exploration does next.
	SupersededBy string
	Operator     string
	Reason       string
}

// BacklogFrontier is internal/frontier's status and link writes as an
// acceptance uses them.
//
// It is injected rather than called directly for HypothesisSink's reason: the
// frontier is a separate component with its own connection to the same durable
// file, and SQLite's write lock is per file, so this package's transaction
// must not be open while the frontier writes.
type BacklogFrontier interface {
	// Settle appends the status the accepted plan calls for, and the
	// supersession link when there is one.
	Settle(ctx context.Context, in Settlement) error
	// Status reports where a candidate currently stands, so an acceptance
	// can refuse a plan the frontier has moved past and name the state.
	Status(ctx context.Context, hypothesisID string) (frontier.Status, error)
}
