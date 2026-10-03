#!/usr/bin/env bash
# Deploy to production (listen.vrontier.org on ionos). The deploy account has no
# root, so this script builds and stages everything in ~questmaster/listen-deploy
# and an admin runs the root step there (see deploy/ionos/INFOS_listen_vrontier.md):
#   scripts/deploy-ionos.sh              # stage everything; admin runs install.sh
#   scripts/deploy-ionos.sh --site-only  # PHP site only, no root step needed
#                                        # (after the first install)
set -euo pipefail
cd "$(dirname "$0")/.."

host="${DEPLOY_HOST:-ionos-questmaster}"
web=/var/www/listen.vrontier.org
stage=bin/ionos-stage
cfg=site/_config/sources.json
samples=/var/lib/listen/samples

if [[ ${1:-} == --site-only ]]; then
  echo "syncing site to $host:$web"
  rsync -rlt --delete --exclude .DS_Store --exclude '*.example.*' --exclude _config/mail.php \
    site/ "$host:$web/"
  ssh "$host" "find $web -user \$(id -un) -type d -exec chmod 2755 {} + ; find $web -user \$(id -un) -type f -exec chmod 0644 {} +"
  echo "done: https://listen.vrontier.org/"
  exit 0
fi

rm -rf "$stage" && mkdir -p "$stage/samples"
echo "generating per-source files from $cfg"
NODE_MODE=continuous scripts/gen-sources.sh "$stage/gen" $'\n    limit_conn perip 20;\n    limit_req zone=general burst=20 nodelay;'
mv "$stage/gen/sources" "$stage/gen/listen-sources.conf" "$stage/gen/slugs" "$stage/" && rm -rf "$stage/gen"

# Archive sources read files from $samples on the server; ship those we have.
for f in $(jq -r --arg d "$samples/" '.sources[].listener.input | select(startswith($d)) | ltrimstr($d)' "$cfg"); do
  [[ -f samples/$f ]] || { echo "samples/$f is missing" >&2; exit 1; }
  cp "samples/$f" "$stage/samples/"
done

echo "building listener (linux/amd64, static)"
(cd listener && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -trimpath -o "../$stage/listen-listener" ./cmd/listener)

# Contact form: mail.php (gitignored) from .email (gitignored).
scripts/mail-config.sh /var/lib/listen-web
mkdir -p "$stage/site"
rsync -a --exclude .DS_Store --exclude '*.example.*' site/ "$stage/site/"

# Narrator endpoint without the key (install.sh adds the key on the server).
if [[ -f .llm ]]; then
  (set -a; . ./.llm; set +a
   [[ -n ${NARRATOR_URL:-} && -n ${NARRATOR_MODEL:-} ]] &&
     printf 'NARRATOR_URL=%s\nNARRATOR_MODEL="%s"\n' "$NARRATOR_URL" "$NARRATOR_MODEL" > "$stage/narrator.env") || true
fi

cp deploy/ionos/install.sh "deploy/systemd/listen-listener@.service" "$stage/"
cp deploy/nginx/listen.vrontier.org.conf "$stage/"
cp deploy/php-fpm/listen.vrontier.org.conf "$stage/listen-pool.conf"

echo "staging in $host:~/listen-deploy"
ssh "$host" 'mkdir -p ~/listen-deploy && chmod 0700 ~/listen-deploy'
rsync -rlt --delete "$stage/" "$host:listen-deploy/"
ssh "$host" 'chmod -R go= ~/listen-deploy'
scp -q deploy/ionos/INFOS_listen_vrontier.md "$host:INFOS_listen_vrontier.md"

echo
echo "staged. Root step (admin, see ~/INFOS_listen_vrontier.md):"
echo "  bash /home/questmaster/listen-deploy/install.sh"
