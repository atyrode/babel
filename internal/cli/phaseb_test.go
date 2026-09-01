package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	cookbookassets "github.com/atyrode/babel/cookbook"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/review"
	runstore "github.com/atyrode/babel/internal/run"
)

// The Phase B command surface: preparation, exploration, the frontier, the
// review log, export, Reality, the cookbook, and the Code profile launcher.
//
// The durable records these commands read are seeded through the service
// packages rather than produced by a run, so a case can plant an exact
// hostile value and assert exactly what the command does with it. The whole
// path from a real binary invocation through a real exploration is
// test/e2e's job; this suite is about the command surface.

// hostileStatement is the presentation-attack fixture Phase B records carry:
// an SGR sequence, an OSC introducer, a raw C1 CSI, a bidi override, and a
// newline that would otherwise let a value forge a second output line. A
// model or a transcript is free to produce every byte of it (SPEC.md §3).
const hostileStatement = "\x1b[31mred\x1b[0m \x1b]0;retitled\x07 \x9b2J \u202egnitirw-thgir\u202c\nforged: line"

// hostilePath is an evidence locator path long enough to push a table's
// other columns off screen, which is a presentation attack even once its
// control characters are escaped.
var hostilePath = "/synthetic/" + strings.Repeat("very-long-path-component/", 30) + "session.jsonl"

// seeded is the durable Phase B state one test planted, with the
// identifiers a command is then given.
type seeded struct {
	hypothesis  string
	deferred    string
	rejected    string
	observation string
	finding     string
	proposal    string
	entity      string
	question    string
	plan        string
}

// seed plants one candidate developed all the way to a proposal, plus a
// second candidate left on the frontier, and the Reality records the reality
// commands read. Every free-text field carries the hostile fixture, so no
// rendering test has to plant it again.
func (f *fixture) seed() seeded {
	f.t.Helper()
	ctx := context.Background()

	front, err := frontier.Open(f.dataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	defer front.Close()
	runs, err := runstore.Open(f.dataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	defer runs.Close()
	svc, err := review.Open(f.dataDir, front, runs)
	if err != nil {
		f.t.Fatal(err)
	}
	defer svc.Close()

	evidence, err := frontier.NewEvidence(event.Locator{
		Path:       hostilePath,
		Line:       12,
		ByteOffset: 4096,
		Digest:     strings.Repeat("ab", 32),
	}, "cited record "+hostileStatement)
	if err != nil {
		f.t.Fatal(err)
	}

	hypothesis, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID: "run-seed",
		Payload: frontier.HypothesisPayload{
			Statement:         "candidate " + hostileStatement,
			OriginCues:        []string{"cue " + hostileStatement},
			ProvisionalLabels: []string{"label"},
			Novelty:           0.4,
			Priority:          0.9,
			Notes:             "notes " + hostileStatement,
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	deferred, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "run-seed",
		Payload: frontier.HypothesisPayload{Statement: "the deferred candidate", Priority: 0.1},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := front.DeferFrontier(ctx, "run-seed", []string{deferred.ID}, "budget"); err != nil {
		f.t.Fatal(err)
	}
	// A candidate that is neither on the unexplored frontier nor enrolled
	// for review. §5.2 keeps every candidate, so a listing that could not
	// reach this one would make the guarantee true in storage and invisible
	// in practice.
	rejected, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "run-seed",
		Status:  frontier.StatusRejected,
		Payload: frontier.HypothesisPayload{Statement: "the rejected candidate", Priority: 0.2},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := front.Link(ctx, frontier.LinkInput{
		FromID: hypothesis.ID, ToID: deferred.ID,
		Type: frontier.LinkContradicts, Note: "link " + hostileStatement,
	}); err != nil {
		f.t.Fatal(err)
	}

	observation, err := front.CreateObservation(ctx, frontier.ObservationInput{
		HypothesisID:  hypothesis.ID,
		RunID:         "run-seed",
		RecipeID:      "outcome-integrity",
		RecipeVersion: 1,
		Payload: frontier.ObservationPayload{
			Claim:                 "claim " + hostileStatement,
			Category:              "outcome",
			Confidence:            frontier.ConfidenceModerate,
			Impact:                frontier.ImpactModerate,
			Evidence:              []frontier.Evidence{evidence},
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := front.SetStatus(ctx, frontier.StatusInput{
		HypothesisID: hypothesis.ID, Status: frontier.StatusPromoted, RunID: "run-seed",
		Note: "note " + hostileStatement,
	}); err != nil {
		f.t.Fatal(err)
	}
	finding, err := front.CreateFinding(ctx, frontier.FindingInput{
		RunID:          "run-seed",
		ObservationIDs: []string{observation.ID},
		Payload: frontier.FindingPayload{
			Title:                 "finding " + hostileStatement,
			Pattern:               "pattern " + hostileStatement,
			Significance:          "significance",
			Scope:                 []string{"scope " + hostileStatement},
			Recurrence:            2,
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	proposal, err := front.CreateProposal(ctx, frontier.ProposalInput{
		RunID:      "run-seed",
		FindingIDs: []string{finding.ID},
		Payload: frontier.ProposalPayload{
			Title:          "proposal " + hostileStatement,
			Problem:        "problem " + hostileStatement,
			Outcome:        "outcome",
			Impact:         frontier.ImpactModerate,
			Classification: frontier.ClassificationPrivate,
			Supporting:     []frontier.Evidence{evidence},
			Risks:          []string{"risk " + hostileStatement},
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	for _, ref := range []frontier.Ref{
		{Type: frontier.EntityHypothesis, ID: hypothesis.ID},
		{Type: frontier.EntityHypothesis, ID: deferred.ID},
		{Type: frontier.EntityFinding, ID: finding.ID},
		{Type: frontier.EntityProposal, ID: proposal.ID},
	} {
		if _, err := svc.Enroll(ctx, ref); err != nil {
			f.t.Fatal(err)
		}
	}

	out := seeded{
		hypothesis:  hypothesis.ID,
		deferred:    deferred.ID,
		rejected:    rejected.ID,
		observation: observation.ID,
		finding:     finding.ID,
		proposal:    proposal.ID,
	}
	out.entity, out.question, out.plan = f.seedReality()
	return out
}

// seedReality plants an entity with a fact, an open question, and an
// interpreter plan waiting for the acceptance §4.8 requires.
func (f *fixture) seedReality() (entityID, questionID, planID string) {
	f.t.Helper()
	ctx := context.Background()
	store, err := reality.Open(f.dataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	defer store.Close()

	entity, err := store.CreateEntity(ctx, reality.EntityInput{
		Kind:    reality.EntityProject,
		Payload: reality.EntityPayload{DisplayName: "project " + hostileStatement},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := store.AddAlias(ctx, reality.AliasInput{
		EntityID: entity.ID,
		Kind:     reality.AliasRepository,
		Payload:  reality.AliasPayload{Value: "repo " + hostileStatement},
	}); err != nil {
		f.t.Fatal(err)
	}
	observed := time.Now().UTC()
	if _, _, err := store.AssertFact(ctx, reality.FactInput{
		SubjectID: entity.ID,
		// local-path is text-valued, which is what lets the seeded fact
		// carry a hostile value at all: the enum predicates have closed
		// vocabularies and would refuse one.
		Predicate:   reality.PredicateLocalPath,
		Value:       reality.FactValue{Kind: reality.ValueText, Text: hostilePath + " " + hostileStatement},
		ValidFrom:   observed,
		ObservedAt:  observed,
		Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: "seed-operator", At: observed},
		Confidence:  reality.ConfidenceHigh,
		Sensitivity: reality.SensitivityRoutine,
		Note:        "note " + hostileStatement,
	}); err != nil {
		f.t.Fatal(err)
	}

	// An open question for `reality answer`, kept separate from the one the
	// plan belongs to: answering a question moves its state, and a fixture
	// that shared one would make the two commands' tests depend on order.
	open, err := store.Ask(ctx, reality.QuestionInput{
		Kind:              reality.KindAcquireContext,
		Class:             reality.ClassBlocking,
		Sensitivity:       reality.SensitivityRoutine,
		ExpectedAuthority: reality.AuthorityOperator,
		TargetEntityIDs:   []string{entity.ID},
		TargetPredicates:  []reality.Predicate{reality.PredicateLifecycle},
		MaterialEvidence:  []string{"seed"},
		Payload: reality.QuestionPayload{
			Prompt:   "prompt " + hostileStatement,
			WhyAsked: "why " + hostileStatement,
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}

	planned, err := store.Ask(ctx, reality.QuestionInput{
		Kind:              reality.KindRefreshStale,
		Class:             reality.ClassMaintenance,
		Sensitivity:       reality.SensitivityRoutine,
		ExpectedAuthority: reality.AuthorityOperator,
		TargetEntityIDs:   []string{entity.ID},
		TargetPredicates:  []reality.Predicate{reality.PredicateLifecycle},
		MaterialEvidence:  []string{"seed-plan"},
		Payload:           reality.QuestionPayload{Prompt: "is it still active?", WhyAsked: "the fact is stale"},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	answer, err := store.RecordAnswer(ctx, reality.AnswerInput{
		QuestionID: planned.ID,
		Author:     "seed-operator",
		At:         observed,
		Outcome:    reality.OutcomeAnswered,
		Text:       "it is dormant",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := store.BeginInterpretation(ctx, planned.ID); err != nil {
		f.t.Fatal(err)
	}
	plan, _, err := store.RecordPlan(ctx, reality.PlanInput{
		QuestionID:         planned.ID,
		AnswerID:           answer.ID,
		InterpreterVersion: 1,
		Summary:            "record dormancy",
		Kinds:              []reality.ActionKind{reality.ActionAssertFact},
		Actions: []reality.ActionPayload{{
			Rationale: "the operator said so",
			Fact: &reality.FactInput{
				SubjectID:   entity.ID,
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
		f.t.Fatal(err)
	}
	return entity.ID, open.ID, plan.ID
}

// mustStdout drives one successful invocation and returns its stdout, for
// the cases that only need the document.
func mustStdout(t *testing.T, f *fixture, args ...string) string {
	t.Helper()
	stdout, _ := f.ok(args...)
	return stdout
}

// decodeJSON parses a --json document, proving stdout carried exactly one.
// Unknown fields are rejected so a change to a machine-readable shape is
// caught here rather than by a script.
func decodeJSON[T any](t *testing.T, stdout string) T {
	t.Helper()
	var v T
	dec := json.NewDecoder(strings.NewReader(stdout))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not the expected JSON document: %v\nstdout:\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout carries more than one document:\n%s", stdout)
	}
	return v
}

// TestPrepareFixesAScopeAndRecordsItDurably covers §8's "prepare emits an
// immutable preparation/selection ID": the identity has to come back, and it
// has to name a record the store holds rather than a value printed once.
func TestPrepareFixesAScopeAndRecordsItDurably(t *testing.T) {
	f := newFixture(t)
	f.writeSession(sessionSpec{
		project: "-alpha", stem: "2026-01-02T03-04-05-000Z_" + testUUID(1),
		id: testUUID(1), title: hostileTitle, workspace: "/synthetic/alpha",
	})
	f.writeSession(sessionSpec{
		project: "-beta", stem: "2026-01-02T04-04-05-000Z_" + testUUID(2),
		id: testUUID(2), title: "beta", workspace: "/synthetic/beta",
	})

	stdout, stderr := f.ok("prepare", "--json")
	res := decodeJSON[prepareResult](t, stdout)
	if !strings.HasPrefix(res.PreparationID, "prep-") {
		t.Errorf("preparation id %q does not look like one", res.PreparationID)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("prepared %d sessions, want 2", len(res.Sessions))
	}
	for _, row := range res.Sessions {
		switch {
		case !strings.HasPrefix(row.CaptureDigest, "sha256:"):
			t.Errorf("session %s has capture digest %q", row.Selector, row.CaptureDigest)
		case !strings.HasPrefix(row.SourceDigest, "sha256:"):
			t.Errorf("session %s has source digest %q", row.Selector, row.SourceDigest)
		case row.CaptureDigest == row.SourceDigest:
			// The two digests answer different questions (§7): the file's
			// bytes and the normalized event stream derived from them.
			t.Errorf("session %s reports one digest for both the capture and the stream", row.Selector)
		case row.Events == 0:
			t.Errorf("session %s indexed no events", row.Selector)
		}
	}
	if res.IndexedEvents == 0 {
		t.Error("the preparation indexed no events, so a run would have nothing to search")
	}
	assertNoRawControls(t, "prepare --json", stdout, stderr)

	// Durable, not merely printed.
	runs, err := runstore.Open(f.dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer runs.Close()
	stored, err := runs.Preparation(context.Background(), runstore.PreparationID(res.PreparationID))
	if err != nil {
		t.Fatalf("the reported preparation is not in the store: %v", err)
	}
	if err := stored.Verify(); err != nil {
		t.Errorf("stored preparation does not match its own identity: %v", err)
	}
	if len(stored.Selection) != 2 {
		t.Errorf("stored selection holds %d sessions, want 2", len(stored.Selection))
	}
}

// TestPrepareRejectsAnUnmatchedSelector keeps a preparation an exact
// statement of intent: a selector that matches nothing must not silently
// yield a smaller scope.
func TestPrepareRejectsAnUnmatchedSelector(t *testing.T) {
	f := newFixture(t)
	f.writeSession(sessionSpec{
		project: "-alpha", stem: "2026-01-02T03-04-05-000Z_" + testUUID(1),
		id: testUUID(1), title: "alpha", workspace: "/synthetic/alpha",
	})
	stdout, _ := f.mustExit(exitFailure, "prepare", "omp/absent-session")
	if stdout != "" {
		t.Errorf("a rejected preparation wrote to stdout: %q", stdout)
	}
}

// TestExploreWithoutAWorkerFailsActionably is the message a correctly
// installed Babel produces today: Code does not implement the worker
// protocol yet, so this is the normal path and the text is the product.
func TestExploreWithoutAWorkerFailsActionably(t *testing.T) {
	f := newFixture(t)
	stdout, stderr := f.mustExit(exitFailure, "explore", "--preparation", "prep-whatever")

	if stdout != "" {
		t.Errorf("the failure wrote to stdout: %q", stdout)
	}
	for _, want := range []string{
		"no Code analysis worker is available",
		"babel analysis profile configure",
		"BABEL_ANALYSIS_WORKER",
		"Code does not implement this protocol yet",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the message does not mention %q:\n%s", want, stderr)
		}
	}
	// Nothing in it may read like a crash.
	for _, forbidden := range []string{"panic", "goroutine", "runtime error", "nil pointer", ".go:"} {
		if strings.Contains(stderr, forbidden) {
			t.Errorf("the message reads like a crash, it contains %q:\n%s", forbidden, stderr)
		}
	}
}

// TestExploreRejectsAnUnknownPreparation names the remedy rather than
// failing inside the store.
func TestExploreRejectsAnUnknownPreparation(t *testing.T) {
	f := newFixture(t)
	worker := filepath.Join(f.root, "worker")
	if err := os.WriteFile(worker, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, stderr := f.mustExit(exitFailure, "explore",
		"--preparation", "prep-absent", "--worker", worker, "--profile", "synthetic@1")
	if !strings.Contains(stderr, "babel prepare") {
		t.Errorf("the message does not name the remedy:\n%s", stderr)
	}
}

// TestExploreRefusesAStoredDial holds the exploration path to the same rule
// the ceremony enforces. A machine configured before #86 carries "--set" in
// its stored worker arguments; launching the worker with it attached produces
// Code's own refusal and then "exited without a result", which names neither
// the cause nor the remedy. The run must not start at all.
func TestExploreRefusesAStoredDial(t *testing.T) {
	f := newFixture(t)
	binary, record := ceremonyWorker(t, `{"profile":"dialled","revision":1}`, 0)
	if _, err := saveAnalysisSettings(analysisSettings{
		Worker:     binary,
		WorkerArgs: []string{"babel", "--set", "model=haiku"},
		Profile:    &profileRecord{ID: "agent-minted", Revision: 5, ConfiguredAt: "2026-08-30T00:00:00Z"},
	}); err != nil {
		t.Fatal(err)
	}
	before := settingsBytes(t)

	_, stderr := f.mustExit(exitUsage, "explore", "--preparation", "prep-whatever")

	for _, want := range []string{"stored worker arguments", "--worker"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not name the remedy %q:\n%s", want, stderr)
		}
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Error("the worker was launched with the stored override attached")
	}
	if got := settingsBytes(t); got != before {
		t.Error("the refusal rewrote the settings document")
	}
}

// TestPhaseBListingsAreScriptable holds every record-printing command to its
// machine-readable contract: exactly one document on stdout, no result data
// on stderr, and the fields a script needs to chain the next command.
func TestPhaseBListingsAreScriptable(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	t.Run("hypotheses", func(t *testing.T) {
		stdout, stderr := f.ok("hypotheses", "--json")
		res := decodeJSON[hypothesesResult](t, stdout)
		if res.Total != 3 {
			t.Fatalf("listed %d hypotheses, want all three seeded candidates", res.Total)
		}
		// Including the one that is neither unexplored nor enrolled: §5.2
		// keeps every candidate and the listing has to be able to reach it.
		var reachable bool
		for _, row := range res.Hypotheses {
			if row.ID == ids.rejected {
				reachable = true
			}
		}
		if !reachable {
			t.Errorf("the rejected, unenrolled candidate is unreachable from the listing: %+v", res.Hypotheses)
		}
		var promoted *hypothesisRow
		for i, row := range res.Hypotheses {
			if row.ID == ids.hypothesis {
				promoted = &res.Hypotheses[i]
			}
		}
		if promoted == nil {
			t.Fatalf("the seeded candidate is missing from %+v", res.Hypotheses)
		}
		if promoted.Status != string(frontier.StatusPromoted) {
			t.Errorf("status = %q, want promoted", promoted.Status)
		}
		if promoted.ReviewStatus != string(frontier.ReviewNew) {
			t.Errorf("review status = %q, want new", promoted.ReviewStatus)
		}
		if promoted.Priority != 0.9 {
			t.Errorf("priority = %v, want 0.9", promoted.Priority)
		}
		assertNoRawControls(t, "hypotheses --json", stdout, stderr)
	})

	t.Run("hypotheses --status", func(t *testing.T) {
		stdout, _ := f.ok("hypotheses", "--status", "deferred", "--json")
		res := decodeJSON[hypothesesResult](t, stdout)
		if res.Total != 1 || res.Hypotheses[0].ID != ids.deferred {
			t.Fatalf("--status deferred listed %+v, want only the deferred candidate", res.Hypotheses)
		}
	})

	t.Run("hypotheses paging", func(t *testing.T) {
		// The total is the whole matching set, not the page, which is what
		// lets a script page by arithmetic instead of by hitting the end.
		first := decodeJSON[hypothesesResult](t, mustStdout(t, f, "hypotheses", "--limit", "2", "--json"))
		if first.Total != 3 || len(first.Hypotheses) != 2 || first.Limit != 2 || first.Offset != 0 {
			t.Fatalf("first page = %+v, want two of three rows", first)
		}
		second := decodeJSON[hypothesesResult](t, mustStdout(t, f, "hypotheses", "--limit", "2", "--offset", "2", "--json"))
		if second.Total != 3 || len(second.Hypotheses) != 1 || second.Offset != 2 {
			t.Fatalf("second page = %+v, want the remaining row", second)
		}
		for _, row := range second.Hypotheses {
			for _, prior := range first.Hypotheses {
				if row.ID == prior.ID {
					t.Errorf("%s appears on both pages", row.ID)
				}
			}
		}
	})

	t.Run("hypothesis show", func(t *testing.T) {
		stdout, stderr := f.ok("hypothesis", "show", ids.hypothesis, "--json")
		res := decodeJSON[hypothesisResult](t, stdout)
		if res.Hypothesis.ID != ids.hypothesis {
			t.Fatalf("showed %q, want %q", res.Hypothesis.ID, ids.hypothesis)
		}
		if len(res.StatusHistory) < 2 {
			t.Errorf("status history holds %d events, want the creation and the promotion", len(res.StatusHistory))
		}
		if len(res.Observations) != 1 || res.Observations[0].ID != ids.observation {
			t.Fatalf("observations = %+v, want the seeded one", res.Observations)
		}
		if len(res.Observations[0].Evidence) != 1 {
			t.Fatalf("the observation carries %d citations, want 1", len(res.Observations[0].Evidence))
		}
		// §4.3: a claim's evidence has to be reopenable, so the whole
		// locator survives the rendering.
		cited := res.Observations[0].Evidence[0]
		if cited.Line != 12 || cited.ByteOffset != 4096 || cited.Digest == "" {
			t.Errorf("locator = %+v, want the seeded line, offset and digest", cited)
		}
		if len(res.LinksFrom) != 1 || res.LinksFrom[0].ToID != ids.deferred {
			t.Errorf("links from = %+v, want the contradiction to the deferred candidate", res.LinksFrom)
		}
		assertNoRawControls(t, "hypothesis show --json", stdout, stderr)
	})

	t.Run("findings", func(t *testing.T) {
		stdout, stderr := f.ok("findings", "--json")
		res := decodeJSON[findingsResult](t, stdout)
		if res.Total != 1 || res.Findings[0].ID != ids.finding {
			t.Fatalf("findings = %+v, want the seeded one", res.Findings)
		}
		if len(res.Findings[0].ObservationIDs) != 1 {
			t.Errorf("the finding names %d observations, want 1", len(res.Findings[0].ObservationIDs))
		}
		assertNoRawControls(t, "findings --json", stdout, stderr)
	})

	t.Run("finding show", func(t *testing.T) {
		stdout, stderr := f.ok("finding", "show", ids.finding, "--json")
		res := decodeJSON[findingResult](t, stdout)
		if res.Finding.ID != ids.finding {
			t.Fatalf("showed %q, want %q", res.Finding.ID, ids.finding)
		}
		if len(res.Observations) != 1 {
			t.Errorf("supporting observations = %d, want 1", len(res.Observations))
		}
		if len(res.Proposals) != 1 || res.Proposals[0].ID != ids.proposal {
			t.Errorf("proposals = %+v, want the one citing this finding", res.Proposals)
		}
		assertNoRawControls(t, "finding show --json", stdout, stderr)
	})

	t.Run("review queue", func(t *testing.T) {
		stdout, stderr := f.ok("review", "queue", "--json")
		res := decodeJSON[queueResult](t, stdout)
		// Four, not five: §6.7 makes an observation unreviewable, so a run
		// enrols its hypotheses, findings, and proposals and nothing else.
		if len(res.Items) != 4 {
			t.Fatalf("the queue holds %d rows, want the four enrolled records", len(res.Items))
		}
		for _, row := range res.Items {
			if row.Status != string(frontier.ReviewNew) {
				t.Errorf("%s is %q, want new", row.ID, row.Status)
			}
			if row.Title == "" {
				t.Errorf("%s has no title, so the queue cannot be triaged", row.ID)
			}
		}
		assertNoRawControls(t, "review queue --json", stdout, stderr)
	})

	t.Run("reality inbox", func(t *testing.T) {
		stdout, stderr := f.ok("reality", "inbox", "--json")
		res := decodeJSON[inboxResult](t, stdout)
		if len(res.Items) == 0 {
			t.Fatal("the inbox is empty, want the seeded question")
		}
		var found bool
		for _, item := range res.Items {
			if item.ID == ids.question {
				found = true
				if len(item.Terms) == 0 {
					t.Error("the ranked question carries no score terms, so its ranking cannot be argued with")
				}
			}
		}
		if !found {
			t.Errorf("the seeded question is missing from %+v", res.Items)
		}
		assertNoRawControls(t, "reality inbox --json", stdout, stderr)
	})

	t.Run("reality entity", func(t *testing.T) {
		stdout, stderr := f.ok("reality", "entity", ids.entity, "--json")
		res := decodeJSON[entityResult](t, stdout)
		if res.Entity.ID != ids.entity {
			t.Fatalf("showed %q, want %q", res.Entity.ID, ids.entity)
		}
		if len(res.Aliases) != 1 {
			t.Errorf("aliases = %+v, want the seeded repository alias", res.Aliases)
		}
		if len(res.Facts) != 1 {
			t.Errorf("facts = %+v, want the seeded ownership fact", res.Facts)
		}
		assertNoRawControls(t, "reality entity --json", stdout, stderr)
	})

	t.Run("cookbook list", func(t *testing.T) {
		stdout, stderr := f.ok("cookbook", "list", "--json")
		res := decodeJSON[cookbookListResult](t, stdout)
		if res.Total == 0 {
			t.Fatal("the embedded cookbook listed no recipes")
		}
		if res.Source != "embedded" {
			t.Errorf("source = %q, want embedded", res.Source)
		}
		for _, row := range res.Recipes {
			if row.Version < 1 || row.Digest == "" || len(row.Stages) == 0 {
				t.Errorf("recipe %+v is missing the fields a version check needs", row)
			}
		}
		assertNoRawControls(t, "cookbook list --json", stdout, stderr)
	})

	t.Run("analysis profile show", func(t *testing.T) {
		stdout, stderr := f.ok("analysis", "profile", "show", "--json")
		res := decodeJSON[profileResult](t, stdout)
		if res.Configured {
			t.Error("an unconfigured machine reported a stored profile")
		}
		// Decision 18 is stated in the machine-readable document too: a
		// caller that only reads JSON must not conclude Babel owns one.
		if res.Owner != profileOwner {
			t.Errorf("profile owner = %q, want %q", res.Owner, profileOwner)
		}
		assertNoRawControls(t, "analysis profile show --json", stdout, stderr)
	})
}

// TestReviewDecideRequiresAnOperatorIdentity is §4.7's attribution rule at
// the command surface: an unattributed disposition is refused rather than
// recorded against a placeholder.
func TestReviewDecideRequiresAnOperatorIdentity(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	stdout, stderr := f.mustExit(exitUsage, "review", "decide", ids.proposal, "--accept")
	if stdout != "" {
		t.Errorf("a refused decision wrote to stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "--operator") || !strings.Contains(stderr, "attributed") {
		t.Errorf("the refusal does not explain attribution:\n%s", stderr)
	}

	// And nothing was recorded.
	if status := f.reviewStatus(ids.proposal); status != frontier.ReviewNew {
		t.Fatalf("the refused decision changed the status to %q", status)
	}

	// With an identity it lands, and it lands durably.
	stdout, _ = f.ok("review", "decide", ids.proposal, "--accept",
		"--operator", "synthetic-operator", "--context", "context "+hostileStatement, "--json")
	res := decodeJSON[decideResult](t, stdout)
	if res.Decision.ReviewerID != "synthetic-operator" {
		t.Errorf("reviewer = %q, want the operator identity given", res.Decision.ReviewerID)
	}
	if res.Status != string(frontier.ReviewAccepted) {
		t.Errorf("status = %q, want accepted", res.Status)
	}
	if res.Decision.ContextID == "" {
		t.Error("the attributed guidance was not recorded")
	}
	if status := f.reviewStatus(ids.proposal); status != frontier.ReviewAccepted {
		t.Fatalf("durable status is %q, want accepted", status)
	}

	// The history is append-only: reconsidering appends rather than replaces.
	f.ok("review", "decide", ids.proposal, "--defer", "--operator", "synthetic-operator")
	historyOut, historyErr := f.ok("review", "history", ids.proposal, "--json")
	history := decodeJSON[historyResult](t, historyOut)
	if len(history.Decisions) != 2 {
		t.Fatalf("history holds %d decisions, want both", len(history.Decisions))
	}
	if history.Decisions[0].Disposition != "accept" || history.Decisions[1].Disposition != "defer" {
		t.Errorf("history = %+v, want the accept then the defer", history.Decisions)
	}
	if history.Decisions[0].ContextText == "" {
		t.Error("the first decision lost the guidance it cited")
	}
	assertNoRawControls(t, "review history --json", historyOut, historyErr)
}

// TestRealityMutationsRequireAnOperatorIdentity is §4.8's counterpart: an
// answer is authority-bearing provenance and an acceptance turns an
// interpretation into reality, so neither may be anonymous.
func TestRealityMutationsRequireAnOperatorIdentity(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	for _, args := range [][]string{
		{"reality", "answer", ids.question, "--text", "an answer"},
		{"reality", "accept", ids.plan},
	} {
		stdout, stderr := f.mustExit(exitUsage, args...)
		if stdout != "" {
			t.Errorf("babel %s wrote to stdout: %q", strings.Join(args, " "), stdout)
		}
		if !strings.Contains(stderr, "--operator") {
			t.Errorf("babel %s does not name --operator:\n%s", strings.Join(args, " "), stderr)
		}
	}

	stdout, _ := f.ok("reality", "answer", ids.question, "--text", "answer "+hostileStatement,
		"--operator", "synthetic-operator", "--json")
	answered := decodeJSON[answerResult](t, stdout)
	if answered.Author != "synthetic-operator" {
		t.Errorf("author = %q, want the operator identity given", answered.Author)
	}
	if answered.State != string(reality.QuestionAnsweredUninterpreted) {
		t.Errorf("question state = %q, want answered-uninterpreted: an answer is not yet authority",
			answered.State)
	}

	stdout, _ = f.ok("reality", "accept", ids.plan, "--operator", "synthetic-operator", "--json")
	accepted := decodeJSON[acceptResult](t, stdout)
	if len(accepted.FactIDs) != 1 {
		t.Fatalf("acceptance applied %d facts, want 1", len(accepted.FactIDs))
	}
	if accepted.QuestionState != string(reality.QuestionAnswered) {
		t.Errorf("question state = %q, want answered", accepted.QuestionState)
	}

	// Durable: the accepted plan's fact is on the entity.
	stdout, _ = f.ok("reality", "entity", ids.entity, "--predicate", "lifecycle", "--json")
	entity := decodeJSON[entityResult](t, stdout)
	if len(entity.Facts) != 1 || entity.Facts[0].ID != accepted.FactIDs[0] {
		t.Errorf("entity facts = %+v, want the fact the acceptance applied", entity.Facts)
	}
}

// TestExportWritesBothFormats covers §6.7's private view: the whole record
// with its provenance, rendered to a file or to stdout, and stopping there.
func TestExportWritesBothFormats(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()

	stdout, _ := f.ok("export", ids.proposal, "--format", "json")
	var doc review.Export
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("the JSON export does not decode: %v\n%s", err, stdout)
	}
	if doc.Proposal == nil || doc.Proposal.ID != ids.proposal {
		t.Fatalf("the export does not carry the proposal: %+v", doc)
	}
	if doc.Notice == "" {
		t.Error("the export dropped its fallibility notice")
	}
	if len(doc.Locators) == 0 {
		t.Error("the export collected no locators, so its claims cannot be reopened")
	}

	jsonPath := filepath.Join(f.root, "export.json")
	if _, stderr := f.ok("export", ids.finding, "--format", "json", "--output", jsonPath); !strings.Contains(stderr, "export.json") {
		t.Errorf("the write was not reported on stderr: %q", stderr)
	}
	written, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(written, &doc); err != nil {
		t.Fatalf("the written JSON export does not decode: %v", err)
	}

	markdownPath := filepath.Join(f.root, "export.md")
	f.ok("export", ids.finding, "--format", "markdown", "--output", markdownPath)
	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(markdown), "#") {
		t.Errorf("the Markdown export does not start with a heading:\n%s", markdown)
	}
	if !strings.Contains(string(markdown), ids.finding) {
		t.Error("the Markdown export does not name the record it renders")
	}

	// Both files are private: an export carries the whole record, including
	// locators into the corpus (SPEC.md §9).
	for _, path := range []string{jsonPath, markdownPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s is mode %O, want 600", filepath.Base(path), perm)
		}
	}
}

// TestExportRejectsAnUnaddressableIdentifier keeps the kind derived from the
// identifier rather than from a flag a caller can get wrong.
func TestExportRejectsAnUnaddressableIdentifier(t *testing.T) {
	f := newFixture(t)
	_, stderr := f.mustExit(exitUsage, "export", "not-an-id")
	if !strings.Contains(stderr, "hyp_") || !strings.Contains(stderr, "rcpt-") {
		t.Errorf("the refusal does not say what an identifier looks like:\n%s", stderr)
	}
}

// TestCookbookCheckDetectsAnAlteredBody is §5.1's versioning rule enforced
// from outside the build: a recipe whose guidance changed without its
// version increasing is drift, not a convention someone remembers.
func TestCookbookCheckDetectsAnAlteredBody(t *testing.T) {
	f := newFixture(t)
	tree := filepath.Join(f.root, "cookbook")
	copyCookbook(t, tree)

	if _, _, code := f.run("cookbook", "check", "--dir", tree); code != exitOK {
		t.Fatalf("an unaltered copy reported drift (exit %d)", code)
	}

	// One sentence appended to a body, the version left alone.
	target := filepath.Join(tree, "recipes", "outcome-integrity.md")
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, append(body, []byte("\nAn added sentence that changes the guidance.\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr := f.mustExit(exitFailure, "cookbook", "check", "--dir", tree, "--json")
	res := decodeJSON[cookbookCheckResult](t, stdout)
	if res.OK {
		t.Fatal("an altered body was reported as matching its version")
	}
	if len(res.Drift) != 1 || res.Drift[0].ID != "outcome-integrity" {
		t.Fatalf("drift = %+v, want exactly the altered recipe", res.Drift)
	}
	if res.Drift[0].Kind != "undeclared-change" {
		t.Errorf("drift kind = %q, want undeclared-change", res.Drift[0].Kind)
	}
	if !strings.Contains(stderr, "increase the version") {
		t.Errorf("the diagnostic does not name the remedy:\n%s", stderr)
	}
}

// TestPhaseBTerminalOutputEscapesHostileValues is §8's terminal-safety rule
// across the whole new surface. Every seeded record carries control
// sequences, a bidi override, an embedded newline, and an overlong path, and
// none of it may reach a terminal raw on either stream.
func TestPhaseBTerminalOutputEscapesHostileValues(t *testing.T) {
	f := newFixture(t)
	ids := f.seed()
	f.ok("review", "decide", ids.finding, "--reject", "--operator", "synthetic-operator",
		"--note", "note "+hostileStatement)

	invocations := [][]string{
		{"hypotheses"},
		{"hypotheses", "--json"},
		{"hypothesis", "show", ids.hypothesis},
		{"hypothesis", "show", ids.hypothesis, "--json"},
		{"findings"},
		{"findings", "--json"},
		{"finding", "show", ids.finding},
		{"finding", "show", ids.finding, "--json"},
		{"review", "queue"},
		{"review", "queue", "--json"},
		{"review", "history", ids.finding},
		{"review", "history", ids.finding, "--json"},
		{"reality", "inbox"},
		{"reality", "inbox", "--json"},
		{"reality", "entity", ids.entity},
		{"reality", "entity", ids.entity, "--json"},
		{"cookbook", "list"},
		{"analysis", "profile", "show"},
	}
	for _, args := range invocations {
		label := strings.Join(args, " ")
		t.Run(label, func(t *testing.T) {
			stdout, stderr := f.ok(args...)
			assertNoRawControls(t, label, stdout, stderr)
		})
	}
}

// assertNoRawControls fails when any escape-worthy byte survived into a
// stream. Newlines are layout the commands themselves write, so they are
// allowed between lines and forbidden inside a rendered value — which is
// what the forged-line fixture checks.
func assertNoRawControls(t *testing.T, label, stdout, stderr string) {
	t.Helper()
	for name, stream := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if !utf8.ValidString(stream) {
			t.Errorf("%s: %s is not valid UTF-8", label, name)
		}
		for _, r := range stream {
			if r == '\n' {
				continue
			}
			if unsafeRune(r) {
				t.Fatalf("%s: %s carries raw U+%04X:\n%s", label, name, r, stream)
			}
		}
		if strings.Contains(stream, "forged: line") && !strings.Contains(stream, `\u{A}forged: line`) {
			t.Errorf("%s: %s let a value forge its own line:\n%s", label, name, stream)
		}
	}
}

// reviewStatus reads one record's derived review status straight from the
// store, so a test asserts durable state rather than a command's own report.
func (f *fixture) reviewStatus(id string) frontier.ReviewStatus {
	f.t.Helper()
	front, err := frontier.Open(f.dataDir)
	if err != nil {
		f.t.Fatal(err)
	}
	defer front.Close()
	kind, ok := recordKinds[strings.SplitN(id, "_", 2)[0]]
	if !ok {
		f.t.Fatalf("%q does not name an analysis record", id)
	}
	status, err := front.ReviewStatus(context.Background(), frontier.Ref{Type: kind, ID: id})
	if err != nil {
		f.t.Fatal(err)
	}
	return status
}

// copyCookbook materializes the embedded asset tree on disk so a test can
// alter one recipe. The embedded tree is what a run applies and cannot be
// written to, which is exactly why `cookbook check --dir` exists.
func copyCookbook(t *testing.T, dst string) {
	t.Helper()
	assets := cookbookassets.Assets()
	err := fs.WalkDir(assets, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(path))
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := fs.ReadFile(assets, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// testUUID builds a synthetic session identity. Nothing in this suite
// derives from a real session (SPEC.md §10).
func testUUID(n int) string {
	return "00000000-0000-4000-8000-" + strings.Repeat("0", 11) + string(rune('0'+n))
}
