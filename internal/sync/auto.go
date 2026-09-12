package sync

import (
	"context"
	stdsync "sync"
	"time"
)

// This file is publication's *when*. Everything else in this package is its
// *how*, and the two were never the same problem.
//
// Retry already drains the whole debt: it seals every run proven over and then
// commits every declared closure the journal still holds. What it lacked on a
// bare workstation was a caller. The conductor calls it at each cycle
// boundary, an archive push calls it after reconcile, and `babel sync` calls
// it when an operator types the command - so a machine running `explore` and
// `evaluate` lanes with no conductor publishes only what each run's own
// CommitInline carries, and a run killed before it declared a closure carries
// nothing at all. Observed on 2026-09-12: 379 finished runs had declared no
// closure and 300 staged records waited for a person to notice.
//
// SPEC.md §9.1 requires every record to reach the shared catalog without an
// operator action. The missing piece was therefore a clock, not a protocol,
// and a clock is the whole of what a Drainer owns.

// Drainer runs a Publisher's drain on a schedule, and on nobody's command.
//
// It adds no step to publication and decides nothing about it: one attempt is
// exactly one Retry, with the same sealing, the same ordering and the same
// idempotence. What it owns is when an attempt happens, that two never happen
// at once, and that a failed one is a condition rather than an outcome - a
// machine whose catalog is unreachable keeps serving, keeps writing locally,
// and keeps owing the fleet exactly what it owed before.
type Drainer struct {
	pub *Publisher

	// report receives every attempt's Report, including the empty ones. What
	// is worth saying about one is the surface's decision rather than this
	// package's: a command that drains once as it exits and a server that
	// drains every minute speak differently about the same numbers, and a
	// package that chose for them would make one of the two wrong.
	report func(Report)

	// diag receives the one failure a Report cannot carry - a journal this
	// machine cannot read at all - on Options.Diag's terms: the value may
	// quote a store's words, so the command surface renders it.
	diag func(error)

	// attempt admits one drain at a time. This is correctness and not
	// tidiness: a Publisher writes the durable journal and is documented as
	// unsafe for concurrent use, so two overlapping attempts would be §9's
	// single-writer invariant broken by a timer rather than by a bug anyone
	// could see at a call site.
	attempt stdsync.Mutex
}

// NewDrainer returns the drainer a long-lived surface starts and a finishing
// command runs once, or nil when this deployment publishes nothing.
//
// A nil *Publisher is what a local-only deployment, a shared one with no
// catalog, and a shared one whose payload keys have not arrived all produce.
// None of them is an error and none of them owes the fleet anything a clock
// could carry, so the nil travels: every method below tolerates a nil
// receiver, and a caller needs no branch beyond the one it already writes for
// the publisher itself.
//
// Either sink may be nil, which drops what it would have received. That is the
// Publisher's own rule for Options.Diag and it is here for the same reason: a
// test asserts on the journal and the catalog rather than on a stream, and a
// caller that wants the line supplies the sink.
func NewDrainer(p *Publisher, report func(Report), diag func(error)) *Drainer {
	if p == nil {
		return nil
	}
	return &Drainer{pub: p, report: report, diag: diag}
}

// Drain performs one attempt: seal every run proven over, then publish every
// declared closure still owed.
//
// It is single-flight. A caller that finds an attempt already in flight
// returns immediately with the empty report rather than waiting, because the
// attempt in flight is already doing precisely this work - waiting for it
// would turn a slow catalog into a slow command, and joining it would put two
// writers on one journal. A skipped attempt reaches no sink: nothing was
// attempted, so there is nothing to say about it.
//
// Nothing here is a process error. Every closure that failed to publish is in
// the returned Report and has already reached the Publisher's own diagnostic
// sink; a journal that cannot be read at all reaches diag. An attempt that
// failed part-way still reports what it achieved, because a seal that happened
// is a fact regardless of what the publication after it could reach.
func (d *Drainer) Drain(ctx context.Context) Report {
	if d == nil {
		return Report{}
	}
	if !d.attempt.TryLock() {
		return Report{}
	}
	defer d.attempt.Unlock()

	rep, err := d.pub.Retry(ctx)
	if d.report != nil {
		d.report(rep)
	}
	// A cancelled attempt is the caller stopping, not a fault to narrate: a
	// surface shutting down mid-drain would otherwise sign off with a
	// diagnostic about the shutdown it just performed. What was owed is still
	// owed, still durable, and still visibly pending.
	if err != nil && d.diag != nil && ctx.Err() == nil {
		d.diag(err)
	}
	return rep
}

// Start drains now and then every interval until ctx ends, and returns the
// function that stops it.
//
// The first attempt is immediate because the debt is already there. A surface
// that waited out one interval before its first drain would hold what a dead
// run stranded for that long while being perfectly able to carry it.
//
// Attempts never overlap and never pile up. The loop drains on one goroutine
// and arms the next timer only once the drain has returned, so the interval is
// the gap between attempts rather than between their starts, and an attempt
// that outlives the interval delays the next one instead of racing it.
//
// stop cancels the attempt in flight and waits for it to return. The waiting
// is the point: the caller owns the journal handle and the catalog pool this
// drainer publishes through, and a stop that returned while an attempt was
// still writing would hand that caller a handle to close out from under a live
// transaction.
func (d *Drainer) Start(ctx context.Context, interval time.Duration) (stop func()) {
	if d == nil {
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		d.Drain(ctx)
		if interval <= 0 {
			// An interval that is not positive schedules nothing, and this is
			// the smallest honest reading of it: the one drain that was
			// certainly asked for happened. time.NewTicker's answer is a
			// panic, and a panic on this goroutine would take a serving
			// process down over a mistyped dial.
			return
		}
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			d.Drain(ctx)
			timer.Reset(interval)
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
