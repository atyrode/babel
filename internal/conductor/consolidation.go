package conductor

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/run"
)

// consolidationPrefix is what a consolidation cycle's authority reference
// begins with, so a receipt's why reads as consolidation without a lookup
// table and the day's receipts can be searched for the cycles that were
// spent turning the frontier into findings rather than growing it.
const consolidationPrefix = "consolidation:"

// DefaultConsolidationRoots bounds how many candidates one consolidation cycle
// seeds itself from when no bound was configured.
//
// Five, because a consolidation cycle is a challenge-and-synthesis pass over
// candidates that already exist: the cost is in the passes rather than in the
// discovery, and a cycle that seeded itself from fifty roots would spend one
// ceiling on a superficial look at all of them instead of a real one at a few.
const DefaultConsolidationRoots = 5

// consolidationWindow is how much of the frontier's head one draw reads before
// grouping it.
//
// It is larger than the root bound because the draw has to find candidates
// that share a corpus, and the frontier's head is ordered by priority rather
// than by where the candidates came from. Reading forty to fill five is what
// makes a coherent group likely without turning a draw into a table scan of a
// frontier that holds thousands.
const consolidationWindow = 40

// Candidates is the unexplored frontier as the consolidation rung reads it.
// *frontier.Store satisfies it.
type Candidates interface {
	Unexplored(ctx context.Context, limit int) ([]frontier.Hypothesis, error)
}

// ConsolidationRung draws cycles that consolidate the frontier instead of
// growing it.
//
// It exists because a loop that only ever explores fresh sessions has one
// failure mode and it is silent: every cycle adds candidates, no cycle ever
// attacks them, and after a few hundred cycles the frontier holds thousands of
// deferred hypotheses and no findings. §5.2 is explicit that a deferred
// candidate is not a terminal state — it is work the frontier is holding for a
// later run — and this rung is that later run.
//
// It draws from the frontier's own ordering (priority, then age) and seeds a
// run from what it drew, so a consolidation cycle is an ordinary exploration
// with `--root`: the same preparation, the same recipes, the same receipt. The
// difference is only where the run starts, which is what keeps a cycle a cycle.
type ConsolidationRung struct {
	candidates Candidates
	origins    Origins
	focus      Focus
	maxRoots   int
}

// NewConsolidationRung builds the rung over the unexplored frontier, the
// resolver that says which sessions a candidate came out of, and the recorded
// expenditure policy the draw is bound by. A non-positive bound is
// DefaultConsolidationRoots, and a nil focus is a machine with no stated
// policy, which withholds nothing.
func NewConsolidationRung(candidates Candidates, origins Origins, focus Focus, maxRoots int) *ConsolidationRung {
	if maxRoots <= 0 {
		maxRoots = DefaultConsolidationRoots
	}
	return &ConsolidationRung{candidates: candidates, origins: origins, focus: focus, maxRoots: maxRoots}
}

// Name reports this rung's stable name.
func (r *ConsolidationRung) Name() string { return RungConsolidation }

// Depth reports how many candidates the frontier is still holding unexplored
// and could still be drawn from.
//
// The number is the backlog rather than the number of cycles it would take,
// because the backlog is the thing an operator is deciding about: a frontier of
// two is a loop that is keeping up, and one of two thousand is the state this
// rung exists for.
//
// It is the drawable backlog, and it has to be: a status view reporting five
// waiting beside a loop that draws nothing would be the kind of quiet
// disagreement this package is built to avoid. So the same policy Draw is
// bound by is applied here, and the note carries the two numbers behind the
// difference — how many candidates the recorded focus keeps a consolidation
// cycle off, and how many of those are subjects nothing at all may be spent
// on. The second is not the first: a subject at learn-only is one Babel may
// still mine for cross-cutting lessons, and reporting it as dead would hide
// the distinction the allowance exists to draw.
//
// Nothing is recorded here. A depth report spends nothing, so it has no
// deferral to justify.
func (r *ConsolidationRung) Depth(ctx context.Context) (Depth, error) {
	open, err := r.candidates.Unexplored(ctx, 0)
	if err != nil {
		return Depth{}, fmt.Errorf("conductor: read the unexplored frontier: %w", err)
	}
	pass := newFocusPass(r.focus, time.Time{})
	drawable, spendable := 0, 0
	for _, candidate := range open {
		anything, err := pass.permits(ctx, candidate, reality.WorkSynthesis)
		if err != nil {
			return Depth{}, err
		}
		if !anything.Permitted {
			continue
		}
		spendable++
		seeding, err := pass.permits(ctx, candidate, reality.WorkSubjectSpecific)
		if err != nil {
			return Depth{}, err
		}
		if seeding.Permitted {
			drawable++
		}
	}
	note := fmt.Sprintf("%d unexplored %s", len(open),
		plural(len(open), "candidate", "candidates"))
	if withheld := len(open) - drawable; withheld > 0 {
		note += fmt.Sprintf(", %d withheld by recorded focus (%d excluded from analysis entirely)",
			withheld, len(open)-spendable)
	}
	return Depth{Waiting: drawable, Implemented: true, Note: note}, nil
}

// Draw takes the frontier's head and seeds a cycle from the candidates around
// it that share a corpus.
//
// Grouping by corpus is the whole of the design here. A candidate's evidence
// names the sessions it came out of, and a cycle whose roots span unrelated
// corpora would prepare the union of all of them and hand a challenger a scope
// with no subject — several smaller cycles over coherent scopes are cheaper and
// say more. So the highest-priority candidate picks the corpus, and the rest of
// the window joins it only if it came from the same sessions.
//
// Nothing is consumed. A candidate stays unexplored until a run explores it,
// which is what makes an interrupted consolidation cycle resumable under its
// own run identity rather than a piece of work that has to be found again.
//
// Focus is consulted before a candidate can lead or join a cycle, because a
// consolidation cycle is subject-specific work: it seeds an ordinary
// exploration rooted at the candidate, with the same recipes, which is
// exactly the expenditure §4.8's deferral list is about. A candidate the
// policy withholds is passed over and left where it is — §4.8 and §5.2 both
// forbid removing it, so the frontier is byte-identical afterwards and the
// refusal lives in an immutable context snapshot beside the candidate it was
// taken about. Superseding the fact, or installing a rule set version that
// decides differently, makes the same candidate drawable on the next cycle
// with nothing to undo.
//
// The window is walked once and the walk stops as soon as the roots are full,
// so a draw that fills immediately records no refusals it never reached. A
// draw that reaches the end of the window recorded one per candidate it
// actually considered and could not use, which is the number worth having:
// it is the cycle that found nothing to consolidate, and the snapshots are
// the answer to why.
func (r *ConsolidationRung) Draw(ctx context.Context, d DrawRequest) (Assignment, error) {
	open, err := r.candidates.Unexplored(ctx, consolidationWindow)
	if err != nil {
		return Assignment{}, fmt.Errorf("conductor: read the unexplored frontier: %w", err)
	}
	if len(open) == 0 {
		return Assignment{}, ErrNoWork
	}

	pass := newFocusPass(r.focus, d.At)
	var (
		lead     frontier.Hypothesis
		scope    []string
		roots    []string
		withheld int
	)
	for _, candidate := range open {
		if len(roots) >= r.maxRoots {
			break
		}
		admission, err := pass.spend(ctx, candidate, reality.WorkSubjectSpecific,
			"a consolidation cycle would have been seeded from this candidate")
		if err != nil {
			return Assignment{}, fmt.Errorf("conductor: consult focus for %s: %w", candidate.ID, err)
		}
		if !admission.Permitted {
			withheld++
			continue
		}
		corpus, err := r.corpusOf(ctx, candidate)
		if err != nil {
			return Assignment{}, err
		}
		if len(roots) == 0 {
			lead, scope = candidate, corpus
			roots = append(roots, candidate.ID)
			continue
		}
		if !slices.Equal(corpus, scope) {
			continue
		}
		roots = append(roots, candidate.ID)
	}
	if len(roots) == 0 {
		return Assignment{}, ErrNoWork
	}

	note := fmt.Sprintf("consolidating %d unexplored %s over %s",
		len(roots), plural(len(roots), "candidate", "candidates"), corpusPhrase(scope))
	if withheld > 0 {
		note += fmt.Sprintf("; %d %s withheld by recorded focus",
			withheld, plural(withheld, "candidate", "candidates"))
	}
	return Assignment{
		Rung:      RungConsolidation,
		Authority: run.Authority{Kind: run.AuthorityPolicy, Ref: consolidationPrefix + lead.ID},
		Sessions:  scope,
		Roots:     roots,
		Note:      note,
	}, nil
}

// corpusOf reports the sessions a candidate came out of, in a stable order so
// two candidates from one run group together.
//
// A candidate whose originating run left no recoverable scope reports none, and
// that is a corpus too: those candidates group with each other and the cycle
// runs over the whole host, on the same terms rung one already accepts — a gap
// in Babel's own bookkeeping must not strand a candidate on the frontier
// forever.
func (r *ConsolidationRung) corpusOf(ctx context.Context, h frontier.Hypothesis) ([]string, error) {
	origin, err := r.origins.Origin(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: h.ID})
	if err != nil {
		return nil, fmt.Errorf("conductor: resolve the corpus behind %s: %w", h.ID, err)
	}
	sessions := slices.Clone(origin.Sessions)
	slices.Sort(sessions)
	return slices.Compact(sessions), nil
}

// corpusPhrase renders a cycle's scope for the note an operator reads.
func corpusPhrase(sessions []string) string {
	switch len(sessions) {
	case 0:
		return "this host's whole corpus"
	case 1:
		return sessions[0]
	default:
		return fmt.Sprintf("%s and %d other %s", sessions[0], len(sessions)-1,
			plural(len(sessions)-1, "session", "sessions"))
	}
}

// Consolidation is the share of cycles spent turning the frontier into
// findings, and the rung that draws them.
//
// It is a protected fraction rather than a ladder position, for the same
// reason the serendipity floor is one and the mirror image of it. Below the
// invitations it would be starved by a busy operator; above them it would
// outrank a person. What it actually competes with is the loop's appetite for
// fresh discovery, and the honest way to state that is a ratio the operator
// sets: one cycle in N consolidates, the rest of the ladder decides the others.
type Consolidation struct {
	// OneIn guarantees one consolidation cycle in every OneIn cycles. Zero is
	// off, and off is the default: a consolidation cycle runs the challenger
	// and the synthesizer, which are worker jobs the operator pays for, so a
	// loop that started consolidating because it was upgraded would be
	// spending against a ceiling that was set for something else.
	OneIn int
	// Rung draws the candidates. It is required whenever OneIn is set and
	// ignored when it is not.
	Rung Rung
}

// scheduled reports whether this build draws consolidation cycles at all.
func (c Consolidation) scheduled() bool { return c.OneIn > 0 && c.Rung != nil }

func (c Consolidation) validate() error {
	if c.OneIn < 0 {
		return fmt.Errorf("conductor: a consolidation fraction of one cycle in %d is not a fraction", c.OneIn)
	}
	if c.OneIn > 0 && c.Rung == nil {
		return fmt.Errorf("conductor: a consolidation fraction with no rung to draw from can consolidate nothing")
	}
	return nil
}

// due reports whether the protected consolidation fraction requires this cycle
// to consolidate.
//
// It counts the cycles since the last consolidation drawn, exactly as the
// serendipity floor counts its own, so the guarantee is a property of the
// journal rather than of one process's memory and survives a restart.
func (c Consolidation) due(history History) bool {
	if !c.scheduled() {
		return false
	}
	if c.OneIn <= 1 {
		return true
	}
	other := 0
	for _, cycle := range history.Reverse() {
		if !cycle.counts() {
			continue
		}
		if cycle.Rung == RungConsolidation {
			return false
		}
		other++
		if other >= c.OneIn-1 {
			return true
		}
	}
	return other >= c.OneIn-1
}
