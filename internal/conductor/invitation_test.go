package conductor_test

import (
	"context"
	"errors"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// stores is the durable Phase B state a conductor reads, opened over one
// temporary directory. These tests use the real stores rather than fakes,
// because consume-once is a property of internal/disposition's schema and a
// stub that reimplemented it would be testing the stub.
type stores struct {
	dir          string
	frontier     *frontier.Store
	runs         *runstore.Store
	dispositions *disposition.Store
}

func openStores(t *testing.T) *stores {
	t.Helper()
	dir := t.TempDir()
	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	t.Cleanup(func() { front.Close() })
	runs, err := runstore.Open(dir)
	if err != nil {
		t.Fatalf("run.Open: %v", err)
	}
	t.Cleanup(func() { runs.Close() })
	actions, err := disposition.Open(dir, front)
	if err != nil {
		t.Fatalf("disposition.Open: %v", err)
	}
	t.Cleanup(func() { actions.Close() })
	return &stores{dir: dir, frontier: front, runs: runs, dispositions: actions}
}

// plantHypothesis writes one candidate attributed to runID.
func (s *stores) plantHypothesis(t *testing.T, runID, statement string) frontier.Hypothesis {
	t.Helper()
	record, err := s.frontier.CreateHypothesis(context.Background(), frontier.HypothesisInput{
		RunID:   runID,
		Payload: frontier.HypothesisPayload{Statement: statement, Novelty: 0.5, Priority: 0.5},
	})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	return record
}

// plantInvitation leaves one operator "process this further" on a record.
func (s *stores) plantInvitation(t *testing.T, ref frontier.Ref, by string) disposition.Invitation {
	t.Helper()
	invitation, err := s.dispositions.Invite(context.Background(), disposition.InviteInput{
		Record: ref,
		By:     by,
	})
	if err != nil {
		t.Fatalf("Invite: %v", err)
	}
	return invitation
}

// plantReceipt records one run receipt over a one-session scope, with the cost
// its profile reported. It is the shape the budget reads: an estimate the worker
// declared, never a measurement Babel made.
func (s *stores) plantReceipt(t *testing.T, runID string, at time.Time,
	sessions []string, currency string, cost float64) runstore.Receipt {
	t.Helper()
	selection := make([]runstore.Selected, 0, len(sessions))
	for i, session := range sessions {
		harness, sourceID := splitSelector(t, session)
		selection = append(selection, runstore.Selected{
			Host:          "test-host",
			Harness:       harness,
			SourceID:      sourceID,
			CaptureDigest: digest.Digest(fixedDigest(i)),
			SourceDigest:  digest.Digest(fixedDigest(i + 100)),
			Adapter:       runstore.AdapterRef{Schema: 1, Version: "test", Completeness: []adapter.CompletenessReason{}},
		})
	}
	prep, err := runstore.NewPreparation(at.Add(-time.Minute), selection, runstore.PreparationContext{})
	if err != nil {
		t.Fatalf("NewPreparation: %v", err)
	}
	body := runstore.Body{
		Cookbook: []runstore.CookbookAsset{{
			Kind: runstore.AssetLens,
			Ref:  worker.RecipeRef{ID: "outcome-integrity", Version: 1},
		}},
		Job:    runstore.JobVersions{Job: 1, Prompt: "p1", Schema: worker.ResultSchema},
		Policy: runstore.PolicyVersions{Redaction: "r1", Disclosure: "d1"},
		Worker: &worker.Receipt{
			Profile: worker.ProfileRef{ID: "synthetic-profile", Revision: 1},
			Cost:    worker.Cost{Currency: currency, EstimatedRun: cost},
		},
		Timing: runstore.Timing{StartedAt: at.Add(-time.Second), FinishedAt: at},
	}
	receipt, err := runstore.NewReceipt(runstore.NewReceiptID(), runID, prep,
		runstore.Authority{Kind: runstore.AuthoritySerendipity, Ref: "draw:d-" + runID}, body, at)
	if err != nil {
		t.Fatalf("NewReceipt: %v", err)
	}
	if err := s.runs.PutReceipt(context.Background(), receipt); err != nil {
		t.Fatalf("PutReceipt: %v", err)
	}
	return receipt
}

func splitSelector(t *testing.T, selector string) (harness, sourceID string) {
	t.Helper()
	for i := range selector {
		if selector[i] == '/' {
			return selector[:i], selector[i+1:]
		}
	}
	t.Fatalf("selector %q is not harness/source-id", selector)
	return "", ""
}

// fixedDigest is a stable content digest for a planted selection entry. The
// bytes behind it do not exist; a preparation's identity is derived from these
// values rather than from reading them again.
func fixedDigest(n int) string {
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 64)
	for i := range out {
		out[i] = hexDigits[(i+n)%16]
	}
	return "sha256:" + string(out)
}

// An invitation is claimed before the run, so a conductor that dies mid-cycle
// leaves it consumed by a named run rather than open for a second one to take.
// The interrupted cycle is then resumed under that same run identity — which is
// what makes losing a conductor cost one amended receipt instead of a duplicate
// run over the operator's request.
func TestInvitationIsConsumedOnceAcrossACrash(t *testing.T) {
	ctx := context.Background()
	s := openStores(t)
	hypothesis := s.plantHypothesis(t, "run-earlier", "a candidate an operator wants developed")
	ref := frontier.Ref{Type: frontier.EntityHypothesis, ID: hypothesis.ID}
	s.plantReceipt(t, "run-earlier", day.Add(-24*time.Hour), []string{"omp/session-a"}, "USD", 0.10)
	invitation := s.plantInvitation(t, ref, "operator-alex")

	journalDir := t.TempDir()
	ladder := func(journal *conductor.Journal) []conductor.Rung {
		return conductor.DefaultLadder(
			conductor.NewInvitationRung(s.dispositions,
				conductor.NewRecordOrigins(s.frontier, s.runs)),
			// No duty is authorized, so rung two contributes nothing: this
			// scenario is about the operator's own queue.
			conductor.NewDutyRung(conductor.Duties{}, journal, nil, 0),
			conductor.NewSerendipityRung(fixedCorpus{"omp/session-a", "omp/session-b"},
				fixedRecipes{"effective-patterns"}, newTestRand(), 2),
		)
	}
	build := func(t *testing.T, runner conductor.Runner) *conductor.Conductor {
		t.Helper()
		journal, err := conductor.OpenJournal(journalDir)
		if err != nil {
			t.Fatalf("OpenJournal: %v", err)
		}
		loop, err := conductor.New(conductor.Config{
			Ceilings: testCeilings,
			// A wide floor, so the first cycle is the operator's invitation
			// rather than a chaotic draw.
			Floor:   conductor.Floor{OneIn: 100},
			Ladder:  ladder(journal),
			Runner:  runner,
			Ledger:  fakeLedger{},
			Journal: journal,
			Now:     (&clock{now: day}).Now,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return loop
	}

	// The first conductor claims the invitation and dies inside the run. The
	// cycle was journalled as running before the run started, so what it leaves
	// behind is exactly what a killed process leaves behind.
	var crashedRunID string
	crashing := &fakeRunner{hook: func(runID string, a conductor.Assignment) (conductor.Result, error) {
		crashedRunID = runID
		panic("the conductor was killed mid-run")
	}}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the planted crash did not happen")
			}
		}()
		loop := build(t, crashing)
		loop.Once(ctx) //nolint:errcheck // the runner panics
	}()
	if crashedRunID == "" {
		t.Fatal("the crashing cycle never reached the runner")
	}

	// The invitation is spent, and it names the run that took it.
	taken, err := s.dispositions.Invitation(ctx, invitation.ID)
	if err != nil {
		t.Fatalf("read the invitation: %v", err)
	}
	if taken.Open() {
		t.Fatal("the invitation is still open after a run claimed it")
	}
	if taken.ConsumedBy != crashedRunID {
		t.Errorf("invitation consumed by %q, want the crashed run %q", taken.ConsumedBy, crashedRunID)
	}
	open, err := s.dispositions.Invitations(ctx, disposition.InvitationFilter{})
	if err != nil {
		t.Fatalf("read the queue: %v", err)
	}
	if len(open) != 0 {
		t.Errorf("the open queue still holds %d invitations", len(open))
	}

	// The next conductor resumes rather than drawing again.
	resuming := &fakeRunner{}
	cycle, err := build(t, resuming).Once(ctx)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !cycle.Resumed {
		t.Error("the interrupted cycle was not resumed")
	}
	if cycle.RunID != crashedRunID {
		t.Errorf("resumed as run %q, want the interrupted %q", cycle.RunID, crashedRunID)
	}
	if cycle.Invitation != invitation.ID {
		t.Errorf("resumed cycle invitation = %q, want %q", cycle.Invitation, invitation.ID)
	}
	if cycle.Authority.Kind != runstore.AuthorityOperator ||
		cycle.Authority.Ref != "invitation:"+invitation.ID {
		t.Errorf("resumed authority = %+v, want the operator's invitation", cycle.Authority)
	}
	if len(resuming.runs) != 1 {
		t.Fatalf("the resumed conductor made %d runs", len(resuming.runs))
	}
	// One run identity across both attempts is the whole property: two would
	// mean the operator's invitation was spent twice.
	if resuming.runs[0].runID != crashedRunID {
		t.Errorf("resumed run id = %q, want %q", resuming.runs[0].runID, crashedRunID)
	}
	// And the corpus it was pointed at is the one the record came out of.
	if got := resuming.runs[0].assignment.Sessions; len(got) != 1 || got[0] != "omp/session-a" {
		t.Errorf("resumed over %v, want the originating session", got)
	}

	// A third conductor has nothing left on rung one: the invitation is gone
	// for good, so the cycle falls through to the floor.
	next, err := build(t, &fakeRunner{}).Once(ctx)
	if err != nil {
		t.Fatalf("third cycle: %v", err)
	}
	if next.Rung != conductor.RungSerendipity {
		t.Errorf("third cycle drew from %q, want the floor after a spent invitation", next.Rung)
	}
	if next.RunID == crashedRunID {
		t.Error("the third cycle reused the resumed run identity")
	}
}

// Two conductors racing for one invitation: exactly one gets it, and the loser
// treats the loss as an empty rung rather than as a failure. The work is being
// done either way.
func TestASecondConductorCannotTakeTheSameInvitation(t *testing.T) {
	ctx := context.Background()
	s := openStores(t)
	hypothesis := s.plantHypothesis(t, "run-earlier", "one candidate, two conductors")
	ref := frontier.Ref{Type: frontier.EntityHypothesis, ID: hypothesis.ID}
	invitation := s.plantInvitation(t, ref, "operator-alex")

	rung := conductor.NewInvitationRung(s.dispositions, conductor.NewRecordOrigins(s.frontier, s.runs))
	first, err := rung.Draw(ctx, conductor.DrawRequest{RunID: "run-one", At: day})
	if err != nil {
		t.Fatalf("first draw: %v", err)
	}
	if first.Invitation != invitation.ID {
		t.Errorf("first draw took %q", first.Invitation)
	}
	if _, err := rung.Draw(ctx, conductor.DrawRequest{RunID: "run-two", At: day}); !errors.Is(err, conductor.ErrNoWork) {
		t.Errorf("second draw error = %v, want ErrNoWork", err)
	}
	// A draw with no run to attribute the claim to is refused outright.
	if _, err := rung.Draw(ctx, conductor.DrawRequest{At: day}); err == nil {
		t.Error("an invitation was claimed in the name of no run")
	}
}

// An invitation scopes its cycle to the sessions the record came out of, and to
// the recipe that produced it when the record names one. Processing a record
// "further" over a corpus it never touched would be a different investigation
// wearing the operator's request.
func TestInvitationScopesTheCycleToTheRecordsOrigin(t *testing.T) {
	ctx := context.Background()
	s := openStores(t)
	s.plantReceipt(t, "run-origin", day.Add(-time.Hour),
		[]string{"omp/session-a", "codex/session-b"}, "USD", 0.20)
	hypothesis := s.plantHypothesis(t, "run-origin", "a candidate from a two-session run")
	invitation := s.plantInvitation(t,
		frontier.Ref{Type: frontier.EntityHypothesis, ID: hypothesis.ID}, "operator-alex")

	rung := conductor.NewInvitationRung(s.dispositions, conductor.NewRecordOrigins(s.frontier, s.runs))
	a, err := rung.Draw(ctx, conductor.DrawRequest{RunID: "run-cycle", At: day})
	if err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if len(a.Sessions) != 2 || a.Sessions[0] != "codex/session-b" || a.Sessions[1] != "omp/session-a" {
		t.Errorf("cycle scoped to %v, want both originating sessions", a.Sessions)
	}
	// A candidate is a frontier root, so the run starts from the record the
	// operator pointed at rather than from broad discovery.
	if len(a.Roots) != 1 || a.Roots[0] != hypothesis.ID {
		t.Errorf("cycle roots = %v, want the invited candidate", a.Roots)
	}
	if a.Authority.Ref != "invitation:"+invitation.ID {
		t.Errorf("authority = %+v", a.Authority)
	}
	if a.Note == "" || !containsAll(a.Note, "operator-alex", hypothesis.ID) {
		t.Errorf("note = %q, which does not say who asked for what", a.Note)
	}

	// An unknown record does not resolve to a scope: a cycle over a guessed
	// corpus would make the receipt's scope fiction.
	origins := conductor.NewRecordOrigins(s.frontier, s.runs)
	if _, err := origins.Origin(ctx, frontier.Ref{Type: frontier.EntityObservation, ID: "obs-missing"}); err == nil {
		t.Error("an unknown observation resolved to an origin")
	}
	if _, err := origins.Origin(ctx, frontier.Ref{Type: "invention", ID: "x-1"}); err == nil {
		t.Error("a record kind this build does not resolve produced an origin")
	}
}

// An observation carries the recipe that produced it, so an invitation on one
// re-runs that lens rather than the build's current defaults. A §4.3-valid
// observation needs evidence whose locator recovers real bytes, so the recipe
// path is exercised against the frontier interface the resolver actually reads.
func TestObservationOriginCarriesItsRecipe(t *testing.T) {
	ctx := context.Background()
	s := openStores(t)
	s.plantReceipt(t, "run-origin", day.Add(-time.Hour), []string{"omp/session-a"}, "USD", 0.20)

	origins := conductor.NewRecordOrigins(recipeFrontier{
		observation: frontier.Observation{
			ID:            "obs-1",
			RunID:         "run-origin",
			RecipeID:      "durable-operator-model",
			RecipeVersion: 1,
		},
	}, s.runs)
	origin, err := origins.Origin(ctx, frontier.Ref{Type: frontier.EntityObservation, ID: "obs-1"})
	if err != nil {
		t.Fatalf("Origin: %v", err)
	}
	if origin.Recipe != "durable-operator-model" {
		t.Errorf("origin recipe = %q, want the lens that produced the claim", origin.Recipe)
	}
	if len(origin.Sessions) != 1 || origin.Sessions[0] != "omp/session-a" {
		t.Errorf("origin sessions = %v, want the originating run's scope", origin.Sessions)
	}
}

// recipeFrontier answers with one planted observation and refuses everything
// else, so a test can reach the recipe-hint path without manufacturing a
// locator-backed claim.
type recipeFrontier struct{ observation frontier.Observation }

func (f recipeFrontier) Hypothesis(context.Context, string) (frontier.Hypothesis, error) {
	return frontier.Hypothesis{}, frontier.ErrUnknownEntity
}

func (f recipeFrontier) Observation(_ context.Context, id string) (frontier.Observation, error) {
	if id != f.observation.ID {
		return frontier.Observation{}, frontier.ErrUnknownEntity
	}
	return f.observation, nil
}

func (f recipeFrontier) Finding(context.Context, string) (frontier.Finding, error) {
	return frontier.Finding{}, frontier.ErrUnknownEntity
}

func (f recipeFrontier) Proposal(context.Context, string) (frontier.Proposal, error) {
	return frontier.Proposal{}, frontier.ErrUnknownEntity
}

// A record whose originating run left no receipt is still a record the operator
// pointed at. The cycle runs over the whole host rather than refusing their
// invitation over a gap in Babel's own bookkeeping.
func TestOriginWithNoReceiptIsAWholeHostCycle(t *testing.T) {
	ctx := context.Background()
	s := openStores(t)
	hypothesis := s.plantHypothesis(t, "run-with-no-receipt", "a candidate whose run left no receipt")
	origin, err := conductor.NewRecordOrigins(s.frontier, s.runs).
		Origin(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: hypothesis.ID})
	if err != nil {
		t.Fatalf("Origin: %v", err)
	}
	if len(origin.Sessions) != 0 {
		t.Errorf("origin sessions = %v, want none, which means the whole host", origin.Sessions)
	}
}

// The ceilings are enforced against what the receipts recorded, not against the
// loop's own memory: a run an operator started by hand spends the same budget,
// and a restart must not forget the day.
func TestReceiptLedgerSumsTodaysEstimates(t *testing.T) {
	ctx := context.Background()
	s := openStores(t)
	s.plantReceipt(t, "run-today-1", day, []string{"omp/session-a"}, "USD", 0.25)
	s.plantReceipt(t, "run-today-2", day.Add(time.Hour), []string{"omp/session-b"}, "USD", 0.30)
	// Quoted in a currency the ceilings are not in: Babel holds no exchange
	// rate, so it is unpriced rather than converted.
	s.plantReceipt(t, "run-today-3", day.Add(2*time.Hour), []string{"omp/session-c"}, "EUR", 9.00)
	// A profile that reported no currency at all.
	s.plantReceipt(t, "run-today-4", day.Add(3*time.Hour), []string{"omp/session-d"}, "", 5.00)
	// Yesterday, which is outside the window the per-day ceiling covers.
	s.plantReceipt(t, "run-yesterday", day.Add(-20*time.Hour), []string{"omp/session-e"}, "USD", 4.00)

	ledger := conductor.NewReceiptLedger(s.runs)
	spend, err := ledger.SpentSince(ctx, conductor.StartOfDay(day.Add(4*time.Hour)), "USD")
	if err != nil {
		t.Fatalf("SpentSince: %v", err)
	}
	if spend.Amount < 0.549 || spend.Amount > 0.551 {
		t.Errorf("today's spend = %.3f, want 0.55", spend.Amount)
	}
	if spend.Runs != 2 {
		t.Errorf("priced runs = %d, want 2", spend.Runs)
	}
	if spend.Unpriced != 2 {
		t.Errorf("unpriced runs = %d, want 2: an unpriceable run is not a free one", spend.Unpriced)
	}
	ceilings := conductor.Ceilings{Currency: "USD", PerCycle: 0.50, PerDay: 5.00}
	if got := spend.Remaining(ceilings); got < 4.449 || got > 4.451 {
		t.Errorf("remaining = %.3f, want 4.45", got)
	}
}

// newTestRand is a seeded generator, so a test that draws is reproducible.
func newTestRand() *rand.Rand { return rand.New(rand.NewPCG(42, 43)) }

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
