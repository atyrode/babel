package reference

import (
	"bytes"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

func TestRestagePublishesLocalEdgeWithoutChangingItsBytes(t *testing.T) {
	f := newPublishingFixture(t, false)
	ctx := t.Context()
	f.store.sync = nil
	edge, err := f.store.Append(ctx, operatorAsserted(sentinel))
	if err != nil {
		t.Fatal(err)
	}
	var original []byte
	if err := f.store.db.QueryRowContext(ctx, `SELECT payload_json FROM reference_edge WHERE id = ?`, edge.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	f.store.sync = babelsync.NewStager()
	if n, err := f.store.Restage(ctx); n != 1 || err != nil {
		t.Fatalf("restage = %d, %v", n, err)
	}
	if got := f.remoteRecordCount(t); got != 0 {
		t.Fatalf("restage published %d remote records", got)
	}
	if n, err := f.store.Restage(ctx); n != 0 || err != nil {
		t.Fatalf("restage before retry = %d, %v", n, err)
	}
	if rep := f.retry(t); rep.Committed[sharedcatalog.KindLink] != 1 {
		t.Fatalf("retry = %+v", rep)
	}
	edges := f.remoteEdges(t)
	if len(edges) != 1 || edges[0].RecordID != edge.ID ||
		edges[0].Kind != sharedcatalog.EdgeKind(edge.Kind) ||
		edges[0].From.Kind != edge.From.Kind || edges[0].From.ID != edge.From.ID ||
		edges[0].To.Kind != edge.To.Kind || edges[0].To.ID != edge.To.ID {
		t.Fatalf("recovered edges = %+v, want %+v", edges, edge)
	}
	records, err := sharedcatalog.AnalysisRecords(ctx, f.catalog, edge.ID)
	if err != nil || len(records) != 1 {
		t.Fatalf("published records = %+v, %v", records, err)
	}
	opened, err := sharedcatalog.OpenRecord(ctx, f.objects, f.ring, records[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, original) {
		t.Fatalf("opened payload = %s, want original bytes %s", opened, original)
	}
	if n, err := f.store.Restage(ctx); n != 0 || err != nil {
		t.Fatalf("restage after publication = %d, %v", n, err)
	}
	if rep := f.retry(t); rep.ObjectsWritten != 0 || rep.RunsCommitted != 0 {
		t.Fatalf("repeated retry = %+v", rep)
	}
	if got := f.remoteRecordCount(t); got != 1 {
		t.Fatalf("repeated recovery left %d remote records", got)
	}
}
