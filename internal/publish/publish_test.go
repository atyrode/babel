package publish

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
	"github.com/atyrode/babel/internal/objectstore/local"
)

const testHost = "host-a"

func fixedTime() time.Time { return time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC) }

// line renders one synthetic transcript record. Tests never use real
// session content.
func line(n int) string { return fmt.Sprintf("{\"seq\":%d,\"text\":\"synthetic-%d\"}\n", n, n) }

// lines renders a synthetic transcript of n records.
func lines(n int) []byte {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(line(i))
	}
	return []byte(b.String())
}

// fakeSession is one synthetic local session an adapter can stage.
type fakeSession struct {
	content   []byte
	artifacts map[string][]byte
	unstable  bool
}

// fakeAdapter implements adapter.Adapter over in-memory synthetic sessions.
type fakeAdapter struct {
	harness       string
	schema        int
	sessions      map[string]*fakeSession
	discoverErr   error
	snapshotCalls map[string]int
	lastRoots     []string
	snapTime      time.Time
}

func newFakeAdapter(harness string, schema int) *fakeAdapter {
	return &fakeAdapter{
		harness:       harness,
		schema:        schema,
		sessions:      map[string]*fakeSession{},
		snapshotCalls: map[string]int{},
		snapTime:      fixedTime(),
	}
}

func (a *fakeAdapter) set(sourceID string, content []byte) {
	a.sessions[sourceID] = &fakeSession{content: content}
}

func (a *fakeAdapter) appendTo(t *testing.T, sourceID, extra string) {
	t.Helper()
	s, ok := a.sessions[sourceID]
	if !ok {
		t.Fatalf("session %q not present", sourceID)
	}
	s.content = append(slices.Clone(s.content), extra...)
}

func (a *fakeAdapter) Harness() string { return a.harness }
func (a *fakeAdapter) Schema() int     { return a.schema }
func (a *fakeAdapter) DefaultRoots() []string {
	return []string{"/synthetic/default/" + a.harness}
}

func (a *fakeAdapter) Discover(_ context.Context, roots []string) ([]adapter.SourceSession, error) {
	a.lastRoots = slices.Clone(roots)
	if a.discoverErr != nil {
		return nil, a.discoverErr
	}
	// Returned in reverse order on purpose: the publisher must impose its
	// own deterministic scan order.
	ids := slices.Sorted(maps.Keys(a.sessions))
	slices.Reverse(ids)
	out := make([]adapter.SourceSession, 0, len(ids))
	for _, id := range ids {
		out = append(out, adapter.SourceSession{
			Harness:     a.harness,
			SourceID:    id,
			PrimaryPath: "/synthetic/" + a.harness + "/" + id,
		})
	}
	return out, nil
}

func (a *fakeAdapter) Snapshot(_ context.Context, src adapter.SourceSession, stagingDir string) (*adapter.Snapshot, error) {
	a.snapshotCalls[src.SourceID]++
	s, ok := a.sessions[src.SourceID]
	if !ok {
		return nil, fmt.Errorf("source %q disappeared", src.SourceID)
	}
	if s.unstable {
		return nil, fmt.Errorf("source %q: %w", src.SourceID, adapter.ErrUnstable)
	}
	primary := filepath.Join(stagingDir, "primary.jsonl")
	if err := os.WriteFile(primary, s.content, 0o600); err != nil {
		return nil, err
	}
	var staged []adapter.StagedFile
	for _, rel := range slices.Sorted(maps.Keys(s.artifacts)) {
		p := filepath.Join(stagingDir, "artifacts", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, s.artifacts[rel], 0o600); err != nil {
			return nil, err
		}
		staged = append(staged, adapter.StagedFile{RelPath: rel, StagedPath: p, Size: int64(len(s.artifacts[rel]))})
	}
	title := "session " + src.SourceID
	return &adapter.Snapshot{
		Source:        src,
		SnapshotTime:  a.snapTime,
		StagedPrimary: primary,
		PrimarySize:   int64(len(s.content)),
		Meta: adapter.CommonMeta{
			Title: &title,
			Completeness: []archive.CompletenessReason{
				{Field: "workspace", Reason: "synthetic source has no workspace"},
			},
		},
		AdapterMetadataSchema: 1,
		AdapterMetadata:       []byte(`{"synthetic": true}`),
		Artifacts:             staged,
		ContinuationGrade:     true,
	}, nil
}

// env is one isolated publication environment: a local archive, a state
// and staging directory, and the adapters a push scans.
type env struct {
	t        *testing.T
	root     string
	backing  *local.Store
	store    objectstore.Store
	adapters []adapter.Adapter
	roots    map[string][]string
	now      time.Time
}

func newEnv(t *testing.T) *env {
	t.Helper()
	root := t.TempDir()
	backing, err := local.New(filepath.Join(root, "archive"))
	if err != nil {
		t.Fatalf("open local store: %v", err)
	}
	return &env{t: t, root: root, backing: backing, store: backing, now: fixedTime()}
}

func (e *env) add(a adapter.Adapter) { e.adapters = append(e.adapters, a) }

func (e *env) publisher() *Publisher {
	e.t.Helper()
	p, err := New(Config{
		Store:           e.store,
		Adapters:        e.adapters,
		HostID:          testHost,
		HostDisplayName: "Host A",
		Roots:           e.roots,
		StateDir:        filepath.Join(e.root, "state"),
		StagingDir:      filepath.Join(e.root, "staging"),
		BabelVersion:    "test",
		Now:             func() time.Time { return e.now },
	})
	if err != nil {
		e.t.Fatalf("new publisher: %v", err)
	}
	return p
}

// push runs a complete publication and fails the test on error. Every push
// builds a fresh Publisher, the way a fresh process would.
func (e *env) push() *Result {
	e.t.Helper()
	res, err := e.publisher().Push(context.Background())
	if err != nil {
		e.t.Fatalf("push: %v", err)
	}
	return res
}

func (e *env) pushErr() error {
	e.t.Helper()
	_, err := e.publisher().Push(context.Background())
	return err
}

func (e *env) head() *archive.Head {
	e.t.Helper()
	h, err := archive.VerifiedHead(context.Background(), e.backing, testHost)
	if err != nil {
		e.t.Fatalf("verified head: %v", err)
	}
	return h
}

func (e *env) entries() []archive.ManifestEntry {
	e.t.Helper()
	h := e.head()
	if h == nil {
		return nil
	}
	entries, err := archive.LoadEntries(context.Background(), e.backing, h.Index)
	if err != nil {
		e.t.Fatalf("load entries: %v", err)
	}
	return entries
}

func (e *env) commitKeys() []string {
	e.t.Helper()
	infos, err := e.backing.List(context.Background(), archive.CommitPrefix(testHost))
	if err != nil {
		e.t.Fatalf("list commit records: %v", err)
	}
	keys := make([]string, len(infos))
	for i, in := range infos {
		keys[i] = in.Key
	}
	return keys
}

func (e *env) hint() *archive.LatestHint {
	e.t.Helper()
	h, err := archive.ReadLatestHint(context.Background(), e.backing, testHost)
	if err != nil {
		e.t.Fatalf("read latest hint: %v", err)
	}
	return h
}

// newest returns a session's newest committed revision in entries.
func newestOf(t *testing.T, entries []archive.ManifestEntry, sessionKey string) archive.ManifestEntry {
	t.Helper()
	var best archive.ManifestEntry
	for _, e := range entries {
		if e.SessionKey != sessionKey {
			continue
		}
		if best.RevisionKey == "" || newerRevision(e, best) {
			best = e
		}
	}
	if best.RevisionKey == "" {
		t.Fatalf("session %q has no committed revision", sessionKey)
	}
	return best
}

func revisionsOf(entries []archive.ManifestEntry, sessionKey string) int {
	n := 0
	for _, e := range entries {
		if e.SessionKey == sessionKey {
			n++
		}
	}
	return n
}

func coverageOf(t *testing.T, res *Result, harness string) archive.AdapterCoverage {
	t.Helper()
	for _, c := range res.Coverage {
		if c.Harness == harness {
			return c
		}
	}
	t.Fatalf("no coverage for harness %q", harness)
	return archive.AdapterCoverage{}
}

func sessionKeyOf(harness, sourceID string) string {
	return archive.SessionKey{Harness: harness, HostID: testHost, SourceID: sourceID}.String()
}

// reassemble walks a revision's append chain exactly as a reader would and
// returns the plaintext, failing unless it digest-verifies.
func (e *env) reassemble(entries []archive.ManifestEntry, rev archive.ManifestEntry) []byte {
	e.t.Helper()
	byRevision := make(map[string]archive.ManifestEntry, len(entries))
	for _, en := range entries {
		byRevision[en.RevisionKey] = en
	}
	chain, err := chainFor(byRevision, rev.RevisionKey)
	if err != nil {
		e.t.Fatalf("chain for %s: %v", rev.RevisionKey, err)
	}
	rc := openChain(context.Background(), e.backing, chain)
	defer rc.Close()
	plaintext, err := io.ReadAll(rc)
	if err != nil {
		e.t.Fatalf("reassemble %s: %v", rev.RevisionKey, err)
	}
	if got := archive.DigestBytes(plaintext); got != rev.Content.Digest {
		e.t.Fatalf("reassembled %s to digest %s, want %s", rev.RevisionKey, got, rev.Content.Digest)
	}
	if int64(len(plaintext)) != rev.Content.Size {
		e.t.Fatalf("reassembled %s to %d bytes, want %d", rev.RevisionKey, len(plaintext), rev.Content.Size)
	}
	return plaintext
}

func (e *env) object(digest archive.Digest) []byte {
	e.t.Helper()
	rc, err := e.backing.Read(context.Background(), archive.CASKey(digest))
	if err != nil {
		e.t.Fatalf("read object %s: %v", digest, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		e.t.Fatalf("read object %s: %v", digest, err)
	}
	return raw
}

// twoHarnessEnv is the shared starting corpus: two OMP sessions and one
// Codex session, none of them published yet.
func twoHarnessEnv(t *testing.T) (*env, *fakeAdapter, *fakeAdapter) {
	t.Helper()
	e := newEnv(t)
	omp := newFakeAdapter("omp", 3)
	omp.set("s1", lines(4))
	omp.set("s2", lines(2))
	codex := newFakeAdapter("codex", 1)
	codex.set("c1", lines(3))
	e.add(omp)
	e.add(codex)
	return e, omp, codex
}

// (a) A bootstrap push from an empty store publishes generation 1.
func TestPushBootstrapsGenerationOne(t *testing.T) {
	e, omp, codex := twoHarnessEnv(t)
	e.roots = map[string][]string{"omp": {"/synthetic/configured/omp"}}
	omp.sessions["s1"].artifacts = map[string][]byte{"notes/a.txt": []byte("artifact-a")}

	res := e.push()

	if !res.Changed || res.Generation != 1 || !res.Bootstrap || !res.BootstrapComplete {
		t.Fatalf("bootstrap result = %+v", res)
	}
	if res.Published != 3 || res.CarriedForward != 0 || res.Deferred != 0 {
		t.Fatalf("counts published=%d carried=%d deferred=%d", res.Published, res.CarriedForward, res.Deferred)
	}
	if got := coverageOf(t, res, "omp"); got.Scanned != 2 || got.Published != 2 || got.AdapterSchema != 3 || !got.Complete {
		t.Fatalf("omp coverage = %+v", got)
	}
	if got := coverageOf(t, res, "codex"); got.Scanned != 1 || got.Published != 1 || got.AdapterSchema != 1 || !got.Complete {
		t.Fatalf("codex coverage = %+v", got)
	}
	// Configured roots override the adapter default; an unconfigured
	// harness falls back to DefaultRoots.
	if !slices.Equal(omp.lastRoots, []string{"/synthetic/configured/omp"}) {
		t.Fatalf("omp roots = %v", omp.lastRoots)
	}
	if !slices.Equal(codex.lastRoots, codex.DefaultRoots()) {
		t.Fatalf("codex roots = %v", codex.lastRoots)
	}

	head := e.head()
	if head == nil || head.Commit.Generation != 1 || !head.Commit.Bootstrap {
		t.Fatalf("head = %+v", head)
	}
	if head.Commit.HostDisplayName != "Host A" || head.Commit.BabelVersion != "test" {
		t.Fatalf("commit provenance = %+v", head.Commit)
	}
	if head.Key != res.CommitKey || head.CommitDigest != res.CommitDigest {
		t.Fatalf("result commit %s/%s does not match head %s/%s", res.CommitKey, res.CommitDigest, head.Key, head.CommitDigest)
	}

	entries := e.entries()
	if len(entries) != 3 {
		t.Fatalf("generation 1 has %d entries, want 3", len(entries))
	}
	for _, en := range entries {
		if en.Encoding != archive.EncodingFull || en.ParentRevision != "" || en.ChainDepth != 0 {
			t.Fatalf("entry %s is not a full revision: %+v", en.RevisionKey, en)
		}
		if en.GenerationAdded != 1 || en.HostID != testHost || en.ManifestSchema != archive.ManifestSchemaVersion {
			t.Fatalf("entry %s envelope = %+v", en.RevisionKey, en)
		}
		if en.Content != en.Object {
			t.Fatalf("full revision %s stores %+v, content %+v", en.RevisionKey, en.Object, en.Content)
		}
		if !en.ContinuationGrade || en.Title == nil || len(en.Completeness) != 1 {
			t.Fatalf("entry %s metadata = %+v", en.RevisionKey, en)
		}
		if got := e.reassemble(entries, en); int64(len(got)) != en.Content.Size {
			t.Fatalf("entry %s reassembled to %d bytes", en.RevisionKey, len(got))
		}
	}

	s1 := newestOf(t, entries, sessionKeyOf("omp", "s1"))
	if !bytes.Equal(e.reassemble(entries, s1), lines(4)) {
		t.Fatal("s1 plaintext does not match the staged source")
	}
	if len(s1.Artifacts) != 1 || s1.Artifacts[0].Path != "notes/a.txt" {
		t.Fatalf("s1 artifacts = %+v", s1.Artifacts)
	}
	if got := e.object(s1.Artifacts[0].Digest); !bytes.Equal(got, []byte("artifact-a")) {
		t.Fatalf("artifact object = %q", got)
	}

	hint := e.hint()
	if hint == nil || hint.Generation != 1 || hint.Commit.Digest != head.CommitDigest {
		t.Fatalf("latest hint = %+v", hint)
	}
}

// (b) An unchanged corpus commits nothing.
func TestPushUnchangedCommitsNothing(t *testing.T) {
	e, _, _ := twoHarnessEnv(t)
	first := e.push()

	res := e.push()
	if res.Changed {
		t.Fatal("unchanged corpus committed a new generation")
	}
	if res.Generation != 1 || res.CommitKey != first.CommitKey {
		t.Fatalf("unchanged result = %+v", res)
	}
	if res.Published != 0 || res.CarriedForward != 3 {
		t.Fatalf("counts published=%d carried=%d", res.Published, res.CarriedForward)
	}
	if keys := e.commitKeys(); len(keys) != 1 {
		t.Fatalf("commit records = %v", keys)
	}
	if head := e.head(); head.Commit.Generation != 1 {
		t.Fatalf("head advanced to %d", head.Commit.Generation)
	}
}

// (c) An appended session publishes a tail-only append-delta revision.
func TestPushAppendPublishesDelta(t *testing.T) {
	e, omp, _ := twoHarnessEnv(t)
	e.push()
	tail := line(5) + line(6)
	omp.appendTo(t, "s1", tail)

	res := e.push()
	if !res.Changed || res.Generation != 2 || res.Bootstrap {
		t.Fatalf("append result = %+v", res)
	}
	if res.Published != 1 || res.CarriedForward != 2 {
		t.Fatalf("counts published=%d carried=%d", res.Published, res.CarriedForward)
	}

	entries := e.entries()
	if len(entries) != 4 {
		t.Fatalf("generation 2 has %d entries, want 4", len(entries))
	}
	key := sessionKeyOf("omp", "s1")
	if n := revisionsOf(entries, key); n != 2 {
		t.Fatalf("s1 has %d revisions, want 2 (history is never dropped)", n)
	}
	rev := newestOf(t, entries, key)
	if rev.Encoding != archive.EncodingAppendDelta || rev.ChainDepth != 1 {
		t.Fatalf("newest s1 revision = %+v", rev)
	}
	if rev.GenerationAdded != 2 {
		t.Fatalf("s1 revision generation_added = %d", rev.GenerationAdded)
	}
	if rev.Object.Size != int64(len(tail)) {
		t.Fatalf("stored object is %d bytes, want the %d-byte tail", rev.Object.Size, len(tail))
	}
	if got := e.object(rev.Object.Digest); string(got) != tail {
		t.Fatalf("stored object = %q, want the tail only", got)
	}
	if rev.Content.Size != int64(len(lines(6))) || rev.Content.Digest != archive.DigestBytes(lines(6)) {
		t.Fatalf("content ref %+v does not describe the reassembled plaintext", rev.Content)
	}
	parent := rev.ParentRevision
	if parent == "" {
		t.Fatal("append-delta revision has no parent")
	}
	prior := archive.SessionKey{Harness: "omp", HostID: testHost, SourceID: "s1"}.Revision(archive.DigestBytes(lines(4)))
	if parent != prior {
		t.Fatalf("parent = %q, want the generation 1 revision %q", parent, prior)
	}
	if got := e.reassemble(entries, rev); !bytes.Equal(got, lines(6)) {
		t.Fatalf("reassembled chain = %q", got)
	}
}

// (d) A rewritten session is not a prefix and publishes a full revision.
func TestPushRewritePublishesFull(t *testing.T) {
	e, omp, _ := twoHarnessEnv(t)
	e.push()
	rewritten := []byte(line(9) + line(10) + line(11) + line(12) + line(13))
	omp.set("s1", rewritten)

	res := e.push()
	if !res.Changed || res.Generation != 2 {
		t.Fatalf("rewrite result = %+v", res)
	}
	entries := e.entries()
	rev := newestOf(t, entries, sessionKeyOf("omp", "s1"))
	if rev.Encoding != archive.EncodingFull || rev.ParentRevision != "" || rev.ChainDepth != 0 {
		t.Fatalf("rewritten session published %+v, want a full revision", rev)
	}
	if rev.Content != rev.Object {
		t.Fatalf("full revision stores %+v, content %+v", rev.Object, rev.Content)
	}
	if got := e.reassemble(entries, rev); !bytes.Equal(got, rewritten) {
		t.Fatalf("reassembled %q", got)
	}
	// A truncation is equally not a prefix.
	omp.set("s1", rewritten[:len(line(9))])
	e.push()
	rev = newestOf(t, e.entries(), sessionKeyOf("omp", "s1"))
	if rev.Encoding != archive.EncodingFull {
		t.Fatalf("truncated session published %s", rev.Encoding)
	}
}

// (e) A chain that reached the bound forces the next revision to be full.
func TestPushBoundsAppendChainDepth(t *testing.T) {
	e := newEnv(t)
	omp := newFakeAdapter("omp", 1)
	omp.set("s1", []byte(line(0)))
	e.add(omp)
	e.push()

	key := sessionKeyOf("omp", "s1")
	for depth := 1; depth <= archive.MaxChainDepth; depth++ {
		omp.appendTo(t, "s1", line(depth))
		e.push()
		rev := newestOf(t, e.entries(), key)
		if rev.Encoding != archive.EncodingAppendDelta || rev.ChainDepth != depth {
			t.Fatalf("append %d published %s at depth %d", depth, rev.Encoding, rev.ChainDepth)
		}
	}

	omp.appendTo(t, "s1", line(archive.MaxChainDepth+1))
	e.push()
	entries := e.entries()
	rev := newestOf(t, entries, key)
	if rev.Encoding != archive.EncodingFull || rev.ChainDepth != 0 || rev.ParentRevision != "" {
		t.Fatalf("revision past the chain bound = %+v", rev)
	}
	if got := e.reassemble(entries, rev); int64(len(got)) != rev.Content.Size {
		t.Fatalf("full revision reassembled to %d bytes", len(got))
	}
	// The bounded chain below it still reassembles.
	prior := entries[0]
	for _, en := range entries {
		if en.ChainDepth == archive.MaxChainDepth {
			prior = en
		}
	}
	if prior.ChainDepth != archive.MaxChainDepth {
		t.Fatal("no revision at the chain bound survived")
	}
	e.reassemble(entries, prior)
}

// (f) A session whose local source disappeared is carried forward.
func TestPushCarriesForwardDeletedSession(t *testing.T) {
	e, omp, _ := twoHarnessEnv(t)
	e.push()
	gone := newestOf(t, e.entries(), sessionKeyOf("omp", "s2"))

	delete(omp.sessions, "s2")
	omp.appendTo(t, "s1", line(5))
	res := e.push()

	if !res.Changed || res.Generation != 2 {
		t.Fatalf("result = %+v", res)
	}
	if got := coverageOf(t, res, "omp"); got.Scanned != 1 || got.Published != 1 || got.CarriedForward != 1 || !got.Complete {
		t.Fatalf("omp coverage = %+v", got)
	}
	if res.CarriedForward != 2 {
		t.Fatalf("carried forward = %d, want s2 and c1", res.CarriedForward)
	}
	entries := e.entries()
	carried := newestOf(t, entries, sessionKeyOf("omp", "s2"))
	if carried.RevisionKey != gone.RevisionKey || carried.GenerationAdded != 1 {
		t.Fatalf("carried entry = %+v, want the generation 1 revision unchanged", carried)
	}
	if carried.Content != gone.Content || carried.Encoding != gone.Encoding {
		t.Fatalf("carried entry content changed: %+v vs %+v", carried, gone)
	}
}

// (g) A persistently unstable source is deferred, never published.
func TestPushDefersUnstableSource(t *testing.T) {
	e, omp, _ := twoHarnessEnv(t)
	e.push()
	stable := newestOf(t, e.entries(), sessionKeyOf("omp", "s2"))

	omp.sessions["s2"].unstable = true
	omp.sessions["s2"].content = append(omp.sessions["s2"].content, line(9)...)
	omp.snapshotCalls = map[string]int{}
	omp.appendTo(t, "s1", line(5))

	res := e.push()
	if !res.Changed || res.Generation != 2 {
		t.Fatalf("result = %+v", res)
	}
	if res.Deferred != 1 {
		t.Fatalf("deferred = %d", res.Deferred)
	}
	cov := coverageOf(t, res, "omp")
	if cov.Scanned != 2 || cov.Published != 1 || cov.Deferred != 1 || cov.Complete {
		t.Fatalf("omp coverage = %+v", cov)
	}
	if len(cov.DeferredReasons) != 1 || !strings.Contains(cov.DeferredReasons[0], "s2: unstable after 3 attempts") {
		t.Fatalf("deferred reasons = %v", cov.DeferredReasons)
	}
	if got := omp.snapshotCalls["s2"]; got != snapshotAttempts {
		t.Fatalf("snapshot attempted %d times, want %d", got, snapshotAttempts)
	}
	if !coverageOf(t, res, "codex").Complete {
		t.Fatal("an unstable omp source made codex coverage incomplete")
	}
	if head := e.head(); head.Commit.BootstrapComplete != true {
		t.Fatal("bootstrap completeness regressed after a later deferral")
	}

	entries := e.entries()
	if n := revisionsOf(entries, sessionKeyOf("omp", "s2")); n != 1 {
		t.Fatalf("deferred session has %d revisions, want its single carried revision", n)
	}
	if carried := newestOf(t, entries, sessionKeyOf("omp", "s2")); carried.RevisionKey != stable.RevisionKey {
		t.Fatalf("deferred session revision = %s, want the carried %s", carried.RevisionKey, stable.RevisionKey)
	}
}

// A failed root enumeration degrades that adapter's coverage without
// aborting the push or inventing an empty scan.
func TestPushDegradesOnDiscoverFailure(t *testing.T) {
	e, _, codex := twoHarnessEnv(t)
	codex.discoverErr = errors.New("root unreadable")

	res := e.push()
	if !res.Changed || res.Generation != 1 {
		t.Fatalf("result = %+v", res)
	}
	cov := coverageOf(t, res, "codex")
	if cov.Complete || cov.Scanned != 0 || len(cov.DeferredReasons) != 1 {
		t.Fatalf("codex coverage = %+v", cov)
	}
	if res.BootstrapComplete {
		t.Fatal("bootstrap reported complete despite an unscanned adapter")
	}
	if len(e.entries()) != 2 {
		t.Fatalf("entries = %d, want only the two scanned omp sessions", len(e.entries()))
	}
}

// (i) A concurrent writer never has its commit record clobbered.
func TestPushConcurrentWriter(t *testing.T) {
	t.Run("foreign record at the same generation yields a later generation", func(t *testing.T) {
		e, _, _ := twoHarnessEnv(t)
		plantedKey, plantedBytes := plantForeignGeneration(t, e, 1)

		res := e.push()
		if !res.Changed || res.Generation != 2 {
			t.Fatalf("result = %+v, want a retry at the next generation", res)
		}
		got := readObject(t, e.backing, plantedKey)
		if !bytes.Equal(got, plantedBytes) {
			t.Fatal("the concurrent writer's commit record was clobbered")
		}
		if keys := e.commitKeys(); len(keys) != 2 {
			t.Fatalf("commit records = %v", keys)
		}
		if head := e.head(); head.Commit.Generation != 2 {
			t.Fatalf("head = %d", head.Commit.Generation)
		}
	})

	t.Run("different bytes at this push's own commit key abort it", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			plant func(raw []byte) []byte
		}{
			{"same size", func(raw []byte) []byte {
				other := slices.Clone(raw)
				other[len(other)-2] ^= 0x20
				return other
			}},
			{"different size", func(raw []byte) []byte { return append(slices.Clone(raw), ' ') }},
		} {
			t.Run(tc.name, func(t *testing.T) {
				// Two identically configured hosts produce byte-identical
				// commit records, which is how this test learns the exact
				// write-once key the next push will target. The bytes are
				// planted over generation 2 so generation 1 stays the
				// verified head throughout.
				twin := envWithPendingChange(t)
				expected := twin.push()
				if expected.Generation != 2 {
					t.Fatalf("twin published generation %d", expected.Generation)
				}
				raw := readObject(t, twin.backing, expected.CommitKey)

				e := envWithPendingChange(t)
				if _, _, err := e.backing.Put(context.Background(), expected.CommitKey, bytes.NewReader(tc.plant(raw))); err != nil {
					t.Fatalf("plant record: %v", err)
				}
				err := e.pushErr()
				if !errors.Is(err, ErrConcurrentWriter) {
					t.Fatalf("push error = %v, want ErrConcurrentWriter", err)
				}
				if head := e.head(); head.Commit.Generation != 1 {
					t.Fatalf("head = %d, want the prior generation to remain current", head.Commit.Generation)
				}
				if hint := e.hint(); hint == nil || hint.Generation != 1 {
					t.Fatalf("latest hint = %+v, want it still naming generation 1", hint)
				}
				if got := readObject(t, e.backing, expected.CommitKey); !bytes.Equal(got, tc.plant(raw)) {
					t.Fatal("the concurrent writer's commit record was clobbered")
				}
			})
		}
	})
}

// envWithPendingChange publishes generation 1 and then appends to one
// session, leaving exactly one revision to publish.
func envWithPendingChange(t *testing.T) *env {
	t.Helper()
	e, omp, _ := twoHarnessEnv(t)
	e.push()
	omp.appendTo(t, "s1", line(5))
	return e
}

// plantForeignGeneration writes a complete, independently verifiable
// generation for this host, as a concurrent writer without a lease would.
func plantForeignGeneration(t *testing.T, e *env, gen uint64) (string, []byte) {
	t.Helper()
	ctx := context.Background()
	entry := archive.ManifestEntry{
		ManifestSchema:  archive.ManifestSchemaVersion,
		Harness:         "omp",
		AdapterSchema:   3,
		HostID:          testHost,
		SourceID:        "foreign",
		SessionKey:      sessionKeyOf("omp", "foreign"),
		RevisionKey:     archive.SessionKey{Harness: "omp", HostID: testHost, SourceID: "foreign"}.Revision(archive.DigestBytes(lines(1))),
		GenerationAdded: gen,
		SnapshotTime:    fixedTime(),
		Encoding:        archive.EncodingFull,
		Content:         archive.ObjectRef{Digest: archive.DigestBytes(lines(1)), Size: int64(len(lines(1)))},
		Object:          archive.ObjectRef{Digest: archive.DigestBytes(lines(1)), Size: int64(len(lines(1)))},
	}
	putBytes(t, e.backing, archive.CASKey(entry.Object.Digest), lines(1))

	built, err := archive.BuildSegments([]archive.ManifestEntry{entry})
	if err != nil {
		t.Fatalf("build segments: %v", err)
	}
	idx := archive.GenerationIndex{
		IndexSchema: archive.IndexSchemaVersion,
		HostID:      testHost,
		Generation:  gen,
		CreatedAt:   fixedTime(),
		Sessions:    1,
		Revisions:   1,
	}
	for _, b := range built {
		putBytes(t, e.backing, archive.CASKey(b.Ref.Object.Digest), b.Bytes)
		idx.Segments = append(idx.Segments, b.Ref)
	}
	idxBytes, err := archive.MarshalCanonical(idx)
	if err != nil {
		t.Fatalf("marshal index: %v", err)
	}
	idxRef := archive.ObjectRef{Digest: archive.DigestBytes(idxBytes), Size: int64(len(idxBytes))}
	putBytes(t, e.backing, archive.CASKey(idxRef.Digest), idxBytes)

	rec := archive.CommitRecord{
		CommitSchema: archive.CommitSchemaVersion,
		HostID:       testHost,
		Generation:   gen,
		CreatedAt:    fixedTime(),
		Index:        idxRef,
		Coverage:     []archive.AdapterCoverage{{Harness: "omp", AdapterSchema: 3, Scanned: 1, Published: 1, Complete: true}},
	}
	raw, err := archive.MarshalCanonical(rec)
	if err != nil {
		t.Fatalf("marshal commit record: %v", err)
	}
	key := archive.CommitKey(testHost, gen, archive.DigestBytes(raw))
	putBytes(t, e.backing, key, raw)
	if _, err := archive.VerifiedHead(ctx, e.backing, testHost); err != nil {
		t.Fatalf("planted generation does not verify: %v", err)
	}
	return key, raw
}

func putBytes(t *testing.T, st objectstore.Store, key string, raw []byte) {
	t.Helper()
	if _, _, err := st.Put(context.Background(), key, bytes.NewReader(raw)); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func readObject(t *testing.T, st objectstore.Store, key string) []byte {
	t.Helper()
	rc, err := st.Read(context.Background(), key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return raw
}
