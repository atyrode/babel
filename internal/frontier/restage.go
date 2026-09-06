package frontier

import (
	"context"
	"database/sql"
	"fmt"

	babelsync "github.com/atyrode/babel/internal/sync"
)

// Restage recovers canonical local publications missing from the sync journal.
// It never publishes or changes frontier records. Each record (or rejection and
// refinement pair) commits independently, so an interrupted pass is resumable.
func (s *Store) Restage(ctx context.Context) (int, error) {
	if s.sync == nil {
		return 0, fmt.Errorf("restage frontier: sync hook is required")
	}
	count := 0
	for _, kind := range []EntityType{EntityHypothesis, EntityObservation, EntityFinding, EntityProposal} {
		table, err := tableFor(kind)
		if err != nil {
			return count, err
		}
		ids, err := babelsync.Missing(ctx, s.db, table, "id")
		if err != nil {
			return count, err
		}
		for _, id := range ids {
			added := 0
			err := s.transact(ctx, func(tx *sql.Tx) error {
				missing, err := frontierUnstaged(ctx, tx, id)
				if err != nil || !missing {
					return err
				}
				rec, runID, err := restagedEntity(ctx, tx, kind, table, id)
				if err != nil {
					return err
				}
				if _, err := s.stage(ctx, tx, runID, rec); err != nil {
					return err
				}
				added = 1
				return nil
			})
			if err != nil {
				return count, fmt.Errorf("restage frontier %s %s: %w", kind, id, err)
			}
			count += added
		}
	}
	ids, err := babelsync.Missing(ctx, s.db, "frontier_hypothesis_link", "id")
	if err != nil {
		return count, err
	}
	for _, id := range ids {
		added := 0
		err := s.transact(ctx, func(tx *sql.Tx) error {
			missing, err := frontierUnstaged(ctx, tx, id)
			if err != nil || !missing {
				return err
			}
			var link Link
			var created string
			var payload []byte
			if err := tx.QueryRowContext(ctx, `SELECT id, from_id, to_id, link_type, created_at, payload_json
				FROM frontier_hypothesis_link WHERE id = ?`, id).Scan(
				&link.ID, &link.FromID, &link.ToID, &link.Type, &created, &payload); err != nil {
				return err
			}
			if link.CreatedAt, err = parseTime(created); err != nil {
				return err
			}
			rec, err := stagedLink(link, payload)
			if err != nil {
				return err
			}
			if _, err := s.stage(ctx, tx, "", rec); err != nil {
				return err
			}
			added = 1
			return nil
		})
		if err != nil {
			return count, fmt.Errorf("restage frontier link %s: %w", id, err)
		}
		count += added
	}
	// Select the owner once even if only the refinement is missing. Close the
	// cursor before transactions: durable handles have one SQLite connection.
	ids, err = queryIDs(ctx, s.db, `SELECT d.id FROM frontier_disposition d
		WHERE NOT EXISTS (SELECT 1 FROM sync_record s WHERE s.record_id = d.id)
		OR EXISTS (SELECT 1 FROM frontier_refinement_request r WHERE r.disposition_id = d.id
			AND NOT EXISTS (SELECT 1 FROM sync_record s WHERE s.record_id = r.id))
		ORDER BY d.recorded_at, d.id`)
	if err != nil {
		return count, err
	}
	for _, id := range ids {
		added := 0
		err := s.transact(ctx, func(tx *sql.Tx) error {
			var err error
			added, err = s.restageReview(ctx, tx, id)
			return err
		})
		if err != nil {
			return count, fmt.Errorf("restage frontier review %s: %w", id, err)
		}
		count += added
	}
	return count, nil
}

func frontierUnstaged(ctx context.Context, tx *sql.Tx, id string) (bool, error) {
	var missing bool
	err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM sync_record WHERE record_id = ?)`, id).Scan(&missing)
	return missing, err
}

func restagedEntity(ctx context.Context, tx *sql.Tx, kind EntityType, table, id string) (babelsync.Record, string, error) {
	var ancestor, runID, created, root string
	var payload []byte
	// table comes only from tableFor's closed vocabulary. Keep the original
	// payload bytes, not the public readers' decoded and enriched projections.
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(e.ancestor_id, ''), e.run_id, e.created_at, e.payload_json, r.root_id
		FROM `+table+` e JOIN frontier_revision r ON r.entity_id = e.id AND r.entity_type = ?
		WHERE e.id = ?`, string(kind), id).Scan(&ancestor, &runID, &created, &payload, &root)
	if err != nil {
		return babelsync.Record{}, "", err
	}
	at, err := parseTime(created)
	if err != nil {
		return babelsync.Record{}, "", err
	}
	var rec babelsync.Record
	switch kind {
	case EntityHypothesis:
		var status Status
		// Later status events were not part of the immutable publication.
		err = tx.QueryRowContext(ctx, `SELECT status FROM frontier_status_event WHERE hypothesis_id = ? ORDER BY seq LIMIT 1`, id).Scan(&status)
		if err == nil {
			rec, err = stagedHypothesis(Hypothesis{ID: id, AncestorID: ancestor, RunID: runID, CreatedAt: at, Status: status}, root, payload)
		}
	case EntityObservation:
		rec, err = stagedObservation(Observation{ID: id, AncestorID: ancestor, RunID: runID, CreatedAt: at}, root, payload)
	case EntityFinding:
		rec, err = stagedFinding(Finding{ID: id, AncestorID: ancestor, RunID: runID, CreatedAt: at}, root, payload)
	case EntityProposal:
		p := Proposal{ID: id, AncestorID: ancestor, RunID: runID, CreatedAt: at}
		p.FindingIDs, err = queryIDs(ctx, tx, `SELECT finding_id FROM frontier_proposal_finding WHERE proposal_id = ? ORDER BY position`, id)
		if err != nil {
			break
		}
		p.Form = proposalForm(p.FindingIDs)
		if p.Form == ProposalCandidate {
			p.HypothesisIDs, err = proposalHypotheses(ctx, tx, id)
			if err != nil {
				break
			}
		}
		rec, err = stagedProposal(p, root, payload)
	}
	return rec, runID, err
}

func (s *Store) restageReview(ctx context.Context, tx *sql.Tx, id string) (int, error) {
	var event DispositionEvent
	var recorded string
	var payload []byte
	if err := tx.QueryRowContext(ctx, `SELECT id, subject_type, subject_id, disposition, reviewer_id, recorded_at, payload_json
		FROM frontier_disposition WHERE id = ?`, id).Scan(&event.ID, &event.Subject.Type, &event.Subject.ID,
		&event.Disposition, &event.ReviewerID, &recorded, &payload); err != nil {
		return 0, err
	}
	var err error
	if event.RecordedAt, err = parseTime(recorded); err != nil {
		return 0, err
	}
	requests, err := queryIDs(ctx, tx, `SELECT id FROM frontier_refinement_request WHERE disposition_id = ?`, id)
	if err != nil {
		return 0, err
	}
	count := 0
	if missing, err := frontierUnstaged(ctx, tx, id); err != nil {
		return 0, err
	} else if missing {
		rec, err := stagedDisposition(event, payload)
		if err != nil {
			return 0, err
		}
		if len(requests) == 0 {
			_, err = s.stage(ctx, tx, "", rec)
		} else {
			rec.RunID = id
			err = s.sync.StageTx(ctx, tx, rec)
		}
		if err != nil {
			return 0, err
		}
		count++
	}
	for _, requestID := range requests {
		missing, err := frontierUnstaged(ctx, tx, requestID)
		if err != nil {
			return 0, err
		}
		if !missing {
			continue
		}
		var request RefinementRequest
		if err := tx.QueryRowContext(ctx, `SELECT id, subject_type, subject_id, created_at, payload_json
			FROM frontier_refinement_request WHERE id = ?`, requestID).Scan(&request.ID, &request.Subject.Type,
			&request.Subject.ID, &recorded, &payload); err != nil {
			return 0, err
		}
		if request.CreatedAt, err = parseTime(recorded); err != nil {
			return 0, err
		}
		rec, err := stagedRefinement(request, event.ReviewerID, payload)
		if err != nil {
			return 0, err
		}
		rec.RunID = id
		if err := s.sync.StageTx(ctx, tx, rec); err != nil {
			return 0, err
		}
		count++
	}
	if len(requests) > 0 && count > 0 {
		if err := s.sync.DeclareTx(ctx, tx, babelsync.Closure{RunID: id}); err != nil {
			return 0, err
		}
	}
	return count, nil
}
