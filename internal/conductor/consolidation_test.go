package conductor_test

import (
	"context"
	"slices"
	"testing"

	"github.com/atyrode/babel/internal/conductor"
	"github.com/atyrode/babel/internal/frontier"
	runstore "github.com/atyrode/babel/internal/run"
)

// fakeCandidates is an unexplored frontier with a planted head, in the order
// the store would return it: priority first, then age.
type fakeCandidates struct {
	ids   []string
	draws int
}

func (f *fakeCandidates) Unexplored(_ context.Context, limit int) ([]frontier.Hypothesis, error) {
	f.draws++
	out := make([]frontier.Hypothesis, 0, len(f.ids))
	for _, id := range f.ids {
		if limit > 0 && len(out) == limit {
			break
		}
		out = append(out, frontier.Hypothesis{ID: id})
	}
	return out, nil
}

// fakeOrigins says which sessions each candidate came out of.
type fakeOrigins map[string][]string

func (f fakeOrigins) Origin(_ context.Context, ref frontier.Ref) (conductor.Origin, error) {
	return conductor.Origin{Sessions: f[ref.ID]}, nil
}

// A consolidation cycle is seeded from candidates the frontier is still
// holding, and its roots share one corpus.
//
// Both halves matter. Without the roots the cycle would be another discovery
// pass and the frontier would keep growing, which is the state 1,602 deferred
// hypotheses and no findings describes. Without the grouping a cycle would
// prepare the union of every corpus its window happened to touch and hand the
// challenger a scope with no subject.
func TestConsolidationSeedsRootsFromOneCorpusOfUnexploredCandidates(t *testing.T) {
	candidates := &fakeCandidates{ids: []string{"h-1", "h-2", "h-3", "h-4"}}
	origins := fakeOrigins{
		"h-1": {"omp/beta", "omp/alpha"},
		"h-2": {"omp/alpha", "omp/beta"},
		"h-3": {"omp/gamma"},
		"h-4": {"omp/alpha", "omp/beta"},
	}
	runner := &fakeRunner{}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		// A floor this wide never comes due inside a test of a few cycles, so
		// what the loop draws is the consolidation share and not the chaos.
		Floor:  conductor.Floor{OneIn: 100},
		Ladder: []conductor.Rung{&stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{Note: "an operator asked"}}},
		Consolidation: conductor.Consolidation{
			OneIn: 1,
			Rung:  conductor.NewConsolidationRung(candidates, origins, nil, 5),
		},
		Runner: runner, Ledger: fakeLedger{}, Journal: testJournal(t), Now: (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cycle, err := loop.Once(t.Context())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Rung != conductor.RungConsolidation {
		t.Fatalf("cycle drew the %s rung, want consolidation even with an invitation waiting", cycle.Rung)
	}
	if len(runner.runs) != 1 {
		t.Fatalf("the runner saw %d runs, want 1", len(runner.runs))
	}
	got := runner.runs[0].assignment
	if want := []string{"h-1", "h-2", "h-4"}; !slices.Equal(got.Roots, want) {
		t.Errorf("cycle seeded from %v, want %v: h-3 came out of another corpus", got.Roots, want)
	}
	if want := []string{"omp/alpha", "omp/beta"}; !slices.Equal(got.Sessions, want) {
		t.Errorf("cycle scoped to %v, want the corpus its roots came from %v", got.Sessions, want)
	}
	if got.Authority.Kind != runstore.AuthorityPolicy || got.Authority.Ref != "consolidation:h-1" {
		t.Errorf("cycle ran under %s, want a policy authority naming the candidate it led with", got.Authority)
	}
	if cycle.Authority != got.Authority {
		t.Errorf("the journal recorded %s and the run was given %s", cycle.Authority, got.Authority)
	}
}

// The share is a share: it draws its guaranteed fraction of cycles and leaves
// the rest to the ladder. A dial the operator sets and the loop ignores in
// either direction would be worse than no dial.
func TestConsolidationTakesItsShareAndNoMore(t *testing.T) {
	candidates := &fakeCandidates{ids: []string{"h-1", "h-2", "h-3", "h-4", "h-5", "h-6"}}
	runner := &fakeRunner{}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Floor:    conductor.Floor{OneIn: 100},
		Ladder:   []conductor.Rung{&stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{Note: "an operator asked"}}},
		Consolidation: conductor.Consolidation{
			OneIn: 2,
			Rung:  conductor.NewConsolidationRung(candidates, fakeOrigins{}, nil, 1),
		},
		Runner: runner, Ledger: fakeLedger{}, Journal: testJournal(t), Now: (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var drawn []string
	for range 4 {
		cycle, err := loop.Once(t.Context())
		if err != nil {
			t.Fatalf("Once: %v", err)
		}
		drawn = append(drawn, cycle.Rung)
	}
	want := []string{conductor.RungInvitation, conductor.RungConsolidation,
		conductor.RungInvitation, conductor.RungConsolidation}
	if !slices.Equal(drawn, want) {
		t.Errorf("one cycle in two drew %v, want %v", drawn, want)
	}
}

// An unset share draws nothing. Consolidation runs the challenger and the
// synthesizer, so a build that started scheduling it because it was upgraded
// would spend against a ceiling set for discovery alone.
func TestConsolidationIsOffUntilTheOperatorSetsAShare(t *testing.T) {
	candidates := &fakeCandidates{ids: []string{"h-1"}}
	runner := &fakeRunner{}
	loop, err := conductor.New(conductor.Config{
		Ceilings: testCeilings,
		Floor:    conductor.Floor{OneIn: 100},
		Ladder:   []conductor.Rung{&stubRung{name: conductor.RungInvitation, work: &conductor.Assignment{Note: "an operator asked"}}},
		Consolidation: conductor.Consolidation{
			Rung: conductor.NewConsolidationRung(candidates, fakeOrigins{}, nil, 0),
		},
		Runner: runner, Ledger: fakeLedger{}, Journal: testJournal(t), Now: (&clock{now: day}).Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cycle, err := loop.Once(t.Context())
	if err != nil {
		t.Fatalf("Once: %v", err)
	}
	if cycle.Rung != conductor.RungInvitation {
		t.Errorf("an unconfigured share drew the %s rung", cycle.Rung)
	}
	if candidates.draws != 0 {
		t.Errorf("the frontier was read %d times by a loop that consolidates nothing", candidates.draws)
	}
}

// A share with no rung to draw from is refused at construction rather than
// discovered as a nil dereference on the cycle that first came due.
func TestConsolidationShareWithoutARungIsRefused(t *testing.T) {
	_, err := conductor.New(conductor.Config{
		Ceilings:      testCeilings,
		Ladder:        []conductor.Rung{&stubRung{name: conductor.RungInvitation}},
		Consolidation: conductor.Consolidation{OneIn: 3},
		Runner:        &fakeRunner{}, Ledger: fakeLedger{}, Journal: testJournal(t),
	})
	if err == nil {
		t.Fatal("a consolidation share with no rung was accepted")
	}
}
