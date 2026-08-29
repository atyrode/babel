package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/atyrode/babel/internal/config"
)

const storageUsage = `Usage: babel storage <command> [flags]

Commands:
  configure --from-json FILE|-   replace persistent storage configuration
  status                         report persistent storage configuration
  migrate [--from-json FILE|-]   apply pending shared catalog migrations
  verify                         check the configured shared catalog live
  revoke-instance INSTANCE_ID    refuse an instance's leases and publications

Run "babel storage <command> -h" for a command's flags.
`

const storageConfigureUsage = `Usage: babel storage configure --from-json FILE|- [--json]

Reads a complete storage configuration from FILE, or from stdin when FILE is
"-", validates it, and atomically replaces the whole storage.json file.

Flags:
  --from-json FILE|-          complete JSON configuration to install (required)
  --json                      emit {path, repository, host_id} as JSON
`

const storageStatusUsage = `Usage: babel storage status [--json]

Reports the persistent storage configuration and checks the password file's
existence and permissions. This command succeeds when storage is unconfigured.

Flags:
  --json                      emit the report as JSON
`

// storage routes `babel storage <verb>`.
//
// Shared-mode verbs reach PostgreSQL, so the router threads a context: an
// operator interrupting a hung connection attempt must not have to wait out a
// dial timeout.
func (a *app) storage(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "storage requires a subcommand", usage: storageUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, storageUsage)
		return nil
	case "configure":
		return a.storageConfigure(ctx, args[1:])
	case "status":
		return a.storageStatus(args[1:])
	case "migrate":
		return a.storageMigrate(ctx, args[1:])
	case "verify":
		return a.storageVerify(ctx, args[1:])
	case "revoke-instance":
		return a.storageRevokeInstance(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown storage subcommand %q", args[0]), usage: storageUsage}
	}
}

type storageConfigureResult struct {
	Path       string          `json:"path"`
	Repository string          `json:"repository"`
	HostID     string          `json:"host_id"`
	Mode       string          `json:"mode"`
	Catalog    *catalogChecked `json:"catalog,omitempty"`
}

func (a *app) storageConfigure(ctx context.Context, args []string) error {
	c := newCmd("storage configure", storageConfigureUsage)
	fromJSON := c.fs.String("from-json", "", "complete JSON configuration to install")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *fromJSON == "" {
		return c.usagef("storage configure requires --from-json FILE|-")
	}

	cfg, err := a.decodeConfigDocument(*fromJSON)
	if err != nil {
		return err
	}
	if err := config.Validate(cfg); err != nil {
		return err
	}
	// SPEC.md 9: identity, TLS, credential privileges, and schema compatibility
	// are checked *before* the mode-0600 file is replaced, so a document that
	// cannot work never displaces a configuration that does.
	var checked *catalogChecked
	if cfg.Mode == config.ModeShared {
		got, err := a.checkCatalog(ctx, cfg)
		if err != nil {
			return err
		}
		checked = &got
	}
	if err := config.Save(cfg); err != nil {
		return err
	}

	res := storageConfigureResult{
		Path:       Sanitize(config.Path()),
		Repository: Sanitize(cfg.Repository),
		HostID:     Sanitize(cfg.HostID),
		Mode:       storageMode(cfg),
		Catalog:    checked,
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	fmt.Fprintf(a.stdout, "storage configuration written to %s\n", res.Path)
	return nil
}

// storageStatusResult stays an offline report: it describes the configuration
// on disk and never dials PostgreSQL, so it keeps working during an outage and
// when storage is unconfigured. `storage verify` is the live counterpart.
type storageStatusResult struct {
	Path               string `json:"path"`
	Exists             bool   `json:"exists"`
	Mode               string `json:"mode"`
	Repository         string `json:"repository"`
	PasswordFile       string `json:"password_file"`
	PasswordFileExists bool   `json:"password_file_exists"`
	PasswordFileSecure bool   `json:"password_file_secure"`
	HostID             string `json:"host_id"`
	ResticBinary       string `json:"restic_binary"`
	DeploymentID       string `json:"deployment_id,omitempty"`
	InstanceID         string `json:"instance_id,omitempty"`
	CatalogEndpoint    string `json:"catalog_endpoint,omitempty"`
	CatalogUser        string `json:"catalog_user,omitempty"`
	CatalogTLSMode     string `json:"catalog_tls_mode,omitempty"`
	MigrationUser      string `json:"migration_user,omitempty"`
}

func (a *app) storageStatus(args []string) error {
	c := newCmd("storage status", storageStatusUsage)
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}

	cfg, found, err := config.Load()
	if err != nil {
		return err
	}
	res := storageStatusResult{
		Path:         Sanitize(config.Path()),
		Exists:       found,
		Mode:         storageMode(cfg),
		Repository:   Sanitize(cfg.Repository),
		PasswordFile: Sanitize(cfg.PasswordFile),
		HostID:       Sanitize(cfg.HostID),
		ResticBinary: Sanitize(cfg.ResticBinary),
		DeploymentID: Sanitize(cfg.DeploymentID),
		InstanceID:   Sanitize(cfg.InstanceID),
	}
	// The endpoint and role names are reported; the passwords are not read here
	// at all, so no redaction can be forgotten downstream.
	if cfg.Catalog != nil {
		res.CatalogEndpoint = Sanitize(fmt.Sprintf("%s:%d/%s", cfg.Catalog.Host, cfg.Catalog.Port, cfg.Catalog.Database))
		res.CatalogUser = Sanitize(cfg.Catalog.User)
		res.CatalogTLSMode = Sanitize(cfg.Catalog.TLSMode)
		res.MigrationUser = Sanitize(cfg.Catalog.MigrationUser)
	}
	if cfg.PasswordFile != "" {
		info, statErr := os.Stat(cfg.PasswordFile)
		switch {
		case statErr == nil:
			res.PasswordFileExists = true
			res.PasswordFileSecure = info.Mode().Perm()&^os.FileMode(0o600) == 0
			if !res.PasswordFileSecure {
				a.diagf("warning: password file %s has permissions %04o; expected 0600 or stricter\n", Sanitize(cfg.PasswordFile), info.Mode().Perm())
			}
		case errors.Is(statErr, os.ErrNotExist):
			a.diagf("warning: password file %s does not exist\n", Sanitize(cfg.PasswordFile))
		default:
			return fmt.Errorf("inspect password file %s: %w", cfg.PasswordFile, statErr)
		}
	}

	if *asJSON {
		return a.emitJSON(res)
	}
	rows := [][2]string{
		{"path", res.Path},
		{"configured", yesNo(res.Exists, "yes", "no")},
		{"mode", res.Mode},
		{"repository", res.Repository},
		{"password file", res.PasswordFile},
		{"password file exists", yesNo(res.PasswordFileExists, "yes", "no")},
		{"password file secure", yesNo(res.PasswordFileSecure, "yes", "no")},
		{"host id", res.HostID},
		{"restic binary", res.ResticBinary},
	}
	if res.Mode == config.ModeShared {
		rows = append(rows,
			[2]string{"deployment id", res.DeploymentID},
			[2]string{"instance id", res.InstanceID},
			[2]string{"catalog endpoint", res.CatalogEndpoint},
			[2]string{"catalog user", res.CatalogUser},
			[2]string{"catalog tls mode", res.CatalogTLSMode},
		)
		if res.MigrationUser != "" {
			rows = append(rows, [2]string{"migration user", res.MigrationUser})
		}
	}
	return writeDetail(a.stdout, rows)
}
