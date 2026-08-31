package omp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/digest"
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

func describe(t *testing.T, src adapter.SourceSession) *adapter.Description {
	t.Helper()
	desc, err := New().Describe(context.Background(), src)
	if err != nil {
		t.Fatalf("Describe(%s): %v", src.SourceID, err)
	}
	return desc
}

func digestOf(t *testing.T, path string) digest.Digest {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return digest.New(sha256.Sum256(data))
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
		if !adapter.ValidSourceID(s.SourceID) {
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
	if !adapter.ValidSourceID(got.SourceID) {
		t.Errorf("sanitized source id %q is invalid", got.SourceID)
	}
}

func TestDescribeReadsPrimaryArtifactsAndBlobs(t *testing.T) {
	t.Parallel()
	root := fixtureRoot(t)
	src := session(t, discover(t, root), "-synthetic-project/")
	desc := describe(t, src)

	// Primary log: described in place, never copied.
	sourceDigest := digestOf(t, src.PrimaryPath)
	info, err := os.Stat(src.PrimaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if desc.PrimarySize != info.Size() {
		t.Errorf("PrimarySize = %d, want %d", desc.PrimarySize, info.Size())
	}
	if desc.Source.PrimaryPath != src.PrimaryPath {
		t.Errorf("Source.PrimaryPath = %q, want the live source path %q", desc.Source.PrimaryPath, src.PrimaryPath)
	}

	// Artifact tree: adapter-root-relative names pointing at live files.
	wantRel := []string{"Helper.jsonl", "nested/7.bash.log"}
	if len(desc.Artifacts) != len(wantRel) {
		t.Fatalf("described %d artifacts, want %d", len(desc.Artifacts), len(wantRel))
	}
	artifactDir := strings.TrimSuffix(src.PrimaryPath, ".jsonl")
	for i, artifact := range desc.Artifacts {
		if artifact.RelPath != wantRel[i] {
			t.Errorf("artifact %d rel path = %q, want %q", i, artifact.RelPath, wantRel[i])
		}
		want := filepath.Join(artifactDir, filepath.FromSlash(wantRel[i]))
		if artifact.SourcePath != want {
			t.Errorf("artifact %s source path = %q, want %q", artifact.RelPath, artifact.SourcePath, want)
		}
		if !filepath.IsAbs(artifact.SourcePath) {
			t.Errorf("artifact %s source path %q is not absolute", artifact.RelPath, artifact.SourcePath)
		}
		live, err := os.Stat(artifact.SourcePath)
		if err != nil {
			t.Fatal(err)
		}
		if artifact.Size != live.Size() {
			t.Errorf("artifact %s size = %d, want %d", artifact.RelPath, artifact.Size, live.Size())
		}
	}

	// Blob closure: one reference from the primary log, one from an
	// artifact, both digest-verified against the blob store.
	if len(desc.UnresolvedBlobRefs) != 0 {
		t.Errorf("UnresolvedBlobRefs = %v, want none", desc.UnresolvedBlobRefs)
	}
	if !desc.ContinuationGrade {
		t.Error("ContinuationGrade = false, want true with a complete closure")
	}
	if len(desc.Blobs) != 2 {
		t.Fatalf("resolved %d blobs, want 2", len(desc.Blobs))
	}
	extensionResolved := false
	for _, blob := range desc.Blobs {
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
			// this reference is only reachable from an artifact file.
			extensionResolved = true
		}
	}
	if !extensionResolved {
		t.Error("no blob resolved through an extension-suffixed store entry")
	}

	// Portable metadata the format actually exposes.
	if desc.Meta.Title == nil || *desc.Meta.Title != "Synthetic fixture session one" {
		t.Errorf("Title = %v, want the current padded title record", desc.Meta.Title)
	}
	if desc.Meta.Workspace == nil || *desc.Meta.Workspace != "/synthetic/workspace/one" {
		t.Errorf("Workspace = %v, want the recorded cwd", desc.Meta.Workspace)
	}
	if desc.Meta.CreatedAt == nil || desc.Meta.CreatedAt.UTC().Format("2006-01-02T15:04:05.000Z") != "2026-01-02T03:04:05.678Z" {
		t.Errorf("CreatedAt = %v, want the session record timestamp", desc.Meta.CreatedAt)
	}
	if desc.Meta.ModifiedAt == nil || !desc.Meta.ModifiedAt.Equal(info.ModTime().UTC()) {
		t.Errorf("ModifiedAt = %v, want the source log modification time", desc.Meta.ModifiedAt)
	}
	if desc.DescribedAt.IsZero() || desc.DescribedAt.Location() != time.UTC {
		t.Errorf("DescribedAt = %v, want a UTC instant", desc.DescribedAt)
	}

	// Versioned adapter metadata, canonical and compact. Schema 2 added the
	// usage document; this fixture's one assistant record carries no usage
	// block, so the field is absent here and TestDescribeExplainsAbsentUsage
	// is what holds that absence to a stated reason.
	if desc.AdapterMetadataSchema != 2 {
		t.Errorf("AdapterMetadataSchema = %d, want 2", desc.AdapterMetadataSchema)
	}
	canonical, err := adapter.CanonicalRawMessage(desc.AdapterMetadata)
	if err != nil {
		t.Fatalf("adapter metadata is not canonical JSON: %v", err)
	}
	if string(canonical) != string(desc.AdapterMetadata) {
		t.Errorf("adapter metadata is not already canonical:\n got %s\nwant %s", desc.AdapterMetadata, canonical)
	}
	var meta adapterMetadata
	if err := json.Unmarshal(desc.AdapterMetadata, &meta); err != nil {
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
	if meta.PrimarySize != info.Size() {
		t.Errorf("primary_size = %d, want %d", meta.PrimarySize, info.Size())
	}
	if meta.ArtifactCount != 2 || meta.BlobRefCount != 2 || meta.ResolvedBlobCount != 2 || meta.UnresolvedBlobCount != 0 {
		t.Errorf("adapter metadata counts = %+v", meta)
	}
	var wantBytes int64
	for _, rel := range wantRel {
		fi, err := os.Stat(filepath.Join(artifactDir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		wantBytes += fi.Size()
	}
	if meta.ArtifactBytes != wantBytes {
		t.Errorf("artifact_bytes = %d, want %d recomputed from the live files", meta.ArtifactBytes, wantBytes)
	}
	if !meta.BlobStoreFound {
		t.Error("blob_store_found = false, want true for the fixture store")
	}
}

func TestDescribeReportsUnresolvedBlobReferences(t *testing.T) {
	t.Parallel()
	src := session(t, discover(t, fixtureRoot(t)), "-synthetic-other/")
	desc := describe(t, src)

	if len(desc.Blobs) != 1 {
		t.Errorf("resolved %d blobs, want 1", len(desc.Blobs))
	}
	// One reference has no stored object; one has a stored object whose
	// bytes do not hash to the referenced digest. Neither may enter the
	// closure, and both must be listed.
	if len(desc.UnresolvedBlobRefs) != 2 {
		t.Fatalf("UnresolvedBlobRefs = %v, want 2 entries", desc.UnresolvedBlobRefs)
	}
	for _, ref := range desc.UnresolvedBlobRefs {
		if !strings.HasPrefix(ref, "blob:sha256:") {
			t.Errorf("unresolved reference %q is not a blob reference", ref)
		}
		if digest.Digest(strings.TrimPrefix(ref, "blob:")) == desc.Blobs[0].Digest {
			t.Errorf("resolved blob %s also listed as unresolved", desc.Blobs[0].Digest)
		}
	}
	if desc.ContinuationGrade {
		t.Error("ContinuationGrade = true, want false while references are unresolved")
	}
	if desc.UnresolvedBlobRefs[0] >= desc.UnresolvedBlobRefs[1] {
		t.Errorf("UnresolvedBlobRefs not deduplicated and sorted: %v", desc.UnresolvedBlobRefs)
	}

	var meta adapterMetadata
	if err := json.Unmarshal(desc.AdapterMetadata, &meta); err != nil {
		t.Fatalf("decode adapter metadata: %v", err)
	}
	if meta.ParentSessionID != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("parent_session_id = %q, want the recorded parent session", meta.ParentSessionID)
	}
	if meta.UnresolvedBlobCount != 2 || meta.ResolvedBlobCount != 1 || meta.BlobRefCount != 3 {
		t.Errorf("adapter metadata blob counts = %+v", meta)
	}
	if meta.ArtifactCount != 0 || meta.ArtifactBytes != 0 {
		t.Errorf("artifact_count/bytes = %d/%d, want 0/0 for a session without an artifact tree", meta.ArtifactCount, meta.ArtifactBytes)
	}
}

// TestDescribeTreatsCorruptBlobAsUnresolved isolates the digest
// verification: a store entry whose name is a valid reference but whose
// bytes hash to something else must never enter the closure, and its
// reference must withhold continuation grade.
func TestDescribeTreatsCorruptBlobAsUnresolved(t *testing.T) {
	t.Parallel()
	root := fixtureRoot(t)
	src := session(t, discover(t, root), "-synthetic-project/")

	// Overwrite one referenced blob with different bytes, leaving its
	// content-addressed name intact.
	before := describe(t, src)
	if len(before.Blobs) != 2 {
		t.Fatalf("fixture no longer resolves 2 blobs: %+v", before.Blobs)
	}
	corrupted := before.Blobs[0]
	if err := os.WriteFile(corrupted.SourcePath, []byte("synthetic fixture corrupted blob bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	desc := describe(t, src)
	if len(desc.Blobs) != 1 {
		t.Errorf("resolved %d blobs, want 1 after corrupting one store entry", len(desc.Blobs))
	}
	want := "blob:" + string(corrupted.Digest)
	if len(desc.UnresolvedBlobRefs) != 1 || desc.UnresolvedBlobRefs[0] != want {
		t.Errorf("UnresolvedBlobRefs = %v, want [%s]", desc.UnresolvedBlobRefs, want)
	}
	if desc.ContinuationGrade {
		t.Error("ContinuationGrade = true, want false: a corrupt blob is unresolved")
	}
}

func TestDescribeExplainsAbsentMetadata(t *testing.T) {
	t.Parallel()
	src := session(t, discover(t, fixtureRoot(t)), "-synthetic-sparse/")
	desc := describe(t, src)

	if desc.Meta.Title != nil {
		t.Errorf("Title = %q, want nil for a log without a title", *desc.Meta.Title)
	}
	if desc.Meta.Workspace != nil {
		t.Errorf("Workspace = %q, want nil for a session record without cwd", *desc.Meta.Workspace)
	}
	if desc.Meta.Lifecycle != nil {
		t.Errorf("Lifecycle = %q, want nil: omp persists no lifecycle state", *desc.Meta.Lifecycle)
	}
	if desc.Meta.Repo != nil {
		t.Errorf("Repo = %+v, want nil: the adapter never reads the workspace", desc.Meta.Repo)
	}
	if desc.Meta.CreatedAt == nil {
		t.Error("CreatedAt = nil, want the session record timestamp")
	}
	if desc.Meta.ModifiedAt == nil {
		t.Error("ModifiedAt = nil, want the source log modification time")
	}

	reasons := make(map[string]string, len(desc.Meta.Completeness))
	for _, reason := range desc.Meta.Completeness {
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
	if !desc.ContinuationGrade {
		t.Error("ContinuationGrade = false, want true: a session with no references has a complete closure")
	}
}

// TestDescribeToleratesTornAndGarbageLines defends the recorded
// consistency boundary: restic snapshots are crash-consistent per file,
// so a log whose head or tail is torn or garbage must still describe
// whatever the readable records expose instead of failing.
func TestDescribeToleratesTornAndGarbageLines(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "agent", "sessions")
	project := filepath.Join(root, "-torn-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	log := strings.Join([]string{
		`{"type":"title","title":"Synthetic fixture torn session"}`,
		`not json at all`,
		`{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000000d",` +
			`"timestamp":"2026-01-07T00:00:00.000Z","cwd":"/synthetic/workspace/torn"}`,
		`{"type":"message","id":"f0000001","message":{"role":"user","content":[{"type":"te`,
	}, "\n")
	primary := filepath.Join(project, "2026-01-07T00-00-00-000Z_00000000-0000-4000-8000-00000000000d.jsonl")
	if err := os.WriteFile(primary, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}

	src := session(t, discover(t, root), "-torn-project/")
	desc := describe(t, src)

	// The title record precedes the garbage line, so it is observed; the
	// session record after it is not reached by the head decoder, which
	// stops at the first undecodable record.
	if desc.Meta.Title == nil || *desc.Meta.Title != "Synthetic fixture torn session" {
		t.Errorf("Title = %v, want the record preceding the garbage line", desc.Meta.Title)
	}
	if desc.Meta.Workspace != nil {
		t.Errorf("Workspace = %v, want nil: the garbage line hides the session record", desc.Meta.Workspace)
	}
	for _, field := range []string{"workspace", "created_at"} {
		found := false
		for _, reason := range desc.Meta.Completeness {
			if reason.Field == field {
				found = true
			}
		}
		if !found {
			t.Errorf("unreadable field %q carries no completeness reason", field)
		}
	}
	if desc.PrimarySize != int64(len(log)) {
		t.Errorf("PrimarySize = %d, want %d: raw bytes are described regardless", desc.PrimarySize, len(log))
	}
	if !desc.ContinuationGrade {
		t.Error("ContinuationGrade = false, want true: the torn log references no blobs")
	}
}

// TestDescribeFindsBlobReferenceAcrossReadChunks defends the chunked
// reference scan: a reference straddling the internal chunk boundary of a
// multi-megabyte log must still enter the closure.
func TestDescribeFindsBlobReferenceAcrossReadChunks(t *testing.T) {
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
	want := digest.Bytes(payload)
	if err := os.WriteFile(filepath.Join(blobStore, want.Hex()), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	head := `{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000000c","timestamp":"2026-01-06T00:00:00.000Z","cwd":"/synthetic/workspace/chunk","comment":"`
	// Place the reference so it starts a few bytes before the 1 MiB
	// boundary the scanner reads in.
	pad := 1<<20 - len(head) - 4
	log := head + strings.Repeat("x", pad) + `blob:` + string(want) + `"}` + "\n"
	primary := filepath.Join(project, "2026-01-06T00-00-00-000Z_00000000-0000-4000-8000-00000000000c.jsonl")
	if err := os.WriteFile(primary, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}

	src := session(t, discover(t, root), "-chunk-project/")
	desc := describe(t, src)
	if len(desc.Blobs) != 1 || desc.Blobs[0].Digest != want {
		t.Fatalf("blobs = %+v, want the chunk-boundary reference resolved to %s", desc.Blobs, want)
	}
	if !desc.ContinuationGrade {
		t.Errorf("ContinuationGrade = false, unresolved = %v", desc.UnresolvedBlobRefs)
	}
}

func TestDescribeRejectsForeignSource(t *testing.T) {
	t.Parallel()
	src := session(t, discover(t, fixtureRoot(t)), "-synthetic-project/")

	foreign := src
	foreign.Harness = "codex"
	if _, err := New().Describe(context.Background(), foreign); err == nil {
		t.Error("Describe accepted a session from another harness")
	}
	pathless := src
	pathless.PrimaryPath = ""
	if _, err := New().Describe(context.Background(), pathless); err == nil {
		t.Error("Describe accepted a session with no primary path")
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
		if !adapter.ValidSourceID(s.SourceID) {
			invalid++
		}
	}
	if invalid != 0 {
		t.Errorf("%d of %d discovered source ids are invalid", invalid, len(sessions))
	}
	t.Logf("discovered %d sessions across %d real root(s)", len(sessions), len(roots))
}
