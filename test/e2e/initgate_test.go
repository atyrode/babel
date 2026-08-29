package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Creating the repository is an explicit operator act, and this is the test that
// keeps it that way.
//
// It used to be a side effect of `archive push`, which is the unattended hourly
// command. Two hazards followed, and the second is the worse one:
//
//   - restic generates a master key per `init` and writes the key before the
//     config, so two inits racing on an empty repository both succeed and leave
//     two valid keys with one config. restic then selects a key by iteration and
//     fails outright when it picks the wrong one. Measured against restic 0.19.1:
//     10 of 10 races left two keys, and 7 of 10 subsequent backups failed with
//     "config or key <id> is damaged: ciphertext verification failed". Two
//     machines' timers firing together at a new repository is exactly that race.
//
//   - a mistyped locator silently became a brand-new empty archive. Hourly
//     pushes would keep succeeding into it while the real archive appeared to
//     stop growing — a failure that reports success, which is worse than one
//     that stops.
func TestPushRefusesToCreateTheRepository(t *testing.T) {
	e := newEnv(t)
	e.writeSources(t)

	stdout, stderr, code := e.run(t, e.with("archive", "push", "--json")...)
	if code == exitOK {
		t.Fatalf("a push created the repository instead of refusing:\nstdout:\n%s", stdout)
	}
	if !strings.Contains(stderr, "babel archive init") {
		t.Fatalf("the refusal does not name the command that creates one: %s", stderr)
	}
	// Nothing was written where the repository would have gone. A refusal that
	// left a half-made repository behind would be worse than the auto-init.
	if entries, err := os.ReadDir(e.repoDir); err == nil && len(entries) > 0 {
		t.Fatalf("the refused push left %d entries in %s", len(entries), e.repoDir)
	}

	// The explicit step creates it, says so, and is idempotent.
	first := okJSON[initResultDoc](t, e, e.with("archive", "init", "--json")...)
	if !first.Created {
		t.Fatalf("the first init did not report creating the repository: %+v", first)
	}
	if _, err := os.Stat(filepath.Join(e.repoDir, "config")); err != nil {
		t.Fatalf("init reported success without a repository config: %v", err)
	}
	second := okJSON[initResultDoc](t, e, e.with("archive", "init", "--json")...)
	if second.Created {
		t.Fatalf("a second init claimed to create an existing repository: %+v", second)
	}

	// And the push that was refused now works, which is what makes the bootstrap
	// a step rather than an obstacle.
	push := okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)
	if push.SnapshotID == "" {
		t.Fatalf("the push after init produced no snapshot: %+v", push)
	}
}

// initResultDoc mirrors `archive init --json`.
type initResultDoc struct {
	Repository string `json:"repository"`
	Created    bool   `json:"created"`
}

// A repository that exists but cannot be opened is a different failure from one
// that does not exist, and it must not be treated as "needs initializing":
// initializing over a real repository the password does not open would be a
// destructive answer to a wrong-credential problem.
func TestPushDistinguishesAWrongPasswordFromAMissingRepository(t *testing.T) {
	e := newEnv(t)
	e.writeSources(t)
	e.ok(t, e.with("archive", "init")...)

	wrong := filepath.Join(e.root, "wrong-password")
	if err := os.WriteFile(wrong, []byte("not-the-repository-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, stderr, code := e.run(t, "archive", "push",
		"--repo", e.repoDir, "--password-file", wrong, "--json")
	if code == exitOK {
		t.Fatal("a push with the wrong password succeeded")
	}
	if strings.Contains(stderr, "babel archive init") {
		t.Fatalf("a wrong password was reported as a missing repository: %s", stderr)
	}

	// The repository is untouched: still openable with the real password.
	if _, err := os.Stat(filepath.Join(e.repoDir, "config")); err != nil {
		t.Fatalf("the failed push disturbed the repository: %v", err)
	}
	push := okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)
	if push.SnapshotID == "" {
		t.Fatalf("the repository stopped working after a wrong-password attempt: %+v", push)
	}
}
