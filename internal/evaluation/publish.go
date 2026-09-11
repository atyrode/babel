package evaluation

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/sharedcatalog"
	babelsync "github.com/atyrode/babel/internal/sync"
)

// This file is the wire side of the store: it turns a record this store has
// just written into a staged Phase B record on its way to the shared catalog,
// and it turns those bytes back into a record on an instance that did not
// write them (SPEC.md §6.5, §9.1, issue #219 stage E2).
//
// It is separate from store.go because the failure modes are separate.
// store.go's job is that a local write is atomic, immutable and attributable
// on this machine; this file's job is that the same write becomes owed to the
// fleet in the same instant, and that a second instance reconstructs exactly
// what was said rather than an optimistic approximation of it.
//
// The whole record travels sealed. Nothing content-derived is projected into a
// plaintext catalog column - no vote, no tally, no rank, no argument, no
// criterion - which is why the staged record carries a payload and nothing
// else: §9 keeps content-derived judgements out of the managed database in the
// clear, and an evaluation record is content-derived judgement in its
// entirety.

// Option configures a Store at Open time.
//
// Every dependency here is optional and each absence is a supported
// deployment: no hook is local-only mode, no coordinator is a local claim
// authority, and no record resolver means an authority that is not local
// cannot be verified and is therefore refused rather than assumed.
type Option func(*Store)

// WithSync attaches the Phase B publication hook.
//
// Without it - the default - this store is purely local: nothing is staged,
// nothing is published, and every write path behaves the same otherwise. With
// it, every record is staged inside the transaction that writes it, so
// "durable here" and "owed to the fleet" are one event rather than two with a
// crash window between them. A machine that ran local-only and is later
// configured for shared mode settles the debt with Restage.
func WithSync(h babelsync.Hook) Option {
	return func(s *Store) { s.sync = h }
}

// WithRecordResolver attaches the published-record lookup used for the
// authority checks that shape alone cannot answer: the operator-authored
// criteria an outcome is measured against, and the reconsider item an
// operator's decision answers.
//
// It is separate from the artifact Resolver because it answers a different
// question - "is this evaluation record the authority it claims to be" rather
// than "what does this subject say" - and because a local-mode deployment
// legitimately has neither a fleet to ask nor a need to.
func WithRecordResolver(r RecordResolver) Option {
	return func(s *Store) { s.records = r }
}

// WithDispositionStore attaches the frontier writer an explicit reopen moves.
//
// It is the one place this package writes outside its own tables, and it is
// deliberate: §4.12 says the operator "can reopen or retain the decision", so
// a reopen that recorded an evaluation decision and left the review surface
// reading rejected would be a decision that did not happen. The write is
// staged inside the transaction that records the decision, so the two states
// cannot disagree.
//
// Without it, an explicit reopen is ErrUnavailable. Every other operator act
// is unaffected, because no other one moves a disposition.
func WithDispositionStore(f *frontier.Store) Option {
	return func(s *Store) { s.dispositions = f }
}

// publication is what a write path learns inside its transaction and acts on
// after it: the closure to publish, and whether there is one to publish yet.
//
// There often is not. A record produced by a run whose closure is still open
// joins it and publishes nothing, because migration 0003 fixes a run's
// record_count at declaration and never lets it move - so a closure may not be
// declared while it can still grow. The run declares and publishes itself when
// it ends.
type publication struct {
	closure babelsync.Closure
	publish bool
}

// stage stages one record inside the caller's transaction.
//
// producedBy is the run that produced the record, and empty for an operator's
// own act or a coverage sweep. That distinction decides the closure and it is
// not a detail: an operator's criteria for a proposal a run emitted last week
// is not part of that run's output closure, and staging it into one would try
// to join a closure the run already declared. internal/sync's Append resolves
// it in one place so no write site has to.
//
// A nil hook makes this a no-op, which is what local-only mode is.
func (s *Store) stage(ctx context.Context, tx *sql.Tx, producedBy string,
	rec babelsync.Record) (publication, error) {
	if s.sync == nil {
		return publication{}, nil
	}
	closure, publish, err := s.sync.Append(ctx, tx, producedBy, rec)
	if err != nil {
		return publication{}, err
	}
	return publication{closure: closure, publish: publish}, nil
}

// commit attempts to publish what stage staged, after the writer's transaction
// has committed.
//
// It is best-effort by contract. internal/sync returns nil for every transient
// failure - an unreachable catalog, a refused object write, a closure the
// catalog does not hold in full - because SPEC.md §6.5 makes publication a
// step that may be completed later and never a step a local write depends on.
// A returned error is a caller bug in this file and a test must not swallow
// it.
func (s *Store) commit(ctx context.Context, p publication) error {
	if s.sync == nil || !p.publish {
		return nil
	}
	return s.sync.CommitInline(ctx, p.closure)
}

// staged builds the record internal/sync stages.
//
// It carries a payload and nothing else. The other Phase B families project
// part of themselves into plaintext columns - a link's endpoints, a proposal's
// subjects - because those relations are the record's meaning and are not
// recoverable from a sealed payload. An evaluation record is the opposite
// case: every field of it is a judgement or the identity of a judgement, and
// §9 keeps content-derived judgements out of the clear. A keyless fleet reader
// therefore learns that this instance published an evaluation record, and
// nothing about what it says.
func staged(id string, payload []byte) babelsync.Record {
	return babelsync.Record{
		EntityID: id,
		Kind:     sharedcatalog.KindEvaluation,
		Schema:   RecordSchema,
		Payload:  payload,
	}
}

// envelope is the canonical wire form: a schema version and the record.
//
// The version is outside the record rather than inside it because it describes
// the encoding rather than the judgement, and because a reader has to be able
// to learn it before it trusts anything else in the bytes. That ordering is
// the whole point of the field: a build that meets a newer version refuses the
// record explicitly instead of decoding the half it recognizes, so a rollback
// retains every newer record and misreads none of them.
type envelope struct {
	Schema int    `json:"schema"`
	Record Record `json:"record"`
}

// encodeRecord renders one record's canonical publication bytes.
//
// It validates on the way out, so a malformed record can never become a
// content-addressed object nothing ever deletes, and it is the same function
// the durable row is written from - the local payload_json and the published
// object are the same bytes rather than two encodings that might diverge.
func encodeRecord(record Record) ([]byte, error) {
	if err := record.validate(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(envelope{Schema: RecordSchema, Record: record})
	if err != nil {
		return nil, fmt.Errorf("encode evaluation record %s: %w", record.ID, err)
	}
	return encoded, nil
}

// Decode reads one canonical evaluation record.
//
// It is the only way into this package from the wire, and it revalidates
// everything a write path validated: the schema version, the closed
// vocabularies, the authority of the author for the kind, the payload each
// kind must carry. A record that cannot pass is an error rather than a
// partially trusted value, because the alternative - ingesting what decodes
// and shrugging at the rest - is how a fleet reader ends up rendering an
// unauthorized assessment as a published one.
//
// An unsupported schema version is refused by name. The newer record is not
// discarded: it stays in the catalog and in the object store, and this build
// says it cannot read it.
func Decode(data []byte) (Record, error) {
	var wire envelope
	if err := json.Unmarshal(data, &wire); err != nil {
		return Record{}, fmt.Errorf("%w: decode evaluation record: %w", ErrInvalid, err)
	}
	if wire.Schema == 0 {
		return Record{}, fmt.Errorf("%w: evaluation record carries no schema version", ErrInvalid)
	}
	if wire.Schema > RecordSchema {
		return Record{}, fmt.Errorf("%w: evaluation record %s is schema %d and this build reads %d; "+
			"the record is retained and not read rather than read incorrectly",
			ErrUnavailable, wire.Record.ID, wire.Schema, RecordSchema)
	}
	if err := wire.Record.validate(); err != nil {
		return Record{}, err
	}
	return wire.Record, nil
}

// DecodeSchema reports the schema version of canonical bytes without decoding
// the record.
//
// A reader that holds a catalog row compares this against the row's
// record_schema before it opens anything: the two are written in the same
// transaction by the producer, so a disagreement means the object and the row
// are not the same record and neither one should be trusted.
func DecodeSchema(data []byte) (int, error) {
	var wire struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return 0, fmt.Errorf("%w: decode evaluation record schema: %w", ErrInvalid, err)
	}
	if wire.Schema == 0 {
		return 0, fmt.Errorf("%w: evaluation record carries no schema version", ErrInvalid)
	}
	return wire.Schema, nil
}

// DecodePublished decodes a record read from the shared catalog, checked
// against the authenticated row that named it.
//
// The row is the authority for identity here, not the payload. A sealed object
// says what it is; the catalog row says which record this deployment actually
// published and under which schema. Checking them against each other is what
// makes a swapped or misfiled object a refused read rather than a record
// attributed to the wrong statement - and it is why this is one function
// rather than a convention each reader implements.
//
// catalogID and catalogSchema come from the row. An empty id or a zero schema
// is a caller that did not have the row, which is refused: this function's
// whole value is the comparison.
func DecodePublished(data []byte, catalogID string, catalogSchema int) (Record, error) {
	if catalogID == "" || catalogSchema == 0 {
		return Record{}, fmt.Errorf("%w: a published evaluation record is decoded against its catalog row's "+
			"id and schema", ErrInvalid)
	}
	schema, err := DecodeSchema(data)
	if err != nil {
		return Record{}, err
	}
	if schema != catalogSchema {
		return Record{}, fmt.Errorf("%w: evaluation record %s is schema %d in the object and %d in the "+
			"catalog row", ErrInvalid, catalogID, schema, catalogSchema)
	}
	record, err := Decode(data)
	if err != nil {
		return Record{}, err
	}
	if record.ID != catalogID {
		return Record{}, fmt.Errorf("%w: catalog row %s names an object holding record %s",
			ErrInvalid, catalogID, record.ID)
	}
	return record, nil
}
