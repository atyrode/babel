-- Distinguish an unknown count from a count of zero.
--
-- A snapshot row can arrive two ways. A publication knows exactly what it
-- backed up, so it supplies every count. Reconciliation and rebuild only see
-- the repository's snapshot list, and restic records a summary on most but not
-- all snapshots: a record without one has no counts to report, which is not the
-- same as having backed up nothing.
--
-- With NOT NULL DEFAULT 0 those two cases were indistinguishable, so a
-- reconciled snapshot lacking a summary would read as "0 files, 0 bytes" - a
-- statement about the archive that is simply false. Making the measures
-- nullable lets a reader tell "unknown" from "nothing", and a later push from
-- the owning host replaces NULL with the real values.
--
-- session_count is included for the same reason: reconciliation cannot know how
-- many sessions a snapshot holds without reading its file tree, so storing 0
-- would claim the snapshot is empty.
--
-- No column is added or removed, so the Phase A plaintext allowlist is
-- unchanged: these remain sizes and counts (SPEC.md 9).

ALTER TABLE snapshots
    ALTER COLUMN files_new        DROP NOT NULL,
    ALTER COLUMN files_new        DROP DEFAULT,
    ALTER COLUMN files_changed    DROP NOT NULL,
    ALTER COLUMN files_changed    DROP DEFAULT,
    ALTER COLUMN files_unmodified DROP NOT NULL,
    ALTER COLUMN files_unmodified DROP DEFAULT,
    ALTER COLUMN bytes_added      DROP NOT NULL,
    ALTER COLUMN bytes_added      DROP DEFAULT,
    ALTER COLUMN session_count    DROP NOT NULL,
    ALTER COLUMN session_count    DROP DEFAULT;
