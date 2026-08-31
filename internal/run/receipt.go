package run

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/worker"
)

// ReceiptSchema is the version of the run receipt's stored shape. Every
// receipt records it, so a build that meets a record it cannot fully
// understand says so instead of reading it optimistically.
const ReceiptSchema = 1

// Sync states of a durable record. A Phase B output is durable locally the
// moment it is written, and globally committed only once its PostgreSQL rows
// and encrypted objects exist remotely (SPEC.md §6.5, §9); until then it is
// visibly pending rather than quietly assumed to be safe.
const (
	SyncPending   = "pending-sync"
	SyncCommitted = "committed"
)

// Cookbook asset kinds, matching SPEC.md §5's three: shared investigation
// policies, domain lenses, and meta recipes. §7 requires a receipt to record
// policy and lens identities and versions, and the kind is what distinguishes
// them once they are in the same list.
const (
	AssetPolicy = "policy"
	AssetLens   = "lens"
	AssetMeta   = "meta"
)

// maxIdentifier bounds every identifier this package accepts. Identifiers are
// names, not payloads; an unbounded one is either a bug or an attempt to use a
// column as storage.
const maxIdentifier = 256

// ReceiptID is a run receipt's globally unique, client-generated identity
// (SPEC.md §9 requires immutable events to carry one). Unlike a preparation
// ID it is assigned rather than derived: two runs over the same preparation
// are two runs, and a receipt that collapsed into its content would erase one
// of them.
type ReceiptID string

// NewReceiptID mints a receipt identity from the system CSPRNG. 128 bits of
// randomness makes collision across every instance of a deployment a
// non-consideration, which is what "globally unique client-generated" has to
// mean when there is no coordinator to ask.
func NewReceiptID() ReceiptID { return ReceiptID("rcpt-" + rand.Text()) }

// AuthorityKind names what caused a run to happen. SPEC.md §7 makes a receipt
// the record of what a run did; issue #96 makes it the record of why the run
// was allowed to start, which is a different question and the one an operator
// watching an autonomous loop actually asks.
//
// The vocabulary is closed at three because there are exactly three ways work
// can be authorized: a person asked for it, a named standing policy directed
// it, or the loop's protected serendipity floor drew it. A fourth kind would
// be a run nobody could be held to.
type AuthorityKind string

// The three authorities a run may carry.
const (
	// AuthorityOperator is a person's own request: a command they typed or an
	// invitation they left. The operator always outranks the loop.
	AuthorityOperator AuthorityKind = "operator"
	// AuthorityPolicy is a named standing policy or duty directing the run.
	AuthorityPolicy AuthorityKind = "policy"
	// AuthoritySerendipity is a declared draw from the loop's chaotic floor,
	// which has no aim by design and says so rather than borrowing a reason.
	AuthoritySerendipity AuthorityKind = "serendipity"
)

// Authority is a run's why: which of the three authorities started it, and the
// reference that makes the claim checkable — the command, the invitation id,
// the policy name, or the identity of the draw.
//
// It lives in the header rather than the body because it is an identifier pair
// and nothing else, which is inside SPEC.md §9's plaintext allowlist: a
// deployment that seals receipt bodies must still be able to list runs by why
// they happened, and an authority readable only by opening the payload would
// make the legibility invariant depend on holding a key.
//
// A zero Authority is not an error everywhere: receipts recorded before this
// field existed carry none, and Recorded reports that honestly rather than
// letting a renderer attribute them to whoever is asking. What is refused is
// recording a *new* receipt without one — no nameable authority, no run.
type Authority struct {
	Kind AuthorityKind `json:"kind"`
	Ref  string        `json:"ref"`
}

// Recorded reports whether this receipt says why its run happened. It is false
// only for a receipt written before the field existed; a receipt this build
// wrote always carries one.
func (a Authority) Recorded() bool { return a.Kind != "" }

// String renders an authority for a diagnostic as kind:ref.
func (a Authority) String() string {
	if !a.Recorded() {
		return "unrecorded"
	}
	return string(a.Kind) + ":" + a.Ref
}

// validate refuses an authority that cannot be held to what it claims. The
// reference is mandatory for every kind, including serendipity: a draw that
// names no draw is indistinguishable from a loop that ran for no reason it
// could state afterwards.
func (a Authority) validate() error {
	switch a.Kind {
	case AuthorityOperator, AuthorityPolicy, AuthoritySerendipity:
	case "":
		return fmt.Errorf("receipt: a run records no authority; no nameable authority, no run")
	default:
		return fmt.Errorf("receipt: authority kind is not one this build implements")
	}
	if !validIdentifier(a.Ref) {
		return fmt.Errorf("receipt: authority %s names no reference", a.Kind)
	}
	return nil
}

// Evidence binds a claim about the corpus to the locator that recovers the
// bytes behind it. SPEC.md §4.3 is explicit that an observation cannot exist
// without evidence, and the same rule is what keeps a receipt inspectable
// rather than merely assertive.
//
// The locator is unexported precisely so the pairing cannot be broken: there
// is no way to build an Evidence without one, no way to clear it afterwards,
// and decoding a stored record re-validates it. A zero Evidence is invalid and
// says so.
type Evidence struct {
	locator event.Locator
	note    string
}

// NewEvidence pairs a note with the locator that backs it. An invalid locator
// is refused rather than stored as a broken pointer: a locator that cannot
// recover its bytes is not weaker evidence, it is none.
func NewEvidence(loc event.Locator, note string) (Evidence, error) {
	if err := validLocator(loc); err != nil {
		return Evidence{}, err
	}
	return Evidence{locator: loc, note: note}, nil
}

// Locator returns the locator that recovers this evidence's bytes.
func (e Evidence) Locator() event.Locator { return e.locator }

// Note returns the human-readable note recorded beside the locator. It is
// worker-controlled text and has been through credential redaction.
func (e Evidence) Note() string { return e.note }

// evidenceWire is Evidence's stored shape. It exists because the Go fields are
// unexported by design, and the encoding must not be what re-opens the hole
// the unexported fields close.
type evidenceWire struct {
	Locator event.Locator `json:"locator"`
	Note    string        `json:"note,omitempty"`
}

// MarshalJSON writes the locator and note.
func (e Evidence) MarshalJSON() ([]byte, error) {
	return json.Marshal(evidenceWire{Locator: e.locator, Note: e.note})
}

// UnmarshalJSON reads a stored evidence record and re-validates its locator,
// so a row edited outside Babel cannot reintroduce an unlocatable claim.
func (e *Evidence) UnmarshalJSON(b []byte) error {
	var w evidenceWire
	if err := json.Unmarshal(b, &w); err != nil {
		return fmt.Errorf("evidence: decode: %w", err)
	}
	if err := validLocator(w.Locator); err != nil {
		return err
	}
	e.locator, e.note = w.Locator, w.Note
	return nil
}

// validLocator checks that a locator can actually recover bytes: a path, a
// 1-based line, a non-negative offset, and the record digest internal/event
// computes (bare lowercase hex, not a prefixed digest string).
func validLocator(l event.Locator) error {
	switch {
	case l.Path == "":
		return fmt.Errorf("evidence: locator has no path")
	case l.Line < 1:
		return fmt.Errorf("evidence: locator line must be 1-based")
	case l.ByteOffset < 0:
		return fmt.Errorf("evidence: locator byte offset is negative")
	case len(l.Digest) != 64 || !isHex(l.Digest):
		return fmt.Errorf("evidence: locator digest is not a record digest")
	}
	return nil
}

// CookbookAsset is one versioned cookbook asset a run applied. The reference
// is internal/worker's, because the recipe identity Babel sends to the worker
// and the one it records must be the same type rather than two that agree by
// convention.
type CookbookAsset struct {
	// Kind is AssetPolicy, AssetLens or AssetMeta.
	Kind string           `json:"kind"`
	Ref  worker.RecipeRef `json:"ref"`
}

// FrontierScope is where on the durable hypothesis frontier a run started
// (SPEC.md §5.2, §7). Roots are the candidates it was pointed at; Prior names
// the hypotheses already in scope, which is what makes a descendant's lineage
// checkable rather than asserted.
//
// Both may be empty: broad discovery starts from no root at all, and recording
// that as an empty list is a different statement from recording roots that
// were never selected.
type FrontierScope struct {
	Roots []string `json:"roots,omitempty"`
	Prior []string `json:"prior,omitempty"`
}

// CapabilityVersions records the version of each capability implementation the
// run was given (SPEC.md §7). They are separate from the capability names in
// the worker's grant: the grant says what was allowed, these say which build
// of the sandbox, broker, repository materializer and research broker actually
// enforced it, which is what a containment question asked months later needs.
//
// An empty string means the facility was not part of this run. NewReceipt
// refuses a receipt whose grant includes a capability whose facility carries
// no version, so "absent" can only mean absent.
type CapabilityVersions struct {
	Sandbox        string `json:"sandbox,omitempty"`
	Tool           string `json:"tool,omitempty"`
	Repository     string `json:"repository,omitempty"`
	PublicResearch string `json:"public_research,omitempty"`
}

// JobVersions is the analysis job's own versioning (SPEC.md §7): the job
// document schema Babel wrote, the versioned prompt it carried, and the
// structured result schema the worker's output was validated against. A change
// to any of the three can change what a run produces without any source or
// recipe changing, which is why they are recorded separately from the recipes.
type JobVersions struct {
	Job    int    `json:"job"`
	Prompt string `json:"prompt"`
	Schema string `json:"schema"`
}

// PolicyVersions is the redaction and disclosure policy the run ran under
// (SPEC.md §7). Redaction decides what left the machine; disclosure decides
// what class of destination it could leave for. Both are versioned because a
// later review of what was disclosed is meaningless without knowing which
// rules were in force.
type PolicyVersions struct {
	Redaction  string `json:"redaction"`
	Disclosure string `json:"disclosure"`
}

// RetrievalResult is one hit of one retrieval step.
//
// Rank is presentation order and nothing else. SPEC.md §5.4 forbids retrieval
// rank from becoming evidence strength, so nothing downstream may read this
// number as confidence; it exists so a reviewer can reproduce the order the
// investigator saw.
type RetrievalResult struct {
	Rank     int      `json:"rank"`
	Evidence Evidence `json:"evidence"`
}

// RetrievalStep is one retrieval the run performed, part of the retrieval
// trace SPEC.md §6.5 requires. Recording the query as well as the hits is what
// makes a thin conclusion diagnosable: an investigator that never searched for
// the contradicting term looks the same as one that searched and found
// nothing, unless the trace distinguishes them.
type RetrievalStep struct {
	// Index is the step's 1-based position in the run, so the trace stays
	// ordered independently of how it is stored or displayed.
	Index int `json:"index"`
	// Tool is the retrieval facility used.
	Tool string `json:"tool"`
	// Query is the query as issued, after credential redaction.
	Query string `json:"query"`
	// At is when Babel served the retrieval.
	At time.Time `json:"at"`
	// Results are the hits in the order they were returned. An empty result
	// set is a recorded outcome, not a missing record.
	Results []RetrievalResult `json:"results,omitempty"`
	// Scope says which surface the retrieval read. Empty is the corpus,
	// which is what every receipt written before #87's frontier
	// self-retrieval means, so an old receipt reads correctly rather than
	// reading as an unknown scope.
	Scope string `json:"scope,omitempty"`
	// Records are the durable frontier records a self-retrieval disclosed,
	// by identifier and in the order they were served.
	//
	// They are not Results because they are not that kind of thing. A corpus
	// hit is recovered by an event locator — file, line, offset, digest —
	// and Evidence refuses to exist without one; a frontier record is
	// recovered by its id, which is the whole of its address, and inventing
	// a locator that pointed at the durable database would be a worse
	// pointer than the id it wrapped. §9's plaintext allowlist admits
	// structured identifiers, which is why they can be recorded here at all.
	Records []string `json:"records,omitempty"`
	// Research is the public source a brokered research fetch read, present
	// exactly on the steps whose Scope is research. It is a third
	// representation beside Results and Records for the same reason those
	// two are separate: a fetched document is recovered by URL and content
	// digest, and neither an event locator nor a database id is that
	// address.
	//
	// SPEC.md §2.6 fixes what a brokered fetch must return and §6.5 makes
	// the research source part of the receipt, so these are the fields
	// rather than a summary of them. The content is deliberately absent: the
	// digest is what makes a citation checkable, and a receipt carrying
	// fetched pages would put a copy of the public web inside the operator's
	// durable store.
	Research *ResearchSource `json:"research,omitempty"`
}

// ScopeResearch is the retrieval scope of a brokered public fetch. It lives
// here rather than beside the facility that serves it because this package
// validates the pairing: a step in this scope must carry a ResearchSource and
// a step in any other must not, and an invariant checked here cannot be
// enforced against a constant defined somewhere else.
const ScopeResearch = "research"

// ResearchSource is one brokered public fetch as the receipt records it
// (SPEC.md §2.6: source URL, retrieval time, redirect chain, content digest).
//
// The redirect chain is recorded rather than collapsed because a document
// served from somewhere other than the URL the operator fixed is a different
// fact about provenance, and a reviewer re-fetching the source months later
// needs to see the hop that was followed to know whether they are looking at
// the same thing.
type ResearchSource struct {
	// SourceID is the opaque identifier the run's fixed source set minted,
	// which is also the only thing the worker got to choose about the
	// request.
	SourceID string `json:"source_id"`
	// URL is the source as the operator fixed it, validated to carry no
	// userinfo before the run started.
	URL string `json:"url"`
	// RetrievedAt is when the fetch completed.
	RetrievedAt time.Time `json:"retrieved_at"`
	// Redirects is the chain actually followed, in order. Empty means the
	// source answered directly.
	Redirects []string `json:"redirects,omitempty"`
	// MediaType is what the source said it served, without parameters.
	MediaType string `json:"media_type"`
	// Digest covers exactly the bytes served to the worker, so a citation
	// can be checked against a re-fetch.
	Digest digest.Digest `json:"digest"`
	// Bytes is how many bytes were served, which for a truncated document is
	// the run's bound rather than the source's length.
	Bytes int64 `json:"bytes"`
	// Truncated reports that the source's document is longer than what was
	// served.
	Truncated bool `json:"truncated,omitempty"`
}

// Candidate is one hypothesis a finite run surfaced but did not develop:
// deferred to the durable frontier or rejected outright (SPEC.md §6.5).
// Recording both is what keeps "resource limits choose what is explored now,
// not what ideas are permitted to exist" (§5.2) auditable.
//
// Origin is optional, and deliberately so. §4.2 allows a candidate to be
// speculative and uncategorized; only an observation is required to carry
// evidence (§4.3). When origin cues do exist they are recorded as Evidence, so
// they cannot be separated from their locators.
type Candidate struct {
	ID     string     `json:"id"`
	Reason string     `json:"reason"`
	At     time.Time  `json:"at"`
	Origin []Evidence `json:"origin,omitempty"`
}

// Failure is one failure Babel's own control plane recorded (SPEC.md §6.5).
// It is distinct from the worker's failure record, which the embedded worker
// receipt already holds: a worker that reported its own failure behaved
// correctly, while these are failures of preparation, authorization, brokering
// or storage on Babel's side.
type Failure struct {
	Stage   string    `json:"stage"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

// Resources is the run's resource use. Every field is a pointer because
// absence is not zero: where Code reported nothing, the receipt says nothing,
// rather than recording a zero that reads as a measurement. This is the same
// rule SPEC.md §3 applies to adapter metadata — a value is never synthesized
// to satisfy a shape — and internal/worker already applies it to its own
// resource block by making the whole block a pointer.
//
// The distinction survives storage: an absent value encodes as JSON null and
// decodes back to nil, so a reviewer can tell "the sandbox wrote nothing" from
// "nobody counted".
//
// A measured value is written with new(expr): Resources{ToolCalls: new(0)}
// records a counted zero, while leaving the field nil records that nobody
// counted.
type Resources struct {
	CPUSeconds          *float64 `json:"cpu_seconds"`
	MaxRSSBytes         *int64   `json:"max_rss_bytes"`
	SandboxBytesWritten *int64   `json:"sandbox_bytes_written"`
	ToolCalls           *int     `json:"tool_calls"`
}

// Timing is Babel's own clock reading for the whole run, which is wider than
// the worker boundary's: it includes preparation, authorization and storage.
// It is Babel's clock rather than the worker's because a duration timed by the
// counterpart would be unfalsifiable.
type Timing struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// Duration is the run's wall-clock length, derived rather than stored so it
// cannot disagree with the two timestamps it comes from.
func (t Timing) Duration() time.Duration { return t.FinishedAt.Sub(t.StartedAt) }

// Counts summarizes the body without opening it. Sizes and counts are inside
// SPEC.md §9's plaintext allowlist while the body is not, so these are what a
// listing, a sync queue or an operator overview may read from a receipt whose
// payload is sealed.
//
// They are derived from the body at construction, so they cannot drift from
// what they summarize.
type Counts struct {
	ToolRequests int `json:"tool_requests"`
	ToolsDenied  int `json:"tools_denied"`
	Retrieval    int `json:"retrieval"`
	Deferred     int `json:"deferred"`
	Rejected     int `json:"rejected"`
	Failures     int `json:"failures"`
	// Redactions counts the credential-shaped values removed while building
	// this receipt. A non-zero count is itself the audit signal: something on
	// the other side of the worker boundary tried to write a credential into
	// Babel's durable record, and an operator should see that from a listing
	// rather than by reading the payload.
	Redactions int `json:"redactions"`
}

// Header is the plaintext-eligible half of a run receipt. Everything in it is
// inside SPEC.md §9's minimal allowlist — identifiers, schema version,
// ordering, commit state, counts and timestamps — so a deployment that seals
// receipt bodies into AEAD envelopes can still list, order, chain and
// reconcile them without a key.
//
// This package does not implement that sealing or any remote sync. The split
// exists so the boundary is a property of the type rather than a promise in a
// comment.
type Header struct {
	Schema        int           `json:"schema"`
	ID            ReceiptID     `json:"id"`
	RunID         string        `json:"run_id"`
	PreparationID PreparationID `json:"preparation_id"`
	// Revision is 1 for a run's first receipt and increments per amendment.
	Revision int `json:"revision"`
	// Supersedes names the revision this one replaces, empty at revision 1.
	// Amendment appends a linked revision; it never edits the prior record.
	Supersedes ReceiptID `json:"supersedes,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
	// Authority is why this run happened (#96). It is the one field a
	// receipt may lack: records written before the field existed carry no
	// authority, and Recorded distinguishes that from a claim.
	Authority Authority `json:"authority"`
	Sync      string    `json:"sync"`
	Counts    Counts    `json:"counts"`
}

// Body is the sensitive half of a run receipt: the material SPEC.md §9 names
// as a payload that must travel as a randomized AEAD envelope rather than as
// plaintext. It holds queries, reasons, worker metadata, failure messages and
// the worker's own structured output.
type Body struct {
	// Cookbook is the policy and lens identities and versions the run applied
	// (SPEC.md §7).
	Cookbook []CookbookAsset `json:"cookbook"`
	// Frontier is the run's starting position on the hypothesis frontier.
	Frontier FrontierScope `json:"frontier"`
	// Capabilities is the version of each capability facility that enforced
	// the grant.
	Capabilities CapabilityVersions `json:"capabilities"`
	// Job and Policy are the analysis job's and the redaction/disclosure
	// policy's versions.
	Job    JobVersions    `json:"job"`
	Policy PolicyVersions `json:"policy"`
	// Worker is the worker boundary's own receipt, embedded whole rather than
	// summarized: it is already the record of the profile reference, the
	// capability grant, every tool request with its decision, the resolved
	// provider/model/thinking metadata Code returned, the Code build's
	// identity, and that boundary's failures, resources and timing. §7's
	// "Code version and profile ID/revision" and "resolved provider/model/
	// thinking metadata returned by Code" are read from here.
	//
	// It is nil only when the run never reached the worker at all, and
	// NewReceipt then requires at least one Failure explaining why — otherwise
	// a receipt could silently lack the provenance §7 makes mandatory.
	Worker *worker.Receipt `json:"worker"`
	// Retrieval is the retrieval trace (§6.5).
	Retrieval []RetrievalStep `json:"retrieval,omitempty"`
	// Deferred and Rejected are the candidates the run did not develop.
	Deferred []Candidate `json:"deferred,omitempty"`
	Rejected []Candidate `json:"rejected,omitempty"`
	// Failures are Babel-side failures.
	Failures []Failure `json:"failures,omitempty"`
	// Resources is Babel's resource accounting, with absence distinguished
	// from zero.
	Resources Resources `json:"resources"`
	// Timing is the whole run's wall clock.
	Timing Timing `json:"timing"`
	// AmendmentReason explains why this revision exists. It is required from
	// revision 2 and forbidden at revision 1, so an amendment can never be
	// recorded without saying what it corrects.
	AmendmentReason string `json:"amendment_reason,omitempty"`
}

// Receipt is one run's immutable provenance record.
//
// It makes a run reproducible enough to inspect, not deterministic enough to
// promise identical ideas (SPEC.md §7). Re-running an identical receipt's
// inputs is allowed and may produce entirely different hypotheses; nothing
// here is a cache key, and treating it as one would turn "we recorded what
// happened" into "we promise what will happen".
//
// The three parts are separate because they have different lifetimes and
// different secrecy. Header is plaintext-eligible. Preparation is a separately
// addressed immutable record shared by every run over the same scope, stored
// once and referenced by ID, so a receipt cannot carry a second copy of the
// corpus scope that disagrees with the first. Body is the sensitive payload.
type Receipt struct {
	Header      Header      `json:"header"`
	Preparation Preparation `json:"preparation"`
	Body        Body        `json:"body"`
}

// NewReceipt records a run's first receipt over prep.
//
// The authority is mandatory: issue #96 makes "no nameable authority, no run"
// the scheduling counterpart of §2.6's intentionality rule, and a receipt is
// where the claim becomes checkable. A caller that cannot say why the run
// happened records nothing rather than a run attributable to no one.
//
// The body is deep-copied and passed through credential redaction before it is
// validated, so no value a counterpart supplied can reach the stored record or
// any error this package returns. Errors here name fields and never values,
// for the same reason.
func NewReceipt(id ReceiptID, runID string, prep Preparation, authority Authority,
	body Body, recordedAt time.Time) (Receipt, error) {
	return newReceipt(id, runID, prep, authority, body, recordedAt, 1, "")
}

// Amend records the next revision of a run's receipt.
//
// Receipts are append-only: prior is not modified, not deleted and not
// rewritten. The new revision links back to it, and the store refuses a second
// revision claiming the same predecessor, so the history of a run is a chain a
// reviewer can walk rather than a set of competing versions.
//
// The authority is inherited rather than supplied. A resumed run is the same
// run: the invitation that authorized it does not become a different reason
// because the first attempt was interrupted, and letting an amendment restate
// the why would make the record of who started a run editable after the fact.
// A prior revision that carries none — a receipt from before the field
// existed — is amended as it stands, because inventing an authority for it
// would be worse than recording that nobody wrote one down.
func Amend(prior Receipt, id ReceiptID, body Body, recordedAt time.Time) (Receipt, error) {
	if prior.Header.ID == "" || prior.Header.Revision < 1 {
		return Receipt{}, fmt.Errorf("receipt: amend needs a recorded prior revision")
	}
	if body.AmendmentReason == "" {
		return Receipt{}, fmt.Errorf("receipt: amendment reason is required")
	}
	return newReceipt(id, prior.Header.RunID, prior.Preparation, prior.Header.Authority, body,
		recordedAt, prior.Header.Revision+1, prior.Header.ID)
}

func newReceipt(id ReceiptID, runID string, prep Preparation, authority Authority, body Body,
	recordedAt time.Time, revision int, supersedes ReceiptID) (Receipt, error) {
	if !validIdentifier(string(id)) {
		return Receipt{}, fmt.Errorf("receipt: invalid receipt id")
	}
	if !validIdentifier(runID) {
		return Receipt{}, fmt.Errorf("receipt: invalid run id")
	}
	if recordedAt.IsZero() {
		return Receipt{}, fmt.Errorf("receipt: recorded_at is required")
	}
	// An unrecorded authority survives amendment and nothing else: a first
	// revision this build writes has to name one.
	if revision == 1 || authority.Recorded() {
		if err := authority.validate(); err != nil {
			return Receipt{}, err
		}
	}
	if err := prep.Verify(); err != nil {
		return Receipt{}, err
	}
	copied, redactions := redactBody(body)
	if revision == 1 && copied.AmendmentReason != "" {
		return Receipt{}, fmt.Errorf("receipt: revision 1 cannot carry an amendment reason")
	}
	if err := validateBody(copied); err != nil {
		return Receipt{}, err
	}
	counts := countBody(copied)
	counts.Redactions = redactions
	return Receipt{
		Header: Header{
			Schema:        ReceiptSchema,
			ID:            id,
			RunID:         runID,
			PreparationID: prep.ID,
			Revision:      revision,
			Supersedes:    supersedes,
			RecordedAt:    recordedAt.UTC(),
			Authority:     authority,
			Sync:          SyncPending,
			Counts:        counts,
		},
		Preparation: prep,
		Body:        copied,
	}, nil
}

// validateBody enforces the invariants that make a receipt worth having. It
// runs after redaction, so any string it might name has already been cleaned —
// but it names none of them anyway.
func validateBody(b Body) error {
	if len(b.Cookbook) == 0 {
		return fmt.Errorf("receipt: no cookbook asset recorded")
	}
	seen := make(map[worker.RecipeRef]struct{}, len(b.Cookbook))
	for i, a := range b.Cookbook {
		switch a.Kind {
		case AssetPolicy, AssetLens, AssetMeta:
		default:
			return fmt.Errorf("receipt: cookbook asset %d has an unknown kind", i)
		}
		if !validIdentifier(a.Ref.ID) || a.Ref.Version < 1 {
			return fmt.Errorf("receipt: cookbook asset %d has no identity and version", i)
		}
		if _, dup := seen[a.Ref]; dup {
			return fmt.Errorf("receipt: cookbook asset %d is recorded twice", i)
		}
		seen[a.Ref] = struct{}{}
	}
	for i, r := range b.Frontier.Roots {
		if !validIdentifier(r) {
			return fmt.Errorf("receipt: frontier root %d is not an identifier", i)
		}
	}
	for i, h := range b.Frontier.Prior {
		if !validIdentifier(h) {
			return fmt.Errorf("receipt: prior hypothesis %d is not an identifier", i)
		}
	}
	if b.Job.Job < 1 || b.Job.Prompt == "" || b.Job.Schema == "" {
		return fmt.Errorf("receipt: analysis job/prompt/schema version is incomplete")
	}
	if b.Policy.Redaction == "" || b.Policy.Disclosure == "" {
		return fmt.Errorf("receipt: redaction/disclosure policy version is incomplete")
	}
	if b.Timing.StartedAt.IsZero() || b.Timing.FinishedAt.IsZero() {
		return fmt.Errorf("receipt: run timing is incomplete")
	}
	if b.Timing.FinishedAt.Before(b.Timing.StartedAt) {
		return fmt.Errorf("receipt: run finished before it started")
	}
	if b.Worker == nil {
		if len(b.Failures) == 0 {
			return fmt.Errorf("receipt: a run with no worker receipt must record why")
		}
	} else if err := validateGrantedFacilities(b); err != nil {
		return err
	}
	if err := validateTrace(b); err != nil {
		return err
	}
	for _, set := range [][]Candidate{b.Deferred, b.Rejected} {
		for i, c := range set {
			if !validIdentifier(c.ID) {
				return fmt.Errorf("receipt: candidate %d has no identity", i)
			}
			if c.Reason == "" {
				return fmt.Errorf("receipt: candidate %d has no reason", i)
			}
			if c.At.IsZero() {
				return fmt.Errorf("receipt: candidate %d has no time", i)
			}
			for j := range c.Origin {
				if err := validLocator(c.Origin[j].locator); err != nil {
					return fmt.Errorf("receipt: candidate %d origin %d: %w", i, j, err)
				}
			}
		}
	}
	for i, f := range b.Failures {
		if f.Stage == "" || f.Code == "" || f.Message == "" {
			return fmt.Errorf("receipt: failure %d is incomplete", i)
		}
		if f.At.IsZero() {
			return fmt.Errorf("receipt: failure %d has no time", i)
		}
	}
	return nil
}

// validateGrantedFacilities refuses a receipt that cannot say which build
// enforced a capability the run was granted. An unversioned sandbox is not a
// missing detail: it is a containment question that can no longer be answered.
func validateGrantedFacilities(b Body) error {
	grant := b.Worker.Grant
	if len(grant.Capabilities) > 0 && b.Capabilities.Tool == "" {
		return fmt.Errorf("receipt: granted capabilities without a tool capability version")
	}
	for _, c := range grant.Capabilities {
		var version string
		switch c {
		case worker.CapabilitySandboxExec:
			version = b.Capabilities.Sandbox
		case worker.CapabilityRepoRead:
			version = b.Capabilities.Repository
		case worker.CapabilityPublicResearch:
			version = b.Capabilities.PublicResearch
		default:
			continue
		}
		if version == "" {
			return fmt.Errorf("receipt: granted capability without its facility version")
		}
	}
	return nil
}

// validateTrace checks the retrieval trace's ordering. Index and Rank are
// ordering, and an ordering with gaps or repeats is not one: a reviewer
// reproducing what the investigator saw needs the sequence to be exactly what
// it claims.
func validateTrace(b Body) error {
	for i, step := range b.Retrieval {
		if step.Index != i+1 {
			return fmt.Errorf("receipt: retrieval step %d is out of sequence", i)
		}
		if step.Tool == "" {
			return fmt.Errorf("receipt: retrieval step %d names no tool", i)
		}
		if step.At.IsZero() {
			return fmt.Errorf("receipt: retrieval step %d has no time", i)
		}
		for j, hit := range step.Results {
			if hit.Rank != j+1 {
				return fmt.Errorf("receipt: retrieval step %d result %d is out of rank order", i, j)
			}
			if err := validLocator(hit.Evidence.locator); err != nil {
				return fmt.Errorf("receipt: retrieval step %d result %d: %w", i, j, err)
			}
		}
		// A disclosed record with no usable identifier is refused for the
		// same reason a hit with no locator is: a trace entry nobody can
		// follow records that something was disclosed while making it
		// impossible to check what.
		for j, id := range step.Records {
			if !validIdentifier(id) {
				return fmt.Errorf("receipt: retrieval step %d record %d has no identity", i, j)
			}
		}
		if err := validateResearch(step); err != nil {
			return fmt.Errorf("receipt: retrieval step %d: %w", i, err)
		}
	}
	return nil
}

// validateResearch enforces the pairing between a step's scope and its
// research record, and refuses a record that cannot recover what it claims.
//
// Both directions are checked. A research step with no source would record
// that a run reached the public internet while making it impossible to say
// where; a source recorded under another scope would attribute a fetch to a
// corpus search. §2.6 fixes the four fields a brokered fetch must return, so
// each is required here: a fetch with no digest is a citation nobody can
// check, and one with no time cannot be correlated with the run's other
// disclosures.
func validateResearch(step RetrievalStep) error {
	if step.Scope != ScopeResearch {
		if step.Research != nil {
			return fmt.Errorf("a %q retrieval carries a research source", step.Scope)
		}
		return nil
	}
	src := step.Research
	if src == nil {
		return fmt.Errorf("a research retrieval names no source")
	}
	switch {
	case !validIdentifier(src.SourceID):
		return fmt.Errorf("the research source has no identity")
	case !validIdentifier(src.URL):
		return fmt.Errorf("research source %s has no usable URL", src.SourceID)
	case src.RetrievedAt.IsZero():
		return fmt.Errorf("research source %s has no retrieval time", src.SourceID)
	case src.MediaType == "":
		return fmt.Errorf("research source %s names no media type", src.SourceID)
	case !src.Digest.Valid():
		return fmt.Errorf("research source %s has no content digest", src.SourceID)
	case src.Bytes < 0:
		return fmt.Errorf("research source %s reports a negative size", src.SourceID)
	}
	for i, hop := range src.Redirects {
		if !validIdentifier(hop) {
			return fmt.Errorf("research source %s redirect %d is not a usable URL", src.SourceID, i)
		}
	}
	return nil
}

// countBody derives the header's counts. Deriving beats accepting them from a
// caller: a count that can be supplied is a count that can be wrong.
func countBody(b Body) Counts {
	c := Counts{
		Retrieval: len(b.Retrieval),
		Deferred:  len(b.Deferred),
		Rejected:  len(b.Rejected),
		Failures:  len(b.Failures),
	}
	if b.Worker != nil {
		c.ToolRequests = len(b.Worker.ToolRequests)
		c.ToolsDenied = b.Worker.Denied()
	}
	return c
}

// validIdentifier reports whether s is a usable identifier: non-empty, bounded,
// and free of spaces and control characters, so it cannot smuggle a terminal
// escape into a diagnostic or a newline into a log line.
func validIdentifier(s string) bool {
	if s == "" || len(s) > maxIdentifier {
		return false
	}
	for _, r := range s {
		if r <= ' ' || r == 0x7f {
			return false
		}
	}
	return true
}

// MarshalBody renders the sensitive half of a receipt for storage: the bytes a
// deployment following SPEC.md §9 would seal before they leave the machine.
func (r Receipt) MarshalBody() ([]byte, error) {
	b, err := json.Marshal(r.Body)
	if err != nil {
		return nil, fmt.Errorf("receipt: encode body: %w", err)
	}
	return b, nil
}

// unmarshalBody parses a stored body and re-validates it, so a row altered
// outside Babel cannot reintroduce an unlocatable evidence pointer or a
// receipt with no provenance.
func unmarshalBody(raw []byte) (Body, error) {
	var b Body
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil {
		return Body{}, fmt.Errorf("receipt: decode body: %w", err)
	}
	if err := validateBody(b); err != nil {
		return Body{}, err
	}
	return b, nil
}

// String renders a receipt reference for diagnostics as id@revision. It never
// renders the body.
func (r Receipt) String() string {
	return fmt.Sprintf("%s@%d", r.Header.ID, r.Header.Revision)
}
