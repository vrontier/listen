#!/usr/bin/env bash
# Builds the listener (static linux/amd64) for a deploy, from a clean working
# tree only: Go stamps the commit and a "modified" flag into the binary, so
# only a committed tree gives production and staging the byte-identical file.
#   scripts/build-listener.sh <output>      ALLOW_DIRTY=1 to build anyway
set -euo pipefail
cd "$(dirname "$0")/.."
out="${1:?usage: build-listener.sh <output>}"

if [[ -n $(git status --porcelain) && ${ALLOW_DIRTY:-} != 1 ]]; then
  echo "working tree has uncommitted changes; commit them first, so both nodes get the same build:" >&2
  git status --short >&2
  echo "(ALLOW_DIRTY=1 to deploy anyway; the binary is then marked modified)" >&2
  exit 1
fi

echo "building listener (linux/amd64, static)"
(cd listener && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -trimpath -o "$OLDPWD/$out" ./cmd/listener)
rev=$(go version -m "$out" | sed -n 's/.*vcs\.revision=\([0-9a-f]\{7\}\).*/\1/p')
mod=$(go version -m "$out" | sed -n 's/.*vcs\.modified=\(.*\)$/\1/p')
sum=$(shasum -a 256 "$out" | cut -c1-16)
[[ $mod == true ]] && rev="$rev (modified)"
echo "listener build: commit $rev, sha256 $sum…"
