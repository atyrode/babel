package e2e_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The point of cross-host fetch is addressing a session whose files this
// machine does not have. So this test removes the source tree entirely after
// archiving it, which is the same situation as a session that only ever existed
// on another machine: the selector cannot be resolved from anything local.
func TestFetchResolvesASessionThatIsNoLongerLocal(t *testing.T) {
	e := newEnv(t)
	src := e.writeSources(t)

	sourceBytes := readFile(t, src.richPrimary)
	artifactRel := []string{"Helper.jsonl", filepath.Join("nested", "7.bash.log")}
	artifactBytes := make(map[string][]byte, len(artifactRel))
	for _, rel := range artifactRel {
		artifactBytes[rel] = readFile(t, filepath.Join(src.richArtifactDir, rel))
	}

	e.bootstrapRepo(t)
	push := okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)
	if push.Incomplete {
		t.Fatalf("push reported an incomplete backup: %+v", push)
	}

	// Erase every local trace of the OMP sessions tree. The blob store is left
	// alone: it is a separate backup root, and a blob is not addressable by a
	// session selector anyway.
	if err := os.RemoveAll(e.ompSessions); err != nil {
		t.Fatal(err)
	}

	// A local fetch must now fail, which is what makes the cross-host path
	// worth having rather than a redundant flag.
	_, stderr, code := e.run(t, e.with("sessions", "fetch", src.richSelector, "--json")...)
	if code == 0 {
		t.Fatal("a local fetch succeeded for a session with no local files")
	}
	if !strings.Contains(stderr, "no local session matches selector") {
		t.Fatalf("local fetch failed for the wrong reason: %s", stderr)
	}

	// Cross-host fetch identifies it from the snapshot's listing instead.
	res := okJSON[fetchResult](t, e,
		e.with("sessions", "fetch", src.richSelector, "--host", hostID, "--json")...)
	if res.Selector != src.richSelector {
		t.Fatalf("cross-host fetch resolved %q, want %q", res.Selector, src.richSelector)
	}
	if res.SnapshotID != push.SnapshotID {
		t.Fatalf("fetched snapshot %q, want the pushed %q", res.SnapshotID, push.SnapshotID)
	}
	if res.AlreadyPresent {
		t.Fatalf("first cross-host fetch claimed the target existed: %+v", res)
	}
	if len(res.Missing) != 0 {
		t.Fatalf("closure reported missing paths although the snapshot holds them: %v", res.Missing)
	}
	if res.Files == 0 {
		t.Fatal("cross-host fetch restored no files")
	}

	// Byte-exact: the whole point is recovering what was archived, not an
	// approximation of it.
	if got := readFile(t, filepath.Join(res.Target, src.richPrimary)); !bytes.Equal(got, sourceBytes) {
		t.Fatalf("restored primary log differs from the archived bytes (%d vs %d)",
			len(got), len(sourceBytes))
	}
	for rel, want := range artifactBytes {
		got := readFile(t, filepath.Join(res.Target, src.richArtifactDir, rel))
		if !bytes.Equal(got, want) {
			t.Errorf("restored artifact %s differs from the archived bytes (%d vs %d)",
				rel, len(got), len(want))
		}
	}

	// Idempotent on the cross-host path too.
	again := okJSON[fetchResult](t, e,
		e.with("sessions", "fetch", src.richSelector, "--host", hostID, "--json")...)
	if !again.AlreadyPresent {
		t.Fatalf("second cross-host fetch re-restored the session: %+v", again)
	}
}

// Selecting a host that has published nothing must name the hosts that have,
// rather than silently falling back to another machine's snapshots.
func TestFetchRejectsAnUnknownHost(t *testing.T) {
	e := newEnv(t)
	src := e.writeSources(t)
	e.bootstrapRepo(t)
	okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)

	_, stderr, code := e.run(t,
		e.with("sessions", "fetch", src.richSelector, "--host", "not-a-host", "--json")...)
	if code == 0 {
		t.Fatal("fetch accepted a host with no snapshots")
	}
	if !strings.Contains(stderr, "no snapshots for host") {
		t.Fatalf("error does not explain the missing host: %s", stderr)
	}
	if !strings.Contains(stderr, hostID) {
		t.Fatalf("error does not name the hosts that do have snapshots: %s", stderr)
	}
}

// A selector that matches nothing in the chosen snapshot must say so, and must
// not fall back to a local scan.
func TestCrossHostFetchRejectsAnUnknownSelector(t *testing.T) {
	e := newEnv(t)
	e.writeSources(t)
	e.bootstrapRepo(t)
	okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)

	_, stderr, code := e.run(t,
		e.with("sessions", "fetch", "omp/nope/absent", "--host", hostID, "--json")...)
	if code == 0 {
		t.Fatal("cross-host fetch accepted a selector no archived session matches")
	}
	if !strings.Contains(stderr, "no session in the selected snapshot matches selector") {
		t.Fatalf("error does not explain the miss: %s", stderr)
	}
}
