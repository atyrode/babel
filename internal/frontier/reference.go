package frontier

import (
	"context"
	"fmt"

	"github.com/atyrode/babel/internal/reference"
)

// This file is the emission half of issue #113 for the two link forms this
// package already stores: the revision chain of #87, and the dedup path's
// duplicate warnings.
//
// Neither edge is a new fact. frontier_revision.supersedes_id already says
// which wording a revision replaced, and frontier_duplicate_warning already
// says which head a candidate resembled when it was written. Those rows stay
// the authority - the chain orders history, the warning carries the overlap -
// and an edge is their graph-visible shadow, so a reader asking "what cites
// what" gets one answer for every record kind instead of one query per table.
//
// Two consequences follow from being a shadow, and both are deliberate.
//
// Emission happens after the record's transaction has committed, never inside
// it. The edge store is a separate component of the same durable file with its
// own connection, so appending from inside this package's write transaction
// would deadlock against the write lock that transaction holds - and, more to
// the point, a record must not fail to be durable because its shadow could
// not be written. §5.2 requires every emitted candidate to be persisted.
//
// A failure is a warning and never an error. The record exists, the
// authoritative row exists, and the graph is missing one edge until something
// re-emits it - which the next append of the same (kind, from, to) does,
// because Appender.Append is idempotent on that triple by contract.

// WithReferences attaches the typed reference graph's write half (#113).
//
// Without it - and that is the default - this store behaves exactly as it did
// before #113: records are written, chains are appended, warnings are
// recorded, and no edge is minted. A nil Appender is therefore a supported
// deployment and not a degraded one, on the same reasoning WithSync leaves
// local-only mode a first-class mode.
//
// diag receives one emission failure at a time, for the same reason
// internal/sync takes a func rather than an io.Writer: the value inside the
// error may carry a store's or a remote endpoint's own words, and only the
// command surface owns the terminal-safe renderer that may put those on a
// terminal (SPEC.md §8). A nil diag drops the warning, which is the honest
// consequence of a caller that asked for edges and offered nowhere to report
// them.
func WithReferences(a reference.Appender, diag func(error)) Option {
	return func(s *Store) {
		s.refs = a
		s.refsDiag = diag
	}
}

// edgeNoteDuplicates is the hedge every duplicates edge carries.
//
// It is a fixed string rather than a rendering of the overlap, and that is the
// point twice over. The number lives on the warning row, which stays the
// authority, so restating it here would be a second copy free to disagree with
// the first. And the edge kind alone reads as a verdict to anybody browsing
// the graph - especially on a host without payload keys, where the warning's
// own payload is sealed and this note is not (SPEC.md §763 admits the kind and
// the endpoints in the clear) - so the edge says in words what the heuristic
// actually established, which is a resemblance worth comparing and nothing
// more.
const edgeNoteDuplicates = "candidate resemblance recorded by the dedup heuristic: " +
	"a prompt to compare the two records, never a finding that they say the same thing"

// edgeNoteAddresses is the hedge every addresses edge from a proposal carries.
//
// #114 separates a claim about how things are from a suggested change, and the
// separation is only worth anything if the second is never read with the
// first's authority. So the note says what the edge actually establishes: that
// somebody proposed this change in answer to that claim, which is a want and
// an option rather than a verified fact, and that the two records are reviewed
// independently - rejecting the remedy leaves the claim standing, and
// accepting the claim authorizes nothing about the remedy.
const edgeNoteAddresses = "suggested change offered in answer to this claim: " +
	"a want or an option under separate review, never a verified fact and never " +
	"a disposition of the claim itself"

// recordRef addresses one of this store's records in the reference graph.
//
// The namespace is the entity kind verbatim, so "hypothesis", "observation",
// "finding" and "proposal" mean in the graph exactly what they mean in this
// package's tables and in every identifier an operator has already pasted.
func recordRef(kind EntityType, id string) reference.RecordRef {
	return reference.RecordRef{Kind: string(kind), ID: id}
}

// mintSupersedes records the graph shadow of one revision.
//
// It is a no-op for an original: a first revision replaces nothing, and an
// edge from a record to the empty string would be a dangling link asserting
// that it has a history it does not have.
//
// The actor is the revision's own, resolved by revisionActor, so an operator's
// reword carries "operator" and a run's carries "run" without either call site
// having to say so a second time - which is what keeps the graph's attribution
// and the chain's from ever disagreeing.
func (s *Store) mintSupersedes(ctx context.Context, kind EntityType, id, ancestorID string, actor Actor) {
	if s.refs == nil || ancestorID == "" {
		return
	}
	// No note. The reason this revision exists is on the revision, which is
	// where #87 put it and where an operator reads it; copying the sentence
	// onto the edge would make one argument exist twice.
	s.appendEdge(ctx, reference.Edge{
		Kind:      reference.KindSupersedes,
		From:      recordRef(kind, id),
		To:        recordRef(kind, ancestorID),
		ActorKind: string(actor.Kind),
		ActorRef:  actor.ID,
	})
}

// mintDuplicates records the graph shadow of the dedup path's warnings.
//
// One edge per warning the write actually recorded, rather than per
// near-duplicate the caller offered: appendDuplicateWarnings collapses a
// target named twice and refuses one that names nothing, so taking its output
// keeps the graph and the table describing the same set.
func (s *Store) mintDuplicates(ctx context.Context, id string, warnings []DuplicateWarning, actor Actor) {
	if s.refs == nil {
		return
	}
	for _, warning := range warnings {
		s.appendEdge(ctx, reference.Edge{
			Kind:      reference.KindDuplicates,
			From:      recordRef(EntityHypothesis, id),
			To:        recordRef(EntityHypothesis, warning.DuplicateOf),
			ActorKind: string(actor.Kind),
			ActorRef:  actor.ID,
			Note:      edgeNoteDuplicates,
		})
	}
}

// mintAddresses records the graph shadow of what a proposal answers (#114).
//
// One edge per hypothesis the proposal reaches, whichever form it has. A
// candidate proposal reaches the claims its own rows assert; a consolidated
// proposal reaches them transitively through its findings' observations. Both
// are `addresses` because both say the same thing in the graph - this suggested
// change answers that claim - and a reader browsing the corpus should not have
// to know which table established it to see that it holds.
//
// The ids are the record's own derived HypothesisIDs rather than the caller's
// input, for the reason mintDuplicates takes appendDuplicateWarnings' output:
// the derivation de-duplicates and orders, so the graph and the rows describe
// one set rather than two.
//
// The note is the epistemic rule #114 extends, stated on the edge because the
// edge is what survives on a host with no payload key: the kind and both
// endpoints are plaintext (SPEC.md §763) while the proposal's own words are
// sealed, so without it a keyless reader would see an arrow into a claim and
// nothing telling them the arrow is a want rather than a verdict.
func (s *Store) mintAddresses(ctx context.Context, id string, hypothesisIDs []string, actor Actor) {
	if s.refs == nil {
		return
	}
	for _, hypothesisID := range hypothesisIDs {
		s.appendEdge(ctx, reference.Edge{
			Kind:      reference.KindAddresses,
			From:      recordRef(EntityProposal, id),
			To:        recordRef(EntityHypothesis, hypothesisID),
			ActorKind: string(actor.Kind),
			ActorRef:  actor.ID,
			Note:      edgeNoteAddresses,
		})
	}
}

// appendEdge appends one edge and reports a refusal as a warning.
//
// The error is wrapped with the edge's shape rather than with its note,
// because the shape is what an operator needs to re-emit or to recognize a
// resolver that has lost a record, and the note is content.
func (s *Store) appendEdge(ctx context.Context, e reference.Edge) {
	if _, err := s.refs.Append(ctx, e); err != nil && s.refsDiag != nil {
		s.refsDiag(fmt.Errorf("frontier: record the %s edge from %s to %s: %w",
			e.Kind, e.From, e.To, err))
	}
}
