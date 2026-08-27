// Package claude implements the Claude Code source adapter (SPEC.md §3).
//
// Claude Code keeps one JSONL transcript per session at
// <root>/projects/<project-dir>/<session-uuid>.jsonl. The project
// directory name is a lossy encoding of the workspace path: path
// separators and several punctuation characters collapse to "-", so the
// absolute workspace path cannot be recovered from it. Trees named after
// the same session UUID — the sibling <session-uuid>/ directory inside the
// project directory plus <root>/tasks/<session-uuid>/ and
// <root>/session-env/<session-uuid>/ — hold the session-linked local
// artifacts the format exposes.
//
// The adapter always preserves the raw transcript bytes. The format is
// undocumented and unstable, so title, project identity, lifecycle,
// timestamps beyond filesystem observation, repository fingerprint, and
// artifact closure are allowed to be unavailable; every absent value
// carries an explicit archive.CompletenessReason instead of a synthesized
// substitute. Claude Code declares no referenced-artifact closure, so
// snapshots are never continuation grade.
package claude

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/archive"
)

const (
	// harnessName is the stable lowercase harness name recorded in
	// manifests.
	harnessName = "claude"
	// adapterSchema is the adapter_schema version of this adapter's
	// discovery and staging behavior.
	adapterSchema = 1
	// metadataSchema versions the adapter_metadata object independently of
	// the manifest envelope.
	metadataSchema = 1

	// projectsDirName is the root-relative directory holding one
	// directory per encoded workspace.
	projectsDirName = "projects"
	// sessionExt is the extension of a session transcript file.
	sessionExt = ".jsonl"

	// maxSegmentLen bounds one SourceID segment; longer names are
	// shortened deterministically so the identity stays stable across
	// runs and the two-segment identity stays inside the 512-byte
	// archive.ValidSourceID limit.
	maxSegmentLen = 200
	// maxRecordBytes bounds the bytes retained while parsing one
	// transcript line for metadata. Longer lines are counted and skipped
	// for metadata only; the staged copy keeps every byte verbatim.
	maxRecordBytes = 1 << 20
	// copyBufBytes is the staging copy buffer size.
	copyBufBytes = 128 << 10
)

// sessionArtifactDirs are root-relative trees whose immediate children are
// named after a session UUID, which is the only positive association the
// on-disk format offers.
var sessionArtifactDirs = []string{"tasks", "session-env"}

// stagedPrimaryHook runs, when non-nil, after the primary transcript has
// been copied and before stability is re-verified. Only tests set it, to
// make a concurrent source change deterministic.
var stagedPrimaryHook func()

// Adapter is the Claude Code source adapter. It holds no state and is safe
// for concurrent use.
type Adapter struct{}

// New returns the Claude Code source adapter.
func New() *Adapter { return &Adapter{} }

// Harness returns the stable lowercase harness name.
func (*Adapter) Harness() string { return harnessName }

// Schema returns the adapter_schema version recorded in manifests.
func (*Adapter) Schema() int { return adapterSchema }

// DefaultRoots returns the Claude Code home directory. It returns nil when
// the user home directory cannot be resolved; a caller may always pass
// explicit roots instead.
func (*Adapter) DefaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{filepath.Join(home, ".claude")}
}

// Discover enumerates one session per transcript file directly inside a
// project directory. Missing or unreadable roots and project directories
// are skipped so a single unreadable tree never aborts the scan. Results
// are ordered by SourceID and duplicate identities across roots are kept
// once.
func (*Adapter) Discover(ctx context.Context, roots []string) ([]adapter.SourceSession, error) {
	var out []adapter.SourceSession
	seen := make(map[string]struct{})
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		projectsPath := filepath.Join(root, projectsDirName)
		projects, err := os.ReadDir(projectsPath)
		if err != nil {
			continue
		}
		for _, project := range projects {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !project.IsDir() {
				continue
			}
			entries, err := os.ReadDir(filepath.Join(projectsPath, project.Name()))
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), sessionExt) {
					continue
				}
				session := strings.TrimSuffix(entry.Name(), sessionExt)
				if session == "" {
					continue
				}
				id := sourceID(project.Name(), session)
				if _, dup := seen[id]; dup {
					continue
				}
				seen[id] = struct{}{}
				out = append(out, adapter.SourceSession{
					Harness:     harnessName,
					SourceID:    id,
					PrimaryPath: filepath.Join(projectsPath, project.Name(), entry.Name()),
					Hint:        project.Name(),
				})
			}
		}
	}
	slices.SortFunc(out, func(a, b adapter.SourceSession) int {
		return strings.Compare(a.SourceID, b.SourceID)
	})
	return out, nil
}

// Snapshot stages the raw transcript plus every positively associated
// session-linked artifact into stagingDir, mirroring the root-relative
// source layout. The digest of the staged transcript bytes is compared
// against a re-read of the source afterwards; any change during staging
// returns a wrapped adapter.ErrUnstable and no snapshot. Metadata is
// derived from the staged bytes, so it always describes exactly what was
// published, and a malformed transcript still yields a snapshot of the
// raw bytes.
func (*Adapter) Snapshot(ctx context.Context, src adapter.SourceSession, stagingDir string) (*adapter.Snapshot, error) {
	if src.Harness != harnessName {
		return nil, fmt.Errorf("claude: source session harness %q is not %q", src.Harness, harnessName)
	}
	if !archive.ValidSourceID(src.SourceID) {
		return nil, fmt.Errorf("claude: invalid source id %q", src.SourceID)
	}
	root, project, session, err := splitPrimaryPath(src.PrimaryPath)
	if err != nil {
		return nil, err
	}
	if stagingDir == "" {
		return nil, errors.New("claude: empty staging directory")
	}

	before, err := os.Stat(src.PrimaryPath)
	if err != nil {
		return nil, fmt.Errorf("claude: stat transcript: %w", err)
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("claude: transcript %q is not a regular file", src.PrimaryPath)
	}

	relPrimary := path.Join(projectsDirName, project, session+sessionExt)
	stagedPrimary := filepath.Join(stagingDir, filepath.FromSlash(relPrimary))
	stagedDigest, stagedSize, err := stageFile(ctx, src.PrimaryPath, stagedPrimary)
	if err != nil {
		return nil, err
	}

	if stagedPrimaryHook != nil {
		stagedPrimaryHook()
	}

	sourceDigest, _, err := digestFile(ctx, src.PrimaryPath)
	if err != nil {
		return nil, fmt.Errorf("claude: re-read transcript: %w", err)
	}
	if sourceDigest != stagedDigest {
		return nil, fmt.Errorf("claude: transcript %q changed while staging: %w", src.PrimaryPath, adapter.ErrUnstable)
	}

	scan, err := scanTranscript(ctx, stagedPrimary)
	if err != nil {
		return nil, fmt.Errorf("claude: read staged transcript: %w", err)
	}

	artifacts, failures, err := stageArtifacts(ctx, root, project, session, stagingDir)
	if err != nil {
		return nil, err
	}

	meta, workspaceSource := buildMeta(scan, project, before.ModTime(), len(artifacts), failures)

	rawMeta, err := archive.MarshalCanonical(adapterMetadata{
		ProjectDir:       project,
		SessionUUID:      session,
		PrimaryRelPath:   relPrimary,
		PrimaryDigest:    stagedDigest,
		PrimarySize:      stagedSize,
		InFileSessionID:  scan.sessionID,
		ClaudeVersion:    scan.version,
		WorkspaceSource:  workspaceSource,
		RecordCount:      scan.records,
		MalformedRecords: scan.malformed,
		OversizedRecords: scan.oversized,
		RecordTypeCounts: scan.types,
		ArtifactCount:    len(artifacts),
		ArtifactFailures: failures,
	})
	if err != nil {
		return nil, fmt.Errorf("claude: marshal adapter metadata: %w", err)
	}
	canonicalMeta, err := archive.CanonicalRawMessage(rawMeta)
	if err != nil {
		return nil, fmt.Errorf("claude: canonicalize adapter metadata: %w", err)
	}

	return &adapter.Snapshot{
		Source:                src,
		SnapshotTime:          time.Now().UTC(),
		StagedPrimary:         stagedPrimary,
		PrimarySize:           stagedSize,
		Meta:                  meta,
		AdapterMetadataSchema: metadataSchema,
		AdapterMetadata:       canonicalMeta,
		Artifacts:             artifacts,
		// Claude Code embeds attachment content inline and declares no
		// content-addressed blob store, so there is nothing to resolve and
		// no closure to guarantee.
		ContinuationGrade: false,
	}, nil
}

// adapterMetadata is the namespaced Claude Code adapter extension
// (adapter_metadata_schema 1). It records observable transcript structure
// and provenance only; transcript bodies never appear in it.
type adapterMetadata struct {
	ProjectDir       string         `json:"project_dir"`
	SessionUUID      string         `json:"session_uuid"`
	PrimaryRelPath   string         `json:"primary_rel_path"`
	PrimaryDigest    archive.Digest `json:"primary_digest"`
	PrimarySize      int64          `json:"primary_size"`
	InFileSessionID  string         `json:"in_file_session_id,omitempty"`
	ClaudeVersion    string         `json:"claude_version,omitempty"`
	WorkspaceSource  string         `json:"workspace_source"`
	RecordCount      int            `json:"record_count"`
	MalformedRecords int            `json:"malformed_records"`
	OversizedRecords int            `json:"oversized_records"`
	RecordTypeCounts map[string]int `json:"record_type_counts,omitempty"`
	ArtifactCount    int            `json:"artifact_count"`
	ArtifactFailures int            `json:"artifact_failures,omitempty"`
}

// Workspace provenance values recorded in adapter metadata.
const (
	workspaceFromTranscript = "transcript-cwd"
	workspaceFromProjectDir = "project-dir-name"
	workspaceUnavailable    = "unavailable"
)

// buildMeta derives the nullable portable catalog fields from a transcript
// scan, preferring in-file evidence over filesystem and project-directory
// observation. It returns the metadata plus the workspace provenance value
// recorded in adapter metadata.
func buildMeta(scan *transcriptScan, project string, mtime time.Time, artifacts, failures int) (adapter.CommonMeta, string) {
	var meta adapter.CommonMeta
	reason := func(field, why string) {
		meta.Completeness = append(meta.Completeness, archive.CompletenessReason{Field: field, Reason: why})
	}

	if scan.title != "" {
		title := scan.title
		meta.Title = &title
	} else {
		reason("title", "The transcript contains no ai-title record and the Claude Code format exposes no other session title.")
	}

	workspaceSource := workspaceUnavailable
	switch {
	case scan.cwd != "":
		workspace := scan.cwd
		meta.Workspace = &workspace
		workspaceSource = workspaceFromTranscript
		if scan.cwdConflict {
			reason("workspace", "The transcript recorded several distinct cwd values; the first observed value was used.")
		}
	case project != "":
		workspace := project
		meta.Workspace = &workspace
		workspaceSource = workspaceFromProjectDir
		reason("workspace", "No transcript record carried a cwd, so the value is the Claude Code project directory name; that encoding is lossy and the absolute workspace path cannot be recovered from it.")
	default:
		reason("workspace", "The transcript recorded no cwd and the session has no project directory name to fall back on.")
	}

	if !scan.first.IsZero() {
		created := scan.first
		meta.CreatedAt = &created
	} else {
		reason("created_at", "The transcript exposes no parseable record timestamp; Claude Code creation time beyond filesystem observation is unavailable.")
	}
	modified := mtime.UTC()
	if !scan.last.IsZero() {
		modified = scan.last
	}
	meta.ModifiedAt = &modified

	reason("lifecycle", "The Claude Code on-disk format records no session lifecycle state.")

	if scan.branch != "" {
		meta.Repo = &archive.RepoFingerprint{Branch: scan.branch}
		reason("repo", "Only the gitBranch recorded in the transcript is available; repository remote, commit, and dirty state are not exposed by the format.")
	} else {
		reason("repo", "No transcript record carried a gitBranch and the format exposes no repository remote, commit, or dirty state.")
	}

	if artifacts > 0 {
		reason("artifacts", "Claude Code declares no referenced-artifact closure; only session-linked trees named after the session UUID were staged, so the set may be incomplete.")
	} else {
		reason("artifacts", "Claude Code declares no referenced-artifact closure and no session-linked tree named after the session UUID was found.")
	}
	if failures > 0 {
		reason("artifacts", "One or more session-linked artifact files could not be read and were omitted from the snapshot.")
	}

	return meta, workspaceSource
}

// transcriptRecord is the small subset of transcript fields the adapter
// reads. Every other field is ignored; nothing decoded here holds message
// content.
type transcriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Cwd       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	Version   string `json:"version"`
	SessionID string `json:"sessionId"`
	AITitle   string `json:"aiTitle"`
}

// transcriptScan accumulates the observable structure of one transcript.
type transcriptScan struct {
	records   int
	malformed int
	oversized int
	types     map[string]int

	title       string
	cwd         string
	cwdConflict bool
	branch      string
	version     string
	sessionID   string
	first       time.Time
	last        time.Time
}

// scanTranscript reads a staged transcript line by line and extracts the
// best-effort metadata the format exposes. Unparseable and oversized lines
// are counted and skipped rather than failing the snapshot, because the
// raw bytes are preserved regardless.
func scanTranscript(ctx context.Context, staged string) (*transcriptScan, error) {
	f, err := os.Open(staged)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scan := &transcriptScan{types: make(map[string]int)}
	br := bufio.NewReaderSize(f, 64<<10)
	buf := make([]byte, 0, 8<<10)
	for n := 0; ; n++ {
		if n%512 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		line, oversized, rerr := readLine(br, buf)
		buf = line[:0]
		if trimmed := bytes.TrimRight(line, "\r\n"); len(trimmed) > 0 {
			scan.records++
			switch {
			case oversized:
				scan.oversized++
			default:
				scan.observe(trimmed)
			}
		}
		if rerr == io.EOF {
			return scan, nil
		}
		if rerr != nil {
			return nil, rerr
		}
	}
}

// observe folds one syntactically complete transcript line into the scan.
func (s *transcriptScan) observe(line []byte) {
	var rec transcriptRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		s.malformed++
		return
	}
	if rec.Type != "" {
		s.types[rec.Type]++
	}
	if rec.AITitle != "" {
		s.title = rec.AITitle
	}
	if rec.Cwd != "" {
		switch {
		case s.cwd == "":
			s.cwd = rec.Cwd
		case s.cwd != rec.Cwd:
			s.cwdConflict = true
		}
	}
	if rec.GitBranch != "" {
		s.branch = rec.GitBranch
	}
	if rec.Version != "" {
		s.version = rec.Version
	}
	if rec.SessionID != "" {
		s.sessionID = rec.SessionID
	}
	if rec.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339, rec.Timestamp); err == nil {
			ts = ts.UTC()
			if s.first.IsZero() || ts.Before(s.first) {
				s.first = ts
			}
			if s.last.IsZero() || ts.After(s.last) {
				s.last = ts
			}
		}
	}
}

// readLine reads one newline-terminated logical line into buf, growing it
// only up to maxRecordBytes. It reports whether the line exceeded that
// bound, in which case the returned prefix must not be parsed, and returns
// io.EOF together with any final unterminated line.
func readLine(br *bufio.Reader, buf []byte) ([]byte, bool, error) {
	buf = buf[:0]
	oversized := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 {
			if !oversized && len(buf)+len(chunk) <= maxRecordBytes {
				buf = append(buf, chunk...)
			} else {
				oversized = true
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return buf, oversized, err
	}
}

// stageArtifacts copies every session-linked artifact tree it can
// positively associate with the session into stagingDir, mirroring the
// root-relative source layout. Dot-prefixed entries such as lock files are
// skipped because they hold transient state, and unreadable files are
// counted instead of failing the snapshot.
func stageArtifacts(ctx context.Context, root, project, session, stagingDir string) ([]adapter.StagedFile, int, error) {
	trees := make([]string, 0, 1+len(sessionArtifactDirs))
	trees = append(trees, path.Join(projectsDirName, project, session))
	for _, dir := range sessionArtifactDirs {
		trees = append(trees, path.Join(dir, session))
	}

	var staged []adapter.StagedFile
	failures := 0
	for _, tree := range trees {
		treePath := filepath.Join(root, filepath.FromSlash(tree))
		info, err := os.Stat(treePath)
		if err != nil || !info.IsDir() {
			continue
		}
		walkErr := filepath.WalkDir(treePath, func(p string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				failures++
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") && p != treePath {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(treePath, p)
			if err != nil {
				failures++
				return nil
			}
			relPath := path.Join(tree, filepath.ToSlash(rel))
			stagedPath := filepath.Join(stagingDir, filepath.FromSlash(relPath))
			_, size, err := stageFile(ctx, p, stagedPath)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				failures++
				return nil
			}
			staged = append(staged, adapter.StagedFile{RelPath: relPath, StagedPath: stagedPath, Size: size})
			return nil
		})
		if walkErr != nil && ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
	}
	return staged, failures, nil
}

// stageFile copies srcPath to dstPath, creating parent directories, and
// returns the digest and size of the bytes actually written.
func stageFile(ctx context.Context, srcPath, dstPath string) (archive.Digest, int64, error) {
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o700); err != nil {
		return "", 0, fmt.Errorf("claude: create staging directory: %w", err)
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	buf := make([]byte, copyBufBytes)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			out.Close()
			return "", 0, err
		}
		n, rerr := in.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return "", 0, werr
			}
			size += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return "", 0, rerr
		}
	}
	if err := out.Close(); err != nil {
		return "", 0, err
	}
	return archive.NewDigest([sha256.Size]byte(h.Sum(nil))), size, nil
}

// digestFile returns the canonical digest and size of a file's bytes.
func digestFile(ctx context.Context, p string) (archive.Digest, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return archive.ComputeDigest(f)
}

// splitPrimaryPath decomposes a transcript path into the Claude Code root,
// the project directory name, and the session UUID. It rejects paths that
// do not match the <root>/projects/<project>/<session>.jsonl layout,
// because every other derivation in this adapter depends on it.
func splitPrimaryPath(primary string) (root, project, session string, err error) {
	if primary == "" {
		return "", "", "", errors.New("claude: source session has no primary path")
	}
	clean := filepath.Clean(primary)
	base := filepath.Base(clean)
	if !strings.HasSuffix(base, sessionExt) {
		return "", "", "", fmt.Errorf("claude: transcript %q is not a %s file", primary, sessionExt)
	}
	session = strings.TrimSuffix(base, sessionExt)
	projectPath := filepath.Dir(clean)
	project = filepath.Base(projectPath)
	projectsPath := filepath.Dir(projectPath)
	if session == "" || project == "" || filepath.Base(projectsPath) != projectsDirName {
		return "", "", "", fmt.Errorf("claude: transcript %q is not a <root>/%s/<project>/<session>%s path", primary, projectsDirName, sessionExt)
	}
	return filepath.Dir(projectsPath), project, session, nil
}

// sourceID builds the stable "<project-dir>/<session-uuid>" identity. Both
// segments are sanitized into archive.ValidSourceID's alphabet and
// deterministically shortened when necessary, so the identity is stable
// across runs for the same on-disk session.
func sourceID(project, session string) string {
	return sanitizeSegment(project) + "/" + sanitizeSegment(session)
}

// sanitizeSegment maps one on-disk name onto a single valid SourceID
// segment: characters outside [A-Za-z0-9._-] become "-", degenerate names
// and over-long names are replaced or suffixed with a digest of the
// original so distinct names stay distinct.
func sanitizeSegment(s string) string {
	b := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '-')
		}
	}
	out := string(b)
	if out == "" || out == "." || out == ".." {
		return "x-" + nameDigest(s)
	}
	if len(out) > maxSegmentLen {
		return out[:maxSegmentLen-len(nameDigest(s))-1] + "-" + nameDigest(s)
	}
	return out
}

// nameDigest returns a short stable discriminator for an on-disk name.
func nameDigest(s string) string {
	return archive.DigestBytes([]byte(s)).Hex()[:12]
}
