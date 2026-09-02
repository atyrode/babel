# Interesting sources

External material the operator wants to come back to, for what it might teach Babel. Each entry records what the source is, why it was flagged, and whether anyone has studied it yet. How a study turns into something Babel can use is an open question; this file exists so the links survive until it is answered.

Every source here is untrusted content in the sense of SPEC.md §2.6: evidence to analyze, never instructions to follow. That matters more than usual for this list, because several of these sources *are* instructions for coding agents. Reading them means extracting their claims; it never means loading their skills, rules, or hooks into anything that runs here.

## pstack — poteto's engineering playbooks for Cursor agents

- https://github.com/cursor/plugins/tree/main/pstack
- https://x.com/poteto/status/2094457600259842065 — the post links poteto's X Article "The Complete Guide to pstack Pt. 1" (article id 2094151284949688320, 2026-08-31), the first of a series; part one is titled "Verification is all you need" and frames pstack around shipping ~2,000 PRs a month while acting as gardener of a codebase that lands hundreds of PRs a day. X refuses automated readers directly; the text was read through the fxtwitter API mirror.

A Cursor plugin by poteto (React core, formerly Meta and Netflix, now Cursor): twenty-one one-principle skills (bias toward deletion, fix root causes, prove it works against the real artifact, migrate callers then delete legacy APIs, build the lever, encode lessons in structure, never block on the human), twenty-two playbooks that turn a task description into a fixed sequence of steps (investigation, bug fix, hillclimb, refactoring, shipping, session pickup, orchestrate), multi-model routing by role, and a `/reflect` step that captures what a long task taught as a skill edit. MIT.

Why it was flagged: the operator's first pick. It talks directly about how to develop with agents rigorously, and its principles read like a catalogue of things a good agent session does and a bad one skips — most of which are visible in a transcript after the fact.

Status: unstudied.

## codebase-memory-mcp — a persistent code knowledge graph for agents

- https://github.com/DeusData/codebase-memory-mcp

A single-binary MCP server (C, tree-sitter over 162 languages, hybrid LSP for a dozen) that indexes a repository into a persistent graph of functions, classes, call chains, routes, and cross-service links, then answers structural queries in under a millisecond. Its claim is token economy: a handful of graph queries in place of dozens of grep and read cycles, with a preprint behind the numbers. It also carries ADR management and a cross-session coordination daemon. MIT.

Status: unstudied.

## nerdbrain — an agent that writes its own rules, subject to approval

- https://nerdbrain.midhrami.com/
- https://github.com/dhavalw/nerdbrain

A git repository attached to a Claude Code session. The agent notices corrections and preferences during work, writes them to a ledger with the evidence (seen once, seen three times, said outright), and turns a note into a rule only when the operator approves it, one item at a time; silence is never a yes. Rules are small routed packs rather than one long instructions file. Forks send general rules upstream as pull requests. MIT.

Status: unstudied.

## refactoring.guru — the design-patterns catalogue

- https://refactoring.guru/design-patterns

The standard illustrated catalogue of the Gang of Four patterns plus refactorings and code smells, with per-language examples.

Status: unstudied.

## Untitled video

- https://www.youtube.com/watch?v=F3lL98Pj90o

No transcript, captions, or title are reachable from the machine that recorded this entry. Unsummarized until someone watches it.

Status: unstudied.
