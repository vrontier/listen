#!/usr/bin/env bash
# Applies site/_config/sources.json to a Linux server with systemd and nginx
# (deploy/observatory/INSTALL.md, step 4 and "Changing sources later"). Run as
# root from the release archive's folder or a clone:
#   sudo scripts/apply-sources.sh
#
# It writes each source's settings to /etc/listen/sources/<slug>.env and
# restarts the listeners whose settings changed, starts new ones, stops and
# removes those of sources taken out, installs the routes as
# /etc/nginx/snippets/listen-sources.conf and reloads nginx when it accepts
# them, and copies sources.json to the website.
#
#   OBSERVATORY_WEB     the website's directory (default /var/www/observatory)
#   OBSERVATORY_LIMITS  nginx directives for every route (default: the limits
#                       of deploy/observatory/observatory-limits.conf); empty: none
#   NODE_MODE, NODE_IDLE_AFTER, NODE_INGEST  as in scripts/gen-sources.sh
# Idempotent.
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $(id -u) -eq 0 ]] || { echo "run as root (sudo)" >&2; exit 1; }

web="${OBSERVATORY_WEB:-/var/www/observatory}"
limits="${OBSERVATORY_LIMITS- limit_req zone=obs burst=20 nodelay; limit_conn obs_conn 20;}"
etc=/etc/listen/sources
snip=/etc/nginx/snippets/listen-sources.conf

gen=$(mktemp -d)
trap 'rm -rf "$gen"' EXIT
scripts/gen-sources.sh "$gen/out" "$limits"
slugs=$(cat "$gen/out/slugs")

install -d -m 0755 "$etc"
# Sources taken out: stop their listener and remove its settings. Their
# memory stays in /var/lib/listen/memory/<slug>/.
for f in "$etc"/*.env; do
  [[ -e $f ]] || continue
  slug=$(basename "$f" .env)
  case " $slugs " in *" $slug "*) ;; *)
    echo "removing $slug"
    systemctl disable --now --quiet "listen-listener@$slug" || true
    rm -f "$f" ;;
  esac
done

changed=() new=()
for slug in $slugs; do
  if [[ ! -f $etc/$slug.env ]]; then
    new+=("$slug")
  elif cmp -s "$gen/out/sources/$slug.env" "$etc/$slug.env"; then
    continue
  else
    changed+=("$slug")
  fi
  install -m 0644 "$gen/out/sources/$slug.env" "$etc/$slug.env"
done
systemctl daemon-reload
for slug in $slugs; do
  systemctl enable --quiet "listen-listener@$slug"
  case " ${changed[*]-} " in
    *" $slug "*) echo "restarting $slug (settings changed)"; systemctl restart "listen-listener@$slug" ;;
    *) case " ${new[*]-} " in *" $slug "*) echo "starting $slug" ;; esac
       systemctl start "listen-listener@$slug" ;;
  esac
done

if [[ -d $web/_config ]]; then
  install -m 0644 site/_config/sources.json "$web/_config/sources.json"
fi

# The routes: the previous file comes back if nginx refuses the new one.
[[ -f $snip ]] && cp -p "$snip" "$gen/prev.conf"
install -m 0644 "$gen/out/listen-sources.conf" "$snip"
if nginx -t -q; then
  systemctl reload nginx
else
  echo "nginx -t failed: the previous routes are restored, nginx is not reloaded" >&2
  if [[ -f $gen/prev.conf ]]; then cp -p "$gen/prev.conf" "$snip"; else rm -f "$snip"; fi
  exit 1
fi

sleep 2
for slug in $slugs; do
  printf '%-24s %s\n' "$slug" "$(systemctl is-active "listen-listener@$slug")"
done
