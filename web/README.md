Install/build: `bun install && bun run build`
Dev: `bun run dev`
Mock: `bun run mock`

The mock simulates the background catalog scan so the sessions page can be
previewed without the Go server. Select a scenario with `MOCK_SCAN`:
`running` (default, cold cache that fills in one describe per poll), `error`
(scan fails part-way), `idle` (warm cache, no scan), `empty` (cold cache with no
scan running).

Phase B fixtures (Explore, Hypotheses, Findings, Reality, Review) are served
by the same mock with stateful answer/decide/accept flows. `MOCK_PHASEB=rich`
(default) includes the awkward cases — a rejected hypothesis, fifty
observations, conflicting evidence, a plan awaiting acceptance, hostile
HTML/Markdown/URL/terminal-control content, and unbroken kilocharacter
tokens; `MOCK_PHASEB=empty` presents the day-one empty frontier, queue, and
inbox.
