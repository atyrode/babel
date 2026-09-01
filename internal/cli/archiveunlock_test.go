package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// plantLock leaves one real restic lock behind in the fixture's repository the
// way an interrupted command does: it starts a locking restic command, stops it
// the moment its lock file appears, and then kills it, so the lock stays and
// its holder is gone and reaped.
//
// The window is not a race: restic sleeps 200ms between creating a lock and
// re-checking for others (internal/repository/lock_file.go, newLock), so the
// lock file is present that long even for a command that would otherwise
// finish at once.
func (f *fixture) plantLock(args ...string) string {
	f.t.Helper()
	before := f.lockIDs()
	cmd := exec.Command(resticBinary(f.t), args...)
	cmd.Env = append(os.Environ(),
		"RESTIC_REPOSITORY="+f.repoDir,
		"RESTIC_PASSWORD_FILE="+f.passwordFile,
		"RESTIC_CACHE_DIR="+filepath.Join(f.cacheDir, "restic"),
	)
	if err := cmd.Start(); err != nil {
		f.t.Fatalf("starting restic %v: %v", args, err)
	}
	f.t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
	})

	deadline := time.Now().Add(30 * time.Second)
	for {
		for id := range f.lockIDs() {
			if _, had := before[id]; had {
				continue
			}
			if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
				f.t.Fatalf("stopping the lock holder: %v", err)
			}
			// Killed now, while it is stopped: the lock file it wrote stays
			// behind and no process answers for it any more.
			_ = cmd.Process.Signal(syscall.SIGKILL)
			_ = cmd.Wait()
			return id
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("no lock appeared while running restic %v", args)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// lockIDs reads the repository's lock ids from the directory itself, so the
// tests never depend on the listing they are checking.
//
// restic's local backend uploads through a "<id>-tmp-<n>" name and renames it
// into place, so only finished lock files count: a plant that returned the
// temporary name would name a file that no longer exists a moment later.
func (f *fixture) lockIDs() map[string]struct{} {
	f.t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.repoDir, "locks"))
	if err != nil && !os.IsNotExist(err) {
		f.t.Fatalf("reading the repository's locks: %v", err)
	}
	ids := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if len(name) != lockIDLen || strings.Trim(name, "0123456789abcdef") != "" {
			continue
		}
		ids[name] = struct{}{}
	}
	return ids
}

// lockSource writes a small tree for a planted backup to lock the repository
// over.
func (f *fixture) lockSource() string {
	f.t.Helper()
	dir := filepath.Join(f.root, "lock-source")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("synthetic\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return dir
}

// The whole verb, end to end, against the failure it exists for: an abandoned
// shared lock that makes `archive verify` exit 1 while the repository itself is
// sound. A clean repository first, because "nothing to remove" is a success
// that has to say so rather than read as a malfunction.
func TestArchiveUnlockClearsAnAbandonedLock(t *testing.T) {
	f := newFixture(t).withRepo()
	f.bootstrapRepo()

	stdout, _ := f.ok(f.with("archive", "unlock")...)
	if !strings.Contains(stdout, "no locks: nothing to remove") {
		t.Fatalf("a clean repository did not say so:\n%s", stdout)
	}

	planted := f.plantLock("backup", "--json", f.lockSource())
	short := planted[:lockShortIDLen]

	// The lock is what an operator meets it as: verify fails, restores do not.
	if _, stderr, code := f.run(f.with("archive", "verify")...); code != exitFailure {
		t.Fatalf("verify exited %d with a lock present, want 1: %s", code, stderr)
	}

	stdout, stderr := f.ok(f.with("archive", "unlock")...)
	for _, want := range []string{
		"LOCK", "KIND", "JUDGEMENT", "REASON",
		short, "shared", "stale", "cannot be signalled",
		"removed 1 lock: " + short,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("unlock output does not name %q:\n%s\nstderr:\n%s", want, stdout, stderr)
		}
	}
	if ids := f.lockIDs(); len(ids) != 0 {
		t.Fatalf("the lock is still in the repository: %v", ids)
	}
	if stdout, stderr := f.ok(f.with("archive", "verify")...); !strings.Contains(stdout, "ok (structure)") {
		t.Fatalf("verify still fails after the lock was cleared:\n%s\n%s", stdout, stderr)
	}

	// The second run is the one an operator makes when unsure whether the
	// first worked. It must be a success that says nothing was there.
	stdout, _ = f.ok(f.with("archive", "unlock")...)
	if !strings.Contains(stdout, "no locks: nothing to remove") {
		t.Fatalf("the repeat run did not report an empty repository:\n%s", stdout)
	}
}

// An exclusive lock is what `restic check`, `prune` and `repair` hold, and
// restic's stale removal cannot spare one, so a default run refuses rather than
// remove it silently - and says which lock and which flag. Naming it is what
// makes the removal the operator's decision.
func TestArchiveUnlockRefusesAnExclusiveLockUntilItIsNamed(t *testing.T) {
	f := newFixture(t).withRepo()
	f.bootstrapRepo()

	planted := f.plantLock("check")
	short := planted[:lockShortIDLen]

	stdout, stderr := f.mustExit(exitFailure, f.with("archive", "unlock")...)
	if !strings.Contains(stdout, "exclusive") || !strings.Contains(stdout, "stale") {
		t.Fatalf("the refused run did not list the lock it refused:\n%s", stdout)
	}
	for _, want := range []string{
		"refusing to remove any lock", short, "babel archive unlock --remove " + short,
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("the refusal does not name %q:\n%s", want, stderr)
		}
	}
	if _, ok := f.lockIDs()[planted]; !ok {
		t.Fatalf("the refused run removed the lock anyway: %v", f.lockIDs())
	}

	// --json carries the same refusal, and reports nothing removed.
	stdout, _ = f.mustExit(exitFailure, f.with("archive", "unlock", "--json")...)
	res := decode[unlockResult](t, stdout)
	if res.Refused == "" || !strings.Contains(res.Refused, short) {
		t.Fatalf("json refusal = %q, want it to name %s", res.Refused, short)
	}
	if len(res.Removed) != 0 || res.Remaining != 1 {
		t.Fatalf("json refusal claimed a removal: %+v", res)
	}
	if len(res.Locks) != 1 || !res.Locks[0].Exclusive || !res.Locks[0].Stale {
		t.Fatalf("json listing = %+v, want one stale exclusive lock", res.Locks)
	}

	stdout, _ = f.ok(f.with("archive", "unlock", "--remove", short, "--json")...)
	named := decode[unlockResult](t, stdout)
	if len(named.Removed) != 1 || named.Removed[0] != short {
		t.Fatalf("naming the lock removed %+v, want %s", named.Removed, short)
	}
	if named.Remaining != 0 || !named.Locks[0].Named {
		t.Fatalf("named run = %+v, want an empty repository and a named lock", named)
	}
	if ids := f.lockIDs(); len(ids) != 0 {
		t.Fatalf("the named lock is still there: %v", ids)
	}
}

// --remove is a confirmation, so it only accepts an id the listing just
// printed: a mistyped or already-cleared id is a rejected invocation rather
// than permission to remove whatever the repository holds now.
func TestArchiveUnlockRejectsLockIDsItNeverListed(t *testing.T) {
	f := newFixture(t).withRepo()
	f.bootstrapRepo()

	for _, tc := range []struct {
		name string
		id   string
		want string
	}{
		{"absent", "5f86a86f", "which this repository does not hold"},
		{"malformed", "5f86", "name a lock by the 8-character short id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr := f.mustExit(exitUsage, f.with("archive", "unlock", "--remove", tc.id)...)
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("rejection does not say %q:\n%s", tc.want, stderr)
			}
		})
	}
}

// The listing is the evidence for every removal that follows it, so its
// columns are a contract: what the lock claims, and which of those claims the
// judgement relied on.
func TestPrintLocksRendersTheJudgementTable(t *testing.T) {
	var stdout, stderr strings.Builder
	a := &app{stdout: &stdout, stderr: &stderr}

	err := a.printLocks([]lockRow{
		{
			ShortID: "5f86a86f", Host: "ubuntu-4gb-nbg1-1", PID: 2841104,
			AgeSeconds: seconds(2*time.Hour + 48*time.Minute), age: 2*time.Hour + 48*time.Minute,
			Stale: true, Reason: "PID 2841104 on this host cannot be signalled, so the process is gone",
		},
		{
			ShortID: "e64f7be2", Exclusive: true, Host: "other-host", PID: 5512,
			AgeSeconds: seconds(12 * time.Minute), age: 12 * time.Minute,
			Reason: "it names another host, so whether its process still runs cannot be checked from here",
		},
		{
			ShortID: "aa11bb22", Unreadable: "restic cat lock: exit status 1",
			Reason: "its own lock document could not be read, and restic's stale removal skips a lock it cannot load",
		},
	})
	if err != nil {
		t.Fatalf("printLocks: %v", err)
	}

	const want = "LOCK      KIND       HOST               PID      AGE    JUDGEMENT   REASON\n" +
		"5f86a86f  shared     ubuntu-4gb-nbg1-1  2841104  2h48m  stale       PID 2841104 on this host cannot be signalled, so the process is gone\n" +
		"e64f7be2  exclusive  other-host         5512     12m    held        it names another host, so whether its process still runs cannot be checked from here\n" +
		"aa11bb22  shared     -                  -        -      unreadable  its own lock document could not be read, and restic's stale removal skips a lock it cannot load\n"
	if got := stdout.String(); got != want {
		t.Errorf("rendered lock listing:\n%s\nwant:\n%s", got, want)
	}
	if stderr.Len() != 0 {
		t.Errorf("rendering wrote diagnostics: %q", stderr.String())
	}
}

// The plan is the gate, so it is exercised directly on the states restic's two
// granularities force a decision about: what a default run may remove, and
// what a named run would drag along with it.
func TestPlanUnlockNeverExceedsStaleAndShared(t *testing.T) {
	stale := lockRow{ShortID: "aaaaaaaa", ID: "aaaaaaaa", Stale: true}
	held := lockRow{ShortID: "bbbbbbbb", ID: "bbbbbbbb"}
	staleExclusive := lockRow{ShortID: "cccccccc", ID: "cccccccc", Stale: true, Exclusive: true}

	cases := []struct {
		name       string
		rows       []lockRow
		wantRemove []string
		wantForce  bool
		wantRefuse string
	}{
		{name: "no locks"},
		{name: "held only", rows: []lockRow{held}},
		{name: "stale shared", rows: []lockRow{stale, held}, wantRemove: []string{"aaaaaaaa"}},
		{
			name:       "a stale exclusive lock nobody named refuses the run",
			rows:       []lockRow{stale, staleExclusive},
			wantRefuse: "cccccccc is exclusive and stale",
		},
		{
			name:       "naming it removes it, and the stale shared lock with it",
			rows:       []lockRow{stale, named(staleExclusive)},
			wantRemove: []string{"aaaaaaaa", "cccccccc"},
			wantForce:  true,
		},
		{
			name:       "naming a held lock is enough for that lock alone",
			rows:       []lockRow{named(held)},
			wantRemove: []string{"bbbbbbbb"},
			wantForce:  true,
		},
		{
			name:       "a held lock nobody named blocks a run that needs --remove-all",
			rows:       []lockRow{named(staleExclusive), held},
			wantRefuse: "bbbbbbbb is held and was not named",
		},
		{
			// Naming a lock that was removable anyway needs no --remove-all:
			// the narrower removal is the one that cannot reach a live lock.
			name:       "naming a stale shared lock stays on stale-only removal",
			rows:       []lockRow{named(stale), held},
			wantRemove: []string{"aaaaaaaa"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targets, force, refusal := planUnlock(tc.rows)
			if tc.wantRefuse != "" {
				if refusal == nil {
					t.Fatalf("planUnlock allowed the run, want a refusal naming %q", tc.wantRefuse)
				}
				if !strings.Contains(refusal.summary, tc.wantRefuse) {
					t.Fatalf("refusal = %q, want it to name %q", refusal.summary, tc.wantRefuse)
				}
				if len(targets) != 0 {
					t.Fatalf("a refused plan still names targets: %+v", targets)
				}
				return
			}
			if refusal != nil {
				t.Fatalf("planUnlock refused: %q", refusal.summary)
			}
			var got []string
			for _, target := range targets {
				got = append(got, target.ShortID)
			}
			if strings.Join(got, ",") != strings.Join(tc.wantRemove, ",") {
				t.Fatalf("targets = %v, want %v", got, tc.wantRemove)
			}
			if force != tc.wantForce {
				t.Fatalf("force = %v, want %v", force, tc.wantForce)
			}
		})
	}
}

// named marks a row the way --remove does.
func named(row lockRow) lockRow {
	row.Named = true
	return row
}
