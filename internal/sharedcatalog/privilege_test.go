package sharedcatalog_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/sharedcatalog"
)

// Every fixture role logs in with the same synthetic secret. The harness
// cluster trusts loopback, but an external BABEL_TEST_POSTGRES may not, so the
// roles carry a real password and it is scrubbed from any failure message.
const fixtureSecret = "detect-privileges-fixture"

// dropRole removes a fixture role and the grants that would otherwise block the
// drop. Errors are deliberately ignored: this runs both before a fixture is
// created, to clear leftovers from an interrupted run, and after it, when the
// role may already be gone.
func dropRole(db *sql.DB, name string) {
	db.Exec("DROP OWNED BY " + name)
	db.Exec("DROP ROLE IF EXISTS " + name)
}

// newFixtureRole creates a login role with the given attributes and grants, and
// removes it afterwards. Roles are cluster-global while databases are per-test,
// so leaking one would change what a later run detects.
func newFixtureRole(t *testing.T, db *sql.DB, name, attrs string, grants ...string) {
	t.Helper()
	dropRole(db, name)
	t.Cleanup(func() { dropRole(db, name) })

	stmt := fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD '%s' %s", name, fixtureSecret, attrs)
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("create fixture role %s: %v", name, scrubFixtureSecret(err))
	}
	for _, g := range grants {
		if _, err := db.Exec(g); err != nil {
			t.Fatalf("grant to fixture role %s: %v", name, err)
		}
	}
}

func scrubFixtureSecret(err error) string {
	return strings.ReplaceAll(err.Error(), fixtureSecret, "[redacted]")
}

func detect(t *testing.T, db *sql.DB) sharedcatalog.Privileges {
	t.Helper()
	p, err := sharedcatalog.DetectPrivileges(context.Background(), db)
	if err != nil {
		t.Fatalf("DetectPrivileges: %v", err)
	}
	return p
}

// The migration credential of the harness cluster is a superuser, which is the
// strongest case the ladder reports.
func TestDetectPrivilegesReportsSuperuser(t *testing.T) {
	db := newDB(t)
	p := detect(t, db)

	if p.Level != sharedcatalog.PrivilegeSuperuser {
		t.Errorf("Level = %q, want %q", p.Level, sharedcatalog.PrivilegeSuperuser)
	}
	if !p.Superuser || !p.CanCreateRole || !p.CanCreateDB || !p.CanDDL {
		t.Errorf("superuser reported as limited: %+v", p)
	}
}

// The Clever Cloud case: one credential for the whole deployment with no DDL
// and no way to issue another. Reporting anything stronger here would let
// operator-facing output claim per-instance revocable credentials that do not
// exist (SPEC.md 9, decision 46).
func TestDetectPrivilegesReportsApplicationLevel(t *testing.T) {
	db := newDB(t)
	const role = "babel_priv_app"

	// PostgreSQL 15 and later already withhold CREATE on public from PUBLIC;
	// revoking explicitly keeps the fixture honest on an older external server.
	newFixtureRole(t, db, role, "NOSUPERUSER NOCREATEDB NOCREATEROLE",
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		"REVOKE CREATE ON SCHEMA public FROM "+role,
		"GRANT USAGE ON SCHEMA public TO "+role,
	)

	p := detect(t, openAs(t, db, role, fixtureSecret))
	if p.Level != sharedcatalog.PrivilegeApplication {
		t.Errorf("Level = %q, want %q (%+v)", p.Level, sharedcatalog.PrivilegeApplication, p)
	}
	if p.Superuser || p.CanCreateRole || p.CanCreateDB || p.CanDDL {
		t.Errorf("application credential over-reported: %+v", p)
	}
	// Two observations are only comparable if each names the role it observed.
	if p.User != role {
		t.Errorf("User = %q, want %q", p.User, role)
	}
}

// CREATE on the schema is what a migration credential needs, and it is the only
// thing separating the migration case from the application case where the
// provider issues no second user.
func TestDetectPrivilegesReportsDDLLevel(t *testing.T) {
	db := newDB(t)
	const role = "babel_priv_ddl"

	newFixtureRole(t, db, role, "NOSUPERUSER NOCREATEDB NOCREATEROLE",
		"GRANT USAGE, CREATE ON SCHEMA public TO "+role,
	)

	p := detect(t, openAs(t, db, role, fixtureSecret))
	if p.Level != sharedcatalog.PrivilegeDDL {
		t.Errorf("Level = %q, want %q (%+v)", p.Level, sharedcatalog.PrivilegeDDL, p)
	}
	if !p.CanDDL {
		t.Error("CanDDL is false for a role holding CREATE on its schema")
	}
	if p.Superuser || p.CanCreateRole || p.CanCreateDB {
		t.Errorf("DDL credential over-reported: %+v", p)
	}
}

// Only a role-creating credential can give each instance a distinct revocable
// credential, so it must be distinguishable from a mere DDL credential.
func TestDetectPrivilegesReportsRoleCreating(t *testing.T) {
	db := newDB(t)
	const role = "babel_priv_creator"

	newFixtureRole(t, db, role, "NOSUPERUSER NOCREATEDB CREATEROLE",
		"GRANT USAGE ON SCHEMA public TO "+role,
	)

	p := detect(t, openAs(t, db, role, fixtureSecret))
	if p.Level != sharedcatalog.PrivilegeRoleCreating {
		t.Errorf("Level = %q, want %q (%+v)", p.Level, sharedcatalog.PrivilegeRoleCreating, p)
	}
	if !p.CanCreateRole {
		t.Error("CanCreateRole is false for a CREATEROLE credential")
	}
	if p.Superuser {
		t.Errorf("CREATEROLE reported as superuser: %+v", p)
	}
}

// Role attributes are never inherited, so a member of a superuser role reads as
// unprivileged in pg_roles while one SET ROLE makes it superuser. Detection must
// report the escalation, not the attribute.
//
// Both membership kinds are covered because they are what distinguishes the two
// candidate checks. Observed on PostgreSQL 18 for a member of a superuser role:
// rolsuper is false either way, pg_has_role USAGE is true for the INHERIT
// member but false for the NOINHERIT one, and MEMBER is true for both. A USAGE
// check would therefore report a NOINHERIT member of a superuser role as an
// application credential while SET ROLE is one statement away.
func TestDetectPrivilegesSeesSuperuserViaMembership(t *testing.T) {
	db := newDB(t)
	const parent = "babel_priv_super"

	dropRole(db, parent)
	t.Cleanup(func() { dropRole(db, parent) })
	if _, err := db.Exec("CREATE ROLE " + parent + " NOLOGIN SUPERUSER"); err != nil {
		t.Fatalf("create superuser fixture role: %v", err)
	}

	for _, tc := range []struct{ name, role, inherit string }{
		{"inherit", "babel_priv_heir_inh", "INHERIT"},
		{"noinherit", "babel_priv_heir_noinh", "NOINHERIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newFixtureRole(t, db, tc.role,
				"NOSUPERUSER NOCREATEDB NOCREATEROLE "+tc.inherit+" IN ROLE "+parent,
				"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
				"REVOKE CREATE ON SCHEMA public FROM "+tc.role,
				"GRANT USAGE ON SCHEMA public TO "+tc.role,
			)

			p := detect(t, openAs(t, db, tc.role, fixtureSecret))
			if p.Level != sharedcatalog.PrivilegeSuperuser {
				t.Errorf("Level = %q, want %q for a %s member of a superuser role (%+v)",
					p.Level, sharedcatalog.PrivilegeSuperuser, tc.inherit, p)
			}
			if !p.Superuser {
				t.Error("member of a superuser role reported as not superuser")
			}
			// Nothing grants this role CREATE on the schema, but a superuser
			// bypasses permission checks, so reporting CanDDL false would be a lie.
			if !p.CanDDL || !p.CanCreateRole {
				t.Errorf("effective superuser reported as unable to migrate or issue roles: %+v", p)
			}
		})
	}
}
