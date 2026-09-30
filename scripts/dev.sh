#!/usr/bin/env bash
# Local development: the Go listener on :8080 and the PHP site on :8000.
#   scripts/dev.sh                        # loop the newest capture in samples/
#   scripts/dev.sh https://…/live         # analyse a live stream
set -euo pipefail
cd "$(dirname "$0")/.."

in="${1:-$(ls -t samples/*.mp3 2>/dev/null | head -1 || true)}"
if [[ -z "$in" ]]; then
  echo "usage: scripts/dev.sh <file|url>  (no capture found in samples/)" >&2
  exit 2
fi
loop=()
[[ "$in" == *://* ]] || loop=(-loop)

(cd listener && go build -o ../bin/listener ./cmd/listener)
bin/listener -in "$in" "${loop[@]}" -addr 127.0.0.1:8080 &
listener=$!
php -S 127.0.0.1:8000 -t site scripts/dev-router.php >/dev/null 2>&1 &
site=$!
trap 'kill $listener $site 2>/dev/null' EXIT INT TERM

echo
echo "  open http://127.0.0.1:8000/live?ws=ws://127.0.0.1:8080/ws/live"
echo
wait
