# Babel's manifold plugins

Babel's final form is a manifold plugin (`docs/manifold-transition.md`). This directory is
that plugin, in the shape the operator ratified on 2026-09-05 (§7 of the record): one
**baseline** plus independently enable-able **sub-plugins**, each an isolated plugin authored
against `@manifold/plugin-kit` (manifold `docs/PLUGINS.md` §9), packed as one
`<id>.manifold-plugin.json` artifact and installed at `engine.plugins.install` by hash.

| Plugin                        | Directory                   | Halves          | What it is                                                                                                                                                              |
| ----------------------------- | --------------------------- | --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `atyrode.babel`               | `atyrode.babel/`            | server + web    | The baseline. Doors `atyrode.babel.run` (record that one of babel's read-only reports runs on an online machine; answers the argv) and `atyrode.babel.listRuns`. No panel. |
| `atyrode.babel.sessions`      | `atyrode.babel/sessions/`   | web             | Panel `reports`: pick an online machine and a report, "Run here" records through the baseline's door and opens a terminal tile on that machine running it.             |
| `atyrode.babel.configure`     | reserved                    |                 | The profile ceremony (babel#160). Not built; recorded in the transition record and filed.                                                                               |

A child is a directory inside its parent's directory, and depends on it with
`dependencies: { "atyrode.babel": { type: "required" } }`. The baseline is not a library: the
kit inlines `contract.ts` into every bundle, and a sub-plugin reaches the baseline only through
its doors (`host.action`). Every id, door name, storage key, event kind and launch line is
spelled once, in `atyrode.babel/contract.ts`.

## The SDK is a sibling checkout, for now

`@manifold/plugin-kit` and `@manifold/protocol` are private workspace packages of
`atyrode/manifold`; nothing publishes them yet. Until the kit ships as a release asset, the SDK
is a checkout of `atyrode/manifold` **beside this repository**, at the revision in
`MANIFOLD_REV`:

```
<parent>/
  babel/plugins/      this directory
  manifold/           atyrode/manifold @ $(cat MANIFOLD_REV)
```

`tsconfig.json` maps `@manifold/plugin-kit`, `@manifold/plugin-kit/*` and `@manifold/protocol`
to `../../manifold/packages/{plugin-kit,protocol}/src`; Bun honours those paths at run time and
when bundling, and `pack.sh` runs the kit's `pack` from the same checkout. The checkout needs
its own `bun install` for the kit's one dependency (zod):

```sh
git clone https://github.com/atyrode/manifold ../manifold
git -C ../manifold checkout "$(cat MANIFOLD_REV)"
bun install --cwd ../manifold --frozen-lockfile --filter '@manifold/plugin-kit' --filter '@manifold/protocol'
```

The plugins' own `zod` (`package.json`, pinned to the kit's exact version) and the checkout's
`zod` are the same release resolved from two places, so tonight every bundle carries zod twice.
The two interoperate — `test/server.test.ts` proves the kit's `z.toJSONSchema` over the
contract's schemas from the `loaded` frame — and the duplication ends when the kit is a package
this directory installs, at which point the `paths` block and `MANIFOLD_REV` go away.

## Build, test, pack

```sh
bun install                 # zod, typescript, @types/bun; nothing else
bun test                    # the doors against a fake ctx; the panel against a fake host; the launch line
bunx tsc --noEmit -p tsconfig.json
./pack.sh                   # dist/<id>.manifold-plugin.json for every manifest, plus dist/SHA256SUMS
```

`.github/workflows/manifold-plugins.yml` runs exactly those steps on every push or pull request
touching `plugins/` and uploads `dist/` as an artifact. Installing on a hub is
`engine.plugins.install` with the artifact and its sha256 from `SHA256SUMS`, baseline first
(the engine has no bundle-set install yet; a manifold follow-up is filed). Nothing here
installs anything.

The sha256 is over the artifact's exact bytes, and Bun writes every bundled module's path as a
comment, so a hash reproduces only from the layout above (`../../manifold`, an isolated
`bun install`) with the same Bun. The pins that count are the ones CI prints.

## What the panel does tonight, honestly

`babel web` serves on 127.0.0.1 behind a one-time bootstrap nonce (`babel web --help`) and is not
reachable from a hub-origin Worker, so there is no session browsing yet. What runs is babel's
read-only reports — `archive status`, `archive fleet`, `storage status`, `version` — in a
terminal on the machine the viewer chooses, with the agent's PATH, each run recorded as a
door dispatch. The report exits at once, so the terminal runs
`sh -c '<report>; printf "\n[babel exited %s]\n" $?; exec sleep 600'` and the tile stays
readable for ten minutes. `openTerminal` needs the panel's container to have a mounted
composition view; the host refuses by name otherwise and the panel shows that sentence.
