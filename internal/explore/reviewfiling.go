package explore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/reality"
)

// This file is SPEC.md §4.13's filing pass as the review runner carries it
// out: the role that decides what a record is *about*, the three answers it
// may give, and the two stores one of those answers becomes durable in.
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
// resolve is a question rather than a new thing. So the only write this
// package makes against the ledger is a topic question, and the entity is
// created — if it ever is — by the operator accepting it.

// ErrUnknownTopic reports a name no live entity answers to, or one that two
// answer to.
//
// Both are the same refusal on purpose. §4.8 puts the operator in charge of
// which spellings mean which entity, so a run that picked between two
// candidates would be resolving an alias the ledger deliberately left
// ambiguous — and the honest response to either is the same: raise the
// question and let the operator answer it.
var ErrUnknownTopic = errors.New("explore: no entity in the ledger answers to that name")

// ErrTopicKnown reports a proposal the ledger already holds: the identity
// already binds an entity, an open question already carries it, or the
// operator declined it and nothing new has been said.
//
// It is not a failure and it is not a filing. The pass did its work and the
// answer was already in the ledger, so the assignment ends as a recorded skip
// with that reason: recording it as a failure would make the recipe fight the
// operator's refusal, and recording it as a proposal would name a question
// this pass did not raise.
var ErrTopicKnown = errors.New("explore: the ledger already holds this topic")

// TopicService is internal/frontier's `about` edge and internal/reality's
// topic question as this runner uses them.
//
// It is declared here rather than in either of those packages for the reason
// ReviewService is: it is the consumer's view. What makes a filing safe — the
// append-only supersession, the refusal to file under an entity the ledger
// does not hold, the dedup that stops one identity being proposed twice — is
// the stores' own and is not this package's to restate.
type TopicService interface {
	// Ledger is what a filing pass is shown about the world it is naming:
	// the live entities with their aliases and bindings, the reasons topics
	// were retired and declined, and the repository identities of the
	// sessions this record cites. §4.13 requires the recipe to read the
	// retired and declined reasons — that is how Babel gets better at
	// naming topics structurally rather than through an unparsed memory
	// prompt.
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
	// Propose raises the topic question that names this record and returns
	// the question's id. A proposal whose identity the ledger already
	// carries as an open question is not an error: the id returned is the
	// question that already holds it.
	Propose(ctx context.Context, record evaluation.Subject, runID string,
		proposal TopicProposal) (string, error)
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

// FiledUnder is the first answer: this record is about an entity the ledger
// already names.
type FiledUnder struct {
	// Entity is the name or alias the entity is known by, resolved through
	// the ledger. It is never an id a model invented: an unresolvable name
	// becomes a topic question rather than a filing.
	Entity string `json:"entity"`
	// Rationale is why this record is about that entity. It is required,
	// because a link nobody can argue with is a link nobody can correct.
	Rationale string `json:"rationale"`
}

// TopicProposal is the second answer: the record is about something no entity
// names, so the run proposes one and the operator decides (§4.8).
type TopicProposal struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Identity is the dedup key: the normalized remote or the common
	// directory for a repository, a hostname for a machine, a slug for a
	// concept. It is what stops the same topic being proposed once per
	// record.
	Identity string   `json:"identity"`
	Aliases  []string `json:"aliases,omitempty"`
	// Remote, Paths and Definition are the binding: the something real a
	// proposal has to name. A repository is bound by its remote and the
	// checkouts it was seen in; a concept is bound by a one-sentence
	// definition, which is the least a reader needs to disagree with it.
	Remote     string   `json:"remote,omitempty"`
	Paths      []string `json:"paths,omitempty"`
	Definition string   `json:"definition,omitempty"`
	// Reasoning is why this topic should exist and why the entities below
	// were rejected.
	Reasoning string `json:"reasoning"`
	// Considered are the existing topics the pass weighed and rejected,
	// named the way it was shown them.
	Considered []string `json:"considered,omitempty"`
}

// NoTopic is the third answer: some outputs are about nothing in particular
// and saying so is the honest result (§4.13).
type NoTopic struct {
	Reason string `json:"reason"`
}

// validate checks one filing result against the three shapes, and nothing
// about whether the answer is right.
//
// Exactly one answer, because the three are alternatives rather than fields: a
// pass that filed a record and proposed a topic for it in the same breath has
// not decided what the record is about, and storing both would leave the
// operator to guess which the run meant.
func validateFilingResult(res *ReviewResult) error {
	answers := 0
	for _, given := range []bool{res.Filing != nil, res.Topic != nil, res.NoTopic != nil} {
		if given {
			answers++
		}
	}
	switch {
	case answers == 0:
		return fmt.Errorf("%w: a filing states one of `filing`, `topic` or `no_topic`", ErrReviewEmpty)
	case answers > 1:
		return fmt.Errorf("explore: a filing states one of `filing`, `topic` or `no_topic`, not %d of them",
			answers)
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
	}
	return nil
}

// validateTopicProposal refuses a proposal the operator could not act on.
//
// The binding requirement is the load-bearing one. §4.13 admits a repository,
// a machine, a service or a concept as a topic and requires each to be bound
// to something real; a proposal with a name and no binding is a folder, which
// is the one thing a topic is not.
func validateTopicProposal(p *TopicProposal) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("explore: a topic proposal needs a name")
	}
	if !slices.Contains(reality.EntityKinds(), reality.EntityKind(p.Kind)) {
		return fmt.Errorf("explore: %q is not an entity kind the ledger admits", p.Kind)
	}
	if strings.TrimSpace(p.Reasoning) == "" {
		return fmt.Errorf("explore: a topic proposal says why the topic should exist")
	}
	if strings.TrimSpace(p.Remote) == "" && len(p.Paths) == 0 && strings.TrimSpace(p.Definition) == "" {
		return fmt.Errorf("explore: a topic proposal binds to something real — a repository remote, a " +
			"path, or a one-sentence definition — because a topic with no binding is a folder")
	}
	return nil
}

// file carries out the answer: the store write, and the record of which answer
// it was.
//
// The unresolvable name is the interesting path and it is deliberately not a
// failure. §4.13: "a run's structured result may name the entities its records
// are about the way it names question subjects, resolved through aliases and
// refused when unknown, and the refusal is what makes the run raise a topic
// question instead". So a filing under a name nobody has created becomes the
// question that proposes it, the record stays honestly unfiled, and the pass
// completes — because it did the work it was drawn for.
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

// propose raises one topic question and reports it as this pass's outcome.
func (r *Reviewer) propose(st *reviewState, record evaluation.Subject,
	proposal TopicProposal) (*evaluation.Filing, error) {
	question, err := r.cfg.Topics.Propose(st.commit, record, st.opt.Assignment.RunID, proposal)
	if err != nil {
		return nil, fmt.Errorf("explore: propose the topic %q: %w", proposal.Name, err)
	}
	return &evaluation.Filing{
		Outcome:  evaluation.FilingProposed,
		Question: question,
		Reason:   strings.TrimSpace(proposal.Reasoning),
	}, nil
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
// unresolvable name proposed from two records is one question rather than two.
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
		return "topic proposed as question " + filing.Question
	case evaluation.FilingNone:
		return "no topic: " + filing.Reason
	}
	return filing.Outcome
}
