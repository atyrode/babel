package frontier

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// This file is the wire form of one frontier record between hosts, and nothing
// else: it neither reads nor writes the durable store.
//
// That restraint is the design. §9's local state is split by lifecycle - the
// durable half holds analysis that exists nowhere else, the rebuildable half
// holds caches - and §14's frozen envelope decision settled local-only
// decrypted indexing on the same ground: the retrieval index is a rebuildable
// cache whose loss costs a re-index and never data. A record another machine
// committed therefore belongs in the cache and must never be written into this
// machine's durable tables. Two reasons, and either alone would be enough.
//
// It would stop being one copy. The publishing host's durable row is the
// record; a second row here would be a replica that no rule keeps in step, and
// the append-only triggers that make §4.7's "rejection never deletes" a
// database property would be enforcing immutability over a copy.
//
// And it would publish itself back. The staging journal that carries a durable
// write to the shared catalog is keyed on the record's global id, so an
// ingested row would be staged again by the machine that ingested it, and two
// hosts would take turns republishing one record forever. Keeping ingest out of
// the durable store removes that failure rather than guarding against it.
//
// So the only thing here is a translation: sealed plaintext in, an Output out,
// with Summary and Text derived by exactly the code path the local index uses.
// A remote record indexes byte-identically to the same record read locally,
// which is what makes one search over both honest.

// ErrNotSearchable reports a record kind the retrieval surface does not hold.
//
// The shared catalog carries nine Phase B record kinds, this package publishes
// six of them, and four of those six have a searchable output. So meeting a
// proposal or a link in a fleet read is normal rather than exceptional: an
// ingest skips them, it does not fail, because failing would make one
// unindexable kind stop the fleet from being searchable at all.
//
// Proposals are absent from the search surface for the reason Outputs gives: a
// proposal's text is its findings' text restated for a reviewer, and indexing
// both would make every consolidated finding match twice and read as two
// independent prior ideas. Links carry a note and two endpoints; the note is
// indexed with the records it connects rather than on its own.
var ErrNotSearchable = errors.New("frontier: record kind has no searchable output")

// PublishedKind names a frontier record kind on the wire.
//
// It is a type of its own rather than an OutputKind because publishing and
// searching are different questions with different answers. Every record this
// package stores must be able to reach the shared catalog - SPEC.md §240 names
// hypotheses, observations, findings, proposals, review events and lineage
// among the globally durable set, and migration 0003's closed vocabulary
// carries `proposal` and `link` precisely so they can commit - while only four
// of them have a flattened searchable view. Smuggling the other two into
// OutputKind would make `ValidOutputKind(k) == false` true of a legal kind,
// which is a landmine for every future reader of the search surface; giving the
// wire form its own vocabulary costs one type and keeps both honest.
type PublishedKind string

// The frontier record kinds that reach the shared catalog. The first four are
// spelled the same as their OutputKind because they are the same records; the
// last two have no searchable view and Output refuses them.
const (
	PublishedHypothesis                 = PublishedKind(OutputHypothesis)
	PublishedObservation                = PublishedKind(OutputObservation)
	PublishedFinding                    = PublishedKind(OutputFinding)
	PublishedReviewAnswer               = PublishedKind(OutputReviewAnswer)
	PublishedProposal                   = PublishedKind(EntityProposal)
	PublishedLink         PublishedKind = "link"
)

// PublishedKinds lists every kind this package publishes, which a caller
// validating a wire record needs and which nothing else should restate.
func PublishedKinds() []PublishedKind {
	return []PublishedKind{
		PublishedHypothesis, PublishedObservation, PublishedFinding,
		PublishedReviewAnswer, PublishedProposal, PublishedLink,
	}
}

// ValidPublishedKind reports whether kind is one this package publishes.
func ValidPublishedKind(kind PublishedKind) bool {
	for _, known := range PublishedKinds() {
		if kind == known {
			return true
		}
	}
	return false
}

// PublishedRecord is the canonical plaintext form of one frontier record on its
// way through the shared catalog: the record's own structural metadata, plus
// the payload bytes unchanged.
//
// Two questions decide what is on it, and the answers are not symmetric.
//
// What must be here is whatever a reader cannot recover from the plaintext row.
// migrations/0003 gives PostgreSQL the record's id, its run, its kind, its
// schema, its closure ordinal, the sealed-object reference and a timestamp -
// and deliberately no more, because every additional plaintext column is a
// fact a managed provider can read without a key. A record's chain identity and
// a candidate's lifecycle state are not among those columns, and both are
// load-bearing for the thing this envelope exists to serve: without the chain
// id, two wordings of one candidate read as two candidates and cross-host dedup
// gets the answer exactly backwards, and without the status a run cannot tell
// that what it is about to mint already exists and is at rest.
//
// What must not be here is anything derived. Summary and Text are absent on
// purpose: they are computed from Payload by describePayload, which is the same
// function the local path calls, so a remote record cannot index differently
// from the identical local record. Shipping them would create a second answer
// to one question and invite the two to disagree across versions.
//
// Payload is the record's stored payload_json bytes verbatim. Nothing
// re-encodes model-authored text on the way out or on the way in.
type PublishedRecord struct {
	// Schema is the record shape's own version (RecordSchema), independent of
	// the shared catalog's. A reader that meets a version it does not
	// understand says so rather than decoding optimistically.
	Schema int           `json:"schema"`
	Kind   PublishedKind `json:"kind"`
	ID     string        `json:"id"`
	// RootID is the chain identity. It is the record's own id for anything
	// with no chain, which is what keeps a reader from having to know which
	// kinds have one.
	RootID string `json:"root_id"`
	// Ancestor is the revision this record immediately supersedes, empty for
	// the first revision in a chain.
	//
	// RootID alone is not enough, and the gap is specific: a fleet reader
	// holding three revisions of one candidate from two machines can group
	// them by root but cannot order them, because the only other value it has
	// is a timestamp from a clock it does not control. The ancestor link is
	// the order, it is a relationship id SPEC.md §9 admits, and the local row
	// already carries it.
	Ancestor string `json:"ancestor,omitempty"`
	// Subject names the record a review answer answers about, and is the zero
	// Ref for every other kind.
	Subject Ref `json:"subject,omitzero"`
	// Answer carries what a review answer's derivation needs beyond its
	// payload, and is nil for every other kind. Both of its fields are
	// plaintext-eligible by §9's allowlist and both are sealed here anyway:
	// this envelope is one object, and carving an exception for two fields
	// would buy nothing and cost the property that a record's whole content
	// travels sealed.
	Answer *PublishedAnswer `json:"answer,omitempty"`
	// Edge carries a link's endpoints and type, and is nil for every other
	// kind.
	//
	// A link is the one record whose whole meaning is outside its payload:
	// LinkPayload holds a note and nothing else, so a reader given only the
	// note and the row would know that something was asserted about the
	// corpus and not what. §9 admits relationship ids in the clear, and they
	// are sealed here anyway for the reason Answer's are.
	Edge *PublishedEdge `json:"edge,omitempty"`
	// RestsOn names the records a proposal rests on, and is empty for every
	// other kind (#114).
	//
	// It is here for the reason Edge is: the relation is the record's
	// meaning and is not recoverable from the payload. A proposal has two
	// lawful forms - consolidated from findings (§4.5), or a candidate
	// remedy addressing hypotheses directly - and which one it is decides
	// how much authority a reader may lend it. An ingest that could not tell
	// them apart would render an unbacked want exactly like an
	// evidence-backed consolidation, which is the failure #114 exists to
	// prevent.
	//
	// The order is the order the producing store asserted, because it is the
	// order the plaintext projection's positions take and a fleet reader
	// comparing the two must not see them disagree.
	RestsOn []PublishedSubject `json:"rests_on,omitempty"`
	RunID   string             `json:"run_id,omitempty"`
	// Status is a candidate's lifecycle state at the moment the record was
	// staged for publication, empty for every other kind.
	//
	// It is a snapshot and is documented as one. A candidate's status lives in
	// an append-only history whose entries are records of their own, and
	// analysis_records is append-only by database trigger, so nothing may
	// rewrite this value once it has committed. A reader therefore renders it
	// as the state at commit time rather than as the current state, which is
	// the honest reading and the only one available without asking the owning
	// host.
	Status    Status    `json:"status,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Payload is the record's sealed content: the model's own words, its
	// evidence notes, its gradings. It is the one field that is not
	// structural, and it is the reason the whole envelope is sealed rather
	// than partly plaintext.
	Payload json.RawMessage `json:"payload"`
}

// PublishedAnswer carries what a review answer's derivation needs beyond its
// payload: the decision itself, and who made it.
//
// Neither is in the payload because neither is in the payload locally either -
// a disposition's vocabulary value and its reviewer are columns of
// frontier_disposition, and the payload holds only the note. Restating them
// here keeps the wire form a faithful copy of what the local row holds rather
// than a reshaped one.
//
// Decision is empty for a refinement request, which is what distinguishes the
// two forms a review answer takes. It could have been a boolean or a second
// kind; it is neither, because §4.7 is explicit that there is no standalone
// `refine` disposition - a refinement exists because a recorded rejection
// authorized it - and an absent decision says exactly that.
type PublishedAnswer struct {
	Decision Disposition `json:"decision,omitempty"`
	Reviewer string      `json:"reviewer,omitempty"`
}

// PublishedEdge carries a link's endpoints and relationship type.
//
// The ids are the records the link connects, in the direction the local row
// records them, and Type is the closed relationship vocabulary §4.7 names.
// Direction matters and is not recoverable from anything else: `contradicts`
// and `supersedes` are both asymmetric, and a reader that lost which end was
// which would read a refutation backwards.
type PublishedEdge struct {
	FromID string   `json:"from_id"`
	ToID   string   `json:"to_id"`
	Type   LinkType `json:"type"`
}

// PublishedSubject is one record a proposal rests on: the finding it was
// consolidated from, or the hypothesis its remedy addresses (#114).
//
// Kind is this package's own EntityType rather than a second vocabulary,
// because the id is meaningless without knowing which store minted it and
// there is exactly one naming of those stores that every surface already uses
// - the same strings the reference graph's namespaces and 0003's kind column
// carry.
//
// Only `finding` and `hypothesis` can appear. A proposal resting on an
// observation would be §4.3's evidence-free consolidation wearing a different
// name, and one resting on another proposal would be a remedy justified by a
// want.
type PublishedSubject struct {
	Kind EntityType `json:"kind"`
	ID   string     `json:"id"`
}

// valid reports whether this is a subject a proposal may rest on.
func (s PublishedSubject) valid() bool {
	if s.ID == "" {
		return false
	}
	switch s.Kind {
	case EntityFinding, EntityHypothesis:
		return true
	}
	return false
}

// validate refuses a wire record that could not be read back as the record it
// claims to be. It is the one validation both directions share: Marshal calls
// it so a malformed record never leaves the machine, and
// DecodePublishedRecord calls it so a record that arrived from another machine
// is checked with the same rules that let it out of that one.
//
// Nothing here is a trust check. The envelope's associated data binds the
// sealed bytes to the record's global id and kind, so a swapped or moved object
// fails to open at all; what this catches is a record that is authentic and
// still not usable - a shape from a build that has moved on, a kind this build
// has no surface for, a payload that is absent.
//
// Every sub-object rule is refused in both directions. A review answer with no
// decision would publish a verdict nobody reached; a hypothesis carrying one
// would grow a field no reader ever looks at, which is how a wire format
// acquires a second meaning.
func (p PublishedRecord) validate() error {
	if p.ID == "" {
		return fmt.Errorf("%w: published record carries no id", ErrInvalidValue)
	}
	if p.Schema < 1 {
		return fmt.Errorf("%w: published record %s carries no schema version",
			ErrInvalidValue, p.ID)
	}
	if p.Schema > RecordSchema {
		return fmt.Errorf("%w: published record %s is schema %d and this build reads %d",
			ErrInvalidValue, p.ID, p.Schema, RecordSchema)
	}
	if !ValidPublishedKind(p.Kind) {
		return fmt.Errorf("%w: record %s is kind %q", ErrInvalidValue, p.ID, p.Kind)
	}
	// A JSON null is not a payload, and the length check alone does not catch
	// it: an absent Payload field marshals to `null`, which is four bytes. A
	// record with no content at all would otherwise be publishable and
	// ingestable in both directions, and the failure would surface much later
	// as a record whose statement is empty rather than as the malformed record
	// it is.
	if len(p.Payload) == 0 || bytes.Equal(bytes.TrimSpace(p.Payload), []byte("null")) {
		return fmt.Errorf("%w: published record %s carries no payload", ErrInvalidValue, p.ID)
	}
	switch {
	case p.Kind == PublishedReviewAnswer && p.Answer == nil:
		return fmt.Errorf("%w: review answer %s names no decision or reviewer",
			ErrInvalidValue, p.ID)
	case p.Kind != PublishedReviewAnswer && p.Answer != nil:
		return fmt.Errorf("%w: a %s record carries no review answer",
			ErrInvalidValue, p.Kind)
	}
	if p.Kind == PublishedReviewAnswer && p.Subject.ID == "" {
		return fmt.Errorf("%w: review answer %s names no subject", ErrInvalidValue, p.ID)
	}
	switch {
	case p.Kind == PublishedLink && p.Edge == nil:
		return fmt.Errorf("%w: link %s names no endpoints", ErrInvalidValue, p.ID)
	case p.Kind != PublishedLink && p.Edge != nil:
		return fmt.Errorf("%w: a %s record carries no endpoints", ErrInvalidValue, p.Kind)
	}
	if p.Edge != nil {
		if p.Edge.FromID == "" || p.Edge.ToID == "" {
			return fmt.Errorf("%w: link %s is missing an endpoint", ErrInvalidValue, p.ID)
		}
		if !p.Edge.Type.valid() {
			return fmt.Errorf("%w: link %s type %q", ErrInvalidValue, p.ID, p.Edge.Type)
		}
	}
	switch {
	case p.Kind == PublishedProposal && len(p.RestsOn) == 0:
		// A proposal that rests on nothing is neither form: not §4.5's
		// consolidation, which is suggested by findings, and not #114's
		// remedy, which answers a claim. Publishing one would put a want
		// with no subject into the fleet's review queue.
		return fmt.Errorf("%w: proposal %s names nothing it rests on", ErrInvalidValue, p.ID)
	case p.Kind != PublishedProposal && len(p.RestsOn) > 0:
		return fmt.Errorf("%w: a %s record rests on nothing", ErrInvalidValue, p.Kind)
	}
	for _, subject := range p.RestsOn {
		if !subject.valid() {
			return fmt.Errorf("%w: proposal %s rests on %q %q",
				ErrInvalidValue, p.ID, subject.Kind, subject.ID)
		}
	}
	return nil
}

// Marshal encodes the record for sealing.
//
// It is a method rather than a bare json.Marshal at the call site so that the
// bytes a publisher seals and the bytes an ingest decodes are produced and
// consumed by one pair of functions, and so that the validation runs on the way
// out. A record refused here never becomes an object, which is the cheap place
// to catch it: an object is content-addressed and never deleted, so a malformed
// one is permanent litter.
func (p PublishedRecord) Marshal() ([]byte, error) {
	if err := p.validate(); err != nil {
		return nil, err
	}
	out, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("encode published record %s: %w", p.ID, err)
	}
	return out, nil
}

// DecodePublishedRecord reads the wire form back out of decrypted plaintext.
//
// It validates rather than trusting, because these bytes arrived from another
// machine. They are authenticated - the envelope's associated data binds them
// to the record's global id and kind, so a swapped object fails to open at all -
// but authentication proves origin, not shape: a record written by a build
// whose payload shape has moved on, or by one that has a kind this build does
// not know, is authentic and still not decodable here. Reporting that as a
// refusal with the id in it is how an operator learns to look at versions
// rather than at storage.
func DecodePublishedRecord(plaintext []byte) (PublishedRecord, error) {
	var p PublishedRecord
	if err := json.Unmarshal(plaintext, &p); err != nil {
		return PublishedRecord{}, fmt.Errorf("decode published record: %w", err)
	}
	if err := p.validate(); err != nil {
		return PublishedRecord{}, err
	}
	return p, nil
}

// Output rebuilds the flattened, searchable view of the record.
//
// The derivation is describePayload's, unchanged and uncopied. That is the
// whole value of this function: the summary a fleet listing shows and the text
// a fleet search matched are produced by the same code that produced them for
// the local record, so one index over both hosts ranks and displays them
// consistently, and a change to how a finding is summarized reaches remote
// records the moment it reaches local ones.
//
// A kind with no searchable view reports ErrNotSearchable. That is the honest
// place for the refusal: the record published correctly and is readable by id;
// what it does not have is a row in a search index.
func (p PublishedRecord) Output() (Output, error) {
	if p.Kind == PublishedProposal || p.Kind == PublishedLink {
		return Output{}, fmt.Errorf("%w: record %s is kind %q", ErrNotSearchable, p.ID, p.Kind)
	}
	out := Output{
		Kind:      OutputKind(p.Kind),
		ID:        p.ID,
		RootID:    p.RootID,
		Subject:   p.Subject,
		RunID:     p.RunID,
		Status:    p.Status,
		CreatedAt: p.CreatedAt.UTC(),
	}
	if out.RootID == "" {
		out.RootID = out.ID
	}
	if p.Kind == PublishedReviewAnswer {
		if p.Answer == nil {
			return Output{}, fmt.Errorf(
				"%w: review answer %s names no decision or reviewer", ErrInvalidValue, p.ID)
		}
		// The two forms a review answer takes carry different payloads and
		// are summarized differently, and both derivations are the ones
		// outputs.go uses for the local record. An absent decision is a
		// refinement request (see PublishedAnswer).
		if p.Answer.Decision == "" {
			var refinement RefinementPayload
			if err := unmarshalPayload(p.Payload, &refinement); err != nil {
				return Output{}, fmt.Errorf("decode refinement answer %s payload: %w", p.ID, err)
			}
			out.Summary, out.Text = describeRefinementAnswer(
				out.Subject, refinement.Guidance, refinement.Scope)
			return out, nil
		}
		var note DispositionPayload
		if err := unmarshalPayload(p.Payload, &note); err != nil {
			return Output{}, fmt.Errorf("decode review answer %s payload: %w", p.ID, err)
		}
		out.Summary, out.Text = describeDispositionAnswer(
			out.Subject, string(p.Answer.Decision), p.Answer.Reviewer, note.Note)
		return out, nil
	}
	if err := describePayload(&out, p.Payload); err != nil {
		return Output{}, err
	}
	return out, nil
}
