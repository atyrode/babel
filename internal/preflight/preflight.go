// Package preflight implements the deterministic preflight of SPEC.md §6.4
// and the redaction contract of §3. It is the last thing that runs before
// inference and the only analysis stage that is allowed to be boring: same
// input, same output, no model, no network, no clock.
//
// §6.4 says the preflight's results "use the same evidence model as AI
// observations", and that symmetry is the whole design. A preflight result is
// a locator-bearing Finding of the shape §4.3 requires of an observation —
// evidence, category, confidence — produced by a rule instead of by a model.
// A human reviewing a run's evidence therefore learns one format, not two,
// and a deterministic finding is distinguishable from an inferred one by its
// Confidence rather than by living somewhere else.
//
// Three properties are load-bearing.
//
// Evidence is never separable from its locator (§4.3). Evidence holds its
// locator in an unexported field reachable only through NewEvidence, and its
// JSON decoder refuses a document whose locator cannot recover bytes, so a
// finding with nothing behind it cannot be constructed or unmarshalled.
//
// A finding never carries the secret it found. It carries the locator that
// recovers the bytes, a category, an occurrence count, and a placeholder
// rendering. A preflight report that copied credentials out of the corpus
// would have moved the exposure into a new file rather than reported it, and
// that file is the one an operator reads, exports, and forwards.
//
// Cost is per byte, not per file. Every input streams through
// internal/event's scanner, findings are deduplicated as they are produced,
// and a single input's distinct findings are capped, because the corpus this
// runs against is extremely skewed: a small number of very large sessions
// hold a large share of all bytes, so anything that buffers a whole session
// or emits a finding per occurrence is dominated by a handful of inputs.
//
// Scope: this package detects likely secrets. §6.4 also names "high-risk
// data" more broadly; no deterministic detector for personal data ships here,
// and none is claimed. The disclosure class is reported, never decided: the
// report says which findings force redaction before a hosted run, and the
// operator and control plane decide what to do about it.
//
// Two limits are worth knowing rather than discovering. What is scanned for
// secrets is internal/event's normalized text, which is bounded per record, so
// a credential past that bound inside one very large record is not seen — such
// a record is reported as oversize instead, which is the condition an operator
// can act on. And a whole input's evidence is its primary log's locator, so a
// finding about a session's size or closure names the log rather than a
// position inside it.
package preflight

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/worker"
)

// ReportVersion is the report's schema version. §9 requires every derived
// result to carry one, and a report is durable enough to outlive the build
// that wrote it.
const ReportVersion = 1

// Category is what a finding is about. The set is closed: a preflight check
// that cannot name its category has not been designed yet.
type Category string

// The preflight categories. CategorySecret is the only one that can force
// redaction before a hosted run; the rest describe corpus health and input
// provenance, which change what a run should do rather than what it may see.
const (
	CategorySecret              Category = "likely-secret"
	CategoryMalformedRecord     Category = "malformed-record"
	CategoryTruncatedInput      Category = "truncated-input"
	CategoryOversizeRecord      Category = "oversize-record"
	CategoryOversizeTranscript  Category = "oversize-transcript"
	CategoryOversizeAttachment  Category = "oversize-attachment"
	CategoryUnresolvedReference Category = "unresolved-reference"
	CategoryChangedInput        Category = "changed-input"
	CategoryDuplicateInput      Category = "duplicate-input"
)

// Confidence separates a rule that matched a structure from a rule that
// guessed, because the two deserve different treatment by a reviewer and the
// difference is invisible once both are called "detected".
type Confidence string

// The confidence levels.
const (
	// ConfidenceObserved is a fact about the corpus rather than a judgement
	// about its content: a count, a size, a digest that differs from a
	// recorded one. It cannot be a false positive, only a stale input.
	ConfidenceObserved Confidence = "observed"

	// ConfidenceStructural means the bytes matched a documented credential
	// structure — a private-key armour header, a vendor's key prefix and
	// length, a URL with a password in its userinfo. False positives are
	// possible but require material that is deliberately credential-shaped.
	ConfidenceStructural Confidence = "structural"

	// ConfidenceHeuristic means an unstructured guess: a token that is long,
	// mixed, and dense enough to look like a key. It is expected to produce
	// false positives and must never be presented as equivalent to a
	// structural match.
	ConfidenceHeuristic Confidence = "heuristic"
)

// WholeInput is the Evidence event index of a finding about an entire input
// rather than one record: a transcript's size, a reference that does not
// resolve, a digest that changed.
const WholeInput = -1

// Evidence binds a claim to the bytes behind it. The locator is unexported
// and set only by NewEvidence so that §4.3's rule — an observation cannot
// exist without evidence — is enforced by the type rather than by a review
// comment.
type Evidence struct {
	harness    string
	sourceID   string
	eventIndex int
	eventKind  event.Kind
	locator    event.Locator
}

// NewEvidence binds a locator to the input it came from. It fails when the
// locator cannot recover bytes: a path names where to look and a digest is
// what proves the bytes found there are the bytes that were seen, so evidence
// missing either is a claim with nothing behind it.
func NewEvidence(harness, sourceID string, eventIndex int, kind event.Kind, loc event.Locator) (Evidence, error) {
	switch {
	case harness == "":
		return Evidence{}, fmt.Errorf("preflight: evidence without a harness")
	case sourceID == "":
		return Evidence{}, fmt.Errorf("preflight: evidence without a source id")
	case loc.Path == "":
		return Evidence{}, fmt.Errorf("preflight: evidence locator without a path")
	case loc.Digest == "":
		return Evidence{}, fmt.Errorf("preflight: evidence locator without a digest (%s)", loc.Path)
	}
	return Evidence{harness: harness, sourceID: sourceID, eventIndex: eventIndex, eventKind: kind, locator: loc}, nil
}

// Harness, SourceID, EventIndex, EventKind, and Locator read the binding.
// EventIndex is WholeInput for a finding about a whole input.
func (e Evidence) Harness() string        { return e.harness }
func (e Evidence) SourceID() string       { return e.sourceID }
func (e Evidence) EventIndex() int        { return e.eventIndex }
func (e Evidence) EventKind() event.Kind  { return e.eventKind }
func (e Evidence) Locator() event.Locator { return e.locator }

// evidenceJSON is the wire shape. Evidence marshals through it because its
// fields are unexported by design; the alternative — exported fields plus a
// validating constructor — leaves a zero value that looks like evidence.
type evidenceJSON struct {
	Harness    string        `json:"harness"`
	SourceID   string        `json:"source_id"`
	EventIndex int           `json:"event_index"`
	EventKind  event.Kind    `json:"event_kind,omitempty"`
	Locator    event.Locator `json:"locator"`
}

// MarshalJSON writes the binding.
func (e Evidence) MarshalJSON() ([]byte, error) {
	return json.Marshal(evidenceJSON{
		Harness:    e.harness,
		SourceID:   e.sourceID,
		EventIndex: e.eventIndex,
		EventKind:  e.eventKind,
		Locator:    e.locator,
	})
}

// UnmarshalJSON refuses a document whose evidence cannot recover bytes, so a
// report that round-trips through JSON cannot acquire a locator-less finding
// on the way back in.
func (e *Evidence) UnmarshalJSON(b []byte) error {
	var raw evidenceJSON
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("preflight: decode evidence: %w", err)
	}
	decoded, err := NewEvidence(raw.Harness, raw.SourceID, raw.EventIndex, raw.EventKind, raw.Locator)
	if err != nil {
		return err
	}
	*e = decoded
	return nil
}

// Finding is one preflight result: §4.3's shape, produced deterministically.
//
// Nothing here holds corpus content. Summary is the check's own text, which
// may quote a reason Babel's own adapter produced but never a transcript;
// Placeholder is the stable rendering of a redacted value; Reference names a
// blob reference, a sibling input, or an artifact path; and Measure/Limit are
// the size and the threshold that a size check compared. The bytes stay in
// the corpus, behind Evidence.
type Finding struct {
	// ID is stable across runs over unchanged input, so two reports can be
	// compared and a reviewed finding can be recognized again.
	ID         string     `json:"id"`
	Category   Category   `json:"category"`
	Detector   string     `json:"detector"`
	Confidence Confidence `json:"confidence"`
	Summary    string     `json:"summary"`

	// Placeholder is what Redact substitutes for this value, so a reviewer
	// can find the same value in a redacted transcript without seeing it.
	Placeholder string `json:"placeholder,omitempty"`
	// ValueBytes is the length of the matched value. A length is metadata; it
	// is reported because a 20-byte match and a 3000-byte match are different
	// kinds of exposure.
	ValueBytes int `json:"value_bytes,omitempty"`

	// Occurrences is how many times this finding's condition was observed in
	// its input. Repeats are counted rather than emitted: a credential pasted
	// two hundred times is one finding that recurred, and a report that grows
	// with the corpus is a report nobody reads.
	Occurrences int `json:"occurrences"`

	// Measure and Limit are the observed value and the threshold a size check
	// compared, in bytes. Measure is the largest observation when the finding
	// was deduplicated.
	Measure int64 `json:"measure,omitempty"`
	Limit   int64 `json:"limit,omitempty"`

	// Reference names non-secret material the finding is about: an
	// unresolved blob reference, the sibling input a duplicate matched, or
	// the artifact path an attachment finding concerns.
	Reference string `json:"reference,omitempty"`

	Evidence Evidence `json:"evidence"`
}

// identity derives Finding.ID. It hashes what makes this finding this one and
// nothing that varies between runs: the category, the detector, the record's
// own digest and position, and the placeholder or reference that distinguishes
// two findings inside one record. The value never enters it, because the ID is
// stored and displayed.
//
// The digest rather than the path is what anchors it, so the same session
// checked from a different materialization keeps its finding IDs.
func (f Finding) identity() string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	loc := f.Evidence.Locator()
	write(
		"babel/preflight/finding/v1",
		string(f.Category),
		f.Detector,
		f.Evidence.Harness(),
		f.Evidence.SourceID(),
		loc.Digest,
		strconv.Itoa(loc.Line),
		strconv.FormatInt(loc.ByteOffset, 10),
		f.Placeholder,
		f.Reference,
	)
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8])
}

// Validate reports a finding that cannot be reviewed: one with no evidence,
// no category, or no confidence. Check calls it on everything it emits, and a
// caller that builds findings from a decoded report can call it again.
func (f Finding) Validate() error {
	switch {
	case f.ID == "":
		return fmt.Errorf("preflight: finding without an id")
	case f.Category == "":
		return fmt.Errorf("preflight: finding %s without a category", f.ID)
	case f.Confidence == "":
		return fmt.Errorf("preflight: finding %s without a confidence", f.ID)
	case f.Occurrences < 1:
		return fmt.Errorf("preflight: finding %s observed %d times", f.ID, f.Occurrences)
	}
	if _, err := NewEvidence(f.Evidence.harness, f.Evidence.sourceID, f.Evidence.eventIndex,
		f.Evidence.eventKind, f.Evidence.locator); err != nil {
		return fmt.Errorf("preflight: finding %s: %w", f.ID, err)
	}
	return nil
}

// Disclosure is §6.4's disclosure-class result and §3's step 4. It reports;
// it does not decide. RedactionRequired says a hosted run may not proceed on
// raw material, and Forcing names the findings that make that so, because
// "redaction required" without the reasons is a verdict rather than evidence.
type Disclosure struct {
	Profile worker.ProfileRef `json:"profile"`
	worker.Privacy
	// Forcing lists the IDs of the findings that require redaction, sorted.
	// It is empty for a local run even when secrets were found: local
	// evidence keeps its locators to the original (§3).
	Forcing []string `json:"forcing,omitempty"`
}

// Stats are the corpus-health counts §6.4 asks for, aggregated over the
// checked inputs. They are what a preparation records; the findings are what
// a reviewer reads.
type Stats struct {
	Inputs  int   `json:"inputs"`
	Bytes   int64 `json:"bytes"`
	Records int   `json:"records"`
	Events  int   `json:"events"`

	MalformedRecords    int `json:"malformed_records"`
	TruncatedInputs     int `json:"truncated_inputs"`
	OversizeRecords     int `json:"oversize_records"`
	OversizeTranscripts int `json:"oversize_transcripts"`

	Attachments         int   `json:"attachments"`
	AttachmentBytes     int64 `json:"attachment_bytes"`
	OversizeAttachments int   `json:"oversize_attachments"`

	UnresolvedReferences int `json:"unresolved_references"`

	SecretFindings           int `json:"secret_findings"`
	StructuralSecretFindings int `json:"structural_secret_findings"`
	HeuristicSecretFindings  int `json:"heuristic_secret_findings"`

	NewInputs       int `json:"new_inputs"`
	UnchangedInputs int `json:"unchanged_inputs"`
	ChangedInputs   int `json:"changed_inputs"`
	DuplicateInputs int `json:"duplicate_inputs"`

	// FindingsOmitted counts distinct findings dropped by
	// Thresholds.MaxFindingsPerInput. A truncated report says so rather than
	// looking complete.
	FindingsOmitted int `json:"findings_omitted"`
}

// Report is one preflight. It carries its own schema version and the
// thresholds it applied, so a report read months later explains why it says
// what it says instead of depending on the build that produced it.
type Report struct {
	Version    int        `json:"version"`
	Disclosure Disclosure `json:"disclosure"`
	Thresholds Thresholds `json:"thresholds"`
	Stats      Stats      `json:"stats"`
	Findings   []Finding  `json:"findings"`
}

// Validate reports the first structural problem in a report. It is the check
// a decoded report should pass before anything acts on it.
func (r *Report) Validate() error {
	if r.Version != ReportVersion {
		return fmt.Errorf("preflight: report version %d, want %d", r.Version, ReportVersion)
	}
	seen := make(map[string]bool, len(r.Findings))
	for _, f := range r.Findings {
		if err := f.Validate(); err != nil {
			return err
		}
		if seen[f.ID] {
			return fmt.Errorf("preflight: duplicate finding id %s", f.ID)
		}
		seen[f.ID] = true
	}
	for _, id := range r.Disclosure.Forcing {
		if !seen[id] {
			return fmt.Errorf("preflight: disclosure names finding %s, which the report does not contain", id)
		}
	}
	return nil
}

// Input is one session to check, as the adapter that discovered it describes
// it. Digest is required: duplicate and changed-input detection compares
// digests, and evidence about a whole input needs one to be recoverable.
type Input struct {
	// Stream identifies the session and names its primary log on disk.
	Stream event.Stream
	// Digest is the primary log's content digest, in the caller's canonical
	// form (internal/digest's "sha256:<hex>").
	Digest string

	// Attachments are the sibling artifacts the adapter reported. Preflight
	// checks their metadata and size; it never opens them, because closure
	// and content digesting belong to the adapter that discovered them.
	Attachments []Attachment
	// Unresolved are the references the adapter could not resolve — an OMP
	// blob reference or a Codex attachment reference whose bytes are absent
	// or do not match their name. Preflight reports the closure result; it
	// does not recompute it.
	Unresolved []Reference
}

// Attachment is one sibling artifact. Digest is optional because an adapter
// reports artifact paths and sizes without hashing them; a finding about an
// attachment with no digest is evidenced by its session's locator instead,
// with the artifact named in Finding.Reference.
type Attachment struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Digest string `json:"digest,omitempty"`
}

// Reference is one reference that did not resolve, with the adapter's reason.
type Reference struct {
	Ref    string `json:"ref"`
	Reason string `json:"reason,omitempty"`
}

// Preparation is a prior preparation's input list, against which this run's
// inputs are compared. It is supplied by the caller that owns preparation
// records rather than read from a database here: preflight computes, and
// storage belongs to the layer that owns the durable file.
type Preparation struct {
	ID     string       `json:"id"`
	Inputs []PriorInput `json:"inputs"`
}

// PriorInput is one input as a prior preparation recorded it.
type PriorInput struct {
	SourceID string `json:"source_id"`
	Digest   string `json:"digest"`
}

// Request is one preflight invocation.
type Request struct {
	// Profile and Disclosure are the selected Code profile and its class
	// (worker.DisclosureLocal or worker.DisclosureHosted). The class is
	// fixed before material is sent (§3), so preflight is told it rather
	// than resolving it.
	Profile    worker.ProfileRef
	Disclosure string

	Inputs []Input

	// Prior is the preparation this run's inputs are compared against, or
	// nil for a first preparation.
	Prior *Preparation

	// Thresholds overrides DefaultThresholds when non-nil.
	Thresholds *Thresholds
}

// Check runs the deterministic preflight. It streams every input's primary
// log once, and returns a report whose findings are sorted into a total order
// that does not depend on the order the inputs were given in.
//
// It returns an error only for a request it cannot honour — an unknown
// disclosure class, an input with no digest, an unreadable primary log —
// because a preflight that silently skipped an input would report a clean
// corpus it never looked at.
func Check(req Request) (*Report, error) {
	th := DefaultThresholds()
	if req.Thresholds != nil {
		th = *req.Thresholds
	}
	if err := th.validate(); err != nil {
		return nil, err
	}
	switch req.Disclosure {
	case worker.DisclosureLocal, worker.DisclosureHosted:
	default:
		return nil, fmt.Errorf("preflight: unknown disclosure class %q, want %q or %q",
			req.Disclosure, worker.DisclosureLocal, worker.DisclosureHosted)
	}

	c := &checker{
		th:         th,
		index:      make(map[findingKey]int),
		perInput:   make(map[string]int, len(req.Inputs)),
		priorBySID: make(map[string]string, len(priorInputs(req.Prior))),
		priorDig:   make(map[string]string, len(priorInputs(req.Prior))),
		digests:    make(map[string]string, len(req.Inputs)),
	}
	for _, p := range priorInputs(req.Prior) {
		c.priorBySID[p.SourceID] = p.Digest
		if _, ok := c.priorDig[p.Digest]; !ok {
			c.priorDig[p.Digest] = p.SourceID
		}
	}
	for _, in := range req.Inputs {
		if err := c.check(in); err != nil {
			return nil, err
		}
	}

	rep := &Report{
		Version:    ReportVersion,
		Thresholds: th,
		Stats:      c.stats,
		Findings:   c.finalize(),
	}
	rep.Disclosure = Disclosure{
		Profile: req.Profile,
		Privacy: worker.Privacy{Disclosure: req.Disclosure},
	}
	if req.Disclosure == worker.DisclosureHosted {
		for _, f := range rep.Findings {
			if f.Category == CategorySecret {
				rep.Disclosure.Forcing = append(rep.Disclosure.Forcing, f.ID)
			}
		}
		rep.Disclosure.RedactionRequired = len(rep.Disclosure.Forcing) > 0
	}
	if err := rep.Validate(); err != nil {
		return nil, err
	}
	return rep, nil
}

func priorInputs(p *Preparation) []PriorInput {
	if p == nil {
		return nil
	}
	return p.Inputs
}

// finalize sorts the findings into a total order and assigns their IDs.
//
// The order is derived entirely from the findings themselves — source id,
// then position within the input, then category and detector — so it does not
// depend on the order Request.Inputs happened to be in. The ID breaks the
// last tie, which keeps the sort total even for two findings that agree on
// everything a reader can see.
func (c *checker) finalize() []Finding {
	out := c.findings
	for i := range out {
		out[i].ID = out[i].identity()
	}
	slices.SortFunc(out, func(a, b Finding) int {
		loca, locb := a.Evidence.Locator(), b.Evidence.Locator()
		return cmp.Or(
			cmp.Compare(a.Evidence.SourceID(), b.Evidence.SourceID()),
			cmp.Compare(loca.Line, locb.Line),
			cmp.Compare(loca.ByteOffset, locb.ByteOffset),
			cmp.Compare(string(a.Category), string(b.Category)),
			cmp.Compare(a.Detector, b.Detector),
			cmp.Compare(a.Reference, b.Reference),
			cmp.Compare(a.ID, b.ID),
		)
	})
	return out
}
