package frontier

// The frontier as a time series: how many records of each kind were written on
// each day (SPEC.md §8.6's observatory register).
//
// It is a separate read from the enumerations beside it because it answers a
// different question with a different cost. Hypotheses, Findings and Proposals
// page records so a reader can open one, and they decode a payload per row;
// this counts rows and decodes nothing, which is what makes a ninety-day
// series one scan per kind instead of a corpus read. There is also no listing
// that could stand in for it: the store enumerates hypotheses, findings and
// proposals and never observations, so a count assembled from listings would
// silently omit the kind a run produces most.

import (
	"context"
	"fmt"
	"time"
)

// RecordDay is how many records of one kind were written on one UTC day.
//
// The day is a date string rather than a time because that is what it is: a
// bucket, taken from the stored timestamp's own UTC date, so no reader's clock
// or zone can move a record into a neighbouring day.
//
// Every revision counts on the day it was written. A record's chain is its
// history (§4.7), and an amendment is analysis somebody performed that day
// rather than a correction to an earlier count, so collapsing revisions into
// their root would report less work than the frontier holds.
type RecordDay struct {
	Day   string
	Kind  string
	Count int
}

// recordDayTables are the four record tables and the kind each one holds. The
// kinds are EntityType's own constants, so this series cannot name a kind the
// rest of the package does not.
var recordDayTables = []struct {
	table string
	kind  EntityType
}{
	{"frontier_hypothesis", EntityHypothesis},
	{"frontier_observation", EntityObservation},
	{"frontier_finding", EntityFinding},
	{"frontier_proposal", EntityProposal},
}

// RecordDays counts the records written on or after since, by UTC day and
// kind, oldest day first.
//
// A day on which a kind produced nothing is absent rather than zero: the
// caller knows which days it asked for, and a zero row per empty kind per day
// would be three quarters of a ninety-day answer. A kind that produced nothing
// at all in the window contributes no rows either, which is the same statement
// at a different scale.
//
// The bound is a text comparison against the stored column, which is exact
// rather than approximate here: formatTime writes UTC with a fixed nine-digit
// fraction, so the column's text order is its chronological order and the
// substring taken below is the record's own UTC date.
func (s *Store) RecordDays(ctx context.Context, since time.Time) ([]RecordDay, error) {
	from := formatTime(since)
	var out []RecordDay
	for _, t := range recordDayTables {
		// One query per table, and the table name is interpolated from the
		// fixed list above rather than taken from a caller: these are four
		// separate tables, so a single grouped query is not available
		// without a union that would still name each of them.
		query := `SELECT substr(created_at, 1, 10) AS day, count(*) FROM ` + t.table +
			` WHERE created_at >= ? GROUP BY day ORDER BY day`
		rows, err := s.db.QueryContext(ctx, query, from)
		if err != nil {
			return nil, fmt.Errorf("count %s records by day: %w", t.kind, err)
		}
		for rows.Next() {
			day := RecordDay{Kind: string(t.kind)}
			if err := rows.Scan(&day.Day, &day.Count); err != nil {
				rows.Close()
				return nil, fmt.Errorf("count %s records by day: %w", t.kind, err)
			}
			out = append(out, day)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("count %s records by day: %w", t.kind, err)
		}
	}
	return out, nil
}
