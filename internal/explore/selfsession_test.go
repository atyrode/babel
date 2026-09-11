package explore_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/adapter/babelself"
	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/event"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/research"
	"github.com/atyrode/babel/internal/transcript"
	"github.com/atyrode/babel/internal/worker"
)

// TestEveryServedPayloadIsReducedToIdentifiers covers the reducer against
// each shape this package puts on the wire, and against one it does not
// publish.
//
// Every branch has to hold, not just the corpus one. A frontier hit carries
// Babel's own prior output, which quotes the corpus; a fetched document is
// public material whose bytes the operator's store has no business holding.
// And the default branch is the one that matters over time: the day a
// facility serves a new schema, a reducer that passed unknown payloads
// through would persist whatever it was, so the contract is that content
// never survives — the reason names the schema and nothing quotes the body.
func TestEveryServedPayloadIsReducedToIdentifiers(t *testing.T) {
	corpusLocator := event.Locator{Path: "/synthetic/omp.jsonl", Line: 3, ByteOffset: 41, Digest: strings.Repeat("ab", 32)}
	const (
		excerpt  = "synthetic corpus record: the zeppelin cache warms lazily"
		priorRun = "synthetic prior output: the cache warms on startup"
		fetched  = "synthetic public document body"
		url      = "https://synthetic.invalid/doc"
	)
	document := research.Document{
		Schema: research.DocumentSchema, Source: research.Source{ID: "src-1", URL: url},
		RetrievedAt: time.Now().UTC(), MediaType: "text/plain",
		Digest: digest.Bytes([]byte(fetched)), Bytes: int64(len(fetched)), Content: fetched,
	}

	cases := []struct {
		name    string
		payload any
		secret  string
		want    []event.Locator
	}{{
		name: "corpus search",
		payload: explore.SearchResults{
			Schema: explore.SearchResultsSchema, Query: "zeppelin", Limit: 10,
			Hits: []explore.SearchHit{{Harness: "omp", SourceID: "s-1", Index: 3,
				Excerpt: excerpt, Locator: corpusLocator}},
		},
		secret: excerpt,
		want:   []event.Locator{corpusLocator},
	}, {
		name: "frontier search",
		payload: explore.FrontierResults{
			Schema: explore.FrontierResultsSchema, Query: "cache", Limit: 10,
			Hits: []explore.FrontierSearchHit{{Kind: "hypothesis", ID: "hyp-1",
				Summary: priorRun, Text: priorRun}},
		},
		secret: priorRun,
	}, {
		name:    "research catalog",
		payload: research.Catalog{Schema: research.CatalogSchema, Sources: []research.Source{{ID: "src-1", URL: url}}},
		secret:  url,
	}, {
		name:    "research document",
		payload: document,
		secret:  fetched,
		want:    []event.Locator{{Path: url, Digest: string(document.Digest)}},
	}, {
		name:    "a schema this build does not publish",
		payload: map[string]any{"schema": "babel.synthetic-future-facility/1", "body": excerpt},
		secret:  excerpt,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("encode the served payload: %v", err)
			}
			locators, reason := explore.RedactServedResult(string(encoded))
			if reason == "" {
				t.Error("a withheld result with no reason says only that something is missing")
			}
			if strings.Contains(reason, tc.secret) {
				t.Errorf("the reason quotes the content it withheld: %q", reason)
			}
			if len(locators) != len(tc.want) {
				t.Fatalf("reduced to %d locators, want %d (%q)", len(locators), len(tc.want), reason)
			}
			for i, want := range tc.want {
				if locators[i] != want {
					t.Errorf("locator %d = %+v, want %+v", i, locators[i], want)
				}
			}
		})
	}

	// A tool result that is not a served payload at all: Babel's own answer
	// to a submission, which arrives as plain text.
	if _, reason := explore.RedactServedResult("the submission was accepted"); reason == "" {
		t.Error("a plain-text tool result was withheld with no reason")
	}
}

// transcripts wires a real writer into a run, under a root the test owns, and
// returns the factory the controller calls once per job.
//
// It uses the production redactor rather than a stand-in on purpose: the
// thing under test is whether the payload internal/explore actually serves is
// reduced before it is persisted, and a test redactor would prove only that
// the writer calls something.
func transcripts(t *testing.T, root string) func(*explore.Config) {
	t.Helper()
	store, err := babelself.NewStore(babelself.StoreConfig{
		Root:      root,
		Redact:    explore.RedactServedResult,
		Workspace: "/synthetic/workspace",
		Profile:   "synthetic-profile@1",
		Recipes:   []string{testRecipe.ID},
	})
	if err != nil {
		t.Fatalf("new transcript store: %v", err)
	}
	return func(cfg *explore.Config) {
		cfg.Transcript = func(runID, job string) (explore.TranscriptWriter, error) {
			return store.Open(runID, job)
		}
	}
}

// TestSelfSessionArchivesTheRunWithoutTheCorpus is the §9 guard on the second
// artifact a served retrieval could land in.
//
// Recording an analysis run as a session is what makes Babel's own reasoning
// recoverable: a receipt says which records were served and what the boundary
// allowed, and never why the model concluded anything. The conversation says
// why. But the conversation is also where every served excerpt arrives — the
// engine reports the whole message list, tool results included, in its
// agent_end frames — so a writer that persisted it verbatim would copy the
// corpus into a plaintext file under Babel's data directory. That is exactly
// what the receipt gives up the text to avoid, and §9 forbids it of both.
//
// The log is searched whole rather than per field, because a leak covered
// only where it was expected is one the next field reintroduces.
func TestSelfSessionArchivesTheRunWithoutTheCorpus(t *testing.T) {
	h := newHarness(t)
	root := t.TempDir()
	payload := h.writeResult("discovery.json", h.discovery())
	controller := h.controller(
		payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		transcripts(t, root))

	outcome, err := controller.Explore(context.Background(), explore.Options{
		Authority: testAuthority,
		RunID:     "r-self-session",
	})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Retrieval) != 1 {
		t.Fatalf("the run served %d retrievals, want 1", len(outcome.Retrieval))
	}
	excerpts := servedExcerpts(t, outcome.Retrieval[0])
	if len(excerpts) == 0 {
		t.Fatal("the served payload carried no excerpt, so this test would pass over an empty search")
	}

	found, err := babelself.New().Discover(context.Background(), []string{root})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("the run archived %d sessions, want the one job it ran: %+v", len(found), found)
	}
	if want := "r-self-session/" + string(explore.StageExplore); found[0].SourceID != want {
		t.Errorf("archived source id %q, want %q", found[0].SourceID, want)
	}

	body, err := os.ReadFile(found[0].PrimaryPath)
	if err != nil {
		t.Fatalf("read the archived session: %v", err)
	}
	log := string(body)
	for _, excerpt := range excerpts {
		if strings.Contains(log, excerpt) {
			t.Errorf("the archived session quotes the served excerpt %q; the payload carries content to the model and a durable record carries locators only", excerpt)
		}
	}

	// The locators are what makes the withheld result reopenable, and they
	// are the same locators the receipt's retrieval trace kept: one served
	// retrieval, two durable records of it, one address.
	if len(outcome.Receipt.Body.Retrieval) != 1 {
		t.Fatalf("the receipt records %d retrieval steps, want 1", len(outcome.Receipt.Body.Retrieval))
	}
	for _, result := range outcome.Receipt.Body.Retrieval[0].Results {
		locator := result.Evidence.Locator()
		if !strings.Contains(log, locator.Digest) {
			t.Errorf("the archived session cannot reopen served record %d: its digest %s is not in the log",
				result.Rank, locator.Digest)
		}
	}

	// What the session is for: the job document Babel composed, the tool
	// calls the model made, and the conclusion it submitted. A log stripped
	// of those would be a safe file with nothing in it.
	if !strings.Contains(log, string(worker.ToolSearch)) {
		t.Errorf("the archived session does not record the corpus search the model made")
	}
	if !strings.Contains(log, "the first synthetic candidate") {
		t.Errorf("the archived session does not record the result the model submitted")
	}
	if !strings.Contains(log, babelself.WithheldSchema) {
		t.Errorf("the archived session records no withheld-result document, so nothing says why a result is absent")
	}

	// And it is a session, not a file: the harness the adapter reports is
	// one internal/transcript can parse, so the web session view and the CLI
	// render these records rather than raw JSON.
	total, events, err := transcript.Events(found[0].PrimaryPath, found[0].Harness, 0, 200)
	if err != nil {
		t.Fatalf("transcript.Events: %v", err)
	}
	messages := 0
	for _, e := range events {
		if e.Kind == "message" {
			messages++
		}
	}
	if messages == 0 {
		t.Fatalf("none of the log's %d records parsed as a message; the harness decodes as unparseable", total)
	}
}
