#!/usr/bin/env bash
# Local development: one listener per source in site/_config/sources.json
# (on its configured port, local_input or input) and the PHP site on :8000.
#   scripts/dev.sh                 # all sources
#   scripts/dev.sh vlf-heidelberg  # only these slugs
# Extra listener flags can be added per source in sources.json ("args").
set -euo pipefail
cd "$(dirname "$0")/.."

cfg=site/_config/sources.json
[[ -f $cfg ]] || { echo "$cfg is missing" >&2; exit 1; }
php -r 'exit(extension_loaded("mbstring") ? 0 : 1);' ||
  echo "note: PHP without mbstring; the site works, but servers install php8.3-mbstring (see README)" >&2

# LLM credentials for the narrator (LLM_API_KEY, NARRATOR_URL, NARRATOR_MODEL), kept out of git.
if [[ -f .llm ]]; then set -a; . ./.llm; set +a; fi

# Contact form config from .email (if present); no rate limit locally.
scripts/mail-config.sh >/dev/null

(cd listener && go build -o ../bin/listener ./cmd/listener)

pids=()
trap 'kill "${pids[@]}" 2>/dev/null' EXIT INT TERM
mkdir -p bin/dev-memory
while read -r s; do
  slug=$(jq -r .slug <<<"$s")
  if [[ $# -gt 0 && " $* " != *" $slug "* ]]; then continue; fi
  port=$(jq -r .listener.port <<<"$s")
  in=$(jq -r '.listener.local_input // .listener.input' <<<"$s")
  args=()
  while IFS= read -r a; do args+=("$a"); done < <(jq -r '(.listener.args // [])[], (if .audio then "-audio" else empty end)' <<<"$s")
  bin/listener -source "$slug" -addr "127.0.0.1:$port" -in "$in" -memory-dir bin/dev-memory ${args[@]+"${args[@]}"} \
    2>&1 | sed -u "s/^/[$slug] /" &
  pids+=($!)
  echo "  http://127.0.0.1:8000/$slug"
done < <(jq -c '.sources[]' "$cfg")

LISTEN_DIRECT_PORTS=1 php -S 127.0.0.1:8000 -t site scripts/dev-router.php >/dev/null 2>&1 &
pids+=($!)
echo "  http://127.0.0.1:8000/   (landing page)"
wait
