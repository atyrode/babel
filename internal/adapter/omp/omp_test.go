package omp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/archive"
)

// realRootsEnv opts the real-tree smoke test in. Every adapter package
// uses the same switch so one environment variable enables them all.
const realRootsEnv = "BABEL_SMOKE_REAL_ROOTS"

// fixtureRoot copies the synthetic fixture tree into a temporary
// directory and returns the sessions root inside the copy. Tests never
// read the operator's real tree and never mutate testdata.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	copyTree(t, filepath.Join("testdata", "root"), dst)
	return filepath.Join(dst, "agent", "sessions")
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatalf("copy fixture tree: %v", err)
	}
}

func discover(t *testing.T, root string) []adapter.SourceSession {
	t.Helper()
	sessions, err := New().Discover(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return sessions
}

// session returns the discovered session whose source id starts with
// prefix.
func session(t *testing.T, sessions []adapter.SourceSession, prefix string) adapter.SourceSession {
	t.Helper()
	for _, s := range sessions {
		if strings.HasPrefix(s.SourceID, prefix) {
			return s
		}
	}
	t.Fatalf("no discovered session with source-id prefix %q", prefix)
	return adapter.SourceSession{}
}

func snapshot(t *testing.T, a *Adapter, src adapter.SourceSession) *adapter.Snapshot {
	t.Helper()
	snap, err := a.Snapshot(context.Background(), src, filepath.Join(t.TempDir(), "staging"))
	if err != nil {
		t.Fatalf("Snapshot(%s): %v", src.SourceID, err)
	}
	return snap
}

func digestOf(t *testing.T, path string) archive.Digest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return archive.NewDigest(sha256.Sum256(data))
}

func TestPortIdentity(t *testing.T) {
	t.Parallel()
	var a adapter.Adapter = New()
	if got := a.Harness(); got != "omp" {
		t.Errorf("Harness() = %q, want omp", got)
	}
	if got := a.Schema(); got != 1 {
		t.Errorf("Schema() = %d, want 1", got)
	}
	roots := a.DefaultRoots()
	if len(roots) != 1 || !strings.HasSuffix(filepath.ToSlash(roots[0]), "/.omp/agent/sessions") {
		t.Errorf("DefaultRoots() = %v, want one ~/.omp/agent/sessions entry", roots)
	}
}

func TestDiscoverFindsSyntheticSessions(t *testing.T) {
	t.Parallel()
	root := fixtureRoot(t)
	sessions := discover(t, root)

	if len(sessions) != 4 {
		var ids []string
		for _, s := range sessions {
			ids = append(ids, s.SourceID)
		}
		t.Fatalf("discovered %d sessions (%v), want 4", len(sessions), ids)
	}
	for i, s := range sessions {
		if s.Harness != "omp" {
			t.Errorf("session %d harness = %q, want omp", i, s.Harness)
		}
		if !archive.ValidSourceID(s.SourceID) {
			t.Errorf("session %d source id %q is not a valid source id", i, s.SourceID)
		}
		if i > 0 && sessions[i-1].SourceID >= s.SourceID {
			t.Errorf("sessions not in ascending source-id order at %d: %q then %q", i, sessions[i-1].SourceID, s.SourceID)
		}
		if filepath.Dir(filepath.Dir(s.PrimaryPath)) != root {
			t.Errorf("session %d primary %q is not one level below the root", i, s.PrimaryPath)
		}
		// A session's sibling artifact tree contains JSONL files too;
		// none of them may be discovered as a session.
		if strings.Contains(filepath.ToSlash(s.PrimaryPath), "_00000000-0000-4000-8000-000000000001/") {
			t.Errorf("session %d is an artifact file, not a primary log: %q", i, s.PrimaryPath)
		}
	}

	// A second scan of the same tree must produce identical identities.
	again := discover(t, root)
	if len(again) != len(sessions) {
		t.Fatalf("second scan found %d sessions, want %d", len(again), len(sessions))
	}
	for i := range sessions {
		if again[i].SourceID != sessions[i].SourceID {
			t.Errorf("source id %d unstable: %q then %q", i, sessions[i].SourceID, again[i].SourceID)
		}
	}
}

func TestDiscoverSkipsMissingRootsAndDeduplicates(t *testing.T) {
	t.Parallel()
	root := fixtureRoot(t)
	sessions, err := New().Discover(context.Background(), []string{
		filepath.Join(t.TempDir(), "absent"),
		root,
		root,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 4 {
		t.Fatalf("discovered %d sessions, want 4 after deduplication", len(sessions))
	}
}

func TestSourceIDSanitizesUnsafePathComponents(t *testing.T) {
	t.Parallel()
	sessions := discover(t, fixtureRoot(t))
	got := session(t, sessions, "-synthetic-dir-odd-")
	first, _, ok := strings.Cut(got.SourceID, "/")
	if !ok {
		t.Fatalf("source id %q has no project segment", got.SourceID)
	}
	// "-synthetic dir+odd" cannot appear verbatim: the space and plus are
	// outside the source-id alphabet, so they are replaced and a digest
	// of the original component keeps the id injective.
	if strings.ContainsAny(first, " +") {
		t.Errorf("project segment %q still carries unsafe bytes", first)
	}
	if len(first) != len("-synthetic-dir-odd")+9 {
		t.Errorf("project segment %q is not the sanitized name plus a digest suffix", first)
	}
	if !archive.ValidSourceID(got.SourceID) {
		t.Errorf("sanitized source id %q is invalid", got.SourceID)
	}
}

func TestSnapshotStagesPrimaryArtifactsAndBlobs(t *testing.T) {
	t.Parallel()
	root := fixtureRoot(t)
	src := session(t, discover(t, root), "-synthetic-project/")
	snap := snapshot(t, New(), src)

	// Primary log: staged bytes are the source bytes.
	sourceDigest := digestOf(t, src.PrimaryPath)
	if got := digestOf(t, snap.StagedPrimary); got != sourceDigest {
		t.Errorf("staged primary digest %s, want %s", got, sourceDigest)
	}
	info, err := os.Stat(src.PrimaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if snap.PrimarySize != info.Size() {
		t.Errorf("PrimarySize = %d, want %d", snap.PrimarySize, info.Size())
	}
	if filepath.Base(snap.StagedPrimary) != filepath.Base(src.PrimaryPath) {
		t.Errorf("staged primary name %q, want %q", filepath.Base(snap.StagedPrimary), filepath.Base(src.PrimaryPath))
	}

	// Artifact tree: source-relative paths, staged copies present.
	wantRel := []string{"Helper.jsonl", "nested/7.bash.log"}
	if len(snap.Artifacts) != len(wantRel) {
		t.Fatalf("staged %d artifacts, want %d", len(snap.Artifacts), len(wantRel))
	}
	artifactDir := strings.TrimSuffix(src.PrimaryPath, ".jsonl")
	for i, artifact := range snap.Artifacts {
		if artifact.RelPath != wantRel[i] {
			t.Errorf("artifact %d rel path = %q, want %q", i, artifact.RelPath, wantRel[i])
		}
		want := digestOf(t, filepath.Join(artifactDir, filepath.FromSlash(artifact.RelPath)))
		if got := digestOf(t, artifact.StagedPath); got != want {
			t.Errorf("artifact %s staged digest %s, want %s", artifact.RelPath, got, want)
		}
		staged, err := os.Stat(artifact.StagedPath)
		if err != nil {
			t.Fatal(err)
		}
		if artifact.Size != staged.Size() {
			t.Errorf("artifact %s size = %d, want %d", artifact.RelPath, artifact.Size, staged.Size())
		}
	}

	// Blob closure: one reference from the primary log, one from a staged
	// artifact, both digest-verified against the blob store.
	if len(snap.UnresolvedBlobRefs) != 0 {
		t.Errorf("UnresolvedBlobRefs = %v, want none", snap.UnresolvedBlobRefs)
	}
	if !snap.ContinuationGrade {
		t.Error("ContinuationGrade = false, want true with a complete closure")
	}
	if len(snap.Blobs) != 2 {
		t.Fatalf("resolved %d blobs, want 2", len(snap.Blobs))
	}
	extensionResolved := false
	for _, blob := range snap.Blobs {
		if !blob.Digest.Valid() {
			t.Errorf("blob digest %q is not canonical", blob.Digest)
		}
		if got := digestOf(t, blob.SourcePath); got != blob.Digest {
			t.Errorf("blob %s does not hash to its recorded digest %s", got, blob.Digest)
		}
		stored, err := os.Stat(blob.SourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if blob.Size != stored.Size() {
			t.Errorf("blob %s size = %d, want %d", blob.Digest, blob.Size, stored.Size())
		}
		if filepath.Ext(blob.SourcePath) == ".webp" {
			// Proves the store's extension-suffixed copy is accepted;
			// this reference is only reachable from a staged artifact.
			extensionResolved = true
		}
	}
	if !extensionResolved {
		t.Error("no blob resolved through an extension-suffixed store entry")
	}

	// Portable metadata the format actually exposes.
	if snap.Meta.Title == nil || *snap.Meta.Title != "Synthetic fixture session one" {
		t.Errorf("Title = %v, want the current padded title record", snap.Meta.Title)
	}
	if snap.Meta.Workspace == nil || *snap.Meta.Workspace != "/synthetic/workspace/one" {
		t.Errorf("Workspace = %v, want the recorded cwd", snap.Meta.Workspace)
	}
	if snap.Meta.CreatedAt == nil || snap.Meta.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z") != "2026-01-02T03:04:05.678Z" {
		t.Errorf("CreatedAt = %v, want the session record timestamp", snap.Meta.CreatedAt)
	}
	if snap.Meta.ModifiedAt == nil || !snap.Meta.ModifiedAt.Equal(info.ModTime().UTC()) {
		t.Errorf("ModifiedAt = %v, want the source log modification time", snap.Meta.ModifiedAt)
	}
	if snap.SnapshotTime.IsZero() || snap.SnapshotTime.Location() != time.UTC {
		t.Errorf("SnapshotTime = %v, want a UTC instant", snap.SnapshotTime)
	}

	// Versioned adapter metadata, canonical and compact.
	if snap.AdapterMetadataSchema != 1 {
		t.Errorf("AdapterMetadataSchema = %d, want 1", snap.AdapterMetadataSchema)
	}
	canonical, err := archive.CanonicalRawMessage(snap.AdapterMetadata)
	if err != nil {
		t.Fatalf("adapter metadata is not canonical JSON: %v", err)
	}
	if string(canonical) != string(snap.AdapterMetadata) {
		t.Errorf("adapter metadata is not already canonical:\n got %s\nwant %s", snap.AdapterMetadata, canonical)
	}
	var meta adapterMetadata
	if err := json.Unmarshal(snap.AdapterMetadata, &meta); err != nil {
		t.Fatalf("decode adapter metadata: %v", err)
	}
	if meta.OMPSessionID != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("omp_session_id = %q", meta.OMPSessionID)
	}
	if meta.SessionRecordVersion != 3 {
		t.Errorf("session_record_version = %d, want 3", meta.SessionRecordVersion)
	}
	if meta.ProjectDir != "-synthetic-project" {
		t.Errorf("project_dir = %q", meta.ProjectDir)
	}
	if meta.PrimaryDigest != sourceDigest {
		t.Errorf("primary_digest = %s, want %s", meta.PrimaryDigest, sourceDigest)
	}
	if meta.ArtifactCount != 2 || meta.BlobRefCount != 2 || meta.ResolvedBlobCount != 2 || meta.UnresolvedBlobCount != 0 {
		t.Errorf("adapter metadata counts = %+v", meta)
	}
	if !meta.BlobStoreFound {
		t.Error("blob_store_found = false, want true for the fixture store")
	}
}

func TestSnapshotReportsUnresolvedBlobReferences(t *testing.T) {
	t.Parallel()
	src := session(t, discover(t, fixtureRoot(t)), "-synthetic-other/")
	snap := snapshot(t, New(), src)

	if len(snap.Blobs) != 1 {
		t.Errorf("resolved %d blobs, want 1", len(snap.Blobs))
	}
	// One reference has no stored object; one has a stored object whose
	// bytes do not hash to the referenced digest. Neither may enter the
	// closure, and both must be listed.
	if len(snap.UnresolvedBlobRefs) != 2 {
		t.Fatalf("UnresolvedBlobRefs = %v, want 2 entries", snap.UnresolvedBlobRefs)
	}
	for _, ref := range snap.UnresolvedBlobRefs {
		if !strings.HasPrefix(ref, "blob:sha256:") {
			t.Errorf("unresolved reference %q is not a blob reference", ref)
		}
		if archive.Digest(strings.TrimPrefix(ref, "blob:")) == snap.Blobs[0].Digest {
			t.Errorf("resolved blob %s also listed as unresolved", snap.Blobs[0].Digest)
		}
	}
	if snap.ContinuationGrade {
		t.Error("ContinuationGrade = true, want false while references are unresolved")
	}
	if snap.UnresolvedBlobRefs[0] >= snap.UnresolvedBlobRefs[1] {
		t.Errorf("UnresolvedBlobRefs not deduplicated and sorted: %v", snap.UnresolvedBlobRefs)
	}

	var meta adapterMetadata
	if err := json.Unmarshal(snap.AdapterMetadata, &meta); err != nil {
		t.Fatalf("decode adapter metadata: %v", err)
	}
	if meta.ParentSessionID != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("parent_session_id = %q, want the recorded parent session", meta.ParentSessionID)
	}
	if meta.UnresolvedBlobCount != 2 || meta.ResolvedBlobCount != 1 || meta.BlobRefCount != 3 {
		t.Errorf("adapter metadata blob counts = %+v", meta)
	}
	if meta.ArtifactCount != 0 {
		t.Errorf("artifact_count = %d, want 0 for a session without an artifact tree", meta.ArtifactCount)
	}
}

func TestSnapshotExplainsAbsentMetadata(t *testing.T) {
	t.Parallel()
	src := session(t, discover(t, fixtureRoot(t)), "-synthetic-sparse/")
	snap := snapshot(t, New(), src)

	if snap.Meta.Title != nil {
		t.Errorf("Title = %q, want nil for a log without a title", *snap.Meta.Title)
	}
	if snap.Meta.Workspace != nil {
		t.Errorf("Workspace = %q, want nil for a session record without cwd", *snap.Meta.Workspace)
	}
	if snap.Meta.Lifecycle != nil {
		t.Errorf("Lifecycle = %q, want nil: omp persists no lifecycle state", *snap.Meta.Lifecycle)
	}
	if snap.Meta.Repo != nil {
		t.Errorf("Repo = %+v, want nil: the adapter never reads the workspace", snap.Meta.Repo)
	}
	if snap.Meta.CreatedAt == nil {
		t.Error("CreatedAt = nil, want the session record timestamp")
	}
	if snap.Meta.ModifiedAt == nil {
		t.Error("ModifiedAt = nil, want the source log modification time")
	}

	reasons := make(map[string]string, len(snap.Meta.Completeness))
	for _, reason := range snap.Meta.Completeness {
		if reason.Reason == "" {
			t.Errorf("completeness entry for %q has no reason", reason.Field)
		}
		reasons[reason.Field] = reason.Reason
	}
	for _, field := range []string{"title", "workspace", "lifecycle", "repo"} {
		if _, ok := reasons[field]; !ok {
			t.Errorf("nil field %q carries no completeness reason (have %v)", field, reasons)
		}
	}
	for _, field := range []string{"created_at", "modified_at"} {
		if _, ok := reasons[field]; ok {
			t.Errorf("present field %q must not carry a completeness reason", field)
		}
	}
	if !snap.ContinuationGrade {
		t.Error("ContinuationGrade = false, want true: a session with no references has a complete closure")
	}
}

func TestSnapshotDetectsPrimaryLogChangedWhileStaging(t *testing.T) {
	t.Parallel()
	src := session(t, discover(t, fixtureRoot(t)), "-synthetic-project/")

	a := New()
	a.afterStage = func() {
		f, err := os.OpenFile(src.PrimaryPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("append to source log: %v", err)
		}
		if _, err := f.WriteString(`{"type":"message","id":"f000000e","message":{"role":"user","content":[{"type":"text","text":"synthetic fixture appended message"}]}}` + "\n"); err != nil {
			t.Fatalf("append to source log: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("append to source log: %v", err)
		}
	}
	_, err := a.Snapshot(context.Background(), src, filepath.Join(t.TempDir(), "staging"))
	if !errors.Is(err, adapter.ErrUnstable) {
		t.Fatalf("Snapshot error = %v, want adapter.ErrUnstable", err)
	}
}

func TestSnapshotDetectsArtifactTreeChangedWhileStaging(t *testing.T) {
	t.Parallel()
	src := session(t, discover(t, fixtureRoot(t)), "-synthetic-project/")

	a := New()
	a.afterStage = func() {
		added := filepath.Join(strings.TrimSuffix(src.PrimaryPath, ".jsonl"), "Late.jsonl")
		if err := os.WriteFile(added, []byte("synthetic fixture late artifact\n"), 0o600); err != nil {
			t.Fatalf("add artifact: %v", err)
		}
	}
	_, err := a.Snapshot(context.Background(), src, filepath.Join(t.TempDir(), "staging"))
	if !errors.Is(err, adapter.ErrUnstable) {
		t.Fatalf("Snapshot error = %v, want adapter.ErrUnstable", err)
	}
}

// TestSnapshotFindsBlobReferenceAcrossReadChunks defends the chunked
// reference scan: a reference straddling the internal chunk boundary of a
// multi-megabyte log must still enter the closure.
func TestSnapshotFindsBlobReferenceAcrossReadChunks(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "agent", "sessions")
	project := filepath.Join(root, "-chunk-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	blobStore := filepath.Join(filepath.Dir(root), "blobs")
	if err := os.MkdirAll(blobStore, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("synthetic fixture chunk-boundary blob\n")
	digest := archive.NewDigest(sha256.Sum256(payload))
	if err := os.WriteFile(filepath.Join(blobStore, digest.Hex()), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	head := `{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000000c","timestamp":"2026-01-06T00:00:00.000Z","cwd":"/synthetic/workspace/chunk","comment":"`
	// Place the reference so it starts a few bytes before the 1 MiB
	// boundary the scanner reads in.
	pad := 1<<20 - len(head) - 4
	log := head + strings.Repeat("x", pad) + `blob:` + string(digest) + `"}` + "\n"
	primary := filepath.Join(project, "2026-01-06T00-00-00-000Z_00000000-0000-4000-8000-00000000000c.jsonl")
	if err := os.WriteFile(primary, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}

	src := session(t, discover(t, root), "-chunk-project/")
	snap := snapshot(t, New(), src)
	if len(snap.Blobs) != 1 || snap.Blobs[0].Digest != digest {
		t.Fatalf("blobs = %+v, want the chunk-boundary reference resolved to %s", snap.Blobs, digest)
	}
	if !snap.ContinuationGrade {
		t.Errorf("ContinuationGrade = false, unresolved = %v", snap.UnresolvedBlobRefs)
	}
}

// TestDiscoverRealRootSmoke checks that the adapter survives the
// operator's real tree. It is opt-in, asserts nothing about contents, and
// reports counts only.
func TestDiscoverRealRootSmoke(t *testing.T) {
	if os.Getenv(realRootsEnv) == "" {
		t.Skipf("set %s to scan the real OMP root", realRootsEnv)
	}
	a := New()
	roots := a.DefaultRoots()
	if len(roots) == 0 {
		t.Skip("no home directory available")
	}
	if _, err := os.Stat(roots[0]); err != nil {
		t.Skip("default OMP root is absent")
	}
	sessions, err := a.Discover(context.Background(), roots)
	if err != nil {
		t.Fatalf("Discover on real root: %v", err)
	}
	invalid := 0
	for _, s := range sessions {
		if !archive.ValidSourceID(s.SourceID) {
			invalid++
		}
	}
	if invalid != 0 {
		t.Errorf("%d of %d discovered source ids are invalid", invalid, len(sessions))
	}
	t.Logf("discovered %d sessions across %d real root(s)", len(sessions), len(roots))
}
