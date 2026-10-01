# Deployment: listen.home.arpa (taurus)

Updates: `scripts/deploy-taurus.sh`. It builds a static linux/amd64 listener,
syncs `site/` (including `site/_config/sources.json` and the uncommitted,
generated `mail.php`) and installs the files below. It is idempotent. Production
(listen.vrontier.org) has no sudo for the deploy account; see
[`ionos/INFOS_listen_vrontier.md`](ionos/INFOS_listen_vrontier.md) and `scripts/deploy-ionos.sh`.

| Piece | Where on taurus | Source |
|---|---|---|
| Listener binary | `/usr/local/bin/listen-listener` | `listener/` |
| systemd template | `/etc/systemd/system/listen-listener@.service`, one instance per source | `systemd/listen-listener@.service` |
| Per-source arguments | `/etc/listen/sources/<slug>.env` (generated: port, input, flags) | `site/_config/sources.json` |
| Per-source routes | `/etc/nginx/snippets/listen-sources.conf` (generated: `/<slug>/ws/…`, `/<slug>/api/state/current`) | `site/_config/sources.json` |
| Listener state | `/var/lib/listen` (user `listen`, 0700; captures in `samples/`, memory in `memory/<source>/<input>/`) | — |
| Site | `/var/www/listen.home.arpa` | `site/` |
| PHP-FPM pool | `/etc/php/8.3/fpm/pool.d/listen.conf`, socket `/run/php/php8.3-fpm-listen.sock` | `php-fpm/listen.conf` |
| nginx vhost | `/etc/nginx/sites-available/listen.home.arpa` | `nginx/listen.home.arpa.conf` |
| TLS | `/etc/ssl/home-arpa/listen.{key,csr,crt}` | — |
| Contact form | `site/_config/mail.php` (generated from `.email` by `scripts/mail-config.sh`, deployed with the site, root:www-data 0640 in a directory nginx never serves); rate limit in `/var/lib/listen-web` (www-data, 0700) | `.email` (gitignored) |
| Narrator credentials | `/etc/listen/llm.env` (root, 0600): `LLM_API_KEY`, `NARRATOR_URL`, `NARRATOR_MODEL`; optional, narrator off without it | — |

Each listener instance listens on its port from `sources.json` on 127.0.0.1
(8095 and up; 8080 belongs to llama-server). nginx proxies `/<slug>/ws/…` and
`/<slug>/api/state/current` to it; everything else goes to the PHP front controller in its
own FPM pool (`open_basedir` limited to the site and `/var/lib/listen-web`).
Access is limited to the home LAN and WireGuard.

## One-time host setup (done 2026-09-30)

```sh
sudo apt-get install ffmpeg
sudo useradd --system --home-dir /var/lib/listen --no-create-home --shell /usr/sbin/nologin listen
sudo install -d -o listen -g listen -m 0700 /var/lib/listen /var/lib/listen/samples
sudo install -d -m 0755 /var/www/listen.home.arpa
```

TLS: leaf for `listen.home.arpa` signed by the home.arpa Internal CA (on nubes),
valid until 2027-11-01. The key was generated on taurus and never left it.
Renewal: copy `/etc/ssl/home-arpa/listen.csr` to nubes and sign it with the CA
(SAN `DNS:listen.home.arpa`, `keyUsage=critical,digitalSignature,keyEncipherment`,
`extendedKeyUsage=serverAuth`, `basicConstraints=critical,CA:false`, `-days 397`,
random serial). Then install the new `listen.crt` on taurus and reload nginx.

## Audio playback

Per source, `"audio": true` in `sources.json` turns on both halves: the listener
runs with `-audio` (encoding AAC and Opus from the same decode it analyses, served
on `/<slug>/ws/audio`) and the page shows the *listen* button, holding each event
back until its sound is heard. Only enable it where the source's terms allow it.

## Adding or changing a source

Edit `site/_config/sources.json` and run `scripts/deploy-taurus.sh`. The script
checks the file (slugs, unique ports, no spaces in inputs or flags), writes one env
file and one pair of nginx routes per source, starts or restarts
`listen-listener@<slug>` for every source and removes instances whose source is gone.
Memory is kept per source and input under `/var/lib/listen/memory/<slug>/`, so a
changed input starts a fresh memory and switching back resumes the old one.
A file input replays with the clock pinned to the recording time (from a
`YYYYMMDDTHHMMSSZ` stamp in the file name); a URL input runs live.
