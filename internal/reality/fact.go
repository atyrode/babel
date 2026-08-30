package reality

import (
	"fmt"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/event"
)

// Predicate names what a fact says about its subject.
//
// §4.8 requires lifecycle, ownership, and analysis policy to remain separate
// predicates, and that separation is the reason the vocabulary is closed:
// merging them into one "state" field is exactly how a lifecycle value would
// start implying an expenditure policy. Each predicate carries its own value
// vocabulary and its own freshness rule, declared once in predicateSpecs.
type Predicate string

// The predicates this build knows. The three §4.8 names explicitly are
// deliberately first, and the three volatile ones exist to make freshness
// testable against a predicate that genuinely expires.
const (
	// PredicateLifecycle is where a subject is in its life: operator
	// intent, so it does not expire.
	PredicateLifecycle Predicate = "lifecycle"
	// PredicateOwnership is Babel's relationship to the subject: operator
	// intent, so it does not expire.
	PredicateOwnership Predicate = "ownership"
	// PredicateAnalysisPolicy is what analysis is permitted to do with the
	// subject. It is operator intent and does not expire — and it is still
	// only a fact: a focus rule has to map it to a decision (§4.8).
	PredicateAnalysisPolicy Predicate = "analysis-policy"
	// PredicateServicePlacement names the machine a service runs on. This
	// is the volatile fleet observation §4.8 describes: it is true until
	// something moves, so it carries a TTL.
	PredicateServicePlacement Predicate = "service-placement"
	// PredicateDeploymentState is whether a service is currently deployed,
	// the most volatile thing the ledger holds.
	PredicateDeploymentState Predicate = "deployment-state"
	// PredicateLocalPath is where a repository is checked out on a machine.
	// Paths move, so it expires; the value is sensitive, so it is payload.
	PredicateLocalPath Predicate = "local-path"
)

// The lifecycle values of §4.8.
const (
	LifecycleActive          = "active"
	LifecycleMaintenanceOnly = "maintenance-only"
	LifecycleDormant         = "dormant"
	LifecycleRetired         = "retired"
)

// The ownership values of §4.8.
const (
	OwnershipOwned       = "owned"
	OwnershipContributed = "contributed"
	OwnershipExternal    = "external"
)

// The analysis-policy values of §4.8. They read like Allowance's values and
// are deliberately a different type: a policy fact is what the operator said,
// an allowance is what a versioned focus rule decided, and the whole point of
// §4.8's separation is that the second does not follow from the first without
// a rule.
const (
	PolicyNormal              = "normal"
	PolicyLearnOnly           = "learn-only"
	PolicyNoCodeInvestigation = "no-code-investigation"
	PolicyExcluded            = "excluded"
)

// The deployment-state values.
const (
	DeploymentDeployed   = "deployed"
	DeploymentUndeployed = "undeployed"
	DeploymentUnknown    = "unknown"
)

// ValueKind is how a fact's value is typed. §4.8 requires a "typed value or
// object entity", and the distinction matters for storage: an object entity is
// an identifier and may travel in the clear, while text is a claim and may not.
type ValueKind string

// The value kinds. There is deliberately no boolean kind: a predicate's value
// space is declared in predicateSpecs, and every two-valued fact worth
// recording reads better as a named enum — `deployed|undeployed|unknown` says
// what a bare false would not, and it leaves room for the third answer that
// volatile fleet facts actually need.
const (
	ValueEnum   ValueKind = "enum"
	ValueText   ValueKind = "text"
	ValueEntity ValueKind = "entity"
)

// FactValue is a fact's typed value. Exactly one field is meaningful, chosen
// by Kind; the zero value of the others is not a value.
type FactValue struct {
	Kind ValueKind `json:"kind"`
	// Enum is the value for a closed-vocabulary predicate.
	Enum string `json:"enum,omitempty"`
	// Text is free text, such as a path. It is a claim, so it is sealed
	// with the rest of the payload.
	Text string `json:"text,omitempty"`
	// ObjectID is the object entity of a relationship-shaped fact. It is
	// also stored as a plaintext column so the ledger can be queried by
	// object without decrypting payloads; the two must agree, and a read
	// that finds them disagreeing reports corruption.
	ObjectID string `json:"object_id,omitempty"`
}

// equals compares two values for the contradiction check. It compares only the
// field Kind selects, so a value that differs solely in an unused field is not
// treated as a contradiction.
func (v FactValue) equals(other FactValue) bool {
	if v.Kind != other.Kind {
		return false
	}
	switch v.Kind {
	case ValueEnum:
		return v.Enum == other.Enum
	case ValueText:
		return v.Text == other.Text
	case ValueEntity:
		return v.ObjectID == other.ObjectID
	}
	return false
}

// predicateSpec declares a predicate's value vocabulary and freshness rule in
// one place, so a new predicate cannot be added without stating both.
type predicateSpec struct {
	kind ValueKind
	// values is the closed vocabulary for an enum predicate.
	values []string
	// ttl is how long an observation of this predicate stays fresh. Zero
	// means it never expires, which §4.8 requires of operator intent.
	ttl time.Duration
	// why documents the freshness choice, because a TTL with no stated
	// reason is a number someone will change without knowing what it meant.
	why string
}

// predicateSpecs is the registry. §4.8's freshness rule is predicate-specific,
// and this is where "specific" lives.
var predicateSpecs = map[Predicate]predicateSpec{
	PredicateLifecycle: {
		kind:   ValueEnum,
		values: []string{LifecycleActive, LifecycleMaintenanceOnly, LifecycleDormant, LifecycleRetired},
		why:    "operator intent; §4.8 says intent does not expire automatically",
	},
	PredicateOwnership: {
		kind:   ValueEnum,
		values: []string{OwnershipOwned, OwnershipContributed, OwnershipExternal},
		why:    "operator intent; ownership changes by decision, not by time passing",
	},
	PredicateAnalysisPolicy: {
		kind:   ValueEnum,
		values: []string{PolicyNormal, PolicyLearnOnly, PolicyNoCodeInvestigation, PolicyExcluded},
		why:    "operator intent; an expiring analysis policy would silently widen what analysis may spend",
	},
	PredicateServicePlacement: {
		kind: ValueEntity,
		ttl:  30 * 24 * time.Hour,
		why:  "volatile fleet observation; a service that moved leaves this true-looking and wrong",
	},
	PredicateDeploymentState: {
		kind:   ValueEnum,
		values: []string{DeploymentDeployed, DeploymentUndeployed, DeploymentUnknown},
		ttl:    7 * 24 * time.Hour,
		why:    "the most volatile thing the ledger holds; a week-old deployment state is a guess",
	},
	PredicateLocalPath: {
		kind: ValueText,
		ttl:  90 * 24 * time.Hour,
		why:  "checkouts move and machines are rebuilt, but not weekly",
	},
}

func (p Predicate) valid() bool {
	_, ok := predicateSpecs[p]
	return ok
}

// Predicates lists the known predicates in a stable order. A caller
// registering a trusted source needs to know what it may declare, and a UI
// needs to render the vocabulary, so the registry is readable rather than
// private lore.
func Predicates() []Predicate {
	out := make([]Predicate, 0, len(predicateSpecs))
	for p := range predicateSpecs {
		out = append(out, p)
	}
	sortPredicates(out)
	return out
}

// TTL reports how long an observation of this predicate stays fresh, and the
// reason. A zero duration means the predicate never expires.
func (p Predicate) TTL() (time.Duration, string) {
	spec := predicateSpecs[p]
	return spec.ttl, spec.why
}

// validateValue checks a value against its predicate's declared type and
// vocabulary. An out-of-vocabulary enum is refused rather than stored, because
// a focus rule that matches on a value can only be deterministic if the value
// space is closed.
func (p Predicate) validateValue(v FactValue) error {
	spec, ok := predicateSpecs[p]
	if !ok {
		return fmt.Errorf("%w: predicate %q", ErrInvalidValue, p)
	}
	if v.Kind != spec.kind {
		return fmt.Errorf("%w: predicate %s takes a %s value, got %q", ErrInvalidValue, p, spec.kind, v.Kind)
	}
	switch spec.kind {
	case ValueEnum:
		for _, allowed := range spec.values {
			if v.Enum == allowed {
				return nil
			}
		}
		return fmt.Errorf("%w: predicate %s value %q is outside its vocabulary %v",
			ErrInvalidValue, p, v.Enum, spec.values)
	case ValueText:
		if strings.TrimSpace(v.Text) == "" {
			return fmt.Errorf("%w: predicate %s text value is empty", ErrInvalidValue, p)
		}
		return checkNoCredential(string(p)+" value", v.Text)
	case ValueEntity:
		if v.ObjectID == "" {
			return fmt.Errorf("%w: predicate %s needs an object entity", ErrInvalidValue, p)
		}
	}
	return nil
}

// FactStatus is a fact's state from §4.8. It is derived from an append-only
// status history rather than stored on the fact, which is what lets the fact
// row itself be immutable: superseding a fact appends to the ancestor's
// history and never touches its bytes.
type FactStatus string

// The §4.8 fact states.
const (
	// FactProposed is a revision awaiting authority. Anything derived from
	// observation — Git activity, repository inspection, Babel analysis —
	// can only ever be this.
	FactProposed FactStatus = "proposed"
	// FactActive is an authorized fact in force.
	FactActive FactStatus = "active"
	// FactSuperseded is a fact a later revision replaced, or one a dispute
	// resolution set aside. It is still readable and still byte-identical.
	FactSuperseded FactStatus = "superseded"
	// FactDisputed is a fact contradicted by another. §4.8 requires a
	// contradiction to create a dispute rather than letting one side win.
	FactDisputed FactStatus = "disputed"
	// FactStale is a fact whose freshness expectation has lapsed. §4.8
	// requires expiry to mark rather than delete.
	FactStale FactStatus = "stale"
)

func (s FactStatus) valid() bool {
	switch s {
	case FactProposed, FactActive, FactSuperseded, FactDisputed, FactStale:
		return true
	}
	return false
}

// Fact is one immutable revision. §4.8's field list is the struct's field
// list, and none of it is editable: a change is a superseding revision whose
// ancestor stays byte-identical.
type Fact struct {
	ID            string
	SchemaVersion int
	SubjectID     string
	Predicate     Predicate
	Value         FactValue
	// ValidFrom and ValidUntil are the fact's own valid time, which is not
	// the time it was recorded: a fact can be recorded today about last
	// month. A zero ValidUntil is open-ended.
	ValidFrom  time.Time
	ValidUntil time.Time
	// ObservedAt is when the authority observed what it is asserting; it is
	// what freshness is measured from.
	ObservedAt time.Time
	RecordedAt time.Time
	// ExpiresAt is ObservedAt plus the predicate's TTL, zero when the
	// predicate does not expire. It is derived and stored so expiry is one
	// indexed query instead of a scan that decodes every payload.
	ExpiresAt   time.Time
	Authority   Authority
	Confidence  Confidence
	Sensitivity Sensitivity
	Status      FactStatus
	// Supersedes is the fact this revision replaces, empty for an original.
	// It is unique in the database, so a revision chain cannot fork.
	Supersedes string
	// SourceID and ImportID are set when a trusted source authored the
	// fact, linking it to the declared scope that permitted it.
	SourceID string
	ImportID string
	Payload  FactPayload
}

// FactPayload is the §9 encryption-bound half of a fact: the value's text, the
// provenance locator, and the reasoning. §9 keeps claims, paths, and evidence
// out of PostgreSQL in the clear, and all three are here.
type FactPayload struct {
	// Value carries the typed value. It is in the payload because a
	// predicate's value is a claim about reality, which §9 does not
	// allowlist.
	Value FactValue `json:"value"`
	// Provenance recovers what the fact was derived from. It is required
	// for anything derived from the corpus and optional for a direct
	// operator assertion, where the operator is the provenance.
	Provenance *event.Locator `json:"provenance,omitempty"`
	// Note is the reasoning the authority gave.
	Note string `json:"note,omitempty"`
	// ContextID links attributed operator guidance that accompanied the
	// assertion. It is a link, never support: §4.7 makes Context guidance
	// rather than evidence, and a locator is the only thing that can
	// recover bytes, so guidance structurally cannot stand in for one.
	ContextID string `json:"context_id,omitempty"`
}

func (p FactPayload) validate() error {
	if err := checkNoCredential("fact note", p.Note); err != nil {
		return err
	}
	if p.Provenance != nil {
		if p.Provenance.Path == "" || p.Provenance.Digest == "" {
			return fmt.Errorf("%w: fact provenance needs a path and a digest", ErrInvalidValue)
		}
	}
	return nil
}

// FactInput asserts a fact.
type FactInput struct {
	SubjectID   string
	Predicate   Predicate
	Value       FactValue
	ValidFrom   time.Time
	ValidUntil  time.Time
	ObservedAt  time.Time
	Authority   Authority
	Confidence  Confidence
	Sensitivity Sensitivity
	Provenance  *event.Locator
	Note        string
	ContextID   string
}

func (in FactInput) validate() error {
	if in.SubjectID == "" {
		return fmt.Errorf("%w: fact subject is empty", ErrInvalidValue)
	}
	if err := in.Predicate.validateValue(in.Value); err != nil {
		return err
	}
	if in.ValidFrom.IsZero() {
		return fmt.Errorf("%w: fact valid_from is zero", ErrInvalidValue)
	}
	if !in.ValidUntil.IsZero() && !in.ValidUntil.After(in.ValidFrom) {
		return fmt.Errorf("%w: fact valid_until is not after valid_from", ErrInvalidValue)
	}
	if in.ObservedAt.IsZero() {
		return fmt.Errorf("%w: fact observed_at is zero", ErrInvalidValue)
	}
	if err := in.Authority.validate(); err != nil {
		return err
	}
	if !in.Confidence.valid() {
		return fmt.Errorf("%w: confidence %q", ErrInvalidValue, in.Confidence)
	}
	if !in.Sensitivity.valid() {
		return fmt.Errorf("%w: sensitivity %q", ErrInvalidValue, in.Sensitivity)
	}
	// A trusted source asserts something it observed elsewhere, so it must
	// say where: an import with no locator is an unattributable claim
	// wearing an authority's name. An operator assertion needs none — the
	// attributed operator action is itself the provenance §4.8 accepts.
	if in.Authority.Kind != AuthorityOperator && in.Provenance == nil {
		return fmt.Errorf("%w: %s authority requires a provenance locator",
			ErrInvalidValue, in.Authority.Kind)
	}
	return FactPayload{Value: in.Value, Provenance: in.Provenance, Note: in.Note}.validate()
}

// initialStatus is the status a newly asserted fact may hold.
//
// This is where §4.8's authority rule becomes unavoidable: an observation
// yields `proposed` and nothing else, so Git activity or Babel analysis cannot
// enter the ledger as reality no matter what the caller asks for.
func (in FactInput) initialStatus() FactStatus {
	if in.Authority.Kind.authorizes() {
		return FactActive
	}
	return FactProposed
}

// overlaps reports whether two valid-time intervals intersect. A zero
// ValidUntil is open-ended, which is the common case for operator intent, and
// treating it as the zero instant instead would make every intent fact
// disjoint from every other.
func overlaps(aFrom, aUntil, bFrom, bUntil time.Time) bool {
	if !aUntil.IsZero() && !aUntil.After(bFrom) {
		return false
	}
	if !bUntil.IsZero() && !bUntil.After(aFrom) {
		return false
	}
	return true
}

// expiryOf derives a fact's expiry from its predicate's freshness rule.
func expiryOf(p Predicate, observedAt time.Time) time.Time {
	ttl, _ := p.TTL()
	if ttl == 0 {
		return time.Time{}
	}
	return observedAt.Add(ttl)
}

// FactStatusEvent is one entry in a fact's append-only status history. Storing
// the history rather than a column is what makes "expiry marks rather than
// deletes" and "a contradiction creates a dispute" auditable after the fact.
type FactStatusEvent struct {
	ID         string
	FactID     string
	Sequence   int
	Status     FactStatus
	RecordedAt time.Time
	Payload    StatusPayload
}

// StatusPayload is the §9 encryption-bound half of a status event.
type StatusPayload struct {
	Note string `json:"note,omitempty"`
}

// SupersedeInput replaces a fact with a new revision. §4.8 has no update path:
// the prior revision keeps its bytes and gains a `superseded` status event.
type SupersedeInput struct {
	PriorID string
	Fact    FactInput
}

// DisputeState is a dispute's append-only lifecycle.
type DisputeState string

// The dispute states.
const (
	DisputeOpen     DisputeState = "open"
	DisputeResolved DisputeState = "resolved"
)

// Dispute records that two or more facts contradict one another. §4.8 requires
// a contradiction to create one of these rather than letting the newest write
// win, so the ledger can hold "these two disagree and nobody has decided yet"
// instead of quietly preferring whichever arrived second.
type Dispute struct {
	ID            string
	SchemaVersion int
	SubjectID     string
	Predicate     Predicate
	FactIDs       []string
	CreatedAt     time.Time
	State         DisputeState
	Payload       DisputePayload
}

// DisputePayload is the §9 encryption-bound half of a dispute.
type DisputePayload struct {
	Reason string `json:"reason,omitempty"`
}

// DisputeInput opens a dispute explicitly, for the case where a human or a
// plan judges two facts to conflict in a way the deterministic check cannot
// see — different predicates that cannot both hold, for instance.
type DisputeInput struct {
	FactIDs []string
	Actor   string
	Reason  string
}

// ResolveDisputeInput closes a dispute by naming the fact that survives.
//
// Resolution is an authoritative act, so it carries an operator identity. The
// facts that lose are marked `superseded` by a status event rather than by a
// revision link: the revision chain records "this replaced that", and a
// dispute resolution records "an operator chose between these", which is a
// different claim and must not be indistinguishable from the first.
type ResolveDisputeInput struct {
	DisputeID  string
	KeepFactID string
	Actor      string
	Note       string
}

// TrustedSource is a configured non-operator authority: §4.8's versioned,
// provider-neutral inventory import, which lets dotfiles supply the facts it
// owns.
//
// The declared scope is the whole point. A source that could author any
// predicate about any entity would be a second operator, so registration names
// the predicates it may author and the entities or entity kinds it may author
// about, and an import outside that scope is refused rather than downgraded.
type TrustedSource struct {
	ID            string
	SchemaVersion int
	// Version is the import contract version the source implements, which
	// §4.8 requires to be versioned.
	Version      int
	RegisteredAt time.Time
	Predicates   []Predicate
	// EntityIDs and EntityKinds are alternative ways to bound the entity
	// scope. An empty EntityIDs with a non-empty EntityKinds means "any
	// entity of these kinds", which is what an inventory source needs
	// because it discovers machines Babel has not seen yet. Both empty is
	// refused: an unbounded entity scope is not a scope.
	EntityIDs   []string
	EntityKinds []EntityKind
	Payload     TrustedSourcePayload
}

// TrustedSourcePayload is the §9 encryption-bound half of a source: its
// description and the operator's reason for trusting it.
type TrustedSourcePayload struct {
	Description string `json:"description"`
	Note        string `json:"note,omitempty"`
}

// TrustedSourceInput registers a trusted source.
type TrustedSourceInput struct {
	// ID is the source's stable configured identity, supplied rather than
	// generated: the same dotfiles inventory must be the same source across
	// machines and reinstalls, so its identity cannot be a fresh random
	// value per registration.
	ID          string
	Version     int
	Predicates  []Predicate
	EntityIDs   []string
	EntityKinds []EntityKind
	Payload     TrustedSourcePayload
}

func (in TrustedSourceInput) validate() error {
	if in.ID == "" {
		return fmt.Errorf("%w: trusted source id is empty", ErrInvalidValue)
	}
	if in.Version <= 0 {
		return fmt.Errorf("%w: trusted source version must be positive", ErrInvalidValue)
	}
	if len(in.Predicates) == 0 {
		return fmt.Errorf("%w: trusted source declares no predicates", ErrInvalidValue)
	}
	for _, p := range in.Predicates {
		if !p.valid() {
			return fmt.Errorf("%w: predicate %q", ErrInvalidValue, p)
		}
	}
	if len(in.EntityIDs) == 0 && len(in.EntityKinds) == 0 {
		return fmt.Errorf("%w: trusted source declares no entity scope", ErrInvalidValue)
	}
	for _, k := range in.EntityKinds {
		if !k.valid() {
			return fmt.Errorf("%w: entity kind %q", ErrInvalidValue, k)
		}
	}
	if strings.TrimSpace(in.Payload.Description) == "" {
		return fmt.Errorf("%w: trusted source description is empty", ErrInvalidValue)
	}
	if err := checkNoCredential("trusted source description", in.Payload.Description); err != nil {
		return err
	}
	return checkNoCredential("trusted source note", in.Payload.Note)
}

// permitsPredicate reports whether the source declared this predicate.
func (s TrustedSource) permitsPredicate(p Predicate) bool {
	for _, declared := range s.Predicates {
		if declared == p {
			return true
		}
	}
	return false
}

// permitsEntity reports whether the source declared this entity, either by ID
// or by kind.
func (s TrustedSource) permitsEntity(id string, kind EntityKind) bool {
	for _, declared := range s.EntityIDs {
		if declared == id {
			return true
		}
	}
	for _, declared := range s.EntityKinds {
		if declared == kind {
			return true
		}
	}
	return false
}

// ImportInput is one versioned batch from a trusted source.
//
// A batch is all-or-nothing. §4.8 says an import outside the declared scope is
// refused, and refusing it fact by fact would leave the source's inventory
// half-applied, which is worse than rejecting the batch: the operator would
// have to diff the ledger against the source to learn what landed.
type ImportInput struct {
	SourceID string
	// BatchKey is the source's own idempotency key for this batch, which §9
	// requires of immutable event writes. Replaying a batch is refused as a
	// duplicate rather than duplicating every fact in it.
	BatchKey string
	Facts    []FactInput
}
