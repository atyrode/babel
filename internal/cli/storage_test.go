package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/config"
)

func TestStorageConfigureFromStdinDrivesArchiveStatus(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))

	// Create a real repository first, then remove every flag/environment
	// selection so status has only storage.json to resolve from.
	f.bootstrapRepo()
	f.ok(f.with("archive", "push")...)
	t.Setenv("BABEL_HOST_ID", "")
	body, err := json.Marshal(config.Config{
		ConfigSchema: 1,
		Repository:   f.repoDir,
		PasswordFile: f.passwordFile,
		HostID:       "configured-host",
	})
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"storage", "configure", "--from-json", "-", "--json"}, bytes.NewReader(body), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("configure via stdin exited %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	configured := decode[storageConfigureResult](t, stdout.String())
	if configured.Path != config.Path() || configured.Repository != f.repoDir || configured.HostID != "configured-host" {
		t.Fatalf("configure result = %+v", configured)
	}
	if stderr.Len() != 0 {
		t.Fatalf("configure diagnostics = %q", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"archive", "status", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("archive status through storage.json exited %d\nstdout: %s\nstderr: %s", code, stdout.String(), stderr.String())
	}
	status := decode[statusResult](t, stdout.String())
	if status.Repository != f.repoDir || status.Snapshots != 1 {
		t.Fatalf("archive status through storage.json = %+v", status)
	}
}

func TestRepositorySelectionPrecedence(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))
	t.Setenv("BABEL_HOST_ID", "")
	cfg := config.Config{
		Repository:   filepath.Join(f.root, "config-repo"),
		PasswordFile: filepath.Join(f.root, "config-password"),
		HostID:       "config-host",
		ResticBinary: "/config/restic",
	}
	if err := config.Save(cfg); err != nil {
		t.Fatal(err)
	}

	open := func(rf *repoFlags) {
		t.Helper()
		c := newCmd("test", "")
		if _, err := rf.open(c, dirs{cache: filepath.Join(f.root, "restic-cache")}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}

	var fromConfig repoFlags
	open(&fromConfig)
	if fromConfig.repository != cfg.Repository || fromConfig.passwordFile != cfg.PasswordFile || fromConfig.binary != cfg.ResticBinary {
		t.Fatalf("config selection = %+v", fromConfig)
	}
	if got, err := fromConfig.hostID(newCmd("test", "")); err != nil || got != cfg.HostID {
		t.Fatalf("config host = (%q, %v)", got, err)
	}

	envRepo := filepath.Join(f.root, "env-repo")
	envPassword := filepath.Join(f.root, "env-password")
	t.Setenv("BABEL_RESTIC_REPO", envRepo)
	t.Setenv("BABEL_RESTIC_PASSWORD_FILE", envPassword)
	t.Setenv("BABEL_HOST_ID", "env-host")
	var fromEnv repoFlags
	open(&fromEnv)
	if fromEnv.repository != envRepo || fromEnv.passwordFile != envPassword {
		t.Fatalf("environment selection = %+v", fromEnv)
	}
	if got, err := fromEnv.hostID(newCmd("test", "")); err != nil || got != "env-host" {
		t.Fatalf("environment host = (%q, %v)", got, err)
	}

	fromFlags := repoFlags{
		repository:   filepath.Join(f.root, "flag-repo"),
		passwordFile: filepath.Join(f.root, "flag-password"),
		binary:       "/flag/restic",
		host:         "flag-host",
	}
	open(&fromFlags)
	if fromFlags.repository != filepath.Join(f.root, "flag-repo") || fromFlags.passwordFile != filepath.Join(f.root, "flag-password") || fromFlags.binary != "/flag/restic" {
		t.Fatalf("flag selection = %+v", fromFlags)
	}
	if got, err := fromFlags.hostID(newCmd("test", "")); err != nil || got != "flag-host" {
		t.Fatalf("flag host = (%q, %v)", got, err)
	}
}

func TestStorageStatusMissingAndPasswordPermissions(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))

	stdout, stderr := f.ok("storage", "status", "--json")
	missing := decode[storageStatusResult](t, stdout)
	if missing.Exists || missing.Path != config.Path() || stderr != "" {
		t.Fatalf("unconfigured status = %+v, stderr %q", missing, stderr)
	}

	password := filepath.Join(f.root, "password-status")
	if err := os.WriteFile(password, []byte("synthetic\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(config.Config{Repository: "repo", PasswordFile: password}); err != nil {
		t.Fatal(err)
	}
	stdout, stderr = f.ok("storage", "status", "--json")
	status := decode[storageStatusResult](t, stdout)
	if !status.Exists || !status.PasswordFileExists || status.PasswordFileSecure {
		t.Fatalf("insecure password status = %+v", status)
	}
	if !strings.Contains(stderr, "expected 0600 or stricter") {
		t.Fatalf("insecure password warning = %q", stderr)
	}
}

func TestStorageConfigureRejectsInvalidInputWithoutReplacement(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(f.root, "config"))
	good := config.Config{Repository: "repo", PasswordFile: filepath.Join(f.root, "password")}
	if err := config.Save(good); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"storage", "configure", "--from-json", "-"}, strings.NewReader(`{"repository":"replacement","password_file":"relative"}`), &stdout, &stderr)
	if code != exitFailure {
		t.Fatalf("invalid configure exited %d, stderr %q", code, stderr.String())
	}
	after, err := os.ReadFile(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("invalid configure replaced storage.json")
	}
}

func TestStorageDispatchUsage(t *testing.T) {
	f := newFixture(t)
	stdout, stderr := f.ok("--help")
	if !strings.Contains(stdout, "storage configure") || stderr != "" {
		t.Fatalf("root help omitted storage commands: stdout %q stderr %q", stdout, stderr)
	}
}
