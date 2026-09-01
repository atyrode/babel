package cookbook

import (
	"fmt"
	"strings"
	"testing"
)

// samplePreamble renders a statement document: one `version` line and a body
// with a title and something under it.
func samplePreamble(version int, statement string) []byte {
	return []byte(fmt.Sprintf("---\nversion: %d\n---\n\n# Sample statement\n\n%s\n", version, statement))
}

// defaultPreamble is the statement mapFS plants in a tree whose case is about
// something else.
var defaultPreamble = samplePreamble(1, "What these recipes are for, and what their changes are measured against.")

// defaultPreambleEntry is the version record's line for defaultPreamble, so a
// drift case about a recipe records the statement as unchanged.
func defaultPreambleEntry(t *testing.T) PreambleEntry {
	t.Helper()
	p, err := ParsePreamble(preambleFile, defaultPreamble)
	if err != nil {
		t.Fatalf("ParsePreamble(defaultPreamble) = %v", err)
	}
	return PreambleEntry{Version: p.Version, Digest: p.Digest}
}

// TestShippedPreambleStatesTheCharterPrinciple is #120's acceptance on the
// asset side: the cookbook carries the charter's axiom where recipes are
// written, and says it is an ordering rather than a subject-matter boundary.
// A cookbook whose statement stopped saying either would still load, so this
// is asserted rather than assumed.
func TestShippedPreambleStatesTheCharterPrinciple(t *testing.T) {
	set := loadEmbedded(t)
	p := set.Preamble()
	if p == nil {
		t.Fatal("the shipped cookbook carries no statement")
	}
	if p.Version < 1 || p.Title == "" || p.Path != preambleFile {
		t.Errorf("statement = %+v, want a versioned, titled document at %s", p, preambleFile)
	}

	body := strings.ToLower(p.Body)
	for _, want := range []string{
		// The axiom itself (SPEC.md §1, operator decision 2026-08-31).
		"friction",
		// The flagship lens the principle points at.
		"human-agent-coordination",
		// And the half that keeps friction-primacy from becoming
		// friction-exclusivity: Babel still wanders.
		"open discovery",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the statement never mentions %q; it is what recipe changes are measured against", want)
		}
	}
}

// TestPreambleTravelsWithANarrowedSelection is the property a run depends on:
// a selection of two recipes is still this cookbook, written under this
// statement, so a receipt or a listing derived from it can say which one.
func TestPreambleTravelsWithANarrowedSelection(t *testing.T) {
	full := loadEmbedded(t)
	narrowed, err := full.Select([]string{"human-agent-coordination"})
	if err != nil {
		t.Fatalf("Select = %v", err)
	}
	if narrowed.Preamble() != full.Preamble() {
		t.Errorf("a narrowed selection answers with a different statement: %+v", narrowed.Preamble())
	}
}

// TestPreambleRejections is the statement's grammar. It is narrower than a
// recipe's — one key, one title — so the errors must still name the line, for
// the same reason a recipe's do.
func TestPreambleRejections(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		line int
		want string
	}{
		{
			name: "no front matter",
			doc:  "# Statement\n\nText.\n",
			line: 1,
			want: "must begin with",
		},
		{
			name: "unclosed front matter",
			doc:  "---\nversion: 1\n\n# Statement\n",
			line: 4,
			want: "never closed",
		},
		{
			name: "a second key",
			doc:  "---\nversion: 1\nkind: lens\n---\n\n# Statement\n\nText.\n",
			line: 2,
			want: "single line",
		},
		{
			name: "the wrong key",
			doc:  "---\nid: preamble\n---\n\n# Statement\n\nText.\n",
			line: 2,
			want: "must declare \"version\"",
		},
		{
			name: "two spaces after the colon",
			doc:  "---\nversion:  1\n---\n\n# Statement\n\nText.\n",
			line: 2,
			want: "exactly one space",
		},
		{
			name: "version zero",
			doc:  "---\nversion: 0\n---\n\n# Statement\n\nText.\n",
			line: 2,
			want: "version",
		},
		{
			name: "text before the title",
			doc:  "---\nversion: 1\n---\n\nText first.\n\n# Statement\n",
			line: 5,
			want: "must open with a level-1 title",
		},
		{
			name: "two titles",
			doc:  "---\nversion: 1\n---\n\n# Statement\n\nText.\n\n# Second\n",
			line: 9,
			want: "second level-1 heading",
		},
		{
			name: "a title with nothing under it",
			doc:  "---\nversion: 1\n---\n\n# Statement\n",
			line: 4,
			want: "no statement under it",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParsePreamble(preambleFile, []byte(tc.doc))
			if err == nil {
				t.Fatal("ParsePreamble succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
			if !strings.HasPrefix(err.Error(), fmt.Sprintf("%s:%d: ", preambleFile, tc.line)) {
				t.Errorf("error = %v, want it reported against line %d", err, tc.line)
			}
		})
	}
}

// TestPreambleNamingAProviderFailsToLoad extends §5.1's prohibition to the
// document recipes are written against. Guidance that may not choose a
// provider cannot be handed one by the statement above it.
func TestPreambleNamingAProviderFailsToLoad(t *testing.T) {
	_, err := ParsePreamble(preambleFile, samplePreamble(1, "Prefer the copilot runtime for coordination work."))
	if err == nil {
		t.Fatal("a statement naming a provider loaded")
	}
	if !strings.Contains(err.Error(), "copilot") {
		t.Errorf("error = %v, want it to name the mention", err)
	}
}

// TestCookbookWithoutAStatementIsAnError is why the statement is part of the
// cookbook rather than a document beside it: a tree that lost it would go on
// loading, and its recipes would go on evolving against nothing.
func TestCookbookWithoutAStatementIsAnError(t *testing.T) {
	fsys := mapFS(map[string]string{"recipes/sample.md": string(sampleDoc(nil, sampleBody("Sample")))})
	delete(fsys, preambleFile)

	if _, err := Load(fsys); err == nil {
		t.Fatal("a cookbook with no statement loaded")
	} else if !strings.Contains(err.Error(), preambleFile) {
		t.Errorf("error = %v, want it to name the missing document", err)
	}
}

// TestRecipeMayNotTakeTheStatementsID keeps one drift report about one
// document: the record keys the statement and the recipes by the same
// namespace.
func TestRecipeMayNotTakeTheStatementsID(t *testing.T) {
	fsys := mapFS(map[string]string{
		"recipes/preamble.md": string(sampleDoc([]string{"id: preamble"}, sampleBody("Sample"))),
	})
	if _, err := Load(fsys); err == nil {
		t.Fatal("a recipe took the statement's id")
	} else if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error = %v, want it to report the reserved id", err)
	}
}

// TestStatementDriftIsCheckedLikeARecipe is the §5.1 discipline #120 asks for:
// the document that measures recipe evolution is measured the same way. A
// rewritten statement under an unchanged version is drift, and an increment
// the record has not seen is a record to regenerate.
func TestStatementDriftIsCheckedLikeARecipe(t *testing.T) {
	recipe := sampleDoc(nil, sampleBody("Sample"))
	parsed, err := ParseRecipe("recipes/sample.md", recipe)
	if err != nil {
		t.Fatalf("ParseRecipe = %v", err)
	}
	entry := ManifestEntry{ID: parsed.ID, Version: parsed.Version, Digest: parsed.Digest}

	record := func(t *testing.T) string {
		t.Helper()
		m := Manifest{
			Schema:   manifestSchema,
			Preamble: defaultPreambleEntry(t),
			Recipes:  []ManifestEntry{entry},
		}
		data, err := m.Canonical()
		if err != nil {
			t.Fatalf("Canonical() = %v", err)
		}
		return string(data)
	}

	tests := []struct {
		name     string
		preamble []byte
		want     []DriftKind
		detail   string
	}{
		{
			name:     "committed state has no drift",
			preamble: defaultPreamble,
		},
		{
			name:     "restated without a version increment",
			preamble: samplePreamble(1, "A materially different account of what the recipes are for."),
			want:     []DriftKind{DriftUndeclared},
			detail:   "§5.1 requires an increment",
		},
		{
			name:     "restated with a version increment the record has not seen",
			preamble: samplePreamble(2, "A materially different account of what the recipes are for."),
			want:     []DriftKind{DriftStale},
			detail:   "does not have",
		},
		{
			name:     "version increment with nothing restated",
			preamble: samplePreamble(2, "What these recipes are for, and what their changes are measured against."),
			want:     []DriftKind{DriftStale},
			detail:   "no content change to declare",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := mapFS(map[string]string{
				preambleFile:        string(tc.preamble),
				"recipes/sample.md": string(recipe),
				manifestFile:        record(t),
			})
			drifts, err := Verify(fsys)
			if err != nil {
				t.Fatalf("Verify() = %v", err)
			}
			if len(drifts) != len(tc.want) {
				t.Fatalf("drifts = %v, want %v", drifts, tc.want)
			}
			for i, want := range tc.want {
				if drifts[i].Kind != want {
					t.Errorf("drift %d = %q, want %q", i, drifts[i].Kind, want)
				}
				if drifts[i].ID != preambleID {
					t.Errorf("drift %d names %q, want the statement", i, drifts[i].ID)
				}
			}
			if tc.detail != "" && !strings.Contains(drifts[0].Detail, tc.detail) {
				t.Errorf("detail = %q, want it to contain %q", drifts[0].Detail, tc.detail)
			}
		})
	}

	// And the record's own integrity: a statement whose version moved
	// backwards would let a superseded standard be recorded under a version
	// that has already been cited.
	recorded := defaultPreambleEntry(t)
	recorded.Version = 7
	back, err := Manifest{
		Schema:   manifestSchema,
		Preamble: recorded,
		Recipes:  []ManifestEntry{entry},
	}.Canonical()
	if err != nil {
		t.Fatalf("Canonical() = %v", err)
	}
	drifts, err := Verify(mapFS(map[string]string{
		"recipes/sample.md": string(recipe),
		manifestFile:        string(back),
	}))
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	if len(drifts) != 1 || drifts[0].Kind != DriftRegressed || drifts[0].ID != preambleID {
		t.Fatalf("drifts = %v, want one regressed statement", drifts)
	}
}
