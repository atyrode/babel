package explore

import (
	"context"
	"testing"

	"github.com/atyrode/babel/internal/worker"
)

// TestProductionAuthorizerAgreesWithTheSuiteOnToolNames pins the invariant
// whose absence cost Babel its first exploration.
//
// The conformance suite grades a candidate with worker.AllowWithinGrant. A real
// run authorizes through this package. When the suite's policy was the more
// permissive of the two — it inspected no tool name at all, while this one
// always has — a worker could satisfy every obligation with a tool name that
// existed nowhere in Babel, and then be denied on every request it made. That
// is not a bug this test would have caught by asserting a name; it is caught by
// asserting the two decide alike, because then no name can be right for one and
// wrong for the other.
//
// It needs neither a corpus index nor a preparation, which is the same property
// the suite needs and the reason the two share worker.ServesTool rather than
// sharing an implementation.
func TestProductionAuthorizerAgreesWithTheSuiteOnToolNames(t *testing.T) {
	suite := worker.AllowWithinGrant()
	production := &retrieval{}

	for _, tool := range []string{
		worker.ToolSearch,
		"babel_corpus_search", // the name the first real run invented for itself
		"",
		"SEARCH",
		"search ",
	} {
		req := worker.ToolRequest{Capability: worker.CapabilityCorpusSearch, Tool: tool}
		fromSuite := suite.Authorize(context.Background(), req)
		fromProduction := production.Authorize(context.Background(), req)

		// A served name gets past the name gate in both. This retrieval has no
		// index, so production stops there instead of allowing — which is the
		// point: the two must agree about the name, and only about the name.
		servedByProduction := fromProduction.Reason != worker.DenyUnservedTool(worker.CapabilityCorpusSearch, tool).Reason
		if fromSuite.Allow != servedByProduction {
			t.Errorf("tool %q: the conformance suite's policy says served=%v and a real run says served=%v; a worker cannot be graded by one and served by the other",
				tool, fromSuite.Allow, servedByProduction)
		}
		if fromSuite.Allow {
			continue
		}
		if fromSuite.Reason != fromProduction.Reason {
			t.Errorf("tool %q: suite denial reason %q, production denial reason %q; a candidate must read the same sentence in the exam it will read in a run",
				tool, fromSuite.Reason, fromProduction.Reason)
		}
	}
}

// TestUnservedCapabilityDeniesEveryToolName covers the absence representation
// from the enforcing side. A capability no facility in this build brokers has no
// entry in the published mapping, so no tool name is served under it — not even
// one another capability serves.
func TestUnservedCapabilityDeniesEveryToolName(t *testing.T) {
	for _, capability := range []worker.Capability{
		worker.CapabilityRepoRead,
		worker.CapabilitySandboxExec,
		worker.CapabilityPublicResearch,
	} {
		if worker.ServesTool(capability, worker.ToolSearch) {
			t.Errorf("%s serves %q, but nothing in this build brokers %s", capability, worker.ToolSearch, capability)
		}
		decision := worker.AllowWithinGrant().Authorize(context.Background(),
			worker.ToolRequest{Capability: capability, Tool: worker.ToolSearch})
		if decision.Allow {
			t.Errorf("%s allowed tool %q although no facility in this build serves the capability", capability, worker.ToolSearch)
		}
	}
}
