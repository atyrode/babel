package reality

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/atyrode/babel/internal/frontier"
)

// backlogSubjectKey derives a plan's opaque subject-matter key, digested for
// topicSubjectKey's reason: it is built from record identifiers and a
// predicate, and §9 keeps the values in the payload.
func backlogSubjectKey(subject string) string {
	return digestKey("babel/reality/backlog", subject)
}

// ProposeBacklog attaches §4.13's backlog plan to the proposal record a run
// published.
//
// The run wrote an ordinary proposal through the ordinary chain and the
// operator will rule on it like any other; this is what that ruling would
// *do*, recorded beside it so acceptance has something to apply. Nothing here
// settles a candidate, and nothing here asserts a fact.
//
// The refusals are ProposeTopic's, for the same reasons. A proposal that
// already carries a plan is ErrConflict, because a plan is immutable and one
// per proposal. A subject matter already proposed and unruled is ErrConflict,
// because two runs that reached the same act on the same candidates give the
// operator one thing to rule on rather than two. A subject matter he declined
// is ErrSuppressed until more stands behind it than did when he refused, which
// for a backlog act is the number of observations the argument rests on.
func (s *Store) ProposeBacklog(ctx context.Context, plan BacklogPlan) error {
	admitted, key, err := s.admitBacklogPlan(ctx, plan, true)
	if err != nil {
		return err
	}
	plan = admitted
	encoded, err := marshalPayload(backlogPayload{
		Hypotheses:   plan.Hypotheses,
		SupersededBy: plan.SupersededBy,
		Finding:      plan.Finding,
		Observation:  plan.Observation,
		Fact:         plan.Fact,
		Reasoning:    plan.Reasoning,
		By:           plan.By,
	})
	if err != nil {
		return err
	}
	// Nothing is staged, for ProposeTopic's reason: a plan is Babel's
	// reasoning about a proposal record that publishes on its own terms, and
	// a second machine holding the same frontier derives it for itself. What
	// no other machine can produce is the operator's acceptance, and that
	// publishes the fact a promotion asserts.
	return s.transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_backlog_plan(
			proposal_id, subject_key, operation, evidence_weight, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?)`,
			plan.ProposalID, key, string(plan.Operation), plan.Evidence,
			formatTime(s.now()), encoded); err != nil {
			return fmt.Errorf("reality: insert backlog plan: %w", err)
		}
		return nil
	})
}

// CheckBacklogPlan answers whether ProposeBacklog would take this plan, and
// writes nothing.
//
// It exists for CheckTopicPlan's reason, which is write ordering rather than
// caution: a plan is keyed by the proposal that carries it, so a run has to
// publish the whole chain before it can record the plan at all, and a refusal
// discovered then has already littered the frontier with records explaining a
// proposal the ledger will not accept.
//
// The proposal and the finding are the two identifiers that do not exist yet,
// so a placeholder stands in for each and the shape check answers about the
// plan rather than about the absence of an identifier. Every other rule is the
// rule ProposeBacklog applies, shared rather than restated.
func (s *Store) CheckBacklogPlan(ctx context.Context, plan BacklogPlan) error {
	if strings.TrimSpace(plan.ProposalID) == "" {
		plan.ProposalID = "pending-proposal"
	}
	if plan.Operation == BacklogConsolidate && strings.TrimSpace(plan.Finding) == "" {
		plan.Finding = "pending-finding"
	}
	_, _, err := s.admitBacklogPlan(ctx, plan, false)
	return err
}

// admitBacklogPlan applies the admission rule and reports the plan beside the
// subject-matter key it would be stored under. It writes nothing; keyed says
// whether the proposal record's own uniqueness is part of the question, which
// it is not for a pre-check.
func (s *Store) admitBacklogPlan(ctx context.Context, plan BacklogPlan, keyed bool) (
	BacklogPlan, string, error) {
	if err := plan.validate(); err != nil {
		return BacklogPlan{}, "", err
	}
	if keyed {
		if _, found, err := s.BacklogPlan(ctx, plan.ProposalID); err != nil {
			return BacklogPlan{}, "", err
		} else if found {
			return BacklogPlan{}, "", fmt.Errorf("%w: proposal %s already carries a backlog plan",
				ErrConflict, plan.ProposalID)
		}
	}
	if plan.Fact != nil {
		// The entity has to be one the ledger already holds: §4.8 keeps
		// creating one the operator's act, and a promotion that could name
		// an unknown subject would mint identity from a run's reading.
		subject, err := s.requireEntity(ctx, plan.Fact.SubjectID)
		if err != nil {
			return BacklogPlan{}, "", fmt.Errorf("reality: promotion subject: %w", err)
		}
		fact := *plan.Fact
		fact.SubjectID = subject
		plan.Fact = &fact
	}
	key := backlogSubjectKey(plan.subjectMatter())
	if err := s.checkBacklogEvidence(ctx, key, plan.Evidence); err != nil {
		return BacklogPlan{}, "", err
	}
	return plan, key, nil
}

// checkBacklogEvidence enforces the duplicate rule and §4.13's suppression, on
// checkTopicEvidence's terms: an unruled plan about the same act is one
// decision, and a declined one silences the next until more evidence stands
// behind it. An applied plan blocks nothing here — what it did is on the
// candidates themselves, and checkBacklogApplicable is the refusal that names
// their state.
func (s *Store) checkBacklogEvidence(ctx context.Context, subjectKey string, evidence int) error {
	rows, err := s.db.QueryContext(ctx, `SELECT p.proposal_id, p.evidence_weight,
		(SELECT r.verdict FROM reality_backlog_ruling r WHERE r.proposal_id = p.proposal_id)
		FROM reality_backlog_plan p WHERE p.subject_key = ?
		ORDER BY p.created_at, p.proposal_id`, subjectKey)
	if err != nil {
		return fmt.Errorf("reality: read backlog plans: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			proposalID string
			weight     int
			verdict    sql.NullString
		)
		if err := rows.Scan(&proposalID, &weight, &verdict); err != nil {
			return fmt.Errorf("reality: read backlog plans: %w", err)
		}
		switch {
		case !verdict.Valid:
			return fmt.Errorf("%w: proposal %s already proposes this and awaits the operator",
				ErrConflict, proposalID)
		case RulingState(verdict.String) == RulingDeclined && evidence <= weight:
			return fmt.Errorf("%w: the plan on proposal %s was declined with %d observations "+
				"behind it and this one has %d", ErrSuppressed, proposalID, weight, evidence)
		}
	}
	return rows.Err()
}

// OpenBacklogPlans lists the plans no ruling has answered, heaviest evidence
// first, because that is the order a reader working through a backlog would
// choose; ties break on time and id so two reads agree.
func (s *Store) OpenBacklogPlans(ctx context.Context) ([]BacklogPlan, error) {
	return s.backlogPlansWhere(ctx, `WHERE NOT EXISTS(
		SELECT 1 FROM reality_backlog_ruling r WHERE r.proposal_id = p.proposal_id)
		ORDER BY p.evidence_weight DESC, p.created_at, p.proposal_id`, 0)
}

// DeclinedBacklogPlans lists the plans the operator refused, newest ruling
// first, with his reason kept verbatim. It is what a later pass reads before
// proposing the same act again, on DeclinedTopicPlans' terms. A limit of zero
// or less is every one of them.
func (s *Store) DeclinedBacklogPlans(ctx context.Context, limit int) ([]BacklogPlan, error) {
	return s.backlogPlansWhere(ctx, `WHERE EXISTS(
		SELECT 1 FROM reality_backlog_ruling r WHERE r.proposal_id = p.proposal_id
			AND r.verdict = '`+string(RulingDeclined)+`')
		ORDER BY (SELECT r.recorded_at FROM reality_backlog_ruling r
			WHERE r.proposal_id = p.proposal_id) DESC, p.proposal_id`, limit)
}

func (s *Store) backlogPlansWhere(ctx context.Context, clause string, limit int) ([]BacklogPlan, error) {
	query := `SELECT p.proposal_id FROM reality_backlog_plan p ` + clause
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	ids, err := queryStrings(ctx, s.db, query)
	if err != nil {
		return nil, err
	}
	out := make([]BacklogPlan, 0, len(ids))
	for _, id := range ids {
		plan, found, err := s.BacklogPlan(ctx, id)
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

// BacklogPlan reads the plan attached to one proposal record, reporting
// whether there is one. Most proposals carry none — they are about the corpus
// rather than about the backlog — so it is a false rather than an error.
func (s *Store) BacklogPlan(ctx context.Context, proposalID string) (BacklogPlan, bool, error) {
	var (
		operation string
		weight    int
		created   string
		encoded   []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT operation, evidence_weight, created_at, payload_json
		FROM reality_backlog_plan WHERE proposal_id = ?`, proposalID).
		Scan(&operation, &weight, &created, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return BacklogPlan{}, false, nil
	}
	if err != nil {
		return BacklogPlan{}, false, fmt.Errorf("reality: read backlog plan: %w", err)
	}
	var payload backlogPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return BacklogPlan{}, false, fmt.Errorf("reality: decode backlog plan %s: %w", proposalID, err)
	}
	plan := BacklogPlan{
		ProposalID:   proposalID,
		Operation:    BacklogOperation(operation),
		Hypotheses:   payload.Hypotheses,
		SupersededBy: payload.SupersededBy,
		Finding:      payload.Finding,
		Observation:  payload.Observation,
		Fact:         payload.Fact,
		Reasoning:    payload.Reasoning,
		Evidence:     weight,
		By:           payload.By,
		State:        RulingOpen,
	}
	if plan.CreatedAt, err = parseTime(created); err != nil {
		return BacklogPlan{}, false, fmt.Errorf("reality: backlog plan %s: %w", proposalID, err)
	}
	if err := s.readBacklogRuling(ctx, &plan); err != nil {
		return BacklogPlan{}, false, err
	}
	return plan, true, nil
}

// readBacklogRuling attaches what the operator did with a plan, leaving it
// open when he has not ruled.
func (s *Store) readBacklogRuling(ctx context.Context, plan *BacklogPlan) error {
	var (
		verdict  string
		factID   sql.NullString
		actor    string
		recorded string
		encoded  []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT verdict, fact_id, actor, recorded_at, payload_json
		FROM reality_backlog_ruling WHERE proposal_id = ?`, plan.ProposalID).
		Scan(&verdict, &factID, &actor, &recorded, &encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("reality: read backlog ruling: %w", err)
	}
	var payload StatusPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return fmt.Errorf("reality: decode backlog ruling %s: %w", plan.ProposalID, err)
	}
	plan.State = RulingState(verdict)
	plan.RuledBy = actor
	plan.Reason = payload.Note
	plan.FactID = factID.String
	at, err := parseTime(recorded)
	if err != nil {
		return fmt.Errorf("reality: backlog ruling %s: %w", plan.ProposalID, err)
	}
	plan.RuledAt = at
	return nil
}

// DeclineBacklogPlan records that the operator refused what a backlog proposal
// would have done.
//
// The reason is kept verbatim and is required, for DeclineTopicPlan's reason:
// the next pass reads why an act was refused, and a refusal with no reason
// teaches it nothing. Suppression follows from the record.
func (s *Store) DeclineBacklogPlan(ctx context.Context, proposalID, operator, reason string) error {
	if operator == "" {
		return fmt.Errorf("%w: a decline has no operator", ErrInvalidValue)
	}
	if strings.TrimSpace(reason) == "" {
		return fmt.Errorf("%w: a declined backlog act keeps the operator's reason, and this one is empty",
			ErrInvalidValue)
	}
	plan, found, err := s.BacklogPlan(ctx, proposalID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: proposal %s carries no backlog plan", ErrUnknownRecord, proposalID)
	}
	if plan.State != RulingOpen {
		return fmt.Errorf("%w: the plan on proposal %s is already %s",
			ErrAlreadyDecided, proposalID, plan.State)
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		_, err := s.recordBacklogRuling(ctx, tx, proposalID, RulingDeclined, operator, reason, "")
		return err
	})
}

// ApplyBacklogPlan performs what the operator accepted: the candidates a
// consolidation folds are promoted, a superseded candidate is linked to the
// one that speaks for it and settled, a retired one is settled with the
// reason, and a promotion asserts the fact under his authority.
//
// The transaction boundary is ApplyTopicPlan's and for the same reason. The
// Reality Ledger and the hypothesis frontier are two components of one durable
// file reached through two handles, SQLite's write lock is per file, and every
// transaction begins IMMEDIATE — so a status event written from inside this
// transaction would block on the lock this transaction holds. The ledger's
// half commits first, and the frontier's follows through the injected writer.
//
// A frontier write that fails leaves the ruling durable and the candidates
// where they were, and the error names them. That is the direction to fail in:
// a candidate still deferred is the backlog this recipe already works, while
// the other ordering would settle candidates on an acceptance the ledger never
// recorded. The returned BacklogAcceptance is populated either way.
func (s *Store) ApplyBacklogPlan(ctx context.Context, proposalID, operator string,
	front BacklogFrontier) (BacklogAcceptance, error) {
	if operator == "" {
		return BacklogAcceptance{}, fmt.Errorf("%w: an acceptance has no operator", ErrInvalidValue)
	}
	plan, found, err := s.BacklogPlan(ctx, proposalID)
	if err != nil {
		return BacklogAcceptance{}, err
	}
	if !found {
		return BacklogAcceptance{}, fmt.Errorf("%w: proposal %s carries no backlog plan",
			ErrUnknownRecord, proposalID)
	}
	if plan.State != RulingOpen {
		return BacklogAcceptance{}, fmt.Errorf("%w: the plan on proposal %s is already %s",
			ErrAlreadyDecided, proposalID, plan.State)
	}
	if front == nil {
		// Refused rather than silently skipped: accepting the proposal is
		// what settles the candidates, and an acceptance that quietly
		// settled none would leave the operator believing it had.
		return BacklogAcceptance{}, fmt.Errorf(
			"%w: the plan on proposal %s settles %d candidates and no frontier was supplied",
			ErrInvalidValue, proposalID, len(plan.Hypotheses))
	}
	if err := s.checkBacklogApplicable(ctx, plan, front); err != nil {
		return BacklogAcceptance{}, err
	}

	acceptance := BacklogAcceptance{
		ProposalID: proposalID,
		Operation:  plan.Operation,
		Status:     plan.Settles(),
		Actor:      operator,
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		recorded := s.now()
		authority := Authority{Kind: AuthorityOperator, ID: operator, At: recorded}
		set := s.newRecordSet()
		if plan.Fact != nil {
			fact, err := s.promoteFact(ctx, tx, plan, authority, set)
			if err != nil {
				return err
			}
			acceptance.Fact, acceptance.FactID = fact, fact.ID
		}
		ruling, err := s.recordBacklogRuling(ctx, tx, proposalID, RulingApplied, operator,
			plan.Reasoning, acceptance.FactID)
		if err != nil {
			return err
		}
		acceptance.ID, acceptance.RecordedAt = ruling, recorded
		if acceptance.FactID == "" {
			// Nothing of the ledger's own travels: what a consolidation,
			// a supersession and a retirement produce is frontier
			// history, which publishes on its own terms.
			return nil
		}
		pub, err = s.stageSet(ctx, tx, acceptance.FactID, set)
		return err
	})
	if err != nil {
		return BacklogAcceptance{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return acceptance, err
	}
	for _, id := range plan.Hypotheses {
		if err := front.Settle(ctx, Settlement{
			HypothesisID: id,
			Status:       plan.Settles(),
			SupersededBy: plan.SupersededBy,
			Operator:     operator,
			Reason:       plan.Reasoning,
		}); err != nil {
			return acceptance, fmt.Errorf(
				"reality: the plan on proposal %s was applied; candidate %s is still deferred: %w",
				proposalID, id, err)
		}
		acceptance.Settled = append(acceptance.Settled, id)
	}
	return acceptance, nil
}

// promoteFact asserts the fact a promotion carries under the accepting
// operator's authority, which is §4.8's rule that only he attributes one.
func (s *Store) promoteFact(ctx context.Context, tx *sql.Tx, plan BacklogPlan,
	authority Authority, set *recordSet) (Fact, error) {
	input := *plan.Fact
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
	if err := input.validate(); err != nil {
		return Fact{}, err
	}
	fact, _, err := s.assertFact(ctx, tx, input, "", "", "")
	if err != nil {
		return Fact{}, err
	}
	if err := set.add(stagedFact(fact)); err != nil {
		return Fact{}, err
	}
	return fact, nil
}

// checkBacklogApplicable refuses a plan the frontier has moved past, and names
// the state.
//
// A plan is Babel's reading of the backlog at the moment it ran, and the
// operator may rule on it days later. Between the two a candidate can have
// been revived, promoted by another accepted plan, or superseded by a
// different one; settling it again would append a second ending nobody argued
// for. So each candidate is checked and the refusal says what it is now, which
// is what tells the operator the answer is to let Babel look again.
func (s *Store) checkBacklogApplicable(ctx context.Context, plan BacklogPlan,
	front BacklogFrontier) error {
	for _, id := range append(append([]string{}, plan.Hypotheses...), plan.SupersededBy) {
		if id == "" {
			continue
		}
		status, err := front.Status(ctx, id)
		if err != nil {
			return fmt.Errorf("reality: backlog candidate %s: %w", id, err)
		}
		settled := plan.settles(id)
		switch {
		case settled && status == frontier.StatusDeferred:
		case settled:
			return fmt.Errorf("%w: hypothesis %s was already %s", ErrConflict, id, status)
		case status.Replaced():
			return fmt.Errorf("%w: hypothesis %s was already %s", ErrConflict, id, status)
		}
	}
	return nil
}

// recordBacklogRuling appends what the operator decided about a plan. The
// unique index on proposal_id is what makes a double-click impossible.
func (s *Store) recordBacklogRuling(ctx context.Context, tx *sql.Tx, proposalID string,
	verdict RulingState, operator, note, factID string) (string, error) {
	id, err := newID("brl")
	if err != nil {
		return "", err
	}
	encoded, err := marshalPayload(StatusPayload{Note: note})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_backlog_ruling(
		id, proposal_id, verdict, fact_id, actor, recorded_at, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?)`,
		id, proposalID, string(verdict), nullableID(factID), operator,
		formatTime(s.now()), encoded); err != nil {
		return "", fmt.Errorf("reality: record backlog ruling: %w", err)
	}
	return id, nil
}
