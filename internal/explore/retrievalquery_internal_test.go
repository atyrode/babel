package explore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/index"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// The retrieval fixture is a hand-written session whose vocabulary is chosen
// so every expected result set can be stated exactly.
//
// One record holds the words "agent decision override" adjacently, one holds
// two of them scattered, one holds a single one, and one holds none. That is
// the whole shape a corpus search has to get right: a worker asking for all
// three words must be answered with every record holding any of them, ranked
// so the record holding the phrase comes first, and must not be answered with
// the empty intersection.
const retrievalFixture = `{"type":"session","version":3,"id":"00000000-0000-4000-8000-00000000f001","timestamp":"2026-05-01T00:00:00.000Z","cwd":"/synthetic/retrieval","title":"synthetic retrieval fixture"}
{"type":"message","id":"f0000001","parentId":null,"timestamp":"2026-05-01T00:01:00.000Z","message":{"role":"user","content":[{"type":"text","text":"an agent decision override is what I want to read about"}]}}
{"type":"message","id":"f0000002","parentId":"f0000001","timestamp":"2026-05-01T00:02:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"the agent recorded its decision"}]}}
{"type":"message","id":"f0000003","parentId":"f0000002","timestamp":"2026-05-01T00:03:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"an override was applied"}]}}
{"type":"message","id":"f0000004","parentId":"f0000003","timestamp":"2026-05-01T00:04:00.000Z","message":{"role":"user","content":[{"type":"text","text":"human-agent coordination needs a shared vocabulary"}]}}
{"type":"message","id":"f0000005","parentId":"f0000004","timestamp":"2026-05-01T00:05:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"the zeppelin hummed quietly"}]}}
`

const fixtureSourceID = "omp-retrieval"

// newRetrievalBroker builds the production evidence broker over the fixture,
// with the retrieval budget the caller wants to observe.
func newRetrievalBroker(t *testing.T, budget int) *retrieval {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "retrieval-fixture.jsonl")
	if err := os.WriteFile(path, []byte(retrievalFixture), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	idx, err := index.Open(filepath.Join(root, "state"))
	if err != nil {
		t.Fatalf("open index: %v", err)
	}
	t.Cleanup(func() { idx.Close() })

	stream := event.Stream{
		Harness:       event.HarnessOMP,
		AdapterSchema: 1,
		SourceID:      fixtureSourceID,
		Path:          path,
	}
	res, err := idx.IndexSession(context.Background(), stream)
	if err != nil {
		t.Fatalf("index fixture: %v", err)
	}
	if res.Events == 0 {
		t.Fatalf("fixture indexed %d events", res.Events)
	}
	at := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	return &retrieval{
		index:     idx,
		harnesses: []string{event.HarnessOMP},
		sourceIDs: []string{fixtureSourceID},
		limit:     budget,
		now: func() time.Time {
			at = at.Add(time.Second)
			return at
		},
	}
}

// search issues one corpus-search request the way internal/worker does.
func (r *retrieval) search(t *testing.T, req SearchRequest) worker.Decision {
	t.Helper()
	args, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal search request: %v", err)
	}
	return r.Authorize(context.Background(), worker.ToolRequest{
		Capability: worker.CapabilityCorpusSearch,
		Tool:       worker.ToolSearch,
		Arguments:  args,
	})
}

// lastStep is the retrieval trace entry the most recent request recorded.
func (r *retrieval) lastStep(t *testing.T) (run.RetrievalStep, []index.Hit) {
	t.Helper()
	steps, served := r.trace()
	if len(steps) == 0 {
		t.Fatal("no retrieval was recorded")
	}
	return steps[len(steps)-1], served[len(served)-1].Hits
}

func hitTexts(hits []index.Hit) []string {
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Text)
	}
	return out
}

// TestMultiTermQueryIsAnsweredByRelevanceNotByIntersection is the regression
// this package exists to prevent a second time.
//
// A worker's query is a bag of keywords, not an expression. Read as an
// intersection it asked for one record holding every word, which on a corpus
// of individual transcript records is almost never anything: the operator's
// first exploration retrieved four times against a healthy index of 26,948
// events and was served 0, 0, 1 and 0 hits while every single one of its
// words matched hundreds of records. Read as a union it is answered, and
// relevance — not membership — is what decides which answers are worth a
// context window.
func TestMultiTermQueryIsAnsweredByRelevanceNotByIntersection(t *testing.T) {
	r := newRetrievalBroker(t, 0)
	decision := r.search(t, SearchRequest{Query: "agent decision override"})
	if !decision.Allow {
		t.Fatalf("decision = %+v, want the search served", decision)
	}
	_, hits := r.lastStep(t)

	// No record holds all three words, so an intersection is empty and the
	// worker would have been told the corpus says nothing. Four records hold
	// at least one: the hyphenated "human-agent" is two tokens to the
	// tokenizer, so it holds "agent" and is a legitimate weak match.
	if len(hits) != 4 {
		t.Fatalf("hits = %q, want the four records holding any of the words", hitTexts(hits))
	}
	// Relevance is the whole reason a union is safe to serve, so a test that
	// only counted hits would pass on a translation that served them in any
	// order. The record holding the three words adjacently is first — that
	// is the phrase preference — and the record that holds one of them only
	// inside a compound is last.
	if want := "an agent decision override is what I want to read about"; hits[0].Text != want {
		t.Errorf("best-ranked hit = %q, want %q", hits[0].Text, want)
	}
	if want := "human-agent coordination needs a shared vocabulary"; hits[len(hits)-1].Text != want {
		t.Errorf("worst-ranked hit = %q, want %q", hits[len(hits)-1].Text, want)
	}
	// The record holding a single word is served too. A worker reading down
	// the page decides what is evidence; this package never reads rank as
	// strength, and a weak hit it can dismiss is worth more than no hit.
	if got := strings.Join(hitTexts(hits), "\n"); !strings.Contains(got, "an override was applied") {
		t.Errorf("hits = %q, want the single-word record among them", hitTexts(hits))
	}
}

// TestQueryWithNoBearingOnTheCorpusReturnsNothing is the other half of the
// contract. A union that answered everything would have replaced one failure
// with another, and the property that stops it is that a word absent from the
// corpus contributes no records at all.
func TestQueryWithNoBearingOnTheCorpusReturnsNothing(t *testing.T) {
	r := newRetrievalBroker(t, 0)
	decision := r.search(t, SearchRequest{Query: "marsupial photosynthesis tuba"})
	if !decision.Allow {
		t.Fatalf("decision = %+v, want a served search", decision)
	}
	step, hits := r.lastStep(t)
	if len(hits) != 0 {
		t.Errorf("hits = %q, want none", hitTexts(hits))
	}
	if len(step.Results) != 0 {
		t.Errorf("recorded %d results for a query nothing matches", len(step.Results))
	}
	if decision.Reason == "" || strings.Contains(decision.Reason, "failed") {
		t.Errorf("reason = %q, want an answer rather than a failure", decision.Reason)
	}
}

// TestHostileQueryShapesAnswerRatherThanError covers the grammar a worker's
// text is untrusted input to. A hyphen used to raise "no such column: agent"
// out of FTS5, and NEAR, carets, colons, stars and parentheses all still mean
// something to it. Every one of them must produce an answer.
func TestHostileQueryShapesAnswerRatherThanError(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantHits bool
	}{{
		name: "hyphen is a word character to everyone but FTS5",
		// The recipe driving the operator's first exploration is named
		// human-agent-coordination, so this phrasing is what a worker
		// writes without being asked to.
		query:    "human-agent coordination",
		wantHits: true,
	}, {
		name:     "fts5 operators are matched as the words they are",
		query:    `NEAR(agent decision) OR override* ^kind:opaque (paren) -"nothing here"`,
		wantHits: true,
	}, {
		name:     "a query with nothing a tokenizer can match is answered, not refused",
		query:    "*** --- ///",
		wantHits: false,
	}, {
		name:     "a query longer than the index accepts is answered, not refused",
		query:    strings.Repeat("agent ", index.MaxMatchBytes),
		wantHits: false,
	}, {
		name:     "an empty query browses rather than searching text",
		query:    "",
		wantHits: true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRetrievalBroker(t, 0)
			decision := r.search(t, SearchRequest{Query: tc.query})
			if !decision.Allow {
				t.Fatalf("decision = %+v, want an answer", decision)
			}
			if strings.Contains(decision.Reason, "failed") {
				t.Errorf("reason = %q, want an answer rather than a failure", decision.Reason)
			}
			step, hits := r.lastStep(t)
			if got := len(hits) > 0; got != tc.wantHits {
				t.Errorf("hits = %q, want any = %v", hitTexts(hits), tc.wantHits)
			}
			// The query is recorded verbatim whatever it was. Diagnosing a
			// retrieval that returned nothing needs the string the worker
			// actually sent, not the expression it was translated into.
			if step.Query != tc.query {
				t.Errorf("recorded query = %q, want %q", step.Query, tc.query)
			}
		})
	}
}

// TestServedPageIsBounded pins the limit. A union matches far more than a
// worker can read, and an unbounded answer would drown a context window as
// effectively as an empty one starved it.
func TestServedPageIsBounded(t *testing.T) {
	r := newRetrievalBroker(t, 0)
	if decision := r.search(t, SearchRequest{Query: "agent decision override", Limit: 2}); !decision.Allow {
		t.Fatalf("decision = %+v, want the search served", decision)
	}
	step, hits := r.lastStep(t)
	if len(hits) != 2 {
		t.Errorf("hits = %q, want the two the request asked for", hitTexts(hits))
	}
	if len(step.Results) != 2 {
		t.Errorf("recorded %d results, want 2", len(step.Results))
	}
	// A request over the index's page ceiling is refused rather than
	// silently narrowed: a quietly reduced page looks like the end of the
	// results.
	if decision := r.search(t, SearchRequest{Query: "agent", Limit: index.MaxLimit + 1}); decision.Allow {
		t.Errorf("decision = %+v, want a limit past the ceiling refused", decision)
	}
}

// TestRetrievalBudgetAndTraceSurviveTheTranslation guards the two properties
// the receipt depends on. Both are what made the original defect diagnosable
// at all: the trace named the queries, and the budget bounded them.
func TestRetrievalBudgetAndTraceSurviveTheTranslation(t *testing.T) {
	r := newRetrievalBroker(t, 2)
	queries := []string{"agent decision override", "human-agent coordination"}
	for _, q := range queries {
		if decision := r.search(t, SearchRequest{Query: q}); !decision.Allow {
			t.Fatalf("query %q: decision = %+v, want it served", q, decision)
		}
	}
	decision := r.search(t, SearchRequest{Query: "one retrieval too many"})
	if decision.Allow || !strings.Contains(decision.Reason, "budget") {
		t.Errorf("third search = %+v, want it refused for the retrieval budget", decision)
	}

	steps, served := r.trace()
	if len(steps) != len(queries) || len(served) != len(queries) {
		t.Fatalf("trace = %d steps and %d served, want %d of each", len(steps), len(served), len(queries))
	}
	for i, step := range steps {
		if step.Index != i+1 {
			t.Errorf("step %d has index %d", i, step.Index)
		}
		if step.Query != queries[i] {
			t.Errorf("step %d recorded query %q, want %q", i+1, step.Query, queries[i])
		}
		if step.Tool != string(worker.CapabilityCorpusSearch) {
			t.Errorf("step %d recorded tool %q", i+1, step.Tool)
		}
		if len(step.Results) == 0 {
			t.Fatalf("step %d recorded no results", i+1)
		}
		// Every served hit stays reopenable against the archive it came
		// from: the receipt carries the locator, never the excerpt.
		for _, result := range step.Results {
			locator := result.Evidence.Locator()
			if locator.Path == "" || locator.Digest == "" {
				t.Errorf("step %d rank %d: locator = %+v, want a path and a digest",
					i+1, result.Rank, locator)
			}
			if strings.Contains(result.Evidence.Note(), served[i].Hits[result.Rank-1].Text) {
				t.Errorf("step %d rank %d: the evidence note carries transcript text", i+1, result.Rank)
			}
		}
	}
}

// TestServedHitCarriesEnoughToBeCited records what one hit is, because a hit
// that cannot be cited is not evidence however well it was retrieved.
func TestServedHitCarriesEnoughToBeCited(t *testing.T) {
	r := newRetrievalBroker(t, 0)
	if decision := r.search(t, SearchRequest{Query: "agent decision override", Limit: 1}); !decision.Allow {
		t.Fatalf("decision = %+v, want the search served", decision)
	}
	_, hits := r.lastStep(t)
	hit := hits[0]

	if hit.Harness == "" || hit.SourceID != fixtureSourceID {
		t.Errorf("hit identity = %s/%s, want the fixture session", hit.Harness, hit.SourceID)
	}
	if hit.Kind == "" {
		t.Error("hit carries no event kind")
	}
	if hit.Text == "" {
		t.Error("hit carries no text to quote")
	}
	if hit.Locator.Path == "" || hit.Locator.Line == 0 || hit.Locator.Digest == "" {
		t.Errorf("hit locator = %+v, want a path, a line and a digest", hit.Locator)
	}
	// The locator is the authority: it must recover the record's bytes from
	// the archive on its own, which is what makes a quotation reopenable.
	recovered, err := os.ReadFile(hit.Locator.Path)
	if err != nil {
		t.Fatalf("reopen the hit's session: %v", err)
	}
	line := strings.Split(string(recovered), "\n")[hit.Locator.Line-1]
	if !strings.Contains(line, "agent decision override") {
		t.Errorf("locator line %d = %q, want the record the hit was drawn from", hit.Locator.Line, line)
	}
	if int64(len(recovered)) <= hit.Locator.ByteOffset {
		t.Errorf("locator byte offset %d is past the end of a %d byte log",
			hit.Locator.ByteOffset, len(recovered))
	}
	t.Logf("one served hit:\n%s", mustIndent(t, hit))
}

func mustIndent(t *testing.T, v any) string {
	t.Helper()
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal hit: %v", err)
	}
	return fmt.Sprintf("%s", out)
}
