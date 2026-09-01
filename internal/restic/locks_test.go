package restic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The staleness rule is restic's, and Babel states it before removing
// anything, so every branch of it is pinned here: the age test that applies
// whatever host a lock names, the refusal to judge another host's pid, and the
// liveness test that is only ever made about this machine.
func TestLockVerdictMirrorsResticsOwnStaleRule(t *testing.T) {
	const thisHost = "workstation-linux"
	now := time.Date(2026, 8, 31, 3, 30, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute)
	old := now.Add(-staleLockTimeout - time.Minute)
	alive := func(int) bool { return true }
	dead := func(int) bool { return false }

	cases := []struct {
		name      string
		lock      Lock
		hostname  string
		reachable func(int) bool
		stale     bool
		reason    string
	}{
		{
			name:      "past the timeout on this host",
			lock:      Lock{Time: old, Hostname: thisHost, PID: 1},
			hostname:  thisHost,
			reachable: alive,
			stale:     true,
			reason:    "older than restic's 30m staleness timeout",
		},
		{
			// The age test comes first in restic and applies to a lock this
			// machine cannot check at all, which is the only ground on which
			// another host's lock is ever removed.
			name:      "past the timeout on another host",
			lock:      Lock{Time: old, Hostname: "other-host", PID: 4242},
			hostname:  thisHost,
			reachable: dead,
			stale:     true,
			reason:    "older than restic's 30m staleness timeout",
		},
		{
			name:      "fresh on another host is never judged by its pid",
			lock:      Lock{Time: fresh, Hostname: "other-host", PID: 4242},
			hostname:  thisHost,
			reachable: dead,
			stale:     false,
			reason:    "it names another host",
		},
		{
			name:      "fresh on this host with a dead process",
			lock:      Lock{Time: fresh, Hostname: thisHost, PID: 2841104},
			hostname:  thisHost,
			reachable: dead,
			stale:     true,
			reason:    "cannot be signalled",
		},
		{
			name:      "fresh on this host with a live process",
			lock:      Lock{Time: fresh, Hostname: thisHost, PID: 2841104},
			hostname:  thisHost,
			reachable: alive,
			stale:     false,
			reason:    "is alive",
		},
		{
			// An unreadable hostname must not make every lock's host match:
			// that would invent a liveness claim out of a failure.
			name:      "an unknown local hostname declines the liveness test",
			lock:      Lock{Time: fresh, Hostname: "", PID: 2841104},
			hostname:  "",
			reachable: dead,
			stale:     false,
			reason:    "it names another host",
		},
		{
			name:      "no pid recorded",
			lock:      Lock{Time: fresh, Hostname: thisHost},
			hostname:  thisHost,
			reachable: alive,
			stale:     false,
			reason:    "it names no process",
		},
		{
			// restic's own stale removal skips a lock it cannot load, so a
			// judgement of "stale" here would predict a removal that will not
			// happen - even when the lock is plainly ancient.
			name:      "unreadable is never stale",
			lock:      Lock{Time: old, Unreadable: "restic cat lock: exit status 1"},
			hostname:  thisHost,
			reachable: dead,
			stale:     false,
			reason:    "could not be read",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.lock.verdict(now, tc.hostname, tc.reachable)
			if got.Stale != tc.stale {
				t.Errorf("Stale = %v, want %v (reason %q)", got.Stale, tc.stale, got.Reason)
			}
			if !strings.Contains(got.Reason, tc.reason) {
				t.Errorf("Reason = %q, want it to name %q", got.Reason, tc.reason)
			}
		})
	}
}

// Verdict's ambient inputs are the point of the exercise: a judgement that
// consulted the wrong clock, hostname or process table would be confidently
// wrong. This drives the real ones against a process that certainly exists and
// one that certainly does not.
func TestVerdictReadsThisMachine(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Skipf("this machine has no readable hostname: %v", err)
	}
	live := Lock{Time: time.Now(), Hostname: hostname, PID: os.Getpid()}
	if v := live.Verdict(); v.Stale {
		t.Errorf("this test's own process read as gone: %+v", v)
	}

	// A reaped child's pid is unreachable by construction.
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running a throwaway child: %v", err)
	}
	gone := Lock{Time: time.Now(), Hostname: hostname, PID: cmd.Process.Pid}
	if v := gone.Verdict(); !v.Stale {
		t.Errorf("a reaped child's lock read as held: %+v", v)
	}
}

// plantLock leaves one real restic lock behind in the fixture's repository,
// the way an interrupted command does: it starts a locking restic command,
// stops it the moment its lock file appears, and then kills it, so the lock
// stays and the process holding it is gone and reaped.
//
// The window is not a race. restic sleeps 200ms between creating its lock and
// re-checking for others (internal/repository/lock_file.go, newLock), so the
// lock file exists for at least that long even for a command that would
// otherwise finish immediately.
func (f *fixture) plantLock(t *testing.T, args ...string) string {
	t.Helper()
	before := f.lockIDs(t)
	cmd, err := f.Repo.command(context.Background(), args...)
	if err != nil {
		t.Fatalf("building %v: %v", args, err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %v: %v", args, err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		for id := range f.lockIDs(t) {
			if _, had := before[id]; had {
				continue
			}
			// Stopped before it can release the lock or notice the kill.
			if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
				t.Fatalf("stopping the lock holder: %v", err)
			}
			return id
		}
		if time.Now().After(deadline) {
			t.Fatalf("no lock appeared while running %v", args)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lockIDs reads the lock ids present in a local-path repository directly, so
// planting a lock does not depend on the code under test.
//
// restic's local backend uploads through a "<id>-tmp-<n>" name and renames it
// into place, so only finished lock files count: a plant that returned the
// temporary name would name a file that no longer exists a moment later.
func (f *fixture) lockIDs(t *testing.T) map[string]struct{} {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.repoDir, "locks"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading the repository's locks: %v", err)
	}
	ids := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if name := entry.Name(); isLockID(name) {
			ids[name] = struct{}{}
		}
	}
	return ids
}

// fullIDLen is the length of a restic storage id rendered as hex.
const fullIDLen = 64

// isLockID reports whether name is a finished lock file rather than one of
// restic's in-flight uploads.
func isLockID(name string) bool {
	if len(name) != fullIDLen {
		return false
	}
	for _, r := range name {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

// The listing and the removal are exercised against a real repository holding
// a real abandoned lock, because that is the state the verb exists for: a lock
// whose holder is gone, blocking `restic check` while restores keep working.
func TestLocksReportsAnAbandonedLockAndUnlockRemovesIt(t *testing.T) {
	f := newFixture(t)
	f.writeFile(t, "src/session.jsonl", []byte(`{"role":"user"}`+"\n"))
	ctx := context.Background()

	planted := f.plantLock(t, "backup", "--json", filepath.Join(f.root, "src"))

	locks, err := f.Locks(ctx)
	if err != nil {
		t.Fatalf("Locks: %v", err)
	}
	if len(locks) != 1 {
		t.Fatalf("Locks reported %d locks, want the one that was planted: %+v", len(locks), locks)
	}
	lock := locks[0]
	if lock.ID != planted {
		t.Errorf("lock id = %q, want the planted %q", lock.ID, planted)
	}
	if lock.ShortID != planted[:shortIDLen] {
		t.Errorf("short id = %q, want %q", lock.ShortID, planted[:shortIDLen])
	}
	if lock.Unreadable != "" {
		t.Errorf("a lock restic had just written read as unreadable: %q", lock.Unreadable)
	}
	if lock.Exclusive {
		t.Error("a backup's lock reported itself exclusive")
	}
	if lock.PID <= 0 {
		t.Errorf("lock recorded no pid: %+v", lock)
	}
	if hostname, err := os.Hostname(); err == nil && lock.Hostname != hostname {
		t.Errorf("lock hostname = %q, want this machine's %q", lock.Hostname, hostname)
	}
	if lock.Time.IsZero() {
		t.Error("lock recorded no time")
	}
	// The holder was killed and reaped, so restic's own rule makes this stale
	// without waiting out the 30m timeout.
	if v := lock.Verdict(); !v.Stale {
		t.Errorf("an abandoned lock read as held: %+v", v)
	}

	if err := f.Unlock(ctx, false); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	after, err := f.Locks(ctx)
	if err != nil {
		t.Fatalf("Locks after Unlock: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("Unlock left %d locks behind: %+v", len(after), after)
	}

	// Removing nothing is a success: the operator's second run must not
	// invent a failure out of a repository that is already clean.
	if err := f.Unlock(ctx, false); err != nil {
		t.Fatalf("Unlock on a repository with no locks: %v", err)
	}
	if err := f.Check(ctx, false); err != nil {
		t.Fatalf("check after the lock was cleared: %v", err)
	}
}

// A stale exclusive lock is the case restic's own default cannot express, and
// the case Babel's default therefore refuses: plain `unlock` removes it too.
// The refusal lives in internal/cli; what is proven here is the restic
// behaviour it exists for, so the reason for that refusal cannot silently
// stop being true.
func TestPlainUnlockRemovesAStaleExclusiveLock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	planted := f.plantLock(t, "check")

	locks, err := f.Locks(ctx)
	if err != nil {
		t.Fatalf("Locks: %v", err)
	}
	if len(locks) != 1 || locks[0].ID != planted {
		t.Fatalf("Locks reported %+v, want the planted %q", locks, planted)
	}
	if !locks[0].Exclusive {
		t.Fatalf("a killed `restic check` left a shared lock: %+v", locks[0])
	}

	if err := f.Unlock(ctx, false); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	after, err := f.Locks(ctx)
	if err != nil {
		t.Fatalf("Locks after Unlock: %v", err)
	}
	if len(after) != 0 {
		t.Fatalf("restic's stale removal spared the exclusive lock: %+v", after)
	}
}
