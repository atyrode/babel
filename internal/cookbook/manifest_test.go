package cookbook

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cookbookassets "github.com/atyrode/babel/cookbook"
)

// assetDir is the committed asset tree, relative to this package. It is used
// only to regenerate the version record under -update; every assertion below
// reads the embedded copy, so the tests do not depend on the repository
// layout.
const assetDir = "../../cookbook"

// update regenerates the committed version record. It is the authoring step
// that follows a deliberate recipe-version increment:
//
//	go test ./internal/cookbook -run TestCommittedVersionRecord -update
var update = flag.Bool("update", false, "regenerate the committed cookbook version record")

// TestCommittedVersionRecord is what makes SPEC.md §5.1's versioning rule
// real: the committed record must describe the shipped recipes exactly, so any
// later edit to a body shows up as drift instead of passing unnoticed.
func TestCommittedVersionRecord(t *testing.T) {
	if *update {
		if err := WriteManifest(assetDir); err != nil {
			t.Fatalf("WriteManifest(%s) = %v", assetDir, err)
		}
		t.Logf("regenerated %s", filepath.Join(assetDir, manifestFile))
	}

	drifts, err := Verify(cookbookassets.Assets())
	if err != nil {
		t.Fatalf("Verify() = %v", err)
	}
	for _, d := range drifts {
		t.Errorf("drift: %s", d)
	}
	if len(drifts) > 0 {
		t.Log("regenerate with: go test ./internal/cookbook -run TestCommittedVersionRecord -update")
	}

	set := loadEmbedded(t)
	want, err := NewManifest(set).Canonical()
	if err != nil {
		t.Fatalf("Canonical() = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(assetDir, manifestFile))
	if err != nil {
		t.Fatalf("read committed record: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("committed %s is not canonical; regenerate it with -update", manifestFile)
	}
}

// TestDriftDetection is the drift check's contract. The case that matters is
// "undeclared": a changed body under an unchanged version. The others exist
// because each of them would otherwise blind the check to a later undeclared
// change, and a check that can be blinded is not a rule.
func TestDriftDetection(t *testing.T) {
	original := sampleDoc(nil, sampleBody("Sample"))
	base, err := ParseRecipe("recipes/sample.md", original)
	if err != nil {
		t.Fatalf("ParseRecipe = %v", err)
	}

	// The recorded state: the planted statement, and one recipe at version 1
	// with its current digest.
	record := func(entries ...ManifestEntry) string {
		m := Manifest{Schema: manifestSchema, Preamble: defaultPreambleEntry(t), Recipes: entries}
		data, err := m.Canonical()
		if err != nil {
			t.Fatalf("Canonical() = %v", err)
		}
		return string(data)
	}
	current := ManifestEntry{ID: base.ID, Version: base.Version, Digest: base.Digest}

	// An edited body, and the same edit with the version incremented.
	editedBody := strings.Replace(sampleBody("Sample"),
		"Question guidance.", "Question guidance, materially revised.", 1)
	edited := sampleDoc(nil, editedBody)
	editedV2 := sampleDoc([]string{"version: 2"}, editedBody)
	// A front-matter change with an untouched body: scope is part of what a
	// run does, so it must move the digest too.
	rescoped := sampleDoc([]string{"scope: [session]"}, sampleBody("Sample"))

	tests := []struct {
		name   string
		doc    []byte
		record string
		want   []DriftKind
		detail string
	}{
		{
			name:   "committed state has no drift",
			doc:    original,
			record: record(current),
			want:   nil,
		},
		{
			name:   "body changed without a version increment",
			doc:    edited,
			record: record(current),
			want:   []DriftKind{DriftUndeclared},
			detail: "§5.1 requires an increment",
		},
		{
			name:   "front matter changed without a version increment",
			doc:    rescoped,
			record: record(current),
			want:   []DriftKind{DriftUndeclared},
			detail: "§5.1 requires an increment",
		},
		{
			name:   "body changed with a version increment",
			doc:    editedV2,
			record: record(current),
			want:   []DriftKind{DriftStale},
			detail: "does not have",
		},
		{
			name:   "version increment with no content change",
			doc:    sampleDoc([]string{"version: 2"}, sampleBody("Sample")),
			record: record(current),
			want:   []DriftKind{DriftStale},
			detail: "no content change to declare",
		},
		{
			name:   "version regressed",
			doc:    edited,
			record: record(ManifestEntry{ID: current.ID, Version: 7, Digest: current.Digest}),
			want:   []DriftKind{DriftRegressed},
			detail: "below the recorded 7",
		},
		{
			name:   "recipe absent from the record",
			doc:    original,
			record: record(),
			want:   []DriftKind{DriftUnrecorded},
			detail: "regenerate the version record",
		},
		{
			name: "recorded recipe that no longer exists",
			doc:  original,
			record: record(current, ManifestEntry{
				ID: "retired", Version: 3, Digest: current.Digest,
			}),
			want:   []DriftKind{DriftDropped},
			detail: "the recipe is gone",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fsys := mapFS(map[string]string{
				"recipes/sample.md": string(tc.doc),
				manifestFile:        tc.record,
			})
			drifts, err := Verify(fsys)
			if err != nil {
				t.Fatalf("Verify() = %v", err)
			}
			var kinds []DriftKind
			for _, d := range drifts {
				kinds = append(kinds, d.Kind)
			}
			if len(kinds) != len(tc.want) {
				t.Fatalf("drifts = %v, want %v", drifts, tc.want)
			}
			for i, want := range tc.want {
				if kinds[i] != want {
					t.Errorf("drift %d = %q, want %q", i, kinds[i], want)
				}
			}
			if tc.detail != "" && !strings.Contains(drifts[0].Detail, tc.detail) {
				t.Errorf("detail = %q, want it to contain %q", drifts[0].Detail, tc.detail)
			}
		})
	}
}

// TestManifestRejections covers the record's own integrity. A record this
// build only partly understands cannot be trusted to detect drift, so every
// case is a load failure rather than a tolerated oddity.
func TestManifestRejections(t *testing.T) {
	sha := "sha256:" + strings.Repeat("a", 64)
	preamble := `"preamble":{"version":1,"digest":"` + sha + `"},`

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "unknown field",
			content: `{"manifest_schema":2,` + preamble + `"recipes":[],"notes":"extra"}`,
			want:    "unknown field",
		},
		{
			name:    "wrong schema",
			content: `{"manifest_schema":1,` + preamble + `"recipes":[]}`,
			want:    "manifest schema 1",
		},
		{
			name:    "no statement recorded",
			content: `{"manifest_schema":2,"recipes":[]}`,
			want:    "want at least 1",
		},
		{
			name:    "statement with a malformed digest",
			content: `{"manifest_schema":2,"preamble":{"version":1,"digest":"abc"},"recipes":[]}`,
			want:    "malformed digest",
		},
		{
			name:    "a recipe recorded under the statement's id",
			content: `{"manifest_schema":2,` + preamble + `"recipes":[{"id":"preamble","version":1,"digest":"` + sha + `"}]}`,
			want:    `id "preamble"`,
		},
		{
			name:    "malformed digest",
			content: `{"manifest_schema":2,` + preamble + `"recipes":[{"id":"sample","version":1,"digest":"abc"}]}`,
			want:    "malformed digest",
		},
		{
			name:    "invalid id",
			content: `{"manifest_schema":2,` + preamble + `"recipes":[{"id":"Sample","version":1,"digest":"` + sha + `"}]}`,
			want:    "invalid id",
		},
		{
			name:    "version below one",
			content: `{"manifest_schema":2,` + preamble + `"recipes":[{"id":"sample","version":0,"digest":"` + sha + `"}]}`,
			want:    "want at least 1",
		},
		{
			name:    "not json",
			content: "manifest_schema: 2\n",
			want:    "decode cookbook",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadManifest(mapFS(map[string]string{manifestFile: tc.content}))
			if err == nil {
				t.Fatal("LoadManifest succeeded, want an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestWriteManifestRoundTrips covers the authoring path end to end on a
// throwaway tree: write the record, and the drift check on that tree passes.
func TestWriteManifestRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, recipesDir), 0o755); err != nil {
		t.Fatalf("create recipe directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, preambleFile), defaultPreamble, 0o644); err != nil {
		t.Fatalf("write statement: %v", err)
	}
	path := filepath.Join(dir, recipesDir, "sample.md")
	if err := os.WriteFile(path, sampleDoc(nil, sampleBody("Sample")), 0o644); err != nil {
		t.Fatalf("write recipe: %v", err)
	}

	if err := WriteManifest(dir); err != nil {
		t.Fatalf("WriteManifest = %v", err)
	}
	drifts, err := Verify(os.DirFS(dir))
	if err != nil {
		t.Fatalf("Verify = %v", err)
	}
	if len(drifts) > 0 {
		t.Fatalf("a freshly written record reports drift: %v", drifts)
	}

	// Editing the body without touching the version is the violation the
	// whole mechanism exists to catch.
	edited := strings.Replace(sampleBody("Sample"), "Examples guidance.", "Examples guidance, revised.", 1)
	if err := os.WriteFile(path, sampleDoc(nil, edited), 0o644); err != nil {
		t.Fatalf("rewrite recipe: %v", err)
	}
	drifts, err = Verify(os.DirFS(dir))
	if err != nil {
		t.Fatalf("Verify = %v", err)
	}
	if len(drifts) != 1 || drifts[0].Kind != DriftUndeclared {
		t.Fatalf("drifts = %v, want one %q", drifts, DriftUndeclared)
	}
}
