package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// This file wires SPEC.md §4.13's filing pass to the two stores that hold what
// it produces: internal/frontier's `about` edge, and the Reality Ledger's topic
// question.
//
// It is a translation layer and nothing else. internal/explore states what a
// filing pass needs in its own vocabulary — resolve a name, file a record,
// propose a topic, record that there is no topic — and this turns each of those
// into the call the owning store already validates. No rule lives here: the
// frontier decides what supersedes what, and the ledger decides whether a
// proposal duplicates one it already holds.

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

// topicFiler is internal/explore's TopicService over this machine's stores.
type topicFiler struct {
	front  *frontier.Store
	ledger *reality.Store
	// sessions is the session cache the cited repositories are read from.
	// Nil on a machine whose catalog did not open, which costs the pass one
	// piece of evidence rather than the ability to file.
	sessions *catalog.Cache
	// recipe and version attribute the topic question to the pass that
	// raised it, which is §4.8's requirement that a proposal name its
	// author.
	recipe  string
	version int
}

// topicService is the filing surface for one machine, or nothing at all.
//
// The nil checks are questionLedger's and for the same reason: a typed nil in
// an interface field is not a nil interface, and internal/explore reads a
// non-nil field as a facility that is present. A machine whose ledger did not
// open has nowhere to put a filing, and the honest state is the absent one —
// a filing assignment is then refused before a worker starts.
func topicService(front *frontier.Store, ledger *reality.Store, sessions *catalog.Cache) explore.TopicService {
	if front == nil || ledger == nil {
		return nil
	}
	version, _ := recipeVersion(FilingRecipe)
	return &topicFiler{front: front, ledger: ledger, sessions: sessions,
		recipe: FilingRecipe, version: version}
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
	if out.Declined, err = t.declined(ctx); err != nil {
		return explore.TopicLedger{}, err
	}
	if out.Repositories, err = t.repositories(ctx, sessions); err != nil {
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
// The reason is the decline event's own note. §4.8 keeps the operator's words
// verbatim and §4.13 requires this pass to read them: a proposal that repeats a
// declined one is the failure this material exists to prevent, and a list of
// declined names with no reasons would tell a pass what was refused without
// telling it why.
func (t *topicFiler) declined(ctx context.Context) ([]explore.TopicReason, error) {
	listings, err := t.ledger.Questions(ctx, reality.QuestionQuery{
		States: []reality.QuestionState{reality.QuestionDeclined},
		Limit:  topicLedgerReasons * 4,
	})
	if err != nil {
		return nil, fmt.Errorf("read the ledger's declined questions: %w", err)
	}
	var out []explore.TopicReason
	for _, listing := range listings {
		if listing.Question.Kind != reality.QuestionTopic {
			continue
		}
		history, err := t.ledger.QuestionHistory(ctx, listing.Question.ID)
		if err != nil {
			return nil, fmt.Errorf("read the history of question %s: %w", listing.Question.ID, err)
		}
		reason := ""
		for _, event := range history {
			if event.State == reality.QuestionDeclined && event.Payload.Note != "" {
				reason = event.Payload.Note
			}
		}
		out = append(out, explore.TopicReason{
			Name:   listing.Question.Payload.Prompt,
			Reason: reason,
		})
		if len(out) == topicLedgerReasons {
			break
		}
	}
	return out, nil
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

// Propose raises the topic question this pass wants answered.
//
// The proposal names the record it would file, which is what makes accepting it
// one operator act rather than two: the ledger creates the entity and the
// filing lands with it. A question the ledger already holds for this identity
// is not an error — the same topic proposed from a second record is the same
// question with more evidence behind it — so the duplicate is reported as the
// question that already carries it.
func (t *topicFiler) Propose(ctx context.Context, record evaluation.Subject, runID string,
	proposal explore.TopicProposal) (string, error) {
	ref, err := filingRef(record)
	if err != nil {
		return "", err
	}
	question, err := t.ledger.AskTopic(ctx, reality.TopicProposal{
		Name:       proposal.Name,
		Kind:       reality.EntityKind(proposal.Kind),
		Aliases:    proposalAliases(proposal),
		Binding:    proposalBinding(proposal),
		Reasoning:  proposalReasoning(proposal),
		Records:    []frontier.Ref{ref},
		Considered: proposal.Considered,
		Identity:   proposal.Identity,
		Sessions:   1,
	}, reality.Provenance{
		RunID:    runID,
		RecipeID: t.recipe,
		Version:  t.version,
		Actor:    "run",
	})
	switch {
	case errors.Is(err, reality.ErrTopicBound), errors.Is(err, reality.ErrDuplicateQuestion),
		errors.Is(err, reality.ErrSuppressed):
		// The ledger already holds this topic: the identity binds an
		// entity, an open question already carries it, or the operator
		// declined it and nothing new has been said since. None of the
		// three is this pass's failure, and the last one is the
		// operator's answer — which §4.13 suppresses precisely so a
		// recipe cannot keep asking.
		return "", fmt.Errorf("%w: %s", explore.ErrTopicKnown, err)
	case err != nil:
		return "", err
	}
	return question.ID, nil
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
