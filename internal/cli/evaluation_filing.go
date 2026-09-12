package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/web"
)

// This file wires SPEC.md §4.13's filing pass to the stores that hold what it
// produces: internal/frontier's `about` edge, the chain a topic proposal is
// published as, the Reality Ledger's plan behind it, and the operator's own
// steering entries in internal/complaint.
//
// It is a translation layer and nothing else. internal/explore states what a
// filing pass needs in its own vocabulary — resolve a name, file a record,
// propose a change to the ledger, answer an ask, record that there is no topic
// — and this turns each of those into the calls the owning stores already
// validate. No rule lives here: the frontier decides what supersedes what, and
// the ledger decides whether a plan duplicates one it already holds.
//
// The one thing this file does compose is wording. §4.13's second reading
// makes a topic change an ordinary proposal, produced through Babel's normal
// chain, so somebody has to turn "split t/manifold, keeping the service" into
// a hypothesis, an observation, a finding and a proposal an operator can read.
// That composition is mechanical — the judgement is upstream, in the pass that
// decided the operation and the targets — and it lives here because this is
// where the frontier is.

// FilingRecipe is the cookbook asset a filing pass runs under.
//
// It is a second recipe beside EvaluationRecipe rather than another role inside
// it, because the two answer different questions and §5.1 makes a recipe's body
// the statement of one method. A review judges a record; a filing decides what
// the record is about, may create nothing, and produces no vote — putting both
// methods in one body would give every reviewer instructions for an authority
// it does not have.
const FilingRecipe = "babel-files-its-output"

// topicLedgerEntities bounds how many entities one filing pass is shown.
//
// A pass has to be able to prefer an existing topic, which means the list has
// to be the ledger's, not a sample of it — but a deployment that has been
// running for years must not put an unbounded list in a prompt. Two hundred is
// far more topics than an operator has ever curated by hand and small enough to
// render, and the entities come newest first, which is the order a record
// produced today is most likely to belong in.
const topicLedgerEntities = 200

// topicLedgerReasons bounds the retired and declined material.
//
// Fifty of each: these are the mistakes Babel already made at naming topics,
// and reading the most recent ones is what stops the next proposal repeating
// them. Older ones stay in the ledger and on the topic page; they are not
// evidence a filing pass needs in front of it.
const topicLedgerReasons = 50

// topicLedgerObservations bounds the unbound identities one pass is shown.
//
// A host with a hundred scratch clones observes a hundred identities, and none
// of them is worth a prompt line beside the one the record under review is
// actually about. The bound therefore keeps the heaviest evidence — most
// sessions first — because that is the order in which an identity is likely to
// be a project rather than a checkout somebody made once.
const topicLedgerObservations = 40

// topicLedgerAsks bounds the operator's unanswered asks one pass is shown.
//
// It is small on purpose. An ask is the operator's own sentence and a pass has
// to read all of them it is given, so a list long enough to skim is a list
// long enough to ignore; twenty unanswered asks about topics is already a
// backlog the operator would rather Babel worked through than paged.
const topicLedgerAsks = 20

// askPrefix is how the operator addresses a topic when he tells Babel
// something about one: `babel tell "topic t/manifold: the cli and the service
// are two things"`.
//
// It is a prefix rather than a parser because the sentence after it is his and
// stays his. §4.13 has the operator ask Babel and Babel answer; a grammar here
// would turn the ask into a command, and the run would obey a parse instead of
// judging a request.
const askPrefix = "topic t/"

// topicFiler is internal/explore's TopicService over this machine's stores.
type topicFiler struct {
	front  *frontier.Store
	ledger *reality.Store
	// sessions is the session cache the cited repositories are read from.
	// Nil on a machine whose catalog did not open, which costs the pass one
	// piece of evidence rather than the ability to file.
	sessions *catalog.Cache
	// listing is the session listing this host can answer from its catalog
	// without blocking — the same one `babel web` serves — and it is where
	// the unbound identities come from. Nil costs the pass the scan's
	// evidence and nothing else.
	listing func(context.Context) []web.SessionRow
	// complaints holds the operator's steering, which is where an ask about
	// a topic is told and where its answer is recorded as a reply.
	complaints *complaint.Store
	// edges and backlinks are #113's graph: the reply a `no_change` answer
	// mints towards the ask, and the read that tells a pass an ask has
	// already been answered.
	edges     reference.Appender
	backlinks reference.Lister
	// recipe and version attribute the records a pass produces, which is
	// §4.8's requirement that a proposal name its author.
	recipe  string
	version int
}

// topicFilerStores is what a filing surface needs from one machine.
//
// It is a struct rather than eight arguments because every call site opens the
// same handles and a positional list of nilable stores is how one of them ends
// up in the wrong slot.
type topicFilerStores struct {
	Frontier   *frontier.Store
	Ledger     *reality.Store
	Sessions   *catalog.Cache
	Listing    func(context.Context) []web.SessionRow
	Complaints *complaint.Store
	Edges      reference.Appender
	Backlinks  reference.Lister
}

// topicService is the filing surface for one machine, or nothing at all.
//
// The nil checks are questionLedger's and for the same reason: a typed nil in
// an interface field is not a nil interface, and internal/explore reads a
// non-nil field as a facility that is present. A machine whose ledger did not
// open has nowhere to put a filing, and the honest state is the absent one —
// a filing assignment is then refused before a worker starts.
func topicService(in topicFilerStores) explore.TopicService {
	if in.Frontier == nil || in.Ledger == nil {
		return nil
	}
	version, _ := recipeVersion(FilingRecipe)
	return &topicFiler{
		front: in.Frontier, ledger: in.Ledger, sessions: in.Sessions,
		listing: in.Listing, complaints: in.Complaints,
		edges: in.Edges, backlinks: in.Backlinks,
		recipe: FilingRecipe, version: version,
	}
}

// topicFilingStores names the handles a filing pass writes through on one
// machine, with every absence stated as an absence.
//
// The nil tests reach inside the analysis state as well as at it, on
// internal/web's reasoning about the same handles: a launch whose durable file
// opened but whose edge store did not would otherwise hand this adapter an
// interface holding a nil *reference.Store, which passes every nil test and
// then answers reads from nothing.
func topicFilingStores(state *analysisState, ledger *reality.Store,
	roots []string) topicFilerStores {
	in := topicFilerStores{Ledger: ledger, Listing: observedSessions(roots)}
	if state == nil {
		return in
	}
	in.Frontier, in.Sessions, in.Complaints = state.frontier, state.sessionCatalog, state.complaints
	in.Edges = state.referenceAppender()
	if state.references != nil {
		in.Backlinks = state.references
	}
	return in
}

// observedSessions is the session listing this host can answer from its
// catalog without blocking, which is the one `babel web` serves.
//
// It is the cached listing on purpose. A filing pass reads it to learn which
// repository identities exist that nothing names, and re-describing a corpus
// to answer that would make the evidence cost more than the proposal it
// supports; the coordinator starts a background scan when its newest one has
// aged out, exactly as it does for a browser reload, and answers from what the
// catalog already holds either way.
//
// The context is deliberately unused, for the reason the server's Lister gives
// about its own: the listing is answered from the catalog immediately, and the
// scan that keeps it current belongs to the process rather than to whatever
// asked.
func observedSessions(roots []string) func(context.Context) []web.SessionRow {
	return func(context.Context) []web.SessionRow {
		dirs, err := babelDirs()
		if err != nil {
			return nil
		}
		rows, _, _ := scanner(dirs.data).Listing(adapters(), roots)
		return webSessionRows(rows)
	}
}

// Ledger assembles what a filing pass is shown about the world it is naming.
func (t *topicFiler) Ledger(ctx context.Context, sessions []string) (explore.TopicLedger, error) {
	listings, err := t.ledger.Entities(ctx, reality.EntityQuery{Limit: topicLedgerEntities})
	if err != nil {
		return explore.TopicLedger{}, fmt.Errorf("list the ledger's entities: %w", err)
	}
	out := explore.TopicLedger{Topics: make([]explore.LedgerTopic, 0, len(listings))}
	names := make(map[string]string, len(listings))
	for _, listing := range listings {
		entity := listing.Entity
		names[entity.ID] = entity.Payload.DisplayName
		// A merged identity is reachable and is not a topic: filing under
		// it would file under a name the operator already folded away, and
		// the canonical entity is the one the ledger resolves to anyway.
		if entity.Role != reality.RoleSelf {
			continue
		}
		topic := explore.LedgerTopic{
			ID:   entity.ID,
			Name: entity.Payload.DisplayName,
			Kind: string(entity.Kind),
		}
		aliases, err := t.ledger.Aliases(ctx, entity.ID)
		if err != nil {
			return explore.TopicLedger{}, fmt.Errorf("read the aliases of %s: %w", entity.ID, err)
		}
		topic.Aliases, topic.Binding = aliasView(aliases)
		out.Topics = append(out.Topics, topic)
	}
	if out.Retired, err = t.retired(ctx, names); err != nil {
		return explore.TopicLedger{}, err
	}
	if out.Declined, err = t.declined(ctx, names); err != nil {
		return explore.TopicLedger{}, err
	}
	if out.Repositories, err = t.repositories(ctx, sessions); err != nil {
		return explore.TopicLedger{}, err
	}
	if out.Unbound, err = t.unbound(ctx); err != nil {
		return explore.TopicLedger{}, err
	}
	if out.Asks, err = t.asks(ctx); err != nil {
		return explore.TopicLedger{}, err
	}
	return out, nil
}

// aliasView splits an entity's live aliases into the names it answers to and
// the one line that says what it is bound to.
//
// The binding prefers the repository alias over a path, which is §4.13's own
// preference restated where it is read: a remote is the identity that survives
// the same repository being cloned somewhere else, and a path is a locator.
func aliasView(aliases []reality.Alias) ([]string, string) {
	var (
		names   []string
		binding string
		path    string
	)
	for _, alias := range aliases {
		if alias.State != reality.StateAsserted {
			continue
		}
		value := strings.TrimSpace(alias.Payload.Value)
		if value == "" {
			continue
		}
		switch alias.Kind {
		case reality.AliasRepository:
			if binding == "" {
				binding = "repository " + value
			}
		case reality.AliasHostname:
			if binding == "" {
				binding = "host " + value
			}
		case reality.AliasPath:
			if path == "" {
				path = "path " + value
			}
		default:
			names = append(names, value)
		}
	}
	if binding == "" {
		binding = path
	}
	sort.Strings(names)
	return names, binding
}

// retired reports the topics that were retired and why.
//
// It reads the recent facts rather than one lifecycle query per entity: the
// question is "what has been retired lately", which is a slice of the ledger's
// own newest-first fact history, and asking each of two hundred entities
// whether it is retired would be two hundred queries for a list that is
// usually empty.
func (t *topicFiler) retired(ctx context.Context, names map[string]string) ([]explore.TopicReason, error) {
	facts, err := t.ledger.RecentFacts(ctx, topicLedgerReasons*4)
	if err != nil {
		return nil, fmt.Errorf("read the ledger's recent facts: %w", err)
	}
	var out []explore.TopicReason
	seen := make(map[string]struct{}, len(facts))
	for _, fact := range facts {
		if fact.Predicate != reality.PredicateLifecycle || fact.Status != reality.FactActive {
			continue
		}
		if strings.TrimSpace(fact.Value.Text) != "retired" {
			continue
		}
		if _, repeated := seen[fact.SubjectID]; repeated {
			continue
		}
		seen[fact.SubjectID] = struct{}{}
		name := names[fact.SubjectID]
		if name == "" {
			name = fact.SubjectID
		}
		out = append(out, explore.TopicReason{Name: name, Reason: fact.Payload.Note})
		if len(out) == topicLedgerReasons {
			break
		}
	}
	return out, nil
}

// declined reports the topic proposals the operator refused and why.
//
// The reason is his own, kept verbatim by the ledger's ruling. §4.8 keeps the
// operator's words and §4.13 requires this pass to read them: a proposal that
// repeats a declined one is the failure this material exists to prevent, and a
// list of declined names with no reasons would tell a pass what was refused
// without telling it why.
func (t *topicFiler) declined(ctx context.Context, names map[string]string) ([]explore.TopicReason, error) {
	plans, err := t.ledger.DeclinedTopicPlans(ctx, topicLedgerReasons)
	if err != nil {
		return nil, fmt.Errorf("read the ledger's declined topic plans: %w", err)
	}
	out := make([]explore.TopicReason, 0, len(plans))
	for _, plan := range plans {
		out = append(out, explore.TopicReason{
			Name:   declinedName(plan, names),
			Reason: plan.Reason,
		})
	}
	return out, nil
}

// declinedName says which topic a refused plan was about.
//
// A create or a split is named by what it would have brought into being, and a
// merge or a retirement by what it would have acted on — because those are the
// names a next pass would use, and a decline a pass cannot recognize is a
// decline it repeats.
func declinedName(plan reality.TopicPlan, names map[string]string) string {
	if plan.Entity != nil && strings.TrimSpace(plan.Entity.Subject.DisplayName) != "" {
		return plan.Entity.Subject.DisplayName
	}
	parts := make([]string, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		if name := names[target]; name != "" {
			parts = append(parts, name)
			continue
		}
		parts = append(parts, target)
	}
	if len(parts) == 0 {
		return string(plan.Operation)
	}
	return string(plan.Operation) + " " + strings.Join(parts, ", ")
}

// repositories reports the repositories the cited sessions' workspaces belong
// to, deduplicated.
//
// This is the one binding a deployment observes without a model (§4.13), and
// it is offered as evidence rather than as the answer: a record whose sessions
// all happened in one checkout is usually about that repository, and sometimes
// is not.
func (t *topicFiler) repositories(ctx context.Context, sessions []string) ([]string, error) {
	if t.sessions == nil || len(sessions) == 0 {
		return nil, nil
	}
	observed, err := t.sessions.Repositories(ctx, sessions)
	if err != nil {
		return nil, fmt.Errorf("read the cited sessions' repositories: %w", err)
	}
	out := make([]string, 0, len(observed))
	for _, repository := range observed {
		if !contains(out, repository) {
			out = append(out, repository)
		}
	}
	sort.Strings(out)
	return out, nil
}

// unbound reports the repository identities this host observed that no entity
// in the ledger binds.
//
// It is the evidence half of §4.13's second reading. The observations are the
// same derivation the browser surface renders — one identity per repository,
// grouped by identity rather than by name, so two checkouts called "babel" stay
// two things — and what changed is who may act on them: nothing here becomes a
// proposal until a pass reads a record and says that record is about one of
// them.
//
// The bound is applied after the ledger filter and by evidence weight, so the
// identities a pass is shown are the ones with the most behind them rather than
// the ones whose remote sorts first.
func (t *topicFiler) unbound(ctx context.Context) ([]explore.TopicObservation, error) {
	if t.listing == nil {
		return nil, nil
	}
	observed := web.TopicObservationsFromSessions(t.listing(ctx))
	out := make([]explore.TopicObservation, 0, len(observed))
	for _, observation := range observed {
		bound, err := t.ledger.EntityBoundTo(ctx, observation.Identity)
		if err != nil {
			return nil, fmt.Errorf("read what binds the identity %q: %w", observation.Identity, err)
		}
		if bound != "" {
			continue
		}
		out = append(out, explore.TopicObservation{
			Identity:  observation.Identity,
			Remote:    observation.Remote,
			Name:      observation.Name,
			Kind:      string(observation.Kind),
			Paths:     observation.Paths,
			Sessions:  observation.Sessions,
			Checkouts: observation.Checkouts,
			Records:   len(observation.Records),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Sessions != out[j].Sessions {
			return out[i].Sessions > out[j].Sessions
		}
		return out[i].Identity < out[j].Identity
	})
	if len(out) > topicLedgerObservations {
		out = out[:topicLedgerObservations]
	}
	return out, nil
}

// asks reports what the operator has said about topics and nobody has
// answered.
//
// The source is the complaints store, because that is where `babel tell` and
// the shell's own steering route put a sentence the operator typed: §4.13 has
// everything about a topic go through Babel, and an ask is how the operator
// enters that path without a button that edits the ledger.
//
// An ask that something already addresses is not shown again. #115 makes "was
// this answered" a backlink query rather than a column, so this asks the graph:
// an ask with an `addresses` edge pointing at it has been answered, and showing
// it to the next pass would buy the operator the same answer twice.
func (t *topicFiler) asks(ctx context.Context) ([]explore.TopicAsk, error) {
	if t.complaints == nil {
		return nil, nil
	}
	heads, err := t.complaints.Heads(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the operator's steering: %w", err)
	}
	out := make([]explore.TopicAsk, 0, len(heads))
	for _, head := range heads {
		topic, ok := askTopic(head.Text)
		if !ok {
			continue
		}
		answered, err := t.answered(ctx, head.ID)
		if err != nil {
			return nil, err
		}
		if answered {
			continue
		}
		out = append(out, explore.TopicAsk{
			ID:    head.ID,
			Topic: topic,
			Text:  strings.TrimSpace(head.Text),
			At:    head.CreatedAt,
		})
		if len(out) == topicLedgerAsks {
			break
		}
	}
	return out, nil
}

// askTopic reports the topic an ask names, and whether the sentence is an ask
// about a topic at all.
//
// The match is on the prefix and the name up to the colon, and the operator's
// sentence is passed on whole. A complaint that does not open with `topic t/`
// is steering about something else and is none of this pass's business.
func askTopic(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(strings.ToLower(trimmed), askPrefix) {
		return "", false
	}
	name := trimmed[len("topic "):]
	if cut := strings.IndexAny(name, ":\n"); cut >= 0 {
		name = name[:cut]
	}
	name = strings.TrimSpace(name)
	if name == "t/" {
		return "", false
	}
	return name, true
}

// answered reports whether anything already addresses this ask.
//
// A machine with no edge graph answers "no", which is the honest reading of
// what it can see: the alternative is withholding every ask because the store
// that records answers is absent, and an ask nobody can be told about is the
// operator talking to himself.
func (t *topicFiler) answered(ctx context.Context, ask string) (bool, error) {
	if t.backlinks == nil {
		return false, nil
	}
	edges, err := t.backlinks.To(ctx, reference.RecordRef{Kind: complaint.Namespace, ID: ask})
	if err != nil {
		return false, fmt.Errorf("read what addresses the ask %s: %w", ask, err)
	}
	for _, edge := range edges {
		if edge.Kind == reference.KindAddresses {
			return true, nil
		}
	}
	return false, nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// Resolve maps one name or alias to the canonical entity id.
//
// Unknown and ambiguous are the same refusal on the way out, because they call
// for the same answer: §4.8 leaves both to the operator, and a run that picked
// between two entities would be doing the resolution the ledger deliberately
// did not.
func (t *topicFiler) Resolve(ctx context.Context, name string) (string, error) {
	entity, err := t.ledger.ResolveSubject(ctx, name)
	switch {
	case errors.Is(err, reality.ErrUnknownRecord), errors.Is(err, reality.ErrAmbiguousAlias):
		return "", fmt.Errorf("%w: %s", explore.ErrUnknownTopic, err)
	case err != nil:
		return "", err
	}
	return entity, nil
}

// File writes the `about` edge, authored by the run that decided it.
func (t *topicFiler) File(ctx context.Context, record evaluation.Subject, runID, entityID,
	rationale string) error {
	ref, err := filingRef(record)
	if err != nil {
		return err
	}
	_, err = t.front.File(ctx, frontier.FilingInput{
		Record:    ref,
		EntityID:  entityID,
		Rationale: rationale,
		Author:    frontier.FilingRun,
		AuthorID:  runID,
	})
	return err
}

// NoTopic records that this record is about nothing in particular.
func (t *topicFiler) NoTopic(ctx context.Context, record evaluation.Subject, runID, reason string) error {
	ref, err := filingRef(record)
	if err != nil {
		return err
	}
	_, err = t.front.NoTopic(ctx, ref, frontier.FilingRun, runID, reason)
	return err
}

// Propose publishes one topic change the way §4.13's second reading requires:
// as an ordinary proposal, produced through the chain every other proposal
// travels, with the ledger plan that would be applied hanging off it.
//
// The order is the contract and it is not rearrangeable. The ledger is asked
// first whether it would take the plan at all, because the plan needs the
// proposal's id and a refusal after the chain was written would leave four
// records behind a proposal that never existed. Then the chain is written from
// the claim outwards, each record naming the one before it, because that is
// §4.8's development path and the store enforces it. The plan is committed
// last, against the proposal the operator will rule on.
//
// The chain has two shapes and the store picks between them. A record that
// cites locators carries the pass's observation, the finding that consolidates
// it, and a consolidated proposal; a record that cites none — a bare
// hypothesis, most often — cannot carry an observation at all, because §4.3
// forbids an evidence-free claim, so the proposal is #114's candidate form
// addressing the claim directly. Neither shape is a degraded version of the
// other: both say exactly what they rest on.
func (t *topicFiler) Propose(ctx context.Context, plan explore.TopicPlan) (string, error) {
	ref, err := filingRef(plan.Record)
	if err != nil {
		return "", err
	}
	if err := topicPlanShape(plan); err != nil {
		return "", err
	}
	ledgerPlan := t.topicPlan(plan, ref, "")
	if err := t.ledger.CheckTopicPlan(ctx, ledgerPlan); err != nil {
		return "", topicPlanError(err)
	}

	hypothesis, err := t.front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID: plan.RunID,
		Payload: frontier.HypothesisPayload{
			Statement:         topicStatement(plan),
			OriginCues:        topicCues(plan),
			ProvisionalLabels: []string{"topic", string(plan.Operation)},
			Notes:             plan.Reasoning,
		},
	})
	if err != nil {
		return "", fmt.Errorf("record the claim behind the topic proposal: %w", err)
	}
	var findings []string
	if len(plan.Evidence) > 0 {
		observation, err := t.front.CreateObservation(ctx, frontier.ObservationInput{
			HypothesisID:  hypothesis.ID,
			RunID:         plan.RunID,
			RecipeID:      t.recipe,
			RecipeVersion: t.version,
			Payload: frontier.ObservationPayload{
				Claim:                 topicObservation(plan),
				Category:              "topic",
				Confidence:            frontier.ConfidenceModerate,
				Impact:                frontier.ImpactModerate,
				Evidence:              plan.Evidence,
				CounterEvidenceAbsent: true,
			},
		})
		if err != nil {
			return "", fmt.Errorf("record the evidence behind the topic proposal: %w", err)
		}
		finding, err := t.front.CreateFinding(ctx, frontier.FindingInput{
			RunID:          plan.RunID,
			ObservationIDs: []string{observation.ID},
			Payload: frontier.FindingPayload{
				Title:                 topicTitle(plan),
				Pattern:               plan.Reasoning,
				Significance:          topicOutcome(plan),
				Scope:                 topicScope(plan),
				CounterEvidenceAbsent: true,
			},
		})
		if err != nil {
			return "", fmt.Errorf("consolidate the topic proposal's evidence: %w", err)
		}
		findings = append(findings, finding.ID)
	}

	payload := frontier.ProposalPayload{
		Title:   topicTitle(plan),
		Problem: plan.Reasoning,
		Outcome: topicOutcome(plan),
		Targets: []frontier.Target{{
			// The system is the ledger and there is nothing uncertain
			// about that: a topic change acts on the Reality Ledger and
			// on nothing else, which is why the confidence here is a
			// statement about the target rather than a grading of the
			// claim.
			System:     "reality ledger",
			Confidence: frontier.ConfidenceHigh,
			Rationale:  topicRationale(plan),
		}},
		// The impact is the middle value because the run graded nothing:
		// the result schema asks a filing pass what a record is about and
		// never how much a topic matters, and a proposal has to carry a
		// grading the store admits. Reading it as a model's judgement
		// would be reading a required field as an opinion.
		Impact:         frontier.ImpactModerate,
		Classification: frontier.ClassificationPrivate,
		Supporting:     plan.Evidence,
	}
	var proposal frontier.Proposal
	if len(findings) > 0 {
		proposal, err = t.front.CreateProposal(ctx, frontier.ProposalInput{
			RunID: plan.RunID, FindingIDs: findings, Payload: payload,
		})
	} else {
		proposal, err = t.front.CreateCandidateProposal(ctx, frontier.CandidateProposalInput{
			RunID: plan.RunID, HypothesisIDs: []string{hypothesis.ID}, Payload: payload,
		})
	}
	if err != nil {
		return "", fmt.Errorf("publish the topic proposal: %w", err)
	}

	ledgerPlan = t.topicPlan(plan, ref, proposal.ID)
	if err := t.ledger.ProposeTopic(ctx, ledgerPlan); err != nil {
		return "", topicPlanError(err)
	}
	if plan.Ask != nil {
		t.mintAnswer(ctx, frontier.EntityProposal, proposal.ID, plan.RunID, plan.Ask.ID,
			"the proposal this ask asked for")
	}
	return proposal.ID, nil
}

// AnswerAsk records the pass's reasoned no where the operator asked.
//
// The answer is a record citing the ask, which is #115's own design for "was
// this addressed": the complaint holds what the operator said and never
// acquires a state, and what answers it is an attributed record with an
// `addresses` edge pointing back. A hypothesis is the right record for this
// one, because a reasoned no is exactly a claim — "the cli and the service are
// one deployable" — that the operator can contest and a later pass can revisit.
//
// Nothing amends the complaint. The operator is the only author of what he
// asked, and a run rewording his sentence to record its own answer would be
// the one edit §4.13 forbids.
func (t *topicFiler) AnswerAsk(ctx context.Context, askID, runID, reason string) error {
	if t.complaints == nil {
		return fmt.Errorf("babel: this machine opened no steering store, so there is no ask to answer")
	}
	ask, err := t.complaints.Complaint(ctx, askID)
	if err != nil {
		return fmt.Errorf("read the ask %s: %w", askID, err)
	}
	topic, _ := askTopic(ask.Text)
	answer, err := t.front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID: runID,
		Payload: frontier.HypothesisPayload{
			Statement:         reason,
			OriginCues:        []string{"the operator asked about " + topic + ": " + strings.TrimSpace(ask.Text)},
			ProvisionalLabels: []string{"topic", "no-change"},
		},
	})
	if err != nil {
		return fmt.Errorf("record the answer to the ask %s: %w", askID, err)
	}
	t.mintAnswer(ctx, frontier.EntityHypothesis, answer.ID, runID, askID, reason)
	return nil
}

// mintAnswer points one record at the ask it answers.
//
// A refusal is a warning and never an error, on internal/complaint's own
// terms: the answer exists, it is attributed and readable, and the graph is
// missing one edge until something re-emits it — while an answer refused
// because its shadow could not be written would be the pass telling the
// operator nothing at all.
func (t *topicFiler) mintAnswer(ctx context.Context, kind frontier.EntityType, record, runID,
	ask, note string) {
	if t.edges == nil {
		return
	}
	_, _ = t.edges.Append(ctx, reference.Edge{
		Kind:      reference.KindAddresses,
		From:      reference.RecordRef{Kind: string(kind), ID: record},
		To:        reference.RecordRef{Kind: complaint.Namespace, ID: ask},
		ActorKind: reference.ActorRun,
		ActorRef:  runID,
		Note:      note,
	})
}

// topicPlanShape refuses a plan whose arithmetic the wording below would index
// past.
//
// internal/explore already checks this against the recipe's contract; the check
// is repeated here because this file composes prose out of the targets, and a
// merge that arrived with one target would panic rather than be refused. The
// ledger's own validation is the authority on everything else.
func topicPlanShape(plan explore.TopicPlan) error {
	want := map[explore.TopicOperation]int{
		explore.TopicCreate: 0, explore.TopicSplit: 1,
		explore.TopicMerge: 2, explore.TopicRetire: 1,
	}
	arity, known := want[plan.Operation]
	if !known {
		return fmt.Errorf("babel: %q is not a topic operation", plan.Operation)
	}
	if len(plan.Targets) != arity {
		return fmt.Errorf("babel: a %s names %d existing topics, not %d",
			plan.Operation, arity, len(plan.Targets))
	}
	if creatingTopic(plan.Operation) != (plan.Entity != nil) {
		return fmt.Errorf("babel: a %s carries %s entity draft",
			plan.Operation, map[bool]string{true: "no", false: "an"}[plan.Entity == nil])
	}
	return nil
}

// creatingTopic reports whether an operation brings an entity into being.
func creatingTopic(operation explore.TopicOperation) bool {
	return operation == explore.TopicCreate || operation == explore.TopicSplit
}

// topicPlan states one pass's plan in the ledger's vocabulary.
//
// The filings are the records the acceptance would file: the record under
// review, filed under the entity the same acceptance creates, which is why
// EntityID is left empty. A merge and a retirement file nothing — the filings
// they affect already exist and resolve through the ledger's own merge history
// — so they carry none.
func (t *topicFiler) topicPlan(plan explore.TopicPlan, record frontier.Ref,
	proposalID string) reality.TopicPlan {
	out := reality.TopicPlan{
		ProposalID: proposalID,
		Operation:  reality.TopicOperation(plan.Operation),
		Reasoning:  plan.Reasoning,
		By: reality.Provenance{
			RunID:    plan.RunID,
			RecipeID: t.recipe,
			Version:  t.version,
			Actor:    "run",
		},
	}
	for _, target := range plan.Targets {
		out.Targets = append(out.Targets, target.ID)
	}
	for _, considered := range plan.Considered {
		out.Considered = append(out.Considered, considered.ID)
	}
	if plan.Entity == nil {
		return out
	}
	out.Identity = plan.Entity.Identity
	out.Entity = &reality.EntityDraft{
		Subject: reality.NewSubject{
			Kind:        reality.EntityKind(plan.Entity.Kind),
			DisplayName: plan.Entity.Name,
			Notes:       proposalReasoning(*plan.Entity),
			Aliases:     proposalAliases(*plan.Entity),
		},
		Binding: proposalBinding(*plan.Entity),
	}
	// The filing's author is not set here: the ledger takes it from the
	// plan's provenance when the acceptance applies it, so a run's plan
	// produces a run-authored filing and a plan with no run behind it
	// produces the heuristic one §4.13 requires to be labelled as such.
	out.Filings = []reality.FilingDraft{{Record: record, Rationale: plan.Reasoning}}
	if plan.Observed != nil {
		// The evidence weight is the scan's count rather than this pass's
		// opinion, because §4.13 measures "materially new evidence"
		// against it: a second plan for an identity the operator declined
		// is admitted only when more sessions stand behind it than did
		// when he refused.
		out.Sessions = plan.Observed.Sessions
	}
	return out
}

// topicPlanError folds the ledger's "already holds this" refusals into the one
// sentinel internal/explore turns into a recorded skip.
//
// The three are one answer to the pass: the identity binds an entity, an open
// plan already proposes it, or the operator declined it and nothing new has
// been said since. None of the three is this pass's failure, and the last one
// is the operator's answer — which §4.13 suppresses precisely so a recipe
// cannot keep asking.
func topicPlanError(err error) error {
	switch {
	case errors.Is(err, reality.ErrTopicBound), errors.Is(err, reality.ErrConflict),
		errors.Is(err, reality.ErrSuppressed):
		return fmt.Errorf("%w: %s", explore.ErrTopicKnown, err)
	case err != nil:
		return err
	}
	return nil
}

// topicTitle is the one line the operator rules on, and it is the same line on
// every surface: the feed's row, the proposal's page, the receipt.
func topicTitle(plan explore.TopicPlan) string {
	switch plan.Operation {
	case explore.TopicCreate:
		return "New topic: " + plan.Entity.Name
	case explore.TopicSplit:
		return "Split t/" + plan.Targets[0].Name + ": " + plan.Entity.Name
	case explore.TopicMerge:
		return "Merge t/" + plan.Targets[0].Name + " into t/" + plan.Targets[1].Name
	case explore.TopicRetire:
		return "Retire t/" + plan.Targets[0].Name
	}
	return "Topic proposal"
}

// topicStatement is the claim the chain hangs off: what would have to be true
// for the change to be right.
func topicStatement(plan explore.TopicPlan) string {
	switch plan.Operation {
	case explore.TopicCreate:
		return "these records are about one thing: " + plan.Entity.Name
	case explore.TopicSplit:
		return "t/" + plan.Targets[0].Name + " names two things, and one of them is " + plan.Entity.Name
	case explore.TopicMerge:
		return "t/" + plan.Targets[0].Name + " and t/" + plan.Targets[1].Name + " name one thing"
	case explore.TopicRetire:
		return "t/" + plan.Targets[0].Name + " names nothing a reader would look a record up under"
	}
	return plan.Reasoning
}

// topicObservation is what the claim rests on, in the material's own terms: the
// scan's counts, the operator's sentence, or — when the pass read neither — the
// reasoning it gave.
func topicObservation(plan explore.TopicPlan) string {
	if observed := plan.Observed; observed != nil {
		claim := fmt.Sprintf("%d %s in %d %s resolve to %s, and no entity in the ledger names it",
			observed.Sessions, plural(observed.Sessions, "session", "sessions"),
			observed.Checkouts, plural(observed.Checkouts, "checkout", "checkouts"),
			observed.Identity)
		if observed.Records > 0 {
			claim += fmt.Sprintf("; %d %s cite it",
				observed.Records, plural(observed.Records, "record", "records"))
		}
		return claim
	}
	if ask := plan.Ask; ask != nil {
		return "the operator asked about " + ask.Topic + ": " + strings.TrimSpace(ask.Text)
	}
	return plan.Reasoning
}

// topicOutcome is what accepting the proposal does, stated as the operator
// would read it: one act, and what is true afterwards.
func topicOutcome(plan explore.TopicPlan) string {
	switch plan.Operation {
	case explore.TopicCreate:
		return "the topic " + plan.Entity.Name + " exists, bound to " + topicBindingLine(*plan.Entity) +
			", and the record under review is filed under it"
	case explore.TopicSplit:
		return plan.Entity.Name + " exists as its own topic, bound to " + topicBindingLine(*plan.Entity) +
			", and the record under review moves there from t/" + plan.Targets[0].Name
	case explore.TopicMerge:
		return "t/" + plan.Targets[0].Name + " is folded into t/" + plan.Targets[1].Name +
			"; nothing is rewritten, because a filing names an entity id and every reader " +
			"resolves it through the merge history"
	case explore.TopicRetire:
		return "t/" + plan.Targets[0].Name + " stops being a topic, and everything filed under it " +
			"returns to the triage backlog"
	}
	return plan.Reasoning
}

// topicRationale is why the ledger is the system this proposal acts on.
func topicRationale(plan explore.TopicPlan) string {
	return "the ledger's topics change by " + string(plan.Operation) +
		", and only the operator's acceptance applies it"
}

// topicScope names what the change touches, for the finding's own scope field.
func topicScope(plan explore.TopicPlan) []string {
	scope := make([]string, 0, len(plan.Targets)+1)
	for _, target := range plan.Targets {
		scope = append(scope, "t/"+target.Name)
	}
	if plan.Entity != nil && plan.Entity.Identity != "" {
		scope = append(scope, plan.Entity.Identity)
	}
	return scope
}

// topicCues records what provoked the claim, which is what makes a topic
// proposal readable as an answer to something rather than as a run's idea.
func topicCues(plan explore.TopicPlan) []string {
	cues := []string{"filing pass on " + plan.Record.Kind + " " + plan.Record.ID}
	if plan.Observed != nil {
		cues = append(cues, "the scan observed the identity "+plan.Observed.Identity+
			" and no entity names it")
	}
	if plan.Ask != nil {
		cues = append(cues, "the operator asked about "+plan.Ask.Topic)
	}
	return cues
}

// topicBindingLine states a draft's binding in one phrase, so the outcome says
// what the topic would be bound to rather than that it would be bound.
func topicBindingLine(entity explore.TopicProposal) string {
	switch {
	case strings.TrimSpace(entity.Remote) != "":
		return "the repository " + entity.Remote
	case len(entity.Paths) > 0:
		return "the checkout " + entity.Paths[0]
	case strings.TrimSpace(entity.Definition) != "":
		return entity.Definition
	}
	return entity.Identity
}

// proposalAliases turns the pass's binding into the typed aliases §4.8 stores.
//
// Typed, because §4.8 requires it: a remote, a path and a spoken name are
// different kinds of evidence that two names mean one thing, and an untyped
// list would compare a checkout path against a nickname.
func proposalAliases(p explore.TopicProposal) []reality.AliasInput {
	out := make([]reality.AliasInput, 0, len(p.Aliases)+len(p.Paths)+2)
	if name := strings.TrimSpace(p.Name); name != "" {
		out = append(out, reality.AliasInput{Kind: reality.AliasName,
			Payload: reality.AliasPayload{Value: name}})
	}
	for _, alias := range p.Aliases {
		if value := strings.TrimSpace(alias); value != "" {
			out = append(out, reality.AliasInput{Kind: reality.AliasChatTerm,
				Payload: reality.AliasPayload{Value: value}})
		}
	}
	if remote := strings.TrimSpace(p.Remote); remote != "" {
		out = append(out, reality.AliasInput{Kind: reality.AliasRepository,
			Payload: reality.AliasPayload{Value: remote}})
	}
	for _, path := range p.Paths {
		if value := strings.TrimSpace(path); value != "" {
			out = append(out, reality.AliasInput{Kind: reality.AliasPath,
				Payload: reality.AliasPayload{Value: value}})
		}
	}
	return out
}

// proposalBinding turns the binding into the facts the operator's acceptance
// asserts.
//
// The subject and the authority are deliberately left zero: the entity does not
// exist yet and the operator accepting the proposal is the authority for every
// fact it carries. A run filling either would be asserting facts under an
// authority it does not have (§4.8).
func proposalBinding(p explore.TopicProposal) []reality.FactInput {
	var out []reality.FactInput
	if remote := strings.TrimSpace(p.Remote); remote != "" {
		out = append(out, reality.FactInput{
			Predicate: reality.PredicateRepositoryRemote,
			Value:     reality.FactValue{Kind: reality.ValueText, Text: remote},
		})
	}
	for _, path := range p.Paths {
		value := strings.TrimSpace(path)
		if value == "" {
			continue
		}
		out = append(out, reality.FactInput{
			Predicate: reality.PredicateLocalPath,
			Value:     reality.FactValue{Kind: reality.ValueText, Text: value},
		})
	}
	return out
}

// proposalReasoning keeps the definition beside the reasoning, because for a
// concept the definition is the binding and the operator deciding whether the
// topic should exist is deciding about that sentence.
func proposalReasoning(p explore.TopicProposal) string {
	reasoning := strings.TrimSpace(p.Reasoning)
	definition := strings.TrimSpace(p.Definition)
	switch {
	case definition == "":
		return reasoning
	case reasoning == "":
		return definition
	}
	return definition + "\n\n" + reasoning
}

// filingRef turns an evaluation subject into the frontier record it names.
//
// An evaluation record is refused rather than translated: this package's own
// assessments are not frontier records, there is no `about` edge that could
// point at one, and the draw does not produce a filing assignment for one.
func filingRef(record evaluation.Subject) (frontier.Ref, error) {
	entity, ok := filingEntityType(record.Kind)
	if !ok {
		return frontier.Ref{}, fmt.Errorf("babel: a %s carries no topic", record.Kind)
	}
	return frontier.Ref{Type: entity, ID: record.ID}, nil
}

func filingEntityType(kind string) (frontier.EntityType, bool) {
	switch kind {
	case evaluation.SubjectKindHypothesis:
		return frontier.EntityHypothesis, true
	case evaluation.SubjectKindObservation:
		return frontier.EntityObservation, true
	case evaluation.SubjectKindFinding:
		return frontier.EntityFinding, true
	case evaluation.SubjectKindProposal:
		return frontier.EntityProposal, true
	}
	return "", false
}
