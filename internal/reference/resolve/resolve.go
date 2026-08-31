// Package resolve wires Babel's record stores into the reference graph's
// anchoring gate (issue #113).
//
// It is a package of its own rather than part of internal/reference, and the
// reason is structural. Emission sites - a revision mint in internal/frontier,
// an absorption in internal/explore, a disposition - hold reference.Appender
// and therefore import internal/reference; a resolver has to import those same
// stores to ask whether a record exists. One package doing both would be an
// import cycle. So internal/reference knows only the Resolver interface, this
// package knows every store, and nothing but the wiring imports this.
//
// What a resolver answers is deliberately narrow: does this id name a record
// that exists, in this namespace, on this machine. It never reads the record,
// never reports what it says, and never widens - #113's rule is that an edge
// may only bind to a record that demonstrably exists, and existence is the
// whole question.
//
// Absence and failure are different answers and are kept apart. Every store
// here reports a missing record as its own sentinel error, which becomes
// (false, nil) - a refused endpoint, the caller's problem. Anything else is
// reported as itself, because an unreadable durable file must not look like a
// hallucinated citation.
package resolve

import (
	"context"
	"errors"
	"fmt"
	stdsync "sync"

	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/reference"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// The record namespaces this package can resolve. They are the strings that
// become RecordRef.Kind and, on publication, migrations/0008's from_kind and
// to_kind columns, so they are named once here rather than spelled at every
// emission site.
//
// The set is what exists today. A namespace #113 anticipates but Babel has not
// built - a complaint (#114, #115) - is deliberately absent rather than
// registered against a store that cannot answer: an unregistered namespace is
// refused with "this machine can resolve ..." and names the gap, while a
// namespace wired to a resolver that always says no would report a real
// complaint as a hallucination.
const (
	// NamespaceSession addresses a session by its durable session key: the
	// sharedcatalog.SessionUID digest. It is deliberately not the selector,
	// and that is a privacy boundary rather than a preference - see Sessions.
	NamespaceSession = "session"

	NamespaceHypothesis  = "hypothesis"
	NamespaceObservation = "observation"
	NamespaceFinding     = "finding"
	NamespaceProposal    = "proposal"

	// NamespaceRun addresses an exploration by its run id, which exists as a
	// record exactly when the run has written at least one receipt revision.
	NamespaceRun = "run"
	// NamespaceReceipt addresses one receipt revision by its own id, which is
	// the finer question: a run's current answer versus a specific revision of
	// it.
	NamespaceReceipt     = "receipt"
	NamespacePreparation = "preparation"

	NamespaceDisposition = "disposition"

	NamespaceRealityFact   = "reality_fact"
	NamespaceRealityEntity = "reality_entity"
)

// Stores names the local stores a deployment has opened. Every field is
// optional: a machine that has not opened the Reality Ledger registers no
// reality namespace, and an edge that tries to bind to one is refused with the
// list of namespaces this machine can actually vouch for.
type Stores struct {
	Frontier     *frontier.Store
	Runs         *runstore.Store
	Dispositions *disposition.Store
	Reality      *reality.Store

	// Sessions resolves session endpoints, built by NewSessions.
	//
	// It is a constructed resolver rather than the cache and the two identity
	// strings, because the caller needs the object anyway: the same derivation
	// that answers "does this session key exist" is the one an emission site
	// uses to MINT a session endpoint, and Sessions.Key is what it holds for
	// that. Two independent copies of a digest's inputs is how two machines
	// end up naming one session two ways.
	Sessions *Sessions
}

// Registry builds the resolver registry from the stores a deployment holds.
//
// It registers exactly the namespaces it can answer and returns the registry
// even when that is none, because "this machine resolves nothing" is a
// coherent state - a fresh install with no durable stores open - and refusing
// to build would turn it into a startup failure instead of an edge store that
// accepts nothing.
func Registry(s Stores) (*reference.Registry, error) {
	registry := reference.NewRegistry()
	register := func(kind string, resolver reference.Resolver) error {
		if err := registry.Register(kind, resolver); err != nil {
			return fmt.Errorf("wire %s resolver: %w", kind, err)
		}
		return nil
	}

	if s.Frontier != nil {
		front := s.Frontier
		for kind, resolver := range map[string]reference.Resolver{
			NamespaceHypothesis: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := front.Hypothesis(ctx, id)
				return present(err, frontier.ErrUnknownEntity)
			}),
			NamespaceObservation: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := front.Observation(ctx, id)
				return present(err, frontier.ErrUnknownEntity)
			}),
			NamespaceFinding: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := front.Finding(ctx, id)
				return present(err, frontier.ErrUnknownEntity)
			}),
			NamespaceProposal: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := front.Proposal(ctx, id)
				return present(err, frontier.ErrUnknownEntity)
			}),
		} {
			if err := register(kind, resolver); err != nil {
				return nil, err
			}
		}
	}

	if s.Runs != nil {
		runs := s.Runs
		for kind, resolver := range map[string]reference.Resolver{
			// A run is not a row: internal/run keys receipts by their own id
			// and carries the run id as a column, so "this run exists" is
			// "this run has written a receipt". That is also the honest
			// answer - a run that has produced no receipt has produced
			// nothing to cite, and #96 makes the receipt the record of the
			// run. internal/run says so with ErrNotFound, and the length
			// check stays because "no revisions" is the same absence
			// whichever way that store chooses to report it.
			NamespaceRun: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				revisions, err := runs.Revisions(ctx, id)
				exists, err := present(err, runstore.ErrNotFound)
				if err != nil || !exists {
					return false, err
				}
				return len(revisions) > 0, nil
			}),
			NamespaceReceipt: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := runs.Receipt(ctx, runstore.ReceiptID(id))
				return present(err, runstore.ErrNotFound)
			}),
			NamespacePreparation: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := runs.Preparation(ctx, runstore.PreparationID(id))
				return present(err, runstore.ErrNotFound)
			}),
		} {
			if err := register(kind, resolver); err != nil {
				return nil, err
			}
		}
	}

	if s.Dispositions != nil {
		dispositions := s.Dispositions
		if err := register(NamespaceDisposition,
			reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := dispositions.Disposition(ctx, id)
				return present(err, disposition.ErrUnknownDisposition)
			})); err != nil {
			return nil, err
		}
	}

	if s.Reality != nil {
		ledger := s.Reality
		for kind, resolver := range map[string]reference.Resolver{
			NamespaceRealityFact: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := ledger.Fact(ctx, id)
				return present(err, reality.ErrUnknownRecord)
			}),
			NamespaceRealityEntity: reference.ResolverFunc(func(ctx context.Context, id string) (bool, error) {
				_, err := ledger.Entity(ctx, id)
				return present(err, reality.ErrUnknownRecord)
			}),
		} {
			if err := register(kind, resolver); err != nil {
				return nil, err
			}
		}
	}

	if s.Sessions != nil {
		if err := register(NamespaceSession, s.Sessions); err != nil {
			return nil, err
		}
	}

	return registry, nil
}

// present turns a store's lookup outcome into an existence answer.
//
// The sentinel is passed in rather than inferred because each store names
// absence its own way, and matching on the message would be matching on prose.
// Anything that is not that sentinel is a failure of the store and is reported
// as itself: an endpoint the machine cannot check is not an endpoint the
// machine has disproved.
func present(err error, missing error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, missing):
		return false, nil
	default:
		return false, err
	}
}

// Sessions resolves a session by its durable session key against the local
// session catalog.
//
// The key is the sharedcatalog.SessionUID digest over deployment, host,
// harness and source id, and using it rather than the selector is the whole
// design of a session reference. A selector embeds the adapter source id,
// which embeds a workspace-derived project slug; an edge's endpoints publish
// as plaintext columns, so a selector endpoint would put a path Babel
// deliberately keeps out of PostgreSQL into PostgreSQL (internal/cli's
// publishableSessions states the same boundary for session rows). The digest is
// stable across pushes, distinct across hosts and deployments, and already the
// identity the shared catalog keys sessions by - so a session reference means
// the same session on every machine in the fleet, and reveals nothing about
// which project it belongs to.
//
// The consequence a caller has to know: this resolver answers for sessions THIS
// machine holds. A reference to another host's session is refused here, which
// is the anchoring rule applied honestly - this machine cannot demonstrate that
// record exists - and not a claim that the session is fictional.
type Sessions struct {
	cache        *catalog.Cache
	deploymentID string
	hostID       string

	// keys memoizes the derived digests. A digest is one-way, so answering a
	// lookup means deriving the key of every local session and comparing;
	// doing that per edge would make each append scan the corpus.
	//
	// Only a hit is served from the memo. A miss reloads once and asks again,
	// because the catalog grows - a session described a minute ago is a
	// legitimate endpoint - and a cached "no" would refuse a citation to a
	// record that exists. A miss therefore costs one identity scan, which is
	// what a refused write is worth; a hit costs a map lookup.
	mu     stdsync.Mutex
	keys   map[string]bool
	loaded bool
}

// NewSessions builds the session resolver. The deployment and host identity are
// required: they are two of the digest's four inputs, and deriving keys under
// the wrong identity would produce a resolver that refuses every real session
// while looking like it works.
func NewSessions(cache *catalog.Cache, deploymentID, hostID string) (*Sessions, error) {
	if cache == nil {
		return nil, errors.New("resolve: a session resolver needs the local session catalog")
	}
	if deploymentID == "" || hostID == "" {
		return nil, errors.New(
			"resolve: a session resolver needs the deployment and host identity its durable keys are derived under")
	}
	return &Sessions{cache: cache, deploymentID: deploymentID, hostID: hostID}, nil
}

// Exists reports whether a durable session key names a session this machine
// holds.
func (s *Sessions) Exists(ctx context.Context, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded && s.keys[id] {
		return true, nil
	}
	if err := s.reload(ctx); err != nil {
		return false, err
	}
	return s.keys[id], nil
}

// Key derives the durable session key for one local session, which is what an
// emission site holding a selector needs in order to name a session endpoint at
// all.
//
// It is exported because the derivation must not be copied: an emitter that
// digested the same four fields in another order, or without the length
// prefixes SessionUID applies, would mint keys that resolve nowhere and publish
// endpoints no other host can match. The method value is directly assignable to
// a `func(harness, sourceID string) string` seam, which is how an emission site
// takes it without importing the shared catalog's identity algebra.
//
// It is total, and a nil resolver returns the empty string rather than
// panicking: a machine with no session catalog mints no session endpoint, and
// an emitter reading "" as "no key" needs no second failure mode for a
// derivation that cannot fail.
func (s *Sessions) Key(harness, sourceID string) string {
	if s == nil || harness == "" || sourceID == "" {
		return ""
	}
	return sharedcatalog.SessionUID(s.deploymentID, s.hostID, harness, sourceID)
}

// reload derives the durable key of every session the catalog holds. The caller
// holds the lock.
func (s *Sessions) reload(ctx context.Context) error {
	identities, err := s.cache.SessionIdentities(ctx)
	if err != nil {
		return fmt.Errorf("resolve: read local sessions: %w", err)
	}
	keys := make(map[string]bool, len(identities))
	for _, identity := range identities {
		if identity.Harness == "" || identity.SourceID == "" {
			// A cached row with no harness or no source id cannot be given a
			// durable key, and inventing one under the empty string would make
			// every such row resolve to the same session. Skipping it means a
			// reference to it is refused, which is correct: nothing can name
			// it.
			continue
		}
		keys[s.Key(identity.Harness, identity.SourceID)] = true
	}
	s.keys, s.loaded = keys, true
	return nil
}
