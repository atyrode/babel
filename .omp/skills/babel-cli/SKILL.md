---
name: babel-cli
description: Archive, browse, verify, and restore OMP/Codex/Claude Code sessions with the babel CLI. Use when asked to back up agent sessions, list or inspect archived conversations, restore a session from a snapshot, or check archive integrity.
---

# Using the babel CLI

Babel archives this machine's agent sessions (OMP, Codex, Claude Code) into a
restic repository and restores any historical capture byte-exactly. It is
headless: every command works over stdin/stdout, and `--json` emits exactly one
machine-readable JSON document on stdout (diagnostics go to stderr).

## Build and prerequisites

```sh
go build -o babel ./cmd/babel   # from the repo root
```

The `restic` binary must be on `$PATH` (or pass `--restic-binary PATH`).
Babel never installs, deletes, or prunes anything: there is no `forget`/`prune`
code path, snapshots are append-only, and local prune never touches the
repository.

## Repository selection (required for archive/fetch commands)

There is no persistent configuration yet. Name the repository per invocation:

- `--repo REPOSITORY --password-file FILE`, or
- `$BABEL_RESTIC_REPO` and `$BABEL_RESTIC_PASSWORD_FILE`.

A local path works as a repository (e.g. `/tmp/babel-repo`) — ideal for
experiments; S3/Cellar locators work the same way. Create it once with
`babel archive init`: `babel archive push` deliberately refuses to create a
repository, so a mistyped locator fails instead of silently becoming a second
empty archive.

Host identity for snapshots: `--host ID`, else `$BABEL_HOST_ID`, else the
sanitized system hostname. Host ids are `[a-z0-9._-]`, 1-64 chars.

## Commands
```sh
babel version [--json]                     # build identity
babel web [--port N] [--open]              # loopback web GUI (primary surface); prints a token URL
babel storage configure --from-json FILE|- # persist repository selection in storage.json (0600)
babel storage status [--json]              # report persistent configuration
babel archive init [--json]                # create the repository, once per deployment
babel archive push [--json]                # snapshot every adapter root on this host
babel archive status [--json]              # snapshots grouped by host (read-only)
babel archive fleet [--expect HOST,...] [--every DURATION] [--json]
                                           # is every machine still publishing? (read-only)
babel archive verify [--deep] [--json]     # structural check; --deep re-reads all pack data
babel sessions list [--harness omp|codex|claude] [--json]   # discover local sessions in place
babel sessions list --host HOST [--snapshot ID] [--json]     # list another host's archived sessions
babel sessions inspect SELECTOR [--json]   # one session: metadata, artifacts, blob closure
babel sessions fetch SELECTOR [--snapshot ID] [--json]      # restore a session's file closure
babel sessions prune --local --yes (--all | SELECTOR...)    # delete locally fetched copies only
```

- `sessions list`/`inspect` are read-only and never open the repository.
- `archive fleet` answers "did every machine back up", which `archive status`
  only supplies timestamps for. Each host is judged against a cadence derived
  from its own recent snapshot gaps (or the fleet's, when it has too little
  history), and the `EXPECTED EVERY` column always names that source, so an
  inferred cadence is never mistaken for a configured one. A host with no
  derivable cadence reports `unknown`, never `current`.
- A machine that has never published is invisible to the archive by
  construction, so name the ones you expect: `--expect ws-linux,wsl-nixos`
  reports an absent one as `MISSING`. Babel stores no roster - a stored fleet
  list goes stale silently and then answers confidently.
- `archive fleet` always exits `0`, including for a late or missing host: it
  reports a judgement, and is deliberately not an alerting hook. Script off
  the `state` field of `--json`.
- Selectors come from `sessions list` output (`harness/source_id`); a unique
  suffix (e.g. the session stem) is accepted, and ambiguity is reported with
  candidates rather than guessed.
- `fetch` defaults to the latest snapshot; `--snapshot ID` restores the exact
  bytes of an older capture. Restores land in a private directory under the
  XDG data dir and are idempotent (`already_present` in the JSON result).
- `storage configure` makes flags/env unnecessary: precedence is flag >
  env > storage.json. `babel web` works unconfigured for read-only browsing;
  archive actions then report "not configured".

## Exit codes and JSON contract

- `0` success, `1` operation failed, `2` bad invocation (usage on stderr).
- With `--json`, stdout is exactly one JSON document; parse with unknown
  fields tolerated. Without `--json`, output is human-oriented and terminal-safe.

## Safety rules

- Never run `restic forget`, `restic prune`, or delete repository files;
  Babel's contract is append-only retention.
- Never point `--repo` at the operator's production repository during tests —
  use a throwaway local path repository and delete it afterwards.
- Session content is sensitive: do not paste transcript bodies into logs,
  commits, or chat; the test suites use synthetic fixtures only.
- Create password files with owner-only permissions — `(umask 077 && echo
  PASSWORD > FILE)` or `chmod 600 FILE` immediately after writing — and
  remove them together with throwaway repositories when finished.

## Testing

```sh
go test ./...                 # unit + integration (needs restic on PATH)
go test ./test/e2e/...        # full Phase A loop against a real local repo
```
