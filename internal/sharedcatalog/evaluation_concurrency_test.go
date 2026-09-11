package sharedcatalog

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestEvaluationFinishCannotCrossConcurrentTakeover(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	claim := mustClaim(t, db, sampleClaim("concurrent-takeover", "run-a", "inst-a", 1), sampleBudget(3, 3, 1))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, `SELECT pg_sleep(GREATEST(0, EXTRACT(EPOCH FROM ($1::timestamptz-clock_timestamp()))))`, claim.ExpiresAt); err != nil {
		t.Fatal(err)
	}

	// Hold the takeover at its write boundary. The stale completion starts its
	// database operation before the new fence commits, not merely afterward.
	takeover, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer takeover.Rollback()
	var deployment string
	if err := takeover.QueryRowContext(ctx, `SELECT deployment_id FROM deployments WHERE deployment_id='d1' FOR UPDATE`).Scan(&deployment); err != nil {
		t.Fatal(err)
	}
	var fence int64
	if err := takeover.QueryRowContext(ctx, `SELECT fence FROM evaluation_claims WHERE deployment_id='d1' AND claim_id=$1 FOR UPDATE`, claim.ID).Scan(&fence); err != nil {
		t.Fatal(err)
	}
	var blocker int
	if err := takeover.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- FinishEvaluationClaim(ctx, db, "d1", claim.ID, "run-a", claim.Fence, 0.2)
	}()
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE datname=current_database()
			AND $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("completion bypassed the held takeover: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err := takeover.ExecContext(ctx, `INSERT INTO evaluation_claims
		(deployment_id,claim_id,fence,subject_kind,subject_id,run_id,owner_id,
		 policy_version,day,reserved_cost,state,claimed_at,expires_at)
		SELECT deployment_id,claim_id,fence+1,subject_kind,subject_id,'run-b','inst-b',
		 policy_version,day,reserved_cost,'claimed',claimed_at,claimed_at+interval '1 day'
		FROM evaluation_claims WHERE deployment_id='d1' AND claim_id=$1 AND fence=$2`, claim.ID, fence); err != nil {
		t.Fatal(err)
	}
	if err := takeover.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; !errors.Is(err, ErrEvaluationConflict) {
		t.Fatalf("a completion begun before takeover committed stale authority: %v", err)
	}
}

func TestEvaluationDeniedClaimStillTightensTheAllowance(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := t.Context()
	claim := mustClaim(t, db, sampleClaim("first", "run-a", "inst-a", 1), sampleBudget(3, 3, 600))
	if err := FinishEvaluationClaim(ctx, db, "d1", claim.ID, claim.RunID, claim.Fence, 0.25); err != nil {
		t.Fatal(err)
	}
	tight := sampleClaim("tight", "run-b", "inst-b", 1)
	tight.PolicyVersion = "eval-2"
	if _, err := ClaimEvaluation(ctx, db, tight, sampleBudget(0.5, 3, 600)); !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("oversized claim under lowered allowance: %v", err)
	}
	if _, err := ClaimEvaluation(ctx, db, sampleClaim("stale", "run-c", "inst-c", 0.5), sampleBudget(3, 3, 600)); !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("refused work rolled back the operator's lower allowance: %v", err)
	}
}

func TestEvaluationMixedBudgetChangeRemainsUsableWithinBothCaps(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	mustClaim(t, db, sampleClaim("original", "run-a", "inst-a", 0.25), sampleBudget(3, 1, 600))
	changed := sampleClaim("changed", "run-b", "inst-b", 0.25)
	changed.PolicyVersion = "eval-2"
	mustClaim(t, db, changed, sampleBudget(2, 3, 600))
	changed.ID = "next"
	mustClaim(t, db, changed, sampleBudget(2, 3, 600))
	changed.ID = "too-large"
	changed.ReservedCost = 0.75
	if _, err := ClaimEvaluation(t.Context(), db, changed, sampleBudget(2, 3, 600)); !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("raising the offered cycle limit bypassed its pinned ceiling: %v", err)
	}
}
