package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
	"github.com/atyrode/babel/internal/objectstore/local"
)

// baseTime anchors every fixture timestamp so canonical bytes are stable.
var baseTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fixture builds committed archive states directly from the frozen archive
// primitives — BuildSegments, MarshalCanonical, Store.Put, ReplacePointer —
// over a real local backend. It never uses the publisher, so the reader is
// tested against the contract rather than against one writer.
//
// Payloads are synthetic bytes; no fixture contains transcript content.
type fixture struct {
	t     *testing.T
	root  string
	st    *local.Store
	plain map[string][]byte // revision key -> reassembled plaintext
	seq   int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	st, err := local.New(root)
	if err != nil {
		t.Fatalf("local.New: %v", err)
	}
	return &fixture{t: t, root: root, st: st, plain: make(map[string][]byte)}
}

// put stores plaintext bytes in the CAS and returns their reference.
func (f *fixture) put(b []byte) archive.ObjectRef {
	f.t.Helper()
	ref := archive.ObjectRef{Digest: archive.DigestBytes(b), Size: int64(len(b))}
	if _, _, err := f.st.Put(context.Background(), archive.CASKey(ref.Digest), bytes.NewReader(b)); err != nil {
		f.t.Fatalf("put object: %v", err)
	}
	return ref
}

// next returns a monotonic snapshot time so revisions order deterministically.
func (f *fixture) next() time.Time {
	f.seq++
	return baseTime.Add(time.Duration(f.seq) * time.Minute)
}

// full builds a full revision, storing its payload.
func (f *fixture) full(harness, host, source string, gen uint64, payload []byte) archive.ManifestEntry {
	f.t.Helper()
	ref := f.put(payload)
	key := archive.SessionKey{Harness: harness, HostID: host, SourceID: source}
	title := "session " + source
	e := archive.ManifestEntry{
		ManifestSchema:    archive.ManifestSchemaVersion,
		Harness:           harness,
		AdapterSchema:     1,
		HostID:            host,
		SourceID:          source,
		SessionKey:        key.String(),
		RevisionKey:       key.Revision(ref.Digest),
		GenerationAdded:   gen,
		SnapshotTime:      f.next(),
		Encoding:          archive.EncodingFull,
		Content:           ref,
		Object:            ref,
		Title:             &title,
		ContinuationGrade: true,
	}
	f.plain[e.RevisionKey] = payload
	return e
}

// delta builds an append-delta revision over parent, storing only the tail.
func (f *fixture) delta(parent archive.ManifestEntry, gen uint64, tail []byte) archive.ManifestEntry {
	f.t.Helper()
	parentPlain, ok := f.plain[parent.RevisionKey]
	if !ok {
		f.t.Fatalf("delta: parent %s has no recorded plaintext", parent.RevisionKey)
	}
	content := append(append([]byte{}, parentPlain...), tail...)
	contentRef := archive.ObjectRef{Digest: archive.DigestBytes(content), Size: int64(len(content))}
	key := archive.SessionKey{Harness: parent.Harness, HostID: parent.HostID, SourceID: parent.SourceID}

	e := parent
	e.GenerationAdded = gen
	e.SnapshotTime = f.next()
	e.Encoding = archive.EncodingAppendDelta
	e.Content = contentRef
	e.Object = f.put(tail)
	e.ParentRevision = parent.RevisionKey
	e.ChainDepth = parent.ChainDepth + 1
	e.RevisionKey = key.Revision(contentRef.Digest)
	f.plain[e.RevisionKey] = content
	return e
}

// artifact stores one artifact object and returns its declared reference.
func (f *fixture) artifact(path string, content []byte) archive.FileRef {
	f.t.Helper()
	ref := f.put(content)
	return archive.FileRef{Path: path, Digest: ref.Digest, Size: ref.Size}
}

// publishOpts varies one publication so tests can plant the anomalies a
// damaged or misbehaving writer would leave behind.
type publishOpts struct {
	// babelVersion changes the commit record's bytes, and therefore its
	// write-once key, without changing the generation.
	babelVersion string
	// noHint leaves the mutable pointer untouched.
	noHint bool
	// skewIndex inflates the index's declared revision total.
	skewIndex int
}

// commit publishes one generation and replaces the latest hint with it, in
// the frozen order: segments, index, commit record, pointer.
func (f *fixture) commit(host string, gen uint64, entries []archive.ManifestEntry) string {
	f.t.Helper()
	return f.publish(host, gen, entries, publishOpts{})
}

// publish writes one generation with the frozen canonical bytes and
// returns its commit-record key.
func (f *fixture) publish(host string, gen uint64, entries []archive.ManifestEntry, o publishOpts) string {
	f.t.Helper()
	ctx := context.Background()
	if o.babelVersion == "" {
		o.babelVersion = "test"
	}

	built, err := archive.BuildSegments(entries)
	if err != nil {
		f.t.Fatalf("BuildSegments: %v", err)
	}
	refs := make([]archive.SegmentRef, 0, len(built))
	for _, b := range built {
		if _, _, err := f.st.Put(ctx, archive.CASKey(b.Ref.Object.Digest), bytes.NewReader(b.Bytes)); err != nil {
			f.t.Fatalf("put segment: %v", err)
		}
		refs = append(refs, b.Ref)
	}
	sessions, revisions := archive.CountEntries(entries)
	idx := archive.GenerationIndex{
		IndexSchema: archive.IndexSchemaVersion,
		HostID:      host,
		Generation:  gen,
		CreatedAt:   baseTime.Add(time.Duration(gen) * time.Hour),
		Segments:    refs,
		Sessions:    sessions,
		Revisions:   revisions + o.skewIndex,
	}
	idxBytes, err := archive.MarshalCanonical(idx)
	if err != nil {
		f.t.Fatalf("marshal index: %v", err)
	}
	idxRef := f.put(idxBytes)

	rec := archive.CommitRecord{
		CommitSchema:      archive.CommitSchemaVersion,
		HostID:            host,
		HostDisplayName:   host + " display",
		Generation:        gen,
		CreatedAt:         idx.CreatedAt,
		Index:             idxRef,
		Coverage:          coverageOf(entries),
		Bootstrap:         gen == 1,
		BootstrapComplete: gen == 1,
		BabelVersion:      o.babelVersion,
	}
	recBytes, err := archive.MarshalCanonical(rec)
	if err != nil {
		f.t.Fatalf("marshal commit record: %v", err)
	}
	recDigest := archive.DigestBytes(recBytes)
	key := archive.CommitKey(host, gen, recDigest)
	if _, _, err := f.st.Put(ctx, key, bytes.NewReader(recBytes)); err != nil {
		f.t.Fatalf("put commit record: %v", err)
	}
	if !o.noHint {
		f.writeHint(host, gen, archive.ObjectRef{Digest: recDigest, Size: int64(len(recBytes))})
	}
	return key
}

// writeHint replaces a host's mutable pointer with an arbitrary target, so
// tests can plant stale, dangling, and tampered hints.
func (f *fixture) writeHint(host string, gen uint64, commit archive.ObjectRef) {
	f.t.Helper()
	raw, err := archive.MarshalCanonical(archive.LatestHint{
		HintSchema: archive.HintSchemaVersion,
		HostID:     host,
		Generation: gen,
		Commit:     commit,
	})
	if err != nil {
		f.t.Fatalf("marshal hint: %v", err)
	}
	if err := f.st.ReplacePointer(context.Background(), archive.LatestKey(host), raw); err != nil {
		f.t.Fatalf("replace pointer: %v", err)
	}
}

func coverageOf(entries []archive.ManifestEntry) []archive.AdapterCoverage {
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.Harness]++
	}
	harnesses := make([]string, 0, len(counts))
	for h := range counts {
		harnesses = append(harnesses, h)
	}
	sort.Strings(harnesses)
	out := make([]archive.AdapterCoverage, 0, len(harnesses))
	for _, h := range harnesses {
		out = append(out, archive.AdapterCoverage{
			Harness: h, AdapterSchema: 1,
			Scanned: counts[h], Published: counts[h], Complete: true,
		})
	}
	return out
}

// path maps a store key onto its backing file so tests can corrupt bytes
// the way a damaged remote would.
func (f *fixture) path(key string) string {
	return filepath.Join(f.root, filepath.FromSlash(key))
}

// flip rewrites one object with different bytes of the same length, the
// corruption that presence-and-size checks cannot see.
func (f *fixture) flip(key string) {
	f.t.Helper()
	p := f.path(key)
	b, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatalf("read %s: %v", key, err)
	}
	if len(b) == 0 {
		f.t.Fatalf("cannot flip empty object %s", key)
	}
	b[len(b)-1] ^= 0xff
	if err := os.WriteFile(p, b, 0o644); err != nil {
		f.t.Fatalf("write %s: %v", key, err)
	}
}

// truncateObject rewrites one object with fewer bytes, a size mismatch.
func (f *fixture) truncateObject(key string) {
	f.t.Helper()
	p := f.path(key)
	b, err := os.ReadFile(p)
	if err != nil {
		f.t.Fatalf("read %s: %v", key, err)
	}
	if err := os.WriteFile(p, b[:len(b)/2], 0o644); err != nil {
		f.t.Fatalf("write %s: %v", key, err)
	}
}

func (f *fixture) remove(key string) {
	f.t.Helper()
	if err := os.Remove(f.path(key)); err != nil {
		f.t.Fatalf("remove %s: %v", key, err)
	}
}

func mustLoad(t *testing.T, st objectstore.Store, hosts ...string) *Catalog {
	t.Helper()
	c, err := Load(context.Background(), st, hosts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

// summary renders the catalog's observable resolution state so a test can
// assert that a hostile pointer changed nothing at all.
func summary(c *Catalog) string {
	var b bytes.Buffer
	for _, h := range c.Hosts() {
		fmt.Fprintf(&b, "host %s g%d commit %s err=%q\n", h.HostID, h.Generation, h.CommitDigest, h.Err)
	}
	for _, s := range c.Sessions() {
		fmt.Fprintf(&b, "session %s newest %s revisions %d\n", s.Key, s.Newest.Key(), s.RevisionCount())
	}
	return b.String()
}

// threeHarnessArchive publishes two hosts and three harnesses: host-a
// carries an OMP session that gained an append-delta revision in
// generation 2 plus a Codex session carried forward, host-b a Claude
// session.
func threeHarnessArchive(t *testing.T) (*fixture, archive.ManifestEntry, archive.ManifestEntry) {
	t.Helper()
	f := newFixture(t)

	ompFull := f.full("omp", "host-a", "sessions/0001-alpha", 1, []byte("omp-alpha-base\n"))
	codex := f.full("codex", "host-a", "rollout/0002-beta", 1, []byte("codex-beta\n"))
	f.commit("host-a", 1, []archive.ManifestEntry{ompFull, codex})

	ompDelta := f.delta(ompFull, 2, []byte("omp-alpha-appended\n"))
	f.commit("host-a", 2, []archive.ManifestEntry{ompFull, ompDelta, codex})

	claude := f.full("claude", "host-b", "projects/gamma/0003", 1, []byte("claude-gamma\n"))
	f.commit("host-b", 1, []archive.ManifestEntry{claude})

	return f, ompFull, ompDelta
}

func TestLoadMergesHostsAndHarnesses(t *testing.T) {
	f, ompFull, ompDelta := threeHarnessArchive(t)
	c := mustLoad(t, f.st)

	sessions := c.Sessions()
	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3", len(sessions))
	}
	keys := make([]string, len(sessions))
	harnesses := map[string]bool{}
	for i, s := range sessions {
		keys[i] = s.Key.String()
		harnesses[s.Key.Harness] = true
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("sessions not in canonical order: %v", keys)
	}
	for _, h := range []string{"omp", "codex", "claude"} {
		if !harnesses[h] {
			t.Errorf("harness %s missing from merged catalog: %v", h, keys)
		}
	}

	a, ok := c.Host("host-a")
	if !ok {
		t.Fatal("host-a absent")
	}
	if a.Generation != 2 || a.Sessions != 2 || a.Revisions != 3 {
		t.Errorf("host-a = g%d %d sessions %d revisions, want g2 2 3", a.Generation, a.Sessions, a.Revisions)
	}
	if a.DisplayName != "host-a display" || len(a.Coverage) != 2 {
		t.Errorf("host-a display %q coverage %d, want display name and 2 adapters", a.DisplayName, len(a.Coverage))
	}
	if a.HintStale || !a.HintPresent {
		t.Errorf("host-a hint present=%t stale=%t, want present and fresh", a.HintPresent, a.HintStale)
	}
	if len(a.Skipped) != 0 || len(a.Anomalies) != 0 || a.Err != "" {
		t.Errorf("host-a degraded: skipped=%v anomalies=%v err=%q", a.Skipped, a.Anomalies, a.Err)
	}
	if b, ok := c.Host("host-b"); !ok || b.Generation != 1 || b.Revisions != 1 {
		t.Errorf("host-b = %+v, want generation 1 with 1 revision", b)
	}

	// A bare session key resolves to the newest committed stable revision.
	bare := ompFull.SessionKey
	rev, err := c.Resolve(bare)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", bare, err)
	}
	if rev.Key() != ompDelta.RevisionKey {
		t.Errorf("bare selector resolved to %s, want the generation-2 revision %s", rev.Key(), ompDelta.RevisionKey)
	}
	if rev.Entry.GenerationAdded != 2 || rev.Generation != 2 {
		t.Errorf("resolved revision added in g%d exposed by g%d, want 2 and 2", rev.Entry.GenerationAdded, rev.Generation)
	}

	sess, ok := c.Session(bare)
	if !ok {
		t.Fatalf("session %q absent", bare)
	}
	if sess.RevisionCount() != 2 || sess.Newest.Key() != ompDelta.RevisionKey {
		t.Errorf("session has %d revisions newest %s, want 2 and %s", sess.RevisionCount(), sess.Newest.Key(), ompDelta.RevisionKey)
	}
	if sess.Revisions[1].Key() != ompFull.RevisionKey {
		t.Errorf("history lost: revisions = %s, %s", sess.Revisions[0].Key(), sess.Revisions[1].Key())
	}
	if sess.Title == nil || *sess.Title == "" {
		t.Error("nullable title should carry the newest revision's value")
	}
	if sess.Workspace != nil {
		t.Error("absent workspace must stay nil rather than being synthesized")
	}

	// An exact revision selector is reproducible and never drifts to the
	// newest revision.
	for range 2 {
		got, err := c.Resolve(ompFull.RevisionKey)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", ompFull.RevisionKey, err)
		}
		if got.Key() != ompFull.RevisionKey || got.Entry.Encoding != archive.EncodingFull {
			t.Errorf("exact selector resolved to %s (%s)", got.Key(), got.Entry.Encoding)
		}
	}
}

func TestResolveUnknownSelectorListsNearMatches(t *testing.T) {
	f, ompFull, ompDelta := threeHarnessArchive(t)
	c := mustLoad(t, f.st)

	// A source-id suffix is not a session key: it does not resolve, but it
	// is what an operator typed, so it must guide them.
	_, err := c.Resolve("0001-alpha")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(suffix) error = %v, want ErrNotFound", err)
	}
	var unknown *UnknownSelectorError
	if !errors.As(err, &unknown) {
		t.Fatalf("error %v is not *UnknownSelectorError", err)
	}
	if len(unknown.NearMatches) != 1 || unknown.NearMatches[0] != ompFull.SessionKey {
		t.Errorf("near matches = %v, want [%s]", unknown.NearMatches, ompFull.SessionKey)
	}

	// A known session with an unknown digest suggests its real revisions.
	sel := ompFull.SessionKey + "@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	_, err = c.Resolve(sel)
	if !errors.As(err, &unknown) {
		t.Fatalf("Resolve(unknown revision) error = %v", err)
	}
	if len(unknown.NearMatches) != 2 {
		t.Fatalf("near matches = %v, want both revision keys", unknown.NearMatches)
	}
	if unknown.NearMatches[0] != ompDelta.RevisionKey {
		t.Errorf("near matches should lead with the newest revision, got %v", unknown.NearMatches)
	}

	if _, err := c.Resolve("omp/host-a/no-such-session"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(missing) error = %v, want ErrNotFound", err)
	}
	if _, err := c.Resolve("not a key@sha256:zz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(malformed) error = %v, want ErrNotFound", err)
	}
}

func TestLoadIgnoresHostileLatestHint(t *testing.T) {
	f, _, _ := threeHarnessArchive(t)
	want := summary(mustLoad(t, f.st))

	// A hint naming a commit record that does not exist.
	f.writeHint("host-a", 99, archive.ObjectRef{
		Digest: archive.DigestBytes([]byte("no such record")),
		Size:   4096,
	})
	c := mustLoad(t, f.st)
	if got := summary(c); got != want {
		t.Errorf("dangling hint changed the catalog:\n%s\nwant:\n%s", got, want)
	}
	a, _ := c.Host("host-a")
	if !a.HintPresent || !a.HintStale || a.HintGeneration != 99 {
		t.Errorf("dangling hint reported as present=%t stale=%t gen=%d", a.HintPresent, a.HintStale, a.HintGeneration)
	}

	// A hint pointing at the real generation with a tampered digest.
	f.writeHint("host-a", 2, archive.ObjectRef{Digest: archive.DigestBytes([]byte("wrong")), Size: 1})
	if got := summary(mustLoad(t, f.st)); got != want {
		t.Errorf("tampered hint changed the catalog:\n%s\nwant:\n%s", got, want)
	}

	// No hint at all: the verified-record scan is the semantics.
	f.remove(archive.LatestKey("host-a"))
	c = mustLoad(t, f.st)
	if got := summary(c); got != want {
		t.Errorf("absent hint changed the catalog:\n%s\nwant:\n%s", got, want)
	}
	if a, _ := c.Host("host-a"); a.HintPresent || a.HintStale {
		t.Errorf("absent hint reported as present=%t stale=%t", a.HintPresent, a.HintStale)
	}
}

func TestLoadFallsBackToOlderVerifiedGeneration(t *testing.T) {
	f, ompFull, ompDelta := threeHarnessArchive(t)

	// Corrupt a manifest segment that only generation 2 references, the way
	// a damaged remote object would: same size, different bytes.
	c := mustLoad(t, f.st)
	head, _ := c.Host("host-a")
	if head.Generation != 2 {
		t.Fatalf("precondition: host-a exposes g%d, want g2", head.Generation)
	}
	rec, _, err := readCommitRecord(context.Background(), f.st, "host-a", head.CommitKey)
	if err != nil {
		t.Fatalf("read head record: %v", err)
	}
	idx, err := readIndex(context.Background(), f.st, rec)
	if err != nil {
		t.Fatalf("read head index: %v", err)
	}
	target := archive.Digest("")
	for _, seg := range idx.Segments {
		if seg.Partition == archive.PartitionOf(ompFull.SessionKey) {
			target = seg.Object.Digest
		}
	}
	if target == "" {
		t.Fatal("generation 2 references no segment for the OMP session")
	}
	f.flip(archive.CASKey(target))

	c = mustLoad(t, f.st)
	a, ok := c.Host("host-a")
	if !ok {
		t.Fatal("host-a absent after corruption")
	}
	if a.Generation != 1 {
		t.Fatalf("host-a exposes g%d, want the older verified g1", a.Generation)
	}
	if len(a.Skipped) != 1 {
		t.Fatalf("skipped generations = %v, want exactly the damaged one", a.Skipped)
	}
	if a.Err != "" {
		t.Errorf("host-a marked unavailable: %q", a.Err)
	}
	if !a.HintStale {
		t.Error("hint still names generation 2 and must be reported as stale")
	}

	sess, ok := c.Session(ompFull.SessionKey)
	if !ok {
		t.Fatal("OMP session absent from the fallback generation")
	}
	if sess.RevisionCount() != 1 || sess.Newest.Key() != ompFull.RevisionKey {
		t.Errorf("fallback session has %d revisions newest %s, want only %s",
			sess.RevisionCount(), sess.Newest.Key(), ompFull.RevisionKey)
	}
	if _, err := c.Resolve(ompDelta.RevisionKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("uncommitted-by-fallback revision resolved: %v", err)
	}
	// Host-b is untouched by host-a's damage.
	if b, ok := c.Host("host-b"); !ok || b.Generation != 1 || b.Err != "" {
		t.Errorf("host-b = %+v, want an intact generation 1", b)
	}
}

func TestLoadHostSelection(t *testing.T) {
	f, _, _ := threeHarnessArchive(t)

	c := mustLoad(t, f.st, "host-b")
	if len(c.Hosts()) != 1 || len(c.Sessions()) != 1 {
		t.Errorf("explicit host selection read %d hosts and %d sessions, want 1 and 1", len(c.Hosts()), len(c.Sessions()))
	}
	if _, err := Load(context.Background(), f.st, []string{"Host-A"}); err == nil {
		t.Error("invalid host ID accepted")
	}

	// An unknown but well-formed host is reported, not fatal.
	c = mustLoad(t, f.st, "host-c")
	h, ok := c.Host("host-c")
	if !ok {
		t.Fatal("requested host absent from the report")
	}
	if h.Generation != 0 || h.Err != "" {
		t.Errorf("host with no commit records = %+v, want generation 0 and no error", h)
	}
}
