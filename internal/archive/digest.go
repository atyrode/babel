// Package archive defines the frozen-candidate babel/v1 archive contract:
// digest semantics, object keys, manifest/commit schemas, canonical
// encoding, and the verified-head reader. Everything here is contract
// surface headed for the pre-first-write freeze gate (SPEC.md §6.1); treat
// changes as contract changes.
package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
)

// Digest is a content digest in canonical "sha256:<64 lowercase hex>" form.
//
// Digests are always computed over plaintext bytes, before any transport
// encryption, so a disaster-recovery operator can verify content with plain
// `sha256sum` after `rclone cat`. For append-delta revisions the content
// digest describes the reassembled plaintext, never the delta object.
type Digest string

const digestPrefix = "sha256:"

// NewDigest wraps a raw SHA-256 sum in canonical form.
func NewDigest(sum [sha256.Size]byte) Digest {
	return Digest(digestPrefix + hex.EncodeToString(sum[:]))
}

// ComputeDigest consumes r and returns its canonical digest and size.
func ComputeDigest(r io.Reader) (Digest, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return NewDigest(sum), n, nil
}

// DigestBytes returns the canonical digest of b.
func DigestBytes(b []byte) Digest {
	return NewDigest(sha256.Sum256(b))
}

// Valid reports whether d is well-formed canonical "sha256:<64 hex>".
func (d Digest) Valid() bool {
	s := string(d)
	if !strings.HasPrefix(s, digestPrefix) || len(s) != len(digestPrefix)+sha256.Size*2 {
		return false
	}
	for _, c := range s[len(digestPrefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Hex returns the 64-character lowercase hex portion of d.
// It must only be called on a Valid digest.
func (d Digest) Hex() string {
	return strings.TrimPrefix(string(d), digestPrefix)
}
