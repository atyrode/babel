// Package reference defines Babel's typed record-reference graph (issue
// #113): append-only edges that let any Babel record cite any other.
//
// Edges are structured, provenance-bearing records - never free-text links
// inside record bodies. A write MUST validate both endpoints against the
// caller's Resolver so an edge can only bind to a record that demonstrably
// exists: the draft-issue anchoring rule applied to the corpus itself. A
// hallucinated target is a write error, not a dangling link.
//
// Publication (SPEC §763): Kind and both RecordRef endpoints are
// identifier/kind metadata and plaintext-eligible in the shared catalog;
// Note is content and MUST travel envelope-encrypted. The graph's shape is
// therefore navigable fleet-wide even on a host without payload keys (#112):
// sealed records still show where they sit in the web of citations.
package reference

import (
	"context"
	"fmt"
	"time"
)

// Kind names the meaning of an edge. The set is closed: an unknown kind is
// rejected at write time, so a model cannot mint novel relation semantics.
type Kind string

const (
	// KindEvidence - From rests on To as supporting material. To is usually
	// a session revision; the byte-precise locator inside that session stays
	// on the citing record itself, not on the edge.
	KindEvidence Kind = "evidence"
	// KindSupersedes - From replaces To at the head of a revision chain
	// (#87). Emitted wherever a revision is minted; the chain tables remain
	// the ordering authority, the edge is its graph-visible shadow.
	KindSupersedes Kind = "supersedes"
	// KindRefines - From narrows, corrects, or extends To without
	// replacing it.
	KindRefines Kind = "refines"
	// KindAddresses - From answers or acts on To: proposal -> hypothesis,
	// output -> complaint (#114, #115).
	KindAddresses Kind = "addresses"
	// KindInspiredBy - From grew out of To: retrieval injection during
	// preparation, serendipitous adjacency.
	KindInspiredBy Kind = "inspired_by"
	// KindDuplicates - From records the same idea as To; the dedup path's
	// explicit trace.
	KindDuplicates Kind = "duplicates"
)

// Valid reports whether k is one of the closed set of edge kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindEvidence, KindSupersedes, KindRefines, KindAddresses,
		KindInspiredBy, KindDuplicates:
		return true
	}
	return false
}

// RecordRef addresses one record of any Babel kind. Kind is the record
// namespace ("session", "hypothesis", "observation", "finding", "proposal",
// "complaint", "receipt", "run", "disposition", "reality_fact", ...) as each
// store names itself in the resolver registry; ID is that store's durable
// identifier - for sessions, the durable session key.
type RecordRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

func (r RecordRef) String() string { return r.Kind + ":" + r.ID }

// Edge is one append-only assertion that From relates to To. Edges are never
// updated or deleted; a wrong link is answered by a later edge (or a revision
// of the citing record), matching the corpus-wide append-only discipline.
type Edge struct {
	ID   string    `json:"id"`
	Kind Kind      `json:"kind"`
	From RecordRef `json:"from"`
	To   RecordRef `json:"to"`
	// ActorKind and ActorRef record who asserted the link: "operator",
	// "run" (ActorRef is the run ID), or "system" (absorptions and
	// migrations). Same attribution discipline as run receipts (#96).
	ActorKind string `json:"actor_kind"`
	ActorRef  string `json:"actor_ref,omitempty"`
	// Note is optional free text about why the link exists. Content bytes:
	// encrypted on publication, sanitized on render.
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Validate checks the shape rules that hold regardless of storage: known
// kind, both endpoints named, no self-reference.
func (e Edge) Validate() error {
	if !e.Kind.Valid() {
		return fmt.Errorf("reference: unknown edge kind %q", e.Kind)
	}
	if e.From.Kind == "" || e.From.ID == "" || e.To.Kind == "" || e.To.ID == "" {
		return fmt.Errorf("reference: edge %s must name both endpoints", e.Kind)
	}
	if e.From == e.To {
		return fmt.Errorf("reference: edge %s from %s to itself", e.Kind, e.From)
	}
	if e.ActorKind == "" {
		return fmt.Errorf("reference: edge %s carries no actor", e.Kind)
	}
	return nil
}

// Resolver reports whether a record exists in one record namespace. Each
// store registers one; the edge store consults the registry before any
// append so links bind only to records that exist.
type Resolver interface {
	Exists(ctx context.Context, id string) (bool, error)
}

// Appender is the write half consumed by emission sites (revision minting,
// evidence absorption, preparation injection, dispositions). The concrete
// store implements it; callers hold the interface so emission and storage
// build independently.
type Appender interface {
	// Append validates, persists, and returns the edge with its minted ID
	// and CreatedAt. Appending an edge identical in (Kind, From, To) to one
	// already recorded is idempotent, not an error: emitters retry.
	Append(ctx context.Context, e Edge) (Edge, error)
}

// Lister is the read half consumed by render surfaces: outgoing links and
// backlinks for one record, newest first.
type Lister interface {
	From(ctx context.Context, ref RecordRef) ([]Edge, error)
	To(ctx context.Context, ref RecordRef) ([]Edge, error)
}
