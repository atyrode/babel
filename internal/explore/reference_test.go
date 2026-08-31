package explore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/run"
)

// recordingAppender is a reference.Appender that keeps what it was asked to
// record and answers with the store's own contract: append-only, idempotent on
// (kind, from, to).
//
// calls counts every Append including the ones idempotence collapsed, which is
// what makes the resume assertion possible: "emitted again and stored once" is
// the property, and a fake that only kept the set could not tell it from
// "never emitted again".
type recordingAppender struct {
	mu    sync.Mutex
	edges []reference.Edge
	calls int
	fail  error
}

func (a *recordingAppender) Append(_ context.Context, e reference.Edge) (reference.Edge, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	if a.fail != nil {
		return reference.Edge{}, a.fail
	}
	if err := e.Validate(); err != nil {
		return reference.Edge{}, err
	}
	for _, held := range a.edges {
		if held.Kind == e.Kind && held.From == e.From && held.To == e.To {
			return held, nil
		}
	}
	e.ID = fmt.Sprintf("edge-%02d", len(a.edges)+1)
	a.edges = append(a.edges, e)
	return e, nil
}

func (a *recordingAppender) of(kind reference.Kind) []reference.Edge {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []reference.Edge
	for _, e := range a.edges {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// keys renders the recorded edges as "kind from->to" strings, which is the
// comparable shape when a test is asserting a set rather than a payload.
func (a *recordingAppender) keys(kind reference.Kind) []string {
	var out []string
	for _, e := range a.of(kind) {
		out = append(out, string(e.Kind)+" "+e.From.String()+"->"+e.To.String())
	}
	return out
}

// withReferences attaches an appender and a session-key deriver, which is what
// a wired deployment injects: the edge store owns write-time validation and
// refuses a session endpoint that is not the deployment-scoped durable key, so
// the emitter never invents one.
//
// The deriver here is synthetic and deliberately not the selector, so a test
// asserting the endpoint cannot pass by accident on a build that fell back to
// "<harness>/<source id>".
func withReferences(a reference.Appender) func(*explore.Config) {
	return func(cfg *explore.Config) {
		cfg.References = a
		cfg.SessionKey = syntheticSessionKey
	}
}

func syntheticSessionKey(harness, sourceID string) string {
	return "sess-synthetic-deployment-" + harness + "-" + sourceID
}

// sessionKey is the durable session key of one of the harness's corpus
// sessions, under the same derivation the controller was handed.
func (h *harness) sessionKey(i int) string {
	stream := h.inputs[i].Stream
	return syntheticSessionKey(stream.Harness, stream.SourceID)
}

// TestEvidenceEdgesNameTheSessionTheLocatorCameFrom is #113's evidence
// emission.
//
// The two halves are checked together on purpose, because the whole design
// rests on the split: the byte-precise locator stays on the observation, where
// §4.3 put it and where a reader recovers the cited bytes, and the edge adds
// only the session those bytes live in so the graph can be walked from a
// session to every claim drawn from it. An edge that replaced the locator
// would be a claim nobody can reopen.
func TestEvidenceEdgesNameTheSessionTheLocatorCameFrom(t *testing.T) {
	h := newHarness(t)
	appender := &recordingAppender{}
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withReferences(appender))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-evidence"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Observations) != 2 {
		t.Fatalf("the run wrote %d observations, want 2", len(outcome.Observations))
	}

	// Both synthetic claims cite the first corpus session, so both edges name
	// it and each names it once.
	want := h.sessionKey(0)
	edges := appender.of(reference.KindEvidence)
	if len(edges) != 2 {
		t.Fatalf("minted %d evidence edges for 2 observations: %v",
			len(edges), appender.keys(reference.KindEvidence))
	}
	cited := map[string]bool{}
	for _, e := range edges {
		if e.From.Kind != "observation" {
			t.Errorf("evidence edge from %s, want an observation", e.From)
		}
		if e.To != (reference.RecordRef{Kind: explore.SessionRecordKind, ID: want}) {
			t.Errorf("evidence edge to %s, want session:%s", e.To, want)
		}
		if e.ActorKind != "run" || e.ActorRef != "r-evidence" {
			t.Errorf("evidence edge actor = %s/%s, want run/r-evidence", e.ActorKind, e.ActorRef)
		}
		if !strings.Contains(e.Note, "locator") {
			t.Errorf("evidence edge note %q does not say the locator is the authority", e.Note)
		}
		cited[e.From.ID] = true
	}
	for _, id := range outcome.Observations {
		if !cited[id] {
			t.Errorf("observation %s cites a session in its payload and has no evidence edge", id)
		}
	}

	// The locator on the record is untouched. The edge is additive: it adds a
	// coarse endpoint and takes nothing away from what makes the claim
	// verifiable.
	ctx := context.Background()
	for _, id := range outcome.Observations {
		record, err := h.frontier.Observation(ctx, id)
		if err != nil {
			t.Fatalf("read observation: %v", err)
		}
		if len(record.Payload.Evidence) != 1 {
			t.Fatalf("observation %s carries %d locators, want 1", id, len(record.Payload.Evidence))
		}
		locator := record.Payload.Evidence[0].Locator()
		if locator.Path == "" || locator.Line < 1 || len(locator.Digest) != 64 {
			t.Errorf("observation %s lost its byte-precise locator: %+v", id, locator)
		}
	}
}

// TestObjectionEvidenceIsAttributedToTheChallengerRun covers the second
// evidence site, and the branch that must not mint.
//
// §5.4's challenger is a separate job with its own run identity and its own
// receipt, so its edges have to carry that identity: an objection attributed
// to the exploration would put the challenger's citation under the wrong
// author (#96). And the objection that carries no locator becomes a candidate
// rather than an observation, which has nothing to cite - a candidate with an
// evidence edge would be §4.3 defeated through the graph.
func TestObjectionEvidenceIsAttributedToTheChallengerRun(t *testing.T) {
	h := newHarness(t)
	appender := &recordingAppender{}
	explorePayload := h.writeResult("discovery.json", h.discovery())
	grounded, err := json.Marshal(h.evidence(2, "the record the objection rests on"))
	if err != nil {
		t.Fatalf("encode evidence: %v", err)
	}
	challengePayload := h.writeRaw("challenge.json", `{
	  "objections": [
	    {"ref": "obj-grounded", "hypothesis": "${paramitem:`+explore.ParamBriefHypotheses+`:0}",
	     "grounds": "evidence", "recipe": {"id": "outcome-integrity", "version": 2},
	     "claim": {"claim": "the cited record says otherwise", "confidence": "moderate",
	               "impact": "moderate", "counter_evidence_absent": true,
	               "evidence": [`+string(grounded)+`]}},
	    {"ref": "obj-ungrounded", "hypothesis": "${paramitem:`+explore.ParamBriefHypotheses+`:0}",
	     "grounds": "missing-check", "recipe": {"id": "outcome-integrity", "version": 2},
	     "claim": {"claim": "nothing verified the claim", "confidence": "low", "impact": "low",
	               "counter_evidence_absent": true}}
	  ]
	}`)
	controller := h.controller(payloadArgs(map[explore.Stage]string{
		explore.StageExplore:   explorePayload,
		explore.StageChallenge: challengePayload,
	}), withReferences(appender))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-objections", Challenge: true})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Objections) != 2 {
		t.Fatalf("recorded %d objections, want 2", len(outcome.Objections))
	}

	counterObservation, ungroundedCandidate := outcome.Objections[0], outcome.Objections[1]
	var found bool
	for _, e := range appender.of(reference.KindEvidence) {
		if e.From.ID == ungroundedCandidate {
			t.Errorf("the ungrounded objection minted an evidence edge: %+v", e)
		}
		if e.From.ID != counterObservation {
			continue
		}
		found = true
		if e.ActorRef != "r-objections/challenge" {
			t.Errorf("counter-observation edge actor ref = %q, want the challenger's run id", e.ActorRef)
		}
		if e.To.Kind != explore.SessionRecordKind {
			t.Errorf("counter-observation cites %s, want a session", e.To)
		}
	}
	if !found {
		t.Errorf("the locator-backed objection minted no evidence edge: %v",
			appender.keys(reference.KindEvidence))
	}
}

// TestWholeObjectEvidenceMintsNoSessionEdge covers the locator that has no
// session behind it.
//
// frontier.NewEvidence admits whole-object evidence - a repository blob, a
// brokered research document - and those locators recover their bytes without
// naming a session at all. There is no session endpoint to bind to, so the
// honest answer is no edge and no warning: the claim keeps its locator, and
// the diagnostics path stays useful for the failures that mean something.
func TestWholeObjectEvidenceMintsNoSessionEdge(t *testing.T) {
	h := newHarness(t)
	appender := &recordingAppender{}
	blob, err := frontier.NewEvidence(event.Locator{
		Path:   "synthetic-repository/deploy.yaml",
		Digest: strings.Repeat("ab", 32),
	}, "a repository blob, which is not a session")
	if err != nil {
		t.Fatalf("build whole-object evidence: %v", err)
	}
	result := h.discovery()
	result.Consolidations = nil
	result.Deferred = nil
	result.Candidates = result.Candidates[:1]
	result.Candidates[0].Observations[0].Claim.Evidence = []frontier.Evidence{blob}
	payload := h.writeResult("discovery.json", result)
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withReferences(appender))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-blob"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Observations) != 1 {
		t.Fatalf("the run wrote %d observations, want 1", len(outcome.Observations))
	}
	if edges := appender.of(reference.KindEvidence); len(edges) != 0 {
		t.Errorf("a repository blob minted %d session edges: %+v", len(edges), edges)
	}
	if hasFailure(outcome.Failures, explore.FailureReference) {
		t.Errorf("a locator with no session behind it was reported as a failure: %+v", outcome.Failures)
	}
	// The claim still recovers its bytes, which is the point of tolerating it.
	record, err := h.frontier.Observation(context.Background(), outcome.Observations[0])
	if err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if record.Payload.Evidence[0].Locator().Path != "synthetic-repository/deploy.yaml" {
		t.Errorf("the whole-object locator was altered: %+v", record.Payload.Evidence[0].Locator())
	}
}

// relatedPreparation re-fixes the harness's scope with one prior output named,
// which is what `babel prepare` records when the frontier already holds work
// that looks related (#87 item 4).
func relatedPreparation(t *testing.T, h *harness, related ...run.RelatedOutput) run.Preparation {
	t.Helper()
	prep, err := run.NewPreparation(h.prep.PreparedAt, h.prep.Selection,
		run.PreparationContext{Related: related})
	if err != nil {
		t.Fatalf("re-fix the scope with related outputs: %v", err)
	}
	return prep
}

// TestInjectedPriorOutputsBecomeInspiredByEdges is #113's inspired-by
// emission at the preparation-retrieval site.
//
// What Babel knows is that the run received the cited record as refine-first
// context. What it does not know is that anything the run then wrote came out
// of it, so the edge records the adjacency and says so in its note. Every
// record the run minted gets one, because the injection was made at the job
// level and every stage carries it.
func TestInjectedPriorOutputsBecomeInspiredByEdges(t *testing.T) {
	h := newHarness(t)
	appender := &recordingAppender{}
	prior := plantFrontier(t, h, "the release pipeline skips the integration suite it claims to run")
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withReferences(appender),
		func(cfg *explore.Config) {
			cfg.Preparation = relatedPreparation(t, h,
				run.RelatedOutput{Kind: string(frontier.OutputHypothesis), ID: prior})
		})

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-inspired"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}

	var minted []string
	minted = append(minted, outcome.Hypotheses...)
	minted = append(minted, outcome.Observations...)
	minted = append(minted, outcome.Findings...)
	minted = append(minted, outcome.Proposals...)
	if len(minted) != 7 {
		t.Fatalf("the run reached %d records, want 3 candidates, 2 observations, "+
			"a finding and a proposal: %v", len(minted), minted)
	}

	edges := appender.of(reference.KindInspiredBy)
	byFrom := map[string]reference.Edge{}
	for _, e := range edges {
		if e.To != (reference.RecordRef{Kind: "hypothesis", ID: prior}) {
			t.Errorf("inspired-by edge points at %s, want the injected hypothesis:%s", e.To, prior)
		}
		if e.ActorKind != "run" || e.ActorRef != "r-inspired" {
			t.Errorf("inspired-by edge actor = %s/%s, want run/r-inspired", e.ActorKind, e.ActorRef)
		}
		if !strings.Contains(e.Note, "not a claim of derivation") {
			t.Errorf("inspired-by note %q overclaims: it must say the link is adjacency", e.Note)
		}
		byFrom[e.From.ID] = e
	}
	for _, id := range minted {
		if _, ok := byFrom[id]; !ok {
			t.Errorf("record %s was produced by a run that received the prior output and has no inspired-by edge", id)
		}
	}
	if len(edges) != len(minted) {
		t.Errorf("minted %d inspired-by edges for %d records: %v",
			len(edges), len(minted), appender.keys(reference.KindInspiredBy))
	}
	// The namespaces are the record kinds, so a render surface resolves an
	// endpoint with the identifier an operator already pastes.
	kinds := map[string]bool{}
	for _, e := range edges {
		kinds[e.From.Kind] = true
	}
	for _, want := range []string{"hypothesis", "observation", "finding", "proposal"} {
		if !kinds[want] {
			t.Errorf("no inspired-by edge is from a %s: %v", want, kinds)
		}
	}
}

// TestScopeWithNoInjectionMintsNoInspiredByEdges is the other half of the
// rule: an edge may only say a run was shown something when it was. A first
// preparation on a machine has no prior output, and asserting inspiration
// there would be inventing provenance.
func TestScopeWithNoInjectionMintsNoInspiredByEdges(t *testing.T) {
	h := newHarness(t)
	appender := &recordingAppender{}
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withReferences(appender))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-uninspired"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Hypotheses) == 0 {
		t.Fatal("the run wrote nothing, so the absence of edges proves nothing")
	}
	if edges := appender.of(reference.KindInspiredBy); len(edges) != 0 {
		t.Errorf("a scope that named no prior output minted %d inspired-by edges: %+v",
			len(edges), edges)
	}
}

// TestResumeReEmitsTheSameEdgesIdempotently is §6.5's resumability rule
// applied to the graph.
//
// A resumed attempt recognizes the records the first one committed rather than
// writing second copies, and it re-emits their edges rather than assuming the
// first attempt got that far - which is what repairs an attempt killed between
// a record's write and its shadow. Re-emission is safe because Append is
// idempotent on (kind, from, to), so the assertion is both halves: the store
// was asked again, and the graph did not grow.
func TestResumeReEmitsTheSameEdgesIdempotently(t *testing.T) {
	h := newHarness(t)
	appender := &recordingAppender{}
	prior := plantFrontier(t, h, "the release pipeline skips the integration suite it claims to run")
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withReferences(appender),
		func(cfg *explore.Config) {
			cfg.Preparation = relatedPreparation(t, h,
				run.RelatedOutput{Kind: string(frontier.OutputHypothesis), ID: prior})
		})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := true
	interrupted, err := controller.Explore(ctx, explore.Options{
		Authority: testAuthority,
		RunID:     "r-resumed-edges",
		OnRecord: func(e explore.RecordEvent) {
			if first && e.Type == frontier.EntityHypothesis {
				first = false
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first attempt error = %v, want a cancellation", err)
	}
	if len(interrupted.Hypotheses) == 0 {
		t.Fatal("the interrupted attempt committed nothing to re-emit for")
	}
	afterFirst := len(appender.of(reference.KindInspiredBy))
	callsAfterFirst := appender.calls
	if afterFirst == 0 {
		t.Fatal("the interrupted attempt minted no edges")
	}

	resumed, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-resumed-edges"})
	if err != nil {
		t.Fatalf("resumed attempt: %v (failures %+v)", err, resumed.Failures)
	}
	if resumed.Reused != len(interrupted.Hypotheses) {
		t.Fatalf("resumption reused %d records, want the %d already committed",
			resumed.Reused, len(interrupted.Hypotheses))
	}

	// The reused records were offered to the store again: that is what makes
	// an attempt killed before its edges landed recoverable.
	reoffered := appender.calls - callsAfterFirst
	if reoffered < resumed.Reused {
		t.Errorf("the resumed attempt appended %d edges for %d reused records; "+
			"reused records must be re-emitted", reoffered, resumed.Reused)
	}
	// And nothing was duplicated: every re-emission collapsed onto the edge
	// already recorded.
	held := map[string]int{}
	for _, key := range append(appender.keys(reference.KindInspiredBy),
		appender.keys(reference.KindEvidence)...) {
		held[key]++
	}
	for key, n := range held {
		if n != 1 {
			t.Errorf("edge %q is held %d times; re-emission is not idempotent", key, n)
		}
	}
	// The interrupted attempt's edges are still the same edges, now joined by
	// the records the resumed attempt finished.
	if got := len(appender.of(reference.KindInspiredBy)); got < afterFirst {
		t.Errorf("the graph shrank from %d inspired-by edges to %d", afterFirst, got)
	}
}

// TestAbsentAppenderMintsNothingAndChangesNoOutcome is #113's nil-injection
// rule at this package's emission sites: no Appender means the feature is
// absent, which is a supported deployment and not a degraded run.
func TestAbsentAppenderMintsNothingAndChangesNoOutcome(t *testing.T) {
	h := newHarness(t)
	prior := plantFrontier(t, h, "the release pipeline skips the integration suite it claims to run")
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		func(cfg *explore.Config) {
			cfg.Preparation = relatedPreparation(t, h,
				run.RelatedOutput{Kind: string(frontier.OutputHypothesis), ID: prior})
		})

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-no-graph"})
	if err != nil {
		t.Fatalf("a run with no reference graph failed: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Hypotheses) != 3 || len(outcome.Observations) != 2 ||
		len(outcome.Findings) != 1 || len(outcome.Proposals) != 1 {
		t.Errorf("the absent graph changed what the run recorded: %d/%d/%d/%d",
			len(outcome.Hypotheses), len(outcome.Observations),
			len(outcome.Findings), len(outcome.Proposals))
	}
	if hasFailure(outcome.Failures, explore.FailureReference) {
		t.Errorf("an absent graph was reported as a failure: %+v", outcome.Failures)
	}
}

// TestEdgeRefusalIsAWarningAndNotTheRunsVerdict is #113's failure rule at this
// package's emission sites.
//
// An edge is a shadow of a record that is already durable, so a refused
// append degrades navigation and nothing else. The run's verdict has to stay
// the analysis's - §6.5 already draws that line for publication - and the
// refusal still has to be visible, because a graph silently drifting out of
// date is worse than one an operator knows is incomplete.
func TestEdgeRefusalIsAWarningAndNotTheRunsVerdict(t *testing.T) {
	h := newHarness(t)
	appender := &recordingAppender{fail: errors.New("the edge store refuses this endpoint")}
	prior := plantFrontier(t, h, "the release pipeline skips the integration suite it claims to run")
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		withReferences(appender),
		func(cfg *explore.Config) {
			cfg.Preparation = relatedPreparation(t, h,
				run.RelatedOutput{Kind: string(frontier.OutputHypothesis), ID: prior})
		})

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-refused-edges"})
	if err != nil {
		t.Fatalf("a refused edge became the run's verdict: %v", err)
	}
	if len(outcome.Hypotheses) != 3 || len(outcome.Observations) != 2 {
		t.Errorf("a refused edge cost the run records: %d candidates, %d observations",
			len(outcome.Hypotheses), len(outcome.Observations))
	}
	if !hasFailure(outcome.Failures, explore.FailureReference) {
		t.Fatalf("the refusals were not recorded anywhere: %+v", outcome.Failures)
	}
	if outcome.Receipt == nil {
		t.Fatal("no receipt was written")
	}
	if !hasFailure(outcome.Receipt.Body.Failures, explore.FailureReference) {
		t.Errorf("the receipt does not record the refused edges: %+v", outcome.Receipt.Body.Failures)
	}
	// The refusal names the shape an operator would re-emit, and not the note.
	var reported bool
	for _, failure := range outcome.Failures {
		if failure.Code != explore.FailureReference {
			continue
		}
		reported = true
		if !strings.Contains(failure.Message, "->") && !strings.Contains(failure.Message, "edge from") {
			t.Errorf("failure %q does not name the edge that is missing", failure.Message)
		}
	}
	if !reported {
		t.Error("no reference failure was recorded")
	}
}
