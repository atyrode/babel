package evaluation

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// The backlog share is a share like the others: inside [0,1], counted in the
// sum that must not over-commit a cycle, and zero is a policy — a deployment
// that wants its backlog left where it is spends nothing on it.
func TestBacklogShareIsValidatedAsAShare(t *testing.T) {
	if err := ValidatePolicy(DefaultPolicy()); err != nil {
		t.Fatalf("the default policy must validate with its backlog share: %v", err)
	}
	if DefaultPolicy().BacklogShare != DefaultBacklogShare {
		t.Fatalf("the default policy does not carry the default backlog share")
	}

	zero := DefaultPolicy()
	zero.BacklogShare = 0
	if err := ValidatePolicy(zero); err != nil {
		t.Errorf("a deployment that works its backlog by hand must be allowed to reserve nothing: %v", err)
	}

	over := DefaultPolicy()
	over.BacklogShare = 1 - over.CoverageShare - over.ExplorationShare - over.DiscoveryShare -
		over.FilingShare + 0.01
	if err := ValidatePolicy(over); !errors.Is(err, ErrInvalid) {
		t.Fatalf("shares totalling above one must be refused, got %v", err)
	}

	outside := DefaultPolicy()
	outside.BacklogShare = 1.5
	if err := ValidatePolicy(outside); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "backlog share") {
		t.Errorf("a backlog share outside [0,1] must be refused by name, got %v", err)
	}

	prev := DefaultPolicy()
	next := prev
	next.BacklogShare = prev.BacklogShare / 2
	if out := next.Invalidates(prev); !out.Selection {
		t.Error("moving the backlog share changes which draws are legitimate under it")
	}
}

// backlogPolicy is a policy whose whole cycle is the backlog share, so a
// draw's lane is decided by what is drawable rather than by the seed.
func backlogPolicy() Policy {
	policy := viewPolicy()
	policy.CoverageShare = 0
	policy.DiscoveryShare = 0.01
	policy.ExplorationShare = 0.01
	policy.FilingShare = 0
	policy.BacklogShare = 0.98
	return policy
}

func backlogItem(id string, at time.Time) projected {
	return projected{
		Artifact: hypothesisArtifact(id, at),
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
		Required: map[string]bool{RoleReception: true},
	}
}

// The backlog lane draws the candidates the frontier reports as deferred, in
// the order it reported them — oldest deferral first — and a candidate nobody
// reported as deferred is not drawable for backlog work however old it is.
func TestBacklogLaneDrawsOnlyDeferredCandidates(t *testing.T) {
	now := time.Now().UTC()
	// The newest record is the oldest deferral: a candidate written in
	// January and set down in June has been waiting since June, so the
	// creation dates deliberately disagree with the frontier's order.
	longest := backlogItem("h-longest", now.Add(-2*time.Hour))
	recent := backlogItem("h-recent", now.Add(-72*time.Hour))
	live := backlogItem("h-live", now.Add(-96*time.Hour))

	in := drawInput{
		Policy: backlogPolicy(),
		Items:  []projected{recent, longest, live},
		Deferred: []Subject{
			{Kind: SubjectKindHypothesis, ID: "h-longest"},
			{Kind: SubjectKindHypothesis, ID: "h-recent"},
		},
		Now: now,
	}
	result, err := selectDraw(in, "run-1", 7)
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if result.Lane != LaneBacklog {
		t.Fatalf("lane = %q, want %q", result.Lane, LaneBacklog)
	}
	if result.Assignment.Role != RoleBacklog {
		t.Fatalf("role = %q, want %q", result.Assignment.Role, RoleBacklog)
	}
	if got := result.Assignment.Subject.ID; got != "h-longest" {
		t.Fatalf("the backlog is worked in the frontier's own order, drew %q", got)
	}

	candidates, _ := buildCandidates(in)
	for _, candidate := range candidates {
		if candidate.Backlog && candidate.Item.Artifact.Subject.ID == "h-live" {
			t.Fatal("a candidate nobody reported as deferred is drawable for backlog work")
		}
	}
}

// A share of zero draws nothing and says so, and a share with an empty
// backlog says the backlog is empty rather than that nothing was drawable.
func TestTheBacklogShareSaysWhyItDidNotSpend(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	policy.BacklogShare = 0
	_, gaps := buildCandidates(drawInput{
		Policy: policy,
		Items:  []projected{backlogItem("h1", now.Add(-48*time.Hour))},
		Now:    now,
	})
	if hasGap(gaps, "nothing is deferred") {
		t.Error("a deployment that reserved nothing owes no answer about an empty backlog")
	}

	policy.BacklogShare = DefaultBacklogShare
	_, gaps = buildCandidates(drawInput{
		Policy: policy,
		Items:  []projected{backlogItem("h1", now.Add(-48*time.Hour))},
		Now:    now,
	})
	if !hasGap(gaps, "nothing is deferred") {
		t.Errorf("an empty backlog is the good outcome and must be named: %v", gaps)
	}

	_, gaps = buildCandidates(drawInput{
		Policy:   policy,
		Deferred: []Subject{{Kind: SubjectKindHypothesis, ID: "h-remote"}},
		Now:      now,
	})
	if !hasGap(gaps, "not in the reviewable inventory") {
		t.Errorf("a deferred candidate this projection cannot serve must be named: %v", gaps)
	}
}

// A candidate an accepted proposal superseded or retired is not work: it is
// not drawn for review in any role, and the gap says which state it is in.
func TestSupersededAndRetiredCandidatesAreNotDrawn(t *testing.T) {
	now := time.Now().UTC()
	for _, status := range []frontier.Status{frontier.StatusSuperseded, frontier.StatusRetired} {
		t.Run(string(status), func(t *testing.T) {
			item := backlogItem("h1", now.Add(-96*time.Hour))
			item.Artifact.Status = string(status)
			policy := viewPolicy()
			policy.BacklogShare = 0
			policy.FilingShare = 0
			candidates, gaps := buildCandidates(drawInput{
				Policy: policy,
				Items:  []projected{item},
				Now:    now,
			})
			if len(candidates) != 0 {
				t.Fatalf("a %s candidate is drawable for review: %+v", status, candidates)
			}
			if !hasGap(gaps, string(status)) {
				t.Errorf("the gap does not say the candidate is %s: %v", status, gaps)
			}
		})
	}
}
