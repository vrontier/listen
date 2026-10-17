#!/usr/bin/env bash
# Root step of the Workbench's deployment on ionos (low.vrontier.org).
# Staged by scripts/deploy-ionos-workbench.sh into ~questmaster/low-deploy; run
# as root:
#   bash /home/questmaster/low-deploy/install-workbench.sh
# Idempotent: safe to run again after every new staging.
#
# It installs listen-engine (always) and the Workbench (when staged). It needs
# listen-ingest, which the Observatory's root step installs. It touches no
# nginx file, no certificate and nothing of the Observatory: the site for
# low.vrontier.org is the administrator's.
set -euo pipefail
[[ $(id -u) -eq 0 ]] || { echo "run as root" >&2; exit 1; }
stage="$(cd "$(dirname "$0")" && pwd)"
step() { printf '\n== %s\n' "$*"; }

# install_bin <staged file> <target>: replaces the program only when it
# differs; prints "changed" if it did.
install_bin() {
  if cmp -s "$1" "$2"; then return; fi
  install -m 0755 -o root -g root "$1" "$2.new"
  mv -f "$2.new" "$2"
  echo changed
}
install_unit() {
  if cmp -s "$1" "$2"; then return; fi
  install -m 0644 -o root -g root "$1" "$2"
  echo changed
}

step "listen-ingest (from the Observatory's step)"
[[ -S /run/listen-ingest/feeds.sock ]] || { echo "listen-ingest is not running; install it first (~questmaster/listen-deploy/install.sh)" >&2; exit 1; }
systemctl is-active listen-ingest

step "listen-engine (decodes and analyses for the Workbench; reads through listen-ingest)"
id listen-engine >/dev/null 2>&1 || useradd --system --home-dir /var/lib/listen-engine --no-create-home --shell /usr/sbin/nologin listen-engine
usermod -aG listen-ingest listen-engine
changed=$(install_bin "$stage/listen-engine" /usr/local/bin/listen-engine)
changed+=$(install_unit "$stage/listen-engine.service" /etc/systemd/system/listen-engine.service)
install -d -m 0750 -o root -g listen-engine /etc/listen-engine
[[ -f /etc/listen-engine/engine.env ]] || install -m 0640 -o root -g listen-engine /dev/stdin /etc/listen-engine/engine.env <<<'ENGINE_ARGS=""'
systemctl daemon-reload
systemctl enable --quiet listen-engine
if [[ -n $changed ]] || ! systemctl is-active --quiet listen-engine; then
  systemctl restart listen-engine
else
  echo "unchanged, not restarted"
fi
for i in $(seq 1 20); do [[ -S /run/listen-engine/control.sock ]] && break; sleep 0.25; done
ls -l /run/listen-engine/control.sock
curl -fsS --unix-socket /run/listen-engine/control.sock http://engine/v1/engine | head -c 200; echo

if [[ -f $stage/low-workbench ]]; then
  step "the Workbench (low-workbench on 127.0.0.1:8110)"
  id low >/dev/null 2>&1 || useradd --system --home-dir /var/lib/low --no-create-home --shell /usr/sbin/nologin --user-group low
  usermod -aG listen-engine low
  changed=$(install_bin "$stage/low-workbench" /usr/local/bin/low-workbench)
  changed+=$(install_unit "$stage/low-workbench.service" /etc/systemd/system/low-workbench.service)

  # Settings: written once; the mail lines follow the staging (a changed
  # password comes with a new staging), everything else stays as it is.
  install -d -m 0750 -o root -g low /etc/low
  env=/etc/low/workbench.env
  if [[ ! -f $env ]]; then
    echo "creating $env"
    install -m 0640 -o root -g low /dev/null "$env"
    cat >"$env" <<'EOF'
# Workbench settings, see /etc/systemd/system/low-workbench.service.
LOW_BASE_URL=https://low.vrontier.org
# Let members publish analyses at /research (off unless "on"):
# LOW_PUBLISHING=on
# Offer members the page for their language-model keys (off unless "on"):
# LOW_LLM_KEYS=on
LOW_RESERVED_INPUTS=/etc/low/reserved-inputs
EOF
  fi
  # The sealing key of members' language-model keys, made here once. Without
  # it, saved keys cannot be opened again: back it up, apart from the data.
  if ! grep -q '^LOW_KEYS_SECRET=' "$env"; then
    echo "creating LOW_KEYS_SECRET in $env (back it up separately)"
    echo "LOW_KEYS_SECRET=$(openssl rand -base64 32)" >>"$env"
  fi
  # Mail for login links and invitations.
  if [[ -f $stage/smtp.env ]]; then
    before=$(grep '^LOW_SMTP' "$env" || true)
    sed -i '/^LOW_SMTP/d' "$env"
    grep '^LOW_SMTP' "$stage/smtp.env" >>"$env"
    [[ $before == "$(grep '^LOW_SMTP' "$env")" ]] || { echo "mail settings written"; changed+=yes; }
  fi
  chown root:low "$env"
  chmod 0640 "$env"
  # Stream addresses the Observatory keeps for itself, which members cannot
  # register; one host or address per line. Empty: none.
  reserved=/etc/low/reserved-inputs
  [[ -f $reserved ]] || printf '# Addresses the Listening Observatory keeps for itself: one host or address per line.\n' >"$reserved"
  chown root:low "$reserved"
  chmod 0640 "$reserved"

  systemctl daemon-reload
  systemctl enable --quiet low-workbench
  if [[ -n $changed ]] || ! systemctl is-active --quiet low-workbench; then
    systemctl restart low-workbench
  else
    echo "unchanged, not restarted"
  fi
  for i in $(seq 1 20); do curl -fs -o /dev/null http://127.0.0.1:8110/healthz && break; sleep 0.5; done
  curl -fsS -o /dev/null -w 'healthz %{http_code}\n' http://127.0.0.1:8110/healthz

  # The master admin, once: created without printing a login link; they log
  # in by mail at https://low.vrontier.org/login.
  if [[ -f $stage/master-admin ]]; then
    # Two lines, MASTER_EMAIL=… and MASTER_NAME=…; read, not run.
    email=$(sed -n 's/^MASTER_EMAIL=//p' "$stage/master-admin")
    name=$(sed -n 's/^MASTER_NAME=//p' "$stage/master-admin")
    runuser -u low -- /usr/local/bin/low-workbench -db /var/lib/low/workbench.db admin -email "$email" -name "$name" >/dev/null
    echo "master admin: $email"
  fi
fi

step "status"
for u in listen-ingest listen-engine low-workbench; do
  systemctl cat "$u" >/dev/null 2>&1 || continue
  printf '%-16s %s\n' "$u" "$(systemctl is-active "$u")"
done
