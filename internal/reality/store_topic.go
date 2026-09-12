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

// topicSubjectKey derives a plan's opaque subject-matter key. It is digested
// for aliasKey's reason: a remote and a checkout directory are operator
// vocabulary, and §9 keeps them out of every plaintext column.
func topicSubjectKey(subject string) string {
	return digestKey("babel/reality/topic", subject)
}

// ProposeTopic attaches §4.13's plan to the proposal record a run published.
//
// The run wrote an ordinary proposal through the ordinary chain and the
// operator will rule on it like any other; this is what that ruling would
// *do*, recorded beside it so that acceptance has something to apply. Nothing
// here creates, merges, splits or retires anything — §4.8's rule stands
// unchanged, and only the operator's acceptance applies a plan.
//
// Four refusals matter, and they are different from one another.
//
// A proposal that already carries a plan is ErrConflict. The plan is
// immutable and one per proposal: a run that changes its mind publishes
// another proposal, which is what the frontier's revision chain is for.
//
// An identity that already binds a live entity is ErrTopicBound, and the error
// names that entity. The ledger already holds the thing; what the caller has
// is a name for it, which is an alias or a filing rather than a new subject,
// and minting a second identity for one thing is exactly the confusion §4.8's
// merge history exists to undo.
//
// A subject matter already proposed and unruled is ErrConflict through the
// same key: two runs that met the same repository, or proposed the same
// merge, produce one thing for the operator to rule on rather than two.
//
// A subject matter the operator declined is ErrSuppressed until materially new
// evidence exists, and for a topic §4.13's "materially new" is measurable:
// more sessions than stood behind it when he refused. The count is recorded on
// the plan, so a re-proposal with the same or less evidence is the repetition
// suppression exists to stop.
func (s *Store) ProposeTopic(ctx context.Context, plan TopicPlan) error {
	admitted, key, err := s.admitTopicPlan(ctx, plan, true)
	if err != nil {
		return err
	}
	plan = admitted
	encoded, err := marshalPayload(topicPayload{
		Identity:   plan.Identity,
		Targets:    plan.Targets,
		Entity:     plan.Entity,
		Filings:    plan.Filings,
		Considered: sortedUnique(plan.Considered),
		Reasoning:  plan.Reasoning,
		By:         plan.By,
	})
	if err != nil {
		return err
	}
	// Nothing is staged. A plan is Babel's reasoning about a proposal
	// record that publishes on its own terms, and publish.go says why a
	// derivation does not travel: a second machine holding the same ledger
	// and the same proposal derives it for itself. What no other machine
	// can produce is the operator's acceptance, and that publishes the
	// entity, the resolution and the facts.
	return s.transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_topic_plan(
			proposal_id, subject_key, operation, entity_kind, evidence_weight,
			created_at, payload_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			plan.ProposalID, key, string(plan.Operation), string(plan.Kind()),
			plan.Sessions, formatTime(s.now()), encoded); err != nil {
			return fmt.Errorf("reality: insert topic plan: %w", err)
		}
		return nil
	})
}

// CheckTopicPlan answers whether ProposeTopic would take this plan, and
// writes nothing.
//
// It exists because of write ordering rather than caution. A plan is keyed by
// the proposal record that carries it, so a run has to publish the whole
// chain — hypothesis, observation, finding, proposal — before it can record
// the plan at all; a refusal discovered at that point has already littered
// the frontier with records explaining a proposal the ledger will not accept.
// Asking first is what lets a run skip the topic and write nothing.
//
// ProposalID is ignored, because there is no proposal yet. Every other rule
// is the same rule ProposeTopic applies, shared rather than restated: the
// shape, the credential refusals, the targets, the already-bound identity,
// the open duplicate and the suppressed decline. A plan admitted here can
// still be refused at ProposeTopic — another run may propose the same
// identity in between — and that refusal is the honest one, because by then
// two runs really did race.
func (s *Store) CheckTopicPlan(ctx context.Context, plan TopicPlan) error {
	if strings.TrimSpace(plan.ProposalID) == "" {
		// A placeholder stands in for the record the caller has not
		// written yet, so the shape check answers about the plan rather
		// than about the absence of an identifier.
		plan.ProposalID = "pending-proposal"
	}
	_, _, err := s.admitTopicPlan(ctx, plan, false)
	return err
}

// admitTopicPlan applies §4.13's admission rule and reports the plan with its
// targets stated canonically, beside the subject-matter key it would be
// stored under. It writes nothing; keyed says whether the proposal record's
// own uniqueness is part of the question, which it is not for a pre-check.
func (s *Store) admitTopicPlan(ctx context.Context, plan TopicPlan, keyed bool) (
	TopicPlan, string, error) {
	if err := plan.validate(); err != nil {
		return TopicPlan{}, "", err
	}
	if keyed {
		if _, found, err := s.TopicPlan(ctx, plan.ProposalID); err != nil {
			return TopicPlan{}, "", err
		} else if found {
			return TopicPlan{}, "", fmt.Errorf("%w: proposal %s already carries a topic plan",
				ErrConflict, plan.ProposalID)
		}
	}
	targets, err := s.resolveTargets(ctx, plan)
	if err != nil {
		return TopicPlan{}, "", err
	}
	plan.Targets = targets
	if plan.Operation.creates() {
		bound, err := s.EntityBoundTo(ctx, plan.Identity)
		if err != nil {
			return TopicPlan{}, "", err
		}
		if bound != "" {
			// The identity stays out of the message for §9's reason;
			// the entity id is the caller's handle on what holds it.
			return TopicPlan{}, "", fmt.Errorf("%w: entity %s", ErrTopicBound, bound)
		}
	}
	for _, entityID := range plan.Considered {
		if err := requireRow(ctx, s.db, "reality_entity", "id", entityID); err != nil {
			return TopicPlan{}, "", fmt.Errorf("reality: considered entity: %w", err)
		}
	}
	key := topicSubjectKey(plan.subjectMatter())
	if err := s.checkTopicEvidence(ctx, key, plan.Sessions); err != nil {
		return TopicPlan{}, "", err
	}
	return plan, key, nil
}

// resolveTargets checks that every entity a plan acts on exists and states it
// canonically, so a plan raised about a name that a merge has since folded
// away is applied to the identity that speaks for it.
func (s *Store) resolveTargets(ctx context.Context, plan TopicPlan) ([]string, error) {
	out := make([]string, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		canonical, err := s.Resolve(ctx, target)
		if err != nil {
			return nil, fmt.Errorf("reality: topic target: %w", err)
		}
		out = append(out, canonical)
	}
	if plan.Operation == TopicMerge && len(out) == 2 && out[0] == out[1] {
		return nil, fmt.Errorf("%w: these two names are already one topic", ErrConflict)
	}
	return out, nil
}

// checkTopicEvidence enforces §4.13's suppression, and the duplicate rule that
// keeps two runs' identical proposals one thing to rule on.
//
// A plan the operator has not ruled on is a duplicate: the same repository, or
// the same merge, is one decision however differently two runs worded the
// proposals carrying it. A plan he declined suppresses the next one until more
// evidence stands behind it than did when he refused, which is the term §4.13
// measures a topic in. An applied plan blocks nothing here — what it created
// is an entity, and EntityBoundTo is the refusal that names it.
func (s *Store) checkTopicEvidence(ctx context.Context, subjectKey string, sessions int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT p.proposal_id, p.evidence_weight,
		(SELECT r.verdict FROM reality_topic_ruling r WHERE r.proposal_id = p.proposal_id)
		FROM reality_topic_plan p WHERE p.subject_key = ?
		ORDER BY p.created_at, p.proposal_id`, subjectKey)
	if err != nil {
		return fmt.Errorf("reality: read topic plans: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			proposalID string
			weight     int
			verdict    sql.NullString
		)
		if err := rows.Scan(&proposalID, &weight, &verdict); err != nil {
			return fmt.Errorf("reality: read topic plans: %w", err)
		}
		switch {
		case !verdict.Valid:
			return fmt.Errorf("%w: proposal %s already proposes this and awaits the operator",
				ErrConflict, proposalID)
		case TopicPlanState(verdict.String) == TopicPlanDeclined && sessions <= weight:
			return fmt.Errorf("%w: the plan on proposal %s was declined with %d sessions "+
				"behind it and this one has %d", ErrSuppressed, proposalID, weight, sessions)
		}
	}
	return rows.Err()
}

// OpenTopicPlans lists the plans no ruling has answered, heaviest evidence
// first, because that is the order a reader deciding what to name would choose
// for himself; ties break on time and id so two reads agree.
func (s *Store) OpenTopicPlans(ctx context.Context) ([]TopicPlan, error) {
	return s.topicPlansWhere(ctx, `WHERE NOT EXISTS(
		SELECT 1 FROM reality_topic_ruling r WHERE r.proposal_id = p.proposal_id)
		ORDER BY p.evidence_weight DESC, p.created_at, p.proposal_id`, 0)
}

// DeclinedTopicPlans lists the plans the operator refused, newest ruling
// first, with his reason kept verbatim.
//
// It exists because §4.13 has the triage recipe read *why* topics were
// declined as evidence for its next proposals, and a recipe that had to
// reconstruct that from dispositions would be reading the ruling rather than
// the reason. A limit of zero or less is every one of them.
func (s *Store) DeclinedTopicPlans(ctx context.Context, limit int) ([]TopicPlan, error) {
	return s.topicPlansWhere(ctx, `WHERE EXISTS(
		SELECT 1 FROM reality_topic_ruling r WHERE r.proposal_id = p.proposal_id
			AND r.verdict = '`+string(TopicPlanDeclined)+`')
		ORDER BY (SELECT r.recorded_at FROM reality_topic_ruling r
			WHERE r.proposal_id = p.proposal_id) DESC, p.proposal_id`, limit)
}

func (s *Store) topicPlansWhere(ctx context.Context, clause string, limit int) ([]TopicPlan, error) {
	query := `SELECT p.proposal_id FROM reality_topic_plan p ` + clause
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	ids, err := queryStrings(ctx, s.db, query)
	if err != nil {
		return nil, err
	}
	out := make([]TopicPlan, 0, len(ids))
	for _, id := range ids {
		plan, found, err := s.TopicPlan(ctx, id)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out = append(out, plan)
	}
	return out, nil
}

// TopicPlan reads the plan attached to one proposal record, reporting whether
// there is one. A proposal with no plan is the ordinary case — most proposals
// are about the corpus rather than about the ledger's naming — so it is a
// false rather than an error.
func (s *Store) TopicPlan(ctx context.Context, proposalID string) (TopicPlan, bool, error) {
	var (
		operation string
		weight    int
		created   string
		encoded   []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT operation, evidence_weight, created_at, payload_json
		FROM reality_topic_plan WHERE proposal_id = ?`, proposalID).
		Scan(&operation, &weight, &created, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return TopicPlan{}, false, nil
	}
	if err != nil {
		return TopicPlan{}, false, fmt.Errorf("reality: read topic plan: %w", err)
	}
	var payload topicPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return TopicPlan{}, false, fmt.Errorf("reality: decode topic plan %s: %w", proposalID, err)
	}
	plan := TopicPlan{
		ProposalID: proposalID,
		Operation:  TopicOperation(operation),
		Targets:    payload.Targets,
		Identity:   payload.Identity,
		Entity:     payload.Entity,
		Filings:    payload.Filings,
		Considered: payload.Considered,
		Reasoning:  payload.Reasoning,
		Sessions:   weight,
		By:         payload.By,
		State:      TopicPlanOpen,
	}
	if plan.CreatedAt, err = parseTime(created); err != nil {
		return TopicPlan{}, false, fmt.Errorf("reality: topic plan %s: %w", proposalID, err)
	}
	if err := s.readTopicRuling(ctx, &plan); err != nil {
		return TopicPlan{}, false, err
	}
	return plan, true, nil
}

// readTopicRuling attaches what the operator did with a plan, leaving it open
// when he has not ruled.
func (s *Store) readTopicRuling(ctx context.Context, plan *TopicPlan) error {
	var (
		verdict      string
		entityID     sql.NullString
		resolutionID sql.NullString
		actor        string
		recorded     string
		encoded      []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT verdict, entity_id, resolution_id, actor,
		recorded_at, payload_json FROM reality_topic_ruling WHERE proposal_id = ?`,
		plan.ProposalID).Scan(&verdict, &entityID, &resolutionID, &actor, &recorded, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reality: read topic ruling: %w", err)
	}
	var payload StatusPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return fmt.Errorf("reality: decode topic ruling %s: %w", plan.ProposalID, err)
	}
	plan.State = TopicPlanState(verdict)
	plan.RuledBy = actor
	plan.Reason = payload.Note
	plan.EntityID = entityID.String
	plan.ResolutionID = resolutionID.String
	at, err := parseTime(recorded)
	if err != nil {
		return fmt.Errorf("reality: topic ruling %s: %w", plan.ProposalID, err)
	}
	plan.RuledAt = at
	return nil
}

// Topic is one subject read as a topic: what it is bound to, and the
// operator's stance toward it.
type Topic struct {
	Entity   Entity
	Binding  Binding
	Bound    bool
	Interest Interest
	// ProposalID is the proposal the operator accepted to create it, empty
	// for a subject he created directly.
	ProposalID string
}

// Topics lists the subjects a reader would call topics: every identity that
// speaks for itself and has not been retired.
//
// The membership is deliberately every entity rather than only the ones an
// applied plan created. §4.13 is explicit that a topic is a Reality Ledger
// entity and nothing else, so a machine the operator named by hand is as
// legitimate a topic as a repository Babel proposed, and a listing that showed
// only Babel's own proposals would be a listing of Babel's opinions.
//
// The order is §4.13's stance order — working, watching, unsaid, not now,
// excluded — and then the display name, so the list reads as what the
// operator is doing rather than as when Babel happened to record things.
func (s *Store) Topics(ctx context.Context) ([]Topic, error) {
	listings, err := s.Entities(ctx, EntityQuery{})
	if err != nil {
		return nil, err
	}
	applied, err := s.appliedTopics(ctx)
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
			ProposalID: applied[listing.Entity.ID],
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

// appliedTopics maps each entity an applied plan created to the proposal it
// came from, so a listing can say which topics the operator accepted from
// Babel's output and which he minted himself.
func (s *Store) appliedTopics(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT entity_id, proposal_id FROM reality_topic_ruling WHERE entity_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("reality: read topic rulings: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var entityID, proposalID string
		if err := rows.Scan(&entityID, &proposalID); err != nil {
			return nil, fmt.Errorf("reality: read topic rulings: %w", err)
		}
		out[entityID] = proposalID
	}
	return out, rows.Err()
}

// DeclineTopicPlan records that the operator refused what a topic proposal
// would have done.
//
// The reason is kept verbatim and is required: §4.13 has the triage recipe
// read why topics were declined as evidence for its next proposals, and a
// refusal with no reason teaches it nothing. Suppression follows from the
// record — a declined subject matter silences the next plan about it until
// materially new evidence exists — so nothing else here has to arrange it.
func (s *Store) DeclineTopicPlan(ctx context.Context, proposalID, operator, reason string) error {
	if operator == "" {
		return fmt.Errorf("%w: a decline has no operator", ErrInvalidValue)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: a declined topic keeps the operator's reason, and this one is empty",
			ErrInvalidValue)
	}
	plan, found, err := s.TopicPlan(ctx, proposalID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: proposal %s carries no topic plan", ErrUnknownRecord, proposalID)
	}
	if plan.State != TopicPlanOpen {
		return fmt.Errorf("%w: the plan on proposal %s is already %s",
			ErrAlreadyDecided, proposalID, plan.State)
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		_, err := s.recordTopicRuling(ctx, tx, proposalID, TopicPlanDeclined, operator, reason, "", "")
		return err
	})
}

// ApplyTopicPlan performs what the operator accepted, in the one act §4.13
// gives him: a topic created, a topic split with the records that move, two
// topics merged, or a topic retired.
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
// So the order is: the ledger's half in one transaction — the entity or the
// resolution or the lifecycle fact, the ruling, all of which commit together
// or not at all — and then the filings. A filing that fails leaves the
// application standing and its records *unfiled*, and the error names them.
// That is the benign direction for this to fail in: §4.13 makes unfiled an
// honest state and the triage backlog, while the alternative ordering would
// leave edges pointing at an entity no acceptance ever created. The returned
// TopicAcceptance is populated either way, exactly as SubjectNaming.Create
// returns the entity it made beside the alias error.
func (s *Store) ApplyTopicPlan(ctx context.Context, proposalID, operator string,
	filer Filer) (TopicAcceptance, error) {
	if operator == "" {
		return TopicAcceptance{}, fmt.Errorf("%w: an acceptance has no operator", ErrInvalidValue)
	}
	plan, found, err := s.TopicPlan(ctx, proposalID)
	if err != nil {
		return TopicAcceptance{}, err
	}
	if !found {
		return TopicAcceptance{}, fmt.Errorf("%w: proposal %s carries no topic plan",
			ErrUnknownRecord, proposalID)
	}
	if plan.State != TopicPlanOpen {
		return TopicAcceptance{}, fmt.Errorf("%w: the plan on proposal %s is already %s",
			ErrAlreadyDecided, proposalID, plan.State)
	}
	if err := s.checkApplicable(ctx, plan); err != nil {
		return TopicAcceptance{}, err
	}
	// The parent is read before the transaction opens because a split's
	// remainder keeps the parent's own name and kind, and this package's
	// entity read is over the store's handle rather than a caller's
	// transaction.
	var parent Entity
	if plan.Operation == TopicSplit {
		if parent, err = s.Entity(ctx, plan.Targets[0]); err != nil {
			return TopicAcceptance{}, err
		}
	}
	if len(plan.Filings) > 0 && filer == nil {
		// Refused rather than silently skipped: §4.13 has accepting a
		// proposal perform the act *and* file the records, and an
		// acceptance that quietly filed nothing would leave the operator
		// believing it had.
		return TopicAcceptance{}, fmt.Errorf(
			"%w: the plan on proposal %s names %d records and no filer was supplied",
			ErrInvalidValue, proposalID, len(plan.Filings))
	}

	acceptance := TopicAcceptance{
		ProposalID: proposalID,
		Operation:  plan.Operation,
		Targets:    plan.Targets,
		Actor:      operator,
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		recorded := s.now()
		authority := Authority{Kind: AuthorityOperator, ID: operator, At: recorded}
		set := s.newRecordSet()
		anchor, err := s.applyTopicOperation(ctx, tx, plan, parent, authority, set, &acceptance)
		if err != nil {
			return err
		}
		ruling, err := s.recordTopicRuling(ctx, tx, proposalID, TopicPlanApplied, operator,
			plan.Reasoning, acceptance.EntityID, resolutionID(acceptance.Resolution))
		if err != nil {
			return err
		}
		acceptance.ID = ruling
		acceptance.RecordedAt = recorded
		// Anchored on the record without which the others must not exist:
		// the entity a create minted, and the resolution or the fact
		// otherwise. The ruling itself does not travel — a reading host
		// has the facts and the resolution, each attributed to the
		// accepting operator, and the local proposal it answered is one
		// it holds for itself.
		pub, err = s.stageSet(ctx, tx, anchor, set)
		return err
	})
	if err != nil {
		return TopicAcceptance{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return acceptance, err
	}
	filings, err := fileRecords(ctx, filer, topicFilings(plan, acceptance.EntityID))
	acceptance.Filings = filings
	if err != nil {
		return acceptance, fmt.Errorf(
			"reality: the plan on proposal %s was applied; its records stay unfiled: %w",
			proposalID, err)
	}
	return acceptance, nil
}

// applyTopicOperation performs the plan's own act inside the caller's
// transaction and reports the record the published closure anchors on.
func (s *Store) applyTopicOperation(ctx context.Context, tx *sql.Tx, plan TopicPlan, parent Entity,
	authority Authority, set *recordSet, acceptance *TopicAcceptance) (string, error) {
	switch plan.Operation {
	case TopicCreate:
		entity, aliases, facts, err := s.applyEntityDraft(ctx, tx, topicDraft(plan),
			authority, "", set)
		if err != nil {
			return "", err
		}
		acceptance.EntityID, acceptance.Entity = entity.ID, entity
		acceptance.Aliases, acceptance.Facts = aliases, facts
		return entity.ID, nil
	case TopicSplit:
		return s.applyTopicSplit(ctx, tx, plan, parent, authority, set, acceptance)
	case TopicMerge:
		resolution, err := s.mergeEntities(ctx, tx, MergeInput{
			SourceIDs: []string{plan.Targets[0]},
			TargetID:  plan.Targets[1],
			Actor:     authority.ID,
			Reason:    plan.Reasoning,
		}, set)
		if err != nil {
			return "", err
		}
		acceptance.Resolution = &resolution
		return resolution.ID, nil
	default:
		fact, err := s.retireEntity(ctx, tx, plan.Targets[0], authority, plan.Reasoning, set)
		if err != nil {
			return "", err
		}
		acceptance.Facts = []Fact{fact}
		return fact.ID, nil
	}
}

// applyTopicSplit carves the new topic out of the one that covered two things,
// and moves the records that belong to it.
//
// §4.8's split creates the parts rather than carving one out, so the parent is
// replaced by two: the remainder, which keeps the parent's own name and kind
// because that is what is left when the new thing is taken out of it, and the
// topic the plan named. The parent keeps its facts and its history — they were
// asserted about the identity as it was then understood, and reattributing
// them would rewrite history — and it stops speaking for itself, which is what
// a reader needs in order to know to look at the parts.
func (s *Store) applyTopicSplit(ctx context.Context, tx *sql.Tx, plan TopicPlan, parent Entity,
	authority Authority, set *recordSet, acceptance *TopicAcceptance) (string, error) {
	resolution, parts, err := s.splitEntity(ctx, tx, SplitInput{
		ParentID: parent.ID,
		Parts: []EntityInput{
			{Kind: parent.Kind, Payload: EntityPayload{
				DisplayName: parent.Payload.DisplayName,
				Notes:       parent.Payload.Notes,
			}},
			{Kind: plan.Kind(), Payload: EntityPayload{
				DisplayName: plan.Name(),
				Notes:       plan.Reasoning,
			}},
		},
		Actor:  authority.ID,
		Reason: plan.Reasoning,
	}, set)
	if err != nil {
		return "", err
	}
	// The parts come back in the order they were asked for, so the new
	// topic is the second. Reading it by name would be looking up something
	// this call already knows.
	created := parts[len(parts)-1]
	aliases, facts, err := s.applyDraftNames(ctx, tx, created.ID, topicDraft(plan), authority, "", set)
	if err != nil {
		return "", err
	}
	acceptance.EntityID, acceptance.Entity = created.ID, created
	acceptance.Aliases, acceptance.Facts = aliases, facts
	acceptance.Resolution = &resolution
	return resolution.ID, nil
}

// checkApplicable refuses a plan the ledger has moved past.
//
// A plan is Babel's reading of the ledger at the moment it ran, and the
// operator may rule on it days later. Between the two the topic it names can
// have been merged into another or retired, and applying a plan against that
// state would either fail deep inside §4.8's own checks with a generic
// conflict or, worse, act on an identity that no longer speaks for anything.
// So each target is checked here and the refusal names the state, which is
// what tells the operator that the answer is to let Babel look again.
func (s *Store) checkApplicable(ctx context.Context, plan TopicPlan) error {
	if plan.Operation.creates() {
		bound, err := s.EntityBoundTo(ctx, plan.Identity)
		if err != nil {
			return err
		}
		if bound != "" {
			return fmt.Errorf("%w: entity %s", ErrTopicBound, bound)
		}
	}
	for _, target := range plan.Targets {
		canonical, err := s.Resolve(ctx, target)
		if err != nil {
			return fmt.Errorf("reality: topic target: %w", err)
		}
		if canonical != target {
			return fmt.Errorf("%w: topic %s was merged into %s after this was proposed",
				ErrConflict, target, canonical)
		}
		retired, err := s.EntityRetired(ctx, target)
		if err != nil {
			return err
		}
		if retired {
			return fmt.Errorf("%w: topic %s was retired after this was proposed",
				ErrConflict, target)
		}
	}
	return nil
}

// recordTopicRuling appends what the operator decided about a plan. The unique
// index on proposal_id is what makes a double-click impossible, for the same
// reason a plan's acceptance is unique.
func (s *Store) recordTopicRuling(ctx context.Context, tx *sql.Tx, proposalID string,
	verdict TopicPlanState, operator, note, entityID, resolutionID string) (string, error) {
	id, err := newID("trl")
	if err != nil {
		return "", err
	}
	encoded, err := marshalPayload(StatusPayload{Note: note})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_topic_ruling(
		id, proposal_id, verdict, entity_id, resolution_id, actor, recorded_at, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		id, proposalID, string(verdict), nullableID(entityID), nullableID(resolutionID),
		operator, formatTime(s.now()), encoded); err != nil {
		return "", fmt.Errorf("reality: record topic ruling: %w", err)
	}
	return id, nil
}

func resolutionID(resolution *Resolution) string {
	if resolution == nil {
		return ""
	}
	return resolution.ID
}

// topicDraft states a plan's proposed entity as the subject a create mints.
//
// The identity becomes an identifier alias, and that is what makes the binding
// enforceable rather than documentary: the next plan for the same repository
// resolves the identity through the ledger's own alias index and is refused as
// bound, which is the check EntityBoundTo performs.
func topicDraft(plan TopicPlan) EntityDraft {
	draft := *plan.Entity
	aliases := make([]AliasInput, 0, len(draft.Subject.Aliases)+2)
	aliases = append(aliases, AliasInput{
		Kind:    AliasIdentifier,
		Payload: AliasPayload{Value: plan.Identity, Note: "the identity this topic is bound by"},
	})
	aliases = append(aliases, AliasInput{
		Kind:    AliasName,
		Payload: AliasPayload{Value: draft.Subject.DisplayName},
	})
	aliases = append(aliases, draft.Subject.Aliases...)
	draft.Subject.Aliases = aliases
	if strings.TrimSpace(draft.Subject.Notes) == "" {
		draft.Subject.Notes = plan.Reasoning
	}
	return draft
}

// topicFilings states the plan's records as filings under the entity that now
// exists.
//
// The author is the plan's provenance rather than the accepting operator, and
// the distinction is §4.13's. A run that proposed a topic judged that each of
// these records is about it, so the filing is the run's; a plan with no run
// behind it judged nothing, so its filings are heuristic and the triage recipe
// knows to revisit them. What the operator accepted is the topic, not each
// record's membership.
func topicFilings(plan TopicPlan, entityID string) []FilingDraft {
	if entityID == "" {
		return nil
	}
	author, authorID := frontier.FilingRun, plan.By.RunID
	heuristic := plan.By.heuristic()
	if heuristic {
		author, authorID = frontier.FilingHeuristic, ""
	}
	out := make([]FilingDraft, 0, len(plan.Filings))
	for _, filing := range plan.Filings {
		rationale := filing.Rationale
		if strings.TrimSpace(rationale) == "" {
			rationale = plan.Reasoning
		}
		out = append(out, FilingDraft{
			Record:    filing.Record,
			EntityID:  entityID,
			Rationale: rationale,
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
// It is shared by ApplyTopicPlan and by an accepted plan's create-entity
// action, so a topic the operator accepted from a proposal and one he accepted
// as part of an answer plan are the same record written the same way.
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
	aliases, facts, err := s.applyDraftNames(ctx, tx, entity.ID, draft, authority, contextID, set)
	if err != nil {
		return Entity{}, nil, nil, err
	}
	return entity, aliases, facts, nil
}

// applyDraftNames attaches a draft's aliases and binding facts to a subject
// that already exists in the caller's transaction.
//
// It is separate from applyEntityDraft because a split does not mint its parts
// here — §4.8's splitEntity does, so that the parts belong to the resolution
// that says the parent covered two things — and the new part still has to
// receive the identity, the names and the binding the plan proposed.
func (s *Store) applyDraftNames(ctx context.Context, tx *sql.Tx, entityID string, draft EntityDraft,
	authority Authority, contextID string, set *recordSet) ([]Alias, []Fact, error) {
	aliases := make([]Alias, 0, len(draft.Subject.Aliases))
	for _, alias := range draft.Subject.Aliases {
		if strings.TrimSpace(alias.Payload.Value) == "" {
			continue
		}
		// The subject is this entity whatever the caller put there, for
		// SubjectNaming.Create's reason: an alias must not be smuggled
		// onto another subject by an acceptance.
		alias.EntityID = entityID
		added, err := s.addAlias(ctx, tx, alias)
		if err != nil {
			return nil, nil, fmt.Errorf("reality: topic alias %s: %w", alias.Kind, err)
		}
		aliases = append(aliases, added)
	}
	facts := make([]Fact, 0, len(draft.Binding))
	for _, input := range draft.Binding {
		input.SubjectID = entityID
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
			return nil, nil, err
		}
		fact, _, err := s.assertFact(ctx, tx, input, "", "", "")
		if err != nil {
			return nil, nil, err
		}
		// A dispute cannot arise for a subject this transaction created,
		// and a split's new part is one of those.
		if err := set.add(stagedFact(fact)); err != nil {
			return nil, nil, err
		}
		facts = append(facts, fact)
	}
	return aliases, facts, nil
}

// EntityBoundTo names the live entity an identity already binds, or "" when
// none does.
//
// Two mechanisms answer, in the order that makes the answer cheap. An
// identifier, repository, path or name alias is an indexed digest lookup and
// is how every topic this package creates is findable. A binding fact —
// repository-remote or local-path — is the second, because an entity the
// operator created by hand through `babel reality entity create` carries facts
// and may carry no alias at all, and a plan that ignored it would offer to
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
