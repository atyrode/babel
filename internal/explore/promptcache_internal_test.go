package explore

import (
	"strings"
	"testing"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/worker"
)

// Two runs of the same stage share a cache-eligible prompt prefix.
//
// A provider serves a cached prompt only for a byte-identical prefix, and a
// stage's largest stable block by far is the recipe bodies it carries
// verbatim. While the parameter block — which names this run and nothing else
// — sat in front of them, the shared prefix ended at the first run identifier
// and every stage of every run paid to write the recipes into cache again. On
// a real overnight batch that was 5.2M cache-write tokens against 27M reads.
//
// What this pins is the property rather than the byte count: everything that
// does not vary with the run precedes everything that does, so the recipes are
// inside the shared prefix and the run's own identifiers are outside it.
func TestStagePromptsShareTheirRunInvariantPrefix(t *testing.T) {
	set, err := cookbook.Embedded()
	if err != nil {
		t.Fatalf("Embedded: %v", err)
	}
	recipes := set.Defaults()
	if len(recipes) == 0 {
		t.Fatal("the embedded cookbook is empty")
	}
	contract := OutputContract(StageExplore)
	tools := []worker.HostTool{{Name: worker.ToolSearch, Description: "search the corpus"}}

	compose := func(run string) string {
		return composePrompt(StageExplore, contract, recipes,
			[]worker.Source{{Kind: "session", Selector: "omp/" + run, Digest: "sha256:" + run}},
			map[string]string{"run_id": run, "preparation_id": "prep-" + run},
			&RelatedContext{Framing: "prior work", Records: []RelatedRecord{
				{Kind: "hypothesis", ID: "hyp_" + run, Summary: "a prior idea"},
			}}, tools)
	}

	first, second := compose("run-A"), compose("run-B")
	shared := commonPrefix(first, second)
	t.Logf("shared prefix %d bytes of %d; run-specific tail %d bytes",
		len(shared), len(first), len(first)-len(shared))

	// The recipes are the block worth caching, so they must be inside it.
	for _, recipe := range recipes {
		if !strings.Contains(shared, strings.TrimSpace(recipe.Body)) {
			t.Errorf("recipe %s is outside the cache-eligible prefix", recipe.ID)
		}
	}
	// And the run's own identity must be outside it, or the prefix would not
	// be shared at all.
	if strings.Contains(shared, "run-A") {
		t.Error("the shared prefix carries a run identifier")
	}
	// A prefix that stops before the instructions would satisfy the two checks
	// above vacuously if the recipes were empty; they are not, and this is the
	// number that pays: the stable half has to dominate.
	if len(shared) <= len(first)/2 {
		t.Errorf("only %d of %d bytes are cache-eligible; the stable half should dominate",
			len(shared), len(first))
	}
}

func commonPrefix(a, b string) string {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return a[:i]
		}
	}
	return a[:n]
}
