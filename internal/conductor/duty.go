package conductor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/run"
)

// The standing duties this build knows (#88, #94). They are named constants
// because a duty's name reaches the receipt as its authority reference: a
// renamed duty would orphan the history that says why those runs happened.
const (
	// DutyImprovesBabel is #88's product dimension: Babel evaluating its own
	// output quality, acceptance and improvement process, with findings
	// phrased for anyone rather than for this operator.
	DutyImprovesBabel = "babel-improves-babel"
	// DutyMechanizationAudit is #94's axis: where inference substituted for
	// retrieval, and what code would have served the context instead. It is
	// authorized by the same toggle as DutyImprovesBabel because #94 places it
	// in that dimension — its output is proposals against the public codebase.
	DutyMechanizationAudit = "mechanization-audit"
	// DutyTunesItself is #88's personal dimension, carrying #87's item 6:
	// operator-specific relevance and memory amendments, including revisiting
	// recorded facts against fresh archive state.
	DutyTunesItself = "babel-tunes-itself"
)

// dutyPrefix is what a duty's authority reference begins with, so a receipt's
// why reads as a duty without a lookup table and the journal can be searched
// for one duty's history.
const dutyPrefix = "duty:"

// DutyRef renders a duty's authority reference.
func DutyRef(name string) string { return dutyPrefix + name }

// Dimension is the audience a duty's findings are phrased for (#88). The
// distinction is not cosmetic: the two dimensions reach different dispositions,
// and a product finding written as an operator memory would be advice nobody
// else could use.
type Dimension string

// The two dimensions.
const (
	// DimensionProduct is anyone: proposals against the public codebase.
	DimensionProduct Dimension = "product"
	// DimensionPersonal is this operator: relevance and memory amendments.
	DimensionPersonal Dimension = "personal"
)

// Duty is one standing duty: a cookbook recipe the loop may schedule on a slow
// cadence once the operator has authorized its dimension.
type Duty struct {
	// Name is the duty's stable identity, which is also its authority
	// reference and what `conductor status` lists it under.
	Name string
	// Recipe is the cookbook recipe a duty cycle runs. It is a field rather
	// than Name reused, because the duty is the standing obligation and the
	// recipe is the guidance that discharges it: a duty whose recipe is
	// renamed or split stays the same duty in the receipts.
	Recipe string
	// Toggle is the authorization this duty runs under, which is a dimension
	// rather than a duty: #88 gives the operator one switch per audience, and
	// #94's audit is explicitly part of the product dimension.
	Toggle string
	// Dimension is the audience, for status and for the recipe's own framing.
	Dimension Dimension
	// About is the duty in one clause, for `conductor status`.
	About string
}

// StandingDuties returns every duty this build knows, in draw order.
//
// The list is written out here rather than derived from the cookbook. A recipe
// existing is not an obligation to run it: the duties are the three the
// operator authorized dimensions for, and a build that grew a self-analysis
// recipe would otherwise start scheduling it by accident.
func StandingDuties() []Duty {
	return []Duty{
		{
			Name:      DutyImprovesBabel,
			Recipe:    DutyImprovesBabel,
			Toggle:    DutyImprovesBabel,
			Dimension: DimensionProduct,
			About:     "output quality and acceptance, read off the disposition and revision ledgers",
		},
		{
			Name:      DutyMechanizationAudit,
			Recipe:    DutyMechanizationAudit,
			Toggle:    DutyImprovesBabel,
			Dimension: DimensionProduct,
			About:     "where inference substituted for retrieval, read off the run receipts",
		},
		{
			Name:      DutyTunesItself,
			Recipe:    DutyTunesItself,
			Toggle:    DutyTunesItself,
			Dimension: DimensionPersonal,
			About:     "this operator's relevance and memory, revisited against the archive",
		},
	}
}

// Duties is the operator's standing-duty authorization: which dimensions the
// loop may schedule duty cycles for. Both default off.
//
// They are flags on `conductor configure` rather than #86-style ceremonies, and
// that is a statement about what they authorize. A ceremony exists where the
// operator is choosing model authority in front of Code's own interface; these
// authorize *scheduling* of recipes under the profile that ceremony already
// minted, over a corpus the loop could already read, inside ceilings the
// operator already stated. Nothing here reaches a model, widens a grant or
// mints a profile, so a terminal handover would add ritual without adding
// intent — the same reasoning that made the ceilings flags.
type Duties struct {
	ImprovesBabel bool
	TunesItself   bool
}

// authorizes reports whether the operator has authorized d's dimension.
func (t Duties) authorizes(d Duty) bool {
	switch d.Toggle {
	case DutyImprovesBabel:
		return t.ImprovesBabel
	case DutyTunesItself:
		return t.TunesItself
	default:
		return false
	}
}

// History is the loop's own cycle record as the duty rung reads it. *Journal
// satisfies it.
//
// The cadence is computed from the journal rather than from a timestamp the
// rung keeps, for the same reason the serendipity floor's ratio is: a duty that
// ran an hour before a restart must still be a duty that ran today, and a
// scheduler that forgot its duties on restart would run them once per process
// rather than once per day.
type History interface {
	Reverse() []Cycle
}

// DefaultDutyCadence is how often one duty may be drawn when none was
// configured.
//
// A day, because of what these duties read. Their evidence is Babel's own
// accumulated record — the disposition ledger, the revision chains, the run
// receipts — which moves on the timescale of days of use, and a duty drawn
// hourly would re-read the same ledger and re-emit the same finding while
// spending the operator's ceiling on it.
const DefaultDutyCadence = 24 * time.Hour

// DutyRung is rung two: the standing self-improvement duties of #88 and #94.
//
// Its position on the ladder is the whole of its restraint. It is below the
// operator's invitations, so a duty is drawn only when nobody has asked for
// anything — dutifulness never outranks a person. It is above the serendipity
// floor but the floor is a protected fraction rather than a last resort, so the
// guaranteed chaotic share still binds against a rung that would otherwise
// always have something to do. And each duty is bounded by a cadence, because
// the one failure mode of a standing duty is that it is always available: a
// rung with permanent work would quietly become the only rung, and it would do
// it by running the same analysis over an unchanged record.
//
// The rung is implemented whatever the operator authorized. A depth of zero
// here means "no duty is due", and States reports each duty's own state, so an
// unauthorized duty is visibly off rather than absent — #88's toggles default
// off, which makes "you have authorized none of these" the ordinary answer and
// exactly the one a status view must be able to give. What this build still
// does not implement is the other half of the spec's attention policy —
// lifecycle, focus, fleet — and the rung's note says so rather than letting an
// implemented duty imply an implemented policy.
type DutyRung struct {
	duties  []Duty
	toggles Duties
	history History
	cadence time.Duration
	now     func() time.Time
}

// NewDutyRung builds rung two over the operator's authorization and the loop's
// own history. A nil clock is the real one and a non-positive cadence is
// DefaultDutyCadence; both are injected so a test can drive a day of cycles
// without waiting for one.
func NewDutyRung(toggles Duties, history History, now func() time.Time, cadence time.Duration) *DutyRung {
	if now == nil {
		now = time.Now
	}
	if cadence <= 0 {
		cadence = DefaultDutyCadence
	}
	return &DutyRung{
		duties:  StandingDuties(),
		toggles: toggles,
		history: history,
		cadence: cadence,
		now:     now,
	}
}

// Name reports this rung's stable name.
func (r *DutyRung) Name() string { return RungPolicy }

// Cadence reports how often one duty may be drawn.
func (r *DutyRung) Cadence() time.Duration { return r.cadence }

// Depth reports how many duties are due, and what the rung holds beyond them.
func (r *DutyRung) Depth(context.Context) (Depth, error) {
	states := r.States(r.now())
	authorized, due := 0, 0
	for _, s := range states {
		if s.Enabled {
			authorized++
		}
		if s.Due {
			due++
		}
	}
	return Depth{
		Waiting:     due,
		Implemented: true,
		Note: fmt.Sprintf("%d of %d standing %s authorized, %d due; no attention policy in this build",
			authorized, len(states), plural(len(states), "duty", "duties"), due),
	}, nil
}

// Draw takes the first authorized duty whose cadence has elapsed.
//
// First-due-first rather than round-robin: with a daily cadence every
// authorized duty is drawn within a day whichever order they are consulted in,
// and a rotation kept somewhere other than the journal would be one more piece
// of scheduling state that a restart could disagree with.
//
// Nothing is consumed here. A duty is a standing obligation rather than a queue
// entry, so the draw is a statement about the clock and the journal, and a
// conductor that dies mid-cycle leaves a journalled duty cycle that the cadence
// already counts — the interrupted run is resumed under its own identity, and
// the duty is not drawn a second time for it.
func (r *DutyRung) Draw(_ context.Context, d DrawRequest) (Assignment, error) {
	at := d.At
	if at.IsZero() {
		at = r.now()
	}
	for _, s := range r.States(at) {
		if !s.Due {
			continue
		}
		return Assignment{
			Rung:      RungPolicy,
			Authority: run.Authority{Kind: run.AuthorityPolicy, Ref: DutyRef(s.Name)},
			// No slice: a duty reads Babel's own record over whatever corpus
			// this host has, and an empty session list is exactly that. The
			// evidence these duties weigh — dispositions, revisions, receipts,
			// usage — is not session-scoped, so narrowing to a sample would
			// bound the corpus half of the analysis for no stated reason.
			Recipes: []string{s.Recipe},
			// The note states the scope because the assignment cannot: an
			// empty session list is the whole corpus, and a cycle row that
			// only said "0 sessions" would read as an analysis of nothing.
			Note: TrimNote(fmt.Sprintf("standing duty %s over this host's whole corpus: %s; %s",
				s.Name, s.About, drawnBefore(s.LastDrawnAt))),
		}, nil
	}
	return Assignment{}, ErrNoWork
}

// drawnBefore renders a duty's last draw for the cycle note.
func drawnBefore(last time.Time) string {
	if last.IsZero() {
		return "never drawn before"
	}
	return "last drawn " + last.UTC().Format(time.RFC3339)
}

// DutyState is one duty as `conductor status` reports it.
type DutyState struct {
	Duty
	// Enabled is the operator's authorization for this duty's dimension.
	Enabled bool
	// Due reports that this duty may be drawn now. A duty that is off is
	// never due.
	Due bool
	// LastDrawnAt is when the loop last drew this duty, zero when never.
	LastDrawnAt time.Time
	// Note is this duty's state in one line, in the operator's terms: what it
	// is waiting for, or what would turn it on.
	Note string
}

// States reports every duty this build knows with its authorization and its
// cadence at t, in draw order.
//
// Off duties are reported rather than omitted. "This build has no such duty"
// and "you have not authorized it" are different answers to why the loop is not
// doing something, and a status view that listed only what was on would give
// the first answer for both.
func (r *DutyRung) States(t time.Time) []DutyState {
	last := r.lastDrawn()
	out := make([]DutyState, 0, len(r.duties))
	for _, duty := range r.duties {
		s := DutyState{
			Duty:        duty,
			Enabled:     r.toggles.authorizes(duty),
			LastDrawnAt: last[duty.Name],
		}
		switch {
		case !s.Enabled:
			s.Note = fmt.Sprintf("off; authorize with \"babel conductor configure --%s\"", duty.Toggle)
		case s.LastDrawnAt.IsZero():
			s.Due = true
			s.Note = "on and due; never drawn"
		case !t.Before(s.LastDrawnAt.Add(r.cadence)):
			s.Due = true
			s.Note = "on and due; " + drawnBefore(s.LastDrawnAt)
		default:
			s.Note = fmt.Sprintf("on, next draw after %s; %s",
				s.LastDrawnAt.Add(r.cadence).UTC().Format(time.RFC3339), drawnBefore(s.LastDrawnAt))
		}
		out = append(out, s)
	}
	return out
}

// lastDrawn reads the journal for when each duty was last drawn.
//
// A cycle counts if it drew work at all, whatever became of the run: a duty
// cycle that failed spent the operator's budget on the duty, and treating a
// failure as "not drawn" would retry it every cycle for a day.
func (r *DutyRung) lastDrawn() map[string]time.Time {
	out := make(map[string]time.Time, len(r.duties))
	if r.history == nil {
		return out
	}
	for _, cycle := range r.history.Reverse() {
		if !cycle.counts() || cycle.Authority.Kind != run.AuthorityPolicy {
			continue
		}
		name, ok := strings.CutPrefix(cycle.Authority.Ref, dutyPrefix)
		if !ok {
			continue
		}
		// Newest first, so the first entry for a duty is its last draw.
		if _, seen := out[name]; !seen {
			out[name] = cycle.StartedAt
		}
	}
	return out
}
