package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/archive"
)

// The adapter must satisfy the frozen port.
var _ adapter.Adapter = New()

// Synthetic project directory names and session UUIDs. They imitate the
// real on-disk shape without naming any real workspace or session.
const (
	projectAlpha = "-synthetic-workspace-alpha"
	projectBeta  = "-synthetic-workspace-beta"

	sessionAlpha     = "aaaaaaaa-0000-4000-8000-000000000001"
	sessionMalformed = "bbbbbbbb-0000-4000-8000-000000000002"
	sessionBare      = "cccccccc-0000-4000-8000-000000000003"

	subagentName = "agent-asynthetic0001.jsonl"
)

// bareMTime is the modification time forced on the bare transcript so the
// filesystem fallback for ModifiedAt is deterministic.
var bareMTime = time.Date(2026, 6, 15, 11, 22, 33, 0, time.UTC)

// newSyntheticRoot materializes a synthetic Claude Code root in a
// temporary directory: two projects, three sessions, one sibling subagent
// tree, and root-relative task/session-env trees for the alpha session
// plus decoys that discovery and staging must ignore.
func newSyntheticRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	alphaDir := filepath.Join(root, "projects", projectAlpha)
	betaDir := filepath.Join(root, "projects", projectBeta)

	writeFixture(t, "session-rich.jsonl", filepath.Join(alphaDir, sessionAlpha+".jsonl"))
	writeFixture(t, "artifact-subagent.jsonl", filepath.Join(alphaDir, sessionAlpha, "subagents", subagentName))
	writeFixture(t, "session-malformed.jsonl", filepath.Join(betaDir, sessionMalformed+".jsonl"))
	writeFixture(t, "session-bare.jsonl", filepath.Join(betaDir, sessionBare+".jsonl"))
	writeFixture(t, "artifact-task.json", filepath.Join(root, "tasks", sessionAlpha, "1.json"))
	writeFixture(t, "artifact-session-env.json", filepath.Join(root, "session-env", sessionAlpha, "env.json"))

	// Decoys: a non-transcript file and a transient lock file.
	writeBytes(t, filepath.Join(alphaDir, "notes.txt"), []byte("synthetic fixture note\n"))
	writeBytes(t, filepath.Join(root, "tasks", sessionAlpha, ".lock"), nil)

	barePath := filepath.Join(betaDir, sessionBare+".jsonl")
	if err := os.Chtimes(barePath, bareMTime, bareMTime); err != nil {
		t.Fatalf("chtimes bare transcript: %v", err)
	}
	return root
}

func writeFixture(t *testing.T, fixture, dst string) {
	t.Helper()
	writeBytes(t, dst, fixtureBytes(t, fixture))
}

func writeBytes(t *testing.T, dst string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func fixtureBytes(t *testing.T, fixture string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	return data
}

// discover runs discovery over one synthetic root and indexes the result by
// SourceID.
func discover(t *testing.T, root string) ([]adapter.SourceSession, map[string]adapter.SourceSession) {
	t.Helper()
	sessions, err := New().Discover(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	byID := make(map[string]adapter.SourceSession, len(sessions))
	for _, s := range sessions {
		byID[s.SourceID] = s
	}
	return sessions, byID
}

func snapshot(t *testing.T, src adapter.SourceSession) *adapter.Snapshot {
	t.Helper()
	snap, err := New().Snapshot(context.Background(), src, t.TempDir())
	if err != nil {
		t.Fatalf("Snapshot(%s): %v", src.SourceID, err)
	}
	return snap
}

// reasonFields lists the completeness fields recorded on a snapshot.
func reasonFields(meta adapter.CommonMeta) []string {
	fields := make([]string, 0, len(meta.Completeness))
	for _, r := range meta.Completeness {
		fields = append(fields, r.Field)
	}
	return fields
}

func hasReason(meta adapter.CommonMeta, field string) bool {
	for _, r := range meta.Completeness {
		if r.Field == field {
			return true
		}
	}
	return false
}

func sourceIDs(sessions []adapter.SourceSession) []string {
	ids := make([]string, 0, len(sessions))
	for _, s := range sessions {
		ids = append(ids, s.SourceID)
	}
	return ids
}

func metadataMap(t *testing.T, snap *adapter.Snapshot) map[string]any {
	t.Helper()
	if snap.AdapterMetadataSchema != metadataSchema {
		t.Fatalf("AdapterMetadataSchema = %d, want %d", snap.AdapterMetadataSchema, metadataSchema)
	}
	var out map[string]any
	if err := json.Unmarshal(snap.AdapterMetadata, &out); err != nil {
		t.Fatalf("unmarshal adapter metadata: %v", err)
	}
	return out
}

func wantNum(t *testing.T, md map[string]any, key string, want float64) {
	t.Helper()
	got, ok := md[key].(float64)
	if !ok {
		t.Fatalf("adapter metadata %q = %#v, want a number", key, md[key])
	}
	if got != want {
		t.Errorf("adapter metadata %q = %v, want %v", key, got, want)
	}
}

func wantStr(t *testing.T, md map[string]any, key, want string) {
	t.Helper()
	if got, _ := md[key].(string); got != want {
		t.Errorf("adapter metadata %q = %q, want %q", key, md[key], want)
	}
}

func TestIdentity(t *testing.T) {
	a := New()
	if a.Harness() != "claude" {
		t.Errorf("Harness() = %q, want %q", a.Harness(), "claude")
	}
	if a.Schema() != 1 {
		t.Errorf("Schema() = %d, want 1", a.Schema())
	}
	roots := a.DefaultRoots()
	if len(roots) != 1 || filepath.Base(roots[0]) != ".claude" {
		t.Errorf("DefaultRoots() = %v, want one path ending in .claude", roots)
	}
}

func TestDiscoverSyntheticRoot(t *testing.T) {
	root := newSyntheticRoot(t)
	sessions, byID := discover(t, root)

	want := []string{
		projectAlpha + "/" + sessionAlpha,
		projectBeta + "/" + sessionMalformed,
		projectBeta + "/" + sessionBare,
	}
	if len(sessions) != len(want) {
		t.Fatalf("Discover found %d sessions, want %d: %v", len(sessions), len(want), sourceIDs(sessions))
	}
	for i := 1; i < len(sessions); i++ {
		if sessions[i-1].SourceID >= sessions[i].SourceID {
			t.Fatalf("Discover results are not sorted by SourceID: %v", sourceIDs(sessions))
		}
	}
	for _, id := range want {
		src, ok := byID[id]
		if !ok {
			t.Fatalf("Discover missing session %q, got %v", id, sourceIDs(sessions))
		}
		if !archive.ValidSourceID(src.SourceID) {
			t.Errorf("SourceID %q is not a valid archive source id", src.SourceID)
		}
		if src.Harness != "claude" {
			t.Errorf("session %q harness = %q, want claude", id, src.Harness)
		}
		if !strings.HasSuffix(src.PrimaryPath, ".jsonl") {
			t.Errorf("session %q primary path %q is not a transcript", id, src.PrimaryPath)
		}
	}
	if hint := byID[projectAlpha+"/"+sessionAlpha].Hint; hint != projectAlpha {
		t.Errorf("Hint = %q, want %q", hint, projectAlpha)
	}
}

func TestDiscoverIsStableAcrossRuns(t *testing.T) {
	root := newSyntheticRoot(t)
	first, _ := discover(t, root)
	second, _ := discover(t, root)
	if len(first) != len(second) {
		t.Fatalf("run counts differ: %d then %d", len(first), len(second))
	}
	for i := range first {
		if first[i].SourceID != second[i].SourceID {
			t.Errorf("SourceID %d changed between runs: %q then %q", i, first[i].SourceID, second[i].SourceID)
		}
	}
}

func TestDiscoverSkipsMissingAndEmptyRoots(t *testing.T) {
	root := newSyntheticRoot(t)
	empty := t.TempDir() // exists but has no projects/ directory
	sessions, err := New().Discover(context.Background(), []string{
		filepath.Join(root, "does-not-exist"),
		empty,
		root,
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("Discover found %d sessions, want 3", len(sessions))
	}
}

func TestDiscoverDeduplicatesRepeatedRoots(t *testing.T) {
	root := newSyntheticRoot(t)
	sessions, err := New().Discover(context.Background(), []string{root, root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("Discover found %d sessions, want 3", len(sessions))
	}
}

func TestDiscoverHonorsContextCancellation(t *testing.T) {
	root := newSyntheticRoot(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Discover(ctx, []string{root}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Discover error = %v, want context.Canceled", err)
	}
}

func TestSnapshotExtractsInFileMetadata(t *testing.T) {
	root := newSyntheticRoot(t)
	_, byID := discover(t, root)
	snap := snapshot(t, byID[projectAlpha+"/"+sessionAlpha])

	raw := fixtureBytes(t, "session-rich.jsonl")
	wantRel := filepath.Join("projects", projectAlpha, sessionAlpha+".jsonl")
	if got := snap.StagedPrimary; !strings.HasSuffix(got, wantRel) {
		t.Errorf("StagedPrimary = %q, want a path ending in %q", got, wantRel)
	}
	staged, err := os.ReadFile(snap.StagedPrimary)
	if err != nil {
		t.Fatalf("read staged primary: %v", err)
	}
	if string(staged) != string(raw) {
		t.Error("staged primary bytes differ from the source transcript")
	}
	if snap.PrimarySize != int64(len(raw)) {
		t.Errorf("PrimarySize = %d, want %d", snap.PrimarySize, len(raw))
	}
	if snap.SnapshotTime.IsZero() || snap.SnapshotTime.Location() != time.UTC {
		t.Errorf("SnapshotTime = %v, want a non-zero UTC time", snap.SnapshotTime)
	}
	if snap.ContinuationGrade {
		t.Error("ContinuationGrade = true, want false for Claude Code snapshots")
	}
	if len(snap.Blobs) != 0 || len(snap.UnresolvedBlobRefs) != 0 {
		t.Errorf("blob closure = %v/%v, want empty", snap.Blobs, snap.UnresolvedBlobRefs)
	}

	if snap.Meta.Title == nil || *snap.Meta.Title != "Synthetic fixture session alpha" {
		t.Errorf("Title = %v, want the latest ai-title record", snap.Meta.Title)
	}
	if snap.Meta.Workspace == nil || *snap.Meta.Workspace != "/synthetic/workspace/alpha" {
		t.Errorf("Workspace = %v, want the in-file cwd", snap.Meta.Workspace)
	}
	wantCreated := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	wantModified := time.Date(2026, 3, 3, 10, 0, 0, 0, time.UTC)
	if snap.Meta.CreatedAt == nil || !snap.Meta.CreatedAt.Equal(wantCreated) {
		t.Errorf("CreatedAt = %v, want %v", snap.Meta.CreatedAt, wantCreated)
	}
	if snap.Meta.ModifiedAt == nil || !snap.Meta.ModifiedAt.Equal(wantModified) {
		t.Errorf("ModifiedAt = %v, want %v", snap.Meta.ModifiedAt, wantModified)
	}
	if snap.Meta.Repo == nil || snap.Meta.Repo.Branch != "feature/synthetic-branch" {
		t.Errorf("Repo = %v, want the latest in-file gitBranch", snap.Meta.Repo)
	}
	if snap.Meta.Lifecycle != nil {
		t.Errorf("Lifecycle = %v, want nil", snap.Meta.Lifecycle)
	}
	for _, field := range []string{"title", "workspace", "created_at"} {
		if hasReason(snap.Meta, field) {
			t.Errorf("unexpected completeness reason for %q: %v", field, reasonFields(snap.Meta))
		}
	}
	for _, field := range []string{"lifecycle", "repo", "artifacts"} {
		if !hasReason(snap.Meta, field) {
			t.Errorf("missing completeness reason for %q: %v", field, reasonFields(snap.Meta))
		}
	}

	md := metadataMap(t, snap)
	wantStr(t, md, "project_dir", projectAlpha)
	wantStr(t, md, "session_uuid", sessionAlpha)
	wantStr(t, md, "primary_rel_path", "projects/"+projectAlpha+"/"+sessionAlpha+".jsonl")
	wantStr(t, md, "primary_digest", string(archive.DigestBytes(raw)))
	wantStr(t, md, "in_file_session_id", sessionAlpha)
	wantStr(t, md, "claude_version", "9.9.999")
	wantStr(t, md, "workspace_source", workspaceFromTranscript)
	wantNum(t, md, "primary_size", float64(len(raw)))
	wantNum(t, md, "record_count", 6)
	wantNum(t, md, "malformed_records", 0)
	wantNum(t, md, "oversized_records", 0)
	wantNum(t, md, "artifact_count", 3)
	if _, ok := md["artifact_failures"]; ok {
		t.Errorf("artifact_failures present with no failures: %#v", md["artifact_failures"])
	}
	types, ok := md["record_type_counts"].(map[string]any)
	if !ok {
		t.Fatalf("record_type_counts = %#v, want an object", md["record_type_counts"])
	}
	if got, _ := types["ai-title"].(float64); got != 2 {
		t.Errorf("record_type_counts[ai-title] = %v, want 2", types["ai-title"])
	}
}

func TestSnapshotStagesSessionLinkedArtifacts(t *testing.T) {
	root := newSyntheticRoot(t)
	_, byID := discover(t, root)
	snap := snapshot(t, byID[projectAlpha+"/"+sessionAlpha])

	want := []string{
		"projects/" + projectAlpha + "/" + sessionAlpha + "/subagents/" + subagentName,
		"tasks/" + sessionAlpha + "/1.json",
		"session-env/" + sessionAlpha + "/env.json",
	}
	got := make([]string, 0, len(snap.Artifacts))
	for _, a := range snap.Artifacts {
		got = append(got, a.RelPath)
	}
	if len(got) != len(want) {
		t.Fatalf("staged artifacts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("artifact %d rel path = %q, want %q", i, got[i], want[i])
		}
		info, err := os.Stat(snap.Artifacts[i].StagedPath)
		if err != nil {
			t.Fatalf("stat staged artifact %q: %v", want[i], err)
		}
		if info.Size() != snap.Artifacts[i].Size {
			t.Errorf("artifact %q staged size = %d, recorded %d", want[i], info.Size(), snap.Artifacts[i].Size)
		}
	}
	subagent, err := os.ReadFile(snap.Artifacts[0].StagedPath)
	if err != nil {
		t.Fatalf("read staged subagent transcript: %v", err)
	}
	if string(subagent) != string(fixtureBytes(t, "artifact-subagent.jsonl")) {
		t.Error("staged subagent transcript bytes differ from the source")
	}
}

func TestSnapshotRecordsReasonsWhenMetadataIsAbsent(t *testing.T) {
	root := newSyntheticRoot(t)
	_, byID := discover(t, root)
	snap := snapshot(t, byID[projectBeta+"/"+sessionBare])

	if snap.Meta.Title != nil {
		t.Errorf("Title = %v, want nil", snap.Meta.Title)
	}
	if snap.Meta.Workspace == nil || *snap.Meta.Workspace != projectBeta {
		t.Errorf("Workspace = %v, want the encoded project directory name %q", snap.Meta.Workspace, projectBeta)
	}
	if snap.Meta.CreatedAt != nil {
		t.Errorf("CreatedAt = %v, want nil", snap.Meta.CreatedAt)
	}
	if snap.Meta.ModifiedAt == nil || !snap.Meta.ModifiedAt.Equal(bareMTime) {
		t.Errorf("ModifiedAt = %v, want the file mtime %v", snap.Meta.ModifiedAt, bareMTime)
	}
	if snap.Meta.Repo != nil {
		t.Errorf("Repo = %v, want nil", snap.Meta.Repo)
	}
	for _, field := range []string{"title", "workspace", "created_at", "lifecycle", "repo", "artifacts"} {
		if !hasReason(snap.Meta, field) {
			t.Errorf("missing completeness reason for %q: %v", field, reasonFields(snap.Meta))
		}
	}
	if len(snap.Artifacts) != 0 {
		t.Errorf("staged artifacts = %v, want none", snap.Artifacts)
	}

	md := metadataMap(t, snap)
	wantStr(t, md, "workspace_source", workspaceFromProjectDir)
	wantNum(t, md, "record_count", 3)
	wantNum(t, md, "artifact_count", 0)
	if _, ok := md["claude_version"]; ok {
		t.Errorf("claude_version present for a transcript without a version: %#v", md["claude_version"])
	}
}

func TestSnapshotPreservesMalformedTranscript(t *testing.T) {
	root := newSyntheticRoot(t)
	_, byID := discover(t, root)
	snap := snapshot(t, byID[projectBeta+"/"+sessionMalformed])

	raw := fixtureBytes(t, "session-malformed.jsonl")
	staged, err := os.ReadFile(snap.StagedPrimary)
	if err != nil {
		t.Fatalf("read staged primary: %v", err)
	}
	if string(staged) != string(raw) {
		t.Error("staged primary bytes differ from the malformed source transcript")
	}
	if snap.Meta.Workspace == nil || *snap.Meta.Workspace != "/synthetic/workspace/beta" {
		t.Errorf("Workspace = %v, want the cwd from the parseable records", snap.Meta.Workspace)
	}
	if snap.Meta.Title != nil {
		t.Errorf("Title = %v, want nil", snap.Meta.Title)
	}
	md := metadataMap(t, snap)
	wantNum(t, md, "record_count", 5)
	wantNum(t, md, "malformed_records", 3)
}

func TestSnapshotSkipsOversizedRecordsForMetadataOnly(t *testing.T) {
	root := t.TempDir()
	sessionID := "eeeeeeee-0000-4000-8000-000000000005"
	huge := strings.Repeat("x", maxRecordBytes+16)
	raw := []byte(`{"type":"user","cwd":"/synthetic/workspace/huge","timestamp":"2026-07-01T00:00:00.000Z","message":{"role":"user","content":"` +
		huge + "\"}}\n" + `{"type":"ai-title","aiTitle":"Synthetic fixture oversized session"}` + "\n")
	writeBytes(t, filepath.Join(root, "projects", projectAlpha, sessionID+".jsonl"), raw)

	_, byID := discover(t, root)
	snap := snapshot(t, byID[projectAlpha+"/"+sessionID])

	staged, err := os.ReadFile(snap.StagedPrimary)
	if err != nil {
		t.Fatalf("read staged primary: %v", err)
	}
	if string(staged) != string(raw) {
		t.Error("staged primary bytes differ from the source transcript")
	}
	if snap.Meta.Title == nil || *snap.Meta.Title != "Synthetic fixture oversized session" {
		t.Errorf("Title = %v, want the title from the record after the oversized line", snap.Meta.Title)
	}
	md := metadataMap(t, snap)
	wantNum(t, md, "record_count", 2)
	wantNum(t, md, "oversized_records", 1)
	wantNum(t, md, "malformed_records", 0)
	// The oversized record carried the only cwd, so the workspace falls
	// back to the encoded project directory name.
	wantStr(t, md, "workspace_source", workspaceFromProjectDir)
}

func TestSnapshotRecordsConflictingCwd(t *testing.T) {
	root := t.TempDir()
	sessionID := "dddddddd-0000-4000-8000-000000000004"
	writeFixture(t, "session-cwd-conflict.jsonl",
		filepath.Join(root, "projects", projectAlpha, sessionID+".jsonl"))

	_, byID := discover(t, root)
	snap := snapshot(t, byID[projectAlpha+"/"+sessionID])

	if snap.Meta.Workspace == nil || *snap.Meta.Workspace != "/synthetic/workspace/gamma" {
		t.Errorf("Workspace = %v, want the first observed cwd", snap.Meta.Workspace)
	}
	if !hasReason(snap.Meta, "workspace") {
		t.Errorf("missing completeness reason for the conflicting cwd: %v", reasonFields(snap.Meta))
	}
}

func TestSnapshotUnstableSource(t *testing.T) {
	root := newSyntheticRoot(t)
	_, byID := discover(t, root)
	src := byID[projectAlpha+"/"+sessionAlpha]

	stagedPrimaryHook = func() {
		f, err := os.OpenFile(src.PrimaryPath, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("open transcript for mutation: %v", err)
		}
		defer f.Close()
		if _, err := f.WriteString(`{"type":"user","timestamp":"2026-03-04T10:00:00.000Z"}` + "\n"); err != nil {
			t.Fatalf("mutate transcript: %v", err)
		}
	}
	t.Cleanup(func() { stagedPrimaryHook = nil })

	if _, err := New().Snapshot(context.Background(), src, t.TempDir()); !errors.Is(err, adapter.ErrUnstable) {
		t.Fatalf("Snapshot error = %v, want adapter.ErrUnstable", err)
	}
}

func TestSnapshotAcceptsTouchedButUnchangedSource(t *testing.T) {
	root := newSyntheticRoot(t)
	_, byID := discover(t, root)
	src := byID[projectAlpha+"/"+sessionAlpha]

	stagedPrimaryHook = func() {
		later := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(src.PrimaryPath, later, later); err != nil {
			t.Fatalf("touch transcript: %v", err)
		}
	}
	t.Cleanup(func() { stagedPrimaryHook = nil })

	if _, err := New().Snapshot(context.Background(), src, t.TempDir()); err != nil {
		t.Fatalf("Snapshot of a touched but unchanged transcript: %v", err)
	}
}

func TestSnapshotRejectsInvalidInput(t *testing.T) {
	root := newSyntheticRoot(t)
	_, byID := discover(t, root)
	valid := byID[projectAlpha+"/"+sessionAlpha]

	cases := map[string]adapter.SourceSession{
		"foreign harness":    {Harness: "codex", SourceID: valid.SourceID, PrimaryPath: valid.PrimaryPath},
		"invalid source id":  {Harness: "claude", SourceID: "../escape", PrimaryPath: valid.PrimaryPath},
		"empty primary path": {Harness: "claude", SourceID: valid.SourceID},
		"foreign layout": {
			Harness:     "claude",
			SourceID:    valid.SourceID,
			PrimaryPath: filepath.Join(root, "elsewhere", sessionAlpha+".jsonl"),
		},
	}
	for name, src := range cases {
		if _, err := New().Snapshot(context.Background(), src, t.TempDir()); err == nil {
			t.Errorf("Snapshot(%s) succeeded, want an error", name)
		}
	}
}

func TestSourceIDSanitization(t *testing.T) {
	cases := []struct {
		project string
		session string
		want    string
	}{
		{projectAlpha, sessionAlpha, projectAlpha + "/" + sessionAlpha},
		{"synthetic project name", "session 01", "synthetic-project-name/session-01"},
		{"synthetic/nested", "sess", "synthetic-nested/sess"},
		{"..", "sess", "x-" + nameDigest("..") + "/sess"},
	}
	for _, c := range cases {
		got := sourceID(c.project, c.session)
		if got != c.want {
			t.Errorf("sourceID(%q, %q) = %q, want %q", c.project, c.session, got, c.want)
		}
		if !archive.ValidSourceID(got) {
			t.Errorf("sourceID(%q, %q) = %q, which is not a valid archive source id", c.project, c.session, got)
		}
	}

	long := strings.Repeat("a", 4*maxSegmentLen)
	id := sourceID(long, long)
	if !archive.ValidSourceID(id) {
		t.Errorf("over-long names produced an invalid source id of length %d", len(id))
	}
	if id != sourceID(long, long) {
		t.Error("sourceID is not deterministic for over-long names")
	}
	if id == sourceID(long+"b", long) {
		t.Error("distinct over-long project names collapsed to the same source id")
	}
}

// TestDiscoverRealRootSmoke scans the operator's real Claude Code root when
// explicitly enabled. It reports counts only: no discovered path, identity,
// or transcript content is logged.
func TestDiscoverRealRootSmoke(t *testing.T) {
	if os.Getenv("BABEL_SMOKE_REAL_ROOTS") == "" {
		t.Skip("set BABEL_SMOKE_REAL_ROOTS=1 to scan the real Claude Code root")
	}
	a := New()
	roots := a.DefaultRoots()
	if len(roots) == 0 {
		t.Skip("no resolvable home directory")
	}
	if _, err := os.Stat(filepath.Join(roots[0], projectsDirName)); err != nil {
		t.Skip("no local Claude Code projects tree")
	}
	sessions, err := a.Discover(context.Background(), roots)
	if err != nil {
		t.Fatalf("Discover on the real root: %v", err)
	}
	for i, s := range sessions {
		if !archive.ValidSourceID(s.SourceID) {
			t.Fatalf("discovered session %d has an invalid source id", i)
		}
	}
	t.Logf("discovered %d session(s) under %d real root(s)", len(sessions), len(roots))
}
