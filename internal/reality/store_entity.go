package reality

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CreateEntity persists one stable subject.
//
// The row is immutable from here. A rename is an alias, a path change is an
// alias, a chat nickname is an alias, and a mistaken identity is a merge that
// can be undone — which together are what §4.8 means by an identity that
// renames and moves without being lost.
func (s *Store) CreateEntity(ctx context.Context, in EntityInput) (Entity, error) {
	var (
		record Entity
		pub    publication
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		created, err := s.createEntity(ctx, tx, in)
		if err != nil {
			return err
		}
		record = created
		wire, err := stagedEntity(created)
		if err != nil {
			return err
		}
		// Staged inside the transaction that makes the entity durable, so
		// "this subject exists" and "the fleet is owed this subject" are one
		// event. A staging failure takes the durable write down with it.
		pub, err = s.stage(ctx, tx, wire)
		return err
	})
	if err != nil {
		return Entity{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Entity{}, err
	}
	return record, nil
}

// createEntity inserts an entity and opens its membership history inside a
// caller's transaction, so a split can create its parts atomically with the
// resolution that names them.
func (s *Store) createEntity(ctx context.Context, tx *sql.Tx, in EntityInput) (Entity, error) {
	if !in.Kind.valid() {
		return Entity{}, fmt.Errorf("%w: entity kind %q", ErrInvalidValue, in.Kind)
	}
	if err := in.Payload.validate(); err != nil {
		return Entity{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Entity{}, err
	}
	id, err := newID("ent")
	if err != nil {
		return Entity{}, err
	}
	created := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_entity(
		id, kind, schema_version, created_at, payload_json) VALUES(?, ?, ?, ?, ?)`,
		id, string(in.Kind), RecordSchema, formatTime(created), payload); err != nil {
		return Entity{}, fmt.Errorf("reality: insert entity: %w", err)
	}
	// The first membership entry says the identity speaks for itself, and it
	// is the one entry that never publishes: a reading host assumes exactly
	// that of an entity record, and every later move travels under the
	// resolution that made it.
	if _, err := s.appendMembership(ctx, tx, id, RoleSelf, id, ""); err != nil {
		return Entity{}, err
	}
	return Entity{
		ID:            id,
		Kind:          in.Kind,
		SchemaVersion: RecordSchema,
		CreatedAt:     created,
		Role:          RoleSelf,
		CanonicalID:   id,
		Payload:       in.Payload,
	}, nil
}

// membership is one entry of an identity's append-only resolution history: what
// the ledger now says the identity is, and which resolution said so.
//
// It exists so appendMembership can hand back the row it just wrote. The
// recorded instant is chosen inside, and the wire form needs it, so without
// this the caller would have to either read the row back inside the transaction
// or take five positional values from a function that already has them.
type membership struct {
	entityID     string
	role         EntityRole
	canonicalID  string
	resolutionID string
	recordedAt   time.Time
}

// appendMembership records what the resolution history now says an entity is.
// Every identity change in this package is one of these appends, which is why
// none of them rewrites anything.
func (s *Store) appendMembership(ctx context.Context, tx *sql.Tx, entityID string,
	role EntityRole, canonicalID, resolutionID string) (membership, error) {
	if !role.valid() {
		return membership{}, fmt.Errorf("%w: entity role %q", ErrInvalidValue, role)
	}
	seq, err := nextSeq(ctx, tx, "reality_entity_membership", "entity_id", entityID)
	if err != nil {
		return membership{}, err
	}
	entry := membership{
		entityID:     entityID,
		role:         role,
		canonicalID:  canonicalID,
		resolutionID: resolutionID,
		recordedAt:   s.now(),
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_entity_membership(
		entity_id, seq, role, canonical_id, resolution_id, recorded_at) VALUES(?, ?, ?, ?, ?, ?)`,
		entityID, seq, string(role), canonicalID, nullableID(resolutionID),
		formatTime(entry.recordedAt)); err != nil {
		return membership{}, fmt.Errorf("reality: append entity membership: %w", err)
	}
	return entry, nil
}

const entitySelect = `SELECT e.id, e.kind, e.schema_version, e.created_at, e.payload_json,
	m.role, m.canonical_id
	FROM reality_entity e
	JOIN reality_entity_membership m ON m.entity_id = e.id
	AND m.seq = (SELECT MAX(seq) FROM reality_entity_membership x WHERE x.entity_id = e.id)`

// Entity reads one subject with what the resolution history currently says
// about it. A merged-away identity still reads: §4.8 forbids losing it, and a
// reversal has to have something to address.
func (s *Store) Entity(ctx context.Context, id string) (Entity, error) {
	return scanEntity(s.db.QueryRowContext(ctx, entitySelect+` WHERE e.id = ?`, id), id)
}

func scanEntity(row *sql.Row, id string) (Entity, error) {
	var (
		record  Entity
		kind    string
		created string
		payload []byte
		role    string
	)
	err := row.Scan(&record.ID, &kind, &record.SchemaVersion, &created, &payload, &role, &record.CanonicalID)
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, fmt.Errorf("%w: entity %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Entity{}, fmt.Errorf("reality: read entity %q: %w", id, err)
	}
	record.Kind = EntityKind(kind)
	record.Role = EntityRole(role)
	if record.CreatedAt, err = parseTime(created); err != nil {
		return Entity{}, fmt.Errorf("reality: entity %s: %w", record.ID, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Entity{}, fmt.Errorf("reality: decode entity %s payload: %w", record.ID, err)
	}
	return record, nil
}

// maxResolutionDepth bounds canonical-pointer following. A cycle cannot be
// created by MergeEntities, which refuses a merge into a non-self identity, but
// the ledger is the one file whose corruption must not become an infinite loop.
const maxResolutionDepth = 32

// Resolve follows the merge history to the identity that currently speaks for
// this one. An identity that was never merged resolves to itself, and an
// identity whose merge was undone resolves to itself again.
func (s *Store) Resolve(ctx context.Context, id string) (string, error) {
	return resolve(ctx, s.db, id)
}

func resolve(ctx context.Context, q querier, id string) (string, error) {
	current := id
	for range maxResolutionDepth {
		role, canonical, err := currentMembership(ctx, q, current)
		if err != nil {
			return "", err
		}
		if role != RoleMerged || canonical == current {
			return current, nil
		}
		current = canonical
	}
	return "", fmt.Errorf("reality: entity %q resolution exceeds %d hops, which means the membership history is cyclic",
		id, maxResolutionDepth)
}

func currentMembership(ctx context.Context, q querier, id string) (EntityRole, string, error) {
	var role, canonical string
	err := q.QueryRowContext(ctx, `SELECT role, canonical_id FROM reality_entity_membership
		WHERE entity_id = ? ORDER BY seq DESC LIMIT 1`, id).Scan(&role, &canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", fmt.Errorf("%w: entity %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return "", "", fmt.Errorf("reality: read entity %q membership: %w", id, err)
	}
	return EntityRole(role), canonical, nil
}

func entityKindOf(ctx context.Context, q querier, id string) (EntityKind, error) {
	var kind string
	err := q.QueryRowContext(ctx, `SELECT kind FROM reality_entity WHERE id = ?`, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: entity %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return "", fmt.Errorf("reality: read entity %q kind: %w", id, err)
	}
	return EntityKind(kind), nil
}

// AddAlias attaches a typed name to an entity. Aliases are how §4.8 keeps a
// rename from losing identity, so adding one never touches the entity row.
func (s *Store) AddAlias(ctx context.Context, in AliasInput) (Alias, error) {
	if !in.Kind.valid() {
		return Alias{}, fmt.Errorf("%w: alias kind %q", ErrInvalidValue, in.Kind)
	}
	if err := in.Payload.validate(); err != nil {
		return Alias{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Alias{}, err
	}
	id, err := newID("als")
	if err != nil {
		return Alias{}, err
	}
	record := Alias{
		ID:            id,
		EntityID:      in.EntityID,
		Kind:          in.Kind,
		Key:           aliasKey(in.Kind, in.Payload.Value),
		SchemaVersion: RecordSchema,
		CreatedAt:     s.now(),
		State:         StateAsserted,
		Payload:       in.Payload,
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRow(ctx, tx, "reality_entity", "id", in.EntityID); err != nil {
			return fmt.Errorf("reality: alias subject: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_entity_alias(
			id, entity_id, alias_kind, value_key, schema_version, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			record.ID, record.EntityID, string(record.Kind), record.Key, RecordSchema,
			formatTime(record.CreatedAt), payload); err != nil {
			return fmt.Errorf("reality: insert alias: %w", err)
		}
		return s.appendAttachmentState(ctx, tx, "reality_alias_event", "alias_id", record.ID, StateAsserted, "")
	})
	if err != nil {
		return Alias{}, err
	}
	return record, nil
}

// RetractAlias records that an alias was wrong. The alias row stays, because
// the mistake is part of how the identity was understood and §4.8's history is
// append-only; what changes is the newest state event.
func (s *Store) RetractAlias(ctx context.Context, aliasID, note string) error {
	if err := checkNoCredential("alias retraction note", note); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRow(ctx, tx, "reality_entity_alias", "id", aliasID); err != nil {
			return err
		}
		return s.appendAttachmentState(ctx, tx, "reality_alias_event", "alias_id", aliasID, StateRetracted, note)
	})
}

func (s *Store) appendAttachmentState(ctx context.Context, tx *sql.Tx,
	table, column, id string, state AttachmentState, note string) error {
	if !state.valid() {
		return fmt.Errorf("%w: attachment state %q", ErrInvalidValue, state)
	}
	seq, err := nextSeq(ctx, tx, table, column, id)
	if err != nil {
		return err
	}
	eventID, err := newID("ath")
	if err != nil {
		return err
	}
	payload, err := marshalPayload(StatusPayload{Note: note})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO `+table+`(
		id, `+column+`, seq, state, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
		eventID, id, seq, string(state), formatTime(s.now()), payload); err != nil {
		return fmt.Errorf("reality: append %s: %w", table, err)
	}
	return nil
}

// ResolveAlias finds the canonical entity a typed name refers to.
//
// Ambiguity is reported rather than guessed. §4.8 makes resolving an alias a
// Question precisely because two entities can answer to one term, and picking
// one would be the mistaken resolution the whole merge history exists to
// reverse. Retracted aliases do not participate.
func (s *Store) ResolveAlias(ctx context.Context, kind AliasKind, value string) (string, error) {
	if !kind.valid() {
		return "", fmt.Errorf("%w: alias kind %q", ErrInvalidValue, kind)
	}
	ids, err := queryStrings(ctx, s.db, `SELECT a.entity_id FROM reality_entity_alias a
		WHERE a.alias_kind = ? AND a.value_key = ?
		AND (SELECT e.state FROM reality_alias_event e WHERE e.alias_id = a.id
			ORDER BY e.seq DESC LIMIT 1) = ?
		ORDER BY a.entity_id`, string(kind), aliasKey(kind, value), string(StateAsserted))
	if err != nil {
		return "", err
	}
	canonical := make(map[string]struct{}, len(ids))
	var first string
	for _, id := range ids {
		resolved, err := resolve(ctx, s.db, id)
		if err != nil {
			return "", err
		}
		if _, seen := canonical[resolved]; !seen {
			canonical[resolved] = struct{}{}
			if first == "" {
				first = resolved
			}
		}
	}
	switch len(canonical) {
	case 0:
		// The value is deliberately absent from the error: an alias value
		// is a path or a conversational term, and §9 keeps those out of
		// logs.
		return "", fmt.Errorf("%w: %s alias", ErrUnknownRecord, kind)
	case 1:
		return first, nil
	}
	return "", fmt.Errorf("%w: %s alias resolves to %d entities", ErrAmbiguousAlias, kind, len(canonical))
}

// Aliases reads an entity's aliases with their current states.
func (s *Store) Aliases(ctx context.Context, entityID string) ([]Alias, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id, a.entity_id, a.alias_kind, a.value_key,
		a.schema_version, a.created_at, a.payload_json,
		(SELECT e.state FROM reality_alias_event e WHERE e.alias_id = a.id ORDER BY e.seq DESC LIMIT 1)
		FROM reality_entity_alias a WHERE a.entity_id = ? ORDER BY a.created_at, a.id`, entityID)
	if err != nil {
		return nil, fmt.Errorf("reality: read aliases: %w", err)
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var (
			record  Alias
			kind    string
			created string
			payload []byte
			state   string
		)
		if err := rows.Scan(&record.ID, &record.EntityID, &kind, &record.Key,
			&record.SchemaVersion, &created, &payload, &state); err != nil {
			return nil, fmt.Errorf("reality: read aliases: %w", err)
		}
		record.Kind = AliasKind(kind)
		record.State = AttachmentState(state)
		if record.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("reality: alias %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("reality: decode alias %s payload: %w", record.ID, err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// AddRelationship asserts a typed edge between two entities.
func (s *Store) AddRelationship(ctx context.Context, in RelationshipInput) (Relationship, error) {
	if !in.Kind.valid() {
		return Relationship{}, fmt.Errorf("%w: relationship kind %q", ErrInvalidValue, in.Kind)
	}
	if in.FromID == in.ToID {
		return Relationship{}, fmt.Errorf("%w: an entity cannot relate to itself", ErrInvalidValue)
	}
	if err := checkNoCredential("relationship note", in.Payload.Note); err != nil {
		return Relationship{}, err
	}
	payload, err := marshalPayload(in.Payload)
	if err != nil {
		return Relationship{}, err
	}
	id, err := newID("rel")
	if err != nil {
		return Relationship{}, err
	}
	record := Relationship{
		ID:            id,
		FromID:        in.FromID,
		ToID:          in.ToID,
		Kind:          in.Kind,
		SchemaVersion: RecordSchema,
		CreatedAt:     s.now(),
		State:         StateAsserted,
		Payload:       in.Payload,
	}
	err = s.transact(ctx, func(tx *sql.Tx) error {
		for _, side := range []string{in.FromID, in.ToID} {
			if err := requireRow(ctx, tx, "reality_entity", "id", side); err != nil {
				return fmt.Errorf("reality: relationship endpoint: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_relationship(
			id, from_id, to_id, relation_kind, schema_version, created_at, payload_json)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			record.ID, record.FromID, record.ToID, string(record.Kind), RecordSchema,
			formatTime(record.CreatedAt), payload); err != nil {
			return fmt.Errorf("reality: insert relationship: %w", err)
		}
		return s.appendAttachmentState(ctx, tx, "reality_relationship_event", "relationship_id",
			record.ID, StateAsserted, "")
	})
	if err != nil {
		return Relationship{}, err
	}
	return record, nil
}

// RetractRelationship records that an edge was wrong, on the same append-only
// terms as an alias.
func (s *Store) RetractRelationship(ctx context.Context, relationshipID, note string) error {
	if err := checkNoCredential("relationship retraction note", note); err != nil {
		return err
	}
	return s.transact(ctx, func(tx *sql.Tx) error {
		if err := requireRow(ctx, tx, "reality_relationship", "id", relationshipID); err != nil {
			return err
		}
		return s.appendAttachmentState(ctx, tx, "reality_relationship_event", "relationship_id",
			relationshipID, StateRetracted, note)
	})
}

// Relationships reads every edge touching an entity, in either direction:
// §4.8's analysis queries the ledger by relationship, and an edge is as
// findable from its object as from its subject.
func (s *Store) Relationships(ctx context.Context, entityID string) ([]Relationship, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.id, r.from_id, r.to_id, r.relation_kind,
		r.schema_version, r.created_at, r.payload_json,
		(SELECT e.state FROM reality_relationship_event e WHERE e.relationship_id = r.id
			ORDER BY e.seq DESC LIMIT 1)
		FROM reality_relationship r WHERE r.from_id = ? OR r.to_id = ?
		ORDER BY r.created_at, r.id`, entityID, entityID)
	if err != nil {
		return nil, fmt.Errorf("reality: read relationships: %w", err)
	}
	defer rows.Close()
	var out []Relationship
	for rows.Next() {
		var (
			record  Relationship
			kind    string
			created string
			payload []byte
			state   string
		)
		if err := rows.Scan(&record.ID, &record.FromID, &record.ToID, &kind,
			&record.SchemaVersion, &created, &payload, &state); err != nil {
			return nil, fmt.Errorf("reality: read relationships: %w", err)
		}
		record.Kind = RelationKind(kind)
		record.State = AttachmentState(state)
		if record.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("reality: relationship %s: %w", record.ID, err)
		}
		if err := json.Unmarshal(payload, &record.Payload); err != nil {
			return nil, fmt.Errorf("reality: decode relationship %s payload: %w", record.ID, err)
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

// MergeEntities folds source identities into a target.
//
// Both sides must currently speak for themselves. Merging an already-merged
// identity would build a chain whose reversal order matters, and §4.8 promises
// a reversible resolution rather than a reversible-in-the-right-order one; the
// operator undoes the earlier merge first. Kinds must match, because a
// repository folded into a machine is a resolution mistake this can catch
// deterministically rather than a judgement call.
func (s *Store) MergeEntities(ctx context.Context, in MergeInput) (Resolution, error) {
	var (
		record Resolution
		pub    publication
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		set := s.newRecordSet()
		resolution, err := s.mergeEntities(ctx, tx, in, set)
		if err != nil {
			return err
		}
		record = resolution
		// The resolution is the anchor. The membership entries exist because
		// it authorized them, and a fleet reader holding the entries without
		// it would know an identity moved without knowing what moved it —
		// which is also the state §4.8's reversibility could not be checked
		// against.
		pub, err = s.stageSet(ctx, tx, resolution.ID, set)
		return err
	})
	if err != nil {
		return Resolution{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Resolution{}, err
	}
	return record, nil
}

// mergeEntities performs a merge inside a caller's transaction, so an accepted
// plan's entity resolution commits with the acceptance that authorized it.
//
// The set it adds to belongs to whichever record authorized the merge: its own
// resolution when an operator merged directly, the acceptance when a plan did.
// That is why the anchor is the caller's to name.
func (s *Store) mergeEntities(ctx context.Context, tx *sql.Tx, in MergeInput,
	set *recordSet) (Resolution, error) {
	if len(in.SourceIDs) == 0 {
		return Resolution{}, fmt.Errorf("%w: merge names no source entity", ErrInvalidValue)
	}
	if in.TargetID == "" {
		return Resolution{}, fmt.Errorf("%w: merge names no target entity", ErrInvalidValue)
	}
	if in.Actor == "" {
		return Resolution{}, fmt.Errorf("%w: merge has no actor", ErrInvalidValue)
	}
	if err := checkNoCredential("merge reason", in.Reason); err != nil {
		return Resolution{}, err
	}
	sources := sortedUnique(in.SourceIDs)
	for _, id := range sources {
		if id == in.TargetID {
			return Resolution{}, fmt.Errorf("%w: merge target is also a source", ErrInvalidValue)
		}
	}
	targetKind, err := entityKindOf(ctx, tx, in.TargetID)
	if err != nil {
		return Resolution{}, err
	}
	if role, canonical, err := currentMembership(ctx, tx, in.TargetID); err != nil {
		return Resolution{}, err
	} else if role != RoleSelf {
		return Resolution{}, fmt.Errorf("%w: merge target %q is %s (canonical %q)",
			ErrConflict, in.TargetID, role, canonical)
	}
	for _, id := range sources {
		kind, err := entityKindOf(ctx, tx, id)
		if err != nil {
			return Resolution{}, err
		}
		if kind != targetKind {
			return Resolution{}, fmt.Errorf("%w: merge source %q is a %s and the target is a %s",
				ErrInvalidValue, id, kind, targetKind)
		}
		role, _, err := currentMembership(ctx, tx, id)
		if err != nil {
			return Resolution{}, err
		}
		if role != RoleSelf {
			return Resolution{}, fmt.Errorf("%w: merge source %q is already %s", ErrConflict, id, role)
		}
	}
	resolution, err := s.insertResolution(ctx, tx, ResolutionMerge, "", in.Actor,
		ResolutionPayload{Reason: in.Reason}, sources, []string{in.TargetID})
	if err != nil {
		return Resolution{}, err
	}
	if err := set.add(stagedResolution(resolution)); err != nil {
		return Resolution{}, err
	}
	for _, id := range sources {
		entry, err := s.appendMembership(ctx, tx, id, RoleMerged, in.TargetID, resolution.ID)
		if err != nil {
			return Resolution{}, err
		}
		// One entry per folded identity, and it is the only thing that says
		// what that identity now resolves to: the resolution names the
		// target, and this names the pointer.
		if err := set.add(stagedMembership(entry)); err != nil {
			return Resolution{}, err
		}
	}
	return resolution, nil
}

// SplitEntity records that one identity covered several subjects and creates
// the parts.
//
// The parent keeps its facts. They were asserted about the identity as it was
// understood then, and reattributing them would rewrite history; the parent is
// marked split so a reader knows to look at the parts, and a follow-up
// supersession moves any fact that belongs to one of them.
func (s *Store) SplitEntity(ctx context.Context, in SplitInput) (Resolution, []Entity, error) {
	var (
		record Resolution
		parts  []Entity
		pub    publication
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		set := s.newRecordSet()
		resolution, created, err := s.splitEntity(ctx, tx, in, set)
		if err != nil {
			return err
		}
		record, parts = resolution, created
		// The resolution is the anchor: the parts exist because the split
		// said the parent covered several subjects, so a part published
		// without it would be an identity the fleet cannot explain.
		pub, err = s.stageSet(ctx, tx, resolution.ID, set)
		return err
	})
	if err != nil {
		return Resolution{}, nil, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Resolution{}, nil, err
	}
	return record, parts, nil
}

// splitEntity performs a split inside a caller's transaction, adding its records
// to the set the caller anchors.
func (s *Store) splitEntity(ctx context.Context, tx *sql.Tx, in SplitInput,
	set *recordSet) (Resolution, []Entity, error) {
	if in.ParentID == "" {
		return Resolution{}, nil, fmt.Errorf("%w: split names no parent entity", ErrInvalidValue)
	}
	if len(in.Parts) < 2 {
		return Resolution{}, nil, fmt.Errorf("%w: a split needs at least two parts", ErrInvalidValue)
	}
	if in.Actor == "" {
		return Resolution{}, nil, fmt.Errorf("%w: split has no actor", ErrInvalidValue)
	}
	if err := checkNoCredential("split reason", in.Reason); err != nil {
		return Resolution{}, nil, err
	}
	role, _, err := currentMembership(ctx, tx, in.ParentID)
	if err != nil {
		return Resolution{}, nil, err
	}
	if role != RoleSelf {
		return Resolution{}, nil, fmt.Errorf("%w: split parent %q is %s", ErrConflict, in.ParentID, role)
	}
	parts := make([]Entity, 0, len(in.Parts))
	ids := make([]string, 0, len(in.Parts))
	for _, part := range in.Parts {
		created, err := s.createEntity(ctx, tx, part)
		if err != nil {
			return Resolution{}, nil, err
		}
		// The parts are staged here rather than by createEntity because
		// CreateEntity's own path publishes a subject an operator asked for
		// as its own closure of one. A split's parts exist because the split
		// said so, so they belong in the split's closure instead.
		if err := set.add(stagedEntity(created)); err != nil {
			return Resolution{}, nil, err
		}
		parts = append(parts, created)
		ids = append(ids, created.ID)
	}
	resolution, err := s.insertResolution(ctx, tx, ResolutionSplit, "", in.Actor,
		ResolutionPayload{Reason: in.Reason}, []string{in.ParentID}, ids)
	if err != nil {
		return Resolution{}, nil, err
	}
	if err := set.add(stagedResolution(resolution)); err != nil {
		return Resolution{}, nil, err
	}
	entry, err := s.appendMembership(ctx, tx, in.ParentID, RoleSplit, in.ParentID, resolution.ID)
	if err != nil {
		return Resolution{}, nil, err
	}
	if err := set.add(stagedMembership(entry)); err != nil {
		return Resolution{}, nil, err
	}
	return resolution, parts, nil
}

// UndoResolution reverses one merge or split.
//
// This is where §4.8's "mistaken resolutions remain reversible" is stored
// rather than reconstructed. The reversal is a new resolution that names the
// one it reverses and appends the membership rows that restore the earlier
// state: undoing a merge writes each source back to itself, and undoing a
// split folds the parts into the parent they came from. Nothing is rewritten
// and nothing is deleted, so afterwards the merge, its reversal, and every
// identity involved are all still addressable.
//
// That append-only shape is also what makes the reversal publishable, and it is
// worth saying which way round that goes. 0003's published row is insert-only
// by trigger, so a reversal that had mutated the resolution it undoes would
// have no wire form at all and would be visible only on the owning host. This
// one appends, so the undo and the entries restoring each identity travel as
// their own closure, and §4.8's reversibility is a fleet property rather than a
// local one.
//
// The database's unique index on reverses_id is what makes a second reversal
// impossible, so two concurrent undos cannot both succeed.
func (s *Store) UndoResolution(ctx context.Context, in UndoInput) (Resolution, error) {
	if in.Actor == "" {
		return Resolution{}, fmt.Errorf("%w: undo has no actor", ErrInvalidValue)
	}
	if err := checkNoCredential("undo reason", in.Reason); err != nil {
		return Resolution{}, err
	}
	var (
		record Resolution
		pub    publication
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		original, err := readResolution(ctx, tx, in.ResolutionID)
		if err != nil {
			return err
		}
		if original.Kind == ResolutionUndo {
			return fmt.Errorf("%w: %q is itself a reversal", ErrNotReversible, in.ResolutionID)
		}
		var reversedBy string
		err = tx.QueryRowContext(ctx, `SELECT id FROM reality_resolution WHERE reverses_id = ?`,
			in.ResolutionID).Scan(&reversedBy)
		if err == nil {
			return fmt.Errorf("%w: %q was already reversed by %q", ErrNotReversible, in.ResolutionID, reversedBy)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("reality: check resolution reversal: %w", err)
		}

		undo, err := s.insertResolution(ctx, tx, ResolutionUndo, in.ResolutionID, in.Actor,
			ResolutionPayload{Reason: in.Reason}, original.ResultIDs, original.SourceIDs)
		if err != nil {
			return err
		}
		set := s.newRecordSet()
		if err := set.add(stagedResolution(undo)); err != nil {
			return err
		}
		switch original.Kind {
		case ResolutionMerge:
			for _, id := range original.SourceIDs {
				entry, err := s.appendMembership(ctx, tx, id, RoleSelf, id, undo.ID)
				if err != nil {
					return err
				}
				if err := set.add(stagedMembership(entry)); err != nil {
					return err
				}
			}
		case ResolutionSplit:
			parent := original.SourceIDs[0]
			entry, err := s.appendMembership(ctx, tx, parent, RoleSelf, parent, undo.ID)
			if err != nil {
				return err
			}
			if err := set.add(stagedMembership(entry)); err != nil {
				return err
			}
			// The parts are not deleted — nothing here is — so they fold
			// into the parent they were split from and resolve to it.
			for _, id := range original.ResultIDs {
				entry, err := s.appendMembership(ctx, tx, id, RoleMerged, parent, undo.ID)
				if err != nil {
					return err
				}
				if err := set.add(stagedMembership(entry)); err != nil {
					return err
				}
			}
		}
		record = undo
		// The undo is the anchor: it is the record that authorized every
		// entry restoring an identity, and a fleet holding the entries
		// without it would see identities move back for no stated reason.
		pub, err = s.stageSet(ctx, tx, undo.ID, set)
		return err
	})
	if err != nil {
		return Resolution{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Resolution{}, err
	}
	return record, nil
}

func (s *Store) insertResolution(ctx context.Context, tx *sql.Tx, kind ResolutionKind,
	reversesID, actor string, payload ResolutionPayload, sources, results []string) (Resolution, error) {
	if !kind.valid() {
		return Resolution{}, fmt.Errorf("%w: resolution kind %q", ErrInvalidValue, kind)
	}
	encoded, err := marshalPayload(payload)
	if err != nil {
		return Resolution{}, err
	}
	id, err := newID("res")
	if err != nil {
		return Resolution{}, err
	}
	recorded := s.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO reality_resolution(
		id, resolution_kind, reverses_id, actor, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?, ?)`,
		id, string(kind), nullableID(reversesID), actor, formatTime(recorded), encoded); err != nil {
		return Resolution{}, fmt.Errorf("reality: insert resolution: %w", err)
	}
	// The two roles are written in a fixed order rather than by iterating a
	// map, so one resolution always produces the same statement sequence.
	for _, group := range []struct {
		role    string
		entries []string
	}{{"source", sources}, {"result", results}} {
		for position, entityID := range group.entries {
			if _, err := tx.ExecContext(ctx, `INSERT INTO reality_resolution_member(
				resolution_id, member_role, position, entity_id) VALUES(?, ?, ?, ?)`,
				id, group.role, position, entityID); err != nil {
				return Resolution{}, fmt.Errorf("reality: insert resolution member: %w", err)
			}
		}
	}
	return Resolution{
		ID:         id,
		Kind:       kind,
		ReversesID: reversesID,
		Actor:      actor,
		RecordedAt: recorded,
		SourceIDs:  copyIDs(sources),
		ResultIDs:  copyIDs(results),
		Payload:    payload,
	}, nil
}

// copyIDs copies a member list so a returned record does not alias the
// caller's slice, which an immutable record must not.
func copyIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// Resolution reads one entry of the merge/split history.
func (s *Store) Resolution(ctx context.Context, id string) (Resolution, error) {
	return readResolution(ctx, s.db, id)
}

func readResolution(ctx context.Context, q querier, id string) (Resolution, error) {
	var (
		record   Resolution
		kind     string
		reverses sql.NullString
		recorded string
		payload  []byte
	)
	err := q.QueryRowContext(ctx, `SELECT id, resolution_kind, reverses_id, actor, recorded_at, payload_json
		FROM reality_resolution WHERE id = ?`, id).
		Scan(&record.ID, &kind, &reverses, &record.Actor, &recorded, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Resolution{}, fmt.Errorf("%w: resolution %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Resolution{}, fmt.Errorf("reality: read resolution %q: %w", id, err)
	}
	record.Kind = ResolutionKind(kind)
	record.ReversesID = reverses.String
	if record.RecordedAt, err = parseTime(recorded); err != nil {
		return Resolution{}, fmt.Errorf("reality: resolution %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &record.Payload); err != nil {
		return Resolution{}, fmt.Errorf("reality: decode resolution %s payload: %w", id, err)
	}
	if record.SourceIDs, err = resolutionMembers(ctx, q, id, "source"); err != nil {
		return Resolution{}, err
	}
	if record.ResultIDs, err = resolutionMembers(ctx, q, id, "result"); err != nil {
		return Resolution{}, err
	}
	return record, nil
}

func resolutionMembers(ctx context.Context, q querier, id, role string) ([]string, error) {
	return queryStrings(ctx, q, `SELECT entity_id FROM reality_resolution_member
		WHERE resolution_id = ? AND member_role = ? ORDER BY position`, id, role)
}

// ResolutionHistory reads every resolution an entity took part in, oldest
// first. A reversed merge shows both the merge and its reversal, which is the
// record an operator needs to see that the mistake was found and undone.
func (s *Store) ResolutionHistory(ctx context.Context, entityID string) ([]Resolution, error) {
	// The ordering column is not in the result set, so this filters with
	// EXISTS rather than joining and de-duplicating: SQLite would otherwise
	// have to order a DISTINCT projection by a column it does not carry.
	ids, err := queryStrings(ctx, s.db, `SELECT r.id FROM reality_resolution r
		WHERE EXISTS(SELECT 1 FROM reality_resolution_member m
			WHERE m.resolution_id = r.id AND m.entity_id = ?)
		ORDER BY r.recorded_at, r.id`, entityID)
	if err != nil {
		return nil, err
	}
	out := make([]Resolution, 0, len(ids))
	for _, id := range ids {
		record, err := readResolution(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

// factSubjects lists the identities whose facts speak for one canonical
// entity: itself and everything merged into it.
//
// This is what makes a merge mean something. A fact asserted about an identity
// before it was merged is still a fact about the subject afterwards, and a
// focus decision that ignored it would silently lose reality at the moment two
// names were recognized as one. It follows one hop, which is exact because
// MergeEntities refuses to merge an already-merged identity.
func factSubjects(ctx context.Context, q querier, canonicalID string) ([]string, error) {
	merged, err := queryStrings(ctx, q, `SELECT m.entity_id FROM reality_entity_membership m
		WHERE m.canonical_id = ? AND m.entity_id <> ?
		AND m.seq = (SELECT MAX(seq) FROM reality_entity_membership x WHERE x.entity_id = m.entity_id)
		AND m.role = ? ORDER BY m.entity_id`, canonicalID, canonicalID, string(RoleMerged))
	if err != nil {
		return nil, err
	}
	return append([]string{canonicalID}, merged...), nil
}

// AttachContext records attributed operator guidance.
//
// §4.7 makes this guidance and never evidence, and the type system carries
// that rather than a comment: a Context has no locator, a fact's provenance is
// an event.Locator that recovers bytes from the archive, and there is no path
// from one to the other. Guidance can be linked to a fact, an answer, or an
// acceptance, and linking it never satisfies the requirement that a
// non-operator authority produce a locator.
//
// The single insert runs in a transaction because staging shares it. A
// one-statement transaction costs nothing and is what makes the guidance and
// the fleet's claim on it commit together.
func (s *Store) AttachContext(ctx context.Context, in ContextInput) (Context, error) {
	if err := in.validate(); err != nil {
		return Context{}, err
	}
	id, err := newID("ctx")
	if err != nil {
		return Context{}, err
	}
	at := in.At
	if at.IsZero() {
		at = s.now()
	}
	payload, err := marshalPayload(ContextPayload{Text: in.Text})
	if err != nil {
		return Context{}, err
	}
	record := Context{ID: id, Author: in.Author, At: at.UTC(), Text: in.Text}
	recorded := s.now()
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reality_context(
			id, author, supplied_at, recorded_at, payload_json) VALUES(?, ?, ?, ?, ?)`,
			id, in.Author, formatTime(at), formatTime(recorded), payload); err != nil {
			return fmt.Errorf("reality: insert operator context: %w", err)
		}
		wire, err := stagedContext(record, recorded)
		if err != nil {
			return err
		}
		pub, err = s.stage(ctx, tx, wire)
		return err
	})
	if err != nil {
		return Context{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Context{}, err
	}
	return record, nil
}

// Context reads one piece of attributed guidance.
func (s *Store) Context(ctx context.Context, id string) (Context, error) {
	var (
		record   Context
		at       string
		payload  []byte
		contents ContextPayload
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, author, supplied_at, payload_json
		FROM reality_context WHERE id = ?`, id).Scan(&record.ID, &record.Author, &at, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Context{}, fmt.Errorf("%w: operator context %q", ErrUnknownRecord, id)
	}
	if err != nil {
		return Context{}, fmt.Errorf("reality: read operator context %q: %w", id, err)
	}
	if record.At, err = parseTime(at); err != nil {
		return Context{}, fmt.Errorf("reality: operator context %s: %w", id, err)
	}
	if err := json.Unmarshal(payload, &contents); err != nil {
		return Context{}, fmt.Errorf("reality: decode operator context %s payload: %w", id, err)
	}
	record.Text = contents.Text
	return record, nil
}

// requireContext checks a context link before a write that references it, so a
// fact or an answer cannot cite guidance that is not there.
func requireContext(ctx context.Context, q querier, id string) error {
	if id == "" {
		return nil
	}
	return requireRow(ctx, q, "reality_context", "id", id)
}

// asOfOr resolves an optional instant against the store's clock, so every
// as-of read has one rule for what "now" means.
func (s *Store) asOfOr(at time.Time) time.Time {
	if at.IsZero() {
		return s.now()
	}
	return at.UTC()
}
