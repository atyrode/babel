package complaint

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/atyrode/babel/internal/sync"
)

// complaintKind is the shared-catalog kind this package's records publish
// under.
//
// migrations/0003 holds the Phase B kind vocabulary in a database CHECK and it
// is closed on purpose: a record type reaching PostgreSQL under a new kind
// costs a migration and a review. Both were taken for this one -
// migrations/0011 widens the CHECK - because a complaint is genuinely not one
// of the kinds that already existed. Publishing it as a `context` record would
// have avoided the migration and made every fleet reader unable to tell the
// operator's own steering from a run's assembled background.
const complaintKind = sharedcatalog.KindComplaint

// Option configures a Store at Open time.
//
// Publication and the reference graph are both options rather than parameters
// because neither is a property every caller has: a local-only deployment opens
// exactly the store it would have opened without them, and a caller that
// publishes nothing should not have to name the thing it is not doing.
type Option func(*Store)

// WithSync attaches the shared-catalog publication hook, which is what turns
// this store's durable writes into Phase B records the fleet receives
// (SPEC.md §6.5, §9).
//
// Without it the store stages nothing and publishes nothing. That is local-only
// mode, and it is a supported deployment rather than a degraded one: the
// durable rows are the same rows either way, and the only difference is whether
// anything is recorded as owed to the fleet.
func WithSync(h sync.Hook) Option {
	return func(s *Store) { s.sync = h }
}

// stage records rec for the shared catalog inside tx, which is the transaction
// that is making the complaint itself durable, and reports the closure to
// publish once tx has committed.
//
// It shares the writer's transaction rather than following it. A complaint that
// committed locally while its journal row did not would be durable, invisible
// to the publisher, and reported by nothing - which for this package means
// steering this machine believes it captured and the fleet will never hear
// about. So a staging failure rolls the durable write back with it: a record
// nobody can publish and nobody reports as pending is worse than a refused
// write, because the refusal is visible and the silence is not.
func (s *Store) stage(ctx context.Context, tx *sql.Tx, producedBy string, rec sync.Record) (sync.Closure, bool, error) {
	if s.sync == nil {
		return sync.Closure{}, false, nil
	}
	closure, publish, err := s.sync.Append(ctx, tx, producedBy, rec)
	if err != nil {
		return sync.Closure{}, false, fmt.Errorf("stage complaint %s for publication: %w", rec.EntityID, err)
	}
	return closure, publish, nil
}

// publishedComplaint is the canonical publication shape of one wording.
//
// Unlike its siblings this struct is entirely sealed, and that is a property of
// the record rather than an omission. A hypothesis publishes identity and
// subject in plaintext columns beside a sealed payload because a fleet reader
// needs to place it without opening it; a complaint's whole substance is the
// operator's sentence, and migrations/0003 gives analysis_records no per-kind
// columns at all - the kind and the record id in the clear are exactly what
// §763 admits, and everything here rides inside the object.
//
// The chain travels with the record. A reader on another host that received an
// amendment and not the wording it replaced must still be able to tell that it
// is holding a revision rather than a second complaint, which RootID and
// Sequence say and a timestamp comparison cannot.
//
// Timestamps travel as the column's own text rather than a re-encoded
// time.Time, so the published bytes and the row agree literally: this package's
// timestampLayout keeps a fixed nine-digit fraction precisely so text order and
// time order cannot disagree, and encoding/json's default rendering trims it.
//
// Nothing derived travels. There is no head flag, because headship is a
// property of the chain as the receiving host holds it, and a sender's opinion
// about it would go stale the moment the next amendment published. There is no
// addressed-record list either: those are edges, they publish as edges through
// internal/reference's own path, and a second copy here would be a set free to
// disagree with the graph.
type publishedComplaint struct {
	ID         string `json:"id"`
	RootID     string `json:"root_id"`
	AncestorID string `json:"ancestor_id,omitempty"`
	Sequence   int    `json:"sequence"`
	OperatorID string `json:"operator_id"`
	HostID     string `json:"host_id"`
	// Redacted travels because it is the difference between a placeholder
	// Babel wrote and one the operator typed, and a reader that could not
	// tell them apart would attribute Babel's redaction to the operator.
	Redacted  bool   `json:"redacted"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

// validate refuses a published complaint a reader could not attribute or place.
func (p publishedComplaint) validate() error {
	switch {
	case p.ID == "":
		return fmt.Errorf("%w: published complaint carries no id", ErrInvalidValue)
	case p.RootID == "":
		return fmt.Errorf("%w: complaint %s names no chain", ErrInvalidValue, p.ID)
	case p.Sequence < 1:
		return fmt.Errorf("%w: complaint %s is at position %d of its chain",
			ErrInvalidValue, p.ID, p.Sequence)
	case p.Sequence == 1 && p.AncestorID != "":
		return fmt.Errorf("%w: complaint %s is a first wording that amends %s",
			ErrInvalidValue, p.ID, p.AncestorID)
	case p.Sequence > 1 && p.AncestorID == "":
		return fmt.Errorf("%w: complaint %s is at position %d and amends nothing",
			ErrInvalidValue, p.ID, p.Sequence)
	case p.OperatorID == "":
		return fmt.Errorf("%w: complaint %s is unattributed", ErrInvalidValue, p.ID)
	case p.HostID == "":
		return fmt.Errorf("%w: complaint %s names no capture host", ErrInvalidValue, p.ID)
	case p.Text == "":
		return fmt.Errorf("%w: complaint %s carries no words", ErrInvalidValue, p.ID)
	case p.CreatedAt == "":
		return fmt.Errorf("%w: complaint %s carries no capture time", ErrInvalidValue, p.ID)
	}
	return nil
}

// MarshalJSON encodes the complaint after validating it.
//
// Validating in the marshaller rather than at the call site is what makes the
// check unskippable: these bytes become an immutable, sealed object in the
// shared catalog, and a malformed one cannot be corrected there -
// migrations/0003's analysis_records is insert-only - so the only place a
// refusal costs nothing is before the transaction that stages it.
func (p publishedComplaint) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	type wire publishedComplaint
	encoded, err := marshalPayload(wire(p))
	if err != nil {
		return nil, fmt.Errorf("encode complaint %s: %w", p.ID, err)
	}
	return encoded, nil
}

// Namespace is the record namespace a complaint occupies in the typed reference
// graph, and the value internal/reference's resolver registry is keyed by.
//
// It is exported from here rather than spelled at each emission site because
// three packages mint edges that name a complaint - this one, the frontier when
// a hypothesis addresses one, and the web surface when an operator links them
// by hand - and a namespace spelled three times is a namespace that eventually
// differs at one of them.
const Namespace = "complaint"

// recordRef addresses one of this store's records in the reference graph.
func recordRef(id string) reference.RecordRef {
	return reference.RecordRef{Kind: Namespace, ID: id}
}
