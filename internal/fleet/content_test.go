package fleet_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
	"github.com/atyrode/babel/internal/worker"
)

// Capture the producer's actual canonical bytes, not a second definition of
// preparation or receipt JSON maintained by the reader's tests.
type publicationCapture struct {
	records []babelsync.Record
}

func (c *publicationCapture) Append(_ context.Context, _ *sql.Tx, _ string, rec babelsync.Record) (babelsync.Closure, bool, error) {
	c.records = append(c.records, rec)
	return babelsync.Closure{}, false, nil
}

func (*publicationCapture) StageTx(context.Context, *sql.Tx, babelsync.Record) error    { return nil }
func (*publicationCapture) DeclareTx(context.Context, *sql.Tx, babelsync.Closure) error { return nil }
func (*publicationCapture) CommitInline(context.Context, babelsync.Closure) error       { return nil }

func TestCanonicalContentAcrossHosts(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	prep, err := run.NewPreparation(at, []run.Selected{{
		Host: "h2", Harness: event.HarnessOMP, SourceID: "session-opening",
		CaptureDigest: digest.Bytes([]byte("capture")), SourceDigest: digest.Bytes([]byte("source")),
		Adapter: run.AdapterRef{Schema: 1, Version: "fixture/1"},
	}}, run.PreparationContext{})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := run.NewReceipt(run.NewReceiptID(), "run-canonical", prep,
		run.Authority{Kind: run.AuthorityOperator, Ref: "command:explore"}, run.Body{
			Cookbook: []run.CookbookAsset{{Kind: run.AssetLens, Ref: worker.RecipeRef{ID: "fixture", Version: 1}}},
			Job:      run.JobVersions{Job: 1, Prompt: "fixture/1", Schema: "fixture/1"},
			Policy:   run.PolicyVersions{Redaction: "fixture/1", Disclosure: "fixture/1"},
			Timing:   run.Timing{StartedAt: at, FinishedAt: at},
			Failures: []run.Failure{{Stage: "launch", Code: "unavailable", Message: "fixture has no worker", At: at}},
		}, at)
	if err != nil {
		t.Fatal(err)
	}
	capture := &publicationCapture{}
	store, err := run.Open(t.TempDir(), run.WithSync(capture))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.PutPreparation(ctx, prep); err != nil {
		t.Fatal(err)
	}
	if err := store.PutReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	closure := sharedcatalog.RunClosure{
		RunID: "run-canonical", DeploymentID: "d1", OriginInstanceID: "inst-b",
	}
	for _, rec := range capture.records {
		closure.Records = append(closure.Records, sharedcatalog.StagedRecord{
			RecordID: rec.EntityID, Kind: rec.Kind, Schema: rec.Schema, Payload: rec.Payload,
		})
	}
	// Deliberately misleading prefixes: dispatch belongs to the authenticated
	// catalog kind, never to a record ID or a guessed top-level JSON kind.
	wire := hypothesis("prep-frontier", "remote hypothesis").published
	payload, err := wire.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	closure.Records = append(closure.Records,
		sharedcatalog.StagedRecord{RecordID: wire.ID, Kind: sharedcatalog.KindHypothesis, Schema: 1, Payload: payload},
		sharedcatalog.StagedRecord{RecordID: "hyp-context", Kind: sharedcatalog.KindContext, Schema: 1, Payload: []byte(`{ "note": "context" }`)},
		sharedcatalog.StagedRecord{RecordID: "hyp-complaint", Kind: sharedcatalog.KindComplaint, Schema: 1, Payload: []byte(`{"note":"complaint"}`)},
		sharedcatalog.StagedRecord{RecordID: "bad-json", Kind: sharedcatalog.KindPreparation, Schema: 1, Payload: []byte(`{"schema":`)},
		sharedcatalog.StagedRecord{RecordID: "bad-frontier", Kind: sharedcatalog.KindHypothesis, Schema: 1, Payload: []byte(`{"schema":1,"id":"bad-frontier","payload":{}}`)},
		sharedcatalog.StagedRecord{RecordID: "future-frontier", Kind: sharedcatalog.KindHypothesis, Schema: 1, Payload: []byte(`{"schema":999,"kind":"hypothesis","id":"future-frontier","payload":{"statement":"future"}}`)},
		sharedcatalog.StagedRecord{RecordID: "mismatched-frontier", Kind: sharedcatalog.KindFinding, Schema: 1, Payload: payload},
	)
	closure.RecordCount = len(closure.Records)
	want := make(map[string][]byte)
	for i := range closure.Records {
		closure.Records[i].Ordinal = int64(i)
		want[closure.Records[i].RecordID] = closure.Records[i].Payload
	}
	if _, err := sharedcatalog.SyncRun(ctx, h.db, h.store, h.ring, closure); err != nil {
		t.Fatal(err)
	}
	rows, err := h.reader.RecordsWithContent(ctx, sharedcatalog.RecordFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(want) {
		t.Fatalf("got %d records, want %d", len(rows), len(want))
	}
	var preparation, receiptRow sharedcatalog.FleetRecord
	for _, row := range rows {
		id := row.Record.RecordID
		if id == "bad-json" || id == "bad-frontier" || id == "future-frontier" || id == "mismatched-frontier" {
			if row.Unopened == "" || row.Content != nil || row.Published != nil {
				t.Fatalf("malformed record opened: %+v", row)
			}
			continue
		}
		if row.HostID != "h2" || row.Unopened != "" || !bytes.Equal(row.Content, want[id]) {
			t.Fatalf("cross-host payload %s not preserved: %+v", id, row)
		}
		if id == wire.ID {
			if row.Published == nil {
				t.Fatal("frontier projection missing")
			}
			out, err := row.Published.Output()
			if err != nil || out.Summary != "remote hypothesis "+sentinel {
				t.Fatalf("frontier summary = %+v, %v", out, err)
			}
		} else if row.Published != nil {
			t.Fatalf("non-frontier %s acquired a frontier projection", id)
		}
		if row.Record.Kind == sharedcatalog.KindPreparation {
			preparation = row.FleetRecord
		}
		if row.Record.Kind == sharedcatalog.KindReceipt {
			receiptRow = row.FleetRecord
		}
	}
	if preparation.Record.RecordID == "" || receiptRow.Record.RecordID == "" {
		t.Fatal("producer did not publish both preparation and receipt")
	}
	// A valid JSON body must not bypass object digest or authenticated identity.
	for _, mutate := range []struct {
		name   string
		change func(*sharedcatalog.FleetRecord)
	}{
		{"identity", func(r *sharedcatalog.FleetRecord) { r.Record.RecordID = "other-record" }},
		{"kind", func(r *sharedcatalog.FleetRecord) { r.Record.Kind = sharedcatalog.KindReceipt }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			row := preparation
			mutate.change(&row)
			if _, err := h.reader.Open(ctx, row); err == nil {
				t.Fatal("unauthenticated catalog metadata opened")
			}
		})
	}
	wrong, err := fleet.NewReader(h.db, h.store, newKeyring(t, "key-1"), "d1", "h1")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []sharedcatalog.FleetRecord{preparation, receiptRow} {
		if _, err := wrong.Open(ctx, row); err == nil {
			t.Fatal("wrong key material opened canonical content")
		}
	}
	// Swapped and truncated objects fail the digest check, independently of
	// whether their original plaintext belongs to frontier or run.
	for _, row := range rows {
		if row.Unopened != "" {
			continue
		}
		key := row.Record.ObjectKey
		original := h.store.objects[key]
		other := h.store.objects[preparation.Record.ObjectKey]
		if key == preparation.Record.ObjectKey {
			other = h.store.objects[receiptRow.Record.ObjectKey]
		}
		for _, replacement := range [][]byte{original[:len(original)-1], other} {
			h.store.objects[key] = replacement
			if _, err := h.reader.Open(ctx, row.FleetRecord); !errors.Is(err, sharedcatalog.ErrObjectVerification) {
				t.Fatalf("damaged or swapped %s: %v", row.Record.RecordID, err)
			}
		}
		h.store.objects[key] = original
	}
}
