package catalog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRefreshPopulatesFreshCatalog(t *testing.T) {
	cache, refs := testCache(t, 2)
	calls := 0
	rows, err := cache.Refresh(context.Background(), refs, func(ref Ref) (Row, bool) {
		calls++
		return testRow(ref, []byte(`{"selector":"`+ref.Selector+`"}`)), true
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(rows) != 2 {
		t.Fatalf("refresh calls=%d rows=%d, want 2 and 2", calls, len(rows))
	}
	if rows[0].Selector != "omp/session-0" || rows[1].Selector != "omp/session-1" {
		t.Fatalf("rows not returned in selector order: %+v", rows)
	}
}

func TestRefreshDescribesOnlyChangedPrimary(t *testing.T) {
	cache, refs := testCache(t, 2)
	ctx := context.Background()
	describe := func(ref Ref) (Row, bool) { return testRow(ref, []byte(`{"ok":true}`)), true }
	if _, err := cache.Refresh(ctx, refs, describe); err != nil {
		t.Fatal(err)
	}

	calls := 0
	if _, err := cache.Refresh(ctx, refs, func(ref Ref) (Row, bool) {
		calls++
		return describe(ref)
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("unchanged refresh described %d sessions, want 0", calls)
	}

	if err := os.WriteFile(refs[1].PrimaryPath, []byte("primary changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls = 0
	if _, err := cache.Refresh(ctx, refs, func(ref Ref) (Row, bool) {
		calls++
		return describe(ref)
	}); err != nil {
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
	if _, err := cache.Refresh(ctx, refs, describe); err != nil {
		t.Fatal(err)
	}

	calls := 0
	rows, err := cache.Refresh(ctx, refs[:1], func(ref Ref) (Row, bool) {
		calls++
		return describe(ref)
	})
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
	rows, err := cache.Refresh(context.Background(), []Ref{ref}, func(ref Ref) (Row, bool) {
		return testRow(ref, []byte(`{"rebuilt":true}`)), true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Selector != ref.Selector {
		t.Fatalf("rebuilt rows = %+v", rows)
	}
}

func TestRowJSONRoundTripsLosslessly(t *testing.T) {
	cache, refs := testCache(t, 1)
	want := []byte("{ \"selector\" : \"omp/session-0\", \"title\" : null }\n")
	rows, err := cache.Refresh(context.Background(), refs, func(ref Ref) (Row, bool) {
		return testRow(ref, want), true
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !bytes.Equal(rows[0].RowJSON, want) {
		t.Fatalf("row_json = %q, want exact %q", rows[0].RowJSON, want)
	}
}

func testCache(t *testing.T, count int) (*Cache, []Ref) {
	t.Helper()
	dir := t.TempDir()
	cache, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cache.Close() })
	refs := make([]Ref, 0, count)
	for i := range count {
		id := "session-" + string(rune('0'+i))
		primary := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(primary, []byte("synthetic\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, Ref{
			Selector:    "omp/" + id,
			Harness:     "omp",
			SourceID:    id,
			PrimaryPath: primary,
		})
	}
	return cache, refs
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
