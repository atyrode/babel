package explore

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// authority is what a stage's structured result may contain. §5.4 gives the
// challenger no path to a finding and no business developing its own
// observations, and gives the synthesizer no business gathering new evidence,
// so the table is the enforcement rather than a comment about it.
type authority struct {
	// observations permits candidates to arrive with developed claims.
	observations bool
	// consolidate permits findings, proposals and the promotion transitions
	// they justify.
	consolidate bool
	// remedies permits a candidate to arrive with the suggested change it
	// carries (#114).
	//
	// It is its own bit rather than a reading of consolidate, because a
	// remedy is not a consolidation: it rests on the claim beside it and on
	// no finding, so a stage that may emit one has not thereby been granted
	// the development path. The challenger is the stage this separates. §5.4
	// grants it evidence-backed counter-observations, contradicting
	// candidates, and objections — criticism, in other words — and a critic
	// that also prescribed the fix would be answering its own objection
	// inside the stage that raised it.
	remedies bool
	// objections permits §5.4 criticism.
	objections bool
	// schedule permits the job to defer or reject its own candidates.
	schedule bool
	// Questions deliberately have no bit here, and the omission is the
	// statement: §5.4 divides what a stage may *assert*, and a question
	// asserts nothing. A challenger that cannot tell whether a claim holds
	// and a synthesizer that cannot tell whether two findings are about the
	// same service have met the same gap the explorer meets, and the stage
	// they happened to be in has no bearing on who can answer it.
}

var authorities = map[Stage]authority{
	StageExplore:    {observations: true, consolidate: true, remedies: true, schedule: true},
	StageChallenge:  {objections: true},
	StageSynthesize: {consolidate: true, remedies: true},
}

// cookbookStage maps a run stage to the recipe stage that declares
// participation in it. Discovery and development are one recipe stage —
// §5.4's techniques follow emergence within the same investigation — while the
// challenge and the synthesis are separate jobs.
var cookbookStage = map[Stage]cookbook.Stage{
	StageExplore:    cookbook.StageInvestigate,
	StageChallenge:  cookbook.StageChallenge,
	StageSynthesize: cookbook.StageSynthesize,
}

// stageRun is what one supervised job produced.
type stageRun struct {
	receipt *worker.Receipt
	result  *Result
	steps   []run.RetrievalStep
	started time.Time
}

// errConsolidationDeferred reports a consolidation whose observations this
// pass's budget did not develop. It is not a failure: §5.2 makes a budget a
// choice about what is explored now, so the consolidation waits for the
// resumed run rather than being reported as a broken result.
var errConsolidationDeferred = errors.New("explore: consolidation deferred with its observations")

// runStage launches one job, supervises it, and decodes its structured result.
//
// It returns a stageRun whenever a process was launched, even on failure: the
// worker receipt is the audit record of the boundary and is needed most when
// the boundary failed. It returns nil only when nothing could be launched.
func (c *Controller) runStage(st *state, stage Stage, runID string, params map[string]string) *stageRun {
	if !c.safePoint(st, stage) {
		return nil
	}
	at := c.now()
	recipes := c.stageRecipes(stage)
	if len(recipes) == 0 {
		st.fail(stage, FailureNoRecipe, at,
			fmt.Errorf("explore: no selected recipe declares the %s stage", stage))
		return nil
	}

	broker := &retrieval{
		index:      c.cfg.Index,
		policy:     c.cfg.Policy,
		harnesses:  c.harnesses(),
		sourceIDs:  c.sourceIDs(),
		redact:     c.cfg.Redact,
		thresholds: c.thresholds(),
		limit:      st.opt.Budget.Retrievals,
		research:   c.cfg.Research,
		fetches:    c.fetchBudget(st),
		now:        c.now,
	}
	cfg := c.cfg.Worker
	cfg.Authorizer = broker
	if st.opt.OnProgress != nil {
		cfg.OnProgress = func(p worker.ProgressRecord) { st.opt.OnProgress(stage, p) }
	}
	client, err := worker.New(cfg)
	if err != nil {
		st.fail(stage, FailureWorker, c.now(), fmt.Errorf("explore: %s worker: %w", stage, err))
		return nil
	}

	receipt, runErr := client.Run(st.ctx, c.job(st, stage, runID, recipes, params, broker))
	steps, served := broker.trace()
	st.out.Retrieval = append(st.out.Retrieval, served...)
	sr := &stageRun{receipt: receipt, steps: steps, started: at}
	if runErr != nil {
		code := FailureWorker
		if errors.Is(runErr, worker.ErrNoResult) && receipt != nil && receipt.ExitCode == 0 {
			// The engine's turn ended, it left cleanly, and nothing was
			// accepted. Every refused submission is already in the receipt
			// with its reason; the stage's verdict is that it produced
			// nothing. An engine that died mid-turn is the other case,
			// and its exit status says so.
			code = FailureResultSchema
		}
		st.fail(stage, code, c.now(), fmt.Errorf("explore: %s job: %w", stage, runErr))
		return sr
	}
	// The accepted submission already parsed once, when Accept admitted it;
	// parsing it again here reads the same bytes into the value persist
	// consumes, so the receipt's payload and the persisted result cannot
	// diverge.
	result, err := parseResult(receipt.Result)
	if err != nil {
		st.fail(stage, FailureResultSchema, c.now(), fmt.Errorf("explore: %s result: %w", stage, err))
		return sr
	}
	sr.result = result
	return sr
}

// runSeparateJob runs one of §5.4's logically separate passes: its own worker
// invocation, its own run identity, its own receipt. A failure here is
// recorded against that identity and leaves the exploration's records
// untouched, which is §6.5's rule that a failed independent exploration does
// not erase successful work.
func (c *Controller) runSeparateJob(st *state, stage Stage) *run.Receipt {
	runID := stageRunID(st.opt.RunID, stage)
	sr := c.runStage(st, stage, runID, c.brief(st, stage))
	if sr == nil {
		return nil
	}
	if sr.result != nil {
		c.persist(st, stage, runID, sr.result)
	}
	receipt := c.writeReceipt(st, runID, sr.receipt, sr.steps, st.failuresFor(stage), sr.started)
	if receipt != nil {
		st.written[stage] = true
	}
	return receipt
}

// stageRecipes are the selected recipes that declare this stage.
func (c *Controller) stageRecipes(stage Stage) []*cookbook.Recipe {
	want, ok := cookbookStage[stage]
	if !ok {
		return nil
	}
	var out []*cookbook.Recipe
	for _, recipe := range c.cfg.Recipes.All() {
		if slices.Contains(recipe.Stages, want) {
			out = append(out, recipe)
		}
	}
	return out
}

// job builds one stage's job: the tools its grant registers, the prompt, the
// result contract, and the semantic check every submission goes through. The
// broker is the run's evidence trace, which Accept reads at submission time.
func (c *Controller) job(st *state, stage Stage, runID string, recipes []*cookbook.Recipe, params map[string]string, broker *retrieval) worker.Job {
	merged := make(map[string]string, len(st.opt.Params)+len(params)+1)
	maps.Copy(merged, st.opt.Params)
	maps.Copy(merged, params)
	// The parameters this package owns are written last: a caller must not be
	// able to tell the model it is running a stage other than the one Babel
	// is about to hold it to.
	merged[ParamStage] = string(stage)

	refs := make([]worker.RecipeRef, 0, len(recipes))
	for _, recipe := range recipes {
		refs = append(refs, worker.RecipeRef{ID: recipe.ID, Version: recipe.Version})
	}
	sources := make([]worker.Source, 0, len(c.cfg.Preparation.Selection))
	for _, sel := range c.cfg.Preparation.Selection {
		sources = append(sources, worker.Source{
			Kind:     "session",
			Selector: sel.Harness + "/" + sel.SourceID,
			Digest:   string(sel.SourceDigest),
			Snapshot: sel.Snapshot,
		})
	}
	contract := OutputContract(stage)
	tools := jobTools(c.cfg.Grant)
	return worker.Job{
		JobID:   runID + "/job",
		RunID:   runID,
		Profile: c.cfg.Profile,
		Recipes: refs,
		Grant:   c.cfg.Grant,
		Sources: sources,
		Params:  merged,
		Tools:   tools,
		Output:  contract,
		// Every stage carries the refine-first context, not only discovery.
		// A challenger arguing against a candidate the frontier already
		// rejected, and a synthesizer consolidating a finding that restates
		// an existing one, are the same duplication one step further down
		// the development path (#87).
		Prompt: composePrompt(stage, contract, recipes, sources, merged, c.relatedContext(st), tools),
		// Accept is what persistence can decide at submission time without
		// a durable record: the shape, the references within the result,
		// the recipe provenance, and every citation against what this run
		// has served so far. The model reads a refusal while it can still
		// correct it. What a stage may emit, and what a brief's identifier
		// resolves to, are decided in persist over the records the run has
		// written — a stray observation there is dropped with a warning,
		// never at the cost of the candidate beside it (#179).
		Accept: func(payload json.RawMessage) error {
			res, err := parseResult(&worker.ResultRecord{Schema: contract.Schema, Payload: payload})
			if err != nil {
				return err
			}
			if err := c.checkRecipes(stage, res); err != nil {
				return err
			}
			_, servedSoFar := broker.trace()
			return verifyCitations(servedByRun(append(slices.Clone(st.out.Retrieval), servedSoFar...)), res)
		},
	}
}

// checkRecipes refuses a result whose claims cite a recipe this stage did not
// select, so the model corrects the provenance rather than losing the claim
// at persistence. Persist applies the same predicate per item.
func (c *Controller) checkRecipes(stage Stage, res *Result) error {
	for _, cand := range res.Candidates {
		for _, obs := range cand.Observations {
			if !c.recipeAllowed(stage, obs.Recipe) {
				return fmt.Errorf("%w: observation %q cites %s@%d", ErrUnknownRecipe, obs.Ref, obs.Recipe.ID, obs.Recipe.Version)
			}
		}
	}
	for _, obj := range res.Objections {
		if !c.recipeAllowed(stage, obj.Recipe) {
			return fmt.Errorf("%w: objection %q cites %s@%d", ErrUnknownRecipe, obj.Ref, obj.Recipe.ID, obj.Recipe.Version)
		}
	}
	return nil
}

// brief tells a separate pass which durable records it is examining, and makes
// those identifiers resolvable when its result names them back.
func (c *Controller) brief(st *state, stage Stage) map[string]string {
	params := map[string]string{
		ParamBriefHypotheses:   strings.Join(st.out.Hypotheses, ","),
		ParamBriefObservations: strings.Join(st.out.Observations, ","),
	}
	if stage == StageSynthesize {
		params[ParamBriefObjections] = strings.Join(st.out.Objections, ",")
	}
	for _, id := range st.out.Hypotheses {
		st.hypotheses[id] = id
	}
	for _, id := range st.out.Observations {
		st.observations[id] = id
	}
	return params
}

// harnesses and sourceIDs are the preparation's scope, which bounds every
// retrieval the run serves (§2.6 fixes a run's sessions before work starts).
func (c *Controller) harnesses() []string {
	var out []string
	for _, sel := range c.cfg.Preparation.Selection {
		if !slices.Contains(out, sel.Harness) {
			out = append(out, sel.Harness)
		}
	}
	return out
}

func (c *Controller) sourceIDs() []string {
	var out []string
	for _, sel := range c.cfg.Preparation.Selection {
		if !slices.Contains(out, sel.SourceID) {
			out = append(out, sel.SourceID)
		}
	}
	return out
}

// recipeAllowed reports whether a claim's recipe provenance names an asset
// this stage actually selected.
func (c *Controller) recipeAllowed(stage Stage, ref worker.RecipeRef) bool {
	for _, recipe := range c.stageRecipes(stage) {
		if recipe.ID == ref.ID && recipe.Version == ref.Version {
			return true
		}
	}
	return false
}

// persist writes one stage's structured result to the durable frontier.
//
// The order is the contract. Every candidate is persisted first, before any
// development, sorting or consolidation, because §5.2 requires it and because
// the run may be cancelled at any point after this loop — which is exactly why
// this loop does not check for cancellation. Development, criticism and
// consolidation follow, each item validated on its own, so one refused item is
// a recorded failure rather than a discarded result.
func (c *Controller) persist(st *state, stage Stage, runID string, res *Result) {
	auth := authorities[stage]
	committed, err := c.cfg.Ledger.Committed(st.commit, st.opt.RunID, stage)
	if err != nil {
		st.fail(stage, FailureStorage, c.now(), err)
		return
	}
	// The evidence index is rebuilt per stage from the run's whole trace,
	// because each stage's job appends its own retrievals to it.
	st.served = servedByRun(st.out.Retrieval)

	for _, cand := range res.Candidates {
		id, reused, err := c.putHypothesis(st, stage, runID, committed, cand)
		if err != nil {
			st.fail(stage, FailureStorage, c.now(),
				fmt.Errorf("explore: persist candidate %q: %w", cand.Ref, err))
			continue
		}
		st.hypotheses[cand.Ref] = id
		st.note(id)
		st.out.Hypotheses = append(st.out.Hypotheses, id)
		c.record(st, RecordEvent{Stage: stage, Type: frontier.EntityHypothesis, Ref: cand.Ref, ID: id, Reused: reused})
		c.putDispositions(st, stage, runID, frontier.Ref{Type: frontier.EntityHypothesis, ID: id}, cand.Dispositions)
		c.remedy(st, stage, runID, committed, auth, cand, id)
	}

	c.develop(st, stage, runID, committed, auth, res)
	// Cancellation stops here rather than earlier: every candidate is durable
	// above, and criticism, consolidation and scheduling are exploration the
	// resumed run finishes. Recording them as refusals would blame the worker
	// for a decision Babel made.
	if st.ctx.Err() != nil {
		return
	}
	c.object(st, stage, runID, committed, auth, res)
	c.consolidate(st, stage, runID, committed, auth, res)
	c.schedule(st, stage, auth, res)
	c.ask(st, stage, res)
}

// remedy writes the value-claim half of one candidate, splitting the truth from
// the suggestion (#114).
//
// It runs inside the candidate loop rather than as a pass of its own, and that
// is deliberate: the two records are one emission, so the remedy is durable in
// the same iteration that made its claim durable, and a run cancelled between
// candidates leaves pairs rather than orphaned halves. The loop above already
// guarantees the ordering the split needs — the remedy is written only after
// the hypothesis it addresses exists.
//
// A refused remedy is a recorded failure for that one candidate and never for
// the claim beside it. That is the split's whole promise carried into the error
// path: an unwritable suggestion must not take a persisted claim down with it,
// because §5.2 requires every emitted candidate to be persisted and the claim
// already is.
func (c *Controller) remedy(st *state, stage Stage, runID string, committed map[string]Commit, auth authority, cand Candidate, hypothesisID string) {
	if cand.Remedy == nil {
		return
	}
	if !auth.remedies {
		st.fail(stage, FailureAuthority, c.now(), fmt.Errorf(
			"%w: the %s stage cannot suggest changes, and candidate %q arrived with a remedy",
			ErrStageAuthority, stage, cand.Ref))
		return
	}
	if _, done := committed[cand.Remedy.Ref]; !done {
		if err := st.served.verify("remedy", cand.Remedy.Ref,
			cand.Remedy.Proposal.Supporting, cand.Remedy.Proposal.Conflicting); err != nil {
			st.fail(stage, FailureProvenance, c.now(), err)
			return
		}
	}
	id, reused, err := c.putRemedy(st, stage, runID, committed, *cand.Remedy, hypothesisID)
	if err != nil {
		st.fail(stage, FailureStorage, c.now(),
			fmt.Errorf("explore: persist remedy %q for candidate %q: %w", cand.Remedy.Ref, cand.Ref, err))
		return
	}
	st.out.Proposals = append(st.out.Proposals, id)
	c.record(st, RecordEvent{Stage: stage, Type: frontier.EntityProposal, Ref: cand.Remedy.Ref, ID: id, Reused: reused})
}

// develop writes the observations a candidate arrived with, bounded by the
// pass's budget. §5.2 confines a budget to choosing what is explored now: a
// candidate past it keeps its record and reaches the deferred frontier.
//
// A stage without observation authority — the challenger, the synthesizer —
// drops the observations its candidates arrived with and records that it
// did, once per candidate, as a warning rather than the run's verdict. The
// table still decides what is persisted: none of those observations is. What
// it no longer decides is whether the pass survives, because a model that
// attaches an observation to every objection it raises (#171) would otherwise
// turn every challenge and synthesis pass into a failure that spends the run
// and records nothing — and the objection beside the observation, which is
// the material the stage exists to produce, was never the problem.
func (c *Controller) develop(st *state, stage Stage, runID string, committed map[string]Commit, auth authority, res *Result) {
	if !auth.observations {
		for i := range res.Candidates {
			cand := &res.Candidates[i]
			if len(cand.Observations) > 0 {
				st.warn(stage, FailureAuthority, c.now(), fmt.Errorf(
					"%w: the %s stage cannot develop observations; the %d candidate %q arrived with were dropped",
					ErrStageAuthority, stage, len(cand.Observations), cand.Ref))
				cand.Observations = nil
			}
		}
		return
	}
	developed := 0
	for _, cand := range res.Candidates {
		if len(cand.Observations) == 0 {
			continue
		}
		hypothesisID, ok := st.hypotheses[cand.Ref]
		if !ok {
			continue
		}
		if st.ctx.Err() != nil {
			return
		}
		if st.opt.Budget.Develop > 0 && developed >= st.opt.Budget.Develop {
			st.deferReasons[hypothesisID] = "the pass's development budget was exhausted before this candidate"
			for _, obs := range cand.Observations {
				st.undeveloped[obs.Ref] = true
			}
			continue
		}
		developed++
		for _, obs := range cand.Observations {
			if !c.recipeAllowed(stage, obs.Recipe) {
				st.fail(stage, FailureUnknownRecipe, c.now(), fmt.Errorf(
					"%w: observation %q cites %s@%d", ErrUnknownRecipe, obs.Ref, obs.Recipe.ID, obs.Recipe.Version))
				continue
			}
			// A committed record was verified when it was written; the
			// resumed attempt's trace need not serve it again.
			if _, done := committed[obs.Ref]; !done {
				if err := st.served.verify("observation", obs.Ref, obs.Claim.Evidence, obs.Claim.CounterEvidence); err != nil {
					st.fail(stage, FailureProvenance, c.now(), err)
					continue
				}
			}
			id, reused, err := c.putObservation(st, stage, runID, committed, hypothesisID, obs)
			if err != nil {
				st.fail(stage, FailureDevelopmentPath, c.now(),
					fmt.Errorf("explore: persist observation %q: %w", obs.Ref, err))
				continue
			}
			st.observations[obs.Ref] = id
			st.out.Observations = append(st.out.Observations, id)
			c.record(st, RecordEvent{Stage: stage, Type: frontier.EntityObservation, Ref: obs.Ref, ID: id, Reused: reused})
		}
	}
}

// object records §5.4's criticism. A locator-backed objection becomes a
// counter-observation against the hypothesis it attacks; one resting on a
// consequence, a missing check, or an alternative becomes a new candidate
// linked as contradicting its target, because §4.3 forbids an observation with
// no locator. Neither path can reach a finding.
func (c *Controller) object(st *state, stage Stage, runID string, committed map[string]Commit, auth authority, res *Result) {
	if len(res.Objections) == 0 {
		return
	}
	if !auth.objections {
		st.fail(stage, FailureAuthority, c.now(), fmt.Errorf(
			"%w: the %s stage emitted %d objection(s); only a challenger objects",
			ErrStageAuthority, stage, len(res.Objections)))
		return
	}
	for _, obj := range res.Objections {
		if !obj.Grounds.valid() {
			st.fail(stage, FailureAuthority, c.now(), fmt.Errorf(
				"%w: objection %q rests on %q, which is not evidence, a consequence, a missing check or an alternative",
				ErrStageAuthority, obj.Ref, obj.Grounds))
			continue
		}
		target, ok := st.hypotheses[obj.Hypothesis]
		if !ok {
			st.fail(stage, FailureUnknownRecord, c.now(), fmt.Errorf(
				"%w: objection %q attacks %q", ErrUnknownReference, obj.Ref, obj.Hypothesis))
			continue
		}
		if !c.recipeAllowed(stage, obj.Recipe) {
			st.fail(stage, FailureUnknownRecipe, c.now(), fmt.Errorf(
				"%w: objection %q cites %s@%d", ErrUnknownRecipe, obj.Ref, obj.Recipe.ID, obj.Recipe.Version))
			continue
		}
		if _, done := committed[obj.Ref]; !done {
			if err := st.served.verify("objection", obj.Ref, obj.Claim.Evidence, obj.Claim.CounterEvidence); err != nil {
				st.fail(stage, FailureProvenance, c.now(), err)
				continue
			}
		}
		id, kind, reused, err := c.putObjection(st, stage, runID, committed, target, obj)
		if err != nil {
			st.fail(stage, FailureStorage, c.now(),
				fmt.Errorf("explore: persist objection %q: %w", obj.Ref, err))
			continue
		}
		st.out.Objections = append(st.out.Objections, id)
		switch kind {
		case frontier.EntityObservation:
			st.observations[obj.Ref] = id
			st.out.Observations = append(st.out.Observations, id)
		case frontier.EntityHypothesis:
			st.hypotheses[obj.Ref] = id
			st.out.Hypotheses = append(st.out.Hypotheses, id)
			st.note(id)
		}
		c.record(st, RecordEvent{Stage: stage, Type: kind, Ref: obj.Ref, ID: id, Reused: reused})
	}
}

// consolidate writes the findings and proposals a stage proposed, and applies
// the promotion transition its finding justifies.
//
// Only this control plane promotes, and only after the structured result
// validates. A consolidation naming something that is not a locator-backed
// observation is refused: §5.4 lets the synthesizer consolidate developed
// observations and recorded objections, and an unsupported addition stays a
// hypothesis rather than becoming evidence by being cited.
func (c *Controller) consolidate(st *state, stage Stage, runID string, committed map[string]Commit, auth authority, res *Result) {
	if len(res.Consolidations) == 0 {
		return
	}
	if !auth.consolidate {
		st.fail(stage, FailureAuthority, c.now(), fmt.Errorf(
			"%w: the %s stage delivered %d consolidation(s)",
			ErrUnauthorizedFinding, stage, len(res.Consolidations)))
		return
	}
	for _, con := range res.Consolidations {
		ids, err := c.supporting(st, con)
		if errors.Is(err, errConsolidationDeferred) {
			// The budget left this consolidation's observations
			// undeveloped, so the consolidation belongs to the resumed run.
			// The candidates behind it are already deferred with the budget
			// as their reason, and recording a refusal on top would report a
			// worker failure for a decision Babel made.
			continue
		}
		if err != nil {
			st.fail(stage, failureCodeFor(err), c.now(),
				fmt.Errorf("explore: consolidation %q: %w", con.Ref, err))
			continue
		}
		if _, done := committed[con.Ref]; !done {
			if err := st.served.verify("consolidation", con.Ref, con.Finding.CounterEvidence); err != nil {
				st.fail(stage, FailureProvenance, c.now(), err)
				continue
			}
		}
		finding, reused, err := c.putFinding(st, stage, runID, committed, con, ids)
		if err != nil {
			st.fail(stage, FailureDevelopmentPath, c.now(),
				fmt.Errorf("explore: persist finding %q: %w", con.Ref, err))
			continue
		}
		st.out.Findings = append(st.out.Findings, finding.ID)
		c.record(st, RecordEvent{Stage: stage, Type: frontier.EntityFinding, Ref: con.Ref, ID: finding.ID, Reused: reused})

		// A consolidation's actions attach to the proposal when it
		// suggested one and to the finding when it did not: §4.5 makes
		// the proposal the artifact an operator reviews, so a draft-issue
		// hung off the finding underneath it would point past the record
		// the operator is actually looking at.
		bearer := frontier.Ref{Type: frontier.EntityFinding, ID: finding.ID}
		if con.Proposal != nil {
			id, reusedProposal, err := c.putConsolidatedProposal(st, stage, runID, committed, con, finding.ID)
			if err != nil {
				st.fail(stage, failureCodeFor(err), c.now(),
					fmt.Errorf("explore: persist proposal for %q: %w", con.Ref, err))
			} else {
				st.out.Proposals = append(st.out.Proposals, id)
				c.record(st, RecordEvent{Stage: stage, Type: frontier.EntityProposal, Ref: con.Ref + "/proposal", ID: id, Reused: reusedProposal})
				bearer = frontier.Ref{Type: frontier.EntityProposal, ID: id}
			}
		}
		c.putDispositions(st, stage, runID, bearer, con.Dispositions)

		for _, hypothesisID := range finding.HypothesisIDs {
			if st.promoted[hypothesisID] {
				continue
			}
			if _, err := c.cfg.Frontier.SetStatus(st.commit, frontier.StatusInput{
				HypothesisID: hypothesisID,
				Status:       frontier.StatusPromoted,
				RunID:        runID,
				Note:         "consolidated into a finding by the " + string(stage) + " stage",
			}); err != nil {
				st.fail(stage, FailureStorage, c.now(),
					fmt.Errorf("explore: promote %s: %w", hypothesisID, err))
				continue
			}
			st.promoted[hypothesisID] = true
			st.out.Promoted = append(st.out.Promoted, hypothesisID)
		}
	}
}

// supporting resolves a consolidation's observation references and enforces
// §5.4's rule that only locator-backed observations may be consolidated.
//
// The evidence check is made here rather than trusted to the store. The store
// already refuses an evidence-free observation, so this can only fail when a
// reference names something else — and that is the case worth naming, because
// "the synthesizer cited a candidate as if it were evidence" and "the store is
// broken" are different problems.
func (c *Controller) supporting(st *state, con Consolidation) ([]string, error) {
	ids := make([]string, 0, len(con.Observations))
	for _, ref := range con.Observations {
		id, ok := st.observations[ref]
		if !ok && servedObservation(st.out.Retrieval, ref) {
			// A frontier search disclosed this record to the worker, which
			// is the same footing a brief gives it: named by Babel, not
			// guessed. It resolves to itself and stays resolvable.
			id, ok = ref, true
			st.observations[ref] = ref
		}
		if !ok {
			if st.undeveloped[ref] {
				return nil, errConsolidationDeferred
			}
			if _, isCandidate := st.hypotheses[ref]; isCandidate {
				return nil, fmt.Errorf("%w: %q is a candidate hypothesis, not a locator-backed observation", ErrDevelopmentPath, ref)
			}
			return nil, fmt.Errorf("%w: no observation %q", ErrUnknownReference, ref)
		}
		record, err := c.cfg.Frontier.Observation(st.commit, id)
		if err != nil {
			return nil, fmt.Errorf("%w: observation %q: %v", ErrUnknownReference, ref, err)
		}
		if record.EvidenceCount == 0 {
			return nil, fmt.Errorf("%w: observation %q carries no evidence locator", ErrDevelopmentPath, ref)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// putConsolidatedProposal verifies a consolidation's proposal citations and
// writes it. The finding it hangs off is already durable: a proposal citing
// something the run was not served is a refused proposal, never a lost
// finding.
func (c *Controller) putConsolidatedProposal(st *state, stage Stage, runID string, committed map[string]Commit, con Consolidation, findingID string) (string, bool, error) {
	if _, done := committed[con.Ref+"/proposal"]; !done {
		if err := st.served.verify("proposal", con.Ref+"/proposal", con.Proposal.Supporting, con.Proposal.Conflicting); err != nil {
			return "", false, err
		}
	}
	return c.putProposal(st, stage, runID, committed, con, findingID)
}

func failureCodeFor(err error) string {
	switch {
	case errors.Is(err, ErrDevelopmentPath):
		return FailureDevelopmentPath
	case errors.Is(err, ErrUnknownReference):
		return FailureUnknownRecord
	case errors.Is(err, ErrUnservedEvidence):
		return FailureProvenance
	default:
		return FailureStorage
	}
}

// schedule records what the job said it would not develop. §5.2 keeps this to
// scheduling: a deferred or rejected candidate keeps its record and its
// wording, and only the lifecycle history changes.
func (c *Controller) schedule(st *state, stage Stage, auth authority, res *Result) {
	if len(res.Deferred) == 0 && len(res.Rejected) == 0 {
		return
	}
	if !auth.schedule {
		st.fail(stage, FailureAuthority, c.now(), fmt.Errorf(
			"%w: the %s stage cannot schedule the frontier", ErrStageAuthority, stage))
		return
	}
	// A disposal naming a handle the same result never declared is recorded
	// and dropped, not fatal. It is the same shape of degradation as an
	// unwritable reference edge (FailureReference): the candidate the note
	// was about does not exist, so nothing durable is corrupted or
	// contradicted and only the scheduling note is lost. Failing the run
	// instead would discard a verdict that every other stage earned - a
	// synthesized finding included - over one stray handle in the
	// discovery pass.
	for _, d := range res.Deferred {
		id, ok := st.hypotheses[d.Hypothesis]
		if !ok {
			st.warn(stage, FailureUnknownRecord, c.now(), fmt.Errorf(
				"%w: deferral names %q", ErrUnknownReference, d.Hypothesis))
			continue
		}
		if d.Reason != "" {
			st.deferReasons[id] = d.Reason
		}
	}
	for _, d := range res.Rejected {
		id, ok := st.hypotheses[d.Hypothesis]
		if !ok {
			st.warn(stage, FailureUnknownRecord, c.now(), fmt.Errorf(
				"%w: rejection names %q", ErrUnknownReference, d.Hypothesis))
			continue
		}
		if st.rejected[id] || st.promoted[id] {
			continue
		}
		reason := d.Reason
		if reason == "" {
			reason = "rejected by the " + string(stage) + " stage"
		}
		if _, err := c.cfg.Frontier.SetStatus(st.commit, frontier.StatusInput{
			HypothesisID: id,
			Status:       frontier.StatusRejected,
			RunID:        st.opt.RunID,
			Note:         reason,
		}); err != nil {
			st.fail(stage, FailureStorage, c.now(), fmt.Errorf("explore: reject %s: %w", id, err))
			continue
		}
		st.rejected[id] = true
		st.out.Rejected = append(st.out.Rejected, id)
		st.rejectedRecords = append(st.rejectedRecords, run.Candidate{ID: id, Reason: reason, At: c.now()})
	}
}

// deferRemainder checkpoints the unexplored frontier. §5.2: a finite run
// defers its remainder, it does not erase it — and a resumed attempt does not
// append a second deferral to a candidate that is already deferred, because
// only an untriaged candidate is one this pass left behind.
func (c *Controller) deferRemainder(st *state) {
	var ids []string
	for _, id := range st.touched {
		if st.promoted[id] || st.rejected[id] {
			continue
		}
		record, err := c.cfg.Frontier.Hypothesis(st.commit, id)
		if err != nil {
			st.fail(StageExplore, FailureStorage, c.now(),
				fmt.Errorf("explore: read candidate %s before deferring: %w", id, err))
			continue
		}
		if record.Status != frontier.StatusUntriaged {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return
	}
	if _, err := c.cfg.Frontier.DeferFrontier(st.commit, st.opt.RunID, ids,
		"deferred by a finite run; the frontier keeps it for a later pass"); err != nil {
		st.fail(StageExplore, FailureStorage, c.now(), fmt.Errorf("explore: defer the remainder: %w", err))
		return
	}
	at := c.now()
	st.out.Deferred = ids
	for _, id := range ids {
		reason := st.deferReasons[id]
		if reason == "" {
			reason = "this finite run did not develop it"
		}
		st.deferredRecords = append(st.deferredRecords, run.Candidate{ID: id, Reason: reason, At: at})
	}
}
