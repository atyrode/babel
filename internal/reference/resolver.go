package reference

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	stdsync "sync"
)

// This file is the write-time anchoring rule: an edge may only bind to a record
// that demonstrably exists.
//
// It is the same rule a draft-issue disposition applies to a repository, turned
// on the corpus itself, and it exists because the alternative is a graph a model
// can hallucinate into. A free-text link to a record that was never written is a
// dead end a reader discovers later; a typed edge to one is worse, because it
// reads as a fact Babel checked. So an endpoint is resolved against the store
// that owns its namespace before anything is staged, and a target nothing
// vouches for is a write error rather than a dangling row.
//
// Nothing here trusts the caller's word about which namespaces exist. The
// registry is keyed by the namespaces a deployment actually registered, and an
// unregistered one is refused rather than admitted unvalidated: a gate that
// approves whatever it does not recognize is not a gate. The consequence is that
// a store opened with no resolvers refuses every append, which is the correct
// direction to fail - a corpus with no validated citations is recoverable, and
// one full of unvalidated ones is not.

// Errors callers match with errors.Is.
var (
	// ErrUnknownNamespace reports an endpoint whose record kind no resolver
	// claims. It is not "the record is missing": it is "nothing here can say
	// whether that record exists", which is a wiring fault or a caller
	// inventing a namespace, and both are refused on the same terms.
	ErrUnknownNamespace = errors.New("reference: no resolver for record namespace")

	// ErrNoSuchTarget reports an endpoint the owning store does not hold: the
	// hallucinated citation this whole file exists to refuse.
	ErrNoSuchTarget = errors.New("reference: edge endpoint names no record that exists")

	// ErrInvalidValue reports malformed input - an unknown edge kind, an
	// endpoint with no id, a note over the bound.
	ErrInvalidValue = errors.New("reference: invalid value")

	// ErrNotConfigured reports an operation on a store that was never opened.
	// A deployment may run without an edge store, and a caller holding a nil
	// one degrades rather than crashes: reads are empty and writes report
	// this, so an emission site that forgot its nil check reports a condition
	// instead of taking the process down.
	ErrNotConfigured = errors.New("reference: no edge store is configured")
)

// validNamespace bounds a record namespace. It is the shape internal/sharedcatalog
// admits in migrations/0008's plaintext column, restated here so a malformed
// namespace is refused at registration rather than at publication - a value the
// remote protocol would reject is a staged row that can never publish.
var validNamespace = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// maxIDLen bounds an endpoint identifier. It matches the Phase B identifier
// bound internal/sync stages against, for the same reason: an id the shared
// catalog will refuse must not become a durable edge first.
const maxIDLen = 128

// maxNoteLen bounds an edge's note.
//
// The note is the one field of an edge that is prose, and it is bounded rather
// than trimmed because an edge is immutable: a truncated note cannot be
// corrected, only superseded by another edge, so a caller that wrote too much
// should learn it at the write. The bound is generous for a sentence about why
// a link exists and far below anything that would make the sealed payload of an
// edge comparable to the record it cites.
const maxNoteLen = 4096

// Registry maps a record namespace to the store that can answer whether an id
// in it exists.
//
// Registration is explicit and per-deployment rather than a package-level
// default, because which namespaces exist is a property of what a machine has
// opened: a host running without the Reality Ledger cannot vouch for a
// reality_fact endpoint, and pretending otherwise would let an edge bind to a
// record this machine has never seen.
//
// It is safe for concurrent use. The web surface resolves on request
// goroutines while a command may still be registering, and a map read racing a
// map write is a crash rather than a wrong answer.
type Registry struct {
	mu     stdsync.RWMutex
	byKind map[string]Resolver
}

// NewRegistry returns an empty registry. Nothing is registered by default: a
// namespace exists here because a deployment opened the store that owns it.
func NewRegistry() *Registry { return &Registry{byKind: map[string]Resolver{}} }

// Register attaches a resolver to one record namespace.
//
// It refuses three things rather than accepting them quietly. A malformed
// namespace, because the value becomes a plaintext column and a vocabulary
// value. A nil resolver, because it would register a namespace that then
// refuses every endpoint in it, and "the store cannot answer" and "the record
// does not exist" must not read alike. And a second resolver for a namespace
// that already has one, because two stores claiming one namespace makes
// validation depend on which registration ran last.
func (r *Registry) Register(kind string, resolver Resolver) error {
	if r == nil {
		return ErrNotConfigured
	}
	if !validNamespace.MatchString(kind) {
		return fmt.Errorf("%w: record namespace %q must match %s",
			ErrInvalidValue, kind, validNamespace)
	}
	if resolver == nil {
		return fmt.Errorf("%w: namespace %q registered with no resolver", ErrInvalidValue, kind)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byKind == nil {
		r.byKind = map[string]Resolver{}
	}
	if _, taken := r.byKind[kind]; taken {
		return fmt.Errorf("%w: record namespace %q already has a resolver", ErrInvalidValue, kind)
	}
	r.byKind[kind] = resolver
	return nil
}

// Namespaces reports the registered namespaces, sorted. It is what an error
// message names so a caller who mistyped a namespace, or forgot to open a
// store, can see which ones this machine can vouch for.
func (r *Registry) Namespaces() []string {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.byKind))
	for kind := range r.byKind {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

// resolver reports the resolver for one namespace.
func (r *Registry) resolver(kind string) (Resolver, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	resolver, ok := r.byKind[kind]
	return resolver, ok
}

// require refuses an endpoint that no resolver claims or that its resolver says
// does not exist.
//
// A resolver's own failure - an unreadable database, a cancelled context - is
// neither of those and is reported as itself. The distinction matters at the
// call site: a refused endpoint is the caller's problem and a broken store is
// the machine's, and collapsing them would make an outage look like a
// hallucination.
func (r *Registry) require(ctx context.Context, what string, ref RecordRef) error {
	if !validNamespace.MatchString(ref.Kind) {
		return fmt.Errorf("%w: edge %s namespace %q must match %s",
			ErrInvalidValue, what, ref.Kind, validNamespace)
	}
	if len(ref.ID) > maxIDLen {
		return fmt.Errorf("%w: edge %s id is %d characters, over the %d-character bound",
			ErrInvalidValue, what, len(ref.ID), maxIDLen)
	}
	resolver, ok := r.resolver(ref.Kind)
	if !ok {
		return fmt.Errorf("%w: %s endpoint %s; this machine can resolve %v",
			ErrUnknownNamespace, what, ref, r.Namespaces())
	}
	exists, err := resolver.Exists(ctx, ref.ID)
	if err != nil {
		return fmt.Errorf("reference: resolve %s endpoint %s: %w", what, ref, err)
	}
	if !exists {
		return fmt.Errorf("%w: %s endpoint %s", ErrNoSuchTarget, what, ref)
	}
	return nil
}

// ResolverFunc adapts a function to Resolver, for a store whose existence check
// is one call and needs no state of its own.
type ResolverFunc func(ctx context.Context, id string) (bool, error)

func (f ResolverFunc) Exists(ctx context.Context, id string) (bool, error) { return f(ctx, id) }
