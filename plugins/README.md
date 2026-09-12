# Babel's manifold plugins

Babel **is** this directory. Decision 91 (2026-09-12, SPEC.md §2.8, `docs/manifold-plan.md`)
retired the standalone product: the hub owns Babel's state, its pages are panels in the shell,
its runs are jobs on enrolled machines, and the Go tree under `internal/` is the reference for
behaviour until P7 retires it. One **baseline** plus independently enable-able **parts**, each a
directory, each packed as one `<id>.manifold-plugin.json` and installed at
`engine.plugins.install` by hash (manifold `docs/PLUGINS.md` §10).

| Plugin                | Directory                | Halves       | What it is                                                                                                                                                                   |
| --------------------- | ------------------------ | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `atyrode.babel`       | `atyrode.babel/`         | server + web | The baseline: the store (one SQLite file of its own), the nine read doors, the operator's acts, the machine operations and the conductor. Contributes the five event kinds and no panel. |
| `atyrode.babel.feed`  | `atyrode.babel/feed/`    | web          | Home — every record Babel produced, ranked by what needs the operator — the peeled record, and a topic with its filings and his interest. Panels `home`, `record`, `topic`.     |
| `atyrode.babel.watch` | `atyrode.babel/watch/`   | web          | What is running and what will run: presets instead of flags, the model and the ceiling up front, the live pulse, the receipt afterwards. Panel `watch`.                         |

A part is a directory inside its parent's and says so with
`dependencies: { "atyrode.babel": { type: "required" } }`; assembly refuses it otherwise. The
baseline is not a library — a part reaches it only through its doors (`host.client.action`) —
and every id, door name, event kind, panel id, preset, job output file and receipt field is
spelled once, in `atyrode.babel/contract.ts`, with the tables in
`atyrode.babel/store/schema.ts`. `test/contract.test.ts` pins every manifest to those two.

The web halves are **in-realm React** (`docs/PLUGINS.md` §10): `web.tsx` default-exports
`{ id, panels }` of ordinary components on `@manifold/ui`'s layout primitives, with the skin in
a `styles.css` whose every selector is rooted at the plugin's own class. The server half is
authored against the kit (`@manifold/plugin-kit/server`: `defineServerAction`, `GuestCtx`), which
the in-realm loader takes as it stands — one authoring shape for a row that may later be
hardened.

## The store is rows, not keys

The baseline's data is a graph — 65,818 records, 60,793 links, filings, per-role tallies, a
ledger of entities and facts — read by filter, sort, join and aggregate on every page, so it
lives in the plugin database (manifold ADR 0034, `docs/PLUGINS.md` §4 "Your tables"): one SQLite
file at `<data>/plugins/atyrode.babel/data.db`, asked for by `database: { maxBytes }` in the
manifest and served as `ctx.database`. Three consequences worth knowing before reading
`server.ts`:

- **The shape is made by the enable hook, not by a migration.** A fresh install has no stored
  data version, and `planDataMigration` answers `ok` for that case — a migration chain exists for
  a MAJOR bump over data that already exists. So `onEnable` runs the 58 statements of `SCHEMA_V1`
  as one `batch` (all or none) when the file has no tables, and records the shape's name,
  `2026-09-12-store-v1`, as a storage key for the next one to read.
- **`batch` is the transaction.** There is no open handle: read, decide, then a batch whose first
  statements are its own guards. Bounds are the engine's — 10,000 rows and 4 MiB a call, 256
  statements a batch, a 5-second deadline — and every refusal is a rejection.
- **Purge is the file.** A disable retains it, an uninstall refuses while it holds pages, and a
  purge deletes `data.db` with its `-wal` and `-shm`. `bun run verify` asserts both halves of
  that: the file exists once the doors have answered, and is gone after the purge.

## The machine half: one bundled file, two runtime tools the machine provides

A run is a **job on an enrolled machine** (manifold `docs/PLUGINS.md` §8), and the baseline's
manifest carries the `machine` block that says what may run there. `atyrode.babel/machine/` is
the half that runs: one dispatcher, `main.ts`, behind one command line —

```
bun /job/artifact <operation> --input /inputs/input --out /outputs/outputs
```

— which is literally the `argv` every operation declares. `pack.sh` builds that half with
`bun build --target bun` into `atyrode.babel/machine.js`, stamps its sha256 into **both**
platform artifacts of the manifest (a `raw` artifact is its own entry, so `sha256` and
`entrySha256` are one digest), and packs
it as a `bundleFile` member of the baseline's bundle. The file itself is never committed —
`.gitignore` has it, a pack deletes it afterwards, `bun run dev` keeps it (`./pack.sh --machine`)
because the inner loop re-packs on every save. The committed manifest carries the **last stamp**,
so a change to the machine half shows up as a moved hash in the diff; `test/bundle.test.ts` runs
the packed member through the argv the manifest declares and checks the receipt it leaves.

The operations are `scan`, `prepare`, `explore` and `evaluate`. Each takes ONE input document —
a JSON string in the job request, which the engine materializes as a file at `/inputs/input`, so
`--input` is a path and never 64 KiB of argv — and writes every file it produced flat into the
sealed output lease at `/outputs/outputs`, which the hub reads back with `ctx.jobs.outputs`. The
lease is cut from a **managed** location: the alternative, an ordinary anchor, must already exist
on the machine for `write` and refuses a second job for `create`. `scan` and `prepare` run with
`network: "none"`; `explore` and `evaluate` reach the host because they launch the engine.

Two tools are **runtime tools the machine's owner provides, not artifacts this manifest pins**:
`bun`, the executable every operation runs, and `code`, the engine `explore` and `evaluate`
launch. A Manifold job sandbox is built from `/proc`, `/dev`, the job's own directories and the
declared binds and nothing else — no `/lib64`, no libc — and manifold's own rule is that "a
dynamically linked executable without its loader cannot run in the empty sandbox"
(`docs/SELF-HOST.md`). Neither bun nor code ships a static build (the musl bun is dynamic
against `ld-musl` too), so a pinned artifact could be downloaded and verified and still die at
`execvp` — which is exactly what happened on the first real job. The owner's
`execution.runtimeToolClosures` binds a tool WITH its exact closure at Nix build time, per
machine, under the alias the operation names; the manifest therefore declares
`runtimeTools: ["bun"]` / `["bun", "code"]` and no `tools` block, so a declared artifact can never
shadow the owner's binding. For the operator's fleet that is one dotfiles module:

```nix
services.manifold.execution = {
  runtimeTools.bun = [{ source = "${pkgs.bun}/bin/bun"; target = "/runtime/bin/bun"; kind = "file"; }];
  runtimeToolClosures.bun = [ pkgs.bun ];
  runtimeTools.code = [{ source = "${code}/bin/code"; target = "/runtime/bin/code"; kind = "file"; }];
  runtimeToolClosures.code = [ code ];
};
```

**`archive` is not declared, and `restic` is not pinned.** Upstream's whole Linux distribution is
bare bzip2 — `restic_0.19.1_linux_amd64.bz2` (10,107,515 bytes) and
`restic_0.19.1_linux_arm64.bz2` (9,044,264 bytes), with no tar or zip of either — while
`MachineArtifactSchema` takes `raw`, `zip` or `tar.gz` and nothing else. The second thing missing
is a way to hand the operation its repository password: `machine/restic.ts` reads it from
`BABEL_RESTIC_PASSWORD`, and an operation's `environment` is fixed reviewed values in the
manifest, which is not where a secret goes. `machine/archive.ts` and `machine/restic.ts` are
written and tested; the operation returns when manifold admits a `.bz2` artifact (or a `tar.gz` of
that binary is hosted where the operator will trust it) AND a job can be given a credential — not
before, because a pin nobody obtained by download is not a pin, and a password in a manifest is
not a password.

`git` is the third runtime tool, for `scan` and `prepare`: repository identity is `git rev-parse
--git-common-dir` in the session's workspace. Bound the same way, it runs - and today still answers
nothing, because a job sees only its declared locations and the workspaces a session names are host
paths outside them (#254 records the decision this needs).

Two more things an enrolled machine's operator must arrange, because a manifest cannot: the
`home` anchor needs `~/.omp/agent/sessions`, `~/.codex` and `~/.claude` to **exist** (a job whose
read location is missing fails to start; `mkdir -p` is the whole fix), and the `runtime` anchor
must be a dedicated bounded tmpfs, since the named-output lease is cut from it.

## The SDK is a sibling checkout, for now

`@manifold/plugin-kit`, `@manifold/protocol`, `@manifold/plugin` and `@manifold/ui` are private
workspace packages of [atyrode/manifold](https://github.com/atyrode/manifold); nothing publishes
them. Until the kit ships as a release asset, a checkout **beside this repository** at the
revision in `MANIFOLD_REV` is the SDK:

```
<parent>/
  babel/plugins/      this directory
  manifold/           atyrode/manifold @ $(cat MANIFOLD_REV), with `bun install` run
```

```sh
git clone https://github.com/atyrode/manifold ../../manifold
git -C ../../manifold checkout "$(cat MANIFOLD_REV)"
bun install --cwd ../../manifold --frozen-lockfile   # the kit resolves zod and the protocol from its workspace
```

`MANIFOLD_REV` is currently `ea675d21`, which carries the plugin database (ADR 0034) — the
primitive Babel cannot start without, and a branch rather than a release, which is why the
checkout on dev-01 is named `manifold-db`. `tsconfig.json` therefore lists **two candidates** for
every `@manifold/*` alias, `../../manifold-db` before `../../manifold`, and tsc and Bun take the
first that exists; `pack.sh` resolves `../../manifold` and honours `MANIFOLD_DIR` for a tree that
keeps the checkout elsewhere (an isolated worktree, a second branch). The pin and the workflow's
`uses:` ref are one revision and are bumped together.

`bun install` here fetches only what typechecking and tests need: `zod` (pinned to the kit's own
version, and the one thing inlined into every bundle), `typescript`, React with its types, and
`happy-dom` — the document a panel test mounts a React panel into, test-only and in no bundle.
React, `@manifold/plugin` and `@manifold/ui` are **shared externals**, rewritten by `pack` into
reads from the shell's own module registry, so a bundle never carries a second copy of them.

## Build, test, pack, verify, develop

```sh
bun install                 # zod, typescript, react + types, happy-dom; nothing else
bun run check               # tsc over both halves, the store, the panels and the tests
bun test                    # the manifests against the contract, the doors against a real temporary database, the panels in a document, and `pack` itself
bun run pack                # builds machine.js, stamps the manifest, dist/<id>.manifold-plugin.json per manifest, parents first, plus dist/SHA256SUMS
bun run verify              # every bundle installed on a real engine spawned from the checkout
bun run dev -- --hub http://127.0.0.1:7912 --deliver docker:manifold-dev-manifold-1
```

`verify` is the kit's own (`docs/PLUGINS.md` §9 Verifying): it spawns the sibling checkout's
server on a temporary data directory and a free port, installs each bundle parents first,
requires the roster row enabled and not `enable_failed`, dispatches every door it publishes with
`{}` as the owner and refuses `unavailable`, asserts the declared database file exists, then
uninstalls with purge and asserts it is gone. `dev` is the inner loop: it packs every manifest
under this directory, installs the baseline before its parts on the hub named by `--hub`, then
watches for edits and reinstalls only the bundles whose sha changed; a browser reload shows the
change. The line above is the integrated preview (`https://preview.manifold.tyrode.dev`) as
addressed from dev-01, where `docker:` delivery copies the bundle into the hub's container and
reads the owner key from its volume, so the key never appears in argv, output or this repository.
Against another hub, pass `--owner-key-file <path>` and `--deliver path`.

## Where a change is proved, and how production gets it

**Every change is proved on a preview**, never on production: the integrated preview or a local
preview-equivalent hub, by the loop above. `manifold.tyrode.dev` is installed by the operator, by
hand, from the plugin manager — nothing here automates it, and nothing should.

`.github/workflows/manifold-plugins.yml` is one `uses:` line: manifold's reusable
`plugins.yml@<MANIFOLD_REV>` checks this repository out beside `atyrode/manifold` at the pinned
revision — the layout above, so `tsconfig.json` and `pack.sh` resolve exactly as they do on a
developer's machine — then runs `check`, `test`, `pack` and `verify` and uploads `dist/` as the
`manifold-plugins` artifact. It runs on every push to a `plugin/**` branch and on every pull
request touching `plugins/`.

Release delivery is not wired yet, and this file will not pretend it is: `release.yml` releases
Babel's own binary and says in its own comment why the plugin jobs were removed. The shape to
restore, when the cutover reaches P7, is `atyrode/code`'s: a `v*` tag attaches
`dist/*.manifold-plugin.json` and their sums to the GitHub Release and hands each asset URL and
sha to the preview hub's receiver, baseline before parts, so the preview installs the release by
itself. Until then a preview gets a build through `bun run dev --deliver`, and the sha256 that
counts is the one CI prints.

The sha256 is over an artifact's exact bytes, and Bun writes every bundled module's path as a
comment, so a hash reproduces only from the layout above with the same Bun. The pins are what
`engine.plugins.install` demands; `dist/SHA256SUMS` is what carries them.
