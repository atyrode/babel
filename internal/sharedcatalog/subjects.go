package sharedcatalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// This file is the shared catalog's half of a proposal's provenance
// (issue #114, migrations/0010): the plaintext list of what one proposal rests
// on or addresses, on its way out, and the fleet read that lets a host with no
// payload key tell a want from a finding-backed conclusion.
//
// It exists because `analysis_records` is deliberately generic. A record row
// carries an identity, a kind, a schema, a closure position and the reference to
// the sealed object; it carries no relationship column, so `kind = 'proposal'`
// is the same string for both forms #114 makes lawful. A consolidated proposal
// rests on findings and derives its hypotheses through them (SPEC.md §4.5); a
// candidate proposal addresses hypotheses directly, rests on no finding at all,
// and is a want rather than a verified fact. Rendering the second with the
// authority of the first is the failure #114 exists to prevent, and a surface
// that cannot ask which it is holding commits that failure by default.
//
// The citation graph is not the answer. internal/frontier mints an `addresses`
// edge after the record's transaction has committed and treats a failure to
// mint one as a warning by explicit design, so the graph is a best-effort
// shadow and a missing edge would make a finding-backed proposal look unbacked.
// A record's form is a property of the record, so it travels in the record's own
// transaction; migrations/0010's header carries the whole argument.

// SubjectKind names which frontier store a proposal's subject id belongs to.
//
// The vocabulary is closed and matches migrations/0010's CHECK exactly: a
// proposal rests on findings and addresses hypotheses, and a third kind reaching
// PostgreSQL would assert a lineage nothing in the frontier defines, so it costs
// a migration and a review rather than being a string a caller invents.
type SubjectKind string

const (
	// SubjectHypothesis is what a candidate proposal addresses directly, and
	// what a consolidated proposal reaches transitively through its findings.
	SubjectHypothesis SubjectKind = "hypothesis"
	// SubjectFinding is what a consolidated proposal rests on. Its presence in
	// a record's subject list is what makes that record consolidated rather
	// than a candidate, which is why the form is derived from these rows and
	// never stored beside them: a stored form is a second answer, and a second
	// answer can disagree with the first.
	SubjectFinding SubjectKind = "finding"
)

// Valid reports whether k is one of the kinds migrations/0010 admits. It is
// exported for the reason EdgeKind.Valid is: the writer that stages a subject
// has to be able to ask before the database answers with a constraint
// violation.
func (k SubjectKind) Valid() bool {
	switch k {
	case SubjectHypothesis, SubjectFinding:
		return true
	}
	return false
}

// RecordSubject is one entry in a proposal's provenance: which store, and which
// record in it.
//
// It travels beside a StagedRecord rather than inside its payload on SPEC.md
// §9's terms - a relationship ID is plaintext-eligible - and it carries nothing
// else. The proposal's claim, its rationale and its confidence are content and
// stay in the sealed object, so a reader learns that a proposal addresses a
// hypothesis without learning a word of what either of them says.
type RecordSubject struct {
	Kind SubjectKind
	// ID is the subject's own durable, client-generated Phase B record id: the
	// identifier its store minted, which is what `analysis_records` is keyed
	// by. It says nothing about what that record contains.
	ID string
}

// Validate refuses at stage time what migrations/0010 would refuse at publish
// time, and one thing more: an id that is not a well-formed Phase B identifier.
//
// The reasoning is RecordEdge.Validate's. A staged subject PostgreSQL will
// reject is a journal row that can never publish, which is worse than a refused
// write because the refusal is visible and the permanently pending row is not.
func (s RecordSubject) Validate() error {
	if !s.Kind.Valid() {
		return fmt.Errorf("proposal subject kind %q is not one the shared catalog carries", string(s.Kind))
	}
	if !validRecordID.MatchString(s.ID) {
		return fmt.Errorf("proposal subject id must match %s", validRecordID)
	}
	return nil
}

// String renders the subject for a diagnostic, in the same "namespace:id" form
// internal/reference and RecordEdge use.
func (s RecordSubject) String() string { return string(s.Kind) + ":" + s.ID }

// commitSubjects writes a proposal's provenance rows inside the transaction that
// is inserting the record row they belong to.
//
// The shared transaction is commitEdge's reasoning with one difference that
// makes it stronger here. A record row without its subject rows would be a
// proposal whose form no reader can determine and no retry will supply - SyncRun
// skips a record the catalog already holds, so the missing rows would never be
// reconsidered - and where a missing edge leaves a citation merely absent, a
// missing subject list leaves a finding-backed proposal indistinguishable from
// an unbacked want.
//
// The ordinal is the slice index, so the order the producer asserted its
// subjects in is the order the catalog holds them in. ON CONFLICT covers the
// same narrow race commitRecord's own insert does: another instance that
// committed this record between the presence check and here has already written
// these rows, and they are immutable, so ours are redundant rather than
// contradictory.
func commitSubjects(ctx context.Context, tx *sql.Tx, recordID string, subjects []RecordSubject) error {
	for i, subject := range subjects {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO analysis_proposal_subjects (record_id, position, subject_kind, subject_id)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (record_id, position) DO NOTHING`,
			recordID, i, string(subject.Kind), subject.ID); err != nil {
			return fmt.Errorf("record proposal subject %d for %s: %w", i, recordID, err)
		}
	}
	return nil
}

// subjectColumns is the projection the fleet subject read selects. Every name is
// an allowlisted plaintext column, and SubjectProjection is what lets the Phase
// B gate check that claim against the query rather than against this comment.
//
// position is ordered by and not selected. Its value has no reader - the slice
// index the caller gets back is the position - so carrying it would be a column
// scanned into a variable nothing looks at.
const subjectColumns = `s.record_id, s.subject_kind, s.subject_id`

// SubjectProjection reports the plaintext columns the fleet subject read
// selects, as "table.column" pairs, so the Phase B plaintext gate can be
// pointed at what this file actually reads.
//
// It exists for RecordProjection's and EdgeProjection's reason: a projection
// that grew a content column would pass every query test in this package and
// fail this one.
func SubjectProjection() map[string][]string {
	return map[string][]string{
		"analysis_proposal_subjects": {
			"record_id", "subject_kind", "subject_id",
		},
	}
}

// RecordSubjects maps every named record id to its subjects, in the order they
// were asserted, omitting ids that have none.
//
// This is what makes migrations/0010 worth its columns: a host holding only the
// catalog credential can ask, of a page of proposals, which of them rest on
// findings and which are unbacked wants addressing a hypothesis, without opening
// a single object.
//
// An omitted id means one of two things, and a caller must not conflate them. A
// proposal absent here was written before #114 and says nothing about its form:
// the operator scoped #114 to new output only, and nothing back-fills a
// historical record, because guessing a proposal's provenance is precisely what
// this table exists to stop. Any other record is absent because subjects are
// proposal vocabulary and it has none. Either way the honest reading of absence
// is "unknown", never "candidate".
//
// An empty ids slice queries nothing and returns an empty map, on
// RecordSyncStates' terms: a caller with nothing to ask about must not
// accidentally read the whole deployment.
func RecordSubjects(ctx context.Context, db *sql.DB, recordIDs []string) (map[string][]RecordSubject, error) {
	subjects := make(map[string][]RecordSubject, len(recordIDs))
	if len(recordIDs) == 0 {
		return subjects, nil
	}
	where, args, _ := appendAnyOf(nil, nil, 1, `s.record_id`, recordIDs)
	// Ordering by position is what makes "the finding this proposal was
	// consolidated from" a stable answer rather than whatever the planner
	// returned; the record id joins it so the whole result is totally ordered.
	rows, err := db.QueryContext(ctx, `
		SELECT `+subjectColumns+`
		  FROM analysis_proposal_subjects s
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY s.record_id, s.position`, args...)
	if err != nil {
		return nil, fmt.Errorf("read proposal subjects: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			recordID string
			kind     string
			subject  RecordSubject
		)
		if err := rows.Scan(&recordID, &kind, &subject.ID); err != nil {
			return nil, fmt.Errorf("scan proposal subject: %w", err)
		}
		subject.Kind = SubjectKind(kind)
		subjects[recordID] = append(subjects[recordID], subject)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read proposal subjects: %w", err)
	}
	return subjects, nil
}
