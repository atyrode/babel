package explore

import (
	"fmt"

	"github.com/atyrode/babel/internal/complaint"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/reference"
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

// The refine-first context reaches the model as a section of the prompt
// (composePrompt) rather than as a field of any wire document: prior candidates
// are the run's material, and the prompt is the one place material travels.

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

// RelatedContext is the refine-first section one prompt carries.
type RelatedContext struct {
	Framing string
	// Serendipitous mirrors the preparation's marker so a worker can tell
	// which framing it is reading without matching the prose.
	Serendipitous bool
	// Records are the prior outputs, in the preparation's canonical order.
	// The order is not a ranking: §5.4 forbids retrieval rank from becoming
	// evidence strength, and the preparation deliberately stored these
	// sorted rather than ranked.
	Records []RelatedRecord
}

// RelatedRecord is one prior output as the model receives it: the id it must
// name to refine the record, and one line saying what the record says.
type RelatedRecord struct {
	Kind    string
	ID      string
	Summary string
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
		output, err := c.relatedOutput(st, kind, ref.ID)
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
		// Recorded here, where the injection is decided, rather than read
		// back off the preparation: this is the set that reached a worker,
		// and the inspired-by edges of #113 may only name records a run was
		// actually shown.
		if namespace, addressable := graphNamespace(output.Kind); addressable {
			st.inject(reference.RecordRef{Kind: namespace, ID: output.ID})
		}
	}
	return doc
}

// relatedOutput reads one related record from whichever store owns its kind.
//
// The dispatch exists because the retrieval index's kind vocabulary outgrew the
// frontier: a complaint is indexed beside Babel's own output and retrieved by
// the same query, but internal/complaint stores it and the frontier has never
// heard of it (#115). Asking the frontier for one would report the operator's
// own words as an unknown record.
//
// A machine that opened no complaint component says so rather than reporting
// absence. The preparation is immutable and names what it named; "this build
// has no complaint store" and "that complaint is gone" are different facts, and
// the recorded failure should be the true one.
func (c *Controller) relatedOutput(st *state, kind frontier.OutputKind, id string) (frontier.Output, error) {
	if kind != frontier.OutputComplaint {
		return c.cfg.Frontier.Output(st.commit, kind, id)
	}
	if c.cfg.Complaints == nil {
		return frontier.Output{}, fmt.Errorf("this instance opened no complaint store")
	}
	return c.cfg.Complaints.Output(st.commit, id)
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

// writtenStatement is one candidate this run has already persisted, kept for
// the dedup probe alone: the identifier a warning would name, and the wording
// the overlap measure compares against.
type writtenStatement struct {
	id        string
	statement string
}

// nearDuplicates reports the existing heads a statement resembles.
//
// It is an FTS overlap heuristic and it is named as one everywhere it appears:
// retrieval proposes, term overlap measures, and the result is a warning
// recorded beside the candidate. Nothing about it is a judgement that two
// records say the same thing — vocabulary is not meaning, and two statements
// sharing their words may assert opposite things about them.
//
// The index is refreshed once, before the run starts, so it cannot see what
// this run has written since. That gap is not academic: a run's own challenge
// and synthesis stages restate their explore stage's candidates, and eight
// concurrent cycles write into a frontier none of them can read. So the
// probe measures against two things — the indexed heads, and the statements
// this run has already persisted — and the second needs no index at all.
//
// A frontier index that is absent, empty or failing produces no index
// warnings and no failure. Dedup is an improvement on the record, not a
// precondition for writing one, and a run that could not check is a run whose
// candidates are still worth keeping.
func (c *Controller) nearDuplicates(st *state, statement string) []frontier.NearDuplicate {
	if statement == "" {
		return nil
	}
	var found []frontier.NearDuplicate
	seen := map[string]bool{}
	add := func(id string, overlap float64) {
		if overlap < DuplicateOverlap || seen[id] {
			return
		}
		seen[id] = true
		found = append(found, frontier.NearDuplicate{HypothesisID: id, Overlap: overlap})
	}
	for _, written := range st.statements {
		add(written.id, index.TermOverlap(statement, written.statement))
	}
	if c.cfg.Index == nil {
		return found
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
		return found
	}
	for _, hit := range hits {
		add(hit.ID, index.TermOverlap(statement, hit.Text))
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
	// The complaint heads join the same set, because IndexFrontier reconciles
	// the whole local partition and deletes the rows the set does not name: a
	// frontier-only pass here would delete every complaint `babel tell`
	// indexed, and the operator's words would appear and vanish depending on
	// which command ran last.
	outputs, err = complaint.Append(st.commit, c.cfg.Complaints, outputs)
	if err != nil {
		st.fail(StageExplore, FailureFrontierIndex, c.now(),
			fmt.Errorf("explore: read the complaints for indexing: %w", err))
		return
	}
	if _, err := c.cfg.Index.IndexFrontier(st.commit, outputs); err != nil {
		st.fail(StageExplore, FailureFrontierIndex, c.now(),
			fmt.Errorf("explore: index the frontier: %w", err))
	}
}
