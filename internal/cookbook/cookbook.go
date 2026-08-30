// Package cookbook loads and validates Babel's versioned analysis cookbook
// (SPEC.md §5): the shared investigation policies, domain lenses, and
// meta-analysis recipes that give exploration productive starting structure
// without constraining what discovery may propose.
//
// The package owns three things and deliberately no more. It parses a recipe:
// a reviewable Markdown document whose machine-readable front matter follows
// the narrow grammar of frontmatter.go, and whose body must carry every
// section §5.1 requires. It validates a set of recipes as a set: unique ids,
// a default-enabled recipe being a lens, capabilities drawn from the four
// facilities Babel actually brokers, and at least one scope and one stage. And
// it enforces §5.1's versioning rule mechanically through manifest.go: a
// recipe whose semantics change without its version increasing is a reported
// drift, not a convention someone remembers.
//
// What the package must never do is choose how analysis runs. §5.1 forbids a
// recipe from naming a provider or model, so a body naming one fails to load
// (see VendorMentions), and nothing here selects sources, builds prompts, or
// runs inference.
package cookbook

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	cookbookassets "github.com/atyrode/babel/cookbook"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/worker"
)

// Kind is a cookbook asset kind (§5): a shared investigation policy, a domain
// lens, or a meta recipe that analyzes Babel's own cookbook and outputs.
type Kind string

// The asset kinds.
const (
	KindPolicy Kind = "policy"
	KindLens   Kind = "lens"
	KindMeta   Kind = "meta"
)

// Known reports whether k is a documented asset kind.
func (k Kind) Known() bool {
	switch k {
	case KindPolicy, KindLens, KindMeta:
		return true
	}
	return false
}

// Scope is the material a recipe applies to.
type Scope string

// The declarable scopes: one session, a multi-session corpus, or a repository
// snapshot.
const (
	ScopeSession    Scope = "session"
	ScopeCorpus     Scope = "corpus"
	ScopeRepository Scope = "repository"
)

// Known reports whether s is a documented scope.
func (s Scope) Known() bool {
	switch s {
	case ScopeSession, ScopeCorpus, ScopeRepository:
		return true
	}
	return false
}

// Stage is a phase of exploration a recipe participates in (§5.4).
type Stage string

// The declarable stages. They match §5.4's separation of exploration from the
// logically separate challenger and the synthesizer that judges both.
const (
	StageInvestigate Stage = "investigate"
	StageChallenge   Stage = "challenge"
	StageSynthesize  Stage = "synthesize"
)

// Known reports whether s is a documented stage.
func (s Stage) Known() bool {
	switch s {
	case StageInvestigate, StageChallenge, StageSynthesize:
		return true
	}
	return false
}

// requiredSections is the body structure §5.1 mandates, in the order §5.1
// lists it. Order is enforced as well as presence: every recipe reads the
// same way, and a prompt builder can rely on the shape instead of searching
// for headings.
var requiredSections = []string{
	"Question",
	"Inclusion, exclusion, and ambiguity",
	"Sorting cues",
	"Evidence and counter-evidence",
	"Temporal and present-reality checks",
	"Classifications and stopping conditions",
	"Cross-session synthesis keys",
	"Capability needs",
	"Known failure modes",
	"Examples",
}

// RequiredSections returns the level-2 body headings every recipe must carry,
// in required order. It is exported for recipe authoring and review tooling.
func RequiredSections() []string {
	return append([]string(nil), requiredSections...)
}

// Section is one level-2 body section of a recipe, kept from validation so a
// consumer that needs the guidance under one heading does not re-parse
// Markdown to find it.
type Section struct {
	Title string
	Text  string
}

// Recipe is one loaded, validated cookbook asset.
//
// Digest covers everything a run's behaviour depends on except Version
// itself — the body plus the semantic front-matter fields — which is what
// makes §5.1's rule checkable: a changed Digest under an unchanged Version is
// exactly "semantic behaviour changed without a version increment".
type Recipe struct {
	ID           string
	Version      int
	Kind         Kind
	Scope        []Scope
	Stages       []Stage
	Capabilities []worker.Capability

	// Default reports whether the recipe is enabled without the operator
	// naming it. §5.5 default-enables five lenses and ships three as
	// reviewable drafts; a draft is simply a recipe that is not default.
	Default bool

	// Title is the body's level-1 heading, the human name of the recipe.
	Title string

	// Path is the recipe's location inside the asset tree it was loaded
	// from, so an error can be traced to a file.
	Path string

	// Body is the Markdown following the front matter, verbatim. It is the
	// guidance itself and is never rewritten here.
	Body string

	Sections []Section
	Digest   digest.Digest
}

// Ref returns the id/version pair a run receipt records (§7). The pair, not
// the id, is the identity of the guidance a run actually used.
func (r *Recipe) Ref() worker.RecipeRef {
	return worker.RecipeRef{ID: r.ID, Version: r.Version}
}

// Section returns the text under one level-2 heading.
func (r *Recipe) Section(title string) (string, bool) {
	for _, s := range r.Sections {
		if s.Title == title {
			return s.Text, true
		}
	}
	return "", false
}

// HasScope reports whether the recipe applies to s.
func (r *Recipe) HasScope(s Scope) bool {
	for _, have := range r.Scope {
		if have == s {
			return true
		}
	}
	return false
}

// HasStage reports whether the recipe participates in s.
func (r *Recipe) HasStage(s Stage) bool {
	for _, have := range r.Stages {
		if have == s {
			return true
		}
	}
	return false
}

// Set is a loaded cookbook: every recipe of one asset tree, validated
// together. Recipes are ordered by id so every listing, manifest, and receipt
// derived from a Set is stable.
type Set struct {
	recipes []*Recipe
	byID    map[string]*Recipe
}

// recipesDir is the asset-tree directory holding recipe documents.
const recipesDir = "recipes"

// Embedded loads the cookbook compiled into this binary. It is the normal
// entry point: a run's guidance then comes from the build's own audited
// assets rather than from whatever happens to sit on disk.
func Embedded() (*Set, error) {
	return Load(cookbookassets.Assets())
}

// LoadDir loads a cookbook from a directory, which is how a recipe is edited
// and reviewed without rebuilding the binary.
func LoadDir(dir string) (*Set, error) {
	return Load(os.DirFS(dir))
}

// Load reads and validates every recipe in fsys.
//
// Ids are unique by construction rather than by a separate scan: a recipe's
// file name must be its id, and a directory cannot hold two files of one name.
//
// Validation is all-or-nothing by design: a cookbook with one invalid recipe
// is not a cookbook missing one recipe, because a run receipt that recorded a
// silently reduced recipe set would misdescribe the analysis it produced.
func Load(fsys fs.FS) (*Set, error) {
	entries, err := fs.ReadDir(fsys, recipesDir)
	if err != nil {
		return nil, fmt.Errorf("read cookbook recipes: %w", err)
	}

	set := &Set{byID: make(map[string]*Recipe, len(entries))}
	for _, entry := range entries {
		name := path.Join(recipesDir, entry.Name())
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil, fmt.Errorf("cookbook: %s is not a recipe document; the %s directory holds only .md recipes", name, recipesDir)
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read recipe %s: %w", name, err)
		}
		recipe, err := ParseRecipe(name, data)
		if err != nil {
			return nil, err
		}
		if want := strings.TrimSuffix(entry.Name(), ".md"); recipe.ID != want {
			return nil, fmt.Errorf("cookbook: recipe %s declares id %q; the file name must be the id", name, recipe.ID)
		}
		set.byID[recipe.ID] = recipe
		set.recipes = append(set.recipes, recipe)
	}
	if len(set.recipes) == 0 {
		return nil, errors.New("cookbook: no recipes found")
	}
	sort.Slice(set.recipes, func(i, j int) bool { return set.recipes[i].ID < set.recipes[j].ID })
	return set, nil
}

// All returns every recipe, ordered by id.
func (s *Set) All() []*Recipe {
	return append([]*Recipe(nil), s.recipes...)
}

// ByID returns one recipe.
func (s *Set) ByID(id string) (*Recipe, bool) {
	r, ok := s.byID[id]
	return r, ok
}

// Defaults returns the recipes enabled without the operator naming one.
func (s *Set) Defaults() []*Recipe {
	var out []*Recipe
	for _, r := range s.recipes {
		if r.Default {
			out = append(out, r)
		}
	}
	return out
}

// IDs returns every recipe id, ordered by id. It exists so a caller
// rejecting an unknown selection can name the ids that do exist without
// copying every recipe to read one field.
func (s *Set) IDs() []string {
	ids := make([]string, 0, len(s.recipes))
	for _, r := range s.recipes {
		ids = append(ids, r.ID)
	}
	return ids
}

// UnknownRecipeError reports a selection naming a recipe this cookbook does
// not hold. It carries the available ids because the caller that reports it
// is a command line: an operator who mistyped an id, or who is holding a
// recipe name from a different build, needs the list and not just the
// rejection.
type UnknownRecipeError struct {
	ID        string
	Available []string
}

func (e *UnknownRecipeError) Error() string {
	return fmt.Sprintf("cookbook: no recipe %q; the cookbook holds %s",
		e.ID, strings.Join(e.Available, " "))
}

// Select returns the subset of the cookbook holding exactly the named
// recipes, ordered by id like any other Set. Duplicate ids collapse: naming
// a recipe twice is a repeated flag, not a request to run it twice.
//
// Narrowing belongs here, once, rather than at each reader of a Set. A run
// derives several things from the Set it was given — the cookbook assets its
// receipt attests to, the recipes each stage runs — and every one of them
// reads the whole Set. A Set that still held recipes the operator did not
// ask for would therefore make the receipt overstate what was analyzed,
// which is the one claim a provenance record may never make.
//
// The subset is filtered rather than re-parsed because every check this
// package makes is either per-recipe — already passed, on the very same
// *Recipe values — or an invariant a subset inherits: ids stay unique and
// the order stays sorted. The single property a subset can lose is being
// non-empty, so an empty selection is an error here rather than a run that
// analyzes nothing and writes a receipt for it.
func (s *Set) Select(ids []string) (*Set, error) {
	out := &Set{byID: make(map[string]*Recipe, len(ids))}
	for _, id := range ids {
		r, ok := s.byID[id]
		if !ok {
			return nil, &UnknownRecipeError{ID: id, Available: s.IDs()}
		}
		if _, dup := out.byID[id]; dup {
			continue
		}
		out.byID[id] = r
		out.recipes = append(out.recipes, r)
	}
	if len(out.recipes) == 0 {
		return nil, errors.New("cookbook: no recipes selected")
	}
	sort.Slice(out.recipes, func(i, j int) bool { return out.recipes[i].ID < out.recipes[j].ID })
	return out, nil
}

// Refs returns the id/version pairs of every recipe, for a run receipt.
func (s *Set) Refs() []worker.RecipeRef {
	refs := make([]worker.RecipeRef, 0, len(s.recipes))
	for _, r := range s.recipes {
		refs = append(refs, r.Ref())
	}
	return refs
}

// ParseRecipe parses and validates one recipe document. It is exported so a
// review or authoring tool can check a candidate recipe before it is placed
// in the asset tree.
func ParseRecipe(name string, data []byte) (*Recipe, error) {
	fm, body, bodyLine, err := parseFrontMatter(name, data)
	if err != nil {
		return nil, err
	}
	title, sections, err := parseBody(name, body, bodyLine)
	if err != nil {
		return nil, err
	}
	if mentions := VendorMentions(body); len(mentions) > 0 {
		return nil, fmt.Errorf("cookbook: recipe %s names %s; §5.1 forbids a recipe from selecting a provider or model",
			name, strings.Join(mentions, ", "))
	}

	r := &Recipe{
		ID:           fm.id,
		Version:      fm.version,
		Kind:         fm.kind,
		Scope:        fm.scope,
		Stages:       fm.stages,
		Capabilities: fm.capabilities,
		Default:      fm.enabled,
		Title:        title,
		Path:         name,
		Body:         body,
		Sections:     sections,
	}
	r.Digest = contentDigest(r)
	return r, nil
}

// parseBody validates the required §5.1 structure and returns the level-1
// title with every level-2 section. bodyLine is the file line the body starts
// on, so a structural complaint names the line in the file rather than an
// offset into the body.
func parseBody(name, body string, bodyLine int) (string, []Section, error) {
	lines := strings.Split(body, "\n")
	num := func(i int) int { return bodyLine + i }

	title := ""
	var sections []Section
	var current *Section
	var text strings.Builder
	closeSection := func() {
		if current != nil {
			current.Text = strings.TrimSpace(text.String())
			sections = append(sections, *current)
			text.Reset()
		}
	}

	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "# "):
			if title != "" {
				return "", nil, parseErrorf(name, num(i), "body has a second level-1 heading; a recipe has one title")
			}
			if len(sections) > 0 || current != nil {
				return "", nil, parseErrorf(name, num(i), "body's level-1 title must precede every section")
			}
			title = strings.TrimSpace(strings.TrimPrefix(line, "# "))
		case strings.HasPrefix(line, "## "):
			closeSection()
			heading := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			index := len(sections)
			if index >= len(requiredSections) {
				return "", nil, parseErrorf(name, num(i),
					"body has more sections than the %d §5.1 requires, starting with %q", len(requiredSections), heading)
			}
			if want := requiredSections[index]; heading != want {
				return "", nil, parseErrorf(name, num(i), "body section %d must be %q, found %q", index+1, want, heading)
			}
			current = &Section{Title: heading}
		default:
			if current == nil {
				if title == "" && strings.TrimSpace(line) != "" {
					return "", nil, parseErrorf(name, num(i),
						"body must open with a level-1 title, found %q", strings.TrimSpace(line))
				}
				continue
			}
			text.WriteString(line)
			text.WriteString("\n")
		}
	}
	closeSection()

	if title == "" {
		return "", nil, parseErrorf(name, bodyLine, "body has no level-1 title")
	}
	if len(sections) < len(requiredSections) {
		return "", nil, parseErrorf(name, bodyLine+len(lines)-1,
			"body is missing required section %q", requiredSections[len(sections)])
	}
	for i, s := range sections {
		if s.Text == "" {
			return "", nil, parseErrorf(name, bodyLine, "body section %q is empty", requiredSections[i])
		}
	}
	return title, sections, nil
}

// contentDigest hashes the recipe's semantics: the front-matter fields that
// change how a run behaves, then the body. Version is excluded on purpose —
// including it would make every version increment look like a content change
// and destroy the drift check's only signal.
func contentDigest(r *Recipe) digest.Digest {
	var b bytes.Buffer
	fmt.Fprintf(&b, "id:%s\n", r.ID)
	fmt.Fprintf(&b, "kind:%s\n", r.Kind)
	fmt.Fprintf(&b, "scope:%s\n", joinValues(r.Scope))
	fmt.Fprintf(&b, "stages:%s\n", joinValues(r.Stages))
	fmt.Fprintf(&b, "capabilities:%s\n", joinValues(r.Capabilities))
	fmt.Fprintf(&b, "default:%t\n", r.Default)
	b.WriteString(r.Body)
	return digest.Bytes(b.Bytes())
}

func joinValues[T ~string](values []T) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = string(v)
	}
	return strings.Join(parts, ",")
}

func joinKinds() string {
	return joinValues([]Kind{KindPolicy, KindLens, KindMeta})
}

func joinScopes() string {
	return joinValues([]Scope{ScopeSession, ScopeCorpus, ScopeRepository})
}

func joinStages() string {
	return joinValues([]Stage{StageInvestigate, StageChallenge, StageSynthesize})
}

func joinCapabilities() string {
	return joinValues([]worker.Capability{
		worker.CapabilityCorpusSearch,
		worker.CapabilityRepoRead,
		worker.CapabilitySandboxExec,
		worker.CapabilityPublicResearch,
	})
}

// vendorDenylist is the documented set of provider, model, and vendor names a
// recipe may never contain. §5.1 forbids a recipe from selecting a provider or
// model — Code owns that choice (§2.6) — and a prohibition nobody checks
// erodes one helpful-sounding sentence at a time. The list is deliberately
// broader than "names a model we support": guidance that reasons about a
// specific vendor's behaviour is already outside the recipe's business, and a
// recipe that must discuss a runtime says "the harness" or "the model".
//
// Matching is case-insensitive on word boundaries. Widening this list is a
// deliberate edit; a recipe that needs an entry removed is a review question,
// not a load-time exception.
var vendorDenylist = []string{
	"anthropic", "openai", "chatgpt", "gpt", "claude", "sonnet", "opus", "haiku",
	"codex", "copilot", "cursor", "gemini", "bard", "palm", "deepmind",
	"llama", "mistral", "mixtral", "deepseek", "qwen", "kimi", "grok", "xai",
	"groq", "cohere", "ollama", "vllm", "bedrock", "sagemaker", "openrouter",
	"vertex ai", "azure", "huggingface", "hugging face", "perplexity",
}

var vendorPattern = regexp.MustCompile(`(?i)\b(?:` + strings.Join(vendorDenylist, "|") + `)\b`)

// VendorMentions reports the denylisted provider, model, or vendor names found
// in a recipe body, lowercased and deduplicated. An empty result is the
// condition every recipe must satisfy to load.
func VendorMentions(body string) []string {
	var found []string
	for _, m := range vendorPattern.FindAllString(body, -1) {
		m = strings.ToLower(m)
		if !containsString(found, m) {
			found = append(found, m)
		}
	}
	sort.Strings(found)
	return found
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
