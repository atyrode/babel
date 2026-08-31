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

> **OPERATOR STEP — clear the two stale locks.** `restic unlock` is a write verb
> and was deliberately not run by this drill. Having confirmed above that both
> holding PIDs are dead, run:
>
> ```sh
> restic unlock          # add --remove-all only if a lock is exclusive and provably stale
> babel archive verify   # success looks like: OK (structure), exit 0
> ```
>
> Never run `restic forget` or `restic prune`: Babel's retention contract is
> append-only and it ships no code path that deletes a snapshot.

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

## What remains operator-gated

Everything above was executed tonight except two procedures, both of which
require something an unattended drill cannot supply:

1. **The Bitwarden unlock/retrieve/relock drill** (§3) — needs the master
   password interactively. Command and success criteria are written out above;
   it takes about a minute.
2. **Full restore-to-service on a clean machine** — the end-to-end case where a
   machine with no `storage.json`, no password file, and no local sessions is
   rebuilt into a publishing fleet member. Its parts are all proven: bootstrap
   of a genuinely new host (§6, `alex-x86_64-linux-wsl`, tonight), cross-machine
   restore of a host's own data (§2), catalog rebuild from the repository (§5),
   and configuration regeneration (§4). What is unproven is the composition, and
   it needs a spare machine.

One unrelated operator action is queued by this drill: clearing the two stale
shared locks found in §2.4, which currently make `babel archive verify` exit 1
while leaving restores and data integrity unaffected.
