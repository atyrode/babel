package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/restic"
)

// maxDirNameLen bounds one fetched session's directory name. Source ids
// may be up to 512 bytes, which no filesystem accepts as one component, so
// an overlong name is folded onto a bounded prefix plus a digest of the
// full key: still deterministic, so prune derives the same name without
// consulting the repository.
const maxDirNameLen = 96

// dirDigestLen is how much of the folded key's digest disambiguates a
// truncated directory name.
const dirDigestLen = 12

// shortIDLen is how much of a snapshot id names a fetch directory when
// restic did not supply a short id itself.
const shortIDLen = 8

// minSnapshotPrefix is the shortest snapshot-id prefix `--snapshot` will
// resolve, so a stray character cannot silently select a snapshot.
const minSnapshotPrefix = 4

const sessionsUsage = `Usage: babel sessions <command> [flags]

Commands:
  list                 list this host's local sessions
  inspect SELECTOR     show one local session in full
  fetch SELECTOR       restore one session's files from a snapshot
  prune --local        remove locally fetched session directories

A selector is "HARNESS/SOURCE-ID", or any unambiguous suffix of one.

Run "babel sessions <command> -h" for a command's flags.
`

const sessionsListUsage = `Usage: babel sessions list [flags]

Lists the sessions the source adapters can see on this host. Sessions are
read in place; nothing is copied and the repository is never opened.

Flags:
  --harness NAME       restrict to one harness: omp, codex, or claude
  --roots DIR[,DIR]    scan these roots instead of the adapters' defaults
  --no-cache           bypass the catalog and describe every session
  --json               emit the listing as JSON on stdout

Absent nullable fields are displayed as "-": Babel never synthesizes a
value to satisfy a shape (SPEC.md §3).
`

const sessionsInspectUsage = `Usage: babel sessions inspect SELECTOR [flags]

Shows one local session's full description: portable metadata with the
reasons any field is absent, adapter metadata, the sibling artifact
closure, resolved blobs, unresolved blob references, and whether the
observed closure is complete enough to continue the session. The
repository is never opened.

Flags:
  --roots DIR[,DIR]    scan these roots instead of the adapters' defaults
  --json               emit the description as JSON on stdout
`

const sessionsFetchUsage = `Usage: babel sessions fetch SELECTOR --repo REPOSITORY --password-file FILE [flags]

Restores one session's file closure — its primary log, sibling artifacts,
and resolved blobs — out of a snapshot into a private directory under
Babel's data directory. The closure is resolved from the live source, so a
session that no longer exists locally cannot be addressed by selector.

Flags:
  --repo REPOSITORY    restic repository (default $BABEL_RESTIC_REPO)
  --password-file FILE password file (default $BABEL_RESTIC_PASSWORD_FILE)
  --restic-binary PATH restic executable (default "restic" from $PATH)
  --snapshot ID        snapshot id, short id, or prefix (default "latest")
  --roots DIR[,DIR]    scan these roots instead of the adapters' defaults
  --json               emit the outcome as JSON on stdout

Fetching is idempotent: an already materialized target is reported and
left untouched.
`

const sessionsPruneUsage = `Usage: babel sessions prune --local --yes (--all | SELECTOR...)

Removes locally fetched session directories under Babel's data directory.
It never opens the repository — no repository is even selectable here —
because babel never deletes archived data: never-delete is policy, and
"restic forget" and "restic prune" are not exposed by any command. Harness
source files are equally untouched.

Flags:
  --local              required: state that only local copies are removed
  --all                remove every fetched session directory
  --yes                required: confirm the deletion
  --json               emit the outcome as JSON on stdout
`

// sessions routes `babel sessions <verb>`.
func (a *app) sessions(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "sessions requires a subcommand", usage: sessionsUsage}
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
		return &usageError{msg: fmt.Sprintf("unknown sessions subcommand %q", args[0]), usage: sessionsUsage}
	}
}

// localSession pairs one discovered session with the adapter that owns it,
// which is what lets a selector resolved from a listing be described later
// without guessing which harness it came from.
type localSession struct {
	owner adapter.Adapter
	src   adapter.SourceSession
}

// key is a session's canonical selector: "<harness>/<source id>".
func (s localSession) key() string { return s.src.Harness + "/" + s.src.SourceID }

// scanFlags are the discovery knobs shared by the read commands.
type scanFlags struct {
	harness string
	roots   string
}

func (sf *scanFlags) bindRoots(c *cmd) {
	c.fs.StringVar(&sf.roots, "roots", "", "comma-separated source roots to scan instead of the adapter defaults")
}

func (sf *scanFlags) bindHarness(c *cmd) {
	c.fs.StringVar(&sf.harness, "harness", "", "restrict to one harness: omp, codex, or claude")
}

// selected resolves the adapters this invocation scans.
func (sf *scanFlags) selected(c *cmd) ([]adapter.Adapter, error) {
	all := adapters()
	if sf.harness == "" {
		return all, nil
	}
	for _, ad := range all {
		if ad.Harness() == sf.harness {
			return []adapter.Adapter{ad}, nil
		}
	}
	names := make([]string, 0, len(all))
	for _, ad := range all {
		names = append(names, ad.Harness())
	}
	return nil, c.usagef("unknown --harness %q (want one of %s)", sf.harness, strings.Join(names, ", "))
}

// rootList splits a --roots override. An empty override means every
// adapter uses its own defaults.
func (sf *scanFlags) rootList() []string {
	if strings.TrimSpace(sf.roots) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(sf.roots, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, filepath.Clean(part))
		}
	}
	return out
}

// scan discovers every local session the selected adapters can see and
// reports which harnesses discovery actually covered. A harness whose
// discovery fails is reported on stderr and skipped: one broken harness
// never hides the rest of the machine (SPEC.md §11), and it is left out of
// the covered set so nothing prunes rows it was unable to look at.
func (a *app) scan(ctx context.Context, ads []adapter.Adapter, roots []string) ([]localSession, []string) {
	var found []localSession
	var covered []string
	seen := make(map[string]struct{})
	for _, ad := range ads {
		use := roots
		if len(use) == 0 {
			use = ad.DefaultRoots()
		}
		discovered, err := ad.Discover(ctx, use)
		if err != nil {
			a.diagf("warning: discover %s: %s\n", Sanitize(ad.Harness()), Sanitize(err.Error()))
			continue
		}
		covered = append(covered, ad.Harness())
		for _, src := range discovered {
			s := localSession{owner: ad, src: src}
			k := s.key()
			if _, dup := seen[k]; dup {
				continue
			}
			seen[k] = struct{}{}
			found = append(found, s)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].key() < found[j].key() })
	return found, covered
}

// refreshScope is the set of harnesses a catalog refresh may prune. Complete
// discovery of a harness is what licenses deleting its vanished rows, so a
// harness whose discovery failed is excluded, and a --roots override — which
// deliberately scans a subtree rather than the machine — licenses no pruning
// at all.
func refreshScope(covered, roots []string) []string {
	if len(roots) > 0 {
		return nil
	}
	return covered
}

// resolveSelector maps a selector onto exactly one discovered session.
// Matching is tiered — the exact canonical key first, then a
// segment-aligned suffix, then any suffix — so a fully qualified selector
// can never be shadowed by a longer id that happens to end the same way.
// Ambiguity is a rejected invocation, not a guess: the candidates are
// listed so the operator can qualify the selector.
func resolveSelector(c *cmd, sessions []localSession, selector string) (localSession, error) {
	if selector == "" {
		return localSession{}, c.usagef("empty selector")
	}
	var exact, aligned, loose []localSession
	for _, s := range sessions {
		switch k := s.key(); {
		case k == selector:
			exact = append(exact, s)
		case strings.HasSuffix(k, "/"+selector):
			aligned = append(aligned, s)
		case strings.HasSuffix(k, selector):
			loose = append(loose, s)
		}
	}
	for _, tier := range [][]localSession{exact, aligned, loose} {
		switch len(tier) {
		case 0:
			continue
		case 1:
			return tier[0], nil
		default:
			return localSession{}, c.usagef("selector %q is ambiguous, it matches %d sessions: %s",
				selector, len(tier), strings.Join(keysOf(tier), " "))
		}
	}
	return localSession{}, fmt.Errorf("no local session matches selector %q", selector)
}

// keysOf lists the canonical keys of a candidate set.
func keysOf(sessions []localSession) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.key())
	}
	return out
}

// describe reads one session's current description. A Description is a
// best-effort view of live files, so a failure here means the source
// vanished or became unreadable, never that Babel's own state is wrong.
func describe(ctx context.Context, s localSession) (*adapter.Description, error) {
	desc, err := s.owner.Describe(ctx, s.src)
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", s.key(), err)
	}
	if desc == nil {
		return nil, fmt.Errorf("describe %s: adapter returned no description", s.key())
	}
	return desc, nil
}

// sessionRow is one session in machine-readable output. Nullable fields
// stay null rather than being filled in (SPEC.md §3); every string has
// already passed through the terminal-safe renderer.
type sessionRow struct {
	Harness    string  `json:"harness"`
	SourceID   string  `json:"source_id"`
	Selector   string  `json:"selector"`
	Size       int64   `json:"size"`
	Modified   *string `json:"modified"`
	Title      *string `json:"title"`
	Workspace  *string `json:"workspace"`
	Continuous bool    `json:"continuation_grade"`
}

// sessionsResult is the machine-readable session listing.
type sessionsResult struct {
	Sessions []sessionRow `json:"sessions"`
}

// sessionsList implements `babel sessions list`. Discovery is always live,
// while descriptions are reused until the primary file's path, size, or
// modification time changes. --no-cache retains the full live-description
// path for diagnostics and troubleshooting.
func (a *app) sessionsList(ctx context.Context, args []string) error {
	c := newCmd("sessions list", sessionsListUsage)
	var sf scanFlags
	sf.bindHarness(c)
	sf.bindRoots(c)
	noCache := c.fs.Bool("no-cache", false, "bypass the catalog and describe every session")
	asJSON := c.fs.Bool("json", false, "emit the listing as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	ads, err := sf.selected(c)
	if err != nil {
		return err
	}

	roots := sf.rootList()
	sessions, covered := a.scan(ctx, ads, roots)
	dataDir := ""
	if !*noCache {
		d, err := babelDirs()
		if err != nil {
			return err
		}
		dataDir = d.data
	}
	// A cold listing describes every session on the machine, which takes
	// minutes on a large corpus: it reports its progress on stderr so the
	// wait is never silent, while stdout keeps carrying exactly one document.
	rows, err := a.listSessionRows(ctx, sessions, refreshScope(covered, roots), dataDir, *noCache, describe, a.scanProgress().report)
	if err != nil {
		return err
	}
	res := sessionsResult{Sessions: rows}
	if *asJSON {
		return a.emitJSON(res)
	}
	if len(res.Sessions) == 0 {
		fmt.Fprint(a.stdout, "no local sessions\n")
		return nil
	}
	tableRows := make([][]string, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		tableRows = append(tableRows, []string{
			s.Harness,
			s.SourceID,
			fmt.Sprint(s.Size),
			derefOrMissing(s.Modified),
			derefOrMissing(s.Title),
			derefOrMissing(s.Workspace),
		})
	}
	return writeTable(a.stdout, []string{"HARNESS", "SOURCE ID", "SIZE", "MODIFIED", "TITLE", "WORKSPACE"}, tableRows)
}

type sessionDescribeFunc func(context.Context, localSession) (*adapter.Description, error)

// listSessionRows is the cache boundary kept separate from flag parsing so its
// incremental behavior can be exercised with an instrumented describer. scope
// names the harnesses discovery covered, which is the only set the catalog may
// prune; onProgress, when non-nil, receives one report per describe attempt.
func (a *app) listSessionRows(ctx context.Context, sessions []localSession, scope []string, dataDir string, noCache bool, describeSession sessionDescribeFunc, onProgress func(catalog.Progress)) ([]sessionRow, error) {
	if noCache {
		rows := make([]sessionRow, 0, len(sessions))
		for _, session := range sessions {
			desc, err := describeSession(ctx, session)
			if err != nil {
				a.diagf("warning: %s\n", Sanitize(err.Error()))
				continue
			}
			rows = append(rows, rowFromDescription(session, desc))
		}
		return rows, nil
	}

	cache, err := catalog.Open(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open session catalog: %w", err)
	}
	defer cache.Close()

	refs, bySelector := catalogRefs(sessions)
	cached, err := cache.Refresh(ctx, scope, refs, a.catalogDescriber(ctx, bySelector, describeSession), onProgress)
	if err != nil {
		return nil, fmt.Errorf("refresh session catalog: %w", err)
	}
	return decodeCatalogRows(cached, bySelector)
}

// catalogRefs restates one discovery result in the catalog's terms, together
// with the index the describer resolves a ref back to its session through.
func catalogRefs(sessions []localSession) ([]catalog.Ref, map[string]localSession) {
	refs := make([]catalog.Ref, 0, len(sessions))
	bySelector := make(map[string]localSession, len(sessions))
	for _, session := range sessions {
		selector := session.key()
		refs = append(refs, catalog.Ref{
			Selector:    selector,
			Harness:     session.src.Harness,
			SourceID:    session.src.SourceID,
			PrimaryPath: session.src.PrimaryPath,
		})
		bySelector[selector] = session
	}
	return refs, bySelector
}

// catalogDescriber turns one stale ref into the row the catalog caches. A
// session that cannot be described or encoded is reported and omitted, so the
// catalog never holds a row it could not round-trip.
func (a *app) catalogDescriber(ctx context.Context, bySelector map[string]localSession, describeSession sessionDescribeFunc) func(catalog.Ref) (catalog.Row, bool) {
	return func(ref catalog.Ref) (catalog.Row, bool) {
		session := bySelector[ref.Selector]
		desc, err := describeSession(ctx, session)
		if err != nil {
			a.diagf("warning: %s\n", Sanitize(err.Error()))
			return catalog.Row{}, false
		}
		row := rowFromDescription(session, desc)
		rowJSON, err := json.Marshal(row)
		if err != nil {
			a.diagf("warning: encode %s: %s\n", Sanitize(ref.Selector), Sanitize(err.Error()))
			return catalog.Row{}, false
		}
		return catalog.Row{
			Title:             row.Title,
			Workspace:         row.Workspace,
			CreatedAt:         timePtr(desc.Meta.CreatedAt),
			ModifiedAt:        row.Modified,
			ContinuationGrade: row.Continuous,
			RowJSON:           rowJSON,
		}, true
	}
}

// decodeCatalogRows renders cached rows back into listing rows. When keep is
// non-nil the listing is narrowed to the sessions discovery actually saw, so a
// harness-restricted or root-restricted invocation reports exactly what it
// scanned even though the catalog holds the whole machine.
func decodeCatalogRows(cached []catalog.Row, keep map[string]localSession) ([]sessionRow, error) {
	rows := make([]sessionRow, 0, len(cached))
	for _, cachedRow := range cached {
		if keep != nil {
			if _, ok := keep[cachedRow.Selector]; !ok {
				continue
			}
		}
		var row sessionRow
		if err := json.Unmarshal(cachedRow.RowJSON, &row); err != nil {
			return nil, fmt.Errorf("decode cached session %q: %w", cachedRow.Selector, err)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Selector < rows[j].Selector })
	return rows, nil
}

func rowFromDescription(session localSession, desc *adapter.Description) sessionRow {
	return sessionRow{
		Harness:    Sanitize(session.src.Harness),
		SourceID:   Sanitize(session.src.SourceID),
		Selector:   Sanitize(session.key()),
		Size:       desc.PrimarySize,
		Modified:   timePtr(desc.Meta.ModifiedAt),
		Title:      sanitizePtr(desc.Meta.Title),
		Workspace:  sanitizePtr(desc.Meta.Workspace),
		Continuous: desc.ContinuationGrade,
	}
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
}

// fileRow is one file of a session's closure.
type fileRow struct {
	RelPath    string `json:"rel_path"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
}

// blobRow is one resolved content-addressed blob a session references.
type blobRow struct {
	Digest     string `json:"digest"`
	SourcePath string `json:"source_path"`
	Size       int64  `json:"size"`
}

// inspectResult is one session's full description in machine-readable form.
type inspectResult struct {
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
	Repo         *repoRow          `json:"repo"`
	Completeness []completenessRow `json:"completeness,omitempty"`

	AdapterMetadataSchema int             `json:"adapter_metadata_schema"`
	AdapterMetadata       json.RawMessage `json:"adapter_metadata,omitempty"`

	Artifacts          []fileRow `json:"artifacts,omitempty"`
	Blobs              []blobRow `json:"blobs,omitempty"`
	UnresolvedBlobRefs []string  `json:"unresolved_blob_refs,omitempty"`
	ContinuationGrade  bool      `json:"continuation_grade"`
}

// sessionsInspect implements `babel sessions inspect`.
func (a *app) sessionsInspect(ctx context.Context, args []string) error {
	c := newCmd("sessions inspect", sessionsInspectUsage)
	var sf scanFlags
	sf.bindRoots(c)
	asJSON := c.fs.Bool("json", false, "emit the description as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	selector, err := c.oneSelector()
	if err != nil {
		return err
	}

	sessions, _ := a.scan(ctx, adapters(), sf.rootList())
	target, err := resolveSelector(c, sessions, selector)
	if err != nil {
		return err
	}
	desc, err := describe(ctx, target)
	if err != nil {
		return err
	}

	res := inspectResult{
		Harness:               Sanitize(desc.Source.Harness),
		SourceID:              Sanitize(desc.Source.SourceID),
		Selector:              Sanitize(target.key()),
		PrimaryPath:           Sanitize(desc.Source.PrimaryPath),
		PrimarySize:           desc.PrimarySize,
		DescribedAt:           formatTime(desc.DescribedAt),
		Hint:                  Sanitize(desc.Source.Hint),
		Title:                 sanitizePtr(desc.Meta.Title),
		Workspace:             sanitizePtr(desc.Meta.Workspace),
		CreatedAt:             timePtr(desc.Meta.CreatedAt),
		ModifiedAt:            timePtr(desc.Meta.ModifiedAt),
		Lifecycle:             sanitizePtr(desc.Meta.Lifecycle),
		Repo:                  repoOf(desc.Meta.Repo),
		AdapterMetadataSchema: desc.AdapterMetadataSchema,
		AdapterMetadata:       desc.AdapterMetadata,
		UnresolvedBlobRefs:    sanitizeAll(desc.UnresolvedBlobRefs),
		ContinuationGrade:     desc.ContinuationGrade,
	}
	for _, r := range desc.Meta.Completeness {
		res.Completeness = append(res.Completeness, completenessRow{
			Field:  Sanitize(r.Field),
			Reason: Sanitize(r.Reason),
		})
	}
	for _, f := range desc.Artifacts {
		res.Artifacts = append(res.Artifacts, fileRow{
			RelPath:    Sanitize(f.RelPath),
			SourcePath: Sanitize(f.SourcePath),
			Size:       f.Size,
		})
	}
	for _, b := range desc.Blobs {
		res.Blobs = append(res.Blobs, blobRow{
			Digest:     Sanitize(string(b.Digest)),
			SourcePath: Sanitize(b.SourcePath),
			Size:       b.Size,
		})
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	return a.printInspect(res)
}

// printInspect writes one description in human form. Adapter metadata is
// rendered as one sanitized line rather than re-indented: it is an opaque
// versioned document here, and --json is where a caller parses it.
func (a *app) printInspect(res inspectResult) error {
	rows := [][2]string{
		{"selector", res.Selector},
		{"harness", res.Harness},
		{"source id", res.SourceID},
		{"primary path", res.PrimaryPath},
		{"primary size", fmt.Sprint(res.PrimarySize)},
		{"described at", res.DescribedAt},
		{"title", derefOrMissing(res.Title)},
		{"workspace", derefOrMissing(res.Workspace)},
		{"created at", derefOrMissing(res.CreatedAt)},
		{"modified at", derefOrMissing(res.ModifiedAt)},
		{"lifecycle", derefOrMissing(res.Lifecycle)},
		{"repo", repoLine(res.Repo)},
		{"continuation grade", yesNo(res.ContinuationGrade, "yes", "no")},
		{"adapter schema", fmt.Sprint(res.AdapterMetadataSchema)},
		{"adapter metadata", orMissing(Sanitize(string(res.AdapterMetadata)))},
		{"artifacts", fmt.Sprint(len(res.Artifacts))},
		{"blobs", fmt.Sprint(len(res.Blobs))},
		{"unresolved blob refs", fmt.Sprint(len(res.UnresolvedBlobRefs))},
	}
	if err := writeDetail(a.stdout, rows); err != nil {
		return err
	}
	if len(res.Completeness) > 0 {
		fmt.Fprint(a.stdout, "\nabsent fields:\n")
		table := make([][]string, 0, len(res.Completeness))
		for _, r := range res.Completeness {
			table = append(table, []string{r.Field, r.Reason})
		}
		if err := writeTable(a.stdout, []string{"FIELD", "REASON"}, table); err != nil {
			return err
		}
	}
	if len(res.Artifacts) > 0 {
		fmt.Fprint(a.stdout, "\nartifacts:\n")
		table := make([][]string, 0, len(res.Artifacts))
		for _, f := range res.Artifacts {
			table = append(table, []string{f.RelPath, fmt.Sprint(f.Size)})
		}
		if err := writeTable(a.stdout, []string{"REL PATH", "SIZE"}, table); err != nil {
			return err
		}
	}
	if len(res.Blobs) > 0 {
		fmt.Fprint(a.stdout, "\nblobs:\n")
		table := make([][]string, 0, len(res.Blobs))
		for _, b := range res.Blobs {
			table = append(table, []string{b.Digest, fmt.Sprint(b.Size)})
		}
		if err := writeTable(a.stdout, []string{"DIGEST", "SIZE"}, table); err != nil {
			return err
		}
	}
	if len(res.UnresolvedBlobRefs) > 0 {
		fmt.Fprint(a.stdout, "\nunresolved blob references:\n")
		table := make([][]string, 0, len(res.UnresolvedBlobRefs))
		for _, ref := range res.UnresolvedBlobRefs {
			table = append(table, []string{ref})
		}
		if err := writeTable(a.stdout, []string{"REFERENCE"}, table); err != nil {
			return err
		}
	}
	return nil
}

// fetchResult is the machine-readable outcome of one materialization.
type fetchResult struct {
	Selector        string   `json:"selector"`
	SnapshotID      string   `json:"snapshot_id"`
	SnapshotShortID string   `json:"snapshot_short_id"`
	SnapshotTime    string   `json:"snapshot_time"`
	Target          string   `json:"target"`
	Files           int      `json:"files"`
	Bytes           int64    `json:"bytes"`
	Included        []string `json:"included"`
	// Missing lists the closure paths the chosen snapshot did not hold, so
	// an incomplete recovery is stated rather than inferred from a file
	// count. A path is missing because it was created after the snapshot,
	// or because no backed-up root covers it.
	Missing []string `json:"missing,omitempty"`
	// AlreadyPresent records that the target existed, so nothing was
	// restored and nothing was overwritten.
	AlreadyPresent bool `json:"already_present"`
}

// sessionsFetch implements `babel sessions fetch`. The file closure comes
// from the live source description and the bytes come from the snapshot,
// which is what makes a fetch a recovery of archived content rather than a
// copy of whatever is on disk now.
func (a *app) sessionsFetch(ctx context.Context, args []string) error {
	c := newCmd("sessions fetch", sessionsFetchUsage)
	var rf repoFlags
	var sf scanFlags
	rf.bind(c.fs)
	sf.bindRoots(c)
	snapshot := c.fs.String("snapshot", "latest", `snapshot id, short id, or prefix; "latest" for the newest`)
	asJSON := c.fs.Bool("json", false, "emit the outcome as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	selector, err := c.oneSelector()
	if err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}

	sessions, _ := a.scan(ctx, adapters(), sf.rootList())
	target, err := resolveSelector(c, sessions, selector)
	if err != nil {
		return err
	}
	desc, err := describe(ctx, target)
	if err != nil {
		return err
	}
	includes := closurePaths(desc)
	if len(includes) == 0 {
		return fmt.Errorf("session %s has no restorable file closure", target.key())
	}

	repo, err := rf.open(c, d, nil)
	if err != nil {
		return err
	}
	snapshots, err := repo.Snapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	chosen, err := pickSnapshot(c, snapshots, *snapshot)
	if err != nil {
		return err
	}

	dir := filepath.Join(d.sessionsRoot(), safeSessionDir(target.key()), shortID(chosen))
	res := fetchResult{
		Selector:        Sanitize(target.key()),
		SnapshotID:      Sanitize(chosen.ID),
		SnapshotShortID: Sanitize(shortID(chosen)),
		SnapshotTime:    formatTime(chosen.Time),
		Target:          Sanitize(dir),
		Included:        sanitizeAll(includes),
	}
	if files, bytes, err := treeSize(dir); err == nil {
		// A materialized target is left exactly as it is: a fetch is
		// idempotent, and a restored tree is never partially rewritten.
		res.Files, res.Bytes, res.AlreadyPresent = files, bytes, true
		a.diagf("note: %s is already materialized; leaving it untouched\n", Sanitize(dir))
		return a.finishFetch(res, dir, includes, *asJSON)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	// Restoring into a sibling and renaming on success means the target
	// directory exists only when its contents are complete, which is what
	// makes the idempotence check above trustworthy.
	staging := dir + ".partial"
	if err := os.RemoveAll(staging); err != nil {
		return fmt.Errorf("clear %s: %w", staging, err)
	}
	if err := ensureDir(staging); err != nil {
		return err
	}
	if err := repo.Restore(ctx, chosen.ID, includes, staging); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("restore %s: %w", target.key(), err)
	}
	files, bytes, err := treeSize(staging)
	if err != nil {
		os.RemoveAll(staging)
		return err
	}
	if files == 0 {
		os.RemoveAll(staging)
		return fmt.Errorf("restore %s: snapshot %s holds none of the session's files", target.key(), shortID(chosen))
	}
	if err := os.Rename(staging, dir); err != nil {
		os.RemoveAll(staging)
		return fmt.Errorf("publish %s: %w", dir, err)
	}
	res.Files, res.Bytes = files, bytes
	return a.finishFetch(res, dir, includes, *asJSON)
}

// maxMissingReported bounds how many absent closure paths are named on
// stderr: the count is the finding, and a session may reference thousands
// of blobs.
const maxMissingReported = 8

// finishFetch completes one fetch: it states which requested closure paths
// the snapshot did not hold, then writes the outcome to stdout. A partial
// recovery is not a failure — the snapshot is an honest record of what
// existed when it was taken — but it must never look complete.
func (a *app) finishFetch(res fetchResult, dir string, includes []string, asJSON bool) error {
	missing := missingPaths(dir, includes)
	if len(missing) > 0 {
		res.Missing = sanitizeAll(missing)
		a.diagf("warning: snapshot %s holds %d of the session's %d closure %s\n",
			res.SnapshotShortID, len(includes)-len(missing), len(includes),
			plural(len(includes), "path", "paths"))
		for i, path := range missing {
			if i == maxMissingReported {
				a.diagf("warning: and %d more absent %s\n",
					len(missing)-i, plural(len(missing)-i, "path", "paths"))
				break
			}
			a.diagf("warning: absent from the snapshot: %s\n", Sanitize(path))
		}
	}
	return a.reportFetch(res, asJSON)
}

// missingPaths reports which of the requested closure paths were not
// materialized under target. restic recreates each recorded absolute path
// beneath the target, so a path's presence is a plain lookup.
func missingPaths(target string, includes []string) []string {
	var missing []string
	for _, path := range includes {
		if _, err := os.Lstat(filepath.Join(target, path)); err != nil {
			missing = append(missing, path)
		}
	}
	return missing
}

// reportFetch writes one fetch outcome to stdout.
func (a *app) reportFetch(res fetchResult, asJSON bool) error {
	if asJSON {
		return a.emitJSON(res)
	}
	return writeDetail(a.stdout, [][2]string{
		{"selector", res.Selector},
		{"snapshot", res.SnapshotShortID},
		{"snapshot time", res.SnapshotTime},
		{"target", res.Target},
		{"files", fmt.Sprint(res.Files)},
		{"bytes", fmt.Sprint(res.Bytes)},
		{"missing", fmt.Sprint(len(res.Missing))},
		{"already present", yesNo(res.AlreadyPresent, "yes", "no")},
	})
}

// closurePaths is the absolute file closure to restore for one session:
// the primary log, its sibling artifacts, and every resolved blob. Only
// absolute paths are used, because a restic include pattern is matched
// against the absolute path recorded in the snapshot. Unresolved blob
// references are deliberately not included: there is no path to restore.
func closurePaths(desc *adapter.Description) []string {
	paths := make([]string, 0, 1+len(desc.Artifacts)+len(desc.Blobs))
	add := func(p string) {
		if filepath.IsAbs(p) {
			paths = append(paths, filepath.Clean(p))
		}
	}
	add(desc.Source.PrimaryPath)
	for _, f := range desc.Artifacts {
		add(f.SourcePath)
	}
	for _, b := range desc.Blobs {
		add(b.SourcePath)
	}
	return sortedUnique(paths)
}

// pickSnapshot resolves --snapshot. "latest" is the newest snapshot in the
// repository; anything else must identify exactly one snapshot by id,
// short id, or a prefix long enough to be deliberate.
func pickSnapshot(c *cmd, snapshots []restic.Snapshot, want string) (restic.Snapshot, error) {
	if len(snapshots) == 0 {
		return restic.Snapshot{}, errors.New("repository holds no snapshots")
	}
	if want == "" || want == "latest" {
		newest := snapshots[0]
		for _, s := range snapshots[1:] {
			if s.Time.After(newest.Time) {
				newest = s
			}
		}
		return newest, nil
	}
	var matches []restic.Snapshot
	for _, s := range snapshots {
		switch {
		case s.ID == want, s.ShortID == want:
			return s, nil
		case len(want) >= minSnapshotPrefix && strings.HasPrefix(s.ID, want):
			matches = append(matches, s)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return restic.Snapshot{}, fmt.Errorf("no snapshot matches %q", want)
	default:
		ids := make([]string, 0, len(matches))
		for _, s := range matches {
			ids = append(ids, shortID(s))
		}
		return restic.Snapshot{}, c.usagef("--snapshot %q is ambiguous, it matches %d snapshots: %s",
			want, len(matches), strings.Join(ids, " "))
	}
}

// shortID names one snapshot compactly, falling back to a prefix of the
// full id when restic supplied no short form.
func shortID(s restic.Snapshot) string {
	if s.ShortID != "" {
		return s.ShortID
	}
	if len(s.ID) > shortIDLen {
		return s.ID[:shortIDLen]
	}
	return s.ID
}

// prunedDir is one removed local session directory.
type prunedDir struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// pruneResult is the machine-readable outcome of a local prune.
type pruneResult struct {
	Root    string      `json:"root"`
	Removed []prunedDir `json:"removed"`
	Files   int         `json:"files"`
	Bytes   int64       `json:"bytes"`
}

// sessionsPrune implements `babel sessions prune --local`. It constructs no
// restic.Repo and reads no repository flag, so the archive is unreachable
// from this code path by construction — which is how SPEC.md §8's rule
// that local prune never affects the archive is enforced rather than
// merely documented. Harness source files are equally untouched: only
// Babel's own rebuildable directories under the data directory are
// removed.
func (a *app) sessionsPrune(args []string) error {
	c := newCmd("sessions prune", sessionsPruneUsage)
	local := c.fs.Bool("local", false, "required: state that only local copies are removed")
	all := c.fs.Bool("all", false, "remove every fetched session directory")
	yes := c.fs.Bool("yes", false, "required: confirm the deletion")
	asJSON := c.fs.Bool("json", false, "emit the outcome as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	selectors := c.args()
	if !*local {
		return c.usagef("sessions prune requires --local: babel never deletes archived data, so no other prune exists")
	}
	if !*yes {
		return c.usagef("refusing to delete without --yes")
	}
	switch {
	case *all && len(selectors) > 0:
		return c.usagef("--all takes no selectors, got %q", selectors[0])
	case !*all && len(selectors) == 0:
		return c.usagef("sessions prune needs --all or at least one SELECTOR")
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}

	root := d.sessionsRoot()
	targets, err := pruneTargets(a, c, root, *all, selectors)
	if err != nil {
		return err
	}
	res := pruneResult{Root: Sanitize(root)}
	for _, name := range targets {
		dir := filepath.Join(root, name)
		files, bytes, err := treeSize(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return err
		}
		if err := os.RemoveAll(dir); err != nil {
			return fmt.Errorf("remove %s: %w", dir, err)
		}
		res.Removed = append(res.Removed, prunedDir{
			Name:  Sanitize(name),
			Path:  Sanitize(dir),
			Files: files,
			Bytes: bytes,
		})
		res.Files += files
		res.Bytes += bytes
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if len(res.Removed) == 0 {
		fmt.Fprint(a.stdout, "nothing to remove\n")
		return nil
	}
	rows := make([][]string, 0, len(res.Removed))
	for _, r := range res.Removed {
		rows = append(rows, []string{r.Name, fmt.Sprint(r.Files), fmt.Sprint(r.Bytes)})
	}
	if err := writeTable(a.stdout, []string{"DIRECTORY", "FILES", "BYTES"}, rows); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "removed %d %s, %d bytes\n",
		len(res.Removed), plural(len(res.Removed), "directory", "directories"), res.Bytes)
	return nil
}

// pruneTargets resolves which directory names under root to remove.
// Selectors are matched against the fetch naming rule rather than against
// discovered sessions, so a session whose source files are already gone
// can still have its local copies pruned. A selector matching nothing is a
// note, not a failure: prune is idempotent.
func pruneTargets(a *app, c *cmd, root string, all bool, selectors []string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if all {
		return names, nil
	}

	var targets []string
	seen := make(map[string]struct{})
	for _, selector := range selectors {
		want := safeSessionDir(selector)
		var exact, suffix []string
		for _, name := range names {
			switch {
			case name == want:
				exact = append(exact, name)
			case strings.HasSuffix(name, want):
				suffix = append(suffix, name)
			}
		}
		matches := exact
		if len(matches) == 0 {
			matches = suffix
		}
		switch len(matches) {
		case 0:
			a.diagf("note: no fetched directory matches selector %s\n", Sanitize(selector))
		case 1:
			if _, dup := seen[matches[0]]; !dup {
				seen[matches[0]] = struct{}{}
				targets = append(targets, matches[0])
			}
		default:
			return nil, c.usagef("selector %q is ambiguous, it matches %d fetched directories: %s",
				selector, len(matches), strings.Join(matches, " "))
		}
	}
	return targets, nil
}

// safeSessionDir maps a session key onto one filesystem path component:
// characters outside [A-Za-z0-9._-] become "-", and an overlong name is
// folded onto a bounded prefix plus a digest of the full key, so the
// mapping stays injective in practice and reproducible without any
// repository access.
func safeSessionDir(key string) string {
	b := make([]byte, 0, len(key))
	for i := range len(key) {
		switch c := key[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			b = append(b, c)
		default:
			b = append(b, '-')
		}
	}
	name := strings.TrimLeft(string(b), ".")
	if name == "" {
		name = "session"
	}
	if len(name) <= maxDirNameLen {
		return name
	}
	fold := digestOf(key)
	return name[:maxDirNameLen-len(fold)-1] + "-" + fold
}

// digestOf is the bounded digest that disambiguates a folded directory
// name. It is derived from the whole key, so two keys sharing a truncated
// prefix still get distinct directories.
func digestOf(key string) string {
	return digest.Bytes([]byte(key)).Hex()[:dirDigestLen]
}

// treeSize counts the regular files and bytes below dir. It reports
// fs.ErrNotExist for an absent directory, so callers can distinguish an
// already pruned or not-yet-fetched target from a failure.
func treeSize(dir string) (files int, bytes int64, err error) {
	info, err := os.Stat(dir)
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("%s is not a directory", dir)
	}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		fi, err := entry.Info()
		if err != nil {
			return err
		}
		files++
		bytes += fi.Size()
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
	s := formatTime(*t)
	return &s
}

// repoOf renders a nullable repository fingerprint.
func repoOf(r *adapter.RepoFingerprint) *repoRow {
	if r == nil {
		return nil
	}
	return &repoRow{
		Remote: Sanitize(r.Remote),
		Commit: Sanitize(r.Commit),
		Branch: Sanitize(r.Branch),
	}
}

// repoLine renders a repository fingerprint on one detail line.
func repoLine(r *repoRow) string {
	if r == nil {
		return missingValue
	}
	parts := make([]string, 0, 3)
	if r.Remote != "" {
		parts = append(parts, r.Remote)
	}
	if r.Branch != "" {
		parts = append(parts, r.Branch)
	}
	if r.Commit != "" {
		parts = append(parts, r.Commit)
	}
	if len(parts) == 0 {
		return missingValue
	}
	return strings.Join(parts, " ")
}
