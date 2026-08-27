package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/catalog"
)

// digestPrefixLen is how much of a revision's content digest names its
// bundle directory. It disambiguates a session's revisions locally while
// keeping the path readable; the full digest stays in the manifest.
const digestPrefixLen = 12

// maxBundleNameLen bounds a bundle directory name. Source ids may be up to
// 512 bytes, which no filesystem accepts as one component, so an overlong
// name is folded onto a bounded prefix plus a digest of the full session
// key: still deterministic, so prune derives the same path without reading
// the archive.
const maxBundleNameLen = 96

// knownHarnesses are the harness names `--harness` accepts.
var knownHarnesses = []string{"omp", "codex", "claude"}

const sessionsUsage = `Usage: babel sessions <command> [flags]

Commands:
  list                  list committed sessions
  inspect SELECTOR      show one revision's full envelope
  fetch SELECTOR        materialize one revision into the local data directory
  prune --local ...     remove locally fetched bundles

A selector is "SESSION" (that session's newest committed revision) or
"SESSION@sha256:<hex>" (exactly that revision).

Run "babel sessions <command> -h" for a command's flags.
`

const sessionsListUsage = `Usage: babel sessions list --archive-backend local|rclone --archive-root PATH [flags]

List every committed session: key, harness, title, workspace, modification
time, and revision count. Read-only with respect to the archive; no
transcript bytes are downloaded.

Flags:
  --archive-backend local|rclone   archive backend (required)
  --archive-root PATH              local root directory, or rclone remote (required)
  --harness omp|codex|claude       restrict to one harness
  --host ID                        restrict to one host; repeatable
  --json                           emit the result as a JSON object on stdout
`

const sessionsInspectUsage = `Usage: babel sessions inspect SELECTOR --archive-backend local|rclone --archive-root PATH [flags]

Show one immutable revision in full: identity, encoding and append-chain
depth, digests, nullable envelope fields with their completeness reasons,
artifact and blob counts, and continuation grade. Read-only with respect to
the archive; no transcript bytes are downloaded.

Flags:
  --archive-backend local|rclone   archive backend (required)
  --archive-root PATH              local root directory, or rclone remote (required)
  --json                           emit the result as a JSON object on stdout
`

const sessionsFetchUsage = `Usage: babel sessions fetch SELECTOR --archive-backend local|rclone --archive-root PATH [flags]

Materialize one immutable revision and its declared artifact and blob
closure into the local data directory, verifying every byte against its
digest. The bundle path is derived from the session key and the revision's
content digest, so a fetch is idempotent: an already fetched bundle is
reported and left untouched, and the command still succeeds.

Flags:
  --archive-backend local|rclone   archive backend (required)
  --archive-root PATH              local root directory, or rclone remote (required)
  --json                           emit the result as a JSON object on stdout
`

const sessionsPruneUsage = `Usage: babel sessions prune --local --yes (--all | SELECTOR...)

Remove locally fetched bundles from Babel's data directory. This command
opens no object store: it cannot affect the archive (SPEC.md §8). Local
source sessions are never touched either; only rebuildable fetched bundles
are removed.

Flags:
  --local     required: acknowledges that only local materializations are removed
  --all       remove every fetched bundle
  --yes       required: confirm the removal
  --json      emit the result as a JSON object on stdout
`

// sessions routes `babel sessions <verb>`.
func (a *app) sessions(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "sessions needs a command", usage: sessionsUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, sessionsUsage)
		return nil
	case "list":
		return a.sessionsList(ctx, args[1:])
	case "inspect":
		return a.sessionsInspect(ctx, args[1:])
	case "fetch":
		return a.sessionsFetch(ctx, args[1:])
	case "prune":
		return a.sessionsPrune(args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown sessions command %q", args[0]), usage: sessionsUsage}
	}
}

// sessionRow is one session in machine-readable output. Nullable fields
// stay null rather than being filled in (SPEC.md §3); every string has
// already passed through the terminal-safe renderer.
type sessionRow struct {
	SessionKey        string  `json:"session_key"`
	Harness           string  `json:"harness"`
	HostID            string  `json:"host_id"`
	SourceID          string  `json:"source_id"`
	Title             *string `json:"title"`
	Workspace         *string `json:"workspace"`
	ModifiedAt        *string `json:"modified_at"`
	SnapshotTime      string  `json:"snapshot_time"`
	Revisions         int     `json:"revisions"`
	NewestRevision    string  `json:"newest_revision"`
	ContinuationGrade bool    `json:"continuation_grade"`
}

// sessionsResult is the machine-readable session listing.
type sessionsResult struct {
	Sessions []sessionRow `json:"sessions"`
}

// sessionsList implements `babel sessions list`.
func (a *app) sessionsList(ctx context.Context, args []string) error {
	c := newCmd("sessions list", sessionsListUsage)
	var sf storeFlags
	sf.bind(c.fs)
	var hosts stringList
	c.fs.Var(&hosts, "host", "restrict to one host; repeatable")
	harness := c.fs.String("harness", "", "restrict to one harness: omp, codex, or claude")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	if *harness != "" && !slices.Contains(knownHarnesses, *harness) {
		return c.usagef("unknown --harness %q (want omp, codex, or claude)", *harness)
	}
	cat, _, err := a.loadCatalog(ctx, c, &sf, hosts)
	if err != nil {
		return err
	}
	a.reportHostDefects(cat.Hosts())

	out := sessionsResult{Sessions: []sessionRow{}}
	for _, s := range cat.Sessions() {
		if *harness != "" && s.Key.Harness != *harness {
			continue
		}
		out.Sessions = append(out.Sessions, sessionRow{
			SessionKey:        Sanitize(s.Key.String()),
			Harness:           Sanitize(s.Key.Harness),
			HostID:            Sanitize(s.Key.HostID),
			SourceID:          Sanitize(s.Key.SourceID),
			Title:             sanitizePtr(s.Title),
			Workspace:         sanitizePtr(s.Workspace),
			ModifiedAt:        timePtr(s.ModifiedAt),
			SnapshotTime:      formatTime(s.SnapshotTime),
			Revisions:         s.RevisionCount(),
			NewestRevision:    Sanitize(s.Newest.Key()),
			ContinuationGrade: s.ContinuationGrade,
		})
	}

	if *asJSON {
		return a.emitJSON(out)
	}
	rows := make([][]string, 0, len(out.Sessions))
	for _, s := range out.Sessions {
		rows = append(rows, []string{
			s.SessionKey,
			s.Harness,
			derefOrMissing(s.Title),
			derefOrMissing(s.Workspace),
			derefOrMissing(s.ModifiedAt),
			fmt.Sprint(s.Revisions),
		})
	}
	if err := writeTable(a.stdout, []string{"SESSION", "HARNESS", "TITLE", "WORKSPACE", "MODIFIED", "REVISIONS"}, rows); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "%d %s\n", len(out.Sessions), plural(len(out.Sessions), "session", "sessions"))
	return nil
}

// completenessRow explains one absent nullable field.
type completenessRow struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// repoRow is a session's best-effort repository fingerprint.
type repoRow struct {
	Remote string `json:"remote,omitempty"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
	Dirty  *bool  `json:"dirty,omitempty"`
}

// inspectResult is one revision's full envelope in machine-readable form.
type inspectResult struct {
	SessionKey  string `json:"session_key"`
	RevisionKey string `json:"revision_key"`
	Harness     string `json:"harness"`
	HostID      string `json:"host_id"`
	SourceID    string `json:"source_id"`

	ManifestSchema  int    `json:"manifest_schema"`
	AdapterSchema   int    `json:"adapter_schema"`
	Generation      uint64 `json:"generation"`
	GenerationAdded uint64 `json:"generation_added"`

	SnapshotTime   string `json:"snapshot_time"`
	Encoding       string `json:"encoding"`
	ContentDigest  string `json:"content_digest"`
	ContentSize    int64  `json:"content_size"`
	ObjectDigest   string `json:"object_digest"`
	ObjectSize     int64  `json:"object_size"`
	ParentRevision string `json:"parent_revision,omitempty"`
	ChainDepth     int    `json:"chain_depth"`

	Title      *string  `json:"title"`
	Workspace  *string  `json:"workspace"`
	CreatedAt  *string  `json:"created_at"`
	ModifiedAt *string  `json:"modified_at"`
	Lifecycle  *string  `json:"lifecycle"`
	Repo       *repoRow `json:"repo"`

	Completeness []completenessRow `json:"completeness,omitempty"`

	Artifacts             int      `json:"artifacts"`
	ArtifactBytes         int64    `json:"artifact_bytes"`
	Blobs                 int      `json:"blobs"`
	BlobBytes             int64    `json:"blob_bytes"`
	UnresolvedBlobRefs    []string `json:"unresolved_blob_refs,omitempty"`
	ContinuationGrade     bool     `json:"continuation_grade"`
	AdapterMetadataSchema int      `json:"adapter_metadata_schema,omitempty"`
	AdapterMetadataBytes  int      `json:"adapter_metadata_bytes"`

	SessionRevisions int `json:"session_revisions"`
}

// sessionsInspect implements `babel sessions inspect`.
func (a *app) sessionsInspect(ctx context.Context, args []string) error {
	c := newCmd("sessions inspect", sessionsInspectUsage)
	var sf storeFlags
	sf.bind(c.fs)
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	selector, err := oneSelector(c)
	if err != nil {
		return err
	}
	cat, _, err := a.loadCatalog(ctx, c, &sf, nil)
	if err != nil {
		return err
	}
	a.reportHostDefects(cat.Hosts())
	rev, err := cat.Resolve(selector)
	if err != nil {
		return err
	}

	e := rev.Entry
	out := inspectResult{
		SessionKey:            Sanitize(e.SessionKey),
		RevisionKey:           Sanitize(e.RevisionKey),
		Harness:               Sanitize(e.Harness),
		HostID:                Sanitize(e.HostID),
		SourceID:              Sanitize(e.SourceID),
		ManifestSchema:        e.ManifestSchema,
		AdapterSchema:         e.AdapterSchema,
		Generation:            rev.Generation,
		GenerationAdded:       e.GenerationAdded,
		SnapshotTime:          formatTime(e.SnapshotTime),
		Encoding:              Sanitize(string(e.Encoding)),
		ContentDigest:         Sanitize(string(e.Content.Digest)),
		ContentSize:           e.Content.Size,
		ObjectDigest:          Sanitize(string(e.Object.Digest)),
		ObjectSize:            e.Object.Size,
		ParentRevision:        Sanitize(e.ParentRevision),
		ChainDepth:            e.ChainDepth,
		Title:                 sanitizePtr(e.Title),
		Workspace:             sanitizePtr(e.Workspace),
		CreatedAt:             timePtr(e.CreatedAt),
		ModifiedAt:            timePtr(e.ModifiedAt),
		Lifecycle:             sanitizePtr(e.Lifecycle),
		Repo:                  repoOf(e.Repo),
		Artifacts:             len(e.Artifacts),
		Blobs:                 len(e.Blobs),
		UnresolvedBlobRefs:    sanitizeAll(e.UnresolvedBlobRefs),
		ContinuationGrade:     e.ContinuationGrade,
		AdapterMetadataSchema: e.AdapterMetadataSchema,
		AdapterMetadataBytes:  len(e.AdapterMetadata),
	}
	for _, art := range e.Artifacts {
		out.ArtifactBytes += art.Size
	}
	for _, b := range e.Blobs {
		out.BlobBytes += b.Size
	}
	for _, r := range e.Completeness {
		out.Completeness = append(out.Completeness, completenessRow{Field: Sanitize(r.Field), Reason: Sanitize(r.Reason)})
	}
	if s, ok := cat.Session(e.SessionKey); ok {
		out.SessionRevisions = s.RevisionCount()
	}

	if *asJSON {
		return a.emitJSON(out)
	}
	rows := [][2]string{
		{"session", out.SessionKey},
		{"revision", out.RevisionKey},
		{"harness", out.Harness},
		{"host", out.HostID},
		{"source", out.SourceID},
		{"generation", fmt.Sprintf("g%d (added g%d)", out.Generation, out.GenerationAdded)},
		{"schemas", fmt.Sprintf("manifest %d, adapter %d", out.ManifestSchema, out.AdapterSchema)},
		{"snapshot", out.SnapshotTime},
		{"encoding", out.Encoding},
		{"chain depth", fmt.Sprint(out.ChainDepth)},
		{"parent revision", orMissing(out.ParentRevision)},
		{"content", fmt.Sprintf("%s (%d bytes)", out.ContentDigest, out.ContentSize)},
		{"stored object", fmt.Sprintf("%s (%d bytes)", out.ObjectDigest, out.ObjectSize)},
		{"title", derefOrMissing(out.Title)},
		{"workspace", derefOrMissing(out.Workspace)},
		{"created", derefOrMissing(out.CreatedAt)},
		{"modified", derefOrMissing(out.ModifiedAt)},
		{"lifecycle", derefOrMissing(out.Lifecycle)},
		{"repo", repoLine(out.Repo)},
		{"artifacts", fmt.Sprintf("%d (%d bytes)", out.Artifacts, out.ArtifactBytes)},
		{"blobs", fmt.Sprintf("%d (%d bytes)", out.Blobs, out.BlobBytes)},
		{"unresolved refs", fmt.Sprint(len(out.UnresolvedBlobRefs))},
		{"continuation grade", yesNo(out.ContinuationGrade, "yes", "no")},
		{"adapter metadata", fmt.Sprintf("schema %d, %d bytes", out.AdapterMetadataSchema, out.AdapterMetadataBytes)},
		{"session revisions", fmt.Sprint(out.SessionRevisions)},
	}
	for _, r := range out.Completeness {
		rows = append(rows, [2]string{"absent " + r.Field, r.Reason})
	}
	for _, ref := range out.UnresolvedBlobRefs {
		rows = append(rows, [2]string{"unresolved ref", ref})
	}
	return writeDetail(a.stdout, rows)
}

// fetchResult is the machine-readable outcome of one materialization.
type fetchResult struct {
	SessionKey         string   `json:"session_key"`
	RevisionKey        string   `json:"revision_key"`
	Harness            string   `json:"harness"`
	HostID             string   `json:"host_id"`
	Dir                string   `json:"dir"`
	Files              int      `json:"files"`
	TotalSize          int64    `json:"total_size"`
	Encoding           string   `json:"encoding"`
	ChainLength        int      `json:"chain_length,omitempty"`
	AlreadyFetched     bool     `json:"already_fetched"`
	UnresolvedBlobRefs []string `json:"unresolved_blob_refs,omitempty"`
}

// sessionsFetch implements `babel sessions fetch`.
func (a *app) sessionsFetch(ctx context.Context, args []string) error {
	c := newCmd("sessions fetch", sessionsFetchUsage)
	var sf storeFlags
	sf.bind(c.fs)
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	selector, err := oneSelector(c)
	if err != nil {
		return err
	}
	cat, st, err := a.loadCatalog(ctx, c, &sf, nil)
	if err != nil {
		return err
	}
	a.reportHostDefects(cat.Hosts())
	rev, err := cat.Resolve(selector)
	if err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}
	if err := ensureDir(d.bundlesRoot()); err != nil {
		return err
	}
	dest := bundleDir(d.bundlesRoot(), rev.SessionKeyString(), rev.Entry.Content.Digest.Hex())

	out := fetchResult{
		SessionKey:         Sanitize(rev.SessionKeyString()),
		RevisionKey:        Sanitize(rev.Key()),
		Harness:            Sanitize(rev.Entry.Harness),
		HostID:             Sanitize(rev.Entry.HostID),
		Dir:                Sanitize(dest),
		Encoding:           Sanitize(string(rev.Entry.Encoding)),
		UnresolvedBlobRefs: sanitizeAll(rev.Entry.UnresolvedBlobRefs),
	}

	a.diagf("babel: fetching revision %s\n", out.RevisionKey)
	mat, err := catalog.Fetch(ctx, st, rev, dest)
	switch {
	case errors.Is(err, catalog.ErrDestExists):
		// A fetched bundle is immutable and its path is a pure function of
		// the revision's content digest, so an existing bundle already is
		// the requested result: report it and succeed.
		files, size, statErr := treeSize(dest)
		if statErr != nil {
			return statErr
		}
		out.AlreadyFetched = true
		out.Files = files
		out.TotalSize = size
		a.diagf("babel: revision already fetched; nothing to download\n")
	case err != nil:
		return err
	default:
		out.Files = len(mat.Files)
		out.TotalSize = mat.TotalSize
		out.ChainLength = mat.ChainLength
		out.Dir = Sanitize(mat.Dir)
	}

	if *asJSON {
		return a.emitJSON(out)
	}
	fmt.Fprintln(a.stdout, out.Dir)
	fmt.Fprintf(a.stdout, "%d %s, %d bytes%s\n",
		out.Files, plural(out.Files, "file", "files"), out.TotalSize,
		yesNo(out.AlreadyFetched, " (already fetched)", ""))
	return nil
}

// prunedBundle is one removed local bundle.
type prunedBundle struct {
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// pruneResult is the machine-readable outcome of a local prune.
type pruneResult struct {
	BundlesRoot string         `json:"bundles_root"`
	Removed     []prunedBundle `json:"removed"`
	Missing     []string       `json:"missing,omitempty"`
	Files       int            `json:"files"`
	BytesFreed  int64          `json:"bytes_freed"`
}

// sessionsPrune implements `babel sessions prune --local`. It never opens an
// object store: the archive is unreachable from this code path by
// construction, which is how SPEC.md §8's "local prune never affects the
// remote" rule is enforced rather than merely documented. Local harness
// source sessions are equally untouched; only Babel's own rebuildable
// bundles under the data directory are removed.
func (a *app) sessionsPrune(args []string) error {
	c := newCmd("sessions prune", sessionsPruneUsage)
	localOnly := c.fs.Bool("local", false, "required: only local materializations are removed")
	all := c.fs.Bool("all", false, "remove every fetched bundle")
	yes := c.fs.Bool("yes", false, "required: confirm the removal")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if !*localOnly {
		return c.usagef("sessions prune requires --local: Babel prunes only local materializations")
	}
	selectors := c.args()
	switch {
	case *all && len(selectors) > 0:
		return c.usagef("--all takes no selectors")
	case !*all && len(selectors) == 0:
		return c.usagef("sessions prune needs --all or at least one SESSION[@REVISION] selector")
	case !*yes:
		return c.usagef("sessions prune requires --yes to confirm removing local bundles")
	}

	d, err := babelDirs()
	if err != nil {
		return err
	}
	root := d.bundlesRoot()
	targets, err := pruneTargets(c, root, *all, selectors)
	if err != nil {
		return err
	}

	out := pruneResult{BundlesRoot: Sanitize(root), Removed: []prunedBundle{}}
	for _, target := range targets {
		files, size, err := treeSize(target)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			out.Missing = append(out.Missing, Sanitize(target))
			continue
		case err != nil:
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove bundle %s: %w", target, err)
		}
		out.Removed = append(out.Removed, prunedBundle{Path: Sanitize(target), Files: files, Bytes: size})
		out.Files += files
		out.BytesFreed += size
	}
	for _, m := range out.Missing {
		a.diagf("babel: no local bundle at %s\n", m)
	}

	if *asJSON {
		return a.emitJSON(out)
	}
	for _, r := range out.Removed {
		fmt.Fprintln(a.stdout, r.Path)
	}
	fmt.Fprintf(a.stdout, "removed %d %s, %d %s, %d bytes\n",
		len(out.Removed), plural(len(out.Removed), "bundle", "bundles"),
		out.Files, plural(out.Files, "file", "files"), out.BytesFreed)
	return nil
}

// pruneTargets resolves what to remove. --all enumerates the bundle root;
// a session selector removes that session's whole bundle directory; a
// revision selector removes exactly the one revision's bundle, derived from
// its content digest without any archive access.
func pruneTargets(c *cmd, root string, all bool, selectors []string) ([]string, error) {
	if all {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, fmt.Errorf("read bundle root %s: %w", root, err)
		}
		targets := make([]string, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				targets = append(targets, filepath.Join(root, e.Name()))
			}
		}
		return targets, nil
	}
	targets := make([]string, 0, len(selectors))
	for _, sel := range selectors {
		if strings.Contains(sel, "@") {
			key, digest, err := archive.ParseRevisionKey(sel)
			if err != nil {
				return nil, c.usagef("invalid selector %q: %v", sel, err)
			}
			targets = append(targets, bundleDir(root, key.String(), digest.Hex()))
			continue
		}
		key, err := archive.ParseSessionKey(sel)
		if err != nil {
			return nil, c.usagef("invalid selector %q: %v", sel, err)
		}
		targets = append(targets, filepath.Join(root, safeSessionDir(key.String())))
	}
	return targets, nil
}

// oneSelector requires exactly one positional selector.
func oneSelector(c *cmd) (string, error) {
	switch args := c.args(); len(args) {
	case 1:
		return args[0], nil
	case 0:
		return "", c.usagef("%s needs a SESSION[@REVISION] selector", c.fs.Name())
	default:
		return "", c.usagef("%s takes one selector, got %d", c.fs.Name(), len(args))
	}
}

// safeSessionDir maps a canonical session key onto one filesystem path
// component: characters outside [A-Za-z0-9._-] become "-", and an overlong
// name is folded onto a bounded prefix plus a digest of the full key so the
// mapping stays injective in practice and reproducible without the archive.
func safeSessionDir(sessionKey string) string {
	var b strings.Builder
	b.Grow(len(sessionKey))
	for i := range len(sessionKey) {
		c := sessionKey[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '.', c == '_', c == '-':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.TrimLeft(b.String(), ".")
	if name == "" {
		name = "session"
	}
	if len(name) > maxBundleNameLen {
		sum := sha256.Sum256([]byte(sessionKey))
		name = name[:maxBundleNameLen-1-digestPrefixLen] + "-" + hex.EncodeToString(sum[:])[:digestPrefixLen]
	}
	return name
}

// treeSize counts the regular files and bytes below dir. It reports
// fs.ErrNotExist for an absent directory so callers can distinguish an
// already pruned bundle from a failure.
func treeSize(dir string) (files int, bytes int64, err error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, 0, err
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			files++
			bytes += info.Size()
		}
		return nil
	})
	if err != nil {
		return 0, 0, fmt.Errorf("measure %s: %w", dir, err)
	}
	return files, bytes, nil
}

// sanitizePtr renders a nullable untrusted string, preserving absence.
func sanitizePtr(p *string) *string {
	if p == nil {
		return nil
	}
	s := Sanitize(*p)
	return &s
}

// timePtr renders a nullable timestamp, preserving absence.
func timePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTimePtr(t)
	return &s
}

// repoOf renders a nullable repository fingerprint.
func repoOf(r *archive.RepoFingerprint) *repoRow {
	if r == nil {
		return nil
	}
	return &repoRow{
		Remote: Sanitize(r.Remote),
		Commit: Sanitize(r.Commit),
		Branch: Sanitize(r.Branch),
		Dirty:  r.Dirty,
	}
}

// repoLine renders a repository fingerprint on one detail line.
func repoLine(r *repoRow) string {
	if r == nil {
		return missingValue
	}
	parts := make([]string, 0, 4)
	if r.Remote != "" {
		parts = append(parts, r.Remote)
	}
	if r.Branch != "" {
		parts = append(parts, r.Branch)
	}
	if r.Commit != "" {
		parts = append(parts, r.Commit)
	}
	if r.Dirty != nil {
		parts = append(parts, yesNo(*r.Dirty, "dirty", "clean"))
	}
	if len(parts) == 0 {
		return missingValue
	}
	return strings.Join(parts, " ")
}

// bundleDir is the deterministic destination of one fetched revision:
// <data dir>/bundles/<safe session name>/<content digest prefix>
// (SPEC.md §9). Both fetch and local prune derive it the same way, so prune
// needs no archive access to find a bundle.
func bundleDir(bundlesRoot, sessionKey string, contentHex string) string {
	return filepath.Join(bundlesRoot, safeSessionDir(sessionKey), contentHex[:digestPrefixLen])
}
