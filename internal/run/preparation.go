// Package run records what a Phase B exploration ran over and what happened
// when it ran: an immutable preparation record that fixes the corpus scope
// (SPEC.md §6.5 and §8's `explore --preparation ID`), and an immutable run
// receipt carrying the provenance §7 enumerates.
//
// The two are separate records on purpose. A preparation is a statement about
// the corpus — which sessions, at which capture, described by which adapter —
// and its identity is derived from that content, so naming one on a later
// exploration makes the scope explicit and reproducible instead of implied by
// whatever the machine happened to hold at the time. A receipt is a statement
// about one run over one preparation, and there may be several of them for the
// same scope.
//
// This package composes rather than duplicates. internal/worker already
// records the worker boundary — profile reference, capability grant, every
// tool request with its decision, failures, resources, timing — and a run
// receipt embeds that record whole. What this package adds is everything
// outside that boundary: the preparation, the cookbook assets, the frontier
// position, the capability/job/policy versions, the retrieval trace, the
// candidates a finite run left undeveloped, and Babel's own failures, resource
// accounting and timing.
//
// Storage is the durable local SQLite database, shared with the hypothesis
// frontier and separate from the rebuildable retrieval index: losing a
// retrieval index costs a re-index, while losing a receipt loses the only
// record of what a run was allowed to see.
package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
)

// PreparationSchema is the version of the preparation record's stored shape.
// It participates in the ID derivation, so a record written by a future schema
// can never collide with one written by this schema even if every other field
// matches.
const PreparationSchema = 1

// preparationDomain separates this hash from every other use of SHA-256 in
// Babel. Without a domain string, a digest computed over some other structure
// that happened to serialize identically would be a valid preparation ID.
const preparationDomain = "babel/preparation/v1"

// preparationIDPrefix marks a preparation ID as one, so a mistyped identifier
// on `explore --preparation` fails as a wrong kind of ID rather than as a
// missing row.
const preparationIDPrefix = "prep-"

// PreparationID is the content-derived identity of a preparation record, in
// the form "prep-<64 lowercase hex>".
//
// It is derived, never assigned: two preparations with identical content are
// the same ID, and any difference in content — including the time the
// preparation was made — is a different ID. That makes a preparation
// referenced from a receipt or a CLI argument self-describing: the ID cannot
// name a scope other than the one it was computed from without the mismatch
// being detectable.
type PreparationID string

// Valid reports whether id is well-formed. It is a shape check, not a claim
// that the record exists or that its content still hashes to this value; use
// Preparation.Verify for that.
func (id PreparationID) Valid() bool {
	s := string(id)
	if !strings.HasPrefix(s, preparationIDPrefix) || len(s) != len(preparationIDPrefix)+sha256.Size*2 {
		return false
	}
	return isHex(s[len(preparationIDPrefix):])
}

// AdapterRef is the source adapter behind one selection entry, at the fidelity
// SPEC.md §3 requires a catalog row to record. The adapter's identity is the
// entry's harness — one adapter owns one harness — so what is recorded here is
// its versioning and what it could not derive.
//
// Completeness is the adapter's explicit account of absent metadata. An empty
// list means the adapter derived everything it knows how to derive; it never
// means the adapter synthesized a value to satisfy the shape.
type AdapterRef struct {
	// Schema is the adapter metadata schema version, versioned independently
	// of the common catalog shape (§3).
	Schema int `json:"schema"`
	// Version is the adapter implementation's own version, so a preparation
	// made by a build with a known parsing bug is identifiable afterwards.
	Version string `json:"version"`
	// Completeness lists the metadata fields the adapter could not derive,
	// sorted and deduplicated by NewPreparation so that reordering the
	// adapter's report does not change the preparation ID.
	Completeness []adapter.CompletenessReason `json:"completeness,omitempty"`
}

// Selected is one session inside a preparation, identified the way SPEC.md
// decision 9 identifies sessions: host, harness, and adapter-defined source
// identity, with an optional restic snapshot naming the immutable historical
// capture. A bare selection with no snapshot is the live local source, and the
// digests are what make that reproducible anyway.
//
// Both digests are recorded because §7 requires both and they answer different
// questions. CaptureDigest identifies the bytes as captured, which is what a
// restore is checked against; SourceDigest identifies the normalized event
// stream derived from them, which is what analysis actually read. A
// normalization change moves the second without moving the first, and that
// difference is exactly what a later reviewer needs to see.
type Selected struct {
	Host          string        `json:"host"`
	Harness       string        `json:"harness"`
	SourceID      string        `json:"source_id"`
	Snapshot      string        `json:"snapshot,omitempty"`
	CaptureDigest digest.Digest `json:"capture_digest"`
	SourceDigest  digest.Digest `json:"source_digest"`
	Adapter       AdapterRef    `json:"adapter"`
}

// key is the identity a preparation may not hold twice. It is also the sort
// key, so canonical order is a total order on distinct entries.
func (s Selected) key() [4]string {
	return [4]string{s.Host, s.Harness, s.SourceID, s.Snapshot}
}

// Preparation is an immutable, content-addressed statement of one exploration
// scope.
//
// Go cannot make a struct field immutable, so immutability here is a checked
// property rather than an enforced one: the ID is derived from the content,
// Verify recomputes it, and the store verifies on write and on read. A mutated
// Preparation therefore stops matching its own ID instead of silently becoming
// a different scope under the same name.
type Preparation struct {
	Schema int           `json:"schema"`
	ID     PreparationID `json:"id"`
	// PreparedAt is when the selection was fixed, in UTC. It is part of the
	// derivation: a preparation is a record of an act, and two acts over the
	// same material are two records. The digests remain available for asking
	// the narrower question of whether the material itself was identical.
	PreparedAt time.Time `json:"prepared_at"`
	// Selection is the corpus, in canonical order. NewPreparation sorts it, so
	// the order a caller discovered sessions in cannot leak into the identity.
	Selection []Selected `json:"selection"`
	// Related are the prior Babel outputs that looked related to this scope
	// when it was fixed (#87 item 4), in canonical order.
	//
	// They are part of the identity for the same reason PreparedAt is. A
	// preparation is the record of an act, and the act now includes one
	// mechanical question asked of the frontier — "what have we already said
	// about material like this" — so two preparations over identical
	// sessions that found different prior work are two different statements
	// of scope. Recording the answer is what makes the injection auditable
	// afterwards instead of being a thing that happened inside a run.
	Related []RelatedOutput `json:"related,omitempty"`
	// Serendipitous marks a scope drawn for exploration rather than assembled
	// to answer something (#87 item 4).
	//
	// It exists because the same injected records mean two different things.
	// A directed run receives prior outputs as the work already done on its
	// question; a serendipity draw receives them as inspiration and is
	// explicitly not confined to them, and a run that treated them as a
	// scope would have had its serendipity taken away by the very mechanism
	// meant to stop it duplicating work. The marker travels with the scope
	// because that is where the decision was made.
	Serendipitous bool `json:"serendipitous,omitempty"`
}

// RelatedOutput is one prior Babel output a preparation names.
//
// It is identifiers and nothing else. The summary a job document shows is
// derived from the record when the job is built, so this record cannot hold a
// stale copy of somebody's wording, and §9's plaintext allowlist admits a kind
// and an id where it does not admit the prose behind them.
type RelatedOutput struct {
	// Kind is a frontier.OutputKind. It is a string here rather than that
	// type because a preparation is a §7 provenance record and must decode
	// on a build whose frontier vocabulary has moved on; the consumer
	// validates it against the kinds it can actually resolve.
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// key is the identity a preparation may not name twice, and the sort key.
func (r RelatedOutput) key() [2]string { return [2]string{r.Kind, r.ID} }

// PreparationContext is what #87 adds to a fixed scope: the prior outputs the
// frontier held that looked related to it, and whether the scope was drawn for
// serendipity.
//
// It is one struct rather than two parameters because both answer the same
// question — what does a run receive besides the sessions — and because the
// zero value is the honest name for "nothing was asked of the frontier",
// which is what every preparation made before #87 recorded.
type PreparationContext struct {
	Related       []RelatedOutput
	Serendipitous bool
}

// MaxRelatedOutputs bounds how many prior outputs one preparation names.
//
// The bound is on the record because it is a bound on what a run receives
// unasked. A job document listing forty prior ideas would spend a low-thinking
// budget reading Babel's own back catalogue instead of the corpus, and would
// make the injection read as a scope rather than as context. Twelve is about
// what fits in a page a model reads before starting work, and the on-demand
// frontier search is what the run uses when twelve is not enough — which is
// the right split: bounded context by default, more of it only when the
// investigator asked and the retrieval budget allowed.
const MaxRelatedOutputs = 12

// NewPreparation fixes a corpus scope and derives its identity.
//
// The selection is copied and canonically ordered, so neither the caller's
// discovery order nor a later mutation of the caller's slice can change what
// the returned record means. An empty selection is refused: an exploration
// over nothing is a mistake, and accepting it would make a broken selection
// indistinguishable from a deliberate one.
//
// The context's related outputs are ordered and deduplicated on the same
// reasoning, and the order is canonical rather than the order retrieval
// returned them in. That is deliberate: the retrieval that found them ranked
// them, §5.4 forbids retrieval rank from becoming evidence strength, and a
// stored record whose order was a ranking would be handing a run exactly that
// signal under the name of a scope.
func NewPreparation(preparedAt time.Time, selection []Selected, context PreparationContext) (Preparation, error) {
	if preparedAt.IsZero() {
		return Preparation{}, fmt.Errorf("preparation: prepared_at is required")
	}
	if len(selection) == 0 {
		return Preparation{}, fmt.Errorf("preparation: selection is empty")
	}
	canonical := make([]Selected, len(selection))
	copy(canonical, selection)
	for i := range canonical {
		if err := canonical[i].canonicalize(); err != nil {
			return Preparation{}, fmt.Errorf("preparation: selection entry %d: %w", i, err)
		}
	}
	sort.Slice(canonical, func(i, j int) bool {
		a, b := canonical[i].key(), canonical[j].key()
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
	for i := 1; i < len(canonical); i++ {
		if canonical[i].key() == canonical[i-1].key() {
			return Preparation{}, fmt.Errorf("preparation: selection holds the same session twice")
		}
	}
	related, err := canonicalRelated(context.Related)
	if err != nil {
		return Preparation{}, err
	}
	p := Preparation{
		Schema:        PreparationSchema,
		PreparedAt:    preparedAt.UTC(),
		Selection:     canonical,
		Related:       related,
		Serendipitous: context.Serendipitous,
	}
	p.ID = p.derive()
	return p, nil
}

// canonicalRelated orders and deduplicates the related outputs, refusing one
// that names nothing. A reference with no kind or no id could not be resolved
// by the run that received it, so accepting it would put an unresolvable
// pointer inside an immutable record.
func canonicalRelated(related []RelatedOutput) ([]RelatedOutput, error) {
	if len(related) == 0 {
		return nil, nil
	}
	canonical := make([]RelatedOutput, len(related))
	copy(canonical, related)
	for i, r := range canonical {
		if r.Kind == "" || r.ID == "" {
			return nil, fmt.Errorf("preparation: related output %d names no record", i)
		}
	}
	sort.Slice(canonical, func(i, j int) bool {
		a, b := canonical[i].key(), canonical[j].key()
		for k := range a {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
	out := canonical[:0]
	for i, r := range canonical {
		if i > 0 && r.key() == canonical[i-1].key() {
			continue
		}
		out = append(out, r)
	}
	// The bound is checked after deduplication, so a caller that offered the
	// same record twice is not refused for a length it does not have.
	if len(out) > MaxRelatedOutputs {
		return nil, fmt.Errorf("preparation: %d related outputs is past the ceiling of %d",
			len(out), MaxRelatedOutputs)
	}
	return out, nil
}

// canonicalize validates one entry and puts its completeness report in a fixed
// order. Validation quotes field names and never field values: a preparation
// is built from Babel's own discovery output, but an error that echoed a
// selector would be one more surface for that output to escape through.
func (s *Selected) canonicalize() error {
	switch s.Harness {
	case event.HarnessOMP, event.HarnessCodex, event.HarnessClaude:
	default:
		return fmt.Errorf("unknown harness")
	}
	if s.Host == "" {
		return fmt.Errorf("host is required")
	}
	if !adapter.ValidSourceID(s.SourceID) {
		return fmt.Errorf("invalid source_id")
	}
	if s.Snapshot != "" && !isHex(s.Snapshot) {
		return fmt.Errorf("snapshot is not a hex restic snapshot id")
	}
	if !s.CaptureDigest.Valid() {
		return fmt.Errorf("invalid capture_digest")
	}
	if !s.SourceDigest.Valid() {
		return fmt.Errorf("invalid source_digest")
	}
	if s.Adapter.Schema < 1 {
		return fmt.Errorf("adapter schema must be positive")
	}
	if s.Adapter.Version == "" {
		return fmt.Errorf("adapter version is required")
	}
	reasons := make([]adapter.CompletenessReason, 0, len(s.Adapter.Completeness))
	for _, r := range s.Adapter.Completeness {
		if r.Field == "" || r.Reason == "" {
			return fmt.Errorf("completeness reason needs both a field and a reason")
		}
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool {
		if reasons[i].Field != reasons[j].Field {
			return reasons[i].Field < reasons[j].Field
		}
		return reasons[i].Reason < reasons[j].Reason
	})
	reasons = slicesCompactFunc(reasons)
	if len(reasons) == 0 {
		reasons = nil
	}
	s.Adapter.Completeness = reasons
	return nil
}

// slicesCompactFunc removes adjacent duplicate reasons. Two identical reasons
// for the same field are one fact reported twice, and letting the repetition
// into the derivation would make the ID depend on how often an adapter
// happened to say it.
func slicesCompactFunc(reasons []adapter.CompletenessReason) []adapter.CompletenessReason {
	out := reasons[:0]
	for i, r := range reasons {
		if i > 0 && r == reasons[i-1] {
			continue
		}
		out = append(out, r)
	}
	return out
}

// derive computes the preparation ID.
//
// The hashed encoding is injective, which is what makes the ID a function of
// the content rather than of its punctuation:
//
//	"babel/preparation/v1" || u32(Schema) || lp(PreparedAt) || u32(len(Selection))
//	  then per entry, in canonical order:
//	    lp(Host) || lp(Harness) || lp(SourceID) || lp(Snapshot)
//	    || lp(CaptureDigest) || lp(SourceDigest)
//	    || u32(Adapter.Schema) || lp(Adapter.Version)
//	    || u32(len(Completeness)) || per reason: lp(Field) || lp(Reason)
//
// where lp(s) is a four-byte big-endian length followed by s's bytes and u32
// is four big-endian bytes. Every variable-length field is length-prefixed and
// every repetition is count-prefixed, so no two distinct records encode to the
// same bytes — plain concatenation would let a host of "a" with a harness of
// "bc" collide with a host of "ab" and a harness of "c".
//
// The time is encoded as RFC3339 with nanoseconds in UTC, the same rendering
// the stored JSON uses, so the derivation and the record agree byte for byte
// about what instant means.
//
// #87's context is appended, and only when there is any:
//
//	if len(Related) > 0 || Serendipitous:
//	    u32(len(Related)) || per entry, in canonical order: lp(Kind) || lp(ID)
//	    || u32(Serendipitous ? 1 : 0)
//
// The condition is what keeps every preparation recorded before #87 verifying
// against its own stored id, which matters because those records are durable,
// pending remote sync, and named on the command line by an operator resuming a
// scope. Appending four zero bytes unconditionally would have made every one of
// them fail Verify and taken "explore that scope again" away permanently. The
// encoding stays injective: nothing follows, so "no bytes" and "a count of at
// least zero followed by a flag" are distinguishable, and a preparation with an
// empty context is exactly the record a pre-#87 Babel would have written.
func (p Preparation) derive() PreparationID {
	h := sha256.New()
	h.Write([]byte(preparationDomain))
	writeU32(h, uint32(p.Schema))
	writeLP(h, p.PreparedAt.UTC().Format(time.RFC3339Nano))
	writeU32(h, uint32(len(p.Selection)))
	for _, s := range p.Selection {
		writeLP(h, s.Host)
		writeLP(h, s.Harness)
		writeLP(h, s.SourceID)
		writeLP(h, s.Snapshot)
		writeLP(h, string(s.CaptureDigest))
		writeLP(h, string(s.SourceDigest))
		writeU32(h, uint32(s.Adapter.Schema))
		writeLP(h, s.Adapter.Version)
		writeU32(h, uint32(len(s.Adapter.Completeness)))
		for _, r := range s.Adapter.Completeness {
			writeLP(h, r.Field)
			writeLP(h, r.Reason)
		}
	}
	if len(p.Related) > 0 || p.Serendipitous {
		writeU32(h, uint32(len(p.Related)))
		for _, r := range p.Related {
			writeLP(h, r.Kind)
			writeLP(h, r.ID)
		}
		var serendipity uint32
		if p.Serendipitous {
			serendipity = 1
		}
		writeU32(h, serendipity)
	}
	var sum [sha256.Size]byte
	h.Sum(sum[:0])
	return PreparationID(preparationIDPrefix + hex.EncodeToString(sum[:]))
}

func writeU32(h hash.Hash, v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	h.Write(b[:])
}

func writeLP(h hash.Hash, s string) {
	writeU32(h, uint32(len(s)))
	h.Write([]byte(s))
}

// Verify recomputes the ID from the content and reports a mismatch.
//
// It is what makes "immutable" checkable in a language that cannot enforce it:
// the store calls it before writing and after reading, so a record altered in
// memory or in the database fails rather than passing itself off as the scope
// it names.
func (p Preparation) Verify() error {
	if p.Schema != PreparationSchema {
		return fmt.Errorf("preparation: unsupported schema %d", p.Schema)
	}
	if len(p.Selection) == 0 {
		return fmt.Errorf("preparation: selection is empty")
	}
	if want := p.derive(); want != p.ID {
		return fmt.Errorf("preparation: content does not match its id")
	}
	return nil
}

// MarshalCanonical renders the record's stored form: deterministic JSON, with
// the selection in canonical order and times in UTC. Two equal preparations
// therefore produce identical bytes, which is what lets storage round-trip
// them byte for byte instead of merely field for field.
//
// It verifies first, so bytes whose ID does not match their content are never
// written anywhere.
func (p Preparation) MarshalCanonical() ([]byte, error) {
	if err := p.Verify(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("preparation: encode: %w", err)
	}
	return b, nil
}

// UnmarshalPreparation parses a stored preparation and verifies that its
// content still derives its ID. A record that fails is reported as corrupt
// rather than returned: a scope that does not match its name is worse than a
// missing one, because a run receipt referencing it would look complete.
func UnmarshalPreparation(b []byte) (Preparation, error) {
	var p Preparation
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Preparation{}, fmt.Errorf("preparation: decode: %w", err)
	}
	p.PreparedAt = p.PreparedAt.UTC()
	if err := p.Verify(); err != nil {
		return Preparation{}, err
	}
	return p, nil
}

// isHex reports whether s is a non-empty lowercase hex string.
func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
