Install/build: `bun install && bun run build`
Dev: `bun run dev`
Mock: `bun run mock`

The mock simulates the background catalog scan so the sessions page can be
previewed without the Go server. Select a scenario with `MOCK_SCAN`:
`running` (default, cold cache that fills in one describe per poll), `error`
(scan fails part-way), `idle` (warm cache, no scan), `empty` (cold cache with no
scan running).
