// Package e2e_test drives Babel's shipped command-line entry point against
// a real restic repository and a synthetic three-harness HOME, in the order
// an operator would: back the machine up, list what is archived, inspect one
// session's closure, recover it, change the source and back it up again,
// recover the *older* generation, verify the repository's integrity, and
// report its state.
//
// It is deliberately one scenario rather than a set of independent cases:
// the property under test is that these commands compose over the same
// repository, which per-command unit tests cannot observe. The assertion no
// other suite covers is old-generation retrieval — after a source file has
// changed, the bytes of a named earlier snapshot are still recoverable
// exactly, and the newest snapshot yields the new bytes.
//
// Every byte of every fixture is synthesized here; nothing derives from a
// real session, transcript, or credential (SPEC.md §10).
package e2e_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/cli"
)

const (
	exitOK      = 0
	exitFailure = 1
)

// hostID is the host identity every snapshot in this suite is written
// under, so status can group by it deterministically.
const hostID = "e2ehost"

// resticPinnedPath is the restic build this project develops against. It is
// consulted only when the binary is absent from PATH; when neither exists
// the suite skips, because it asserts Babel's behavior against real restic
// rather than against a stand-in.
const resticPinnedPath = "/nix/store/h43lp2dls4gyj6zfxssywk9d8s49qisn-restic-0.19.1/bin/restic"

// repoPassword is a synthetic repository password.
const repoPassword = "synthetic-e2e-password\n"

// Fixture identities. The OMP stems mirror the real "<iso>_<uuid>" naming,
// the Codex rollout mirrors "rollout-<iso>-<uuid>.jsonl" under a date-keyed
// directory, and the Claude project directory mirrors the lossy dash
// encoding of a workspace path.
const (
	ompProjectRich     = "-synthetic-e2e-rich"
	ompProjectDangling = "-synthetic-e2e-dangling"
	ompStemRich        = "2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001"
	ompStemDangling    = "2026-01-03T06-07-08-900Z_00000000-0000-4000-8000-000000000002"

	codexRolloutDay  = "2026/01/02"
	codexRolloutName = "rollout-2026-01-02T03-04-05-aaaaaaaa-0000-4000-8000-000000000001.jsonl"

	claudeProjectDir  = "-synthetic-e2e-workspace-claude"
	claudeSessionUUID = "bbbbbbbb-0000-4000-8000-000000000001"
)

// Fixture titles and workspaces, which the listing must report verbatim.
const (
	titleRich     = "Synthetic e2e rich session"
	titleDangling = "Synthetic e2e dangling session"
	titleClaude   = "Synthetic e2e claude session"

	workspaceRich     = "/synthetic/workspace/rich"
	workspaceDangling = "/synthetic/workspace/dangling"
	workspaceCodex    = "/synthetic/workspace/codex"
	workspaceClaude   = "/synthetic/workspace/claude"
)

// danglingBlobRef is a well-formed reference to content that is not in the
// blob store, which is what makes its session fall short of continuation
// grade.
const danglingBlobRef = "blob:sha256:" +
	"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// padRecords and padRecordBytes size the rich session's synthetic bulk.
// Deduplication of an appended log is a content-defined-chunking property:
// restic re-uploads only the chunks a change touched, and a file smaller
// than one chunk has none to spare. The padding is random, so it is
// incompressible enough that "data added is smaller than the file" states
// dedup rather than compression.
//
// The size is not arbitrary. restic picks a random chunker polynomial per
// repository, so identical bytes chunk differently in each run's fresh
// repository, and a file below restic's 8 MiB maximum chunk size can legally
// come out as a single chunk - in which case appending re-stores all of it and
// the dedup assertion fails. That made this suite flake roughly one run in six
// with content that was otherwise byte-identical. Exceeding the maximum forces
// at least two chunks under every polynomial, which is what makes the
// assertion an invariant instead of a probability.
const (
	padRecords     = 1400
	padRecordBytes = 4096

	// resticMaxChunkBytes is restic's chunker upper bound. The padded log must
	// exceed it; assertAppendCanDeduplicate enforces that rather than trusting
	// the arithmetic above to survive editing.
	resticMaxChunkBytes = 8 << 20
)

// env is one hermetic Babel environment: a private HOME holding synthetic
// OMP, Codex, and Claude trees, private XDG data and cache directories, and
// a private restic repository.
type env struct {
	root         string
	home         string
	repoDir      string
	passwordFile string
	dataHome     string
	cacheHome    string

	ompSessions string
	ompBlobs    string
	codexHome   string
	claudeHome  string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{
		root:         root,
		home:         filepath.Join(root, "home"),
		repoDir:      filepath.Join(t.TempDir(), "repo"),
		passwordFile: filepath.Join(root, "password"),
		dataHome:     filepath.Join(root, "data"),
		cacheHome:    filepath.Join(root, "cache"),
	}
	e.ompSessions = filepath.Join(e.home, ".omp", "agent", "sessions")
	e.ompBlobs = filepath.Join(e.home, ".omp", "agent", "blobs")
	e.codexHome = filepath.Join(e.home, ".codex")
	e.claudeHome = filepath.Join(e.home, ".claude")

	mkdir(t, e.home)
	if err := os.WriteFile(e.passwordFile, []byte(repoPassword), 0o600); err != nil {
		t.Fatal(err)
	}

	// The adapters resolve every root from HOME, so a private HOME is what
	// makes the scan hermetic. The Babel environment defaults are cleared
	// so repository selection is only ever what a command was given.
	t.Setenv("HOME", e.home)
	t.Setenv("XDG_DATA_HOME", e.dataHome)
	t.Setenv("XDG_CACHE_HOME", e.cacheHome)
	t.Setenv("BABEL_RESTIC_REPO", "")
	t.Setenv("BABEL_RESTIC_PASSWORD_FILE", "")
	t.Setenv("BABEL_HOST_ID", hostID)
	t.Setenv("CODEX_HOME", e.codexHome)
	t.Setenv("PATH", filepath.Dir(resticBinary(t))+string(os.PathListSeparator)+os.Getenv("PATH"))
	return e
}

// resticBinary locates the restic to test against, skipping when the host
// has none.
func resticBinary(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("restic"); err == nil {
		return path
	}
	if info, err := os.Stat(resticPinnedPath); err == nil && info.Mode().IsRegular() {
		return resticPinnedPath
	}
	t.Skip("restic binary not available")
	return ""
}

// with appends this environment's repository selection to a command.
func (e *env) with(args ...string) []string {
	return append(args, "--repo", e.repoDir, "--password-file", e.passwordFile)
}

// run drives one invocation exactly as cmd/babel does.
func (e *env) run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = cli.Run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// ok drives one invocation that must succeed.
func (e *env) ok(t *testing.T, args ...string) (stdout, stderr string) {
	t.Helper()
	stdout, stderr, code := e.run(t, args...)
	if code != exitOK {
		t.Fatalf("babel %s exited %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout, stderr
}

// okJSON drives one successful --json invocation and decodes the single
// document it must have written to stdout. It also holds the streams to
// their contract: the result document goes to stdout alone.
func okJSON[T any](t *testing.T, e *env, args ...string) T {
	t.Helper()
	stdout, stderr := e.ok(t, args...)
	assertNoJSONOnStderr(t, strings.Join(args, " "), stderr)
	return decode[T](t, stdout)
}

// decode parses one --json result document, proving stdout carries exactly
// that document and nothing else. Unknown fields are rejected so a change
// to the machine-readable shape is caught here rather than by a consumer.
func decode[T any](t *testing.T, stdout string) T {
	t.Helper()
	var v T
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not the expected JSON document: %v\nstdout:\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one document:\n%s", stdout)
	}
	return v
}

// assertNoJSONOnStderr fails when a happy-path invocation put result data
// on the diagnostic stream.
func assertNoJSONOnStderr(t *testing.T, label, stderr string) {
	t.Helper()
	if strings.Contains(stderr, "{") {
		t.Fatalf("babel %s wrote result data to stderr: %q", label, stderr)
	}
}

// Machine-readable result shapes. They mirror the documents the CLI emits;
// decoding with DisallowUnknownFields makes this suite a consumer-side
// contract test of those shapes.

type pushResult struct {
	Host            string   `json:"host"`
	Tags            []string `json:"tags"`
	Roots           []string `json:"roots"`
	SnapshotID      string   `json:"snapshot_id"`
	FilesNew        int      `json:"files_new"`
	FilesChanged    int      `json:"files_changed"`
	FilesUnmodified int      `json:"files_unmodified"`
	DataAdded       int64    `json:"data_added"`
	FilesProcessed  int      `json:"total_files_processed"`
	BytesProcessed  int64    `json:"total_bytes_processed"`
	Incomplete      bool     `json:"incomplete"`
	// Catalog is "local" throughout this suite: it configures no shared
	// catalog, so a push must say so rather than claiming it published.
	Catalog           string `json:"catalog"`
	SessionsPublished int    `json:"sessions_published"`
}

type statusHostRow struct {
	Host          string   `json:"host"`
	Snapshots     int      `json:"snapshots"`
	LatestTime    string   `json:"latest_time"`
	LatestID      string   `json:"latest_id"`
	LatestShortID string   `json:"latest_short_id"`
	Tags          []string `json:"tags,omitempty"`
}

type statusResult struct {
	Repository string          `json:"repository"`
	Snapshots  int             `json:"snapshots"`
	Hosts      []statusHostRow `json:"hosts"`
}

type verifyResult struct {
	Repository string `json:"repository"`
	Deep       bool   `json:"deep"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

type sessionRow struct {
	Harness    string  `json:"harness"`
	SourceID   string  `json:"source_id"`
	Selector   string  `json:"selector"`
	Size       int64   `json:"size"`
	Modified   *string `json:"modified"`
	Title      *string `json:"title"`
	Workspace  *string `json:"workspace"`
	Continuous bool    `json:"continuation_grade"`
}

type sessionsResult struct {
	Sessions []sessionRow `json:"sessions"`
}

type completenessRow struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

type repoRow struct {
	Remote string `json:"remote,omitempty"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
}

type fileRow struct {
	RelPath    string `json:"rel_path"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
}

type blobRow struct {
	Digest     string `json:"digest"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
}

type inspectResult struct {
	Harness     string `json:"harness"`
	SourceID    string `json:"source_id"`
	Selector    string `json:"selector"`
	PrimaryPath string `json:"primary_path"`
	PrimarySize int64  `json:"primary_size"`
	DescribedAt string `json:"described_at"`
	Hint        string `json:"hint,omitempty"`

	Title        *string           `json:"title"`
	Workspace    *string           `json:"workspace"`
	CreatedAt    *string           `json:"created_at"`
	ModifiedAt   *string           `json:"modified_at"`
	Lifecycle    *string           `json:"lifecycle"`
	Repo         *repoRow          `json:"repo"`
	Completeness []completenessRow `json:"completeness,omitempty"`

	AdapterMetadataSchema int             `json:"adapter_metadata_schema"`
	AdapterMetadata       json.RawMessage `json:"adapter_metadata,omitempty"`

	Artifacts          []fileRow `json:"artifacts,omitempty"`
	Blobs              []blobRow `json:"blobs,omitempty"`
	UnresolvedBlobRefs []string  `json:"unresolved_blob_refs,omitempty"`
	ContinuationGrade  bool      `json:"continuation_grade"`
}

type fetchResult struct {
	Selector        string   `json:"selector"`
	SnapshotID      string   `json:"snapshot_id"`
	SnapshotShortID string   `json:"snapshot_short_id"`
	SnapshotTime    string   `json:"snapshot_time"`
	Target          string   `json:"target"`
	Files           int      `json:"files"`
	Bytes           int64    `json:"bytes"`
	Included        []string `json:"included"`
	Missing         []string `json:"missing,omitempty"`
	AlreadyPresent  bool     `json:"already_present"`
}

// TestPhaseALoopEndToEnd is the whole Phase A loop over one repository.
func TestPhaseALoopEndToEnd(t *testing.T) {
	e := newEnv(t)
	src := e.writeSources(t)

	// 1. Back the machine up. Every harness root that exists must be in
	// the snapshot, including OMP's blob store: a session whose referenced
	// content was left behind could never be continued (SPEC.md §3).
	first := okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)
	if first.SnapshotID == "" {
		t.Fatalf("push reported no snapshot: %+v", first)
	}
	if first.Host != hostID {
		t.Fatalf("push host = %q, want %q", first.Host, hostID)
	}
	if first.Incomplete {
		t.Fatalf("push reported an incomplete backup: %+v", first)
	}
	// No shared catalog is configured here, so the push must report that
	// plainly rather than implying it published anything fleet-wide.
	if first.Catalog != "local" {
		t.Fatalf("push catalog state = %q, want %q with no shared catalog configured: %+v",
			first.Catalog, "local", first)
	}
	if first.SessionsPublished != 0 {
		t.Fatalf("push claimed to publish %d session rows with no shared catalog: %+v",
			first.SessionsPublished, first)
	}
	if first.FilesNew == 0 {
		t.Fatalf("push stored no new files: %+v", first)
	}
	if first.FilesProcessed == 0 || first.BytesProcessed == 0 {
		t.Fatalf("push processed nothing: %+v", first)
	}
	wantRoots := []string{e.claudeHome, e.codexHome, e.ompBlobs, e.ompSessions}
	if !slices.Equal(first.Roots, wantRoots) {
		t.Fatalf("push roots = %v, want every harness root %v", first.Roots, wantRoots)
	}

	// 2. The catalog names every session on the machine, across harnesses,
	// reporting only what each format actually exposes.
	list := okJSON[sessionsResult](t, e, "sessions", "list", "--json")
	rows := make(map[string]sessionRow, len(list.Sessions))
	for _, row := range list.Sessions {
		if row.Selector != row.Harness+"/"+row.SourceID {
			t.Fatalf("row selector %q does not compose from harness and source id: %+v", row.Selector, row)
		}
		if row.Size == 0 {
			t.Fatalf("row %s reports no size: %+v", row.Selector, row)
		}
		if row.Modified == nil || *row.Modified == "" {
			t.Fatalf("row %s reports no modification time: %+v", row.Selector, row)
		}
		rows[row.Selector] = row
	}
	if len(list.Sessions) != 5 {
		t.Fatalf("listed %d sessions, want 5: %+v", len(list.Sessions), keysOf(rows))
	}
	for _, want := range []struct {
		selector   string
		title      *string
		workspace  *string
		continuous bool
	}{
		{src.richSelector, new(titleRich), new(workspaceRich), true},
		{src.danglingSelector, new(titleDangling), new(workspaceDangling), false},
		// Codex rollouts expose a working directory but no title, and its
		// host state is not workspace-scoped at all.
		{src.codexSelector, nil, new(workspaceCodex), false},
		{"codex/state", nil, nil, false},
		{src.claudeSelector, new(titleClaude), new(workspaceClaude), false},
	} {
		row, ok := rows[want.selector]
		if !ok {
			t.Fatalf("session %q is missing from the listing: %v", want.selector, keysOf(rows))
		}
		if !equalStrPtr(row.Title, want.title) {
			t.Fatalf("%s title = %s, want %s", want.selector, showPtr(row.Title), showPtr(want.title))
		}
		if !equalStrPtr(row.Workspace, want.workspace) {
			t.Fatalf("%s workspace = %s, want %s", want.selector, showPtr(row.Workspace), showPtr(want.workspace))
		}
		if row.Continuous != want.continuous {
			t.Fatalf("%s continuation_grade = %v, want %v", want.selector, row.Continuous, want.continuous)
		}
	}

	// 3. Inspecting a session shows its whole closure, and grades it by
	// whether that closure is complete.
	rich := okJSON[inspectResult](t, e, "sessions", "inspect", src.richSelector, "--json")
	if rich.Selector != src.richSelector || rich.PrimaryPath != src.richPrimary {
		t.Fatalf("inspect resolved the wrong session: %+v", rich)
	}
	if len(rich.Artifacts) != 2 {
		t.Fatalf("inspect artifacts = %+v, want the two files of the sibling tree", rich.Artifacts)
	}
	for _, want := range []string{"Helper.jsonl", "nested/7.bash.log"} {
		if !slices.ContainsFunc(rich.Artifacts, func(f fileRow) bool { return f.RelPath == want }) {
			t.Fatalf("artifact %q is missing: %+v", want, rich.Artifacts)
		}
	}
	if len(rich.Blobs) != 1 {
		t.Fatalf("inspect blobs = %+v, want the one resolvable reference", rich.Blobs)
	}
	if rich.Blobs[0].Digest != "sha256:"+src.blobHex {
		t.Fatalf("blob digest = %q, want the referenced content's digest", rich.Blobs[0].Digest)
	}
	if rich.Blobs[0].SourcePath != filepath.Join(e.ompBlobs, src.blobHex) {
		t.Fatalf("blob source path = %q, want it inside the blob store", rich.Blobs[0].SourcePath)
	}
	if len(rich.UnresolvedBlobRefs) != 0 || !rich.ContinuationGrade {
		t.Fatalf("a complete closure was not graded continuable: %+v", rich)
	}

	dangling := okJSON[inspectResult](t, e, "sessions", "inspect", src.danglingSelector, "--json")
	if !slices.Equal(dangling.UnresolvedBlobRefs, []string{danglingBlobRef}) {
		t.Fatalf("unresolved refs = %v, want the dangling reference", dangling.UnresolvedBlobRefs)
	}
	if len(dangling.Blobs) != 0 {
		t.Fatalf("a dangling reference resolved to content: %+v", dangling.Blobs)
	}
	if dangling.ContinuationGrade {
		t.Fatalf("an incomplete closure was graded continuable: %+v", dangling)
	}

	// 4. Fetching recovers the session's bytes from the snapshot, and
	// doing it twice changes nothing.
	sourceBytes := readFile(t, src.richPrimary)
	fetch := okJSON[fetchResult](t, e, e.with("sessions", "fetch", src.richSelector, "--json")...)
	if fetch.AlreadyPresent {
		t.Fatalf("the first fetch claimed the target already existed: %+v", fetch)
	}
	if fetch.SnapshotID != first.SnapshotID {
		t.Fatalf("fetch used snapshot %q, want the only one there is (%q)", fetch.SnapshotID, first.SnapshotID)
	}
	if len(fetch.Missing) != 0 {
		t.Fatalf("the snapshot was missing part of the closure: %v", fetch.Missing)
	}
	if got := readFile(t, filepath.Join(fetch.Target, src.richPrimary)); !bytes.Equal(got, sourceBytes) {
		t.Fatalf("restored primary log differs from its source (%d vs %d bytes)", len(got), len(sourceBytes))
	}
	for _, rel := range []string{"Helper.jsonl", filepath.Join("nested", "7.bash.log")} {
		if _, err := os.Stat(filepath.Join(fetch.Target, src.richArtifactDir, rel)); err != nil {
			t.Fatalf("restored artifact %s is missing: %v", rel, err)
		}
	}
	blobCopy := filepath.Join(fetch.Target, e.ompBlobs, src.blobHex)
	if got := readFile(t, blobCopy); string(got) != src.blobContent {
		t.Fatalf("restored blob content = %q, want the referenced payload", got)
	}

	before := treeDigest(t, fetch.Target)
	again := okJSON[fetchResult](t, e, e.with("sessions", "fetch", src.richSelector, "--json")...)
	if !again.AlreadyPresent {
		t.Fatalf("the second fetch re-restored the session: %+v", again)
	}
	if again.Target != fetch.Target || again.Files != fetch.Files {
		t.Fatalf("the second fetch disagreed with the first: %+v vs %+v", again, fetch)
	}
	if diff := diffTrees(before, treeDigest(t, fetch.Target)); diff != "" {
		t.Fatalf("the second fetch rewrote the target: %s", diff)
	}

	// 5. The source changes and is backed up again. The append must
	// deduplicate against the first snapshot, and — the property this
	// suite exists for — the earlier generation must remain recoverable
	// byte for byte while the newest snapshot yields the new bytes.
	appended := append(slices.Clone(sourceBytes),
		[]byte("{\"type\":\"message\",\"id\":\"f0009999\",\"timestamp\":\"2026-01-02T05:00:00.000Z\","+
			"\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"synthetic appended turn\"}]}}\n")...)
	if err := os.WriteFile(src.richPrimary, appended, 0o600); err != nil {
		t.Fatal(err)
	}

	second := okJSON[pushResult](t, e, e.with("archive", "push", "--json")...)
	if second.SnapshotID == "" || second.SnapshotID == first.SnapshotID {
		t.Fatalf("the second push did not create a new snapshot: %+v", second)
	}
	if second.FilesChanged < 1 {
		t.Fatalf("the second push saw no changed file: %+v", second)
	}
	if second.FilesUnmodified == 0 {
		t.Fatalf("the second push re-read every file: %+v", second)
	}
	// Assert the precondition rather than trusting the padding constants to
	// survive editing: below restic's maximum chunk size the file may be a
	// single chunk under an unlucky per-repository polynomial, and then an
	// append genuinely re-stores everything. Without this the assertion below
	// silently becomes a coin flip.
	if len(appended) <= resticMaxChunkBytes {
		t.Fatalf("the padded log is %d bytes, which does not exceed restic's %d-byte maximum chunk: "+
			"an append is not guaranteed to leave any chunk untouched, so the dedup assertion would be a probability",
			len(appended), resticMaxChunkBytes)
	}
	// Report the accounting so a future tightening of this bound can be argued
	// from observed numbers rather than from reasoning about restic internals.
	t.Logf("append dedup: file=%d bytes, first push added=%d, second push added=%d",
		len(appended), first.DataAdded, second.DataAdded)
	if second.DataAdded >= int64(len(appended)) {
		t.Fatalf("the second push added %d bytes for a %d-byte file: an appended log must deduplicate",
			second.DataAdded, len(appended))
	}

	// A fresh data directory forces both recoveries to be real restores
	// rather than the idempotent no-op a materialized target would give.
	t.Setenv("XDG_DATA_HOME", filepath.Join(e.root, "data-second-generation"))

	old := okJSON[fetchResult](t, e, e.with("sessions", "fetch", src.richSelector, "--snapshot", first.SnapshotID, "--json")...)
	if old.SnapshotID != first.SnapshotID || old.AlreadyPresent {
		t.Fatalf("the pinned fetch did not restore the first snapshot: %+v", old)
	}
	if got := readFile(t, filepath.Join(old.Target, src.richPrimary)); !bytes.Equal(got, sourceBytes) {
		t.Fatalf("snapshot %s no longer yields the bytes it archived (%d vs %d bytes)",
			old.SnapshotShortID, len(got), len(sourceBytes))
	}

	latest := okJSON[fetchResult](t, e, e.with("sessions", "fetch", src.richSelector, "--json")...)
	if latest.SnapshotID != second.SnapshotID || latest.AlreadyPresent {
		t.Fatalf("the latest fetch did not restore the newest snapshot: %+v", latest)
	}
	if latest.Target == old.Target {
		t.Fatalf("two generations shared one target directory: %q", latest.Target)
	}
	if got := readFile(t, filepath.Join(latest.Target, src.richPrimary)); !bytes.Equal(got, appended) {
		t.Fatalf("the newest snapshot did not yield the appended bytes (%d vs %d bytes)",
			len(got), len(appended))
	}

	// 6. Integrity. A healthy repository verifies; a repository with one
	// flipped byte in a data pack must not verify deeply; repairing the
	// byte restores the verdict.
	if v := okJSON[verifyResult](t, e, e.with("archive", "verify", "--json")...); !v.OK || v.Deep || v.Error != "" {
		t.Fatalf("verify of a healthy repository = %+v", v)
	}

	pack := largestPack(t, filepath.Join(e.repoDir, "data"))
	original, mode := readFileMode(t, pack)
	writePack(t, pack, flipOneByte(original))
	e.dropResticCache(t)

	stdout, stderr, code := e.run(t, e.with("archive", "verify", "--deep", "--json")...)
	deep := decode[verifyResult](t, stdout)
	if code != exitFailure || deep.OK || !deep.Deep || deep.Error == "" {
		t.Fatalf("deep verify passed over a tampered pack: exit %d, %+v", code, deep)
	}
	if !strings.Contains(stderr, "verify repository") {
		t.Fatalf("deep verify gave no detail on stderr: %q", stderr)
	}

	// The structural check reads index and metadata rather than pack
	// contents, so it is expected to tolerate this corruption; if this
	// restic build does notice it, that is stricter than required and the
	// suite records it rather than failing.
	stdout, _, code = e.run(t, e.with("archive", "verify", "--json")...)
	shallow := decode[verifyResult](t, stdout)
	switch code {
	case exitOK:
		if !shallow.OK || shallow.Deep {
			t.Fatalf("a passing verify reported failure: %+v", shallow)
		}
	case exitFailure:
		if shallow.OK {
			t.Fatalf("a failing verify reported success: %+v", shallow)
		}
		t.Logf("note: restic's structural check also rejects the tampered pack: %s", shallow.Error)
	default:
		t.Fatalf("verify exited %d, want 0 or 1: %+v", code, shallow)
	}

	writePack(t, pack, original)
	if err := os.Chmod(pack, mode); err != nil {
		t.Fatal(err)
	}
	e.dropResticCache(t)
	if v := okJSON[verifyResult](t, e, e.with("archive", "verify", "--deep", "--json")...); !v.OK || !v.Deep || v.Error != "" {
		t.Fatalf("deep verify of the repaired repository = %+v", v)
	}

	// 7. Status reports what the repository now holds.
	status := okJSON[statusResult](t, e, e.with("archive", "status", "--json")...)
	if status.Snapshots != 2 {
		t.Fatalf("status snapshots = %d, want the two pushes", status.Snapshots)
	}
	if len(status.Hosts) != 1 {
		t.Fatalf("status hosts = %+v, want exactly this machine", status.Hosts)
	}
	host := status.Hosts[0]
	if host.Host != hostID || host.Snapshots != 2 {
		t.Fatalf("status host row = %+v", host)
	}
	if host.LatestID != second.SnapshotID {
		t.Fatalf("status latest id = %q, want the newest push %q", host.LatestID, second.SnapshotID)
	}
	if host.LatestTime == "" || host.LatestShortID == "" {
		t.Fatalf("status host row is missing latest state: %+v", host)
	}
}

// sources names the synthetic fixtures the scenario addresses.
type sources struct {
	richPrimary      string
	richArtifactDir  string
	richSelector     string
	danglingSelector string
	codexSelector    string
	claudeSelector   string
	blobHex          string
	blobContent      string
}

// writeSources materializes all three harnesses' trees in the layouts the
// adapters document: OMP sessions with a sibling artifact tree and a
// content-addressed blob store, a Codex home with one rollout plus its two
// host-state files, and a Claude home with one project transcript.
func (e *env) writeSources(t *testing.T) sources {
	t.Helper()
	blobContent := "synthetic e2e blob payload\n"
	blobHex := e.writeBlob(t, blobContent)

	richPrimary := e.writeOMPSession(t, ompSpec{
		project:   ompProjectRich,
		stem:      ompStemRich,
		id:        "00000000-0000-4000-8000-000000000001",
		title:     titleRich,
		workspace: workspaceRich,
		blobRef:   "blob:sha256:" + blobHex,
		pad:       true,
		artifacts: map[string]string{
			"Helper.jsonl":      "{\"type\":\"message\",\"text\":\"synthetic subagent transcript\"}\n",
			"nested/7.bash.log": "synthetic command output\n",
		},
	})
	e.writeOMPSession(t, ompSpec{
		project:   ompProjectDangling,
		stem:      ompStemDangling,
		id:        "00000000-0000-4000-8000-000000000002",
		title:     titleDangling,
		workspace: workspaceDangling,
		blobRef:   danglingBlobRef,
	})
	e.writeCodexHome(t)
	e.writeClaudeHome(t)

	return sources{
		richPrimary:      richPrimary,
		richArtifactDir:  strings.TrimSuffix(richPrimary, ".jsonl"),
		richSelector:     "omp/" + ompProjectRich + "/" + ompStemRich,
		danglingSelector: "omp/" + ompProjectDangling + "/" + ompStemDangling,
		codexSelector:    "codex/sessions/" + codexRolloutDay + "/" + codexRolloutName,
		claudeSelector:   "claude/" + claudeProjectDir + "/" + claudeSessionUUID,
		blobHex:          blobHex,
		blobContent:      blobContent,
	}
}

// writeBlob stores one synthetic payload in OMP's content-addressed blob
// store, whose file name is the payload's SHA-256 hex.
func (e *env) writeBlob(t *testing.T, content string) string {
	t.Helper()
	mkdir(t, e.ompBlobs)
	sum := sha256.Sum256([]byte(content))
	name := hex.EncodeToString(sum[:])
	writeFile(t, filepath.Join(e.ompBlobs, name), []byte(content))
	return name
}

// ompSpec describes one synthetic OMP session.
type ompSpec struct {
	project   string
	stem      string
	id        string
	title     string
	workspace string
	blobRef   string
	artifacts map[string]string
	// pad fills the log with synthetic bulk so it spans several restic
	// chunks, which is what makes deduplication of an append observable.
	pad bool
}

// writeOMPSession materializes one session in OMP's layout:
// "<sessions>/<project>/<stem>.jsonl" for the primary log, with a sibling
// "<stem>/" directory holding its artifact tree.
func (e *env) writeOMPSession(t *testing.T, spec ompSpec) string {
	t.Helper()
	dir := filepath.Join(e.ompSessions, spec.project)
	mkdir(t, dir)

	var b bytes.Buffer
	b.Write(jsonLine(t, map[string]any{
		"type":      "title",
		"v":         1,
		"title":     spec.title,
		"source":    "auto",
		"updatedAt": "2026-01-02T04:00:00.000Z",
	}))
	b.Write(jsonLine(t, map[string]any{
		"type":        "session",
		"version":     3,
		"id":          spec.id,
		"timestamp":   "2026-01-02T03:04:05.678Z",
		"cwd":         spec.workspace,
		"titleSource": "auto",
	}))
	content := []any{map[string]any{"type": "text", "text": "synthetic fixture message"}}
	if spec.blobRef != "" {
		content = append(content, map[string]any{"type": "image", "data": spec.blobRef, "mimeType": "image/webp"})
	}
	b.Write(jsonLine(t, map[string]any{
		"type":      "message",
		"id":        "f0000001",
		"timestamp": "2026-01-02T03:10:00.000Z",
		"message":   map[string]any{"role": "user", "content": content},
	}))
	if spec.pad {
		writePadding(t, &b)
	}

	primary := filepath.Join(dir, spec.stem+".jsonl")
	writeFile(t, primary, b.Bytes())
	for rel, body := range spec.artifacts {
		writeFile(t, filepath.Join(dir, spec.stem, filepath.FromSlash(rel)), []byte(body))
	}
	return primary
}

// writePadding appends synthetic assistant records carrying random hex, so
// the log is large and incompressible enough for content-defined chunking
// to have interior chunks an append cannot touch. The generator is seeded,
// so a run is reproducible.
func writePadding(t *testing.T, b *bytes.Buffer) {
	t.Helper()
	rng := rand.New(rand.NewPCG(0x9E3779B97F4A7C15, 0xBF58476D1CE4E5B9))
	raw := make([]byte, padRecordBytes)
	for i := range padRecords {
		for j := 0; j < len(raw); j += 8 {
			v := rng.Uint64()
			for k := 0; k < 8 && j+k < len(raw); k++ {
				raw[j+k] = byte(v >> (8 * k))
			}
		}
		b.Write(jsonLine(t, map[string]any{
			"type":      "message",
			"id":        "p" + hex.EncodeToString([]byte{byte(i >> 8), byte(i)}),
			"timestamp": "2026-01-02T03:20:00.000Z",
			"message": map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "text", "text": hex.EncodeToString(raw)}},
			},
		}))
	}
}

// writeCodexHome materializes a Codex home: one rollout log under the
// date-keyed sessions tree, plus the two host-level state files the adapter
// describes as one dedicated "state" session.
func (e *env) writeCodexHome(t *testing.T) {
	t.Helper()
	rollout := filepath.Join(e.codexHome, "sessions", filepath.FromSlash(codexRolloutDay), codexRolloutName)
	var b bytes.Buffer
	b.Write(jsonLine(t, map[string]any{
		"timestamp": "2026-01-02T03:04:05.100Z",
		"type":      "session_meta",
		"payload": map[string]any{
			"session_id":  "aaaaaaaa-0000-4000-8000-00000000000a",
			"id":          "aaaaaaaa-0000-4000-8000-000000000001",
			"timestamp":   "2026-01-02T03:04:05.100Z",
			"cwd":         workspaceCodex,
			"originator":  "Synthetic Fixture Harness",
			"cli_version": "0.0.0-synthetic",
		},
	}))
	b.Write(jsonLine(t, map[string]any{
		"timestamp": "2026-01-02T03:04:05.200Z",
		"type":      "turn_context",
		"payload": map[string]any{
			"approval_policy": "on-request",
			"cwd":             workspaceCodex,
			"model":           "synthetic-model",
			"turn_id":         "synthetic-turn-1",
			"workspace_roots": []string{workspaceCodex},
		},
	}))
	b.Write(jsonLine(t, map[string]any{
		"timestamp": "2026-01-02T03:04:06.000Z",
		"type":      "response_item",
		"payload": map[string]any{
			"id":      "synthetic-item-1",
			"type":    "message",
			"role":    "user",
			"content": []any{map[string]any{"type": "input_text", "text": "synthetic fixture message one"}},
		},
	}))
	writeFile(t, rollout, b.Bytes())

	writeFile(t, filepath.Join(e.codexHome, "history.jsonl"),
		bytes.Join([][]byte{
			jsonLine(t, map[string]any{
				"session_id": "aaaaaaaa-0000-4000-8000-00000000000a",
				"ts":         1767322000,
				"text":       "synthetic fixture prompt one",
			}),
			jsonLine(t, map[string]any{
				"session_id": "aaaaaaaa-0000-4000-8000-00000000000b",
				"ts":         1767322500,
				"text":       "synthetic fixture prompt two",
			}),
		}, nil))

	writeFile(t, filepath.Join(e.codexHome, "session_index.jsonl"),
		jsonLine(t, map[string]any{
			"id":          "aaaaaaaa-0000-4000-8000-000000000001",
			"thread_name": "synthetic fixture thread one",
			"updated_at":  "2026-01-02T03:04:09.500000000Z",
		}))
}

// writeClaudeHome materializes a Claude Code home: one transcript inside a
// project directory whose name is the lossy dash encoding of a workspace
// path.
func (e *env) writeClaudeHome(t *testing.T) {
	t.Helper()
	transcript := filepath.Join(e.claudeHome, "projects", claudeProjectDir, claudeSessionUUID+".jsonl")
	var b bytes.Buffer
	b.Write(jsonLine(t, map[string]any{
		"type":      "ai-title",
		"aiTitle":   titleClaude,
		"sessionId": claudeSessionUUID,
	}))
	b.Write(jsonLine(t, map[string]any{
		"parentUuid": nil,
		"type":       "user",
		"cwd":        workspaceClaude,
		"sessionId":  claudeSessionUUID,
		"version":    "9.9.999",
		"gitBranch":  "main",
		"message":    map[string]any{"role": "user", "content": "synthetic fixture message one"},
		"uuid":       "11111111-0000-4000-8000-000000000101",
		"timestamp":  "2026-03-01T08:00:00.000Z",
	}))
	b.Write(jsonLine(t, map[string]any{
		"parentUuid": "11111111-0000-4000-8000-000000000101",
		"type":       "assistant",
		"cwd":        workspaceClaude,
		"sessionId":  claudeSessionUUID,
		"version":    "9.9.999",
		"gitBranch":  "main",
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": "synthetic fixture reply one"}},
		},
		"uuid":      "11111111-0000-4000-8000-000000000102",
		"timestamp": "2026-03-02T09:15:09.250Z",
	}))
	writeFile(t, transcript, b.Bytes())
}

// dropResticCache removes restic's metadata cache, so a check reads the
// repository itself rather than a cached view of it.
func (e *env) dropResticCache(t *testing.T) {
	t.Helper()
	if err := os.RemoveAll(filepath.Join(e.cacheHome, "babel")); err != nil {
		t.Fatal(err)
	}
}

// largestPack picks the biggest pack file below dir, which is the one
// holding the fixture's file data rather than its tree metadata.
func largestPack(t *testing.T, dir string) string {
	t.Helper()
	var best string
	var bestSize int64
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Size() > bestSize {
			best, bestSize = path, info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if best == "" {
		t.Fatalf("no pack file below %s", dir)
	}
	return best
}

// flipOneByte returns a copy of raw with exactly one byte inverted, leaving
// its length untouched so only a content check can notice.
func flipOneByte(raw []byte) []byte {
	out := slices.Clone(raw)
	out[len(out)/2] ^= 0xff
	return out
}

// writePack overwrites a repository pack file, which restic keeps
// read-only.
func writePack(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// treeDigest digests every file below root so a test can prove a command
// left a tree byte-identical.
func treeDigest(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// diffTrees describes the first difference between two digest maps, and the
// empty string when they are identical.
func diffTrees(before, after map[string]string) string {
	for path, sum := range before {
		other, ok := after[path]
		if !ok {
			return "missing " + path
		}
		if other != sum {
			return "changed " + path
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			return "added " + path
		}
	}
	return ""
}

// jsonLine renders one JSONL record.
func jsonLine(t *testing.T, rec map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readFileMode(t *testing.T, path string) ([]byte, fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return readFile(t, path), info.Mode().Perm()
}

func equalStrPtr(got, want *string) bool {
	switch {
	case got == nil && want == nil:
		return true
	case got == nil || want == nil:
		return false
	default:
		return *got == *want
	}
}

func showPtr(p *string) string {
	if p == nil {
		return "absent"
	}
	return "\"" + *p + "\""
}

func keysOf(m map[string]sessionRow) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
