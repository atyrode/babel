package evaluation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/frontier"
)

// What this file defends is the part of #219 that is a distributed-systems
// problem rather than a vocabulary problem: only the valid claim commits, a
// stale worker's late result is refused rather than published, unobserved
// spend is charged rather than forgiven, and an interrupted completion lands
// exactly once when it resumes.
//
// The observable behaviour is asserted through the store, not through the
// local coordinator's tables, except where the ledger is the observation - a
// reservation that was released as zero is not visible any other way.

// An outcome is not a vote and is not admitted on the strength of its own
// assertion. Each of these refusals is a distinct way the "manufactured
// verified result" #219 forbids could otherwise get in.
func TestOutcomeRequiresRoleEvidenceAndOperatorCriteria(t *testing.T) {
	ctx := context.Background()
	criteria := []Criterion{{ID: "crit_1", Description: "p99 drops below 100ms"}}

	// The role the grant carries is the authority, so a reception assignment
	// cannot return an outcome claim.
	h := newHarness(t)
	reception := h.claim(t, "asg_reception", RoleReception, testRun)
	if _, err := h.store.Submit(ctx, Submission{
		AssignmentID: reception.ID,
		RunID:        testRun,
		Fence:        reception.Fence,
		Assessment: &Assessment{
			Outcome:     OutcomeVerified,
			CriteriaID:  "evr_whatever",
			Environment: "staging",
			AsOf:        time.Now().UTC(),
			Results:     []CriterionResult{{CriterionID: "crit_1", Satisfied: true}},
		},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("outcome under a reception role error = %v, want ErrInvalid", err)
	}

	operatorCriteria, err := h.store.Operator(ctx, OperatorInput{
		Kind:     KindCriteria,
		Operator: "alex",
		Subject:  proposalSubject(),
		Criteria: criteria,
	})
	if err != nil {
		t.Fatalf("record criteria: %v", err)
	}
	// A second version with two conditions: the unsatisfied case below has to
	// name a criterion the operator actually stated, or it would be refused
	// for naming an unknown one and prove nothing about satisfaction.
	pair, err := h.store.Operator(ctx, OperatorInput{
		Kind:     KindCriteria,
		Operator: "alex",
		Subject:  proposalSubject(),
		Criteria: []Criterion{
			{ID: "crit_1", Description: "p99 drops below 100ms"},
			{ID: "crit_2", Description: "no error-rate regression"},
		},
	})
	if err != nil {
		t.Fatalf("record paired criteria: %v", err)
	}

	cases := []struct {
		name       string
		assessment Assessment
	}{
		{
			name: "a satisfied criterion with no evidence",
			assessment: Assessment{
				Outcome: OutcomeVerified, CriteriaID: operatorCriteria.ID, Environment: "staging",
				AsOf: time.Now().UTC(), Results: []CriterionResult{{CriterionID: "crit_1", Satisfied: true}},
			},
		},
		{
			name: "verified while a criterion is unsatisfied",
			assessment: Assessment{
				Outcome: OutcomeVerified, CriteriaID: pair.ID, Environment: "staging",
				AsOf: time.Now().UTC(),
				Results: []CriterionResult{
					{CriterionID: "crit_1", Satisfied: true, Evidence: []frontier.Evidence{testEvidence(t)}},
					{CriterionID: "crit_2"},
				},
			},
		},
		{
			name: "no criteria version at all",
			assessment: Assessment{
				Outcome: OutcomeImplemented, Environment: "staging", AsOf: time.Now().UTC(),
				Contributions: []Contribution{{Kind: ContributionEvidence,
					Evidence: []frontier.Evidence{testEvidence(t)}}},
			},
		},
		{
			name: "no environment",
			assessment: Assessment{
				Outcome: OutcomeImplemented, CriteriaID: operatorCriteria.ID, AsOf: time.Now().UTC(),
				Contributions: []Contribution{{Kind: ContributionEvidence,
					Evidence: []frontier.Evidence{testEvidence(t)}}},
			},
		},
		{
			name: "no evidence anywhere",
			assessment: Assessment{
				Outcome: OutcomeImplemented, CriteriaID: operatorCriteria.ID, Environment: "prod",
				AsOf: time.Now().UTC(),
			},
		},
		{
			name: "unverifiable while claiming satisfied criteria",
			assessment: Assessment{
				Outcome: OutcomeUnverifiable, CriteriaID: operatorCriteria.ID, Environment: "prod",
				AsOf: time.Now().UTC(), Uncertainty: "no access",
				Results: []CriterionResult{{CriterionID: "crit_1", Satisfied: true,
					Evidence: []frontier.Evidence{testEvidence(t)}}},
			},
		},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			granted := h.claim(t, fmt.Sprintf("asg_outcome_%d", i), RoleOutcome, testRun)
			assessment := tc.assessment
			if _, err := h.store.Submit(ctx, Submission{
				AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence, Assessment: &assessment,
			}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}

	// The lawful form: the operator's criteria version, an environment, a
	// time, and evidence for every satisfied criterion.
	granted := h.claim(t, "asg_outcome_ok", RoleOutcome, testRun)
	record, err := h.store.Submit(ctx, Submission{
		AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
		Assessment: &Assessment{
			Outcome: OutcomeVerified, CriteriaID: operatorCriteria.ID, Environment: "staging",
			AsOf: time.Now().UTC(),
			Results: []CriterionResult{{CriterionID: "crit_1", Satisfied: true,
				Evidence: []frontier.Evidence{testEvidence(t)}}},
		},
	})
	if err != nil {
		t.Fatalf("verified outcome: %v", err)
	}
	if record.ActorKind != ActorRun {
		t.Fatalf("an outcome was recorded as %s authority, not as a run's observation", record.ActorKind)
	}
}

// A criteria version authored by a run is Babel choosing its own target, and a
// criteria version about another subject is measuring against the wrong thing.
// Both are refused, and an authority that cannot be reached at all is refused
// differently: explicitly unavailable rather than assumed acceptable.
func TestOutcomeCriteriaAuthorityIsResolvedNotAssumed(t *testing.T) {
	ctx := context.Background()
	otherSubject := Subject{Kind: "proposal", ID: "prop_0002"}

	t.Run("criteria about another subject", func(t *testing.T) {
		h := newHarness(t)
		h.resolver.artifacts[otherSubject] = Artifact{Subject: otherSubject, HeadID: otherSubject.ID,
			RunID: "run_author", ContextVersion: "ctx-1"}
		elsewhere, err := h.store.Operator(ctx, OperatorInput{
			Kind: KindCriteria, Operator: "alex", Subject: otherSubject,
			Criteria: []Criterion{{ID: "crit_1", Description: "something else entirely"}},
		})
		if err != nil {
			t.Fatalf("record criteria: %v", err)
		}
		granted := h.claim(t, "asg_wrongsubject", RoleOutcome, testRun)
		if _, err := h.store.Submit(ctx, outcomeSubmission(t, granted, elsewhere.ID)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("criteria authored by a run", func(t *testing.T) {
		h := newHarness(t)
		// A run-authored record standing where the operator's criteria belong
		// is exactly the substitution §4.12 forbids, so the resolver is given
		// one to prove the check is on the author and not on existence.
		h.records.records["evr_runcriteria"] = Record{
			ID: "evr_runcriteria", Kind: KindCriteria, Subject: proposalSubject(),
			ActorKind: ActorRun, ActorID: testRun, CreatedAt: time.Now().UTC(),
			Criteria: []Criterion{{ID: "crit_1", Description: "whatever I can verify"}},
		}
		granted := h.claim(t, "asg_runcriteria", RoleOutcome, testRun)
		if _, err := h.store.Submit(ctx,
			outcomeSubmission(t, granted, "evr_runcriteria")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("error = %v, want ErrInvalid", err)
		}
	})

	t.Run("criteria published by another instance", func(t *testing.T) {
		h := newHarness(t)
		h.records.records["evr_remote"] = Record{
			ID: "evr_remote", Kind: KindCriteria, Subject: proposalSubject(),
			ActorKind: ActorOperator, ActorID: "other-operator", CreatedAt: time.Now().UTC(),
			Criteria: []Criterion{{ID: "crit_1", Description: "p99 drops below 100ms"}},
		}
		granted := h.claim(t, "asg_remotecriteria", RoleOutcome, testRun)
		if _, err := h.store.Submit(ctx, outcomeSubmission(t, granted, "evr_remote")); err != nil {
			t.Fatalf("remote operator criteria: %v", err)
		}
	})

	t.Run("authority that cannot be looked up", func(t *testing.T) {
		h := newHarness(t)
		h.records.err = fmt.Errorf("catalog unreachable")
		granted := h.claim(t, "asg_unavailable", RoleOutcome, testRun)
		_, err := h.store.Submit(ctx, outcomeSubmission(t, granted, "evr_elsewhere"))
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("error = %v, want ErrUnavailable", err)
		}
	})

	t.Run("no resolver wired at all", func(t *testing.T) {
		clock := &fixedClock{at: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)}
		resolver := &fakeResolver{artifacts: map[Subject]Artifact{
			proposalSubject(): {Subject: proposalSubject(), HeadID: testProposal, RunID: "run_author",
				ContextVersion: "ctx-1"},
		}}
		store, err := Open(t.TempDir(), resolver)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer store.Close()
		store.now = clock.now
		store.coord.(*localCoordinator).now = clock.now
		granted, err := store.Claim(ctx, Assignment{
			ID: "asg_noresolver", Subject: proposalSubject(), RunID: testRun, Role: RoleOutcome,
			PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
		}, testPolicy())
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		_, err = store.Submit(ctx, outcomeSubmission(t, granted, "evr_elsewhere"))
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("error = %v, want ErrUnavailable", err)
		}
	})
}

func outcomeSubmission(t *testing.T, granted Assignment, criteriaID string) Submission {
	t.Helper()
	return Submission{
		AssignmentID: granted.ID,
		RunID:        granted.RunID,
		Fence:        granted.Fence,
		Assessment: &Assessment{
			Outcome: OutcomeVerified, CriteriaID: criteriaID, Environment: "staging",
			AsOf: time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC),
			Results: []CriterionResult{{CriterionID: "crit_1", Satisfied: true,
				Evidence: []frontier.Evidence{testEvidence(t)}}},
		},
	}
}

// A run may not boost what it just wrote, and the refusal is asymmetric on
// purpose: doubting your own output is a legitimate review, refining it is the
// ordinary way §4.12 says an idea is improved, and the compound
// `<run>/<stage>` identity is the same run.
func TestSelfBoostRefusedButSelfCriticismAccepted(t *testing.T) {
	ctx := context.Background()
	mine := Subject{Kind: "proposal", ID: "prop_mine"}

	newSelfHarness := func(t *testing.T) *harness {
		h := newHarness(t)
		h.resolver.artifacts[mine] = Artifact{Subject: mine, HeadID: mine.ID, RootID: mine.ID,
			RunID: testRun, ContextVersion: "ctx-1"}
		return h
	}

	t.Run("support for its own output", func(t *testing.T) {
		h := newSelfHarness(t)
		granted, err := h.store.Claim(ctx, Assignment{
			ID: "asg_boost", Subject: mine, RunID: testRun + "/challenge", Role: RoleReception,
			PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
		}, testPolicy())
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: granted.RunID, Fence: granted.Fence,
			Assessment: &Assessment{Vote: VoteSupport},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("same-run support error = %v, want ErrInvalid", err)
		}
	})

	t.Run("opposition to its own output", func(t *testing.T) {
		h := newSelfHarness(t)
		granted, err := h.store.Claim(ctx, Assignment{
			ID: "asg_selfdoubt", Subject: mine, RunID: testRun, Role: RoleReception,
			PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
		}, testPolicy())
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Vote: VoteOppose},
		}); err != nil {
			t.Fatalf("same-run opposition: %v", err)
		}
	})

	t.Run("preferring its own alternative in a comparison", func(t *testing.T) {
		h := newSelfHarness(t)
		granted := h.claim(t, "asg_compare", RoleComparison, testRun)
		preferred := mine
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Contributions: []Contribution{{
				Kind:         ContributionComparison,
				Text:         "mine is better",
				Alternatives: []Subject{proposalSubject(), mine},
				Preferred:    &preferred,
			}}},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("preferring its own alternative error = %v, want ErrInvalid", err)
		}
	})

	t.Run("a partial outcome about its own output", func(t *testing.T) {
		h := newSelfHarness(t)
		criteria, err := h.store.Operator(ctx, OperatorInput{
			Kind: KindCriteria, Operator: "alex", Subject: mine,
			Criteria: []Criterion{{ID: "crit_1", Description: "the index is cached"}},
		})
		if err != nil {
			t.Fatalf("record criteria: %v", err)
		}
		granted, err := h.store.Claim(ctx, Assignment{
			ID: "asg_selfpartial", Subject: mine, RunID: testRun, Role: RoleOutcome,
			PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
		}, testPolicy())
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// A partial outcome is still a positive report about what this run
		// wrote, so it is the same hazard as a verification wearing a
		// smaller claim.
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{
				Outcome: OutcomePartial, CriteriaID: criteria.ID, Environment: "staging",
				AsOf: time.Now().UTC(), Uncertainty: "the tail latency is unmeasured",
				Results: []CriterionResult{{CriterionID: "crit_1", Satisfied: true,
					Evidence: []frontier.Evidence{testEvidence(t)}}},
			},
		}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("same-run partial outcome error = %v, want ErrInvalid", err)
		}
	})

	t.Run("refining its own output", func(t *testing.T) {
		h := newSelfHarness(t)
		granted, err := h.store.Claim(ctx, Assignment{
			ID: "asg_selfrefine", Subject: mine, RunID: testRun, Role: RoleReception,
			PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
		}, testPolicy())
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// A refinement carries its own provenance and endorses nothing, so
		// refusing it would refuse the mechanism instead of the abuse.
		record, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Contributions: []Contribution{{
				Kind: ContributionRefinement, Text: "narrow the claim to the indexing path",
			}}},
		})
		if err != nil {
			t.Fatalf("refining its own output: %v", err)
		}
		if record.Assessment.Vote != "" {
			t.Fatalf("an author's refinement minted the vote %q for its own record", record.Assessment.Vote)
		}
	})

	t.Run("endorsing an artifact with no recorded author", func(t *testing.T) {
		h := newSelfHarness(t)
		anonymous := Subject{Kind: "proposal", ID: "prop_anon"}
		h.resolver.artifacts[anonymous] = Artifact{Subject: anonymous, HeadID: anonymous.ID,
			ContextVersion: "ctx-1"}
		granted, err := h.store.Claim(ctx, Assignment{
			ID: "asg_anon", Subject: anonymous, RunID: testRun, Role: RoleReception,
			PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
		}, testPolicy())
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		// The producing run is what the self-boost check compares against, so
		// an artifact that records none leaves the question unanswered - and
		// an unanswered question is not a yes.
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
			Assessment: &Assessment{Vote: VoteSupport},
		}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("support for an unattributed artifact error = %v, want ErrUnavailable", err)
		}
		// Opposition is not an endorsement, so the unknown author does not
		// block it.
		doubt, err := h.store.Claim(ctx, Assignment{
			ID: "asg_anon_doubt", Subject: anonymous, RunID: testRun, Role: RoleReception,
			PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
		}, testPolicy())
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		if _, err := h.store.Submit(ctx, Submission{
			AssignmentID: doubt.ID, RunID: testRun, Fence: doubt.Fence,
			Assessment: &Assessment{Vote: VoteOppose},
		}); err != nil {
			t.Fatalf("opposition to an unattributed artifact: %v", err)
		}
	})
}

// Two workers, one assignment, and a stale worker that comes back after the
// takeover: only the valid claim commits, the stale result never becomes a
// vote, and the takeover's own result still lands.
func TestTakeoverFencesTheStaleWorker(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first := h.claim(t, "asg_fence", RoleReception, testRun)

	// The lease is allowed to lapse, which is what lets another worker take
	// the claim rather than being told it is held.
	h.clock.at = h.clock.at.Add(time.Duration(testPolicy().LeaseSeconds+1) * time.Second)
	second := h.claim(t, "asg_fence", RoleReception, otherRun)
	if second.Fence <= first.Fence {
		t.Fatalf("takeover fence %d did not advance past %d", second.Fence, first.Fence)
	}

	if _, err := h.store.Submit(ctx, Submission{
		AssignmentID: first.ID, RunID: testRun, Fence: first.Fence,
		Assessment: &Assessment{Vote: VoteSupport},
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale submit error = %v, want ErrConflict", err)
	}
	if err := h.store.Expose(ctx, first.ID, testRun, first.Fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expose error = %v, want ErrConflict", err)
	}

	winner, err := h.store.Submit(ctx, Submission{
		AssignmentID: second.ID, RunID: otherRun, Fence: second.Fence,
		Assessment: &Assessment{Vote: VoteOppose}, Cost: 0.5,
	})
	if err != nil {
		t.Fatalf("the valid claim could not commit: %v", err)
	}
	votes := 0
	events, err := h.store.Events(ctx)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, ev := range events {
		if ev.Kind == KindAssessment && ev.AssignmentID == "asg_fence" {
			votes++
		}
	}
	if votes != 1 {
		t.Fatalf("active statements for one grant = %d, want 1", votes)
	}
	if winner.ActorID != otherRun {
		t.Fatalf("the committed statement is attributed to %q", winner.ActorID)
	}
}

// A completion whose spend was reconciled but whose record did not commit is
// resumed, not repeated: the same record id lands once and the coordinator
// sees one receipt.
func TestPendingSettlementResumesExactlyOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	granted := h.claim(t, "asg_resume", RoleReception, testRun)
	in := Submission{
		AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
		Assessment: &Assessment{Vote: VoteSupport}, Cost: 0.5,
	}
	digest, err := digestSubmission(in)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	// The receipt this instance would have owed, left pending exactly as a
	// crash between the reconciliation and the local commit leaves it.
	owed, err := h.store.openSettlement(ctx, granted, in, AttemptCompleted, digest, testProposal)
	if err != nil {
		t.Fatalf("open settlement: %v", err)
	}

	recovered, err := h.store.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered %d settlements, want 1", recovered)
	}
	record, err := h.store.Record(ctx, owed.recordID)
	if err != nil {
		t.Fatalf("the resumed record is missing: %v", err)
	}
	if record.Assessment.Vote != VoteSupport {
		t.Fatalf("resumed statement = %+v", record.Assessment)
	}

	// The worker retrying after the crash gets the same record rather than a
	// second one, and the claim is not finished twice at two costs.
	again, err := h.store.Submit(ctx, in)
	if err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if again.ID != record.ID {
		t.Fatalf("retry produced %s beside the resumed %s", again.ID, record.ID)
	}
	if second, err := h.store.Recover(ctx); err != nil || second != 0 {
		t.Fatalf("second recovery settled %d (err %v), want 0", second, err)
	}
}

// A claim whose spend the fleet refuses does not become a local record. This
// is the ordering the whole settlement machinery exists for: the record commits
// only after the allowance has accounted for it.
func TestFencedFinishPublishesNothing(t *testing.T) {
	h := newHarness(t, WithCoordinator(&stubCoordinator{
		finishErr: fmt.Errorf("%w: taken over", ErrConflict),
	}))
	ctx := context.Background()
	granted, err := h.store.Claim(ctx, Assignment{
		ID: "asg_refused", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
	}, testPolicy())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	staged := h.hook.count()
	_, err = h.store.Submit(ctx, Submission{
		AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
		Assessment: &Assessment{Vote: VoteSupport}, Cost: 1,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	if h.hook.count() != staged {
		t.Fatalf("a refused result staged %d records", h.hook.count()-staged)
	}
	// A retry of the same refused result stays refused rather than being
	// admitted by a fresh attempt.
	if _, err := h.store.Submit(ctx, Submission{
		AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
		Assessment: &Assessment{Vote: VoteSupport}, Cost: 1,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("retry of a fenced result error = %v, want ErrConflict", err)
	}
}

// Spend that was accounted in full and overran the ceiling is news about
// admission, not a reason to throw away the completed work whose cost is
// already charged.
func TestAccountedOverrunStillRecordsTheResult(t *testing.T) {
	h := newHarness(t, WithCoordinator(&stubCoordinator{
		finishErr: fmt.Errorf("%w: the day is over its ceiling", ErrOverrun),
	}))
	ctx := context.Background()
	granted, err := h.store.Claim(ctx, Assignment{
		ID: "asg_overrun", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
	}, testPolicy())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	record, err := h.store.Submit(ctx, Submission{
		AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
		Assessment: &Assessment{Vote: VoteSupport}, Cost: 5,
	})
	if err != nil {
		t.Fatalf("overrun completion: %v", err)
	}
	if record.ID == "" {
		t.Fatalf("an accounted overrun discarded the result")
	}
	var reason string
	if err := h.store.db.QueryRow(`SELECT reason FROM evaluation_settlement
		WHERE assignment_id = ?`, granted.ID).Scan(&reason); err != nil {
		t.Fatalf("read receipt: %v", err)
	}
	if reason == "" {
		t.Fatalf("the overrun was not recorded on the receipt")
	}
	// The mirror carries the receipt the fleet already charged. A spend the
	// fleet holds and this machine does not is a spend a resumed worker would
	// report a second time, so an accounted overrun must not stop the local
	// half of the reconciliation.
	var finished float64
	if err := h.store.db.QueryRow(`SELECT finished_cost FROM evaluation_claim WHERE id = ?`,
		granted.ID).Scan(&finished); err != nil {
		t.Fatalf("read the mirrored claim: %v", err)
	}
	if finished != 5 {
		t.Fatalf("the mirror recorded %v for an accounted overrun of 5", finished)
	}
	var (
		actual  float64
		settled int
	)
	if err := h.store.db.QueryRow(`SELECT actual, settled FROM evaluation_spend
		WHERE assignment_id = ? AND fence = ?`, granted.ID, granted.Fence).Scan(&actual, &settled); err != nil {
		t.Fatalf("read the mirrored ledger row: %v", err)
	}
	if actual != 5 || settled != 1 {
		t.Fatalf("the mirrored ledger row = (%v, %d), want the full spend reconciled", actual, settled)
	}
}

// The day's allowance is one allowance. A reservation that expired without a
// receipt stays charged, because assuming an abandoned attempt cost nothing
// lets a crash loop spend the day many times over.
func TestExpiredReservationStaysChargedAgainstTheDay(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	policy := testPolicy()
	policy.DailyCost = 2
	// The cycle ceiling admits both reservations here: it is the day that
	// runs out, and the per-run ceiling has its own test.
	policy.PerCycleCost = 2

	for i := range 2 {
		if _, err := h.store.Claim(ctx, Assignment{
			ID: fmt.Sprintf("asg_day_%d", i), Subject: proposalSubject(), RunID: testRun,
			Role: RoleReception, PolicyVersion: policy.Version, InputDigest: "d", Lane: "coverage",
			ReservedCost: 1,
		}, policy); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	// Both leases lapse with nothing reported. The attention they reserved is
	// gone whether or not anyone observed it being spent.
	h.clock.at = h.clock.at.Add(time.Duration(policy.LeaseSeconds+1) * time.Second)
	_, err := h.store.Claim(ctx, Assignment{
		ID: "asg_day_2", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: policy.Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
	}, policy)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("error = %v, want ErrBudget: expired reservations were released as zero", err)
	}

	// A takeover of one of those abandoned attempts reserves again on top of
	// what it still owes rather than inheriting it for free.
	_, err = h.store.Claim(ctx, Assignment{
		ID: "asg_day_0", Subject: proposalSubject(), RunID: otherRun, Role: RoleReception,
		PolicyVersion: policy.Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
	}, policy)
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("takeover error = %v, want ErrBudget", err)
	}
}

// A reservation over the per-cycle ceiling is refused before any work starts,
// and a policy nobody enabled authorizes nothing at all.
func TestClaimRefusesOversizedReservationAndDisabledPolicy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	policy := testPolicy()
	policy.PerCycleCost = 1
	if _, err := h.store.Claim(ctx, Assignment{
		ID: "asg_toobig", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: policy.Version, InputDigest: "d", Lane: "coverage",
		ReservedCost: policy.PerCycleCost + 1,
	}, policy); !errors.Is(err, ErrBudget) {
		t.Fatalf("oversized reservation error = %v, want ErrBudget", err)
	}

	disabled := policy
	disabled.Enabled = false
	if _, err := h.store.Claim(ctx, Assignment{
		ID: "asg_disabled", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: disabled.Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
	}, disabled); !errors.Is(err, ErrNoWork) {
		t.Fatalf("disabled policy error = %v, want ErrNoWork", err)
	}
}

// A grant is refused when its role does not apply to its subject kind, when
// the subject cannot be read at all, and when the draw could not be replayed.
// None of those is a coverage gap; each is a grant that should not exist.
func TestClaimValidatesRoleSubjectAndReplayability(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	policy := testPolicy()
	observation := Subject{Kind: "observation", ID: "obs_1"}
	h.resolver.artifacts[observation] = Artifact{Subject: observation, HeadID: "obs_1",
		RunID: "run_author", ContextVersion: "ctx-1"}

	for _, tc := range []struct {
		name string
		a    Assignment
		want error
	}{
		{
			name: "an outcome role on an observation",
			a: Assignment{ID: "asg_role", Subject: observation, RunID: testRun, Role: RoleOutcome,
				PolicyVersion: policy.Version, InputDigest: "d", Lane: "coverage"},
			want: ErrInvalid,
		},
		{
			name: "a subject nothing can resolve",
			a: Assignment{ID: "asg_ghost", Subject: Subject{Kind: "proposal", ID: "prop_ghost"},
				RunID: testRun, Role: RoleReception, PolicyVersion: policy.Version,
				InputDigest: "d", Lane: "coverage"},
			want: ErrNotFound,
		},
		{
			name: "no captured input digest",
			a: Assignment{ID: "asg_nodigest", Subject: proposalSubject(), RunID: testRun,
				Role: RoleReception, PolicyVersion: policy.Version, Lane: "coverage"},
			want: ErrInvalid,
		},
		{
			name: "a policy version the draw was not made under",
			a: Assignment{ID: "asg_otherpolicy", Subject: proposalSubject(), RunID: testRun,
				Role: RoleReception, PolicyVersion: "some-other", InputDigest: "d", Lane: "coverage"},
			want: ErrInvalid,
		},
		{
			name: "no lane to account the spend to",
			a: Assignment{ID: "asg_nolane", Subject: proposalSubject(), RunID: testRun,
				Role: RoleReception, PolicyVersion: policy.Version, InputDigest: "d"},
			want: ErrInvalid,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.store.Claim(ctx, tc.a, policy); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

// A subject the resolver cannot read - the remote case - is refused
// explicitly, and a resolver that cannot answer at all is refused as
// unavailable rather than treated as an absent record.
func TestRemoteSubjectResolutionFailuresAreDistinguished(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.resolver.err = fmt.Errorf("%w: the fleet is unreachable", ErrUnavailable)
	if _, err := h.store.Claim(ctx, Assignment{
		ID: "asg_unreachable", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
	}, testPolicy()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

// A grant claimed here must survive a read as the grant it was: the fleet's
// ABI carries only the fence, the lease and the reservation, so everything a
// replay and a Reality admission need has to come back from the local row.
func TestGrantRoundTripsWithReplayInputs(t *testing.T) {
	h := newHarness(t, WithCoordinator(&stubCoordinator{}))
	ctx := context.Background()
	granted, err := h.store.Claim(ctx, Assignment{
		ID: "asg_replay", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: testPolicy().Version, Seed: 1 << 63, InputDigest: "digest-9",
		Lane: "exploration", ReservedCost: 1, Subjects: []string{"index", "cache"},
	}, testPolicy())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	read, err := h.store.Assignment(ctx, "asg_replay")
	if err != nil {
		t.Fatalf("read assignment: %v", err)
	}
	if read.Seed != 1<<63 {
		t.Fatalf("seed = %d, want %d: a seed above the signed range was lost", read.Seed, uint64(1)<<63)
	}
	if read.InputDigest != "digest-9" || read.Lane != "exploration" {
		t.Fatalf("replay inputs = %q/%q", read.InputDigest, read.Lane)
	}
	if len(read.Subjects) != 2 || read.Subjects[0] != "index" {
		t.Fatalf("recorded entity names = %v", read.Subjects)
	}
	if read.Fence != granted.Fence || read.ExpiresAt.IsZero() {
		t.Fatalf("grant authority = fence %d, expiry %v", read.Fence, read.ExpiresAt)
	}
}

// An exposure is a fact about what was served, and serving the same content
// twice under one epoch is one exposure. Retries must not inflate it.
func TestExposeIsIdempotentPerEpoch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	granted := h.claim(t, "asg_expose", RoleReception, testRun)
	for range 2 {
		if err := h.store.Expose(ctx, granted.ID, testRun, granted.Fence); err != nil {
			t.Fatalf("expose: %v", err)
		}
	}
	attempts, err := h.store.Attempts(ctx)
	if err != nil {
		t.Fatalf("attempts: %v", err)
	}
	exposures := 0
	for _, attempt := range attempts {
		if attempt.AssignmentID == granted.ID && attempt.State == AttemptExposed {
			exposures++
		}
	}
	if exposures != 1 {
		t.Fatalf("exposures = %d, want 1", exposures)
	}
	// The exposure publishes, because a second instance cannot otherwise tell
	// an assignment that was opened from one nobody ever looked at.
	published := 0
	for _, staged := range h.hook.staged {
		record, err := Decode(staged.Payload)
		if err != nil {
			t.Fatalf("decode staged record: %v", err)
		}
		if record.Kind == KindAttempt && record.Attempt.State == AttemptExposed {
			published++
		}
	}
	if published != 1 {
		t.Fatalf("published exposures = %d, want 1", published)
	}
}

// A result cannot claim a context it did not read, and the statement it does
// record keeps the context the grant named - which is what keeps a stale
// result from representing current-context coverage.
func TestAssessmentCannotClaimAnUnreadContext(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	granted := h.claim(t, "asg_ctx", RoleReception, testRun)
	if _, err := h.store.Submit(ctx, Submission{
		AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
		Assessment: &Assessment{Vote: VoteSupport, ContextVersion: "ctx-99"},
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("error = %v, want ErrInvalid", err)
	}

	// The artifact moves on while the assignment is outstanding: the vote
	// stays on the revision that was read, and the head observed at write
	// time is recorded rather than the vote following the chain.
	moved := h.resolver.artifacts[proposalSubject()]
	moved.HeadID = "prop_0001b"
	h.resolver.artifacts[proposalSubject()] = moved
	record, err := h.store.Submit(ctx, Submission{
		AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
		Assessment: &Assessment{Vote: VoteSupport},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if record.Subject.ID != testProposal {
		t.Fatalf("the vote moved to %s", record.Subject.ID)
	}
	var head string
	if err := h.store.db.QueryRow(`SELECT read_head_id FROM evaluation_record WHERE id = ?`,
		record.ID).Scan(&head); err != nil {
		t.Fatalf("read observed head: %v", err)
	}
	if head != "prop_0001b" {
		t.Fatalf("observed head = %q, want the head at write time", head)
	}
}

// The local authority is not a relaxed authority: an expired lease cannot
// read, a taken-over holder cannot finish, and a finish is idempotent only for
// the identical receipt.
func TestLocalCoordinatorAuthority(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	granted := h.claim(t, "asg_local", RoleReception, testRun)

	if err := h.store.ValidateClaim(ctx, granted.ID, otherRun, granted.Fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong holder error = %v, want ErrConflict", err)
	}
	if err := h.store.ValidateClaim(ctx, granted.ID, testRun, granted.Fence+1); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong fence error = %v, want ErrConflict", err)
	}
	if err := h.store.ValidateClaim(ctx, "asg_missing", testRun, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown assignment error = %v, want ErrNotFound", err)
	}
	if err := h.store.ValidateClaim(ctx, granted.ID, testRun, granted.Fence); err != nil {
		t.Fatalf("the current holder was refused: %v", err)
	}

	h.clock.at = h.clock.at.Add(time.Duration(testPolicy().LeaseSeconds+1) * time.Second)
	if err := h.store.ValidateClaim(ctx, granted.ID, testRun, granted.Fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired lease error = %v, want ErrConflict", err)
	}

	// Expiry alone does not refuse the finish: the work happened and the
	// money was spent.
	if err := h.store.coord.Finish(ctx, granted.ID, testRun, granted.Fence, 0.5); err != nil {
		t.Fatalf("finish after expiry: %v", err)
	}
	if err := h.store.coord.Finish(ctx, granted.ID, testRun, granted.Fence, 0.5); err != nil {
		t.Fatalf("identical receipt: %v", err)
	}
	if err := h.store.coord.Finish(ctx, granted.ID, testRun, granted.Fence, 0.9); !errors.Is(err, ErrConflict) {
		t.Fatalf("differing cost error = %v, want ErrConflict", err)
	}
	// The holder's own finished receipt stays valid after the lease lapses.
	// Completion is recorded before the record commits, so re-driving that
	// settlement - or naming the completion from the correction that
	// supersedes it - is the same authority asking about the same finished
	// work, and refusing it would strand a result already paid for.
	if err := h.store.ValidateClaim(ctx, granted.ID, testRun, granted.Fence); err != nil {
		t.Fatalf("the run that finished the work was refused: %v", err)
	}
	if err := h.store.ValidateClaim(ctx, granted.ID, otherRun, granted.Fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("another run on a finished claim error = %v, want ErrConflict", err)
	}
	if err := h.store.ValidateClaim(ctx, granted.ID, testRun, granted.Fence+1); !errors.Is(err, ErrConflict) {
		t.Fatalf("a superseded fence on a finished claim error = %v, want ErrConflict", err)
	}
	// Nothing takes over a finished claim.
	if _, err := h.store.Claim(ctx, Assignment{
		ID: granted.ID, Subject: proposalSubject(), RunID: otherRun, Role: RoleReception,
		PolicyVersion: testPolicy().Version, InputDigest: "d", Lane: "coverage", ReservedCost: 1,
	}, testPolicy()); !errors.Is(err, ErrConflict) {
		t.Fatalf("takeover of a finished claim error = %v, want ErrConflict", err)
	}
}

// stubCoordinator stands in for the fleet: it grants whatever it is asked for
// and answers Finish with whatever the test wants to see.
type stubCoordinator struct {
	finishErr   error
	validateErr error
	finishes    int
	renewErr    error
	renewals    int
}

func (c *stubCoordinator) Claim(ctx context.Context, a Assignment, p Policy) (Assignment, error) {
	granted := a
	granted.Fence = 7
	granted.CreatedAt = time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	granted.ExpiresAt = granted.CreatedAt.Add(time.Duration(p.LeaseSeconds) * time.Second)
	// The fleet's ABI has no column for these, so a coordinator that returned
	// its own row would blank them. The merge in Claim is what this proves.
	granted.Seed = 0
	granted.InputDigest = ""
	granted.Lane = ""
	granted.Subjects = nil
	return granted, nil
}

func (c *stubCoordinator) Validate(ctx context.Context, id, runID string, fence int64) error {
	return c.validateErr
}

func (c *stubCoordinator) Renew(ctx context.Context, id, runID string, fence int64,
	p Policy) (time.Time, error) {
	c.renewals++
	if c.renewErr != nil {
		return time.Time{}, c.renewErr
	}
	// The fleet answers with its own clock, which is why the mirror keeps its
	// own window rather than adopting this one.
	return time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC).
		Add(time.Duration(p.LeaseSeconds) * time.Second), nil
}

func (c *stubCoordinator) Finish(ctx context.Context, id, runID string, fence int64, cost float64) error {
	c.finishes++
	return c.finishErr
}

// claimWith grants one assignment under a policy and a reservation the test
// chose, which is what makes the budget assertions about the policy rather
// than about the fixture.
func claimWith(t *testing.T, h *harness, id, runID string, reserved float64, p Policy) (Assignment, error) {
	t.Helper()
	return h.store.Claim(context.Background(), Assignment{
		ID: id, Subject: proposalSubject(), RunID: runID, Role: RoleReception,
		PolicyVersion: p.Version, Seed: 7, InputDigest: "digest-1", Lane: "coverage",
		ReservedCost: reserved,
	}, p)
}

// A retry of a live claim is the grant the run already holds. A lost answer
// must not advance the fence or reserve a second time for one assignment -
// that would spend the day's allowance on a dropped packet - and only an
// expired authority may be taken over.
func TestLiveReClaimReturnsTheSameGrant(t *testing.T) {
	h := newHarness(t)
	first := h.claim(t, "asg_reclaim", RoleReception, testRun)

	again := h.claim(t, "asg_reclaim", RoleReception, testRun)
	if again.Fence != first.Fence || again.ReservedCost != first.ReservedCost ||
		!again.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("re-claim = fence %d reserving %v until %s, want the held grant: fence %d reserving %v until %s",
			again.Fence, again.ReservedCost, again.ExpiresAt, first.Fence, first.ReservedCost, first.ExpiresAt)
	}
	// The draw still travels on the answer: what comes back is the caller's
	// assignment with the authority's fields, not a row rebuilt from columns.
	if again.Seed != first.Seed || again.InputDigest != first.InputDigest || again.Lane != first.Lane {
		t.Fatalf("the re-claim lost its replay inputs: %+v", again)
	}

	var rows int
	var reserved float64
	if err := h.store.db.QueryRow(`SELECT COUNT(1), COALESCE(SUM(reserved), 0)
		FROM evaluation_spend WHERE assignment_id = ?`, first.ID).Scan(&rows, &reserved); err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if rows != 1 || reserved != first.ReservedCost {
		t.Fatalf("the ledger holds %d reservations totalling %v, want one of %v",
			rows, reserved, first.ReservedCost)
	}
	var grants int
	if err := h.store.db.QueryRow(`SELECT COUNT(1) FROM evaluation_record
		WHERE kind = ? AND assignment_id = ?`, KindAssignment, first.ID).Scan(&grants); err != nil {
		t.Fatalf("read the grant records: %v", err)
	}
	if grants != 1 {
		t.Fatalf("grant records = %d, want 1: a retry published a second grant", grants)
	}

	// Another run may not take a live claim, and the run that holds it is not
	// competing with itself.
	if _, err := claimWith(t, h, first.ID, otherRun, 1, testPolicy()); !errors.Is(err, ErrConflict) {
		t.Fatalf("live takeover error = %v, want ErrConflict", err)
	}
	h.clock.at = h.clock.at.Add(time.Duration(testPolicy().LeaseSeconds+1) * time.Second)
	taken, err := claimWith(t, h, first.ID, otherRun, 1, testPolicy())
	if err != nil {
		t.Fatalf("takeover after expiry: %v", err)
	}
	if taken.Fence != first.Fence+1 {
		t.Fatalf("takeover fence = %d, want %d", taken.Fence, first.Fence+1)
	}
}

// A retry that changed the draw is not a retry. What comes back from a
// re-claim is the caller's own assignment with the authority's fields merged
// into it, so answering an altered draw with the held fence would attach a
// paid-for reservation to replay inputs nobody granted - including the
// statement a paid follow-up claims to be correcting.
func TestReClaimRefusesAnAlteredDraw(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	held := Assignment{
		ID: "asg_altered", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: testPolicy().Version, ContextVersion: "ctx-1", Seed: 11,
		InputDigest: "digest-1", Lane: "coverage", ReservedCost: 1,
		Subjects: []string{"cache", "index"},
	}
	granted, err := h.store.Claim(ctx, held, testPolicy())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	for _, tc := range []struct {
		name  string
		alter func(a *Assignment)
	}{
		{"another seed", func(a *Assignment) { a.Seed = 12 }},
		{"another captured input", func(a *Assignment) { a.InputDigest = "digest-2" }},
		{"another context", func(a *Assignment) { a.ContextVersion = "ctx-2" }},
		{"another statement to correct", func(a *Assignment) { a.Corrects = "evr_elsewhere" }},
		{"another entity list", func(a *Assignment) { a.Subjects = []string{"cache"} }},
		{"another role", func(a *Assignment) { a.Role = RoleEvidence }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			altered := held
			tc.alter(&altered)
			if _, err := h.store.Claim(ctx, altered, testPolicy()); !errors.Is(err, ErrConflict) {
				t.Fatalf("error = %v, want ErrConflict", err)
			}
		})
	}

	// The grant that is held is untouched by those refusals, and the draw it
	// was made from is still accepted as the retry it is.
	again, err := h.store.Claim(ctx, held, testPolicy())
	if err != nil {
		t.Fatalf("the unaltered retry was refused: %v", err)
	}
	if again.Fence != granted.Fence || again.ReservedCost != granted.ReservedCost {
		t.Fatalf("re-claim = fence %d reserving %v, want fence %d reserving %v",
			again.Fence, again.ReservedCost, granted.Fence, granted.ReservedCost)
	}
	var rows int
	if err := h.store.db.QueryRow(`SELECT COUNT(1) FROM evaluation_spend
		WHERE assignment_id = ?`, held.ID).Scan(&rows); err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if rows != 1 {
		t.Fatalf("the ledger holds %d reservations, want 1", rows)
	}
}

// The statement a paid follow-up corrects travels on the grant and survives a
// read. A correction's authority rests on it, so a column nothing scanned back
// would make every resumed claim look like a first opinion.
func TestGrantCarriesTheStatementItCorrects(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	granted, err := h.store.Claim(ctx, Assignment{
		ID: "asg_corrects", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: testPolicy().Version, Seed: 3, InputDigest: "digest-1", Lane: "coverage",
		ReservedCost: 1, Corrects: "evr_prior",
	}, testPolicy())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if granted.Corrects != "evr_prior" {
		t.Fatalf("the grant came back correcting %q", granted.Corrects)
	}
	read, err := h.store.Assignment(ctx, granted.ID)
	if err != nil {
		t.Fatalf("read the grant: %v", err)
	}
	if read.Corrects != "evr_prior" {
		t.Fatalf("the stored grant corrects %q, want evr_prior", read.Corrects)
	}
}

// The mirror is the only holder of the draw, so it is the only authority that
// can refuse a re-pointed one. The fleet's ABI carries no seed, no captured
// input digest and no corrected statement, so a grant re-adopted at the same
// fence under different replay inputs would rewrite the record of what this
// machine was authorized to do.
func TestMirroredGrantIsNotReAdoptedUnderADifferentDraw(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, WithCoordinator(&stubCoordinator{}))
	first := Assignment{
		ID: "asg_mirror_draw", Subject: proposalSubject(), RunID: testRun, Role: RoleReception,
		PolicyVersion: testPolicy().Version, Seed: 5, InputDigest: "digest-1", Lane: "coverage",
		ReservedCost: 1, Corrects: "evr_prior",
	}
	granted, err := h.store.Claim(ctx, first, testPolicy())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if granted.InputDigest != "digest-1" || granted.Corrects != "evr_prior" {
		t.Fatalf("the fleet's narrower answer blanked the draw: %+v", granted)
	}
	altered := first
	altered.InputDigest = "digest-2"
	if _, err := h.store.Claim(ctx, altered, testPolicy()); !errors.Is(err, ErrConflict) {
		t.Fatalf("re-adoption under another draw error = %v, want ErrConflict", err)
	}
	read, err := h.store.Assignment(ctx, first.ID)
	if err != nil {
		t.Fatalf("read the mirrored grant: %v", err)
	}
	if read.InputDigest != "digest-1" || read.Corrects != "evr_prior" {
		t.Fatalf("the mirrored grant was rewritten to %+v", read)
	}
}

// The per-cycle ceiling bounds one run's whole UTC day. A cycle allowed the
// full ceiling once per assignment would consume the allowance an assignment
// at a time while every individual claim looked lawful.
func TestPerCycleCeilingBoundsTheRunsWholeDay(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	policy := testPolicy()
	policy.DailyCost = 10
	policy.PerCycleCost = 2

	for i := range 2 {
		if _, err := claimWith(t, h, fmt.Sprintf("asg_cycle_%d", i), testRun, 1, policy); err != nil {
			t.Fatalf("claim %d: %v", i, err)
		}
	}
	if _, err := claimWith(t, h, "asg_cycle_2", testRun, 1, policy); !errors.Is(err, ErrBudget) {
		t.Fatalf("third claim error = %v, want ErrBudget: the ceiling bounded one assignment rather than the run",
			err)
	}
	// The ceiling is per run, not per machine: the day still has room and
	// another run is admitted against it.
	if _, err := claimWith(t, h, "asg_cycle_other", otherRun, 1, policy); err != nil {
		t.Fatalf("another run was refused the day's remaining allowance: %v", err)
	}
	// A reconciliation below the reservation returns the difference to the
	// run, because the cycle is measured against spend rather than grants.
	if err := h.store.coord.Finish(ctx, "asg_cycle_0", testRun, 1, 0.25); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := claimWith(t, h, "asg_cycle_3", testRun, 0.5, policy); err != nil {
		t.Fatalf("claim inside the reconciled cycle: %v", err)
	}
}

// A takeover moves the claim and not the abandoned attempt's spend. The ledger
// keeps one row per epoch carrying the run that opened it, so the run that
// vanished still owes what it reserved and the new holder is charged for its
// own attempt only.
func TestTakeoverKeepsSpendAttributedToTheRunThatOwesIt(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	policy := testPolicy()
	policy.DailyCost = 10
	policy.PerCycleCost = 4

	first, err := claimWith(t, h, "asg_attrib", testRun, 1, policy)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	h.clock.at = h.clock.at.Add(time.Duration(policy.LeaseSeconds+1) * time.Second)
	second, err := claimWith(t, h, "asg_attrib", otherRun, 1, policy)
	if err != nil {
		t.Fatalf("takeover: %v", err)
	}
	if err := h.store.coord.Finish(ctx, second.ID, otherRun, second.Fence, 0.25); err != nil {
		t.Fatalf("finish: %v", err)
	}

	day := dayOf(h.clock.at)
	for _, tc := range []struct {
		run  string
		want float64
	}{
		{testRun, 1},     // the abandoned reservation, charged in full
		{otherRun, 0.25}, // what the takeover actually reported
	} {
		total, cycle, err := dayCharge(ctx, h.store.db, day, tc.run)
		if err != nil {
			t.Fatalf("day charge: %v", err)
		}
		if cycle != tc.want {
			t.Fatalf("run %s is charged %v for %s, want %v", tc.run, cycle, day, tc.want)
		}
		if total != 1.25 {
			t.Fatalf("the day carries %v, want 1.25: both epochs stay charged", total)
		}
	}
	var run string
	if err := h.store.db.QueryRow(`SELECT run_id FROM evaluation_spend
		WHERE assignment_id = ? AND fence = ?`, first.ID, first.Fence).Scan(&run); err != nil {
		t.Fatalf("read the superseded reservation: %v", err)
	}
	if run != testRun {
		t.Fatalf("the superseded reservation is attributed to %q, want %q", run, testRun)
	}
}

// A tightening survives the refusal of the claim that offered it. Rolling it
// back would leave the next worker spending an allowance the operator has
// already lowered.
func TestDeniedClaimStillTightensTheAllowance(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	policy := testPolicy()
	policy.DailyCost = 10
	policy.PerCycleCost = 10
	if _, err := claimWith(t, h, "asg_pin_0", testRun, 1, policy); err != nil {
		t.Fatalf("claim: %v", err)
	}

	lowered := policy
	lowered.DailyCost = 1
	lowered.PerCycleCost = 1
	if _, err := claimWith(t, h, "asg_pin_1", testRun, 5, lowered); !errors.Is(err, ErrBudget) {
		t.Fatalf("claim over the lowered allowance error = %v, want ErrBudget", err)
	}
	// The refused claim left no grant of its own...
	if _, err := h.store.Assignment(ctx, "asg_pin_1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused claim left a grant behind: %v", err)
	}
	// ...and the day now holds the lower ceiling, so the wide policy that
	// arrives afterwards is judged against it rather than against its own
	// number.
	if _, err := claimWith(t, h, "asg_pin_2", testRun, 1, policy); !errors.Is(err, ErrBudget) {
		t.Fatalf("claim under the original policy error = %v, want ErrBudget: the tightening was rolled back",
			err)
	}
	var daily, perCycle float64
	if err := h.store.db.QueryRow(`SELECT daily_cost, per_cycle_cost FROM evaluation_budget_day
		WHERE day = ?`, dayOf(h.clock.at)).Scan(&daily, &perCycle); err != nil {
		t.Fatalf("read the pinned day: %v", err)
	}
	if daily != 1 || perCycle != 1 {
		t.Fatalf("the day is pinned at (%v, %v), want (1, 1): the refused claim's tightening was lost",
			daily, perCycle)
	}
}

// A policy that lowers one ceiling and raises the other stays usable: it is
// judged against the conservative pair, and only its raise waits for the next
// UTC day.
func TestMixedCapChangeStaysUsableAndRaisesWaitForTheNextDay(t *testing.T) {
	h := newHarness(t)
	policy := testPolicy()
	policy.DailyCost = 10
	policy.PerCycleCost = 4
	if _, err := claimWith(t, h, "asg_mixed_0", testRun, 1, policy); err != nil {
		t.Fatalf("claim: %v", err)
	}

	mixed := policy
	mixed.DailyCost = 8    // lower: in force at once
	mixed.PerCycleCost = 6 // raise: not before the next day
	if _, err := claimWith(t, h, "asg_mixed_1", testRun, 3, mixed); err != nil {
		t.Fatalf("the mixed policy was refused outright: %v", err)
	}
	if _, err := claimWith(t, h, "asg_mixed_2", testRun, 1, mixed); !errors.Is(err, ErrBudget) {
		t.Fatalf("claim over the pinned cycle ceiling error = %v, want ErrBudget: a raise took effect mid-day",
			err)
	}

	// The next UTC day opens at what the policy offers then, raise included.
	h.clock.at = h.clock.at.Add(24 * time.Hour)
	if _, err := claimWith(t, h, "asg_mixed_3", testRun, 5, mixed); err != nil {
		t.Fatalf("the raise never took effect on the next day: %v", err)
	}
}

// Spend is reported as finite and non-negative or not at all, an overrun is
// charged in full and reported as an overrun on every retry of the identical
// receipt, and the completion it belongs to is still recorded.
func TestOverrunIsChargedInFullAndReportedOnEveryRetry(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	granted := h.claim(t, "asg_over", RoleReception, testRun)

	for _, cost := range []float64{math.NaN(), math.Inf(1), -0.1} {
		if err := h.store.coord.Finish(ctx, granted.ID, testRun, granted.Fence, cost); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a finish reporting %v error = %v, want ErrInvalid", cost, err)
		}
	}
	if err := h.store.coord.Finish(ctx, granted.ID, testRun, granted.Fence, 2.5); !errors.Is(err, ErrOverrun) {
		t.Fatalf("overrun finish error = %v, want ErrOverrun", err)
	}
	if err := h.store.coord.Finish(ctx, granted.ID, testRun, granted.Fence, 2.5); !errors.Is(err, ErrOverrun) {
		t.Fatalf("the identical retry error = %v, want the same ErrOverrun", err)
	}
	if err := h.store.coord.Finish(ctx, granted.ID, testRun, granted.Fence, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second cost for one attempt error = %v, want ErrConflict", err)
	}
	total, _, err := dayCharge(ctx, h.store.db, dayOf(h.clock.at), testRun)
	if err != nil {
		t.Fatalf("day charge: %v", err)
	}
	if total != 2.5 {
		t.Fatalf("the day carries %v, want the 2.5 that was actually spent", total)
	}

	// The work is not discarded: its cost is already charged, so throwing the
	// result away would leave the spend standing with nothing to show for it.
	second := h.claim(t, "asg_over_record", RoleReception, testRun)
	record, err := h.store.Submit(ctx, Submission{
		AssignmentID: second.ID, RunID: testRun, Fence: second.Fence,
		Assessment: &Assessment{Vote: VoteSupport}, Cost: 3,
	})
	if err != nil {
		t.Fatalf("a completion that overran its reservation: %v", err)
	}
	if record.ID == "" {
		t.Fatalf("an accounted overrun discarded the result")
	}
}

// A review that outlives its lease is not a crashed worker, and this
// deployment paid to learn the difference: on 2026-09-12 four review runs took
// 386s, 461s, 556s and 630s under a 240s lease, and every one of them was
// refused its read context - "the lease on assignment ... expired" - for a
// claim it had never stopped holding. Nothing was published and the store
// holds zero reviewer votes as a result.
//
// So the first half of this test is that failure, and the second is the fix:
// a holder that renews at a third of its lease works for as long as it keeps
// answering, and its assessment lands.
func TestALongReviewKeepsItsClaimByRenewingTheLease(t *testing.T) {
	ctx := context.Background()
	policy := testPolicy()
	lease := time.Duration(policy.LeaseSeconds) * time.Second

	lapsed := newHarness(t)
	stale := lapsed.claim(t, "asg_lapsed", RoleReception, testRun)
	lapsed.clock.at = lapsed.clock.at.Add(lease + time.Second)
	err := lapsed.store.Expose(ctx, stale.ID, testRun, stale.Fence)
	if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("the read context after expiry = %v, want the recorded expired-lease conflict", err)
	}

	h := newHarness(t)
	granted := h.claim(t, "asg_long", RoleReception, testRun)
	// Three leases' worth of work, renewed on the cadence the runner ticks.
	step := lease / 3
	until := granted.CreatedAt.Add(3 * lease)
	for h.clock.at.Before(until) {
		h.clock.at = h.clock.at.Add(step)
		extended, err := h.store.RenewClaim(ctx, granted.ID, testRun, granted.Fence, policy)
		if err != nil {
			t.Fatalf("renew %s into the review: %v", h.clock.at.Sub(granted.CreatedAt), err)
		}
		if !extended.After(h.clock.at) {
			t.Fatalf("a renewal at %s answered with %s, which is not a window to work in",
				h.clock.at.Format(time.RFC3339), extended.Format(time.RFC3339))
		}
	}
	if elapsed := h.clock.at.Sub(granted.CreatedAt); elapsed <= lease {
		t.Fatalf("the review only ran %s, which is inside its original %s lease", elapsed, lease)
	}

	// The gate that refused the four lost runs now admits this one, and the
	// vote it produces is durable.
	if err := h.store.Expose(ctx, granted.ID, testRun, granted.Fence); err != nil {
		t.Fatalf("the read context after renewing: %v", err)
	}
	record, err := h.store.Submit(ctx, Submission{
		AssignmentID: granted.ID, RunID: testRun, Fence: granted.Fence,
		Assessment: &Assessment{Vote: VoteSupport}, Cost: 0.5,
		Provenance: Provenance{Model: "m", Profile: "p", Recipe: "r", RecipeVersion: 1, Blinded: true},
	})
	if err != nil {
		t.Fatalf("the assessment of a renewed claim: %v", err)
	}
	if record.Assessment == nil || record.Assessment.Vote != VoteSupport {
		t.Fatalf("the record carries %+v, want the support vote the review produced", record.Assessment)
	}
}

// Renewal is the holder's heartbeat and never a way back in. Every refusal
// here is a different worker or a different epoch asking for time on a claim
// that is not its own to extend.
func TestLeaseRenewalIsRefusedOnceTheClaimIsNotHeld(t *testing.T) {
	ctx := context.Background()
	policy := testPolicy()
	h := newHarness(t)
	granted := h.claim(t, "asg_renew_refused", RoleReception, testRun)

	if _, err := h.store.RenewClaim(ctx, granted.ID, otherRun, granted.Fence, policy); !errors.Is(err, ErrConflict) {
		t.Fatalf("renewal by another run = %v, want ErrConflict", err)
	}
	if _, err := h.store.RenewClaim(ctx, granted.ID, testRun, granted.Fence+1, policy); !errors.Is(err, ErrConflict) {
		t.Fatalf("renewal at an unheld fence = %v, want ErrConflict", err)
	}
	if _, err := h.store.RenewClaim(ctx, "asg_missing", testRun, 1, policy); !errors.Is(err, ErrNotFound) {
		t.Fatalf("renewal of an unknown assignment = %v, want ErrNotFound", err)
	}

	// Once the lease has lapsed the holder is refused too: the next claimer
	// may already have taken it, so an expired claim is released rather than
	// resurrected by the worker that let it go.
	h.clock.at = h.clock.at.Add(time.Duration(policy.LeaseSeconds+1) * time.Second)
	if _, err := h.store.RenewClaim(ctx, granted.ID, testRun, granted.Fence, policy); !errors.Is(err, ErrConflict) ||
		!strings.Contains(err.Error(), "expired") {
		t.Fatalf("renewal after expiry = %v, want the expired-lease conflict", err)
	}
	// A refused renewal writes no window: the claim is still expired.
	if err := h.store.ValidateClaim(ctx, granted.ID, testRun, granted.Fence); !errors.Is(err, ErrConflict) {
		t.Fatalf("a refused renewal extended the claim anyway: validate = %v", err)
	}

	// After a takeover the superseded epoch stays refused and the new holder
	// renews on its own fence.
	taken, err := claimWith(t, h, granted.ID, otherRun, 1, policy)
	if err != nil {
		t.Fatalf("takeover after expiry: %v", err)
	}
	if _, err := h.store.RenewClaim(ctx, granted.ID, testRun, granted.Fence, policy); !errors.Is(err, ErrConflict) {
		t.Fatalf("renewal from the superseded epoch = %v, want ErrConflict", err)
	}
	if _, err := h.store.RenewClaim(ctx, taken.ID, otherRun, taken.Fence, policy); err != nil {
		t.Fatalf("the current holder could not renew: %v", err)
	}

	// A finished claim has no lease left to extend.
	if _, err := h.store.Submit(ctx, Submission{
		AssignmentID: taken.ID, RunID: otherRun, Fence: taken.Fence,
		SkipReason: "nothing to check against", Cost: 0.1,
	}); err != nil {
		t.Fatalf("finish the claim with a skip: %v", err)
	}
	if _, err := h.store.RenewClaim(ctx, taken.ID, otherRun, taken.Fence, policy); !errors.Is(err, ErrConflict) {
		t.Fatalf("renewal of a finished claim = %v, want ErrConflict", err)
	}
}

// In shared mode the fleet owns the lease and the mirror follows it. A fleet
// refusal is the answer; a fleet extension has to reach the local row, because
// every local write path asks the mirror first and an unextended mirror would
// refuse the holder the fleet had just extended.
func TestSharedLeaseRenewalFollowsTheFleetAndReachesTheMirror(t *testing.T) {
	ctx := context.Background()
	policy := testPolicy()
	stub := &stubCoordinator{}
	h := newHarness(t, WithCoordinator(stub))
	granted := h.claim(t, "asg_shared_renew", RoleReception, testRun)

	stub.renewErr = fmt.Errorf("%w: assignment %s was taken over", ErrConflict, granted.ID)
	if _, err := h.store.RenewClaim(ctx, granted.ID, testRun, granted.Fence, policy); !errors.Is(err, ErrConflict) {
		t.Fatalf("a fleet refusal = %v, want ErrConflict", err)
	}
	if stub.renewals != 1 {
		t.Fatalf("the fleet was asked %d times, want once", stub.renewals)
	}

	stub.renewErr = nil
	lease := time.Duration(policy.LeaseSeconds) * time.Second
	h.clock.at = h.clock.at.Add(lease - time.Minute)
	if _, err := h.store.RenewClaim(ctx, granted.ID, testRun, granted.Fence, policy); err != nil {
		t.Fatalf("renew in shared mode: %v", err)
	}
	// Past the window the grant carried, which only the mirrored extension
	// can carry the holder through.
	h.clock.at = h.clock.at.Add(2 * time.Minute)
	if err := h.store.ValidateClaim(ctx, granted.ID, testRun, granted.Fence); err != nil {
		t.Fatalf("the mirror refused a claim the fleet extended: %v", err)
	}
}
