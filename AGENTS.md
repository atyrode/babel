# Repository instructions

Babel is a public Go application (`cmd/babel`, `internal/`) with a TypeScript/React web
surface (`web/`) compiled into the binary, a versioned analysis cookbook (`cookbook/`) embedded
the same way, and its manifold plugins (`plugins/`). The product and delivery specification is
`SPEC.md`; the exercised recovery, custody and rollback procedures are `docs/runbook.md`; the
direction — Babel becomes a manifold plugin — is `docs/manifold-transition.md`.

## Commands

- Setup: Go from `go.mod`; restic 0.19.1 on PATH (the archive tests skip without it); for the
  web surface, `cd web && bun install`.
- Build: `go build ./...`. The web build is `cd web && bun run build`, and the committed
  `web/dist` is what the binary embeds (`web/embed.go`), so a web change ships only once
  `dist` is rebuilt and committed with it.
- Test: `go test -count=1 ./...`; `go test -race -count=1 ./internal/...`. The shared-catalog
  suite needs `BABEL_TEST_POSTGRES=<url>` and skips itself without it; `BABEL_REQUIRE_POSTGRES=1`
  turns that skip into a failure, which is how CI runs. The two-instance acceptance
  (`test/e2e`) provisions its own TLS cluster, so `initdb` and `pg_ctl` must be on PATH. The
  browser leak acceptance is `cd web && bun run test:browser` with `BABEL_TEST_CHROME` set; it
  hard-fails rather than skips without Chrome.
- Lint/typecheck: `gofmt -l .` (must print nothing), `go vet ./...`; `cd web && bunx tsc
  --noEmit`.
- Other required workflows: `babel cookbook check` after any edit under `cookbook/` — a recipe
  or preamble whose semantics changed must carry a higher version in `cookbook/versions.json`,
  and drift exits 1.

## Generated files

- `web/dist` is built from `web/src` by `cd web && bun run build` and committed.
- `cookbook/versions.json` is the version record `babel cookbook check` enforces; edit it with
  the recipe it describes, never alone.

## Deployment

- Targets and environments: a `v*` tag builds the cross-platform binaries and creates the GitHub
  Release (`.github/workflows/release.yml`); managed machines get the binary through
  `atyrode/dotfiles`, which also owns the hourly archive timer and the storage ceremony that
  writes `~/.config/babel/storage.json`. The manifold plugins ride the same release; see below.
- Only tag what you would deploy, and never move or delete a published tag: Go modules are
  immutable once proxied. A bad tag stays and the next patch follows it.

## Live systems and secrets

- The operator deployment is real: managed PostgreSQL and a restic repository on Cellar, one
  logical backend shared by every authorized machine. Every read-only command in
  `docs/runbook.md` was run against it; nothing that writes (`restic backup`, `forget`, `prune`,
  `unlock`, `init`, `repair`, a catalog insert) is run outside an operator's own hand.
- Credentials arrive in one document through `babel storage configure --from-json -` and live
  at mode 0600 beside the payload key ring (`docs/runbook.md` §4, §8). They never enter argv,
  logs, error text, shell history or persistent temporary files; Babel never invokes Bitwarden.
- Babel makes ideas inspectable and does nothing else: it does not open issues, edit
  repositories, rotate credentials or apply what it suggests. Keep it that way.

## Verification

- Go change: `gofmt`, `go vet`, `go build ./...`, `go test -count=1 ./...`, and
  `go test -race ./internal/...` when the change touches anything concurrent. Web change:
  `bunx tsc --noEmit`, `bun run build`, and the browser acceptance when the bootstrap nonce,
  the address bar or the history handling moved. Plugin change: the loop under "Manifold
  plugins".
- A procedure in `docs/runbook.md` is recorded only after it was executed; state the host, the
  date and what it printed, or mark it **OPERATOR STEP** and say what success looks like.
- When a check cannot run here (no PostgreSQL, no Chrome, no restic), say which one and why;
  do not report the suite as green.

## Delivery

- Every change is a pull request into `main` with a conventional-commit title; `ci.yml` runs
  the Go suites, the race job, the web build and the browser acceptance on every PR.
- One `## [Unreleased]` bullet in `CHANGELOG.md` per user-visible change, in the voice already
  there: what changed, why, and what proves it.
- Cross-repository facts about manifold are cited with `path:line` at a named manifold revision
  (`docs/manifold-transition.md` §1 explains the pinning); do not restate them from memory.

## Manifold plugins

`plugins/` holds `atyrode.babel` (the baseline: doors, no panel) and `atyrode.babel.sessions`
(the panel), isolated plugins authored against `@manifold/plugin-kit`. The SDK is a sibling
checkout of `atyrode/manifold` at `../manifold`, pinned by `plugins/MANIFOLD_REV`;
`plugins/tsconfig.json` and `plugins/pack.sh` both resolve it there. That pin and the
`uses:` ref in `.github/workflows/manifold-plugins.yml` are one revision and are bumped
together. The kit's commands and the two delivery strategies are manifold's `docs/PLUGINS.md`
§9; Babel's direction is `docs/manifold-transition.md`.

The loop, from `plugins/`:

```sh
cd plugins && bun install && bun run check && bun test && bun run pack && bun run verify
bun run dev -- --hub http://127.0.0.1:7912 --deliver docker:manifold-dev-manifold-1   # from dev-01
```

`verify` spawns the checkout's real server, installs every bundle, dispatches every door it
publishes and uninstalls; it is the check CI runs. `dev` packs, installs baseline before
sub-plugin on the hub named, watches the directory and reinstalls what changed; the line above
is the integrated preview as addressed from dev-01, where the owner key is read from the hub's
volume and never appears in argv, output or the tree.

Where a change is seen: `https://preview.manifold.tyrode.dev`, plugin manager, Installed,
`atyrode.babel` and `atyrode.babel.sessions`; the panel is `reports` and needs an enrolled
machine online on that hub (dev-01 is). The pipeline: PR, CI `check`/`test`/`pack`/`verify`
through manifold's reusable workflow, `v*` tag, the bundles and their sums attached to the
GitHub Release, the preview installing each one by itself through the receiver. Production
(`https://manifold.tyrode.dev`) is installed by the operator, by hand, from the release URL in
the plugin manager; never automate it.

At the end of a plugin task, and whenever asking the operator to look at something, name the
hub URL, the panel, the action to take and the result to expect.
