package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"

	"github.com/atyrode/babel/internal/transcript"
)

var (
	// ErrBadRequest lets an injected operation reject an invalid request.
	ErrBadRequest = errors.New("bad request")
	// ErrNotFound lets an injected operation report a missing selector or snapshot.
	ErrNotFound = errors.New("not found")
	// ErrConflict lets an injected operation report unavailable configured state.
	ErrConflict = errors.New("conflict")
)

// Options supplies the server's narrow application dependencies.
type Options struct {
	Port        int
	Static      fs.FS
	Diagnostics io.Writer
	State       StateProvider
	Lister      SessionLister
	Scanner     Scanner
	Inspector   SessionInspector
	Archive     ArchiveOperations
	Transcripts TranscriptReader
}

// State is the non-secret subset of persistent storage configuration exposed
// by GET /api/state.
type State struct {
	Configured bool   `json:"configured"`
	Repository string `json:"repository"`
	HostID     string `json:"host_id"`
}

// StateProvider supplies current storage state without exposing password data.
type StateProvider interface {
	WebState(context.Context) (State, error)
}

// StateProviderFunc adapts a function to StateProvider.
type StateProviderFunc func(context.Context) (State, error)

func (f StateProviderFunc) WebState(ctx context.Context) (State, error) { return f(ctx) }

// SessionRow mirrors internal/cli sessionRow field-for-field.
type SessionRow struct {
	Harness           string  `json:"harness"`
	SourceID          string  `json:"source_id"`
	Selector          string  `json:"selector"`
	Size              int64   `json:"size"`
	Modified          *string `json:"modified"`
	Title             *string `json:"title"`
	Workspace         *string `json:"workspace"`
	ContinuationGrade bool    `json:"continuation_grade"`
}

// ScanState mirrors internal/cli scanState field-for-field. It reports the
// background catalog scan that describes sessions: describing a large corpus
// takes minutes, so every listing surface reports its progress rather than
// waiting on it.
type ScanState struct {
	Running    bool   `json:"running"`
	Described  int    `json:"described"`
	Total      int    `json:"total"`
	Failed     int    `json:"failed"`
	Harness    string `json:"harness,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// Scanner reports and starts the background catalog scan behind
// GET /api/scan and POST /api/sessions/refresh. Both methods return
// immediately: a scan is owned by the server process, never by the request
// that asked for it, so a canceled request never discards described
// sessions and concurrent requests share one scan.
type Scanner interface {
	State() ScanState
	StartRefresh() ScanState
}

// SessionsResult extends internal/cli sessionsResult with the cache refresh
// time and the scan state required by GET /api/sessions. The rows are
// whatever the catalog already holds, which is why they are served without
// waiting for the scan the request may have started.
type SessionsResult struct {
	Sessions    []SessionRow `json:"sessions"`
	RefreshedAt string       `json:"refreshed_at"`
	Scan        ScanState    `json:"scan"`
}

// SessionLister supplies the cached local session listing.
type SessionLister interface {
	ListSessions(context.Context) (SessionsResult, error)
}

// SessionListerFunc adapts a function to SessionLister.
type SessionListerFunc func(context.Context) (SessionsResult, error)

func (f SessionListerFunc) ListSessions(ctx context.Context) (SessionsResult, error) { return f(ctx) }

// CompletenessRow mirrors internal/cli completenessRow field-for-field.
type CompletenessRow struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// RepoRow mirrors internal/cli repoRow field-for-field.
type RepoRow struct {
	Remote string `json:"remote,omitempty"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
}

// FileRow mirrors internal/cli fileRow field-for-field.
type FileRow struct {
	RelPath    string `json:"rel_path"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
}

// BlobRow mirrors internal/cli blobRow field-for-field.
type BlobRow struct {
	Digest     string `json:"digest"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
}

// InspectResult mirrors internal/cli inspectResult field-for-field.
type InspectResult struct {
	Harness     string `json:"harness"`
	SourceID    string `json:"source_id"`
	Selector    string `json:"selector"`
	PrimaryPath string `json:"primary_path"`
	PrimarySize int64  `json:"primary_size"`
	DescribedAt string `json:"described_at"`
	Hint        string `json:"hint,omitempty"`

	Title        *string           `json:"title"`
	Workspace    *string           `json:"workspace"`
	CreatedAt    *string           `json:"created_at"`
	ModifiedAt   *string           `json:"modified_at"`
	Lifecycle    *string           `json:"lifecycle"`
	Repo         *RepoRow          `json:"repo"`
	Completeness []CompletenessRow `json:"completeness,omitempty"`

	AdapterMetadataSchema int             `json:"adapter_metadata_schema"`
	AdapterMetadata       json.RawMessage `json:"adapter_metadata,omitempty"`

	Artifacts          []FileRow `json:"artifacts,omitempty"`
	Blobs              []BlobRow `json:"blobs,omitempty"`
	UnresolvedBlobRefs []string  `json:"unresolved_blob_refs,omitempty"`
	ContinuationGrade  bool      `json:"continuation_grade"`
}

// SessionInspector resolves and describes one selector.
type SessionInspector interface {
	InspectSession(context.Context, string) (InspectResult, error)
}

// SessionInspectorFunc adapts a function to SessionInspector.
type SessionInspectorFunc func(context.Context, string) (InspectResult, error)

func (f SessionInspectorFunc) InspectSession(ctx context.Context, selector string) (InspectResult, error) {
	return f(ctx, selector)
}

// StatusHostRow mirrors internal/cli statusHostRow field-for-field.
type StatusHostRow struct {
	Host          string   `json:"host"`
	Snapshots     int      `json:"snapshots"`
	LatestTime    string   `json:"latest_time"`
	LatestID      string   `json:"latest_id"`
	LatestShortID string   `json:"latest_short_id"`
	Tags          []string `json:"tags,omitempty"`
}

// StatusResult mirrors internal/cli statusResult field-for-field.
type StatusResult struct {
	Repository string          `json:"repository"`
	Snapshots  int             `json:"snapshots"`
	Hosts      []StatusHostRow `json:"hosts"`
}

// VerifyResult mirrors internal/cli verifyResult field-for-field.
type VerifyResult struct {
	Repository string `json:"repository"`
	Deep       bool   `json:"deep"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// FetchResult mirrors internal/cli fetchResult field-for-field.
type FetchResult struct {
	Selector        string   `json:"selector"`
	SnapshotID      string   `json:"snapshot_id"`
	SnapshotShortID string   `json:"snapshot_short_id"`
	SnapshotTime    string   `json:"snapshot_time"`
	Target          string   `json:"target"`
	Files           int      `json:"files"`
	Bytes           int64    `json:"bytes"`
	Included        []string `json:"included"`
	Missing         []string `json:"missing,omitempty"`
	AlreadyPresent  bool     `json:"already_present"`
}

// ArchiveOperations is the read/restore-only repository surface. Deliberately
// no forget or prune operation is representable here.
type ArchiveOperations interface {
	ArchiveStatus(context.Context) (StatusResult, error)
	ArchiveVerify(context.Context, bool) (VerifyResult, error)
	FetchSession(context.Context, string, string) (FetchResult, error)
}

// TranscriptReader turns an inspected primary log into display events.
type TranscriptReader interface {
	Events(path, harness string, offset, limit int) (int, []transcript.Event, error)
}

// TranscriptReaderFunc adapts a function to TranscriptReader.
type TranscriptReaderFunc func(string, string, int, int) (int, []transcript.Event, error)

func (f TranscriptReaderFunc) Events(path, harness string, offset, limit int) (int, []transcript.Event, error) {
	return f(path, harness, offset, limit)
}
