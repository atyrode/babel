// Package adapter defines the harness source-adapter port (SPEC.md §3).
// OMP, Codex, and Claude Code implement it over one provenance model.
// Adapters read live session files in place: durability is restic's job
// (snapshots are crash-consistent per file, not transactional across
// files), so descriptions are best-effort views refreshed on every call,
// and parsers tolerate torn or malformed lines by counting and skipping
// them rather than failing. Metadata beyond the raw files is best effort;
// absent values carry explicit completeness reasons instead of
// synthesized values.
package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/digest"
)

// SourceSession is one discovered local session.
type SourceSession struct {
	Harness  string
	SourceID string // adapter-defined stable identity; must pass ValidSourceID
	// PrimaryPath is the on-disk primary raw log of this session.
	PrimaryPath string
	// Hint is a display-only description for diagnostics; may be empty.
	Hint string
}

// SourceFile is one on-disk file belonging to a session's closure.
type SourceFile struct {
	RelPath    string // root-relative path, for display and snapshot lookup
	SourcePath string // absolute on-disk path
	Size       int64
}

// BlobRef is one resolved content-addressed blob referenced by a session.
type BlobRef struct {
	Digest     digest.Digest
	SourcePath string
	Size       int64
}

// RepoFingerprint identifies the repository state associated with a
// session's workspace.
type RepoFingerprint struct {
	Remote string `json:"remote,omitempty"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// CompletenessReason explains one absent metadata field instead of
// synthesizing a value (SPEC.md §3).
type CompletenessReason struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
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
	Repo         *RepoFingerprint
	Completeness []CompletenessReason
}

// Description is one best-effort view of a session's metadata and file
// closure, read from the live source files at DescribedAt.
type Description struct {
	Source      SourceSession
	DescribedAt time.Time // UTC
	PrimarySize int64

	Meta CommonMeta

	// AdapterMetadata is canonical compact JSON (CanonicalRawMessage) or
	// nil; its schema is versioned independently of the common shape.
	AdapterMetadataSchema int
	AdapterMetadata       json.RawMessage

	// Artifacts is the session's sibling file closure beyond the primary
	// log, and Blobs the resolved content-addressed blobs it references.
	Artifacts          []SourceFile
	Blobs              []BlobRef
	UnresolvedBlobRefs []string

	// ContinuationGrade is true only when the adapter observed the
	// complete artifact/blob closure required to continue the session.
	// Unresolved references force false.
	ContinuationGrade bool
}

// Adapter is the harness source-adapter port.
type Adapter interface {
	// Harness returns the stable lowercase harness name: "omp", "codex",
	// or "claude".
	Harness() string
	// Schema returns the adapter metadata schema version.
	Schema() int
	// DefaultRoots returns the default local source roots to scan.
	DefaultRoots() []string
	// BackupRoots returns the local roots `archive push` must capture: a
	// superset of DefaultRoots. OMP adds its content-addressed blob store,
	// which holds referenced content but no discoverable sessions.
	BackupRoots() []string
	// Discover enumerates locally available sessions under roots. Roots
	// that do not exist are skipped silently; unreadable content degrades
	// per session, never by aborting the scan.
	Discover(ctx context.Context, roots []string) ([]SourceSession, error)
	// Describe reads one session's metadata and file closure in place,
	// without copying. Concurrent source modification degrades to a
	// best-effort description, never an error.
	Describe(ctx context.Context, src SourceSession) (*Description, error)
}

// ArchivedFile is one file inside a snapshot, as the repository records it.
//
// Paths are absolute in the source machine's layout, so they may name a home
// directory that does not exist here - that is the point: cross-host fetch
// identifies another machine's sessions without those files being local.
//
// This type deliberately mirrors internal/restic.Entry rather than importing
// it, keeping the adapter port free of the storage engine.
type ArchivedFile struct {
	Path string
	Size int64
}

// ArchivedSession is one session identified from a snapshot's file listing.
//
// It is metadata only. Nothing has been downloaded: identification reads the
// snapshot's tree, and Files names what a fetch would have to restore.
type ArchivedSession struct {
	// SourceID is the same adapter-defined identity Discover assigns to the
	// session when its files are local, so a session archived from one machine
	// and discovered on another carries one identity.
	SourceID string
	// PrimaryPath is the session's main log, as recorded in the snapshot.
	PrimaryPath string
	// PrimarySize is that log's size in the snapshot.
	PrimarySize int64
	// Files is the session's closure inside this snapshot: the primary log,
	// its sibling artifacts, and any content-addressed blobs the snapshot
	// happens to contain. A blob the snapshot lacks is simply absent - the
	// listing cannot say whether the session referenced it, because that
	// requires reading the log.
	Files []string
}

// SnapshotIdentifier is implemented by adapters that can recognize their own
// sessions in an archived file listing.
//
// Identification is pure: it takes a listing and returns sessions, with no
// filesystem or network access. An entry it does not recognize is ignored
// rather than rejected, because one snapshot holds several harnesses' trees.
type SnapshotIdentifier interface {
	// IdentifyArchived enumerates this adapter's sessions in a snapshot
	// listing. Results are ordered by SourceID and deduplicated, matching
	// Discover's contract so the two can be compared.
	IdentifyArchived(files []ArchivedFile) ([]ArchivedSession, error)
}

// ValidSourceID validates adapter-defined source identities: one or more
// "/"-separated segments of [A-Za-z0-9._-]+, no empty or "." / ".."
// segments, at most 512 bytes.
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

// MarshalCanonical produces canonical bytes of a metadata document:
// encoding/json output of the Go structs — fields in declaration order, no
// insignificant whitespace, times in UTC RFC3339(Nano). json.RawMessage
// values must already be canonical (see CanonicalRawMessage).
func MarshalCanonical(v any) ([]byte, error) {
	return json.Marshal(v)
}

// CanonicalRawMessage validates and compacts adapter-supplied metadata so
// it is deterministic. It requires a JSON object.
func CanonicalRawMessage(raw []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("adapter: metadata must be a JSON object")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return nil, fmt.Errorf("adapter: invalid metadata: %w", err)
	}
	return json.RawMessage(buf.Bytes()), nil
}
