#!/usr/bin/env bash
# Packs every plugin under this directory into dist/<id>.manifold-plugin.json with the manifold
# plugin kit's `pack` (the kit, the protocol and zod inlined into each half), then writes
# dist/SHA256SUMS over the artifacts' exact bytes: the pins `engine.plugins.install` demands.
#
# Before any of that it builds the MACHINE HALF and stamps the baseline's manifest, because a
# bundle carries that half as a `bundleFile` artifact and an artifact is named by its hash:
#
#   1. `bun build atyrode.babel/machine/main.ts` → atyrode.babel/machine.js, one bundled file
#      (git ignores it; `--machine` leaves it in place for `dev`, a pack deletes it after).
#   2. Its sha256 is written into BOTH platform artifacts of manifest.json — a `raw` artifact is
#      its own entry, so `sha256` and `entrySha256` are the same digest (manifold
#      packages/plugin-kit/src/artifacts.ts, the `format === "raw"` branch). The manifest that
#      ships is therefore always over the bytes just built, and the committed one carries the
#      last stamp: whoever changes the machine half sees the pin move in the diff, which is the
#      point. No tool is pinned here: `bun` and `code` are runtime tools the machine's owner
#      provides with their closures (README.md), because the job sandbox has no libc.
#
# The SDK is the checkout manifold-dir.sh resolves (MANIFOLD_DIR, ../../manifold-db, ../../manifold).
set -euo pipefail

cd "$(dirname "$0")"
MANIFOLD="$(./manifold-dir.sh)"
PACK="$MANIFOLD/packages/plugin-kit/src/pack.ts"
BASELINE=atyrode.babel

STAMP='
const dir = process.argv[1];
const file = `${dir}/manifest.json`;
const manifest = await Bun.file(file).json();
const machine = await Bun.file(`${dir}/machine.js`).arrayBuffer();
const sha256 = new Bun.CryptoHasher("sha256").update(machine).digest("hex");
for (const artifact of Object.values(manifest.machine.artifacts)) {
  artifact.sha256 = sha256;
  artifact.entrySha256 = sha256;
}
await Bun.write(file, JSON.stringify(manifest, null, 2) + "\n");
console.log(`machine.js ${String(machine.byteLength)} bytes sha256=${sha256}`);
'

build_machine() {
  bun build "$BASELINE/machine/main.ts" --target bun --outfile "$BASELINE/machine.js" >/dev/null
  bun -e "$STAMP" "$BASELINE"
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

# Every manifest.json below a plugin directory is one plugin, and a child is a directory inside
# its parent's, so the walk is recursive and the artifact is named by the manifest's own id.
# Shallowest first: the family reads parents before parts, in dist/SHA256SUMS as everywhere else.
while IFS= read -r manifest; do
  dir="$(dirname "$manifest")"
  id="$(bun -e 'console.log(JSON.parse(await Bun.file(process.argv[1]).text()).id)' "$manifest")"
  bun "$PACK" "$dir" --out "dist/$id.manifold-plugin.json"
done < <(find . -path ./node_modules -prune -o -path ./dist -prune -o -name manifest.json -print |
  awk -F/ '{ print NF, $0 }' | sort -k1,1n -k2 | cut -d" " -f2-)

(cd dist && sha256sum -- *.manifold-plugin.json > SHA256SUMS)
cat dist/SHA256SUMS
