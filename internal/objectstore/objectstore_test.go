package objectstore

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/config"
)

// syntheticAccessKeyID and syntheticSecret are the sentinels every "no
// credential escapes" assertion in this package searches for. They are
// deliberately unmistakable strings: an assertion against a realistic-looking
// key would pass while a partial leak went unnoticed.
const (
	syntheticAccessKeyID = "SYNTHETICACCESSKEYID"
	syntheticSecret      = "SYNTHETICSECRET"
)

func TestDirStoreRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		key  string
		data []byte
	}{
		{name: "flat key", key: "object", data: []byte("sealed bytes")},
		{name: "content addressed key", key: "analysis/rec-1/deadbeef", data: []byte(`{"v":1}`)},
		{name: "deeply nested key", key: "analysis/rec-2/a/b/c/deadbeef", data: []byte("nested")},
		{name: "empty object", key: "analysis/rec-3/e3b0c442", data: []byte{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := NewDirStore(filepath.Join(t.TempDir(), "objects"))
			if err != nil {
				t.Fatalf("NewDirStore: %v", err)
			}
			ctx := context.Background()
			if err := store.Put(ctx, tc.key, tc.data); err != nil {
				t.Fatalf("Put: %v", err)
			}
			got, err := store.Get(ctx, tc.key)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if !bytes.Equal(got, tc.data) {
				t.Fatalf("Get returned %q, want %q", got, tc.data)
			}
		})
	}
}

// A retry of a content-addressed write puts identical bytes under an identical
// key, so the second Put must succeed and leave the object readable.
func TestDirStoreOverwriteWithIdenticalContent(t *testing.T) {
	store, err := NewDirStore(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatalf("NewDirStore: %v", err)
	}
	ctx := context.Background()
	const key = "analysis/rec-1/deadbeef"
	object := []byte("sealed bytes")
	for attempt := 1; attempt <= 3; attempt++ {
		if err := store.Put(ctx, key, object); err != nil {
			t.Fatalf("Put attempt %d: %v", attempt, err)
		}
	}
	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, object) {
		t.Fatalf("Get returned %q, want %q", got, object)
	}
}

// The commit protocol's read-back has to tell "not there yet" from "the store
// is broken", so absence is a distinguishable error rather than any error.
func TestDirStoreGetAbsent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "objects")
	store, err := NewDirStore(root)
	if err != nil {
		t.Fatalf("NewDirStore: %v", err)
	}
	ctx := context.Background()
	if err := store.Put(ctx, "analysis/rec-1/deadbeef", []byte("present")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	cases := []string{
		"analysis/rec-1/cafebabe",
		"analysis/rec-9/deadbeef",
		"absent",
	}
	for _, key := range cases {
		t.Run(key, func(t *testing.T) {
			_, err := store.Get(ctx, key)
			if !errors.Is(err, ErrObjectAbsent) {
				t.Fatalf("Get(%q) error = %v, want ErrObjectAbsent", key, err)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("Get(%q) error %q does not name the key", key, err)
			}
			// The store root is the operator's directory layout and
			// adds nothing a caller can act on.
			if strings.Contains(err.Error(), root) {
				t.Errorf("Get(%q) error %q repeats the store root", key, err)
			}
		})
	}
}

// A filesystem-backed store is the boundary that would actually obey a
// traversal, so every unsafe key is refused by both verbs, before any path is
// joined, and nothing appears outside the root as a result.
func TestDirStoreRejectsUnsafeKeys(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "objects")
	store, err := NewDirStore(root)
	if err != nil {
		t.Fatalf("NewDirStore: %v", err)
	}
	ctx := context.Background()
	cases := []struct {
		name string
		key  string
	}{
		{name: "parent traversal", key: "../escape"},
		{name: "deep traversal", key: "analysis/../../escape"},
		{name: "absolute", key: "/absolute"},
		{name: "empty segment", key: "a//b"},
		{name: "dot segment", key: "a/./b"},
		{name: "dotdot segment", key: "a/../b"},
		{name: "trailing separator", key: "a/b/"},
		{name: "empty key", key: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := store.Put(ctx, tc.key, []byte("payload")); err == nil {
				t.Errorf("Put(%q) succeeded, want a refusal", tc.key)
			}
			if _, err := store.Get(ctx, tc.key); err == nil {
				t.Errorf("Get(%q) succeeded, want a refusal", tc.key)
			}
		})
	}
	// The refusals are only meaningful if nothing was written: walk the
	// root's parent, which a traversal out of the root would land in.
	if files := regularFiles(t, parent); len(files) != 0 {
		t.Fatalf("refused keys created files: %v", files)
	}
}

// A crash during Put must not leave anything a reader could mistake for an
// object, so a completed Put leaves exactly its objects and no staging file.
func TestDirStoreLeavesNoStagingFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "objects")
	store, err := NewDirStore(root)
	if err != nil {
		t.Fatalf("NewDirStore: %v", err)
	}
	ctx := context.Background()
	keys := []string{
		"analysis/rec-1/deadbeef",
		"analysis/rec-1/cafebabe",
		"analysis/rec-2/deadbeef",
	}
	for _, key := range keys {
		if err := store.Put(ctx, key, []byte("sealed "+key)); err != nil {
			t.Fatalf("Put(%q): %v", key, err)
		}
	}
	want := append([]string(nil), keys...)
	sort.Strings(want)
	got := regularFiles(t, root)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("store holds %v, want exactly %v", got, want)
	}
}

// A cancelled context is reported rather than ignored, because the caller that
// cancelled it is entitled to know the write did not happen.
func TestDirStoreHonoursCancelledContext(t *testing.T) {
	store, err := NewDirStore(filepath.Join(t.TempDir(), "objects"))
	if err != nil {
		t.Fatalf("NewDirStore: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, "analysis/rec-1/deadbeef", []byte("payload")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put error = %v, want context.Canceled", err)
	}
	if _, err := store.Get(ctx, "analysis/rec-1/deadbeef"); !errors.Is(err, context.Canceled) {
		t.Errorf("Get error = %v, want context.Canceled", err)
	}
}

func TestPhaseBLocation(t *testing.T) {
	cases := []struct {
		name       string
		repository string
		want       objectLocation
	}{
		{
			name:       "s3 with explicit https and a prefix",
			repository: "s3:https://cellar.services.clever-cloud.com/babel-archive/babel/v1",
			want: objectLocation{
				kind:     locationS3,
				endpoint: "https://cellar.services.clever-cloud.com",
				bucket:   "babel-archive",
				prefix:   "babel/v1-analysis/",
			},
		},
		{
			name:       "s3 with explicit http and a port",
			repository: "s3:http://127.0.0.1:9000/babel-archive/babel/v1",
			want: objectLocation{
				kind:     locationS3,
				endpoint: "http://127.0.0.1:9000",
				bucket:   "babel-archive",
				prefix:   "babel/v1-analysis/",
			},
		},
		{
			name:       "s3 scheme-less means https",
			repository: "s3:cellar.services.clever-cloud.com/babel-archive/babel/v1",
			want: objectLocation{
				kind:     locationS3,
				endpoint: "https://cellar.services.clever-cloud.com",
				bucket:   "babel-archive",
				prefix:   "babel/v1-analysis/",
			},
		},
		{
			name:       "s3 single-component prefix",
			repository: "s3:https://host.example/bucket/restic",
			want: objectLocation{
				kind:     locationS3,
				endpoint: "https://host.example",
				bucket:   "bucket",
				prefix:   "restic-analysis/",
			},
		},
		{
			name:       "s3 at the bucket root takes a name of its own",
			repository: "s3:https://host.example/bucket",
			want: objectLocation{
				kind:     locationS3,
				endpoint: "https://host.example",
				bucket:   "bucket",
				prefix:   "babel-analysis/",
			},
		},
		{
			name:       "s3 trailing slash is not a prefix",
			repository: "s3:https://host.example/bucket/",
			want: objectLocation{
				kind:     locationS3,
				endpoint: "https://host.example",
				bucket:   "bucket",
				prefix:   "babel-analysis/",
			},
		},
		{
			name:       "local absolute path",
			repository: "/srv/babel/repo",
			want:       objectLocation{kind: locationDir, dir: "/srv/babel/repo-analysis"},
		},
		{
			name:       "local absolute path is cleaned before the suffix",
			repository: "/srv/babel/repo/",
			want:       objectLocation{kind: locationDir, dir: "/srv/babel/repo-analysis"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := phaseBLocation(tc.repository)
			if err != nil {
				t.Fatalf("phaseBLocation(%q): %v", tc.repository, err)
			}
			if got != tc.want {
				t.Fatalf("phaseBLocation(%q) = %+v, want %+v", tc.repository, got, tc.want)
			}
		})
	}
}

// The derived prefix has to be provably disjoint from restic's tree, which is
// the property the "-analysis" suffix buys and a "/analysis" child would not.
func TestPhaseBPrefixIsDisjointFromResticTree(t *testing.T) {
	const repository = "s3:https://host.example/bucket/babel/v1"
	location, err := phaseBLocation(repository)
	if err != nil {
		t.Fatalf("phaseBLocation: %v", err)
	}
	const resticTree = "babel/v1/"
	if strings.HasPrefix(location.prefix, resticTree) {
		t.Fatalf("Phase B prefix %q lives inside restic's tree %q", location.prefix, resticTree)
	}
	if strings.HasPrefix(resticTree, location.prefix) {
		t.Fatalf("restic's tree %q lives inside the Phase B prefix %q", resticTree, location.prefix)
	}
}

// Every other restic backend is a bounded, named refusal rather than a silent
// fallback to somewhere the operator did not choose.
func TestPhaseBLocationRejectsUnsupportedSchemes(t *testing.T) {
	cases := []struct {
		repository string
		scheme     string
	}{
		{repository: "gs:bucket/prefix", scheme: "gs:"},
		{repository: "azure:container:/prefix", scheme: "azure:"},
		{repository: "b2:bucket/prefix", scheme: "b2:"},
		{repository: "swift:container:/prefix", scheme: "swift:"},
		{repository: "sftp:user@host:/srv/repo", scheme: "sftp:"},
		{repository: "rest:https://host.example/repo", scheme: "rest:"},
		{repository: "rclone:remote:path", scheme: "rclone:"},
	}
	for _, tc := range cases {
		t.Run(tc.scheme, func(t *testing.T) {
			_, err := phaseBLocation(tc.repository)
			if err == nil {
				t.Fatalf("phaseBLocation(%q) succeeded, want a refusal", tc.repository)
			}
			if !strings.Contains(err.Error(), tc.scheme) {
				t.Errorf("error %q does not name the scheme %q", err, tc.scheme)
			}
			if !strings.Contains(err.Error(), "S3") {
				t.Errorf("error %q does not say what Phase B object storage implements", err)
			}
		})
	}
}

func TestPhaseBLocationRejectsMalformedLocators(t *testing.T) {
	cases := []struct {
		name       string
		repository string
	}{
		{name: "empty repository", repository: ""},
		{name: "s3 with no host", repository: "s3:"},
		{name: "s3 with no bucket", repository: "s3:https://host.example"},
		{name: "s3 with a host and no bucket", repository: "s3:host.example/"},
		{name: "relative local path", repository: "relative/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := phaseBLocation(tc.repository); err == nil {
				t.Fatalf("phaseBLocation(%q) succeeded, want a refusal", tc.repository)
			}
		})
	}
}

// Open passes DefaultRegion because the frozen schema carries no region field,
// and it derives everything else from the locator.
func TestOpenS3Store(t *testing.T) {
	cfg := config.Config{
		Repository: "s3:https://cellar.services.clever-cloud.com/babel-archive/babel/v1",
		RepositoryStore: &config.RepositoryStore{
			AccessKeyID:     syntheticAccessKeyID,
			SecretAccessKey: syntheticSecret,
		},
	}
	store, err := Open(cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	s3, ok := store.(*S3Store)
	if !ok {
		t.Fatalf("Open returned %T, want *S3Store", store)
	}
	if s3.endpoint != "https://cellar.services.clever-cloud.com" {
		t.Errorf("endpoint = %q", s3.endpoint)
	}
	if s3.bucket != "babel-archive" {
		t.Errorf("bucket = %q", s3.bucket)
	}
	if s3.prefix != "babel/v1-analysis/" {
		t.Errorf("prefix = %q", s3.prefix)
	}
	if s3.region != DefaultRegion {
		t.Errorf("region = %q, want %q", s3.region, DefaultRegion)
	}
	if s3.client == nil {
		t.Error("client is nil, want a default client with a timeout")
	} else if s3.client.Timeout == 0 {
		t.Error("default client has no timeout")
	}
}

// A local-path repository gets a DirStore beside it, created on the spot.
func TestOpenDirStore(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repo")
	store, err := Open(config.Config{Repository: repository})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dir, ok := store.(*DirStore)
	if !ok {
		t.Fatalf("Open returned %T, want *DirStore", store)
	}
	want := repository + "-analysis"
	if dir.root != want {
		t.Fatalf("root = %q, want %q", dir.root, want)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat object root: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", want)
	}
	if perm := info.Mode().Perm(); perm != dirPerm {
		t.Errorf("object root mode = %#o, want %#o", perm, dirPerm)
	}
}

// A missing credential is named and never echoed: these messages reach a
// terminal, and Open is reached from a status command an operator runs while
// diagnosing exactly this.
func TestOpenS3CredentialErrorsNeverEchoCredentials(t *testing.T) {
	cases := []struct {
		name       string
		credential *config.RepositoryStore
		wantNamed  string
	}{
		{
			name:      "no repository_store at all",
			wantNamed: "repository_store",
		},
		{
			name:       "no access key id",
			credential: &config.RepositoryStore{SecretAccessKey: syntheticSecret},
			wantNamed:  "access_key_id",
		},
		{
			name:       "no secret access key",
			credential: &config.RepositoryStore{AccessKeyID: syntheticAccessKeyID},
			wantNamed:  "secret_access_key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{
				Repository:      "s3:https://host.example/bucket/babel/v1",
				RepositoryStore: tc.credential,
			}
			store, err := Open(cfg)
			if err == nil {
				t.Fatalf("Open succeeded with %T, want a refusal", store)
			}
			message := err.Error()
			if !strings.Contains(message, tc.wantNamed) {
				t.Errorf("error %q does not name %q", message, tc.wantNamed)
			}
			for _, secret := range []string{syntheticAccessKeyID, syntheticSecret} {
				if strings.Contains(message, secret) {
					t.Errorf("error %q contains the credential %q", message, secret)
				}
			}
		})
	}
}

// regularFiles lists every regular file under dir, as slash-separated paths
// relative to it, sorted. It is how the filesystem itself is asked what a
// refused key or a completed Put actually left behind.
func regularFiles(t *testing.T, dir string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		found = append(found, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	sort.Strings(found)
	return found
}
