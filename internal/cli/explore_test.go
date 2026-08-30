package cli

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/explore"
	runstore "github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// shippedRecipes and defaultRecipes are SPEC.md §5.5's five-of-eight split,
// written out here as counts rather than derived from the cookbook. A run's
// implicit scope is the thing being defended: if default-enablement quietly
// became "everything" again, deriving the expectation from the same source
// would make this file agree with the bug.
const (
	shippedRecipes = 8
	defaultRecipes = 5
)

func exploreCmd() *cmd { return newCmd("explore", exploreUsage) }

func embeddedCookbook(t *testing.T) *cookbook.Set {
	t.Helper()
	set, err := cookbook.Embedded()
	if err != nil {
		t.Fatalf("cookbook.Embedded() = %v", err)
	}
	return set
}

// briefedIDs reads a selection the way every consumer of a run's recipe set
// reads it: through Set.All(). internal/explore renders the receipt's
// cookbook assets and picks each stage's recipes from that one accessor, so
// what this returns is what the worker is actually briefed with.
func briefedIDs(set *cookbook.Set) []string {
	ids := make([]string, 0, len(set.All()))
	for _, r := range set.All() {
		ids = append(ids, r.ID)
	}
	return ids
}

// receiptRecipes is the run's own attestation of what was analyzed: the
// recipe list of the document `babel explore --json` prints and an export
// carries. It is asserted separately from briefedIDs because the defect being
// fixed made these two disagree with the operator's request in the same way,
// and a receipt overstating the analysis is the more damaging half.
func receiptRecipes(set *cookbook.Set) []string {
	res := exploreOutcome(
		runstore.Preparation{ID: "prep-synthetic"},
		worker.ProfileRef{ID: "synthetic", Revision: 1},
		set,
		&explore.Outcome{RunID: "run-synthetic"},
		0,
	)
	return res.Recipes
}

// refsOf renders the "id@version" pairs a receipt records for these ids.
func refsOf(t *testing.T, full *cookbook.Set, ids ...string) []string {
	t.Helper()
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		r, ok := full.ByID(id)
		if !ok {
			t.Fatalf("the embedded cookbook has no recipe %q", id)
		}
		out = append(out, id+"@"+strconv.Itoa(r.Version))
	}
	return out
}

func assertIDs(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// TestNamedRecipeIsTheOnlyOneRunAndTheOnlyOneAttested is the operator's
// scoping reaching both places it has to reach. --recipe used to be validated
// and then discarded, so a run asked for one lens briefed the worker with
// eight and wrote a receipt claiming eight — a provenance record overstating
// what was analyzed.
func TestNamedRecipeIsTheOnlyOneRunAndTheOnlyOneAttested(t *testing.T) {
	full := embeddedCookbook(t)
	const want = "outcome-integrity"

	set, err := selectRecipes(exploreCmd(), []string{want})
	if err != nil {
		t.Fatalf("selectRecipes(--recipe %s) = %v", want, err)
	}

	assertIDs(t, "recipes briefed to the worker", briefedIDs(set), []string{want})
	assertIDs(t, "recipes the receipt attests", receiptRecipes(set), refsOf(t, full, want))
	if _, ok := set.ByID("effective-patterns"); ok {
		t.Error("the selection still resolves a recipe the operator did not name")
	}
}

// TestNoRecipeFlagRunsExactlyTheDefaultLenses pins the documented implicit
// scope. The counts are asserted as well as the ids: "the defaults" silently
// widening to the whole cookbook is precisely the regression, and it would
// pass an id comparison derived from Defaults() alone.
func TestNoRecipeFlagRunsExactlyTheDefaultLenses(t *testing.T) {
	full := embeddedCookbook(t)
	if got := len(full.All()); got != shippedRecipes {
		t.Fatalf("the embedded cookbook holds %d recipes, want %d; update §5.5's counts deliberately", got, shippedRecipes)
	}

	var want []string
	for _, r := range full.Defaults() {
		want = append(want, r.ID)
	}
	if len(want) != defaultRecipes {
		t.Fatalf("the cookbook default-enables %d recipes, want %d", len(want), defaultRecipes)
	}

	set, err := selectRecipes(exploreCmd(), nil)
	if err != nil {
		t.Fatalf("selectRecipes(no --recipe) = %v", err)
	}

	assertIDs(t, "recipes briefed to the worker", briefedIDs(set), want)
	assertIDs(t, "recipes the receipt attests", receiptRecipes(set), refsOf(t, full, want...))
	if got := len(set.All()); got >= shippedRecipes {
		t.Errorf("the default selection holds %d of %d recipes; the default set is not a narrowing at all",
			got, shippedRecipes)
	}
}

// TestUnknownRecipeIsRefusedByName keeps a mistyped id an actionable usage
// failure rather than a run that quietly explores something else. The exit
// code matters as much as the text: a usage error is exit 2, and a script
// distinguishes it from a failed analysis.
func TestUnknownRecipeIsRefusedByName(t *testing.T) {
	const bad = "outcome-integrety"

	_, err := selectRecipes(exploreCmd(), []string{bad})
	if err == nil {
		t.Fatal("an unknown --recipe was accepted")
	}
	if !strings.Contains(err.Error(), bad) {
		t.Errorf("the message does not name the unknown id: %v", err)
	}
	if !strings.Contains(err.Error(), "outcome-integrity") {
		t.Errorf("the message does not list the ids that do exist: %v", err)
	}
	var usage *usageError
	if !errors.As(err, &usage) {
		t.Errorf("err is %T, want a usage error so the invocation exits %d", err, exitUsage)
	}
}

// TestExplicitlyNamedDraftRecipeRuns fixes the meaning of an explicit
// --recipe naming a lens §5.5 ships as a draft: it runs. Default-enablement
// answers what a run does when the operator said nothing, not what a run is
// permitted to do, and intersecting the request with the defaults would be
// the discarded-selection bug in a quieter costume.
func TestExplicitlyNamedDraftRecipeRuns(t *testing.T) {
	full := embeddedCookbook(t)
	const draft = "durable-operator-model"

	r, ok := full.ByID(draft)
	if !ok {
		t.Fatalf("the embedded cookbook has no recipe %q", draft)
	}
	if r.Default {
		t.Fatalf("recipe %q is default-enabled; this test needs a non-default one to be meaningful", draft)
	}

	set, err := selectRecipes(exploreCmd(), []string{draft})
	if err != nil {
		t.Fatalf("selectRecipes(--recipe %s) = %v", draft, err)
	}
	assertIDs(t, "recipes briefed to the worker", briefedIDs(set), []string{draft})
	assertIDs(t, "recipes the receipt attests", receiptRecipes(set), refsOf(t, full, draft))
}
