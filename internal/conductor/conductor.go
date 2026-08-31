// Package conductor is Babel's runtime loop: the thing that decides what
// deserves a run when nobody typed a command (issue #96).
//
// Until this package existed Babel ran only when summoned. That is a strange
// default for an instrument whose subject matter accumulates whether or not
// anyone is watching, and the operator's objection to the summoned-only model
// was not that it was slow but that it was opaque: what Babel analyses, when,
// and by whose authority. So legibility is the invariant this package is built
// around rather than a report it produces afterwards.
//
// Four properties hold, and each is checkable rather than promised.
//
// A cycle is an ordinary run. Preparation, recipes, receipt, frontier,
// dispositions — the same path `babel prepare` and `babel explore` take, reached
// through the same code. This package mints no output kind of its own, holds no
// grant, and reads no capability: turning it off degrades Babel to exactly the
// manual instrument it was, which is why Runner is the whole of what scheduling
// is allowed to ask for.
//
// No nameable authority, no run. Every cycle carries a run.Authority naming why
// it happened — an operator's invitation, a standing policy, or a declared draw
// from the serendipity floor — and that authority reaches the receipt, where it
// outlives the loop. A rung that cannot name one produces no work.
//
// The operator outranks the loop. The ladder is ordered, and rung one is the
// operator's own process-further queue (#87). A rung is consulted only when
// every rung above it is empty, with one deliberate exception: the serendipity
// floor is a protected fraction rather than a last resort, so a guaranteed
// share of cycles is chaotic even while invitations are waiting. A loop that
// converged into pure dutifulness would stop being an emergence instrument.
//
// Autonomy is budget-bounded, not trust-bounded. The conductor refuses to exist
// without explicit per-cycle and per-day ceilings, and it enforces them against
// what the receipts actually recorded rather than against its own optimism.
// Over the ceiling it parks with a reason an operator can read, and it inherits
// the stored profile: this package never configures one, because a loop that
// could mint its own profile would be choosing its own spending limit.
//
// What this package deliberately does not do: daemonize. `babel conductor run`
// is a foreground loop that stops cleanly at a cycle boundary; supervision,
// restart policy and wall-clock scheduling belong to the OS, which already owns
// them (the archive timer is the sibling precedent).
package conductor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"syscall"
	"time"

	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/run"
)

// Outcome is what became of one cycle. Every value is a statement about the
// loop rather than about analysis: "ran" says a run happened, not that it found
// anything, and §6.5's own record of what a run produced is the receipt.
type Outcome string

// The outcomes a cycle can end in.
const (
	// OutcomeRunning is a cycle in flight. It is written before the run
	// starts, so a conductor that dies mid-cycle leaves the fact visible
	// rather than leaving no trace of the run it was paying for.
	OutcomeRunning Outcome = "running"
	// OutcomeRan is a completed run, whatever the run itself concluded.
	OutcomeRan Outcome = "ran"
	// OutcomeParked is a cycle the budget refused, with the reason.
	OutcomeParked Outcome = "parked"
	// OutcomeIdle is a cycle no rung could draw work for — no invitations, no
	// policies, and a serendipity floor with no corpus or no recipe to draw
	// from. It is not an error: an instrument with nothing to look at should
	// say so rather than invent something to do.
	OutcomeIdle Outcome = "idle"
	// OutcomeFailed is a cycle whose run returned an error. The run's own
	// receipt says what failed; this records that the loop saw it.
	OutcomeFailed Outcome = "failed"
	// OutcomeInterrupted is a cycle that was in flight when its conductor
	// stopped existing, recorded by whichever conductor found it afterwards.
	OutcomeInterrupted Outcome = "interrupted"
)

// Cycle is one turn of the loop: what it decided to do, why, and what came of
// it. It is the journal's record and `conductor status`'s row, which is
// deliberate — a status view assembled from anything other than what the loop
// wrote down could disagree with it.
type Cycle struct {
	Seq        int           `json:"seq"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at,omitzero"`
	Outcome    Outcome       `json:"outcome"`
	Reason     string        `json:"reason,omitempty"`
	Rung       string        `json:"rung,omitempty"`
	Authority  run.Authority `json:"authority,omitzero"`
	// Resumed reports that this cycle continues an interrupted one under its
	// original run identity, which is what keeps a killed conductor from
	// running the same work twice.
	Resumed bool `json:"resumed,omitempty"`
	// The assignment, recorded so an interrupted cycle can be replayed exactly
	// and so a status view can say what a run was pointed at.
	RunID      string   `json:"run_id,omitempty"`
	Invitation string   `json:"invitation,omitempty"`
	Sessions   []string `json:"sessions,omitempty"`
	Recipes    []string `json:"recipes,omitempty"`
	Roots      []string `json:"roots,omitempty"`
	Note       string   `json:"note,omitempty"`
	// What the run recorded.
	PreparationID string  `json:"preparation_id,omitempty"`
	ReceiptID     string  `json:"receipt_id,omitempty"`
	Cost          float64 `json:"cost,omitempty"`
	Currency      string  `json:"currency,omitempty"`
	Failures      int     `json:"failures,omitempty"`
	Cancelled     bool    `json:"cancelled,omitempty"`
	// PID is the conductor process that owns this cycle, so a status view can
	// tell a loop that is still working from one that died holding the record.
	PID int `json:"pid,omitempty"`
}

// assignment reconstructs the work this cycle was given, which is what a resume
// replays.
func (c Cycle) assignment() Assignment {
	return Assignment{
		Rung:       c.Rung,
		Authority:  c.Authority,
		Invitation: c.Invitation,
		Sessions:   slices.Clone(c.Sessions),
		Recipes:    slices.Clone(c.Recipes),
		Roots:      slices.Clone(c.Roots),
		Note:       c.Note,
	}
}

// Result is what one cycle's run recorded. The conductor reads it for the
// journal and for the budget, and for nothing else: a run's output belongs to
// the frontier and the receipt, not to the scheduler.
type Result struct {
	PreparationID string
	ReceiptID     string
	// Cost is the estimated cost the receipt recorded, in Currency. Both are
	// empty or zero when the profile reported none, which the budget counts as
	// unpriced rather than as free.
	Cost      float64
	Currency  string
	Failures  int
	Cancelled bool
}

// Runner performs one cycle as an ordinary run: preparation, recipes, receipt,
// frontier and dispositions, through the same path a typed command takes.
//
// It is an interface because the conductor must not be able to reach past it.
// Everything a run is allowed to do — the sandbox, the per-run grant, the
// brokered read-only corpus access, the profile — is decided on the other side
// of this boundary, so scheduling cannot widen any of it.
type Runner interface {
	Run(ctx context.Context, runID string, a Assignment) (Result, error)
}

// Ledger reports what the day's receipts already estimated. It is an interface
// for the same reason Runner is: the conductor is told what was spent, and has
// no path to a number it produced itself.
type Ledger interface {
	SpentSince(ctx context.Context, since time.Time, currency string) (Spend, error)
}

// Config is a conductor's whole configuration.
type Config struct {
	// Ceilings bound autonomy. Both are mandatory: a loop that may spend
	// without a stated limit is trust-bounded, which is the thing #96 refuses.
	Ceilings Ceilings

	// Floor is the protected fraction of cycles that are chaotic.
	Floor Floor

	// Interval is the wait between cycles. Zero means the next cycle starts as
	// soon as the previous one finished; the budget is what bounds the loop,
	// not the clock.
	Interval time.Duration

	// Ladder is the work ladder in precedence order. Rung one is consulted
	// first and the last rung is the floor.
	Ladder []Rung

	Runner  Runner
	Ledger  Ledger
	Journal *Journal

	// Now and PID are injected so a test can plant a day, a clock and a
	// process identity. Both default to the real ones.
	Now func() time.Time
	PID int

	// Presence announces each cycle that draws work to the shared catalog, so
	// a loop on this machine is visible from every other machine in the fleet
	// before its receipt commits (#118). It is the loop's own row, distinct
	// from the one internal/explore announces for the run inside the cycle:
	// the conductor can be alive while the run it started is not, and a fleet
	// view that merged the two could not say which.
	//
	// Nil is the feature quietly absent, on the same terms Runner's presence
	// is mandatory and this is not: a presence write may never block, fail or
	// slow a cycle, so a loop with no announcer schedules identically and is
	// simply invisible off-host.
	//
	// A parked or idle cycle announces nothing, deliberately. It drew no work,
	// it has no authority to name, and there is nothing for a heartbeat to be
	// about; a row for it would be the loop asserting presence on behalf of
	// work that does not exist.
	Presence presence.Announcer

	// Log narrates the loop on the operator's diagnostic stream. A silent
	// autonomous process is the opaque model #96 exists to replace, so this is
	// wired in normal operation and nil only in tests.
	Log func(format string, args ...any)
}

// Conductor is the scheduling loop.
type Conductor struct {
	cfg Config
}

// New validates cfg and returns a conductor. It performs no I/O: every reason
// a loop cannot legitimately run — no ceilings, no ladder, no runner — is
// reported before anything has been scheduled or spent.
func New(cfg Config) (*Conductor, error) {
	if err := cfg.Ceilings.validate(); err != nil {
		return nil, err
	}
	if cfg.Runner == nil {
		return nil, errors.New("conductor: a cycle is an ordinary run, so a runner is required")
	}
	if cfg.Ledger == nil {
		return nil, errors.New("conductor: enforcing a ceiling requires a ledger of what was spent")
	}
	if cfg.Journal == nil {
		return nil, errors.New("conductor: a loop nobody can read is the model #96 replaces, so a journal is required")
	}
	if len(cfg.Ladder) == 0 {
		return nil, errors.New("conductor: a ladder with no rungs can draw no work")
	}
	if cfg.Interval < 0 {
		return nil, errors.New("conductor: the interval between cycles cannot be negative")
	}
	if err := cfg.Floor.validate(); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PID == 0 {
		cfg.PID = os.Getpid()
	}
	if cfg.Log == nil {
		cfg.Log = func(string, ...any) {}
	}
	return &Conductor{cfg: cfg}, nil
}

// RunOptions bound one foreground loop.
type RunOptions struct {
	// Until stops the loop at a wall-clock time. It is sugar: the OS owns
	// scheduling, and a conductor that implemented its own calendar would be
	// reimplementing the timer that supervises it.
	Until time.Time
	// Once runs exactly one cycle. It exists so the loop is exercisable
	// without waiting for an interval, which is also how it is tested.
	Once bool
	// Stop asks the loop to finish the cycle it is in and return. Cancelling
	// the context is the harder stop: it cancels the run itself, which
	// internal/explore already makes safe — the frontier keeps what was
	// committed and the receipt records the cancellation.
	Stop <-chan struct{}
}

// ErrParked reports that the loop stopped because the budget refused the next
// cycle. It is not a failure: the ceilings are working. A supervised conductor
// is expected to be restarted and to find the day's ceiling refreshed, which is
// why parking returns rather than spinning against a limit that cannot change
// until the day does.
var ErrParked = errors.New("conductor: parked")

// Run repeats cycles until it is asked to stop, the deadline passes, or the
// budget parks it.
//
// Stopping is at cycle granularity by construction: a cycle either completed or
// is recorded as interrupted, and the next conductor to start resumes it under
// its original run identity rather than drawing the same work twice.
func (c *Conductor) Run(ctx context.Context, opt RunOptions) error {
	if err := c.refuseConcurrent(); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if stopped(opt.Stop) {
			c.cfg.Log("conductor: stopping at the cycle boundary\n")
			return nil
		}
		if !opt.Until.IsZero() && !c.cfg.Now().Before(opt.Until) {
			c.cfg.Log("conductor: the requested end time has passed\n")
			return nil
		}

		cycle, err := c.Once(ctx)
		if err != nil {
			return err
		}
		if cycle.Outcome == OutcomeParked {
			return fmt.Errorf("%w: %s", ErrParked, cycle.Reason)
		}
		if opt.Once {
			return nil
		}
		if err := c.wait(ctx, opt); err != nil {
			return err
		}
	}
}

// wait pauses between cycles, and is interruptible by every way the loop can be
// asked to stop. A sleep that ignored a stop request would make "clean at the
// cycle boundary" mean "clean within one interval".
func (c *Conductor) wait(ctx context.Context, opt RunOptions) error {
	if c.cfg.Interval <= 0 {
		return nil
	}
	timer := time.NewTimer(c.cfg.Interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-opt.Stop:
		return nil
	case <-timer.C:
		return nil
	}
}

func stopped(ch <-chan struct{}) bool {
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// Once runs one cycle: reconcile whatever the last conductor left, enforce the
// budget, draw work from the ladder, run it, and record what happened.
//
// The order is the whole design. Reconciliation comes first so an interrupted
// run is resumed rather than duplicated. The budget comes before the draw so a
// refused cycle cannot have consumed an operator's invitation on the way to
// being refused. The journal entry is written before the run so a conductor that
// dies mid-run leaves the fact behind. And the cycle is recorded whatever the
// outcome, because a loop that only journalled its successes would be exactly as
// opaque as no loop at all.
func (c *Conductor) Once(ctx context.Context) (Cycle, error) {
	now := c.cfg.Now()
	resume, resuming, err := c.reconcile(now)
	if err != nil {
		return Cycle{}, err
	}

	spend, err := c.cfg.Ledger.SpentSince(ctx, StartOfDay(now), c.cfg.Ceilings.Currency)
	if err != nil {
		return Cycle{}, fmt.Errorf("conductor: read today's spend: %w", err)
	}
	if reason, over := c.cfg.Ceilings.refuse(spend); over {
		return c.park(now, reason)
	}

	// The run identity is minted before the draw, because taking work is part
	// of the draw: rung one claims an operator's invitation in the name of the
	// run that is about to happen, and a claim that named no run could not be
	// checked against what ran.
	seq := c.cfg.Journal.NextSeq()
	runID := newRunID(now, seq)
	assignment := resume.assignment()
	if resuming {
		seq, runID = resume.Seq, resume.RunID
	} else {
		assignment, err = c.draw(ctx, DrawRequest{RunID: runID, At: now})
		if err != nil {
			return Cycle{}, err
		}
		if assignment.Rung == "" {
			return c.idle(now, assignment.Note)
		}
	}

	cycle := Cycle{
		Seq:        seq,
		StartedAt:  now,
		Outcome:    OutcomeRunning,
		Rung:       assignment.Rung,
		Authority:  assignment.Authority,
		Resumed:    resuming,
		RunID:      runID,
		Invitation: assignment.Invitation,
		Sessions:   assignment.Sessions,
		Recipes:    assignment.Recipes,
		Roots:      assignment.Roots,
		Note:       assignment.Note,
		PID:        c.cfg.PID,
	}
	if err := c.cfg.Journal.Record(cycle); err != nil {
		return Cycle{}, err
	}
	c.cfg.Log("conductor: cycle %d on the %s rung, authority %s: %s\n",
		cycle.Seq, cycle.Rung, cycle.Authority, cycle.Note)

	// The cycle becomes visible to the fleet here, between the journal entry
	// and the run: the journal is this machine's own record and presence is
	// every other machine's, and both are written before the work rather than
	// after it, for the same reason - a conductor that died mid-cycle must
	// leave the fact behind rather than leaving no trace of what it was paying
	// for.
	//
	// Nothing below can fail this cycle. Announce returns an empty id when the
	// catalog was unreachable, which makes the heartbeat loop and the finalize
	// no-ops, and the errors it swallowed have already reached the store's own
	// diagnostic sink. So the loop runs identically on a machine whose
	// PostgreSQL is down; it is only invisible.
	presenceID := c.announce(ctx, cycle)
	stopBeat := presence.Beat(ctx, c.cfg.Presence, presenceID)

	result, runErr := c.cfg.Runner.Run(ctx, runID, assignment)
	// The heartbeat stops before the row is finalized, so the last thing the
	// fleet sees about this cycle is how it ended rather than a heartbeat that
	// raced past it.
	stopBeat()

	cycle.FinishedAt = c.cfg.Now()
	cycle.PreparationID = result.PreparationID
	cycle.ReceiptID = result.ReceiptID
	cycle.Cost = result.Cost
	cycle.Currency = result.Currency
	cycle.Failures = result.Failures
	cycle.Cancelled = result.Cancelled
	switch {
	case runErr != nil:
		cycle.Outcome = OutcomeFailed
		cycle.Reason = runErr.Error()
	default:
		cycle.Outcome = OutcomeRan
	}
	c.finalize(ctx, presenceID, cycle)
	if err := c.cfg.Journal.Record(cycle); err != nil {
		return Cycle{}, err
	}
	if runErr != nil {
		c.cfg.Log("conductor: cycle %d degraded: %v\n", cycle.Seq, runErr)
	} else {
		c.cfg.Log("conductor: cycle %d recorded receipt %s\n", cycle.Seq, cycle.ReceiptID)
	}

	// A cycle that overran the operator's own per-cycle ceiling parks the loop
	// rather than being noted and repeated. The ceiling is a statement about
	// what one cycle may cost, so the first cycle that breaks it is evidence
	// the next one will too.
	if over, reason := c.cfg.Ceilings.overrun(cycle); over {
		return c.park(c.cfg.Now(), reason)
	}
	return cycle, nil
}

// announce makes this cycle visible to the fleet and returns the row's id, or
// the empty id when there is no announcer or the catalog would not take it.
//
// The recipe is the first the assignment named, singular because a presence row
// is a status line rather than a receipt: the receipt records every recipe a
// cycle applied with its version, and this says what the cycle is for. No
// preparation id is announced, because there is none yet - the runner prepares
// the corpus, and a cycle announces before the runner has been called at all.
//
// The returned error is discarded rather than checked, and that is the contract
// rather than sloppiness: internal/presence routes every failure to its own
// diagnostic sink and returns an error only for a caller bug. A loop that
// stopped because it could not tell the fleet it was working would have
// inverted the whole point of the feature.
func (c *Conductor) announce(ctx context.Context, cycle Cycle) presence.PresenceID {
	if c.cfg.Presence == nil {
		return ""
	}
	var recipe string
	if len(cycle.Recipes) > 0 {
		recipe = cycle.Recipes[0]
	}
	id, _ := c.cfg.Presence.Announce(ctx, presence.Announcement{
		Kind:      presence.KindConductor,
		RunID:     cycle.RunID,
		Recipe:    recipe,
		Authority: cycle.Authority,
	})
	return id
}

// finalize records how the cycle ended, in the presence vocabulary rather than
// the journal's: OutcomeRan and OutcomeFailed are statements about the loop,
// and what the fleet asks is whether the work finished, failed, or was stopped.
//
// A cancelled cycle is finalized as cancelled rather than failed even though
// the journal records it as either, because everything the run committed before
// the cancellation is durable and rendering that as a failure would misreport
// the most common way a long cycle ends. The receipt id is attached whether or
// not it has published yet: it is the join a fleet reader follows from "this
// cycle finished" to what it produced.
func (c *Conductor) finalize(ctx context.Context, id presence.PresenceID, cycle Cycle) {
	if c.cfg.Presence == nil || id == "" {
		return
	}
	state := presence.StateFinished
	switch {
	case cycle.Cancelled:
		state = presence.StateCancelled
	case cycle.Outcome == OutcomeFailed:
		state = presence.StateFailed
	}
	_ = c.cfg.Presence.Finalize(ctx, id, presence.Outcome{
		State:           state,
		ReceiptRecordID: cycle.ReceiptID,
	})
}

// reconcile deals with whatever the last conductor left in flight. A cycle that
// was running when its process stopped existing is resumable: the assignment is
// in the journal and the run identity is what internal/explore resumes from, so
// replaying it amends that run's receipt chain instead of starting a second run
// over work an operator's invitation has already been spent on.
func (c *Conductor) reconcile(now time.Time) (Cycle, bool, error) {
	last, ok := c.cfg.Journal.Last()
	if !ok || last.Outcome != OutcomeRunning {
		return Cycle{}, false, nil
	}
	if last.RunID == "" {
		// Nothing to resume under: record the interruption and move on.
		last.Outcome = OutcomeInterrupted
		last.Reason = "the conductor stopped before the run had an identity"
		last.FinishedAt = now
		return Cycle{}, false, c.cfg.Journal.Record(last)
	}
	c.cfg.Log("conductor: resuming interrupted cycle %d as run %s\n", last.Seq, last.RunID)
	return last, true, nil
}

// refuseConcurrent refuses to start beside a conductor that is still working.
// Two loops sharing one journal would each read the other's cycles as their own
// history, and the floor, the ladder and the budget are all computed from that
// history.
func (c *Conductor) refuseConcurrent() error {
	last, ok := c.cfg.Journal.Last()
	if !ok || last.Outcome != OutcomeRunning || last.PID == 0 || last.PID == c.cfg.PID {
		return nil
	}
	if !processAlive(last.PID) {
		return nil
	}
	return fmt.Errorf("conductor: another conductor is running cycle %d as process %d",
		last.Seq, last.PID)
}

// draw walks the ladder. The floor is checked first, because it is a protected
// fraction rather than a last resort: a loop that only ever reached serendipity
// when nothing else was waiting would converge into pure dutifulness exactly
// when it was busiest, which is when the chaos is worth most.
func (c *Conductor) draw(ctx context.Context, d DrawRequest) (Assignment, error) {
	ladder := c.cfg.Ladder
	if c.floorIsDue() {
		floor := ladder[len(ladder)-1]
		a, err := floor.Draw(ctx, d)
		switch {
		case err == nil:
			a.Note = "the serendipity floor is due: " + a.Note
			return a, nil
		case errors.Is(err, ErrNoWork):
			// The floor could not draw. That is not a reason to skip it
			// silently: the dutiful rungs still get their turn below, and the
			// status view reports the floor's own emptiness.
		default:
			return Assignment{}, fmt.Errorf("conductor: draw from the %s rung: %w", floor.Name(), err)
		}
	}
	var empty []string
	for _, rung := range ladder {
		a, err := rung.Draw(ctx, d)
		switch {
		case err == nil:
			return a, nil
		case errors.Is(err, ErrNoWork):
			empty = append(empty, rung.Name())
		default:
			return Assignment{}, fmt.Errorf("conductor: draw from the %s rung: %w", rung.Name(), err)
		}
	}
	return Assignment{Note: joinNames(empty)}, nil
}

// floorIsDue reports whether the protected serendipity fraction requires this
// cycle to be chaotic. It counts the completed cycles since the last drawn
// serendipity run, so the guarantee survives a restart: the ratio is a property
// of the record, not of one process's memory.
func (c *Conductor) floorIsDue() bool {
	oneIn := c.cfg.Floor.oneIn()
	if oneIn <= 1 {
		return true
	}
	dutiful := 0
	for _, cycle := range c.cfg.Journal.Reverse() {
		if !cycle.counts() {
			continue
		}
		if cycle.Rung == RungSerendipity {
			break
		}
		dutiful++
		if dutiful >= oneIn-1 {
			return true
		}
	}
	return dutiful >= oneIn-1
}

// counts reports whether a journalled cycle is part of the floor's arithmetic.
// A parked or idle cycle drew no work at all, so counting it would let a quiet
// day satisfy the floor with runs that never happened.
func (c Cycle) counts() bool {
	switch c.Outcome {
	case OutcomeRan, OutcomeFailed, OutcomeRunning, OutcomeInterrupted:
		return c.Rung != ""
	default:
		return false
	}
}

func (c *Conductor) park(now time.Time, reason string) (Cycle, error) {
	cycle := Cycle{
		Seq:        c.cfg.Journal.NextSeq(),
		StartedAt:  now,
		FinishedAt: now,
		Outcome:    OutcomeParked,
		Reason:     reason,
		PID:        c.cfg.PID,
	}
	if err := c.cfg.Journal.Record(cycle); err != nil {
		return Cycle{}, err
	}
	c.cfg.Log("conductor: parked: %s\n", reason)
	return cycle, nil
}

func (c *Conductor) idle(now time.Time, reason string) (Cycle, error) {
	cycle := Cycle{
		Seq:        c.cfg.Journal.NextSeq(),
		StartedAt:  now,
		FinishedAt: now,
		Outcome:    OutcomeIdle,
		Reason:     reason,
		PID:        c.cfg.PID,
	}
	if err := c.cfg.Journal.Record(cycle); err != nil {
		return Cycle{}, err
	}
	c.cfg.Log("conductor: nothing to do: %s\n", reason)
	return cycle, nil
}

// newRunID mints a cycle's run identity on the same rule `babel explore` uses:
// derived from the clock so a listing of receipts sorts in the order the runs
// happened, and prefixed so a stray identifier in a log says what it is. The
// prefix says a conductor started it, which is the one thing a run id can
// usefully add to what the receipt's authority already records.
//
// The cycle number is part of it because a run identity is what resumption is
// named by: two cycles within one second sharing an identity would make the
// second one amend the first one's receipt chain and inherit its authority,
// which is precisely the confusion the authority field exists to prevent.
func newRunID(now time.Time, seq int) string {
	return fmt.Sprintf("run-cyc-%s-%d", now.UTC().Format("20060102T150405Z"), seq)
}

// processAlive reports whether a recorded conductor process still exists.
//
// The signal-zero probe is the portable-enough answer: on the platforms Babel
// runs analysis on it distinguishes a working loop from one that died holding
// the journal, and anywhere it is unsupported it reports "not alive", which
// degrades to treating an interrupted cycle as resumable — the safe direction,
// because resuming replays one run identity rather than duplicating work.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// StartOfDay is midnight UTC on the day t falls in, which is the window the
// per-day ceiling covers. UTC rather than local time so a machine that changes
// zone does not grant itself a second day's budget.
func StartOfDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return "no rung was consulted"
	case 1:
		return names[0] + " is empty"
	default:
		out := ""
		for i, n := range names {
			if i > 0 {
				out += ", "
			}
			out += n
		}
		return out + " are empty"
	}
}
