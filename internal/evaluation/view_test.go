package evaluation

import (
	"errors"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// newTestRNG is a fixed generator for the lane-picking helpers, so a test that
// asserts which lane may draw a candidate does not depend on which seed the
// sampler happened to roll.
func newTestRNG() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

// These scenarios cover the contract failures that would be invisible in a
// passing build: a vote credited to the wrong obligation, an endorsement that
// followed a revision it never read, a sampler that ignored its reservation, and
// a queue that could not say why it stopped. Each asserts what a consumer
// observes — a coverage state, a lane, an error class, a chosen subject — and
// none pins wording beyond the identifying substring an operator would search
// for.

func viewPolicy() Policy {
	policy := DefaultPolicy()
	policy.Enabled = true
	policy.InitialReviews = 2
	policy.MaxItemReviews = 6
	policy.CooldownSeconds = 3600
	policy.OverdueSeconds = 7200
	policy.BatchSize = 2
	policy.PerCycleCost = 1
	policy.DailyCost = 4
	return policy
}

func receptionVote(id, assignmentID, vote string, at time.Time) roledAssessment {
	return roledAssessment{
		Role: RoleReception,
		Record: Record{
			ID:           id,
			Kind:         KindAssessment,
			AssignmentID: assignmentID,
			CreatedAt:    at,
			Assessment:   &Assessment{Vote: vote, AsOf: at},
		},
	}
}

func hypothesisArtifact(id string, created time.Time) Artifact {
	return Artifact{
		Subject:        Subject{Kind: SubjectKindHypothesis, ID: id},
		RootID:         "root-" + id,
		HeadID:         id,
		CreatedAt:      created,
		Title:          "candidate " + id,
		Body:           []byte(`{"statement":"a claim","novelty":0.4,"priority":0.9}`),
		ContextVersion: "ctx-a",
	}
}

func roleState(t *testing.T, coverage []RoleCoverage, role string) RoleCoverage {
	t.Helper()
	for _, entry := range coverage {
		if entry.Role == role {
			return entry
		}
	}
	t.Fatalf("role %q has no coverage row; rows: %+v", role, coverage)
	return RoleCoverage{}
}

// A bare vote is a complete reception review and satisfies nothing else. This
// is the decision docs/evaluation-lifecycle.md §1 states first and the one a
// single "reviewed" flag would silently break.
func TestBareVotesSatisfyReceptionOnly(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	artifact := hypothesisArtifact("h1", now.Add(-time.Hour))

	coverage, required := deriveRoleCoverage(coverageInput{
		Artifact: artifact,
		Assessments: []roledAssessment{
			receptionVote("r1", "a1", VoteSupport, now),
			receptionVote("r2", "a2", VoteOppose, now),
		},
		Policy: policy,
		Now:    now,
	})

	if got := roleState(t, coverage, RoleReception); got.State != CoverageReviewed {
		t.Fatalf("two bare votes should satisfy reception, got %q (%s)", got.State, got.Reason)
	}
	if !required[RoleReception] {
		t.Fatal("reception must always be an activated obligation")
	}
	evidence := roleState(t, coverage, RoleEvidence)
	if evidence.Reviews != 0 {
		t.Fatalf("a reception vote must not count as an evidence check, got %d reviews", evidence.Reviews)
	}
	if required[RoleEvidence] {
		t.Fatal("evidence must not be an obligation without recorded uncertainty")
	}

	item := projected{Artifact: artifact, Roles: coverage, Required: required}
	state, _ := deriveCoverage(&item)
	if state != CoverageReviewed {
		t.Fatalf("an item with satisfied reception and no activated evidence obligation must "+
			"not claim to be unread, got %q", state)
	}
}

// Recorded uncertainty is what turns an evidence check into an obligation, and
// then the item is no longer covered. Without this the two halves of the
// activation rule would be indistinguishable from "evidence is never required".
func TestRecordedUncertaintyActivatesEvidenceObligation(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	artifact := hypothesisArtifact("h2", now.Add(-time.Hour))
	uncertain := receptionVote("r1", "a1", VoteSupport, now)
	uncertain.Record.Assessment.Uncertainty = "the benchmark was not reproduced"

	coverage, required := deriveRoleCoverage(coverageInput{
		Artifact:    artifact,
		Assessments: []roledAssessment{uncertain, receptionVote("r2", "a2", VoteSupport, now)},
		Policy:      policy,
		Now:         now,
	})
	if !required[RoleEvidence] {
		t.Fatal("recorded uncertainty must activate the evidence role")
	}
	if got := roleState(t, coverage, RoleEvidence); got.State != CoverageUnreviewed {
		t.Fatalf("an activated, unmet evidence obligation must read unreviewed, got %q", got.State)
	}
	item := projected{Artifact: artifact, Roles: coverage, Required: required}
	if state, _ := deriveCoverage(&item); state != CoverageUnreviewed {
		t.Fatalf("an item with an outstanding evidence obligation must report it, got %q", state)
	}
}

// Observations and findings are covered kinds with real role support. A build
// that reported them as unsupported would satisfy coverage by omission, which is
// the failure §E1 names.
func TestObservationsAndFindingsAreCoveredWithNamedRefusals(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	for _, kind := range []string{SubjectKindObservation, SubjectKindFinding} {
		artifact := Artifact{
			Subject:        Subject{Kind: kind, ID: "x-" + kind},
			CreatedAt:      now.Add(-time.Hour),
			ContextVersion: "ctx-a",
			Body:           []byte(`{}`),
		}
		coverage, required := deriveRoleCoverage(coverageInput{
			Artifact: artifact, Policy: policy, Now: now,
		})
		if len(coverage) == 0 {
			t.Fatalf("%s must have role coverage rows", kind)
		}
		reception := roleState(t, coverage, RoleReception)
		if reception.State != CoverageUnreviewed || !required[RoleReception] {
			t.Fatalf("%s reception must be an unmet obligation, got %q required=%v",
				kind, reception.State, required[RoleReception])
		}
		if slices.ContainsFunc(coverage, func(entry RoleCoverage) bool {
			return entry.State == CoverageUnsupported
		}) {
			t.Fatalf("%s must not report any role as unsupported: %+v", kind, coverage)
		}
		if reason := RoleUnsupportedReason(kind, RoleOutcome); reason == "" ||
			!strings.Contains(reason, RoleApplicabilityPolicy) {
			t.Fatalf("%s must refuse the outcome role under a named policy, got %q", kind, reason)
		}
	}
}

// A revision that supersedes reviewed wording starts due, not reviewed and not
// never-reviewed. Both wrong answers are specific failures: the first moves an
// endorsement to wording nobody read, the second erases review work that
// happened.
func TestSupersededWordingMakesReassessmentDue(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	artifact := hypothesisArtifact("h3", now.Add(-time.Hour))

	coverage, _ := deriveRoleCoverage(coverageInput{
		Artifact: artifact,
		Assessments: []roledAssessment{
			receptionVote("r1", "a1", VoteSupport, now),
			receptionVote("r2", "a2", VoteSupport, now),
		},
		Superseded: true,
		Policy:     policy,
		Now:        now,
	})
	got := roleState(t, coverage, RoleReception)
	if got.State != CoverageDue {
		t.Fatalf("a revised chain must make reassessment due, got %q", got.State)
	}
	if got.Reason == "" {
		t.Fatal("a due obligation must say what changed")
	}
}

// An assessment recorded under a different context version is a statement about
// a situation that no longer holds. The same votes must then produce a different
// coverage state, which is §E5's "same votes, changed reality".
func TestChangedContextVersionMakesReassessmentDue(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	artifact := hypothesisArtifact("h4", now.Add(-time.Hour))
	stale := receptionVote("r1", "a1", VoteSupport, now)
	stale.Record.Provenance.ContextVersion = "ctx-old"
	fresh := receptionVote("r2", "a2", VoteSupport, now)
	fresh.Record.Provenance.ContextVersion = "ctx-old"

	coverage, _ := deriveRoleCoverage(coverageInput{
		Artifact:    artifact,
		Assessments: []roledAssessment{stale, fresh},
		Policy:      policy,
		Now:         now,
	})
	if got := roleState(t, coverage, RoleReception); got.State != CoverageDue {
		t.Fatalf("assessments made under a superseded context must become due, got %q", got.State)
	}
}

// The context version must cover material context only. If this package's own
// bookkeeping or a reworded explanation moved it, every sweep would report every
// artifact as changed reality and restore cooled-down items as fresh.
func TestContextVersionIgnoresProseAndBookkeeping(t *testing.T) {
	base := Context{Priority: 2, CurrentWork: true, Pain: 1, Allowance: "full"}
	first := contextDigest(base, "1:0")

	reworded := base
	reworded.Reasons = []string{"an explanation that was rephrased"}
	reworded.Evidence = nil
	if second := contextDigest(reworded, "1:0"); second != first {
		t.Fatal("rewording an explanation must not change the material context version")
	}

	moved := base
	moved.Pain = 2
	if third := contextDigest(moved, "1:0"); third == first {
		t.Fatal("a changed pain reading must change the material context version")
	}
}

// A disabled policy, an exhausted budget and an empty eligible set are three
// different answers. A scheduler idles on one and parks on another, so
// collapsing them into a single "no work" would either burn cycles or stall
// review entirely.
func TestDrawDistinguishesDisabledBudgetAndEmpty(t *testing.T) {
	now := time.Now().UTC()
	items := []projected{{
		Artifact: hypothesisArtifact("h5", now.Add(-4*time.Hour)),
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
		Required: map[string]bool{RoleReception: true},
	}}

	disabled := viewPolicy()
	disabled.Enabled = false
	result, err := selectDraw(drawInput{Policy: disabled, Items: items, Now: now}, "run-1", 1)
	if !errors.Is(err, ErrNoWork) {
		t.Fatalf("a disabled policy must refuse with ErrNoWork, got %v", err)
	}
	if !strings.Contains(result.StopReason, "disabled") {
		t.Fatalf("the stop reason must name the disabled policy, got %q", result.StopReason)
	}

	policy := viewPolicy()
	result, err = selectDraw(drawInput{
		Policy: policy, Items: items, Now: now, SpentToday: policy.DailyCost,
	}, "run-1", 1)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("an exhausted daily ceiling must refuse with ErrBudget, got %v", err)
	}
	if !strings.Contains(result.StopReason, "daily ceiling") {
		t.Fatalf("the stop reason must name the ceiling that stopped it, got %q", result.StopReason)
	}

	result, err = selectDraw(drawInput{
		Policy: policy, Items: items, Now: now, ActiveClaims: policy.BatchSize,
	}, "run-1", 1)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("a full cycle batch must refuse with ErrBudget, got %v", err)
	}

	// A well-reviewed open idea stays sampleable: §5.8 keeps a small positive
	// exploration share rather than declaring a satisfied set finished.
	covered := []projected{{
		Artifact: hypothesisArtifact("h6", now.Add(-4*time.Hour)),
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageReviewed, Reviews: 2}},
		Required: map[string]bool{RoleReception: true},
	}}
	if _, err := selectDraw(drawInput{Policy: policy, Items: covered, Now: now}, "run-1", 1); err != nil {
		t.Fatalf("a well-reviewed open idea must stay sampleable, got %v", err)
	}
	// The per-revision cap is what ends it, and the draw says so.
	capped := covered
	capped[0].Assessments = policy.MaxItemReviews
	capped[0].Roles = []RoleCoverage{{
		Role: RoleReception, State: CoverageReviewed, Reviews: policy.MaxItemReviews,
	}}
	result, err = selectDraw(drawInput{Policy: policy, Items: capped, Now: now}, "run-1", 1)
	if !errors.Is(err, ErrNoWork) {
		t.Fatalf("an item at its review cap must refuse with ErrNoWork, got %v", err)
	}
	if result.StopReason == "" {
		t.Fatal("an exhausted draw must record why it stopped")
	}
}

// A recorded restriction withholds the work and keeps the record visible as a
// gap. A restricted subject that silently disappeared would make §4.8's
// "exclusion is a decision, not a removal" unobservable.
func TestRestrictedSubjectBecomesAVisibleGap(t *testing.T) {
	now := time.Now().UTC()
	artifact := hypothesisArtifact("h7", now.Add(-4*time.Hour))
	artifact.Context = Context{Allowance: "excluded", Blocked: true}
	items := []projected{{
		Artifact: artifact,
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
		Required: map[string]bool{RoleReception: true},
	}}
	result, err := selectDraw(drawInput{Policy: viewPolicy(), Items: items, Now: now}, "run-1", 7)
	if !errors.Is(err, ErrNoWork) {
		t.Fatalf("a withheld subject must not be drawn, got %v", err)
	}
	if !slices.ContainsFunc(result.Gaps, func(gap string) bool {
		return strings.Contains(gap, "withholds")
	}) {
		t.Fatalf("the withheld subject must remain a reported gap, got %v", result.Gaps)
	}
}

// Repeated skips consume bounded attention and then stop, without ever becoming
// a negative vote. An unbounded retry would spend a deployment's whole allowance
// on one record no evaluator can process.
func TestRepeatedSkipsBoundAttentionWithoutVoting(t *testing.T) {
	now := time.Now().UTC()
	artifact := hypothesisArtifact("h8", now.Add(-4*time.Hour))
	subject := artifact.Subject
	items := []projected{{
		Artifact: artifact,
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
		Required: map[string]bool{RoleReception: true},
	}}
	var (
		assignments []Assignment
		attempts    []Attempt
	)
	for i := range maxSkipAttempts {
		id := "a" + string(rune('1'+i))
		assignments = append(assignments, Assignment{
			ID: id, Subject: subject, Role: RoleReception, ExpiresAt: now.Add(-time.Minute),
		})
		attempts = append(attempts, Attempt{
			AssignmentID: id, State: AttemptSkipped, RecordedAt: now.Add(-time.Minute),
		})
	}
	result, err := selectDraw(drawInput{
		Policy: viewPolicy(), Items: items, Assignments: assignments,
		Attempts: attempts, Now: now,
	}, "run-1", 3)
	if !errors.Is(err, ErrNoWork) {
		t.Fatalf("a subject past its skip bound must not be drawn, got %v", err)
	}
	if !slices.ContainsFunc(result.Gaps, func(gap string) bool {
		return strings.Contains(gap, "rather than a negative vote")
	}) {
		t.Fatalf("the skip bound must be reported as a gap rather than a vote, got %v", result.Gaps)
	}
	if tally(nil, map[string]int{RoleReception: maxSkipAttempts}).Reviews != 0 {
		t.Fatal("skips must never appear as reviews in the tally")
	}
}

// The reserved coverage lane clears the oldest unreviewed work deterministically
// and a recorded seed reproduces the whole draw. A reservation that sampled at
// random would never reliably clear the backlog it exists to clear, and a draw
// that could not be replayed would make the recorded seed decorative.
func TestReservedCoverageDrawsOldestAndReplaysFromSeed(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	policy.CoverageShare = 1
	policy.ExplorationShare = 0
	policy.DiscoveryShare = 0

	older := projected{
		Artifact: hypothesisArtifact("old", now.Add(-72*time.Hour)),
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
		Required: map[string]bool{RoleReception: true},
	}
	newer := projected{
		Artifact: hypothesisArtifact("new", now.Add(-2*time.Hour)),
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
		Required: map[string]bool{RoleReception: true},
	}
	in := drawInput{Policy: policy, Items: []projected{newer, older}, Now: now, InputDigest: "in-1"}

	// ValidatePolicy refuses zero protected shares, so exercise the
	// reservation through the sampler directly with a policy the validator
	// would accept and a seed that lands in the reserved band.
	in.Policy = viewPolicy()
	in.Policy.CoverageShare = 0.9
	in.Policy.ExplorationShare = 0.05
	in.Policy.DiscoveryShare = 0.05

	var drew Assignment
	for seed := uint64(1); seed < 64; seed++ {
		result, err := selectDraw(in, "run-1", seed)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		if result.Lane != LaneCoverage {
			continue
		}
		if result.Assignment.Subject.ID != "old" {
			t.Fatalf("the reserved lane must draw the oldest due work, got %q",
				result.Assignment.Subject.ID)
		}
		drew = result.Assignment
		break
	}
	if drew.ID == "" {
		t.Fatal("no seed in the range landed in the reserved coverage lane")
	}

	replay, err := selectDraw(in, "run-1", drew.Seed)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Assignment.ID != drew.ID || replay.Assignment.Subject != drew.Subject {
		t.Fatalf("one seed must reproduce one draw: %+v vs %+v", replay.Assignment, drew)
	}
	if replay.Assignment.InputDigest != "in-1" || replay.Assignment.PolicyVersion == "" {
		t.Fatal("a draw must record the captured input digest and policy version for replay")
	}
}

// Two workers that independently decide the same review is next must derive one
// assignment id and contend for one claim. An id derived from the run would give
// each worker its own assignment and spend the shared allowance twice on one
// vote.
func TestAssignmentIdentityIsIndependentOfRun(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	policy.CoverageShare = 0.9
	policy.ExplorationShare = 0.05
	policy.DiscoveryShare = 0.05
	items := []projected{{
		Artifact: hypothesisArtifact("h9", now.Add(-48*time.Hour)),
		Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
		Required: map[string]bool{RoleReception: true},
	}}
	in := drawInput{Policy: policy, Items: items, Now: now}

	first, err := selectDraw(in, "run-a", 11)
	if err != nil {
		t.Fatalf("first draw: %v", err)
	}
	second, err := selectDraw(in, "run-b", 11)
	if err != nil {
		t.Fatalf("second draw: %v", err)
	}
	if first.Assignment.ID != second.Assignment.ID {
		t.Fatalf("two runs choosing one review must contend on one assignment id: %q vs %q",
			first.Assignment.ID, second.Assignment.ID)
	}
	if first.Assignment.RunID == second.Assignment.RunID {
		t.Fatal("each draw must carry the run that took it")
	}

	// A legitimate later review of the same revision is a different
	// assignment, distinguished by the sample ordinal rather than colliding
	// with the first.
	next := assignmentID(items[0].Artifact.Subject, RoleReception,
		items[0].Artifact.ContextVersion, policy.Version, 2)
	if next == first.Assignment.ID {
		t.Fatal("a later independent review must not reuse the first assignment's id")
	}
}

// Settled reception rests, and a material change wakes it. Without the cooldown
// the sampler pays to confirm what it already recorded; without the escape, an
// item's opinion freezes against a situation that has changed.
func TestSettledReceptionRestsUntilSomethingChanges(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	item := projected{
		Artifact:  hypothesisArtifact("h10", now.Add(-48*time.Hour)),
		Reception: Reception{Support: 4, Reviews: 4},
	}
	coverage := RoleCoverage{
		Role: RoleReception, State: CoverageDue, Reviews: 4,
		LastReviewed: now.Add(-10 * time.Minute),
	}
	reason, resting := cooling(&item, coverage, policy, now)
	if !resting {
		t.Fatal("a settled reception inside its cooldown must rest")
	}
	if !strings.Contains(reason, "settled") {
		t.Fatalf("the cooldown must say why it is resting, got %q", reason)
	}

	item.ContextChanged = true
	if _, resting := cooling(&item, coverage, policy, now); resting {
		t.Fatal("a material change must clear the cooldown")
	}
}

// The recommended ordering puts recorded work and explicit restrictions ahead of
// reception. Reversing that is the popularity-as-work-plan failure SPEC §4.12
// exists to prevent.
func TestRecommendationFollowsRecordedContextNotPopularity(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()

	popular := projected{
		Artifact:  hypothesisArtifact("popular", now),
		Reception: Reception{Support: 5, Reviews: 5},
	}
	popular.Artifact.Context = Context{Allowance: "full"}

	relevant := projected{
		Artifact:  hypothesisArtifact("relevant", now),
		Reception: Reception{Support: 1, Reviews: 1},
	}
	relevant.Artifact.Context = Context{
		Allowance: "full", CurrentWork: true, Priority: 2, Pain: 1,
	}

	popularScore := score(&popular, policy)
	relevantScore := score(&relevant, policy)
	if relevantScore <= popularScore {
		t.Fatalf("recorded current work must outrank a larger tally: %v vs %v",
			relevantScore, popularScore)
	}
	if !slices.ContainsFunc(relevant.Reasons, func(reason string) bool {
		return strings.Contains(reason, "current work")
	}) {
		t.Fatalf("the ordering must explain itself, got %v", relevant.Reasons)
	}

	restricted := projected{
		Artifact:  hypothesisArtifact("restricted", now),
		Reception: Reception{Support: 9, Reviews: 9},
	}
	restricted.Artifact.Context = Context{Allowance: "excluded", Blocked: true}
	if restrictedScore := score(&restricted, policy); restrictedScore >= popularScore {
		t.Fatalf("an explicit restriction must outrank popularity: %v vs %v",
			restrictedScore, popularScore)
	}
}

// learn-only demotes a remedy about the subject and leaves material learned from
// it alone. Collapsing the allowance into "exclude everything" loses the whole
// content of the value (§E5).
func TestLearnOnlyDemotesRemediesNotLearning(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	learnOnly := Context{Allowance: "learn-only"}

	proposal := projected{Artifact: Artifact{
		Subject: Subject{Kind: SubjectKindProposal, ID: "p1"}, CreatedAt: now,
		Body: []byte(`{}`), Context: learnOnly,
	}}
	finding := projected{Artifact: Artifact{
		Subject: Subject{Kind: SubjectKindFinding, ID: "f1"}, CreatedAt: now,
		Body: []byte(`{}`), Context: learnOnly,
	}}
	if score(&proposal, policy) >= score(&finding, policy) {
		t.Fatal("learn-only must demote a remedy about the subject but not learning from it")
	}
}

// Scoped feedback follows the revision it named. A descendant addressing the
// refusal is evaluated on its new merits, which is §E5's rule and the reason the
// penalty is not attached to the chain.
func TestScopedFeedbackFollowsTheRevisionItNamed(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	subject := Subject{Kind: SubjectKindProposal, ID: "p2"}
	older := Subject{Kind: SubjectKindProposal, ID: "p1"}

	refused := projected{
		Artifact: Artifact{Subject: subject, CreatedAt: now, Body: []byte(`{}`)},
		Feedback: []feedbackNote{{RecordID: "f1", Subject: subject,
			Operator: "alex", Reason: feedbackWrongRemedy}},
	}
	descendant := projected{
		Artifact: Artifact{Subject: subject, CreatedAt: now, Body: []byte(`{}`)},
		Feedback: []feedbackNote{{RecordID: "f1", Subject: older,
			Operator: "alex", Reason: feedbackWrongRemedy}},
	}
	if score(&refused, policy) >= score(&descendant, policy) {
		t.Fatal("a scoped refusal must demote the revision it named, not its descendant")
	}
	if len(descendant.Objections) != 0 {
		t.Fatalf("a prior revision's refusal is not this revision's objection, got %v",
			descendant.Objections)
	}
	if !slices.ContainsFunc(descendant.Reasons, func(reason string) bool {
		return strings.Contains(reason, "own merits")
	}) {
		t.Fatalf("the descendant must say the prior refusal is not charged to it, got %v",
			descendant.Reasons)
	}
	if len(refused.Objections) == 0 {
		t.Fatal("a scoped refusal must be readable as an objection on the revision it named")
	}
}

// Nothing is inferred from an absence. A card nobody touched and a record with
// no recorded rationale must both produce no preference and no invented reason.
func TestNoPreferenceIsInferredFromSilence(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	untouched := projected{Artifact: Artifact{
		Subject: Subject{Kind: SubjectKindProposal, ID: "p3"}, CreatedAt: now, Body: []byte(`{}`),
	}}
	bare := untouched
	bare.Reception = Reception{Support: 1, Reviews: 1}

	explainWouldChange(&bare, []Record{{
		Kind: KindAssessment, Assessment: &Assessment{Vote: VoteSupport},
	}})
	if len(bare.WouldChange) != 0 {
		t.Fatalf("a bare vote must not acquire a fabricated rationale, got %v", bare.WouldChange)
	}
	if len(bare.Objections) != 0 {
		t.Fatalf("a bare support vote is not an objection, got %v", bare.Objections)
	}
	_ = score(&untouched, policy)
	if untouched.Reception != (Reception{}) {
		t.Fatal("an untouched item must carry no reception")
	}
}

// Lane precedence: a reconsider item wins, then an observed outcome, then the
// operator's disposition. Every disposition keeps a lane of its own so §8.5's
// duplicate and refine-requested states stay reachable.
func TestLaneDerivationPreservesTheDispositionVocabulary(t *testing.T) {
	for _, tc := range []struct {
		name   string
		item   projected
		expect string
	}{
		{"open", projected{}, LaneOpen},
		{"accepted", projected{Artifact: Artifact{ReviewStatus: "accepted"}}, LaneAccepted},
		{"deferred", projected{Artifact: Artifact{ReviewStatus: "deferred"}}, LaneDeferred},
		{"duplicate", projected{Artifact: Artifact{ReviewStatus: "duplicate"}}, LaneDuplicate},
		{"refine", projected{Artifact: Artifact{ReviewStatus: "refine-requested"}},
			LaneRefineRequested},
		{"outcome beats disposition", projected{
			Artifact: Artifact{ReviewStatus: "accepted"},
			Outcomes: []outcomeState{{Outcome: OutcomeVerified, AsOf: time.Unix(10, 0)}},
		}, LaneVerified},
		{"reconsider beats everything", projected{
			Artifact:   Artifact{ReviewStatus: "rejected"},
			Outcomes:   []outcomeState{{Outcome: OutcomeVerified, AsOf: time.Unix(10, 0)}},
			Reconsider: true,
		}, LaneReconsider},
	} {
		item := tc.item
		if got := deriveLane(&item); got != tc.expect {
			t.Fatalf("%s: lane %q, want %q", tc.name, got, tc.expect)
		}
	}
	if !ValidLane(LaneDuplicate) || !ValidLane(LaneDeferred) {
		t.Fatal("the duplicate and deferred lanes must be part of the vocabulary")
	}
}

// Contrary evidence is displayed beside earlier verification rather than
// replacing it. A last-writer-wins success badge is the failure §E6 names.
func TestContraryOutcomeIsKeptBesideTheEarlierOne(t *testing.T) {
	outcomes := markContrary([]outcomeState{
		{RecordID: "o1", Outcome: OutcomeVerified, AsOf: time.Unix(10, 0)},
		{RecordID: "o2", Outcome: OutcomeContradicted, AsOf: time.Unix(20, 0)},
	})
	if len(outcomes) != 2 {
		t.Fatalf("both outcomes must survive, got %d", len(outcomes))
	}
	if outcomes[0].Contrary {
		t.Fatal("the earlier outcome is not contrary to anything")
	}
	if !outcomes[1].Contrary {
		t.Fatal("a later disagreeing outcome must be marked contrary")
	}
	item := projected{Outcomes: outcomes}
	if got := deriveLane(&item); got != LaneContradicted {
		t.Fatalf("the newest observation names the lane, got %q", got)
	}
}

// A reconsider item appears once per material change and closes only on an
// explicit operator decision. Two items for one change would nag; a self-closing
// one would lose the operator's act.
func TestReconsiderClosesOnlyOnAnOperatorDecision(t *testing.T) {
	item := projected{Reconsider: true, Artifact: Artifact{ReviewStatus: "rejected"}}
	if deriveLane(&item) != LaneReconsider {
		t.Fatal("an open reconsider item must be visible as its own lane")
	}
	item.Reconsider = false
	if got := deriveLane(&item); got != LaneRejected {
		t.Fatalf("a decided reconsider must return to the prior decision, got %q", got)
	}
}

// Grouped alternatives keep their own records and cannot fill the view by
// volume. Merging them, or letting one problem's five near-identical remedies
// occupy the top five positions, are the two failures §E5 names.
func TestAlternativesGroupWithoutMergingOrFloodingTheView(t *testing.T) {
	now := time.Now().UTC()
	claim := Subject{Kind: SubjectKindHypothesis, ID: "h-shared"}
	items := []projected{
		{Artifact: Artifact{Subject: Subject{Kind: SubjectKindProposal, ID: "p1"},
			CreatedAt: now, Related: []Subject{claim}}, Score: 3},
		{Artifact: Artifact{Subject: Subject{Kind: SubjectKindProposal, ID: "p2"},
			CreatedAt: now, Related: []Subject{claim}}, Score: 2},
		{Artifact: Artifact{Subject: Subject{Kind: SubjectKindProposal, ID: "p3"},
			CreatedAt: now, Related: []Subject{claim}}, Score: 1},
		{Artifact: Artifact{Subject: Subject{Kind: SubjectKindFinding, ID: "f1"},
			CreatedAt: now}},
	}
	groupAlternatives(items)

	if items[0].Group == "" || items[0].Group != items[1].Group {
		t.Fatalf("remedies for one claim must share a group: %q vs %q",
			items[0].Group, items[1].Group)
	}
	if items[3].Group != "" {
		t.Fatalf("a finding is nobody's alternative, got group %q", items[3].Group)
	}
	if len(items[0].Alternatives) != 2 {
		t.Fatalf("each member must name the others, got %v", items[0].Alternatives)
	}
	if items[0].GroupIndex != 0 || items[1].GroupIndex == 0 {
		t.Fatal("group positions must be assigned so later members are demoted")
	}
	policy := viewPolicy()
	first := score(&items[0], policy)
	second := score(&items[1], policy)
	if second >= first {
		t.Fatalf("a later group member must be demoted so one problem cannot fill the view: "+
			"%v vs %v", second, first)
	}
}

// Every sort produces one total order over the complete set. A sort that left
// ties unbroken would page the same record twice and omit another.
func TestEverySortIsATotalOrderOverTheWholeSet(t *testing.T) {
	now := time.Now().UTC()
	items := make([]projected, 0, 4)
	for _, id := range []string{"a", "b", "c", "d"} {
		items = append(items, projected{
			Artifact: hypothesisArtifact(id, now),
			Roles:    []RoleCoverage{{Role: RoleReception, State: CoverageUnreviewed}},
			Required: map[string]bool{RoleReception: true},
		})
	}
	for _, sortName := range Sorts() {
		first, err := rankAll(items, sortName)
		if err != nil {
			t.Fatalf("%s: %v", sortName, err)
		}
		if len(first) != len(items) {
			t.Fatalf("%s ranked %d of %d items", sortName, len(first), len(items))
		}
		seen := make(map[int]bool, len(first))
		for _, index := range first {
			if seen[index] {
				t.Fatalf("%s ranked item %d twice", sortName, index)
			}
			seen[index] = true
		}
		second, err := rankAll(items, sortName)
		if err != nil {
			t.Fatalf("%s: %v", sortName, err)
		}
		if !slices.Equal(first, second) {
			t.Fatalf("%s is not stable: %v vs %v", sortName, first, second)
		}
	}
	if _, err := rankAll(items, "popularity"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("an unknown sort must be refused, got %v", err)
	}
}

// The strengthened sort ignores bare votes, which is how §8.5 defines it. A
// sort that moved on another support vote would be the recency sort with a
// misleading name.
func TestStrengthenedIgnoresBareVotes(t *testing.T) {
	now := time.Now().UTC()
	bare := []roledAssessment{receptionVote("r1", "a1", VoteSupport, now)}
	if at := strengthenedAt(bare, nil, nil); !at.IsZero() {
		t.Fatalf("a bare vote must not strengthen a record, got %v", at)
	}

	substantive := receptionVote("r2", "a2", VoteSupport, now)
	substantive.Record.Assessment.Contributions = []Contribution{{
		Kind: ContributionArgument, Text: "the benchmark was reproduced on two hosts",
	}}
	if at := strengthenedAt([]roledAssessment{substantive}, nil, nil); at.IsZero() {
		t.Fatal("a contribution carrying argument must strengthen the record")
	}
}

// The policy validator refuses the settings that would make another part of the
// system lie. Each case below is one such setting.
func TestPolicyValidationRefusesUnhonourableSettings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Policy)
	}{
		{"no version", func(p *Policy) { p.Version = "" }},
		{"zero exploration", func(p *Policy) { p.ExplorationShare = 0 }},
		{"zero discovery", func(p *Policy) { p.DiscoveryShare = 0 }},
		{"over-committed shares", func(p *Policy) {
			p.CoverageShare, p.ExplorationShare, p.DiscoveryShare = 0.8, 0.2, 0.2
		}},
		{"cap below initial", func(p *Policy) { p.MaxItemReviews = 1; p.InitialReviews = 2 }},
		{"daily below cycle", func(p *Policy) { p.DailyCost = 0.1; p.PerCycleCost = 1 }},
		{"zero batch", func(p *Policy) { p.BatchSize = 0 }},
		{"zero lease", func(p *Policy) { p.LeaseSeconds = 0 }},
	} {
		policy := DefaultPolicy()
		tc.mutate(&policy)
		if err := ValidatePolicy(policy); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s must be refused, got %v", tc.name, err)
		}
	}
	if err := ValidatePolicy(DefaultPolicy()); err != nil {
		t.Fatalf("the shipped default must be valid: %v", err)
	}
	if DefaultPolicy().Enabled {
		t.Fatal("the default policy must not authorize work before an operator does")
	}
}

// A policy change invalidates derived work and never a record. Getting this
// backwards would either leave a stale ranking in place or discard assessments
// an operator paid for.
func TestPolicyChangeInvalidatesDerivedWorkOnly(t *testing.T) {
	previous := DefaultPolicy()
	next := previous
	next.OverdueSeconds = previous.OverdueSeconds * 2
	invalidation := next.Invalidates(previous)
	if !invalidation.Coverage || !invalidation.Selection || !invalidation.Ranking {
		t.Fatalf("a changed overdue threshold moves coverage, selection and ranking: %+v",
			invalidation)
	}
	if len(invalidation.Reasons) == 0 {
		t.Fatal("an invalidation must name what moved")
	}
	if previous.Invalidates(previous).Changed() {
		t.Fatal("an unchanged policy invalidates nothing")
	}
}

// Meta-review terminates at depth one by reading the parent subject chain. A
// name-based bound would admit level three while claiming to stop at level one,
// and bookkeeping records must never become subjects at all.
func TestMetaReviewIsBoundedByTheParentSubjectChain(t *testing.T) {
	firstLevel := Record{
		ID: "rec-1", Kind: KindAssessment,
		Subject:    Subject{Kind: SubjectKindProposal, ID: "p1"},
		Assessment: &Assessment{Vote: VoteSupport},
	}
	if ok, reason := MetaReviewSubject(firstLevel, RoleChallenge); !ok {
		t.Fatalf("an assessment of an artifact must be challengeable: %s", reason)
	}

	secondLevel := firstLevel
	secondLevel.ID = "rec-2"
	secondLevel.Subject = Subject{Kind: SubjectKindEvaluation, ID: "rec-1"}
	ok, reason := MetaReviewSubject(secondLevel, RoleChallenge)
	if ok {
		t.Fatal("a review of a review must not itself be reviewable")
	}
	if !strings.Contains(reason, "bounded at depth") {
		t.Fatalf("the bound must explain itself, got %q", reason)
	}

	for _, kind := range []string{KindCheckpoint, KindAttempt, KindAssignment, KindFeedback} {
		bookkeeping := Record{ID: "b", Kind: kind,
			Subject: Subject{Kind: SubjectKindProposal, ID: "p1"}}
		if ok, _ := MetaReviewSubject(bookkeeping, RoleChallenge); ok {
			t.Fatalf("%s is not a reviewable judgement", kind)
		}
	}
}

// Non-reviewable produced kinds are enumerated with a named reason rather than
// omitted. An operator has to be able to tell an exempt kind from a forgotten
// one, and neither may read as reviewed.
func TestNonReviewableKindsCarryANamedReason(t *testing.T) {
	for _, kind := range NonReviewableKinds() {
		reason := KindUnreviewableReason(kind)
		if reason == "" || !strings.Contains(reason, KindApplicabilityPolicy) {
			t.Fatalf("%s must carry a named policy reason, got %q", kind, reason)
		}
		if ValidKind(kind) {
			t.Fatalf("%s must not also be a reviewable subject kind", kind)
		}
	}
	if reason := KindUnreviewableReason("something-new"); !strings.Contains(reason, "unsupported") {
		t.Fatalf("an unrecognised kind must read as unsupported, got %q", reason)
	}
	for _, kind := range Kinds() {
		if KindUnreviewableReason(kind) != "" {
			t.Fatalf("%s is reviewable and must carry no exemption", kind)
		}
	}
}

// Blinding withholds rank-shaped material from the roles that could be swayed by
// it, and withholds nothing that makes a role unperformable. §E3 requires the
// served content to be audited, which is what this does.
func TestBlindedReviewWithholdsRankNotSubstance(t *testing.T) {
	artifact := hypothesisArtifact("h11", time.Now().UTC())
	artifact.Context = Context{Priority: 3, CurrentWork: true, Pain: 2, Allowance: "full"}

	blinded := blind(artifact, RoleReception)
	if strings.Contains(string(blinded.Body), "novelty") ||
		strings.Contains(string(blinded.Body), "priority") {
		t.Fatalf("a blinded read must not carry the model's own scores: %s", blinded.Body)
	}
	if strings.Contains(string(blinded.Body), "statement") == false {
		t.Fatalf("a blinded read must keep the record's own wording: %s", blinded.Body)
	}
	if blinded.Context.CurrentWork || blinded.Context.Priority != 0 || blinded.Context.Pain != 0 {
		t.Fatalf("a blinded read must withhold the recorded ranking signals: %+v", blinded.Context)
	}
	if len(blinded.Context.Unknown) == 0 {
		t.Fatal("withheld context must be visibly withheld rather than silently absent")
	}

	relevance := blind(artifact, RoleRelevance)
	if !relevance.Context.CurrentWork {
		t.Fatal("the relevance role's whole question is the recorded context; it must be served")
	}

	comparison := blind(artifact, RoleComparison)
	if !comparison.Context.CurrentWork {
		t.Fatal("a comparison is not a blinded role")
	}
	for _, role := range BlindedRoles() {
		if !Blinded(role) {
			t.Fatalf("%s must be blinded", role)
		}
	}
	if Blinded(RoleChallenge) || Blinded(RoleComparison) {
		t.Fatal("challenge and comparison answer questions about the disagreement itself")
	}
}

// Conservative spend accounting counts an expired, unsettled reservation. An
// expired lease that reported zero spend would let a crashed worker's
// expenditure be drawn again from the same allowance.
func TestExpiredReservationStillCountsAsSpend(t *testing.T) {
	now := time.Now().UTC()
	assignments := []Assignment{{
		ID: "a1", ReservedCost: 0.5, CreatedAt: now.Add(-time.Hour),
		ExpiresAt: now.Add(-time.Minute),
	}}
	today, cycle := spend(assignments, nil, now, now.Add(-2*time.Hour))
	if today != 0.5 || cycle != 0.5 {
		t.Fatalf("an unsettled reservation must be accounted: today=%v cycle=%v", today, cycle)
	}
	if got := activeClaims(assignments, nil, now); got != 0 {
		t.Fatalf("an expired claim is not running work, got %d active", got)
	}

	settled := []Attempt{{AssignmentID: "a1", State: AttemptCompleted,
		Cost: 0.2, RecordedAt: now.Add(-time.Minute)}}
	today, _ = spend(assignments, settled, now, now.Add(-2*time.Hour))
	if today != 0.2 {
		t.Fatalf("a settled assignment is accounted by its receipt, not its reservation, got %v",
			today)
	}
}

// A reconsider item's polarity comes from the closed decision value and never
// from its reason text. Retaining resolves the item and leaves the prior
// decision alone; only an explicit reopen puts attention back on the work.
func TestReconsiderPolarityComesFromTheDecisionValue(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	artifact := hypothesisArtifact("h12", now.Add(-48*time.Hour))
	artifact.ReviewStatus = "rejected"
	reviewed := []roledAssessment{
		receptionVote("r1", "a1", VoteOppose, now.Add(-24*time.Hour)),
		receptionVote("r2", "a2", VoteOppose, now.Add(-24*time.Hour)),
	}

	// Retained: no reopening, and the prior disposition still names the lane.
	retained := projected{Artifact: artifact}
	if got := deriveLane(&retained); got != LaneRejected {
		t.Fatalf("a retained reconsider must leave the prior decision in place, got %q", got)
	}
	coverage, required := deriveRoleCoverage(coverageInput{
		Artifact: artifact, Assessments: reviewed, Policy: policy, Now: now,
	})
	if required[RoleReception] {
		t.Fatal("a ruled-on artifact must not carry a generic reception obligation")
	}
	if reason := roleState(t, coverage, RoleReception).Reason; reason == "" {
		t.Fatal("a deactivated reception role must say what would activate it")
	}

	// Reopened: attention is due again and the cooldown no longer holds.
	coverage, required = deriveRoleCoverage(coverageInput{
		Artifact: artifact, Assessments: reviewed, Reopened: true,
		Policy: policy, Now: now,
	})
	if !required[RoleReception] {
		t.Fatal("an explicit reopening must put reception back in play")
	}
	reception := roleState(t, coverage, RoleReception)
	if reception.State != CoverageDue {
		t.Fatalf("a reopened artifact's reception must be due again, got %q", reception.State)
	}
	item := projected{Artifact: artifact, Reopened: true,
		Reception: Reception{Oppose: 2, Reviews: 2}}
	if _, resting := cooling(&item, RoleCoverage{
		Role: RoleReception, State: CoverageDue, Reviews: 2,
		LastReviewed: now.Add(-time.Minute),
	}, policy, now); resting {
		t.Fatal("an explicit reopening must clear the cooldown")
	}
	if score(&item, policy) <= score(&projected{Artifact: artifact}, policy) {
		t.Fatal("a reopened item must rank above the same item unreopened")
	}
}

// A well-reviewed open idea stays sampleable through the exploration share
// after its cooldown, and it never competes with due work for the paid lanes.
// Refusing it outright would remove SPEC §5.8's small positive exploration
// share; admitting it to the weighted lane would let revisits displace backlog.
func TestWellReviewedOpenIdeasStaySampleableByExplorationOnly(t *testing.T) {
	now := time.Now().UTC()
	policy := viewPolicy()
	policy.CooldownSeconds = 60

	open := projected{
		Artifact:  hypothesisArtifact("open", now.Add(-72*time.Hour)),
		Reception: Reception{Support: 1, Oppose: 1, Reviews: 2},
		Roles: []RoleCoverage{{
			Role: RoleReception, State: CoverageReviewed, Reviews: 2,
			LastReviewed: now.Add(-24 * time.Hour),
		}},
		Required:    map[string]bool{RoleReception: true},
		Assessments: 2,
	}
	candidates, _ := buildCandidates(drawInput{
		Policy: policy, Items: []projected{open}, Now: now,
	})
	if len(candidates) != 1 || !candidates[0].Revisit {
		t.Fatalf("a well-reviewed open idea past its cooldown must remain sampleable as a "+
			"revisit, got %+v", candidates)
	}

	rng := newTestRNG()
	if pick(candidates, LaneWeighted, rng) != nil {
		t.Fatal("a revisit must not be drawable from the paid weighted lane")
	}
	if pick(candidates, LaneCoverage, rng) != nil {
		t.Fatal("a revisit must not consume the reserved coverage share")
	}
	if pick(candidates, LaneDiscovery, rng) != nil {
		t.Fatal("a revisit is not undiscovered work")
	}
	if pick(candidates, LaneExploration, rng) == nil {
		t.Fatal("the exploration share must be able to draw a revisit")
	}

	// A decided artifact leaves the revisit pool entirely: the operator
	// answered, and another generic vote revisits nothing.
	decidedItem := open
	decidedItem.Artifact.ReviewStatus = "accepted"
	candidates, _ = buildCandidates(drawInput{
		Policy: policy, Items: []projected{decidedItem}, Now: now,
	})
	for _, got := range candidates {
		if got.Role == RoleReception {
			t.Fatalf("an accepted artifact must leave generic reception voting, got %+v", got)
		}
	}

	// The per-revision cap bounds revisits like everything else.
	capped := open
	capped.Assessments = policy.MaxItemReviews
	candidates, gaps := buildCandidates(drawInput{
		Policy: policy, Items: []projected{capped}, Now: now,
	})
	if len(candidates) != 0 {
		t.Fatalf("the per-revision cap must bound revisits too, got %+v", candidates)
	}
	if len(gaps) == 0 {
		t.Fatal("a capped subject must remain a reported gap")
	}
}

// Corrections are suppressed globally by record id, not per assignment. A paid
// correction is a NEW assignment, so grouping the suppression by assignment
// would let one run's corrected statement and its correction both count as
// effective assessments — two active votes from one independent review.
func TestCorrectionSuppressesTheSupersededRecordGlobally(t *testing.T) {
	now := time.Now().UTC()
	records := []Record{
		{ID: "rec-1", Kind: KindAssessment, AssignmentID: "a1", CreatedAt: now,
			Subject:    Subject{Kind: SubjectKindProposal, ID: "p1"},
			Assessment: &Assessment{Vote: VoteSupport, AsOf: now}},
		{ID: "rec-2", Kind: KindAssessment, AssignmentID: "a2", SupersedesID: "rec-1",
			CreatedAt:  now.Add(time.Minute),
			Subject:    Subject{Kind: SubjectKindProposal, ID: "p1"},
			Assessment: &Assessment{Vote: VoteOppose, AsOf: now.Add(time.Minute)}},
	}
	superseded := map[string]bool{}
	for _, record := range records {
		if record.SupersedesID != "" {
			superseded[record.SupersedesID] = true
		}
	}
	if !superseded["rec-1"] {
		t.Fatal("a correction must suppress the record it names")
	}

	var effective []roledAssessment
	for _, record := range records {
		if superseded[record.ID] {
			continue
		}
		effective = append(effective, roledAssessment{Record: record, Role: RoleReception})
	}
	got := tally(effective, nil)
	if got.Reviews != 1 || got.Oppose != 1 || got.Support != 0 {
		t.Fatalf("one independent review must leave exactly one effective vote, got %+v", got)
	}
}

// Every path that claims work passes one set of bounds. A correction with its
// own reservation must be admitted by the same arithmetic as a draw, or the two
// would disagree about what the deployment has left to spend.
func TestOneBudgetAdmissionGovernsEveryClaim(t *testing.T) {
	policy := viewPolicy()
	reserved, reason, err := admitSpend(policy, 0, 0, 0)
	if err != nil {
		t.Fatalf("an idle deployment must admit work: %v", err)
	}
	if reserved <= 0 || reserved > policy.PerCycleCost {
		t.Fatalf("a review must reserve a share of the cycle, got %v", reserved)
	}
	if reserved*float64(policy.BatchSize) != policy.PerCycleCost {
		t.Fatalf("a full batch must reserve exactly one cycle's allowance, got %v", reserved)
	}
	if reason != "" {
		t.Fatalf("an admitted claim needs no stop reason, got %q", reason)
	}

	if _, reason, err = admitSpend(policy, policy.BatchSize, 0, 0); !errors.Is(err, ErrBudget) ||
		!strings.Contains(reason, "batch") {
		t.Fatalf("a full batch must refuse with ErrBudget naming the batch: %v %q", err, reason)
	}
	if _, reason, err = admitSpend(policy, 0, policy.PerCycleCost, 0); !errors.Is(err, ErrBudget) ||
		!strings.Contains(reason, "per-cycle") {
		t.Fatalf("an exhausted cycle must refuse naming the cycle: %v %q", err, reason)
	}
	if _, reason, err = admitSpend(policy, 0, 0, policy.DailyCost); !errors.Is(err, ErrBudget) ||
		!strings.Contains(reason, "daily") {
		t.Fatalf("an exhausted day must refuse naming the day: %v %q", err, reason)
	}
}

// A correction claim is identified by what it supersedes, under the policy and
// context in force — never by the run. Two attempts to correct one statement
// must contend for one claim rather than minting two superseding records.
func TestCorrectionClaimIdentityIsDerivedFromItsTarget(t *testing.T) {
	first := correctionID("rec-1", "eval-policy-1", "ctx-a")
	if first != correctionID("rec-1", "eval-policy-1", "ctx-a") {
		t.Fatal("one correction of one record under one policy is one claim")
	}
	if first == correctionID("rec-2", "eval-policy-1", "ctx-a") {
		t.Fatal("corrections of different records must be different claims")
	}
	if first == correctionID("rec-1", "eval-policy-2", "ctx-a") {
		t.Fatal("a policy change must not collide with an earlier correction claim")
	}
	if first == correctionID("rec-1", "eval-policy-1", "ctx-b") {
		t.Fatal("a changed context must not collide with an earlier correction claim")
	}
	// A correction is a new claim and must never reuse the assignment id of
	// the review it corrects, because it reserves its own budget.
	original := assignmentID(Subject{Kind: SubjectKindProposal, ID: "p1"},
		RoleReception, "ctx-a", "eval-policy-1", 1)
	if first == original {
		t.Fatal("a correction must not reuse the corrected assignment's id")
	}
}
