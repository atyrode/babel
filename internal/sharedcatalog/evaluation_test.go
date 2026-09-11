package sharedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// These tests defend the coordination half of full-lifecycle evaluation
// (SPEC.md 4.12, 5.8; issue #219): that one assignment has one owner, that one
// deployment has one allowance however many hosts are spending it, and that
// money nobody can observe is never assumed to be zero.
//
// They live inside the package and against a real PostgreSQL because every
// property here is a database property: transaction locks, server-authoritative
// time, and triggers that refuse a rewritten charge.

// seedEvaluation registers the deployment a claim charges against. Hosts and
// instances are seeded with it because a worker normally runs on a registered
// machine, even though a claim deliberately does not require one.
func seedEvaluation(t *testing.T, db *sql.DB) {
	t.Helper()
	seedHost(t, db, "h1")
}

// sampleClaim is one assignment as the selector would mint it: the id carries
// the revision, the role and the sample ordinal, which is what makes a second
// independent look a different assignment rather than a duplicate of this one.
func sampleClaim(id, runID, owner string, reserved float64) EvaluationClaim {
	return EvaluationClaim{
		DeploymentID:  "d1",
		ID:            id,
		SubjectKind:   "proposal",
		SubjectID:     "prop-1",
		RunID:         runID,
		OwnerID:       owner,
		PolicyVersion: "eval-1",
		ReservedCost:  reserved,
	}
}

func sampleBudget(daily, perCycle float64, lease int) EvaluationBudget {
	return EvaluationBudget{DailyCost: daily, PerCycleCost: perCycle, LeaseSeconds: lease}
}

func mustClaim(t *testing.T, db *sql.DB, c EvaluationClaim, b EvaluationBudget) EvaluationClaim {
	t.Helper()
	got, err := ClaimEvaluation(context.Background(), db, c, b)
	if err != nil {
		t.Fatalf("claim %s: %v", c.ID, err)
	}
	return got
}

// An evaluation is a Phase B record like any other: it publishes through the
// same object-first protocol, reads back on any instance, and says nothing in
// the clear. The kind is the only new thing, and migrations/0012 is what lets
// it reach the catalog at all.
func TestEvaluationRecordsPublishAndReadBack(t *testing.T) {
	db := newInternalDB(t)
	seedPhaseB(t, db)
	store, ring := newMemStore(), newKeyring(t)
	ctx := context.Background()

	if !KindEvaluation.Valid() {
		t.Fatal("KindEvaluation is not in the Go vocabulary, so a writer cannot stage one")
	}
	mustSync(t, db, store, ring, sampleClosure("run-eval", KindEvaluation, KindEvaluation))

	got, err := Records(ctx, db, RecordFilter{DeploymentID: "d1", Kinds: []RecordKind{KindEvaluation}})
	if err != nil {
		t.Fatalf("read evaluation records: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d evaluation records, want 2", len(got))
	}
	for _, rec := range got {
		if rec.Record.Kind != KindEvaluation {
			t.Errorf("record %s has kind %q", rec.Record.RecordID, rec.Record.Kind)
		}
		if !rec.Committed() {
			t.Errorf("record %s is %q, want committed", rec.Record.RecordID, rec.SyncState)
		}
	}

	// A vote, a tally or an outcome is content. None of it may be in the
	// clear, and the allowlist is what keeps that checkable rather than
	// aspirational as the schema grows.
	for _, hit := range scanSchemaForText(t, db, sentinel) {
		t.Errorf("an evaluation's plaintext reached PostgreSQL in %s", hit)
	}
	if err := Verify(ctx, db); err != nil {
		t.Fatalf("the migrated schema no longer matches the allowlist: %v", err)
	}
}

// Two workers, one assignment. The second must be refused rather than granted
// a parallel authority, and the first must be able to ask again - a retry after
// a lost response is not a second reservation.
func TestEvaluationClaimGrantsOneOwner(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()
	budget := sampleBudget(10, 10, 600)

	first := mustClaim(t, db, sampleClaim("assign-1", "run-a", "inst-a", 1), budget)
	switch {
	case first.Fence != 1:
		t.Errorf("first grant has fence %d, want 1", first.Fence)
	case first.Day != time.Now().UTC().Format(EvaluationDayLayout):
		// Tolerated only if the suite is running across a UTC midnight, which
		// is worth knowing about rather than papering over.
		t.Errorf("claim charged to day %q, want the server's UTC day", first.Day)
	case !first.ExpiresAt.After(time.Now().Add(-time.Minute)):
		t.Errorf("claim expires at %s, which is not a live lease", first.ExpiresAt)
	}

	_, err := ClaimEvaluation(ctx, db, sampleClaim("assign-1", "run-b", "inst-b", 1), budget)
	if !errors.Is(err, ErrEvaluationConflict) {
		t.Fatalf("a second worker claiming a live assignment got %v, want ErrEvaluationConflict", err)
	}

	again := mustClaim(t, db, sampleClaim("assign-1", "run-a", "inst-a", 1), budget)
	if again.Fence != first.Fence || !again.ExpiresAt.Equal(first.ExpiresAt) {
		t.Errorf("the owner re-claiming got %+v, want the grant it already holds %+v", again, first)
	}
	// The retry must not have reserved a second time: the day has room for
	// exactly ten claims of one, and one of them is spent.
	if n := countRows(t, db, "evaluation_claims"); n != 1 {
		t.Errorf("the catalog holds %d attempts, want 1: a retry reserved again", n)
	}

	// A different subject under the same id is a collision, not a retry.
	collide := sampleClaim("assign-1", "run-a", "inst-a", 1)
	collide.SubjectID = "prop-2"
	if _, err := ClaimEvaluation(ctx, db, collide, budget); !errors.Is(err, ErrEvaluationConflict) {
		t.Fatalf("an id naming a second subject got %v, want ErrEvaluationConflict", err)
	}
}

// The allowance is the deployment's, not each host's. Concurrent claimers must
// produce exactly as many grants as the budget pays for - this is the property
// the lifecycle plan says receipt-derived local accounting cannot provide.
func TestEvaluationBudgetIsSharedAndSerialized(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()

	const claimants = 8
	// Three of one, against a day of three and a generous per-run ceiling, so
	// only the daily allowance binds.
	budget := sampleBudget(3, 100, 600)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		granted int
		refused int
		other   []error
	)
	for i := range claimants {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := sampleClaim(fmt.Sprintf("assign-%d", i), fmt.Sprintf("run-%d", i),
				fmt.Sprintf("inst-%d", i), 1)
			_, err := ClaimEvaluation(ctx, db, c, budget)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				granted++
			case errors.Is(err, ErrEvaluationBudget):
				refused++
			default:
				other = append(other, err)
			}
		}(i)
	}
	wg.Wait()

	for _, err := range other {
		t.Errorf("a concurrent claim failed for an unexpected reason: %v", err)
	}
	if granted != 3 || refused != claimants-3 {
		t.Fatalf("%d grants and %d budget refusals from %d concurrent claimants, want 3 and %d: "+
			"the allowance is being measured per claimer rather than per deployment",
			granted, refused, claimants, claimants-3)
	}
}

// One cycle must not consume the fleet's whole day, and one cycle's exhaustion
// must not stop another's work.
func TestEvaluationPerCycleCeilingBindsOneRun(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()
	budget := sampleBudget(10, 2, 600)

	mustClaim(t, db, sampleClaim("assign-a1", "run-a", "inst-a", 1), budget)
	mustClaim(t, db, sampleClaim("assign-a2", "run-a", "inst-a", 1), budget)

	_, err := ClaimEvaluation(ctx, db, sampleClaim("assign-a3", "run-a", "inst-a", 1), budget)
	if !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("a third claim in a two-unit cycle got %v, want ErrEvaluationBudget", err)
	}
	// The day still has eight units, and they belong to the deployment rather
	// than to the run that just exhausted its cycle.
	mustClaim(t, db, sampleClaim("assign-b1", "run-b", "inst-b", 1), budget)
}

// A lease that lapses is what takeover exists for, and the stale worker coming
// back is what the fence exists for. The spend of the attempt nobody can ask
// about stays charged: the lifecycle plan's "expiry, cancellation and failed
// delivery require explicit recovery semantics", where the recovery must not be
// a discount.
func TestEvaluationTakeoverFencesTheStaleWorkerAndKeepsItsSpend(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()
	// Two and a half units of day, so the forfeited reservation plus the
	// takeover's own leave no room for a third claim.
	budget := sampleBudget(2.5, 10, 1)

	stale := mustClaim(t, db, sampleClaim("assign-1", "run-a", "inst-a", 1), budget)
	if err := ValidateEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", stale.Fence); err != nil {
		t.Fatalf("the live owner cannot validate its own claim: %v", err)
	}

	// Real elapsed time: expiry is the server's, and the trigger refuses any
	// statement that would move expires_at, which is the point of it.
	time.Sleep(1200 * time.Millisecond)

	if err := ValidateEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", stale.Fence); !errors.Is(err, ErrEvaluationConflict) {
		t.Fatalf("validating an expired lease got %v, want ErrEvaluationConflict", err)
	}

	taken := mustClaim(t, db, sampleClaim("assign-1", "run-b", "inst-b", 1),
		sampleBudget(2.5, 10, 600))
	if taken.Fence != stale.Fence+1 {
		t.Fatalf("takeover fence %d, want %d", taken.Fence, stale.Fence+1)
	}

	// The stale worker returns. It may neither act nor report.
	if err := ValidateEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", stale.Fence); !errors.Is(err, ErrEvaluationConflict) {
		t.Errorf("a superseded worker validated its claim: %v", err)
	}
	if err := FinishEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", stale.Fence, 0.2); !errors.Is(err, ErrEvaluationConflict) {
		t.Errorf("a superseded worker finished its claim: %v", err)
	}

	// And its reservation is still charged, so the day has half a unit left
	// rather than one and a half.
	_, err := ClaimEvaluation(ctx, db, sampleClaim("assign-2", "run-c", "inst-c", 1),
		sampleBudget(2.5, 10, 600))
	if !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("a claim after an abandoned attempt got %v, want ErrEvaluationBudget: "+
			"unobserved spend was discounted to zero", err)
	}

	// The new owner completes normally, and reporting less than it reserved
	// releases the difference to the rest of the day.
	if err := FinishEvaluationClaim(ctx, db, "d1", "assign-1", "run-b", taken.Fence, 0.1); err != nil {
		t.Fatalf("the current owner could not finish: %v", err)
	}
	mustClaim(t, db, sampleClaim("assign-2", "run-c", "inst-c", 1), sampleBudget(2.5, 10, 600))
}

// A completed assignment is completed. A second look at the same revision and
// role is a new sample with its own ordinal, which is a different id and is
// free to be granted.
func TestEvaluationCompletedAssignmentIsNotReclaimable(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()
	budget := sampleBudget(10, 10, 600)

	first := mustClaim(t, db, sampleClaim("assign-rev7-reception-0", "run-a", "inst-a", 1), budget)
	if err := FinishEvaluationClaim(ctx, db, "d1", first.ID, "run-a", first.Fence, 0.5); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if _, err := ClaimEvaluation(ctx, db, sampleClaim(first.ID, "run-b", "inst-b", 1), budget); !errors.Is(err, ErrEvaluationConflict) {
		t.Fatalf("re-claiming a completed assignment got %v, want ErrEvaluationConflict", err)
	}
	// The next ordinal is a legitimate independent sample.
	mustClaim(t, db, sampleClaim("assign-rev7-reception-1", "run-b", "inst-b", 1), budget)
}

// Finishing is what a writer does before it publishes, so it has to survive the
// publication failing and being retried. One receipt, replayed, is one charge;
// two different costs for one attempt is a contradiction the catalog refuses to
// resolve by taking the newer.
func TestEvaluationFinishIsIdempotentForOneReceipt(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()
	budget := sampleBudget(10, 10, 600)

	granted := mustClaim(t, db, sampleClaim("assign-1", "run-a", "inst-a", 1), budget)

	if err := FinishEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", granted.Fence, 0.25); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := FinishEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", granted.Fence, 0.25); err != nil {
		t.Fatalf("replaying the identical receipt got %v, want success: a local commit "+
			"failure would otherwise strand a paid-for assessment", err)
	}
	// The owner may still validate what it finished, which is what makes that
	// retry possible at all.
	if err := ValidateEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", granted.Fence); err != nil {
		t.Errorf("validating a claim this worker finished got %v, want success", err)
	}

	err := FinishEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", granted.Fence, 0.9)
	if !errors.Is(err, ErrEvaluationConflict) {
		t.Fatalf("a second cost for one attempt got %v, want ErrEvaluationConflict", err)
	}
	err = FinishEvaluationClaim(ctx, db, "d1", "assign-1", "run-z", granted.Fence, 0.25)
	if !errors.Is(err, ErrEvaluationConflict) {
		t.Fatalf("a finish from a run that owns nothing got %v, want ErrEvaluationConflict", err)
	}
	err = FinishEvaluationClaim(ctx, db, "d1", "assign-absent", "run-a", 1, 0.25)
	if !errors.Is(err, ErrEvaluationNotFound) {
		t.Fatalf("finishing an assignment nobody claimed got %v, want ErrEvaluationNotFound", err)
	}
	err = ValidateEvaluationClaim(ctx, db, "d1", "assign-absent", "run-a", 1)
	if !errors.Is(err, ErrEvaluationNotFound) {
		t.Fatalf("validating an assignment nobody claimed got %v, want ErrEvaluationNotFound", err)
	}
}

// A provider can spend more than the worker reserved. The ledger's job is to
// say so, charge it, and let the next claim be judged against reality.
func TestEvaluationOverrunIsRecordedAndCharged(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()
	budget := sampleBudget(3, 10, 600)

	granted := mustClaim(t, db, sampleClaim("assign-1", "run-a", "inst-a", 1), budget)

	err := FinishEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", granted.Fence, 2.5)
	switch {
	case !errors.Is(err, ErrEvaluationOverrun):
		t.Fatalf("an overspending finish got %v, want ErrEvaluationOverrun", err)
	case !errors.Is(err, ErrEvaluationBudget):
		t.Fatalf("an overrun does not classify as a budget condition: %v", err)
	}
	// Reported, and recorded: replaying the receipt says the same thing rather
	// than succeeding quietly.
	if err := FinishEvaluationClaim(ctx, db, "d1", "assign-1", "run-a", granted.Fence, 2.5); !errors.Is(err, ErrEvaluationOverrun) {
		t.Fatalf("replaying an overrun receipt got %v, want the same overrun", err)
	}
	// And charged: 2.5 of the day's 3 is spent, so a second unit does not fit.
	if _, err := ClaimEvaluation(ctx, db, sampleClaim("assign-2", "run-b", "inst-b", 1), budget); !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("a claim after an overrun got %v, want ErrEvaluationBudget: the overspend "+
			"was recorded as if it stayed inside its reservation", err)
	}
}

// One authorized allowance means one allowance even when the hosts spending it
// disagree about what it is. A stale worker's larger number must not become the
// fleet's budget, and one policy version naming two ceilings must not be
// silently resolved in either direction.
func TestEvaluationAllowanceIsPinnedForTheDay(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()

	mustClaim(t, db, sampleClaim("assign-1", "run-a", "inst-a", 1), sampleBudget(3, 10, 600))

	// A stale host offering a larger ceiling still spends only the pinned
	// allowance, even when it reuses the same policy version label.
	mustClaim(t, db, sampleClaim("assign-2", "run-b", "inst-b", 1), sampleBudget(9, 10, 600))

	// A different version offering more does not raise the day: the pinned
	// three still binds, so the fourth unit is refused.
	generous := sampleClaim("assign-3", "run-c", "inst-c", 1)
	generous.PolicyVersion = "eval-2"
	mustClaim(t, db, generous, sampleBudget(9, 10, 600))
	fifth := sampleClaim("assign-5", "run-c", "inst-c", 1)
	fifth.PolicyVersion = "eval-2"
	if _, err := ClaimEvaluation(ctx, db, fifth, sampleBudget(9, 10, 600)); !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("a larger allowance from another policy version raised the day: %v", err)
	}

	// A different version offering less tightens it at once and fleet-wide,
	// which is what an operator cutting a budget mid-day means.
	tight := sampleClaim("assign-6", "run-d", "inst-d", 1)
	tight.PolicyVersion = "eval-3"
	if _, err := ClaimEvaluation(ctx, db, tight, sampleBudget(2, 10, 600)); !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("a tightened allowance did not take effect: %v", err)
	}
	// And it stays tightened for the host that still believes in the old one.
	stale := sampleClaim("assign-7", "run-e", "inst-e", 1)
	stale.PolicyVersion = "eval-2"
	if _, err := ClaimEvaluation(ctx, db, stale, sampleBudget(9, 10, 600)); !errors.Is(err, ErrEvaluationBudget) {
		t.Fatalf("a stale host reopened the day after it was tightened: %v", err)
	}
}

// The claim path refuses what the database would refuse, and names a caller bug
// as one rather than reporting it as contention. An id or kind PostgreSQL will
// not accept is a worker that can never make progress.
func TestEvaluationClaimRefusesUnusableInput(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	ctx := context.Background()
	budget := sampleBudget(10, 10, 600)

	for _, tc := range []struct {
		name   string
		mut    func(*EvaluationClaim)
		budget EvaluationBudget
	}{
		{"an assignment id that could escape an object key",
			func(c *EvaluationClaim) { c.ID = "../../etc/passwd" }, budget},
		{"a subject kind evaluation does not cover",
			func(c *EvaluationClaim) { c.SubjectKind = "session" }, budget},
		{"no policy version to pin the allowance to",
			func(c *EvaluationClaim) { c.PolicyVersion = "" }, budget},
		{"a negative reservation",
			func(c *EvaluationClaim) { c.ReservedCost = -1 }, budget},
		{"a deployment nothing registered",
			func(c *EvaluationClaim) { c.DeploymentID = "nobody" }, budget},
		{"no lease at all", func(*EvaluationClaim) {}, sampleBudget(10, 10, 0)},
		{"a lease that outlives the day it reserves against",
			func(*EvaluationClaim) {}, sampleBudget(10, 10, maxEvaluationLeaseSeconds+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := sampleClaim("assign-1", "run-a", "inst-a", 1)
			tc.mut(&c)
			if _, err := ClaimEvaluation(ctx, db, c, tc.budget); !errors.Is(err, ErrEvaluationInvalid) {
				t.Fatalf("got %v, want ErrEvaluationInvalid", err)
			}
		})
	}

	if ValidEvaluationSubjectKind("session") {
		t.Error("ValidEvaluationSubjectKind admits a kind the CHECK refuses")
	}
	for _, kind := range []string{"hypothesis", "observation", "finding", "proposal", "evaluation"} {
		if !ValidEvaluationSubjectKind(kind) {
			t.Errorf("ValidEvaluationSubjectKind rejects %q, which migrations/0013 admits", kind)
		}
	}
}

// The charge on a day is not rewritable. Everything above depends on it: a
// writer that could move a claim to another day, extend its own lease, or lower
// its reservation after the fact could spend the allowance twice.
func TestEvaluationClaimRowsAreImmutableExceptForTheFinish(t *testing.T) {
	db := newInternalDB(t)
	seedEvaluation(t, db)
	mustClaim(t, db, sampleClaim("assign-1", "run-a", "inst-a", 1), sampleBudget(10, 10, 600))

	for _, tc := range []struct {
		name string
		stmt string
	}{
		{"extending its own lease",
			`UPDATE evaluation_claims SET expires_at = expires_at + interval '1 hour'`},
		{"lowering the reservation after it was charged",
			`UPDATE evaluation_claims SET reserved_cost = 0`},
		{"moving the charge to another day",
			`UPDATE evaluation_claims SET day = day - 1`},
		{"re-attributing the work to another run",
			`UPDATE evaluation_claims SET run_id = 'run-z'`},
		{"raising the day's allowance",
			`UPDATE evaluation_budget_days SET daily_cost = daily_cost * 10`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec(tc.stmt); err == nil {
				t.Fatal("the database accepted it; the accounting is a convention rather than a guarantee")
			}
		})
	}
}
