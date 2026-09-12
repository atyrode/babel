package evaluation

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// The filing share is a reservation like the others, and the two rules that
// make a share meaningful are that it cannot over-commit a cycle and that a
// deployment which spends nothing on it is a policy rather than a fault.
func TestFilingShareIsValidatedAsAShare(t *testing.T) {
	if err := ValidatePolicy(DefaultPolicy()); err != nil {
		t.Fatalf("the default policy must validate with its filing share: %v", err)
	}
	if DefaultPolicy().FilingShare != DefaultFilingShare {
		t.Fatalf("the default policy does not carry the default filing share")
	}

	zero := DefaultPolicy()
	zero.FilingShare = 0
	if err := ValidatePolicy(zero); err != nil {
		t.Errorf("a deployment that files by hand must be allowed to reserve nothing: %v", err)
	}

	over := DefaultPolicy()
	over.FilingShare = 1 - over.CoverageShare - over.ExplorationShare - over.DiscoveryShare + 0.01
	err := ValidatePolicy(over)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("shares totalling above one must be refused, got %v", err)
	}
	if !strings.Contains(err.Error(), "over-commit") {
		t.Errorf("the refusal must say the cycle is over-committed: %v", err)
	}

	outside := DefaultPolicy()
	outside.FilingShare = 1.5
	if err := ValidatePolicy(outside); !errors.Is(err, ErrInvalid) ||
		!strings.Contains(err.Error(), "filing share") {
		t.Errorf("a filing share outside [0,1] must be refused by name, got %v", err)
	}
}

// A changed filing share moves the next draw and nothing else: no assessment
// was made under it, and no coverage arithmetic reads it.
func TestFilingShareInvalidatesTheSelection(t *testing.T) {
	prev := DefaultPolicy()
	next := prev
	next.FilingShare = prev.FilingShare / 2

	out := next.Invalidates(prev)
	if !out.Selection {
		t.Error("a changed filing share must invalidate the next draw")
	}
	if out.Coverage || out.Ranking {
		t.Error("a changed filing share does not change coverage or the reading order")
	}
	if !slices.Contains(out.Reasons, "filing share") {
		t.Errorf("the invalidation must name the setting that moved: %v", out.Reasons)
	}
}

// filingPolicy is a policy whose whole cycle is the filing share, so a draw's
// lane is decided by what is drawable rather than by the seed.
func filingPolicy() Policy {
	policy := viewPolicy()
	policy.CoverageShare = 0
	policy.DiscoveryShare = 0.01
	policy.ExplorationShare = 0.01
	policy.FilingShare = 0.98
	policy.BacklogShare = 0
	return policy
}

func unfiledItem(id string, at time.Time) projected {
	return projected{
		Artifact: hypothesisArtifact(id, at),
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
		Required: map[string]bool{RoleReception: true},
	}
}

// The filing lane draws the records the frontier reports as unfiled, oldest
// first, in the filing role — and a record nobody reported as unfiled is not
// drawable for filing however overdue its review is.
func TestFilingLaneDrawsOnlyUnfiledRecords(t *testing.T) {
	now := time.Now().UTC()
	older := unfiledItem("h-old", now.Add(-72*time.Hour))
	newer := unfiledItem("h-new", now.Add(-2*time.Hour))
	filed := unfiledItem("h-filed", now.Add(-96*time.Hour))

	in := drawInput{
		Policy: filingPolicy(),
		Items:  []projected{newer, older, filed},
		Unfiled: []Subject{
			{Kind: SubjectKindHypothesis, ID: "h-new"},
			{Kind: SubjectKindHypothesis, ID: "h-old"},
		},
		Now:         now,
		InputDigest: "in-filing",
	}

	result, err := selectDraw(in, "run-filing", 3)
	if err != nil {
		t.Fatalf("draw: %v", err)
	}
	if result.Lane != LaneFiling {
		t.Fatalf("lane = %q, want %q", result.Lane, LaneFiling)
	}
	if result.Assignment.Role != RoleFiling {
		t.Fatalf("role = %q, want %q", result.Assignment.Role, RoleFiling)
	}
	if got := result.Assignment.Subject.ID; got != "h-old" {
		t.Fatalf("the filing backlog is cleared oldest first, drew %q", got)
	}

	// The oldest record of the three is the one nobody reported as unfiled,
	// and no lane may reach it for filing.
	candidates, _ := buildCandidates(in)
	for _, candidate := range candidates {
		if candidate.Filing && candidate.Item.Artifact.Subject.ID == "h-filed" {
			t.Fatal("a record with a live filing was drawn to be filed again")
		}
	}
}

// A filing candidate is not review work, and the reserved review lanes must not
// spend on one: a record drawn to be named would arrive at a reviewer with a
// contract it cannot fill.
func TestReviewLanesNeverDrawAFilingCandidate(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	policy.FilingShare = 0
	in := drawInput{
		Policy:  policy,
		Items:   []projected{unfiledItem("h1", now.Add(-48*time.Hour))},
		Unfiled: []Subject{{Kind: SubjectKindHypothesis, ID: "h1"}},
		Now:     now,
	}
	candidates, _ := buildCandidates(in)
	if len(candidates) != 2 {
		t.Fatalf("an unfiled record with a due reception review is two candidates, got %d", len(candidates))
	}
	rng := rand.New(rand.NewPCG(7, 11))
	for _, lane := range []string{LaneCoverage, LaneWeighted, LaneExploration, LaneDiscovery} {
		chosen := pick(candidates, lane, rng)
		if chosen == nil {
			t.Fatalf("the %s lane drew nothing from a due reception review", lane)
		}
		if chosen.Filing {
			t.Fatalf("the %s lane drew a filing candidate", lane)
		}
	}
	if chosen := pick(candidates, LaneFiling, rng); chosen == nil || !chosen.Filing {
		t.Fatal("the filing lane must draw the filing candidate")
	}
}

// An empty backlog and a backlog nothing can be drawn from are different
// states, and a share that did not spend owes an operator the difference.
func TestTheFilingBacklogExplainsWhatItDidNotDraw(t *testing.T) {
	now := time.Now().UTC()
	policy := filingPolicy()

	_, gaps := buildCandidates(drawInput{
		Policy: policy,
		Items:  []projected{unfiledItem("h1", now.Add(-time.Hour))},
		Now:    now,
	})
	if !hasGap(gaps, "every open record carries a filing") {
		t.Errorf("an empty filing backlog must be reported as such: %v", gaps)
	}

	// A record still listed as unfiled after a completed filing pass is one
	// whose topic question the operator has not answered. Drawing it again
	// would pay to ask a question the ledger already holds.
	subject := Subject{Kind: SubjectKindHypothesis, ID: "h1"}
	assignment := Assignment{ID: "asg-filing", Subject: subject, Role: RoleFiling}
	candidates, gaps := buildCandidates(drawInput{
		Policy:      policy,
		Items:       []projected{unfiledItem("h1", now.Add(-time.Hour))},
		Unfiled:     []Subject{subject},
		Assignments: []Assignment{assignment},
		Attempts:    []Attempt{{AssignmentID: assignment.ID, State: AttemptCompleted}},
		Now:         now,
	})
	for _, candidate := range candidates {
		if candidate.Filing {
			t.Fatal("a record whose filing pass already ran was drawn for filing again")
		}
	}
	if !hasGap(gaps, "a filing pass already ran") {
		t.Errorf("the spent filing pass must be a visible gap: %v", gaps)
	}

	// A record the frontier reports as unfiled that the reviewable inventory
	// does not hold cannot be served a filing context at all.
	_, gaps = buildCandidates(drawInput{
		Policy:  policy,
		Unfiled: []Subject{{Kind: SubjectKindFinding, ID: "f-remote"}},
		Now:     now,
	})
	if !hasGap(gaps, "not in the reviewable inventory") {
		t.Errorf("an unservable unfiled record must be a visible gap: %v", gaps)
	}
}

func hasGap(gaps []string, want string) bool {
	for _, gap := range gaps {
		if strings.Contains(gap, want) {
			return true
		}
	}
	return false
}

// A filing is not a judgement, and the assessment vocabulary enforces the
// separation in both directions: no other role may state where a record
// belongs, and a filing pass that judged the record instead is refused.
func TestFilingAssessmentBelongsToTheFilingRole(t *testing.T) {
	filed := Assessment{Filing: &Filing{Outcome: FilingFiled, Entity: "ent_1", Reason: "why"}}
	if err := filed.validate(RoleFiling); err != nil {
		t.Fatalf("a filed record is a complete filing assessment: %v", err)
	}
	if err := filed.validate(RoleReception); !errors.Is(err, ErrInvalid) {
		t.Errorf("a reception review may not file a record, got %v", err)
	}

	empty := Assessment{}
	if err := empty.validate(RoleFiling); !errors.Is(err, ErrInvalid) {
		t.Errorf("a filing pass that stated nothing must be refused, got %v", err)
	}

	judged := Assessment{
		Filing:        &Filing{Outcome: FilingNone, Reason: "about nothing in particular"},
		Contributions: []Contribution{{Kind: ContributionComment, Text: "and it is weak"}},
	}
	if err := judged.validate(RoleFiling); !errors.Is(err, ErrInvalid) {
		t.Errorf("a filing pass may not contribute to the review of the record, got %v", err)
	}

	for name, filing := range map[string]Filing{
		"unknown outcome":      {Outcome: "sorted", Reason: "why"},
		"filed with nothing":   {Outcome: FilingFiled, Reason: "why"},
		"proposed an entity":   {Outcome: FilingProposed, Entity: "ent_1", Proposal: "pro_1", Operation: "create", Reason: "why"},
		"proposed no change":   {Outcome: FilingProposed, Proposal: "pro_1", Reason: "why"},
		"no topic but an id":   {Outcome: FilingNone, Entity: "ent_1", Reason: "why"},
		"answered no ask":      {Outcome: FilingAnswered, Reason: "why"},
		"answered with a plan": {Outcome: FilingAnswered, Ask: "cmp_1", Proposal: "pro_1", Reason: "why"},
		"no reason for a why":  {Outcome: FilingFiled, Entity: "ent_1"},
	} {
		assessment := Assessment{Filing: &filing}
		if err := assessment.validate(RoleFiling); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s must be refused, got %v", name, err)
		}
	}
}

// A filing assignment is drawable against every frontier record kind and
// against nothing else: this package's own assessments are not frontier
// records, so there is no about edge one could carry.
func TestFilingIsAWorkRoleAndNotAReviewRole(t *testing.T) {
	if slices.Contains(Roles(), RoleFiling) {
		t.Fatal("filing must not be a review role: coverage would report every record as unreviewed for it")
	}
	if !slices.Contains(WorkRoles(), RoleFiling) || !ValidWorkRole(RoleFiling) {
		t.Fatal("filing must be a role an assignment can carry")
	}
	for _, kind := range []string{SubjectKindHypothesis, SubjectKindObservation,
		SubjectKindFinding, SubjectKindProposal} {
		if !WorkRoleApplies(kind, RoleFiling) {
			t.Errorf("a %s must be fileable", kind)
		}
	}
	if WorkRoleApplies(SubjectKindEvaluation, RoleFiling) {
		t.Error("an evaluation record is not a frontier record and carries no topic")
	}
}
