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

## The machine half: one bundled file, one pinned engine, four tools the machine provides

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

The operations are `scan`, `archive`, `prepare`, `explore` and `evaluate`. Each takes ONE input
document — a JSON string in the job request, which the engine materializes as a file at
`/inputs/input`, so `--input` is a path and never 64 KiB of argv — and writes every file it
produced flat into the sealed output lease at `/outputs/outputs`, which the hub reads back with
`ctx.jobs.outputs`. The lease is cut from a **managed** location: the alternative, an ordinary
anchor, must already exist on the machine for `write` and refuses a second job for `create`.
`scan` and `prepare` run with `network: "none"`; `explore` and `evaluate` reach the host because
they launch the engine, and `archive` because it reaches the repository and its storage service.

**Four tools are runtime tools the machine's owner provides, not artifacts this manifest pins**:
`bun`, the executable every operation runs; `git`, which reads repository identity for `scan` and
`prepare`; `restic`, which owns the archive's repository format; and — for the two operations that
drive a model — `ca-certificates`, the CA bundle `SSL_CERT_FILE` names, and `system`, the reviewed
libc closure a dynamically linked binary needs. A Manifold job sandbox is built from `/proc`,
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
  runtimeTools.ca-certificates = [{ source = "${pkgs.cacert}/etc/ssl/certs/ca-bundle.crt"; target = "/runtime/bin/ca-certificates"; kind = "file"; }];
  runtimeToolClosures.ca-certificates = [ pkgs.cacert ];
  # `system` is the reviewed libc closure; manifold-omp's runtime-artifacts.json names the exact
  # interpreter and sonames it must supply per platform.
};
```

**`omp` is the one tool this manifest PINS**, by url and digest, from manifold-omp's own
`runtime-artifacts.json` at SDK 18.1.14 — `machine.tools.omp`, one `MachineArtifact` per platform,
which the machine agent downloads and verifies against `entrySha256` before binding it read-only
at `/runtime/bin/omp`. It is pinned rather than delegated because it is the thing being DRIVEN: a
run's answers come from that exact build, and an owner-bound `omp` would let one machine's engine
differ from another's with nothing in the record saying so.

### What launches the model, and what it is never given

Until 2026-09-13 `explore` and `evaluate` launched `code engine`, a Code subcommand that owned
the profile, the credential and the sandbox and wrote a runtime-info sidecar Babel read before
writing a prompt. atyrode/code#153 removed that engine, and Manifold has no plugin-to-plugin call
to replace it with — `ActionCtx` carries no way to reach another plugin's doors, and every
`ctx.jobs` verb is bound to the calling plugin's own id. So Babel's own job launches the engine
(atyrode/babel#279):

```
/runtime/bin/omp --mode rpc --no-tools --no-lsp --no-session --no-extensions --no-rules \
  --no-skills --no-title --auto-approve --config $HOME/.omp/agent/config.yml --cwd <scratch>
```

That argv carries nothing about a model, an account or a provider, and it never will: argv is
world-readable in any process listing on the host. The session travels as two files the OWNER
materializes into the job's private home out of the job's own inputs —
`~/.omp/agent/models.yml` and `~/.omp/agent/config.yml`, declared as `inputFiles` with a
`homePath` — and the inference binding's url and bearer are spliced by the owner into
`models.yml`'s `providers.*.baseUrl` and `providers.*.apiKey` through `jsonValues`. The bearer
therefore never passes through Babel's code, its argv, its environment or a log line, and the
provider credential never enters the sandbox at all.

Admission is over facts rather than over a declaration, which is the substantive improvement on
the sidecar. Before a byte of prompt is written the machine half checks that the boundary around
it is a Manifold job sandbox (its `HOME` is the job's private home and the XDG directories are
inside it) and that the two files above are present and the environment holds no provider
credential variable. A run that fails either is refused with a named reason — `inference_unbound`
— and its receipt records what it was asked to be. Babel's own launch report (`babel.launch/1`)
replaces Code's runtime-info sidecar: the model, the thinking level, the account, the observed
boundary, the models that answered, the exit status, the bounded retry count, and a named failure
(`broker_unavailable`, `rate_limited`, `inference_unbound`) instead of "the engine closed its
stdout before a ready frame".

### The policy the owner installs, and the one table he edits

`explore` and `evaluate` bind ONE service, `atyrode.babel.inference`, and its policy is the
machine owner's to install. `setupInference` (`doors/inference.ts`) assembles and compare-and-sets
it against the omp gateway actually installed on that host, which is why nobody types it: three of
its fields are pins of that installation and only a live `readConfiguration` can produce them.
This is what it installs, so an operator reading a machine's service configuration knows what he
is looking at:

```json
{
  "serviceId": "atyrode.babel.inference",
  "revision": "1",
  "runtime": {
    "pluginId": "atyrode.omp.gateway",
    "operationId": "atyrode.omp.gateway.serve",
    "installationRevision": "<the gateway installation on THIS machine>",
    "artifactSha256": "<its artifact digest>",
    "resourceBindingDigest": "<its resource binding digest>",
    "input": { "accountPool": { "input": "accountPool" } }
  },
  "maxConcurrent": 16,
  "operations": {
    "models": {
      "kind": "http-proxy",
      "method": "GET",
      "path": "/v1/models",
      "request": { "kind": "none" },
      "response": {
        "kind": "stream",
        "disclosure": "full",
        "contentTypes": ["application/json"],
        "headers": []
      },
      "timeoutMs": 60000,
      "maxRequestBytes": 65536,
      "maxResponseBytes": 4194304
    },
    "stream": {
      "kind": "http-proxy",
      "method": "POST",
      "path": "/v1/pi/stream",
      "request": { "kind": "json", "disclosure": "full" },
      "response": {
        "kind": "stream",
        "disclosure": "full",
        "contentTypes": ["application/json", "text/event-stream"],
        "headers": []
      },
      "meter": { "kind": "pi-native-usage" },
      "timeoutMs": 300000,
      "maxRequestBytes": 16777216,
      "maxResponseBytes": 268435456
    }
  },
  "prices": {
    "models": {
      "anthropic/claude-opus-5":     { "inputPerMillion": 5000000,  "outputPerMillion": 25000000, "cachedInputPerMillion": 500000 },
      "anthropic/claude-opus-4-8":   { "inputPerMillion": 5000000,  "outputPerMillion": 25000000, "cachedInputPerMillion": 500000 },
      "anthropic/claude-sonnet-5":   { "inputPerMillion": 2000000,  "outputPerMillion": 10000000, "cachedInputPerMillion": 200000 },
      "anthropic/claude-sonnet-4-6": { "inputPerMillion": 2000000,  "outputPerMillion": 10000000, "cachedInputPerMillion": 200000 },
      "anthropic/claude-haiku-4-5":  { "inputPerMillion": 1000000,  "outputPerMillion": 5000000,  "cachedInputPerMillion": 100000 },
      "anthropic/claude-fable-5-1":  { "inputPerMillion": 10000000, "outputPerMillion": 50000000, "cachedInputPerMillion": 250000 }
    }
  }
}
```

Four things in it are load-bearing and one is a default:

- **`runtime`, not `origin`.** An origin policy points at a provider and needs a credential the
  owner holds; this one points at another plugin's machine operation, so there is no credential in
  the policy at all. The gateway resolves one from the machine's broker for the pool it was handed,
  and the job gets a loopback url and a bearer minted for it alone. It carries no `scope`, because
  the candidates a hub offers carry none and job scope is the default: an INSTANCE-scoped
  candidate is refused by name (`doors/inference.ts`), since an instance runtime may hold only
  literal inputs and so could not carry the job's account pool at all.
- **`input: {accountPool: {input: "accountPool"}}`** is how a run names the account it spends: the
  owner maps the CALLING job's `accountPool` input into the gateway's own, so the pool the launch
  posted is the pool that gateway resolves a credential for. manifold-omp installs its own `omp`
  service exactly this way; #267 therefore needs no `credentialRef` selector on a job request.
- **`meter` on `stream` only.** Listing models costs nothing; the streaming call is what spends,
  and `pi-native-usage` is the kind that reads omp's own wire (`usage.input`, `usage.output`,
  `usage.cacheRead`). `openai-usage` over this wire would refuse every call rather than silently
  mis-read one. The kind is POLICY CONTENT and appears in no manifest: a manifest's service
  declaration is `{serviceId, revision, operationIds}` (`ServiceBindingSchema`) and names no
  meter, so nothing about the kind is baked into the artifact an owner installs.

  **Which hub revisions run what.** manifold#570 adds the kind and #572 landed it; `MANIFOLD_REV`
  0bc76660 carries it, so the SDK this tree typechecks, tests and packs against knows it and the
  policy is handed to `configureConfiguration` with no cast. On a HUB older than that revision the
  consequence is wider than the write:

  - `setupInference` is refused by the hub's own schema, by name, and `launchPreview` reports
    the session policy as `unsupported` with that sentence as its evidence (#284) rather than as
    a machine nobody configured;
  - the machine half cannot be DEPLOYED there at all. The deployment review asks for a policy
    per service any operation binds — `servicePolicies` flattens
    `Object.values(machine.operations).flatMap(op => op.services)`
    (manifold `packages/server/src/job-service.ts`) — so the absent inference policy refuses the
    whole target `service_definition_changed`, and `scan`, `prepare` and `archive` are collateral
    even though they bind no model. Nothing in this plugin can narrow that: the requirement is
    real for `explore` and `evaluate`, and the review's scope is the hub's.
  - once deployed on a hub that DOES know the kind, admission is per operation
    (`resourceRefusal` reads only that operation's bindings), so a machine whose gateway is down
    still scans, prepares and archives while `explore` and `evaluate` are refused by name.
- **`prices.models` keys are FULLY QUALIFIED** (`anthropic/claude-sonnet-5`, never
  `claude-sonnet-5`): the owner prices a call by the verbatim `modelId` the request body carried,
  and omp's gateway keys its model map by `<provider>/<id>`. A bare key prices nothing, and a model
  the table does not price is refused `service_price_unknown` before its first call whenever the
  request carries a cost ceiling — which every Babel run's does.
- **THE PRICE TABLE IS OPERATOR-EDITABLE POLICY AND NOT A FACT ABOUT ANTHROPIC.** The numbers
  above are the self-serve list prices observed 2026-09-14, in integer micro-dollars per million
  tokens, and they are a default so that installing the service does not require retyping a price
  table. An operator on an enterprise rate, a batch discount or another provider edits the
  installed policy; a price change is a new policy revision he consents to, and `launchPreview`
  states the price it FOUND on the machine, never the table in this repository. A REFRESH DOES NOT
  TAKE IT BACK: `setupInference` exists for the runtime pins, so when a policy is already there it
  carries the installed table through verbatim and rewrites the pins alone — reinstating these
  defaults would reprice every run behind the owner's back and move the policy digest a deployment
  is pinned at.

The ceiling is the other half and it comes from the other side: the operator's per-run allowance
leaves the hub as `limits.inference.costMicros` on the job request (`server/plan.ts`
`inferenceCeiling`, rounded UP so a ceiling is never quietly tightened), and the OWNER refuses the
call that would pass it. Nothing in the policy above states a budget, and nothing in a request
states a price.

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
