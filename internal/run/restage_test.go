package run

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/atyrode/babel/internal/sync"
)

type interruptedRestage struct {
	*sync.Stager
	remaining int
}

func (h *interruptedRestage) Append(ctx context.Context, tx *sql.Tx, producedBy string, rec sync.Record) (sync.Closure, bool, error) {
	if h.remaining == 0 {
		return sync.Closure{}, false, errors.New("injected recovery interruption")
	}
	h.remaining--
	return h.Stager.Append(ctx, tx, producedBy, rec)
}

func TestRestageResumesWithoutChangingCanonicalRecordsOrDeclaringActiveRuns(t *testing.T) {
	ctx := t.Context()
	s := testStore(t)
	r := mustReceipt(t)
	if err := s.PutReceipt(ctx, r); err != nil {
		t.Fatal(err)
	}
	// Historical local markers are not the journal: recovery must include
	// this receipt without rewriting its source row to make it look pending.
	if err := s.MarkReceiptCommitted(ctx, r.Header.ID); err != nil {
		t.Fatal(err)
	}
	// The live encoder is the oracle, not a separately maintained JSON shape.
	capture := &fakeHook{}
	live := syncStore(t, capture)
	if err := live.PutPreparation(ctx, r.Preparation); err != nil {
		t.Fatal(err)
	}
	if err := live.PutReceipt(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := sync.EnsureSchema(s.db); err != nil {
		t.Fatal(err)
	}
	s.sync = &interruptedRestage{Stager: sync.NewStager(), remaining: 1}
	if n, err := s.Restage(ctx); n != 1 || err == nil {
		t.Fatalf("interrupted recovery = %d, %v; want one durable checkpoint and error", n, err)
	}
	s.sync = sync.NewStager()
	if n, err := s.Restage(ctx); n != 1 || err != nil {
		t.Fatalf("resumed recovery = %d, %v; want remaining receipt", n, err)
	}
	if n, err := s.Restage(ctx); n != 0 || err != nil {
		t.Fatalf("repeated recovery = %d, %v", n, err)
	}
	for _, want := range capture.appended {
		var payload []byte
		if err := s.db.QueryRowContext(ctx, `SELECT payload FROM sync_payload WHERE record_id = ?`, want.record.EntityID).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(payload, want.record.Payload) {
			t.Fatalf("recovery changed canonical bytes for %s", want.record.EntityID)
		}
	}
	// Output in an active run is staged but a receipt is the only evidence
	// permitting the run to be declared. Recovery must not infer completion.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, _, err := s.sync.Append(ctx, tx, "active-run", sync.Record{
		EntityID: "active-output", Kind: sharedcatalog.KindHypothesis, Schema: 1, Payload: []byte(`{"active":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	declared, skipped, err := s.DeclareFinished(ctx, s.sync)
	if err != nil || len(skipped) != 0 || len(declared) != 1 || declared[0] != r.Header.RunID {
		t.Fatalf("finished declaration = %v, %v, %v", declared, skipped, err)
	}
	// Undeclared runs have staged records but no sync_run declaration row;
	// record_count is never a nullable placeholder for an active run.
	var activeRecords int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sync_record r
		WHERE r.record_id = ? AND r.run_id = ? AND r.sync_state = ?
		AND NOT EXISTS (SELECT 1 FROM sync_run u WHERE u.run_id = r.run_id)`,
		"active-output", "active-run", sharedcatalog.SyncPending).Scan(&activeRecords); err != nil {
		t.Fatal(err)
	}
	if activeRecords != 1 {
		t.Fatal("active unreceipted output is not pending in an undeclared run")
	}
	var receipts int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM run_receipt`).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("immutable receipts = %d, %v", receipts, err)
	}
	if state, err := s.SyncState(ctx, r.Header.ID); err != nil || state != SyncCommitted {
		t.Fatalf("recovery changed historical receipt state: %q, %v", state, err)
	}
}
