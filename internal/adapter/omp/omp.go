// Package omp implements the OMP source adapter (SPEC.md §3). OMP is the
// reference, highest-fidelity harness: a description always names the raw
// session JSONL, the complete sibling artifact tree, and every resolvable
// referenced blob from the content-addressed blob store, so unresolved
// references are the only reason continuation grade is withheld.
//
// The on-disk layout this adapter reads is:
//
//	<data root>/agent/sessions/<project>/<stem>.jsonl   primary raw log
//	<data root>/agent/sessions/<project>/<stem>/...      sibling artifacts
//	<data root>/agent/blobs/<64 hex>[.<ext>]             blob store
//
// A session log is a stream of JSON records, one per line. The first
// record is a fixed-width padded {"type":"title"} record rewritten in
// place as the title changes, followed by a {"type":"session"} record
// carrying the session id, creation timestamp, and workspace cwd.
// Persisted blob references appear as "blob:sha256:<64 hex>" inside
// record payloads, both directly and escaped inside nested JSON strings,
// so references are found by scanning raw bytes rather than by walking a
// record schema that would miss the nested form.
//
// Every file is read in place and never copied: durability belongs to
// restic, whose snapshots are crash-consistent per file rather than
// transactional across files. A description is therefore a best-effort
// view of the live tree at one instant, refreshed on every call, and a
// concurrent OMP write degrades the view instead of failing it.
package omp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/digest"
)

const (
	harnessName = "omp"
	// adapterSchema is the adapter_schema version of this adapter's
	// discovery and description behavior.
	adapterSchema = 1
	// adapterMetadataSchema versions the omp-specific metadata object
	// independently of the common description shape.
	adapterMetadataSchema = 1

	dataDirName    = ".omp"
	sessionsSubdir = "sessions"
	blobsSubdir    = "blobs"
	sessionExt     = ".jsonl"

	// blobRefPrefix is the persisted blob-reference scheme; the remainder
	// of a reference is the blob's lowercase SHA-256 hex, which makes
	// every reference independently verifiable.
	blobRefPrefix = "blob:sha256:"
	blobRefLen    = len(blobRefPrefix) + 2*sha256.Size

	// headerScanLimit and headerScanRecords bound best-effort metadata
	// extraction: the title and session records are the first records of
	// a well-formed log, and a malformed head never costs a full read.
	headerScanLimit   = 1 << 20
	headerScanRecords = 8

	// maxIDSegment bounds one source-id segment so a composed source id
	// always satisfies adapter.ValidSourceID's 512-byte limit.
	maxIDSegment = 128
)

var blobRefPattern = regexp.MustCompile(`blob:sha256:[0-9a-f]{64}`)

// Adapter is the OMP source adapter. It is stateless and safe for
// concurrent use.
type Adapter struct{}

// New returns the OMP source adapter.
func New() *Adapter { return &Adapter{} }

var (
	_ adapter.Adapter            = (*Adapter)(nil)
	_ adapter.SnapshotIdentifier = (*Adapter)(nil)
)

// Harness returns the stable lowercase harness name.
func (*Adapter) Harness() string { return harnessName }

// Schema returns the adapter_schema version of this adapter.
func (*Adapter) Schema() int { return adapterSchema }

// DefaultRoots returns the default OMP session root, "~/.omp/agent/sessions".
// It returns nil when the home directory cannot be determined; a caller
// that configures roots explicitly never depends on this.
func (*Adapter) DefaultRoots() []string {
	agent, ok := agentDir()
	if !ok {
		return nil
	}
	return []string{filepath.Join(agent, sessionsSubdir)}
}

// BackupRoots adds the content-addressed blob store to the session root:
// referenced blobs live outside the session trees, so a backup that
// captured only DefaultRoots could never restore a continuation-grade
// closure (SPEC.md §3).
func (*Adapter) BackupRoots() []string {
	agent, ok := agentDir()
	if !ok {
		return nil
	}
	return []string{
		filepath.Join(agent, sessionsSubdir),
		filepath.Join(agent, blobsSubdir),
	}
}

// agentDir locates "~/.omp/agent", the parent of both default roots.
func agentDir() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", false
	}
	return filepath.Join(home, dataDirName, "agent"), true
}

// Discover enumerates primary session logs under roots. A session log is
// a regular "*.jsonl" file one directory below a root: the sibling
// artifact tree of a session shares its stem as a directory name, so its
// own JSONL files sit one level deeper and are never mistaken for
// sessions. Roots and project directories that do not exist or cannot be
// read are skipped silently; results are ordered by source id and
// deduplicated across overlapping roots.
func (*Adapter) Discover(ctx context.Context, roots []string) ([]adapter.SourceSession, error) {
	var found []adapter.SourceSession
	seen := make(map[string]struct{})
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		projects, err := os.ReadDir(root)
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
			projectDir := filepath.Join(root, project.Name())
			entries, err := os.ReadDir(projectDir)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), sessionExt) {
					continue
				}
				stem := strings.TrimSuffix(entry.Name(), sessionExt)
				id := idSegment(project.Name()) + "/" + idSegment(stem)
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
					PrimaryPath: filepath.Join(projectDir, entry.Name()),
					Hint:        project.Name() + "/" + entry.Name(),
				})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].SourceID < found[j].SourceID })
	return found, nil
}

// IdentifyArchived recognizes OMP sessions in a snapshot's file listing,
// the cross-host twin of Discover: the files belong to another machine, so
// nothing may be read and the layout alone has to carry the identity.
//
// It applies exactly Discover's rule to paths instead of directory
// entries. A session log is a "*.jsonl" file two levels below a path
// segment named "sessions", so the sibling artifact tree - a directory
// sharing the session's stem - keeps its own JSONL files one level deeper,
// where this rule never sees them. Source ids are composed by the same
// idSegment pair and validated the same way, which is what lets a session
// archived here be recognized as the same session there. Entries that do
// not fit are ignored, because one snapshot holds several harnesses' trees.
func (*Adapter) IdentifyArchived(files []adapter.ArchivedFile) ([]adapter.ArchivedSession, error) {
	// Entries are examined in ascending path order so identification does
	// not depend on the caller's listing order: two distinct paths can
	// sanitize to one source id, and the survivor must be reproducible.
	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return files[order[a]].Path < files[order[b]].Path })

	sessions := make(map[string]*adapter.ArchivedSession)
	// artifactOwner maps a session's sibling artifact tree to its source
	// id, so the closure is assembled in one further pass rather than by
	// rescanning the listing per session.
	artifactOwner := make(map[string]string)
	for _, i := range order {
		f := files[i]
		id, artifactDir, ok := archivedSession(f.Path)
		if !ok {
			continue
		}
		if _, dup := sessions[id]; dup {
			continue
		}
		sessions[id] = &adapter.ArchivedSession{
			SourceID:    id,
			PrimaryPath: f.Path,
			PrimarySize: f.Size,
			Files:       []string{f.Path},
		}
		artifactOwner[artifactDir] = id
	}

	for _, i := range order {
		p := files[i].Path
		if !walkablePath(p) {
			continue
		}
		// The nearest enclosing artifact tree owns the file. A primary log
		// is never inside one, because a log two levels below "sessions" is
		// a sibling of the artifact trees rather than a member of one.
		for dir := p; ; {
			slash := strings.LastIndexByte(dir, '/')
			if slash <= 0 {
				break
			}
			dir = dir[:slash]
			if id, ok := artifactOwner[dir]; ok {
				s := sessions[id]
				s.Files = append(s.Files, p)
				break
			}
		}
	}

	// Blobs are deliberately absent from Files. The blob store is a
	// sibling of the sessions root (blobsDir), so the listing does name the
	// store's files, but which blobs a session references is recorded only
	// inside the primary log, and identification does not read bytes. An
	// attribution guessed from the layout would be a fabrication, and
	// attaching the whole store to every session would be worse, so blobs
	// stay out until a fetch restores the log and Describe resolves them.
	out := make([]adapter.ArchivedSession, 0, len(sessions))
	for _, s := range sessions {
		// The closure is normalized here rather than inferred from the
		// order the listing happened to arrive in.
		slices.Sort(s.Files)
		s.Files = slices.Compact(s.Files)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out, nil
}

// archivedSession matches one snapshot path against the OMP session
// layout, returning the source id Discover would assign the same file and
// the sibling artifact tree that carries the rest of its closure.
func archivedSession(p string) (id, artifactDir string, ok bool) {
	// The extension is tested first because it rejects most of a
	// snapshot's entries before their paths are split.
	if !strings.HasSuffix(p, sessionExt) || !walkablePath(p) {
		return "", "", false
	}
	// Snapshot paths are "/"-separated whatever host reads them, so they
	// are split explicitly rather than with filepath, whose separator is
	// the local machine's.
	segs := strings.Split(p, "/")
	n := len(segs)
	if n < 3 || segs[n-3] != sessionsSubdir {
		return "", "", false
	}
	stem := strings.TrimSuffix(segs[n-1], sessionExt)
	id = idSegment(segs[n-2]) + "/" + idSegment(stem)
	if !adapter.ValidSourceID(id) {
		return "", "", false
	}
	// The artifact tree is the primary log's path without the extension.
	return id, p[:len(p)-len(sessionExt)], true
}

// walkablePath reports whether a snapshot path is one a local walk could
// have produced. os.ReadDir never yields an empty, "." or ".." component,
// so a path carrying one names a tree Discover could not have reached and
// its components are not a trustworthy identity - idSegment would happily
// sanitize ".." into the source-id alphabet.
func walkablePath(p string) bool {
	rest := strings.TrimPrefix(p, "/") // an absolute path's leading "/"
	for {
		seg, more := rest, false
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			seg, rest, more = rest[:i], rest[i+1:], true
		}
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
		if !more {
			return true
		}
	}
}

// Describe reads one session in place: the primary log's digest and size,
// its sibling artifact tree, and the closure of referenced blobs it can
// resolve. Nothing is copied and the source is never re-verified, because
// a description is a best-effort view rather than a transaction: an OMP
// process appending to the log while it is read yields a slightly older
// or slightly newer view, and the next call supersedes it.
func (*Adapter) Describe(ctx context.Context, src adapter.SourceSession) (*adapter.Description, error) {
	if src.Harness != "" && src.Harness != harnessName {
		return nil, fmt.Errorf("omp: session %q belongs to harness %q", src.SourceID, src.Harness)
	}
	if src.PrimaryPath == "" {
		return nil, fmt.Errorf("omp: session %q has no primary path", src.SourceID)
	}

	primaryInfo, err := os.Stat(src.PrimaryPath)
	if err != nil {
		return nil, err
	}
	primaryDigest, primarySize, err := digestFile(ctx, src.PrimaryPath)
	if err != nil {
		return nil, err
	}

	artifactDir := strings.TrimSuffix(src.PrimaryPath, sessionExt)
	artifacts, err := collectArtifacts(ctx, artifactDir)
	if err != nil {
		return nil, err
	}
	var artifactBytes int64
	for _, artifact := range artifacts {
		artifactBytes += artifact.Size
	}

	refs, err := scanRefs(ctx, src.PrimaryPath, artifacts)
	if err != nil {
		return nil, err
	}
	blobStore := blobsDir(src.PrimaryPath)
	blobStoreFound := isDir(blobStore)
	var blobs []adapter.BlobRef
	var unresolved []string
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		blob, ok := resolveBlob(blobStore, ref)
		if !ok {
			unresolved = append(unresolved, ref)
			continue
		}
		blobs = append(blobs, blob)
	}

	head := readHeader(src.PrimaryPath)
	meta := head.commonMeta(primaryInfo.ModTime())

	rawMeta, err := adapter.MarshalCanonical(&adapterMetadata{
		OMPSessionID:         head.sessionID,
		SessionRecordVersion: head.recordVersion,
		ParentSessionID:      head.parentSession,
		TitleSource:          head.titleSource,
		ProjectDir:           filepath.Base(filepath.Dir(src.PrimaryPath)),
		PrimaryDigest:        primaryDigest,
		PrimarySize:          primarySize,
		ArtifactCount:        len(artifacts),
		ArtifactBytes:        artifactBytes,
		BlobRefCount:         len(refs),
		ResolvedBlobCount:    len(blobs),
		UnresolvedBlobCount:  len(unresolved),
		BlobStoreFound:       blobStoreFound,
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
		Artifacts:             artifacts,
		Blobs:                 blobs,
		UnresolvedBlobRefs:    unresolved,
		ContinuationGrade:     len(unresolved) == 0,
	}, nil
}

// adapterMetadata is the versioned omp-specific metadata document. Field
// order is the canonical encoding order (adapter.MarshalCanonical).
type adapterMetadata struct {
	OMPSessionID         string        `json:"omp_session_id,omitempty"`
	SessionRecordVersion int           `json:"session_record_version,omitempty"`
	ParentSessionID      string        `json:"parent_session_id,omitempty"`
	TitleSource          string        `json:"title_source,omitempty"`
	ProjectDir           string        `json:"project_dir,omitempty"`
	PrimaryDigest        digest.Digest `json:"primary_digest"`
	PrimarySize          int64         `json:"primary_size"`
	ArtifactCount        int           `json:"artifact_count"`
	ArtifactBytes        int64         `json:"artifact_bytes"`
	BlobRefCount         int           `json:"blob_ref_count"`
	ResolvedBlobCount    int           `json:"resolved_blob_count"`
	UnresolvedBlobCount  int           `json:"unresolved_blob_count"`
	BlobStoreFound       bool          `json:"blob_store_found"`
}

// collectArtifacts lists the regular files of a session's sibling
// artifact tree in ascending relative-path order, with paths relative to
// the artifact tree itself. A missing tree is not an error: many sessions
// have none. Irregular entries (symlinks, devices, sockets) are excluded,
// so the closure can never follow a link out of the tree.
func collectArtifacts(ctx context.Context, dir string) ([]adapter.SourceFile, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	var out []adapter.SourceFile
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree degrades this session's artifact
			// closure instead of aborting the walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		out = append(out, adapter.SourceFile{
			RelPath:    filepath.ToSlash(rel),
			SourcePath: path,
			Size:       fi.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RelPath < out[j].RelPath })
	return out, nil
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

// scanRefs returns the deduplicated, ascending blob references of the
// primary log and every artifact. Artifacts are scanned because subagent
// logs carry their own blob references, and the closure a continuation
// needs spans the whole session tree.
func scanRefs(ctx context.Context, primaryPath string, artifacts []adapter.SourceFile) ([]string, error) {
	refs := make(map[string]struct{})
	if err := scanBlobRefs(ctx, primaryPath, refs); err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		if err := scanBlobRefs(ctx, artifact.SourcePath, refs); err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(refs))
	for ref := range refs {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out, nil
}

// scanBlobRefs adds every blob reference in path to refs. It reads in
// bounded chunks that overlap by one reference length, so a reference
// split across chunk boundaries is still found and memory use does not
// scale with a multi-megabyte log. A file that disappears or becomes
// unreadable between the walk and the scan contributes no references
// instead of failing the description.
func scanBlobRefs(ctx context.Context, path string, refs map[string]struct{}) error {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	const chunk = 1 << 20
	overlap := blobRefLen - 1
	buf := make([]byte, chunk+overlap)
	filled := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := io.ReadFull(f, buf[filled:])
		filled += n
		for _, match := range blobRefPattern.FindAll(buf[:filled], -1) {
			refs[string(match)] = struct{}{}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		copy(buf, buf[filled-overlap:filled])
		filled = overlap
	}
}

// blobsDir returns the blob store serving a session, derived from the
// documented layout: the sessions root is the grandparent of a primary
// log, and "blobs" is its sibling.
func blobsDir(primaryPath string) string {
	sessionsRoot := filepath.Dir(filepath.Dir(primaryPath))
	return filepath.Join(filepath.Dir(sessionsRoot), blobsSubdir)
}

// resolveBlob resolves one reference against the blob store and verifies
// the stored bytes against the digest the reference encodes. A missing
// file, an unreadable file, or a digest mismatch leaves the reference
// unresolved rather than admitting unverified bytes into the closure.
func resolveBlob(store, ref string) (adapter.BlobRef, bool) {
	want := digest.Digest(strings.TrimPrefix(ref, "blob:"))
	if !want.Valid() {
		return adapter.BlobRef{}, false
	}
	path := filepath.Join(store, want.Hex())
	if !isRegular(path) {
		// The store also keeps extension-suffixed copies of the same
		// bytes; either name is acceptable because the digest decides.
		matches, err := filepath.Glob(path + ".*")
		if err != nil {
			return adapter.BlobRef{}, false
		}
		path = ""
		for _, candidate := range matches {
			if isRegular(candidate) {
				path = candidate
				break
			}
		}
		if path == "" {
			return adapter.BlobRef{}, false
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return adapter.BlobRef{}, false
	}
	defer f.Close()
	got, size, err := digest.Compute(f)
	if err != nil || got != want {
		return adapter.BlobRef{}, false
	}
	return adapter.BlobRef{Digest: got, SourcePath: path, Size: size}, true
}

func isRegular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// sessionRecord decodes the head records of a session log. One shape
// covers both the title record and the session record; Type selects which
// fields are meaningful.
type sessionRecord struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Timestamp     string `json:"timestamp"`
	CWD           string `json:"cwd"`
	TitleSource   string `json:"titleSource"`
	ParentSession string `json:"parentSession"`
}

// header is the best-effort metadata read from a log's head records.
type header struct {
	title         string
	sessionID     string
	recordVersion int
	parentSession string
	titleSource   string
	cwd           string
	createdAt     time.Time
	hasCreatedAt  bool
}

// readHeader extracts metadata from the head of a live log. Every absent
// value stays absent: a log whose head is truncated, malformed, or
// missing records yields a header whose empty fields become explicit
// completeness reasons.
func readHeader(path string) header {
	var h header
	f, err := os.Open(path)
	if err != nil {
		return h
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, headerScanLimit))
	for range headerScanRecords {
		var rec sessionRecord
		if err := dec.Decode(&rec); err != nil {
			break
		}
		switch rec.Type {
		case "title":
			// The padded title record is rewritten in place as the title
			// changes, so it supersedes the session record's title.
			if h.title == "" {
				h.title = rec.Title
			}
		case "session":
			h.sessionID = rec.ID
			h.recordVersion = rec.Version
			h.parentSession = rec.ParentSession
			h.titleSource = rec.TitleSource
			h.cwd = rec.CWD
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

// commonMeta converts the header plus the source log's modification time
// into the portable catalog fields. Absent values are nil and each nil
// carries a completeness reason; nothing is synthesized to fill a shape.
func (h header) commonMeta(sourceModTime time.Time) adapter.CommonMeta {
	var meta adapter.CommonMeta
	missing := func(field, reason string) {
		meta.Completeness = append(meta.Completeness, adapter.CompletenessReason{Field: field, Reason: reason})
	}

	if h.title != "" {
		title := h.title
		meta.Title = &title
	} else {
		missing("title", "session log carries no non-empty title record")
	}
	if h.cwd != "" {
		workspace := h.cwd
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

	missing("lifecycle", "omp does not persist a session lifecycle state")
	missing("repo", "repository fingerprint would require reading the recorded workspace, which the adapter never opens")
	return meta
}

// idSegment renders one path component as a source-id segment. Components
// that already satisfy the source-id alphabet and length are used
// verbatim, which keeps ids readable and stable. Otherwise invalid bytes
// are replaced and a digest of the original component is appended, so
// distinct components keep distinct, reproducible ids.
func idSegment(s string) string {
	if len(s) <= maxIDSegment && validSegment(s) {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	suffix := "-" + hex.EncodeToString(sum[:4])
	keep := maxIDSegment - len(suffix)
	b := make([]byte, 0, maxIDSegment)
	for i := 0; i < len(s) && len(b) < keep; i++ {
		if segmentByteOK(s[i]) {
			b = append(b, s[i])
		} else {
			b = append(b, '-')
		}
	}
	return string(b) + suffix
}

func validSegment(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for i := range len(s) {
		if !segmentByteOK(s[i]) {
			return false
		}
	}
	return true
}

func segmentByteOK(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.' || c == '_' || c == '-':
		return true
	}
	return false
}
