# Babel's manifold plugins

Babel **is** this directory. Decision 91 (2026-09-12, SPEC.md §2.8, `docs/manifold-plan.md`)
retired the standalone product: the hub owns Babel's state, its pages are panels in the shell,
its runs are jobs on enrolled machines, and the Go tree under `internal/` is the reference for
behaviour until P7 retires it. One **baseline** plus independently enable-able **parts**, each a
directory, each packed as one `<id>.manifold-plugin.json` and installed at
`engine.plugins.install` by hash (manifold `docs/PLUGINS.md` §10).

| Plugin                | Directory                | Halves       | What it is                                                                                                                                                                   |
| --------------------- | ------------------------ | ------------ | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `atyrode.babel`       | `atyrode.babel/`         | server + web | The baseline: the store (one SQLite file of its own), the nine read doors, the operator's acts, the three drain doors, the machine operations and the conductor. Contributes the five event kinds and no panel. |
| `atyrode.babel.feed`  | `atyrode.babel/feed/`    | web          | Home — every record Babel produced, ranked by what needs the operator — the peeled record, and a topic with its filings and his interest. Panels `home`, `record`, `topic`.     |
| `atyrode.babel.watch` | `atyrode.babel/watch/`   | web          | What is running, what will run and what a drain is spending: presets instead of flags, the model and the ceiling up front, the live pulse, the receipt afterwards. Panel `watch`. |

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

## The machine half: one bundled file, no pinned engine, three tools the machine provides

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

The operations are `scan`, `archive` and `prepare` — the catalog, and what is kept of it. Each
takes ONE input
document — a JSON string in the job request, which the engine materializes as a file at
`/inputs/input`, so `--input` is a path and never 64 KiB of argv — and writes every file it
produced flat into the sealed output lease at `/outputs/outputs`, which the hub reads back with
`ctx.jobs.outputs`. The lease is cut from a **managed** location: the alternative, an ordinary
anchor, must already exist on the machine for `write` and refuses a second job for `create`.
`scan` and `prepare` run with `network: "none"`; `archive` reaches the host because it reaches
the repository and its storage service.

**Three tools are runtime tools the machine's owner provides, not artifacts this manifest
pins**: `bun`, the executable every operation runs; `git`, which reads repository identity for
`scan` and `prepare`; and `restic`, which owns the archive's repository format.
A Manifold job sandbox is built from `/proc`,
`/dev`, the job's own tmpfs home and the artifacts it declares, and it carries no libc at all, so
a bare binary cannot `execvp` in one — which is exactly what happened on the first real job. The
owner's `execution.runtimeToolClosures` binds a tool WITH its exact closure at Nix build time,
per machine, under the alias the operation names. Each tool is resolved at `/runtime/bin/<alias>`
first and on PATH second, so the same code runs in a job and in a test. For the operator's fleet
that is one dotfiles module:

```nix
services.manifold.execution = {
  runtimeTools.bun = [{ source = "${pkgs.bun}/bin/bun"; target = "/runtime/bin/bun"; kind = "file"; }];
  runtimeToolClosures.bun = [ pkgs.bun ];
  runtimeTools.git = [{ source = "${pkgs.git}/bin/git"; target = "/runtime/bin/git"; kind = "file"; }];
  runtimeToolClosures.git = [ pkgs.git ];
  runtimeTools.restic = [{ source = "${pkgs.restic}/bin/restic"; target = "/runtime/bin/restic"; kind = "file"; }];
  runtimeToolClosures.restic = [ pkgs.restic ];
};
```

**This manifest pins NO tool.** #284 pinned `omp` here, by url and digest, because Babel drove
that exact build; the revert (atyrode/babel#279) took the engine with it, and a run that
reaches a model is now a Code session whose engine is pinned by whoever posts it.

### What runs a model, and why it is not this bundle

Until 2026-09-13 `explore` and `evaluate` launched `code engine`; #284 briefly replaced that
with a launcher of Babel's own — a pinned `omp` runtime tool, an `atyrode.babel.inference`
service, a `setupInference` door and a price table. **That is reverted** (atyrode/babel#279).

The operator's architecture is `atyrode.babel` → `atyrode.code` → `atyrode.omp`. Code owns the
profiles — the model, the thinking level, the account — and Code launches omp. When Babel's
button is pressed, the operator has already picked a saved Code profile or parametrized one in
Code's generator, and Babel posts the run through Code's `runSession` door. Babel never composes
a session and never launches omp, so this bundle pins no engine, binds no model service,
declares no `explore` or `evaluate` operation and installs no price table.

**A BABEL RUN IS A CODE SESSION, IN FIVE STEPS.** `doors/launch.ts` does them in this order and
the order is the point:

1. the **selection** — this machine's catalogued sessions, never a live log and never one of
   Babel's own runs' transcripts (#262);
2. the **recipes** — the methods this hub holds, read off the policy document's own `recipes`
   block, which is the same list Watch's Recipes section shows. A hub whose policy names none
   refuses the explore by name rather than posting one with no method to run;
3. the **material** — one `atyrode.babel.prepare` job, posted here, whose SECOND sealed output
   is the evidence the run reads. `prepare` was already digesting every selected session; the
   same single pass now writes the normalized record stream into that lease, so the material
   costs no second read of a 240 MB log;
4. the **prompt**, composed around `/inputs/material` — no tool block at all, because Babel
   runs no session and holds no tools in one. The answering protocol is a fenced ` ```json `
   block in the session's final message, with the stage's JSON Schema printed above it;
5. the **session** — `atyrode.code.runSession`, and a `runs` row that records Code's job id,
   the container that answered and the `prepare` job whose material it read.

**The material is what makes a claim checkable.** `/inputs/material` holds `index.json` — the
selection, with a `sourceDigest` per session — and `sessions/<file>`, one canonical JSON record
per line in the order the harness wrote them. A citation names the file the index names and
copies that digest unchanged; anything else is a recorded refusal (`unknown-reference`). The
index rides the `prepare` receipt as well as the lease, so the hub verifies a locator from one
row instead of pulling a sealed archive back to read the front of it. That is the answer to the
2026-09-13 post-mortem's F1: the breadth-of-evidence principle survives as an immutable
selection, not as a whole-corpus digest per run.

**A settled session is reconciled through Code, never through `ctx.jobs`.** `onJobSettled` is
delivered only to the plugin that STARTED the job and `ctx.jobs` verbs are bound to the calling
plugin's id, so Code's job — posted under `atyrode.omp`'s own operation — is never Babel's to
poll or be woken by. `server/conductor.ts` splits every run whose `container_id` is non-null
onto `code.readSession({containerId, jobId})`: a finished one has its final message read for the
answer, its citations checked against the material index, its receipt written with the model and
the usage (one call; the tokens and cost as omp counted them) and its claim settled — and a
REFUSED submission settles too, at the cost, because the model answered and the deployment paid
for it. A read Code refuses is recorded on the run as its note and retried once; twice in a row
closes the run and releases its claim, on the same bound the claim reaper uses.

**THE MATERIAL IS A BOUND JOB INPUT (ADR 0044, atyrode/manifold#592).** `materialInput()`
returns `inputs: [{ name: "material", from: { jobId: <the prepare job>, output: "material" } }]`
and `atyrode.babel.prepare` declares `exports: ["material"]` — the second is what lets ANOTHER
plugin's job bind the first, since a same-plugin binding needs no export and Code's job is
`atyrode.omp`'s. Admission refuses `input_not_exported:material` without it, and
`test/contract.test.ts` refuses an operation that exports a name it does not output.

**A BINDING NAMES A SETTLED JOB, which is why a run is started in two wakes.** The hub refuses
a binding whose source is still active, and `prepare` is running the instant the press posts
it. So the press seals the material and records the run's intent, and `postPrepared` — reached
from the cycle once the conductor has settled that preparation — composes the prompt from the
material's own index and posts the session. The prompt is the better half of that constraint:
built there, it carries the real file names, record counts and the digests a citation has to
copy, instead of the layout the press could only guess.

**WHAT IS STILL REFUSED: CODE'S PROMPT BOUND.** `SessionRunInputSchema` takes a prompt of
16,384 characters and Babel's composed explore prompt is about 33,000 — the answering
protocol, the per-role instructions and the stage's JSON Schema are most of it. `postPrepared`
measures against CODE'S OWN published number and closes the run `prompt_too_large` with both
figures, rather than letting Code's parse report it as a door "asked for something it does not
take". What moves is Code's bound or the analysis contract; it is not a thing a narrower
selection fixes.

`explore` and `evaluate` survive as NAMES (`OPERATIONS` in `contract.ts`): they are what a run
is called, the node a launch asks authority at, and the `kind` a run row and a receipt record.
They are not in `MACHINE_OPERATIONS`, which is what the machine half implements and what
`manifest.json` declares. A DRAWN review (`review-backlog`, `file-and-tidy`) is a third thing
again: the coordinator picks it, claims it under a fence and dispatches it with a blinded
projection of the record under review, and that dispatch went with Babel's own launcher in the
revert. Both the door and the conductor answer `draw_pending` for it, and it returns with #268.

**`archive` is declared, and `restic` is a closure like the others.** restic is half of why the
operation waited: upstream's whole Linux distribution is bare bzip2 —
`restic_0.19.1_linux_amd64.bz2` (10,107,515 bytes) and `restic_0.19.1_linux_arm64.bz2`
(9,044,264 bytes), with no tar or zip of either — while `MachineArtifactSchema` takes `raw`,
`zip` or `tar.gz` and nothing else. A tool the owner binds needs no artifact format at all,
which is the whole point of the mechanism above: the manifest names the alias `restic` and pins
nothing, and a machine whose module does not bind it simply cannot run the operation.

The other half was the repository and the secrets that open it, and neither belongs in the
manifest: `environment` is fixed reviewed values in committed code, which is not where a
password goes and not where one deployment's `s3:` locator belongs either. Both arrive through
ONE service the operator installs, `atyrode.babel.restic`:

- the operation declares `services: [{ serviceId: "atyrode.babel.restic", revision: "1",
  operationIds: ["storage"] }]` and a second input file whose literal is `{"url":"","bearer":""}`
  with `jsonValues` filling `url` and `bearer` from that binding;
- the engine opens a loopback proxy for the job, mints a capability for that job alone, writes
  both into `/inputs/restic`, and refuses to open it at all unless the operation is
  `network: "host"` (`service_proxy_requires_host_network`);
- `machine/restic.ts` reads that file, asks `GET /storage` once with the capability, and takes
  the storage document — `{repository, password, accessKeyId?, secretAccessKey?}` — from the
  answer. The object-store pair is required for an `s3:` locator and refused in halves
  (`SPEC.md` decision 50), so a half-installed policy fails as itself rather than as an
  unexplained restic exit. The password then reaches restic as `RESTIC_PASSWORD` in the CHILD's
  environment and nowhere else: never argv, never this process's environment, never a receipt.

The one value the manifest does fix is `BABEL_RESTIC_CACHE_DIR=/home/job/.cache/restic`, inside
the managed cache location the operation may write — without an index cache every backup
re-reads every byte it already archived, and a confined job has nowhere else to put one.

**The bearer is not the password.** `inputFiles[*].jsonValues[*].value` takes `url` or `bearer`,
and the bearer is 32 random bytes the owner mints per job for its own proxy
(`packages/agent/src/job-service-proxy.ts`: "Fresh job capability, never an upstream
credential"); the protocol can materialize a capability into a job's file and has no way to
materialize an owner-held secret *value* into one. So the capability is what the job is given
and the document behind it is what carries the secret — which is also why the policy's upstream
is the operator's own store rather than anything Babel ships or generates (decision 51: Babel
never creates or emits a credential, and stays vault-agnostic).

Installing it is one owner call per machine, `engine.services.configureConfiguration`
(`expectedRevision` is what `readConfiguration` last reported, `null` for a machine with no
configuration yet):

```json
{
  "machineId": "<machine>",
  "expectedRevision": null,
  "policies": [
    {
      "serviceId": "atyrode.babel.restic",
      "revision": "1",
      "origin": "https://<the operator's store>",
      "allowLoopbackHttp": false,
      "maxConcurrent": 2,
      "credential": { "ref": "babel-restic", "header": "Authorization", "prefix": "Bearer " },
      "operations": {
        "storage": {
          "kind": "http-proxy",
          "method": "GET",
          "path": "/storage",
          "request": { "kind": "none" },
          "response": {
            "kind": "stream",
            "disclosure": "full",
            "contentTypes": ["application/json"],
            "headers": []
          },
          "timeoutMs": 5000,
          "maxRequestBytes": 1024,
          "maxResponseBytes": 4096
        }
      }
    }
  ]
}
```

The `credential.ref` is the store's own token, held by the owner and attached to the upstream
request by it; the job never sees it, and it is a machine-side declaration like any other:

```nix
services.manifold.execution.serviceCredentials.babel-restic = {
  source = "/run/credentials/babel-restic-token";
  origins = [ "https://<the operator's store>" ];
};
```

A store that needs no token of its own drops both blocks. The same binding also works unchanged
against an **instance service** configured once for the fleet (`engine.services.configureInstance`
with `policy.runtime.scope = "instance"`), because a job names a serviceId and the operations it
may call, never where the answer comes from.

Two things then have to name the service by hand, both at install rather than at launch:
`engine.jobs.install` carries `resourceBindings.services["atyrode.babel.restic"]`, the
fingerprint `engine.jobs.describe` reports for the installed policy (a binding whose digest no
longer matches is `service_binding_mismatch`, which is the point: a policy the operator changed
is a new installation, not a silent upgrade); and the governed consent `archive` needs is
`services:invoke` at
`manifold://machine/<machine>/service/atyrode.babel.restic/operation/storage` beside the
`network:host` every host-network operation needs at
`manifold://machine/<machine>/operation/atyrode.babel.archive`. Neither is a manifest
capability: both are governed, so they are consented per node against the exact artifact
revision (`packages/protocol/src/capabilities.ts:92-103`).

`git` runs the same way and today still answers nothing, because a job sees only its declared
locations and the workspaces a session names are host paths outside them (#254 records the
decision this needs).

Two more things an enrolled machine's operator must arrange, because a manifest cannot: the
`home` anchor needs `~/.omp/agent/sessions`, `~/.codex` and `~/.claude` to **exist** (a job whose
read location is missing fails to start; `mkdir -p` is the whole fix), and the `runtime` anchor
must be a dedicated bounded tmpfs, since the named-output lease is cut from it.

## Draining a usage window

A **drain** is the one operation that spends a chosen account's remaining usage on purpose,
before it resets, and stops itself (#258; `docs/runbook.md` §11 is the procedure). It is three
doors of the baseline and one section of Watch:

| Door           | Governed at                             | What it does                                                                                                                                                      |
| -------------- | --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `drainStart`   | `machines:run` at the **operation** node | Validates the target, refuses a fan above the manifest's `concurrentJobs`, posts the first fan of jobs through the same launch path the operator's own button uses, and writes the `drains` row. |
| `drainStatus`  | `containers:read`, delegating `jobs:read` | What is draining: jobs live, jobs at the model, tokens and cost a minute over the last three minutes, spend against target, ETA against deadline, refusals by reason, the account. A cycle follows it, and the delegate is what lets that cycle read a running job back. |
| `drainStop`    | `jobs:cancel` at the **operation** node | Cancels every job the drain holds, and marks the row `closing` — or `stopped`, when it holds none.                                                                  |

Four things are worth knowing before reading `server/drain.ts`:

- **The controller is not a second launcher.** Every job it posts goes through
  `launchMachinery`'s `startExplore`/`startBeat` — the same code path, the same document, the
  same pinned installation and the same `runs` row as the `launch` door — so there is never a
  second answer to what a run is. It fans out only the presets that are launched DIRECTLY
  (`read-whats-new`, `explore-topic`, `keep-going`); a drawn preset goes through the coordinator,
  and fanning it out would be a second implementation of the thing the coordinator arbitrates.
- **It is not a governor at all, and it sets no overlay.** The standing `policies` row and the
  `budgets` table are both untouched. A drain's jobs are launched directly and take no claim, so
  no admission bound in `coordinator.ts` ever counts one: an overlay raising
  `concurrentPerMachine` bounded nothing of the drain's and raised the CONDUCTOR's review bound
  on every online machine for the drain's TTL. The fan is bounded where it is real — against the
  manifest's `limits.concurrentJobs` at the door, and by the drain's own live jobs in the
  controller — and what ONE run may spend stays the standing policy's `perRunUsd` (#268). A
  heavier run is a profile change; a longer drain is a deadline, and a drain that names none gets
  one two hours out.
- **It has no clock.** A tick happens when something has already woken this half, and the wake
  that matters is a settlement, because a settlement is exactly when a slot opens. Launch ids are
  DERIVED from the drain and its launch ordinal (`job_<drainId>_<n>`), so a retried tick re-posts
  the same job rather than a second one — and a job the hub already holds under that id
  (`job_digest_conflict`, the write that did not land) is taken back onto the row rather than
  re-posted for ever.
- **What it cannot cancel, it keeps.** Closing on a target stops launching and ASKS the hub to
  cancel what is in flight; a tick woken by a settlement holds no `jobs:cancel`, so the jobs keep
  running and the row goes to `closing` holding them, folding each receipt as it lands and taking
  its recorded `ending` when none is left. Their spend is the drain's spend: a row that emptied
  `live` at the close under-reported its own total by up to (N−1) runs. `drainStop` is where
  cancellation really lands, and a `closing` drain is exactly the one whose stragglers it can
  still reach.

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

`MANIFOLD_REV` follows Manifold `main` and currently names `0bc76660`, which carries the plugin
database (ADR 0034) with its failure-atomic lifecycle (atyrode/manifold#536) — the primitive
Babel cannot start without — per-operation `concurrentJobs` admission (#551), the `job_progress`
event (#552), metered brokered inference (ADR 0038, #554) and the `pi-native-usage` meter kind
(#572) that omp's own gateway wire needs. The checkout on dev-01 is named
`manifold-db` because the plugin database was a branch before it was `main`; it is now simply
that clone of atyrode/manifold, detached at the pin, and `feat/brokered-inference`, the other
branch it once carried, is merged into `main` and superseded by it. Nothing here pins a branch.
`tsconfig.json` still lists **two candidates** for every `@manifold/*` alias, `../../manifold-db`
before `../../manifold`, and tsc and Bun take the first that exists; `pack.sh` resolves
`../../manifold` and honours `MANIFOLD_DIR` for a tree that keeps the checkout elsewhere (an
isolated worktree, a second branch). The pin and the workflow's `uses:` ref are one revision and
are bumped together; moving both to a newer `main` revision, with this checkout moved with them
and the gate green against it, is ordinary work in its own PR (`AGENTS.md`).

`bun install` here fetches only what typechecking and tests need: `zod` (pinned to the kit's own
version, and the one thing inlined into every bundle), `typescript`, React with its types, and
`happy-dom` — the document a panel test mounts a React panel into, test-only and in no bundle.
React, `@manifold/plugin` and `@manifold/ui` are **shared externals**, rewritten by `pack` into
reads from the shell's own module registry, so a bundle never carries a second copy of them.

## Code is a second pin, and verification composes three families

`atyrode.babel` declares `atyrode.code` a **required** dependency, because that is the
architecture and not a convenience: a hub that enabled Babel without Code would offer a Start
section whose every press the host itself refuses, and assembly refusing the install is the
earlier and better answer. A required dependency is a dependency ASSEMBLY CHECKS, so
`bun run verify` — which installs every bundle on a disposable engine — cannot compose Babel
until Code, and `atyrode.omp` beneath it, are on disk.

`CODE_REV` is that pin: one commit of atyrode/code, and **the same commit**
`package.json`'s `@atyrode/manifold-code` names, because verifying against one revision while
compiling the types against another proves nothing about either. `test/contract.test.ts`
refuses a tree where the two disagree.

```sh
bun run deps:code   # fetch atyrode/code @ CODE_REV and build its bundles, and omp's
bun run pack
bun run verify      # installs atyrode.omp*, then atyrode.code*, then atyrode.babel*
```

`scripts/prepare-code.ts` **packs nothing of its own**. It fetches the Code revision into
`.integration/<rev>/code`, links this tree's Manifold checkout beside it as
`.integration/<rev>/manifold` — the sibling layout Code's own scripts resolve — and then runs
CODE's `prepare:integration` (which does the same for omp) and CODE's `pack`. A packer here
would be a second answer to what a Code bundle is, and the day Code changed its own it would
be the copy nobody updated. The script refuses a Code whose `plugins/MANIFOLD_REV` is not this
tree's: three families verified against two different kits would prove nothing about the hub
they install on.

`.integration/` is gitignored — it is another repository's source and another family's
bundles — and `pack.sh` prunes it, so this family's `dist/` holds this family's bundles only.
In CI the same thing happens through the reusable workflow's `prepare-command` hook
(`manifold-plugins.yml`), which needs no second `actions/checkout`: the fetch is the script's
own, and the manifold sibling the workflow already lays out is the one `manifold-dir.sh`
resolves.

## Build, test, pack, verify, develop

```sh
bun install                 # zod, typescript, react + types, happy-dom; nothing else
bun run deps:code           # atyrode/code @ CODE_REV, and omp beneath it, as bundles to compose against
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

Release delivery is `release.yml`: a `v*` tag runs the same reusable gate against the tagged
revision, attaches `dist/*.manifold-plugin.json` and `manifold-plugins.SHA256SUMS` to the GitHub
Release beside Babel's binary, and hands each asset URL and sha to the integrated preview's
receiver (`plugin <url> <sha256>` over the forced-command key, the same verb a developer runs
from dev-01), baseline before its parts, so the preview installs the release by itself. Between
releases a preview gets a build through `bun run dev --deliver`, and the sha256 that counts is
the one CI prints. A tag is permanent: a bad one stays and the next patch follows it.

The sha256 is over an artifact's exact bytes, and Bun writes every bundled module's path as a
comment, so a hash reproduces only from the layout above with the same Bun. The pins are what
`engine.plugins.install` demands; `dist/SHA256SUMS` is what carries them.
