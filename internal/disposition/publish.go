package disposition

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sharedcatalog"
	"github.com/atyrode/babel/internal/sync"
)

// The shared-catalog kinds this package's durable records publish under.
//
// migrations/0003 holds the Phase B kind vocabulary in a database CHECK, and it
// is closed on purpose: a record type reaching PostgreSQL under a new kind
// costs a migration and a review. Neither is taken here, so each record this
// package writes maps onto a kind that already exists, and the mapping is named
// once rather than spelled out at three write sites.
const (
	// dispositionKind carries both halves of this package's ledger: the
	// proposed next action and every operator answer appended to it. That is
	// the kind's documented meaning in 0003 — a disposition, and the
	// append-only review material attached to a record — and the two are one
	// kind rather than two because an answer is not readable without the
	// action it answers, so a reader that can decode one needs to decode both.
	dispositionKind = sharedcatalog.KindDisposition

	// invitationKind is sharedcatalog.KindContext, and the choice is worth
	// stating because KindDisposition is the tempting one.
	//
	// An invitation here is not a disposition of anything. It carries no
	// proposed action, no ruling, and by construction no words at all: #87's
	// nudge says a record deserves attention and refuses to say what to do
	// about it, which is why disposition_invitation is the one table in this
	// package with no payload column. What it does carry is one person's
	// authority pointed at one record — #96 puts an invitation above the
	// conductor's own policy — and 0003 documents KindContext as attributed
	// operator guidance, which is exactly that: from a person, attributable,
	// and guidance rather than instruction.
	//
	// Publishing it as KindDisposition would tell a reader on another host to
	// expect a decision and a vocabulary value that are not in the bytes. A
	// kind of its own is the honest third answer, and it is a migration and a
	// review this change deliberately does not take.
	invitationKind = sharedcatalog.KindContext
)

// Option configures a Store at Open time.
//
// It is a variadic option rather than a parameter because publication is not a
// property every caller has: a local-only deployment opens exactly the store it
// opened before this file existed, and a caller that never publishes should not
// have to name the thing it is not doing.
type Option func(*Store)

// WithSync attaches the shared-catalog publication hook, which is what turns
// this store's durable writes into Phase B records the fleet receives
// (SPEC.md §6.5, §9).
//
// Without it the store stages nothing and publishes nothing. That is
// local-only mode, and it is a supported deployment rather than a degraded one:
// the durable rows are the same rows either way, and the only difference is
// whether anything is recorded as owed to the fleet.
func WithSync(h sync.Hook) Option {
	return func(s *Store) { s.sync = h }
}

// stage records rec for the shared catalog inside tx, which is the transaction
// that is making the record itself durable, and reports the closure to publish
// once tx has committed.
//
// It shares the writer's transaction rather than following it. A record that
// committed locally while its journal row did not would be durable, invisible
// to the publisher, and reported by nothing — which for this package means an
// operator's decision that this machine believes it recorded and the fleet will
// never hear about. So a staging failure rolls the durable write back with it:
// a record nobody can publish and nobody reports as pending is worse than a
// refused write, because the refusal is visible and the silence is not.
//
// A store opened without WithSync stages nothing, so the reported flag is never
// true for one and a caller may reach s.sync directly underneath it. Each write
// path also checks the hook before it encodes a record, so a local-only store
// does not build publication bytes nobody reads; the check here is what makes
// that flag's guarantee a property of this method rather than of three call
// sites that have to keep agreeing.
//
// It is a method rather than the same lines at each write path, for the reason
// sync.Append itself gives: a rule restated at every write site is a rule that
// eventually differs at one of them.
func (s *Store) stage(ctx context.Context, tx *sql.Tx, producedBy string, rec sync.Record) (sync.Closure, bool, error) {
	if s.sync == nil {
		return sync.Closure{}, false, nil
	}
	closure, publish, err := s.sync.Append(ctx, tx, producedBy, rec)
	if err != nil {
		return sync.Closure{}, false, fmt.Errorf("stage %s for publication: %w", rec.EntityID, err)
	}
	return closure, publish, nil
}

// publishedDisposition is the canonical publication shape of one proposed next
// action.
//
// It exists because disposition_proposal's payload_json holds only the §9
// encryption-bound half of the row — a summary, a rationale, a repository
// anchor — while the action's identity, its subject, its proposer and its
// moment live in sibling plaintext columns. Staging payload_json alone would
// publish prose no reader on another host could attribute to a record, an
// author, or a time.
//
// Timestamps travel as the column's own text rather than a re-encoded
// time.Time, so the published bytes and the row agree literally: this package's
// timestampLayout keeps a fixed nine-digit fraction precisely so text order and
// time order cannot disagree, and encoding/json's default rendering trims it.
//
// Nothing derived travels. Status is absent because it is derived from the
// ledger rather than stored, and shipping it would move the drift this package
// refuses locally onto the wire, where a reader holding both the status and the
// entries behind it has two answers and no way to choose. The schema version is
// absent for the same reason: migration 0003 carries it in
// analysis_records.record_schema, beside the object these are the plaintext of.
type publishedDisposition struct {
	ID         string              `json:"id"`
	RecordType frontier.EntityType `json:"record_type"`
	RecordID   string              `json:"record_id"`
	Kind       Kind                `json:"kind"`
	// ProposerKind and ProposerID are the run or the operator that proposed
	// the action. They travel because #88's acceptance rate is only a quality
	// signal if it can be read per proposer.
	ProposerKind frontier.ActorKind `json:"proposer_kind"`
	ProposerID   string             `json:"proposer_id"`
	// EmittedRef is the reference a proposing run emitted the action under,
	// absent for an operator's synthesized one. It travels because it is the
	// resume key a continuation of that run recognizes its own proposal by,
	// and a continuation may run on a different host than the original.
	EmittedRef string          `json:"emitted_ref,omitempty"`
	CreatedAt  string          `json:"created_at"`
	Payload    json.RawMessage `json:"payload"`
}

// validate refuses a published action a reader could not attribute.
//
// It checks identity, subject, proposer, time and payload presence, plus the
// one vocabulary this package owns. It deliberately does not re-check that
// RecordType names a frontier record type: Propose proved that by reading the
// record out of the frontier, which is a stronger claim than a string
// comparison, and a copy of frontier's vocabulary here would be a second one
// with nothing keeping it in step.
func (p publishedDisposition) validate() error {
	switch {
	case p.ID == "":
		return fmt.Errorf("%w: a published proposed action carries its id", ErrInvalidValue)
	case p.RecordType == "" || p.RecordID == "":
		return fmt.Errorf("%w: a published proposed action names the record revision it is about", ErrInvalidValue)
	case !p.Kind.valid():
		return fmt.Errorf("%w: disposition kind %q", ErrInvalidValue, p.Kind)
	case p.ProposerKind == "" || p.ProposerID == "":
		return fmt.Errorf("%w: a published proposed action names who proposed it", ErrInvalidValue)
	case len(p.Payload) == 0:
		return fmt.Errorf("%w: a published proposed action carries the payload that was stored", ErrInvalidValue)
	}
	if _, err := parseTime(p.CreatedAt); err != nil {
		return fmt.Errorf("%w: published proposed action %s: %v", ErrInvalidValue, p.ID, err)
	}
	return nil
}

// MarshalJSON encodes the action after validating it.
//
// Validating in the marshaller rather than at the call site is what makes the
// check unskippable: these bytes become an immutable, sealed object in the
// shared catalog, and a malformed one cannot be corrected there — 0003's
// analysis_records is insert-only — so the only place a refusal costs nothing
// is before the transaction that stages it.
func (p publishedDisposition) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	// The local type drops the method, which is what keeps this from
	// recursing into itself.
	type wire publishedDisposition
	return json.Marshal(wire(p))
}

// publishedLedgerEntry is the canonical publication shape of one operator
// answer to a proposed action.
//
// disposition_ledger's payload_json holds only the operator's own note, so
// staging it alone would publish a sentence with no ruling, no author, no
// position in the ledger and no action it answers — and #88 and #94 read
// provenance out of exactly those four.
//
// The entry publishes as a record of its own rather than as an amendment to the
// action it answers, which is the same shape it has locally and for the same
// reason: reconsidering appends another entry, and an answer that overwrote its
// predecessor would discard the reconsideration this ledger exists to keep.
// 0003's analysis_records is insert-only, so the shared catalog would refuse
// the amendment anyway.
type publishedLedgerEntry struct {
	ID            string `json:"id"`
	DispositionID string `json:"disposition_id"`
	// Sequence is per-action and strictly increasing, so a reconsidered
	// decision reads in order on another host even when two entries land
	// inside one timestamp.
	Sequence   int64           `json:"seq"`
	Ruling     Ruling          `json:"ruling"`
	OperatorID string          `json:"operator_id"`
	RecordedAt string          `json:"recorded_at"`
	Payload    json.RawMessage `json:"payload"`
}

// validate refuses a published answer a reader could not place in a ledger.
func (p publishedLedgerEntry) validate() error {
	switch {
	case p.ID == "":
		return fmt.Errorf("%w: a published decision carries its id", ErrInvalidValue)
	case p.DispositionID == "":
		return fmt.Errorf("%w: a published decision names the proposed action it answers", ErrInvalidValue)
	case p.Sequence < 1:
		return fmt.Errorf("%w: a published decision carries its position in the ledger", ErrInvalidValue)
	case !p.Ruling.valid():
		return fmt.Errorf("%w: ruling %q", ErrInvalidValue, p.Ruling)
	case p.OperatorID == "":
		// An anonymous acceptance would record that an action was authorized
		// without recording that anyone authorized it, which is the one thing
		// #87's "suggestions, never side effects" rests on.
		return fmt.Errorf("%w: a published decision is attributed to an operator", ErrInvalidValue)
	case len(p.Payload) == 0:
		return fmt.Errorf("%w: a published decision carries the payload that was stored", ErrInvalidValue)
	}
	if _, err := parseTime(p.RecordedAt); err != nil {
		return fmt.Errorf("%w: published decision %s: %v", ErrInvalidValue, p.ID, err)
	}
	return nil
}

// MarshalJSON encodes the answer after validating it, on the same terms as
// publishedDisposition's.
func (p publishedLedgerEntry) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	type wire publishedLedgerEntry
	return json.Marshal(wire(p))
}

// publishedInvitation is the canonical publication shape of one
// instruction-free "process this further".
//
// disposition_invitation has no payload column at all, so the whole record is
// these four fields — and that absence is the invariant rather than an
// omission. #87's nudge carries no operator words; a payload field here would
// be the place an instruction appeared in a later release, on a host where the
// table that refuses one is not.
//
// Consumption does not travel. Which run took an invitation is a claim recorded
// in a sibling table by the machine that took it, and it is local scheduling
// rather than part of the record the operator wrote; see claim.
type publishedInvitation struct {
	ID         string              `json:"id"`
	RecordType frontier.EntityType `json:"record_type"`
	RecordID   string              `json:"record_id"`
	// OperatorID is the person whose authority the invitation borrows (#96),
	// which is why an unattributed one is refused here rather than published
	// as a nudge from nobody.
	OperatorID string `json:"operator_id"`
	CreatedAt  string `json:"created_at"`
}

// validate refuses a published invitation a reader could not act on.
func (p publishedInvitation) validate() error {
	switch {
	case p.ID == "":
		return fmt.Errorf("%w: a published invitation carries its id", ErrInvalidValue)
	case p.RecordType == "" || p.RecordID == "":
		return fmt.Errorf("%w: a published invitation names the record revision it points at", ErrInvalidValue)
	case p.OperatorID == "":
		return fmt.Errorf("%w: a published invitation is attributed to an operator", ErrInvalidValue)
	}
	if _, err := parseTime(p.CreatedAt); err != nil {
		return fmt.Errorf("%w: published invitation %s: %v", ErrInvalidValue, p.ID, err)
	}
	return nil
}

// MarshalJSON encodes the invitation after validating it, on the same terms as
// publishedDisposition's.
func (p publishedInvitation) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	type wire publishedInvitation
	return json.Marshal(wire(p))
}
