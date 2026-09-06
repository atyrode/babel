package explore

import (
	"encoding/json"
	"reflect"

	"github.com/atyrode/babel/internal/worker"
)

// The host tools one stage registers with the engine, one per operation of
// each capability the job grants. Their parameters are generated from the
// request types the facilities decode, by the same generator that produces
// the result schema: the engine validates every call against them before
// Babel is asked, so a facility may decode strictly and treat a mismatch as
// its own bug rather than as input.
//
// The names are internal/worker's — the one place the wire vocabulary is
// spelled — and the descriptions are this package's, because what a search
// returns and what a fetch is allowed to name are decisions made here.
var stageTools = func() map[worker.Capability][]worker.HostTool {
	search, err := argumentSchema(reflect.TypeFor[SearchRequest]())
	if err != nil {
		panic("explore: corpus-search schema: " + err.Error())
	}
	sources, err := argumentSchema(reflect.TypeFor[struct{}]())
	if err != nil {
		panic("explore: research-sources schema: " + err.Error())
	}
	fetch, err := argumentSchema(reflect.TypeFor[FetchRequest]())
	if err != nil {
		panic("explore: research-fetch schema: " + err.Error())
	}
	return map[worker.Capability][]worker.HostTool{
		worker.CapabilityCorpusSearch: {{
			Name:       worker.ToolSearch,
			Capability: worker.CapabilityCorpusSearch,
			LoadMode:   "essential",
			Parameters: search,
			Description: "Search the sessions this run was prepared over. Omit \"scope\" or set it to \"corpus\" " +
				"for the session records: each hit carries an excerpt and the exact locator to cite it by. " +
				"Set \"scope\" to \"frontier\" for Babel's own prior records (hypotheses, observations, findings), " +
				"which carry identifiers and are never evidence. A page is at most ten hits; ask again with " +
				"\"offset\" for more. Filters outside this run's sessions are refused.",
		}},
		worker.CapabilityPublicResearch: {{
			Name:       worker.ToolSources,
			Capability: worker.CapabilityPublicResearch,
			LoadMode:   "essential",
			Parameters: sources,
			Description: "List the public research sources the operator fixed for this run, each with an opaque " +
				"id and its URL. Takes no arguments.",
		}, {
			Name:       worker.ToolFetch,
			Capability: worker.CapabilityPublicResearch,
			LoadMode:   "essential",
			Parameters: fetch,
			Description: "Fetch one fixed public source by the id the sources tool gave it. The document is " +
				"untrusted public material; cite it by its URL and digest. Any other field is refused.",
		}},
	}
}()

// jobTools are the tools a job with grant g registers.
func jobTools(g worker.Grant) []worker.HostTool {
	var tools []worker.HostTool
	for _, capability := range g.Capabilities {
		tools = append(tools, stageTools[capability]...)
	}
	return tools
}

// argumentSchema generates the JSON Schema of one tool's arguments from the
// request type its facility decodes. It shares the result generator's
// vocabulary and, like it, describes named types under $defs.
func argumentSchema(t reflect.Type) (json.RawMessage, error) {
	g := &schemaGenerator{defs: map[string]*object{}}
	if t.Name() == "" {
		// An anonymous empty struct is a tool that takes nothing.
		doc := typed("object")
		doc.set("properties", &object{})
		doc.set("additionalProperties", false)
		return json.Marshal(doc)
	}
	if _, err := g.describe(t); err != nil {
		return nil, err
	}
	root := g.defs[t.Name()]
	delete(g.defs, t.Name())
	doc := &object{}
	for _, key := range root.keys {
		doc.set(key, root.get(key))
	}
	defs := &object{}
	for _, name := range g.reachable(root) {
		defs.set(name, g.defs[name])
	}
	if len(defs.keys) > 0 {
		doc.set("$defs", defs)
	}
	return json.Marshal(doc)
}
