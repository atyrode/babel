package conductor_test

import (
	"errors"
	"testing"

	"github.com/atyrode/babel/internal/conductor"
	runstore "github.com/atyrode/babel/internal/run"
)

func TestAttemptClaimedAfterAdmissionLeavesCyclePending(t *testing.T) {
	journal := testJournal(t)
	runner := &fakeRunner{err: runstore.ErrAttemptOwned}
	rung := &stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{Authority: runstore.Authority{Kind: runstore.AuthorityOperator, Ref: "invitation:claimed"}}}
	loop, err := conductor.New(conductor.Config{Ceilings: testCeilings, Ladder: []conductor.Rung{rung}, Runner: runner, Ledger: fakeLedger{}, Journal: journal, Now: (&clock{now: day}).Now})
	if err != nil {
		t.Fatal(err)
	}
	first, err := loop.Once(t.Context())
	if !errors.Is(err, runstore.ErrAttemptOwned) {
		t.Fatalf("admission race = %v", err)
	}
	last, ok := journal.Last()
	if !ok || last.Outcome != conductor.OutcomeRunning || last.RunID != first.RunID {
		t.Fatal("lease contention permanently failed the claimed cycle")
	}
	second, err := loop.Once(t.Context())
	if !errors.Is(err, runstore.ErrAttemptOwned) {
		t.Fatalf("pending retry = %v", err)
	}
	if second.RunID != first.RunID || second.Seq != first.Seq || rung.draws != 1 {
		t.Fatal("pending recovery abandoned the original assignment")
	}
}
