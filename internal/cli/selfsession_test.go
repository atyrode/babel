package cli

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/atyrode/babel/internal/adapter/babelself"
)

// TestArchivePushCapturesBabelsOwnSessions is the whole of what registering
// the adapter had to achieve on the storage side.
//
// `archive push` snapshots exactly existingRoots(): the union of every
// registered adapter's BackupRoots that exists as a directory on this host.
// Babel's own analysis sessions are therefore archived because the adapter is
// in adapters() and its backup root is where a run writes — and by no other
// arrangement. No storage configuration names a path, so an adapter left
// unregistered, or one whose BackupRoots pointed anywhere but the write root,
// would leave every recorded conversation out of every snapshot on every
// machine, silently.
//
// It asserts the root's presence rather than pushing, because restic is not
// what is under test: the set handed to it is.
func TestArchivePushCapturesBabelsOwnSessions(t *testing.T) {
	f := newFixture(t)

	root, ok := babelself.Root()
	if !ok {
		t.Fatal("no analysis-session root resolves under the fixture's XDG_DATA_HOME")
	}
	if want := filepath.Join(f.dataDir, "analysis"); root != want {
		t.Errorf("analysis root = %q, want %q under Babel's own data directory", root, want)
	}

	// A machine that has run no analysis has no root to capture, and that is
	// the documented behaviour of existingRoots rather than a gap: harness
	// coverage is a property of the machine.
	if slices.Contains(existingRoots(), root) {
		t.Errorf("existingRoots() named %q before any analysis session was written", root)
	}

	if err := os.MkdirAll(filepath.Join(root, "run-synthetic"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := existingRoots(); !slices.Contains(got, root) {
		t.Errorf("existingRoots() = %v, which omits the analysis root %q; archive push would snapshot no Babel session", got, root)
	}
}
