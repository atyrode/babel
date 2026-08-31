package index_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
)

// frontierOutput builds one output of Babel's own, at a fixed instant so a
// fingerprint depends on what the test varied and nothing else.
func frontierOutput(kind frontier.OutputKind, id, text string) frontier.Output {
	return frontier.Output{
		Kind:      kind,
		ID:        id,
		RootID:    id,
		RunID:     "run-1",
		CreatedAt: time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC),
		Summary:   strings.SplitN(text, "\n", 2)[0],
		Text:      text,
	}
}

func openIndex(t *testing.T) *index.Index {
	t.Helper()
	idx, err := index.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatalf("index.Open: %v", err)
	}
	t.Cleanup(func() { idx.Close() })
	return idx
}

// TestFrontierIndexRoundTripsAndUpdatesIncrementally covers the two properties
// the self-retrieval surface has to have. A record put in comes back out with
// everything a run needs in order to name it, and a second pass over an
// unchanged frontier writes nothing — the property that makes reconciling on
// every prepare and every explore affordable rather than a full FTS5 rebuild
// each time.
func TestFrontierIndexRoundTripsAndUpdatesIncrementally(t *testing.T) {
	ctx := context.Background()
	idx := openIndex(t)

	first := frontierOutput(frontier.OutputHypothesis, "hyp-1",
		"the release pipeline skips the integration suite it claims to run")
	first.Status = frontier.StatusUntriaged
	second := frontierOutput(frontier.OutputFinding, "fnd-1",
		"deployment verification gaps\nthe same unverified deployment claim recurs across four sessions")
	answer := frontierOutput(frontier.OutputReviewAnswer, "dsp-1",
		"reject\noperator\nthe pipeline candidate is too broad to act on")
	answer.Subject = frontier.Ref{Type: frontier.EntityHypothesis, ID: "hyp-1"}

	res, err := idx.IndexFrontier(ctx, []frontier.Output{first, second, answer})
	if err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}
	if res.Added != 3 || res.Records != 3 || res.Skipped != 0 || res.Removed != 0 {
		t.Fatalf("first pass = %+v, want three additions", res)
	}

	hits, err := idx.FrontierSearch(ctx, index.FrontierQuery{Match: "integration suite"})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("the indexed candidate is not searchable, so a run cannot find its own prior work")
	}
	hit := hits[0]
	if hit.ID != "hyp-1" || hit.Kind != frontier.OutputHypothesis {
		t.Errorf("hit = %s %s, want the hypothesis hyp-1", hit.Kind, hit.ID)
	}
	if hit.RootID != "hyp-1" || hit.RunID != "run-1" || hit.Status != frontier.StatusUntriaged {
		t.Errorf("hit lost its identity fields: %+v", hit)
	}
	if !hit.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("hit created at %s, want %s", hit.CreatedAt, first.CreatedAt)
	}
	if hit.Summary != first.Summary || hit.Text != first.Text {
		t.Errorf("hit text round-tripped as %q / %q", hit.Summary, hit.Text)
	}

	// A review answer is searchable by the operator's own words and carries
	// the record it answers about, which is what makes "an operator already
	// declined this" reachable from a search rather than only from the
	// record page.
	answers, err := idx.FrontierSearch(ctx, index.FrontierQuery{
		Match: "too broad",
		Kinds: []frontier.OutputKind{frontier.OutputReviewAnswer},
	})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(answers) != 1 || answers[0].Subject.ID != "hyp-1" {
		t.Fatalf("review answers = %+v, want one about hyp-1", answers)
	}

	// Second pass, unchanged frontier: everything is skipped.
	res, err = idx.IndexFrontier(ctx, []frontier.Output{first, second, answer})
	if err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}
	if res.Skipped != 3 || res.Added != 0 || res.Updated != 0 || res.Removed != 0 {
		t.Fatalf("unchanged pass = %+v, want three skips and no writes", res)
	}

	// Third pass: one record's derived text changed, one is new, and one is
	// gone because a revision superseded it.
	moved := first
	moved.Status = frontier.StatusRejected
	third := frontierOutput(frontier.OutputObservation, "obs-1", "the suite was skipped on this run")
	res, err = idx.IndexFrontier(ctx, []frontier.Output{moved, third, answer})
	if err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}
	if res.Updated != 1 || res.Added != 1 || res.Removed != 1 || res.Skipped != 1 {
		t.Fatalf("changed pass = %+v, want one update, one addition, one removal, one skip", res)
	}
	gone, err := idx.FrontierSearch(ctx, index.FrontierQuery{
		Match: "deployment verification",
		Kinds: []frontier.OutputKind{frontier.OutputFinding},
	})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("the superseded finding is still searchable: %+v", gone)
	}
	rejected, err := idx.FrontierSearch(ctx, index.FrontierQuery{
		Match:    "integration suite",
		Statuses: []frontier.Status{frontier.StatusRejected},
	})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(rejected) != 1 {
		// #87 removes the idea of a terminal status, so a rejected
		// candidate has to stay findable — that is what makes reviving one
		// possible instead of re-minting it.
		t.Fatalf("rejected candidates = %d, want the one whose status moved", len(rejected))
	}
}

// TestFrontierSearchRefusesAnUnknownKind pins the filter's refusal. A filter
// naming a surface this build does not index would otherwise be dropped, and a
// dropped filter answers a question about all of Babel's output while looking
// like an answer about one part of it.
func TestFrontierSearchRefusesAnUnknownKind(t *testing.T) {
	idx := openIndex(t)
	_, err := idx.FrontierSearch(context.Background(), index.FrontierQuery{
		Match: "anything",
		Kinds: []frontier.OutputKind{"proposal"},
	})
	if err == nil {
		t.Fatal("an unknown frontier kind was accepted")
	}
}

// TestSalientTermsAreTheScopeSubjectAndAreDeterministic is the mechanical half
// of the injection: the terms a preparation searches the frontier with come
// from term frequency against document frequency over the prepared records, and
// nothing else. Two properties matter and both are checked here — the
// boilerplate every record shares must not reach the query, and the same corpus
// must produce the same query every time, or an immutable preparation would
// record an unreproducible one.
func TestSalientTermsAreTheScopeSubjectAndAreDeterministic(t *testing.T) {
	build := func() *index.Salience {
		s := index.NewSalience()
		// Boilerplate in every record: present everywhere, so its inverse
		// document frequency is zero however often it appears.
		boilerplate := "assistant message thread session assistant message thread session"
		s.Add(boilerplate + " the release pipeline skipped the integration suite again")
		s.Add(boilerplate + " release pipeline logs show the integration suite skipped")
		s.Add(boilerplate + " the integration suite was skipped by the release pipeline")
		s.Add(boilerplate + " unrelated wording about documentation formatting")
		return s
	}
	terms := build().Terms(4)
	if len(terms) != 4 {
		t.Fatalf("terms = %v, want four", terms)
	}
	for _, banned := range []string{"assistant", "message", "thread", "session"} {
		for _, term := range terms {
			if term == banned {
				t.Errorf("boilerplate term %q reached the query: %v", banned, terms)
			}
		}
	}
	want := map[string]bool{"release": true, "pipeline": true, "integration": true, "suite": true, "skipped": true}
	for _, term := range terms {
		if !want[term] {
			t.Errorf("term %q is not this scope's subject: %v", term, terms)
		}
	}
	if second := build().Terms(4); strings.Join(second, " ") != strings.Join(terms, " ") {
		t.Errorf("the same corpus produced %v then %v", terms, second)
	}
	if got := len(build().Terms(0)); got == 0 {
		t.Error("a default-limited query came back empty")
	}
}

// TestRelatedOutputsAreBoundedAndDeterministic is what a preparation records:
// the top page of a frontier search with the scope's own terms, bounded, and
// identical on a second run over the same store. A preparation's identity is
// derived from its content, so an injection that reordered between two calls
// would produce two ids for one act.
func TestRelatedOutputsAreBoundedAndDeterministic(t *testing.T) {
	ctx := context.Background()
	idx := openIndex(t)

	// Twenty candidates about the same subject: more than the ceiling a
	// preparation may name, so the bound is doing work rather than being
	// wider than the fixture.
	outputs := make([]frontier.Output, 0, 20)
	for i := range 20 {
		id := "hyp-" + string(rune('a'+i))
		out := frontierOutput(frontier.OutputHypothesis, id,
			"the release pipeline skips the integration suite, wording "+id)
		out.CreatedAt = out.CreatedAt.Add(time.Duration(i) * time.Minute)
		outputs = append(outputs, out)
	}
	if _, err := idx.IndexFrontier(ctx, outputs); err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}

	query := index.FrontierQuery{
		Match: "release pipeline integration suite",
		Order: index.OrderRelevance,
		Limit: 12,
	}
	ids := func() []string {
		hits, err := idx.FrontierSearch(ctx, query)
		if err != nil {
			t.Fatalf("FrontierSearch: %v", err)
		}
		out := make([]string, 0, len(hits))
		for _, hit := range hits {
			out = append(out, hit.ID)
		}
		return out
	}
	first := ids()
	if len(first) != 12 {
		t.Fatalf("related outputs = %d, want the page bound of 12", len(first))
	}
	if second := ids(); strings.Join(second, ",") != strings.Join(first, ",") {
		t.Errorf("two identical searches returned %v then %v", first, second)
	}
}

// TestTermOverlapMeasuresContainment covers the dedup heuristic's measure. A
// restatement of one idea shares the shorter statement's vocabulary; two
// candidates about the same subsystem share its nouns and little else, and the
// measure has to separate those two cases or every candidate about one area
// would be warned about.
func TestTermOverlapMeasuresContainment(t *testing.T) {
	original := "the release pipeline skips the integration suite it claims to run"
	restated := "release runs skip the integration suite they claim to run"
	distinct := "the release notes template omits the migration checklist"

	if got := index.TermOverlap(original, restated); got < 0.6 {
		t.Errorf("overlap of a restatement = %.2f, want at least 0.6", got)
	}
	if got := index.TermOverlap(original, distinct); got >= 0.6 {
		t.Errorf("overlap of a distinct candidate = %.2f, want below 0.6", got)
	}
	if got := index.TermOverlap(original, ""); got != 0 {
		t.Errorf("overlap with nothing = %.2f, want 0", got)
	}
}
