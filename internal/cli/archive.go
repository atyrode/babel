package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/restic"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

const archiveUsage = `Usage: babel archive <command> [flags]

Commands:
  push     back up this host's source roots into the restic repository
  status   report snapshots per host
  verify   check repository integrity

Repository selection:
  --repo REPOSITORY           else $BABEL_RESTIC_REPO
  --password-file FILE        else $BABEL_RESTIC_PASSWORD_FILE

Run "babel archive <command> -h" for a command's flags.
`

const archivePushUsage = `Usage: babel archive push --repo REPOSITORY --password-file FILE [flags]

Backs up every source adapter's default root that exists on this host into
one restic snapshot, tagged "babel" and attributed to this host's identity.
Sources are read in place: no staging copy is made, and the snapshot is
crash-consistent per file rather than transactional across files, so the
next push supersedes any torn write.

Flags:
  --repo REPOSITORY           restic repository (default $BABEL_RESTIC_REPO)
  --password-file FILE        password file (default $BABEL_RESTIC_PASSWORD_FILE)
  --restic-binary PATH        restic executable (default "restic" from $PATH)
  --host ID                   host identity (default $BABEL_HOST_ID, else hostname)
  --json                      emit the summary as JSON on stdout

Exits 1 when restic could back up only part of the source tree; the
summary of what was committed is still reported.

The reported catalog state is "local" with no shared catalog, "committed"
once this snapshot and its session rows are visible fleet-wide, or
"uncatalogued" when the snapshot is durable but the catalog holds no row
for it - an outage, or another instance already publishing for this host.
Both of those exit 0: the archive is intact, and the next push or a
reconciliation records the snapshot. "babel archive status" reports what
is still uncatalogued between pushes.
`

const archiveStatusUsage = `Usage: babel archive status --repo REPOSITORY --password-file FILE [flags]

Reports the repository's snapshots grouped by host: how many, when the
latest was taken, its id, and the tags observed. Read-only.

In shared mode it also reports how far the shared catalog is behind the
repository, derived by comparing the two rather than read from a local
journal. The two counts mean different things:

  uncatalogued      snapshots the repository holds that the catalog has no
                    row for at all, which is what an outage leaves behind.
                    The next "babel archive push" records them.

  catalog-pending   snapshots the catalog has a row for, with real counts
                    from restic, but no record of which sessions they held.
                    That record can only be written by the owning host at
                    push time, and it is not derivable from the snapshot
                    listing, so no command here resolves it: pushing again
                    publishes the next snapshot rather than completing this
                    one, and the count does not fall. Nothing is wrong with
                    the archive - the snapshots remain durable and
                    restorable, and only the catalog's session detail for
                    them is missing. Recovering it would mean restoring the
                    snapshot and rescanning it, which Babel does not
                    implement yet.

An unreachable catalog reports both counts as unknown rather than zero.

In shared mode it then prints a second table, "catalog by host", holding
what the catalog itself recorded for every host that has published - so an
instance that archived none of it can still see the fleet without
downloading a transcript. It is deliberately separate from the first table:
the first is what the repository holds, this is what the catalog was told,
and the difference between them is the subject of the counts above.

  SNAPSHOTS         catalog rows for that host, catalog-pending ones
                    included: those snapshots exist in the repository.

  SESSIONS          distinct sessions the host has ever published. A host
                    whose catalog rows were rebuilt from the repository
                    listing reports 0 until it pushes again, because session
                    detail is not in that listing.

  PENDING           that host's share of the catalog-pending count above.

  NEWEST ORDER      the host's publication sequence high-water mark, which
                    it assigns itself so ordering does not depend on clocks
                    agreeing between machines.

  NEWEST SNAPSHOT   when restic took that newest-ordered snapshot, not when
                    the catalog learned of it. After an outage a reconciled
                    snapshot takes the highest order while carrying an older
                    time, so this can predate another row's timestamp.

Sessions are counted, never listed: the catalog identifies them by an opaque
digest, and resolving one back to a selector needs the repository or a local
index.

Flags:
  --repo REPOSITORY           restic repository (default $BABEL_RESTIC_REPO)
  --password-file FILE        password file (default $BABEL_RESTIC_PASSWORD_FILE)
  --restic-binary PATH        restic executable (default "restic" from $PATH)
  --json                      emit the report as JSON on stdout
`

const archiveVerifyUsage = `Usage: babel archive verify --repo REPOSITORY --password-file FILE [flags]

Checks repository structure with "restic check". With --deep, pack data is
re-read and re-hashed, which costs a full download of the repository.
Read-only.

Flags:
  --repo REPOSITORY           restic repository (default $BABEL_RESTIC_REPO)
  --password-file FILE        password file (default $BABEL_RESTIC_PASSWORD_FILE)
  --restic-binary PATH        restic executable (default "restic" from $PATH)
  --deep                      also read and re-hash all pack data
  --json                      emit the outcome as JSON on stdout

Exits 1 on any integrity failure; restic's detail goes to stderr.
`

// archive routes `babel archive <verb>`.
func (a *app) archive(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return &usageError{msg: "archive requires a subcommand", usage: archiveUsage}
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(a.stdout, archiveUsage)
		return nil
	case "push":
		return a.archivePush(ctx, args[1:])
	case "status":
		return a.archiveStatus(ctx, args[1:])
	case "verify":
		return a.archiveVerify(ctx, args[1:])
	default:
		return &usageError{msg: fmt.Sprintf("unknown archive subcommand %q", args[0]), usage: archiveUsage}
	}
}

// pushResult is the machine-readable outcome of one backup. Byte and file
// counts come straight from restic's own summary: Babel reports what the
// repository recorded rather than what it hoped to send.
type pushResult struct {
	Host            string   `json:"host"`
	Tags            []string `json:"tags"`
	Roots           []string `json:"roots"`
	SnapshotID      string   `json:"snapshot_id"`
	FilesNew        int      `json:"files_new"`
	FilesChanged    int      `json:"files_changed"`
	FilesUnmodified int      `json:"files_unmodified"`
	DataAdded       int64    `json:"data_added"`
	FilesProcessed  int      `json:"total_files_processed"`
	BytesProcessed  int64    `json:"total_bytes_processed"`
	// Incomplete records that restic reported a partial backup: the
	// snapshot exists and is usable, but some source files were not read.
	Incomplete bool `json:"incomplete"`
	// Catalog is what happened to the shared catalog: "local" when there is
	// none, "committed" when this snapshot and its session rows are visible
	// fleet-wide, or "uncatalogued" when the snapshot is durable and the
	// catalog holds no row for it. "uncatalogued" is `archive status`'s word
	// for the same condition, and is deliberately not "catalog-pending",
	// which names a row that exists without session detail.
	Catalog string `json:"catalog"`
	// SessionsPublished counts the session identity rows this push recorded.
	SessionsPublished int `json:"sessions_published"`
}

// archivePush implements `babel archive push`.
func (a *app) archivePush(ctx context.Context, args []string) error {
	c := newCmd("archive push", archivePushUsage)
	var rf repoFlags
	rf.bind(c.fs)
	asJSON := c.fs.Bool("json", false, "emit the summary as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	host, err := rf.hostID(c)
	if err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}

	roots := existingRoots()
	res := pushResult{Host: Sanitize(host), Tags: []string{babelTag}, Roots: sanitizeAll(roots)}
	if len(roots) == 0 {
		// Nothing to back up is not a failure: a machine may simply run no
		// harness yet. It must not look like a successful backup either,
		// so the empty summary is reported alongside an explicit warning.
		a.diagf("warning: no source root exists on this host; nothing to back up\n")
		return a.reportPush(res, *asJSON)
	}

	repo, err := rf.open(c, d, &sanitizingWriter{w: a.stderr, prefix: "restic: "})
	if err != nil {
		return err
	}
	if err := repo.EnsureInit(ctx); err != nil {
		return fmt.Errorf("initialize repository: %w", err)
	}
	a.diagf("backing up %d %s as host %s\n", len(roots), plural(len(roots), "root", "roots"), Sanitize(host))

	summary, backupErr := repo.Backup(ctx, roots, host, []string{babelTag})
	if summary == nil {
		if backupErr == nil {
			return errors.New("back up: restic reported no summary")
		}
		return fmt.Errorf("back up: %w", backupErr)
	}
	res.SnapshotID = Sanitize(summary.SnapshotID)
	res.FilesNew = summary.FilesNew
	res.FilesChanged = summary.FilesChanged
	res.FilesUnmodified = summary.FilesUnmodified
	res.DataAdded = summary.DataAdded
	res.FilesProcessed = summary.TotalFilesProcessed
	res.BytesProcessed = summary.TotalBytesProcessed
	res.Incomplete = backupErr != nil

	// The archive is already durable. Cataloguing is a separate step whose
	// failure must not make a successful backup look failed, so an outage
	// reports catalog-pending and a real misconfiguration is what fails.
	if backupErr == nil {
		state, published, pubErr := a.publishToCatalog(ctx, d, host, repo, summary)
		res.Catalog, res.SessionsPublished = state, published
		if pubErr != nil {
			if reportErr := a.reportPush(res, *asJSON); reportErr != nil {
				return reportErr
			}
			return pubErr
		}
	}

	if err := a.reportPush(res, *asJSON); err != nil {
		return err
	}
	if backupErr != nil {
		// A partial backup is a failure the operator must see, but the
		// snapshot it produced is real and already reported.
		return fmt.Errorf("back up: %w", backupErr)
	}
	return nil
}

// reportPush writes one push summary to stdout.
func (a *app) reportPush(res pushResult, asJSON bool) error {
	if asJSON {
		return a.emitJSON(res)
	}
	if res.SnapshotID == "" {
		fmt.Fprintf(a.stdout, "no snapshot created\n")
		return nil
	}
	rows := [][2]string{
		{"snapshot", res.SnapshotID},
		{"host", res.Host},
		{"files new", fmt.Sprint(res.FilesNew)},
		{"files changed", fmt.Sprint(res.FilesChanged)},
		{"files unmodified", fmt.Sprint(res.FilesUnmodified)},
		{"files processed", fmt.Sprint(res.FilesProcessed)},
		{"bytes processed", fmt.Sprint(res.BytesProcessed)},
		{"data added", fmt.Sprint(res.DataAdded)},
		{"complete", yesNo(!res.Incomplete, "yes", "no")},
	}
	if res.Catalog != "" && res.Catalog != catalogLocal {
		rows = append(rows,
			[2]string{"catalog", res.Catalog},
			[2]string{"sessions published", fmt.Sprint(res.SessionsPublished)},
		)
	}
	for _, root := range res.Roots {
		rows = append(rows, [2]string{"root", root})
	}
	return writeDetail(a.stdout, rows)
}

// existingRoots is the set of adapter backup roots that exist on this
// host, deduplicated and ordered. A root an adapter proposes but that this
// machine does not have is silently absent rather than a failure: harness
// coverage is a property of the machine, not an error condition.
func existingRoots() []string {
	var found []string
	for _, ad := range adapters() {
		for _, root := range ad.BackupRoots() {
			if root == "" {
				continue
			}
			if info, err := os.Stat(root); err != nil || !info.IsDir() {
				continue
			}
			found = append(found, root)
		}
	}
	return sortedUnique(found)
}

// statusHostRow is one host's snapshot state in the repository.
type statusHostRow struct {
	Host          string   `json:"host"`
	Snapshots     int      `json:"snapshots"`
	LatestTime    string   `json:"latest_time"`
	LatestID      string   `json:"latest_id"`
	LatestShortID string   `json:"latest_short_id"`
	Tags          []string `json:"tags,omitempty"`
}

// catalogHostRow is what the shared catalog recorded for one host: its own
// count of snapshots and sessions, and where its publication sequence stands.
//
// It is reported alongside statusHostRow rather than merged into it because the
// two are different authorities. The repository is archive truth; the catalog is
// derived state that an outage can leave behind and that a rebuild can reset.
// Merging them into one row would let a reader take a catalog number for a
// repository fact, and the difference between the two is the entire subject of
// this command.
type catalogHostRow struct {
	Host      string `json:"host"`
	Snapshots int    `json:"snapshots"`
	Sessions  int    `json:"sessions"`
	Pending   int    `json:"pending"`
	// NewestOrder is the host's publication sequence high-water mark, assigned
	// by the host itself so it does not depend on clock agreement between
	// machines.
	NewestOrder int64 `json:"newest_order"`
	// NewestSnapshot is restic's recorded time for that newest-ordered
	// snapshot, not when the catalog learned of it: after an outage a
	// reconciled snapshot takes the highest order while carrying an older
	// time, and reporting the adoption time would claim the archive is fresher
	// than it is.
	NewestSnapshot string `json:"newest_snapshot"`
}

// catalogStatus answers "is anything archived but not catalogued", derived from
// the repository listing and the catalog rather than from a local journal.
//
// The counts are pointers because an unreachable catalog does not make them
// zero, it makes them unknown, and reporting 0 uncatalogued snapshots is a
// factual claim this command cannot make without reading the catalog. Absent
// means unknown, exactly as elsewhere in Babel.
type catalogStatus struct {
	// Reachable is false during an outage. Status stays useful offline, so an
	// unreachable catalog is reported rather than fatal.
	Reachable bool `json:"reachable"`
	// Uncatalogued counts snapshots the repository holds that the catalog has
	// never seen. A push catalogues them.
	Uncatalogued *int `json:"uncatalogued,omitempty"`
	// Pending counts snapshots the catalog holds without session rows, which
	// is the state reconciliation leaves them in.
	Pending *int `json:"pending,omitempty"`
	// Hosts is what the catalog holds per host, which is how a second instance
	// browses a fleet it did not archive. It stays absent when the catalog
	// could not be read: an empty array would claim the catalog holds nothing,
	// and this command would not have looked.
	Hosts []catalogHostRow `json:"hosts,omitempty"`
}

// statusResult is the machine-readable archive status.
type statusResult struct {
	Repository string          `json:"repository"`
	Snapshots  int             `json:"snapshots"`
	Hosts      []statusHostRow `json:"hosts"`
	// Catalog is absent in local mode: there is nothing to be behind.
	Catalog *catalogStatus `json:"catalog,omitempty"`
}

// archiveStatus implements `babel archive status`.
func (a *app) archiveStatus(ctx context.Context, args []string) error {
	c := newCmd("archive status", archiveStatusUsage)
	var rf repoFlags
	rf.bind(c.fs)
	asJSON := c.fs.Bool("json", false, "emit the report as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}
	repo, err := rf.open(c, d, nil)
	if err != nil {
		return err
	}
	snapshots, err := repo.Snapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}

	res := statusResult{
		Repository: Sanitize(rf.repository),
		Snapshots:  len(snapshots),
		Hosts:      groupByHost(snapshots),
	}
	res.Catalog = a.catalogLag(ctx, snapshots)
	if len(snapshots) == 0 {
		a.diagf("warning: repository holds no snapshots yet\n")
	}
	if *asJSON {
		return a.emitJSON(res)
	}
	if len(res.Hosts) == 0 {
		fmt.Fprint(a.stdout, "no snapshots\n")
		return nil
	}
	rows := make([][]string, 0, len(res.Hosts))
	for _, h := range res.Hosts {
		rows = append(rows, []string{
			h.Host,
			fmt.Sprint(h.Snapshots),
			orMissing(h.LatestTime),
			orMissing(h.LatestShortID),
			orMissing(joinCell(h.Tags)),
		})
	}
	if err := writeTable(a.stdout, []string{"HOST", "SNAPSHOTS", "LATEST", "LATEST ID", "TAGS"}, rows); err != nil {
		return err
	}
	if res.Catalog != nil {
		return a.printCatalogStatus(res.Catalog)
	}
	return nil
}

// printCatalogStatus renders the catalog half of `archive status` below the
// repository table.
//
// The per-host rows go in a second table rather than extra columns on the
// first, because the two tables are two different authorities: the repository
// holds the archive, the catalog holds derived state an outage can strand and a
// rebuild can reset. Keeping them visibly separate is what lets an operator
// tell "the snapshot exists" from "the catalog knows about it" - the very
// distinction the counts above measure - and merged columns would invite
// reading one as the other.
func (a *app) printCatalogStatus(c *catalogStatus) error {
	if err := writeDetail(a.stdout, [][2]string{
		{"catalog reachable", yesNo(c.Reachable, "yes", "no")},
		{"uncatalogued snapshots", countOrUnknown(c.Uncatalogued)},
		{"catalog-pending snapshots", countOrUnknown(c.Pending)},
	}); err != nil {
		return err
	}
	// No rows means either an unreachable catalog or one nothing has published
	// into. Neither warrants an empty table claiming to show the fleet.
	if len(c.Hosts) == 0 {
		return nil
	}
	fmt.Fprint(a.stdout, "\ncatalog by host:\n")
	rows := make([][]string, 0, len(c.Hosts))
	for _, h := range c.Hosts {
		rows = append(rows, []string{
			h.Host,
			fmt.Sprint(h.Snapshots),
			fmt.Sprint(h.Sessions),
			fmt.Sprint(h.Pending),
			fmt.Sprint(h.NewestOrder),
			orMissing(h.NewestSnapshot),
		})
	}
	return writeTable(a.stdout,
		[]string{"HOST", "SNAPSHOTS", "SESSIONS", "PENDING", "NEWEST ORDER", "NEWEST SNAPSHOT"},
		rows)
}

// catalogLag reports what the shared catalog holds and how far behind the
// repository it is.
//
// The lag is the answer to "did an outage leave something uncatalogued", and it
// is computed rather than remembered: the repository is authoritative for what
// exists and the catalog for what it has recorded, so no local journal can be
// more correct than comparing them. The per-host rows are the other half - what
// the catalog itself says the fleet archived, which is how an instance that
// archived none of it can still see the fleet. It returns nil in local mode,
// where there is no catalog and nothing to be behind.
func (a *app) catalogLag(ctx context.Context, snapshots []restic.Snapshot) *catalogStatus {
	cfg, found, err := config.Load()
	if err != nil || !found || storageMode(cfg) != config.ModeShared || cfg.Catalog == nil {
		return nil
	}

	db, err := sharedcatalog.Open(ctx, cfg.Catalog.DSN())
	if err != nil {
		// Status must stay useful during an outage, so this is reported rather
		// than fatal. An error that is not an outage is still only a report:
		// nothing here writes, and the repository half of the answer is real.
		a.diagf("warning: could not reach the shared catalog: %s\n", Sanitize(err.Error()))
		return &catalogStatus{}
	}
	defer db.Close()

	states, err := sharedcatalog.SnapshotStates(ctx, db)
	if err != nil {
		a.diagf("warning: could not read catalog snapshot states: %s\n", Sanitize(err.Error()))
		return &catalogStatus{}
	}

	var uncatalogued, pending int
	for _, s := range snapshots {
		if _, known := states[s.ID]; !known {
			uncatalogued++
		}
	}
	for _, state := range states {
		if state == sharedcatalog.CommitPending {
			pending++
		}
	}
	// Two different conditions with two different meanings, deliberately not
	// summed. An uncatalogued snapshot has no catalog row and a push records it.
	// A catalog-pending row exists with real counts from restic but no record of
	// which sessions the snapshot held, and only its owning host could have
	// written that, at push time. No shipped command resolves it - pushing again
	// publishes the next snapshot rather than completing this one - so the count
	// does not fall, and saying so is kinder than leaving an operator looking
	// for the command that clears it. A restore-and-rescan could complete it
	// (SPEC.md 12, Phase C), which is why the note says "yet" rather than
	// claiming the detail is unrecoverable in principle.
	if uncatalogued > 0 {
		a.diagf("note: %d %s archived but not catalogued; `babel archive push` records them\n",
			uncatalogued, plural(uncatalogued, "snapshot is", "snapshots are"))
	}
	if pending > 0 {
		a.diagf("note: %d %s recorded without session detail, which only its owning host could write at push time; the %s durable and restorable, and no command resolves this yet, so the count does not fall\n",
			pending, plural(pending, "snapshot is", "snapshots are"),
			plural(pending, "snapshot stays", "snapshots stay"))
	}
	return &catalogStatus{
		Reachable:    true,
		Uncatalogued: &uncatalogued,
		Pending:      &pending,
		Hosts:        a.catalogHosts(ctx, db),
	}
}

// catalogHosts reads the catalog's own per-host view, or nil when it cannot.
//
// A failed browse leaves the rows absent while the catalog stays reachable:
// PostgreSQL answered the state query, so the lag counts above are real, and
// claiming the fleet is empty because one query failed would be a different
// lie. Host ids come from the database, which any authorized instance writes,
// so they are sanitized like any other foreign value.
func (a *app) catalogHosts(ctx context.Context, db *sql.DB) []catalogHostRow {
	hosts, err := sharedcatalog.HostCatalog(ctx, db)
	if err != nil {
		a.diagf("warning: could not read the catalog's host rows: %s\n", Sanitize(err.Error()))
		return nil
	}
	rows := make([]catalogHostRow, 0, len(hosts))
	for _, h := range hosts {
		rows = append(rows, catalogHostRow{
			Host:           Sanitize(h.HostID),
			Snapshots:      h.Snapshots,
			Sessions:       h.Sessions,
			Pending:        h.Pending,
			NewestOrder:    h.NewestOrder,
			NewestSnapshot: formatTime(h.NewestSnapshotTime),
		})
	}
	return rows
}

// countOrUnknown renders a derived count, or says it is unknown. An unreachable
// catalog leaves the counts absent rather than zero, and printing 0 would state
// something this command did not observe.
func countOrUnknown(n *int) string {
	if n == nil {
		return "unknown"
	}
	return fmt.Sprint(*n)
}

// groupByHost folds the repository's snapshots into one row per host. Host
// names and tags come from the repository, which any machine sharing it
// can write, so both are rendered through Sanitize.
func groupByHost(snapshots []restic.Snapshot) []statusHostRow {
	if len(snapshots) == 0 {
		return nil
	}
	type acc struct {
		count  int
		latest restic.Snapshot
		tags   []string
	}
	byHost := make(map[string]*acc)
	for _, s := range snapshots {
		a, ok := byHost[s.Host]
		if !ok {
			a = &acc{}
			byHost[s.Host] = a
		}
		a.count++
		a.tags = append(a.tags, s.Tags...)
		if a.latest.ID == "" || s.Time.After(a.latest.Time) {
			a.latest = s
		}
	}
	hosts := make([]string, 0, len(byHost))
	for host := range byHost {
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)

	rows := make([]statusHostRow, 0, len(hosts))
	for _, host := range hosts {
		a := byHost[host]
		rows = append(rows, statusHostRow{
			Host:          Sanitize(host),
			Snapshots:     a.count,
			LatestTime:    formatTime(a.latest.Time),
			LatestID:      Sanitize(a.latest.ID),
			LatestShortID: Sanitize(a.latest.ShortID),
			Tags:          sanitizeAll(sortedUnique(a.tags)),
		})
	}
	return rows
}

// verifyResult is the machine-readable verification outcome.
type verifyResult struct {
	Repository string `json:"repository"`
	Deep       bool   `json:"deep"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
}

// archiveVerify implements `babel archive verify`.
func (a *app) archiveVerify(ctx context.Context, args []string) error {
	c := newCmd("archive verify", archiveVerifyUsage)
	var rf repoFlags
	rf.bind(c.fs)
	deep := c.fs.Bool("deep", false, "also read and re-hash all pack data")
	asJSON := c.fs.Bool("json", false, "emit the outcome as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	d, err := babelDirs()
	if err != nil {
		return err
	}
	repo, err := rf.open(c, d, nil)
	if err != nil {
		return err
	}

	checkErr := repo.Check(ctx, *deep)
	res := verifyResult{Repository: Sanitize(rf.repository), Deep: *deep, OK: checkErr == nil}
	if checkErr != nil {
		res.Error = Sanitize(checkErr.Error())
	}
	// The outcome is a result even when it is bad news, so a --json caller
	// always gets a document; the failure detail is repeated on stderr and
	// the exit code carries the verdict.
	if *asJSON {
		if err := a.emitJSON(res); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(a.stdout, "%s (%s)\n", yesNo(res.OK, "ok", "FAILED"), yesNo(*deep, "deep", "structure"))
	}
	if checkErr != nil {
		return fmt.Errorf("verify repository: %w", checkErr)
	}
	return nil
}

// joinCell renders a small already-sanitized list inside one table cell.
// Sanitize escapes separators, so the list is joined after rendering,
// never before: the comma is layout, not part of any value.
func joinCell(values []string) string {
	return strings.Join(values, ",")
}
