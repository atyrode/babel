package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A failing restic is the failure an operator meets most often, and it used to
// arrive in restic's voice rather than Babel's. Babel asks restic for
// machine-readable output, and under --json restic reports a fatal error as one
// envelope object, so a mistyped password read
//
//	babel: list snapshots: restic snapshots: exit status 12: {"message_type":"exit_error","code":12,"message":"Fatal: wrong password or no key found"}
//
// in the middle of a sentence Babel wrote. What the operator has to know is at
// the end, behind a field name that means nothing to them.
func TestResticJSONErrorEnvelopeReadsAsASentence(t *testing.T) {
	f := newFixture(t).withRepo()
	f.bootstrapRepo()

	wrong := filepath.Join(f.root, "wrong-password")
	if err := os.WriteFile(wrong, []byte("not-the-repository-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr := f.mustExit(exitFailure,
		"archive", "status", "--repo", f.repoDir, "--password-file", wrong)

	if !strings.Contains(stderr, "wrong password or no key found") {
		t.Fatalf("the failure does not name the cause: %q", stderr)
	}
	for _, framing := range []string{"message_type", "exit_error", `{"`} {
		if strings.Contains(stderr, framing) {
			t.Errorf("restic's json envelope reached the operator (%q): %q", framing, stderr)
		}
	}
	// restic's exit status stays: it is the diagnostic that survives a
	// truncated message, and it is what `archive push` switches on to tell a
	// missing repository from an unopenable one.
	if !strings.Contains(stderr, "exit status 12") {
		t.Errorf("the failure dropped restic's exit code: %q", stderr)
	}
	// A repository that exists and will not open is not a repository that
	// needs creating, and suggesting init here would be a destructive answer
	// to a wrong-credential problem.
	if strings.Contains(stderr, "archive init") {
		t.Errorf("a wrong password was reported as a missing repository: %q", stderr)
	}
	assertInert(t, "status stderr", stderr)
}

// The restic executable is the one restic failure whose remedy is a setting
// rather than anything about the archive, and --restic-binary is that setting.
// It used to be reported as "fork/exec /some/restic: no such file or
// directory": accurate, and naming neither what Babel was attempting nor
// anything the operator could do about it.
//
// internal/restic reports the typed *restic.BinaryError and deliberately does
// not know the flag's name; this is the layer that does.
func TestUnrunnableResticBinaryNamesTheFlagThatSelectsIt(t *testing.T) {
	f := newFixture(t).withRepo()
	f.bootstrapRepo()

	notExecutable := filepath.Join(f.root, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		args    []string
		tried   string
		clearPA bool
	}{
		{
			name:  "a named path that does not exist",
			args:  []string{"--restic-binary", filepath.Join(f.root, "absent", "restic")},
			tried: filepath.Join(f.root, "absent", "restic"),
		},
		{
			// A file that is there and still cannot be run: the operator
			// pointed at the wrong thing, not at nothing.
			name:  "a named path that is not executable",
			args:  []string{"--restic-binary", notExecutable},
			tried: notExecutable,
		},
		{
			// No flag, nothing on PATH: the remedy is the same one, and the
			// message has to name it even though the operator named nothing.
			name:    "nothing on PATH and no flag",
			tried:   "restic",
			clearPA: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.clearPA {
				t.Setenv("PATH", filepath.Join(f.root, "empty-bin"))
			}
			_, stderr := f.mustExit(exitFailure, f.with(append([]string{"archive", "status"}, tc.args...)...)...)

			if !strings.Contains(stderr, "--restic-binary") {
				t.Errorf("the failure names no remedy: %q", stderr)
			}
			if !strings.Contains(stderr, tc.tried) {
				t.Errorf("the failure does not say which executable was tried (%q): %q", tc.tried, stderr)
			}
			// The command Babel was running is part of the answer: "which
			// operation died" is what tells an operator whether anything was
			// written.
			if !strings.Contains(stderr, "list snapshots") {
				t.Errorf("the failure does not name what was attempted: %q", stderr)
			}
			if strings.Contains(stderr, "fork/exec") {
				t.Errorf("the failure still reads as a raw exec error: %q", stderr)
			}
			assertInert(t, "status stderr", stderr)
		})
	}
}

// `verify --deep` exists for restic's own diagnostic: it names the damaged pack
// and the `restic repair packs` command that removes it, which is the whole
// value of paying for a full re-read. Rendering restic's failures in Babel's
// voice may reframe restic's words and must never shorten them, so this asserts
// the guidance arrives whole rather than merely that the check failed.
func TestVerifyDeepKeepsResticsRepairGuidance(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	f.bootstrapRepo()
	f.ok(f.with("archive", "push")...)

	pack := largestPack(t, filepath.Join(f.repoDir, "data"))
	flipOneByte(t, pack)
	// restic serves metadata from its cache; a deep check must read the
	// repository itself, so the cache is dropped first.
	if err := os.RemoveAll(f.cacheDir); err != nil {
		t.Fatal(err)
	}

	_, stderr := f.mustExit(exitFailure, f.with("archive", "verify", "--deep")...)
	for _, want := range []string{
		// What is damaged, named precisely enough to act on.
		filepath.Base(pack),
		"damaged pack files",
		// The remedy, in restic's own words, both commands of it.
		"restic repair packs",
		"restic repair snapshots --forget",
		// And restic's verdict, which is the last line of the report.
		"repository contains errors",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("deep verify lost %q, so the operator loses the remedy: %q", want, stderr)
		}
	}
	assertInert(t, "deep verify stderr", stderr)
}
