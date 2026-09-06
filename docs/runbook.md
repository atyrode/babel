# Operations runbook: recovery, custody, and rollback

This document separates **historical evidence** from **current operator
procedures**. Captured output is dated; it is not a claim about today's fleet.
Every new operational procedure is marked **OPERATOR STEP**, with prerequisites
and observable success. No current secret generation, activation, rotation, or
restore-to-service was executed for this documentation update.

The 2026-08-31 exercises below supply historical evidence for SPEC.md §14's
recovery and rollback gate. Current managed-fleet custody follows the pinned
dotfiles sources in §3; the retired provisioning ceremony is not a recovery path.

## Exercise environment

All historical output below was captured on **2026-08-31** on host `workstation-linux`
(kernel hostname `ubuntu-4gb-nbg1-1`), against the **real production
deployment** — real Cellar repository, real managed PostgreSQL catalog.

```
babel 0-unstable-2026-08-30 (64ba5e178386) linux/amd64 go1.26.5
restic 0.19.1 compiled with go1.26.5 on linux/amd64
```

**Production commands exercised by the historical drill were read-only with
respect to durable remote state.** Scratch configuration checks are separately
identified in §8.1. No snapshot was written, no catalog row was inserted or
deleted, and no `restic` write verb (`init`, `backup`, `forget`, `prune`,
`unlock`, `repair`) was run.
Direct `restic` invocations all carried `--no-lock`; the one Babel command that
reaches the repository through `restic restore` takes restic's ordinary
transient shared lock and releases it, which was confirmed afterwards (see
§2.4).

### Redaction

Babel is a public repository, so the live infrastructure identifiers are
replaced by placeholders and nothing else is altered. Counts, digests, exit
codes, and timings are verbatim.

| Placeholder | What it stands for |
| --- | --- |
| `<cellar-host>` | the Cellar S3 endpoint hostname |
| `<bucket>` | the Cellar bucket holding the repository |
| `<catalog-host>:<port>/<db>` | the managed PostgreSQL endpoint |
| `<catalog-user>` | the catalog credential's username |
| `addon_<uuid>` | a Clever Cloud add-on id |

No credential value appears in this document, and none appears in the output of
any command it tells you to run — that is a Babel invariant, not a redaction
applied here.

---

## 1. Backup: the hourly timer and a manual push

The archive is published by a systemd user timer on every managed Linux
machine. Babel itself is not scheduled; `atyrode/dotfiles` schedules it.

**Preconditions.** The current clan-var placement and checks in §4 are complete.
The Linux timer's `ConditionPathExists` names `~/.config/babel/storage.json`;
Darwin instead relies on the common wrapper's storage-document gate (§7).
Neither gate proves all inputs or remote services healthy; check §4 diagnostics.

The scheduler runs `babel-archive-push`, which calls `babel archive push --json`
and stamps `~/.local/state/babel/last-success` only for a complete published
snapshot. Manual publication is the unexecuted **OPERATOR STEP** in §6.

**Verify.** `babel archive status` is the read-only fleet-wide answer:

```
$ babel archive status
note: 1 snapshot is archived but not catalogued; `babel archive push` records them
note: 2 snapshots are recorded without session detail, which only its owning host could write at push time; the snapshots stay durable and restorable, and no command resolves this yet, so the count does not fall
HOST                   SNAPSHOTS  LATEST                LATEST ID  TAGS
alex-x86_64-linux-wsl  4          2026-08-31T03:01:23Z  3dd67096   babel
workstation-linux      40         2026-08-31T03:02:30Z  87dc5f89   babel
catalog reachable          yes
uncatalogued snapshots     1
catalog-pending snapshots  2

catalog by host:
HOST                   SNAPSHOTS  SESSIONS  PENDING  NEWEST ORDER  NEWEST SNAPSHOT
alex-x86_64-linux-wsl  3          5         0        3             2026-08-31T02:01:24Z
workstation-linux      40         843       2        40            2026-08-31T03:02:30Z

real  0m4.201s   exit 0
```

`archive status` reports timestamps; it does not answer "did every machine back
up". That is `archive fleet`, which judges each host against a cadence derived
from its own history and always names the source of that cadence:

```
$ babel archive fleet --expect workstation-linux,alex-x86_64-linux-wsl
fleet: 2 hosts, all current

HOST                   STATE    LAST PUBLISHED        AGE  EXPECTED EVERY  SNAPSHOTS
alex-x86_64-linux-wsl  current  2026-08-31T03:01:23Z  31m  1h (observed)   4
workstation-linux      current  2026-08-31T03:02:30Z  30m  1h (observed)   40

real  0m1.653s   exit 0
```

`archive fleet` exits `0` even for a missing host — it reports a judgement and
is deliberately not an alerting hook. Script off the `state` field of `--json`.

The most recent timer-driven push, unedited from the journal:

```
$ systemctl --user status babel-archive.service
○ babel-archive.service - Archive agent session histories with Babel
     Loaded: loaded (/home/alex/.config/systemd/user/babel-archive.service; linked; preset: enabled)
     Active: inactive (dead) since Mon 2026-08-31 03:05:01 UTC; 29min ago
    Process: 3095093 ExecStart=/nix/store/...-babel-archive-push/bin/babel-archive-push (code=exited, status=0/SUCCESS)
        CPU: 2min 2.946s

Aug 31 03:02:28 ubuntu-4gb-nbg1-1 systemd[1105]: Starting babel-archive.service...
Aug 31 03:02:30 ubuntu-4gb-nbg1-1 babel-archive-push[3095103]: backing up 4 roots as host workstation-linux
Aug 31 03:05:01 ubuntu-4gb-nbg1-1 babel-archive-push[3095093]: babel-archive-push: snapshot 87dc5f89...b56428, 843 session(s) published
Aug 31 03:05:01 ubuntu-4gb-nbg1-1 systemd[1105]: Finished babel-archive.service.

$ cat ~/.local/state/babel/last-success
2026-08-31T03:05:01Z
```

**Exercised 2026-08-31 on `workstation-linux`** (observation only — the push
itself is the timer's own 03:02:28 run, not an invocation from this drill).

---

## 2. Repository recovery

Two independent paths restore archived bytes. The second exists because the
first must never become load-bearing: *archive recovery does not depend on
PostgreSQL — or on Babel.*

### 2.1 Find what to restore

`sessions list --host` reads only the snapshot's file listing; nothing is
downloaded to enumerate.

```
$ babel sessions list --host alex-x86_64-linux-wsl
HARNESS  SOURCE ID                                                           SIZE     MODIFIED  TITLE  TITLE FROM  WORKSPACE  GRADE
omp      -tmp/2026-07-19T19-06-49-938Z_019f7bc6-...                          329315   -         -      -           -          -
omp      -tmp/2026-08-20T11-29-15-084Z_01a01eee-...                          45645    -         -      -           -          -
omp      -tmp/2026-08-20T20-47-52-738Z_01a020ee-...                          13678    -         -      -           -          -
omp      -tmp/2026-08-25T19-17-51-909Z_01a03a5b-...                          506605   -         -      -           -          -
omp      -tmp/2026-08-27T10-18-24-291Z_01a042ba-...                          9045630  -         -      -           -          -

exit 0, 3.4s
```

Note the host: this machine listed and then restored a session belonging to a
*different* machine, which it never held locally. That is the cross-machine
recovery case, not the easy one.

### 2.2 Path A — recovery through Babel

```
$ babel sessions fetch '-tmp/2026-08-20T20-47-52-738Z_01a020ee-23a2-7000-9792-fe3ff53f9009' \
    --host alex-x86_64-linux-wsl --json
{
  "selector": "omp/-tmp/2026-08-20T20-47-52-738Z_01a020ee-...",
  "snapshot_id": "3dd670960b949c58daec2444df007b56a805ef1cbe616b61f92a6f19a8e85b18",
  "snapshot_short_id": "3dd67096",
  "snapshot_time": "2026-08-31T03:01:23Z",
  "target": "/home/alex/.local/share/babel/sessions/omp--tmp-2026-08-20T.../3dd67096",
  "files": 2,
  "bytes": 26333,
  "already_present": false
}

real  0m15.041s   exit 0
```

A selector beginning with `-` is read as a selector, not a flag — every OMP and
Claude Code source id begins with one, because they encode a workspace path.

Restores are idempotent. Running the identical command again downloads nothing
and says so:

```
  "files": 2,
  "bytes": 26333,
  "already_present": true
```

Digests of the restored closure:

```
368fd244cb26f7e6bfed99d356bec06f8a0651a7f616902e230564a30643c81b  ...01a020ee-....jsonl
65f0eb680f118f82172979fa6bb7432ff40423867fc9958305911824d693a421  ...01a020ee-.../__advisor.jsonl
```

### 2.3 Path B — recovery with restic alone

This is the path that must work when PostgreSQL is gone, when Babel will not
build, and when the only surviving assets are the repository password and the
object-store credentials. It uses no Babel code.

The credentials live in `storage.json`; move them into the environment without
printing them:

```sh
cfg="$HOME/.config/babel/storage.json"
export RESTIC_REPOSITORY="$(jq -r .repository        "$cfg")"
export RESTIC_PASSWORD_FILE="$(jq -r .password_file  "$cfg")"
export AWS_ACCESS_KEY_ID="$(jq -r .repository_store.access_key_id     "$cfg")"
export AWS_SECRET_ACCESS_KEY="$(jq -r .repository_store.secret_access_key "$cfg")"
```

If `storage.json` is also gone, recover the existing clan custody and reapply
placement as in §3–4. The repository password and provider inputs must survive
independently of this machine; a new password cannot open the old repository.

```
$ restic snapshots --no-lock --host alex-x86_64-linux-wsl
ID        Time                 Host                   Tags   Paths                           Size
---------------------------------------------------------------------------------------------------
c78f6a67  2026-08-31 00:57:48  alex-x86_64-linux-wsl  babel  /home/alex/.claude              44.395 MiB
                                                             /home/alex/.codex
                                                             /home/alex/.omp/agent/sessions
21c4db38  2026-08-31 01:01:27  alex-x86_64-linux-wsl  babel  (same three roots)              44.395 MiB
a3842fd3  2026-08-31 02:01:24  alex-x86_64-linux-wsl  babel  (same three roots)              44.395 MiB
3dd67096  2026-08-31 03:01:23  alex-x86_64-linux-wsl  babel  (same three roots)              44.395 MiB
---------------------------------------------------------------------------------------------------
4 snapshots

real  0m1.562s   exit 0
```

Restore the same subtree to a scratch directory:

```
$ restic restore 3dd67096 --no-lock \
    --include '/home/alex/.omp/agent/sessions/-tmp/2026-08-20T20-47-52-738Z_01a020ee-*' \
    --target /tmp/babel-restore-drill.lpSTF1
restoring snapshot 3dd67096 of [/home/alex/.claude /home/alex/.codex /home/alex/.omp/agent/sessions]
  at 2026-08-31 03:01:23.612665256 +0000 UTC by alex@alex-x86_64-linux-wsl to /tmp/babel-restore-drill.lpSTF1
Summary: Restored 9 / 3 files/dirs (25.716 KiB / 25.716 KiB) in 0:00

real  0m1.895s   exit 0
```

**Verify — the two paths agree byte for byte:**

```
$ diff -r /home/alex/.local/share/babel/sessions/omp--tmp-.../3dd67096 /tmp/babel-restore-drill.lpSTF1
exit 0

$ sha256sum (restic-restored tree)
368fd244cb26f7e6bfed99d356bec06f8a0651a7f616902e230564a30643c81b  ...01a020ee-....jsonl
65f0eb680f118f82172979fa6bb7432ff40423867fc9958305911824d693a421  ...01a020ee-.../__advisor.jsonl
```

Identical to §2.2. `restic` plus the repository password reproduced exactly what
Babel reproduced, with no catalog consulted.

Repository scale, for restore planning:

```
$ restic stats --no-lock --mode raw-data
     Snapshots processed:  44
        Total Blob Count:  50631
 Total Uncompressed Size:  23.466 GiB
              Total Size:  5.687 GiB
       Compression Ratio:  4.13x

real  0m2.985s   exit 0
```

**Exercised 2026-08-31 on `workstation-linux`.**

### 2.4 Integrity check, and a real failure found while drilling

`babel archive verify` wraps `restic check`. Tonight it **failed**, and the
reason is worth recording because it is the failure an operator will actually
meet:

```
$ babel archive verify
FAILED (structure)
babel: verify repository: restic check: exit status 11: unable to create lock in backend:
repository is already locked by PID 2841104 on ubuntu-4gb-nbg1-1 by alex (UID 1000, GID 1000);
lock was created at 2026-08-31 00:44:10 (2h48m28s ago); storage ID 5f86a86f;
the `unlock` command can be used to remove stale locks

real  0m1.002s   exit 1
```

Two shared locks are stranded in the repository, both from processes that no
longer exist:

```
$ restic list locks --no-lock
5f86a86f9dde08f7eabe98f64815594b6f00669026f4c67355d34c6ccfbd1795
e64f7be2f7f0ee64205e71560c37bab924bef6d6cb4c9f174a50946e17186754

$ restic cat lock 5f86a86f...  ->  {"time":"2026-08-31T00:44:10Z","exclusive":false,"hostname":"ubuntu-4gb-nbg1-1","pid":2841104}
$ restic cat lock e64f7be2...  ->  {"time":"2026-08-31T00:49:09Z","exclusive":false,"hostname":"ubuntu-4gb-nbg1-1","pid":2858765}

$ ps -p 2841104  ->  dead
$ ps -p 2858765  ->  dead
```

A stale *shared* lock does not endanger data and does not block restores —
§2.2 and §2.3 both succeeded while these were present. It blocks `restic check`,
which wants the repository quiescent.

The read-only way through, which is what an operator should reach for first
because it diagnoses without mutating:

```
$ restic check --no-lock
using temporary cache in /tmp/restic-check-cache-3282174982
load indexes
check all packs
check snapshots, trees and blobs
[0:00] 100.00%  44 / 44 snapshots
no errors were found

real  0m2.235s   exit 0
```

**The repository is structurally sound: 44 snapshots, no errors.** Add
`--read-data` to re-read and re-hash every pack; that costs a full download of
5.687 GiB and is the deep check behind `babel archive verify --deep`.

> **OPERATOR STEP — clear the two stale locks.** `babel archive unlock` is the
> verb for this, and it is deliberately one you type: no timer, no conductor
> duty and no other Babel path invokes it. Babel supplies the plumbing this
> drill originally had to assemble by hand — repository locator, password file
> and object-store keys, read from `storage.json` exactly as `archive verify`
> reads them — so clearing a lock needs no environment block at all:
>
> ```sh
> babel archive unlock   # lists every lock with its staleness reasoning,
>                        # then removes the stale shared ones
> babel archive verify   # success looks like: ok (structure), exit 0
> ```
>
> Both locks above are shared and both holders are dead, which is precisely
> what the default removes. Every run prints the listing before removing
> anything, states the judgement it reached on each lock and the reason, and
> refuses the PID-liveness claim for a lock naming another host — the same
> check made by hand above, made by the command instead. A run that removes
> nothing exits 0 and says so.
>
> A lock that is not both stale and shared is removed only when the run names
> it: `babel archive unlock --remove LOCKID`, with an id from that listing. An
> exclusive lock is always in that category, and restic's stale removal takes
> every stale lock at once and cannot exclude one, so Babel refuses the whole
> run and names the lock rather than removing it quietly. That granularity is
> restic's: measured against restic 0.19.1, plain `restic unlock` **does**
> remove a stale exclusive lock, so the earlier note here — that `--remove-all`
> is what an exclusive lock needs — was wrong about restic. `--remove-all` is
> what a lock that is not *stale* needs, and it removes every lock in the
> repository.
>
> The four-line `jq` block that opens §2.3 remains the prerequisite for every
> **direct** `restic` command in this runbook, including a bare `restic unlock`.
> That is the recovery path which uses no Babel code, and it is the one to reach
> for when Babel will not build; it is no longer the path for clearing a lock on
> a working machine.
>
> Never run `restic forget` or `restic prune`: Babel's retention contract is
> append-only and it ships no code path that deletes a snapshot. `babel archive
> unlock` removes coordination state and never archived data, so it leaves that
> contract exactly where it was.

---

## 3. Restic repository password custody

**No provider can reissue this password.** Losing every copy makes the existing
repository unreadable. The same rule applies independently to each Phase B
payload key (§8.1). A working machine is not the only backup of either.

### Current source contract

The following sources were read at dotfiles revision
**`f2a4749eab77ac859b85354142e42c78ac6d8c80` (2026-09-06)**. They establish
declared behavior, not proof that any fleet member has applied it:

| Contract | Pinned source |
| --- | --- |
| Shared custody; existing password is prompted, never minted; existing rings survive rotation | [`modules/shared/babel-archive.nix:1–31`](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L1-L31) |
| sops placement, owner/group and 0600 mode | [`modules/shared/babel-archive.nix:44–75`](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L44-L75) |
| Shared `babel-custody`: three secret, undeployed inputs; deployed complete ring; prompt validation | [`modules/shared/babel-archive.nix:77–178`](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L77-L178) |
| Per-machine schema-2 shared configuration, registry host/instance identity, and Home Manager links | [`modules/shared/babel-archive.nix:180–243`](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/modules/shared/babel-archive.nix#L180-L243) |
| Operator-device generation followed by apply; no vault/provider session on target | [`fleet/provisioning.json:39–44`](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/fleet/provisioning.json#L39-L44) |
| Input readiness before success-stamp health | [`pkgs/atyrode/lib/doctor.sh:1262–1313`](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/pkgs/atyrode/lib/doctor.sh#L1262-L1313) |

`babel-custody` holds `repository-password`, `cellar-env.json`,
`catalog-env.json`, and `payload-keys.json`. The first three are generation
inputs, not separately deployed custody files; their required values feed the
machine's storage document and password file. The whole ring is deployed.
Babel does not retrieve provider credentials or generate managed-fleet storage
configuration. Identity comes from the clan machine registry, not a flag or
the kernel hostname.

> **OPERATOR STEP — preserve and recover custody (not executed).**
> **Prerequisites:** an authorized operator device able to decrypt the clan
> vars, access to the encrypted dotfiles history, and an independent secure
> backup destination. Preserve the encrypted vars and the means to decrypt them
> off the fleet; test access from a recovery device without printing secret
> values. If custody is missing, recover the existing password and complete ring
> from a surviving authorized copy or backup before generating derived files.
> Do not replace a password or ring to repair missing placement.
> **Success:** the recovery device can securely recover the existing custody
> values, including all historical key ids, without relying on the failed host.
> Repository recovery remains possible with restic alone (§2); Phase B recovery
> additionally needs its catalog/object backups and every sealing key (§8).

**Historical evidence, 2026-08-31 only:** the old local password file and
`storage.json` were observed with mode 0600; `storage status` reported the
password present and secure. The former vault was observed locked. No live
secret retrieval or custody export was exercised. These observations do not
establish current clan-var custody or activation.

---

## 4. `storage.json` recovery

> **OPERATOR STEP — generate missing derived configuration (not executed).**
> **Prerequisites:** a current dotfiles checkout on an authorized operator
> device, the intended registered host, an existing archive repository, and
> recoverable shared custody from §3. Inspect the declared/generated var status
> without displaying contents first. If the existing vars are already complete,
> skip generation and apply them; a dangling link is not a reason to regenerate.
>
> For a registered machine needing generated files, on the operator device:
>
> ```sh
> clan vars generate <host>
> ```
>
> If shared custody prompts appear, supply the existing repository password,
> whole Cellar and catalog environment JSON documents, and **the entire existing
> payload ring** through the hidden prompts. Never leave the ring prompt empty
> for this existing deployment: the generator's empty-input branch mints a new
> ring and cannot recover old ciphertext. Stop and recover custody if it is
> unavailable. Review and commit only the encrypted var update in dotfiles,
> then make that revision available to the target.
> **Success:** the intended host's derived storage/password vars and shared
> ring are present in encrypted custody; no secret value enters Git plaintext,
> argv, shell history, logs, or an ordinary temporary file.

> **OPERATOR STEP — apply existing custody (not executed).**
> **Prerequisites:** the target is the registered host, has its decryption
> identity, and can apply the reviewed dotfiles revision containing its vars.
> Run `atyrode apply` on that machine. Do not hand-edit managed config links or
> invoke Babel's standalone configuration writer on them.
> **Success:** sops-nix places these readable, nonempty 0600 files for the archive
> account, and Home Manager links the two config documents:
>
> | Babel-facing path | Placed target |
> | --- | --- |
> | `~/.config/babel/storage.json` | `/run/secrets/vars/babel-archive/storage.json` |
> | `~/.config/babel/payload-keys.json` | `/run/secrets/vars/babel-custody/payload-keys.json` |
> | `password_file` inside storage | `/run/secrets/vars/babel-archive/repository-password` |
>
> Apply also restores declarative scheduling (§7). Failed generation or
> activation is not success: do not remove old custody or manually arm a job
> with partial inputs. Diagnose placement and retain a usable generation (§7.3).
> This is not a claim that activation is an atomic rollback of all secret files.

> **OPERATOR STEP — read-only readiness checks (not executed on current fleet).**
> **Prerequisites:** the intended generation has been applied; run as the
> archive account. These commands diagnose, not generate, activate, or publish:
>
> ```sh
> babel storage status
> babel storage verify
> atyrode doctor provisioning --json
> ```
>
> **Success:** status identifies shared mode and the registry host/instance,
> with the placed password present and secure; verify reports negotiated TLS,
> compatible schema and no pending migration. Inspect the `babel-archive`
> entry in doctor's `surfaces`, not just exit status (this diagnostic can exit
> zero while degraded). Missing, dangling, unreadable or empty storage/ring/
> password files produce `archive-input-unavailable`; a storage document without
> an absolute password path produces `archive-config-invalid`. A recent stamp
> cannot mask either. With usable inputs, `never-succeeded` is expected before
> first publication; `archive-stale` means the parsed success time is over
> 48 hours old; `ok` reports a prior successful archive, not live remote health.
> Neither doctor nor link existence validates that all historical keys are
> present: §8.1's cross-host opening check covers that separately.

### Historical read-only verification — 2026-08-31

The paths and identities in this captured output belong to the old deployment,
not the current sops placement. Both commands were read-only:

```
$ babel storage status
path                  /home/alex/.config/babel/storage.json
configured            yes
mode                  shared
repository            s3:https://<cellar-host>/<bucket>/babel/v1
password file         /home/alex/.config/babel/repository-password
password file exists  yes
password file secure  yes
host id               workstation-linux
deployment id         babel-prod
instance id           workstation-linux
catalog endpoint      <catalog-host>:<port>/<db>
catalog user          <catalog-user>
catalog tls mode      require

exit 0
```

```
$ babel storage verify
endpoint                  <catalog-host>:<port>/<db>
tls mode                  require
tls active                yes
tls protocol              TLSv1.3
schema version            1
schema compatible         yes
pending migration         no
credential                <catalog-user>
privilege observed        ddl
role separation observed  no
note: one credential serves this deployment, so no database-level control evicts a single instance;
      fleet-wide credential rotation and repository-password custody are the controls

real  0m0.388s   exit 0
```

TLS is reported as *observed* (`TLSv1.3` actually negotiated), not as
configured, and the privilege is reported as *observed* rather than assumed.

**Exercised 2026-08-31 on `workstation-linux`:** `storage status` and
`storage verify` only. Current generation and apply remain operator steps above.

---

## 5. Coordinated PostgreSQL catalog backup

**The catalog is not the authority and does not have to be.** The restic
repository is. Every catalog row for a host is rederivable from the repository's
snapshot list, which is what makes "coordinated backup" a much smaller problem
than it first appears: there is no cross-store consistency requirement between
PostgreSQL and Cellar to preserve, because PostgreSQL never references a
snapshot restic did not report committed.

**Managed backups.** Clever Cloud takes them for the `babel-catalog-prod`
add-on. Resolve the add-on by name — ids are never recorded — and list:

```sh
clever addon list --org Tyrode --format json     # -> addon_<uuid> for babel-catalog-prod
clever database backups addon_<uuid>
```

```
$ clever database backups addon_<uuid>
BACKUP ID                             CREATION DATE                STATUS
0af47a2d-178e-46fc-9937-8fc5e2e1f9d4  2026-08-30T01:15:39.896901Z  Done
f60291ca-bb46-49c2-af94-0743825a5673  2026-08-31T02:39:10.317788Z  Done

exit 0
```

Two daily provider backups, both `Done`, the most recent 56 minutes before this
drill. `clever database backups download` retrieves one.

**Recovery without a provider backup.** Restore the catalog from the repository
instead:

```sh
babel storage migrate                    # bring an empty database to schema version 1
# then either: let each host's next `archive push` register and reconcile itself
# (the ordinary recovery, and the one the acceptance suite exercises), or:
babel storage rebuild --host HOST --yes  # rebuild one host's rows from the snapshot list
```

Know what `storage rebuild` costs before reaching for it. It **writes to the
catalog** — it discards what the catalog held for that host — so it was
deliberately not run by this read-only drill. What comes back is what a listing
can support: snapshot identity, ordering rederived from restic's recorded times,
and restic's counts. Session rows cannot be rebuilt from a listing, because
their sizes and counts are read from the sessions themselves, so rebuilt
snapshots arrive `catalog-pending` and session titles, workspaces, and
continuation grades return only with the owning host's next push. The repository
is never touched and no snapshot it still reports is ever dropped.

**Verify.** Catalog reachability and drift are visible in the §1
`archive status` output: `catalog reachable yes`, and the honest counts
`uncatalogued snapshots 1` / `catalog-pending snapshots 2`. Those are not
failures. An uncatalogued snapshot is one restic holds that the catalog has not
adopted yet; the next push records it. A `catalog-pending` snapshot was adopted
from the repository list after a PostgreSQL outage, so its record of which
sessions it held was never written and is not derivable from a listing — no
shipped command resolves it, which is exactly why `archive status` reports the
count instead of presenting a pending action.

**Exercised 2026-08-31 on `workstation-linux`** (backup listing and catalog
health live; `storage rebuild` documented but not run, because it mutates the
catalog).

---

## 6. Manual bootstrap of a new machine

**Historical evidence, 2026-08-31:** `alex-x86_64-linux-wsl` joined the fleet
under the then-current deployment. Its first snapshot and subsequent hourly
snapshots were observed; this does not exercise today's clan-var bootstrap:

```
$ restic snapshots --no-lock --host alex-x86_64-linux-wsl
c78f6a67  2026-08-31 00:57:48   <- first publication: the bootstrap
21c4db38  2026-08-31 01:01:27   <- hourly timer from here on
a3842fd3  2026-08-31 02:01:24
3dd67096  2026-08-31 03:01:23
```

Confirmed from the other side by `archive fleet` in §1: `current`, cadence
`1h (observed)`, 4 snapshots.

> **OPERATOR STEP — join a registered machine (not executed).**
> **Prerequisites:** the host is registered in current dotfiles, authorized to
> read the entire shared corpus, and the repository already exists. Complete
> §3–4's custody, generation (only if needed), apply, and read-only checks.
> **Success:** storage uses the registered host/instance, inputs are usable,
> catalog verification passes, and the platform scheduler is present (§7).
> Registry identity must remain stable for a machine that has published:
> changing it creates a different host history, not a rename of old snapshots.

**Do not run `babel archive init` on a new machine.** Repository creation is a
one-time operator act for the whole deployment. `archive push` refuses to create
a repository precisely so that a mistyped locator fails loudly instead of
silently becoming a second, empty archive, and concurrent creation corrupts a
fresh one.

> **OPERATOR STEP — first publication (not executed).**
> **Prerequisites:** §4 readiness passes, the correct existing repository is
> selected, source sessions exist, and publication is authorized. Run the common
> `babel-archive-push` wrapper on either platform (on Linux, alternatively
> `systemctl --user start babel-archive.service`). This writes the archive and
> may reconcile/publish catalog state; it is not a read-only check.
> **Success:** the wrapper reports a complete snapshot and updates `last-success`.
> Then read `babel archive status` and
> `babel archive fleet --expect <comma-separated-registry-hosts>`: the host has a
> new published snapshot and is `current`. Confirm it from another authorized
> machine. A successful no-op with no source roots is not proof of backup.

---

## 7. Timer enablement and rollback

### 7.1 Enablement

Current source declares Linux's hourly persistent timer with 10-minute jitter,
the storage-document start condition, and restarting that timer after activation
([`checks/atyrode/babel-archive.nix:80–123`](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/checks/atyrode/babel-archive.nix#L80-L123)).
The condition is checked at timer start, not continuously. It gates only document
existence; do not equate it with full storage health. §4's checks and §6's first
publication establish readiness and actual archive outcome separately.

**Historical unit and timer observations, 2026-08-31:**

```
$ systemctl --user cat babel-archive.timer
# /home/alex/.config/systemd/user/babel-archive.timer -> /nix/store/...-babel-archive.timer
[Install]
WantedBy=timers.target

[Timer]
OnCalendar=hourly
Persistent=true
RandomizedDelaySec=10m

[Unit]
ConditionPathExists=/home/alex/.config/babel/storage.json
Description=Hourly Babel archive of agent session histories
```

`Persistent=true` so a machine that was asleep at the top of the hour runs the
missed archive once it is back, instead of silently dropping a window of session
history. `RandomizedDelaySec=10m` keeps a growing fleet from arriving at the
object store together.

```
$ systemctl --user status babel-archive.timer
● babel-archive.timer - Hourly Babel archive of agent session histories
     Loaded: loaded (/home/alex/.config/systemd/user/babel-archive.timer; enabled; preset: enabled)
     Active: active (waiting) since Sun 2026-08-30 22:59:13 UTC; 4h 29min ago
    Trigger: Mon 2026-08-31 04:06:38 UTC; 37min left
   Triggers: ● babel-archive.service

exit 0

$ systemctl --user list-timers 'babel*'
NEXT                        LEFT   LAST                        PASSED    UNIT                 ACTIVATES
Mon 2026-08-31 04:06:38 UTC 37min  Mon 2026-08-31 03:02:28 UTC 26min ago babel-archive.timer  babel-archive.service
```

Darwin instead declares enabled `launchd.agents.babel-archive`, the same wrapper,
`StartInterval = 3600`, and `RunAtLoad = true`
([`checks/atyrode/babel-archive.nix:197–209`](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/checks/atyrode/babel-archive.nix#L197-L209)).
There is no systemd condition on macOS: the wrapper checks the storage document.
Do not infer Linux's persistent catch-up or jitter semantics from launchd's
interval. The wrapper never initializes a repository and earns the success stamp
from a nonempty snapshot id with no incomplete result
([same check, lines 58–78](https://github.com/atyrode/dotfiles/blob/f2a4749eab77ac859b85354142e42c78ac6d8c80/checks/atyrode/babel-archive.nix#L58-L78)).

> **OPERATOR STEP — inspect scheduling (read-only; not executed on current fleet).**
> **Prerequisites:** §4 activation completed in the archive user's session.
> On Linux, inspect `systemctl --user status babel-archive.timer` and
> `systemctl --user list-timers 'babel*'`; success is an active waiting timer
> with a next trigger and the expected service. On Darwin, inspect the installed
> Home Manager LaunchAgent plist for `babel-archive`, then use `launchctl print`
> with its actual `gui/<uid>/<Label>` service target. Success is a loaded job
> with the wrapper, 3600-second interval and RunAtLoad configuration. No Mac
> runtime observation was made here. On either platform, scheduling alone is
> insufficient: §6 must show the actual published snapshot.

### 7.2 Stopping the archive

> **OPERATOR STEP — suspend scheduling (not executed).**
> **Prerequisites:** authorized maintenance and awareness that stopping a
> scheduler does not cancel an already running archive. On Linux run
> `systemctl --user stop babel-archive.timer` for this session, or
> `systemctl --user disable --now babel-archive.timer`. On Darwin use
> `launchctl bootout` with the actual service target inspected in §7.1.
> **Success:** the timer is inactive or the launchd job is unloaded; separately
> inspect the service/job before assuming an in-flight push ended. These are
> temporary local changes: the next apply restores declarative scheduling.
> For a durable suspension, change the owning dotfiles scheduler declaration
> through review and apply it; success is absence of scheduled starts after apply.

### 7.3 Rollback

**There is no longer a legacy backup to roll back to, and that is a deliberate
end state rather than an omission.** Babel replaced an rclone-crypt mirror of
the same trees. That mirror is retired: `rclone` is gone from `PATH`, and
`babel-archive.timer` is the only backup timer on this machine.

```
$ command -v rclone
rclone: NOT on PATH (legacy crypt archive retired)

$ systemctl --user list-timers --all | grep -iE 'rclone|backup|archive'
Mon 2026-08-31 04:06:38 UTC 34min Mon 2026-08-31 03:02:28 UTC 30min ago babel-archive.timer  babel-archive.service
```

So rollback means reverting the *deployment*, not switching to a parallel
backup system:

```
$ home-manager generations
2026-08-30 23:29 : id 185 -> /nix/store/k4rs3v39n71m9ax389vk7vb9a4yawwda-home-manager-generation (current)
2026-08-30 23:08 : id 184 -> /nix/store/4nn4mvp4l7l0zxfn6gia0vncyw21xdrl-home-manager-generation
2026-08-30 22:59 : id 183 -> /nix/store/x7pdq3rjjdps70lxbb28hh76f7k0fj0k-home-manager-generation
2026-08-29 20:58 : id 182 -> /nix/store/wskj3hxsyxd344sw7zngybv4ypsy8ybn-home-manager-generation
...
exit 0
```

> **OPERATOR STEP — rollback deployment (not executed).**
> **Prerequisites:** authorized maintenance, independent recoverable custody
> (§3), and a known-good generation compatible with the current complete ring.
> Inspect available generations using the owning platform's deployment tools.
> A Home Manager-only rollback activates the chosen generation's `activate`
> script; it is not a NixOS/nix-darwin system or sops placement rollback.
> Choose the matching system rollback when those components changed.
> **Success:** the selected generation's package, links and scheduler are
> effective, and §4 checks pass before §6 publication is attempted.

**Rollback boundaries.** Which timer/service/launchd agent and wrapper remain
depends on the chosen generation; rollback does not inherently remove them.
Home Manager links or sops runtime files may change or disappear, so do not
promise that old `~/.config` files survive. Preserve the encrypted custody and
every historical payload key independently; never roll custody back to an
incomplete ring. Deployment rollback must not delete the repository, catalog,
published snapshots or pending local durable state. Already published snapshots
remain restorable through §2, even without Babel or PostgreSQL. No legacy mirror
is reintroduced.

> **OPERATOR STEP — roll forward (not executed).**
> **Prerequisites:** corrected reviewed configuration and complete current
> custody. Run `atyrode apply`, then §4 and §7.1 checks.
> **Success:** usable placement and intended scheduling return; §6 publication
> separately proves the machine archives again.

**Exercised 2026-08-31 on `workstation-linux`** (timer state, unit definition,
legacy-backup absence, and available rollback generations observed live; no
generation was activated, since that would mutate this machine's deployment).

---
## 8. Phase B publication: payload keys and `babel sync`

**This section is about the one thing in Babel that exists nowhere else.** A
snapshot is rederivable from the repository and a Phase A catalog row is
rederivable from the snapshot list, which is what makes §5 a small problem. A
hypothesis, a finding, an operator's decision or a run receipt is rederivable
from nothing: it exists in `durable.db` on the machine that produced it until
it reaches the shared catalog, and `durable.db` is deliberately not under the
hourly archive roots. Until it is published, a dead workstation disk loses it.

`babel sync` is what publishes it, and every Phase B write already attempts the
same publication inline the moment it commits locally.

### 8.1 The payload key document

Phase B payloads are sealed before they leave the process (SPEC.md §9, decision
55, `internal/envelope`), so publication needs a key. It lives in its own
mode-0600 document beside `storage.json`:

```
$XDG_CONFIG_HOME/babel/payload-keys.json
```

It is a **separate document from `storage.json` on purpose**, for two reasons.
`config_schema` 2 is frozen (SPEC.md §14) after running against real Cellar and
real managed PostgreSQL, so a new field in it is a schema change rather than an
addition. And the lifecycles differ: a repository locator and a database
credential are current values an operator edits, while a key document is a
*history* — every sealed object ever written under a retired key still needs
that key to open.

On managed machines this path is a Home Manager out-of-store link to
`/run/secrets/vars/babel-custody/payload-keys.json` (§3 sources), not a local
document to overwrite. The generator validates and copies the supplied whole
ring. It does **not** union it with extra keys on a target machine; the operator
must preserve those in shared custody before switching that machine to the
managed link. A new active key alone cannot open historical objects.

> **OPERATOR STEP — recover/distribute an existing ring (not executed).**
> **Prerequisites:** an authorized operator device, access to encrypted clan
> custody and surviving authorized ring copies, and knowledge of the intended
> deployment. Securely reconcile the complete key history: preserve every key,
> refuse conflicting material under the same id, and resolve any conflict from
> authoritative backups before proceeding. Never print the ring or put it in
> argv, shell history, logs or an ordinary temporary file. Use clan's var
> get/set workflow for the existing shared `babel-custody/payload-keys.json`
> value (see §3, module lines 21–31), not regeneration. Commit the encrypted
> update, make it available to each authorized machine, and run `atyrode apply`.
> If encrypted custody is already complete and only placement is missing, skip
> the var edit and apply that existing value.
> **Success:** §4 reports usable links/inputs and, on a second authorized host,
> `babel fleet records` can open known committed records sealed under both the
> active and historical key ids. Compare ids and opening outcomes privately;
> do not publish record contents or key material as diagnostic evidence.

> **OPERATOR STEP — deliberate append-only rotation (not executed).**
> **Prerequisites:** an independently recoverable complete ring, authorized
> rotation, and access to every intended fleet member. The source-prescribed
> sequence is `clan vars get`, append one securely generated 32-byte
> standard-base64 key with a unique id, set `active_key_id` to that id, then
> `clan vars set` for the same shared ring. Use protected secret input/output,
> not a terminal transcript. Retain every old entry unchanged. Review/commit
> encrypted custody and apply to every authorized host before relying on
> cross-host opening of newly sealed records.
> **Success:** every recipient has the full history and new active id; existing
> committed records still open and a deliberately authorized new publication
> can be opened by another host. Missing placement never justifies rotation.

The module can mint a ring when its prompt is empty, but that branch is only
for a genuinely new deployment that has sealed nothing. It is **not** part of
this existing deployment's recovery or bootstrap procedure. Losing every copy
of a key leaves its ciphertext permanently unreadable; a coordinated PostgreSQL
and Cellar backup without the ring restores ciphertext, not readable records.
Every fully authorized instance can decrypt the shared corpus, so a machine not
authorized for that blast radius must not receive this custody.

**Historical scratch evidence, 2026-08-31 only:** the former standalone
configuration handoff and stubbed vault checks exercised delivery, repeat
delivery, retaining omitted keys, refusing conflicting material, refusing
replacement generation, and an upload merge. No live vault ring retrieval or
upload was executed. Those results describe the retired handoff, not today's
clan generator, which copies the supplied ring verbatim. No current generation,
placement, cross-host opening, or rotation was exercised for this update.

### 8.2 What is pending, and why

Every durable Phase B record is born `pending-sync` and stays visibly pending
until its rows and its objects have both committed remotely. `babel sync`
reports both halves:

```
$ babel sync
committed 3 hypotheses, 1 finding, 1 receipt
3 runs committed, 5 objects written
nothing pending
```

Three states are worth telling apart in that report:

- **pending, in a declared closure** — the records are ready and the backend was
  unreachable. The next `babel sync` finishes them, and nothing is lost by
  waiting.
- **undeclared** — records of a run that has not finished. A run's record count
  is fixed when its closure is declared and is immutable in the catalog
  (`migrations/0003`), so a closure may not be declared while it can still grow.
  These publish as soon as that run ends, and resuming an interrupted
  exploration under the same run id is what ends it. They are never dropped.
- **local** — this build has no shared publication configured (local mode, no
  catalog, or no payload key document). Nothing is owed to anybody, and the
  report says so rather than implying a sync that nothing will perform.

**Publication never blocks a write.** An unreachable catalog, a refused object
write, a missing key: all of them leave the record durable and pending, emit one
diagnostic line, and let the command that produced the record succeed. That is
SPEC.md §6.5's ordering, and it is the only arrangement under which an outage
cannot destroy analysis output.

### 8.3 Reconcile after a push

`babel archive push` runs the Phase B sync as its final step, after the Phase A
catalog reconcile. It is **non-fatal**: a failure there never changes the push's
exit code or its reported catalog state, because the snapshot is already durable
and the Phase B records are already durable locally. On an hourly timer that
makes the backlog self-draining without a second schedule.

### 8.4 When a record will not publish

`babel sync` names each closure that failed and why. Two causes are
misconfiguration rather than outage, and neither resolves itself:

- **the instance is not registered.** A Phase B run row references the
  deployment and the instance, and those rows are written by the first
  `babel archive push`. A machine that has never pushed cannot publish
  analysis. Run a push first.
- **a pending migration.** `babel storage verify` reports it; the catalog needs
  `migrations/0003`, which is part of schema version 1 and applied by
  `babel storage migrate`.

An unreachable PostgreSQL or Cellar is neither of those and needs nothing but a
later `babel sync`.

**Not exercised against the real deployment.** Everything in this section is
proven against a throwaway PostgreSQL and a local-directory object store, which
is what the test suite drives. Publication against the real Cellar endpoint and
the real managed catalog is an operator-gated step; see *What remains
operator-gated* below.

---

## 9. Reading the fleet's analysis

Phase B records are globally durable, so every authorized instance can read what every other
machine committed. Two commands expose it, and they answer different questions.

**Preconditions.** Shared mode configured (`~/.config/babel/storage.json`, §4) and payload keys
placed (§8.1). Without payload keys the catalog is still readable and every plaintext row still
renders, but no record's content can be opened, so both commands report that rather than printing
a wall of unopenable rows.

### 9.1 What the fleet holds

```
$ babel fleet records --limit 5
```

One row per committed record, newest commit first. `HOST` is the machine that produced it,
`SYNC` is whether it is globally reviewable, and `SUMMARY` is the record's own first line,
decrypted locally.

Three values appear under `SYNC` and they are not interchangeable:

| Value | Meaning |
| --- | --- |
| `committed` | The record's rows and objects are both durable remotely. Globally reviewable. |
| `pending-sync` | Staged but not globally committed. Not reviewable yet; `babel sync` finishes it. |
| `local` | No remote row and nothing claims it is owed. Either this machine is in local mode, or the record was never staged. |

`local` is deliberately not spelled `pending-sync`. A record marked pending is a promise that
something will carry it; for a `local` record nothing will, and rendering it as pending would be
the one lie the visible-staging requirement must not tell.

`HOST` reads `unattributed` when the record's origin instance has no registered host. That is a
real state, not an error: an instance that last registered before the `instances.host_id` column
existed has no host to attribute, and the remedy is one push from the owning machine. Babel will
not guess — a record filed under the wrong machine is invisible, and a gap is not.

`--host alex-x86_64-linux-wsl` narrows to one machine, repeatable. `--kind` narrows to a record
type. `--pending` additionally shows staged records, which is how an operator answers "why is my
hypothesis not visible on the other machine".

### 9.2 Making the other hosts' work searchable here

```
$ babel fleet ingest
```

This is what stops two conductors on two machines from silently duplicating one another. It
fetches every host's committed records, decrypts them on this machine, and indexes them into the
local retrieval cache, so self-retrieval and dedup answer across the fleet instead of across one
workstation.

**It writes only to the cache.** Nothing it does touches `durable.db`, and that is the whole
design: a remote record is never copied into the local durable store, so it can never be
republished by the machine that read it, and losing the index costs a re-index and never data.

`--rebuild` drops every remote partition and rebuilds it from the catalog. It is safe to run at
any time and it does not touch this machine's own analysis.

The report names, per host, what changed, plus three totals worth reading:

* **unattributed** — committed records skipped because their origin instance has no registered
  host, so there is no machine to file them under. They remain readable in `fleet records`; they
  are just not searchable per-host. Expect this to fall to zero as each machine pushes again.
* **unopened** — records this instance could not read, one line each with the reason: a key this
  machine does not hold, a payload from a newer build, or an object the store would not return.
  Each costs one record and never the ingest, so one host publishing something this binary cannot
  open never makes the rest of the fleet unreadable.
* **forgotten** — hosts whose rows were dropped because the catalog no longer reports records for
  them. A cache eviction; the records are still in PostgreSQL and Cellar.

### 9.3 What these commands do not tell you

Neither command judges recency. A host that last committed six days ago renders identically to one
that committed minutes ago, apart from a timestamp to compare by eye — the same deliberate
restraint `archive status` has, and for the same reason: publication recency is a per-host
judgement, and `babel archive fleet` is where that judgement lives.

And an empty result is an answer. A deployment where nothing has been explored yet reports no
records and exits zero; it is not a malfunction and is not distinguished from one, because there
is nothing to distinguish.

---

## What remains operator-gated

The dated historical observations above do not establish current fleet state.
The following **OPERATOR STEPS remain unexecuted** for the clan-var deployment:

1. Independent custody backup/recovery (§3), any necessary derived generation,
   encrypted commit, and per-machine apply (§4). Preserve the existing password
   and whole append-only ring; missing placement is not rotation.
2. Read-only current placement/diagnostic/scheduler checks (§4, §7.1), including
   actual Darwin launchd state, followed by authorized first publication and
   cross-host archive visibility (§6).
3. Ring reconciliation/distribution and any deliberately authorized rotation
   (§8.1), including cross-host opening of historical and new records.
4. Suspension, rollback and roll-forward (§7), none of which was activated by
   the historical drill.
5. **OPERATOR STEP — full restore-to-service on a clean machine (not executed).**
   **Prerequisites:** an authorized spare registered machine, independently
   recoverable custody, current dotfiles and access to the existing repository/
   catalog. Follow §3–4 placement, restore a historical source tree using §2,
   and complete §6 publication and §7 scheduling checks.
   **Success:** the restored bytes match the chosen snapshot, the clean machine
   publishes under its intended registry identity, and another authorized host
   sees that publication. The 2026-08-31 cross-machine restores and archive
   observations prove their historical parts, not this current composition.

The stale shared locks in §2.4 were also observed only on 2026-08-31. Reassess
lock ownership and liveness before the explicit unlock operator step; do not
assume those lock ids still need removal today.
