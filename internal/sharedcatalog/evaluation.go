package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// This file is the fleet's half of evaluation work (SPEC.md 4.12, 5.8; issue
// #219): who is allowed to review what, until when, and against which
// allowance. The records those reviews produce are ordinary Phase B output
// under KindEvaluation and travel through sync.go like every other kind; what
// is here is only the coordination that has to be deployment-wide to mean
// anything.
//
// It exists in this package rather than in internal/evaluation because the
// question it answers is a database question. Two workers on two machines with
// no local knowledge of each other must not both believe they own one
// assignment, and a fleet spending one authorized daily budget cannot have each
// host measure that budget against its own local receipts - which is exactly
// what the conductor's existing per-host accounting does, and why the lifecycle
// plan calls it insufficient for a fleet-wide allowance. Nothing here imports
// internal/evaluation, and it deliberately holds no opinion about what an
// evaluation says: a claim is authority and money, never a judgement.
//
// ClaimEvaluation admits work within the deployment allowance. Both admission
// and completion check schema compatibility before writing.
// ValidateEvaluationClaim asks whether a worker may still act - it is the
// stricter test, and it refuses an expired lease. FinishEvaluationClaim records
// what was spent, and its authority test is takeover rather than expiry: money
// that was already spent must be recorded even if the lease lapsed while the
// model was working, because the only alternative is a fleet that understates
// its own spending. A worker whose claim was taken over is refused by both.

// EvaluationDayLayout renders the UTC day an evaluation claim is charged to.
// It is exported because EvaluationClaim.Day is a string and a caller that
// wants to group its own reporting by day should not have to guess the shape.
const EvaluationDayLayout = "2006-01-02"

// maxEvaluationLeaseSeconds bounds how long one assignment may be held.
//
// A lease longer than a day would outlive the budget day it was reserved
// against, so its unobserved spend would sit on a day nobody is claiming
// against any more while the assignment stayed unclaimable. The ceiling is a
// day exactly: it is the longest lease that can still be reconciled within the
// accounting period that authorized it.
const maxEvaluationLeaseSeconds = 24 * 60 * 60

var (
	// ErrEvaluationInvalid reports a claim or budget the catalog will not even
	// consider: a malformed identifier, a subject kind outside the closed
	// vocabulary, a cost that is negative or not a number, a lease that is not
	// a positive bounded duration, or a deployment nothing has registered.
	// It is a caller bug rather than contention.
	ErrEvaluationInvalid = errors.New("invalid evaluation claim")

	// ErrEvaluationNotFound reports an assignment id the deployment does not
	// hold at all. It is distinct from ErrEvaluationConflict on purpose: a
	// worker validating an id nobody ever claimed has a different problem from
	// one whose claim was taken over.
	ErrEvaluationNotFound = errors.New("evaluation claim not found")

	// ErrEvaluationConflict reports that this caller is not the authority it
	// believes it is, or that it is contradicting one. It covers a live claim
	// held by another owner, a stale fence or run id after a takeover, an
	// expired lease at validation time, an assignment already completed, a
	// second finish reporting a different cost.
	ErrEvaluationConflict = errors.New("evaluation claim conflict")

	// ErrEvaluationBudget reports that the work would exceed the allowance in
	// force - the deployment's day or this run's cycle. It is a normal answer
	// rather than a failure: the selector asks, is refused, and stops.
	ErrEvaluationBudget = errors.New("evaluation budget exhausted")

	// ErrEvaluationOverrun reports an attempt that finished having spent more
	// than it reserved, which a provider can do to a worker that asked for
	// less. It is a budget condition, and errors.Is matches
	// ErrEvaluationBudget, so a caller with a coarse error map still
	// classifies it correctly.
	//
	// It is the one error in this file that is returned after the write
	// succeeded. The overrun is recorded in full before it is reported,
	// because the ledger's job is to say what was actually spent: refusing to
	// store it would leave the day charged at the reservation and call a real
	// overspend within budget. A caller that treats this as a failed finish
	// and retries with the identical receipt gets the identical answer, and
	// nothing is charged twice. What it must not do is publish the result as
	// if the attempt stayed inside its allowance.
	ErrEvaluationOverrun = fmt.Errorf("%w: attempt spent more than it reserved", ErrEvaluationBudget)
)

// EvaluationClaim is one worker's authority to perform one review, and the
// money reserved for it.
//
// ID is minted by the selecting instance and is deterministic in the artifact
// revision reviewed, the review role, and the sample ordinal (SPEC.md 4.12).
// The catalog treats it as opaque and enforces the one thing it can: a single
// live attempt per id, so an identical assignment cannot run twice at once,
// while a later independent sample carries a different ordinal, is therefore a
// different id, and is free to be granted alongside.
//
// Day, Fence and ExpiresAt are assigned by PostgreSQL and are output only. A
// Day supplied by a caller is ignored rather than trusted: a client clock that
// is wrong, or rolled back, would otherwise choose which day's allowance it
// spends, and yesterday's exhausted budget would be fresh again.
type EvaluationClaim struct {
	DeploymentID string
	ID           string
	SubjectID    string
	// SubjectKind is one of hypothesis, observation, finding, proposal, or
	// evaluation - the last being the bounded meta-review the lifecycle plan
	// permits. See ValidEvaluationSubjectKind.
	SubjectKind string
	// RunID is the worker run the authority is bound to, and the unit the
	// per-cycle ceiling is measured over. OwnerID is the instance that asked.
	RunID   string
	OwnerID string
	// PolicyVersion is the versioned policy that admitted this work. It is
	// also the identity the day's pinned allowance is checked against: one
	// version naming two different ceilings is a conflict, not a race.
	PolicyVersion string
	// Day is the server's UTC day the claim is charged to, as
	// EvaluationDayLayout.
	Day string
	// Fence is strictly increasing per ID and increments on every takeover. It
	// is the authority a worker presents, and it is why a resumed worker
	// holding the right run id is still refused after a takeover.
	Fence int64
	// ReservedCost is what the claim is permitted to spend. It is charged to
	// the day immediately and stays charged unless a finish reports what was
	// actually spent, so an abandoned attempt never reads as free.
	ReservedCost float64
	ExpiresAt    time.Time
}

// EvaluationBudget is the allowance a claim is judged against.
//
// DailyCost bounds the whole deployment across one UTC day; PerCycleCost bounds
// one worker run within that day, which is what stops a single cycle consuming
// the fleet's whole attention in one sitting. LeaseSeconds is how long the
// granted assignment may be held.
//
// The two ceilings are pinned by the first claim of a day, and within that day
// they may only be tightened (see migrations/0013). A caller presenting a
// larger ceiling under a different policy version is judged against the pinned
// one rather than its own, because within a day the catalog cannot tell an
// operator raising a budget from a stale worker presenting an outdated larger
// number - and it must never be the second. A raise takes effect at the next
// UTC day. LeaseSeconds is not pinned: a lease is not an allowance, and a
// worker that asks for a short one risks only its own claim.
type EvaluationBudget struct {
	DailyCost    float64
	PerCycleCost float64
	LeaseSeconds int
}

// evaluationSubjectKinds is the closed vocabulary migrations/0013's CHECK
// holds. It is the set of artifact kinds evaluation covers, plus `evaluation`
// itself for the bounded meta-review the lifecycle plan permits and bounds -
// reviewing a review is legitimate, requiring a review of every review is not,
// and that bound is the selector's to enforce rather than the catalog's.
var evaluationSubjectKinds = map[string]bool{
	"hypothesis":  true,
	"observation": true,
	"finding":     true,
	"proposal":    true,
	"evaluation":  true,
}

// ValidEvaluationSubjectKind reports whether kind is one migrations/0013
// admits. It is exported for ValidEntityID's reason: a selector that can ask
// before the database answers with a constraint violation turns a permanently
// failing claim into a caller bug its own test catches.
func ValidEvaluationSubjectKind(kind string) bool { return evaluationSubjectKinds[kind] }

// ClaimEvaluation grants one assignment to one worker within the deployment's
// allowance, or explains why it cannot.
//
// The whole decision is one transaction, serialized deployment-wide on the
// day's row: the transaction locks that row before it totals anything, so two
// instances claiming at once produce one grant and one refusal rather than two
// grants against the same remaining budget. migrations/0013 explains why the
// lock has to be a real row - an aggregate over claims locks nothing that does
// not exist yet, and the first claim of a day is exactly when there is nothing.
//
// What the caller gets back is the granted claim with the server's day, fence
// and expiry filled in. The outcomes that are not a grant:
//
//   - ErrEvaluationConflict when another owner holds a live claim on this id,
//     when the assignment already completed (a later independent sample needs
//     its own ordinal, not a second attempt at this one), when the id names a
//     different subject than the catalog holds for it.
//   - ErrEvaluationBudget when the reservation does not fit the day or the
//     run's cycle.
//   - ErrEvaluationInvalid for a malformed claim, budget, or unregistered
//     deployment.
//
// A repeat claim by the same run and owner while its lease is live is the
// grant it already holds, returned unchanged and charged nothing further. That
// is what makes a retry after a lost response safe: the alternative, a second
// reservation for one assignment, would spend the day's budget on a dropped
// packet.
//
// Taking over an expired claim inserts a new attempt with the next fence and
// its own reservation. The superseded attempt keeps its row, its day and its
// full reserved cost, because nobody can observe what a worker spent before it
// vanished and zero is the one answer that is certainly wrong.
func ClaimEvaluation(ctx context.Context, db *sql.DB, c EvaluationClaim, b EvaluationBudget) (EvaluationClaim, error) {
	if err := c.validate(); err != nil {
		return EvaluationClaim{}, err
	}
	if err := b.validate(); err != nil {
		return EvaluationClaim{}, err
	}
	// New authority may only be written against a compatible catalog schema.
	if err := EnsureCompatible(ctx, db); err != nil {
		return EvaluationClaim{}, err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return EvaluationClaim{}, fmt.Errorf("claim evaluation: %w", err)
	}
	defer tx.Rollback()
	if err := lockEvaluationDeployment(ctx, tx, c.DeploymentID); err != nil {
		if errors.Is(err, ErrEvaluationNotFound) {
			return EvaluationClaim{}, fmt.Errorf("%w: deployment %s is not registered", ErrEvaluationInvalid, c.DeploymentID)
		}
		return EvaluationClaim{}, err
	}

	// One read of the server's clock, reused for the day, the grant time and
	// the expiry, so the three cannot disagree and the day CHECK in
	// migrations/0013 holds by construction. clock_timestamp() rather than
	// now() for lease.go's reason: a transaction-frozen clock cannot observe
	// an expiry that happens while the transaction runs.
	var now time.Time
	if err := tx.QueryRowContext(ctx, `SELECT `+serverNow).Scan(&now); err != nil {
		return EvaluationClaim{}, fmt.Errorf("claim evaluation: read server time: %w", err)
	}

	day, budget, err := pinEvaluationDay(ctx, tx, c, b, now)
	if err != nil {
		return EvaluationClaim{}, err
	}
	// A valid policy tightening survives a refused assignment. Rolling it back
	// with a budget refusal would let a stale host spend the old allowance.
	refuse := func(reason error) (EvaluationClaim, error) {
		if err := tx.Commit(); err != nil {
			return EvaluationClaim{}, fmt.Errorf("persist evaluation allowance: %w", err)
		}
		return EvaluationClaim{}, reason
	}

	prior, held, err := latestAttempt(ctx, tx, c.DeploymentID, c.ID)
	if err != nil {
		return EvaluationClaim{}, err
	}
	fence := int64(1)
	if held {
		if prior.subjectKind != c.SubjectKind || prior.subjectID != c.SubjectID {
			return refuse(fmt.Errorf("%w: assignment %s names another subject", ErrEvaluationConflict, c.ID))
		}
		switch {
		case prior.finished:
			return refuse(fmt.Errorf("%w: assignment %s already completed", ErrEvaluationConflict, c.ID))
		case prior.expiresAt.After(now) && prior.runID == c.RunID && prior.ownerID == c.OwnerID:
			// The same worker asking again. Return what it already holds
			// rather than reserving a second time for one assignment.
			return EvaluationClaim{
				DeploymentID:  c.DeploymentID,
				ID:            c.ID,
				SubjectID:     prior.subjectID,
				SubjectKind:   prior.subjectKind,
				RunID:         prior.runID,
				OwnerID:       prior.ownerID,
				PolicyVersion: prior.policyVersion,
				Day:           prior.day,
				Fence:         prior.fence,
				ReservedCost:  prior.reservedCost,
				ExpiresAt:     prior.expiresAt,
			}, tx.Commit()
		case prior.expiresAt.After(now):
			return refuse(fmt.Errorf("%w: assignment %s is held by another worker until %s",
				ErrEvaluationConflict, c.ID, prior.expiresAt.UTC().Format(time.RFC3339)))
		}
		fence = prior.fence + 1
	}

	spent, cycle, err := evaluationSpend(ctx, tx, c.DeploymentID, day, c.RunID)
	if err != nil {
		return EvaluationClaim{}, err
	}
	if spent+c.ReservedCost > budget.DailyCost {
		return refuse(fmt.Errorf("%w: deployment %s has %.4f of %.4f charged to %s; this claim reserves %.4f",
			ErrEvaluationBudget, c.DeploymentID, spent, budget.DailyCost, day, c.ReservedCost))
	}
	if cycle+c.ReservedCost > budget.PerCycleCost {
		return refuse(fmt.Errorf("%w: run %s has %.4f of %.4f charged to %s; this claim reserves %.4f",
			ErrEvaluationBudget, c.RunID, cycle, budget.PerCycleCost, day, c.ReservedCost))
	}

	out := c
	out.Day = day
	out.Fence = fence
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO evaluation_claims (
			deployment_id, claim_id, fence, subject_kind, subject_id,
			run_id, owner_id, policy_version, day, reserved_cost,
			state, claimed_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::date, $10,
			'claimed', $11::timestamptz,
			$11::timestamptz + make_interval(secs => $12))
		RETURNING expires_at`,
		c.DeploymentID, c.ID, fence, c.SubjectKind, c.SubjectID,
		c.RunID, c.OwnerID, c.PolicyVersion, day, c.ReservedCost,
		now, float64(b.LeaseSeconds)).Scan(&out.ExpiresAt); err != nil {
		return EvaluationClaim{}, fmt.Errorf("claim evaluation %s: %w", c.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return EvaluationClaim{}, fmt.Errorf("claim evaluation %s: %w", c.ID, err)
	}
	return out, nil
}

// ValidateEvaluationClaim reports whether this worker may still act on this
// assignment.
//
// It is the strict gate, and the one a writer calls before it commits anything
// derived from the work: the presented run and fence must be the current
// authority, and an unfinished lease must not have expired. An expired lease
// is refused even when nobody has taken over yet, because the next claimer may
// take it at any instant and two live opinions on one assignment is the failure
// the fence exists to prevent.
//
// A claim this exact worker already finished validates successfully. That is
// deliberate rather than lenient: completion is recorded before the local
// record is published (see FinishEvaluationClaim), so a writer retrying after
// its own commit failed is the same authority finishing the same work, and
// refusing it would strand a paid-for assessment that can never be republished.
func ValidateEvaluationClaim(ctx context.Context, db *sql.DB, deploymentID, id, runID string, fence int64) error {
	if err := validateClaimRef(deploymentID, id, runID, fence); err != nil {
		return err
	}
	var (
		latest   int64
		owner    string
		finished bool
		live     bool
	)
	err := db.QueryRowContext(ctx, `
		SELECT fence, run_id, state = 'finished', expires_at > `+serverNow+`
		  FROM evaluation_claims
		 WHERE deployment_id = $1 AND claim_id = $2
		 ORDER BY fence DESC
		 LIMIT 1`, deploymentID, id).Scan(&latest, &owner, &finished, &live)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: deployment %s holds no assignment %s",
			ErrEvaluationNotFound, deploymentID, id)
	case err != nil:
		return fmt.Errorf("validate evaluation claim %s: %w", id, err)
	case latest != fence:
		return fmt.Errorf(
			"%w: assignment %s is at fence %d and this worker holds %d; it was taken over",
			ErrEvaluationConflict, id, latest, fence)
	case owner != runID:
		return fmt.Errorf("%w: assignment %s at fence %d belongs to run %s, not %s",
			ErrEvaluationConflict, id, fence, owner, runID)
	case finished:
		return nil
	case !live:
		return fmt.Errorf(
			"%w: the lease on assignment %s has expired; another worker may take it over at any moment, so this one may no longer act on it",
			ErrEvaluationConflict, id)
	}
	return nil
}

// FinishEvaluationClaim records what an assignment actually cost and closes it.
//
// Its authority test is takeover, not expiry, and the asymmetry with
// ValidateEvaluationClaim is the point. A worker whose lease lapsed while the
// model was still answering has already spent the money; refusing to record it
// would leave the day charged at the reservation, which is the conservative
// direction for a lost worker but the wrong one for a worker that is standing
// right here with a receipt. Once another owner has taken the assignment the
// old attempt's finish is refused, because then the reservation it forfeited is
// exactly what the day should keep carrying.
//
// It is idempotent for the identical receipt and only that. Repeating the same
// (run, fence, cost) succeeds silently, which is what lets a caller finish the
// claim before publishing its record and retry the publication after a local
// failure without spending twice. A second finish naming a different cost is a
// contradiction rather than a correction and returns ErrEvaluationConflict:
// two numbers for one attempt means one of them is wrong, and silently keeping
// the newer would make the fleet's spending whatever the last retry said.
//
// A cost larger than the reservation is recorded in full and then reported as
// ErrEvaluationOverrun, which errors.Is also matches as ErrEvaluationBudget. A
// provider can overrun a worker that asked for less, and the ledger's job is
// to say what was spent: the day carries the real number, the deployment's
// next claim is judged against it, and a caller must not publish the result as
// if the attempt stayed inside its allowance. The retry of that same receipt
// reports the same overrun rather than quietly succeeding.
func FinishEvaluationClaim(ctx context.Context, db *sql.DB, deploymentID, id, runID string, fence int64, cost float64) error {
	if err := validateClaimRef(deploymentID, id, runID, fence); err != nil {
		return err
	}
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost < 0 {
		return fmt.Errorf("%w: observed cost must be a finite non-negative number, got %v",
			ErrEvaluationInvalid, cost)
	}
	if err := EnsureCompatible(ctx, db); err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("finish evaluation claim: %w", err)
	}
	defer tx.Rollback()
	if err := lockEvaluationDeployment(ctx, tx, deploymentID); err != nil {
		return err
	}

	var reserved float64
	err = tx.QueryRowContext(ctx, `
		UPDATE evaluation_claims c
		   SET state = 'finished', observed_cost = $5, finished_at = `+serverNow+`
		 WHERE c.deployment_id = $1 AND c.claim_id = $2
		   AND c.run_id = $3 AND c.fence = $4
		   AND c.state = 'claimed'
		   AND NOT EXISTS (
			   SELECT 1 FROM evaluation_claims later
			    WHERE later.deployment_id = $1 AND later.claim_id = $2
			      AND later.fence > $4)
		RETURNING c.reserved_cost`,
		deploymentID, id, runID, fence, cost).Scan(&reserved)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Nothing was updated. Either this worker is not the authority any
		// more, or the identical receipt is already recorded.
		return finishRefusal(ctx, tx, deploymentID, id, runID, fence, cost)
	case err != nil:
		return fmt.Errorf("finish evaluation claim %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("finish evaluation claim %s: %w", id, err)
	}
	return overrun(id, fence, reserved, cost)
}

// finishRefusal explains a finish that wrote no row.
//
// The decision was already made by the statement above; this read only chooses
// which answer to report, so it adds no race - and the one outcome it can
// discover, an identical receipt already recorded, is a success rather than an
// error.
func finishRefusal(ctx context.Context, db *sql.Tx, deploymentID, id, runID string, fence int64, cost float64) error {
	var (
		owner    string
		finished bool
		observed sql.NullFloat64
		reserved float64
	)
	err := db.QueryRowContext(ctx, `
		SELECT run_id, state = 'finished', observed_cost, reserved_cost
		  FROM evaluation_claims
		 WHERE deployment_id = $1 AND claim_id = $2 AND fence = $3`,
		deploymentID, id, fence).Scan(&owner, &finished, &observed, &reserved)
	if errors.Is(err, sql.ErrNoRows) {
		var latest sql.NullInt64
		if err := db.QueryRowContext(ctx,
			`SELECT max(fence) FROM evaluation_claims WHERE deployment_id = $1 AND claim_id = $2`,
			deploymentID, id).Scan(&latest); err != nil {
			return fmt.Errorf("finish evaluation claim %s: %w", id, err)
		}
		if !latest.Valid {
			return fmt.Errorf("%w: deployment %s holds no assignment %s",
				ErrEvaluationNotFound, deploymentID, id)
		}
		return fmt.Errorf(
			"%w: assignment %s has no attempt at fence %d; it is at %d",
			ErrEvaluationConflict, id, fence, latest.Int64)
	}
	if err != nil {
		return fmt.Errorf("finish evaluation claim %s: %w", id, err)
	}
	switch {
	case owner != runID:
		return fmt.Errorf("%w: assignment %s at fence %d belongs to run %s, not %s",
			ErrEvaluationConflict, id, fence, owner, runID)
	case finished && observed.Valid && observed.Float64 == cost:
		// The identical receipt, already recorded. It reports whatever it
		// reported the first time, overrun included, so a retry cannot make
		// an overspend look like a clean finish.
		return overrun(id, fence, reserved, cost)
	case finished:
		return fmt.Errorf(
			"%w: assignment %s at fence %d already reported %v and this finish reports %v; one attempt has one cost, and the newer number does not silently win",
			ErrEvaluationConflict, id, fence, observed.Float64, cost)
	}
	// The row is still claimed, so the takeover guard is what refused: a later
	// attempt exists and this one forfeited its reservation.
	return fmt.Errorf(
		"%w: assignment %s was taken over after fence %d; its reservation stays charged as unobserved spend and this worker's result is not the fleet's",
		ErrEvaluationConflict, id, fence)
}

// Serializing admission and completion on the deployment also covers midnight
// takeovers and observed overruns. A snapshot-only NOT EXISTS fence check cannot
// protect an UPDATE that waits while another transaction inserts a newer fence.
func lockEvaluationDeployment(ctx context.Context, tx *sql.Tx, deploymentID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT deployment_id FROM deployments WHERE deployment_id=$1 FOR UPDATE`, deploymentID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: deployment %s is not registered", ErrEvaluationNotFound, deploymentID)
	}
	if err != nil {
		return fmt.Errorf("lock evaluation deployment: %w", err)
	}
	return nil
}

// overrun reports a finish that was recorded and exceeded its reservation, and
// nil for one that stayed inside it.
//
// It is separate from the statements that write, because both the first finish
// and a retry of the identical receipt must answer it the same way: an
// overspend that stops being reported on the second call would be an overspend
// a caller can retry its way out of.
func overrun(id string, fence int64, reserved, observed float64) error {
	if observed <= reserved {
		return nil
	}
	return fmt.Errorf(
		"%w: assignment %s at fence %d reserved %.4f and spent %.4f; the full amount is recorded and charged to its day, so the deployment's next claim is judged against reality rather than against the reservation",
		ErrEvaluationOverrun, id, fence, reserved, observed)
}

// budgetPin is the allowance in force on one day, as the catalog holds it.
type budgetPin struct {
	DailyCost    float64
	PerCycleCost float64
}

// pinEvaluationDay locks the deployment's day, reconciles the caller's offered
// allowance with the pinned one, and reports the day and the ceilings the claim
// is judged against.
//
// Admission already holds the deployment lock. Each ceiling is the minimum
// offered during the UTC day: lowering either applies immediately, while a
// raised ceiling waits for the next day. The effective pair may consequently
// differ from every single policy offered that day.
func pinEvaluationDay(ctx context.Context, tx *sql.Tx, c EvaluationClaim, b EvaluationBudget, now time.Time) (string, budgetPin, error) {
	// Gated on the deployment row so an unregistered deployment reports itself
	// by name rather than as a foreign-key violation from whichever statement
	// happened to run first. The same select is what the FOR UPDATE below
	// finds nothing of when the deployment does not exist.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO evaluation_budget_days (
			deployment_id, day, daily_cost, per_cycle_cost, policy_version,
			created_at, updated_at)
		SELECT d.deployment_id, ($2::timestamptz AT TIME ZONE 'UTC')::date,
		       $3, $4, $5, $2, $2
		  FROM deployments d
		 WHERE d.deployment_id = $1
		ON CONFLICT (deployment_id, day) DO NOTHING`,
		c.DeploymentID, now, b.DailyCost, b.PerCycleCost, c.PolicyVersion); err != nil {
		return "", budgetPin{}, fmt.Errorf("claim evaluation: open budget day: %w", err)
	}

	var (
		day string
		pin budgetPin
	)
	err := tx.QueryRowContext(ctx, `
		SELECT to_char(day, 'YYYY-MM-DD'), daily_cost, per_cycle_cost
		  FROM evaluation_budget_days
		 WHERE deployment_id = $1
		   AND day = ($2::timestamptz AT TIME ZONE 'UTC')::date
		 FOR UPDATE`,
		c.DeploymentID, now).Scan(&day, &pin.DailyCost, &pin.PerCycleCost)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", budgetPin{}, fmt.Errorf(
			"%w: deployment %s is not registered in this catalog, so it has no allowance to spend",
			ErrEvaluationInvalid, c.DeploymentID)
	case err != nil:
		return "", budgetPin{}, fmt.Errorf("claim evaluation: lock budget day: %w", err)
	}

	// Reconcile bounds independently, including when one policy lowers one
	// ceiling and raises the other. Repeating that policy remains usable
	// within the conservative pair; its requested raise never takes effect.
	tightened := budgetPin{
		DailyCost:    math.Min(pin.DailyCost, b.DailyCost),
		PerCycleCost: math.Min(pin.PerCycleCost, b.PerCycleCost),
	}
	if tightened == pin {
		return day, pin, nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE evaluation_budget_days
		   SET daily_cost = $3, per_cycle_cost = $4, policy_version = $5, updated_at = $2
		 WHERE deployment_id = $1
		   AND day = ($2::timestamptz AT TIME ZONE 'UTC')::date`,
		c.DeploymentID, now, tightened.DailyCost, tightened.PerCycleCost, c.PolicyVersion); err != nil {
		return "", budgetPin{}, fmt.Errorf("claim evaluation: tighten budget day: %w", err)
	}
	return day, tightened, nil
}

// attempt is one row of evaluation_claims, as the claim path reads it.
type attempt struct {
	fence         int64
	subjectKind   string
	subjectID     string
	runID         string
	ownerID       string
	policyVersion string
	day           string
	reservedCost  float64
	finished      bool
	expiresAt     time.Time
}

// latestAttempt reads and locks the newest attempt on an assignment, if the
// deployment holds one.
//
// The row lock matters even though the day row is already held: a finish taking
// the same row does not touch the day, so without it a takeover could be
// decided against a state a concurrent finish was in the middle of changing.
func latestAttempt(ctx context.Context, tx *sql.Tx, deploymentID, id string) (attempt, bool, error) {
	var a attempt
	err := tx.QueryRowContext(ctx, `
		SELECT fence, subject_kind, subject_id, run_id, owner_id, policy_version,
		       to_char(day, 'YYYY-MM-DD'), reserved_cost, state = 'finished', expires_at
		  FROM evaluation_claims
		 WHERE deployment_id = $1 AND claim_id = $2
		 ORDER BY fence DESC
		 LIMIT 1
		 FOR UPDATE`, deploymentID, id).Scan(
		&a.fence, &a.subjectKind, &a.subjectID, &a.runID, &a.ownerID, &a.policyVersion,
		&a.day, &a.reservedCost, &a.finished, &a.expiresAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return attempt{}, false, nil
	case err != nil:
		return attempt{}, false, fmt.Errorf("claim evaluation: read assignment %s: %w", id, err)
	}
	return a, true, nil
}

// evaluationSpend totals what a deployment's day already carries, and how much
// of it belongs to one run.
//
// The CASE is the conservative rule in one line: a finished attempt is charged
// what it reported, and every other attempt - live, expired, abandoned - is
// charged the whole reservation it was granted. An expired attempt is not
// discounted and never becomes free, because the fleet cannot observe what a
// worker spent before it stopped answering, and zero is the one answer that is
// certainly wrong.
func evaluationSpend(ctx context.Context, tx *sql.Tx, deploymentID, day, runID string) (total, cycle float64, err error) {
	if err := tx.QueryRowContext(ctx, `
		SELECT
		  coalesce(sum(CASE WHEN state = 'finished' THEN observed_cost
		                    ELSE reserved_cost END), 0),
		  coalesce(sum(CASE WHEN run_id = $3
		                    THEN CASE WHEN state = 'finished' THEN observed_cost
		                              ELSE reserved_cost END
		                    ELSE 0 END), 0)
		  FROM evaluation_claims
		 WHERE deployment_id = $1 AND day = $2::date`,
		deploymentID, day, runID).Scan(&total, &cycle); err != nil {
		return 0, 0, fmt.Errorf("claim evaluation: total day %s: %w", day, err)
	}
	return total, cycle, nil
}

func (c EvaluationClaim) validate() error {
	switch {
	case !validRecordID.MatchString(c.DeploymentID):
		return fmt.Errorf("%w: deployment id must match %s", ErrEvaluationInvalid, validRecordID)
	case !validRecordID.MatchString(c.ID):
		return fmt.Errorf("%w: assignment id must match %s", ErrEvaluationInvalid, validRecordID)
	case !validRecordID.MatchString(c.SubjectID):
		return fmt.Errorf("%w: subject id must match %s", ErrEvaluationInvalid, validRecordID)
	case !validRecordID.MatchString(c.RunID):
		return fmt.Errorf("%w: run id must match %s", ErrEvaluationInvalid, validRecordID)
	case !validRecordID.MatchString(c.OwnerID):
		return fmt.Errorf("%w: owner instance id must match %s", ErrEvaluationInvalid, validRecordID)
	case !validRecordID.MatchString(c.PolicyVersion):
		return fmt.Errorf("%w: policy version must match %s: the allowance is pinned to it, so an unusable version is an unusable claim",
			ErrEvaluationInvalid, validRecordID)
	case !ValidEvaluationSubjectKind(c.SubjectKind):
		return fmt.Errorf("%w: subject kind %q is not one evaluation covers", ErrEvaluationInvalid, c.SubjectKind)
	case math.IsNaN(c.ReservedCost) || math.IsInf(c.ReservedCost, 0) || c.ReservedCost < 0:
		return fmt.Errorf("%w: reserved cost must be a finite non-negative number, got %v",
			ErrEvaluationInvalid, c.ReservedCost)
	}
	return nil
}

func (b EvaluationBudget) validate() error {
	switch {
	case math.IsNaN(b.DailyCost) || math.IsInf(b.DailyCost, 0) || b.DailyCost < 0:
		return fmt.Errorf("%w: daily allowance must be a finite non-negative number, got %v",
			ErrEvaluationInvalid, b.DailyCost)
	case math.IsNaN(b.PerCycleCost) || math.IsInf(b.PerCycleCost, 0) || b.PerCycleCost < 0:
		return fmt.Errorf("%w: per-cycle allowance must be a finite non-negative number, got %v",
			ErrEvaluationInvalid, b.PerCycleCost)
	case b.LeaseSeconds <= 0:
		return fmt.Errorf("%w: lease must be a positive number of seconds", ErrEvaluationInvalid)
	case b.LeaseSeconds > maxEvaluationLeaseSeconds:
		return fmt.Errorf("%w: a lease of %ds outlives the budget day it reserves against; the ceiling is %ds",
			ErrEvaluationInvalid, b.LeaseSeconds, maxEvaluationLeaseSeconds)
	}
	return nil
}

// validateClaimRef bounds the identifiers the two post-grant calls carry. They
// name an existing claim rather than describing a new one, so they check shape
// and nothing else: whether the named authority is current is the database's
// answer, not a predicate's.
func validateClaimRef(deploymentID, id, runID string, fence int64) error {
	switch {
	case !validRecordID.MatchString(deploymentID):
		return fmt.Errorf("%w: deployment id must match %s", ErrEvaluationInvalid, validRecordID)
	case !validRecordID.MatchString(id):
		return fmt.Errorf("%w: assignment id must match %s", ErrEvaluationInvalid, validRecordID)
	case !validRecordID.MatchString(runID):
		return fmt.Errorf("%w: run id must match %s", ErrEvaluationInvalid, validRecordID)
	case fence < 1:
		return fmt.Errorf("%w: a fence is assigned by the catalog and starts at 1, got %d",
			ErrEvaluationInvalid, fence)
	}
	return nil
}
