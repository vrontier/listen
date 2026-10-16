#!/usr/bin/env bash
# Installs listen-engine, the Workbench's engine process, on a host with
# passwordless sudo (the Workbench's intranet host; max since 2026-10-16).
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

ssh "$host" "rm -rf $stage && mkdir -p $stage"
scp -q bin/listen-engine-linux-amd64 deploy/systemd/listen-engine.service "$host:$stage/"
ssh "$host" "sudo bash -s" <<EOF
set -euo pipefail
id listen-engine >/dev/null 2>&1 || useradd --system --home-dir /var/lib/listen-engine --shell /usr/sbin/nologin listen-engine
if id "$wbuser" >/dev/null 2>&1; then usermod -aG listen-engine "$wbuser"; fi
install -m 0755 -o root -g root $stage/listen-engine-linux-amd64 /usr/local/bin/listen-engine.new
mv -f /usr/local/bin/listen-engine.new /usr/local/bin/listen-engine
install -m 0644 -o root -g root $stage/listen-engine.service /etc/systemd/system/listen-engine.service
install -d -m 0750 -o root -g listen-engine /etc/listen-engine
systemctl daemon-reload
systemctl enable --quiet listen-engine
systemctl restart listen-engine
for i in \$(seq 1 20); do [ -S /run/listen-engine/control.sock ] && break; sleep 0.25; done
ls -l /run/listen-engine/control.sock
curl -s --unix-socket /run/listen-engine/control.sock http://engine/v1/engine | head -c 300; echo
rm -rf $stage
EOF
echo "listen-engine on $host: $(ssh "$host" systemctl is-active listen-engine)"
