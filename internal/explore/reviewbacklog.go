package explore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// This file is SPEC.md §4.13's last paragraph as the review runner carries it
// out: the pass that works through the hypotheses a run deferred and nobody
// came back to, the answers it may give, and what each of them becomes
// durable as.
//
// "Observations are evidence, not posts." An observation hangs off exactly one
// hypothesis, findings consolidate them, proposals address findings — so the
// backlog a deferral leaves is not a list of orphaned claims to tidy but a
// pile of hypotheses whose evidence nobody has read since. This pass reads one
// of them with its observations and says what should become of it.
//
// It is a work job in every mechanical respect — one drawn assignment, one
// claimed reservation, one supervised worker, one attributed record, one
// receipt — and it is reviewfiling.go's twin in the one respect that matters:
// what it produces is a *proposal*, published through the ordinary chain, that
// the operator rules on. Nothing here settles a candidate. A status a run set
// on its own authority would be Babel deciding what it knows, which is the
// boundary §4.13's third reading draws around the whole recursion.
//
// Five answers, and `keep` is one of them. A pass that read a deferred
// candidate and judged that nothing should happen to it yet has worked the
// backlog entry: it is a completion with a reason, not a skip, because a skip
// means the material could not be read.

// ErrBacklogResult reports a result the material it was shown contradicts: a
// candidate, an observation, an entity or a predicate that was not served to
// this pass.
//
// It is a malformed result and not a failure of the act, and the distinction
// is reviewfiling.go's. A consolidation of candidates nobody showed it, or a
// promotion onto an entity the ledger does not hold, is a name the pass
// invented; carrying it would let an invented identifier reach a plan whose
// acceptance settles records.
var ErrBacklogResult = errors.New("explore: the backlog act named something the material does not hold")

// ErrBacklogKnown reports an act the ledger already holds: an open plan
// already proposes it, or the operator declined it and nothing new has been
// said since.
//
// It is not a failure and it is not an act. The pass did its work and the
// answer was already in the ledger, so the assignment ends as a recorded skip
// with that reason — recording it as a failure would make the recipe fight the
// operator's refusal.
var ErrBacklogKnown = errors.New("explore: the ledger already holds this backlog act")

// BacklogOperation is which of §4.13's four acts on a deferred candidate a
// proposal carries.
type BacklogOperation string

// The four acts a backlog proposal may carry (§4.13).
const (
	// BacklogConsolidate folds several deferred candidates into one finding.
	BacklogConsolidate BacklogOperation = "consolidate"
	// BacklogSupersede replaces a candidate with a newer one that says the
	// same thing better.
	BacklogSupersede BacklogOperation = "supersede"
	// BacklogRetire settles a candidate with a reason a reader can check.
	BacklogRetire BacklogOperation = "retire"
	// BacklogPromote turns one observation into a fact about a named entity.
	BacklogPromote BacklogOperation = "promote"
)

// BacklogOperations lists the operations in a stable order, which is also the
// order the recipe's prose states them in.
func BacklogOperations() []BacklogOperation {
	return []BacklogOperation{BacklogConsolidate, BacklogSupersede, BacklogRetire, BacklogPromote}
}

// FactPredicate is the Reality Ledger's predicate vocabulary as a promotion
// may name it. Its values come from internal/reality for ReviewVote's reason:
// one declaration serves the enum a worker is handed and the refusal this
// package makes.
type FactPredicate string

// Values lists the predicate vocabulary for the generated result schema.
func (FactPredicate) Values() []string {
	predicates := reality.Predicates()
	out := make([]string, 0, len(predicates))
	for _, predicate := range predicates {
		out = append(out, string(predicate))
	}
	return out
}

// BacklogService is the material a backlog pass reads and the chain it
// publishes, as this runner uses them.
//
// It is declared here rather than in internal/frontier or internal/reality for
// TopicService's reason: it is the consumer's view. What makes an act safe —
// the append-only status history, the plan the operator's acceptance applies,
// the dedup that stops two runs proposing one act twice — is the stores' own
// and is not this package's to restate.
type BacklogService interface {
	// Material is what this pass is shown about the candidate it was drawn
	// for: the candidate with its observations, the topics it is filed
	// under, the sibling candidates a consolidation or a supersession could
	// name, and the entities and predicates a promotion could use.
	Material(ctx context.Context, candidate evaluation.Subject) (BacklogMaterial, error)
	// Propose publishes the act as an ordinary chain — observation,
	// finding, proposal — and records the plan the operator's acceptance
	// applies, returning the proposal's id. A plan the ledger already
	// carries is not an error: ErrBacklogKnown says the answer was already
	// there.
	Propose(ctx context.Context, plan BacklogPlan) (string, error)
}

// BacklogMaterial is everything a backlog pass reads before it answers.
//
// It is a projection rather than the stores' own types for reviewTarget's
// reason: what reaches a model is a struct with no field for anything it must
// not be told, so the guarantee survives a field added upstream. Nothing here
// is a judgement about a record — no tally, no rank, no prior assessment —
// because what becomes of a deferred candidate is not a question about how it
// was received.
type BacklogMaterial struct {
	// Candidate is the deferred hypothesis this pass was drawn for.
	Candidate BacklogCandidate `json:"candidate"`
	// Observations are its own evidence, oldest first (§4.3: every
	// observation hangs off exactly one hypothesis).
	Observations []BacklogObservation `json:"observations,omitempty"`
	// Topics are the entities the candidate is filed under, by name.
	Topics []string `json:"topics,omitempty"`
	// Siblings are other candidates filed under the same topics that share
	// the candidate's key terms, bounded by the caller. They are what a
	// consolidation folds and what a supersession names, and a pass may
	// name no candidate it was not shown.
	Siblings []BacklogCandidate `json:"siblings,omitempty"`
	// Entities are the ledger's entities a fact could be promoted onto, and
	// Predicates the closed vocabulary such a fact may use.
	Entities   []LedgerTopic      `json:"entities,omitempty"`
	Predicates []BacklogPredicate `json:"predicates,omitempty"`
}

// BacklogCandidate is one hypothesis as this pass sees it.
type BacklogCandidate struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	// Status is the candidate's own lifecycle state. A sibling may be live
	// where the drawn candidate is deferred, and which of the two is which
	// is exactly what decides whether a supersession makes sense.
	Status string `json:"status,omitempty"`
	// DeferredAt is when it was last set down, and Note the reason the run
	// gave for setting it down. The note is why a deferral is evidence
	// rather than a date: "out of budget" and "contradicted by the second
	// observation" are different reasons to have stopped.
	DeferredAt time.Time `json:"deferred_at,omitzero"`
	Note       string    `json:"deferral_note,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitzero"`
	Labels     []string  `json:"labels,omitempty"`
	Cues       []string  `json:"origin_cues,omitempty"`
	// Observations is how many claims hang off it, which is the weight a
	// reader judges a consolidation by without being shown every one.
	Observations int `json:"observations,omitempty"`
	// Topics are the entities it is filed under, by name.
	Topics []string `json:"topics,omitempty"`
}

// BacklogObservation is one claim hanging off the candidate, with what stands
// behind it.
//
// The evidence travels as excerpts rather than as locators alone, because a
// promotion is a claim about the world and the operator accepting it has to be
// able to see what it rests on without opening the archive.
type BacklogObservation struct {
	ID         string              `json:"id"`
	Claim      string              `json:"claim"`
	Category   string              `json:"category,omitempty"`
	Confidence string              `json:"confidence,omitempty"`
	Impact     string              `json:"impact,omitempty"`
	CreatedAt  time.Time           `json:"created_at,omitzero"`
	Evidence   []frontier.Evidence `json:"evidence,omitempty"`
}

// BacklogPredicate is one predicate a promotion may use, with what the ledger
// admits as its value.
type BacklogPredicate struct {
	Name string `json:"name"`
	// Kind is how the value is typed — an enum, free text, or another
	// entity — and Values the closed vocabulary when there is one.
	Kind   string   `json:"value_kind"`
	Values []string `json:"values,omitempty"`
	// Why is the registry's own note about the predicate's freshness, which
	// is what tells a pass whether the fact it wants to record is the kind
	// of thing that expires.
	Why string `json:"why,omitempty"`
}

// BacklogConsolidation is the first answer: several deferred candidates say one
// thing, and a finding should say it.
type BacklogConsolidation struct {
	// Hypotheses are the candidates to fold, named from the material. The
	// drawn candidate is one of them whether or not it is listed, because a
	// consolidation that did not include the candidate this pass was drawn
	// for would be work on somebody else's backlog entry.
	Hypotheses []string `json:"hypotheses"`
	// Finding is the record that would consolidate their observations.
	Finding FindingDraft `json:"finding"`
}

// FindingDraft is the finding a consolidation would write, in §4.4's own
// fields.
type FindingDraft struct {
	Title        string   `json:"title"`
	Pattern      string   `json:"pattern"`
	WhyItMatters string   `json:"why_it_matters"`
	Scope        []string `json:"scope,omitempty"`
}

// Supersession is the second answer: a newer candidate says the same thing
// better.
type Supersession struct {
	// By is the candidate that says it better, named from the material.
	By string `json:"by"`
	// Reason is what the newer one says better. It is required: a
	// supersession with no reason is a deletion with a link attached.
	Reason string `json:"reason"`
}

// Retirement is the third answer: the candidate is not worth returning to.
type Retirement struct {
	// Reason is why, and it has to be checkable by a reader: "no evidence
	// was ever found for it and the system it describes no longer exists"
	// is a reason, and "low value" is a grading.
	Reason string `json:"reason"`
}

// Promotion is the fourth answer: one observation is a durable fact about
// something the ledger already names.
type Promotion struct {
	// Observation is the claim being promoted, named from the material.
	Observation string `json:"observation"`
	// Entity is the ledger entity the fact is about, by any name or alias
	// it is listed under. §4.8 keeps creating one the operator's act, so an
	// unresolvable name is refused rather than turned into a new entity.
	Entity string `json:"entity"`
	// Predicate and Value are the fact itself, in the ledger's own closed
	// vocabulary.
	Predicate FactPredicate `json:"predicate"`
	Value     string        `json:"value"`
	// Reason is why this observation is durable rather than a moment's
	// finding.
	Reason string `json:"reason"`
}

// Kept is the fifth answer: the candidate is worth keeping exactly as it is.
//
// It is an answer and not a refusal to work, on NoTopic's terms. A backlog
// full of candidates that are genuinely still open is a healthy backlog, and a
// pass that could only ever propose an act would consolidate, supersede or
// retire something in order to have produced output.
type Kept struct {
	Reason string `json:"reason"`
}

// BacklogTarget is one candidate an act settles, resolved against the material
// this pass was shown.
type BacklogTarget struct {
	ID        string
	Statement string
}

// BacklogPlan is one backlog act as this pass reached it, handed to the
// consumer that publishes it.
//
// It is the semantics and not the wording, on TopicPlan's terms: the consumer
// writes the chain — observation, finding, proposal — because that is where
// the frontier is, and composes the prose mechanically from these facts. What
// is a judgement rather than a translation was decided here, in the package
// that read the candidate and its evidence.
type BacklogPlan struct {
	// Candidate is the deferred hypothesis this pass was drawn for.
	Candidate BacklogCandidate
	// RunID attributes every record the plan produces.
	RunID string
	// Operation is the act proposed.
	Operation BacklogOperation
	// Hypotheses are the candidates the act settles, resolved: the ones a
	// consolidation folds, or the single candidate the other three answer.
	Hypotheses []BacklogTarget
	// By is the newer candidate a supersession names, nil otherwise.
	By *BacklogTarget
	// Finding is what a consolidation would say, nil otherwise.
	Finding *FindingDraft
	// Entity, Predicate, Value and Observation are a promotion's fact and
	// the claim it comes from, nil and empty otherwise.
	Entity      *LedgerTopic
	Predicate   string
	Value       string
	Observation *BacklogObservation
	// Reason is why the act is right, in the run's own words.
	Reason string
	// Evidence is what the act rests on: the observations of the candidates
	// it settles, because §4.3 forbids an evidence-free observation and
	// this pass cites what the records it read already cited.
	Evidence []frontier.Evidence
}

// validateBacklogResult checks one backlog result against the five shapes, and
// nothing about whether the answer is right.
//
// Exactly one answer, because the five are alternatives rather than fields: a
// pass that consolidated a candidate and retired it in the same breath has not
// decided what should become of it, and storing both would leave the operator
// to guess which the run meant.
func validateBacklogResult(res *ReviewResult) error {
	answers := 0
	for _, given := range []bool{res.Consolidate != nil, res.Supersede != nil, res.Retire != nil,
		res.Promote != nil, res.Keep != nil} {
		if given {
			answers++
		}
	}
	switch {
	case answers == 0:
		return fmt.Errorf("%w: a backlog pass states one of `consolidate`, `supersede`, `retire`, "+
			"`promote` or `keep`", ErrReviewEmpty)
	case answers > 1:
		return fmt.Errorf("explore: a backlog pass states one of `consolidate`, `supersede`, "+
			"`retire`, `promote` or `keep`, not %d of them", answers)
	}
	switch {
	case res.Consolidate != nil:
		return validateConsolidation(res.Consolidate)
	case res.Supersede != nil:
		if strings.TrimSpace(res.Supersede.By) == "" {
			return fmt.Errorf("explore: a supersession names the candidate that says it better")
		}
		if strings.TrimSpace(res.Supersede.Reason) == "" {
			return fmt.Errorf("explore: a supersession says what the newer candidate says better")
		}
	case res.Retire != nil:
		if strings.TrimSpace(res.Retire.Reason) == "" {
			return fmt.Errorf("explore: a retirement states the reason a reader could check")
		}
	case res.Promote != nil:
		return validatePromotion(res.Promote)
	case res.Keep != nil:
		if strings.TrimSpace(res.Keep.Reason) == "" {
			return fmt.Errorf("explore: a candidate kept as it stands still needs the reason it is " +
				"worth keeping")
		}
	}
	return nil
}

// validateConsolidation refuses a consolidation the operator could not act on.
//
// Two candidates at least, because one candidate consolidated into a finding
// is a finding somebody should have written when the observation was made; and
// a finding with a title and nothing else is a heading rather than a claim,
// which is why §4.4's own three fields are all required.
func validateConsolidation(c *BacklogConsolidation) error {
	named := make([]string, 0, len(c.Hypotheses))
	for _, id := range c.Hypotheses {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			named = append(named, trimmed)
		}
	}
	c.Hypotheses = named
	if len(named) < 2 {
		return fmt.Errorf("explore: a consolidation folds at least two candidates, and this names %d",
			len(named))
	}
	if len(slices.Compact(slices.Clone(sortedStrings(named)))) != len(named) {
		return fmt.Errorf("explore: a consolidation names the same candidate twice")
	}
	for field, value := range map[string]string{
		"title":          c.Finding.Title,
		"pattern":        c.Finding.Pattern,
		"why_it_matters": c.Finding.WhyItMatters,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("explore: a consolidation's finding needs its %s", field)
		}
	}
	return nil
}

// validatePromotion refuses a fact the ledger could not hold.
//
// The predicate check is the load-bearing one. §4.8 keeps the predicate
// vocabulary closed so that a focus rule matching on a value is deterministic,
// and a promotion naming a predicate outside it would be a run widening the
// ledger's schema by writing a word into a field.
func validatePromotion(p *Promotion) error {
	if strings.TrimSpace(p.Observation) == "" {
		return fmt.Errorf("explore: a promotion names the observation the fact comes from")
	}
	if strings.TrimSpace(p.Entity) == "" {
		return fmt.Errorf("explore: a promotion names the entity the fact is about")
	}
	if !slices.Contains(FactPredicate.Values(p.Predicate), string(p.Predicate)) {
		return fmt.Errorf("explore: %q is not a predicate the ledger admits (%s)",
			p.Predicate, strings.Join(FactPredicate.Values(p.Predicate), ", "))
	}
	if strings.TrimSpace(p.Value) == "" {
		return fmt.Errorf("explore: a promotion states the value of the fact it records")
	}
	if strings.TrimSpace(p.Reason) == "" {
		return fmt.Errorf("explore: a promotion says why this observation is a durable fact")
	}
	return nil
}

// sortedStrings copies and sorts, so a duplicate check does not reorder the
// candidates the operator will read.
func sortedStrings(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// candidate resolves one identifier against the material this pass was shown:
// the drawn candidate itself or one of the siblings beside it.
func (m *BacklogMaterial) candidate(id string) (BacklogCandidate, bool) {
	want := strings.TrimSpace(id)
	if want == "" || m == nil {
		return BacklogCandidate{}, false
	}
	if m.Candidate.ID == want {
		return m.Candidate, true
	}
	for _, sibling := range m.Siblings {
		if sibling.ID == want {
			return sibling, true
		}
	}
	return BacklogCandidate{}, false
}

// observation resolves one claim by id. It is the drawn candidate's own: an
// observation takes its hypothesis's fate (§4.13), so promoting one that hangs
// off a candidate this pass was not drawn for would be acting on another
// backlog entry's evidence.
func (m *BacklogMaterial) observation(id string) (BacklogObservation, bool) {
	want := strings.TrimSpace(id)
	if want == "" || m == nil {
		return BacklogObservation{}, false
	}
	for _, observation := range m.Observations {
		if observation.ID == want {
			return observation, true
		}
	}
	return BacklogObservation{}, false
}

// entity resolves one entity name the way the pass was shown it: the entity's
// own name, an alias beside it, or the canonical id.
//
// The comparison folds case and surrounding space and nothing else, for
// TopicLedger.topic's reason: §4.8 keeps alias resolution the ledger's, and a
// fuzzier match here would be this package deciding that two spellings mean
// one entity.
func (m *BacklogMaterial) entity(name string) (LedgerTopic, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" || m == nil {
		return LedgerTopic{}, false
	}
	for _, entity := range m.Entities {
		if strings.ToLower(entity.ID) == want || strings.ToLower(entity.Name) == want {
			return entity, true
		}
		for _, alias := range entity.Aliases {
			if strings.ToLower(strings.TrimSpace(alias)) == want {
				return entity, true
			}
		}
	}
	return LedgerTopic{}, false
}

// evidence is every locator the candidate's own observations cite, which is
// what the chain this pass publishes rests on.
func (m *BacklogMaterial) evidence() []frontier.Evidence {
	var out []frontier.Evidence
	for _, observation := range m.Observations {
		out = append(out, observation.Evidence...)
	}
	return out
}

// settle carries out the answer: the store write, and the record of which
// answer it was.
func (r *Reviewer) settle(st *reviewState, res *ReviewResult) (*evaluation.Backlog, error) {
	if r.cfg.Backlog == nil {
		return nil, fmt.Errorf("explore: a backlog pass needs the service it proposes through")
	}
	if res.Keep != nil {
		// Nothing is written anywhere else, and that is the answer. The
		// candidate stays deferred, this assessment is the attributed
		// record that a pass read it and judged it worth keeping, and the
		// draw's own accounting is what stops it being drawn again.
		return &evaluation.Backlog{
			Outcome: evaluation.BacklogKept,
			Reason:  strings.TrimSpace(res.Keep.Reason),
		}, nil
	}
	plan, err := r.backlogPlan(st, res)
	if err != nil {
		return nil, err
	}
	id, err := r.cfg.Backlog.Propose(st.commit, plan)
	if err != nil {
		return nil, fmt.Errorf("explore: propose to %s: %w", plan.Operation, err)
	}
	settled := make([]string, 0, len(plan.Hypotheses))
	for _, target := range plan.Hypotheses {
		settled = append(settled, target.ID)
	}
	return &evaluation.Backlog{
		Outcome:    evaluation.BacklogProposed,
		Proposal:   id,
		Operation:  string(plan.Operation),
		Hypotheses: settled,
		Reason:     plan.Reason,
	}, nil
}

// backlogPlan resolves one answer against the material this pass was shown.
//
// Every identifier is resolved here rather than downstream, and an
// unresolvable one is refused rather than carried: a pass that named a
// candidate nobody showed it produced a malformed result, and passing it on
// would let an invented identifier reach a plan whose acceptance settles
// records.
func (r *Reviewer) backlogPlan(st *reviewState, res *ReviewResult) (BacklogPlan, error) {
	if st.backlog == nil {
		return BacklogPlan{}, fmt.Errorf("explore: a backlog pass was not shown the candidate it is working")
	}
	material := st.backlog
	plan := BacklogPlan{
		Candidate: material.Candidate,
		RunID:     st.opt.Assignment.RunID,
		Evidence:  material.evidence(),
	}
	switch {
	case res.Consolidate != nil:
		plan.Operation = BacklogConsolidate
		finding := res.Consolidate.Finding
		plan.Finding = &finding
		plan.Reason = strings.TrimSpace(finding.WhyItMatters)
		named := res.Consolidate.Hypotheses
		if !slices.Contains(named, material.Candidate.ID) {
			// The drawn candidate is folded whether or not the pass
			// listed it. It is the backlog entry this draw paid for,
			// and a consolidation that settled every candidate but
			// the one it was drawn for would leave it deferred with
			// its evidence spoken for elsewhere.
			named = append([]string{material.Candidate.ID}, named...)
		}
		for _, id := range named {
			target, ok := material.candidate(id)
			if !ok {
				return BacklogPlan{}, fmt.Errorf(
					"%w: the consolidation names %q, which is not a candidate it was shown",
					ErrBacklogResult, id)
			}
			plan.Hypotheses = append(plan.Hypotheses, BacklogTarget{
				ID: target.ID, Statement: target.Statement,
			})
		}
	case res.Supersede != nil:
		plan.Operation = BacklogSupersede
		plan.Reason = strings.TrimSpace(res.Supersede.Reason)
		newer, ok := material.candidate(res.Supersede.By)
		if !ok {
			return BacklogPlan{}, fmt.Errorf(
				"%w: the supersession names %q, which is not a candidate it was shown",
				ErrBacklogResult, res.Supersede.By)
		}
		if newer.ID == material.Candidate.ID {
			return BacklogPlan{}, fmt.Errorf("%w: a candidate cannot supersede itself",
				ErrBacklogResult)
		}
		plan.By = &BacklogTarget{ID: newer.ID, Statement: newer.Statement}
		plan.Hypotheses = []BacklogTarget{{
			ID: material.Candidate.ID, Statement: material.Candidate.Statement,
		}}
	case res.Retire != nil:
		plan.Operation = BacklogRetire
		plan.Reason = strings.TrimSpace(res.Retire.Reason)
		plan.Hypotheses = []BacklogTarget{{
			ID: material.Candidate.ID, Statement: material.Candidate.Statement,
		}}
	case res.Promote != nil:
		plan.Operation = BacklogPromote
		plan.Reason = strings.TrimSpace(res.Promote.Reason)
		observation, ok := material.observation(res.Promote.Observation)
		if !ok {
			return BacklogPlan{}, fmt.Errorf(
				"%w: the promotion names observation %q, which does not hang off this candidate",
				ErrBacklogResult, res.Promote.Observation)
		}
		entity, ok := material.entity(res.Promote.Entity)
		if !ok {
			// Refused rather than turned into a proposal to create the
			// entity. §4.8 reserves that to the operator and §4.13
			// routes it through the filing pass, which is the pass
			// that reads what a record is about; inventing an entity
			// here would mint identity from a pass drawn to work a
			// backlog.
			return BacklogPlan{}, fmt.Errorf(
				"%w: the promotion names the entity %q, which the ledger it was shown does not hold",
				ErrBacklogResult, res.Promote.Entity)
		}
		plan.Observation = &observation
		plan.Entity = &entity
		plan.Predicate = string(res.Promote.Predicate)
		plan.Value = strings.TrimSpace(res.Promote.Value)
		plan.Hypotheses = []BacklogTarget{{
			ID: material.Candidate.ID, Statement: material.Candidate.Statement,
		}}
	default:
		return BacklogPlan{}, fmt.Errorf("%w: the backlog pass stated no answer", ErrReviewEmpty)
	}
	return plan, nil
}

// backlogOutcome is the one line a receipt records about what a backlog pass
// did, so the outcome is readable without opening the evaluation store.
func backlogOutcome(act *evaluation.Backlog) string {
	if act == nil {
		return ""
	}
	switch act.Outcome {
	case evaluation.BacklogProposed:
		return act.Operation + " proposed as " + act.Proposal
	case evaluation.BacklogKept:
		return "kept as it stands: " + act.Reason
	}
	return act.Outcome
}
