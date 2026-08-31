-- The typed reference graph's plaintext shape (SPEC.md 4.7, 9, 763; issue #113).
--
-- WHY A TABLE RATHER THAN A PAYLOAD FIELD
--
-- An edge says that one Babel record cites another: this finding rests on that
-- session, this revision supersedes that one, this proposal addresses that
-- hypothesis. 0003_phase_b_records already carries such a record - the `link`
-- kind - and it carries it the way it carries every Phase B record, as a
-- reference to a sealed object. That is right for a record's content and wrong
-- for a citation, because a citation is not content: it is the answer to "where
-- does this record sit in the corpus", and a host without a payload key cannot
-- ask it of an object it cannot open.
--
-- SPEC.md 763 settles which half is which. An edge's kind and both endpoint
-- references are identifier and kind metadata, plaintext-eligible; the edge's
-- note - the only prose an edge carries - is content and stays in the envelope.
-- So the shape travels in columns and the words do not, and the fleet-wide
-- graph is navigable on a machine that can read none of the records in it
-- (issue #112): a sealed record still shows what it cites and what cites it.
--
-- WHAT IS AND IS NOT STORED
--
-- Six columns, and every one of them is on 0003's own terms. record_id is the
-- edge's Phase B record id, so this table is a projection of an
-- `analysis_records` row rather than a second place an edge can exist:
-- 0003_phase_b_records still holds the object with the note in it, and there is
-- deliberately no row here that names no record. edge_kind is a closed
-- vocabulary in a CHECK, for exactly the reason 0003 closes `kind`: an edge kind
-- is relation semantics, and a model that could mint a new one could assert a
-- relation nobody defined. from_kind and to_kind are the record namespaces
-- internal/reference's resolver registry is keyed by; from_id and to_id are
-- those namespaces' own durable identifiers.
--
-- The endpoint ids are opaque by construction, and it is internal/reference's
-- resolver registry - not this table - that makes them so. A session resolves
-- by its durable session key, which is the `sessions.session_uid` digest, so a
-- selector or a workspace path cannot become an endpoint: it would fail
-- resolution and the write would be refused before anything was staged. An
-- analysis record resolves by the client-generated id its own store minted.
-- Neither says anything about what the record contains.
--
-- No note, no actor prose, no free text. An edge's actor is in the sealed
-- payload beside its note: attribution is plaintext-eligible for a RUN
-- (0003 admits actor attribution) but an actor reference here would be the one
-- column where an operator identity of the operator's own choosing reached
-- PostgreSQL, and the graph is navigable without it.
--
-- IMMUTABILITY
--
-- An edge is append-only in internal/reference and append-only here. A wrong
-- link is answered by a later edge, never by editing one - SPEC.md 4.7's rule
-- that rejection never deletes, applied to citations - and the trigger is what
-- makes that a property of the database rather than of every future statement.
--
-- SchemaVersion stays 1: this is additive, it constrains no existing writer,
-- and EnsureCompatible refuses a database migrated past the binary, so raising
-- it would stop every live Phase A writer against production (SPEC.md 14).

CREATE TABLE analysis_edges (
    -- The edge's own Phase B record id. It is the primary key rather than a
    -- surrogate because an edge is one record: two rows here for one record
    -- would be two answers to what that record asserts.
    record_id  text        PRIMARY KEY REFERENCES analysis_records (record_id),
    edge_kind  text        NOT NULL CHECK (edge_kind IN (
                   'evidence', 'supersedes', 'refines',
                   'addresses', 'inspired_by', 'duplicates')),
    from_kind  text        NOT NULL,
    from_id    text        NOT NULL,
    to_kind    text        NOT NULL,
    to_id      text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    -- A record cannot cite itself. The endpoints are a pair, so the constraint
    -- is on the pair: one namespace citing another id of the same kind is the
    -- normal case a revision chain produces.
    CHECK (from_kind <> to_kind OR from_id <> to_id)
);

-- The two questions a render surface asks of one record: what does it cite, and
-- what cites it. Both are answered by namespace and id together, because an id
-- is only unique within its namespace.
CREATE INDEX analysis_edges_from_idx ON analysis_edges (from_kind, from_id);
CREATE INDEX analysis_edges_to_idx ON analysis_edges (to_kind, to_id);
CREATE INDEX analysis_edges_kind_idx ON analysis_edges (edge_kind);

CREATE FUNCTION analysis_edges_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION
        'analysis_edges is append-only: % refused; a wrong link is answered by a later edge (SPEC.md 4.7)', TG_OP;
END;
$$;

CREATE TRIGGER analysis_edges_append_only_trg
    BEFORE UPDATE OR DELETE ON analysis_edges
    FOR EACH ROW EXECUTE FUNCTION analysis_edges_append_only();
