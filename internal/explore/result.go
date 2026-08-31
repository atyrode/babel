package explore

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/atyrode/babel/internal/disposition"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/worker"
)

// Result is one analysis job's structured output, decoded from the payload of
// a terminal result declaring worker.ResultSchema. Anything declaring another
// schema is refused rather than parsed hopefully, because a payload
// interpreted under the wrong schema would produce durable records nobody
// wrote.
//
// The types below are internal/frontier's payload types verbatim rather than a
// parallel set of wire structs. That is a deliberate coupling: the fields a
// worker proposes and the fields Babel stores are the same information, and
// two declarations of it would drift into a translation layer that silently
// dropped one of them. The cost is that a change to a frontier payload is a
// change to the schema, and therefore requires worker.ResultSchema to change
// too — the string itself lives in internal/worker because it is wire surface
// shared with a separately-developed worker implementation, and naming it from
// here is what keeps exactly one definition of it.
//
// Every field is optional and an absent field is a statement: a job that
// emitted no candidate emitted none, which §5.2 permits. What a stage is
// allowed to put here is not uniform — a challenger may object but may not
// consolidate (§5.4) — and that check happens per item during persistence, so
// one item a stage had no authority to create is a recorded failure rather
// than a discarded result.
type Result struct {
	// Candidates are the hypotheses this job emitted, with any observations
	// it developed against them. §5.2 requires every one of them to be
	// persisted before any sorting, so the order here is emission order and
	// carries no ranking.
	Candidates []Candidate `json:"candidates,omitempty"`
	// Consolidations are the findings this job proposes over observations
	// that already exist or that this same result developed (§4.4, §6.6).
	Consolidations []Consolidation `json:"consolidations,omitempty"`
	// Objections are the challenger's criticisms (§5.4).
	Objections []Objection `json:"objections,omitempty"`
	// Deferred and Rejected are the candidates this job surfaced and chose
	// not to develop, with the worker's own reason. They never remove a
	// record: §5.2 requires a finite run to defer its remainder.
	Deferred []Disposal `json:"deferred,omitempty"`
	Rejected []Disposal `json:"rejected,omitempty"`
}

// Candidate is one emitted hypothesis and the claims developed against it.
type Candidate struct {
	// Ref is the worker's own reference for this candidate, unique within the
	// result. Babel binds it to the durable record it creates so a resumed
	// run recognizes the same candidate instead of writing a second copy.
	Ref string `json:"ref"`
	// Hypothesis is the candidate in the model's own wording (§5.2).
	Hypothesis frontier.HypothesisPayload `json:"hypothesis"`
	// Observations are the §4.3 claims developed against it. An empty list
	// is valid: §4.2 preserves a speculative candidate, and a finite run may
	// leave it for a later pass.
	Observations []Observation `json:"observations,omitempty"`
	// Dispositions are the next actions the job proposes for the candidate
	// (#87). They are proposals about what an operator could do with the
	// record and never actions: persisting one renders a button, not an
	// issue, a fact, or a memory.
	Dispositions []ProposedAction `json:"dispositions,omitempty"`
}

// Observation is one provenance-bearing claim a job developed.
type Observation struct {
	// Ref is the worker's reference for this claim, unique within the result
	// and the key a consolidation names it by.
	Ref string `json:"ref"`
	// Recipe is the §5.1 recipe provenance. It must name a recipe the run
	// actually selected for this stage: provenance that cannot be traced to
	// an asset the receipt records is not provenance.
	Recipe worker.RecipeRef `json:"recipe"`
	// Claim carries the evidence locators, gradings, and counter-evidence
	// position §4.3 requires. Its evidence is decoded through
	// frontier.Evidence, so a locator that cannot recover its bytes fails to
	// parse rather than reaching a durable record.
	Claim frontier.ObservationPayload `json:"claim"`
}

// Consolidation is one proposed finding, and optionally the proposal it
// suggests (§4.4, §4.5).
type Consolidation struct {
	Ref string `json:"ref"`
	// Observations names the supporting claims, each by the ref this result
	// emitted it under or by the durable identifier the job's brief listed.
	// Both resolve to a stored observation; anything that resolves to
	// neither is a refused consolidation, never a repaired one.
	Observations []string                `json:"observations"`
	Finding      frontier.FindingPayload `json:"finding"`
	// Proposal is §4.5's review artifact, when the job suggests one.
	Proposal *frontier.ProposalPayload `json:"proposal,omitempty"`
	// Dispositions are the next actions the job proposes for what this
	// consolidation produced (#87): the proposal when it suggested one,
	// because §4.5 makes the proposal the reviewable artifact, and the
	// finding when it did not.
	Dispositions []ProposedAction `json:"dispositions,omitempty"`
}

// Grounds is what a challenger's criticism rests on. §5.4 requires an
// objection to be grounded in evidence, consequences, missing checks, or
// concrete alternatives, and forbids inferring character, ability, emotion, or
// intent — so the vocabulary is closed and an objection that names none of the
// four is refused rather than stored as an unattributable complaint.
type Grounds string

// The §5.4 grounds an objection may rest on.
const (
	GroundsEvidence     Grounds = "evidence"
	GroundsConsequence  Grounds = "consequence"
	GroundsMissingCheck Grounds = "missing-check"
	GroundsAlternative  Grounds = "alternative"
)

func (g Grounds) valid() bool {
	switch g {
	case GroundsEvidence, GroundsConsequence, GroundsMissingCheck, GroundsAlternative:
		return true
	}
	return false
}

// Objection is one challenger criticism of a hypothesis (§5.4).
//
// What it becomes depends on whether it is locator-backed, and that is the
// whole of the challenger's authority. A criticism carrying evidence is a
// counter-observation against the hypothesis it attacks. A criticism resting
// on a consequence, a missing check, or an alternative carries no locator, so
// §4.3 forbids it from being an observation: it becomes a new candidate
// hypothesis linked as contradicting its target, which is exactly the third
// thing §5.4 permits a challenger to emit. Neither path can reach a finding.
type Objection struct {
	Ref string `json:"ref"`
	// Hypothesis names the candidate under attack, by the ref this result
	// emitted it under or by the durable identifier the brief listed.
	Hypothesis string           `json:"hypothesis"`
	Grounds    Grounds          `json:"grounds"`
	Recipe     worker.RecipeRef `json:"recipe"`
	// Claim is the criticism. Its evidence decides what the objection can
	// become and is never assumed: an empty evidence list is a valid
	// objection, just not an observation.
	Claim frontier.ObservationPayload `json:"claim"`
}

// Disposal is one candidate a job surfaced and did not develop, with the
// reason it gave. §5.2 confines this to ordering and scheduling: a deferred or
// rejected candidate stays in the frontier with its original wording.
type Disposal struct {
	// Hypothesis names the candidate, by result ref or durable identifier.
	Hypothesis string `json:"hypothesis"`
	Reason     string `json:"reason"`
}

// ProposedAction is one next action a job proposes against the record it is
// attached to (#87). It is the schema field that lets a run propose a
// disposition, and it is deliberately additive: a worker that emits none is a
// worker that proposed none, and the result schema does not move for a field
// both sides may ignore, which is the same rule worker.ProtocolVersion states
// for optional message fields.
//
// Babel refuses a kind it does not implement rather than storing it as an
// opaque string. The five kinds are five surfaces a click feeds; a sixth would
// be a button wired to nothing.
type ProposedAction struct {
	// Ref is the worker's own reference for this action, unique within the
	// result. It is the resume key: a re-run replaying its result finds the
	// action it already proposed instead of proposing a second copy.
	Ref  string           `json:"ref"`
	Kind disposition.Kind `json:"kind"`
	// Summary and Rationale are the action in the model's own words.
	Summary   string `json:"summary"`
	Rationale string `json:"rationale,omitempty"`
	// Workspace is the local checkout a draft-issue binds to, and is
	// required of that kind alone. Babel verifies it against the checkout's
	// own git configuration (#88) rather than trusting the repository the
	// worker names, so an unverifiable workspace is a refused action, never
	// a draft aimed at a repository nobody confirmed exists.
	Workspace string `json:"workspace,omitempty"`
}

// Errors a structured result can produce. They are sentinels because the
// difference between "the counterpart speaks a schema Babel does not
// implement", "the counterpart skipped a step in the development path", and
// "the counterpart exceeded its stage's authority" is the difference between
// upgrading Babel, reviewing a recipe, and distrusting a job.
var (
	// ErrNoResult reports a job that produced no terminal result. It is
	// distinct from an empty result: emitting nothing is an outcome, and
	// never getting there is a failure.
	ErrNoResult = errors.New("explore: worker delivered no structured result")

	// ErrResultSchema reports a result Babel cannot read: a schema it does
	// not implement, or a payload that does not decode under the one it
	// declared. A locator that cannot recover its bytes lands here too,
	// because frontier.Evidence refuses to decode one.
	ErrResultSchema = errors.New("explore: structured result does not match the required schema")

	// ErrDevelopmentPath reports a result that skipped a step of §4.2's
	// mandatory path: a consolidation whose supporting observations do not
	// exist, or one that names a record that is not an observation. Babel
	// refuses it and records the refusal; it never invents the missing step.
	ErrDevelopmentPath = errors.New("explore: structured result skips the development path")

	// ErrUnauthorizedFinding reports a stage that tried to create or promote
	// a finding without the authority to. §5.4 gives the challenger none.
	ErrUnauthorizedFinding = errors.New("explore: this stage cannot create or promote a finding")

	// ErrStageAuthority reports material a stage may not emit at all — a
	// challenger developing its own observations, or a job objecting outside
	// a challenge.
	ErrStageAuthority = errors.New("explore: structured result contains material this stage cannot emit")

	// ErrUnknownRecipe reports recipe provenance naming an asset the run did
	// not select for this stage. §5.1 provenance the receipt cannot confirm
	// is not provenance.
	ErrUnknownRecipe = errors.New("explore: claim names a recipe this stage did not select")

	// ErrUnknownReference reports a reference resolving to nothing: an
	// objection against an unknown candidate, a disposal of one, or a
	// consolidation naming an identifier no brief listed.
	ErrUnknownReference = errors.New("explore: structured result names a record that does not exist")
)

// parseResult decodes and structurally validates a worker's terminal result.
//
// It checks the shape of the whole document and nothing about its content:
// Babel validates structure and provenance, never analytical correctness
// (§6.5). A duplicate reference is fatal to the stage rather than to one item,
// because two items under one reference make the resume ledger's binding
// ambiguous and there is no honest way to choose between them.
func parseResult(rec *worker.ResultRecord) (*Result, error) {
	if rec == nil {
		return nil, ErrNoResult
	}
	if rec.Schema != worker.ResultSchema {
		return nil, fmt.Errorf("%w: worker declared %q, this build requires %q",
			ErrResultSchema, rec.Schema, worker.ResultSchema)
	}
	var res Result
	if len(rec.Payload) > 0 {
		if err := json.Unmarshal(rec.Payload, &res); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrResultSchema, err)
		}
	}
	refs := make(map[string]struct{})
	claim := func(kind, ref string) error {
		if ref == "" {
			return fmt.Errorf("%w: a %s carries no ref", ErrResultSchema, kind)
		}
		if _, dup := refs[ref]; dup {
			return fmt.Errorf("%w: ref %q is emitted twice", ErrResultSchema, ref)
		}
		refs[ref] = struct{}{}
		return nil
	}
	for _, cand := range res.Candidates {
		if err := claim("candidate", cand.Ref); err != nil {
			return nil, err
		}
		for _, obs := range cand.Observations {
			if err := claim("observation", obs.Ref); err != nil {
				return nil, err
			}
		}
		for _, act := range cand.Dispositions {
			if err := claim("disposition", act.Ref); err != nil {
				return nil, err
			}
		}
	}
	for _, con := range res.Consolidations {
		if err := claim("consolidation", con.Ref); err != nil {
			return nil, err
		}
		if len(con.Observations) == 0 {
			return nil, fmt.Errorf("%w: consolidation %q names no observation", ErrDevelopmentPath, con.Ref)
		}
		for _, act := range con.Dispositions {
			if err := claim("disposition", act.Ref); err != nil {
				return nil, err
			}
		}
	}
	for _, obj := range res.Objections {
		if err := claim("objection", obj.Ref); err != nil {
			return nil, err
		}
	}
	return &res, nil
}
