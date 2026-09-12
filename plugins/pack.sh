#!/usr/bin/env bash
# Packs every plugin under this directory into dist/<id>.manifold-plugin.json with the manifold
# plugin kit's `pack` (the kit, the protocol and zod inlined into each half), then writes
# dist/SHA256SUMS over the artifacts' exact bytes: the pins `engine.plugins.install` demands.
#
# The SDK is the checkout manifold-dir.sh resolves (MANIFOLD_DIR, ../../manifold-db, ../../manifold).
set -euo pipefail

cd "$(dirname "$0")"
MANIFOLD="$(./manifold-dir.sh)"
PACK="$MANIFOLD/packages/plugin-kit/src/pack.ts"

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
