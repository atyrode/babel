package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/archive"
)

// testHostID is the host identity every fixture publishes under.
const testHostID = "testhost"

// fixture is one hermetic CLI environment: a synthetic OMP source tree
// under a private HOME, private XDG state/data/cache directories, and an
// empty local archive root. Tests drive Run exactly as cmd/babel does, so
// what they exercise is the shipped wiring, not a parallel harness.
//
// All fixture content is synthetic; no transcript ever comes from a real
// session (SPEC.md §10).
type fixture struct {
	t           *testing.T
	root        string
	home        string
	sessionsDir string
	blobsDir    string
	archiveRoot string
	stateDir    string
	dataDir     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t:           t,
		root:        root,
		home:        filepath.Join(root, "home"),
		archiveRoot: filepath.Join(root, "archive"),
		stateDir:    filepath.Join(root, "state", "babel"),
		dataDir:     filepath.Join(root, "data", "babel"),
	}
	f.sessionsDir = filepath.Join(f.home, ".omp", "agent", "sessions")
	f.blobsDir = filepath.Join(f.home, ".omp", "agent", "blobs")
	for _, dir := range []string{f.sessionsDir, f.blobsDir, f.archiveRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", f.home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("BABEL_HOST_ID", testHostID)
	// The Codex and Claude adapters must find nothing: this fixture only
	// synthesizes an OMP tree, and their default roots live under the
	// private HOME, which has none.
	t.Setenv("CODEX_HOME", filepath.Join(root, "absent-codex"))
	return f
}

// run drives one invocation and returns its stdout, stderr, and exit code.
func (f *fixture) run(args ...string) (stdout, stderr string, code int) {
	f.t.Helper()
	var out, errOut bytes.Buffer
	code = Run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// ok drives one invocation that must succeed.
func (f *fixture) ok(args ...string) (stdout, stderr string) {
	f.t.Helper()
	stdout, stderr, code := f.run(args...)
	if code != exitOK {
		f.t.Fatalf("babel %s exited %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout, stderr
}

// withStore appends this fixture's local archive selection to a command.
func (f *fixture) withStore(args ...string) []string {
	return append(args, "--archive-backend", "local", "--archive-root", f.archiveRoot)
}

// blob writes one synthetic blob into the OMP blob store and returns the
// "blob:sha256:<hex>" reference a session log embeds.
func (f *fixture) blob(content string) string {
	f.t.Helper()
	sum := sha256.Sum256([]byte(content))
	name := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(f.blobsDir, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return "blob:sha256:" + name
}

// sessionSpec describes one synthetic OMP session to materialize.
type sessionSpec struct {
	project   string
	stem      string
	title     string
	workspace string
	blobRef   string
	artifacts map[string]string
}

// writeSession materializes one synthetic session in the OMP layout the
// adapter documents: "<sessions>/<project>/<stem>.jsonl" for the primary
// log plus a sibling "<stem>/" directory for its artifact tree.
func (f *fixture) writeSession(spec sessionSpec) {
	f.t.Helper()
	dir := filepath.Join(f.sessionsDir, spec.project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	var b strings.Builder
	b.Write(jsonLine(f.t, map[string]any{
		"type":      "title",
		"v":         1,
		"title":     spec.title,
		"source":    "auto",
		"updatedAt": "2026-01-02T04:00:00.000Z",
		"pad":       strings.Repeat(" ", 16),
	}))
	b.Write(jsonLine(f.t, map[string]any{
		"type":        "session",
		"version":     3,
		"id":          "00000000-0000-4000-8000-0000000000" + spec.stem[len(spec.stem)-2:],
		"timestamp":   "2026-01-02T03:04:05.678Z",
		"cwd":         spec.workspace,
		"titleSource": "auto",
	}))
	content := []any{map[string]any{"type": "text", "text": "synthetic fixture message"}}
	if spec.blobRef != "" {
		content = append(content, map[string]any{"type": "image", "data": spec.blobRef, "mimeType": "image/webp"})
	}
	b.Write(jsonLine(f.t, map[string]any{
		"type":      "message",
		"id":        "f0000001",
		"timestamp": "2026-01-02T03:10:00.000Z",
		"message":   map[string]any{"role": "user", "content": content},
	}))
	if err := os.WriteFile(filepath.Join(dir, spec.stem+".jsonl"), []byte(b.String()), 0o600); err != nil {
		f.t.Fatal(err)
	}
	for rel, body := range spec.artifacts {
		path := filepath.Join(dir, spec.stem, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			f.t.Fatal(err)
		}
	}
}

// jsonLine renders one canonical JSONL record.
func jsonLine(t *testing.T, rec map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

// twoSessions populates the fixture with a rich session (artifact tree plus
// a resolvable blob) and a bare one.
func (f *fixture) twoSessions() {
	f.t.Helper()
	f.writeSession(sessionSpec{
		project:   "-synthetic-project",
		stem:      "2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001",
		title:     "Synthetic fixture session one",
		workspace: "/synthetic/workspace/one",
		blobRef:   f.blob("synthetic blob payload"),
		artifacts: map[string]string{
			"Helper.jsonl":      "{\"type\":\"message\",\"text\":\"synthetic subagent\"}\n",
			"nested/7.bash.log": "synthetic command output\n",
		},
	})
	f.writeSession(sessionSpec{
		project:   "-synthetic-other",
		stem:      "2026-01-03T06-07-08-900Z_00000000-0000-4000-8000-000000000002",
		title:     "Synthetic fixture session two",
		workspace: "/synthetic/workspace/two",
	})
}

// decode parses a --json result document, proving stdout carries exactly
// one machine-readable document and nothing else.
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

// storeSnapshot digests every object in the local archive so a test can
// prove a command left the store byte-identical.
func storeSnapshot(t *testing.T, root string) map[string]string {
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

func TestVersionReportsBuildIdentity(t *testing.T) {
	f := newFixture(t)

	stdout, stderr := f.ok("version")
	if !strings.HasPrefix(stdout, "babel ") {
		t.Fatalf("version stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("version wrote diagnostics: %q", stderr)
	}

	stdout, stderr = f.ok("version", "--json")
	if stderr != "" {
		t.Fatalf("version --json wrote diagnostics: %q", stderr)
	}
	id := decode[buildIdentity](t, stdout)
	if id.Version == "" {
		t.Fatal("version is empty")
	}
	if id.GoVersion == "" || id.Platform == "" {
		t.Fatalf("incomplete build identity: %+v", id)
	}
}

func TestBareBabelAnnouncesTUIAndSucceeds(t *testing.T) {
	f := newFixture(t)
	stdout, stderr, code := f.run()
	if code != exitOK {
		t.Fatalf("bare babel exited %d", code)
	}
	if !strings.Contains(stdout, "TUI is not implemented") || !strings.Contains(stdout, "Usage: babel") {
		t.Fatalf("bare babel stdout = %q", stdout)
	}
	if stderr != "" {
		t.Fatalf("bare babel wrote diagnostics: %q", stderr)
	}
}

func TestHelpGoesToStdout(t *testing.T) {
	f := newFixture(t)
	for _, args := range [][]string{
		{"-h"},
		{"archive", "-h"},
		{"sessions", "-h"},
		{"version", "-h"},
		{"archive", "push", "-h"},
		{"sessions", "prune", "-h"},
	} {
		stdout, stderr, code := f.run(args...)
		if code != exitOK {
			t.Fatalf("babel %s exited %d", strings.Join(args, " "), code)
		}
		if !strings.Contains(stdout, "Usage:") {
			t.Fatalf("babel %s printed no usage on stdout: %q", strings.Join(args, " "), stdout)
		}
		if stderr != "" {
			t.Fatalf("babel %s wrote help to stderr: %q", strings.Join(args, " "), stderr)
		}
	}
}

// TestPushThenReadCommands is the milestone's end-to-end contract: a real
// publication over a synthetic OMP tree, then every read command against
// the committed generation.
func TestPushThenReadCommands(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()

	stdout, stderr := f.ok(f.withStore("archive", "push", "--json", "--display-name", "Synthetic Workstation")...)
	push := decode[pushResult](t, stdout)
	if push.Generation != 1 || !push.Changed || !push.Bootstrap {
		t.Fatalf("unexpected push result: %+v", push)
	}
	if push.HostID != testHostID {
		t.Fatalf("push host = %q", push.HostID)
	}
	if push.Sessions != 2 || push.Revisions != 2 || push.Published != 2 || push.Deferred != 0 {
		t.Fatalf("unexpected push counts: %+v", push)
	}
	if push.CommitKey == "" || push.CommitDigest == "" {
		t.Fatalf("push reported no commit: %+v", push)
	}
	omp := coverageFor(t, push.Coverage, "omp")
	if omp.Scanned != 2 || omp.Published != 2 || !omp.Complete {
		t.Fatalf("unexpected omp coverage: %+v", omp)
	}
	if len(push.Coverage) != 3 {
		t.Fatalf("expected coverage for three adapters, got %d", len(push.Coverage))
	}
	// The coverage table is diagnostic context, so it must be on stderr and
	// must not pollute the machine-readable document.
	if !strings.Contains(stderr, "HARNESS") {
		t.Fatalf("coverage table missing from stderr:\n%s", stderr)
	}
	if strings.Contains(stdout, "HARNESS") {
		t.Fatalf("coverage table leaked into stdout:\n%s", stdout)
	}

	stdout, _ = f.ok(f.withStore("archive", "catalog", "--json")...)
	cat := decode[catalogResult](t, stdout)
	if len(cat.Hosts) != 1 || cat.Hosts[0].HostID != testHostID {
		t.Fatalf("unexpected catalog hosts: %+v", cat.Hosts)
	}
	if cat.Hosts[0].Generation != 1 || cat.Hosts[0].Sessions != 2 {
		t.Fatalf("unexpected catalog host: %+v", cat.Hosts[0])
	}
	if cat.Hosts[0].DisplayName != "Synthetic Workstation" {
		t.Fatalf("display name = %q", cat.Hosts[0].DisplayName)
	}
	if cat.Sessions != 2 || cat.Revisions != 2 {
		t.Fatalf("unexpected catalog totals: %+v", cat)
	}

	stdout, _ = f.ok(f.withStore("archive", "status", "--json")...)
	status := decode[statusResult](t, stdout)
	if len(status.Hosts) != 1 {
		t.Fatalf("unexpected status hosts: %+v", status.Hosts)
	}
	host := status.Hosts[0]
	if host.Generation != 1 || !host.BootstrapComplete {
		t.Fatalf("unexpected status host: %+v", host)
	}
	if !host.HintPresent || host.HintStale || host.HintGeneration != 1 {
		t.Fatalf("unexpected hint state: %+v", host)
	}
	if !host.JournalPresent || len(status.Journals) != 1 || status.Journals[0] != testHostID {
		t.Fatalf("push did not leave a local journal for its host: %+v", status)
	}
	if status.StateDir != f.stateDir {
		t.Fatalf("state dir = %q, want %q", status.StateDir, f.stateDir)
	}

	stdout, _ = f.ok(f.withStore("sessions", "list", "--json")...)
	list := decode[sessionsResult](t, stdout)
	if len(list.Sessions) != 2 {
		t.Fatalf("unexpected session count: %+v", list.Sessions)
	}
	first := list.Sessions[0]
	if first.Harness != "omp" || first.HostID != testHostID {
		t.Fatalf("unexpected session row: %+v", first)
	}
	if first.Title == nil || !strings.HasPrefix(*first.Title, "Synthetic fixture session") {
		t.Fatalf("unexpected title: %+v", first.Title)
	}
	if first.Workspace == nil || !strings.HasPrefix(*first.Workspace, "/synthetic/workspace/") {
		t.Fatalf("unexpected workspace: %+v", first.Workspace)
	}
	if first.Revisions != 1 {
		t.Fatalf("unexpected revision count: %d", first.Revisions)
	}

	// The harness filter selects a harness that published nothing.
	stdout, _ = f.ok(f.withStore("sessions", "list", "--json", "--harness", "codex")...)
	if got := decode[sessionsResult](t, stdout); len(got.Sessions) != 0 {
		t.Fatalf("codex filter returned %d sessions", len(got.Sessions))
	}

	// The rich session is the one carrying artifacts and a blob.
	rich := richSession(t, list.Sessions)
	stdout, _ = f.ok(f.withStore("sessions", "inspect", rich.SessionKey, "--json")...)
	insp := decode[inspectResult](t, stdout)
	if insp.SessionKey != rich.SessionKey || insp.RevisionKey != rich.NewestRevision {
		t.Fatalf("inspect resolved %+v, want session %s", insp, rich.SessionKey)
	}
	if insp.Encoding != "full" || insp.ChainDepth != 0 {
		t.Fatalf("unexpected encoding/chain: %+v", insp)
	}
	if insp.Artifacts != 2 || insp.Blobs != 1 {
		t.Fatalf("unexpected closure counts: %+v", insp)
	}
	if insp.ContentSize == 0 || !strings.HasPrefix(insp.ContentDigest, "sha256:") {
		t.Fatalf("unexpected content reference: %+v", insp)
	}
	if insp.Generation != 1 || insp.GenerationAdded != 1 || insp.SessionRevisions != 1 {
		t.Fatalf("unexpected generation detail: %+v", insp)
	}
	if !insp.ContinuationGrade {
		t.Fatal("omp snapshot with a resolved closure should be continuation grade")
	}

	// Human output is a table, and diagnostics never reach stdout.
	stdout, _ = f.ok(f.withStore("sessions", "list")...)
	if !strings.Contains(stdout, "SESSION") || !strings.Contains(stdout, "REVISIONS") {
		t.Fatalf("human listing = %q", stdout)
	}

	stdout, _ = f.ok(f.withStore("sessions", "fetch", rich.SessionKey, "--json")...)
	fetched := decode[fetchResult](t, stdout)
	if fetched.AlreadyFetched {
		t.Fatalf("first fetch reported an existing bundle: %+v", fetched)
	}
	if fetched.Files != 4 {
		// primary transcript, two artifacts, one blob
		t.Fatalf("unexpected file count: %+v", fetched)
	}
	wantPrefix := filepath.Join(f.dataDir, "bundles")
	if !strings.HasPrefix(fetched.Dir, wantPrefix) {
		t.Fatalf("bundle %q is not under %q", fetched.Dir, wantPrefix)
	}
	if got := filepath.Base(fetched.Dir); len(got) != digestPrefixLen {
		t.Fatalf("bundle leaf %q does not name a digest prefix", got)
	}
	if files, _, err := treeSize(fetched.Dir); err != nil || files != fetched.Files {
		t.Fatalf("materialized tree has %d files (err %v), reported %d", files, err, fetched.Files)
	}

	stdout, _ = f.ok(f.withStore("archive", "verify", "--json")...)
	rep := decode[verifyResult](t, stdout)
	if !rep.OK || rep.Deep {
		t.Fatalf("unexpected verify report: %+v", rep)
	}
	if len(rep.Hosts) != 1 || rep.Hosts[0].Revisions != 2 || rep.Hosts[0].Objects == 0 {
		t.Fatalf("unexpected verify hosts: %+v", rep.Hosts)
	}
	if len(rep.Hosts[0].Errors) != 0 {
		t.Fatalf("verify reported errors: %+v", rep.Hosts[0].Errors)
	}

	stdout, _ = f.ok(f.withStore("archive", "verify", "--deep", "--json")...)
	if deep := decode[verifyResult](t, stdout); !deep.OK || !deep.Deep {
		t.Fatalf("unexpected deep verify report: %+v", deep)
	}
}

// TestInspectRevisionKeyRoundTripsIntoFetch guards the operator workflow the
// renderer must not break: a revision key printed by the human detail view
// is complete and can be pasted straight into the next command.
func TestInspectRevisionKeyRoundTripsIntoFetch(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()
	f.ok(f.withStore("archive", "push")...)

	stdout, _ := f.ok(f.withStore("sessions", "list", "--json")...)
	session := decode[sessionsResult](t, stdout).Sessions[0]

	human, _ := f.ok(f.withStore("sessions", "inspect", session.SessionKey)...)
	if !strings.Contains(human, session.NewestRevision) {
		t.Fatalf("human inspect did not print the full revision key:\n%s", human)
	}

	stdout, _ = f.ok(f.withStore("sessions", "fetch", session.NewestRevision, "--json")...)
	if got := decode[fetchResult](t, stdout); got.RevisionKey != session.NewestRevision {
		t.Fatalf("fetch resolved %q, want %q", got.RevisionKey, session.NewestRevision)
	}
}

func TestPushIsIdempotentAndFetchIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()
	f.ok(f.withStore("archive", "push")...)

	stdout, _ := f.ok(f.withStore("archive", "push", "--json")...)
	second := decode[pushResult](t, stdout)
	if second.Changed || second.Generation != 1 {
		t.Fatalf("unchanged corpus committed a generation: %+v", second)
	}
	if second.Published != 0 || second.CarriedForward != 2 {
		t.Fatalf("unexpected second push counts: %+v", second)
	}

	stdout, _ = f.ok(f.withStore("sessions", "list", "--json")...)
	key := decode[sessionsResult](t, stdout).Sessions[0].SessionKey

	stdout, _ = f.ok(f.withStore("sessions", "fetch", key, "--json")...)
	first := decode[fetchResult](t, stdout)
	if first.AlreadyFetched {
		t.Fatal("first fetch reported an existing bundle")
	}

	stdout, stderr, code := f.run(f.withStore("sessions", "fetch", key, "--json")...)
	if code != exitOK {
		t.Fatalf("second fetch exited %d\nstderr:\n%s", code, stderr)
	}
	again := decode[fetchResult](t, stdout)
	if !again.AlreadyFetched {
		t.Fatalf("second fetch did not report idempotence: %+v", again)
	}
	if again.Dir != first.Dir || again.Files != first.Files || again.TotalSize != first.TotalSize {
		t.Fatalf("second fetch disagrees with the first: %+v vs %+v", again, first)
	}
	if !strings.Contains(stderr, "already fetched") {
		t.Fatalf("idempotent fetch printed no diagnostic: %q", stderr)
	}
}

func TestPruneLocalRemovesOnlyBundles(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()
	f.ok(f.withStore("archive", "push")...)

	stdout, _ := f.ok(f.withStore("sessions", "list", "--json")...)
	sessions := decode[sessionsResult](t, stdout).Sessions
	if len(sessions) != 2 {
		t.Fatalf("expected two sessions, got %d", len(sessions))
	}
	for _, s := range sessions {
		f.ok(f.withStore("sessions", "fetch", s.SessionKey)...)
	}

	bundles := filepath.Join(f.dataDir, "bundles")
	before := storeSnapshot(t, f.archiveRoot)
	if len(before) == 0 {
		t.Fatal("archive is empty")
	}
	sourcesBefore := storeSnapshot(t, f.sessionsDir)

	// Pruning exactly one session leaves the other bundle in place.
	stdout, _ = f.ok("sessions", "prune", "--local", "--yes", "--json", sessions[0].SessionKey)
	pruned := decode[pruneResult](t, stdout)
	if len(pruned.Removed) != 1 || pruned.Files == 0 || pruned.BytesFreed == 0 {
		t.Fatalf("unexpected prune result: %+v", pruned)
	}
	gone := filepath.Join(bundles, safeSessionDir(sessions[0].SessionKey))
	if pruned.Removed[0].Path != gone {
		t.Fatalf("pruned %q, want %q", pruned.Removed[0].Path, gone)
	}
	if _, err := os.Stat(gone); !os.IsNotExist(err) {
		t.Fatalf("bundle %s survived prune (err %v)", gone, err)
	}
	kept := filepath.Join(bundles, safeSessionDir(sessions[1].SessionKey))
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("prune removed an unselected bundle: %v", err)
	}

	// Pruning everything empties the bundle root and nothing else.
	stdout, _ = f.ok("sessions", "prune", "--local", "--all", "--yes", "--json")
	if rest := decode[pruneResult](t, stdout); len(rest.Removed) != 1 {
		t.Fatalf("unexpected --all prune result: %+v", rest)
	}
	entries, err := os.ReadDir(bundles)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("bundle root still holds %d entries", len(entries))
	}

	if after := storeSnapshot(t, f.archiveRoot); !equalSnapshots(before, after) {
		t.Fatal("local prune modified the object store")
	}
	if after := storeSnapshot(t, f.sessionsDir); !equalSnapshots(sourcesBefore, after) {
		t.Fatal("local prune modified local source sessions")
	}

	// A second --all prune is a no-op that still succeeds.
	stdout, _ = f.ok("sessions", "prune", "--local", "--all", "--yes", "--json")
	if empty := decode[pruneResult](t, stdout); len(empty.Removed) != 0 || empty.BytesFreed != 0 {
		t.Fatalf("second prune removed something: %+v", empty)
	}
}

func TestPruneRevisionSelectorTargetsOneBundle(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()
	f.ok(f.withStore("archive", "push")...)

	stdout, _ := f.ok(f.withStore("sessions", "list", "--json")...)
	session := decode[sessionsResult](t, stdout).Sessions[0]
	stdout, _ = f.ok(f.withStore("sessions", "fetch", session.NewestRevision, "--json")...)
	dir := decode[fetchResult](t, stdout).Dir

	stdout, _ = f.ok("sessions", "prune", "--local", "--yes", "--json", session.NewestRevision)
	pruned := decode[pruneResult](t, stdout)
	if len(pruned.Removed) != 1 || pruned.Removed[0].Path != dir {
		t.Fatalf("revision selector pruned %+v, want %s", pruned.Removed, dir)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("revision bundle survived prune (err %v)", err)
	}
	// The session directory itself remains, since only one revision was named.
	if _, err := os.Stat(filepath.Dir(dir)); err != nil {
		t.Fatalf("session bundle directory was removed: %v", err)
	}
}

func TestVerifyDeepDetectsTamperedObject(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()
	f.ok(f.withStore("archive", "push")...)

	stdout, _ := f.ok(f.withStore("sessions", "list", "--json")...)
	session := decode[sessionsResult](t, stdout).Sessions[0]
	stdout, _ = f.ok(f.withStore("sessions", "inspect", session.SessionKey, "--json")...)
	digest := archive.Digest(decode[inspectResult](t, stdout).ContentDigest)

	// Flip one bit of a payload object, preserving its size: only the deep
	// tier reads object bytes, so this is exactly the damage presence and
	// size checks cannot see.
	path := filepath.Join(f.archiveRoot, filepath.FromSlash(archive.CASKey(digest)))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 0x20
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := f.run(f.withStore("archive", "verify", "--json")...)
	if code != exitOK {
		t.Fatalf("default verify tier failed on a same-size flip (exit %d)\nstderr:\n%s", code, stderr)
	}
	if rep := decode[verifyResult](t, stdout); !rep.OK {
		t.Fatalf("default tier reported errors: %+v", rep.Hosts)
	}

	stdout, stderr, code = f.run(f.withStore("archive", "verify", "--deep", "--json")...)
	if code != exitFailure {
		t.Fatalf("deep verify exited %d, want %d\nstderr:\n%s", code, exitFailure, stderr)
	}
	rep := decode[verifyResult](t, stdout)
	if rep.OK || len(rep.Hosts) != 1 || len(rep.Hosts[0].Errors) == 0 {
		t.Fatalf("deep verify did not report the tampered object: %+v", rep)
	}
	if !strings.Contains(stderr, "error:") {
		t.Fatalf("deep verify printed no diagnostic:\n%s", stderr)
	}

	// A fetch of the damaged revision must fail rather than yield bytes.
	if _, _, code := f.run(f.withStore("sessions", "fetch", session.SessionKey)...); code != exitFailure {
		t.Fatalf("fetch of a tampered revision exited %d", code)
	}
}

func TestVerifyWarnsWithoutFailing(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()
	f.ok(f.withStore("archive", "push")...)

	// A latest hint that names no readable record is anomalous but never
	// authoritative: verify warns and still succeeds.
	hint := filepath.Join(f.archiveRoot, filepath.FromSlash(archive.LatestKey(testHostID)))
	if err := os.WriteFile(hint, []byte(`{"hint_schema":1,"host_id":"testhost","generation":9,"commit":{"digest":"sha256:`+strings.Repeat("0", 64)+`","size":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := f.run(f.withStore("archive", "verify", "--json")...)
	if code != exitOK {
		t.Fatalf("verify exited %d on a warning-only archive\nstderr:\n%s", code, stderr)
	}
	rep := decode[verifyResult](t, stdout)
	if !rep.OK || len(rep.Hosts[0].Warnings) == 0 {
		t.Fatalf("expected warnings and success: %+v", rep.Hosts)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Fatalf("warnings did not reach stderr:\n%s", stderr)
	}

	// The same anomaly shows up as hint staleness in status.
	stdout, _ = f.ok(f.withStore("archive", "status", "--json")...)
	if host := decode[statusResult](t, stdout).Hosts[0]; !host.HintStale {
		t.Fatalf("status did not report a stale hint: %+v", host)
	}
}

// TestHostileMetadataIsAlwaysEscaped is the malicious-fixture contract of
// SPEC.md §9: a title full of terminal control sequences must reach neither
// stdout nor stderr in raw form, in table or JSON output.
func TestHostileMetadataIsAlwaysEscaped(t *testing.T) {
	f := newFixture(t)
	const hostileTitle = "\x1b[31mred\x1b[0m \x1b]0;pwned\x07 \u202ereversed\u202c \u200bhidden\ufeff"
	const hostileWorkspace = "/synthetic/\x1b[2Jworkspace\u2066/evil"
	f.writeSession(sessionSpec{
		project:   "-synthetic-project",
		stem:      "2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001",
		title:     hostileTitle,
		workspace: hostileWorkspace,
	})
	f.ok(f.withStore("archive", "push")...)

	stdout, _ := f.ok(f.withStore("sessions", "list", "--json")...)
	list := decode[sessionsResult](t, stdout)
	if len(list.Sessions) != 1 {
		t.Fatalf("expected one session, got %d", len(list.Sessions))
	}
	key := list.Sessions[0].SessionKey
	if list.Sessions[0].Title == nil {
		t.Fatal("hostile title was dropped instead of escaped")
	}
	if !strings.Contains(*list.Sessions[0].Title, "\\u{1B}") || !strings.Contains(*list.Sessions[0].Title, "\\u{202E}") {
		t.Fatalf("title was not escaped: %q", *list.Sessions[0].Title)
	}

	for _, args := range [][]string{
		{"sessions", "list"},
		{"sessions", "list", "--json"},
		{"sessions", "inspect", key},
		{"sessions", "inspect", key, "--json"},
	} {
		stdout, stderr := f.ok(f.withStore(args...)...)
		for name, out := range map[string]string{"stdout": stdout, "stderr": stderr} {
			assertInert(t, strings.Join(args, " ")+" "+name, out)
		}
		if !strings.Contains(stdout, "\\u{1B}") {
			t.Fatalf("babel %s did not escape ESC on stdout:\n%s", strings.Join(args, " "), stdout)
		}
		if !strings.Contains(stdout, "\\u{202E}") {
			t.Fatalf("babel %s did not escape the bidi override on stdout:\n%s", strings.Join(args, " "), stdout)
		}
		if !strings.Contains(stdout, "\\u{200B}") || !strings.Contains(stdout, "\\u{FEFF}") {
			t.Fatalf("babel %s did not escape invisible controls on stdout:\n%s", strings.Join(args, " "), stdout)
		}
	}
}

// assertInert fails when rendered output carries a raw control, bidi, or
// invisible character.
func assertInert(t *testing.T, label, out string) {
	t.Helper()
	for i, r := range out {
		if r == '\n' || r == '\t' {
			continue // layout emitted by the printer, never by a value
		}
		if r < 0x20 || r == 0x7f || unsafeRune(r) {
			t.Fatalf("%s leaked raw %U at byte %d:\n%q", label, r, i, out)
		}
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name string
		args []string
	}{
		{"unknown command", []string{"bogus"}},
		{"unknown archive verb", []string{"archive", "bogus"}},
		{"unknown sessions verb", []string{"sessions", "bogus"}},
		{"archive without verb", []string{"archive"}},
		{"sessions without verb", []string{"sessions"}},
		{"missing backend", []string{"archive", "push"}},
		{"missing root", []string{"archive", "push", "--archive-backend", "local"}},
		{"unknown backend", []string{"sessions", "list", "--archive-backend", "sqlite", "--archive-root", f.archiveRoot}},
		{"unknown flag", []string{"version", "--nope"}},
		{"version with arguments", []string{"version", "extra"}},
		{"unknown harness", f.withStore("sessions", "list", "--harness", "gemini")},
		{"invalid host", f.withStore("archive", "catalog", "--host", "Not A Host")},
		{"inspect without selector", f.withStore("sessions", "inspect")},
		{"inspect with two selectors", f.withStore("sessions", "inspect", "a", "b")},
		{"fetch without selector", f.withStore("sessions", "fetch")},
		{"prune without --local", []string{"sessions", "prune", "--all", "--yes"}},
		{"prune without --yes", []string{"sessions", "prune", "--local", "--all"}},
		{"prune without selection", []string{"sessions", "prune", "--local", "--yes"}},
		{"prune with --all and selector", []string{"sessions", "prune", "--local", "--all", "--yes", "omp/testhost/x"}},
		{"prune invalid selector", []string{"sessions", "prune", "--local", "--yes", "not a key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := f.run(tc.args...)
			if code != exitUsage {
				t.Fatalf("babel %s exited %d, want %d\nstdout:\n%s\nstderr:\n%s",
					strings.Join(tc.args, " "), code, exitUsage, stdout, stderr)
			}
			if stdout != "" {
				t.Fatalf("rejected invocation wrote to stdout: %q", stdout)
			}
			if !strings.HasPrefix(stderr, "babel: ") || !strings.Contains(stderr, "Usage:") {
				t.Fatalf("unhelpful usage error: %q", stderr)
			}
		})
	}
}

func TestUnknownSelectorFails(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()
	f.ok(f.withStore("archive", "push")...)

	stdout, stderr, code := f.run(f.withStore("sessions", "inspect", "omp/testhost/absent/session")...)
	if code != exitFailure {
		t.Fatalf("unknown selector exited %d, want %d", code, exitFailure)
	}
	if stdout != "" {
		t.Fatalf("failed inspect wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "babel: ") {
		t.Fatalf("unexpected diagnostic: %q", stderr)
	}
}

func TestJournalPresenceIsReported(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()

	// Before any publication this machine holds no journal for the host.
	stdout, _ := f.ok(f.withStore("archive", "status", "--json")...)
	if before := decode[statusResult](t, stdout); len(before.Journals) != 0 {
		t.Fatalf("unpublished archive reported journals: %+v", before.Journals)
	}

	f.ok(f.withStore("archive", "push")...)

	// The journal is private local resumption state; status reports that it
	// exists without interpreting its contents.
	journal := filepath.Join(f.stateDir, "publish-journal-"+testHostID+".json")
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("push wrote no journal: %v", err)
	}
	stdout, _ = f.ok(f.withStore("archive", "status", "--json")...)
	status := decode[statusResult](t, stdout)
	if len(status.Journals) != 1 || status.Journals[0] != testHostID {
		t.Fatalf("unexpected journals: %+v", status.Journals)
	}
	if !status.Hosts[0].JournalPresent {
		t.Fatalf("host did not report its journal: %+v", status.Hosts[0])
	}

	stdout, _ = f.ok(f.withStore("archive", "status")...)
	if !strings.Contains(stdout, "JOURNAL") || !strings.Contains(stdout, "present") {
		t.Fatalf("human status hid the journal:\n%s", stdout)
	}
}

func TestEmptyArchiveReadsCleanly(t *testing.T) {
	f := newFixture(t)

	stdout, _ := f.ok(f.withStore("archive", "catalog", "--json")...)
	if cat := decode[catalogResult](t, stdout); len(cat.Hosts) != 0 || cat.Sessions != 0 {
		t.Fatalf("empty archive reported content: %+v", cat)
	}
	stdout, _ = f.ok(f.withStore("sessions", "list", "--json")...)
	if list := decode[sessionsResult](t, stdout); len(list.Sessions) != 0 {
		t.Fatalf("empty archive listed sessions: %+v", list)
	}
	stdout, _ = f.ok(f.withStore("archive", "verify", "--json")...)
	if rep := decode[verifyResult](t, stdout); !rep.OK {
		t.Fatalf("empty archive failed verification: %+v", rep)
	}
}

func TestHostIdentityAndHostFilter(t *testing.T) {
	f := newFixture(t)
	f.twoSessions()
	f.ok(f.withStore("archive", "push")...)
	// --host overrides $BABEL_HOST_ID, so the same corpus publishes a second
	// host identity into the same archive.
	f.ok(f.withStore("archive", "push", "--host", "secondhost")...)

	stdout, _ := f.ok(f.withStore("archive", "catalog", "--json")...)
	all := decode[catalogResult](t, stdout)
	if len(all.Hosts) != 2 || all.Sessions != 4 {
		t.Fatalf("expected two hosts and four sessions: %+v", all)
	}

	stdout, _ = f.ok(f.withStore("archive", "catalog", "--json", "--host", testHostID)...)
	one := decode[catalogResult](t, stdout)
	if len(one.Hosts) != 1 || one.Hosts[0].HostID != testHostID || one.Sessions != 2 {
		t.Fatalf("host filter returned %+v", one)
	}

	stdout, _ = f.ok(f.withStore("sessions", "list", "--json", "--host", "secondhost")...)
	list := decode[sessionsResult](t, stdout)
	if len(list.Sessions) != 2 {
		t.Fatalf("host filter listed %d sessions", len(list.Sessions))
	}
	for _, s := range list.Sessions {
		if s.HostID != "secondhost" {
			t.Fatalf("host filter leaked host %q", s.HostID)
		}
	}

	// A host that has never published is reported as contributing nothing
	// rather than as a failure.
	stdout, _ = f.ok(f.withStore("archive", "catalog", "--json", "--host", "absenthost")...)
	absent := decode[catalogResult](t, stdout)
	if len(absent.Hosts) != 1 || absent.Hosts[0].Generation != 0 || absent.Sessions != 0 {
		t.Fatalf("unknown host reported content: %+v", absent)
	}
}

func TestRcloneBackendFailsCleanly(t *testing.T) {
	f := newFixture(t)
	// The rclone backend is selectable; without a configured remote the
	// command must fail as a failure (exit 1), not as a usage error, and must
	// keep stdout free of anything but a result.
	stdout, stderr, code := f.run("archive", "catalog", "--archive-backend", "rclone",
		"--archive-root", "babel-absent-remote:archive/babel/v1")
	if code != exitFailure {
		t.Fatalf("rclone catalog exited %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitFailure, stdout, stderr)
	}
	if stdout != "" {
		t.Fatalf("failed catalog wrote to stdout: %q", stdout)
	}
	if !strings.HasPrefix(stderr, "babel: ") {
		t.Fatalf("unexpected diagnostic: %q", stderr)
	}
}

// coverageFor returns one adapter's coverage row.
func coverageFor(t *testing.T, rows []coverageRow, harness string) coverageRow {
	t.Helper()
	for _, r := range rows {
		if r.Harness == harness {
			return r
		}
	}
	t.Fatalf("no coverage for harness %q in %+v", harness, rows)
	return coverageRow{}
}

// richSession picks the fixture session that carries an artifact closure.
func richSession(t *testing.T, rows []sessionRow) sessionRow {
	t.Helper()
	for _, r := range rows {
		if strings.Contains(r.SessionKey, "synthetic-project") {
			return r
		}
	}
	t.Fatalf("no rich session in %+v", rows)
	return sessionRow{}
}

// equalSnapshots compares two store digests maps.
func equalSnapshots(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
