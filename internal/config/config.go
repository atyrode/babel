// Package config owns Babel's persistent storage configuration.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	currentSchema = 2
	maxHostIDLen  = 64
)

// Babel's supported deployment modes. Local mode keeps every byte on this
// machine; shared mode adds one PostgreSQL catalog shared by every authorized
// instance of a deployment.
const (
	ModeLocal  = "local"
	ModeShared = "shared"
)

// The accepted catalog TLS modes, in PostgreSQL's sslmode vocabulary. Only
// these two are accepted: the weaker modes let a connection fall back to
// plaintext.
const (
	TLSRequire    = "require"
	TLSVerifyFull = "verify-full"
)

// redactedPlaceholder replaces a secret in a Config safe to show. It is a
// fixed string so status output never reveals a secret's length.
const redactedPlaceholder = "[redacted]"

// Config is the complete persistent repository selection stored in
// storage.json.
//
// Schema 2 added mode, deployment/instance identity, and the shared catalog.
// Compatibility is deliberately narrow: a schema-1 document has no mode and
// loads as local, unknown fields are ignored so a compatible newer writer's
// document stays readable, and a schema newer than this build is refused
// outright rather than guessed at. Save always writes schema 2 with an
// explicit mode.
type Config struct {
	ConfigSchema int    `json:"config_schema"`
	Mode         string `json:"mode,omitempty"`
	Repository   string `json:"repository"`
	PasswordFile string `json:"password_file"`
	HostID       string `json:"host_id,omitempty"`
	ResticBinary string `json:"restic_binary,omitempty"`

	// DeploymentID and InstanceID name the shared deployment and this
	// instance within it. They are required in shared mode: coordination
	// rows and host leases are keyed by them.
	DeploymentID string `json:"deployment_id,omitempty"`
	InstanceID   string `json:"instance_id,omitempty"`

	// RepositoryStore carries the object-store credentials an S3-compatible
	// repository locator needs. It is absent for a local-path repository, which
	// needs none.
	RepositoryStore *RepositoryStore `json:"repository_store,omitempty"`

	// Catalog is present exactly in shared mode.
	Catalog *Catalog `json:"catalog,omitempty"`
}

// RepositoryStore is the credential an object-store repository needs.
//
// It lives inline in the document for the same reason Catalog's password does:
// dotfiles pipes one document over stdin and Babel writes it to one mode-0600
// file, so a second secret file would be a second thing to place, back up, and
// rotate (decision 38, operator decision 2026-08-29).
//
// The field names are the S3 vocabulary rather than any provider's, because
// compatibility is S3-plus-PostgreSQL and not Clever Cloud's APIs (decision 36).
// These reach the restic child process as AWS_ACCESS_KEY_ID and
// AWS_SECRET_ACCESS_KEY, which is the only mechanism restic offers for object
// store credentials - it accepts no file reference for them, unlike the
// repository password.
type RepositoryStore struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// Catalog is the shared PostgreSQL connection this instance uses.
//
// User/Password are the one credential the whole deployment shares by default:
// Clever Cloud's managed PostgreSQL cannot create database users (provider
// confirmation, 2026-08-28). MigrationUser/MigrationPassword are the optional
// separate schema-change credential a provider that does issue users can
// supply; where they are absent, schema change is restrained by operator
// procedure rather than by privilege.
type Catalog struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`

	// TLSMode is a PostgreSQL sslmode: "require" encrypts without
	// authenticating the server, "verify-full" also checks the certificate
	// chain and hostname against TLSRootCAFile or the system roots.
	// "require" is the conservative default because whether a given managed
	// provider works under "verify-full" is a per-provider fact Babel does
	// not assume.
	TLSMode       string `json:"tls_mode"`
	TLSRootCAFile string `json:"tls_root_ca_file,omitempty"`

	// MaxConnections caps the connection pool this instance opens against the
	// catalog. Zero means the built-in default (sharedcatalog.Open's four),
	// which is what an omitted field has always meant and what every existing
	// document keeps.
	//
	// It is a per-provider fact, like TLSMode: a managed plan may cap
	// connections per role well below what a fleet of instances would open at
	// the default. Clever Cloud's DEV PostgreSQL plan allows one role five
	// connections in total (measured against the real add-on, 2026-08-31,
	// issue #20), so two instances at four each cannot both be up. Recording
	// the ceiling in the deployment document is what lets one document
	// describe a deployment that fits its provider, instead of the provider
	// deciding which of two instances gets to connect.
	MaxConnections int `json:"max_connections,omitempty"`

	MigrationUser     string `json:"migration_user,omitempty"`
	MigrationPassword string `json:"migration_password,omitempty"`
}

// Path returns the location of Babel's persistent storage configuration.
func Path() string {
	path, _ := pathName()
	return path
}

// Load reads storage.json. A missing file is not an error. Unknown fields are
// ignored so an older Babel can read configuration written by a compatible
// newer version, and an absent mode - every schema-1 document - is local.
//
// Decode errors name the path and the offending field, never a value: a
// storage document holds credentials.
func Load() (Config, bool, error) {
	path, err := pathName()
	if err != nil {
		return Config{}, false, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, false, nil
	}
	if err != nil {
		return Config{}, false, fmt.Errorf("open storage configuration %s: %w", path, err)
	}
	defer f.Close()

	var cfg Config
	dec := json.NewDecoder(f)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, false, fmt.Errorf("decode storage configuration %s: %w", path, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Config{}, false, fmt.Errorf("decode storage configuration %s: %w", path, err)
	}
	if cfg.ConfigSchema > currentSchema {
		return Config{}, false, fmt.Errorf("storage configuration schema %d is newer than supported schema %d", cfg.ConfigSchema, currentSchema)
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeLocal
	}
	return cfg, true, nil
}

// Save validates and atomically replaces storage.json with the canonical
// schema-2 document: the current schema and an explicit mode, so a written
// document never depends on an absent-field default. The containing directory
// and file are private to the current user.
//
// Errors name the path but never the document: it holds credentials.
func Save(cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	cfg.ConfigSchema = currentSchema
	if cfg.Mode == "" {
		cfg.Mode = ModeLocal
	}

	path, err := pathName()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create configuration directory %s: %w", dir, err)
	}

	f, err := os.CreateTemp(dir, ".storage.json-*")
	if err != nil {
		return fmt.Errorf("create temporary storage configuration: %w", err)
	}
	temp := f.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temp)
		}
	}()

	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return fmt.Errorf("secure temporary storage configuration: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(cfg); err != nil {
		_ = f.Close()
		return fmt.Errorf("encode storage configuration: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync storage configuration: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close storage configuration: %w", err)
	}
	if err := os.Rename(temp, path); err != nil {
		return fmt.Errorf("replace storage configuration %s: %w", path, err)
	}
	keep = true
	return nil
}

// Validate checks the complete configuration accepted by Save and by
// `babel storage configure`.
//
// An absent mode is local, so a schema-1 document that was valid stays valid.
// Errors name the offending field and never carry a password: a caller may
// report them to a terminal or a log.
func Validate(cfg Config) error {
	if cfg.ConfigSchema > currentSchema {
		return fmt.Errorf("storage configuration schema %d is newer than supported schema %d", cfg.ConfigSchema, currentSchema)
	}
	if cfg.Repository == "" {
		return errors.New("storage configuration repository is required")
	}
	if cfg.PasswordFile == "" {
		return errors.New("storage configuration password_file is required")
	}
	if !filepath.IsAbs(cfg.PasswordFile) {
		return errors.New("storage configuration password_file must be an absolute path")
	}
	if cfg.HostID != "" && !ValidHostID(cfg.HostID) {
		return fmt.Errorf("storage configuration host_id %q is invalid: %s", cfg.HostID, idPolicy)
	}
	if err := validateRepositoryStore(cfg); err != nil {
		return err
	}
	switch cfg.Mode {
	case "", ModeLocal:
		if cfg.Catalog != nil {
			return fmt.Errorf("storage configuration catalog is only valid in %q mode", ModeShared)
		}
	case ModeShared:
		if err := validateSharedIdentity(cfg); err != nil {
			return err
		}
		if cfg.Catalog == nil {
			return fmt.Errorf("storage configuration catalog is required in %q mode", ModeShared)
		}
		if err := validateCatalog(*cfg.Catalog); err != nil {
			return err
		}
	default:
		return fmt.Errorf("storage configuration mode %q is invalid: mode is %q or %q", cfg.Mode, ModeLocal, ModeShared)
	}
	return nil
}

// validateSharedIdentity checks the identity a shared deployment keys its rows
// and leases by. Both ids use the host-id policy: they reach SQL as bind values
// and appear in operator-facing output, so a small boring character set applies
// to all three.
func validateSharedIdentity(cfg Config) error {
	if cfg.DeploymentID == "" {
		return fmt.Errorf("storage configuration deployment_id is required in %q mode", ModeShared)
	}
	if !validID(cfg.DeploymentID) {
		return fmt.Errorf("storage configuration deployment_id %q is invalid: %s", cfg.DeploymentID, idPolicy)
	}
	if cfg.InstanceID == "" {
		return fmt.Errorf("storage configuration instance_id is required in %q mode", ModeShared)
	}
	if !validID(cfg.InstanceID) {
		return fmt.Errorf("storage configuration instance_id %q is invalid: %s", cfg.InstanceID, idPolicy)
	}
	return nil
}

// validateRepositoryStore checks the object-store credential.
//
// Both halves or neither: one key alone cannot authenticate, and accepting it
// would defer a configuration error to the first backup, which is the worst
// moment to discover it. An S3-style locator with no credential is refused for
// the same reason - restic would fail at push time with the provider's own
// error, and the document is where that is fixable.
func validateRepositoryStore(cfg Config) error {
	s := cfg.RepositoryStore
	if s != nil {
		if (s.AccessKeyID == "") != (s.SecretAccessKey == "") {
			return errors.New("storage configuration repository_store.access_key_id and repository_store.secret_access_key must be supplied together")
		}
		if s.AccessKeyID == "" {
			return errors.New("storage configuration repository_store is present but empty: omit it for a repository that needs no credential")
		}
		return nil
	}
	if needsObjectStoreCredential(cfg.Repository) {
		return errors.New("storage configuration repository_store is required for an object-store repository")
	}
	return nil
}

// needsObjectStoreCredential reports whether a locator addresses a store that
// authenticates with an access key. restic's own scheme prefixes are the
// authority; a bare path is a local repository and needs nothing.
func needsObjectStoreCredential(repository string) bool {
	for _, prefix := range [...]string{"s3:", "gs:", "azure:", "b2:", "swift:"} {
		if strings.HasPrefix(repository, prefix) {
			return prefix == "s3:"
		}
	}
	return false
}

// validateCatalog checks the shared PostgreSQL connection. No error quotes a
// password, and none quotes the connection's host, database, or user either:
// naming the field is enough to fix the document, and the assembled values are
// a DSN.
func validateCatalog(cat Catalog) error {
	if cat.Host == "" {
		return errors.New("storage configuration catalog.host is required")
	}
	if cat.Port < 1 || cat.Port > 65535 {
		return fmt.Errorf("storage configuration catalog.port %d is invalid: port is 1-65535", cat.Port)
	}
	if cat.Database == "" {
		return errors.New("storage configuration catalog.database is required")
	}
	if cat.User == "" {
		return errors.New("storage configuration catalog.user is required")
	}
	if cat.Password == "" {
		return errors.New("storage configuration catalog.password is required")
	}
	switch cat.TLSMode {
	case TLSRequire, TLSVerifyFull:
	default:
		return fmt.Errorf("storage configuration catalog.tls_mode %q is invalid: tls_mode is %q or %q", cat.TLSMode, TLSRequire, TLSVerifyFull)
	}
	if cat.TLSRootCAFile != "" && !filepath.IsAbs(cat.TLSRootCAFile) {
		return errors.New("storage configuration catalog.tls_root_ca_file must be an absolute path")
	}
	// Zero is "not stated" and takes the default; a negative ceiling would
	// reach database/sql as an unlimited pool, which is the opposite of what
	// anyone writing a number here wants.
	if cat.MaxConnections < 0 {
		return fmt.Errorf("storage configuration catalog.max_connections %d is invalid: omit it for the default, or give a positive ceiling", cat.MaxConnections)
	}
	if (cat.MigrationUser == "") != (cat.MigrationPassword == "") {
		return errors.New("storage configuration catalog.migration_user and catalog.migration_password must be supplied together")
	}
	// A migration credential that is the same role as the application
	// credential is not separation, and would report as separation if it were
	// accepted here.
	if cat.MigrationUser != "" && cat.MigrationUser == cat.User {
		return errors.New("storage configuration catalog.migration_user must differ from catalog.user")
	}
	return nil
}

// Redacted returns a copy of c safe for status output, diagnostics, and
// anything a caller might print.
//
// The guarantee is total, not a list of the fields someone remembered: every
// secret-bearing field the document carries becomes a fixed placeholder. Those
// fields are the catalog's Password and MigrationPassword and the object
// store's AccessKeyID and SecretAccessKey. The access key id is covered
// alongside its secret half because validateRepositoryStore already treats the
// pair as one credential that cannot authenticate in halves, and because a
// status report that prints an account-identifying key hands an attacker the
// target for free.
//
// An empty field stays empty. That is what the placeholder is for: status
// output must distinguish "configured, not shown" from "absent", or a missing
// credential cannot be diagnosed from it.
//
// Every other field is kept in the clear, deliberately, because none of them
// authenticates anything and they are precisely what an operator reads a status
// report to check: the repository locator, the password file path, the restic
// binary, the host/deployment/instance ids, and the catalog's host, port,
// database, TLS mode, root CA path, User and MigrationUser. Role names and
// locators are not secrets.
//
// The copy is deep through both nested structs, so the source is untouched. A
// redacted copy is not a valid document to save, and DSN on its catalog does
// not connect.
func (c Config) Redacted() Config {
	if c.RepositoryStore != nil {
		store := *c.RepositoryStore
		if store.AccessKeyID != "" {
			store.AccessKeyID = redactedPlaceholder
		}
		if store.SecretAccessKey != "" {
			store.SecretAccessKey = redactedPlaceholder
		}
		c.RepositoryStore = &store
	}
	if c.Catalog != nil {
		cat := *c.Catalog
		if cat.Password != "" {
			cat.Password = redactedPlaceholder
		}
		if cat.MigrationPassword != "" {
			cat.MigrationPassword = redactedPlaceholder
		}
		c.Catalog = &cat
	}
	return c
}

// DSN returns the pgx connection string for the application credential.
//
// The result is itself a credential: it embeds the password. It may be passed
// to the driver and nowhere else - never to a log, an error, a diagnostic, or a
// tool argument (SPEC.md 9). Callers that need something printable use
// Redacted.
func (c Catalog) DSN() string {
	return c.dsn(c.User, c.Password)
}

// MigrationDSN returns the connection string for the optional separate
// migration credential, and false when the deployment runs the
// single-credential default. Like DSN, the result is a credential.
//
// A configured migration credential is a claim, not an observation: whether it
// actually holds privileges the application credential lacks is only knowable
// by connecting with both and comparing.
func (c Catalog) MigrationDSN() (string, bool) {
	if c.MigrationUser == "" || c.MigrationPassword == "" {
		return "", false
	}
	return c.dsn(c.MigrationUser, c.MigrationPassword), true
}

func (c Catalog) dsn(user, password string) string {
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(user, password),
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(c.Port)),
		Path:   "/" + c.Database,
	}
	q := url.Values{"sslmode": []string{c.TLSMode}}
	if c.TLSRootCAFile != "" {
		q.Set("sslrootcert", c.TLSRootCAFile)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// ValidHostID reports whether s is 1-64 characters of [a-z0-9._-] and
// starts with an alphanumeric character.
func ValidHostID(s string) bool {
	return validID(s)
}

// idPolicy states the accepted shape once, for every error that rejects one.
var idPolicy = fmt.Sprintf("ids are 1-%d characters of [a-z0-9._-] starting alphanumeric", maxHostIDLen)

// validID is the single character policy behind host, deployment, and instance
// ids.
func validID(s string) bool {
	if s == "" || len(s) > maxHostIDLen {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case (c == '.' || c == '_' || c == '-') && i > 0:
		default:
			return false
		}
	}
	return true
}

func pathName() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve configuration directory: %w", err)
	}
	return filepath.Join(base, "babel", "storage.json"), nil
}
