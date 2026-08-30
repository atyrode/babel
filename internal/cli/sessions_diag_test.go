package cli

import (
	"os"
	"strings"
	"testing"
)

// A session gone from this machine's disk is still in the repository — that
// is the whole reason the archive exists — and `sessions fetch SELECTOR` used
// to report only "no local session matches selector", never naming --host,
// the one flag that recovers it.
//
// The property defended here is that the failure names its own remedy, and
// that the remedy is true: the same selector that fails locally succeeds
// under --host against the snapshot taken before the files were deleted. The
// message is also required to admit what it does not know, because a
// local-only resolution cannot tell "archived elsewhere" from "no such
// session" without reading some host's snapshot listing.
func TestSessionsFetchNamesHostWhenLocalFilesAreGone(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	f.bootstrapRepo()
	f.ok(f.with("archive", "push")...)

	// The archive now holds the session; the machine no longer does.
	if err := os.RemoveAll(f.sessionsDir); err != nil {
		t.Fatal(err)
	}

	_, stderr := f.mustExit(exitFailure, f.with("sessions", "fetch", richSessionStem)...)
	for _, want := range []string{
		"no local session matches",
		richSessionStem,
		"--host",
		"cannot tell whether the archive still holds the session",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("fetch failure %q does not mention %q", stderr, want)
		}
	}

	// The named remedy actually recovers the session, so the message is a
	// direction rather than a decoration.
	stdout, _ := f.ok(f.with("sessions", "fetch", "--host", testHostID, richSessionStem, "--json")...)
	recovered := decode[fetchResult](t, stdout)
	if recovered.Files == 0 || !strings.HasSuffix(recovered.Selector, richSessionStem) {
		t.Fatalf("--host fetch did not recover the session: %+v", recovered)
	}
}

// The two other selector failures have their own remedies — qualify the
// selector, or supply one — and --host helps neither. Rewriting them would
// send an operator to the repository for a mistake on the command line.
func TestSessionsFetchLeavesOtherSelectorFailuresAlone(t *testing.T) {
	err := hintHostFetch(newCmd("sessions fetch", "").usagef("empty selector"), "")
	if strings.Contains(err.Error(), "--host") {
		t.Fatalf("a usage failure was rewritten with the archive remedy: %v", err)
	}
}
