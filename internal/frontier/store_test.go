package frontier

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/harness"
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

// TestReopenUndecidesWithoutRewriting is the transition the reconsideration
// lifecycle turns on. A rejected record that new evidence unsettled has to
// become decidable again without anybody having to endorse it first, and
// without the rejection disappearing: both events stay, in order, and the
// derived status goes back to `new`.
func TestReopenUndecidesWithoutRewriting(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	_, _, _, proposal := developPath(t, store)
	subject := Ref{Type: EntityProposal, ID: proposal.ID}

	if _, err := store.Decide(ctx, DispositionInput{
		Subject: subject, Disposition: DispositionReject, ReviewerID: "operator",
		Note: "the evidence does not support the outcome",
	}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := store.Decide(ctx, DispositionInput{
		Subject: subject, Disposition: DispositionReopen, ReviewerID: "operator",
		Note: "a later session contradicts the basis of the rejection",
	}); err != nil {
		t.Fatalf("reopen: %v", err)
	}

	status, err := store.ReviewStatus(ctx, subject)
	if err != nil {
		t.Fatalf("derive review status: %v", err)
	}
	if status != ReviewNew {
		t.Fatalf("review status = %q, want %q after a reopen", status, ReviewNew)
	}
	// The reopened record is decidable again, and on its own merits: the
	// point of the fifth value is that reconsidering costs no endorsement.
	if _, err := store.Decide(ctx, DispositionInput{
		Subject: subject, Disposition: DispositionReject, ReviewerID: "operator",
		Note: "and the second reading agrees with the first",
	}); err != nil {
		t.Fatalf("reject after reopen: %v", err)
	}
	history, err := store.DispositionHistory(ctx, subject)
	if err != nil {
		t.Fatalf("read disposition history: %v", err)
	}
	want := []Disposition{DispositionReject, DispositionReopen, DispositionReject}
	if len(history) != len(want) {
		t.Fatalf("history has %d events, want %d", len(history), len(want))
	}
	for i, disposition := range want {
		if history[i].Disposition != disposition {
			t.Fatalf("history[%d] = %q, want %q", i, history[i].Disposition, disposition)
		}
	}
	// And the record itself reads with the status its events justify.
	reread, err := store.Proposal(ctx, proposal.ID)
	if err != nil {
		t.Fatalf("read proposal: %v", err)
	}
	if reread.ReviewStatus != ReviewRejected {
		t.Fatalf("review status = %q, want %q", reread.ReviewStatus, ReviewRejected)
	}
}

// TestReopenIsRefusedWhereThereIsNothingToReopen pins the three refusals. A
// reopen that says nothing about why, a reopen of a record nobody decided, and
// a reopen of a record whose decision now belongs elsewhere are each a
// different false statement about the history.
func TestReopenIsRefusedWhereThereIsNothingToReopen(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	hypothesis, _, finding, proposal := developPath(t, store)

	t.Run("a reopen states its reason", func(t *testing.T) {
		if _, err := store.Decide(ctx, DispositionInput{
			Subject:     Ref{Type: EntityProposal, ID: proposal.ID},
			Disposition: DispositionReopen, ReviewerID: "operator",
		}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("got %v, want ErrInvalidValue for a reopen with no reason", err)
		}
	})

	t.Run("an undecided record has nothing to reopen", func(t *testing.T) {
		if _, err := store.Decide(ctx, DispositionInput{
			Subject:     Ref{Type: EntityProposal, ID: proposal.ID},
			Disposition: DispositionReopen, ReviewerID: "operator",
			Note: "reopening what was never decided",
		}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("got %v, want ErrInvalidValue reopening an undecided record", err)
		}
	})

	t.Run("a duplicate is reopened at its original", func(t *testing.T) {
		subject := Ref{Type: EntityHypothesis, ID: hypothesis.ID}
		other, err := store.CreateHypothesis(ctx, HypothesisInput{
			RunID: "run-1", Payload: hypothesisPayload("the original this duplicates", 0.4),
		})
		if err != nil {
			t.Fatalf("create original: %v", err)
		}
		if _, err := store.Decide(ctx, DispositionInput{
			Subject: subject, Disposition: DispositionDuplicate, ReviewerID: "operator",
			DuplicateOfID: other.ID,
		}); err != nil {
			t.Fatalf("mark duplicate: %v", err)
		}
		if _, err := store.Decide(ctx, DispositionInput{
			Subject: subject, Disposition: DispositionReopen, ReviewerID: "operator",
			Note: "reopening a duplicate",
		}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("got %v, want ErrInvalidValue reopening a duplicate", err)
		}
	})

	t.Run("a rejection that authorized a refinement is answered there", func(t *testing.T) {
		subject := Ref{Type: EntityFinding, ID: finding.ID}
		if _, _, err := store.RejectAndRefine(ctx,
			DispositionInput{Subject: subject, ReviewerID: "operator", Note: "too broad"},
			RefinementPayload{Guidance: "narrow it to one repository"},
		); err != nil {
			t.Fatalf("reject and refine: %v", err)
		}
		if _, err := store.Decide(ctx, DispositionInput{
			Subject: subject, Disposition: DispositionReopen, ReviewerID: "operator",
			Note: "reopening the ancestor",
		}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("got %v, want ErrInvalidValue reopening a refine-requested record", err)
		}
	})

	t.Run("a reopen names no original", func(t *testing.T) {
		if _, err := store.Decide(ctx, DispositionInput{
			Subject:     Ref{Type: EntityProposal, ID: proposal.ID},
			Disposition: DispositionReopen, ReviewerID: "operator",
			Note: "reopening", DuplicateOfID: proposal.ID,
		}); !errors.Is(err, ErrInvalidValue) {
			t.Fatalf("got %v, want ErrInvalidValue for a reopen naming an original", err)
		}
	})
}

// TestStageResolvedDispositionServesACallerResolvedSubject is the path that
// makes an operator's reconsideration decision and the reopen it performs one
// durable act. The caller owns the transaction, so the two writes commit or
// roll back together; the subject is one this store holds no row for, which is
// the whole reason Decide cannot serve the case.
func TestStageResolvedDispositionServesACallerResolvedSubject(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	// Deliberately not created here: a record another host published has no
	// durable row on this machine, and staging a decision about it must not
	// require one.
	subject := Ref{Type: EntityProposal, ID: "pro_resolved-elsewhere"}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	event, publish, err := store.StageResolvedDisposition(ctx, tx, DispositionInput{
		Subject: subject, Disposition: DispositionReopen, ReviewerID: "operator",
		Note:           "the rejection's basis changed",
		ResolvedStatus: ReviewRejected,
	})
	if err != nil {
		tx.Rollback()
		t.Fatalf("stage resolved disposition: %v", err)
	}
	if event.Disposition != DispositionReopen || event.Subject != subject {
		t.Fatalf("event = %+v, want the staged reopen", event)
	}
	// A read on the same single-writer file would block behind this open
	// transaction, which is itself the atomicity this method relies on: the
	// staged row becomes visible with the caller's commit, below.
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := publish(ctx); err != nil {
		t.Fatalf("publish after commit: %v", err)
	}
	history, err := store.DispositionHistory(ctx, subject)
	if err != nil {
		t.Fatalf("read disposition history: %v", err)
	}
	if len(history) != 1 || history[0].Disposition != DispositionReopen {
		t.Fatalf("history = %v, want one reopen", history)
	}
	status, err := store.ReviewStatus(ctx, subject)
	if err != nil {
		t.Fatalf("derive review status: %v", err)
	}
	if status != ReviewNew {
		t.Fatalf("review status = %q, want %q", status, ReviewNew)
	}
}

// TestStageResolvedDispositionRefusesAnUncheckedClaim keeps the resolved path
// from being the loose one. It cannot see the subject, so what it does instead
// is refuse every input it would have to assume something about: a status it
// was not told, a status that admits no decision, and a reopen of a record the
// caller itself resolved as undecided.
func TestStageResolvedDispositionRefusesAnUncheckedClaim(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	subject := Ref{Type: EntityProposal, ID: "pro_resolved-elsewhere"}

	for _, probe := range []struct {
		name string
		in   DispositionInput
	}{
		{
			name: "no resolved status",
			in: DispositionInput{Subject: subject, Disposition: DispositionReopen,
				ReviewerID: "operator", Note: "reopening"},
		},
		{
			name: "a status outside the vocabulary",
			in: DispositionInput{Subject: subject, Disposition: DispositionReopen,
				ReviewerID: "operator", Note: "reopening", ResolvedStatus: ReviewStatus("stale")},
		},
		{
			name: "resolved as undecided",
			in: DispositionInput{Subject: subject, Disposition: DispositionReopen,
				ReviewerID: "operator", Note: "reopening", ResolvedStatus: ReviewNew},
		},
		{
			name: "resolved as a duplicate",
			in: DispositionInput{Subject: subject, Disposition: DispositionReopen,
				ReviewerID: "operator", Note: "reopening", ResolvedStatus: ReviewDuplicate},
		},
		{
			name: "an unreviewable kind",
			in: DispositionInput{Subject: Ref{Type: EntityObservation, ID: "obs_elsewhere"},
				Disposition: DispositionReopen, ReviewerID: "operator", Note: "reopening",
				ResolvedStatus: ReviewRejected},
		},
	} {
		t.Run(probe.name, func(t *testing.T) {
			tx, err := store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin transaction: %v", err)
			}
			defer tx.Rollback()
			if _, _, err := store.StageResolvedDisposition(ctx, tx, probe.in); err == nil {
				t.Fatal("the staged disposition was accepted, want a refusal")
			}
		})
	}

	// And the local path refuses a supplied status, so there is exactly one
	// authority for a subject this store can see.
	_, _, _, proposal := developPath(t, store)
	if _, err := store.Decide(ctx, DispositionInput{
		Subject: Ref{Type: EntityProposal, ID: proposal.ID}, Disposition: DispositionAccept,
		ReviewerID: "operator", ResolvedStatus: ReviewNew,
	}); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("got %v, want ErrInvalidValue for a locally held subject with a supplied status", err)
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
		Reason:     "the second run narrowed the claim to one repository",
		Payload:    hypothesisPayload("handoffs drop constraints, narrowed to one repository", 0.8),
	})
	if err != nil {
		t.Fatalf("revise hypothesis: %v", err)
	}
	if _, err := store.CreateObservation(ctx, ObservationInput{
		HypothesisID: revisedHypothesis.ID, RunID: "run-2", RecipeID: "lens", RecipeVersion: 2,
		AncestorID: observation.ID,
		Reason:     "a counterexample turned up",
		Payload:    observationPayload("restated later, with one counterexample", mustEvidence(t, 21, "second")),
	}); err != nil {
		t.Fatalf("revise observation: %v", err)
	}
	if _, err := store.CreateFinding(ctx, FindingInput{
		RunID: "run-2", AncestorID: finding.ID, ObservationIDs: []string{observation.ID},
		Reason:  "narrowed with the hypothesis it consolidates",
		Payload: findingPayload("late constraints, narrowed"),
	}); err != nil {
		t.Fatalf("revise finding: %v", err)
	}
	if _, err := store.CreateProposal(ctx, ProposalInput{
		RunID: "run-2", AncestorID: proposal.ID, FindingIDs: []string{finding.ID},
		Reason:  "restated over the narrowed finding",
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
			RunID: "run-2", AncestorID: "hyp_absent", Reason: "reworded",
			Payload: hypothesisPayload("orphan", 0.1),
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
			Reason:     "the wording was ambiguous",
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
			"id", "hypothesis_id", "seq", "status", "run_id", "actor_kind", "actor_id", "recorded_at",
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
		// What #114's candidate proposal addresses: relationship ids only,
		// which is why it is payload-free like the finding join beside it.
		"frontier_proposal_hypothesis": {"proposal_id", "hypothesis_id", "position"},
		"frontier_disposition": {
			"id", "subject_type", "subject_id", "seq", "disposition", "reviewer_id",
			"context_id", "duplicate_of_id", "recorded_at",
		},
		"frontier_refinement_request": {"id", "disposition_id", "subject_type", "subject_id", "created_at"},
		"frontier_revision": {
			"id", "entity_type", "entity_id", "root_id", "supersedes_id", "seq",
			"actor_kind", "actor_id", "recorded_at",
		},
		"frontier_duplicate_warning": {"id", "hypothesis_id", "duplicate_of", "recorded_at"},
		// Triage advice: the record ids it relates, the pass that wrote it,
		// and when. The rank and the counter-argument are a judgement about
		// what the proposals say, so they stay in the payload for the reason
		// the warning's overlap does.
		"frontier_triage_advice": {
			"id", "proposal_id", "alternative_id", "run_id", "recorded_at",
		},
		"frontier_triage_cluster": {"advice_id", "proposal_id", "position"},
		// A filing: which record, which topic, who filed it, whether the
		// filing was a guess, whether it is a withdrawal, and which row it
		// replaced. Every one is an identifier or a lifecycle bit; the
		// rationale and the withdrawal reason are prose about the corpus and
		// stay in the payload (SPEC.md §4.13, §9).
		"frontier_filing": {
			"id", "record_kind", "record_id", "entity_id", "author", "author_id",
			"heuristic", "withdrawn", "supersedes_id", "schema_version", "created_at",
		},
	}
	// Join tables are pure relationship IDs and carry no payload at all.
	payloadFree := map[string]bool{
		"frontier_finding_observation": true,
		"frontier_proposal_finding":    true,
		"frontier_proposal_hypothesis": true,
		"frontier_triage_cluster":      true,
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

// TestEveryFrontierTableRefusesUpdateAndDelete checks the append-only rule at
// the only level that survives a future caller: the database refuses, so the
// property does not depend on this package never writing an UPDATE. §14's
// frontier gate rests on it, and so does §4.7's append-only review semantics,
// because internal/review records dispositions and refinements in these same
// tables. A test that searched this file for the word DELETE would say nothing
// about the next statement someone adds.
//
// The cases are checked against the tables the file actually carries, so a
// table added later without triggers fails here instead of quietly joining the
// schema unprotected. Every case asserts its row exists first: a DELETE
// against an empty table succeeds and would prove nothing.
func TestEveryFrontierTableRefusesUpdateAndDelete(t *testing.T) {
	ctx := context.Background()
	store := openStore(t)
	hypothesis, observation, finding, proposal := developPath(t, store)

	// developPath leaves the four record tables and their two join tables
	// populated. A link, a later lifecycle event, and a rejection with the
	// refinement it authorized fill the rest.
	other, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID: "run-1", Payload: hypothesisPayload("a second idea", 0.3),
	})
	if err != nil {
		t.Fatalf("create second hypothesis: %v", err)
	}
	if _, err := store.Link(ctx, LinkInput{
		FromID: hypothesis.ID, ToID: other.ID, Type: LinkCorroborates,
	}); err != nil {
		t.Fatalf("link hypotheses: %v", err)
	}
	if _, err := store.SetStatus(ctx, StatusInput{
		HypothesisID: hypothesis.ID, Status: StatusInvestigating, RunID: "run-1",
	}); err != nil {
		t.Fatalf("set status: %v", err)
	}
	if _, _, err := store.RejectAndRefine(ctx,
		DispositionInput{
			Subject:    Ref{Type: EntityProposal, ID: proposal.ID},
			ReviewerID: "operator", Note: "too broad",
		},
		RefinementPayload{Guidance: "narrow it to one repository"},
	); err != nil {
		t.Fatalf("reject and refine: %v", err)
	}
	// A third candidate carries the near-duplicate warning, so the table is
	// populated: a DELETE against an empty table succeeds and would prove
	// nothing.
	warned, err := store.CreateHypothesis(ctx, HypothesisInput{
		RunID:          "run-1",
		Payload:        hypothesisPayload("a second idea, restated", 0.3),
		NearDuplicates: []NearDuplicate{{HypothesisID: other.ID, Overlap: 0.8}},
	})
	if err != nil {
		t.Fatalf("create warned hypothesis: %v", err)
	}
	// A candidate proposal populates #114's addressed-claim join, for the
	// same reason the warned candidate populates the one above it.
	remedy, err := store.CreateCandidateProposal(ctx, CandidateProposalInput{
		RunID:         "run-1",
		HypothesisIDs: []string{hypothesis.ID},
		Payload:       proposalPayload("state constraints up front"),
	})
	if err != nil {
		t.Fatalf("create candidate proposal: %v", err)
	}
	// Triage advice on the remedy, clustered with the consolidation, fills
	// the last two tables. The remedy is the subject because nobody has
	// ruled on it: advice on the rejected consolidation is refused, which is
	// its own property and TestTriagePassCannotRecordADisposition's.
	if _, err := store.Triage().Advise(ctx, TriageInput{
		ProposalID: remedy.ID,
		RunID:      "run-1",
		Cluster:    []string{proposal.ID},
		Payload: TriageAdvicePayload{
			Rank: 1, Cohort: 2,
			CounterArgument: "the constraint may already be stated elsewhere",
		},
	}); err != nil {
		t.Fatalf("advise the remedy: %v", err)
	}
	// A filing fills the last table: what the candidate is about (§4.13).
	if _, err := store.File(ctx, FilingInput{
		Record:    Ref{Type: EntityHypothesis, ID: hypothesis.ID},
		EntityID:  "ent_manifold",
		Rationale: "the evidence is a session in this repository",
		Author:    FilingOperator,
		AuthorID:  "alex",
	}); err != nil {
		t.Fatalf("file the candidate under a topic: %v", err)
	}

	// set is the mutation a future caller might plausibly write, and where
	// selects the row it would aim at. The delete and the row count reuse
	// where, so all three statements address the same rows.
	type immutableCase struct {
		set   string
		where string
		args  []any
	}
	cases := map[string]immutableCase{
		"frontier_hypothesis":          {`run_id = 'run-forged'`, `id = ?`, []any{hypothesis.ID}},
		"frontier_status_event":        {`status = 'promoted'`, `hypothesis_id = ?`, []any{hypothesis.ID}},
		"frontier_hypothesis_link":     {`link_type = 'contradicts'`, `from_id = ?`, []any{hypothesis.ID}},
		"frontier_observation":         {`payload_json = '{}'`, `id = ?`, []any{observation.ID}},
		"frontier_finding":             {`payload_json = '{}'`, `id = ?`, []any{finding.ID}},
		"frontier_finding_observation": {`position = 7`, `finding_id = ?`, []any{finding.ID}},
		"frontier_proposal":            {`payload_json = '{}'`, `id = ?`, []any{proposal.ID}},
		"frontier_proposal_finding":    {`position = 7`, `proposal_id = ?`, []any{proposal.ID}},
		"frontier_proposal_hypothesis": {`position = 7`, `proposal_id = ?`, []any{remedy.ID}},
		"frontier_disposition":         {`disposition = 'accept'`, `subject_id = ?`, []any{proposal.ID}},
		"frontier_refinement_request":  {`payload_json = '{}'`, `subject_id = ?`, []any{proposal.ID}},
		"frontier_revision":            {`actor_id = 'forged'`, `entity_id = ?`, []any{hypothesis.ID}},
		"frontier_duplicate_warning":   {`payload_json = '{}'`, `hypothesis_id = ?`, []any{warned.ID}},
		"frontier_triage_advice":       {`payload_json = '{}'`, `proposal_id = ?`, []any{remedy.ID}},
		"frontier_triage_cluster":      {`position = 7`, `proposal_id = ?`, []any{proposal.ID}},
		"frontier_filing":              {`entity_id = 'ent_forged'`, `record_id = ?`, []any{hypothesis.ID}},
	}

	tables := frontierTables(t, store)
	if len(tables) != len(cases) {
		t.Fatalf("frontier has tables %v, and %d of them are checked for immutability", tables, len(cases))
	}
	count := func(t *testing.T, table string, tc immutableCase) int {
		t.Helper()
		var rows int
		if err := store.db.QueryRowContext(ctx,
			`SELECT count(*) FROM `+table+` WHERE `+tc.where, tc.args...).Scan(&rows); err != nil {
			t.Fatalf("count %s rows: %v", table, err)
		}
		return rows
	}
	for _, table := range tables {
		tc, ok := cases[table]
		if !ok {
			t.Fatalf("table %s is not checked for immutability", table)
		}
		t.Run(table, func(t *testing.T) {
			before := count(t, table, tc)
			if before == 0 {
				t.Fatalf("no %s row matches, so the statements below would abort nothing", table)
			}
			if _, err := store.db.ExecContext(ctx,
				`UPDATE `+table+` SET `+tc.set+` WHERE `+tc.where, tc.args...); err == nil {
				t.Errorf("%s accepted an update", table)
			}
			if _, err := store.db.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE `+tc.where, tc.args...); err == nil {
				t.Errorf("%s accepted a delete", table)
			}
			// An abort that left half its work behind would be no
			// better than the statement it refused.
			if after := count(t, table, tc); after != before {
				t.Errorf("%s has %d matching rows after the refused statements, want %d", table, after, before)
			}
		})
	}
}

// TestFrontierPredatingTheTriggersGainsThem is the half of the immutability
// change that a fresh temporary directory can never exercise. SQLite attaches
// a trigger when it is created, so putting these triggers in the first
// migration would have protected only databases this build creates, and a
// durable file an operator already has would have kept accepting updates while
// every other test passed. They are a second migration for exactly that
// reason, and this proves the forward step runs and leaves the rows alone.
func TestFrontierPredatingTheTriggersGainsThem(t *testing.T) {
	dir := t.TempDir()

	// Exactly what a build older than the triggers left behind: migration
	// 1's tables, the shared ledger at version 1, and no trigger anywhere.
	old, err := sql.Open("sqlite", filepath.Join(dir, databaseFile))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		migrations[0],
		`CREATE TABLE schema_migration(component TEXT PRIMARY KEY, version INTEGER)`,
	} {
		if _, err := old.Exec(statement); err != nil {
			t.Fatalf("build a pre-trigger frontier: %v", err)
		}
	}
	if _, err := old.Exec(`INSERT INTO schema_migration(component, version) VALUES(?, 1)`, component); err != nil {
		t.Fatalf("record the pre-trigger version: %v", err)
	}
	// Guard the premise. If a later change moved these triggers into
	// migration 1, this file would no longer represent a frontier that
	// predates them and the upgrade below would prove nothing — while the
	// databases this test exists for would be left unprotected.
	var existing int
	if err := old.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'trigger'`).Scan(&existing); err != nil {
		t.Fatalf("count triggers in the pre-trigger frontier: %v", err)
	}
	if existing != 0 {
		t.Fatalf("migration 1 created %d triggers, so no existing database can gain them by upgrading", existing)
	}
	// A hypothesis and the initial status event the older build always
	// wrote beside it, since status is read from that history rather than
	// from a column.
	const kept = "hyp_predates_triggers"
	written := formatTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	if _, err := old.Exec(`INSERT INTO frontier_hypothesis(
		id, ancestor_id, run_id, schema_version, created_at, payload_json)
		VALUES(?, NULL, 'run-old', ?, ?, ?)`,
		kept, RecordSchema, written,
		`{"statement":"an idea written before the triggers existed","priority":0.4}`,
	); err != nil {
		t.Fatalf("write a pre-trigger hypothesis: %v", err)
	}
	if _, err := old.Exec(`INSERT INTO frontier_status_event(
		id, hypothesis_id, seq, status, run_id, recorded_at, payload_json)
		VALUES('sev_predates_triggers', ?, 1, ?, 'run-old', ?, '{}')`,
		kept, string(StatusUntriaged), written,
	); err != nil {
		t.Fatalf("write a pre-trigger status event: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close the pre-trigger frontier: %v", err)
	}

	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open a pre-trigger frontier: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close frontier: %v", err)
		}
	})

	var version int
	if err := store.db.QueryRow(`SELECT version FROM schema_migration WHERE component = ?`,
		component).Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != len(migrations) {
		t.Fatalf("schema version = %d, want %d", version, len(migrations))
	}

	// Both directions for every table, so a migration that named only some
	// of them fails here rather than at the one table it forgot.
	var triggers int
	if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master
		WHERE type = 'trigger' AND tbl_name LIKE 'frontier_%'`).Scan(&triggers); err != nil {
		t.Fatalf("count triggers: %v", err)
	}
	if want := 2 * len(frontierTables(t, store)); triggers != want {
		t.Fatalf("upgraded frontier has %d triggers, want %d", triggers, want)
	}

	if _, err := store.db.Exec(`UPDATE frontier_hypothesis SET run_id = 'run-forged' WHERE id = ?`, kept); err == nil {
		t.Error("a frontier that predates the triggers still accepts an update")
	}
	if _, err := store.db.Exec(`DELETE FROM frontier_hypothesis WHERE id = ?`, kept); err == nil {
		t.Error("a frontier that predates the triggers still accepts a delete")
	}

	// The upgrade adds a rule, not a rewrite: the row an operator already
	// had is still readable and unchanged.
	preserved, err := store.Hypothesis(context.Background(), kept)
	if err != nil {
		t.Fatalf("read a pre-trigger hypothesis after the upgrade: %v", err)
	}
	if preserved.RunID != "run-old" {
		t.Errorf("run id = %q, want the value the older build wrote", preserved.RunID)
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
		// The row is forged by an insert rather than by updating the one
		// written above, because the immutability triggers refuse an
		// update — which is the point of them. An insert reaches the same
		// state a corrupted or a foreign-written row would leave behind.
		const forged = "obs_orphan_locator"
		if _, err := store.db.ExecContext(ctx, `INSERT INTO frontier_observation(
			id, ancestor_id, hypothesis_id, run_id, recipe_id, recipe_version,
			schema_version, evidence_count, created_at, payload_json)
			VALUES(?, NULL, ?, 'r', 'lens', 3, ?, 1, ?, ?)`,
			forged, hypothesis.ID, RecordSchema, formatTime(store.now()),
			`{"claim":"a claim","confidence":"low","impact":"low","evidence":[{"note":"orphan"}]}`,
		); err != nil {
			t.Fatalf("forge payload: %v", err)
		}
		if _, err := store.Observation(ctx, forged); !errors.Is(err, ErrInvalidLocator) {
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
		Harness: harness.OMP, AdapterSchema: 1, SourceID: session.ID, Path: session.Path,
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
