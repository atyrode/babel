package conductor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"
)

// JournalName is the conductor's cycle journal, held in Babel's private local
// state.
//
// It lives beside the durable database rather than inside it, and that is a
// judgement about what it is. A receipt is analysis: it must never be lost and
// it will eventually be committed remotely (SPEC.md §9). A cycle record is the
// loop's own account of its scheduling — why it drew what it drew, what it
// parked on, which cycle was in flight when the process died — which nothing
// else can rebuild but which no one else needs. Putting it in the durable
// database would put scheduling bookkeeping into the remote sync path; putting
// it in the cache would invite discarding the only record of a park.
const JournalName = "conductor.json"

// JournalCap bounds the journal.
//
// A conductor accumulates one record per cycle forever, and `conductor status`
// answers "the last N outcomes" rather than "every cycle since the machine was
// built". The cap is what keeps a file that is rewritten on every cycle small
// enough that rewriting it is free, and 200 is comfortably more history than the
// floor's arithmetic or a status view reads.
const JournalCap = 200

// journalSchema versions the document this package owns.
const journalSchema = 1

// Journal is the conductor's cycle record: the loop's memory across restarts and
// the source `conductor status` reads.
//
// It is held whole in memory and rewritten atomically on every record. That is
// the right trade at one write per cycle: an append-only log would need its own
// truncation, its own partial-line recovery and its own tail reader, to save
// microseconds on a file a minutes-long cycle writes twice.
type Journal struct {
	path   string
	cycles []Cycle
}

// journalFile is the stored shape.
type journalFile struct {
	Schema int     `json:"schema"`
	Cycles []Cycle `json:"cycles"`
}

// OpenJournal reads the journal in dir, creating neither the file nor a record
// until something is written. A missing journal is the normal state of a machine
// where the conductor has never run.
func OpenJournal(dir string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("conductor: create state directory: %w", err)
	}
	path := filepath.Join(dir, JournalName)
	j := &Journal{path: path}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return j, nil
	}
	if err != nil {
		return nil, fmt.Errorf("conductor: read %s: %w", path, err)
	}
	var stored journalFile
	if err := json.Unmarshal(data, &stored); err != nil {
		// The path is named and the content is not, matching the rule every
		// other Babel settings document follows: a stored document is not a
		// place to quote values from.
		return nil, fmt.Errorf("conductor: decode %s: %w", path, err)
	}
	if stored.Schema > journalSchema {
		return nil, fmt.Errorf("conductor: %s: schema %d, this build reads %d",
			path, stored.Schema, journalSchema)
	}
	j.cycles = stored.Cycles
	sort.SliceStable(j.cycles, func(a, b int) bool { return j.cycles[a].Seq < j.cycles[b].Seq })
	return j, nil
}

// Path reports the journal's location, so a status view can say where its
// answers come from.
func (j *Journal) Path() string { return j.path }

// Record stores one cycle, replacing the entry with the same sequence number if
// there is one.
//
// Replacing rather than appending is what makes the in-flight record work: a
// cycle is written before its run starts and rewritten when it ends, so at every
// instant the journal holds exactly one entry per cycle and the last entry says
// whether a run is happening right now.
func (j *Journal) Record(c Cycle) error {
	c.Note = TrimNote(c.Note)
	c.Reason = TrimNote(c.Reason)
	if i := slices.IndexFunc(j.cycles, func(existing Cycle) bool { return existing.Seq == c.Seq }); i >= 0 {
		j.cycles[i] = c
	} else {
		j.cycles = append(j.cycles, c)
		sort.SliceStable(j.cycles, func(a, b int) bool { return j.cycles[a].Seq < j.cycles[b].Seq })
	}
	if len(j.cycles) > JournalCap {
		j.cycles = slices.Clone(j.cycles[len(j.cycles)-JournalCap:])
	}
	return j.save()
}

// save rewrites the journal atomically, so an interrupted write leaves the
// previous record rather than a truncated one — the same rule the settings
// documents follow, and for the same reason: the file is the only copy.
func (j *Journal) save() error {
	data, err := json.MarshalIndent(journalFile{Schema: journalSchema, Cycles: j.cycles}, "", "  ")
	if err != nil {
		return fmt.Errorf("conductor: encode the cycle journal: %w", err)
	}
	data = append(data, '\n')
	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("conductor: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, j.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("conductor: replace %s: %w", j.path, err)
	}
	return nil
}

// NextSeq is the sequence number the next cycle takes.
func (j *Journal) NextSeq() int {
	if len(j.cycles) == 0 {
		return 1
	}
	return j.cycles[len(j.cycles)-1].Seq + 1
}

// Last returns the most recent cycle.
func (j *Journal) Last() (Cycle, bool) {
	if len(j.cycles) == 0 {
		return Cycle{}, false
	}
	return j.cycles[len(j.cycles)-1], true
}

// Recent returns the last n cycles, newest first. A zero or negative n returns
// them all, bounded by the cap the file is already held to.
func (j *Journal) Recent(n int) []Cycle {
	if n <= 0 || n > len(j.cycles) {
		n = len(j.cycles)
	}
	out := make([]Cycle, 0, n)
	for i := len(j.cycles) - 1; i >= len(j.cycles)-n; i-- {
		out = append(out, j.cycles[i])
	}
	return out
}

// Reverse iterates the journal newest first, which is the direction every
// question about it is asked in: what is the loop doing now, when did it last
// draw a serendipity cycle, what were the last few outcomes.
func (j *Journal) Reverse() []Cycle { return j.Recent(0) }

// State is what the journal says the loop is doing, and it is deliberately
// derived from the record rather than from a separate liveness file: two sources
// for one answer is how a status view starts lying.
type State string

// The states a conductor can be observed in.
const (
	// StateIdle is a conductor that is not running. It is the state of a
	// machine where the loop has never run and of one whose last cycle
	// finished cleanly — the difference is visible in the cycles themselves.
	StateIdle State = "idle"
	// StateRunning is a cycle in flight whose conductor process still exists.
	StateRunning State = "running"
	// StateParked is a loop whose last act was to refuse a cycle on the
	// budget. It is not an error state; it is the ceilings working, and the
	// reason is on the cycle.
	StateParked State = "parked"
	// StateInterrupted is a cycle recorded as in flight whose conductor is
	// gone. The next conductor resumes it under its original run identity, so
	// this state is a note about the last process rather than about lost work.
	StateInterrupted State = "interrupted"
)

// Observe reports what the journal says the loop is doing now.
//
// The liveness probe is what keeps this honest. A journal entry saying a cycle
// is running is a claim about a process, and a conductor that was killed leaves
// that claim behind; reading it back as "running" would make the status view
// confidently wrong for as long as nobody restarted the loop.
func (j *Journal) Observe() (State, Cycle) {
	last, ok := j.Last()
	if !ok {
		return StateIdle, Cycle{}
	}
	switch last.Outcome {
	case OutcomeRunning:
		if processAlive(last.PID) {
			return StateRunning, last
		}
		return StateInterrupted, last
	case OutcomeParked:
		return StateParked, last
	default:
		return StateIdle, last
	}
}

// SpentToday sums the journal's own record of what the day's cycles estimated.
//
// It is not what the ceilings are enforced against — ReceiptLedger reads the
// receipts, which include runs an operator started by hand — but it is what the
// loop believes it spent, and a status view showing both would show a real
// disagreement rather than a rounding difference.
func (j *Journal) SpentToday(now time.Time, currency string) float64 {
	day := StartOfDay(now)
	var total float64
	for _, c := range j.cycles {
		if c.Currency != currency || c.StartedAt.Before(day) {
			continue
		}
		total += c.Cost
	}
	return total
}
