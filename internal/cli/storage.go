package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/atyrode/babel/internal/config"
)

const storageUsage = `Usage: babel storage <command> [flags]

Commands:
  configure --from-json FILE|-   replace persistent storage configuration
  status                         report persistent storage configuration
  migrate [--from-json FILE|-]   apply pending shared catalog migrations
  verify                         check the configured shared catalog live
  rebuild --host ID --yes        rebuild one host's catalog rows from the repository

Run "babel storage <command> -h" for a command's flags.
`

const storageConfigureUsage = `Usage: babel storage configure --from-json FILE|- [--json]

Reads a complete storage configuration from FILE, or from stdin when FILE is
"-", validates it, and atomically replaces the whole storage.json file.

A minimal local-mode document — the smallest thing that validates:

  {"repository": "/srv/babel/repo", "password_file": "/etc/babel/repo-password"}

repository and password_file are the only required names, and password_file
must be absolute. Everything else is optional: config_schema (the current
schema is written back regardless), mode ("local" by default, or "shared"),
host_id, restic_binary, and repository_store {access_key_id,
secret_access_key} — which an "s3:" repository does require. Shared mode
additionally requires deployment_id, instance_id, and a catalog object;
config.Config and config.Validate in internal/config define those fields and
every rule this command enforces.

One catalog field is worth naming here because a provider limit is what
surfaces it: max_connections caps the pool this instance opens, and omitting it
takes Babel's default of four. A managed plan that allows one role fewer
connections than the fleet opens needs it — Clever Cloud's DEV PostgreSQL
allows five in total, so two instances at the default cannot both connect, and
"too many connections for role" is what an operator sees instead.

Unknown names are ignored by design, so a document written by a compatible
newer Babel stays readable — which also means a misspelled name is dropped
silently rather than refused. Read back what was actually installed with
"babel storage status".

The document may also carry payload_keys: this deployment's whole append-only
payload key ring, as {"active_key_id": ID, "keys": [{"key_id": ID, "key":
BASE64}]}. It is installed into the mode-0600 payload key document beside
storage.json — storage.json itself never holds key material — and the install
is a union. A key this machine already holds is never dropped because the
document omitted it, a delivered key id whose material differs from the one
held here is refused outright, and a document carrying no payload_keys at all
leaves this machine's keys exactly as they are. That makes rotation a
re-provision: add the new key to the ring in custody, name it active there,
and every host seals under it while every host keeps opening what it already
had.

Flags:
  --from-json FILE|-          complete JSON configuration to install (required)
  --json                      emit {path, repository, host_id, payload_keys} as JSON
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
	case "rebuild":
		return a.storageRebuild(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown storage subcommand %q", args[0]), usage: storageUsage}
	}
}

type storageConfigureResult struct {
	Path        string                `json:"path"`
	Repository  string                `json:"repository"`
	HostID      string                `json:"host_id"`
	Mode        string                `json:"mode"`
	Catalog     *catalogChecked       `json:"catalog,omitempty"`
	PayloadKeys *payloadKeysInstalled `json:"payload_keys,omitempty"`
}

// payloadKeysInstalled is what this ceremony did to the machine's payload key
// ring: key ids and one path, and nothing else it could possibly carry.
//
// SPEC.md §9 admits a key id in plaintext beside every ciphertext it selects,
// so ids are reportable; key material is the one value in Babel that reaches no
// report, no diagnostic and no error. A result type that cannot hold material
// is how that stays true of this command as it grows.
type payloadKeysInstalled struct {
	Path               string   `json:"path"`
	Added              []string `json:"added"`
	AbsentFromDocument []string `json:"absent_from_document"`
	ActiveKeyID        string   `json:"active_key_id"`
	Changed            bool     `json:"changed"`
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

	doc, err := a.decodeConfigDocument(*fromJSON)
	if err != nil {
		return err
	}
	if err := doc.Validate(); err != nil {
		return err
	}
	cfg := doc.Config
	// Setup is when an unsafe password file is cheapest to fix, so configure
	// says what `storage status` would say later — through the same check, so
	// the two commands can never disagree about whether a file is safe.
	//
	// This warns rather than refuses. A repository password other local
	// accounts can read defeats the archive's confidentiality, but refusing to
	// install an otherwise valid document would strand an operator mid-setup
	// with no configuration at all, which is harder to recover from than a
	// chmod. The finding is named with its remedy, and `storage status` keeps
	// reporting it until it is fixed.
	if cfg.PasswordFile != "" {
		if _, err := a.checkPasswordFile(cfg.PasswordFile); err != nil {
			// An uninspectable password file is reported for the same reason
			// and on the same terms: it is a fact about the machine, not a
			// defect in the document being installed.
			a.diagf("warning: %s\n", Sanitize(err.Error()))
		}
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
	// The ring is installed before the configuration is replaced, because the
	// one refusal this can produce must not cost the machine a configuration
	// that works: a delivered key id whose material differs from the one held
	// here is a fork of the deployment's key space and is refused rather than
	// resolved. Everything else the install does is monotone — it adds keys and
	// never drops one — so a host that gains keys and then fails to gain a
	// configuration has lost nothing.
	installed, err := a.installPayloadKeys(doc)
	if err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}

	res := storageConfigureResult{
		Path:        Sanitize(config.Path()),
		Repository:  Sanitize(cfg.Repository),
		HostID:      Sanitize(cfg.HostID),
		Mode:        storageMode(cfg),
		Catalog:     checked,
		PayloadKeys: installed,
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	fmt.Fprintf(a.stdout, "storage configuration written to %s\n", res.Path)
	if installed != nil {
		switch {
		case !installed.Changed:
			fmt.Fprintf(a.stdout, "payload key ring at %s already carries every key the document delivers\n",
				installed.Path)
		case len(installed.Added) > 0:
			fmt.Fprintf(a.stdout, "payload key ring at %s gained %s; new records seal under %s\n",
				installed.Path, strings.Join(installed.Added, ", "), installed.ActiveKeyID)
		default:
			fmt.Fprintf(a.stdout, "payload key ring at %s now seals new records under %s\n",
				installed.Path, installed.ActiveKeyID)
		}
	}
	return nil
}

// installPayloadKeys installs the ring the document delivered, and reports
// nothing whatsoever when it carried none.
//
// Silence on the empty case is deliberate. Every document written before this
// field existed carries no ring, as does every deployment that keeps its keys
// somewhere this ceremony does not reach, and a `storage configure` that
// learned to warn about an unchanged machine would train an operator to ignore
// its diagnostics.
func (a *app) installPayloadKeys(doc config.ConfigureDocument) (*payloadKeysInstalled, error) {
	if doc.PayloadKeys == nil {
		return nil, nil
	}
	got, err := config.InstallPayloadKeys(*doc.PayloadKeys)
	if err != nil {
		return nil, err
	}
	res := &payloadKeysInstalled{
		Path:               Sanitize(got.Path),
		Added:              sanitizeAll(got.Added),
		AbsentFromDocument: sanitizeAll(got.AbsentFromDocument),
		ActiveKeyID:        Sanitize(got.ActiveKeyID),
		Changed:            got.Changed,
	}
	// Empty rather than absent, on the same terms as `storage migrate`'s applied
	// list: a provisioning script branches on these, and "no keys were added" is
	// an answer it should be able to iterate over rather than a null it has to
	// special-case.
	if res.Added == nil {
		res.Added = []string{}
	}
	if res.AbsentFromDocument == nil {
		res.AbsentFromDocument = []string{}
	}
	// The one thing this command can observe that the operator has to act on:
	// keys held here that no document carries. They are kept — dropping one
	// orphans every object sealed under it forever — but as far as anything
	// here knows they exist on this disk alone.
	//
	// Babel stays vault-agnostic (SPEC.md decisions 38, 50, 51): it never
	// learns what a vault is, so this names the document field and the file to
	// copy from, and leaves naming the custodian to whatever runs the ceremony.
	// It names the file rather than printing the ring, because the material
	// reaches no stream.
	if len(res.AbsentFromDocument) > 0 {
		a.diagf("warning: this host holds payload key(s) %s that the delivered document does not carry; "+
			"they are kept, and as far as this document knows they exist on this disk alone — "+
			"copy the ring from %s into the document's \"payload_keys\" field, or every record sealed "+
			"under them stays unreadable on every other host and unrecoverable if this one is lost\n",
			strings.Join(res.AbsentFromDocument, ", "), res.Path)
	}
	return res, nil
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
		state, err := a.checkPasswordFile(cfg.PasswordFile)
		if err != nil {
			return err
		}
		res.PasswordFileExists, res.PasswordFileSecure = state.exists, state.secure
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

// passwordFileState is everything an offline inspection can say about the
// repository password file.
type passwordFileState struct {
	exists bool
	secure bool
}

// checkPasswordFile inspects the repository password file, reporting what is
// wrong with it on stderr and naming the remedy.
//
// `storage status` and `storage configure` share this one implementation
// deliberately: a permission rule written twice drifts, and two commands
// disagreeing about whether a password file is safe is worse than either
// answer alone. Secure means no permission bit outside 0600 is set — restic
// derives the repository key from this file and nothing else, so any other
// local account that can read it can read the whole archive.
//
// A stat failure that is not "absent" is returned rather than warned about,
// because whether an uninspectable password file is fatal is the caller's
// call: status reports on the machine and fails, configure installs a
// document and does not.
func (a *app) checkPasswordFile(path string) (passwordFileState, error) {
	info, err := os.Stat(path)
	switch {
	case err == nil:
		state := passwordFileState{exists: true, secure: info.Mode().Perm()&^os.FileMode(0o600) == 0}
		if !state.secure {
			a.diagf("warning: password file %s has permissions %04o; expected 0600 or stricter: run `chmod 600 %s`\n",
				Sanitize(path), info.Mode().Perm(), Sanitize(path))
		}
		return state, nil
	case errors.Is(err, os.ErrNotExist):
		a.diagf("warning: password file %s does not exist\n", Sanitize(path))
		return passwordFileState{}, nil
	default:
		return passwordFileState{}, fmt.Errorf("inspect password file %s: %w", path, err)
	}
}
