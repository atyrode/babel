package config

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func configHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "config")
	t.Setenv("XDG_CONFIG_HOME", home)
	return home
}

func validConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		Repository:   filepath.Join(t.TempDir(), "repo"),
		PasswordFile: filepath.Join(t.TempDir(), "password"),
		HostID:       "host-1",
		ResticBinary: "/opt/restic",
	}
}

func TestLoadMissingAndSaveRoundTrip(t *testing.T) {
	home := configHome(t)
	if cfg, found, err := Load(); err != nil || found || cfg != (Config{}) {
		t.Fatalf("Load missing = (%+v, %v, %v)", cfg, found, err)
	}
	if got, want := Path(), filepath.Join(home, "babel", "storage.json"); got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}

	want := validConfig(t)
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("storage.json mode = %04o, want 0600", got)
	}
	if got := mustStat(t, filepath.Dir(Path())).Mode().Perm(); got != 0o700 {
		t.Fatalf("configuration directory mode = %04o, want 0700", got)
	}

	got, found, err := Load()
	if err != nil || !found {
		t.Fatalf("Load saved = (%+v, %v, %v)", got, found, err)
	}
	want.ConfigSchema = currentSchema
	want.Mode = ModeLocal
	if got != want {
		t.Fatalf("Load saved = %+v, want %+v", got, want)
	}
}

func TestLoadCompatibilityAndMalformedInput(t *testing.T) {
	configHome(t)
	if err := os.MkdirAll(filepath.Dir(Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(Path(), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write(`{"config_schema":1,"repository":"repo","password_file":"/password","future_field":{"kept":false}}`)
	cfg, found, err := Load()
	if err != nil || !found || cfg.Repository != "repo" {
		t.Fatalf("Load with unknown field = (%+v, %v, %v)", cfg, found, err)
	}

	write(`{"config_schema":3,"repository":"repo","password_file":"/password"}`)
	if _, _, err := Load(); err == nil || !strings.Contains(err.Error(), "schema 3 is newer than supported schema 2") {
		t.Fatalf("Load future schema error = %v", err)
	}

	for _, body := range []string{`{`, `{}` + `{}`} {
		write(body)
		if _, _, err := Load(); err == nil {
			t.Fatalf("Load malformed %q succeeded", body)
		}
	}
}

func TestSaveValidationDoesNotReplaceExistingFile(t *testing.T) {
	configHome(t)
	good := validConfig(t)
	if err := Save(good); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}

	cases := []Config{
		{PasswordFile: good.PasswordFile},
		{Repository: good.Repository},
		{Repository: good.Repository, PasswordFile: "relative"},
		{Repository: good.Repository, PasswordFile: good.PasswordFile, HostID: "Bad Host"},
		{ConfigSchema: 3, Repository: good.Repository, PasswordFile: good.PasswordFile},
	}
	for _, cfg := range cases {
		if err := Save(cfg); err == nil {
			t.Fatalf("Save(%+v) succeeded", cfg)
		}
		after, err := os.ReadFile(Path())
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Fatalf("invalid Save replaced existing configuration")
		}
	}
}

func TestValidHostID(t *testing.T) {
	for _, value := range []string{"host", "host-1", "1.example_host"} {
		if !ValidHostID(value) {
			t.Errorf("ValidHostID(%q) = false", value)
		}
	}
	for _, value := range []string{"", "-host", "Host", "two words", strings.Repeat("a", 65)} {
		if ValidHostID(value) {
			t.Errorf("ValidHostID(%q) = true", value)
		}
	}
}

func mustStat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// Synthetic sentinels for the redaction test. Each is distinct so a failure
// names the field that leaked, and each is obviously not a credential: no real
// access key id contains lowercase words and hyphens, and no real password is
// self-describing.
const (
	sentinelRedactCatalogPassword   = "SENTINEL-not-a-real-catalog-password-1111"
	sentinelRedactMigrationPassword = "SENTINEL-not-a-real-migration-password-2222"
	sentinelRedactAccessKeyID       = "SENTINEL-not-a-real-access-key-id-3333"
	sentinelRedactSecretAccessKey   = "SENTINEL-not-a-real-secret-access-key-4444"
)

// documentSecrets classifies every field the storage document carries: true
// where the field holds something that authenticates, false where it is a
// locator, a role name, a mode, a schema number, or the pointer to a nested
// block.
//
// This is a frozen list rather than reflection over a marker tag, because the
// structs carry no such marker and adding one would defend nothing: the failure
// this test exists to prevent is a new secret field that nobody redacted, and
// whoever forgets the redaction forgets the marker in the same edit. So the
// list is instead asserted exhaustive in both directions - every field of every
// struct reachable from Config appears here, and every name here is a real
// field - which is what makes adding any field at all fail this test until its
// secret-or-not decision has been recorded. That is stronger than a field
// count and says why it failed.
//
// Go field names, not JSON names: this is about the values Redacted copies, and
// a field tagged json:"-" would still be printed by %+v.
var documentSecrets = map[string]bool{
	"Config.ConfigSchema":             false,
	"Config.Mode":                     false,
	"Config.Repository":               false,
	"Config.PasswordFile":             false,
	"Config.HostID":                   false,
	"Config.ResticBinary":             false,
	"Config.DeploymentID":             false,
	"Config.InstanceID":               false,
	"Config.RepositoryStore":          false,
	"Config.Catalog":                  false,
	"RepositoryStore.AccessKeyID":     true,
	"RepositoryStore.SecretAccessKey": true,
	"Catalog.Host":                    false,
	"Catalog.Port":                    false,
	"Catalog.Database":                false,
	"Catalog.User":                    false,
	"Catalog.Password":                true,
	"Catalog.TLSMode":                 false,
	"Catalog.TLSRootCAFile":           false,
	"Catalog.MigrationUser":           false,
	"Catalog.MigrationPassword":       true,
}

// populatedDocument returns a Config with every field set. Secret-bearing
// fields carry their sentinel; the rest carry synthetic values that are not
// secrets. No field is left at its zero value, because a field the fixture
// forgot to populate is a field this test would pass on vacuously - which is
// the exact shape of the defect it defends against.
func populatedDocument() Config {
	return Config{
		ConfigSchema: currentSchema,
		Mode:         ModeShared,
		Repository:   "s3:https://object.example/babel-fixture",
		PasswordFile: "/etc/babel/repository-password",
		HostID:       "fixture-host",
		ResticBinary: "/usr/bin/restic",
		DeploymentID: "fixture-deployment",
		InstanceID:   "fixture-instance",
		RepositoryStore: &RepositoryStore{
			AccessKeyID:     sentinelRedactAccessKeyID,
			SecretAccessKey: sentinelRedactSecretAccessKey,
		},
		Catalog: &Catalog{
			Host:              "catalog.example",
			Port:              5432,
			Database:          "babel",
			User:              "babel_app",
			Password:          sentinelRedactCatalogPassword,
			TLSMode:           TLSVerifyFull,
			TLSRootCAFile:     "/etc/ssl/certs/fixture-root.pem",
			MigrationUser:     "babel_migration",
			MigrationPassword: sentinelRedactMigrationPassword,
		},
	}
}

// Redacted's doc comment promises the result is safe to print. This defends
// that promise structurally: the classification above must cover the document
// exactly, every field it calls secret must come back as the placeholder, no
// sentinel may survive anywhere in the copy or its JSON, and every field it
// calls public must survive unchanged - a Redacted that blanked everything
// would be safe and useless.
func TestRedactedRedactsEverySecretBearingField(t *testing.T) {
	found := documentFieldPaths(reflect.TypeOf(Config{}))
	for _, path := range found {
		if _, ok := documentSecrets[path]; !ok {
			t.Errorf("%s is a new field of the storage document and documentSecrets does not classify it.\n"+
				"Decide whether it is secret-bearing, record it above, and make Redacted cover it if it is.", path)
		}
	}
	for path := range documentSecrets {
		if !slices.Contains(found, path) {
			t.Errorf("documentSecrets classifies %s, which is no longer a field: remove it", path)
		}
	}

	cfg := populatedDocument()
	before := documentStrings(cfg)
	for path, value := range before {
		if value == "" {
			t.Fatalf("populatedDocument leaves %s empty; the assertions below would pass vacuously for it", path)
		}
	}

	got := documentStrings(cfg.Redacted())
	for path, value := range got {
		switch {
		case documentSecrets[path] && value != redactedPlaceholder:
			t.Errorf("Redacted left %s in the clear: %q", path, value)
		case !documentSecrets[path] && value != before[path]:
			t.Errorf("Redacted altered the public field %s: %q, want %q", path, value, before[path])
		}
	}

	// Printing is what Redacted exists for, so no sentinel may appear in
	// anything a caller can render: not in any string the copy carries at any
	// depth, and not in its JSON.
	document, err := json.Marshal(cfg.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	rendered := append(slices.Collect(maps.Values(got)), string(document))
	for path, value := range before {
		if !documentSecrets[path] {
			continue
		}
		for _, text := range rendered {
			if strings.Contains(text, value) {
				t.Errorf("the %s sentinel survives in printable output: %s", path, text)
			}
		}
	}

	if !reflect.DeepEqual(documentStrings(cfg), before) {
		t.Error("Redacted mutated its source instead of copying")
	}

	// An absent secret must stay distinguishable from a hidden one: a status
	// report where the two look alike cannot diagnose a missing credential.
	sparse := Config{Mode: ModeShared, RepositoryStore: &RepositoryStore{}, Catalog: &Catalog{}}
	for path, value := range documentStrings(sparse.Redacted()) {
		if documentSecrets[path] && value != "" {
			t.Errorf("Redacted made the absent secret %s look configured: %q", path, value)
		}
	}
}

// documentFieldPaths reports every field of every struct reachable from t as
// "Type.Field", following pointers to nested blocks. New nested structs are
// discovered without editing this helper.
func documentFieldPaths(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		field := t.Field(i)
		out = append(out, t.Name()+"."+field.Name)
		nested := field.Type
		if nested.Kind() == reflect.Pointer {
			nested = nested.Elem()
		}
		if nested.Kind() == reflect.Struct {
			out = append(out, documentFieldPaths(nested)...)
		}
	}
	return out
}

// documentStrings reports every string a Config carries, keyed the same way as
// documentFieldPaths. A nil nested block contributes nothing.
func documentStrings(cfg Config) map[string]string {
	out := map[string]string{}
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		t := v.Type()
		for i := range t.NumField() {
			value := v.Field(i)
			switch value.Kind() {
			case reflect.String:
				out[t.Name()+"."+t.Field(i).Name] = value.String()
			case reflect.Pointer:
				if !value.IsNil() && value.Elem().Kind() == reflect.Struct {
					walk(value.Elem())
				}
			case reflect.Struct:
				walk(value)
			}
		}
	}
	walk(reflect.ValueOf(cfg))
	return out
}
