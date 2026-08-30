package reality

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// AssertFact records a fact.
//
// Three §4.8 rules meet here.
//
// Authority decides the initial status, not the caller. An attributed operator
// action or a configured trusted source produces an `active` fact; anything
// derived from Git activity, repository inspection, conversation, or Babel
// analysis produces a `proposed` revision, whatever it asks for.
//
// A contradiction creates a dispute rather than letting the newest write win.
// If an active fact already covers this subject and predicate over an
// overlapping valid time with a different value, and this assertion does not
// supersede it, both facts become `disputed` and a dispute row records them.
// The returned Dispute has an empty ID when none was created.
//
// Nothing is ever mutated. The prior fact keeps its bytes; what changes is an
// appended status event.
func (s *Store) AssertFact(ctx context.Context, in FactInput) (Fact, Dispute, error) {
	if err := in.validate(); err != nil {
		return Fact{}, Dispute{}, err
	}
	if in.Authority.Kind == AuthorityTrustedSource {
		// A trusted source's scope is checked against its registration,
		// which ImportFacts does. Letting a caller name a source here
		// would be a way around the declared scope.
		return Fact{}, Dispute{}, fmt.Errorf(
			"%w: a trusted source's facts enter through ImportFacts, which checks its declared scope",
			ErrOutsideScope)
	}
	var (
		record  Fact
		dispute Dispute
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		created, found, err := s.assertFact(ctx, tx, in, "", "", "")
		record, dispute = created, found
		return err
	})
	if err != nil {
		return Fact{}, Dispute{}, err
	}
	return record, dispute, nil
}

// assertFact inserts a fact and runs the contradiction check inside a caller's
// transaction, so a plan's acceptance applies facts on exactly these terms.
func (s *Store) assertFact(ctx context.Context, tx *sql.Tx, in FactInput,
	supersedes, sourceID, importID string) (Fact, Dispute, error) {
	record, err := s.insertFact(ctx, tx, in, supersedes, sourceID, importID)
	if err != nil {
		return Fact{}, Dispute{}, err
	}
	status := in.initialStatus()
	if status != FactActive || supersedes != "" {
		// A proposal contradicts nothing — it is a proposed revision, not
		// a claim in force — and a supersession is an explicit
		// replacement rather than a conflict.
		if err := s.appendFactStatus(ctx, tx, record.ID, status, ""); err != nil {
			return Fact{}, Dispute{}, err
		}
		record.Status = status
		return record, Dispute{}, nil
	}

	conflicting, err := s.contradictions(ctx, tx, record)
	if err != nil {
		return Fact{}, Dispute{}, err
	}
	if len(conflicting) == 0 {
		if err := s.appendFactStatus(ctx, tx, record.ID, FactActive, ""); err != nil {
			return Fact{}, Dispute{}, err
		}
		record.Status = FactActive
		return record, Dispute{}, nil
	}

	members := append([]string{record.ID}, conflicting...)
	dispute, err := s.openDispute(ctx, tx, record.SubjectID, record.Predicate, members,
		in.Authority.ID, "an active fact with a different value already covers this valid time")
	if err != nil {
		return Fact{}, Dispute{}, err
	}
	record.Status = FactDisputed
	return record, dispute, nil
}

// contradictions finds the active facts a new assertion disagrees with.
//
// Disagreement is narrow on purpose: the same subject, the same predicate, an
// overlapping valid time, and a different value. Two facts with disjoint valid
// times are a history, not a conflict, and two facts with the same value are
// corroboration.
//
// "The same subject" means the same canonical identity, not the same row. Two
// facts asserted about names that a merge has since recognized as one thing do
// contradict each other, and comparing raw subject IDs would let a merge hide a
// conflict it actually created.
func (s *Store) contradictions(ctx context.Context, tx *sql.Tx, candidate Fact) ([]string, error) {
	canonical, err := resolve(ctx, tx, candidate.SubjectID)
	if err != nil {
		return nil, err
	}
	subjects, err := factSubjects(ctx, tx, canonical)
	if err != nil {
		return nil, err
	}
	var conflicting []string
	for _, subject := range subjects {
		existing, err := readFacts(ctx, tx, `WHERE f.subject_id = ? AND f.predicate = ? AND f.id <> ?`,
			subject, string(candidate.Predicate), candidate.ID)
		if err != nil {
			return nil, err
		}
		for _, other := range existing {
			if other.Status != FactActive {
				continue
			}
			if !overlaps(candidate.ValidFrom, candidate.ValidUntil, other.ValidFrom, other.ValidUntil) {
				continue
			}
			if other.Value.equals(candidate.Value) {
				continue
			}
			conflicting = append(conflicting, other.ID)
		}
	}
	return conflicting, nil
}

func (s *Store) insertFact(ctx context.Context, tx *sql.Tx, in FactInput,
	supersedes, sourceID, importID string) (Fact, error) {
	if err := requireRow(ctx, tx, "reality_entity", "id", in.SubjectID); err != nil {
		return Fact{}, fmt.Errorf("reality: fact subject: %w", err)
	}
	if in.Value.Kind == ValueEntity {
		if err := requireRow(ctx, tx, "reality_entity", "id", in.Value.ObjectID); err != nil {
			return Fact{}, fmt.Errorf("reality: fact object: %w", err)
		}
	}
	if err := requireContext(ctx, tx, in.ContextID); err != nil {
		return Fact{}, fmt.Errorf("reality: fact context: %w", err)
	}
	payload := FactPayload{
		Value:      in.Value,
		Provenance: in.Provenance,
		Note:       in.Note,
		ContextID:  in.ContextID,
	}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return Fact{}, err
	}
	id, err := newID("fct")
	if err != nil {
		return Fact{}, err
	}
	recorded := s.now()
	record := Fact{
		ID:            id,
		SchemaVersion: RecordSchema,
		SubjectID:     in.SubjectID,
		Predicate:     in.Predicate,
		Value:         in.Value,
		ValidFrom:     in.ValidFrom.UTC(),
		ValidUntil:    in.ValidUntil,
		ObservedAt:    in.ObservedAt.UTC(),
		RecordedAt:    recorded,
		ExpiresAt:     expiryOf(in.Predicate, in.ObservedAt.UTC()),
		Authority:     in.Authority,
		Confidence:    in.Confidence,
		Sensitivity:   in.Sensitivity,
		Supersedes:    supersedes,
		SourceID:      sourceID,
		ImportID:      importID,
		Payload:       payload,
	}
	if !record.ValidUntil.IsZero() {
		record.ValidUntil = record.ValidUntil.UTC()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_fact(
		id, schema_version, subject_id, predicate, value_kind, object_id,
		valid_from, valid_until, observed_at, recorded_at, expires_at,
		authority_kind, authority_id, authority_at, confidence, sensitivity,
		supersedes, source_id, import_id, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.ID, RecordSchema, record.SubjectID, string(record.Predicate), string(record.Value.Kind),
		nullableID(record.Value.ObjectID), formatTime(record.ValidFrom), nullableTime(record.ValidUntil),
		formatTime(record.ObservedAt), formatTime(record.RecordedAt), nullableTime(record.ExpiresAt),
		string(record.Authority.Kind), record.Authority.ID, formatTime(record.Authority.At),
		string(record.Confidence), string(record.Sensitivity),
		nullableID(supersedes), nullableID(sourceID), nullableID(importID), encoded); err != nil {
		return Fact{}, fmt.Errorf("reality: insert fact: %w", err)
	}
	return record, nil
}

func (s *Store) appendFactStatus(ctx context.Context, tx *sql.Tx, factID string, status FactStatus, note string) error {
	if !status.valid() {
		return fmt.Errorf("%w: fact status %q", ErrInvalidValue, status)
	}
	seq, err := nextSeq(ctx, tx, "reality_fact_status", "fact_id", factID)
	if err != nil {
		return err
	}
	id, err := newID("fst")
	if err != nil {
		return err
	}
	payload, err := marshalPayload(StatusPayload{Note: note})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_fact_status(
		id, fact_id, seq, status, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
		id, factID, seq, string(status), formatTime(s.now()), payload); err != nil {
		return fmt.Errorf("reality: append fact status: %w", err)
	}
	return nil
}

// SupersedeFact replaces a fact with a new revision.
//
// This is the only way a fact's content changes, and it does not change it: the
// prior revision's row is untouched and byte-identical afterwards, and it gains
// a `superseded` status event. The database's unique index on supersedes is
// what stops a chain from forking, so a second attempt to supersede one
// revision loses rather than producing two competing successors.
func (s *Store) SupersedeFact(ctx context.Context, in SupersedeInput) (Fact, error) {
	if in.PriorID == "" {
		return Fact{}, fmt.Errorf("%w: supersession names no prior fact", ErrInvalidValue)
	}
	if err := in.Fact.validate(); err != nil {
		return Fact{}, err
	}
	if in.Fact.Authority.Kind == AuthorityTrustedSource {
		return Fact{}, fmt.Errorf(
			"%w: a trusted source's facts enter through ImportFacts, which checks its declared scope",
			ErrOutsideScope)
	}
	var record Fact
	err := s.transact(ctx, func(tx *sql.Tx) error {
		created, err := s.supersedeFact(ctx, tx, in, "", "")
		record = created
		return err
	})
	if err != nil {
		return Fact{}, err
	}
	return record, nil
}

func (s *Store) supersedeFact(ctx context.Context, tx *sql.Tx, in SupersedeInput,
	sourceID, importID string) (Fact, error) {
	prior, err := readFact(ctx, tx, in.PriorID)
	if err != nil {
		return Fact{}, err
	}
	if prior.SubjectID != in.Fact.SubjectID || prior.Predicate != in.Fact.Predicate {
		return Fact{}, fmt.Errorf("%w: a supersession must keep the subject and predicate of %q",
			ErrInvalidValue, in.PriorID)
	}
	var successor string
	err = tx.QueryRowContext(ctx, `SELECT id FROM reality_fact WHERE supersedes = ?`, in.PriorID).Scan(&successor)
	if err == nil {
		return Fact{}, fmt.Errorf("%w: fact %q is already superseded by %q", ErrConflict, in.PriorID, successor)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Fact{}, fmt.Errorf("reality: check fact successor: %w", err)
	}
	record, _, err := s.assertFact(ctx, tx, in.Fact, in.PriorID, sourceID, importID)
	if err != nil {
		return Fact{}, err
	}
	if err := s.appendFactStatus(ctx, tx, in.PriorID, FactSuperseded,
		"superseded by a later revision"); err != nil {
		return Fact{}, err
	}
	// A supersession is an explicit replacement, so the successor is in
	// force even though assertFact declined to run the contradiction check
	// for it.
	if record.Status != FactActive && in.Fact.initialStatus() == FactActive {
		record.Status = FactActive
	}
	return record, nil
}

const factSelect = `SELECT f.id, f.schema_version, f.subject_id, f.predicate, f.value_kind, f.object_id,
	f.valid_from, f.valid_until, f.observed_at, f.recorded_at, f.expires_at,
	f.authority_kind, f.authority_id, f.authority_at, f.confidence, f.sensitivity,
	f.supersedes, f.source_id, f.import_id, f.payload_json,
	(SELECT e.status FROM reality_fact_status e WHERE e.fact_id = f.id ORDER BY e.seq DESC LIMIT 1)
	FROM reality_fact f `

// Fact reads one immutable revision with its current status.
func (s *Store) Fact(ctx context.Context, id string) (Fact, error) {
	return readFact(ctx, s.db, id)
}

func readFact(ctx context.Context, q querier, id string) (Fact, error) {
	facts, err := readFacts(ctx, q, `WHERE f.id = ?`, id)
	if err != nil {
		return Fact{}, err
	}
	if len(facts) == 0 {
		return Fact{}, fmt.Errorf("%w: fact %q", ErrUnknownRecord, id)
	}
	return facts[0], nil
}

// readFacts decodes a fact query to completion before returning. The durable
// database allows one connection, so a follow-up query issued while a result
// set is open would wait for a connection that result set is holding.
func readFacts(ctx context.Context, q querier, where string, args ...any) ([]Fact, error) {
	rows, err := q.QueryContext(ctx, factSelect+where+` ORDER BY f.recorded_at, f.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("reality: read facts: %w", err)
	}
	defer rows.Close()
	var out []Fact
	for rows.Next() {
		record, err := scanFact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func scanFact(rows *sql.Rows) (Fact, error) {
	var (
		record                                   Fact
		predicate, valueKind                     string
		objectID, validUntil, expiresAt          sql.NullString
		supersedes, sourceID, importID           sql.NullString
		validFrom, observedAt, recordedAt        string
		authorityKind, authorityAt               string
		confidence, sensitivity, status, payload string
	)
	if err := rows.Scan(&record.ID, &record.SchemaVersion, &record.SubjectID, &predicate, &valueKind,
		&objectID, &validFrom, &validUntil, &observedAt, &recordedAt, &expiresAt,
		&authorityKind, &record.Authority.ID, &authorityAt, &confidence, &sensitivity,
		&supersedes, &sourceID, &importID, &payload, &status); err != nil {
		return Fact{}, fmt.Errorf("reality: read fact: %w", err)
	}
	record.Predicate = Predicate(predicate)
	record.Authority.Kind = AuthorityKind(authorityKind)
	record.Confidence = Confidence(confidence)
	record.Sensitivity = Sensitivity(sensitivity)
	record.Status = FactStatus(status)
	record.Supersedes = supersedes.String
	record.SourceID = sourceID.String
	record.ImportID = importID.String

	var err error
	if record.ValidFrom, err = parseTime(validFrom); err != nil {
		return Fact{}, fmt.Errorf("reality: fact %s: %w", record.ID, err)
	}
	if validUntil.Valid {
		if record.ValidUntil, err = parseTime(validUntil.String); err != nil {
			return Fact{}, fmt.Errorf("reality: fact %s: %w", record.ID, err)
		}
	}
	if record.ObservedAt, err = parseTime(observedAt); err != nil {
		return Fact{}, fmt.Errorf("reality: fact %s: %w", record.ID, err)
	}
	if record.RecordedAt, err = parseTime(recordedAt); err != nil {
		return Fact{}, fmt.Errorf("reality: fact %s: %w", record.ID, err)
	}
	if expiresAt.Valid {
		if record.ExpiresAt, err = parseTime(expiresAt.String); err != nil {
			return Fact{}, fmt.Errorf("reality: fact %s: %w", record.ID, err)
		}
	}
	if record.Authority.At, err = parseTime(authorityAt); err != nil {
		return Fact{}, fmt.Errorf("reality: fact %s: %w", record.ID, err)
	}
	if err := json.Unmarshal([]byte(payload), &record.Payload); err != nil {
		return Fact{}, fmt.Errorf("reality: decode fact %s payload: %w", record.ID, err)
	}
	record.Value = record.Payload.Value
	// The object entity is stored twice — once as an allowlisted column so
	// the ledger is queryable by object, once inside the sealed payload —
	// and a row where the two disagree has been altered outside Babel.
	if record.Value.ObjectID != objectID.String {
		return Fact{}, fmt.Errorf("reality: fact %s object entity column and payload disagree", record.ID)
	}
	if record.Value.Kind != ValueKind(valueKind) {
		return Fact{}, fmt.Errorf("reality: fact %s value kind column and payload disagree", record.ID)
	}
	return record, nil
}

// FactQuery selects facts. §4.8 has analysis query the ledger by entity,
// predicate, valid time, freshness, and conflict rather than injecting it into
// a prompt, and these are those axes.
type FactQuery struct {
	// SubjectID is the entity the facts are about. It is resolved through
	// the merge history first, and facts asserted about identities merged
	// into it are included: a merge that did not carry its subject's facts
	// forward would lose reality at the moment two names were recognized as
	// one.
	SubjectID string
	Predicate Predicate
	// AsOf narrows to facts whose valid time covers an instant. Zero means
	// every valid time.
	AsOf time.Time
	// Statuses narrows to these statuses. Empty means every status,
	// including proposals and superseded revisions, because reviewing what
	// was proposed is a real need.
	Statuses []FactStatus
}

// Facts reads the ledger.
func (s *Store) Facts(ctx context.Context, q FactQuery) ([]Fact, error) {
	if q.SubjectID == "" {
		return nil, fmt.Errorf("%w: fact query names no subject", ErrInvalidValue)
	}
	canonical, err := resolve(ctx, s.db, q.SubjectID)
	if err != nil {
		return nil, err
	}
	subjects, err := factSubjects(ctx, s.db, canonical)
	if err != nil {
		return nil, err
	}
	allowed := make(map[FactStatus]struct{}, len(q.Statuses))
	for _, status := range q.Statuses {
		allowed[status] = struct{}{}
	}
	var out []Fact
	for _, subject := range subjects {
		facts, err := readFacts(ctx, s.db, `WHERE f.subject_id = ?`, subject)
		if err != nil {
			return nil, err
		}
		for _, fact := range facts {
			if q.Predicate != "" && fact.Predicate != q.Predicate {
				continue
			}
			if !q.AsOf.IsZero() &&
				!overlaps(fact.ValidFrom, fact.ValidUntil, q.AsOf, q.AsOf.Add(time.Nanosecond)) {
				continue
			}
			if len(allowed) > 0 {
				if _, ok := allowed[fact.Status]; !ok {
					continue
				}
			}
			out = append(out, fact)
		}
	}
	return out, nil
}

// FactStatusHistory reads a fact's append-only status history. It is what
// proves expiry marked rather than deleted, and what shows a dispute opening
// and closing over one revision.
func (s *Store) FactStatusHistory(ctx context.Context, factID string) ([]FactStatusEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, fact_id, seq, status, recorded_at, payload_json
		FROM reality_fact_status WHERE fact_id = ? ORDER BY seq`, factID)
	if err != nil {
		return nil, fmt.Errorf("reality: read fact status history: %w", err)
	}
	defer rows.Close()
	var out []FactStatusEvent
	for rows.Next() {
		var (
			record   FactStatusEvent
			status   string
			recorded string
			payload  []byte
		)
		if err := rows.Scan(&record.ID, &record.FactID, &record.Sequence, &status, &recorded, &payload); err != nil {
			return nil, fmt.Errorf("reality: read fact status history: %w", err)
		}
		record.Status = FactStatus(status)
		if record.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, fmt.Errorf("reality: fact status %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("reality: decode fact status %s payload: %w", record.ID, err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// ExpireStale marks facts whose freshness expectation has lapsed.
//
// §4.8 requires expiry to mark a fact stale rather than delete it, and this
// package has no delete path at all, so what happens is an appended status
// event: the row keeps its bytes, its provenance, and its authority, and a
// reader learns that what it says was last observed too long ago to rely on.
// Only predicates with a TTL can expire, which is how "operator intent does
// not expire automatically" is implemented rather than promised.
//
// It returns the fact IDs it marked, so a caller can raise refresh questions
// for exactly those.
func (s *Store) ExpireStale(ctx context.Context, asOf time.Time) ([]string, error) {
	at := s.asOfOr(asOf)
	var marked []string
	err := s.transact(ctx, func(tx *sql.Tx) error {
		ids, err := queryStrings(ctx, tx, `SELECT f.id FROM reality_fact f
			WHERE f.expires_at IS NOT NULL AND f.expires_at <= ?
			AND (SELECT e.status FROM reality_fact_status e WHERE e.fact_id = f.id
				ORDER BY e.seq DESC LIMIT 1) = ?
			ORDER BY f.expires_at, f.id`, formatTime(at), string(FactActive))
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := s.appendFactStatus(ctx, tx, id, FactStale,
				"the predicate's refresh expectation lapsed"); err != nil {
				return err
			}
		}
		marked = ids
		return nil
	})
	if err != nil {
		return nil, err
	}
	return marked, nil
}

// DisputeFacts opens a dispute explicitly, for a contradiction the
// deterministic check cannot see — two predicates that cannot both hold, or a
// human judging two values incompatible.
func (s *Store) DisputeFacts(ctx context.Context, in DisputeInput) (Dispute, error) {
	if len(in.FactIDs) < 2 {
		return Dispute{}, fmt.Errorf("%w: a dispute needs at least two facts", ErrInvalidValue)
	}
	if in.Actor == "" {
		return Dispute{}, fmt.Errorf("%w: dispute has no actor", ErrInvalidValue)
	}
	if err := checkNoCredential("dispute reason", in.Reason); err != nil {
		return Dispute{}, err
	}
	var record Dispute
	err := s.transact(ctx, func(tx *sql.Tx) error {
		first, err := readFact(ctx, tx, in.FactIDs[0])
		if err != nil {
			return err
		}
		created, err := s.openDispute(ctx, tx, first.SubjectID, first.Predicate,
			sortedUnique(in.FactIDs), in.Actor, in.Reason)
		record = created
		return err
	})
	if err != nil {
		return Dispute{}, err
	}
	return record, nil
}

// openDispute records a dispute and marks its members disputed.
func (s *Store) openDispute(ctx context.Context, tx *sql.Tx, subjectID string, predicate Predicate,
	factIDs []string, actor, reason string) (Dispute, error) {
	id, err := newID("dsp")
	if err != nil {
		return Dispute{}, err
	}
	payload := DisputePayload{Reason: reason}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return Dispute{}, err
	}
	created := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_dispute(
		id, schema_version, subject_id, predicate, created_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
		id, RecordSchema, subjectID, string(predicate), formatTime(created), encoded); err != nil {
		return Dispute{}, fmt.Errorf("reality: insert dispute: %w", err)
	}
	for _, factID := range factIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_dispute_member(dispute_id, fact_id)
			VALUES(?, ?)`, id, factID); err != nil {
			return Dispute{}, fmt.Errorf("reality: insert dispute member: %w", err)
		}
		if err := s.appendFactStatus(ctx, tx, factID, FactDisputed, "contradicted by another fact"); err != nil {
			return Dispute{}, err
		}
	}
	if err := s.appendDisputeState(ctx, tx, id, DisputeOpen, actor, reason); err != nil {
		return Dispute{}, err
	}
	return Dispute{
		ID:            id,
		SchemaVersion: RecordSchema,
		SubjectID:     subjectID,
		Predicate:     predicate,
		FactIDs:       copyIDs(factIDs),
		CreatedAt:     created,
		State:         DisputeOpen,
		Payload:       payload,
	}, nil
}

func (s *Store) appendDisputeState(ctx context.Context, tx *sql.Tx, disputeID string,
	state DisputeState, actor, note string) error {
	seq, err := nextSeq(ctx, tx, "reality_dispute_event", "dispute_id", disputeID)
	if err != nil {
		return err
	}
	id, err := newID("dse")
	if err != nil {
		return err
	}
	payload, err := marshalPayload(StatusPayload{Note: note})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_dispute_event(
		id, dispute_id, seq, state, actor, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		id, disputeID, seq, string(state), actor, formatTime(s.now()), payload); err != nil {
		return fmt.Errorf("reality: append dispute state: %w", err)
	}
	return nil
}

// ResolveDispute closes a dispute by naming the fact that survives.
//
// The survivor returns to `active` and the others are marked `superseded` by a
// status event rather than by a revision link. The distinction is deliberate: a
// revision link means "this replaced that", and a dispute resolution means "an
// operator chose between these", which is a different claim about how the
// ledger reached its current state.
func (s *Store) ResolveDispute(ctx context.Context, in ResolveDisputeInput) error {
	if in.Actor == "" {
		return fmt.Errorf("%w: dispute resolution has no actor", ErrInvalidValue)
	}
	if err := checkNoCredential("dispute resolution note", in.Note); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		record, err := readDispute(ctx, tx, in.DisputeID)
		if err != nil {
			return err
		}
		if record.State != DisputeOpen {
			return fmt.Errorf("%w: dispute %q is %s", ErrInvalidTransition, in.DisputeID, record.State)
		}
		found := false
		for _, id := range record.FactIDs {
			if id == in.KeepFactID {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("%w: fact %q is not part of dispute %q",
				ErrInvalidValue, in.KeepFactID, in.DisputeID)
		}
		for _, id := range record.FactIDs {
			status := FactSuperseded
			note := "set aside by a dispute resolution"
			if id == in.KeepFactID {
				status = FactActive
				note = "upheld by a dispute resolution"
			}
			if err := s.appendFactStatus(ctx, tx, id, status, note); err != nil {
				return err
			}
		}
		return s.appendDisputeState(ctx, tx, in.DisputeID, DisputeResolved, in.Actor, in.Note)
	})
}

// Dispute reads one dispute with its members and current state.
func (s *Store) Dispute(ctx context.Context, id string) (Dispute, error) {
	return readDispute(ctx, s.db, id)
}

func readDispute(ctx context.Context, q querier, id string) (Dispute, error) {
	var (
		record    Dispute
		predicate string
		created   string
		payload   []byte
		state     sql.NullString
	)
	err := q.QueryRowContext(ctx, `SELECT d.id, d.schema_version, d.subject_id, d.predicate,
		d.created_at, d.payload_json,
		(SELECT e.state FROM reality_dispute_event e WHERE e.dispute_id = d.id ORDER BY e.seq DESC LIMIT 1)
		FROM reality_dispute d WHERE d.id = ?`, id).
		Scan(&record.ID, &record.SchemaVersion, &record.SubjectID, &predicate, &created, &payload, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return Dispute{}, fmt.Errorf("%w: dispute %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Dispute{}, fmt.Errorf("reality: read dispute %q: %w", id, err)
	}
	record.Predicate = Predicate(predicate)
	record.State = DisputeState(state.String)
	if record.CreatedAt, err = parseTime(created); err != nil {
		return Dispute{}, fmt.Errorf("reality: dispute %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Dispute{}, fmt.Errorf("reality: decode dispute %s payload: %w", id, err)
	}
	if record.FactIDs, err = queryStrings(ctx, q,
		`SELECT fact_id FROM reality_dispute_member WHERE dispute_id = ? ORDER BY fact_id`, id); err != nil {
		return Dispute{}, err
	}
	return record, nil
}

// DisputesFor reads every dispute a fact belongs to, so a disputed fact
// explains itself.
func (s *Store) DisputesFor(ctx context.Context, factID string) ([]Dispute, error) {
	ids, err := queryStrings(ctx, s.db, `SELECT m.dispute_id FROM reality_dispute_member m
		JOIN reality_dispute d ON d.id = m.dispute_id
		WHERE m.fact_id = ? ORDER BY d.created_at, d.id`, factID)
	if err != nil {
		return nil, err
	}
	out := make([]Dispute, 0, len(ids))
	for _, id := range ids {
		record, err := readDispute(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// RegisterTrustedSource configures a non-operator authority with the scope it
// may author within.
//
// The registration is immutable. Widening a source's scope means registering a
// new version of it, because a scope that could be edited would make every
// past import's authorization unverifiable after the fact.
func (s *Store) RegisterTrustedSource(ctx context.Context, in TrustedSourceInput) (TrustedSource, error) {
	if err := in.validate(); err != nil {
		return TrustedSource{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return TrustedSource{}, err
	}
	registered := s.now()
	record := TrustedSource{
		ID:            in.ID,
		SchemaVersion: RecordSchema,
		Version:       in.Version,
		RegisteredAt:  registered,
		Predicates:    in.Predicates,
		EntityIDs:     sortedUnique(in.EntityIDs),
		EntityKinds:   in.EntityKinds,
		Payload:       in.Payload,
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_trusted_source(
			id, schema_version, source_version, registered_at, payload_json) VALUES(?, ?, ?, ?, ?)`,
			record.ID, RecordSchema, record.Version, formatTime(registered), payload); err != nil {
			return fmt.Errorf("reality: insert trusted source: %w", err)
		}
		for _, predicate := range record.Predicates {
			if _, err := tx.ExecContext(ctx, `INSERT INTO reality_trusted_source_predicate(
				source_id, predicate) VALUES(?, ?)`, record.ID, string(predicate)); err != nil {
				return fmt.Errorf("reality: declare source predicate: %w", err)
			}
		}
		for _, entityID := range record.EntityIDs {
			if err := requireRow(ctx, tx, "reality_entity", "id", entityID); err != nil {
				return fmt.Errorf("reality: declared entity scope: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO reality_trusted_source_entity(
				source_id, entity_id) VALUES(?, ?)`, record.ID, entityID); err != nil {
				return fmt.Errorf("reality: declare source entity: %w", err)
			}
		}
		for _, kind := range record.EntityKinds {
			if _, err := tx.ExecContext(ctx, `INSERT INTO reality_trusted_source_kind(
				source_id, entity_kind) VALUES(?, ?)`, record.ID, string(kind)); err != nil {
				return fmt.Errorf("reality: declare source entity kind: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return TrustedSource{}, err
	}
	return record, nil
}

// TrustedSource reads a source's registration and declared scope.
func (s *Store) TrustedSource(ctx context.Context, id string) (TrustedSource, error) {
	return readTrustedSource(ctx, s.db, id)
}

func readTrustedSource(ctx context.Context, q querier, id string) (TrustedSource, error) {
	var (
		record     TrustedSource
		registered string
		payload    []byte
	)
	err := q.QueryRowContext(ctx, `SELECT id, schema_version, source_version, registered_at, payload_json
		FROM reality_trusted_source WHERE id = ?`, id).
		Scan(&record.ID, &record.SchemaVersion, &record.Version, &registered, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return TrustedSource{}, fmt.Errorf("%w: trusted source %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return TrustedSource{}, fmt.Errorf("reality: read trusted source %q: %w", id, err)
	}
	if record.RegisteredAt, err = parseTime(registered); err != nil {
		return TrustedSource{}, fmt.Errorf("reality: trusted source %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return TrustedSource{}, fmt.Errorf("reality: decode trusted source %s payload: %w", id, err)
	}
	predicates, err := queryStrings(ctx, q,
		`SELECT predicate FROM reality_trusted_source_predicate WHERE source_id = ? ORDER BY predicate`, id)
	if err != nil {
		return TrustedSource{}, err
	}
	for _, name := range predicates {
		record.Predicates = append(record.Predicates, Predicate(name))
	}
	if record.EntityIDs, err = queryStrings(ctx, q,
		`SELECT entity_id FROM reality_trusted_source_entity WHERE source_id = ? ORDER BY entity_id`,
		id); err != nil {
		return TrustedSource{}, err
	}
	kinds, err := queryStrings(ctx, q,
		`SELECT entity_kind FROM reality_trusted_source_kind WHERE source_id = ? ORDER BY entity_kind`, id)
	if err != nil {
		return TrustedSource{}, err
	}
	for _, kind := range kinds {
		record.EntityKinds = append(record.EntityKinds, EntityKind(kind))
	}
	return record, nil
}

// ImportFacts applies one versioned batch from a trusted source.
//
// Every §4.8 constraint on non-operator authority is enforced here, and each
// one refuses the whole batch rather than skipping a row.
//
// The source must be registered, and every fact must name a predicate and an
// entity inside its declared scope: an inventory that may author machine
// placement must not be able to set an analysis policy, and one scoped to
// machines must not author about a repository.
//
// Credentials are forbidden outright. The batch's prose is checked with
// internal/preflight's detector, and a hit refuses the import instead of
// redacting it: a ledger that stored a redacted credential would have recorded
// that a secret was in the inventory, which is a fact about the secret rather
// than about reality.
//
// The batch is atomic and idempotent. A replayed batch key is refused as a
// duplicate rather than duplicating every fact in it.
func (s *Store) ImportFacts(ctx context.Context, in ImportInput) ([]Fact, error) {
	if in.SourceID == "" {
		return nil, fmt.Errorf("%w: import names no source", ErrInvalidValue)
	}
	if in.BatchKey == "" {
		return nil, fmt.Errorf("%w: import has no batch key", ErrInvalidValue)
	}
	if len(in.Facts) == 0 {
		return nil, fmt.Errorf("%w: import carries no facts", ErrInvalidValue)
	}
	var imported []Fact
	err := s.transact(ctx, func(tx *sql.Tx) error {
		source, err := readTrustedSource(ctx, tx, in.SourceID)
		if err != nil {
			return err
		}
		importID, err := newID("imp")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_import(
			id, source_id, batch_key, imported_at, fact_count) VALUES(?, ?, ?, ?, ?)`,
			importID, source.ID, in.BatchKey, formatTime(s.now()), len(in.Facts)); err != nil {
			return fmt.Errorf("reality: record import batch: %w", err)
		}
		for i, fact := range in.Facts {
			// The source authors on its own authority, so the caller
			// does not get to name a different one.
			fact.Authority = Authority{
				Kind: AuthorityTrustedSource,
				ID:   source.ID,
				At:   s.now(),
			}
			if err := fact.validate(); err != nil {
				return fmt.Errorf("reality: imported fact %d: %w", i, err)
			}
			if !source.permitsPredicate(fact.Predicate) {
				return fmt.Errorf("%w: source %q may not author predicate %s",
					ErrOutsideScope, source.ID, fact.Predicate)
			}
			kind, err := entityKindOf(ctx, tx, fact.SubjectID)
			if err != nil {
				return err
			}
			if !source.permitsEntity(fact.SubjectID, kind) {
				return fmt.Errorf("%w: source %q may not author about entity %q (a %s)",
					ErrOutsideScope, source.ID, fact.SubjectID, kind)
			}
			record, _, err := s.assertFact(ctx, tx, fact, "", source.ID, importID)
			if err != nil {
				return err
			}
			imported = append(imported, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return imported, nil
}

// PutFocusRules installs a focus rule set version.
//
// A version is immutable once installed. That is what makes §4.8's mapping
// versioned in a useful sense: a decision recorded against version 1 can be
// re-derived from version 1's own bytes, so an operator comparing two
// decisions is comparing two policies rather than two memories of one.
func (s *Store) PutFocusRules(ctx context.Context, rules FocusRuleSet) (FocusRuleSet, error) {
	if err := rules.validate(); err != nil {
		return FocusRuleSet{}, err
	}
	var stored FocusRuleSet
	err := s.transact(ctx, func(tx *sql.Tx) error {
		created, err := s.putFocusRules(ctx, tx, rules)
		stored = created
		return err
	})
	if err != nil {
		return FocusRuleSet{}, err
	}
	return stored, nil
}

func (s *Store) putFocusRules(ctx context.Context, tx *sql.Tx, rules FocusRuleSet) (FocusRuleSet, error) {
	if err := rules.validate(); err != nil {
		return FocusRuleSet{}, err
	}
	var existing int
	err := tx.QueryRowContext(ctx, `SELECT version FROM reality_focus_ruleset WHERE version = ?`,
		rules.Version).Scan(&existing)
	if err == nil {
		return FocusRuleSet{}, fmt.Errorf("%w: focus rule set version %d is already installed",
			ErrConflict, rules.Version)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return FocusRuleSet{}, fmt.Errorf("reality: check focus rule set version: %w", err)
	}
	rules.CreatedAt = s.now()
	payload, err := marshalPayload(rules)
	if err != nil {
		return FocusRuleSet{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_focus_ruleset(version, created_at, payload_json)
		VALUES(?, ?, ?)`, rules.Version, formatTime(rules.CreatedAt), payload); err != nil {
		return FocusRuleSet{}, fmt.Errorf("reality: install focus rule set: %w", err)
	}
	return rules, nil
}

// FocusRules reads one installed policy version.
func (s *Store) FocusRules(ctx context.Context, version int) (FocusRuleSet, error) {
	return readFocusRules(ctx, s.db, version)
}

func readFocusRules(ctx context.Context, q querier, version int) (FocusRuleSet, error) {
	var (
		created string
		payload []byte
		rules   FocusRuleSet
	)
	err := q.QueryRowContext(ctx, `SELECT created_at, payload_json FROM reality_focus_ruleset
		WHERE version = ?`, version).Scan(&created, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return FocusRuleSet{}, fmt.Errorf("%w: focus rule set version %d", ErrUnknownRecord, version)
	}
	if err != nil {
		return FocusRuleSet{}, fmt.Errorf("reality: read focus rule set %d: %w", version, err)
	}
	if err := json.Unmarshal(payload, &rules); err != nil {
		return FocusRuleSet{}, fmt.Errorf("reality: decode focus rule set %d: %w", version, err)
	}
	if rules.CreatedAt, err = parseTime(created); err != nil {
		return FocusRuleSet{}, fmt.Errorf("reality: focus rule set %d: %w", version, err)
	}
	return rules, nil
}

// EvaluateFocus decides what analysis may spend on an entity.
//
// This is §4.8's mapping, performed rather than inferred. Nothing about a
// lifecycle, ownership, or policy fact reaches a decision except through a rule
// in the named version, the decision names the rule and the facts it read, and
// the same ledger under two versions yields two different answers. A decision
// resting on a stale or disputed fact is marked contested rather than taken
// quietly, which is what §4.8 gives the challenger to check.
func (s *Store) EvaluateFocus(ctx context.Context, q FocusQuery) (FocusDecision, error) {
	if q.EntityID == "" {
		return FocusDecision{}, fmt.Errorf("%w: focus query names no entity", ErrInvalidValue)
	}
	rules, err := readFocusRules(ctx, s.db, q.RuleSetVersion)
	if err != nil {
		return FocusDecision{}, err
	}
	asOf := s.asOfOr(q.AsOf)
	canonical, err := resolve(ctx, s.db, q.EntityID)
	if err != nil {
		return FocusDecision{}, err
	}
	facts, err := s.Facts(ctx, FactQuery{SubjectID: canonical})
	if err != nil {
		return FocusDecision{}, err
	}
	return evaluateFocus(canonical, rules, facts, asOf), nil
}
