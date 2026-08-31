package frontier

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/atyrode/babel/internal/reference"
)

// recordingAppender is a reference.Appender that keeps what it was asked to
// record and answers with the store's own contract: append-only, idempotent on
// (kind, from, to).
//
// It is a fake rather than the real edge store on purpose. What these tests
// check is what this package emits - the kind, the endpoints and the actor -
// and a real store would let a storage bug and an emission bug fail the same
// assertion.
type recordingAppender struct {
	mu    sync.Mutex
	edges []reference.Edge
	// calls counts every Append, including the ones idempotence collapsed, so
	// a test can tell "emitted twice and stored once" from "emitted once".
	calls int
	// fail, when set, refuses every append: the live-Appender-that-says-no
	// case, which must degrade a write and never fail one.
	fail error
}

func (a *recordingAppender) Append(_ context.Context, e reference.Edge) (reference.Edge, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.fail != nil {
		return reference.Edge{}, a.fail
	}
	if err := e.Validate(); err != nil {
		return reference.Edge{}, err
	}
	for _, held := range a.edges {
		if held.Kind == e.Kind && held.From == e.From && held.To == e.To {
			return held, nil
		}
	}
	e.ID = fmt.Sprintf("edge-%02d", len(a.edges)+1)
	a.edges = append(a.edges, e)
	return e, nil
}

// of returns the recorded edges of one kind, in emission order.
func (a *recordingAppender) of(kind reference.Kind) []reference.Edge {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []reference.Edge
	for _, e := range a.edges {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// referencedStore opens a frontier wired to a recording appender and a
// diagnostics sink.
func referencedStore(t *testing.T) (*Store, *recordingAppender, *[]error) {
	t.Helper()
	appender := &recordingAppender{}
	var warnings []error
	store, err := Open(t.TempDir(), WithReferences(appender, func(err error) {
		warnings = append(warnings, err)
	}))
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close frontier: %v", err)
		}
	})
	return store, appender, &warnings
}

// TestRevisionMintsSupersedesEdgeWithTheChainsOwnActor is #113's supersedes
// emission at the site that already knows the answer.
//
// The chain is the authority and the edge is its shadow, so the two must never
// be able to disagree about direction or about who did it: the edge points
// from the new wording to the one it replaced, and it carries the actor
// revisionActor resolved rather than an actor a call site restated.
func TestRevisionMintsSupersedesEdgeWithTheChainsOwnActor(t *testing.T) {
	ctx := context.Background()
	store, appender, warnings := referencedStore(t)

	original, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("handoffs drop constraints", 0.4),
	})
	if err != nil {
		t.Fatalf("create the original: %v", err)
	}
	// An original replaces nothing, so it has no shadow to cast.
	if edges := appender.of(reference.KindSupersedes); len(edges) != 0 {
		t.Fatalf("a first revision minted %d supersedes edges: %+v", len(edges), edges)
	}

	byRun, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:      "run-2",
		AncestorID: original.ID,
		Reason:     "the second run narrowed it to one repository",
		Payload:    hypothesisPayload("handoffs drop constraints in one repository", 0.4),
	})
	if err != nil {
		t.Fatalf("create the run's revision: %v", err)
	}
	byOperator, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:      "run-2",
		AncestorID: byRun.ID,
		Actor:      Operator("alex"),
		Reason:     "an operator sharpened the wording",
		Payload:    hypothesisPayload("handoffs drop constraints in the deploy repository", 0.4),
	})
	if err != nil {
		t.Fatalf("create the operator's revision: %v", err)
	}

	edges := appender.of(reference.KindSupersedes)
	if len(edges) != 2 {
		t.Fatalf("the chain minted %d supersedes edges, want 2: %+v", len(edges), edges)
	}
	want := []struct {
		from, to            string
		actorKind, actorRef string
	}{
		{byRun.ID, original.ID, string(ActorRun), "run-2"},
		{byOperator.ID, byRun.ID, string(ActorOperator), "alex"},
	}
	for i, w := range want {
		got := edges[i]
		if got.From != (reference.RecordRef{Kind: "hypothesis", ID: w.from}) {
			t.Errorf("edge %d from = %s, want hypothesis:%s", i+1, got.From, w.from)
		}
		if got.To != (reference.RecordRef{Kind: "hypothesis", ID: w.to}) {
			t.Errorf("edge %d to = %s, want hypothesis:%s", i+1, got.To, w.to)
		}
		if got.ActorKind != w.actorKind || got.ActorRef != w.actorRef {
			t.Errorf("edge %d actor = %s/%s, want %s/%s",
				i+1, got.ActorKind, got.ActorRef, w.actorKind, w.actorRef)
		}
	}
	// The revision's reason is not copied onto the edge: one argument, one
	// place, and the chain is the place #87 put it.
	for i, got := range edges {
		if got.Note != "" {
			t.Errorf("supersedes edge %d carries a note %q; the reason belongs to the revision", i+1, got.Note)
		}
	}
	if len(*warnings) != 0 {
		t.Errorf("emission warned on a healthy appender: %v", *warnings)
	}
}

// TestEveryRecordKindShadowsItsChain checks that the four record kinds of §4.2
// through §4.5 all reach the graph, each in its own namespace.
//
// A namespace that did not match the entity kind an operator already pastes
// would make an edge unresolvable by the only identifier anybody has, so the
// assertion is on the exact strings rather than on "some kind".
func TestEveryRecordKindShadowsItsChain(t *testing.T) {
	ctx := context.Background()
	store, appender, warnings := referencedStore(t)
	hypothesis, observation, finding, proposal := developPath(t, store)

	revisions := []struct {
		kind     EntityType
		ancestor string
		create   func(ancestor string) (string, error)
	}{
		{EntityHypothesis, hypothesis.ID, func(ancestor string) (string, error) {
			record, err := store.CreateHypothesis(ctx, HypothesisInput{
				RunID: "run-2", AncestorID: ancestor, Reason: "narrowed",
				Payload: hypothesisPayload("handoffs drop constraints, narrowed", 0.4),
			})
			return record.ID, err
		}},
		{EntityObservation, observation.ID, func(ancestor string) (string, error) {
			record, err := store.CreateObservation(ctx, ObservationInput{
				HypothesisID: hypothesis.ID, RunID: "run-2", RecipeID: "lens", RecipeVersion: 2,
				AncestorID: ancestor, Reason: "a counterexample turned up",
				Payload: observationPayload("a narrowed claim", mustEvidence(t, 3, "a cited record")),
			})
			return record.ID, err
		}},
		{EntityFinding, finding.ID, func(ancestor string) (string, error) {
			record, err := store.CreateFinding(ctx, FindingInput{
				RunID: "run-2", AncestorID: ancestor, ObservationIDs: []string{observation.ID},
				Reason:  "narrowed with the hypothesis it consolidates",
				Payload: findingPayload("late constraints, narrowed"),
			})
			return record.ID, err
		}},
		{EntityProposal, proposal.ID, func(ancestor string) (string, error) {
			record, err := store.CreateProposal(ctx, ProposalInput{
				RunID: "run-2", AncestorID: ancestor, FindingIDs: []string{finding.ID},
				Reason:  "the narrowed finding suggests a narrower action",
				Payload: proposalPayload("state constraints up front, narrowly"),
			})
			return record.ID, err
		}},
	}

	for _, r := range revisions {
		t.Run(string(r.kind), func(t *testing.T) {
			id, err := r.create(r.ancestor)
			if err != nil {
				t.Fatalf("revise the %s: %v", r.kind, err)
			}
			var found bool
			for _, e := range appender.of(reference.KindSupersedes) {
				if e.From.ID != id {
					continue
				}
				found = true
				if e.From.Kind != string(r.kind) || e.To.Kind != string(r.kind) {
					t.Errorf("edge namespaces = %s -> %s, want both %s",
						e.From.Kind, e.To.Kind, r.kind)
				}
				if e.To.ID != r.ancestor {
					t.Errorf("edge points at %q, want the ancestor %q", e.To.ID, r.ancestor)
				}
			}
			if !found {
				t.Errorf("revising a %s minted no supersedes edge", r.kind)
			}
		})
	}
	if len(*warnings) != 0 {
		t.Errorf("emission warned on a healthy appender: %v", *warnings)
	}
}

// TestDuplicateWarningsShadowTheDedupPath is #113's duplicates emission.
//
// One edge per warning the write actually recorded, so the graph and
// frontier_duplicate_warning describe the same set: the same target offered
// twice is one warning and must be one edge. And the edge says in words what
// the heuristic established, because the kind alone reads as a verdict to a
// host that cannot open the warning's sealed payload (#112).
func TestDuplicateWarningsShadowTheDedupPath(t *testing.T) {
	ctx := context.Background()
	store, appender, warnings := referencedStore(t)

	first, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("the release pipeline skips its own tests", 0.3),
	})
	if err != nil {
		t.Fatalf("create the first candidate: %v", err)
	}
	second, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("deploys report success without verifying", 0.3),
	})
	if err != nil {
		t.Fatalf("create the second candidate: %v", err)
	}

	warned, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-2",
		Payload: hypothesisPayload("release runs skip the test suite they claim to run", 0.3),
		NearDuplicates: []NearDuplicate{
			{HypothesisID: first.ID, Overlap: 0.82},
			{HypothesisID: second.ID, Overlap: 0.61},
			// Offered twice; appendDuplicateWarnings collapses it, and the
			// graph must collapse with it rather than beside it.
			{HypothesisID: first.ID, Overlap: 0.82},
		},
	})
	if err != nil {
		t.Fatalf("create the warned candidate: %v", err)
	}
	if len(warned.Duplicates) != 2 {
		t.Fatalf("the write recorded %d warnings, want 2: %+v", len(warned.Duplicates), warned.Duplicates)
	}

	edges := appender.of(reference.KindDuplicates)
	if len(edges) != len(warned.Duplicates) {
		t.Fatalf("minted %d duplicates edges for %d warnings: %+v",
			len(edges), len(warned.Duplicates), edges)
	}
	targets := map[string]bool{}
	for _, e := range edges {
		if e.From != (reference.RecordRef{Kind: "hypothesis", ID: warned.ID}) {
			t.Errorf("edge from = %s, want hypothesis:%s", e.From, warned.ID)
		}
		if e.To.Kind != "hypothesis" {
			t.Errorf("edge to = %s, want a hypothesis endpoint", e.To)
		}
		if e.ActorKind != string(ActorRun) || e.ActorRef != "run-2" {
			t.Errorf("edge actor = %s/%s, want run/run-2", e.ActorKind, e.ActorRef)
		}
		// The overlap stays on the warning row. A number here would be a
		// second copy of the authority, free to disagree with it.
		if strings.Contains(e.Note, "0.8") || strings.Contains(e.Note, "0.6") {
			t.Errorf("edge note restates the overlap: %q", e.Note)
		}
		if !strings.Contains(e.Note, "never a finding") {
			t.Errorf("edge note %q does not say the resemblance is not a verdict", e.Note)
		}
		targets[e.To.ID] = true
	}
	if !targets[first.ID] || !targets[second.ID] {
		t.Errorf("edges name %v, want both %s and %s", targets, first.ID, second.ID)
	}
	if len(*warnings) != 0 {
		t.Errorf("emission warned on a healthy appender: %v", *warnings)
	}
}

// TestAbsentReferenceGraphChangesNothing is #113's nil-injection rule at this
// package's emission site: no Appender means the feature is absent, not that
// the store is degraded.
//
// The store opened here is the one every existing caller of Open already gets,
// which is the point - a deployment that never heard of the reference graph
// must write exactly the records it wrote before, with no panic and no error
// arriving from a facility it did not ask for.
func TestAbsentReferenceGraphChangesNothing(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	original, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("an idea", 0.2),
	})
	if err != nil {
		t.Fatalf("create the original: %v", err)
	}
	revised, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:          "run-2",
		AncestorID:     original.ID,
		Reason:         "reworded",
		Payload:        hypothesisPayload("an idea, reworded", 0.2),
		NearDuplicates: []NearDuplicate{{HypothesisID: original.ID, Overlap: 0.9}},
	})
	if err != nil {
		t.Fatalf("revise with no reference graph attached: %v", err)
	}
	// The authoritative rows are all there; only the shadow is missing.
	if len(revised.Duplicates) != 1 {
		t.Errorf("the warning was lost with the graph: %+v", revised.Duplicates)
	}
	chain, err := store.Revisions(ctx, Ref{Type: EntityHypothesis, ID: revised.ID})
	if err != nil {
		t.Fatalf("read the chain: %v", err)
	}
	if len(chain) != 2 || chain[1].SupersedesID != original.ID {
		t.Errorf("the chain is %d long and supersedes %q, want 2 and %q",
			len(chain), chain[len(chain)-1].SupersedesID, original.ID)
	}
}

// TestEdgeRefusalWarnsAndKeepsTheRecord is #113's failure rule: an edge is a
// shadow, so an Appender that refuses degrades navigation and never a durable
// write.
//
// §5.2 requires every emitted candidate to be persisted, and a candidate lost
// because a graph component was unhappy would be exactly the write this
// package exists to guarantee. The refusal has to be visible, though - a
// warning nobody receives is a graph silently drifting out of date - so it
// reaches the diagnostics func the caller injected.
func TestEdgeRefusalWarnsAndKeepsTheRecord(t *testing.T) {
	ctx := context.Background()
	store, appender, warnings := referencedStore(t)
	appender.fail = errors.New("the edge store is unhappy")

	original, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("an idea worth keeping", 0.5),
	})
	if err != nil {
		t.Fatalf("create the original: %v", err)
	}
	revised, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:          "run-2",
		AncestorID:     original.ID,
		Reason:         "reworded",
		Payload:        hypothesisPayload("an idea worth keeping, reworded", 0.5),
		NearDuplicates: []NearDuplicate{{HypothesisID: original.ID, Overlap: 0.95}},
	})
	if err != nil {
		t.Fatalf("a refused edge failed the write: %v", err)
	}
	if _, err := store.Hypothesis(ctx, revised.ID); err != nil {
		t.Fatalf("the revision is not durable: %v", err)
	}

	// Both edges were attempted and both refusals were reported: a partial
	// report would leave an operator believing the graph holds one of them.
	if len(*warnings) != 2 {
		t.Fatalf("recorded %d warnings for 2 refused edges: %v", len(*warnings), *warnings)
	}
	for _, warning := range *warnings {
		if !errors.Is(warning, appender.fail) {
			t.Errorf("warning %v does not wrap the appender's refusal", warning)
		}
		if !strings.Contains(warning.Error(), revised.ID) {
			t.Errorf("warning %v does not name the record whose edge is missing", warning)
		}
	}
}

// TestEdgeRefusalWithNoDiagnosticsSinkIsSilent covers the caller that attached
// an Appender and no reporter. It must not panic: a nil func is a caller's
// choice to discard the warning, and discarding is not crashing.
func TestEdgeRefusalWithNoDiagnosticsSinkIsSilent(t *testing.T) {
	ctx := context.Background()
	appender := &recordingAppender{fail: errors.New("refused")}
	store, err := Open(t.TempDir(), WithReferences(appender, nil))
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	original, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID: "run-1", Payload: hypothesisPayload("an idea", 0.2),
	})
	if err != nil {
		t.Fatalf("create the original: %v", err)
	}
	if _, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID: "run-2", AncestorID: original.ID, Reason: "reworded",
		Payload: hypothesisPayload("an idea, reworded", 0.2),
	}); err != nil {
		t.Fatalf("revise with a refusing appender and no sink: %v", err)
	}
	if appender.calls != 1 {
		t.Errorf("the appender was called %d times, want 1", appender.calls)
	}
}
