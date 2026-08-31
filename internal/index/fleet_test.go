package index_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/index"
)

// These tests defend the origin dimension (issue #109 item 4): one search
// answers across every host, each partition is reconciled against its own
// authority, and a fleet ingest can never delete this machine's own analysis
// from its own search index.

// TestFleetPartitionsAreReconciledIndependently is the property the whole
// dimension exists for. A machine re-indexing its own durable store must not
// drop what it has learned about the fleet, and a fleet ingest must not drop
// what it holds locally - otherwise the two passes fight, and whichever ran
// last wins.
func TestFleetPartitionsAreReconciledIndependently(t *testing.T) {
	ctx := context.Background()
	idx := openIndex(t)

	local := frontierOutput(frontier.OutputHypothesis, "hyp-local",
		"the release pipeline skips the integration suite it claims to run")
	remote := frontierOutput(frontier.OutputHypothesis, "hyp-remote",
		"scheduled deployments report success without running verification")

	if res, err := idx.IndexFrontier(ctx, []frontier.Output{local}); err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	} else if res.Added != 1 {
		t.Fatalf("local pass = %+v, want one addition", res)
	}
	if res, err := idx.IndexFleetFrontier(ctx, "h2", []frontier.Output{remote}); err != nil {
		t.Fatalf("IndexFleetFrontier: %v", err)
	} else if res.Added != 1 || res.Removed != 0 {
		t.Fatalf("fleet pass = %+v, want one addition and no removal", res)
	}

	// One search, both machines. This is the answer a conductor needs before
	// minting a candidate: not "nothing here", but "another host explored
	// this".
	hits, err := idx.FrontierSearch(ctx, index.FrontierQuery{Match: "deployments verification"})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "hyp-remote" {
		t.Fatalf("fleet search returned %d hits (%v), want the remote candidate", len(hits), ids(hits))
	}
	if hits[0].Origin != "h2" {
		t.Errorf("remote hit attributed to %q, want h2", hits[0].Origin)
	}

	// Re-running the local pass with its unchanged set must leave the remote
	// partition alone. Before the origin dimension this deleted it.
	res, err := idx.IndexFrontier(ctx, []frontier.Output{local})
	if err != nil {
		t.Fatalf("second IndexFrontier: %v", err)
	}
	if res.Removed != 0 || res.Skipped != 1 {
		t.Fatalf("second local pass = %+v, want one skip and no removal", res)
	}
	if origins, err := idx.FrontierOrigins(ctx); err != nil {
		t.Fatalf("FrontierOrigins: %v", err)
	} else if origins[index.LocalOrigin] != 1 || origins["h2"] != 1 {
		t.Errorf("origins = %v, want one row local and one on h2", origins)
	}

	// And a fleet pass for one host must not touch another host's partition.
	third := frontierOutput(frontier.OutputFinding, "fnd-h3", "one more machine reporting")
	if _, err := idx.IndexFleetFrontier(ctx, "h3", []frontier.Output{third}); err != nil {
		t.Fatalf("IndexFleetFrontier h3: %v", err)
	}
	if _, err := idx.IndexFleetFrontier(ctx, "h2", []frontier.Output{remote}); err != nil {
		t.Fatalf("IndexFleetFrontier h2 again: %v", err)
	}
	if origins, err := idx.FrontierOrigins(ctx); err != nil {
		t.Fatalf("FrontierOrigins: %v", err)
	} else if len(origins) != 3 {
		t.Errorf("origins = %v, want three partitions", origins)
	}

	// A remote partition reconciled against a set that no longer names a
	// record drops that record, which is how a wording another host superseded
	// stops being searchable here.
	if res, err := idx.IndexFleetFrontier(ctx, "h2", nil); err != nil {
		t.Fatalf("IndexFleetFrontier h2 empty: %v", err)
	} else if res.Removed != 1 {
		t.Errorf("emptying h2 = %+v, want one removal", res)
	}
}

// A fleet read hands this machine its own published records back. Taking them
// into a remote partition would hold one record twice - once as the durable
// record it is, once as a snapshot of it - and a search would report two
// independent prior ideas where there is one.
func TestLocalPartitionWinsARecordTwoOriginsClaim(t *testing.T) {
	ctx := context.Background()
	idx := openIndex(t)

	own := frontierOutput(frontier.OutputHypothesis, "hyp-own",
		"the release pipeline skips the integration suite it claims to run")
	if _, err := idx.IndexFrontier(ctx, []frontier.Output{own}); err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}

	// The same record arriving from the catalog, attributed to the host that
	// published it - which is this machine.
	res, err := idx.IndexFleetFrontier(ctx, "h1", []frontier.Output{own})
	if err != nil {
		t.Fatalf("IndexFleetFrontier: %v", err)
	}
	if res.Foreign != 1 || res.Added != 0 || res.Updated != 0 {
		t.Fatalf("fleet pass over this machine's own record = %+v, want it declined as foreign", res)
	}
	hits, err := idx.FrontierSearch(ctx, index.FrontierQuery{Match: "integration suite"})
	if err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("search returned %d hits, want one: the record is held twice", len(hits))
	}
	if hits[0].Origin != index.LocalOrigin {
		t.Errorf("the durable record's row was replaced by the published snapshot: origin %q",
			hits[0].Origin)
	}

	// A remote pass declining a record must not then delete it as unoffered:
	// that would make the two passes flap the row on alternate reconciles.
	if _, err := idx.IndexFleetFrontier(ctx, "h1", nil); err != nil {
		t.Fatalf("IndexFleetFrontier empty: %v", err)
	}
	if hits, err := idx.FrontierSearch(ctx, index.FrontierQuery{Match: "integration suite"}); err != nil {
		t.Fatalf("FrontierSearch: %v", err)
	} else if len(hits) != 1 || hits[0].Origin != index.LocalOrigin {
		t.Errorf("a fleet pass deleted this machine's own analysis: %v", ids(hits))
	}

	// The reverse direction is an adoption: a record a remote partition held,
	// which this machine now holds durably, moves to the local partition.
	adopted := frontierOutput(frontier.OutputFinding, "fnd-adopt", "restored from a backup")
	if _, err := idx.IndexFleetFrontier(ctx, "h2", []frontier.Output{adopted}); err != nil {
		t.Fatalf("IndexFleetFrontier h2: %v", err)
	}
	if _, err := idx.IndexFrontier(ctx, []frontier.Output{own, adopted}); err != nil {
		t.Fatalf("IndexFrontier adopting: %v", err)
	}
	if origins, err := idx.FrontierOrigins(ctx); err != nil {
		t.Fatalf("FrontierOrigins: %v", err)
	} else if origins[index.LocalOrigin] != 2 || origins["h2"] != 0 {
		t.Errorf("origins after adoption = %v, want both rows local", origins)
	}
}

// The origin filter is what a host chip passes. LocalOrigin is a real column
// value naming this machine, so the filter has to bind the empty string rather
// than read it as "no filter".
func TestFrontierSearchNarrowsByOrigin(t *testing.T) {
	ctx := context.Background()
	idx := openIndex(t)

	shared := "verification"
	local := frontierOutput(frontier.OutputHypothesis, "hyp-local", shared+" is skipped locally")
	remote := frontierOutput(frontier.OutputHypothesis, "hyp-remote", shared+" is skipped remotely")
	if _, err := idx.IndexFrontier(ctx, []frontier.Output{local}); err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}
	if _, err := idx.IndexFleetFrontier(ctx, "h2", []frontier.Output{remote}); err != nil {
		t.Fatalf("IndexFleetFrontier: %v", err)
	}

	for _, tc := range []struct {
		name    string
		origins []string
		want    []string
	}{
		{"every origin by default", nil, []string{"hyp-local", "hyp-remote"}},
		{"this machine only", []string{index.LocalOrigin}, []string{"hyp-local"}},
		{"one remote host", []string{"h2"}, []string{"hyp-remote"}},
		{"both named", []string{index.LocalOrigin, "h2"}, []string{"hyp-local", "hyp-remote"}},
		{"a host with no rows", []string{"h-absent"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := idx.FrontierSearch(ctx, index.FrontierQuery{
				Match: shared, Origins: tc.origins, Order: index.OrderOldest,
			})
			if err != nil {
				t.Fatalf("FrontierSearch: %v", err)
			}
			if fmt.Sprint(ids(hits)) != fmt.Sprint(tc.want) {
				t.Errorf("hits = %v, want %v", ids(hits), tc.want)
			}
		})
	}
}

// Forgetting a host is a cache eviction and must be impossible to aim at the
// durable half by accident: LocalOrigin is the zero value, so an unset argument
// would otherwise delete this machine's own analysis from its own index.
func TestForgetFrontierOriginRefusesTheLocalPartition(t *testing.T) {
	ctx := context.Background()
	idx := openIndex(t)

	local := frontierOutput(frontier.OutputHypothesis, "hyp-local", "held durably here")
	remote := frontierOutput(frontier.OutputHypothesis, "hyp-remote", "held by another machine")
	if _, err := idx.IndexFrontier(ctx, []frontier.Output{local}); err != nil {
		t.Fatalf("IndexFrontier: %v", err)
	}
	if _, err := idx.IndexFleetFrontier(ctx, "h2", []frontier.Output{remote}); err != nil {
		t.Fatalf("IndexFleetFrontier: %v", err)
	}

	if _, err := idx.ForgetFrontierOrigin(ctx, index.LocalOrigin); !errors.Is(err, index.ErrFrontierOrigin) {
		t.Errorf("forgetting the local partition gave %v, want ErrFrontierOrigin", err)
	}
	if _, err := idx.IndexFleetFrontier(ctx, index.LocalOrigin, nil); !errors.Is(err, index.ErrFrontierOrigin) {
		t.Errorf("a fleet reconcile of the local partition gave %v, want ErrFrontierOrigin", err)
	}

	removed, err := idx.ForgetFrontierOrigin(ctx, "h2")
	if err != nil {
		t.Fatalf("ForgetFrontierOrigin: %v", err)
	}
	if removed != 1 {
		t.Errorf("forgot %d rows, want 1", removed)
	}
	if origins, err := idx.FrontierOrigins(ctx); err != nil {
		t.Fatalf("FrontierOrigins: %v", err)
	} else if origins[index.LocalOrigin] != 1 || len(origins) != 1 {
		t.Errorf("origins = %v, want only this machine's row", origins)
	}
}

func ids(hits []index.FrontierHit) []string {
	var out []string
	for _, hit := range hits {
		out = append(out, hit.ID)
	}
	return out
}
