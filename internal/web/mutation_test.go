package web

// The mutations, and the §14 gate they close: Reality and review writes share
// the Go service authorization path rather than becoming browser-owned state.
//
// Three kinds of proof are here, because one alone would not settle it. A
// structural one, that the surface holds no operation that could write a store
// directly. A behavioural one, that a mutation driven through HTTP lands in the
// durable record with the session's operator attached. And a refusal one, that
// an anonymous or unauthenticated mutation writes nothing at all.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
)

// recordingReview forwards every call to the real service and keeps what the
// handler passed. It is a decorator rather than a fake: the durable effect the
// assertions read is the real service's, so the test proves what the handler
// asked for and what the store recorded at the same time.
type recordingReview struct {
	ReviewService
	decisions []review.Decision
	contexts  []review.Authority
}

func (r *recordingReview) Decide(ctx context.Context, in review.Decision) (frontier.DispositionEvent, error) {
	r.decisions = append(r.decisions, in)
	return r.ReviewService.Decide(ctx, in)
}

func (r *recordingReview) RecordContext(ctx context.Context, by review.Authority, text string) (review.Context, error) {
	r.contexts = append(r.contexts, by)
	return r.ReviewService.RecordContext(ctx, by, text)
}

type recordingReality struct {
	RealityService
	answers     []reality.AnswerInput
	acceptances []reality.AcceptanceInput
}

func (r *recordingReality) RecordAnswer(ctx context.Context, in reality.AnswerInput) (reality.Answer, error) {
	r.answers = append(r.answers, in)
	return r.RealityService.RecordAnswer(ctx, in)
}

func (r *recordingReality) AcceptPlan(ctx context.Context, in reality.AcceptanceInput) (reality.Acceptance, reality.Application, error) {
	r.acceptances = append(r.acceptances, in)
	return r.RealityService.AcceptPlan(ctx, in)
}

// TestMutationsCallTheServiceWithTheSessionsAttribution drives all five
// mutations through HTTP and then reads the durable records back through the
// services.
//
// This is the gate's behavioural half. Each assertion pairs what the handler
// handed the service with what the store now holds: a decision whose reviewer is
// the session's operator, guidance attributed to the same identity, an answer
// retained verbatim under that author, a fact made authoritative on the
// accepting operator's authority rather than on the interpretation's, and
// #115's capture recorded against that operator and this machine's host.
func TestMutationsCallTheServiceWithTheSessionsAttribution(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	reviewSpy := &recordingReview{ReviewService: h.review}
	realitySpy := &recordingReality{RealityService: h.reality}
	h.server, h.http = testServer(t, Options{
		Operator: operatorID,
		Review:   reviewSpy,
		Frontier: h.front,
		Reality:  realitySpy,
		// The capture takes no recording decorator. The other two services
		// are wrapped because what the handler passed them is otherwise
		// unobservable — an authority value and an actor id disappear into a
		// derived state — while a complaint's whole content is its own
		// durable row, so reading the row back through the real store proves
		// both halves at once with nothing standing between them.
		Complaints: h.complaints,
		State: StateProviderFunc(func(context.Context) (State, error) {
			return State{Configured: true, HostID: hostUnderTest}, nil
		}),
	})

	var guidance contextResult
	decodeResponse(t, h.post("/api/review/context", `{"text":"the corpus is three sessions wide"}`), &guidance)
	if guidance.ID == "" || guidance.Author != operatorID {
		t.Fatalf("context = %+v", guidance)
	}
	if len(reviewSpy.contexts) != 1 || reviewSpy.contexts[0].ID() != operatorID {
		t.Fatalf("context authority = %+v", reviewSpy.contexts)
	}

	var decided decideResult
	decodeResponse(t, h.post("/api/review/decide",
		`{"subject":{"type":"proposal","id":"`+h.proposal.ID+`"},"disposition":"accept","contextId":"`+
			guidance.ID+`","note":"the evidence is thin but the direction is right"}`), &decided)
	if decided.Status != string(frontier.ReviewAccepted) || decided.Event.ID == "" {
		t.Fatalf("decision = %+v", decided)
	}
	if len(reviewSpy.decisions) != 1 {
		t.Fatalf("the handler did not call the service: %+v", reviewSpy.decisions)
	}
	if got := reviewSpy.decisions[0]; got.By.ID() != operatorID || got.ContextID != guidance.ID ||
		got.Subject.ID != h.proposal.ID || got.Disposition != frontier.DispositionAccept {
		t.Fatalf("decision passed to the service = %+v", got)
	}

	// The durable effect, read from the service rather than from the response
	// the handler wrote: attribution that only appeared in the reply would be
	// a claim about a decision rather than a decision.
	history, err := h.review.History(h.ctx, frontier.Ref{Type: frontier.EntityProposal, ID: h.proposal.ID})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if history.Status != frontier.ReviewAccepted || len(history.Decisions) != 1 {
		t.Fatalf("history = %+v", history)
	}
	recorded := history.Decisions[0]
	if recorded.Event.ReviewerID != operatorID {
		t.Fatalf("durable decision is attributed to %q, want %q", recorded.Event.ReviewerID, operatorID)
	}
	if recorded.Context == nil || recorded.Context.Author != operatorID {
		t.Fatalf("durable guidance = %+v", recorded.Context)
	}

	var answer answerResult
	decodeResponse(t, h.post("/api/reality/answer",
		`{"questionId":"`+h.question.ID+`","text":"it is dormant now"}`), &answer)
	if answer.AnswerID == "" || answer.State != string(reality.QuestionAnsweredUninterpreted) {
		t.Fatalf("answer = %+v", answer)
	}
	if len(realitySpy.answers) != 1 || realitySpy.answers[0].Author != operatorID {
		t.Fatalf("answer passed to the ledger = %+v", realitySpy.answers)
	}
	answers, err := h.reality.Answers(h.ctx, h.question.ID)
	if err != nil {
		t.Fatalf("Answers: %v", err)
	}
	if len(answers) != 1 || answers[0].Author != operatorID ||
		answers[0].Payload.Text != "it is dormant now" {
		t.Fatalf("durable answers = %+v", answers)
	}

	var accepted planAcceptResult
	decodeResponse(t, h.post("/api/reality/plan/accept", `{"planId":"`+h.plan.ID+`"}`), &accepted)
	if len(accepted.Applied) != 1 || accepted.Applied[0].Kind != "fact" ||
		accepted.State != string(reality.QuestionAnswered) {
		t.Fatalf("acceptance = %+v", accepted)
	}
	if len(realitySpy.acceptances) != 1 || realitySpy.acceptances[0].Actor != operatorID {
		t.Fatalf("acceptance passed to the ledger = %+v", realitySpy.acceptances)
	}
	facts, err := h.reality.Facts(h.ctx, reality.FactQuery{SubjectID: h.entity.ID})
	if err != nil {
		t.Fatalf("Facts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("durable facts = %+v", facts)
	}
	// §4.8's rule, observed: the accepting operator is the authority, whatever
	// the interpretation proposed.
	fact := facts[0]
	if fact.Status != reality.FactActive || fact.Authority.Kind != reality.AuthorityOperator ||
		fact.Authority.ID != operatorID {
		t.Fatalf("applied fact = %+v", fact)
	}

	// The plan is spent: §4.8 allows exactly one acceptance, and the second
	// request is refused by the ledger rather than applied again.
	response := h.post("/api/reality/plan/accept", `{"planId":"`+h.plan.ID+`"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("second acceptance status = %d, want 409", response.StatusCode)
	}

	// #115's capture. It is the one mutation here whose record did not exist
	// before the request, so the durable read is by the identifier the
	// response returned: a handler that had answered without writing would
	// have nothing to find.
	const told = "the repository rules keep getting ignored and I keep re-explaining them"
	var captured captureResult
	decodeResponse(t, h.post("/api/complaint/tell", `{"text":`+mustJSON(t, told)+`}`), &captured)
	if captured.Complaint.ID == "" || !captured.Complaint.Head {
		t.Fatalf("capture = %+v", captured)
	}
	if captured.Steering != captureOpenedNothing {
		t.Errorf("steering = %q, want the fixed sentence", captured.Steering)
	}
	durable, err := h.complaints.Complaint(h.ctx, captured.Complaint.ID)
	if err != nil {
		t.Fatalf("Complaint: %v", err)
	}
	if durable.By != operatorID {
		t.Errorf("durable complaint is attributed to %q, want %q", durable.By, operatorID)
	}
	if durable.Host != hostUnderTest {
		t.Errorf("durable complaint host = %q, want the machine the launch named", durable.Host)
	}
	if durable.Text != told {
		t.Errorf("durable complaint text = %q, want what was posted", durable.Text)
	}
}

// TestMutationsRefuseAnUnattributableOperator is §4.7's attribution rule at the
// boundary: a session that cannot name an operator gets no mutation at all,
// rather than one recorded against a default identity. The durable state is
// compared before and after, because a refusal that had already written would
// be indistinguishable from success in the response alone.
func TestMutationsRefuseAnUnattributableOperator(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) { opts.Operator = "" })
	before := h.snapshot()
	for _, route := range phaseBRoutes(h) {
		if !route.mutating {
			continue
		}
		t.Run(route.name, func(t *testing.T) {
			response := h.post(route.path, route.body)
			text := body(t, response)
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d body %q, want 409", response.StatusCode, text)
			}
			if !strings.Contains(text, "operator") {
				t.Errorf("refusal does not say what is missing: %q", text)
			}
		})
	}
	if after := h.snapshot(); after != before {
		t.Fatalf("an unattributed mutation wrote to the durable record:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestUnauthenticatedMutationsWriteNothing pairs the 401 with the durable
// record: the session check has to happen before the service call, not beside
// it.
func TestUnauthenticatedMutationsWriteNothing(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	before := h.snapshot()
	for _, route := range phaseBRoutes(h) {
		if !route.mutating {
			continue
		}
		req, err := http.NewRequest(http.MethodPost, h.http.URL+route.path, strings.NewReader(route.body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := h.http.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401", route.path, response.StatusCode)
		}
	}
	if after := h.snapshot(); after != before {
		t.Fatalf("an unauthenticated mutation wrote to the durable record:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestTheWebSurfaceHoldsNoWriteThatBypassesAService is the gate's structural
// half: a mutation that skipped the service cannot be written here, because the
// surface has no method that could perform one.
//
// The interfaces in types.go are the whole authority this package has over
// durable state, so the test enumerates them and compares them to what the
// contract permits. Each forbidden name is also asserted to exist on the
// concrete service, which is what keeps the test from passing vacuously: a
// renamed store method would otherwise silently become "not in the interface,
// therefore fine".
func TestTheWebSurfaceHoldsNoWriteThatBypassesAService(t *testing.T) {
	for _, test := range []struct {
		name      string
		surface   reflect.Type
		concrete  reflect.Type
		permitted []string
		forbidden []string
	}{
		{
			name:      "review",
			surface:   reflect.TypeOf((*ReviewService)(nil)).Elem(),
			concrete:  reflect.TypeOf((*review.Service)(nil)),
			permitted: []string{"Decide", "Export", "History", "Lineage", "Queue", "RecordContext"},
			// The review service's other writes are outside the contract:
			// a refinement is requested and recorded by the CLI and its
			// workers, and a durable-learning proposal is disposed of there
			// too. A browser holding either would be holding authority the
			// contract never granted it.
			forbidden: []string{"RejectAndRefine", "RecordRefinement", "DisposeMemory", "Enroll", "Close"},
		},
		{
			name:     "frontier",
			surface:  reflect.TypeOf((*FrontierReader)(nil)).Elem(),
			concrete: reflect.TypeOf((*frontier.Store)(nil)),
			permitted: []string{"Finding", "Head", "Hypothesis", "LinksFrom", "LinksTo", "Observation",
				"ObservationsFor", "Proposal", "ProposalsAddressing", "ReviewStatus", "Revisions",
				"StatusHistory", "Unexplored"},
			// Every frontier write, including its own Decide and the
			// revive transition #87 added: one disposition log exists and
			// internal/review is the only way this surface may append to
			// it, and a revive is reachable only through FrontierReviver.
			//
			// CreateCandidateProposal is #114's second proposal
			// constructor and is forbidden on exactly the terms
			// CreateProposal is: a browser GET that could mint a remedy
			// would be a durable record written by a page view.
			forbidden: []string{"CreateHypothesis", "CreateObservation", "CreateFinding", "CreateProposal",
				"CreateCandidateProposal", "Decide", "RejectAndRefine", "SetStatus", "Link",
				"DeferFrontier", "Revive", "Close"},
		},
		{
			name:      "frontier reviver",
			surface:   reflect.TypeOf((*FrontierReviver)(nil)).Elem(),
			concrete:  reflect.TypeOf((*frontier.Store)(nil)),
			permitted: []string{"Revive"},
			// The reviver is one method wide on purpose. SetStatus is the
			// bookkeeping a run does about a candidate it is working on,
			// and a browser holding it could move a live exploration's
			// candidate from outside the run; DeferFrontier is the same
			// authority in bulk.
			forbidden: []string{"SetStatus", "DeferFrontier", "CreateHypothesis", "Close"},
		},
		{
			name:      "dispositions",
			surface:   reflect.TypeOf((*DispositionService)(nil)).Elem(),
			concrete:  reflect.TypeOf((*disposition.Store)(nil)),
			permitted: []string{"Decide", "Disposition", "Invitations", "Invite", "Ledger", "List"},
			// Proposing is the loop's job and consuming is a run's. A
			// browser that could propose would be authoring the actions it
			// exists to authorize, and one that could consume would spend
			// an invitation without a run to spend it on.
			forbidden: []string{"Propose", "Consume", "ConsumeOne", "Close"},
		},
		{
			name:     "reality",
			surface:  reflect.TypeOf((*RealityService)(nil)).Elem(),
			concrete: reflect.TypeOf((*reality.Store)(nil)),
			permitted: []string{"AcceptPlan", "Aliases", "Answers", "Entity", "Facts", "Inbox",
				"Plan", "Question", "QuestionHistory", "RecordAnswer", "Relationships"},
			// The ledger's authoritative writes. §4.8 reaches them through
			// a plan an operator accepted, never directly, so a browser
			// request cannot assert, supersede, merge, import, or install
			// anything.
			forbidden: []string{"AssertFact", "SupersedeFact", "DisputeFacts", "ResolveDispute",
				"MergeEntities", "SplitEntity", "UndoResolution", "ImportFacts", "PutFocusRules",
				"RegisterTrustedSource", "CreateEntity", "AddAlias", "AddRelationship",
				"RetractAlias", "RetractRelationship", "Ask", "RecordPlan", "RejectPlan",
				"SetQuestionState", "BeginInterpretation", "ExpireStale", "CaptureSnapshot", "Close"},
		},
		{
			name:      "complaints",
			surface:   reflect.TypeOf((*ComplaintService)(nil)).Elem(),
			concrete:  reflect.TypeOf((*complaint.Store)(nil)),
			permitted: []string{"Complaint", "Heads", "Revisions", "Tell"},
			// Amend is the operator restating a complaint, and it belongs
			// to `babel tell --amend` where they can see the wording they
			// are replacing: a browser box that could reach it would be
			// able to silently rewrite the text the page was showing.
			// Outputs and Output flatten complaints for the retrieval
			// index, which is preparation's job and never a page view's,
			// and Close would let a request shut the durable component
			// every other route on this server reads.
			forbidden: []string{"Amend", "Output", "Outputs", "Close"},
		},
		{
			name:      "search",
			surface:   reflect.TypeOf((*SearchIndex)(nil)).Elem(),
			concrete:  reflect.TypeOf((*index.Index)(nil)),
			permitted: []string{"FrontierSearch", "Search"},
			// The two writers are forbidden together because they are one
			// authority in two partitions: IndexSession rebuilds the corpus
			// side and IndexFrontier the self-retrieval side, both are
			// reconciled by preparation and by `babel tell`, and a browser
			// request that rebuilt either would be a shared cache written
			// by a page view. #115's capture searches this index and
			// deliberately does not reconcile it, which is only a rule the
			// handler can keep if the surface cannot break it.
			forbidden: []string{"IndexSession", "IndexFrontier", "Close"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			names := make([]string, test.surface.NumMethod())
			for i := range names {
				names[i] = test.surface.Method(i).Name
			}
			slices.Sort(names)
			slices.Sort(test.permitted)
			if !slices.Equal(names, test.permitted) {
				t.Errorf("surface methods = %v, want %v", names, test.permitted)
			}
			for _, forbidden := range test.forbidden {
				if _, ok := test.concrete.MethodByName(forbidden); !ok {
					t.Errorf("%s no longer has a %s method; this test is checking a name that "+
						"does not exist and must be updated", test.concrete, forbidden)
				}
				if slices.Contains(names, forbidden) {
					t.Errorf("the web surface can reach %s.%s", test.concrete, forbidden)
				}
			}
		})
	}
}

// TestMutationRefusalsComeFromTheService checks that the rules a decision can
// break are the service's and are reported as themselves. The handler validates
// shape and nothing else, so each of these refusals had to travel from
// internal/review to a status code.
func TestMutationRefusalsComeFromTheService(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	subject := `{"subject":{"type":"proposal","id":"` + h.proposal.ID + `"},`
	for _, test := range []struct {
		name   string
		path   string
		body   string
		status int
	}{
		{
			name: "an unknown record", path: "/api/review/decide", status: http.StatusNotFound,
			body: `{"subject":{"type":"proposal","id":"prp_absent"},"disposition":"accept"}`,
		},
		{
			name: "a kind that carries no decision", path: "/api/review/decide", status: http.StatusBadRequest,
			body: `{"subject":{"type":"observation","id":"obs_1"},"disposition":"accept"}`,
		},
		{
			name: "a disposition outside the vocabulary", path: "/api/review/decide", status: http.StatusBadRequest,
			body: subject + `"disposition":"refine"}`,
		},
		{
			name: "guidance that names nothing", path: "/api/review/decide", status: http.StatusNotFound,
			body: subject + `"disposition":"accept","contextId":"ctx_absent"}`,
		},
		{
			name: "an empty context", path: "/api/review/context", status: http.StatusBadRequest,
			body: `{"text":""}`,
		},
		{
			name: "an answer to an unknown question", path: "/api/reality/answer", status: http.StatusNotFound,
			body: `{"questionId":"qst_absent","text":"x"}`,
		},
		{
			name: "an answer outcome outside the vocabulary", path: "/api/reality/answer", status: http.StatusBadRequest,
			body: `{"questionId":"` + h.question.ID + `","text":"x","outcome":"maybe"}`,
		},
		{
			name: "an unknown plan", path: "/api/reality/plan/accept", status: http.StatusNotFound,
			body: `{"planId":"pln_absent"}`,
		},
		{
			name: "a body with a field the route does not accept", path: "/api/review/decide", status: http.StatusBadRequest,
			body: subject + `"disposition":"accept","reviewer":"someone else"}`,
		},
		{
			name: "a body that is not an object", path: "/api/review/decide", status: http.StatusBadRequest,
			body: `["accept"]`,
		},
		{
			name: "a subject with no kind", path: "/api/review/decide", status: http.StatusBadRequest,
			body: `{"subject":{"type":"","id":"x"},"disposition":"accept"}`,
		},
		{
			name: "a subject with no id", path: "/api/review/decide", status: http.StatusBadRequest,
			body: `{"subject":{"type":"proposal","id":""},"disposition":"accept"}`,
		},
		{
			name: "a plan acceptance with no plan", path: "/api/reality/plan/accept", status: http.StatusBadRequest,
			body: `{"planId":""}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := h.post(test.path, test.body)
			text := body(t, response)
			if response.StatusCode != test.status {
				t.Fatalf("status = %d body %q, want %d", response.StatusCode, text, test.status)
			}
		})
	}

	// A decision that repeats the standing one is refused by the service,
	// because §4.7's history is the record of how a reviewer's position moved
	// and an event that moved nothing makes it say something false.
	first := h.post("/api/review/decide", subject+`"disposition":"defer"}`)
	first.Body.Close()
	repeat := h.post("/api/review/decide", subject+`"disposition":"defer"}`)
	defer repeat.Body.Close()
	if repeat.StatusCode != http.StatusConflict {
		t.Fatalf("repeated decision status = %d, want 409", repeat.StatusCode)
	}
}

// leakyError is a service failure carrying exactly what must not reach a
// browser or a log: a filesystem path, a connection string, and a
// credential-shaped value. The credential shape is assembled from parts because
// a contiguous literal would block every push of this repository.
var leakyError = fmt.Errorf("review: open %s: %w",
	"/home/operator/.local/state/babel/durable.db", errors.New(
		"postgres://babel:"+"gh"+"p_"+strings.Repeat("A", 36)+"@db.example:5432/babel?sslmode=verify-full"))

// failingServices refuses every call with leakyError. It stands in for a service
// whose storage failed, which is the only way to observe what a handler does
// with an error it cannot classify.
type failingServices struct{}

func (failingServices) Queue(context.Context, review.QueueFilter) ([]review.QueueItem, error) {
	return nil, leakyError
}

func (failingServices) History(context.Context, frontier.Ref) (review.History, error) {
	return review.History{}, leakyError
}

func (failingServices) Lineage(context.Context, review.Node) (review.Lineage, error) {
	return review.Lineage{}, leakyError
}

func (failingServices) Export(context.Context, review.Node, review.ExportOptions) (review.Export, error) {
	return review.Export{}, leakyError
}

func (failingServices) Decide(context.Context, review.Decision) (frontier.DispositionEvent, error) {
	return frontier.DispositionEvent{}, leakyError
}

func (failingServices) RecordContext(context.Context, review.Authority, string) (review.Context, error) {
	return review.Context{}, leakyError
}

func (failingServices) Inbox(context.Context, reality.InboxQuery) ([]reality.InboxItem, error) {
	return nil, leakyError
}

func (failingServices) Question(context.Context, string) (reality.Question, error) {
	return reality.Question{}, leakyError
}

func (failingServices) QuestionHistory(context.Context, string) ([]reality.QuestionEvent, error) {
	return nil, leakyError
}

func (failingServices) Answers(context.Context, string) ([]reality.Answer, error) {
	return nil, leakyError
}

func (failingServices) Plan(context.Context, string) (reality.Plan, error) {
	return reality.Plan{}, leakyError
}

func (failingServices) Entity(context.Context, string) (reality.Entity, error) {
	return reality.Entity{}, leakyError
}

func (failingServices) Aliases(context.Context, string) ([]reality.Alias, error) {
	return nil, leakyError
}

func (failingServices) Relationships(context.Context, string) ([]reality.Relationship, error) {
	return nil, leakyError
}

func (failingServices) Facts(context.Context, reality.FactQuery) ([]reality.Fact, error) {
	return nil, leakyError
}

func (failingServices) RecordAnswer(context.Context, reality.AnswerInput) (reality.Answer, error) {
	return reality.Answer{}, leakyError
}

func (failingServices) AcceptPlan(context.Context, reality.AcceptanceInput) (reality.Acceptance, reality.Application, error) {
	return reality.Acceptance{}, reality.Application{}, leakyError
}

func (failingServices) Search(context.Context, index.Query) ([]index.Hit, error) {
	return nil, leakyError
}

// FrontierSearch fails the same way, so #115's capture-time adjacency pass is
// exercised against a retrieval surface whose error carries exactly what must
// not reach a browser.
func (failingServices) FrontierSearch(context.Context, index.FrontierQuery) ([]index.FrontierHit, error) {
	return nil, leakyError
}

// failingFrontier is the read surface failing the same way.
type failingFrontier struct{ FrontierReader }

func (failingFrontier) Hypothesis(context.Context, string) (frontier.Hypothesis, error) {
	return frontier.Hypothesis{}, leakyError
}

func (failingFrontier) Finding(context.Context, string) (frontier.Finding, error) {
	return frontier.Finding{}, leakyError
}

func (failingFrontier) ReviewStatus(context.Context, frontier.Ref) (frontier.ReviewStatus, error) {
	return "", leakyError
}

func (failingFrontier) Unexplored(context.Context, int) ([]frontier.Hypothesis, error) {
	return nil, leakyError
}

// TestServiceFailuresRevealNothing checks both directions a leak could take: the
// response a browser reads and the diagnostics stream an operator reads. §9
// keeps credentials out of logs and errors, and a wrapped storage error is
// exactly where a path or a connection string would otherwise travel.
func TestServiceFailuresRevealNothing(t *testing.T) {
	var diagnostics syncBuffer
	s, httpServer := testServer(t, Options{
		Operator:    operatorID,
		Diagnostics: &diagnostics,
		Review:      failingServices{},
		Frontier:    failingFrontier{},
		Reality:     failingServices{},
		Search:      failingServices{},
	})
	secrets := []string{
		"durable.db",
		"/home/operator",
		"postgres://",
		"sslmode",
		"gh" + "p_" + strings.Repeat("A", 36),
		"db.example",
	}
	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/hypotheses", ""},
		{http.MethodGet, "/api/hypothesis?id=hyp_1", ""},
		{http.MethodGet, "/api/findings", ""},
		{http.MethodGet, "/api/finding?id=fnd_1", ""},
		{http.MethodGet, "/api/review/queue?status=all", ""},
		{http.MethodGet, "/api/review/history?type=proposal&id=prp_1", ""},
		{http.MethodGet, "/api/export?type=proposal&id=prp_1", ""},
		{http.MethodGet, "/api/reality/inbox", ""},
		{http.MethodGet, "/api/reality/entity?id=ent_1", ""},
		{http.MethodGet, "/api/search?q=x", ""},
		{http.MethodPost, "/api/review/decide", `{"subject":{"type":"proposal","id":"prp_1"},"disposition":"accept"}`},
		{http.MethodPost, "/api/review/context", `{"text":"guidance"}`},
		{http.MethodPost, "/api/reality/answer", `{"questionId":"qst_1","text":"x"}`},
		{http.MethodPost, "/api/reality/plan/accept", `{"planId":"pln_1"}`},
	} {
		var reader *strings.Reader
		if route.body != "" {
			reader = strings.NewReader(route.body)
		}
		var request *http.Request
		var err error
		if reader != nil {
			request, err = http.NewRequest(route.method, httpServer.URL+route.path, reader)
		} else {
			request, err = http.NewRequest(route.method, httpServer.URL+route.path, nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		authorize(request, bootstrapSession(t, s, httpServer))
		response, err := httpServer.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		text := body(t, response)
		if response.StatusCode != http.StatusInternalServerError {
			t.Errorf("%s: status = %d, want 500", route.path, response.StatusCode)
		}
		if !strings.Contains(text, "could not be completed") {
			t.Errorf("%s: error does not name the problem: %q", route.path, text)
		}
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Errorf("%s: response leaks %q: %s", route.path, secret, text)
			}
		}
	}
	log := diagnostics.String()
	for _, secret := range secrets {
		if strings.Contains(log, secret) {
			t.Errorf("diagnostics leak %q: %s", secret, log)
		}
	}
	if !strings.Contains(log, "refused") {
		t.Errorf("diagnostics do not record the refusals: %s", log)
	}
}
