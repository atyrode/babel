package evaluation

// Evaluation as a time series: how many assessments were recorded on each day.
//
// It is a count over the durable records rather than over the projection,
// because the projection answers what a subject's reception currently is and
// this answers how much reviewing happened. The two cannot be derived from
// each other: a subject reviewed three times is one row in the projection, and
// a subject whose reviews were all superseded still holds the days they were
// performed on.

import (
	"context"
	"fmt"
	"time"
)

// AssessmentDay is how many assessments were recorded on one UTC day.
//
// The day is a date string taken from the stored timestamp's own UTC date, so
// no reader's zone can move a record into a neighbouring day. A day with no
// assessment is absent rather than zero, for the reason frontier's record
// series gives: the caller knows which days it asked about.
type AssessmentDay struct {
	Day   string
	Count int
}

// AssessmentDays counts the assessments recorded on or after since, oldest day
// first.
//
// Assessments and nothing else. The store holds the operator's own criteria,
// feedback and reconsideration decisions beside them (§4.12), and those are
// acts of steering rather than reviews performed — counting them here would
// make an afternoon of saying "not now" read as an afternoon of review.
//
// The read is one query against the (kind, created_at) index, and the bound is
// a text comparison against the stored column: formatTime writes UTC with a
// fixed nine-digit fraction, so text order is chronological order and the
// substring below is the record's own UTC date.
func (s *Store) AssessmentDays(ctx context.Context, since time.Time) ([]AssessmentDay, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT substr(created_at, 1, 10) AS day, count(*) FROM evaluation_record
		 WHERE kind = ? AND created_at >= ? GROUP BY day ORDER BY day`,
		KindAssessment, formatTime(since))
	if err != nil {
		return nil, fmt.Errorf("count assessments by day: %w", err)
	}
	defer rows.Close()
	var out []AssessmentDay
	for rows.Next() {
		var day AssessmentDay
		if err := rows.Scan(&day.Day, &day.Count); err != nil {
			return nil, fmt.Errorf("count assessments by day: %w", err)
		}
		out = append(out, day)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count assessments by day: %w", err)
	}
	return out, nil
}

// AssessmentDays answers the same question through the service, which is the
// surface every reader outside this package holds: the store is opened by the
// wiring site and handed to NewService, and a caller that had to be given both
// would be holding the durable writer in order to read a count.
func (s *Service) AssessmentDays(ctx context.Context, since time.Time) ([]AssessmentDay, error) {
	return s.store.AssessmentDays(ctx, since)
}
