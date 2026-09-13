#!/usr/bin/env bash
# React is a SHARED EXTERNAL: the host provides one copy at runtime and every in-realm panel
# renders through it (docs/PLUGINS.md §10). A check or a test here must render through that
# same copy, because two React instances in one process is a hook dispatcher that is null -
# so react and react-dom are not dependencies of this package; they are linked from the SDK
# checkout's own resolution root, the one @manifold/plugin and @manifold/ui resolve against.
# The checkout is the one manifold-dir.sh resolves, like pack and verify. The type packages are linked with them: two @types/react on one
# tsc program are two unrelated `Ref` types, and @manifold/ui is typed against the SDK's.
set -euo pipefail
cd "$(dirname "$0")"
root="$(./manifold-dir.sh)/packages/plugin/node_modules"
mkdir -p node_modules/@types
for name in react react-dom scheduler @types/react @types/react-dom; do
  if [ -e "$root/$name" ]; then
    rm -rf "node_modules/$name"
    ln -s "$root/$name" "node_modules/$name"
  fi
done
