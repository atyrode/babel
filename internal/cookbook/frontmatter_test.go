package cookbook

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
)

// sampleFrontMatter is a valid front matter in canonical order. Tests build a
// malformed document by replacing or removing one of these lines, so every
// expected line number below is stable and readable: line 1 is the opening
// fence and line N+1 is the Nth key.
var sampleFrontMatter = []string{
	"id: sample",
	"version: 1",
	"kind: lens",
	"scope: [session, corpus]",
	"stages: [investigate, synthesize]",
	"capabilities: [corpus-search]",
	"default: true",
}

// sampleDoc renders a recipe document whose front matter is the canonical one
// with overrides applied by key, and whose body is body. An override with an
// empty value removes the key.
func sampleDoc(overrides []string, body string) []byte {
	lines := append([]string(nil), sampleFrontMatter...)
	for _, override := range overrides {
		key, _, _ := strings.Cut(override, ":")
		replaced := false
		for i, line := range lines {
			if have, _, _ := strings.Cut(line, ":"); have != key {
				continue
			}
			if override == key+":" {
				lines = append(lines[:i:i], lines[i+1:]...)
			} else {
				lines[i] = override
			}
			replaced = true
			break
		}
		if !replaced {
			lines = append(lines, override)
		}
	}
	return []byte("---\n" + strings.Join(lines, "\n") + "\n---\n" + body)
}

// sampleBody renders a body carrying every required section with enough text
// to be accepted as guidance.
func sampleBody(title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", title)
	for _, section := range requiredSections {
		fmt.Fprintf(&b, "\n## %s\n\n%s guidance.\n", section, section)
	}
	return b.String()
}

// mapFS renders an in-memory asset tree. Tests never need a real cookbook
// directory, so nothing here depends on the repository layout.
func mapFS(files map[string]string) fstest.MapFS {
	fsys := make(fstest.MapFS, len(files))
	for name, content := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return fsys
}

// TestFrontMatterRejections is the grammar's contract. Every case is a
// construct the documented subset does not contain, and every case must fail
// with the offending line, because the grammar is narrow enough that most
// authoring mistakes are grammar mistakes and an error without a line number
// sends the author reading the whole document.
func TestFrontMatterRejections(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		line int
		want string
	}{
		{
			name: "unknown key",
			doc:  "---\nid: sample\nlens: true\n---\n",
			line: 3,
			want: `unknown front-matter key "lens"`,
		},
		{
			name: "duplicate key",
			doc:  "---\nid: sample\nversion: 1\nid: other\n---\n",
			line: 4,
			want: `duplicate front-matter key "id", first set on line 2`,
		},
		{
			name: "wrong type: integer expected",
			doc:  "---\nid: sample\nversion: one\n---\n",
			line: 3,
			want: `"one" is not a positive integer`,
		},
		{
			name: "wrong type: list expected",
			doc:  "---\nid: sample\nscope: session\n---\n",
			line: 3,
			want: `"session" is not a flow-style list`,
		},
		{
			name: "wrong type: boolean expected",
			doc:  "---\nid: sample\ndefault: yes\n---\n",
			line: 3,
			want: `default must be true or false, not "yes"`,
		},
		{
			name: "unclosed list",
			doc:  "---\nid: sample\nscope: [session, corpus\n---\n",
			line: 3,
			want: `is not closed with "]"`,
		},
		{
			name: "value outside the kind enum",
			doc:  "---\nid: sample\nkind: oracle\n---\n",
			line: 3,
			want: `kind "oracle" is not one of policy,lens,meta`,
		},
		{
			name: "value outside the scope enum",
			doc:  "---\nid: sample\nscope: [session, fleet]\n---\n",
			line: 3,
			want: `scope "fleet" is not one of`,
		},
		{
			name: "value outside the stage enum",
			doc:  "---\nid: sample\nstages: [ponder]\n---\n",
			line: 3,
			want: `stage "ponder" is not one of`,
		},
		{
			name: "capability Babel does not define",
			doc:  "---\nid: sample\ncapabilities: [network-access]\n---\n",
			line: 3,
			want: `capability "network-access" is not one Babel defines`,
		},
		{
			name: "tab after the colon",
			doc:  "---\nid: sample\nkind:\tlens\n---\n",
			line: 3,
			want: "allows no tab character",
		},
		{
			name: "tab as indentation",
			doc:  "---\nid: sample\n\tkind: lens\n---\n",
			line: 3,
			want: "allows no tab character",
		},
		{
			name: "space indentation",
			doc:  "---\nid: sample\n  kind: lens\n---\n",
			line: 3,
			want: "allows no indentation",
		},
		{
			name: "trailing space",
			doc:  "---\nid: sample\nkind: lens \n---\n",
			line: 3,
			want: "allows no trailing space",
		},
		{
			name: "two spaces after the colon",
			doc:  "---\nid: sample\nkind:  lens\n---\n",
			line: 3,
			want: `exactly one space must follow the colon of key "kind"`,
		},
		{
			name: "blank line",
			doc:  "---\nid: sample\n\nkind: lens\n---\n",
			line: 3,
			want: "allows no blank line",
		},
		{
			name: "comment",
			doc:  "---\nid: sample\n#kind: lens\n---\n",
			line: 3,
			want: "allows no comment",
		},
		{
			name: "carriage return",
			doc:  "---\nid: sample\nkind: lens\r\n---\n",
			line: 3,
			want: "allows no carriage return",
		},
		{
			name: "not a key: value pair",
			doc:  "---\nid: sample\nlens\n---\n",
			line: 3,
			want: `line is not a "key: value" pair`,
		},
		{
			name: "missing terminator",
			doc:  "---\nid: sample\nversion: 1\n",
			line: 3,
			want: `front matter is never closed by a "---" line`,
		},
		{
			name: "missing front matter",
			doc:  "# Recipe\n\nText.\n",
			line: 1,
			want: `must begin with a "---" front-matter line`,
		},
		{
			name: "empty document",
			doc:  "",
			line: 1,
			want: `must begin with a "---" front-matter line`,
		},
		{
			name: "missing required key",
			doc:  "---\nid: sample\nversion: 1\nkind: lens\nscope: [session]\nstages: [investigate]\ndefault: false\n---\n",
			line: 8,
			want: `missing required key "capabilities"`,
		},
		{
			name: "empty scope list",
			doc:  "---\nid: sample\nversion: 1\nkind: lens\nscope: []\nstages: [investigate]\ncapabilities: []\ndefault: false\n---\n",
			line: 5,
			want: "at least one scope",
		},
		{
			name: "empty stage list",
			doc:  "---\nid: sample\nversion: 1\nkind: lens\nscope: [session]\nstages: []\ncapabilities: []\ndefault: false\n---\n",
			line: 6,
			want: "at least one stage",
		},
		{
			name: "repeated list item",
			doc:  "---\nid: sample\nscope: [session, session]\n---\n",
			line: 3,
			want: `repeats item "session"`,
		},
		{
			name: "quoted list item",
			doc:  "---\nid: sample\nscope: [\"session\"]\n---\n",
			line: 3,
			want: "is not a bare token",
		},
		{
			name: "trailing comma in a list",
			doc:  "---\nid: sample\nscope: [session,]\n---\n",
			line: 3,
			want: "has an empty item",
		},
		{
			name: "version with a leading zero",
			doc:  "---\nid: sample\nversion: 01\n---\n",
			line: 3,
			want: "has a leading zero",
		},
		{
			name: "id outside the identifier form",
			doc:  "---\nid: Sample Recipe\n---\n",
			line: 2,
			want: "must be lowercase letters, digits, and single interior hyphens",
		},
		{
			name: "default true on a non-lens",
			doc:  "---\nid: sample\nversion: 1\nkind: meta\nscope: [session]\nstages: [investigate]\ncapabilities: []\ndefault: true\n---\n",
			line: 8,
			want: "only a lens may be default-enabled",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRecipe("recipes/sample.md", []byte(tc.doc))
			if err == nil {
				t.Fatal("ParseRecipe succeeded, want a grammar error")
			}
			var perr *ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("error %v is not a *ParseError", err)
			}
			if perr.Line != tc.line {
				t.Errorf("error names line %d, want line %d (%v)", perr.Line, tc.line, err)
			}
			if !strings.Contains(perr.Msg, tc.want) {
				t.Errorf("message = %q, want it to contain %q", perr.Msg, tc.want)
			}
			if !strings.HasPrefix(err.Error(), fmt.Sprintf("recipes/sample.md:%d: ", tc.line)) {
				t.Errorf("error %q does not open with path:line", err)
			}
		})
	}
}

// TestBodyRejections covers the §5.1 body contract: the required sections, in
// order, each with content, under one title.
func TestBodyRejections(t *testing.T) {
	full := sampleBody("Sample")

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing the last required section",
			body: strings.Replace(full, "\n## Examples\n\nExamples guidance.\n", "", 1),
			want: `missing required section "Examples"`,
		},
		{
			name: "missing an interior section",
			body: strings.Replace(full, "\n## Sorting cues\n\nSorting cues guidance.\n", "", 1),
			want: `body section 3 must be "Sorting cues", found "Evidence and counter-evidence"`,
		},
		{
			name: "sections out of order",
			body: strings.Replace(full, "## Question", "## Examples", 1),
			want: `body section 1 must be "Question", found "Examples"`,
		},
		{
			name: "unknown section",
			body: full + "\n## Appendix\n\nExtra.\n",
			want: "more sections than the 10 §5.1 requires",
		},
		{
			name: "empty section",
			body: strings.Replace(full, "Examples guidance.", "", 1),
			want: `body section "Examples" is empty`,
		},
		{
			name: "no title",
			body: strings.Replace(full, "# Sample\n", "", 1),
			want: "body has no level-1 title",
		},
		{
			name: "text before the title",
			body: "Preamble before the title.\n" + full,
			want: "must open with a level-1 title",
		},
		{
			name: "second title",
			body: full + "\n# Another recipe\n",
			want: "second level-1 heading",
		},
		{
			name: "title after a section",
			body: "## Question\n\nText.\n\n# Sample\n",
			want: "must precede every section",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseRecipe("recipes/sample.md", sampleDoc(nil, tc.body))
			if err == nil {
				t.Fatal("ParseRecipe succeeded, want a body-structure error")
			}
			var perr *ParseError
			if !errors.As(err, &perr) {
				t.Fatalf("error %v is not a *ParseError", err)
			}
			if !strings.Contains(perr.Msg, tc.want) {
				t.Errorf("message = %q, want it to contain %q", perr.Msg, tc.want)
			}
			// Front matter occupies lines 1 to 9, so a body complaint must
			// name a line inside the body.
			if perr.Line < 10 {
				t.Errorf("error names line %d, which is inside the front matter", perr.Line)
			}
		})
	}
}

// TestValidDocumentParses pins what the grammar accepts, including the forms a
// stricter reading might reject: a list without spaces after its commas, an
// empty capability list, and preamble text between the title and the first
// section.
func TestValidDocumentParses(t *testing.T) {
	body := strings.Replace(sampleBody("Sample"), "# Sample\n",
		"# Sample\n\n> **Draft.** Not default-enabled.\n", 1)
	doc := sampleDoc([]string{
		"scope: [session,corpus,repository]",
		"capabilities: []",
		"default: false",
		"version: 4",
	}, body)

	r, err := ParseRecipe("recipes/sample.md", doc)
	if err != nil {
		t.Fatalf("ParseRecipe = %v", err)
	}
	if r.ID != "sample" || r.Version != 4 || r.Kind != KindLens {
		t.Errorf("parsed id/version/kind = %q/%d/%q", r.ID, r.Version, r.Kind)
	}
	if len(r.Scope) != 3 || !r.HasScope(ScopeRepository) {
		t.Errorf("scope = %v, want all three", r.Scope)
	}
	if !r.HasStage(StageInvestigate) || r.HasStage(StageChallenge) {
		t.Errorf("stages = %v, want investigate and synthesize", r.Stages)
	}
	if len(r.Capabilities) != 0 {
		t.Errorf("capabilities = %v, want none", r.Capabilities)
	}
	if r.Default {
		t.Error("default = true, want false")
	}
	if r.Title != "Sample" {
		t.Errorf("title = %q", r.Title)
	}
	if !strings.Contains(r.Body, "Draft.") {
		t.Error("body dropped the preamble between the title and the first section")
	}
}
