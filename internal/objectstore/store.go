// Package objectstore defines Babel's narrow object-store port (SPEC.md
// §2.3): immutable put, stat, read, list, and atomic small-pointer replace.
// Implementations are injected; the archive layer never knows transport
// details.
package objectstore

import (
	"context"
	"errors"
	"io"
)

// ErrNotExist is returned by Stat and Read for absent keys.
var ErrNotExist = errors.New("objectstore: object does not exist")

// ErrImmutableConflict is returned by Put when an object already exists at
// the key with observably different content (size mismatch).
var ErrImmutableConflict = errors.New("objectstore: immutable object exists with different content")

// Info describes a stored object.
type Info struct {
	Key  string
	Size int64
}

// Store is the injected object-store port.
//
// Immutability contract: Put is ADVISORY no-clobber. Backends without a
// native conditional write (S3-compatible stores) implement it as
// stat-then-write, which leaves a race window under concurrent writers.
// The archive layer is safe against that window by construction:
//
//   - content-addressed keys (cas/...) bind key to content, so a racing
//     duplicate write stores identical bytes and is idempotent;
//   - non-content-addressed immutable keys (commit records) MUST be
//     verified by the writer with a full read-back comparing the exact
//     bytes written; a failed read-back means a concurrent writer won and
//     this publication must be retried at a later generation — never
//     rewritten in place;
//   - shared mode additionally serializes same-host publication with
//     PostgreSQL server-time fenced leases.
//
// Readers only trust digest-verified records, so a torn or clobbered
// object is skipped, never silently exposed.
type Store interface {
	// Put writes an immutable object. If the key already exists with the
	// same size it is left untouched and created=false is returned; a
	// same-key different-size object returns ErrImmutableConflict.
	// Byte-level equality of an existing object is NOT verified here;
	// callers that require it perform a read-back.
	Put(ctx context.Context, key string, r io.Reader) (created bool, size int64, err error)

	// Stat reports existence and size. Absent keys return ErrNotExist.
	Stat(ctx context.Context, key string) (Info, error)

	// Read opens the object for reading. Absent keys return ErrNotExist.
	Read(ctx context.Context, key string) (io.ReadCloser, error)

	// List returns every object whose key starts with prefix, in ascending
	// lexicographic key order.
	List(ctx context.Context, prefix string) ([]Info, error)

	// ReplacePointer atomically replaces a small mutable pointer object.
	// Pointers are non-authoritative hints; last writer wins and readers
	// always fall back to a verified-record scan.
	ReplacePointer(ctx context.Context, key string, data []byte) error
}
