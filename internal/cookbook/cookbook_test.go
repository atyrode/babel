package cookbook

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/worker"
)

// shipped is the cookbook this build is expected to carry: SPEC.md §5.5's
// eight baseline lenses, five of them default-enabled and three shipped as
// reviewable drafts, plus the three self-improvement duty recipes of issues
// #88 and #94, none of which is default-enabled because each runs only under
// the operator's own conductor toggle. The table is written out rather than
// derived from the asset tree so that adding, removing, or default-enabling a
// recipe is a deliberate edit to a test someone reviews.
var shipped = []struct {
	id       string
	kind     Kind
	enabled  bool
	minScope int
}{
	{id: "babel-improves-babel", kind: KindMeta, enabled: false, minScope: 2},
	{id: "babel-tunes-itself", kind: KindMeta, enabled: false, minScope: 2},
	{id: "code-health-comprehensibility", kind: KindLens, enabled: true, minScope: 3},
	{id: "decision-quality-operational-risk", kind: KindLens, enabled: false, minScope: 3},
	{id: "durable-operator-model", kind: KindLens, enabled: false, minScope: 2},
	{id: "effective-patterns", kind: KindLens, enabled: true, minScope: 3},
	{id: "human-agent-coordination", kind: KindLens, enabled: true, minScope: 2},
	{id: "mechanization-audit", kind: KindMeta, enabled: false, minScope: 2},
	{id: "outcome-integrity", kind: KindLens, enabled: true, minScope: 3},
	{id: "reusable-practice-capability-leverage", kind: KindLens, enabled: false, minScope: 3},
	{id: "security-trust-boundaries", kind: KindLens, enabled: true, minScope: 3},
}

// defaultLenses is §5.5's list of the five lenses Phase B fully authors and
// default-enables. Nothing outside it is enabled by omission: the duty recipes
// are authorized per dimension by the operator, and a duty recipe that appeared
// here would be scheduled by every bare `babel explore`.
var defaultLenses = []string{
	"code-health-comprehensibility",
	"effective-patterns",
	"human-agent-coordination",
	"outcome-integrity",
	"security-trust-boundaries",
}

// drafts is §5.5's list of the three lenses that ship as reviewable drafts
// until corpus evaluation sharpens their overlap and guidance.
var drafts = []string{
	"decision-quality-operational-risk",
	"durable-operator-model",
	"reusable-practice-capability-leverage",
}

// duties is the set of recipes the conductor's duty rung can schedule (#88,
// #94), which is exactly the set of meta recipes this build ships.
var duties = []string{
	"babel-improves-babel",
	"babel-tunes-itself",
	"mechanization-audit",
}

func loadEmbedded(t *testing.T) *Set {
	t.Helper()
	set, err := Embedded()
	if err != nil {
		t.Fatalf("Embedded() = %v", err)
	}
	return set
}

// TestEmbeddedCookbookLoads is the acceptance test for the shipped assets:
// every recipe parses under the strict grammar, validates as a set, and
// carries exactly the metadata the cookbook is documented to ship.
func TestEmbeddedCookbookLoads(t *testing.T) {
	set := loadEmbedded(t)

	if got, want := len(set.All()), len(shipped); got != want {
		var ids []string
		for _, r := range set.All() {
			ids = append(ids, r.ID)
		}
		t.Fatalf("cookbook holds %d recipes, want %d: %v", got, want, ids)
	}

	for _, want := range shipped {
		t.Run(want.id, func(t *testing.T) {
			r, ok := set.ByID(want.id)
			if !ok {
				t.Fatalf("recipe %q is missing from the cookbook", want.id)
			}
			if r.Kind != want.kind {
				t.Errorf("kind = %q, want %q", r.Kind, want.kind)
			}
			if r.Default != want.enabled {
				t.Errorf("default = %t, want %t", r.Default, want.enabled)
			}
			if r.Version < 1 {
				t.Errorf("version = %d, want at least 1", r.Version)
			}
			if len(r.Scope) < want.minScope {
				t.Errorf("declares %d scopes, want at least %d", len(r.Scope), want.minScope)
			}
			if len(r.Stages) == 0 {
				t.Error("declares no stage")
			}
			for _, c := range r.Capabilities {
				if !c.Known() {
					t.Errorf("declares capability %q, which Babel does not define", c)
				}
			}
			if r.Title == "" {
				t.Error("has no title")
			}
			if r.Path != "recipes/"+want.id+".md" {
				t.Errorf("path = %q, want recipes/%s.md", r.Path, want.id)
			}
			if !r.Digest.Valid() {
				t.Errorf("digest %q is not canonical", r.Digest)
			}
			if ref := r.Ref(); ref != (worker.RecipeRef{ID: r.ID, Version: r.Version}) {
				t.Errorf("Ref() = %+v, want the id/version pair", ref)
			}
		})
	}
}

// TestShippedBodiesCarryEverySection covers the §5.1 body contract on the real
// assets: each required section exists, in order, with substantive content
// rather than a heading followed by a placeholder line.
func TestShippedBodiesCarryEverySection(t *testing.T) {
	// A section shorter than this is a placeholder, not guidance. The
	// threshold is deliberately low: it defends the contract without
	// legislating prose length.
	const minSectionBytes = 120

	for _, r := range loadEmbedded(t).All() {
		t.Run(r.ID, func(t *testing.T) {
			if len(r.Sections) != len(requiredSections) {
				t.Fatalf("body has %d sections, want %d", len(r.Sections), len(requiredSections))
			}
			for i, want := range requiredSections {
				got := r.Sections[i]
				if got.Title != want {
					t.Errorf("section %d = %q, want %q", i+1, got.Title, want)
				}
				if len(got.Text) < minSectionBytes {
					t.Errorf("section %q holds %d bytes of guidance, want at least %d",
						got.Title, len(got.Text), minSectionBytes)
				}
				if text, ok := r.Section(want); !ok || text != got.Text {
					t.Errorf("Section(%q) did not return the section's text", want)
				}
			}
		})
	}
}

// TestDefaultsAreExactlyTheFiveNamedLenses pins §5.5's split between
// default-enabled lenses and shipped drafts. Enabling a lens by accident
// silently changes what every run does, which is why the set is asserted from
// both directions.
func TestDefaultsAreExactlyTheFiveNamedLenses(t *testing.T) {
	set := loadEmbedded(t)

	var got []string
	for _, r := range set.Defaults() {
		got = append(got, r.ID)
		if r.Kind != KindLens {
			t.Errorf("default recipe %q is a %s; only a lens may be default-enabled", r.ID, r.Kind)
		}
	}
	if strings.Join(got, ",") != strings.Join(defaultLenses, ",") {
		t.Errorf("default recipes = %v, want %v", got, defaultLenses)
	}

	for _, id := range drafts {
		r, ok := set.ByID(id)
		if !ok {
			t.Fatalf("draft recipe %q is missing", id)
		}
		if r.Default {
			t.Errorf("draft recipe %q is default-enabled", id)
		}
	}

	// The duty recipes are the same kind of opt-in as a draft and a stricter
	// one: a draft is a lens the operator may name, while a duty recipe is
	// scheduled only after the operator authorized its dimension (#88, #94).
	// Default-enabling one would hand every bare `babel explore` an analysis
	// of Babel itself.
	for _, id := range duties {
		r, ok := set.ByID(id)
		if !ok {
			t.Fatalf("duty recipe %q is missing", id)
		}
		if r.Default {
			t.Errorf("duty recipe %q is default-enabled", id)
		}
		if r.Kind != KindMeta {
			t.Errorf("duty recipe %q is a %s, want a %s", id, r.Kind, KindMeta)
		}
	}
}

// TestNoRecipeNamesAProviderOrModel enforces §5.1's prohibition on a recipe
// selecting a provider or model. The rule erodes silently — one helpful
// sentence at a time — so it is checked against the denylist on every body.
func TestNoRecipeNamesAProviderOrModel(t *testing.T) {
	for _, r := range loadEmbedded(t).All() {
		if mentions := VendorMentions(r.Body); len(mentions) > 0 {
			t.Errorf("recipe %q names %v; §5.1 forbids a recipe from selecting a provider or model",
				r.ID, mentions)
		}
	}
}

// TestVendorMentionInBodyFailsToLoad covers the enforcement side: a recipe
// that names a vendor does not load at all, so the prohibition cannot be
// bypassed by shipping the recipe and ignoring a report.
func TestVendorMentionInBodyFailsToLoad(t *testing.T) {
	body := strings.Replace(sampleBody("Provider naming"),
		"Question guidance.", "Prefer the anthropic transport for this lens.", 1)
	_, err := ParseRecipe("recipes/sample.md", sampleDoc(nil, body))
	if err == nil {
		t.Fatal("a recipe naming a vendor loaded")
	}
	if !strings.Contains(err.Error(), "anthropic") || !strings.Contains(err.Error(), "§5.1") {
		t.Errorf("error = %v, want it to name the vendor and the rule", err)
	}
}

// TestVendorMentionsIsWordBounded guards the denylist against the failure that
// would make it useless: matching inside ordinary words. "corpus" contains
// "opus" as a substring, and every recipe discusses corpora.
func TestVendorMentionsIsWordBounded(t *testing.T) {
	clean := "The corpus and its encoding are copied, and the coherent claim stands."
	if got := VendorMentions(clean); len(got) > 0 {
		t.Errorf("VendorMentions(%q) = %v, want none", clean, got)
	}
	dirty := "Use GPT here, and Claude there."
	want := "claude,gpt"
	if got := strings.Join(VendorMentions(dirty), ","); got != want {
		t.Errorf("VendorMentions = %q, want %q", got, want)
	}
}

// TestSetValidation covers the whole-set rules: a recipe's file name is its id
// (which is also what makes ids unique), the recipe directory holds only
// recipes, and a default-enabled recipe is a lens.
func TestSetValidation(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "file name must be the id",
			files: map[string]string{"recipes/renamed.md": string(sampleDoc(nil, sampleBody("T")))},
			want:  "the file name must be the id",
		},
		{
			name: "stray file in the recipe directory",
			files: map[string]string{
				"recipes/sample.md": string(sampleDoc(nil, sampleBody("T"))),
				"recipes/notes.txt": "not a recipe",
			},
			want: "is not a recipe document",
		},
		{
			name: "nested directory in the recipe directory",
			files: map[string]string{
				"recipes/sample.md":       string(sampleDoc(nil, sampleBody("T"))),
				"recipes/drafts/other.md": string(sampleDoc(nil, sampleBody("T"))),
			},
			want: "is not a recipe document",
		},
		{
			name: "default on a non-lens",
			files: map[string]string{"recipes/sample.md": string(sampleDoc(
				[]string{"kind: policy", "default: true"}, sampleBody("T")))},
			want: "only a lens may be default-enabled",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(mapFS(tc.files))
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestEmptyCookbookIsAnError covers the two ways a cookbook can carry no
// guidance: no recipe directory at all, and an empty one. Neither may look
// like a valid load, because a run whose receipt records a recipe set must
// have had one.
func TestEmptyCookbookIsAnError(t *testing.T) {
	_, err := Load(mapFS(map[string]string{"versions.json": "{}"}))
	if err == nil {
		t.Fatal("Load succeeded on a tree with no recipe directory")
	}
	if !strings.Contains(err.Error(), "read cookbook recipes") {
		t.Errorf("error = %v, want it to name the missing recipe directory", err)
	}

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, recipesDir), 0o755); err != nil {
		t.Fatalf("create recipe directory: %v", err)
	}
	if _, err := LoadDir(dir); err == nil {
		t.Fatal("LoadDir succeeded on an empty recipe directory")
	} else if !strings.Contains(err.Error(), "no recipes found") {
		t.Errorf("error = %v, want it to report an empty cookbook", err)
	}
}

// TestSelectNarrowsEveryAccessor is the property a narrowed run depends on:
// a consumer of the selection sees the selection through whichever accessor
// it happens to use. internal/explore reads the run's recipes through All()
// — for the receipt's cookbook assets and for each stage's participants —
// and resolves one recipe through ByID, so a Set that narrowed one accessor
// and not the others would still brief the worker with, or attest to,
// recipes the operator never asked for.
func TestSelectNarrowsEveryAccessor(t *testing.T) {
	full := loadEmbedded(t)

	// Named out of order and with a repeat: --recipe is a repeatable flag,
	// and a receipt listing one recipe twice would misdescribe the run.
	set, err := full.Select([]string{"outcome-integrity", "effective-patterns", "outcome-integrity"})
	if err != nil {
		t.Fatalf("Select() = %v", err)
	}

	want := []string{"effective-patterns", "outcome-integrity"}
	if got := strings.Join(set.IDs(), ","); got != strings.Join(want, ",") {
		t.Errorf("IDs() = %q, want %q sorted and deduplicated", got, strings.Join(want, ","))
	}
	if got := len(set.All()); got != len(want) {
		t.Errorf("All() holds %d recipes, want %d", got, len(want))
	}
	if got := len(set.Refs()); got != len(want) {
		t.Errorf("Refs() holds %d refs, want %d", got, len(want))
	}
	for _, id := range want {
		if _, ok := set.ByID(id); !ok {
			t.Errorf("ByID(%q) missed a selected recipe", id)
		}
	}
	for _, id := range drafts {
		if _, ok := set.ByID(id); ok {
			t.Errorf("ByID(%q) resolved a recipe the selection excluded", id)
		}
	}
	// The source set is untouched: `babel cookbook list` and the web
	// catalog read the full cookbook from the same value a run narrows.
	if got := len(full.All()); got != len(shipped) {
		t.Errorf("selecting mutated the source cookbook: %d recipes remain, want %d", got, len(shipped))
	}
}

// TestSelectRejectsAnUnknownIDNamingWhatExists keeps a mistyped selection a
// refusal that can be acted on. The error carries the available ids because
// its reporter is a command line, and an id from a different build is
// indistinguishable from a typo without the list.
func TestSelectRejectsAnUnknownIDNamingWhatExists(t *testing.T) {
	full := loadEmbedded(t)

	_, err := full.Select([]string{"outcome-integrity", "no-such-lens"})
	if err == nil {
		t.Fatal("Select succeeded on an unknown id")
	}
	var unknown *UnknownRecipeError
	if !errors.As(err, &unknown) {
		t.Fatalf("err is %T, want *UnknownRecipeError so a caller can phrase it as a flag error", err)
	}
	if unknown.ID != "no-such-lens" {
		t.Errorf("ID = %q, want the id that does not exist", unknown.ID)
	}
	if strings.Join(unknown.Available, ",") != strings.Join(full.IDs(), ",") {
		t.Errorf("Available = %v, want every id the cookbook holds", unknown.Available)
	}
	if !strings.Contains(err.Error(), "no-such-lens") {
		t.Errorf("error = %v, want it to name the unknown id", err)
	}
}

// TestSelectRefusesAnEmptySelection covers the one invariant a subset can
// lose. An empty Set would otherwise flow into a run that analyzed nothing
// and still wrote a receipt for it, which is a provenance record of an
// analysis that never happened.
func TestSelectRefusesAnEmptySelection(t *testing.T) {
	full := loadEmbedded(t)

	for _, ids := range [][]string{nil, {}} {
		if _, err := full.Select(ids); err == nil {
			t.Errorf("Select(%v) succeeded, want a refusal", ids)
		} else if !strings.Contains(err.Error(), "no recipes selected") {
			t.Errorf("error = %v, want it to report an empty selection", err)
		}
	}
}
