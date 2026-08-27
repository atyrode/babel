package rclonestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/objectstore"
)

// newStore returns a Store addressing a fresh temporary directory as a
// plain-path rclone remote, which exercises the real rclone binary without
// any network, credential, or configuration dependency.
func newStore(t *testing.T) *Store {
	t.Helper()
	if _, err := exec.LookPath("rclone"); err != nil {
		t.Skip("rclone binary not on PATH")
	}
	// Point rclone at an absent config file so the operator's own remotes
	// can never influence the test; plain-path remotes need no config.
	t.Setenv("RCLONE_CONFIG", filepath.Join(t.TempDir(), "absent.conf"))
	return New(t.TempDir())
}

// casKey renders a realistic content-addressed key for synthetic content.
func casKey(content []byte) string {
	sum := sha256.Sum256(content)
	hex := fmt.Sprintf("%x", sum)
	return "cas/sha256/" + hex[:2] + "/" + hex
}

func mustPut(t *testing.T, s *Store, key string, content []byte) {
	t.Helper()
	created, size, err := s.Put(context.Background(), key, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
	if !created {
		t.Fatalf("Put(%q): created=false for a new object", key)
	}
	if size != int64(len(content)) {
		t.Fatalf("Put(%q): size=%d, want %d", key, size, len(content))
	}
}

func readAll(t *testing.T, s *Store, key string) []byte {
	t.Helper()
	rc, err := s.Read(context.Background(), key)
	if err != nil {
		t.Fatalf("Read(%q): %v", key, err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("Read(%q) body: %v", key, err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("Read(%q) close: %v", key, err)
	}
	return got
}

func TestPutStatReadRoundTrip(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	content := []byte("synthetic fixture message one\n")
	key := casKey(content)

	mustPut(t, s, key, content)

	info, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Key != key || info.Size != int64(len(content)) {
		t.Fatalf("Stat = %+v, want key %q size %d", info, key, len(content))
	}
	if got := readAll(t, s, key); !bytes.Equal(got, content) {
		t.Fatalf("Read = %q, want %q", got, content)
	}
}

func TestPutEmptyObject(t *testing.T) {
	s := newStore(t)
	key := casKey(nil)

	created, size, err := s.Put(context.Background(), key, bytes.NewReader(nil))
	if err != nil || !created || size != 0 {
		t.Fatalf("Put empty = (%v, %d, %v), want (true, 0, nil)", created, size, err)
	}
	info, err := s.Stat(context.Background(), key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != 0 {
		t.Fatalf("Stat size = %d, want 0", info.Size)
	}
	if got := readAll(t, s, key); len(got) != 0 {
		t.Fatalf("Read = %q, want empty", got)
	}
}

func TestPutIdempotentRewriteIsSkipped(t *testing.T) {
	s := newStore(t)
	content := []byte("synthetic fixture message two")
	key := casKey(content)
	mustPut(t, s, key, content)

	created, size, err := s.Put(context.Background(), key, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("re-Put: %v", err)
	}
	if created {
		t.Fatal("re-Put: created=true for an existing object")
	}
	if size != int64(len(content)) {
		t.Fatalf("re-Put size = %d, want %d", size, len(content))
	}
	if got := readAll(t, s, key); !bytes.Equal(got, content) {
		t.Fatalf("content changed to %q", got)
	}
}

func TestPutDifferentSizeConflicts(t *testing.T) {
	s := newStore(t)
	content := []byte("synthetic fixture message three")
	key := casKey(content)
	mustPut(t, s, key, content)

	longer := append(append([]byte{}, content...), " extended"...)
	created, _, err := s.Put(context.Background(), key, bytes.NewReader(longer))
	if !errors.Is(err, objectstore.ErrImmutableConflict) {
		t.Fatalf("Put conflicting = %v, want ErrImmutableConflict", err)
	}
	if created {
		t.Fatal("Put conflicting: created=true")
	}
	if got := readAll(t, s, key); !bytes.Equal(got, content) {
		t.Fatalf("existing object was clobbered: %q", got)
	}
}

func TestStatAndReadAbsentKeys(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	present := []byte("synthetic fixture message four")
	mustPut(t, s, casKey(present), present)

	// An absent leaf in an existing directory and an absent directory both
	// map to ErrNotExist.
	absent := []string{
		strings.TrimSuffix(casKey(present), "f") + "0",
		"cas/sha256/zz/" + strings.Repeat("0", 64),
		"hosts/testhost/latest.json",
	}
	for _, key := range absent {
		if _, err := s.Stat(ctx, key); !errors.Is(err, objectstore.ErrNotExist) {
			t.Errorf("Stat(%q) = %v, want ErrNotExist", key, err)
		}
		if _, err := s.Read(ctx, key); !errors.Is(err, objectstore.ErrNotExist) {
			t.Errorf("Read(%q) = %v, want ErrNotExist", key, err)
		}
	}
}

func TestListOrderingAndPrefixFiltering(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	const host = "hosts/testhost/"
	keys := []string{
		host + "latest.json",
		host + "commits/g0000000001-aa.json",
		host + "commits/g0000000001-ab.json",
		host + "commits/g0000000002-aa.json",
		"cas/sha256/aa/" + strings.Repeat("aa", 32),
	}
	for i, key := range keys {
		mustPut(t, s, key, []byte(fmt.Sprintf("synthetic fixture object %d", i)))
	}

	cases := []struct {
		name   string
		prefix string
		want   []string
	}{
		{
			name:   "everything",
			prefix: "",
			want: []string{
				"cas/sha256/aa/" + strings.Repeat("aa", 32),
				host + "commits/g0000000001-aa.json",
				host + "commits/g0000000001-ab.json",
				host + "commits/g0000000002-aa.json",
				host + "latest.json",
			},
		},
		{
			// Ascending key order must place the nested commits before
			// latest.json even though rclone lists a directory's own files
			// before its subdirectories.
			name:   "host subtree ordering",
			prefix: host,
			want: []string{
				host + "commits/g0000000001-aa.json",
				host + "commits/g0000000001-ab.json",
				host + "commits/g0000000002-aa.json",
				host + "latest.json",
			},
		},
		{
			name:   "generation prefix ends mid-filename",
			prefix: host + "commits/g0000000001-",
			want: []string{
				host + "commits/g0000000001-aa.json",
				host + "commits/g0000000001-ab.json",
			},
		},
		{
			name:   "single object prefix ends mid-filename",
			prefix: host + "commits/g0000000001-aa",
			want:   []string{host + "commits/g0000000001-aa.json"},
		},
		{
			name:   "prefix shares a directory name but not a filename",
			prefix: host + "lat",
			want:   []string{host + "latest.json"},
		},
		{
			name:   "unused prefix",
			prefix: "hosts/otherhost/",
			want:   nil,
		},
		{
			name:   "unused mid-name prefix in an existing directory",
			prefix: host + "commits/g0000000003",
			want:   nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			infos, err := s.List(ctx, tc.prefix)
			if err != nil {
				t.Fatalf("List(%q): %v", tc.prefix, err)
			}
			got := make([]string, len(infos))
			for i, info := range infos {
				got[i] = info.Key
				if info.Size == 0 {
					t.Errorf("List(%q): %q reported size 0", tc.prefix, info.Key)
				}
			}
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("List(%q) = %v, want %v", tc.prefix, got, tc.want)
			}
		})
	}
}

func TestReplacePointerOverwrites(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	key := "hosts/testhost/latest.json"

	first := []byte(`{"generation":1,"synthetic":true}`)
	if err := s.ReplacePointer(ctx, key, first); err != nil {
		t.Fatalf("ReplacePointer: %v", err)
	}
	if got := readAll(t, s, key); !bytes.Equal(got, first) {
		t.Fatalf("pointer = %q, want %q", got, first)
	}

	// A shorter replacement must fully replace the object, not overlay it.
	second := []byte(`{"generation":2}`)
	if err := s.ReplacePointer(ctx, key, second); err != nil {
		t.Fatalf("ReplacePointer overwrite: %v", err)
	}
	if got := readAll(t, s, key); !bytes.Equal(got, second) {
		t.Fatalf("pointer = %q, want %q", got, second)
	}
	info, err := s.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Size != int64(len(second)) {
		t.Fatalf("pointer size = %d, want %d", info.Size, len(second))
	}
}

// unseekableReader hides the io.Seeker of its source, forcing Put onto the
// buffering path that determines the size itself.
type unseekableReader struct{ r io.Reader }

func (u unseekableReader) Read(p []byte) (int, error) { return u.r.Read(p) }

func TestPutUnseekableReaderIsBuffered(t *testing.T) {
	s := newStore(t)
	content := []byte("synthetic fixture message five, streamed without a seeker")
	key := casKey(content)

	created, size, err := s.Put(context.Background(), key, unseekableReader{bytes.NewReader(content)})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !created || size != int64(len(content)) {
		t.Fatalf("Put = (%v, %d), want (true, %d)", created, size, len(content))
	}
	if got := readAll(t, s, key); !bytes.Equal(got, content) {
		t.Fatalf("Read = %q, want %q", got, content)
	}
}

func TestPutSeekableReaderStreamsFromCurrentPosition(t *testing.T) {
	s := newStore(t)
	full := []byte("SKIPsynthetic fixture message six")
	src := bytes.NewReader(full)
	if _, err := src.Seek(4, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	want := full[4:]
	key := casKey(want)

	created, size, err := s.Put(context.Background(), key, src)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !created || size != int64(len(want)) {
		t.Fatalf("Put = (%v, %d), want (true, %d)", created, size, len(want))
	}
	if got := readAll(t, s, key); !bytes.Equal(got, want) {
		t.Fatalf("Read = %q, want %q", got, want)
	}
}

func TestReadStreamsLargeObject(t *testing.T) {
	s := newStore(t)
	content := bytes.Repeat([]byte("synthetic fixture bundle line\n"), 40000)
	key := casKey(content)
	mustPut(t, s, key, content)

	if got := readAll(t, s, key); !bytes.Equal(got, content) {
		t.Fatalf("Read returned %d bytes, want %d", len(got), len(content))
	}
}

func TestReadCloseBeforeDrainSucceeds(t *testing.T) {
	s := newStore(t)
	content := bytes.Repeat([]byte("synthetic fixture bundle line\n"), 40000)
	key := casKey(content)
	mustPut(t, s, key, content)

	rc, err := s.Read(context.Background(), key)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if _, err := io.ReadFull(rc, make([]byte, 16)); err != nil {
		t.Fatalf("partial read: %v", err)
	}
	// Abandoning the stream breaks rclone's output pipe; that is the
	// caller's choice, not a store error.
	if err := rc.Close(); err != nil {
		t.Fatalf("early Close: %v", err)
	}
}

func TestInvalidKeysAndPrefixesRejected(t *testing.T) {
	for _, key := range []string{"", "/cas/x", "cas//x", "cas/./x", "cas/../x", "cas/x/", "cas/x\ny"} {
		if err := validateKey(key); err == nil {
			t.Errorf("validateKey(%q) = nil, want error", key)
		}
	}
	for _, key := range []string{"latest.json", "cas/sha256/aa/bb", "hosts/h-1/commits/g0000000001-aa.json"} {
		if err := validateKey(key); err != nil {
			t.Errorf("validateKey(%q) = %v, want nil", key, err)
		}
	}
	for _, prefix := range []string{"", "cas/", "cas/sha256/aa", "hosts/h1/commits/g00"} {
		if err := validatePrefix(prefix); err != nil {
			t.Errorf("validatePrefix(%q) = %v, want nil", prefix, err)
		}
	}
	for _, prefix := range []string{"/cas", "cas//sha256", "cas/../x"} {
		if err := validatePrefix(prefix); err == nil {
			t.Errorf("validatePrefix(%q) = nil, want error", prefix)
		}
	}
}

func TestTargetJoinsRemoteForms(t *testing.T) {
	cases := []struct{ remote, key, want string }{
		{"cellar:bucket/babel/v1", "cas/x", "cellar:bucket/babel/v1/cas/x"},
		{"cellar:bucket/babel/v1/", "cas/x", "cellar:bucket/babel/v1/cas/x"},
		{"cellar:", "cas/x", "cellar:cas/x"},
		{"/srv/babel", "cas/x", "/srv/babel/cas/x"},
		{"/srv/babel", "", "/srv/babel"},
	}
	for _, tc := range cases {
		if got := New(tc.remote).target(tc.key); got != tc.want {
			t.Errorf("New(%q).target(%q) = %q, want %q", tc.remote, tc.key, got, tc.want)
		}
	}
}

func TestStderrTailKeepsBoundedSingleLineExcerpt(t *testing.T) {
	var w stderrTail
	for i := range 50 {
		if _, err := fmt.Fprintf(&w, "NOTICE: noisy diagnostic line %d\n", i); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if _, err := io.WriteString(&w, "ERROR : last error was: directory not found\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	if len(w.buf) > stderrExcerptLimit {
		t.Fatalf("retained %d bytes, want at most %d", len(w.buf), stderrExcerptLimit)
	}
	excerpt := w.excerpt()
	if strings.Contains(excerpt, "\n") {
		t.Errorf("excerpt spans multiple lines: %q", excerpt)
	}
	if !strings.HasSuffix(excerpt, "directory not found") {
		t.Errorf("excerpt lost the final error: %q", excerpt)
	}
	re := &runError{args: []string{"lsjson"}, err: errors.New("exit status 3"), stderr: excerpt}
	if !re.pathAbsent() {
		t.Error("pathAbsent() = false for a directory-not-found failure")
	}
	other := &runError{args: []string{"lsjson"}, err: errors.New("exit status 1"), stderr: "Failed to lsjson: AccessDenied"}
	if other.pathAbsent() {
		t.Error("pathAbsent() = true for a transport failure")
	}
}
