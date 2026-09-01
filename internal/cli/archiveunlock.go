package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/restic"
)

// lockIDLen is the length of a full restic lock id, and lockShortIDLen the
// abbreviation restic's own diagnostics print. --remove accepts exactly these
// two forms: they are the ids the listing above it shows, and admitting an
// arbitrary prefix would let a single mistyped character name a lock the
// operator never read.
const (
	lockIDLen      = 64
	lockShortIDLen = 8
)

// lockRow is one repository lock as this command reports it: what restic
// recorded, plus the staleness judgement and the reasoning behind it.
//
// Every value a lock claims about itself is reported and none is trusted. The
// host and pid are the holder's own words, so they are sanitized like any
// other foreign value, and the judgement states which of them it relied on.
type lockRow struct {
	ID      string `json:"id"`
	ShortID string `json:"short_id"`
	// Exclusive is restic's own distinction, not a severity: an exclusive
	// lock blocks every other command rather than only the writers.
	Exclusive bool `json:"exclusive"`
	// Host and PID name the holder. Absent for a lock whose document could
	// not be read, because then nothing named anything.
	Host string `json:"host,omitempty"`
	PID  int    `json:"pid,omitempty"`
	// Created and AgeSeconds are when restic last refreshed the lock and how
	// long ago that was by this machine's clock. Absent, never zero, for an
	// unreadable lock: 0 would read as "just now".
	Created    string `json:"created,omitempty"`
	AgeSeconds *int64 `json:"age_seconds,omitempty"`
	// Stale is the verdict and Reason the ground it stands on, both mirroring
	// restic's own stale test so the listing predicts restic's behaviour
	// rather than describing a policy of Babel's own.
	Stale  bool   `json:"stale"`
	Reason string `json:"reason"`
	// Unreadable carries restic's own message for a lock whose document could
	// not be read. Such a lock still blocks other commands, so it is listed.
	Unreadable string `json:"unreadable,omitempty"`
	// Named records that the operator named this lock with --remove, which is
	// the only way a lock that is not both stale and shared is ever removed.
	Named bool `json:"named,omitempty"`

	age time.Duration
}

// judgement is the one-word verdict shown in the table.
func (r lockRow) judgement() string {
	switch {
	case r.Stale:
		return "stale"
	case r.Unreadable != "":
		return "unreadable"
	default:
		return "held"
	}
}

// removableByDefault reports whether a run with no --remove may remove this
// lock: stale, and shared. Exclusivity is the gate, because an exclusive lock
// is the one restic takes for `check`, `prune` and `repair`, and a repository
// mid-repair is exactly the state whose lock must not be cleared by a command
// that was only asked to tidy up after an interrupted backup.
func (r lockRow) removableByDefault() bool { return r.Stale && !r.Exclusive }

// unlockResult is the machine-readable outcome of one unlock run.
//
// Locks is the listing as it stood before anything was removed, which is the
// evidence for what followed; Removed is what a second listing showed gone
// afterwards, rather than what restic said it did, so the report describes the
// repository instead of the command.
type unlockResult struct {
	Repository string    `json:"repository"`
	Locks      []lockRow `json:"locks"`
	Removed    []string  `json:"removed"`
	Remaining  int       `json:"remaining"`
	// Refused names why a run removed nothing it was asked to. It is the
	// --json half of the multi-line remedy written to stderr.
	Refused string `json:"refused,omitempty"`
}

// lockRefusal is a removal this command declines to perform, with the remedy
// that makes it possible. Summary is one line for --json and for the first
// line on stderr; remedy is the rest, which is layout and therefore composed
// here rather than handed to Sanitize (see errReported).
type lockRefusal struct {
	summary string
	remedy  string
}

// archiveUnlock implements `babel archive unlock`.
//
// It is an operator-typed command and nothing else may reach it: no timer, no
// conductor duty, and no autonomous path. That is structural rather than
// promised - internal/conductor holds no repository handle at all, and the web
// surface reaches the archive only through web.ArchiveOperations, which offers
// status, verify, sessions and fetch and no removal of anything - so the only
// caller of restic.Repo.Unlock in Babel is this function.
//
// The order of operations is the contract: list every lock and state the
// judgement on each one, then remove, then read the repository again to
// report what is actually gone. An operator who is about to break a lock is
// entitled to see what he is breaking first, and a summary derived from a
// second listing cannot claim a removal that did not happen.
func (a *app) archiveUnlock(ctx context.Context, args []string) error {
	c := newCmd("archive unlock", archiveUnlockUsage)
	var rf repoFlags
	// Repository selection without --host: the locks belong to the
	// repository, not to a machine, and the staleness judgement compares
	// against the kernel's hostname because that is what restic wrote into
	// the lock. A --host here would look like it changed that comparison.
	rf.bindRepo(c.fs)
	remove := c.fs.String("remove", "", "comma-separated lock ids to remove even when not stale or shared")
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
	locks, err := repo.Locks(ctx)
	if err != nil {
		return a.resticFailure("list repository locks", err)
	}

	res := unlockResult{Repository: Sanitize(rf.repository), Locks: lockRows(locks, time.Now())}
	if err := nameLocks(c, res.Locks, *remove); err != nil {
		return err
	}
	if !*asJSON {
		if err := a.printLocks(res.Locks); err != nil {
			return err
		}
	}

	targets, force, refusal := planUnlock(res.Locks)
	res.Remaining = len(res.Locks)
	switch {
	case refusal != nil:
		res.Refused = refusal.summary
	case len(targets) > 0:
		if err := repo.Unlock(ctx, force); err != nil {
			return a.resticFailure("remove repository locks", err)
		}
		// A lock that crosses restic's staleness timeout between this
		// listing and this removal is removed by restic's own rule rather
		// than by the judgement printed above, so what happened is read back
		// from the repository instead of assumed from the plan.
		after, err := repo.Locks(ctx)
		if err != nil {
			return a.resticFailure("list repository locks", err)
		}
		res.Removed, res.Remaining = removedLocks(res.Locks, after)
	}

	if *asJSON {
		if err := a.emitJSON(res); err != nil {
			return err
		}
	} else if err := a.reportUnlock(res, targets); err != nil {
		return err
	}
	if refusal != nil {
		fmt.Fprintf(a.stderr, "babel: %s\n\n%s\n", refusal.summary, refusal.remedy)
		return errReported
	}
	// restic exits 0 having removed nothing when a lock it planned to remove
	// is already gone, which is fine, and equally when one it could not
	// remove remains, which is not: the operator would read "ok" and go on to
	// a command the lock still blocks.
	if len(targets) > 0 && len(res.Removed) < len(targets) {
		fmt.Fprintf(a.stderr, "babel: restic reported success but %d of the %d locks it was asked to remove are still there\n",
			len(targets)-len(res.Removed), len(targets))
		return errReported
	}
	return nil
}

// lockRows renders the listing: restic's record of each lock, its age by this
// machine's clock, and the verdict internal/restic reaches with restic's own
// rule.
func lockRows(locks []restic.Lock, now time.Time) []lockRow {
	rows := make([]lockRow, 0, len(locks))
	for _, lock := range locks {
		verdict := lock.Verdict()
		row := lockRow{
			ID:         Sanitize(lock.ID),
			ShortID:    Sanitize(lock.ShortID),
			Exclusive:  lock.Exclusive,
			Host:       Sanitize(lock.Hostname),
			PID:        lock.PID,
			Stale:      verdict.Stale,
			Reason:     Sanitize(verdict.Reason),
			Unreadable: Sanitize(lock.Unreadable),
		}
		// A lock whose document could not be read has no time of its own, and
		// an age computed from a zero timestamp would report it as decades
		// stale - a number the listing never observed.
		if !lock.Time.IsZero() {
			row.age = now.Sub(lock.Time)
			if row.age < 0 {
				row.age = 0
			}
			row.Created = formatTime(lock.Time)
			row.AgeSeconds = seconds(row.age)
		}
		rows = append(rows, row)
	}
	return rows
}

// nameLocks marks the rows the operator named with --remove.
//
// A named lock must be in the listing. That is what makes the flag a
// confirmation rather than a formality: the ids come from the table printed
// above it, so naming one is a statement about a lock that was read, and an id
// that matches nothing is a mistyped or already-cleared lock rather than
// permission to remove whatever is there now.
func nameLocks(c *cmd, rows []lockRow, raw string) error {
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if len(part) != lockIDLen && len(part) != lockShortIDLen {
			return c.usagef("invalid --remove lock id %q: name a lock by the %d-character short id or the full %d-character id the listing prints",
				part, lockShortIDLen, lockIDLen)
		}
		var matched []string
		for i := range rows {
			if rows[i].ID == part || rows[i].ShortID == part {
				rows[i].Named = true
				matched = append(matched, rows[i].ShortID)
			}
		}
		switch len(matched) {
		case 1:
		case 0:
			return c.usagef("--remove names lock %q, which this repository does not hold; it holds %d %s",
				part, len(rows), plural(len(rows), "lock", "locks"))
		default:
			return c.usagef("--remove %q is ambiguous, it matches %d locks: %s",
				part, len(matched), strings.Join(matched, " "))
		}
	}
	return nil
}

// planUnlock decides what one run may remove, and refuses rather than exceed
// it. One invariant governs both branches: a lock is removed only when it is
// stale and shared, or the operator named it.
//
// restic offers exactly two granularities and neither is per-lock. Plain
// `unlock` removes every stale lock, exclusive ones included, so a stale
// exclusive lock nobody named cannot be left behind while its shared
// neighbours are cleared - which is why its presence refuses the run instead
// of being quietly removed with them. `unlock --remove-all` removes every lock
// there is, so a run that needs it refuses unless every lock in the repository
// is one this command was already entitled to remove.
//
// The refusals are therefore restic's granularity showing through, and they
// say so: the alternative would be a command whose documented default removes
// less than it removes.
func planUnlock(rows []lockRow) (targets []lockRow, force bool, refusal *lockRefusal) {
	for _, row := range rows {
		if row.Named && !row.removableByDefault() {
			force = true
		}
	}
	for _, row := range rows {
		if row.Named || row.removableByDefault() {
			continue
		}
		// An unnamed lock that is not removable by default blocks the run
		// only when it stands in the way: --remove-all would remove it
		// whatever it is, while plain unlock reaches it only if it is stale,
		// which for a lock that is not removable by default means exclusive.
		if force {
			return nil, false, blockedByLock(rows, row)
		}
		if row.Stale {
			return nil, false, blockedByStaleExclusive(row)
		}
	}
	for _, row := range rows {
		// --remove-all removes every lock, and the loop above proved every
		// one of them is either named or removable by default.
		if force || row.Stale {
			targets = append(targets, row)
		}
	}
	return targets, force, nil
}

// blockedByStaleExclusive refuses a default run whose stale locks include an
// exclusive one.
func blockedByStaleExclusive(row lockRow) *lockRefusal {
	return &lockRefusal{
		summary: fmt.Sprintf("refusing to remove any lock: %s is exclusive and stale", row.ShortID),
		remedy: fmt.Sprintf(`restic's stale removal takes every stale lock at once and cannot exclude
one. The default run is entitled to locks that are stale and shared, so it
cannot run at all while this one is here. An exclusive lock is what "restic
check", "prune" and "repair" hold, so clearing one is a decision about a
repository that may be mid-repair rather than tidying up after a backup.

Name it to make that decision deliberately:

  babel archive unlock --remove %s`, row.ShortID),
	}
}

// blockedByLock refuses a run that would need --remove-all while a lock it was
// not entitled to remove is present.
func blockedByLock(rows []lockRow, row lockRow) *lockRefusal {
	named := make([]string, 0, len(rows)+1)
	for _, r := range rows {
		if r.Named {
			named = append(named, r.ShortID)
		}
	}
	named = append(named, row.ShortID)
	return &lockRefusal{
		summary: fmt.Sprintf("refusing to remove any lock: %s is %s and was not named", row.ShortID, row.judgement()),
		remedy: fmt.Sprintf(`Removing a named lock is restic's --remove-all, which removes every lock in
the repository, so this run would remove that one too. Its judgement was:
%s.

Wait for it to be released, or name it as well once you know it is dead:

  babel archive unlock --remove %s`, row.Reason, strings.Join(named, ",")),
	}
}

// removedLocks compares the listing this run reported with the repository
// afterwards, and returns what is gone plus what is left. The removal report
// is therefore an observation rather than restic's own count, which would
// credit this command for a lock its holder released on its own.
func removedLocks(before []lockRow, after []restic.Lock) (removed []string, remaining int) {
	present := make(map[string]struct{}, len(after))
	for _, lock := range after {
		present[Sanitize(lock.ID)] = struct{}{}
	}
	for _, row := range before {
		if _, ok := present[row.ID]; !ok {
			removed = append(removed, row.ShortID)
		}
	}
	return removed, len(after)
}

// printLocks writes the listing every run produces before it removes anything.
//
// The reason travels in its own column rather than as a note below the table:
// there is one per row, it is the whole point of listing before removing, and
// a reader deciding whether to name a lock is comparing the reasons against
// each other.
func (a *app) printLocks(rows []lockRow) error {
	if len(rows) == 0 {
		return nil
	}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{
			row.ShortID,
			yesNo(row.Exclusive, "exclusive", "shared"),
			orMissing(row.Host),
			lockPIDCell(row),
			lockAgeCell(row),
			row.judgement(),
			row.Reason,
		})
	}
	return writeTable(a.stdout,
		[]string{"LOCK", "KIND", "HOST", "PID", "AGE", "JUDGEMENT", "REASON"},
		table)
}

// lockPIDCell renders the holder's pid, or absence for a lock that named none.
func lockPIDCell(row lockRow) string {
	if row.PID <= 0 {
		return missingValue
	}
	return fmt.Sprint(row.PID)
}

// lockAgeCell renders how long ago the lock was last refreshed, or absence for
// one whose document could not be read.
func lockAgeCell(row lockRow) string {
	if row.AgeSeconds == nil {
		return missingValue
	}
	return formatAge(row.age)
}

// reportUnlock writes the one line that says what the run did.
//
// A run that removes nothing is a success and says which nothing it was: an
// empty repository lock set, or locks that are all still held. Neither is a
// failure - the operator asked what could be cleared and the answer was
// nothing - and reporting it as one would train him to ignore the exit code
// of the command he reaches for when a lock is blocking a check.
func (a *app) reportUnlock(res unlockResult, targets []lockRow) error {
	switch {
	case res.Refused != "":
		// The refusal itself, with its remedy, goes to stderr beside the
		// other diagnostics; stdout has already shown the listing it is
		// about.
		return nil
	case len(res.Locks) == 0:
		fmt.Fprint(a.stdout, "no locks: nothing to remove\n")
	case len(targets) == 0:
		fmt.Fprintf(a.stdout, "nothing to remove: no lock is both stale and shared, and none was named\n")
	case len(res.Removed) == 0:
		fmt.Fprint(a.stdout, "nothing was removed\n")
	default:
		fmt.Fprintf(a.stdout, "removed %d %s: %s\n",
			len(res.Removed), plural(len(res.Removed), "lock", "locks"), strings.Join(res.Removed, ", "))
	}
	if res.Remaining > 0 && len(res.Removed) > 0 {
		fmt.Fprintf(a.stdout, "%d %s %s\n",
			res.Remaining, plural(res.Remaining, "lock", "locks"), plural(res.Remaining, "remains", "remain"))
	}
	return nil
}
