package archive

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Schema versions of the babel/v1 archive documents. Any semantic change
// to canonical bytes requires an increment and a compatibility rule.
const (
	ManifestSchemaVersion = 1
	SegmentSchemaVersion  = 1
	IndexSchemaVersion    = 1
	CommitSchemaVersion   = 1
	HintSchemaVersion     = 1
)

// MaxChainDepth bounds append-delta chains: a revision whose chain depth
// would exceed this limit is published as a full revision instead, so
// restore walks a short verified chain and a single damaged object cannot
// strand long history. Freeze-gate tunable (SPEC.md decision 40).
const MaxChainDepth = 24

// Encoding identifies how a bundle revision's payload object encodes the
// session plaintext.
type Encoding string

const (
	// EncodingFull marks a payload object containing the complete raw bytes.
	EncodingFull Encoding = "full"
	// EncodingAppendDelta marks a payload object containing only the bytes
	// appended after the parent revision's plaintext, valid only when that
	// parent plaintext is an exact byte prefix of the new content.
	EncodingAppendDelta Encoding = "append-delta"
)

// ObjectRef references an immutable content-addressed object by plaintext
// digest and size.
type ObjectRef struct {
	Digest Digest `json:"digest"`
	Size   int64  `json:"size"`
}

// FileRef references one artifact file of a session's declared closure.
// Path is the source-relative path inside the session's artifact tree.
type FileRef struct {
	Path   string `json:"path"`
	Digest Digest `json:"digest"`
	Size   int64  `json:"size"`
}

// RepoFingerprint records the best-effort repository identity observed for
// a session's workspace.
type RepoFingerprint struct {
	Remote string `json:"remote,omitempty"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
	Dirty  *bool  `json:"dirty,omitempty"`
}

// CompletenessReason explains one absent nullable field instead of
// synthesizing a value (SPEC.md §3).
type CompletenessReason struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// ManifestEntry is one immutable session revision in a manifest segment:
// the portable common envelope plus namespaced adapter metadata.
// Absent nullable fields are omitted from the canonical bytes and carry a
// CompletenessReason.
type ManifestEntry struct {
	ManifestSchema  int    `json:"manifest_schema"`
	Harness         string `json:"harness"`
	AdapterSchema   int    `json:"adapter_schema"`
	HostID          string `json:"host_id"`
	SourceID        string `json:"source_id"`
	SessionKey      string `json:"session_key"`
	RevisionKey     string `json:"revision_key"`
	GenerationAdded uint64 `json:"generation_added"`

	SnapshotTime time.Time `json:"snapshot_time"`

	// Content describes the reassembled plaintext; Object the stored
	// payload (identical to Content for full revisions, the appended tail
	// for append-delta revisions).
	Encoding       Encoding  `json:"encoding"`
	Content        ObjectRef `json:"content"`
	Object         ObjectRef `json:"object"`
	ParentRevision string    `json:"parent_revision,omitempty"`
	ChainDepth     int       `json:"chain_depth,omitempty"`

	Title      *string          `json:"title,omitempty"`
	Workspace  *string          `json:"workspace,omitempty"`
	CreatedAt  *time.Time       `json:"created_at,omitempty"`
	ModifiedAt *time.Time       `json:"modified_at,omitempty"`
	Lifecycle  *string          `json:"lifecycle,omitempty"`
	Repo       *RepoFingerprint `json:"repo,omitempty"`

	Completeness []CompletenessReason `json:"completeness,omitempty"`

	Artifacts          []FileRef   `json:"artifacts,omitempty"`
	Blobs              []ObjectRef `json:"blobs,omitempty"`
	UnresolvedBlobRefs []string    `json:"unresolved_blob_refs,omitempty"`
	ContinuationGrade  bool        `json:"continuation_grade"`

	AdapterMetadataSchema int             `json:"adapter_metadata_schema,omitempty"`
	AdapterMetadata       json.RawMessage `json:"adapter_metadata,omitempty"`
}

// Segment is one content-addressed manifest partition. Entries are sorted
// by (SessionKey, RevisionKey) and the segment's canonical bytes are
// reused verbatim across generations while no member entry changes.
type Segment struct {
	SegmentSchema int             `json:"segment_schema"`
	Partition     string          `json:"partition"`
	Entries       []ManifestEntry `json:"entries"`
}

// SegmentRef references one segment from a generation index.
type SegmentRef struct {
	Partition string    `json:"partition"`
	Object    ObjectRef `json:"object"`
	Entries   int       `json:"entries"`
}

// GenerationIndex is the small per-generation document referencing every
// manifest segment of a complete generation.
type GenerationIndex struct {
	IndexSchema int          `json:"index_schema"`
	HostID      string       `json:"host_id"`
	Generation  uint64       `json:"generation"`
	CreatedAt   time.Time    `json:"created_at"`
	Segments    []SegmentRef `json:"segments"`
	Sessions    int          `json:"sessions"`
	Revisions   int          `json:"revisions"`
}

// AdapterCoverage records one adapter's scan coverage inside a commit
// record: scanned, published, carried forward, and deferred counts plus
// bootstrap completeness (SPEC.md §6.1).
type AdapterCoverage struct {
	Harness         string   `json:"harness"`
	AdapterSchema   int      `json:"adapter_schema"`
	Scanned         int      `json:"scanned"`
	Published       int      `json:"published"`
	CarriedForward  int      `json:"carried_forward"`
	Deferred        int      `json:"deferred"`
	DeferredReasons []string `json:"deferred_reasons,omitempty"`
	Complete        bool     `json:"complete"`
}

// CommitRecord is the immutable publication boundary: a generation exists
// exactly when its digest-valid commit record is durable and readable.
type CommitRecord struct {
	CommitSchema      int               `json:"commit_schema"`
	HostID            string            `json:"host_id"`
	HostDisplayName   string            `json:"host_display_name,omitempty"`
	Generation        uint64            `json:"generation"`
	CreatedAt         time.Time         `json:"created_at"`
	Index             ObjectRef         `json:"index"`
	Coverage          []AdapterCoverage `json:"coverage"`
	Bootstrap         bool              `json:"bootstrap"`
	BootstrapComplete bool              `json:"bootstrap_complete"`
	BabelVersion      string            `json:"babel_version,omitempty"`
}

// LatestHint is the small mutable pointer replaced after a durable commit.
// It is a non-authoritative hint: readers validate it and always fall back
// to the verified-record scan.
type LatestHint struct {
	HintSchema int       `json:"hint_schema"`
	HostID     string    `json:"host_id"`
	Generation uint64    `json:"generation"`
	Commit     ObjectRef `json:"commit"`
}

// MarshalCanonical produces the canonical bytes of an archive document.
//
// Canonical form: encoding/json output of the Go contract structs — struct
// fields in declaration order, map keys sorted, no insignificant
// whitespace, times in UTC RFC3339(Nano). json.RawMessage values must
// already be canonical (see CanonicalRawMessage). Producing a document any
// other way is a contract violation.
func MarshalCanonical(v any) ([]byte, error) {
	return json.Marshal(v)
}

// CanonicalRawMessage validates and compacts adapter-supplied metadata so
// it is deterministic inside canonical segment bytes. It requires a JSON
// object.
func CanonicalRawMessage(raw []byte) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] != '{' {
		return nil, fmt.Errorf("archive: adapter metadata must be a JSON object")
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return nil, fmt.Errorf("archive: invalid adapter metadata: %w", err)
	}
	return json.RawMessage(buf.Bytes()), nil
}
