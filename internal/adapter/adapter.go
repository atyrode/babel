// Package adapter defines the harness source-adapter port (SPEC.md §3).
// OMP, Codex, and Claude Code implement it over one manifest and
// provenance model. Adapters always preserve raw logs; metadata beyond
// that is best effort and absent values carry explicit completeness
// reasons instead of synthesized values.
package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/atyrode/babel/internal/archive"
)

// ErrUnstable is returned by Snapshot when the source changed while being
// staged. The publisher retries within a bound, then defers the session;
// an unstable source never produces a committed manifest entry.
var ErrUnstable = errors.New("adapter: source changed during snapshot")

// SourceSession is one discovered local session.
type SourceSession struct {
	Harness  string
	SourceID string // adapter-defined stable identity; must pass archive.ValidSourceID
	// PrimaryPath is the on-disk primary raw log this session snapshots.
	PrimaryPath string
	// Hint is a display-only description for diagnostics; may be empty.
	Hint string
}

// StagedFile is one artifact staged into the snapshot directory.
type StagedFile struct {
	RelPath    string // source-relative path recorded in the manifest
	StagedPath string // stable copy inside the staging dir
	Size       int64
}

// BlobRef is one resolved content-addressed blob referenced by a session.
type BlobRef struct {
	Digest     archive.Digest
	SourcePath string
	Size       int64
}

// CommonMeta carries the nullable portable catalog fields. A nil field
// MUST be explained by a CompletenessReason; adapters never synthesize
// values merely to satisfy the shape.
type CommonMeta struct {
	Title        *string
	Workspace    *string
	CreatedAt    *time.Time
	ModifiedAt   *time.Time
	Lifecycle    *string
	Repo         *archive.RepoFingerprint
	Completeness []archive.CompletenessReason
}

// Snapshot is one stable staged copy of a session, ready for publication.
// Staged files live under the stagingDir passed to Adapter.Snapshot and
// are owned by the caller afterwards.
type Snapshot struct {
	Source        SourceSession
	SnapshotTime  time.Time // UTC
	StagedPrimary string    // stable copy of the primary raw log
	PrimarySize   int64

	Meta CommonMeta

	// AdapterMetadata is canonical compact JSON (archive.CanonicalRawMessage)
	// or nil; its schema is versioned independently of the envelope.
	AdapterMetadataSchema int
	AdapterMetadata       json.RawMessage

	Artifacts          []StagedFile
	Blobs              []BlobRef
	UnresolvedBlobRefs []string

	// ContinuationGrade is true only when the adapter guarantees the
	// complete artifact/blob closure required to continue the session.
	// Unresolved references force false.
	ContinuationGrade bool
}

// Adapter is the harness source-adapter port.
type Adapter interface {
	// Harness returns the stable lowercase harness name: "omp", "codex",
	// or "claude".
	Harness() string
	// Schema returns the adapter_schema version recorded in manifests.
	Schema() int
	// DefaultRoots returns the default local source roots to scan.
	DefaultRoots() []string
	// Discover enumerates locally available sessions under roots. Roots
	// that do not exist are skipped silently; unreadable content degrades
	// per session, never by aborting the scan.
	Discover(ctx context.Context, roots []string) ([]SourceSession, error)
	// Snapshot stages a stable copy of one session into stagingDir,
	// re-verifying source stability after copying. It returns ErrUnstable
	// (possibly wrapped) when the source changed underneath it.
	Snapshot(ctx context.Context, src SourceSession, stagingDir string) (*Snapshot, error)
}
