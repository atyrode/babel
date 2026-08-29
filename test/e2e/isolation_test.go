package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The suite must not read the operator's real storage.json.
//
// `os.UserConfigDir` prefers $XDG_CONFIG_HOME over HOME, so a suite that
// isolates HOME but not XDG_CONFIG_HOME reads whatever configuration the
// developer or CI happens to have. That was harmless while no production
// configuration existed anywhere. It stops being harmless the moment one does:
// a shared-mode document carries a real repository locator and a real catalog
// DSN, so a command resolving through configuration rather than explicit flags
// would address the operator's actual Cellar bucket and PostgreSQL — from a test.
//
// internal/cli's fixture already carries this guard, its comment recording that
// two unrelated tests once observed a configuration they never wrote. This suite
// drives the same commands, so it needs the same isolation, and this is the test
// that keeps it.
func TestEnvironmentIgnoresAnOutsideConfiguration(t *testing.T) {
	// A configuration that would be unmistakable if it were read: the locator is
	// syntactically fine and nothing like a fixture path, so an error naming it
	// proves the leak, and an error not naming it proves the isolation.
	const outsideRepo = "s3:https://outside.invalid/operator-bucket/babel/v1"
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outside, "babel"), 0o700); err != nil {
		t.Fatal(err)
	}
	doc := `{
  "config_schema": 2,
  "mode": "shared",
  "repository": "` + outsideRepo + `",
  "password_file": "/nonexistent/outside-password",
  "host_id": "outsidehost",
  "deployment_id": "outside-deployment",
  "instance_id": "outside-instance",
  "repository_store": {"access_key_id": "OUTSIDEKEYID", "secret_access_key": "outside-secret"},
  "catalog": {"host": "outside.invalid", "port": 5432, "database": "outside",
              "user": "outside", "password": "outside-password", "tls_mode": "require"}
}`
	if err := os.WriteFile(filepath.Join(outside, "babel", "storage.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	// Set before newEnv, exactly as a developer's shell would have it.
	t.Setenv("XDG_CONFIG_HOME", outside)

	e := newEnv(t)

	// A command that resolves its repository through configuration when no flag
	// supplies one. With isolation it finds nothing configured; without it, it
	// finds the outside document and addresses that bucket.
	stdout, stderr, code := e.run(t, "archive", "status")
	if code == exitOK {
		t.Fatalf("archive status succeeded with no repository configured, which means it found one:\n%s", stdout)
	}
	if strings.Contains(stdout+stderr, "outside") {
		t.Fatalf("the suite read a configuration outside its own environment:\n%s", stdout+stderr)
	}
	if !strings.Contains(stderr, "no restic repository selected") {
		t.Fatalf("expected the unconfigured refusal, got: %s", stderr)
	}

	// And the environment's own configuration directory is where a command
	// installs one, so a test that configures storage stays self-contained.
	if got := filepath.Dir(e.configHome); got != e.root {
		t.Fatalf("config home %q is not directly inside the environment root %q", e.configHome, e.root)
	}
}
