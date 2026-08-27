package archive

import (
	"fmt"
	"strconv"
	"strings"
)

// Object keys are slash-separated paths relative to the archive root
// (`archive:babel/v1/` on the remote, the configured root directory for the
// local backend):
//
//	cas/sha256/<aa>/<64 hex>          all immutable content-addressed bytes:
//	                                  bundle payloads, append-delta tails,
//	                                  artifacts, blobs, manifest segments,
//	                                  generation indexes
//	hosts/<host-id>/commits/g<gen 10>-<record digest 64>.json
//	hosts/<host-id>/latest.json       non-authoritative pointer hint
//
// Commit-record keys are write-once by construction: the name embeds the
// 10-digit zero-padded generation plus the full hex digest of the record's
// own canonical bytes, so an identical key implies identical bytes and
// different bytes can never clobber one another. This rests on the same
// SHA-256 collision-resistance assumption as the CAS itself — no new
// cryptographic assumption is introduced.
//
// Shared-mode publication remains serialized by PostgreSQL server-time
// fenced host leases; the digest-suffixed key is defense-in-depth for the
// lease-less recovery path, which must not depend on PostgreSQL.
//
// The canonical total order of a host's commit records is ascending
// lexicographic key order — the zero-padded generation dominates and the
// record digest deterministically breaks same-generation ties. Readers
// select the highest fully verified record in that order. Two records at
// one generation are anomalous: they indicate concurrent publication
// without a lease (recovery or misconfiguration). The deterministic
// winner keeps every reader consistent; the shadowed writer's sessions
// are re-published by its next reconciling push, and verify surfaces the
// duplicate as a warning.
//
// DRAFT CONTRACT SURFACE: this key form and ordering model require
// explicit operator approval at the pre-first-write freeze gate
// (SPEC.md §6.1) before any durable babel/v1 remote write.
const commitNameLen = 1 + 10 + 1 + 64 // "g" + generation + "-" + digest hex

// CASKey returns the content-addressed storage key for a digest.
func CASKey(d Digest) string {
	h := d.Hex()
	return "cas/sha256/" + h[:2] + "/" + h
}

// HostPrefix returns the key prefix owning every host-scoped object.
func HostPrefix(hostID string) string { return "hosts/" + hostID + "/" }

// CommitPrefix returns the key prefix of a host's commit records.
func CommitPrefix(hostID string) string { return HostPrefix(hostID) + "commits/" }

// LatestKey returns the key of a host's latest-hint pointer.
func LatestKey(hostID string) string { return HostPrefix(hostID) + "latest.json" }

// GenerationKey renders a generation number in canonical ordered form.
func GenerationKey(gen uint64) string { return fmt.Sprintf("g%010d", gen) }

// CommitKey returns the write-once commit-record key of a host generation,
// derived from the digest of the record's canonical bytes.
func CommitKey(hostID string, gen uint64, recordDigest Digest) string {
	return CommitPrefix(hostID) + GenerationKey(gen) + "-" + recordDigest.Hex() + ".json"
}

// ParseCommitKey extracts the generation and record digest from a full
// commit-record key as listed under CommitPrefix. It returns ok=false for
// foreign or malformed keys, which readers skip.
func ParseCommitKey(key string) (gen uint64, recordDigest Digest, ok bool) {
	base := key[strings.LastIndexByte(key, '/')+1:]
	name, found := strings.CutSuffix(base, ".json")
	if !found || len(name) != commitNameLen || name[0] != 'g' || name[11] != '-' {
		return 0, "", false
	}
	for _, c := range name[1:11] {
		if c < '0' || c > '9' {
			return 0, "", false
		}
	}
	d := Digest("sha256:" + name[12:])
	if !d.Valid() {
		return 0, "", false
	}
	n, err := strconv.ParseUint(name[1:11], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return n, d, true
}

// SessionKey is the globally unique session identity, namespaced by
// harness, stable host ID, and adapter-defined source identity
// (SPEC.md §3). Its canonical string form is "harness/host/source-id".
type SessionKey struct {
	Harness  string
	HostID   string
	SourceID string
}

// String renders the canonical session-key form.
func (k SessionKey) String() string {
	return k.Harness + "/" + k.HostID + "/" + k.SourceID
}

// Revision renders the immutable revision key: the session key plus the
// reassembled-content digest, separated by "@".
func (k SessionKey) Revision(content Digest) string {
	return k.String() + "@" + string(content)
}

// ParseSessionKey parses the canonical "harness/host/source-id" form.
func ParseSessionKey(s string) (SessionKey, error) {
	parts := strings.SplitN(s, "/", 3)
	if len(parts) != 3 {
		return SessionKey{}, fmt.Errorf("archive: malformed session key %q", s)
	}
	k := SessionKey{Harness: parts[0], HostID: parts[1], SourceID: parts[2]}
	if !ValidName(k.Harness) || !ValidName(k.HostID) || !ValidSourceID(k.SourceID) {
		return SessionKey{}, fmt.Errorf("archive: invalid session key %q", s)
	}
	return k, nil
}

// ParseRevisionKey splits a "session@sha256:<hex>" revision key.
func ParseRevisionKey(s string) (SessionKey, Digest, error) {
	i := strings.LastIndexByte(s, '@')
	if i < 0 {
		return SessionKey{}, "", fmt.Errorf("archive: malformed revision key %q", s)
	}
	k, err := ParseSessionKey(s[:i])
	if err != nil {
		return SessionKey{}, "", err
	}
	d := Digest(s[i+1:])
	if !d.Valid() {
		return SessionKey{}, "", fmt.Errorf("archive: invalid digest in revision key %q", s)
	}
	return k, d, nil
}

// ValidName validates host IDs and harness names: 1-64 characters of
// [a-z0-9._-], starting with an alphanumeric.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// ValidSourceID validates adapter-defined source identities: one or more
// "/"-separated segments of [A-Za-z0-9._-]+, no empty or "." / ".."
// segments, at most 512 bytes. "@" is reserved for revision keys.
func ValidSourceID(s string) bool {
	if len(s) == 0 || len(s) > 512 {
		return false
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		for _, c := range seg {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case c == '.' || c == '_' || c == '-':
			default:
				return false
			}
		}
	}
	return true
}
