#!/usr/bin/env bash
# Builds the release archives of one component for Linux, macOS and Windows,
# from a clean working tree, into dist/<component>-<version>/.
#   scripts/release.sh ingest 0.1.0
#   scripts/release.sh engine 0.2.0
#   scripts/release.sh workbench 0.1.0
# The release workflow (.github/workflows/release.yml) runs it for a tag
# ingest/v0.1.0, engine/v0.2.0 or workbench/v0.1.0 and publishes the result.
# ALLOW_DIRTY=1 builds from an uncommitted tree (for trying it out).
set -euo pipefail
cd "$(dirname "$0")/.."
component="${1:?usage: release.sh ingest|engine|workbench <version>}"
version="${2:?usage: release.sh ingest|engine|workbench <version>}"
version="${version#v}"
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "version must be like 1.2.3" >&2; exit 2; }

if [[ -n $(git status --porcelain) && ${ALLOW_DIRTY:-} != 1 ]]; then
  echo "working tree has uncommitted changes; a release is built from a commit" >&2
  exit 1
fi

# Per component: its module, the variable that carries the version, the
# commands to build (name=package) and the files that go along.
case "$component" in
ingest)
  dir=ingest
  var=main.baseVersion
  cmds=("listen-ingest=./cmd/listen-ingest")
  docs=(ingest/README.md ingest/LICENSE)
  units=(deploy/systemd/listen-ingest.service)
  ;;
engine)
  dir=listener
  var=github.com/vrontier/listen/listener/internal/engine.baseVersion
  cmds=("listen-engine=./cmd/listen-engine")
  docs=(listener/cmd/listen-engine/README.md LICENSE)
  units=(deploy/systemd/listen-engine.service)
  ;;
workbench)
  dir=workbench
  var=main.baseVersion
  cmds=("low-workbench=./cmd/workbench" "listen-standin=./cmd/listen-standin" "engine-check=./cmd/engine-check")
  docs=(workbench/README.md workbench/LICENSE)
  units=(workbench/deploy/systemd/low-workbench.service)
  ;;
*)
  echo "unknown component: $component (ingest, engine or workbench)" >&2
  exit 2
  ;;
esac

targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64)
out="dist/$component-$version"
rm -rf "$out"
mkdir -p "$out"
root=$PWD

for t in "${targets[@]}"; do
  os=${t%/*} arch=${t#*/}
  name="listen-$component"
  [[ $component == workbench ]] && name="listen-workbench"
  stage="$out/stage/${name}_${version}_${os}_${arch}"
  mkdir -p "$stage"
  ext=""
  [[ $os == windows ]] && ext=".exe"
  for c in "${cmds[@]}"; do
    bin=${c%%=*} pkg=${c#*=}
    (cd "$dir" && CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
      -ldflags "-s -w -X $var=$version" -o "$root/$stage/$bin$ext" "$pkg")
  done
  cp "${docs[@]}" "$stage/"
  if [[ $os == linux ]]; then
    mkdir -p "$stage/systemd"
    cp "${units[@]}" "$stage/systemd/"
  fi
  if [[ $os == windows ]]; then
    (cd "$out/stage" && zip -qr "../$(basename "$stage").zip" "$(basename "$stage")")
  else
    tar -C "$out/stage" -czf "$out/$(basename "$stage").tar.gz" "$(basename "$stage")"
  fi
  echo "built $(basename "$stage")"
done
rm -rf "$out/stage"
(cd "$out" && shasum -a 256 *.tar.gz *.zip > SHA256SUMS)
echo "release files in $out:"
ls -1 "$out"
