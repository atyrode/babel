package conductor_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/conductor"
	runstore "github.com/atyrode/babel/internal/run"
)

// fakePublisher is the shared backend as the cycle boundary reaches it.
type fakePublisher struct {
	report  conductor.Publication
	err     error
	calls   int
	lastErr error
}

func (p *fakePublisher) Publish(context.Context) (conductor.Publication, error) {
	p.calls++
	if p.err != nil {
		return conductor.Publication{}, p.err
	}
	return p.report, nil
}

func publishingLoop(t *testing.T, pub conductor.Publisher, journal *conductor.Journal) *conductor.Conductor {
	t.Helper()
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings, Floor: conductor.Floor{OneIn: 1},
		Ladder: []conductor.Rung{&stubRung{name: conductor.RungSerendipity, work: &conductor.Assignment{
			Authority: runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:publish"},
		}}},
		Runner: &fakeRunner{result: conductor.Result{ReceiptID: "rcpt-1"}},
		Ledger: fakeLedger{}, Journal: journal, Publisher: pub, Now: (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return loop
}

// A cycle publishes what it made durable, and says what the machine still
// owes. A loop that committed records locally and never published them is a
// machine whose analysis nobody else can read.
func TestACycleReportsWhatItPublishedAndWhatIsStillOwed(t *testing.T) {
	pub := &fakePublisher{report: conductor.Publication{Published: 7, Pending: 2, Runs: 1}}
	journal := testJournal(t)
	cycle, err := publishingLoop(t, pub, journal).Once(t.Context())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if pub.calls != 1 {
		t.Fatalf("the cycle boundary published %d times, want once", pub.calls)
	}
	if cycle.Published != 7 || cycle.PendingRecords != 2 {
		t.Errorf("cycle reported %d published and %d pending, want 7 and 2",
			cycle.Published, cycle.PendingRecords)
	}
	// The journal is what `conductor status` reads, so the figures have to be
	// in the record rather than only in the value this call returned.
	last, ok := journal.Last()
	if !ok || last.Published != 7 || last.PendingRecords != 2 {
		t.Errorf("the journal recorded %+v", last)
	}
}

// A publication failure does not fail the cycle. The records are durable here
// and staged as owed, which is sync's own contract: a cycle that reported
// itself failed because a remote endpoint blinked would teach an operator to
// distrust the one column the loop exists to keep honest.
func TestAPublicationFailureLeavesTheCycleSuccessfulAndTheRecordsPending(t *testing.T) {
	pub := &fakePublisher{err: errors.New("dial the shared catalog: connection refused")}
	journal := testJournal(t)
	cycle, err := publishingLoop(t, pub, journal).Once(t.Context())
	if err != nil {
		t.Fatalf("a publication failure failed the cycle: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeRan {
		t.Errorf("cycle ended as %q, want ran: its receipt was written before publication was tried", cycle.Outcome)
	}
	if cycle.ReceiptID != "rcpt-1" {
		t.Errorf("cycle recorded receipt %q, want the one its run wrote", cycle.ReceiptID)
	}
	if !strings.Contains(cycle.PublishNote, "connection refused") {
		t.Errorf("the cycle does not say why nothing published: %q", cycle.PublishNote)
	}
	last, ok := journal.Last()
	if !ok || last.Outcome != conductor.OutcomeRan || last.PublishNote == "" {
		t.Errorf("the journal recorded %+v, want a successful cycle whose records are visibly unpublished", last)
	}
}

// A loop with no publisher is a local-only deployment, which owes the fleet
// nothing and must schedule identically.
func TestALoopWithNoPublisherRunsIdentically(t *testing.T) {
	journal := testJournal(t)
	cycle, err := publishingLoop(t, nil, journal).Once(t.Context())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Outcome != conductor.OutcomeRan || cycle.PublishNote != "" ||
		cycle.Published != 0 || cycle.PendingRecords != 0 {
		t.Errorf("a local-only cycle reported publication state: %+v", cycle)
	}
}
