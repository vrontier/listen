# Installing your own Listening Observatory

This guide sets up a Listening Observatory of your own: a website with one live page per audio stream, each analysed by its own listener. It is written for the release archive `listen-observatory_<version>_<system>.tar.gz`, which holds the programs, the website and the templates named below. From a clone of the repository the same files are in `listener/` (build the programs with Go), `site/`, `scripts/` and `deploy/observatory/`.

## Before you start: whose streams?

Every running Observatory opens its own connection to each stream it listens to, and its pages can relay the sound. **Ask the operator of a stream before you add it**, and record their answer (the `rights` field in `sources.json` is for that). A stream you run yourself, or a recording you hold the rights to, needs no one's permission. The Observatory never stores audio.

## What you need

- **ffmpeg** (decoding) and **jq** (the scripts), on every system.
- **PHP 8** with `mbstring`, for the website.
- For a server: **Linux with systemd**, **nginx** and **PHP-FPM**. The programs also run on macOS and Windows, but the server setup below is for Linux; on Windows, use the Windows Subsystem for Linux for the scripts.

## Try it on your computer

1. Unpack the archive and change into its folder.
2. Copy `site/_config/sources.example.json` to `site/_config/sources.json` and describe one stream: its `slug` (the page's address), name, place, time zone, and in `listener` a free port (8095 to 8109) and the stream's address as `input`. A local audio file works as well, with `"args": ["-loop"]`.
3. Run `scripts/dev.sh`. It starts one listener per source and the website on PHP's built-in server.
4. Open <http://127.0.0.1:8000/>. After a few seconds of listening, the page starts to draw.

Stop it with Ctrl-C. Memories (what the listener has learned to recognise) are kept in `bin/dev-memory/`.

## Install it on a Linux server

The steps below use `/var/www/observatory` for the website and `observatory.example.org` as its address; use your own.

**1. Programs and user.**

```sh
sudo apt-get install ffmpeg jq nginx php-fpm php-mbstring
sudo install -m 0755 bin/listen-listener /usr/local/bin/listen-listener
sudo useradd --system --home-dir /var/lib/listen --no-create-home --shell /usr/sbin/nologin listen
sudo install -d -o listen -g listen -m 0700 /var/lib/listen
```

**2. The website.**

```sh
sudo install -d -m 0755 /var/www/observatory
sudo cp -r site/. /var/www/observatory/
```

`sources.json` in `/var/www/observatory/_config/` is the one the site reads; keep your copy in step 3 and this one the same (copy it again after every change).

**3. One listener per source.** `scripts/gen-sources.sh` reads `site/_config/sources.json` and writes each source's settings and the web server's routes:

```sh
scripts/gen-sources.sh /tmp/observatory-gen
sudo install -d /etc/listen/sources
sudo cp /tmp/observatory-gen/sources/*.env /etc/listen/sources/
sudo cp /tmp/observatory-gen/listen-sources.conf /etc/nginx/snippets/
sudo cp systemd/listen-listener@.service /etc/systemd/system/
sudo systemctl daemon-reload
for slug in $(cat /tmp/observatory-gen/slugs); do sudo systemctl enable --now "listen-listener@$slug"; done
```

Each listener listens on its port on 127.0.0.1 only. By default they listen continuously; `NODE_MODE=on-demand scripts/gen-sources.sh …` makes them connect only while someone has the page open. If you also run [`listen-ingest`](https://github.com/vrontier/listen/tree/main/ingest), `NODE_INGEST=/run/listen-ingest/feeds.sock scripts/gen-sources.sh …` makes every listener read through it, so that each stream is connected once on the machine.

**4. PHP and nginx.**

```sh
sudo cp deploy/php-fpm-pool.conf /etc/php/*/fpm/pool.d/observatory.conf
sudo cp deploy/snippets/*.conf /etc/nginx/snippets/
sudo cp deploy/nginx-site.conf /etc/nginx/sites-available/observatory
sudo ln -s /etc/nginx/sites-available/observatory /etc/nginx/sites-enabled/observatory
# edit the server name, certificate paths and the site's directory in the two files, then
sudo systemctl restart php*-fpm && sudo nginx -t && sudo systemctl reload nginx
```

A certificate for your address, for example from Let's Encrypt, goes where `nginx-site.conf` says.

**5. Optional: the contact form.** Copy `site/_config/mail.example.php` to `/var/www/observatory/_config/mail.php` (readable by the web server only), fill in your mail account, and give it a `state_dir` that PHP may write (for example `/var/lib/listen-web`, owned by `www-data`) to limit submissions to five per hour per visitor. Without the file the page shows an address instead of the form.

**6. Optional: the narrator.** A language model can turn what the listeners detect into short sentences. Put `LLM_API_KEY`, `NARRATOR_URL` (any OpenAI-compatible endpoint) and `NARRATOR_MODEL` into `/etc/listen/llm.env` (root only, mode 0600) and restart the listeners. Without the file the narrator stays off.

**7. Optional: an AI assistant.** `bin/listen-mcp` gives an assistant read-only access to the listeners over MCP, typically through SSH: `{"mcpServers": {"listen": {"command": "ssh", "args": ["<host>", "listen-mcp"]}}}`.

## Changing sources later

Edit `site/_config/sources.json`, copy it to `/var/www/observatory/_config/`, and repeat step 3. Remove the listener of a source you took out with `sudo systemctl disable --now listen-listener@<slug>`. A listener keeps its memory per source and input under `/var/lib/listen/memory/<slug>/`, so changing a source's input starts a new memory, and changing back resumes the old one.

## Next to the Workbench

The Observatory and Listening Observatory Workbench can run on the same machine, each under its own subdomain. The listeners take ports 8095 to 8109 and the Workbench 8110; see [`deploy/README.md`](https://github.com/vrontier/listen/blob/main/deploy/README.md) in the repository.
