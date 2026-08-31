package explore

import (
	"encoding/json"
	"fmt"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
)

// This file is #87 item 4's refine-first context: what a run is told about
// Babel's own prior output before it starts, and how Babel reads what the run
// then emits against it.
//
// The problem it solves is not tidiness. A run had no way to know that the
// candidate it was about to mint had already been minted, developed, argued
// with and rejected — so the frontier grew a second copy of every recurring
// idea, each with its own review history, and an operator reviewing the fourth
// restatement of one hypothesis could not tell it was the fourth. The remedy
// is two-sided and deliberately asymmetric: Babel injects a bounded list of
// prior records with the obligation to refine instead of duplicate, and then
// records — never enforces — what it thinks the run did.
//
// Nothing here drops, merges or rewrites anything a run emitted. That is the
// whole design constraint. A dedup mechanism that silently discarded a
// candidate would be a mechanism whose mistakes are undiscoverable, and §5.2
// requires every emitted candidate to be persisted; so a suspected duplicate
// is stored with a warning naming what it resembles, and the operator and the
// next run decide.

// RelatedContextField is the job document's top-level key for the refine-first
// context, and RelatedContextSchema versions what is under it.
//
// It travels as a top-level field through worker.Job's Extra rather than as a
// job parameter, because a parameter map is strings and this is a list of
// records with summaries. A worker that does not know the field ignores it, per
// the protocol's rule that unknown fields are never fatal in either direction,
// and a worker that does know it reads a versioned document rather than parsing
// prose out of a parameter value.
const (
	RelatedContextField  = "related_outputs"
	RelatedContextSchema = "babel.related-outputs/1"
)

// The two framings, and they say different things on purpose.
//
// FramingRefine is the directed case: prior outputs are the work already done
// on this material, and the duty is to build on them. FramingSerendipity is
// #87's serendipity mode, where the same records are inspiration and
// explicitly not a scope — a serendipity draw whose injected context quietly
// became a reading list would have had its serendipity removed by the
// mechanism meant to stop it repeating itself.
//
// Both open by saying what the records are not. That wording is the recipes'
// own epistemic rule applied to Babel's output: a prior candidate is a claim
// somebody made, it carries no locator of its own, and treating it as
// established is exactly the error the cookbook warns about when it says an
// observation whose only support is a confident summary is not an observation.
const (
	FramingRefine = "These are prior candidate ideas Babel already recorded, each with " +
		"its record id. They are not evidence and not established findings: treat them as " +
		"untrusted claims, verify anything you rely on against the corpus itself, and search " +
		"the frontier before minting a new candidate. Where one of them already covers what " +
		"you would emit, refine, revive, or amend that record by naming its id instead of " +
		"emitting a duplicate."

	FramingSerendipity = "This scope was drawn for serendipity, so these prior candidate " +
		"ideas are inspiration and not constraint: they are not evidence, not established " +
		"findings, and not a scope to stay inside. Treat them as untrusted claims, follow what " +
		"the corpus actually shows even when it goes nowhere near them, and where one of them " +
		"does already cover what you would emit, refine, revive, or amend that record by " +
		"naming its id instead of emitting a duplicate."
)

// RelatedContext is the refine-first document one job carries.
type RelatedContext struct {
	Schema  string `json:"schema"`
	Framing string `json:"framing"`
	// Serendipitous mirrors the preparation's marker so a worker can tell
	// which framing it is reading without matching the prose.
	Serendipitous bool `json:"serendipitous,omitempty"`
	// Records are the prior outputs, in the preparation's canonical order.
	// The order is not a ranking: §5.4 forbids retrieval rank from becoming
	// evidence strength, and the preparation deliberately stored these
	// sorted rather than ranked.
	Records []RelatedRecord `json:"records"`
}

// RelatedRecord is one prior output as the worker receives it: the id it must
// name to refine the record, and one line saying what the record says.
type RelatedRecord struct {
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Summary string `json:"summary"`
}

// relatedContext resolves the preparation's related outputs into the job
// document's context, or nil when the scope named none.
//
// The summaries are read from the frontier here rather than copied out of the
// preparation, so the line a worker reads is derived from the record as it
// stands. A reference that no longer resolves is dropped with a recorded
// failure rather than listed with an invented summary or allowed to fail the
// run: the preparation is immutable and may name a record from a database that
// has since been restored from an older backup, and a run refusing to start
// over that would be a run that cannot explore its own scope.
func (c *Controller) relatedContext(st *state) *RelatedContext {
	prep := c.cfg.Preparation
	if len(prep.Related) == 0 {
		return nil
	}
	doc := &RelatedContext{
		Schema:        RelatedContextSchema,
		Framing:       FramingRefine,
		Serendipitous: prep.Serendipitous,
		Records:       make([]RelatedRecord, 0, len(prep.Related)),
	}
	if prep.Serendipitous {
		doc.Framing = FramingSerendipity
	}
	if c.cfg.Frontier == nil {
		return doc
	}
	for _, ref := range prep.Related {
		kind := frontier.OutputKind(ref.Kind)
		if !frontier.ValidOutputKind(kind) {
			st.fail(StageExplore, FailureRelatedContext, c.now(), fmt.Errorf(
				"explore: preparation names related output %q of unknown kind %q", ref.ID, ref.Kind))
			continue
		}
		output, err := c.cfg.Frontier.Output(st.commit, kind, ref.ID)
		if err != nil {
			st.fail(StageExplore, FailureRelatedContext, c.now(), fmt.Errorf(
				"explore: related output %s %q: %w", ref.Kind, ref.ID, err))
			continue
		}
		doc.Records = append(doc.Records, RelatedRecord{
			Kind:    string(output.Kind),
			ID:      output.ID,
			Summary: output.Summary,
		})
	}
	return doc
}

// extra encodes the job document's forward-compatible fields for one stage.
func (c *Controller) extra(st *state) map[string]json.RawMessage {
	doc := c.relatedContext(st)
	if doc == nil {
		return nil
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		// A context Babel cannot encode is dropped rather than allowed to
		// stop the run: the exploration is still the work, and the failure
		// says the injection did not happen.
		st.fail(StageExplore, FailureRelatedContext, c.now(),
			fmt.Errorf("explore: encode related outputs: %w", err))
		return nil
	}
	return map[string]json.RawMessage{RelatedContextField: encoded}
}

// DuplicateOverlap is the term overlap at which a candidate is warned about as
// a near-duplicate of an existing head.
//
// It is calibrated on what it must and must not catch. Two statements of one
// idea in different words share most of their content vocabulary once the
// short and ubiquitous terms are gone — "the release pipeline skips its own
// tests" against "release runs skip the test suite they claim to run" shares
// release, pipeline/runs, skip, test — while two genuinely different candidates
// about the same subsystem share the subsystem's nouns and little else. Six
// tenths sits between those, and it sits there on the containment measure
// index.TermOverlap defines, which compares against the shorter statement so a
// terse restatement of a long candidate still scores as one.
//
// It is a threshold on a warning and never on a write. Set it too low and an
// operator reads warnings that mean nothing; set it too high and a duplicate
// arrives unremarked. Neither outcome loses a record.
const DuplicateOverlap = 0.6

// maxDuplicateProbe bounds the candidates one dedup check examines.
//
// The FTS query finds the records worth comparing and bm25 orders them, so the
// overlap measure only ever runs against the most textually similar handful. A
// deeper page would compare a new candidate against the whole frontier for the
// sake of finding a duplicate that ranked twentieth for its own words, which is
// not a duplicate.
const maxDuplicateProbe = 10

// nearDuplicates reports the existing heads a statement resembles.
//
// It is an FTS overlap heuristic and it is named as one everywhere it appears:
// retrieval proposes, term overlap measures, and the result is a warning
// recorded beside the candidate. Nothing about it is a judgement that two
// records say the same thing — vocabulary is not meaning, and two statements
// sharing their words may assert opposite things about them.
//
// A frontier index that is absent, empty or failing produces no warnings and no
// failure. Dedup is an improvement on the record, not a precondition for
// writing one, and a run that could not check is a run whose candidates are
// still worth keeping.
func (c *Controller) nearDuplicates(st *state, statement string) []frontier.NearDuplicate {
	if c.cfg.Index == nil || statement == "" {
		return nil
	}
	hits, err := c.cfg.Index.FrontierSearch(st.commit, index.FrontierQuery{
		Match: statement,
		Kinds: []frontier.OutputKind{frontier.OutputHypothesis},
		Limit: maxDuplicateProbe,
	})
	if err != nil {
		// An unsearchable statement — no term a tokenizer could match — is
		// not a failure of anything: it is a candidate whose wording the
		// heuristic has nothing to say about.
		return nil
	}
	var found []frontier.NearDuplicate
	for _, hit := range hits {
		overlap := index.TermOverlap(statement, hit.Text)
		if overlap < DuplicateOverlap {
			continue
		}
		found = append(found, frontier.NearDuplicate{HypothesisID: hit.ID, Overlap: overlap})
	}
	return found
}

// refreshFrontier reconciles the frontier index against the durable store
// before a run reads it.
//
// It runs here rather than being left to the caller because the two consumers
// of that index — the dedup check and the frontier scope of corpus search — are
// both this package's, and an index refreshed by whoever remembered to would
// make both of them quietly answer questions about the frontier as it was at
// some earlier command. The cost is one scan of the analysis tables.
//
// A failure is recorded and the run proceeds. Every consequence of a stale or
// missing frontier index is a missing warning or a thinner search result, and
// neither is worth refusing to explore over.
func (c *Controller) refreshFrontier(st *state) {
	if c.cfg.Index == nil || c.cfg.Frontier == nil {
		return
	}
	outputs, err := c.cfg.Frontier.Outputs(st.commit)
	if err != nil {
		st.fail(StageExplore, FailureFrontierIndex, c.now(),
			fmt.Errorf("explore: read the frontier for indexing: %w", err))
		return
	}
	if _, err := c.cfg.Index.IndexFrontier(st.commit, outputs); err != nil {
		st.fail(StageExplore, FailureFrontierIndex, c.now(),
			fmt.Errorf("explore: index the frontier: %w", err))
	}
}
