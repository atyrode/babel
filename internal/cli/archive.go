package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/adapter/claude"
	"github.com/atyrode/babel/internal/adapter/codex"
	"github.com/atyrode/babel/internal/adapter/omp"
	"github.com/atyrode/babel/internal/archive"
	"github.com/atyrode/babel/internal/catalog"
	"github.com/atyrode/babel/internal/objectstore"
	"github.com/atyrode/babel/internal/publish"
)

const archiveUsage = `Usage: babel archive <command> [flags]

Commands:
  push      publish this host's local sessions as a new immutable generation
  catalog   list every host's committed generation and session counts
  status    report head generation, bootstrap, hint, and journal state
  verify    verify committed state; --deep also re-reads every object

Run "babel archive <command> -h" for a command's flags.
`

const archivePushUsage = `Usage: babel archive push --archive-backend local|rclone --archive-root PATH [flags]

Publish every locally discovered OMP, Codex, and Claude Code session as one
new immutable generation. Push is the only archive command that writes; it
never deletes remote objects and re-running it converges.

Flags:
  --archive-backend local|rclone   archive backend (required)
  --archive-root PATH              local root directory, or rclone remote (required)
  --host ID                        this host's archive identity
                                   (default: $BABEL_HOST_ID, else the system hostname)
  --display-name NAME              human host name recorded in the commit record
  --json                           emit the result as a JSON object on stdout
`

const archiveCatalogUsage = `Usage: babel archive catalog --archive-backend local|rclone --archive-root PATH [flags]

List each host's exposed committed generation with its session and revision
counts. Read-only with respect to the archive.

Flags:
  --archive-backend local|rclone   archive backend (required)
  --archive-root PATH              local root directory, or rclone remote (required)
  --host ID                        restrict to one host; repeatable
  --json                           emit the result as a JSON object on stdout
`

const archiveStatusUsage = `Usage: babel archive status --archive-backend local|rclone --archive-root PATH [flags]

Report, per host, the head generation Babel would read, whether its
bootstrap scan was complete, how the non-authoritative latest hint compares,
and whether this machine holds a local publication journal for the host.

Flags:
  --archive-backend local|rclone   archive backend (required)
  --archive-root PATH              local root directory, or rclone remote (required)
  --host ID                        restrict to one host; repeatable
  --json                           emit the result as a JSON object on stdout
`

const archiveVerifyUsage = `Usage: babel archive verify --archive-backend local|rclone --archive-root PATH [flags]

Verify committed state: commit records against their write-once keys,
generation indexes and manifest segments against their digests, entry and
append-chain contracts, and referenced object presence and size. --deep
additionally reads every referenced object end to end and reassembles every
append-delta chain.

Errors exit 1; warnings (such as two commit records at one generation) are
printed to stderr and exit 0.

Flags:
  --archive-backend local|rclone   archive backend (required)
  --archive-root PATH              local root directory, or rclone remote (required)
  --deep                           also verify every object's bytes
  --host ID                        restrict to one host; repeatable
  --json                           emit the result as a JSON object on stdout
`

// archive routes `babel archive <verb>`.
func (a *app) archive(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "archive needs a command", usage: archiveUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, archiveUsage)
		return nil
	case "push":
		return a.archivePush(ctx, args[1:])
	case "catalog":
		return a.archiveCatalog(ctx, args[1:])
	case "status":
		return a.archiveStatus(ctx, args[1:])
	case "verify":
		return a.archiveVerify(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown archive command %q", args[0]), usage: archiveUsage}
	}
}

// coverageRow is one adapter's scan coverage in machine-readable output.
type coverageRow struct {
	Harness         string   `json:"harness"`
	AdapterSchema   int      `json:"adapter_schema"`
	Scanned         int      `json:"scanned"`
	Published       int      `json:"published"`
	CarriedForward  int      `json:"carried_forward"`
	Deferred        int      `json:"deferred"`
	Complete        bool     `json:"complete"`
	DeferredReasons []string `json:"deferred_reasons,omitempty"`
}

// pushResult is the machine-readable outcome of one publication.
type pushResult struct {
	HostID            string        `json:"host_id"`
	Generation        uint64        `json:"generation"`
	Changed           bool          `json:"changed"`
	Bootstrap         bool          `json:"bootstrap"`
	BootstrapComplete bool          `json:"bootstrap_complete"`
	Sessions          int           `json:"sessions"`
	Revisions         int           `json:"revisions"`
	Published         int           `json:"published"`
	CarriedForward    int           `json:"carried_forward"`
	Deferred          int           `json:"deferred"`
	CommitKey         string        `json:"commit_key,omitempty"`
	CommitDigest      string        `json:"commit_digest,omitempty"`
	Coverage          []coverageRow `json:"coverage"`
}

// archivePush implements `babel archive push`: it wires the three source
// adapters into the publication pipeline with Babel's private XDG state and
// staging directories and reports what was committed.
func (a *app) archivePush(ctx context.Context, args []string) error {
	c := newCmd("archive push", archivePushUsage)
	var sf storeFlags
	sf.bind(c.fs)
	host := c.fs.String("host", "", "this host's archive identity")
	displayName := c.fs.String("display-name", "", "human host name recorded in the commit record")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	st, err := sf.open(c)
	if err != nil {
		return err
	}
	hostID, err := resolveHostID(c, *host)
	if err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}
	if err := ensureDir(d.state); err != nil {
		return err
	}
	staging := d.stagingRoot()
	if err := ensureDir(staging); err != nil {
		return err
	}

	pub, err := publish.New(publish.Config{
		Store:           st,
		Adapters:        []adapter.Adapter{omp.New(), codex.New(), claude.New()},
		HostID:          hostID,
		HostDisplayName: *displayName,
		StateDir:        d.state,
		StagingDir:      staging,
		BabelVersion:    readBuildIdentity().provenance(),
	})
	if err != nil {
		return err
	}

	a.diagf("babel: publishing host %s to %s archive %s\n", Sanitize(hostID), Sanitize(sf.backend), Sanitize(sf.root))
	res, err := pub.Push(ctx)
	if err != nil {
		return fmt.Errorf("archive push: %w", err)
	}

	rows := make([]coverageRow, 0, len(res.Coverage))
	for _, cov := range res.Coverage {
		rows = append(rows, coverageRow{
			Harness:         Sanitize(cov.Harness),
			AdapterSchema:   cov.AdapterSchema,
			Scanned:         cov.Scanned,
			Published:       cov.Published,
			CarriedForward:  cov.CarriedForward,
			Deferred:        cov.Deferred,
			Complete:        cov.Complete,
			DeferredReasons: sanitizeAll(cov.DeferredReasons),
		})
	}
	a.coverageTable(rows)

	out := pushResult{
		HostID:            Sanitize(hostID),
		Generation:        res.Generation,
		Changed:           res.Changed,
		Bootstrap:         res.Bootstrap,
		BootstrapComplete: res.BootstrapComplete,
		Sessions:          res.Sessions,
		Revisions:         res.Revisions,
		Published:         res.Published,
		CarriedForward:    res.CarriedForward,
		Deferred:          res.Deferred,
		CommitKey:         Sanitize(res.CommitKey),
		CommitDigest:      Sanitize(string(res.CommitDigest)),
		Coverage:          rows,
	}
	if *asJSON {
		return a.emitJSON(out)
	}
	state := "unchanged at"
	if res.Changed {
		state = "committed"
	}
	fmt.Fprintf(a.stdout, "%s generation %d for host %s\n", state, res.Generation, out.HostID)
	fmt.Fprintf(a.stdout, "%d %s, %d %s (%d published, %d carried forward, %d deferred)\n",
		res.Sessions, plural(res.Sessions, "session", "sessions"),
		res.Revisions, plural(res.Revisions, "revision", "revisions"),
		res.Published, res.CarriedForward, res.Deferred)
	if out.CommitKey != "" {
		fmt.Fprintf(a.stdout, "commit %s\n", out.CommitKey)
	}
	return nil
}

// coverageTable writes per-adapter scan coverage to stderr: it is
// diagnostic context for the operator, never the command's result.
func (a *app) coverageTable(rows []coverageRow) {
	table := make([][]string, 0, len(rows))
	for _, r := range rows {
		table = append(table, []string{
			r.Harness,
			fmt.Sprint(r.AdapterSchema),
			fmt.Sprint(r.Scanned),
			fmt.Sprint(r.Published),
			fmt.Sprint(r.CarriedForward),
			fmt.Sprint(r.Deferred),
			yesNo(r.Complete, "complete", "incomplete"),
		})
	}
	_ = writeTable(a.stderr, []string{"HARNESS", "SCHEMA", "SCANNED", "PUBLISHED", "CARRIED", "DEFERRED", "COVERAGE"}, table)
	for _, r := range rows {
		for _, reason := range r.DeferredReasons {
			a.diagf("babel: %s deferred: %s\n", r.Harness, reason)
		}
	}
}

// catalogHostRow is one host's contribution in machine-readable output.
type catalogHostRow struct {
	HostID            string   `json:"host_id"`
	DisplayName       string   `json:"display_name,omitempty"`
	Generation        uint64   `json:"generation"`
	CommitDigest      string   `json:"commit_digest,omitempty"`
	CommittedAt       string   `json:"committed_at,omitempty"`
	Sessions          int      `json:"sessions"`
	Revisions         int      `json:"revisions"`
	Bootstrap         bool     `json:"bootstrap"`
	BootstrapComplete bool     `json:"bootstrap_complete"`
	BabelVersion      string   `json:"babel_version,omitempty"`
	HintPresent       bool     `json:"hint_present"`
	HintGeneration    uint64   `json:"hint_generation,omitempty"`
	HintStale         bool     `json:"hint_stale"`
	Skipped           []string `json:"skipped,omitempty"`
	Anomalies         []string `json:"anomalies,omitempty"`
	Error             string   `json:"error,omitempty"`
}

// catalogResult is the machine-readable catalog summary.
type catalogResult struct {
	Hosts     []catalogHostRow `json:"hosts"`
	Sessions  int              `json:"sessions"`
	Revisions int              `json:"revisions"`
}

// archiveCatalog implements `babel archive catalog`.
func (a *app) archiveCatalog(ctx context.Context, args []string) error {
	c := newCmd("archive catalog", archiveCatalogUsage)
	var sf storeFlags
	sf.bind(c.fs)
	var hosts stringList
	c.fs.Var(&hosts, "host", "restrict to one host; repeatable")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	cat, _, err := a.loadCatalog(ctx, c, &sf, hosts)
	if err != nil {
		return err
	}

	out := catalogResult{Hosts: make([]catalogHostRow, 0, len(cat.Hosts()))}
	for _, h := range cat.Hosts() {
		out.Hosts = append(out.Hosts, catalogHostRow{
			HostID:            Sanitize(h.HostID),
			DisplayName:       Sanitize(h.DisplayName),
			Generation:        h.Generation,
			CommitDigest:      Sanitize(string(h.CommitDigest)),
			CommittedAt:       formatTime(h.CommittedAt),
			Sessions:          h.Sessions,
			Revisions:         h.Revisions,
			Bootstrap:         h.Bootstrap,
			BootstrapComplete: h.BootstrapComplete,
			BabelVersion:      Sanitize(h.BabelVersion),
			HintPresent:       h.HintPresent,
			HintGeneration:    h.HintGeneration,
			HintStale:         h.HintStale,
			Skipped:           sanitizeAll(h.Skipped),
			Anomalies:         sanitizeAll(h.Anomalies),
			Error:             Sanitize(h.Err),
		})
	}
	for _, s := range cat.Sessions() {
		out.Sessions++
		out.Revisions += s.RevisionCount()
	}
	a.reportHostDefects(cat.Hosts())

	if *asJSON {
		return a.emitJSON(out)
	}
	rows := make([][]string, 0, len(out.Hosts))
	for _, h := range out.Hosts {
		gen := fmt.Sprintf("g%d", h.Generation)
		if h.Generation == 0 {
			gen = missingValue
		}
		rows = append(rows, []string{
			h.HostID,
			gen,
			fmt.Sprint(h.Sessions),
			fmt.Sprint(h.Revisions),
			yesNo(h.BootstrapComplete, "complete", "incomplete"),
			orMissing(h.DisplayName),
		})
	}
	if err := writeTable(a.stdout, []string{"HOST", "GENERATION", "SESSIONS", "REVISIONS", "BOOTSTRAP", "DISPLAY NAME"}, rows); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "%d %s, %d %s across %d %s\n",
		out.Sessions, plural(out.Sessions, "session", "sessions"),
		out.Revisions, plural(out.Revisions, "revision", "revisions"),
		len(out.Hosts), plural(len(out.Hosts), "host", "hosts"))
	return nil
}

// statusHostRow is one host's health line.
type statusHostRow struct {
	HostID            string   `json:"host_id"`
	Generation        uint64   `json:"generation"`
	CommittedAt       string   `json:"committed_at,omitempty"`
	Bootstrap         bool     `json:"bootstrap"`
	BootstrapComplete bool     `json:"bootstrap_complete"`
	Sessions          int      `json:"sessions"`
	Revisions         int      `json:"revisions"`
	HintPresent       bool     `json:"hint_present"`
	HintGeneration    uint64   `json:"hint_generation,omitempty"`
	HintStale         bool     `json:"hint_stale"`
	JournalPresent    bool     `json:"journal_present"`
	Skipped           []string `json:"skipped,omitempty"`
	Anomalies         []string `json:"anomalies,omitempty"`
	Error             string   `json:"error,omitempty"`
}

// statusResult is the machine-readable archive status.
type statusResult struct {
	StateDir string `json:"state_dir"`
	// Journals lists the hosts this machine holds local publication
	// journals for.
	Journals []string        `json:"journals,omitempty"`
	Hosts    []statusHostRow `json:"hosts"`
}

// archiveStatus implements `babel archive status`. The publication journal
// is only probed for existence: it is private local resumption state and
// never an authority, so its contents are not interpreted here and its
// presence is reported as presence, not as a claim about outstanding work
// (SPEC.md §6.1).
func (a *app) archiveStatus(ctx context.Context, args []string) error {
	c := newCmd("archive status", archiveStatusUsage)
	var sf storeFlags
	sf.bind(c.fs)
	var hosts stringList
	c.fs.Var(&hosts, "host", "restrict to one host; repeatable")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	cat, _, err := a.loadCatalog(ctx, c, &sf, hosts)
	if err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}
	journals, err := journalHosts(d.state)
	if err != nil {
		return err
	}

	out := statusResult{StateDir: Sanitize(d.state), Journals: sanitizeAll(journals)}
	hasJournal := make(map[string]bool, len(journals))
	for _, h := range journals {
		hasJournal[h] = true
	}
	for _, h := range cat.Hosts() {
		out.Hosts = append(out.Hosts, statusHostRow{
			HostID:            Sanitize(h.HostID),
			Generation:        h.Generation,
			CommittedAt:       formatTime(h.CommittedAt),
			Bootstrap:         h.Bootstrap,
			BootstrapComplete: h.BootstrapComplete,
			Sessions:          h.Sessions,
			Revisions:         h.Revisions,
			HintPresent:       h.HintPresent,
			HintGeneration:    h.HintGeneration,
			HintStale:         h.HintStale,
			JournalPresent:    hasJournal[h.HostID],
			Skipped:           sanitizeAll(h.Skipped),
			Anomalies:         sanitizeAll(h.Anomalies),
			Error:             Sanitize(h.Err),
		})
	}
	a.reportHostDefects(cat.Hosts())

	if *asJSON {
		return a.emitJSON(out)
	}
	rows := make([][]string, 0, len(out.Hosts))
	for _, h := range out.Hosts {
		gen := fmt.Sprintf("g%d", h.Generation)
		if h.Generation == 0 {
			gen = missingValue
		}
		rows = append(rows, []string{
			h.HostID,
			gen,
			yesNo(h.BootstrapComplete, "complete", "incomplete"),
			hintState(h),
			yesNo(h.JournalPresent, "present", missingValue),
		})
	}
	if err := writeTable(a.stdout, []string{"HOST", "HEAD", "BOOTSTRAP", "HINT", "JOURNAL"}, rows); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "state %s\n", out.StateDir)
	return nil
}

// hintState renders how a host's non-authoritative latest pointer compares
// with the generation selection actually chose.
func hintState(h statusHostRow) string {
	switch {
	case !h.HintPresent:
		return "absent"
	case h.HintStale:
		return fmt.Sprintf("stale g%d", h.HintGeneration)
	default:
		return fmt.Sprintf("g%d", h.HintGeneration)
	}
}

// journalHosts lists the hosts this machine holds a publication journal
// for. The journal is a private accelerator whose contents are not part of
// any contract, so only its existence is reported (SPEC.md §6.1).
func journalHosts(stateDir string) ([]string, error) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read state directory %s: %w", stateDir, err)
	}
	const prefix, suffix = "publish-journal-", ".json"
	var hosts []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
			continue
		}
		host := name[len(prefix) : len(name)-len(suffix)]
		if archive.ValidName(host) {
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)
	return hosts, nil
}

// verifyHostRow is one host's verification outcome.
type verifyHostRow struct {
	HostID      string   `json:"host_id"`
	Records     int      `json:"records"`
	Generations int      `json:"generations"`
	Revisions   int      `json:"revisions"`
	Objects     int      `json:"objects"`
	Warnings    []string `json:"warnings,omitempty"`
	Errors      []string `json:"errors,omitempty"`
}

// verifyResult is the machine-readable verification report.
type verifyResult struct {
	Deep  bool            `json:"deep"`
	OK    bool            `json:"ok"`
	Hosts []verifyHostRow `json:"hosts"`
}

// archiveVerify implements `babel archive verify`.
func (a *app) archiveVerify(ctx context.Context, args []string) error {
	c := newCmd("archive verify", archiveVerifyUsage)
	var sf storeFlags
	sf.bind(c.fs)
	var hosts stringList
	c.fs.Var(&hosts, "host", "restrict to one host; repeatable")
	deep := c.fs.Bool("deep", false, "also verify every referenced object's bytes")
	asJSON := c.fs.Bool("json", false, "emit the result as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	st, err := sf.open(c)
	if err != nil {
		return err
	}
	selected, err := hostFilter(c, hosts)
	if err != nil {
		return err
	}
	tier := "default"
	if *deep {
		tier = "deep"
	}
	a.diagf("babel: verifying %s archive %s (%s tier)\n", Sanitize(sf.backend), Sanitize(sf.root), tier)
	rep, err := catalog.Verify(ctx, st, selected, *deep)
	if err != nil {
		return err
	}

	out := verifyResult{Deep: rep.Deep, OK: rep.OK(), Hosts: make([]verifyHostRow, 0, len(rep.Hosts))}
	for _, h := range rep.Hosts {
		row := verifyHostRow{
			HostID:      Sanitize(h.HostID),
			Records:     h.Records,
			Generations: h.Generations,
			Revisions:   h.Revisions,
			Objects:     h.Objects,
			Warnings:    sanitizeAll(h.Warnings),
			Errors:      sanitizeAll(h.Errors),
		}
		out.Hosts = append(out.Hosts, row)
		for _, w := range row.Warnings {
			a.diagf("babel: warning: %s: %s\n", row.HostID, w)
		}
		for _, e := range row.Errors {
			a.diagf("babel: error: %s: %s\n", row.HostID, e)
		}
	}

	if *asJSON {
		if err := a.emitJSON(out); err != nil {
			return err
		}
	} else {
		rows := make([][]string, 0, len(out.Hosts))
		for _, h := range out.Hosts {
			rows = append(rows, []string{
				h.HostID,
				fmt.Sprint(h.Records),
				fmt.Sprint(h.Generations),
				fmt.Sprint(h.Revisions),
				fmt.Sprint(h.Objects),
				fmt.Sprint(len(h.Warnings)),
				fmt.Sprint(len(h.Errors)),
			})
		}
		if err := writeTable(a.stdout, []string{"HOST", "RECORDS", "GENERATIONS", "REVISIONS", "OBJECTS", "WARNINGS", "ERRORS"}, rows); err != nil {
			return err
		}
		fmt.Fprintf(a.stdout, "verify %s (%s tier)\n", yesNo(out.OK, "ok", "failed"), tier)
	}
	if !out.OK {
		return errors.New("archive verify found errors in committed state")
	}
	return nil
}

// loadCatalog opens the selected store and loads the merged committed view.
// It is the shared read path of every read-only command.
func (a *app) loadCatalog(ctx context.Context, c *cmd, sf *storeFlags, hosts stringList) (*catalog.Catalog, objectstore.Store, error) {
	st, err := sf.open(c)
	if err != nil {
		return nil, nil, err
	}
	selected, err := hostFilter(c, hosts)
	if err != nil {
		return nil, nil, err
	}
	cat, err := catalog.Load(ctx, st, selected)
	if err != nil {
		return nil, nil, err
	}
	return cat, st, nil
}

// reportHostDefects sends every host-level defect to stderr. A damaged host
// never hides the rest of the archive, so these are diagnostics rather than
// failures (SPEC.md §11).
func (a *app) reportHostDefects(hosts []catalog.HostInfo) {
	for _, h := range hosts {
		id := Sanitize(h.HostID)
		if h.Err != "" {
			a.diagf("babel: host %s contributes nothing: %s\n", id, Sanitize(h.Err))
		}
		for _, s := range h.Skipped {
			a.diagf("babel: host %s skipped generation: %s\n", id, Sanitize(s))
		}
		for _, an := range h.Anomalies {
			a.diagf("babel: host %s anomaly: %s\n", id, Sanitize(an))
		}
		if h.HintStale {
			a.diagf("babel: host %s latest hint is stale (hint g%d, head g%d)\n", id, h.HintGeneration, h.Generation)
		}
	}
}
