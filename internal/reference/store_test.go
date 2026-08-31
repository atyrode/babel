package reference

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// These tests defend the three properties an emission site relies on: an edge
// binds only to records that exist, appending the same citation twice is the
// citation that already exists, and a record's links read back newest first
// within a bound.

func TestAppendMintsAnAnchoredEdge(t *testing.T) {
	f := newLocalFixture(t)
	ctx := t.Context()

	edge, err := f.store.Append(ctx, evidence("the run read this session"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if edge.ID == "" {
		t.Error("the edge was returned with no minted id")
	}
	if edge.CreatedAt.IsZero() {
		t.Error("the edge was returned with no creation time")
	}
	if edge.Kind != KindEvidence {
		t.Errorf("edge kind = %q, want %q", edge.Kind, KindEvidence)
	}

	read, err := f.store.Edge(ctx, edge.ID)
	if err != nil {
		t.Fatalf("read the edge back: %v", err)
	}
	if read.From != edge.From || read.To != edge.To {
		t.Errorf("endpoints round-tripped as %s -> %s, want %s -> %s",
			read.From, read.To, edge.From, edge.To)
	}
	if read.ActorKind != ActorRun || read.ActorRef != "run-fixture" {
		t.Errorf("actor round-tripped as %s/%s, want %s/run-fixture",
			read.ActorKind, read.ActorRef, ActorRun)
	}
	// The note is content: it lives in the payload, and it has to survive the
	// round trip through it or the sealed publication would carry nothing.
	if read.Note != "the run read this session" {
		t.Errorf("note round-tripped as %q", read.Note)
	}
}

// The anchoring rule, which is the reason this package validates at all: an
// edge may only bind to a record that demonstrably exists. Each case here is a
// citation a model could produce and a graph must refuse.
func TestAppendRefusesEndpointsNothingVouchesFor(t *testing.T) {
	f := newLocalFixture(t)
	ctx := t.Context()

	cases := []struct {
		name string
		edge Edge
		want error
	}{
		{
			name: "a target that does not exist",
			edge: Edge{
				Kind:      KindEvidence,
				From:      RecordRef{Kind: "finding", ID: testFindingID},
				To:        RecordRef{Kind: "session", ID: "0000000000000000000000000000000000000000000000000000000000000000"},
				ActorKind: ActorRun, ActorRef: "run-1",
			},
			want: ErrNoSuchTarget,
		},
		{
			name: "a source that does not exist",
			edge: Edge{
				Kind:      KindEvidence,
				From:      RecordRef{Kind: "finding", ID: "fnd_deadbeefdeadbeefdeadbeefdeadbeef"},
				To:        RecordRef{Kind: "session", ID: testSessionKey},
				ActorKind: ActorRun, ActorRef: "run-1",
			},
			want: ErrNoSuchTarget,
		},
		{
			// A namespace nothing registered is refused rather than admitted
			// unvalidated: "nothing here can say whether that record exists"
			// is not "the record exists".
			name: "a namespace no resolver claims",
			edge: Edge{
				Kind:      KindAddresses,
				From:      RecordRef{Kind: "finding", ID: testFindingID},
				To:        RecordRef{Kind: "complaint", ID: "cmp_1"},
				ActorKind: ActorOperator, ActorRef: "alex",
			},
			want: ErrUnknownNamespace,
		},
		{
			name: "a namespace that is prose rather than a vocabulary value",
			edge: Edge{
				Kind:      KindEvidence,
				From:      RecordRef{Kind: "finding", ID: testFindingID},
				To:        RecordRef{Kind: "the session I read", ID: testSessionKey},
				ActorKind: ActorRun, ActorRef: "run-1",
			},
			want: ErrInvalidValue,
		},
		{
			name: "an edge kind outside the closed set",
			edge: Edge{
				Kind:      Kind("cites"),
				From:      RecordRef{Kind: "finding", ID: testFindingID},
				To:        RecordRef{Kind: "session", ID: testSessionKey},
				ActorKind: ActorRun, ActorRef: "run-1",
			},
			want: ErrInvalidValue,
		},
		{
			name: "an edge nobody asserted",
			edge: Edge{
				Kind: KindEvidence,
				From: RecordRef{Kind: "finding", ID: testFindingID},
				To:   RecordRef{Kind: "session", ID: testSessionKey},
			},
			want: ErrInvalidValue,
		},
		{
			name: "an edge from a record to itself",
			edge: Edge{
				Kind:      KindRefines,
				From:      RecordRef{Kind: "finding", ID: testFindingID},
				To:        RecordRef{Kind: "finding", ID: testFindingID},
				ActorKind: ActorSystem,
			},
			want: ErrInvalidValue,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.store.Append(ctx, tc.edge)
			if !errors.Is(err, tc.want) {
				t.Fatalf("append: %v, want %v", err, tc.want)
			}
			if got := f.edgeRows(t); got != 0 {
				t.Errorf("a refused edge left %d durable rows", got)
			}
		})
	}
}

// A resolver that cannot answer is not a resolver that said no. Collapsing the
// two would make an unreadable durable file look like a hallucinated citation,
// and the write would be refused for a reason the operator cannot act on.
func TestAppendReportsAResolverFailureAsItself(t *testing.T) {
	f := newLocalFixture(t)
	broken := errors.New("durable database is locked")
	f.findings.err = broken

	_, err := f.store.Append(t.Context(), evidence(""))
	if !errors.Is(err, broken) {
		t.Fatalf("append: %v, want the resolver's own failure", err)
	}
	if errors.Is(err, ErrNoSuchTarget) {
		t.Error("a resolver failure was reported as a nonexistent target")
	}
}

// Emitters retry, so appending the same citation twice must be the citation
// that already exists - the same id and the same timestamp - rather than a
// second row. Otherwise "how many times was this asserted" is a question the
// graph answers wrongly, and a crash-retry would double every edge.
func TestAppendIsIdempotentOnKindAndEndpoints(t *testing.T) {
	f := newLocalFixture(t)
	ctx := t.Context()

	first, err := f.store.Append(ctx, evidence("read during exploration"))
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	// The retry differs in everything the key does not cover: a different
	// note, a different actor. The recorded edge is the one that was written,
	// because an immutable record is not amended by a second attempt to write
	// it.
	retry := evidence("a different note")
	retry.ActorKind = ActorOperator
	retry.ActorRef = "alex"
	second, err := f.store.Append(ctx, retry)
	if err != nil {
		t.Fatalf("second append: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("the retry minted %s, want the existing %s", second.ID, first.ID)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("the retry reported creation at %s, want %s", second.CreatedAt, first.CreatedAt)
	}
	if second.Note != first.Note || second.ActorKind != first.ActorKind {
		t.Errorf("the retry reported note %q by %s, want the recorded %q by %s",
			second.Note, second.ActorKind, first.Note, first.ActorKind)
	}
	if got := f.edgeRows(t); got != 1 {
		t.Errorf("two appends of one citation left %d rows, want 1", got)
	}

	// A different relation between the same records is a different citation,
	// not a duplicate: the key is the kind and both endpoints.
	other := evidence("")
	other.Kind = KindInspiredBy
	if _, err := f.store.Append(ctx, other); err != nil {
		t.Fatalf("append a second relation: %v", err)
	}
	if got := f.edgeRows(t); got != 2 {
		t.Errorf("two distinct relations left %d rows, want 2", got)
	}
}

// The two questions a record page asks are the same table read from two sides,
// which is why there is no separate backlink index to fall out of step with the
// edges.
func TestListingReadsBothDirectionsNewestFirst(t *testing.T) {
	f := newLocalFixture(t)
	ctx := t.Context()

	first, err := f.store.Append(ctx, evidence("first"))
	if err != nil {
		t.Fatalf("append evidence: %v", err)
	}
	supersedes := Edge{
		Kind:      KindSupersedes,
		From:      RecordRef{Kind: "finding", ID: otherFindingID},
		To:        RecordRef{Kind: "finding", ID: testFindingID},
		ActorKind: ActorOperator, ActorRef: "alex",
	}
	second, err := f.store.Append(ctx, supersedes)
	if err != nil {
		t.Fatalf("append supersedes: %v", err)
	}

	out, err := f.store.From(ctx, RecordRef{Kind: "finding", ID: testFindingID})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	if len(out) != 1 || out[0].ID != first.ID {
		t.Fatalf("outgoing links = %+v, want only the evidence edge", out)
	}

	back, err := f.store.To(ctx, RecordRef{Kind: "finding", ID: testFindingID})
	if err != nil {
		t.Fatalf("To: %v", err)
	}
	if len(back) != 1 || back[0].ID != second.ID {
		t.Fatalf("backlinks = %+v, want only the supersedes edge", back)
	}

	// Newest first, on the citing side, with three edges out of one record.
	for _, kind := range []Kind{KindRefines, KindDuplicates} {
		edge := evidence("")
		edge.Kind = kind
		edge.To = RecordRef{Kind: "finding", ID: otherFindingID}
		if _, err := f.store.Append(ctx, edge); err != nil {
			t.Fatalf("append %s: %v", kind, err)
		}
	}
	out, err = f.store.From(ctx, RecordRef{Kind: "finding", ID: testFindingID})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("read %d outgoing links, want 3", len(out))
	}
	for i := 1; i < len(out); i++ {
		if out[i-1].CreatedAt.Before(out[i].CreatedAt) {
			t.Errorf("edge %d is older than edge %d; the listing is not newest-first", i-1, i)
		}
	}

	// A kind filter narrows without reordering.
	only, err := f.store.List(ctx, Query{
		Ref:       RecordRef{Kind: "finding", ID: testFindingID},
		Direction: DirectionFrom,
		Kinds:     []Kind{KindDuplicates},
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(only) != 1 || only[0].Kind != KindDuplicates {
		t.Fatalf("filtered listing = %+v, want one duplicates edge", only)
	}
}

// A listing is bounded whether or not the caller remembered to bound it: a
// record that has accumulated thousands of citations must not arrive whole
// because a render surface asked for its links.
func TestListingIsBoundedEvenWhenTheCallerIsNot(t *testing.T) {
	f := newLocalFixture(t)
	ctx := t.Context()

	// Enough records to overflow a small page, cited by one finding.
	const cited = DefaultEdgeLimit + 5
	for i := range cited {
		id := fmt.Sprintf("fnd_%032x", i)
		f.findings.add(id)
		edge := Edge{
			Kind:      KindInspiredBy,
			From:      RecordRef{Kind: "finding", ID: testFindingID},
			To:        RecordRef{Kind: "finding", ID: id},
			ActorKind: ActorSystem,
		}
		if _, err := f.store.Append(ctx, edge); err != nil {
			t.Fatalf("append edge %d: %v", i, err)
		}
	}

	page, err := f.store.From(ctx, RecordRef{Kind: "finding", ID: testFindingID})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	if len(page) != DefaultEdgeLimit {
		t.Errorf("an unbounded listing returned %d edges, want the %d-edge default page",
			len(page), DefaultEdgeLimit)
	}
	over, err := f.store.List(ctx, Query{
		Ref:       RecordRef{Kind: "finding", ID: testFindingID},
		Direction: DirectionFrom,
		Limit:     MaxEdgeLimit * 10,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(over) != cited {
		t.Errorf("a listing asking for ten times the ceiling returned %d of %d edges",
			len(over), cited)
	}
	if _, err := f.store.List(ctx, Query{
		Ref:       RecordRef{Kind: "finding", ID: testFindingID},
		Direction: Direction("sideways"),
	}); err == nil {
		t.Error("a listing with no meaningful direction was answered")
	}
}

// An edge is immutable and never deleted, and the database enforces it rather
// than this package's SQL: a wrong citation is answered by a later edge
// (SPEC.md §4.7), and an append-only history that depends on nobody writing an
// UPDATE is append-only by convention.
func TestDurableEdgesAreImmutable(t *testing.T) {
	f := newLocalFixture(t)
	ctx := t.Context()

	edge, err := f.store.Append(ctx, evidence("as written"))
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := f.store.db.ExecContext(ctx,
		`UPDATE reference_edge SET edge_kind = 'refines' WHERE id = ?`, edge.ID); err == nil {
		t.Error("an edge's relation kind was rewritten")
	}
	if _, err := f.store.db.ExecContext(ctx,
		`UPDATE reference_edge SET payload_json = '{"note":"rewritten"}' WHERE id = ?`, edge.ID); err == nil {
		t.Error("an edge's note was rewritten")
	}
	if _, err := f.store.db.ExecContext(ctx,
		`DELETE FROM reference_edge WHERE id = ?`, edge.ID); err == nil {
		t.Error("an edge was deleted")
	}
	if got := f.edgeRows(t); got != 1 {
		t.Errorf("the durable file holds %d edges after the attempts, want 1", got)
	}
}

// A note is bounded on the way in rather than trimmed on the way out, because
// an edge is immutable: a truncated note can only be superseded, never fixed.
func TestAppendBoundsTheNote(t *testing.T) {
	f := newLocalFixture(t)
	edge := evidence("")
	for len(edge.Note) <= maxNoteLen {
		edge.Note += "why this link exists. "
	}
	if _, err := f.store.Append(t.Context(), edge); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("append with an oversized note: %v, want ErrInvalidValue", err)
	}
}

// A store with no resolvers accepts nothing. Failing closed is the point: a
// corpus with no validated citations is recoverable and one full of
// unvalidated ones is not, so a deployment that forgot to wire its stores gets
// a refusal that names what it can resolve.
func TestAStoreWithNoResolversAcceptsNothing(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	_, err = store.Append(t.Context(), evidence(""))
	if !errors.Is(err, ErrUnknownNamespace) {
		t.Fatalf("append against an unwired store: %v, want ErrUnknownNamespace", err)
	}
}

// Nil injection is the documented degrade: a surface wired without an edge
// store shows a record with no links rather than an error page, and a write
// path that forgot its nil check reports a condition instead of taking the
// process down.
func TestANilStoreDegradesRatherThanPanicking(t *testing.T) {
	var store *Store
	ctx := context.Background()

	edges, err := store.From(ctx, RecordRef{Kind: "finding", ID: testFindingID})
	if err != nil || edges != nil {
		t.Errorf("From on a nil store = %v, %v; want no edges and no error", edges, err)
	}
	edges, err = store.To(ctx, RecordRef{Kind: "finding", ID: testFindingID})
	if err != nil || edges != nil {
		t.Errorf("To on a nil store = %v, %v; want no edges and no error", edges, err)
	}
	if _, err := store.Append(ctx, evidence("")); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("Append on a nil store = %v, want ErrNotConfigured", err)
	}
	if err := store.Close(); err != nil {
		t.Errorf("Close on a nil store = %v", err)
	}
	if path := store.Path(); path != "" {
		t.Errorf("Path on a nil store = %q", path)
	}
}

// The registry is the one place a namespace's authority is decided, so it
// refuses the three ways two answers could exist for one namespace.
func TestRegistryRefusesAmbiguousWiring(t *testing.T) {
	registry := NewRegistry()
	resolver := &stubResolver{}
	if err := registry.Register("finding", resolver); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := registry.Register("finding", &stubResolver{}); err == nil {
		t.Error("a second resolver claimed a namespace that already had one")
	}
	if err := registry.Register("Finding", resolver); err == nil {
		t.Error("a malformed namespace was registered")
	}
	if err := registry.Register("session", nil); err == nil {
		t.Error("a namespace was registered with no resolver")
	}
	if got := registry.Namespaces(); len(got) != 1 || got[0] != "finding" {
		t.Errorf("registered namespaces = %v, want only finding", got)
	}
}

// Every kind this package defines must be one migrations/0008 admits, or an
// edge would be durable and staged before PostgreSQL refused it. The two
// vocabularies are separate declarations - internal/sharedcatalog cannot import
// this package - and this is what keeps them from drifting.
func TestEveryEdgeKindIsPublishable(t *testing.T) {
	kinds := []Kind{
		KindEvidence, KindSupersedes, KindRefines,
		KindAddresses, KindInspiredBy, KindDuplicates,
	}
	for _, kind := range kinds {
		if !kind.Valid() {
			t.Fatalf("kind %q is not valid in this package", kind)
		}
		staged, err := stagedEdge(Edge{
			ID:        "ref_00112233445566778899aabbccddeeff",
			Kind:      kind,
			From:      RecordRef{Kind: "finding", ID: testFindingID},
			To:        RecordRef{Kind: "session", ID: testSessionKey},
			ActorKind: ActorSystem,
			CreatedAt: mustTime(t),
		}, []byte(`{"schema":1}`))
		if err != nil {
			t.Errorf("kind %q cannot be staged for publication: %v", kind, err)
			continue
		}
		if staged.Edge == nil || !staged.Edge.Kind.Valid() {
			t.Errorf("kind %q is not in the shared catalog's vocabulary", kind)
		}
	}
	// Non-vacuity: the check must actually reject something.
	if _, err := stagedEdge(Edge{
		ID:        "ref_00112233445566778899aabbccddeeff",
		Kind:      Kind("cites"),
		From:      RecordRef{Kind: "finding", ID: testFindingID},
		To:        RecordRef{Kind: "session", ID: testSessionKey},
		ActorKind: ActorSystem,
		CreatedAt: mustTime(t),
	}, []byte(`{"schema":1}`)); err == nil {
		t.Error("a kind outside the closed set was staged for publication")
	}
}

// mustTime is the fixed instant the publication-shape checks use, since a
// published edge must carry a parseable creation time.
func mustTime(t *testing.T) time.Time {
	t.Helper()
	value, err := parseTime("2026-08-31T12:00:00.000000000Z")
	if err != nil {
		t.Fatalf("parse fixture time: %v", err)
	}
	return value
}
