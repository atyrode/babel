#!/usr/bin/env bash
# Packs every plugin under this directory into dist/<id>.manifold-plugin.json with the
# manifold plugin kit's `pack` (the kit, the protocol and zod inlined), then writes
# dist/SHA256SUMS over the artifacts' exact bytes: the pins `engine.plugins.install` demands.
#
# The SDK is the sibling manifold checkout at ../../manifold, at the revision in MANIFOLD_REV;
# tsconfig.json resolves `@manifold/*` there, and this script runs its `pack` from there too,
# so one checkout answers for both. All bundles are cut from one tree, together.
set -euo pipefail

cd "$(dirname "$0")"
MANIFOLD="$(cd ../.. && pwd)/manifold"
PACK="$MANIFOLD/packages/plugin-kit/src/pack.ts"
if [ ! -f "$PACK" ]; then
  echo "pack.sh: no manifold checkout at $MANIFOLD (expected atyrode/manifold @ $(cat MANIFOLD_REV), see README.md)" >&2
  exit 1
fi

rm -rf dist
mkdir -p dist

# Every manifest.json below a plugin directory is one plugin; a child is a directory inside
# its parent's directory, so the walk is recursive and the artifact is named by the manifest id.
while IFS= read -r manifest; do
  dir="$(dirname "$manifest")"
  id="$(bun -e 'console.log(JSON.parse(await Bun.file(process.argv[1]).text()).id)' "$manifest")"
  bun "$PACK" "$dir" --out "dist/$id.manifold-plugin.json"
done < <(find . -path ./node_modules -prune -o -path ./dist -prune -o -name manifest.json -print | sort)

(cd dist && sha256sum -- *.manifold-plugin.json > SHA256SUMS)
cat dist/SHA256SUMS
