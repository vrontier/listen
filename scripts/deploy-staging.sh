#!/usr/bin/env bash
# Deploy listeners and site to the staging host (listen.home.arpa, on max since
# 2026-10-03; taurus before). Idempotent; run from anywhere in the repo.
# DEPLOY_HOST picks another host. One-time host setup: deploy/README.md.
#
# Sources come from site/_config/sources.json: every entry gets a listener
# instance (listen-listener@<slug>) and nginx routes under /<slug>/.
set -euo pipefail
cd "$(dirname "$0")/.."

host="${DEPLOY_HOST:-max-mike}"
stage=/tmp/listen-deploy
cfg=site/_config/sources.json
gen=bin/deploy-gen

echo "generating per-source files from $cfg"
NODE_MODE="${LISTEN_MODE:-on-demand}" NODE_IDLE_AFTER="${IDLE_AFTER:-30s}" scripts/gen-sources.sh "$gen"
slugs=$(cat "$gen/slugs")

# Staging listens on demand by default: a source connects only while a live
# page is open and closes IDLE_AFTER after the last one, so providers don't see
# a second permanent connection next to production. LISTEN_MODE=continuous to
# compare; a source's own "mode" in sources.json overrides both.
echo "node default: ${LISTEN_MODE:-on-demand} (idle after ${IDLE_AFTER:-30s}); per source:"
column -t "$gen/modes" | sed 's/^/  /'

echo "building listener (linux/amd64, static)"
(cd listener && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -trimpath -o ../bin/listen-listener-linux-amd64 ./cmd/listener)

ssh "$host" "rm -rf $stage && mkdir -p $stage/sources"
scp -q bin/listen-listener-linux-amd64 "deploy/systemd/listen-listener@.service" \
  deploy/nginx/listen.home.arpa.conf deploy/php-fpm/listen.conf "$gen/listen-sources.conf" "$host:$stage/"
scp -q "$gen"/sources/*.env "$host:$stage/sources/"

# Contact form: mail.php (gitignored) from .email (gitignored), with the
# rate-limit directory on the staging host.
scripts/mail-config.sh /var/lib/listen-web

echo "syncing site"
rsync -rlt --delete --exclude .DS_Store --exclude '*.example.*' \
  --rsync-path="sudo rsync" site/ "$host:/var/www/listen.home.arpa/"

echo "installing"
ssh "$host" "sudo bash -s" <<SH
set -euo pipefail
chown -R root:root /var/www/listen.home.arpa
find /var/www/listen.home.arpa -type d -exec chmod 0755 {} +
find /var/www/listen.home.arpa -type f -exec chmod 0644 {} +
# Credentials: readable by PHP only.
if [ -f /var/www/listen.home.arpa/_config/mail.php ]; then
  chown root:www-data /var/www/listen.home.arpa/_config/mail.php
  chmod 0640 /var/www/listen.home.arpa/_config/mail.php
fi
php -m | grep -qx mbstring || apt-get install -y -q php8.3-mbstring
install -m 0755 $stage/listen-listener-linux-amd64 /usr/local/bin/listen-listener
install -m 0644 "$stage/listen-listener@.service" "/etc/systemd/system/listen-listener@.service"

# Migration from the single-source service.
if [ -f /etc/systemd/system/listen-listener.service ]; then
  systemctl disable --now --quiet listen-listener.service || true
  rm -f /etc/systemd/system/listen-listener.service /etc/default/listen-listener
fi

install -d -m 0755 /etc/listen/sources
install -d -o www-data -g www-data -m 0700 /var/lib/listen-web   # contact form rate limit
for f in /etc/listen/sources/*.env; do
  [ -e "\$f" ] || continue
  slug=\$(basename "\$f" .env)
  case " $slugs " in *" \$slug "*) ;; *)
    echo "removing source \$slug"
    systemctl disable --now --quiet "listen-listener@\$slug" || true
    rm -f "\$f" ;;
  esac
done
install -m 0644 $stage/sources/*.env /etc/listen/sources/
systemctl daemon-reload
for slug in $slugs; do
  systemctl enable --quiet "listen-listener@\$slug"
  systemctl restart "listen-listener@\$slug"
done

if ! cmp -s $stage/listen.conf /etc/php/8.3/fpm/pool.d/listen.conf; then
  install -m 0644 $stage/listen.conf /etc/php/8.3/fpm/pool.d/listen.conf
  php-fpm8.3 -t
  systemctl reload php8.3-fpm   # graceful; other pools keep serving
fi
install -m 0644 $stage/listen-sources.conf /etc/nginx/snippets/listen-sources.conf
install -m 0644 $stage/listen.home.arpa.conf /etc/nginx/sites-available/listen.home.arpa
ln -sf /etc/nginx/sites-available/listen.home.arpa /etc/nginx/sites-enabled/listen.home.arpa
nginx -t -q
systemctl reload nginx
rm -rf $stage
for slug in $slugs; do
  printf '%-22s %s\n' "\$slug" "\$(systemctl is-active listen-listener@\$slug)"
done
SH
echo "done: https://listen.home.arpa/"
