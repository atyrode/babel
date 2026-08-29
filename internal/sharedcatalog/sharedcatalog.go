// Package sharedcatalog implements Babel's Phase A PostgreSQL catalog and
// coordination schema (SPEC.md 6.2, 9).
//
// PostgreSQL is derived state, never archive truth: restic commits a snapshot
// first, and this catalog records opaque identity and ordering so any authorized
// instance can see the fleet without downloading transcripts. Losing the
// database is recoverable from the repository snapshot list plus source rescans,
// so nothing here may hold data that exists nowhere else.
//
// The plaintext boundary is enforced, not documented: see allowlist.go.
package sharedcatalog

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// SchemaVersion is the Phase A schema version. A deployment records it, and an
// instance refuses to operate against a newer one rather than guessing.
//
// It buys no protection against an OLDER binary, and no comment here should
// imply otherwise: a binary that predates a check does not perform it. What
// constrains an old binary is only what PostgreSQL evaluates for it - a lease's
// SQL expiry predicate, a fence comparison, a constraint.
const SchemaVersion = 1

// ErrSchemaTooNew reports a database migrated by a newer Babel. Downgrading
// silently would risk writing rows an older writer cannot represent.
var ErrSchemaTooNew = errors.New("shared catalog schema is newer than this babel")

// Schema is the schema Babel owns. Every catalog object lives in it, and
// nothing else may.
//
// Babel does not own the database. A managed provider may pre-install
// extensions - Clever Cloud's PostgreSQL ships PostGIS, pg_stat_statements and
// dozens more, which put their own tables and views in `public` - and an
// operator may keep unrelated data there. Sharing a schema with them would make
// the allowlist gate in allowlist.go unenforceable: it could no longer tell a
// relation Babel created from one that was always there, so it would have to
// either reject the provider's own extensions or stop checking for unexpected
// tables. A schema Babel creates and owns keeps that check exact.
const Schema = "babel"

// createSchemaDDL is built at compile time from Schema, which is why Schema must
// stay a bare lowercase identifier.
const createSchemaDDL = `CREATE SCHEMA IF NOT EXISTS ` + Schema

// Open connects to the shared catalog. The DSN is supplied by validated storage
// configuration and never logged; callers pass it through and must not echo it.
//
// The pool is deliberately small: an instance publishes from one goroutine under
// a host lease, and a wide pool would only add ways to interleave writes.
func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// Parsing does not dial, so an error here is a malformed DSN. Do not
		// wrap it with the DSN itself: it carries credentials.
		return nil, errors.New("open shared catalog: invalid connection string")
	}
	// Pin the schema on every connection in the pool rather than qualifying
	// each statement. Unqualified DDL in a migration then lands in Babel's
	// schema, and a query can never silently resolve to a same-named relation
	// that a provider extension or an operator put in `public`.
	cfg.RuntimeParams["search_path"] = Schema
	db := stdlib.OpenDB(*cfg)
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("reach shared catalog: %w", redactDSN(err, dsn))
	}
	return db, nil
}

// Migrate creates Babel's schema if it is absent, applies every pending
// migration in one transaction per migration, records it in schema_migrations,
// then verifies the resulting schema against the Phase A allowlist. A migration
// that widens the plaintext boundary therefore fails at apply time rather than
// after data has been written.
//
// It requires a credential that may create a schema and the objects in it.
// Where a provider can issue more than one user, normal instances hold an
// application role without those rights (SPEC.md 9).
func Migrate(ctx context.Context, db *sql.DB) (applied []string, err error) {
	// Schema is a compile-time constant matching a bare identifier, asserted by
	// TestSchemaNameIsABareIdentifier, so it is concatenated rather than routed
	// through the role-DDL renderer that exists for operator-supplied names.
	if _, err := db.ExecContext(ctx, createSchemaDDL); err != nil {
		return nil, fmt.Errorf("create schema %s: %w", Schema, err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
		    version    text        PRIMARY KEY,
		    applied_at timestamptz NOT NULL DEFAULT now()
		)`); err != nil {
		return nil, fmt.Errorf("create migration ledger: %w", err)
	}

	done, err := appliedVersions(ctx, db)
	if err != nil {
		return nil, err
	}

	pending, err := migrations()
	if err != nil {
		return nil, err
	}

	for _, m := range pending {
		if done[m.version] {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			return applied, err
		}
		applied = append(applied, m.version)
	}

	if err := Verify(ctx, db); err != nil {
		return applied, err
	}
	return applied, nil
}

// EnsureCompatible reports whether this binary may operate against the
// database. A newer schema is refused; anything else that is not ready reports
// what to run.
//
// An entirely unmigrated database is a normal state for a new deployment, not a
// malfunction, so it must not surface as a raw "relation does not exist" from
// whichever query happened to run first.
func EnsureCompatible(ctx context.Context, db *sql.DB) error {
	var ready bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass($1) IS NOT NULL`, Schema+".deployments").Scan(&ready); err != nil {
		return fmt.Errorf("look for the shared catalog schema: %w", err)
	}
	if !ready {
		return errors.New("shared catalog has no schema yet: run `babel storage migrate`")
	}

	var version int
	err := db.QueryRowContext(ctx,
		`SELECT coalesce(max(schema_version), 0) FROM deployments`).Scan(&version)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	switch {
	case version > SchemaVersion:
		return fmt.Errorf("%w: database is version %d, this babel implements %d",
			ErrSchemaTooNew, version, SchemaVersion)
	case version < SchemaVersion:
		return fmt.Errorf("shared catalog is version %d and this babel implements %d: run `babel storage migrate`",
			version, SchemaVersion)
	}
	return nil
}

type migration struct {
	version string
	body    string
}

// PendingMigrations reports embedded migrations the database has not recorded
// yet, in apply order.
//
// The ledger is the only honest source for this question. A deployment's
// recorded schema_version answers a different one - which version the fleet
// bootstrapped as - and it is written at first publication, not by migrating,
// so reading it here would report a fully migrated catalog as pending until
// something published into it.
func PendingMigrations(ctx context.Context, db *sql.DB) ([]string, error) {
	all, err := migrations()
	if err != nil {
		return nil, err
	}
	var ledgerExists bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass($1) IS NOT NULL`, Schema+".schema_migrations").Scan(&ledgerExists); err != nil {
		return nil, fmt.Errorf("look for the migration ledger: %w", err)
	}
	done := map[string]bool{}
	if ledgerExists {
		if done, err = appliedVersions(ctx, db); err != nil {
			return nil, err
		}
	}
	var pending []string
	for _, m := range all {
		if !done[m.version] {
			pending = append(pending, m.version)
		}
	}
	return pending, nil
}

func migrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		version := strings.TrimSuffix(e.Name(), ".sql")
		if _, err := strconv.Atoi(strings.SplitN(version, "_", 2)[0]); err != nil {
			return nil, fmt.Errorf("migration %s must start with a numeric ordinal", e.Name())
		}
		out = append(out, migration{version: version, body: string(body)})
	}
	// Lexicographic order over zero-padded ordinals is apply order.
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

func appliedVersions(ctx context.Context, db *sql.DB) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()
	done := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan migration ledger: %w", err)
		}
		done[v] = true
	}
	return done, rows.Err()
}

// applyOne runs a migration and its ledger insert in one transaction, so an
// interrupted migration either applied fully or not at all. PostgreSQL supports
// transactional DDL, which is what makes this safe.
func applyOne(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", m.version, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.body); err != nil {
		return fmt.Errorf("apply migration %s: %w", m.version, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version) VALUES ($1)`, m.version); err != nil {
		return fmt.Errorf("record migration %s: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", m.version, err)
	}
	return nil
}

// redactDSN keeps a connection string out of an error message. Driver errors
// sometimes echo the DSN, which would put the catalog password in a log.
//
// Replacing the whole DSN is not sufficient on its own: a driver may
// reconstruct a connection string from parsed fields rather than echo the one
// it was handed, and that reconstruction would not match. pgx happens to omit
// the password when it does this, but relying on a dependency's discretion is
// not a guarantee, so the password is redacted on its own as well - it is the
// part that must never appear, in any arrangement.
func redactDSN(err error, dsn string) error {
	if dsn == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), dsn, "[redacted dsn]")
	if u, parseErr := url.Parse(dsn); parseErr == nil {
		if password, ok := u.User.Password(); ok && password != "" {
			msg = strings.ReplaceAll(msg, password, "[redacted]")
		}
	}
	return errors.New(msg)
}
