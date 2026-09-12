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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/fleet"
	"github.com/atyrode/babel/internal/frontier"
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
