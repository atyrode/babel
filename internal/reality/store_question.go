package reality

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Ask records a Reality Question.
//
// Deduplication and suppression are §4.8's, and they are two different things.
// A question equivalent to one that is still live is a duplicate and is
// refused as such — the operator already has it in the inbox. A question
// equivalent to one the operator declined, or answered `unknown`, is
// suppressed until materially new evidence exists: the ask has to carry a
// material-evidence key the suppressing question did not have. When it does,
// the old question is marked `superseded` and the new one links to it, so the
// refusal and the reason it was revisited stay connected.
//
// Equivalence is computed over canonical entities, so a question about an
// identity that was merged away dedupes against one about the identity it
// merged into.
func (s *Store) Ask(ctx context.Context, in QuestionInput) (Question, error) {
	if err := in.validate(); err != nil {
		return Question{}, err
	}
	var record Question
	err := s.transact(ctx, func(tx *sql.Tx) error {
		created, err := s.ask(ctx, tx, in, "asker")
		record = created
		return err
	})
	if err != nil {
		return Question{}, err
	}
	return record, nil
}

func (s *Store) ask(ctx context.Context, tx *sql.Tx, in QuestionInput, actor string) (Question, error) {
	canonical := make([]string, 0, len(in.TargetEntityIDs))
	for _, id := range in.TargetEntityIDs {
		resolved, err := resolve(ctx, tx, id)
		if err != nil {
			return Question{}, fmt.Errorf("reality: question target: %w", err)
		}
		canonical = append(canonical, resolved)
	}
	canonical = sortedUnique(canonical)
	key := dedupeKey(in.Kind, canonical, in.TargetPredicates)

	superseded, err := s.checkDuplicate(ctx, tx, key, in.MaterialEvidence)
	if err != nil {
		return Question{}, err
	}

	id, err := newID("qst")
	if err != nil {
		return Question{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Question{}, err
	}
	created := s.now()
	record := Question{
		ID:                id,
		SchemaVersion:     RecordSchema,
		Kind:              in.Kind,
		Class:             in.Class,
		Sensitivity:       in.Sensitivity,
		ExpectedAuthority: in.ExpectedAuthority,
		TargetEntityIDs:   canonical,
		TargetPredicates:  in.TargetPredicates,
		DependentWork:     in.DependentWork,
		ExistingFactIDs:   sortedUnique(in.ExistingFactIDs),
		ConflictFactIDs:   sortedUnique(in.ConflictFactIDs),
		DedupeKey:         key,
		MaterialEvidence:  sortedUnique(in.MaterialEvidence),
		AvoidedCost:       in.AvoidedCost,
		PromptedByID:      superseded,
		CreatedAt:         created,
		State:             QuestionOpen,
		Payload:           in.Payload,
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_question(
		id, schema_version, question_kind, question_class, sensitivity, expected_authority,
		dedupe_key, avoided_cost, prompted_by_id, created_at, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, RecordSchema, string(record.Kind), string(record.Class), string(record.Sensitivity),
		string(record.ExpectedAuthority), key, record.AvoidedCost, nullableID(superseded),
		formatTime(created), payload); err != nil {
		return Question{}, fmt.Errorf("reality: insert question: %w", err)
	}
	for _, entityID := range record.TargetEntityIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_question_entity(question_id, entity_id)
			VALUES(?, ?)`, record.ID, entityID); err != nil {
			return Question{}, fmt.Errorf("reality: link question target: %w", err)
		}
	}
	for _, predicate := range record.TargetPredicates {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_question_predicate(question_id, predicate)
			VALUES(?, ?)`, record.ID, string(predicate)); err != nil {
			return Question{}, fmt.Errorf("reality: link question predicate: %w", err)
		}
	}
	for _, group := range []struct {
		role FactRole
		ids  []string
	}{{RoleExisting, record.ExistingFactIDs}, {RoleConflicting, record.ConflictFactIDs}} {
		for _, factID := range group.ids {
			if err := requireRow(ctx, tx, "reality_fact", "id", factID); err != nil {
				return Question{}, fmt.Errorf("reality: question fact: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO reality_question_fact(
				question_id, fact_id, fact_role) VALUES(?, ?, ?)`,
				record.ID, factID, string(group.role)); err != nil {
				return Question{}, fmt.Errorf("reality: link question fact: %w", err)
			}
		}
	}
	for _, work := range record.DependentWork {
		blocking := 0
		if work.Blocking {
			blocking = 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_question_work(
			question_id, work_kind, work_id, blocking) VALUES(?, ?, ?, ?)`,
			record.ID, string(work.Kind), work.ID, blocking); err != nil {
			return Question{}, fmt.Errorf("reality: link dependent work: %w", err)
		}
	}
	for _, item := range record.MaterialEvidence {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_question_evidence(question_id, item)
			VALUES(?, ?)`, record.ID, item); err != nil {
			return Question{}, fmt.Errorf("reality: link question evidence: %w", err)
		}
	}
	if err := s.appendQuestionState(ctx, tx, record.ID, QuestionOpen, actor, ""); err != nil {
		return Question{}, err
	}
	if superseded != "" {
		if err := s.transitionQuestion(ctx, tx, superseded, QuestionSuperseded, actor,
			"materially new evidence produced question "+record.ID); err != nil {
			return Question{}, err
		}
	}
	return record, nil
}

// checkDuplicate applies §4.8's deduplication and suppression, returning the
// question a materially-new-evidence re-ask supersedes.
func (s *Store) checkDuplicate(ctx context.Context, tx *sql.Tx, key string, evidence []string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT q.id,
		(SELECT e.state FROM reality_question_event e WHERE e.question_id = q.id ORDER BY e.seq DESC LIMIT 1),
		EXISTS(SELECT 1 FROM reality_answer a WHERE a.question_id = q.id AND a.outcome IN (?, ?))
		FROM reality_question q WHERE q.dedupe_key = ? ORDER BY q.created_at, q.id`,
		string(OutcomeUnknown), string(OutcomeDeclined), key)
	if err != nil {
		return "", fmt.Errorf("reality: check duplicate question: %w", err)
	}
	type candidate struct {
		id         string
		state      QuestionState
		suppressed bool
	}
	var existing []candidate
	for rows.Next() {
		var (
			id             string
			state          string
			hasSuppressing bool
		)
		if err := rows.Scan(&id, &state, &hasSuppressing); err != nil {
			rows.Close()
			return "", fmt.Errorf("reality: check duplicate question: %w", err)
		}
		existing = append(existing, candidate{id: id, state: QuestionState(state), suppressed: hasSuppressing})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", fmt.Errorf("reality: check duplicate question: %w", err)
	}
	rows.Close()

	var supersede string
	for _, prior := range existing {
		if prior.state.live() {
			return "", fmt.Errorf("%w: question %q", ErrDuplicateQuestion, prior.id)
		}
		if !prior.state.suppresses() && !prior.suppressed {
			continue
		}
		known, err := queryStrings(ctx, tx,
			`SELECT item FROM reality_question_evidence WHERE question_id = ?`, prior.id)
		if err != nil {
			return "", err
		}
		if !materiallyNew(evidence, known) {
			return "", fmt.Errorf("%w: question %q was refused and this ask offers no new evidence",
				ErrSuppressed, prior.id)
		}
		// The most recent suppressing question is the one the re-ask
		// supersedes; earlier ones are already superseded by it.
		supersede = prior.id
	}
	return supersede, nil
}

// materiallyNew reports whether an ask offers evidence a suppressing question
// did not have.
//
// An ask with no evidence at all is never materially new: §4.8's suppression
// exists so a refusal is not re-litigated by repetition, and repetition is
// exactly an ask that says nothing more than the last one did.
func materiallyNew(offered, known []string) bool {
	if len(offered) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(known))
	for _, item := range known {
		seen[item] = struct{}{}
	}
	for _, item := range offered {
		if _, ok := seen[item]; !ok {
			return true
		}
	}
	return false
}

func (s *Store) appendQuestionState(ctx context.Context, tx *sql.Tx, questionID string,
	state QuestionState, actor, note string) error {
	if !state.valid() {
		return fmt.Errorf("%w: question state %q", ErrInvalidValue, state)
	}
	seq, err := nextSeq(ctx, tx, "reality_question_event", "question_id", questionID)
	if err != nil {
		return err
	}
	id, err := newID("qse")
	if err != nil {
		return err
	}
	payload, err := marshalPayload(StatusPayload{Note: note})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_question_event(
		id, question_id, seq, state, actor, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		id, questionID, seq, string(state), actor, formatTime(s.now()), payload); err != nil {
		return fmt.Errorf("reality: append question state: %w", err)
	}
	return nil
}

// transitionQuestion appends a state event after checking §4.8's machine.
func (s *Store) transitionQuestion(ctx context.Context, tx *sql.Tx, questionID string,
	to QuestionState, actor, note string) error {
	from, err := questionState(ctx, tx, questionID)
	if err != nil {
		return err
	}
	if !canTransition(from, to) {
		return fmt.Errorf("%w: question %q is %s and cannot become %s",
			ErrInvalidTransition, questionID, from, to)
	}
	return s.appendQuestionState(ctx, tx, questionID, to, actor, note)
}

func questionState(ctx context.Context, q querier, questionID string) (QuestionState, error) {
	var state string
	err := q.QueryRowContext(ctx, `SELECT state FROM reality_question_event
		WHERE question_id = ? ORDER BY seq DESC LIMIT 1`, questionID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: question %q", ErrUnknownRecord, questionID)
	}
	if err != nil {
		return "", fmt.Errorf("reality: read question %q state: %w", questionID, err)
	}
	return QuestionState(state), nil
}

// SetQuestionState records an operator decision about a question.
func (s *Store) SetQuestionState(ctx context.Context, in QuestionStateInput) error {
	if in.Actor == "" {
		return fmt.Errorf("%w: question state change has no actor", ErrInvalidValue)
	}
	if err := checkNoCredential("question state note", in.Note); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		return s.transitionQuestion(ctx, tx, in.QuestionID, in.State, in.Actor, in.Note)
	})
}

// Question reads one question with its targets, links and current state.
func (s *Store) Question(ctx context.Context, id string) (Question, error) {
	return readQuestion(ctx, s.db, id)
}

func readQuestion(ctx context.Context, q querier, id string) (Question, error) {
	var (
		record   Question
		kind     string
		class    string
		sens     string
		expected string
		prompted sql.NullString
		created  string
		payload  []byte
		state    sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT q.id, q.schema_version, q.question_kind, q.question_class,
		q.sensitivity, q.expected_authority, q.dedupe_key, q.avoided_cost, q.prompted_by_id,
		q.created_at, q.payload_json,
		(SELECT e.state FROM reality_question_event e WHERE e.question_id = q.id ORDER BY e.seq DESC LIMIT 1)
		FROM reality_question q WHERE q.id = ?`, id).
		Scan(&record.ID, &record.SchemaVersion, &kind, &class, &sens, &expected, &record.DedupeKey,
			&record.AvoidedCost, &prompted, &created, &payload, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return Question{}, fmt.Errorf("%w: question %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Question{}, fmt.Errorf("reality: read question %q: %w", id, err)
	}
	record.Kind = QuestionKind(kind)
	record.Class = QuestionClass(class)
	record.Sensitivity = Sensitivity(sens)
	record.ExpectedAuthority = AuthorityKind(expected)
	record.PromptedByID = prompted.String
	record.State = QuestionState(state.String)
	if record.CreatedAt, err = parseTime(created); err != nil {
		return Question{}, fmt.Errorf("reality: question %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Question{}, fmt.Errorf("reality: decode question %s payload: %w", id, err)
	}
	if record.TargetEntityIDs, err = queryStrings(ctx, q,
		`SELECT entity_id FROM reality_question_entity WHERE question_id = ? ORDER BY entity_id`,
		id); err != nil {
		return Question{}, err
	}
	predicates, err := queryStrings(ctx, q,
		`SELECT predicate FROM reality_question_predicate WHERE question_id = ? ORDER BY predicate`, id)
	if err != nil {
		return Question{}, err
	}
	for _, name := range predicates {
		record.TargetPredicates = append(record.TargetPredicates, Predicate(name))
	}
	if record.ExistingFactIDs, err = queryStrings(ctx, q,
		`SELECT fact_id FROM reality_question_fact WHERE question_id = ? AND fact_role = ? ORDER BY fact_id`,
		id, string(RoleExisting)); err != nil {
		return Question{}, err
	}
	if record.ConflictFactIDs, err = queryStrings(ctx, q,
		`SELECT fact_id FROM reality_question_fact WHERE question_id = ? AND fact_role = ? ORDER BY fact_id`,
		id, string(RoleConflicting)); err != nil {
		return Question{}, err
	}
	if record.MaterialEvidence, err = queryStrings(ctx, q,
		`SELECT item FROM reality_question_evidence WHERE question_id = ? ORDER BY item`, id); err != nil {
		return Question{}, err
	}
	work, err := q.QueryContext(ctx, `SELECT work_kind, work_id, blocking FROM reality_question_work
		WHERE question_id = ? ORDER BY work_kind, work_id`, id)
	if err != nil {
		return Question{}, fmt.Errorf("reality: read dependent work: %w", err)
	}
	defer work.Close()
	for work.Next() {
		var (
			ref      WorkRef
			kindName string
			blocking int
		)
		if err := work.Scan(&kindName, &ref.ID, &blocking); err != nil {
			return Question{}, fmt.Errorf("reality: read dependent work: %w", err)
		}
		ref.Kind = WorkKind(kindName)
		ref.Blocking = blocking == 1
		record.DependentWork = append(record.DependentWork, ref)
	}
	if err := work.Err(); err != nil {
		return Question{}, fmt.Errorf("reality: read dependent work: %w", err)
	}
	return record, nil
}

// QuestionHistory reads a question's append-only state history.
func (s *Store) QuestionHistory(ctx context.Context, questionID string) ([]QuestionEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, question_id, seq, state, actor, recorded_at, payload_json
		FROM reality_question_event WHERE question_id = ? ORDER BY seq`, questionID)
	if err != nil {
		return nil, fmt.Errorf("reality: read question history: %w", err)
	}
	defer rows.Close()
	var out []QuestionEvent
	for rows.Next() {
		var (
			record   QuestionEvent
			state    string
			recorded string
			payload  []byte
		)
		if err := rows.Scan(&record.ID, &record.QuestionID, &record.Sequence, &state,
			&record.Actor, &recorded, &payload); err != nil {
			return nil, fmt.Errorf("reality: read question history: %w", err)
		}
		record.State = QuestionState(state)
		if record.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, fmt.Errorf("reality: question event %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("reality: decode question event %s payload: %w", record.ID, err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// RecordAnswer retains an answer verbatim.
//
// The text is stored exactly as supplied — §4.8 requires verbatim retention,
// so nothing here trims, normalizes, or renders it — and the row is immutable.
// The outcome decides the question's next state: a substantive answer becomes
// `answered-uninterpreted` and waits for the interpreter, `unknown` disposes of
// the question with nothing to interpret, and `declined` declines it. The last
// two suppress equivalent re-asks.
func (s *Store) RecordAnswer(ctx context.Context, in AnswerInput) (Answer, error) {
	if err := in.validate(); err != nil {
		return Answer{}, err
	}
	var record Answer
	err := s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireContext(ctx, tx, in.ContextID); err != nil {
			return fmt.Errorf("reality: answer context: %w", err)
		}
		seq, err := nextSeq(ctx, tx, "reality_answer", "question_id", in.QuestionID)
		if err != nil {
			return err
		}
		id, err := newID("ans")
		if err != nil {
			return err
		}
		payload, err := marshalPayload(AnswerPayload{Text: in.Text})
		if err != nil {
			return err
		}
		recorded := s.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_answer(
			id, question_id, schema_version, seq, author, answered_at, recorded_at, outcome,
			context_id, payload_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, in.QuestionID, RecordSchema, seq, in.Author, formatTime(in.At), formatTime(recorded),
			string(in.Outcome), nullableID(in.ContextID), payload); err != nil {
			return fmt.Errorf("reality: insert answer: %w", err)
		}
		if err := s.transitionQuestion(ctx, tx, in.QuestionID, in.Outcome.nextState(),
			in.Author, "answer "+id); err != nil {
			return err
		}
		record = Answer{
			ID:            id,
			QuestionID:    in.QuestionID,
			SchemaVersion: RecordSchema,
			Sequence:      seq,
			Author:        in.Author,
			At:            in.At.UTC(),
			RecordedAt:    recorded,
			Outcome:       in.Outcome,
			ContextID:     in.ContextID,
			Payload:       AnswerPayload{Text: in.Text},
		}
		return nil
	})
	if err != nil {
		return Answer{}, err
	}
	return record, nil
}

// Answers reads a question's answers in the order they were given.
func (s *Store) Answers(ctx context.Context, questionID string) ([]Answer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, question_id, schema_version, seq, author,
		answered_at, recorded_at, outcome, context_id, payload_json
		FROM reality_answer WHERE question_id = ? ORDER BY seq`, questionID)
	if err != nil {
		return nil, fmt.Errorf("reality: read answers: %w", err)
	}
	defer rows.Close()
	var out []Answer
	for rows.Next() {
		var (
			record    Answer
			at        string
			recorded  string
			outcome   string
			contextID sql.NullString
			payload   []byte
		)
		if err := rows.Scan(&record.ID, &record.QuestionID, &record.SchemaVersion, &record.Sequence,
			&record.Author, &at, &recorded, &outcome, &contextID, &payload); err != nil {
			return nil, fmt.Errorf("reality: read answers: %w", err)
		}
		record.Outcome = AnswerOutcome(outcome)
		record.ContextID = contextID.String
		if record.At, err = parseTime(at); err != nil {
			return nil, fmt.Errorf("reality: answer %s: %w", record.ID, err)
		}
		if record.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, fmt.Errorf("reality: answer %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("reality: decode answer %s payload: %w", record.ID, err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// BeginInterpretation moves a retained answer into the Answer Interpreter.
// It is a separate step so that a crash during interpretation is visible as
// `interpreting` rather than indistinguishable from an answer nobody sent.
func (s *Store) BeginInterpretation(ctx context.Context, questionID string) error {
	return s.transact(ctx, func(tx *sql.Tx) error {
		return s.transitionQuestion(ctx, tx, questionID, QuestionInterpreting,
			component, "sent to the answer interpreter")
	})
}

// FailInterpretation returns a question to `answered-uninterpreted` for retry,
// which is what §4.8 requires when interpretation is unavailable or fails: the
// raw answer is still good, so it is kept and the interpretation is retried.
func (s *Store) FailInterpretation(ctx context.Context, questionID, reason string) error {
	if err := checkNoCredential("interpretation failure reason", reason); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		return s.transitionQuestion(ctx, tx, questionID, QuestionAnsweredUninterpreted,
			component, reason)
	})
}

// RecordPlan records an interpretation and retains its non-authoritative
// descendants.
//
// The split is §4.8's, and it is the reason this operation exists at all.
// Hypotheses, follow-up questions, and pipeline requests are retained
// immediately: none of them asserts anything about reality, and holding them
// hostage to an acceptance would lose ideas the interpretation produced. Every
// fact, entity-resolution, and focus-policy action is stored as
// `pending-acceptance` and changes nothing until AcceptPlan.
//
// A plan may not attribute its own facts. §4.8's rule that agent interpretation
// never silently becomes authoritative reality means the authority behind an
// applied fact is the operator who accepted it, so an action arriving with an
// authority already filled in is refused rather than trusted.
//
// Hypothesis retention happens before the transaction opens, because the
// frontier is a separate component with its own connection to the same file and
// this transaction would hold the write lock against it. Cross-component
// atomicity is therefore not claimed, exactly as §9 says of multi-store
// commits: a candidate the frontier accepted before a failed plan write remains
// a persisted candidate, which is what §5.2 wants anyway.
func (s *Store) RecordPlan(ctx context.Context, in PlanInput) (Plan, Retained, error) {
	if in.QuestionID == "" || in.AnswerID == "" {
		return Plan{}, Retained{}, fmt.Errorf("%w: a plan names both a question and an answer", ErrInvalidValue)
	}
	if in.InterpreterVersion <= 0 {
		return Plan{}, Retained{}, fmt.Errorf("%w: interpreter version must be positive", ErrInvalidValue)
	}
	if len(in.Actions) != len(in.Kinds) {
		return Plan{}, Retained{}, fmt.Errorf("%w: %d actions and %d kinds",
			ErrInvalidValue, len(in.Actions), len(in.Kinds))
	}
	if len(in.Actions) == 0 {
		return Plan{}, Retained{}, fmt.Errorf("%w: a plan has at least one action, which may be no-action",
			ErrInvalidValue)
	}
	if err := checkNoCredential("plan summary", in.Summary); err != nil {
		return Plan{}, Retained{}, err
	}
	for i, kind := range in.Kinds {
		if err := validateAction(kind, in.Actions[i]); err != nil {
			return Plan{}, Retained{}, fmt.Errorf("reality: plan action %d: %w", i, err)
		}
		if kind == ActionCreateHypothesis && s.sink == nil {
			return Plan{}, Retained{}, ErrNoHypothesisSink
		}
	}

	// Retain hypotheses first, outside the transaction, then record what
	// came back.
	hypothesisIDs := make(map[int]string, len(in.Kinds))
	var retained Retained
	for i, kind := range in.Kinds {
		if kind != ActionCreateHypothesis {
			continue
		}
		id, err := s.sink.RecordHypothesis(ctx, *in.Actions[i].Hypothesis)
		if err != nil {
			return Plan{}, Retained{}, fmt.Errorf("reality: retain plan hypothesis: %w", err)
		}
		hypothesisIDs[i] = id
		retained.HypothesisIDs = append(retained.HypothesisIDs, id)
	}

	var plan Plan
	err := s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRow(ctx, tx, "reality_question", "id", in.QuestionID); err != nil {
			return err
		}
		var answerQuestion, answerOutcome string
		if err := tx.QueryRowContext(ctx, `SELECT question_id, outcome FROM reality_answer WHERE id = ?`,
			in.AnswerID).Scan(&answerQuestion, &answerOutcome); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: answer %q", ErrUnknownRecord, in.AnswerID)
			}
			return fmt.Errorf("reality: read plan answer: %w", err)
		}
		if answerQuestion != in.QuestionID {
			return fmt.Errorf("%w: answer %q belongs to question %q",
				ErrInvalidValue, in.AnswerID, answerQuestion)
		}
		if AnswerOutcome(answerOutcome) != OutcomeAnswered {
			return fmt.Errorf("%w: answer %q is %s and has nothing to interpret",
				ErrInvalidValue, in.AnswerID, answerOutcome)
		}

		id, err := newID("pln")
		if err != nil {
			return err
		}
		payload, err := marshalPayload(PlanPayload{Summary: in.Summary})
		if err != nil {
			return err
		}
		created := s.now()
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_plan(
			id, question_id, answer_id, schema_version, interpreter_version, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			id, in.QuestionID, in.AnswerID, RecordSchema, in.InterpreterVersion,
			formatTime(created), payload); err != nil {
			return fmt.Errorf("reality: insert plan: %w", err)
		}
		plan = Plan{
			ID:                 id,
			QuestionID:         in.QuestionID,
			AnswerID:           in.AnswerID,
			SchemaVersion:      RecordSchema,
			InterpreterVersion: in.InterpreterVersion,
			CreatedAt:          created,
			State:              PlanProposed,
			Payload:            PlanPayload{Summary: in.Summary},
		}
		for position, kind := range in.Kinds {
			actionID, err := newID("act")
			if err != nil {
				return err
			}
			action := Action{
				ID:       actionID,
				PlanID:   id,
				Position: position,
				Kind:     kind,
				State:    ActionPendingAcceptance,
				Payload:  in.Actions[position],
			}
			if !kind.RequiresAcceptance() {
				action.State = ActionRetained
				action.AppliedAt = created
			}
			// The action row is written first because a recorded
			// request references it: a request that pointed at no
			// action would be a durable row nobody can trace back to
			// the interpretation that produced it.
			if err := s.insertAction(ctx, tx, action); err != nil {
				return err
			}
			switch kind {
			case ActionCreateHypothesis:
				action.ResultID = hypothesisIDs[position]
			case ActionAskFollowUp:
				followUp, err := s.ask(ctx, tx, *action.Payload.FollowUp, component)
				if err != nil {
					return fmt.Errorf("reality: retain follow-up question: %w", err)
				}
				action.ResultID = followUp.ID
				retained.QuestionIDs = append(retained.QuestionIDs, followUp.ID)
			case ActionRequestInvestigation, ActionRequestRefinement:
				request, err := s.recordRequest(ctx, tx, action.ID, *action.Payload.Request)
				if err != nil {
					return err
				}
				action.ResultID = request.ID
				retained.RequestIDs = append(retained.RequestIDs, request.ID)
			}
			if action.ResultID != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE reality_plan_action
					SET result_id = ? WHERE id = ?`, action.ResultID, action.ID); err != nil {
					return fmt.Errorf("reality: link plan action result: %w", err)
				}
			}
			plan.Actions = append(plan.Actions, action)
		}
		return s.transitionQuestion(ctx, tx, in.QuestionID, QuestionPlanReady,
			component, "plan "+id+" is ready for review")
	})
	if err != nil {
		return Plan{}, Retained{}, err
	}
	return plan, retained, nil
}

// validateAction checks that an action's payload matches its kind and carries
// nothing the kind does not permit.
//
// The per-kind check is what keeps the closed vocabulary meaningful: without
// it, an ask-follow-up action carrying a fact would be storable, and the
// distinction between what waits for acceptance and what does not would be
// decorative.
func validateAction(kind ActionKind, payload ActionPayload) error {
	if !kind.valid() {
		return fmt.Errorf("%w: action kind %q", ErrInvalidValue, kind)
	}
	if payload.Rationale == "" {
		return fmt.Errorf("%w: action states no rationale", ErrInvalidValue)
	}
	if err := checkNoCredential("action rationale", payload.Rationale); err != nil {
		return err
	}
	present := map[string]bool{
		"fact":        payload.Fact != nil,
		"dispute":     len(payload.DisputeFactIDs) > 0,
		"merge":       payload.Merge != nil,
		"split":       payload.Split != nil,
		"focus_rules": payload.FocusRules != nil,
		"hypothesis":  payload.Hypothesis != nil,
		"follow_up":   payload.FollowUp != nil,
		"request":     payload.Request != nil,
	}
	expected := map[ActionKind]string{
		ActionAssertFact:           "fact",
		ActionSupersedeFact:        "fact",
		ActionDisputeFact:          "dispute",
		ActionMergeEntities:        "merge",
		ActionSplitEntity:          "split",
		ActionChangeFocus:          "focus_rules",
		ActionCreateHypothesis:     "hypothesis",
		ActionAskFollowUp:          "follow_up",
		ActionRequestInvestigation: "request",
		ActionRequestRefinement:    "request",
		ActionNone:                 "",
	}[kind]
	for field, filled := range present {
		if filled && field != expected {
			return fmt.Errorf("%w: a %s action must not carry %s", ErrInvalidValue, kind, field)
		}
	}
	if expected != "" && !present[expected] {
		return fmt.Errorf("%w: a %s action needs %s", ErrInvalidValue, kind, expected)
	}

	switch kind {
	case ActionAssertFact, ActionSupersedeFact:
		if payload.Fact.Authority.Kind != "" {
			return fmt.Errorf("%w: an interpretation may not attribute a fact; the accepting operator does",
				ErrNotAuthoritative)
		}
		if err := payload.Fact.validateProposed(); err != nil {
			return err
		}
		if kind == ActionSupersedeFact && payload.PriorFactID == "" {
			return fmt.Errorf("%w: a supersede-fact action names no prior fact", ErrInvalidValue)
		}
		if kind == ActionAssertFact && payload.PriorFactID != "" {
			return fmt.Errorf("%w: an assert-fact action must not name a prior fact", ErrInvalidValue)
		}
	case ActionDisputeFact:
		if len(payload.DisputeFactIDs) < 2 {
			return fmt.Errorf("%w: a dispute-fact action needs at least two facts", ErrInvalidValue)
		}
	case ActionChangeFocus:
		if err := payload.FocusRules.validate(); err != nil {
			return err
		}
	case ActionCreateHypothesis:
		if payload.Hypothesis.Statement == "" || payload.Hypothesis.RunID == "" {
			return fmt.Errorf("%w: a hypothesis needs a run and a statement", ErrInvalidValue)
		}
		if err := checkNoCredential("hypothesis statement", payload.Hypothesis.Statement); err != nil {
			return err
		}
	case ActionAskFollowUp:
		if err := payload.FollowUp.validate(); err != nil {
			return err
		}
	case ActionRequestInvestigation, ActionRequestRefinement:
		request := payload.Request
		if !request.Kind.valid() {
			return fmt.Errorf("%w: request kind %q", ErrInvalidValue, request.Kind)
		}
		if (kind == ActionRequestInvestigation) != (request.Kind == RequestInvestigation) {
			return fmt.Errorf("%w: a %s action carries a %s request", ErrInvalidValue, kind, request.Kind)
		}
		if request.Guidance == "" {
			return fmt.Errorf("%w: a request states no guidance", ErrInvalidValue)
		}
		if err := checkNoCredential("request guidance", request.Guidance); err != nil {
			return err
		}
		if request.Kind == RequestInvestigation && request.HypothesisID == "" {
			// This is §4.2's mandatory path, enforced: an investigation
			// that starts from no hypothesis is the bypass §4.8 says
			// the interpreter can never perform.
			return fmt.Errorf("%w: an investigation request must name the hypothesis it starts from",
				ErrInvalidValue)
		}
		if request.Kind == RequestRefinement && (request.SubjectID == "" || !request.SubjectKind.valid()) {
			return fmt.Errorf("%w: a refinement request must name the work to refine", ErrInvalidValue)
		}
	}
	return nil
}

// validateProposed checks a plan's proposed fact without an authority, which
// the accepting operator supplies.
func (in FactInput) validateProposed() error {
	in.Authority = Authority{Kind: AuthorityOperator, ID: "pending-acceptance", At: in.ObservedAt}
	if in.Authority.At.IsZero() {
		in.Authority.At = time.Unix(0, 0).UTC()
	}
	return in.validate()
}

func (s *Store) insertAction(ctx context.Context, tx *sql.Tx, action Action) error {
	payload, err := marshalPayload(action.Payload)
	if err != nil {
		return err
	}
	var applied any
	if !action.AppliedAt.IsZero() {
		applied = formatTime(action.AppliedAt)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_plan_action(
		id, plan_id, position, action_kind, state, result_id, applied_at, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		action.ID, action.PlanID, action.Position, string(action.Kind), string(action.State),
		nullableID(action.ResultID), applied, payload); err != nil {
		return fmt.Errorf("reality: insert plan action: %w", err)
	}
	return nil
}

func (s *Store) recordRequest(ctx context.Context, tx *sql.Tx, actionID string, draft RequestDraft) (Request, error) {
	id, err := newID("req")
	if err != nil {
		return Request{}, err
	}
	payload, err := marshalPayload(RequestPayload{Guidance: draft.Guidance})
	if err != nil {
		return Request{}, err
	}
	created := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_request(
		id, action_id, request_kind, hypothesis_id, subject_kind, subject_id, created_at, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		id, actionID, string(draft.Kind), nullableID(draft.HypothesisID),
		nullableID(string(draft.SubjectKind)), nullableID(draft.SubjectID),
		formatTime(created), payload); err != nil {
		return Request{}, fmt.Errorf("reality: record request: %w", err)
	}
	return Request{
		ID:           id,
		ActionID:     actionID,
		Kind:         draft.Kind,
		HypothesisID: draft.HypothesisID,
		SubjectKind:  draft.SubjectKind,
		SubjectID:    draft.SubjectID,
		CreatedAt:    created,
		Payload:      RequestPayload{Guidance: draft.Guidance},
	}, nil
}

// Plan reads one interpretation with its actions and current disposition.
func (s *Store) Plan(ctx context.Context, id string) (Plan, error) {
	return readPlan(ctx, s.db, id)
}

func readPlan(ctx context.Context, q querier, id string) (Plan, error) {
	var (
		record  Plan
		created string
		payload []byte
	)
	err := q.QueryRowContext(ctx, `SELECT id, question_id, answer_id, schema_version,
		interpreter_version, created_at, payload_json FROM reality_plan WHERE id = ?`, id).
		Scan(&record.ID, &record.QuestionID, &record.AnswerID, &record.SchemaVersion,
			&record.InterpreterVersion, &created, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, fmt.Errorf("%w: plan %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Plan{}, fmt.Errorf("reality: read plan %q: %w", id, err)
	}
	if record.CreatedAt, err = parseTime(created); err != nil {
		return Plan{}, fmt.Errorf("reality: plan %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Plan{}, fmt.Errorf("reality: decode plan %s payload: %w", id, err)
	}
	record.State, err = planState(ctx, q, id)
	if err != nil {
		return Plan{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id, plan_id, position, action_kind, state,
		result_id, applied_at, payload_json FROM reality_plan_action
		WHERE plan_id = ? ORDER BY position`, id)
	if err != nil {
		return Plan{}, fmt.Errorf("reality: read plan actions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			action    Action
			kind      string
			state     string
			resultID  sql.NullString
			appliedAt sql.NullString
			actionRaw []byte
		)
		if err := rows.Scan(&action.ID, &action.PlanID, &action.Position, &kind, &state,
			&resultID, &appliedAt, &actionRaw); err != nil {
			return Plan{}, fmt.Errorf("reality: read plan actions: %w", err)
		}
		action.Kind = ActionKind(kind)
		action.State = ActionState(state)
		action.ResultID = resultID.String
		if appliedAt.Valid {
			if action.AppliedAt, err = parseTime(appliedAt.String); err != nil {
				return Plan{}, fmt.Errorf("reality: plan action %s: %w", action.ID, err)
			}
		}
		if err := json.Unmarshal(actionRaw, &action.Payload); err != nil {
			return Plan{}, fmt.Errorf("reality: decode plan action %s payload: %w", action.ID, err)
		}
		record.Actions = append(record.Actions, action)
	}
	return record, rows.Err()
}

func planState(ctx context.Context, q querier, planID string) (PlanState, error) {
	var accepted, rejected int
	if err := q.QueryRowContext(ctx, `SELECT
		EXISTS(SELECT 1 FROM reality_plan_acceptance WHERE plan_id = ?),
		EXISTS(SELECT 1 FROM reality_plan_rejection WHERE plan_id = ?)`,
		planID, planID).Scan(&accepted, &rejected); err != nil {
		return "", fmt.Errorf("reality: read plan %q disposition: %w", planID, err)
	}
	switch {
	case accepted == 1:
		return PlanAccepted, nil
	case rejected == 1:
		return PlanRejected, nil
	}
	return PlanProposed, nil
}

// AcceptPlan applies a plan's authoritative actions on one explicit operator
// acceptance, atomically with the question's disposition.
//
// This is the operation §4.8 is most specific about, and every part of it is
// load-bearing.
//
// There is exactly one acceptance. The database's unique index on plan_id
// enforces it, so a double-click cannot apply a plan twice and a rejected plan
// cannot later be accepted.
//
// The accepting operator is the authority. Each applied fact is attributed to
// the actor and the acceptance instant, whatever the interpretation proposed,
// because §4.8 forbids agent interpretation from silently becoming
// authoritative reality.
//
// The acceptance, the mutations, and the question's disposition are one
// transaction. A failure between them would leave either facts nobody accepted
// or an accepted plan that changed nothing, and both are worse than the
// operation not having happened.
func (s *Store) AcceptPlan(ctx context.Context, in AcceptanceInput) (Acceptance, Application, error) {
	if in.PlanID == "" {
		return Acceptance{}, Application{}, fmt.Errorf("%w: acceptance names no plan", ErrInvalidValue)
	}
	if in.Actor == "" {
		return Acceptance{}, Application{}, fmt.Errorf("%w: acceptance has no actor", ErrInvalidValue)
	}
	if err := checkNoCredential("acceptance note", in.Note); err != nil {
		return Acceptance{}, Application{}, err
	}
	var (
		acceptance  Acceptance
		application Application
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		plan, err := readPlan(ctx, tx, in.PlanID)
		if err != nil {
			return err
		}
		if plan.State != PlanProposed {
			return fmt.Errorf("%w: plan %q is %s", ErrAlreadyDecided, in.PlanID, plan.State)
		}
		state, err := questionState(ctx, tx, plan.QuestionID)
		if err != nil {
			return err
		}
		if state != QuestionPlanReady {
			return fmt.Errorf("%w: question %q is %s and has no plan awaiting acceptance",
				ErrInvalidTransition, plan.QuestionID, state)
		}
		if err := requireContext(ctx, tx, in.ContextID); err != nil {
			return fmt.Errorf("reality: acceptance context: %w", err)
		}

		id, err := newID("acc")
		if err != nil {
			return err
		}
		recorded := s.now()
		payload := StatusPayload{Note: in.Note}
		encoded, err := marshalPayload(payload)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_plan_acceptance(
			id, plan_id, actor, context_id, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
			id, in.PlanID, in.Actor, nullableID(in.ContextID), formatTime(recorded), encoded); err != nil {
			return fmt.Errorf("reality: record acceptance: %w", err)
		}
		acceptance = Acceptance{
			ID:         id,
			PlanID:     in.PlanID,
			Actor:      in.Actor,
			ContextID:  in.ContextID,
			RecordedAt: recorded,
			Payload:    payload,
		}

		authority := Authority{Kind: AuthorityOperator, ID: in.Actor, At: recorded}
		for _, action := range plan.Actions {
			if !action.Kind.RequiresAcceptance() {
				continue
			}
			resultID, err := s.applyAction(ctx, tx, action, authority, in.ContextID, &application)
			if err != nil {
				return fmt.Errorf("reality: apply plan action %d: %w", action.Position, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE reality_plan_action
				SET state = ?, result_id = ?, applied_at = ? WHERE id = ?`,
				string(ActionApplied), nullableID(resultID), formatTime(recorded), action.ID); err != nil {
				return fmt.Errorf("reality: mark plan action applied: %w", err)
			}
		}

		if s.faultBeforeDisposition != nil {
			if err := s.faultBeforeDisposition(); err != nil {
				return fmt.Errorf("reality: accept plan: %w", err)
			}
		}

		if err := s.transitionQuestion(ctx, tx, plan.QuestionID, QuestionAnswered,
			in.Actor, "plan "+in.PlanID+" accepted"); err != nil {
			return err
		}
		application.QuestionState = QuestionAnswered
		return nil
	})
	if err != nil {
		return Acceptance{}, Application{}, err
	}
	return acceptance, application, nil
}

// applyAction performs one authoritative action under the accepting operator's
// authority.
func (s *Store) applyAction(ctx context.Context, tx *sql.Tx, action Action,
	authority Authority, contextID string, application *Application) (string, error) {
	switch action.Kind {
	case ActionAssertFact:
		input := *action.Payload.Fact
		input.Authority = authority
		if input.ContextID == "" {
			input.ContextID = contextID
		}
		fact, dispute, err := s.assertFact(ctx, tx, input, "", "", "")
		if err != nil {
			return "", err
		}
		application.FactIDs = append(application.FactIDs, fact.ID)
		if dispute.ID != "" {
			application.DisputeIDs = append(application.DisputeIDs, dispute.ID)
		}
		return fact.ID, nil
	case ActionSupersedeFact:
		input := *action.Payload.Fact
		input.Authority = authority
		if input.ContextID == "" {
			input.ContextID = contextID
		}
		fact, err := s.supersedeFact(ctx, tx,
			SupersedeInput{PriorID: action.Payload.PriorFactID, Fact: input}, "", "")
		if err != nil {
			return "", err
		}
		application.FactIDs = append(application.FactIDs, fact.ID)
		return fact.ID, nil
	case ActionDisputeFact:
		first, err := readFact(ctx, tx, action.Payload.DisputeFactIDs[0])
		if err != nil {
			return "", err
		}
		dispute, err := s.openDispute(ctx, tx, first.SubjectID, first.Predicate,
			sortedUnique(action.Payload.DisputeFactIDs), authority.ID, action.Payload.Rationale)
		if err != nil {
			return "", err
		}
		application.DisputeIDs = append(application.DisputeIDs, dispute.ID)
		return dispute.ID, nil
	case ActionMergeEntities:
		merge := *action.Payload.Merge
		merge.Actor = authority.ID
		resolution, err := s.mergeEntities(ctx, tx, merge)
		if err != nil {
			return "", err
		}
		application.ResolutionIDs = append(application.ResolutionIDs, resolution.ID)
		return resolution.ID, nil
	case ActionSplitEntity:
		split := *action.Payload.Split
		split.Actor = authority.ID
		resolution, _, err := s.splitEntity(ctx, tx, split)
		if err != nil {
			return "", err
		}
		application.ResolutionIDs = append(application.ResolutionIDs, resolution.ID)
		return resolution.ID, nil
	case ActionChangeFocus:
		rules, err := s.putFocusRules(ctx, tx, *action.Payload.FocusRules)
		if err != nil {
			return "", err
		}
		application.FocusVersions = append(application.FocusVersions, rules.Version)
		return fmt.Sprintf("focus-ruleset-%d", rules.Version), nil
	}
	return "", fmt.Errorf("%w: action kind %q does not require acceptance", ErrInvalidValue, action.Kind)
}

// RejectPlan records that the operator rejected an interpretation.
//
// Nothing authoritative was applied, so nothing is undone. The question returns
// to `answered-uninterpreted`, which is §4.8's retry state: the answer is still
// a good answer, and it is the interpretation of it that was wrong. The plan,
// the answer, and the rejection all remain linked.
func (s *Store) RejectPlan(ctx context.Context, in AcceptanceInput) error {
	if in.Actor == "" {
		return fmt.Errorf("%w: rejection has no actor", ErrInvalidValue)
	}
	if err := checkNoCredential("rejection note", in.Note); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		plan, err := readPlan(ctx, tx, in.PlanID)
		if err != nil {
			return err
		}
		if plan.State != PlanProposed {
			return fmt.Errorf("%w: plan %q is %s", ErrAlreadyDecided, in.PlanID, plan.State)
		}
		id, err := newID("rej")
		if err != nil {
			return err
		}
		payload, err := marshalPayload(StatusPayload{Note: in.Note})
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_plan_rejection(
			id, plan_id, actor, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?)`,
			id, in.PlanID, in.Actor, formatTime(s.now()), payload); err != nil {
			return fmt.Errorf("reality: record rejection: %w", err)
		}
		for _, action := range plan.Actions {
			if action.State != ActionPendingAcceptance {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE reality_plan_action SET state = ? WHERE id = ?`,
				string(ActionRejected), action.ID); err != nil {
				return fmt.Errorf("reality: mark plan action rejected: %w", err)
			}
		}
		return s.transitionQuestion(ctx, tx, plan.QuestionID, QuestionAnsweredUninterpreted,
			in.Actor, "plan "+in.PlanID+" rejected; the answer remains for another interpretation")
	})
}

// Requests reads the pipeline requests a plan recorded. Nothing here starts an
// investigation or refines anything: §4.6 and decision 13 keep publication and
// application outside this package entirely, and these rows are what the review
// pipeline reads.
func (s *Store) Requests(ctx context.Context, planID string) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.action_id, r.request_kind, r.hypothesis_id,
		r.subject_kind, r.subject_id, r.created_at, r.payload_json
		FROM reality_request r JOIN reality_plan_action a ON a.id = r.action_id
		WHERE a.plan_id = ? ORDER BY a.position`, planID)
	if err != nil {
		return nil, fmt.Errorf("reality: read requests: %w", err)
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		var (
			record       Request
			kind         string
			hypothesisID sql.NullString
			subjectKind  sql.NullString
			subjectID    sql.NullString
			created      string
			payload      []byte
		)
		if err := rows.Scan(&record.ID, &record.ActionID, &kind, &hypothesisID,
			&subjectKind, &subjectID, &created, &payload); err != nil {
			return nil, fmt.Errorf("reality: read requests: %w", err)
		}
		record.Kind = RequestKind(kind)
		record.HypothesisID = hypothesisID.String
		record.SubjectKind = WorkKind(subjectKind.String)
		record.SubjectID = subjectID.String
		if record.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("reality: request %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("reality: decode request %s payload: %w", record.ID, err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// Inbox ranks the questions awaiting operator attention.
//
// Membership is narrow: a question is in the inbox when it is `open` or
// `plan-ready`. Those are the two states where the operator is the only thing
// that can move it. A snoozed question was deferred by the operator, a declined
// one was refused, and an `answered-uninterpreted` one is waiting on Babel, not
// on a human — putting any of them in the inbox would make the inbox the list
// of everything rather than the list of what to do.
//
// The ranking is §4.8's five factors in integer arithmetic, and each item
// carries its own terms so the policy can be argued with rather than trusted.
// Ties break on creation time and then ID, so two runs of one inbox agree
// exactly.
func (s *Store) Inbox(ctx context.Context, query InboxQuery) ([]InboxItem, error) {
	if query.Class != "" && !query.Class.valid() {
		return nil, fmt.Errorf("%w: question class %q", ErrInvalidValue, query.Class)
	}
	asOf := s.asOfOr(query.AsOf)
	ids, err := queryStrings(ctx, s.db, `SELECT q.id FROM reality_question q
		WHERE (SELECT e.state FROM reality_question_event e WHERE e.question_id = q.id
			ORDER BY e.seq DESC LIMIT 1) IN (?, ?)
		ORDER BY q.created_at, q.id`, string(QuestionOpen), string(QuestionPlanReady))
	if err != nil {
		return nil, err
	}
	items := make([]InboxItem, 0, len(ids))
	for _, id := range ids {
		question, err := readQuestion(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		if query.Class != "" && question.Class != query.Class {
			continue
		}
		staleness, err := s.stalenessDays(ctx, question, asOf)
		if err != nil {
			return nil, err
		}
		items = append(items, rank(question, staleness))
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		if !items[i].Question.CreatedAt.Equal(items[j].Question.CreatedAt) {
			return items[i].Question.CreatedAt.Before(items[j].Question.CreatedAt)
		}
		return items[i].Question.ID < items[j].Question.ID
	})
	if query.Limit > 0 && len(items) > query.Limit {
		items = items[:query.Limit]
	}
	return items, nil
}

// stalenessDays measures how far past its freshness horizon the stalest fact
// behind a question is. A question about nothing stale scores zero on this
// term, which is correct: acquiring missing context is urgent for other
// reasons, not because something expired.
func (s *Store) stalenessDays(ctx context.Context, question Question, asOf time.Time) (int, error) {
	worst := 0
	for _, factID := range append(copyIDs(question.ExistingFactIDs), question.ConflictFactIDs...) {
		fact, err := readFact(ctx, s.db, factID)
		if err != nil {
			return 0, err
		}
		if fact.ExpiresAt.IsZero() || fact.ExpiresAt.After(asOf) {
			continue
		}
		days := int(asOf.Sub(fact.ExpiresAt).Hours() / 24)
		if days > worst {
			worst = days
		}
	}
	if worst > stalenessCap {
		worst = stalenessCap
	}
	return worst, nil
}

// rank scores one question against §4.8's factors.
func rank(question Question, stalenessDays int) InboxItem {
	blocked := 0
	for _, work := range question.DependentWork {
		if work.Blocking {
			blocked++
		}
	}
	terms := map[string]int{
		"affected-work":    blocked * weightBlockedWork,
		"security-impact":  question.Sensitivity.weight() * weightSecurity,
		"dependency-count": len(question.DependentWork) * weightDependency,
		"staleness":        stalenessDays * weightStaleness,
		"avoided-cost":     question.AvoidedCost * weightAvoidedCost,
	}
	score := 0
	for _, value := range terms {
		score += value
	}
	return InboxItem{Question: question, Score: score, Terms: terms}
}

// CaptureSnapshot freezes the context a focus decision was made against.
//
// §4.8 has discovery persist hypotheses before context-based focus, then
// resolve entities and attach an immutable as-of snapshot, so that a
// deterministic deferral records the context that caused it. That is exactly
// what this stores: the resolved identities, the facts that were read, the
// policy version, the instant, and the decisions — never a mutation of the
// hypothesis, which stays where it is whatever the decision says.
func (s *Store) CaptureSnapshot(ctx context.Context, in SnapshotInput) (Snapshot, error) {
	if in.HypothesisID == "" {
		return Snapshot{}, fmt.Errorf("%w: snapshot names no hypothesis", ErrInvalidValue)
	}
	if len(in.EntityIDs) == 0 {
		return Snapshot{}, fmt.Errorf("%w: snapshot resolves no entity", ErrInvalidValue)
	}
	if err := checkNoCredential("snapshot note", in.Note); err != nil {
		return Snapshot{}, err
	}
	rules, err := s.FocusRules(ctx, in.RuleSetVersion)
	if err != nil {
		return Snapshot{}, err
	}
	asOf := s.asOfOr(in.AsOf)

	record := Snapshot{
		SchemaVersion:  RecordSchema,
		HypothesisID:   in.HypothesisID,
		RuleSetVersion: in.RuleSetVersion,
		AsOf:           asOf,
		Allowance:      AllowanceFull,
		Payload:        SnapshotPayload{Note: in.Note},
	}
	seenFacts := make(map[string]struct{})
	for _, entityID := range sortedUnique(in.EntityIDs) {
		canonical, err := resolve(ctx, s.db, entityID)
		if err != nil {
			return Snapshot{}, err
		}
		facts, err := s.Facts(ctx, FactQuery{SubjectID: canonical})
		if err != nil {
			return Snapshot{}, err
		}
		decision := evaluateFocus(canonical, rules, facts, asOf)
		record.Payload.Decisions = append(record.Payload.Decisions, decision)
		record.Entities = append(record.Entities, SnapshotEntity{
			EntityID:    entityID,
			CanonicalID: canonical,
			Allowance:   decision.Allowance,
		})
		if decision.Allowance.MoreRestrictiveThan(record.Allowance) {
			record.Allowance = decision.Allowance
		}
		for _, input := range decision.Inputs {
			if _, dup := seenFacts[input.FactID]; dup {
				continue
			}
			seenFacts[input.FactID] = struct{}{}
			record.FactIDs = append(record.FactIDs, input.FactID)
		}
	}

	id, err := newID("snp")
	if err != nil {
		return Snapshot{}, err
	}
	record.ID = id
	record.CreatedAt = s.now()
	payload, err := marshalPayload(record.Payload)
	if err != nil {
		return Snapshot{}, err
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_snapshot(
			id, schema_version, hypothesis_id, ruleset_version, as_of, created_at, allowance, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			record.ID, RecordSchema, record.HypothesisID, record.RuleSetVersion,
			formatTime(record.AsOf), formatTime(record.CreatedAt), string(record.Allowance),
			payload); err != nil {
			return fmt.Errorf("reality: insert context snapshot: %w", err)
		}
		for position, entity := range record.Entities {
			if _, err := tx.ExecContext(ctx, `INSERT INTO reality_snapshot_entity(
				snapshot_id, position, entity_id, canonical_id, allowance) VALUES(?, ?, ?, ?, ?)`,
				record.ID, position, entity.EntityID, entity.CanonicalID,
				string(entity.Allowance)); err != nil {
				return fmt.Errorf("reality: insert snapshot entity: %w", err)
			}
		}
		for position, factID := range record.FactIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO reality_snapshot_fact(
				snapshot_id, position, fact_id) VALUES(?, ?, ?)`,
				record.ID, position, factID); err != nil {
				return fmt.Errorf("reality: insert snapshot fact: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	return record, nil
}

// Snapshot reads one immutable context snapshot.
func (s *Store) Snapshot(ctx context.Context, id string) (Snapshot, error) {
	var (
		record    Snapshot
		asOf      string
		created   string
		allowance string
		payload   []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, schema_version, hypothesis_id, ruleset_version,
		as_of, created_at, allowance, payload_json FROM reality_snapshot WHERE id = ?`, id).
		Scan(&record.ID, &record.SchemaVersion, &record.HypothesisID, &record.RuleSetVersion,
			&asOf, &created, &allowance, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Snapshot{}, fmt.Errorf("%w: context snapshot %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Snapshot{}, fmt.Errorf("reality: read context snapshot %q: %w", id, err)
	}
	record.Allowance = Allowance(allowance)
	if record.AsOf, err = parseTime(asOf); err != nil {
		return Snapshot{}, fmt.Errorf("reality: context snapshot %s: %w", id, err)
	}
	if record.CreatedAt, err = parseTime(created); err != nil {
		return Snapshot{}, fmt.Errorf("reality: context snapshot %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Snapshot{}, fmt.Errorf("reality: decode context snapshot %s payload: %w", id, err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT entity_id, canonical_id, allowance
		FROM reality_snapshot_entity WHERE snapshot_id = ? ORDER BY position`, id)
	if err != nil {
		return Snapshot{}, fmt.Errorf("reality: read snapshot entities: %w", err)
	}
	for rows.Next() {
		var (
			entity   SnapshotEntity
			entityAl string
		)
		if err := rows.Scan(&entity.EntityID, &entity.CanonicalID, &entityAl); err != nil {
			rows.Close()
			return Snapshot{}, fmt.Errorf("reality: read snapshot entities: %w", err)
		}
		entity.Allowance = Allowance(entityAl)
		record.Entities = append(record.Entities, entity)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Snapshot{}, fmt.Errorf("reality: read snapshot entities: %w", err)
	}
	rows.Close()
	if record.FactIDs, err = queryStrings(ctx, s.db,
		`SELECT fact_id FROM reality_snapshot_fact WHERE snapshot_id = ? ORDER BY position`, id); err != nil {
		return Snapshot{}, err
	}
	return record, nil
}
