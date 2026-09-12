package web

// Issue #235's record peel, held to what a reader observes.
//
// Every test here asks a question the operator asked in #234: open the record
// I just clicked, tell me where it stands, let me check what it rests on, and
// let me say what I think of it without leaving the page. What the assembly
// reads to answer is not asserted anywhere — the point of the peel is that a
// reader stops having to know.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// peelText is woven through the fixtures, so every assertion below is against
// wording the fixture chose rather than against a field being non-empty.
const peelText = "peeled to the claim"

// TestTheRecordPeelOpensARecordOnlyTheDeploymentHolds is the 404-on-merged-
// record regression, at the route a reader now arrives on.
//
// The listings are deployment-wide, so a row an operator clicks routinely
// names a record this machine's durable store has never held. Answering that
// click with "no record with that identifier" reads as Babel having lost the
// finding, which is what #234 recorded as the cost.
func TestTheRecordPeelOpensARecordOnlyTheDeploymentHolds(t *testing.T) {
	committed := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	h := newPhaseB(t, peelText, func(o *Options) {
		o.Fleet = &fakeFleet{
			local:  localFleetHost,
			states: map[string]string{},
			records: []fleet.Record{fixtureProposal("pro_elsewhere", "frun-remote",
				remoteFleetHost, "the other laptop "+peelText, "inst-remote", &committed, peelText)},
		}
	})

	var peel recordPeel
	decodeResponse(t, h.ok(t, "/api/record/pro_elsewhere"), &peel)
	if peel.ID != "pro_elsewhere" || peel.Kind != string(frontier.EntityProposal) {
		t.Fatalf("peel identifies %s/%s", peel.Kind, peel.ID)
	}
	// The claim is the proposal's own proposed outcome, which is what depth
	// one asks a reader to decide about.
	if peel.Claim != "one read, threaded through the deploy steps" {
		t.Errorf("claim = %q, want the record's own wording", peel.Claim)
	}
	if !strings.Contains(peel.Title, peelText) {
		t.Errorf("title = %q, want the line the listing row showed", peel.Title)
	}
	if peel.Case == nil || peel.Case.Problem == "" {
		t.Errorf("case = %+v, want the argument the reader opened this for", peel.Case)
	}
	// The derivations this machine makes over records it holds are absent
	// rather than zero. A standing of `new` would say nobody has ruled on a
	// record whose decisions live on the machine that published it, and an
	// action beside it would offer a ruling this surface cannot record.
	if peel.Standing != nil || peel.Action != nil {
		t.Errorf("standing = %+v and action = %+v, want both absent", peel.Standing, peel.Action)
	}
	if peel.Machinery == nil || peel.Machinery.Host != remoteFleetHost {
		t.Errorf("machinery = %+v, want the machine that published it", peel.Machinery)
	}
	if peel.Notice != "" {
		t.Errorf("a record that opened carries a notice: %q", peel.Notice)
	}

	// `?fleet=0` is the one question on this surface genuinely about this
	// machine, and a record it does not hold is legitimately absent.
	narrowed := h.get("/api/record/pro_elsewhere?fleet=0")
	defer narrowed.Body.Close()
	if narrowed.StatusCode != http.StatusNotFound {
		t.Errorf("narrowed to this machine: status = %d, want 404", narrowed.StatusCode)
	}
}

// TestTheRecordPeelSaysWhatTheDeploymentCouldNotAnswer is the outage half.
//
// A catalog that did not answer must cost the deployment's facts about the
// record and nothing else: every word this machine holds still renders, and
// the page says what it could not consult rather than presenting a partial
// answer as a whole one. The sentence is about the record, because that is
// what the reader asked about.
func TestTheRecordPeelSaysWhatTheDeploymentCouldNotAnswer(t *testing.T) {
	h := newPhaseB(t, peelText, func(o *Options) { o.FleetError = leakyError })

	var peel recordPeel
	body := jsonBody(t, h.ok(t, "/api/record/"+h.proposal.ID), &peel)
	if !strings.Contains(peel.Claim, peelText) {
		t.Errorf("claim = %q: an unreachable catalog took this machine's own record away", peel.Claim)
	}
	if peel.Standing == nil {
		t.Error("an unreachable catalog took away a standing this machine derives itself")
	}
	if peel.Notice == "" {
		t.Fatal("the deployment could not be consulted and the page does not say so")
	}
	if peel.Machinery != nil && (peel.Machinery.Digest != "" || peel.Machinery.Host != "") {
		t.Errorf("machinery = %+v, want no published identity from a catalog that did not answer",
			peel.Machinery)
	}
	assertRecordTerms(t, "/api/record/"+h.proposal.ID, peel.Notice)
	// The catalog's own error carries a path and a connection string, and no
	// part of it may reach the browser in any field at all.
	if strings.Contains(body, "postgres://") || strings.Contains(body, "durable.db") {
		t.Error("the response repeats the catalog's error text")
	}
}

// TestTheRecordPeelOffersNoRulingOnEvidence holds the reading surface to
// §6.7's line.
//
// An observation is the evidence a finding consolidates, not an artifact
// anybody accepts or rejects. A page that showed one as `new` with a button
// beside it would be offering an act internal/review refuses, and the reader
// would learn that Babel's own vocabulary does not mean what it says.
func TestTheRecordPeelOffersNoRulingOnEvidence(t *testing.T) {
	h := newPhaseB(t, peelText, nil)

	var observation recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+h.observationID(t)), &observation)
	if observation.Standing != nil || observation.Action != nil {
		t.Errorf("an observation offers standing %+v and action %+v",
			observation.Standing, observation.Action)
	}
	if !strings.Contains(observation.Claim, peelText) {
		t.Errorf("claim = %q, want the observation's own claim", observation.Claim)
	}

	// A proposal is reviewable, undecided, and therefore asks for exactly one
	// thing.
	var proposal recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+h.proposal.ID), &proposal)
	if proposal.Standing == nil || proposal.Standing.Label != string(frontier.ReviewNew) {
		t.Fatalf("proposal standing = %+v", proposal.Standing)
	}
	if proposal.Action == nil || proposal.Action.Verb != "rule" {
		t.Fatalf("proposal action = %+v, want the one act it wants", proposal.Action)
	}
}

// TestTheRecordPeelOpensEvidenceAtTheCitedLine is depth three's whole promise:
// a citation a reader can follow to the conversation it came from.
//
// The event index is the cited line minus one. internal/event stamps a 1-based
// record line and internal/transcript numbers the same records from zero, and
// a page that shipped the line as the position would land every citation one
// record late — close enough to look right and wrong on every one.
func TestTheRecordPeelOpensEvidenceAtTheCitedLine(t *testing.T) {
	h := newPhaseB(t, peelText, func(o *Options) {
		o.Lister = SessionListerFunc(func(context.Context) (SessionsResult, error) {
			return SessionsResult{Sessions: []SessionRow{{
				Harness: "omp", SourceID: "session-a", Selector: "omp/session-a",
			}}}, nil
		})
	})

	var peel recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+h.observationID(t)), &peel)
	if len(peel.Evidence) != 1 {
		t.Fatalf("evidence = %+v, want the one citation the claim rests on", peel.Evidence)
	}
	cited := peel.Evidence[0]
	if cited.SessionID != "omp/session-a" {
		t.Errorf("session = %q, want the selector the session page routes on", cited.SessionID)
	}
	if cited.Line != 12 || cited.Event != 11 {
		t.Errorf("line %d lands on event %d, want 12 and 11", cited.Line, cited.Event)
	}
	if cited.Href != "#/sessions/omp%2Fsession-a?event=11" {
		t.Errorf("href = %q", cited.Href)
	}
	if !strings.Contains(cited.Quote, peelText) {
		t.Errorf("quote = %q, want what the citing record says the bytes show", cited.Quote)
	}
	if cited.Kind != evidenceDirect {
		t.Errorf("kind = %q, want %q", cited.Kind, evidenceDirect)
	}

	// A host with no session catalog leaves the citation as the text it has
	// always been rather than as a link into nothing.
	unlisted := newPhaseB(t, peelText, nil)
	var bare recordPeel
	decodeResponse(t, unlisted.ok(t, "/api/record/"+unlisted.observationID(t)), &bare)
	if len(bare.Evidence) != 1 || bare.Evidence[0].Href != "" || bare.Evidence[0].SessionID != "" {
		t.Errorf("an unresolvable citation renders as a link: %+v", bare.Evidence)
	}
	if bare.Evidence[0].Line != 12 {
		t.Error("an unresolvable citation dropped its locator, which is what makes it evidence")
	}
}

// TestTheRecordPeelQuotesTheCitedBytes is the direction's fourth complaint,
// answered.
//
// The best moment in the corpus was behind a link: the human sentence a
// proposal grew out of, reachable in one click and invisible on the page,
// which showed a model's note *about* the sentence instead. So the excerpt is
// the hero and the note is a gloss beneath it — and because the bytes are
// quoted rather than paraphrased, what is quoted has to be provably the bytes
// the record cited.
//
// Three states, because absence means three different things. A session this
// host holds and can open quotes the record and names the conversation; a
// session it holds but whose log is gone keeps the origin and the note and
// quotes nothing; a session it has never had has neither. None of the three
// may render an empty quotation, because a blank pull-quote reads as somebody
// who said nothing.
func TestTheRecordPeelQuotesTheCitedBytes(t *testing.T) {
	said := "it's too hard to copy paste multi line commands, perhaps create a script " + peelText
	_, locator := writeCitedSession(t, said)
	row := SessionRow{
		Harness:  "omp",
		SourceID: "cited-session",
		Selector: "omp/cited-session",
		Title:    new("fixing the release pipeline " + peelText),
		Modified: new("2026-05-01T00:00:00Z"),
	}
	row.Workspace = new("/synthetic/workspace")
	row.CostUSD = new(1.25)
	row.Turns = new(int64(42))

	h := newPhaseB(t, peelText, func(o *Options) {
		o.Lister = SessionListerFunc(func(context.Context) (SessionsResult, error) {
			return SessionsResult{Sessions: []SessionRow{row}}, nil
		})
	})
	readable := h.citingObservation(t, locator)

	var peel recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+readable), &peel)
	if len(peel.Evidence) != 1 {
		t.Fatalf("evidence = %+v, want the one citation", peel.Evidence)
	}
	cited := peel.Evidence[0]
	if cited.Excerpt != said {
		t.Errorf("excerpt = %q, want the words at the cited locator", cited.Excerpt)
	}
	if cited.Speaker != speakerUser {
		t.Errorf("speaker = %q, want the person who said it", cited.Speaker)
	}
	if !strings.Contains(cited.SessionTitle, peelText) {
		t.Errorf("session title = %q, want the conversation's own title", cited.SessionTitle)
	}
	if !strings.Contains(cited.Quote, "the note about it") {
		t.Errorf("quote = %q, want the citing record's note kept beneath the excerpt", cited.Quote)
	}
	if peel.Origin == nil {
		t.Fatalf("no origin: the record cites a session this host holds")
	}
	if peel.Origin.SessionID != "omp/cited-session" || peel.Origin.Workspace != "/synthetic/workspace" {
		t.Errorf("origin = %+v, want the session's own identity and workspace", peel.Origin)
	}
	if peel.Origin.CostUSD == nil || *peel.Origin.CostUSD != 1.25 ||
		peel.Origin.Turns == nil || *peel.Origin.Turns != 42 {
		t.Errorf("origin usage = %+v, want what the catalog measured", peel.Origin)
	}
	if peel.Origin.Href != "#/sessions/omp%2Fcited-session?event=1" {
		t.Errorf("origin href = %q, want the transcript at the cited record", peel.Origin.Href)
	}

	// The same citation with its log gone. The conversation is still the
	// record's origin — that is a catalog fact — and the bytes are not
	// recoverable, so the excerpt is absent rather than empty.
	missing := locator
	missing.Path = "/nowhere/cited-session.jsonl"
	var gone recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+h.citingObservation(t, missing)), &gone)
	if len(gone.Evidence) != 1 || gone.Evidence[0].Excerpt != "" || gone.Evidence[0].Speaker != "" {
		t.Errorf("an unreadable log still quoted: %+v", gone.Evidence)
	}
	if gone.Evidence[0].Quote == "" || gone.Origin == nil {
		t.Errorf("an unreadable log cost the note or the origin: %+v", gone)
	}

	// A host that has never held the session has neither.
	unlisted := newPhaseB(t, peelText, nil)
	var bare recordPeel
	decodeResponse(t, unlisted.ok(t, "/api/record/"+unlisted.citingObservation(t, locator)), &bare)
	if bare.Origin != nil {
		t.Errorf("origin = %+v, want none: this host holds no such session", bare.Origin)
	}
	if len(bare.Evidence) != 1 || bare.Evidence[0].Excerpt != "" {
		t.Errorf("excerpt without a catalog row: %+v", bare.Evidence)
	}
}

// TestTheRecordPeelNamesTheRunsOwnOutputs is the siblings strip, held to its
// one claim: these records were written by this run, and nothing else is.
//
// The relation has been on every record since the frontier's first migration
// and no query could ask it, so the failure mode worth testing is not an empty
// list — it is a list that quietly includes the rest of the frontier, which is
// what a scan with a forgotten predicate produces.
func TestTheRecordPeelNamesTheRunsOwnOutputs(t *testing.T) {
	h := newPhaseB(t, peelText, nil)
	// One record from another run, on the same claim, so a siblings list
	// that answered "what touches this" rather than "what this run wrote"
	// would pick it up.
	stranger, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID:   "run-2",
		Payload: frontier.HypothesisPayload{Statement: "another run's candidate " + peelText, Novelty: 0.3, Priority: 0.3},
	})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}

	var peel recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+h.finding.ID), &peel)
	if peel.Related == nil || len(peel.Related.Siblings) == 0 {
		t.Fatalf("related = %+v, want the rest of run-1's output", peel.Related)
	}
	kinds := map[string]string{}
	for _, sibling := range peel.Related.Siblings {
		if sibling.ID == h.finding.ID {
			t.Error("the siblings list offers a link to the page the reader is on")
		}
		if sibling.ID == stranger.ID {
			t.Error("a record from another run is listed as made in the same run")
		}
		if sibling.Title == "" {
			t.Errorf("sibling %s has no line of its own", sibling.ID)
		}
		kinds[sibling.ID] = sibling.Kind
	}
	if kinds[h.proposal.ID] != string(frontier.EntityProposal) {
		t.Errorf("the run's proposal is missing or miskinded: %+v", kinds)
	}
	if kinds[h.hypothesis.ID] != string(frontier.EntityHypothesis) {
		t.Errorf("the run's candidate is missing or miskinded: %+v", kinds)
	}
}

// TestTheRecordPeelPricesTheRunThatWroteIt is depth five's one number about
// Babel rather than about the corpus.
//
// The rule it holds is the one a dollar figure makes dangerous: an engine that
// never reported its own accounting leaves a receipt whose usage is zeros, and
// rendering that as $0.00 would state that producing the record was free. So a
// priced run carries a figure and an unpriced one carries none, while the
// tokens, the model and the duration still travel.
func TestTheRecordPeelPricesTheRunThatWroteIt(t *testing.T) {
	priced := newPhaseB(t, peelText, func(o *Options) {
		o.Receipts = fakeRecordReceipts{usage: &worker.Usage{
			InputTokens: 1200, OutputTokens: 340, Cost: 0.42,
		}}
	})
	var peel recordPeel
	decodeResponse(t, priced.ok(t, "/api/record/"+priced.proposal.ID), &peel)
	if peel.Machinery == nil || peel.Machinery.Cost == nil {
		t.Fatalf("machinery = %+v, want what the producing run cost", peel.Machinery)
	}
	cost := peel.Machinery.Cost
	if cost.USD == nil || *cost.USD != 0.42 {
		t.Errorf("cost = %+v, want the receipt's own figure", cost)
	}
	if cost.InputTokens != 1200 || cost.OutputTokens != 340 {
		t.Errorf("tokens = %+v", cost)
	}
	if cost.Model != "claude-opus-5" || cost.DurationS <= 0 {
		t.Errorf("cost = %+v, want the resolved model and Babel's own clock", cost)
	}

	unpriced := newPhaseB(t, peelText, func(o *Options) {
		o.Receipts = fakeRecordReceipts{usage: &worker.Usage{InputTokens: 1200}}
	})
	var quiet recordPeel
	decodeResponse(t, unpriced.ok(t, "/api/record/"+unpriced.proposal.ID), &quiet)
	if quiet.Machinery == nil || quiet.Machinery.Cost == nil {
		t.Fatalf("an unpriced run lost its cost block entirely: %+v", quiet.Machinery)
	}
	if quiet.Machinery.Cost.USD != nil {
		t.Errorf("usd = %v, want none: nobody priced this run", *quiet.Machinery.Cost.USD)
	}
}

// TestTheReceptionTalliesPerRoleAndContestsOnlyWithinOne is the tally that
// means something.
//
// A role is what a reviewer was authorized to answer (§4.12), so two supports
// under two roles are two answers to two questions: a record with one
// satisfied evidence check and one satisfied relevance check read as broadly
// supported when the surface summed them. Disagreement matters in the other
// direction — two reviewers answering the *same* question differently is the
// one thing a flat tally cannot say — so `contested` is per role and not
// across the reception.
func TestTheReceptionTalliesPerRoleAndContestsOnlyWithinOne(t *testing.T) {
	within := newPhaseB(t, peelText, func(o *Options) {
		o.Evaluation = reviewedBy(peelText,
			assessed("evidence", "support", ""),
			assessed("evidence", "oppose", "the locator does not show what the claim says "+peelText),
			assessed("relevance", "support", ""))
	})
	var peel recordPeel
	decodeResponse(t, within.ok(t, "/api/record/"+within.proposal.ID), &peel)
	if peel.Reception == nil || len(peel.Reception.ByRole) != 2 {
		t.Fatalf("by_role = %+v, want one row per question asked", peel.Reception)
	}
	evidence := peel.Reception.ByRole[0]
	if evidence.Role != "evidence" || evidence.Support != 1 || evidence.Oppose != 1 {
		t.Errorf("evidence row = %+v, want the two answers to that question", evidence)
	}
	if len(evidence.OpposingRationales) != 1 ||
		!strings.Contains(evidence.OpposingRationales[0], peelText) {
		t.Errorf("opposing rationales = %+v, want the argument against in its own words",
			evidence.OpposingRationales)
	}
	if !peel.Reception.Contested {
		t.Error("two reviewers answered one question differently and the record is not contested")
	}

	// The same votes under different roles are not a disagreement: one
	// reviewer says the evidence does not hold and another says the problem
	// matters, and both can be right.
	across := newPhaseB(t, peelText, func(o *Options) {
		o.Evaluation = reviewedBy(peelText,
			assessed("evidence", "oppose", "thin "+peelText),
			assessed("relevance", "support", ""))
	})
	var apart recordPeel
	decodeResponse(t, across.ok(t, "/api/record/"+across.proposal.ID), &apart)
	if apart.Reception == nil || len(apart.Reception.ByRole) != 2 {
		t.Fatalf("by_role = %+v", apart.Reception)
	}
	if apart.Reception.Contested {
		t.Error("support on one question and opposition on another reads as contested")
	}
}

// reviewedBy is an evaluation projection holding exactly the assessments a
// test names, each with the grant that authorized it — because the role comes
// off the grant and not off the vote, and a fixture that put it on the vote
// would be asserting a join this surface does not perform.
func reviewedBy(text string, votes ...roleVote) *fakeEvaluation {
	fake := evaluationFixture(text)
	detail := evaluation.Detail{Item: fake.detail.Item}
	for i, vote := range votes {
		assignment := fmt.Sprintf("asg_%d", i)
		record := vote.record
		record.ID = fmt.Sprintf("evr_role_%d", i)
		record.AssignmentID = assignment
		detail.History = append(detail.History, record)
		detail.Assignments = append(detail.Assignments, evaluation.Assignment{
			ID: assignment, Role: vote.role,
		})
	}
	fake.detail = detail
	return fake
}

// roleVote pairs one vote with the role its grant authorized, because that is
// how the two arrive: the vote is the run's record and the role is the grant's.
type roleVote struct {
	role   string
	record evaluation.Record
}

// assessed is one run-authored vote under one role, with what it said.
func assessed(role, vote, rationale string) roleVote {
	assessment := &evaluation.Assessment{Vote: vote}
	if rationale != "" {
		assessment.Contributions = []evaluation.Contribution{{Text: rationale}}
	}
	return roleVote{role: role, record: evaluation.Record{
		Kind:       evaluation.KindAssessment,
		ActorKind:  evaluation.ActorRun,
		ActorID:    "run-role",
		CreatedAt:  time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
		Assessment: assessment,
	}}
}

// TestAStanceDoesNotWaitOnTheProjection is the operator's sixth complaint: the
// upvote took six and a half seconds.
//
// The stance is durable when its transaction commits; what followed it inline
// was a refresh of a rebuildable projection. This holds the route to answering
// on the write: the refresh here never finishes, and the response arrives
// anyway, with the stance the store recorded.
func TestAStanceDoesNotWaitOnTheProjection(t *testing.T) {
	wedged := make(chan struct{})
	t.Cleanup(func() { close(wedged) })
	h := newPhaseB(t, peelText, func(o *Options) {
		o.Evaluation = wedgedProjection{fakeEvaluation: evaluationFixture(peelText), release: wedged}
	})

	start := time.Now()
	var recorded receptionResult
	decodeResponse(t, h.okPost(t, "/api/record/"+h.proposal.ID+"/reception", `{"stance":"agree"}`), &recorded)
	elapsed := time.Since(start)
	if recorded.Stance != "agree" {
		t.Errorf("stance = %q, want the one the store recorded", recorded.Stance)
	}
	if elapsed > time.Second {
		t.Errorf("the stance took %s, want well under a second even when the projection will not answer", elapsed)
	}
}

// wedgedProjection is an evaluation service whose projection refresh never
// returns, which is the only way to assert that the route does not wait on it.
type wedgedProjection struct {
	*fakeEvaluation
	release chan struct{}
}

func (w wedgedProjection) OperatorDeferred(ctx context.Context, in evaluation.OperatorInput) (
	evaluation.Record, func(context.Context) error, error) {
	record, err := w.fakeEvaluation.Operator(ctx, in)
	if err != nil {
		return evaluation.Record{}, nil, err
	}
	return record, func(context.Context) error {
		<-w.release
		return nil
	}, nil
}

// fakeRecordReceipts is one run receipt, as the producing machine can still read it.
// The body is what §9 seals before it leaves a host, so this is the only shape
// a cost block can be built from.
type fakeRecordReceipts struct {
	usage *worker.Usage
}

func (f fakeRecordReceipts) Revisions(context.Context, string) ([]run.Receipt, error) {
	started := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	return []run.Receipt{{Body: run.Body{
		Timing: run.Timing{StartedAt: started, FinishedAt: started.Add(90 * time.Second)},
		Worker: &worker.Receipt{
			Usage:    f.usage,
			Metadata: map[string]string{"model": "claude-opus-5", "provider": "anthropic"},
		},
	}}}, nil
}

func (f fakeRecordReceipts) Receipts(context.Context, int, int) ([]run.Receipt, int, error) {
	return nil, 0, nil
}

// citingObservation records one observation that cites locator, and answers
// with its id. It is a second record rather than an edit to the fixture's,
// because the frontier is append-only and a test that needed to rewrite a
// citation would be testing something the store cannot do.
func (h *phaseB) citingObservation(t *testing.T, locator event.Locator) string {
	t.Helper()
	cited, err := frontier.NewEvidence(locator, "the note about it, which is not the words "+peelText)
	if err != nil {
		t.Fatalf("frontier.NewEvidence: %v", err)
	}
	record, err := h.front.CreateObservation(h.ctx, frontier.ObservationInput{
		HypothesisID:  h.hypothesis.ID,
		RunID:         "run-1",
		RecipeID:      "outcome-integrity",
		RecipeVersion: 1,
		Payload: frontier.ObservationPayload{
			Claim:                 "the operator said so himself " + peelText,
			Category:              "outcome " + peelText,
			Confidence:            frontier.ConfidenceModerate,
			Impact:                frontier.ImpactModerate,
			Evidence:              []frontier.Evidence{cited},
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		t.Fatalf("CreateObservation: %v", err)
	}
	return record.ID
}

// writeCitedSession writes one synthetic OMP session log and answers with the
// locator of the message record in it, exactly as internal/event would have
// minted it: the record's byte offset and the digest of its bytes.
//
// The digest is the point. It is what lets the record page quote the bytes at
// an offset instead of trusting that a log nobody has touched is a log nobody
// has touched, so a fixture that computed it any other way than over the
// record's own bytes would be testing a check that cannot fail.
func writeCitedSession(t *testing.T, said string) (string, event.Locator) {
	t.Helper()
	header := `{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000000c",` +
		`"timestamp":"2026-05-01T00:00:00.000Z","cwd":"/synthetic/workspace"}`
	message := `{"type":"message","id":"c0000001","parentId":null,` +
		`"timestamp":"2026-05-01T00:01:00.000Z","message":{"role":"user","content":` +
		`[{"type":"text","text":` + mustJSON(t, said) + `}]}}`
	path := filepath.Join(t.TempDir(), "cited-session.jsonl")
	if err := os.WriteFile(path, []byte(header+"\n"+message+"\n"), 0o600); err != nil {
		t.Fatalf("write synthetic session: %v", err)
	}
	sum := sha256.Sum256([]byte(message))
	return path, event.Locator{
		Path:       path,
		Line:       2,
		ByteOffset: int64(len(header) + 1),
		Digest:     hex.EncodeToString(sum[:]),
	}
}

// TestAnOperatorStanceRoundTripsAndKeepsTheOneItReplaced is #235 §3's missing
// control, end to end.
//
// The operator says what he thinks from the page he is reading it on, changes
// his mind, and both statements survive: §4.12 is append-only, so a second
// stance is a second record and never an edit to the first. A surface that
// overwrote the earlier one would make "he used to agree" unanswerable, which
// is the one thing an append-only log exists to prevent.
func TestAnOperatorStanceRoundTripsAndKeepsTheOneItReplaced(t *testing.T) {
	var service *evaluation.Service
	h := newPhaseB(t, peelText, func(o *Options) {
		service = realEvaluation(t, o.Frontier.(*frontier.Store))
		o.Evaluation = service
	})
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh the evaluation projection: %v", err)
	}
	path := "/api/record/" + h.proposal.ID + "/reception"

	var agreed receptionResult
	decodeResponse(t, h.okPost(t, path, `{"stance":"agree","reason":"this is the right remedy"}`), &agreed)
	if agreed.Stance != evaluation.StanceAgree || agreed.At == "" {
		t.Fatalf("first reception = %+v", agreed)
	}

	var disagreed receptionResult
	decodeResponse(t, h.okPost(t, path, `{"stance":"disagree","reason":"the benchmark changed my mind"}`),
		&disagreed)
	if disagreed.Stance != evaluation.StanceDisagree {
		t.Fatalf("second reception = %+v", disagreed)
	}

	var peel recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+h.proposal.ID), &peel)
	if peel.Reception == nil || peel.Reception.Operator == nil {
		t.Fatalf("the record does not carry the operator's own voice: %+v", peel.Reception)
	}
	if peel.Reception.Operator.Stance != evaluation.StanceDisagree {
		t.Errorf("current stance = %q, want the one he last recorded", peel.Reception.Operator.Stance)
	}
	if peel.Reception.Operator.Reason != "the benchmark changed my mind" {
		t.Errorf("reason = %q, want his words kept verbatim", peel.Reception.Operator.Reason)
	}
	if len(peel.Reception.History) != 1 || peel.Reception.History[0].Stance != evaluation.StanceAgree {
		t.Fatalf("history = %+v, want the agreement he replaced", peel.Reception.History)
	}
	// The operator's voice is never summed with Babel's reviewers: a person
	// agreeing is not a run voting support, and a tally that counted him
	// would make §4.12's separation invisible on the one page it matters on.
	if peel.Reception.Counts != nil {
		t.Errorf("counts = %+v, want none: no run has voted on this record", peel.Reception.Counts)
	}
	if len(peel.Reception.Model) != 0 {
		t.Errorf("model reception = %+v, want none", peel.Reception.Model)
	}
}

// TestTheReceptionRouteRecordsNoPositionItWasNotGiven covers the two ways a
// reception can be malformed, both of which a real store refuses.
//
// The empty body is the one worth stating: a click that recorded `agree`
// because no stance arrived would attribute a position to the operator that he
// never took, and it would do it silently and permanently.
func TestTheReceptionRouteRecordsNoPositionItWasNotGiven(t *testing.T) {
	var service *evaluation.Service
	h := newPhaseB(t, peelText, func(o *Options) {
		service = realEvaluation(t, o.Frontier.(*frontier.Store))
		o.Evaluation = service
	})
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh the evaluation projection: %v", err)
	}
	path := "/api/record/" + h.proposal.ID + "/reception"

	for _, tc := range []struct{ name, body string }{
		{"a word outside the vocabulary", `{"stance":"maybe"}`},
		{"nothing at all", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := h.post(path, tc.body)
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", response.StatusCode)
			}
		})
	}

	// Nothing was recorded by either attempt, which is what makes the refusal
	// a refusal rather than a message beside a stored record.
	var peel recordPeel
	decodeResponse(t, h.ok(t, "/api/record/"+h.proposal.ID), &peel)
	if peel.Reception != nil && peel.Reception.Operator != nil {
		t.Errorf("a refused reception was recorded anyway: %+v", peel.Reception.Operator)
	}
}

// TestTheReceptionRouteCannotMintAModelsObservation is §4.12's authority
// boundary, asserted at the surface that just grew an operator write.
//
// The new route reaches evaluation.Service.Operator, which is the same path
// /api/evaluation/operator uses, so the boundary has to be checked where it
// could have widened: an operator surface that accepted `assessment` would let
// a person mint what reads as a model's observation.
func TestTheReceptionRouteCannotMintAModelsObservation(t *testing.T) {
	var service *evaluation.Service
	h := newPhaseB(t, peelText, func(o *Options) {
		service = realEvaluation(t, o.Frontier.(*frontier.Store))
		o.Evaluation = service
	})

	response := h.post("/api/evaluation/operator",
		`{"kind":"assessment","subject":{"kind":"proposal","id":"`+h.proposal.ID+`"}}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a person authored a run's assessment", response.StatusCode)
	}
	// The reception route carries no kind at all, which is the stronger half
	// of the same guarantee: there is no field a request could put one in.
	refused := h.post("/api/record/"+h.proposal.ID+"/reception", `{"kind":"assessment","stance":"agree"}`)
	defer refused.Body.Close()
	if refused.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: the reception body accepts no kind", refused.StatusCode)
	}
}

// observationID reads the claim the fixture candidate was developed through.
// The harness keeps the three reviewable records and not this one, because
// until #235 no route rendered an observation on its own.
func (h *phaseB) observationID(t *testing.T) string {
	t.Helper()
	observations, err := h.front.ObservationsFor(h.ctx, h.hypothesis.ID)
	if err != nil || len(observations) == 0 {
		t.Fatalf("ObservationsFor: %v (%d)", err, len(observations))
	}
	return observations[0].ID
}

// okPost performs the POST a reader's click makes and refuses anything but an
// answer.
func (h *phaseB) okPost(t *testing.T, path, body string) *http.Response {
	t.Helper()
	response := h.post(path, body)
	if response.StatusCode != http.StatusOK {
		defer response.Body.Close()
		t.Fatalf("POST %s: status = %d", path, response.StatusCode)
	}
	return response
}

// realEvaluation opens the evaluation service the operator's reception is
// actually recorded through.
//
// It is the real store and the real projection rather than the fixture fake,
// because what these tests check is that a second stance replaces the first
// while the first stays readable — which is a property of an append-only store
// and its projection, and a fake that returned whatever it was handed would
// assert nothing about it.
func realEvaluation(t *testing.T, front *frontier.Store) *evaluation.Service {
	t.Helper()
	source := evaluation.NewSource(front, nil, nil)
	store, err := evaluation.Open(t.TempDir(), source)
	if err != nil {
		t.Fatalf("evaluation.Open: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	service, err := evaluation.NewService(t.TempDir(), store, source)
	if err != nil {
		t.Fatalf("evaluation.NewService: %v", err)
	}
	t.Cleanup(func() { service.Close() })
	return service
}
