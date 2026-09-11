package reality

// The operator's subject-naming surface: minting the stable subject §4.8's
// facts are about, and the typed names it answers to.
//
// It exists as a type of its own for FocusPolicy's reason, one step further.
// FocusPolicy holds the store so that a browser handler cannot assert a
// lifecycle fact; this holds the store so that the handler which creates a
// subject cannot assert anything at all. Its method set creates an identity
// and attaches names to it, and there is no method here that writes a fact,
// merges two identities, installs a rule set, or resolves a dispute — which is
// the property #4.8's own separation depends on: naming a thing and believing
// something about it are different acts with different authority, and the
// surface an operator names a subject from must not be able to perform the
// second one on his behalf.
//
// Three rules hold here rather than in the caller.
//
// What is created is exactly what `babel reality entity create` creates. The
// entity row, its first membership entry, the fleet's claim on it, and each
// typed alias are the store's own writes in the store's own order — this type
// adds no column, no default kind and no implicit alias, so a subject named
// from a browser and a subject named from the CLI are the same record. A
// second set of creation rules is the failure mode this avoids, because the
// two would then disagree about what a subject is.
//
// Nothing believed is created with it. §4.8 makes an entity the thing facts
// are about rather than a fact, and an empty ledger has to be writable without
// anything being asserted into it: a new subject leaves Babel believing
// nothing about it, which is why every count on its record page reads zero
// until analysis or an operator says something.
//
// Nothing is deleted. A mistaken name is retracted and a mistaken identity is
// merged, both of which are appends that stay reversible, and neither is
// reachable from here: this surface only ever adds.

import (
	"context"
	"fmt"
)

// SubjectNaming is the ledger's subject-naming surface. It holds the store
// rather than embedding it, so its method set is exactly the methods below.
type SubjectNaming struct {
	store *Store
}

// Naming returns the store's subject-naming surface. It is a view, not a
// second store: every write below is the store's own, with the store's own
// clock and the store's own validation.
func (s *Store) Naming() *SubjectNaming { return &SubjectNaming{store: s} }

// Kinds is the entity-kind vocabulary a caller has to choose from, in
// EntityKinds' stable order.
//
// It is served rather than restated because the vocabulary is closed: §4.8's
// kinds are what the ledger will accept, a typo is a refused write, and a
// picker holding its own copy of the list would offer a ninth kind that
// nothing can store — or miss the one a build added.
func (p *SubjectNaming) Kinds() []EntityKind { return EntityKinds() }

// AliasKinds is the typed-name vocabulary, for Kinds' reason: an alias kind is
// how §4.8 keeps a hostname from being compared with a chat term, and a caller
// that guessed a kind name would have its name refused.
func (p *SubjectNaming) AliasKinds() []AliasKind { return AliasKinds() }

// Resolve names the canonical entity an untyped word already refers to, and is
// how a caller learns that a name is taken before creating a second subject
// for the thing that has it.
//
// It is ResolveSubject, the same resolution a run and the focus surface
// perform, which is what makes the answer worth acting on: a word that
// resolves here is a word that would have reached the existing subject
// anyway.
func (p *SubjectNaming) Resolve(ctx context.Context, value string) (string, error) {
	return p.store.ResolveSubject(ctx, value)
}

// NewSubject is one subject to name: what it is, what to call it, and the
// typed names it should answer to.
//
// The aliases travel with the creation rather than after it because that is
// what makes the subject findable by the word its operator actually used. An
// entity whose only name is its display name is reachable by identifier and by
// nothing else, and §4.8's resolution reads aliases.
type NewSubject struct {
	Kind        EntityKind
	DisplayName string
	Notes       string
	// Aliases are attached in order. EntityID is filled in by Create — the
	// entity does not exist when the caller assembles these — and anything
	// a caller put there is replaced rather than honoured, so an alias
	// cannot be smuggled onto a different subject.
	Aliases []AliasInput
}

// Create mints one subject and attaches its typed names.
//
// The order is the CLI's and so is the failure mode: an alias that is refused
// after the entity exists is reported against the created entity rather than
// rolled back into it. The identity is real by then — it has a row, a
// membership entry and a publication — and taking it back is not something an
// append-only ledger can do; naming it again would mint a second subject for
// one thing, which is the confusion §4.8's merge history exists to undo. So
// the entity and the aliases that did attach are returned with the error, and
// the caller reports what exists.
//
// Nothing here checks whether the name is taken. That is Resolve's answer and
// it belongs to the caller, because the two callers want different things from
// it: an operator naming a subject from a page that just told him the word
// resolves to nothing wants the attempt refused, while a caller reconciling an
// import may legitimately be attaching a second name to a thing it already
// found. A check buried in here would make the first case silent and the
// second impossible.
func (p *SubjectNaming) Create(ctx context.Context, in NewSubject) (Entity, []Alias, error) {
	entity, err := p.store.CreateEntity(ctx, EntityInput{
		Kind:    in.Kind,
		Payload: EntityPayload{DisplayName: in.DisplayName, Notes: in.Notes},
	})
	if err != nil {
		return Entity{}, nil, err
	}
	attached := make([]Alias, 0, len(in.Aliases))
	for _, alias := range in.Aliases {
		alias.EntityID = entity.ID
		added, err := p.store.AddAlias(ctx, alias)
		if err != nil {
			return entity, attached, fmt.Errorf("entity %s created; add alias %s: %w",
				entity.ID, alias.Kind, err)
		}
		attached = append(attached, added)
	}
	return entity, attached, nil
}
