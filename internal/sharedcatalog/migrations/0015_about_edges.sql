-- A record says what it is about (SPEC.md 4.13).
--
-- migrations/0008 closed the edge vocabulary in a CHECK on purpose: a relation
-- kind reaching PostgreSQL is a migration and a review, never a string a
-- caller invents. This is that review, for one kind.
--
-- `about` is a record's membership in a topic, and a topic is a Reality Ledger
-- entity (SPEC.md 4.8, 4.13) - a repository, a project, a service, a concept.
-- The edge therefore points out of the analysis corpus and into the ledger:
-- from a hypothesis, observation, finding or proposal, to a `reality_entity`.
-- That is new for this table and needs no column, because 0008 already stores
-- both endpoints as a namespace and an id and deliberately constrains neither
-- to the analysis kinds - the resolver registry is what decides which
-- namespaces a machine can vouch for.
--
-- Why it belongs in the clear, on 0008's own terms: SPEC.md 9.1 admits kind
-- and identifier metadata, and both endpoints are opaque identifiers - `ent_`
-- and `hyp_` prefixed random ids that name nothing about the project they
-- belong to. What a filing SAYS - the rationale for putting this record under
-- that topic, and the reason a withdrawal gives - is content and stays sealed
-- in the object, exactly like an edge's note. A fleet host with no payload key
-- can therefore see that a corpus is organized and how densely, and cannot
-- read what any of it is about.
--
-- Widening a CHECK is the one edit an append-only table admits, because it
-- refuses fewer rows than before: every row that satisfied 0008's constraint
-- satisfies this one, so no existing row is invalidated and no writer that
-- predates this migration is affected. The constraint is dropped and recreated
-- rather than edited because PostgreSQL has no ALTER for a CHECK's expression;
-- the name is 0008's own generated name, which is what a deployment that ran
-- 0008 holds.
--
-- SchemaVersion stays 1: no table, column or plaintext boundary moves, and
-- EnsureCompatible still refuses a database migrated past the binary.

ALTER TABLE analysis_edges DROP CONSTRAINT analysis_edges_edge_kind_check;

ALTER TABLE analysis_edges ADD CONSTRAINT analysis_edges_edge_kind_check
    CHECK (edge_kind IN (
        'evidence', 'supersedes', 'refines',
        'addresses', 'inspired_by', 'duplicates', 'about'));
