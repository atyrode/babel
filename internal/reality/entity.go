package reality

import (
	"fmt"
	"strings"
	"time"
)

// EntityKind names what a stable subject is. §4.8 lists projects,
// repositories, machines, services, providers, environments, and
// organizations, and then admits "other operator-defined subjects", which is
// what EntityKindSubject is: an operator-defined subject is a first-class
// entity rather than a shapeless escape hatch, and giving it a name keeps the
// vocabulary closed so a typo is a refused write instead of a new kind.
type EntityKind string

// The entity kinds.
const (
	EntityProject      EntityKind = "project"
	EntityRepository   EntityKind = "repository"
	EntityMachine      EntityKind = "machine"
	EntityService      EntityKind = "service"
	EntityProvider     EntityKind = "provider"
	EntityEnvironment  EntityKind = "environment"
	EntityOrganization EntityKind = "organization"
	EntitySubject      EntityKind = "subject"
)

func (k EntityKind) valid() bool {
	switch k {
	case EntityProject, EntityRepository, EntityMachine, EntityService,
		EntityProvider, EntityEnvironment, EntityOrganization, EntitySubject:
		return true
	}
	return false
}

// EntityRole is what the resolution history currently says an entity is.
//
// It is derived from an append-only membership history rather than stored as a
// mutable column, which is what makes a mistaken resolution reversible: the
// merge that folded an identity away is still there, the event that reversed
// it is beside it, and the role is whichever the newest event says.
type EntityRole string

// The entity roles.
const (
	// RoleSelf means the entity is its own canonical identity.
	RoleSelf EntityRole = "self"
	// RoleMerged means a resolution folded this identity into another. The
	// row is still addressable — §4.8 forbids losing identity — and
	// Resolve follows it to the canonical entity.
	RoleMerged EntityRole = "merged"
	// RoleSplit means this identity was found to cover several distinct
	// subjects. It stays canonical for the facts already attached to it,
	// and its parts are separate entities.
	RoleSplit EntityRole = "split"
)

func (r EntityRole) valid() bool {
	switch r {
	case RoleSelf, RoleMerged, RoleSplit:
		return true
	}
	return false
}

// Entity is one stable subject. The row is immutable: renames, path changes,
// and chat terminology are aliases, not edits, which is exactly why §4.8
// promises they do not lose identity.
type Entity struct {
	ID            string
	Kind          EntityKind
	SchemaVersion int
	CreatedAt     time.Time
	// Role and CanonicalID come from the newest membership event.
	// CanonicalID is the entity itself unless a merge folded it away.
	Role        EntityRole
	CanonicalID string
	Payload     EntityPayload
}

// EntityPayload is the §9 encryption-bound half of an entity: a display name
// is a title, and §9 keeps titles out of PostgreSQL in the clear.
type EntityPayload struct {
	DisplayName string `json:"display_name"`
	Notes       string `json:"notes,omitempty"`
}

func (p EntityPayload) validate() error {
	if strings.TrimSpace(p.DisplayName) == "" {
		return fmt.Errorf("%w: entity display name is empty", ErrInvalidValue)
	}
	if err := checkNoCredential("entity display name", p.DisplayName); err != nil {
		return err
	}
	return checkNoCredential("entity notes", p.Notes)
}

// EntityInput creates one entity.
type EntityInput struct {
	Kind    EntityKind
	Payload EntityPayload
}

// AliasKind types an alias so that a chat nickname and a filesystem path are
// not compared with one another. §4.8 requires typed aliases precisely because
// a rename, a path change, and a conversational term are different kinds of
// evidence that two names mean one thing.
type AliasKind string

// The alias kinds.
const (
	AliasName       AliasKind = "name"
	AliasPath       AliasKind = "path"
	AliasRepository AliasKind = "repository"
	AliasHostname   AliasKind = "hostname"
	AliasChatTerm   AliasKind = "chat-term"
	AliasURL        AliasKind = "url"
	AliasIdentifier AliasKind = "identifier"
)

func (k AliasKind) valid() bool {
	switch k {
	case AliasName, AliasPath, AliasRepository, AliasHostname,
		AliasChatTerm, AliasURL, AliasIdentifier:
		return true
	}
	return false
}

// AttachmentState is the append-only lifecycle of an alias or a relationship.
// Nothing is deleted here either: a wrong alias is retracted, and the
// retraction is an event beside the assertion, so the mistake and its
// correction are both inspectable.
type AttachmentState string

// The attachment states.
const (
	StateAsserted  AttachmentState = "asserted"
	StateRetracted AttachmentState = "retracted"
)

func (s AttachmentState) valid() bool {
	switch s {
	case StateAsserted, StateRetracted:
		return true
	}
	return false
}

// Alias is one typed name for an entity.
//
// Key is an opaque digest of the normalized value, not the value: it is what
// makes alias lookup an exact-match query without putting a repository path or
// a conversational term into a plaintext column §9 would have to allowlist.
// The value itself lives in the payload.
type Alias struct {
	ID            string
	EntityID      string
	Kind          AliasKind
	Key           string
	SchemaVersion int
	CreatedAt     time.Time
	State         AttachmentState
	Payload       AliasPayload
}

// AliasPayload is the §9 encryption-bound half of an alias.
type AliasPayload struct {
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}

func (p AliasPayload) validate() error {
	if strings.TrimSpace(p.Value) == "" {
		return fmt.Errorf("%w: alias value is empty", ErrInvalidValue)
	}
	if err := checkNoCredential("alias value", p.Value); err != nil {
		return err
	}
	return checkNoCredential("alias note", p.Note)
}

// AliasInput attaches a typed alias to an entity.
type AliasInput struct {
	EntityID string
	Kind     AliasKind
	Payload  AliasPayload
}

// normalizeAlias folds the incidental differences between two spellings of one
// name: surrounding whitespace and letter case. It deliberately does nothing
// else — collapsing separators or trimming a path's trailing slash would make
// two genuinely different paths compare equal, and a wrong merge is the
// failure §4.8 is most concerned with.
func normalizeAlias(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// aliasKey derives an alias's lookup key.
func aliasKey(kind AliasKind, value string) string {
	return digestKey("babel/reality/alias", string(kind), normalizeAlias(value))
}

// RelationKind types a relationship between two entities. The set covers what
// §4.8 names — project/service/host relationships and service placement — and
// stays closed so a relationship graph query cannot be defeated by a synonym.
type RelationKind string

// The relationship kinds.
const (
	RelationContains    RelationKind = "contains"
	RelationPartOf      RelationKind = "part-of"
	RelationDependsOn   RelationKind = "depends-on"
	RelationDeployedOn  RelationKind = "deployed-on"
	RelationHostedBy    RelationKind = "hosted-by"
	RelationOwnedBy     RelationKind = "owned-by"
	RelationSuccessorOf RelationKind = "successor-of"
	RelationRelatedTo   RelationKind = "related-to"
)

func (k RelationKind) valid() bool {
	switch k {
	case RelationContains, RelationPartOf, RelationDependsOn, RelationDeployedOn,
		RelationHostedBy, RelationOwnedBy, RelationSuccessorOf, RelationRelatedTo:
		return true
	}
	return false
}

// Relationship is one typed edge between entities, with the same append-only
// lifecycle as an alias.
type Relationship struct {
	ID            string
	FromID        string
	ToID          string
	Kind          RelationKind
	SchemaVersion int
	CreatedAt     time.Time
	State         AttachmentState
	Payload       RelationshipPayload
}

// RelationshipPayload is the §9 encryption-bound half of an edge: why the edge
// was asserted is investigator or operator prose.
type RelationshipPayload struct {
	Note string `json:"note,omitempty"`
}

// RelationshipInput asserts a typed edge.
type RelationshipInput struct {
	FromID  string
	ToID    string
	Kind    RelationKind
	Payload RelationshipPayload
}

// ResolutionKind names an entity-identity operation.
type ResolutionKind string

// The resolution kinds.
const (
	// ResolutionMerge folds one or more source identities into a target.
	ResolutionMerge ResolutionKind = "merge"
	// ResolutionSplit records that one identity covered several subjects
	// and names the new part entities.
	ResolutionSplit ResolutionKind = "split"
	// ResolutionUndo reverses exactly one earlier merge or split. It is a
	// resolution in its own right rather than a flag on the one it
	// reverses, because §4.8's history is append-only and the mistake has
	// to remain visible beside its correction.
	ResolutionUndo ResolutionKind = "undo"
)

func (k ResolutionKind) valid() bool {
	switch k {
	case ResolutionMerge, ResolutionSplit, ResolutionUndo:
		return true
	}
	return false
}

// Resolution is one immutable entry in the append-only merge/split history.
//
// Reversibility is stored, not reconstructed. A merge records its sources and
// its target; reversing it appends an undo resolution that names the merge and
// writes each source's membership back to itself. Nothing is rewritten, so
// after a reversal the merge, the undo, and both identities are all still
// there — which is what §4.8 means by a mistaken resolution remaining
// reversible.
type Resolution struct {
	ID   string
	Kind ResolutionKind
	// ReversesID is the resolution this one undoes, empty unless Kind is
	// ResolutionUndo. It is unique in the database, so a resolution cannot
	// be undone twice and two concurrent undos cannot both win.
	ReversesID string
	// Actor is the attributed operator identity that performed it. Entity
	// resolution is an authoritative act, so it is never anonymous.
	Actor      string
	RecordedAt time.Time
	// SourceIDs are the identities the resolution consumed: a merge's
	// sources, a split's parent, or an undo's subject.
	SourceIDs []string
	// ResultIDs are the identities it produced or preserved: a merge's
	// target, or a split's new parts.
	ResultIDs []string
	Payload   ResolutionPayload
}

// ResolutionPayload is the §9 encryption-bound half of a resolution: the
// reason two identities were judged the same is operator prose.
type ResolutionPayload struct {
	Reason string `json:"reason,omitempty"`
}

// MergeInput folds source identities into a target.
type MergeInput struct {
	SourceIDs []string
	TargetID  string
	Actor     string
	Reason    string
}

// SplitInput records that a parent identity covered several subjects. The
// parts are created by the same transaction, so a split never leaves a
// dangling reference to an entity a caller was supposed to create first.
type SplitInput struct {
	ParentID string
	Parts    []EntityInput
	Actor    string
	Reason   string
}

// UndoInput reverses one earlier resolution.
type UndoInput struct {
	ResolutionID string
	Actor        string
	Reason       string
}
