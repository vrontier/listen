#!/usr/bin/env bash
# Deploy listener and site to taurus (listen.home.arpa). Idempotent; run from
# anywhere in the repo. One-time host setup is described in deploy/README.md.
set -euo pipefail
cd "$(dirname "$0")/.."

host="${DEPLOY_HOST:-taurus-mike}"
stage=/tmp/listen-deploy

echo "building listener (linux/amd64, static)"
(cd listener && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -trimpath -o ../bin/listen-listener-linux-amd64 ./cmd/listener)

ssh "$host" "rm -rf $stage && mkdir -p $stage"
scp -q bin/listen-listener-linux-amd64 deploy/systemd/listen-listener.service \
  deploy/systemd/listen-listener.default deploy/nginx/listen.home.arpa.conf \
  deploy/php-fpm/listen.conf "$host:$stage/"

echo "syncing site"
rsync -rlt --delete --exclude .DS_Store \
  --rsync-path="sudo rsync" site/ "$host:/var/www/listen.home.arpa/"

echo "installing"
ssh "$host" "sudo bash -s" <<SH
set -euo pipefail
install -m 0755 $stage/listen-listener-linux-amd64 /usr/local/bin/listen-listener
install -m 0644 $stage/listen-listener.service /etc/systemd/system/listen-listener.service
[ -f /etc/default/listen-listener ] || install -m 0644 $stage/listen-listener.default /etc/default/listen-listener
if ! cmp -s $stage/listen.conf /etc/php/8.3/fpm/pool.d/listen.conf; then
  install -m 0644 $stage/listen.conf /etc/php/8.3/fpm/pool.d/listen.conf
  php-fpm8.3 -t
  systemctl reload php8.3-fpm   # graceful; other pools keep serving
fi
install -m 0644 $stage/listen.home.arpa.conf /etc/nginx/sites-available/listen.home.arpa
ln -sf /etc/nginx/sites-available/listen.home.arpa /etc/nginx/sites-enabled/listen.home.arpa
chown -R root:root /var/www/listen.home.arpa
find /var/www/listen.home.arpa -type d -exec chmod 0755 {} +
find /var/www/listen.home.arpa -type f -exec chmod 0644 {} +
systemctl daemon-reload
systemctl enable --quiet listen-listener
systemctl restart listen-listener
nginx -t -q
systemctl reload nginx
rm -rf $stage
systemctl --no-pager --lines=0 status listen-listener | head -3
SH
echo "done: https://listen.home.arpa/live"
