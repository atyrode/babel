package index_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/index"

	_ "modernc.org/sqlite"
)

// The three fixtures are hand-written synthetic logs, one per harness, small
// enough to state every expected event and shaped to exercise each
// classification the event model produces: reports, claims, tool
// observations, a repository change with paths, verification evidence with
// and without an outcome, and a torn final record. Their vocabulary is
// deliberately disjoint per harness — zeppelin, basilisk, chandelier — so a
// text query's expected result set is unambiguous, and every session shares
// the word "fixture" so a cross-harness query has something to find.
var fixtures = []struct {
	harness  string
	sourceID string
	file     string
}{
	{event.HarnessOMP, "omp-alpha", "omp-session.jsonl"},
	{event.HarnessCodex, "codex-beta", "codex-session.jsonl"},
	{event.HarnessClaude, "claude-gamma", "claude-session.jsonl"},
}

// corpus copies the fixtures into a private directory and returns the
// streams that index them. They are copied rather than indexed in place
// because incremental behavior is observed through size and modification
// time, which a test has to be able to change.
func corpus(t *testing.T) (root string, streams []event.Stream) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "corpus")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create corpus directory: %v", err)
	}
	for _, f := range fixtures {
		content, err := os.ReadFile(filepath.Join("testdata", f.file))
		if err != nil {
			t.Fatalf("read fixture %s: %v", f.file, err)
		}
		path := filepath.Join(root, f.file)
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", path, err)
		}
		streams = append(streams, event.Stream{
			Harness:       f.harness,
			AdapterSchema: 1,
			SourceID:      f.sourceID,
			Path:          path,
		})
	}
	return root, streams
}

// indexedCorpus opens an index in its own directory and indexes the whole
// fixture corpus into it.
func indexedCorpus(t *testing.T) (*index.Index, []event.Stream) {
	t.Helper()
	_, streams := corpus(t)
	idx, err := index.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	total := 0
	for _, s := range streams {
		res, err := idx.IndexSession(context.Background(), s)
		if err != nil {
			t.Fatalf("index %s: %v", s.Path, err)
		}
		if res.Skipped || res.Replaced || res.Events == 0 {
			t.Fatalf("index %s: %+v, want a fresh index with events", s.Path, res)
		}
		total += res.Events
	}
	stats, err := idx.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	// Every event the sessions reported has to be a row: a partially
	// written batch would otherwise leave a silently thin index that only
	// a count catches.
	if stats.Events != total || stats.Sessions != len(streams) {
		t.Fatalf("stats = %+v, want %d events across %d sessions", stats, total, len(streams))
	}
	return idx, streams
}

// key identifies one expected hit as harness plus event index, which is
// stable across runs and readable in a failure message.
func key(h index.Hit) string { return h.Harness + ":" + fmt.Sprint(h.Index) }

func keys(hits []index.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, key(h))
	}
	return out
}

func sortedKeys(hits []index.Hit) []string {
	out := keys(hits)
	slices.Sort(out)
	return out
}

func TestSearchFindsByText(t *testing.T) {
	idx, streams := indexedCorpus(t)
	hits, err := idx.Search(context.Background(), index.Query{Match: "zeppelin cache"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// The four events whose text holds both words. The tool call
	// "go test ./internal/zeppelin/..." holds only one of them, and the
	// other harnesses hold neither.
	if got, want := sortedKeys(hits), []string{"omp:1", "omp:2", "omp:5", "omp:6"}; !slices.Equal(got, want) {
		t.Errorf("hits = %v, want %v", got, want)
	}
	for _, h := range hits {
		if h.Harness != event.HarnessOMP || h.SourceID != "omp-alpha" {
			t.Errorf("hit %s: identity = %s/%s, want omp/omp-alpha", key(h), h.Harness, h.SourceID)
		}
		if h.AdapterSchema != 1 {
			t.Errorf("hit %s: adapter schema = %d, want 1", key(h), h.AdapterSchema)
		}
		if h.Locator.Path != streams[0].Path {
			t.Errorf("hit %s: locator path = %s, want %s", key(h), h.Locator.Path, streams[0].Path)
		}
		if h.Locator.Line == 0 || h.Locator.Digest == "" {
			t.Errorf("hit %s: locator = %+v, want a line and a digest", key(h), h.Locator)
		}
	}
}

func TestSearchStructuredFilters(t *testing.T) {
	idx, _ := indexedCorpus(t)
	partial, intact := true, false

	cases := []struct {
		name  string
		query index.Query
		want  []string
	}{{
		name:  "harness",
		query: index.Query{Harnesses: []string{event.HarnessClaude}},
		want:  []string{"claude:0", "claude:1", "claude:2", "claude:3", "claude:4", "claude:5"},
	}, {
		name:  "several harnesses",
		query: index.Query{Match: "fixture request", Harnesses: []string{event.HarnessOMP, event.HarnessCodex}},
		want:  []string{"codex:1", "omp:1"},
	}, {
		name:  "session",
		query: index.Query{SourceIDs: []string{"codex-beta"}, Kinds: []event.Kind{event.KindAgentClaim}},
		want:  []string{"codex:2"},
	}, {
		name:  "kind",
		query: index.Query{Kinds: []event.Kind{event.KindRepositoryChange}},
		want:  []string{"codex:6", "omp:6"},
	}, {
		name:  "several kinds",
		query: index.Query{Kinds: []event.Kind{event.KindUserReport, event.KindOpaque}},
		want:  []string{"claude:0", "codex:0", "codex:1", "omp:0", "omp:1", "omp:9"},
	}, {
		name:  "tool",
		query: index.Query{Tools: []string{"write"}},
		want:  []string{"omp:5", "omp:6"},
	}, {
		name:  "outcome fail",
		query: index.Query{Outcomes: []string{event.OutcomeFail}},
		want:  []string{"omp:8"},
	}, {
		name:  "outcome error",
		query: index.Query{Outcomes: []string{event.OutcomeError}},
		want:  []string{"claude:5"},
	}, {
		name:  "repository path exact",
		query: index.Query{RepositoryPath: "/synthetic/workspace/beta/basilisk/retry.go"},
		want:  []string{"codex:6"},
	}, {
		name:  "repository path directory",
		query: index.Query{RepositoryPath: "/synthetic/workspace/beta"},
		want:  []string{"codex:6"},
	}, {
		name: "repository path is not a substring filter",
		// One character short of a whole component: a prefix that is not
		// a directory must not match, which is the trap a LIKE or GLOB
		// filter falls into.
		query: index.Query{RepositoryPath: "/synthetic/workspace/bet"},
		want:  nil,
	}, {
		name:  "repository path relative",
		query: index.Query{RepositoryPath: "synthetic/zeppelin"},
		want:  []string{"omp:6"},
	}, {
		name: "time range",
		query: index.Query{
			Since: time.Date(2026, 6, 1, 0, 3, 0, 0, time.UTC),
			Until: time.Date(2026, 6, 1, 0, 6, 0, 0, time.UTC),
		},
		want: []string{"codex:3", "codex:4", "codex:5"},
	}, {
		name: "time range excludes events with no recorded time",
		// Every opaque session-header event has no timestamp, so a
		// temporal filter that spans the whole corpus still omits them.
		query: index.Query{
			Since: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Kinds: []event.Kind{event.KindOpaque},
		},
		want: nil,
	}, {
		name:  "partial records",
		query: index.Query{Partial: &partial},
		want:  []string{"omp:9"},
	}, {
		name:  "intact records of one session",
		query: index.Query{Partial: &intact, SourceIDs: []string{"omp-alpha"}, Kinds: []event.Kind{event.KindOpaque}},
		want:  []string{"omp:0"},
	}, {
		name: "text and kind and harness",
		query: index.Query{
			Match:     "synthetic",
			Kinds:     []event.Kind{event.KindVerificationEvidence},
			Harnesses: []string{event.HarnessOMP, event.HarnessCodex},
		},
		want: []string{"codex:4", "omp:4", "omp:8"},
	}, {
		name:  "text and outcome",
		query: index.Query{Match: "chandelier", Outcomes: []string{event.OutcomeError}},
		want:  []string{"claude:5"},
	}, {
		name:  "text and partial",
		query: index.Query{Match: "zeppelin", Partial: &partial},
		want:  []string{"omp:9"},
	}, {
		name: "text and tool and time range",
		query: index.Query{
			Match: "zeppelin",
			Tools: []string{"bash"},
			Since: time.Date(2026, 5, 1, 0, 6, 0, 0, time.UTC),
		},
		want: []string{"omp:7", "omp:8"},
	}, {
		name:  "filters that intersect to nothing",
		query: index.Query{Match: "zeppelin", Harnesses: []string{event.HarnessClaude}},
		want:  nil,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.query.Limit = index.MaxLimit
			hits, err := idx.Search(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if got := sortedKeys(hits); !slices.Equal(got, tc.want) {
				t.Errorf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestHitLocatorRecoversItsRecord is the provenance contract of §4.3 and
// §5.4: a hit is only evidence if its locator gets back to the bytes. Every
// hit in the corpus is checked, not a sample, because a locator that is
// right for classified records and wrong for degraded ones would still look
// correct in a spot check.
func TestHitLocatorRecoversItsRecord(t *testing.T) {
	idx, _ := indexedCorpus(t)
	hits, err := idx.Search(context.Background(), index.Query{Limit: index.MaxLimit})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits to verify")
	}
	for _, h := range hits {
		record, line := recordAt(t, h.Locator)
		sum := sha256.Sum256(record)
		if got := hex.EncodeToString(sum[:]); got != h.Locator.Digest {
			t.Errorf("hit %s: record at offset %d hashes to %s, want %s",
				key(h), h.Locator.ByteOffset, got, h.Locator.Digest)
		}
		if line != h.Locator.Line {
			t.Errorf("hit %s: offset %d is on line %d, want %d",
				key(h), h.Locator.ByteOffset, line, h.Locator.Line)
		}
	}
}

// recordAt reads the record a locator addresses and the line number its byte
// offset actually falls on, so the two halves of the locator are checked
// against each other rather than against themselves.
func recordAt(t *testing.T, loc event.Locator) (record []byte, line int) {
	t.Helper()
	file, err := os.Open(loc.Path)
	if err != nil {
		t.Fatalf("open %s: %v", loc.Path, err)
	}
	defer file.Close()

	prefix := make([]byte, loc.ByteOffset)
	if _, err := io.ReadFull(file, prefix); err != nil {
		t.Fatalf("read %s up to offset %d: %v", loc.Path, loc.ByteOffset, err)
	}
	line = 1
	for _, b := range prefix {
		if b == '\n' {
			line++
		}
	}
	record, err = bufio.NewReader(file).ReadBytes('\n')
	if err != nil && err != io.EOF {
		t.Fatalf("read record at %s:%d: %v", loc.Path, loc.ByteOffset, err)
	}
	// The digest covers the record, not its terminator, and a log captured
	// mid-write has no terminator on its final record.
	record = bytes.TrimSuffix(record, []byte("\n"))
	record = bytes.TrimSuffix(record, []byte("\r"))
	return record, line
}

func TestReindexSkipsUnchangedSessionAndReplacesChanged(t *testing.T) {
	idx, streams := indexedCorpus(t)
	ctx := context.Background()
	target := streams[0]

	before, err := idx.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	// An unchanged session is skipped on the strength of its recorded size
	// and modification time. Rewriting the file's bytes while preserving
	// both is how the test proves the file was not read again rather than
	// merely that the rows came out the same.
	info, err := os.Stat(target.Path)
	if err != nil {
		t.Fatalf("stat %s: %v", target.Path, err)
	}
	original, err := os.ReadFile(target.Path)
	if err != nil {
		t.Fatalf("read %s: %v", target.Path, err)
	}
	decoy := append([]byte(nil), original...)
	copy(decoy, strings.Repeat("x", 32))
	if err := os.WriteFile(target.Path, decoy, 0o600); err != nil {
		t.Fatalf("rewrite %s: %v", target.Path, err)
	}
	if err := os.Chtimes(target.Path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore mtime of %s: %v", target.Path, err)
	}

	res, err := idx.IndexSession(ctx, target)
	if err != nil {
		t.Fatalf("reindex unchanged: %v", err)
	}
	if !res.Skipped || res.Replaced {
		t.Errorf("reindex of an unchanged session = %+v, want skipped", res)
	}
	after, err := idx.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if after.Events != before.Events || after.Sessions != before.Sessions {
		t.Errorf("stats after skip = %+v, want %+v", after, before)
	}

	// A changed session is re-read and replaced. Appending one record adds
	// events without changing the ones already indexed, so a stale row
	// would show up as a duplicate.
	appended := append([]byte(nil), original...)
	appended = append(appended, []byte(`{"type":"message","id":"a0000009","timestamp":"2026-05-01T00:09:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic fixture follow-up about the zeppelin cache warmer"}]}}`+"\n")...)
	if err := os.WriteFile(target.Path, appended, 0o600); err != nil {
		t.Fatalf("append to %s: %v", target.Path, err)
	}
	future := info.ModTime().Add(time.Second)
	if err := os.Chtimes(target.Path, future, future); err != nil {
		t.Fatalf("advance mtime of %s: %v", target.Path, err)
	}

	res, err = idx.IndexSession(ctx, target)
	if err != nil {
		t.Fatalf("reindex changed: %v", err)
	}
	if res.Skipped || !res.Replaced {
		t.Errorf("reindex of a changed session = %+v, want replaced", res)
	}
	replaced, err := idx.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	// The appended record classifies into exactly one more event, so a
	// stale copy of the session would show up as a count that grew twice.
	if want := before.Events + 1; replaced.Events != want {
		t.Errorf("events after replacement = %d, want %d", replaced.Events, want)
	}
	if replaced.Sessions != before.Sessions {
		t.Errorf("sessions after replacement = %d, want %d", replaced.Sessions, before.Sessions)
	}

	// A term from a record that exists in both versions must be found
	// exactly once: twice would mean the old rows survived.
	hits, err := idx.Search(ctx, index.Query{Match: `"cache warm on startup"`, Limit: index.MaxLimit})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got := keys(hits); !slices.Equal(got, []string{"omp:1"}) {
		t.Errorf("hits for a record present in both versions = %v, want [omp:1]", got)
	}
	hits, err = idx.Search(ctx, index.Query{Match: "follow-up", Limit: index.MaxLimit})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits for the appended record = %v, want one", keys(hits))
	}
	if hits[0].Locator.Line != 10 {
		t.Errorf("appended record locator = %+v, want line 10", hits[0].Locator)
	}
}

// TestFullTextIndexStaysConsistentAcrossReplacement checks the hazard that
// comes with an external-content FTS5 index: its entries are maintained by
// trigger against a content table, so a replaced session could leave tokens
// pointing at rows that no longer hold them. SQLite's own integrity-check is
// the authority on whether it did.
func TestFullTextIndexStaysConsistentAcrossReplacement(t *testing.T) {
	ctx := context.Background()
	_, streams := corpus(t)
	idx, err := index.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	for _, s := range streams {
		if _, err := idx.IndexSession(ctx, s); err != nil {
			t.Fatalf("index %s: %v", s.Path, err)
		}
	}
	// Replace every session twice, so rows are deleted and re-inserted
	// while other sessions' rows sit between them.
	for round := range 2 {
		for _, s := range streams {
			content, err := os.ReadFile(s.Path)
			if err != nil {
				t.Fatalf("read %s: %v", s.Path, err)
			}
			content = append(content, []byte(`{"type":"message","id":"r`+fmt.Sprint(round)+
				`","timestamp":"2026-05-02T00:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic replacement round `+
				fmt.Sprint(round)+`"}]}}`+"\n")...)
			if err := os.WriteFile(s.Path, content, 0o600); err != nil {
				t.Fatalf("rewrite %s: %v", s.Path, err)
			}
			if _, err := idx.IndexSession(ctx, s); err != nil {
				t.Fatalf("reindex %s: %v", s.Path, err)
			}
		}
	}
	path := idx.Path()
	if err := idx.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO events_fts(events_fts) VALUES('integrity-check')`); err != nil {
		t.Fatalf("full-text integrity check: %v", err)
	}
	var events, indexed int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM events_fts`).Scan(&indexed); err != nil {
		t.Fatalf("count indexed events: %v", err)
	}
	if indexed != events {
		t.Errorf("full-text index holds %d rows for %d events", indexed, events)
	}
}

// TestDroppedIndexRebuildsIdenticalResults is what makes the index safe to
// keep in its own file and to delete: it is derived state, so losing it costs
// a re-index and nothing else.
func TestDroppedIndexRebuildsIdenticalResults(t *testing.T) {
	ctx := context.Background()
	_, streams := corpus(t)
	dir := filepath.Join(t.TempDir(), "state")

	query := index.Query{Match: "synthetic fixture", Limit: index.MaxLimit}
	browse := index.Query{Order: index.OrderOldest, Limit: index.MaxLimit}

	first, err := index.Open(dir)
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	for _, s := range streams {
		if _, err := first.IndexSession(ctx, s); err != nil {
			t.Fatalf("index %s: %v", s.Path, err)
		}
	}
	wantMatch, err := first.Search(ctx, query)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	wantBrowse, err := first.Search(ctx, browse)
	if err != nil {
		t.Fatalf("browse: %v", err)
	}
	path := first.Path()
	if err := first.Close(); err != nil {
		t.Fatalf("close index: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("drop index %s: %v", path+suffix, err)
		}
	}

	second, err := index.Open(dir)
	if err != nil {
		t.Fatalf("reopen dropped index: %v", err)
	}
	defer second.Close()
	if stats, err := second.Stats(); err != nil || stats.Events != 0 {
		t.Fatalf("dropped index stats = %+v (err %v), want an empty index", stats, err)
	}
	for _, s := range streams {
		res, err := second.IndexSession(ctx, s)
		if err != nil {
			t.Fatalf("rebuild %s: %v", s.Path, err)
		}
		if res.Skipped {
			t.Errorf("rebuild of %s was skipped; a dropped index has nothing to skip", s.Path)
		}
	}
	gotMatch, err := second.Search(ctx, query)
	if err != nil {
		t.Fatalf("search after rebuild: %v", err)
	}
	if !reflect.DeepEqual(gotMatch, wantMatch) {
		t.Errorf("relevance results after rebuild = %v, want %v", keys(gotMatch), keys(wantMatch))
	}
	gotBrowse, err := second.Search(ctx, browse)
	if err != nil {
		t.Fatalf("browse after rebuild: %v", err)
	}
	if !reflect.DeepEqual(gotBrowse, wantBrowse) {
		t.Errorf("temporal results after rebuild = %v, want %v", keys(gotBrowse), keys(wantBrowse))
	}
}

// TestOpenDiscardsUnusableIndex covers the two ways a cache goes bad. Both
// rebuild rather than fail, which is only correct because no row here is
// durable state.
func TestOpenDiscardsUnusableIndex(t *testing.T) {
	cases := map[string]func(t *testing.T, path string){
		"incompatible schema": func(t *testing.T, path string) {
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatalf("open raw database: %v", err)
			}
			defer db.Close()
			if _, err := db.Exec(`UPDATE meta SET v = '0' WHERE k = 'schema_version'`); err != nil {
				t.Fatalf("downgrade schema version: %v", err)
			}
		},
		"corrupt file": func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("this is not a SQLite database"), 0o600); err != nil {
				t.Fatalf("corrupt index: %v", err)
			}
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			_, streams := corpus(t)
			dir := filepath.Join(t.TempDir(), "state")
			idx, err := index.Open(dir)
			if err != nil {
				t.Fatalf("open index: %v", err)
			}
			if _, err := idx.IndexSession(ctx, streams[0]); err != nil {
				t.Fatalf("index %s: %v", streams[0].Path, err)
			}
			path := idx.Path()
			if err := idx.Close(); err != nil {
				t.Fatalf("close index: %v", err)
			}
			damage(t, path)

			rebuilt, err := index.Open(dir)
			if err != nil {
				t.Fatalf("open damaged index: %v", err)
			}
			defer rebuilt.Close()
			stats, err := rebuilt.Stats()
			if err != nil {
				t.Fatalf("stats: %v", err)
			}
			if stats.Sessions != 0 || stats.Events != 0 {
				t.Fatalf("stats after rebuild = %+v, want an empty index", stats)
			}
			res, err := rebuilt.IndexSession(ctx, streams[0])
			if err != nil {
				t.Fatalf("index into rebuilt: %v", err)
			}
			if res.Skipped || res.Events == 0 {
				t.Errorf("index into rebuilt = %+v, want a fresh index with events", res)
			}
		})
	}
}

// TestOversizedRecordIsFindableByMetadataAndLocator covers the record the
// corpus measurements make unavoidable: a fraction of a percent of records
// exceed a megabyte and the largest observed reach the tens of megabytes.
// Such a record is never parsed, so it is indexed as the degraded evidence it
// is — a bounded excerpt, its metadata, and a locator whose digest still
// covers every byte.
func TestOversizedRecordIsFindableByMetadataAndLocator(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "oversized.jsonl")

	head := `{"type":"message","id":"o0000001","timestamp":"2026-08-01T00:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic oversized xylophone payload `
	filler := strings.Repeat("y", event.MaxRecordBytes)
	oversized := head + filler + `"}]}}`
	content := `{"type":"message","id":"o0000000","timestamp":"2026-08-01T00:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"synthetic ordinary xylophone record"}]}}` + "\n" +
		oversized + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write oversized log: %v", err)
	}

	idx, err := index.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()
	stream := event.Stream{Harness: event.HarnessOMP, AdapterSchema: 1, SourceID: "omp-oversized", Path: path}
	if _, err := idx.IndexSession(ctx, stream); err != nil {
		t.Fatalf("index oversized log: %v", err)
	}

	hits, err := idx.Search(ctx, index.Query{Match: "oversized xylophone"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits = %v, want the one oversized record", keys(hits))
	}
	hit := hits[0]
	if hit.Kind != event.KindOpaque || !hit.Partial {
		t.Errorf("oversized record = kind %s partial %v, want an opaque partial event", hit.Kind, hit.Partial)
	}
	if len(hit.Text) > index.MaxIndexedTextBytes {
		t.Errorf("indexed text is %d bytes, above the %d byte limit", len(hit.Text), index.MaxIndexedTextBytes)
	}
	if len(hit.Text) >= len(oversized)/2 {
		t.Errorf("indexed text is %d bytes of a %d byte record; it must be an excerpt",
			len(hit.Text), len(oversized))
	}
	// The locator is what makes the truncation lossless: its digest covers
	// the whole record, including the megabytes the index never held.
	sum := sha256.Sum256([]byte(oversized))
	if want := hex.EncodeToString(sum[:]); hit.Locator.Digest != want {
		t.Errorf("locator digest = %s, want the digest of the whole record %s", hit.Locator.Digest, want)
	}
	if hit.Locator.Line != 2 {
		t.Errorf("locator = %+v, want line 2", hit.Locator)
	}
	record, _ := recordAt(t, hit.Locator)
	if len(record) != len(oversized) {
		t.Errorf("record recovered through the locator is %d bytes, want %d", len(record), len(oversized))
	}
}

// TestUnstorableTimestampsAreNotIndexedAsTimes covers a hostile transcript:
// a timestamp outside the range a nanosecond epoch can express must leave
// the event without a time rather than at a wrapped instant, because a
// wrapped instant would answer temporal filters with a record that is not in
// the range and hide one that is.
func TestUnstorableTimestampsAreNotIndexedAsTimes(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "timestamps.jsonl")
	record := func(id, timestamp, text string) string {
		return `{"type":"message","id":"` + id + `","timestamp":"` + timestamp +
			`","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	}
	content := record("t1", "9999-12-31T23:59:59Z", "synthetic ocarina far future") +
		record("t2", "0001-01-01T00:00:00Z", "synthetic ocarina far past") +
		record("t3", "2026-08-01T00:00:00Z", "synthetic ocarina ordinary")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	idx, err := index.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()
	if _, err := idx.IndexSession(ctx, event.Stream{
		Harness:       event.HarnessOMP,
		AdapterSchema: 1,
		SourceID:      "omp-timestamps",
		Path:          path,
	}); err != nil {
		t.Fatalf("index: %v", err)
	}

	hits, err := idx.Search(ctx, index.Query{Match: "ocarina", Order: index.OrderOldest, Limit: index.MaxLimit})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want the three records", len(hits))
	}
	// The storable record sorts first and carries its time; the two
	// unstorable ones are last and carry none.
	if hits[0].Time == nil || !hits[0].Time.Equal(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("first hit time = %v, want 2026-08-01", hits[0].Time)
	}
	for _, h := range hits[1:] {
		if h.Time != nil {
			t.Errorf("hit at line %d has time %v, want none", h.Locator.Line, h.Time)
		}
	}

	// A temporal filter spanning everything storable finds only the record
	// whose time could be stored.
	hits, err = idx.Search(ctx, index.Query{
		Match: "ocarina",
		Since: time.Date(1700, 1, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC),
		Limit: index.MaxLimit,
	})
	if err != nil {
		t.Fatalf("search with a time range: %v", err)
	}
	if len(hits) != 1 || hits[0].Locator.Line != 3 {
		t.Fatalf("hits = %v, want only the record on line 3", keys(hits))
	}

	// Bounds outside the storable range saturate instead of wrapping, so
	// they still include that record rather than excluding it.
	hits, err = idx.Search(ctx, index.Query{
		Match: "ocarina",
		Since: time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC),
		Limit: index.MaxLimit,
	})
	if err != nil {
		t.Fatalf("search with saturating bounds: %v", err)
	}
	if len(hits) != 1 || hits[0].Locator.Line != 3 {
		t.Fatalf("hits = %v, want only the record on line 3", keys(hits))
	}
}

// TestAdversarialMatchExpressions is the guarantee that a caller's query
// string is data. Each case must produce either the correct result set or a
// clean sentinel error; none may panic, error inside SQLite, or match on
// FTS5 syntax the caller did not intend.
func TestAdversarialMatchExpressions(t *testing.T) {
	idx, _ := indexedCorpus(t)

	// Every event whose text holds the token "zeppelin".
	allZeppelin := []string{"omp:1", "omp:2", "omp:3", "omp:4", "omp:5", "omp:6", "omp:7", "omp:8", "omp:9"}
	phraseHits := []string{"omp:1", "omp:2", "omp:5", "omp:6"}

	cases := []struct {
		name    string
		match   string
		want    []string
		wantErr error
	}{
		{name: "plain term", match: "zeppelin", want: allZeppelin},
		{name: "quoted phrase", match: `"zeppelin cache"`, want: phraseHits},
		{name: "escaped quote inside a phrase", match: `"zeppelin ""cache"""`, want: phraseHits},
		{name: "unterminated phrase closes at end of input", match: `"zeppelin cache`, want: phraseHits},
		{name: "bare quote", match: `"`, wantErr: index.ErrNoSearchableTerm},
		{name: "prefix", match: "zepp*", want: allZeppelin},
		{name: "negation", match: "zeppelin cache -warms", want: []string{"omp:1", "omp:5", "omp:6"}},
		{name: "negation only", match: "-zeppelin", wantErr: index.ErrNoSearchableTerm},
		// Operators are data: no event contains the word "or", so a query
		// that FTS5 would have read as a disjunction finds nothing.
		{name: "OR is data", match: "zeppelin OR basilisk", want: nil},
		{name: "AND is data", match: "zeppelin AND cache", want: nil},
		{name: "NEAR is data", match: "NEAR(zeppelin cache)", want: nil},
		{name: "column filter is data", match: "kind:opaque", want: nil},
		{name: "initial token operator is data", match: "^zeppelin", want: allZeppelin},
		{name: "parentheses and stars", match: "(zeppelin) cache", want: phraseHits},
		{name: "punctuation only", match: "***", wantErr: index.ErrNoSearchableTerm},
		{name: "operators only", match: "AND OR NOT NEAR", want: nil},
		{name: "empty after control characters", match: "\x00\x01\x02", wantErr: index.ErrNoSearchableTerm},
		// A NUL cannot be a token, so it separates the words around it and
		// the term matches them as a phrase.
		{name: "embedded NUL", match: "zeppelin\x00cache", want: phraseHits},
		{name: "invalid utf-8", match: "\xff\xfezeppelin", want: allZeppelin},
		{name: "unmatched bracket", match: "[zeppelin", want: allZeppelin},
		{name: "very long expression", match: strings.Repeat("zeppelin ", 200), wantErr: index.ErrMatchTooLong},
		{name: "long single token", match: strings.Repeat("z", index.MaxMatchBytes), want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := idx.Search(context.Background(), index.Query{Match: tc.match, Limit: index.MaxLimit})
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				if hits != nil {
					t.Errorf("hits = %v, want none with an error", keys(hits))
				}
				return
			}
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			if got := sortedKeys(hits); !slices.Equal(got, tc.want) {
				t.Errorf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestOrderChangesOrderNotMembership is §5.4's rule made observable:
// retrieval rank orders a page, so a different order returns the same
// evidence in a different sequence. Nothing about a hit's position may look
// like a claim about the corpus, which is why Hit carries no score at all.
func TestOrderChangesOrderNotMembership(t *testing.T) {
	idx, _ := indexedCorpus(t)
	ctx := context.Background()
	base := index.Query{Match: "synthetic", Limit: index.MaxLimit}

	var pages = map[index.Order][]index.Hit{}
	for _, order := range []index.Order{index.OrderRelevance, index.OrderNewest, index.OrderOldest} {
		q := base
		q.Order = order
		hits, err := idx.Search(ctx, q)
		if err != nil {
			t.Fatalf("search %s: %v", order, err)
		}
		if len(hits) == 0 {
			t.Fatalf("search %s returned nothing", order)
		}
		pages[order] = hits
	}
	want := sortedKeys(pages[index.OrderRelevance])
	for order, hits := range pages {
		if got := sortedKeys(hits); !slices.Equal(got, want) {
			t.Errorf("membership under %s = %v, want %v", order, got, want)
		}
	}
	newest, oldest := keys(pages[index.OrderNewest]), keys(pages[index.OrderOldest])
	if slices.Equal(newest, oldest) {
		t.Errorf("newest and oldest returned the same sequence %v; ordering does nothing", newest)
	}

	// A temporal order is monotonic in event time, and the events whose
	// harness recorded no time come last rather than being treated as very
	// old or very new.
	for _, order := range []index.Order{index.OrderNewest, index.OrderOldest} {
		var previous *time.Time
		untimed := false
		for _, h := range pages[order] {
			if h.Time == nil {
				untimed = true
				continue
			}
			if untimed {
				t.Errorf("%s: hit %s carries a time but follows an untimed hit", order, key(h))
			}
			if previous != nil {
				if order == index.OrderNewest && h.Time.After(*previous) {
					t.Errorf("%s: hit %s at %s follows %s", order, key(h), h.Time, previous)
				}
				if order == index.OrderOldest && h.Time.Before(*previous) {
					t.Errorf("%s: hit %s at %s follows %s", order, key(h), h.Time, previous)
				}
			}
			previous = h.Time
		}
		if previous == nil {
			t.Errorf("%s: no hit carried a time", order)
		}
	}

	// Paging is stable: consecutive pages of one order reproduce it
	// exactly, which requires the total ordering the tie-break provides.
	var paged []string
	for offset := 0; ; offset += 4 {
		q := base
		q.Order = index.OrderOldest
		q.Limit, q.Offset = 4, offset
		page, err := idx.Search(ctx, q)
		if err != nil {
			t.Fatalf("page at offset %d: %v", offset, err)
		}
		paged = append(paged, keys(page)...)
		if len(page) < 4 {
			break
		}
	}
	if want := keys(pages[index.OrderOldest]); !slices.Equal(paged, want) {
		t.Errorf("paged sequence = %v, want %v", paged, want)
	}
}

func TestSearchRejectsUnusableQueries(t *testing.T) {
	idx, _ := indexedCorpus(t)
	cases := map[string]struct {
		query index.Query
		want  error
	}{
		"relevance without a match expression": {
			query: index.Query{Order: index.OrderRelevance},
			want:  index.ErrRelevanceWithoutMatch,
		},
		"unknown order":  {query: index.Query{Order: "closest"}, want: index.ErrOrder},
		"limit too high": {query: index.Query{Limit: index.MaxLimit + 1}, want: index.ErrLimit},
		"negative limit": {query: index.Query{Limit: -1}, want: index.ErrLimit},
		"negative offset": {
			query: index.Query{Offset: -1},
			want:  index.ErrOffset,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := idx.Search(context.Background(), tc.query); !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestIndexSessionRejectsUnusableSessions(t *testing.T) {
	_, streams := corpus(t)
	idx, err := index.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	defer idx.Close()

	missing := streams[0]
	missing.Path = filepath.Join(filepath.Dir(missing.Path), "absent.jsonl")
	if _, err := idx.IndexSession(context.Background(), missing); err == nil {
		t.Error("indexing a missing session succeeded")
	}

	unknown := streams[0]
	unknown.Harness = "synthetic-future-harness"
	if _, err := idx.IndexSession(context.Background(), unknown); err == nil {
		t.Error("indexing an unknown harness succeeded")
	}
	if stats, err := idx.Stats(); err != nil || stats.Sessions != 0 || stats.Events != 0 {
		t.Errorf("stats = %+v (err %v), want nothing recorded for a failed index", stats, err)
	}
}

// TestDefaultLimitBoundsAPage keeps the unbounded browse honest: a query
// that names no limit must still return a page, because retrieval feeds
// contexts and review surfaces that cannot hold a corpus.
func TestDefaultLimitBoundsAPage(t *testing.T) {
	idx, _ := indexedCorpus(t)
	hits, err := idx.Search(context.Background(), index.Query{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) > index.DefaultLimit {
		t.Errorf("unbounded query returned %d hits, want at most %d", len(hits), index.DefaultLimit)
	}
	if len(hits) == 0 {
		t.Error("unbounded query returned nothing")
	}
}
