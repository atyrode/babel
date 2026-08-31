package resolve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/reference"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// These tests point the resolvers at the real stores, because the thing that
// can be wrong here is not the logic - it is the assumption each resolver makes
// about how one store names absence. A stub that returns the sentinel this file
// expects would prove only that this file agrees with itself.

const (
	testDeployment = "fixturedeployment"
	testHost       = "fixturehost"
)

// stores opens every durable store in one temporary directory, which is also
// how a real machine holds them: one durable.db, one component per store.
func stores(t *testing.T) (Stores, *frontier.Store) {
	t.Helper()
	dir := t.TempDir()

	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() { front.Close() })

	runs, err := runstore.Open(dir)
	if err != nil {
		t.Fatalf("open run store: %v", err)
	}
	t.Cleanup(func() { runs.Close() })

	dispositions, err := disposition.Open(dir, front)
	if err != nil {
		t.Fatalf("open disposition store: %v", err)
	}
	t.Cleanup(func() { dispositions.Close() })

	ledger, err := reality.Open(dir)
	if err != nil {
		t.Fatalf("open reality ledger: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })

	sessions, err := NewSessions(sessionCache(t, dir), testDeployment, testHost)
	if err != nil {
		t.Fatalf("build session resolver: %v", err)
	}

	return Stores{
		Frontier:     front,
		Runs:         runs,
		Dispositions: dispositions,
		Reality:      ledger,
		Sessions:     sessions,
	}, front
}

// sessionCache is a local session catalog holding one session, described the
// way a real refresh describes one: a primary file that exists, a harness, and
// an adapter source id.
func sessionCache(t *testing.T, dir string) *catalog.Cache {
	t.Helper()
	cache, err := catalog.Open(filepath.Join(dir, "cache"))
	if err != nil {
		t.Fatalf("open session catalog: %v", err)
	}
	t.Cleanup(func() { cache.Close() })

	transcript := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write fixture transcript: %v", err)
	}
	ref := catalog.Ref{
		Selector:    testSelector,
		Harness:     testHarness,
		SourceID:    testSourceID,
		PrimaryPath: transcript,
	}
	if _, err := cache.Refresh(context.Background(), []string{testHarness},
		[]catalog.Ref{ref}, func(catalog.Ref) (catalog.Row, bool) {
			return catalog.Row{RowJSON: []byte(`{}`)}, true
		}, nil); err != nil {
		t.Fatalf("refresh session catalog: %v", err)
	}
	return cache
}

const (
	testHarness  = "claude"
	testSourceID = "synthetic-project-0001"
	// testSelector is the local fetch handle, and deliberately not an
	// endpoint: it embeds the source id, which embeds a workspace-derived
	// slug.
	testSelector = testHarness + "/" + testSourceID
)

// The resolvers must answer for records that exist and refuse ids that do not,
// against the stores as they actually report absence.
func TestResolversAnswerForRecordsThatExist(t *testing.T) {
	s, front := stores(t)
	ctx := t.Context()

	registry, err := Registry(s)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}

	hypothesis, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "run-fixture",
		Payload: frontier.HypothesisPayload{Statement: "a synthetic candidate"},
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}

	// An edge store wired to these resolvers is the real consumer, so the
	// assertions run through it: an endpoint that resolves is accepted, and one
	// that does not is a write error.
	edges, err := reference.Open(t.TempDir(), reference.WithResolvers(registry))
	if err != nil {
		t.Fatalf("open edge store: %v", err)
	}
	defer edges.Close()

	sessionKey := s.Sessions.Key(testHarness, testSourceID)
	if sessionKey == "" {
		t.Fatal("the session resolver derived no key for a session it holds")
	}
	if sessionKey == testSelector {
		t.Fatal("the durable session key is the selector; the digest is not being applied")
	}
	if sessionKey != sharedcatalog.SessionUID(testDeployment, testHost, testHarness, testSourceID) {
		t.Fatal("the derived key does not match the shared catalog's own derivation")
	}

	edge, err := edges.Append(ctx, reference.Edge{
		Kind:      reference.KindEvidence,
		From:      reference.RecordRef{Kind: NamespaceHypothesis, ID: hypothesis.ID},
		To:        reference.RecordRef{Kind: NamespaceSession, ID: sessionKey},
		ActorKind: reference.ActorRun,
		ActorRef:  "run-fixture",
	})
	if err != nil {
		t.Fatalf("append an edge between records that exist: %v", err)
	}
	if edge.ID == "" {
		t.Error("the accepted edge was minted with no id")
	}

	// The privacy boundary, stated as a test: a selector is not an endpoint. It
	// would publish a workspace-derived path into a plaintext PostgreSQL
	// column, so the resolver refuses it rather than trusting the caller to
	// know.
	_, err = edges.Append(ctx, reference.Edge{
		Kind:      reference.KindEvidence,
		From:      reference.RecordRef{Kind: NamespaceHypothesis, ID: hypothesis.ID},
		To:        reference.RecordRef{Kind: NamespaceSession, ID: testSelector},
		ActorKind: reference.ActorRun,
		ActorRef:  "run-fixture",
	})
	if !errors.Is(err, reference.ErrNoSuchTarget) {
		t.Fatalf("append with a selector endpoint: %v, want ErrNoSuchTarget", err)
	}

	// A hypothesis id that no store holds is refused on the same terms.
	_, err = edges.Append(ctx, reference.Edge{
		Kind:      reference.KindRefines,
		From:      reference.RecordRef{Kind: NamespaceHypothesis, ID: hypothesis.ID},
		To:        reference.RecordRef{Kind: NamespaceHypothesis, ID: "hyp_deadbeefdeadbeefdeadbeefdeadbeef"},
		ActorKind: reference.ActorSystem,
	})
	if !errors.Is(err, reference.ErrNoSuchTarget) {
		t.Fatalf("append to a hypothesis nothing holds: %v, want ErrNoSuchTarget", err)
	}

	// A namespace #113 anticipates but Babel has not built is unregistered
	// rather than wired to a resolver that always says no, so the refusal names
	// the gap.
	_, err = edges.Append(ctx, reference.Edge{
		Kind:      reference.KindAddresses,
		From:      reference.RecordRef{Kind: NamespaceHypothesis, ID: hypothesis.ID},
		To:        reference.RecordRef{Kind: "complaint", ID: "cmp_1"},
		ActorKind: reference.ActorOperator,
		ActorRef:  "alex",
	})
	if !errors.Is(err, reference.ErrUnknownNamespace) {
		t.Fatalf("append to an unbuilt namespace: %v, want ErrUnknownNamespace", err)
	}
}

// Every namespace this package claims must actually be registered, and every
// registered one must report a missing id as absence rather than as a failure -
// which is the assumption about each store's sentinel error, checked against
// the store.
func TestEveryRegisteredNamespaceReportsAbsenceAsAbsence(t *testing.T) {
	s, _ := stores(t)
	registry, err := Registry(s)
	if err != nil {
		t.Fatalf("build registry: %v", err)
	}

	want := []string{
		NamespaceDisposition, NamespaceFinding, NamespaceHypothesis,
		NamespaceObservation, NamespacePreparation, NamespaceProposal,
		NamespaceRealityEntity, NamespaceRealityFact, NamespaceReceipt,
		NamespaceRun, NamespaceSession,
	}
	got := registry.Namespaces()
	if len(got) != len(want) {
		t.Fatalf("registered namespaces = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("registered namespaces = %v, want %v", got, want)
		}
	}

	// The stores are empty apart from the session catalog, so every lookup is
	// a miss - and a miss must be (false, nil). A store reporting absence some
	// other way would surface here as an error, which is exactly the failure
	// this test exists to catch when one of those stores changes.
	edges, err := reference.Open(t.TempDir(), reference.WithResolvers(registry))
	if err != nil {
		t.Fatalf("open edge store: %v", err)
	}
	defer edges.Close()

	for _, namespace := range want {
		_, err := edges.Append(t.Context(), reference.Edge{
			Kind:      reference.KindRefines,
			From:      reference.RecordRef{Kind: namespace, ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
			To:        reference.RecordRef{Kind: namespace, ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			ActorKind: reference.ActorSystem,
		})
		if !errors.Is(err, reference.ErrNoSuchTarget) {
			t.Errorf("%s: append with an unknown id = %v, want ErrNoSuchTarget", namespace, err)
		}
	}
}

// A resolver whose store cannot answer must not report absence. Collapsing the
// two would make an unreadable durable file look like a hallucinated citation.
func TestPresentKeepsFailureApartFromAbsence(t *testing.T) {
	broken := errors.New("durable database is locked")
	for _, tc := range []struct {
		name    string
		err     error
		missing error
		exists  bool
		fails   bool
	}{
		{name: "the record is there", err: nil, missing: frontier.ErrUnknownEntity, exists: true},
		{name: "the frontier says no", err: frontier.ErrUnknownEntity, missing: frontier.ErrUnknownEntity},
		{name: "the run store says no", err: runstore.ErrNotFound, missing: runstore.ErrNotFound},
		{name: "the disposition store says no", err: disposition.ErrUnknownDisposition, missing: disposition.ErrUnknownDisposition},
		{name: "the ledger says no", err: reality.ErrUnknownRecord, missing: reality.ErrUnknownRecord},
		{name: "the store is broken", err: broken, missing: frontier.ErrUnknownEntity, fails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exists, err := present(tc.err, tc.missing)
			if exists != tc.exists {
				t.Errorf("exists = %v, want %v", exists, tc.exists)
			}
			if tc.fails != (err != nil) {
				t.Errorf("err = %v, want a failure: %v", err, tc.fails)
			}
		})
	}
}

// A machine that opened no stores registers no namespaces, and that is a
// coherent state rather than a startup failure: the edge store then accepts
// nothing and says which namespaces it can resolve.
func TestAnEmptyDeploymentRegistersNothing(t *testing.T) {
	registry, err := Registry(Stores{})
	if err != nil {
		t.Fatalf("build an empty registry: %v", err)
	}
	if got := registry.Namespaces(); len(got) != 0 {
		t.Errorf("an empty deployment registered %v", got)
	}
}

// The session resolver's identity inputs are required, because deriving keys
// under the wrong identity produces a resolver that refuses every real session
// while looking like it works.
func TestSessionResolverRequiresItsIdentity(t *testing.T) {
	dir := t.TempDir()
	cache := sessionCache(t, dir)
	if _, err := NewSessions(nil, testDeployment, testHost); err == nil {
		t.Error("a session resolver was built with no catalog")
	}
	if _, err := NewSessions(cache, "", testHost); err == nil {
		t.Error("a session resolver was built with no deployment identity")
	}
	if _, err := NewSessions(cache, testDeployment, ""); err == nil {
		t.Error("a session resolver was built with no host identity")
	}

	// A nil resolver derives nothing rather than panicking, which is the
	// "" means no key contract an emission site holds.
	var absent *Sessions
	if key := absent.Key(testHarness, testSourceID); key != "" {
		t.Errorf("a nil session resolver derived %q", key)
	}
	sessions, err := NewSessions(cache, testDeployment, testHost)
	if err != nil {
		t.Fatalf("build session resolver: %v", err)
	}
	if key := sessions.Key("", testSourceID); key != "" {
		t.Errorf("a session with no harness derived %q", key)
	}

	// A session described after the resolver first answered is still a
	// legitimate endpoint: a miss reloads rather than serving a cached no.
	if exists, err := sessions.Exists(t.Context(), "0000000000000000000000000000000000000000000000000000000000000000"); err != nil || exists {
		t.Fatalf("unknown key: %v, %v", exists, err)
	}
	if exists, err := sessions.Exists(t.Context(), sessions.Key(testHarness, testSourceID)); err != nil || !exists {
		t.Fatalf("a session the catalog holds: %v, %v", exists, err)
	}
}
