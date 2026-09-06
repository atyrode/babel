package complaint

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/sync"
)

type interruptedRestageHook struct {
	sync.Hook
	remaining int
	err       error
}

func (h *interruptedRestageHook) Append(ctx context.Context, tx *sql.Tx, producedBy string, rec sync.Record) (sync.Closure, bool, error) {
	if h.remaining == 0 {
		return sync.Closure{}, false, h.err
	}
	h.remaining--
	return h.Hook.Append(ctx, tx, producedBy, rec)
}

func (h *interruptedRestageHook) CommitInline(context.Context, sync.Closure) error {
	panic("restage must not publish")
}

func TestRestageResumesComplaintHistory(t *testing.T) {
	h := newHarness(t)
	original := h.tell("Keep the operator's original wording.")
	amended, err := h.store.Amend(h.ctx, AmendInput{ComplaintID: original.ID, Text: "Keep both wordings.", By: "alex", Host: "workstation-linux"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Restage(h.ctx); err == nil {
		t.Fatal("restage without a hook succeeded")
	}
	if err := sync.EnsureSchema(h.store.db); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("interrupted")
	hook := &interruptedRestageHook{Hook: sync.NewStager(), remaining: 1, err: interrupted}
	h.store.sync = hook
	if n, err := h.store.Restage(h.ctx); n != 1 || !errors.Is(err, interrupted) {
		t.Fatalf("interrupted restage = %d, %v", n, err)
	}
	hook.remaining = 1
	if n, err := h.store.Restage(h.ctx); n != 1 || err != nil {
		t.Fatalf("resumed restage = %d, %v", n, err)
	}
	if n, err := h.store.Restage(h.ctx); n != 0 || err != nil {
		t.Fatalf("repeated restage = %d, %v", n, err)
	}
	for _, want := range []Complaint{original, amended} {
		var wire []byte
		var runID string
		if err := h.store.db.QueryRowContext(h.ctx, `SELECT r.run_id, p.payload
			FROM sync_record r JOIN sync_payload p ON p.record_id = r.record_id
			WHERE r.record_id = ?`, want.ID).Scan(&runID, &wire); err != nil {
			t.Fatal(err)
		}
		var got publishedComplaint
		if err := json.Unmarshal(wire, &got); err != nil {
			t.Fatal(err)
		}
		if runID != want.ID || got.ID != want.ID || got.RootID != want.RootID ||
			got.AncestorID != want.AncestorID || got.Sequence != want.Sequence ||
			got.Text != want.Text || got.OperatorID != want.By || got.HostID != want.Host ||
			got.Redacted != want.Redacted || got.CreatedAt != formatTime(want.CreatedAt) {
			t.Fatalf("recovered complaint = %+v in %q, want %+v", got, runID, want)
		}
	}
}
