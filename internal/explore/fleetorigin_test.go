package explore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
)

// TestFrontierHitsAttributeTheMachineTheyCameFrom is issue #109 item 4 at the
// point where it has to pay off: not the index knowing which host an idea came
// from, but the worker being told.
//
// The value of a fleet-wide frontier is that two conductors on two machines
// cannot silently duplicate one another, and the conductor is the party making
// the decision. A worker handed "this idea already exists" with no attribution
// would reasonably read it as this machine's own prior work - and that changes
// what it should do. Its own superseded wording is a revision to make; another
// host's committed candidate is a record to argue with or defer to. Withholding
// which one it is would be inference presented as fact, which SPEC.md §3
// forbids.
func TestFrontierHitsAttributeTheMachineTheyCameFrom(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// This machine's own durable candidate, and another host's committed one,
	// both matching the same query. Only the local one exists durably here;
	// the remote one reached the rebuildable cache through a fleet ingest,
	// which is the only place a remote record is ever written.
	local := plantFrontier(t, h, "the release pipeline skips the integration suite it claims to run")
	remote := frontier.Output{
		Kind:      frontier.OutputHypothesis,
		ID:        "hyp-remote-1",
		RootID:    "hyp-remote-1",
		RunID:     "run-elsewhere",
		Status:    frontier.StatusUntriaged,
		CreatedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
		Summary:   "the release pipeline reports a verified integration suite it never ran",
		Text:      "the release pipeline reports a verified integration suite it never ran",
	}
	if _, err := h.index.IndexFleetFrontier(ctx, "workstation-darwin", []frontier.Output{remote}); err != nil {
		t.Fatalf("ingest another host's committed record: %v", err)
	}

	payload := h.writeResult("discovery.json",
		oneCandidate("c-1", "an unrelated documentation formatting question"))
	args := append(payloadArgs(map[explore.Stage]string{explore.StageExplore: payload}),
		"-request-capability", "corpus-search",
		"-search-scope", explore.ScopeFrontier,
		"-search-query", "release pipeline integration suite")

	outcome, err := h.controller(args).Explore(ctx,
		explore.Options{Authority: testAuthority, RunID: "r-fleet-origin"})
	if err != nil {
		t.Fatalf("Explore: %v (failures %+v)", err, outcome.Failures)
	}
	if len(outcome.Retrieval) != 1 {
		t.Fatalf("the run served %d retrievals, want 1", len(outcome.Retrieval))
	}

	var results explore.FrontierResults
	if err := json.Unmarshal(outcome.Retrieval[0].Served, &results); err != nil {
		t.Fatalf("the served payload is not a FrontierResults document: %v", err)
	}

	origins := map[string]string{}
	for _, hit := range results.Hits {
		origins[hit.ID] = hit.Origin
	}
	if _, served := origins[local]; !served {
		t.Fatalf("the worker was not served this machine's own candidate: %+v", results.Hits)
	}
	if _, served := origins[remote.ID]; !served {
		t.Fatalf("the worker was not served the other host's candidate, so the fleet frontier "+
			"is not reaching the conductor: %+v", results.Hits)
	}
	// This machine's own analysis carries no origin: the absence is the
	// statement, and it is the same absence index.LocalOrigin means.
	if origins[local] != "" {
		t.Errorf("this machine's own candidate was attributed to %q, want no origin",
			origins[local])
	}
	if origins[remote.ID] != "workstation-darwin" {
		t.Errorf("the other host's candidate was attributed to %q, want workstation-darwin",
			origins[remote.ID])
	}

	// An absent origin stays off the wire entirely rather than travelling as an
	// empty string a worker would have to interpret.
	var raw struct {
		Hits []map[string]json.RawMessage `json:"hits"`
	}
	if err := json.Unmarshal(outcome.Retrieval[0].Served, &raw); err != nil {
		t.Fatalf("decode the served hits: %v", err)
	}
	for _, hit := range raw.Hits {
		var id string
		if err := json.Unmarshal(hit["id"], &id); err != nil {
			t.Fatalf("decode a served hit id: %v", err)
		}
		_, present := hit["origin"]
		if id == local && present {
			t.Error("a local hit carried an empty origin field")
		}
		if id == remote.ID && !present {
			t.Error("a remote hit omitted its origin")
		}
	}

	// And the narrowing the operator gets: the same index answers one machine
	// at a time, with LocalOrigin bound as a real value rather than read as
	// "no filter".
	mine, err := h.index.FrontierSearch(ctx, index.FrontierQuery{
		Match: "release pipeline", Origins: []string{index.LocalOrigin},
	})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(mine) != 1 || mine[0].ID != local {
		t.Errorf("a local-only search returned %d hits, want only this machine's candidate", len(mine))
	}
}
