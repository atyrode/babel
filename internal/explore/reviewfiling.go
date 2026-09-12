package explore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// This file is SPEC.md §4.13's filing pass as the review runner carries it
// out: the role that decides what a record is *about*, the answers it may
// give, and what each of them becomes durable as.
//
// It is a review job in every mechanical respect — one drawn assignment, one
// claimed reservation, one supervised worker, one attributed record, one
// receipt — and a different question in the only respect that matters. A
// review says whether a record is any good; this says where a reader would
// look for it. That is why filing is not one of evaluation.Roles(): a record
// nobody has filed is a backlog entry, not an unmet review obligation.
//
// The authority boundary is §4.8's and this file does not widen it. A run may
// name a subject through an alias the ledger already holds; a name it cannot
// resolve is a proposal rather than a new thing. §4.13's second reading makes
// that concrete for every change to a topic: a new topic, a split, a merge and
// a retirement are one output kind — a topic proposal, published as an
// ordinary proposal record and ruled on like any other — so the only thing
// this package writes against the ledger is a *plan* the operator's acceptance
// applies.
//
// The operator's asks are answered here too, and answered is the word. §4.13
// routes everything about a topic through Babel, which means a sentence the
// operator told Babel about a topic is material this pass reads and judges: it
// answers one with a proposal when it agrees, and with a reasoned no when it
// does not. An ask obeyed without judgement would be the operator editing the
// ledger through a run, which is the one thing the second reading forbids.

// ErrUnknownTopic reports a name no live entity answers to, or one that two
// answer to.
//
// Both are the same refusal on purpose. §4.8 puts the operator in charge of
// which spellings mean which entity, so a run that picked between two
// candidates would be resolving an alias the ledger deliberately left
// ambiguous — and the honest response to either is the same: raise the
// proposal and let the operator answer it.
var ErrUnknownTopic = errors.New("explore: no entity in the ledger answers to that name")

// ErrTopicKnown reports a proposal the ledger already holds: the identity
// already binds an entity, an open plan already carries it, or the operator
// declined it and nothing new has been said.
//
// It is not a failure and it is not a filing. The pass did its work and the
// answer was already in the ledger, so the assignment ends as a recorded skip
// with that reason: recording it as a failure would make the recipe fight the
// operator's refusal, and recording it as a proposal would name a change this
// pass did not propose.
var ErrTopicKnown = errors.New("explore: the ledger already holds this topic")

// ErrTopicResult reports a result the ledger it was shown contradicts: a
// target, a considered entity or an ask that was not in the material this pass
// was served.
//
// It is a malformed result and not a filing failure, and the distinction is
// the point. A split of a topic nobody holds, or an answer to an ask nobody
// made, is a name the pass invented; turning it into a new topic — the way an
// unresolvable `filing` entity honestly becomes a create proposal — would let
// an invented target mint the very thing §4.8 reserves to the operator.
var ErrTopicResult = errors.New("explore: the filing named something the ledger it was shown does not hold")

// TopicOperation is which of §4.13's four changes to a topic a proposal
// carries.
//
// They are one output kind with four operations rather than four kinds,
// because the operator's act on each is identical: he reads a proposal, and
// accepting it applies it. A surface that offered a different control for a
// split than for a new topic would be offering him four decisions where
// §4.13's second reading says there is one.
type TopicOperation string

// The four changes a topic proposal may carry (§4.13).
const (
	// TopicCreate proposes a topic no entity names yet.
	TopicCreate TopicOperation = "create"
	// TopicSplit proposes that one topic names two things, and describes
	// the part that would be split out.
	TopicSplit TopicOperation = "split"
	// TopicMerge proposes that two topics name one thing, the first
	// merging into the second.
	TopicMerge TopicOperation = "merge"
	// TopicRetire proposes that a topic should never have existed, which
	// re-queues its filings for triage.
	TopicRetire TopicOperation = "retire"
)

// TopicOperations lists the operations in a stable order, which is also the
// order the schema's enum and the recipe's prose state them in.
func TopicOperations() []TopicOperation {
	return []TopicOperation{TopicCreate, TopicSplit, TopicMerge, TopicRetire}
}

// Values lists the operation vocabulary for the generated result schema, on
// the same terms ReviewVote does: one declaration serves the enum a worker is
// handed and the refusal this package makes.
func (TopicOperation) Values() []string {
	out := make([]string, 0, len(TopicOperations()))
	for _, operation := range TopicOperations() {
		out = append(out, string(operation))
	}
	return out
}

// TopicService is internal/frontier's `about` edge, the chain a topic proposal
// is published as, and the ledger plan behind it, as this runner uses them.
//
// It is declared here rather than in any of those packages for the reason
// ReviewService is: it is the consumer's view. What makes a filing safe — the
// append-only supersession, the refusal to file under an entity the ledger
// does not hold, the dedup that stops one identity being proposed twice — is
// the stores' own and is not this package's to restate.
type TopicService interface {
	// Ledger is what a filing pass is shown about the world it is naming:
	// the live entities with their aliases and bindings, the reasons topics
	// were retired and proposals declined, the repository identities of the
	// sessions this record cites, the identities the scan observed that
	// nothing names, and the operator's own asks about topics. §4.13
	// requires the recipe to read the retired and declined reasons — that
	// is how Babel gets better at naming topics structurally rather than
	// through an unparsed memory prompt.
	Ledger(ctx context.Context, sessions []string) (TopicLedger, error)
	// Resolve maps one name or alias to the canonical entity id, following
	// the ledger's merge history, and refuses with ErrUnknownTopic when
	// nothing or more than one entity answers.
	Resolve(ctx context.Context, name string) (string, error)
	// File writes the `about` edge from the record to the entity, authored
	// by the run that decided it: §4.13 requires a filing to carry its
	// author, and the run id is what makes the edge attributable to a pass
	// an operator can read the receipt of.
	File(ctx context.Context, record evaluation.Subject, runID, entityID, rationale string) error
	// Propose publishes one topic proposal as an ordinary record and
	// returns the proposal's id. The chain is the consumer's to write —
	// §4.13's second reading puts a topic change through Babel's normal
	// output path — and a plan whose identity or target pair the ledger
	// already carries is not an error: ErrTopicKnown says the answer was
	// already there.
	Propose(ctx context.Context, plan TopicPlan) (string, error)
	// AnswerAsk records this pass's reasoned no against the operator's own
	// steering entry, attributed to the run that judged it. It is a reply
	// and never an amendment: the operator is the only author of what he
	// asked.
	AnswerAsk(ctx context.Context, askID, runID, reason string) error
	// NoTopic records that this record is about nothing in particular, with
	// the reason kept verbatim.
	NoTopic(ctx context.Context, record evaluation.Subject, runID, reason string) error
}

// TopicLedger is the ledger material a filing pass reads before it answers.
//
// It is a projection rather than the ledger's own types for the reason
// reviewTarget is one: what reaches a model is a struct with no field for
// anything it must not be told, so the guarantee survives a field added
// upstream. Nothing here is a judgement about a record — these are facts about
// the world the operator recorded — so no part of it is withheld from any
// role.
type TopicLedger struct {
	// Topics are the live entities the record may be filed under.
	Topics []LedgerTopic `json:"topics"`
	// Retired and Declined are why topics stopped existing and why
	// proposals were refused, which is the evidence a next proposal is
	// judged against.
	Retired  []TopicReason `json:"retired,omitempty"`
	Declined []TopicReason `json:"declined,omitempty"`
	// Repositories are the repository identities of the sessions this
	// record cites: the one binding this deployment can observe without a
	// model, offered as evidence rather than as the answer.
	Repositories []string `json:"cited_repositories,omitempty"`
	// Unbound is what the scan observed and nothing names: repository
	// identities with what stands behind them, and no entity bound to any
	// of them.
	//
	// It is evidence handed to this pass rather than a list of proposals,
	// and that is §4.13's second reading in one field. A Go loop that
	// turned each of these into a topic question was the surface the
	// operator rejected: nothing but a run may propose a topic, so an
	// observation with no entity behind it stays an observation until a
	// pass reads the record it belongs to and says so.
	Unbound []TopicObservation `json:"unbound_identities,omitempty"`
	// Asks are what the operator has said about topics and nobody has
	// answered: the steering entries §4.13 routes through Babel rather than
	// through a button.
	Asks []TopicAsk `json:"operator_asks,omitempty"`
}

// LedgerTopic is one live entity as a filing pass sees it.
type LedgerTopic struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	Aliases []string `json:"aliases,omitempty"`
	// Binding is what the entity is bound to in one line — a remote, a
	// path, a host — so a pass can tell two similarly named topics apart by
	// the thing they name rather than by their names.
	Binding string `json:"binding,omitempty"`
}

// TopicReason is one retired topic or one declined proposal, with the reason
// kept verbatim.
type TopicReason struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// TopicObservation is one identity the scan observed that no entity names,
// with what stands behind it.
//
// The counts are the evidence weight and they are what an operator reads
// first: three sessions in one checkout is a scratch clone, and ninety
// sessions in four checkouts across two years is a project. §4.13 also makes
// the count what lifts a decline — a proposal refused once is refused until
// materially more stands behind it — so a pass that repeats an identity with
// the same numbers is repeating a question the operator already answered.
type TopicObservation struct {
	// Identity is the repository's own identity, preferring the remote: the
	// dedup key a create proposal carries.
	Identity string `json:"identity"`
	Remote   string `json:"remote,omitempty"`
	Name     string `json:"name"`
	// Kind is the entity kind the observation suggests, empty when the scan
	// observed a repository and nothing that says what kind of thing it is.
	// It is a suggestion and never the answer: §4.13 refuses the invented
	// binding, and a kind derived from a name is exactly that.
	Kind  string   `json:"kind,omitempty"`
	Paths []string `json:"paths,omitempty"`
	// Sessions and Checkouts are how many of this host's sessions carry the
	// identity and how many workspaces resolved to it.
	Sessions  int `json:"sessions"`
	Checkouts int `json:"checkouts"`
	// Records is how many of Babel's own records cite a session in it,
	// which is how much would be filed if the operator accepted a topic for
	// it. Zero means nobody counted as often as it means nothing cites it:
	// the count is a corpus-wide derivation and a caller that does not hold
	// the corpus leaves it unset, which is why the prompt renders it only
	// when it is positive.
	Records int `json:"records,omitempty"`
}

// TopicAsk is one thing the operator said about a topic and nobody has
// answered.
//
// The text is his own words and is not parsed into an instruction. A pass
// reads it, judges it, and answers it — with a proposal when it agrees, and
// with a reasoned no when it does not — which is the difference §4.13 draws
// between Babel doing what the operator asked and Babel understanding it.
type TopicAsk struct {
	ID string `json:"id"`
	// Topic is the topic the ask names, as the operator spelled it.
	Topic string    `json:"topic"`
	Text  string    `json:"text"`
	At    time.Time `json:"at,omitzero"`
}

// FiledUnder is the first answer: this record is about an entity the ledger
// already names.
type FiledUnder struct {
	// Entity is the name or alias the entity is known by, resolved through
	// the ledger. It is never an id a model invented: an unresolvable name
	// becomes a topic proposal rather than a filing.
	Entity string `json:"entity"`
	// Rationale is why this record is about that entity. It is required,
	// because a link nobody can argue with is a link nobody can correct.
	Rationale string `json:"rationale"`
}

// TopicProposal is the second answer: the ledger should change, so the run
// proposes the change and the operator decides (§4.8, §4.13).
type TopicProposal struct {
	// Operation is which change this proposes. It is required, because the
	// four are different acts on the ledger and a proposal that did not say
	// which one it meant would leave the operator ruling on a guess.
	Operation TopicOperation `json:"operation"`
	// Targets are the existing topics the operation acts on, named the way
	// the ledger listed them: the topic being split, the two being merged —
	// the first into the second — or the one being retired. A create names
	// none.
	Targets []string `json:"targets,omitempty"`
	// AskID is the operator's ask this proposal answers, when it answers
	// one. It is how a proposal is readable as the reply it is rather than
	// as a coincidence.
	AskID string `json:"ask_id,omitempty"`
	// Name and Kind describe the entity a create brings into being, or the
	// part a split separates out. A merge and a retirement create nothing
	// and carry neither.
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
	// Identity is the dedup key: the normalized remote or the common
	// directory for a repository, a hostname for a machine, a slug for a
	// concept. It is what stops the same topic being proposed once per
	// record.
	Identity string   `json:"identity,omitempty"`
	Aliases  []string `json:"aliases,omitempty"`
	// Remote, Paths and Definition are the binding: the something real a
	// created topic has to name. A repository is bound by its remote and
	// the checkouts it was seen in; a concept is bound by a one-sentence
	// definition, which is the least a reader needs to disagree with it.
	Remote     string   `json:"remote,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	Definition string   `json:"definition,omitempty"`
	// Reasoning is why the ledger should change and why the entities below
	// were rejected.
	Reasoning string `json:"reasoning"`
	// Considered are the existing topics the pass weighed and rejected,
	// named the way it was shown them.
	Considered []string `json:"considered,omitempty"`
}

// creates reports whether this operation brings an entity into being, which is
// what decides whether a name, a kind and a binding are required or refused.
func (p TopicProposal) creates() bool {
	return p.Operation == TopicCreate || p.Operation == TopicSplit
}

// NoTopic is the third answer: some outputs are about nothing in particular
// and saying so is the honest result (§4.13).
type NoTopic struct {
	Reason string `json:"reason"`
}

// NoChange is the fourth answer: the operator asked for something about a
// topic and this pass judges it wrong.
//
// It is an answer and not a refusal to work. §4.13 has the operator ask Babel
// and Babel propose, which only means anything if Babel may also say no: the
// reason lands on the ask as a reply, the operator reads it where he wrote it,
// and nothing about the ledger moved.
type NoChange struct {
	// AskID names the ask being answered. It must be one this pass was
	// shown: an answer to something nobody asked is a reply into the void.
	AskID  string `json:"ask_id"`
	Reason string `json:"reason"`
}

// TopicTarget is one existing topic a proposal acts on, resolved.
//
// Both halves travel because both are needed downstream and neither can be
// derived from the other by the consumer: the ledger takes the canonical id,
// and the proposal an operator reads is titled with the name he knows the
// topic by.
type TopicTarget struct {
	ID   string
	Name string
}

// TopicPlan is one topic change as this pass reached it, handed to the
// consumer that publishes it.
//
// It is the semantics and not the wording. The consumer writes the chain —
// hypothesis, observation, finding, proposal — because that is where the
// frontier is, and it composes the prose mechanically from these facts; what
// is a judgement rather than a translation is decided here, in the package
// that read the ledger and the record.
type TopicPlan struct {
	// Record is the record under review: what the proposal would file, and
	// what the chain is written about.
	Record evaluation.Subject
	// RunID attributes every record the plan produces, which is §4.8's
	// requirement that a proposal name its author.
	RunID string
	// Operation is the change proposed.
	Operation TopicOperation
	// Targets are the existing topics it acts on, resolved through the
	// ledger this pass was shown.
	Targets []TopicTarget
	// Entity is the topic a create or a split brings into being, nil for a
	// merge and a retirement.
	Entity *TopicProposal
	// Considered are the entities the pass weighed and rejected, resolved;
	// a name that resolved to nothing is dropped rather than refused,
	// because what it was considering is in the reasoning either way.
	Considered []TopicTarget
	// Reasoning is why the ledger should change, in the run's own words.
	Reasoning string
	// Evidence is the record's own citations: what the observation the
	// consumer writes rests on, because §4.3 forbids an evidence-free
	// observation and this pass cites what the record it read cited.
	Evidence []frontier.Evidence
	// Observed is the scan evidence behind a create: the identity nothing
	// names, with the counts that stand behind it. Nil when the pass
	// proposed a topic the scan never saw, which is the ordinary case for a
	// concept.
	Observed *TopicObservation
	// Ask is the operator's steering entry this proposal answers, nil when
	// it answers none.
	Ask *TopicAsk
}

// validateFilingResult checks one filing result against the four shapes, and
// nothing about whether the answer is right.
//
// Exactly one answer, because the four are alternatives rather than fields: a
// pass that filed a record and proposed a topic for it in the same breath has
// not decided what the record is about, and storing both would leave the
// operator to guess which the run meant.
func validateFilingResult(res *ReviewResult) error {
	answers := 0
	for _, given := range []bool{res.Filing != nil, res.Topic != nil, res.NoTopic != nil,
		res.NoChange != nil} {
		if given {
			answers++
		}
	}
	switch {
	case answers == 0:
		return fmt.Errorf("%w: a filing states one of `filing`, `topic`, `no_topic` or `no_change`",
			ErrReviewEmpty)
	case answers > 1:
		return fmt.Errorf("explore: a filing states one of `filing`, `topic`, `no_topic` or "+
			"`no_change`, not %d of them", answers)
	}
	switch {
	case res.Filing != nil:
		if strings.TrimSpace(res.Filing.Entity) == "" {
			return fmt.Errorf("explore: a filing names the entity the record is about")
		}
		if strings.TrimSpace(res.Filing.Rationale) == "" {
			return fmt.Errorf("explore: a filing says why this record is about that entity")
		}
	case res.Topic != nil:
		return validateTopicProposal(res.Topic)
	case res.NoTopic != nil:
		if strings.TrimSpace(res.NoTopic.Reason) == "" {
			return fmt.Errorf("explore: a record about nothing in particular still needs the reason " +
				"it is about nothing in particular")
		}
	case res.NoChange != nil:
		if strings.TrimSpace(res.NoChange.AskID) == "" {
			return fmt.Errorf("explore: an answer that proposes no change names the ask it answers")
		}
		if strings.TrimSpace(res.NoChange.Reason) == "" {
			return fmt.Errorf("explore: an ask is answered with the reason, not with a refusal to say")
		}
	}
	return nil
}

// validateTopicProposal refuses a proposal the operator could not act on.
//
// The binding requirement is the load-bearing one for what a proposal creates.
// §4.13 admits a repository, a machine, a service or a concept as a topic and
// requires each to be bound to something real; a proposal with a name and no
// binding is a folder, which is the one thing a topic is not. The target
// arithmetic is the same rule for the other three operations: a merge of one
// topic, or a retirement of two, is not an act the ledger can perform.
func validateTopicProposal(p *TopicProposal) error {
	if !slices.Contains(TopicOperations(), p.Operation) {
		return fmt.Errorf("explore: %q is not one of the four changes a topic proposal carries (%s)",
			p.Operation, strings.Join(TopicOperation.Values(p.Operation), ", "))
	}
	if strings.TrimSpace(p.Reasoning) == "" {
		return fmt.Errorf("explore: a topic proposal says why the ledger should change")
	}
	want := map[TopicOperation]int{TopicCreate: 0, TopicSplit: 1, TopicMerge: 2, TopicRetire: 1}
	if len(p.Targets) != want[p.Operation] {
		return fmt.Errorf("explore: a %s names %d existing topics, not %d",
			p.Operation, want[p.Operation], len(p.Targets))
	}
	if !p.creates() {
		if strings.TrimSpace(p.Name) != "" || strings.TrimSpace(p.Kind) != "" ||
			strings.TrimSpace(p.Identity) != "" {
			return fmt.Errorf("explore: a %s creates no entity, so it carries no name, kind or identity",
				p.Operation)
		}
		return nil
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("explore: a %s needs the name of the topic it would create", p.Operation)
	}
	if !slices.Contains(reality.EntityKinds(), reality.EntityKind(p.Kind)) {
		return fmt.Errorf("explore: %q is not an entity kind the ledger admits", p.Kind)
	}
	if strings.TrimSpace(p.Identity) == "" {
		return fmt.Errorf("explore: a %s needs the identity that deduplicates it", p.Operation)
	}
	if strings.TrimSpace(p.Remote) == "" && len(p.Paths) == 0 && strings.TrimSpace(p.Definition) == "" {
		return fmt.Errorf("explore: a topic proposal binds to something real — a repository remote, a " +
			"path, or a one-sentence definition — because a topic with no binding is a folder")
	}
	return nil
}

// topic resolves one name the way the pass was shown it: the entity's own
// name, any alias beside it, or the canonical id.
//
// The comparison folds case and surrounding space and nothing else. §4.8 keeps
// alias resolution the ledger's, so a fuzzier match here would be this package
// deciding that two spellings mean one entity — and the ledger's own Resolve
// is what a filing goes through for exactly that reason.
func (l *TopicLedger) topic(name string) (TopicTarget, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" || l == nil {
		return TopicTarget{}, false
	}
	for _, topic := range l.Topics {
		if strings.ToLower(topic.ID) == want || strings.ToLower(topic.Name) == want {
			return TopicTarget{ID: topic.ID, Name: topic.Name}, true
		}
		for _, alias := range topic.Aliases {
			if strings.ToLower(strings.TrimSpace(alias)) == want {
				return TopicTarget{ID: topic.ID, Name: topic.Name}, true
			}
		}
	}
	return TopicTarget{}, false
}

// ask reports one of the operator's asks by id.
func (l *TopicLedger) ask(id string) (TopicAsk, bool) {
	if l == nil {
		return TopicAsk{}, false
	}
	for _, ask := range l.Asks {
		if ask.ID == id {
			return ask, true
		}
	}
	return TopicAsk{}, false
}

// observed reports the unbound identity a create proposal is about, matched on
// the identity the proposal carries.
//
// The match is on the identity rather than on the name because that is what
// the identity is for: two checkouts called "babel" are two identities and one
// name, and the evidence belongs to the one the pass named.
func (l *TopicLedger) observed(identity string) (TopicObservation, bool) {
	want := strings.TrimSpace(identity)
	if want == "" || l == nil {
		return TopicObservation{}, false
	}
	for _, observation := range l.Unbound {
		if observation.Identity == want {
			return observation, true
		}
	}
	return TopicObservation{}, false
}

// file carries out the answer: the store write, and the record of which answer
// it was.
//
// The unresolvable name is the interesting path and it is deliberately not a
// failure. §4.13: "a run's structured result may name the entities its records
// are about the way it names question subjects, resolved through aliases and
// refused when unknown, and the refusal is what makes the run raise a topic
// question instead". So a filing under a name nobody has created becomes the
// proposal that would create it, the record stays honestly unfiled, and the
// pass completes — because it did the work it was drawn for.
func (r *Reviewer) file(st *reviewState, res *ReviewResult) (*evaluation.Filing, error) {
	if r.cfg.Topics == nil {
		return nil, fmt.Errorf("explore: a filing pass needs the topic service it files through")
	}
	record, runID := st.opt.Assignment.Subject, st.opt.Assignment.RunID
	switch {
	case res.NoTopic != nil:
		reason := strings.TrimSpace(res.NoTopic.Reason)
		if err := r.cfg.Topics.NoTopic(st.commit, record, runID, reason); err != nil {
			return nil, fmt.Errorf("explore: record that %s %s is about nothing in particular: %w",
				record.Kind, record.ID, err)
		}
		return &evaluation.Filing{Outcome: evaluation.FilingNone, Reason: reason}, nil
	case res.NoChange != nil:
		return r.answer(st, *res.NoChange)
	case res.Topic != nil:
		return r.propose(st, record, *res.Topic)
	case res.Filing != nil:
		entity, err := r.cfg.Topics.Resolve(st.commit, res.Filing.Entity)
		switch {
		case errors.Is(err, ErrUnknownTopic):
			return r.propose(st, record, namedTopic(*res.Filing))
		case err != nil:
			return nil, fmt.Errorf("explore: resolve the topic %q: %w", res.Filing.Entity, err)
		}
		rationale := strings.TrimSpace(res.Filing.Rationale)
		if err := r.cfg.Topics.File(st.commit, record, runID, entity, rationale); err != nil {
			return nil, fmt.Errorf("explore: file %s %s under %s: %w", record.Kind, record.ID, entity, err)
		}
		return &evaluation.Filing{Outcome: evaluation.FilingFiled, Entity: entity, Reason: rationale}, nil
	}
	return nil, fmt.Errorf("%w: the filing stated no answer", ErrReviewEmpty)
}

// propose publishes one topic proposal and reports it as this pass's outcome.
func (r *Reviewer) propose(st *reviewState, record evaluation.Subject,
	proposal TopicProposal) (*evaluation.Filing, error) {
	plan, err := r.plan(st, record, proposal)
	if err != nil {
		return nil, err
	}
	id, err := r.cfg.Topics.Propose(st.commit, plan)
	if err != nil {
		return nil, fmt.Errorf("explore: propose to %s a topic: %w", plan.Operation, err)
	}
	return &evaluation.Filing{
		Outcome:   evaluation.FilingProposed,
		Proposal:  id,
		Operation: string(plan.Operation),
		Reason:    plan.Reasoning,
	}, nil
}

// plan resolves one proposal against the ledger this pass was shown.
//
// A target, or an answered ask, that the served material does not hold is
// refused here rather than carried. A pass that named a topic nobody holds
// produced a malformed result; treating that the way an unresolvable `filing`
// entity is treated — as an honest create proposal — would let an invented
// split target become a new entity, which is precisely the authority §4.8
// reserves to the operator.
func (r *Reviewer) plan(st *reviewState, record evaluation.Subject,
	proposal TopicProposal) (TopicPlan, error) {
	if st.ledger == nil {
		return TopicPlan{}, fmt.Errorf("explore: a filing pass was not shown the ledger it is naming")
	}
	plan := TopicPlan{
		Record:    record,
		RunID:     st.opt.Assignment.RunID,
		Operation: proposal.Operation,
		Reasoning: strings.TrimSpace(proposal.Reasoning),
		Evidence:  st.evidence,
	}
	for _, name := range proposal.Targets {
		target, ok := st.ledger.topic(name)
		if !ok {
			return TopicPlan{}, fmt.Errorf("%w: the %s names %q, which no listed topic answers to",
				ErrTopicResult, proposal.Operation, name)
		}
		plan.Targets = append(plan.Targets, target)
	}
	for _, name := range proposal.Considered {
		// A considered entity that resolves to nothing is dropped rather
		// than refused. It is the soft half of a proposal — what the pass
		// weighed — and the reasoning says what it weighed either way, so
		// a half-remembered spelling costs the operator nothing and must
		// not cost him the proposal.
		if target, ok := st.ledger.topic(name); ok {
			plan.Considered = append(plan.Considered, target)
		}
	}
	if proposal.creates() {
		entity := proposal
		plan.Entity = &entity
		if observed, ok := st.ledger.observed(proposal.Identity); ok {
			plan.Observed = &observed
		}
	}
	if proposal.AskID != "" {
		ask, ok := st.ledger.ask(proposal.AskID)
		if !ok {
			return TopicPlan{}, fmt.Errorf("%w: no ask %q was shown to this pass",
				ErrTopicResult, proposal.AskID)
		}
		plan.Ask = &ask
	}
	return plan, nil
}

// answer records this pass's reasoned no against the operator's ask.
func (r *Reviewer) answer(st *reviewState, no NoChange) (*evaluation.Filing, error) {
	ask, ok := st.ledger.ask(no.AskID)
	if !ok {
		return nil, fmt.Errorf("%w: no ask %q was shown to this pass", ErrTopicResult, no.AskID)
	}
	reason := strings.TrimSpace(no.Reason)
	if err := r.cfg.Topics.AnswerAsk(st.commit, ask.ID, st.opt.Assignment.RunID, reason); err != nil {
		return nil, fmt.Errorf("explore: answer the operator's ask %s: %w", ask.ID, err)
	}
	return &evaluation.Filing{Outcome: evaluation.FilingAnswered, Ask: ask.ID, Reason: reason}, nil
}

// namedTopic is the proposal a filing under an unresolvable name becomes.
//
// The kind is `subject` and not a guess. §4.8's operator-defined subject is
// exactly what a run has here: a name it believes means something, with no
// observation behind it that says whether it is a repository, a machine or an
// idea. Proposing `repository` on the strength of a name that looks like one
// would be the invented binding §4.13 refuses; the operator retypes it in one
// click when accepting, and the rationale the run gave is the definition it is
// judged on.
func namedTopic(filed FiledUnder) TopicProposal {
	name := strings.TrimSpace(filed.Entity)
	return TopicProposal{
		Operation:  TopicCreate,
		Name:       name,
		Kind:       string(reality.EntitySubject),
		Identity:   topicSlug(name),
		Definition: strings.TrimSpace(filed.Rationale),
		Reasoning: fmt.Sprintf("the filing pass named %q as what this record is about, and no alias in "+
			"the ledger answers to it: %s", name, strings.TrimSpace(filed.Rationale)),
	}
}

// topicSlug is the dedup identity a name-only proposal carries.
//
// Lowercased, with every run of non-alphanumeric characters folded to a single
// hyphen, so "Manifold CLI" and "manifold-cli" are one identity and the same
// unresolvable name proposed from two records is one proposal rather than two.
func topicSlug(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	hyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if !hyphen && b.Len() > 0 {
			b.WriteByte('-')
			hyphen = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// filingOutcome is the one line a receipt records about what a filing pass
// did, so the outcome is readable without opening the evaluation store.
func filingOutcome(filing *evaluation.Filing) string {
	if filing == nil {
		return ""
	}
	switch filing.Outcome {
	case evaluation.FilingFiled:
		return "filed under " + filing.Entity
	case evaluation.FilingProposed:
		return "topic " + filing.Operation + " proposed as " + filing.Proposal
	case evaluation.FilingNone:
		return "no topic: " + filing.Reason
	case evaluation.FilingAnswered:
		return "answered the operator's ask " + filing.Ask + ": " + filing.Reason
	}
	return filing.Outcome
}
