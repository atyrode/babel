package conductor

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
)

// Focus is §4.8's expenditure policy as the ladder consults it.
// *reality.Attention satisfies it.
//
// It is an interface for the reason Runner and Ledger are: the loop is told
// what may be spent and has no path to a policy it decided for itself. A nil
// Focus is a machine that has recorded none, and then nothing is withheld —
// which is the state every machine is in until an operator says otherwise,
// and the only safe reading of an absent ledger.
type Focus interface {
	Admit(ctx context.Context, in reality.AdmitRequest) (reality.Admission, error)
}

// subjectsOf reports the names a candidate states about itself.
//
// A hypothesis carries no entity reference. §4.8 has emergence resolve one
// and freeze it into a context snapshot, and a candidate sitting on the
// frontier predates that step, so the names it does carry are what a gate has
// to work from: §4.2's provisional labels and the origin cues that provoked
// it. They go through the ledger's typed aliases, which is where AliasChatTerm
// earns its existence — it is there precisely so the terminology a
// conversation used can name an entity.
//
// A label nothing answers to names no subject and withholds nothing. That is
// the right failure direction: a gate that inferred a subject from prose
// would silence candidates about projects the operator never spoke about,
// and the operator's remedy for a candidate that escapes the gate is to
// attach the alias, which is a record rather than a guess.
func subjectsOf(h frontier.Hypothesis) []string {
	names := make([]string, 0,
		len(h.Payload.ProvisionalLabels)+len(h.Payload.OriginCues))
	for _, name := range slices.Concat(h.Payload.ProvisionalLabels, h.Payload.OriginCues) {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// focusPass consults the policy for one draw or one depth report.
//
// The memo is per pass and deliberately not per rung. A depth report reads a
// frontier that can hold thousands of candidates over a handful of distinct
// labels, so resolving each name and evaluating each entity once turns a
// status view from a query storm into a few lookups — while a cache that
// outlived the pass would let the loop act on a policy the operator has since
// changed, which is the one thing a versioned, as-of decision exists to
// prevent.
type focusPass struct {
	focus Focus
	at    time.Time
	memo  map[string]reality.Admission
}

func newFocusPass(focus Focus, at time.Time) *focusPass {
	return &focusPass{focus: focus, at: at, memo: make(map[string]reality.Admission)}
}

// permits reports what the policy allows without recording anything. A pass
// that spends nothing has no deferral to record, and writing a snapshot every
// time `conductor status` counted the backlog would fill the ledger with
// refusals nobody asked for.
func (p *focusPass) permits(ctx context.Context, h frontier.Hypothesis, work reality.Work) (reality.Admission, error) {
	names := subjectsOf(h)
	key := string(work) + "\x00" + strings.Join(names, "\x00")
	if hit, ok := p.memo[key]; ok {
		return hit, nil
	}
	admission, err := p.admit(ctx, reality.AdmitRequest{Names: names, Work: work, AsOf: p.at})
	if err != nil {
		return reality.Admission{}, err
	}
	p.memo[key] = admission
	return admission, nil
}

// spend reports what the policy allows for work this cycle is about to do,
// and records the refusal against the candidate when it is refused. It is
// never memoized: the snapshot is taken per candidate, and a memo would drop
// the trace for every candidate after the first that shares its labels.
func (p *focusPass) spend(ctx context.Context, h frontier.Hypothesis, work reality.Work, note string) (reality.Admission, error) {
	return p.admit(ctx, reality.AdmitRequest{
		Names:        subjectsOf(h),
		Work:         work,
		HypothesisID: h.ID,
		Note:         note,
		AsOf:         p.at,
	})
}

func (p *focusPass) admit(ctx context.Context, in reality.AdmitRequest) (reality.Admission, error) {
	if p.focus == nil {
		return reality.Admission{Work: in.Work, Permitted: true, Allowance: reality.AllowanceFull}, nil
	}
	return p.focus.Admit(ctx, in)
}
