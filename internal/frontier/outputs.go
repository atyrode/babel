package frontier

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// This file is the frontier's read surface for retrieval over Babel's own
// output (#87 item 4).
//
// It exists because dedup and refinement are retrieval problems and Babel had
// no way to retrieve itself. Every record here was already stored, readable by
// id and listable by page; none of it was searchable, so a run had no
// mechanical way to discover that the candidate it was about to mint had been
// minted, developed, argued about and rejected three runs ago. The remedy is
// not a second store: it is one flattened view of what the frontier currently
// says, which a rebuildable index can hold and a query can reach.
//
// Two properties make it a view rather than a copy. Only head revisions are
// offered: a superseded wording is not what the frontier says now, and
// offering it would make refinement compete with its own history. And nothing
// here is authoritative — Summary and Text are derived from the payloads on
// every read, so a consumer that caches them holds a cache, and the records
// remain the only source of truth.

// maxSummaryBytes bounds the one-line summary of a record.
//
// A summary is what a job document lists and what a search hit shows, and both
// consume a line rather than a paragraph. It is deliberately much smaller than
// the indexed text: the text answers "does this record match", the summary
// answers "which record is this", and a summary long enough to need scrolling
// answers neither.
const maxSummaryBytes = 240

// OutputKind names one searchable surface of Babel's own analysis output.
//
// The first three are EntityType values and are spelled the same, because they
// are the same records. OutputReviewAnswer is not: an operator's §4.7 decision
// and the refinement it authorized are not records that develop, so they are
// not an entity kind — but they are the operator's own words about a record,
// and a run that is about to re-propose something a person already declined
// needs to be able to find that out.
type OutputKind string

// The searchable kinds.
const (
	OutputHypothesis   OutputKind = "hypothesis"
	OutputObservation  OutputKind = "observation"
	OutputFinding      OutputKind = "finding"
	OutputReviewAnswer OutputKind = "review-answer"
)

// OutputKinds lists every searchable kind, which a caller validating a
// requested filter needs and which nothing else should restate.
func OutputKinds() []OutputKind {
	return []OutputKind{OutputHypothesis, OutputObservation, OutputFinding, OutputReviewAnswer}
}

// ValidOutputKind reports whether kind is one this package produces.
func ValidOutputKind(kind OutputKind) bool {
	for _, known := range OutputKinds() {
		if kind == known {
			return true
		}
	}
	return false
}

// Output is one of Babel's own outputs, flattened for retrieval.
//
// Everything on it is either a structured identifier or text derived from a
// payload. There is no evidence, no locator and no grading: an Output is a
// pointer into the frontier for a reader who then opens the record, and a
// consumer that treated one as a claim would be reading a search result as
// analysis.
type Output struct {
	Kind OutputKind
	// ID is the record this output is. For a review answer it is the
	// disposition or refinement request's own id, and Subject names the
	// record it answers about.
	ID string
	// RootID is the chain identity, so two runs looking at the same
	// candidate under different wordings can tell they are the same
	// candidate. It is the record's own id for anything with no chain.
	RootID string
	// Subject is the record a review answer answers about, and the zero Ref
	// for everything else.
	Subject Ref
	// RunID is the run that produced the record, empty for anything an
	// operator wrote.
	RunID string
	// Status is a candidate's lifecycle state, empty for every other kind.
	// It travels because the whole point of #87's revive transition is that
	// a rejected candidate is still a candidate: a run must be able to see
	// that what it is about to mint exists and is at rest.
	Status    Status
	CreatedAt time.Time
	// Summary is the record in one bounded line.
	Summary string
	// Text is what a search matches against: everything the record says
	// that a reader would search for, in no particular order.
	Text string
}

// Outputs enumerates every searchable output the frontier currently holds:
// the head revision of each hypothesis, observation and finding chain, plus
// the review answers recorded against any record.
//
// It returns the complete set rather than a page. The consumer is an index
// reconciling itself against the durable store, and a page would let it
// mistake "not on this page" for "no longer a head" — which is a row it would
// delete. The set is bounded by the analysis Babel has produced, which is
// thousands of records rather than the millions of events the corpus index
// holds, so enumerating it costs one scan of a small table set.
//
// Proposals are deliberately absent. A proposal is §4.5's review artifact
// assembled from findings, so its text is the findings' text restated for a
// reviewer; indexing both would make every consolidated finding match twice
// and read as two independent prior ideas.
func (s *Store) Outputs(ctx context.Context) ([]Output, error) {
	var out []Output
	hypotheses, err := s.headOutputs(ctx, OutputHypothesis, "")
	if err != nil {
		return nil, err
	}
	out = append(out, hypotheses...)
	observations, err := s.headOutputs(ctx, OutputObservation, "")
	if err != nil {
		return nil, err
	}
	out = append(out, observations...)
	findings, err := s.headOutputs(ctx, OutputFinding, "")
	if err != nil {
		return nil, err
	}
	out = append(out, findings...)
	answers, err := s.reviewAnswers(ctx, Ref{}, "")
	if err != nil {
		return nil, err
	}
	return append(out, answers...), nil
}

// Output reads one searchable output by kind and id.
//
// It shares every derivation with Outputs so that the summary a job document
// lists and the text an index matched cannot disagree about the same record.
// Unlike Outputs it does not require the record to be a head: a caller holding
// an identifier from a stored preparation is entitled to read what that
// identifier names, and telling it "superseded" is the record's own business
// to say through its chain rather than this function's to hide.
func (s *Store) Output(ctx context.Context, kind OutputKind, id string) (Output, error) {
	if id == "" {
		return Output{}, fmt.Errorf("%w: output id is empty", ErrInvalidValue)
	}
	var found []Output
	var err error
	switch kind {
	case OutputHypothesis, OutputObservation, OutputFinding:
		found, err = s.headOutputs(ctx, kind, id)
	case OutputReviewAnswer:
		found, err = s.reviewAnswers(ctx, Ref{}, id)
	default:
		return Output{}, fmt.Errorf("%w: output kind %q", ErrInvalidValue, kind)
	}
	if err != nil {
		return Output{}, err
	}
	if len(found) == 0 {
		return Output{}, fmt.Errorf("%w: %s %q", ErrUnknownEntity, kind, id)
	}
	return found[0], nil
}

// headOutputs reads the head revisions of one record kind, or the single
// record named by id.
//
// "Head" is "nothing supersedes it", read off the revision chain rather than
// off the ancestor column. The two agree — appendRevision refuses a second
// descendant, so a chain has exactly one leaf — and the chain is the one that
// carries the identity a caller needs, so reading both would be reading the
// same fact twice and inviting them to disagree.
func (s *Store) headOutputs(ctx context.Context, kind OutputKind, id string) ([]Output, error) {
	table, err := tableFor(EntityType(kind))
	if err != nil {
		return nil, err
	}
	status := `''`
	if kind == OutputHypothesis {
		status = `COALESCE((SELECT e.status FROM frontier_status_event e
			WHERE e.hypothesis_id = r.id ORDER BY e.seq DESC LIMIT 1), '')`
	}
	where := `NOT EXISTS (SELECT 1 FROM frontier_revision s
			WHERE s.entity_type = ? AND s.supersedes_id = r.id)`
	args := []any{string(kind), string(kind)}
	if id != "" {
		// One record by id, head or not: see Output's contract.
		where = `r.id = ?`
		args = []any{string(kind), id}
	}
	query := `SELECT r.id, COALESCE(v.root_id, r.id), r.run_id, r.created_at, r.payload_json, ` + status + `
		FROM ` + table + ` r
		LEFT JOIN frontier_revision v ON v.entity_type = ? AND v.entity_id = r.id
		WHERE ` + where + `
		ORDER BY r.created_at, r.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read %s outputs: %w", kind, err)
	}
	defer rows.Close()
	var out []Output
	for rows.Next() {
		var (
			record  Output
			created string
			payload []byte
			state   string
		)
		if err := rows.Scan(&record.ID, &record.RootID, &record.RunID, &created, &payload, &state); err != nil {
			return nil, fmt.Errorf("read %s output: %w", kind, err)
		}
		record.Kind = kind
		record.Status = Status(state)
		at, err := parseTime(created)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", kind, record.ID, err)
		}
		record.CreatedAt = at
		if err := describePayload(&record, payload); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// describePayload fills in an output's Summary and Text from the record's
// stored payload.
//
// Each kind contributes what a later reader would search for and nothing that
// only a machine reads. Confidence, impact, novelty and priority are all
// absent: they are gradings, §10 warns they never substitute for evidence, and
// a term-frequency measure over "high" and "medium" would make every strongly
// graded record look related to every other one.
func describePayload(record *Output, payload []byte) error {
	var parts []string
	switch record.Kind {
	case OutputHypothesis:
		var p HypothesisPayload
		if err := decodePayload(record, payload, &p); err != nil {
			return err
		}
		record.Summary = summarize(p.Statement)
		parts = append(parts, p.Statement, strings.Join(p.OriginCues, " "),
			strings.Join(p.ProvisionalLabels, " "), p.Notes)
	case OutputObservation:
		var p ObservationPayload
		if err := decodePayload(record, payload, &p); err != nil {
			return err
		}
		record.Summary = summarize(p.Claim)
		parts = append(parts, p.Claim, p.Category)
		// The evidence notes are the model's words about what the cited
		// bytes show, which is exactly the phrasing a later run searching
		// for prior work on the same corpus would use. The locators
		// themselves stay out: a path is not subject matter, and §9 keeps
		// source paths out of anything that travels.
		for _, ev := range p.Evidence {
			parts = append(parts, ev.Note())
		}
		for _, ev := range p.CounterEvidence {
			parts = append(parts, ev.Note())
		}
	case OutputFinding:
		var p FindingPayload
		if err := decodePayload(record, payload, &p); err != nil {
			return err
		}
		record.Summary = summarize(p.Title)
		parts = append(parts, p.Title, p.Pattern, p.Significance, strings.Join(p.Scope, " "))
	default:
		return fmt.Errorf("%w: output kind %q", ErrInvalidValue, record.Kind)
	}
	record.Text = joinText(parts)
	return nil
}

func decodePayload(record *Output, payload []byte, into any) error {
	if err := unmarshalPayload(payload, into); err != nil {
		return fmt.Errorf("decode %s %s payload: %w", record.Kind, record.ID, err)
	}
	return nil
}

// reviewAnswers reads the operator's recorded answers about records: every
// §4.7 disposition and every refinement request a rejection authorized.
//
// Both are answers and both are indexed, because they say different things. A
// disposition is the decision and its note is why; a refinement request is
// what the reviewer asked for instead. A run that re-proposes what an operator
// declined, and a run that ignores the refinement that operator asked for, are
// two different failures, and neither is discoverable from the other's row.
func (s *Store) reviewAnswers(ctx context.Context, subject Ref, id string) ([]Output, error) {
	answers, err := s.dispositionAnswers(ctx, subject, id)
	if err != nil {
		return nil, err
	}
	refinements, err := s.refinementAnswers(ctx, subject, id)
	if err != nil {
		return nil, err
	}
	return append(answers, refinements...), nil
}

func (s *Store) dispositionAnswers(ctx context.Context, subject Ref, id string) ([]Output, error) {
	where := []string{"1 = 1"}
	var args []any
	if subject.ID != "" {
		where = append(where, `d.subject_type = ? AND d.subject_id = ?`)
		args = append(args, string(subject.Type), subject.ID)
	}
	if id != "" {
		where = append(where, `d.id = ?`)
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, d.subject_type, d.subject_id, d.disposition,
		d.reviewer_id, d.recorded_at, d.payload_json FROM frontier_disposition d
		WHERE `+strings.Join(where, " AND ")+` ORDER BY d.recorded_at, d.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read review answers: %w", err)
	}
	defer rows.Close()
	var out []Output
	for rows.Next() {
		var (
			record      Output
			subjectType string
			decision    string
			reviewer    string
			recorded    string
			payload     []byte
		)
		if err := rows.Scan(&record.ID, &subjectType, &record.Subject.ID, &decision,
			&reviewer, &recorded, &payload); err != nil {
			return nil, fmt.Errorf("read review answer: %w", err)
		}
		var note DispositionPayload
		if err := unmarshalPayload(payload, &note); err != nil {
			return nil, fmt.Errorf("decode review answer %s payload: %w", record.ID, err)
		}
		record.Kind = OutputReviewAnswer
		record.RootID = record.ID
		record.Subject.Type = EntityType(subjectType)
		at, err := parseTime(recorded)
		if err != nil {
			return nil, fmt.Errorf("review answer %s: %w", record.ID, err)
		}
		record.CreatedAt = at
		record.Summary = summarize(fmt.Sprintf("%s on %s %s: %s",
			decision, subjectType, record.Subject.ID, note.Note))
		// The reviewer identity is indexed with the words. §9's allowlist
		// admits it in the clear, internal/review already keeps reviewer
		// identities unsealed, and "what did this reviewer decide about
		// this area" is a question the frontier could not answer before.
		record.Text = joinText([]string{decision, reviewer, note.Note})
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) refinementAnswers(ctx context.Context, subject Ref, id string) ([]Output, error) {
	where := []string{"1 = 1"}
	var args []any
	if subject.ID != "" {
		where = append(where, `f.subject_type = ? AND f.subject_id = ?`)
		args = append(args, string(subject.Type), subject.ID)
	}
	if id != "" {
		where = append(where, `f.id = ?`)
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.subject_type, f.subject_id,
		f.created_at, f.payload_json FROM frontier_refinement_request f
		WHERE `+strings.Join(where, " AND ")+` ORDER BY f.created_at, f.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read refinement answers: %w", err)
	}
	defer rows.Close()
	var out []Output
	for rows.Next() {
		var (
			record      Output
			subjectType string
			created     string
			payload     []byte
		)
		if err := rows.Scan(&record.ID, &subjectType, &record.Subject.ID, &created, &payload); err != nil {
			return nil, fmt.Errorf("read refinement answer: %w", err)
		}
		var refinement RefinementPayload
		if err := unmarshalPayload(payload, &refinement); err != nil {
			return nil, fmt.Errorf("decode refinement answer %s payload: %w", record.ID, err)
		}
		record.Kind = OutputReviewAnswer
		record.RootID = record.ID
		record.Subject.Type = EntityType(subjectType)
		at, err := parseTime(created)
		if err != nil {
			return nil, fmt.Errorf("refinement answer %s: %w", record.ID, err)
		}
		record.CreatedAt = at
		record.Summary = summarize(fmt.Sprintf("refinement requested on %s %s: %s",
			subjectType, record.Subject.ID, refinement.Guidance))
		record.Text = joinText([]string{"refinement requested",
			refinement.Guidance, strings.Join(refinement.Scope, " ")})
		out = append(out, record)
	}
	return out, rows.Err()
}

// ReviewAnswers reads the recorded answers about one record, oldest first. It
// is the read a record page needs and the read a caller resolving a stored
// preparation reference needs, and it is the same derivation the index holds
// so the two cannot drift.
func (s *Store) ReviewAnswers(ctx context.Context, subject Ref) ([]Output, error) {
	if err := s.requireSubject(ctx, s.db, subject, false); err != nil {
		return nil, err
	}
	return s.reviewAnswers(ctx, subject, "")
}

// joinText assembles a record's searchable text, dropping the empty parts. The
// separator is a newline so that two fields' last and first words cannot
// tokenize into one term that neither field contains.
func joinText(parts []string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, "\n")
}

// summarize reduces one field to a bounded single line.
//
// A newline becomes a space rather than a truncation point: a statement whose
// first line is "The release pipeline" and whose second line carries the verb
// would otherwise be summarized into something that says nothing. The cut
// never splits a rune, because half a rune is invalid UTF-8 and would reach a
// JSON encoder as a substitution character in the middle of a record's own
// wording.
func summarize(text string) string {
	line := strings.Join(strings.Fields(text), " ")
	if len(line) <= maxSummaryBytes {
		return line
	}
	cut := maxSummaryBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return strings.TrimSpace(line[:cut]) + "…"
}

// unmarshalPayload decodes a stored payload. It is marshalPayload's inverse
// and exists for the same reason: one place for the later sync slice to
// unwrap an AEAD envelope.
func unmarshalPayload(payload []byte, into any) error {
	return json.Unmarshal(payload, into)
}
