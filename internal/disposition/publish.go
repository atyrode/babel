package disposition

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

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

// PublishedDisposition is the canonical publication shape of one proposed next
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
type PublishedDisposition struct {
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
func (p PublishedDisposition) validate() error {
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
func (p PublishedDisposition) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	// The local type drops the method, which is what keeps this from
	// recursing into itself.
	type wire PublishedDisposition
	return json.Marshal(wire(p))
}

// PublishedLedgerEntry is the canonical publication shape of one operator
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
type PublishedLedgerEntry struct {
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
func (p PublishedLedgerEntry) validate() error {
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
// PublishedDisposition's.
func (p PublishedLedgerEntry) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	type wire PublishedLedgerEntry
	return json.Marshal(wire(p))
}

// Published is one record this package committed under the shared catalog's
// disposition kind, in whichever of its two forms the bytes carried.
//
// Exactly one field is set. The two are not folded into one struct for the
// reason internal/fleet keeps a citation edge beside a frontier projection: a
// proposed action and an operator's answer to one are different records that
// share a kind slot, and folding them would mean one of the two always arrived
// with half its fields empty, which is how a reader learns to ignore fields.
type Published struct {
	// Action is the proposed next action, nil when these bytes were an
	// answer to one.
	Action *PublishedDisposition
	// Answer is the operator's ruling appended to a proposed action's
	// ledger, nil when these bytes were the action itself.
	Answer *PublishedLedgerEntry
}

// ID is the record's global identity, which is the value the authenticated
// catalog row binds these bytes to.
func (p Published) ID() string {
	switch {
	case p.Action != nil:
		return p.Action.ID
	case p.Answer != nil:
		return p.Answer.ID
	}
	return ""
}

// Summary renders one published record as the single line a listing shows.
//
// The derivation lives here for internal/reference's reason: the CLI's fleet
// listing and the web fleet view both render this line, and a second phrasing
// would make one proposed action read differently depending on which surface
// an operator opened. The shape is internal/frontier's review-answer line —
// verb, then the record it is about, then the prose — so a disposition and the
// frontier's own review answers read as one vocabulary in a merged listing.
//
// A payload this build cannot read loses the prose half and keeps the
// structural one. That is the honest partial answer: the row still says what
// was proposed about which record, by whom, which is what a listing is for.
func (p Published) Summary() string {
	switch {
	case p.Action != nil:
		var payload Payload
		_ = json.Unmarshal(p.Action.Payload, &payload)
		return summarize(fmt.Sprintf("%s on %s %s: %s", p.Action.Kind,
			p.Action.RecordType, p.Action.RecordID, payload.Summary))
	case p.Answer != nil:
		var payload LedgerPayload
		_ = json.Unmarshal(p.Answer.Payload, &payload)
		return summarize(fmt.Sprintf("%s disposition %s: %s", p.Answer.Ruling,
			p.Answer.DispositionID, payload.Note))
	}
	return ""
}

// IsPublished reports whether decrypted plaintext committed under the shared
// catalog's `disposition` kind is one of this package's records rather than one
// of internal/frontier's review answers.
//
// A reader needs this because the two share that kind slot by design (see
// dispositionKind) and the authenticated catalog row says `disposition` and
// nothing more, so the bytes themselves have to say which decoder owns them.
//
// The two vocabularies are disjoint, which makes the answer a lookup rather
// than a guess and the same answer on every host. A proposed action spells
// `kind` as one of the five next actions this package owns, where a frontier
// review answer always spells it `review-answer`. An operator's ruling carries
// no `kind` at all and is recognized by `ruling`, which is a field no frontier
// record has: frontier keeps its decision inside `answer`, one level down.
//
// Bytes that declare neither are not this package's. They are reported by the
// frontier decoder, which is the one that can say what it expected and did not
// find; answering "not mine" here and nothing else would lose the record with
// no reason attached.
func IsPublished(plaintext []byte) bool {
	var declared struct {
		Kind   Kind   `json:"kind"`
		Ruling Ruling `json:"ruling"`
	}
	if err := json.Unmarshal(plaintext, &declared); err != nil {
		return false
	}
	return declared.Kind.valid() || declared.Ruling.valid()
}

// DecodePublished reads one of this package's published records back out of
// decrypted plaintext, under the global id the catalog authenticated the bytes
// against.
//
// It validates rather than trusting, because these bytes arrived from another
// machine. They are authenticated - the envelope binds them to the record's
// global id and catalog kind, so a swapped object does not open at all - but
// authentication proves origin and not shape, and what is left is whether this
// build can act on the record it claims to be.
//
// The catalog's id is a parameter rather than a check the caller repeats
// afterwards, because which field carries the id depends on which of the two
// forms decoded, and a caller performing the comparison would have to know
// that. Here it cannot be skipped and cannot be done against the wrong field.
func DecodePublished(plaintext []byte, recordID string) (Published, error) {
	if !IsPublished(plaintext) {
		return Published{}, fmt.Errorf(
			"%w: record %s declares neither a disposition kind nor a ruling",
			ErrInvalidValue, recordID)
	}
	var declared struct {
		Kind Kind `json:"kind"`
	}
	if err := json.Unmarshal(plaintext, &declared); err != nil {
		return Published{}, fmt.Errorf("decode published disposition: %w", err)
	}
	var published Published
	if declared.Kind.valid() {
		var action PublishedDisposition
		if err := json.Unmarshal(plaintext, &action); err != nil {
			return Published{}, fmt.Errorf("decode published proposed action: %w", err)
		}
		if err := action.validate(); err != nil {
			return Published{}, err
		}
		published.Action = &action
	} else {
		var answer PublishedLedgerEntry
		if err := json.Unmarshal(plaintext, &answer); err != nil {
			return Published{}, fmt.Errorf("decode published decision: %w", err)
		}
		if err := answer.validate(); err != nil {
			return Published{}, err
		}
		published.Answer = &answer
	}
	if published.ID() != recordID {
		return Published{}, fmt.Errorf(
			"%w: record %s carries disposition identity %q",
			ErrInvalidValue, recordID, published.ID())
	}
	return published, nil
}

// maxSummaryBytes bounds a rendered disposition line, and is internal/
// frontier's bound restated rather than shared for internal/reference's
// reason: frontier's is unexported, and a listing whose disposition rows wrap
// while every other kind stays on one line would be a listing whose rows
// disagree about how tall a row is.
const maxSummaryBytes = 240

// summarize collapses a rendered line to one bounded line. The cut lands on a
// rune boundary because a summary and a note are model- or operator-authored
// prose and half a rune is not a character.
func summarize(text string) string {
	line := strings.Join(strings.Fields(text), " ")
	if len(line) <= maxSummaryBytes {
		return line
	}
	cut := maxSummaryBytes
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return strings.TrimSpace(line[:cut]) + "…"
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
// PublishedDisposition's.
func (p publishedInvitation) MarshalJSON() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	type wire publishedInvitation
	return json.Marshal(wire(p))
}

// Restage recovers proposed actions, their complete decision histories, and
// invitations missing from the publication journal. Consumption is local
// scheduling state, not a published record.
func (s *Store) Restage(ctx context.Context) (int, error) {
	if s.sync == nil {
		return 0, fmt.Errorf("disposition: restage requires a sync hook")
	}
	count := 0
	for _, table := range []string{"disposition_proposal", "disposition_ledger", "disposition_invitation"} {
		ids, err := sync.Missing(ctx, s.db, table, "id")
		if err != nil {
			return count, err
		}
		for _, id := range ids {
			staged := false
			err := s.transact(ctx, func(tx *sql.Tx) error {
				var present bool
				if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
					SELECT 1 FROM sync_record WHERE record_id = ?)`, id).Scan(&present); err != nil {
					return err
				}
				if present {
					return nil
				}
				if err := s.restageRecord(ctx, tx, table, id); err != nil {
					return err
				}
				staged = true
				return nil
			})
			if err != nil {
				return count, fmt.Errorf("disposition: restage %s: %w", id, err)
			}
			if staged {
				count++
			}
		}
	}
	return count, nil
}

func (s *Store) restageRecord(ctx context.Context, tx *sql.Tx, table, id string) error {
	var (
		body       any
		producedBy string
		schema     = RecordSchema
		kind       = dispositionKind
	)
	switch table {
	case "disposition_proposal":
		var p PublishedDisposition
		var payload []byte
		if err := tx.QueryRowContext(ctx, `SELECT id, record_type, record_id, kind,
			proposer_kind, proposer_id, emitted_ref, created_at, payload_json, schema_version
			FROM disposition_proposal WHERE id = ?`, id).Scan(&p.ID, &p.RecordType,
			&p.RecordID, &p.Kind, &p.ProposerKind, &p.ProposerID, &p.EmittedRef,
			&p.CreatedAt, &payload, &schema); err != nil {
			return err
		}
		p.Payload = payload
		if p.ProposerKind == frontier.ActorRun {
			producedBy = p.ProposerID
		}
		body = p
	case "disposition_ledger":
		var p PublishedLedgerEntry
		var payload []byte
		if err := tx.QueryRowContext(ctx, `SELECT id, disposition_id, seq, ruling,
			operator_id, recorded_at, payload_json, schema_version
			FROM disposition_ledger WHERE id = ?`, id).Scan(&p.ID, &p.DispositionID,
			&p.Sequence, &p.Ruling, &p.OperatorID, &p.RecordedAt, &payload, &schema); err != nil {
			return err
		}
		p.Payload = payload
		body = p
	case "disposition_invitation":
		var p publishedInvitation
		if err := tx.QueryRowContext(ctx, `SELECT id, record_type, record_id, operator_id,
			created_at FROM disposition_invitation WHERE id = ?`, id).Scan(&p.ID,
			&p.RecordType, &p.RecordID, &p.OperatorID, &p.CreatedAt); err != nil {
			return err
		}
		body = p
		kind = invitationKind
	default:
		return fmt.Errorf("unknown publication table %q", table)
	}
	wire, err := marshalPayload(body)
	if err != nil {
		return err
	}
	_, _, err = s.stage(ctx, tx, producedBy, sync.Record{
		EntityID: id, Kind: kind, Schema: schema, Payload: wire,
	})
	return err
}
