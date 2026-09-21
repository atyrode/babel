#!/bin/sh
set -eu

# Test-only provisioning: never install a tool or configure storage on a deployment.
: "${RUNNER_TEMP:?A disposable CI runner directory is required}"
: "${GITHUB_PATH:?A CI path output file is required}"
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) ;;
  *) echo "Archive test provisioning requires a Linux x86_64 runner" >&2; exit 1 ;;
esac

tools="$(mktemp -d "$RUNNER_TEMP/babel-test-tools.XXXXXX")"
trap 'rm -rf "$tools"' EXIT
curl --fail --location --silent --show-error \
  https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_amd64.bz2 \
  --output "$tools/restic.bz2"
printf '%s  %s\n' \
  f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c \
  "$tools/restic.bz2" | sha256sum --check
bzip2 --decompress "$tools/restic.bz2"
chmod 0755 "$tools/restic"
"$tools/restic" version
printf '%s\n' "$tools" >> "$GITHUB_PATH"
# Subsequent steps use this directory; the disposable runner owns its final cleanup.
trap - EXIT
