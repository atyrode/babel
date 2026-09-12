#!/usr/bin/env bash
# Packs every plugin under this directory into dist/<id>.manifold-plugin.json with the manifold
# plugin kit's `pack` (the kit, the protocol and zod inlined into each half), then writes
# dist/SHA256SUMS over the artifacts' exact bytes: the pins `engine.plugins.install` demands.
#
# The SDK is a sibling checkout of atyrode/manifold at the revision in MANIFOLD_REV (README.md):
# `../../manifold` from here, which is the layout manifold's reusable `plugins.yml` builds in CI
# and the one tsconfig.json resolves. MANIFOLD_DIR overrides it for a working tree that keeps the
# checkout somewhere else (an isolated worktree, or dev-01, where the branch carrying the plugin
# database is checked out as `manifold-db`).
set -euo pipefail

cd "$(dirname "$0")"
MANIFOLD="${MANIFOLD_DIR:-$(cd ../.. && pwd)/manifold}"
PACK="$MANIFOLD/packages/plugin-kit/src/pack.ts"
if [ ! -f "$PACK" ]; then
  echo "pack.sh: no manifold checkout at $MANIFOLD (expected atyrode/manifold @ $(cat MANIFOLD_REV); see README.md, or set MANIFOLD_DIR)" >&2
  exit 1
fi

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
