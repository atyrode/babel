package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testHostID is the host identity every fixture backs up under.
const testHostID = "testhost"

// resticPinnedPath is the restic build this project develops against. It is
// only consulted when the binary is absent from PATH; when neither is
// available the repository-dependent tests skip rather than fail, because
// they assert Babel's behavior against real restic, not a fake.
const resticPinnedPath = "/nix/store/h43lp2dls4gyj6zfxssywk9d8s49qisn-restic-0.19.1/bin/restic"

// testRepoPassword is a synthetic repository password. Nothing in this test
// suite is derived from a real session, credential, or transcript
// (SPEC.md §10).
const testRepoPassword = "synthetic-fixture-password\n"

// hostileTitle is the presentation-attack fixture: an SGR sequence, an OSC
// introducer, a raw C1 CSI, and a bidi override, all inside a value a
// session log is free to carry. No byte of it may reach stdout raw
// (SPEC.md §8, §9).
const hostileTitle = "\x1b[31mred\x1b[0m \x1b]0;retitled\x07 \x9b2J \u202egnitirw-thgir\u202c"

// fixture is one hermetic CLI environment: a synthetic OMP source tree
// under a private HOME, private XDG data and cache directories, and a
// private restic repository. Tests drive Run exactly as cmd/babel does, so
// what they exercise is the shipped wiring rather than a parallel harness.
//
// All fixture content is synthetic; no transcript ever comes from a real
// session (SPEC.md §10).
type fixture struct {
	t            *testing.T
	root         string
	home         string
	sessionsDir  string
	blobsDir     string
	repoDir      string
	passwordFile string
	dataDir      string
	cacheDir     string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	f := &fixture{
		t:            t,
		root:         root,
		home:         filepath.Join(root, "home"),
		repoDir:      filepath.Join(root, "repo"),
		passwordFile: filepath.Join(root, "password"),
		dataDir:      filepath.Join(root, "data", "babel"),
		cacheDir:     filepath.Join(root, "cache", "babel"),
	}
	f.sessionsDir = filepath.Join(f.home, ".omp", "agent", "sessions")
	f.blobsDir = filepath.Join(f.home, ".omp", "agent", "blobs")
	if err := os.MkdirAll(f.home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f.home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	// storage.json must be private to this fixture too. Without this, a test
	// that installs a configuration is visible to every later test in the
	// process whenever the developer or CI has XDG_CONFIG_HOME set, because
	// os.UserConfigDir prefers it over the fixture's HOME. That surfaced as
	// two unrelated tests observing a configuration they never wrote.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	// The environment defaults must not leak in from the developer's own
	// shell: repository selection is what several tests assert.
	t.Setenv("BABEL_RESTIC_REPO", "")
	t.Setenv("BABEL_RESTIC_PASSWORD_FILE", "")
	t.Setenv("BABEL_HOST_ID", testHostID)
	// Phase B's own defaults, for the same reason: an attributed decision
	// and a configured worker are exactly what several cases assert the
	// absence of, and either would otherwise arrive from the developer's
	// shell.
	t.Setenv("BABEL_OPERATOR", "")
	t.Setenv("BABEL_ANALYSIS_WORKER", "")
	// Codex and Claude must find nothing: this fixture synthesizes only an
	// OMP tree, and their default roots live under the private HOME, which
	// has none.
	t.Setenv("CODEX_HOME", filepath.Join(root, "absent-codex"))
	return f
}

// withRepo prepares the restic side of the fixture: it puts the restic
// binary on PATH, so the commands exercise the wrapper's default binary
// resolution, and writes the password file. The repository itself is
// created only by `bootstrapRepo` calling `archive init`, which a push
// deliberately does not perform (SPEC.md §6.1).
func (f *fixture) withRepo() *fixture {
	f.t.Helper()
	return f.withRepoPassword(testRepoPassword)
}

// withRepoPassword is withRepo with a caller-chosen password, so a test can
// make the credential a unique sentinel and then search the surfaces that
// must never carry it. The password must be selected before `bootstrapRepo`,
// because `archive init` is what creates the repository with it; overwriting
// the file afterwards would not re-key the repository.
func (f *fixture) withRepoPassword(password string) *fixture {
	f.t.Helper()
	f.t.Setenv("PATH", filepath.Dir(resticBinary(f.t))+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := os.WriteFile(f.passwordFile, []byte(password), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return f
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

// repoArgs is this fixture's explicit repository selection.
func (f *fixture) repoArgs() []string {
	return []string{"--repo", f.repoDir, "--password-file", f.passwordFile}
}

// with appends the repository selection to a command.
func (f *fixture) with(args ...string) []string {
	return append(args, f.repoArgs()...)
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

// mustExit drives one invocation that must exit with a specific code.
func (f *fixture) mustExit(want int, args ...string) (stdout, stderr string) {
	f.t.Helper()
	stdout, stderr, code := f.run(args...)
	if code != want {
		f.t.Fatalf("babel %s exited %d, want %d\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), code, want, stdout, stderr)
	}
	return stdout, stderr
}

// bootstrapRepo performs the one-time repository creation that `archive
// init` owns. Every test that pushes calls it first, because a push
// deliberately refuses to create the repository (SPEC.md §6.1).
func (f *fixture) bootstrapRepo() {
	f.t.Helper()
	f.ok(f.with("archive", "init")...)
}

// blob writes one synthetic blob into the OMP blob store and returns the
// "blob:sha256:<hex>" reference a session log embeds.
func (f *fixture) blob(content string) string {
	f.t.Helper()
	if err := os.MkdirAll(f.blobsDir, 0o700); err != nil {
		f.t.Fatal(err)
	}
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
	id        string
	title     string
	workspace string
	blobRef   string
	artifacts map[string]string
	// message overrides the synthetic user message in the primary log, so a
	// test can plant a unique string inside transcript content.
	message string
}

// writeSession materializes one synthetic session in the layout the OMP
// adapter documents: "<sessions>/<project>/<stem>.jsonl" for the primary
// log plus a sibling "<stem>/" directory for its artifact tree.
func (f *fixture) writeSession(spec sessionSpec) string {
	f.t.Helper()
	dir := filepath.Join(f.sessionsDir, spec.project)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	var b bytes.Buffer
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
		"id":          spec.id,
		"timestamp":   "2026-01-02T03:04:05.678Z",
		"cwd":         spec.workspace,
		"titleSource": "auto",
	}))
	text := spec.message
	if text == "" {
		text = "synthetic fixture message"
	}
	content := []any{map[string]any{"type": "text", "text": text}}
	if spec.blobRef != "" {
		content = append(content, map[string]any{"type": "image", "data": spec.blobRef, "mimeType": "image/webp"})
	}
	b.Write(jsonLine(f.t, map[string]any{
		"type":      "message",
		"id":        "f0000001",
		"timestamp": "2026-01-02T03:10:00.000Z",
		"message":   map[string]any{"role": "user", "content": content},
	}))
	primary := filepath.Join(dir, spec.stem+".jsonl")
	if err := os.WriteFile(primary, b.Bytes(), 0o600); err != nil {
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
	return primary
}

// richSessionStem names the fixture session that carries an artifact tree
// and a resolvable blob.
const richSessionStem = "2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-000000000001"

// bareSessionStem names the fixture session with no closure beyond its log.
const bareSessionStem = "2026-01-03T06-07-08-900Z_00000000-0000-4000-8000-000000000002"

// hostileSessionStem names the fixture session whose title is a
// presentation attack.
const hostileSessionStem = "2026-01-04T09-10-11-121Z_00000000-0000-4000-8000-000000000003"

// threeSessions populates the fixture with a rich session, a bare one, and
// one whose title is hostile. It returns the rich session's primary path.
func (f *fixture) threeSessions() string {
	f.t.Helper()
	primary := f.writeSession(sessionSpec{
		project:   "synthetic-project",
		stem:      richSessionStem,
		id:        "00000000-0000-4000-8000-000000000001",
		title:     "Synthetic fixture session one",
		workspace: "/synthetic/workspace/one",
		blobRef:   f.blob("synthetic blob payload"),
		artifacts: map[string]string{
			"Helper.jsonl":      "{\"type\":\"message\",\"text\":\"synthetic subagent\"}\n",
			"nested/7.bash.log": "synthetic command output\n",
		},
	})
	f.writeSession(sessionSpec{
		project:   "synthetic-other",
		stem:      bareSessionStem,
		id:        "00000000-0000-4000-8000-000000000002",
		title:     "Synthetic fixture session two",
		workspace: "/synthetic/workspace/two",
	})
	f.writeSession(sessionSpec{
		project:   "synthetic-hostile",
		stem:      hostileSessionStem,
		id:        "00000000-0000-4000-8000-000000000003",
		title:     hostileTitle,
		workspace: "/synthetic/workspace/" + hostileTitle,
	})
	return primary
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

// assertInert fails when a stream carries a byte or rune that could move a
// terminal's cursor, reorder its text, or hide characters.
func assertInert(t *testing.T, label, out string) {
	t.Helper()
	for _, bad := range []struct {
		name string
		text string
	}{
		{"ESC", "\x1b"},
		{"BEL", "\x07"},
		{"C1 CSI", "\u009b"},
		{"bidi override", "\u202e"},
		{"bidi pop", "\u202c"},
	} {
		if strings.Contains(out, bad.text) {
			t.Fatalf("%s leaked a raw %s:\n%q", label, bad.name, out)
		}
	}
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

func TestBareBabelPrintsStatusOverview(t *testing.T) {
	f := newFixture(t)
	stdout, stderr, code := f.run()
	if code != exitOK {
		t.Fatalf("bare babel exited %d", code)
	}
	for _, want := range []string{"babel ", "storage:", "babel web", "Usage: babel"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("bare babel stdout missing %q: %q", want, stdout)
		}
	}
	if stderr != "" {
		t.Fatalf("bare babel wrote diagnostics: %q", stderr)
	}

	for _, args := range [][]string{
		{"-h"},
		{"archive", "-h"},
		{"sessions", "-h"},
		{"version", "-h"},
		{"archive", "push", "-h"},
		{"archive", "verify", "-h"},
		{"sessions", "fetch", "-h"},
		{"sessions", "prune", "-h"},
		{"web", "-h"},
	} {
		stdout, stderr := f.ok(args...)
		if !strings.Contains(stdout, "Usage:") {
			t.Fatalf("babel %s printed no usage on stdout: %q", strings.Join(args, " "), stdout)
		}
		if stderr != "" {
			t.Fatalf("babel %s wrote help to stderr: %q", strings.Join(args, " "), stderr)
		}
	}
}

// TestPushThenStatusAndVerify is the milestone's end-to-end contract: a
// real backup of a synthetic source tree into a real restic repository,
// then every read command against it.
func TestPushThenStatusAndVerify(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	f.bootstrapRepo()

	stdout, stderr := f.ok(f.with("archive", "push", "--json")...)
	push := decode[pushResult](t, stdout)
	if push.SnapshotID == "" {
		t.Fatalf("push reported no snapshot: %+v", push)
	}
	if push.Host != testHostID {
		t.Fatalf("push host = %q, want %q", push.Host, testHostID)
	}
	if push.Incomplete {
		t.Fatalf("push reported an incomplete backup: %+v", push)
	}
	if push.FilesNew == 0 || push.FilesProcessed == 0 || push.BytesProcessed == 0 {
		t.Fatalf("push processed nothing: %+v", push)
	}
	// BackupRoots must add OMP's blob store to the session root, so a
	// snapshot can restore a continuation-grade closure (SPEC.md §3).
	wantRoots := []string{
		filepath.Join(f.home, ".omp", "agent", "blobs"),
		filepath.Join(f.home, ".omp", "agent", "sessions"),
	}
	if !slices.Equal(push.Roots, wantRoots) {
		t.Fatalf("push roots = %v, want %v", push.Roots, wantRoots)
	}
	if strings.Contains(stderr, "{") {
		t.Fatalf("push wrote result data to stderr: %q", stderr)
	}

	// A second push is a no-op backup: nothing is unreadable and the
	// repository already holds every file.
	stdout, _ = f.ok(f.with("archive", "push", "--json")...)
	second := decode[pushResult](t, stdout)
	if second.SnapshotID == "" || second.FilesUnmodified == 0 {
		t.Fatalf("second push did not deduplicate: %+v", second)
	}

	stdout, stderr = f.ok(f.with("archive", "status", "--json")...)
	status := decode[statusResult](t, stdout)
	if status.Snapshots != 2 {
		t.Fatalf("status snapshots = %d, want 2", status.Snapshots)
	}
	if len(status.Hosts) != 1 {
		t.Fatalf("status hosts = %+v, want exactly one", status.Hosts)
	}
	host := status.Hosts[0]
	if host.Host != testHostID || host.Snapshots != 2 {
		t.Fatalf("status host row = %+v", host)
	}
	if host.LatestID != second.SnapshotID {
		t.Fatalf("status latest id = %q, want the newest push %q", host.LatestID, second.SnapshotID)
	}
	if host.LatestTime == "" || host.LatestShortID == "" {
		t.Fatalf("status host row is missing latest state: %+v", host)
	}
	if len(host.Tags) != 1 || host.Tags[0] != babelTag {
		t.Fatalf("status tags = %v, want [%s]", host.Tags, babelTag)
	}
	if strings.Contains(stderr, "{") {
		t.Fatalf("status wrote result data to stderr: %q", stderr)
	}

	// The human table names the host and its snapshot without help.
	stdout, _ = f.ok(f.with("archive", "status")...)
	if !strings.Contains(stdout, testHostID) || !strings.Contains(stdout, host.LatestShortID) {
		t.Fatalf("status table = %q", stdout)
	}

	stdout, stderr = f.ok(f.with("archive", "verify", "--json")...)
	verify := decode[verifyResult](t, stdout)
	if !verify.OK || verify.Deep || verify.Error != "" {
		t.Fatalf("verify = %+v", verify)
	}
	if strings.Contains(stderr, "{") {
		t.Fatalf("verify wrote result data to stderr: %q", stderr)
	}

	// --restic-binary selects the executable without any help from PATH,
	// which is what makes Babel usable where restic is not installed
	// system-wide.
	t.Setenv("PATH", "")
	stdout, _ = f.ok(f.with("archive", "verify", "--restic-binary", resticBinary(t))...)
	if !strings.HasPrefix(stdout, "ok") {
		t.Fatalf("verify with an explicit binary = %q", stdout)
	}
}

func TestPushWarnsWhenNoSourceRootExists(t *testing.T) {
	f := newFixture(t).withRepo()

	stdout, stderr := f.ok(f.with("archive", "push", "--json")...)
	push := decode[pushResult](t, stdout)
	if push.SnapshotID != "" || len(push.Roots) != 0 {
		t.Fatalf("push invented work: %+v", push)
	}
	if !strings.Contains(stderr, "no source root exists") {
		t.Fatalf("push did not warn about the empty host: %q", stderr)
	}
	if _, err := os.Stat(f.repoDir); !os.IsNotExist(err) {
		t.Fatalf("push touched the repository with nothing to back up: %v", err)
	}
}

// TestPushReportsAnIncompleteBackup covers restic's partial-backup exit:
// the snapshot it produced is real and must still be summarized, the
// per-file diagnostics must reach stderr sanitized, and the invocation
// must fail so an operator's wrapper notices.
func TestPushReportsAnIncompleteBackup(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads unreadable files, so no partial backup can be provoked")
	}
	f := newFixture(t).withRepo()
	f.bootstrapRepo()
	f.threeSessions()
	unreadable := filepath.Join(f.sessionsDir, "synthetic-project", "unreadable.jsonl")
	if err := os.WriteFile(unreadable, []byte("{\"type\":\"title\",\"title\":\"synthetic\"}\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := f.mustExit(exitFailure, f.with("archive", "push", "--json")...)
	push := decode[pushResult](t, stdout)
	if !push.Incomplete {
		t.Fatalf("a partial backup was reported as complete: %+v", push)
	}
	if push.SnapshotID == "" {
		t.Fatalf("a partial backup reported no snapshot: %+v", push)
	}
	if !strings.Contains(stderr, "restic") || !strings.Contains(stderr, "unreadable.jsonl") {
		t.Fatalf("restic's per-file diagnostic never reached stderr: %q", stderr)
	}
	assertInert(t, "push stderr", stderr)

	// The snapshot it did commit is usable: status sees it and verify
	// passes over it.
	stdout, _ = f.ok(f.with("archive", "status", "--json")...)
	if status := decode[statusResult](t, stdout); status.Snapshots != 1 {
		t.Fatalf("status after a partial backup = %+v", status)
	}
	f.ok(f.with("archive", "verify")...)
}

func TestEnvironmentSelectsRepository(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	f.bootstrapRepo()
	t.Setenv("BABEL_RESTIC_REPO", f.repoDir)
	t.Setenv("BABEL_RESTIC_PASSWORD_FILE", f.passwordFile)

	stdout, _ := f.ok("archive", "push", "--json")
	if push := decode[pushResult](t, stdout); push.SnapshotID == "" {
		t.Fatalf("push through the environment reported no snapshot: %+v", push)
	}
	stdout, _ = f.ok("archive", "status", "--json")
	if status := decode[statusResult](t, stdout); status.Snapshots != 1 {
		t.Fatalf("status through the environment = %+v", status)
	}
}

func TestSessionsListDescribesEverySessionInertly(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()

	stdout, stderr := f.ok("sessions", "list", "--json")
	if stderr != "" {
		t.Fatalf("sessions list wrote diagnostics: %q", stderr)
	}
	assertInert(t, "sessions list --json stdout", stdout)
	list := decode[sessionsResult](t, stdout)
	if len(list.Sessions) != 3 {
		t.Fatalf("listed %d sessions, want 3: %+v", len(list.Sessions), list.Sessions)
	}
	var hostile *sessionRow
	for i, row := range list.Sessions {
		if row.Harness != "omp" {
			t.Fatalf("row %d harness = %q", i, row.Harness)
		}
		if row.Size == 0 {
			t.Fatalf("row %d reports no size: %+v", i, row)
		}
		if row.Modified == nil || *row.Modified == "" {
			t.Fatalf("row %d has no modification time: %+v", i, row)
		}
		if row.Selector != row.Harness+"/"+row.SourceID {
			t.Fatalf("row %d selector = %q", i, row.Selector)
		}
		if strings.Contains(row.SourceID, hostileSessionStem) {
			hostile = &list.Sessions[i]
		}
	}
	if hostile == nil {
		t.Fatalf("the hostile session is missing from the listing: %+v", list.Sessions)
	}
	if hostile.Title == nil || !strings.Contains(*hostile.Title, `\u{1B}`) {
		t.Fatalf("hostile title was not escaped: %+v", hostile.Title)
	}
	if hostile.Workspace == nil || !strings.Contains(*hostile.Workspace, `\u{202E}`) {
		t.Fatalf("hostile workspace was not escaped: %+v", hostile.Workspace)
	}

	// The human table is equally inert, and absent nullable fields are
	// displayed as absence rather than filled in (SPEC.md §3).
	stdout, _ = f.ok("sessions", "list")
	assertInert(t, "sessions list stdout", stdout)
	if !strings.Contains(stdout, "HARNESS") || !strings.Contains(stdout, richSessionStem) {
		t.Fatalf("sessions list table = %q", stdout)
	}

	// --harness restricts the scan; the harnesses with no local tree list
	// nothing at all rather than failing.
	stdout, _ = f.ok("sessions", "list", "--harness", "claude", "--json")
	if claude := decode[sessionsResult](t, stdout); len(claude.Sessions) != 0 {
		t.Fatalf("claude listed sessions from an omp tree: %+v", claude.Sessions)
	}
	stdout, _ = f.ok("sessions", "list", "--harness", "omp", "--json")
	if only := decode[sessionsResult](t, stdout); len(only.Sessions) != 3 {
		t.Fatalf("--harness omp listed %d sessions, want 3", len(only.Sessions))
	}

	// An explicit root override replaces the adapter defaults.
	stdout, _ = f.ok("sessions", "list", "--roots", filepath.Join(f.root, "absent"), "--json")
	if empty := decode[sessionsResult](t, stdout); len(empty.Sessions) != 0 {
		t.Fatalf("an empty root override still listed sessions: %+v", empty.Sessions)
	}
}

func TestSessionsInspectShowsTheWholeClosure(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()

	// A segment-aligned suffix is enough to address a session.
	stdout, stderr := f.ok("sessions", "inspect", richSessionStem, "--json")
	if stderr != "" {
		t.Fatalf("sessions inspect wrote diagnostics: %q", stderr)
	}
	assertInert(t, "sessions inspect --json stdout", stdout)
	got := decode[inspectResult](t, stdout)
	if got.Harness != "omp" || !strings.HasSuffix(got.SourceID, richSessionStem) {
		t.Fatalf("inspect resolved the wrong session: %+v", got)
	}
	if got.PrimarySize == 0 || got.PrimaryPath == "" || got.DescribedAt == "" {
		t.Fatalf("inspect reported no primary state: %+v", got)
	}
	if len(got.Artifacts) != 2 {
		t.Fatalf("inspect artifacts = %+v, want 2", got.Artifacts)
	}
	if len(got.Blobs) != 1 {
		t.Fatalf("inspect blobs = %+v, want 1", got.Blobs)
	}
	if !strings.HasPrefix(got.Blobs[0].Digest, "sha256:") {
		t.Fatalf("blob digest is not canonical: %+v", got.Blobs[0])
	}
	if len(got.UnresolvedBlobRefs) != 0 || !got.ContinuationGrade {
		t.Fatalf("a complete closure was not graded continuable: %+v", got)
	}
	if len(got.Completeness) == 0 {
		t.Fatal("inspect reported no reasons for its absent fields")
	}
	for _, reason := range got.Completeness {
		if reason.Field == "" || reason.Reason == "" {
			t.Fatalf("empty completeness reason: %+v", reason)
		}
	}
	if got.Lifecycle != nil {
		t.Fatalf("lifecycle was synthesized: %+v", got.Lifecycle)
	}
	if len(got.AdapterMetadata) == 0 || got.AdapterMetadataSchema == 0 {
		t.Fatalf("inspect dropped the adapter metadata: %+v", got)
	}

	// The selector reported by list feeds straight back into inspect, and
	// the human rendering stays inert for the hostile session.
	stdout, _ = f.ok("sessions", "list", "--json")
	for _, row := range decode[sessionsResult](t, stdout).Sessions {
		if !strings.Contains(row.SourceID, hostileSessionStem) {
			continue
		}
		detail, _ := f.ok("sessions", "inspect", row.Selector)
		assertInert(t, "sessions inspect stdout", detail)
		if !strings.Contains(detail, `\u{1B}`) {
			t.Fatalf("hostile title was not escaped in the detail view: %q", detail)
		}
	}
}

func TestFetchMaterializesTheSessionAndIsIdempotent(t *testing.T) {
	f := newFixture(t).withRepo()
	primary := f.threeSessions()
	f.bootstrapRepo()
	f.ok(f.with("archive", "push")...)

	stdout, stderr := f.ok(f.with("sessions", "fetch", richSessionStem, "--json")...)
	fetch := decode[fetchResult](t, stdout)
	if fetch.AlreadyPresent {
		t.Fatalf("the first fetch claimed the target already existed: %+v", fetch)
	}
	if fetch.Files == 0 || fetch.Bytes == 0 {
		t.Fatalf("fetch restored nothing: %+v", fetch)
	}
	if fetch.SnapshotID == "" || fetch.SnapshotShortID == "" || fetch.SnapshotTime == "" {
		t.Fatalf("fetch did not report its snapshot: %+v", fetch)
	}
	if !strings.HasPrefix(fetch.Target, f.dataDir) {
		t.Fatalf("fetch target %q is outside the data directory %q", fetch.Target, f.dataDir)
	}
	if _, err := os.Stat(fetch.Target + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("fetch left its staging directory behind: %v", err)
	}

	// restic recreates absolute source paths beneath the target, so the
	// restored primary log must be byte-identical to the source it was
	// backed up from.
	restored := filepath.Join(fetch.Target, primary)
	want, err := os.ReadFile(primary)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(restored)
	if err != nil {
		t.Fatalf("restored primary log is missing: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("restored primary log differs from the source: %d vs %d bytes", len(got), len(want))
	}
	// The sibling artifact tree came back with it.
	artifact := filepath.Join(fetch.Target, strings.TrimSuffix(primary, ".jsonl"), "nested", "7.bash.log")
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("restored artifact is missing: %v", err)
	}

	// Fetch accounts for its whole requested closure: every included path
	// is either materialized under the target or reported as absent from
	// the snapshot, and the primary log is never merely assumed present.
	if len(fetch.Included) == 0 {
		t.Fatal("fetch reported no closure paths")
	}
	for _, path := range fetch.Included {
		absent := containsString(fetch.Missing, path)
		_, err := os.Lstat(filepath.Join(fetch.Target, path))
		switch {
		case err == nil && absent:
			t.Fatalf("%s was restored but reported missing", path)
		case err != nil && !absent:
			t.Fatalf("%s was neither restored nor reported missing", path)
		}
	}
	if containsString(fetch.Missing, primary) {
		t.Fatalf("the primary log was reported missing from the snapshot: %+v", fetch.Missing)
	}

	before := treeDigest(t, fetch.Target)
	stdout, stderr = f.ok(f.with("sessions", "fetch", richSessionStem, "--json")...)
	again := decode[fetchResult](t, stdout)
	if !again.AlreadyPresent {
		t.Fatalf("the second fetch re-restored the session: %+v", again)
	}
	if again.Target != fetch.Target || again.Files != fetch.Files {
		t.Fatalf("the second fetch disagreed with the first: %+v vs %+v", again, fetch)
	}
	if !strings.Contains(stderr, "already materialized") {
		t.Fatalf("the second fetch did not note the existing target: %q", stderr)
	}
	if diff := diffTrees(before, treeDigest(t, fetch.Target)); diff != "" {
		t.Fatalf("the second fetch rewrote the target: %s", diff)
	}

	// An explicit snapshot id resolves the same way "latest" did.
	stdout, _ = f.ok(f.with("sessions", "fetch", richSessionStem, "--snapshot", fetch.SnapshotShortID, "--json")...)
	if pinned := decode[fetchResult](t, stdout); pinned.Target != fetch.Target {
		t.Fatalf("--snapshot resolved a different target: %q", pinned.Target)
	}
	if _, _, code := f.run(f.with("sessions", "fetch", richSessionStem, "--snapshot", "deadbeef")...); code != exitFailure {
		t.Fatalf("an unknown snapshot exited %d, want %d", code, exitFailure)
	}
}

func TestPruneLocalRemovesOnlyFetchedCopies(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	f.bootstrapRepo()
	f.ok(f.with("archive", "push")...)

	stdout, _ := f.ok(f.with("sessions", "fetch", richSessionStem, "--json")...)
	rich := decode[fetchResult](t, stdout)
	stdout, _ = f.ok(f.with("sessions", "fetch", bareSessionStem, "--json")...)
	bare := decode[fetchResult](t, stdout)

	repoBefore := treeDigest(t, f.repoDir)
	sourceBefore := treeDigest(t, f.sessionsDir)

	stdout, stderr := f.ok("sessions", "prune", "--local", "--yes", rich.Selector, "--json")
	prune := decode[pruneResult](t, stdout)
	if len(prune.Removed) != 1 {
		t.Fatalf("prune removed %+v, want exactly the fetched rich session", prune.Removed)
	}
	if prune.Files == 0 || prune.Bytes == 0 {
		t.Fatalf("prune reported nothing removed: %+v", prune)
	}
	if strings.Contains(stderr, "{") {
		t.Fatalf("prune wrote result data to stderr: %q", stderr)
	}
	if _, err := os.Stat(rich.Target); !os.IsNotExist(err) {
		t.Fatalf("prune left the fetched rich session behind: %v", err)
	}
	if _, err := os.Stat(bare.Target); err != nil {
		t.Fatalf("prune removed an unselected session: %v", err)
	}
	if diff := diffTrees(repoBefore, treeDigest(t, f.repoDir)); diff != "" {
		t.Fatalf("prune touched the repository: %s", diff)
	}
	if diff := diffTrees(sourceBefore, treeDigest(t, f.sessionsDir)); diff != "" {
		t.Fatalf("prune touched the harness sources: %s", diff)
	}

	// Pruning the same selector twice is a note, not a failure.
	stdout, stderr = f.ok("sessions", "prune", "--local", "--yes", rich.Selector, "--json")
	if repeat := decode[pruneResult](t, stdout); len(repeat.Removed) != 0 {
		t.Fatalf("the second prune removed something: %+v", repeat.Removed)
	}
	if !strings.Contains(stderr, "no fetched directory matches") {
		t.Fatalf("the second prune did not explain itself: %q", stderr)
	}

	// --all clears what is left, and still cannot reach the repository.
	stdout, _ = f.ok("sessions", "prune", "--local", "--all", "--yes", "--json")
	if all := decode[pruneResult](t, stdout); len(all.Removed) != 1 {
		t.Fatalf("--all removed %+v, want the remaining fetch", all.Removed)
	}
	if _, err := os.Stat(bare.Target); !os.IsNotExist(err) {
		t.Fatalf("--all left a fetched session behind: %v", err)
	}
	if diff := diffTrees(repoBefore, treeDigest(t, f.repoDir)); diff != "" {
		t.Fatalf("--all touched the repository: %s", diff)
	}
}

func TestVerifyDeepDetectsATamperedPack(t *testing.T) {
	f := newFixture(t).withRepo()
	f.threeSessions()
	f.bootstrapRepo()
	f.ok(f.with("archive", "push")...)
	f.ok(f.with("archive", "verify")...)

	flipOneByte(t, largestPack(t, filepath.Join(f.repoDir, "data")))
	// restic serves metadata from its cache; a deep check must read the
	// repository itself, so the cache is dropped first.
	if err := os.RemoveAll(f.cacheDir); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := f.mustExit(exitFailure, f.with("archive", "verify", "--deep", "--json")...)
	verify := decode[verifyResult](t, stdout)
	if verify.OK || !verify.Deep || verify.Error == "" {
		t.Fatalf("deep verify passed over a tampered pack: %+v", verify)
	}
	if !strings.Contains(stderr, "verify repository") {
		t.Fatalf("deep verify gave no detail on stderr: %q", stderr)
	}
	assertInert(t, "verify stderr", stderr)
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

// flipOneByte corrupts exactly one byte of a file, leaving its length
// untouched so only a content check can notice.
func flipOneByte(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatalf("%s is empty", path)
	}
	raw[len(raw)/2] ^= 0xff
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	f := newFixture(t)
	f.threeSessions()
	// Two sessions sharing a stem across projects make a bare-stem
	// selector ambiguous.
	f.writeSession(sessionSpec{
		project:   "synthetic-twin",
		stem:      richSessionStem,
		id:        "00000000-0000-4000-8000-000000000004",
		title:     "Synthetic fixture twin",
		workspace: "/synthetic/workspace/twin",
	})

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"frobnicate"}, "unknown command"},
		{"unknown archive verb", []string{"archive", "wibble"}, "unknown archive subcommand"},
		{"unknown sessions verb", []string{"sessions", "wibble"}, "unknown sessions subcommand"},
		{"missing repository", []string{"archive", "status"}, "no restic repository selected"},
		{"missing password file", []string{"archive", "status", "--repo", f.repoDir}, "no repository password file selected"},
		{"missing repository on push", []string{"archive", "push"}, "no restic repository selected"},
		{"unknown flag", []string{"archive", "status", "--nope"}, "flag provided but not defined"},
		{"unknown harness", []string{"sessions", "list", "--harness", "emacs"}, "unknown --harness"},
		{"no selector", []string{"sessions", "inspect"}, "requires a SELECTOR"},
		{"ambiguous selector", []string{"sessions", "inspect", richSessionStem}, "is ambiguous"},
		{"prune without local", []string{"sessions", "prune", "--yes", "--all"}, "requires --local"},
		{"prune without yes", []string{"sessions", "prune", "--local", "--all"}, "without --yes"},
		{"prune without a target", []string{"sessions", "prune", "--local", "--yes"}, "needs --all or at least one SELECTOR"},
		{"prune with both", []string{"sessions", "prune", "--local", "--yes", "--all", "omp/x"}, "--all takes no selectors"},
		{"push positional", []string{"archive", "push", "extra"}, "takes no positional arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr := f.mustExit(exitUsage, tc.args...)
			if stdout != "" {
				t.Fatalf("a rejected invocation wrote to stdout: %q", stdout)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr = %q, want it to mention %q", stderr, tc.want)
			}
			if !strings.Contains(stderr, "Usage:") {
				t.Fatalf("a rejected invocation printed no usage: %q", stderr)
			}
		})
	}

	// An ambiguous selector names its candidates so the operator can
	// qualify it.
	_, stderr := f.mustExit(exitUsage, "sessions", "inspect", richSessionStem)
	if !strings.Contains(stderr, "synthetic-project/"+richSessionStem) ||
		!strings.Contains(stderr, "synthetic-twin/"+richSessionStem) {
		t.Fatalf("ambiguity report listed no candidates: %q", stderr)
	}

	// A selector matching nothing is a failure, not a rejected invocation:
	// the syntax was fine, the session simply is not here.
	f.mustExit(exitFailure, "sessions", "inspect", "omp/absent/session")

	// A hostile selector is echoed inertly.
	_, stderr = f.mustExit(exitFailure, "sessions", "inspect", "omp/"+hostileTitle)
	assertInert(t, "selector error stderr", stderr)
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

// containsString reports whether values holds want.
func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
