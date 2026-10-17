# Installing your own Listening Observatory

This guide sets up a Listening Observatory of your own: a website with one live page per audio stream, each analysed by its own listener. It is written for the release archive `listen-observatory_<version>_<system>.tar.gz`, which holds the programs, the website and the templates named below. From a clone of the repository the same files are in `listener/` (build the programs with Go), `site/`, `scripts/` and `deploy/observatory/`.

## Before you start: whose streams?

Every running Observatory opens its own connection to each stream it listens to, and its pages can relay the sound. **Ask the operator of a stream before you add it**, and record their answer (the `rights` field in `sources.json` is for that). A stream you run yourself, or a recording you hold the rights to, needs no one's permission. The Observatory never stores audio.

## What you need

- **ffmpeg** (decoding) and **jq** (the scripts), on every system.
- **PHP 8**, for the website.
- For a server: **Linux with systemd**, **nginx**, **PHP-FPM** and an address (a domain name) pointing to it. The programs also run on macOS and Windows, but the server setup below is for Linux; on Windows, use the Windows Subsystem for Linux for the scripts.

## Try it on your computer

1. Unpack the archive and change into its folder.
2. Copy `site/_config/sources.example.json` to `site/_config/sources.json` and describe one stream: its `slug` (the page's address), name, place, time zone, and in `listener` a free port (8095 to 8109) and the stream's address as `input`. A local audio file works as well, with `"args": ["-loop"]`.
3. Run `scripts/dev.sh`. It starts one listener per source and the website on PHP's built-in server.
4. Open <http://127.0.0.1:8000/>. After a few seconds of listening, the page starts to draw.

Stop it with Ctrl-C. Memories (what the listener has learned to recognise) are kept in `bin/dev-memory/`.

## Install it on a Linux server

The steps below use `/var/www/observatory` for the website and `observatory.example.org` as its address; use your own. Run them from the unpacked archive's folder.

**1. Programs and users.** The listeners run as `listen`, the website's PHP as `observatory-web`; neither can log in.

```sh
sudo apt-get install ffmpeg jq rsync nginx php-fpm certbot
sudo install -m 0755 bin/listen-listener /usr/local/bin/listen-listener
sudo useradd --system --home-dir /var/lib/listen --no-create-home --shell /usr/sbin/nologin listen
sudo useradd --system --no-create-home --shell /usr/sbin/nologin observatory-web
sudo install -d -o listen -g listen -m 0700 /var/lib/listen
```

Only ports 80 and 443 (and SSH) need to be open to the outside; the listeners answer on 127.0.0.1 only. A fresh nginx tells every visitor its version: uncomment `server_tokens off;` in `/etc/nginx/nginx.conf`.

**2. Your sources.** If you tried it on your computer, bring your `site/_config/sources.json` along. Otherwise copy `site/_config/sources.example.json` to `site/_config/sources.json` now and describe your streams as in "Try it" above; the next steps need it. A local recording must be readable by the user `listen`, for example in `/var/lib/listen/samples/` (create it with `sudo install -d -o listen -g listen -m 0700 /var/lib/listen/samples`).

**3. The website.**

```sh
sudo install -d -m 0755 /var/www/observatory
sudo rsync -r --exclude _config/mail.php site/ /var/www/observatory/
ls /var/www/observatory/_config/
```

`_config/` should now hold `sources.json` and the `*.example.*` files, nothing else; the contact form's settings come in step 6.

**4. One listener per source, and their routes.**

```sh
sudo install -m 0644 systemd/listen-listener@.service /etc/systemd/system/
sudo install -m 0644 deploy/observatory-limits.conf /etc/nginx/conf.d/
sudo scripts/apply-sources.sh
```

`scripts/apply-sources.sh` reads `site/_config/sources.json`, writes each source's settings to `/etc/listen/sources/<slug>.env`, starts one listener per source (`listen-listener@<slug>`) and installs their routes as `/etc/nginx/snippets/listen-sources.conf`, with the per-visitor limits of `observatory-limits.conf` (10 requests a second, 20 connections at a time). It ends with one line per source, which should read `active`.

Each listener answers on its port on 127.0.0.1 only. By default they listen continuously; `sudo NODE_MODE=on-demand scripts/apply-sources.sh` makes them connect only while someone has the page open. If you also run [`listen-ingest`](https://github.com/vrontier/listen/tree/main/ingest), `sudo NODE_INGEST=/run/listen-ingest/feeds.sock scripts/apply-sources.sh` makes every listener read through it, so that each stream is connected once on the machine. The script's header lists its other settings.

**5. The certificate.** Let's Encrypt checks that the address is yours over port 80, at the first certificate and at every renewal. A small site for that check comes first, then the certificate:

```sh
sudo install -d -m 0755 /var/www/letsencrypt
printf 'server {\n    listen 80;\n    listen [::]:80;\n    server_name observatory.example.org;\n    location ^~ /.well-known/acme-challenge/ { root /var/www/letsencrypt; }\n}\n' | sudo tee /etc/nginx/sites-available/observatory >/dev/null
sudo ln -sfn /etc/nginx/sites-available/observatory /etc/nginx/sites-enabled/observatory
sudo nginx -t && sudo systemctl reload nginx
sudo certbot certonly --webroot -w /var/www/letsencrypt -d observatory.example.org --deploy-hook 'systemctl reload nginx'
```

certbot renews the certificate by itself and reloads nginx after each renewal; the site of the next step keeps the check open.

**6. PHP and nginx.** Use your PHP version in the paths (`ls /etc/php` shows it; 8.3 on Ubuntu 24.04), and your address in the site:

```sh
sudo install -m 0644 deploy/php-fpm-pool.conf /etc/php/8.3/fpm/pool.d/observatory.conf
sudo php-fpm8.3 -t && sudo systemctl reload php8.3-fpm
for f in deploy/snippets/*.conf; do [ -e "/etc/nginx/snippets/${f##*/}" ] || sudo install -m 0644 "$f" /etc/nginx/snippets/; done
sudo sed 's/observatory\.example\.org/your.address.org/g' deploy/nginx-site.conf | sudo tee /etc/nginx/sites-available/observatory >/dev/null
sudo nginx -t && sudo systemctl reload nginx
```

`reload` lets the other sites on the machine keep serving. Snippets of the same name that your server already has are kept. If you chose another directory than `/var/www/observatory`, change it in the pool, in the site and in `OBSERVATORY_WEB` for `scripts/apply-sources.sh`.

**7. Optional: the contact form.** Without it the page shows an address instead of the form.

```sh
cp site/_config/mail.example.php mail.php
openssl rand -hex 32        # the form's secret
# edit mail.php: your mail account, and the secret above in 'secret'
sudo install -o root -g observatory-web -m 0640 mail.php /var/www/observatory/_config/mail.php
sudo install -d -o observatory-web -g observatory-web -m 0700 /var/lib/listen-web
rm mail.php
```

Only PHP's own user can read the file, so no other site on the machine sees the mail password. The form stays off while the secret is the example's placeholder or shorter than 32 characters. `/var/lib/listen-web` keeps a hash of each visitor's address for an hour, to limit submissions to five per hour per visitor.

**8. Optional: the narrator.** A language model can turn what the listeners detect into short sentences. Put `LLM_API_KEY`, `NARRATOR_URL` (any OpenAI-compatible endpoint) and `NARRATOR_MODEL` into `/etc/listen/llm.env` (root only, mode 0600) and restart the listeners (`sudo systemctl restart 'listen-listener@*'`). Without the file the narrator stays off.

**9. Optional: an AI assistant.** `bin/listen-mcp` gives an assistant read-only access to the listeners over MCP, typically through SSH. Install it on the server (`sudo install -m 0755 bin/listen-mcp /usr/local/bin/`) and tell it where your sources are: `{"mcpServers": {"listen": {"command": "ssh", "args": ["<host>", "listen-mcp", "-sources", "/var/www/observatory/_config/sources.json"]}}}`.

## Changing sources later

Edit `site/_config/sources.json` and run `sudo scripts/apply-sources.sh` again. It restarts the listeners whose settings changed, starts new ones, stops those of sources you took out and removes their settings, reloads nginx with the new routes (or keeps the old ones if nginx refuses them) and copies `sources.json` to the website. A listener keeps its memory per source and input under `/var/lib/listen/memory/<slug>/`, so changing a source's input starts a new memory, and changing back resumes the old one; the memory of a source taken out stays there until you remove it.

## Beyond this guide

A public server attracts scanners. Blocking them (fail2ban, CrowdSec, a list of known bad user agents) is your server's policy rather than part of the Observatory; their documentation is the place to start.

## Next to the Workbench

The Observatory and Listening Observatory Workbench can run on the same machine, each under its own subdomain. The listeners take ports 8095 to 8109 and the Workbench 8110; see [`deploy/README.md`](https://github.com/vrontier/listen/blob/main/deploy/README.md) in the repository.
