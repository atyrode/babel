#!/usr/bin/env bash
# Prints the SDK checkout every script here builds against: atyrode/manifold at the revision in
# MANIFOLD_REV (README.md). MANIFOLD_DIR names it outright; otherwise `../../manifold-db` (a
# working tree that keeps the plugin-database branch beside this one) before `../../manifold`,
# the layout manifold's reusable `plugins.yml` builds in CI. tsconfig.json resolves the same
# two candidates in the same order, so a typecheck and a pack never read different kits.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
for candidate in "${MANIFOLD_DIR:-}" "$here/../../manifold-db" "$here/../../manifold"; do
  if [ -n "$candidate" ] && [ -f "$candidate/packages/plugin-kit/src/pack.ts" ]; then
    cd "$candidate" && pwd
    exit 0
  fi
done
echo "manifold-dir.sh: no manifold checkout beside this tree (expected atyrode/manifold @ $(cat "$here/MANIFOLD_REV"); see README.md, or set MANIFOLD_DIR)" >&2
exit 1
