package config

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Synthetic credential sentinels. No validation error, and no test name, may
// contain either one.
const (
	sentinelAppPassword       = "sentinel-application-password-6b1f"
	sentinelMigrationPassword = "sentinel-migration-password-9d4c"
)

func installFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), body, 0o600); err != nil {
		t.Fatal(err)
	}
	return body
}

func sharedConfig() Config {
	return Config{
		Mode:       ModeShared,
		Repository: "s3:s3.example.invalid/babel-archive",
		RepositoryStore: &RepositoryStore{
			AccessKeyID:     "SYNTHETICACCESSKEYID",
			SecretAccessKey: "synthetic-secret-access-key",
		},
		PasswordFile: "/etc/babel/repository-password",
		DeploymentID: "example-deployment",
		InstanceID:   "workstation",
		Catalog: &Catalog{
			Host:     "postgresql.example.invalid",
			Port:     5432,
			Database: "babel_catalog",
			User:     "babel_application",
			Password: sentinelAppPassword,
			TLSMode:  TLSRequire,
		},
	}
}

func TestLoadSchema1IsLocalAndSaveCanonicalizes(t *testing.T) {
	configHome(t)
	installFixture(t, "local_schema1.json")

	cfg, found, err := Load()
	if err != nil || !found {
		t.Fatalf("Load = (%v, %v)", found, err)
	}
	if cfg.ConfigSchema != 1 {
		t.Fatalf("ConfigSchema = %d, want 1", cfg.ConfigSchema)
	}
	if cfg.Mode != ModeLocal {
		t.Fatalf("Mode = %q, want %q", cfg.Mode, ModeLocal)
	}
	if cfg.Catalog != nil {
		t.Fatalf("Catalog = %+v, want nil", cfg.Catalog)
	}

	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	canonical, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	saved, found, err := Load()
	if err != nil || !found {
		t.Fatalf("Load canonical = (%v, %v)", found, err)
	}
	if saved.ConfigSchema != currentSchema || saved.Mode != ModeLocal {
		t.Fatalf("canonical document = schema %d mode %q, want schema %d mode %q", saved.ConfigSchema, saved.Mode, currentSchema, ModeLocal)
	}
	if err := Save(saved); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(canonical) {
		t.Fatalf("canonicalization is not a fixed point:\n%s\n%s", canonical, again)
	}
}

func TestSharedFixturesRoundTripByteStable(t *testing.T) {
	for _, name := range []string{"shared_single_credential.json", "shared_migration_credential.json"} {
		t.Run(name, func(t *testing.T) {
			configHome(t)
			want := installFixture(t, name)

			cfg, found, err := Load()
			if err != nil || !found {
				t.Fatalf("Load = (%v, %v)", found, err)
			}
			if err := Validate(cfg); err != nil {
				t.Fatalf("Validate fixture: %v", err)
			}
			if cfg.Mode != ModeShared {
				t.Fatalf("Mode = %q, want %q", cfg.Mode, ModeShared)
			}
			if cfg.DeploymentID == "" || cfg.InstanceID == "" {
				t.Fatalf("shared identity = (%q, %q), want both set", cfg.DeploymentID, cfg.InstanceID)
			}
			if cfg.Catalog == nil {
				t.Fatal("Catalog = nil, want a shared catalog")
			}
			if cfg.Catalog.TLSMode != TLSRequire {
				t.Fatalf("tls_mode = %q, want %q", cfg.Catalog.TLSMode, TLSRequire)
			}
			separate := name == "shared_migration_credential.json"
			if _, ok := cfg.Catalog.MigrationDSN(); ok != separate {
				t.Fatalf("MigrationDSN configured = %v, want %v", ok, separate)
			}

			if err := Save(cfg); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(Path())
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("fixture is not canonical:\nwant:\n%s\ngot:\n%s", want, got)
			}
		})
	}
}

func TestValidateModesAndCatalogFields(t *testing.T) {
	cases := []struct {
		name  string
		build func() Config
		field string
	}{
		{"object store locator requires a credential", func() Config {
			cfg := sharedConfig()
			cfg.RepositoryStore = nil
			return cfg
		}, "repository_store"},
		{"half a credential is refused", func() Config {
			cfg := sharedConfig()
			cfg.RepositoryStore = &RepositoryStore{AccessKeyID: "SYNTHETICACCESSKEYID"}
			return cfg
		}, "repository_store"},
		{"an empty credential block is refused", func() Config {
			cfg := sharedConfig()
			cfg.RepositoryStore = &RepositoryStore{}
			return cfg
		}, "repository_store"},
		{"local mode rejects a catalog", func() Config {
			cfg := sharedConfig()
			cfg.Mode = ModeLocal
			return cfg
		}, "catalog"},
		{"absent mode rejects a catalog", func() Config {
			cfg := sharedConfig()
			cfg.Mode = ""
			return cfg
		}, "catalog"},
		{"unknown mode", func() Config {
			cfg := sharedConfig()
			cfg.Mode = "cluster"
			return cfg
		}, "mode"},
		{"shared mode requires a catalog", func() Config {
			cfg := sharedConfig()
			cfg.Catalog = nil
			return cfg
		}, "catalog is required"},
		{"deployment id required", func() Config {
			cfg := sharedConfig()
			cfg.DeploymentID = ""
			return cfg
		}, "deployment_id"},
		{"deployment id policy", func() Config {
			cfg := sharedConfig()
			cfg.DeploymentID = "Example Deployment"
			return cfg
		}, "deployment_id"},
		{"instance id required", func() Config {
			cfg := sharedConfig()
			cfg.InstanceID = ""
			return cfg
		}, "instance_id"},
		{"instance id policy", func() Config {
			cfg := sharedConfig()
			cfg.InstanceID = "-leading-dash"
			return cfg
		}, "instance_id"},
		{"instance id length", func() Config {
			cfg := sharedConfig()
			cfg.InstanceID = strings.Repeat("a", maxHostIDLen+1)
			return cfg
		}, "instance_id"},
		{"catalog host", func() Config { return withCatalog(func(c *Catalog) { c.Host = "" }) }, "catalog.host"},
		{"catalog port zero", func() Config { return withCatalog(func(c *Catalog) { c.Port = 0 }) }, "catalog.port"},
		{"catalog port negative", func() Config { return withCatalog(func(c *Catalog) { c.Port = -1 }) }, "catalog.port"},
		{"catalog port too large", func() Config { return withCatalog(func(c *Catalog) { c.Port = 65536 }) }, "catalog.port"},
		{"catalog database", func() Config { return withCatalog(func(c *Catalog) { c.Database = "" }) }, "catalog.database"},
		{"catalog user", func() Config { return withCatalog(func(c *Catalog) { c.User = "" }) }, "catalog.user"},
		{"catalog password", func() Config { return withCatalog(func(c *Catalog) { c.Password = "" }) }, "catalog.password"},
		{"catalog tls mode empty", func() Config { return withCatalog(func(c *Catalog) { c.TLSMode = "" }) }, "catalog.tls_mode"},
		{"catalog tls mode disable", func() Config { return withCatalog(func(c *Catalog) { c.TLSMode = "disable" }) }, "catalog.tls_mode"},
		{"catalog tls mode verify-ca", func() Config { return withCatalog(func(c *Catalog) { c.TLSMode = "verify-ca" }) }, "catalog.tls_mode"},
		{"catalog root ca relative", func() Config {
			return withCatalog(func(c *Catalog) {
				c.TLSMode = TLSVerifyFull
				c.TLSRootCAFile = "certs/root.crt"
			})
		}, "catalog.tls_root_ca_file"},
		{"migration user without password", func() Config {
			return withCatalog(func(c *Catalog) { c.MigrationUser = "babel_migration" })
		}, "together"},
		{"migration password without user", func() Config {
			return withCatalog(func(c *Catalog) { c.MigrationPassword = sentinelMigrationPassword })
		}, "together"},
		{"migration user equals user", func() Config {
			return withCatalog(func(c *Catalog) {
				c.MigrationUser = c.User
				c.MigrationPassword = sentinelMigrationPassword
			})
		}, "must differ"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(tc.build())
			if err == nil {
				t.Fatalf("Validate succeeded, want an error naming %q", tc.field)
			}
			message := err.Error()
			if !strings.Contains(message, tc.field) {
				t.Fatalf("error %q does not name %q", message, tc.field)
			}
			for _, secret := range []string{sentinelAppPassword, sentinelMigrationPassword} {
				if strings.Contains(message, secret) {
					t.Fatalf("validation error leaked a password")
				}
			}
		})
	}
}

func withCatalog(mutate func(*Catalog)) Config {
	cfg := sharedConfig()
	mutate(cfg.Catalog)
	return cfg
}

func TestValidateAcceptsSharedDocuments(t *testing.T) {
	if err := Validate(sharedConfig()); err != nil {
		t.Fatalf("single-credential shared configuration: %v", err)
	}
	separate := withCatalog(func(c *Catalog) {
		c.MigrationUser = "babel_migration"
		c.MigrationPassword = sentinelMigrationPassword
	})
	if err := Validate(separate); err != nil {
		t.Fatalf("separate migration credential: %v", err)
	}
	verify := withCatalog(func(c *Catalog) {
		c.TLSMode = TLSVerifyFull
		c.TLSRootCAFile = "/etc/ssl/certs/example-root.crt"
	})
	if err := Validate(verify); err != nil {
		t.Fatalf("verify-full with a root CA: %v", err)
	}
}

func TestRedactedDropsBothPasswords(t *testing.T) {
	cfg := withCatalog(func(c *Catalog) {
		c.MigrationUser = "babel_migration"
		c.MigrationPassword = sentinelMigrationPassword
	})

	got := cfg.Redacted()
	if got.Catalog == cfg.Catalog {
		t.Fatal("Redacted shares the catalog with its source")
	}
	if got.Catalog.Password != redactedPlaceholder || got.Catalog.MigrationPassword != redactedPlaceholder {
		t.Fatalf("redacted passwords = (%q, %q), want %q for both", got.Catalog.Password, got.Catalog.MigrationPassword, redactedPlaceholder)
	}
	if got.Catalog.Password != got.Catalog.MigrationPassword {
		t.Fatal("placeholder differs between passwords, revealing which is which")
	}
	if got.Catalog.User != cfg.Catalog.User || got.Catalog.Host != cfg.Catalog.Host || got.DeploymentID != cfg.DeploymentID {
		t.Fatal("Redacted dropped a non-credential field")
	}
	if cfg.Catalog.Password != sentinelAppPassword || cfg.Catalog.MigrationPassword != sentinelMigrationPassword {
		t.Fatal("Redacted mutated its source")
	}

	local := Config{Mode: ModeLocal, Repository: "repo", PasswordFile: "/password"}
	if local.Redacted() != local {
		t.Fatal("Redacted changed a local configuration")
	}
}

func TestDSNCarriesCredentialAndTLS(t *testing.T) {
	cat := Catalog{
		Host:     "postgresql.example.invalid",
		Port:     5432,
		Database: "babel_catalog",
		User:     "babel_application",
		Password: "sentinel/password with?reserved#bytes",
		TLSMode:  TLSRequire,
	}

	u, err := url.Parse(cat.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "postgres" {
		t.Fatalf("scheme = %q, want postgres", u.Scheme)
	}
	if u.Host != "postgresql.example.invalid:5432" {
		t.Fatalf("host = %q", u.Host)
	}
	if u.Path != "/babel_catalog" {
		t.Fatalf("path = %q, want /babel_catalog", u.Path)
	}
	if user := u.User.Username(); user != cat.User {
		t.Fatalf("user = %q, want %q", user, cat.User)
	}
	if password, _ := u.User.Password(); password != cat.Password {
		t.Fatal("password did not survive DSN escaping")
	}
	if got := u.Query().Get("sslmode"); got != TLSRequire {
		t.Fatalf("sslmode = %q, want %q", got, TLSRequire)
	}
	if _, ok := u.Query()["sslrootcert"]; ok {
		t.Fatal("sslrootcert present without a configured root CA")
	}
	if _, ok := cat.MigrationDSN(); ok {
		t.Fatal("MigrationDSN reported a credential that is not configured")
	}

	cat.TLSMode = TLSVerifyFull
	cat.TLSRootCAFile = "/etc/ssl/certs/example-root.crt"
	u, err = url.Parse(cat.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Query().Get("sslmode"); got != TLSVerifyFull {
		t.Fatalf("sslmode = %q, want %q", got, TLSVerifyFull)
	}
	if got := u.Query().Get("sslrootcert"); got != cat.TLSRootCAFile {
		t.Fatalf("sslrootcert = %q, want %q", got, cat.TLSRootCAFile)
	}
}

func TestMigrationDSNUsesTheSeparateCredential(t *testing.T) {
	cat := Catalog{
		Host:              "postgresql.example.invalid",
		Port:              5432,
		Database:          "babel_catalog",
		User:              "babel_application",
		Password:          sentinelAppPassword,
		TLSMode:           TLSRequire,
		MigrationUser:     "babel_migration",
		MigrationPassword: sentinelMigrationPassword,
	}

	dsn, ok := cat.MigrationDSN()
	if !ok {
		t.Fatal("MigrationDSN = false with a configured migration credential")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if user := u.User.Username(); user != cat.MigrationUser {
		t.Fatalf("user = %q, want %q", user, cat.MigrationUser)
	}
	if password, _ := u.User.Password(); password != cat.MigrationPassword {
		t.Fatal("MigrationDSN does not carry the migration password")
	}
	if u.Host != "postgresql.example.invalid:5432" || u.Path != "/babel_catalog" || u.Query().Get("sslmode") != TLSRequire {
		t.Fatalf("MigrationDSN target differs from the application DSN target: %q %q %q", u.Host, u.Path, u.Query().Get("sslmode"))
	}

	// A missing half must never yield a usable DSN, matching validation.
	half := cat
	half.MigrationPassword = ""
	if _, ok := half.MigrationDSN(); ok {
		t.Fatal("MigrationDSN accepted a user without a password")
	}
	half = cat
	half.MigrationUser = ""
	if _, ok := half.MigrationDSN(); ok {
		t.Fatal("MigrationDSN accepted a password without a user")
	}
}
