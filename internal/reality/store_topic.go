package reality

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/frontier"
)

// topicIdentityKey derives a proposal's opaque subject-matter key. It is
// digested for aliasKey's reason: a remote and a checkout directory are
// operator vocabulary, and §9 keeps them out of every plaintext column.
func topicIdentityKey(identity string) string {
	return digestKey("babel/reality/topic", normalizeAlias(identity))
}

// AskTopic raises §4.13's topic question: a run proposes an identity, and the
// operator is the only one who can create it.
//
// Three refusals matter, and they are different from one another.
//
// An identity that already binds a live entity is ErrTopicBound, and the error
// names that entity. The ledger already holds the thing; what the caller has
// is a name for it, which is an alias or a filing rather than a new subject,
// and minting a second identity for one thing is exactly the confusion §4.8's
// merge history exists to undo.
//
// An identity already proposed and still open is ErrDuplicateQuestion, through
// the same deduplication every other question uses: the proposal is keyed by
// identity rather than by wording, so two runs that met the same repository
// raise one question.
//
// An identity the operator declined is ErrSuppressed until materially new
// evidence exists, and for a topic §4.13's "materially new" is measurable:
// more sessions than stood behind it when he refused. The count is recorded on
// the proposal, so a re-ask with the same or less evidence is the repetition
// suppression exists to stop, and one with more supersedes the refusal and
// links to it.
func (s *Store) AskTopic(ctx context.Context, in TopicProposal, by Provenance) (Question, error) {
	if err := in.validate(); err != nil {
		return Question{}, err
	}
	bound, err := s.EntityBoundTo(ctx, in.Identity)
	if err != nil {
		return Question{}, err
	}
	if bound != "" {
		// The identity stays out of the message for §9's reason; the
		// entity id is the caller's handle on what already holds it.
		return Question{}, fmt.Errorf("%w: entity %s", ErrTopicBound, bound)
	}
	for _, entityID := range in.Considered {
		if err := requireRow(ctx, s.db, "reality_entity", "id", entityID); err != nil {
			return Question{}, fmt.Errorf("reality: considered entity: %w", err)
		}
	}
	key := topicIdentityKey(in.Identity)
	if err := s.checkTopicEvidence(ctx, key, in.Sessions); err != nil {
		return Question{}, err
	}

	payload := topicPayload{
		Name:       in.Name,
		Identity:   in.Identity,
		Aliases:    in.Aliases,
		Binding:    in.Binding,
		Reasoning:  in.Reasoning,
		Records:    in.Records,
		Considered: sortedUnique(in.Considered),
		Sessions:   in.Sessions,
		By:         by,
	}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return Question{}, err
	}

	var record Question
	err = s.transact(ctx, func(tx *sql.Tx) error {
		created, err := s.ask(ctx, tx, QuestionInput{
			Kind:              QuestionTopic,
			Class:             ClassMaintenance,
			Sensitivity:       SensitivityRoutine,
			ExpectedAuthority: AuthorityOperator,
			MaterialEvidence:  topicEvidence(key, in.Sessions),
			Payload: QuestionPayload{
				Prompt:   topicPrompt(in),
				WhyAsked: in.Reasoning,
			},
			identity: key,
		}, by.actor())
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_topic_proposal(
			question_id, identity_key, entity_kind, evidence_weight, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?)`,
			created.ID, key, string(in.Kind), in.Sessions,
			formatTime(created.CreatedAt), encoded); err != nil {
			return fmt.Errorf("reality: insert topic proposal: %w", err)
		}
		record = created
		return nil
	})
	if err != nil {
		return Question{}, err
	}
	// Nothing is staged. A topic question is a question, and publish.go
	// says why a question does not travel: Babel derives it, and a second
	// machine holding the same ledger and the same catalog proposes the
	// same topic itself. What no other machine can produce is the
	// operator's acceptance, and that publishes the entity and its facts.
	return record, nil
}

// topicPrompt is what the inbox shows. It is prose about the corpus, so it
// lives in the sealed payload with the reasoning.
func topicPrompt(in TopicProposal) string {
	return fmt.Sprintf("Is %q a %s worth naming as a topic?", in.Name, in.Kind)
}

// topicEvidence states what stands behind a proposal in terms a later ask can
// be compared against. The identity's digest is always present, so a repeat
// with nothing new offers nothing new; the session count is the term that
// moves when the world says more than it did.
func topicEvidence(identityKey string, sessions int) []string {
	return []string{identityKey, fmt.Sprintf("sessions:%d", sessions)}
}

// checkTopicEvidence enforces §4.13's suppression in the term a topic is
// measured in. Question.checkDuplicate already refuses a re-ask that offers no
// new evidence key at all; this refuses one that offers a *smaller* count,
// which the generic comparison cannot see because a different number is a
// different string.
func (s *Store) checkTopicEvidence(ctx context.Context, identityKey string, sessions int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT p.question_id, p.evidence_weight,
		(SELECT e.state FROM reality_question_event e WHERE e.question_id = p.question_id
			ORDER BY e.seq DESC LIMIT 1)
		FROM reality_topic_proposal p WHERE p.identity_key = ?
		ORDER BY p.created_at, p.question_id`, identityKey)
	if err != nil {
		return fmt.Errorf("reality: read topic proposals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			questionID string
			weight     int
			state      string
		)
		if err := rows.Scan(&questionID, &weight, &state); err != nil {
			return fmt.Errorf("reality: read topic proposals: %w", err)
		}
		if QuestionState(state) != QuestionDeclined {
			continue
		}
		if sessions > weight {
			continue
		}
		return fmt.Errorf("%w: topic question %q was declined with %d sessions behind it "+
			"and this ask has %d", ErrSuppressed, questionID, weight, sessions)
	}
	return rows.Err()
}

// TopicProposals lists the topic questions awaiting the operator.
//
// Open only: a declined proposal is a refusal that must stay refused, and an
// accepted one is an entity, so neither is something to propose again. The
// order is the evidence behind them, heaviest first, because that is the order
// a reader deciding what to name would choose for himself; ties break on time
// and id so two reads agree.
func (s *Store) TopicProposals(ctx context.Context) ([]TopicQuestion, error) {
	ids, err := queryStrings(ctx, s.db, `SELECT p.question_id FROM reality_topic_proposal p
		WHERE (SELECT e.state FROM reality_question_event e WHERE e.question_id = p.question_id
			ORDER BY e.seq DESC LIMIT 1) = ?
		ORDER BY p.evidence_weight DESC, p.created_at, p.question_id`, string(QuestionOpen))
	if err != nil {
		return nil, err
	}
	out := make([]TopicQuestion, 0, len(ids))
	for _, id := range ids {
		topic, err := s.TopicQuestion(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, topic)
	}
	return out, nil
}

// Topic is one subject read as a topic: what it is bound to, and the
// operator's stance toward it.
type Topic struct {
	Entity   Entity
	Binding  Binding
	Bound    bool
	Interest Interest
	// QuestionID is the proposal the operator accepted to create it, empty
	// for a subject he created directly.
	QuestionID string
}

// Topics lists the subjects a reader would call topics: every identity that
// speaks for itself and has not been retired.
//
// The membership is deliberately every entity rather than only the ones an
// accepted proposal created. §4.13 is explicit that a topic is a Reality
// Ledger entity and nothing else, so a machine the operator named by hand is
// as legitimate a topic as a repository Babel proposed, and a listing that
// showed only Babel's own proposals would be a listing of Babel's opinions.
//
// The order is §4.13's stance order — working, watching, unsaid, not now,
// excluded — and then the display name, so the list reads as what the
// operator is doing rather than as when Babel happened to record things.
func (s *Store) Topics(ctx context.Context) ([]Topic, error) {
	listings, err := s.Entities(ctx, EntityQuery{})
	if err != nil {
		return nil, err
	}
	accepted, err := s.acceptedTopics(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Topic, 0, len(listings))
	for _, listing := range listings {
		if listing.Entity.Role != RoleSelf && listing.Entity.Role != RoleSplit {
			// A merged-away identity is still readable, and it is not a
			// topic: the identity that absorbed it speaks for it now.
			continue
		}
		interest, err := s.EntityInterest(ctx, listing.Entity.ID)
		if err != nil {
			return nil, err
		}
		retired, err := s.EntityRetired(ctx, listing.Entity.ID)
		if err != nil {
			return nil, err
		}
		if retired {
			continue
		}
		binding, bound, err := s.EntityBinding(ctx, listing.Entity.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, Topic{
			Entity:     listing.Entity,
			Binding:    binding,
			Bound:      bound,
			Interest:   interest,
			QuestionID: accepted[listing.Entity.ID],
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := interestRank(out[i].Interest.State), interestRank(out[j].Interest.State); a != b {
			return a < b
		}
		if a, b := out[i].Entity.Payload.DisplayName, out[j].Entity.Payload.DisplayName; a != b {
			return a < b
		}
		return out[i].Entity.ID < out[j].Entity.ID
	})
	return out, nil
}

// interestRank orders the stances as §4.13's page does: what he is working on
// first, what he has excluded last, and what nobody has said in the middle
// rather than at the bottom — an unsaid stance is not a weak one.
func interestRank(state string) int {
	switch state {
	case InterestWorking:
		return 0
	case InterestWatching:
		return 1
	case "":
		return 2
	case InterestNotNow:
		return 3
	case InterestExcluded:
		return 4
	}
	return 5
}

// acceptedTopics maps each entity an accepted proposal created to the question
// it came from, so a listing can say which topics the operator named from an
// inbox and which he minted himself.
func (s *Store) acceptedTopics(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id, question_id FROM reality_topic_acceptance`)
	if err != nil {
		return nil, fmt.Errorf("reality: read topic acceptances: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var entityID, questionID string
		if err := rows.Scan(&entityID, &questionID); err != nil {
			return nil, fmt.Errorf("reality: read topic acceptances: %w", err)
		}
		out[entityID] = questionID
	}
	return out, rows.Err()
}

// TopicQuestion reads one proposal with the question carrying it, and the
// entity an acceptance created from it.
func (s *Store) TopicQuestion(ctx context.Context, questionID string) (TopicQuestion, error) {
	question, err := readQuestion(ctx, s.db, questionID)
	if err != nil {
		return TopicQuestion{}, err
	}
	proposal, payload, err := readTopicProposal(ctx, s.db, questionID)
	if err != nil {
		return TopicQuestion{}, err
	}
	topic := TopicQuestion{Question: question, Proposal: proposal, By: payload.By}
	var entityID sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT entity_id FROM reality_topic_acceptance WHERE question_id = ?`,
		questionID).Scan(&entityID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return TopicQuestion{}, fmt.Errorf("reality: read topic acceptance: %w", err)
	}
	topic.EntityID = entityID.String
	return topic, nil
}

func readTopicProposal(ctx context.Context, q querier, questionID string) (TopicProposal, topicPayload, error) {
	var (
		kind    string
		weight  int
		encoded []byte
	)
	err := q.QueryRowContext(ctx, `SELECT entity_kind, evidence_weight, payload_json
		FROM reality_topic_proposal WHERE question_id = ?`, questionID).Scan(&kind, &weight, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return TopicProposal{}, topicPayload{}, fmt.Errorf("%w: topic proposal for question %q",
			ErrUnknownRecord, questionID)
	}
	if err != nil {
		return TopicProposal{}, topicPayload{}, fmt.Errorf("reality: read topic proposal: %w", err)
	}
	var payload topicPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return TopicProposal{}, topicPayload{}, fmt.Errorf("reality: decode topic proposal %s: %w",
			questionID, err)
	}
	return TopicProposal{
		Name:       payload.Name,
		Kind:       EntityKind(kind),
		Aliases:    payload.Aliases,
		Binding:    payload.Binding,
		Reasoning:  payload.Reasoning,
		Records:    payload.Records,
		Considered: payload.Considered,
		Identity:   payload.Identity,
		Sessions:   weight,
	}, payload, nil
}

// DeclineTopic records the operator's refusal of a proposal.
//
// The reason is kept verbatim and is required: §4.13 has the triage recipe
// read why topics were declined as evidence for its next proposals, and a
// refusal with no reason teaches it nothing. Suppression follows from the
// state — a declined question silences an equivalent re-ask until materially
// new evidence exists — so nothing else here has to arrange it.
func (s *Store) DeclineTopic(ctx context.Context, questionID, operator, reason string) error {
	if operator == "" {
		return fmt.Errorf("%w: a decline has no operator", ErrInvalidValue)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: a declined topic keeps the operator's reason, and this one is empty",
			ErrInvalidValue)
	}
	question, err := readQuestion(ctx, s.db, questionID)
	if err != nil {
		return err
	}
	if question.Kind != QuestionTopic {
		return fmt.Errorf("%w: question %q is a %s question, not a topic proposal",
			ErrInvalidValue, questionID, question.Kind)
	}
	return s.SetQuestionState(ctx, QuestionStateInput{
		QuestionID: questionID,
		State:      QuestionDeclined,
		Actor:      operator,
		Note:       reason,
	})
}

// AcceptTopic creates the topic the operator accepted and files the records it
// named.
//
// The transaction boundary is the interesting part, and it is a consequence of
// §9's storage layout rather than a choice made here. The Reality Ledger and
// the hypothesis frontier are two components of one durable.db file, reached
// through two *sql.DB handles with one connection each; SQLite's write lock is
// per file, and every transaction in Babel begins IMMEDIATE. So a filing
// attempted from inside this transaction would block on the write lock this
// transaction is holding until the busy timeout expired and then fail — the
// deadlock internal/reality's HypothesisSink already exists to avoid. Sharing
// one transaction would require the frontier to accept this package's *sql.Tx
// and write its tables through it, which is a cross-component coupling the
// codebase has deliberately refused twice (HypothesisSink, RecordPlan).
//
// So the order is: the ledger's half in one transaction — the entity, its
// aliases, its binding facts, the acceptance and the question's disposition,
// all of which commit together or not at all — and then the filings. A filing
// that fails leaves the acceptance standing and its records *unfiled*, and the
// error names them. That is the benign direction for this to fail in: §4.13
// makes unfiled an honest state and the triage backlog, while the alternative
// ordering would leave edges pointing at an entity no acceptance ever created.
// The returned TopicAcceptance is populated either way, exactly as
// SubjectNaming.Create returns the entity it made beside the alias error.
func (s *Store) AcceptTopic(ctx context.Context, questionID, operator string,
	filer Filer) (TopicAcceptance, error) {
	if operator == "" {
		return TopicAcceptance{}, fmt.Errorf("%w: an acceptance has no operator", ErrInvalidValue)
	}
	topic, err := s.TopicQuestion(ctx, questionID)
	if err != nil {
		return TopicAcceptance{}, err
	}
	if topic.EntityID != "" {
		return TopicAcceptance{}, fmt.Errorf("%w: topic question %q already created entity %s",
			ErrAlreadyDecided, questionID, topic.EntityID)
	}
	if topic.Question.State != QuestionOpen {
		return TopicAcceptance{}, fmt.Errorf("%w: question %q is %s",
			ErrInvalidTransition, questionID, topic.Question.State)
	}
	bound, err := s.EntityBoundTo(ctx, topic.Proposal.Identity)
	if err != nil {
		return TopicAcceptance{}, err
	}
	if bound != "" {
		return TopicAcceptance{}, fmt.Errorf("%w: entity %s", ErrTopicBound, bound)
	}
	if len(topic.Proposal.Records) > 0 && filer == nil {
		// Refused rather than silently skipped: §4.13 has accepting a
		// proposal create the entity *and* file the records, and an
		// acceptance that quietly filed nothing would leave the operator
		// believing it had.
		return TopicAcceptance{}, fmt.Errorf("%w: topic question %q names %d records and no filer was supplied",
			ErrInvalidValue, questionID, len(topic.Proposal.Records))
	}

	acceptance := TopicAcceptance{QuestionID: questionID, Actor: operator}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		recorded := s.now()
		authority := Authority{Kind: AuthorityOperator, ID: operator, At: recorded}
		set := s.newRecordSet()
		entity, aliases, facts, err := s.applyEntityDraft(ctx, tx, topicDraft(topic.Proposal),
			authority, "", set)
		if err != nil {
			return err
		}
		id, err := newID("tac")
		if err != nil {
			return err
		}
		encoded, err := marshalPayload(StatusPayload{Note: topic.Proposal.Reasoning})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_topic_acceptance(
			id, question_id, entity_id, actor, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
			id, questionID, entity.ID, operator, formatTime(recorded), encoded); err != nil {
			return fmt.Errorf("reality: record topic acceptance: %w", err)
		}
		if err := s.transitionQuestion(ctx, tx, questionID, QuestionAnswered, operator,
			"topic accepted as entity "+entity.ID); err != nil {
			return err
		}
		acceptance.ID = id
		acceptance.EntityID = entity.ID
		acceptance.Entity = entity
		acceptance.Aliases = aliases
		acceptance.Facts = facts
		acceptance.RecordedAt = recorded
		// Anchored on the entity, which is the record without which the
		// others must not exist. The acceptance row itself does not
		// travel, for the reason its question does not: a reading host
		// has the facts, each attributed to the accepting operator, and
		// the local question the acceptance disposed of is one it derives
		// for itself.
		pub, err = s.stageSet(ctx, tx, entity.ID, set)
		return err
	})
	if err != nil {
		return TopicAcceptance{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return acceptance, err
	}
	filings, err := fileRecords(ctx, filer, topicFilings(topic, acceptance.EntityID))
	acceptance.Filings = filings
	if err != nil {
		return acceptance, fmt.Errorf(
			"reality: topic %s accepted as entity %s; its records stay unfiled: %w",
			questionID, acceptance.EntityID, err)
	}
	return acceptance, nil
}

// topicDraft states a proposal as the subject a create-entity action mints.
//
// The identity becomes an identifier alias, and that is what makes the binding
// enforceable rather than documentary: the next proposal of the same
// repository resolves the identity through the ledger's own alias index and is
// refused as bound, which is the check EntityBoundTo performs.
func topicDraft(in TopicProposal) EntityDraft {
	aliases := make([]AliasInput, 0, len(in.Aliases)+2)
	aliases = append(aliases, AliasInput{
		Kind:    AliasIdentifier,
		Payload: AliasPayload{Value: in.Identity, Note: "the identity this topic is bound by"},
	})
	aliases = append(aliases, AliasInput{Kind: AliasName, Payload: AliasPayload{Value: in.Name}})
	aliases = append(aliases, in.Aliases...)
	return EntityDraft{
		Subject: NewSubject{
			Kind:        in.Kind,
			DisplayName: in.Name,
			Notes:       in.Reasoning,
			Aliases:     aliases,
		},
		Binding: in.Binding,
	}
}

// topicFilings states the proposal's records as filings under the entity that
// now exists.
//
// The author is the proposal's provenance rather than the accepting operator,
// and the distinction is §4.13's. A run that proposed a topic judged that each
// of these records is about it, so the filing is the run's; a proposal derived
// from repository identity alone judged nothing, so its filings are heuristic
// and the triage recipe knows to revisit them. What the operator accepted is
// the topic, not each record's membership.
func topicFilings(topic TopicQuestion, entityID string) []FilingDraft {
	author, authorID := frontier.FilingRun, topic.By.RunID
	heuristic := topic.By.heuristic()
	if heuristic {
		author, authorID = frontier.FilingHeuristic, ""
	}
	out := make([]FilingDraft, 0, len(topic.Proposal.Records))
	for _, record := range topic.Proposal.Records {
		out = append(out, FilingDraft{
			Record:    record,
			EntityID:  entityID,
			Rationale: topic.Proposal.Reasoning,
			Author:    author,
			AuthorID:  authorID,
			Heuristic: heuristic,
		})
	}
	return out
}

// fileRecords hands a set of drafts to the Filer, stopping at the first
// refusal. It stops rather than continuing because a Filer that refused one
// record has usually refused all of them for the same reason, and the caller
// is told what did land.
func fileRecords(ctx context.Context, filer Filer, drafts []FilingDraft) ([]frontier.Filing, error) {
	if len(drafts) == 0 {
		return nil, nil
	}
	if filer == nil {
		return nil, fmt.Errorf("%w: no filer was supplied", ErrInvalidValue)
	}
	out := make([]frontier.Filing, 0, len(drafts))
	for _, draft := range drafts {
		filing, err := filer.File(ctx, frontier.FilingInput{
			Record:    draft.Record,
			EntityID:  draft.EntityID,
			Rationale: draft.Rationale,
			Author:    draft.Author,
			AuthorID:  draft.AuthorID,
			Heuristic: draft.Heuristic,
		})
		if err != nil {
			return out, fmt.Errorf("file record %s: %w", draft.Record.ID, err)
		}
		out = append(out, filing)
	}
	return out, nil
}

// applyEntityDraft mints a subject, its names and its binding facts inside the
// caller's transaction, under the accepting operator's authority.
//
// It is shared by AcceptTopic and by an accepted plan's create-entity action,
// so a topic the operator accepted from its own page and one he accepted as
// part of an answer plan are the same record written the same way.
func (s *Store) applyEntityDraft(ctx context.Context, tx *sql.Tx, draft EntityDraft,
	authority Authority, contextID string, set *recordSet) (Entity, []Alias, []Fact, error) {
	entity, err := s.createEntity(ctx, tx, EntityInput{
		Kind:    draft.Subject.Kind,
		Payload: EntityPayload{DisplayName: draft.Subject.DisplayName, Notes: draft.Subject.Notes},
	})
	if err != nil {
		return Entity{}, nil, nil, err
	}
	if err := set.add(stagedEntity(entity)); err != nil {
		return Entity{}, nil, nil, err
	}
	aliases := make([]Alias, 0, len(draft.Subject.Aliases))
	for _, alias := range draft.Subject.Aliases {
		if strings.TrimSpace(alias.Payload.Value) == "" {
			continue
		}
		// The subject is this entity whatever the caller put there, for
		// SubjectNaming.Create's reason: an alias must not be smuggled
		// onto another subject by an acceptance.
		alias.EntityID = entity.ID
		added, err := s.addAlias(ctx, tx, alias)
		if err != nil {
			return Entity{}, nil, nil, fmt.Errorf("reality: topic alias %s: %w", alias.Kind, err)
		}
		aliases = append(aliases, added)
	}
	facts := make([]Fact, 0, len(draft.Binding))
	for _, input := range draft.Binding {
		input.SubjectID = entity.ID
		input.Authority = authority
		if input.ValidFrom.IsZero() {
			input.ValidFrom = authority.At
		}
		if input.ObservedAt.IsZero() {
			input.ObservedAt = authority.At
		}
		if input.Confidence == "" {
			input.Confidence = ConfidenceHigh
		}
		if input.Sensitivity == "" {
			input.Sensitivity = SensitivityRoutine
		}
		if input.ContextID == "" {
			input.ContextID = contextID
		}
		if err := input.validate(); err != nil {
			return Entity{}, nil, nil, err
		}
		fact, _, err := s.assertFact(ctx, tx, input, "", "", "")
		if err != nil {
			return Entity{}, nil, nil, err
		}
		// A dispute cannot arise here: the subject was created by this
		// transaction, so nothing can already claim its predicates.
		if err := set.add(stagedFact(fact)); err != nil {
			return Entity{}, nil, nil, err
		}
		facts = append(facts, fact)
	}
	return entity, aliases, facts, nil
}

// EntityBoundTo names the live entity an identity already binds, or "" when
// none does.
//
// Two mechanisms answer, in the order that makes the answer cheap. An
// identifier, repository, path or name alias is an indexed digest lookup and
// is how every topic this package creates is findable. A binding fact —
// repository-remote or local-path — is the second, because an entity the
// operator created by hand through `babel reality entity create` carries facts
// and may carry no alias at all, and a proposal that ignored it would offer to
// create a second subject for a repository the ledger already holds.
//
// A retired entity does not bind. §4.13 retires a topic that should never have
// existed, and a retirement that permanently forbade re-proposing the thing it
// named would make the mistake unfixable.
func (s *Store) EntityBoundTo(ctx context.Context, identity string) (string, error) {
	if strings.TrimSpace(identity) == "" {
		return "", fmt.Errorf("%w: no identity given", ErrInvalidValue)
	}
	switch id, err := s.ResolveSubject(ctx, identity); {
	case err == nil:
		retired, err := s.EntityRetired(ctx, id)
		if err != nil {
			return "", err
		}
		if !retired {
			return id, nil
		}
	case errors.Is(err, ErrUnknownRecord):
		// Nothing answers to the name; the facts may still bind it.
	case errors.Is(err, ErrAmbiguousAlias):
		// Two entities answer to it. Either way the ledger already holds
		// something by that name, and proposing a third is not the
		// answer; the caller is told which by resolving it himself.
		return "", err
	default:
		return "", err
	}

	facts, err := readFacts(ctx, s.db, `WHERE f.predicate IN (?, ?)`,
		string(PredicateRepositoryRemote), string(PredicateLocalPath))
	if err != nil {
		return "", err
	}
	wanted := normalizeAlias(identity)
	for _, fact := range facts {
		if fact.Status != FactActive && fact.Status != FactDisputed {
			continue
		}
		if normalizeAlias(fact.Value.Text) != wanted {
			continue
		}
		canonical, err := s.Resolve(ctx, fact.SubjectID)
		if err != nil {
			return "", err
		}
		retired, err := s.EntityRetired(ctx, canonical)
		if err != nil {
			return "", err
		}
		if !retired {
			return canonical, nil
		}
	}
	return "", nil
}

// EntityBinding reports what a subject is bound to, read from the facts and
// names in force. The second result is false when the ledger holds no binding
// for it, which is an honest answer about a subject nobody has bound rather
// than an error.
func (s *Store) EntityBinding(ctx context.Context, entityID string) (Binding, bool, error) {
	canonical, err := s.requireEntity(ctx, entityID)
	if err != nil {
		return Binding{}, false, err
	}
	entity, err := s.Entity(ctx, canonical)
	if err != nil {
		return Binding{}, false, err
	}
	binding := Binding{Kind: entity.Kind}
	for _, predicate := range []Predicate{PredicateRepositoryRemote, PredicateLocalPath} {
		facts, err := s.Facts(ctx, FactQuery{SubjectID: canonical, Predicate: predicate})
		if err != nil {
			return Binding{}, false, err
		}
		current := currentFacts(facts, s.now())
		fact, ok := current[predicate]
		if !ok {
			continue
		}
		if predicate == PredicateRepositoryRemote {
			binding.Remote = fact.Value.Text
			continue
		}
		binding.Paths = append(binding.Paths, fact.Value.Text)
	}
	aliases, err := s.Aliases(ctx, canonical)
	if err != nil {
		return Binding{}, false, err
	}
	paths := map[string]struct{}{}
	for _, path := range binding.Paths {
		paths[path] = struct{}{}
	}
	for _, alias := range aliases {
		if alias.State != StateAsserted {
			continue
		}
		switch alias.Kind {
		case AliasIdentifier:
			if binding.Identity == "" {
				binding.Identity = alias.Payload.Value
			}
		case AliasPath:
			paths[alias.Payload.Value] = struct{}{}
		}
	}
	binding.Paths = make([]string, 0, len(paths))
	for path := range paths {
		binding.Paths = append(binding.Paths, path)
	}
	sort.Strings(binding.Paths)
	// The remote is the identity that survives the same repository being
	// cloned on another machine (§4.13), so it wins when both are present.
	if binding.Remote != "" {
		binding.Identity = binding.Remote
	}
	if binding.Identity == "" && len(binding.Paths) > 0 {
		binding.Identity = binding.Paths[0]
	}
	return binding, binding.Identity != "" || binding.Remote != "" || len(binding.Paths) > 0, nil
}
