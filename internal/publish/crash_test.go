package publish

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

// errCrash stands in for the process dying: the publisher observes a failed
// operation and unwinds, exactly as an interrupted push would.
var errCrash = errors.New("simulated crash")

// hookStore interposes on the mutating operations of an object store so a
// test can interrupt a push at an exact point. A `before` failure models a
// crash with the write never reaching the archive; an `after` failure
// models a crash once the write is durable but before the publisher could
// act on it — the two halves of every crash rule in SPEC.md §6.1.
type hookStore struct {
	inner  objectstore.Store
	ops    int
	log    []string
	before func(op, key string, n int) error
	after  func(op, key string, n int) error
}

func (h *hookStore) tick(op, key string) int {
	h.ops++
	h.log = append(h.log, op+" "+key)
	return h.ops
}

func (h *hookStore) fire(fn func(op, key string, n int) error, op, key string, n int) error {
	if fn == nil {
		return nil
	}
	return fn(op, key, n)
}

func (h *hookStore) Put(ctx context.Context, key string, r io.Reader) (bool, int64, error) {
	n := h.tick("put", key)
	if err := h.fire(h.before, "put", key, n); err != nil {
		return false, 0, err
	}
	created, size, err := h.inner.Put(ctx, key, r)
	if err != nil {
		return created, size, err
	}
	if err := h.fire(h.after, "put", key, n); err != nil {
		return created, size, err
	}
	return created, size, nil
}

func (h *hookStore) ReplacePointer(ctx context.Context, key string, data []byte) error {
	n := h.tick("pointer", key)
	if err := h.fire(h.before, "pointer", key, n); err != nil {
		return err
	}
	if err := h.inner.ReplacePointer(ctx, key, data); err != nil {
		return err
	}
	return h.fire(h.after, "pointer", key, n)
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

// interrupt runs one push through a hookStore configured by install and
// requires it to fail.
func (e *env) interrupt(t *testing.T, install func(h *hookStore)) *hookStore {
	t.Helper()
	h := &hookStore{inner: e.backing}
	install(h)
	e.store = h
	defer func() { e.store = e.backing }()
	if err := e.pushErr(); err == nil {
		t.Fatal("interrupted push reported success")
	} else if !errors.Is(err, errCrash) {
		t.Fatalf("interrupted push failed with %v, want the simulated crash", err)
	}
	return h
}

func hasGeneration(keys []string, gen uint64) bool {
	for _, k := range keys {
		if g, _, ok := archive.ParseCommitKey(k); ok && g == gen {
			return true
		}
	}
	return false
}

// requireConsistent asserts the invariant every crash point must preserve:
// the verified head is a complete generation, it is the newest committed
// one, and the latest hint never runs ahead of it.
func (e *env) requireConsistent(t *testing.T) *archive.Head {
	t.Helper()
	head := e.head()
	if head == nil {
		t.Fatal("no verified head survived the interruption")
	}
	keys := e.commitKeys()
	want := uint64(1)
	if hasGeneration(keys, 2) {
		want = 2
	}
	if head.Commit.Generation != want {
		t.Fatalf("verified head is generation %d, want %d for commit records %v", head.Commit.Generation, want, keys)
	}
	if hint := e.hint(); hint != nil && hint.Generation > head.Commit.Generation {
		t.Fatalf("latest hint names generation %d, ahead of the verified head %d", hint.Generation, head.Commit.Generation)
	}
	if _, err := archive.LoadEntries(context.Background(), e.backing, head.Index); err != nil {
		t.Fatalf("verified head does not load: %v", err)
	}
	return head
}

// requireConverged asserts that recovery produced exactly one new verified
// generation holding the expected revisions, and that a further push is a
// no-op.
func (e *env) requireConverged(t *testing.T) {
	t.Helper()
	res := e.push()
	head := e.head()
	if head.Commit.Generation != 2 {
		t.Fatalf("recovery settled on generation %d", head.Commit.Generation)
	}
	if res.Generation != 2 {
		t.Fatalf("recovery push reported generation %d", res.Generation)
	}
	if keys := e.commitKeys(); len(keys) != 2 {
		t.Fatalf("commit records = %v, want exactly one new generation", keys)
	}

	entries := e.entries()
	if len(entries) != 4 {
		t.Fatalf("generation 2 holds %d entries, want 4", len(entries))
	}
	seen := make(map[string]struct{}, len(entries))
	for _, en := range entries {
		if _, dup := seen[en.RevisionKey]; dup {
			t.Fatalf("duplicate revision %s", en.RevisionKey)
		}
		seen[en.RevisionKey] = struct{}{}
	}
	rev := newestOf(t, entries, sessionKeyOf("omp", "s1"))
	if rev.Encoding != archive.EncodingAppendDelta || rev.ChainDepth != 1 || rev.GenerationAdded != 2 {
		t.Fatalf("recovered revision = %+v", rev)
	}
	if got := e.reassemble(entries, rev); string(got) != string(lines(4))+line(5) {
		t.Fatalf("recovered plaintext = %q", got)
	}
	if hint := e.hint(); hint == nil || hint.Generation != 2 || hint.Commit.Digest != head.CommitDigest {
		t.Fatalf("latest hint = %+v, want generation 2", hint)
	}
	if again := e.push(); again.Changed {
		t.Fatal("a settled archive published another generation")
	}
}

// recordMutations returns the mutating operations one clean generation-2
// push performs, in order, so the crash matrix can interrupt each of them.
func recordMutations(t *testing.T) []string {
	t.Helper()
	e := envWithPendingChange(t)
	h := &hookStore{inner: e.backing}
	e.store = h
	if res := e.push(); !res.Changed || res.Generation != 2 {
		t.Fatalf("reference push = %+v", res)
	}
	return h.log
}

// (h) Interrupting any single mutating operation, before or after it
// reaches the archive, must leave a consistent archive and converge to
// exactly one new verified generation on the next push (SPEC.md §6.1).
func TestPushCrashMatrix(t *testing.T) {
	mutations := recordMutations(t)
	if len(mutations) < 4 {
		t.Fatalf("reference push performed only %d mutations: %v", len(mutations), mutations)
	}
	for i, m := range mutations {
		k := i + 1
		label := strings.ReplaceAll(strings.SplitN(m, " ", 2)[1], "/", "_")
		if len(label) > 24 {
			label = label[:24]
		}
		for _, when := range []string{"before", "after"} {
			t.Run(fmt.Sprintf("%s-op%d-%s-%s", when, k, strings.SplitN(m, " ", 2)[0], label), func(t *testing.T) {
				e := envWithPendingChange(t)
				fail := func(_, _ string, n int) error {
					if n == k {
						return errCrash
					}
					return nil
				}
				e.interrupt(t, func(h *hookStore) {
					if when == "before" {
						h.before = fail
					} else {
						h.after = fail
					}
				})
				e.requireConsistent(t)
				e.requireConverged(t)
			})
		}
	}
}

// A crash right after staging, before any object reaches the archive,
// leaves the prior generation current and nothing uncommitted visible.
func TestCrashAfterStagingLeavesPriorGenerationCurrent(t *testing.T) {
	e := envWithPendingChange(t)
	e.interrupt(t, func(h *hookStore) {
		h.before = func(op, key string, n int) error {
			if n == 1 {
				return errCrash
			}
			return nil
		}
	})
	head := e.requireConsistent(t)
	if head.Commit.Generation != 1 {
		t.Fatalf("head = %d", head.Commit.Generation)
	}
	if len(e.entries()) != 3 {
		t.Fatal("an uncommitted revision became visible")
	}
	e.requireConverged(t)
}

// A crash after every object and manifest is durable but before the commit
// record is written leaves the prior committed generation current: the
// uploaded objects are harmless and reused by digest on retry.
func TestCrashBeforeCommitRecordLeavesPriorHeadCurrent(t *testing.T) {
	e := envWithPendingChange(t)
	h := e.interrupt(t, func(h *hookStore) {
		h.before = func(op, key string, n int) error {
			if op == "put" && strings.HasPrefix(key, archive.CommitPrefix(testHost)) {
				return errCrash
			}
			return nil
		}
	})
	if got := h.log[len(h.log)-1]; !strings.Contains(got, archive.CommitPrefix(testHost)) {
		t.Fatalf("last operation was %q, expected the commit-record write", got)
	}
	head := e.requireConsistent(t)
	if head.Commit.Generation != 1 {
		t.Fatalf("head = %d, want the prior generation", head.Commit.Generation)
	}
	if hint := e.hint(); hint == nil || hint.Generation != 1 {
		t.Fatalf("latest hint = %+v", hint)
	}
	if j := loadJournal(e.publisher().stateDir, testHost); j == nil || j.Stage != stageIndex || j.Generation != 2 {
		t.Fatalf("journal = %+v, want the index stage of generation 2", j)
	}
	e.requireConverged(t)
}

// A crash after the commit record is durable but before its read-back
// leaves the new generation discoverable by the verified-record scan, while
// the hint still names the old one.
func TestCrashAfterCommitRecordWriteBeforeReadBack(t *testing.T) {
	e := envWithPendingChange(t)
	e.interrupt(t, func(h *hookStore) {
		h.after = func(op, key string, n int) error {
			if op == "put" && strings.HasPrefix(key, archive.CommitPrefix(testHost)) {
				return errCrash
			}
			return nil
		}
	})
	head := e.requireConsistent(t)
	if head.Commit.Generation != 2 {
		t.Fatalf("head = %d, want the durable new generation", head.Commit.Generation)
	}
	if hint := e.hint(); hint == nil || hint.Generation != 1 {
		t.Fatalf("latest hint = %+v, want it lagging at generation 1", hint)
	}
	// The journal never claimed the commit: only the archive did.
	if j := loadJournal(e.publisher().stateDir, testHost); j == nil || j.Stage != stageIndex {
		t.Fatalf("journal = %+v, want no commit claim", j)
	}
	e.requireConverged(t)
}

// A crash after the commit-record read-back but before pointer replacement
// is a durable publication with a stale hint. The journal short-circuits
// the next push straight to pointer replacement.
func TestCrashAfterReadBackBeforePointerReplacement(t *testing.T) {
	e := envWithPendingChange(t)
	e.interrupt(t, func(h *hookStore) {
		h.before = func(op, key string, n int) error {
			if op == "pointer" {
				return errCrash
			}
			return nil
		}
	})
	head := e.requireConsistent(t)
	if head.Commit.Generation != 2 {
		t.Fatalf("head = %d, want the durable new generation", head.Commit.Generation)
	}
	if hint := e.hint(); hint == nil || hint.Generation != 1 {
		t.Fatalf("latest hint = %+v, want it lagging at generation 1", hint)
	}
	stateDir := e.publisher().stateDir
	j := loadJournal(stateDir, testHost)
	if j == nil || j.Stage != stageCommitted || j.Generation != 2 || j.CommitKey != head.Key {
		t.Fatalf("journal = %+v, want a durable commit claim for generation 2", j)
	}

	res := e.push()
	if res.Changed {
		t.Fatal("recovery republished an already committed generation")
	}
	if j := loadJournal(stateDir, testHost); j != nil {
		t.Fatalf("journal = %+v, want no resumption state once the push settled", j)
	}
	if hint := e.hint(); hint == nil || hint.Generation != 2 || hint.Commit.Digest != head.CommitDigest {
		t.Fatalf("latest hint = %+v, want generation 2", hint)
	}
	if keys := e.commitKeys(); len(keys) != 2 {
		t.Fatalf("commit records = %v", keys)
	}
}

// A corrupt journal is a cold start: publication is idempotent, so losing
// resumption state costs work and never correctness.
func TestCorruptJournalColdStarts(t *testing.T) {
	e := envWithPendingChange(t)
	stateDir := e.publisher().stateDir
	if err := os.WriteFile(journalPath(stateDir, testHost), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt journal: %v", err)
	}
	if j := loadJournal(stateDir, testHost); j != nil {
		t.Fatalf("corrupt journal loaded as %+v", j)
	}
	res := e.push()
	if !res.Changed || res.Generation != 2 {
		t.Fatalf("cold-start push = %+v", res)
	}
	e.requireConsistent(t)
}
