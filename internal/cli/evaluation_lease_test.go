package cli

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
)

// What this file defends is the lifetime of the renewal loop, which is the
// half of lease renewal that is a concurrency contract rather than a store
// rule: the ticker has to renew while the review runs, stop when it ends, and
// stop for good when the claim is no longer this run's to extend.
//
// The renewal's semantics - who may extend, by how much, and what an expired
// claim is answered with - belong to internal/evaluation and are asserted
// there.

// renewalRecorder counts renewals and can refuse them the way the coordinator
// does.
type renewalRecorder struct {
	mu     sync.Mutex
	calls  int
	answer error
}

func (r *renewalRecorder) renew(context.Context) (time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.answer != nil {
		return time.Time{}, r.answer
	}
	return time.Now().Add(time.Minute), nil
}

func (r *renewalRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// keeper builds the loop under test with a tick short enough for a test and a
// renewal the test controls.
func (r *renewalRecorder) keeper(a *app) leaseKeeper {
	return leaseKeeper{
		id:    "eval-a-test",
		every: 2 * time.Millisecond,
		renew: r.renew,
		diag:  a.diagf,
	}
}

// A long review renews repeatedly while it runs, and not once after it ends.
// The second half is the part that matters: a renewal that outlived its review
// would be this process holding an assignment nothing is working on, which is
// exactly what expiry exists to release.
func TestLeaseKeeperRenewsWhileTheReviewRunsAndStopsWithIt(t *testing.T) {
	recorder := &renewalRecorder{}
	a := &app{stdout: io.Discard, stderr: io.Discard}
	stop := recorder.keeper(a).start(t.Context())

	deadline := time.Now().Add(2 * time.Second)
	for recorder.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	renewed := recorder.count()
	if renewed < 3 {
		t.Fatalf("the keeper renewed %d times in two seconds, want a repeating ticker", renewed)
	}

	stop()
	settled := recorder.count()
	time.Sleep(20 * time.Millisecond)
	if after := recorder.count(); after != settled {
		t.Fatalf("%d renewals arrived after stop returned, so the loop outlived the review",
			after-settled)
	}
}

// A refusal that will stay refused ends the loop. Renewal is a heartbeat, so a
// keeper that kept ticking against a claim another run holds would repeat one
// sentence every interval for the rest of the review and never regain the
// authority it lost.
func TestLeaseKeeperStopsWhenTheClaimIsNoLongerHeld(t *testing.T) {
	for _, answer := range []error{
		fmt.Errorf("%w: assignment was taken over", evaluation.ErrConflict),
		fmt.Errorf("%w: assignment is unknown here", evaluation.ErrNotFound),
		fmt.Errorf("%w: this authority cannot extend a lease", evaluation.ErrUnavailable),
	} {
		recorder := &renewalRecorder{answer: answer}
		a := &app{stdout: io.Discard, stderr: io.Discard}
		stop := recorder.keeper(a).start(t.Context())
		deadline := time.Now().Add(2 * time.Second)
		for recorder.count() < 1 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		time.Sleep(20 * time.Millisecond)
		if calls := recorder.count(); calls != 1 {
			t.Fatalf("a %v refusal was retried %d times, want the loop to end", answer, calls)
		}
		stop()
	}
}

// A transient failure is not a lost claim, so the keeper tries again: a busy
// database for one tick must not spend the rest of the review not renewing.
func TestLeaseKeeperRetriesATransientRefusal(t *testing.T) {
	recorder := &renewalRecorder{answer: fmt.Errorf("database is locked")}
	a := &app{stdout: io.Discard, stderr: io.Discard}
	stop := recorder.keeper(a).start(t.Context())
	defer stop()

	deadline := time.Now().Add(2 * time.Second)
	for recorder.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if calls := recorder.count(); calls < 3 {
		t.Fatalf("a transient failure stopped the keeper after %d renewals", calls)
	}
}

// The cadence is a third of the authorized lease, and a grant this instance
// cannot read a policy for still renews on the window it was given.
func TestLeaseRenewalIntervalIsAThirdOfTheLease(t *testing.T) {
	if got := renewalInterval(900 * time.Second); got != 300*time.Second {
		t.Fatalf("interval for a 15 minute lease = %s, want 5m0s", got)
	}
	if got := renewalInterval(time.Second); got != time.Second {
		t.Fatalf("interval for a one second lease = %s, want one second: a shorter tick is a busy loop", got)
	}
	if got := renewalInterval(0); got != 0 {
		t.Fatalf("interval for no lease = %s, want none: there is nothing to keep alive", got)
	}
}
