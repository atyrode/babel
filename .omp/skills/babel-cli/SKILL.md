---
name: babel-cli
description: Back up, browse, verify and restore OMP/Codex/Claude Code sessions from Babel's restic archive. Use when asked to archive agent sessions, list or inspect archived conversations, restore a session from a snapshot, or check archive integrity.
---

# Babel's session archive

Babel archives a machine's agent sessions (OMP, Codex, Claude Code) into a restic repository,
tagged `babel`, one snapshot per adapter root, attributed to the machine's own identity. Any
historical capture restores byte-exactly.

**There is no `babel` binary.** Babel is a Manifold plugin family; the standalone Go command was
retired with the rest of the product. What replaced each half:

| was                       | is                                                                   |
| ------------------------- | -------------------------------------------------------------------- |
| `babel archive push`      | the `atyrode.babel.archive` machine operation, run as a Manifold job |
| `babel archive status`    | the same operation's receipt, and `runs`/`sessions` in the store     |
| `babel sessions list`     | the `atyrode.babel.scan` machine operation, and Babel's own surfaces |
| `babel web`               | the Feed and Watch panels in Manifold                                |
| `babel archive verify`    | the `atyrode.babel.verify` operation, or `restic check` — see below  |
| `babel sessions fetch`    | `atyrode.babel.verify` naming a session, or `restic restore` — below |
| `babel archive fleet`     | nothing; a deployment is one hub                                     |
| `babel storage configure` | the deployment's storage document, owned by dotfiles/clan            |

## Backing up

The `archive` operation reads the repository and its secrets from the job's own service binding
(`atyrode.babel.restic`) and from nowhere else. It never creates a repository: a repository is
created once, by hand, per deployment, because silent creation turns a mistyped locator into a
second empty archive that grows while the real one appears to stop.

Run it as a job from Manifold, or scheduled — `docs/runbook.md` owns both. Nothing here installs,
deletes or prunes: there is no `forget`, no `prune`, no `repair` code path anywhere in the
plugin, and snapshots are append-only.

## Verifying and restoring

Two paths. Prefer the first; the second is what still works when there is no hub.

**The `verify` operation.** `atyrode.babel.verify` runs `restic check` — structurally, or over
every stored byte when asked — and restores one catalogued session from a named snapshot,
comparing the restored bytes against the snapshot's own and against the digest `scan` recorded.
The `atyrode.babel.verify` door posts it with the machine, how deep to read, and the session's
selector; the run's receipt carries the verdict (`counts.checkErrors`, `counts.restored`,
`counts.digestCompared`). Its verbs are a closed set that holds no `forget`, `prune`, `repair` or
`unlock`, so it cannot remove anything whatever it is asked. `docs/runbook.md` §2 owns the
procedure and its prerequisites.

**restic by hand.** Use it when there is no hub or the store is lost, when the machine that took
the snapshot is gone or was never enrolled, when the session is not catalogued, when a whole
snapshot or root is wanted rather than one session, and for anything to do with a lock. Take the
repository and password from the deployment's storage document (`docs/runbook.md` §§3–4 and §8
own where those live and how to read one without putting a secret in argv or shell history).

```sh
export RESTIC_REPOSITORY=…                 # from the storage document
export RESTIC_PASSWORD_FILE=…              # mode 0600, never the value in argv

restic snapshots --tag babel               # what this deployment has archived, by host
restic check --no-lock                     # structural integrity, and the form a lock allows
restic check --read-data                   # re-reads every pack; slow, and the real check
restic ls <snapshot-id>                    # what one snapshot holds
restic restore <snapshot-id> --target DIR --include PATH   # byte-exact restore
```

Read a session's selector off Babel's own surfaces or the `sessions` table; `sessions.snapshot_id`
is the snapshot that holds it, which is the one fact the archive writes back into the store.

## Safety rules

- Never run `restic forget`, `restic prune`, `restic repair` or `restic unlock`, and never delete
  repository files. Each destroys history nothing else holds and needs the operator's
  case-by-case authorization for that occasion.
- Never point a command at the operator's production repository during a test. Use a throwaway
  local path repository and delete it afterwards.
- Session content is sensitive: never paste transcript bodies into logs, commits, issues or chat.
- Create a password file owner-only — `(umask 077 && printf '%s\n' "$PASSWORD" > FILE)` — and
  remove it with any throwaway repository when finished.
