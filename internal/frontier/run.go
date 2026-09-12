package frontier

// This file answers one question the frontier has always been able to answer
// and never been asked: what did this run write?
//
// Every record carries the run that emitted it, and until now that identity
// only travelled outward — a receipt names its run, a record names its run,
// and a reader holding one record had no way back to the rest of the same
// thought. A run is the unit of work that produced a candidate, the
// observations that developed it and the finding that consolidated them, so
// the records of one run are siblings in the strong sense: they were written
// by one pass over one scope, and reading one of them without the others is
// reading a paragraph of a page.
//
// It is deliberately not internal/frontier/outputs.go's Output. That type is
// the retrieval index's row and its kind vocabulary is the index's, which
// excludes proposals on purpose (a proposal restates its findings, and
// indexing both would make one idea match twice). A run's outputs include its
// proposals, because a remedy is exactly what a reader of the finding beside
// it wants next. So this is a second, smaller projection with its own type
// rather than a fifth kind on the index's.

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// RunOutput is one record a run wrote, as a reader asking "what else came out
// of this" needs it: which record it is, what kind of thing it is, and its own
// one line.
//
// The line is bounded by the index's own summary bound, for the reason a link
// row is: this is a list of other records, so a member of it must be the
// height of a row rather than the height of a claim.
type RunOutput struct {
	Kind EntityType
	ID   string
	// RootID is the chain identity, so a reader can tell that two rows are
	// two wordings of one record rather than two records.
	RootID    string
	Title     string
	CreatedAt time.Time
}

// OutputsOfRun lists the head revisions one run wrote, newest first.
//
// Head revisions only, on Outputs' terms: a superseded wording is not what the
// frontier says now, and a siblings list that carried both wordings would
// offer a reader the same record twice and let him rule on the older one.
//
// A run's separate jobs are the same run's output. SPEC.md §5.4 makes the
// challenger and the synthesizer logically separate jobs with their own run
// identity, and internal/explore spells those identities `<run>/<stage>`, so
// the records of one exploration are stored under the run's own id and under
// each stage's. A query for the bare id alone answered for the candidates and
// left out the finding the synthesizer wrote — on the live catalog, 69 of 179
// findings — and the run page read that as a run that had published nothing.
// internal/sync makes the same reduction in the other direction, for the same
// reason it holds here: a stage is a job within a run, not a run.
//
// An empty run id answers nothing rather than everything. Every record row
// requires a run, so the empty string names no run at all — it is what a
// caller passes when it could not read the producing run — and a query on it
// would return the rows of whatever a future write path leaves blank.
func (s *Store) OutputsOfRun(ctx context.Context, runID string) ([]RunOutput, error) {
	if runID == "" {
		return nil, nil
	}
	var out []RunOutput
	for _, kind := range []EntityType{EntityHypothesis, EntityObservation, EntityFinding, EntityProposal} {
		rows, err := s.runOutputs(ctx, kind, runID)
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
	}
	sortRunOutputs(out)
	return out, nil
}

// runOutputs reads one kind's head revisions for a run.
//
// The predicate is the revision chain's rather than the ancestor column's,
// which is headOutputs' judgement and holds for the same reason: the chain is
// where supersession is asserted, and reading the same fact from two places is
// how the two come to disagree. The run_id filter and the stage range are both
// what migration 7's index serves.
func (s *Store) runOutputs(ctx context.Context, kind EntityType, runID string) ([]RunOutput, error) {
	table, err := tableFor(kind)
	if err != nil {
		return nil, err
	}
	from, to := stageBounds(runID)
	query := `SELECT r.id, COALESCE(v.root_id, r.id), r.created_at, r.payload_json
		FROM ` + table + ` r
		LEFT JOIN frontier_revision v ON v.entity_type = ? AND v.entity_id = r.id
		WHERE (r.run_id = ? OR (r.run_id >= ? AND r.run_id < ?))
			AND NOT EXISTS (SELECT 1 FROM frontier_revision s
				WHERE s.entity_type = ? AND s.supersedes_id = r.id)
		ORDER BY r.created_at, r.id`
	rows, err := s.db.QueryContext(ctx, query, string(kind), runID, from, to, string(kind))
	if err != nil {
		return nil, fmt.Errorf("read %s records of run %s: %w", kind, runID, err)
	}
	defer rows.Close()
	var out []RunOutput
	for rows.Next() {
		var (
			record  = RunOutput{Kind: kind}
			created string
			payload []byte
		)
		if err := rows.Scan(&record.ID, &record.RootID, &created, &payload); err != nil {
			return nil, fmt.Errorf("read %s record of run %s: %w", kind, runID, err)
		}
		at, err := parseTime(created)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", kind, record.ID, err)
		}
		record.CreatedAt = at
		if record.Title, err = runOutputTitle(kind, record.ID, payload); err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// stageSeparator is what internal/explore puts between a run and one of its
// separate jobs. It is the frontier's business because the frontier stores
// the compound identity: a challenger's records name `<run>/challenge` in the
// column every other read filters on.
const stageSeparator = '/'

// stageBounds brackets the run ids of one run's separate stages: every value
// that begins with `<run>/`, as the half-open range a B-tree answers with one
// seek. The upper bound is the separator's successor, which is the exclusive
// end of any prefix range.
//
// It is a range rather than a pattern because the match has to be exact as
// well as indexed. A run id is whatever its writer minted — the store requires
// only that it is not empty — so a LIKE pattern would read an underscore in
// one as a wildcard and a GLOB pattern would read an asterisk as one, and
// either would answer with another run's records.
func stageBounds(runID string) (from, to string) {
	return runID + string(rune(stageSeparator)), runID + string(rune(stageSeparator+1))
}

// runOutputTitle is the record's own line, per kind.
//
// Each kind's line is the one its own page leads with, so a siblings row and
// the record it points at say the same thing: a candidate is its statement, an
// observation is its claim, and a finding and a proposal wrote themselves a
// title.
func runOutputTitle(kind EntityType, id string, payload []byte) (string, error) {
	switch kind {
	case EntityHypothesis:
		var decoded HypothesisPayload
		if err := unmarshalPayload(payload, &decoded); err != nil {
			return "", fmt.Errorf("decode hypothesis %s payload: %w", id, err)
		}
		return summarize(decoded.Statement), nil
	case EntityObservation:
		var decoded ObservationPayload
		if err := unmarshalPayload(payload, &decoded); err != nil {
			return "", fmt.Errorf("decode observation %s payload: %w", id, err)
		}
		return summarize(decoded.Claim), nil
	case EntityFinding:
		var decoded FindingPayload
		if err := unmarshalPayload(payload, &decoded); err != nil {
			return "", fmt.Errorf("decode finding %s payload: %w", id, err)
		}
		return summarize(decoded.Title), nil
	case EntityProposal:
		var decoded ProposalPayload
		if err := unmarshalPayload(payload, &decoded); err != nil {
			return "", fmt.Errorf("decode proposal %s payload: %w", id, err)
		}
		return summarize(decoded.Title), nil
	}
	return "", fmt.Errorf("%w: entity type %q", ErrInvalidValue, kind)
}

// sortRunOutputs puts the newest record first and breaks ties on the id, so
// the order is total and the same on every read.
//
// Newest first because the four queries answer per kind and a reader is not
// reading a taxonomy: what a run wrote is one sequence of work, and the last
// thing it said is the thing its earlier records were building towards.
func sortRunOutputs(out []RunOutput) {
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
}
