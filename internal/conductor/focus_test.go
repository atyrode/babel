package conductor_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// focusFixture is a real frontier and a real Reality Ledger over temporary
// directories.
//
// Both stores are real on purpose. What is under test is whether a recorded
// policy reaches candidate selection and whether the candidate survives being
// refused, and a fake frontier could not answer the second question at all —
// "the record is still there" is a claim about a store, not about a slice the
// test filled in itself.
type focusFixture struct {
	t        *testing.T
	front    *frontier.Store
	ledger   *reality.Store
	subject  reality.Entity
	observed time.Time
}

const focusAlias = "the-abandoned-project"

func newFocusFixture(t *testing.T) *focusFixture {
	t.Helper()
	ctx := t.Context()
	front, err := frontier.Open(t.TempDir())
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	t.Cleanup(func() { front.Close() })
	ledger, err := reality.Open(t.TempDir())
	if err != nil {
		t.Fatalf("reality.Open: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })

	if _, err := ledger.PutFocusRules(ctx, reality.DefaultFocusRules()); err != nil {
		t.Fatalf("PutFocusRules: %v", err)
	}
	subject, err := ledger.CreateEntity(ctx, reality.EntityInput{
		Kind:    reality.EntityProject,
		Payload: reality.EntityPayload{DisplayName: "a project nobody maintains"},
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	if _, err := ledger.AddAlias(ctx, reality.AliasInput{
		EntityID: subject.ID,
		Kind:     reality.AliasChatTerm,
		Payload:  reality.AliasPayload{Value: focusAlias},
	}); err != nil {
		t.Fatalf("AddAlias: %v", err)
	}
	return &focusFixture{t: t, front: front, ledger: ledger, subject: subject,
		observed: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
}

// candidate plants one unexplored hypothesis labelled with the alias that
// resolves to the fixture's subject.
func (f *focusFixture) candidate(statement string, labels ...string) frontier.Hypothesis {
	f.t.Helper()
	record, err := f.front.CreateHypothesis(f.t.Context(), frontier.HypothesisInput{
		RunID: "run-focus",
		Payload: frontier.HypothesisPayload{
			Statement:         statement,
			ProvisionalLabels: labels,
			Priority:          0.5,
		},
	})
	if err != nil {
		f.t.Fatalf("CreateHypothesis: %v", err)
	}
	return record
}

// policy records the operator's stated analysis policy for the subject.
func (f *focusFixture) policy(value string) reality.Fact {
	f.t.Helper()
	fact, _, err := f.ledger.AssertFact(f.t.Context(), reality.FactInput{
		SubjectID:   f.subject.ID,
		Predicate:   reality.PredicateAnalysisPolicy,
		Value:       reality.FactValue{Kind: reality.ValueEnum, Enum: value},
		ValidFrom:   f.observed,
		ObservedAt:  f.observed,
		Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: "operator", At: f.observed},
		Confidence:  reality.ConfidenceHigh,
		Sensitivity: reality.SensitivityRoutine,
		Note:        "the operator stopped maintaining this project",
	})
	if err != nil {
		f.t.Fatalf("AssertFact(%s): %v", value, err)
	}
	return fact
}

func (f *focusFixture) rung(maxRoots int) *conductor.ConsolidationRung {
	return conductor.NewConsolidationRung(f.front, fakeOrigins{},
		reality.NewAttention(f.ledger, reality.DefaultFocusRules().Version), maxRoots)
}

// Selection differs per allowance rather than per boolean.
//
// This is the property the whole wiring exists for and the one a shortcut
// would destroy. §4.8's values are not "allowed" and "not allowed": a subject
// at learn-only is one Babel may still read the corpus about and may not
// spend subject-specific work on, and one at no-code-investigation is
// narrower again. A consolidation cycle seeds an exploration rooted at the
// candidate, so it asks for subject-specific work — and the three restricted
// values answer that question the same way while answering the corpus
// question differently, which is checked here in the same table so the two
// cannot drift apart.
func TestFocusDecidesSelectionPerAllowance(t *testing.T) {
	cases := []struct {
		policy    string
		allowance reality.Allowance
		rule      string
		drawable  bool
		inCorpus  bool
	}{
		{reality.PolicyNormal, reality.AllowanceFull, "", true, true},
		{reality.PolicyLearnOnly, reality.AllowanceLearnOnly, "policy-learn-only", false, true},
		{reality.PolicyNoCodeInvestigation, reality.AllowanceNoCodeInvestigation, "policy-no-code-investigation", false, false},
		{reality.PolicyExcluded, reality.AllowanceExcluded, "policy-excluded", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.policy, func(t *testing.T) {
			f := newFocusFixture(t)
			planted := f.candidate("the pipeline retries forever", focusAlias)
			f.policy(tc.policy)

			assignment, err := f.rung(5).Draw(t.Context(),
				conductor.DrawRequest{RunID: "run-1", At: f.observed})
			switch {
			case tc.drawable:
				if err != nil {
					t.Fatalf("Draw: %v", err)
				}
				if !slices.Contains(assignment.Roots, planted.ID) {
					t.Errorf("a subject at %s yielded roots %v, want the candidate",
						tc.policy, assignment.Roots)
				}
			default:
				if !errors.Is(err, conductor.ErrNoWork) {
					t.Fatalf("a subject at %s drew %+v (err %v), want no work",
						tc.policy, assignment, err)
				}
			}

			// The same policy, asked the corpus question instead. This is
			// where learn-only parts company with the values below it.
			attention := reality.NewAttention(f.ledger, reality.DefaultFocusRules().Version)
			corpus, err := attention.Admit(t.Context(), reality.AdmitRequest{
				Names: []string{focusAlias},
				Work:  reality.WorkCorpusReading,
				AsOf:  f.observed,
			})
			if err != nil {
				t.Fatalf("Admit(corpus-reading): %v", err)
			}
			if corpus.Allowance != tc.allowance {
				t.Errorf("the ledger decides %s, want %s", corpus.Allowance, tc.allowance)
			}
			if corpus.Permitted != tc.inCorpus {
				t.Errorf("%s leaves the subject's sessions in the corpus: %v, want %v",
					tc.policy, corpus.Permitted, tc.inCorpus)
			}
			if corpus.Deciding.RuleName != tc.rule {
				t.Errorf("decided by rule %q, want %q", corpus.Deciding.RuleName, tc.rule)
			}
		})
	}
}

// An operator who wants nothing more said about a project has not asked for
// anything to be forgotten. §4.8 and §5.2 both forbid removing a candidate,
// and the whole argument for recording intent instead of deleting records is
// that it is reversible — so the candidate is still on the frontier,
// unexplored, with its wording intact, and lifting the policy brings it back
// with nothing to restore.
func TestExclusionSkipsACandidateWithoutRemovingIt(t *testing.T) {
	f := newFocusFixture(t)
	planted := f.candidate("the pipeline retries forever", focusAlias)
	fact := f.policy(reality.PolicyExcluded)

	if _, err := f.rung(5).Draw(t.Context(),
		conductor.DrawRequest{RunID: "run-1", At: f.observed}); !errors.Is(err, conductor.ErrNoWork) {
		t.Fatalf("Draw over an excluded subject returned %v, want ErrNoWork", err)
	}

	kept, err := f.front.Hypothesis(t.Context(), planted.ID)
	if err != nil {
		t.Fatalf("the excluded candidate is gone from the store: %v", err)
	}
	if kept.Payload.Statement != planted.Payload.Statement {
		t.Errorf("the candidate's wording is now %q, want %q",
			kept.Payload.Statement, planted.Payload.Statement)
	}
	open, err := f.front.Unexplored(t.Context(), 0)
	if err != nil {
		t.Fatalf("Unexplored: %v", err)
	}
	if len(open) != 1 || open[0].ID != planted.ID {
		t.Errorf("the frontier holds %d unexplored candidates, want the excluded one still there", len(open))
	}

	// Reversal is a superseding fact, not a restore.
	later := f.observed.Add(24 * time.Hour)
	if _, err := f.ledger.SupersedeFact(t.Context(), reality.SupersedeInput{
		PriorID: fact.ID,
		Fact: reality.FactInput{
			SubjectID:   f.subject.ID,
			Predicate:   reality.PredicateAnalysisPolicy,
			Value:       reality.FactValue{Kind: reality.ValueEnum, Enum: reality.PolicyNormal},
			ValidFrom:   later,
			ObservedAt:  later,
			Authority:   reality.Authority{Kind: reality.AuthorityOperator, ID: "operator", At: later},
			Confidence:  reality.ConfidenceHigh,
			Sensitivity: reality.SensitivityRoutine,
			Note:        "picked the project back up",
		},
	}); err != nil {
		t.Fatalf("SupersedeFact: %v", err)
	}
	assignment, err := f.rung(5).Draw(t.Context(), conductor.DrawRequest{RunID: "run-2", At: later})
	if err != nil {
		t.Fatalf("Draw after the policy was lifted: %v", err)
	}
	if !slices.Contains(assignment.Roots, planted.ID) {
		t.Errorf("lifting the policy left roots %v, want the candidate back", assignment.Roots)
	}
}

// "Why was this skipped" has an answer, and the answer names the rule and the
// fact it matched.
//
// A deferral that recorded only that something was withheld would be worse
// than none: the operator would see a quiet loop and have nothing to argue
// with. §4.8 requires a deterministic deferral to freeze the context that
// caused it, so the refusal is an immutable context snapshot against the
// candidate, naming the policy version, the entity, the rule and the fact.
func TestASkippedCandidateRecordsAReadableReason(t *testing.T) {
	f := newFocusFixture(t)
	planted := f.candidate("the pipeline retries forever", focusAlias)
	fact := f.policy(reality.PolicyExcluded)

	if _, err := f.rung(5).Draw(t.Context(),
		conductor.DrawRequest{RunID: "run-1", At: f.observed}); !errors.Is(err, conductor.ErrNoWork) {
		t.Fatalf("Draw returned %v, want ErrNoWork", err)
	}

	snapshots, err := f.ledger.Snapshots(t.Context(), planted.ID)
	if err != nil {
		t.Fatalf("Snapshots: %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("the skip left %d context snapshots, want exactly one", len(snapshots))
	}
	got := snapshots[0]
	if got.Allowance != reality.AllowanceExcluded {
		t.Errorf("the snapshot records %s, want %s", got.Allowance, reality.AllowanceExcluded)
	}
	if got.RuleSetVersion != reality.DefaultFocusRules().Version {
		t.Errorf("the snapshot names policy version %d", got.RuleSetVersion)
	}
	if !slices.Contains(got.FactIDs, fact.ID) {
		t.Errorf("the snapshot cites %v, want the analysis-policy fact %s", got.FactIDs, fact.ID)
	}
	if len(got.Entities) != 1 || got.Entities[0].CanonicalID != f.subject.ID {
		t.Errorf("the snapshot resolved %+v, want the subject %s", got.Entities, f.subject.ID)
	}
	if len(got.Payload.Decisions) != 1 || got.Payload.Decisions[0].RuleName != "policy-excluded" {
		t.Fatalf("the snapshot's decisions are %+v, want one taken by policy-excluded",
			got.Payload.Decisions)
	}
	inputs := got.Payload.Decisions[0].Inputs
	if len(inputs) != 1 || inputs[0].FactID != fact.ID ||
		inputs[0].Predicate != reality.PredicateAnalysisPolicy || inputs[0].Value != reality.PolicyExcluded {
		t.Errorf("the decision read %+v, want the analysis-policy fact that caused it", inputs)
	}

	// The prose carries the same answer for a reader who has only the note,
	// and carries no identifier: the ledger's credential detector runs over
	// prose, and a random hex identifier in it would refuse the write.
	note := got.Payload.Note
	for _, want := range []string{
		"consolidation cycle",
		`rule "policy-excluded"`,
		"analysis-policy=excluded",
		"subject-specific work is withheld",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("the recorded reason %q does not mention %q", note, want)
		}
	}
	for _, forbidden := range []string{fact.ID, f.subject.ID, planted.ID} {
		if strings.Contains(note, forbidden) {
			t.Errorf("the recorded reason %q embeds the identifier %q, which the "+
				"credential detector would refuse", note, forbidden)
		}
	}
}

// A candidate whose labels name no entity is untouched by any policy.
//
// This is the failure direction the gate has to have. An operator's aliases
// are their own record of which spellings mean which subject, and a gate that
// inferred a subject from prose would silence candidates about projects
// nobody ever recorded an intent about.
func TestACandidateNamingNoSubjectIsNotWithheld(t *testing.T) {
	f := newFocusFixture(t)
	planted := f.candidate("something about a project the ledger never heard of", "unheard-of")
	f.policy(reality.PolicyExcluded)

	assignment, err := f.rung(5).Draw(t.Context(),
		conductor.DrawRequest{RunID: "run-1", At: f.observed})
	if err != nil {
		t.Fatalf("Draw: %v", err)
	}
	if !slices.Contains(assignment.Roots, planted.ID) {
		t.Errorf("roots are %v, want the unrelated candidate", assignment.Roots)
	}
}

// The depth a status view reports is the backlog the loop would actually
// draw, and the note separates a subject nothing may be spent on from one
// that is merely off limits for a consolidation cycle.
//
// A view that reported the raw backlog would tell an operator five candidates
// are waiting beside a loop that draws none of them, and a view that reported
// only the drawable count would hide that a subject at learn-only is still
// material Babel reads.
func TestDepthReportsTheDrawableBacklogAndWhatFocusHolds(t *testing.T) {
	f := newFocusFixture(t)
	f.candidate("a claim about the abandoned project", focusAlias)
	f.candidate("a claim about something else", "unheard-of")

	for _, tc := range []struct {
		policy string
		want   int
		phrase string
	}{
		{reality.PolicyLearnOnly, 1, "1 withheld by recorded focus (0 excluded from analysis entirely)"},
		{reality.PolicyExcluded, 1, "1 withheld by recorded focus (1 excluded from analysis entirely)"},
	} {
		t.Run(tc.policy, func(t *testing.T) {
			fresh := newFocusFixture(t)
			fresh.candidate("a claim about the abandoned project", focusAlias)
			fresh.candidate("a claim about something else", "unheard-of")
			fresh.policy(tc.policy)

			depth, err := fresh.rung(5).Depth(t.Context())
			if err != nil {
				t.Fatalf("Depth: %v", err)
			}
			if depth.Waiting != tc.want {
				t.Errorf("depth reports %d waiting, want %d", depth.Waiting, tc.want)
			}
			if !strings.Contains(depth.Note, tc.phrase) {
				t.Errorf("depth note is %q, want it to contain %q", depth.Note, tc.phrase)
			}
		})
	}
}

// A machine with no ledger, or one whose operator installed no policy
// version, withholds nothing. An absent policy is not a refusal, and reading
// it as one would make an unopenable database look like a decision.
func TestNoInstalledPolicyWithholdsNothing(t *testing.T) {
	ctx := t.Context()
	front, err := frontier.Open(t.TempDir())
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	t.Cleanup(func() { front.Close() })
	ledger, err := reality.Open(t.TempDir())
	if err != nil {
		t.Fatalf("reality.Open: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })
	planted, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "run-focus",
		Payload: frontier.HypothesisPayload{Statement: "a claim"},
	})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}

	for name, focus := range map[string]conductor.Focus{
		"no ledger at all":     nil,
		"no installed version": reality.NewAttention(ledger, reality.DefaultFocusRules().Version),
	} {
		t.Run(name, func(t *testing.T) {
			rung := conductor.NewConsolidationRung(front, fakeOrigins{}, focus, 5)
			assignment, err := rung.Draw(ctx, conductor.DrawRequest{RunID: "run-1"})
			if err != nil {
				t.Fatalf("Draw: %v", err)
			}
			if !slices.Contains(assignment.Roots, planted.ID) {
				t.Errorf("roots are %v, want the candidate", assignment.Roots)
			}
		})
	}
}

// An allowance says what it permits, and the three work kinds are not a
// ranking dressed up as three names: each pair of adjacent allowances differs
// on exactly one of them, which is what makes the vocabulary worth having.
func TestAllowancePermissions(t *testing.T) {
	want := map[reality.Allowance][]reality.Work{
		reality.AllowanceFull:                {reality.WorkSynthesis, reality.WorkCorpusReading, reality.WorkSubjectSpecific},
		reality.AllowanceLearnOnly:           {reality.WorkSynthesis, reality.WorkCorpusReading},
		reality.AllowanceNoCodeInvestigation: {reality.WorkSynthesis},
		reality.AllowanceExcluded:            {},
	}
	for allowance, permitted := range want {
		for _, work := range reality.Works() {
			got := allowance.Permits(work)
			if expect := slices.Contains(permitted, work); got != expect {
				t.Errorf("%s permits %s = %v, want %v", allowance, work, got, expect)
			}
		}
	}
}

var _ = context.Background
