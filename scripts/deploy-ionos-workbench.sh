#!/usr/bin/env bash
# Stages the Workbench's side of production (low.vrontier.org on ionos):
# listen-engine and the Workbench. Like scripts/deploy-ionos.sh, it builds and
# stages in ~questmaster/low-deploy, and an admin runs the root step there
# (deploy/ionos/install-workbench.sh). listen-ingest comes with the
# Observatory's staging and must be installed first.
#   scripts/deploy-ionos-workbench.sh --engine-only   # step 2: the engine
#   MASTER_EMAIL=… MASTER_NAME=… scripts/deploy-ionos-workbench.sh
#                                    # step 3: the engine and the Workbench;
#                                    # the master admin is created once
# The Workbench's mail account is the intranet's (the same mailbox): its
# LOW_SMTP* lines are read from /etc/low/workbench.env on the intranet host.
set -euo pipefail
cd "$(dirname "$0")/.."

host="${DEPLOY_HOST:-ionos-questmaster}"
intranet="${INTRANET_HOST:-max-mike}"
stage=bin/ionos-low-stage
workbench=yes
[[ ${1:-} == --engine-only ]] && workbench=

rm -rf "$stage" && mkdir -p "$stage"
scripts/build-listener.sh "$stage/listen-engine" listen-engine
cp deploy/ionos/install-workbench.sh deploy/systemd/listen-engine.service "$stage/"

if [[ -n $workbench ]]; then
  scripts/build-listener.sh "$stage/low-workbench" low-workbench
  cp workbench/deploy/systemd/low-workbench.service "$stage/"
  (umask 077
   ssh "$intranet" "sudo grep '^LOW_SMTP' /etc/low/workbench.env" > "$stage/smtp.env")
  grep -q '^LOW_SMTP_PASSWORD=.' "$stage/smtp.env" || { echo "no mail settings read from $intranet" >&2; exit 1; }
  if [[ -n ${MASTER_EMAIL:-} ]]; then
    [[ -n ${MASTER_NAME:-} ]] || { echo "MASTER_EMAIL needs MASTER_NAME" >&2; exit 1; }
    (umask 077; printf 'MASTER_EMAIL=%s\nMASTER_NAME=%s\n' "$MASTER_EMAIL" "$MASTER_NAME" > "$stage/master-admin")
  fi
fi

echo "staging in $host:~/low-deploy"
ssh "$host" 'mkdir -p ~/low-deploy && chmod 0700 ~/low-deploy'
rsync -rlt --delete "$stage/" "$host:low-deploy/"
ssh "$host" 'chmod -R go= ~/low-deploy'
scp -q deploy/ionos/INFOS_low_vrontier.md "$host:INFOS_low_vrontier.md"
rm -f "$stage/smtp.env"
(cd "$stage" && shasum -a 256 listen-engine ${workbench:+low-workbench})
echo
echo "staged. Root step (admin):"
echo "  bash /home/questmaster/low-deploy/install-workbench.sh"
