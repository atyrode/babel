package review_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/atyrode/babel/internal/frontier"
	"github.com/atyrode/babel/internal/review"
	"github.com/atyrode/babel/internal/run"
)

// TestThreeComponentsShareOneDurableFile extends internal/run's coexistence
// test to the third writer. The durable database is shared by agreement rather
// than by a shared type — one file, one `schema_migration(component, version)`
// ledger, one table prefix per component — so the agreement needs a test that
// fails if any side changes its ledger key or its prefixes. Opening all three
// against one directory in either order is the whole of the contract.
func TestThreeComponentsShareOneDurableFile(t *testing.T) {
	ctx := context.Background()

	open := func(t *testing.T, order []string) {
		t.Helper()
		dir := t.TempDir()
		var (
			front *frontier.Store
			runs  *run.Store
			svc   *review.Service
			err   error
		)
		for _, which := range order {
			switch which {
			case "frontier":
				if front, err = frontier.Open(dir); err != nil {
					t.Fatalf("frontier.Open: %v", err)
				}
				t.Cleanup(func() { front.Close() })
			case "run":
				if runs, err = run.Open(dir); err != nil {
					t.Fatalf("run.Open: %v", err)
				}
				t.Cleanup(func() { runs.Close() })
			case "review":
				if svc, err = review.Open(dir, front, runs); err != nil {
					t.Fatalf("review.Open: %v", err)
				}
				t.Cleanup(func() { svc.Close() })
			}
		}
		if svc.Path() != front.Path() || svc.Path() != runs.Path() {
			t.Fatalf("stores disagree on the file: %q, %q, %q", svc.Path(), front.Path(), runs.Path())
		}
		// Each must still work with the others' tables present.
		hyp, err := front.CreateHypothesis(ctx, frontier.HypothesisInput{
			RunID:   "run-1",
			Payload: frontier.HypothesisPayload{Statement: "synthetic"},
		})
		if err != nil {
			t.Fatalf("frontier write with review tables present: %v", err)
		}
		if _, err := svc.Enroll(ctx, frontier.Ref{Type: frontier.EntityHypothesis, ID: hyp.ID}); err != nil {
			t.Fatalf("review write with frontier and run tables present: %v", err)
		}
	}

	// The review component needs the other two, so it is always last; what
	// varies is the order of the two it depends on, since nothing orders
	// them.
	t.Run("frontier first", func(t *testing.T) { open(t, []string{"frontier", "run", "review"}) })
	t.Run("run first", func(t *testing.T) { open(t, []string{"run", "frontier", "review"}) })
}

// TestProposingAndAuthorizingAreDifferentTypes is §4.7's "the refinement agent
// may propose but never authorize lasting context", checked where it is
// enforced: in the signatures.
//
// A test cannot compile the mistake it is preventing, so it asserts the
// property that makes the mistake uncompilable — the identity a refinement
// outcome carries and the identity a disposition requires are distinct,
// unconvertible named types, and the method that decides a durable-learning
// proposal accepts only the latter.
func TestProposingAndAuthorizingAreDifferentTypes(t *testing.T) {
	agent := reflect.TypeOf(review.Agent{})
	authority := reflect.TypeOf(review.Authority{})

	if agent == authority {
		t.Fatal("Agent and Authority are the same type")
	}
	if agent.ConvertibleTo(authority) || authority.ConvertibleTo(agent) {
		t.Fatal("Agent and Authority are interconvertible: an agent identity could be passed as an operator's")
	}

	input, ok := reflect.TypeOf(review.RefinementInput{}).FieldByName("Agent")
	if !ok {
		t.Fatal("RefinementInput has no Agent field")
	}
	if input.Type != agent {
		t.Fatalf("RefinementInput.Agent is %s, want review.Agent", input.Type)
	}

	dispose, ok := reflect.TypeOf(&review.Service{}).MethodByName("DisposeMemory")
	if !ok {
		t.Fatal("Service has no DisposeMemory method")
	}
	var takesAuthority bool
	for i := range dispose.Type.NumIn() {
		if dispose.Type.In(i) == authority {
			takesAuthority = true
		}
		if dispose.Type.In(i) == agent {
			t.Fatal("DisposeMemory accepts a refinement agent identity")
		}
	}
	if !takesAuthority {
		t.Fatal("DisposeMemory does not require an operator identity")
	}

	decide, ok := reflect.TypeOf(&review.Service{}).MethodByName("Decide")
	if !ok {
		t.Fatal("Service has no Decide method")
	}
	by, ok := reflect.TypeOf(review.Decision{}).FieldByName("By")
	if !ok {
		t.Fatal("Decision has no By field")
	}
	if by.Type != authority {
		t.Fatalf("Decision.By is %s, want review.Authority", by.Type)
	}
	_ = decide
}
