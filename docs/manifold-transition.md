# Babel as a manifold plugin: the transition

Status: operator direction, revised 2026-09-02. Every manifold fact below was
read at manifold `dev` @ `68f102e`; every Babel fact at the working tree of the
same date. Tags are `exists` (in code, cited), `declared` (named in a ratified
manifold document, not implemented), `absent` (searched for, not found; the
search is in the brief that reports it).

The six research briefs this document rests on are scratch, not record; the
record is this file: `.omp/research/manifold-pluginengine.md`,
`manifold-permissions.md`, `manifold-machines.md`, `manifold-webshell.md`,
`manifold-operations.md`, `manifold-babelsurface.md`. The runs console has its
own document, `docs/runs-interface.md`, which owns the streaming question and
the run projection; this document names its conclusions and does not restate
them.

---

## 1. Status and direction

**The invariant.** Babel's final form is a manifold plugin. Its whole web UI
becomes plugin-rendered UI with full integration into manifold's workspace,
not a terminal and not a separate web application; its fleet becomes
manifold's enrolled machines; the plugin requests permissions and exposes what
it does on machines. Babel adapts to manifold. What manifold cannot offer is
filed as a manifold issue so manifold can evolve, never worked around in Babel
with a second mechanism. Every Babel process is revisited through this prism.

**What "stepwise" permits.** The React shell (`web/`, served by
`internal/web`) stays until the plugin is proven end to end, so an operator
never loses a working surface. It permits nothing else: from this date nothing
new is built PTY-bound or web-shell-bound. A capability that would land in
`internal/web` lands instead in the host-independent engine API (§4, D1), and a
capability that would ride a terminal waits for the door or the exec primitive
(§4, D2). The final step (§5, step 6) retires `internal/web` and `web/`.

**Manifold revision studied.** `dev` @ `68f102e` (2026-09-02 02:15 +0200).
`origin/main` is at `59e221b` "release: v0.4.4" (2026-08-27); `dev` is 30
first-parent commits ahead of it (`git log --oneline --first-parent
origin/main..dev | wc -l`). The newest tag, `v0.5.0` (2026-08-30), was cut from
`dev` commit `9d37e5e` and is not an ancestor of `main`. `dev` is the line
Babel's work targets; `main`'s position is a manifold housekeeping matter
(`.omp/research/manifold-issues.md`, general improvements).

**Settled before this document** (operator): manifest ids live
in the `babel.<x>` namespace; secrets stay outside manifold (the dotfiles
Bitwarden ceremony delivers Babel's storage document per machine, and the
plugin holds no credential); Babel never publishes or applies proposals; friction
remains Babel's axiomatic center; the hub runs a babel instance configured like
any other machine. Decisions D1 to D4 of 2026-09-02 are in §4 and are settled.

---

## 2. What manifold is, for a Babel reader

### 2.1 The axioms and the plane rule

`AXIOMS.md` is manifold's constitution, amended by operator ratification only
(`AXIOMS.md:3-19`). Six axioms (`AXIOMS.md:23-115`):

- **A1** Everything above the floor is a plugin: a manifest declaring what it
  contributes plus actions declaring the capabilities they need; plugins load
  through one registry, are enabled or disabled per workspace while the page
  runs, and collide loudly. There is no privileged "core" mechanism; `engine.*`
  is the one reserved namespace.
- **A2** Multiplayer by design: every capability is reachable identically by a
  local human, a remote human and an agent, over the UI and over the API; the
  door is the only place authority is decided; solo is a room of one.
- **A3** Moddable by design: a stranger's agent can author a plugin against
  documented interfaces without reading the engine; contracts are sandbox-shaped
  on purpose so an isolated runner can arrive later behind the same manifest.
- **A4** Sovereign nodes: every node has one owner, one home, one canonical
  `manifold://` address; composition is projection through capability-scoped
  pipes.
- **A5** Authority is a waterfall of grants on the node tree, evaluated
  root-to-node, deeper beating shallower, principal beating class, deny beating
  allow at equal specificity; landed 2026-09-01 (`AXIOMS.md:76-88`).
- **A6** Every exercise of authority leaves a trace: one durable row per
  dispatch, written ahead of the effect, refusals included, secrets redacted by
  the log's own rule (`AXIOMS.md:90-115`).

The **plane rule** (`AXIOMS.md:123-145`) assigns every piece of state to one
plane: **action** (legality or effect depends on state or authority the actor
lacks; `POST /api/actions/:name`), **document** (a per-element edit whose worst
merge a human accepts; Yjs), **presence** (dies with the connection, never
persisted), **events** (declared notifications at the doors, topics are nodes,
no queue semantics; landed wave 2). Continuous streams (PTY I/O, cursor motion,
live drags) stay channel traffic; "an action fires at the commit point of a
gesture, never per frame."

### 2.2 The floor and the plugin territory

A pillar is floor if and only if it passes all three of bootstrap circularity,
neutrality (zero domain nouns) and arbitration (`AXIOMS.md:381-397`); failing
one makes it a plugin. The registries in `REGISTRY.md` (pillars, floor rows,
lexicon, `cssFamilies`, device-local, gate contracts, budgets) are read in both
directions by `bun run verify:axioms` (static S1 to S16 plus browser R1 to R10;
`REGISTRY.md:2069-2138`) and `verify:trace` (T1 to T5). A boundary crossed
without a registry edit fails `bun run gate` (`AXIOMS.md:9-14`).

### 2.3 What a plugin structurally is

- **Package.** `packages/plugins/<name>`, published as
  `@manifold-plugin/<name>`, exporting up to three subpaths: `.` (manifest and
  action definitions), `./server` (handlers), `./web` (panels, sections,
  renderers, tools) (exists, `docs/PLUGINS.md:23-46`;
  `packages/plugins/machines/package.json:5-9`). Its source imports are limited
  to `@manifold/protocol`, `@manifold/scene`, `@manifold/sdk` and
  `@manifold/plugin` (gate S2); its own npm dependencies are unrestricted by the
  gate and governed by review under invariant 8 (`AGENTS.md:116-117`).
- **Manifest.** Inert, strict-schema data (exists,
  `packages/protocol/src/plugin.ts:510-556`): `id` matching
  `^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`, at most 64 characters, so at least
  two segments (`packages/protocol/src/plugin.ts:22-23`); `version` (display
  only); `capabilities` (a ceiling, at most 16, every member drawn from the
  closed nine-member `CAPS` enum, `packages/protocol/src/capabilities.ts:11-22`);
  `contributes` (bounded arrays of `panels`, `seats`, `sections`, `elements`,
  `disciplines`, `tools`, `events`, `routes`, `settings`; no HTTP-route kind);
  `dependencies` (other plugins only); `dataVersion` (governs `ctx.storage`
  shape); `dormant`; `purges`; `entry` reserved for the dynamic wave
  (`packages/protocol/src/plugin.ts:554-555`).
- **Actions are the only door.** `defineAction({name, title, caps, input,
  result, cleanup?, scope?})`; wire name `${manifest.id}.${name}`; called only
  by `POST /api/actions/:name`, answering `{ok:true,result}` or
  `{ok:false,denial:{rule,message}}` (exists, `packages/plugin/src/action.ts`;
  `docs/PLUGINS.md:257-408`). The denial ladder is monotonic: `unknown_action`,
  `plugin_disabled`, `forbidden` (scope), `forbidden` (caps at the node),
  `invalid_args`, `refused` (`docs/PLUGINS.md:366-408`). Inbound bodies are
  capped at 1 MiB (`packages/server/src/http.ts:36`).
- **Server half.** `{manifest, actions, handlers, lifecycle?, migrations?}`.
  A handler receives `ActionCtx` (exists,
  `packages/server/src/plugin-host.ts:249-350`): principal, auth, scope, the
  store, rooms, broker, `machines.isOnline()` only
  (`packages/server/src/plugin-host.ts:237-239`), placement, host, identity,
  dials, `storage`, `now()`, `newId()`, `emit`. No timer, no spawn, no fetch, no
  filesystem member; ADR 0010 calls plugins "trusted in-process code" with "no
  sandbox in this wave and no pretence of one"
  (`docs/decisions/0010-plugin-engine-and-action-plane.md:63-65`), so the
  runtime does not forbid `Bun.spawn` or `fetch`; no shipped plugin uses them.
- **Browser half.** Panels and sections receive one prop, `HostServices`
  (exists, `packages/plugin/src/host.ts:351-406`): the typed `client`
  (`SessionHandle`), `principal`, `token` (this device's live bearer), the
  viewed container, `navigate`, viewport/authoring/tile-geometry handles,
  `assembly` (read-only roster snapshot), `topics`. Push reaches a plugin only
  through `client.subscribe` (the event plane) or `usePolledResource`, which
  reads once at mount, re-reads on a matching event, and polls at
  `FALLBACK_POLL_MS = 2_000` only while the socket is down or the feed has no
  topics (`packages/plugin/src/polled-resource.ts:77,274-277`). Direct `fetch`
  is disallowed (`docs/PLUGINS.md:752-838`).
- **Storage.** `ctx.storage` is "the only place a plugin persists anything":
  one shared SQLite table `plugin_kv`, namespaced by plugin id, synchronous
  `get/set/delete/keys(prefix)`, keys matching
  `^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,127}$`, values at most 64 KiB
  (`MAX_STORAGE_VALUE_BYTES = 64 * 1024`,
  `packages/plugin/src/storage.ts:58,74`), `$`-prefixed keys reserved. A plugin
  cannot declare a table (`docs/PLUGINS.md:485-550`).
- **Events.** A plugin declares kinds in `contributes.events` and emits through
  `ctx.emit` inside an action handler; emission is staged and flushes only if
  the handler resolves `ok` (`packages/server/src/plugin-host.ts:1207-1210`).
  Payloads are flat, at most 16 scalar keys (`EventPayloadSchema =
  boundedPayloadSchema("event")`, `packages/protocol/src/events.ts:73`);
  subscriptions are bounded at 64 topics per frame and 256 per connection
  (`packages/protocol/src/events.ts:82,91`); there are no offsets, no replay:
  catch-up is reading state (`AXIOMS.md:137-145`).
- **Settings.** `contributes.settings: {id, title, kind, default}` with
  `SETTING_KINDS = ["boolean"]` (`packages/protocol/src/plugin.ts:104`); the
  value is per principal, written through `engine.plugins.setSetting` and
  rendered generically by the plugin manager (`docs/PLUGINS.md:711-723`).
- **Lifecycle.** `onEnable`, `onDisable`, `onAssemblyChanged`, `onPurge` are
  one-shot transition hooks, each bounded to `LIFECYCLE_TIMEOUT_MS = 2_000`, none
  able to veto (`packages/plugin/src/lifecycle.ts:15-21`). There is no boot
  hook and no recurring hook.

### 2.4 How plugins are loaded

Today: static assembly. `packages/server/src/assembly.ts:1-26` is a hand-written
import block feeding `SERVER_PLUGIN_DEFS`, "THE registration point, and the
only server file allowed to import `@manifold-plugin/*`"
(`packages/server/src/assembly.ts:60-63`); `packages/web/src/assembly.ts` is its
browser twin. "There is no discovery, no filesystem scan, no load order:
assembly is data, and an unregistered package is not a plugin"
(`docs/PLUGINS.md:40-42`). `@manifold-plugin/<name>` resolves through Bun
workspaces only (`package.json:5-8`), and `@manifold/plugin` itself is
`"private": true` with `workspace:*` dependencies
(`packages/plugin/package.json:3,10-13`): nothing a package outside the
repository can install. Registration also touches `package.json`'s `check`
script and `scripts/gate.ts`'s `packages` array for gate coverage.

Declared: the **marketplace and dynamic plugin distribution** wave, "plugin code
that is not compiled into the build", hard-gated on ADR 0016's stage 1
(`AXIOMS.md:247-267`). ADR 0016 is ratified (2026-09-01) and states "No code
depends on this file until stage 1 is scheduled"
(`docs/decisions/0016-plugin-isolation.md:4-20`). Its shape: one OS process per
isolated plugin on the server and one Worker per isolated plugin in the
browser; first-party rows keep running in-realm, an installed row runs isolated
(`docs/decisions/0016-plugin-isolation.md:178-200`). An isolated plugin reaches
exactly the ctx slices it declares over an RPC, its own storage (which becomes
asynchronous for everybody), the action door and the event plane
(`:202-217`); it cannot reach the DOM ("Ever."): its UI is "a closed, host-owned
component vocabulary, serialized component trees in, named callbacks out",
and it is not handed `HostServices.token` or the engine's React library
(`:219-240`). Consent is a grant on the roster row, effective caps `granted ∩
declared` (`:255-264`). Staging is engine, then protocol (install and uninstall
doors from a local artifact), then distribution and signing (`:302-323`), each
landable alone; none is scheduled.

### 2.5 Identity, grants, trace

Two credential kinds authenticate: the owner key (root, never expires) and
bearer tokens hashed at rest, each a grant reference (`docs/CONTRACTS.md:107-246`).
One identity shape for humans and agents, `Principal {id, kind: "human" |
"agent", name, color, origin?}` (`packages/protocol/src/principal.ts:5-27`).
Interactive tokens expire after 14 days; agent-kind and machine tokens never
expire, by two independent code paths (`docs/CONTRACTS.md:125-137`). A grant is
a row `{principal | class, node, caps, effect, reach}` on the node tree; the
addressable node kinds are exactly `terminal`, `container`, `element`, `tile`,
`principal`, `plugin`, `action` (`packages/protocol/src/uri.ts:33-45`); there is
no `machine` kind. Every dispatch at a registered door writes one trace row
before the handler runs (`packages/server/src/plugin-host.ts:1172`) and settles
it once (`:1181-1207`); the payload is redacted by `SECRET_FIELD =
/(token|key|authorization|secret)/i` (`packages/server/src/log.ts:36`), bounded,
read through `core.events.list` only, and pruned with the journal.

### 2.6 Machines and the agent

Enrollment is the action `core.machines.enroll` (cap `machines:mint`),
idempotent by name, rotation revokes and fences the old socket
(`docs/ENROLL.md:28-66`; `packages/plugins/machines/src/index.ts:23-29`).
`core.machines` publishes three doors, `list`, `enroll`, `revoke`. The agent is
`packages/agent`, a PTY dial-out daemon whose whole vocabulary is seven verbs
each way (`packages/protocol/src/machine.ts:37-93`): agent to server `hello`,
`created`, `create_error`, `output`, `snapshot`, `exited`, `pong`; server to
agent `welcome`, `create`, `input`, `resize`, `kill`, `snapshot_request`,
`ping`. `create` carries geometry, `cwd` and `env`, never a command: the PTY runs
`$SHELL`, else `bash`, else `sh` (`packages/agent/src/terminal.ts:115-124`). A
program inside a manifold PTY receives `MANIFOLD_URL` and a per-terminal
session-agent `MANIFOLD_TOKEN` (`packages/server/src/terminal-broker.ts:494-542`)
and can call the door as a scoped principal. An agent restart kills every PTY it
owns (`docs/ENROLL.md:157`). Supervision across reboots is operator
infrastructure: a systemd user unit with `Restart=always` or a launchd plist,
documented but not generated (`docs/ENROLL.md:96-126`). The hub is also a
machine: the compose container spawns an in-container agent named `hub`, and
real shells on the host require enrolling the host natively as a spoke
(`docs/SELF-HOST.md:211-216`). Protocol version is 21 and the machine channel
accepts agents at `{16..21}` (`packages/protocol/src/version.ts:2,216-218`); a
spoke newer than the hub is refused silently from the spoke's side, the incident
that produced invariant 10 (`AGENTS.md:126-144`).

### 2.7 Persistence

One SQLite database in WAL mode, `SCHEMA_VERSION = 15`
(`packages/server/src/db.ts:8`), holding containers, scene documents, the
events journal (plain events and trace rows), principals, tokens, machines,
terminals, `plugin_kv`, shares, dials, grants and meta. No PostgreSQL, no object
or blob storage, no per-plugin table (absent; the search is in
`.omp/research/manifold-machines.md` facts 47 to 49). The journal is pruned at 30
days, 10,000 rows per container, 100,000 in the container-less bucket
(`docs/CONTRACTS.md:991-994`).

### 2.8 Build, release, branch discipline

Bun is pinned exactly at 1.3.13 by ADR 0001
(`docs/decisions/0001-runtime-and-pins.md:5,26`) and by the release runner
(`.github/workflows/release.yml:28`), while `Dockerfile:4,18` builds `FROM
oven/bun:1`, a floating tag. Two Nix outputs, `manifold-agent` and
`manifold-server`, both `bun build --compile`. Production is Docker Compose
behind caddy (`docs/SELF-HOST.md:3-19`); upgrade is `git pull && docker compose
up -d --build` (`:205`). `.github/workflows/` holds exactly `main-guard.yml`
(PRs into `main` must come from `dev`) and `release.yml` (tag-triggered); no
workflow runs `bun run check` or `bun run gate` on a pull request into `dev`.
`scripts/release.ts` runs on `main` only. Every planned change starts from a
GitHub issue; one `## [Unreleased]` changelog bullet per user-visible change;
new runtime dependencies need a dated ADR (invariant 8); protocol bumps ship as
dedicated commits and the hub is deployed at or ahead of any release whose
`PROTOCOL_VERSION` exceeds the deployed one (invariant 10).

---

## 3. Capability map

One row per Babel need. "Manifold offers" carries the tag and the citation;
"decision or draft issue" names the settled decision (§4) or the draft title in
`.omp/research/manifold-issues.md` (manifold), `.omp/research/babel-issues.md`
(Babel) or `.omp/research/runs-issues.md` (runs).

| Need | Manifold offers | Missing | What Babel changes | Decision or draft issue |
| --- | --- | --- | --- | --- |
| **N1** Operator identity, principals, tokens | exists: one `Principal` shape for humans and agents (`packages/protocol/src/principal.ts:5-27`); owner key root plus capability-scoped, attenuation-only, revocable bearer tokens through `core.access` (`docs/CONTRACTS.md:107-246`); agent tokens never expire (`docs/CONTRACTS.md:134-137`). | A second identity layer distinguishing two humans (declared, ADR 0019 §6); nothing Babel needs. | Drops the bootstrap-nonce and session-cookie scheme (`internal/web/session.go:37-49`) with `internal/web`; the operator is the owner key or a delegate human principal; each machine's babel is an agent-kind principal; Babel's per-run `worker.Grant` stays Babel-internal and never becomes a `Cap`. | D2; babel "Agent-kind principal enrollment: babel holds a scoped manifold token per machine and announces presence" (#143). |
| **N2** Machines and per-machine grants | exists: idempotent enrollment, revocation, live online state (`core.machines`, `packages/plugins/machines/src/index.ts:23-29`; `ActionCtx.machines.isOnline`, `packages/server/src/plugin-host.ts:237-239`); `machine_enrolled/online/offline` events. | absent: a `machine` node kind (`packages/protocol/src/uri.ts:33-45`), so no grant can name one machine; absent: any way to run work on a machine except a PTY. | The manifold roster becomes the fleet; Babel's host identity aligns with the machine name; `hosts` stays archive bookkeeping; `--expect` on `archive fleet` is replaced by the roster. | D2; manifold "A `machine` node kind in the manifold:// grammar so grants can scope per enrolled machine" (atyrode/manifold#157); babel "Align Babel host identity with the manifold machine name" (#144). |
| **N3** Timers and long-running loops | absent: no scheduler, timer or supervised-job primitive for a plugin (`ActionCtx`, `packages/server/src/plugin-host.ts:249-350`); lifecycle hooks are one-shot and 2 s bounded (`packages/plugin/src/lifecycle.ts:21`); the Budgets register has no server-half row (`REGISTRY.md:1944-1993`). | A supervised long-lived primitive, hub or spoke. | The hourly archive timer and `babel conductor run` stay OS-supervised (`internal/conductor/conductor.go:39-42`; `docs/runbook.md:53-70`) and become visible and drivable through the door. | D2; manifold "Typed exec/job primitive on the machine channel" (atyrode/manifold#156). |
| **N4** Secrets | absent: no vault, no encrypted tier, no per-plugin env injection; `ctx.storage` is a plaintext string map (`packages/plugin/src/storage.ts:58-74`); `ActionCtx` has no env, config or secrets member (`packages/server/src/plugin-host.ts:249-350`); the word "vault" occurs once in the tree, as a negation (`docs/decisions/0020-desktop-shell.md:414`); searches in `.omp/research/manifold-permissions.md` facts 29-37. | Nothing requested: secrets stay outside manifold by decision. | Nothing: the ceremony keeps delivering `storage.json` per machine (`SPEC.md:77-81`); the plugin holds no credential; no `babel.*` door accepts a raw secret argument. | Settled earlier (operator); manifold general improvement "Widen the trace and log redaction field-name rule to password, credential and passphrase" (atyrode/manifold#165). |
| **N5** Durable storage | exists: `plugin_kv`, string values at most 64 KiB (`packages/plugin/src/storage.ts:74`). absent: relational tables, blob or object storage, PostgreSQL; one SQLite schema ledger owns every table (`packages/server/src/db.ts:8`; searches in `.omp/research/manifold-machines.md` facts 44-49). | Nothing for the stores: both placements are Babel's own modes. An enum, workspace-scoped setting kind to render the placement. | Storage placement is a plugin setting with two values, master (Babel's local mode on the hub's host, non-durable) and external (shared mode, the fleet default), both runnable today; `plugin_kv` holds pointers and projections, never the catalog or the archive. | D3 (revised); manifold "Workspace-scoped plugin settings with an enum kind" (atyrode/manifold#158) for the pane; babel "Storage placement setting: master (local mode, non-durable) or external (shared mode)" (#146). Manifold "Plugin-owned durable storage beyond plugin_kv" (atyrode/manifold#159) stays filed as a general need, off Babel's path. |
| **N6** Real-time events | exists: the event plane, door-gated, flat 16-key payloads, delivered to live subscribers, no replay (`packages/protocol/src/events.ts:73,82,91`; `packages/server/src/plugin-host.ts:1207-1210`). absent: any plugin-declarable continuous stream; the session-channel frame vocabulary is closed (`packages/protocol/src/session.ts:155,355`). | A continuous, structured, sub-second channel for run progress. | Milestone events at run boundaries now; the stream channel is the end state; presence carries a throttled snapshot in the interim. | D4; manifold "Plugin-declarable continuous stream channel on the session socket" (atyrode/manifold#169) (runs); babel "Run milestone events: a declared, bounded vocabulary emitted at run boundaries" (#152) (runs). |
| **N7** UI surfaces | exists: sidebar sections, panels and seats, container disciplines with a `ContainerRenderer`, elements, tools, browser-side routes, one notice stack, one command palette (`packages/protocol/src/plugin.ts:260-410`; `docs/PLUGINS.md:588-923`). declared: under ADR 0016 an installed plugin paints only through a closed host component vocabulary (`docs/decisions/0016-plugin-isolation.md:219-240`). absent: a virtualized list, nested sidebar IA, non-boolean settings, any sanitizer (`.omp/research/manifold-webshell.md` facts 5, 12, 21). | The component vocabulary itself, sized to a real surface; a sanitizer. | Every Babel area becomes a section, a discipline or a route; Babel brings one sanitizer for every plugin-rendered surface; Babel's surface inventory (`.omp/research/manifold-babelsurface.md` §1) is the sizing input for the vocabulary. | Step 5; manifold "Size the isolated-plugin component vocabulary against a real plugin surface inventory" (atyrode/manifold#160); manifold general improvement "Promote door-form rendering out of core.debug into @manifold/plugin/ui" (atyrode/manifold#168); babel "Babel surfaces as plugin contributions: sections, disciplines and routes" (#147); babel "One untrusted-content sanitizer for every plugin-rendered Babel surface" (#142). |
| **N8** Traceability | exists: one trace row per dispatch, write-ahead, refusals included, read through `core.events.list` (`packages/server/src/plugin-host.ts:1172-1207`; ADR 0018). | A handler is not handed its trace id (`ActionCtx`, `packages/server/src/plugin-host.ts:249-350`). | Keeps receipts and dispositions as Babel's record (`internal/run/receipt.go`); gains a generic root-readable audit row for every operator call through the door; records the acting principal on the receipt; correlates by door, principal and time window until the id is handed over. | Settled; manifold general improvement "Hand an action handler its trace id" (atyrode/manifold#166). |
| **N9** Executing the binary on hub and spokes | exists, undeclared: a server half may `Bun.spawn` in-process today (ADR 0010, `docs/decisions/0010-plugin-engine-and-action-plane.md:63-65`), unmediated and removed by isolation (`docs/decisions/0016-plugin-isolation.md:202-217`). absent: any exec verb on the machine channel (`packages/protocol/src/machine.ts:37-93`). | A typed exec/job primitive; a way for an isolated server half to reach a local companion service. | Babel runs on every machine, hub included, as OS-supervised services fronted by an agent principal; the plugin's server half reads the hub's engine service; nothing is driven through a PTY. | D2; manifold "Typed exec/job primitive on the machine channel" (atyrode/manifold#156); manifold "Companion-service seam for an isolated plugin's server half" (atyrode/manifold#155); babel "Action-door API over the babel engine: the babel.* action set and the headless engine service behind it" (#141). |
| **N10** Packaging and distribution | exists: in-tree workspace packages only (`package.json:5-8`; `docs/PLUGINS.md:40-42`). declared: the dynamic wave with `entry` reserved (`packages/protocol/src/plugin.ts:554-555`; `AXIOMS.md:247-267`), artifact hash pinned on the roster row (ADR 0016 R8). absent: an out-of-tree authoring path (`packages/plugin/package.json:3`), a manifest field for an external binary dependency. | Out-of-tree loading; consumable SDK packages; an external-binary declaration. | The plugin package lives in `atyrode/babel` and ships as a hashed artifact from Babel's release; the Go binary keeps its own Nix pin through dotfiles; the plugin declares the binary and protocol versions it needs. | D1; manifold "Schedule ADR 0016 stage 1: the isolation runner, so an out-of-tree plugin can load" (atyrode/manifold#151); manifold "Dynamic plugin loading from a pinned local artifact: evaluate entry, add install and uninstall doors" (atyrode/manifold#152); manifold "Manifest declaration of an external host-binary dependency" (atyrode/manifold#153); babel "Babel plugin package and manifest in atyrode/babel" (#140). |
| **N11** Cross-instance sharing | exists: the instance channel federates two manifold servers, control only (ADR 0014; `docs/CONTRACTS.md:1557-1615`); `Principal.origin` is data, never a branch (`packages/protocol/src/principal.ts:26`). | Nothing Babel needs: Babel's fleet is the machine channel, not the instance channel. | Nothing. Babel's cross-host sharing is its storage placement (D3); a second manifold instance is out of scope. | Settled: no issue. |
| **N12** The analysis sandbox | absent: no `bwrap` anywhere in the tree (whole-repository search); a PTY is a bare `Bun.spawn` under the agent's user with no rlimits, cgroup or namespace (`packages/agent/src/terminal.ts:219-222`); "no sandbox in this wave" is said of plugin code, not of processes (`docs/decisions/0010-plugin-engine-and-action-plane.md:63-65`). | Nothing requested. | Nothing: the sandbox runs under Babel's OS-supervised process on the machine (`docs/sandbox-threat-model.md`), never as a descendant of a manifold PTY (`docs/ENROLL.md:157`); the manifold agent is not in the sandbox's trust chain. | D2 (the exec/job primitive later carries it); no issue. |

---

## 4. Decisions (settled 2026-09-02)

### D1 Loading: manifold builds dynamic, out-of-tree loading first

Manifold builds dynamic loading first: ADR 0016 stage 1 and the marketplace
and dynamic-distribution wave, both declared and unscheduled today
(`AXIOMS.md:247-267`; `docs/decisions/0016-plugin-isolation.md:4-20,302-323`).
The Babel plugin package lives in `atyrode/babel` and is loaded out of tree.
There is no vendoring into `packages/plugins` as an interim.

Reaffirmed 2026-09-02 against the in-tree-first alternative this review
proposed (SPEC.md decision 83): a thin `packages/plugins/babel` in manifold now,
extracted when the loader exists, was rejected by the operator on three
grounds — it would push the marketplace and dynamic-distribution wave back, it
would mint debt to be paid at extraction, and it would contradict the
direction that manifold builds exactly what Babel needs to exist. The
consequence is accepted and named: nothing of Babel renders inside manifold
before out-of-tree loading exists, and step 1 is entirely manifold work while
Babel's host-independent items (#141, #142, #143, #151) proceed. The
circularity in "size the isolated vocabulary against a real plugin surface"
(atyrode/manifold#160) is resolved on paper: the surface inventory is SPEC.md
§8.2–§8.3 and `docs/runs-interface.md`, not a plugin in the tree.

Consequences:

- The first transition steps are manifold work. Babel builds host-independent
  contracts in parallel (the engine service and engine API, the `babel.*` action
  set as data, milestone events, enrollment, the sanitizer), so nothing waits
  idle and nothing is thrown away when loading lands.
- An installed roster row runs isolated
  (`docs/decisions/0016-plugin-isolation.md:196-200`). Babel's server half is
  its own OS process supervised by the host; its browser half is a Worker that
  never touches the DOM and paints through the closed host component vocabulary
  (`:219-240`). "Full plugin-rendered UI" therefore means Babel's screens are
  serialized component trees in manifold's vocabulary. That vocabulary is not
  designed; Babel's surface inventory is the strongest sizing input it can have.
- An isolated server half reaches only declared ctx slices, its storage, the
  door and the event plane (`:202-217`); it is not designed to `fetch` or spawn.
  The plugin's server half must reach the hub's engine service, so a declared
  seam is a prerequisite.
- The plugin requests permissions for real: the install grant intersects with
  the declared ceiling (`:255-264`). The names it can request are the closed
  nine (`packages/protocol/src/capabilities.ts:11-22`); Babel's own authority
  domain (archive, analysis, review) has no name in that enum.
- Invariant 8 no longer governs Babel's own npm dependencies (they are not in
  manifold's tree); ADR 0016 R8 pins the artifact hash on the roster row, so
  Babel's release produces a hashed plugin artifact beside the Go binary.
  Dependency version ranges arrive with dynamic distribution
  (`docs/decisions/0013-plugin-behavioral-contract.md:939-941`); the plugin
  states the manifold protocol version it was built against.
- `@manifold/plugin`, `@manifold/protocol`, `@manifold/sdk` and
  `@manifold/scene` are private workspace packages
  (`packages/plugin/package.json:3,10-13`); an out-of-tree plugin needs them
  published or pinned as an artifact, which is part of the loading prerequisite.

Manifold prerequisites: "Schedule ADR 0016 stage 1: the isolation runner, so
an out-of-tree plugin can load" (atyrode/manifold#151); "Dynamic plugin loading from a pinned local
artifact: evaluate entry, add install and uninstall doors" (atyrode/manifold#152); "Companion-service
seam for an isolated plugin's server half" (atyrode/manifold#155); "Plugin-declared capability names
beyond the closed nine-member enum" (atyrode/manifold#154); "Manifest declaration of an external
host-binary dependency" (atyrode/manifold#153); "Size the isolated-plugin component vocabulary against
a real plugin surface inventory" (atyrode/manifold#160). Babel work: "Babel plugin package and manifest
in atyrode/babel" (#140); "Plugin artifact in Babel's release: hashed, versioned
against the manifold protocol" (#149).

### D2 Spokes: manifold-native shapes now

Babel on an enrolled machine acts as an agent-kind principal with its own
scoped token (A2, `AXIOMS.md:41-51`; `Principal.kind: "agent"`,
`packages/protocol/src/principal.ts:7`; agent tokens never expire,
`docs/CONTRACTS.md:134-137`). Babel's per-machine processes, the hourly archive
push and the conductor loop, stay OS-supervised services exactly as
`docs/ENROLL.md:96-126` supervises `manifold-agent`; the plugin observes and
drives them through the action door and presence. A typed exec/job primitive on
the machine channel is filed as a manifold prerequisite so the OS-supervision
seam can later be replaced.

Consequences:

- The token is delivered per machine by the dotfiles ceremony beside the
  machine token, as a mode-0600 file (the `MANIFOLD_MACHINE_TOKEN_FILE` pattern,
  `docs/ENROLL.md:68-95`); Babel never mints it.
- Direction of traffic is spoke to hub. Each machine's babel calls `babel.*`
  doors to announce presence and publish projections (archive state, running
  work, receipts) and subscribes to the event plane for requests addressed to
  its machine. The plugin's server half never reaches into a spoke; it holds the
  request in `ctx.storage`, emits the event, and reads the answer the spoke
  posts. This is the shape A2 already permits and the exec primitive will later
  shorten.
- `internal/presence` (PostgreSQL advisory rows,
  `internal/presence/presence.go:33-36`) stops being the fleet's liveness
  signal: an agent principal's presence dies with its connection, which is the
  truer answer. What a machine is running rides the door, not presence.
- The conductor keeps its "never daemonize" rule
  (`internal/conductor/conductor.go:39-42`); it gains a door-fed intake for
  invitations and a door-fed report of parks and spends. The archive timer
  gains nothing but visibility: its last result is a projection.
- The hub runs a babel instance configured like any other machine: the master
  node's host is enrolled natively as a spoke (`docs/SELF-HOST.md:211-216`
  places real shells on the host, not in the container), holds its own storage
  document, and its engine service is the one the plugin's server half reads.
- The sandbox (N12) stays under Babel's supervised process and is never a
  descendant of a manifold PTY (`docs/ENROLL.md:157`).

Manifold prerequisites: "Typed exec/job primitive on the machine channel" (atyrode/manifold#156); "A
`machine` node kind in the manifold:// grammar so grants can scope per enrolled
machine" (atyrode/manifold#157). Babel work: "Agent-kind principal enrollment: babel holds a scoped
manifold token per machine and announces presence" (#143); "Door-fed conductor intake
and archive reporting" (#145); "Align Babel host identity with the manifold machine
name" (#144); "Sandbox threat model addendum: manifold's agent and PTYs are outside the
sandbox's supervision chain" (#150).

### D3 Storage placement is a plugin-level setting with two values

Revised 2026-09-02 after the review (SPEC.md decision 84, narrowing 63). A
Babel-plugin setting selects where durable state lives, and both values run
today over Babel's provider-neutral storage interfaces (`SPEC.md` §9):

- **master** — Babel's local mode (`storage.json` `mode: local`) on the hub's
  host: a single box, and named non-durable in the setting itself, because the
  hub's own backup is a hand-run archive of one named volume
  (`docs/SELF-HOST.md`) and an archive placed there has the durability of the
  thing it is meant to help recover;
- **external** — shared mode: the restic repository and the PostgreSQL catalog
  wherever the operator provisions them, today's Clever Cloud, the default for
  any fleet.

"One enrolled fleet machine hosts it" is not a third value. A store the
operator runs on a fleet machine — an S3-compatible service, a restic REST
server, a PostgreSQL — is external with that machine's endpoint, provisioned by
dotfiles under OS supervision as any other service is; installing it through
manifold waits on the exec primitive (D2, atyrode/manifold#156) and would still
resolve to an endpoint underneath.

Consequences:

- No manifold prerequisite stands between Babel and either placement. The
  setting is `storage.json`'s `mode`, delivered per machine by the ceremony as
  before; the plugin reads it and shows it.
- Rendering the setting in manifold's generic settings pane needs an enum kind
  and a workspace scope (boolean-only and per principal today:
  `packages/protocol/src/plugin.ts:104,128-133`; `docs/PLUGINS.md:711-723`).
  That is a UI prerequisite (atyrode/manifold#158), not a placement one; until
  it lands the pane shows the placement read-only with its source named.
- Plugin-owned tables and blobs on the master node (atyrode/manifold#159) are
  no longer on Babel's path: master placement is Babel's own local mode, not
  manifold-hosted storage. The issue stays filed as a general manifold need.
- Secrets stay outside manifold in both placements.

Manifold prerequisites: "Workspace-scoped plugin settings with an enum kind"
(atyrode/manifold#158), for the pane only. Babel work: "Storage placement setting: master (local mode, non-durable) or external (shared mode)" (#146).

### D4 Runs console: full plugin-rendered UI

No terminal fallback and no poll-grade compromise as the end state. A
plugin-declarable continuous stream channel is a manifold prerequisite; until it
exists the interim is milestone events plus polled state, labelled interim.
`docs/runs-interface.md` owns the design.

Consequences:

- The interim rides what exists: `babel.*` actions emit milestone events at run
  boundaries (emission is possible only inside a dispatch,
  `packages/server/src/plugin-host.ts:1207-1210`), the browser re-reads state
  on each event through `usePolledResource`, and polling at 2 s occurs only
  while the socket is down (`packages/plugin/src/polled-resource.ts:77`).
- The console is subject to D1: an isolated web half paints through the host
  vocabulary, so a live log tail or a progress strip is a component the
  vocabulary must offer.
- Babel's stated refusal to start a run from the web
  (`internal/web/analysis.go`, `exploreRefusal`) is retired by the run
  projection API and the door; the "runs in flight" listing it declined to hold
  becomes plugin state fed by presence and milestones.

Manifold prerequisite: "Plugin-declarable continuous stream channel on the
session socket" (atyrode/manifold#169) (owned by the runs document). Babel work: "Host-independent run
projection API: list, detail, create, cancel" (#151); "Run milestone events: a
declared, bounded vocabulary emitted at run boundaries" (#152); "Interim runs console
in the React shell over the projection API" (#153); "Runs console as manifold plugin
surfaces" (#154).

---

## 5. Transition steps

Each step names its manifold prerequisites and Babel work by draft issue
title, its proof (what must be observed to call it done), and what it retires.
Manifold prerequisites of a step run in parallel with Babel's host-independent
work of the same step; a step is done only when both halves are.

### Step 0. Today

Babel is a Go binary with an embedded React shell served on loopback by `babel
web` (`internal/web/server.go:306-317`; `SPEC.md:146-148`), 16 pages and 40 API
routes (`.omp/research/manifold-babelsurface.md` §1), an hourly archive push
scheduled by dotfiles, a foreground conductor loop, PostgreSQL presence rows,
and a fleet known only through what has archived. Manifold has no loader for
out-of-tree code, no exec verb, no stream channel, boolean settings, and
`plugin_kv` as its only plugin storage.

### Step 1. Loading, and the engine API

Manifold: "Schedule ADR 0016 stage 1: the isolation runner, so an out-of-tree
plugin can load" (atyrode/manifold#151); "Dynamic plugin loading from a pinned local artifact: evaluate
entry, add install and uninstall doors" (atyrode/manifold#152); "Manifest declaration of an external
host-binary dependency" (atyrode/manifold#153); "Plugin-declared capability names beyond the closed
nine-member enum" (atyrode/manifold#154); "Companion-service seam for an isolated plugin's server
half" (atyrode/manifold#155).

Babel, in parallel and host-independent: "Babel plugin package and manifest in
atyrode/babel" (#140) (the package skeleton under `plugin/`, manifests as data,
`babel.<x>` ids, the capability ceiling); "Action-door API over the babel
engine: the babel.* action set and the headless engine service behind it" (#141) (the
engine service serves the engine API over a Unix socket; during the transition
`babel web` mounts the same engine API for the shell, so a route added for the
shell is an engine-API endpoint or it is not added; `internal/web`'s Go services
are reused, its React serving is not); "One untrusted-content sanitizer for
every plugin-rendered Babel surface" (#142); "Host-independent run projection API:
list, detail, create, cancel" (#151) (runs).

Also host-independent, and not a manifold step at all: "Recall: the data lake as
hot-ready context for any agent, in any harness, on any machine" (#156) (SPEC.md
§4.10). Recall is the worker's own retrieval and selective fetch offered through a
second door — the CLI, and later one `babel.*` action for agents that are
manifold principals — so it rides on #141's action-door API without a second
implementation and needs nothing from manifold to exist. Its exposure to agents
is dotfiles' (atyrode/dotfiles#505), not manifold's.

Proof: a development manifold loads the Babel plugin artifact from
`atyrode/babel` by hash; `GET /api/plugins` lists `babel.<x>` rows with `source:
"plugin"` and an install grant; one `babel.*` action dispatched from the
inspector's door form reaches the hub's engine service and returns a projection;
the trace row exists. Babel's shell keeps working unchanged.

Retired: nothing. From this step on `internal/web` gains no route that is not
an engine-API endpoint.

### Step 2. The fleet as enrolled machines

Manifold, in parallel, not blocking: "Typed exec/job primitive on the machine
channel" (atyrode/manifold#156); "A `machine` node kind in the manifold:// grammar so grants can scope
per enrolled machine" (atyrode/manifold#157).

Babel: "Agent-kind principal enrollment: babel holds a scoped manifold token per
machine and announces presence" (#143); "Align Babel host identity with the manifold
machine name" (#144); "Door-fed conductor intake and archive reporting" (#145). Deployment
(dotfiles): the ceremony delivers the agent token beside the machine token; the
master node's host enrolls its own babel.

Proof: every fleet machine appears in manifold as a machine (online state from
`core.machines`) and as a babel agent principal (presence); an archive push
requested from the plugin lands on the named spoke and its result is a
projection the plugin shows with the trace row that requested it; a machine
whose agent principal is offline is shown as such without any Babel presence
row being read.

Retired: `internal/presence` as the liveness signal and `GET
/api/fleet/presence` as its reader; `archive fleet --expect` as the way to name
an absent machine.

### Step 3. Storage placement

Manifold: "Workspace-scoped plugin settings with an enum kind"
(atyrode/manifold#158), for rendering the setting only.

Babel: "Storage placement setting: master (local mode, non-durable) or external (shared mode)" (#146).

Proof: every machine's engine service reports the deployment's placement and
its source (`storage.json`); external round-trips against the real stores;
master runs on one box and the surface names it non-durable wherever it is
shown; the pane, once atyrode/manifold#158 lands, edits the value and the
ceremony's document is what changes underneath.

Retired: nothing; the ceremony continues.

### Step 4. The runs console

Manifold: "Plugin-declarable continuous stream channel on the session socket" (atyrode/manifold#169).

Babel: "Run milestone events: a declared, bounded vocabulary emitted at run
boundaries" (#152); "Interim runs console in the React shell over the projection API" (#153)
(labelled interim); "Runs console as manifold plugin surfaces" (#154).

Proof: a run created from the plugin shows its milestones as events on the
plugin's node and its state through the projection with no timer armed while
the socket is up (`verify:budgets` discipline, `REGISTRY.md:1974-1990`); once
the stream channel lands, progress frames arrive sub-second and the interim
label is removed.

Retired: `exploreRefusal` in `internal/web/analysis.go`.

### Step 5. Every Babel area as plugin surfaces

Manifold: "Size the isolated-plugin component vocabulary against a real plugin
surface inventory" (atyrode/manifold#160); general improvement "Promote door-form rendering out of
core.debug into @manifold/plugin/ui" (atyrode/manifold#168).

Babel: "Babel surfaces as plugin contributions: sections, disciplines and
routes" (#147), covering Sessions, Archive, Explore/Runs, Hypotheses and Findings,
Reality, Cookbook, Review and complaints, Help, Fleet; every page in
`.omp/research/manifold-babelsurface.md` §1.1 has a named plugin equivalent.

Proof: every operator task the React shell supports is performed in manifold
through the plugin, by an operator, with no `babel web` running; the plugin
declares no route the sanitizer does not guard; an idle Babel workspace holds no
armed timer.

Retired: nothing yet; the shell stays until this proof is recorded.

### Step 6. Retire the web shell

Babel: "Retire internal/web and web/: the plugin is the only UI" (#148). The `babel
web` verb, the bootstrap nonce and cookie, the loopback `Host` check and the
embedded React build go; the engine service stays, headless.

Proof: `web/` and `internal/web` are deleted; `babel web` is a rejected
invocation; every former API route is either a `babel.*` door or an engine-API
endpoint the plugin reads; the runbook's recovery procedures still run with
manifold down.

What stays CLI-only, and why. Recovery must not depend circularly on Babel or
PostgreSQL (`SPEC.md:83`), and from this step on it must not depend on manifold
either; so every verb keeps a headless form (`sessions fetch`, `archive push`,
`archive verify` and the review verbs keep theirs beside their doors, as the
recovery path), and the following never get a door:

- `storage configure --from-json -`, `storage migrate`: the ceremony pipes a
  credential-bearing document over stdin and the migration credential is held
  ephemerally on one machine (`SPEC.md:79-81,543`); manifold holds no secret and
  a door body would be a trace row.
- `storage status`, `storage verify`, `storage rebuild --host ID`: they read or
  rebuild one machine's own configuration and catalog rows and must answer when
  nothing else does; the plugin shows the same facts as projections.
- `archive init`, `archive unlock`: one-time or operator-typed verbs that touch
  restic's own coordination state (`SPEC.md:73`).
- `sessions prune --local`: disk on one machine, a recovery-side action.
- `analysis profile configure`, `titles configure`: hand the terminal to Code
  (`internal/cli/analysis.go`); interactive by construction.
- `conformance WORKER`: a developer test suite.
- `reality import --source ID`: a trusted-source batch applied by hand.
- bare `babel` and `version`: the offline status overview that never opens
  the repository (`SPEC.md:506`), needed exactly when nothing else answers.

### Cross-cutting manifold improvements

Not prerequisites, but each was found on the line Babel's work targets and
each has a strong evidenced case; drafted as general improvements in
`.omp/research/manifold-issues.md`: "No CI workflow runs on pull requests into
dev" (atyrode/manifold#161); "Dockerfile builds from floating oven/bun:1 against ADR 0001's exact
1.3.13 pin" (atyrode/manifold#162); "main is pinned at v0.4.4 while v0.5.0 was tagged from a dev
commit" (atyrode/manifold#163); "freeze is declared in PLAN.md but absent as a verb" (atyrode/manifold#164); "Widen the trace
and log redaction field-name rule to password, credential and passphrase" (atyrode/manifold#165);
"Hand an action handler its trace id" (atyrode/manifold#166); "Event-plane delivery to a session
socket has no backpressure guard" (atyrode/manifold#167); "Promote door-form rendering out of
core.debug into @manifold/plugin/ui" (atyrode/manifold#168).

---

## 6. Gotchas

Every hard ceiling or rule an implementer will hit, in the order they bite.

- **Id pattern.** `id` must match `^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$`, at
  most 64 characters; `babel` alone is invalid, `babel.<x>` is required
  (`packages/protocol/src/plugin.ts:22-23`). `core.*` is refused unless the id
  is in `SHIPPED_PLUGIN_IDS`; `engine.*` is never claimable.
- **Closed nine-member capability enum.** `capabilities` and every action's
  `caps` must be drawn from `CAPS` (`packages/protocol/src/capabilities.ts:11-24`);
  a string outside it fails manifest parsing. Until the vocabulary issue lands,
  a Babel action must borrow a semantically nearest cap or use none.
- **1 MiB action bodies.** `MAX_HTTP_BODY_BYTES = 1_048_576`
  (`packages/server/src/http.ts:36`); session frames share the ceiling. Transcript
  pages and record payloads must be paginated below it.
- **64 KiB storage values.** `MAX_STORAGE_VALUE_BYTES = 64 * 1024`
  (`packages/plugin/src/storage.ts:74`); keys are at most 128 characters; one key
  per session or record, never one key per listing.
- **Boolean-only, per-principal settings.** `SETTING_KINDS = ["boolean"]`, at
  most eight per plugin, the value follows the reader
  (`packages/protocol/src/plugin.ts:104,128-133`; `docs/PLUGINS.md:711-723`).
- **No plugin HTTP routes.** `contributes.routes` claims a browser path
  segment only; the server route table is a gate-enforced allowlist (S7,
  `REGISTRY.md` §Gates). Nothing large is served as one response.
- **2 s lifecycle hooks, no boot hook.** `LIFECYCLE_TIMEOUT_MS = 2_000`
  (`packages/plugin/src/lifecycle.ts:21`); disable always completes; at boot
  everything enabled is simply live.
- **PTY-only agent.** Seven verbs each way, `create` carries no command
  (`packages/protocol/src/machine.ts:37-93`; `packages/agent/src/terminal.ts:115-124`);
  an agent restart kills every PTY it owns (`docs/ENROLL.md:157`). Nothing of
  Babel's runs inside a manifold PTY.
- **Emission only inside a dispatch.** `ctx.emit` is staged and flushes on a
  handler's `ok` (`packages/server/src/plugin-host.ts:1207-1210`); a kind not in
  `contributes.events` is refused. Payloads are flat, at most 16 scalar keys.
- **No CI on PRs into `dev`.** `.github/workflows/` holds `main-guard.yml` and
  `release.yml` only; `bun run gate` green before push is contributor
  discipline (`AGENTS.md`). A Babel-authored manifold PR runs the gate locally.
- **Invariant 8 dependency ADRs.** In manifold's tree every new runtime
  dependency needs a dated `docs/decisions/` entry (`AGENTS.md:116-117`), with
  no mechanical gate; the same applies to a hand-rolled pattern that a named
  library covers (D14).
- **`cssFamilies` and one stylesheet.** A plugin ships one `src/styles.css`
  (`docs/PLUGINS.md:78-98`); every selector family has one owner, checked by
  S13 (`REGISTRY.md:1369,2095`); a classless rule outside the floor sheet is
  RED. Under isolation the plugin paints no CSS at all.
- **No sanitization in manifold.** No sanitizer, no `DOMPurify`, no
  `dangerouslySetInnerHTML` anywhere under `packages/`; user text passes through
  React's escaping. Babel's transcripts are hostile input and Babel brings the
  sanitizer.
- **The Budgets register.** An idle workspace asks nothing: a backgrounded
  tab has a ceiling of zero, a timer beside a live subscription is RED, an
  undeclared door polled at idle is RED on sight (`REGISTRY.md:1974-1990`).
  There is no server-half row; ADR 0016 T5 owes one at stage 1.
- **Redaction is by field name.** `SECRET_FIELD = /(token|key|authorization|secret)/i`
  (`packages/server/src/log.ts:36`) does not match `password`, `credential` or
  `passphrase`. No `babel.*` action accepts such an argument.
- **Protocol lanes.** `PROTOCOL_VERSION = 21`; the machine channel accepts
  `{16..21}`; the session channel has no compatibility set
  (`packages/protocol/src/version.ts:2,216-218`). A spoke ahead of the hub is
  refused silently from the spoke's side (`AGENTS.md:126-144`).
- **Trace id is not handed to the handler.** `traceId` is minted at
  `packages/server/src/plugin-host.ts:1172` and absent from `ActionCtx`; a
  receipt cannot cite its trace row directly.
- **No plugin-defined node kinds.** The `manifold://` grammar has seven
  kinds and a plugin cannot add one (`packages/protocol/src/uri.ts:33-45`), so
  a Babel run, session or record has no address of its own: it is reached
  through a plugin route path or a discipline container, and a grant can scope
  to the plugin, an action or a container, never to one run. The runs document
  names this gap; the machine node issue is its first instance.
