package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

const storageMigrateUsage = `Usage: babel storage migrate [--from-json FILE|-] [--json]

Applies pending shared catalog migrations. Without --from-json it uses the
configured catalog credential, which is the supported default: Clever Cloud's
managed PostgreSQL cannot create database users, so one credential serves the
whole deployment. With --from-json it takes a separate migration credential
from a document supplied ephemerally, uses it, and never persists it.

Flags:
  --from-json FILE|-          ephemeral document supplying a migration credential
  --json                      emit the result as JSON
`

const storageVerifyUsage = `Usage: babel storage verify [--json]

Checks the configured shared catalog against the live database: whether TLS was
actually negotiated, what privileges the credential is observed to hold, and
whether the schema is compatible with this binary. It reports observations; it
never changes the database or the configuration.

Flags:
  --json                      emit the report as JSON
`

const storageRevokeInstanceUsage = `Usage: babel storage revoke-instance INSTANCE_ID [--json]

Refuses an instance's lease acquisition, renewal, and publication, and
force-expires any lease it holds so another instance can take over immediately.

This is an application-level control, not a database one. Where the provider
cannot issue per-instance credentials, revocation stops a cooperating instance
and bounds a compromised one until it is noticed; it cannot stop a holder of the
shared credential from clearing its own revocation. Fleet-wide credential
rotation and repository-password custody remain the controls for that case.

Flags:
  --json                      emit the result as JSON
`

// tlsObserved is what the server says about the connection, rather than what the
// configuration asked for. A document may request verify-full and still end up
// on a plaintext socket if the server does not offer TLS, so the report reads
// pg_stat_ssl instead of echoing tls_mode back.
type tlsObserved struct {
	Active   bool   `json:"active"`
	Protocol string `json:"protocol,omitempty"`
}

// catalogChecked is one live observation of a configured shared catalog.
//
// RoleSeparation is derived from two observations rather than from
// configuration: supplying a second credential is an operational input and
// proves nothing on its own (SPEC.md 9, decision 46). Separation is reported
// only when the two credentials are observably different roles with observably
// different DDL rights.
type catalogChecked struct {
	Endpoint         string                    `json:"endpoint"`
	TLSMode          string                    `json:"tls_mode"`
	TLS              tlsObserved               `json:"tls"`
	SchemaVersion    int                       `json:"schema_version"`
	SchemaCompatible bool                      `json:"schema_compatible"`
	PendingMigration bool                      `json:"pending_migration"`
	Application      sharedcatalog.Privileges  `json:"application"`
	Migration        *sharedcatalog.Privileges `json:"migration,omitempty"`
	RoleSeparation   bool                      `json:"role_separation"`
}

// storageMode names the mode a document selects, defaulting to local so a
// schema-1 configuration reports the mode it actually behaves as.
func storageMode(cfg config.Config) string {
	if cfg.Mode == "" {
		return config.ModeLocal
	}
	return cfg.Mode
}

// checkCatalog performs the live checks SPEC.md 9 requires of `storage
// configure`, and the same ones `storage verify` reports later.
//
// A fresh deployment has no schema at all, so pending migrations are reported
// rather than treated as failure: refusing here would make configuring a new
// deployment impossible, since migrating requires a configuration. A schema
// that is *newer* than this binary is fatal, because no migration this binary
// runs can reconcile it.
func (a *app) checkCatalog(ctx context.Context, cfg config.Config) (catalogChecked, error) {
	if cfg.Catalog == nil {
		return catalogChecked{}, errors.New("shared mode requires a catalog configuration")
	}
	out := catalogChecked{
		Endpoint: fmt.Sprintf("%s:%d/%s", cfg.Catalog.Host, cfg.Catalog.Port, cfg.Catalog.Database),
		TLSMode:  cfg.Catalog.TLSMode,
	}

	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN())
	if err != nil {
		return catalogChecked{}, err
	}
	defer db.Close()

	if out.TLS, err = observeTLS(ctx, db); err != nil {
		return catalogChecked{}, err
	}
	if out.Application, err = sharedcatalog.DetectPrivileges(ctx, db); err != nil {
		return catalogChecked{}, err
	}
	if out.SchemaVersion, out.SchemaCompatible, out.PendingMigration, err = observeSchema(ctx, db); err != nil {
		return catalogChecked{}, err
	}

	// A configured migration credential must work, or the document is wrong.
	// Its privileges are observed on its own connection, never inferred from
	// the fact that it was supplied.
	if dsn, ok := cfg.Catalog.MigrationDSN(); ok {
		mdb, err := sharedcatalog.Open(ctx, dsn)
		if err != nil {
			return catalogChecked{}, fmt.Errorf("migration credential: %w", err)
		}
		defer mdb.Close()
		got, err := sharedcatalog.DetectPrivileges(ctx, mdb)
		if err != nil {
			return catalogChecked{}, fmt.Errorf("migration credential: %w", err)
		}
		out.Migration = &got
		out.RoleSeparation = got.User != out.Application.User && got.CanDDL && !out.Application.CanDDL
	}
	return out, nil
}

// observeTLS asks the server whether this backend's connection is encrypted.
// pg_stat_ssl describes the caller's own backend, so it needs no privilege
// beyond connecting.
func observeTLS(ctx context.Context, db *sql.DB) (tlsObserved, error) {
	var active bool
	var protocol sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT ssl, version FROM pg_stat_ssl WHERE pid = pg_backend_pid()`).Scan(&active, &protocol)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// No row for our own backend should be impossible on a supported
		// server; report it as unencrypted rather than inventing a protocol.
		return tlsObserved{}, nil
	case err != nil:
		return tlsObserved{}, fmt.Errorf("observe connection encryption: %w", err)
	}
	return tlsObserved{Active: active, Protocol: protocol.String}, nil
}

// observeSchema reports the deployment's recorded schema version, whether this
// binary may operate against it, and whether migrations are pending. A database
// with no schema yet reports version 0 with migrations pending.
func observeSchema(ctx context.Context, db *sql.DB) (version int, compatible, pending bool, err error) {
	err = db.QueryRowContext(ctx, `
		SELECT coalesce(max(schema_version), 0) FROM deployments`).Scan(&version)
	if err != nil {
		// An unmigrated database has no deployments table. Distinguishing that
		// from a real failure by error text is fragile, so ask the catalog.
		var exists bool
		if probe := db.QueryRowContext(ctx, `
			SELECT to_regclass('public.deployments') IS NOT NULL`).Scan(&exists); probe != nil {
			return 0, false, false, fmt.Errorf("read schema version: %w", probe)
		}
		if exists {
			return 0, false, false, fmt.Errorf("read schema version: %w", err)
		}
		return 0, false, true, nil
	}
	switch {
	case version > sharedcatalog.SchemaVersion:
		return version, false, false, fmt.Errorf("%w: database is version %d, this babel implements %d",
			sharedcatalog.ErrSchemaTooNew, version, sharedcatalog.SchemaVersion)
	case version < sharedcatalog.SchemaVersion:
		return version, false, true, nil
	}
	return version, true, false, nil
}

// loadShared reads the configuration and refuses commands that only mean
// something in shared mode.
func loadShared() (config.Config, error) {
	cfg, found, err := config.Load()
	if err != nil {
		return config.Config{}, err
	}
	if !found {
		return config.Config{}, errors.New("storage is not configured: run `babel storage configure --from-json FILE|-`")
	}
	if storageMode(cfg) != config.ModeShared {
		return config.Config{}, fmt.Errorf("storage is configured in %s mode and this command requires shared mode", storageMode(cfg))
	}
	if cfg.Catalog == nil {
		return config.Config{}, errors.New("shared mode requires a catalog configuration")
	}
	return cfg, nil
}

type storageMigrateResult struct {
	Endpoint      string   `json:"endpoint"`
	Applied       []string `json:"applied"`
	SchemaVersion int      `json:"schema_version"`
	Credential    string   `json:"credential"`
}

func (a *app) storageMigrate(ctx context.Context, args []string) error {
	c := newCmd("storage migrate", storageMigrateUsage)
	fromJSON := c.fs.String("from-json", "", "ephemeral document supplying a migration credential")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	cfg, err := loadShared()
	if err != nil {
		return err
	}

	// The default is the configured credential. An ephemeral document overrides
	// it for this invocation only and is never written to storage.json: SPEC.md
	// 2.3 keeps a migration credential off the machine's persistent state.
	dsn := cfg.Catalog.DSN()
	credential := "configured"
	if *fromJSON != "" {
		ephemeral, err := a.decodeConfigDocument(*fromJSON)
		if err != nil {
			return err
		}
		if err := config.Validate(ephemeral); err != nil {
			return err
		}
		if ephemeral.Catalog == nil {
			return errors.New("migration document requires a catalog configuration")
		}
		if migration, ok := ephemeral.Catalog.MigrationDSN(); ok {
			dsn, credential = migration, "ephemeral migration credential"
		} else {
			dsn, credential = ephemeral.Catalog.DSN(), "ephemeral credential"
		}
	}

	db, err := sharedcatalog.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	applied, err := sharedcatalog.Migrate(ctx, db)
	if err != nil {
		return err
	}
	version, _, _, err := observeSchema(ctx, db)
	if err != nil {
		return err
	}

	res := storageMigrateResult{
		Endpoint:      fmt.Sprintf("%s:%d/%s", cfg.Catalog.Host, cfg.Catalog.Port, cfg.Catalog.Database),
		Applied:       applied,
		SchemaVersion: version,
		Credential:    credential,
	}
	if res.Applied == nil {
		res.Applied = []string{}
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if len(res.Applied) == 0 {
		fmt.Fprintf(a.stdout, "shared catalog is up to date at %s\n", Sanitize(res.Endpoint))
		return nil
	}
	fmt.Fprintf(a.stdout, "applied %d migration(s) to %s using the %s:\n",
		len(res.Applied), Sanitize(res.Endpoint), Sanitize(res.Credential))
	for _, v := range res.Applied {
		fmt.Fprintf(a.stdout, "  %s\n", Sanitize(v))
	}
	return nil
}

func (a *app) storageVerify(ctx context.Context, args []string) error {
	c := newCmd("storage verify", storageVerifyUsage)
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	cfg, err := loadShared()
	if err != nil {
		return err
	}
	got, err := a.checkCatalog(ctx, cfg)
	if err != nil {
		return err
	}

	if *asJSON {
		return a.emitJSON(got)
	}
	rows := [][2]string{
		{"endpoint", Sanitize(got.Endpoint)},
		{"tls mode", Sanitize(got.TLSMode)},
		{"tls active", yesNo(got.TLS.Active, "yes", "no")},
		{"tls protocol", Sanitize(got.TLS.Protocol)},
		{"schema version", fmt.Sprint(got.SchemaVersion)},
		{"schema compatible", yesNo(got.SchemaCompatible, "yes", "no")},
		{"pending migration", yesNo(got.PendingMigration, "yes", "no")},
		{"credential", Sanitize(got.Application.User)},
		{"privilege observed", Sanitize(got.Application.Level)},
	}
	if got.Migration != nil {
		rows = append(rows,
			[2]string{"migration credential", Sanitize(got.Migration.User)},
			[2]string{"migration privilege observed", Sanitize(got.Migration.Level)},
		)
	}
	rows = append(rows, [2]string{"role separation observed", yesNo(got.RoleSeparation, "yes", "no")})
	if err := writeDetail(a.stdout, rows); err != nil {
		return err
	}
	if !got.RoleSeparation {
		a.diagf("note: one credential serves this deployment, so no database-level control revokes a single instance; use `babel storage revoke-instance` and fleet-wide rotation\n")
	}
	return nil
}

type storageRevokeResult struct {
	InstanceID string `json:"instance_id"`
	Revoked    bool   `json:"revoked"`
}

func (a *app) storageRevokeInstance(ctx context.Context, args []string) error {
	c := newCmd("storage revoke-instance", storageRevokeInstanceUsage)
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	rest := c.args()
	if len(rest) != 1 {
		return c.usagef("storage revoke-instance takes exactly one INSTANCE_ID")
	}
	instanceID := rest[0]

	cfg, err := loadShared()
	if err != nil {
		return err
	}
	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN())
	if err != nil {
		return err
	}
	defer db.Close()

	if err := sharedcatalog.RevokeInstance(ctx, db, instanceID); err != nil {
		return err
	}

	res := storageRevokeResult{InstanceID: Sanitize(instanceID), Revoked: true}
	if *asJSON {
		return a.emitJSON(res)
	}
	fmt.Fprintf(a.stdout, "instance %s is revoked: its leases are expired and its publications are refused\n", res.InstanceID)
	a.diagf("note: this is an application-level control; a holder of the shared credential can clear it, so rotate credentials if the instance is compromised rather than merely retired\n")
	return nil
}

// decodeConfigDocument reads exactly one JSON document from a file or stdin. It
// is shared by `configure` and `migrate` so both reject trailing data the same
// way, and neither ever echoes the document: it carries credentials.
func (a *app) decodeConfigDocument(from string) (config.Config, error) {
	in := a.stdin
	if from != "-" {
		f, err := os.Open(from)
		if err != nil {
			return config.Config{}, fmt.Errorf("open configuration input %s: %w", from, err)
		}
		defer f.Close()
		in = f
	}

	var cfg config.Config
	dec := json.NewDecoder(in)
	if err := dec.Decode(&cfg); err != nil {
		return config.Config{}, fmt.Errorf("decode configuration input: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return config.Config{}, fmt.Errorf("decode configuration input: %w", err)
	}
	return cfg, nil
}
