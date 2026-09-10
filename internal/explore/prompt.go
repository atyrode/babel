package explore

import (
	"fmt"
	"sort"
	"strings"

	"github.com/atyrode/babel/internal/cookbook"
	"github.com/atyrode/babel/internal/worker"
)

// The prompt is the whole of what the model is told, composed here from
// Babel-owned parts: the stage's instructions, the recipes selected for it
// verbatim, the approved sources, the brief's identifiers, the refine-first
// context and the tools. Nothing about how to prompt lives in Code; Code
// forwards the engine and the engine reads this.
//
// Order is load-bearing for cost, not for meaning. Everything a provider can
// serve from its prompt cache has to be a byte-identical prefix, so this
// composes the run-invariant half first — stage instructions, the tool list,
// then the recipe bodies, which are the largest stable block and are sorted
// by id upstream — and the run's own half after it: parameters naming this
// run, the sessions it was prepared over, and the prior records it may
// refine. Two runs of the same stage over the same recipes now share that
// prefix, and three stages of one run share it with each other, where before
// the parameter block sat in front of the recipes and every run paid to
// write the whole thing again.
//
// Two of its sections are machine-readable on purpose. The `[babel-params]`
// block lists the run's parameters one per line, which is how a result can
// name the identifiers Babel minted for the brief, and the sources section
// names each session by the selector the search tool filters on. Both are
// plain text a model reads and a fixture can parse; neither is a protocol.

// paramsOpen and paramsClose delimit the parameter block.
const (
	paramsOpen  = "[babel-params]"
	paramsClose = "[end]"
)

// composePrompt renders one stage's prompt.
func composePrompt(stage Stage, contract worker.OutputContract, recipes []*cookbook.Recipe,
	sources []worker.Source, params map[string]string, related *RelatedContext, tools []worker.HostTool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Babel %s stage\n\n", stage)
	b.WriteString(contract.Instructions)
	b.WriteString("\n")

	b.WriteString("## How to answer\n\n")
	b.WriteString("Work with the tools below, then call `" + worker.ToolSubmit + "` with the complete result. ")
	b.WriteString("Its arguments are validated against the result schema before Babel sees them, and Babel then checks ")
	b.WriteString("the refs within the result, the recipes cited, and every evidence locator against what this run was ")
	b.WriteString("served; a refusal explains what to fix, and calling again replaces the earlier submission. ")
	b.WriteString("Acceptance means the submission is well formed and its citations are real. Whether each item is one ")
	b.WriteString("this stage may emit, and whether an identifier from the brief resolves, is decided when Babel persists ")
	b.WriteString("the result after your turn: an item this stage has no authority for is dropped and recorded, and the ")
	b.WriteString("items beside it are kept. End your turn once the submission is accepted. Do not write the result as prose.\n\n")

	if len(tools) > 0 {
		b.WriteString("## Tools\n\n")
		for _, tool := range tools {
			fmt.Fprintf(&b, "- `%s`: %s\n", tool.Name, tool.Description)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Recipes\n\n")
	b.WriteString("The cookbook recipes selected for this stage, verbatim. Cite one by its id and version in every claim.\n\n")
	for _, recipe := range recipes {
		fmt.Fprintf(&b, "### %s (id %s, version %d)\n\n", recipe.Title, recipe.ID, recipe.Version)
		b.WriteString(strings.TrimSpace(recipe.Body))
		b.WriteString("\n\n")
	}

	b.WriteString("## Parameters\n\n")
	b.WriteString("Every parameter this run carries, one per line. Comma-separated values are lists.\n\n")
	b.WriteString(paramsOpen + "\n")
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "%s = %s\n", key, params[key])
	}
	b.WriteString(paramsClose + "\n\n")

	b.WriteString("## Sources\n\n")
	if len(sources) == 0 {
		b.WriteString("This run was prepared over no sessions.\n\n")
	} else {
		b.WriteString("The sessions this run was prepared over, as `harness/source_id` with the capture digest. ")
		b.WriteString("The search tool reads these and nothing else.\n\n")
		for _, src := range sources {
			fmt.Fprintf(&b, "- %s %s", src.Kind, src.Selector)
			if src.Digest != "" {
				fmt.Fprintf(&b, " (%s)", src.Digest)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	if related != nil && len(related.Records) > 0 {
		b.WriteString("## Prior records\n\n")
		b.WriteString(related.Framing)
		b.WriteString("\n\n")
		for _, rec := range related.Records {
			fmt.Fprintf(&b, "- %s %s: %s\n", rec.Kind, rec.ID, rec.Summary)
		}
		b.WriteString("\n")
	}

	return b.String()
}
