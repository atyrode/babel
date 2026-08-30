package run_test

import (
	"context"
	"testing"

	"github.com/atyrode/babel/internal/explore"
	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/reality"
	"github.com/atyrode/babel/internal/run"
)

// Two packages share the durable database by agreement rather than by a shared
// type, so the agreement needs a test that fails if either side changes its
// migration ledger or table prefixes. Opening both against one directory is the
// whole of the contract.
func TestFrontierAndRunShareOneDurableFile(t *testing.T) {
	dir := t.TempDir()

	f, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	defer f.Close()

	r, err := run.Open(dir)
	if err != nil {
		t.Fatalf("run.Open after frontier: %v", err)
	}
	defer r.Close()

	// Each must still work with the other's tables present.
	if _, err := f.CreateHypothesis(context.Background(), frontier.HypothesisInput{
		RunID:   "r-1",
		Payload: frontier.HypothesisPayload{Statement: "synthetic"},
	}); err != nil {
		t.Errorf("frontier write with run tables present: %v", err)
	}
	if f.Path() != r.Path() {
		t.Errorf("stores disagree on the file: %q vs %q", f.Path(), r.Path())
	}

	// And the reverse open order must work, since nothing orders them.
	dir2 := t.TempDir()
	r2, err := run.Open(dir2)
	if err != nil {
		t.Fatalf("run.Open first: %v", err)
	}
	defer r2.Close()
	f2, err := frontier.Open(dir2)
	if err != nil {
		t.Fatalf("frontier.Open after run: %v", err)
	}
	defer f2.Close()
}

// TestEveryDurableComponentSharesOneFile extends the pairwise agreement to the
// whole set. Five packages now keep durable state in one SQLite file by
// convention rather than through a shared type: each claims a component key in
// schema_migration and a table prefix, and nothing in the type system stops two
// of them from colliding. Opening all five against one directory, in an order no
// caller is obliged to follow, is the only thing that would catch it.
func TestEveryDurableComponentSharesOneFile(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	front, err := frontier.Open(dir)
	if err != nil {
		t.Fatalf("frontier.Open: %v", err)
	}
	defer front.Close()

	runs, err := run.Open(dir)
	if err != nil {
		t.Fatalf("run.Open: %v", err)
	}
	defer runs.Close()

	real, err := reality.Open(dir)
	if err != nil {
		t.Fatalf("reality.Open: %v", err)
	}
	defer real.Close()

	ledger, err := explore.OpenLedger(dir)
	if err != nil {
		t.Fatalf("explore.OpenLedger: %v", err)
	}
	defer ledger.Close()

	// Every component must still write with the others' tables present. A
	// migration that assumed an empty database would fail exactly here.
	if _, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
		RunID:   "r-1",
		Payload: frontier.HypothesisPayload{Statement: "synthetic"},
	}); err != nil {
		t.Errorf("frontier write with every other component present: %v", err)
	}

	// And the reverse open order, since nothing orders them.
	dir2 := t.TempDir()
	l2, err := explore.OpenLedger(dir2)
	if err != nil {
		t.Fatalf("explore.OpenLedger first: %v", err)
	}
	defer l2.Close()
	r2, err := reality.Open(dir2)
	if err != nil {
		t.Fatalf("reality.Open after explore: %v", err)
	}
	defer r2.Close()
	runs2, err := run.Open(dir2)
	if err != nil {
		t.Fatalf("run.Open after reality: %v", err)
	}
	defer runs2.Close()
	f2, err := frontier.Open(dir2)
	if err != nil {
		t.Fatalf("frontier.Open last: %v", err)
	}
	defer f2.Close()
}
