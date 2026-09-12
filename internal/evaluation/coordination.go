package evaluation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// This file owns three things that are really one thing: who is entitled to do
// a unit of review work, what that work is charged against, and how a result
// becomes final exactly once.
//
// The hard part is not the claim. It is the window between "my claim is still
// valid", "the allowance has been charged for what I spent" and "the record is
// durable here". Those are three commits against two different systems, and a
// check-then-write would be wrong in both directions: a crash after the charge
// loses the record, and a crash before it publishes a vote nobody paid for.
//
// So a completion is a settlement with a durable intent. The receipt that will
// be owed is written here, locally, before anyone is told anything; then the
// coordinator is told; then the record commits and the intent settles. Every
// interruption leaves the exact receipt that was owed, and re-driving it is
// idempotent because the coordinator accepts an identical receipt and refuses
// a superseded one. That is what makes "only the valid claim commits, and
// completed work survives resume" a property rather than a hope.

// Coordinator is the authority over claims, fences and the allowance.
//
// It is an interface because the authority is different in the two supported
// deployments and the store must not care which: a local-only instance is
// bounded by this machine's own ledger, and a shared deployment is bounded by
// the whole fleet's. What must never happen is the third thing - a shared
// deployment that could not reach coordination quietly falling back to local
// budgets - and the way this package prevents it is by not offering it:
// Open installs the local authority only when no coordinator was supplied at
// all, so a deployment that meant to be shared and failed to connect refuses
// to start rather than authorizing work nobody is accounting for.
//
// Fence is the authority in every method, not RunID. A resumed worker can
// present the right run id with a superseded fence, and must still be refused.
type Coordinator interface {
	// Claim grants one assignment, or refuses it. The returned assignment
	// carries the granted fence and expiry, which may differ from what was
	// asked for: the grant is the authority's answer, not the caller's
	// proposal echoed back.
	Claim(ctx context.Context, a Assignment, p Policy) (Assignment, error)
	// Validate reports whether this holder may still act. An expired lease
	// fails it, because the question it answers is "may I read and work now".
	Validate(ctx context.Context, id, runID string, fence int64) error
	// Renew extends the lease this holder is working under by the policy's
	// lease, measured from now, and reports the new expiry.
	//
	// Its authority test is Validate's rather than Finish's: renewal is
	// permission to keep working, so a lapsed lease and a takeover are both
	// refused. A worker that has already lost the claim must not be able to
	// take it back by asking for more time, because the next claimer may
	// hold it already.
	Renew(ctx context.Context, id, runID string, fence int64, p Policy) (time.Time, error)
	// Finish reconciles the reservation with what was actually spent.
	//
	// Its authority test is takeover, not expiry: a completion from the
	// still-current holder is accepted even if the lease lapsed while the
	// work ran, because that spend really happened and refusing it would
	// understate the allowance while throwing away finished work. Once
	// another holder has taken the claim, a late finish is a conflict.
	//
	// It is idempotent for an identical receipt and only for an identical
	// one: the same (run, fence, cost) again is accepted as the same event,
	// and a different cost under the same fence is a conflict rather than an
	// addition.
	Finish(ctx context.Context, id, runID string, fence int64, cost float64) error
}

// WithCoordinator attaches the fleet-wide coordinator.
//
// Passing it makes the fleet the authority over fences and the allowance, and
// this machine's ledger a mirror of the grants it was given: concurrent
// workers on several instances then charge one allowance instead of each
// treating the whole of it as theirs.
func WithCoordinator(c Coordinator) Option {
	return func(s *Store) {
		if c == nil {
			return
		}
		s.coord = &mirrored{shared: c, local: &localCoordinator{db: s.db, now: s.now}}
		s.shared = true
	}
}

// mirrored is the shared authority with a local mirror of its grants.
//
// The mirror exists because every local write path needs to know, without a
// round trip, which run holds an assignment and at which fence - Submit's
// first refusal of a stale worker, Expose's gate, the attempt history, the
// projection's view of what is in flight. It is a mirror and never a second
// opinion: it applies no budget of its own, because a local allowance applied
// on top of a fleet grant would refuse work the fleet had already authorized
// and paid for.
type mirrored struct {
	shared Coordinator
	local  *localCoordinator
}

func (m *mirrored) Claim(ctx context.Context, a Assignment, p Policy) (Assignment, error) {
	answer, err := m.shared.Claim(ctx, a, p)
	if err != nil {
		return Assignment{}, err
	}
	// The fleet's answer carries the fence, the expiry and the reservation and
	// has no column for the rest of the draw, so the grant is the caller's
	// assignment with those three replaced. Mirroring the bare answer would
	// write a local row with no seed, no input digest, no lane and no entity
	// names - and a replay would then have nothing to replay.
	granted := mergeGrant(a, answer)
	if err := m.local.adopt(ctx, granted); err != nil {
		return Assignment{}, err
	}
	return granted, nil
}

// mergeGrant keeps the caller's draw and takes only what the authority owns:
// the fence, the lease, the reservation and the moment of the grant.
//
// Everything else is the caller's, which is why the fleet's narrower ABI
// cannot blank it: the seed, the captured input digest, the lane, the recorded
// entity names and the statement a paid follow-up corrects all travel on the
// grant this machine records. It is also why a live re-claim compares the draw
// before it answers with a held fence - see sameDraw.
func mergeGrant(a, answer Assignment) Assignment {
	granted := a
	granted.Fence = answer.Fence
	granted.ExpiresAt = answer.ExpiresAt
	granted.ReservedCost = answer.ReservedCost
	if !answer.CreatedAt.IsZero() {
		granted.CreatedAt = answer.CreatedAt
	}
	return granted
}

func (m *mirrored) Validate(ctx context.Context, id, runID string, fence int64) error {
	// Local first, because a stale worker is refused without a round trip and
	// the local mirror already holds the fence a takeover advanced. The
	// shared check still runs: the mirror can be behind, and the fleet is the
	// authority.
	if err := m.local.Validate(ctx, id, runID, fence); err != nil {
		return err
	}
	return m.shared.Validate(ctx, id, runID, fence)
}

func (m *mirrored) Renew(ctx context.Context, id, runID string, fence int64, p Policy) (time.Time, error) {
	// The fleet goes first and its refusal is final, for Finish's reason
	// inverted: the authority that granted the lease is the only one that can
	// extend it, and a mirror extended on its own would tell a worker it may
	// keep reading after the fleet has already let its claim go.
	extended, err := m.shared.Renew(ctx, id, runID, fence, p)
	if err != nil {
		return time.Time{}, err
	}
	// The mirror then extends on its own clock, as adopt dates a grant on
	// its own clock: the local row exists so every local write path can
	// refuse a stale worker without a round trip, and a mirror still holding
	// the old expiry would refuse the holder the fleet has just extended. A
	// mirror this fails on is reported rather than swallowed - Validate asks
	// the mirror first, so an unextended mirror is an extension the worker
	// does not actually have.
	if _, err := m.local.Renew(ctx, id, runID, fence, p); err != nil {
		return time.Time{}, err
	}
	return extended, nil
}

func (m *mirrored) Finish(ctx context.Context, id, runID string, fence int64, cost float64) error {
	// The shared authority goes first and its refusal is final: the fleet
	// decides whether this holder is still the one, and a local stamp written
	// before that answer would be a spend nobody accounted for.
	answer := m.shared.Finish(ctx, id, runID, fence, cost)
	if answer != nil && !errors.Is(answer, ErrOverrun) {
		return answer
	}
	// An accounted overrun is a completed reconciliation wearing a warning,
	// not a refusal: the fleet charged the full cost, so the mirror has to
	// record the same receipt or a resumed worker would re-drive a spend the
	// fleet already holds. The mirror's own overrun is the same news and is
	// not reported twice, and its other failures are reportable only when the
	// fleet accepted cleanly - a mirror write that fails after the fleet has
	// charged the spend must not turn that spend into an unavailable
	// dependency holding a paid-for result back.
	if err := m.local.Finish(ctx, id, runID, fence, cost); err != nil &&
		answer == nil && !errors.Is(err, ErrOverrun) {
		return err
	}
	return answer
}

// localCoordinator is the claim authority for a local-only deployment, and the
// mirror of a shared one.
//
// Its rules are the shared ones, applied to this file: a fence that advances
// on every takeover, a lease with an expiry, a finish whose authority test is
// takeover rather than expiry, idempotence for an identical receipt only, and
// a day ledger in which an unfinished reservation stays charged. Local mode is
// not a relaxed mode - #219's acceptance requires equally strict local claims -
// it is the same contract with a smaller scope.
type localCoordinator struct {
	db  *sql.DB
	now func() time.Time
	// There is no "am I a mirror" flag: the mirror role is expressed by which
	// methods a mirrored coordinator calls - adopt rather than Claim - so a
	// budget can never be applied twice by a branch someone forgets to take.
}

// claimRow is one row of evaluation_claim.
type claimRow struct {
	assignment    Assignment
	day           string
	finishedAt    string
	finishedRun   string
	finishedFence int64
	finishedCost  float64
}

// Claim grants one assignment against this machine's ledger.
//
// The order matters. The day's allowance is pinned first, because a tightening
// has to survive a refusal; then the day's charge is read, and it includes
// every reservation that was never reconciled - expired or not - so an
// abandoned attempt is charged at what it reserved rather than assumed free;
// then the fence advances; then the new reservation is added on top of the
// superseded one. Two workers racing for one assignment produce one winner and
// one ErrConflict, because the whole grant is one IMMEDIATE transaction on a
// single-writer file.
//
// A live re-claim by the same run is the grant it already holds, returned
// unchanged. A retry after a lost answer must not advance the fence or reserve
// a second time for one assignment - that would spend the day's allowance on a
// dropped packet - so only an expired authority may be taken over.
func (l *localCoordinator) Claim(ctx context.Context, a Assignment, p Policy) (Assignment, error) {
	if err := a.validate(); err != nil {
		return Assignment{}, err
	}
	if p.LeaseSeconds <= 0 {
		return Assignment{}, fmt.Errorf("%w: policy %s grants no lease duration, so a claim could never expire",
			ErrInvalid, p.Version)
	}
	var (
		granted Assignment
		// A refusal is carried out of the transaction rather than returned
		// from it, because the pinned allowance must commit even when this
		// claim is denied: rolling the tightening back would leave a stale
		// policy spending an allowance the operator has already lowered.
		refused error
	)
	err := l.transact(ctx, func(tx *sql.Tx) error {
		// The clock is read inside the write transaction, after the immediate
		// lock is held. A moment sampled before the lock is a moment from
		// before whatever the writer ahead of this one committed, and an
		// expiry decided on it would take over a lease granted while this
		// call was waiting.
		now := l.now().UTC()
		day := dayOf(now)
		budget, err := pinBudgetDay(ctx, tx, day, p)
		if err != nil {
			return err
		}
		existing, found, err := readClaim(ctx, tx, a.ID)
		if err != nil {
			return err
		}
		fence := int64(1)
		if found {
			switch {
			case existing.assignment.Subject != a.Subject || existing.assignment.Role != a.Role:
				refused = fmt.Errorf("%w: assignment %s already names %s in the %s role",
					ErrConflict, a.ID, existing.assignment.Subject, existing.assignment.Role)
				return nil
			case existing.finishedAt != "":
				refused = fmt.Errorf("%w: assignment %s is already finished by run %q at fence %d",
					ErrConflict, a.ID, existing.finishedRun, existing.finishedFence)
				return nil
			case existing.assignment.ExpiresAt.After(now) && existing.assignment.RunID == a.RunID:
				if !sameDraw(existing.assignment, a) {
					// The same run holding the same live grant, presenting a
					// different draw. The answer to a retry is the caller's
					// own assignment with the authority's fields merged into
					// it (see mergeGrant), so returning the held fence here
					// would attach a paid-for reservation to replay inputs
					// nobody granted. That is a conflict, not a redelivery.
					refused = fmt.Errorf("%w: assignment %s is held at fence %d for a different draw, so a "+
						"retry must present the draw that was granted", ErrConflict, a.ID, existing.assignment.Fence)
					return nil
				}
				// The same worker asking again: it gets back the fence, the
				// reservation and the lease it already holds, and the ledger
				// is not touched.
				granted = mergeGrant(a, existing.assignment)
				return nil
			case existing.assignment.ExpiresAt.After(now):
				refused = fmt.Errorf("%w: assignment %s is held by run %q until %s",
					ErrConflict, a.ID, existing.assignment.RunID,
					existing.assignment.ExpiresAt.Format(time.RFC3339))
				return nil
			}
			fence = existing.assignment.Fence + 1
		}
		charged, cycle, err := dayCharge(ctx, tx, day, a.RunID)
		if err != nil {
			return err
		}
		if charged+a.ReservedCost > budget.daily {
			refused = fmt.Errorf("%w: %v is already committed today and assignment %s reserves %v, "+
				"over the %v daily allowance", ErrBudget, charged, a.ID, a.ReservedCost, budget.daily)
			return nil
		}
		// The per-cycle ceiling bounds one run's whole UTC day rather than one
		// assignment. A cycle allowed the full ceiling per assignment would
		// consume the allowance an assignment at a time while every single
		// claim looked lawful, which is the accounting #219 calls insufficient.
		if cycle+a.ReservedCost > budget.perCycle {
			refused = fmt.Errorf("%w: run %s has %v charged to %s and assignment %s reserves %v, "+
				"over the %v a cycle may spend",
				ErrBudget, a.RunID, cycle, day, a.ID, a.ReservedCost, budget.perCycle)
			return nil
		}
		granted = a
		granted.Fence = fence
		granted.CreatedAt = now
		granted.ExpiresAt = now.Add(time.Duration(p.LeaseSeconds) * time.Second)
		return writeClaim(ctx, tx, granted, day, now)
	})
	if err != nil {
		return Assignment{}, err
	}
	if refused != nil {
		return Assignment{}, refused
	}
	return granted, nil
}

// adopt records a grant this machine did not make, without applying a budget
// of its own. See mirrored.
func (l *localCoordinator) adopt(ctx context.Context, granted Assignment) error {
	if err := granted.validate(); err != nil {
		return err
	}
	if granted.Fence <= 0 {
		return fmt.Errorf("%w: assignment %s was granted without a fence", ErrInvalid, granted.ID)
	}
	return l.transact(ctx, func(tx *sql.Tx) error {
		// Sampled under the write lock for Claim's reason: it dates the
		// mirrored row, and a moment from before the lock could date it
		// earlier than a grant this machine has already recorded.
		now := l.now().UTC()
		existing, found, err := readClaim(ctx, tx, granted.ID)
		if err != nil {
			return err
		}
		if found && existing.assignment.Fence > granted.Fence {
			return fmt.Errorf("%w: assignment %s is already at fence %d locally, so the grant at fence %d "+
				"is superseded", ErrConflict, granted.ID, existing.assignment.Fence, granted.Fence)
		}
		// The draw is the mirror's own responsibility. The fleet's ABI has no
		// column for the seed, the captured inputs, the entity names or the
		// statement a paid follow-up corrects, so it cannot refuse a grant
		// that re-points one of them; re-adopting the same epoch under a
		// different draw would rewrite the durable record of what this
		// machine was authorized to do.
		if found && existing.assignment.Fence == granted.Fence &&
			(existing.assignment.Subject != granted.Subject || existing.assignment.Role != granted.Role ||
				!sameDraw(existing.assignment, granted)) {
			return fmt.Errorf("%w: assignment %s is already recorded at fence %d for a different draw",
				ErrConflict, granted.ID, existing.assignment.Fence)
		}
		return writeClaim(ctx, tx, granted, dayOf(granted.CreatedAt, now), now)
	})
}

// Validate reports whether this holder may still act: it is the current
// holder, at the current fence, and the lease has not lapsed.
//
// The one thing expiry does not refuse is this holder's own finished receipt.
// Completion is recorded before the record commits, so a worker re-driving its
// own settlement - or naming that completion from the correction that
// supersedes it - is the same authority asking about the same finished work,
// and refusing it would strand a result the allowance has already been charged
// for. An unfinished lapsed lease is still refused, because the next claimer
// may take it at any instant and two live opinions on one assignment is what
// the fence exists to prevent.
func (l *localCoordinator) Validate(ctx context.Context, id, runID string, fence int64) error {
	row, found, err := readClaimDB(ctx, l.db, id)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: assignment %q", ErrNotFound, id)
	}
	if row.finishedAt != "" {
		if row.finishedRun == runID && row.finishedFence == fence {
			return nil
		}
		return fmt.Errorf("%w: assignment %s was finished by run %q at fence %d",
			ErrConflict, id, row.finishedRun, row.finishedFence)
	}
	if row.assignment.RunID != runID || row.assignment.Fence != fence {
		return fmt.Errorf("%w: assignment %s is held by run %q at fence %d, not by %q at fence %d",
			ErrConflict, id, row.assignment.RunID, row.assignment.Fence, runID, fence)
	}
	if !row.assignment.ExpiresAt.After(l.now().UTC()) {
		return fmt.Errorf("%w: the lease on assignment %s expired at %s",
			ErrConflict, id, row.assignment.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}

// Renew extends a live claim's lease to one full policy lease from now.
//
// It exists because a lease is not a deadline for the work, it is a bound on
// how long an unanswered worker keeps its claim - and the two were being
// conflated. The four reviews this deployment lost on 2026-09-12 ran 386s to
// 630s under a 240s lease and every one of them died the same way: the claim
// lapsed while the corpus was being scanned, and the review context was then
// refused for a claim the worker had never stopped holding. A worker that is
// still answering says so by renewing, which is what distinguishes it from
// the crashed worker expiry exists to release.
//
// The refusals are Validate's and deliberately not Finish's. A finish from a
// lapsed holder is accepted because that spend really happened; an extension
// is permission to start something new, so an expired claim is refused rather
// than resurrected - the next claimer may have taken it already, and two live
// opinions on one assignment is what the fence exists to prevent. A takeover
// is refused at Validate's wording, and a finished claim has no lease left to
// extend.
//
// The expiry never moves backwards. A renewal is the holder keeping the
// authority it has, so a policy whose lease has since been shortened governs
// the next claim rather than cutting short a window already granted.
func (l *localCoordinator) Renew(ctx context.Context, id, runID string, fence int64,
	p Policy) (time.Time, error) {
	if p.LeaseSeconds <= 0 {
		return time.Time{}, fmt.Errorf("%w: policy %s grants no lease duration, so a claim could never expire",
			ErrInvalid, p.Version)
	}
	var extended time.Time
	err := l.transact(ctx, func(tx *sql.Tx) error {
		// Read under the write lock for Claim's reason: an expiry decided on
		// a moment sampled before the lock could extend a claim another
		// worker took over while this call was waiting.
		now := l.now().UTC()
		row, found, err := readClaim(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: assignment %q", ErrNotFound, id)
		}
		if row.finishedAt != "" {
			return fmt.Errorf("%w: assignment %s was finished by run %q at fence %d, so there is no "+
				"lease left to extend", ErrConflict, id, row.finishedRun, row.finishedFence)
		}
		if row.assignment.RunID != runID || row.assignment.Fence != fence {
			return fmt.Errorf("%w: assignment %s is held by run %q at fence %d, not by %q at fence %d",
				ErrConflict, id, row.assignment.RunID, row.assignment.Fence, runID, fence)
		}
		if !row.assignment.ExpiresAt.After(now) {
			return fmt.Errorf("%w: the lease on assignment %s expired at %s",
				ErrConflict, id, row.assignment.ExpiresAt.Format(time.RFC3339))
		}
		extended = now.Add(time.Duration(p.LeaseSeconds) * time.Second)
		if !extended.After(row.assignment.ExpiresAt) {
			extended = row.assignment.ExpiresAt
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE evaluation_claim SET expires_at = ?
			WHERE id = ? AND run_id = ? AND fence = ?`,
			formatTime(extended), id, runID, fence); err != nil {
			return fmt.Errorf("extend evaluation lease: %w", err)
		}
		return nil
	})
	if err != nil {
		return time.Time{}, err
	}
	return extended, nil
}

// Finish reconciles the reservation with what was spent.
//
// Expiry does not refuse it and takeover does. A holder whose lease lapsed
// while the model was still working really did spend that money and really did
// produce that work, so refusing the finish would both understate the
// allowance and discard a finished review; but once another holder has taken
// the claim, this one's result belongs to a superseded epoch and its spend is
// already charged under its own fence.
//
// A cost over the reservation is recorded in full and then reported as
// ErrOverrun. The day carries the real number so the next claim is judged
// against what was spent, and the identical retry reports the identical
// overrun: an overspend that stopped being reported on the second call would
// be an overspend a caller can retry its way out of.
func (l *localCoordinator) Finish(ctx context.Context, id, runID string, fence int64, cost float64) error {
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return fmt.Errorf("%w: a finish must report finite non-negative spend, got %v", ErrInvalid, cost)
	}
	// The overrun is carried out of the transaction rather than returned from
	// it: it is news about the allowance rather than a failed write, and
	// returning it from the closure would roll back the very charge it reports.
	var accounted error
	err := l.transact(ctx, func(tx *sql.Tx) error {
		now := l.now().UTC()
		row, found, err := readClaim(ctx, tx, id)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: assignment %q", ErrNotFound, id)
		}
		reserved, err := reservedFor(ctx, tx, id, fence)
		if err != nil {
			return err
		}
		if row.finishedAt != "" {
			if row.finishedRun == runID && row.finishedFence == fence && row.finishedCost == cost {
				accounted = overrunFor(id, fence, reserved, cost)
				return nil
			}
			return fmt.Errorf("%w: assignment %s was finished by run %q at fence %d for %v, "+
				"and an identical receipt is the only one accepted twice",
				ErrConflict, id, row.finishedRun, row.finishedFence, row.finishedCost)
		}
		if row.assignment.RunID != runID || row.assignment.Fence != fence {
			return fmt.Errorf("%w: assignment %s has been taken over by run %q at fence %d, "+
				"so the result from %q at fence %d is refused",
				ErrConflict, id, row.assignment.RunID, row.assignment.Fence, runID, fence)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE evaluation_claim
			SET finished_at = ?, finished_run = ?, finished_fence = ?, finished_cost = ?
			WHERE id = ?`, formatTime(now), runID, fence, cost, id); err != nil {
			return fmt.Errorf("record evaluation finish: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE evaluation_spend
			SET actual = ?, settled = 1 WHERE assignment_id = ? AND fence = ?`, cost, id, fence); err != nil {
			return fmt.Errorf("reconcile evaluation reservation: %w", err)
		}
		accounted = overrunFor(id, fence, reserved, cost)
		return nil
	})
	if err != nil {
		return err
	}
	return accounted
}

// reservedFor is what the ledger opened for one epoch of one assignment. An
// epoch the ledger never opened reserved nothing, so any spend at all is over
// it - which is the conservative direction and the only honest one.
func reservedFor(ctx context.Context, tx *sql.Tx, id string, fence int64) (float64, error) {
	var reserved float64
	err := tx.QueryRowContext(ctx, `SELECT reserved FROM evaluation_spend
		WHERE assignment_id = ? AND fence = ?`, id, fence).Scan(&reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read evaluation reservation for %s at fence %d: %w", id, fence, err)
	}
	return reserved, nil
}

// overrunFor reports spend that was charged in full and exceeded what its
// epoch reserved, and nil for spend that stayed inside it.
//
// It is separate from the statements that write because the first finish and
// the identical retry have to answer it the same way.
func overrunFor(id string, fence int64, reserved, observed float64) error {
	if observed <= reserved {
		return nil
	}
	return fmt.Errorf("%w: assignment %s at fence %d reserved %v and spent %v; the full amount is charged "+
		"to its day, so the next claim is judged against what was spent rather than against the reservation",
		ErrOverrun, id, fence, reserved, observed)
}

func (l *localCoordinator) transact(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// dayCharge is what the day already owes, in total and for one run: the
// reported cost of everything reconciled, plus the full reservation of
// everything that was not.
//
// The second half is the conservative accounting #219 asks for. A lease that
// expired without a receipt says nothing about what it spent - the worker may
// have burned the whole reservation before the machine died - so releasing it
// as zero would let one abandoned attempt authorize a second one for free, and
// a crash loop would spend the day's allowance many times over.
//
// The run total is what the per-cycle ceiling is measured against, and it is
// read from the same rows because a run's spend has to survive a takeover: the
// ledger keeps one row per (assignment, fence) carrying the run that opened
// it, so an abandoned attempt stays charged to the run that abandoned it
// rather than following the claim to its new holder.
func dayCharge(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, day, runID string) (total, cycle float64, err error) {
	const charge = `CASE WHEN settled = 1 THEN actual ELSE reserved END`
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(SUM(`+charge+`), 0),
		COALESCE(SUM(CASE WHEN run_id = ? THEN `+charge+` ELSE 0 END), 0)
		FROM evaluation_spend WHERE day = ?`, runID, day).Scan(&total, &cycle); err != nil {
		return 0, 0, fmt.Errorf("read evaluation spend for %s: %w", day, err)
	}
	return total, cycle, nil
}

// dayBudget is the allowance in force on one UTC day, as this machine holds it.
type dayBudget struct {
	daily    float64
	perCycle float64
}

// pinBudgetDay records the conservative allowance for one UTC day and reports
// what a claim on that day is judged against.
//
// Each ceiling is the minimum offered during the day, and the two move
// independently. Lowering either applies at once - including when the claim
// that offered it is then refused, which is why the caller commits this write
// on the refusal path as well - while a raise waits for the next UTC day,
// because within a day nothing here can tell an operator raising an allowance
// from a stale worker presenting the wider number it started with, and it must
// never be the second. A policy that lowers one ceiling and raises the other
// stays usable: it is judged against the conservative pair, and only its raise
// is ignored.
func pinBudgetDay(ctx context.Context, tx *sql.Tx, day string, p Policy) (dayBudget, error) {
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_budget_day(day, daily_cost, per_cycle_cost)
		VALUES(?, ?, ?) ON CONFLICT(day) DO NOTHING`, day, p.DailyCost, p.PerCycleCost); err != nil {
		return dayBudget{}, fmt.Errorf("open evaluation budget day %s: %w", day, err)
	}
	var pinned dayBudget
	if err := tx.QueryRowContext(ctx, `SELECT daily_cost, per_cycle_cost FROM evaluation_budget_day
		WHERE day = ?`, day).Scan(&pinned.daily, &pinned.perCycle); err != nil {
		return dayBudget{}, fmt.Errorf("read evaluation budget day %s: %w", day, err)
	}
	tightened := dayBudget{
		daily:    math.Min(pinned.daily, p.DailyCost),
		perCycle: math.Min(pinned.perCycle, p.PerCycleCost),
	}
	if tightened == pinned {
		return pinned, nil
	}
	// This can only ever lower a ceiling, so the schema's tighten-only
	// trigger cannot fire here. If it ever does, the minimum above is wrong
	// and the error says so rather than a wider allowance quietly taking hold.
	if _, err := tx.ExecContext(ctx, `UPDATE evaluation_budget_day
		SET daily_cost = ?, per_cycle_cost = ? WHERE day = ?`,
		tightened.daily, tightened.perCycle, day); err != nil {
		return dayBudget{}, fmt.Errorf("tighten evaluation budget day %s: %w", day, err)
	}
	return tightened, nil
}

// dayOf names the budget day a timestamp falls in, preferring the first
// non-zero of the instants it is given.
//
// A day is a UTC date rather than a local one, because two instances in
// different time zones charging "today" against one allowance would otherwise
// disagree about when the allowance resets.
func dayOf(instants ...time.Time) string {
	for _, instant := range instants {
		if !instant.IsZero() {
			return instant.UTC().Format("2006-01-02")
		}
	}
	return time.Time{}.UTC().Format("2006-01-02")
}

// writeClaim upserts the claim row and opens the reservation for its fence.
//
// The reservation is inserted rather than replaced, because the superseded
// attempt's reservation must stay charged: a takeover reserves again on top of
// what the abandoned attempt still owes. Each reservation carries the run that
// opened it, so a takeover moves the claim without moving the abandoned
// attempt's spend off the run that owes it.
func writeClaim(ctx context.Context, tx *sql.Tx, a Assignment, day string, now time.Time) error {
	// The entity names travel as JSON in one column rather than as a join
	// table: they are a list the grant carries, nothing joins on them, and a
	// row per name would be a second place the grant could disagree with
	// itself.
	subjects, err := json.Marshal(a.Subjects)
	if err != nil {
		return fmt.Errorf("encode assignment subjects: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_claim(
		id, subject_kind, subject_id, run_id, role, policy_version, context_version, seed, input_digest,
		lane, subjects_json, corrects_id, fence, reserved_cost, day, created_at, expires_at, finished_at,
		finished_run, finished_fence, finished_cost)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', '', 0, 0)
		ON CONFLICT(id) DO UPDATE SET
			run_id = excluded.run_id,
			policy_version = excluded.policy_version,
			context_version = excluded.context_version,
			seed = excluded.seed,
			input_digest = excluded.input_digest,
			lane = excluded.lane,
			subjects_json = excluded.subjects_json,
			corrects_id = excluded.corrects_id,
			fence = excluded.fence,
			reserved_cost = excluded.reserved_cost,
			day = excluded.day,
			expires_at = excluded.expires_at`,
		a.ID, a.Subject.Kind, a.Subject.ID, a.RunID, a.Role, a.PolicyVersion, a.ContextVersion,
		formatSeed(a.Seed), a.InputDigest, a.Lane, subjects, a.Corrects, a.Fence, a.ReservedCost, day,
		formatTime(a.CreatedAt), formatTime(a.ExpiresAt)); err != nil {
		return fmt.Errorf("record evaluation claim: %w", err)
	}
	id, err := newID("evs")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO evaluation_spend(
		id, day, assignment_id, run_id, fence, lane, reserved, actual, settled, recorded_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, 0, 0, ?)`,
		id, day, a.ID, a.RunID, a.Fence, a.Lane, a.ReservedCost, formatTime(now)); err != nil {
		return fmt.Errorf("reserve evaluation spend: %w", err)
	}
	return nil
}

const claimSelect = `SELECT id, subject_kind, subject_id, run_id, role, policy_version, context_version,
	seed, input_digest, lane, subjects_json, corrects_id, fence, reserved_cost, day, created_at, expires_at,
	finished_at, finished_run, finished_fence, finished_cost FROM evaluation_claim`

func scanClaim(row interface{ Scan(...any) error }) (claimRow, error) {
	var (
		out              claimRow
		seed             string
		subjects         []byte
		created, expires string
	)
	if err := row.Scan(&out.assignment.ID, &out.assignment.Subject.Kind, &out.assignment.Subject.ID,
		&out.assignment.RunID, &out.assignment.Role, &out.assignment.PolicyVersion,
		&out.assignment.ContextVersion, &seed, &out.assignment.InputDigest, &out.assignment.Lane,
		&subjects, &out.assignment.Corrects, &out.assignment.Fence, &out.assignment.ReservedCost,
		&out.day, &created, &expires,
		&out.finishedAt, &out.finishedRun, &out.finishedFence, &out.finishedCost); err != nil {
		return claimRow{}, err
	}
	parsedSeed, err := parseSeed(seed)
	if err != nil {
		return claimRow{}, err
	}
	out.assignment.Seed = parsedSeed
	if len(subjects) > 0 {
		if err := json.Unmarshal(subjects, &out.assignment.Subjects); err != nil {
			return claimRow{}, fmt.Errorf("decode assignment subjects: %w", err)
		}
	}
	if out.assignment.CreatedAt, err = parseTime(created); err != nil {
		return claimRow{}, err
	}
	if out.assignment.ExpiresAt, err = parseTime(expires); err != nil {
		return claimRow{}, err
	}
	return out, nil
}

// sameDraw reports whether a presented assignment is the draw the held grant
// was actually made from.
//
// It exists for the live re-claim: everything compared here is what a replay
// needs and what a correction's authority rests on - the policy and context the
// draw ran under, the seed, the digest of the captured inputs, the recorded
// entity names, and the statement a paid follow-up corrects - and none of it is
// stored twice. The identity of an assignment is its id, so a caller that
// changed one of these and kept the id is describing different work; answering
// it with the held fence would hand it a reservation granted for something
// else. Subject and role are checked before this, because they are refused for
// every caller rather than only for the holder.
func sameDraw(held, presented Assignment) bool {
	return held.PolicyVersion == presented.PolicyVersion &&
		held.ContextVersion == presented.ContextVersion &&
		held.Seed == presented.Seed &&
		held.InputDigest == presented.InputDigest &&
		held.Corrects == presented.Corrects &&
		slices.Equal(held.Subjects, presented.Subjects)
}

func readClaim(ctx context.Context, tx *sql.Tx, id string) (claimRow, bool, error) {
	row, err := scanClaim(tx.QueryRowContext(ctx, claimSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return claimRow{}, false, nil
	}
	if err != nil {
		return claimRow{}, false, fmt.Errorf("read evaluation claim %s: %w", id, err)
	}
	return row, true, nil
}

func readClaimDB(ctx context.Context, db *sql.DB, id string) (claimRow, bool, error) {
	row, err := scanClaim(db.QueryRowContext(ctx, claimSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return claimRow{}, false, nil
	}
	if err != nil {
		return claimRow{}, false, fmt.Errorf("read evaluation claim %s: %w", id, err)
	}
	return row, true, nil
}

// Claim grants one bounded unit of review attention and publishes the grant.
//
// The subject is resolved before anything is granted, through the same
// resolver a remote subject uses: an assignment against a revision nothing can
// read is not a gap in coverage, it is a grant that could never be completed.
//
// A disabled policy grants nothing. §5.8 is explicit that no budget is
// increased and no compute launched merely because a backlog exists, so an
// unauthorized policy is ErrNoWork rather than a claim nobody asked for.
//
// The grant is published for the reason attempts are: a second instance that
// saw only completed assessments could not tell an assignment in flight from
// one that never existed, and coverage would silently mean "somebody published
// a vote".
func (s *Store) Claim(ctx context.Context, a Assignment, p Policy) (Assignment, error) {
	if err := a.validate(); err != nil {
		return Assignment{}, err
	}
	if err := ValidatePolicy(p); err != nil {
		return Assignment{}, err
	}
	if a.PolicyVersion != p.Version {
		return Assignment{}, fmt.Errorf("%w: assignment %s was drawn under policy %q but %q was supplied, "+
			"so the draw could not be replayed", ErrInvalid, a.ID, a.PolicyVersion, p.Version)
	}
	if !p.Enabled {
		return Assignment{}, fmt.Errorf("%w: evaluation policy %s is not enabled, so no attention is authorized",
			ErrNoWork, p.Version)
	}
	artifact, err := s.artifact(ctx, a.Subject)
	if err != nil {
		return Assignment{}, err
	}
	if a.ContextVersion == "" {
		a.ContextVersion = artifact.ContextVersion
	}
	answer, err := s.coord.Claim(ctx, a, p)
	if err != nil {
		return Assignment{}, err
	}
	if answer.Fence <= 0 {
		return Assignment{}, fmt.Errorf("%w: assignment %s was granted without a fence, so a stale worker "+
			"could not be refused", ErrInvalid, a.ID)
	}
	if answer.ID != "" && answer.ID != a.ID {
		return Assignment{}, fmt.Errorf("%w: a claim for assignment %s was answered with %s",
			ErrConflict, a.ID, answer.ID)
	}
	if answer.RunID != "" && answer.RunID != a.RunID {
		return Assignment{}, fmt.Errorf("%w: assignment %s was granted to run %q, not to %q",
			ErrConflict, a.ID, answer.RunID, a.RunID)
	}
	// The grant is the draw the caller made, with exactly the four values the
	// authority owns taken from its answer.
	//
	// Merging rather than adopting the answer wholesale is deliberate. The
	// fleet coordinator's ABI carries the fence, the expiry and the
	// reservation and nothing else - it has no column for a seed, a captured
	// input digest, a lane or the recorded entity names - so an adapter that
	// returned its own row as an Assignment would silently blank exactly the
	// fields a replay and a Reality admission need. Losing them would be
	// invisible until someone tried to replay a draw.
	granted := mergeGrant(a, answer)
	if granted.CreatedAt.IsZero() {
		granted.CreatedAt = s.now()
	}
	if granted.ExpiresAt.IsZero() {
		return Assignment{}, fmt.Errorf("%w: assignment %s was granted without an expiry, so an abandoned "+
			"claim could never be taken over", ErrInvalid, a.ID)
	}
	id, err := newID("evr")
	if err != nil {
		return Assignment{}, err
	}
	grant := granted
	record := Record{
		ID:           id,
		Kind:         KindAssignment,
		Subject:      granted.Subject,
		AssignmentID: granted.ID,
		ActorKind:    ActorRun,
		ActorID:      granted.RunID,
		CreatedAt:    granted.CreatedAt,
		Provenance:   Provenance{RunID: granted.RunID, ContextVersion: granted.ContextVersion},
		Assignment:   &grant,
	}
	var pub publication
	err = s.transact(ctx, func(tx *sql.Tx) error {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM evaluation_record
			WHERE kind = ? AND assignment_id = ? AND fence = ?`,
			KindAssignment, granted.ID, granted.Fence).Scan(&found); err != nil {
			return fmt.Errorf("read assignment record: %w", err)
		}
		if found > 0 {
			return nil
		}
		var err error
		pub, err = s.writeRecord(ctx, tx, recordWrite{
			record:     record,
			role:       granted.Role,
			fence:      granted.Fence,
			readHead:   artifact.HeadID,
			producedBy: granted.RunID,
		})
		return err
	})
	if err != nil {
		return Assignment{}, err
	}
	if err := s.commit(ctx, pub); err != nil {
		return Assignment{}, err
	}
	return granted, nil
}

// Assignment reads one claim as this instance holds it.
func (s *Store) Assignment(ctx context.Context, id string) (Assignment, error) {
	row, found, err := readClaimDB(ctx, s.db, id)
	if err != nil {
		return Assignment{}, err
	}
	if !found {
		return Assignment{}, fmt.Errorf("%w: assignment %q", ErrNotFound, id)
	}
	return row.assignment, nil
}

// Assignments reports every claim this instance holds or held, newest last.
func (s *Store) Assignments(ctx context.Context) ([]Assignment, error) {
	rows, err := s.db.QueryContext(ctx, claimSelect+` ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("read evaluation claims: %w", err)
	}
	defer rows.Close()
	var assignments []Assignment
	for rows.Next() {
		row, err := scanClaim(rows)
		if err != nil {
			return nil, fmt.Errorf("scan evaluation claim: %w", err)
		}
		assignments = append(assignments, row.assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read evaluation claims: %w", err)
	}
	return assignments, nil
}

// ValidateClaim reports whether a holder may still act on an assignment.
//
// In shared mode both authorities are asked and both must agree. That is not
// belt and braces: the local mirror can be behind a takeover that happened on
// another instance, and the shared coordinator cannot see a local grant that
// was never mirrored, so a caller that asked only one of them would sometimes
// be told yes by the one that did not know.
func (s *Store) ValidateClaim(ctx context.Context, id, runID string, fence int64) error {
	return s.coord.Validate(ctx, id, runID, fence)
}

// RenewClaim extends the lease on a claim whose holder is still working, and
// reports the new expiry.
//
// Nothing is published for a renewal. The attempt journal records what became
// of the work - exposed, completed, skipped, failed - and "the worker is still
// alive" is not one of those; a record per renewal would be a durable row per
// few minutes of every review, saying only that a clock was still ticking.
// The claim row carries the current lease, which is what every authority check
// reads.
func (s *Store) RenewClaim(ctx context.Context, id, runID string, fence int64, p Policy) (time.Time, error) {
	return s.coord.Renew(ctx, id, runID, fence, p)
}

// settlement is one completion in flight: the receipt that is owed, and the
// ids the record and attempt will have when it lands.
//
// The ids are minted before anything is told to anyone, which is what makes a
// re-driven settlement produce the identical record rather than a second one
// with a new identity. A published record is content-addressed and nothing
// ever deletes it, so "the same result twice under two ids" is not a tidiness
// problem; it is two votes.
type settlement struct {
	id              string
	state           string
	attemptState    string
	digest          string
	cost            float64
	recordID        string
	attemptRecordID string
	readHead        string
	createdAt       time.Time
	submission      Submission
}

// The settlement states. A pending settlement is a debt this instance owes
// itself; the other two are terminal.
const (
	settlementPending = "pending"
	settlementSettled = "settled"
	settlementFenced  = "fenced"
)

// settle drives one completion to a terminal state.
//
// The sequence is the reason this package can claim exactly-once completion
// across a crash:
//
//  1. The receipt is written locally, before anyone is told. A crash here
//     leaves a pending debt with the exact cost and ids that were owed.
//  2. The coordinator is told, and its answer is authoritative. A takeover
//     makes this a fenced settlement: no record, no vote, and the spend is
//     already charged under the epoch that reserved it. A transient failure
//     leaves the debt pending and returns ErrUnavailable, because a result
//     whose spend nobody has accounted for must not become a published vote.
//  3. The record commits locally and the debt settles, in one transaction
//     with the staging that owes it to the fleet.
//
// A retry of the identical submission short-circuits at step 1 by finding its
// own receipt: settled returns the same record, pending re-drives from step 2,
// fenced refuses. A different result under the same fence is a conflict rather
// than a second statement.
func (s *Store) settle(ctx context.Context, claim Assignment, in Submission, state, readHead string) (Record, error) {
	digest, err := digestSubmission(in)
	if err != nil {
		return Record{}, err
	}
	owed, err := s.openSettlement(ctx, claim, in, state, digest, readHead)
	if err != nil {
		return Record{}, err
	}
	switch owed.state {
	case settlementSettled:
		if owed.recordID == "" {
			// A skip or a failure: journalled, reconciled, and deliberately
			// without a statement to return.
			return Record{}, nil
		}
		return s.Record(ctx, owed.recordID)
	case settlementFenced:
		return Record{}, fmt.Errorf("%w: the result for assignment %s was refused by coordination and "+
			"cannot be recorded", ErrConflict, claim.ID)
	}
	return s.driveSettlement(ctx, claim, owed)
}

// openSettlement writes the receipt this completion owes, or returns the one
// it already owes.
func (s *Store) openSettlement(ctx context.Context, claim Assignment, in Submission,
	state, digest, readHead string) (settlement, error) {
	owed := settlement{
		state:        settlementPending,
		attemptState: state,
		digest:       digest,
		cost:         in.Cost,
		readHead:     readHead,
		createdAt:    s.now(),
		submission:   in,
	}
	err := s.transact(ctx, func(tx *sql.Tx) error {
		existing, found, err := readSettlement(ctx, tx, claim.ID, in.Fence)
		if err != nil {
			return err
		}
		if found {
			if existing.digest != digest {
				return fmt.Errorf("%w: a different result for assignment %s was already submitted at "+
					"fence %d; append a correction instead", ErrConflict, claim.ID, in.Fence)
			}
			owed = existing
			return nil
		}
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode submission: %w", err)
		}
		if owed.id, err = newID("evt"); err != nil {
			return err
		}
		if state == AttemptCompleted {
			if owed.recordID, err = newID("evr"); err != nil {
				return err
			}
		}
		if owed.attemptRecordID, err = newID("evr"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO evaluation_settlement(
			id, assignment_id, run_id, fence, state, attempt_state, digest, cost, record_id,
			attempt_record_id, read_head_id, reason, created_at, settled_at, submission_json)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, '', ?)`,
			owed.id, claim.ID, in.RunID, in.Fence, settlementPending, state, digest, in.Cost,
			owed.recordID, owed.attemptRecordID, readHead, formatTime(owed.createdAt), encoded); err != nil {
			return fmt.Errorf("record evaluation settlement: %w", err)
		}
		return nil
	})
	if err != nil {
		return settlement{}, err
	}
	return owed, nil
}

// driveSettlement performs steps 2 and 3 of settle for one owed receipt.
func (s *Store) driveSettlement(ctx context.Context, claim Assignment, owed settlement) (Record, error) {
	in := owed.submission
	// Finish is the gate and Validate deliberately is not. Validate refuses
	// an expired lease, which is right for "may I read and work" and wrong
	// here: a model that finished while the lease lapsed really did the work
	// and really spent the money, and Finish's own authority test - takeover -
	// is the one that decides whether this holder is still the owner.
	overrun := ""
	if err := s.coord.Finish(ctx, claim.ID, in.RunID, in.Fence, in.Cost); err != nil {
		switch {
		case errors.Is(err, ErrConflict):
			if markErr := s.markSettlement(ctx, owed.id, settlementFenced, err.Error()); markErr != nil {
				return Record{}, markErr
			}
			return Record{}, err
		case errors.Is(err, ErrOverrun):
			// The spend was accounted in full and the allowance is now over
			// its ceiling. That is news about admission, not about this
			// result: the charge is already recorded, so discarding the work
			// would leave the cost standing with nothing to show for it. The
			// record lands, the overrun is kept on the receipt so an operator
			// can see which completion crossed the line, and the refusal
			// arrives at the next Claim - which is where admission is
			// decided.
			overrun = err.Error()
		default:
			return Record{}, fmt.Errorf("%w: the spend for assignment %s could not be reconciled, so its "+
				"result is held rather than published: %w", ErrUnavailable, claim.ID, err)
		}
	}
	var (
		record Record
		pubs   []publication
	)
	err := s.transact(ctx, func(tx *sql.Tx) error {
		attempt := Attempt{
			AssignmentID: claim.ID,
			State:        owed.attemptState,
			Reason:       attemptReason(in),
			Cost:         in.Cost,
			Unpriced:     in.Unpriced,
			RecordedAt:   owed.createdAt,
		}
		if owed.attemptState == AttemptCompleted {
			// One grant carries one active statement. A grant claimed to
			// correct an earlier record produces a superseding statement
			// instead, so the check is which of the two this is: a second
			// original under one assignment is the conflict, and a second
			// correction of one statement is refused by the partial unique
			// index the chain depends on.
			var active int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(1) FROM evaluation_record
				WHERE kind = ? AND assignment_id = ? AND supersedes_id = ''`,
				KindAssessment, claim.ID).Scan(&active); err != nil {
				return fmt.Errorf("read active statement: %w", err)
			}
			if active > 0 && claim.Corrects == "" {
				return fmt.Errorf("%w: assignment %s already carries an active statement; "+
					"append a correction instead", ErrConflict, claim.ID)
			}
			if claim.Corrects != "" {
				var superseded int
				if err := tx.QueryRowContext(ctx,
					`SELECT COUNT(1) FROM evaluation_record WHERE supersedes_id = ?`,
					claim.Corrects).Scan(&superseded); err != nil {
					return fmt.Errorf("read correction chain: %w", err)
				}
				if superseded > 0 {
					return fmt.Errorf("%w: record %s has already been corrected; "+
						"correct the active statement", ErrConflict, claim.Corrects)
				}
			}
			assessment := *in.Assessment
			assessment.ContextVersion = claim.ContextVersion
			record = Record{
				ID:           owed.recordID,
				Kind:         KindAssessment,
				Subject:      claim.Subject,
				AssignmentID: claim.ID,
				SupersedesID: claim.Corrects,
				ActorKind:    ActorRun,
				ActorID:      in.RunID,
				CreatedAt:    owed.createdAt,
				Provenance:   s.provenance(claim, in),
				Assessment:   &assessment,
			}
			pub, err := s.writeRecord(ctx, tx, recordWrite{
				record:     record,
				role:       claim.Role,
				fence:      claim.Fence,
				readHead:   owed.readHead,
				producedBy: in.RunID,
			})
			if err != nil {
				return err
			}
			pubs = append(pubs, pub)
		}
		inserted, err := s.appendAttempt(ctx, tx, claim, attempt, in.Fence, in.RunID)
		if err != nil {
			return err
		}
		if inserted {
			pub, err := s.attemptRecord(ctx, tx, owed.attemptRecordID, claim, attempt, in.Fence, in.RunID)
			if err != nil {
				return err
			}
			pubs = append(pubs, pub)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE evaluation_settlement
			SET state = ?, reason = ?, settled_at = ? WHERE id = ?`,
			settlementSettled, overrun, formatTime(s.now()), owed.id); err != nil {
			return fmt.Errorf("settle evaluation receipt: %w", err)
		}
		return nil
	})
	if err != nil {
		return Record{}, err
	}
	for _, pub := range pubs {
		if err := s.commit(ctx, pub); err != nil {
			return Record{}, err
		}
	}
	if owed.attemptState != AttemptCompleted {
		return Record{}, nil
	}
	return record, nil
}

// attemptReason is what the attempt history records about why a completion
// looks the way it does. A skip's and a failure's reason are the whole point
// of the row - repeated skips and unsupported sources have to stay visible as
// gaps - and a completed assessment's reason is in the assessment.
func attemptReason(in Submission) string {
	if in.SkipReason != "" {
		return in.SkipReason
	}
	return in.FailedReason
}

// markSettlement moves one receipt to a terminal state with the reason it got
// there. A fenced receipt is kept rather than deleted: it is the durable record
// that a stale worker's result arrived and was refused, which is exactly what
// an operator asking "where did that review go" needs to see.
func (s *Store) markSettlement(ctx context.Context, id, state, reason string) error {
	return s.transact(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE evaluation_settlement
			SET state = ?, reason = ?, settled_at = ? WHERE id = ?`,
			state, reason, formatTime(s.now()), id); err != nil {
			return fmt.Errorf("mark evaluation settlement: %w", err)
		}
		return nil
	})
}

// Recover drives every completion this instance still owes to a terminal
// state, and reports how many it settled.
//
// It is the resume path. A worker killed between reconciling its spend and
// committing its record leaves a pending receipt; re-driving it is safe
// because the coordinator accepts the identical receipt and the record ids
// were minted before the crash, so the result lands exactly once with the
// identity it always had. A receipt whose claim has since been taken over
// becomes fenced and publishes nothing.
//
// Transient failures are left pending on purpose and reported as a count of
// what did settle: an unreachable coordinator is Tuesday, and the debt is
// still owed afterwards.
func (s *Store) Recover(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, settlementSelect+` WHERE state = ? ORDER BY created_at, id`,
		settlementPending)
	if err != nil {
		return 0, fmt.Errorf("read pending evaluation settlements: %w", err)
	}
	var pending []settlement
	for rows.Next() {
		owed, err := scanSettlement(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, owed)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read pending evaluation settlements: %w", err)
	}
	// The cursor is closed before any transaction opens: a durable handle has
	// one SQLite connection, and a write while a read cursor is open would
	// deadlock against itself.
	rows.Close()
	settled := 0
	for _, owed := range pending {
		claim, err := s.Assignment(ctx, owed.submission.AssignmentID)
		if err != nil {
			return settled, err
		}
		if _, err := s.driveSettlement(ctx, claim, owed); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrUnavailable) {
				continue
			}
			return settled, err
		}
		settled++
	}
	return settled, nil
}

const settlementSelect = `SELECT id, state, attempt_state, digest, cost, record_id, attempt_record_id,
	read_head_id, created_at, submission_json FROM evaluation_settlement`

func scanSettlement(row interface{ Scan(...any) error }) (settlement, error) {
	var (
		owed    settlement
		created string
		encoded []byte
	)
	if err := row.Scan(&owed.id, &owed.state, &owed.attemptState, &owed.digest, &owed.cost,
		&owed.recordID, &owed.attemptRecordID, &owed.readHead, &created, &encoded); err != nil {
		return settlement{}, err
	}
	at, err := parseTime(created)
	if err != nil {
		return settlement{}, err
	}
	owed.createdAt = at
	if err := json.Unmarshal(encoded, &owed.submission); err != nil {
		return settlement{}, fmt.Errorf("%w: decode owed submission %s: %w", ErrInvalid, owed.id, err)
	}
	return owed, nil
}

func readSettlement(ctx context.Context, tx *sql.Tx, assignmentID string, fence int64) (settlement, bool, error) {
	owed, err := scanSettlement(tx.QueryRowContext(ctx,
		settlementSelect+` WHERE assignment_id = ? AND fence = ?`, assignmentID, fence))
	if errors.Is(err, sql.ErrNoRows) {
		return settlement{}, false, nil
	}
	if err != nil {
		return settlement{}, false, fmt.Errorf("read evaluation settlement for %s: %w", assignmentID, err)
	}
	return owed, true, nil
}
