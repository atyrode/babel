package e2e_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/adapter/claude"
	"github.com/atyrode/babel/internal/adapter/codex"
	"github.com/atyrode/babel/internal/adapter/omp"
)

// A snapshot holds every harness's tree at once, because `archive push` captures
// all of them under one host. So the property that matters for cross-host fetch
// is not that each adapter recognizes its own layout in isolation - each
// adapter's own package proves that - but that each adapter, handed the whole
// combined listing, claims exactly its own sessions and none of its siblings'.
//
// This is the one check no single adapter's test can make.
func TestIdentifyArchivedPartitionsACombinedListing(t *testing.T) {
	e := newEnv(t)
	e.writeSources(t)

	listing := listingOf(t, e.home)

	// Non-vacuity: the listing must actually contain all three trees, or
	// "each adapter claims only its own" proves nothing.
	for name, prefix := range map[string]string{
		"omp":    filepath.ToSlash(e.ompSessions),
		"codex":  filepath.ToSlash(e.codexHome),
		"claude": filepath.ToSlash(e.claudeHome),
	} {
		var found bool
		for _, f := range listing {
			if strings.HasPrefix(f.Path, prefix+"/") {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("listing holds no %s paths; the fixture cannot exercise partitioning", name)
		}
	}

	adapters := map[string]struct {
		id    adapter.SnapshotIdentifier
		roots []string
	}{
		"omp":    {omp.New(), omp.New().DefaultRoots()},
		"codex":  {codex.New(), codex.New().DefaultRoots()},
		"claude": {claude.New(), claude.New().DefaultRoots()},
	}

	claimed := map[string][]string{}
	for name, a := range adapters {
		got, err := a.id.IdentifyArchived(listing)
		if err != nil {
			t.Fatalf("%s: IdentifyArchived over a combined listing: %v", name, err)
		}
		if len(got) == 0 {
			t.Errorf("%s claimed nothing from a listing that contains its tree", name)
		}

		// Identity must match what a local scan of the same tree yields.
		discovered, err := a.id.(adapter.Adapter).Discover(context.Background(), a.roots)
		if err != nil {
			t.Fatalf("%s: Discover: %v", name, err)
		}
		wantIDs := make([]string, 0, len(discovered))
		for _, s := range discovered {
			wantIDs = append(wantIDs, s.SourceID)
		}
		gotIDs := make([]string, 0, len(got))
		for _, s := range got {
			gotIDs = append(gotIDs, s.SourceID)
		}
		slices.Sort(wantIDs)
		slices.Sort(gotIDs)
		if !slices.Equal(gotIDs, wantIDs) {
			t.Errorf("%s: identified %v from the snapshot listing, but Discover finds %v locally",
				name, gotIDs, wantIDs)
		}

		for _, s := range got {
			claimed[name] = append(claimed[name], s.PrimaryPath)
			for _, f := range s.Files {
				claimed[name] = append(claimed[name], f)
			}
		}
	}

	// No adapter may claim a path that belongs to another harness's tree. This
	// is what stops a cross-host fetch from restoring the wrong machine's
	// Codex rollouts as OMP sessions.
	homes := map[string]string{
		"omp":    filepath.ToSlash(filepath.Join(e.home, ".omp")) + "/",
		"codex":  filepath.ToSlash(e.codexHome) + "/",
		"claude": filepath.ToSlash(e.claudeHome) + "/",
	}
	for name, paths := range claimed {
		for _, p := range paths {
			for other, prefix := range homes {
				if other == name && strings.HasPrefix(p, prefix) {
					continue
				}
				if other != name && strings.HasPrefix(p, prefix) {
					t.Errorf("%s claimed %q, which lives in the %s tree", name, p, other)
				}
			}
		}
	}

	// And every claimed path must actually be in the listing: identification may
	// not invent a file a fetch would then fail to restore.
	inListing := make(map[string]bool, len(listing))
	for _, f := range listing {
		inListing[f.Path] = true
	}
	for name, paths := range claimed {
		for _, p := range paths {
			if !inListing[p] {
				t.Errorf("%s claimed %q, which is not in the snapshot listing", name, p)
			}
		}
	}
}

// An adapter handed a listing with none of its own paths must return nothing
// and no error, so one harness being absent from a host never fails a scan.
func TestIdentifyArchivedToleratesAMissingHarness(t *testing.T) {
	e := newEnv(t)
	e.writeCodexHome(t)

	listing := listingOf(t, e.home)
	for name, a := range map[string]adapter.SnapshotIdentifier{
		"omp":    omp.New(),
		"claude": claude.New(),
	} {
		got, err := a.IdentifyArchived(listing)
		if err != nil {
			t.Errorf("%s: error on a listing without its tree: %v", name, err)
		}
		if len(got) != 0 {
			t.Errorf("%s claimed %d sessions from a listing holding only Codex paths", name, len(got))
		}
	}
}

// listingOf walks a real tree into the shape a snapshot listing has: slash
// separated paths with recorded sizes, files only.
func listingOf(t *testing.T, root string) []adapter.ArchivedFile {
	t.Helper()
	var out []adapter.ArchivedFile
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := os.Stat(p)
		if err != nil {
			return err
		}
		out = append(out, adapter.ArchivedFile{
			Path: filepath.ToSlash(p),
			Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}
