# Operations runbook: archive, custody, recovery, drains

Babel is a Manifold plugin family under `atyrode.babel/`. There is no `babel` binary, no
local configuration document any Babel program reads, no PostgreSQL catalog and no publication
step. What an operator still owns is the restic repository the archive lives in, the secrets that
open it, the machines the jobs run on, and the hub the store lives in. This document is those
four things.

Two conventions, and they are the reason this file is worth reading rather than guessing from:

- **OPERATOR STEP** marks a procedure a person performs. It carries prerequisites and an
  observable success. Nothing marked so is automated, and several of them have never been run —
  each says which.
- Captured output is dated and names its host. It is evidence that something once happened on a
  named machine, not a claim about the fleet today.

`docs/parity.md` is the companion: it records, per retired capability, whether the plugin has it.
Where this runbook says *nothing does this*, that document says the same in a row with an issue.

---

## 1. Backing up: the `archive` operation

The archive is `atyrode.babel.archive`, one of the three operations the machine half implements
(`atyrode.babel/machine/main.ts`; `MACHINE_OPERATIONS` in
`atyrode.babel/contract.ts`). It runs `restic backup` over this machine's session roots —
OMP, Codex, Claude Code, and Babel's own — **one snapshot per root**, tagged `babel`, attributed
to the machine's own id as restic's `--host` (`atyrode.babel/machine/archive.ts`). Per-root
snapshots keep each root's parent chain stable when a machine gains a harness, let one unreadable
root fail alone, and make restoring one harness a restore of one snapshot.

What it writes back into the store is the one fact a backup learns and nothing else can: which
snapshot holds each session, and when (`sessions.snapshot_id`, `store/schema.ts`).

**It never creates a repository.** A repository is created once, by hand, for the deployment. A
silent creation turns a mistyped locator into a second empty archive that grows happily while the
real one appears to stop, and two concurrent creations corrupt a fresh one.

**Nothing in Babel schedules it.** The conductor registers exactly one schedule, and it is the
`scan` beat (§7). No door posts an `archive` job, no preset names one, and the hourly systemd timer
died with the Go binary it wrapped. Today an archive runs when the hub's owner posts the job for
that operation on that machine, or registers a cadence for it with the hub's own schedule verbs.
That gap is real: a deployment that posts nothing archives nothing, and no surface says so.

> **OPERATOR STEP — post an archive (prerequisites in §4 and §6).**
> **Prerequisites:** the machine is enrolled; the `atyrode.babel.restic` service policy is
> installed on it and its binding fingerprint matches the installed job (§4); the `restic` runtime
> tool is bound on that machine; the `home` anchor's roots exist; consent is recorded for
> `services:invoke` at
> `manifold://machine/<machine>/service/atyrode.babel.restic/operation/storage` and for
> `network:host` at `manifold://machine/<machine>/operation/atyrode.babel.archive`.
> **Success:** the job settles `completed` and its receipt carries `kind: "archive"`, a non-zero
> `roots` count and one snapshot id per root; the `sessions` rows for that machine carry the new
> `snapshot_id` and `archived_at`. An `archive` that reports zero roots is not a backup — it is a
> machine whose session directories do not exist (§6).

A machine that binds no `restic` runs `scan` and `prepare` unchanged: requirements are
per-operation, so a missing tool disables `archive` alone (`docs/building.md`).

---

## 2. Recovery: restic, directly

**Babel fills the archive and cannot read it back.** `machine/restic.ts` runs `init`, `backup` and
`snapshots`, and nothing else — no `check`, no `ls`, no `dump`, no `restore`
(`docs/parity.md`, `.omp/skills/babel-cli/SKILL.md`). Verifying and restoring are therefore
`restic` commands the operator types, which is also the property the old runbook cared most about:
*archive recovery does not depend on the catalog — or on Babel.* It never did, and now it cannot.

### 2.1 Find what to restore

`sessions` in the store is the index: a row per session with its harness, selector, host and the
snapshot that holds it. Read it from Babel's own surfaces, or ask restic directly, which needs
nothing of Babel's at all:

```sh
restic snapshots --tag babel                 # what this deployment has archived, by host
restic ls <snapshot-id>                      # what one snapshot holds
```

### 2.2 Restore

```sh
restic restore <snapshot-id> --target /tmp/restore-scratch --include '<path-inside-the-snapshot>'
```

Restores are byte-exact and idempotent: restoring the same snapshot to the same target a second
time reproduces the same bytes.

**Exercised 2026-08-31 on `workstation-linux`**, against the real repository: a session belonging
to a *different* machine (`alex-x86_64-linux-wsl`) — one this host had never held locally — was
restored from snapshot `3dd67096` with `restic` alone, no catalog consulted, and matched the
independently restored copy byte for byte:

```
368fd244cb26f7e6bfed99d356bec06f8a0651a7f616902e230564a30643c81b  ...01a020ee-....jsonl
65f0eb680f118f82172979fa6bb7432ff40423867fc9958305911824d693a421  ...01a020ee-.../__advisor.jsonl
```

Repository scale on that date, for restore planning: 44 snapshots, 50,631 blobs, 23.466 GiB
uncompressed, 5.687 GiB stored, compression 4.13x.

### 2.3 Integrity, and the stale lock an operator will actually meet

```sh
restic check --no-lock          # structural check, mutates nothing, takes no lock
restic check --read-data        # re-reads and re-hashes every pack; a full download
```

Reach for `--no-lock` first: it diagnoses without mutating, and it is the only form that works
while the repository holds a lock.

**Observed 2026-08-31 on `workstation-linux`:** `restic check` failed with `repository is already
locked by PID 2841104 on ubuntu-4gb-nbg1-1`; two *shared* locks were stranded in the repository
from processes that no longer existed. A stale shared lock endangers no data and blocks no
restore — both restores above succeeded while they were present — it blocks `check`, which wants
the repository quiescent. `restic check --no-lock` then reported 44 snapshots and no errors.

> **OPERATOR STEP — clear a stale lock (never automated, never an agent's to run).**
> **Prerequisites:** the lock's holder is confirmed dead, by host as well as PID; a lock naming
> another host is never judged by local PID liveness. Nothing in Babel takes, inspects or removes
> a repository lock, and `.omp/skills/babel-cli/SKILL.md` forbids an agent from running
> `restic unlock` at all.
> **Success:** `restic list locks --no-lock` shows the lock gone and `restic check --no-lock`
> exits 0. Measured against restic 0.19.1, plain `restic unlock` removes a stale lock including an
> exclusive one; `--remove-all` is what a lock that is *not* stale needs, and it removes every lock
> in the repository.
>
> **Never run `restic forget`, `restic prune` or `restic repair`.** Retention is append-only and no
> code path in the plugin deletes a snapshot. Removing coordination state is not removing data;
> removing data is not a recovery step.

The lock ids observed in 2026-08-31's drill are historical. Reassess ownership and liveness before
touching any lock today.

---

## 3. Repository password custody

**No provider can reissue this password.** Losing every copy makes the existing repository
unreadable for ever. A working machine is not a backup of it, and a new password cannot open the
old repository.

Custody is `atyrode/dotfiles`' and always has been: Babel never creates, prints, stores or rotates
a credential. The declared sources below were read at dotfiles revision
`f2a4749eab77ac859b85354142e42c78ac6d8c80` (2026-09-06). They establish declared behaviour, not
proof that any machine has applied it:

| Contract | Pinned source |
| --- | --- |
| Shared custody; an existing password is prompted, never minted; existing rings survive rotation | [modules/shared/babel-archive.nix:1–31](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L1-L31) |
| sops placement, owner/group and 0600 mode | [modules/shared/babel-archive.nix:44–75](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L44-L75) |
| Shared `babel-custody`: secret undeployed inputs, a deployed complete ring, prompt validation | [modules/shared/babel-archive.nix:77–178](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L77-L178) |
| Per-machine derived configuration and registry identity | [modules/shared/babel-archive.nix:180–243](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L180-L243) |
| Operator-device generation followed by apply; no vault or provider session on the target | [fleet/provisioning.json:39–44](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/fleet/provisioning.json#L39-L44) |

`babel-custody` holds `repository-password`, the provider environment inputs and
`payload-keys.json` (§8). The password and the object-store credential are the two values the
deployment's storage document is built from; the plugin reaches that document through a service
(§4) and never through a file.

> **OPERATOR STEP — preserve and recover custody (not executed).**
> **Prerequisites:** an authorized operator device able to decrypt the clan vars, access to the
> encrypted dotfiles history, and an independent secure backup destination. Preserve the encrypted
> vars and the means to decrypt them off the fleet; test access from a recovery device without
> printing secret values. If custody is missing, recover the existing password and complete ring
> from a surviving authorized copy before generating anything derived. Never replace a password or
> a ring to repair missing placement.
> **Success:** the recovery device recovers the existing custody values, including every historical
> key id, without relying on the failed host. Repository recovery then needs nothing but restic
> (§2).

**Historical evidence, 2026-08-31 on `workstation-linux`:** the then-current password file and
storage document were observed with mode 0600 and the password reported present and secure. No
secret value was retrieved or exported. Those observations describe the retired deployment's
placement, not today's custody.

---

## 4. The storage document, and how a job reaches it

The `archive` operation reads the repository and the secrets that open it from **one Manifold
service**, `atyrode.babel.restic`, and from nowhere else (`atyrode.babel/machine/restic.ts`;
`docs/building.md` owns the install shape). The document it answers with is

```
{ "repository": "...", "password": "...", "accessKeyId": "...", "secretAccessKey": "..." }
```

— the object-store pair required for an `s3:` locator and refused in halves, so a half-installed
policy fails as itself rather than as an unexplained restic exit.

Four properties of that path are the security contract, and each is enforced in code rather than
by procedure:

- The engine opens a loopback proxy for the job, mints a capability for that job alone and writes
  the endpoint into the job's own input file. The job never holds the operator's upstream
  credential; the bearer it carries is the owner's per-job capability.
- The password reaches restic as `RESTIC_PASSWORD` in the **child's** environment only: never argv,
  never this process's environment, never a receipt, never a log.
- Every child gets a minimal environment — the repository coordinates, the object-store credential
  when there is one, and `HOME`/`PATH`/`TMPDIR`. Ambient `RESTIC_*` variables cannot redirect an
  archive.
- The one value the manifest fixes is `BABEL_RESTIC_CACHE_DIR=/home/job/.cache/restic`, inside the
  managed cache location the operation may write. Without an index cache every backup re-reads
  every byte it already archived.

> **OPERATOR STEP — install the storage service on a machine (per machine, by the hub's owner).**
> **Prerequisites:** the deployment's storage document is served by the operator's own store over
> HTTPS, and that store's token is placed on the machine as a service credential
> (`serviceCredentials.babel-restic`, sourced from a file the machine holds — the nix shape is in
> `docs/building.md`). The store is the operator's; Babel ships none and generates none.
> **Procedure:** one owner call of `engine.services.configureConfiguration` naming
> `serviceId: "atyrode.babel.restic"`, `revision: "1"`, the origin, the credential ref and the
> single `storage` operation, with `expectedRevision` set to what `readConfiguration` last reported
> (`null` for a machine with no configuration). Then install the job with
> `resourceBindings.services["atyrode.babel.restic"]` carrying the fingerprint
> `engine.jobs.describe` reports for that policy.
> **Success:** `engine.services.readConfiguration` reports the policy at the expected revision, and
> an `archive` job admits rather than refusing `service_binding_mismatch`. A policy the operator
> changed is a new installation, never a silent upgrade — the mismatch is the point.

**Nothing on the machine is a Babel configuration file any more.** No path under `~/.config` is
read by any Babel program; `docs/parity.md` records the retired configuration package as absent by
decision. What dotfiles places on a managed machine is custody (§3) and the service credential
above, and the operator reads a value out of custody only when he is about to use `restic` by hand
(§2) — never by printing it.

---

## 5. Backing up the store itself

The hub holds Babel's one durable store: a SQLite file at
`<data>/atyrode.babel/data.db`, created by the enable hook and deleted by a purge
(`docs/building.md`). Records, edges, rulings, assessments, filings, the ledger and every receipt
live there and nowhere else. There is no second copy, no publication and nothing to reconcile:
`docs/parity.md` records publication, the shared catalog and the object store as absent by decision.

Two consequences an operator must hold at once:

- **The sessions are safe without it.** Every archived session is restorable from restic with the
  password alone (§2), and the catalog rows that point at snapshots are rederivable by re-running
  `scan` and `archive` on each machine.
- **Babel's own analysis is not.** A hypothesis, a finding, a ruling or a receipt exists in
  `data.db` and nowhere else. **Nothing in Babel backs that file up**, and no procedure here
  invents one: backing up the hub's data directory is the hub's own operational question, and
  until it is answered the honest statement is that losing the hub's volume loses every record
  Babel has produced or imported.

---

## 6. Enrolling a machine

A machine runs Babel's jobs when four things are true of it. None is a Babel command; all four are
the hub owner's or the machine's declaration.

1. **It is enrolled in the hub** and online.
2. **It binds the tools each operation names** (`docs/building.md`): `bun` is pinned by Babel's own
   bundle and needs no binding; `development` carries `git`; `system` is the reviewed native
   closure the pinned bun is dynamically linked against; `restic` is the owner's, by name, and only
   `archive` asks for it.
3. **Its anchors exist.** The `home` anchor needs `~/.omp/agent/sessions`, `~/.codex` and
   `~/.claude` to exist — a job whose read location is missing fails to start, and `mkdir -p` is the
   whole fix — and the `runtime` anchor must be a dedicated bounded tmpfs, since the named-output
   lease is cut from it.
4. **Its consents are recorded** at the nodes §1 names, plus `machines:run` at the operation node
   for anything that launches.

> **OPERATOR STEP — join a machine (not executed against the current fleet).**
> **Prerequisites:** items 1–4 above, and §4's service install if this machine is to archive.
> **Success:** a `scan` job settles and its sessions appear in the store under that machine's id;
> then an `archive` job settles with a snapshot per root. A successful `scan` with no roots is not
> proof of anything but an empty machine.

**A machine id is not a host name.** Every row Babel keys on a machine — `sessions.host`,
`runs.machine_id` — holds the id `core.machines.list` publishes, and the hub resolves no names. The
imported Go-era corpus carries the old deployment's host name instead, which is what the
`rehostSessions` door exists to repair: it rewrites one `host` value to one machine id the hub has
just described (`atyrode.babel/contract.ts`).

---

## 7. Cadence, stopping and rollback

### 7.1 The one schedule

`engine.jobs.schedule` schedules a job on a machine, so the loop's beat is the cheapest useful job
Babel owns: `scan` (`BEAT_OPERATION`, `atyrode.babel/server/conductor.ts`). It spends no
model money, refreshes the catalog every cadence, and its settlement is what wakes the hub — a
plugin has no clock of its own and may not poll as an alternate scheduler.

The beat is registered only while the policy is enabled, at the policy's `cadenceSeconds`, on the
machine the policy names, for a bounded lifetime the loop renews. A policy change re-registers it,
because the schedule's revision is the policy version.

Drawing, launching and spending stay inside the cycle, where the coordinator's lanes and ceilings
govern every dollar. A fixed `explore` registered at schedule time would spend outside them, which
is why none exists.

### 7.2 Stopping

> **OPERATOR STEP — stop the loop.** Set the policy's `enabled` to false through the `setPolicy`
> door. A disabled policy registers nothing, draws nothing, ingests nothing and spends nothing.
> **Success:** the beat's schedule is gone and Watch reports the loop as off. Turning Babel off is
> one recorded operator decision, never a migration and never a file edit.

Stopping a *drain* is §11.5, and it is a different act: the policy is untouched and the jobs in
flight must be cancelled.

### 7.3 Rollback

Rollback is an install, not a switch: the hub installs a bundle by hash
(`engine.plugins.install`), so going back is installing the previous bundle's hash. A disable
retains `data.db`, an uninstall refuses while the plugin holds pages, and a **purge deletes
`data.db`** with its `-wal` and `-shm` — which is the one irreversible act in this document, since
§5 has no backup to restore from.

There is no legacy backup path to fall back to. Babel replaced an rclone-crypt mirror of the same
trees and that mirror is retired; the restic repository is the only archive. Rolling a bundle back
never touches the repository, the snapshots or the custody in §3.

---

## 8. Keys, secrets, and what no longer publishes

**The plugin seals nothing.** Records are plaintext rows on the operator's own hub
(`store/schema.ts`), nothing is synced and nothing is published, so no key is needed to read
Babel's analysis and none is held (`docs/parity.md` records sealing, the object store and
publication as absent by decision).

The `babel-custody` ring — `payload-keys.json` — is therefore read by nothing Babel runs today. It
stays in custody for one reason, and it is sufficient: **it is the only thing that opens the
ciphertext the retired deployment published.** Losing every copy leaves those objects permanently
unreadable. A new active key cannot open an object sealed under an old one.

> **OPERATOR STEP — preserve the existing ring (not executed).**
> **Prerequisites:** an authorized operator device and access to encrypted clan custody. Reconcile
> the complete key history: preserve every key, refuse conflicting material under the same id, and
> resolve a conflict from authoritative backups. Never print the ring or put it in argv, shell
> history, logs or an ordinary temporary file; use clan's var get/set workflow rather than
> regeneration, and commit only the encrypted update.
> **Success:** custody holds the whole append-only history. There is nothing to verify it against
> on the machine, because no Babel program opens a sealed object any more.
>
> **Rotation is not a repair.** The generator's empty-prompt branch mints a new ring and cannot
> recover old ciphertext; it is for a genuinely new deployment that has sealed nothing. Missing
> placement never justifies minting.

**The rule that governs every secret in this document**, and the reason §§3–4 and §8 exist as one
procedure: a secret reaches a program through a channel that cannot be observed — a service
capability, a child process's environment, a mode-0600 file read by the thing that needs it — and
never through argv, a log line, an error string, shell history or an ordinary temporary file. The
plugin holds this by construction (§4). An operator using `restic` by hand holds it by discipline:
`RESTIC_PASSWORD_FILE`, never `--password-command` with the value inline, and never `echo`.

---

## 9. Reading what Babel holds

There is one hub, so there is one place to read: the panels. `atyrode.babel.feed` serves Home (every
record, ranked by what needs the operator), the peeled record and a topic with its filings and his
interest; `atyrode.babel.watch` serves what is running, what will run and what a drain is spending
(`docs/building.md`). Behind them are the read doors — `feed`, `record`, `thread`, `topics`,
`topic`, `pulse`, `runs`, `run`, `policy` — spelled in `atyrode.babel/contract.ts`.

Three things that used to be commands are now properties of having one hub:

- There is no fleet read: every machine's work lands in the same store as it settles.
- There is no pending state to chase: a record is durable when the job that produced it is ingested,
  and ingestion is idempotent, so a retried cycle writes the same rows.
- An empty answer is an answer. A deployment that has explored nothing shows nothing, and that is
  not distinguished from a malfunction because there is nothing to distinguish.

---

## 10. Turning evaluation on

Evaluation is off until one operator decision turns it on: `enabled` defaults to false in the
policy schema (`atyrode.babel/store/coordinator.ts`), which is the activation gate
expressed as a value rather than as a migration.

The policy is one document, written through the `setPolicy` door, and it carries both the
authorization and the route:

| Field | What it decides |
| --- | --- |
| `enabled` | whether the loop exists at all |
| `cadenceSeconds` | the beat's period (§7.1) |
| `batchSize`, `concurrentPerMachine`, `leaseSeconds` | how many assignments may be claimed at once, per machine, and for how long |
| `perCycleCost`, `dailyCost` | the ceilings a cycle and a day may spend |
| `coverageShare`, `explorationShare`, `discoveryShare`, `filingShare`, `backlogShare` | the protected allocations across lanes |
| `review.machineId`, `review.profile` | where a drawn review runs and which saved Code profile it is posted on |
| `review.roleRecipes`, `review.recipes` | which reviewed method each role uses, with the recipe bodies carried in the versioned policy so a later edit cannot change an in-flight assignment |

A policy that cannot be honoured is refused with the sentence saying why: an unversioned policy
could never be replayed against, a zero exploration or discovery share removes a protected
allocation, shares over one over-commit a cycle, and a daily ceiling below one cycle's makes the
per-cycle bound decorative. The recipe bodies come from `atyrode.babel/store/recipes.seed.json`,
produced by `atyrode.babel/tools/seed-recipes.ts`.

`setBudget` and `clearBudget` are the bounded exception: an overlay with its own TTL and a required
reason, which never edits the standing policy — because assignment ids are derived from the policy
version, and editing the standing policy mid-flight mints new ids for subjects already claimed.

> **OPERATOR STEP — enable evaluation (not executed against the live hub).**
> **Prerequisites:** a saved Code profile exists for the account and model the reviews will spend;
> `atyrode.code` and `atyrode.omp` are installed and consented at the revisions in force; the
> machine named by `review.machineId` is enrolled and online. Start with a batch and a per-machine
> concurrency of one and retain the standing lease bounds.
> **Success:** the beat registers, the conductor draws on its cadence, and the record's reception
> shows an assessment carrying the model that answered and what it cost.
>
> **What is unproven, and it is verification rather than a defect:** the drawn lane dispatches —
> `dispatchReviews` in `atyrode.babel/server/conductor.ts` draws, claims under a fence,
> blinds the projection, posts the review as a Code session and binds the claim to Code's job id
> — but no drawn review has ever run against a real hub. The evidence is synthetic: a conductor
> regression over the real SQLite store with a simulated Code receipt, 2026-09-16. The first live
> one is owed, and §11.7 lists it in order. The `launch` door's refusal of a direct drawn launch
> (`draw_managed`) is deliberate and not part of that gap: an operator-picked record must not
> bypass the shared claim, the reserved lanes and the budget.

Nothing here erases anything: assessments are append-only and a retired policy version stays
readable as the contract its assessments were formed under.

---

## 11. Draining a usage window

**Nothing in this section has been run on a real hub.** It was written after the 2026-09-13 drain
(`docs/postmortem-2026-09-13-drain.md`), which recorded 50 reviews in two hours and fourteen
minutes and moved the target account's 7-day window by zero percent. Every procedure below is an
**OPERATOR STEP** until a rehearsal on a real hub records host, date and observed output here.
§11.7 says exactly what shipped and what was exercised.

### 11.1 What a drain is

A drain spends a chosen account's remaining usage window, before its reset, on Babel's own work. It
is measured in the tokens and dollars the hub metered on that account — `usage.inference` on every
settled job, `inference_call` while one runs — never in the provider's percentage, which lags by
minutes and moves in whole points.

What the window is spent on is a weighted list over Babel's activities: the presets Watch offers
(`read-whats-new`, `explore-topic`, `keep-going`; a drawn preset is not fannable, see below), with
three rules. Weights are over metered cost, never run count. The controller schedules by deficit, so
a preset with no eligible work yields its slot and reports a gap rather than idling. And the
operator names the allocation when the drain starts, or is asked (rule 8).

On 2026-09-13 the allocation was everything on `review-backlog`, and that was a sound *choice*:
reviews are the mass-produced unit of Babel's self-maintenance, duplicate assessments are reception
data rather than waste, and a review can be as heavy as its profile makes it. It failed because each
review re-prepared the whole corpus before its first model call (post-mortem F1, O1). What must hold
for any allocation: a run never re-prepares the corpus, and the fan is sized to the measured cost of
one run.

Two bounds are structural rather than advisory. **A drawn preset cannot be fanned out**: the
coordinator arbitrates a draw under a claim and a fence, and fanning it would be a second
implementation of that arbitration — so `drainStart` takes the directly-launched presets only
(`atyrode.babel/server/drain.ts`). And **a drain without a target and a deadline is not a
drain; it is a loop** — a drain that names no deadline is given one two hours out, and a fan above
the manifest's `concurrentJobs` for the operation it posts is refused at the door rather than
discovered one refused job at a time.

A drain is not a governor and sets no overlay: the standing policy and the budgets table are
untouched, its jobs take no claim, and what one run may spend stays the standing policy's ceiling.

### 11.2 Pre-flight (T-24h, rehearsal)

> **OPERATOR STEP — pre-flight (not executed).**
> **Prerequisites:** a reachable hub with the Babel plugin installed, one enrolled machine, and the
> account to drain named in advance. Each item has an observable success; an item without it is a
> no-go.
>
> 1. The OMP auth broker is healthy. On 2026-09-13 it was `failed` from 11:07 to 11:24 and every
>    engine launched in that window died before its ready frame.
> 2. A **Code profile** exists for the drain's account and model. A profile IS a configured Code
>    workspace: Code owns the model, the thinking level and the account, and Code's generator is
>    where they are set. Babel neither composes a session nor prices one — it names the container
>    and the revision it was shown. **Success:** Watch's Start section lists that workspace, and the
>    row beside it names the model the drain expects to spend on.
> 3. **Code and omp are installed on the hub and consented at the revisions in force.**
>    `atyrode.babel` declares `atyrode.code` a required dependency, Code declares `atyrode.omp`, and
>    Babel's call travels under the principal of the request it is answering: a hub missing either,
>    or an install whose grant does not reach Code's doors, answers `engine_unavailable` or
>    `engine_forbidden` on the `profiles` door before any button. **Success:** the Start section
>    shows profiles rather than a refusal sentence.
> 4. **The pinned Code takes Babel's prompt.** `runSession` bounds a prompt at `PROMPT_MAX_BYTES` —
>    omp's own constant, re-exported by Code, counted in encoded bytes — and Babel's composed explore
>    prompt is about 33,700, so it fits with room to spare. A selection far larger than the presets',
>    or a corpus of non-ASCII selectors, is how that stops being true. **Success:** no run in the
>    drain closes `prompt_too_large`; if one does, the row carries both figures, and what moves is
>    Code's bound or the analysis contract, never a narrower window.
> 5. The open atyrode/babel issues labelled `drain` have been read. Any still-open one that names a
>    blocker for this machine is a no-go.
> 6. A five-minute rehearsal: `drainStart` with `concurrent: 2`, the Code profile from item 2,
>    `target.costMicros` equal to one exploration's price, `deadline` = now + 5 min.
>    **Success:** two jobs reach the stage `at the model` within 90 s of launch and settle with
>    `usage.inference.calls > 0`.

### 11.3 Go / no-go (T-0)

> **OPERATOR STEP — go / no-go (not executed).**
> Start with the rehearsal's settings scaled to the planned concurrency. If no job reports the stage
> `at the model` within **90 seconds** of the first launch, stop (`drainStop`) and touch nothing else
> until the reason has been read from the job journal. No sleeps, no restarts, no policy edits while
> jobs are in flight. On 2026-09-13 no engine process existed for 75 minutes and nobody looked until
> the operator asked.

### 11.4 Watching

The Watch drain panel shows, for the running drain: jobs live, jobs at the model, tokens and cost a
minute over the last three minutes, spend against the target, ETA against the deadline, refusals by
reason, and the account. Each number has one thing it must do: jobs at the model must be non-zero
within 90 s; tokens per minute must be non-zero once a call has been metered; spend must rise toward
the target; the ETA must stay before the deadline. **Tokens per minute flat for three minutes while
jobs read "at the model" is a no-go: stop and read the journal.** A process count, a socket count or
a percentage read by a home-made script is not any of these numbers.

### 11.5 Stopping

> **OPERATOR STEP — stop (not executed).**
> `drainStop` cancels what the drain still holds, in the lane each job is in, which the row itself
> says: a run that reached a model is a Code session and is cancelled through `code.cancelSession`,
> because its job belongs to `atyrode.omp` and the hub's own cancel is bound to the caller's plugin
> id; a run still preparing has no session yet, so its `atyrode.babel.prepare` job is cancelled —
> that one is Babel's — and its row is closed, which is what stops a later wake from posting the
> session anyway. The door answers how many it cancelled and which it could not, and the panel shows
> those two numbers rather than assuming the cancels landed.
> A drain that still holds a job is `closing`, not finished: what those jobs metered is part of what
> this drain spent, so the row keeps them, folds each receipt as it lands, and records its ending
> when none is left. **The final totals are the ones on the `closing` drain when it finishes**, and
> pressing stop again on a `closing` drain is how the stragglers a self-stop could not cancel are
> cancelled — a tick woken by a settlement holds no cancel capability, an operator's press does.
> **Never `pkill` a job.** The hub owns the process, and a killed worker's claim holds its batch slot
> for the whole lease: on 2026-09-13 five rounds of kills under a 5200 s lease left ~70 ghost claims
> on the top-ranked subjects and the last fan could not draw at all. The 2026-09-10 burn notes said
> the same thing; it was violated five times anyway.

### 11.6 Rules for whoever drives it (human or agent)

Mandatory, and each one was broken on 2026-09-13.

1. The asked-for thing is measured directly: tokens the hub metered on the named account. Never an
   adjacent thing (process counts, TLS sockets, a percentage read by a home-made script).
2. A claim of progress carries the number and its source.
3. No command that blocks the driver for more than 15 s during a deadline.
4. No restart without a stop that releases claims.
5. No policy, lease or batch edit mid-drain.
6. No feature work during a drain: route around it or stop.
7. Every failure met is filed with the `drain` label before the session ends, and the next drain's
   pre-flight reads them.
8. The allocation across duties, the profile (model, thinking, advisor, subagents) and the account
   are named by the operator or asked for before the drain starts. An agent running Babel never
   chooses them silently; an operator running it by hand is asked by the door.

### 11.7 What shipped, and what was exercised

**Shipped.** The drain: `drainStart` / `drainStatus` / `drainStop`, the controller
(`atyrode.babel/server/drain.ts`) and the Watch drain section
(`atyrode.babel/watch/drain.tsx`). The release path: a `v*` tag packs, verifies, attaches
the bundles and hands them to the integrated preview's receiver.

**The engine is Code.** Babel does not launch omp and does not compose a session: `atyrode.babel`
depends on `atyrode.code`, which depends on `atyrode.omp`. Watch's Start section lists the saved
Code profiles Babel read through its own `profiles` door, links to Code's generator for the
workspace chosen, and offers no model, thinking or account field of Babel's own. A press selects the
sessions, posts Babel's own `prepare` job — whose second sealed output is the material the run reads
— composes the prompt around `/inputs/material`, and asks `atyrode.code.runSession` to post the
session. A settled session is reconciled through `code.readSession`, because Code's job belongs to
`atyrode.omp` and its settlement never reaches Babel; a refused submission is still spend, and
settles its claim at its cost. The drain's fan goes through the same launch path and names the same
Code profile.

**Exercised, with the evidence.** On `workstation-linux`, 2026-09-14: the plugin gate —
`deps:code`, `check`, `bun test` (516 tests), `pack`, `verify` — against a real engine at Manifold
`476a586c`, with `atyrode/code` pinned at `8b5ba71d` in both `plugins/CODE_REV` and
`@atyrode/manifold-code`. `verify` composed ten bundles on the disposable engine in dependency
order: `atyrode.omp` and its two parts, `atyrode.code` and its three, then `atyrode.babel` and its
two — which is what a `required` dependency costs and what it proves. Beside the gate: the Code
client's refusal translation against the real host sentences and the material bound into a posted
session that takes Code's job id, with Code's own input schema parsing the request
(`atyrode.babel/server/engine/session.test.ts`, `atyrode.babel/doors/drain.test.ts`);
the settle path — valid, refused-and-charged, still running, cancelled-with-no-transcript, and a
read Code refuses twice — against a fake `readSession`
(`atyrode.babel/server/conductor.test.ts`); the sealed material's layout and its digests
against a real temporary lease (`atyrode.babel/machine/prepare.test.ts`); both launch wakes,
the press that seals and the settle that posts (`atyrode.babel/doors/launch.test.ts`); and
Watch's Start and drain sections rendered from a fake `profiles` door
(`atyrode.babel/watch/test/`).

Local synthetic evidence, 2026-09-16: the conductor regression uses the real SQLite store and the
reception read model with a simulated Code receipt. It covers a reclaimed claim's new run, rejects
the superseded job's assessment while retaining its usage, and preserves the newer claim. It does
not establish live inference, broker metering or preview admission.

**Not exercised — every item below is an OPERATOR STEP.** Nothing has run against a real hub, a real
Code install or a real omp account: no model has answered, no inference call sits on a real journal,
no provider window has moved. The lane is whole in code and unproven in the world. What remains
owed, in order:

> 1. A Code profile exists on the target machine for the account and model the drain will spend; Code
>    and omp are installed and consented at the revisions in force. **Success:** Watch's Start
>    section lists it, and a launch reaches Code's door.
> 2. One exploration, by hand, from the Start section. **Success:** the run takes a Code job id,
>    `code.readSession` answers it on a later cycle, and the receipt carries the model, the account
>    and what it spent.
> 3. §11.2 item 6, the five-minute rehearsal, from the Watch drain section: `concurrent: 2`, target
>    one exploration's price, deadline now + 5 min. **Success:** two rows reach `at the model` within
>    90 s and settle with calls > 0; record host, date and the drain row here.
> 4. **OPERATOR STEP — the first live drawn review.** Install the review-enabled bundle, then use
>    `setPolicy` to record `review.machineId`, the saved Code profile, versioned `review.recipes`
>    and `review.roleRecipes` for every role. Start with a batch size and a per-machine
>    concurrency of one and retain the standing lease bounds. The conductor draws and dispatches
>    on its cadence; `review-backlog` and `file-and-tidy` stay unavailable as direct launches,
>    which is deliberate (§10). **Success:** one Code receipt names the selected model and its
>    measured usage, the claim settles, and the record's reception shows the assessment. Only then
>    enable continuing maintenance work.

---

## What remains operator-gated

None of the dated observations above establishes current fleet state. These remain unexecuted:

1. Independent custody backup and recovery (§3), and any derived generation, encrypted commit and
   per-machine apply. Preserve the existing password and the whole append-only ring; missing
   placement is not a reason to rotate.
2. The `atyrode.babel.restic` service install and its job binding on each machine that archives
   (§4), followed by a first `archive` whose receipt is read (§1).
3. An answer to §5: what backs up the hub's `data.db`, which is the only copy of everything Babel
   knows.
4. A cadence for `archive`. Babel schedules only the `scan` beat, so today an archive happens when
   someone posts one.
5. A full restore-to-service on a clean machine: recover custody, restore a historical source tree
   with restic (§2), enroll the machine (§6), and confirm the restored bytes match the chosen
   snapshot. The 2026-08-31 cross-machine restore proves its own part and not this composition.
6. The drain rehearsal and the four owed steps of §11.7.
