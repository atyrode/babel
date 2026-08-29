-- Application-level instance revocation (SPEC.md 9, 11; pre-deployment gate 14).
--
-- Evicting one machine from the fleet - a decommissioned server, a retired
-- laptop - must not require re-keying the repository. Where a provider issues a
-- database user per instance, eviction is a DROP ROLE and PostgreSQL itself
-- enforces it. Clever Cloud's managed PostgreSQL cannot create database users
-- (provider confirmation, 2026-08-28), so the first deployment runs one
-- credential for the whole deployment, and then no database-level control can
-- revoke a single instance: the credential a revoked instance holds is the same
-- one every other instance holds. Revocation therefore has to be a row that
-- every write path consults - see revoke.go, lease.go, and publish.go.
--
-- NULL means active. revoked_at is a timestamp, which SPEC.md 9 already permits
-- in the Phase A catalog, so this adds no data class to the plaintext boundary,
-- only an entry to allowlist.go. It records when the eviction was decided by
-- server time, so a machine with a skewed clock cannot backdate its own state.
--
-- What this is not: an instance still holding the shared credential can clear
-- its own revoked_at. This stops a cooperating instance, and bounds a
-- compromised one only until someone notices; against a hostile holder the real
-- controls remain fleet-wide credential rotation and repository-password
-- custody (SPEC.md 9).

ALTER TABLE instances ADD COLUMN revoked_at timestamptz;

-- Recording the new version keeps the schema and its allowlist in step, and
-- stops a *newer* database from being written by this binary. It is not
-- downgrade protection: a binary that predates revoked_at performs no version
-- check, so it is unaffected by this row. The literal is deliberate - a
-- migration is frozen bytes and must keep establishing the version it
-- established, even after the constant in sharedcatalog.go moves on. The guard
-- leaves a newer deployment alone.
UPDATE deployments SET schema_version = 2 WHERE schema_version < 2;
