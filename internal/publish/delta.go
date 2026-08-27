package publish

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/objectstore"
)

// chainBuf is the streaming window used by parent reassembly and prefix
// comparison. Reassembly never materializes a whole revision in memory:
// publication must stay cheap on multi-megabyte transcripts.
const chainBuf = 64 << 10

// chainFor resolves the append-delta chain of a committed revision: the
// entries from its nearest EncodingFull ancestor down to the revision
// itself, in reassembly order (SPEC.md §6.1).
//
// The walk follows ManifestEntry.ParentRevision and only ever resolves
// revisions inside byRevision, the entry set of one committed generation.
// A parent outside that set, an unknown encoding, or a chain longer than
// archive.MaxChainDepth is a corrupt chain, reported as an error rather
// than reassembled.
func chainFor(byRevision map[string]archive.ManifestEntry, rev string) ([]archive.ManifestEntry, error) {
	chain := make([]archive.ManifestEntry, 0, 8)
	for cur := rev; len(chain) <= archive.MaxChainDepth; {
		e, ok := byRevision[cur]
		if !ok {
			return nil, fmt.Errorf("publish: revision %q is absent from the committed entry set", cur)
		}
		chain = append(chain, e)
		switch e.Encoding {
		case archive.EncodingFull:
			slices.Reverse(chain)
			return chain, nil
		case archive.EncodingAppendDelta:
			if e.ParentRevision == "" {
				return nil, fmt.Errorf("publish: append-delta revision %q has no parent", cur)
			}
			cur = e.ParentRevision
		default:
			return nil, fmt.Errorf("publish: revision %q has unknown encoding %q", cur, e.Encoding)
		}
	}
	return nil, fmt.Errorf("publish: append chain of %q exceeds the %d-revision bound", rev, archive.MaxChainDepth)
}

// chainReader streams the reassembled plaintext of a chain returned by
// chainFor: the full ancestor's payload followed by every tail object in
// chain order. Objects are opened lazily and closed as they are consumed,
// so a long chain costs one open handle, not one per revision.
type chainReader struct {
	ctx   context.Context
	store objectstore.Store
	refs  []archive.ObjectRef
	next  int
	cur   io.ReadCloser
}

func openChain(ctx context.Context, st objectstore.Store, chain []archive.ManifestEntry) *chainReader {
	refs := make([]archive.ObjectRef, len(chain))
	for i, e := range chain {
		refs[i] = e.Object
	}
	return &chainReader{ctx: ctx, store: st, refs: refs}
}

func (c *chainReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if c.cur == nil {
			if c.next == len(c.refs) {
				return 0, io.EOF
			}
			rc, err := c.store.Read(c.ctx, archive.CASKey(c.refs[c.next].Digest))
			if err != nil {
				return 0, err
			}
			c.cur, c.next = rc, c.next+1
		}
		n, err := c.cur.Read(p)
		switch {
		case err == io.EOF:
			cerr := c.cur.Close()
			c.cur = nil
			if cerr != nil {
				return n, cerr
			}
			if n > 0 {
				return n, nil
			}
			// This object is exhausted; continue with the next tail.
		case err != nil:
			return n, err
		case n > 0:
			return n, nil
		}
	}
}

// Close releases the object currently open, if any.
func (c *chainReader) Close() error {
	if c.cur == nil {
		return nil
	}
	err := c.cur.Close()
	c.cur = nil
	return err
}

// isPrefix reports whether the committed plaintext described by want — the
// reassembly of chain — is an exact byte prefix of the staged file at path,
// which is the sole precondition for publishing an append-delta revision
// (SPEC.md decision 40).
//
// The comparison streams both sides and verifies the parent plaintext
// against its own digest and size on the way, so a delta is only ever
// chained onto a parent this publisher just proved readable. Anything that
// makes the relation unprovable — a byte mismatch, a shorter staged file, a
// missing chain object, a digest mismatch — reports false, which routes the
// caller to a full revision. Only genuine store failures are returned as
// errors.
func isPrefix(ctx context.Context, st objectstore.Store, chain []archive.ManifestEntry, want archive.ObjectRef, path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	parent := openChain(ctx, st, chain)
	defer parent.Close()

	h := sha256.New()
	pb := make([]byte, chainBuf)
	sb := make([]byte, chainBuf)
	var n int64
	for {
		m, rerr := parent.Read(pb)
		if m > 0 {
			n += int64(m)
			if n > want.Size {
				return false, nil // chain outgrew its declared plaintext
			}
			h.Write(pb[:m])
			if _, serr := io.ReadFull(f, sb[:m]); serr != nil {
				if errors.Is(serr, io.EOF) || errors.Is(serr, io.ErrUnexpectedEOF) {
					return false, nil // staged content is shorter than the parent
				}
				return false, serr
			}
			if string(pb[:m]) != string(sb[:m]) {
				return false, nil
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			if errors.Is(rerr, objectstore.ErrNotExist) {
				return false, nil // unverifiable parent: publish a full revision
			}
			return false, rerr
		}
	}
	if n != want.Size {
		return false, nil
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return archive.NewDigest(sum) == want.Digest, nil
}

// digestFile returns the canonical digest and size of a whole staged file.
func digestFile(path string) (archive.Digest, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return archive.ComputeDigest(f)
}

// digestTail returns the canonical digest and size of a staged file from
// offset onwards: the exact bytes an append-delta revision stores as its
// payload object.
func digestTail(path string, offset int64) (archive.Digest, int64, error) {
	f, err := openAt(path, offset)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return archive.ComputeDigest(f)
}

// openAt opens a staged file positioned at offset.
func openAt(path string, offset int64) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
