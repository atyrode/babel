package explore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/research"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// stubBroker stands in for internal/research so this package's tests need no
// network. SPEC.md §10 requires broker tests to run without a provider or
// credentials; the refusals the real broker enforces — the address policy, the
// redirect ceiling, the media-type allowlist — are proven against real sockets
// in internal/research, and what is under test here is the control plane's
// half: routing, budgeting, strict arguments, and the receipt.
type stubBroker struct {
	sources []research.Source
	doc     research.Document
	err     error
	calls   []string
}

func (s *stubBroker) Catalog() research.Catalog {
	return research.Catalog{Schema: research.CatalogSchema, Sources: s.sources}
}

func (s *stubBroker) Fetch(_ context.Context, id string) (research.Document, error) {
	s.calls = append(s.calls, id)
	if s.err != nil {
		return research.Document{}, s.err
	}
	doc := s.doc
	doc.Source = research.Source{ID: id, URL: "https://example.com/" + id}
	for _, src := range s.sources {
		if src.ID == id {
			doc.Source = src
		}
	}
	return doc, nil
}

const researchFetchedAt = "2026-08-31T18:30:00Z"

// newResearchBroker builds a retrieval whose only facility is a stub research
// broker, with fetches capped at limit.
func newResearchBroker(t *testing.T, limit int) (*retrieval, *stubBroker) {
	t.Helper()
	at, err := time.Parse(time.RFC3339, researchFetchedAt)
	if err != nil {
		t.Fatalf("parse the fixture clock: %v", err)
	}
	stub := &stubBroker{
		sources: []research.Source{
			{ID: "res-aaaaaaaaaaaa", URL: "https://example.com/spec"},
			{ID: "res-bbbbbbbbbbbb", URL: "https://example.com/notes"},
		},
		doc: research.Document{
			Schema:      research.DocumentSchema,
			RetrievedAt: at,
			MediaType:   "text/markdown",
			Digest:      digest.Bytes([]byte("the document")),
			Bytes:       int64(len("the document")),
			Content:     "the document",
		},
	}
	return &retrieval{research: stub, fetches: limit, now: func() time.Time { return at }}, stub
}

func fetchRequest(source string) worker.ToolRequest {
	return worker.ToolRequest{
		Capability: worker.CapabilityPublicResearch,
		Tool:       worker.ToolFetch,
		Arguments:  json.RawMessage(`{"source":"` + source + `"}`),
	}
}

// TestServedFetchRecordsProvenanceAndServesContent is the §9 split for this
// facility: the worker receives the document, and the receipt receives the
// provenance that recovers it and never the content.
func TestServedFetchRecordsProvenanceAndServesContent(t *testing.T) {
	broker, stub := newResearchBroker(t, 4)
	decision := broker.Authorize(context.Background(), fetchRequest(stub.sources[0].ID))
	if !decision.Allow {
		t.Fatalf("a fetch of a fixed source was denied: %s", decision.Reason)
	}
	var served research.Document
	if err := json.Unmarshal(decision.Results, &served); err != nil {
		t.Fatalf("the served payload is not a document: %v", err)
	}
	if served.Content != "the document" || served.Schema != research.DocumentSchema {
		t.Errorf("served payload = %+v, want the fetched document under its schema", served)
	}

	steps, out := broker.trace()
	if len(steps) != 1 || len(out) != 1 {
		t.Fatalf("recorded %d steps and %d retrievals, want 1 and 1", len(steps), len(out))
	}
	step := steps[0]
	if step.Scope != run.ScopeResearch {
		t.Errorf("step scope = %q, want %q", step.Scope, run.ScopeResearch)
	}
	if step.Tool != string(worker.CapabilityPublicResearch) {
		t.Errorf("step tool = %q, want the facility that served it", step.Tool)
	}
	if step.Research == nil {
		t.Fatal("a research retrieval recorded no source")
	}
	if step.Research.URL != stub.sources[0].URL || step.Research.SourceID != stub.sources[0].ID {
		t.Errorf("recorded source = %+v, want the fixed source that was read", step.Research)
	}
	if step.Research.Digest != stub.doc.Digest || step.Research.MediaType != "text/markdown" {
		t.Errorf("recorded digest/type = %s %s, want the fetched document's",
			step.Research.Digest, step.Research.MediaType)
	}
	if step.Research.RetrievedAt.Format(time.RFC3339) != researchFetchedAt {
		t.Errorf("recorded retrieval time = %s, want the fetch's own", step.Research.RetrievedAt)
	}
	// The content is what must not be in the durable record: a receipt
	// holding fetched pages is a copy of the public web in the operator's
	// store, and the digest is what makes the citation checkable instead.
	encoded, err := json.Marshal(step)
	if err != nil {
		t.Fatalf("marshal the step: %v", err)
	}
	if strings.Contains(string(encoded), "the document") {
		t.Errorf("the receipt step carries the fetched content: %s", encoded)
	}
	if out[0].Document == nil || out[0].Document.Content != "the document" {
		t.Errorf("the outcome lost the served document: %+v", out[0].Document)
	}
}

// TestFetchArgumentsAdmitNothingButASourceID is the disclosure boundary at the
// argument document. §2.6 makes URL, query, header and body disclosure sinks,
// so a request with room in it is a channel whatever the broker does with the
// extra keys.
func TestFetchArgumentsAdmitNothingButASourceID(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{name: "a URL of its own", args: `{"source":"res-aaaaaaaaaaaa","url":"https://elsewhere.example/x"}`},
		{name: "a header", args: `{"source":"res-aaaaaaaaaaaa","headers":{"authorization":"Bearer x"}}`},
		{name: "a body", args: `{"source":"res-aaaaaaaaaaaa","body":"private transcript text"}`},
		{name: "a query", args: `{"source":"res-aaaaaaaaaaaa","query":"private transcript text"}`},
		{name: "no source at all", args: `{}`},
		{name: "an empty source", args: `{"source":""}`},
		{name: "not a document", args: `"res-aaaaaaaaaaaa"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broker, stub := newResearchBroker(t, 4)
			decision := broker.Authorize(context.Background(), worker.ToolRequest{
				Capability: worker.CapabilityPublicResearch,
				Tool:       worker.ToolFetch,
				Arguments:  json.RawMessage(tc.args),
			})
			if decision.Allow {
				t.Errorf("arguments %s were served", tc.args)
			}
			if len(stub.calls) != 0 {
				t.Errorf("the broker was called with %v for a request that should never have reached it",
					stub.calls)
			}
			if steps, _ := broker.trace(); len(steps) != 0 {
				t.Errorf("a refused fetch left %d steps in the trace", len(steps))
			}
		})
	}
}

// TestTheCatalogIsServedAndIsNotARetrieval covers the one operation that
// reaches nothing: it answers which sources the operator fixed, so it is not
// budgeted and not written to the retrieval trace — the tool decision in the
// worker receipt is where an authorization event belongs.
func TestTheCatalogIsServedAndIsNotARetrieval(t *testing.T) {
	broker, stub := newResearchBroker(t, 1)
	decision := broker.Authorize(context.Background(), worker.ToolRequest{
		Capability: worker.CapabilityPublicResearch,
		Tool:       worker.ToolSources,
	})
	if !decision.Allow {
		t.Fatalf("the catalog was denied: %s", decision.Reason)
	}
	var catalog research.Catalog
	if err := json.Unmarshal(decision.Results, &catalog); err != nil {
		t.Fatalf("the served catalog does not parse: %v", err)
	}
	if len(catalog.Sources) != len(stub.sources) || catalog.Sources[0].ID != stub.sources[0].ID {
		t.Errorf("served catalog = %+v, want this run's fixed sources", catalog.Sources)
	}
	if steps, _ := broker.trace(); len(steps) != 0 {
		t.Errorf("listing the sources was recorded as %d retrievals", len(steps))
	}
	// The fetch budget is untouched by the listing, so a run that asked what
	// it may read can still read it.
	if decision := broker.Authorize(context.Background(), fetchRequest(stub.sources[0].ID)); !decision.Allow {
		t.Errorf("the catalog spent the fetch budget: %s", decision.Reason)
	}
}

// TestEgressIsBudgetedSeparatelyFromTheCorpus checks the two budgets do not
// draw on each other. A run that fetched twice must not look to the corpus
// budget like a run that searched twice, and the reverse.
func TestEgressIsBudgetedSeparatelyFromTheCorpus(t *testing.T) {
	broker, stub := newResearchBroker(t, 1)
	// The corpus budget is exhausted from the start, and public research is
	// unaffected by it.
	broker.limit = 1
	broker.searched = 1

	first := broker.Authorize(context.Background(), fetchRequest(stub.sources[0].ID))
	if !first.Allow {
		t.Fatalf("an exhausted corpus budget denied a fetch: %s", first.Reason)
	}
	second := broker.Authorize(context.Background(), fetchRequest(stub.sources[1].ID))
	if second.Allow {
		t.Error("a second fetch was served against a budget of one")
	}
	if !strings.Contains(second.Reason, "public-research budget") {
		t.Errorf("denial reason = %q, want the research budget named", second.Reason)
	}
	if len(stub.calls) != 1 {
		t.Errorf("the broker was called %d times, want 1: a budget denial must not reach the network",
			len(stub.calls))
	}
}

// TestABrokerRefusalIsADenialAndNotATermination covers the failure path: what
// the broker refuses is reported to the worker as a reason it can adapt to,
// and nothing enters the retrieval trace, because no document was served.
func TestABrokerRefusalIsADenialAndNotATermination(t *testing.T) {
	broker, stub := newResearchBroker(t, 4)
	stub.err = research.ErrBlockedAddress

	decision := broker.Authorize(context.Background(), fetchRequest(stub.sources[0].ID))
	if decision.Allow {
		t.Fatal("a refused fetch was served")
	}
	if !strings.Contains(decision.Reason, "not public") {
		t.Errorf("denial reason = %q, want the broker's own", decision.Reason)
	}
	if steps, _ := broker.trace(); len(steps) != 0 {
		t.Errorf("a fetch that served nothing left %d steps in the trace", len(steps))
	}
}

// TestAGrantWithNoBrokerBehindItIsDenied is the run configuration this package
// refuses to serve rather than fail silently: the capability reached the job
// but no facility was wired to it.
func TestAGrantWithNoBrokerBehindItIsDenied(t *testing.T) {
	broker := &retrieval{fetches: 4}
	for _, tool := range []string{worker.ToolSources, worker.ToolFetch} {
		decision := broker.Authorize(context.Background(), worker.ToolRequest{
			Capability: worker.CapabilityPublicResearch,
			Tool:       tool,
			Arguments:  json.RawMessage(`{"source":"res-aaaaaaaaaaaa"}`),
		})
		if decision.Allow {
			t.Errorf("%s was served by a run with no research broker", tool)
		}
		if !strings.Contains(decision.Reason, "public-research broker") {
			t.Errorf("%s denial reason = %q, want the missing facility named", tool, decision.Reason)
		}
	}
}
