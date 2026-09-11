package cli

// Restore-and-rescan: the drain that completes a `catalog-pending` snapshot.
//
// A catalog-pending row is one restic's counts reached and session detail never
// did - what a PostgreSQL outage, a `storage rebuild`, or an adoption from the
// repository snapshot list leaves behind. The detail is not derivable from the
// snapshot listing, because a session's sizes, closure and metadata come from
// the session's own bytes, so for the whole of Phase A the state had no shipped
// resolution and `archive status` said so.
//
// It has one now, and it is not a command. SPEC.md §9.1 requires every record
// Babel produces to reach the shared catalog without an operator action, and a
// state that only a remembered invocation could clear is the defect that
// section names rather than a task for the operator. So the drain rides the
// hourly `archive push` every source machine already runs: restore the
// snapshot, rescan it with the adapters that own its trees, publish the session
// rows it actually held, mark it committed.
//
// Three properties make that safe to run unattended, and each is asserted
// below rather than hoped for: the work is bounded per push, the restore area
// is disposable and removed on success and on failure alike, and one snapshot
// that cannot be restored or described costs that snapshot and not the rest.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/atyrode/babel/internal/config"
	"github.com/atyrode/babel/internal/restic"
	"github.com/atyrode/babel/internal/sharedcatalog"
)

// maxRescanPerPush bounds how many catalog-pending snapshots one push restores.
//
// The bound exists because the timer is hourly and the work is a full restore:
// without it, the first push after a rebuild of a long-lived host would restore
// every snapshot that host ever took, in one run, on a machine the operator is
// using for something else. Two per push drains a backlog of twenty in ten
// hours unattended, which is the right trade for a state that is rare and
// never urgent - the snapshots are durable and restorable throughout, and only
// the catalog's detail about them is missing.
//
// It is also what keeps a genuinely unrestorable snapshot from stalling the
// rest: it consumes one of the two slots on each push and the other slot keeps
// draining the backlog.
const maxRescanPerPush = 2

// rescanLeaseTTL is how long a rescan may hold the lease on the host whose
// snapshot it is completing.
//
// It is longer than publicationLeaseTTL because it covers different work. A
// publication takes its lease after restic has committed, so it bounds one
// short transaction; a rescan has to restore a whole snapshot before it has
// anything to write, and a lease that expired in the middle of that would make
// the completion refuse work already done - for every snapshot whose restore
// takes longer than a publication's TTL, on every push, permanently. Fifteen
// minutes covers a restore of any plausible session corpus, and still bounds
// how long another instance waits to publish for that host if this process
// dies mid-rescan.
const rescanLeaseTTL = 15 * time.Minute

// rescanOutcome is what one push's restore-and-rescan achieved.
type rescanOutcome struct {
	// completed counts snapshots that left catalog-pending, and recovered the
	// session rows written for them.
	completed int
	recovered int
	// unrecovered counts snapshots this push attempted and could not complete.
	// It is reported rather than left to inference: a pending count that did
	// not fall needs to say whether nothing was tried or something failed.
	unrecovered int
}

// rescanVerdict says what one snapshot's rescan did, short of failing.
//
// Three outcomes rather than a bool because they are three different sentences
// for the operator, and two of them are not this push's work at all: reporting
// "completed by another instance" for a host whose lease is merely busy would
// claim a completion nobody has made yet.
//
// The zero value is deliberately not the completion. A verdict returned
// alongside an error means nothing, and the one mistake worth making
// structurally impossible is a failure that reads as a recovered snapshot.
type rescanVerdict int

const (
	// rescanSettled means this push changed nothing and nothing is owed: the
	// row was no longer catalog-pending by the time the write was attempted,
	// because another instance completed it. It is also the verdict a failure
	// carries, where the error is the answer.
	rescanSettled rescanVerdict = iota
	// rescanHeld means another instance holds the publication lease of the
	// host that produced the snapshot, so its rows are that instance's to
	// write. Nothing is owed: its push drains this snapshot, or a later one.
	rescanHeld
	// rescanCompleted means the row left catalog-pending carrying the session
	// rows this rescan read out of the snapshot.
	rescanCompleted
)

// rescanRoot is the disposable area a restore-and-rescan materializes into.
//
// The cache directory, not the data directory: SPEC.md §9 gives the cache
// disposable staging and gives the data directory the fetched session trees
// that `sessions prune` is the only command allowed to remove from. A rescan's
// bytes are neither browsable nor prunable - they exist for one describe and
// are gone before the push returns - so filing them with the retained corpus
// would put trees under prune's authority that no operator ever asked for.
func (d dirs) rescanRoot() string { return filepath.Join(d.cache, "rescan") }

// drainPending completes as many catalog-pending snapshots as this push is
// allowed to, and reports what it managed.
//
// It never returns an error. Every failure belongs to one snapshot, is reported
// against it, and leaves that row exactly as it was - catalog-pending, durable,
// and eligible for the next push. The push's own publication has already
// settled by the time this runs, so nothing here can move its exit code.
//
// self is the lease this push already holds on its own host. Another host's
// rows need that host's lease, which is acquired per snapshot and released
// immediately: writing a machine's session rows without its lease is what
// publication_order and the fencing exist to prevent.
func (a *app) drainPending(ctx context.Context, d dirs, db *sql.DB, repo *restic.Repo,
	cfg config.Config, listing []restic.Snapshot, self sharedcatalog.Lease) rescanOutcome {
	var out rescanOutcome

	pending, err := sharedcatalog.PendingSnapshots(ctx, db)
	if err != nil {
		a.diagf("warning: could not read the catalog-pending snapshots: %s\n", Sanitize(err.Error()))
		return out
	}
	chosen := pickPending(pending, listing, maxRescanPerPush)
	if len(chosen) == 0 {
		return out
	}

	// One run-scoped area for the whole drain, removed whatever happens next.
	// Each snapshot then gets a subdirectory of its own that is removed as soon
	// as that snapshot is done, so the peak cost is one restored snapshot
	// rather than every snapshot this push touches.
	if err := ensureDir(d.rescanRoot()); err != nil {
		a.diagf("warning: could not prepare the rescan area: %s\n", Sanitize(err.Error()))
		return out
	}
	root, err := os.MkdirTemp(d.rescanRoot(), "run-")
	if err != nil {
		a.diagf("warning: could not prepare the rescan area: %s\n", Sanitize(err.Error()))
		return out
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			a.diagf("warning: could not remove the rescan area %s: %s\n",
				Sanitize(root), Sanitize(err.Error()))
		}
	}()

	a.diagf("restoring %d catalog-pending %s to recover their session detail...\n",
		len(chosen), plural(len(chosen), "snapshot", "snapshots"))
	for _, snap := range chosen {
		// A cancelled push stops taking new work rather than recording the
		// remaining snapshots as failures: nothing looked at them.
		if ctx.Err() != nil {
			return out
		}
		recovered, verdict, err := a.rescanSnapshot(ctx, root, db, repo, cfg, snap, self)
		switch {
		case err != nil:
			out.unrecovered++
			a.diagf("warning: could not recover the session detail of snapshot %s: %s\n",
				Sanitize(shortSnapshotID(snap.ID)), Sanitize(err.Error()))
		case verdict == rescanCompleted:
			out.completed++
			out.recovered += recovered
			a.diagf("note: snapshot %s left catalog-pending with %d %s recovered\n",
				Sanitize(shortSnapshotID(snap.ID)), recovered,
				plural(recovered, "session", "sessions"))
		case verdict == rescanHeld:
			a.diagf("note: another instance is publishing for %s, so snapshot %s is left to that push\n",
				Sanitize(snap.Host), Sanitize(shortSnapshotID(snap.ID)))
		default:
			a.diagf("note: snapshot %s was completed by another instance\n",
				Sanitize(shortSnapshotID(snap.ID)))
		}
	}
	return out
}

// pickPending selects the catalog-pending snapshots one push will restore.
//
// Two rules. A pending row the repository no longer lists is skipped: it cannot
// be restored, and it is the MissingFromRepository anomaly reconciliation
// already surfaces rather than something a rescan can fix. And the limit is
// spent round-robin across hosts rather than on one host's oldest snapshots,
// so a machine with a hundred pending rows cannot monopolize the drain while
// another machine's single pending row waits for a hundred pushes.
//
// Within a host the order is the host's own publication order, oldest first,
// which is the order PendingSnapshots returns them in. That is deterministic:
// the same catalog and the same repository choose the same snapshots.
func pickPending(pending []sharedcatalog.PendingSnapshot, listing []restic.Snapshot, limit int) []restic.Snapshot {
	if limit <= 0 {
		return nil
	}
	listed := make(map[string]restic.Snapshot, len(listing))
	for _, s := range listing {
		listed[s.ID] = s
	}

	byHost := make(map[string][]restic.Snapshot)
	var hosts []string
	for _, p := range pending {
		snap, ok := listed[p.SnapshotID]
		if !ok {
			continue
		}
		if _, seen := byHost[p.HostID]; !seen {
			hosts = append(hosts, p.HostID)
		}
		byHost[p.HostID] = append(byHost[p.HostID], snap)
	}
	sort.Strings(hosts)

	out := make([]restic.Snapshot, 0, limit)
	for round := 0; len(out) < limit; round++ {
		took := false
		for _, host := range hosts {
			queue := byHost[host]
			if round >= len(queue) {
				continue
			}
			out = append(out, queue[round])
			took = true
			if len(out) == limit {
				return out
			}
		}
		if !took {
			break
		}
	}
	return out
}

// rescanSnapshot restores one snapshot, describes the sessions it held, and
// publishes them as that snapshot's session detail.
//
// It reports how many session rows it recovered and whether the catalog row
// actually left catalog-pending. Those are two answers rather than one: a row
// another instance completed first is not a failure, and a snapshot that held
// no session Babel can identify completes with no rows at all.
//
// That last case is an observation and not a shrug. The restore is complete or
// it is an error, so "no session in this snapshot" means the whole restored
// tree was walked and no adapter recognized one - exactly what an ordinary push
// records when discovery finds nothing on a machine whose source roots exist
// and are empty.
//
// Two residues around harnesses are real and are handled differently, because
// one is invisible here and the other is not. A harness this binary does not
// read at all - the set is internal/harness's single declaration - contributes
// nothing to the listing, so a snapshot holding only such trees completes with
// no sessions, which is the same blind spot an ordinary push of that machine
// would have. A harness this binary does read but the catalog's session
// vocabulary does not admit is visible, and the snapshot is refused rather than
// completed short of it: `sessions.harness` is a closed enum, so the row could
// not carry that session and a `session_count` including it would be a number
// no reader could reconcile with the rows present.
//
// A session that cannot be described is refused for the same reason. The files
// are byte-identical to what the snapshot holds and adapters tolerate torn and
// malformed lines by counting them, so a describe failure here is unusual
// enough that retrying on a later push is the honest response rather than a
// loop.
func (a *app) rescanSnapshot(ctx context.Context, root string, db *sql.DB, repo *restic.Repo,
	cfg config.Config, snap restic.Snapshot, self sharedcatalog.Lease) (recovered int, verdict rescanVerdict, err error) {
	if snap.Host == "" {
		return 0, rescanSettled, errors.New("the snapshot records no host, so its sessions have no identity")
	}

	// The lease covers the restore as well as the write, so it is taken for
	// longer than a publication's. publicationLeaseTTL bounds one short
	// transaction, which is right for a push: restic has already committed by
	// the time it is taken. A rescan's transaction is just as short, but the
	// work that has to happen first is a full restore of a snapshot, and a
	// lease that expired during it would make CompletePending refuse a
	// recovery that had already been paid for - every hour, forever, for any
	// snapshot slower than two minutes.
	lease := self
	if snap.Host != self.HostID {
		// Another machine's rows, so another machine's lease. A lease that is
		// held is not an error: that instance is publishing for the host right
		// now, and its own push or a later one drains this snapshot.
		lease, err = sharedcatalog.AcquireHostLease(ctx, db, snap.Host, cfg.InstanceID, rescanLeaseTTL)
		if err != nil {
			if errors.Is(err, sharedcatalog.ErrLeaseHeld) {
				return 0, rescanHeld, nil
			}
			return 0, rescanSettled, fmt.Errorf("take the publication lease of %s: %w", Sanitize(snap.Host), err)
		}
		defer func() {
			// Best effort, for the reason a push's own release is: the lease
			// expires on its own, and failing to release it must not discard a
			// completion that committed.
			_ = sharedcatalog.ReleaseHostLease(ctx, db, lease)
		}()
	} else if lease, err = sharedcatalog.RenewHostLease(ctx, db, self, rescanLeaseTTL); err != nil {
		// This host's own lease, extended rather than re-acquired: re-acquiring
		// mints a fresh fence, which would invalidate the lease the push is
		// still going to release. Renewal keeps the fence, so the push's own
		// release still lands and shortens it again.
		return 0, rescanSettled, fmt.Errorf("extend this host's publication lease: %w", err)
	}

	tree := filepath.Join(root, shortSnapshotID(snap.ID))
	if err := ensureDir(tree); err != nil {
		return 0, rescanSettled, err
	}
	defer func() {
		if rmErr := os.RemoveAll(tree); rmErr != nil {
			a.diagf("warning: could not remove the restored snapshot %s: %s\n",
				Sanitize(tree), Sanitize(rmErr.Error()))
		}
	}()

	// The whole snapshot, with no include filters. Restoring reads the
	// repository and writes nothing to it, exactly as `sessions fetch` does.
	// Filtering to the identified sessions' closures would save little - a
	// Babel snapshot is source trees, and those trees are the sessions - and
	// would pass restic one --include per file, which a corpus of a few
	// thousand sessions cannot fit into an argument list.
	if err := repo.Restore(ctx, snap.ID, nil, tree); err != nil {
		return 0, rescanSettled, fmt.Errorf("restore: %w", err)
	}

	// identifyFetched is the cross-host identification a fetched tree already
	// goes through: restic recreates the recorded absolute paths beneath the
	// target, so stripping the target prefix recovers the paths the snapshot
	// holds and each adapter names its own sessions in them. Reusing it is what
	// keeps a rescanned session's identity equal to the one its own machine
	// assigns, with no second layout rule anywhere.
	sessions, err := identifyFetched(tree, fetchedOrigin{Host: snap.Host, SnapshotID: snap.ID}, adapters())
	if err != nil {
		return 0, rescanSettled, err
	}

	rows := make([]sharedcatalog.SessionRow, 0, len(sessions))
	for _, session := range sessions {
		// A harness the catalog's session vocabulary does not admit cannot be
		// recorded, and the row must not be completed without it: session_count
		// would then be a number no reader could reconcile with the sessions
		// present. So the snapshot keeps its state and says why, which is the
		// one residue this drain genuinely cannot clear (SPEC.md 9, 14).
		if !sharedcatalog.PublishableHarness(session.src.Harness) {
			return 0, rescanSettled, fmt.Errorf(
				"it holds a %s session, and the catalog's session vocabulary admits only the harnesses migrations/0001_init.sql enumerates",
				session.src.Harness)
		}
		desc, err := describe(ctx, session)
		if err != nil {
			return 0, rescanSettled, err
		}
		rows = append(rows, rescannedSessionRow(session, desc, cfg.DeploymentID, snap.Host))
	}

	applied, err := sharedcatalog.CompletePending(ctx, db, lease, snap.ID, cfg.InstanceID, rows)
	switch {
	case err != nil:
		return 0, rescanSettled, err
	case !applied:
		return 0, rescanSettled, nil
	}
	return len(rows), rescanCompleted, nil
}
