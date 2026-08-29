package restic

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// pinnedBinary is the restic build this package is developed against. Tests
// prefer it for reproducibility and fall back to PATH.
const pinnedBinary = "/nix/store/h43lp2dls4gyj6zfxssywk9d8s49qisn-restic-0.19.1/bin/restic"

const testPassword = "correct horse battery staple"

// resticBinary locates a restic executable, skipping the calling test when
// neither the pinned path nor PATH provides one.
func resticBinary(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat(pinnedBinary); err == nil {
		return pinnedBinary
	}
	if found, err := exec.LookPath("restic"); err == nil {
		return found
	}
	t.Skip("no restic binary: neither the pinned store path nor PATH provides one")
	return ""
}

// fixture is an initialized repository plus the scratch space around it.
type fixture struct {
	*Repo
	root    string
	repoDir string
	diag    *bytes.Buffer
}

// newFixture creates a password file and an initialized repository under
// t.TempDir, with diagnostics captured for assertions.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	bin := resticBinary(t)
	root := t.TempDir()

	pwFile := filepath.Join(root, "password")
	if err := os.WriteFile(pwFile, []byte(testPassword+"\n"), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	repoDir := filepath.Join(root, "repo")
	diag := &bytes.Buffer{}

	repo, err := Open(Config{
		Repository:   repoDir,
		PasswordFile: pwFile,
		Binary:       bin,
		CacheDir:     filepath.Join(root, "cache"),
		Diagnostics:  diag,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := repo.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return &fixture{Repo: repo, root: root, repoDir: repoDir, diag: diag}
}

// writeFile creates a file under the fixture root, making parents.
func (f *fixture) writeFile(t *testing.T, rel string, content []byte) string {
	t.Helper()
	path := filepath.Join(f.root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("writing %s: %v", rel, err)
	}
	return path
}

func TestOpenValidatesConfigWithoutTouchingRepository(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, "repo")
	pwFile := filepath.Join(root, "password")

	cases := []struct {
		name string
		cfg  Config
		want error
	}{
		{"no repository", Config{PasswordFile: pwFile}, ErrRepositoryRequired},
		{"blank repository", Config{Repository: "   ", PasswordFile: pwFile}, ErrRepositoryRequired},
		{"no password file", Config{Repository: repoDir}, ErrPasswordFileRequired},
		{"blank password file", Config{Repository: repoDir, PasswordFile: "\t"}, ErrPasswordFileRequired},
		{"complete", Config{Repository: repoDir, PasswordFile: pwFile}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := Open(tc.cfg)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Open error = %v, want %v", err, tc.want)
			}
			if tc.want == nil && repo == nil {
				t.Fatal("Open returned nil Repo without an error")
			}
			if tc.want != nil && repo != nil {
				t.Fatal("Open returned a Repo alongside an error")
			}
		})
	}

	// Open performs no I/O: nothing was created, not even the cache dir,
	// and a nonexistent repository is not reported.
	if _, err := os.Stat(repoDir); !os.IsNotExist(err) {
		t.Fatalf("Open touched the repository: stat err = %v", err)
	}
}

func TestCommandKeepsPasswordOffArgvAndEnvMinimal(t *testing.T) {
	root := t.TempDir()
	pwFile := filepath.Join(root, "password")
	if err := os.WriteFile(pwFile, []byte(testPassword), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	// A fake binary keeps this a pure command-construction test.
	repo, err := Open(Config{
		Repository:   filepath.Join(root, "repo"),
		PasswordFile: pwFile,
		Binary:       filepath.Join(root, "fake-restic"),
		CacheDir:     filepath.Join(root, "cache"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Setenv("BABEL_SENTINEL_ENV", "must-not-be-inherited")
	t.Setenv("RESTIC_PASSWORD", testPassword)

	cmd, err := repo.command(context.Background(), "backup", "--json", "--host", "h", "--tag", "t", "--", root)
	if err != nil {
		t.Fatalf("command: %v", err)
	}

	for _, arg := range cmd.Args {
		if strings.Contains(arg, testPassword) {
			t.Fatalf("password leaked into argv: %q", cmd.Args)
		}
	}

	allowed := map[string]bool{
		"RESTIC_REPOSITORY":    true,
		"RESTIC_PASSWORD_FILE": true,
		"RESTIC_CACHE_DIR":     true,
		"HOME":                 true,
		"PATH":                 true,
		"TMPDIR":               true,
	}
	seen := map[string]string{}
	for _, entry := range cmd.Env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			t.Fatalf("malformed env entry %q", entry)
		}
		if !allowed[key] {
			t.Fatalf("child env leaked %q", key)
		}
		if strings.Contains(value, testPassword) {
			t.Fatalf("password leaked into env var %s", key)
		}
		seen[key] = value
	}
	for _, required := range []string{"RESTIC_REPOSITORY", "RESTIC_PASSWORD_FILE", "RESTIC_CACHE_DIR"} {
		if _, ok := seen[required]; !ok {
			t.Fatalf("child env missing %s: %v", required, cmd.Env)
		}
	}
	if seen["RESTIC_PASSWORD_FILE"] != pwFile {
		t.Fatalf("RESTIC_PASSWORD_FILE = %q, want %q", seen["RESTIC_PASSWORD_FILE"], pwFile)
	}
	if cmd.Stdin != nil {
		t.Fatal("child has stdin: restic must never be able to prompt")
	}
	// prepare() created the cache dir it advertises.
	if info, err := os.Stat(seen["RESTIC_CACHE_DIR"]); err != nil || !info.IsDir() {
		t.Fatalf("cache dir %q not created: %v", seen["RESTIC_CACHE_DIR"], err)
	}
}

func TestDefaultCacheDirIsUsedWhenUnset(t *testing.T) {
	root := t.TempDir()
	pwFile := filepath.Join(root, "password")
	if err := os.WriteFile(pwFile, []byte(testPassword), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	// os.UserCacheDir honours XDG_CACHE_HOME, so the documented default is
	// observable without writing outside the test's temp dir.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "xdg"))

	repo, err := Open(Config{
		Repository:   filepath.Join(root, "repo"),
		PasswordFile: pwFile,
		Binary:       filepath.Join(root, "fake-restic"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cmd, err := repo.command(context.Background(), "snapshots")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	want := "RESTIC_CACHE_DIR=" + filepath.Join(root, "xdg", "babel-restic")
	if !slices.Contains(cmd.Env, want) {
		t.Fatalf("env %v missing %q", cmd.Env, want)
	}
}

func TestTailBufferKeepsBoundedSingleLineTail(t *testing.T) {
	tail := &tailBuffer{limit: 16}
	for range 10 {
		if _, err := tail.Write([]byte("line-abcdefghij\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	if len(tail.buf) > 16 {
		t.Fatalf("retained %d bytes, want <= 16", len(tail.buf))
	}
	got := tail.String()
	if !strings.HasPrefix(got, "...") {
		t.Fatalf("truncated tail %q lacks an ellipsis marker", got)
	}
	if strings.Contains(got, "\n") {
		t.Fatalf("tail %q is not a single line", got)
	}

	short := &tailBuffer{limit: 64}
	short.Write([]byte("Fatal: nope\nIs there a repository?\n"))
	if want := "Fatal: nope; Is there a repository?"; short.String() != want {
		t.Fatalf("tail = %q, want %q", short.String(), want)
	}
}

func TestInitIsIdempotentAndReportsWhetherItCreated(t *testing.T) {
	// newFixture already ran Init once against a fresh directory.
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.Init(ctx)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if created {
		// The distinction is what lets `archive init` tell an operator whether
		// they just created the deployment's archive or found it already there.
		t.Fatal("Init claimed to create a repository that already existed")
	}
	if _, err := os.Stat(filepath.Join(f.repoDir, "config")); err != nil {
		t.Fatalf("repository config missing: %v", err)
	}

	snaps, err := f.Snapshots(ctx)
	if err != nil {
		t.Fatalf("Snapshots on empty repo: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("empty repository listed %d snapshots", len(snaps))
	}
}

// Require is the check every repository-touching command makes instead of
// creating one, so its "missing" answer has to be distinguishable from every
// other failure. A locator typo that read as some other error would be reported
// as a broken repository; one that read as success would grow a second archive.
func TestRequireReportsAMissingRepositoryDistinctly(t *testing.T) {
	bin := resticBinary(t)
	root := t.TempDir()
	pwFile := filepath.Join(root, "password")
	if err := os.WriteFile(pwFile, []byte(testPassword), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	repo, err := Open(Config{
		Repository:   filepath.Join(root, "absent"),
		PasswordFile: pwFile,
		Binary:       bin,
		CacheDir:     filepath.Join(root, "cache"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	if err := repo.Require(ctx); !errors.Is(err, ErrRepoMissing) {
		t.Fatalf("Require on an absent repository = %v, want ErrRepoMissing", err)
	}
	// Nothing was created by asking.
	if _, err := os.Stat(filepath.Join(root, "absent", "config")); err == nil {
		t.Fatal("Require created a repository")
	}

	created, err := repo.Init(ctx)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !created {
		t.Fatal("Init did not report creating an absent repository")
	}
	if err := repo.Require(ctx); err != nil {
		t.Fatalf("Require after Init: %v", err)
	}
}

func TestBackupRecordsSnapshotWithHostAndTags(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	src := filepath.Join(f.root, "src")
	contents := map[string]string{
		"src/a.txt":       "alpha\n",
		"src/sub/b.txt":   "bravo bravo\n",
		"src/sub/c.jsonl": "{\"synthetic\":true}\n",
	}
	var totalBytes int64
	for rel, body := range contents {
		f.writeFile(t, rel, []byte(body))
		totalBytes += int64(len(body))
	}

	summary, err := f.Backup(ctx, []string{src}, "babel-test-host", []string{"session", "hourly"})
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}
	if len(summary.SnapshotID) != 64 {
		t.Fatalf("snapshot id %q is not a full hex id", summary.SnapshotID)
	}
	if summary.FilesNew != len(contents) {
		t.Errorf("FilesNew = %d, want %d", summary.FilesNew, len(contents))
	}
	if summary.FilesChanged != 0 || summary.FilesUnmodified != 0 {
		t.Errorf("first backup reported changed=%d unmodified=%d, want 0/0",
			summary.FilesChanged, summary.FilesUnmodified)
	}
	if summary.TotalFilesProcessed != len(contents) {
		t.Errorf("TotalFilesProcessed = %d, want %d", summary.TotalFilesProcessed, len(contents))
	}
	if summary.TotalBytesProcessed != totalBytes {
		t.Errorf("TotalBytesProcessed = %d, want %d", summary.TotalBytesProcessed, totalBytes)
	}
	if summary.DataAdded <= 0 {
		t.Errorf("DataAdded = %d, want > 0", summary.DataAdded)
	}
	if f.diag.Len() != 0 {
		t.Errorf("clean backup wrote diagnostics: %q", f.diag.String())
	}

	snaps, err := f.Snapshots(ctx)
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("listed %d snapshots, want 1", len(snaps))
	}
	got := snaps[0]
	if got.ID != summary.SnapshotID {
		t.Errorf("snapshot id = %q, want %q", got.ID, summary.SnapshotID)
	}
	if got.ShortID != summary.SnapshotID[:8] {
		t.Errorf("ShortID = %q, want %q", got.ShortID, summary.SnapshotID[:8])
	}
	if got.Host != "babel-test-host" {
		t.Errorf("Host = %q, want babel-test-host", got.Host)
	}
	if strings.Join(got.Tags, ",") != "session,hourly" {
		t.Errorf("Tags = %v, want [session hourly]", got.Tags)
	}
	if len(got.Paths) != 1 || got.Paths[0] != src {
		t.Errorf("Paths = %v, want [%s]", got.Paths, src)
	}
	if got.Time.IsZero() {
		t.Error("snapshot time is zero")
	}
}

// bigContent builds deterministic, incompressible bytes so deduplication
// and compression effects are measurable rather than lucky.
func bigContent(seed uint64, n int) []byte {
	rng := rand.New(rand.NewPCG(seed, 0x9E3779B97F4A7C15))
	buf := make([]byte, n)
	for off := 0; off < n; off += 8 {
		var word [8]byte
		binary.LittleEndian.PutUint64(word[:], rng.Uint64())
		copy(buf[off:], word[:])
	}
	return buf
}

func TestBackupDeduplicatesAppendAndDumpsEachGeneration(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Large enough that restic's chunker (max chunk 8 MiB) must split the
	// file, so appending can only invalidate the final chunk.
	const baseSize = 24 << 20
	const maxChunk = 8 << 20
	base := bigContent(1, baseSize)
	appended := bigContent(2, 8<<10)

	src := filepath.Join(f.root, "grow")
	logPath := f.writeFile(t, "grow/session.jsonl", base)

	first, err := f.Backup(ctx, []string{src}, "host", []string{"gen1"})
	if err != nil {
		t.Fatalf("first Backup: %v", err)
	}
	if first.TotalBytesProcessed != baseSize {
		t.Fatalf("first TotalBytesProcessed = %d, want %d", first.TotalBytesProcessed, baseSize)
	}

	fh, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("reopening for append: %v", err)
	}
	if _, err := fh.Write(appended); err != nil {
		t.Fatalf("appending: %v", err)
	}
	if err := fh.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	second, err := f.Backup(ctx, []string{src}, "host", []string{"gen2"})
	if err != nil {
		t.Fatalf("second Backup: %v", err)
	}
	if second.FilesChanged != 1 {
		t.Errorf("FilesChanged = %d, want 1", second.FilesChanged)
	}
	if second.FilesNew != 0 {
		t.Errorf("FilesNew = %d, want 0", second.FilesNew)
	}
	grown := int64(baseSize + len(appended))
	if second.TotalBytesProcessed != grown {
		t.Errorf("TotalBytesProcessed = %d, want %d", second.TotalBytesProcessed, grown)
	}
	// Deduplication proof: re-snapshotting a file that only grew must add
	// no more than the one chunk the append landed in, far below the file's
	// size, because every earlier chunk is reused.
	if second.DataAdded >= grown {
		t.Errorf("DataAdded = %d, want < file size %d (no deduplication happened)", second.DataAdded, grown)
	}
	if second.DataAdded > maxChunk+(1<<20) {
		t.Errorf("DataAdded = %d, want at most one re-chunked tail (~%d) of the %d byte file",
			second.DataAdded, maxChunk, grown)
	}
	if second.SnapshotID == first.SnapshotID {
		t.Error("second backup reused the first snapshot id")
	}

	// Each generation dumps byte-exact: the old snapshot still yields the
	// pre-append bytes, the new one the appended bytes.
	for _, tc := range []struct {
		name     string
		snapshot string
		want     []byte
	}{
		{"first generation", first.SnapshotID, base},
		{"second generation", second.SnapshotID, append(append([]byte{}, base...), appended...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := f.Dump(ctx, tc.snapshot, logPath, &out); err != nil {
				t.Fatalf("Dump: %v", err)
			}
			if !bytes.Equal(out.Bytes(), tc.want) {
				t.Fatalf("dumped %d bytes, want %d byte-exact match (equal prefix: %v)",
					out.Len(), len(tc.want), bytes.HasPrefix(tc.want, out.Bytes()))
			}
		})
	}
}

func TestRestoreMaterializesIncludedSubtree(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	src := filepath.Join(f.root, "src")
	wanted := map[string][]byte{
		"src/keep/one.txt":   []byte("kept one\n"),
		"src/keep/two.jsonl": []byte("{\"kept\":2}\n"),
	}
	for rel, body := range wanted {
		f.writeFile(t, rel, body)
	}
	f.writeFile(t, "src/skip/other.txt", []byte("not restored\n"))

	summary, err := f.Backup(ctx, []string{src}, "host", nil)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	target := filepath.Join(f.root, "restored")
	keep := filepath.Join(src, "keep")
	if err := f.Restore(ctx, summary.SnapshotID, []string{keep}, target); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	for rel, body := range wanted {
		got, err := os.ReadFile(filepath.Join(target, filepath.Join(f.root, rel)))
		if err != nil {
			t.Fatalf("reading restored %s: %v", rel, err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("restored %s = %q, want %q", rel, got, body)
		}
	}
	excluded := filepath.Join(target, filepath.Join(src, "skip", "other.txt"))
	if _, err := os.Stat(excluded); !os.IsNotExist(err) {
		t.Errorf("excluded path was restored (stat err = %v)", err)
	}
}

func TestCheckPassesThenDetectsPackCorruption(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	src := filepath.Join(f.root, "src")
	f.writeFile(t, "src/a.txt", bigContent(3, 128<<10))
	f.writeFile(t, "src/b.txt", []byte("second file\n"))
	if _, err := f.Backup(ctx, []string{src}, "host", nil); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	if err := f.Check(ctx, false); err != nil {
		t.Fatalf("Check(false) on a healthy repository: %v", err)
	}
	if err := f.Check(ctx, true); err != nil {
		t.Fatalf("Check(true) on a healthy repository: %v", err)
	}

	pack := largestPack(t, filepath.Join(f.repoDir, "data"))
	original, err := os.ReadFile(pack)
	if err != nil {
		t.Fatalf("reading pack: %v", err)
	}
	if err := os.Chmod(pack, 0o644); err != nil {
		t.Fatalf("chmod pack: %v", err)
	}
	corrupt := append([]byte{}, original...)
	mid := len(corrupt) / 2
	corrupt[mid] ^= 0xff
	if err := os.WriteFile(pack, corrupt, 0o644); err != nil {
		t.Fatalf("corrupting pack: %v", err)
	}

	err = f.Check(ctx, true)
	if err == nil {
		t.Fatal("Check(true) accepted a corrupted pack file")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("Check error %v is not an *ExitError", err)
	}
	if exitErr.Code == 0 {
		t.Errorf("ExitError.Code = 0 for a failed check")
	}
	if exitErr.Stderr == "" {
		t.Error("ExitError carries no stderr tail")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Error("error message leaked the repository password")
	}

	// Undoing the single-byte flip restores integrity, proving the failure
	// was that byte and not incidental repository damage.
	if err := os.WriteFile(pack, original, 0o644); err != nil {
		t.Fatalf("repairing pack: %v", err)
	}
	if err := f.Check(ctx, true); err != nil {
		t.Fatalf("Check(true) after repairing the byte: %v", err)
	}
}

// largestPack returns the biggest pack file below dir, the one most likely
// to hold file data rather than only tree metadata.
func largestPack(t *testing.T, dir string) string {
	t.Helper()
	var best string
	var bestSize int64
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
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
		t.Fatalf("walking packs: %v", err)
	}
	if best == "" {
		t.Fatalf("no pack files under %s", dir)
	}
	return best
}

func TestBackupUnreadableFileIsIncompleteButSnapshots(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits cannot make a file unreadable")
	}
	f := newFixture(t)
	ctx := context.Background()

	src := filepath.Join(f.root, "src")
	f.writeFile(t, "src/readable.txt", []byte("readable\n"))
	blocked := f.writeFile(t, "src/blocked.txt", []byte("unreadable\n"))
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o644) })

	summary, err := f.Backup(ctx, []string{src}, "host", []string{"partial"})
	if !errors.Is(err, ErrIncomplete) {
		t.Fatalf("Backup error = %v, want ErrIncomplete", err)
	}
	if summary == nil {
		t.Fatal("Backup returned no summary for an incomplete backup")
	}
	if summary.SnapshotID == "" {
		t.Error("incomplete backup reported no snapshot id")
	}
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("error %v does not carry an *ExitError", err)
	}
	if exitErr.Code != exitIncomplete {
		t.Errorf("exit code = %d, want %d", exitErr.Code, exitIncomplete)
	}

	diag := f.diag.String()
	if !strings.Contains(diag, blocked) {
		t.Errorf("diagnostics %q does not name the unreadable file %s", diag, blocked)
	}
	if strings.Contains(diag, "unreadable\n") {
		t.Error("diagnostics leaked file content")
	}

	// The partial snapshot really exists and holds the readable file.
	snaps, err := f.Snapshots(ctx)
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(snaps) != 1 || snaps[0].ID != summary.SnapshotID {
		t.Fatalf("snapshots = %+v, want the incomplete snapshot %s", snaps, summary.SnapshotID)
	}
	var out bytes.Buffer
	if err := f.Dump(ctx, summary.SnapshotID, filepath.Join(src, "readable.txt"), &out); err != nil {
		t.Fatalf("Dump from partial snapshot: %v", err)
	}
	if out.String() != "readable\n" {
		t.Errorf("dumped %q, want %q", out.String(), "readable\n")
	}
}

func TestOperationErrorsNameOperationAndCarryStderr(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	src := filepath.Join(f.root, "src")
	f.writeFile(t, "src/a.txt", []byte("alpha\n"))
	summary, err := f.Backup(ctx, []string{src}, "host", nil)
	if err != nil {
		t.Fatalf("Backup: %v", err)
	}

	var sink bytes.Buffer
	err = f.Dump(ctx, summary.SnapshotID, filepath.Join(src, "absent.txt"), &sink)
	if err == nil {
		t.Fatal("Dump of a missing path succeeded")
	}
	if !strings.Contains(err.Error(), "restic dump") {
		t.Errorf("error %q does not name the operation", err)
	}
	if !strings.Contains(err.Error(), "not found in snapshot") {
		t.Errorf("error %q lacks restic's stderr detail", err)
	}

	if err := f.Restore(ctx, "0000000000000000000000000000000000000000000000000000000000000000", nil, filepath.Join(f.root, "out")); err == nil {
		t.Fatal("Restore of an unknown snapshot succeeded")
	} else if !strings.Contains(err.Error(), "restic restore") {
		t.Errorf("error %q does not name the operation", err)
	}
}

func TestArgumentValidation(t *testing.T) {
	root := t.TempDir()
	pwFile := filepath.Join(root, "password")
	if err := os.WriteFile(pwFile, []byte(testPassword), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	repo, err := Open(Config{
		Repository:   filepath.Join(root, "repo"),
		PasswordFile: pwFile,
		Binary:       filepath.Join(root, "fake-restic"),
		CacheDir:     filepath.Join(root, "cache"),
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"backup without paths", func() error {
			_, err := repo.Backup(ctx, nil, "h", nil)
			return err
		}},
		{"dump without snapshot", func() error { return repo.Dump(ctx, "", "/p", &bytes.Buffer{}) }},
		{"dump without path", func() error { return repo.Dump(ctx, "abc", "", &bytes.Buffer{}) }},
		{"dump without writer", func() error { return repo.Dump(ctx, "abc", "/p", nil) }},
		{"restore without snapshot", func() error { return repo.Restore(ctx, "", nil, "/t") }},
		{"restore without target", func() error { return repo.Restore(ctx, "abc", nil, "") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("invalid arguments accepted")
			}
			if !strings.HasPrefix(err.Error(), "restic ") {
				t.Errorf("error %q does not name the operation", err)
			}
		})
	}
}

func TestOperationsHonourContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	src := filepath.Join(f.root, "src")
	f.writeFile(t, "src/a.txt", []byte("alpha\n"))

	if _, err := f.Backup(ctx, []string{src}, "host", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Backup with cancelled context: %v, want context.Canceled", err)
	}
	if _, err := f.Snapshots(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Snapshots with cancelled context: %v, want context.Canceled", err)
	}
	if err := f.Check(ctx, false); !errors.Is(err, context.Canceled) {
		t.Errorf("Check with cancelled context: %v, want context.Canceled", err)
	}
	if err := f.Dump(ctx, "abc", "/p", &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Errorf("Dump with cancelled context: %v, want context.Canceled", err)
	}
	if err := f.Restore(ctx, "abc", nil, filepath.Join(f.root, "out")); !errors.Is(err, context.Canceled) {
		t.Errorf("Restore with cancelled context: %v, want context.Canceled", err)
	}
}

func TestMissingBinaryIsReportedOnFirstUse(t *testing.T) {
	root := t.TempDir()
	pwFile := filepath.Join(root, "password")
	if err := os.WriteFile(pwFile, []byte(testPassword), 0o600); err != nil {
		t.Fatalf("writing password file: %v", err)
	}
	// An empty PATH makes the deferred lookup fail; Open still succeeds.
	t.Setenv("PATH", filepath.Join(root, "empty-bin"))
	repo, err := Open(Config{Repository: filepath.Join(root, "repo"), PasswordFile: pwFile})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err = repo.Init(context.Background())
	if err == nil {
		t.Fatal("Init succeeded without a restic binary")
	}
	if !strings.Contains(err.Error(), "locating binary") {
		t.Errorf("error %q does not explain the missing binary", err)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("error %v does not wrap exec.ErrNotFound", err)
	}
}

func TestIsMissingRepoRecognisesResticSignals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"exit code 10", &ExitError{Op: "cat config", Code: exitNoSuchRepo}, true},
		{"prose only", &ExitError{Op: "cat config", Code: 1, Stderr: "Fatal: nope; Is there a repository at the following location?"}, true},
		{"wrapped", fmt.Errorf("outer: %w", &ExitError{Code: exitNoSuchRepo}), true},
		{"wrong password", &ExitError{Op: "cat config", Code: 12, Stderr: "wrong password or no key found"}, false},
		{"unrelated", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isMissingRepo(tc.err); got != tc.want {
				t.Fatalf("isMissingRepo(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
