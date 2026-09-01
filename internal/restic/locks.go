package restic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"
)

// staleLockTimeout mirrors restic's own staleLockTimeout: the age past which
// restic considers a lock stale whatever host it names, because a live restic
// process refreshes its lock every five minutes and one that stopped
// refreshing is one that stopped running.
//
// It is duplicated here rather than derived, because it cannot be read from
// the binary: restic exposes no "would this lock be removed" query, so the
// only way to state a judgement before removing anything is to make the same
// judgement restic makes. That is a copy with a version dependency, so it is
// named and dated: restic 0.19.1, internal/repository/lock_file.go.
const staleLockTimeout = 30 * time.Minute

// Lock is one lock file the repository holds, as restic recorded it.
//
// A repository lock is not archived data: it is the coordination record restic
// writes while a command runs and removes when it finishes, so an interrupted
// process leaves one behind. Every field here comes from that record; Babel
// derives none of them.
type Lock struct {
	// ID is the lock's full storage id, and ShortID restic's abbreviation of
	// it - the form restic's own diagnostics print and the form an operator
	// therefore has in hand.
	ID      string
	ShortID string

	// Time is when restic created or last refreshed the lock.
	Time time.Time

	// Exclusive distinguishes the lock `check`, `prune` and `repair` take,
	// which no other command may hold alongside, from the shared lock a
	// backup or a restore takes.
	Exclusive bool

	// Hostname and PID name the process that holds the lock. Both are the
	// holder's own claim about itself: they are only checkable from the
	// machine the lock names.
	Hostname string
	PID      int

	// Unreadable, when non-empty, says why the lock's own document could not
	// be read - a lock removed between the listing and the read, an empty
	// file a backend left behind mid-upload, or a corrupt one. The lock
	// exists and blocks other commands either way, so it is reported rather
	// than dropped, and restic's stale removal skips exactly these.
	Unreadable string
}

// LockVerdict is the staleness judgement for one lock together with the
// reasoning that reached it.
//
// The reason is the point. `restic unlock` prints how many locks it removed
// and nothing about why, so an operator clearing a lock has to trust the
// removal rather than check it. Reason states the ground the judgement stands
// on - an age, an unreachable process, or the absence of any claim that could
// be checked - and never mentions an age in numbers, because the caller
// already renders the lock's age and two renderings of one number invite a
// disagreement between them.
type LockVerdict struct {
	Stale  bool
	Reason string
}

// Verdict judges lock l the way restic's own `unlock` judges it: stale past
// staleLockTimeout whatever host it names, else stale when it names this
// machine and its process cannot be reached, else held.
//
// The ordering is restic's and matters: age comes first, so a lock older than
// the timeout is stale even though its host is unverifiable from here, and the
// liveness claim is only ever made about this machine's own processes.
func (l Lock) Verdict() LockVerdict {
	// An unreadable hostname makes every lock's host unverifiable rather than
	// matching the locks that record no host, which is also what restic does
	// with it: it declines the liveness test rather than guessing.
	hostname, err := os.Hostname()
	if err != nil {
		hostname = ""
	}
	return l.verdict(time.Now(), hostname, processReachable)
}

// verdict is Verdict with its three ambient inputs injected, so the rule can
// be tested against a fixed clock, a chosen hostname, and a process table
// that does not depend on which pids the test host happens to have.
func (l Lock) verdict(now time.Time, hostname string, reachable func(pid int) bool) LockVerdict {
	if l.Unreadable != "" {
		return LockVerdict{Reason: "its own lock document could not be read, and restic's stale removal skips a lock it cannot load"}
	}
	if now.Sub(l.Time) > staleLockTimeout {
		// Whole minutes rather than Duration's own "30m0s": the timeout is a
		// round number in restic's documentation and in the runbook, and the
		// figure is derived from the constant so the two cannot drift.
		return LockVerdict{Stale: true, Reason: fmt.Sprintf("no longer refreshed: older than restic's %dm staleness timeout", int(staleLockTimeout/time.Minute))}
	}
	if hostname == "" || l.Hostname == "" || l.Hostname != hostname {
		return LockVerdict{Reason: "it names another host, so whether its process still runs cannot be checked from here"}
	}
	if l.PID <= 0 {
		return LockVerdict{Reason: "it names no process, so whether its holder still runs cannot be checked"}
	}
	if reachable(l.PID) {
		return LockVerdict{Reason: fmt.Sprintf("PID %d on this host is alive", l.PID)}
	}
	return LockVerdict{Stale: true, Reason: fmt.Sprintf("PID %d on this host cannot be signalled, so the process is gone", l.PID)}
}

// processReachable reports whether a signal can be delivered to pid, which is
// the same test restic's stale check makes and is deliberately not a stronger
// one: a process this user may not signal reads as gone to restic, so reading
// it as alive here would let Babel promise a lock survives a removal restic is
// about to perform.
func processReachable(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// lockJSON is one lock document as `restic cat lock` prints it. Username, uid
// and gid are also recorded there and deliberately not read: they identify a
// person rather than the process, and nothing Babel decides consults them.
type lockJSON struct {
	Time      time.Time `json:"time"`
	Exclusive bool      `json:"exclusive"`
	Hostname  string    `json:"hostname"`
	PID       int       `json:"pid"`
}

// Locks lists every lock the repository holds, oldest first, and reads each
// one's document. It takes no lock of its own (--no-lock), so it can report
// the locks that are blocking everything else.
//
// One unreadable lock does not fail the listing. A lock can disappear between
// being listed and being read - the process holding it finishing is the happy
// case - and refusing to report the other locks because one of them got better
// would be the wrong answer to "what is in there".
func (r *Repo) Locks(ctx context.Context) ([]Lock, error) {
	out, err := r.run(ctx, "list locks", "list", "locks", "--no-lock")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	locks := make([]Lock, 0, len(ids))
	for _, id := range ids {
		locks = append(locks, r.readLock(ctx, id))
	}
	// Oldest first: the lock most likely to be stale is the one an operator
	// is looking for, and a stable order makes two runs comparable.
	sort.Slice(locks, func(i, j int) bool {
		if !locks[i].Time.Equal(locks[j].Time) {
			return locks[i].Time.Before(locks[j].Time)
		}
		return locks[i].ID < locks[j].ID
	})
	return locks, nil
}

// readLock reads one lock's document, reporting a lock that could not be read
// as itself rather than as an error.
func (r *Repo) readLock(ctx context.Context, id string) Lock {
	lock := Lock{ID: id, ShortID: id}
	if len(id) > shortIDLen {
		lock.ShortID = id[:shortIDLen]
	}
	out, err := r.run(ctx, "cat lock", "cat", "lock", id, "--no-lock")
	if err != nil {
		lock.Unreadable = err.Error()
		return lock
	}
	var doc lockJSON
	if err := json.Unmarshal(out, &doc); err != nil {
		lock.Unreadable = fmt.Sprintf("restic cat lock: parsing json: %v", err)
		return lock
	}
	lock.Time = doc.Time
	lock.Exclusive = doc.Exclusive
	lock.Hostname = doc.Hostname
	lock.PID = doc.PID
	return lock
}

// Unlock removes repository locks: with removeAll false the stale ones only,
// which is restic's own default and its own staleness rule; with removeAll
// true every lock in the repository, including locks live processes are
// holding.
//
// It is the one verb in this package that removes anything, and it removes no
// archived data: a lock is coordination state, so retention stays append-only
// (SPEC.md §6.1). restic offers no third granularity - no per-lock removal
// exists - which is why the caller states its intent before calling and why
// removeAll is a decision made above this package rather than here.
func (r *Repo) Unlock(ctx context.Context, removeAll bool) error {
	args := []string{"unlock"}
	op := "unlock"
	if removeAll {
		args = append(args, "--remove-all")
		op = "unlock --remove-all"
	}
	_, err := r.run(ctx, op, args...)
	return err
}
