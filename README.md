# Babel

Babel is an open-ended exploratory instrument for archived conversations from OMP, Codex, and Claude Code. It helps unexpected ideas emerge about the operator's systems, code, tools, processes, interactions, and Babel itself. Analytical output is creative, fallible, and incomplete—not an automated audit or source of truth. Babel makes ideas and their evidence inspectable; it does not open issues, edit repositories, rotate credentials, or apply suggested improvements.

Babel **is** a [Manifold](https://github.com/atyrode/manifold) plugin family. It has no binary and no web application of its own: it is installed into a Manifold hub, which owns its data, runs its jobs on enrolled machines, and renders its pages as panels in its shell.

| Plugin                | Directory              | What it is                                                                                                                                                                                                          |
| --------------------- | ---------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `atyrode.babel`       | `atyrode.babel/`       | The baseline: the store (one SQLite file of its own), the read doors, the operator's acts, the machine operations that catalogue, prepare and archive sessions, and the conductor that decides what deserves a run. |
| `atyrode.babel.feed`  | `atyrode.babel/feed/`  | Home, a peeled record, and a topic with its filings — every record Babel produced, ranked by what needs the operator.                                                                                               |
| `atyrode.babel.watch` | `atyrode.babel/watch/` | What is running, what will run, and what a drain is spending: the model and the ceiling up front, the live pulse, the receipt afterwards.                                                                           |

[SPEC.md](SPEC.md) is the product and delivery specification. [docs/building.md](docs/building.md) is how the family is built, packed, verified and delivered. [docs/runbook.md](docs/runbook.md) holds the exercised recovery, custody and rollback procedures, and [docs/parity.md](docs/parity.md) records, per capability, what the retired standalone product did and whether the plugin does it.

## Build and verify

The SDK is a checkout of [atyrode/manifold](https://github.com/atyrode/manifold) beside this repository, at the revision in `plugins/MANIFOLD_REV`, with its own `bun install` run. With that in place:

```sh
cd plugins
bun install --frozen-lockfile
bun run deps:code   # atyrode/code at CODE_REV, and omp beneath it, as bundles to compose against
bun run check && bun test && bun run pack && bun run verify
```

`pack` writes one `dist/<id>.manifold-plugin.json` per manifest; `verify` installs every bundle on a disposable engine, dispatches every door it publishes, and asserts the plugin's database is created and then purged. `docs/building.md` explains the sibling layout, the two pins and the inner loop.

## Where it runs

Every change is proved on a preview hub — the integrated preview at `preview.manifold.tyrode.dev` or a local preview-equivalent — never on production. `manifold.tyrode.dev` is installed by the operator, by hand, from the plugin manager. A `v*` tag runs the same gate against the tagged revision, attaches the verified bundles to the GitHub Release and hands them to the preview's receiver.

## Core loop

1. Catalogue the machine's OMP, Codex and Claude Code sessions without modifying their source, and archive them to an encrypted restic repository. Babel fills that archive; reading it back is `restic` itself, with the repository password the deployment holds.
2. Prepare a selection: normalize the chosen sessions into one sealed material lease, each entry carrying the digest a citation has to copy.
3. Explore it. A run is a Code session posted through Code's own door: Babel composes the prompt around the material and owns no model, no thinking level and no account.
4. Keep what the run claimed — hypotheses, observations, findings, proposals and questions — as immutable rows, with the edges that relate them and the citations that ground them.
5. Rank what needs the operator and let him rule. Rulings, votes, filings, comments, answers and steering are append-only; a correction is a later row, never an edit.
6. Feed approved ideas into repositories, practices, trusted context, or Babel itself — outside Babel, by hand.

## Design principles

- **Ideas may be arbitrary; actions may not.** Candidates are cheap and permissive. Evidence, containment, and human review become stricter as an idea approaches action.
- **Local and private by default.** Raw transcripts can contain source code, credentials, personal data, and adversarial instructions. Babel's rows are plaintext on the operator's own hub, and the archive behind them is client-side encrypted at rest.
- **Public code, private data.** Babel is public and independently packageable; credentials stay with the operator. An explicitly approved model provider receives its selected input in readable form; transport encryption does not hide it from the provider.
- **Harness-agnostic sources, explicit execution worker.** OMP, Codex, and Claude Code feed one provenance model. Babel owns exploration, containment, and evidence; Code owns provider, model and thinking configuration, and the credential-isolated OMP controller beneath it.
- **The hub is the one place.** Nothing is sealed into a second store and nothing is synced to another host: a record is durable the instant it is written, and "published" is a word the schema does not need.
- **Open hypothesis space, closed action space.** Sandbox and capability boundaries constrain what analysis can affect, not what it may notice or imagine. A job sees its declared locations, the tools its machine binds, and nothing else.
- **Provenance over certainty.** Babel reliably records where an idea came from and how it was investigated; it does not promise that the idea is correct. A citation naming a locator the material does not hold is a recorded refusal.
- **Resumable rather than exhaustive.** Finite runs checkpoint unexplored hypotheses so later inference can continue without constraining initial emergence.
- **Reality is versioned, not remembered.** Stable entities and append-only, temporal, provenance-bearing facts ground attention without becoming an opaque global prompt; agent-inferred context remains proposed until a human or trusted source authorizes it.
- **Suggestions, never side effects.** Babel may render an issue draft; publishing or applying it is out of scope.
- **The recipes are a product.** Analysis recipes are versioned, reviewable assets that improve as useful and harmful patterns are learned. Their bodies live in `atyrode.babel/store/recipes.seed.json`, and a hub reads them from its own policy, so a claim can cite the exact version it was produced under.
- **Good patterns matter too.** Babel should preserve effective habits, not merely collect failures.
