-- What a proposal rests on, in the clear (SPEC.md 4.5, 9, 763; issue #114).
--
-- WHY A TABLE RATHER THAN AN INFERENCE
--
-- Issue #114 makes a frontier proposal have two lawful provenance forms, and
-- both are first-class. A consolidated proposal rests on one or more findings
-- and derives its hypotheses transitively through them (SPEC.md 4.5). A
-- candidate proposal is the value-claim half of an emitted candidate: it
-- addresses one or more hypotheses directly, rests on no finding at all, and is
-- a want or an option rather than a verified fact.
--
-- `analysis_records` cannot tell them apart. It is generic by design - an
-- identity, a kind, a schema version, a place in a closure, and the reference to
-- the sealed object that holds everything else - and it carries no relationship
-- column of any sort, because nothing before #114 needed one. So a fleet host
-- holding only the catalog credential sees `kind = 'proposal'` twice and has no
-- way to ask which of the two stands on evidence. Rendering an unbacked want
-- with the authority of a finding-backed one is exactly the epistemic failure
-- #114 exists to prevent, and a surface that cannot ask the question commits it
-- by default rather than by mistake.
--
-- WHY NOT THE EDGE GRAPH
--
-- `analysis_edges` looks like it already answers this, and it does not. A
-- candidate proposal does mint an `addresses` edge per hypothesis, but
-- internal/frontier mints those edges AFTER the transaction that made the record
-- durable has committed, and treats a failure to mint one as a warning rather
-- than an error - by explicit design, stated in internal/frontier/reference.go's
-- own package prose, so that a citation failure can never cost the record that
-- was already written. The graph is therefore a best-effort shadow of the
-- corpus, and reading a record's form out of it inverts the safe direction of
-- the error: a MISSING edge would make a finding-backed proposal look unbacked,
-- so the one failure mode is the one that discards verified provenance.
--
-- A record's form is a property of the record, not of its citation graph. So it
-- travels in the same transaction as the row it describes, which is what makes
-- "this row exists" and "this row's provenance is legible" one event rather than
-- two with a crash window between them. The edge graph keeps doing what it is
-- for - walking the corpus - and this table answers what one record is.
--
-- WHAT IS AND IS NOT STORED
--
-- Relationship ids and nothing else. SPEC.md 9's Phase B plaintext allowlist
-- names relationship IDs, and that is precisely what these five columns are: the
-- record whose provenance this is, the ordinal that preserves the order the
-- producer asserted its subjects in, a closed two-value vocabulary saying which
-- frontier store an id belongs to, and the id itself. No claim, no rationale, no
-- confidence, no score, no measure of anything. A proposal's words are in the
-- sealed object with every other Phase B payload, and a column here that held
-- one would fail the Phase B class gate in allowlist.go.
--
-- record_id is a foreign key rather than a bare string, for 0008's reason: this
-- table is a projection of an `analysis_records` row rather than a second place
-- a proposal can exist, and there is deliberately no row here that names no
-- record. The primary key is the pair, not the record, because a proposal has
-- many subjects - #114's relation is many-to-many in both directions - and the
-- ordinal is what makes "the first finding it rests on" a stable answer.
--
-- subject_kind is closed in a CHECK for the reason 0003 closes `kind` and 0008
-- closes `edge_kind`: it is provenance semantics rather than data. A finding and
-- a hypothesis are the only two subjects a proposal can rest on or address, and
-- a third value reaching this column would assert a lineage nothing in the
-- frontier defines. The form itself is not a column: it is derived from which
-- subject kinds a record's rows carry - any finding present means consolidated,
-- hypotheses alone mean candidate - because a stored form sitting beside the
-- rows that imply it is a second answer, and a second answer can disagree with
-- the first.
--
-- WHAT ABSENCE MEANS
--
-- A proposal with no rows here is a proposal written before #114, and it says
-- nothing about its form. Nothing back-fills them: the operator scoped #114 to
-- new output only, and a migration that guessed a historical proposal's
-- provenance from whatever the edge graph happens to hold would be inventing
-- lineage, which is the exact thing this table exists to stop being invented.
--
-- So a reader has three answers rather than two, and the third is "unknown": a
-- finding subject means consolidated, hypothesis subjects alone mean candidate,
-- and no rows at all mean a record from before the distinction was recorded.
-- Collapsing that third case into either of the first two is the same failure in
-- a new place, which is why it is stated here rather than left to a reader to
-- discover from an empty result.
--
-- IMMUTABILITY
--
-- A proposal's provenance is fixed when the proposal is written. A proposal that
-- rested on different evidence is a different proposal, answered by a later
-- record and never by editing this one - SPEC.md 4.7's rule that rejection never
-- deletes, applied to provenance - and the trigger is what makes that a property
-- of the database rather than of every future statement that touches the table.
--
-- SchemaVersion stays 1, on 0008's reasoning exactly: this is additive, it
-- constrains no existing writer, and EnsureCompatible refuses a database
-- migrated past the binary, so raising it would stop every live Phase A writer
-- against production (SPEC.md 14).

CREATE TABLE analysis_proposal_subjects (
    record_id    text        NOT NULL REFERENCES analysis_records (record_id),
    -- The producer's own order, from zero. It is part of the key rather than a
    -- sort hint because "the finding this proposal was consolidated from" is a
    -- question with a first answer, and a set would lose it.
    position     integer     NOT NULL CHECK (position >= 0),
    subject_kind text        NOT NULL CHECK (subject_kind IN ('hypothesis', 'finding')),
    subject_id   text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (record_id, position)
);

-- The question #114 added: which proposals compete against this hypothesis. The
-- relation is many-to-many in both directions - one hypothesis draws several
-- candidate proposals, one consolidated proposal covers several hypotheses - so
-- the reverse lookup is a first-class read rather than a rare one, and it is
-- keyed by kind and id together because an id is only unique within its store.
CREATE INDEX analysis_proposal_subjects_subject_idx
    ON analysis_proposal_subjects (subject_kind, subject_id);

CREATE FUNCTION analysis_proposal_subjects_append_only() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION
        'analysis_proposal_subjects is append-only: % refused; a proposal resting on different evidence is a different proposal (SPEC.md 4.7, issue #114)', TG_OP;
END;
$$;

CREATE TRIGGER analysis_proposal_subjects_append_only_trg
    BEFORE UPDATE OR DELETE ON analysis_proposal_subjects
    FOR EACH ROW EXECUTE FUNCTION analysis_proposal_subjects_append_only();
