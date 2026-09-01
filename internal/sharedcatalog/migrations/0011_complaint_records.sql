-- The operator's own steering, admitted to the Phase B kind vocabulary
-- (SPEC.md 4.7, 9; issue #115).
--
-- WHY THIS IS A MIGRATION AND NOT A NEW TABLE
--
-- 0003_phase_b_records holds every Phase B record the same way: the content is
-- sealed into an object and PostgreSQL keeps the reference, the kind, the schema
-- version and the ordinal. A complaint is a Phase B record on exactly those
-- terms - durable, irreplaceable, envelope-encrypted, object-first - so it needs
-- no columns of its own and gets none. Its whole substance is a sentence the
-- operator wrote, which is content and stays in the envelope; what reaches this
-- database is what reaches it for a hypothesis, and that symmetry is the point.
--
-- What it does need is permission to exist. 0003 closes `kind` in a CHECK on
-- purpose: a record type reaching PostgreSQL under a new kind is a migration and
-- a review, not an unnoticed string, so that nothing can quietly start writing a
-- category no reader knows how to interpret. This is that migration and that
-- review.
--
-- WHY NOT PUBLISH IT AS ONE OF THE NINE KINDS THAT ALREADY EXIST
--
-- `context` is the near miss and it is worth naming, because reusing it would
-- have cost nothing today and would have been wrong. Context is attributed
-- operator guidance: a person answering a question Babel put in front of them,
-- inside a review, about a record. A complaint answers nothing - it is
-- unprompted, it names no subject, and it can be the first thing that ever
-- happens on a machine. A fleet reader that saw both as `context` could not tell
-- the operator's standing annoyance from a run's assembled background material,
-- and "which of these did nobody ask for" is precisely the question steering
-- exists to answer.
--
-- WHAT THIS SAYS ABOUT LIFECYCLE, BY OMISSION
--
-- Nothing. There is no complaint state column here, no resolved flag, no closure
-- timestamp and no assignee, and their absence is #115's charter guard expressed
-- where it is hardest to walk back: the moment a complaint has a resolved state,
-- Babel is a ticket queue, and GitHub already is one. "Was this addressed?" is
-- answered from analysis_edges (0008) - the `addresses` edges a hypothesis or a
-- proposal mints towards a complaint - so the answer is derived from work that
-- actually happened rather than from a field somebody remembered to set.
--
-- Amendment needs no schema either. A complaint's revision chain publishes as
-- rows: each wording is its own record with its own object, and the chain that
-- orders them travels inside the sealed payload where the words are, because a
-- root and a sequence number say nothing PostgreSQL needs and 0003's
-- insert-only discipline already refuses the alternative of editing one.
--
-- WHY THE CONSTRAINT IS DROPPED AND RE-ADDED
--
-- A CHECK is not extensible: widening a closed vocabulary means replacing the
-- constraint that closed it. The constraint name is PostgreSQL's own default for
-- an inline column CHECK - <table>_<column>_check - which 0003 declared without
-- naming, so it is named here rather than guessed at: DROP CONSTRAINT without IF
-- EXISTS fails loudly on a database whose constraint is named something else,
-- which is the right outcome. Silently skipping the drop would leave the old
-- vocabulary in force and turn every complaint into a permanently stuck
-- pending-sync row on that deployment.
--
-- The new CHECK is the old nine values plus 'complaint'. Nothing is removed, so
-- no row that satisfied the old constraint can fail the new one, and the
-- statement cannot fail on existing data.
--
-- SchemaVersion stays 1: this is additive, it constrains no existing writer, and
-- EnsureCompatible refuses a database migrated past the binary, so raising it
-- would stop every live Phase A writer against production (SPEC.md 14).

ALTER TABLE analysis_records
    DROP CONSTRAINT analysis_records_kind_check;

ALTER TABLE analysis_records
    ADD CONSTRAINT analysis_records_kind_check CHECK (kind IN (
        'hypothesis', 'observation', 'finding', 'proposal',
        'link', 'disposition', 'context',
        'preparation', 'receipt', 'complaint'));
