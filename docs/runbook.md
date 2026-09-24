# Operations runbook: archive, custody, recovery, drains

Babel is a Manifold plugin family under `babel/`. There is no `babel` binary, no
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
Where this runbook says _nothing does this_, that document says the same in a row with an issue.

---

## 1. Backing up: the `archive` operation

The archive is `atyrode.babel.archive`, one of the operations the machine half implements
(`babel/machine/main.ts`; `MACHINE_OPERATIONS` in
`babel/contract.ts`). It runs `restic backup` over this machine's session roots —
OMP, Codex, Claude Code, and Babel's own — **one snapshot per root**, tagged `babel`, attributed
to the machine's own id as restic's `--host` (`babel/machine/archive.ts`). Per-root
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

## 2. Recovery: the `verify` operation, or restic directly

There are two paths, and the second is the one that must never stop working.

**Through Babel.** `atyrode.babel.verify` reads the repository back: `restic check`, structurally
or over every stored byte, and one catalogued session restored from a named snapshot and compared
byte for byte against the digest `scan` recorded (`babel/machine/verify.ts`). The
`atyrode.babel.verify` door posts it and the run's receipt carries the verdict. It is the path for
the routine question — is the archive sound, and does this session still come back — because it
takes the snapshot and the digest out of the catalog rather than out of somebody's memory, it
deletes nothing and cannot (the verbs restic may be asked for are a closed set that holds no
`forget`, `prune`, `repair` or `unlock`), and it leaves a receipt a reviewer can read later.

**By hand.** `restic`, with the repository and the password out of custody (§3). Use it when there
is no hub or the store is lost; when the machine that took the snapshot is gone, or was never
enrolled; when the session is not catalogued, or its row is an imported one whose snapshot column
is not a restic id; when what is wanted is a whole snapshot or a root rather than one session; and
for anything to do with a lock, which Babel cannot clear. It is also the path that proves the
property this runbook cares most about: _archive recovery does not depend on the catalog — or on
Babel._ It never did.

> **OPERATOR STEP — verify the archive from the hub (prerequisites in §4 and §6).**
> **Prerequisites:** those of an archive (§1), at this operation's own node: the
> `atyrode.babel.restic` service policy installed and its fingerprint matching the installed job,
> the `restic` runtime tool bound, consent for `services:invoke` at
> `manifold://machine/<machine>/service/atyrode.babel.restic/operation/storage` and for
> `network:host` at `manifold://machine/<machine>/operation/atyrode.babel.verify`.
> **Call:** `atyrode.babel.verify` with the machine, `readData` — `false` for the structure,
> `true` for every stored byte, or a subset such as `"10%"` — and, to prove one session, its
> catalogued `selector`. The door reads that session's snapshot and digest out of `sessions`; a
> snapshot named in the call overrides the catalogued one.
> **Success:** the run settles `completed` and its receipt carries `kind: "verify"`,
> `counts.checked: 1`, `counts.checkErrors: 0`, and — when a session was named —
> `counts.restored: 1` beside `counts.digestCompared: 1`. A `failed` receipt's reason names what
> disagreed: the repository's own errors, or the two digests that did not match.
> **A stale lock fails this step.** `restic check` wants the repository quiescent and takes an
> exclusive lock to get it, so a stranded lock is diagnosed and cleared by hand (§2.3) before a
> verification will run at all.

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
to a _different_ machine (`alex-x86_64-linux-wsl`) — one this host had never held locally — was
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
locked by PID 2841104 on ubuntu-4gb-nbg1-1`; two _shared_ locks were stranded in the repository
from processes that no longer existed. A stale shared lock endangers no data and blocks no
restore — both restores above succeeded while they were present — it blocks `check`, which wants
the repository quiescent. `restic check --no-lock` then reported 44 snapshots and no errors.

> **OPERATOR STEP — clear a stale lock (never automated, never an agent's to run).**
> **Prerequisites:** the lock's holder is confirmed dead, by host as well as PID; a lock naming
> another host is never judged by local PID liveness. Nothing in Babel inspects or removes a
> repository lock — a verification's own `restic check` takes one and releases it, and nothing
> else — and `.omp/skills/babel-cli/SKILL.md` forbids an agent from running `restic unlock` at
> all.
> **Success:** `restic list locks --no-lock` shows the lock gone and `restic check --no-lock`
> exits 0. Measured against restic 0.19.1, plain `restic unlock` removes a stale lock including an
> exclusive one; `--remove-all` is what a lock that is _not_ stale needs, and it removes every lock
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

| Contract                                                                                        | Pinned source                                                                                                                                                            |
| ----------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Shared custody; an existing password is prompted, never minted; existing rings survive rotation | [modules/shared/babel-archive.nix:1–31](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L1-L31)       |
| sops placement, owner/group and 0600 mode                                                       | [modules/shared/babel-archive.nix:44–75](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L44-L75)     |
| Shared `babel-custody`: secret undeployed inputs, a deployed complete ring, prompt validation   | [modules/shared/babel-archive.nix:77–178](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L77-L178)   |
| Per-machine derived configuration and registry identity                                         | [modules/shared/babel-archive.nix:180–243](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L180-L243) |
| Operator-device generation followed by apply; no vault or provider session on the target        | [fleet/provisioning.json:39–44](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/fleet/provisioning.json#L39-L44)                       |

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
service**, `atyrode.babel.restic`, and from nowhere else (`babel/machine/restic.ts`;
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
> `docs/building.md`). The store is the operator's; Babel ships none and generates none. **Nothing
> anywhere accepts the token's value through a screen**: Manifold has no path for a person to
> supply a credential value at all (atyrode/manifold#768), so the file is written on the machine
> and the policy only ever names it.
> **Procedure:** Watch's **Services** section. Pick the machine and press **Check**: it composes
> the policy out of the `services` block `babel/manifest.json` already declares — the service id,
> its revision and its operations — and reports, per service, what is installed, the credential
> reference the policy needs, the file the machine's agent reads it from, and whether the binding
> came up. Fill the endpoint, press Check again to compose it, then press **Install**, which
> applies the policies as a compare-and-swap on the configuration revision the preview was read
> at. A preview that has gone stale is refused naming why rather than overwritten. Then install
> the job with `resourceBindings.services["atyrode.babel.restic"]` carrying the fingerprint
> `engine.jobs.describe` reports for that policy; that half is still by hand.
> **Success:** the section reports the service as `ready`, and an `archive` job admits rather than
> refusing `service_binding_mismatch`. A policy the operator changed is a new installation, never
> a silent upgrade — the mismatch is the point.

> **OPERATOR STEP — install it by hand instead (three cases, and only these three).**
> The panel is the ordinary path; the hand-written call remains right when **there is no hub UI to
> reach** (a headless recovery, a script, a machine being provisioned before anyone signs in),
> when **recovering from a configuration the panel cannot compose** — a policy Babel no longer
> declares, or one that must be removed rather than replaced — or when **the machine is one the
> panel cannot reach**, because Watch offers only machines this hub enrolls.
> **Procedure:** one owner call of `engine.services.configureConfiguration` naming
> `serviceId: "atyrode.babel.restic"`, `revision: "1"`, the origin, the credential ref and the
> single `storage` operation, with `expectedRevision` set to what `readConfiguration` last
> reported (`null` for a machine with no configuration). The document's exact shape is in
> `docs/building.md`. It carries every policy on that machine, not only Babel's: the call
> replaces the whole configuration, which is the one thing the panel does for you.
> **Success:** as above.

**Nothing on the machine is a Babel configuration file any more.** No path under `~/.config` is
read by any Babel program; `docs/parity.md` records the retired configuration package as absent by
decision. What dotfiles places on a managed machine is custody (§3) and the service credential
above, and the operator reads a value out of custody only when he is about to use `restic` by hand
(§2) — never by printing it.

---

## 5. Backing up the store itself

The hub holds Babel's one store: a SQLite file at `<data>/plugins/atyrode.babel/data.db` (on the
preview hub, `/data/plugins/atyrode.babel/data.db` inside the Manifold container), created by the
enable hook and deleted by a purge (`docs/building.md`). Records, edges, rulings, assessments,
filings, the ledger and every receipt live there and nowhere else. There is no publication and
nothing to reconcile: `docs/parity.md` records publication, the shared catalog and the object
store as absent by decision, and no second Babel-owned store is added in their place.

**That store is currently a single copy.** Neither of Manifold's own copies answers for it.
`data.db.backup` is the fixed path a database migration stages its rollback image at, beside
`data.db` on the same volume (atyrode/manifold at `7b5fe301`, `docs/PLUGINS.md:1000-1006`): it
recovers a failed migration and is lost with the volume. Litestream replicates `manifold.db` and
excludes every per-plugin `data.db` (`docs/SELF-HOST.md:902-904` there). The backup of `<data>/`
that would include it (`docs/PLUGINS.md:1135-1137`) is a `tar` an operator takes by hand
(`docs/SELF-HOST.md:745-751`), and nothing schedules one. Two consequences an operator must hold
at once:

- **The sessions are safe without it.** Every archived session is restorable from restic with the
  password alone (§2), and the catalog rows that point at snapshots are rederivable by re-running
  `scan` and `archive` on each machine.
- **Babel's own analysis is not, yet.** A hypothesis, a finding, a ruling or a receipt exists in
  `data.db` and nowhere else, so until the backup below has run, losing the hub's volume loses
  every record Babel has produced or imported.

**The backup is open work, tracked in #454, and its destination is decided**: the restic
repository that already holds the session transcripts — the one the storage document names (§4)
— under its own tag, `babel-store`. Never `babel`: snapshots tagged `babel` are read as session
transcripts, and a store image among them would be read as one. The backup belongs to the
deployment, placed on the hub's host by dotfiles, and is not a Babel operation. It takes a
consistent image of the live database rather than a byte copy of a WAL-mode file, and it is a copy
of the one store, never a second store Babel reads or writes. The procedure and a proven restore
are recorded here, with host, date and observed output, once they have run; until then nothing in
this section is exercised.

---

## 6. Enrolling a machine

A machine runs Babel's jobs when four things are true of it. None is a Babel command; all four are
the hub owner's or the machine's declaration.

1. **It is enrolled in the hub** and online.
2. **It binds the tools each operation names** (`docs/building.md`): `bun` is pinned by Babel's own
   bundle and needs no binding; `development` carries `git`; `system` is the reviewed native
   closure the pinned bun is dynamically linked against; `restic` is the owner's, by name, and only
   `archive` asks for it.
3. **Its anchors exist, and `home` is where the sessions are.** The `runtime` anchor must be a
   dedicated bounded tmpfs, since the named-output lease is cut from it. The `home` anchor needs
   `~/.omp/agent/sessions`, `~/.codex` and `~/.claude` beneath it, or a job whose read location is
   missing fails to start. **Creating them does not make the sessions readable on a native
   Manifold worker** (the NixOS module): there the `home` anchor is the service account's
   workload home, `/var/lib/manifold-workload/home`, hard-coded by the module (atyrode/manifold at
   `7b5fe301`, `infra/native/module.nix:10,23-31`), and an operator may also protect `/home` from
   workloads with `execution.protectedDirectories`. `mkdir -p` there makes `scan`, `archive` and
   `prepare` start and read an empty tree. Local roots are only for the machine that holds the
   sessions, with its `home` anchor at the home that holds them; reading every machine's sessions
   from the archive instead is #453.
4. **Its consents are recorded** at the nodes §1 names, plus `machines:run` at the operation node
   for anything that launches.

> **OPERATOR STEP — join a machine (not executed against the current fleet).**
> **Prerequisites:** items 1–4 above, and §4's service install if this machine is to archive.
> **Success:** a `scan` job settles and its sessions appear in the store under that machine's id;
> then an `archive` job settles with a snapshot per root. A successful `scan` with no roots is not
> proof of anything but an empty machine.

**Item 3's runtime scratch has one size.** Every operation that writes the `runtime` anchor —
Babel's `scan`, `archive`, `prepare` and `verify`, and `atyrode.omp.session` — declares
`outputBytes` 1 GiB. The runtime refuses a job whose `outputBytes` is below the scratch's
capacity (`bounded-output-storage-required`) and gives stdout and stderr only what is above it
(`docs/building.md`, the machine half). So the scratch is 768 MiB (`RUNTIME_SCRATCH_BYTES`),
which leaves each of them 256 MiB of stdio, and never the full 1 GiB, which would leave none.

> **OPERATOR STEP — size a native machine's runtime scratch (not executed).**
> **Prerequisites:** the hub runs a Babel bundle whose four runtime-writing operations declare
> 1 GiB; an older one's `scan`, `archive` and `verify` declared 64 MiB and are refused on any
> larger scratch. Every other operation installed for that machine that writes the `runtime`
> anchor declares more than 768 MiB.
> **Procedure:** in the machine's NixOS configuration, set
> `services.manifold.execution.outputBytes = 805306368;` and `outputInodes = 10000;`, then
> activate. Merging the configuration is not activation.
> **Success:** `findmnt -no OPTIONS /var/lib/manifold-output` shows `size=786432k` and
> `nr_inodes=10000`, and a `scan` and a `prepare` on that machine settle rather than refusing
> `bounded-output-storage-required`.

**A machine id is not a host name.** Every column Babel keys on a machine — `sessions.host`,
`runs.machine_id`, `drains.machine_id` and `run_calls.transcript_host` — holds the id
`core.machines.list` publishes, and the hub resolves no names. The crossing refuses a chunk whose
machine column names something the hub cannot describe, and it reads that list of columns out of
the migration rather than out of a list of its own, so a column added under either spelling is
guarded from the day it exists. The imported Go-era corpus carries the old deployment's host name
instead, which is what the `rehostSessions` door exists to repair: it rewrites one `host` value to
one machine id the hub has just described (`babel/contract.ts`).

---

## 7. Cadence, stopping and rollback

### 7.1 The one schedule

`engine.jobs.schedule` schedules a job on a machine, so the loop's beat is the cheapest useful job
Babel owns: `scan` (`BEAT_OPERATION`, `babel/server/conductor.ts`). It spends no
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

Stopping a _drain_ is §11.5, and it is a different act: the policy is untouched and the jobs in
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

The hub's durable analysis records are read through its panels. `atyrode.babel.feed` serves Home (every
record, ranked by what needs the operator), the peeled record and a topic with its filings and his
interest; `atyrode.babel.watch` serves what is running, what will run and what a drain is spending
(`docs/building.md`). Behind them are the read doors — `feed`, `record`, `thread`, `topics`,
`topic`, `pulse`, `runs`, `run`, `policy` — spelled in `babel/contract.ts`.

Three things that used to be commands are now properties of having one hub:

- There is no fleet read: every machine's work lands in the same store as it settles.
- There is no pending state to chase: a record is durable when the job that produced it is ingested,
  and ingestion is idempotent, so a retried cycle writes the same rows.
- An empty answer is an answer. A deployment that has explored nothing shows nothing, and that is
  not distinguished from a malfunction because there is nothing to distinguish.

### 9.1 Recall reads archived conversations, not the live corpus

`babel/recall-skill.md` is the versioned outside-agent procedure, also returned by the
`recallSkill` door. The installed `babel-recall-runner` is a thin wrapper around Manifold's
supported SDK runner, not a resurrected Babel CLI. Its immutable `babel/recall-profile.json`
approves six exact bounded read doors. Neither file selects a hub, discovers a credential,
creates a grant, launches inference or installs a native runtime.

> **OPERATOR STEP — provision Recall authority. Not exercised on a live deployment.**
> **Prerequisites:** an enrolled owner has installed this Babel artifact with the `restic` and
> `system` tools, the writable managed cache and the existing `atyrode.babel.restic` storage
> binding (§4). Preserve that deployment's repository and custody; do not create replacements.
> The native `atyrode.babel.recall` operation must be a ready runtime candidate. The owner
> chooses subject names, stable hosts, optional harness/selector prefixes and sensitivities
> 0–3, plus disclosure classes with fixed ceilings. More specific matches cannot lower a
> broader sensitivity: the highest matching sensitivity wins.
> **Procedure:** as root, call `previewRecall` with the selected machine and policy. Inspect
> its readiness, reason, changed flag, exact returned class targets, configuration revision
> and preview digest. Call `installRecall` with that same policy, machine, expected revision
> and digest; it re-reads the installation/artifact/resource tuple and uses native
> compare-and-swap. A changed preview must be reviewed again. Installation configures one
> owner-managed persistent instance service, not one native job per reader.
> Grant an outside agent only `services:invoke` at its exact returned disclosure target;
> do not give it native job, configuration or storage authority. The trusted launcher
> supplies the hub and its own supported Agent/run binding, privately, and the matching
> reviewed profile. **Success:** native readiness is acknowledged, a permitted filtered search
> returns projected evidence and a durable trace, and an intentionally higher-class subject
> stays refused before and after another authorized caller warms its cache.

Start with bounded search, inspect partial coverage and archive dates, then follow returned
locators. The archive is the only source: newer live logs and unarchived sessions are blind
spots. Keep `requestId` on pending or uncertain starts and poll it with the same principal and
target. If projection publication lost that handle, pass the original numeric `traceId` instead;
`located` returns only the owned request id, which can then be polled normally. Lookup preserves
the same principal/target/grant/revision fences and performs no native work. If both identifiers
are lost, report uncertainty, not zero cost. Do not automatically retry under fresh ids.
All excerpts are mandatory-redacted, explicitly untrusted data. Detector coverage
and historical metadata have limits; neither is permission to send sensitive material to a
different class or provider.

Whole-session widening requires a completed size preview and explicit intent after reviewing
that size. Respect the returned per-class `previewByteLimit`, sequential `nextOffset` and 8 KiB
page ceiling. Progress renews the one-hour idle window; an expired or revision-invalidated
preview needs a new preview, not guessed offsets. Completion releases temporary bytes while
preserving a bounded final-page retry. Temporary widening lives in the native job's private
tmpfs, never its persistent cache. Rebuildable indexes and metadata sidecars may be lost without
losing the archive; rebuilding incurs reported fetch/replay work.

**Executed synthetic evidence — dev-01, 2026-09-21.** A disposable restic repository, private
HOME/XDG roots and a bundled machine half used the SDK's real anonymous input materializer
(including its JSON-encoded generated service bearer), readonly input mounts and owner IPC.
After deleting both synthetic source logs, the native service returned 2,000,413 canonical
redacted bytes from a 2,000,394-byte capture in 245 UTF-8-safe pages. Reconstructed bytes and
digests matched preparation's canonical scanner; cold refusal and higher-class cache warming
preserved disclosure isolation. The final page retried unchanged, old transport responses
expired through bounded eviction, and owner disconnect exited zero. Fifty core/record/store/
runtime regressions and the eleven-test shared preparation suite passed. The compiled skill
was byte-identical to its source and all six approvals matched their declared digests.
Fixtures were removed. This proves synthetic data paths, **not** installed native governance,
real-corpus classification, managed activation, a paid provider call or production installation.

The Nix-built managed wrapper was also exercised on dev-01 on 2026-09-21 against a disposable
loopback lifecycle peer connected to the actual Babel doors, ledger, native HTTP service and
archive core over an immutable synthetic snapshot. It delivered the exact shipped skill and
mandatory-redacted carrier-shaped evidence, performed one cold fetch, and recovered a deliberately
withheld request handle from its action trace without a repeated fetch or evidence publication.
Every SDK run confirmed cleanup. The managed `omp-stack` check proved byte-identical skill
paths and OMP command discovery without activation or providers. This separate consumer proof
does not claim real hub admission or a live storage binding.

---

## 10. Turning evaluation on

Evaluation is off until one operator decision turns it on: `enabled` defaults to false in the
policy schema (`babel/store/coordinator.ts`), which is the activation gate
expressed as a value rather than as a migration.

The policy is one document, written through the `setPolicy` door, and it carries both the
authorization and the route:

| Field                                                                                | What it decides                                                                                                                                    |
| ------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------- |
| `enabled`                                                                            | whether the loop exists at all                                                                                                                     |
| `cadenceSeconds`                                                                     | the beat's period (§7.1)                                                                                                                           |
| `batchSize`, `concurrentPerMachine`, `leaseSeconds`                                  | how many assignments may be claimed at once, per machine, and for how long                                                                         |
| `perCycleCost`, `dailyCost`                                                          | the ceilings a cycle and a day may spend                                                                                                           |
| `coverageShare`, `explorationShare`, `discoveryShare`, `filingShare`, `backlogShare` | the protected allocations across lanes                                                                                                             |
| `review.machineId`, `review.profile`                                                 | where a drawn review runs and which saved Code profile it is posted on                                                                             |
| `review.roleRecipes`, `review.recipes`                                               | which reviewed method each role uses, with the recipe bodies carried in the versioned policy so a later edit cannot change an in-flight assignment |

A policy that cannot be honoured is refused with the sentence saying why: an unversioned policy
could never be replayed against, a zero exploration or discovery share removes a protected
allocation, shares over one over-commit a cycle, and a daily ceiling below one cycle's makes the
per-cycle bound decorative. The recipe bodies come from `babel/store/recipes.seed.json`,
produced by `babel/tools/seed-recipes.ts`.

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
> `dispatchReviews` in `babel/server/conductor.ts` draws, claims under a fence,
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

**This section applies when the operator asks to drain a paid usage window**, and only then.
Running Babel's ordinary work — reviews, the analysis stages, the conductor's cycle — on any
model, a free one included, is ordinary operation (`AGENTS.md`, Boundaries): it runs under the
standing policy and budgets and needs none of the pre-flight, go/no-go or reporting rules below.

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

On 2026-09-13 the allocation was everything on `review-backlog`, and that was a sound _choice_:
reviews are the mass-produced unit of Babel's self-maintenance, duplicate assessments are reception
data rather than waste, and a review can be as heavy as its profile makes it. It failed because each
review re-prepared the whole corpus before its first model call (post-mortem F1, O1). What must hold
for any allocation: a run never re-prepares the corpus, and the fan is sized to the measured cost of
one run.

Two bounds are structural rather than advisory. **A drawn preset cannot be fanned out**: the
coordinator arbitrates a draw under a claim and a fence, and fanning it would be a second
implementation of that arbitration — so `drainStart` takes the directly-launched presets only
(`babel/server/drain.ts`). And **a drain without a finite bound is a loop**:
`drainStart` requires a target, deadline or positive `maxJobs`; omission of the deadline still
gives it one two hours out. A fan above the manifest's `concurrentJobs` for the operation it posts
is refused at the door rather than discovered one refused job at a time.

`concurrent` limits simultaneous work, not total launches. `maxJobs` bounds admission ordinals
across wakes and recovery, including refused and zero-usage attempts. At that bound the drain
waits for its held jobs and folds their results without cancelling them or refilling the fan.
Optional `inferenceLimits` use Code's published schema and are retained through preparation and
every refill. Ordinary `launch` accepts the same ephemeral limits. Malformed retained limits
refuse rather than replaying an unbounded request.

A drain is not a governor and sets no policy overlay: the standing policy and budgets table
are untouched, and its jobs take no claim. Native token/cost thresholds are checked before a
call, so an accepted response can overshoot. Provider-internal retries and charged failures
still require a conservative exposure reservation in the shared ledger before admission.

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
> 6. A five-minute rehearsal: `drainStart` with `concurrent: 2`, `maxJobs: 2`, the Code profile
>    from item 2, reviewed per-job `inferenceLimits`, `target.costMicros` equal to one exploration's
>    price, and `deadline` = now + 5 min. Reconcile cumulative spend and reserve worst-case
>    in-flight exposure first; the target is not a hard spending ceiling.
>    **Success:** two jobs reach the stage `at the model` within 90 s of launch and settle with
>    `usage.inference.calls > 0`.

For a manual-only rehearsal, first quiesce existing work, then retain the policy's `review`
block and recipes with `enabled: true` and every `activityWeights` value set to zero. This
withdraws the standing scan beat and suppresses new autonomous title preparation without
blocking an explicit exploration or drain. It does not cancel or discard previously admitted
work. Setting the whole policy to `enabled: false` also fences deferred manual model admission,
so use that state before and after the bounded run, not while its material is preparing.
Restoring a configuration must not silently restart standing work.

**Partial pre-flight observed on dev-01, 2026-09-22, integrated preview
`https://preview.manifold.tyrode.dev` (protocol 42, build `0.17.0+65.g7b5fe30`).**
Native destination, account broker and gateway setup reads reported ready under the inspected
owner authority. Both saved Code catalogs lacked Haiku/Luna entries. A non-inference native
inventory request with one configured Anthropic account returned `omp_invalid_request` after
posting job `722d2c40-bbb5-40fa-9ba4-964f35a1ebc3`; that job exited 0, and its ordinary
`readInventory` still refused. The sealed native stdout was recovered diagnostically with its
SHA-256 verified (`31d06cafdfeea1386be33308c2513c0f9804aceb6f9e3e174184fc42aa0e45a1`).
It listed `anthropic/claude-3-haiku-20240307`, `anthropic/claude-haiku-4-5`, and
`anthropic/claude-haiku-4-5-20251001`. Artifact recovery is not a successful Code consumer path.
Catalog membership is not provider availability: Anthropic lists
`claude-3-haiku-20240307` as [retired since 2026-04-20](https://platform.claude.com/docs/en/about-claude/model-deprecations),
so it is not a cheap fallback for this rehearsal.

The initial inventory refusal was reproduced without posting another job. The then-installed OMP root
bundle `f72c104a1c794499b9846bbf78e671d142ee9ab2b337bae1853d588d2fc9fc3c` was built against
Manifold `743ee75a92b64b75b4a97244ebd58297c1287164`. Its strict public-job schema rejected
the exact retained native status receipt solely for the root `limits` key. The current
native schema against Manifold `7b5fe3015c3308532de634c2b2d8068ec2f0e451` accepted that same receipt.
Both output APIs are valid; no output-reader workaround was needed. The compatible deployment
and normal inventory start/read smoke below subsequently passed.

**Offline native proof on dev-01, 2026-09-22:** the
[packaged verifier at native revision `74c0759`](https://github.com/atyrode/manifold-omp/blob/74c0759/plugins/sdk-host/test/packaged-sdk-host.ts#L365-L397)
observed model progress before an isolated synthetic response completed in both CLI 18.1.14 and
SDK-host 18.2.7 one-shot paths, with stdout and sealed session receipts intact. The full native
gate passed (181 tests, zero failures). This proves those runtimes, not the governed
Babel → Code → OMP launch/follow/result path or a real-provider rehearsal.

Reviewed service-call and recorded-usage limits do not bound every provider-internal retry or
its in-flight response. The
[reviewed overlay at native revision `74c0759`](https://github.com/atyrode/manifold-omp/blob/74c0759/plugins/api/index.ts#L128-L165)
has no supported per-request token cap reaching the preserved gateway. A remaining account
balance or a low expected call price does
not replace that exposure bound.

**Compatible preview deployment qualified on dev-01, 2026-09-22.** The ten existing
OMP, Code and Babel family bundles were replaced through the supported receiver, retaining their
hardening settings. The installed root SHA-256 values are:

| Root  | Qualified bundle SHA-256                                           |
| ----- | ------------------------------------------------------------------ |
| OMP   | `0f69f37bac23a2ac4eb132e5ff630ae7d66f41d1ac3c555d5d068ef0ea21bd2c` |
| Code  | `8aa40792b7572686a255e2359c4e5bd2c1bbfc1be48e0580d3427a1dba20303c` |
| Babel | `526744e6c5b2ba281778d6cc91acadb6f91a4368776d6429011003650849a0c3` |

Policy `qualification-264-paused-20260922`, sequence 185, has `enabled: false`; its other
policy fields were preserved. The dependency-ordered pause preceded replacement. Babel
[#445](https://github.com/atyrode/babel/pull/445) makes that activation fence atomic with
deferred posting. The views were restored without restarting standing work, and an explicit
disabled-policy pulse left the native job count unchanged with no active work. On
`https://preview.manifold.tyrode.dev`, open **Watch → Ceilings**: the expected policy version
is `qualification-264-paused-20260922`. Do not press Start as part of this inspection.

Native replacement invalidated the old runtime approvals. The account broker and gateway
were reviewed and renewed in place, retaining client-listener configuration and existing
pricing configuration. Both report ready; eight accounts are visible again, and both saved
Code profiles retain revisions 15 and 3 and their selected models. Native OMP approval on
dev-01 was renewed for inventory only, not benchmark or model-session execution.

The ordinary `startInventory` and `readInventory` doors succeeded for job
`79c5465e-7f79-4a64-8847-976665fe82a9`: CLI 18.1.14, exit 0, 12,191 ms, 24 models, and
authoritative inference usage of zero calls, zero tokens and zero cost. Haiku 4.5's catalog
metadata reports US$1/M input, US$5/M output, a 200,000-token context and a 64,000-token
maximum output; this is still not a provider reachability or worst-case exposure proof.

The shared verification ledger is US$0 spent, US$0 reserved and US$5 remaining. No provider
inference, benchmark, exploration or rehearsal was executed. The deployed gateway has no
reviewed price schedule; the dedicated profile, conservative provider retry/token exposure,
paid-operation approval and complete Babel → Code → OMP consumer proof remain admission
prerequisites. No new OMP patch was introduced. The rehearsal, 90-second go/no-go, live-model
Watch, Stop and final-result procedures remain unexecuted. Track their receipt in #264 and
atyrode/code#170; the inventory compatibility diagnosis and successful smoke are recorded
separately in atyrode/manifold-omp#71.

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

A drain that reaches its ending — a target, a deadline, a stop, or a failure — leaves a **drain
report**: one record in the frontier, of kind `finding`, written by the controller with
`actor_kind = 'engine'` and `provenance: "drain"` in its payload. It answers, from itself alone,
the questions the 2026-09-13 drain was reconstructed by hand to answer: the allocation as the
operator named it and as the runs carried it; tokens and cost per duty and per account; jobs
launched, at the model, settled and refused by code; launches that never became jobs, by the hub's
own code; records and assessments per million tokens; where the wall time went between preparing
and being in session; the reason for every gap; and the controller's own notes — stalls, admission
refusals, and jobs it had to take back. It also names what it cannot see: the machine's CPU and
memory, cache-write tokens, and the account's window reading. The Watch drain panel shows the last
drain's report beside the drain that left it, and because the report is an ordinary frontier
record, an `explore` run can be pointed at it to propose what the next drain should change.

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
(`babel/server/drain.ts`) and the Watch drain section
(`babel/watch/drain.tsx`). The release path: a `v*` tag packs, verifies, attaches
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
(`babel/server/engine/session.test.ts`, `babel/doors/drain.test.ts`);
the settle path — valid, refused-and-charged, still running, cancelled-with-no-transcript, and a
read Code refuses twice — against a fake `readSession`
(`babel/server/conductor.test.ts`); the sealed material's layout and its digests
against a real temporary lease (`babel/machine/prepare.test.ts`); both launch wakes,
the press that seals and the settle that posts (`babel/doors/launch.test.ts`); and
Watch's Start and drain sections rendered from a fake `profiles` door
(`babel/watch/test/`).

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
3. The hub store's backup (§5, #454): a scheduled image of `data.db`, the only copy of everything
   Babel knows, in the transcripts' restic repository under `babel-store`, and one proven restore.
4. A cadence for `archive`. Babel schedules only the `scan` beat, so today an archive happens when
   someone posts one.
5. A full restore-to-service on a clean machine: recover custody, restore a historical source tree
   with restic (§2), enroll the machine (§6), and confirm the restored bytes match the chosen
   snapshot. The 2026-08-31 cross-machine restore proves its own part and not this composition.
6. The drain rehearsal and the four owed steps of §11.7.
7. Each native machine's runtime scratch sized to 768 MiB (§6), after the bundle declaring 1 GiB
   on every runtime-writing operation is installed.
