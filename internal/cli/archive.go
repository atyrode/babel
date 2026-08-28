package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/restic"
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
`

const archiveStatusUsage = `Usage: babel archive status --repo REPOSITORY --password-file FILE [flags]

Reports the repository's snapshots grouped by host: how many, when the
latest was taken, its id, and the tags observed. Read-only.

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

// statusResult is the machine-readable archive status.
type statusResult struct {
	Repository string          `json:"repository"`
	Snapshots  int             `json:"snapshots"`
	Hosts      []statusHostRow `json:"hosts"`
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
	return writeTable(a.stdout, []string{"HOST", "SNAPSHOTS", "LATEST", "LATEST ID", "TAGS"}, rows)
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
