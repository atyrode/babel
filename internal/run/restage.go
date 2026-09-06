package run

import (
	"context"
	"fmt"

	"github.com/atyrode/babel/internal/sync"
)

// Restage recovers publication records written without a staging hook. It never
// changes the preparation or receipt, declares a producing run, or publishes.
// Each committed record is a checkpoint: an interrupted pass can be repeated.
func (s *Store) Restage(ctx context.Context) (int, error) {
	if s.sync == nil {
		return 0, fmt.Errorf("run: restage requires a publication hook")
	}
	count := 0
	for _, table := range []string{"run_preparation", "run_receipt"} {
		ids, err := sync.Missing(ctx, s.db, table, "id")
		if err != nil {
			return count, err
		}
		for _, id := range ids {
			added, err := s.restageRecord(ctx, table, id)
			if err != nil {
				return count, fmt.Errorf("run: restage %s: %w", id, err)
			}
			if added {
				count++
			}
		}
	}
	return count, nil
}

func (s *Store) restageRecord(ctx context.Context, table, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sync_record WHERE record_id = ?)`, id).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if table == "run_preparation" {
		var payload []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM run_preparation WHERE id = ?`, id).Scan(&payload); err != nil {
			return false, err
		}
		p, err := UnmarshalPreparation(payload)
		if err != nil {
			return false, err
		}
		if _, _, err := s.stagePreparation(ctx, tx, p, payload); err != nil {
			return false, err
		}
	} else {
		r, err := decodeReceipt(tx.QueryRowContext(ctx, receiptColumns+` WHERE id = ?`, id))
		if err != nil {
			return false, err
		}
		var payload []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM run_receipt WHERE id = ?`, id).Scan(&payload); err != nil {
			return false, err
		}
		if _, _, err := s.stageReceipt(ctx, tx, r.Header, payload); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

