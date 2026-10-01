#!/usr/bin/env bash
# Root step of the Listening Observatory deployment on ionos (listen.vrontier.org).
# Staged by scripts/deploy-ionos.sh into ~questmaster/listen-deploy; run as root:
#   bash /home/questmaster/listen-deploy/install.sh
# Idempotent: safe to run again after every new staging. It touches only
# listen.* files: its nginx vhost, its FPM pool, its systemd units and its users.
set -euo pipefail
[[ $(id -u) -eq 0 ]] || { echo "run as root" >&2; exit 1; }
stage="$(cd "$(dirname "$0")" && pwd)"
web=/var/www/listen.vrontier.org
owner=questmaster
slugs=$(cat "$stage/slugs")
step() { printf '\n== %s\n' "$*"; }

step "packages"
command -v ffmpeg >/dev/null || { apt-get update -q && apt-get install -y -q ffmpeg; }
php -m | grep -qx mbstring || { apt-get install -y -q php8.3-mbstring && systemctl reload php8.3-fpm; }
command -v rsync >/dev/null || apt-get install -y -q rsync
ffmpeg -version | head -1

step "system user 'listen' (runs the listeners)"
id listen >/dev/null 2>&1 || useradd --system --home-dir /var/lib/listen --no-create-home --shell /usr/sbin/nologin listen
id listen

step "listener binary, unit, per-source settings"
install -m 0755 "$stage/listen-listener" /usr/local/bin/listen-listener
install -m 0644 "$stage/listen-listener@.service" /etc/systemd/system/listen-listener@.service
install -d -m 0755 /etc/listen/sources
for f in /etc/listen/sources/*.env; do
  [ -e "$f" ] || continue
  slug=$(basename "$f" .env)
  case " $slugs " in *" $slug "*) ;; *)
    echo "removing source $slug"
    systemctl disable --now --quiet "listen-listener@$slug" || true
    rm -f "$f" ;;
  esac
done
install -m 0644 "$stage"/sources/*.env /etc/listen/sources/

step "audio files for the archive sources"
install -d -o listen -g listen -m 0750 /var/lib/listen /var/lib/listen/samples
if compgen -G "$stage/samples/*" >/dev/null; then
  install -o listen -g listen -m 0640 "$stage"/samples/* /var/lib/listen/samples/
fi
ls -la /var/lib/listen/samples
# Files no source uses any more stay on disk; list them so they can be removed.
for f in /var/lib/listen/samples/*; do
  [ -e "$f" ] || continue
  grep -qF -- "$f" "$stage"/sources/*.env 2>/dev/null ||
    echo "note: $f is not used by any source (rm it if it is no longer needed)"
done

step "narrator credentials (/etc/listen/llm.env)"
if [[ -f /etc/listen/llm.env ]]; then
  echo "exists, unchanged"
else
  # The key for the LiteLLM proxy on this server is already in mike's .env.
  key=$(sed -n 's/^[[:space:]]*\(export[[:space:]]\+\)\?LITELLM_API_KEY=//p' /home/mike/.env 2>/dev/null | tail -1 | tr -d "\"'")
  if [[ -n $key && -f $stage/narrator.env ]]; then
    umask 077
    { printf 'LLM_API_KEY=%s\n' "$key"; cat "$stage/narrator.env"; } > /etc/listen/llm.env
    chmod 0600 /etc/listen/llm.env
    echo "written (key taken from /home/mike/.env)"
  else
    echo "skipped: no LITELLM_API_KEY in /home/mike/.env; the interpretation layer stays off"
  fi
fi

step "listeners"
systemctl daemon-reload
for slug in $slugs; do
  systemctl enable --quiet "listen-listener@$slug"
  systemctl restart "listen-listener@$slug"
done

step "site files ($web)"
install -d -o listen-web -g listen-web -m 0700 /var/lib/listen-web   # contact form rate limit
rsync -rlt --delete --exclude _config/mail.php "$stage/site/" "$web/"
find "$web" ! -path "$web/_config/mail.php" -exec chown "$owner:www-data" {} +
find "$web" -type d -exec chmod 2755 {} +
find "$web" -type f ! -path "$web/_config/mail.php" -exec chmod 0644 {} +
if [[ -f $stage/site/_config/mail.php ]]; then
  # SMTP credentials: readable by the PHP pool only.
  install -o root -g listen-web -m 0640 "$stage/site/_config/mail.php" "$web/_config/mail.php"
fi

step "PHP-FPM pool 'listen'"
pool=/etc/php/8.3/fpm/pool.d/listen.conf
if ! cmp -s "$stage/listen-pool.conf" "$pool"; then
  cp -p "$pool" "$pool.bak-$(date +%Y%m%d-%H%M%S)" 2>/dev/null || true
  install -m 0644 "$stage/listen-pool.conf" "$pool"
  php-fpm8.3 -t
  systemctl reload php8.3-fpm   # graceful; other pools keep serving
else
  echo "unchanged"
fi

step "nginx"
vhost=/etc/nginx/sites-available/listen.vrontier.org
snip=/etc/nginx/snippets/listen-sources.conf
bak="$vhost.bak-$(date +%Y%m%d-%H%M%S)"
[[ -f $vhost ]] && cp -p "$vhost" "$bak"
[[ -f $snip ]] && cp -p "$snip" "$snip.prev"
install -m 0644 "$stage/listen-sources.conf" "$snip"
install -m 0644 "$stage/listen.vrontier.org.conf" "$vhost"
ln -sf "$vhost" /etc/nginx/sites-enabled/listen.vrontier.org
if ! nginx -t -q; then
  echo "nginx -t failed: restoring the previous vhost; nothing reloaded" >&2
  [[ -f $bak ]] && cp -p "$bak" "$vhost"
  if [[ -f $snip.prev ]]; then mv "$snip.prev" "$snip"; else rm -f "$snip"; fi
  exit 1
fi
rm -f "$snip.prev"
systemctl reload nginx

step "status"
sleep 3
for slug in $slugs; do
  printf '%-22s %s\n' "$slug" "$(systemctl is-active "listen-listener@$slug")"
done
echo
echo "done: https://listen.vrontier.org/"
