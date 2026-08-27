package local

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/objectstore"
)

// newStore returns a store rooted in a fresh temporary directory.
func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := New(filepath.Join(t.TempDir(), "archive"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return st
}

func put(t *testing.T, st *Store, key, body string) (bool, int64) {
	t.Helper()
	created, n, err := st.Put(context.Background(), key, strings.NewReader(body))
	if err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	return created, n
}

func read(t *testing.T, st *Store, key string) string {
	t.Helper()
	rc, err := st.Read(context.Background(), key)
	if err != nil {
		t.Fatalf("Read(%q): %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read body of %q: %v", key, err)
	}
	return string(b)
}

func TestNewCreatesRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "nested", "archive")
	st, err := New(root)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		t.Fatalf("root not created: stat err %v", err)
	}
	if !filepath.IsAbs(st.Root()) {
		t.Fatalf("Root() = %q, want absolute path", st.Root())
	}
}

func TestPutStatReadRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		key  string
		body string
	}{
		{"top level", "latest.json", `{"synthetic":"pointer"}`},
		{"content addressed", "cas/sha256/aa/" + strings.Repeat("a", 64), "synthetic fixture bundle bytes"},
		{"host commit record", "hosts/h1/commits/g0000000001-" + strings.Repeat("b", 64) + ".json", `{"synthetic":"commit"}`},
		{"empty object", "cas/sha256/00/" + strings.Repeat("0", 64), ""},
	}
	st := newStore(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			created, n := put(t, st, tc.key, tc.body)
			if !created {
				t.Fatalf("created = false, want true for fresh key")
			}
			if n != int64(len(tc.body)) {
				t.Fatalf("Put size = %d, want %d", n, len(tc.body))
			}
			info, err := st.Stat(context.Background(), tc.key)
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			if info.Key != tc.key || info.Size != int64(len(tc.body)) {
				t.Fatalf("Stat = %+v, want key %q size %d", info, tc.key, len(tc.body))
			}
			if got := read(t, st, tc.key); got != tc.body {
				t.Fatalf("Read = %q, want %q", got, tc.body)
			}
		})
	}
}

func TestPutExistingObject(t *testing.T) {
	t.Parallel()
	const key = "cas/sha256/cc/" + "cccc"
	const original = "synthetic fixture message one"

	cases := []struct {
		name        string
		second      string
		wantCreated bool
		wantSize    int64
		wantErr     error
	}{
		{
			name:        "same size is idempotent",
			second:      strings.Repeat("x", len(original)),
			wantCreated: false,
			wantSize:    int64(len(original)),
		},
		{
			name:    "shorter body conflicts",
			second:  original[:len(original)-1],
			wantErr: objectstore.ErrImmutableConflict,
		},
		{
			name:    "longer body conflicts",
			second:  original + "!",
			wantErr: objectstore.ErrImmutableConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newStore(t)
			put(t, st, key, original)

			created, n, err := st.Put(context.Background(), key, strings.NewReader(tc.second))
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Put err = %v, want %v", err, tc.wantErr)
				}
			case err != nil:
				t.Fatalf("Put: %v", err)
			default:
				if created != tc.wantCreated || n != tc.wantSize {
					t.Fatalf("Put = (%v, %d), want (%v, %d)", created, n, tc.wantCreated, tc.wantSize)
				}
			}
			if got := read(t, st, key); got != original {
				t.Fatalf("stored bytes = %q, want the original %q", got, original)
			}
		})
	}
}

func TestNotExistMapping(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	put(t, st, "hosts/h1/latest.json", "synthetic pointer")

	cases := []struct {
		name string
		key  string
	}{
		{"absent object", "cas/sha256/ff/ffff"},
		{"absent under existing dir", "hosts/h1/missing.json"},
		{"directory key", "hosts/h1"},
		{"root-level directory key", "hosts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := st.Stat(context.Background(), tc.key); !errors.Is(err, objectstore.ErrNotExist) {
				t.Fatalf("Stat err = %v, want ErrNotExist", err)
			}
			rc, err := st.Read(context.Background(), tc.key)
			if !errors.Is(err, objectstore.ErrNotExist) {
				if err == nil {
					rc.Close()
				}
				t.Fatalf("Read err = %v, want ErrNotExist", err)
			}
		})
	}
}

func TestPutOverDirectoryKeyConflicts(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	put(t, st, "hosts/h1/latest.json", "synthetic pointer")

	_, _, err := st.Put(context.Background(), "hosts/h1", strings.NewReader("synthetic object"))
	if !errors.Is(err, objectstore.ErrImmutableConflict) {
		t.Fatalf("Put over directory key err = %v, want ErrImmutableConflict", err)
	}
	if got := read(t, st, "hosts/h1/latest.json"); got != "synthetic pointer" {
		t.Fatalf("existing object disturbed: %q", got)
	}
}

// listFixture seeds keys chosen so filesystem walk order differs from key
// order ("a-c" sorts before "a/b") and so a prefix can end mid-filename or
// mid-directory-name ("hosts/h1" must not swallow "hosts/h10").
func listFixture(t *testing.T) *Store {
	t.Helper()
	st := newStore(t)
	for _, key := range []string{
		"hosts/h1/latest.json",
		"a/b",
		"hosts/h10/commits/g0000000001-z.json",
		"cas/sha256/ab/bbbb",
		"a-c",
		"hosts/h1/commits/g0000000002-y.json",
		"cas/sha256/aa/aaaa",
		"hosts/h1/commits/g0000000001-x.json",
	} {
		put(t, st, key, "synthetic "+key)
	}
	return st
}

func TestList(t *testing.T) {
	t.Parallel()
	st := listFixture(t)

	all := []string{
		"a-c",
		"a/b",
		"cas/sha256/aa/aaaa",
		"cas/sha256/ab/bbbb",
		"hosts/h1/commits/g0000000001-x.json",
		"hosts/h1/commits/g0000000002-y.json",
		"hosts/h1/latest.json",
		"hosts/h10/commits/g0000000001-z.json",
	}
	if !sort.StringsAreSorted(all) {
		t.Fatalf("test expectation %v is not in ascending key order", all)
	}

	cases := []struct {
		name   string
		prefix string
		want   []string
	}{
		{"empty prefix lists everything", "", all},
		{"directory prefix", "hosts/h1/", []string{
			"hosts/h1/commits/g0000000001-x.json",
			"hosts/h1/commits/g0000000002-y.json",
			"hosts/h1/latest.json",
		}},
		{"prefix ends mid directory name", "hosts/h1", []string{
			"hosts/h1/commits/g0000000001-x.json",
			"hosts/h1/commits/g0000000002-y.json",
			"hosts/h1/latest.json",
			"hosts/h10/commits/g0000000001-z.json",
		}},
		{"prefix ends mid file name", "hosts/h1/commits/g000000000", []string{
			"hosts/h1/commits/g0000000001-x.json",
			"hosts/h1/commits/g0000000002-y.json",
		}},
		{"prefix selects one object", "hosts/h1/commits/g0000000002", []string{
			"hosts/h1/commits/g0000000002-y.json",
		}},
		{"prefix matching a file exactly", "a-c", []string{"a-c"}},
		{"missing prefix directory", "hosts/h2/commits/", nil},
		{"missing top-level prefix", "nope", nil},
		{"non-matching mid-name prefix", "cas/sha256/ac", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			infos, err := st.List(context.Background(), tc.prefix)
			if err != nil {
				t.Fatalf("List(%q): %v", tc.prefix, err)
			}
			got := make([]string, len(infos))
			for i, in := range infos {
				got[i] = in.Key
				wantSize := int64(len("synthetic " + in.Key))
				if in.Size != wantSize {
					t.Errorf("size of %q = %d, want %d", in.Key, in.Size, wantSize)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("List(%q) = %v, want %v", tc.prefix, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("List(%q) = %v, want %v", tc.prefix, got, tc.want)
				}
			}
		})
	}
}

func TestListEmptyStore(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	infos, err := st.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("List = %v, want empty", infos)
	}
}

func TestListSkipsTempFiles(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	put(t, st, "cas/sha256/aa/aaaa", "synthetic object")
	tmp := filepath.Join(st.Root(), "cas", "sha256", "aa", tmpPrefix+"123")
	if err := os.WriteFile(tmp, []byte("in flight"), 0o644); err != nil {
		t.Fatalf("seed temp file: %v", err)
	}
	infos, err := st.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 1 || infos[0].Key != "cas/sha256/aa/aaaa" {
		t.Fatalf("List = %v, want only the published object", infos)
	}
}

func TestReplacePointer(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	const key = "hosts/h1/latest.json"

	bodies := []string{
		`{"generation":1,"synthetic":true}`,
		`{"generation":2,"synthetic":true,"longer":"payload"}`,
		`{"g":3}`,
	}
	for _, body := range bodies {
		if err := st.ReplacePointer(context.Background(), key, []byte(body)); err != nil {
			t.Fatalf("ReplacePointer: %v", err)
		}
		if got := read(t, st, key); got != body {
			t.Fatalf("Read after replace = %q, want %q", got, body)
		}
		info, err := st.Stat(context.Background(), key)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if info.Size != int64(len(body)) {
			t.Fatalf("Stat size = %d, want %d", info.Size, len(body))
		}
	}

	if err := st.ReplacePointer(context.Background(), key, nil); err != nil {
		t.Fatalf("ReplacePointer(nil): %v", err)
	}
	if got := read(t, st, key); got != "" {
		t.Fatalf("Read after empty replace = %q, want empty", got)
	}
}

func TestInvalidKeyRejection(t *testing.T) {
	t.Parallel()
	st := newStore(t)

	keys := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"absolute", "/etc/passwd"},
		{"parent escape", "../outside"},
		{"embedded parent escape", "cas/../../outside"},
		{"bare parent", ".."},
		{"dot segment", "./cas/aa"},
		{"empty segment", "cas//aa"},
		{"trailing slash", "cas/aa/"},
		{"backslash", `cas\aa`},
		{"nul byte", "cas/a\x00a"},
	}
	for _, tc := range keys {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := st.Put(context.Background(), tc.key, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Put err = %v, want ErrInvalidKey", err)
			}
			if _, err := st.Stat(context.Background(), tc.key); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("Stat err = %v, want ErrInvalidKey", err)
			}
			if rc, err := st.Read(context.Background(), tc.key); !errors.Is(err, ErrInvalidKey) {
				if err == nil {
					rc.Close()
				}
				t.Errorf("Read err = %v, want ErrInvalidKey", err)
			}
			if err := st.ReplacePointer(context.Background(), tc.key, []byte("x")); !errors.Is(err, ErrInvalidKey) {
				t.Errorf("ReplacePointer err = %v, want ErrInvalidKey", err)
			}
		})
	}

	// Rejection must leave nothing behind on disk.
	infos, err := st.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("List = %v, want empty after rejected writes", infos)
	}
}

func TestListPrefixValidation(t *testing.T) {
	t.Parallel()
	st := newStore(t)

	cases := []struct {
		name    string
		prefix  string
		wantErr bool
	}{
		{"empty is valid", "", false},
		{"trailing slash is valid", "hosts/h1/", false},
		{"partial name is valid", "hosts/h1/commits/g00", false},
		{"parent escape", "../", true},
		{"embedded parent escape", "hosts/../..", true},
		{"absolute", "/hosts", true},
		{"empty segment", "hosts//h1", true},
		{"backslash", `hosts\h1`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := st.List(context.Background(), tc.prefix)
			if tc.wantErr != errors.Is(err, ErrInvalidKey) {
				t.Fatalf("List(%q) err = %v, wantErr %v", tc.prefix, err, tc.wantErr)
			}
		})
	}
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()
	st := newStore(t)
	put(t, st, "cas/sha256/aa/aaaa", "synthetic object")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, _, err := st.Put(ctx, "cas/sha256/bb/bbbb", strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put err = %v, want context.Canceled", err)
	}
	if _, err := st.Stat(ctx, "cas/sha256/aa/aaaa"); !errors.Is(err, context.Canceled) {
		t.Errorf("Stat err = %v, want context.Canceled", err)
	}
	if rc, err := st.Read(ctx, "cas/sha256/aa/aaaa"); !errors.Is(err, context.Canceled) {
		if err == nil {
			rc.Close()
		}
		t.Errorf("Read err = %v, want context.Canceled", err)
	}
	if _, err := st.List(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("List err = %v, want context.Canceled", err)
	}
	if err := st.ReplacePointer(ctx, "hosts/h1/latest.json", []byte("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("ReplacePointer err = %v, want context.Canceled", err)
	}
}
