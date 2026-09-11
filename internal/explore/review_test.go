package explore_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// These cases are about SPEC §4.12 and E3 of docs/evaluation-lifecycle.md: one
// evaluation assignment carried out by the real worker boundary, against the
// same fixture engine every exploration test drives. What they defend is the
// shape of the authority and the accounting rather than the judgement — a vote
// invented to fill a field, a blinded review served Babel's own prior output,
// an outcome recorded with nothing behind it, or a claimed assignment left
// holding a reservation would each be the worker doing something other than
// what it was authorized to do.

// reviewAuthority is why every review in these tests happened.
var reviewAuthority = run.Authority{Kind: run.AuthorityPolicy, Ref: "evaluation:reception:asg-1"}

// fakeReviewService is internal/evaluation's service with planted answers. It
// records every submission, because the submissions are what these tests are
// about: one active assessment per assignment, a reconciliation for every
// claim that produced none, and never both.
type fakeReviewService struct {
	input    evaluation.ReviewInput
	inputErr error
	reviews  int

	submissions []evaluation.Submission
	submitErr   error
	record      evaluation.Record
	corrected   []string
	recoverErr  error
}

func (s *fakeReviewService) Correct(_ context.Context, id string,
	in evaluation.Submission) (evaluation.Record, error) {
	s.corrected = append(s.corrected, id)
	return s.Submit(context.Background(), in)
}

func (s *fakeReviewService) Recover(context.Context) error { return s.recoverErr }

func (s *fakeReviewService) Review(_ context.Context, a evaluation.Assignment) (evaluation.ReviewInput, error) {
	s.reviews++
	if s.inputErr != nil {
		return evaluation.ReviewInput{}, s.inputErr
	}
	in := s.input
	in.Assignment = a
	return in, nil
}

func (s *fakeReviewService) Submit(_ context.Context, in evaluation.Submission) (evaluation.Record, error) {
	s.submissions = append(s.submissions, in)
	if s.submitErr != nil {
		return evaluation.Record{}, s.submitErr
	}
	if in.Assessment == nil {
		// A skip and a failure are completions rather than records, which is
		// the store's own contract and the thing the runner must not treat as
		// a recorded assessment.
		return evaluation.Record{}, nil
	}
	return s.record, nil
}

// assessed reports the one submission that carried an assessment, and refuses
// to let a test pass on two.
func (s *fakeReviewService) assessed(t *testing.T) evaluation.Submission {
	t.Helper()
	var found []evaluation.Submission
	for _, in := range s.submissions {
		if in.Assessment != nil {
			found = append(found, in)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d submissions carried an assessment, want exactly one: %+v", len(found), s.submissions)
	}
	return found[0]
}

// reviewSubject is the record under review in these tests.
var reviewSubject = evaluation.Subject{Kind: "proposal", ID: "prop-7"}

// artifact builds the served projection of one proposal. The body is the
// producer's own payload, which is what the blind audit walks: this build does
// not own its shape, so a field carrying a judgement has to be caught in the
// bytes about to be sent.
func (h *harness) artifact(body map[string]any) evaluation.Artifact {
	h.t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		h.t.Fatalf("encode artifact body: %v", err)
	}
	return evaluation.Artifact{
		Subject:   reviewSubject,
		RootID:    "prop-root",
		HeadID:    "prop-7",
		RunID:     "run-producer",
		CreatedAt: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		Title:     "state constraints in the handoff template",
		Body:      encoded,
		Evidence:  []frontier.Evidence{h.evidence(0, "the record's own citation")},
		Criteria:  []evaluation.Criterion{{ID: "crit-1", Description: "the template carries a constraints section"}},
		// The operator's adopted criteria record, which is what a criterion
		// result is an answer to.
		CriteriaID: "evr-criteria-1",
		Status:     "open",
		Context:    evaluation.Context{Version: "ctx-9", Priority: 2, CurrentWork: true},
	}
}

// plainBody is a producer payload with nothing withheld in it.
func plainBody() map[string]any {
	return map[string]any{
		"title":   "state constraints in the handoff template",
		"problem": "constraints are restated after a change",
		"outcome": "the template carries them",
	}
}

// reviewConfig is the reviewer configuration these tests share. It is the
// harness's exploration configuration minus the frontier stores a review does
// not write to, plus the evaluation service it writes through.
func (h *harness) reviewConfig(svc explore.ReviewService, args []string,
	mutate ...func(*explore.ReviewConfig)) explore.ReviewConfig {
	h.t.Helper()
	cfg := explore.ReviewConfig{
		Service: svc,
		Recipes: h.recipes,
		Recipe:  "babel-triages-the-queue",
		Grant: worker.Grant{
			Capabilities: []worker.Capability{worker.CapabilityCorpusSearch},
			Disclosure:   worker.DisclosureLocal,
		},
		Profile: worker.ProfileRef{ID: "synthetic-profile", Revision: 1},
		Worker: worker.Config{
			Binary: fakeEnginePath,
			Args:   args,
			Limits: worker.Limits{
				HandshakeTimeout: 10 * time.Second,
				IdleTimeout:      10 * time.Second,
				ExitGrace:        5 * time.Second,
				TerminateGrace:   500 * time.Millisecond,
			},
		},
		Runs:         h.runs,
		Ledger:       h.ledger,
		Index:        h.index,
		Capabilities: run.CapabilityVersions{Tool: "explore-test/1"},
	}
	for _, m := range mutate {
		m(&cfg)
	}
	return cfg
}

func (h *harness) reviewer(svc explore.ReviewService, args []string,
	mutate ...func(*explore.ReviewConfig)) *explore.Reviewer {
	h.t.Helper()
	reviewer, err := explore.NewReviewer(h.reviewConfig(svc, args, mutate...))
	if err != nil {
		h.t.Fatalf("NewReviewer: %v", err)
	}
	return reviewer
}

// assignment is one claimed review of reviewSubject in role.
func assignment(role, runID string) evaluation.Assignment {
	return evaluation.Assignment{
		ID:             "asg-" + role,
		Subject:        reviewSubject,
		RunID:          runID,
		Role:           role,
		PolicyVersion:  "policy-2",
		ContextVersion: "ctx-9",
		Seed:           99,
		InputDigest:    "sha256:abc",
		Fence:          4,
		ReservedCost:   0.05,
		Lane:           "coverage",
	}
}

// writeReview writes a review payload the fixture submits verbatim.
func (h *harness) writeReview(name string, res explore.ReviewResult) string {
	h.t.Helper()
	encoded, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		h.t.Fatalf("encode review result: %v", err)
	}
	return h.writeRaw(name, string(encoded))
}

// reviewArgs are the fixture flags for one review job. The selector is the
// stage parameter, exactly as an exploration's payloads are selected, because
// a review job carries the same parameter block.
func reviewArgs(payload string, extra ...string) []string {
	args := []string{"-submit-selector", explore.ParamStage,
		"-submit", string(explore.StageReview) + "=" + payload}
	return append(args, extra...)
}

// A bare support vote is a complete reception review. Nothing is invented to
// fill the result, the assessment is recorded once, and the receipt records the
// boundary that produced it.
func TestBareVoteIsACompleteReception(t *testing.T) {
	h := newHarness(t)
	svc := &fakeReviewService{
		input:  evaluation.ReviewInput{Artifact: h.artifact(plainBody())},
		record: evaluation.Record{ID: "evr-1", Kind: evaluation.KindAssessment},
	}
	payload := h.writeReview("vote.json", explore.ReviewResult{Vote: "support"})
	reviewer := h.reviewer(svc, reviewArgs(payload))

	out, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleReception, "rev-bare"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err != nil {
		t.Fatalf("Review: %v (failures %+v)", err, out.Failures)
	}
	in := svc.assessed(t)
	if in.Assessment.Vote != evaluation.VoteSupport {
		t.Errorf("recorded vote = %q, want support", in.Assessment.Vote)
	}
	if len(in.Assessment.Contributions) != 0 {
		t.Errorf("a bare vote arrived with %d contributions: %+v",
			len(in.Assessment.Contributions), in.Assessment.Contributions)
	}
	if in.Assessment.Outcome != "" {
		t.Errorf("a reception vote carried outcome %q", in.Assessment.Outcome)
	}
	if in.AssignmentID != "asg-reception" || in.Fence != 4 || in.RunID != "rev-bare" {
		t.Errorf("the submission does not name the claim it commits under: %+v", in)
	}
	if in.SkipReason != "" || in.FailedReason != "" {
		t.Errorf("an assessment arrived beside a skip or failure: %+v", in)
	}
	if !in.Provenance.Blinded {
		t.Error("a reception assessment was recorded as unblinded")
	}
	if in.Provenance.Recipe == "" || in.Provenance.RecipeVersion == 0 {
		t.Errorf("provenance lost the recipe contract: %+v", in.Provenance)
	}
	if out.Record.ID != "evr-1" {
		t.Errorf("the run reports record %q, want the one the store returned", out.Record.ID)
	}
	if out.Receipt == nil {
		t.Fatal("a supervised review wrote no receipt")
	}
	if out.Receipt.Body.Job.Schema != explore.ReviewResultSchema {
		t.Errorf("receipt records schema %q, want the evaluation result contract",
			out.Receipt.Body.Job.Schema)
	}
	if out.Receipt.Body.Worker == nil {
		t.Error("the receipt embeds no worker boundary")
	}
}

// A contribution with no vote is equally valid, and the reception role may not
// smuggle an observed outcome into the same submission.
func TestContributionWithoutAVoteIsValidAndOutcomeIsNot(t *testing.T) {
	h := newHarness(t)
	svc := &fakeReviewService{
		input:  evaluation.ReviewInput{Artifact: h.artifact(plainBody())},
		record: evaluation.Record{ID: "evr-2"},
	}
	payload := h.writeReview("contribution.json", explore.ReviewResult{
		Contributions: []evaluation.Contribution{{
			Kind: evaluation.ContributionEvidence,
			Text: "the cited record says something narrower than the claim",
			Evidence: []frontier.Evidence{
				h.evidence(1, "the locator the claim rests on"),
			},
		}},
		Uncertainty: "whether the narrower reading is what the author meant",
	})
	reviewer := h.reviewer(svc, reviewArgs(payload, "-call", worker.ToolSearch, "-search-query", ""))

	out, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleReception, "rev-contrib"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err != nil {
		t.Fatalf("Review: %v (failures %+v)", err, out.Failures)
	}
	in := svc.assessed(t)
	if in.Assessment.Vote != "" {
		t.Errorf("a contribution-only review recorded vote %q", in.Assessment.Vote)
	}
	if len(in.Assessment.Contributions) != 1 {
		t.Fatalf("recorded %d contributions, want one", len(in.Assessment.Contributions))
	}
	if in.Assessment.Uncertainty == "" {
		t.Error("the recorded uncertainty was dropped")
	}

	// The same role may not report an observed outcome: the schema it was
	// handed has no field for one, and a payload that names it anyway is
	// refused rather than stored.
	bad := h.writeReview("outcome-from-reception.json", explore.ReviewResult{Outcome: "verified"})
	svc2 := &fakeReviewService{input: evaluation.ReviewInput{Artifact: h.artifact(plainBody())}}
	reviewer2 := h.reviewer(svc2, reviewArgs(bad))
	out2, err := reviewer2.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleReception, "rev-outcome-refused"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err == nil {
		t.Fatal("a reception review recorded an observed outcome")
	}
	if out2.Failed == "" {
		t.Error("the refused review left its reservation unreconciled")
	}
	for _, in := range svc2.submissions {
		if in.Assessment != nil {
			t.Errorf("a refused review still recorded an assessment: %+v", in.Assessment)
		}
	}
}

// A blinded initial assessment is not offered a tool it could use to look the
// tally up: the search argument schema cannot express the frontier surface,
// and a worker that constructs one anyway is denied rather than served.
func TestBlindedReviewCannotReachBabelsOwnOutput(t *testing.T) {
	h := newHarness(t)
	contract, ok := explore.ReviewOutputContract(evaluation.RoleReception)
	if !ok {
		t.Fatal("the reception role has no result contract")
	}
	if strings.Contains(string(contract.JSONSchema), "\"outcome\"") {
		t.Error("the reception schema offers an outcome field the role may not fill")
	}

	svc := &fakeReviewService{
		input:  evaluation.ReviewInput{Artifact: h.artifact(plainBody())},
		record: evaluation.Record{ID: "evr-3"},
	}
	payload := h.writeReview("blind.json", explore.ReviewResult{Vote: "unsure"})
	reviewer := h.reviewer(svc, reviewArgs(payload,
		"-call", worker.ToolSearch, "-search-query", "constraints", "-search-scope", "frontier"))

	out, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleReception, "rev-blind"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err != nil {
		t.Fatalf("Review: %v (failures %+v)", err, out.Failures)
	}
	if !out.Blinded {
		t.Fatal("a reception review was not taken blind")
	}
	if out.Record.ID == "" {
		t.Error("a denied search cost the review its assessment; a denial is not a termination")
	}

	boundary := out.Receipt.Body.Worker
	if boundary == nil {
		t.Fatal("no worker boundary was recorded")
	}
	// The tool the job registered carries no surface selector at all, which
	// is the difference between blinding the prompt and blinding the run.
	var schema string
	for _, tool := range boundary.Tools {
		if tool == worker.ToolSearch {
			schema = tool
		}
	}
	if schema == "" {
		t.Fatal("the blinded job registered no corpus search")
	}
	denied := 0
	for _, req := range boundary.ToolRequests {
		if req.Tool != worker.ToolSearch {
			continue
		}
		if req.Allowed {
			t.Errorf("a frontier-scope search was served to a blinded review: %+v", req)
			continue
		}
		denied++
		if !strings.Contains(req.Reason, "blind") {
			t.Errorf("the denial reason %q does not say the assessment is blinded", req.Reason)
		}
	}
	if denied == 0 {
		t.Error("the frontier-scope search was neither served nor denied; it never happened")
	}
	if len(out.Disclosed) != 0 {
		t.Errorf("a blinded review was served %d retrievals from a denied surface", len(out.Disclosed))
	}
}

// Prior evaluations offered to a blinded role are a projection defect, and the
// review is refused before a worker is launched rather than prompted with
// material it may not read.
func TestBlindedReviewRefusesLeakedPriorEvaluations(t *testing.T) {
	h := newHarness(t)
	svc := &fakeReviewService{
		input: evaluation.ReviewInput{
			Artifact: h.artifact(plainBody()),
			Previous: []evaluation.Record{{
				ID:        "evr-prior",
				Kind:      evaluation.KindAssessment,
				Subject:   reviewSubject,
				ActorKind: evaluation.ActorRun,
				Assessment: &evaluation.Assessment{
					Vote: evaluation.VoteSupport,
				},
			}},
		},
	}
	payload := h.writeReview("never-read.json", explore.ReviewResult{Vote: "support"})
	reviewer := h.reviewer(svc, reviewArgs(payload))

	out, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleReception, "rev-leak"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if !errors.Is(err, explore.ErrReviewBlinded) {
		t.Fatalf("Review = %v, want the blinding refusal", err)
	}
	if out.Receipt != nil && out.Receipt.Body.Worker != nil {
		t.Error("a worker was launched for a review whose blind was already broken")
	}
	if len(svc.submissions) != 1 || svc.submissions[0].FailedReason == "" {
		t.Fatalf("the refused claim was not reconciled as failed: %+v", svc.submissions)
	}
	if svc.submissions[0].Assessment != nil {
		t.Error("a refused review recorded an assessment")
	}
}

// A producer payload carrying a rank or a tally is refused for a blinded role
// on the same terms, because the body is the one part of the projection this
// build does not own.
func TestBlindedReviewRefusesARankedBody(t *testing.T) {
	h := newHarness(t)
	body := plainBody()
	body["reception"] = map[string]any{"support": 4, "oppose": 1}
	svc := &fakeReviewService{input: evaluation.ReviewInput{Artifact: h.artifact(body)}}
	payload := h.writeReview("ranked.json", explore.ReviewResult{Vote: "support"})
	reviewer := h.reviewer(svc, reviewArgs(payload))

	_, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleReception, "rev-ranked"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if !errors.Is(err, explore.ErrReviewBlinded) {
		t.Fatalf("Review = %v, want the blinding refusal", err)
	}
	if !strings.Contains(err.Error(), "reception") {
		t.Errorf("the refusal %v does not name the withheld key", err)
	}
	if len(svc.submissions) != 1 || svc.submissions[0].FailedReason == "" {
		t.Fatalf("the refused claim was not reconciled: %+v", svc.submissions)
	}
}

// An observed outcome needs evidence, and a comparison reveals the prior
// evaluations a reception vote is blinded to. The two halves are one test
// because they are the same boundary read from both sides.
func TestOutcomeNeedsEvidenceAndComparisonRevealsPriors(t *testing.T) {
	h := newHarness(t)
	unevidenced := h.writeReview("unevidenced.json", explore.ReviewResult{Outcome: "verified"})
	svc := &fakeReviewService{input: evaluation.ReviewInput{Artifact: h.artifact(plainBody())}}
	reviewer := h.reviewer(svc, reviewArgs(unevidenced))

	_, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleOutcome, "rev-unevidenced"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err == nil {
		t.Fatal("an unevidenced verified outcome was recorded")
	}
	for _, in := range svc.submissions {
		if in.Assessment != nil {
			t.Errorf("an unevidenced outcome reached the store: %+v", in.Assessment)
		}
	}

	// The same role with evidence behind it records the outcome and the
	// criterion it was judged against.
	evidenced := h.writeReview("evidenced.json", explore.ReviewResult{
		Outcome: "partial",
		Results: []evaluation.CriterionResult{{
			CriterionID: "crit-1",
			Satisfied:   true,
			Evidence:    []frontier.Evidence{h.evidence(0, "the template as it stands")},
		}},
		Environment: "the archived sessions of this deployment",
		AsOf:        time.Date(2026, 9, 11, 9, 0, 0, 0, time.UTC),
		Uncertainty: "the effect on later handoffs is unmeasured",
	})
	svc2 := &fakeReviewService{
		input:  evaluation.ReviewInput{Artifact: h.artifact(plainBody())},
		record: evaluation.Record{ID: "evr-outcome"},
	}
	reviewer2 := h.reviewer(svc2, reviewArgs(evidenced, "-call", worker.ToolSearch, "-search-query", ""))
	out, err := reviewer2.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleOutcome, "rev-outcome"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err != nil {
		t.Fatalf("Review: %v (failures %+v)", err, out.Failures)
	}
	in := svc2.assessed(t)
	if in.Assessment.Outcome != evaluation.OutcomePartial {
		t.Errorf("recorded outcome = %q, want partial", in.Assessment.Outcome)
	}
	if in.Assessment.CriteriaID == "" {
		t.Error("an outcome was recorded with no criteria version behind it")
	}
	if in.Assessment.AsOf.IsZero() {
		t.Error("an outcome was recorded with no observation time")
	}

	// A comparison is the role the reveal is for: the priors travel, the
	// review is not blinded, and the provenance says so.
	compared := h.writeReview("compared.json", explore.ReviewResult{
		Contributions: []evaluation.Contribution{{
			Kind: evaluation.ContributionComparison,
			Text: "the mechanism is checkable where the practice is not",
			Alternatives: []evaluation.Subject{
				reviewSubject, {Kind: "proposal", ID: "prop-8"},
			},
			Preferred:   &evaluation.Subject{Kind: "proposal", ID: "prop-8"},
			WouldChange: "evidence that the practice holds without the template",
		}},
	})
	alt := h.artifact(plainBody())
	alt.Subject = evaluation.Subject{Kind: "proposal", ID: "prop-8"}
	alt.RunID = "run-other"
	svc3 := &fakeReviewService{
		input: evaluation.ReviewInput{
			Artifact:     h.artifact(plainBody()),
			Alternatives: []evaluation.Artifact{alt},
			Previous: []evaluation.Record{{
				ID: "evr-prior", Kind: evaluation.KindAssessment, Subject: reviewSubject,
				Assessment: &evaluation.Assessment{Vote: evaluation.VoteOppose},
			}},
		},
		record: evaluation.Record{ID: "evr-compare"},
	}
	reviewer3 := h.reviewer(svc3, reviewArgs(compared))
	out3, err := reviewer3.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleComparison, "rev-compare"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err != nil {
		t.Fatalf("Review: %v (failures %+v)", err, out3.Failures)
	}
	if out3.Blinded {
		t.Error("a comparison was taken blind; the reveal is what the role is for")
	}
	in3 := svc3.assessed(t)
	if in3.Provenance.Blinded {
		t.Error("provenance claims a blinded comparison")
	}
	if len(in3.Provenance.Consulted) == 0 {
		t.Error("a revealed comparison recorded nothing as consulted")
	}
	if in3.Assessment.Vote != "" {
		t.Errorf("a comparison minted the global vote %q", in3.Assessment.Vote)
	}
}

// A comparison may not prefer an alternative its own run authored, and the
// refusal is by the run that produced the record rather than by its wording.
func TestComparisonRefusesSelfBoost(t *testing.T) {
	h := newHarness(t)
	mine := h.artifact(plainBody())
	mine.Subject = evaluation.Subject{Kind: "proposal", ID: "prop-mine"}
	mine.RunID = "rev-self/review"
	svc := &fakeReviewService{
		input: evaluation.ReviewInput{
			Artifact:     h.artifact(plainBody()),
			Alternatives: []evaluation.Artifact{mine},
		},
	}
	payload := h.writeReview("self.json", explore.ReviewResult{
		Contributions: []evaluation.Contribution{{
			Kind: evaluation.ContributionComparison,
			Text: "mine is better",
			Alternatives: []evaluation.Subject{
				reviewSubject, {Kind: "proposal", ID: "prop-mine"},
			},
			Preferred: &evaluation.Subject{Kind: "proposal", ID: "prop-mine"},
		}},
	})
	reviewer := h.reviewer(svc, reviewArgs(payload))

	_, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleComparison, "rev-self"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err == nil {
		t.Fatal("a review preferred an alternative its own run authored")
	}
	for _, in := range svc.submissions {
		if in.Assessment != nil {
			t.Errorf("a self-boosting comparison was recorded: %+v", in.Assessment)
		}
	}
}

// A skip is not a vote. It completes the assignment, reconciles the
// reservation, and leaves the record with no judgement against it.
func TestSkipCompletesWithoutAVote(t *testing.T) {
	h := newHarness(t)
	svc := &fakeReviewService{input: evaluation.ReviewInput{Artifact: h.artifact(plainBody())}}
	payload := h.writeReview("skip.json", explore.ReviewResult{
		Skip: "the promised effect is latency, which this archive does not record",
	})
	reviewer := h.reviewer(svc, reviewArgs(payload))

	out, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleOutcome, "rev-skip"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err != nil {
		t.Fatalf("Review: %v (failures %+v)", err, out.Failures)
	}
	if out.Skipped == "" {
		t.Error("the run does not report the skip")
	}
	if len(svc.submissions) != 1 {
		t.Fatalf("%d submissions for one skip: %+v", len(svc.submissions), svc.submissions)
	}
	in := svc.submissions[0]
	if in.SkipReason == "" {
		t.Errorf("the submission carries no skip reason: %+v", in)
	}
	if in.Assessment != nil {
		t.Errorf("a skip arrived as an assessment: %+v", in.Assessment)
	}
	if out.Record.ID != "" {
		t.Errorf("a skip produced record %q", out.Record.ID)
	}
}

// A worker that ends without submitting reconciles its reservation as a
// failure rather than leaving the claim to expire, and the cost it reports is
// the cost that reaches the allowance.
func TestFailedWorkerReconcilesTheReservation(t *testing.T) {
	h := newHarness(t)
	svc := &fakeReviewService{input: evaluation.ReviewInput{Artifact: h.artifact(plainBody())}}
	reviewer := h.reviewer(svc, []string{"-no-submit"})

	out, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleReception, "rev-fail"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if err == nil {
		t.Fatal("a review that submitted nothing reported success")
	}
	if len(svc.submissions) != 1 {
		t.Fatalf("%d submissions for one failed review: %+v", len(svc.submissions), svc.submissions)
	}
	in := svc.submissions[0]
	if in.FailedReason == "" {
		t.Errorf("the failure was not delivered: %+v", in)
	}
	if in.Assessment != nil || in.SkipReason != "" {
		t.Errorf("a failure arrived as something else: %+v", in)
	}
	if in.Fence != 4 || in.RunID != "rev-fail" {
		t.Errorf("the reconciliation does not name the claim it releases: %+v", in)
	}
	// The fixture's engine reported its session accounting, so the failure
	// carries the spend that was measured rather than the reservation.
	if in.Unpriced {
		t.Errorf("a measured boundary was submitted as unpriced: %+v", in)
	}
	if in.Cost <= 0 {
		t.Errorf("a measured failure reported no spend at all: %+v", in)
	}
	if out.Receipt == nil {
		t.Error("a failed review wrote no receipt; the record of a failure is when it is needed")
	}
}

// A replayed attempt recognizes its own assessment and does not vote twice.
// The ledger binding is what makes that true, and it survives the process
// that wrote it.
func TestReplayedAssignmentDoesNotVoteTwice(t *testing.T) {
	h := newHarness(t)
	svc := &fakeReviewService{
		input:  evaluation.ReviewInput{Artifact: h.artifact(plainBody())},
		record: evaluation.Record{ID: "evr-replay"},
	}
	payload := h.writeReview("replay.json", explore.ReviewResult{Vote: "oppose"})
	reviewer := h.reviewer(svc, reviewArgs(payload))
	opt := explore.ReviewOptions{
		Assignment:  assignment(evaluation.RoleReception, "rev-replay"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	}

	first, err := reviewer.Review(context.Background(), opt)
	if err != nil {
		t.Fatalf("first Review: %v (failures %+v)", err, first.Failures)
	}
	second, err := reviewer.Review(context.Background(), opt)
	if err != nil {
		t.Fatalf("second Review: %v (failures %+v)", err, second.Failures)
	}
	if !second.Reused {
		t.Error("the replayed attempt did not recognize its own assessment")
	}
	if second.Record.ID != "evr-replay" {
		t.Errorf("the replayed attempt reports record %q, want the bound one", second.Record.ID)
	}
	if svc.reviews != 1 {
		t.Errorf("the replayed attempt was exposed to the record again (%d exposures)", svc.reviews)
	}
	svc.assessed(t)
	if len(svc.submissions) != 1 {
		t.Errorf("%d submissions across two attempts at one assignment", len(svc.submissions))
	}
	// Both attempts are one run identity, so the receipt chain is amended
	// rather than forked.
	revisions, err := h.runs.Revisions(context.Background(), "rev-replay")
	if err != nil {
		t.Fatalf("Revisions: %v", err)
	}
	if len(revisions) != 2 {
		t.Errorf("the run has %d receipt revisions across two attempts, want 2", len(revisions))
	}
}

// An unsupported role is refused before anything is claimed or launched: a
// missing evaluator is an explicit gap, never a permissive default.
func TestUnsupportedRoleIsRefused(t *testing.T) {
	h := newHarness(t)
	svc := &fakeReviewService{input: evaluation.ReviewInput{Artifact: h.artifact(plainBody())}}
	payload := h.writeReview("unsupported.json", explore.ReviewResult{Vote: "support"})
	reviewer := h.reviewer(svc, reviewArgs(payload))

	_, err := reviewer.Review(context.Background(), explore.ReviewOptions{
		Assignment:  assignment("clairvoyance", "rev-unsupported"),
		Preparation: h.prep,
		Authority:   reviewAuthority,
	})
	if !errors.Is(err, explore.ErrReviewRole) {
		t.Fatalf("Review = %v, want the unsupported-role refusal", err)
	}
	if svc.reviews != 0 {
		t.Error("an unsupported role was exposed to the record")
	}
	if len(svc.submissions) != 0 {
		t.Error("an unsupported role claimed and released an assignment it never held")
	}
}

// Every supported role has a contract, and the schema each is handed carries
// only the fields its role may fill. The table is the enforcement, so this
// reads it from the same place the runner does.
func TestEveryRoleHasAPrunedContract(t *testing.T) {
	for _, role := range evaluation.Roles() {
		contract, ok := explore.ReviewOutputContract(role)
		if !ok {
			t.Fatalf("role %q has no result contract", role)
		}
		if contract.Schema != explore.ReviewResultSchema {
			t.Errorf("role %q declares schema %q", role, contract.Schema)
		}
		if len(contract.JSONSchema) == 0 || contract.Instructions == "" {
			t.Errorf("role %q has an empty contract", role)
		}
		schema := string(contract.JSONSchema)
		hasVote := strings.Contains(schema, "\"vote\"")
		hasOutcome := strings.Contains(schema, "\"outcome\"")
		if want := role == evaluation.RoleReception; hasVote != want {
			t.Errorf("role %q vote field present = %t, want %t", role, hasVote, want)
		}
		if want := role == evaluation.RoleOutcome; hasOutcome != want {
			t.Errorf("role %q outcome field present = %t, want %t", role, hasOutcome, want)
		}
		if !slices.Contains([]string{
			evaluation.RoleReception, evaluation.RoleEvidence, evaluation.RoleChallenge,
			evaluation.RoleComparison, evaluation.RoleOutcome, evaluation.RoleRelevance,
		}, role) {
			t.Errorf("role %q is outside the vocabulary this build reviews under", role)
		}
	}
}
