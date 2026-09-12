package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// This file wires SPEC.md §4.13's backlog pass to the stores that hold what it
// reads and what it produces: internal/frontier's candidates, observations and
// filings, and the Reality Ledger's plan behind the proposal a pass publishes.
//
// It is evaluation_filing.go's twin and deliberately so. internal/explore
// states what a backlog pass needs in its own vocabulary — show me the
// candidate with its evidence and its neighbours, publish this act — and this
// turns each of those into calls the owning stores already validate. No rule
// lives here: the frontier decides what a status event may be, and the ledger
// decides whether a plan duplicates one it already holds.
//
// What this file does compose is wording. §4.13 makes every backlog act an
// ordinary proposal produced through Babel's normal chain, so somebody has to
// turn "supersede h-412 with h-903" into a finding and a proposal an operator
// can read and rule on. That composition is mechanical — the judgement is
// upstream, in the pass that chose the act — and it lives here because this is
// where the frontier is.

// BacklogRecipe is the cookbook asset a backlog pass runs under.
//
// A third recipe beside EvaluationRecipe and FilingRecipe for FilingRecipe's
// reason: §5.1 makes a recipe's body the statement of one method, and a
// reviewer handed instructions for consolidating a backlog would be reading a
// method for an authority it does not have.
const BacklogRecipe = "babel-consolidates-its-backlog"

// backlogEntities bounds how many ledger entities one backlog pass is shown.
//
// Smaller than a filing pass's two hundred, because the question is different.
// A filing pass has to be able to prefer any existing topic, so it needs the
// ledger; a backlog pass only promotes a fact onto an entity when the
// observation is plainly about it, and a list long enough to browse would
// invite it to find a subject for a claim that has none.
const backlogEntities = 60

// backlogSiblings bounds how many neighbouring candidates one pass is shown.
//
// Twelve. Consolidation is the answer this bound exists for: a pass that is
// shown two neighbours cannot see a pattern, and one shown a hundred is being
// asked to re-derive the whole frontier from a prompt. Twelve is enough to
// notice that four candidates are saying one thing and small enough that every
// one of them is read.
const backlogSiblings = 12

// backlogTermFloor is how many key terms a candidate must share with the drawn
// one to be shown beside it.
//
// Two, because one shared word is a coincidence in a corpus about one system:
// every candidate Babel writes about itself shares "babel". Two words is a
// weak signal and it is stated as one — the siblings are neighbours to
// consider, not a claim that they are related.
const backlogTermFloor = 2

// backlogService is internal/explore's BacklogService over this machine's
// stores.
type backlogService struct {
	front  *frontier.Store
	ledger *reality.Store
	// recipe and version attribute the records a pass produces, which is
	// §4.8's requirement that a proposal name its author.
	recipe  string
	version int
}

// backlogStores is what a backlog surface needs from one machine.
type backlogStores struct {
	Frontier *frontier.Store
	Ledger   *reality.Store
}

// backlogWork is the backlog surface for one machine, or nothing at all.
//
// The nil checks are topicService's and for the same reason: a typed nil in an
// interface field is not a nil interface, and internal/explore reads a non-nil
// field as a facility that is present. A machine whose ledger did not open has
// nowhere to put a plan, and the honest state is the absent one — a backlog
// assignment is then refused before a worker starts.
func backlogWork(in backlogStores) explore.BacklogService {
	if in.Frontier == nil || in.Ledger == nil {
		return nil
	}
	version, _ := recipeVersion(BacklogRecipe)
	return &backlogService{
		front: in.Frontier, ledger: in.Ledger,
		recipe: BacklogRecipe, version: version,
	}
}

// backlogWorkStores names the handles a backlog pass reads and writes through
// on one machine, with every absence stated as an absence.
func backlogWorkStores(state *analysisState, ledger *reality.Store) backlogStores {
	in := backlogStores{Ledger: ledger}
	if state != nil {
		in.Frontier = state.frontier
	}
	return in
}

// Material assembles what a backlog pass is shown about the candidate it was
// drawn for.
func (b *backlogService) Material(ctx context.Context, subject evaluation.Subject) (
	explore.BacklogMaterial, error) {
	if subject.Kind != evaluation.SubjectKindHypothesis {
		return explore.BacklogMaterial{}, fmt.Errorf(
			"babel: a %s is not a candidate the backlog works", subject.Kind)
	}
	record, err := b.front.Hypothesis(ctx, subject.ID)
	if err != nil {
		return explore.BacklogMaterial{}, fmt.Errorf("read the deferred candidate %s: %w", subject.ID, err)
	}
	candidate, err := b.candidate(ctx, record)
	if err != nil {
		return explore.BacklogMaterial{}, err
	}
	material := explore.BacklogMaterial{Candidate: candidate, Topics: candidate.Topics}
	observations, err := b.front.ObservationsFor(ctx, subject.ID)
	if err != nil {
		return explore.BacklogMaterial{}, fmt.Errorf("read the observations of %s: %w", subject.ID, err)
	}
	for _, observation := range observations {
		material.Observations = append(material.Observations, explore.BacklogObservation{
			ID:         observation.ID,
			Claim:      observation.Payload.Claim,
			Category:   observation.Payload.Category,
			Confidence: string(observation.Payload.Confidence),
			Impact:     string(observation.Payload.Impact),
			CreatedAt:  observation.CreatedAt,
			Evidence:   observation.Payload.Evidence,
		})
	}
	if material.Siblings, err = b.siblings(ctx, candidate); err != nil {
		return explore.BacklogMaterial{}, err
	}
	if material.Entities, err = b.entities(ctx); err != nil {
		return explore.BacklogMaterial{}, err
	}
	material.Predicates = backlogPredicates()
	return material, nil
}

// candidate projects one hypothesis as a backlog pass sees it, with the
// deferral that put it in the backlog and the topics it is filed under.
func (b *backlogService) candidate(ctx context.Context, record frontier.Hypothesis) (
	explore.BacklogCandidate, error) {
	out := explore.BacklogCandidate{
		ID:        record.ID,
		Statement: record.Payload.Statement,
		Status:    string(record.Status),
		CreatedAt: record.CreatedAt,
		Labels:    record.Payload.ProvisionalLabels,
		Cues:      record.Payload.OriginCues,
	}
	history, err := b.front.StatusHistory(ctx, record.ID)
	if err != nil {
		return explore.BacklogCandidate{}, fmt.Errorf("read the history of %s: %w", record.ID, err)
	}
	// The latest deferral rather than the first: a candidate deferred,
	// revived and deferred again has been waiting since the last time
	// somebody set it down, and the note is that run's own reason.
	for _, event := range history {
		if event.Status == frontier.StatusDeferred {
			out.DeferredAt, out.Note = event.RecordedAt, event.Payload.Note
		}
	}
	observations, err := b.front.ObservationsFor(ctx, record.ID)
	if err != nil {
		return explore.BacklogCandidate{}, fmt.Errorf("read the observations of %s: %w", record.ID, err)
	}
	out.Observations = len(observations)
	entities, err := b.front.EntitiesFiled(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: record.ID})
	if err != nil {
		return explore.BacklogCandidate{}, fmt.Errorf("read what %s is filed under: %w", record.ID, err)
	}
	for _, id := range entities {
		entity, err := b.ledger.Entity(ctx, id)
		if err != nil {
			// A filing whose entity this machine cannot read costs the
			// pass one topic name, not the material: the candidate and
			// its evidence are what the act rests on.
			continue
		}
		out.Topics = append(out.Topics, entity.Payload.DisplayName)
	}
	return out, nil
}

// siblings are the candidates a consolidation could fold or a supersession
// could name: the ones filed under the same topics that share the drawn
// candidate's key terms, and — when it is filed under none — the rest of the
// backlog on the same terms.
//
// The fallback is not a degradation. §4.13's filing pass is what gives records
// topics and it has not run everywhere; a backlog pass on an unfiled corpus
// still has a backlog to work, and the shared terms are the same weak signal
// either way.
func (b *backlogService) siblings(ctx context.Context, candidate explore.BacklogCandidate) (
	[]explore.BacklogCandidate, error) {
	terms := keyTerms(candidate.Statement)
	if len(terms) == 0 {
		return nil, nil
	}
	seen := map[string]bool{candidate.ID: true}
	var pool []string
	entities, err := b.front.EntitiesFiled(ctx,
		frontier.Ref{Type: frontier.EntityHypothesis, ID: candidate.ID})
	if err != nil {
		return nil, fmt.Errorf("read what %s is filed under: %w", candidate.ID, err)
	}
	for _, id := range entities {
		refs, err := b.front.FiledUnder(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("read what is filed under %s: %w", id, err)
		}
		for _, ref := range refs {
			if ref.Type == frontier.EntityHypothesis && !seen[ref.ID] {
				seen[ref.ID] = true
				pool = append(pool, ref.ID)
			}
		}
	}
	if len(entities) == 0 {
		deferred, err := b.front.Deferred(ctx, backlogSiblings*8)
		if err != nil {
			return nil, fmt.Errorf("read the deferred backlog: %w", err)
		}
		for _, record := range deferred {
			if !seen[record.Hypothesis.ID] {
				seen[record.Hypothesis.ID] = true
				pool = append(pool, record.Hypothesis.ID)
			}
		}
	}

	type scored struct {
		candidate explore.BacklogCandidate
		shared    int
	}
	var found []scored
	for _, id := range pool {
		record, err := b.front.Hypothesis(ctx, id)
		if err != nil {
			continue
		}
		shared := sharedTerms(terms, keyTerms(record.Payload.Statement))
		if shared < backlogTermFloor {
			continue
		}
		sibling, err := b.candidate(ctx, record)
		if err != nil {
			return nil, err
		}
		found = append(found, scored{candidate: sibling, shared: shared})
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].shared != found[j].shared {
			return found[i].shared > found[j].shared
		}
		return found[i].candidate.ID < found[j].candidate.ID
	})
	if len(found) > backlogSiblings {
		found = found[:backlogSiblings]
	}
	out := make([]explore.BacklogCandidate, 0, len(found))
	for _, item := range found {
		out = append(out, item.candidate)
	}
	return out, nil
}

// entities lists the ledger's live entities a fact could be promoted onto.
func (b *backlogService) entities(ctx context.Context) ([]explore.LedgerTopic, error) {
	listings, err := b.ledger.Entities(ctx, reality.EntityQuery{Limit: backlogEntities})
	if err != nil {
		return nil, fmt.Errorf("list the ledger's entities: %w", err)
	}
	out := make([]explore.LedgerTopic, 0, len(listings))
	for _, listing := range listings {
		entity := listing.Entity
		if entity.Role != reality.RoleSelf {
			// A merged-away identity is reachable and is not a subject
			// to record a fact about: the entity that absorbed it
			// speaks for it now.
			continue
		}
		topic := explore.LedgerTopic{
			ID:   entity.ID,
			Name: entity.Payload.DisplayName,
			Kind: string(entity.Kind),
		}
		aliases, err := b.ledger.Aliases(ctx, entity.ID)
		if err != nil {
			return nil, fmt.Errorf("read the aliases of %s: %w", entity.ID, err)
		}
		topic.Aliases, topic.Binding = aliasView(aliases)
		out = append(out, topic)
	}
	return out, nil
}

// backlogPredicates states the ledger's predicate registry as a pass reads it.
func backlogPredicates() []explore.BacklogPredicate {
	predicates := reality.Predicates()
	out := make([]explore.BacklogPredicate, 0, len(predicates))
	for _, predicate := range predicates {
		_, why := predicate.TTL()
		out = append(out, explore.BacklogPredicate{
			Name:   string(predicate),
			Kind:   string(predicate.ValueKind()),
			Values: predicate.Vocabulary(),
			Why:    why,
		})
	}
	return out
}

// Propose publishes one backlog act as an ordinary chain and records the plan
// the operator's acceptance applies.
//
// The order is evaluation_filing.go's and is not rearrangeable. The ledger is
// asked first whether it would take the plan at all, because the plan needs
// the proposal's id and a refusal after the chain was written would leave
// records behind a proposal that never existed. Then the chain is written from
// the evidence outwards, because that is §4.4's development path and the store
// enforces it. The plan is committed last, against the proposal the operator
// will rule on.
//
// The chain has two shapes and the evidence picks between them. An act whose
// candidates carry observations is published as a finding consolidating them
// and a proposal addressing that finding; an act on a bare candidate — a
// hypothesis nobody ever developed, which is most of a long backlog — cannot
// carry a finding at all, because §4.4 refuses a finding with no observation
// behind it, so the proposal is #114's candidate form addressing the claim
// directly. Neither is a degraded version of the other: both say exactly what
// they rest on.
func (b *backlogService) Propose(ctx context.Context, plan explore.BacklogPlan) (string, error) {
	if err := backlogPlanShape(plan); err != nil {
		return "", err
	}
	observations, err := b.actEvidence(ctx, plan)
	if err != nil {
		return "", err
	}
	if plan.Operation == explore.BacklogConsolidate && len(observations) == 0 {
		// Refused rather than published as a candidate proposal. A
		// consolidation is a claim that several candidates' observations
		// say one thing, and a consolidation of no observations is a
		// finding about nothing — which §4.4 refuses and this pass should
		// have answered `keep` or `retire` instead.
		return "", fmt.Errorf("%w: a consolidation needs an observation behind it, and none of the "+
			"candidates it folds carries one", explore.ErrBacklogResult)
	}

	ledgerPlan, err := b.backlogPlan(plan, "", "")
	if err != nil {
		return "", err
	}
	if err := b.ledger.CheckBacklogPlan(ctx, ledgerPlan); err != nil {
		return "", backlogPlanError(err)
	}

	findingID := ""
	if len(observations) > 0 {
		finding, err := b.front.CreateFinding(ctx, frontier.FindingInput{
			RunID:          plan.RunID,
			ObservationIDs: observations,
			Payload: frontier.FindingPayload{
				Title:                 backlogTitle(plan),
				Pattern:               backlogPattern(plan),
				Significance:          backlogOutcome(plan),
				Scope:                 backlogScope(plan),
				CounterEvidenceAbsent: true,
			},
		})
		if err != nil {
			return "", fmt.Errorf("consolidate the backlog act's evidence: %w", err)
		}
		findingID = finding.ID
	}

	payload := frontier.ProposalPayload{
		Title:   backlogTitle(plan),
		Problem: backlogProblem(plan),
		Outcome: backlogOutcome(plan),
		Targets: []frontier.Target{{
			System:     backlogSystem(plan),
			Confidence: frontier.ConfidenceHigh,
			Rationale:  backlogRationale(plan),
		}},
		// The middle value because the run graded nothing: the result
		// schema asks a backlog pass what should become of a candidate and
		// never how much it matters, and a proposal has to carry a grading
		// the store admits.
		Impact:         frontier.ImpactModerate,
		Classification: frontier.ClassificationPrivate,
		Supporting:     plan.Evidence,
	}
	var proposal frontier.Proposal
	if findingID != "" {
		proposal, err = b.front.CreateProposal(ctx, frontier.ProposalInput{
			RunID: plan.RunID, FindingIDs: []string{findingID}, Payload: payload,
		})
	} else {
		proposal, err = b.front.CreateCandidateProposal(ctx, frontier.CandidateProposalInput{
			RunID: plan.RunID, HypothesisIDs: backlogIDs(plan), Payload: payload,
		})
	}
	if err != nil {
		return "", fmt.Errorf("publish the backlog proposal: %w", err)
	}

	if ledgerPlan, err = b.backlogPlan(plan, proposal.ID, findingID); err != nil {
		return "", err
	}
	if err := b.ledger.ProposeBacklog(ctx, ledgerPlan); err != nil {
		return "", backlogPlanError(err)
	}
	return proposal.ID, nil
}

// actEvidence is the observations the act's finding would consolidate: those
// of every candidate it settles, in the frontier's own order.
//
// A promotion consolidates exactly the observation it promotes, because that
// claim is what the fact rests on and the candidate's other observations are
// about something else.
func (b *backlogService) actEvidence(ctx context.Context, plan explore.BacklogPlan) ([]string, error) {
	if plan.Operation == explore.BacklogPromote {
		if plan.Observation == nil {
			return nil, fmt.Errorf("babel: a promotion carries no observation")
		}
		return []string{plan.Observation.ID}, nil
	}
	var out []string
	for _, target := range plan.Hypotheses {
		observations, err := b.front.ObservationsFor(ctx, target.ID)
		if err != nil {
			return nil, fmt.Errorf("read the observations of %s: %w", target.ID, err)
		}
		for _, observation := range observations {
			out = append(out, observation.ID)
		}
	}
	return out, nil
}

// backlogPlanShape refuses a plan whose arithmetic the wording below would
// index past.
//
// internal/explore already checks this against the recipe's contract; the
// check is repeated here because this file composes prose out of the targets,
// and a supersession that arrived with no successor would panic rather than be
// refused. The ledger's own validation is the authority on everything else.
func backlogPlanShape(plan explore.BacklogPlan) error {
	if !containsOperation(plan.Operation) {
		return fmt.Errorf("babel: %q is not a backlog act", plan.Operation)
	}
	if len(plan.Hypotheses) == 0 {
		return fmt.Errorf("babel: a %s names no candidate", plan.Operation)
	}
	switch plan.Operation {
	case explore.BacklogConsolidate:
		if len(plan.Hypotheses) < 2 || plan.Finding == nil {
			return fmt.Errorf("babel: a consolidation folds at least two candidates into a finding")
		}
	case explore.BacklogSupersede:
		if plan.By == nil {
			return fmt.Errorf("babel: a supersession names no successor")
		}
	case explore.BacklogPromote:
		if plan.Entity == nil || plan.Observation == nil {
			return fmt.Errorf("babel: a promotion names no entity or no observation")
		}
	}
	return nil
}

func containsOperation(operation explore.BacklogOperation) bool {
	for _, known := range explore.BacklogOperations() {
		if known == operation {
			return true
		}
	}
	return false
}

// backlogPlan states one pass's act in the ledger's vocabulary.
func (b *backlogService) backlogPlan(plan explore.BacklogPlan, proposalID, findingID string) (
	reality.BacklogPlan, error) {
	out := reality.BacklogPlan{
		ProposalID: proposalID,
		Operation:  reality.BacklogOperation(plan.Operation),
		Hypotheses: backlogIDs(plan),
		Reasoning:  plan.Reason,
		// How much stands behind the act: the locators the candidates'
		// observations cite. §4.13 measures a re-proposal after a decline
		// against it, so a second plan for the same act is admitted only
		// when more evidence has turned up.
		Evidence: len(plan.Evidence),
		By: reality.Provenance{
			RunID:    plan.RunID,
			RecipeID: b.recipe,
			Version:  b.version,
			Actor:    "run",
		},
	}
	switch plan.Operation {
	case explore.BacklogConsolidate:
		out.Finding = findingID
	case explore.BacklogSupersede:
		out.SupersededBy = plan.By.ID
	case explore.BacklogPromote:
		value, err := backlogFactValue(plan)
		if err != nil {
			return reality.BacklogPlan{}, err
		}
		out.Observation = plan.Observation.ID
		out.Fact = &reality.FactInput{
			SubjectID: plan.Entity.ID,
			Predicate: reality.Predicate(plan.Predicate),
			Value:     value,
			// The subject and the times are the acceptance's; the
			// authority is deliberately left zero, because §4.8 gives
			// the accepting operator the authority for every fact a
			// proposal carries.
			Note: plan.Reason,
		}
	}
	return out, nil
}

// backlogFactValue types the promoted value the way its predicate requires.
//
// The typing is the ledger's registry rather than a guess here: an enum
// predicate takes one of its declared values, a text predicate takes the text,
// and an entity-valued predicate takes an entity — which a backlog pass has no
// way to name, so it is refused rather than stored as a string that looks like
// an identifier.
func backlogFactValue(plan explore.BacklogPlan) (reality.FactValue, error) {
	predicate := reality.Predicate(plan.Predicate)
	value := strings.TrimSpace(plan.Value)
	switch predicate.ValueKind() {
	case reality.ValueEnum:
		return reality.FactValue{Kind: reality.ValueEnum, Enum: value}, nil
	case reality.ValueText:
		return reality.FactValue{Kind: reality.ValueText, Text: value}, nil
	case reality.ValueEntity:
		return reality.FactValue{}, fmt.Errorf(
			"%w: %s names another entity as its value, which a backlog act may not resolve",
			explore.ErrBacklogResult, predicate)
	}
	return reality.FactValue{}, fmt.Errorf("%w: %q is not a predicate the ledger admits",
		explore.ErrBacklogResult, plan.Predicate)
}

// backlogPlanError folds the ledger's "already holds this" refusals into the
// one sentinel internal/explore turns into a recorded skip, on
// topicPlanError's terms: an open plan already proposes the act, or the
// operator declined it and nothing new has been said since. Neither is this
// pass's failure.
func backlogPlanError(err error) error {
	switch {
	case errors.Is(err, reality.ErrConflict), errors.Is(err, reality.ErrSuppressed):
		return fmt.Errorf("%w: %s", explore.ErrBacklogKnown, err)
	case err != nil:
		return err
	}
	return nil
}

// backlogIDs are the candidates the act settles, as the stores name them.
func backlogIDs(plan explore.BacklogPlan) []string {
	out := make([]string, 0, len(plan.Hypotheses))
	for _, target := range plan.Hypotheses {
		out = append(out, target.ID)
	}
	return out
}

// backlogTitle is the one line the operator rules on, and it is the same line
// on every surface: the feed's row, the proposal's page, the receipt.
func backlogTitle(plan explore.BacklogPlan) string {
	switch plan.Operation {
	case explore.BacklogConsolidate:
		return fmt.Sprintf("Consolidate %d %s into: %s", len(plan.Hypotheses),
			plural(len(plan.Hypotheses), "hypothesis", "hypotheses"), plan.Finding.Title)
	case explore.BacklogSupersede:
		return "Supersede " + summarizeClaim(plan.Candidate.Statement) + " with " +
			summarizeClaim(plan.By.Statement)
	case explore.BacklogRetire:
		return "Retire " + summarizeClaim(plan.Candidate.Statement)
	case explore.BacklogPromote:
		return fmt.Sprintf("Record fact: %s %s %s", plan.Entity.Name, plan.Predicate, plan.Value)
	}
	return "Backlog proposal"
}

// backlogPattern is what the finding says the evidence shows.
func backlogPattern(plan explore.BacklogPlan) string {
	if plan.Finding != nil {
		return plan.Finding.Pattern
	}
	return plan.Reason
}

// backlogProblem is what the proposal says is wrong, in the operator's terms:
// what the backlog currently holds and why it should not stay that way.
func backlogProblem(plan explore.BacklogPlan) string {
	deferred := "this candidate has been deferred since " +
		plan.Candidate.DeferredAt.UTC().Format("2006-01-02")
	if plan.Candidate.DeferredAt.IsZero() {
		deferred = "this candidate is deferred"
	}
	switch plan.Operation {
	case explore.BacklogConsolidate:
		return fmt.Sprintf("%d deferred candidates say one thing and no finding says it: %s",
			len(plan.Hypotheses), plan.Reason)
	case explore.BacklogSupersede:
		return deferred + ", and a later candidate states it better: " + plan.Reason
	case explore.BacklogRetire:
		return deferred + ", and it is not worth returning to: " + plan.Reason
	case explore.BacklogPromote:
		return "an observation under this candidate is a durable fact the ledger does not hold: " +
			plan.Reason
	}
	return plan.Reason
}

// backlogOutcome is what accepting the proposal does, stated as the operator
// would read it: one act, and what is true afterwards.
func backlogOutcome(plan explore.BacklogPlan) string {
	switch plan.Operation {
	case explore.BacklogConsolidate:
		return fmt.Sprintf("the finding %q consolidates their observations, and the %d candidates "+
			"behind it are promoted; nothing is deleted and every observation stays readable "+
			"under the candidate it hangs off", plan.Finding.Title, len(plan.Hypotheses))
	case explore.BacklogSupersede:
		return plan.By.ID + " supersedes this candidate, which is marked superseded and stays " +
			"readable with its observations and its history"
	case explore.BacklogRetire:
		return "this candidate is marked retired with the reason above; nothing is deleted, and " +
			"reviving it later is one attributed act"
	case explore.BacklogPromote:
		return fmt.Sprintf("the ledger records that %s %s %s under your authority, and the candidate "+
			"behind it is promoted", plan.Entity.Name, plan.Predicate, plan.Value)
	}
	return plan.Reason
}

// backlogSystem names what the act changes, which is what a target's system
// field is for: a promotion acts on the Reality Ledger and the other three on
// the hypothesis frontier.
func backlogSystem(plan explore.BacklogPlan) string {
	if plan.Operation == explore.BacklogPromote {
		return "reality ledger"
	}
	return "hypothesis frontier"
}

// backlogRationale is why that system is the one this proposal acts on.
func backlogRationale(plan explore.BacklogPlan) string {
	return "the backlog is worked by " + string(plan.Operation) +
		", and only the operator's acceptance applies it"
}

// backlogScope names what the act touches, for the finding's own scope field.
func backlogScope(plan explore.BacklogPlan) []string {
	if plan.Finding != nil && len(plan.Finding.Scope) > 0 {
		return plan.Finding.Scope
	}
	scope := make([]string, 0, len(plan.Candidate.Topics)+1)
	scope = append(scope, plan.Candidate.Topics...)
	if plan.Entity != nil {
		scope = append(scope, plan.Entity.Name)
	}
	return scope
}

// summarizeClaim shortens a candidate's statement to the clause a title can
// carry, on a word boundary, so a proposal's one line stays one line.
func summarizeClaim(statement string) string {
	const limit = 72
	text := strings.Join(strings.Fields(statement), " ")
	if len(text) <= limit {
		return text
	}
	cut := strings.LastIndexByte(text[:limit], ' ')
	if cut <= 0 {
		cut = limit
	}
	return strings.TrimRight(text[:cut], " ,;:") + "…"
}

// keyTerms are the words a statement is matched to its neighbours by.
//
// Lowercased, four characters or more, with the corpus's own filler removed.
// It is a weak signal and it is used as one: the shared terms decide which
// candidates a pass is *shown*, and the pass decides whether they are about
// one thing.
func keyTerms(statement string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, field := range strings.FieldsFunc(strings.ToLower(statement), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(field) < 4 || backlogStopWords[field] {
			continue
		}
		out[field] = struct{}{}
	}
	return out
}

// sharedTerms counts the key terms two statements have in common.
func sharedTerms(a, b map[string]struct{}) int {
	shared := 0
	for term := range a {
		if _, ok := b[term]; ok {
			shared++
		}
	}
	return shared
}

// backlogStopWords are the words that say nothing about what a candidate is
// about in this corpus.
//
// It is short and deliberately not a linguistic stop list: the only words
// worth removing are the ones that appear in most of Babel's own candidates,
// because a term every statement shares would make every candidate every other
// candidate's neighbour.
var backlogStopWords = map[string]bool{
	"babel": true, "that": true, "this": true, "with": true, "from": true,
	"have": true, "been": true, "when": true, "what": true, "which": true,
	"would": true, "could": true, "should": true, "their": true, "there": true,
	"about": true, "because": true, "where": true, "while": true, "these": true,
	"those": true, "than": true, "then": true, "into": true, "over": true,
	"under": true, "after": true, "before": true, "being": true, "does": true,
	"more": true, "most": true, "some": true, "such": true, "only": true,
	"also": true, "each": true, "other": true, "same": true, "session": true,
	"sessions": true, "record": true, "records": true, "run": true, "runs": true,
}
