# Building, packing, verifying and delivering the family

Babel **is** this repository, and the repository is nothing else. The standalone product — a Go
binary, its embedded React surface and the multi-machine backend behind them — is retired: the
hub owns Babel's state, its pages are panels in the shell, and its runs are jobs on enrolled
machines. There is no `plugins/` wrapper, because there is nothing left for it to separate the
plugins from. One **baseline** plus
independently enable-able **parts**, each a directory, each packed as one
`<id>.manifold-plugin.json` and installed at `engine.plugins.install` by hash (manifold
`docs/PLUGINS.md` §10). Where a port's provenance matters the reference implementation is
readable at the tag `v0.4.0` — `git show v0.4.0:internal/<pkg>` — and `docs/parity.md` records,
per capability, what it did and whether this family does it.

| Plugin                | Directory      | Halves       | What it is                                                                                                                                                                                                        |
| --------------------- | -------------- | ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `atyrode.babel`       | `babel/`       | server + web | The baseline: the store (one SQLite file of its own), the nine read doors, the operator's acts, the three drain doors, the machine operations and the conductor. Contributes the five event kinds and no panel.   |
| `atyrode.babel.feed`  | `babel/feed/`  | web          | Home — every record Babel produced, ranked by what needs the operator — the peeled record, and a topic with its filings and his interest. Panels `home`, `record`, `topic`.                                       |
| `atyrode.babel.watch` | `babel/watch/` | web          | What is running, what will run and what a drain is spending: presets instead of flags, the model and the ceiling up front, the live pulse, the receipt afterwards. Panel `watch`.                                 |
| `atyrode.babel.jev`   | `babel/jev/`   | server       | Optional typed judgement. Three read-only doors size and run a bounded corpus sweep or judge candidate pairs of named anchors. They return readings and suggestions for the caller to deliver; they cannot write. |

A part is its own directory — inside its parent's, or beside it — and says so with
`dependencies: { "atyrode.babel": { type: "required" } }`; assembly refuses it otherwise. The
edge from the baseline is one-way: its manifest names no part. Feed additionally declares Jev
optional, so it can read available judgements without making installation or ordinary reading
depend on them. `ctx.actions.call` refuses an undeclared callee. The baseline is not a library — a part reaches it only through its doors
(`host.client.action` from a panel, `ctx.actions.call` from a server half), and the linter holds
both directions to that: the one module of the baseline a part may import is
`babel/contract.ts`, where every id, door name, event kind, panel id, preset, job output
file and receipt field is spelled once with the tables in `babel/store/schema.ts`, and
nothing of the baseline's may import a part at all — an import would inline the part into the
baseline's own bundle and survive the part being removed, which is how optional stops being
optional. `test/contract.test.ts` pins every manifest to those two files, and
`test/optional-part.test.ts` dispatches every read door against a hub that refuses the part, so
the fallback is run rather than described.

Recall's six outside-agent read doors declare exact bounded projections in that same contract.
`babel/recall-skill.md` is imported verbatim into the versioned `recallSkill` result;
`babel/recall-profile.json` contains the corresponding source-reviewed SDK approvals, never
approvals copied from a live hub. After deliberately changing a projection, run
`bun run recall:profile` and review its digest change; `bun run check:recall-profile` is part of
`bun run check` and refuses drift. Dotfiles packages that body, profile and the matching supported
SDK runner as immutable source artifacts. It does not select an origin, retrieve credentials,
create grants or install a service. Owner setup is `docs/runbook.md` §9.1.

`bun test` requires `restic` 0.19.1 on `PATH` for disposable synthetic archive fixtures; a missing
binary fails Recall's tests rather than skipping them. Both gates run `bun run deps:tests`,
which checks the upstream release digest and adds that binary to the disposable CI runner's
path, before building the dependency closure. Older CLI output formats are not covered by this
gate. These fixtures provision no live storage service and read no real conversations.

The web halves are **in-realm React** (`docs/PLUGINS.md` §10): a part's `web.tsx` —
`babel/feed/web.tsx` and `babel/watch/web.tsx` — default-exports `{ id, panels }`
of ordinary components on `@manifold/ui`'s layout primitives, with the skin in a `styles.css`
whose every selector is rooted at the plugin's own class. The baseline's own
`babel/web.ts` registers an id and no panel: it paints nothing, and the entry exists so
that a baseline surface, if one is ever wanted, belongs there rather than in a part. The server
half is authored against the kit (`@manifold/plugin-kit/server`: `defineServerAction`,
`GuestCtx`), which the in-realm loader takes as it stands — one authoring shape for a row that
may later be hardened.

## The store is rows, not keys

The baseline's data is a graph — 65,818 records, 60,793 links, filings, per-role tallies, a
ledger of entities and facts — read by filter, sort, join and aggregate on every page, so it
lives in the plugin database (manifold ADR 0034, `docs/PLUGINS.md` §4 "Your tables"): one SQLite
file at `<data>/babel/data.db`, asked for by `database: { maxBytes }` in the
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

## The machine half: one bundled file, no pinned engine, two tools the machine provides

A run is a **job on an enrolled machine** (manifold `docs/PLUGINS.md` §8), and the baseline's
manifest carries the `machine` block that says what may run there. `babel/machine/` is
the half that runs: one dispatcher, `main.ts`, behind one command line —

```
bun /job/artifact <operation> --input /inputs/input --out /outputs/outputs
```

— which is literally the `argv` every operation declares. `pack.sh` builds that half with
`bun build --target bun` into `babel/machine.js`, and `scripts/stamp-machine.ts` stamps
its sha256 into **both** platform artifacts of the manifest (a `raw` artifact is its own entry,
so `sha256` and `entrySha256` are one digest) along with the `machine.tools` pins below, then
packs
it as a `bundleFile` member of the baseline's bundle. The file itself is never committed —
`.gitignore` has it, a pack deletes it afterwards, `bun run dev` keeps it (`./pack.sh --machine`)
because the inner loop re-packs on every save. The committed manifest carries the **last stamp**,
so a change to the machine half shows up as a moved hash in the diff; `test/bundle.test.ts` runs
the packed member through the argv the manifest declares and checks the receipt it leaves.

The one-shot operations are `catalog`, `archive`, `prepare` and `verify`; Recall adds a persistent
native instance service. Each takes ONE input document — a JSON string materialized at
`/inputs/input`, so `--input` is a path and never 64 KiB of argv. One-shot jobs write their
results into sealed output leases read through `ctx.jobs.outputs`; Recall publishes no output
lease and returns bounded authenticated loopback responses through its native service.
Its generated service bearer is a separate readonly input containing a JSON string, not raw text:
atyrode/manifold at `3e8510c473d84175568ac81012763635112ed7d3`,
`packages/agent/src/job-inputs.ts:34-50`. The service parses it once, acknowledges readiness over
the owner's IPC and shuts down on owner disconnect. It uses private tmpfs for widening bytes
and the managed cache only for rebuildable local indexes and metadata.
Every Babel operation reads or writes the fleet archive, so each runs with `network: "host"` to
reach its bound repository and storage service: `catalog`, `prepare`, `verify` and Recall only
read it, every read verb with `--no-lock`, and `archive` is the one that writes. `scan`, which
catalogued a machine's local session files with no network, is retired (#453); the store keeps
its runs, and `RETIRED_OPERATIONS` in `contract.ts` is what still names them. The owner
configures Recall's instance service and exact disclosure-class grant targets, not an outside
caller's native job.

**A MACHINE ANSWERS FOR TOOLS BY NAME, AND THE FLEET ADVERTISES TWO** (`development` and
`system`, plus anchors). Until #303 this half asked for `bun`, `git` and `restic` by name, so
`engine.jobs.reviewDeployment` answered `resource_evidence_unknown` and Babel had **no native
installation at all** — the conductor still drew and dispatched reviews through Code, but no
beat, no `prepare` and no catalogue ever ran on a machine. A Manifold job sandbox is built from
`/proc`, `/dev`, the job's own tmpfs home and the artifacts it declares, and it carries no libc
at all, so a bare binary cannot `execvp` in one — which is exactly what happened on the first
real job. That is the constraint; asking for aliases nobody binds is not the way to meet it.
There is one decision per tool, and the four answers are different:

- **`bun` is pinned by this bundle**, the one tool it pins: it is the interpreter the machine
  half is written for — Babel's choice, moved by Babel — so it ships as an artifact-managed
  `machine.tools` entry, url and digests, exactly the way `atyrode.omp` ships its own runtime.
  `jobResourceRequirements` drops an alias the installation's own immutable declaration pins, so
  the owner is never asked for it. Those figures are MEASURED:
  `bun scripts/measure-runtime-tools.ts` downloads each release asset, hashes the bytes it
  received, inflates the named entry and hashes that, reads the member count and expanded bytes
  out of the archive's own central directory, and writes `runtime-tools.json`. A pack reads that
  file and downloads nothing, so packing stays offline and deterministic — and a packer whose
  own Bun is not the pinned one is refused, because `bun test` proves the half by running it
  under the LOCAL bun while the manifest declares the machine runs the pinned one.
- **`development` is the owner's toolset, and no Babel operation names it any more.** `git` lives
  inside its closure, and `scan` named the toolset so the machine half could fingerprint a
  scanned workspace with git. With `scan` retired, its git observer (`machine/repository.ts`)
  is kept by the operator's decision but imported by nothing except its own test; the repository
  question is the hub's, asked of the machine a session's archive label maps to, and nothing on
  a job machine runs git.
- **`system` is the owner's reviewed, digest-promoted native closure.** A pinned bun is
  dynamically linked — `runtime-tools.json` records the measured interpreter
  (`/lib64/ld-linux-x86-64.so.2`, `/lib/ld-linux-aarch64.so.1`) and DT_NEEDED list
  (`libc.so.6`, `libdl.so.2`, `libm.so.6`, `libpthread.so.0`) — so every operation that runs it
  names this closure too. Those are direct requirements, not a transitive closure: the owner
  supplies and reviews that.
- **`restic` stays the owner's, by name, and every operation asks for it.** There is nothing
  honest to pin. Requirements are per-operation, and since #453 every Babel operation reads or
  writes the archive, so a machine whose module does not bind restic runs none of them.

For the operator's fleet the remaining bindings and the runtime scratch's size are one dotfiles
module — no `bun` entry, and no `development` entry for Babel's sake since `scan` retired:

```nix
services.manifold.execution = {
  runtimeTools.restic = [{ source = "${pkgs.restic}/bin/restic"; target = "/runtime/bin/restic"; kind = "file"; }];
  runtimeToolClosures.restic = [ pkgs.restic ];
  outputBytes = 805306368; # RUNTIME_SCRATCH_BYTES, 768 MiB; the module's default is 1 MiB
  outputInodes = 10000;    # the runtime's per-job ceiling
};
```

#284 pinned `omp` here too, by url and digest, because Babel drove that exact build; the revert
(atyrode/babel#279) took the engine with it, and a run that reaches a model is now a Code session
whose engine is pinned by whoever posts it. `bun` is a different kind of pin: nothing else in the
architecture can own the interpreter of Babel's own machine half.

**THE RUNTIME SCRATCH HAS ONE SIZE, AND EVERY OPERATION THAT WRITES IT DECLARES MORE.** On a native
worker the `runtime` anchor is the named-output tmpfs `execution.outputBytes` sizes: 1 MiB by
default, at most 1 GiB, in whole 4 KiB pages (atyrode/manifold at `7b5fe301`,
`infra/native/module.nix:263-264,276-277`). Every job that cuts a lease from it shares it:
`catalog`, `archive`, `prepare` and `verify` write `atyrode.babel.outputs` there, and
`atyrode.omp.session` writes its own lease and extracts a bound material into it. The runtime
refuses a job whose `limits.outputBytes` is below that capacity, `bounded-output-storage-required`,
and charges the whole capacity before stdout and stderr
(`packages/agent/src/job-linux.ts:440-453,804`). So the rule is that every operation writing the
scratch declares `outputBytes` strictly above it, and the difference is that job's stdio. Babel's
four declare 1 GiB, Manifold's per-job ceiling (`packages/protocol/src/jobs.ts:86`) and the omp
session's own declaration; the scratch is `RUNTIME_SCRATCH_BYTES` (`contract.ts`), 768 MiB with
`outputInodes` 10000, which leaves each of them 256 MiB of stdio, and `test/contract.test.ts` holds
every runtime-writing operation above it. Until this, `scan`, `archive` and `verify` declared 64 MiB
and `prepare` 512 MiB, so a scratch big enough for a material above 64 MiB refused the other three.
The order is the bundle first, then the scratch: before raising it, confirm every operation
installed on that machine that writes it declares more.

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

1. the **selection** — archived captures the catalog filed, under any machine's label and never
   a machine's local files, never one of Babel's own runs' transcripts (#262). Only a row that
   names a capture is selectable; the input is grouped by snapshot and stops adding captures
   before it would pass `PREPARE_INPUT_MAX_BYTES`, counting the rest as over the bound;
2. the **recipes** — the methods this hub holds, read off the policy document's own `recipes`
   block, which is the same list Watch's Recipes section shows. A hub whose policy names none
   refuses the explore by name rather than posting one with no method to run;
3. the **material** — one `atyrode.babel.prepare` job, posted here on the launch's machine, whose
   SECOND sealed output is the evidence the run reads. It streams each selected capture out of
   the archive with `restic dump` into the single pass that digests, scans and normalizes it, so
   the raw bytes touch no disk and the material costs no second read of a 240 MB log. A capture
   the machine has already read is replayed from its managed cache without contacting the
   archive;
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

**THE PROMPT IS BOUNDED IN BYTES, AND FITS.** `runSession` takes a prompt of
`PROMPT_MAX_BYTES` — omp's own constant, re-exported by Code, and the hub's real ceiling,
since a prompt is carried in the 64 KiB job-input map, which counts ENCODED bytes.
`postPrepared` measures against it with a `TextEncoder` rather than a character count, so a
legal-length prompt whose selectors and digests are multi-byte cannot be refused at admission
instead; over it, the run closes `prompt_too_large` with both figures rather than a Zod issue
from Code's parse. Babel's composed explore prompt is about 33,700 bytes and fits with room
to spare; the guard stays because a longer contract, a bigger selection or a corpus of
non-ASCII selectors is how it would stop fitting.

**WHAT STOPS A RUN IS READ OFF THE ROW, and there are three answers.** A drain's own
bookkeeping cannot say it: `LiveJob.jobId` is the run's DERIVED identity, and for the lane
that spends no job is ever posted under it — the preparation is `${jobId}_material` and the
session is Code's own id. So `endDrain` and the `stop` door both read `runs`: no container is
a job of Babel's, cancelled with `ctx.jobs.cancel`; a container and a `job_id` is a Code
session, cancelled with `code.cancelSession`; a container and NO `job_id` is a run still
preparing, whose `atyrode.babel.prepare` job is cancelled — and whose ROW IS CLOSED, because
`postPrepared` posts a session for every open row whose material sealed and a cancel that
races the seal loses. A stop that left the row open would be the operator pressing stop and
the account spending afterwards.

**AND THE SELECTION'S BOUND IS UNDER THE MACHINE'S, WITH ROOM.** The machine has two bounds.
The leases are written into the runtime scratch. And `outputBytes` is the AGGREGATE the owner
seals against — stdout, stderr and both of `prepare`'s leases come out of one running budget,
and each lease is a ustar archive carrying 512 bytes of header and padding per member.
`MAX_MATERIAL_BYTES` is 448 MiB, 64 MiB under the 512 MiB `inputBytes` the omp session extracts
the material into, and it is the ceiling rather than the bound: the scratch is shared by every
job on the machine, so the hub bounds a material at
`min(MAX_MATERIAL_BYTES, ⌊(C − MATERIAL_HEADROOM_BYTES) / k⌋)`. `C` is the scratch capacity the
machine last measured, the `outputCapacity` on its newest `catalog` or `prepare` receipt (the
ceiling alone until one reports); `MATERIAL_HEADROOM_BYTES` is 64 MiB; `k` is how many materials
the lane may hold at once — 1 for an operator's launch, `concurrentPerMachine` for the
conductor's lanes, and the drain's own `concurrent` for a drain fan. On dev-01's 768 MiB scratch
a two-wide lane bounds each material at 352 MiB. `prepare` measures its own lease as well and
refuses `material_storage_insufficient`, naming both figures, before it fetches anything: a
selection admitted at exactly a bound would fill it and fail after the full read, which is the
failure the pre-post check exists to move.

`explore` and `evaluate` survive as NAMES (`OPERATIONS` in `contract.ts`): they are what a run
is called, the node a launch asks authority at, and the `kind` a run row and a receipt record.
They are not in `MACHINE_OPERATIONS`, which is what the machine half implements and what
`manifest.json` declares. A DRAWN review (`review-backlog`, `file-and-tidy`) is a third thing
again, and the conductor owns it: `dispatchReviews` (`server/conductor.ts`) draws an assignment
from the coordinator, claims it under a fence, refuses a projection that leaks withheld review
state, composes the review prompt around that blinded projection and posts it as a Code session,
then binds the claim to Code's job id and writes the `runs` row — every cycle the policy enables
and the loop is not parked. The `launch` door is the one place a draw is refused, with
`draw_managed` (`doors/launch.ts`): an operator-picked record must not bypass the shared claim,
cadence and budget the policy governs that lane by.

**`archive` is declared, and `restic` is a closure like the others.** restic is half of why the
operation waited: upstream's whole Linux distribution is bare bzip2 —
`restic_0.19.1_linux_amd64.bz2` (10,107,515 bytes) and `restic_0.19.1_linux_arm64.bz2`
(9,044,264 bytes), with no tar or zip of either — while `MachineArtifactSchema` takes `raw`,
`zip` or `tar.gz` and nothing else. A tool the owner binds needs no artifact format at all,
which is the whole point of the mechanism above: the manifest names the alias `restic` and pins
nothing, and a machine whose module does not bind it simply cannot run the operation. Since #453
that is every Babel operation: `jobResourceRequirements` filters each operation's own
`runtimeTools`, and `catalog`, `archive`, `prepare` and `verify` all name `restic` and bind
`atyrode.babel.restic`, so `tools/restic` and `services/atyrode.babel.restic` are the two
resources an operator provisions before any of them is admitted on a machine.

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
  answer. The object-store pair is required for an `s3:` locator and refused in halves — half a
  credential is a misconfiguration and never a default (`SPEC.md`) — so a half-installed policy
  fails as itself rather than as an unexplained restic exit. The password then reaches restic as
  `RESTIC_PASSWORD` in the CHILD's
  environment and nowhere else: never argv, never this process's environment, never a receipt.

The values the manifest does fix are cache directories inside the managed cache location the
operations may write, all of them rebuildable: `BABEL_RESTIC_CACHE_DIR=/home/job/.cache/restic`
— without an index cache every backup re-reads every byte it already archived and every catalog
re-downloads every tree, and a confined job has nowhere else to put one —
`BABEL_CATALOG_CACHE_DIR=/home/job/.cache/catalog`, the catalog's memory of the snapshots it has
already listed, and `BABEL_PREPARE_CACHE_DIR=/home/job/.cache/prepare`, where a preparation keeps
its redacted reading of each capture so a second one over the same captures spawns no restic
child at all.

**Reading takes no lock, so analysis needs no write access.** `machine/restic.ts` builds every
read verb — `cat`, `snapshots`, `ls`, `dump`, `restore`, `check` — with restic's global
`--no-lock`; `backup` alone takes the ordinary lock, and `init` is called only by disposable test
fixtures. A killed catalog or preparation therefore strands no lock in the production
repository, and `catalog`, `prepare`, `verify` and Recall work with a read-only object-store key
where the storage document serves one; only a machine that runs `archive` needs write access.
That is also why `prepare` and `catalog` run with `network: "host"`: the protocol offers `none`
or `host` and nothing between, and restic must reach the object store
(`docs/sandbox-threat-model.md` §3 records what that costs).

**The bearer is not the password.** `inputFiles[*].jsonValues[*].value` takes `url` or `bearer`,
and the bearer is 32 random bytes the owner mints per job for its own proxy
(`packages/agent/src/job-service-proxy.ts`: "Fresh job capability, never an upstream
credential"); the protocol can materialize a capability into a job's file and has no way to
materialize an owner-held secret _value_ into one. So the capability is what the job is given
and the document behind it is what carries the secret — which is also why the policy's upstream
is the operator's own store rather than anything Babel ships or generates: Babel never creates
or emits a credential, and stays vault-agnostic (`SPEC.md`).

Installing it is Watch's **Services** section, which composes exactly the document below out of
the `services` block above — the service id, its revision and its operations come from the
manifest, so the binding and the policy cannot be edited apart — previews it with a digest and
applies it as a compare-and-swap on the configuration revision it was read at
(`babel/doors/services.ts`). The same document by hand is one owner call per machine,
`engine.services.configureConfiguration`, and `docs/runbook.md` §4 says when that is still the
right path (`expectedRevision` is what `readConfiguration` last reported, `null` for a machine
with no configuration yet):

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
          "timeoutMs": 10000,
          "maxRequestBytes": 1024,
          "maxResponseBytes": 65536
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
is a new installation, not a silent upgrade); and the governed consents every archive operation
needs are `services:invoke` at
`manifold://machine/<machine>/service/atyrode.babel.restic/operation/storage` beside the
`network:host` every host-network operation needs at
`manifold://machine/<machine>/operation/<operation>`, once each for `atyrode.babel.catalog`,
`atyrode.babel.prepare` and `atyrode.babel.verify`, and for `atyrode.babel.archive` on a machine
that collects. Neither is a manifest capability: both are governed, so they are consented per
node against the exact artifact revision (`packages/protocol/src/capabilities.ts:92-103`).

`git` no longer runs in any job. It was `scan`'s, to fingerprint a workspace a job could not see
anyway (#254); repository identity is the hub's own question, asked of the machine a session's
archive label maps to.

**WHAT AN ENROLLED MACHINE NEEDS BEFORE BABEL RUNS ON IT**, because a manifest cannot arrange
any of it:

1. **the `restic` runtime tool** bound by the machine's module, beside `system` (the Nix block
   above);
2. **the `atyrode.babel.restic` service** installed for the existing repository and named by
   fingerprint in the job install's `resourceBindings`, as above — the storage document is the
   operator's and nothing here mints one;
3. **the consents** above: `services:invoke` on the storage operation and `network:host` for
   `catalog`, `prepare` and `verify`, which must reach the object store;
4. **the runtime scratch** as a dedicated bounded tmpfs, since every named-output lease is cut
   from it: `execution.outputBytes` at `RUNTIME_SCRATCH_BYTES`, 768 MiB, with `outputInodes`
   10000, raised only after the bundle whose operations declare 1 GiB is installed.

Nothing else is local. `catalog` and `prepare` declare no `home` location and open no file of the
machine they run on, so any machine with these four can catalogue and prepare any archived
session. The `home` anchor matters to `archive` alone, which still reads
`~/.omp/agent/sessions`, `~/.codex` and `~/.claude` beneath it, and a job whose read location is
missing fails to start. On a native Manifold worker (the NixOS module) that anchor is the service
account's workload home, `/var/lib/manifold-workload/home`, hard-coded by the module
(atyrode/manifold at `7b5fe301`, `infra/native/module.nix:10,23-31`), and an operator may protect
`/home` from every workload with `execution.protectedDirectories`, so `archive` there backs up an
empty tree. It collects for real once it reads the session roots through a read-only anchor the
operator declares to Manifold (atyrode/manifold#839); until then the dotfiles collector backs
each machine up under the same contract (`SPEC.md` §6.1).

## Draining a usage window

A **drain** is the one operation that spends a chosen account's remaining usage on purpose,
before it resets, and stops itself (#258; `docs/runbook.md` §11 is the procedure). It is three
doors of the baseline and one section of Watch:

| Door          | Authority                                                                     | What it does                                                                                                                                                                                                                                                                     |
| ------------- | ----------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `drainStart`  | `containers:read`, delegating `machines:read` and `machines:run`              | Validates the target, refuses a fan above the manifest's `concurrentJobs`, posts the first fan of jobs through the same launch path the operator's own button uses, and writes the `drains` row.                                                                                 |
| `drainStatus` | `containers:read`, delegating `jobs:read`, `machines:read` and `machines:run` | What is draining: jobs live, jobs at the model, tokens and cost a minute over the last three minutes, spend against target, ETA against deadline, refusals by reason, the account. A cycle follows it: the delegates let it read a running job back and relaunch a settled slot. |
| `drainStop`   | `containers:write`, delegating `jobs:cancel`                                  | Cancels every job the drain holds, and marks the row `closing` — or `stopped`, when it holds none.                                                                                                                                                                               |

Four things are worth knowing before reading `server/drain.ts`:

- **The controller is not a second launcher.** Every job it posts goes through
  `launchMachinery`'s `startExplore`/`startBeat` — the same code path, the same document, the
  same pinned installation and the same `runs` row as the `launch` door — so there is never a
  second answer to what a run is. It fans out only the presets that are launched DIRECTLY
  (`read-whats-new`, `explore-topic`, `keep-going`); a drawn preset goes through the coordinator,
  and fanning it out would be a second implementation of the thing the coordinator arbitrates.
- **It is not a governor at all, and it sets no overlay.** The standing `policies` row and the
  `budgets` table are both untouched. A drain's jobs are launched directly and take no claim, so
  no admission bound in `store/coordinator.ts` ever counts one: an overlay raising
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
  babel/               this repository
  manifold/            atyrode/manifold @ $(cat MANIFOLD_REV), with `bun install` run
```

```sh
git clone https://github.com/atyrode/manifold ../manifold
git -C ../manifold checkout "$(cat MANIFOLD_REV)"
bun install --cwd ../manifold --frozen-lockfile   # the kit resolves zod and the protocol from its workspace
```

`MANIFOLD_REV` follows Manifold `main` and currently names
`2ee760dda83ad023150c5d206e8cfcbdf9d99e4e`. At that revision the kit stamps
`hardenedContract: 5` into repacked bundles (`packages/plugin-kit/src/pack.ts:298-304`),
while the host retains contracts 1–5 (`packages/protocol/src/isolate.ts:744-758`).
The reviewed bounded-result channel includes exact digest-reviewed `textFields`: string-or-null
leaves preserve already-redacted evidence rather than refusing a redaction marker as a credential
carrier. Ordinary agent-facing results remain mechanical-only without trusted source approval,
and domain-owned classification/redaction remains mandatory
(`packages/sdk/README.md:138-178`; atyrode/manifold#798 and atyrode/manifold#812).
A source pin does not prove which revision any deployed hub is running.

It retains delegated `machines:read` (atyrode/manifold#740) — which four of Babel's doors
declare — and the pre-deployment projection for a plugin holding no installation
(atyrode/manifold#744), which is the state `atyrode.omp.describeDestination` exists to report.
A non-owner caller of a door that observes a machine must hold `machines:read` under it
(atyrode/manifold#749). It also carries the plugin
database (ADR 0034) with its failure-atomic lifecycle (atyrode/manifold#536) — the primitive
Babel cannot start without — per-operation `concurrentJobs` admission (#551), the `job_progress`
event (#552), metered brokered inference (ADR 0038, #554) and the `pi-native-usage` meter kind
(#572) that omp's own gateway wire needs. Some existing SDK checkouts are named
`manifold-db`, from when the plugin database was a branch; the directory name does not select
the revision. Keep the SDK used for this build at the exact pin, using an isolated sibling or
`MANIFOLD_DIR` rather than moving an unrelated checkout. Nothing here pins a branch.
`tsconfig.json` still lists **two candidates** for every `@manifold/*` alias, `../manifold-db`
before `../manifold`, and tsc and Bun take the first that exists; `pack.sh` resolves
`../manifold` and honours `MANIFOLD_DIR` for a tree that keeps the checkout elsewhere (an
isolated worktree, a second branch). The pin and BOTH workflow `uses:` refs — the gate's in
`manifold-plugins.yml` and the release gate's in `release.yml` — are one revision and are bumped
together; a release that ran the gate at another kit than the pin would verify bundles nobody
builds. Moving all three to a newer `main` revision, with this checkout moved with them and the
gate green against it, is ordinary work in its own PR (`AGENTS.md`).

`bun install` here fetches only what typechecking and tests need: `zod` (pinned to the kit's own
version, and the one thing inlined into every bundle), `typescript`, React with its types, and
`happy-dom` — the document a panel test mounts a React panel into, test-only and in no bundle.
React, `@manifold/plugin` and `@manifold/ui` are **shared externals**, rewritten by `pack` into
reads from the shell's own module registry, so a bundle never carries a second copy of them.

**Install and test with the same `MANIFOLD_DIR`, or reinstall after changing it.** `bun install`
writes `node_modules/react` as a SYMLINK into whichever SDK checkout `manifold-dir.sh` resolved
at install time, so pointing `MANIFOLD_DIR` somewhere else afterwards leaves this tree resolving
React through two different checkouts at once. Every web test then fails with "Invalid hook
call … more than one copy of React in the same app" — 79 of them in one measured case — which
reads exactly like a broken component change and is not one. The same applies to a checkout that
moves: if `MANIFOLD_REV` is bumped and the sibling checkout follows it, reinstall. Note that
`deps:code` and `verify` additionally REQUIRE the SDK checkout to sit at `MANIFOLD_REV` —
Code's own `prepare:integration` refuses "Code's SDK checkout does not match MANIFOLD_REV" — so
a checkout parked at another revision (dev-01's is detached at the preview's) needs
`MANIFOLD_DIR` pointed at one at the pin, exported for the install as well as the run.

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
be the copy nobody updated. The script refuses a Code whose `MANIFOLD_REV` is not this
tree's: three families verified against two different kits would prove nothing about the hub
they install on.

**That refusal is equality, so the Manifold pin moves in dependency order across three
repositories.** It is a string comparison of Code's `MANIFOLD_REV` against this one, not
an ancestry test, and Code's own `prepare:integration` compares omp's to Code's the same way; a
`--depth=1` fetch has no history to reason over anyway. So a newer Manifold reaches this tree
last, one reviewable PR per repository, each proved by its own gate:

1. **atyrode/manifold-omp** moves its `plugins/MANIFOLD_REV` and workflow ref; its gate packs
   omp's bundles against the new kit.
2. **atyrode/code** moves its `plugins/MANIFOLD_REV`, its workflow ref and the
   `@atyrode/manifold-omp` pin to a commit of step 1, in both `plugins/package.json` and the
   publishable root `package.json` — a consumer compiles its omp types against the latter.
   Code's `scripts/gate.sh` is the proof.
3. **This repository** moves `MANIFOLD_REV`, both workflow refs, and `CODE_REV` with its
   matching `@atyrode/manifold-code` dependency to a commit of step 2. `deps:code` then agrees
   and the gate composes the complete dependency closure.

A pin naming an unmerged branch commit of the step above is fetchable but temporary: advance it
to that repository's merge commit before this repository's PR leaves draft, because a deleted
branch takes its commits out of reach and `prepare-code.ts` fetches `CODE_REV` by SHA.

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
uninstalls with purge and asserts it is gone. `dev` is the inner loop: it discovers manifests
under `babel/`, installs the baseline before its parts on the named hub, then watches that source
tree and reinstalls only bundles whose sha changed; a browser reload shows the change. Dependency
checkouts under `.integration/` are not this family's plugins and must not enter that walk.
The line above is the integrated preview (`https://preview.manifold.tyrode.dev`) as
addressed from dev-01, where `docker:` delivery copies the bundle into the hub's container and
reads the owner key from its volume, so the key never appears in argv, output or this repository.
Against another hub, pass `--owner-key-file <path>` and `--deliver path`.

### Running optional judgement

`bun babel/jev/tools/seed-questions.ts policy` renders the versioned question literals and response
projections for the `judge` and `pair` service operations. It installs nothing. The deployment must
already have its operator-supplied Jev service policy and credential binding; printing the policy
is not activation evidence. The baseline policy's `suggesters` list must map the caller's
authenticated principal to `atyrode.babel.jev` before it can size the suggestion gap or submit.

Home reads `atyrode.babel.jev.sweepPlan` without invoking a model. **Judge pending corpus** is the
explicit spending action: it walks bounded batches up to the initial pending count, and **Stop
sweep** stops after the current batch. All live record kinds are eligible for a reading; only
unruled records can produce suggestions. **Submit** is a separate action under the caller's own
authority, after the returned count is visible. No Jev door holds `containers:write`.

Positions appear on feed rows and in reception without altering Babel's ranking. They are
browser-session readings, not stored assessments: a reload loses them, while submitted suggestions
remain. The continuation is also transient. Records that produced no durable suggestion can be
offered again; the bounded process memo avoids another payment only while it retains the answer.
A changed bank document or service policy revision changes the pending basis.

The `pairs` door accepts at most 24 anchors, each with its exact record id, revision and kind, and
at most 64 pair judgements. Its pool is those anchors: search neighbours outside it are not
silently hydrated or claimed as checked. Explicit `contradicts` and `supersedes` cuts select which
detectors may speak; missing cuts report uncalibrated and cause no paid call on their own.
Returned suggestions carry `subject` and `aspect` as well as their policy-and-wording basis.
The caller removes the diagnostic `detector` field before submitting through `babel.suggest`.

The local preview exercises real installation, the unbound plan, reading doors and observation
navigation. Funded Feed transitions can be exercised with isolated synthetic action responses;
that verifies the rendered caller, not a paid provider run or coverage of a deployment's corpus.

## Where a change is proved, and how production gets it

**Every change is proved on a preview**, never on production: the integrated preview or a local
preview-equivalent hub, by the loop above. `manifold.tyrode.dev` is installed by the operator, by
hand, from the plugin manager — nothing here automates it, and nothing should.

`.github/workflows/manifold-plugins.yml` is one `uses:` line: manifold's reusable
`plugins.yml@<MANIFOLD_REV>` checks this repository out beside `atyrode/manifold` at the pinned
revision — the layout above, so `tsconfig.json` and `pack.sh` resolve exactly as they do on a
developer's machine — then runs `check`, `test`, `pack` and `verify` and uploads `dist/` as the
`manifold-plugins` artifact. It runs on **every** pull request and on every push to `main`, with
no path filter: this is the repository's only gate, and a filter would let a PR editing a
workflow, a document or the changelog merge with nothing having run at all.

Release delivery is `release.yml`: a `v*` tag runs the same reusable gate against the tagged
revision, builds the dependency closure beside it, attaches `dist/*.manifold-plugin.json` and
its checksums to the GitHub Release — a tag publishes plugin bundles and nothing else — and
hands each asset URL and sha to the integrated preview's receiver (`plugin <url> <sha256>` over
the forced-command key, the same verb a developer runs from dev-01), dependencies first and a
baseline before its parts, so the preview installs the release by itself. That order is read off
the bundles: `scripts/delivery-order.ts` sorts the packed artifacts by their own declared
dependencies through the kit's `familyOrder`, the function `verify` and `dev` install by, so a
plugin added to the repository is delivered without the workflow being edited — and a bundle the
order does not name fails the job rather than riding the release undelivered. Between
releases a preview gets a build through `bun run dev --deliver`, and the sha256 that counts is
the one CI prints. A tag is permanent: a bad one stays and the next patch follows it.

The sha256 is over an artifact's exact bytes, and Bun writes every bundled module's path as a
comment, so a hash reproduces only from the layout above with the same Bun. The pins are what
`engine.plugins.install` demands; `dist/SHA256SUMS` is what carries them.
