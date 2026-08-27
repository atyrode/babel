// Package e2e_test drives Babel's Phase A local core end to end
// (SPEC.md §6.1–6.2, §8): the three real source adapters, the resumable
// publication pipeline, the read side (catalog, resolve, fetch, tiered
// verify), and the documented direct-recovery layout
// (docs/contracts/archive-v1.md §9).
//
// One scenario runs identically against both shipped object-store
// backends, so nothing in the archive contract may depend on transport:
// the directory-backed local store always, and the rclone-backed store
// over a plain temporary-directory remote whenever the rclone binary is
// installed.
//
// Everything the suite reads is synthetic content built in a temporary
// directory that mirrors each harness's documented on-disk layout. No real
// transcript, workspace, or host is named, and no assertion depends on the
// operator's own environment: the rclone store is pointed at an absent
// configuration file so the operator's remotes can never influence a run.
package e2e_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/adapter/claude"
	"github.com/atyrode/babel/internal/adapter/codex"
	"github.com/atyrode/babel/internal/adapter/omp"
	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/objectstore"
	"github.com/atyrode/babel/internal/objectstore/local"
	"github.com/atyrode/babel/internal/objectstore/rclonestore"
	"github.com/atyrode/babel/internal/publish"
)

// This host's archive identity. It satisfies archive.ValidName, which is
// what the CLI's --host / $BABEL_HOST_ID / hostname resolution must also
// produce (SPEC.md §8).
const (
	hostID          = "e2e-host"
	hostDisplayName = "Babel end-to-end host"
	babelVersion    = "e2e-test"
)

// Synthetic on-disk names. They imitate the real layouts of the three
// harnesses without naming any real project, workspace, or session.
const (
	ompProjectAlpha = "-synthetic-e2e-alpha"
	ompProjectBeta  = "-synthetic-e2e-beta"
	ompStemAlpha    = "2026-01-02T03-04-05-678Z_00000000-0000-4000-8000-0000000000a1"
	ompStemBeta     = "2026-01-03T06-07-08-900Z_00000000-0000-4000-8000-0000000000b2"

	codexRolloutRel   = "sessions/2026/01/02/rollout-2026-01-02T03-04-05-aaaaaaaa-0000-4000-8000-0000000000c3.jsonl"
	codexAttachmentID = "aaaaaaaa-0000-4000-8000-0000000000d4"
	codexAttachment   = "synthetic-attachment-0001.png"

	claudeProject  = "-synthetic-e2e-workspace"
	claudeSession  = "eeeeeeee-0000-4000-8000-0000000000e5"
	claudeSubagent = "agent-esynthetic0001.jsonl"
)

// Canonical session keys the corpus must produce. They are spelled out
// here because the whole read side is keyed by them, and a silent identity
// change would be a contract break rather than a test detail (SPEC.md §3).
var (
	sessionOMPAlpha = "omp/" + hostID + "/" + ompProjectAlpha + "/" + ompStemAlpha
	sessionOMPBeta  = "omp/" + hostID + "/" + ompProjectBeta + "/" + ompStemBeta
	sessionCodex    = "codex/" + hostID + "/" + codexRolloutRel
	sessionCodexSt  = "codex/" + hostID + "/" + codex.StateSourceID
	sessionClaude   = "claude/" + hostID + "/" + claudeProject + "/" + claudeSession
)

const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// errInterrupted stands in for the process dying mid-upload.
var errInterrupted = errors.New("e2e: simulated interruption")

// TestPhaseALocalCore runs the whole Phase A loop against every available
// backend. The rclone subtest is skipped only when the binary is genuinely
// absent; a skipped run would otherwise hide a transport-dependent
// regression.
func TestPhaseALocalCore(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "archive")
		st, err := local.New(root)
		if err != nil {
			t.Fatalf("open local store: %v", err)
		}
		runScenario(t, backend{name: "local", store: st, dir: st.Root()})
	})

	t.Run("rclone", func(t *testing.T) {
		if _, err := exec.LookPath("rclone"); err != nil {
			t.Skip("rclone binary not on PATH")
		}
		// A plain-path remote exercises the real binary with no network,
		// credential, or configuration dependency; the absent config file
		// keeps the operator's own remotes out of the test.
		t.Setenv("RCLONE_CONFIG", filepath.Join(t.TempDir(), "absent.conf"))
		root := filepath.Join(t.TempDir(), "archive")
		if err := os.MkdirAll(root, dirPerm); err != nil {
			t.Fatalf("create rclone remote: %v", err)
		}
		runScenario(t, backend{name: "rclone", store: rclonestore.New(root), dir: root})
	})
}

// backend is one object-store implementation under test. dir is the
// filesystem directory the archive actually lands in, which both shipped
// backends expose and which the direct-recovery step reads with nothing
// but os and sha256.
type backend struct {
	name  string
	store objectstore.Store
	dir   string
}

// runScenario executes the seven numbered steps of the Phase A loop in
// order against one backend. The steps share state deliberately: each one
// asserts against the archive the previous ones actually produced.
func runScenario(t *testing.T, be backend) {
	w := newWorld(t, be)

	// (1) Bootstrap publication of the whole synthetic corpus.
	gen1 := w.stepBootstrapPush()

	// (2) The read side sees exactly what was published.
	cat1 := w.stepLoadCatalog()

	// (3) A bare selector resolves to the newest revision and fetches
	//     byte-exactly, with its declared closure.
	origAlpha, revAlpha1 := w.stepResolveAndFetch(cat1)

	// (4) An appended source publishes an append-delta revision; both the
	//     old and the new revision remain byte-recoverable.
	appendedAlpha := w.stepAppendAndRepublish(gen1, revAlpha1, origAlpha)

	// (5) Tiered verification: stat-only cannot see a same-size bit flip,
	//     deep can, and detection is byte-exact.
	w.stepTieredVerify()

	// (6) An interrupted push leaves the archive consistent and converges
	//     to exactly one new verified generation.
	w.stepInterruptedPushConverges(appendedAlpha)

	// (7) The frozen recovery promise, executed: one session recovered
	//     from the store layout alone.
	w.stepDirectRecovery()
}

// world is one isolated end-to-end environment: a synthetic source corpus,
// an archive backend, and the XDG-shaped private directories Babel uses
// for journal, staging, and fetched bundles (SPEC.md §9).
type world struct {
	t   *testing.T
	be  backend
	src *sources

	stateDir string // $XDG_STATE_HOME/babel: publication journal
	cacheDir string // $XDG_CACHE_HOME/babel: staging
	dataDir  string // $XDG_DATA_HOME/babel: fetched bundles

	// fetches counts materializations and fetched records the bundle
	// directories already occupied, because a fetched bundle is immutable.
	fetches int
	fetched map[string]bool
}

func newWorld(t *testing.T, be backend) *world {
	t.Helper()
	base := t.TempDir()
	return &world{
		t:        t,
		be:       be,
		src:      newSources(t),
		stateDir: mkdirIn(t, base, "state"),
		cacheDir: mkdirIn(t, base, "cache"),
		dataDir:  mkdirIn(t, base, "data"),
		fetched:  map[string]bool{},
	}
}

// publisher builds a Publisher over one store. A Publisher holds no
// mutable publication state, so every push may use a fresh one; the state
// directory is shared so the journal spans pushes.
func (w *world) publisher(store objectstore.Store) *publish.Publisher {
	w.t.Helper()
	pub, err := publish.New(publish.Config{
		Store:           store,
		Adapters:        []adapter.Adapter{omp.New(), codex.New(), claude.New()},
		HostID:          hostID,
		HostDisplayName: hostDisplayName,
		Roots: map[string][]string{
			"omp":    {w.src.ompSessions},
			"codex":  {w.src.codexRoot},
			"claude": {w.src.claudeRoot},
		},
		StateDir:     w.stateDir,
		StagingDir:   w.cacheDir,
		BabelVersion: babelVersion,
	})
	if err != nil {
		w.t.Fatalf("publish.New: %v", err)
	}
	return pub
}

func (w *world) push() *publish.Result {
	w.t.Helper()
	res, err := w.publisher(w.be.store).Push(context.Background())
	if err != nil {
		w.t.Fatalf("Push: %v", err)
	}
	return res
}

// -------------------------------------------------------------------------
// Step 1: bootstrap publication
// -------------------------------------------------------------------------

func (w *world) stepBootstrapPush() *publish.Result {
	t := w.t
	t.Helper()

	res := w.push()
	if res.Generation != 1 {
		t.Fatalf("bootstrap push: generation %d, want 1", res.Generation)
	}
	if !res.Changed {
		t.Fatal("bootstrap push: Changed=false, want a committed generation")
	}
	if !res.Bootstrap {
		t.Error("bootstrap push: Bootstrap=false, want true for a host's first generation")
	}
	if !res.BootstrapComplete {
		t.Error("bootstrap push: BootstrapComplete=false, but no adapter deferred a session")
	}
	if res.Sessions != 5 || res.Revisions != 5 {
		t.Errorf("bootstrap push: %d sessions / %d revisions, want 5/5", res.Sessions, res.Revisions)
	}
	if res.Published != 5 || res.CarriedForward != 0 || res.Deferred != 0 {
		t.Errorf("bootstrap push: published=%d carried=%d deferred=%d, want 5/0/0",
			res.Published, res.CarriedForward, res.Deferred)
	}

	// Every one of the three real adapters must be covered, completely.
	want := map[string]archive.AdapterCoverage{
		"claude": {Harness: "claude", AdapterSchema: 1, Scanned: 1, Published: 1, Complete: true},
		"codex":  {Harness: "codex", AdapterSchema: 1, Scanned: 2, Published: 2, Complete: true},
		"omp":    {Harness: "omp", AdapterSchema: 1, Scanned: 2, Published: 2, Complete: true},
	}
	assertCoverage(t, res.Coverage, want)
	return res
}

// assertCoverage compares a push's coverage against the expected set,
// harness by harness, and requires the canonical harness ordering.
func assertCoverage(t *testing.T, got []archive.AdapterCoverage, want map[string]archive.AdapterCoverage) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("coverage has %d adapters, want %d: %+v", len(got), len(want), got)
	}
	names := make([]string, 0, len(got))
	for _, cov := range got {
		names = append(names, cov.Harness)
	}
	if !slices.IsSorted(names) {
		t.Errorf("coverage is not in canonical harness order: %v", names)
	}
	for _, cov := range got {
		exp, ok := want[cov.Harness]
		if !ok {
			t.Errorf("unexpected coverage for harness %q", cov.Harness)
			continue
		}
		if cov.AdapterSchema != exp.AdapterSchema || cov.Scanned != exp.Scanned ||
			cov.Published != exp.Published || cov.CarriedForward != exp.CarriedForward ||
			cov.Deferred != exp.Deferred || cov.Complete != exp.Complete {
			t.Errorf("coverage %s = %+v, want %+v", cov.Harness, cov, exp)
		}
		if len(cov.DeferredReasons) != 0 {
			t.Errorf("coverage %s carries deferral reasons %v", cov.Harness, cov.DeferredReasons)
		}
	}
}

// -------------------------------------------------------------------------
// Step 2: catalog load
// -------------------------------------------------------------------------

// expectSession describes one catalog row. An empty title or workspace
// means the field must be absent and explained, never synthesized
// (SPEC.md §3).
type expectSession struct {
	key          string
	title        string
	workspace    string
	missing      []string
	continuation bool
	revisions    int
}

func (w *world) stepLoadCatalog() *catalog.Catalog {
	t := w.t
	t.Helper()

	cat := w.load()
	host, ok := cat.Host(hostID)
	if !ok {
		t.Fatalf("catalog: host %q absent", hostID)
	}
	if host.Generation != 1 {
		t.Errorf("catalog: host generation %d, want 1", host.Generation)
	}
	if host.DisplayName != hostDisplayName {
		t.Errorf("catalog: host display name %q, want %q", host.DisplayName, hostDisplayName)
	}
	if host.Sessions != 5 || host.Revisions != 5 {
		t.Errorf("catalog: host contributed %d sessions / %d revisions, want 5/5", host.Sessions, host.Revisions)
	}
	if host.Err != "" || len(host.Skipped) != 0 || len(host.Anomalies) != 0 {
		t.Errorf("catalog: host reported err=%q skipped=%v anomalies=%v", host.Err, host.Skipped, host.Anomalies)
	}
	if !host.HintPresent || host.HintGeneration != 1 || host.HintStale {
		t.Errorf("catalog: hint present=%t generation=%d stale=%t, want true/1/false",
			host.HintPresent, host.HintGeneration, host.HintStale)
	}

	w.assertSessions(cat, []expectSession{
		{
			key: sessionOMPAlpha, title: "Synthetic e2e session alpha",
			workspace: "/synthetic/workspace/alpha",
			missing:   []string{"lifecycle", "repo"},
			// The complete blob closure resolved, so this session may be
			// continued.
			continuation: true, revisions: 1,
		},
		{
			key: sessionOMPBeta, title: "Synthetic e2e session beta",
			workspace: "/synthetic/workspace/beta",
			missing:   []string{"lifecycle", "repo"},
			// One referenced blob is missing from the store, which forces
			// continuation grade off however complete the rest is.
			continuation: false, revisions: 1,
		},
		{
			key: sessionCodex, workspace: "/synthetic/workspace/codex",
			// Codex rollout logs record no title.
			missing: []string{"title", "lifecycle", "repo"}, revisions: 1,
		},
		{
			key: sessionCodexSt,
			// Host state is neither titled nor workspace-scoped.
			missing: []string{"title", "workspace", "lifecycle", "repo"}, revisions: 1,
		},
		{
			key: sessionClaude, title: "Synthetic e2e claude session",
			workspace: "/synthetic/workspace/claude",
			missing:   []string{"lifecycle", "repo", "artifacts"}, revisions: 1,
		},
	})

	// The Claude adapter can observe a branch but nothing else about the
	// repository, and says so rather than leaving the field bare.
	claudeRow := w.session(cat, sessionClaude)
	if claudeRow.Repo == nil || claudeRow.Repo.Branch != "main" {
		t.Errorf("claude session repo = %+v, want branch %q", claudeRow.Repo, "main")
	}

	// OMP session beta declares its unresolvable reference instead of
	// hiding it.
	beta := w.session(cat, sessionOMPBeta)
	if got := beta.Newest.Entry.UnresolvedBlobRefs; len(got) != 1 ||
		got[0] != "blob:"+string(w.src.ghostBlob) {
		t.Errorf("omp beta unresolved refs = %v, want exactly the absent blob %s", got, w.src.ghostBlob)
	}
	if len(beta.Newest.Entry.Blobs) != 1 {
		t.Errorf("omp beta resolved blobs = %d, want 1", len(beta.Newest.Entry.Blobs))
	}
	if beta.Newest.Entry.ContinuationGrade {
		t.Error("omp beta: ContinuationGrade=true despite an unresolved blob reference")
	}

	// OMP session alpha carries its complete sibling artifact tree.
	alpha := w.session(cat, sessionOMPAlpha)
	if got := artifactPaths(alpha.Newest.Entry); !slices.Equal(got, []string{"Helper.jsonl", "nested/7.bash.log"}) {
		t.Errorf("omp alpha artifacts = %v, want the full sibling tree", got)
	}
	return cat
}

func (w *world) assertSessions(cat *catalog.Catalog, want []expectSession) {
	t := w.t
	t.Helper()

	got := cat.Sessions()
	if len(got) != len(want) {
		keys := make([]string, 0, len(got))
		for _, s := range got {
			keys = append(keys, s.Key.String())
		}
		t.Fatalf("catalog holds %d sessions, want %d: %v", len(got), len(want), keys)
	}
	for _, exp := range want {
		s := w.session(cat, exp.key)
		checkOptional(t, exp.key+" title", s.Title, exp.title)
		checkOptional(t, exp.key+" workspace", s.Workspace, exp.workspace)
		for _, field := range exp.missing {
			if !hasReason(s.Completeness, field) {
				t.Errorf("%s: no completeness reason for absent %q (have %v)",
					exp.key, field, reasonFields(s.Completeness))
			}
		}
		if s.ContinuationGrade != exp.continuation {
			t.Errorf("%s: ContinuationGrade=%t, want %t", exp.key, s.ContinuationGrade, exp.continuation)
		}
		if exp.revisions != 0 && s.RevisionCount() != exp.revisions {
			t.Errorf("%s: %d revisions, want %d", exp.key, s.RevisionCount(), exp.revisions)
		}
		if s.Newest.Key() != s.Revisions[0].Key() {
			t.Errorf("%s: Newest %s is not Revisions[0] %s", exp.key, s.Newest.Key(), s.Revisions[0].Key())
		}
	}
}

func (w *world) load() *catalog.Catalog {
	w.t.Helper()
	// Passing no host set exercises host discovery, which is what a bare
	// `babel catalog` does against a shared remote.
	cat, err := catalog.Load(context.Background(), w.be.store, nil)
	if err != nil {
		w.t.Fatalf("catalog.Load: %v", err)
	}
	return cat
}

func (w *world) session(cat *catalog.Catalog, key string) catalog.Session {
	w.t.Helper()
	s, ok := cat.Session(key)
	if !ok {
		w.t.Fatalf("catalog: session %q absent", key)
	}
	return s
}

// -------------------------------------------------------------------------
// Step 3: resolve and fetch
// -------------------------------------------------------------------------

// stepResolveAndFetch resolves the bare session key of the richest session
// and proves the materialized bundle is byte-identical to the source tree.
// It returns the source bytes and the resolved revision key, which step 4
// re-fetches after the source has moved on.
func (w *world) stepResolveAndFetch(cat *catalog.Catalog) (source []byte, revisionKey string) {
	t := w.t
	t.Helper()

	rev, err := cat.Resolve(sessionOMPAlpha)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", sessionOMPAlpha, err)
	}
	newest := w.session(cat, sessionOMPAlpha).Newest
	if rev.Key() != newest.Key() {
		t.Fatalf("bare selector resolved %s, want the newest revision %s", rev.Key(), newest.Key())
	}
	if rev.Entry.Encoding != archive.EncodingFull {
		t.Errorf("bootstrap revision encoding = %q, want %q", rev.Entry.Encoding, archive.EncodingFull)
	}

	source = readFile(t, w.src.ompPrimaryAlpha)
	if rev.Entry.Content.Digest != archive.DigestBytes(source) {
		t.Fatalf("revision content digest %s does not describe the source log", rev.Entry.Content.Digest)
	}

	m := w.fetch(rev)
	if got := readFile(t, filepath.Join(m.Dir, m.Files[0].Path)); !bytesEqual(got, source) {
		t.Errorf("materialized primary (%d bytes) differs from the source log (%d bytes)", len(got), len(source))
	}
	if m.Encoding != archive.EncodingFull || m.ChainLength != 1 {
		t.Errorf("materialized encoding=%q chain=%d, want full/1", m.Encoding, m.ChainLength)
	}

	// The declared closure must be on disk, not merely referenced: two
	// sibling artifacts at their source-relative paths plus the one
	// resolvable blob, each verified against the digest the manifest
	// recorded (w.fetch re-hashes every written file).
	if got := kindPaths(m, catalog.KindArtifact); !slices.Equal(got, []string{"Helper.jsonl", "nested/7.bash.log"}) {
		t.Errorf("materialized artifacts = %v, want the declared closure", got)
	}
	blobs := kindPaths(m, catalog.KindBlob)
	if len(blobs) != 1 || blobs[0] != "blobs/"+w.src.alphaBlob.Hex() {
		t.Errorf("materialized blobs = %v, want blobs/%s", blobs, w.src.alphaBlob.Hex())
	}
	if got := readFile(t, filepath.Join(m.Dir, blobs[0])); !bytesEqual(got, w.src.alphaBlobBytes) {
		t.Error("materialized blob bytes differ from the source blob")
	}
	if len(m.UnresolvedBlobRefs) != 0 {
		t.Errorf("continuation-grade bundle reports unresolved refs %v", m.UnresolvedBlobRefs)
	}
	return source, rev.Key()
}

// fetch materializes one revision under the XDG data layout
// (bundles/<safe-session>/<digest-prefix>) and verifies every written file
// against the digest and size the manifest declared. A fetched bundle is
// immutable and Fetch refuses to overwrite one, so re-materializing a
// revision this suite already fetched lands in a sibling destination.
func (w *world) fetch(rev catalog.Revision) *catalog.Materialized {
	t := w.t
	t.Helper()

	_, content, err := archive.ParseRevisionKey(rev.Key())
	if err != nil {
		t.Fatalf("ParseRevisionKey(%s): %v", rev.Key(), err)
	}
	w.fetches++
	dest := filepath.Join(w.dataDir, "bundles",
		strings.ReplaceAll(rev.SessionKeyString(), "/", "_"), content.Hex()[:12])
	if w.fetched[dest] {
		dest += "-refetch-" + strconv.Itoa(w.fetches)
	}
	w.fetched[dest] = true

	m, err := catalog.Fetch(context.Background(), w.be.store, rev, dest)
	if err != nil {
		t.Fatalf("Fetch(%s): %v", rev.Key(), err)
	}
	if m.Dir != dest {
		t.Errorf("Fetch materialized into %s, want %s", m.Dir, dest)
	}
	if len(m.Files) == 0 || m.Files[0].Kind != catalog.KindPrimary {
		t.Fatalf("Fetch(%s): first materialized file is not the primary transcript", rev.Key())
	}
	var total int64
	for _, f := range m.Files {
		got := readFile(t, filepath.Join(dest, filepath.FromSlash(f.Path)))
		if int64(len(got)) != f.Size {
			t.Errorf("materialized %s: %d bytes, manifest declared %d", f.Path, len(got), f.Size)
		}
		if d := archive.DigestBytes(got); d != f.Digest {
			t.Errorf("materialized %s: digest %s, manifest declared %s", f.Path, d, f.Digest)
		}
		total += f.Size
	}
	if total != m.TotalSize {
		t.Errorf("materialized total %d bytes, reported %d", total, m.TotalSize)
	}
	return m
}

// -------------------------------------------------------------------------
// Step 4: append, republish, and recover both revisions
// -------------------------------------------------------------------------

// stepAppendAndRepublish appends to one OMP source log and republishes. The
// changed session must gain an append-delta revision whose stored object is
// the appended tail alone, every other session must be carried forward
// unchanged, and both the old and the new revision must still materialize
// byte-exactly. It returns the appended source bytes.
func (w *world) stepAppendAndRepublish(gen1 *publish.Result, oldRevKey string, original []byte) []byte {
	t := w.t
	t.Helper()

	tail := []byte(`{"type":"message","id":"e0000009","parentId":null,"timestamp":"2026-01-02T05:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic e2e appended message"}]}}` + "\n")
	appendTo(t, w.src.ompPrimaryAlpha, tail)
	appended := readFile(t, w.src.ompPrimaryAlpha)
	if !bytesEqual(appended[:len(original)], original) {
		t.Fatal("appended source is not an extension of the original bytes")
	}

	res := w.push()
	if res.Generation != 2 || !res.Changed {
		t.Fatalf("second push: generation %d changed %t, want 2/true", res.Generation, res.Changed)
	}
	if res.Bootstrap {
		t.Error("second push: Bootstrap=true, want false once a generation exists")
	}
	if res.CommitDigest == gen1.CommitDigest {
		t.Error("second push reused the bootstrap commit record")
	}
	if res.Published != 1 || res.CarriedForward != 4 || res.Deferred != 0 {
		t.Errorf("second push: published=%d carried=%d deferred=%d, want 1/4/0",
			res.Published, res.CarriedForward, res.Deferred)
	}
	assertCoverage(t, res.Coverage, map[string]archive.AdapterCoverage{
		"claude": {Harness: "claude", AdapterSchema: 1, Scanned: 1, CarriedForward: 1, Complete: true},
		"codex":  {Harness: "codex", AdapterSchema: 1, Scanned: 2, CarriedForward: 2, Complete: true},
		"omp":    {Harness: "omp", AdapterSchema: 1, Scanned: 2, Published: 1, CarriedForward: 1, Complete: true},
	})
	if res.Sessions != 5 || res.Revisions != 6 {
		t.Errorf("second push: %d sessions / %d revisions, want 5/6", res.Sessions, res.Revisions)
	}

	cat := w.load()
	alpha := w.session(cat, sessionOMPAlpha)
	if alpha.RevisionCount() != 2 {
		t.Fatalf("omp alpha has %d revisions after the append, want 2", alpha.RevisionCount())
	}
	e := alpha.Newest.Entry
	if e.Encoding != archive.EncodingAppendDelta {
		t.Fatalf("newest omp alpha revision encoding = %q, want %q", e.Encoding, archive.EncodingAppendDelta)
	}
	if e.Object.Size != int64(len(tail)) || e.Object.Digest != archive.DigestBytes(tail) {
		t.Errorf("append-delta object = %+v, want the appended tail only (%d bytes, %s)",
			e.Object, len(tail), archive.DigestBytes(tail))
	}
	if e.Content.Size != int64(len(appended)) || e.Content.Digest != archive.DigestBytes(appended) {
		t.Errorf("append-delta content = %+v, want the reassembled plaintext (%d bytes, %s)",
			e.Content, len(appended), archive.DigestBytes(appended))
	}
	if e.ParentRevision != oldRevKey {
		t.Errorf("append-delta parent = %q, want the bootstrap revision %q", e.ParentRevision, oldRevKey)
	}
	if e.ChainDepth != 1 {
		t.Errorf("append-delta chain depth = %d, want 1", e.ChainDepth)
	}
	if e.GenerationAdded != 2 {
		t.Errorf("append-delta generation_added = %d, want 2", e.GenerationAdded)
	}

	// Every other session is carried forward with its bootstrap revision
	// untouched: same revision key, same generation_added.
	for _, key := range []string{sessionOMPBeta, sessionCodex, sessionCodexSt, sessionClaude} {
		s := w.session(cat, key)
		if s.RevisionCount() != 1 {
			t.Errorf("%s: %d revisions, want 1 carried forward", key, s.RevisionCount())
		}
		if s.Newest.Entry.GenerationAdded != 1 {
			t.Errorf("%s: carried-forward revision claims generation_added %d, want 1",
				key, s.Newest.Entry.GenerationAdded)
		}
		if s.Newest.Generation != 2 {
			t.Errorf("%s: exposed by generation %d, want 2", key, s.Newest.Generation)
		}
	}

	// The immutable revision selector still yields the original plaintext,
	// and the bare selector now yields the appended one.
	oldRev, err := cat.Resolve(oldRevKey)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", oldRevKey, err)
	}
	oldBundle := w.fetch(oldRev)
	if got := readFile(t, filepath.Join(oldBundle.Dir, oldBundle.Files[0].Path)); !bytesEqual(got, original) {
		t.Error("re-fetched historical revision does not reproduce the original plaintext")
	}
	if oldBundle.Encoding != archive.EncodingFull || oldBundle.ChainLength != 1 {
		t.Errorf("historical bundle encoding=%q chain=%d, want full/1", oldBundle.Encoding, oldBundle.ChainLength)
	}

	newRev, err := cat.Resolve(sessionOMPAlpha)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", sessionOMPAlpha, err)
	}
	if newRev.Key() != e.RevisionKey {
		t.Fatalf("bare selector resolved %s, want the append-delta revision %s", newRev.Key(), e.RevisionKey)
	}
	newBundle := w.fetch(newRev)
	if got := readFile(t, filepath.Join(newBundle.Dir, newBundle.Files[0].Path)); !bytesEqual(got, appended) {
		t.Error("reassembled append-delta bundle does not reproduce the appended source bytes")
	}
	if newBundle.ChainLength != 2 {
		t.Errorf("append-delta bundle chain length = %d, want 2", newBundle.ChainLength)
	}
	return appended
}

// -------------------------------------------------------------------------
// Step 5: tiered verification
// -------------------------------------------------------------------------

// stepTieredVerify proves the two verification tiers differ exactly where
// the contract says they do: the default tier checks presence and size, so
// a same-size bit flip is invisible to it, while the deep tier reads every
// object and reports it. The flip is reverted afterwards and deep
// verification returns to OK, which is what makes the detection byte-exact
// rather than a store-wide false positive.
func (w *world) stepTieredVerify() {
	t := w.t
	t.Helper()

	if w.be.dir == "" {
		t.Fatalf("backend %s exposes no archive directory, so the byte-level steps cannot run", w.be.name)
	}

	w.requireVerified(false, "before tampering")
	w.requireVerified(true, "before tampering")

	// Pick a payload object that is not the one under active change: the
	// carried-forward OMP beta revision.
	cat := w.load()
	target := w.session(cat, sessionOMPBeta).Newest.Entry
	casPath := filepath.Join(w.be.dir, filepath.FromSlash(archive.CASKey(target.Object.Digest)))
	before := readFile(t, casPath)

	flipByte(t, casPath, int64(len(before)/2))
	after := readFile(t, casPath)
	if len(after) != len(before) {
		t.Fatalf("tampering changed the object size from %d to %d", len(before), len(after))
	}
	if bytesEqual(after, before) {
		t.Fatal("tampering did not change the object bytes")
	}

	// The default tier is stat-only by contract, so an equal-size flip
	// must stay invisible to it.
	shallow := w.verify(false)
	if !shallow.OK() {
		t.Errorf("default verify reported errors for a same-size bit flip it cannot see: %v",
			allErrors(shallow))
	}

	deep := w.verify(true)
	if deep.OK() {
		t.Fatal("deep verify accepted a corrupted payload object")
	}
	if !deep.Deep {
		t.Error("deep report does not record the deep tier")
	}
	errs := allErrors(deep)
	if !anyContains(errs, string(target.Object.Digest)) {
		t.Errorf("deep verify errors do not name the corrupted object %s: %v", target.Object.Digest, errs)
	}
	if !anyContains(errs, target.RevisionKey) {
		t.Errorf("deep verify errors do not name the affected revision %s: %v", target.RevisionKey, errs)
	}

	// Restoring the byte must restore verification exactly: detection is
	// byte-exact, not a sticky or store-wide verdict.
	flipByte(t, casPath, int64(len(before)/2))
	if got := readFile(t, casPath); !bytesEqual(got, before) {
		t.Fatal("restoring the flipped byte did not reproduce the original object")
	}
	w.requireVerified(true, "after restoring the flipped byte")
}

func (w *world) verify(deep bool) *catalog.Report {
	w.t.Helper()
	rep, err := catalog.Verify(context.Background(), w.be.store, nil, deep)
	if err != nil {
		w.t.Fatalf("catalog.Verify(deep=%t): %v", deep, err)
	}
	if len(rep.Hosts) != 1 || rep.Hosts[0].HostID != hostID {
		w.t.Fatalf("verify covered %d hosts, want only %q", len(rep.Hosts), hostID)
	}
	return rep
}

func (w *world) requireVerified(deep bool, when string) {
	t := w.t
	t.Helper()
	rep := w.verify(deep)
	if !rep.OK() {
		t.Fatalf("verify(deep=%t) %s reported errors: %v", deep, when, allErrors(rep))
	}
	h := rep.Hosts[0]
	if h.Generations == 0 || h.Revisions == 0 || h.Objects == 0 {
		t.Errorf("verify(deep=%t) %s counted generations=%d revisions=%d objects=%d, want non-zero",
			deep, when, h.Generations, h.Revisions, h.Objects)
	}
	// The archive intentionally holds one unresolvable blob reference,
	// which is a warning and never clears OK.
	if !anyContains(h.Warnings, "blob:"+string(w.src.ghostBlob)) {
		t.Errorf("verify(deep=%t) %s did not warn about the unresolved blob reference: %v",
			deep, when, h.Warnings)
	}
}

// -------------------------------------------------------------------------
// Step 6: interrupted push converges
// -------------------------------------------------------------------------

// stepInterruptedPushConverges interrupts a push in the middle of object
// upload, after at least one object is already durable, then re-runs it
// cleanly. Publication is journaled and idempotent, so recovery must add
// exactly one verified generation and leave the read side consistent
// (SPEC.md §6.1).
func (w *world) stepInterruptedPushConverges(previous []byte) {
	t := w.t
	t.Helper()

	tail := []byte(`{"type":"message","id":"e0000010","parentId":null,"timestamp":"2026-01-02T06:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic e2e message after interruption"}]}}` + "\n")
	appendTo(t, w.src.ompPrimaryAlpha, tail)
	expected := readFile(t, w.src.ompPrimaryAlpha)
	if !bytesEqual(expected[:len(previous)], previous) {
		t.Fatal("second append is not an extension of the published plaintext")
	}

	generationsBefore := w.commitGenerations()
	if !slices.Equal(generationsBefore, []uint64{1, 2}) {
		t.Fatalf("commit records before the interrupted push = %v, want [1 2]", generationsBefore)
	}

	// Fail the second content-addressed upload: the first one is already
	// durable, so the interruption really lands mid-upload.
	h := &hookStore{inner: w.be.store}
	h.fail = func(op, key string, _ int) error {
		if op != "put" || !strings.HasPrefix(key, "cas/sha256/") {
			return nil
		}
		h.casPuts++
		if h.firstCAS == "" {
			h.firstCAS = key
		}
		if h.casPuts == 2 {
			return errInterrupted
		}
		return nil
	}
	if _, err := w.publisher(h).Push(context.Background()); !errors.Is(err, errInterrupted) {
		t.Fatalf("interrupted push returned %v, want the simulated interruption", err)
	}
	if h.casPuts != 2 || h.firstCAS == "" {
		t.Fatalf("interruption fired after %d content-addressed puts, want exactly 2", h.casPuts)
	}
	if _, err := w.be.store.Stat(context.Background(), h.firstCAS); err != nil {
		t.Fatalf("first uploaded object %s is not durable, so the push was not interrupted mid-upload: %v",
			h.firstCAS, err)
	}

	// An interrupted push commits nothing: the previous generation is
	// still the whole story, and the journal records the intent.
	if got := w.commitGenerations(); !slices.Equal(got, []uint64{1, 2}) {
		t.Errorf("interrupted push left commit records %v, want [1 2]", got)
	}
	w.requireVerified(false, "after the interrupted push")
	if gen := w.load().Hosts()[0].Generation; gen != 2 {
		t.Errorf("catalog exposes generation %d after the interrupted push, want 2", gen)
	}
	w.assertJournal(3)

	res := w.push()
	if res.Generation != 3 || !res.Changed {
		t.Fatalf("recovery push: generation %d changed %t, want 3/true", res.Generation, res.Changed)
	}
	if got := w.commitGenerations(); !slices.Equal(got, []uint64{1, 2, 3}) {
		t.Fatalf("commit records after recovery = %v, want exactly one new generation [1 2 3]", got)
	}
	w.requireVerified(true, "after recovery")

	cat := w.load()
	w.assertSessions(cat, []expectSession{
		{
			key: sessionOMPAlpha, title: "Synthetic e2e session alpha",
			workspace: "/synthetic/workspace/alpha",
			missing:   []string{"lifecycle", "repo"}, continuation: true, revisions: 3,
		},
		{
			key: sessionOMPBeta, title: "Synthetic e2e session beta",
			workspace: "/synthetic/workspace/beta",
			missing:   []string{"lifecycle", "repo"}, revisions: 1,
		},
		{key: sessionCodex, workspace: "/synthetic/workspace/codex",
			missing: []string{"title", "lifecycle", "repo"}, revisions: 1},
		{key: sessionCodexSt, missing: []string{"title", "workspace", "lifecycle", "repo"}, revisions: 1},
		{
			key: sessionClaude, title: "Synthetic e2e claude session",
			workspace: "/synthetic/workspace/claude",
			missing:   []string{"lifecycle", "repo", "artifacts"}, revisions: 1,
		},
	})

	rev, err := cat.Resolve(sessionOMPAlpha)
	if err != nil {
		t.Fatalf("Resolve(%s): %v", sessionOMPAlpha, err)
	}
	if rev.Entry.ChainDepth != 2 || rev.Entry.Encoding != archive.EncodingAppendDelta {
		t.Errorf("recovered revision encoding=%q chain depth=%d, want append-delta/2",
			rev.Entry.Encoding, rev.Entry.ChainDepth)
	}
	bundle := w.fetch(rev)
	if got := readFile(t, filepath.Join(bundle.Dir, bundle.Files[0].Path)); !bytesEqual(got, expected) {
		t.Error("bundle fetched after recovery does not reproduce the source log")
	}

	// A further push has nothing to publish: convergence, not a loop.
	again := w.publisher(w.be.store)
	res2, err := again.Push(context.Background())
	if err != nil {
		t.Fatalf("settling push: %v", err)
	}
	if res2.Changed || res2.Generation != 3 {
		t.Errorf("settling push: generation %d changed %t, want 3/false", res2.Generation, res2.Changed)
	}
}

// assertJournal requires the private local journal to record an in-flight
// publication of the given generation. The journal is an accelerator, not
// an authority, so only its existence and target are asserted.
func (w *world) assertJournal(gen uint64) {
	t := w.t
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(w.stateDir, "publish-journal-"+hostID+".json"))
	if err != nil {
		t.Fatalf("read publication journal: %v", err)
	}
	var j struct {
		HostID     string `json:"host_id"`
		Generation uint64 `json:"generation"`
		Stage      string `json:"stage"`
	}
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatalf("decode publication journal: %v", err)
	}
	if j.HostID != hostID || j.Generation != gen || j.Stage == "" {
		t.Errorf("journal = %+v, want host %q at generation %d with a recorded stage", j, hostID, gen)
	}
}

// commitGenerations lists the generations a host has commit records for, in
// ascending order, with duplicates preserved so a same-generation anomaly
// would be visible.
func (w *world) commitGenerations() []uint64 {
	w.t.Helper()
	infos, err := w.be.store.List(context.Background(), archive.CommitPrefix(hostID))
	if err != nil {
		w.t.Fatalf("list commit records: %v", err)
	}
	gens := make([]uint64, 0, len(infos))
	for _, in := range infos {
		gen, _, ok := archive.ParseCommitKey(in.Key)
		if !ok {
			w.t.Errorf("unparseable commit-record key %q", in.Key)
			continue
		}
		gens = append(gens, gen)
	}
	slices.Sort(gens)
	return gens
}

// hookStore interposes on an object store's mutating operations so a push
// can be interrupted at an exact point. The pattern mirrors the
// crash-injection store of the publication tests and is restated here so
// this suite depends on no other package's test code.
type hookStore struct {
	inner    objectstore.Store
	ops      int
	casPuts  int
	firstCAS string
	fail     func(op, key string, n int) error
}

var _ objectstore.Store = (*hookStore)(nil)

func (h *hookStore) hook(op, key string) error {
	h.ops++
	if h.fail == nil {
		return nil
	}
	return h.fail(op, key, h.ops)
}

func (h *hookStore) Put(ctx context.Context, key string, r io.Reader) (bool, int64, error) {
	if err := h.hook("put", key); err != nil {
		return false, 0, err
	}
	return h.inner.Put(ctx, key, r)
}

func (h *hookStore) ReplacePointer(ctx context.Context, key string, data []byte) error {
	if err := h.hook("pointer", key); err != nil {
		return err
	}
	return h.inner.ReplacePointer(ctx, key, data)
}

func (h *hookStore) Stat(ctx context.Context, key string) (objectstore.Info, error) {
	return h.inner.Stat(ctx, key)
}

func (h *hookStore) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	return h.inner.Read(ctx, key)
}

func (h *hookStore) List(ctx context.Context, prefix string) ([]objectstore.Info, error) {
	return h.inner.List(ctx, prefix)
}

// -------------------------------------------------------------------------
// Step 7: direct recovery from the store layout alone
// -------------------------------------------------------------------------

// On-disk shapes of the archive documents, declared locally so this step
// depends on nothing but the documented layout — the executable form of the
// `rclone` + `sha256sum` + `jq` + `cat` walkthrough in
// docs/contracts/archive-v1.md §9.
type (
	rawObjectRef struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	}
	rawLatestHint struct {
		HostID     string       `json:"host_id"`
		Generation uint64       `json:"generation"`
		Commit     rawObjectRef `json:"commit"`
	}
	rawCommitRecord struct {
		HostID     string       `json:"host_id"`
		Generation uint64       `json:"generation"`
		Index      rawObjectRef `json:"index"`
	}
	rawSegmentRef struct {
		Partition string       `json:"partition"`
		Object    rawObjectRef `json:"object"`
	}
	rawGenerationIndex struct {
		HostID     string          `json:"host_id"`
		Generation uint64          `json:"generation"`
		Segments   []rawSegmentRef `json:"segments"`
	}
	rawEntry struct {
		SessionKey      string       `json:"session_key"`
		RevisionKey     string       `json:"revision_key"`
		GenerationAdded uint64       `json:"generation_added"`
		Encoding        string       `json:"encoding"`
		Content         rawObjectRef `json:"content"`
		Object          rawObjectRef `json:"object"`
	}
	rawSegment struct {
		Partition string     `json:"partition"`
		Entries   []rawEntry `json:"entries"`
	}
)

// stepDirectRecovery recovers one full-encoding session's plaintext using
// only the store's files, the documented key layout, SHA-256, and JSON
// parsing: no catalog, no fetch, no publisher, no local state. This is the
// frozen disaster-recovery promise of docs/contracts/archive-v1.md §9.
func (w *world) stepDirectRecovery() {
	t := w.t
	t.Helper()

	if w.be.dir == "" {
		t.Fatalf("backend %s exposes no archive directory, so direct recovery cannot run", w.be.name)
	}
	root := w.be.dir

	// Step 1 — find the newest commit record. Canonical order is ascending
	// lexicographic key order, so the last name wins.
	commitsDir := filepath.Join(root, "hosts", hostID, "commits")
	entries, err := os.ReadDir(commitsDir)
	if err != nil {
		t.Fatalf("list commit records: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatal("no commit records on disk")
	}
	slices.Sort(names)
	name := names[len(names)-1]

	// Step 2 — verify the record against its own write-once key.
	recBytes := readFile(t, filepath.Join(commitsDir, name))
	genText, digestText, ok := splitCommitName(name)
	if !ok {
		t.Fatalf("commit-record name %q does not match g<generation>-<digest>.json", name)
	}
	if got := sha256Hex(recBytes); got != digestText {
		t.Fatalf("commit record hashes to %s but its key claims %s", got, digestText)
	}
	gen, err := strconv.ParseUint(genText, 10, 64)
	if err != nil {
		t.Fatalf("parse generation from %q: %v", name, err)
	}
	var rec rawCommitRecord
	decodeJSON(t, recBytes, &rec)
	if rec.HostID != hostID || rec.Generation != gen {
		t.Fatalf("commit record claims host %q generation %d, key says %q/%d",
			rec.HostID, rec.Generation, hostID, gen)
	}

	// The mutable hint is a cross-check only; recovery never depends on it.
	var hint rawLatestHint
	decodeJSON(t, readFile(t, filepath.Join(root, "hosts", hostID, "latest.json")), &hint)
	if hint.Generation != gen || hint.Commit.Digest != "sha256:"+digestText {
		t.Errorf("latest hint = %+v, want generation %d and commit sha256:%s",
			hint, gen, digestText)
	}
	if hint.Commit.Size != int64(len(recBytes)) {
		t.Errorf("latest hint records commit size %d, record is %d bytes", hint.Commit.Size, len(recBytes))
	}

	// Step 3 — read and verify the generation index.
	var idx rawGenerationIndex
	decodeJSON(t, w.readCAS(rec.Index), &idx)
	if idx.HostID != hostID || idx.Generation != gen {
		t.Fatalf("generation index claims host %q generation %d, want %q/%d",
			idx.HostID, idx.Generation, hostID, gen)
	}

	// Step 4 — locate the session's manifest segment by partition: the
	// first byte of sha256(session_key) in lowercase hex.
	sessionKey := sessionClaude
	sum := sha256.Sum256([]byte(sessionKey))
	partition := hex.EncodeToString(sum[:1])
	var segRef *rawSegmentRef
	for i := range idx.Segments {
		if idx.Segments[i].Partition == partition {
			segRef = &idx.Segments[i]
			break
		}
	}
	if segRef == nil {
		t.Fatalf("generation index has no segment for partition %q of %s", partition, sessionKey)
	}
	var seg rawSegment
	decodeJSON(t, w.readCAS(segRef.Object), &seg)
	if seg.Partition != partition {
		t.Fatalf("segment declares partition %q, index said %q", seg.Partition, partition)
	}

	// Step 5 — select the session's newest revision inside the segment.
	var found *rawEntry
	for i := range seg.Entries {
		e := &seg.Entries[i]
		if e.SessionKey != sessionKey {
			continue
		}
		if found == nil || e.GenerationAdded > found.GenerationAdded {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("segment %s holds no entry for %s", partition, sessionKey)
	}
	if found.Encoding != string(archive.EncodingFull) {
		t.Fatalf("recovery target %s is %q encoded, want a full revision", found.RevisionKey, found.Encoding)
	}

	// Step 6 — read the payload object and verify the recovered plaintext
	// against the recorded content digest, then against the source log.
	payload := w.readCAS(found.Object)
	if got := "sha256:" + sha256Hex(payload); got != found.Content.Digest {
		t.Fatalf("recovered plaintext hashes to %s, entry declares %s", got, found.Content.Digest)
	}
	if int64(len(payload)) != found.Content.Size {
		t.Fatalf("recovered plaintext is %d bytes, entry declares %d", len(payload), found.Content.Size)
	}
	if source := readFile(t, w.src.claudePrimary); !bytesEqual(payload, source) {
		t.Fatalf("directly recovered plaintext (%d bytes) differs from the source transcript (%d bytes)",
			len(payload), len(source))
	}
	if !strings.HasPrefix(found.RevisionKey, sessionKey+"@") {
		t.Errorf("revision key %q is not a revision of %q", found.RevisionKey, sessionKey)
	}
}

// readCAS reads one content-addressed object straight from the store's
// directory tree and verifies its size and digest, exactly as
// `rclone cat … | sha256sum` does in the recovery walkthrough.
func (w *world) readCAS(ref rawObjectRef) []byte {
	t := w.t
	t.Helper()
	digestHex, ok := strings.CutPrefix(ref.Digest, "sha256:")
	if !ok || len(digestHex) != 64 {
		t.Fatalf("object reference %+v is not a canonical sha256 digest", ref)
	}
	path := filepath.Join(w.be.dir, "cas", "sha256", digestHex[:2], digestHex)
	data := readFile(t, path)
	if int64(len(data)) != ref.Size {
		t.Fatalf("object %s is %d bytes on disk, reference says %d", ref.Digest, len(data), ref.Size)
	}
	if got := sha256Hex(data); got != digestHex {
		t.Fatalf("object at %s hashes to %s, key claims %s", path, got, digestHex)
	}
	return data
}

// splitCommitName splits "g<10 digits>-<64 hex>.json" into its generation
// and record-digest halves.
func splitCommitName(name string) (gen, digest string, ok bool) {
	trimmed, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return "", "", false
	}
	trimmed, ok = strings.CutPrefix(trimmed, "g")
	if !ok {
		return "", "", false
	}
	gen, digest, ok = strings.Cut(trimmed, "-")
	if !ok || len(gen) != 10 || len(digest) != 64 {
		return "", "", false
	}
	return gen, digest, true
}

// -------------------------------------------------------------------------
// Synthetic source corpus
// -------------------------------------------------------------------------

// sources is one synthetic local corpus: an OMP data root with two
// sessions, a sibling artifact tree and a content-addressed blob store
// holding one referenced blob per session plus one reference that resolves
// to nothing; a Codex root with one rollout log, its attachment, and both
// host-state files; and a Claude Code root with one project session and its
// sibling subagent tree.
type sources struct {
	ompSessions string // root the OMP adapter scans
	ompBlobs    string
	codexRoot   string
	claudeRoot  string

	ompPrimaryAlpha string
	ompPrimaryBeta  string
	codexRollout    string
	codexHistory    string
	claudePrimary   string

	alphaBlob      archive.Digest
	alphaBlobBytes []byte
	betaBlob       archive.Digest
	ghostBlob      archive.Digest // referenced by beta, never stored
}

func newSources(t *testing.T) *sources {
	t.Helper()
	root := t.TempDir()

	s := &sources{
		ompSessions: filepath.Join(root, "omp", "agent", "sessions"),
		ompBlobs:    filepath.Join(root, "omp", "agent", "blobs"),
		codexRoot:   filepath.Join(root, "codex"),
		claudeRoot:  filepath.Join(root, "claude"),
	}

	// The blob store first: session logs reference blobs by digest, so the
	// references are generated from the bytes actually written (and, for
	// the dangling reference, from bytes deliberately not written).
	s.alphaBlobBytes = []byte("synthetic e2e blob alpha\n")
	betaBytes := []byte("synthetic e2e blob beta\n")
	ghostBytes := []byte("synthetic e2e blob that was never stored\n")
	s.alphaBlob = archive.DigestBytes(s.alphaBlobBytes)
	s.betaBlob = archive.DigestBytes(betaBytes)
	s.ghostBlob = archive.DigestBytes(ghostBytes)
	writeFile(t, filepath.Join(s.ompBlobs, s.alphaBlob.Hex()), s.alphaBlobBytes)
	writeFile(t, filepath.Join(s.ompBlobs, s.betaBlob.Hex()), betaBytes)

	// OMP session alpha: title record, session record, one message
	// referencing a resolvable blob, plus a complete sibling artifact tree.
	s.ompPrimaryAlpha = filepath.Join(s.ompSessions, ompProjectAlpha, ompStemAlpha+".jsonl")
	writeFile(t, s.ompPrimaryAlpha, []byte(strings.Join([]string{
		`{"type":"title","v":1,"title":"Synthetic e2e session alpha","source":"auto","updatedAt":"2026-01-02T04:00:00.000Z","pad":"                                        "}`,
		`{"type":"session","version":3,"id":"00000000-0000-4000-8000-0000000000a1","timestamp":"2026-01-02T03:04:05.678Z","cwd":"/synthetic/workspace/alpha","title":"Synthetic e2e session alpha","titleSource":"auto"}`,
		`{"type":"message","id":"e0000001","parentId":null,"timestamp":"2026-01-02T03:10:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic e2e message one"},{"type":"image","data":"blob:` + string(s.alphaBlob) + `","mimeType":"image/webp"}]}}`,
		`{"type":"message","id":"e0000002","parentId":"e0000001","timestamp":"2026-01-02T03:10:30.000Z","message":{"role":"assistant","content":[{"type":"text","text":"synthetic e2e reply one"}]}}`,
		"",
	}, "\n")))
	artifactDir := filepath.Join(s.ompSessions, ompProjectAlpha, ompStemAlpha)
	writeFile(t, filepath.Join(artifactDir, "Helper.jsonl"),
		[]byte(`{"type":"message","id":"e0000003","parentId":null,"timestamp":"2026-01-02T03:11:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic e2e subagent message"}]}}`+"\n"))
	writeFile(t, filepath.Join(artifactDir, "nested", "7.bash.log"),
		[]byte("synthetic e2e bash log line\n"))
	// A decoy the adapter must not mistake for a session.
	writeFile(t, filepath.Join(s.ompSessions, ompProjectAlpha, ".ignored-not-a-session.txt"),
		[]byte("synthetic e2e decoy\n"))

	// OMP session beta: one resolvable and one dangling blob reference, so
	// its closure is incomplete and continuation grade must be withheld.
	s.ompPrimaryBeta = filepath.Join(s.ompSessions, ompProjectBeta, ompStemBeta+".jsonl")
	writeFile(t, s.ompPrimaryBeta, []byte(strings.Join([]string{
		`{"type":"title","v":1,"title":"Synthetic e2e session beta","source":"user","updatedAt":"2026-01-03T07:00:00.000Z","pad":"                                        "}`,
		`{"type":"session","version":3,"id":"00000000-0000-4000-8000-0000000000b2","timestamp":"2026-01-03T06:07:08.900Z","cwd":"/synthetic/workspace/beta","parentSession":"00000000-0000-4000-8000-0000000000a1","title":"Synthetic e2e session beta","titleSource":"user"}`,
		`{"type":"message","id":"e0000004","parentId":null,"timestamp":"2026-01-03T06:10:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic e2e message two"},{"type":"image","data":"blob:` + string(s.betaBlob) + `","mimeType":"image/webp"},{"type":"image","data":"blob:` + string(s.ghostBlob) + `","mimeType":"image/webp"}]}}`,
		"",
	}, "\n")))

	// Codex: one rollout log referencing one attachment directory, plus the
	// two host-level state files archived as the dedicated state session.
	s.codexRollout = filepath.Join(s.codexRoot, filepath.FromSlash(codexRolloutRel))
	attachmentPath := "/synthetic/home/.codex/attachments/" + codexAttachmentID + "/" + codexAttachment
	writeFile(t, s.codexRollout, []byte(strings.Join([]string{
		`{"timestamp":"2026-01-02T03:04:05.100Z","type":"session_meta","payload":{"session_id":"aaaaaaaa-0000-4000-8000-00000000000a","id":"aaaaaaaa-0000-4000-8000-0000000000c3","timestamp":"2026-01-02T03:04:05.100Z","cwd":"/synthetic/workspace/codex","originator":"Synthetic E2E Harness","cli_version":"0.0.0-synthetic","model_provider":"synthetic-provider","thread_source":"fixture","history_mode":"legacy"}}`,
		`{"timestamp":"2026-01-02T03:04:05.200Z","type":"turn_context","payload":{"approval_policy":"on-request","cwd":"/synthetic/workspace/codex","model":"synthetic-model-a","workspace_roots":["/synthetic/workspace/codex"]}}`,
		`{"timestamp":"2026-01-02T03:04:06.000Z","type":"response_item","payload":{"id":"synthetic-item-1","type":"message","role":"user","content":[{"type":"input_text","text":"synthetic e2e codex message\n\n# Files mentioned by the user:\n\n## ` + codexAttachment + `: ` + attachmentPath + `\n"}]}}`,
		`{"timestamp":"2026-01-02T03:04:09.500Z","type":"response_item","payload":{"id":"synthetic-item-2","type":"message","role":"assistant","content":[{"type":"output_text","text":"synthetic e2e codex reply"}]}}`,
		"",
	}, "\n")))
	writeFile(t, filepath.Join(s.codexRoot, "attachments", codexAttachmentID, codexAttachment),
		[]byte("synthetic e2e attachment bytes\n"))
	s.codexHistory = filepath.Join(s.codexRoot, "history.jsonl")
	writeFile(t, s.codexHistory, []byte(strings.Join([]string{
		`{"session_id":"aaaaaaaa-0000-4000-8000-00000000000a","ts":1767322000,"text":"synthetic e2e prompt one"}`,
		`{"session_id":"aaaaaaaa-0000-4000-8000-00000000000b","ts":1767322500,"text":"synthetic e2e prompt two"}`,
		"",
	}, "\n")))
	writeFile(t, filepath.Join(s.codexRoot, "session_index.jsonl"), []byte(strings.Join([]string{
		`{"id":"aaaaaaaa-0000-4000-8000-0000000000c3","thread_name":"synthetic e2e thread","updated_at":"2026-01-02T03:04:09.500000000Z"}`,
		"",
	}, "\n")))

	// Claude Code: one project session with a sibling subagent tree.
	s.claudePrimary = filepath.Join(s.claudeRoot, "projects", claudeProject, claudeSession+".jsonl")
	writeFile(t, s.claudePrimary, []byte(strings.Join([]string{
		`{"type":"ai-title","aiTitle":"Synthetic e2e claude session","sessionId":"` + claudeSession + `"}`,
		`{"parentUuid":null,"isSidechain":false,"userType":"external","cwd":"/synthetic/workspace/claude","sessionId":"` + claudeSession + `","version":"9.9.999","gitBranch":"main","type":"user","message":{"role":"user","content":"synthetic e2e claude message"},"uuid":"11111111-0000-4000-8000-0000000000f1","timestamp":"2026-03-01T08:00:00.000Z"}`,
		`{"parentUuid":"11111111-0000-4000-8000-0000000000f1","isSidechain":false,"userType":"external","cwd":"/synthetic/workspace/claude","sessionId":"` + claudeSession + `","version":"9.9.999","gitBranch":"main","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"synthetic e2e claude reply"}]},"uuid":"11111111-0000-4000-8000-0000000000f2","timestamp":"2026-03-01T08:00:09.000Z"}`,
		"",
	}, "\n")))
	writeFile(t, filepath.Join(s.claudeRoot, "projects", claudeProject, claudeSession, "subagents", claudeSubagent),
		[]byte(`{"type":"user","isSidechain":true,"sessionId":"`+claudeSession+`","timestamp":"2026-03-01T08:01:00.000Z","message":{"role":"user","content":"synthetic e2e subagent prompt"}}`+"\n"))

	return s
}

// -------------------------------------------------------------------------
// Small assertion and filesystem helpers
// -------------------------------------------------------------------------

func mkdirIn(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name, "babel")
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	return dir
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, data, filePerm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func appendTo(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		t.Fatalf("open %s for append: %v", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatalf("append to %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

// flipByte inverts one bit of one byte in place, leaving the file size
// untouched. Applying it twice at the same offset restores the original.
func flipByte(t *testing.T, path string, off int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s for tampering: %v", path, err)
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], off); err != nil {
		t.Fatalf("read byte %d of %s: %v", off, path, err)
	}
	b[0] ^= 0x01
	if _, err := f.WriteAt(b[:], off); err != nil {
		t.Fatalf("write byte %d of %s: %v", off, path, err)
	}
	if err := f.Sync(); err != nil {
		t.Fatalf("sync %s: %v", path, err)
	}
}

func decodeJSON(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatalf("decode %T: %v", dst, err)
	}
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkOptional requires an optional catalog field to hold want, or to be
// absent when want is empty.
func checkOptional(t *testing.T, label string, got *string, want string) {
	t.Helper()
	switch {
	case want == "" && got != nil:
		t.Errorf("%s = %q, want absent", label, *got)
	case want != "" && got == nil:
		t.Errorf("%s absent, want %q", label, want)
	case want != "" && *got != want:
		t.Errorf("%s = %q, want %q", label, *got, want)
	}
}

func hasReason(reasons []archive.CompletenessReason, field string) bool {
	for _, r := range reasons {
		if r.Field == field && r.Reason != "" {
			return true
		}
	}
	return false
}

func reasonFields(reasons []archive.CompletenessReason) []string {
	out := make([]string, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, r.Field)
	}
	return out
}

func artifactPaths(e archive.ManifestEntry) []string {
	out := make([]string, 0, len(e.Artifacts))
	for _, a := range e.Artifacts {
		out = append(out, a.Path)
	}
	return out
}

func kindPaths(m *catalog.Materialized, kind catalog.FileKind) []string {
	var out []string
	for _, f := range m.Files {
		if f.Kind == kind {
			out = append(out, f.Path)
		}
	}
	return out
}

func allErrors(r *catalog.Report) []string {
	var out []string
	for _, h := range r.Hosts {
		out = append(out, h.Errors...)
	}
	return out
}

func anyContains(values []string, substr string) bool {
	for _, v := range values {
		if strings.Contains(v, substr) {
			return true
		}
	}
	return false
}
