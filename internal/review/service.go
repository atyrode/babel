package review

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// DefaultQueueLimit bounds an unbounded queue request. A review queue exists to
// spend operator attention well (§5.2), and an unbounded list is the failure
// mode that wastes it.
const DefaultQueueLimit = 100

// reviewable reports whether §6.7 lets a record kind carry a disposition.
// Observations are excluded: they are the evidence a finding consolidates, not
// an artifact an operator accepts or rejects. internal/frontier enforces the
// same rule at its own boundary; this one exists so a caller learns which rule
// refused before anything is written.
func reviewable(t frontier.EntityType) bool {
	switch t {
	case frontier.EntityHypothesis, frontier.EntityFinding, frontier.EntityProposal:
		return true
	}
	return false
}

// standing maps a derived review status back to the disposition that produced
// it, or the empty string for a record no one has decided yet.
func standing(status frontier.ReviewStatus) frontier.Disposition {
	switch status {
	case frontier.ReviewAccepted:
		return frontier.DispositionAccept
	case frontier.ReviewRejected, frontier.ReviewRefineRequested:
		return frontier.DispositionReject
	case frontier.ReviewDeferred:
		return frontier.DispositionDefer
	case frontier.ReviewDuplicate:
		return frontier.DispositionDuplicate
	}
	return ""
}

// allowTransition decides whether a record in this state may take this
// disposition. §4.7 makes review append-only, so the question is never whether
// a decision can be undone — it cannot — but whether appending this one says
// something true.
//
// Two states are closed, both because the decision belongs elsewhere.
// `duplicate` says this record is represented by another, so a later
// disposition here would produce two answers to one question with no rule for
// which wins; the original is where it goes. `refine-requested` says a
// rejection authorized a descendant, and §4.7 makes that descendant a
// separately reviewable record, so deciding the ancestor again would silently
// reopen something whose replacement is already in flight.
//
// Repeating the standing decision is refused for a different reason. The
// history is the audit record of how a reviewer's position moved; an event
// that moved nothing makes it read as though the record was reconsidered when
// it was not.
func allowTransition(current frontier.ReviewStatus, d frontier.Disposition) error {
	switch current {
	case frontier.ReviewDuplicate:
		return fmt.Errorf("%w: %s is decided at the record it duplicates", ErrTerminalStatus, current)
	case frontier.ReviewRefineRequested:
		return fmt.Errorf("%w: %s is decided at the descendant the rejection authorized",
			ErrTerminalStatus, current)
	}
	if standing(current) == d {
		return fmt.Errorf("%w: already %s", ErrNoChange, current)
	}
	return nil
}

// RecordContext stores one piece of attributed operator guidance (§4.7).
//
// It takes an Authority rather than a bare string because §4.7 calls the
// guidance attributed, and because the same type gates every decision: the
// identity that may record context is the identity that may decide, and a
// refinement agent holds neither.
//
// What it stores is guidance and nothing more. The returned Context has no
// locator and implements no evidence interface, so nothing downstream can
// promote it into support for a claim.
func (s *Service) RecordContext(ctx context.Context, by Authority, text string) (Context, error) {
	if by.operator == "" {
		return Context{}, errInvalid("operator context has no author")
	}
	if text == "" {
		return Context{}, errInvalid("operator context is empty")
	}
	id, err := newID("ctx")
	if err != nil {
		return Context{}, err
	}
	at := s.now()
	payload, err := marshalPayload(contextPayload{Text: text})
	if err != nil {
		return Context{}, err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO review_context(
		id, author, recorded_at, schema_version, payload_json) VALUES(?, ?, ?, ?, ?)`,
		id, by.operator, formatTime(at), RecordSchema, payload); err != nil {
		return Context{}, fmt.Errorf("review: record operator context: %w", err)
	}
	return Context{ID: id, Author: by.operator, At: at, Text: text}, nil
}

// Context reads one attributed operator context.
func (s *Service) Context(ctx context.Context, id string) (Context, error) {
	record, err := s.readContext(ctx, s.db, id)
	if err != nil {
		return Context{}, err
	}
	return record, nil
}

func (s *Service) readContext(ctx context.Context, q querier, id string) (Context, error) {
	var (
		record   Context
		recorded string
		payload  []byte
	)
	err := q.QueryRowContext(ctx, `SELECT id, author, recorded_at, payload_json
		FROM review_context WHERE id = ?`, id).Scan(&record.ID, &record.Author, &recorded, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Context{}, fmt.Errorf("%w: operator context %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Context{}, fmt.Errorf("review: read operator context: %w", err)
	}
	if record.At, err = parseTime(recorded); err != nil {
		return Context{}, err
	}
	var decoded contextPayload
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return Context{}, fmt.Errorf("review: decode operator context %s payload: %w", id, err)
	}
	record.Text = decoded.Text
	return record, nil
}

// querier is the subset of *sql.DB and *sql.Tx the read helpers need, so one
// read serves both a fresh query and the transaction that wrote the row.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// QueueEntry is one record enrolled for review.
type QueueEntry struct {
	Subject    frontier.Ref
	EnrolledAt time.Time
}

// Enroll puts a reviewable record on the review queue.
//
// Enrolment exists because internal/frontier deliberately exposes no way to
// enumerate its records: it answers questions about a record you name, and the
// one listing it offers is the unexplored frontier. A review queue therefore
// needs its own set of what is awaiting a decision, and the honest place for
// it is here, where the review surface owns it. Exploration and synthesis
// enrol what they produced; Decide enrols whatever it decides, so a decided
// record is never missing from its own queue.
//
// It is idempotent: enrolling twice keeps the first enrolment time, because
// the queue records when a record became reviewable, not when someone last
// mentioned it.
func (s *Service) Enroll(ctx context.Context, subject frontier.Ref) (QueueEntry, error) {
	if err := s.requireReviewable(ctx, subject); err != nil {
		return QueueEntry{}, err
	}
	return s.enroll(ctx, s.db, subject)
}

// execQuerier is what enrol needs: a write and a read, on either a *sql.DB or
// a *sql.Tx.
type execQuerier interface {
	querier
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func (s *Service) enroll(ctx context.Context, q execQuerier, subject frontier.Ref) (QueueEntry, error) {
	at := s.now()
	if _, err := q.ExecContext(ctx, `INSERT INTO review_queue(subject_type, subject_id, enrolled_at)
		VALUES(?, ?, ?) ON CONFLICT(subject_type, subject_id) DO NOTHING`,
		string(subject.Type), subject.ID, formatTime(at)); err != nil {
		return QueueEntry{}, fmt.Errorf("review: enrol %s for review: %w", subject.Type, err)
	}
	var enrolled string
	if err := q.QueryRowContext(ctx, `SELECT enrolled_at FROM review_queue
		WHERE subject_type = ? AND subject_id = ?`,
		string(subject.Type), subject.ID).Scan(&enrolled); err != nil {
		return QueueEntry{}, fmt.Errorf("review: read enrolment: %w", err)
	}
	parsed, err := parseTime(enrolled)
	if err != nil {
		return QueueEntry{}, err
	}
	return QueueEntry{Subject: subject, EnrolledAt: parsed}, nil
}

// requireReviewable checks that a reference names a record the frontier holds
// and that §6.7 makes that kind reviewable.
func (s *Service) requireReviewable(ctx context.Context, subject frontier.Ref) error {
	if !reviewable(subject.Type) {
		return fmt.Errorf("%w: %s", ErrNotReviewable, subject.Type)
	}
	_, err := s.ancestorOf(ctx, subject)
	return err
}

// ancestorOf reads a frontier record and returns the ancestor it revises,
// confirming the record exists on the way. It is one switch rather than four
// call sites so that "does this record exist" and "what does it revise" cannot
// be answered from different tables.
func (s *Service) ancestorOf(ctx context.Context, ref frontier.Ref) (string, error) {
	var (
		ancestor string
		err      error
	)
	switch ref.Type {
	case frontier.EntityHypothesis:
		var record frontier.Hypothesis
		record, err = s.frontier.Hypothesis(ctx, ref.ID)
		ancestor = record.AncestorID
	case frontier.EntityObservation:
		var record frontier.Observation
		record, err = s.frontier.Observation(ctx, ref.ID)
		ancestor = record.AncestorID
	case frontier.EntityFinding:
		var record frontier.Finding
		record, err = s.frontier.Finding(ctx, ref.ID)
		ancestor = record.AncestorID
	case frontier.EntityProposal:
		var record frontier.Proposal
		record, err = s.frontier.Proposal(ctx, ref.ID)
		ancestor = record.AncestorID
	default:
		return "", fmt.Errorf("%w: entity type %q", ErrInvalidValue, ref.Type)
	}
	if err != nil {
		if errors.Is(err, frontier.ErrUnknownEntity) {
			return "", fmt.Errorf("%w: %s %q", ErrUnknownRecord, ref.Type, ref.ID)
		}
		return "", fmt.Errorf("review: read %s %q: %w", ref.Type, ref.ID, err)
	}
	return ancestor, nil
}

// QueueFilter narrows a review queue request.
type QueueFilter struct {
	// Type narrows to one record kind; the empty value includes every
	// reviewable kind.
	Type frontier.EntityType
	// Status narrows to one derived review status. The empty value means
	// records awaiting a first decision, which is what a queue is for.
	Status frontier.ReviewStatus
	// AllStatuses widens the queue to everything enrolled, including
	// records already decided. It is separate from Status because "any" and
	// "unspecified" are different requests and one empty string cannot mean
	// both.
	AllStatuses bool
	// Limit bounds the result; zero means DefaultQueueLimit.
	Limit int
}

// QueueItem is one row of the review queue.
type QueueItem struct {
	Subject    frontier.Ref
	EnrolledAt time.Time
	// Status is derived from internal/frontier's append-only disposition
	// history, so a queue row can never disagree with the events behind it.
	Status frontier.ReviewStatus
	// Decisions is how many dispositions the record already carries, and
	// LastDecidedAt when the newest was recorded. Together they distinguish
	// a record nobody has looked at from one that has been reconsidered
	// twice, which is the difference a reviewer sorting a queue cares about.
	Decisions     int
	LastDecidedAt time.Time
	// Refinements is how many refinement requests the record has
	// authorized.
	Refinements int
}

// Queue lists the records awaiting review, oldest enrolment first.
//
// Ordering is enrolment order and nothing else. §5.2 confines novelty and
// priority estimates to ordering and forbids them from gating whether a
// candidate exists, and a review queue that reordered itself by a
// model-produced score would quietly do the sorting the reviewer is there to
// do.
func (s *Service) Queue(ctx context.Context, f QueueFilter) ([]QueueItem, error) {
	if f.Type != "" && !reviewable(f.Type) {
		return nil, fmt.Errorf("%w: %s", ErrNotReviewable, f.Type)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultQueueLimit
	}
	query := `SELECT subject_type, subject_id, enrolled_at FROM review_queue`
	args := []any{}
	if f.Type != "" {
		query += ` WHERE subject_type = ?`
		args = append(args, string(f.Type))
	}
	query += ` ORDER BY enrolled_at, rowid`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("review: read review queue: %w", err)
	}
	var entries []QueueEntry
	for rows.Next() {
		var (
			subjectType string
			id          string
			enrolled    string
		)
		if err := rows.Scan(&subjectType, &id, &enrolled); err != nil {
			rows.Close()
			return nil, fmt.Errorf("review: read review queue: %w", err)
		}
		at, err := parseTime(enrolled)
		if err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, QueueEntry{
			Subject:    frontier.Ref{Type: frontier.EntityType(subjectType), ID: id},
			EnrolledAt: at,
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("review: read review queue: %w", err)
	}
	// The result set is drained before any other query runs: the durable
	// database allows one connection, so a query issued while rows are open
	// would wait for the connection those rows hold.
	rows.Close()

	items := make([]QueueItem, 0, len(entries))
	for _, entry := range entries {
		item, err := s.queueItem(ctx, entry)
		if err != nil {
			return nil, err
		}
		if !f.AllStatuses {
			want := f.Status
			if want == "" {
				want = frontier.ReviewNew
			}
			if item.Status != want {
				continue
			}
		} else if f.Status != "" && item.Status != f.Status {
			continue
		}
		items = append(items, item)
		if len(items) == limit {
			break
		}
	}
	return items, nil
}

func (s *Service) queueItem(ctx context.Context, entry QueueEntry) (QueueItem, error) {
	item := QueueItem{Subject: entry.Subject, EnrolledAt: entry.EnrolledAt}
	history, err := s.frontier.DispositionHistory(ctx, entry.Subject)
	if err != nil {
		return QueueItem{}, fmt.Errorf("review: read disposition history: %w", err)
	}
	item.Decisions = len(history)
	if len(history) > 0 {
		item.LastDecidedAt = history[len(history)-1].RecordedAt
	}
	status, err := s.frontier.ReviewStatus(ctx, entry.Subject)
	if err != nil {
		return QueueItem{}, fmt.Errorf("review: derive review status: %w", err)
	}
	item.Status = status
	requests, err := s.frontier.RefinementRequests(ctx, entry.Subject)
	if err != nil {
		return QueueItem{}, fmt.Errorf("review: read refinement requests: %w", err)
	}
	item.Refinements = len(requests)
	return item, nil
}

// Decision is one §4.7 review decision as a reviewer supplies it.
type Decision struct {
	Subject     frontier.Ref
	Disposition frontier.Disposition
	// By attributes the decision. The type is Authority rather than a
	// string so that the identity recording a decision is one a refinement
	// agent cannot produce.
	By Authority
	// ContextID names attributed operator guidance recorded alongside the
	// decision. It is guidance: it explains the decision and supports
	// nothing.
	ContextID string
	// DuplicateOfID names the original a `duplicate` decision points at.
	DuplicateOfID string
	// Note is the reviewer's own words about the decision.
	Note string
}

// Decide appends one §4.7 disposition, after checking that §6.7 makes the
// record reviewable and that its current state accepts the decision.
//
// The append itself is internal/frontier's, and deliberately so: there is one
// disposition log, not two. What this adds is the validation a reviewer needs
// to hear about before the event exists — a wrong kind, a closed state, a
// decision that repeats itself, a context ID that names nothing — each
// reported with the error that says which rule refused.
func (s *Service) Decide(ctx context.Context, in Decision) (frontier.DispositionEvent, error) {
	if err := s.checkDecision(ctx, in); err != nil {
		return frontier.DispositionEvent{}, err
	}
	if _, err := s.enroll(ctx, s.db, in.Subject); err != nil {
		return frontier.DispositionEvent{}, err
	}
	event, err := s.frontier.Decide(ctx, frontier.DispositionInput{
		Subject:       in.Subject,
		Disposition:   in.Disposition,
		ReviewerID:    in.By.operator,
		ContextID:     in.ContextID,
		DuplicateOfID: in.DuplicateOfID,
		Note:          in.Note,
	})
	if err != nil {
		return frontier.DispositionEvent{}, fmt.Errorf("review: record disposition: %w", err)
	}
	return event, nil
}

// RejectAndRefine is §4.7's single atomic operation, exposed with the
// service's validation in front of it: it appends a `reject` event and creates
// the refinement request that rejection authorizes, in one internal/frontier
// transaction. There is no standalone `refine` disposition, here or anywhere,
// because a refinement that no rejection authorized would be a rewrite with no
// recorded reason.
func (s *Service) RejectAndRefine(ctx context.Context, in Decision, guidance frontier.RefinementPayload) (frontier.DispositionEvent, frontier.RefinementRequest, error) {
	in.Disposition = frontier.DispositionReject
	if err := s.checkDecision(ctx, in); err != nil {
		return frontier.DispositionEvent{}, frontier.RefinementRequest{}, err
	}
	if _, err := s.enroll(ctx, s.db, in.Subject); err != nil {
		return frontier.DispositionEvent{}, frontier.RefinementRequest{}, err
	}
	rejection, request, err := s.frontier.RejectAndRefine(ctx, frontier.DispositionInput{
		Subject:       in.Subject,
		ReviewerID:    in.By.operator,
		ContextID:     in.ContextID,
		DuplicateOfID: in.DuplicateOfID,
		Note:          in.Note,
	}, guidance)
	if err != nil {
		return frontier.DispositionEvent{}, frontier.RefinementRequest{}, fmt.Errorf("review: reject and refine: %w", err)
	}
	return rejection, request, nil
}

// checkDecision runs every rule that can refuse a decision before anything is
// appended.
func (s *Service) checkDecision(ctx context.Context, in Decision) error {
	switch in.Disposition {
	case frontier.DispositionAccept, frontier.DispositionReject,
		frontier.DispositionDefer, frontier.DispositionDuplicate:
	default:
		return fmt.Errorf("%w: disposition %q", ErrInvalidValue, in.Disposition)
	}
	if in.By.operator == "" {
		return errInvalid("disposition has no operator identity")
	}
	if err := s.requireReviewable(ctx, in.Subject); err != nil {
		return err
	}
	if in.ContextID != "" {
		if _, err := s.readContext(ctx, s.db, in.ContextID); err != nil {
			return err
		}
	}
	status, err := s.frontier.ReviewStatus(ctx, in.Subject)
	if err != nil {
		return fmt.Errorf("review: derive review status: %w", err)
	}
	return allowTransition(status, in.Disposition)
}

// ReviewEvent is one decision in a record's history, with the attributed
// guidance it cited resolved.
type ReviewEvent struct {
	Event frontier.DispositionEvent
	// Context is the guidance recorded with the decision, or nil when the
	// reviewer cited none. It is resolved here so a history reader is not
	// left holding an identifier, and it stays a separate field so nothing
	// mistakes it for part of the decision's evidence — a decision has no
	// evidence.
	Context *Context
}

// RefinementEntry is one authorized refinement request and its outcome.
type RefinementEntry struct {
	Request frontier.RefinementRequest
	// Outcome is nil until a refinement worker reported one. §4.7 lets a
	// refinement run independently of its parent, so an authorized request
	// with no outcome is a normal, visible state rather than a gap.
	Outcome *Refinement
}

// History is everything recorded about one reviewable record.
type History struct {
	Subject frontier.Ref
	Status  frontier.ReviewStatus
	// Decisions are in the order they were recorded. §4.7's log is
	// append-only, so a record that was rejected and later reconsidered
	// shows both events; a rejected record remains readable with its whole
	// history rather than disappearing.
	Decisions   []ReviewEvent
	Refinements []RefinementEntry
}

// History reads one record's complete review history: every decision in order,
// the guidance each cited, and every refinement the rejections authorized.
func (s *Service) History(ctx context.Context, subject frontier.Ref) (History, error) {
	if err := s.requireReviewable(ctx, subject); err != nil {
		return History{}, err
	}
	status, err := s.frontier.ReviewStatus(ctx, subject)
	if err != nil {
		return History{}, fmt.Errorf("review: derive review status: %w", err)
	}
	events, err := s.frontier.DispositionHistory(ctx, subject)
	if err != nil {
		return History{}, fmt.Errorf("review: read disposition history: %w", err)
	}
	history := History{Subject: subject, Status: status}
	for _, event := range events {
		entry := ReviewEvent{Event: event}
		if event.ContextID != "" {
			resolved, err := s.readContext(ctx, s.db, event.ContextID)
			if err != nil {
				return History{}, err
			}
			entry.Context = &resolved
		}
		history.Decisions = append(history.Decisions, entry)
	}
	requests, err := s.frontier.RefinementRequests(ctx, subject)
	if err != nil {
		return History{}, fmt.Errorf("review: read refinement requests: %w", err)
	}
	for _, request := range requests {
		entry := RefinementEntry{Request: request}
		outcome, err := s.refinementByRequest(ctx, request.ID)
		switch {
		case err == nil:
			entry.Outcome = &outcome
		case errors.Is(err, ErrUnknownRecord):
		default:
			return History{}, err
		}
		history.Refinements = append(history.Refinements, entry)
	}
	return history, nil
}
