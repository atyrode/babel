package cli

import (
	"context"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/title"
)

// TestProvenancePtrRefusesToAttributeNothing pins the pairing that makes the
// whole feature readable. A provenance without a title names the origin of
// nothing; a title without one lets Babel's derivation pass for the harness's
// own record. Both are rendered as absent rather than guessed at.
func TestProvenancePtrRefusesToAttributeNothing(t *testing.T) {
	tests := []struct {
		name string
		meta adapter.CommonMeta
		want *string
	}{
		{
			name: "recorded title keeps its provenance",
			meta: adapter.CommonMeta{Title: new("a recorded title"), TitleProvenance: adapter.TitleRecorded},
			want: new("recorded"),
		},
		{
			name: "derived title keeps its provenance",
			meta: adapter.CommonMeta{Title: new("a derived title"), TitleProvenance: adapter.TitleDerived},
			want: new("derived"),
		},
		{
			name: "no title means no provenance to report",
			meta: adapter.CommonMeta{TitleProvenance: adapter.TitleDerived},
			want: nil,
		},
		{
			name: "a title with no provenance is not promoted to recorded",
			meta: adapter.CommonMeta{Title: new("a title from nowhere")},
			want: nil,
		},
		{
			name: "a title with an unknown provenance is not passed through",
			meta: adapter.CommonMeta{Title: new("t"), TitleProvenance: adapter.TitleProvenance("guessed")},
			want: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := provenancePtr(tc.meta)
			switch {
			case tc.want == nil && got != nil:
				t.Errorf("provenance = %q, want none", *got)
			case tc.want != nil && got == nil:
				t.Errorf("provenance = none, want %q", *tc.want)
			case tc.want != nil && *got != *tc.want:
				t.Errorf("provenance = %q, want %q", *got, *tc.want)
			}
		})
	}
}

// TestInferredOverlayNeverOverwritesARecordedTitle is the guard that keeps a
// paid guess from displacing a fact. `title infer` already refuses to offer
// such a session, so this store state should be unreachable — and the overlay
// checks anyway, because a rule enforced at one end of a pipeline stops holding
// the day someone adds a second writer.
func TestInferredOverlayNeverOverwritesARecordedTitle(t *testing.T) {
	overlay := inferredOverlay{
		"omp/one":   {Selector: "omp/one", Title: "a model's guess"},
		"codex/two": {Selector: "codex/two", Title: "a model's summary"},
	}

	gotTitle, gotProv := overlay.apply("omp/one", new("the harness wrote this"), new("recorded"))
	if *gotTitle != "the harness wrote this" || *gotProv != "recorded" {
		t.Errorf("a recorded title was overlaid: %q/%q", *gotTitle, *gotProv)
	}

	gotTitle, gotProv = overlay.apply("codex/two", new("babel derived this"), new("derived"))
	if *gotTitle != "a model's summary" || *gotProv != "inferred" {
		t.Errorf("a derived title was not overlaid: %q/%q", *gotTitle, *gotProv)
	}

	// An untitled session gains the inferred title, which is the case that
	// matters most: 287 of the operator's codex sessions have no title at all.
	gotTitle, gotProv = overlay.apply("codex/two", nil, nil)
	if gotTitle == nil || *gotTitle != "a model's summary" || *gotProv != "inferred" {
		t.Errorf("an untitled session was not given its inferred title: %v/%v", gotTitle, gotProv)
	}

	// A session nobody paid for keeps what it had.
	gotTitle, gotProv = overlay.apply("codex/three", new("babel derived this"), new("derived"))
	if *gotTitle != "babel derived this" || *gotProv != "derived" {
		t.Errorf("an un-inferred session was changed: %q/%q", *gotTitle, *gotProv)
	}
}

// TestInferredTitleStoreRoundTripsAndValidates: the store is the one place a
// value that cost money is durable, and the one boundary an external titler's
// output crosses into durable state, so it validates rather than trusts.
func TestInferredTitleStoreRoundTripsAndValidates(t *testing.T) {
	ctx := context.Background()
	store, err := title.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	in := title.Inferred{
		Selector:   "codex/sessions/2026/01/02/x.jsonl",
		Title:      "  Installing codex profiles\non ubuntu  ",
		Titler:     "/tmp/titler.sh",
		Model:      "openai/gpt-oss-20b",
		InferredAt: time.Now().UTC(),
	}
	if err := store.Put(ctx, in); err != nil {
		t.Fatalf("Put: %v", err)
	}
	all, err := store.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	got, ok := all[in.Selector]
	if !ok {
		t.Fatalf("stored title missing from %v", all)
	}
	if got.Title != "Installing codex profiles on ubuntu" {
		t.Errorf("title = %q, want it collapsed to one line", got.Title)
	}
	if got.Model != "openai/gpt-oss-20b" || got.Titler != "/tmp/titler.sh" {
		t.Errorf("attribution lost: %+v", got)
	}

	// Re-inferring replaces rather than accumulating: a display value the
	// operator may regenerate must have one current answer.
	in.Title = "A better title"
	if err := store.Put(ctx, in); err != nil {
		t.Fatalf("re-Put: %v", err)
	}
	all, _ = store.All(ctx)
	if len(all) != 1 || all[in.Selector].Title != "A better title" {
		t.Errorf("re-inference did not replace: %v", all)
	}

	// Withdrawal restores the derived title, because inference only overlaid it.
	removed, err := store.Delete(ctx, in.Selector)
	if err != nil || !removed {
		t.Fatalf("Delete = %v, %v", removed, err)
	}
	if all, _ := store.All(ctx); len(all) != 0 {
		t.Errorf("delete left %v", all)
	}
}

// TestNormalizeRefusesUnusableTitlerOutput: a titler is an external command and
// its stdout is untrusted in the same way a session log is.
func TestNormalizeRefusesUnusableTitlerOutput(t *testing.T) {
	for _, bad := range []string{"", "   ", "\n\t ", string(make([]rune, 0))} {
		if _, err := title.Normalize(bad); err == nil {
			t.Errorf("Normalize(%q) accepted an empty title", bad)
		}
	}
	long := ""
	for range title.MaxTitleRunes + 1 {
		long += "x"
	}
	if _, err := title.Normalize(long); err == nil {
		t.Errorf("Normalize accepted %d runes, over the %d bound", len(long), title.MaxTitleRunes)
	}
	got, err := title.Normalize("a  reasonable\ttitle\n")
	if err != nil {
		t.Fatalf("Normalize rejected a usable title: %v", err)
	}
	if got != "a reasonable title" {
		t.Errorf("Normalize = %q, want whitespace collapsed to one line", got)
	}
}
