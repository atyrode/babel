// Package omp implements the OMP source adapter (SPEC.md §3). OMP is the
// reference, highest-fidelity harness: a snapshot always carries the raw
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
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/archive"
)

const (
	harnessName = "omp"
	// adapterSchema is the adapter_schema version recorded in manifests.
	adapterSchema = 1
	// adapterMetadataSchema versions the omp-specific metadata object
	// independently of the manifest envelope.
	adapterMetadataSchema = 1

	dataDirName    = ".omp"
	sessionsSubdir = "sessions"
	blobsSubdir    = "blobs"
	sessionExt     = ".jsonl"

	// artifactsSubdir separates the staged sibling artifact tree from the
	// staged primary log, so no artifact relative path can collide with
	// the primary file name.
	artifactsSubdir = "artifacts"

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
	// always satisfies archive.ValidSourceID's 512-byte limit.
	maxIDSegment = 128

	dirPerm  = 0o700
	filePerm = 0o600
)

var blobRefPattern = regexp.MustCompile(`blob:sha256:[0-9a-f]{64}`)

// Adapter is the OMP source adapter. It is stateless and safe for
// concurrent use.
type Adapter struct {
	// afterStage runs after every source file has been staged and before
	// stability is re-verified. It exists so tests can mutate the source
	// exactly inside the window Snapshot must detect; it is nil in
	// production.
	afterStage func()
}

// New returns the OMP source adapter.
func New() *Adapter { return &Adapter{} }

// Harness returns the stable lowercase harness name.
func (a *Adapter) Harness() string { return harnessName }

// Schema returns the adapter_schema version recorded in manifests.
func (a *Adapter) Schema() int { return adapterSchema }

// DefaultRoots returns the default OMP session root, "~/.omp/agent/sessions".
// It returns nil when the home directory cannot be determined; a caller
// that configures roots explicitly never depends on this.
func (a *Adapter) DefaultRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{filepath.Join(home, dataDirName, "agent", sessionsSubdir)}
}

// Discover enumerates primary session logs under roots. A session log is
// a regular "*.jsonl" file one directory below a root: the sibling
// artifact tree of a session shares its stem as a directory name, so its
// own JSONL files sit one level deeper and are never mistaken for
// sessions. Roots and project directories that do not exist or cannot be
// read are skipped silently; results are ordered by source id and
// deduplicated across overlapping roots.
func (a *Adapter) Discover(ctx context.Context, roots []string) ([]adapter.SourceSession, error) {
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
				if !archive.ValidSourceID(id) {
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

// Snapshot stages a stable copy of one session: the primary log, its
// sibling artifact tree, and the closure of referenced blobs it can
// resolve. The primary log is digested while it is copied and digested
// again afterwards, and the artifact tree is re-walked, so any change
// underneath the snapshot surfaces as a wrapped adapter.ErrUnstable
// instead of an inconsistent bundle.
func (a *Adapter) Snapshot(ctx context.Context, src adapter.SourceSession, stagingDir string) (*adapter.Snapshot, error) {
	if src.Harness != "" && src.Harness != harnessName {
		return nil, fmt.Errorf("omp: session %q belongs to harness %q", src.SourceID, src.Harness)
	}
	if src.PrimaryPath == "" {
		return nil, fmt.Errorf("omp: session %q has no primary path", src.SourceID)
	}
	if err := os.MkdirAll(stagingDir, dirPerm); err != nil {
		return nil, err
	}

	primaryInfo, err := os.Stat(src.PrimaryPath)
	if err != nil {
		return nil, err
	}

	stagedPrimary := filepath.Join(stagingDir, filepath.Base(src.PrimaryPath))
	primaryDigest, primarySize, err := stageFile(ctx, src.PrimaryPath, stagedPrimary)
	if err != nil {
		return nil, err
	}

	artifactDir := strings.TrimSuffix(src.PrimaryPath, sessionExt)
	before, err := collectArtifacts(ctx, artifactDir)
	if err != nil {
		return nil, err
	}
	stagedArtifactRoot := filepath.Join(stagingDir, artifactsSubdir)
	artifacts := make([]adapter.StagedFile, 0, len(before))
	var artifactBytes int64
	for _, entry := range before {
		staged := filepath.Join(stagedArtifactRoot, filepath.FromSlash(entry.rel))
		if err := os.MkdirAll(filepath.Dir(staged), dirPerm); err != nil {
			return nil, err
		}
		_, size, err := stageFile(ctx, filepath.Join(artifactDir, filepath.FromSlash(entry.rel)), staged)
		if err != nil {
			return nil, err
		}
		artifacts = append(artifacts, adapter.StagedFile{RelPath: entry.rel, StagedPath: staged, Size: size})
		artifactBytes += size
	}

	if a.afterStage != nil {
		a.afterStage()
	}

	if err := verifyPrimary(ctx, src.PrimaryPath, primaryDigest, primarySize, primaryInfo); err != nil {
		return nil, err
	}
	after, err := collectArtifacts(ctx, artifactDir)
	if err != nil {
		return nil, err
	}
	if err := sameArtifacts(before, after); err != nil {
		return nil, fmt.Errorf("%w: artifact tree of %s: %v", adapter.ErrUnstable, src.SourceID, err)
	}

	refs, err := scanStagedRefs(ctx, stagedPrimary, artifacts)
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

	head := readHeader(stagedPrimary)
	meta := head.commonMeta(primaryInfo.ModTime())

	rawMeta, err := archive.MarshalCanonical(&adapterMetadata{
		OMPSessionID:         head.sessionID,
		SessionRecordVersion: head.recordVersion,
		ParentSessionID:      head.parentSession,
		TitleSource:          head.titleSource,
		ProjectDir:           filepath.Base(filepath.Dir(src.PrimaryPath)),
		PrimaryDigest:        primaryDigest,
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
	canonicalMeta, err := archive.CanonicalRawMessage(rawMeta)
	if err != nil {
		return nil, err
	}

	return &adapter.Snapshot{
		Source: adapter.SourceSession{
			Harness:     harnessName,
			SourceID:    src.SourceID,
			PrimaryPath: src.PrimaryPath,
			Hint:        src.Hint,
		},
		SnapshotTime:          time.Now().UTC(),
		StagedPrimary:         stagedPrimary,
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

// adapterMetadata is the versioned omp-specific manifest extension. Field
// order is the canonical encoding order (archive.MarshalCanonical).
type adapterMetadata struct {
	OMPSessionID         string         `json:"omp_session_id,omitempty"`
	SessionRecordVersion int            `json:"session_record_version,omitempty"`
	ParentSessionID      string         `json:"parent_session_id,omitempty"`
	TitleSource          string         `json:"title_source,omitempty"`
	ProjectDir           string         `json:"project_dir,omitempty"`
	PrimaryDigest        archive.Digest `json:"primary_digest"`
	ArtifactCount        int            `json:"artifact_count"`
	ArtifactBytes        int64          `json:"artifact_bytes"`
	BlobRefCount         int            `json:"blob_ref_count"`
	ResolvedBlobCount    int            `json:"resolved_blob_count"`
	UnresolvedBlobCount  int            `json:"unresolved_blob_count"`
	BlobStoreFound       bool           `json:"blob_store_found"`
}

// artifactEntry is one observed artifact file: the slash-separated path
// relative to the artifact tree plus the stability signals compared
// before and after staging.
type artifactEntry struct {
	rel     string
	size    int64
	modTime time.Time
}

// collectArtifacts lists the regular files of a session's sibling
// artifact tree in ascending relative-path order. A missing tree is not
// an error: many sessions have none. Irregular entries (symlinks,
// devices, sockets) are never staged, so staging cannot follow a link out
// of the tree.
func collectArtifacts(ctx context.Context, dir string) ([]artifactEntry, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	var out []artifactEntry
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
		out = append(out, artifactEntry{rel: filepath.ToSlash(rel), size: fi.Size(), modTime: fi.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	return out, nil
}

// sameArtifacts reports why two observations of an artifact tree differ.
func sameArtifacts(before, after []artifactEntry) error {
	if len(before) != len(after) {
		return fmt.Errorf("file count changed from %d to %d", len(before), len(after))
	}
	for i := range before {
		switch {
		case before[i].rel != after[i].rel:
			return fmt.Errorf("file %s replaced by %s", before[i].rel, after[i].rel)
		case before[i].size != after[i].size:
			return fmt.Errorf("file %s changed size", before[i].rel)
		case !before[i].modTime.Equal(after[i].modTime):
			return fmt.Errorf("file %s changed modification time", before[i].rel)
		}
	}
	return nil
}

// stageFile copies srcPath to dstPath and returns the digest and size of
// the bytes it copied, which are by construction the staged bytes.
func stageFile(ctx context.Context, srcPath, dstPath string) (archive.Digest, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, sum), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	var raw [sha256.Size]byte
	copy(raw[:], sum.Sum(nil))
	return archive.NewDigest(raw), size, nil
}

// verifyPrimary re-reads the source log and reports a wrapped
// adapter.ErrUnstable when its bytes, size, or modification time no
// longer match what was staged.
func verifyPrimary(ctx context.Context, path string, staged archive.Digest, size int64, before os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	digest, n, err := archive.ComputeDigest(f)
	if err != nil {
		return err
	}
	after, err := f.Stat()
	if err != nil {
		return err
	}
	if digest != staged || n != size {
		return fmt.Errorf("%w: primary log of %s changed content while staging", adapter.ErrUnstable, filepath.Base(path))
	}
	if !after.ModTime().Equal(before.ModTime()) {
		return fmt.Errorf("%w: primary log of %s was rewritten while staging", adapter.ErrUnstable, filepath.Base(path))
	}
	return nil
}

// scanStagedRefs returns the deduplicated, ascending blob references of
// the staged primary log and every staged artifact. Artifacts are scanned
// because subagent logs carry their own blob references, and the closure
// a continuation needs spans the whole session tree.
func scanStagedRefs(ctx context.Context, stagedPrimary string, artifacts []adapter.StagedFile) ([]string, error) {
	refs := make(map[string]struct{})
	if err := scanBlobRefs(ctx, stagedPrimary, refs); err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		if err := scanBlobRefs(ctx, artifact.StagedPath, refs); err != nil {
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
// scale with a multi-megabyte log.
func scanBlobRefs(ctx context.Context, path string, refs map[string]struct{}) error {
	f, err := os.Open(path)
	if err != nil {
		return err
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
	want := archive.Digest(strings.TrimPrefix(ref, "blob:"))
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
	got, size, err := archive.ComputeDigest(f)
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

// readHeader extracts metadata from the head of a staged log. Every
// absent value stays absent: a log whose head is truncated, malformed, or
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
	for i := 0; i < headerScanRecords; i++ {
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
		meta.Completeness = append(meta.Completeness, archive.CompletenessReason{Field: field, Reason: reason})
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
	for i := 0; i < len(s); i++ {
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
