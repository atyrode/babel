// Package reality implements the Reality Ledger of SPEC.md §4.8: the versioned
// record of non-GitHub reality that keeps Babel's understanding of projects,
// machines, services, and their policies out of freeform model memory.
//
// Five properties shape everything here, and each one is a refused write
// rather than a convention.
//
// Nothing is mutated and nothing is deleted. An entity, a fact, an answer, a
// plan, and a snapshot are immutable rows protected by database triggers; a
// correction is a superseding revision that leaves its ancestor byte-identical,
// and a lifecycle change is an appended event. The only columns that ever
// change after insert are a plan action's application state and its result
// link, which record what Babel did with the row rather than what the row says.
//
// Authority is narrow and explicit. §4.8 permits only attributed operator
// actions and configured trusted sources to authorize a fact. Git activity,
// repository inspection, conversation, and Babel's own analysis produce
// observations or proposed revisions, so a fact whose authority is an
// observation can never reach `active`, and a trusted source that authors
// outside its declared predicate and entity scope is refused rather than
// trusted a little.
//
// Lifecycle never implies an expenditure policy. §4.8 is explicit that the
// mapping from what a ledger says to what analysis may spend is performed by
// explicit versioned focus rules. So a lifecycle fact has no effect on any
// decision by itself: focus is evaluated against a stored, versioned rule set,
// the decision records which facts it read, and the same ledger under two rule
// versions yields two different decisions.
//
// Interpretation never becomes reality on its own. An answer is retained
// verbatim, an interpreter's plan is a proposal, and a plan's non-authoritative
// descendants — hypotheses, follow-up questions, requests — may be retained
// immediately while every fact, entity-resolution, and focus-policy mutation
// waits for exactly one explicit operator acceptance that commits atomically
// with the question's disposition. The action vocabulary is closed and contains
// no way to create a proposal or to publish anything: §4.6 and decision 13
// make the hypothesis → observation → finding → proposal path mandatory, and a
// request recorded here is consumed by the review pipeline rather than applied.
//
// Storage follows §9's split. Every record carries exactly one Payload field
// holding what §9 requires to be sealed before it leaves the machine: display
// names, alias values, claim text, answer text, provenance notes, and
// reasoning. Every other column is drawn from §9's plaintext allowlist —
// identifiers, kinds, schema versions, ordering, counts, states, and
// timestamps — so the sync slice replaces one column per table rather than
// auditing a field list. The tables join internal/frontier's and internal/run's
// in `<dir>/durable.db` under the component key `reality` and the table prefix
// `reality_`.
package reality

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/preflight"
)

// RecordSchema is the version stamped on every record this build writes. §9
// requires every row to carry a schema version; it is plaintext-eligible
// because it describes the shape of the envelope rather than its contents.
const RecordSchema = 1

// Sentinel errors for the invariants a caller is expected to handle rather
// than merely report. They are distinct from one another because refusing a
// write for the wrong reason is indistinguishable from a bug.
var (
	// ErrInvalidValue reports a malformed or out-of-vocabulary input.
	ErrInvalidValue = errors.New("reality: invalid value")
	// ErrUnknownRecord reports a reference to a record the ledger does not
	// hold. It is returned before the insert so the caller learns which
	// reference dangled rather than reading a foreign-key message.
	ErrUnknownRecord = errors.New("reality: unknown record")
	// ErrNotAuthoritative reports an attempt to make a fact active on
	// authority §4.8 does not accept: anything other than an attributed
	// operator action or a configured trusted source.
	ErrNotAuthoritative = errors.New("reality: authority cannot authorize a fact")
	// ErrOutsideScope reports a trusted source authoring a predicate or an
	// entity outside the scope it declared when it was registered.
	ErrOutsideScope = errors.New("reality: outside the trusted source's declared scope")
	// ErrCredentialMaterial reports credential-shaped material in a fact.
	// §4.8 forbids credentials in the ledger outright, so this refuses the
	// whole write rather than redacting it: a ledger that silently accepted
	// a redacted credential would have recorded that a secret was there.
	ErrCredentialMaterial = errors.New("reality: credential-shaped material is forbidden in the ledger")
	// ErrConflict reports a revision chain that would fork: a fact that is
	// already superseded, or a resolution already reversed.
	ErrConflict = errors.New("reality: record already has a successor")
	// ErrInvalidTransition reports a state machine transition §4.8 does not
	// allow. Question state is derived from an append-only history, so an
	// illegal transition is refused rather than recorded and ignored.
	ErrInvalidTransition = errors.New("reality: illegal state transition")
	// ErrAmbiguousAlias reports an alias that resolves to more than one
	// canonical entity. §4.8 makes entity-alias resolution a Question, so
	// ambiguity is reported for a human rather than guessed.
	ErrAmbiguousAlias = errors.New("reality: alias resolves to several entities")
	// ErrDuplicateQuestion reports an ask that repeats a question already
	// live in the inbox.
	ErrDuplicateQuestion = errors.New("reality: an equivalent question is already open")
	// ErrSuppressed reports an ask suppressed by a `declined` or `unknown`
	// outcome that no materially new evidence has displaced (§4.8).
	ErrSuppressed = errors.New("reality: question suppressed until materially new evidence")
	// ErrAlreadyDecided reports a second acceptance or rejection of one
	// plan. §4.8 requires exactly one explicit operator acceptance.
	ErrAlreadyDecided = errors.New("reality: plan already accepted or rejected")
	// ErrNoHypothesisSink reports a plan that would create a hypothesis
	// with no frontier configured to retain it. The promise that a
	// non-authoritative descendant is retained immediately cannot be kept
	// halfway, so the plan is refused instead.
	ErrNoHypothesisSink = errors.New("reality: no hypothesis sink configured")
	// ErrNotReversible reports an attempt to reverse something that is not
	// a reversible resolution, or one that has already been reversed.
	ErrNotReversible = errors.New("reality: resolution is not reversible")
)

// Sensitivity grades how freely a record may be shown, in the vocabulary §9's
// allowlist implies: `routine` material could be projected after §6.7's
// redaction, `sensitive` material is private evidence, and `restricted`
// material carries disclosure risk that must weigh on the inbox. It is three
// valued rather than a free string so the inbox's security term is computable.
type Sensitivity string

// The sensitivity grades.
const (
	SensitivityRoutine    Sensitivity = "routine"
	SensitivitySensitive  Sensitivity = "sensitive"
	SensitivityRestricted Sensitivity = "restricted"
)

func (s Sensitivity) valid() bool {
	switch s {
	case SensitivityRoutine, SensitivitySensitive, SensitivityRestricted:
		return true
	}
	return false
}

// weight is the sensitivity's contribution to the inbox's security term.
// §4.8 ranks by "security/disclosure impact", and a restricted fact left
// unresolved is the case where asking early is cheapest.
func (s Sensitivity) weight() int {
	switch s {
	case SensitivityRestricted:
		return 2
	case SensitivitySensitive:
		return 1
	}
	return 0
}

// Confidence is a coarse grading of how sure the authorizing party is. It
// matches internal/frontier's three-valued grading for the same reason §10
// gives: a spurious decimal invites confidence to substitute for evidence.
type Confidence string

// The confidence gradings.
const (
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)

func (c Confidence) valid() bool {
	switch c {
	case ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
		return true
	}
	return false
}

// AuthorityKind names who stands behind a fact. §4.8 admits exactly two
// authorities and names the rest observations, so the third value exists in
// order to be recorded and refused rather than to be omitted and assumed.
type AuthorityKind string

// The authority kinds.
const (
	// AuthorityOperator is an attributed operator action: an answer, an
	// edit, or an acceptance.
	AuthorityOperator AuthorityKind = "operator"
	// AuthorityTrustedSource is a configured versioned import from a source
	// that declared the predicates and entities it may author.
	AuthorityTrustedSource AuthorityKind = "trusted-source"
	// AuthorityObservation is Git activity, repository inspection,
	// conversation, or Babel analysis. §4.8 states these are observations
	// or proposed revisions rather than authority, so a fact carrying this
	// authority can only ever be `proposed`.
	AuthorityObservation AuthorityKind = "observation"
)

func (a AuthorityKind) valid() bool {
	switch a {
	case AuthorityOperator, AuthorityTrustedSource, AuthorityObservation:
		return true
	}
	return false
}

// authorizes reports whether §4.8 lets this authority make a fact active.
func (a AuthorityKind) authorizes() bool {
	return a == AuthorityOperator || a == AuthorityTrustedSource
}

// Authority is the attribution a fact carries. ID is the operator identity or
// the trusted source's ID; it is an identifier, so §9's allowlist admits it as
// a plaintext column, while the reasoning behind the fact lives in the payload.
type Authority struct {
	Kind AuthorityKind
	ID   string
	At   time.Time
}

func (a Authority) validate() error {
	if !a.Kind.valid() {
		return fmt.Errorf("%w: authority kind %q", ErrInvalidValue, a.Kind)
	}
	if a.ID == "" {
		return fmt.Errorf("%w: authority id is empty", ErrInvalidValue)
	}
	if a.At.IsZero() {
		return fmt.Errorf("%w: authority timestamp is zero", ErrInvalidValue)
	}
	return nil
}

// Context is attributed operator guidance. SPEC §4.7: it is guidance, never
// independent evidence, so it can never satisfy an evidence requirement.
type Context struct {
	ID     string
	Author string // the operator identity that supplied it
	At     time.Time
	Text   string
}

// ContextPayload is the §9 encryption-bound half of a Context. Guidance is
// operator prose about the corpus, so it never travels in the clear.
type ContextPayload struct {
	Text string `json:"text"`
}

// ContextInput records one piece of attributed guidance.
type ContextInput struct {
	Author string
	At     time.Time
	Text   string
}

func (in ContextInput) validate() error {
	if in.Author == "" {
		return fmt.Errorf("%w: context author is empty", ErrInvalidValue)
	}
	if strings.TrimSpace(in.Text) == "" {
		return fmt.Errorf("%w: context text is empty", ErrInvalidValue)
	}
	return nil
}

// checkNoCredential refuses credential-shaped material in ledger prose.
//
// The detector is internal/preflight's, reused rather than reimplemented: a
// second pattern set would drift from the one §6.4 enforces, and the ledger
// would end up forbidding a different set of credentials than the preflight
// reports. Redaction is deterministic, so text it would change is text that
// contains something it recognizes as a secret, and that is the whole test.
//
// It runs over prose only — value text, notes, and reasoning — and never over
// identifiers or digests. Babel generates those itself, they cannot be
// credentials, and preflight's entropy detector legitimately flags a random
// hex identifier, so scanning them would refuse well-formed writes.
//
// A false positive refuses a write, which is the safe direction: the operator
// can rewrite the prose. A false negative is the failure that matters, and it
// is bounded by the same detector §6.4 already relies on.
func checkNoCredential(field, text string) error {
	if text == "" {
		return nil
	}
	if preflight.Redact(text) != text {
		// The offending bytes are deliberately not echoed: an error
		// message carrying the credential would have moved the exposure
		// into the log.
		return fmt.Errorf("%w: %s", ErrCredentialMaterial, field)
	}
	return nil
}

// marshalPayload encodes the §9 encryption-bound half of a record. Every
// payload passes through here, so the sync slice has one place to seal.
func marshalPayload(payload any) ([]byte, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return encoded, nil
}

// newID generates a globally unique client-side identifier, which §9 requires
// of immutable entities and events. The prefix makes a stray ID in an error
// message self-describing without revealing anything about the corpus.
func newID(prefix string) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "_" + hex.EncodeToString(raw[:]), nil
}

// digestKey derives an opaque lookup key from sensitive parts.
//
// This is what lets the ledger find an alias by its value, or recognize a
// repeated question, without holding either in a plaintext column: §9's
// allowlist admits opaque identifiers and locators, and a truncated
// domain-separated digest is one. The domain separator keeps two different
// key spaces from colliding, and the parts are length-prefixed so that
// ("ab","c") and ("a","bc") are different keys.
func digestKey(domain string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(domain))
	h.Write([]byte{0})
	for _, part := range parts {
		fmt.Fprintf(h, "%d:", len(part))
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// sortedUnique normalizes an identifier set so a derived key does not depend
// on the order a caller happened to supply.
func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// nullableID stores an absent reference as SQL NULL so a self-referencing
// foreign key does not have to match an empty string.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// timestampLayout stores an instant as UTC RFC 3339 with a fixed nine-digit
// fraction.
//
// The fixed width is the point, and time.RFC3339Nano is wrong here. It trims
// trailing zeros, so 12:00:00.1Z and 12:00:00.12Z store as ".1Z" and ".12Z",
// and comparing those as text puts the later instant first: the shorter string
// runs out of digits and compares its 'Z' against a digit. Every ordered read
// in this package — a fact's status history, a question's inbox position, a
// resolution history that must show a mistake before its correction — sorts on
// these columns, so the format has to make lexicographic order and
// chronological order the same thing. Nine digits always does; a trimmed
// fraction does so only by luck.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

// formatTime stores a timestamp so that it sorts lexicographically in the same
// order it sorts chronologically. RFC 3339 with nanoseconds parses it back to
// the exact instant it was written from.
func formatTime(t time.Time) string { return t.UTC().Format(timestampLayout) }

// nullableTime stores an open-ended or absent instant as SQL NULL, which is
// the difference between "valid until nothing" and "valid until the zero
// time", and the two must not be confused by a range query.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", value, err)
	}
	return parsed.UTC(), nil
}
