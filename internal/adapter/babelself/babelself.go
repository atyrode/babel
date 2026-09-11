// Package babelself implements the source adapter for Babel's own analysis
// sessions (SPEC.md §3). It is the fourth harness beside OMP, Codex and
// Claude Code, and the only one whose logs Babel writes itself.
//
// It exists because an analysis run is the one conversation Babel used to
// forget. A receipt records the boundary — the profile, the grant, every
// tool call and its decision — and deliberately not the transcript
// (internal/worker/receipt.go), so the reasoning that produced a finding
// was legible for as long as the process lived and then gone. Recording it
// as a session means Babel's own runs are archived, browsable and
// recoverable by exactly the machinery that already carries every other
// harness: one adapter, one restic snapshot, one session view.
//
// The on-disk layout this adapter owns is:
//
//	<data dir>/babel/analysis/<run id>/<job>.jsonl
//
// One directory per exploration, one log per supervised job inside it, which
// is the same shape OMP's sessions root has — a "*.jsonl" one directory
// below the root — so discovery is the same two-level rule and a source id
// is "<run id>/<job>".
//
// A log is written in OMP's record language: a {"type":"title"} record, a
// {"type":"session"} header, then one {"type":"message"} record per message.
// That is not convenience. The conversation these logs record *is* OMP's own
// message objects, reported verbatim in the engine's agent_end frames, and
// restating them in a vocabulary of Babel's own would be Babel asserting a
// schema over content it does not own — the same reason internal/worker
// carries a served payload as raw JSON rather than as a Go type.
//
// What a log must never hold is a retrieved corpus excerpt. §9 forbids
// Babel's durable records becoming a second plaintext copy of the archive,
// and a served search result is exactly that (internal/explore/retrieval.go
// serve). The writer in writer.go reduces every served tool result to the
// locators that recover its bytes; this file only reads what that writer
// produced.
package babelself

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/digest"
)

const (
	// harnessName is the stable lowercase harness name.
	harnessName = "babel"
	// adapterSchema is the adapter_schema version of this adapter's
	// discovery and description behavior.
	adapterSchema = 1
	// adapterMetadataSchema versions the babel-specific metadata object
	// independently of the common description shape.
	adapterMetadataSchema = 1

	// dataDirName and analysisSubdir compose the root: Babel's private
	// data directory (SPEC.md §9) and the one tree inside it that holds
	// analysis sessions. It is deliberately not "sessions", which is where
	// a fetch materializes *other* harnesses' logs out of the archive: a
	// root that mixed Babel's own transcripts with restored copies of the
	// corpus would make the two indistinguishable to every later reader.
	dataDirName    = "babel"
	analysisSubdir = "analysis"

	// sessionExt is the extension a log is named with, and it is part of the
	// identity rather than decoration. The layout here — one "*.jsonl" a
	// directory below a root — is OMP's layout exactly, so under a root an
	// operator named explicitly ("babel sessions list --roots DIR", which
	// every adapter is asked to scan) the plain extension would make this
	// adapter claim OMP's sessions as its own and then report the harness
	// "babel" for records OMP wrote. The compound extension is the
	// discriminator, and it works the same for a local walk and for a
	// snapshot listing, where no bytes may be read at all.
	sessionExt = ".babel.jsonl"

	// headerScanLimit and headerScanRecords bound best-effort metadata
	// extraction, as in the OMP adapter: the title and session records are
	// the first two records of a well-formed log, and a malformed head
	// never costs a full read.
	headerScanLimit   = 1 << 20
	headerScanRecords = 4
)

// Adapter is the source adapter for Babel's own analysis sessions. It is
// stateless and safe for concurrent use.
type Adapter struct{}

// New returns the adapter for Babel's own analysis sessions.
func New() *Adapter { return &Adapter{} }

var (
	_ adapter.Adapter            = (*Adapter)(nil)
	_ adapter.SnapshotIdentifier = (*Adapter)(nil)
)

// Harness returns the stable lowercase harness name.
func (*Adapter) Harness() string { return harnessName }

// Schema returns the adapter_schema version of this adapter.
func (*Adapter) Schema() int { return adapterSchema }

// DefaultRoots returns the single analysis-session root. It returns nil when
// neither XDG_DATA_HOME nor a home directory can be resolved; a caller that
// configures roots explicitly never depends on this.
func (*Adapter) DefaultRoots() []string {
	root, ok := Root()
	if !ok {
		return nil
	}
	return []string{root}
}

// BackupRoots is DefaultRoots. The interface asks for a superset and this
// adapter genuinely has nothing to add: a log references no blobs, has no
// sibling artifact tree, and is the whole of what the session is, so the
// session root is already the complete closure `archive push` must capture.
func (a *Adapter) BackupRoots() []string { return a.DefaultRoots() }

// Root resolves the analysis-session root, "<data dir>/babel/analysis".
//
// It resolves the data directory itself rather than taking one from its
// caller, and both halves of that matter. An adapter is a port the CLI
// depends on and not the other way round, so it cannot read the CLI's own
// directory resolution; and a root supplied per call could let a run write
// its transcript into one tree while Discover looked in another, which is
// the one failure that would leave a recorded session invisible. The rule
// here is therefore the same rule Babel's data directory follows:
// XDG_DATA_HOME when set, else ~/.local/share.
func Root() (string, bool) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, dataDirName, analysisSubdir), true
}

// Discover enumerates analysis session logs under roots: a regular "*.jsonl"
// file one directory below a root, whose two path components are already a
// valid source id. They are valid by construction — the writer refuses to
// open a log it could not name — so a component that does not fit names a
// file this adapter did not write and is skipped rather than sanitized into
// an identity nothing would reproduce. Roots and run directories that do not
// exist or cannot be read are skipped silently; results are ordered by
// source id and deduplicated across overlapping roots.
func (*Adapter) Discover(ctx context.Context, roots []string) ([]adapter.SourceSession, error) {
	var found []adapter.SourceSession
	seen := make(map[string]struct{})
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		runs, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, runDir := range runs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !runDir.IsDir() {
				continue
			}
			dir := filepath.Join(root, runDir.Name())
			entries, err := os.ReadDir(dir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), sessionExt) {
					continue
				}
				id := runDir.Name() + "/" + strings.TrimSuffix(entry.Name(), sessionExt)
				if !adapter.ValidSourceID(id) {
					continue
				}
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				found = append(found, adapter.SourceSession{
					Harness:     harnessName,
					SourceID:    id,
					PrimaryPath: filepath.Join(dir, entry.Name()),
					Hint:        id,
				})
			}
		}
	}
	slices.SortFunc(found, func(a, b adapter.SourceSession) int {
		return strings.Compare(a.SourceID, b.SourceID)
	})
	return found, nil
}

// IdentifyArchived recognizes analysis sessions in a snapshot's file listing,
// the cross-host twin of Discover: the files belong to another machine, so
// nothing may be read and the layout alone carries the identity.
//
// It applies Discover's rule to paths instead of directory entries: a
// "*.jsonl" file two levels below a path segment named "analysis", with both
// components already a valid source id. The closure is the log itself,
// because that is all a session is here. Entries that do not fit are ignored,
// because one snapshot holds several harnesses' trees.
func (*Adapter) IdentifyArchived(files []adapter.ArchivedFile) ([]adapter.ArchivedSession, error) {
	sessions := make(map[string]adapter.ArchivedSession)
	for _, f := range files {
		id, ok := archivedSessionID(f.Path)
		if !ok {
			continue
		}
		// Two distinct paths cannot produce one id — the components are
		// used verbatim — so a duplicate is the same session listed twice
		// and the first one in path order wins reproducibly.
		if existing, dup := sessions[id]; dup && existing.PrimaryPath <= f.Path {
			continue
		}
		sessions[id] = adapter.ArchivedSession{
			SourceID:    id,
			PrimaryPath: f.Path,
			PrimarySize: f.Size,
			Files:       []string{f.Path},
		}
	}
	out := make([]adapter.ArchivedSession, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b adapter.ArchivedSession) int {
		return strings.Compare(a.SourceID, b.SourceID)
	})
	return out, nil
}

// archivedSessionID matches one snapshot path against the analysis-session
// layout, returning the source id Discover would assign the same file.
func archivedSessionID(p string) (string, bool) {
	// The extension is tested first because it rejects most of a snapshot's
	// entries before their paths are split.
	if !strings.HasSuffix(p, sessionExt) {
		return "", false
	}
	// Snapshot paths are "/"-separated whatever host reads them, so they are
	// split explicitly rather than with filepath, whose separator is the
	// local machine's.
	segs := strings.Split(p, "/")
	n := len(segs)
	if n < 3 || segs[n-3] != analysisSubdir {
		return "", false
	}
	id := segs[n-2] + "/" + strings.TrimSuffix(segs[n-1], sessionExt)
	if !adapter.ValidSourceID(id) {
		return "", false
	}
	return id, true
}

// Describe reads one analysis session in place: the log's digest and size and
// the header records at its head. There is no artifact tree and no blob
// store to resolve, so the description is complete as soon as the head is
// read, and continuation grade holds: the log references nothing outside
// itself, which is the whole of the closure the interface asks about.
//
// It is read in place and never copied, and a run still appending to it
// yields a slightly older or slightly newer view that the next call
// supersedes — the same best-effort contract every adapter keeps.
func (*Adapter) Describe(ctx context.Context, src adapter.SourceSession) (*adapter.Description, error) {
	if src.Harness != "" && src.Harness != harnessName {
		return nil, fmt.Errorf("babelself: session %q belongs to harness %q", src.SourceID, src.Harness)
	}
	if src.PrimaryPath == "" {
		return nil, fmt.Errorf("babelself: session %q has no primary path", src.SourceID)
	}

	info, err := os.Stat(src.PrimaryPath)
	if err != nil {
		return nil, err
	}
	primaryDigest, primarySize, err := digestFile(ctx, src.PrimaryPath)
	if err != nil {
		return nil, err
	}

	head := readHeader(src.PrimaryPath)
	meta := head.commonMeta(info.ModTime())

	rawMeta, err := adapter.MarshalCanonical(&adapterMetadata{
		RunID:         head.runID,
		Job:           head.job,
		SessionID:     head.sessionID,
		RecordVersion: head.recordVersion,
		Profile:       head.profile,
		Recipes:       head.recipes,
		PrimaryDigest: primaryDigest,
		PrimarySize:   primarySize,
	})
	if err != nil {
		return nil, err
	}
	canonicalMeta, err := adapter.CanonicalRawMessage(rawMeta)
	if err != nil {
		return nil, err
	}

	return &adapter.Description{
		Source: adapter.SourceSession{
			Harness:     harnessName,
			SourceID:    src.SourceID,
			PrimaryPath: src.PrimaryPath,
			Hint:        src.Hint,
		},
		DescribedAt:           time.Now().UTC(),
		PrimarySize:           primarySize,
		Meta:                  meta,
		AdapterMetadataSchema: adapterMetadataSchema,
		AdapterMetadata:       canonicalMeta,
		ContinuationGrade:     true,
	}, nil
}

// adapterMetadata is the versioned babel-specific metadata document. Field
// order is the canonical encoding order (adapter.MarshalCanonical).
type adapterMetadata struct {
	RunID         string        `json:"run_id,omitempty"`
	Job           string        `json:"job,omitempty"`
	SessionID     string        `json:"session_id,omitempty"`
	RecordVersion int           `json:"record_version,omitempty"`
	Profile       string        `json:"profile,omitempty"`
	Recipes       []string      `json:"recipes,omitempty"`
	PrimaryDigest digest.Digest `json:"primary_digest"`
	PrimarySize   int64         `json:"primary_size"`
}

// digestFile returns the canonical digest and size of a file's live bytes.
func digestFile(ctx context.Context, path string) (digest.Digest, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return digest.Compute(f)
}

// header is the best-effort metadata read from a log's head records.
type header struct {
	title         string
	sessionID     string
	recordVersion int
	runID         string
	job           string
	profile       string
	recipes       []string
	workspace     string
	createdAt     time.Time
	hasCreatedAt  bool
}

// readHeader extracts metadata from the head of a log. Every absent value
// stays absent: a log whose head is truncated — a run killed between the
// title record and the session record — yields a header whose empty fields
// become explicit completeness reasons rather than synthesized values.
func readHeader(path string) header {
	var h header
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, headerScanLimit))
	for range headerScanRecords {
		var rec headRecord
		if err := dec.Decode(&rec); err != nil {
			return h
		}
		switch rec.Type {
		case recordTitle:
			if h.title == "" {
				h.title = rec.Title
			}
		case recordSession:
			h.sessionID = rec.ID
			h.recordVersion = rec.Version
			h.runID = rec.RunID
			h.job = rec.Job
			h.profile = rec.Profile
			h.recipes = rec.Recipes
			h.workspace = rec.CWD
			if h.title == "" {
				h.title = rec.Title
			}
			if t, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
				h.createdAt = t.UTC()
				h.hasCreatedAt = true
			}
			return h
		}
	}
	return h
}

// commonMeta converts the header plus the log's modification time into the
// portable catalog fields. Absent values are nil and each nil carries a
// completeness reason; nothing is synthesized to fill a shape.
func (h header) commonMeta(sourceModTime time.Time) adapter.CommonMeta {
	var meta adapter.CommonMeta
	missing := func(field, reason string) {
		meta.Completeness = append(meta.Completeness, adapter.CompletenessReason{Field: field, Reason: reason})
	}

	if h.title != "" {
		title := h.title
		meta.Title = &title
		// The title is in the log because the writer put it there, composed
		// from the run's own identity. No model wrote it and nothing about
		// it is inferred, so it is recorded provenance in exactly the sense
		// adapter.TitleRecorded means.
		meta.TitleProvenance = adapter.TitleRecorded
	} else {
		missing("title", "session log carries no non-empty title record")
	}
	if h.workspace != "" {
		workspace := h.workspace
		meta.Workspace = &workspace
	} else {
		missing("workspace", "session record carries no cwd")
	}
	if h.hasCreatedAt {
		created := h.createdAt
		meta.CreatedAt = &created
	} else {
		missing("created_at", "session record carries no parsable timestamp")
	}
	modified := sourceModTime.UTC()
	meta.ModifiedAt = &modified

	missing("lifecycle", "an analysis session is complete when its run ends and carries no lifecycle state")
	missing("repo", "repository fingerprint would require reading the recorded workspace, which the adapter never opens")
	// Usage is the one absence worth stating twice, because the figures do
	// exist: the run's receipt records what the engine reported it spent
	// (internal/worker.Usage). They are not in the session log, so summing
	// them here would mean this adapter reading a different document than
	// the one it describes.
	missing("usage", "an analysis session log records the conversation; the run's receipt records what it cost")
	return meta
}
