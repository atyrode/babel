# Phase A bootstrap evidence

SPEC.md §12 requires rollout acceptance to record the exact Babel source
revision, the `babel version --json` result, the storage-schema version, the
dotfiles revision, and the activation time. This file is that record for the
first real archive publication.

It is deliberately a file in the repository rather than a note somewhere: the
binary that produced the first snapshot lived in `/tmp`, which does not survive
a reboot, so the provenance was recoverable for hours rather than indefinitely.

## First real publication

| | |
|---|---|
| snapshot | `a6ea43ca100e1b0589261d20c5bfbd60e3c04e2dffde8efecfd78ffc72ce0fd6` |
| taken | 2026-08-29T16:04:19Z |
| host identity | `workstation-linux` |
| instance | `workstation-linux` |
| deployment | `babel-prod` |
| repository | `s3:https://cellar-c2.services.clever-cloud.com/tyrode-babel-archive/babel/v1` |
| catalog | Clever Cloud PostgreSQL, `XXS Medium Space` dedicated plan, `par` |
| files captured | 14,724 |
| bytes processed | 11,136,517,635 |
| data added | 11,038,267,574 |
| sessions published | 837 (641 codex, 163 omp, 33 claude) |
| backup roots | `~/.claude`, `~/.codex`, `~/.omp/agent/blobs`, `~/.omp/agent/sessions` |

## Binary provenance

```json
{
  "version": "v0.2.2-0.20260829144754-c7ca85a27343",
  "commit": "c7ca85a27343645abe2d8ecaf1e68bbffdd5d1bb",
  "dirty": false,
  "build_time": "2026-08-29T14:47:54Z",
  "go_version": "go1.26.7",
  "platform": "linux/amd64"
}
```

Built from a clean tree at `c7ca85a` ("fix: stop the e2e suite from being able to
read the real storage.json", #51). The executable was a locally built binary at
`/tmp/bin/babel`, **not** a pinned Nix store path. That is the one part of §12's
rollout contract this publication does not satisfy, and it is why the hourly
timer must execute Babel through its absolute pinned store path rather than
inheriting provenance from this session.

`c7ca85a` precedes the freeze commit (#52), which is worth stating plainly rather
than leaving to inference: the freeze added enforcement tests and changed no
schema, so the document and catalog shapes this binary wrote are exactly the ones
now frozen.

## Schema versions

| | |
|---|---|
| `storage.json` | `config_schema` 2 (frozen 2026-08-29) |
| catalog | `SchemaVersion` 1, migrations `0001_init`, `0002_unknown_counts` (frozen 2026-08-29) |

## Dotfiles revision

`732e88a` — "feat: add the Babel storage configuration ceremony" (atyrode/dotfiles#441).
The configuration was installed by `scripts/babel-storage-configure.sh`: Bitwarden
generated the repository password into a new vault item, the script wrote it to a
mode-0600 file and piped one document into `babel storage configure --from-json -`.

## Verification performed before retiring the legacy archive

The pre-Babel backup (`tyrode-agent-sessions`, 12,982 objects, 8.78 GiB, rclone
crypt with no config remaining on the machine) was the only other copy of this
data. It was purged only after every check below passed, because an exit status
proves restic accepted an upload rather than that the archive is restorable.

- `babel archive verify` — `ok (structure)`.
- `babel archive verify --deep` — `ok (deep)`; every repository byte read and
  verified, 36s.
- **restic alone**, with no Babel and no PostgreSQL in the restore path: seven
  files spanning all three harnesses plus the content-addressed blob store,
  restored from Cellar and byte-compared against the live sources. 7/7 identical.
- Catalog: 837 session rows committed, 0 uncatalogued, 0 `catalog-pending`.
- Coverage by enumeration rather than by trusting the push's `complete` flag:
  `restic ls latest` yields 14,724 files and the live roots yield 14,724. The
  only difference in either direction is one transient Codex lock file
  (`~/.codex/tmp/arg0/codex-arg0*/.lock`), recreated under a new random name
  between the push and the comparison.

The push reported `complete yes`, meaning restic read every source file. That
flag and the exit status share a cause, so the enumeration above is the
independent check: it establishes coverage without relying on either.

## Hourly timer rollout

Landed 2026-08-29 in atyrode/dotfiles (#442), replacing the pre-Babel
`atyrode-session-backup` job rather than running beside it. `babel` is a pinned
flake input, so `ExecStart` is an absolute store path — the provenance the first
publication above could not claim.

| proof | result |
|---|---|
| unconfigured machine | exit 0, no stamp, points at the ceremony script |
| configured, repository never created | exit 1, no stamp, `run \`babel archive init\`` visible |
| valid repository, nothing to archive | exit 0, **no stamp** |
| `Persistent=true` after a missed window | fired immediately; snapshot `aa009834`, 837 sessions, stamp written |

The third row is a regression this file exists to remember. The wrapper first
stamped success unconditionally, so a host whose source roots were absent
reported a fresh archive having archived nothing. It now reads `snapshot_id`
and `incomplete` from `babel archive push --json` and stamps only a complete
snapshot; `checks/babel-archive.nix` asserts that and names why.

## Not yet satisfied
- Coordinated backup and restore drills, and repository-password custody
  documentation beyond the vault item itself.
- Database-enforced application/migration role separation, which Clever Cloud
  cannot express (decision 46).
