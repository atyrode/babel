package disposition

import (
	"bytes"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sync"
)

func TestRestagePreservesDispositionPublications(t *testing.T) {
	// Capture the live canonical publications without journaling them, leaving
	// the same durable source rows as a pre-publication local deployment.
	hook := &fakeHook{t: t}
	h := newHarness(t, WithSync(hook))
	record := h.hypothesis("the manifest is stale")
	action, err := h.store.Propose(h.ctx, ProposeInput{
		Record: record, Kind: KindAskQuestion, ProposedBy: frontier.Run("run-restage"),
		Ref: "act-1", Payload: Payload{Summary: "who owns the manifest?"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, ruling := range []Ruling{RulingAccepted, RulingDeclined} {
		if _, err := h.store.Decide(h.ctx, DecideInput{
			DispositionID: action.ID, Ruling: ruling, By: "alex", Note: "reconsidered",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.store.Invite(h.ctx, InviteInput{Record: record, By: "alex"}); err != nil {
		t.Fatal(err)
	}
	h.store.sync = sync.NewStager()
	if n, err := h.store.Restage(h.ctx); n != 4 || err != nil {
		t.Fatalf("restage = %d, %v", n, err)
	}
	for i, want := range hook.staged {
		var payload []byte
		var runID, kind string
		var schema int
		if err := h.store.db.QueryRowContext(h.ctx, `SELECT r.run_id, r.kind, r.record_schema, p.payload
			FROM sync_record r JOIN sync_payload p ON p.record_id = r.record_id
			WHERE r.record_id = ?`, want.EntityID).Scan(&runID, &kind, &schema, &payload); err != nil {
			t.Fatal(err)
		}
		wantRun := hook.producedBy[i]
		if wantRun == "" {
			wantRun = want.EntityID
		}
		if runID != wantRun || kind != string(want.Kind) || schema != want.Schema || !bytes.Equal(payload, want.Payload) {
			t.Fatalf("recovered %s in %s as %s/%d: %s; want run %s and %+v",
				want.EntityID, runID, kind, schema, payload, wantRun, want)
		}
	}
	if n, err := h.store.Restage(h.ctx); n != 0 || err != nil {
		t.Fatalf("repeated restage = %d, %v", n, err)
	}
}
