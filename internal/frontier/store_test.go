package frontier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/synth"
)

// openStore opens a frontier in a fresh temporary directory. It fails the test
// on a setup error rather than asserting anything about the store, so a
// failure here is unambiguously environmental.
func openStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close frontier: %v", err)
		}
	})
	return store
}

// syntheticLocator builds a locator with the shape internal/event produces,
// without naming anything real.
func syntheticLocator(line int) event.Locator {
	return event.Locator{
		Path:       fmt.Sprintf("synthetic/session-%02d.jsonl", line%7),
		Line:       line,
		ByteOffset: int64(line) * 512,
		Digest:     strings.Repeat(fmt.Sprintf("%x", line%16), 64),
	}
}

func mustEvidence(t *testing.T, line int, note string) Evidence {
	t.Helper()
	evidence, err := NewEvidence(syntheticLocator(line), note)
	if err != nil {
		t.Fatalf("build evidence: %v", err)
	}
	return evidence
}

func hypothesisPayload(statement string, priority float64) HypothesisPayload {
	return HypothesisPayload{
		Statement:         statement,
		OriginCues:        []string{"repeated correction"},
		ProvisionalLabels: []string{"coordination"},
		Novelty:           0.5,
		Priority:          priority,
	}
}

func observationPayload(claim string, evidence ...Evidence) ObservationPayload {
	return ObservationPayload{
		Claim:                 claim,
		Category:              "coordination",
		Confidence:            ConfidenceModerate,
		Impact:                ImpactModerate,
		Evidence:              evidence,
		CounterEvidenceAbsent: true,
	}
}

func findingPayload(title string) FindingPayload {
	return FindingPayload{
		Title:                 title,
		Pattern:               "the same handoff loses a constraint",
		Significance:          "rework",
		Scope:                 []string{"synthetic-project"},
		Recurrence:            2,
		CounterEvidenceAbsent: true,
	}
}

func proposalPayload(title string) ProposalPayload {
	return ProposalPayload{
		Title:          title,
		Problem:        "constraints are restated late",
		Outcome:        "state constraints in the handoff template",
		Impact:         ImpactModerate,
		Classification: ClassificationPrivate,
		Destinations:   []Destination{DestinationOperatorNote},
	}
}

// developPath walks the mandatory hypothesis -> observation -> finding ->
// proposal path once and returns each record, so tests about review and
// lineage do not each rebuild it.
func developPath(t *testing.T, store *Store) (Hypothesis, Observation, Finding, Proposal) {
	t.Helper()
	ctx := context.Background()
	hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("handoffs drop constraints", 0.7),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	observation, err := store.CreateObservation(ctx, ObservationInput{
		HypothesisID:  hypothesis.ID,
		RunID:         "run-1",
		RecipeID:      "human-agent-coordination",
		RecipeVersion: 1,
		Payload:       observationPayload("the constraint is restated after the change", mustEvidence(t, 12, "first")),
	})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}
	finding, err := store.CreateFinding(ctx, FindingInput{
		RunID:          "run-1",
		ObservationIDs: []string{observation.ID},
		Payload:        findingPayload("late constraints"),
	})
	if err != nil {
		t.Fatalf("create finding: %v", err)
	}
	proposal, err := store.CreateProposal(ctx, ProposalInput{
		RunID:      "run-1",
		FindingIDs: []string{finding.ID},
		Payload:    proposalPayload("state constraints up front"),
	})
	if err != nil {
		t.Fatalf("create proposal: %v", err)
	}
	return hypothesis, observation, finding, proposal
}

// TestObservationRequiresEvidence proves §4.3's rule that an observation
// cannot exist without evidence, at every layer that can enforce it: the
// evidence constructor, the record constructor, and the column constraint that
// still holds once §9 seals the payload.
func TestObservationRequiresEvidence(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:   "run-1",
		Payload: hypothesisPayload("an idea", 0.1),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}

	t.Run("constructor refuses zero locators", func(t *testing.T) {
		payload := observationPayload("a claim without provenance")
		payload.Evidence = nil
		_, err := store.CreateObservation(ctx, ObservationInput{
			HypothesisID: hypothesis.ID, RunID: "run-1", RecipeID: "lens", Payload: payload,
		})
		if !errors.Is(err, ErrNoEvidence) {
			t.Fatalf("evidence-free observation: got %v, want ErrNoEvidence", err)
		}
		observations, err := store.ObservationsFor(ctx, hypothesis.ID)
		if err != nil {
			t.Fatalf("read observations: %v", err)
		}
		if len(observations) != 0 {
			t.Fatalf("refused observation still stored %d rows", len(observations))
		}
	})

	t.Run("locator must recover its bytes", func(t *testing.T) {
		cases := []struct {
			name    string
			locator event.Locator
		}{
			{"no path", event.Locator{Digest: strings.Repeat("a", 64), Line: 1}},
			{"no digest", event.Locator{Path: "synthetic/s.jsonl", Line: 1}},
			{"negative line", event.Locator{Path: "synthetic/s.jsonl", Digest: "ab", Line: -1}},
			{"negative offset", event.Locator{Path: "synthetic/s.jsonl", Digest: "ab", ByteOffset: -1}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, err := NewEvidence(tc.locator, "note"); !errors.Is(err, ErrInvalidLocator) {
					t.Fatalf("got %v, want ErrInvalidLocator", err)
				}
			})
		}
	})

	t.Run("stored row refuses a zero evidence count", func(t *testing.T) {
		// The payload is what §9 encrypts, so the evidence rule would be
		// unenforceable remotely if it lived only inside it. This writes
		// around the constructor to prove the column constraint carries it.
		_, err := store.db.ExecContext(ctx, `INSERT INTO frontier_observation(
			id, ancestor_id, hypothesis_id, run_id, recipe_id, recipe_version,
			schema_version, evidence_count, created_at, payload_json)
			VALUES('obs_forged', NULL, ?, 'run-1', 'lens', 1, 1, 0, '2026-01-01T00:00:00Z', '{}')`,
			hypothesis.ID)
		if err == nil {
			t.Fatal("inserted an observation claiming zero evidence")
		}
		if !strings.Contains(err.Error(), "CHECK") && !strings.Contains(err.Error(), "constraint") {
			t.Fatalf("insert failed for the wrong reason: %v", err)
		}
	})
}

// TestCounterEvidenceMustBeStated proves §4.3 and §4.4's "explicit
// counter-evidence or absence thereof": an empty list is not a position.
func TestCounterEvidenceMustBeStated(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{RunID: "r", Payload: hypothesisPayload("idea", 0.1)})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}

	unstated := observationPayload("a claim", mustEvidence(t, 3, "note"))
	unstated.CounterEvidenceAbsent = false
	if _, err := store.CreateObservation(ctx, ObservationInput{
		HypothesisID: hypothesis.ID, RunID: "r", RecipeID: "lens", Payload: unstated,
	}); !errors.Is(err, ErrCounterEvidenceUnstated) {
		t.Fatalf("unstated counter-evidence: got %v, want ErrCounterEvidenceUnstated", err)
	}

	contradictory := observationPayload("a claim", mustEvidence(t, 3, "note"))
	contradictory.CounterEvidence = []Evidence{mustEvidence(t, 4, "against")}
	contradictory.CounterEvidenceAbsent = true
	if _, err := store.CreateObservation(ctx, ObservationInput{
		HypothesisID: hypothesis.ID, RunID: "r", RecipeID: "lens", Payload: contradictory,
	}); !errors.Is(err, ErrCounterEvidenceUnstated) {
		t.Fatalf("contradictory counter-evidence: got %v, want ErrCounterEvidenceUnstated", err)
	}
}

// TestDevelopmentPathIsMandatory proves §4.2's path: a finding cannot be
// created from a hypothesis that produced no observations, and a proposal
// cannot be created without a finding. Skipping a step is an error, not a
// warning.
func TestDevelopmentPathIsMandatory(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{RunID: "r", Payload: hypothesisPayload("idea", 0.4)})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}

	t.Run("finding from a hypothesis with no observations", func(t *testing.T) {
		observations, err := store.ObservationsFor(ctx, hypothesis.ID)
		if err != nil {
			t.Fatalf("read observations: %v", err)
		}
		if len(observations) != 0 {
			t.Fatalf("hypothesis unexpectedly has %d observations", len(observations))
		}
		ids := make([]string, 0, len(observations))
		for _, observation := range observations {
			ids = append(ids, observation.ID)
		}
		if _, err := store.CreateFinding(ctx, FindingInput{
			RunID: "r", ObservationIDs: ids, Payload: findingPayload("premature"),
		}); !errors.Is(err, ErrNoObservations) {
			t.Fatalf("finding without observations: got %v, want ErrNoObservations", err)
		}
	})

	t.Run("finding citing an observation that does not exist", func(t *testing.T) {
		if _, err := store.CreateFinding(ctx, FindingInput{
			RunID: "r", ObservationIDs: []string{"obs_absent"}, Payload: findingPayload("invented"),
		}); !errors.Is(err, ErrUnknownEntity) {
			t.Fatalf("finding on an absent observation: got %v, want ErrUnknownEntity", err)
		}
	})

	t.Run("proposal without a finding", func(t *testing.T) {
		if _, err := store.CreateProposal(ctx, ProposalInput{
			RunID: "r", Payload: proposalPayload("premature"),
		}); !errors.Is(err, ErrNoFindings) {
			t.Fatalf("proposal without findings: got %v, want ErrNoFindings", err)
		}
	})

	t.Run("proposal citing a finding that does not exist", func(t *testing.T) {
		if _, err := store.CreateProposal(ctx, ProposalInput{
			RunID: "r", FindingIDs: []string{"fnd_absent"}, Payload: proposalPayload("invented"),
		}); !errors.Is(err, ErrUnknownEntity) {
			t.Fatalf("proposal on an absent finding: got %v, want ErrUnknownEntity", err)
		}
	})

	t.Run("the complete path derives its own lineage", func(t *testing.T) {
		developed, observation, finding, proposal := developPath(t, openStore(t))
		if got := finding.HypothesisIDs; len(got) != 1 || got[0] != developed.ID {
			t.Fatalf("finding hypotheses = %v, want [%s]", got, developed.ID)
		}
		if got := proposal.HypothesisIDs; len(got) != 1 || got[0] != developed.ID {
			t.Fatalf("proposal hypotheses = %v, want [%s]", got, developed.ID)
		}
		if got := finding.ObservationIDs; len(got) != 1 || got[0] != observation.ID {
			t.Fatalf("finding observations = %v, want [%s]", got, observation.ID)
		}
		if proposal.ReviewStatus != ReviewNew {
			t.Fatalf("new proposal review status = %q, want %q", proposal.ReviewStatus, ReviewNew)
		}
	})
}

// TestNoDeletePathForHypothesis proves §5.2's "sorting never deletes" and
// §4.7's "rejection never deletes a record" two ways: the API offers no
// removal operation at all, and a rejected candidate is still readable with
// its full history afterwards.
func TestNoDeletePathForHypothesis(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)

	t.Run("api surface offers no removal", func(t *testing.T) {
		destructive := regexp.MustCompile(`(?i)^(delete|remove|purge|drop|erase|forget|prune|clear|reset|truncate)`)
		storeType := reflect.TypeOf(store)
		var offenders []string
		for i := range storeType.NumMethod() {
			name := storeType.Method(i).Name
			if destructive.MatchString(name) {
				offenders = append(offenders, name)
			}
		}
		if len(offenders) != 0 {
			t.Fatalf("frontier exposes removal methods %v", offenders)
		}
	})

	hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID: "run-1", Payload: hypothesisPayload("a candidate worth keeping", 0.3),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	if _, err := store.Decide(ctx, DispositionInput{
		Subject:     Ref{Type: EntityHypothesis, ID: hypothesis.ID},
		Disposition: DispositionReject,
		ReviewerID:  "operator",
		Note:        "not worth the budget",
	}); err != nil {
		t.Fatalf("reject hypothesis: %v", err)
	}
	if _, err := store.SetStatus(ctx, StatusInput{
		HypothesisID: hypothesis.ID, Status: StatusRejected, RunID: "run-1",
	}); err != nil {
		t.Fatalf("set rejected status: %v", err)
	}

	after, err := store.Hypothesis(ctx, hypothesis.ID)
	if err != nil {
		t.Fatalf("read rejected hypothesis: %v", err)
	}
	if after.Payload.Statement != hypothesis.Payload.Statement {
		t.Fatalf("rejected hypothesis wording changed: %q", after.Payload.Statement)
	}
	if after.Status != StatusRejected {
		t.Fatalf("status = %q, want %q", after.Status, StatusRejected)
	}
	history, err := store.StatusHistory(ctx, hypothesis.ID)
	if err != nil {
		t.Fatalf("read status history: %v", err)
	}
	if len(history) != 2 || history[0].Status != StatusUntriaged || history[1].Status != StatusRejected {
		t.Fatalf("status history = %v, want untriaged then rejected", history)
	}
	dispositions, err := store.DispositionHistory(ctx, Ref{Type: EntityHypothesis, ID: hypothesis.ID})
	if err != nil {
		t.Fatalf("read disposition history: %v", err)
	}
	if len(dispositions) != 1 || dispositions[0].Disposition != DispositionReject {
		t.Fatalf("disposition history = %v, want one reject", dispositions)
	}
}

// TestDispositionHistoryIsAppendOnly proves §4.7's append-only decisions: a
// record rejected and then reconsidered retains both events, in order.
func TestDispositionHistoryIsAppendOnly(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	_, _, _, proposal := developPath(t, store)
	subject := Ref{Type: EntityProposal, ID: proposal.ID}

	sequence := []Disposition{DispositionReject, DispositionDefer, DispositionAccept}
	for _, disposition := range sequence {
		if _, err := store.Decide(ctx, DispositionInput{
			Subject: subject, Disposition: disposition, ReviewerID: "operator",
		}); err != nil {
			t.Fatalf("record %s: %v", disposition, err)
		}
	}

	history, err := store.DispositionHistory(ctx, subject)
	if err != nil {
		t.Fatalf("read disposition history: %v", err)
	}
	if len(history) != len(sequence) {
		t.Fatalf("history has %d events, want %d", len(history), len(sequence))
	}
	for i, want := range sequence {
		if history[i].Disposition != want {
			t.Fatalf("history[%d] = %q, want %q", i, history[i].Disposition, want)
		}
		if history[i].Sequence != int64(i+1) {
			t.Fatalf("history[%d] sequence = %d, want %d", i, history[i].Sequence, i+1)
		}
	}

	reread, err := store.Proposal(ctx, proposal.ID)
	if err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if reread.ReviewStatus != ReviewAccepted {
		t.Fatalf("review status = %q, want %q after reconsideration", reread.ReviewStatus, ReviewAccepted)
	}
}

// TestDispositionVocabularyIsClosed proves §4.7's rule that there is no
// standalone `refine` disposition, and §6.7's rule that only hypotheses,
// findings, and proposals are reviewable.
func TestDispositionVocabularyIsClosed(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	hypothesis, observation, finding, proposal := developPath(t, store)

	cases := []struct {
		name    string
		input   DispositionInput
		wantErr error
	}{
		{
			name: "refine is not a disposition",
			input: DispositionInput{
				Subject:     Ref{Type: EntityProposal, ID: proposal.ID},
				Disposition: Disposition("refine"), ReviewerID: "operator",
			},
			wantErr: ErrInvalidValue,
		},
		{
			name: "observations are evidence, not review subjects",
			input: DispositionInput{
				Subject:     Ref{Type: EntityObservation, ID: observation.ID},
				Disposition: DispositionAccept, ReviewerID: "operator",
			},
			wantErr: ErrNotReviewable,
		},
		{
			name: "decisions are attributed",
			input: DispositionInput{
				Subject:     Ref{Type: EntityFinding, ID: finding.ID},
				Disposition: DispositionAccept,
			},
			wantErr: ErrInvalidValue,
		},
		{
			name: "duplicate names its original",
			input: DispositionInput{
				Subject:     Ref{Type: EntityHypothesis, ID: hypothesis.ID},
				Disposition: DispositionDuplicate, ReviewerID: "operator",
			},
			wantErr: ErrInvalidValue,
		},
		{
			name: "subject must exist",
			input: DispositionInput{
				Subject:     Ref{Type: EntityProposal, ID: "pro_absent"},
				Disposition: DispositionAccept, ReviewerID: "operator",
			},
			wantErr: ErrUnknownEntity,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := store.Decide(ctx, tc.input); !errors.Is(err, tc.wantErr) {
				t.Fatalf("got %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestRejectAndRefineIsAtomic proves §4.7's single atomic operation: the
// rejection and the authorized refinement request are created together, and an
// injected failure between them leaves neither.
func TestRejectAndRefineIsAtomic(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	_, _, _, proposal := developPath(t, store)
	subject := Ref{Type: EntityProposal, ID: proposal.ID}

	injected := errors.New("injected failure between the two writes")
	store.faultAfterReject = func() error { return injected }
	if _, _, err := store.RejectAndRefine(ctx,
		DispositionInput{Subject: subject, ReviewerID: "operator", Note: "too broad"},
		RefinementPayload{Guidance: "narrow it to one repository"},
	); !errors.Is(err, injected) {
		t.Fatalf("got %v, want the injected failure", err)
	}
	history, err := store.DispositionHistory(ctx, subject)
	if err != nil {
		t.Fatalf("read disposition history: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("failed reject-and-refine left %d disposition events", len(history))
	}
	requests, err := store.RefinementRequests(ctx, subject)
	if err != nil {
		t.Fatalf("read refinement requests: %v", err)
	}
	if len(requests) != 0 {
		t.Fatalf("failed reject-and-refine left %d refinement requests", len(requests))
	}
	if status, err := store.ReviewStatus(ctx, subject); err != nil || status != ReviewNew {
		t.Fatalf("review status = %q (err %v), want %q", status, err, ReviewNew)
	}

	store.faultAfterReject = nil
	rejection, request, err := store.RejectAndRefine(ctx,
		DispositionInput{Subject: subject, ReviewerID: "operator", Note: "too broad"},
		RefinementPayload{Guidance: "narrow it to one repository", Scope: []string{"one repository"}},
	)
	if err != nil {
		t.Fatalf("reject and refine: %v", err)
	}
	if rejection.Disposition != DispositionReject {
		t.Fatalf("disposition = %q, want %q", rejection.Disposition, DispositionReject)
	}
	if request.DispositionID != rejection.ID {
		t.Fatalf("refinement request authorized by %q, want %q", request.DispositionID, rejection.ID)
	}
	status, err := store.ReviewStatus(ctx, subject)
	if err != nil {
		t.Fatalf("read review status: %v", err)
	}
	if status != ReviewRefineRequested {
		t.Fatalf("review status = %q, want %q", status, ReviewRefineRequested)
	}
	requests, err = store.RefinementRequests(ctx, subject)
	if err != nil {
		t.Fatalf("read refinement requests: %v", err)
	}
	if len(requests) != 1 || requests[0].Payload.Guidance != "narrow it to one repository" {
		t.Fatalf("refinement requests = %v", requests)
	}

	t.Run("a refinement request needs guidance", func(t *testing.T) {
		if _, _, err := store.RejectAndRefine(ctx,
			DispositionInput{Subject: subject, ReviewerID: "operator"},
			RefinementPayload{},
		); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("got %v, want ErrInvalidValue", err)
		}
		history, err := store.DispositionHistory(ctx, subject)
		if err != nil {
			t.Fatalf("read disposition history: %v", err)
		}
		if len(history) != 1 {
			t.Fatalf("refused reject-and-refine left %d disposition events, want the 1 earlier one", len(history))
		}
	})
}

// TestLinksTraverseBothDirections proves §4.2's typed links are queryable from
// either end and that asserting one never changes a candidate's status —
// including `supersedes`, the link most likely to be mistaken for a lifecycle
// operation.
func TestLinksTraverseBothDirections(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	parent, err := store.CreateHypothesis(ctx, HypothesisInput{RunID: "r", Payload: hypothesisPayload("parent idea", 0.6)})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, err := store.CreateHypothesis(ctx, HypothesisInput{RunID: "r", Payload: hypothesisPayload("child idea", 0.9)})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	for _, linkType := range []LinkType{LinkDerivedFrom, LinkSupersedes, LinkContradicts, LinkCorroborates, LinkSameConcept} {
		if _, err := store.Link(ctx, LinkInput{FromID: child.ID, ToID: parent.ID, Type: linkType}); err != nil {
			t.Fatalf("link %s: %v", linkType, err)
		}
	}

	outgoing, err := store.LinksFrom(ctx, child.ID)
	if err != nil {
		t.Fatalf("read outgoing links: %v", err)
	}
	incoming, err := store.LinksTo(ctx, parent.ID)
	if err != nil {
		t.Fatalf("read incoming links: %v", err)
	}
	if len(outgoing) != 5 || len(incoming) != 5 {
		t.Fatalf("links: %d outgoing from the child, %d incoming to the parent, want 5 each", len(outgoing), len(incoming))
	}
	forward := map[string]LinkType{}
	for _, link := range outgoing {
		forward[link.ID] = link.Type
	}
	for _, link := range incoming {
		if forward[link.ID] != link.Type {
			t.Fatalf("link %s is %q from one side and %q from the other", link.ID, forward[link.ID], link.Type)
		}
		if link.FromID != child.ID || link.ToID != parent.ID {
			t.Fatalf("link %s endpoints = %s -> %s", link.ID, link.FromID, link.ToID)
		}
	}
	reverse, err := store.LinksFrom(ctx, parent.ID)
	if err != nil {
		t.Fatalf("read links from the parent: %v", err)
	}
	if len(reverse) != 0 {
		t.Fatalf("parent asserts %d links it never made", len(reverse))
	}

	for _, hypothesis := range []Hypothesis{parent, child} {
		current, err := store.Hypothesis(ctx, hypothesis.ID)
		if err != nil {
			t.Fatalf("read hypothesis: %v", err)
		}
		if current.Status != StatusUntriaged {
			t.Fatalf("linking changed %s status to %q", hypothesis.ID, current.Status)
		}
		history, err := store.StatusHistory(ctx, hypothesis.ID)
		if err != nil {
			t.Fatalf("read status history: %v", err)
		}
		if len(history) != 1 {
			t.Fatalf("linking appended %d status events to %s", len(history)-1, hypothesis.ID)
		}
	}

	t.Run("invalid links are refused", func(t *testing.T) {
		if _, err := store.Link(ctx, LinkInput{FromID: child.ID, ToID: parent.ID, Type: "inspires"}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("unknown link type: got %v, want ErrInvalidValue", err)
		}
		if _, err := store.Link(ctx, LinkInput{FromID: child.ID, ToID: child.ID, Type: LinkSameConcept}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("self link: got %v, want ErrInvalidValue", err)
		}
		if _, err := store.Link(ctx, LinkInput{FromID: child.ID, ToID: "hyp_absent", Type: LinkSameConcept}); !errors.Is(err, ErrUnknownEntity) {
			t.Fatalf("dangling link: got %v, want ErrUnknownEntity", err)
		}
	})
}

// TestDescendantLeavesAncestorByteIdentical proves §4.7's immutability: a
// revision creates a descendant linked to its ancestor, and the ancestor's
// stored row is not touched.
func TestDescendantLeavesAncestorByteIdentical(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	ancestor, observation, finding, proposal := developPath(t, store)

	before := rawRows(t, store)

	revisedHypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:      "run-2",
		AncestorID: ancestor.ID,
		Payload:    hypothesisPayload("handoffs drop constraints, narrowed to one repository", 0.8),
	})
	if err != nil {
		t.Fatalf("revise hypothesis: %v", err)
	}
	if _, err := store.CreateObservation(ctx, ObservationInput{
		HypothesisID: revisedHypothesis.ID, RunID: "run-2", RecipeID: "lens", RecipeVersion: 2,
		AncestorID: observation.ID,
		Payload:    observationPayload("restated later, with one counterexample", mustEvidence(t, 21, "second")),
	}); err != nil {
		t.Fatalf("revise observation: %v", err)
	}
	if _, err := store.CreateFinding(ctx, FindingInput{
		RunID: "run-2", AncestorID: finding.ID, ObservationIDs: []string{observation.ID},
		Payload: findingPayload("late constraints, narrowed"),
	}); err != nil {
		t.Fatalf("revise finding: %v", err)
	}
	if _, err := store.CreateProposal(ctx, ProposalInput{
		RunID: "run-2", AncestorID: proposal.ID, FindingIDs: []string{finding.ID},
		Payload: proposalPayload("state constraints up front, narrowed"),
	}); err != nil {
		t.Fatalf("revise proposal: %v", err)
	}

	after := rawRows(t, store)
	for key, want := range before {
		got, ok := after[key]
		if !ok {
			t.Fatalf("%s vanished after revision", key)
		}
		if got != want {
			t.Fatalf("%s changed:\n before %s\n  after %s", key, want, got)
		}
	}

	reread, err := store.Hypothesis(ctx, revisedHypothesis.ID)
	if err != nil {
		t.Fatalf("read descendant: %v", err)
	}
	if reread.AncestorID != ancestor.ID {
		t.Fatalf("descendant ancestor = %q, want %q", reread.AncestorID, ancestor.ID)
	}

	t.Run("a descendant needs a real ancestor", func(t *testing.T) {
		if _, err := store.CreateHypothesis(ctx, HypothesisInput{
			RunID: "run-2", AncestorID: "hyp_absent", Payload: hypothesisPayload("orphan", 0.1),
		}); !errors.Is(err, ErrUnknownEntity) {
			t.Fatalf("got %v, want ErrUnknownEntity", err)
		}
	})
}

// rawRows reads every frontier row as one string per row, keyed by table and
// id, so a revision's effect on existing rows can be compared byte for byte
// rather than through the accessors that might normalize a difference away.
func rawRows(t *testing.T, store *Store) map[string]string {
	t.Helper()
	rows := map[string]string{}
	for _, table := range frontierTables(t, store) {
		columns := tableColumns(t, store, table)
		selected := make([]string, len(columns))
		for i, column := range columns {
			selected[i] = "quote(" + column + ")"
		}
		query := fmt.Sprintf(`SELECT %s FROM %s`, strings.Join(selected, ", "), table)
		result, err := store.db.Query(query)
		if err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		func() {
			defer result.Close()
			for result.Next() {
				values := make([]string, len(columns))
				targets := make([]any, len(columns))
				for i := range values {
					targets[i] = &values[i]
				}
				if err := result.Scan(targets...); err != nil {
					t.Fatalf("scan %s: %v", table, err)
				}
				rows[table+"/"+values[0]] = strings.Join(values, "|")
			}
			if err := result.Err(); err != nil {
				t.Fatalf("read %s: %v", table, err)
			}
		}()
	}
	return rows
}

func frontierTables(t *testing.T, store *Store) []string {
	t.Helper()
	rows, err := store.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE 'frontier_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("list frontier tables: %v", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("list frontier tables: %v", err)
		}
		tables = append(tables, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("list frontier tables: %v", err)
	}
	return tables
}

func tableColumns(t *testing.T, store *Store, table string) []string {
	t.Helper()
	rows, err := store.db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("read %s columns: %v", table, err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	return columns
}

// TestDeferredFrontierSurvivesRestart proves §5.2's resumability: a finite run
// defers its unexplored remainder rather than erasing it, and the store still
// answers "what is still unexplored" after the process that wrote it is gone.
func TestDeferredFrontierSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}

	priorities := []float64{0.9, 0.5, 0.2, 0.7}
	created := make([]Hypothesis, 0, len(priorities))
	for i, priority := range priorities {
		hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{
			RunID:   "run-1",
			Payload: hypothesisPayload(fmt.Sprintf("candidate %d", i), priority),
		})
		if err != nil {
			t.Fatalf("create hypothesis %d: %v", i, err)
		}
		created = append(created, hypothesis)
	}

	// The run investigates the highest-priority candidate and promotes it,
	// then checkpoints the rest.
	investigated := created[0]
	if _, err := store.SetStatus(ctx, StatusInput{HypothesisID: investigated.ID, Status: StatusPromoted, RunID: "run-1"}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	remainder := []string{created[1].ID, created[2].ID, created[3].ID}
	if _, err := store.DeferFrontier(ctx, "run-1", remainder, "run budget exhausted"); err != nil {
		t.Fatalf("defer frontier: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close frontier: %v", err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen frontier: %v", err)
	}
	defer reopened.Close()

	unexplored, err := reopened.Unexplored(ctx, 0)
	if err != nil {
		t.Fatalf("read unexplored frontier: %v", err)
	}
	gotIDs := make([]string, len(unexplored))
	for i, hypothesis := range unexplored {
		gotIDs[i] = hypothesis.ID
		if hypothesis.Status != StatusDeferred {
			t.Fatalf("unexplored candidate %s status = %q, want %q", hypothesis.ID, hypothesis.Status, StatusDeferred)
		}
	}
	// Highest priority first: 0.7, then 0.5, then 0.2.
	wantIDs := []string{created[3].ID, created[1].ID, created[2].ID}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("unexplored frontier = %v, want %v ordered by priority", gotIDs, wantIDs)
	}

	history, err := reopened.StatusHistory(ctx, created[1].ID)
	if err != nil {
		t.Fatalf("read status history: %v", err)
	}
	if len(history) != 2 || history[1].Payload.Note != "run budget exhausted" {
		t.Fatalf("deferral history = %v, want the checkpoint note preserved", history)
	}

	t.Run("a revision replaces its ancestor on the frontier", func(t *testing.T) {
		revision, err := reopened.CreateHypothesis(ctx, HypothesisInput{
			RunID:      "run-2",
			AncestorID: created[3].ID,
			Status:     StatusQueued,
			Payload:    hypothesisPayload("candidate 3, reworded", 0.7),
		})
		if err != nil {
			t.Fatalf("revise: %v", err)
		}
		unexplored, err := reopened.Unexplored(ctx, 0)
		if err != nil {
			t.Fatalf("read unexplored frontier: %v", err)
		}
		for _, hypothesis := range unexplored {
			if hypothesis.ID == created[3].ID {
				t.Fatal("a superseded revision is still on the frontier")
			}
		}
		if unexplored[0].ID != revision.ID {
			t.Fatalf("frontier head = %s, want the revision %s", unexplored[0].ID, revision.ID)
		}
		// The ancestor is still readable: deferral and revision remove
		// nothing.
		if _, err := reopened.Hypothesis(ctx, created[3].ID); err != nil {
			t.Fatalf("read superseded ancestor: %v", err)
		}
	})

	t.Run("limit bounds the batch a run takes", func(t *testing.T) {
		batch, err := reopened.Unexplored(ctx, 2)
		if err != nil {
			t.Fatalf("read unexplored frontier: %v", err)
		}
		if len(batch) != 2 {
			t.Fatalf("limited frontier has %d entries, want 2", len(batch))
		}
	})
}

// TestPlaintextColumnsMatchAllowlist pins the §9 split. Every frontier table
// carries exactly one payload_json column, holding everything the sync slice
// must seal, and its remaining columns are only what §9's plaintext allowlist
// admits: identifiers, entity kind, schema version, counts, lifecycle state,
// and timestamps. A new column that is not obviously allowlisted fails here
// before it can leak to PostgreSQL.
func TestPlaintextColumnsMatchAllowlist(t *testing.T) {
	store := openStore(t)
	want := map[string][]string{
		"frontier_hypothesis": {"id", "ancestor_id", "run_id", "schema_version", "created_at"},
		"frontier_status_event": {
			"id", "hypothesis_id", "seq", "status", "run_id", "recorded_at",
		},
		"frontier_hypothesis_link": {"id", "from_id", "to_id", "link_type", "created_at"},
		"frontier_observation": {
			"id", "ancestor_id", "hypothesis_id", "run_id", "recipe_id", "recipe_version",
			"schema_version", "evidence_count", "created_at",
		},
		"frontier_finding":             {"id", "ancestor_id", "run_id", "schema_version", "created_at"},
		"frontier_finding_observation": {"finding_id", "observation_id", "position"},
		"frontier_proposal":            {"id", "ancestor_id", "run_id", "schema_version", "created_at"},
		"frontier_proposal_finding":    {"proposal_id", "finding_id", "position"},
		"frontier_disposition": {
			"id", "subject_type", "subject_id", "seq", "disposition", "reviewer_id",
			"context_id", "duplicate_of_id", "recorded_at",
		},
		"frontier_refinement_request": {"id", "disposition_id", "subject_type", "subject_id", "created_at"},
	}
	// Join tables are pure relationship IDs and carry no payload at all.
	payloadFree := map[string]bool{
		"frontier_finding_observation": true,
		"frontier_proposal_finding":    true,
	}

	tables := frontierTables(t, store)
	if len(tables) != len(want) {
		t.Fatalf("frontier has tables %v, want %d described in the allowlist", tables, len(want))
	}
	for _, table := range tables {
		expected, ok := want[table]
		if !ok {
			t.Fatalf("table %s is not described in the plaintext allowlist", table)
		}
		columns := tableColumns(t, store, table)
		var plaintext []string
		payloads := 0
		for _, column := range columns {
			if column == "payload_json" {
				payloads++
				continue
			}
			plaintext = append(plaintext, column)
		}
		if payloadFree[table] && payloads != 0 {
			t.Fatalf("%s is a relationship table but carries %d payload columns", table, payloads)
		}
		if !payloadFree[table] && payloads != 1 {
			t.Fatalf("%s has %d payload columns, want exactly 1", table, payloads)
		}
		sort.Strings(plaintext)
		wantSorted := append([]string(nil), expected...)
		sort.Strings(wantSorted)
		if !reflect.DeepEqual(plaintext, wantSorted) {
			t.Fatalf("%s plaintext columns = %v, want %v", table, plaintext, wantSorted)
		}
	}
}

// TestPayloadRoundTripKeepsEvidenceWithItsLocator proves evidence survives the
// payload encode/decode boundary intact, and that a payload whose evidence
// lost its locator fails to decode instead of yielding an unverifiable claim.
func TestPayloadRoundTripKeepsEvidenceWithItsLocator(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{RunID: "r", Payload: hypothesisPayload("idea", 0.2)})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	payload := observationPayload("a claim", mustEvidence(t, 5, "supports"), mustEvidence(t, 9, "supports too"))
	payload.CounterEvidenceAbsent = false
	payload.CounterEvidence = []Evidence{mustEvidence(t, 11, "against")}
	written, err := store.CreateObservation(ctx, ObservationInput{
		HypothesisID: hypothesis.ID, RunID: "r", RecipeID: "lens", RecipeVersion: 3, Payload: payload,
	})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}
	if written.EvidenceCount != 2 {
		t.Fatalf("evidence count = %d, want 2", written.EvidenceCount)
	}

	read, err := store.Observation(ctx, written.ID)
	if err != nil {
		t.Fatalf("read observation: %v", err)
	}
	if !reflect.DeepEqual(read.Payload, payload) {
		t.Fatalf("payload round trip changed the claim:\n got %+v\nwant %+v", read.Payload, payload)
	}
	if read.Payload.Evidence[0].Locator() != syntheticLocator(5) {
		t.Fatalf("locator = %+v, want %+v", read.Payload.Evidence[0].Locator(), syntheticLocator(5))
	}

	t.Run("a payload whose evidence lost its locator fails to decode", func(t *testing.T) {
		if _, err := store.db.ExecContext(ctx,
			`UPDATE frontier_observation SET payload_json = ? WHERE id = ?`,
			`{"claim":"a claim","confidence":"low","impact":"low","evidence":[{"note":"orphan"}]}`,
			written.ID); err != nil {
			t.Fatalf("corrupt payload: %v", err)
		}
		if _, err := store.Observation(ctx, written.ID); !errors.Is(err, ErrInvalidLocator) {
			t.Fatalf("got %v, want ErrInvalidLocator", err)
		}
	})
}

// TestOpenRefusesUnknownSchema proves the durable half of local state is never
// discarded to resolve a surprise: unlike the rebuildable retrieval cache, a
// newer schema is an error, because these rows are the only copy of analysis
// that has not synchronized yet.
func TestOpenRefusesUnknownSchema(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	hypothesis, err := store.CreateHypothesis(context.Background(), HypothesisInput{
		RunID: "r", Payload: hypothesisPayload("survives", 0.1),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	if _, err := store.db.Exec(`UPDATE schema_migration SET version = 99 WHERE component = ?`, component); err != nil {
		t.Fatalf("forge schema version: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close frontier: %v", err)
	}

	if _, err := Open(dir); err == nil {
		t.Fatal("opened a frontier with a newer schema")
	} else if !strings.Contains(err.Error(), "newer than this build") {
		t.Fatalf("got %v, want a schema-version error", err)
	}
	if _, err := os.Stat(store.Path()); err != nil {
		t.Fatalf("refused open removed the durable database: %v", err)
	}

	// Rolling the version back proves the rows were never touched.
	reopened, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer reopened.Close()
	var statement string
	if err := reopened.QueryRow(`SELECT json_extract(payload_json, '$.statement')
		FROM frontier_hypothesis WHERE id = ?`, hypothesis.ID).Scan(&statement); err != nil {
		t.Fatalf("read hypothesis after refused open: %v", err)
	}
	if statement != "survives" {
		t.Fatalf("statement = %q, want %q", statement, "survives")
	}
}

// TestOpenIsIdempotent proves reopening applies no migration twice and leaves
// existing rows in place, which is what makes the durable file safe to open on
// every process start.
func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := Open(dir)
	if err != nil {
		t.Fatalf("open frontier: %v", err)
	}
	if _, err := first.CreateHypothesis(context.Background(), HypothesisInput{
		RunID: "r", Payload: hypothesisPayload("kept", 0.1),
	}); err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close frontier: %v", err)
	}
	for range 3 {
		store, err := Open(dir)
		if err != nil {
			t.Fatalf("reopen frontier: %v", err)
		}
		var version int
		if err := store.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`, component).Scan(&version); err != nil {
			t.Fatalf("read schema version: %v", err)
		}
		if version != len(migrations) {
			t.Fatalf("schema version = %d, want %d", version, len(migrations))
		}
		frontier, err := store.Unexplored(context.Background(), 0)
		if err != nil {
			t.Fatalf("read unexplored frontier: %v", err)
		}
		if len(frontier) != 1 {
			t.Fatalf("frontier has %d candidates after reopen, want 1", len(frontier))
		}
		if err := store.Close(); err != nil {
			t.Fatalf("close frontier: %v", err)
		}
	}
}

// TestEvidenceFromScannedCorpus proves the evidence type accepts exactly what
// internal/event produces, using a generated corpus rather than any real
// transcript: a locator that came out of a scan recovers the same record bytes
// after a full round trip through the store.
func TestEvidenceFromScannedCorpus(t *testing.T) {
	ctx := context.Background()
	corpus, err := synth.Generate(t.TempDir(), synth.Profile{
		Seed:                20260829,
		OMPSessions:         1,
		SizeBuckets:         []synth.SizeBucket{{Bytes: 4 << 10, Weight: 100}},
		ArtifactsPerSession: [2]int{0, 0},
		BlobCount:           1,
	})
	if err != nil {
		t.Fatalf("generate corpus: %v", err)
	}
	session := corpus.Sessions[0]
	log, err := os.Open(session.Path)
	if err != nil {
		t.Fatalf("open generated session: %v", err)
	}
	defer log.Close()

	var locators []event.Locator
	err = event.Scan(log, event.Stream{
		Harness: event.HarnessOMP, AdapterSchema: 1, SourceID: session.ID, Path: session.Path,
	}, func(e event.Event) error {
		if e.Kind == event.KindUserReport && len(locators) < 3 {
			locators = append(locators, e.Locator)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan generated session: %v", err)
	}
	if len(locators) == 0 {
		t.Fatal("generated session produced no user reports to cite")
	}

	evidence := make([]Evidence, 0, len(locators))
	for i, locator := range locators {
		item, err := NewEvidence(locator, fmt.Sprintf("cited record %d", i))
		if err != nil {
			t.Fatalf("scanned locator %+v rejected: %v", locator, err)
		}
		evidence = append(evidence, item)
	}

	store := openStore(t)
	hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID: "run-1", Payload: hypothesisPayload("a candidate over generated material", 0.5),
	})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	observation, err := store.CreateObservation(ctx, ObservationInput{
		HypothesisID: hypothesis.ID, RunID: "run-1", RecipeID: "outcome-integrity", RecipeVersion: 1,
		Payload: observationPayload("the request is restated", evidence...),
	})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}

	read, err := store.Observation(ctx, observation.ID)
	if err != nil {
		t.Fatalf("read observation: %v", err)
	}
	for i, item := range read.Payload.Evidence {
		if item.Locator() != locators[i] {
			t.Fatalf("evidence %d locator = %+v, want %+v", i, item.Locator(), locators[i])
		}
	}
}

// TestRecordTimestampsAreUTC keeps stored times comparable across the machines
// §9 expects to share this state: a local-zone timestamp would sort correctly
// on its own machine and wrongly against another's.
func TestRecordTimestampsAreUTC(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	store.now = func() time.Time { return time.Date(2026, 8, 29, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60)) }
	hypothesis, err := store.CreateHypothesis(ctx, HypothesisInput{RunID: "r", Payload: hypothesisPayload("idea", 0.1)})
	if err != nil {
		t.Fatalf("create hypothesis: %v", err)
	}
	read, err := store.Hypothesis(ctx, hypothesis.ID)
	if err != nil {
		t.Fatalf("read hypothesis: %v", err)
	}
	if _, offset := read.CreatedAt.Zone(); offset != 0 {
		t.Fatalf("stored timestamp zone offset = %d, want UTC", offset)
	}
	if !read.CreatedAt.Equal(hypothesis.CreatedAt) {
		t.Fatalf("stored timestamp %s differs from the returned %s", read.CreatedAt, hypothesis.CreatedAt)
	}
}
