package frontier_test

// The one thing internal/frontier cannot check about itself: that the record
// namespace a filing's `about` edge points into is the namespace this
// deployment's resolver registry can actually vouch for (SPEC.md §4.13, #113).
//
// It is an external test package because internal/reference/resolve imports
// internal/frontier to build the registry, so the check cannot live beside the
// constant it is checking. What it proves is worth the file: the reference
// store refuses an endpoint in a namespace nothing resolves, so a frontier
// that spelled the ledger's namespace even slightly differently would mint no
// edge at all and would report it as a warning nobody reads.

import (
	"context"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/reference"
	"github.com/atyrode/babel/internal/reference/resolve"
)

func TestAnAboutEdgeBindsToTheLedgerNamespaceTheRegistryResolves(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	t.Cleanup(func() { front.Close() })
	ledger, err := reality.Open(t.TempDir())
	if err != nil {
		t.Fatalf("reality.Open: %v", err)
	}
	t.Cleanup(func() { ledger.Close() })

	registry, err := resolve.Registry(resolve.Stores{Frontier: front, Reality: ledger})
	if err != nil {
		t.Fatalf("resolve.Registry: %v", err)
	}
	edges, err := reference.Open(t.TempDir(), reference.WithResolvers(registry))
	if err != nil {
		t.Fatalf("reference.Open: %v", err)
	}
	t.Cleanup(func() { edges.Close() })

	// A refusal is a warning rather than an error on the write path, so the
	// diagnostics are the assertion: an unresolvable endpoint would leave the
	// filing durable and the graph empty, which is exactly the silent failure
	// this test exists to catch.
	var warnings []error
	filing, err := frontier.Open(dir, frontier.WithReferences(edges, func(err error) {
		warnings = append(warnings, err)
	}))
	if err != nil {
		t.Fatalf("frontier.Open with references: %v", err)
	}
	t.Cleanup(func() { filing.Close() })

	entity, err := ledger.CreateEntity(ctx, reality.EntityInput{
		Kind:    reality.EntityRepository,
		Payload: reality.EntityPayload{DisplayName: "manifold"},
	})
	if err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}
	record, err := filing.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "run-1",
		Actor:   frontier.Run("run-1"),
		Payload: frontier.HypothesisPayload{Statement: "the release pipeline retries on a stale lock"},
	})
	if err != nil {
		t.Fatalf("CreateHypothesis: %v", err)
	}
	if _, err := filing.File(ctx, frontier.FilingInput{
		Record:    frontier.Ref{Type: frontier.EntityHypothesis, ID: record.ID},
		EntityID:  entity.ID,
		Rationale: "the run read this repository",
		Author:    frontier.FilingOperator,
		AuthorID:  "alex",
	}); err != nil {
		t.Fatalf("File: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("the about edge was refused: %v", warnings)
	}

	cited, err := edges.From(ctx, reference.RecordRef{Kind: "hypothesis", ID: record.ID})
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	var about []reference.Edge
	for _, edge := range cited {
		if edge.Kind == reference.KindAbout {
			about = append(about, edge)
		}
	}
	if len(about) != 1 {
		t.Fatalf("the record cites %d topics, want one: %+v", len(about), cited)
	}
	if about[0].To.Kind != resolve.NamespaceRealityEntity {
		t.Errorf("the filing points into namespace %q, want the one the registry resolves (%q)",
			about[0].To.Kind, resolve.NamespaceRealityEntity)
	}
	if about[0].To.ID != entity.ID {
		t.Errorf("the filing points at %q, want the entity %q", about[0].To.ID, entity.ID)
	}
}
