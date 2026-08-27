package catalog

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

// ErrInvalidPath is returned for a declared artifact path that cannot be
// materialized safely below the destination directory.
var ErrInvalidPath = errors.New("catalog: unsafe artifact path")

// ErrDestExists is returned when the destination already holds data. A
// fetched bundle is immutable and persists until the operator prunes it
// (SPEC.md §6.2), so Fetch never overwrites one.
var ErrDestExists = errors.New("catalog: destination already exists")

const (
	// primarySuffix names the reassembled raw transcript.
	primarySuffix = ".jsonl"
	// blobsDir holds the referenced content-addressed blob closure, named
	// by digest because blob references carry no path.
	blobsDir = "blobs"
	// stagePrefix names the in-flight bundle directory.
	stagePrefix = ".babel-fetch-"

	// Materialized bundles are private local data (SPEC.md §9).
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// FileKind classifies one materialized file.
type FileKind string

const (
	// KindPrimary is the reassembled raw transcript.
	KindPrimary FileKind = "primary"
	// KindArtifact is one file of the declared artifact closure.
	KindArtifact FileKind = "artifact"
	// KindBlob is one referenced content-addressed blob.
	KindBlob FileKind = "blob"
)

// MaterializedFile is one verified file written by Fetch.
type MaterializedFile struct {
	// Path is relative to the bundle directory, slash-separated.
	Path   string
	Kind   FileKind
	Digest archive.Digest
	Size   int64
}

// Materialized describes a fully verified local bundle.
type Materialized struct {
	RevisionKey string
	SessionKey  string
	HostID      string
	Harness     string

	// Dir is the bundle directory, which exists only if Fetch succeeded.
	Dir string

	// Encoding and ChainLength record how the primary was reassembled:
	// ChainLength is 1 for a full revision and one more than the number of
	// applied tails otherwise.
	Encoding    archive.Encoding
	ChainLength int

	// Files lists every written file: the primary first, then artifacts by
	// path, then blobs by digest.
	Files []MaterializedFile
	// TotalSize is the sum of the written file sizes.
	TotalSize int64

	// UnresolvedBlobRefs carries the entry's unresolved references. They
	// are not fetchable and force continuation_grade=false (SPEC.md §3).
	UnresolvedBlobRefs []string
}

// Fetch downloads and digest-verifies one immutable revision plus its
// declared artifact and blob closure (SPEC.md §6.2).
//
// The primary transcript is reassembled by walking the revision's
// append-delta chain to the full ancestor inside its generation, bounded by
// archive.MaxChainDepth. Every stored object is verified against its own
// digest and size, and the reassembled plaintext is verified against the
// entry's content digest; a chain that cannot be fully verified fails
// rather than yielding a partial transcript (SPEC.md §11).
//
// The bundle is built in a private staging directory next to destDir and
// renamed into place only after every byte verifies, so a failed or
// cancelled fetch leaves no verified bundle and no partial one (SPEC.md
// §11). Declared artifact paths are validated structurally before use:
// nothing is ever written outside destDir.
func Fetch(ctx context.Context, st objectstore.Store, rev Revision, destDir string) (*Materialized, error) {
	if st == nil {
		return nil, errors.New("catalog: nil object store")
	}
	if strings.TrimSpace(destDir) == "" {
		return nil, errors.New("catalog: empty destination directory")
	}
	e := rev.Entry
	if err := validateEntry(e.HostID, e); err != nil {
		return nil, fmt.Errorf("catalog: revision %s is not fetchable: %w", entryLabel(e), err)
	}
	if e.Encoding == archive.EncodingAppendDelta && len(rev.entries) == 0 {
		return nil, fmt.Errorf("catalog: revision %s is an append delta with no loaded generation, so its chain cannot be resolved", e.RevisionKey)
	}
	chain, err := chainOf(e, rev.entries)
	if err != nil {
		return nil, err
	}
	for _, a := range e.Artifacts {
		if err := validRelPath(a.Path); err != nil {
			return nil, fmt.Errorf("catalog: revision %s: %w", e.RevisionKey, err)
		}
	}

	dest, err := filepath.Abs(destDir)
	if err != nil {
		return nil, fmt.Errorf("catalog: resolve destination: %w", err)
	}
	if err := checkDest(dest); err != nil {
		return nil, err
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, dirPerm); err != nil {
		return nil, fmt.Errorf("catalog: create destination parent: %w", err)
	}
	stage, err := os.MkdirTemp(parent, stagePrefix)
	if err != nil {
		return nil, fmt.Errorf("catalog: create staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			os.RemoveAll(stage)
		}
	}()

	m := &Materialized{
		RevisionKey:        e.RevisionKey,
		SessionKey:         e.SessionKey,
		HostID:             e.HostID,
		Harness:            e.Harness,
		Dir:                dest,
		Encoding:           e.Encoding,
		ChainLength:        len(chain),
		UnresolvedBlobRefs: e.UnresolvedBlobRefs,
	}

	primary := safeName(e.SourceID) + primarySuffix
	if err := writeChain(ctx, st, chain, e.Content, stage, primary); err != nil {
		return nil, fmt.Errorf("catalog: revision %s: %w", e.RevisionKey, err)
	}
	m.add(primary, KindPrimary, e.Content.Digest, e.Content.Size)

	artifacts := make([]archive.FileRef, len(e.Artifacts))
	copy(artifacts, e.Artifacts)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	for _, a := range artifacts {
		ref := archive.ObjectRef{Digest: a.Digest, Size: a.Size}
		if err := writeObject(ctx, st, ref, stage, a.Path); err != nil {
			return nil, fmt.Errorf("catalog: revision %s artifact: %w", e.RevisionKey, err)
		}
		m.add(a.Path, KindArtifact, a.Digest, a.Size)
	}

	blobs := make([]archive.ObjectRef, len(e.Blobs))
	copy(blobs, e.Blobs)
	sort.Slice(blobs, func(i, j int) bool { return blobs[i].Digest < blobs[j].Digest })
	for _, b := range blobs {
		rel := blobsDir + "/" + b.Digest.Hex()
		if err := writeObject(ctx, st, b, stage, rel); err != nil {
			return nil, fmt.Errorf("catalog: revision %s blob: %w", e.RevisionKey, err)
		}
		m.add(rel, KindBlob, b.Digest, b.Size)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, dest); err != nil {
		return nil, fmt.Errorf("catalog: publish bundle: %w", err)
	}
	published = true
	return m, nil
}

func (m *Materialized) add(path string, kind FileKind, d archive.Digest, size int64) {
	m.Files = append(m.Files, MaterializedFile{Path: path, Kind: kind, Digest: d, Size: size})
	m.TotalSize += size
}

// checkDest refuses a destination that already holds a bundle. An empty
// directory is accepted: the atomic rename replaces it.
func checkDest(dest string) error {
	fi, err := os.Stat(dest)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("catalog: inspect destination: %w", err)
	case !fi.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrDestExists, dest)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return fmt.Errorf("catalog: inspect destination: %w", err)
	}
	if len(entries) > 0 {
		return fmt.Errorf("%w: %s", ErrDestExists, dest)
	}
	return nil
}

// writeChain reassembles a revision's plaintext into one file: the full
// payload followed by each append-delta tail in application order. Every
// link is verified against its own object reference as it streams, and the
// concatenation is verified against the content reference before the file
// is accepted.
func writeChain(ctx context.Context, st objectstore.Store, chain []archive.ManifestEntry, content archive.ObjectRef, root, rel string) error {
	f, err := createIn(root, rel)
	if err != nil {
		return err
	}
	defer f.Close()

	total := sha256.New()
	var written int64
	for _, link := range chain {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := copyVerified(ctx, st, link.Object, io.MultiWriter(f, total))
		if err != nil {
			return fmt.Errorf("chain link %s: %w", link.RevisionKey, err)
		}
		written += n
	}
	if written != content.Size {
		return fmt.Errorf("reassembled content has size %d, want %d", written, content.Size)
	}
	var sum [sha256.Size]byte
	copy(sum[:], total.Sum(nil))
	if got := archive.NewDigest(sum); got != content.Digest {
		return fmt.Errorf("reassembled content has digest %s, want %s", got, content.Digest)
	}
	return f.Close()
}

// writeObject materializes one content-addressed object, verifying its
// digest and size while it streams.
func writeObject(ctx context.Context, st objectstore.Store, ref archive.ObjectRef, root, rel string) error {
	f, err := createIn(root, rel)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := copyVerified(ctx, st, ref, f); err != nil {
		return err
	}
	return f.Close()
}

// copyVerified streams one object into w and verifies its size and
// plaintext digest. Verification happens after the copy, which is why the
// caller writes into a staging tree that is discarded on any failure.
func copyVerified(ctx context.Context, st objectstore.Store, ref archive.ObjectRef, w io.Writer) (int64, error) {
	rc, err := st.Read(ctx, archive.CASKey(ref.Digest))
	if err != nil {
		return 0, err
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), rc)
	if err != nil {
		return n, err
	}
	if n != ref.Size {
		return n, fmt.Errorf("object %s has size %d, want %d", ref.Digest, n, ref.Size)
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	if got := archive.NewDigest(sum); got != ref.Digest {
		return n, fmt.Errorf("object %s has digest %s", ref.Digest, got)
	}
	return n, nil
}

// createIn creates one bundle file below root, refusing to overwrite an
// existing file so two declared paths can never silently collide.
func createIn(root, rel string) (*os.File, error) {
	if err := validRelPath(rel); err != nil {
		return nil, err
	}
	target := filepath.Join(root, filepath.FromSlash(rel))
	if !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w %q: escapes the destination", ErrInvalidPath, rel)
	}
	if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, filePerm)
	if err != nil {
		return nil, fmt.Errorf("materialize %q: %w", rel, err)
	}
	return f, nil
}

// validRelPath applies the archive's structural path rule to a declared
// bundle-relative path: non-empty, relative, slash-separated, free of
// backslashes and NUL bytes, and without an empty, "." or ".." segment.
// This is the same structural rule the local object-store backend applies
// to keys, reimplemented here so the reader depends on no backend.
func validRelPath(p string) error {
	if p == "" {
		return fmt.Errorf("%w: empty", ErrInvalidPath)
	}
	if strings.ContainsAny(p, `\`+"\x00") {
		return fmt.Errorf("%w %q: backslash or NUL byte", ErrInvalidPath, p)
	}
	if strings.HasPrefix(p, "/") || filepath.IsAbs(p) {
		return fmt.Errorf("%w %q: absolute path", ErrInvalidPath, p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return fmt.Errorf("%w %q: empty path segment", ErrInvalidPath, p)
		case ".", "..":
			return fmt.Errorf("%w %q: %q path segment", ErrInvalidPath, p, seg)
		}
	}
	return nil
}

// safeName derives the primary transcript's filename from the adapter's
// source identity. Source IDs may be path-shaped, so only the last segment
// is used, characters outside [A-Za-z0-9._-] are replaced, leading dots are
// dropped, and the length is bounded.
func safeName(sourceID string) string {
	seg := sourceID[strings.LastIndexByte(sourceID, '/')+1:]
	b := make([]byte, 0, len(seg))
	for _, c := range []byte(seg) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	name := strings.TrimLeft(string(b), ".")
	if name == "" {
		return "session"
	}
	if len(name) > 96 {
		name = name[:96]
	}
	return name
}
