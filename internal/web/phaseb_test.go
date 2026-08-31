package web

// The Phase B API's harness and its read surface. Everything here runs against
// the real services over throwaway directories: no transcript, no credential,
// no network, and no fake standing in for a store, because the property most of
// these tests are about is that the route and the service agree.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
	"github.com/atyrode/babel/internal/run"
)

// operatorID is the attributed identity the launch session carries. Every
// durable decision these tests make is recorded against it, which is what makes
// attribution assertable rather than assumed.
const operatorID = "operator-under-test"

// phaseB is one throwaway deployment behind one server: the durable database
// with the frontier, run and review components open on it, a Reality ledger, a
// retrieval index, and the fixture records the routes read.
type phaseB struct {
	t          *testing.T
	ctx        context.Context
	server     *Server
	http       *httptest.Server
	front      *frontier.Store
	runs       *run.Store
	review     *review.Service
	actions    *disposition.Store
	reality    *reality.Store
	index      *index.Index
	authority  review.Authority
	hypothesis frontier.Hypothesis
	finding    frontier.Finding
	proposal   frontier.Proposal
	// action and invitation are #87's fixtures: one proposed next action
	// nobody has answered, and one instruction-free invitation nobody has
	// consumed, both against the candidate.
	action     disposition.Disposition
	invitation disposition.Invitation
	// resting is a candidate the frontier has rejected, which is what a
	// revive needs: #87 makes rejection a resting place rather than an
	// ending, and the transition out of one is refused for a candidate that
	// is already on the frontier.
	resting frontier.Hypothesis
	// original and revised are one two-entry chain: the wording a run
	// emitted and the operator revision that superseded it. The head is
	// revised, which is what makes original a stale-head fixture.
	original frontier.Hypothesis
	revised  frontier.Hypothesis
	entity   reality.Entity
	question reality.Question
	answer   reality.Answer
	plan     reality.Plan
}

// newPhaseB opens the services, writes one whole §4.2 development path plus one
// §4.8 question-answer-plan chain, and serves them. text is woven into every
// text field the fixtures carry, so a malicious-content test is the same fixture
// as the ordinary one rather than a special case.
func newPhaseB(t *testing.T, text string, mutate func(*Options)) *phaseB {
	t.Helper()
	dir := t.TempDir()
	h := &phaseB{t: t, ctx: context.Background()}

	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	t.Cleanup(func() { front.Close() })
	runs, err := run.Open(dir)
	if err != nil {
		t.Fatalf("run.Open: %v", err)
	}
	t.Cleanup(func() { runs.Close() })
	service, err := review.Open(dir, front, runs)
	if err != nil {
		t.Fatalf("review.Open: %v", err)
	}
	t.Cleanup(func() { service.Close() })
	actions, err := disposition.Open(dir, front)
	if err != nil {
		t.Fatalf("disposition.Open: %v", err)
	}
	t.Cleanup(func() { actions.Close() })
	ledger, err := reality.Open(t.TempDir())
	if err != nil {
		t.Fatalf("reality.Open: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	retrieval, err := index.Open(t.TempDir())
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { retrieval.Close() })
	authority, err := review.NewAuthority(operatorID)
	if err != nil {
		t.Fatalf("review.NewAuthority: %v", err)
	}
	h.front, h.runs, h.review, h.reality, h.index, h.authority = front, runs, service, ledger, retrieval, authority
	h.actions = actions

	h.writeFrontier(text)
	h.writeActions(text)
	h.writeReality(text)
	h.indexSession(text)
	recipes, err := cookbook.Embedded()
	if err != nil {
		t.Fatalf("cookbook.Embedded: %v", err)
	}

	opts := Options{
		Operator:     operatorID,
		Review:       service,
		Frontier:     front,
		Reality:      ledger,
		Search:       retrieval,
		Cookbook:     recipes,
		Dispositions: actions,
		Reviver:      front,
		// The fleet reader every server here reads through. It is wired by
		// default rather than per test because issue #109 makes attribution
		// and sync state part of every listing's shape: a harness that left it
		// out would exercise the merged surfaces only in the tests that
		// remembered to ask for them. fleet_test.go describes the fixture.
		Fleet: fleetFixture(text),
		Runs: runLister{{
			ReceiptID:     "rcp-1 " + text,
			RunID:         "run-1 " + text,
			PreparationID: "prep-1 " + text,
			Revision:      1,
			RecordedAt:    "2026-03-01T12:00:00Z",
			Sync:          run.SyncPending,
			Authority:     RunAuthority{Kind: "operator", Ref: "babel explore " + text},
		}},
	}
	if mutate != nil {
		mutate(&opts)
	}
	h.server, h.http = testServer(t, opts)
	return h
}

// writeFrontier records hypothesis -> observation -> finding -> proposal, the
// mandatory §4.2 path, and enrols each reviewable record so the queue and the
// listings have something to show.
func (h *phaseB) writeFrontier(text string) {
	h.t.Helper()
	hypothesis, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID: "run-1",
		Payload: frontier.HypothesisPayload{
			Statement:         "verification may be reported rather than performed " + text,
			OriginCues:        []string{"a claimed test run " + text},
			ProvisionalLabels: []string{"outcome-integrity " + text},
			Novelty:           0.5,
			Priority:          0.5,
			Notes:             "noticed across three runs " + text,
		},
	})
	if err != nil {
		h.t.Fatalf("CreateHypothesis: %v", err)
	}
	h.hypothesis = hypothesis

	observation, err := h.front.CreateObservation(h.ctx, frontier.ObservationInput{
		HypothesisID:  hypothesis.ID,
		RunID:         "run-1",
		RecipeID:      "outcome-integrity",
		RecipeVersion: 1,
		Payload: frontier.ObservationPayload{
			Claim:                 "the agent claimed the tests passed without running them " + text,
			Category:              "outcome " + text,
			Confidence:            frontier.ConfidenceModerate,
			Impact:                frontier.ImpactModerate,
			Evidence:              []frontier.Evidence{h.evidence("session-a", 12, "the claim is here "+text)},
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		h.t.Fatalf("CreateObservation: %v", err)
	}

	// A second candidate, linked, so the link view has a far end to name.
	other, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID:   "run-1",
		Payload: frontier.HypothesisPayload{Statement: "the harness reports success early " + text, Novelty: 0.4, Priority: 0.4},
	})
	if err != nil {
		h.t.Fatalf("CreateHypothesis: %v", err)
	}
	if _, err := h.front.Link(h.ctx, frontier.LinkInput{
		FromID: hypothesis.ID,
		ToID:   other.ID,
		Type:   frontier.LinkCorroborates,
		Note:   "both describe the same step " + text,
	}); err != nil {
		h.t.Fatalf("Link: %v", err)
	}

	finding, err := h.front.CreateFinding(h.ctx, frontier.FindingInput{
		RunID:          "run-1",
		ObservationIDs: []string{observation.ID},
		Payload: frontier.FindingPayload{
			Title:                 "claimed verification " + text,
			Pattern:               "the same step is repeated after it reports success " + text,
			Significance:          "a reported outcome is not an observed one " + text,
			Scope:                 []string{"three sessions " + text},
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		h.t.Fatalf("CreateFinding: %v", err)
	}
	h.finding = finding

	proposal, err := h.front.CreateProposal(h.ctx, frontier.ProposalInput{
		RunID:      "run-1",
		FindingIDs: []string{finding.ID},
		Payload: frontier.ProposalPayload{
			Title:          "verify independently " + text,
			Problem:        "the verification step is skipped when the previous step reports success " + text,
			Outcome:        "verify independently of the reported outcome " + text,
			Uncertainty:    "one corpus, three sessions " + text,
			Impact:         frontier.ImpactModerate,
			Classification: frontier.ClassificationPrivate,
			Risks:          []string{"more expensive runs " + text},
			Targets:        []frontier.Target{{System: "the harness " + text, Confidence: frontier.ConfidenceLow, Rationale: "guessing " + text}},
		},
	})
	if err != nil {
		h.t.Fatalf("CreateProposal: %v", err)
	}
	h.proposal = proposal

	for _, subject := range []frontier.Ref{
		{Type: frontier.EntityHypothesis, ID: hypothesis.ID},
		{Type: frontier.EntityHypothesis, ID: other.ID},
		{Type: frontier.EntityFinding, ID: finding.ID},
		{Type: frontier.EntityProposal, ID: proposal.ID},
	} {
		if _, err := h.review.Enroll(h.ctx, subject); err != nil {
			h.t.Fatalf("Enroll(%s): %v", subject.ID, err)
		}
	}
}

// writeActions records #87's fixtures: a candidate a run rejected so a revive
// has something at rest to move, one proposed next action against the live
// candidate, and one open invitation against it.
//
// The action is a develop-further rather than a draft-issue, because a
// draft-issue binds to a verified git checkout (#88) and a unit test that
// needed one would be testing internal/disposition's anchor reader instead of
// this surface. The draft rendering has its own test, against a real checkout.
func (h *phaseB) writeActions(text string) {
	h.t.Helper()
	resting, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID:   "run-1",
		Payload: frontier.HypothesisPayload{Statement: "the retry loop hides the first failure " + text, Novelty: 0.3, Priority: 0.3},
	})
	if err != nil {
		h.t.Fatalf("CreateHypothesis: %v", err)
	}
	if _, err := h.front.SetStatus(h.ctx, frontier.StatusInput{
		HypothesisID: resting.ID,
		Status:       frontier.StatusRejected,
		RunID:        "run-1",
		Note:         "the evidence names one session " + text,
	}); err != nil {
		h.t.Fatalf("SetStatus: %v", err)
	}
	reread, err := h.front.Hypothesis(h.ctx, resting.ID)
	if err != nil {
		h.t.Fatalf("Hypothesis: %v", err)
	}
	h.resting = reread

	record := frontier.Ref{Type: frontier.EntityHypothesis, ID: h.hypothesis.ID}
	action, err := h.actions.Propose(h.ctx, disposition.ProposeInput{
		Record:     record,
		Kind:       disposition.KindDevelopFurther,
		ProposedBy: frontier.Run("run-1"),
		Ref:        "action-1",
		Payload: disposition.Payload{
			Summary:   "read the two adjacent sessions before rewording " + text,
			Rationale: "the claim rests on one transcript " + text,
		},
	})
	if err != nil {
		h.t.Fatalf("Propose: %v", err)
	}
	h.action = action

	invitation, err := h.actions.Invite(h.ctx, disposition.InviteInput{Record: record, By: operatorID})
	if err != nil {
		h.t.Fatalf("Invite: %v", err)
	}
	h.invitation = invitation

	// A two-entry chain: a run's original and an operator's revision of it,
	// which is what a revision-history timeline is for. The original is left
	// readable, because #87's chain replaces nothing it supersedes.
	original, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID:   "run-1",
		Payload: frontier.HypothesisPayload{Statement: "the manifest is read once per deploy " + text, Novelty: 0.6, Priority: 0.6},
	})
	if err != nil {
		h.t.Fatalf("CreateHypothesis: %v", err)
	}
	h.original = original
	revised, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
		RunID:      "run-1",
		AncestorID: original.ID,
		Actor:      frontier.Operator(operatorID),
		Reason:     "the original conflated two deploy steps " + text,
		Payload: frontier.HypothesisPayload{
			Statement: "the manifest is re-read per deploy step, and the second read is stale " + text,
			Novelty:   0.6, Priority: 0.6,
		},
	})
	if err != nil {
		h.t.Fatalf("CreateHypothesis: %v", err)
	}
	h.revised = revised
}

// writeReality records an entity with an alias and a question whose answer the
// interpreter turned into a plan. The plan is left proposed: accepting it is
// what the mutation tests do, through the route.
func (h *phaseB) writeReality(text string) {
	h.t.Helper()
	entity, err := h.reality.CreateEntity(h.ctx, reality.EntityInput{
		Kind:    reality.EntityProject,
		Payload: reality.EntityPayload{DisplayName: "a project " + text, Notes: "named in three sessions " + text},
	})
	if err != nil {
		h.t.Fatalf("CreateEntity: %v", err)
	}
	h.entity = entity
	if _, err := h.reality.AddAlias(h.ctx, reality.AliasInput{
		EntityID: entity.ID,
		Kind:     reality.AliasName,
		Payload:  reality.AliasPayload{Value: "the same project " + text, Note: "chat terminology " + text},
	}); err != nil {
		h.t.Fatalf("AddAlias: %v", err)
	}
	second, err := h.reality.CreateEntity(h.ctx, reality.EntityInput{
		Kind:    reality.EntityRepository,
		Payload: reality.EntityPayload{DisplayName: "a repository " + text},
	})
	if err != nil {
		h.t.Fatalf("CreateEntity: %v", err)
	}
	if _, err := h.reality.AddRelationship(h.ctx, reality.RelationshipInput{
		FromID:  entity.ID,
		ToID:    second.ID,
		Kind:    reality.RelationContains,
		Payload: reality.RelationshipPayload{Note: "the project holds it " + text},
	}); err != nil {
		h.t.Fatalf("AddRelationship: %v", err)
	}

	question, err := h.reality.Ask(h.ctx, reality.QuestionInput{
		Kind:              reality.KindAcquireContext,
		Class:             reality.ClassBlocking,
		Sensitivity:       reality.SensitivityRoutine,
		ExpectedAuthority: reality.AuthorityOperator,
		TargetEntityIDs:   []string{entity.ID},
		TargetPredicates:  []reality.Predicate{reality.PredicateLifecycle},
		MaterialEvidence:  []string{"observation-1"},
		Payload: reality.QuestionPayload{
			Prompt:   "is this project still active? " + text,
			WhyAsked: "a hypothesis about it cannot be scoped without knowing " + text,
		},
	})
	if err != nil {
		h.t.Fatalf("Ask: %v", err)
	}
	h.question = question

	// A second question, already answered and interpreted, so the inbox has a
	// plan to accept without the answer route having run yet.
	planned, err := h.reality.Ask(h.ctx, reality.QuestionInput{
		Kind:              reality.KindResolveEntity,
		Class:             reality.ClassMaintenance,
		Sensitivity:       reality.SensitivityRoutine,
		ExpectedAuthority: reality.AuthorityOperator,
		TargetEntityIDs:   []string{entity.ID},
		TargetPredicates:  []reality.Predicate{reality.PredicateLifecycle},
		MaterialEvidence:  []string{"observation-2"},
		Payload: reality.QuestionPayload{
			Prompt:   "is the other name the same project? " + text,
			WhyAsked: "two names appear for one path " + text,
		},
	})
	if err != nil {
		h.t.Fatalf("Ask: %v", err)
	}
	answer, err := h.reality.RecordAnswer(h.ctx, reality.AnswerInput{
		QuestionID: planned.ID,
		Author:     operatorID,
		At:         time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
		Outcome:    reality.OutcomeAnswered,
		Text:       "yes, and it is dormant now " + text,
	})
	if err != nil {
		h.t.Fatalf("RecordAnswer: %v", err)
	}
	h.answer = answer
	if err := h.reality.BeginInterpretation(h.ctx, planned.ID); err != nil {
		h.t.Fatalf("BeginInterpretation: %v", err)
	}
	observed := time.Date(2026, 3, 1, 12, 30, 0, 0, time.UTC)
	plan, _, err := h.reality.RecordPlan(h.ctx, reality.PlanInput{
		QuestionID:         planned.ID,
		AnswerID:           answer.ID,
		InterpreterVersion: 3,
		Summary:            "record dormancy " + text,
		Kinds:              []reality.ActionKind{reality.ActionAssertFact},
		Actions: []reality.ActionPayload{{
			Rationale: "the operator stated the project is dormant " + text,
			Fact: &reality.FactInput{
				SubjectID:   h.entity.ID,
				Predicate:   reality.PredicateLifecycle,
				Value:       reality.FactValue{Kind: reality.ValueEnum, Enum: reality.LifecycleDormant},
				ValidFrom:   observed,
				ObservedAt:  observed,
				Confidence:  reality.ConfidenceHigh,
				Sensitivity: reality.SensitivityRoutine,
			},
		}},
	})
	if err != nil {
		h.t.Fatalf("RecordPlan: %v", err)
	}
	h.plan = plan
}

func (h *phaseB) evidence(name string, line int, note string) frontier.Evidence {
	h.t.Helper()
	sum := sha256.Sum256([]byte(name + fmt.Sprint(line)))
	ev, err := frontier.NewEvidence(event.Locator{
		Path:       "/synthetic/corpus/" + name + ".jsonl",
		Line:       line,
		ByteOffset: int64(line * 512),
		Digest:     hex.EncodeToString(sum[:]),
	}, note)
	if err != nil {
		h.t.Fatalf("frontier.NewEvidence: %v", err)
	}
	return ev
}

// indexSession writes one synthetic OMP session and indexes it, so the search
// route has a corpus. The log is written by this test: §10 keeps real session
// data out of the repository, and a two-record fixture is enough to prove a hit
// travels with its locator and without a rank.
func (h *phaseB) indexSession(text string) {
	h.t.Helper()
	path := filepath.Join(h.t.TempDir(), "session.jsonl")
	records := []string{
		`{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000000b",` +
			`"timestamp":"2026-05-01T00:00:00.000Z","cwd":"/synthetic/workspace",` +
			`"title":` + mustJSON(h.t, "synthetic session "+text) + `}`,
		`{"type":"message","id":"b0000001","parentId":null,"timestamp":"2026-05-01T00:01:00.000Z",` +
			`"message":{"role":"user","content":[{"type":"text","text":` +
			mustJSON(h.t, "verification was reported rather than performed "+text) + `}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0o600); err != nil {
		h.t.Fatalf("write synthetic session: %v", err)
	}
	result, err := h.index.IndexSession(h.ctx, event.Stream{
		Harness:       event.HarnessOMP,
		AdapterSchema: 1,
		SourceID:      "synthetic-session",
		Path:          path,
	})
	if err != nil {
		h.t.Fatalf("IndexSession: %v", err)
	}
	if result.Events == 0 {
		h.t.Fatalf("synthetic session indexed no events: %+v", result)
	}
}

// get performs an authorized GET and returns the response.
func (h *phaseB) get(path string) *http.Response {
	h.t.Helper()
	return request(h.t, h.http.Client(), http.MethodGet, h.http.URL+path, bootstrapSession(h.t, h.server, h.http))
}

// post performs an authorized POST with a JSON body.
func (h *phaseB) post(path, body string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.http.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	authorize(req, bootstrapSession(h.t, h.server, h.http))
	req.Header.Set("Content-Type", "application/json")
	response, err := h.http.Client().Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	return response
}

// body reads a response body as text, which is what the escaping assertions are
// about: the bytes on the wire rather than a decoded value.
func body(t *testing.T, response *http.Response) string {
	t.Helper()
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// phaseBRoute is one route of the new surface, with a request that reaches its
// handler rather than merely its guard.
type phaseBRoute struct {
	name     string
	method   string
	path     string
	body     string
	mutating bool
}

// phaseBRoutes enumerates every route this file adds. The list is the test's
// subject as much as its input: a route added without an entry here is a route
// with no session, origin, no-store, or escaping coverage.
func phaseBRoutes(h *phaseB) []phaseBRoute {
	return []phaseBRoute{
		// The dashboard's aggregate read is enrolled here rather than
		// covered separately, because it renders records from every
		// service these routes reach: it needs the same session, origin,
		// no-store, read-only and escaping coverage they do.
		{name: "overview", method: http.MethodGet, path: "/api/overview"},
		{name: "analysis state", method: http.MethodGet, path: "/api/analysis/state"},
		{name: "hypotheses", method: http.MethodGet, path: "/api/hypotheses"},
		{name: "hypothesis", method: http.MethodGet, path: "/api/hypothesis?id=" + h.hypothesis.ID},
		{name: "findings", method: http.MethodGet, path: "/api/findings"},
		{name: "finding", method: http.MethodGet, path: "/api/finding?id=" + h.finding.ID},
		{name: "review queue", method: http.MethodGet, path: "/api/review/queue?status=all"},
		{name: "review history", method: http.MethodGet, path: "/api/review/history?type=proposal&id=" + h.proposal.ID},
		{name: "export json", method: http.MethodGet, path: "/api/export?type=proposal&id=" + h.proposal.ID + "&format=json"},
		{name: "export markdown", method: http.MethodGet, path: "/api/export?type=proposal&id=" + h.proposal.ID + "&format=markdown"},
		{name: "reality inbox", method: http.MethodGet, path: "/api/reality/inbox"},
		{name: "reality entity", method: http.MethodGet, path: "/api/reality/entity?id=" + h.entity.ID},
		{name: "search", method: http.MethodGet, path: "/api/search?q=verification"},
		{name: "record revisions", method: http.MethodGet,
			path: "/api/record/revisions?type=hypothesis&id=" + h.original.ID},
		{name: "record dispositions", method: http.MethodGet,
			path: "/api/record/dispositions?type=hypothesis&id=" + h.hypothesis.ID},
		// Issue #109's fleet read. Both carry another host's records, which
		// means every string in them arrived from a machine this one does not
		// control: hostile content is the normal case here rather than the
		// exceptional one, so both are enrolled in the escaping sweep.
		//
		// The merged listings are enrolled as their own entries rather than by
		// widening the local ones, because ?fleet=1 is what makes a response
		// carry a remote host's display name and a remote record's wording.
		{name: "fleet records", method: http.MethodGet, path: "/api/fleet/records?pending=1"},
		{name: "fleet hosts", method: http.MethodGet, path: "/api/fleet/hosts?pending=1"},
		{name: "fleet hypotheses", method: http.MethodGet, path: "/api/hypotheses?fleet=1"},
		{name: "fleet findings", method: http.MethodGet, path: "/api/findings?fleet=1"},
		{name: "fleet review queue", method: http.MethodGet, path: "/api/review/queue?status=all&fleet=1"},
		{
			name: "review decide", method: http.MethodPost, path: "/api/review/decide", mutating: true,
			body: `{"subject":{"type":"proposal","id":"` + h.proposal.ID + `"},"disposition":"defer"}`,
		},
		{
			name: "review context", method: http.MethodPost, path: "/api/review/context", mutating: true,
			body: `{"text":"the corpus is small"}`,
		},
		{
			name: "reality answer", method: http.MethodPost, path: "/api/reality/answer", mutating: true,
			body: `{"questionId":"` + h.question.ID + `","text":"it is dormant"}`,
		},
		{
			name: "reality plan accept", method: http.MethodPost, path: "/api/reality/plan/accept", mutating: true,
			body: `{"planId":"` + h.plan.ID + `"}`,
		},
		// #87's three record actions. Each body carries the chain head the
		// page would have been rendered against, which for an unrevised
		// record is the record itself; records_test.go covers what happens
		// when it is not.
		{
			name: "record disposition decide", method: http.MethodPost,
			path: "/api/record/disposition/decide", mutating: true,
			body: `{"dispositionId":"` + h.action.ID + `","ruling":"declined","headId":"` + h.hypothesis.ID + `"}`,
		},
		{
			name: "record invite", method: http.MethodPost, path: "/api/record/invite", mutating: true,
			body: `{"record":{"type":"hypothesis","id":"` + h.hypothesis.ID + `"},"headId":"` + h.hypothesis.ID + `"}`,
		},
		{
			name: "record revive", method: http.MethodPost, path: "/api/record/revive", mutating: true,
			body: `{"record":{"type":"hypothesis","id":"` + h.resting.ID + `"},` +
				`"reason":"a second session shows the same first failure","headId":"` + h.resting.ID + `"}`,
		},
	}
}

// TestPhaseBRoutesShareThePhaseAGuard is the one assertion every new route needs
// and none of them implements: the launch session, the loopback Host, the Origin
// check, and `no-store` come from the middleware Phase A already had, so a route
// added under /api inherits them rather than restating them. The failure this
// prevents is a Phase B route that answers an unauthenticated browser because
// its handler forgot a check its neighbours make.
func TestPhaseBRoutesShareThePhaseAGuard(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, route := range phaseBRoutes(h) {
		t.Run(route.name, func(t *testing.T) {
			for _, guard := range []struct {
				name    string
				session string
				origin  string
				host    string
				status  int
			}{
				{name: "no session", status: http.StatusUnauthorized},
				{name: "wrong session", session: strings.Repeat("0", 64), status: http.StatusUnauthorized},
				{name: "cross origin", session: bootstrapSession(h.t, h.server, h.http), origin: "http://evil.example", status: http.StatusForbidden},
				{name: "rebound host", session: bootstrapSession(h.t, h.server, h.http), host: "evil.example", status: http.StatusForbidden},
			} {
				t.Run(guard.name, func(t *testing.T) {
					var reader io.Reader
					if route.body != "" {
						reader = strings.NewReader(route.body)
					}
					req, err := http.NewRequest(route.method, h.http.URL+route.path, reader)
					if err != nil {
						t.Fatal(err)
					}
					if guard.session != "" {
						authorize(req, guard.session)
					}
					if guard.origin != "" {
						req.Header.Set("Origin", guard.origin)
					}
					if guard.host != "" {
						req.Host = guard.host
					}
					response, err := h.http.Client().Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer response.Body.Close()
					if response.StatusCode != guard.status {
						t.Errorf("status = %d, want %d", response.StatusCode, guard.status)
					}
					if got := response.Header.Get("Cache-Control"); got != "no-store" {
						t.Errorf("Cache-Control = %q", got)
					}
				})
			}

			// The same request with the session succeeds, so the refusals
			// above are the guard's work and not a broken request.
			response := h.request(route)
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Errorf("authorized status = %d body %q", response.StatusCode, body(t, response))
			}
			if got := response.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("authorized Cache-Control = %q", got)
			}
		})
	}
}

func (h *phaseB) request(route phaseBRoute) *http.Response {
	h.t.Helper()
	if route.method == http.MethodPost {
		return h.post(route.path, route.body)
	}
	return h.get(route.path)
}

// TestPhaseBReadRoutesRejectMutationAndBack lists the whole new surface by
// method, which is how "there is no undocumented way to write" is checked: a
// read route refuses POST and a mutation refuses GET, so every entry point that
// changes durable state is one of the four in the contract.
func TestPhaseBRoutesAcceptOneMethodEach(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	for _, route := range phaseBRoutes(h) {
		t.Run(route.name, func(t *testing.T) {
			wrong := http.MethodPost
			if route.mutating {
				wrong = http.MethodGet
			}
			response := request(t, h.http.Client(), wrong, h.http.URL+route.path, bootstrapSession(h.t, h.server, h.http))
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s %s status = %d, want 400", wrong, route.path, response.StatusCode)
			}
		})
	}
}

// TestPhaseBReadRoutes checks each read route answers with the shape the web
// application is built against.
func TestPhaseBReadRoutes(t *testing.T) {
	h := newPhaseB(t, "plain", func(opts *Options) {
		opts.Runs = runLister{{ReceiptID: "rcp-1", RunID: "run-1", Revision: 1, Sync: run.SyncPending}}
	})

	t.Run("analysis state refuses exploration and lists runs", func(t *testing.T) {
		var got analysisState
		decodeResponse(t, h.get("/api/analysis/state"), &got)
		if !got.Configured || got.Worker.Available || got.Worker.Detail == "" {
			t.Fatalf("state = %+v", got)
		}
		if len(got.Runs) != 1 || got.Runs[0].ReceiptID != "rcp-1" || got.RunsTotal != 1 {
			t.Fatalf("runs = %+v", got.Runs)
		}
	})

	t.Run("hypotheses list every status", func(t *testing.T) {
		var got hypothesisList
		decodeResponse(t, h.get("/api/hypotheses"), &got)
		if got.Total != 3 || len(got.Items) != 3 {
			t.Fatalf("hypotheses = %+v", got)
		}
		var found HypothesisSummary
		for _, item := range got.Items {
			if item.ID == h.hypothesis.ID {
				found = item
			}
		}
		if found.Statement == "" || found.Observations != 1 || found.Status != string(frontier.StatusUntriaged) ||
			found.ReviewStatus != string(frontier.ReviewNew) {
			t.Fatalf("hypothesis summary = %+v", found)
		}
	})

	t.Run("hypothesis status filter refuses an unknown status", func(t *testing.T) {
		response := h.get("/api/hypotheses?status=nonsense")
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d", response.StatusCode)
		}
		var filtered hypothesisList
		decodeResponse(t, h.get("/api/hypotheses?status=untriaged"), &filtered)
		if filtered.Total != 3 {
			t.Fatalf("filtered = %+v", filtered)
		}
		var none hypothesisList
		decodeResponse(t, h.get("/api/hypotheses?status=promoted"), &none)
		if none.Total != 0 || len(none.Items) != 0 {
			t.Fatalf("promoted = %+v", none)
		}
	})

	t.Run("hypothesis detail carries history, observations, links and lineage", func(t *testing.T) {
		var got hypothesisDetail
		decodeResponse(t, h.get("/api/hypothesis?id="+h.hypothesis.ID), &got)
		if got.Hypothesis.ID != h.hypothesis.ID || got.Hypothesis.Payload.Statement == "" {
			t.Fatalf("hypothesis = %+v", got.Hypothesis)
		}
		if len(got.StatusHistory) == 0 || len(got.Observations) != 1 || len(got.Links) != 1 {
			t.Fatalf("detail = %+v", got)
		}
		if got.Links[0].OtherStatement == "" {
			t.Errorf("link does not name the far end: %+v", got.Links[0])
		}
		if len(got.Observations[0].Payload.Evidence) != 1 {
			t.Errorf("observation lost its evidence: %+v", got.Observations[0])
		}
		if got.Lineage.Node.ID != h.hypothesis.ID {
			t.Errorf("lineage = %+v", got.Lineage)
		}
	})

	t.Run("findings and finding detail", func(t *testing.T) {
		var list findingList
		decodeResponse(t, h.get("/api/findings"), &list)
		if list.Total != 1 || len(list.Items) != 1 || list.Items[0].Observations != 1 {
			t.Fatalf("findings = %+v", list)
		}
		var detail findingDetail
		decodeResponse(t, h.get("/api/finding?id="+h.finding.ID), &detail)
		if len(detail.Observations) != 1 || len(detail.Proposals) != 1 ||
			detail.Proposals[0].ID != h.proposal.ID {
			t.Fatalf("finding detail = %+v", detail)
		}
	})

	t.Run("review queue and history", func(t *testing.T) {
		var queue queueResult
		decodeResponse(t, h.get("/api/review/queue?status=all"), &queue)
		if queue.Total != 4 {
			t.Fatalf("queue = %+v", queue)
		}
		for _, item := range queue.Items {
			if item.Excerpt == "" {
				t.Errorf("queue row has no excerpt: %+v", item)
			}
		}
		var typed queueResult
		decodeResponse(t, h.get("/api/review/queue?type=proposal&status=all"), &typed)
		if typed.Total != 1 || typed.Items[0].Subject.ID != h.proposal.ID {
			t.Fatalf("typed queue = %+v", typed)
		}
		var history historyResult
		decodeResponse(t, h.get("/api/review/history?type=proposal&id="+h.proposal.ID), &history)
		if history.Status != string(frontier.ReviewNew) || len(history.Decisions) != 0 {
			t.Fatalf("history = %+v", history)
		}
	})

	t.Run("reality inbox carries the ranking terms, answers and plans", func(t *testing.T) {
		var inbox inboxResult
		decodeResponse(t, h.get("/api/reality/inbox"), &inbox)
		if inbox.Total != 2 {
			t.Fatalf("inbox = %+v", inbox)
		}
		var planned QuestionSummary
		for _, item := range inbox.Items {
			if len(item.Plans) > 0 {
				planned = item
			}
			if len(item.Terms) == 0 {
				t.Errorf("question has no ranking arithmetic: %+v", item)
			}
		}
		if len(planned.Answers) != 1 || planned.Answers[0].Author != operatorID {
			t.Fatalf("answers = %+v", planned.Answers)
		}
		if len(planned.Plans) != 1 || planned.Plans[0].ID != h.plan.ID ||
			planned.Plans[0].State != string(reality.PlanProposed) ||
			len(planned.Plans[0].Actions) != 1 {
			t.Fatalf("plans = %+v", planned.Plans)
		}
	})

	t.Run("reality entity", func(t *testing.T) {
		var got entityDetail
		decodeResponse(t, h.get("/api/reality/entity?id="+h.entity.ID), &got)
		if got.Entity.ID != h.entity.ID || got.Entity.DisplayName == "" {
			t.Fatalf("entity = %+v", got.Entity)
		}
		if len(got.Aliases) != 1 || got.Aliases[0].Value == "" {
			t.Fatalf("aliases = %+v", got.Aliases)
		}
		if len(got.Relationships) != 1 || got.Relationships[0].To.DisplayName == "" {
			t.Fatalf("relationships = %+v", got.Relationships)
		}
	})

	t.Run("export renders both formats", func(t *testing.T) {
		jsonResponse := h.get("/api/export?type=proposal&id=" + h.proposal.ID + "&format=json")
		var document map[string]any
		decodeResponse(t, jsonResponse, &document)
		if document["notice"] != review.Notice || document["kind"] != "proposal" {
			t.Fatalf("export = %v", document)
		}
		markdown := h.get("/api/export?type=proposal&id=" + h.proposal.ID + "&format=markdown")
		if got := markdown.Header.Get("Content-Type"); got != "text/markdown; charset=utf-8" {
			t.Errorf("markdown Content-Type = %q", got)
		}
		if got := markdown.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("markdown nosniff = %q", got)
		}
		if text := body(t, markdown); !strings.Contains(text, review.Notice) {
			t.Errorf("markdown export lost its notice: %q", text)
		}
		for _, bad := range []string{"?type=nonsense&id=x", "?type=proposal&id=" + h.proposal.ID + "&format=pdf"} {
			response := h.get("/api/export" + bad)
			response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Errorf("export%s status = %d, want 400", bad, response.StatusCode)
			}
		}
	})

	t.Run("search bounds its page and refuses an unknown kind", func(t *testing.T) {
		var hits searchResult
		decodeResponse(t, h.get("/api/search?q=verification"), &hits)
		if hits.Hits == nil {
			t.Fatal("search returned a null hit list")
		}
		for _, bad := range []string{"?kind=nonsense", "?limit=0", "?limit=501", "?offset=-1"} {
			response := h.get("/api/search" + bad)
			response.Body.Close()
			if response.StatusCode != http.StatusBadRequest {
				t.Errorf("search%s status = %d, want 400", bad, response.StatusCode)
			}
		}
	})
}

// TestPhaseBRoutesAnswerHonestlyWithoutServices pins the unconfigured case: a
// build with no analysis state reports that rather than failing as though
// something broke, and reports it as a conflict like the archive routes do.
func TestPhaseBRoutesAnswerHonestlyWithoutServices(t *testing.T) {
	s, httpServer := testServer(t, Options{Operator: operatorID})
	for _, route := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/hypotheses", ""},
		{http.MethodGet, "/api/hypothesis?id=hyp-1", ""},
		{http.MethodGet, "/api/findings", ""},
		{http.MethodGet, "/api/finding?id=fnd-1", ""},
		{http.MethodGet, "/api/review/queue", ""},
		{http.MethodGet, "/api/review/history?type=proposal&id=prp-1", ""},
		{http.MethodGet, "/api/export?type=proposal&id=prp-1", ""},
		{http.MethodGet, "/api/reality/inbox", ""},
		{http.MethodGet, "/api/reality/entity?id=ent-1", ""},
		{http.MethodGet, "/api/search?q=x", ""},
		{http.MethodPost, "/api/review/decide", `{"subject":{"type":"proposal","id":"prp-1"},"disposition":"accept"}`},
		{http.MethodPost, "/api/review/context", `{"text":"x"}`},
		{http.MethodPost, "/api/reality/answer", `{"questionId":"qst-1","text":"x"}`},
		{http.MethodPost, "/api/reality/plan/accept", `{"planId":"pln-1"}`},
		{http.MethodGet, "/api/record/revisions?type=hypothesis&id=hyp-1", ""},
		{http.MethodGet, "/api/record/dispositions?type=hypothesis&id=hyp-1", ""},
		{http.MethodPost, "/api/record/disposition/decide", `{"dispositionId":"dsp-1","ruling":"accepted","headId":"hyp-1"}`},
		{http.MethodPost, "/api/record/invite", `{"record":{"type":"hypothesis","id":"hyp-1"},"headId":"hyp-1"}`},
		{http.MethodPost, "/api/record/revive", `{"record":{"type":"hypothesis","id":"hyp-1"},"reason":"x","headId":"hyp-1"}`},
	} {
		var reader io.Reader
		if route.body != "" {
			reader = strings.NewReader(route.body)
		}
		req, err := http.NewRequest(route.method, httpServer.URL+route.path, reader)
		if err != nil {
			t.Fatal(err)
		}
		authorize(req, bootstrapSession(t, s, httpServer))
		response, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		text := body(t, response)
		if response.StatusCode != http.StatusConflict {
			t.Errorf("%s %s status = %d body %q, want 409", route.method, route.path, response.StatusCode, text)
		}
	}
	// The analysis state route is the one that must still answer, because it
	// is how a page learns there is nothing to show.
	var state analysisState
	decodeResponse(t, request(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/api/analysis/state", bootstrapSession(t, s, httpServer)), &state)
	if state.Configured || state.Worker.Available || len(state.Runs) != 0 || len(state.Cookbook) != 0 {
		t.Fatalf("unconfigured state = %+v", state)
	}
}

// TestPhaseBGetsLeaveDurableStateUnchanged compares the whole durable state
// before and after every read route runs. A GET that wrote would break HTTP
// semantics and the audit story at once: §4.7's log is append-only, so a read
// that appended could never be taken back.
func TestPhaseBGetsLeaveDurableStateUnchanged(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	before := h.snapshot()
	for _, route := range phaseBRoutes(h) {
		if route.mutating {
			continue
		}
		response := h.request(route)
		response.Body.Close()
	}
	if after := h.snapshot(); after != before {
		t.Fatalf("durable state changed across reads:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// snapshot renders every durable fact these tests can reach as text. It is read
// through the services rather than off the filesystem so that it describes the
// records rather than the storage: a snapshot of the database file would also
// change when SQLite checkpointed a write-ahead log that no route wrote to.
func (h *phaseB) snapshot() string {
	h.t.Helper()
	var out strings.Builder
	items, err := h.review.Queue(h.ctx, review.QueueFilter{AllStatuses: true, Limit: 1000})
	if err != nil {
		h.t.Fatalf("Queue: %v", err)
	}
	for _, item := range items {
		fmt.Fprintf(&out, "queue %s/%s status=%s decisions=%d refinements=%d\n",
			item.Subject.Type, item.Subject.ID, item.Status, item.Decisions, item.Refinements)
		history, err := h.review.History(h.ctx, item.Subject)
		if err != nil {
			h.t.Fatalf("History: %v", err)
		}
		for _, decision := range history.Decisions {
			fmt.Fprintf(&out, "  decision %s %s by=%s\n",
				decision.Event.ID, decision.Event.Disposition, decision.Event.ReviewerID)
		}
	}
	// #87's ledgers and the lifecycle history a revive appends to. All three
	// are here because all three are now writable from this surface, and a
	// snapshot that did not describe them could not tell a refused mutation
	// from a successful one.
	actions, _, err := h.actions.List(h.ctx, disposition.ListFilter{Limit: disposition.MaxListLimit})
	if err != nil {
		h.t.Fatalf("List: %v", err)
	}
	for _, action := range actions {
		fmt.Fprintf(&out, "action %s %s/%s kind=%s status=%s\n",
			action.ID, action.Record.Type, action.Record.ID, action.Kind, action.Status)
		ledger, err := h.actions.Ledger(h.ctx, action.ID)
		if err != nil {
			h.t.Fatalf("Ledger: %v", err)
		}
		for _, entry := range ledger {
			fmt.Fprintf(&out, "  ruling %s %s by=%s\n", entry.ID, entry.Ruling, entry.By)
		}
	}
	queue, err := h.actions.Invitations(h.ctx, disposition.InvitationFilter{All: true})
	if err != nil {
		h.t.Fatalf("Invitations: %v", err)
	}
	for _, invitation := range queue {
		fmt.Fprintf(&out, "invitation %s %s/%s by=%s open=%t\n", invitation.ID,
			invitation.Record.Type, invitation.Record.ID, invitation.By, invitation.Open())
	}
	for _, id := range []string{h.hypothesis.ID, h.resting.ID} {
		history, err := h.front.StatusHistory(h.ctx, id)
		if err != nil {
			h.t.Fatalf("StatusHistory: %v", err)
		}
		for _, event := range history {
			fmt.Fprintf(&out, "status %s %s seq=%d actor=%s/%s\n", id, event.Status,
				event.Sequence, event.Actor.Kind, event.Actor.ID)
		}
	}
	inbox, err := h.reality.Inbox(h.ctx, reality.InboxQuery{})
	if err != nil {
		h.t.Fatalf("Inbox: %v", err)
	}
	for _, item := range inbox {
		fmt.Fprintf(&out, "question %s state=%s\n", item.Question.ID, item.Question.State)
		answers, err := h.reality.Answers(h.ctx, item.Question.ID)
		if err != nil {
			h.t.Fatalf("Answers: %v", err)
		}
		for _, answer := range answers {
			fmt.Fprintf(&out, "  answer %s author=%s outcome=%s\n", answer.ID, answer.Author, answer.Outcome)
		}
	}
	plan, err := h.reality.Plan(h.ctx, h.plan.ID)
	if err != nil {
		h.t.Fatalf("Plan: %v", err)
	}
	fmt.Fprintf(&out, "plan %s state=%s\n", plan.ID, plan.State)
	facts, err := h.reality.Facts(h.ctx, reality.FactQuery{SubjectID: h.entity.ID})
	if err != nil {
		h.t.Fatalf("Facts: %v", err)
	}
	for _, fact := range facts {
		fmt.Fprintf(&out, "fact %s %s=%v status=%s authority=%s/%s\n",
			fact.ID, fact.Predicate, fact.Value.Enum, fact.Status, fact.Authority.Kind, fact.Authority.ID)
	}
	return out.String()
}

// runLister is a wired listing of run receipts. internal/run exposes no receipt
// enumeration, so this is what the RunLister interface exists for.
type runLister []RunSummary

func (l runLister) Runs(_ context.Context, limit, offset int) ([]RunSummary, int, error) {
	start := min(offset, len(l))
	end := min(start+limit, len(l))
	return l[start:end], len(l), nil
}

// malicious is one fixture value carrying every hostile shape §3 and §10 name:
// HTML, a script tag, a scriptable URL, a data URI, a terminal control
// sequence, and a bidirectional override.
const malicious = "<script>alert(1)</script> javascript:alert(2) " +
	"data:text/html;base64,PHNjcmlwdD5hbGVydCgzKTwvc2NyaXB0Pg== " +
	"\x1b[31mred\x1b[0m \u202egnirts desrever\u202c \u200bzero\u200b"

// TestPhaseBNeutralizesMaliciousContent runs the whole read surface over
// fixtures whose every text field is hostile, and checks the wire bytes rather
// than a decoded value: the question is what a browser receives.
//
// Two mechanisms do the work, both Phase A's. Every string is escaped by the
// same sanitizer session data passes through, so a control sequence or a
// bidirectional override arrives as a printable escape and cannot move a
// terminal's cursor or reverse a reviewer's reading of a claim. And every
// response is JSON with HTML escaping on, so a script tag arrives as \u003c and
// no response body contains a tag a sniffing browser could execute.
func TestPhaseBNeutralizesMaliciousContent(t *testing.T) {
	h := newPhaseB(t, malicious, nil)
	// The mutation routes are driven first so the read routes have hostile
	// operator-supplied text as well as hostile model-supplied text: a
	// reviewer's note and a Reality answer are untrusted for the same reason
	// a transcript is.
	var context contextResult
	decodeResponse(t, h.post("/api/review/context", `{"text":`+mustJSON(t, malicious)+`}`), &context)
	decision := h.post("/api/review/decide", `{"subject":{"type":"proposal","id":"`+h.proposal.ID+
		`"},"disposition":"defer","contextId":"`+context.ID+`","note":`+mustJSON(t, malicious)+`}`)
	decision.Body.Close()
	answered := h.post("/api/reality/answer",
		`{"questionId":"`+h.question.ID+`","text":`+mustJSON(t, malicious)+`}`)
	answered.Body.Close()

	for _, route := range phaseBRoutes(h) {
		if route.mutating {
			continue
		}
		t.Run(route.name, func(t *testing.T) {
			response := h.request(route)
			text := body(t, response)
			// No response carries a terminal control sequence, a
			// bidirectional override, or a zero-width character, whatever
			// the fixture put in the record.
			for _, forbidden := range []string{"\x1b", "\u202e", "\u202c", "\u200b"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("response carries %q unescaped: %s", forbidden, text)
				}
			}
			if route.name == "export markdown" {
				// The export is not JSON, so its neutralization is
				// internal/review's own: every untrusted value is
				// rendered as inert Markdown, with HTML-significant
				// characters as entities and control or bidi runes as a
				// visible escape. What this route adds is a response that
				// cannot be mistaken for a page.
				if got := response.Header.Get("X-Content-Type-Options"); got != "nosniff" {
					t.Errorf("markdown nosniff = %q", got)
				}
				if got := response.Header.Get("Content-Type"); got != "text/markdown; charset=utf-8" {
					t.Errorf("markdown Content-Type = %q", got)
				}
				if !strings.HasPrefix(response.Header.Get("Content-Disposition"), "attachment") {
					t.Errorf("markdown Content-Disposition = %q", response.Header.Get("Content-Disposition"))
				}
				if !strings.Contains(text, `\u{1B}`) || !strings.Contains(text, `\u{202E}`) {
					t.Errorf("markdown export did not escape control or bidi characters: %s", text)
				}
				if strings.Contains(text, "<script") {
					t.Errorf("markdown export carries a raw tag: %s", text)
				}
				// The document is still a document: escaping must not have
				// flattened its lines into one.
				if strings.Count(text, "\n") < 5 {
					t.Errorf("markdown export lost its line structure: %s", text)
				}
				return
			}
			// A JSON response carries no executable tag at all: the
			// encoder escapes HTML-significant characters, so a browser
			// that mis-sniffed the body would still find no tag in it.
			for _, forbidden := range []string{"<script", "</script"} {
				if strings.Contains(text, forbidden) {
					t.Errorf("response carries %q unescaped: %s", forbidden, text)
				}
			}
			// The escaped forms are present instead, so the values were
			// neutralized rather than dropped: a field silently emptied
			// would pass every assertion above and lose the evidence.
			if !strings.Contains(text, `\u003cscript`) {
				t.Errorf("response does not carry the escaped script tag: %s", text)
			}
			if !strings.Contains(text, `\\u{1B}`) || !strings.Contains(text, `\\u{202E}`) {
				t.Errorf("response did not escape control or bidi characters: %s", text)
			}
		})
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// TestPhaseBPaginationBoundsALargeResult writes more records than any page will
// carry and walks them. The bound is the point: a corpus of thousands must not
// arrive whole, and a client that wants the rest asks for the next page.
func TestPhaseBPaginationBoundsALargeResult(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	const extra = 120
	for i := range extra {
		record, err := h.front.CreateHypothesis(h.ctx, frontier.HypothesisInput{
			RunID:   "run-2",
			Payload: frontier.HypothesisPayload{Statement: fmt.Sprintf("candidate %03d", i), Novelty: 0.1, Priority: 0.1},
		})
		if err != nil {
			t.Fatalf("CreateHypothesis: %v", err)
		}
		if _, err := h.review.Enroll(h.ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: record.ID}); err != nil {
			t.Fatalf("Enroll: %v", err)
		}
	}
	total := extra + 3

	// The default page is bounded even though the caller named no limit.
	var first hypothesisList
	decodeResponse(t, h.get("/api/hypotheses"), &first)
	if first.Total != total || len(first.Items) != defaultPageLimit {
		t.Fatalf("default page: total %d items %d", first.Total, len(first.Items))
	}

	// Every record is reachable by paging, and no record appears twice.
	seen := map[string]int{}
	for offset := 0; offset < total; offset += 40 {
		var page hypothesisList
		decodeResponse(t, h.get(fmt.Sprintf("/api/hypotheses?limit=40&offset=%d", offset)), &page)
		if page.Total != total {
			t.Fatalf("offset %d: total %d, want %d", offset, page.Total, total)
		}
		for _, item := range page.Items {
			seen[item.ID]++
		}
	}
	if len(seen) != total {
		t.Fatalf("paging saw %d distinct records, want %d", len(seen), total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("%s appeared %d times across pages", id, count)
		}
	}

	// A filtered listing pages over the filtered set, not over the raw one.
	var filtered hypothesisList
	decodeResponse(t, h.get("/api/hypotheses?status=untriaged&limit=10&offset=115"), &filtered)
	if filtered.Total != total || len(filtered.Items) != total-115 {
		t.Fatalf("filtered page: total %d items %d", filtered.Total, len(filtered.Items))
	}

	// An offset past the end is an empty page rather than an error: records
	// are added while an operator reads.
	var beyond hypothesisList
	decodeResponse(t, h.get("/api/hypotheses?offset=10000"), &beyond)
	if beyond.Total != total || len(beyond.Items) != 0 {
		t.Fatalf("page past the end = %+v", beyond)
	}

	// A page larger than the maximum is refused rather than clamped, so a
	// client cannot mistake a truncated view for the whole set.
	for _, bad := range []string{"?limit=0", "?limit=201", "?offset=-1", "?limit=abc"} {
		response := h.get("/api/hypotheses" + bad)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("/api/hypotheses%s status = %d, want 400", bad, response.StatusCode)
		}
	}

	// The other list routes bound their pages from the same helper.
	for _, path := range []string{
		"/api/findings?limit=201",
		"/api/review/queue?status=all&limit=201",
		"/api/reality/inbox?limit=201",
		"/api/analysis/state?limit=201",
	} {
		response := h.get(path)
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, response.StatusCode)
		}
	}
}

// TestPhaseBQueueAndInboxPaginate walks the two routes whose pages the contract
// did not originally bound.
func TestPhaseBQueueAndInboxPaginate(t *testing.T) {
	h := newPhaseB(t, "plain", nil)
	var queue queueResult
	decodeResponse(t, h.get("/api/review/queue?status=all&limit=2&offset=1"), &queue)
	if queue.Total != 4 || len(queue.Items) != 2 {
		t.Fatalf("queue page = %+v", queue)
	}
	var inbox inboxResult
	decodeResponse(t, h.get("/api/reality/inbox?limit=1"), &inbox)
	if inbox.Total != 2 || len(inbox.Items) != 1 {
		t.Fatalf("inbox page = %+v", inbox)
	}
}
