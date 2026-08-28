// Package codex implements the Codex source adapter (SPEC.md §3).
//
// Codex stores one JSONL rollout log per session under
// "<root>/sessions/<yyyy>/<mm>/<dd>/", plus two host-level state files —
// "history.jsonl" and "session_index.jsonl" — and an "attachments/<id>/"
// tree referenced from message text. The v1 guarantee for this harness is
// the raw logs, `history.jsonl`, `session_index.jsonl`, and discovered
// referenced attachments; title, workspace, lifecycle, and attachment
// closure are explicitly allowed to be unavailable. This adapter therefore
// extracts only what the records actually expose and records a
// completeness reason for every field it cannot observe.
//
// Every file is read in place and never copied: durability belongs to
// restic, whose snapshots are crash-consistent per file rather than
// transactional across files. A live Codex process appending to a log
// while it is read therefore degrades the description — a torn or
// malformed trailing record is counted and skipped — and the next
// description supersedes it. Raw preservation always wins: a malformed or
// unparsable log still names its bytes for archival.
package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/digest"
)

const (
	// HarnessName is the stable lowercase harness name (SPEC.md §3).
	HarnessName = "codex"
	// AdapterSchema is the adapter_schema version of this adapter's
	// discovery and description behavior.
	AdapterSchema = 1
	// MetadataSchema versions the codex adapter_metadata document
	// independently of the common description shape.
	MetadataSchema = 1

	// StateSourceID is the source identity of the single host-state
	// session: `history.jsonl` as its primary raw log with
	// `session_index.jsonl` as its artifact. Codex keeps both at the root
	// rather than per session, so they are described as one dedicated
	// session instead of being duplicated into every rollout.
	StateSourceID = "state"

	// KindSession and KindState are the adapter_metadata "kind" values.
	KindSession = "session"
	KindState   = "state"
)

const (
	sessionsDir      = "sessions"
	attachmentsDir   = "attachments"
	historyFile      = "history.jsonl"
	sessionIndexFile = "session_index.jsonl"
	rolloutExt       = ".jsonl"

	// maxRecordBytes bounds how much of one JSONL record is held in memory.
	// Logs are untrusted data (SPEC.md §3) and a single pathological line
	// must not exhaust the process; the excess is discarded for metadata
	// purposes only, never from the file restic archives.
	maxRecordBytes = 4 << 20

	// maxAttachmentRefs bounds distinct attachment directories honoured
	// from one log, for the same reason: the references are scraped from
	// free-form message text.
	maxAttachmentRefs = 1024
)

// attachmentRefPattern matches the recoverable part of an attachment
// reference. Codex embeds attachments as absolute paths inside message
// text ("<root>/attachments/<id>/<file>"), so only the "<id>" directory is
// reliably extractable; the directory's actual contents then supply the
// file names, including names this pattern would not match.
var attachmentRefPattern = regexp.MustCompile(`attachments/([A-Za-z0-9._-]{1,128})/`)

var attachmentMarker = []byte(attachmentsDir + "/")

// Adapter is the Codex source adapter. It holds no state and is safe for
// concurrent use.
type Adapter struct{}

// New returns the Codex source adapter.
func New() *Adapter { return &Adapter{} }

var _ adapter.Adapter = (*Adapter)(nil)

// Harness returns the stable harness name.
func (*Adapter) Harness() string { return HarnessName }

// Schema returns the adapter_schema version of this adapter.
func (*Adapter) Schema() int { return AdapterSchema }

// DefaultRoots returns the default local Codex root: $CODEX_HOME when the
// operator relocated it, otherwise "~/.codex". It returns nil when neither
// is resolvable, which Discover treats as nothing to scan.
func (*Adapter) DefaultRoots() []string {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return []string{filepath.Clean(home)}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	return []string{filepath.Join(home, ".codex")}
}

// BackupRoots matches DefaultRoots: every Codex file worth capturing
// lives under the single Codex home root.
func (a *Adapter) BackupRoots() []string { return a.DefaultRoots() }

// Discover enumerates one session per rollout log under "<root>/sessions"
// plus, when "<root>/history.jsonl" exists, the host-state session. Roots
// that do not exist are skipped silently and unreadable subtrees degrade
// to their readable part rather than aborting the scan. Results are sorted
// by SourceID and deduplicated, so two scans of an unchanged tree return
// identical identities.
func (*Adapter) Discover(ctx context.Context, roots []string) ([]adapter.SourceSession, error) {
	var out []adapter.SourceSession
	seen := make(map[string]struct{})

	add := func(s adapter.SourceSession) {
		if _, dup := seen[s.SourceID]; dup {
			return
		}
		seen[s.SourceID] = struct{}{}
		out = append(out, s)
	}

	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root = filepath.Clean(root)
		if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
			continue
		}
		if fi, err := os.Stat(filepath.Join(root, historyFile)); err == nil && fi.Mode().IsRegular() {
			add(adapter.SourceSession{
				Harness:     HarnessName,
				SourceID:    StateSourceID,
				PrimaryPath: filepath.Join(root, historyFile),
				Hint:        "codex host state",
			})
		}
		found, err := discoverRollouts(ctx, root)
		if err != nil {
			return nil, err
		}
		for _, s := range found {
			add(s)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].SourceID < out[j].SourceID })
	return out, nil
}

func discoverRollouts(ctx context.Context, root string) ([]adapter.SourceSession, error) {
	base := filepath.Join(root, sessionsDir)
	if fi, err := os.Stat(base); err != nil || !fi.IsDir() {
		return nil, nil
	}
	var out []adapter.SourceSession
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// An unreadable directory degrades to nothing rather than
			// failing the whole scan.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() || !strings.HasSuffix(d.Name(), rolloutExt) {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil
		}
		out = append(out, adapter.SourceSession{
			Harness:     HarnessName,
			SourceID:    sourceIDFor(rel),
			PrimaryPath: p,
			Hint:        "codex session rollout",
		})
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return nil, err
	}
	return out, nil
}

// sourceIDFor derives a stable adapter.ValidSourceID from a root-relative
// rollout path. Path separators become "/" segments, which is already a
// valid identity for every name Codex generates. A path outside the
// identity alphabet or length limit degrades to a collision-resistant
// digest form, so an unusual on-disk name still archives its raw
// transcript (SPEC.md §3) instead of being dropped or aliased onto
// another session.
func sourceIDFor(rel string) string {
	slash := filepath.ToSlash(rel)
	if adapter.ValidSourceID(slash) && slash != StateSourceID {
		return slash
	}
	return "path-" + digest.Bytes([]byte(slash)).Hex()
}

// Describe reads one session's live files in place: the primary log's
// records for metadata, and the attachment directories (or, for the
// host-state session, `session_index.jsonl`) as its artifact closure.
// Nothing is copied and the source is never re-verified, because a
// description is a best-effort view rather than a transaction: a
// concurrent Codex write yields a slightly older or newer view whose
// torn trailing record is counted and skipped, and the next description
// supersedes it.
//
// ContinuationGrade is always false for this adapter: attachment closure
// is not guaranteed by the format. References are scraped from message
// text, non-textual references are invisible, and Codex records no
// artifact manifest, so the adapter can never prove it observed the
// complete closure a continuation would need (SPEC.md §3).
func (*Adapter) Describe(ctx context.Context, src adapter.SourceSession) (*adapter.Description, error) {
	if src.Harness != "" && src.Harness != HarnessName {
		return nil, fmt.Errorf("codex: source belongs to harness %q", src.Harness)
	}
	if !adapter.ValidSourceID(src.SourceID) {
		return nil, fmt.Errorf("codex: invalid source id %q", src.SourceID)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, primaryRel, err := locate(src)
	if err != nil {
		return nil, err
	}
	primaryInfo, err := os.Stat(src.PrimaryPath)
	if err != nil {
		return nil, fmt.Errorf("codex: stat %s: %w", src.SourceID, err)
	}
	if !primaryInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("codex: primary log %s is not a regular file", src.PrimaryPath)
	}

	kind := KindSession
	if src.SourceID == StateSourceID {
		kind = KindState
	}
	var scan *scanResult
	if kind == KindState {
		scan, err = scanHistory(ctx, src.PrimaryPath)
	} else {
		scan, err = scanRollout(ctx, src.PrimaryPath)
	}
	if err != nil {
		return nil, fmt.Errorf("codex: read %s: %w", src.SourceID, err)
	}

	var (
		artifacts       []adapter.SourceFile
		unresolved      []string
		attachmentFiles int
		indexFound      *bool
	)
	if kind == KindState {
		found := false
		indexPath := filepath.Join(root, sessionIndexFile)
		if fi, statErr := os.Stat(indexPath); statErr == nil && fi.Mode().IsRegular() {
			artifacts = append(artifacts, adapter.SourceFile{
				RelPath:    sessionIndexFile,
				SourcePath: indexPath,
				Size:       fi.Size(),
			})
			found = true
		}
		indexFound = &found
	} else {
		artifacts, unresolved, err = resolveAttachments(ctx, root, scan.attachments)
		if err != nil {
			return nil, err
		}
		attachmentFiles = len(artifacts)
	}

	md := &Metadata{
		Kind:                 kind,
		PrimaryPath:          primaryRel,
		SessionID:            scan.sessionID,
		ThreadID:             scan.threadID,
		ParentThreadID:       scan.parentThreadID,
		Originator:           scan.originator,
		CLIVersion:           scan.cliVersion,
		ModelProvider:        scan.modelProvider,
		Models:               scan.models,
		ThreadSource:         scan.threadSource,
		HistoryMode:          scan.historyMode,
		MultiAgentVersion:    scan.multiAgentVersion,
		WorkspaceRoots:       scan.workspaceRoots,
		Records:              scan.records,
		RecordTypes:          scan.recordTypes,
		MalformedRecords:     scan.malformed,
		TruncatedRecords:     scan.truncated,
		AttachmentRefs:       len(scan.attachments),
		AttachmentRefsCapped: scan.attachmentsCapped,
		AttachmentFiles:      attachmentFiles,
		SessionIndexFound:    indexFound,
	}
	rawMeta, err := adapter.MarshalCanonical(md)
	if err != nil {
		return nil, fmt.Errorf("codex: encode adapter metadata for %s: %w", src.SourceID, err)
	}
	canonical, err := adapter.CanonicalRawMessage(rawMeta)
	if err != nil {
		return nil, fmt.Errorf("codex: canonicalize adapter metadata for %s: %w", src.SourceID, err)
	}

	return &adapter.Description{
		Source:                src,
		DescribedAt:           time.Now().UTC(),
		PrimarySize:           primaryInfo.Size(),
		Meta:                  commonMeta(kind, scan),
		AdapterMetadataSchema: MetadataSchema,
		AdapterMetadata:       canonical,
		Artifacts:             artifacts,
		UnresolvedBlobRefs:    unresolved,
		ContinuationGrade:     false,
	}, nil
}

// Metadata is the codex adapter_metadata document. It records only
// structural facts observed in the on-disk records — identifiers,
// versions, roots, and counts — and never transcript text.
type Metadata struct {
	Kind        string `json:"kind"`
	PrimaryPath string `json:"primary_path"`

	SessionID         string   `json:"session_id,omitempty"`
	ThreadID          string   `json:"thread_id,omitempty"`
	ParentThreadID    string   `json:"parent_thread_id,omitempty"`
	Originator        string   `json:"originator,omitempty"`
	CLIVersion        string   `json:"cli_version,omitempty"`
	ModelProvider     string   `json:"model_provider,omitempty"`
	Models            []string `json:"models,omitempty"`
	ThreadSource      string   `json:"thread_source,omitempty"`
	HistoryMode       string   `json:"history_mode,omitempty"`
	MultiAgentVersion string   `json:"multi_agent_version,omitempty"`
	WorkspaceRoots    []string `json:"workspace_roots,omitempty"`

	Records          int            `json:"records"`
	RecordTypes      map[string]int `json:"record_types,omitempty"`
	MalformedRecords int            `json:"malformed_records,omitempty"`
	TruncatedRecords int            `json:"truncated_records,omitempty"`

	AttachmentRefs       int  `json:"attachment_refs,omitempty"`
	AttachmentRefsCapped bool `json:"attachment_refs_capped,omitempty"`
	// AttachmentFiles counts the regular files found inside the resolved
	// attachment directories.
	AttachmentFiles int `json:"attachment_files,omitempty"`

	// SessionIndexFound is set only for the host-state session and
	// reports whether `session_index.jsonl` was present alongside
	// `history.jsonl`.
	SessionIndexFound *bool `json:"session_index_found,omitempty"`
}

// locate recovers the Codex root and the root-relative primary path of a
// discovered session. Host state is "<root>/history.jsonl"; rollout logs
// live under "<root>/sessions/", so the root is the nearest ancestor that
// actually contains a "sessions" directory.
func locate(src adapter.SourceSession) (root, primaryRel string, err error) {
	if src.PrimaryPath == "" {
		return "", "", errors.New("codex: source has no primary path")
	}
	if src.SourceID == StateSourceID {
		return filepath.Dir(src.PrimaryPath), historyFile, nil
	}
	root = codexRoot(filepath.Dir(src.PrimaryPath))
	if root == "" {
		return "", "", fmt.Errorf("codex: no root with a %q directory above %s", sessionsDir, src.PrimaryPath)
	}
	rel, err := filepath.Rel(root, src.PrimaryPath)
	if err != nil {
		return "", "", fmt.Errorf("codex: relativize %s: %w", src.PrimaryPath, err)
	}
	return root, filepath.ToSlash(rel), nil
}

func codexRoot(dir string) string {
	for {
		if fi, err := os.Stat(filepath.Join(dir, sessionsDir)); err == nil && fi.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// commonMeta fills the nullable portable catalog fields from what the
// records exposed, recording a reason for every field left nil.
func commonMeta(kind string, scan *scanResult) adapter.CommonMeta {
	var m adapter.CommonMeta
	reason := func(field, why string) {
		m.Completeness = append(m.Completeness, adapter.CompletenessReason{Field: field, Reason: why})
	}

	// Codex records no session title in a rollout log; `session_index.jsonl`
	// carries thread names, but that is host state rather than per-session
	// provenance and it covers only threads the index still lists.
	reason("title", "codex session logs record no title")

	switch {
	case kind == KindState:
		reason("workspace", "codex host state is not workspace-scoped")
	case scan.cwd != "":
		w := scan.cwd
		m.Workspace = &w
	default:
		reason("workspace", "no session_meta or turn_context record exposed a working directory")
	}

	if !scan.first.IsZero() {
		t := scan.first
		m.CreatedAt = &t
	} else {
		reason("created_at", "no record exposed a parsable timestamp")
	}
	if !scan.last.IsZero() {
		t := scan.last
		m.ModifiedAt = &t
	} else {
		reason("modified_at", "no record exposed a parsable timestamp")
	}

	reason("lifecycle", "codex records no session lifecycle state on disk")
	reason("repo", "codex records no repository fingerprint on disk")
	return m
}

// resolveAttachments lists the live files of each referenced attachment
// directory. Missing, unreadable, or empty directories are reported as
// unresolved references rather than failing the description.
func resolveAttachments(ctx context.Context, root string, refs []string) ([]adapter.SourceFile, []string, error) {
	var (
		out        []adapter.SourceFile
		unresolved []string
	)
	for _, id := range refs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		relDir := path.Join(attachmentsDir, id)
		srcDir := filepath.Join(root, attachmentsDir, id)
		fi, err := os.Stat(srcDir)
		if err != nil || !fi.IsDir() {
			unresolved = append(unresolved, relDir)
			continue
		}
		found := 0
		walkErr := filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, err error) error {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if err != nil {
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if d.IsDir() || !d.Type().IsRegular() {
				return nil
			}
			rel, relErr := filepath.Rel(srcDir, p)
			if relErr != nil {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return nil
			}
			out = append(out, adapter.SourceFile{
				RelPath:    path.Join(relDir, filepath.ToSlash(rel)),
				SourcePath: p,
				Size:       info.Size(),
			})
			found++
			return nil
		})
		if walkErr != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			return nil, nil, fmt.Errorf("codex: read attachment %s: %w", relDir, walkErr)
		}
		if found == 0 {
			unresolved = append(unresolved, relDir)
		}
	}
	return out, unresolved, nil
}

// scanResult is everything one pass over a live log observed. Absent
// values stay zero and become completeness reasons; a parse failure only
// increments a counter, because the raw bytes are archived regardless.
type scanResult struct {
	records     int
	malformed   int
	truncated   int
	recordTypes map[string]int

	first time.Time
	last  time.Time

	sessionID         string
	threadID          string
	parentThreadID    string
	originator        string
	cliVersion        string
	modelProvider     string
	threadSource      string
	historyMode       string
	multiAgentVersion string
	cwd               string

	models            []string
	workspaceRoots    []string
	attachments       []string
	attachmentsCapped bool
}

type recordHead struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
}

type recordPayload struct {
	Payload json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	SessionID         string `json:"session_id"`
	ID                string `json:"id"`
	ParentThreadID    string `json:"parent_thread_id"`
	Timestamp         string `json:"timestamp"`
	CWD               string `json:"cwd"`
	Originator        string `json:"originator"`
	CLIVersion        string `json:"cli_version"`
	ThreadSource      string `json:"thread_source"`
	ModelProvider     string `json:"model_provider"`
	HistoryMode       string `json:"history_mode"`
	MultiAgentVersion string `json:"multi_agent_version"`
}

type turnContextPayload struct {
	CWD            string   `json:"cwd"`
	Model          string   `json:"model"`
	WorkspaceRoots []string `json:"workspace_roots"`
}

// historyRecord is the observable envelope of one `history.jsonl` entry.
// The recorded prompt text is deliberately not modelled: the adapter
// names those bytes for archival but never reads or reports them.
type historyRecord struct {
	TS int64 `json:"ts"`
}

// scanRollout extracts session metadata and attachment references from a
// live rollout log in one streaming pass.
func scanRollout(ctx context.Context, primary string) (*scanResult, error) {
	res := &scanResult{recordTypes: map[string]int{}}
	models := newStringSet()
	roots := newStringSet()
	attach := newStringSet()
	var lastTurnCWD string

	err := eachRecord(ctx, primary, func(line []byte, truncated bool) {
		res.records++
		if truncated {
			res.truncated++
		}
		if bytes.Contains(line, attachmentMarker) {
			res.collectAttachments(line, attach)
		}
		var head recordHead
		if err := json.Unmarshal(line, &head); err != nil {
			res.malformed++
			return
		}
		if head.Type != "" {
			res.recordTypes[head.Type]++
		}
		if t, ok := parseRFC3339(head.Timestamp); ok {
			res.observe(t)
		}
		switch head.Type {
		case "session_meta":
			var p sessionMetaPayload
			if !decodePayload(line, &p) {
				res.malformed++
				return
			}
			setIfEmpty(&res.sessionID, p.SessionID)
			setIfEmpty(&res.threadID, p.ID)
			setIfEmpty(&res.parentThreadID, p.ParentThreadID)
			setIfEmpty(&res.originator, p.Originator)
			setIfEmpty(&res.cliVersion, p.CLIVersion)
			setIfEmpty(&res.threadSource, p.ThreadSource)
			setIfEmpty(&res.modelProvider, p.ModelProvider)
			setIfEmpty(&res.historyMode, p.HistoryMode)
			setIfEmpty(&res.multiAgentVersion, p.MultiAgentVersion)
			setIfEmpty(&res.cwd, p.CWD)
			if t, ok := parseRFC3339(p.Timestamp); ok {
				res.observe(t)
			}
		case "turn_context":
			var p turnContextPayload
			if !decodePayload(line, &p) {
				res.malformed++
				return
			}
			models.add(p.Model)
			for _, r := range p.WorkspaceRoots {
				roots.add(r)
			}
			if p.CWD != "" {
				lastTurnCWD = p.CWD
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if res.cwd == "" {
		res.cwd = lastTurnCWD
	}
	res.models = models.sorted()
	res.workspaceRoots = roots.sorted()
	res.attachments = attach.sorted()
	if len(res.recordTypes) == 0 {
		res.recordTypes = nil
	}
	return res, nil
}

// collectAttachments records the attachment directories referenced by one
// record, stopping at maxAttachmentRefs distinct directories.
func (r *scanResult) collectAttachments(line []byte, attach *stringSet) {
	for _, m := range attachmentRefPattern.FindAllSubmatch(line, -1) {
		id := string(m[1])
		if id == "." || id == ".." {
			continue
		}
		if attach.has(id) {
			continue
		}
		if attach.len() >= maxAttachmentRefs {
			r.attachmentsCapped = true
			return
		}
		attach.add(id)
	}
}

// scanHistory extracts the observable span of the host history log. Only
// the timestamp field is interpreted; the recorded prompt text is never
// parsed or reported.
func scanHistory(ctx context.Context, primary string) (*scanResult, error) {
	res := &scanResult{}
	err := eachRecord(ctx, primary, func(line []byte, truncated bool) {
		res.records++
		if truncated {
			res.truncated++
		}
		var rec historyRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			res.malformed++
			return
		}
		if rec.TS > 0 {
			res.observe(time.Unix(rec.TS, 0).UTC())
		}
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (r *scanResult) observe(t time.Time) {
	t = t.UTC()
	if r.first.IsZero() || t.Before(r.first) {
		r.first = t
	}
	if r.last.IsZero() || t.After(r.last) {
		r.last = t
	}
}

func setIfEmpty(dst *string, v string) {
	if *dst == "" {
		*dst = v
	}
}

// decodePayload re-decodes one record for its payload. Only the few
// metadata-bearing record types pay this second parse; the bulk of a log
// is skipped by the head decode.
func decodePayload(line []byte, dst any) bool {
	var wrap recordPayload
	if err := json.Unmarshal(line, &wrap); err != nil || len(wrap.Payload) == 0 {
		return false
	}
	return json.Unmarshal(wrap.Payload, dst) == nil
}

func parseRFC3339(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// eachRecord streams a JSONL file line by line with bounded memory,
// calling fn for every non-blank line. Oversized lines are delivered
// truncated and flagged so metadata extraction degrades instead of
// failing.
func eachRecord(ctx context.Context, name string, fn func(line []byte, truncated bool)) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64<<10)
	for n := 0; ; n++ {
		if n%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		line, truncated, readErr := readRecordLine(r)
		line = bytes.TrimRight(line, "\r\n")
		if len(bytes.TrimSpace(line)) > 0 {
			fn(line, truncated)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return readErr
		}
	}
}

// readRecordLine returns the next line, or a bounded prefix of it when the
// record exceeds maxRecordBytes. The returned slice is valid only until
// the next call.
func readRecordLine(r *bufio.Reader) ([]byte, bool, error) {
	chunk, err := r.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return chunk, false, err
	}
	line := make([]byte, 0, len(chunk)*2)
	line = append(line, chunk...)
	truncated := false
	for {
		chunk, err = r.ReadSlice('\n')
		room := maxRecordBytes - len(line)
		if room > len(chunk) {
			room = len(chunk)
		}
		if room > 0 {
			line = append(line, chunk[:room]...)
		}
		if room < len(chunk) {
			truncated = true
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, truncated, err
		}
	}
}

// stringSet collects distinct non-empty values in sorted order.
type stringSet struct{ m map[string]struct{} }

func newStringSet() *stringSet { return &stringSet{m: map[string]struct{}{}} }

func (s *stringSet) add(v string) {
	if v == "" {
		return
	}
	s.m[v] = struct{}{}
}

func (s *stringSet) has(v string) bool {
	_, ok := s.m[v]
	return ok
}

func (s *stringSet) len() int { return len(s.m) }

func (s *stringSet) sorted() []string {
	if len(s.m) == 0 {
		return nil
	}
	out := make([]string, 0, len(s.m))
	for v := range s.m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}
