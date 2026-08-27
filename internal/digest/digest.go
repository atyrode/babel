// Package digest provides canonical SHA-256 content digests. Adapters use
// them to verify content-addressed blob stores (OMP's blob directory names
// files by content hash) and to identify session content in metadata; the
// canonical form is verifiable with plain `sha256sum`.
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"
)

// Digest is a content digest in canonical "sha256:<64 lowercase hex>" form.
type Digest string

const prefix = "sha256:"

// New wraps a raw SHA-256 sum in canonical form.
func New(sum [sha256.Size]byte) Digest {
	return Digest(prefix + hex.EncodeToString(sum[:]))
}

// Compute consumes r and returns its canonical digest and size.
func Compute(r io.Reader) (Digest, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, err
	}
	var sum [sha256.Size]byte
	copy(sum[:], h.Sum(nil))
	return New(sum), n, nil
}

// Bytes returns the canonical digest of b.
func Bytes(b []byte) Digest {
	return New(sha256.Sum256(b))
}

// Valid reports whether d is well-formed canonical "sha256:<64 hex>".
func (d Digest) Valid() bool {
	s := string(d)
	if !strings.HasPrefix(s, prefix) || len(s) != len(prefix)+sha256.Size*2 {
		return false
	}
	for _, c := range s[len(prefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Hex returns the 64-character lowercase hex portion of d.
// It must only be called on a Valid digest.
func (d Digest) Hex() string {
	return strings.TrimPrefix(string(d), prefix)
}
