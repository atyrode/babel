package explore_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/digest"
	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/research"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// The public-research facility driven end to end by a supervised worker
// process (#75). What these tests prove is the wiring the live gate depends
// on: that the run publishes the facility's operations to the worker, that a
// worker which reads the catalog can fetch by the identifier it was given, and
// that the receipt says afterwards what crossed the boundary.
//
// The broker is a stand-in, and deliberately: SPEC.md §10 requires broker
// tests to need no provider, credential or network, and internal/research
// proves the refusals — the address policy, the redirect ceiling, the
// media-type allowlist — against real sockets in its own suite.

const researchFetchURL = "https://example.com/spec/section-2"

var researchFetchedAt = time.Date(2026, 8, 31, 18, 30, 0, 0, time.UTC)

type testBroker struct {
	fetched []string
}

func (b *testBroker) Catalog() research.Catalog {
	return research.Catalog{Schema: research.CatalogSchema, Sources: b.sources()}
}

func (b *testBroker) sources() []research.Source {
	return []research.Source{{ID: "res-9f2c1a4b7e30", URL: researchFetchURL}}
}

func (b *testBroker) Fetch(_ context.Context, id string) (research.Document, error) {
	b.fetched = append(b.fetched, id)
	for _, src := range b.sources() {
		if src.ID != id {
			continue
		}
		const body = "# Section 2\n\nThe upstream document says the flag was removed.\n"
		return research.Document{
			Schema:      research.DocumentSchema,
			Source:      src,
			RetrievedAt: researchFetchedAt,
			MediaType:   "text/markdown",
			Digest:      digest.Bytes([]byte(body)),
			Bytes:       int64(len(body)),
			Content:     body,
		}, nil
	}
	return research.Document{}, research.ErrUnknownSource
}

// grantResearch is the run configuration an operator's --public-research URL
// produces: the capability in the grant, the facility version in the receipt's
// capability versions, and the broker behind it.
func grantResearch(broker explore.ResearchBroker) func(*explore.Config) {
	return func(cfg *explore.Config) {
		cfg.Grant.Capabilities = append(cfg.Grant.Capabilities, worker.CapabilityPublicResearch)
		cfg.Capabilities.PublicResearch = "research-test/1"
		cfg.Research = broker
	}
}

// TestBrokeredResearchReachesTheWorkerAndTheReceipt is the whole facility in
// one run: the operator fixes a source, the worker discovers it through the
// published operations, Babel serves the document, and the receipt records the
// URL, the time and the digest of what was read.
func TestBrokeredResearchReachesTheWorkerAndTheReceipt(t *testing.T) {
	h := newHarness(t)
	broker := &testBroker{}
	payload := h.writeResult("discovery.json", h.discovery())
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}), "-research")
	controller := h.controller(args, grantResearch(broker))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-research"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}

	// The worker found both operations because the job published them: the
	// fixture requests nothing under a capability whose names it was not
	// given, so two requests is the evidence that the mapping travelled.
	requests := outcome.Receipt.Body.Worker.ToolRequests
	if len(requests) != 2 {
		t.Fatalf("recorded %d tool requests, want the catalog and the fetch: %+v", len(requests), requests)
	}
	for i, want := range []string{worker.ToolSources, worker.ToolFetch} {
		if requests[i].Tool != want || requests[i].Capability != worker.CapabilityPublicResearch {
			t.Errorf("request %d = %s/%s, want public-research/%s",
				i, requests[i].Capability, requests[i].Tool, want)
		}
		if !requests[i].Allowed {
			t.Errorf("request %d (%s) was denied: %s", i, want, requests[i].Reason)
		}
	}
	if len(broker.fetched) != 1 || broker.fetched[0] != broker.sources()[0].ID {
		t.Errorf("the broker was asked for %v, want the one fixed source", broker.fetched)
	}

	// One retrieval step, and it is the fetch rather than the listing: a
	// catalog reaches nothing and is recorded as the authorization it is.
	var fetches []run.RetrievalStep
	for _, step := range outcome.Receipt.Body.Retrieval {
		if step.Scope == run.ScopeResearch {
			fetches = append(fetches, step)
		}
	}
	if len(fetches) != 1 {
		t.Fatalf("the receipt records %d research retrievals, want 1: %+v",
			len(fetches), outcome.Receipt.Body.Retrieval)
	}
	src := fetches[0].Research
	if src == nil {
		t.Fatal("the research step records no source")
	}
	if src.URL != researchFetchURL || src.SourceID != broker.sources()[0].ID {
		t.Errorf("recorded source = %+v, want the fixed source that was read", src)
	}
	if !src.Digest.Valid() || src.Bytes == 0 || src.MediaType != "text/markdown" {
		t.Errorf("recorded provenance = %+v, want a checkable digest, a size and a media type", src)
	}
	if !src.RetrievedAt.Equal(researchFetchedAt) {
		t.Errorf("recorded retrieval time = %s, want the fetch's own", src.RetrievedAt)
	}

	// The receipt holds the provenance and the outcome holds what was
	// served: §9's split, checkable rather than asserted.
	body, err := outcome.Receipt.MarshalBody()
	if err != nil {
		t.Fatalf("marshal the receipt body: %v", err)
	}
	if strings.Contains(string(body), "The upstream document says") {
		t.Error("the receipt carries the fetched document's content")
	}
	var served *research.Document
	for _, r := range outcome.Retrieval {
		if r.Document != nil {
			served = r.Document
		}
	}
	if served == nil || !strings.Contains(served.Content, "The upstream document says") {
		t.Fatalf("the outcome lost the served document: %+v", served)
	}
	if served.Source.URL != researchFetchURL {
		t.Errorf("served document URL = %q, want the fixed source", served.Source.URL)
	}
}

// TestAFetchCarryingItsOwnFieldsIsRefused is the disclosure boundary observed
// through a real worker process: a fetch request with anything in it besides
// the source identifier is denied, the run continues, and nothing about the
// attempt reaches the network or the retrieval trace.
func TestAFetchCarryingItsOwnFieldsIsRefused(t *testing.T) {
	h := newHarness(t)
	broker := &testBroker{}
	payload := h.writeResult("discovery.json", h.discovery())
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		"-research", "-research-extra", "url=https://elsewhere.example/exfiltrate")
	controller := h.controller(args, grantResearch(broker))

	outcome, err := controller.Explore(context.Background(),
		explore.Options{Authority: testAuthority, RunID: "r-research-extra"})
	if err != nil {
		t.Fatalf("a refused fetch ended the run: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Findings) != 1 {
		t.Errorf("the run produced %d findings, want 1: a denial is not a termination", len(outcome.Findings))
	}
	requests := outcome.Receipt.Body.Worker.ToolRequests
	if len(requests) != 2 {
		t.Fatalf("recorded %d tool requests, want the catalog and the refused fetch", len(requests))
	}
	if !requests[0].Allowed {
		t.Errorf("the catalog was denied: %s", requests[0].Reason)
	}
	if requests[1].Allowed {
		t.Fatal("a fetch carrying a URL of the worker's own was served")
	}
	if requests[1].DenyCode != worker.DenyPolicy {
		t.Errorf("denial code = %q, want a policy denial", requests[1].DenyCode)
	}
	if len(broker.fetched) != 0 {
		t.Errorf("the broker was reached with %v for a request that never should have got there", broker.fetched)
	}
	for _, step := range outcome.Receipt.Body.Retrieval {
		if step.Scope == run.ScopeResearch {
			t.Errorf("a refused fetch was recorded as a retrieval: %+v", step)
		}
	}
}

// TestTheResearchGrantAndItsFacilityMustAgree covers both halves of the
// configuration refusal. A granted capability with no facility would deny
// every fetch after the operator authorized egress; a facility with no grant
// would sit unreachable while the operator believed research was in scope.
func TestTheResearchGrantAndItsFacilityMustAgree(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	args := payloadArgs(map[explore.Stage]string{explore.StageExplore: payload})

	_, err := explore.New(h.config(args, func(cfg *explore.Config) {
		cfg.Grant.Capabilities = append(cfg.Grant.Capabilities, worker.CapabilityPublicResearch)
		cfg.Capabilities.PublicResearch = "research-test/1"
	}))
	if err == nil || !strings.Contains(err.Error(), "no broker") {
		t.Errorf("New with a research grant and no broker = %v, want a refusal naming the missing facility", err)
	}

	_, err = explore.New(h.config(args, func(cfg *explore.Config) {
		cfg.Research = &testBroker{}
	}))
	if err == nil || !strings.Contains(err.Error(), "does not grant it") {
		t.Errorf("New with a broker and no grant = %v, want a refusal", err)
	}

	// And the facility version is still mandatory: a granted capability
	// whose build cannot be named makes the containment question
	// unanswerable later, which is what validateFacilities exists for.
	_, err = explore.New(h.config(args, func(cfg *explore.Config) {
		cfg.Grant.Capabilities = append(cfg.Grant.Capabilities, worker.CapabilityPublicResearch)
		cfg.Research = &testBroker{}
	}))
	if err == nil || !strings.Contains(err.Error(), "facility version") {
		t.Errorf("New with no facility version = %v, want a refusal naming it", err)
	}
}
