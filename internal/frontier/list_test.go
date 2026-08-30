package frontier_test

import (
	"context"
	"testing"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
)

// TestEnumerationReachesEveryRecord is the point of the listing API. Before it
// existed a candidate that was neither unexplored nor enrolled for review could
// be read by identifier and found by no list, which makes §5.2's promise that
// nothing is lost true in storage and invisible in practice. A rejected
// hypothesis is the case that matters: §4.7 says rejection never deletes, so a
// list that omitted it by default would quietly contradict the model.
func TestEnumerationReachesEveryRecord(t *testing.T) {
	store, err := frontier.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	var ids []string
	for _, status := range []frontier.Status{
		frontier.StatusUntriaged,
		frontier.StatusQueued,
		frontier.StatusInvestigating,
		frontier.StatusDeferred,
		frontier.StatusRejected,
		frontier.StatusPromoted,
	} {
		h, err := store.CreateHypothesis(ctx, frontier.HypothesisInput{
			RunID:   "r-1",
			Payload: frontier.HypothesisPayload{Statement: "synthetic " + string(status)},
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if status != frontier.StatusUntriaged {
			if _, err := store.SetStatus(ctx, frontier.StatusInput{
				HypothesisID: h.ID, Status: status, RunID: "r-1",
			}); err != nil {
				t.Fatalf("set %s: %v", status, err)
			}
		}
		ids = append(ids, h.ID)
	}

	all, total, err := store.Hypotheses(ctx, frontier.ListFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != len(ids) || len(all) != len(ids) {
		t.Fatalf("listed %d of %d (total %d); every state must be reachable", len(all), len(ids), total)
	}

	// Rejected must be present in an unfiltered list, not hidden.
	rejected, _, err := store.Hypotheses(ctx, frontier.ListFilter{
		Statuses: []frontier.Status{frontier.StatusRejected},
	})
	if err != nil {
		t.Fatalf("list rejected: %v", err)
	}
	if len(rejected) != 1 {
		t.Errorf("filtering to rejected returned %d records, want 1", len(rejected))
	}

	// Paging must be a total order with no gap and no repeat.
	seen := map[string]int{}
	for offset := 0; offset < len(ids); offset++ {
		page, _, err := store.Hypotheses(ctx, frontier.ListFilter{Limit: 1, Offset: offset})
		if err != nil {
			t.Fatalf("page %d: %v", offset, err)
		}
		if len(page) != 1 {
			t.Fatalf("page %d returned %d records", offset, len(page))
		}
		seen[page[0].ID]++
	}
	if len(seen) != len(ids) {
		t.Errorf("paging saw %d distinct records of %d", len(seen), len(ids))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("record %s appeared %d times across pages", id, n)
		}
	}

	// An unbounded request is bounded anyway, or a large corpus would arrive
	// whole because a caller forgot a limit.
	if _, _, err := store.Hypotheses(ctx, frontier.ListFilter{Limit: 100000}); err != nil {
		t.Fatalf("oversized limit: %v", err)
	}
}

// TestFindingsAndProposalsEnumerate covers the other two record types, and
// asserts a listed record carries the same derived links a detail view shows —
// a leaner list projection is how a list and a detail view start disagreeing
// about the same record.
func TestFindingsAndProposalsEnumerate(t *testing.T) {
	store, err := frontier.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	h, err := store.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "r-1",
		Payload: frontier.HypothesisPayload{Statement: "synthetic"},
	})
	if err != nil {
		t.Fatalf("hypothesis: %v", err)
	}
	locator := event.Locator{Path: "/synthetic/log.jsonl", Line: 1, ByteOffset: 0,
		Digest: "0000000000000000000000000000000000000000000000000000000000000000"}
	evidence, err := frontier.NewEvidence(locator, "synthetic evidence")
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	obs, err := store.CreateObservation(ctx, frontier.ObservationInput{
		HypothesisID:  h.ID,
		RunID:         "r-1",
		RecipeID:      "synthetic-lens",
		RecipeVersion: 1,
		Payload: frontier.ObservationPayload{
			Claim:      "synthetic claim",
			Confidence: frontier.ConfidenceLow,
			Impact:     frontier.ImpactLow,
			Evidence:   []frontier.Evidence{evidence},
			// 4.3 requires counter-evidence to be listed or its absence
			// declared; silence is not an option the model allows.
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		t.Fatalf("observation: %v", err)
	}
	finding, err := store.CreateFinding(ctx, frontier.FindingInput{
		RunID:          "r-1",
		ObservationIDs: []string{obs.ID},
		Payload: frontier.FindingPayload{
			Title:                 "synthetic finding",
			Pattern:               "a synthetic pattern, stated because 4.4 requires the explanation",
			CounterEvidenceAbsent: true,
		},
	})
	if err != nil {
		t.Fatalf("finding: %v", err)
	}
	if _, err := store.CreateProposal(ctx, frontier.ProposalInput{
		RunID:      "r-1",
		FindingIDs: []string{finding.ID},
		Payload: frontier.ProposalPayload{
			Title:   "synthetic proposal",
			Problem: "a synthetic problem statement",
			Outcome: "a synthetic proposed outcome",
			Impact:  frontier.ImpactLow,
			// 4.5 requires a publication classification; there is no default,
			// because a proposal with no stated privacy posture is exactly the
			// one that gets shared by accident.
			Classification: frontier.ClassificationPrivate,
		},
	}); err != nil {
		t.Fatalf("proposal: %v", err)
	}

	findings, total, err := store.Findings(ctx, frontier.ListFilter{})
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if total != 1 || len(findings) != 1 {
		t.Fatalf("listed %d findings (total %d), want 1", len(findings), total)
	}
	if len(findings[0].ObservationIDs) != 1 || len(findings[0].HypothesisIDs) != 1 {
		t.Errorf("listed finding lost its derived links: observations %v hypotheses %v",
			findings[0].ObservationIDs, findings[0].HypothesisIDs)
	}

	proposals, total, err := store.Proposals(ctx, frontier.ListFilter{})
	if err != nil {
		t.Fatalf("proposals: %v", err)
	}
	if total != 1 || len(proposals) != 1 {
		t.Fatalf("listed %d proposals (total %d), want 1", len(proposals), total)
	}
	if len(proposals[0].FindingIDs) != 1 {
		t.Errorf("listed proposal lost its findings: %v", proposals[0].FindingIDs)
	}
}
