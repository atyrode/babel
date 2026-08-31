package frontier

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// This file holds the append-only revision chain of issue #87 and the revive
// transition that makes every lifecycle status a resting place rather than an
// ending. Both are the same idea applied at two levels: a record's content and
// a candidate's lifecycle each move forward by appending an attributable
// event, and neither ever moves backwards by overwriting one.

// ErrSuperseded reports a revision aimed at a record another revision already
// supersedes. It is deliberately not a merge: two descendants of one record
// are two current wordings, and a chain with two heads cannot answer "what
// does this candidate say now" — which is the question the chain exists to
// answer. The remedy is to revise the head, which Head returns.
var ErrSuperseded = errors.New("record is already superseded; revise the chain's head")

// revisionWrite is one chain entry about to be appended.
type revisionWrite struct {
	entity     Ref
	supersedes string
	actor      Actor
	reason     string
	// recordedAt is the record's own creation time rather than a second
	// clock reading. A revision that claimed to have happened a microsecond
	// after the record it describes would make two truthful timestamps
	// disagree about one event.
	recordedAt time.Time
}

// appendRevision records who produced a record and why, and places it in its
// chain. It runs inside the transaction that inserted the record, so a record
// without a revision row is not a state this package can reach.
func (s *Store) appendRevision(ctx context.Context, tx *sql.Tx, in revisionWrite) (Revision, error) {
	if err := in.actor.validate(); err != nil {
		return Revision{}, err
	}
	id, err := newID("rev")
	if err != nil {
		return Revision{}, err
	}
	payload := RevisionPayload{Reason: in.reason}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return Revision{}, err
	}
	record := Revision{
		ID:           id,
		Entity:       in.entity,
		RootID:       in.entity.ID,
		SupersedesID: in.supersedes,
		Sequence:     1,
		Actor:        in.actor,
		RecordedAt:   in.recordedAt,
		Payload:      payload,
	}
	if in.supersedes != "" {
		parent, err := revisionOf(ctx, tx, in.entity.Type, in.supersedes)
		if err != nil {
			return Revision{}, err
		}
		var successor string
		err = tx.QueryRowContext(ctx,
			`SELECT entity_id FROM frontier_revision WHERE entity_type = ? AND supersedes_id = ?`,
			string(in.entity.Type), in.supersedes).Scan(&successor)
		switch {
		case err == nil:
			return Revision{}, fmt.Errorf("%w: %s %q is superseded by %q",
				ErrSuperseded, in.entity.Type, in.supersedes, successor)
		case !errors.Is(err, sql.ErrNoRows):
			return Revision{}, fmt.Errorf("check for a competing revision: %w", err)
		}
		record.RootID = parent.RootID
		record.Sequence = parent.Sequence + 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO frontier_revision(
		id, entity_type, entity_id, root_id, supersedes_id, seq,
		actor_kind, actor_id, recorded_at, payload_json)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, string(in.entity.Type), in.entity.ID, record.RootID, record.SupersedesID, record.Sequence,
		string(in.actor.Kind), in.actor.ID, formatTime(in.recordedAt), encoded); err != nil {
		return Revision{}, fmt.Errorf("append revision: %w", err)
	}
	return record, nil
}

const revisionSelect = `SELECT id, entity_type, entity_id, root_id, supersedes_id, seq,
	actor_kind, actor_id, recorded_at, payload_json FROM frontier_revision`

// revisionOf reads the chain entry of one record.
func revisionOf(ctx context.Context, q querier, kind EntityType, id string) (Revision, error) {
	row := q.QueryRowContext(ctx, revisionSelect+` WHERE entity_type = ? AND entity_id = ?`, string(kind), id)
	record, err := scanRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, fmt.Errorf("%w: %s %q has no revision", ErrUnknownEntity, kind, id)
	}
	return record, err
}

func scanRevision(row interface{ Scan(...any) error }) (Revision, error) {
	var (
		record    Revision
		kind      string
		actorKind string
		actorID   string
		recorded  string
		payload   []byte
	)
	if err := row.Scan(&record.ID, &kind, &record.Entity.ID, &record.RootID, &record.SupersedesID,
		&record.Sequence, &actorKind, &actorID, &recorded, &payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Revision{}, err
		}
		return Revision{}, fmt.Errorf("read revision: %w", err)
	}
	record.Entity.Type = EntityType(kind)
	record.Actor = Actor{Kind: ActorKind(actorKind), ID: actorID}
	parsed, err := parseTime(recorded)
	if err != nil {
		return Revision{}, fmt.Errorf("revision %s: %w", record.ID, err)
	}
	record.RecordedAt = parsed
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Revision{}, fmt.Errorf("decode revision %s payload: %w", record.ID, err)
	}
	return record, nil
}

// Revisions reads a record's whole chain, oldest first, from wherever in it
// the caller happens to be standing.
//
// The argument may name any revision of the record — the original, the head,
// or anything between — and the answer is the same chain. That is the point:
// an operator pastes the identifier a listing printed, and a history that
// depended on having pasted the newest one would be a history you need to
// already know the answer to read.
func (s *Store) Revisions(ctx context.Context, of Ref) ([]Revision, error) {
	if err := s.requireSubject(ctx, s.db, of, false); err != nil {
		return nil, err
	}
	anchor, err := revisionOf(ctx, s.db, of.Type, of.ID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		revisionSelect+` WHERE entity_type = ? AND root_id = ? ORDER BY seq, recorded_at, id`,
		string(of.Type), anchor.RootID)
	if err != nil {
		return nil, fmt.Errorf("read revision chain: %w", err)
	}
	defer rows.Close()
	var chain []Revision
	for rows.Next() {
		record, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		chain = append(chain, record)
	}
	return chain, rows.Err()
}

// Head reports the record that is the chain's current state.
//
// It is the last entry of Revisions and is returned separately because that is
// the question almost every caller actually has: a run that is about to revise
// a candidate, a renderer deciding which wording to show, and a disposition
// binding itself to a record all need the current one and none of them need
// the history to get it. ErrSuperseded keeps the two answers consistent —
// there is exactly one leaf, so "last by sequence" and "the one nothing
// supersedes" name the same record.
func (s *Store) Head(ctx context.Context, of Ref) (Ref, error) {
	chain, err := s.Revisions(ctx, of)
	if err != nil {
		return Ref{}, err
	}
	if len(chain) == 0 {
		return Ref{}, fmt.Errorf("%w: %s %q has no revision", ErrUnknownEntity, of.Type, of.ID)
	}
	return chain[len(chain)-1].Entity, nil
}

// ReviveInput returns one resting candidate to the frontier.
type ReviveInput struct {
	HypothesisID string
	// Status is where the candidate lands. The empty value means queued,
	// which is what reviving is for: a candidate an operator or a run
	// argued back into scope is waiting for a run, not waiting for triage
	// it already had.
	Status Status
	// Actor is who revived it. #87 permits both an operator's click and a
	// run's proposal, and which one it was is the whole provenance of the
	// transition.
	Actor Actor
	// Reason is why. It is required: a status that can always be undone is
	// only safe if undoing it leaves an argument behind, otherwise a
	// rejected candidate quietly reappearing is indistinguishable from one
	// that was never rejected.
	Reason string
}

// Revive transitions a resting candidate back onto the frontier.
//
// #87 removes the idea that a status can be an ending — rejected and promoted
// were the two that read like one — so this is the transition out of every
// resting state, and the only one that must be attributed and argued. It is
// not SetStatus with a nicer name: SetStatus is a run's own bookkeeping about
// a candidate it is working on, while this is a claim that a stopped candidate
// deserves to move again, which is exactly the claim #88's ledger reads back
// as evidence about how the frontier is being curated.
//
// The refusal is as important as the transition. Reviving a candidate that is
// untriaged, queued, or under investigation is refused rather than accepted as
// a no-op: the first two are already on the frontier and the third belongs to
// a running exploration, so "revive" there would either mean nothing or would
// rewrite a live run's lifecycle from outside it.
func (s *Store) Revive(ctx context.Context, in ReviveInput) (StatusEvent, error) {
	status := in.Status
	if status == "" {
		status = StatusQueued
	}
	if !status.valid() {
		return StatusEvent{}, fmt.Errorf("%w: status %q", ErrInvalidValue, status)
	}
	if status.resting() {
		return StatusEvent{}, fmt.Errorf("%w: reviving into %q would leave the candidate at rest",
			ErrInvalidValue, status)
	}
	if err := in.Actor.validate(); err != nil {
		return StatusEvent{}, err
	}
	if in.Reason == "" {
		return StatusEvent{}, fmt.Errorf("%w: a revive states why", ErrInvalidValue)
	}
	var recorded StatusEvent
	err := s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRow(ctx, tx, "frontier_hypothesis", in.HypothesisID); err != nil {
			return fmt.Errorf("revive subject: %w", err)
		}
		var current string
		err := tx.QueryRowContext(ctx,
			`SELECT status FROM frontier_status_event WHERE hypothesis_id = ? ORDER BY seq DESC LIMIT 1`,
			in.HypothesisID).Scan(&current)
		if err != nil {
			return fmt.Errorf("read current status: %w", err)
		}
		if !Status(current).resting() {
			return fmt.Errorf("%w: hypothesis %q is %s", ErrNotResting, in.HypothesisID, current)
		}
		// A run's revive is still that run's transition, so run_id stays
		// populated for it; an operator's revive belongs to no run and
		// leaves the column empty rather than borrowing an identity.
		runID := ""
		if in.Actor.Kind == ActorRun {
			runID = in.Actor.ID
		}
		event, err := s.appendStatus(ctx, tx, statusWrite{
			hypothesisID: in.HypothesisID,
			status:       status,
			runID:        runID,
			actor:        in.Actor,
			note:         in.Reason,
		})
		recorded = event
		return err
	})
	if err != nil {
		return StatusEvent{}, err
	}
	return recorded, nil
}

// revisionActor resolves the author of a record write and checks the reason
// rule in the same place, because the two are one question: a write that
// replaces an existing wording has to say who and why, and a write that
// creates one only has to say who.
func revisionActor(actor Actor, runID, ancestorID, reason string) (Actor, error) {
	resolved := actor
	if resolved == (Actor{}) {
		resolved = Run(runID)
	}
	if err := resolved.validate(); err != nil {
		return Actor{}, err
	}
	switch {
	case ancestorID != "" && reason == "":
		return Actor{}, fmt.Errorf("%w: a revision of %q states why it supersedes it", ErrInvalidValue, ancestorID)
	case ancestorID == "" && reason != "":
		return Actor{}, fmt.Errorf("%w: an original record supersedes nothing and has no reason to give", ErrInvalidValue)
	}
	return resolved, nil
}

// statusActor resolves the author of a lifecycle transition, defaulting to the
// run that recorded it.
func statusActor(actor Actor, runID string) (Actor, error) {
	resolved := actor
	if resolved == (Actor{}) {
		resolved = Run(runID)
	}
	if err := resolved.validate(); err != nil {
		return Actor{}, err
	}
	return resolved, nil
}

// storedActor reads an actor back, resolving the rows written before #87 gave
// status events one. Their author is not unknown — no operator could transition
// a candidate then — so an empty kind means the run that recorded the event,
// and this is the single place that says so.
func storedActor(kind, id, runID string) Actor {
	if kind == "" {
		return Run(runID)
	}
	return Actor{Kind: ActorKind(kind), ID: id}
}
