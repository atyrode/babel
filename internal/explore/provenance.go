package explore

import (
	"fmt"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
)

// served is the evidence one run has disclosed so far: every locator a corpus
// search served, and the whole-document locator of every research fetch. It
// is what a citation is checked against before the claim carrying it becomes
// durable.
//
// The check is exact. A locator is a path, a line, a byte offset and a digest,
// and a citation is accepted only when all four are one Babel served: a
// worker may copy a locator, never compose one. The alternative — accept any
// digest that happens to reopen bytes somewhere in the archive — would let a
// model cite material outside the run's preparation, which §2.6 fixes before
// work starts.
//
// A research document is cited by its source URL and its content digest, with
// no line and no offset, because that is the whole object Babel served and the
// digest covers exactly those bytes. Frontier hits are not here: they are
// Babel's own records, named by identifier, and never evidence.
type served map[event.Locator]struct{}

// servedByRun indexes what the run's retrievals disclosed. The index covers
// every stage so far rather than the current one alone, because the trace is
// the run's: a synthesizer that searched nothing may still consolidate what
// the exploration was served, and a locator is real or not for the run.
func servedByRun(retrievals []Retrieval) served {
	index := make(served)
	for _, r := range retrievals {
		for _, hit := range r.Hits {
			index[hit.Locator] = struct{}{}
		}
		if doc := r.Document; doc != nil {
			index[event.Locator{Path: doc.Source.URL, Digest: string(doc.Digest)}] = struct{}{}
		}
	}
	return index
}

// verify refuses the first citation in lists that names a locator the run
// was not served. kind and ref name the item for the refusal; the locator's
// path is deliberately absent from it, because a failure is recorded in a
// receipt and §9 keeps source paths out of plaintext.
func (s served) verify(kind, ref string, lists ...[]frontier.Evidence) error {
	for _, list := range lists {
		for _, ev := range list {
			loc := ev.Locator()
			if _, ok := s[loc]; ok {
				continue
			}
			return fmt.Errorf("%w: %s %q cites line %d, byte offset %d, digest %.12s…, which no retrieval served",
				ErrUnservedEvidence, kind, ref, loc.Line, loc.ByteOffset, loc.Digest)
		}
	}
	return nil
}

// servedObservation reports whether a frontier search in this run served the
// observation named by id, which makes it a record the synthesizer may
// consolidate on the same terms as one its brief listed: it was disclosed to
// the worker by Babel rather than guessed at.
func servedObservation(retrievals []Retrieval, id string) bool {
	for _, r := range retrievals {
		for _, hit := range r.FrontierHits {
			if hit.Kind == frontier.OutputObservation && hit.ID == id {
				return true
			}
		}
	}
	return false
}

// verifyCitations checks every citation a result carries against what the
// run has served so far. It is the provenance check run at submission time,
// so the model reads the refusal while it can still correct the citation;
// persist runs the same check again over the same trace before anything
// becomes durable, because a submission is not a record and the trace is
// the run's, not the turn's.
//
// It reads the trace and nothing else: no store, no ledger, no authority
// table. What a stage may not emit is decided in persist, where a stray
// observation is dropped with a warning rather than costing the candidate
// beside it (#179), and a check here that refused the whole submission for
// it would undo that.
func verifyCitations(index served, res *Result) error {
	for _, cand := range res.Candidates {
		for _, obs := range cand.Observations {
			if err := index.verify("observation", obs.Ref, obs.Claim.Evidence, obs.Claim.CounterEvidence); err != nil {
				return err
			}
		}
		if rem := cand.Remedy; rem != nil {
			if err := index.verify("remedy", rem.Ref, rem.Proposal.Supporting, rem.Proposal.Conflicting); err != nil {
				return err
			}
		}
	}
	for _, obj := range res.Objections {
		if err := index.verify("objection", obj.Ref, obj.Claim.Evidence, obj.Claim.CounterEvidence); err != nil {
			return err
		}
	}
	for _, con := range res.Consolidations {
		if err := index.verify("consolidation", con.Ref, con.Finding.CounterEvidence); err != nil {
			return err
		}
		if con.Proposal != nil {
			if err := index.verify("proposal", con.Ref+"/proposal", con.Proposal.Supporting, con.Proposal.Conflicting); err != nil {
				return err
			}
		}
	}
	return nil
}
