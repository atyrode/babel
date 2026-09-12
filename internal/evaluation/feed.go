package evaluation

// The deployment-wide reception read (SPEC.md §8.7).
//
// Everything else in this package answers about one subject, because that is
// what a review, a grant and an operator's decision are about. A feed is the
// other shape of the same question — what does every record stand at, right
// now — and asking it one subject at a time is how a front page comes to cost
// one query per row. §8.5 already fixed the rule this file keeps: a listing
// reads a bounded projection rather than fetching the whole corpus, and it
// ranks the complete eligible set before paging it.
//
// Two reads are here and both are deliberate in shape.
//
// Tallies is one grouped pass over the judgement-bearing records. It reads
// assessments and feedback and nothing else — an assignment, an attempt, a
// checkpoint and a policy carry no reception at all — so the cost is the votes
// and the comments rather than the store.
//
// Thread is one subject's records in commit order, read from the durable rows
// rather than from the projection. The projection is a snapshot of the
// evaluable inventory; a record it has not swept, or a subject kind it does
// not cover, is still a record with a conversation under it, and a comment
// thread that disappeared until the next sweep would be the cache deciding
// what was said.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Tally is one subject's reception as a feed counts it.
//
// The three vote columns are Babel's reviewers only, and Stance is the
// operator's own latest position beside them rather than folded into them.
// §4.12's boundary is why: a person never mints what reads as a model's
// observation, so the two stay separable at the point they are read, and a
// surface that wants §8.7's single number adds them itself and says which part
// was whose.
type Tally struct {
	Support int
	Oppose  int
	Unsure  int
	// Stance is the operator's newest reception - agree, disagree or unsure
	// - and is empty when he has recorded none. It is the newest *stance*
	// and not the newest feedback record, because §4.12 lets a scoped
	// reason carry no position at all and a later reason without one
	// withdraws nothing.
	Stance   string
	StanceAt time.Time
	// Comments counts the prose under the subject: a reviewer's
	// contribution text, an operator's reason, a reconsider item's reason
	// and the reason on a reconsideration decision. A bare vote is not a
	// comment and is not counted as one.
	Comments int
	// LastActivity is the newest of everything above, and is zero when
	// nothing has happened to the subject. The subject's own creation is
	// not here: this package did not observe it.
	LastActivity time.Time
	// Activity is when each vote and comment was recorded, which is what a
	// rising rank counts inside its window. It is the timestamps rather
	// than a count because the window moves and the projection that holds
	// this does not: a count bound to the moment of a rebuild would answer
	// a question about a minute ago.
	Activity []time.Time
}

// Tallies groups the deployment's reception by subject in one read.
//
// The dedup rule is §4.12's own and is applied here rather than left to a
// caller: one vote per run per role. A correction supersedes the statement it
// names, so a superseded record is dropped entirely; of what remains the
// newest record per (subject, run, role) is the one that counts, which is what
// makes a re-granted review a changed vote rather than a second one.
func (s *Store) Tallies(ctx context.Context) (map[Subject]Tally, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, subject_kind, subject_id, actor_kind,
		actor_id, role, supersedes_id, created_at, payload_json
		FROM evaluation_record
		WHERE kind IN (?, ?, ?, ?) AND subject_id <> ''
		ORDER BY created_at, seq`,
		KindAssessment, KindFeedback, KindReconsider, KindReconsiderDecision)
	if err != nil {
		return nil, fmt.Errorf("read evaluation tallies: %w", err)
	}
	defer rows.Close()
	var scanned []tallyRow
	superseded := map[string]struct{}{}
	for rows.Next() {
		var (
			row     tallyRow
			created string
			payload []byte
		)
		if err := rows.Scan(&row.id, &row.kind, &row.subject.Kind, &row.subject.ID, &row.actorKind,
			&row.actorID, &row.role, &row.supersedes, &created, &payload); err != nil {
			return nil, fmt.Errorf("scan evaluation tally: %w", err)
		}
		if row.at, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("evaluation record %s: %w", row.id, err)
		}
		record, err := Decode(payload)
		if err != nil {
			return nil, err
		}
		row.record = record
		if row.supersedes != "" {
			superseded[row.supersedes] = struct{}{}
		}
		scanned = append(scanned, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read evaluation tallies: %w", err)
	}
	return foldTallies(scanned, superseded), nil
}

// tallyRow is one judgement-bearing record as the grouped read scans it: the
// plaintext columns the grouping keys on, plus the decoded record the vote and
// the prose live in.
type tallyRow struct {
	id         string
	kind       string
	subject    Subject
	actorKind  string
	actorID    string
	role       string
	supersedes string
	at         time.Time
	record     Record
}

// foldTallies is the grouping itself, separated from the query so the rule is
// testable without a database and cannot differ between the two.
func foldTallies(rows []tallyRow, superseded map[string]struct{}) map[Subject]Tally {
	// votes holds the newest surviving vote per subject, run and role. The
	// rows arrive in commit order, so a later one simply replaces an
	// earlier one under the same key.
	type voteKey struct {
		subject Subject
		actor   string
		role    string
	}
	votes := map[voteKey]string{}
	out := map[Subject]Tally{}
	for _, row := range rows {
		if _, corrected := superseded[row.id]; corrected {
			continue
		}
		tally := out[row.subject]
		switch {
		case row.kind == KindAssessment && row.actorKind == ActorRun && row.record.Assessment != nil:
			if vote := row.record.Assessment.Vote; vote != "" {
				votes[voteKey{row.subject, row.actorID, row.role}] = vote
				tally.Activity = append(tally.Activity, row.at)
			}
			for _, contribution := range row.record.Assessment.Contributions {
				if strings.TrimSpace(contribution.Text) == "" {
					continue
				}
				tally.Comments++
				tally.Activity = append(tally.Activity, row.at)
			}
		case row.kind == KindFeedback && row.actorKind == ActorOperator:
			if row.record.Stance != "" {
				tally.Stance, tally.StanceAt = row.record.Stance, row.at
				tally.Activity = append(tally.Activity, row.at)
			}
			if strings.TrimSpace(row.record.Reason) != "" {
				tally.Comments++
				tally.Activity = append(tally.Activity, row.at)
			}
		case row.kind == KindReconsider || row.kind == KindReconsiderDecision:
			if strings.TrimSpace(row.record.Reason) != "" {
				tally.Comments++
				tally.Activity = append(tally.Activity, row.at)
			}
		default:
			continue
		}
		if row.at.After(tally.LastActivity) {
			tally.LastActivity = row.at
		}
		out[row.subject] = tally
	}
	for key, vote := range votes {
		tally := out[key.subject]
		switch vote {
		case VoteSupport:
			tally.Support++
		case VoteOppose:
			tally.Oppose++
		case VoteUnsure:
			tally.Unsure++
		}
		out[key.subject] = tally
	}
	return out
}

// ThreadRecord is one record of a subject's conversation with the review role
// its grant authorized beside it.
//
// The role travels with the record rather than being read off it because that
// is where §4.12 puts it: a role is what a reviewer was authorized to answer,
// not something the answer claims about itself. A record whose grant this
// instance never saw carries no role, which is the honest absence — crediting
// it to reception by default is how a bare vote comes to look like a satisfied
// evidence check.
type ThreadRecord struct {
	Record Record
	Role   string
}

// Thread reads every record about one subject in commit order.
//
// It is Events narrowed to a subject rather than Detail without a projection:
// the caller is assembling the conversation under one record, and the
// assignments, coverage and alternatives Detail carries are a different
// question with a different cost.
func (s *Store) Thread(ctx context.Context, subject Subject) ([]ThreadRecord, error) {
	if err := subject.validate(); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT role, payload_json FROM evaluation_record
		WHERE subject_kind = ? AND subject_id = ? ORDER BY created_at, seq`,
		subject.Kind, subject.ID)
	if err != nil {
		return nil, fmt.Errorf("read evaluation thread: %w", err)
	}
	defer rows.Close()
	var records []ThreadRecord
	for rows.Next() {
		var (
			role    string
			payload []byte
		)
		if err := rows.Scan(&role, &payload); err != nil {
			return nil, fmt.Errorf("scan evaluation thread: %w", err)
		}
		record, err := Decode(payload)
		if err != nil {
			return nil, err
		}
		records = append(records, ThreadRecord{Record: record, Role: role})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read evaluation thread: %w", err)
	}
	return records, nil
}

// Tallies groups the deployment's reception by subject, for a surface that
// ranks every record at once.
func (s *Service) Tallies(ctx context.Context) (map[Subject]Tally, error) {
	return s.store.Tallies(ctx)
}

// Thread reads one subject's records in commit order, for a surface rendering
// the conversation under it.
func (s *Service) Thread(ctx context.Context, subject Subject) ([]ThreadRecord, error) {
	return s.store.Thread(ctx, subject)
}
