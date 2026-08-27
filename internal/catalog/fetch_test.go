package catalog

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

// failingStore fails Read for one key, standing in for a remote that dies
// mid-fetch. Every other operation passes through.
type failingStore struct {
	objectstore.Store
	failKey string
	reads   int
}

func (s *failingStore) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	s.reads++
	if key == s.failKey {
		return nil, errors.New("injected read failure")
	}
	return s.Store.Read(ctx, key)
}

// deltaBundle publishes one session whose newest revision is an append
// delta over a full parent, with one artifact and one blob.
func deltaBundle(t *testing.T) (*fixture, archive.ManifestEntry, archive.ManifestEntry, []byte) {
	t.Helper()
	f := newFixture(t)

	base := f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("line-one\nline-two\n"))
	base.Artifacts = []archive.FileRef{f.artifact("siblings/notes.md", []byte("sibling artifact\n"))}
	base.Blobs = []archive.ObjectRef{f.put([]byte("blob-bytes\n"))}
	f.commit("host-a", 1, []archive.ManifestEntry{base})

	delta := f.delta(base, 2, []byte("line-three\n"))
	f.commit("host-a", 2, []archive.ManifestEntry{base, delta})

	return f, base, delta, f.plain[delta.RevisionKey]
}

func TestFetchReassemblesChain(t *testing.T) {
	f, base, delta, want := deltaBundle(t)
	c := mustLoad(t, f.st)
	rev, err := c.Resolve(base.SessionKey)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if rev.Key() != delta.RevisionKey {
		t.Fatalf("resolved %s, want the delta revision", rev.Key())
	}

	dest := filepath.Join(t.TempDir(), "bundle")
	m, err := Fetch(context.Background(), f.st, rev, dest)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if m.ChainLength != 2 || m.Encoding != archive.EncodingAppendDelta {
		t.Errorf("materialized chain length %d encoding %s, want 2 and append-delta", m.ChainLength, m.Encoding)
	}
	if len(m.Files) != 3 {
		t.Fatalf("materialized %d files, want primary + artifact + blob: %+v", len(m.Files), m.Files)
	}

	primary := m.Files[0]
	if primary.Kind != KindPrimary || primary.Path != "0001-alpha.jsonl" {
		t.Errorf("primary file = %+v, want a safe-named primary", primary)
	}
	got, err := os.ReadFile(filepath.Join(dest, primary.Path))
	if err != nil {
		t.Fatalf("read primary: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("reassembled plaintext = %q, want %q", got, want)
	}
	if d := archive.DigestBytes(got); d != delta.Content.Digest {
		t.Errorf("reassembled digest %s, want the recorded content digest %s", d, delta.Content.Digest)
	}
	if primary.Digest != delta.Content.Digest || primary.Size != delta.Content.Size {
		t.Errorf("reported primary %+v disagrees with the manifest content %+v", primary, delta.Content)
	}

	artifact := m.Files[1]
	if artifact.Kind != KindArtifact || artifact.Path != "siblings/notes.md" {
		t.Errorf("artifact file = %+v", artifact)
	}
	if b, err := os.ReadFile(filepath.Join(dest, "siblings/notes.md")); err != nil || string(b) != "sibling artifact\n" {
		t.Errorf("artifact contents = %q, err %v", b, err)
	}

	blob := m.Files[2]
	wantBlob := filepath.Join(dest, blobsDir, base.Blobs[0].Digest.Hex())
	if blob.Kind != KindBlob || filepath.Join(dest, blob.Path) != wantBlob {
		t.Errorf("blob file = %+v, want %s", blob, wantBlob)
	}
	if b, err := os.ReadFile(wantBlob); err != nil || string(b) != "blob-bytes\n" {
		t.Errorf("blob contents = %q, err %v", b, err)
	}
	if m.TotalSize != primary.Size+artifact.Size+blob.Size {
		t.Errorf("total size %d disagrees with the file list", m.TotalSize)
	}

	// A fetched bundle is immutable and is never silently replaced.
	if _, err := Fetch(context.Background(), f.st, rev, dest); !errors.Is(err, ErrDestExists) {
		t.Errorf("second Fetch error = %v, want ErrDestExists", err)
	}

	// The exact parent revision remains fetchable on its own.
	parent, err := c.Resolve(base.RevisionKey)
	if err != nil {
		t.Fatalf("Resolve(parent): %v", err)
	}
	parentDest := filepath.Join(t.TempDir(), "parent")
	pm, err := Fetch(context.Background(), f.st, parent, parentDest)
	if err != nil {
		t.Fatalf("Fetch(parent): %v", err)
	}
	if pm.ChainLength != 1 {
		t.Errorf("full revision chain length %d, want 1", pm.ChainLength)
	}
	if b, err := os.ReadFile(filepath.Join(parentDest, pm.Files[0].Path)); err != nil || string(b) != string(f.plain[base.RevisionKey]) {
		t.Errorf("parent plaintext = %q, err %v", b, err)
	}
}

func TestFetchFailureLeavesNoBundle(t *testing.T) {
	f, base, _, _ := deltaBundle(t)
	c := mustLoad(t, f.st)
	rev, err := c.Resolve(base.SessionKey)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// The blob is read last, so the primary and artifact are already
	// staged when the remote fails.
	st := &failingStore{Store: f.st, failKey: archive.CASKey(base.Blobs[0].Digest)}
	parent := t.TempDir()
	dest := filepath.Join(parent, "bundle")
	if _, err := Fetch(context.Background(), st, rev, dest); err == nil {
		t.Fatal("Fetch succeeded despite an injected read failure")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination exists after a failed fetch: %v", err)
	}
	leftovers, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if len(leftovers) != 0 {
		names := make([]string, len(leftovers))
		for i, e := range leftovers {
			names[i] = e.Name()
		}
		t.Errorf("staging leftovers: %v", names)
	}

	// A cancelled fetch is equally clean.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelDest := filepath.Join(parent, "cancelled")
	if _, err := Fetch(ctx, f.st, rev, cancelDest); err == nil {
		t.Error("cancelled Fetch succeeded")
	}
	if _, err := os.Stat(cancelDest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cancelled fetch left a bundle: %v", err)
	}

	// Once the remote is healthy again the same revision fetches.
	if _, err := Fetch(context.Background(), f.st, rev, dest); err != nil {
		t.Errorf("retry after failure: %v", err)
	}
}

func TestFetchRejectsUnsafeArtifactPath(t *testing.T) {
	f := newFixture(t)
	e := f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("alpha\n"))
	e.Artifacts = []archive.FileRef{f.artifact("../x", []byte("escaped\n"))}
	f.commit("host-a", 1, []archive.ManifestEntry{e})

	rev, err := mustLoad(t, f.st).Resolve(e.SessionKey)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	parent := t.TempDir()
	dest := filepath.Join(parent, "bundle")
	_, err = Fetch(context.Background(), f.st, rev, dest)
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("Fetch error = %v, want ErrInvalidPath", err)
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("error does not name the offending path: %v", err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination exists after a rejected fetch: %v", err)
	}
	if entries, _ := os.ReadDir(parent); len(entries) != 0 {
		t.Errorf("rejected fetch wrote %d entries beside the destination", len(entries))
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(parent), "x")); err == nil {
		t.Error("artifact escaped the destination")
	}

	for _, bad := range []string{"", "/etc/passwd", "a/../../b", "a//b", `a\b`, "./x", "x/."} {
		if err := validRelPath(bad); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("validRelPath(%q) = %v, want ErrInvalidPath", bad, err)
		}
	}
	for _, ok := range []string{"x", "a/b/c.md", "blobs/deadbeef", "a-b_c.1"} {
		if err := validRelPath(ok); err != nil {
			t.Errorf("validRelPath(%q) = %v, want nil", ok, err)
		}
	}
}

func TestFetchRejectsUnverifiableChain(t *testing.T) {
	f := newFixture(t)
	base := f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("alpha-base\n"))
	delta := f.delta(base, 2, []byte("alpha-tail\n"))
	// The parent is missing from the generation, so the chain cannot be
	// resolved and no partial transcript may be produced.
	f.commit("host-a", 2, []archive.ManifestEntry{delta})

	rev, err := mustLoad(t, f.st).Resolve(delta.RevisionKey)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "bundle")
	if _, err := Fetch(context.Background(), f.st, rev, dest); err == nil {
		t.Fatal("Fetch reassembled an unresolvable chain")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination exists after an unresolvable chain: %v", err)
	}
}

func TestFetchRejectsChainBeyondDepthBound(t *testing.T) {
	f := newFixture(t)
	entries := []archive.ManifestEntry{f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("base\n"))}
	for i := range archive.MaxChainDepth {
		entries = append(entries, f.delta(entries[i], uint64(i+2), []byte("tail\n")))
	}
	f.commit("host-a", uint64(len(entries)), entries)

	newest := entries[len(entries)-1]
	c := mustLoad(t, f.st)
	rev, err := c.Resolve(newest.RevisionKey)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "bundle")
	if _, err := Fetch(context.Background(), f.st, rev, dest); err == nil ||
		!strings.Contains(err.Error(), "exceeds the") {
		t.Fatalf("Fetch error = %v, want the chain-depth bound", err)
	}

	// The deepest revision still inside the bound reassembles.
	inBound := entries[archive.MaxChainDepth-1]
	rev, err = c.Resolve(inBound.RevisionKey)
	if err != nil {
		t.Fatalf("Resolve(in-bound): %v", err)
	}
	m, err := Fetch(context.Background(), f.st, rev, filepath.Join(t.TempDir(), "bundle"))
	if err != nil {
		t.Fatalf("Fetch(in-bound): %v", err)
	}
	if m.ChainLength != archive.MaxChainDepth {
		t.Errorf("chain length %d, want the bound %d", m.ChainLength, archive.MaxChainDepth)
	}
}

func TestFetchRejectsTamperedPayload(t *testing.T) {
	f, base, _, _ := deltaBundle(t)
	f.flip(archive.CASKey(base.Object.Digest))

	rev, err := mustLoad(t, f.st).Resolve(base.SessionKey)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "bundle")
	if _, err := Fetch(context.Background(), f.st, rev, dest); err == nil {
		t.Fatal("Fetch accepted a tampered payload object")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("destination exists after a digest failure: %v", err)
	}
}

func TestFetchRejectsUnloadedAppendDelta(t *testing.T) {
	f, base, delta, _ := deltaBundle(t)
	// A revision value built outside Load carries no generation, so an
	// append delta cannot be reassembled from it.
	orphan := Revision{Entry: delta, HostID: delta.HostID, Generation: 2}
	if _, err := Fetch(context.Background(), f.st, orphan, filepath.Join(t.TempDir(), "bundle")); err == nil {
		t.Fatal("Fetch reassembled an append delta with no generation")
	}
	// A full revision needs no siblings and fetches fine.
	full := Revision{Entry: base, HostID: base.HostID, Generation: 1}
	if _, err := Fetch(context.Background(), f.st, full, filepath.Join(t.TempDir(), "bundle")); err != nil {
		t.Errorf("Fetch(full revision without a generation): %v", err)
	}
}

func TestSafeName(t *testing.T) {
	cases := map[string]string{
		"sessions/0001-alpha":    "0001-alpha",
		"0002":                   "0002",
		"..":                     "session",
		"":                       "session",
		"weird name/../thing":    "thing",
		"trailing/":              "session",
		strings.Repeat("a", 200): strings.Repeat("a", 96),
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}
