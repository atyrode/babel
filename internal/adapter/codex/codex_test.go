package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/archive"
)

// Fixture identities. The trees under testdata are synthetic: no real
// transcript content, title, path, or identifier appears in them.
const (
	fullID      = "sessions/2026/01/02/rollout-2026-01-02T03-04-05-aaaaaaaa-0000-4000-8000-000000000001.jsonl"
	sparseID    = "sessions/2026/01/02/rollout-2026-01-02T06-07-08-aaaaaaaa-0000-4000-8000-000000000002.jsonl"
	malformedID = "sessions/2026/01/03/rollout-2026-01-03T09-10-11-aaaaaaaa-0000-4000-8000-000000000003.jsonl"

	presentAttachment = "attachments/aaaaaaaa-0000-4000-8000-00000000a001"
	missingAttachment = "attachments/bbbbbbbb-0000-4000-8000-00000000b002"
)

// fixtureRoot copies the synthetic Codex tree into a temporary directory so
// stability tests may mutate it without touching the repository.
func fixtureRoot(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "codex")
	src := filepath.Join("testdata", "root")
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatalf("copy fixture tree: %v", err)
	}
	return dst
}

func discover(t *testing.T, roots ...string) []adapter.SourceSession {
	t.Helper()
	found, err := New().Discover(context.Background(), roots)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	return found
}

func sourceIDs(sessions []adapter.SourceSession) []string {
	out := make([]string, len(sessions))
	for i, s := range sessions {
		out[i] = s.SourceID
	}
	return out
}

func snapshotOf(t *testing.T, root, sourceID string) *adapter.Snapshot {
	t.Helper()
	for _, s := range discover(t, root) {
		if s.SourceID != sourceID {
			continue
		}
		snap, err := New().Snapshot(context.Background(), s, t.TempDir())
		if err != nil {
			t.Fatalf("snapshot %s: %v", sourceID, err)
		}
		return snap
	}
	t.Fatalf("source %s not discovered", sourceID)
	return nil
}

func metadataOf(t *testing.T, snap *adapter.Snapshot) Metadata {
	t.Helper()
	if snap.AdapterMetadataSchema != MetadataSchema {
		t.Errorf("adapter metadata schema = %d, want %d", snap.AdapterMetadataSchema, MetadataSchema)
	}
	var md Metadata
	if err := json.Unmarshal(snap.AdapterMetadata, &md); err != nil {
		t.Fatalf("decode adapter metadata: %v", err)
	}
	return md
}

// requireCompleteness asserts the contract that binds nullable catalog
// fields to their explanations: every nil field carries exactly one
// reason, and no populated field claims one.
func requireCompleteness(t *testing.T, m adapter.CommonMeta) {
	t.Helper()
	reasons := make(map[string]string, len(m.Completeness))
	for _, r := range m.Completeness {
		if r.Reason == "" {
			t.Errorf("completeness entry for %q has no reason", r.Field)
		}
		if _, dup := reasons[r.Field]; dup {
			t.Errorf("duplicate completeness entry for %q", r.Field)
		}
		reasons[r.Field] = r.Reason
	}
	absent := map[string]bool{
		"title":       m.Title == nil,
		"workspace":   m.Workspace == nil,
		"created_at":  m.CreatedAt == nil,
		"modified_at": m.ModifiedAt == nil,
		"lifecycle":   m.Lifecycle == nil,
		"repo":        m.Repo == nil,
	}
	for field, isNil := range absent {
		_, explained := reasons[field]
		if isNil != explained {
			t.Errorf("field %q: nil=%v explained=%v", field, isNil, explained)
		}
	}
}

func TestAdapterIdentity(t *testing.T) {
	a := New()
	if got := a.Harness(); got != "codex" {
		t.Errorf("Harness() = %q, want codex", got)
	}
	if got := a.Schema(); got != 1 {
		t.Errorf("Schema() = %d, want 1", got)
	}
}

func TestDefaultRootsPrefersCodexHome(t *testing.T) {
	relocated := t.TempDir()
	t.Setenv("CODEX_HOME", relocated)
	if got := New().DefaultRoots(); !reflect.DeepEqual(got, []string{relocated}) {
		t.Errorf("DefaultRoots() = %v, want [%s]", got, relocated)
	}

	t.Setenv("CODEX_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	want := []string{filepath.Join(home, ".codex")}
	if got := New().DefaultRoots(); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultRoots() = %v, want %v", got, want)
	}
}

func TestDiscoverFindsRolloutsAndHostState(t *testing.T) {
	root := fixtureRoot(t)
	found := discover(t, root)

	want := []string{fullID, sparseID, malformedID, StateSourceID}
	if got := sourceIDs(found); !reflect.DeepEqual(got, want) {
		t.Fatalf("discovered %v, want %v", got, want)
	}
	for _, s := range found {
		if s.Harness != HarnessName {
			t.Errorf("%s: harness = %q", s.SourceID, s.Harness)
		}
		if !archive.ValidSourceID(s.SourceID) {
			t.Errorf("%s is not a valid source id", s.SourceID)
		}
		if fi, err := os.Stat(s.PrimaryPath); err != nil || !fi.Mode().IsRegular() {
			t.Errorf("%s: primary path %s is not a regular file", s.SourceID, s.PrimaryPath)
		}
	}
	state := found[len(found)-1]
	if want := filepath.Join(root, "history.jsonl"); state.PrimaryPath != want {
		t.Errorf("state primary = %s, want %s", state.PrimaryPath, want)
	}
}

func TestDiscoverSourceIDsAreStableAcrossRuns(t *testing.T) {
	root := fixtureRoot(t)
	first := discover(t, root)
	second := discover(t, root)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("discovery is not stable:\nfirst  %v\nsecond %v", sourceIDs(first), sourceIDs(second))
	}
}

func TestDiscoverSkipsMissingAndEmptyRoots(t *testing.T) {
	root := fixtureRoot(t)
	missing := filepath.Join(t.TempDir(), "absent")
	empty := t.TempDir()

	found := discover(t, missing, empty, root)
	if got, want := len(found), 4; got != want {
		t.Fatalf("discovered %d sessions, want %d", got, want)
	}
	if got := discover(t, missing, empty); len(got) != 0 {
		t.Errorf("discovered %v from rootless directories", sourceIDs(got))
	}
}

func TestDiscoverDeduplicatesRepeatedRoots(t *testing.T) {
	root := fixtureRoot(t)
	if got, want := len(discover(t, root, root)), 4; got != want {
		t.Errorf("discovered %d sessions from a repeated root, want %d", got, want)
	}
}

func TestSnapshotSessionStagesPrimaryAndAttachments(t *testing.T) {
	root := fixtureRoot(t)
	snap := snapshotOf(t, root, fullID)

	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(fullID)))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	stagedBytes, err := os.ReadFile(snap.StagedPrimary)
	if err != nil {
		t.Fatalf("read staged primary: %v", err)
	}
	if !bytes.Equal(source, stagedBytes) {
		t.Error("staged primary differs from the source log")
	}
	if snap.PrimarySize != int64(len(source)) {
		t.Errorf("PrimarySize = %d, want %d", snap.PrimarySize, len(source))
	}
	if snap.SnapshotTime.IsZero() || snap.SnapshotTime.Location() != time.UTC {
		t.Errorf("SnapshotTime = %v, want a UTC instant", snap.SnapshotTime)
	}

	// Attachment closure is best effort, so a resolvable directory is
	// staged whole (including names the reference pattern cannot match)
	// and a missing one is reported rather than fabricated.
	wantArtifacts := []string{
		presentAttachment + "/synthetic capture 0001.txt",
		presentAttachment + "/synthetic-attachment-0001.png",
	}
	var gotArtifacts []string
	for _, f := range snap.Artifacts {
		gotArtifacts = append(gotArtifacts, f.RelPath)
		if fi, err := os.Stat(f.StagedPath); err != nil || fi.Size() != f.Size {
			t.Errorf("artifact %s not staged with the recorded size", f.RelPath)
		}
	}
	if !reflect.DeepEqual(gotArtifacts, wantArtifacts) {
		t.Errorf("artifacts = %v, want %v", gotArtifacts, wantArtifacts)
	}
	if want := []string{missingAttachment}; !reflect.DeepEqual(snap.UnresolvedBlobRefs, want) {
		t.Errorf("unresolved refs = %v, want %v", snap.UnresolvedBlobRefs, want)
	}
	// Closure is never guaranteed for this harness (SPEC.md §3).
	if snap.ContinuationGrade {
		t.Error("ContinuationGrade must be false for codex")
	}

	requireCompleteness(t, snap.Meta)
	if snap.Meta.Workspace == nil || *snap.Meta.Workspace != "/synthetic/workspace" {
		t.Errorf("workspace = %v, want /synthetic/workspace", snap.Meta.Workspace)
	}
	if snap.Meta.CreatedAt == nil || snap.Meta.CreatedAt.Format("2006-01-02T15:04:05.000Z") != "2026-01-02T03:04:05.100Z" {
		t.Errorf("created_at = %v", snap.Meta.CreatedAt)
	}
	if snap.Meta.ModifiedAt == nil || snap.Meta.ModifiedAt.Format("2006-01-02T15:04:05.000Z") != "2026-01-02T03:04:09.500Z" {
		t.Errorf("modified_at = %v", snap.Meta.ModifiedAt)
	}

	md := metadataOf(t, snap)
	if md.Kind != KindSession || md.PrimaryPath != fullID {
		t.Errorf("metadata kind/path = %q/%q", md.Kind, md.PrimaryPath)
	}
	if md.SessionID != "aaaaaaaa-0000-4000-8000-00000000000a" || md.ThreadID != "aaaaaaaa-0000-4000-8000-000000000001" {
		t.Errorf("metadata identities = %q/%q", md.SessionID, md.ThreadID)
	}
	if md.CLIVersion != "0.0.0-synthetic" || md.ModelProvider != "synthetic-provider" || md.HistoryMode != "legacy" {
		t.Errorf("metadata source facts = %+v", md)
	}
	if want := []string{"synthetic-model-a", "synthetic-model-b"}; !reflect.DeepEqual(md.Models, want) {
		t.Errorf("models = %v, want %v", md.Models, want)
	}
	if want := []string{"/synthetic/other-root", "/synthetic/workspace"}; !reflect.DeepEqual(md.WorkspaceRoots, want) {
		t.Errorf("workspace roots = %v, want %v", md.WorkspaceRoots, want)
	}
	if md.Records != 6 || md.MalformedRecords != 0 {
		t.Errorf("records = %d, malformed = %d, want 6/0", md.Records, md.MalformedRecords)
	}
	if md.RecordTypes["turn_context"] != 2 || md.RecordTypes["response_item"] != 2 {
		t.Errorf("record types = %v", md.RecordTypes)
	}
	if md.AttachmentRefs != 2 || md.AttachmentsStaged != 2 {
		t.Errorf("attachment refs = %d, staged = %d, want 2/2", md.AttachmentRefs, md.AttachmentsStaged)
	}
	if md.SessionIndexStaged != nil {
		t.Error("session_index_staged must be absent for a session snapshot")
	}
}

func TestSnapshotRecordsReasonsForUnavailableFields(t *testing.T) {
	root := fixtureRoot(t)
	snap := snapshotOf(t, root, sparseID)

	requireCompleteness(t, snap.Meta)
	if snap.Meta.Title != nil || snap.Meta.Workspace != nil || snap.Meta.CreatedAt != nil ||
		snap.Meta.ModifiedAt != nil || snap.Meta.Lifecycle != nil || snap.Meta.Repo != nil {
		t.Fatalf("sparse log yielded synthesized catalog fields: %+v", snap.Meta)
	}
	want := map[string]bool{"title": true, "workspace": true, "created_at": true, "modified_at": true, "lifecycle": true, "repo": true}
	for _, r := range snap.Meta.Completeness {
		delete(want, r.Field)
	}
	if len(want) != 0 {
		t.Errorf("missing completeness reasons for %v", want)
	}
	if md := metadataOf(t, snap); md.Records != 2 {
		t.Errorf("records = %d, want 2", md.Records)
	}
}

func TestSnapshotMalformedLogStillPreservesRawBytes(t *testing.T) {
	root := fixtureRoot(t)
	snap := snapshotOf(t, root, malformedID)

	source, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(malformedID)))
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	staged, err := os.ReadFile(snap.StagedPrimary)
	if err != nil {
		t.Fatalf("read staged primary: %v", err)
	}
	if !bytes.Equal(source, staged) {
		t.Error("malformed log was not staged byte for byte")
	}
	requireCompleteness(t, snap.Meta)

	md := metadataOf(t, snap)
	if md.MalformedRecords < 2 {
		t.Errorf("malformed records = %d, want at least 2", md.MalformedRecords)
	}
	// The one well-formed session_meta record still yields its facts.
	if md.SessionID != "aaaaaaaa-0000-4000-8000-00000000000c" {
		t.Errorf("session id = %q, want the parsable record's value", md.SessionID)
	}
	if snap.Meta.Workspace == nil || *snap.Meta.Workspace != "/synthetic/malformed-workspace" {
		t.Errorf("workspace = %v", snap.Meta.Workspace)
	}
}

func TestSnapshotHostStateStagesHistoryAndIndex(t *testing.T) {
	root := fixtureRoot(t)
	snap := snapshotOf(t, root, StateSourceID)

	if want := filepath.Base(snap.StagedPrimary); want != "history.jsonl" {
		t.Errorf("staged primary = %s, want history.jsonl", snap.StagedPrimary)
	}
	if len(snap.Artifacts) != 1 || snap.Artifacts[0].RelPath != "session_index.jsonl" {
		t.Fatalf("artifacts = %+v, want session_index.jsonl", snap.Artifacts)
	}
	index, err := os.ReadFile(snap.Artifacts[0].StagedPath)
	if err != nil {
		t.Fatalf("read staged index: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(root, "session_index.jsonl"))
	if err != nil {
		t.Fatalf("read source index: %v", err)
	}
	if !bytes.Equal(index, source) {
		t.Error("staged session index differs from the source")
	}

	requireCompleteness(t, snap.Meta)
	if snap.Meta.Workspace != nil {
		t.Error("host state must not claim a workspace")
	}
	if snap.Meta.CreatedAt == nil || snap.Meta.CreatedAt.Unix() != 1767322000 {
		t.Errorf("created_at = %v, want the earliest history timestamp", snap.Meta.CreatedAt)
	}
	if snap.Meta.ModifiedAt == nil || snap.Meta.ModifiedAt.Unix() != 1767409000 {
		t.Errorf("modified_at = %v, want the latest history timestamp", snap.Meta.ModifiedAt)
	}

	md := metadataOf(t, snap)
	if md.Kind != KindState || md.PrimaryPath != "history.jsonl" {
		t.Errorf("metadata kind/path = %q/%q", md.Kind, md.PrimaryPath)
	}
	if md.Records != 3 || md.MalformedRecords != 0 {
		t.Errorf("records = %d, malformed = %d, want 3/0", md.Records, md.MalformedRecords)
	}
	if md.SessionIndexStaged == nil || !*md.SessionIndexStaged {
		t.Errorf("session_index_staged = %v, want true", md.SessionIndexStaged)
	}
}

func TestSnapshotHostStateWithoutSessionIndex(t *testing.T) {
	root := fixtureRoot(t)
	if err := os.Remove(filepath.Join(root, "session_index.jsonl")); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	snap := snapshotOf(t, root, StateSourceID)
	if len(snap.Artifacts) != 0 {
		t.Errorf("artifacts = %+v, want none", snap.Artifacts)
	}
	md := metadataOf(t, snap)
	if md.SessionIndexStaged == nil || *md.SessionIndexStaged {
		t.Errorf("session_index_staged = %v, want false", md.SessionIndexStaged)
	}
}

func TestSnapshotSourceChangedDuringStagingIsUnstable(t *testing.T) {
	root := fixtureRoot(t)
	primary := filepath.Join(root, filepath.FromSlash(fullID))

	appended := false
	testHookStaged = func() {
		if appended {
			return
		}
		appended = true
		f, err := os.OpenFile(primary, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatalf("open source for append: %v", err)
		}
		if _, err := f.WriteString(`{"timestamp":"2026-01-02T03:04:10.000Z","type":"event_msg","payload":{"type":"agent_message","message":"synthetic fixture message four"}}` + "\n"); err != nil {
			t.Fatalf("append to source: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("close source: %v", err)
		}
	}
	t.Cleanup(func() { testHookStaged = nil })

	var src adapter.SourceSession
	for _, s := range discover(t, root) {
		if s.SourceID == fullID {
			src = s
		}
	}
	_, err := New().Snapshot(context.Background(), src, t.TempDir())
	if !errors.Is(err, adapter.ErrUnstable) {
		t.Fatalf("snapshot error = %v, want adapter.ErrUnstable", err)
	}
	if !appended {
		t.Fatal("stability window never reached the test hook")
	}
}

func TestSnapshotRejectsForeignSource(t *testing.T) {
	root := fixtureRoot(t)
	src := adapter.SourceSession{Harness: "omp", SourceID: fullID, PrimaryPath: filepath.Join(root, filepath.FromSlash(fullID))}
	if _, err := New().Snapshot(context.Background(), src, t.TempDir()); err == nil {
		t.Fatal("snapshot accepted a source from another harness")
	}
	src = adapter.SourceSession{Harness: HarnessName, SourceID: "with spaces", PrimaryPath: "x"}
	if _, err := New().Snapshot(context.Background(), src, t.TempDir()); err == nil {
		t.Fatal("snapshot accepted an invalid source id")
	}
}

func TestSourceIDForUnusualPaths(t *testing.T) {
	plain := filepath.Join("sessions", "2026", "01", "02", "rollout-a.jsonl")
	if got, want := sourceIDFor(plain), "sessions/2026/01/02/rollout-a.jsonl"; got != want {
		t.Errorf("sourceIDFor(%q) = %q, want %q", plain, got, want)
	}

	odd := filepath.Join("sessions", "2026", "a name with spaces.jsonl")
	got := sourceIDFor(odd)
	if !archive.ValidSourceID(got) {
		t.Errorf("sourceIDFor(%q) = %q, which is not a valid source id", odd, got)
	}
	if got != sourceIDFor(odd) {
		t.Error("sourceIDFor is not stable for an unusual path")
	}
	if other := sourceIDFor(filepath.Join("sessions", "2026", "another name.jsonl")); other == got {
		t.Error("distinct unusual paths collapsed onto one source id")
	}
	if sourceIDFor("state") == StateSourceID {
		t.Error("a rollout path must never take the host-state identity")
	}
}

// TestSnapshotOversizedRecords covers the bounded record reader: a record
// larger than the read buffer must still be parsed whole, and one larger
// than the parser limit must degrade to a flagged truncation while its raw
// bytes are staged intact.
func TestSnapshotOversizedRecords(t *testing.T) {
	root := filepath.Join(t.TempDir(), "codex")
	rel := "sessions/2026/01/04/rollout-2026-01-04T00-00-00-aaaaaaaa-0000-4000-8000-000000000004.jsonl"
	log := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var buf bytes.Buffer
	buf.WriteString(`{"timestamp":"2026-01-04T00:00:00.000Z","type":"session_meta","payload":{"id":"aaaaaaaa-0000-4000-8000-000000000004","cwd":"/synthetic/oversized"}}` + "\n")
	// Multi-chunk but within the parser limit: metadata must survive.
	buf.WriteString(`{"timestamp":"2026-01-04T00:00:01.000Z","type":"turn_context","payload":{"model":"synthetic-model-big","cwd":"/synthetic/oversized","note":"` +
		strings.Repeat("s", 256<<10) + `"}}` + "\n")
	// Beyond the parser limit: truncated for metadata, never for bytes.
	buf.WriteString(`{"timestamp":"2026-01-04T00:00:02.000Z","type":"event_msg","payload":{"type":"agent_message","message":"` +
		strings.Repeat("s", maxRecordBytes+(1<<20)) + `"}}` + "\n")
	if err := os.WriteFile(log, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write oversized log: %v", err)
	}

	snap := snapshotOf(t, root, rel)
	staged, err := os.ReadFile(snap.StagedPrimary)
	if err != nil {
		t.Fatalf("read staged primary: %v", err)
	}
	if !bytes.Equal(staged, buf.Bytes()) {
		t.Error("oversized log was not staged byte for byte")
	}

	md := metadataOf(t, snap)
	if md.Records != 3 {
		t.Errorf("records = %d, want 3", md.Records)
	}
	if md.TruncatedRecords != 1 {
		t.Errorf("truncated records = %d, want 1", md.TruncatedRecords)
	}
	if md.MalformedRecords != 1 {
		t.Errorf("malformed records = %d, want 1 (the truncated record)", md.MalformedRecords)
	}
	if want := []string{"synthetic-model-big"}; !reflect.DeepEqual(md.Models, want) {
		t.Errorf("models = %v, want %v: a multi-chunk record must parse whole", md.Models, want)
	}
	if snap.Meta.Workspace == nil || *snap.Meta.Workspace != "/synthetic/oversized" {
		t.Errorf("workspace = %v", snap.Meta.Workspace)
	}
}

// TestSmokeRealRoots exercises discovery against the operator's real Codex
// root when explicitly enabled. It asserts nothing about content and logs
// counts only.
func TestSmokeRealRoots(t *testing.T) {
	if os.Getenv("BABEL_SMOKE_REAL_ROOTS") == "" {
		t.Skip("set BABEL_SMOKE_REAL_ROOTS to scan the real local codex root")
	}
	a := New()
	roots := a.DefaultRoots()
	if len(roots) == 0 {
		t.Skip("no default codex root resolvable")
	}
	if fi, err := os.Stat(roots[0]); err != nil || !fi.IsDir() {
		t.Skip("default codex root is absent")
	}
	found, err := a.Discover(context.Background(), roots)
	if err != nil {
		t.Fatalf("discover real root: %v", err)
	}
	state := 0
	for _, s := range found {
		if !archive.ValidSourceID(s.SourceID) {
			t.Fatal("real root produced an invalid source id")
		}
		if s.SourceID == StateSourceID {
			state++
		}
	}
	t.Logf("discovered %d codex sessions (%d host-state)", len(found), state)
}
