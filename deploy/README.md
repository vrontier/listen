# Deployment: listen.home.arpa (taurus)

Updates: `scripts/deploy-taurus.sh`. It builds a static linux/amd64 listener,
syncs `site/` (including the local, uncommitted `site/_config/source.php`) and
installs the files below. It is idempotent.

| Piece | Where on taurus | Source |
|---|---|---|
| Listener binary | `/usr/local/bin/listen-listener` | `listener/` |
| systemd unit | `/etc/systemd/system/listen-listener.service` | `systemd/listen-listener.service` |
| Listener input | `/etc/default/listen-listener` (`LISTEN_ARGS`, installed once, then edited on the host) | `systemd/listen-listener.default` |
| Listener state | `/var/lib/listen` (user `listen`, 0700; captures in `samples/`) | — |
| Site | `/var/www/listen.home.arpa` | `site/` |
| PHP-FPM pool | `/etc/php/8.3/fpm/pool.d/listen.conf`, socket `/run/php/php8.3-fpm-listen.sock` | `php-fpm/listen.conf` |
| nginx vhost | `/etc/nginx/sites-available/listen.home.arpa` | `nginx/listen.home.arpa.conf` |
| TLS | `/etc/ssl/home-arpa/listen.{key,csr,crt}` | — |

The listener listens on `127.0.0.1:8095` (8080 belongs to llama-server). nginx
proxies `/ws/live` and `/api/state/current` to it; everything else goes to the
PHP front controller in its own FPM pool (`open_basedir` limited to the site).
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

Off by default, and gated twice. The listener needs `-audio` in `LISTEN_ARGS`,
and the site needs `'audio' => true` in `site/_config/source.php`. Only when
both are set does `/live` show a *listen* button. The listener then encodes
AAC and Opus from the same decode it analyses and serves them on `/ws/audio`,
and the page holds each event back until its audio is heard.
Only enable it where the source's terms allow redistribution.

## Switching the input

Edit `LISTEN_ARGS` in `/etc/default/listen-listener`, then run
`sudo systemctl restart listen-listener`. A file input replays with the clock
pinned to the recording time, taken from a `YYYYMMDDTHHMMSSZ` stamp in the file
name or from `-start`. A URL input runs live.
