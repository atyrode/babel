package web

// The evaluation surface over HTTP (issue #219; SPEC.md §4.12, §5.8, §8.5).
//
// These tests are about the boundary this package owns and nothing else. The
// ranking, the coverage arithmetic and the policy validation belong to
// internal/evaluation and are tested there; what has to be true here is that
// the browser reaches that service with the query the operator asked for, that
// it cannot reach anything the service does not expose, and that a write is
// attributed to the launch session rather than to a field in a request body.
//
// The service is a fake, on the terms fleetFixture and presenceFixture already
// established for the surfaces this package reads through rather than owns. It
// is what makes the properties below assertable at all: a real service would
// let a refusal be its own validation's, and the question here is what the
// route does with what the service returned.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
)

// fakeEvaluation is one synthetic evaluation projection, plus a record of what
// the routes asked it for. The recorded calls are the point: most of what this
// surface must get right is invisible in the response.
type fakeEvaluation struct {
	page     evaluation.Page
	detail   evaluation.Detail
	coverage evaluation.Coverage
	policy   evaluation.Policy
	// assessmentDays is the reviews-per-day series the Watch surface reads.
	assessmentDays []evaluation.AssessmentDay
	// tallies, thread and claims are §8.7's feed half: the deployment's
	// reception grouped by subject, one subject's conversation, and the
	// subjects a reviewer is holding right now.
	tallies map[evaluation.Subject]evaluation.Tally
	thread  map[evaluation.Subject][]evaluation.ThreadRecord
	claims  map[evaluation.Subject]evaluation.OpenClaim
	// lastClaimAt is the instant the feed asked what was open, so a test
	// can prove the expiry is judged against the caller's clock rather
	// than against one the store read for itself.
	lastClaimAt time.Time

	// err, when set, is returned by every method, so the sentinel
	// classification can be exercised through a real request.
	err error

	// The last call each route made.
	lastQuery    evaluation.Query
	lastSubject  evaluation.Subject
	lastOperator string
	lastPolicy   evaluation.Policy
	lastInput    evaluation.OperatorInput
	writes       int
	// refreshes counts the deferred projection refreshes a route actually
	// ran. It is guarded because the route runs them after the response, on
	// a goroutine of their own.
	mu        sync.Mutex
	refreshes int
}

func (f *fakeEvaluation) List(_ context.Context, q evaluation.Query) (evaluation.Page, error) {
	f.lastQuery = q
	if f.err != nil {
		return evaluation.Page{}, f.err
	}
	return f.page, nil
}

func (f *fakeEvaluation) Detail(_ context.Context, s evaluation.Subject) (evaluation.Detail, error) {
	f.lastSubject = s
	if f.err != nil {
		return evaluation.Detail{}, f.err
	}
	return f.detail, nil
}

func (f *fakeEvaluation) Coverage(context.Context) (evaluation.Coverage, error) {
	if f.err != nil {
		return evaluation.Coverage{}, f.err
	}
	return f.coverage, nil
}

func (f *fakeEvaluation) AssessmentDays(context.Context, time.Time) ([]evaluation.AssessmentDay, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.assessmentDays, nil
}

// Tallies and Thread are §8.7's feed half. The fake answers from fields a
// test sets, on the same terms as the page above: what the feed and the
// comment thread do with a reception is this package's business, and what a
// reception *is* belongs to internal/evaluation.
func (f *fakeEvaluation) Tallies(context.Context) (map[evaluation.Subject]evaluation.Tally, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tallies, nil
}

func (f *fakeEvaluation) Thread(_ context.Context, s evaluation.Subject) ([]evaluation.ThreadRecord, error) {
	f.lastSubject = s
	if f.err != nil {
		return nil, f.err
	}
	return f.thread[s], nil
}

func (f *fakeEvaluation) OpenClaims(_ context.Context, now time.Time) (
	map[evaluation.Subject]evaluation.OpenClaim, error) {
	f.lastClaimAt = now
	if f.err != nil {
		return nil, f.err
	}
	return f.claims, nil
}

func (f *fakeEvaluation) Policy(context.Context) (evaluation.Policy, error) {
	if f.err != nil {
		return evaluation.Policy{}, f.err
	}
	return f.policy, nil
}

func (f *fakeEvaluation) Configure(_ context.Context, operator string, p evaluation.Policy) (evaluation.Record, error) {
	f.lastOperator, f.lastPolicy = operator, p
	if f.err != nil {
		return evaluation.Record{}, f.err
	}
	f.writes++
	// The stored policy is the request with a version the service minted,
	// which is what makes "the route answers with what is stored rather
	// than what was sent" observable.
	f.policy = p
	f.policy.Version = "eval-policy-2"
	return evaluation.Record{
		ID: "evr_policy-2", Kind: evaluation.KindPolicy,
		ActorKind: "operator", ActorID: operator, CreatedAt: time.Now().UTC(),
	}, nil
}

func (f *fakeEvaluation) Operator(_ context.Context, in evaluation.OperatorInput) (evaluation.Record, error) {
	f.lastInput = in
	if f.err != nil {
		return evaluation.Record{}, f.err
	}
	f.writes++
	return evaluation.Record{
		ID: "evr_operator-1", Kind: in.Kind, Subject: in.Subject,
		ActorKind: "operator", ActorID: in.Operator, Reason: in.Reason,
		Decision: in.Decision, Stance: in.Stance, RelatedID: in.RelatedID,
		CreatedAt: time.Now().UTC(),
	}, nil
}

// OperatorDeferred records like Operator and hands back the refresh the real
// service defers. The counter it bumps is the fake's own, so a test can prove
// the route ran the refresh it was given rather than dropping it.
func (f *fakeEvaluation) OperatorDeferred(ctx context.Context, in evaluation.OperatorInput) (
	evaluation.Record, func(context.Context) error, error) {
	record, err := f.Operator(ctx, in)
	if err != nil {
		return evaluation.Record{}, nil, err
	}
	return record, func(context.Context) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.refreshes++
		return nil
	}, nil
}

// evaluationFixture is the synthetic projection every harness wires.
//
// text is woven through every free-text field for fleetFixture's reason: an
// item title, a coverage reason and an objection are wording a model produced,
// so the escaping sweep has to see them without a test remembering to ask.
func evaluationFixture(text string) *fakeEvaluation {
	subject := evaluation.Subject{Kind: "proposal", ID: "prp_synthetic"}
	artifact := evaluation.Artifact{
		Subject:        subject,
		RootID:         subject.ID,
		HeadID:         "prp_synthetic-r2",
		RunID:          "run-1",
		CreatedAt:      time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		Title:          "bound the describe pass " + text,
		Body:           json.RawMessage(`{"synthetic":true}`),
		ReviewStatus:   "new",
		Status:         "new",
		ContextVersion: "ctx-7",
		Context: evaluation.Context{
			Version:   "ctx-7",
			Allowance: "normal",
			Reasons:   []string{"recorded as current work " + text},
			Unknown:   []string{"whether the pattern survives outside the fixture " + text},
		},
	}
	item := evaluation.Item{
		Artifact:       artifact,
		Reception:      evaluation.Reception{Support: 2, Reviews: 2},
		Coverage:       evaluation.CoverageUnsupported,
		CoverageReason: "no evidence evaluator is registered in this build " + text,
		ReviewCoverage: []evaluation.RoleCoverage{
			{Role: "reception", State: evaluation.CoverageReviewed, Reviews: 2},
			{
				Role:   "evidence",
				State:  evaluation.CoverageUnsupported,
				Reason: "no evidence evaluator is registered in this build " + text,
			},
		},
		Lane:       "open",
		Score:      0.75,
		Reasons:    []string{"why now " + text},
		Objections: []string{"what argues against " + text},
		Group:      "one problem " + text,
	}
	return &fakeEvaluation{
		page: evaluation.Page{
			Items:     []evaluation.Item{item},
			Total:     1,
			Snapshot:  "snap-1",
			UpdatedAt: time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC),
		},
		detail: evaluation.Detail{
			Item: item,
			History: []evaluation.Record{{
				ID: "evr_1", Kind: evaluation.KindAssessment, Subject: subject,
				ActorKind: "run", ActorID: "run-1",
				CreatedAt:  time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
				Assessment: &evaluation.Assessment{Vote: "support"},
			}},
		},
		coverage: evaluation.Coverage{
			CoverageCounts: evaluation.CoverageCounts{Unreviewed: 3, Reviewed: 4, Overdue: 1},
			LastCheck:      time.Date(2026, 9, 11, 6, 0, 0, 0, time.UTC),
			UpdatedAt:      time.Date(2026, 9, 11, 8, 0, 0, 0, time.UTC),
			// A degraded inventory by default, so the one free-text
			// field this read carries is fixture content the escaping
			// sweep can find. The status tests clear it when they are
			// about a healthy deployment.
			Reason: "3 records could not be opened from the shared catalog " + text,
		},
		policy: evaluation.Policy{Version: "eval-policy-1", CadenceSeconds: 3600, DailyCost: 4},
	}
}

// newEvaluation serves the evaluation routes over the fake projection and
// nothing else.
//
// The Phase B harness opens six durable stores and writes a whole development
// path into them, which the route sweeps need and these tests do not: every
// assertion here is about what the evaluation handlers asked the service and
// what they answered. Under the race detector that fixture is the dominant
// cost of this package, so the surface under test is wired on its own and the
// full harness stays where it is actually read.
func newEvaluation(t *testing.T, text string, mutate func(*Options)) *phaseB {
	t.Helper()
	h := &phaseB{t: t, ctx: context.Background()}
	opts := Options{
		Operator: operatorID,
		State: StateProviderFunc(func(context.Context) (State, error) {
			return State{Configured: true, HostID: hostUnderTest}, nil
		}),
		Evaluation: evaluationFixture(text),
	}
	if mutate != nil {
		mutate(&opts)
	}
	h.server, h.http = testServer(t, opts)
	return h
}

// evaluationOf reaches the fake this harness wired, for the assertions that
// are about what a route asked rather than what it answered.
func (h *phaseB) evaluationOf() *fakeEvaluation {
	h.t.Helper()
	fake, ok := h.server.opts.Evaluation.(*fakeEvaluation)
	if !ok {
		h.t.Fatalf("the harness wired %T rather than the evaluation fixture", h.server.opts.Evaluation)
	}
	return fake
}

// TestEvaluationListHandsTheQueryToTheService is the §8.5 pagination contract
// at this boundary: the route narrows nothing, orders nothing and slices
// nothing. It reads the window and the four closed-vocabulary filters, passes
// them down, and answers with the page the service cut.
//
// The failure it prevents is the obvious shortcut — reading the whole set and
// slicing here — which would rank each page independently and would make a
// page read cost the whole corpus as it grows.
func TestEvaluationListHandsTheQueryToTheService(t *testing.T) {
	h := newEvaluation(t, "plain", nil)
	fake := h.evaluationOf()

	var got evaluationListResult
	decodeResponse(t, h.ok(t, "/api/evaluation/list?sort=contested&lane=open&kind=proposal"+
		"&coverage=unreviewed&role=evidence&snapshot=snap-1&limit=10&offset=20"), &got)

	want := evaluation.Query{
		Kind: "proposal", Lane: "open", Sort: "contested", Role: "evidence",
		Coverage: "unreviewed", Snapshot: "snap-1", Limit: 10, Offset: 20,
	}
	if fake.lastQuery != want {
		t.Fatalf("service asked %+v, want %+v", fake.lastQuery, want)
	}
	if got.Total != 1 || len(got.Items) != 1 {
		t.Fatalf("page = %+v, want the service's own page", got.Page)
	}
	// The snapshot echoed is the one served, not the one requested: a
	// pinned snapshot that has been pruned is answered from the current
	// ordering, and a client that rendered its own request back would
	// label the page with a snapshot nobody served it from.
	if got.Query.Snapshot != "snap-1" {
		t.Errorf("echoed snapshot = %q, want the served one", got.Query.Snapshot)
	}
	if got.Query.Role != "evidence" {
		t.Errorf("echoed role = %q, want the role the caller asked for", got.Query.Role)
	}
}

// TestEvaluationListRefusesAnUnboundedPage keeps §8.5's bounded page read a
// property of the route rather than of the caller's manners. The refusal is
// explicit rather than a silent clamp, because a client that asked for a
// thousand rows and received two hundred cannot tell it is paging through a
// truncated view.
func TestEvaluationListRefusesAnUnboundedPage(t *testing.T) {
	h := newEvaluation(t, "plain", nil)

	response := h.get("/api/evaluation/list?limit=100000")
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a page above the bound", response.StatusCode)
	}
	if h.evaluationOf().lastQuery.Limit != 0 {
		t.Errorf("the service was asked for %d rows despite the refusal", h.evaluationOf().lastQuery.Limit)
	}
}

// TestEvaluationListPassesAnUnknownSortThrough is the vocabulary rule every
// mutation on this surface already follows, applied to a read: the handler
// holds no copy of the closed sets, so an unknown value is refused by the one
// place that defines them.
//
// A second list here would drift, and the drift is silent in the worse
// direction: a sort the service supports would be rejected by the browser with
// no way for an operator to tell it exists.
func TestEvaluationListPassesAnUnknownSortThrough(t *testing.T) {
	h := newEvaluation(t, "plain", nil)
	fake := h.evaluationOf()
	fake.err = evaluation.ErrInvalid

	response := h.get("/api/evaluation/list?sort=by-vibes")
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want the service's refusal classified as 400", response.StatusCode)
	}
	if fake.lastQuery.Sort != "by-vibes" {
		t.Errorf("the handler filtered the sort itself: service saw %q", fake.lastQuery.Sort)
	}
}

// TestEvaluationSentinelsClassify checks the statuses an operator's client has
// to act on differently. A degraded projection is not a bad request and an
// exhausted budget is not a server fault; both are states the page renders
// rather than failures it reports.
func TestEvaluationSentinelsClassify(t *testing.T) {
	for _, probe := range []struct {
		name string
		err  error
		want int
	}{
		{"not found", evaluation.ErrNotFound, http.StatusNotFound},
		{"invalid", evaluation.ErrInvalid, http.StatusBadRequest},
		{"conflict", evaluation.ErrConflict, http.StatusConflict},
		{"budget", evaluation.ErrBudget, http.StatusConflict},
		{"unavailable", evaluation.ErrUnavailable, http.StatusServiceUnavailable},
	} {
		t.Run(probe.name, func(t *testing.T) {
			h := newEvaluation(t, "plain", nil)
			h.evaluationOf().err = probe.err

			response := h.get("/api/evaluation/coverage")
			defer response.Body.Close()
			if response.StatusCode != probe.want {
				t.Fatalf("status = %d, want %d", response.StatusCode, probe.want)
			}
			// The error's own text never reaches the client (§9).
			if text := body(t, h.get("/api/evaluation/coverage")); strings.Contains(text, probe.err.Error()) {
				t.Errorf("the response quotes the service error: %s", text)
			}
		})
	}
}

// TestEvaluationOperatorRecordsTheSessionOperator is the §14 attribution gate
// on this surface. The author of an operator record is the launch session's
// identity; the request body has no field that could name one, and a caller
// that sends one is refused rather than obeyed.
func TestEvaluationOperatorRecordsTheSessionOperator(t *testing.T) {
	h := newEvaluation(t, "plain", nil)
	fake := h.evaluationOf()

	var got evaluationOperatorResult
	decodeResponse(t, h.post("/api/evaluation/operator",
		`{"subject":{"kind":"proposal","id":"prp_synthetic"},"kind":"feedback",`+
			`"reason":"not-now","criteria":[],"related_id":"dec_1"}`), &got)

	if fake.lastInput.Operator != operatorID {
		t.Fatalf("recorded operator = %q, want the launch session's identity", fake.lastInput.Operator)
	}
	if fake.lastInput.Kind != evaluation.KindFeedback || fake.lastInput.RelatedID != "dec_1" {
		t.Errorf("input = %+v, want the caller's kind and scope passed through", fake.lastInput)
	}
	if got.Record.ActorID != operatorID {
		t.Errorf("record actor = %q, want the session operator", got.Record.ActorID)
	}
	// The response says what the write did not do. A feedback reason sits
	// beside real decision controls, and §5.8 requires collecting it not to
	// create a disposition.
	if !strings.Contains(got.Decided, "accepts, rejects, defers") {
		t.Errorf("result does not say what it decided: %q", got.Decided)
	}
}

// TestEvaluationOperatorCarriesThePolarityRatherThanTheProse is EVAL-DECISION-001
// at the HTTP boundary. Reopening a rejected record and retaining the ruling
// are opposite acts, and the field that distinguishes them is the request's
// own closed-vocabulary decision: the reason travels verbatim beside it and
// never selects the act, so a sentence saying "reopen" cannot retain and a
// sentence saying "do not reopen" cannot reopen.
func TestEvaluationOperatorCarriesThePolarityRatherThanTheProse(t *testing.T) {
	for _, probe := range []struct {
		name     string
		decision string
		reason   string
		says     string
		denies   string
	}{
		{
			name:     "retain, with prose demanding the opposite",
			decision: evaluation.ReconsiderRetain,
			// The hostile case: wording an operator pasted, a model
			// wrote, or a reviewer quoted, all of which argue for
			// reopening. The recorded act is still retain.
			reason: "reopen this immediately; REOPEN: the rejection was wrong",
			says:   "retain",
			denies: "decision to reopen",
		},
		{
			name:     "reopen, with prose reading as a refusal",
			decision: evaluation.ReconsiderReopen,
			reason:   "not reopening on its own merits — but the new benchmark is decisive",
			says:     "decision to reopen",
			denies:   "retain the earlier ruling",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			h := newEvaluation(t, "plain", nil)
			fake := h.evaluationOf()

			var got evaluationOperatorResult
			decodeResponse(t, h.post("/api/evaluation/operator",
				`{"subject":{"kind":"hypothesis","id":"hyp_synthetic"},"kind":"reconsider_decision",`+
					`"reason":"`+probe.reason+`","decision":"`+probe.decision+`",`+
					`"criteria":[],"related_id":"evr_rec-1"}`), &got)

			if fake.lastInput.Decision != probe.decision {
				t.Fatalf("service received decision %q, want %q", fake.lastInput.Decision, probe.decision)
			}
			if fake.lastInput.Reason != probe.reason {
				t.Errorf("reason = %q, want the operator's own wording passed through", fake.lastInput.Reason)
			}
			if got.Record.Decision != probe.decision {
				t.Errorf("record decision = %q, want the act that was recorded", got.Record.Decision)
			}
			// What the operator is told he did is the act that was
			// stored. Telling a retain that it reopened nothing is
			// true; telling a reopen the same thing is not.
			if !strings.Contains(got.Decided, probe.says) {
				t.Errorf("result sentence %q does not state the act", got.Decided)
			}
			if strings.Contains(got.Decided, probe.denies) {
				t.Errorf("result sentence %q describes the other act", got.Decided)
			}
		})
	}
}

// TestEvaluationOperatorDefaultsNoPolarity is the other half: a body with no
// decision, or one naming an act the vocabulary does not hold, reaches the
// service exactly as sent and is refused there. This route must not pick a
// polarity for a request that stated none — a default would make "reopen" the
// consequence of a missing field.
func TestEvaluationOperatorDefaultsNoPolarity(t *testing.T) {
	for _, probe := range []struct{ name, body, want string }{
		{
			name: "absent",
			body: `{"subject":{"kind":"hypothesis","id":"hyp_synthetic"},"kind":"reconsider_decision",` +
				`"reason":"the benchmark changed","criteria":[],"related_id":"evr_rec-1"}`,
			want: "",
		},
		{
			name: "outside the vocabulary",
			body: `{"subject":{"kind":"hypothesis","id":"hyp_synthetic"},"kind":"reconsider_decision",` +
				`"reason":"the benchmark changed","decision":"escalate","criteria":[],` +
				`"related_id":"evr_rec-1"}`,
			want: "escalate",
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			h := newEvaluation(t, "plain", nil)
			fake := h.evaluationOf()
			fake.err = evaluation.ErrInvalid

			response := h.post("/api/evaluation/operator", probe.body)
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want the service's refusal", response.StatusCode)
			}
			if fake.lastInput.Decision != probe.want {
				t.Errorf("service received decision %q, want %q unchanged", fake.lastInput.Decision, probe.want)
			}
			if fake.writes != 0 {
				t.Errorf("a refused decision performed %d writes", fake.writes)
			}
		})
	}
}

// TestEvaluationServesTheReconsiderVocabulary keeps the control's options the
// service's own. The page offers one radio per polarity, and a client holding
// its own list would offer an act the store refuses.
func TestEvaluationServesTheReconsiderVocabulary(t *testing.T) {
	h := newEvaluation(t, "plain", nil)

	var got evaluationDetailResult
	decodeResponse(t, h.ok(t, "/api/evaluation/detail?kind=proposal&id=prp_synthetic"), &got)
	if !slices.Equal(got.Vocabulary.ReconsiderDecisions, evaluation.ReconsiderDecisions()) {
		t.Fatalf("reconsider decisions = %v, want the service's own", got.Vocabulary.ReconsiderDecisions)
	}
	if len(got.Vocabulary.ReconsiderDecisions) < 2 {
		t.Errorf("a single-valued polarity is not a choice: %v", got.Vocabulary.ReconsiderDecisions)
	}
}

// TestEvaluationOperatorRefusesASuppliedAuthor is the impersonation case
// stated as a test rather than as a comment. The refusal comes from the
// decoder rejecting an unknown field, which is why it is worth pinning: a
// request type that grew an Operator field would silently start honouring it.
func TestEvaluationOperatorRefusesASuppliedAuthor(t *testing.T) {
	h := newEvaluation(t, "plain", nil)
	fake := h.evaluationOf()

	response := h.post("/api/evaluation/operator",
		`{"subject":{"kind":"proposal","id":"prp_synthetic"},"kind":"feedback",`+
			`"reason":"not-now","criteria":[],"related_id":"","operator":"somebody-else"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a body naming its own author", response.StatusCode)
	}
	if fake.writes != 0 {
		t.Errorf("the service was written to %d times by a refused request", fake.writes)
	}
}

// TestEvaluationWritesRefuseAnUnattributedSession is the rule §4.7 already
// applies to every other mutation here: a launch that cannot name an operator
// records nothing rather than recording it against nobody.
func TestEvaluationWritesRefuseAnUnattributedSession(t *testing.T) {
	h := newEvaluation(t, "plain", func(opts *Options) { opts.Operator = "" })
	fake := h.evaluationOf()

	for _, probe := range []struct{ path, body string }{
		{"/api/evaluation/operator",
			`{"subject":{"kind":"proposal","id":"prp_synthetic"},"kind":"feedback",` +
				`"reason":"not-now","criteria":[],"related_id":""}`},
		{"/api/evaluation/policy", `{"version":"eval-policy-1","enabled":true}`},
	} {
		response := h.post(probe.path, probe.body)
		if response.StatusCode != http.StatusConflict {
			t.Errorf("%s status = %d, want 409 without an operator identity", probe.path, response.StatusCode)
		}
		response.Body.Close()
	}
	if fake.writes != 0 {
		t.Errorf("an unattributed session performed %d writes", fake.writes)
	}
}

// TestEvaluationConfigureAnswersWithTheStoredPolicy pins the read-back. A
// configuration is versioned and defaulted by the service, so echoing the
// request would show the operator a policy that is not the one in force —
// including a version number that does not exist.
func TestEvaluationConfigureAnswersWithTheStoredPolicy(t *testing.T) {
	h := newEvaluation(t, "plain", nil)
	fake := h.evaluationOf()

	var got evaluationPolicyResult
	decodeResponse(t, h.post("/api/evaluation/policy",
		`{"version":"eval-policy-1","enabled":true,"cadence_seconds":1800,"daily_cost":2}`), &got)

	if fake.lastOperator != operatorID {
		t.Fatalf("configured by %q, want the launch session's identity", fake.lastOperator)
	}
	if !fake.lastPolicy.Enabled || fake.lastPolicy.CadenceSeconds != 1800 {
		t.Errorf("service received %+v, want the submitted knobs", fake.lastPolicy)
	}
	if got.Policy.Version != "eval-policy-2" {
		t.Errorf("answered version = %q, want the stored one", got.Policy.Version)
	}
	if got.Record == nil || got.Record.ActorID != operatorID {
		t.Errorf("record = %+v, want the attributed policy record", got.Record)
	}
	// §8.5: saving a policy is not permission to launch compute, and the
	// surface has to say so in its own words rather than leaving the
	// operator to assume either way.
	if !strings.Contains(got.Saving, "starts no run") {
		t.Errorf("saving sentence does not disclaim starting work: %q", got.Saving)
	}
}

// TestEvaluationStatusIsObservedRatherThanInferred is the §8.5 requirement
// that the four states are distinguishable, and the one honesty rule inside
// it: an enabled policy is permission to draw, not evidence that anything is
// running.
func TestEvaluationStatusIsObservedRatherThanInferred(t *testing.T) {
	for _, probe := range []struct {
		name     string
		enabled  bool
		active   int
		reason   string
		want     string
		wantDraw bool
	}{
		{name: "disabled is paused", enabled: false, want: evaluationPaused},
		{name: "enabled with nothing claimed is scheduled", enabled: true, want: evaluationScheduled, wantDraw: true},
		{name: "claimed work is running", enabled: true, active: 2, want: evaluationRunning, wantDraw: true},
		{
			name: "a degraded inventory cannot say", enabled: true, active: 2,
			reason: "the fleet source could not open 3 records", want: evaluationUnavailable,
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			h := newEvaluation(t, "plain", nil)
			fake := h.evaluationOf()
			fake.policy.Enabled = probe.enabled
			fake.coverage.Active = probe.active
			fake.coverage.Reason = probe.reason
			fake.coverage.NextDraw = time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC)

			var got evaluationPolicyResult
			decodeResponse(t, h.ok(t, "/api/evaluation/policy"), &got)
			if got.Status != probe.want {
				t.Fatalf("status = %q, want %q (detail %q)", got.Status, probe.want, got.Detail)
			}
			if got.Detail == "" {
				t.Error("no sentence explains the status")
			}
			if probe.wantDraw && got.NextDraw == "" {
				t.Error("an enabled policy with a known next draw reports none")
			}
			if !probe.enabled && got.NextDraw != "" {
				t.Errorf("a paused policy reports a next draw at %q", got.NextDraw)
			}
		})
	}
}

// TestEvaluationStatusWithoutARecordedCheckClaimsNoSchedule is the zero-time
// case, and it is its own test because the wrong answer is plausible: a
// deployment that has never completed a coverage sweep has no cadence to count
// from, and rendering "now" would be this surface inventing a schedule.
func TestEvaluationStatusWithoutARecordedCheckClaimsNoSchedule(t *testing.T) {
	h := newEvaluation(t, "plain", nil)
	fake := h.evaluationOf()
	fake.policy.Enabled = true
	fake.coverage.NextDraw = time.Time{}
	fake.coverage.Reason = ""

	var got evaluationPolicyResult
	decodeResponse(t, h.ok(t, "/api/evaluation/policy"), &got)
	if got.NextDraw != "" {
		t.Fatalf("next draw = %q, want none when no sweep has been recorded", got.NextDraw)
	}
	if got.Status != evaluationScheduled {
		t.Errorf("status = %q, want scheduled", got.Status)
	}
}

// TestEvaluationDetailAddressesAnExactRevision keeps the §4.12 binding at the
// route: the kind and the id name the immutable record that was read, and both
// are required rather than defaulted.
func TestEvaluationDetailAddressesAnExactRevision(t *testing.T) {
	h := newEvaluation(t, "plain", nil)
	fake := h.evaluationOf()

	var got evaluationDetailResult
	decodeResponse(t, h.ok(t, "/api/evaluation/detail?kind=proposal&id=prp_synthetic"), &got)
	if (fake.lastSubject != evaluation.Subject{Kind: "proposal", ID: "prp_synthetic"}) {
		t.Fatalf("service asked for %+v", fake.lastSubject)
	}
	// The disposition link names the review surface's own identity rather
	// than a URL this package assembled.
	if got.Decisions.Type != "proposal" || got.Decisions.ID != "prp_synthetic" {
		t.Errorf("decision link = %+v", got.Decisions)
	}

	for _, path := range []string{
		"/api/evaluation/detail?id=prp_synthetic",
		"/api/evaluation/detail?kind=proposal",
	} {
		response := h.get(path)
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400", path, response.StatusCode)
		}
		response.Body.Close()
	}
}

// TestEvaluationDetailOffersNoDecisionLinkForAnUnreviewableKind is the other
// half of that link. A coverage inventory spans kinds the review surface has
// no page for, and offering a link into the catch-all redirect would be worse
// than offering none.
func TestEvaluationDetailOffersNoDecisionLinkForAnUnreviewableKind(t *testing.T) {
	h := newEvaluation(t, "plain", nil)
	fake := h.evaluationOf()
	fake.detail.Item.Artifact.Subject = evaluation.Subject{Kind: "observation", ID: "obs_1"}

	var got evaluationDetailResult
	decodeResponse(t, h.ok(t, "/api/evaluation/detail?kind=observation&id=obs_1"), &got)
	if got.Decisions.Type != "" || got.Decisions.ID != "" {
		t.Fatalf("decision link = %+v, want none for a kind with no review page", got.Decisions)
	}
}

// TestEvaluationSurfaceCannotSubmitAnAssessment is the guarantee the whole
// surface exists to keep: a browser holds no run identity, no claim and no
// fence, so there is no route through which a click can produce something that
// reads like a worker's review.
//
// It is a route-level assertion because that is where the mistake would be
// made. The interface makes Submit unreachable today; a route added later that
// reached a store directly would compile perfectly.
func TestEvaluationSurfaceCannotSubmitAnAssessment(t *testing.T) {
	h := newEvaluation(t, "plain", nil)

	for _, path := range []string{
		"/api/evaluation/submit",
		"/api/evaluation/assessment",
		"/api/evaluation/draw",
		"/api/evaluation/review",
		"/api/evaluation/claim",
	} {
		response := h.post(path, `{}`)
		if response.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404: this surface has no worker route", path, response.StatusCode)
		}
		response.Body.Close()
	}

	// The operator route is not a way in either. An assessment kind reaches
	// the service unfiltered and is refused there by name, which is what
	// keeps one list of who may author what.
	fake := h.evaluationOf()
	fake.err = evaluation.ErrInvalid
	response := h.post("/api/evaluation/operator",
		`{"subject":{"kind":"proposal","id":"prp_synthetic"},"kind":"assessment",`+
			`"reason":"x","criteria":[],"related_id":""}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want the service's refusal of a run-authored kind", response.StatusCode)
	}
	if fake.lastInput.Kind != evaluation.KindAssessment {
		t.Errorf("the handler filtered the kind itself: service saw %q", fake.lastInput.Kind)
	}
}

// TestEvaluationRoutesRefuseWhenUnwired is the honest degradation every
// optional service on this surface gets: the routes exist, the request is well
// formed, and this build holds no evaluation projection to answer from.
func TestEvaluationRoutesRefuseWhenUnwired(t *testing.T) {
	h := newEvaluation(t, "plain", func(opts *Options) { opts.Evaluation = nil })

	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/evaluation/list", ""},
		{http.MethodGet, "/api/evaluation/detail?kind=proposal&id=prp_synthetic", ""},
		{http.MethodGet, "/api/evaluation/coverage", ""},
		{http.MethodGet, "/api/evaluation/policy", ""},
		{http.MethodPost, "/api/evaluation/policy", `{"version":"eval-policy-1"}`},
		{http.MethodPost, "/api/evaluation/operator",
			`{"subject":{"kind":"proposal","id":"p"},"kind":"feedback","reason":"r","criteria":[],"related_id":""}`},
	} {
		response := h.get(probe.path)
		if probe.method == http.MethodPost {
			response.Body.Close()
			response = h.post(probe.path, probe.body)
		}
		if response.StatusCode != http.StatusConflict {
			t.Errorf("%s %s status = %d, want 409", probe.method, probe.path, response.StatusCode)
		}
		if text := body(t, response); !strings.Contains(text, "evaluation service") {
			t.Errorf("%s refusal does not name the missing service: %s", probe.path, text)
		}
	}
}

// TestEvaluationPolicyPathAnswersBothMethods pins the one two-method entry in
// the router's table. It is the read the form renders from and the write it
// submits to, and splitting them would let one page's read and write disagree
// about their own shape.
func TestEvaluationPolicyPathAnswersBothMethods(t *testing.T) {
	h := newEvaluation(t, "plain", nil)

	read := h.get("/api/evaluation/policy")
	defer read.Body.Close()
	if read.StatusCode != http.StatusOK {
		t.Fatalf("GET status = %d", read.StatusCode)
	}
	write := h.post("/api/evaluation/policy", `{"version":"eval-policy-1","enabled":false}`)
	defer write.Body.Close()
	if write.StatusCode != http.StatusOK {
		t.Fatalf("POST status = %d", write.StatusCode)
	}
	// Every other method is refused by the same rule the rest of the table
	// applies, rather than falling through to the read.
	other := request(t, h.http.Client(), http.MethodDelete,
		h.http.URL+"/api/evaluation/policy", bootstrapSession(t, h.server, h.http))
	defer other.Body.Close()
	if other.StatusCode != http.StatusBadRequest {
		t.Errorf("DELETE status = %d, want 400", other.StatusCode)
	}
}

// TestEvaluationCoverageServesEveryCoveredKind keeps the inventory's own
// shape: §8.5 requires coverage to span the reviewable kinds and to be
// readable by role, including for a kind with nothing due. A page that derived
// its kind list from the rows it received would stop showing a kind the moment
// it was clean.
func TestEvaluationCoverageServesEveryCoveredKind(t *testing.T) {
	h := newEvaluation(t, "plain", nil)

	var got evaluationCoverageResult
	decodeResponse(t, h.ok(t, "/api/evaluation/coverage"), &got)
	if len(got.Kinds) == 0 || len(got.Roles) == 0 {
		t.Fatalf("coverage = %+v, want the covered kinds and roles", got)
	}
	if got.Coverage.Unreviewed != 3 || got.Coverage.Overdue != 1 {
		t.Errorf("counts = %+v, want the projection's own", got.Coverage)
	}
}
