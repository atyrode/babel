package explore_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/run"
	"github.com/atyrode/babel/internal/worker"
)

// TestExportedReceiptNeverCarriesAServedExcerpt is the §9 guard, and it matters
// more than the feature it guards.
//
// The two halves of a served retrieval go to different places on purpose. The
// wire carries content to the worker, because a model that cannot read a record
// cannot form an observation about it. The receipt carries locators and digests
// only, because §9 forbids the durable record becoming a plaintext store of
// archive content readable by anyone with catalog access. The split is absolute:
// there is no disclosure class, no configuration and no operator preference
// under which an excerpt belongs in an exported receipt.
//
// It is the thing most likely to go wrong, because every route into the receipt
// runs beside a route onto the wire. The retrieval trace is written from the
// same redacted hits the payload is built from. The reason string a facility
// returns is handed to the worker and recorded in the tool record. A single
// convenience — quoting the hit in the reason, noting the text beside the
// locator — puts the corpus in the receipt without anyone deciding to.
//
// The whole receipt is searched, encoded rather than field by field, because a
// leak covered only by a reader's chosen field list is a leak the next field
// reintroduces. The fixture worker's own result payload quotes nothing from the
// corpus, so a hit anywhere in the document is Babel's leak rather than the
// counterpart's claim.
func TestExportedReceiptNeverCarriesAServedExcerpt(t *testing.T) {
	h := newHarness(t)
	payload := h.writeResult("discovery.json", h.discovery())
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		"-request-capability", "corpus-search", "-search-query", "")
	controller := h.controller(args)

	outcome, err := controller.Explore(context.Background(), explore.Options{Authority: testAuthority, RunID: "r-boundary"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if outcome.Receipt == nil {
		t.Fatal("the run wrote no receipt, so there is no exported record to check")
	}
	if len(outcome.Retrieval) != 1 {
		t.Fatalf("the run served %d retrievals, want 1", len(outcome.Retrieval))
	}

	excerpts := servedExcerpts(t, outcome.Retrieval[0])
	if len(excerpts) == 0 {
		t.Fatal("the served payload carried no excerpt, so this test would pass over an empty search")
	}
	if found := excerptsInReceipt(t, outcome.Receipt, excerpts); len(found) != 0 {
		t.Errorf("the exported receipt quotes served excerpts %q; the wire carries content to the worker and the receipt carries locators only",
			found)
	}

	// The receipt still records the retrieval — giving up the text must not
	// have cost the trace its ability to reopen the claim.
	if len(outcome.Receipt.Body.Retrieval) != 1 {
		t.Fatalf("the receipt records %d retrieval steps, want 1", len(outcome.Receipt.Body.Retrieval))
	}
	step := outcome.Receipt.Body.Retrieval[0]
	if len(step.Results) == 0 {
		t.Fatal("the receipt's retrieval step records no results, so nothing served is reopenable")
	}
	for _, result := range step.Results {
		locator := result.Evidence.Locator()
		if locator.Path == "" || locator.Line < 1 || len(locator.Digest) != 64 {
			t.Errorf("receipt result %d cannot recover its bytes: %+v", result.Rank, locator)
		}
	}

	// Non-vacuity, in the test rather than beside it. The likeliest leak is a
	// facility quoting what it served into the reason string, which Babel
	// hands to the worker and records in the tool record verbatim. Planting
	// exactly that must fail the check above; a guard that cannot catch the
	// leak it was written for guards nothing.
	poisoned := poisonedReceipt(t, outcome.Receipt, excerpts[0])
	if found := excerptsInReceipt(t, poisoned, excerpts); len(found) == 0 {
		t.Fatal("the check passed a receipt with a served excerpt written into a tool record's reason; it proves nothing about the clean one")
	}
}

// servedExcerpts is every excerpt one served retrieval put on the wire, read
// back out of the payload bytes the worker actually received.
func servedExcerpts(t *testing.T, served explore.Retrieval) []string {
	t.Helper()
	if len(served.Served) == 0 {
		t.Fatal("the retrieval recorded no served payload; a decision that carries no evidence is the defect this boundary exists around")
	}
	var results explore.SearchResults
	if err := json.Unmarshal(served.Served, &results); err != nil {
		t.Fatalf("the served payload is not a SearchResults document: %v", err)
	}
	excerpts := make([]string, 0, len(results.Hits))
	for _, hit := range results.Hits {
		if hit.Excerpt != "" {
			excerpts = append(excerpts, hit.Excerpt)
		}
	}
	return excerpts
}

// excerptsInReceipt reports which excerpts appear anywhere in the encoded
// receipt. It encodes rather than formatting with %+v: a receipt's worker
// record, result and failures are pointers, and %+v renders a pointer as an
// address, so a search over that text would silently skip the places a leak
// lands.
func excerptsInReceipt(t *testing.T, receipt *run.Receipt, excerpts []string) []string {
	t.Helper()
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatalf("encode the exported receipt: %v", err)
	}
	document := string(encoded)
	var found []string
	for _, excerpt := range excerpts {
		if strings.Contains(document, excerpt) {
			found = append(found, excerpt)
		}
	}
	return found
}

// poisonedReceipt is a copy of receipt with excerpt written into the first tool
// record's reason: the leak excerptsInReceipt must catch.
//
// It copies rather than mutating, because the original is the run's own audit
// record and a test that damaged it would be checking a receipt nobody wrote.
func poisonedReceipt(t *testing.T, receipt *run.Receipt, excerpt string) *run.Receipt {
	t.Helper()
	if receipt.Body.Worker == nil || len(receipt.Body.Worker.ToolRequests) == 0 {
		t.Fatal("the receipt records no tool request to poison")
	}
	workerReceipt := *receipt.Body.Worker
	records := make([]worker.ToolRecord, len(workerReceipt.ToolRequests))
	copy(records, workerReceipt.ToolRequests)
	records[0].Reason = "served: " + excerpt
	workerReceipt.ToolRequests = records

	poisoned := *receipt
	poisoned.Body.Worker = &workerReceipt
	return &poisoned
}
