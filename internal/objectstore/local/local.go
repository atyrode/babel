// Package local implements the objectstore.Store port over a plain
// directory tree. Object keys map one-to-one onto slash-separated paths
// beneath the configured root, so the archive stays readable and
// recoverable with ordinary file tools — the local-backend counterpart of
// the direct-rclone recovery path (SPEC.md §6.1).
//
// Durability model: every object is written to a temporary file in its
// destination directory, fsynced, then renamed into place, so a reader
// never observes a partially written object. Parent directories are
// fsynced best-effort; a lost directory entry after a crash costs a
// re-upload, never a corrupt object.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/objectstore"
)

// ErrInvalidKey is returned for keys that cannot be mapped safely onto the
// filesystem: empty, absolute, backslash- or NUL-bearing, or containing an
// empty, "." or ".." path segment.
var ErrInvalidKey = errors.New("objectstore/local: invalid key")

// tmpPrefix names in-flight temporary objects. Listing skips them so a
// concurrent Put is never mistaken for a published object.
const tmpPrefix = ".babel-tmp-"

const (
	dirPerm  fs.FileMode = 0o755
	filePerm fs.FileMode = 0o644
)

// Store is a directory-backed object store.
type Store struct {
	root string
}

var _ objectstore.Store = (*Store)(nil)

// New opens the store rooted at root, creating the directory tree when it
// does not exist. The root is resolved to an absolute path so later
// working-directory changes cannot move the archive.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, errors.New("objectstore/local: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("objectstore/local: resolve root %q: %w", root, err)
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return nil, fmt.Errorf("objectstore/local: create root: %w", err)
	}
	return &Store{root: abs}, nil
}

// Root returns the absolute directory backing the store.
func (s *Store) Root() string { return s.root }

// path maps an already-validated key onto its filesystem path.
func (s *Store) path(key string) string {
	return filepath.Join(s.root, filepath.FromSlash(key))
}

// Put writes an immutable object, honouring the advisory no-clobber
// contract of objectstore.Store: an existing object of the same size is
// left byte-for-byte untouched and reported as created=false, while a
// size mismatch is refused with objectstore.ErrImmutableConflict. The
// reader is fully consumed either way, so callers may always treat it as
// spent.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) (bool, int64, error) {
	if err := validKey(key); err != nil {
		return false, 0, err
	}
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	dst := s.path(key)
	switch fi, err := os.Stat(dst); {
	case err == nil:
		if !fi.Mode().IsRegular() {
			return false, 0, fmt.Errorf("objectstore/local: key %q is not a regular file: %w", key, objectstore.ErrImmutableConflict)
		}
		n, err := io.Copy(io.Discard, r)
		if err != nil {
			return false, 0, fmt.Errorf("objectstore/local: read object %q: %w", key, err)
		}
		if n != fi.Size() {
			return false, 0, fmt.Errorf("objectstore/local: %q exists with size %d, incoming size %d: %w", key, fi.Size(), n, objectstore.ErrImmutableConflict)
		}
		return false, n, nil
	case !errors.Is(err, fs.ErrNotExist):
		return false, 0, fmt.Errorf("objectstore/local: stat object %q: %w", key, err)
	}
	n, err := writeAtomic(dst, func(w io.Writer) (int64, error) { return io.Copy(w, r) })
	if err != nil {
		return false, 0, fmt.Errorf("objectstore/local: write object %q: %w", key, err)
	}
	return true, n, nil
}

// Stat reports existence and size. Absent keys — and keys naming a
// directory or any other non-regular file — return objectstore.ErrNotExist:
// only published objects exist as far as the port is concerned.
func (s *Store) Stat(ctx context.Context, key string) (objectstore.Info, error) {
	if err := validKey(key); err != nil {
		return objectstore.Info{}, err
	}
	if err := ctx.Err(); err != nil {
		return objectstore.Info{}, err
	}
	fi, err := os.Stat(s.path(key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return objectstore.Info{}, fmt.Errorf("objectstore/local: %q: %w", key, objectstore.ErrNotExist)
		}
		return objectstore.Info{}, fmt.Errorf("objectstore/local: stat object %q: %w", key, err)
	}
	if !fi.Mode().IsRegular() {
		return objectstore.Info{}, fmt.Errorf("objectstore/local: %q: %w", key, objectstore.ErrNotExist)
	}
	return objectstore.Info{Key: key, Size: fi.Size()}, nil
}

// Read opens the object for reading. Absent keys and non-regular files
// return objectstore.ErrNotExist. The caller closes the returned reader.
func (s *Store) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validKey(key); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(s.path(key))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("objectstore/local: %q: %w", key, objectstore.ErrNotExist)
		}
		return nil, fmt.Errorf("objectstore/local: open object %q: %w", key, err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("objectstore/local: stat object %q: %w", key, err)
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("objectstore/local: %q: %w", key, objectstore.ErrNotExist)
	}
	return f, nil
}

// List returns every object whose key starts with prefix, in ascending
// lexicographic key order. The prefix is a key prefix, not a directory
// path: it may end mid-filename, and a prefix naming no directory at all
// yields an empty result rather than an error. Filesystem walk order is
// not key order (a directory separator sorts after "-", for instance), so
// results are sorted explicitly.
func (s *Store) List(ctx context.Context, prefix string) ([]objectstore.Info, error) {
	if err := validPrefix(prefix); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base := s.root
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		base = filepath.Join(s.root, filepath.FromSlash(prefix[:i]))
	}

	var out []objectstore.Info
	walkErr := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// A prefix directory that does not exist simply holds no
			// objects; entries removed mid-walk are likewise not listed.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), tmpPrefix) {
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		out = append(out, objectstore.Info{Key: key, Size: fi.Size()})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("objectstore/local: list %q: %w", prefix, walkErr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ReplacePointer atomically replaces a small mutable pointer object.
// Overwriting is expected: pointers are non-authoritative hints and the
// last writer wins.
func (s *Store) ReplacePointer(ctx context.Context, key string, data []byte) error {
	if err := validKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := writeAtomic(s.path(key), func(w io.Writer) (int64, error) {
		n, err := w.Write(data)
		return int64(n), err
	})
	if err != nil {
		return fmt.Errorf("objectstore/local: replace pointer %q: %w", key, err)
	}
	return nil
}

// writeAtomic publishes dst by writing a fsynced temporary file in the
// destination directory and renaming it into place. The temporary file is
// removed on every failure path.
func writeAtomic(dst string, write func(io.Writer) (int64, error)) (int64, error) {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(dir, tmpPrefix+"*")
	if err != nil {
		return 0, err
	}
	tmp := f.Name()
	published := false
	defer func() {
		if !published {
			os.Remove(tmp)
		}
	}()

	n, err := write(f)
	if err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Chmod(tmp, filePerm); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return 0, err
	}
	published = true
	syncDir(dir)
	return n, nil
}

// syncDir flushes a directory entry best-effort. Failure only risks losing
// a rename across a crash, which a retry republishes.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// validKey rejects keys that cannot be mapped safely onto a path below the
// root. Rejection is structural, not a filesystem probe, so it is stable
// across platforms.
func validKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	if err := checkKeyChars(key); err != nil {
		return err
	}
	for _, seg := range strings.Split(key, "/") {
		if err := checkSegment(key, seg); err != nil {
			return err
		}
	}
	return nil
}

// validPrefix applies the key rules to a key prefix. The empty prefix
// matches everything, and a trailing "/" is allowed because it is the
// natural directory-shaped prefix.
func validPrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if err := checkKeyChars(prefix); err != nil {
		return err
	}
	segs := strings.Split(prefix, "/")
	for i, seg := range segs {
		if seg == "" && i == len(segs)-1 {
			continue // trailing "/"
		}
		if err := checkSegment(prefix, seg); err != nil {
			return err
		}
	}
	return nil
}

func checkKeyChars(key string) error {
	if strings.ContainsAny(key, `\`+"\x00") {
		return fmt.Errorf("%w %q: backslash or NUL byte", ErrInvalidKey, key)
	}
	if strings.HasPrefix(key, "/") || filepath.IsAbs(key) {
		return fmt.Errorf("%w %q: absolute path", ErrInvalidKey, key)
	}
	return nil
}

func checkSegment(key, seg string) error {
	switch seg {
	case "":
		return fmt.Errorf("%w %q: empty path segment", ErrInvalidKey, key)
	case ".", "..":
		return fmt.Errorf("%w %q: %q path segment", ErrInvalidKey, key, seg)
	}
	return nil
}
