# listen.vrontier.org: admin step

Listening Observatory (https://listen.vrontier.org) needs one root step on this server.
`questmaster` has already staged everything in `/home/questmaster/listen-deploy/`.
Run one script there as root. It is idempotent and touches only `listen.*` files.

```sh
sudo -i          # or log in as root
bash /home/questmaster/listen-deploy/install.sh
```

The script runs about a minute (longer if ffmpeg has to be installed). It ends with a
status line per source, which should read `active`.

## What the script changes

| Area | Change |
|---|---|
| Packages | Installs `ffmpeg` (apt) if missing. The listeners use it to decode the audio. |
| User | Creates the system user `listen` (nologin, home `/var/lib/listen`). The listeners run as this user. |
| Binary | Installs `/usr/local/bin/listen-listener`, a static Go binary. |
| systemd | Installs the template `/etc/systemd/system/listen-listener@.service` and enables and restarts one instance per source: `listen-listener@<slug>`. Each instance listens on `127.0.0.1` only (ports 8095–8098), with a hardened unit (ProtectSystem=strict, no capabilities). |
| Config | Writes `/etc/listen/sources/<slug>.env` (one per source; sources no longer configured are stopped and removed). Writes `/etc/listen/llm.env` (0600) on the first run only, with the LiteLLM key copied from `/home/mike/.env` (`LITELLM_API_KEY`) for the interpretation text. Without a key that layer stays off. |
| Data | Installs the archive audio files in `/var/lib/listen/samples/` (listen:listen 0640). The listeners keep their memory in `/var/lib/listen/memory/`. Creates `/var/lib/listen-web/` (listen-web, 0700) for the contact form's rate limit. |
| Site | Syncs `/var/www/listen.vrontier.org` (owner questmaster:www-data, 2755/0644). Installs `_config/mail.php` (SMTP credentials of listening.observatory@vrontier.org) as root:listen-web 0640. nginx never serves `/_…`. |
| PHP-FPM | Replaces `/etc/php/8.3/fpm/pool.d/listen.conf` (backup `listen.conf.bak-<date>`). Same pool and user `listen-web`; open_basedir adds `/var/lib/listen-web/`, and `clear_env = yes` is set. Then a graceful `systemctl reload php8.3-fpm`. |
| nginx | Replaces `/etc/nginx/sites-available/listen.vrontier.org` (backup `.bak-<date>` next to it) and adds `/etc/nginx/snippets/listen-sources.conf`. Hardening and honeypots stay as they were. New in the vhost: WebSocket and read-only API proxies under `/<slug>/ws/` and `/<slug>/api/` to the local listeners, and POST, accepted only on `/contact` (body ≤ 32 KB). Then `nginx -t` and a reload. If `nginx -t` fails, the old vhost is restored and nothing is reloaded. |

No other vhost, pool or service is touched. Outbound connections:

- the listeners: the live streams (HTTPS) and the LiteLLM proxy on this server;
- PHP: smtp.ionos.de:587.

## Check

```sh
systemctl status 'listen-listener@*' --no-pager | grep -E '●|Active'
journalctl -u 'listen-listener@*' -n 20 --no-pager
curl -s https://listen.vrontier.org/ -o /dev/null -w '%{http_code}\n'       # 200
```

## Undo

```sh
systemctl disable --now 'listen-listener@*'
cp -p /etc/nginx/sites-available/listen.vrontier.org.bak-<date> /etc/nginx/sites-available/listen.vrontier.org
rm /etc/nginx/snippets/listen-sources.conf
nginx -t && systemctl reload nginx
cp -p /etc/php/8.3/fpm/pool.d/listen.conf.bak-<date> /etc/php/8.3/fpm/pool.d/listen.conf
systemctl reload php8.3-fpm
```

## Later deployments

- **Site only** (pages, styles, scripts): questmaster deploys these without root
  (`scripts/deploy-ionos.sh --site-only`).
- **New listener version, a new source or changed SMTP credentials:** questmaster stages
  again (`scripts/deploy-ionos.sh`), and an admin runs the same `install.sh` again.

Source: https://github.com/vrontier/listen (`deploy/`, `scripts/deploy-ionos.sh`).
