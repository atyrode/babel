package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// dataTables receive full DML for an application role. schema_migrations is
// deliberately absent: an instance may read the ledger but never write it, so a
// compromised or buggy instance cannot claim a migration it did not apply.
var dataTables = []string{
	"deployments",
	"instances",
	"hosts",
	"snapshots",
	"sessions",
	"host_leases",
	"idempotency_keys",
}

// minPasswordLen bounds an application credential. Deployment supplies a
// generated password; this only rules out values too short to redact safely.
const minPasswordLen = 12

// validRoleName is intentionally stricter than PostgreSQL: identifiers reach DDL
// that cannot be parameterized, so the accepted shape stays small and boring.
var validRoleName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// EnsureAppRole creates or updates a least-privilege application role and
// returns nothing sensitive.
//
// SPEC.md 9 requires that a separate migration credential be supplied
// ephemerally to `storage migrate` and that normal instances cannot change
// schema. This is the enforcement: the application role receives DML on data
// tables, SELECT on the migration ledger, and no DDL, no ownership, no CREATE
// on the schema. Each instance uses a distinct revocable credential, so
// revoking one instance never disturbs another.
//
// It must be called with the migration credential. Statements are built by
// PostgreSQL's own format() with %I/%L so an identifier or password never
// reaches DDL through Go string concatenation.
func EnsureAppRole(ctx context.Context, db *sql.DB, role, password string) error {
	if !validRoleName.MatchString(role) {
		return fmt.Errorf("invalid application role name: must match %s", validRoleName)
	}
	// A minimum length keeps the redaction guarantee meaningful: a very short
	// secret could otherwise coincide with ordinary words in an error message.
	// Deployment supplies a generated credential, so this bound is never the
	// limiting factor in practice.
	if len(password) < minPasswordLen {
		return fmt.Errorf("application role password must be at least %d characters", minPasswordLen)
	}

	var database string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return fmt.Errorf("read current database: %w", err)
	}

	var exists bool
	if err := db.QueryRowContext(ctx,
		`SELECT exists(SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return fmt.Errorf("look up role: %w", err)
	}

	// PostgreSQL renders the statement so quoting is its problem, not ours. The
	// rendered text carries the password, so it is never logged or returned.
	verb := "CREATE ROLE"
	if exists {
		verb = "ALTER ROLE"
	}
	if err := execRendered(ctx, db,
		`SELECT format('%s %%I WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT PASSWORD %%L', $1::text, $2::text)`,
		verb, password, role, password); err != nil {
		return fmt.Errorf("provision application role: %w", err)
	}

	grants := []struct {
		query string
		args  []any
		what  string
	}{
		// No CREATE: the role cannot add tables of its own to the schema.
		{`SELECT format('GRANT CONNECT ON DATABASE %I TO %I', $1::text, $2::text)`, []any{database, role}, "connect"},
		{`SELECT format('GRANT USAGE ON SCHEMA public TO %I', $1::text)`, []any{role}, "schema usage"},
		{`SELECT format('REVOKE CREATE ON SCHEMA public FROM %I', $1::text)`, []any{role}, "revoke create"},
		{`SELECT format('GRANT SELECT ON schema_migrations TO %I', $1::text)`, []any{role}, "ledger read"},
	}
	for _, t := range dataTables {
		grants = append(grants, struct {
			query string
			args  []any
			what  string
		}{
			`SELECT format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO %I', $1::text, $2::text)`,
			[]any{t, role},
			"dml on " + t,
		})
	}

	for _, g := range grants {
		if err := execRenderedArgs(ctx, db, g.query, g.args...); err != nil {
			return fmt.Errorf("grant %s: %w", g.what, err)
		}
	}
	return nil
}

// RevokeAppRole removes an instance's credential. Per-instance revocation is a
// SPEC.md 14 gate requirement, and it must not disturb other instances: only
// this role's grants and login are removed, never any data.
func RevokeAppRole(ctx context.Context, db *sql.DB, role string) error {
	if !validRoleName.MatchString(role) {
		return fmt.Errorf("invalid application role name: must match %s", validRoleName)
	}
	var database string
	if err := db.QueryRowContext(ctx, `SELECT current_database()`).Scan(&database); err != nil {
		return fmt.Errorf("read current database: %w", err)
	}

	steps := [][]any{
		{`SELECT format('REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %I', $1::text)`, role},
		{`SELECT format('REVOKE ALL ON SCHEMA public FROM %I', $1::text)`, role},
		{`SELECT format('REVOKE ALL ON DATABASE %I FROM %I', $1::text, $2::text)`, database, role},
		{`SELECT format('ALTER ROLE %I NOLOGIN', $1::text)`, role},
	}
	for _, s := range steps {
		if err := execRenderedArgs(ctx, db, s[0].(string), s[1:]...); err != nil {
			return fmt.Errorf("revoke application role: %w", err)
		}
	}
	return nil
}

// execRendered renders a statement whose first substitution is a trusted
// keyword and whose arguments include a secret, then executes it. The secret is
// removed from any error regardless of its length or content, because a driver
// error can echo the failing statement.
func execRendered(ctx context.Context, db *sql.DB, tmpl, verb, secret string, args ...any) error {
	if err := execRenderedArgs(ctx, db, fmt.Sprintf(tmpl, verb), args...); err != nil {
		if secret == "" {
			return err
		}
		return errors.New(strings.ReplaceAll(err.Error(), secret, "[redacted]"))
	}
	return nil
}

// execRenderedArgs asks PostgreSQL to render a DDL statement with correct
// identifier and literal quoting, then executes the result.
//
// Arguments are not scrubbed here: they are identifiers - role, database, and
// table names - and naming them is what makes a grant failure diagnosable.
// Secrets travel through execRendered, which redacts them explicitly.
func execRenderedArgs(ctx context.Context, db *sql.DB, query string, args ...any) error {
	var stmt string
	if err := db.QueryRowContext(ctx, query, args...).Scan(&stmt); err != nil {
		return fmt.Errorf("render statement: %w", err)
	}
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		// The rendered statement may contain a literal password, so report the
		// driver error alone and never the statement.
		return fmt.Errorf("execute rendered statement: %w", err)
	}
	return nil
}
