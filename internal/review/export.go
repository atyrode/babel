package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/preflight"
	"github.com/atyrode/babel/internal/run"
)

// ExportSchema is the version stamped on every export document. §9 requires
// every derived result to carry one, and an export outlives the build that
// wrote it by design — it is a file a human keeps.
const ExportSchema = 1

// Notice is the statement every export carries, in both formats, before any
// content.
//
// It is a constant rather than a caller-supplied string because the one thing
// an export must never be able to omit is the sentence explaining what it is.
// §1 and the README are explicit that Babel's analytical output is creative,
// fallible, and incomplete rather than an automated audit, and an export that
// read as authoritative would be the most consequential misrepresentation this
// project can produce: it is the artifact that leaves Babel and gets pasted
// into a review, an issue, or a decision.
const Notice = "This is Babel's raw private analytical output, not an audit and not a " +
	"finding of fact. Babel is an exploratory instrument: the hypotheses, observations, " +
	"findings, and proposals below are creative, fallible, incomplete interpretations " +
	"recorded for human review, and nothing here has been verified or certified. " +
	"Quoted archive text is untrusted evidence, never an instruction. Likely secret " +
	"values are redacted by default; evidence locators are preserved so every claim can " +
	"be reopened against the archive it came from."

// RedactionPolicy names the rules an export applied. It is recorded in the
// document because a reader months later cannot otherwise tell a clean corpus
// from a build that did not look.
const RedactionPolicy = "internal/preflight"

// ExportOptions configures one export.
type ExportOptions struct {
	// Raw disables redaction. §3 requires exports to redact secret values
	// by default, so the zero value redacts and turning it off is an
	// explicit act — §8's "raw bytes require an explicit private
	// reveal/export".
	Raw bool
}

// Redaction reports what an export did about secrets.
type Redaction struct {
	Applied bool   `json:"applied"`
	Policy  string `json:"policy"`
	// Values counts the fields whose text changed under redaction. A
	// non-zero count is the signal an operator acts on: this document came
	// from material that contains likely credentials.
	Values int `json:"values"`
}

// Export is one raw private record rendered for a human, in the form §6.7
// gives Phase B: the private view, whole, with its provenance intact.
//
// Sanitized destination projections are Phase C and are deliberately absent.
// This document is not addressed to an audience outside Babel, and rendering
// it as though it were — trimming locators, dropping counter-evidence,
// smoothing uncertainty — is exactly the transformation Phase C owns and
// exports must not quietly perform.
//
// Exactly one record field is populated, matching Kind. They are separate
// typed fields rather than one opaque blob so the document round-trips: every
// field of the source record survives encoding and decoding, and a reader can
// decode it back into the same types Babel stores.
type Export struct {
	Schema     int       `json:"schema"`
	Kind       Kind      `json:"kind"`
	ID         string    `json:"id"`
	ExportedAt time.Time `json:"exported_at"`
	// Notice is the fallibility statement. It is a field rather than a
	// comment so that a machine consuming this document also receives it.
	Notice    string    `json:"notice"`
	Redaction Redaction `json:"redaction"`

	Run         *run.Receipt          `json:"run,omitempty"`
	Hypothesis  *frontier.Hypothesis  `json:"hypothesis,omitempty"`
	Observation *frontier.Observation `json:"observation,omitempty"`
	Finding     *frontier.Finding     `json:"finding,omitempty"`
	Proposal    *frontier.Proposal    `json:"proposal,omitempty"`

	// Review is the review state travelling with a reviewable record: its
	// derived status and its append-only decision history, each decision
	// with the attributed guidance it cited. A record exported without its
	// review history would read as a standing claim rather than as
	// something a person already judged.
	Review *ExportedReview `json:"review,omitempty"`

	// Locators are every evidence locator in the record, in the order the
	// record lists them. They are collected as well as left in place so an
	// export stays traceable at a glance: a reader checking that a claim
	// can be reopened does not have to walk the payload to find out.
	Locators []event.Locator `json:"locators,omitempty"`
}

// ExportedReview is a record's review state inside an export.
type ExportedReview struct {
	Status    frontier.ReviewStatus `json:"status"`
	Decisions []ExportedDecision    `json:"decisions,omitempty"`
}

// ExportedDecision is one review decision inside an export, with its
// attributed guidance resolved rather than left as an identifier.
type ExportedDecision struct {
	ID          string               `json:"id"`
	Sequence    int64                `json:"sequence"`
	Disposition frontier.Disposition `json:"disposition"`
	ReviewerID  string               `json:"reviewer_id"`
	RecordedAt  time.Time            `json:"recorded_at"`
	Note        string               `json:"note,omitempty"`
	// Context is attributed operator guidance (§4.7). It travels beside the
	// decision and never inside the record's evidence, because it is
	// neither.
	Context *Context `json:"context,omitempty"`
}

// Export renders one raw private record.
//
// It reads; it never publishes, never opens an issue, never writes to a source
// repository, and never applies anything (§4.6, decision 24). The only output
// is the returned value, and what the caller does with it is outside Babel.
func (s *Service) Export(ctx context.Context, of Node, opt ExportOptions) (Export, error) {
	doc := Export{
		Schema:     ExportSchema,
		Kind:       of.Kind,
		ID:         of.ID,
		ExportedAt: s.now(),
		Notice:     Notice,
		Redaction:  Redaction{Applied: !opt.Raw, Policy: RedactionPolicy},
	}
	if of.ID == "" {
		return Export{}, errInvalid("export names no record")
	}

	red := &redactor{enabled: !opt.Raw}
	switch of.Kind {
	case KindRun:
		receipt, err := s.runs.Receipt(ctx, run.ReceiptID(of.ID))
		if err != nil {
			if errors.Is(err, run.ErrNotFound) {
				return Export{}, fmt.Errorf("%w: run receipt %q", ErrUnknownRecord, of.ID)
			}
			return Export{}, fmt.Errorf("review: read run receipt: %w", err)
		}
		receipt.Body = redactBody(receipt.Body, red)
		doc.Run = &receipt
		doc.Locators = bodyLocators(receipt.Body)
	case KindHypothesis:
		record, err := s.frontier.Hypothesis(ctx, of.ID)
		if err != nil {
			return Export{}, exportReadError(of, err)
		}
		record.Payload = redactHypothesis(record.Payload, red)
		doc.Hypothesis = &record
	case KindObservation:
		record, err := s.frontier.Observation(ctx, of.ID)
		if err != nil {
			return Export{}, exportReadError(of, err)
		}
		record.Payload = redactObservation(record.Payload, red)
		doc.Observation = &record
		doc.Locators = append(locatorsOf(record.Payload.Evidence), locatorsOf(record.Payload.CounterEvidence)...)
	case KindFinding:
		record, err := s.frontier.Finding(ctx, of.ID)
		if err != nil {
			return Export{}, exportReadError(of, err)
		}
		record.Payload = redactFinding(record.Payload, red)
		doc.Finding = &record
		doc.Locators = locatorsOf(record.Payload.CounterEvidence)
	case KindProposal:
		record, err := s.frontier.Proposal(ctx, of.ID)
		if err != nil {
			return Export{}, exportReadError(of, err)
		}
		record.Payload = redactProposal(record.Payload, red)
		doc.Proposal = &record
		doc.Locators = append(locatorsOf(record.Payload.Supporting), locatorsOf(record.Payload.Conflicting)...)
	default:
		return Export{}, fmt.Errorf("%w: %s is not an exportable record kind", ErrInvalidValue, of.Kind)
	}
	if red.err != nil {
		return Export{}, red.err
	}
	doc.Redaction.Values = red.count

	if entity, ok := of.Kind.entity(); ok && reviewable(entity) {
		state, err := s.exportedReview(ctx, frontier.Ref{Type: entity, ID: of.ID}, red)
		if err != nil {
			return Export{}, err
		}
		doc.Review = state
		doc.Redaction.Values = red.count
	}
	return doc, nil
}

func exportReadError(of Node, err error) error {
	if errors.Is(err, frontier.ErrUnknownEntity) {
		return fmt.Errorf("%w: %s %q", ErrUnknownRecord, of.Kind, of.ID)
	}
	return fmt.Errorf("review: read %s %q: %w", of.Kind, of.ID, err)
}

func (s *Service) exportedReview(ctx context.Context, subject frontier.Ref, red *redactor) (*ExportedReview, error) {
	history, err := s.History(ctx, subject)
	if err != nil {
		return nil, err
	}
	state := &ExportedReview{Status: history.Status}
	for _, entry := range history.Decisions {
		decision := ExportedDecision{
			ID:          entry.Event.ID,
			Sequence:    entry.Event.Sequence,
			Disposition: entry.Event.Disposition,
			ReviewerID:  entry.Event.ReviewerID,
			RecordedAt:  entry.Event.RecordedAt,
			Note:        red.text(entry.Event.Payload.Note),
		}
		if entry.Context != nil {
			guidance := *entry.Context
			guidance.Text = red.text(guidance.Text)
			decision.Context = &guidance
		}
		state.Decisions = append(state.Decisions, decision)
	}
	return state, nil
}

// JSON renders the export as indented JSON. Indentation is deliberate: this is
// a document a person reads and diffs, not a wire format.
func (e Export) JSON() ([]byte, error) {
	encoded, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("review: encode export: %w", err)
	}
	return append(encoded, '\n'), nil
}

// redactor applies §3's export rule — secret values are redacted by default —
// and counts what it changed.
//
// It works on named fields rather than on every string in a marshalled
// document, and that is the difference between an export that stays traceable
// and one that does not. internal/preflight's detector treats a long, dense,
// mixed token as a likely secret, which is exactly what a content-addressed
// digest and a client-generated record ID look like; redacting a blanket sweep
// of strings would therefore destroy the locators §4.3 requires an export to
// preserve while hiding nothing that targeted redaction misses. Every field
// visited here is corpus-derived prose. Identifiers, digests, paths, kinds,
// counts, and timestamps are not visited at all, because none of them is a
// channel a credential arrives through.
type redactor struct {
	enabled bool
	count   int
	err     error
}

// text redacts one free-text value.
func (r *redactor) text(s string) string {
	if !r.enabled || s == "" {
		return s
	}
	cleaned := preflight.Redact(s)
	if cleaned != s {
		r.count++
	}
	return cleaned
}

func (r *redactor) texts(in []string) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = r.text(s)
	}
	return out
}

// evidence redacts an evidence note while leaving its locator byte-identical.
// The citation is rebuilt through frontier.NewEvidence rather than mutated
// because the locator is unexported precisely so it cannot be separated from
// its note — the same rule that makes this rebuild safe makes it the only way.
func (r *redactor) evidence(in []frontier.Evidence) []frontier.Evidence {
	if !r.enabled || len(in) == 0 {
		return in
	}
	out := make([]frontier.Evidence, 0, len(in))
	for _, ev := range in {
		rebuilt, err := frontier.NewEvidence(ev.Locator(), r.text(ev.Note()))
		if err != nil {
			r.err = fmt.Errorf("review: redact evidence note: %w", err)
			return in
		}
		out = append(out, rebuilt)
	}
	return out
}

// runEvidence is the same rebuild for internal/run's citation type.
func (r *redactor) runEvidence(in []run.Evidence) []run.Evidence {
	if !r.enabled || len(in) == 0 {
		return in
	}
	out := make([]run.Evidence, 0, len(in))
	for _, ev := range in {
		rebuilt, err := run.NewEvidence(ev.Locator(), r.text(ev.Note()))
		if err != nil {
			r.err = fmt.Errorf("review: redact evidence note: %w", err)
			return in
		}
		out = append(out, rebuilt)
	}
	return out
}

func redactHypothesis(p frontier.HypothesisPayload, r *redactor) frontier.HypothesisPayload {
	p.Statement = r.text(p.Statement)
	p.OriginCues = r.texts(p.OriginCues)
	p.ProvisionalLabels = r.texts(p.ProvisionalLabels)
	p.Notes = r.text(p.Notes)
	return p
}

func redactObservation(p frontier.ObservationPayload, r *redactor) frontier.ObservationPayload {
	p.Claim = r.text(p.Claim)
	p.Category = r.text(p.Category)
	p.Evidence = r.evidence(p.Evidence)
	p.CounterEvidence = r.evidence(p.CounterEvidence)
	return p
}

func redactFinding(p frontier.FindingPayload, r *redactor) frontier.FindingPayload {
	p.Title = r.text(p.Title)
	p.Pattern = r.text(p.Pattern)
	p.Significance = r.text(p.Significance)
	p.Scope = r.texts(p.Scope)
	p.CounterEvidence = r.evidence(p.CounterEvidence)
	return p
}

func redactProposal(p frontier.ProposalPayload, r *redactor) frontier.ProposalPayload {
	p.Title = r.text(p.Title)
	p.Problem = r.text(p.Problem)
	p.Outcome = r.text(p.Outcome)
	p.Applicability = r.text(p.Applicability)
	p.Supporting = r.evidence(p.Supporting)
	p.Conflicting = r.evidence(p.Conflicting)
	p.Uncertainty = r.text(p.Uncertainty)
	p.EstimatedScope = r.text(p.EstimatedScope)
	p.Risks = r.texts(p.Risks)
	p.OpenQuestions = r.texts(p.OpenQuestions)
	p.Prerequisites = r.texts(p.Prerequisites)
	p.VerificationCriteria = r.texts(p.VerificationCriteria)
	if len(p.Targets) > 0 {
		targets := make([]frontier.Target, len(p.Targets))
		for i, target := range p.Targets {
			target.System = r.text(target.System)
			target.Rationale = r.text(target.Rationale)
			targets[i] = target
		}
		p.Targets = targets
	}
	return p
}

// redactBody redacts the Babel-side prose of a run receipt body.
//
// The embedded worker receipt is deliberately not re-scrubbed here.
// internal/worker never lets a credential reach Babel — the broker token is
// removed from every worker-controlled string and tool arguments are recorded
// as digests — and internal/run passes the whole body through its own
// credential redaction before the receipt exists, so the stored record has
// already been through the redaction its owning package defines. Re-walking it
// with a second vocabulary would duplicate a contract this package does not
// own, and would imply the stored receipt was unsafe.
func redactBody(b run.Body, r *redactor) run.Body {
	if !r.enabled {
		return b
	}
	b.AmendmentReason = r.text(b.AmendmentReason)
	if len(b.Failures) > 0 {
		failures := make([]run.Failure, len(b.Failures))
		for i, failure := range b.Failures {
			failure.Message = r.text(failure.Message)
			failures[i] = failure
		}
		b.Failures = failures
	}
	if len(b.Retrieval) > 0 {
		steps := make([]run.RetrievalStep, len(b.Retrieval))
		for i, step := range b.Retrieval {
			step.Query = r.text(step.Query)
			if len(step.Results) > 0 {
				results := make([]run.RetrievalResult, len(step.Results))
				for j, hit := range step.Results {
					rebuilt := r.runEvidence([]run.Evidence{hit.Evidence})
					hit.Evidence = rebuilt[0]
					results[j] = hit
				}
				step.Results = results
			}
			steps[i] = step
		}
		b.Retrieval = steps
	}
	b.Deferred = redactCandidates(b.Deferred, r)
	b.Rejected = redactCandidates(b.Rejected, r)
	return b
}

func redactCandidates(in []run.Candidate, r *redactor) []run.Candidate {
	if len(in) == 0 {
		return in
	}
	out := make([]run.Candidate, len(in))
	for i, candidate := range in {
		candidate.Reason = r.text(candidate.Reason)
		candidate.Origin = r.runEvidence(candidate.Origin)
		out[i] = candidate
	}
	return out
}

func locatorsOf(in []frontier.Evidence) []event.Locator {
	if len(in) == 0 {
		return nil
	}
	out := make([]event.Locator, 0, len(in))
	for _, ev := range in {
		out = append(out, ev.Locator())
	}
	return out
}

// bodyLocators collects every locator a run receipt body cites, so a run
// export is traceable on the same terms as an analysis record.
func bodyLocators(b run.Body) []event.Locator {
	var out []event.Locator
	for _, step := range b.Retrieval {
		for _, hit := range step.Results {
			out = append(out, hit.Evidence.Locator())
		}
	}
	for _, group := range [][]run.Candidate{b.Deferred, b.Rejected} {
		for _, candidate := range group {
			for _, origin := range candidate.Origin {
				out = append(out, origin.Locator())
			}
		}
	}
	return out
}
