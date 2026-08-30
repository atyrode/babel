package run_test

import (
	"context"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
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
