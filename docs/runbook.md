# Operations runbook: recovery, custody, and rollback

The archive is worth exactly what its recovery is worth, so every procedure
below was run rather than reasoned about. Each section records what was
actually executed, on which host, on which date, and what it printed. Where a
step could not be run read-only it is marked **OPERATOR STEP** and says what
success looks like, so nothing here is a procedure whose first real execution
happens during an incident.

This closes the "document and exercise" half of the SPEC.md §14 gate on
coordinated catalog backup, repository recovery, password custody, storage
configuration recovery, manual bootstrap, timer enablement, and rollback.

## Exercise environment

All output below was captured on **2026-08-31** on host `workstation-linux`
(kernel hostname `ubuntu-4gb-nbg1-1`), against the **real production
deployment** — real Cellar repository, real managed PostgreSQL catalog.

```
babel 0-unstable-2026-08-30 (64ba5e178386) linux/amd64 go1.26.5
restic 0.19.1 compiled with go1.26.5 on linux/amd64
```

**Every command in this runbook that was exercised is read-only.** No snapshot
was written, no catalog row was inserted or deleted, and no `restic`
write verb (`init`, `backup`, `forget`, `prune`, `unlock`, `repair`) was run.
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

**Preconditions.** `~/.config/babel/storage.json` exists (written by the
ceremony in §4). The timer's `ConditionPathExists` names that document, so an
unconfigured machine leaves the unit inactive with a journal line naming the
missing path instead of failing hourly.

**Steps.** Nothing routine. The timer fires `babel-archive-push`, a wrapper that
runs `babel archive push --json` and only stamps
`~/.local/state/babel/last-success` when a complete snapshot was actually
published. To force one archive by hand:

```sh
systemctl --user start babel-archive.service   # or: babel-archive-push
```

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

If `storage.json` is also gone, those four values come from §3 and §4: the
repository password from Bitwarden, the locator and object-store keys from
`clever addon env`.

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

**This is the one secret no provider can reissue.** Clever Cloud can mint new
object-store keys and a new database password; nobody can recover the restic
repository password. Losing it makes all 44 snapshots — 23.466 GiB of source
history — permanently unreadable. There is no support path, no escrow, and no
brute force.

**Custodian.** Bitwarden (`vault.bitwarden.eu`), item **"Babel repository
password"**, username `babel-archive`. The item's own notes state the loss
consequence.

**Who creates it.** Bitwarden does. `scripts/babel-storage-configure.sh` in
`atyrode/dotfiles` generates the password *through the vault* on first run and
never regenerates it afterwards, so the secret is in its custodian from birth
and no script ever invents a credential. Babel is vault-agnostic: it receives
one finished document on stdin, never learns Bitwarden exists, and never prints
a secret.

**Where it lands on a machine.** Exactly one file, because restic accepts a
password only by file or environment and a file keeps it off every command line:

```
$ stat -c '%n  mode=%a  owner=%U  size=%s' ~/.config/babel/storage.json ~/.config/babel/repository-password
/home/alex/.config/babel/storage.json         mode=600  owner=alex  size=708
/home/alex/.config/babel/repository-password  mode=600  owner=alex  size=65
```

`babel storage status` independently confirms the file is present and its mode
is safe, without reading the value:

```
password file exists  yes
password file secure  yes
```

**Vault state observed tonight** (`bw status` is read-only and does not unlock;
account fields redacted):

```
$ bw status
{"serverUrl":"https://vault.bitwarden.eu","lastSync":"2026-08-29T15:48:52.837Z","status":"locked"}
exit 0
```

The vault being `locked` between ceremonies is the correct steady state. The
ceremony relocks on every exit path including failure — that is enforced by a
`trap relock EXIT`, because leaving the vault unlocked is precisely the failure
the ceremony exists to bound.

> **OPERATOR STEP — the unlock/retrieve/relock drill.** It cannot be exercised
> unattended: it needs the master password, and it is the one procedure whose
> whole point is that a human is present. Run:
>
> ```sh
> atyrode provision babel --dry-run
> ```
>
> **Success looks like:** an interactive unlock prompt, then a `would
> configure:` block naming host, instance, deployment, repository locator,
> catalog host, both resolved add-on ids, the password file path, and
> `vault item  Babel repository password (present)` — the `(present)` word is
> the assertion that matters, because it proves the existing password was
> *retrieved* rather than a new one generated. Nothing is written (`--dry-run`
> writes no document), and `bw status` must report `locked` again afterwards.
> If it reports `unlocked`, the relock trap failed and that is a defect.
>
> **Backup of the secret itself** is Bitwarden's own export, and it is the
> operator's periodic obligation, not Babel's: an encrypted vault export stored
> off this fleet. A password held only in a vault that is only reachable from
> the machines it protects is not backed up.

---

## 4. `storage.json` recovery

**Preconditions.** `bw`, `clever`, `babel`, and `python3` on `PATH`; `clever`
logged in to the organisation owning the add-ons.

**The document is regenerated, never hand-edited or restored from a backup.**
It is not worth backing up: everything in it is derivable. The repository
password comes from Bitwarden, and the Cellar and PostgreSQL credentials are
read live from `clever addon env` — deliberately *not* copied into the vault,
because a second source of truth goes stale the moment either is rotated.

**Steps.**

```sh
atyrode provision babel                       # already-configured machine: reuses its own identity
atyrode provision babel --host-id <name>      # a machine that has never been configured
```

Add-ons are referenced by name and resolved to ids at run time, so no opaque
identifier is recorded in any repository or carried by an operator. An explicit
`addon_<uuid>` is honoured as given, so recovery never depends on the lookup
succeeding.

Identity is read, not invented: a machine that has already published keeps the
identity it published under, and changing it requires `--force-host-id`.
Renaming a configured machine starts an empty history and abandons the one it
already has, because host generations and commit ordering are per-host.

**Verify.** Both checks below are read-only; `storage verify` explicitly never
changes the database or the configuration.

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

**Failure behavior.** A failed vault retrieval, validation, or rotation
preserves the previous valid `storage.json`, emits no secret, and does not
enable or restart the archive timer with partial state. Configuration is
replaced atomically from stdin only.

**Exercised 2026-08-31 on `workstation-linux`** (`storage status` and
`storage verify` live; the regenerating ceremony is the OPERATOR STEP in §3,
since it is the same vault ceremony).

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

**Exercised for real on 2026-08-31**: `alex-x86_64-linux-wsl` was bootstrapped
into the fleet tonight. Its first snapshot is in the repository, and the four
hourly snapshots after it show the timer took over unattended:

```
$ restic snapshots --no-lock --host alex-x86_64-linux-wsl
c78f6a67  2026-08-31 00:57:48   <- first publication: the bootstrap
21c4db38  2026-08-31 01:01:27   <- hourly timer from here on
a3842fd3  2026-08-31 02:01:24
3dd67096  2026-08-31 03:01:23
```

Confirmed from the other side by `archive fleet` in §1: `current`, cadence
`1h (observed)`, 4 snapshots.

**Preconditions.** The machine is in the host registry; `bw`, `clever`, `babel`,
`python3` present; the vault logged in; the repository **already created**.

**Steps.**

```sh
atyrode apply                              # activation offers the ceremony, then:
atyrode provision babel --host-id <name>   # or run it directly, by name
```

`--host-id` is required exactly once, on a machine that has never been
configured, and it is the registry name — never the kernel hostname, which is
the thing a stable archive identity exists to stop mattering. (`workstation-linux`
is the archive identity of a machine whose kernel hostname is `ubuntu-4gb-nbg1-1`.)

The ceremony writes `storage.json`, and `provision babel` then arms the timer in
the same run rather than `exec`ing away — otherwise a machine provisioned by
hand would archive nothing until its next login, because the timer's
`ConditionPathExists` is evaluated when the timer *starts*, not continuously.

**Do not run `babel archive init` on a new machine.** Repository creation is a
one-time operator act for the whole deployment. `archive push` refuses to create
a repository precisely so that a mistyped locator fails loudly instead of
silently becoming a second, empty archive, and concurrent creation corrupts a
fresh one.

**Verify.**

```sh
babel storage status                      # configured yes, correct host id
babel storage verify                      # tls active, schema compatible
systemctl --user start babel-archive.service
babel archive fleet --expect <all,hosts>  # the new host reports: current
```

---

## 7. Timer enablement and rollback

### 7.1 Enablement

The ordering is deliberate and is the SPEC.md §14 rule "timer enablement only
after shared-storage health passes": activation configures and verifies storage
first, and only then does anything start pushing on a schedule. A timer armed
before the ceremony is kept from publishing into an unconfigured archive only by
the push failing, and a guarantee made of a failure is no guarantee.

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

On macOS there is no `ConditionPathExists` equivalent, so a `launchd` agent runs
the same wrapper every 3600s and the wrapper's own check of the same document is
the gate.

### 7.2 Stopping the archive

```sh
systemctl --user stop babel-archive.timer      # this boot only
systemctl --user disable --now babel-archive.timer
```

Both are undone by the next `atyrode apply`, which is the point: the timer is
declarative state, so a durable change belongs in the configuration, not in a
`systemctl` invocation.

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

```sh
/nix/store/<generation>/activate     # roll back to a chosen generation
```

**What rollback does and does not touch.** It removes the timer, the service,
and the `babel-archive-push` wrapper. It does **not** delete `storage.json`, the
password file, the repository, or the catalog — and it must not, because a
rollback that discarded the repository password would convert a reversible
deployment change into permanent data loss. Snapshots already published stay
published; retention is append-only and Babel ships no delete path.

Rolling forward again is `atyrode apply`, which re-runs the §4 verification
before rearming.

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

Create it once per deployment:

```sh
babel sync --generate-key phase-b-1
```

```
$ babel sync --generate-key phase-b-1
payload key phase-b-1 written to /home/operator/.config/babel/payload-keys.json
note: back up that document; every object sealed under a key it holds is unreadable without it
note: this host is the only place phase-b-1 exists; put the ring from /home/operator/.config/babel/payload-keys.json into the deployment's custody document as its "payload_keys" field, then re-provision the fleet — `babel storage configure --from-json` is what installs it on every other host
```

The key material is never printed, never logged, and never appears in an error.
The two notes are the steps this key is not finished without, and both are
below. Babel names no custodian in either, deliberately: it receives one
finished document, never learns Bitwarden exists, and never prints a secret
(§3), so the vault-specific half of each step lives here and in
`atyrode/dotfiles`.

A second invocation **refuses**:

```
$ babel sync --generate-key phase-b-2
babel: /home/operator/.config/babel/payload-keys.json already holds this deployment's payload keys, and replacing it would leave every object sealed under them unreadable forever; add a key to that document to rotate instead
```

That refusal is the point. Replacing the document orphans every sealed object
written under the keys it held, and Babel deletes no remote object, so those
objects would remain in Cellar forever and unreadable by anything. Rotation is
an **append**: a new key becomes the one new envelopes are sealed under while
every previous key stays in the ring, so historical records keep opening. That
is `internal/config`'s `AddPayloadKey`, and it is why `SavePayloadKeys` will not
overwrite.

**Distribution: the ring rides in the storage ceremony.** One custody path for
the whole deployment, not two. The Bitwarden item **"Babel repository
password"** (§3) carries the ring in a hidden `payload_keys` field;
`scripts/babel-storage-configure.sh` reads it alongside the repository password
it already reads, puts both in the one document it pipes to `babel storage
configure --from-json -`, and Babel installs the ring at mode 0600 beside
`storage.json`. `atyrode provision babel` therefore hands a new machine its
locator, its provider credentials and its keys in a single act, and `babel sync`
reads the ring from exactly where it always read it.

The field is the ring, whole:

```json
{
  "config_schema": 2,
  "mode": "shared",
  "...": "the rest of §4's document",
  "payload_keys": {
    "key_schema": 1,
    "active_key_id": "phase-b-2",
    "keys": [
      {"key_id": "phase-b-1", "key": "<32 bytes, standard base64>"},
      {"key_id": "phase-b-2", "key": "<32 bytes, standard base64>"}
    ]
  }
}
```

The whole append-only history and never the newest key alone: a host given only
the active key seals correctly and cannot open one historical record.
`storage.json` itself never carries key material — `config_schema` 2 stays
frozen (SPEC.md §14) and the ring lands in the mode-0600 document — and the
install is a **union**. Exercised on 2026-08-31 against a scratch configuration
home, delivering a ring to a machine that held none and then re-delivering it:

```
$ babel storage configure --from-json ceremony-document.json
storage configuration written to /home/second/.config/babel/storage.json
payload key ring at /home/second/.config/babel/payload-keys.json gained phase-b-1; new records seal under phase-b-1

$ babel storage configure --from-json ceremony-document.json
storage configuration written to /home/second/.config/babel/storage.json
payload key ring at /home/second/.config/babel/payload-keys.json already carries every key the document delivers
```

Three properties, each of which a plainer "install what the document says"
would break:

- **A key this host holds that the document omits is kept**, and named. Dropping
  it would orphan every object sealed under it, forever:

  ```
  warning: this host holds payload key(s) workstation-1 that the delivered document does not carry; they are kept, and as far as this document knows they exist on this disk alone — copy the ring from /home/operator/.config/babel/payload-keys.json into the document's "payload_keys" field, or every record sealed under them stays unreadable on every other host and unrecoverable if this one is lost
  ```

- **A delivered key id whose material differs from the material held here is
  refused**, before `storage.json` is replaced, so the machine keeps the
  configuration and the ring it had. Two keys under one id is a fork of the
  deployment's key space — the id is what selects the key that opens a record —
  and nothing on the machine can tell which side is authoritative:

  ```
  $ babel storage configure --from-json conflicting-document.json
  babel: /home/second/.config/babel/payload-keys.json: payload key "phase-b-1": payload key material differs from the key already held under that id
  ```

- **A re-provision that delivers nothing new writes nothing.** The one file in
  Babel that is nothing but key material is not rewritten for no change.

**The one-time backfill.** `phase-b-1` was generated on `workstation-linux`
before this ceremony carried rings, so the vault item does not have it. Every
provision on that machine says so, and names the step:

```
note: vault item "Babel repository password" carries no payload key ring, and this machine holds one at /home/operator/.config/babel/payload-keys.json
      until the vault carries it, no other host can open a single Phase B record this one sealed,
      and losing this disk loses every record sealed under it
      one time, on this machine: babel-storage-configure --upload-payload-keys
      then re-provision the fleet: atyrode provision babel
```

Take it once, on the machine that holds the ring:

```sh
babel-storage-configure --upload-payload-keys   # unlock, upload, relock
atyrode provision babel                         # then on every other host
```

The upload reports key ids and counts and never material, and it merges in the
same direction the install does: a key the vault carries and this host lacks is
kept, and conflicting material under one id refuses rather than picking a side.

**Rotation.** A new key in the vault, then a re-provision of the fleet:

1. **Append a key to the ring on one machine.** `babel sync --generate-key`
   creates the document and refuses to replace it, so today this is a hand-edit
   of the mode-0600 document: another entry in `keys` (32 bytes of standard
   base64, id in `[a-z0-9._-]`) and `active_key_id` moved to it.
   `internal/config`'s `AddPayloadKey` is the code path a rotate command would
   use; the rotation *drill* is Phase C work (SPEC.md §657), and what #112 asked
   of this format was only that it not preclude one.
2. **`babel-storage-configure --upload-payload-keys`.** The vault item's ring
   becomes the union, sealing under the new active key.
3. **`atyrode provision babel` on every host.** Each gains the new key and keeps
   every old one.

Old keys are never removed, and that is what keeps history readable: an object
sealed under `phase-b-1` still opens after `phase-b-2` becomes active. A ring
that lost `phase-b-1` would leave those objects in Cellar forever, unreadable by
anything, because nothing here deletes a remote object.

**Custody.** This document is in the same class as the restic repository
password (§3): if every copy of a key is lost, the records sealed under it are
unrecoverable, and no provider can reissue it. With the ring in the vault item,
backing up the repository password backs up the keys with it — one Bitwarden
export, one obligation, and §3's note applies unchanged: a vault reachable only
from the machines it protects is not a backup. A coordinated backup of
PostgreSQL and Cellar without the keys restores ciphertext and nothing else.

**Every authorized instance holds the same keys.** SPEC.md §9 states that
plainly: every fully authorized instance can necessarily decrypt the shared
corpus, and compromise of one has that blast radius. Distributing the ring
through the ceremony makes that easy, which does not make it free — a machine
that should not read this corpus must not be provisioned into this deployment.

**What has not been run.** Everything above was exercised on 2026-08-31 against
a scratch configuration home and a stubbed vault: the delivery, the union, both
refusals, the upload merge, and the notes. The Bitwarden half — a real
`bw get item` carrying a real `payload_keys` field, and a real
`--upload-payload-keys` against the live vault — needs the master password
interactively, so it joins §3's unlock drill in *What remains operator-gated*.

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

Everything above was executed tonight except three procedures, each of which
requires something an unattended drill cannot supply:

1. **The Bitwarden unlock/retrieve/relock drill** (§3) — needs the master
   password interactively. Command and success criteria are written out above;
   it takes about a minute.
2. **The payload key ring's vault half** (§8.1) — the same master password.
   Reading a real `payload_keys` field out of the vault item, and the one-time
   `babel-storage-configure --upload-payload-keys` that puts this workstation's
   existing ring there, are the two steps that make a second host able to read
   Phase B content rather than only its plaintext rows. Everything on the Babel
   side of that boundary — delivery, the union install, both refusals — is
   exercised; the vault call is not. Until the upload is taken, `phase-b-1`
   exists on one disk.
3. **Full restore-to-service on a clean machine** — the end-to-end case where a
   machine with no `storage.json`, no password file, and no local sessions is
   rebuilt into a publishing fleet member. Its parts are all proven: bootstrap
   of a genuinely new host (§6, `alex-x86_64-linux-wsl`, tonight), cross-machine
   restore of a host's own data (§2), catalog rebuild from the repository (§5),
   and configuration regeneration (§4). What is unproven is the composition, and
   it needs a spare machine.

One unrelated operator action is queued by this drill: clearing the two stale
shared locks found in §2.4, which currently make `babel archive verify` exit 1
while leaving restores and data integrity unaffected. It is one command —
`babel archive unlock` — and that command exists because of this drill: the
step above originally asked the operator to assemble restic's environment by
hand, and bare `restic unlock` answers `Fatal: Please specify repository
location` because it reads nothing from Babel's `storage.json` (issue #108).
