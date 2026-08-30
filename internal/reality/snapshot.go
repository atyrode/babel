package reality

import "time"

// SnapshotInput captures the context a focus decision was made against.
type SnapshotInput struct {
	// HypothesisID is the candidate the snapshot justifies. §4.8 has
	// discovery persist hypotheses before context-based focus, so the
	// hypothesis exists first and the snapshot is attached to it.
	HypothesisID string
	// EntityIDs are the entities emergence resolved to. They are resolved
	// again here through the merge history, and both the supplied and the
	// canonical identity are recorded: a snapshot that kept only the
	// canonical one would lose which name the hypothesis actually mentioned.
	EntityIDs []string
	// RuleSetVersion is the policy the decision used.
	RuleSetVersion int
	// AsOf is the instant the ledger was read at, defaulting to now.
	AsOf time.Time
	Note string
}

// SnapshotEntity is one entity's contribution to a snapshot.
type SnapshotEntity struct {
	EntityID    string
	CanonicalID string
	Allowance   Allowance
}

// Snapshot is the immutable as-of context §4.8 attaches after entity
// resolution.
//
// Its purpose is narrow and important: when deterministic policy defers
// cloning, testing, research, or a repository-specific proposal, the deferral
// has to record the context that caused it. So the snapshot holds the resolved
// entities, the exact facts that were read, the policy version, the instant,
// and the resulting decisions — enough to re-derive the decision later and see
// whether the ledger or the policy changed. The hypothesis itself is never
// deleted, here or in the frontier.
type Snapshot struct {
	ID             string
	SchemaVersion  int
	HypothesisID   string
	RuleSetVersion int
	AsOf           time.Time
	CreatedAt      time.Time
	// Allowance is the combined decision: the most restrictive of the
	// entities'. The most permissive would let one unrelated entity unlock
	// work on an excluded one.
	Allowance Allowance
	Entities  []SnapshotEntity
	FactIDs   []string
	Payload   SnapshotPayload
}

// SnapshotPayload is the §9 encryption-bound half of a snapshot: the decisions
// carry rule reasons and fact values.
type SnapshotPayload struct {
	Note      string          `json:"note,omitempty"`
	Decisions []FocusDecision `json:"decisions"`
}
