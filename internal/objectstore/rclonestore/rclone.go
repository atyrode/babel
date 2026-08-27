// Package rclonestore implements Babel's object-store port (SPEC.md §2.3)
// over the external rclone binary, which owns Cellar and rclone-crypt
// transport.
//
// Babel shells out to rclone instead of linking an S3 client so that the
// operator's rclone configuration — endpoints, credentials, and crypt —
// stays entirely external. This package never reads, derives, holds, or
// forwards credentials or crypt keys, never places any secret on argv or in
// the child environment, and never writes object content to a log or an
// error message: rclone's own diagnostics are the only text quoted back,
// bounded to a short excerpt.
package rclonestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/objectstore"
)

// stderrExcerptLimit bounds how much rclone diagnostic output is retained
// for an error message. The tail is kept because rclone reports its summary
// error last.
const stderrExcerptLimit = 512

// catWaitDelay bounds how long a cancelled `rclone cat` may keep the output
// pipe open before it is force-closed, so Read and Close never block
// indefinitely after their context is cancelled.
const catWaitDelay = 5 * time.Second

// Store is the rclone-backed objectstore.Store.
//
// The remote is an rclone destination such as "cellar:bucket/babel/v1" or a
// plain absolute directory path; keys are appended to it with "/". Every
// operation is a single short-lived rclone invocation bound to the caller's
// context.
//
// Put takes the advisory-immutable stat-then-write path described by the
// objectstore.Store contract: rclone exposes no conditional write, so an
// existing object is detected with a stat and a same-size object is left
// untouched. Between that stat and the write a concurrent writer can win,
// so callers rely on the archive layer's two guarantees rather than on
// backend exclusion — content-addressed keys make a racing duplicate write
// byte-identical and therefore idempotent, and non-content-addressed
// immutable keys (commit records) are verified by the writer with a full
// read-back that fails the publication when another writer won.
//
// ReplacePointer inherits its atomicity from the backend object semantics
// (an S3 PUT replaces a key atomically). Pointers are non-authoritative
// hints under the same contract, so readers tolerate a stale or torn
// pointer by falling back to a verified-record scan.
//
// A Store is stateless and safe for concurrent use.
type Store struct {
	remote string
	binary string
}

var _ objectstore.Store = (*Store)(nil)

// Option customizes a Store.
type Option func(*Store)

// WithBinary overrides the rclone executable, which defaults to "rclone"
// resolved through PATH. The path is passed to exec unchanged; it is a
// program location, never a credential.
func WithBinary(path string) Option {
	return func(s *Store) { s.binary = path }
}

// New returns a Store publishing under the given rclone remote.
func New(remote string, opts ...Option) *Store {
	s := &Store{remote: remote, binary: "rclone"}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Put writes an immutable object, honouring the advisory no-clobber
// contract documented on Store. An existing same-size object yields
// created=false; an existing different-size object yields
// objectstore.ErrImmutableConflict.
//
// The object size is required up front because rclone preallocates the
// destination with `rcat --size`. A seekable reader (an *os.File or a
// *bytes.Reader, as the archive layer supplies) is measured and streamed
// directly; anything else is buffered in memory to learn its length. The
// buffer is memory only: plaintext session bytes are never spilled to a
// temporary file.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) (bool, int64, error) {
	if err := validateKey(key); err != nil {
		return false, 0, err
	}
	existing, err := s.Stat(ctx, key)
	exists := err == nil
	if err != nil && !errors.Is(err, objectstore.ErrNotExist) {
		return false, 0, err
	}
	body, size, err := sizedBody(r)
	if err != nil {
		return false, 0, err
	}
	if exists {
		if existing.Size == size {
			return false, existing.Size, nil
		}
		return false, existing.Size, fmt.Errorf("%w: %s holds %d bytes, refusing to write %d",
			objectstore.ErrImmutableConflict, key, existing.Size, size)
	}
	args := []string{"rcat", "--size", strconv.FormatInt(size, 10), "--", s.target(key)}
	if err := s.run(ctx, body, nil, args); err != nil {
		return false, 0, err
	}
	return true, size, nil
}

// Stat reports existence and size by listing the key's parent directory and
// matching the exact leaf name. An absent object — or an absent parent
// directory on backends that report one — returns objectstore.ErrNotExist.
func (s *Store) Stat(ctx context.Context, key string) (objectstore.Info, error) {
	if err := validateKey(key); err != nil {
		return objectstore.Info{}, err
	}
	dir, leaf := splitKey(key)
	entries, err := s.lsjson(ctx, dir, false)
	if err != nil {
		if isPathAbsent(err) {
			return objectstore.Info{}, fmt.Errorf("%w: %s", objectstore.ErrNotExist, key)
		}
		return objectstore.Info{}, err
	}
	for _, e := range entries {
		if e.IsDir || e.Path != leaf {
			continue
		}
		return objectstore.Info{Key: key, Size: e.Size}, nil
	}
	return objectstore.Info{}, fmt.Errorf("%w: %s", objectstore.ErrNotExist, key)
}

// Read streams the object. Existence is resolved with a Stat first so that
// an absent key returns objectstore.ErrNotExist instead of an opaque rclone
// failure; the returned reader then pipes `rclone cat` output straight
// through without buffering the object.
//
// The rclone process is bound to ctx and reaped by Close, which must be
// called. A stream read to completion reports a non-zero rclone exit as a
// read error, so a truncated transfer can never be mistaken for a short
// object; readers additionally verify digests before trusting bytes.
func (s *Store) Read(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := validateKey(key); err != nil {
		return nil, err
	}
	if _, err := s.Stat(ctx, key); err != nil {
		return nil, err
	}
	args := []string{"cat", "--", s.target(key)}
	cmd := exec.CommandContext(ctx, s.binary, args...)
	cmd.WaitDelay = catWaitDelay
	stderr := &stderrTail{}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("rclonestore: opening rclone cat output pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("rclonestore: starting %s: %w", strings.Join(args, " "), err)
	}
	return &catReader{cmd: cmd, stdout: stdout, stderr: stderr, args: args}, nil
}

// List returns every object whose key starts with prefix, in ascending
// lexicographic key order.
//
// The prefix may end mid-filename, so rclone lists the deepest whole
// directory the prefix names, recursively, and the reconstructed full keys
// are filtered by exact string prefix. A missing directory lists as empty
// rather than as an error, matching the S3 behaviour of an unused prefix.
func (s *Store) List(ctx context.Context, prefix string) ([]objectstore.Info, error) {
	if err := validatePrefix(prefix); err != nil {
		return nil, err
	}
	dir, _ := splitKey(prefix)
	entries, err := s.lsjson(ctx, dir, true)
	if err != nil {
		if isPathAbsent(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]objectstore.Info, 0, len(entries))
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		key := e.Path
		if dir != "" {
			key = dir + "/" + e.Path
		}
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, objectstore.Info{Key: key, Size: e.Size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ReplacePointer overwrites a small mutable pointer object in place.
// Pointers are last-writer-wins hints, so no stat guard applies.
func (s *Store) ReplacePointer(ctx context.Context, key string, data []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	args := []string{"rcat", "--size", strconv.Itoa(len(data)), "--", s.target(key)}
	return s.run(ctx, bytes.NewReader(data), nil, args)
}

// target joins the remote and a slash-separated key or directory. An empty
// sub addresses the remote root.
func (s *Store) target(sub string) string {
	if sub == "" {
		return s.remote
	}
	if strings.HasSuffix(s.remote, "/") || strings.HasSuffix(s.remote, ":") {
		return s.remote + sub
	}
	return s.remote + "/" + sub
}

// lsEntry is the subset of an `rclone lsjson` record Babel consumes. Path
// is relative to the listed directory and carries subdirectories when the
// listing is recursive.
type lsEntry struct {
	Path  string `json:"Path"`
	Size  int64  `json:"Size"`
	IsDir bool   `json:"IsDir"`
}

// lsjson lists dir relative to the remote. Mime types and modification
// times are suppressed because Babel derives neither from the backend, and
// asking for them costs an extra request per object on some remotes.
func (s *Store) lsjson(ctx context.Context, dir string, recursive bool) ([]lsEntry, error) {
	args := []string{"lsjson", "--files-only", "--no-mimetype", "--no-modtime"}
	if recursive {
		args = append(args, "-R")
	}
	args = append(args, "--", s.target(dir))
	var out bytes.Buffer
	if err := s.run(ctx, nil, &out, args); err != nil {
		return nil, err
	}
	var entries []lsEntry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		return nil, fmt.Errorf("rclonestore: decoding rclone lsjson output for %q: %w", dir, err)
	}
	return entries, nil
}

// run invokes rclone to completion. stdin and stdout may be nil.
func (s *Store) run(ctx context.Context, stdin io.Reader, stdout io.Writer, args []string) error {
	cmd := exec.CommandContext(ctx, s.binary, args...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if stdout != nil {
		cmd.Stdout = stdout
	}
	stderr := &stderrTail{}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return &runError{args: args, err: err, stderr: stderr.excerpt()}
	}
	return nil
}

// runError reports a failed rclone invocation. It carries the argument
// vector — which holds no credentials by construction — and a bounded
// excerpt of rclone's diagnostics, never object content.
type runError struct {
	args   []string
	err    error
	stderr string
}

func (e *runError) Error() string {
	msg := fmt.Sprintf("rclonestore: rclone %s: %v", strings.Join(e.args, " "), e.err)
	if e.stderr != "" {
		msg += ": " + e.stderr
	}
	return msg
}

func (e *runError) Unwrap() error { return e.err }

// pathAbsent reports whether rclone failed because the addressed path does
// not exist, as opposed to a transport, configuration, or permission
// failure. Only rclone's specific absence phrasings qualify, so a genuine
// failure is never silently reported as a missing object.
func (e *runError) pathAbsent() bool {
	lower := strings.ToLower(e.stderr)
	return strings.Contains(lower, "directory not found") ||
		strings.Contains(lower, "object not found")
}

// isPathAbsent reports whether err is an rclone failure caused by an absent
// path.
func isPathAbsent(err error) bool {
	var re *runError
	return errors.As(err, &re) && re.pathAbsent()
}

// catReader adapts a running `rclone cat` to io.ReadCloser, surfacing the
// process exit status once the stream has been fully consumed.
type catReader struct {
	cmd     *exec.Cmd
	stdout  io.ReadCloser
	stderr  *stderrTail
	args    []string
	drained bool
	reaped  bool
	waitErr error
}

func (r *catReader) Read(p []byte) (int, error) {
	n, err := r.stdout.Read(p)
	if errors.Is(err, io.EOF) {
		r.drained = true
		if waitErr := r.wait(); waitErr != nil {
			return n, waitErr
		}
	}
	return n, err
}

// Close reaps the rclone process. Closing before the stream is drained is a
// caller decision that makes rclone fail on a broken pipe, so only a fully
// consumed stream reports the exit status.
func (r *catReader) Close() error {
	_ = r.stdout.Close()
	waitErr := r.wait()
	if r.drained {
		return waitErr
	}
	return nil
}

func (r *catReader) wait() error {
	if !r.reaped {
		r.reaped = true
		if err := r.cmd.Wait(); err != nil {
			r.waitErr = &runError{args: r.args, err: err, stderr: r.stderr.excerpt()}
		}
	}
	return r.waitErr
}

// stderrTail retains the trailing stderrExcerptLimit bytes written to it,
// bounding memory for a chatty or hostile child process.
type stderrTail struct {
	buf []byte
}

func (w *stderrTail) Write(p []byte) (int, error) {
	if len(p) >= stderrExcerptLimit {
		w.buf = append(w.buf[:0], p[len(p)-stderrExcerptLimit:]...)
		return len(p), nil
	}
	w.buf = append(w.buf, p...)
	if over := len(w.buf) - stderrExcerptLimit; over > 0 {
		w.buf = w.buf[:copy(w.buf, w.buf[over:])]
	}
	return len(p), nil
}

// excerpt renders the retained diagnostics as a single line.
func (w *stderrTail) excerpt() string {
	var parts []string
	for _, line := range strings.Split(string(w.buf), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "; ")
}

// errNotSeekable marks a reader that does not really support seeking; the
// reader position is unchanged when it is returned.
var errNotSeekable = errors.New("rclonestore: reader is not seekable")

// sizedBody returns the body to stream and its exact byte length, measuring
// a seekable reader in place and buffering anything else.
func sizedBody(r io.Reader) (io.Reader, int64, error) {
	if sk, ok := r.(io.Seeker); ok {
		size, err := remainingLen(sk)
		switch {
		case err == nil:
			return r, size, nil
		case !errors.Is(err, errNotSeekable):
			return nil, 0, err
		}
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, 0, fmt.Errorf("rclonestore: buffering object body to determine its size: %w", err)
	}
	return bytes.NewReader(buf), int64(len(buf)), nil
}

// remainingLen reports how many bytes remain from the current position,
// restoring that position. It returns errNotSeekable when the reader only
// nominally implements io.Seeker.
func remainingLen(sk io.Seeker) (int64, error) {
	cur, err := sk.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, errNotSeekable
	}
	end, err := sk.Seek(0, io.SeekEnd)
	if err != nil {
		if _, rerr := sk.Seek(cur, io.SeekStart); rerr != nil {
			return 0, fmt.Errorf("rclonestore: object body position lost while sizing it: %w", rerr)
		}
		return 0, errNotSeekable
	}
	if _, err := sk.Seek(cur, io.SeekStart); err != nil {
		return 0, fmt.Errorf("rclonestore: restoring object body position after sizing it: %w", err)
	}
	return end - cur, nil
}

// splitKey splits a key or prefix into its whole directory part and the
// remaining leaf, which is a complete filename for a key and possibly a
// partial one for a prefix.
func splitKey(key string) (dir, leaf string) {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// validateKey rejects keys that cannot address exactly one object under the
// remote. Keys are generated by the archive layer, so this guards against
// programming errors and against traversal or flag-injection attempts
// reaching argv.
func validateKey(key string) error {
	if key == "" {
		return errors.New("rclonestore: invalid object key: empty")
	}
	if strings.HasSuffix(key, "/") {
		return fmt.Errorf("rclonestore: invalid object key %q: names a directory", key)
	}
	return validateSegments(key, strings.Split(key, "/"))
}

// validatePrefix rejects prefixes that cannot address a subtree. The empty
// prefix is valid and selects every object.
func validatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	segments := strings.Split(prefix, "/")
	// A trailing "/" leaves an empty final segment, which is a legitimate
	// whole-directory prefix rather than an empty path element.
	if segments[len(segments)-1] == "" {
		segments = segments[:len(segments)-1]
	}
	return validateSegments(prefix, segments)
}

func validateSegments(path string, segments []string) error {
	for _, seg := range segments {
		switch seg {
		case "":
			return fmt.Errorf("rclonestore: invalid object key %q: empty path segment", path)
		case ".", "..":
			return fmt.Errorf("rclonestore: invalid object key %q: relative path segment", path)
		}
	}
	for _, b := range []byte(path) {
		if b < 0x20 || b == 0x7f {
			return fmt.Errorf("rclonestore: invalid object key %q: control character", path)
		}
	}
	return nil
}
