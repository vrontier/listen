# Deployment

This repository holds two websites that run side by side on the same machine, each under its own subdomain, and two services they share. Neither website needs the other: a machine can run the Observatory alone, the Workbench alone, or both.

| Part | What it is | Folder |
|---|---|---|
| **Listening Observatory** | the public, curated website: a handful of streams, each analysed one way, no accounts | `site/`, `listener/` |
| **Listening Observatory Workbench** | the research website: members bring their own streams and decide how they are analysed | `workbench/` |
| `listen-ingest` | the one service on a machine that connects to stations; both websites read through it | `ingest/` |
| `listen-engine` | decodes and analyses for the Workbench, on a local socket | `listener/` |

## Two websites, two subdomains

The web server (nginx) has one site per website, each with its own subdomain and certificate, and passes requests to programs that listen on the machine's loopback address only:

```text
listen.<domain> ─► nginx ─► PHP-FPM: the Observatory's pages
                        └─► 127.0.0.1:8095–8109: one listener per source ─► listen-ingest ─► stations
low.<domain>    ─► nginx ─► 127.0.0.1:8110: the Workbench ─► listen-engine ─► listen-ingest
```

| Environment | Observatory | Workbench |
|---|---|---|
| Intranet | `listen.home.arpa` | `low.home.arpa` |
| Production | `listen.vrontier.org` | `low.vrontier.org` (not set up yet) |

Each website keeps its web-server file next to its code: [`nginx/`](nginx/) for the Observatory, [`../workbench/deploy/nginx/`](../workbench/deploy/nginx/) for the Workbench. Because the two are different subdomains, the browser treats them as separate sites: the Workbench's login cookie is never sent to the Observatory, and neither can read the other's pages.

## Port plan

All ports are on 127.0.0.1; nothing but the web server is reachable from outside.

| Port | Used by |
|---|---|
| 8095–8109 | the Observatory's listeners, one per source (the port is set per source in `site/_config/sources.json`) |
| 8110 | the Workbench (`low-workbench`) |
| 8111–8119 | reserved for later Workbench processes |
| (none) | `listen-ingest` (`/run/listen-ingest/feeds.sock`) and `listen-engine` (`/run/listen-engine/control.sock`) use Unix sockets |

A new process takes a port from this plan, and this table is changed in the same commit.

## Installing the shared services

`scripts/deploy-engine.sh` installs `listen-ingest` and `listen-engine` with their users, groups, units and sockets on a host with sudo. The Observatory's listeners read through the ingest when `NODE_INGEST` is set for their deploy; the Workbench reaches the engine through its socket. Units: [`systemd/`](systemd/).

## The Observatory on the intranet (listen.home.arpa, staging on max)

Updates: `scripts/deploy-staging.sh`. It builds a static linux/amd64 listener, syncs `site/` (including the local `site/_config/sources.json` and the uncommitted, generated `mail.php`) and installs the files below. It is idempotent. Production (listen.vrontier.org) has no sudo for the deploy account; see [`ionos/INFOS_listen_vrontier.md`](ionos/INFOS_listen_vrontier.md) and `scripts/deploy-ionos.sh`.

| Piece | Where on the staging host | Source |
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

Each listener instance listens on its port from `sources.json` on 127.0.0.1 (8095 to 8109, see the port plan above; 8080 belongs to another service on that host). nginx proxies `/<slug>/ws/…` and `/<slug>/api/state/current` to it; everything else goes to the PHP front controller in its own FPM pool (`open_basedir` limited to the site and `/var/lib/listen-web`). Access is limited to the home LAN and WireGuard.

Staging listens **on demand**: each listener connects to its source only while a live page is open and disconnects 30 s after the last one closed (`-on-demand -idle-after 30s`, added by `scripts/deploy-staging.sh`). Production listens continuously, so the memory covers every hour, and each provider sees only one connection from us. To compare: `LISTEN_MODE=continuous scripts/deploy-staging.sh`; to change the close time: `IDLE_AFTER=2m scripts/deploy-staging.sh`.

A source can override the node default on every node with `"mode"` (`continuous` or `on-demand`) and `"idle_after"` in its `listener` entry in `sources.json`. Archive loops, which load no provider, can run `continuous` everywhere, so staging always has live analysis and a growing memory for them. Each deploy prints the resulting mode per source.

### One-time host setup (max: done 2026-10-03; taurus before, 2026-09-30)

```sh
sudo apt-get install ffmpeg php8.3-mbstring
sudo useradd --system --home-dir /var/lib/listen --no-create-home --shell /usr/sbin/nologin listen
sudo install -d -o listen -g listen -m 0700 /var/lib/listen /var/lib/listen/samples
sudo install -d -m 0755 /var/www/listen.home.arpa
```

TLS: leaf for `listen.home.arpa` signed by the home.arpa Internal CA (on nubes), valid until 2027-11-04. The key was generated on max and never left it. The staging data (archive samples, memories) and `/etc/listen/llm.env` were copied from taurus on 2026-10-03; taurus no longer runs listen. Renewal: copy `/etc/ssl/home-arpa/listen.csr` to nubes and sign it with the CA (SAN `DNS:listen.home.arpa`, `keyUsage=critical,digitalSignature,keyEncipherment`, `extendedKeyUsage=serverAuth`, `basicConstraints=critical,CA:false`, `-days 397`, random serial). Then install the new `listen.crt` on max and reload nginx.

### Audio playback

Per source, `"audio": true` in `sources.json` turns on both halves: the listener runs with `-audio` (encoding AAC and Opus from the same decode it analyses, served on `/<slug>/ws/audio`) and the page shows the *listen* button, holding each event back until its sound is heard. Only enable it where the source's terms allow it.

### Adding or changing a source

Edit `site/_config/sources.json` and run `scripts/deploy-staging.sh`. The script checks the file (slugs, unique ports, no spaces in inputs or flags), writes one env file and one pair of nginx routes per source, starts or restarts `listen-listener@<slug>` for every source and removes instances whose source is gone. Memory is kept per source and input under `/var/lib/listen/memory/<slug>/`, so a changed input starts a fresh memory and switching back resumes the old one. A file input replays with the clock pinned to the recording time (from a `YYYYMMDDTHHMMSSZ` stamp in the file name); a URL input runs live.
