package reference

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// This file is the publication side of the edge store: it turns a row this
// package has just written into a staged Phase B record on its way to the
// shared catalog (SPEC.md §6.5, §9, §763).
//
// It is a separate file from store.go for the reason internal/frontier's
// publish.go is: store.go's job is that a durable write is atomic, immutable and
// anchored on this machine, and this file's job is that the same write becomes
// owed to the fleet in the same instant. The staging call is inside store.go's
// transaction - it has to be - but what is staged, and in what shape, is here.
//
// An edge publishes in two halves, and the split is the interesting part.
//
//   - The citation - the relation kind and both endpoint references - travels in
//     migrations/0008's plaintext columns. SPEC.md §763 admits kind and
//     identifier metadata, and a host without a payload key can therefore walk
//     the fleet's whole graph of what cites what (issue #112). It is the only
//     Phase B structure with a plaintext shape beyond an identity, and that is a
//     deliberate widening, argued in the migration.
//   - The note - the only prose an edge carries - travels sealed, in the object,
//     exactly like every other Phase B payload. An edge with a note explaining
//     why one record supersedes another is content, and content does not reach
//     PostgreSQL.
//
// Both halves are staged in the writer's transaction, so a crash between the
// durable write and the publication attempt leaves an edge that is durable,
// visibly pending-sync, and complete: `babel sync` publishes it later with the
// same endpoints, because they were recorded rather than re-derived.

// edgeRecordKind is the shared-catalog kind an edge publishes under.
//
// It is sharedcatalog.KindLink, and the choice is worth stating because a kind
// of its own is the tempting alternative. migrations/0003 holds the Phase B
// kind vocabulary in a database CHECK and closes it on purpose, and `link` is
// documented there as one of internal/frontier's records - a typed assertion
// that one record relates to another, which is exactly what this is. An edge is
// not a new kind of record; it is the corpus-wide form of the record `link`
// already names, and internal/frontier's own typed links are its narrower
// predecessor.
//
// A kind of its own would cost a migration to the 0003 CHECK and would tell a
// reader on another host to expect a record shape that differs from what the
// bytes hold. Neither is taken here.
const edgeRecordKind = sharedcatalog.KindLink

// publication is what a write path learns inside its transaction and acts on
// after it: the closure to publish, and whether there is one to publish yet.
//
// There often is not. An edge a run emitted joins that run's closure and
// publishes nothing, because migration 0003 fixes a run's record_count at
// declaration and never lets it move - so a closure may not be declared while
// it can still grow. The run declares and publishes itself when it ends.
type publication struct {
	closure babelsync.Closure
	publish bool
}

// stage stages one edge inside the caller's transaction.
//
// A nil hook makes this a no-op, which is what local-only mode is: the edge is
// durable, nothing is owed to the fleet, and no write path branches on it.
func (s *Store) stage(ctx context.Context, tx *sql.Tx, producedBy string, rec babelsync.Record) (publication, error) {
	if s.sync == nil {
		return publication{}, nil
	}
	closure, publish, err := s.sync.Append(ctx, tx, producedBy, rec)
	if err != nil {
		return publication{}, fmt.Errorf("reference: stage edge %s for publication: %w", rec.EntityID, err)
	}
	return publication{closure: closure, publish: publish}, nil
}

// commit attempts to publish what stage staged, after the writer's transaction
// has committed.
//
// It is best-effort by contract. internal/sync returns nil for every transient
// failure - an unreachable catalog, a refused object write, a closure the
// catalog does not yet hold in full - and hands one diagnostic line to the
// command surface, because SPEC.md §6.5 makes publication a step that may be
// completed later and never a step a local write depends on. A returned error is
// a caller bug in this file, and a test must not swallow it.
func (s *Store) commit(ctx context.Context, p publication) error {
	if s.sync == nil || !p.publish {
		return nil
	}
	return s.sync.CommitInline(ctx, p.closure)
}

// producingRun reports the run whose closure an edge belongs to, and the empty
// string for an edge no run produced.
//
// An edge asserted by a run is part of that run's output: a citation the
// exploration emitted alongside the finding it supports, which publishes with
// it. An edge an operator or a migration asserted is its own closure of one -
// nobody resumes an operator's act, so a record staged into someone else's
// closed closure would stay pending forever. internal/sync's Append resolves
// which of the two it is; this only reports the actor's run, and only when the
// actor is one.
func producingRun(e Edge) string {
	if e.ActorKind == ActorRun {
		return e.ActorRef
	}
	return ""
}

// The actor vocabulary the frozen contract's ActorKind field ranges over. It is
// three values rather than free text because attribution that cannot be
// compared is attribution nobody can audit, and it matches internal/run's
// receipt discipline (#96): a link is asserted by a person, by a run, or by
// Babel's own machinery, and the third is never dressed as either of the others.
const (
	// ActorOperator is a person, and ActorRef is that person's identity.
	ActorOperator = "operator"
	// ActorRun is an exploration or one of its stages, and ActorRef is the run
	// id - which is also the closure the edge publishes in.
	ActorRun = "run"
	// ActorSystem is Babel itself: an absorption, a migration, a revision mint.
	// ActorRef is empty or names the subsystem.
	ActorSystem = "system"
)

// publishedEdge is the canonical publication shape of one edge.
//
// It carries the endpoints and the actor as well as the note, even though the
// endpoints also travel as plaintext columns, and the duplication is deliberate:
// the object is the record and the columns are a projection of it for a reader
// that cannot open the object. A host that can decrypt must never have to join
// against PostgreSQL to learn what an edge asserts, and the two can never
// disagree because both are written from this struct in one transaction.
//
// The timestamp travels as the same fixed-width text the row holds, so the
// published bytes and the row agree literally; timestampLayout keeps a
// nine-digit fraction precisely so text order and time order cannot disagree,
// and encoding/json's default time rendering trims it.
type publishedEdge struct {
	Schema int    `json:"schema"`
	ID     string `json:"id"`
	Kind   Kind   `json:"kind"`
	// From and To are the endpoints as they were resolved: a namespace and
	// that namespace's durable identifier. They travel because an edge whose
	// endpoints a reader has to reconstruct is not a citation, and because a
	// second instance reviewing this machine's output resolves them against
	// its own stores.
	From RecordRef `json:"from"`
	To   RecordRef `json:"to"`
	// ActorKind and ActorRef say who asserted the link. They are in the sealed
	// half rather than the plaintext columns on purpose: attribution is
	// plaintext-eligible for a run, but an operator identity is a person's
	// name in a managed provider's database, and the graph is navigable
	// without it.
	ActorKind string `json:"actor_kind"`
	ActorRef  string `json:"actor_ref,omitempty"`
	// Note is the edge's only prose, and the reason this record is sealed at
	// all. Absent when the edge speaks for itself.
	Note      string `json:"note,omitempty"`
	CreatedAt string `json:"created_at"`
}

// validate refuses a published edge a reader could not act on.
//
// It runs in the marshaller rather than at the call site, on internal/disposition's
// terms: these bytes become an immutable, sealed object in the shared catalog,
// and a malformed one cannot be corrected there - 0003's analysis_records is
// insert-only - so the only place a refusal costs nothing is before the
// transaction that stages it.
func (p publishedEdge) validate() error {
	switch {
	case p.Schema < 1:
		return fmt.Errorf("%w: a published edge carries its schema version", ErrInvalidValue)
	case p.ID == "":
		return fmt.Errorf("%w: a published edge carries its id", ErrInvalidValue)
	case !p.Kind.Valid():
		return fmt.Errorf("%w: edge kind %q", ErrInvalidValue, p.Kind)
	case p.From.Kind == "" || p.From.ID == "" || p.To.Kind == "" || p.To.ID == "":
		return fmt.Errorf("%w: a published edge names both endpoints", ErrInvalidValue)
	case p.ActorKind == "":
		// An unattributed citation would record that something cites
		// something without recording that anyone asserted it, which is the
		// one thing the provenance half of #113 rests on.
		return fmt.Errorf("%w: a published edge names who asserted it", ErrInvalidValue)
	}
	if _, err := parseTime(p.CreatedAt); err != nil {
		return fmt.Errorf("%w: published edge %s: %v", ErrInvalidValue, p.ID, err)
	}
	return nil
}

// MarshalJSON encodes the edge after validating it.
func (p publishedEdge) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	// The local type drops the method, which is what keeps this from recursing
	// into itself.
	type wire publishedEdge
	return json.Marshal(wire(p))
}

// marshalPayload encodes the §9 encryption-bound half of an edge, which is also
// exactly the bytes stored in payload_json locally.
//
// One shape serves both, and that is the point: the local row and the published
// object are the same record, so a reader that decrypts an object on another
// host sees what this machine stored rather than a re-serialization of it. The
// validating marshaller therefore also guards the local write - a malformed
// edge never becomes a durable row either.
func marshalPayload(e Edge) ([]byte, error) {
	encoded, err := json.Marshal(publishedEdge{
		Schema:    RecordSchema,
		ID:        e.ID,
		Kind:      e.Kind,
		From:      e.From,
		To:        e.To,
		ActorKind: e.ActorKind,
		ActorRef:  e.ActorRef,
		Note:      e.Note,
		CreatedAt: formatTime(e.CreatedAt),
	})
	if err != nil {
		return nil, fmt.Errorf("reference: encode edge payload: %w", err)
	}
	return encoded, nil
}

// unmarshalNote recovers the note from a stored payload.
//
// Only the note is read back, because every other field of the payload is also
// a column: reading them from the payload instead would be reading the same
// fact twice, which is how two readers of one row eventually disagree.
func unmarshalNote(payload []byte) (string, error) {
	var stored struct {
		Note string `json:"note"`
	}
	if err := json.Unmarshal(payload, &stored); err != nil {
		return "", fmt.Errorf("decode edge payload: %w", err)
	}
	return stored.Note, nil
}

// stagedEdge builds the Phase B record internal/sync stages: the sealed payload
// plus the plaintext citation columns.
//
// The payload is the bytes the local row holds, passed through rather than
// re-encoded, so the object and the row are byte-identical and nothing has to
// argue that two encodings agree.
func stagedEdge(e Edge, payload []byte) (babelsync.Record, error) {
	edge := &sharedcatalog.RecordEdge{
		Kind:     sharedcatalog.EdgeKind(e.Kind),
		FromKind: e.From.Kind,
		FromID:   e.From.ID,
		ToKind:   e.To.Kind,
		ToID:     e.To.ID,
	}
	// The conversion above is the one place this package's vocabulary meets the
	// catalog's, and it is checked rather than assumed: a kind this build
	// defines that migrations/0008's CHECK does not admit would otherwise be
	// caught by PostgreSQL, after the edge was already durable and staged.
	if err := edge.Validate(); err != nil {
		return babelsync.Record{}, fmt.Errorf("%w: %s", ErrInvalidValue, err)
	}
	return babelsync.Record{
		EntityID: e.ID,
		Kind:     edgeRecordKind,
		Schema:   RecordSchema,
		Payload:  payload,
		Edge:     edge,
	}, nil
}
