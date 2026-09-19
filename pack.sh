#!/usr/bin/env bash
# Packs every plugin under this directory into dist/<id>.manifold-plugin.json with the manifold
# plugin kit's `pack` (the kit, the protocol and zod inlined into each half), then writes
# dist/SHA256SUMS over the artifacts' exact bytes: the pins `engine.plugins.install` demands.
#
# Before any of that it builds the MACHINE HALF and stamps the baseline's manifest, because a
# bundle carries that half as a `bundleFile` artifact and an artifact is named by its hash:
#
#   1. `bun build babel/machine/main.ts` → babel/machine.js, one bundled file
#      (git ignores it; `--machine` leaves it in place for `dev`, a pack deletes it after).
#   2. `bun scripts/stamp-machine.ts` writes its sha256 into BOTH platform artifacts of
#      manifest.json — a `raw` artifact is its own entry, so `sha256` and `entrySha256` are the
#      same digest (manifold packages/plugin-kit/src/artifacts.ts, the `format === "raw"`
#      branch) — and writes `machine.tools` from runtime-tools.json, the pins that script
#      measured from the bytes it downloaded. The manifest that ships is therefore always over
#      the bytes just built, and the committed one carries the last stamp: whoever changes the
#      machine half or the pinned bun sees the digest move in the diff, which is the point.
#      ONE tool is pinned, `bun` (#303): the fleet advertises the `development` and `system`
#      closures, not Babel's own interpreter. Nothing here downloads anything — packing stays
#      offline, and moving a pin is `bun scripts/measure-runtime-tools.ts`.
#
# The SDK is the checkout manifold-dir.sh resolves (MANIFOLD_DIR, ../manifold-db, ../manifold).
set -euo pipefail

cd "$(dirname "$0")"
MANIFOLD="$(./manifold-dir.sh)"
PACK="$MANIFOLD/packages/plugin-kit/src/pack.ts"
BASELINE=babel

build_machine() {
  bun build "$BASELINE/machine/main.ts" --target bun --outfile "$BASELINE/machine.js" >/dev/null
  bun scripts/stamp-machine.ts "$BASELINE"
}

# The inner loop (`bun run dev`) packs the tree itself on every save, so it needs the built half
# and the stamped manifest to survive this command.
if [ "${1:-}" = "--machine" ]; then
  build_machine
  exit 0
fi

trap 'rm -f "$BASELINE/machine.js"' EXIT
build_machine

rm -rf dist
mkdir -p dist

# Every manifest.json in this repository is one plugin, and a part is a directory inside its
# parent's, so the walk is recursive and the artifact is named by the manifest's own id — never
# by the directory. A directory is named for its id's LAST SEGMENT (`babel/`, `babel/feed/`,
# `babel/jev/`), which is what the repository's own name and the parent's own directory already
# say: repeating the whole id would spell `atyrode` and `babel` twice over inside `atyrode/babel`.
# Shallowest first, so a parent is always packed before the parts nested inside it.
#
# THE WALK STARTS AT THE REPOSITORY ROOT, because the repository IS the plugin family: there is
# no `plugins/` wrapper to descend into, so the prune list has to name everything at the root
# that is not source. `.integration` holds the CODE and OMP checkouts `deps:code` prepares so a
# required dependency can be composed at verification, and their manifests are theirs: a walk
# that packed them would put another family's bundles in this family's dist, and `verify` would
# be handed each of them twice.
while IFS= read -r manifest; do
  dir="$(dirname "$manifest")"
  id="$(bun -e 'console.log(JSON.parse(await Bun.file(process.argv[1]).text()).id)' "$manifest")"
  bun "$PACK" "$dir" --out "dist/$id.manifold-plugin.json"
done < <(find . -path ./node_modules -prune -o -path ./dist -prune -o -path ./.integration -prune \
  -o -path ./.git -prune \
  -o -name manifest.json -print |
  awk -F/ '{ print NF, $0 }' | sort -k1,1n -k2 | cut -d" " -f2-)

(cd dist && sha256sum -- *.manifold-plugin.json > SHA256SUMS)
cat dist/SHA256SUMS
