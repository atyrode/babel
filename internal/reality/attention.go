package reality

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Work names one kind of expenditure a caller is about to make on a subject.
//
// It exists because an allowance is not a switch. §4.8 distinguishes reading
// the corpus from cloning a repository from emitting a proposal about this
// subject in particular, and an allowance that admits the first two while
// refusing the third can only be honoured by a caller that says which of them
// it is about to do. A gate that asked "is this subject allowed" and was
// handed a boolean would throw away the distinction the values exist to
// carry — and that distinction is the whole content of `learn-only`: a
// project nobody maintains is still worth learning from.
type Work string

// The kinds of expenditure this build distinguishes.
const (
	// WorkSynthesis re-reads material Babel already holds — the retrieval
	// index, the frontier, this ledger — and consolidates it. It is the
	// floor of analysis: anything Babel does about a subject at all rests
	// on it, so an allowance that refuses it refuses everything.
	WorkSynthesis Work = "synthesis"
	// WorkCorpusReading draws a subject's sessions into a new scope. It is
	// how a subject keeps contributing cross-cutting lessons while nothing
	// is spent on the subject itself.
	WorkCorpusReading Work = "corpus-reading"
	// WorkSubjectSpecific is §4.8's deferral list: cloning, test
	// execution, and proposals about this subject in particular.
	WorkSubjectSpecific Work = "subject-specific"
)

func (w Work) valid() bool {
	switch w {
	case WorkSynthesis, WorkCorpusReading, WorkSubjectSpecific:
		return true
	}
	return false
}

// Works lists the kinds in a stable order, for the same reason EntityKinds
// does: a caller has to name one, so the vocabulary is readable rather than
// private lore.
func Works() []Work {
	return []Work{WorkSynthesis, WorkCorpusReading, WorkSubjectSpecific}
}

// Permits reports whether this allowance admits a kind of work.
//
// The table is written out rather than derived from restriction(), even
// though the two agree today. restriction() ranks allowances so several
// entities' decisions can be combined into the most restrictive; this says
// what one of them lets a caller do. Deriving the second from the first would
// make a later allowance that restricts in a different dimension — one that
// permitted repository work but no corpus reading, say — unrepresentable, and
// the rank was never a promise that the permissions stay nested.
func (a Allowance) Permits(w Work) bool {
	switch a {
	case AllowanceFull:
		return w.valid()
	case AllowanceLearnOnly:
		// §4.8's deferral list is refused and nothing else is: reading
		// the corpus and consolidating what is already held stay open,
		// which is what makes a finished project still worth mining.
		return w == WorkSynthesis || w == WorkCorpusReading
	case AllowanceNoCodeInvestigation:
		// Synthesis over material already held, and no new
		// investigation at all — so this subject's sessions are not
		// drawn into a fresh scope either, which is the one thing that
		// separates it from learn-only.
		return w == WorkSynthesis
	case AllowanceExcluded:
		// Nothing. The records stay exactly where they are; §4.8 and
		// §5.2 forbid removing them, and this refuses expenditure, not
		// existence.
		return false
	}
	return false
}

// Attention is §4.8's expenditure policy as the rest of Babel consults it.
//
// The ledger has modelled what analysis may spend on a subject since focus
// rules were implemented, and until this type existed nothing outside this
// package read them: an operator could record that a project is finished and
// watch the loop go on generating hypotheses about it, because the decision
// was reachable only from a context snapshot nothing in the scheduler took.
// That gap is what this closes.
//
// It is deliberately a thin composition of what the store already does —
// typed alias resolution, versioned rule evaluation, and the immutable
// context snapshot — so that "what may be spent here" has one answer and one
// place it is recorded, rather than a second policy growing inside whichever
// caller asked first.
//
// Nothing here asserts a fact and nothing here removes a record. The only
// write it performs is the context snapshot §4.8 requires a deterministic
// deferral to leave behind, on a table the schema refuses updates and deletes
// on.
type Attention struct {
	store   *Store
	version int
}

// NewAttention binds a consultation to one stored policy version.
//
// The version is named rather than discovered, for FocusQuery's reason: a
// decision taken against "whatever the newest policy is" cannot be re-derived
// once a newer one is installed, and a deferral an operator cannot reproduce
// is not one they can argue with.
//
// A nil store is a machine whose ledger did not open, and it is accepted on
// purpose. Callers reach this through an interface, where a typed nil is not
// a nil interface; handling absence here rather than at every call site is
// what keeps an unopenable database from reading as an operator decision to
// withhold everything.
func NewAttention(store *Store, version int) *Attention {
	return &Attention{store: store, version: version}
}

// AdmitRequest asks whether one kind of work may be spent on the subject a
// caller is looking at.
type AdmitRequest struct {
	// Names are the terms the caller knows the subject by: a provisional
	// label off a candidate hypothesis, a workspace path off a session, a
	// repository remote. They are resolved through the ledger's typed
	// aliases rather than matched against anything, because §4.8 puts the
	// operator in charge of which spellings mean which entity — and a name
	// nothing answers to names no subject and withholds nothing.
	Names []string
	// Work is what the caller is about to spend. There is no default: a
	// caller that did not say what it was about to do cannot be told
	// whether it may.
	Work Work
	// HypothesisID is the candidate the expenditure would have developed,
	// when there is one. Setting it is what turns a refusal into a record:
	// §4.8 requires a deterministic deferral to freeze the context that
	// caused it, so a withheld candidate gets an immutable context
	// snapshot naming the policy version, the rule, the facts read, and
	// the instant. The candidate itself is untouched.
	HypothesisID string
	// Note is what the caller was about to do, stored with that snapshot
	// so an operator reading it back learns what did not happen.
	Note string
	// AsOf is the instant the ledger is read at, defaulting to now. One
	// instant serves the whole request, so the decision and the snapshot
	// recording it cannot disagree about which facts were current.
	AsOf time.Time
}

// Admission is one consultation's answer.
//
// It is not stored as a property of anything, for FocusDecision's reason: it
// is computed against a named policy version at a named instant and either
// acted on immediately or frozen into a snapshot. What survives it is the
// snapshot, never a cached verdict a later read could disagree with.
type Admission struct {
	Work      Work
	Permitted bool
	// Allowance is the combined decision: the most restrictive of the
	// subjects', because the most permissive would let one unrelated
	// entity unlock work on an excluded one.
	Allowance Allowance
	// Policy is the rule set version that decided. Zero means no policy
	// version is installed, which is a different statement from version
	// zero deciding and is why it is reported rather than assumed.
	Policy int
	// Subjects are the canonical entities the names resolved to, sorted.
	Subjects []string
	// Unresolved and Ambiguous count the names that named no entity and
	// the names that named several. They are counts and not the names
	// themselves because an alias value is a path or an operator's own
	// vocabulary, which §9 keeps out of anything that gets logged — the
	// same reason ResolveAlias leaves the value out of its errors.
	//
	// An ambiguous name decides nothing. §4.8 makes alias resolution a
	// Question precisely because two entities can answer to one term, and
	// a gate that picked one would either withhold work on the wrong
	// subject or bury the ambiguity.
	Unresolved int
	Ambiguous  int
	// Decisions are the per-subject evaluations, in Subjects order.
	Decisions []FocusDecision
	// Deciding is the one that set Allowance.
	Deciding FocusDecision
	// Cause are the facts the deciding rule actually matched on, so a
	// refusal can name them without a second query. Empty when no rule
	// matched and the rule set's default applied.
	Cause []FocusInput
	// Contested reports that a fact behind one of these decisions is stale
	// or disputed, which §4.8 gives the challenger to check.
	Contested bool
	// SnapshotID names the immutable context snapshot this refusal was
	// recorded as. Empty when the work was permitted, or when no candidate
	// was named to record it against.
	SnapshotID string
	AsOf       time.Time
}

// Reason renders the admission as one sentence an operator can read, naming
// the entity, the rule that decided, and the facts it matched.
//
// "Why was this skipped" has to have an answer that does not require a
// second query, because the place the question gets asked — a cycle note, a
// status line, a line of `babel prepare` output — is not a place a ledger
// read is available.
func (a Admission) Reason() string { return a.render(true) }

// prose renders the same sentence without any identifier in it.
//
// It is what reaches the ledger. A snapshot already stores the entity, the
// rule and the fact identifiers in structured columns and in its decisions,
// so a note repeating them adds nothing — and §4.8's credential detector
// runs over prose and legitimately flags a random hex identifier, so a note
// that carried one would refuse the very write that records the refusal.
func (a Admission) prose() string { return a.render(false) }

func (a Admission) render(withIDs bool) string {
	verb := "withheld"
	if a.Permitted {
		verb = "allowed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s work is %s", a.Work, verb)
	switch {
	case a.Policy == 0:
		b.WriteString(": no focus policy version is installed, so nothing is withheld")
		return b.String()
	case len(a.Subjects) == 0:
		fmt.Fprintf(&b, ": no entity in the ledger answers to the %d %s given",
			a.Unresolved+a.Ambiguous, namesWord(a.Unresolved+a.Ambiguous))
		return b.String()
	}
	if withIDs {
		fmt.Fprintf(&b, " on %s", a.Deciding.EntityID)
	}
	fmt.Fprintf(&b, ": focus rule set %d decides %s", a.Policy, a.Allowance)
	if a.Deciding.RuleName != "" {
		fmt.Fprintf(&b, " by rule %q", a.Deciding.RuleName)
	}
	fmt.Fprintf(&b, ", because %s", a.Deciding.Because)
	for i, input := range a.Cause {
		if i == 0 {
			b.WriteString("; from ")
		} else {
			b.WriteString(" and ")
		}
		fmt.Fprintf(&b, "%s=%s", input.Predicate, input.Value)
		if withIDs {
			fmt.Fprintf(&b, " (fact %s)", input.FactID)
		}
	}
	if a.Contested {
		b.WriteString("; a fact behind it is stale or disputed")
	}
	if withIDs && a.SnapshotID != "" {
		fmt.Fprintf(&b, "; recorded as %s", a.SnapshotID)
	}
	return b.String()
}

func namesWord(n int) string {
	if n == 1 {
		return "name"
	}
	return "names"
}

// Admit decides whether the work may be spent, and records the refusal when
// it may not.
//
// The order is the one §4.8 sets out and it matters: names are resolved to
// entities through the merge history, each entity is evaluated against the
// named policy version at one instant, the decisions are combined into the
// most restrictive, and only then is the answer compared against what the
// caller is about to do. Nothing shortcuts from a fact value to an
// expenditure policy — the analysis-policy predicate reaches the answer only
// by matching a rule in the stored version, which is what lets a later
// version decide differently about an unchanged ledger.
func (a *Attention) Admit(ctx context.Context, in AdmitRequest) (Admission, error) {
	if !in.Work.valid() {
		return Admission{}, fmt.Errorf("%w: work kind %q", ErrInvalidValue, in.Work)
	}
	out := Admission{Work: in.Work, Permitted: true, Allowance: AllowanceFull, AsOf: in.AsOf}
	if a == nil || a.store == nil {
		return out, nil
	}
	out.AsOf = a.store.asOfOr(in.AsOf)

	rules, err := a.store.FocusRules(ctx, a.version)
	if errors.Is(err, ErrUnknownRecord) {
		// No policy of that version has been installed, so the operator
		// has stated nothing and nothing is withheld. Policy stays zero
		// to say exactly that rather than implying a version decided.
		return out, nil
	}
	if err != nil {
		return Admission{}, err
	}
	out.Policy = rules.Version

	for _, name := range sortedUnique(in.Names) {
		id, err := a.store.ResolveSubject(ctx, name)
		switch {
		case errors.Is(err, ErrUnknownRecord):
			out.Unresolved++
			continue
		case errors.Is(err, ErrAmbiguousAlias):
			out.Ambiguous++
			continue
		case err != nil:
			return Admission{}, err
		}
		if !slices.Contains(out.Subjects, id) {
			out.Subjects = append(out.Subjects, id)
		}
	}
	slices.Sort(out.Subjects)

	for _, id := range out.Subjects {
		decision, err := a.store.EvaluateFocus(ctx, FocusQuery{
			EntityID:       id,
			RuleSetVersion: a.version,
			AsOf:           out.AsOf,
		})
		if err != nil {
			return Admission{}, err
		}
		out.Decisions = append(out.Decisions, decision)
		out.Contested = out.Contested || decision.Contested
		if out.Deciding.EntityID == "" || decision.Allowance.MoreRestrictiveThan(out.Allowance) {
			out.Allowance, out.Deciding = decision.Allowance, decision
		}
	}
	out.Permitted = out.Allowance.Permits(in.Work)
	out.Cause = matchedInputs(rules, out.Deciding)
	if out.Permitted || in.HypothesisID == "" {
		return out, nil
	}

	// The candidate is not touched. §4.8 has the deferral record the
	// context that caused it and §5.2 keeps the candidate on the frontier,
	// so what a refusal writes is a snapshot beside the record, never a
	// change to it — which is also what makes lifting the policy enough to
	// make the same candidate drawable again, with nothing to undo.
	snapshot, err := a.store.CaptureSnapshot(ctx, SnapshotInput{
		HypothesisID:   in.HypothesisID,
		EntityIDs:      out.Subjects,
		RuleSetVersion: a.version,
		AsOf:           out.AsOf,
		Note:           withheldNote(in.Note, out),
	})
	if err != nil {
		return Admission{}, err
	}
	out.SnapshotID = snapshot.ID
	return out, nil
}

// withheldNote is what the snapshot carries: what the caller was about to
// do, then why it did not happen, in prose only — the identifiers behind it
// are already the snapshot's own columns.
func withheldNote(note string, out Admission) string {
	if note == "" {
		return out.prose()
	}
	return note + "; " + out.prose()
}

// matchedInputs reports the facts the rule that decided actually matched on.
//
// It reads the conditions off the stored rule rather than off the decision
// because a FocusDecision records every current fact it read, and a refusal
// that listed all of them would bury the one the operator recorded on
// purpose. A decision that fell through to the default matched no conditions
// and names no cause, which is honest: nothing in the ledger moved it.
func matchedInputs(rules FocusRuleSet, decision FocusDecision) []FocusInput {
	if decision.RuleName == "" {
		return nil
	}
	var matched FocusRule
	for _, rule := range rules.Rules {
		if rule.Name == decision.RuleName {
			matched = rule
			break
		}
	}
	out := make([]FocusInput, 0, len(matched.When))
	for _, cond := range matched.When {
		for _, input := range decision.Inputs {
			if input.Predicate == cond.Predicate {
				out = append(out, input)
				break
			}
		}
	}
	return out
}
