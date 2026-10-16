#!/usr/bin/env bash
# Installs listen-ingest (the machine's one connection to each station) and
# listen-engine (the Workbench's engine process, reading through the ingest)
# on a host with passwordless sudo (the Workbench's intranet host; max since
# 2026-10-16).
# Idempotent; run from anywhere in the repo.
#   scripts/deploy-engine.sh                 # host max-mike, Workbench user low
#   DEPLOY_HOST=… WORKBENCH_USER=… scripts/deploy-engine.sh
#
# It sets up the user and group listen-engine, the binary, the unit (socket
# /run/listen-engine/control.sock, memories and saved configuration in
# /var/lib/listen-engine) and adds the Workbench's user to the group. Pointing
# the Workbench at the socket is the Workbench deploy's business. Flags for the
# engine (e.g. -allow-host) go into /etc/listen-engine/engine.env on the host.
set -euo pipefail
cd "$(dirname "$0")/.."

host="${DEPLOY_HOST:-max-mike}"
wbuser="${WORKBENCH_USER:-low}"
stage=/tmp/listen-engine-deploy

scripts/build-listener.sh bin/listen-engine-linux-amd64 listen-engine
scripts/build-listener.sh bin/listen-ingest-linux-amd64 listen-ingest

ssh "$host" "rm -rf $stage && mkdir -p $stage"
scp -q bin/listen-engine-linux-amd64 bin/listen-ingest-linux-amd64 deploy/systemd/listen-engine.service \
  deploy/systemd/listen-ingest.service "$host:$stage/"
ssh "$host" "sudo bash -s" <<EOF
set -euo pipefail
id listen-ingest >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin listen-ingest
id listen-engine >/dev/null 2>&1 || useradd --system --home-dir /var/lib/listen-engine --shell /usr/sbin/nologin listen-engine
# The ingest's consumers: the engine, and the Observatory's listeners where they run.
usermod -aG listen-ingest listen-engine
if id listen >/dev/null 2>&1; then usermod -aG listen-ingest listen; fi
install -m 0755 -o root -g root $stage/listen-ingest-linux-amd64 /usr/local/bin/listen-ingest.new
mv -f /usr/local/bin/listen-ingest.new /usr/local/bin/listen-ingest
install -m 0644 -o root -g root $stage/listen-ingest.service /etc/systemd/system/listen-ingest.service
install -d -m 0750 -o root -g listen-ingest /etc/listen-ingest
[ -f /etc/listen-ingest/ingest.env ] || install -m 0640 -o root -g listen-ingest /dev/stdin /etc/listen-ingest/ingest.env <<<'INGEST_ARGS=""'
if id "$wbuser" >/dev/null 2>&1; then usermod -aG listen-engine "$wbuser"; fi
install -m 0755 -o root -g root $stage/listen-engine-linux-amd64 /usr/local/bin/listen-engine.new
mv -f /usr/local/bin/listen-engine.new /usr/local/bin/listen-engine
install -m 0644 -o root -g root $stage/listen-engine.service /etc/systemd/system/listen-engine.service
install -d -m 0750 -o root -g listen-engine /etc/listen-engine
[ -f /etc/listen-engine/engine.env ] || install -m 0640 -o root -g listen-engine /dev/stdin /etc/listen-engine/engine.env <<<'ENGINE_ARGS=""'
systemctl daemon-reload
systemctl enable --quiet listen-ingest listen-engine
systemctl restart listen-ingest
for i in \$(seq 1 20); do [ -S /run/listen-ingest/feeds.sock ] && break; sleep 0.25; done
ls -l /run/listen-ingest/feeds.sock
systemctl restart listen-engine
for i in \$(seq 1 20); do [ -S /run/listen-engine/control.sock ] && break; sleep 0.25; done
ls -l /run/listen-engine/control.sock
curl -s --unix-socket /run/listen-engine/control.sock http://engine/v1/engine | head -c 300; echo
rm -rf $stage
EOF
echo "on $host: listen-ingest $(ssh "$host" systemctl is-active listen-ingest), listen-engine $(ssh "$host" systemctl is-active listen-engine)"
