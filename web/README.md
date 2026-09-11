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

The dashboard reads one aggregate endpoint, `/api/overview`. `MOCK_OVERVIEW=healthy`
(default) answers every panel; `MOCK_OVERVIEW=degraded` takes the archive and
catalog panels away, and combines with `MOCK_UNWIRED` to preview a launch where
neither storage nor analysis state is available — the state a first launch is in.

The evaluation surface (`/evaluation`) has its own fixtures in
`mock/evaluation.ts`, with stateful policy and operator-record flows.
`MOCK_EVALUATION=rich` (default) carries the cases the interface has to render
honestly — a bare vote with no prose, a record nobody reviewed, a role with no
evaluator, a role supported but not yet required, a verified outcome with a
later contradiction, two grouped remedies, a superseded revision, a Reconsider
item, and enough rows to page; `empty` is the day-one state; `degraded` is a
stale projection that still answers and says so; `running` shows claimed work
in flight, which is the one state an enabled policy cannot produce by itself.
`MOCK_UNWIRED=evaluation` refuses the whole surface the way a launch with no
evaluation projection does.
