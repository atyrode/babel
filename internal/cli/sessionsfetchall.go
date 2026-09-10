package cli

// Bulk materialization of the fleet corpus. `sessions fetch` recovers one
// session, which is the right shape for an operator chasing a selector and the
// wrong one for keeping a machine hot: a corpus of a few hundred sessions
// fetched one invocation at a time is dominated by restic's own startup, not by
// the bytes, and any one of those invocations failing loses the operator's place
// in the batch.

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/atyrode/babel/internal/restic"
)

// defaultFetchConcurrency is how many sessions a bulk fetch restores at once.
//
// The cost being amortized is per-invocation: each session is one `restic
// restore` process that opens the repository, loads the index, and then reads a
// few hundred kilobytes. Eight in flight keeps that startup off the critical
// path without turning a shared repository into the bottleneck for whatever
// else the machine is doing.
const defaultFetchConcurrency = 8

// maxFetchConcurrency bounds --concurrency. Past this the repository is being
// hammered rather than used, and restic's own lock and index traffic starts
// costing more than the parallelism returns.
const maxFetchConcurrency = 32

const sessionsFetchAllUsage = `Usage: babel sessions fetch-all (--host ID | --all-hosts) [flags]

Restores every session one machine archived, or every session the fleet
archived, into Babel's own fetched-session area. It is the corpus-shaped
counterpart of "sessions fetch": the catalog is fleet-wide, so analysis of a
session another machine archived is a fetch away rather than a refusal
(SPEC.md §6.3).

Sessions are identified from the snapshot's file listing and restored
concurrently, because the cost of a small session is restic's startup rather
than its bytes. Fetching is idempotent per session, so an interrupted batch
is resumed by running the same command again: an already materialized session
is reported and left untouched, and nothing is downloaded twice.

One session's failure loses that session and not the batch. A transient
repository lock, a closure the snapshot does not hold, or a path this
filesystem rejects is recorded against the selector it happened to, the
remaining sessions still run, and the command exits nonzero so a partial
corpus never reads as a complete one.

Every fetched session records which machine's archive it came from, taken
from the snapshot restic restored. "sessions list --fetched" and
"sessions inspect --fetched" then discover the corpus under those identities
instead of attributing another machine's session to this one.

Flags:
  --host ID            fetch every session this host archived
  --all-hosts          fetch every session every host archived
  --snapshot ID        snapshot id, short id, or prefix (default the host's
                       newest); requires --host
  --concurrency N      restore N sessions at once (default 8, at most 32)
  --repo REPOSITORY    restic repository (default $BABEL_RESTIC_REPO)
  --password-file FILE password file (default $BABEL_RESTIC_PASSWORD_FILE)
  --restic-binary PATH restic executable (default "restic" from $PATH)
  --json               emit the outcome as JSON on stdout

--host names one machine and --all-hosts names every machine, and a snapshot
belongs to exactly one host, so those combinations are rejected by name
rather than resolved by precedence.
`

// sessionFailure is one session a batch could not materialize, and why. The
// selector is named rather than counted because the remedy — retry, or a
// snapshot that predates the file — is per session.
type sessionFailure struct {
	Selector string `json:"selector"`
	Error    string `json:"error"`
}

// hostFetch is what one host's bulk fetch did.
//
// Sessions is what the snapshot held; Fetched, Resumed and the failures
// account for all of it unless the batch was interrupted, which is what makes
// "everything the snapshot held is either here, was already here, or is
// reported as lost" a checkable claim rather than a summary.
type hostFetch struct {
	Host            string `json:"host"`
	SnapshotID      string `json:"snapshot_id,omitempty"`
	SnapshotShortID string `json:"snapshot_short_id,omitempty"`
	SnapshotTime    string `json:"snapshot_time,omitempty"`
	Sessions        int    `json:"sessions"`
	Fetched         int    `json:"fetched"`
	Resumed         int    `json:"resumed"`
	Files           int    `json:"files"`
	Bytes           int64  `json:"bytes"`
	// Error is why this host contributed nothing. It exists only for a
	// fleet-wide fetch: one machine's unreadable snapshot must not cost the
	// operator the other machines, while a fetch that named one host has
	// nothing left to do and fails.
	Error    string           `json:"error,omitempty"`
	Failures []sessionFailure `json:"failures,omitempty"`
}

// fetchAllResult is the machine-readable outcome of one bulk fetch.
type fetchAllResult struct {
	Hosts    []hostFetch `json:"hosts"`
	Sessions int         `json:"sessions"`
	Fetched  int         `json:"fetched"`
	Resumed  int         `json:"resumed"`
	Failed   int         `json:"failed"`
	Files    int         `json:"files"`
	Bytes    int64       `json:"bytes"`
	// Target is the area the corpus was materialized into, which is what
	// "sessions list --fetched" then discovers and "sessions prune" removes.
	Target string `json:"target"`
}

// sessionsFetchAll implements `babel sessions fetch-all`.
func (a *app) sessionsFetchAll(ctx context.Context, args []string) error {
	c := newCmd("sessions fetch-all", sessionsFetchAllUsage)
	var rf repoFlags
	rf.bind(c.fs)
	allHosts := c.fs.Bool("all-hosts", false, "fetch every session every host archived")
	snapshot := c.fs.String("snapshot", "", "with --host: snapshot id, short id, or prefix; default that host's newest")
	workers := c.fs.Int("concurrency", defaultFetchConcurrency, "restore this many sessions at once")
	asJSON := c.fs.Bool("json", false, "emit the outcome as JSON")
	if err := c.parse(a, args); err != nil {
		return err
	}
	if err := c.noArgs(); err != nil {
		return err
	}
	// Each impossible combination is refused by the flags that made it
	// impossible. Resolving one against another by precedence would fetch a
	// corpus the operator did not ask for, and a bulk fetch is the one
	// command where that mistake costs hours.
	switch {
	case rf.host != "" && *allHosts:
		return c.usagef("--host names one machine and --all-hosts names every machine; use one or the other")
	case rf.host == "" && !*allHosts:
		return c.usagef("sessions fetch-all needs --host ID or --all-hosts: materializing a whole corpus is a deliberate act, so it is never the default")
	case *allHosts && *snapshot != "":
		return c.usagef("--snapshot names one snapshot, which belongs to one host; pass it with --host ID")
	case *workers < 1:
		return c.usagef("--concurrency must be at least 1, got %d", *workers)
	case *workers > maxFetchConcurrency:
		return c.usagef("--concurrency %d exceeds the %d this command will drive a repository with", *workers, maxFetchConcurrency)
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
	hosts := []string{rf.host}
	if *allHosts {
		if hosts = archiveHosts(snapshots); len(hosts) == 0 {
			return fmt.Errorf("the repository holds no snapshots")
		}
	}

	res, fetchErr := a.fetchFleetCorpus(ctx, c, repo, d.sessionsRoot(), hosts, snapshots, *snapshot, *workers)
	// The report is written whatever happened. A batch that was interrupted or
	// lost a host still materialized whatever it materialized, and the
	// operator's next act depends on knowing which sessions those were.
	if err := a.reportFetchAll(res, *asJSON); err != nil {
		return err
	}
	if fetchErr != nil {
		return fetchErr
	}
	if res.Failed > 0 {
		return fmt.Errorf("%d of %d %s could not be materialized; the rest are here",
			res.Failed, res.Sessions, plural(res.Sessions, "session", "sessions"))
	}
	return nil
}

// fetchFleetCorpus materializes every session the named hosts archived. It is
// the whole capability behind `sessions fetch-all`, factored out of the flags so
// an unattended caller — a conductor keeping this machine's copy of the corpus
// current under its own ceilings — drives it without a command line.
//
// A host that cannot be enumerated is fatal when it is the only one named and
// recorded when it is one of several. That follows from the request rather than
// from a policy flag: an operator who named one machine has nothing left to
// receive, while a fleet-wide pass that dropped four machines because the fifth
// was mid-backup would be the refusal SPEC.md §6.3 rules out.
func (a *app) fetchFleetCorpus(ctx context.Context, c *cmd, repo *restic.Repo, root string,
	hosts []string, snapshots []restic.Snapshot, want string, workers int) (fetchAllResult, error) {
	res := fetchAllResult{Target: Sanitize(root)}
	for _, host := range hosts {
		got, err := a.fetchHostCorpus(ctx, c, repo, root, host, snapshots, want, workers)
		if err != nil {
			if len(hosts) == 1 {
				return res, err
			}
			a.diagf("warning: %s contributed nothing: %s\n", Sanitize(host), Sanitize(err.Error()))
			res.Hosts = append(res.Hosts, hostFetch{Host: Sanitize(host), Error: Sanitize(err.Error())})
			continue
		}
		res.Hosts = append(res.Hosts, got)
		res.Sessions += got.Sessions
		res.Fetched += got.Fetched
		res.Resumed += got.Resumed
		res.Failed += len(got.Failures)
		res.Files += got.Files
		res.Bytes += got.Bytes
		if err := ctx.Err(); err != nil {
			return res, err
		}
	}
	return res, nil
}

// fetchHostCorpus materializes every session one host's snapshot holds.
//
// Snapshot selection and identification precede any download, the same order a
// cross-host single fetch uses: the archive rather than this machine is the
// authority on what a named host has, and identifying the whole snapshot once
// costs one listing for the entire corpus.
func (a *app) fetchHostCorpus(ctx context.Context, c *cmd, repo *restic.Repo, root, host string,
	snapshots []restic.Snapshot, want string, workers int) (hostFetch, error) {
	hostSnapshots, err := snapshotsForHost(c, snapshots, host)
	if err != nil {
		return hostFetch{}, err
	}
	if want == "" {
		want = "latest"
	}
	chosen, err := pickSnapshot(c, hostSnapshots, want)
	if err != nil {
		return hostFetch{}, err
	}
	archived, err := identifyArchived(ctx, repo, chosen.ID)
	if err != nil {
		return hostFetch{}, err
	}
	a.diagf("fetching %d %s archived by %s from snapshot %s, %d at a time...\n",
		len(archived), plural(len(archived), "session", "sessions"),
		Sanitize(host), Sanitize(shortID(chosen)), workers)
	return a.fetchSessionSet(ctx, repo, root, host, chosen, archived, workers), nil
}

// fetchSessionSet restores a set of identified sessions concurrently.
//
// It never returns an error. Every outcome belongs to one session and is
// recorded against it, because the whole point of a batch is that the operator
// keeps the other three hundred sessions when one restore hits a stale lock.
// Progress narrates on stderr as each session settles, so a corpus that takes
// half an hour is never silent, while stdout keeps carrying exactly one
// document.
func (a *app) fetchSessionSet(ctx context.Context, r sessionRestorer, root, host string,
	snap restic.Snapshot, sessions []archivedSession, workers int) hostFetch {
	res := hostFetch{
		Host:            Sanitize(host),
		SnapshotID:      Sanitize(snap.ID),
		SnapshotShortID: Sanitize(shortID(snap)),
		SnapshotTime:    formatTime(snap.Time),
		Sessions:        len(sessions),
	}
	if len(sessions) == 0 {
		return res
	}
	if workers > len(sessions) {
		workers = len(sessions)
	}

	// One mutex guards both the accumulating outcome and the diagnostic
	// stream: stderr is a shared writer, and interleaved progress lines from
	// eight goroutines would be unreadable even where they are not a race.
	var mu sync.Mutex
	settled := 0
	work := make(chan archivedSession)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for session := range work {
				// A cancelled batch stops taking new work rather than
				// recording every remaining session as a failure: nothing
				// looked at them, and the counts say so by not summing.
				if ctx.Err() != nil {
					return
				}
				key := session.key()
				got, err := materialize(ctx, r, root, key, snap, session.sess.Files)
				mu.Lock()
				settled++
				switch {
				case err != nil:
					res.Failures = append(res.Failures, sessionFailure{
						Selector: Sanitize(key),
						Error:    Sanitize(err.Error()),
					})
					a.diagf("warning: [%s %d/%d] %s: %s\n", Sanitize(host), settled, len(sessions),
						Sanitize(key), Sanitize(err.Error()))
				case got.resumed:
					res.Resumed++
					res.Files += got.files
					res.Bytes += got.bytes
					a.diagf("[%s %d/%d] %s already materialized\n", Sanitize(host), settled, len(sessions),
						Sanitize(key))
				default:
					res.Fetched++
					res.Files += got.files
					res.Bytes += got.bytes
					a.diagf("[%s %d/%d] %s restored %d %s\n", Sanitize(host), settled, len(sessions),
						Sanitize(key), got.files, plural(got.files, "file", "files"))
				}
				mu.Unlock()
			}
		}()
	}
feed:
	for _, session := range sessions {
		select {
		case <-ctx.Done():
			break feed
		case work <- session:
		}
	}
	close(work)
	wg.Wait()

	// Completion order is whichever restore finished first, which is not a
	// property of the corpus. The report is ordered by selector so two runs
	// over the same archive read the same.
	sort.Slice(res.Failures, func(i, j int) bool { return res.Failures[i].Selector < res.Failures[j].Selector })
	return res
}

// archiveHosts lists the machines that have published snapshots, which is the
// fleet a --all-hosts fetch covers. Attribution is the snapshot's own recorded
// host, never this machine's hostname.
func archiveHosts(snapshots []restic.Snapshot) []string {
	hosts := make([]string, 0, len(snapshots))
	for _, s := range snapshots {
		if s.Host != "" {
			hosts = append(hosts, s.Host)
		}
	}
	return sortedUnique(hosts)
}

// reportFetchAll writes one bulk fetch's outcome. The human form is one row per
// host plus the totals, because the question a batch answers is "is this
// machine's copy of the fleet corpus complete" and that is a per-host answer.
func (a *app) reportFetchAll(res fetchAllResult, asJSON bool) error {
	if asJSON {
		return a.emitJSON(res)
	}
	rows := make([][]string, 0, len(res.Hosts))
	for _, h := range res.Hosts {
		snapshot := h.SnapshotShortID
		if h.Error != "" {
			snapshot = h.Error
		}
		rows = append(rows, []string{
			h.Host, snapshot,
			fmt.Sprint(h.Sessions), fmt.Sprint(h.Fetched), fmt.Sprint(h.Resumed),
			fmt.Sprint(len(h.Failures)), fmt.Sprint(h.Bytes),
		})
	}
	if err := writeTable(a.stdout,
		[]string{"HOST", "SNAPSHOT", "SESSIONS", "FETCHED", "RESUMED", "FAILED", "BYTES"}, rows); err != nil {
		return err
	}
	fmt.Fprintf(a.stdout, "\n%d %s across %d %s: %d fetched, %d already here, %d failed\n",
		res.Sessions, plural(res.Sessions, "session", "sessions"),
		len(res.Hosts), plural(len(res.Hosts), "host", "hosts"),
		res.Fetched, res.Resumed, res.Failed)
	fmt.Fprintf(a.stdout, "materialized under %s\n", res.Target)
	fmt.Fprint(a.stdout, "discover it with: babel sessions list --fetched\n")
	return nil
}
