package cli

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/config"
)

// catalogPassword is the sentinel these tests hunt for. SPEC.md §9 forbids
// credentials in logs and errors, and the shared-mode surface is the first one
// that holds a database password, so every assertion below searches both
// channels for this exact string.
const catalogPassword = "SYNTHETICCATALOGPASSWORD5d41402a"

// closedPort returns a loopback port with nothing listening on it. Binding and
// releasing is how the port is known to be closed rather than merely unusual,
// which makes "the catalog is unreachable" a fact of the test rather than an
// assumption about the developer's machine.
func closedPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("release the reserved port: %v", err)
	}
	return port
}

// sharedDocument builds a valid shared-mode document aimed at a given port. It
// is valid by construction: what the tests exercise is live behaviour, so a
// document rejected by Validate would prove nothing about it.
func sharedDocument(port int) config.Config {
	return config.Config{
		ConfigSchema: 2,
		Mode:         config.ModeShared,
		Repository:   "/tmp/synthetic-repository",
		PasswordFile: "/tmp/synthetic-password",
		HostID:       "fixturehost",
		DeploymentID: "fixturedeployment",
		InstanceID:   "fixturehost",
		Catalog: &config.Catalog{
			Host:     "127.0.0.1",
			Port:     port,
			Database: "babel_catalog",
			User:     "babel_application",
			Password: catalogPassword,
			TLSMode:  config.TLSRequire,
		},
	}
}

func writeDocument(t *testing.T, cfg config.Config) string {
	t.Helper()
	path := t.TempDir() + "/document.json"
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode document: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write document: %v", err)
	}
	return path
}

// A shared document whose catalog cannot be reached must not replace a working
// configuration. SPEC.md §2.3: "failure preserves the previous valid
// configuration and prior timer state." This is the reason the live checks run
// before the atomic replacement rather than after it.
func TestFailedSharedConfigureKeepsPreviousConfiguration(t *testing.T) {
	f := newFixture(t)

	local := config.Config{
		ConfigSchema: 2,
		Mode:         config.ModeLocal,
		Repository:   "/tmp/synthetic-repository",
		PasswordFile: "/tmp/synthetic-password",
		HostID:       "fixturehost",
	}
	if _, _, code := f.run("storage", "configure", "--from-json", writeDocument(t, local)); code != 0 {
		t.Fatalf("configuring local mode must succeed, got exit %d", code)
	}
	before, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatalf("read the installed configuration: %v", err)
	}

	stdout, stderr, code := f.run("storage", "configure", "--from-json", writeDocument(t, sharedDocument(closedPort(t))))
	if code == 0 {
		t.Fatalf("configuring an unreachable catalog must fail, got exit 0\nstdout: %s", stdout)
	}

	after, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatalf("read the configuration after the failure: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("a failed configure replaced the previous configuration\nbefore: %s\nafter:  %s", before, after)
	}
	// The failure path is the one most likely to echo a connection string.
	if strings.Contains(stderr, catalogPassword) || strings.Contains(stdout, catalogPassword) {
		t.Error("the catalog password reached a CLI channel")
	}
}

// The shared-mode verbs must refuse a local configuration by name rather than
// dialing nothing, panicking on a nil catalog, or silently succeeding.
func TestSharedVerbsRefuseLocalMode(t *testing.T) {
	f := newFixture(t)

	local := config.Config{
		ConfigSchema: 2,
		Mode:         config.ModeLocal,
		Repository:   "/tmp/synthetic-repository",
		PasswordFile: "/tmp/synthetic-password",
		HostID:       "fixturehost",
	}
	if _, _, code := f.run("storage", "configure", "--from-json", writeDocument(t, local)); code != 0 {
		t.Fatal("configuring local mode must succeed")
	}

	for _, args := range [][]string{
		{"storage", "verify"},
		{"storage", "migrate"},
		{"storage", "revoke-instance", "some-instance"},
	} {
		_, stderr, code := f.run(args...)
		if code == 0 {
			t.Errorf("%v must fail in local mode", args)
			continue
		}
		if !strings.Contains(stderr, "shared mode") {
			t.Errorf("%v must say it requires shared mode, got: %s", args, stderr)
		}
	}
}

// Unconfigured storage must produce guidance rather than a connection attempt.
func TestSharedVerbsRefuseUnconfiguredStorage(t *testing.T) {
	f := newFixture(t)
	_, stderr, code := f.run("storage", "verify")
	if code == 0 {
		t.Fatal("storage verify must fail when storage is unconfigured")
	}
	if !strings.Contains(stderr, "storage configure") {
		t.Errorf("the error must name the command that fixes it, got: %s", stderr)
	}
}

func TestRevokeInstanceRequiresExactlyOneID(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{
		{"storage", "revoke-instance"},
		{"storage", "revoke-instance", "a", "b"},
	} {
		_, stderr, code := f.run(args...)
		if code == 0 {
			t.Errorf("%v must be rejected", args)
			continue
		}
		if !strings.Contains(stderr, "exactly one INSTANCE_ID") {
			t.Errorf("%v must explain the argument count, got: %s", args, stderr)
		}
	}
}

// storage status is the offline report: it must describe a shared configuration
// without dialing PostgreSQL and without emitting either password. The
// configuration is installed directly here because installing it through
// `storage configure` would require a live catalog, which is the property the
// other tests cover.
func TestStorageStatusReportsSharedIdentityWithoutCredentials(t *testing.T) {
	f := newFixture(t)

	cfg := sharedDocument(closedPort(t))
	cfg.Catalog.MigrationUser = "babel_migration"
	cfg.Catalog.MigrationPassword = catalogPassword + "-migration"
	if err := config.Save(cfg); err != nil {
		t.Fatalf("install a shared configuration: %v", err)
	}

	stdout, stderr, code := f.run("storage", "status", "--json")
	if code != 0 {
		t.Fatalf("storage status must succeed offline, got exit %d\nstderr: %s", code, stderr)
	}

	var got storageStatusResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if got.Mode != config.ModeShared {
		t.Errorf("mode = %q, want %q", got.Mode, config.ModeShared)
	}
	if got.DeploymentID != "fixturedeployment" || got.InstanceID != "fixturehost" {
		t.Errorf("status must report deployment and instance identity, got %q/%q", got.DeploymentID, got.InstanceID)
	}
	if got.CatalogUser != "babel_application" || got.MigrationUser != "babel_migration" {
		t.Errorf("status must report both role names, got %q/%q", got.CatalogUser, got.MigrationUser)
	}
	if !strings.Contains(got.CatalogEndpoint, "babel_catalog") {
		t.Errorf("status must report the endpoint, got %q", got.CatalogEndpoint)
	}
	// Non-vacuity: the document really does carry the sentinel, so an absence
	// below is the report withholding it rather than nothing being there.
	installed, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatalf("read the installed configuration: %v", err)
	}
	if !strings.Contains(string(installed), catalogPassword) {
		t.Fatal("the fixture must install a document that actually holds the password")
	}
	if strings.Contains(stdout, catalogPassword) || strings.Contains(stderr, catalogPassword) {
		t.Error("storage status emitted a catalog password")
	}
}
