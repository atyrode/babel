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
	"github.com/atyrode/babel/internal/restic"
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

const storageRebuildUsage = `Usage: babel storage rebuild --host HOST --yes [flags]

Rebuilds one host's catalog rows from the repository's snapshot list, discarding
what the catalog held for that host. The repository is never touched, and no
snapshot it still reports is ever dropped.

This is the repair path for a catalog whose rows for a host are wrong or lost.
It is not needed for an empty catalog: after "storage migrate", each host's next
"archive push" registers and reconciles its own snapshots, which is the ordinary
recovery and the one the acceptance suite exercises.

What comes back is what the snapshot list can support: snapshot identity,
ordering rederived from restic's recorded times, and restic's counts where the
listing carries them. Session rows cannot be rebuilt from a listing, because
their sizes and counts are read from the sessions themselves, so the rebuilt
snapshots arrive "catalog-pending" and session identity returns with the owning
host's next push (SPEC.md 9).

That makes this the wrong tool for filling in session title, workspace, and
continuation grade: it deletes the host's session rows, metadata included, and
cannot reconstruct any of it. Those values arrive only from the owning host's
next push. The host's own identity - display name, operating system,
architecture, and first-seen time - does survive a rebuild, because this
command has no way to know another machine's facts and so asserts none.

--host is required rather than defaulting to this machine, because rebuilding
discards derived rows and the wrong host would be a silent loss. --yes is
required for the same reason.

Flags:
  --host ID                   host whose catalog rows to rebuild
  --yes                       confirm discarding that host's derived rows
  --repo REPOSITORY           restic repository (default $BABEL_RESTIC_REPO)
  --password-file FILE        password file (default $BABEL_RESTIC_PASSWORD_FILE)
  --restic-binary PATH        restic executable (default "restic" from $PATH)
  --json                      emit the report as JSON
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

	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN(), sharedcatalog.WithMaxConnections(cfg.Catalog.MaxConnections))
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
		mdb, err := sharedcatalog.Open(ctx, dsn, sharedcatalog.WithMaxConnections(cfg.Catalog.MaxConnections))
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

// schemaVersionLabel renders the recorded deployment version. Zero is not a
// version: it means no deployment has published into this catalog yet, which
// reads as an error if printed bare next to a compatible schema.
func schemaVersionLabel(version int) string {
	if version == 0 {
		return "not recorded yet"
	}
	return fmt.Sprint(version)
}

// observeSchema reports three separate facts that are easy to conflate.
//
// Pending migrations come from the migration ledger. The deployment's recorded
// schema_version answers a different question - which version the fleet
// bootstrapped as - and it is written at first publication rather than by
// migrating, so a version of 0 means "no deployment has published yet", not
// "unmigrated". Reading it as the latter reported a fully migrated catalog as
// pending, which is what this shape fixes.
//
// A recorded version newer than this binary is fatal: no migration this binary
// runs can reconcile it.
func observeSchema(ctx context.Context, db *sql.DB) (version int, compatible, pending bool, err error) {
	pendingList, err := sharedcatalog.PendingMigrations(ctx, db)
	if err != nil {
		return 0, false, false, err
	}
	pending = len(pendingList) > 0

	// The existence probe must name Babel's own schema. `public.deployments`
	// never exists - every catalog object lives in `babel` and every connection
	// pins search_path to it - so this probe always failed and the recorded
	// version always read as 0, reporting a registered deployment as "not
	// recorded yet" (observed against the real add-on, 2026-08-30). The
	// table name is a parameter for the same reason EnsureCompatible passes one.
	var recorded bool
	if err := db.QueryRowContext(ctx,
		`SELECT to_regclass($1) IS NOT NULL`, sharedcatalog.Schema+".deployments").Scan(&recorded); err != nil {
		return 0, false, pending, fmt.Errorf("look for the deployment table: %w", err)
	}
	if recorded {
		if err := db.QueryRowContext(ctx, `
			SELECT coalesce(max(schema_version), 0) FROM deployments`).Scan(&version); err != nil {
			return 0, false, pending, fmt.Errorf("read schema version: %w", err)
		}
	}
	if version > sharedcatalog.SchemaVersion {
		return version, false, pending, fmt.Errorf("%w: database is version %d, this babel implements %d",
			sharedcatalog.ErrSchemaTooNew, version, sharedcatalog.SchemaVersion)
	}
	return version, !pending, pending, nil
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
	// The pool ceiling describes the server, not the credential, so the
	// configured document's stands unless the ephemeral one states its own.
	maxConns := cfg.Catalog.MaxConnections
	if *fromJSON != "" {
		// A migration document is read for exactly one thing: the credential
		// this invocation connects with. Every other field it carries is
		// ignored, a payload key ring included — this command persists nothing
		// by contract, and installing keys from an ephemeral document would
		// make the one command that promises to write nothing write the most
		// consequential file Babel has. `babel storage configure` installs a
		// ring.
		ephemeral, err := a.decodeConfigDocument(*fromJSON)
		if err != nil {
			return err
		}
		if err := config.Validate(ephemeral.Config); err != nil {
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
		if ephemeral.Catalog.MaxConnections > 0 {
			maxConns = ephemeral.Catalog.MaxConnections
		}
	}

	db, err := sharedcatalog.Open(ctx, dsn, sharedcatalog.WithMaxConnections(maxConns))
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
		{"schema version", schemaVersionLabel(got.SchemaVersion)},
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
		a.diagf("note: one credential serves this deployment, so no database-level control evicts a single instance; fleet-wide credential rotation and repository-password custody are the controls\n")
	}
	return nil
}

// decodeConfigDocument reads exactly one JSON document from a file or stdin. It
// is shared by `configure` and `migrate` so both reject trailing data the same
// way, and neither ever echoes the document: it carries credentials, and since
// the ceremony grew a payload key ring (#112) it can carry key material too.
//
// The whole document is decoded here, ring included; what each caller does with
// the halves is the caller's contract. `configure` installs both, `migrate`
// reads a credential and persists nothing.
func (a *app) decodeConfigDocument(from string) (config.ConfigureDocument, error) {
	in := a.stdin
	if from != "-" {
		f, err := os.Open(from)
		if err != nil {
			return config.ConfigureDocument{}, fmt.Errorf("open configuration input %s: %w", from, err)
		}
		defer f.Close()
		in = f
	}

	var doc config.ConfigureDocument
	dec := json.NewDecoder(in)
	if err := dec.Decode(&doc); err != nil {
		return config.ConfigureDocument{}, fmt.Errorf("decode configuration input: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return config.ConfigureDocument{}, fmt.Errorf("decode configuration input: %w", err)
	}
	return doc, nil
}

// rebuildResult is the machine-readable outcome of a host-scoped catalog
// rebuild. Rebuilt counts snapshots reinserted from the repository listing; it
// is the whole of that host's catalog state afterwards, because a rebuild
// discards what was there rather than merging into it.
type rebuildResult struct {
	Host     string `json:"host"`
	Rebuilt  int    `json:"rebuilt"`
	Sessions int    `json:"sessions"`
}

func (a *app) storageRebuild(ctx context.Context, args []string) error {
	c := newCmd("storage rebuild", storageRebuildUsage)
	var rf repoFlags
	rf.bind(c.fs)
	yes := c.fs.Bool("yes", false, "confirm discarding the host's derived rows")
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if rf.host == "" {
		return c.usagef("storage rebuild requires --host ID: it discards that host's catalog rows")
	}
	if !*yes {
		return c.usagef("storage rebuild discards host %q's catalog rows; pass --yes to confirm", rf.host)
	}

	cfg, err := loadShared()
	if err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}
	repo, err := rf.open(c, d, &sanitizingWriter{w: a.stderr, prefix: "restic: "})
	if err != nil {
		return err
	}
	// The repository is the authority a rebuild reads from, so its absence is a
	// refusal rather than something to create: rebuilding from a repository this
	// command had just made would empty the host's catalog rows.
	if err := repo.Require(ctx); err != nil {
		if errors.Is(err, restic.ErrRepoMissing) {
			return fmt.Errorf("no repository at %s: nothing to rebuild from", Sanitize(rf.repository))
		}
		return fmt.Errorf("open repository: %w", err)
	}

	listing, err := repo.Snapshots(ctx)
	if err != nil {
		return fmt.Errorf("list the repository's snapshots: %w", err)
	}
	// snapshotsForHost is the same host resolution cross-host fetch uses, so a
	// mistyped host is an error naming the hosts that exist rather than a rebuild
	// to empty - which is what an unfiltered listing would produce.
	if _, err := snapshotsForHost(c, listing, rf.host); err != nil {
		return err
	}
	// hostSnapshots restates them in the catalog's terms, carrying restic's own
	// counts where the snapshot record has them. Rebuild refuses a listing
	// attributed to another host, and one repository holds every machine's.
	rows := hostSnapshots(listing, rf.host)

	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN(), sharedcatalog.WithMaxConnections(cfg.Catalog.MaxConnections))
	if err != nil {
		return fmt.Errorf("reach the shared catalog: %w", err)
	}
	defer db.Close()

	rep, err := sharedcatalog.Rebuild(ctx, db, cfg.DeploymentID, rf.host, rows)
	if err != nil {
		return fmt.Errorf("rebuild the catalog: %w", err)
	}

	res := rebuildResult{Host: Sanitize(rf.host), Rebuilt: rep.Added, Sessions: 0}
	if *asJSON {
		return a.emitJSON(res)
	}
	if err := writeDetail(a.stdout, [][2]string{
		{"host", res.Host},
		{"snapshots rebuilt", fmt.Sprint(res.Rebuilt)},
		{"session rows", fmt.Sprint(res.Sessions)},
	}); err != nil {
		return err
	}
	a.diagf("note: session identity is not derivable from a snapshot listing; these snapshots are catalog-pending until host %s pushes again\n",
		Sanitize(rf.host))
	return nil
}
