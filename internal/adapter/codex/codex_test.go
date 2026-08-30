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
// tests may mutate it without touching the repository.
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

func describeOf(t *testing.T, root, sourceID string) *adapter.Description {
	t.Helper()
	for _, s := range discover(t, root) {
		if s.SourceID != sourceID {
			continue
		}
		desc, err := New().Describe(context.Background(), s)
		if err != nil {
			t.Fatalf("describe %s: %v", sourceID, err)
		}
		return desc
	}
	t.Fatalf("source %s not discovered", sourceID)
	return nil
}

func metadataOf(t *testing.T, desc *adapter.Description) Metadata {
	t.Helper()
	if desc.AdapterMetadataSchema != MetadataSchema {
		t.Errorf("adapter metadata schema = %d, want %d", desc.AdapterMetadataSchema, MetadataSchema)
	}
	canonical, err := adapter.CanonicalRawMessage(desc.AdapterMetadata)
	if err != nil {
		t.Fatalf("adapter metadata is not canonical JSON: %v", err)
	}
	if !bytes.Equal(canonical, desc.AdapterMetadata) {
		t.Errorf("adapter metadata is not already canonical:\n got %s\nwant %s", desc.AdapterMetadata, canonical)
	}
	var md Metadata
	if err := json.Unmarshal(desc.AdapterMetadata, &md); err != nil {
		t.Fatalf("decode adapter metadata: %v", err)
	}
	return md
}

// requireDescribesRawFile asserts the invariant that survives the pivot to
// restic: the description names the live file and its full size, so the
// bytes archived are exactly the bytes on disk.
func requireDescribesRawFile(t *testing.T, desc *adapter.Description, path string) {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if desc.Source.PrimaryPath != path {
		t.Errorf("Source.PrimaryPath = %q, want the live source %q", desc.Source.PrimaryPath, path)
	}
	if desc.PrimarySize != int64(len(source)) {
		t.Errorf("PrimarySize = %d, want %d", desc.PrimarySize, len(source))
	}
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
	var a adapter.Adapter = New()
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
		if !adapter.ValidSourceID(s.SourceID) {
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

func TestDescribeSessionResolvesPrimaryAndAttachments(t *testing.T) {
	root := fixtureRoot(t)
	primary := filepath.Join(root, filepath.FromSlash(fullID))
	desc := describeOf(t, root, fullID)

	requireDescribesRawFile(t, desc, primary)
	if desc.DescribedAt.IsZero() || desc.DescribedAt.Location() != time.UTC {
		t.Errorf("DescribedAt = %v, want a UTC instant", desc.DescribedAt)
	}

	// Attachment closure is best effort, so a resolvable directory
	// contributes every file it holds (including names the reference
	// pattern cannot match) and a missing one is reported, not fabricated.
	wantArtifacts := []string{
		presentAttachment + "/synthetic capture 0001.txt",
		presentAttachment + "/synthetic-attachment-0001.png",
	}
	var gotArtifacts []string
	for _, f := range desc.Artifacts {
		gotArtifacts = append(gotArtifacts, f.RelPath)
		if !filepath.IsAbs(f.SourcePath) {
			t.Errorf("artifact %s source path %q is not absolute", f.RelPath, f.SourcePath)
		}
		if want := filepath.Join(root, filepath.FromSlash(f.RelPath)); f.SourcePath != want {
			t.Errorf("artifact %s source path = %q, want %q", f.RelPath, f.SourcePath, want)
		}
		fi, err := os.Stat(f.SourcePath)
		if err != nil || fi.Size() != f.Size {
			t.Errorf("artifact %s does not name a live file of the recorded size", f.RelPath)
		}
	}
	if !reflect.DeepEqual(gotArtifacts, wantArtifacts) {
		t.Errorf("artifacts = %v, want %v", gotArtifacts, wantArtifacts)
	}
	if want := []string{missingAttachment}; !reflect.DeepEqual(desc.UnresolvedBlobRefs, want) {
		t.Errorf("unresolved refs = %v, want %v", desc.UnresolvedBlobRefs, want)
	}
	// Closure is never guaranteed for this harness (SPEC.md §3).
	if desc.ContinuationGrade {
		t.Error("ContinuationGrade must be false for codex")
	}

	requireCompleteness(t, desc.Meta)
	if desc.Meta.Workspace == nil || *desc.Meta.Workspace != "/synthetic/workspace" {
		t.Errorf("workspace = %v, want /synthetic/workspace", desc.Meta.Workspace)
	}
	if desc.Meta.CreatedAt == nil || desc.Meta.CreatedAt.Format("2006-01-02T15:04:05.000Z") != "2026-01-02T03:04:05.100Z" {
		t.Errorf("created_at = %v", desc.Meta.CreatedAt)
	}
	if desc.Meta.ModifiedAt == nil || desc.Meta.ModifiedAt.Format("2006-01-02T15:04:05.000Z") != "2026-01-02T03:04:09.500Z" {
		t.Errorf("modified_at = %v", desc.Meta.ModifiedAt)
	}

	md := metadataOf(t, desc)
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
	if md.AttachmentRefs != 2 || md.AttachmentFiles != 2 {
		t.Errorf("attachment refs = %d, files = %d, want 2/2", md.AttachmentRefs, md.AttachmentFiles)
	}
	if md.SessionIndexFound != nil {
		t.Error("session_index_found must be absent for a session description")
	}
}

// TestDescribeRecordsReasonsForUnavailableFields defends SPEC.md §3's rule
// that an absent field carries a reason rather than a synthesized value.
//
// Title is not in the absent set here, and that is the change rather than an
// exemption: the sparse fixture's one user record is a genuine request, so a
// title is now derivable from it and the adapter reports one — labelled
// derived, never recorded. What must still hold is the pairing: a title
// without a provenance would let Babel's arithmetic pass for Codex's own
// record, which title_test.go asserts in both directions.
func TestDescribeRecordsReasonsForUnavailableFields(t *testing.T) {
	root := fixtureRoot(t)
	desc := describeOf(t, root, sparseID)

	requireCompleteness(t, desc.Meta)
	if desc.Meta.Workspace != nil || desc.Meta.CreatedAt != nil ||
		desc.Meta.ModifiedAt != nil || desc.Meta.Lifecycle != nil || desc.Meta.Repo != nil {
		t.Fatalf("sparse log yielded synthesized catalog fields: %+v", desc.Meta)
	}
	if desc.Meta.Title == nil || desc.Meta.TitleProvenance != adapter.TitleDerived {
		t.Fatalf("Title/TitleProvenance = %v/%q, want a derived title from the sparse log's request",
			desc.Meta.Title, desc.Meta.TitleProvenance)
	}
	want := map[string]bool{"workspace": true, "created_at": true, "modified_at": true, "lifecycle": true, "repo": true}
	for _, r := range desc.Meta.Completeness {
		delete(want, r.Field)
	}
	if len(want) != 0 {
		t.Errorf("missing completeness reasons for %v", want)
	}
	if md := metadataOf(t, desc); md.Records != 2 {
		t.Errorf("records = %d, want 2", md.Records)
	}
}

// TestDescribeToleratesMalformedAndTornRecords defends the recorded
// consistency boundary: restic snapshots are crash-consistent per file, so
// a log containing garbage lines, a wrong-shaped payload, and a torn
// trailing record must count and skip them while still describing the raw
// file and every fact the readable records expose.
func TestDescribeToleratesMalformedAndTornRecords(t *testing.T) {
	root := fixtureRoot(t)
	primary := filepath.Join(root, filepath.FromSlash(malformedID))
	desc := describeOf(t, root, malformedID)

	requireDescribesRawFile(t, desc, primary)
	requireCompleteness(t, desc.Meta)

	md := metadataOf(t, desc)
	// Six records: one good session_meta, one wrong-shaped payload, one
	// garbage line, two parsable records, and one torn trailing record
	// with no newline.
	if md.Records != 6 {
		t.Errorf("records = %d, want 6", md.Records)
	}
	if md.MalformedRecords != 3 {
		t.Errorf("malformed records = %d, want 3 (wrong shape, garbage, torn tail)", md.MalformedRecords)
	}
	if md.TruncatedRecords != 0 {
		t.Errorf("truncated records = %d, want 0: no record exceeds the parser limit", md.TruncatedRecords)
	}
	// The one well-formed session_meta record still yields its facts.
	if md.SessionID != "aaaaaaaa-0000-4000-8000-00000000000c" {
		t.Errorf("session id = %q, want the parsable record's value", md.SessionID)
	}
	if desc.Meta.Workspace == nil || *desc.Meta.Workspace != "/synthetic/malformed-workspace" {
		t.Errorf("workspace = %v", desc.Meta.Workspace)
	}
}

func TestDescribeHostStateResolvesHistoryAndIndex(t *testing.T) {
	root := fixtureRoot(t)
	desc := describeOf(t, root, StateSourceID)

	requireDescribesRawFile(t, desc, filepath.Join(root, "history.jsonl"))
	if len(desc.Artifacts) != 1 || desc.Artifacts[0].RelPath != "session_index.jsonl" {
		t.Fatalf("artifacts = %+v, want session_index.jsonl", desc.Artifacts)
	}
	index := desc.Artifacts[0]
	if want := filepath.Join(root, "session_index.jsonl"); index.SourcePath != want {
		t.Errorf("index source path = %q, want %q", index.SourcePath, want)
	}
	fi, err := os.Stat(index.SourcePath)
	if err != nil {
		t.Fatalf("stat source index: %v", err)
	}
	if index.Size != fi.Size() {
		t.Errorf("index size = %d, want %d", index.Size, fi.Size())
	}

	requireCompleteness(t, desc.Meta)
	if desc.Meta.Workspace != nil {
		t.Error("host state must not claim a workspace")
	}
	if desc.Meta.CreatedAt == nil || desc.Meta.CreatedAt.Unix() != 1767322000 {
		t.Errorf("created_at = %v, want the earliest history timestamp", desc.Meta.CreatedAt)
	}
	if desc.Meta.ModifiedAt == nil || desc.Meta.ModifiedAt.Unix() != 1767409000 {
		t.Errorf("modified_at = %v, want the latest history timestamp", desc.Meta.ModifiedAt)
	}

	md := metadataOf(t, desc)
	if md.Kind != KindState || md.PrimaryPath != "history.jsonl" {
		t.Errorf("metadata kind/path = %q/%q", md.Kind, md.PrimaryPath)
	}
	if md.Records != 3 || md.MalformedRecords != 0 {
		t.Errorf("records = %d, malformed = %d, want 3/0", md.Records, md.MalformedRecords)
	}
	if md.SessionIndexFound == nil || !*md.SessionIndexFound {
		t.Errorf("session_index_found = %v, want true", md.SessionIndexFound)
	}
}

func TestDescribeHostStateWithoutSessionIndex(t *testing.T) {
	root := fixtureRoot(t)
	if err := os.Remove(filepath.Join(root, "session_index.jsonl")); err != nil {
		t.Fatalf("remove index: %v", err)
	}
	desc := describeOf(t, root, StateSourceID)
	if len(desc.Artifacts) != 0 {
		t.Errorf("artifacts = %+v, want none", desc.Artifacts)
	}
	md := metadataOf(t, desc)
	if md.SessionIndexFound == nil || *md.SessionIndexFound {
		t.Errorf("session_index_found = %v, want false", md.SessionIndexFound)
	}
}

// TestDescribeSurvivesConcurrentAppend records the consistency model: a
// live Codex process appending between two descriptions never fails the
// adapter; the later description simply supersedes the earlier one.
func TestDescribeSurvivesConcurrentAppend(t *testing.T) {
	root := fixtureRoot(t)
	primary := filepath.Join(root, filepath.FromSlash(fullID))

	before := describeOf(t, root, fullID)

	f, err := os.OpenFile(primary, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open source for append: %v", err)
	}
	appended := `{"timestamp":"2026-01-02T03:04:10.000Z","type":"event_msg","payload":{"type":"agent_message","message":"synthetic fixture message four"}}` + "\n"
	if _, err := f.WriteString(appended); err != nil {
		t.Fatalf("append to source: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close source: %v", err)
	}

	after := describeOf(t, root, fullID)
	if after.PrimarySize != before.PrimarySize+int64(len(appended)) {
		t.Errorf("PrimarySize = %d, want %d after the append", after.PrimarySize, before.PrimarySize+int64(len(appended)))
	}
	beforeMD, afterMD := metadataOf(t, before), metadataOf(t, after)
	if afterMD.Records != beforeMD.Records+1 {
		t.Errorf("records = %d, want %d after the append", afterMD.Records, beforeMD.Records+1)
	}
	if !after.DescribedAt.After(before.DescribedAt) && !after.DescribedAt.Equal(before.DescribedAt) {
		t.Errorf("DescribedAt went backwards: %v then %v", before.DescribedAt, after.DescribedAt)
	}
}

func TestDescribeRejectsForeignSource(t *testing.T) {
	root := fixtureRoot(t)
	src := adapter.SourceSession{Harness: "omp", SourceID: fullID, PrimaryPath: filepath.Join(root, filepath.FromSlash(fullID))}
	if _, err := New().Describe(context.Background(), src); err == nil {
		t.Fatal("describe accepted a source from another harness")
	}
	src = adapter.SourceSession{Harness: HarnessName, SourceID: "with spaces", PrimaryPath: "x"}
	if _, err := New().Describe(context.Background(), src); err == nil {
		t.Fatal("describe accepted an invalid source id")
	}
	src = adapter.SourceSession{Harness: HarnessName, SourceID: fullID}
	if _, err := New().Describe(context.Background(), src); err == nil {
		t.Fatal("describe accepted a source with no primary path")
	}
}

func TestDescribeHonorsContextCancellation(t *testing.T) {
	root := fixtureRoot(t)
	var src adapter.SourceSession
	for _, s := range discover(t, root) {
		if s.SourceID == fullID {
			src = s
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Describe(ctx, src); !errors.Is(err, context.Canceled) {
		t.Fatalf("Describe error = %v, want context.Canceled", err)
	}
}

func TestSourceIDForUnusualPaths(t *testing.T) {
	plain := filepath.Join("sessions", "2026", "01", "02", "rollout-a.jsonl")
	if got, want := sourceIDFor(plain), "sessions/2026/01/02/rollout-a.jsonl"; got != want {
		t.Errorf("sourceIDFor(%q) = %q, want %q", plain, got, want)
	}

	odd := filepath.Join("sessions", "2026", "a name with spaces.jsonl")
	got := sourceIDFor(odd)
	if !adapter.ValidSourceID(got) {
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

// TestDescribeOversizedRecords covers the bounded record reader: a record
// larger than the read buffer must still be parsed whole, and one larger
// than the parser limit must degrade to a flagged truncation while the
// described file keeps every byte.
func TestDescribeOversizedRecords(t *testing.T) {
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

	desc := describeOf(t, root, rel)
	requireDescribesRawFile(t, desc, log)

	md := metadataOf(t, desc)
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
	if desc.Meta.Workspace == nil || *desc.Meta.Workspace != "/synthetic/oversized" {
		t.Errorf("workspace = %v", desc.Meta.Workspace)
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
		if !adapter.ValidSourceID(s.SourceID) {
			t.Fatal("real root produced an invalid source id")
		}
		if s.SourceID == StateSourceID {
			state++
		}
	}
	t.Logf("discovered %d codex sessions (%d host-state)", len(found), state)
}
