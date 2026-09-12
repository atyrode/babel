package catalog

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenLeavesABusyCatalogAlone is the contract behind the web server's
// SIGBUS on macOS: a catalog another connection is writing at the instant
// Open runs is busy, not corrupt, and Open must report that rather than
// remove the file. The writer holds a fresh database under a rollback
// journal with its write lock, which is what Open's switch to WAL collides
// with; SQLite refuses that switch without consulting the busy handler, so
// Open's answer is an error — and the writer's table must still be there
// for it to commit and for the next Open to find.
func TestOpenLeavesABusyCatalogAlone(t *testing.T) {
	dir := t.TempDir()
	writer, err := sql.Open("sqlite", filepath.Join(dir, "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	ctx := context.Background()
	conn, err := writer.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	for _, stmt := range []string{`BEGIN IMMEDIATE`, `CREATE TABLE marker(x INTEGER)`, `INSERT INTO marker VALUES(1)`} {
		if _, err := conn.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	if cache, err := Open(dir); err == nil {
		cache.Close()
		t.Fatal("Open succeeded against a database another connection holds the write lock on")
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		t.Fatalf("the writer's commit failed after Open, so Open touched the file: %v", err)
	}

	cache, err := Open(dir)
	if err != nil {
		t.Fatalf("Open after the writer released the lock: %v", err)
	}
	t.Cleanup(func() { cache.Close() })
	var n int
	if err := cache.db.QueryRow(`SELECT COUNT(*) FROM marker`).Scan(&n); err != nil {
		t.Fatalf("the writer's table did not survive Open: %v", err)
	}
	if n != 1 {
		t.Fatalf("marker rows = %d, want the writer's 1", n)
	}
}

func TestRefreshPopulatesFreshCatalog(t *testing.T) {
	cache, refs := testCache(t, 2)
	calls := 0
	rows, err := cache.Refresh(context.Background(), ompScope, refs, func(ref Ref) (Row, bool) {
		calls++
		return testRow(ref, []byte(`{"selector":"`+ref.Selector+`"}`)), true
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(rows) != 2 {
		t.Fatalf("refresh calls=%d rows=%d, want 2 and 2", calls, len(rows))
	}
	if rows[0].Selector != "omp/session-00" || rows[1].Selector != "omp/session-01" {
		t.Fatalf("rows not returned in selector order: %+v", rows)
	}
}

func TestRefreshDescribesOnlyChangedPrimary(t *testing.T) {
	cache, refs := testCache(t, 2)
	ctx := context.Background()
	describe := func(ref Ref) (Row, bool) { return testRow(ref, []byte(`{"ok":true}`)), true }
	if _, err := cache.Refresh(ctx, ompScope, refs, describe, nil); err != nil {
		t.Fatal(err)
	}

	calls := 0
	if _, err := cache.Refresh(ctx, ompScope, refs, func(ref Ref) (Row, bool) {
		calls++
		return describe(ref)
	}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("unchanged refresh described %d sessions, want 0", calls)
	}

	if err := os.WriteFile(refs[1].PrimaryPath, []byte("primary changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := cache.Refresh(ctx, ompScope, refs, func(ref Ref) (Row, bool) {
		calls++
		return describe(ref)
	}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("one changed primary caused %d describes, want 1", calls)
	}
}

func TestRefreshDeletesVanishedSession(t *testing.T) {
	cache, refs := testCache(t, 2)
	ctx := context.Background()
	describe := func(ref Ref) (Row, bool) { return testRow(ref, []byte(`{"ok":true}`)), true }
	if _, err := cache.Refresh(ctx, ompScope, refs, describe, nil); err != nil {
		t.Fatal(err)
	}

	calls := 0
	rows, err := cache.Refresh(ctx, ompScope, refs[:1], func(ref Ref) (Row, bool) {
		calls++
		return describe(ref)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("vanish refresh described %d unchanged sessions, want 0", calls)
	}
	if len(rows) != 1 || rows[0].Selector != refs[0].Selector {
		t.Fatalf("vanished session remains in rows: %+v", rows)
	}
}

func TestRefreshDropsRowWhenDescribeFails(t *testing.T) {
	cache, refs := testCache(t, 2)
	ctx := context.Background()
	describe := func(ref Ref) (Row, bool) { return testRow(ref, []byte(`{"ok":true}`)), true }
	if _, err := cache.Refresh(ctx, ompScope, refs, describe, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refs[1].PrimaryPath, []byte("primary changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var last Progress
	rows, err := cache.Refresh(ctx, ompScope, refs, func(ref Ref) (Row, bool) {
		return Row{}, false
	}, func(p Progress) { last = p })
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Selector != refs[0].Selector {
		t.Fatalf("failed describe kept a stale row: %+v", rows)
	}
	if last.Total != 1 || last.Described != 1 || last.Failed != 1 {
		t.Fatalf("progress after failure = %+v, want total 1 described 1 failed 1", last)
	}
}

func TestRefreshReportsProgress(t *testing.T) {
	cache, refs := testCache(t, 30)
	var seq []Progress
	_, err := cache.Refresh(context.Background(), ompScope, refs, func(ref Ref) (Row, bool) {
		return testRow(ref, []byte(`{"ok":true}`)), true
	}, func(p Progress) { seq = append(seq, p) })
	if err != nil {
		t.Fatal(err)
	}

	if len(seq) != len(refs)+1 {
		t.Fatalf("progress reports = %d, want %d", len(seq), len(refs)+1)
	}
	if seq[0].Total != len(refs) || seq[0].Described != 0 || seq[0].Harness != "" {
		t.Fatalf("first progress = %+v, want total %d described 0", seq[0], len(refs))
	}
	for i, p := range seq {
		if p.Total != len(refs) {
			t.Fatalf("progress %d total = %d, want %d", i, p.Total, len(refs))
		}
		if p.Described != i {
			t.Fatalf("progress %d described = %d, want monotonic %d", i, p.Described, i)
		}
	}
	final := seq[len(seq)-1]
	if final.Described != len(refs) || final.Failed != 0 || final.Harness != "omp" {
		t.Fatalf("final progress = %+v, want described %d failed 0 harness omp", final, len(refs))
	}
}

func TestRefreshKeepsDescribesAfterCancel(t *testing.T) {
	// Below and above the batch boundary: either way the completed describes
	// must be committed, which is what lets a reloaded page converge.
	for _, stopAfter := range []int{10, refreshBatchSize} {
		t.Run(fmt.Sprintf("after-%d", stopAfter), func(t *testing.T) {
			cache, dir, refs := testCacheDir(t, 30)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			calls := 0
			_, err := cache.Refresh(ctx, ompScope, refs, func(ref Ref) (Row, bool) {
				calls++
				if calls == stopAfter {
					cancel()
				}
				return testRow(ref, []byte(`{"ok":true}`)), true
			}, nil)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled refresh error = %v, want context.Canceled", err)
			}
			if calls != stopAfter {
				t.Fatalf("describes after cancel = %d, want %d", calls, stopAfter)
			}
			if err := cache.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { reopened.Close() })
			redescribed := 0
			rows, err := reopened.Refresh(context.Background(), ompScope, refs, func(ref Ref) (Row, bool) {
				redescribed++
				return Row{}, false
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != stopAfter {
				t.Fatalf("durable rows = %d, want the %d completed describes", len(rows), stopAfter)
			}
			if redescribed != len(refs)-stopAfter {
				t.Fatalf("re-described %d sessions, want only the %d unfinished ones",
					redescribed, len(refs)-stopAfter)
			}
		})
	}
}

func TestRefreshCommitsEveryBatch(t *testing.T) {
	for _, count := range []int{refreshBatchSize - 1, refreshBatchSize + 1} {
		t.Run(fmt.Sprintf("count-%d", count), func(t *testing.T) {
			cache, refs := testCache(t, count)
			ctx := context.Background()
			rows, err := cache.Refresh(ctx, ompScope, refs, func(ref Ref) (Row, bool) {
				return testRow(ref, []byte(`{"ok":true}`)), true
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != count {
				t.Fatalf("rows = %d, want %d", len(rows), count)
			}
			for i, row := range rows {
				if row.Selector != refs[i].Selector {
					t.Fatalf("row %d selector = %q, want %q", i, row.Selector, refs[i].Selector)
				}
			}

			calls := 0
			again, err := cache.Refresh(ctx, ompScope, refs, func(ref Ref) (Row, bool) {
				calls++
				return testRow(ref, []byte(`{"ok":true}`)), true
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || len(again) != count {
				t.Fatalf("second refresh described %d and returned %d rows, want 0 and %d",
					calls, len(again), count)
			}
		})
	}
}

func TestRefreshScopePreservesOtherHarnesses(t *testing.T) {
	cache, dir := testEmptyCache(t)
	ctx := context.Background()
	omp := testRefs(t, dir, "omp", 2)
	codex := testRefs(t, dir, "codex", 2)
	describe := func(ref Ref) (Row, bool) { return testRow(ref, []byte(`{"ok":true}`)), true }
	if _, err := cache.Refresh(ctx, []string{"omp", "codex"}, append(omp, codex...), describe, nil); err != nil {
		t.Fatal(err)
	}

	// A per-harness listing must neither describe nor delete the other harness.
	rows, err := cache.Refresh(ctx, ompScope, omp, describe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("scoped rows = %d, want only the 2 omp rows", len(rows))
	}
	for _, row := range rows {
		if row.Harness != "omp" {
			t.Fatalf("scoped refresh returned %q row %q", row.Harness, row.Selector)
		}
	}
	all, err := cache.readRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 {
		t.Fatalf("catalog holds %d rows after scoped refresh, want all 4: %+v", len(all), all)
	}
}

func TestRefreshEmptyScopePrunesNothing(t *testing.T) {
	cache, dir := testEmptyCache(t)
	ctx := context.Background()
	omp := testRefs(t, dir, "omp", 2)
	describe := func(ref Ref) (Row, bool) { return testRow(ref, []byte(`{"ok":true}`)), true }
	if _, err := cache.Refresh(ctx, ompScope, omp, describe, nil); err != nil {
		t.Fatal(err)
	}

	rows, err := cache.Refresh(ctx, nil, nil, describe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("unscoped refresh with no refs returned %d rows, want the cached 2", len(rows))
	}
}

func TestRefreshScopePrunesVanishedHarness(t *testing.T) {
	cache, dir := testEmptyCache(t)
	ctx := context.Background()
	omp := testRefs(t, dir, "omp", 2)
	codex := testRefs(t, dir, "codex", 2)
	describe := func(ref Ref) (Row, bool) { return testRow(ref, []byte(`{"ok":true}`)), true }
	if _, err := cache.Refresh(ctx, []string{"omp", "codex"}, append(omp, codex...), describe, nil); err != nil {
		t.Fatal(err)
	}

	// Scope is explicit precisely so a harness with zero live refs still prunes.
	rows, err := cache.Refresh(ctx, ompScope, nil, describe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("scoped refresh with no live refs returned %+v, want none", rows)
	}
	all, err := cache.readRows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("catalog holds %d rows, want only the 2 codex rows: %+v", len(all), all)
	}
	for _, row := range all {
		if row.Harness != "codex" {
			t.Fatalf("out-of-scope prune removed %q row %q", row.Harness, row.Selector)
		}
	}
}

func TestOpenSilentlyRebuildsCorruptDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalog.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	cache, err := Open(dir)
	if err != nil {
		t.Fatalf("Open corrupt database: %v", err)
	}
	t.Cleanup(func() { cache.Close() })
	primary := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(primary, []byte("synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := Ref{Selector: "omp/rebuilt", Harness: "omp", SourceID: "rebuilt", PrimaryPath: primary}
	rows, err := cache.Refresh(context.Background(), ompScope, []Ref{ref}, func(ref Ref) (Row, bool) {
		return testRow(ref, []byte(`{"rebuilt":true}`)), true
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Selector != ref.Selector {
		t.Fatalf("rebuilt rows = %+v", rows)
	}
}

func TestRowJSONRoundTripsLosslessly(t *testing.T) {
	cache, refs := testCache(t, 1)
	want := []byte("{ \"selector\" : \"omp/session-00\", \"title\" : null }\n")
	rows, err := cache.Refresh(context.Background(), ompScope, refs, func(ref Ref) (Row, bool) {
		return testRow(ref, want), true
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !bytes.Equal(rows[0].RowJSON, want) {
		t.Fatalf("row_json = %q, want exact %q", rows[0].RowJSON, want)
	}
}

// The publish path reads these columns rather than RowJSON, so a value that
// does not survive the cache is a value other machines never see. The pairing
// that matters is zero against nil: a session measured at zero cost and a
// session nothing measured must not read back the same.
func TestUsageColumnsRoundTripAndKeepAbsenceAbsent(t *testing.T) {
	cache, refs := testCache(t, 2)
	measured, unmeasured := refs[0], refs[1]

	rows, err := cache.Refresh(context.Background(), ompScope, refs, func(ref Ref) (Row, bool) {
		row := testRow(ref, []byte(`{"ok":true}`))
		if ref.Selector == measured.Selector {
			// Deliberately all zeros but all present: the free session is
			// the case a NOT NULL column would make indistinguishable from
			// the unmeasured one below.
			row.CostUSD = new(0.0)
			row.TotalTokens = new(int64(0))
			row.Turns = new(int64(0))
			row.ToolErrors = new(int64(0))
		}
		return row, true
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("cached %d rows, want 2", len(rows))
	}

	bySelector := map[string]Row{}
	for _, row := range rows {
		bySelector[row.Selector] = row
	}
	free := bySelector[measured.Selector]
	if free.CostUSD == nil || *free.CostUSD != 0 {
		t.Errorf("a measured free session read back cost %v, want a present 0", free.CostUSD)
	}
	if free.Turns == nil || *free.Turns != 0 {
		t.Errorf("turns = %v, want a present 0", free.Turns)
	}
	silent := bySelector[unmeasured.Selector]
	if silent.CostUSD != nil || silent.TotalTokens != nil || silent.Turns != nil || silent.ToolErrors != nil {
		t.Errorf("an unmeasured session read back with usage: %+v", silent)
	}

	// A session whose transcript grew is re-described, and the new aggregate
	// replaces the old one rather than being coalesced with it: a session
	// that ran another twenty turns costs more than it did.
	if err := os.WriteFile(measured.PrimaryPath, []byte("synthetic, and longer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated := Row{
		Selector: measured.Selector, Harness: measured.Harness, SourceID: measured.SourceID,
		PrimaryPath: measured.PrimaryPath, RowJSON: []byte(`{"ok":true}`),
		CostUSD: new(3.5), TotalTokens: new(int64(90210)), Turns: new(int64(7)), ToolErrors: new(int64(2)),
	}
	if _, err := cache.Refresh(context.Background(), ompScope, []Ref{measured},
		func(Ref) (Row, bool) { return updated, true }, nil); err != nil {
		t.Fatal(err)
	}
	// And it survives the read path an hourly push actually takes, which
	// describes nothing because nothing changed.
	reread, err := cache.Refresh(context.Background(), ompScope, []Ref{measured},
		func(Ref) (Row, bool) { t.Fatal("an unchanged session was described again"); return Row{}, false }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread) != 1 {
		t.Fatalf("reread %d rows, want 1", len(reread))
	}
	got := reread[0]
	if got.CostUSD == nil || *got.CostUSD != 3.5 || got.TotalTokens == nil || *got.TotalTokens != 90210 ||
		got.Turns == nil || *got.Turns != 7 || got.ToolErrors == nil || *got.ToolErrors != 2 {
		t.Errorf("the cached usage summary did not survive: %+v", got)
	}
}

// TestRepositoryColumnsRoundTripAndKeepTheReasonWithTheAbsence is §4.13's
// binding surviving the cache, and §3's rule surviving with it.
//
// The feed derives its topics from these columns, so a value that does not
// round-trip is a repository whose records go unfiled on every page load but
// the first. The pairing that matters is the identity against the reason: a
// session with no repository must read back with the reason it was given,
// because "unfiled because /tmp is not a checkout" and "unfiled because
// nothing looked" are different answers and only the surface can tell the
// operator which one he is reading.
func TestRepositoryColumnsRoundTripAndKeepTheReasonWithTheAbsence(t *testing.T) {
	cache, refs := testCache(t, 2)
	inRepo, inScratch := refs[0], refs[1]
	identity := "/home/alex/manifold/.git"
	remote := "github.com/atyrode/manifold"
	reason := "not a git repository"

	rows, err := cache.Refresh(context.Background(), ompScope, refs, func(ref Ref) (Row, bool) {
		row := testRow(ref, []byte(`{"ok":true}`))
		if ref.Selector == inRepo.Selector {
			row.RepositoryIdentity, row.RepositoryRemote = &identity, &remote
		} else {
			row.RepositoryReason = &reason
		}
		return row, true
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	bySelector := map[string]Row{}
	for _, row := range rows {
		bySelector[row.Selector] = row
	}

	filed := bySelector[inRepo.Selector]
	if filed.RepositoryIdentity == nil || *filed.RepositoryIdentity != identity {
		t.Errorf("identity read back as %v, want %q", filed.RepositoryIdentity, identity)
	}
	if filed.RepositoryRemote == nil || *filed.RepositoryRemote != remote {
		t.Errorf("remote read back as %v, want %q", filed.RepositoryRemote, remote)
	}
	if filed.RepositoryReason != nil {
		t.Errorf("an observed repository read back with a reason: %q", *filed.RepositoryReason)
	}
	unfiled := bySelector[inScratch.Selector]
	if unfiled.RepositoryIdentity != nil || unfiled.RepositoryRemote != nil {
		t.Errorf("a session with no repository read back with one: %+v", unfiled)
	}
	if unfiled.RepositoryReason == nil || *unfiled.RepositoryReason != reason {
		t.Errorf("reason read back as %v, want %q", unfiled.RepositoryReason, reason)
	}

	// And it survives the read path a page actually takes, which describes
	// nothing because nothing changed.
	reread, err := cache.Refresh(context.Background(), ompScope, []Ref{inRepo},
		func(Ref) (Row, bool) { t.Fatal("an unchanged session was described again"); return Row{}, false }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(reread) != 1 || reread[0].RepositoryIdentity == nil || *reread[0].RepositoryIdentity != identity {
		t.Errorf("the cached repository did not survive: %+v", reread)
	}
}

// TestAPreRepositoryCatalogIsRebuilt is why the schema version moved. A
// catalog written before the repository columns holds no observation at all,
// and reading that absence as "no session belongs to a repository" would file
// the whole corpus as unfiled until every session happened to change.
func TestAPreRepositoryCatalogIsRebuilt(t *testing.T) {
	cache, dir, refs := testCacheDir(t, 1)
	if _, err := cache.Refresh(context.Background(), ompScope, refs,
		func(ref Ref) (Row, bool) { return testRow(ref, []byte(`{"ok":true}`)), true }, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.db.Exec(`UPDATE meta SET v = '4' WHERE k = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	cache.Close()

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen a catalog written by the previous schema: %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
	rows, err := reopened.Refresh(context.Background(), nil, nil,
		func(Ref) (Row, bool) { return Row{}, false }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("the old catalog survived with %d rows, want a clean rebuild", len(rows))
	}
}

var ompScope = []string{"omp"}

func testCache(t *testing.T, count int) (*Cache, []Ref) {
	t.Helper()
	cache, _, refs := testCacheDir(t, count)
	return cache, refs
}

// testCacheDir also hands back the directory so a test can reopen the same
// catalog and prove which rows survived.
func testCacheDir(t *testing.T, count int) (*Cache, string, []Ref) {
	t.Helper()
	cache, dir := testEmptyCache(t)
	return cache, dir, testRefs(t, dir, "omp", count)
}

func testEmptyCache(t *testing.T) (*Cache, string) {
	t.Helper()
	dir := t.TempDir()
	cache, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })
	return cache, dir
}

func testRefs(t *testing.T, dir, harness string, count int) []Ref {
	t.Helper()
	refs := make([]Ref, 0, count)
	for i := range count {
		// Zero padding keeps the selector order the same as the creation order.
		id := fmt.Sprintf("session-%02d", i)
		primary := filepath.Join(dir, harness+"-"+id+".jsonl")
		if err := os.WriteFile(primary, []byte("synthetic\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, Ref{
			Selector:    harness + "/" + id,
			Harness:     harness,
			SourceID:    id,
			PrimaryPath: primary,
		})
	}
	return refs
}

func testRow(ref Ref, rowJSON []byte) Row {
	title := "Synthetic title"
	return Row{
		Selector:          ref.Selector,
		Harness:           ref.Harness,
		SourceID:          ref.SourceID,
		PrimaryPath:       ref.PrimaryPath,
		Title:             &title,
		ContinuationGrade: true,
		RowJSON:           rowJSON,
	}
}
