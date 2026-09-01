package complaint

import (
	"context"
	"fmt"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reference"
)

// This file is the emission half of issue #113 for the two link forms a
// complaint has: the amendment chain, and what the operator said the complaint
// is about.
//
// Neither edge is a new fact. The ancestor column already says which wording an
// amendment replaced, and TellInput.Addresses is the operator's own statement
// made at capture; an edge is their graph-visible shadow, so a reader asking
// "what cites what" gets one answer for every record kind instead of one query
// per table.
//
// The direction of an addresses edge is worth stating, because #115 describes
// the opposite one. A hypothesis that answers a complaint mints
// hypothesis -> complaint, and that is the edge a complaint's page reads as a
// backlink to answer "was this ever addressed?". The edge minted here is the
// operator's own aim - complaint -> hypothesis, "this is what I am complaining
// about" - and it is deliberately not read as an answer: nothing the operator
// wrote can be evidence that Babel did anything about it.
//
// Emission happens after the complaint's transaction has committed, never
// inside it. The edge store is a separate component of the same durable file
// with its own connection, so appending from inside this package's write
// transaction would deadlock against the write lock that transaction holds -
// and, more to the point, a complaint must not fail to be captured because its
// shadow could not be written. An operator who was told their complaint was
// refused would say it somewhere Babel cannot read.
//
// A failure is a warning and never an error. The complaint exists, the
// authoritative row exists, and the graph is missing one edge until something
// re-emits it - which the next append of the same (kind, from, to) does, because
// Appender.Append is idempotent on that triple by contract.

// WithReferences attaches the typed reference graph's write half (#113).
//
// Without it - and that is the default - this store captures complaints and
// mints no edge. A nil Appender is therefore a supported deployment and not a
// degraded one, on the same reasoning WithSync leaves local-only mode a
// first-class mode.
//
// diag receives one emission failure at a time, for the same reason
// internal/sync takes a func rather than an io.Writer: the value inside the
// error may carry a store's own words, and only the command surface owns the
// terminal-safe renderer that may put those on a terminal (SPEC.md §8). A nil
// diag drops the warning, which is the honest consequence of a caller that
// asked for edges and offered nowhere to report them.
func WithReferences(a reference.Appender, diag func(error)) Option {
	return func(s *Store) {
		s.refs = a
		s.refsDiag = diag
	}
}

// mintSupersedes records the graph shadow of one amendment.
//
// No note: the complaint's own text is why it was amended, and a sentence here
// would be this package narrating the operator's edit.
func (s *Store) mintSupersedes(ctx context.Context, c Complaint) {
	if s.refs == nil || c.AncestorID == "" {
		return
	}
	s.appendEdge(ctx, reference.Edge{
		Kind:      reference.KindSupersedes,
		From:      recordRef(c.ID),
		To:        recordRef(c.AncestorID),
		ActorKind: reference.ActorOperator,
		ActorRef:  c.By,
	})
}

// mintAddresses records what the operator said the complaint is about.
//
// A target named twice is collapsed, because the operator naming a record twice
// is one aim stated twice and the graph should hold one edge for it. A target
// that names nothing is skipped rather than refused: the complaint is what
// mattered, and there is nothing an operator could do about a malformed ref
// except tell Babel again.
func (s *Store) mintAddresses(ctx context.Context, c Complaint, targets []frontier.Ref) {
	if s.refs == nil || len(targets) == 0 {
		return
	}
	seen := make(map[frontier.Ref]struct{}, len(targets))
	for _, target := range targets {
		if target.Type == "" || target.ID == "" {
			continue
		}
		if _, dup := seen[target]; dup {
			continue
		}
		seen[target] = struct{}{}
		s.appendEdge(ctx, reference.Edge{
			Kind:      reference.KindAddresses,
			From:      recordRef(c.ID),
			To:        reference.RecordRef{Kind: string(target.Type), ID: target.ID},
			ActorKind: reference.ActorOperator,
			ActorRef:  c.By,
		})
	}
}

// appendEdge appends one edge and reports a refusal as a warning.
//
// The error is wrapped with the edge's shape rather than with its note, because
// the shape is what an operator needs to re-emit or to recognize a resolver
// that has lost a record, and the note is content.
func (s *Store) appendEdge(ctx context.Context, e reference.Edge) {
	if _, err := s.refs.Append(ctx, e); err != nil && s.refsDiag != nil {
		s.refsDiag(fmt.Errorf("complaint: record the %s edge from %s to %s: %w",
			e.Kind, e.From, e.To, err))
	}
}
