package synth_test

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/adapter"
	"github.com/atyrode/babel/internal/adapter/claude"
	"github.com/atyrode/babel/internal/adapter/codex"
	"github.com/atyrode/babel/internal/adapter/omp"
	"github.com/atyrode/babel/internal/synth"
)

// The generated corpus is only worth anything if the production adapters read
// it the way they read a real one. These tests are therefore written against
// what the adapters report, not against what this package believes it wrote:
// the corpus states a claim, the adapter is the judge, and a layout drift on
// either side fails here rather than in an analysis run.

// plannedPaths returns the primary-log paths the corpus claims for one harness.
func plannedPaths(c *synth.Corpus, harness string) []string {
	var out []string
	for _, s := range c.Sessions {
		if s.Harness == harness {
			out = append(out, s.Path)
		}
	}
	slices.Sort(out)
	return out
}

func discoveredPaths(t *testing.T, found []adapter.SourceSession) []string {
	t.Helper()
	out := make([]string, 0, len(found))
	for _, s := range found {
		out = append(out, s.PrimaryPath)
	}
	slices.Sort(out)
	return out
}

// completeness reports whether an adapter recorded a reason for a field, which
// is how every adapter says "the format did not tell me".
func completeness(meta adapter.CommonMeta, field string) bool {
	for _, r := range meta.Completeness {
		if r.Field == field {
			return true
		}
	}
	return false
}

func generateCorpus(t *testing.T) *synth.Corpus {
	t.Helper()
	corpus, err := synth.Generate(t.TempDir(), synth.DefaultProfile())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return corpus
}

// TestSingleHarnessProfilesStayDiscoverable covers the degenerate corners the
// knobs allow: one harness at a time, no blobs, no artifacts, no defects. Each
// harness's tree has to stand on its own, because a caller testing one reader
// should not have to generate the other two.
func TestSingleHarnessProfilesStayDiscoverable(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		harness string
		shape   func(*synth.Profile)
		adapter adapter.Adapter
		root    func(*synth.Corpus) string
		extra   int // sessions the harness has beyond the generated ones
	}{
		{synth.HarnessOMP, func(p *synth.Profile) { p.OMPSessions = 2 }, omp.New(),
			func(c *synth.Corpus) string { return c.OMPSessionsRoot }, 0},
		{synth.HarnessCodex, func(p *synth.Profile) { p.CodexSessions = 2 }, codex.New(),
			func(c *synth.Corpus) string { return c.CodexRoot }, 1},
		{synth.HarnessClaude, func(p *synth.Profile) { p.ClaudeSessions = 2 }, claude.New(),
			func(c *synth.Corpus) string { return c.ClaudeRoot }, 0},
	} {
		t.Run(tc.harness, func(t *testing.T) {
			p := synth.Profile{
				Seed:                7,
				SizeBuckets:         []synth.SizeBucket{{Bytes: 2 << 10, Weight: 1}},
				ArtifactsPerSession: [2]int{0, 0},
			}
			tc.shape(&p)
			corpus, err := synth.Generate(t.TempDir(), p)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			found, err := tc.adapter.Discover(ctx, []string{tc.root(corpus)})
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			if want := len(corpus.Sessions) + tc.extra; len(found) != want {
				t.Fatalf("discovered %d sessions, want %d", len(found), want)
			}
			for _, src := range found {
				d, err := tc.adapter.Describe(ctx, src)
				if err != nil {
					t.Fatalf("describe %s: %v", src.SourceID, err)
				}
				if len(d.Artifacts) != 0 && src.PrimaryPath != corpus.CodexHistoryPath {
					t.Errorf("%s reported %d artifacts from a profile that generates none",
						src.SourceID, len(d.Artifacts))
				}
				if len(d.UnresolvedBlobRefs) != 0 {
					t.Errorf("%s reported unresolved %v from a profile that plants none",
						src.SourceID, d.UnresolvedBlobRefs)
				}
			}
		})
	}
}

func TestOMPAdapterReadsGeneratedCorpus(t *testing.T) {
	ctx := context.Background()
	corpus := generateCorpus(t)
	a := omp.New()

	found, err := a.Discover(ctx, []string{corpus.OMPSessionsRoot})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	want := plannedPaths(corpus, synth.HarnessOMP)
	if got := discoveredPaths(t, found); !slices.Equal(got, want) {
		t.Fatalf("discovered\n %v\nwant\n %v", got, want)
	}

	for _, src := range found {
		planned := corpus.Find(src.PrimaryPath)
		if planned == nil {
			t.Fatalf("discovered an unplanned session %s", src.PrimaryPath)
		}
		t.Run(planned.ID, func(t *testing.T) {
			d, err := a.Describe(ctx, src)
			if err != nil {
				t.Fatalf("describe: %v", err)
			}
			var md struct {
				BlobRefCount        int `json:"blob_ref_count"`
				ResolvedBlobCount   int `json:"resolved_blob_count"`
				UnresolvedBlobCount int `json:"unresolved_blob_count"`
				ArtifactCount       int `json:"artifact_count"`
			}
			if err := json.Unmarshal(d.AdapterMetadata, &md); err != nil {
				t.Fatalf("adapter metadata: %v", err)
			}

			if d.PrimarySize != planned.Bytes {
				t.Errorf("primary size %d, corpus wrote %d", d.PrimarySize, planned.Bytes)
			}
			if md.ArtifactCount != len(planned.Artifacts) {
				t.Errorf("artifact count %d, corpus wrote %d", md.ArtifactCount, len(planned.Artifacts))
			}
			// Every reference the corpus planted must be found, including the
			// one hidden in a sibling artifact and the one escaped inside a
			// nested JSON string.
			if md.BlobRefCount != len(planned.BlobRefs) {
				t.Errorf("found %d references, corpus planted %d", md.BlobRefCount, len(planned.BlobRefs))
			}
			if !slices.Equal(d.UnresolvedBlobRefs, planned.UnresolvedRefs) {
				t.Errorf("unresolved %v, corpus planned %v", d.UnresolvedBlobRefs, planned.UnresolvedRefs)
			}
			if md.UnresolvedBlobCount != len(planned.UnresolvedRefs) {
				t.Errorf("unresolved count %d, corpus planned %d", md.UnresolvedBlobCount, len(planned.UnresolvedRefs))
			}
			if want := len(planned.UnresolvedRefs) == 0; d.ContinuationGrade != want {
				t.Errorf("continuation grade %v, want %v with %d unresolved references",
					d.ContinuationGrade, want, len(planned.UnresolvedRefs))
			}
			// A starved header must degrade explicitly rather than produce a
			// synthesized title, workspace, or creation time.
			for _, field := range []string{"title", "workspace", "created_at"} {
				if got := completeness(d.Meta, field); got != planned.SparseHeader {
					t.Errorf("%s completeness reason = %v, sparse header = %v", field, got, planned.SparseHeader)
				}
			}
		})
	}
}

func TestCodexAdapterReadsGeneratedCorpus(t *testing.T) {
	ctx := context.Background()
	corpus := generateCorpus(t)
	a := codex.New()

	found, err := a.Discover(ctx, []string{corpus.CodexRoot})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// Codex keeps host state rather than only per-session logs, so its
	// discovery legitimately returns one session the corpus did not generate.
	want := append(plannedPaths(corpus, synth.HarnessCodex), corpus.CodexHistoryPath)
	slices.Sort(want)
	if got := discoveredPaths(t, found); !slices.Equal(got, want) {
		t.Fatalf("discovered\n %v\nwant\n %v", got, want)
	}

	for _, src := range found {
		d, err := a.Describe(ctx, src)
		if err != nil {
			t.Fatalf("describe %s: %v", src.SourceID, err)
		}
		var md codex.Metadata
		if err := json.Unmarshal(d.AdapterMetadata, &md); err != nil {
			t.Fatalf("adapter metadata for %s: %v", src.SourceID, err)
		}

		if src.SourceID == codex.StateSourceID {
			if md.Kind != codex.KindState {
				t.Errorf("host state described as kind %q", md.Kind)
			}
			if md.SessionIndexFound == nil || !*md.SessionIndexFound {
				t.Errorf("session_index.jsonl was generated but not found beside history.jsonl")
			}
			if len(d.Artifacts) != 1 {
				t.Errorf("host state has %d artifacts, want the session index alone", len(d.Artifacts))
			}
			if md.MalformedRecords != 0 {
				t.Errorf("host state reported %d malformed records; it is generated clean", md.MalformedRecords)
			}
			continue
		}

		planned := corpus.Find(src.PrimaryPath)
		if planned == nil {
			t.Fatalf("discovered an unplanned session %s", src.PrimaryPath)
		}
		t.Run(planned.ID, func(t *testing.T) {
			if md.Records != planned.Records {
				t.Errorf("read %d records, corpus wrote %d", md.Records, planned.Records)
			}
			// Codex bounds a record at 4 MiB and reports the excess as
			// truncated; the truncated prefix is then unparsable, so an
			// oversized record is counted twice, once under each name.
			torn := 0
			if planned.TornFinalLine {
				torn = 1
			}
			wantMalformed := planned.MalformedRecords + planned.OversizedRecords + torn
			if md.MalformedRecords != wantMalformed {
				t.Errorf("reported %d malformed records, corpus wrote %d malformed + %d oversized + %d torn",
					md.MalformedRecords, planned.MalformedRecords, planned.OversizedRecords, torn)
			}
			if md.TruncatedRecords != planned.OversizedRecords {
				t.Errorf("reported %d truncated records, corpus wrote %d oversized",
					md.TruncatedRecords, planned.OversizedRecords)
			}
			if md.AttachmentRefs != len(planned.BlobRefs) {
				t.Errorf("scraped %d attachment references, corpus planted %d", md.AttachmentRefs, len(planned.BlobRefs))
			}
			if md.AttachmentFiles != len(planned.Artifacts) {
				t.Errorf("resolved %d attachment files, corpus wrote %d", md.AttachmentFiles, len(planned.Artifacts))
			}
			if !slices.Equal(d.UnresolvedBlobRefs, planned.UnresolvedRefs) {
				t.Errorf("unresolved %v, corpus planned %v", d.UnresolvedBlobRefs, planned.UnresolvedRefs)
			}
			for _, field := range []string{"workspace", "created_at"} {
				if got := completeness(d.Meta, field); got != planned.SparseHeader {
					t.Errorf("%s completeness reason = %v, sparse header = %v", field, got, planned.SparseHeader)
				}
			}
		})
	}
}

func TestClaudeAdapterReadsGeneratedCorpus(t *testing.T) {
	ctx := context.Background()
	corpus := generateCorpus(t)
	a := claude.New()

	found, err := a.Discover(ctx, []string{corpus.ClaudeRoot})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	want := plannedPaths(corpus, synth.HarnessClaude)
	if got := discoveredPaths(t, found); !slices.Equal(got, want) {
		t.Fatalf("discovered\n %v\nwant\n %v", got, want)
	}

	for _, src := range found {
		planned := corpus.Find(src.PrimaryPath)
		if planned == nil {
			t.Fatalf("discovered an unplanned session %s", src.PrimaryPath)
		}
		t.Run(planned.ID, func(t *testing.T) {
			d, err := a.Describe(ctx, src)
			if err != nil {
				t.Fatalf("describe: %v", err)
			}
			var md struct {
				RecordCount      int `json:"record_count"`
				MalformedRecords int `json:"malformed_records"`
				OversizedRecords int `json:"oversized_records"`
				ArtifactCount    int `json:"artifact_count"`
				ArtifactFailures int `json:"artifact_failures"`
			}
			if err := json.Unmarshal(d.AdapterMetadata, &md); err != nil {
				t.Fatalf("adapter metadata: %v", err)
			}

			if md.RecordCount != planned.Records {
				t.Errorf("read %d records, corpus wrote %d", md.RecordCount, planned.Records)
			}
			// Claude Code skips an over-budget record without parsing it, so
			// unlike Codex it never counts one as malformed too. A torn final
			// line is still unparsable and still counted.
			torn := 0
			if planned.TornFinalLine {
				torn = 1
			}
			if want := planned.MalformedRecords + torn; md.MalformedRecords != want {
				t.Errorf("reported %d malformed records, corpus wrote %d malformed + %d torn",
					md.MalformedRecords, planned.MalformedRecords, torn)
			}
			if md.OversizedRecords != planned.OversizedRecords {
				t.Errorf("reported %d oversized records, corpus wrote %d", md.OversizedRecords, planned.OversizedRecords)
			}
			// The dot-prefixed artifact is deliberately skipped: absence here
			// is the adapter working, not the corpus failing to write it.
			if md.ArtifactCount != len(planned.Artifacts) {
				t.Errorf("collected %d artifacts, corpus wrote %d visible and %d hidden",
					md.ArtifactCount, len(planned.Artifacts), len(planned.HiddenArtifacts))
			}
			if md.ArtifactFailures != 0 {
				t.Errorf("%d artifacts could not be read", md.ArtifactFailures)
			}
			for _, field := range []string{"title", "created_at"} {
				if got := completeness(d.Meta, field); got != planned.SparseHeader {
					t.Errorf("%s completeness reason = %v, sparse header = %v", field, got, planned.SparseHeader)
				}
			}
			// A transcript that recorded two working directories must report
			// the conflict rather than silently choose one.
			conflict := false
			for _, r := range d.Meta.Completeness {
				if r.Field == "workspace" && strings.Contains(r.Reason, "distinct cwd") {
					conflict = true
				}
			}
			if conflict != planned.WorkspaceMoved {
				t.Errorf("cwd conflict reported = %v, corpus moved the workspace = %v", conflict, planned.WorkspaceMoved)
			}
		})
	}
}
