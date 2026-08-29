package sharedcatalog

import (
	"context"
	"database/sql"
	"fmt"
)

// Privilege levels, strongest first. Level is the one line an operator reads,
// so the ladder names what shared mode actually depends on: whether the
// credential can issue per-instance credentials (role-creating), whether it can
// change the schema (ddl), or whether it can only run the application.
const (
	PrivilegeSuperuser    = "superuser"
	PrivilegeRoleCreating = "role-creating"
	PrivilegeDDL          = "ddl"
	PrivilegeApplication  = "application"
)

// Privileges is what one connection's credential actually carries. Every field
// is observed, never configured, so a report built from it cannot claim a
// capability nobody saw.
//
// User names the observed role so two observations are comparable: a caller
// holding both an application and a migration credential can detect one with
// each and derive whether they are genuinely different credentials with
// different capabilities. That derivation belongs to the caller, because it
// needs two connections and this describes one.
type Privileges struct {
	Level         string `json:"level"`
	Superuser     bool   `json:"superuser"`
	CanCreateRole bool   `json:"can_create_role"`
	CanCreateDB   bool   `json:"can_create_db"`
	CanDDL        bool   `json:"can_ddl"`
	User          string `json:"user"`
}

// privilegeQuery asks PostgreSQL what the connected credential can do rather
// than attempting DDL to find out, because a probe that creates or drops
// anything is a probe that can damage a live catalog.
//
// Role attributes are not inherited: a member of a superuser role does not
// silently act as superuser, but it may SET ROLE to one at any moment, and
// SET ROLE needs only MEMBER - it works for a NOINHERIT member too. So the
// attribute lookups ask MEMBER, not USAGE: USAGE would report a NOINHERIT
// member of a superuser role as unprivileged when one statement makes it
// superuser. A role is a member of itself, so each EXISTS covers both the
// credential's own attributes and every attribute it can reach by escalation.
//
// The schema check is an ACL check, not an attribute, so it uses
// has_schema_privilege, which already accounts for inherited membership. Its
// blind spot is the mirror image of the above - a NOINHERIT member of a role
// holding CREATE reads as false - and DetectPrivileges closes the case that
// matters by treating effective superuser as implying DDL.
const privilegeQuery = `
SELECT
  current_user::text,
  EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolsuper      AND pg_has_role(r.oid, 'MEMBER')),
  EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolcreaterole AND pg_has_role(r.oid, 'MEMBER')),
  EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolcreatedb   AND pg_has_role(r.oid, 'MEMBER')),
  CASE WHEN current_schema() IS NULL THEN false
       ELSE has_schema_privilege(current_user, current_schema(), 'CREATE') END`

// DetectPrivileges reports the privilege level the connected catalog credential
// actually holds, so Babel records what the provider granted instead of
// assuming the stronger case (SPEC.md 9, decision 46; the reported-not-assumed
// requirement is a pre-deployment gate in SPEC.md 14).
//
// This exists because the two shared-mode deployments differ in what they can
// enforce, and only one of them can revoke an instance at the database. Clever
// Cloud's managed PostgreSQL cannot create database users, so one credential
// serves the whole deployment and no database-level control evicts a single
// instance. Detection keeps operator-facing output honest about which case is
// in force: a deployment whose only credential reports `application` must not
// be described as having per-instance revocable credentials, and eviction there
// is the application-level instance revocation of SPEC.md 11.
//
// It describes the credential this connection authenticated with and nothing
// else. It cannot see whether the deployment also holds a separate migration
// credential, or what that one carries, so a claim about role separation is
// never a single result: it requires observing each credential's own
// connection and comparing them.
//
// A result is a point-in-time observation, not a guarantee. An operator or the
// provider can grant or revoke privileges between this call and the next
// statement, so this may be used to report and to refuse, never to conclude
// that a later operation is safe.
func DetectPrivileges(ctx context.Context, db *sql.DB) (Privileges, error) {
	var user string
	var super, createRole, createDB, schemaCreate bool
	if err := db.QueryRowContext(ctx, privilegeQuery).
		Scan(&user, &super, &createRole, &createDB, &schemaCreate); err != nil {
		// The query carries no arguments and names no credential, so the driver
		// error is safe to return as-is.
		return Privileges{}, fmt.Errorf("detect catalog credential privileges: %w", err)
	}

	// A superuser bypasses every permission check, so reporting it as unable to
	// create a role or change the schema would be a lie the ladder then repeats.
	p := Privileges{
		Superuser:     super,
		CanCreateRole: super || createRole,
		CanCreateDB:   super || createDB,
		CanDDL:        super || schemaCreate,
		User:          user,
	}

	// CREATEDB does not move the summary: it grants no per-instance credential
	// and no change to Babel's schema. It is reported because a credential with
	// it is stronger than the deployment needs, and the field is the place an
	// operator sees that.
	switch {
	case p.Superuser:
		p.Level = PrivilegeSuperuser
	case p.CanCreateRole:
		p.Level = PrivilegeRoleCreating
	case p.CanDDL:
		p.Level = PrivilegeDDL
	default:
		p.Level = PrivilegeApplication
	}
	return p, nil
}
