package review

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/atyrode/babel/internal/frontier"
)

// RecordRefinement records what a refinement run produced for one authorized
// request: the §4.7 durable-learning assessment, and exactly the descendants
// that assessment's mode allows.
//
// Everything is written in one transaction, because §4.7's separateness is a
// statement about identity rather than about coupling: the assessment, the
// revision link, and the memory proposal each carry their own immutable ID and
// their own lineage, but a stored assessment whose proposal never landed would
// describe a run that did not happen.
//
// What is deliberately not written here is any decision. The revision's
// disposition is appended against the frontier record by Decide; the memory
// proposal's is appended against the review record by DisposeMemory; and this
// method takes an Agent, which neither of those accepts. §4.7's "may propose
// but never authorize" is therefore a property of the signatures rather than a
// rule someone has to remember.
func (s *Service) RecordRefinement(ctx context.Context, in RefinementInput) (Refinement, error) {
	if in.Agent.worker == "" {
		return Refinement{}, errInvalid("refinement outcome has no agent identity")
	}
	if err := in.validateShape(); err != nil {
		return Refinement{}, err
	}
	if err := in.Assessment.validate(in.Mode); err != nil {
		return Refinement{}, err
	}
	if in.Memory != nil {
		if err := in.Memory.validate(); err != nil {
			return Refinement{}, err
		}
	}
	if err := s.requireReviewable(ctx, in.Subject); err != nil {
		return Refinement{}, err
	}
	request, err := s.requireRequest(ctx, in.Subject, in.RequestID)
	if err != nil {
		return Refinement{}, err
	}
	for _, id := range in.Assessment.ContextIDs {
		if _, err := s.readContext(ctx, s.db, id); err != nil {
			return Refinement{}, err
		}
	}
	if in.Revision != nil {
		if err := s.requireRevisionOf(ctx, in.Subject, *in.Revision); err != nil {
			return Refinement{}, err
		}
	}
	if err := s.requestUnanswered(ctx, in.RequestID); err != nil {
		return Refinement{}, err
	}

	recorded := s.now()
	outcome := Refinement{
		RequestID:  request.ID,
		Subject:    in.Subject,
		Mode:       in.Mode,
		AgentID:    in.Agent.worker,
		RecordedAt: recorded,
		Revision:   in.Revision,
	}
	assessment := Assessment{
		RequestID:     request.ID,
		Subject:       in.Subject,
		Mode:          in.Mode,
		AgentID:       in.Agent.worker,
		SchemaVersion: RecordSchema,
		RecordedAt:    recorded,
		Payload:       in.Assessment,
	}
	if outcome.ID, err = newID("rfo"); err != nil {
		return Refinement{}, err
	}
	if assessment.ID, err = newID("asm"); err != nil {
		return Refinement{}, err
	}
	var memory *MemoryProposal
	if in.Memory != nil {
		id, err := newID("mem")
		if err != nil {
			return Refinement{}, err
		}
		memory = &MemoryProposal{
			ID:            id,
			AssessmentID:  assessment.ID,
			Destination:   in.Assessment.Destination,
			Sensitivity:   in.Assessment.Sensitivity,
			SchemaVersion: RecordSchema,
			CreatedAt:     recorded,
			Payload: MemoryPayload{
				Title:         in.Memory.Title,
				Statement:     in.Memory.Statement,
				Applicability: in.Memory.Applicability,
				Supporting:    in.Memory.Supporting,
			},
			// A freshly proposed artifact is undisposed, and stays that
			// way until an Authority says otherwise.
			Status: frontier.ReviewNew,
		}
	}

	err = s.transact(func(tx *sql.Tx) error {
		assessmentPayload, err := marshalPayload(assessment.Payload)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_assessment(
			id, request_id, subject_type, subject_id, mode, agent_id,
			schema_version, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			assessment.ID, assessment.RequestID, string(in.Subject.Type), in.Subject.ID,
			string(assessment.Mode), assessment.AgentID, assessment.SchemaVersion,
			formatTime(recorded), assessmentPayload); err != nil {
			return fmt.Errorf("review: record assessment: %w", err)
		}
		if memory != nil {
			memoryPayload, err := marshalPayload(memory.Payload)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO review_memory(
				id, assessment_id, destination, sensitivity, schema_version,
				created_at, payload_json) VALUES(?, ?, ?, ?, ?, ?, ?)`,
				memory.ID, memory.AssessmentID, string(memory.Destination),
				string(memory.Sensitivity), memory.SchemaVersion,
				formatTime(recorded), memoryPayload); err != nil {
				return fmt.Errorf("review: record durable-learning proposal: %w", err)
			}
		}
		revisionKind, revisionID := "", ""
		if in.Revision != nil {
			revisionKind, revisionID = string(in.Revision.Type), in.Revision.ID
		}
		memoryID := ""
		if memory != nil {
			memoryID = memory.ID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_refinement(
			id, request_id, subject_type, subject_id, assessment_id, mode,
			revision_kind, revision_id, memory_id, agent_id, recorded_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			outcome.ID, outcome.RequestID, string(in.Subject.Type), in.Subject.ID,
			assessment.ID, string(in.Mode), revisionKind, revisionID, memoryID,
			outcome.AgentID, formatTime(recorded)); err != nil {
			return fmt.Errorf("review: record refinement outcome: %w", err)
		}

		// Lineage. §4.7 requires the rejection, the assessment, the
		// revised output, and the memory proposal to keep separate
		// lineage; these edges are that lineage, and they are what
		// makes two refinement generations walkable in both
		// directions.
		edges := []lineageEdge{
			{RelationRespondsTo, Node{KindAssessment, assessment.ID}, Node{KindRefinementRequest, request.ID}},
		}
		if in.Revision != nil {
			edges = append(edges,
				lineageEdge{RelationRefines, node(*in.Revision), node(in.Subject)},
				lineageEdge{RelationRespondsTo, node(*in.Revision), Node{KindAssessment, assessment.ID}},
			)
		}
		if memory != nil {
			edges = append(edges,
				lineageEdge{RelationRespondsTo, Node{KindMemoryProposal, memory.ID}, Node{KindAssessment, assessment.ID}},
			)
		}
		for _, edge := range edges {
			if err := s.link(ctx, tx, edge.relation, edge.from, edge.to); err != nil {
				return err
			}
		}
		if in.Revision != nil {
			// A revised descendant is reviewable in its own right, so
			// it joins the queue with the same write that records it.
			if _, err := s.enroll(ctx, tx, *in.Revision); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Refinement{}, err
	}
	outcome.Assessment = assessment
	outcome.Memory = memory
	return outcome, nil
}

// requireRequest resolves an authorized refinement request against its
// subject.
//
// The subject is a parameter rather than something looked up because
// internal/frontier reads refinement requests by subject and exposes no
// by-ID reader; asking the caller for the subject it already knows is a
// smaller cost than this package keeping a shadow copy of another component's
// rows.
func (s *Service) requireRequest(ctx context.Context, subject frontier.Ref, requestID string) (frontier.RefinementRequest, error) {
	if requestID == "" {
		return frontier.RefinementRequest{}, errInvalid("refinement outcome names no request")
	}
	requests, err := s.frontier.RefinementRequests(ctx, subject)
	if err != nil {
		return frontier.RefinementRequest{}, fmt.Errorf("review: read refinement requests: %w", err)
	}
	for _, request := range requests {
		if request.ID == requestID {
			return request, nil
		}
	}
	return frontier.RefinementRequest{}, fmt.Errorf("%w: refinement request %q for %s %q",
		ErrUnknownRecord, requestID, subject.Type, subject.ID)
}

// requireRevisionOf checks that a claimed revised descendant really is one:
// the same kind as the rejected record, and carrying it as its ancestor.
// §4.7's descendants are immutable revisions of what they correct, and a run
// that named an unrelated record here would have produced a replacement with
// no lineage to the thing it replaced.
func (s *Service) requireRevisionOf(ctx context.Context, subject, revision frontier.Ref) error {
	if revision.Type != subject.Type {
		return fmt.Errorf("%w: revision of a %s is a %s", ErrModeMismatch, subject.Type, revision.Type)
	}
	if revision.ID == subject.ID {
		return fmt.Errorf("%w: revision is the rejected record itself", ErrModeMismatch)
	}
	ancestor, err := s.ancestorOf(ctx, revision)
	if err != nil {
		return err
	}
	if ancestor != subject.ID {
		return fmt.Errorf("%w: revision %q does not revise %s %q", ErrModeMismatch, revision.ID, subject.Type, subject.ID)
	}
	return nil
}

// requestUnanswered refuses a second outcome for one request. The database
// enforces it too, through the unique constraint; this exists so the caller
// gets ErrAlreadyRecorded rather than a constraint message.
func (s *Service) requestUnanswered(ctx context.Context, requestID string) error {
	var existing string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM review_refinement WHERE request_id = ?`,
		requestID).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("review: check refinement outcome: %w", err)
	}
	return fmt.Errorf("%w: refinement request %q answered by %q", ErrAlreadyRecorded, requestID, existing)
}

// Refinement reads one recorded refinement outcome.
func (s *Service) Refinement(ctx context.Context, id string) (Refinement, error) {
	return s.refinementWhere(ctx, `id = ?`, id)
}

// refinementByRequest reads the outcome recorded for one refinement request.
func (s *Service) refinementByRequest(ctx context.Context, requestID string) (Refinement, error) {
	return s.refinementWhere(ctx, `request_id = ?`, requestID)
}

func (s *Service) refinementWhere(ctx context.Context, where, arg string) (Refinement, error) {
	var (
		record       Refinement
		subjectType  string
		assessmentID string
		mode         string
		revisionKind string
		revisionID   string
		memoryID     string
		recorded     string
	)
	// where is never caller-supplied: both call sites pass a constant.
	err := s.db.QueryRowContext(ctx, `SELECT id, request_id, subject_type, subject_id,
		assessment_id, mode, revision_kind, revision_id, memory_id, agent_id, recorded_at
		FROM review_refinement WHERE `+where, arg).Scan(&record.ID, &record.RequestID,
		&subjectType, &record.Subject.ID, &assessmentID, &mode, &revisionKind, &revisionID,
		&memoryID, &record.AgentID, &recorded)
	if errors.Is(err, sql.ErrNoRows) {
		return Refinement{}, fmt.Errorf("%w: refinement outcome %q", ErrUnknownRecord, arg)
	}
	if err != nil {
		return Refinement{}, fmt.Errorf("review: read refinement outcome: %w", err)
	}
	record.Subject.Type = frontier.EntityType(subjectType)
	record.Mode = Mode(mode)
	if record.RecordedAt, err = parseTime(recorded); err != nil {
		return Refinement{}, err
	}
	if revisionID != "" {
		record.Revision = &frontier.Ref{Type: frontier.EntityType(revisionKind), ID: revisionID}
	}
	if record.Assessment, err = s.assessment(ctx, assessmentID); err != nil {
		return Refinement{}, err
	}
	if memoryID != "" {
		memory, err := s.Memory(ctx, memoryID)
		if err != nil {
			return Refinement{}, err
		}
		record.Memory = &memory
	}
	return record, nil
}

// Assessment reads one durable-learning assessment.
func (s *Service) Assessment(ctx context.Context, id string) (Assessment, error) {
	return s.assessment(ctx, id)
}

func (s *Service) assessment(ctx context.Context, id string) (Assessment, error) {
	var (
		record      Assessment
		subjectType string
		mode        string
		recorded    string
		payload     []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, request_id, subject_type, subject_id, mode,
		agent_id, schema_version, recorded_at, payload_json FROM review_assessment WHERE id = ?`,
		id).Scan(&record.ID, &record.RequestID, &subjectType, &record.Subject.ID, &mode,
		&record.AgentID, &record.SchemaVersion, &recorded, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Assessment{}, fmt.Errorf("%w: assessment %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Assessment{}, fmt.Errorf("review: read assessment: %w", err)
	}
	record.Subject.Type = frontier.EntityType(subjectType)
	record.Mode = Mode(mode)
	if record.RecordedAt, err = parseTime(recorded); err != nil {
		return Assessment{}, err
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Assessment{}, fmt.Errorf("review: decode assessment %s payload: %w", id, err)
	}
	return record, nil
}

// Memory reads one durable-learning proposal with its derived status.
func (s *Service) Memory(ctx context.Context, id string) (MemoryProposal, error) {
	var (
		record      MemoryProposal
		destination string
		sensitivity string
		created     string
		payload     []byte
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, assessment_id, destination, sensitivity,
		schema_version, created_at, payload_json FROM review_memory WHERE id = ?`,
		id).Scan(&record.ID, &record.AssessmentID, &destination, &sensitivity,
		&record.SchemaVersion, &created, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return MemoryProposal{}, fmt.Errorf("%w: durable-learning proposal %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return MemoryProposal{}, fmt.Errorf("review: read durable-learning proposal: %w", err)
	}
	record.Destination = Destination(destination)
	record.Sensitivity = frontier.Classification(sensitivity)
	if record.CreatedAt, err = parseTime(created); err != nil {
		return MemoryProposal{}, err
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return MemoryProposal{}, fmt.Errorf("review: decode durable-learning proposal %s payload: %w", id, err)
	}
	if record.Status, err = s.memoryStatus(ctx, id); err != nil {
		return MemoryProposal{}, err
	}
	return record, nil
}

// memoryStatus derives a proposal's review status from its own append-only
// disposition history. Deriving rather than storing is what makes it
// impossible for a status to disagree with the events behind it — and, more to
// the point here, impossible for anything other than a recorded disposition
// against this proposal to change it.
func (s *Service) memoryStatus(ctx context.Context, id string) (frontier.ReviewStatus, error) {
	var disposition string
	err := s.db.QueryRowContext(ctx, `SELECT disposition FROM review_memory_disposition
		WHERE memory_id = ? ORDER BY seq DESC LIMIT 1`, id).Scan(&disposition)
	if errors.Is(err, sql.ErrNoRows) {
		return frontier.ReviewNew, nil
	}
	if err != nil {
		return "", fmt.Errorf("review: derive durable-learning status: %w", err)
	}
	switch frontier.Disposition(disposition) {
	case frontier.DispositionAccept:
		return frontier.ReviewAccepted, nil
	case frontier.DispositionReject:
		return frontier.ReviewRejected, nil
	case frontier.DispositionDefer:
		return frontier.ReviewDeferred, nil
	case frontier.DispositionDuplicate:
		return frontier.ReviewDuplicate, nil
	}
	return "", fmt.Errorf("%w: stored disposition %q", ErrInvalidValue, disposition)
}

// DisposeMemory appends one operator decision about a proposed piece of
// durable learning.
//
// It is a separate method writing a separate table from the disposition
// against the revised output, and that separation is §4.7's requirement rather
// than an implementation detail: accepting a revision never silently accepts
// the memory it proposed, and accepting the memory never touches the
// revision's own disposition, because there is no code path that writes both.
//
// It takes an Authority. The refinement agent that proposed the artifact holds
// an Agent, which this signature does not accept, so §4.7's "may propose but
// never authorize" is enforced before the function body runs.
//
// For DestinationRealityPlan, acceptance here means the plan is authorized to
// be reviewed under §4.8's atomic operator-plan acceptance. It does not make a
// fact authoritative; §4.8 owns that step, and nothing in this package can
// perform it.
func (s *Service) DisposeMemory(ctx context.Context, memoryID string, d frontier.Disposition, by Authority, contextID, note string) (MemoryDisposition, error) {
	switch d {
	case frontier.DispositionAccept, frontier.DispositionReject,
		frontier.DispositionDefer, frontier.DispositionDuplicate:
	default:
		return MemoryDisposition{}, fmt.Errorf("%w: disposition %q", ErrInvalidValue, d)
	}
	if by.operator == "" {
		return MemoryDisposition{}, errInvalid("disposition has no operator identity")
	}
	current, err := s.Memory(ctx, memoryID)
	if err != nil {
		return MemoryDisposition{}, err
	}
	if contextID != "" {
		if _, err := s.readContext(ctx, s.db, contextID); err != nil {
			return MemoryDisposition{}, err
		}
	}
	if err := allowTransition(current.Status, d); err != nil {
		return MemoryDisposition{}, err
	}
	id, err := newID("mds")
	if err != nil {
		return MemoryDisposition{}, err
	}
	payload, err := marshalPayload(contextPayload{Text: note})
	if err != nil {
		return MemoryDisposition{}, err
	}
	recorded := s.now()
	event := MemoryDisposition{
		ID:          id,
		MemoryID:    memoryID,
		Disposition: d,
		AuthorityID: by.operator,
		ContextID:   contextID,
		RecordedAt:  recorded,
		Note:        note,
	}
	err = s.transact(func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) + 1
			FROM review_memory_disposition WHERE memory_id = ?`, memoryID).Scan(&event.Sequence); err != nil {
			return fmt.Errorf("review: next disposition sequence: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO review_memory_disposition(
			id, memory_id, seq, disposition, authority_id, context_id, recorded_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			id, memoryID, event.Sequence, string(d), by.operator, contextID,
			formatTime(recorded), payload); err != nil {
			return fmt.Errorf("review: append durable-learning disposition: %w", err)
		}
		return nil
	})
	if err != nil {
		return MemoryDisposition{}, err
	}
	return event, nil
}

// MemoryHistory reads a durable-learning proposal's decisions in order.
func (s *Service) MemoryHistory(ctx context.Context, memoryID string) ([]MemoryDisposition, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, memory_id, seq, disposition, authority_id,
		context_id, recorded_at, payload_json FROM review_memory_disposition
		WHERE memory_id = ? ORDER BY seq`, memoryID)
	if err != nil {
		return nil, fmt.Errorf("review: read durable-learning history: %w", err)
	}
	defer rows.Close()
	var history []MemoryDisposition
	for rows.Next() {
		var (
			record      MemoryDisposition
			disposition string
			recorded    string
			payload     []byte
		)
		if err := rows.Scan(&record.ID, &record.MemoryID, &record.Sequence, &disposition,
			&record.AuthorityID, &record.ContextID, &recorded, &payload); err != nil {
			return nil, fmt.Errorf("review: read durable-learning history: %w", err)
		}
		record.Disposition = frontier.Disposition(disposition)
		if record.RecordedAt, err = parseTime(recorded); err != nil {
			return nil, err
		}
		var decoded contextPayload
		if err := json.Unmarshal(payload, &decoded); err != nil {
			return nil, fmt.Errorf("review: decode disposition %s payload: %w", record.ID, err)
		}
		record.Note = decoded.Text
		history = append(history, record)
	}
	return history, rows.Err()
}
