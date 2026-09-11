package explore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/evaluation"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/presence"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// A review is one evaluation assignment carried out by the same supervised
// worker every exploration uses (SPEC.md §4.12, and E3 of
// docs/evaluation-lifecycle.md). It is a separate job rather than a fifth
// stage of an exploration because it answers a different question about
// already-durable output — is this received, does its evidence hold, did the
// promised outcome happen — and because its authority is bounded by a review
// role rather than by an exploration's development path.
//
// What it reuses is everything that makes an exploration auditable: one
// worker.Job through internal/worker, a generated result schema the engine
// validates structurally before Babel sees a submission, the brokered corpus
// index as the only read path, a run.Receipt recording the boundary, and the
// resume ledger that makes a replayed attempt recognize its own prior work
// instead of voting twice.
//
// What it adds is procedural blinding. An initial reception, evidence,
// outcome or relevance assessment is taken without the tallies, ranks and
// earlier evaluations the target has already collected — not because a model
// can be made to forget, but because Babel can be held to what it served.
// That obligation covers three surfaces and this file enforces all three: the
// prompt's rendered target, the job parameters, and the argument schema of
// every tool the job offers. A blinded job is not registered with a tool it
// could use to look the tally up, and the surface that would have answered is
// denied by policy as well, so a worker that guesses the wire format of a
// surface it was not offered is refused rather than served.

// StageReview is the run stage one review job records under. It is a Stage so
// that the resume ledger, the job parameters and the fixture selector a test
// drives are the same three mechanisms an exploration stage uses; it has no
// entry in the authority table because a review emits no frontier record and
// persists through internal/evaluation instead.
const StageReview Stage = "review"

// Versions of the review job's own inputs, recorded in its receipt for the
// reason every other version in this package is (§7): a later re-run is
// compared against the contract the earlier one applied, and a schema
// identifier that did not move when the payload shape did would make a stored
// assessment readable under the wrong contract.
const (
	// ReviewJobVersion 1 is the first evaluation job: a role-bounded result
	// schema, a blinded target projection, and the corpus index as the only
	// brokered read.
	ReviewJobVersion = 1
	// ReviewResultSchema names the payload shape a review submits. It is a
	// different schema from worker.ResultSchema rather than a variant of it:
	// an analysis result becomes frontier records and a review result becomes
	// an evaluation record, and a payload read under the other contract would
	// produce durable state nobody wrote.
	ReviewResultSchema = "babel.evaluation-result/1"
	// ReviewPromptVersion names the prompt this file composes.
	ReviewPromptVersion = "babel.evaluation-prompt/1"
	// ReviewBlindingPolicyVersion names the blinding rules applied: which
	// roles are taken blind, what the target projection withholds, which
	// parameters are withheld, and which tool surfaces are withdrawn.
	ReviewBlindingPolicyVersion = "babel.evaluation-blinding/1"
)

// Job parameters a review job carries. They are identifiers and vocabulary
// rather than content, which is what §9's plaintext allowlist admits, and the
// blinded set is deliberately smaller than the whole: see reviewParams.
const (
	// ParamReviewRole names the review role the job is running under, which
	// is what bounds the result it may submit.
	ParamReviewRole = "babel.review.role"
	// ParamReviewSubjectKind and ParamReviewSubjectID name the exact record
	// revision under review. The identity is the revision's own, never a
	// mutable chain head: a vote binds to the wording that was read.
	ParamReviewSubjectKind = "babel.review.subject.kind"
	ParamReviewSubjectID   = "babel.review.subject.id"
	// ParamReviewAssignment names the assignment, so a receipt, a submission
	// and a ledger binding can all be checked against one identity.
	ParamReviewAssignment = "babel.review.assignment"
	// ParamReviewPolicyVersion and ParamReviewContextVersion name the policy
	// and recorded-context versions the draw was made under (E4 requires both
	// to be replayable).
	ParamReviewPolicyVersion  = "babel.review.policy"
	ParamReviewContextVersion = "babel.review.context"
	// ParamReviewBlinded states whether this assessment is being taken blind.
	// It is told to the worker rather than hidden from it: a model that knows
	// it has not been shown the reception is a model that will not claim to
	// have weighed it.
	ParamReviewBlinded = "babel.review.blinded"
	// ParamReviewLane names the allocation lane the draw came from. It is
	// withheld from a blinded job: a lane reserved for never-reviewed work
	// says something about the target's prior evaluations, which is exactly
	// what a blinded assessment may not be told.
	ParamReviewLane = "babel.review.lane"
)

// Failure codes a review records in its receipt, beside this package's own.
const (
	// FailureReviewInput reports that the evaluation service would not hand
	// over the review's read context: an expired claim, a fence a takeover
	// moved, a subject whose revision is gone.
	FailureReviewInput = "review-input"
	// FailureReviewRole reports a result carrying material the assignment's
	// role has no authority to submit — an outcome from a reception vote, a
	// global vote from a comparison.
	FailureReviewRole = "review-role"
	// FailureReviewBlinding reports that the material Babel was about to
	// serve a blinded assessment carried the tallies, ranks or prior
	// evaluations the blind exists to withhold. The job is not launched: the
	// remedy is the projection that produced the leak, never a prompt that
	// asks the model to ignore what it read.
	FailureReviewBlinding = "review-blinding"
	// FailureReviewSubmit reports that the assessment could not be recorded.
	// The worker boundary already happened, so the receipt is the record of
	// it and the assignment's reservation is reconciled as failed rather than
	// left to expire.
	FailureReviewSubmit = "review-submit"
)

// ErrReviewBlinded reports material that would have broken a blinded
// assessment's blind. It is enforcement rather than advice, on the same terms
// as ErrRedactionRequired: the job does not start.
var ErrReviewBlinded = errors.New("explore: the review's read context would disclose prior evaluations")

// ErrReviewRole reports a submission outside the assignment's role authority.
var ErrReviewRole = errors.New("explore: the review role has no authority for this result")

// ReviewVote is the reception vocabulary a reception review may submit. The
// values come from internal/evaluation so the schema's enum, the store's
// validation and this package's refusal cannot disagree; an absent vote is
// the empty string and is deliberately not one of them, because "I did not
// vote" is a statement a schema should let a worker make by omission rather
// than by naming a value.
type ReviewVote string

// Values lists the reception vocabulary for the generated schema.
func (ReviewVote) Values() []string { return evaluation.Votes() }

// ObservedOutcome is the observed-outcome vocabulary an outcome review may
// submit (§4.12's full lifecycle). Its values come from internal/evaluation
// for ReviewVote's reason.
type ObservedOutcome string

// Values lists the outcome vocabulary for the generated schema.
func (ObservedOutcome) Values() []string { return evaluation.Outcomes() }

// ReviewResult is one accepted review submission.
//
// Every field is optional and absence is a statement. A bare vote with no
// prose is a valid reception review, a contribution with no vote is a valid
// review of any role that may contribute, and a skip is a review that
// declined to judge — which is not a vote and is recorded as neither. What a
// role is allowed to fill is not uniform, so the schema is pruned per role
// and the same table refuses a result that arrived with more.
//
// The payload types are internal/evaluation's own. The schema a worker is
// handed and the record a store writes are generated from one declaration for
// the reason the exploration result reuses the frontier's payloads: a second
// declaration is the one a new field silently misses.
type ReviewResult struct {
	// Vote is the reception judgement. Reception only.
	Vote ReviewVote `json:"vote,omitempty"`
	// Contributions are the optional comment, new evidence, refinement or
	// named comparison this review offers.
	Contributions []evaluation.Contribution `json:"contributions,omitempty"`
	// Outcome is the observed implementation or outcome state. Outcome role
	// only, and only with evidence behind it.
	Outcome ObservedOutcome `json:"outcome,omitempty"`
	// Results are the per-criterion findings an evidence or outcome review
	// records against the criteria version it was given.
	Results     []evaluation.CriterionResult `json:"results,omitempty"`
	Environment string                       `json:"environment,omitempty"`
	AsOf        time.Time                    `json:"as_of,omitzero"`
	// Uncertainty is what this review could not settle. It is free text
	// because the honest answer to "what are you unsure about" is prose, and
	// a closed vocabulary would make a model choose the nearest wrong label.
	Uncertainty string `json:"uncertainty,omitempty"`
	// Skip is the reason this assignment was declined: an unsupported
	// subject, a target whose evidence this worker cannot reach. A skip is
	// not a vote and never becomes one.
	Skip string `json:"skip,omitempty"`
}

// reviewAuthority is what one role's result may contain. It is the review
// counterpart of the exploration's authority table and it is the enforcement
// rather than a comment about it: the generated schema omits what the role may
// not fill, and parseReviewResult refuses a result that carried it anyway.
type reviewAuthority struct {
	// vote admits the reception vocabulary. Reception is the only role that
	// has it: §4.12 separates reception from evidence, relevance and observed
	// outcomes, and a comparison that could mint a global vote would turn
	// "B is better here" into an endorsement of B everywhere.
	vote bool
	// outcome admits an observed-outcome claim and requires evidence behind
	// it. Only the outcome role has it, so no reception vote can become a
	// verified result by being recorded in the same field.
	outcome bool
	// criteria admits per-criterion results, which is what an evidence check
	// and an outcome verification produce. A reception vote does not meet an
	// evidence-check obligation, so reception does not have it.
	criteria bool
	// alternatives admits contributions that name alternatives and a
	// preference. Comparison only: an alternative preferred in a named
	// context is the comparison role's whole output.
	alternatives bool
}

// reviewAuthorities is the role table. A role absent from it is unsupported by
// this build, which is an explicit gap rather than a silently permissive
// default: NewReviewer refuses an assignment carrying one.
var reviewAuthorities = map[string]reviewAuthority{
	evaluation.RoleReception:  {vote: true},
	evaluation.RoleEvidence:   {criteria: true},
	evaluation.RoleChallenge:  {},
	evaluation.RoleComparison: {alternatives: true},
	evaluation.RoleOutcome:    {outcome: true, criteria: true},
	evaluation.RoleRelevance:  {},
}

// ReviewOutputContract is the result contract one role's review job is held
// to: the schema identifier its receipt records, the JSON Schema the submit
// tool is registered with, and the role's instructions.
//
// It is generated per role at start-up from ReviewResult and the role table,
// so a field added to evaluation.Contribution reaches the model on the next
// run with no edit here.
func ReviewOutputContract(role string) (worker.OutputContract, bool) {
	contract, ok := reviewContracts[role]
	return contract, ok
}

var reviewContracts = func() map[string]worker.OutputContract {
	contracts := make(map[string]worker.OutputContract, len(reviewAuthorities))
	for role, auth := range reviewAuthorities {
		schema, err := reviewSchema(auth)
		if err != nil {
			panic("explore: generate the review result schema: " + err.Error())
		}
		contracts[role] = worker.OutputContract{
			Schema:       ReviewResultSchema,
			JSONSchema:   schema,
			Instructions: reviewInstructions(role, auth),
		}
	}
	return contracts
}()

// reviewSchema generates the JSON Schema (draft 2020-12) of ReviewResult as
// one role may fill it, pruning the fields the role has no authority for.
func reviewSchema(auth reviewAuthority) (json.RawMessage, error) {
	g := &schemaGenerator{defs: map[string]*object{}}
	name := reflect.TypeFor[ReviewResult]().Name()
	if _, err := g.describe(reflect.TypeFor[ReviewResult]()); err != nil {
		return nil, err
	}
	root := g.defs[name]
	delete(g.defs, name)
	doc := &object{}
	doc.set("$schema", "https://json-schema.org/draft/2020-12/schema")
	doc.set("title", ReviewResultSchema)
	for _, key := range root.keys {
		doc.set(key, root.get(key))
	}
	if !auth.vote {
		doc.remove("vote")
	}
	if !auth.outcome {
		doc.remove("outcome")
	}
	if !auth.criteria {
		doc.remove("results")
		doc.remove("environment")
		doc.remove("as_of")
	}
	defs := &object{}
	for _, defName := range g.reachable(root) {
		defs.set(defName, g.defs[defName])
	}
	if len(defs.keys) > 0 {
		doc.set("$defs", defs)
	}
	return json.Marshal(doc)
}

// Errors a review submission can produce. They are sentinels for the reason
// the exploration result's are: "the payload is not this contract", "the role
// may not say that", and "an outcome was claimed with nothing behind it" are
// three different things to fix.
var (
	// ErrReviewEmpty reports a submission that judged nothing. It is refused
	// rather than stored, because a review that neither voted, contributed,
	// recorded a criterion, reported an outcome nor skipped has not told
	// Babel anything, and recording it would consume the assignment while
	// leaving the target exactly as unreviewed as before.
	ErrReviewEmpty = errors.New("explore: the review submitted no vote, contribution, criterion result, outcome or skip reason")
	// ErrReviewVocabulary reports a vote or outcome outside the closed
	// vocabulary. The engine validates the schema's enum, so reaching this
	// means the far side submitted a value its own schema forbade.
	ErrReviewVocabulary = errors.New("explore: the review used a vote or outcome outside the closed vocabulary")
	// ErrReviewUnsupportedOutcome reports an observed outcome with no
	// evidence behind it. §4.12 is explicit that missing or partial evidence
	// is not success, and an unevidenced "verified" is the one claim this
	// package must never persist.
	ErrReviewUnsupportedOutcome = errors.New("explore: an observed outcome needs evidence")
	// ErrReviewSelfBoost reports a review promoting work its own run
	// authored: a support vote or a claimed implementation on a record this
	// run produced, or a preference for an alternative it just wrote.
	// Independence is the whole value of the judgement, and a run that could
	// endorse its own output would be scoring its own work. A bare
	// opposition or uncertainty from the same run is not refused — a run
	// arguing against what it produced is the honest direction.
	ErrReviewSelfBoost = errors.New("explore: a review may not promote work its own run authored")
)

// reviewSelf is what this run authored among the records it was handed, which
// is what the self-boost refusal is checked against.
//
// It is computed from the artifacts the service served rather than from the
// worker's submission, because the submission names subjects and only the
// served artifact says which run produced one. The comparison is on the run's
// base identity, so a stage of one run cannot endorse another stage of itself.
type reviewSelf struct {
	// target reports that the record under review came out of this run.
	target bool
	// subjects are the alternative subject identities this run authored.
	subjects map[string]bool
}

// reviewSelfOf resolves what runID authored among an assignment's read
// context.
func reviewSelfOf(runID string, input evaluation.ReviewInput) reviewSelf {
	base := runBaseID(runID)
	self := reviewSelf{subjects: map[string]bool{}}
	if base != "" && runBaseID(input.Artifact.RunID) == base {
		self.target = true
	}
	for _, alt := range input.Alternatives {
		if base != "" && runBaseID(alt.RunID) == base {
			self.subjects[alt.Subject.ID] = true
		}
	}
	return self
}

// runBaseID reduces a stage run identity to the exploration it belongs to.
// §5.4's separate passes carry identities of the form "<run>/challenge", and
// two stages of one run are one author.
func runBaseID(runID string) string {
	if i := strings.IndexByte(runID, '/'); i >= 0 {
		return runID[:i]
	}
	return runID
}

// priorByRun reports the record this run already stated about the assignment's
// subject, which is what a revealed second pass corrects rather than votes
// beside.
//
// It reads the served read context rather than querying, because the only
// prior statement this pass may act on is one it was actually shown: a
// correction of a record the service withheld would be a write about material
// the run never read.
func priorByRun(input evaluation.ReviewInput, runID string) string {
	base := runBaseID(runID)
	if base == "" {
		return ""
	}
	for _, prior := range input.Previous {
		if prior.Kind != evaluation.KindAssessment || prior.Assessment == nil {
			continue
		}
		if prior.Subject != input.Assignment.Subject {
			continue
		}
		if runBaseID(prior.Provenance.RunID) == base {
			return prior.ID
		}
	}
	return ""
}

// parseReviewResult decodes and validates one submission against the role's
// authority. It checks shape, vocabulary and support, and nothing about
// whether the judgement is correct: Babel validates structure and provenance
// (§6.5), and a reception vote has no truth condition to check.
func parseReviewResult(rec *worker.ResultRecord, role string, self reviewSelf) (*ReviewResult, error) {
	if rec == nil {
		return nil, fmt.Errorf("%w: no submission was accepted", ErrReviewEmpty)
	}
	if rec.Schema != ReviewResultSchema {
		return nil, fmt.Errorf("explore: review result declares schema %q, not %q", rec.Schema, ReviewResultSchema)
	}
	auth, ok := reviewAuthorities[role]
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a role this build reviews under", ErrReviewRole, role)
	}
	decoder := json.NewDecoder(strings.NewReader(string(rec.Payload)))
	decoder.DisallowUnknownFields()
	var res ReviewResult
	if err := decoder.Decode(&res); err != nil {
		return nil, fmt.Errorf("explore: review result: %w", err)
	}
	res.Uncertainty = strings.TrimSpace(res.Uncertainty)
	res.Skip = strings.TrimSpace(res.Skip)
	res.Environment = strings.TrimSpace(res.Environment)
	if res.Skip != "" && (res.Vote != "" || res.Outcome != "" || len(res.Contributions) > 0 ||
		len(res.Results) > 0 || res.Uncertainty != "") {
		return nil, fmt.Errorf("explore: a skip cannot also state an assessment")
	}

	if res.Vote != "" && !slices.Contains(ReviewVote.Values(res.Vote), string(res.Vote)) {
		return nil, fmt.Errorf("%w: vote %q", ErrReviewVocabulary, res.Vote)
	}
	if res.Outcome != "" && !slices.Contains(ObservedOutcome.Values(res.Outcome), string(res.Outcome)) {
		return nil, fmt.Errorf("%w: outcome %q", ErrReviewVocabulary, res.Outcome)
	}
	switch {
	case res.Vote != "" && !auth.vote:
		return nil, fmt.Errorf("%w: the %s role may not cast a reception vote", ErrReviewRole, role)
	case res.Outcome != "" && !auth.outcome:
		return nil, fmt.Errorf("%w: the %s role may not report an observed outcome", ErrReviewRole, role)
	case len(res.Results) > 0 && !auth.criteria:
		return nil, fmt.Errorf("%w: the %s role may not record criterion results", ErrReviewRole, role)
	case (res.Environment != "" || !res.AsOf.IsZero()) && !auth.criteria:
		return nil, fmt.Errorf("%w: the %s role may not report outcome scope", ErrReviewRole, role)
	}
	for i, contribution := range res.Contributions {
		kind := strings.TrimSpace(contribution.Kind)
		if !slices.Contains(evaluation.ContributionKinds(), kind) {
			return nil, fmt.Errorf("%w: contribution %d is of kind %q", ErrReviewVocabulary, i+1, contribution.Kind)
		}
		compares := kind == evaluation.ContributionComparison
		if compares && !auth.alternatives {
			return nil, fmt.Errorf("%w: the %s role may not compare alternatives", ErrReviewRole, role)
		}
		if !compares && (len(contribution.Alternatives) > 0 || contribution.Preferred != nil) {
			return nil, fmt.Errorf("explore: contribution %d is a %s and may not name alternatives", i+1, kind)
		}
		switch {
		case kind == evaluation.ContributionEvidence && len(contribution.Evidence) == 0:
			return nil, fmt.Errorf("explore: contribution %d offers evidence and cites none", i+1)
		case compares && len(contribution.Alternatives) < 2:
			return nil, fmt.Errorf("explore: contribution %d compares %d alternatives; a comparison needs at least two",
				i+1, len(contribution.Alternatives))
		case compares && contribution.Preferred != nil &&
			!slices.Contains(contribution.Alternatives, *contribution.Preferred):
			return nil, fmt.Errorf("explore: contribution %d prefers an alternative it did not compare", i+1)
		case !compares && strings.TrimSpace(contribution.Text) == "" && len(contribution.Evidence) == 0:
			return nil, fmt.Errorf("explore: contribution %d carries neither text nor evidence", i+1)
		}
		if contribution.Preferred != nil && self.subjects[contribution.Preferred.ID] {
			return nil, fmt.Errorf("%w: alternative %s", ErrReviewSelfBoost, contribution.Preferred.ID)
		}
	}
	if self.target && (res.Vote == evaluation.VoteSupport ||
		res.Outcome == evaluation.OutcomeImplemented || res.Outcome == evaluation.OutcomeVerified) {
		return nil, fmt.Errorf("%w: the record under review", ErrReviewSelfBoost)
	}
	if res.Outcome != "" && res.Outcome != evaluation.OutcomeUnverifiable && len(reviewEvidence(&res)) == 0 {
		return nil, fmt.Errorf("%w: outcome %q", ErrReviewUnsupportedOutcome, res.Outcome)
	}
	if (res.Outcome != "" || len(res.Results) > 0) && (res.Environment == "" || res.AsOf.IsZero()) {
		return nil, fmt.Errorf("%w: criterion and outcome results need an explicit environment and as-of time", ErrReviewUnsupportedOutcome)
	}
	if res.Outcome == evaluation.OutcomeUnverifiable && res.Uncertainty == "" {
		return nil, fmt.Errorf("%w: an unverifiable outcome must name what could not be checked", ErrReviewUnsupportedOutcome)
	}
	if res.Vote == "" && res.Outcome == "" && res.Skip == "" &&
		len(res.Contributions) == 0 && len(res.Results) == 0 {
		return nil, ErrReviewEmpty
	}
	return &res, nil
}

// reviewEvidence flattens every citation a submission carries, which is what
// an observed outcome is checked for support against and what the served-trace
// check verifies.
func reviewEvidence(res *ReviewResult) []frontier.Evidence {
	var out []frontier.Evidence
	for _, contribution := range res.Contributions {
		out = append(out, contribution.Evidence...)
	}
	for _, result := range res.Results {
		out = append(out, result.Evidence...)
	}
	return out
}

// ReviewService is internal/evaluation's service as this runner uses it: the
// role-specific read context for one assignment, and the one write that ends
// it.
//
// It is declared here rather than in internal/evaluation because it is the
// consumer's view, on the same terms ResearchBroker is. Everything that makes
// a submission safe — the claim validation, the fence, the one-active-vote
// rule, the reservation accounting — belongs to the store and is not this
// package's to restate. What this package needs is the two calls it makes.
type ReviewService interface {
	// Review validates the claim, records the exposure, and returns what the
	// role may read. It withholds prior evaluations for every blinded role.
	Review(ctx context.Context, a evaluation.Assignment) (evaluation.ReviewInput, error)
	// Submit records the assessment, or the skip or failure that ended the
	// assignment instead, and reconciles its reservation either way.
	Submit(ctx context.Context, in evaluation.Submission) (evaluation.Record, error)
	// Correct appends a linked correction to an earlier statement this run
	// made, settling the new assignment's reservation. It is a separate call
	// rather than a flag on Submit because the two say different things: a
	// submission is this assignment's first statement, and a correction
	// preserves an earlier one while superseding it.
	Correct(ctx context.Context, id string, in evaluation.Submission) (evaluation.Record, error)
	// Recover settles whatever a cancelled or crashed worker left claimed. It
	// is called after a review that did not finish cleanly, on the detached
	// context, because the next cycle must not meet a phantom active
	// assignment holding a share of the allowance.
	Recover(ctx context.Context) error
}

// ReviewConfig is everything a reviewer needs that does not change between
// assignments.
type ReviewConfig struct {
	// Service is the evaluation service the review reads its context from
	// and writes its assessment to.
	Service ReviewService

	// Recipes are the cookbook assets this build reviews under, and Recipe
	// is the one whose body the prompt carries. Both are required: a review
	// with no recipe would be a judgement with no stated method, and §7 makes
	// the recipe version part of what a re-run is compared against.
	Recipes *cookbook.Set
	Recipe  string

	// Grant is the review's capability boundary, fixed before work starts.
	// internal/worker enforces it ahead of any policy, so nothing here can
	// widen it.
	Grant   worker.Grant
	Profile worker.ProfileRef
	Worker  worker.Config

	// Policy narrows the grant further, per operator negotiation. The
	// blinding policy is composed in front of it, so an operator narrowing
	// and a blind both apply and neither can widen the other.
	Policy worker.Authorizer

	// Runs and Ledger are the durable stores a review shares with every other
	// run on this machine: the receipt chain, and the resume ledger that
	// makes a replayed attempt recognize its own assessment.
	Runs   *run.Store
	Ledger *Ledger

	// Index is the retrieval index a review's corpus search is served from.
	// A review with no index has retrieval denied with that reason rather
	// than answered with nothing.
	Index *index.Index

	// Research is the public-research broker, required exactly when the grant
	// carries the capability and forbidden when it does not.
	Research ResearchBroker

	// Transcript opens the session log the review's conversation is archived
	// into. Nil is the feature quietly absent.
	Transcript func(runID, job string) (TranscriptWriter, error)

	// Presence announces the review to the shared catalog while it happens.
	// Nil is the feature quietly absent.
	Presence presence.Announcer

	// Redact applies §3's step 4 to everything Babel serves, and Thresholds
	// overrides preflight's calibrated limits.
	Redact     bool
	Thresholds *preflight.Thresholds

	// Capabilities names the build of each facility that enforced the grant.
	Capabilities run.CapabilityVersions

	// Now is the clock, injectable so a test's receipts are deterministic.
	Now func() time.Time
}

// Reviewer carries out evaluation assignments.
type Reviewer struct {
	cfg    ReviewConfig
	assets []run.CookbookAsset
	recipe *cookbook.Recipe
	now    func() time.Time
}

// NewReviewer validates cfg and returns a reviewer. It performs no I/O and
// launches nothing: every reason a review cannot happen that is knowable from
// configuration alone is reported before an operator has waited for a worker.
func NewReviewer(cfg ReviewConfig) (*Reviewer, error) {
	if cfg.Service == nil {
		return nil, fmt.Errorf("explore: a review needs the evaluation service it reads and writes through")
	}
	if cfg.Runs == nil || cfg.Ledger == nil {
		return nil, fmt.Errorf("explore: a review needs the receipt store and the resume ledger")
	}
	if cfg.Recipes == nil || cfg.Recipe == "" {
		return nil, fmt.Errorf("explore: a review needs the cookbook recipe that states its method")
	}
	recipe, ok := cfg.Recipes.ByID(cfg.Recipe)
	if !ok {
		return nil, fmt.Errorf("explore: recipe %q is not in the selected cookbook", cfg.Recipe)
	}
	if cfg.Worker.Binary == "" {
		return nil, fmt.Errorf("explore: no analysis worker binary configured")
	}
	switch cfg.Grant.Disclosure {
	case worker.DisclosureLocal, worker.DisclosureHosted:
	default:
		return nil, fmt.Errorf("explore: unknown disclosure class %q", cfg.Grant.Disclosure)
	}
	if err := validateFacilities(cfg.Grant, cfg.Capabilities); err != nil {
		return nil, err
	}
	if err := validateResearchGrant(cfg.Grant, cfg.Research); err != nil {
		return nil, err
	}
	assets, err := cookbookAssets(cfg.Recipes)
	if err != nil {
		return nil, err
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Reviewer{
		cfg:    cfg,
		assets: assets,
		recipe: recipe,
		now:    func() time.Time { return now().UTC() },
	}, nil
}

// ReviewOptions is one attempt at one assignment.
type ReviewOptions struct {
	// Assignment is the drawn review, as the evaluation service claimed it.
	Assignment evaluation.Assignment

	// Preparation is the corpus scope the review reads, fixed before work
	// starts (§2.6) and recorded in the receipt. It bounds every search the
	// review's broker serves.
	Preparation run.Preparation

	// Authority is why this review is happening (#96): the operator command
	// or the standing policy that authorized it. It is required, because "no
	// nameable authority, no run" applies to a review as much as to an
	// exploration.
	Authority run.Authority

	// Budget bounds what this review spends on retrieval and egress.
	Budget Budget

	// StopFile and OnProgress are the same two hooks an exploration takes.
	StopFile   string
	OnProgress func(worker.ProgressRecord)

	// Corrects names an earlier statement this pass was drawn to supersede,
	// for a caller that knows it. Empty lets the runner find it in the
	// revealed read context, which is the ordinary path: a correction is a
	// revealed second pass under a newly claimed assignment, so the extra
	// compute is reserved before it is spent either way. It is refused in a
	// blinded role, whose whole premise is not having read the record it
	// would be correcting.
	Corrects string
}

// ReviewRun is what one attempt at one assignment did. It is returned even
// when the attempt failed, on the same reasoning internal/worker's receipt is:
// the record of a failed review is exactly when the record is needed.
type ReviewRun struct {
	RunID        string
	AssignmentID string
	Role         string
	Subject      evaluation.Subject

	// Blinded reports that this assessment was taken without the target's
	// tallies, ranks or earlier evaluations.
	Blinded bool

	// Receipt is the review's run receipt, recording the one worker boundary
	// it embedded.
	Receipt *run.Receipt

	// Record is the evaluation record the assessment produced. It is the zero
	// value for a skip, a failure, and for a replayed attempt that recognized
	// its own earlier submission — Reused says which.
	Record evaluation.Record
	Reused bool
	// Corrects names the earlier statement this pass superseded, empty for a
	// first statement. The earlier record is preserved and linked, never
	// replaced.
	Corrects string

	// Assessment is what was submitted, nil for a skip or a failure.
	Assessment *evaluation.Assessment

	// Skipped and Failed are the reasons this assignment ended without an
	// assessment. They are recorded as completions rather than left to a
	// lease expiry, so the reservation is reconciled and the gap stays
	// visible as a gap.
	Skipped string
	Failed  string

	// Cost and Currency are the profile's own estimate for this review, as
	// the worker reported it. They are what the shared allowance is
	// reconciled against.
	Cost     float64
	Currency string
	// Launched reports that a worker process was supervised, which is what
	// makes a zero cost meaningful: a review refused before launch spent
	// nothing, and a review whose profile quoted no currency spent something
	// nobody measured.
	Launched bool
	// Unpriced reports a supervised boundary the profile quoted no currency
	// for. Cost then carries the reservation rather than an observation, so
	// the shared allowance is reconciled at no less than what was held.
	Unpriced bool

	// Disclosed is what the review's broker served, in service order, so the
	// record of what was shown is as auditable as the assessment itself.
	Disclosed []Retrieval

	// Failures mirrors what the receipt records.
	Failures []run.Failure

	// Cancelled reports that the review stopped because its context was
	// cancelled. Nothing it had already submitted is lost.
	Cancelled bool
}

// Review carries out one assignment: read the role's context, launch one
// supervised job, validate the submission against the role's authority, and
// record the assessment, the skip or the failure.
//
// It always ends the assignment. A refused blind, a worker that never spoke, a
// submission the role had no authority for and a cancelled context all reach
// the service as a recorded failure or skip, because a claimed assignment that
// is merely abandoned holds a reservation against a shared allowance until its
// lease expires — and an expired reservation cannot say whether the spend
// happened.
func (r *Reviewer) Review(ctx context.Context, opt ReviewOptions) (*ReviewRun, error) {
	a := opt.Assignment
	if a.ID == "" || a.RunID == "" {
		return nil, fmt.Errorf("explore: a review needs a claimed assignment and its run identity")
	}
	auth, ok := reviewAuthorities[a.Role]
	if !ok {
		return nil, fmt.Errorf("%w: %q is not a role this build reviews under", ErrReviewRole, a.Role)
	}
	contract, ok := ReviewOutputContract(a.Role)
	if !ok {
		return nil, fmt.Errorf("%w: %q has no result contract", ErrReviewRole, a.Role)
	}
	if err := opt.Preparation.Verify(); err != nil {
		return nil, err
	}
	if opt.Authority.Kind == "" {
		return nil, fmt.Errorf("explore: a review with no nameable authority may not run")
	}

	blinded := slices.Contains(evaluation.BlindedRoles(), a.Role)
	st := &reviewState{
		ctx: ctx,
		// Every durable write a review makes runs on a context detached from
		// the review's cancellation, for the reason every durable write in
		// this package does: once a judgement has crossed the worker
		// boundary, recording it is not work a budget may cut short.
		commit:  context.WithoutCancel(ctx),
		opt:     opt,
		started: r.now(),
		out: &ReviewRun{
			RunID:        a.RunID,
			AssignmentID: a.ID,
			Role:         a.Role,
			Subject:      a.Subject,
			Blinded:      blinded,
		},
	}

	// A replayed attempt recognizes its own assessment before it reads
	// anything else. The store's Submit is idempotent, but an assignment
	// already recorded here must not be re-exposed to a second worker: that
	// would spend the allowance again and hand a model a target it has
	// already judged.
	if commit, found, err := r.replayed(st, a); err != nil {
		return st.out, err
	} else if found {
		st.out.Record = evaluation.Record{ID: commit.ID}
		st.out.Reused = true
		st.out.Receipt = r.receipt(st, nil, nil)
		return st.out, nil
	}

	input, err := r.cfg.Service.Review(st.ctx, a)
	if err != nil {
		st.fail(FailureReviewInput, r.now(), fmt.Errorf("explore: review context for %s: %w", a.ID, err))
		// Nothing was exposed and nothing was launched, so the assignment is
		// finished as a failure rather than skipped: a read context Babel
		// could not obtain is a defect to look at, not a subject to pass over.
		r.finishFailed(st, st.err)
		return st.out, st.err
	}
	// A blinded role reads no prior evaluation, with exactly one exception:
	// an assignment claimed to correct a named statement is served that
	// statement, which this run wrote itself. Anything else offered to a
	// blinded role is a broken blind rather than a precedence question.
	st.correcting = opt.Corrects
	if st.correcting == "" {
		st.correcting = a.Corrects
	}
	if st.correcting == "" {
		st.correcting = priorByRun(input, a.RunID)
	}
	if blinded {
		if err := boundReveal(a, st.correcting, input.Previous); err != nil {
			st.fail(FailureReviewBlinding, r.now(), err)
			r.finishFailed(st, st.err)
			return st.out, st.err
		}
	}
	st.out.Corrects = st.correcting
	if st.correcting != "" {
		// A correction read the statement it revises, so the record must not
		// claim a blind it did not have - whatever the role's default is.
		st.out.Blinded = false
	}
	target, err := reviewTargetOf(input.Artifact, a.Role, blinded)
	if err != nil {
		st.fail(FailureReviewBlinding, r.now(), err)
		r.finishFailed(st, st.err)
		return st.out, st.err
	}
	alternatives := make([]reviewTarget, 0, len(input.Alternatives))
	for _, artifact := range input.Alternatives {
		alt, err := reviewTargetOf(artifact, a.Role, blinded)
		if err != nil {
			st.fail(FailureReviewBlinding, r.now(), err)
			r.finishFailed(st, st.err)
			return st.out, st.err
		}
		alternatives = append(alternatives, alt)
	}
	if !auth.alternatives && len(alternatives) > 0 {
		st.fail(FailureReviewRole, r.now(), fmt.Errorf("%w: the %s role was offered %d alternatives to compare",
			ErrReviewRole, a.Role, len(alternatives)))
		r.finishFailed(st, st.err)
		return st.out, st.err
	}

	broker := &retrieval{
		index:      r.cfg.Index,
		policy:     blindingPolicy(blinded, r.cfg.Policy),
		harnesses:  preparationHarnesses(opt.Preparation),
		sourceIDs:  preparationSourceIDs(opt.Preparation),
		redact:     r.cfg.Redact,
		thresholds: r.thresholds(),
		limit:      opt.Budget.Retrievals,
		research:   r.cfg.Research,
		fetches:    r.fetchBudget(opt.Budget),
		now:        r.now,
	}
	self := reviewSelfOf(a.RunID, input)

	receipt, runErr := r.launch(st, broker, contract, target, alternatives, input.Previous, blinded, self)
	steps, served := broker.trace()
	st.out.Disclosed = served
	st.steps = steps
	if receipt != nil {
		// A launched worker either measured what this review cost or it did
		// not, and the two must not arrive at the store as the same number.
		// The measurement is the engine's own session accounting: a profile
		// that quotes no currency priced nothing, and a run that never
		// reported its usage - the ordinary case for a worker that died or
		// submitted nothing - measured nothing. Submitting a plain zero
		// there would tell the shared allowance the attempt was free and
		// release the reservation in full, which is exactly the
		// unobserved-spend-as-zero accounting the coordination contract
		// refuses. So an unmeasured boundary is reported as unpriced and
		// charged at no less than what it reserved.
		st.out.Launched = true
		if receipt.Cost.Currency != "" && receipt.Usage != nil {
			st.out.Cost, st.out.Currency = receipt.Cost.EstimatedRun, receipt.Cost.Currency
		} else {
			st.out.Unpriced = true
			st.out.Cost = max(receipt.Cost.EstimatedRun, a.ReservedCost)
		}
		st.model = receipt.Metadata["model"]
	}
	if ctx.Err() != nil {
		st.out.Cancelled = true
		// A cancelled worker may have left the claim mid-flight. Recovery
		// runs on the detached context for the reason every durable write
		// here does, and it runs before the failure is reconciled so the
		// next cycle cannot meet a phantom active assignment still holding a
		// share of the allowance.
		if err := r.cfg.Service.Recover(st.commit); err != nil {
			st.fail(FailureReviewSubmit, r.now(),
				fmt.Errorf("explore: settle what the cancelled review left claimed: %w", err))
		}
	}

	if runErr != nil {
		code := FailureWorker
		if errors.Is(runErr, worker.ErrNoResult) && receipt != nil && receipt.ExitCode == 0 {
			code = FailureResultSchema
		}
		st.fail(code, r.now(), fmt.Errorf("explore: review job %s: %w", a.ID, runErr))
		r.finishFailed(st, st.err)
		st.out.Receipt = r.receipt(st, receipt, steps)
		return st.out, st.err
	}

	res, err := parseReviewResult(receipt.Result, a.Role, self)
	if err != nil {
		code := FailureResultSchema
		if errors.Is(err, ErrReviewRole) {
			code = FailureReviewRole
		}
		st.fail(code, r.now(), fmt.Errorf("explore: review result for %s: %w", a.ID, err))
		r.finishFailed(st, st.err)
		st.out.Receipt = r.receipt(st, receipt, steps)
		return st.out, st.err
	}
	if err := servedByRun(served).verify("review", a.ID, reviewEvidence(res)); err != nil {
		st.fail(FailureProvenance, r.now(), fmt.Errorf("explore: review result for %s: %w", a.ID, err))
		r.finishFailed(st, st.err)
		st.out.Receipt = r.receipt(st, receipt, steps)
		return st.out, st.err
	}

	r.record(st, res, input)
	st.out.Receipt = r.receipt(st, receipt, steps)
	return st.out, st.err
}

// boundReveal is the only prior material a blinded role may read: its own
// statement, on an assignment the service claimed in order to correct it.
//
// Both halves are checked because either alone is insufficient. A correction
// nobody reserved is a worker deciding to reveal its own history; a reveal of
// somebody else's statement is the tally the blind exists to withhold, whatever
// the assignment says it is for.
func boundReveal(a evaluation.Assignment, correcting string, previous []evaluation.Record) error {
	if a.Corrects == "" {
		if len(previous) > 0 {
			return fmt.Errorf("%w: %d prior evaluations were offered to a blinded %s review",
				ErrReviewBlinded, len(previous), a.Role)
		}
		if correcting != "" {
			return fmt.Errorf("%w: correcting %s needs the earlier statement, which no claim authorized reading",
				ErrReviewBlinded, correcting)
		}
		return nil
	}
	if correcting != a.Corrects {
		return fmt.Errorf("%w: this claim corrects %s, not %s",
			ErrReviewBlinded, a.Corrects, correcting)
	}
	for _, record := range previous {
		if record.ID != a.Corrects {
			return fmt.Errorf("%w: record %s is not the statement this claim corrects",
				ErrReviewBlinded, record.ID)
		}
		if runBaseID(record.Provenance.RunID) != runBaseID(a.RunID) {
			return fmt.Errorf("%w: record %s was authored by run %s",
				ErrReviewBlinded, record.ID, record.Provenance.RunID)
		}
	}
	return nil
}

// reviewState is one attempt's working set.
type reviewState struct {
	ctx     context.Context
	commit  context.Context
	opt     ReviewOptions
	out     *ReviewRun
	started time.Time
	steps   []run.RetrievalStep
	model   string
	// correcting is the earlier record this pass supersedes, empty for a
	// first statement.
	correcting string
	failures   []run.Failure
	err        error
}

// fail records one failure and remembers the first error, so a review can
// report that it degraded without losing the receipt.
func (s *reviewState) fail(code string, at time.Time, err error) {
	s.failures = append(s.failures, run.Failure{Stage: string(StageReview), Code: code, Message: err.Error(), At: at})
	s.out.Failures = s.failures
	if s.err == nil {
		s.err = err
	}
}

// replayed reports the assessment a prior attempt at this assignment already
// recorded, read from the resume ledger.
func (r *Reviewer) replayed(st *reviewState, a evaluation.Assignment) (Commit, bool, error) {
	committed, err := r.cfg.Ledger.Committed(st.commit, a.RunID, StageReview)
	if err != nil {
		st.fail(FailureStorage, r.now(), err)
		return Commit{}, false, err
	}
	commit, ok := committed[a.ID]
	return commit, ok, nil
}

// reviewRecordType is the entity type this package binds an evaluation record
// under in its own resume ledger.
//
// It is deliberately not one of internal/frontier's four kinds. The ledger's
// column is this package's own, an evaluation record lives in
// internal/evaluation rather than on the frontier, and recording it as a
// hypothesis or a proposal would make the ledger's entity index answer a
// question about the frontier with a record the frontier does not hold.
const reviewRecordType = frontier.EntityType("evaluation")

// record submits the assessment and binds it in the resume ledger.
//
// The order is the contract. The assessment is submitted first, because the
// evaluation store is where the judgement becomes durable and is the only
// thing that can refuse it; the ledger binding follows, so a crash between the
// two leaves a submitted assessment the store's own idempotence recognizes,
// rather than a binding pointing at a record that does not exist.
func (r *Reviewer) record(st *reviewState, res *ReviewResult, input evaluation.ReviewInput) {
	a := st.opt.Assignment
	at := r.now()
	submission := evaluation.Submission{
		AssignmentID: a.ID,
		RunID:        a.RunID,
		Fence:        a.Fence,
		Cost:         st.out.Cost,
		Unpriced:     st.out.Unpriced,
		Provenance:   r.provenance(st, input),
	}
	switch {
	case res.Skip != "":
		submission.SkipReason = res.Skip
		st.out.Skipped = res.Skip
	case len(res.Results) > 0 && input.Artifact.CriteriaID == "":
		// Criterion results are answers to an operator's adopted criteria,
		// and there are none here. Recording them against the record's own
		// suggestions would be the evaluator choosing the target it is
		// judged by, so the assignment ends as a visible gap instead.
		submission.SkipReason = "no operator-adopted criteria version to judge these results against"
		st.out.Skipped = submission.SkipReason
	default:
		assessment := &evaluation.Assessment{
			Vote:           string(res.Vote),
			Contributions:  res.Contributions,
			Outcome:        string(res.Outcome),
			Results:        res.Results,
			Environment:    res.Environment,
			AsOf:           res.AsOf,
			Uncertainty:    res.Uncertainty,
			ContextVersion: a.ContextVersion,
		}
		if len(res.Results) > 0 || res.Outcome != "" {
			// The criteria version is what links a criterion result to the
			// operator decision it was judged against. It comes from the
			// artifact rather than from the worker: a review that could name
			// its own criteria version could verify itself against a target
			// it replaced.
			assessment.CriteriaID = input.Artifact.CriteriaID
		}
		submission.Assessment = assessment
		st.out.Assessment = assessment
	}

	// A correction and a first statement are two different writes. The
	// correction preserves the earlier statement and links this one to it;
	// the store settles this assignment's reservation either way, so the
	// accounting does not branch even though the record does.
	var (
		record evaluation.Record
		err    error
	)
	if st.correcting != "" {
		record, err = r.cfg.Service.Correct(st.commit, st.correcting, submission)
	} else {
		record, err = r.cfg.Service.Submit(st.commit, submission)
	}
	if err != nil {
		st.fail(FailureReviewSubmit, r.now(), fmt.Errorf("explore: record the assessment for %s: %w", a.ID, err))
		return
	}
	st.out.Record = record
	if record.ID == "" {
		// A skip and a failure are completions rather than records, so there
		// is nothing to bind: the assignment is finished, the reservation is
		// reconciled, and the attempt journal is the store's.
		return
	}
	if err := r.cfg.Ledger.Record(st.commit, a.RunID, StageReview,
		Commit{Ref: a.ID, Type: reviewRecordType, ID: record.ID, At: at}); err != nil {
		st.fail(FailureStorage, r.now(), fmt.Errorf("explore: bind the assessment for %s: %w", a.ID, err))
	}
}

// finishFailed reconciles a claimed assignment that produced no assessment.
//
// It is the other half of every early return above, and it is not optional. A
// claim holds a reservation against one shared allowance; an assignment left
// to its lease expiry is spend nobody can account for, because an expired
// reservation cannot say whether the worker reached a provider before it died.
// So the failure is delivered, with the cost the boundary actually reported.
func (r *Reviewer) finishFailed(st *reviewState, cause error) {
	a := st.opt.Assignment
	reason := "the review did not complete"
	if cause != nil {
		reason = cause.Error()
	}
	st.out.Failed = reason
	if _, err := r.cfg.Service.Submit(st.commit, evaluation.Submission{
		AssignmentID: a.ID,
		RunID:        a.RunID,
		Fence:        a.Fence,
		FailedReason: reason,
		Cost:         st.out.Cost,
		Unpriced:     st.out.Unpriced,
		Provenance:   r.provenance(st, evaluation.ReviewInput{}),
	}); err != nil {
		st.fail(FailureReviewSubmit, r.now(),
			fmt.Errorf("explore: reconcile the reservation for %s: %w", a.ID, err))
	}
}

// provenance is what the record says about how this judgement was produced.
func (r *Reviewer) provenance(st *reviewState, input evaluation.ReviewInput) evaluation.Provenance {
	consulted := make([]evaluation.Subject, 0, len(input.Alternatives)+len(input.Previous))
	for _, alt := range input.Alternatives {
		consulted = append(consulted, alt.Subject)
	}
	for _, prior := range input.Previous {
		consulted = append(consulted, prior.Subject)
	}
	return evaluation.Provenance{
		RunID:          st.opt.Assignment.RunID,
		Model:          st.model,
		Profile:        r.cfg.Profile.String(),
		Recipe:         r.recipe.ID,
		RecipeVersion:  r.recipe.Version,
		Blinded:        st.out.Blinded,
		ContextVersion: st.opt.Assignment.ContextVersion,
		Consulted:      consulted,
	}
}

// launch builds and supervises the review's one job.
func (r *Reviewer) launch(st *reviewState, broker *retrieval, contract worker.OutputContract,
	target reviewTarget, alternatives []reviewTarget, previous []evaluation.Record,
	blinded bool, self reviewSelf) (*worker.Receipt, error) {
	a := st.opt.Assignment
	cfg := r.cfg.Worker
	cfg.Authorizer = broker
	if st.opt.OnProgress != nil {
		cfg.OnProgress = st.opt.OnProgress
	}
	if r.cfg.Transcript != nil {
		log, err := r.cfg.Transcript(a.RunID, string(StageReview))
		if err != nil {
			return nil, fmt.Errorf("explore: review transcript: %w", err)
		}
		defer log.Close()
		cfg.Transcript = log
	}
	client, err := worker.New(cfg)
	if err != nil {
		return nil, fmt.Errorf("explore: review worker: %w", err)
	}

	tools := reviewTools(r.cfg.Grant, blinded)
	params := reviewParams(a, blinded)
	sources := preparationSources(st.opt.Preparation)
	prompt, err := composeReviewPrompt(contract, r.recipe, target, alternatives, previous, sources, params, tools, blinded)
	if err != nil {
		return nil, err
	}
	job := worker.Job{
		JobID:   a.RunID + "/review",
		RunID:   a.RunID,
		Profile: r.cfg.Profile,
		Recipes: []worker.RecipeRef{{ID: r.recipe.ID, Version: r.recipe.Version}},
		Grant:   r.cfg.Grant,
		Sources: sources,
		Params:  params,
		Tools:   tools,
		Output:  contract,
		Prompt:  prompt,
		// Accept is what can be decided at submission time, while the model
		// can still correct it: the shape, the role's authority, the closed
		// vocabularies, the support an observed outcome needs, and every
		// citation against what this review has been served so far. Nothing
		// here is durable, so a refusal costs the model a turn rather than
		// costing the assignment its record.
		Accept: func(payload json.RawMessage) error {
			res, err := parseReviewResult(&worker.ResultRecord{Schema: contract.Schema, Payload: payload}, a.Role, self)
			if err != nil {
				return err
			}
			_, servedSoFar := broker.trace()
			return servedByRun(servedSoFar).verify("review", a.ID, reviewEvidence(res))
		},
	}
	return client.Run(st.ctx, job)
}

// receipt records the review's run receipt, amending a prior attempt's rather
// than replacing it.
func (r *Reviewer) receipt(st *reviewState, workerReceipt *worker.Receipt, steps []run.RetrievalStep) *run.Receipt {
	a := st.opt.Assignment
	body := run.Body{
		Cookbook:     slices.Clone(r.assets),
		Capabilities: r.cfg.Capabilities,
		Job: run.JobVersions{Job: ReviewJobVersion, Prompt: ReviewPromptVersion,
			Schema: ReviewResultSchema},
		Policy: run.PolicyVersions{Redaction: RedactionPolicyVersion,
			Disclosure: DisclosurePolicyVersion},
		Worker:    workerReceipt,
		Retrieval: steps,
		Failures:  slices.Clone(st.failures),
		Timing:    run.Timing{StartedAt: st.started, FinishedAt: r.now()},
	}
	lifecycle := run.Closed
	if st.out.Cancelled {
		lifecycle = run.Interrupted
	}
	body.Checkpoint = &run.Checkpoint{
		State:   lifecycle,
		Stage:   string(StageReview),
		Verdict: &run.Verdict{Cancelled: st.out.Cancelled},
	}
	if st.err != nil {
		body.Checkpoint.Verdict.Failure = st.err.Error()
	}
	if st.out.Cancelled {
		body.Checkpoint.Reason = "operator stop or context cancellation"
	}
	if st.out.Record.ID != "" {
		body.Checkpoint.Records = []string{st.out.Record.ID}
	}
	if workerReceipt != nil {
		calls := len(workerReceipt.ToolRequests)
		body.Resources = run.Resources{ToolCalls: &calls}
	}

	prior, err := r.cfg.Runs.Revisions(st.commit, a.RunID)
	if err != nil && !errors.Is(err, run.ErrNotFound) {
		st.fail(FailureStorage, r.now(), fmt.Errorf("explore: read the receipt chain for %s: %w", a.RunID, err))
		return nil
	}
	var receipt run.Receipt
	if len(prior) == 0 {
		receipt, err = run.NewReceipt(run.NewReceiptID(), a.RunID, st.opt.Preparation,
			st.opt.Authority, body, r.now())
	} else {
		body.AmendmentReason = "record the next durable review attempt checkpoint"
		receipt, err = run.Amend(prior[len(prior)-1], run.NewReceiptID(), body, r.now())
	}
	if err != nil {
		st.fail(FailureStorage, r.now(), fmt.Errorf("explore: build the receipt for %s: %w", a.RunID, err))
		return nil
	}
	if err := r.cfg.Runs.PutReceipt(st.commit, receipt); err != nil {
		st.fail(FailureStorage, r.now(), fmt.Errorf("explore: store the receipt for %s: %w", a.RunID, err))
		return nil
	}
	return &receipt
}

func (r *Reviewer) thresholds() preflight.Thresholds {
	if r.cfg.Thresholds != nil {
		return *r.cfg.Thresholds
	}
	return preflight.DefaultThresholds()
}

func (r *Reviewer) fetchBudget(b Budget) int {
	if b.Fetches > 0 {
		return b.Fetches
	}
	return DefaultFetches
}

// reviewTarget is the projection of one artifact a review is shown.
//
// It is a projection rather than the artifact itself, and that is the whole of
// the procedural blind. A struct with no reception field cannot leak a tally
// however the artifact grows, so the blind survives a field added upstream —
// which a prompt template scanning for forbidden words would not.
type reviewTarget struct {
	Kind       string                 `json:"kind"`
	ID         string                 `json:"id"`
	RootID     string                 `json:"root_id,omitempty"`
	CreatedAt  time.Time              `json:"created_at,omitzero"`
	Title      string                 `json:"title,omitempty"`
	Body       json.RawMessage        `json:"body,omitempty"`
	Evidence   []frontier.Evidence    `json:"evidence,omitempty"`
	Criteria   []evaluation.Criterion `json:"criteria,omitempty"`
	CriteriaID string                 `json:"criteria_id,omitempty"`
	Related    []evaluation.Subject   `json:"related,omitempty"`

	// Status and ReviewStatus are the operator's own lifecycle state. They
	// are withheld from a reception vote and shown to every other role,
	// because a recorded disposition is the loudest prior judgement there is
	// and reception is the one question that has to be answered
	// independently of it — while an evidence check, an outcome
	// verification and a relevance review are all *about* the lifecycle
	// state and cannot be asked without it.
	Status       string `json:"status,omitempty"`
	ReviewStatus string `json:"review_status,omitempty"`

	// Context is the recorded priority, current work, pain and permitted work
	// behind the target. The service clears it for every blinded role except
	// relevance, whose question it is; this projection carries whatever
	// survived that.
	Context *evaluation.Context `json:"context,omitempty"`
}

// reviewBlindedKeys are the payload keys a blinded assessment may not be
// served. They are the tallies, ranks and prior-judgement fields E3 withholds:
// a reception score, a cohort rank, a novelty or priority grading a producer
// attached to its own claim, and any embedded assessment.
//
// The audit is over the producer-authored body, which is opaque JSON this
// package does not own. Everything else in the projection is typed, so the
// struct is the guarantee; the body is where an upstream projection change or
// another instance's payload could carry a judgement in, and a check over the
// bytes actually about to be sent is the only one that can see it.
var reviewBlindedKeys = []string{
	"reception", "tally", "votes", "vote", "rank", "cohort",
	"novelty", "priority", "assessment", "assessments", "evaluations",
	"support_count", "oppose_count", "unsure_count",
}

// reviewTargetOf projects one artifact for one role, and refuses a blinded
// projection whose body carries withheld material.
func reviewTargetOf(a evaluation.Artifact, role string, blinded bool) (reviewTarget, error) {
	if blinded {
		if leaked, err := blindedBodyLeak(a.Body); err != nil {
			return reviewTarget{}, fmt.Errorf("%w: %s body: %w", ErrReviewBlinded, a.Subject.Kind, err)
		} else if leaked != "" {
			return reviewTarget{}, fmt.Errorf("%w: the %s body carries %q, which a blinded %s review may not read",
				ErrReviewBlinded, a.Subject.Kind, leaked, role)
		}
	}
	target := reviewTarget{
		Kind:         a.Subject.Kind,
		ID:           a.Subject.ID,
		RootID:       a.RootID,
		CreatedAt:    a.CreatedAt,
		Title:        a.Title,
		Body:         a.Body,
		Evidence:     a.Evidence,
		Criteria:     a.Criteria,
		CriteriaID:   a.CriteriaID,
		Related:      a.Related,
		Status:       a.Status,
		ReviewStatus: a.ReviewStatus,
	}
	if role == evaluation.RoleReception {
		target.Status, target.ReviewStatus = "", ""
	}
	if a.Context.Version != "" {
		context := a.Context
		target.Context = &context
	}
	return target, nil
}

// blindedBodyLeak reports the first withheld key a payload carries, at any
// depth, and an error when the payload is not decodable JSON.
//
// It walks the decoded document rather than matching the raw bytes, so a
// withheld key is found wherever it is nested and a claim's prose that merely
// contains the word "priority" is not mistaken for a grading.
func blindedBodyLeak(body json.RawMessage) (string, error) {
	if len(body) == 0 {
		return "", nil
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	return blindedLeakIn(decoded), nil
}

func blindedLeakIn(value any) string {
	switch v := value.(type) {
	case map[string]any:
		keys := mapKeys(v)
		slices.Sort(keys)
		for _, key := range keys {
			if slices.Contains(reviewBlindedKeys, strings.ToLower(key)) {
				return key
			}
			if leaked := blindedLeakIn(v[key]); leaked != "" {
				return leaked
			}
		}
	case []any:
		for _, item := range v {
			if leaked := blindedLeakIn(item); leaked != "" {
				return leaked
			}
		}
	}
	return ""
}

// mapKeys is maps.Keys as a slice, so the walk above is deterministic and the
// key a refusal names does not change between two identical payloads.
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// reviewParams are the job parameters one review carries.
//
// The blinded set is smaller by exactly one entry, and the omission is the
// point: the allocation lane names why the draw happened, and a lane reserved
// for never-reviewed work tells a blinded reviewer that nobody has judged the
// target yet. That is a fact about the target's prior evaluations, so it is
// withheld with them rather than being treated as harmless scheduling detail.
func reviewParams(a evaluation.Assignment, blinded bool) map[string]string {
	params := map[string]string{
		ParamStage:                string(StageReview),
		ParamReviewRole:           a.Role,
		ParamReviewSubjectKind:    a.Subject.Kind,
		ParamReviewSubjectID:      a.Subject.ID,
		ParamReviewAssignment:     a.ID,
		ParamReviewPolicyVersion:  a.PolicyVersion,
		ParamReviewContextVersion: a.ContextVersion,
		ParamReviewBlinded:        fmt.Sprintf("%t", blinded),
	}
	if !blinded {
		params[ParamReviewLane] = a.Lane
	}
	return params
}

// reviewTools are the tools a review job registers.
//
// A blinded job is offered a corpus search whose argument schema cannot name
// the frontier surface. That is the difference between blinding the prompt and
// blinding the run: the tool list and its schemas are as much of what Babel
// serves as the prompt is, and a reviewer offered a `scope` argument would
// have been told the surface exists and invited to ask for it. The policy in
// front of the broker denies it too, so a worker that guesses the wire format
// is refused rather than served.
func reviewTools(g worker.Grant, blinded bool) []worker.HostTool {
	tools := jobTools(g)
	if !blinded {
		return tools
	}
	out := make([]worker.HostTool, 0, len(tools))
	for _, tool := range tools {
		if tool.Name == worker.ToolSearch {
			schema, err := blindSearchSchema()
			if err != nil {
				// The schema is generated from a type in this package, so a
				// failure here is a build defect rather than a runtime
				// condition. Dropping the tool is the conservative answer:
				// a review with no search is a review that reports the gap,
				// where one with an unpruned schema would be a blind that
				// does not hold.
				continue
			}
			tool.Parameters = schema
			tool.Description = tool.Description +
				" A blinded initial assessment searches the corpus only; Babel's own prior output is not available to it."
		}
		out = append(out, tool)
	}
	return out
}

// blindSearchSchema is the corpus-search argument schema with the surface
// selector and the frontier-only filter removed, so the schema cannot express
// a search of Babel's own output.
func blindSearchSchema() (json.RawMessage, error) {
	t := reflect.TypeFor[SearchRequest]()
	g := &schemaGenerator{defs: map[string]*object{}}
	if _, err := g.describe(t); err != nil {
		return nil, err
	}
	root := g.defs[t.Name()]
	delete(g.defs, t.Name())
	doc := &object{}
	for _, key := range root.keys {
		doc.set(key, root.get(key))
	}
	doc.remove("scope")
	doc.remove("statuses")
	defs := &object{}
	for _, name := range g.reachable(root) {
		defs.set(name, g.defs[name])
	}
	if len(defs.keys) > 0 {
		doc.set("$defs", defs)
	}
	return json.Marshal(doc)
}

// blindingPolicy composes the blind in front of an operator's own narrowing.
//
// Order matters and this is the safe one: both must allow, so an operator
// policy cannot widen the blind and the blind cannot widen the operator's
// policy. A nil operator policy is no narrowing beyond the blind, which is
// what internal/explore's retrieval broker already documents for its own
// policy field.
func blindingPolicy(blinded bool, operator worker.Authorizer) worker.Authorizer {
	if !blinded {
		return operator
	}
	return worker.AuthorizerFunc(func(ctx context.Context, req worker.ToolRequest) worker.Decision {
		if req.Capability == worker.CapabilityCorpusSearch {
			var args SearchRequest
			if len(req.Arguments) > 0 {
				if err := json.Unmarshal(req.Arguments, &args); err != nil {
					return worker.Decision{Reason: "the search arguments are not a search request"}
				}
			}
			if args.Scope != "" && args.Scope != ScopeCorpus {
				return worker.Decision{Reason: "this initial assessment is taken blind: Babel's own prior " +
					"output, including earlier evaluations of this record, is not served to it. " +
					"Search the corpus, or submit what you can judge from the record itself."}
			}
			if len(args.Statuses) > 0 {
				return worker.Decision{Reason: "lifecycle status is a filter of Babel's own output, which " +
					"this blinded assessment is not served. Search the corpus instead."}
			}
		}
		if operator == nil {
			return worker.Decision{Allow: true}
		}
		return operator.Authorize(ctx, req)
	})
}

// preparationSources renders a preparation's sessions as the approved inputs
// the job records, on the same terms an exploration's job does.
func preparationSources(prep run.Preparation) []worker.Source {
	sources := make([]worker.Source, 0, len(prep.Selection))
	for _, sel := range prep.Selection {
		sources = append(sources, worker.Source{
			Kind:     "session",
			Selector: sel.Harness + "/" + sel.SourceID,
			Digest:   string(sel.SourceDigest),
			Snapshot: sel.Snapshot,
		})
	}
	return sources
}

func preparationHarnesses(prep run.Preparation) []string {
	var out []string
	for _, sel := range prep.Selection {
		if !slices.Contains(out, sel.Harness) {
			out = append(out, sel.Harness)
		}
	}
	sort.Strings(out)
	return out
}

func preparationSourceIDs(prep run.Preparation) []string {
	var out []string
	for _, sel := range prep.Selection {
		if !slices.Contains(out, sel.SourceID) {
			out = append(out, sel.SourceID)
		}
	}
	sort.Strings(out)
	return out
}
