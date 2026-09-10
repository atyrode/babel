package explore

import (
	"context"
	"errors"
	"fmt"

	"github.com/atyrode/babel/internal/reality"
)

// This file is the one path from an analysis run into the Reality Ledger, and
// it is deliberately the narrowest one that is useful.
//
// §4.8 keeps a run out of the ledger's fact tables entirely: analysis produces
// observations and proposed revisions, never authority. A Question is the
// exception the same section describes, and it is an exception only because
// of what a question is — a request that somebody else authorize something.
// Accepting one from a model costs the ledger no authority at all, because
// nothing it says becomes true by being asked. That asymmetry is the whole
// licence, and it is why this seam exposes Ask and a resolver and nothing
// else: there is no reachable path from a run to a fact, an answer, or a plan.

// FailureQuestion names a question Babel refused to raise. It is a per-item
// refusal in persist's sense: the candidate beside it is durable regardless,
// and the run's other questions are unaffected.
const FailureQuestion = "question"

// QuestionLedger is the inbox half of internal/reality, as a run may use it.
type QuestionLedger interface {
	// ResolveSubject names the entity an untyped term refers to.
	ResolveSubject(ctx context.Context, value string) (string, error)
	// Ask records a Question, deduplicating and suppressing per §4.8.
	Ask(ctx context.Context, in reality.QuestionInput) (reality.Question, error)
}

// ask raises the questions one stage's result carried.
//
// It runs last in persist, after every record the questions may point at is
// durable: a question naming the hypothesis it blocks is worth more than the
// question alone, and the reference only exists once the candidate has an
// identifier. A cancelled run has already returned before this, on the same
// reasoning that stops criticism and consolidation there — an unasked question
// is a question the resumed run asks, while a half-written frontier is not
// something a resume can repair.
func (c *Controller) ask(st *state, stage Stage, res *Result) {
	if len(res.Questions) == 0 {
		return
	}
	// Every refusal below is a warning rather than a failure, and the
	// asymmetry with the rest of persist is deliberate. A question is the
	// one output whose refusal is routine: the ledger holds whatever
	// entities the operator has declared and no more, so a run meeting a
	// machine nobody has named yet will be refused, correctly, on most
	// machines for a long time. Failing the pass for it would make a run
	// that asked a good question worth less than one that asked nothing,
	// and the candidates, observations and findings beside it — the work
	// the run actually exists to produce — are all durable already.
	if c.cfg.Questions == nil {
		st.warn(stage, FailureQuestion, c.now(), fmt.Errorf(
			"explore: this build has no Reality Ledger open, so %d question(s) were dropped",
			len(res.Questions)))
		return
	}
	for _, draft := range res.Questions {
		in, err := c.question(st, draft)
		if err != nil {
			st.warn(stage, FailureQuestion, c.now(), err)
			continue
		}
		record, err := c.cfg.Questions.Ask(st.ctx, in)
		if err != nil {
			// A duplicate is not a defect: §4.8 refuses a question the
			// operator already holds, and a run that met the same gap
			// twice did nothing wrong by asking.
			st.warn(stage, FailureQuestion, c.now(),
				fmt.Errorf("explore: ask question %q: %w", draft.Ref, err))
			continue
		}
		st.out.Questions = append(st.out.Questions, record.ID)
	}
}

// question turns one draft into the ledger's input, resolving everything the
// run named by hand.
//
// Every name is resolved rather than trusted. A run does not mint identity:
// SPEC §4.8 puts entity creation behind an attributed operator act, so a
// subject the ledger has never heard of is a refused question and not a new
// row. The refusal is the useful outcome — it says the run met something the
// operator's world model does not contain, which is a gap in the ledger rather
// than in the run.
func (c *Controller) question(st *state, draft QuestionDraft) (reality.QuestionInput, error) {
	if len(draft.Subjects) == 0 {
		return reality.QuestionInput{}, fmt.Errorf(
			"explore: question %q names no subject, so nothing in the ledger can answer it", draft.Ref)
	}
	targets := make([]string, 0, len(draft.Subjects))
	for _, subject := range draft.Subjects {
		id, err := c.cfg.Questions.ResolveSubject(st.ctx, subject)
		if err != nil {
			if errors.Is(err, reality.ErrUnknownRecord) {
				return reality.QuestionInput{}, fmt.Errorf(
					"explore: question %q names a subject the ledger does not hold: %w", draft.Ref, err)
			}
			return reality.QuestionInput{}, fmt.Errorf("explore: question %q subject: %w", draft.Ref, err)
		}
		targets = append(targets, id)
	}
	predicates := make([]reality.Predicate, 0, len(draft.Predicates))
	for _, p := range draft.Predicates {
		predicates = append(predicates, reality.Predicate(p))
	}

	in := reality.QuestionInput{
		// A run's question is always the same kind: it is missing context
		// it could not recover from the corpus. The other six kinds are
		// the ledger's own — a stale fact, a conflict between two sources,
		// an ambiguous alias — and each is raised by the machinery that
		// can see the condition. A run cannot see any of them.
		Kind: reality.KindAcquireContext,
		// Nothing a run asks is more than routine by default. Sensitivity
		// grades the answer's exposure, and a model that graded its own
		// question restricted would be classifying material it has not
		// received yet.
		Sensitivity: reality.SensitivityRoutine,
		// Only the operator can answer. A trusted source's batch answers
		// no question — it asserts facts, which may incidentally settle
		// one — and no model may authorize an answer at all (§4.8).
		ExpectedAuthority: reality.AuthorityOperator,
		TargetEntityIDs:   targets,
		TargetPredicates:  predicates,
		Payload: reality.QuestionPayload{
			Prompt:   draft.Prompt,
			WhyAsked: draft.WhyAsked,
		},
	}

	// Class is derived from the work, never declared. §4.8 ranks a question
	// that holds up real work above one that satisfies curiosity, and a
	// self-graded class would make the ranking a matter of how urgent the
	// model felt: every question would be blocking within a week.
	in.Class = reality.ClassCuriosity
	if draft.Hypothesis != "" {
		id, ok := st.hypotheses[draft.Hypothesis]
		if !ok {
			id = draft.Hypothesis
		}
		in.DependentWork = []reality.WorkRef{{Kind: reality.WorkHypothesis, ID: id, Blocking: true}}
		in.Class = reality.ClassBlocking
		// The blocked record is also the suppression key. A question the
		// operator declined stays declined until materially new evidence
		// arrives, and the next run meeting the same gap on the same
		// record has none: it is the same question, and re-asking it
		// would make a decline mean "ask again tomorrow".
		in.MaterialEvidence = []string{id}
	}
	return in, nil
}
