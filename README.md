# Listening Observatory

**Listening Observatory** is an experimental system for continuous computational listening.

It connects to live audio streams, analyses their acoustic structure in real time, and translates what it hears into evolving visual forms and textual observations. Rather than treating sound as something to be displayed moment by moment, the system listens across different timescales — from immediate frequency changes to recurring resonances, motifs, and longer-term patterns.

Signal processing provides the observation layer: frequencies, harmonics, spectral energy, entropy, transients, and other measurable characteristics are extracted continuously. A memory layer then compares current activity with what has been heard before, allowing recurring structures to return with a persistent visual identity.

The live visualization is generated in the browser and evolves with the signal. Stable resonances may form persistent structures, transient events may disturb the visual field, and recurring acoustic motifs can reappear as familiar entities. Over time, the visualization becomes not only a representation of the current sound, but also a record of the system's history of listening.

A language model can add a slower narrative layer, turning structured observations into concise textual descriptions. It does not decide what happened in the sound; it gives language to events detected by the analytical system.

**Live at <https://listen.vrontier.org>.**

## Streams

The observatory listens to several sources at once. Each has its own page at `/<slug>`
with the visualization, the interpretation and (where the source allows it) the audio.
The site's landing page explains each source and how to read the visualization.

| Stream | Kind | What you hear |
|---|---|---|
| [VLF Natural Radio](https://listen.vrontier.org/vlf-heidelberg) | live | Very low frequency radio from a receiver near Heidelberg: lightning (sferics), whistlers, the magnetosphere |
| [Amsterdam · Hydrophile 1](https://listen.vrontier.org/amsterdam-hydrophile-1) and [5](https://listen.vrontier.org/amsterdam-hydrophile-5) | live | Hydrophones under Amsterdam's water, streamed by sound artist Lia Mazzari for radio.earth |
| [Svalbard · Bayelva](https://listen.vrontier.org/svalbard-bayelva) | live | Arctic permafrost observatory near Ny-Ålesund: wind, meltwater, weather (Common Grounds, radio.earth) |
| [Brno · Park Lužánky](https://listen.vrontier.org/brno-luzanky) | live | Bird choruses and city sound in Brno's oldest park (radio.earth) |
| [Kozmice · Bird Meadows](https://listen.vrontier.org/kozmice-meadows) | live | Floodplain meadows of the Opava River, solar-powered station (radio.earth) |
| [Mars · InSight](https://listen.vrontier.org/mars-insight) | archive loop | NASA InSight seismometer: Martian wind, marsquakes, the lander itself |
| [Jupiter · Juno](https://listen.vrontier.org/jupiter-juno) | archive loop | NASA Juno Waves: radio and plasma waves around Jupiter, Ganymede and Europa |

Credits and licences for each source are on its page. Audio is never stored in this repository.

## Status

Early development. See [`docs/`](docs/) for the concept and the
[live visualization event & data model](docs/sonic_division_live_visualization_event_model.md).

## Layout

```text
docs/       concept, event model, visual study
listener/   Go daemon: ffmpeg ingestion, DSP, event detection, memory, narrator, WebSocket API
site/       website (vanilla PHP front controller, served by NGINX + PHP-FPM):
            landing page, contact form, one live page per source (p5.js, site/assets/js/)
deploy/     nginx, PHP-FPM and systemd files for staging and production
scripts/    local development and deployment helpers
```

## Installation

### Requirements

- Go ≥ 1.23 (listener), ffmpeg (audio decoding), PHP 8 (site), jq (scripts)
- for a server: Linux with systemd, NGINX and PHP-FPM 8.3

### Configuration

Three files hold local settings and secrets; none of them is committed.

| File | Purpose |
|---|---|
| `site/_config/sources.json` | The streams. Copy `sources.example.json`. Each entry is one source: its page (name, place, time zone, summary, credit, whether audio may be played, whether it is listed on the landing page) and its listener (port, input URL or file, extra flags such as `-sample-rate 32000`). Unlisted sources are reachable by URL only and marked `noindex`. |
| `.email` | SMTP account for the contact form (`EMAIL_ADDRESS`, `EMAIL_NAME`, `EMAIL_USER`, `EMAIL_PASSWORD`, `SMTP_SERVER`, `SMTP_PORT`). `scripts/mail-config.sh` turns it into `site/_config/mail.php` (see `mail.example.php`); without it the form shows the address instead. |
| `.llm` | Optional interpretation layer: `LLM_API_KEY`, `NARRATOR_URL` (any OpenAI-compatible endpoint), `NARRATOR_MODEL`. Without it the interpretation stays off. |

### Run locally

```sh
scripts/dev.sh                     # every source in sources.json, plus the site
scripts/dev.sh vlf-heidelberg      # or only some
# → http://127.0.0.1:8000/

cd listener && go test ./...
bin/listener -in capture.mp3 -dump > events.jsonl   # offline, as fast as possible
```

### Deploy to a server

Each source runs as its own listener instance (`listen-listener@<slug>`, systemd,
bound to 127.0.0.1). NGINX routes `/<slug>/ws/…` and `/<slug>/api/…` to it, and
everything else to the PHP front controller. The listener is cross-compiled to a
static linux/amd64 binary, so the server needs no Go. `scripts/gen-sources.sh`
generates the per-source systemd settings and NGINX routes from `sources.json`.

- **With sudo** (staging): `scripts/deploy-taurus.sh` builds, uploads and installs
  everything in one go. Host setup and file locations: [`deploy/README.md`](deploy/README.md).
- **Without root** (production): `scripts/deploy-ionos.sh` builds and stages
  everything in the deploy account's home, and an admin runs the idempotent
  [`deploy/ionos/install.sh`](deploy/ionos/install.sh) there as root (what it changes:
  [`deploy/ionos/INFOS_listen_vrontier.md`](deploy/ionos/INFOS_listen_vrontier.md)).
  Site-only changes need no root: `scripts/deploy-ionos.sh --site-only`.

Both scripts are idempotent. A source removed from `sources.json` is stopped on
the next full deploy.

## How it works

The listener implements the MVP of the [event model](docs/sonic_division_live_visualization_event_model.md)
(§27): `signal.frame` and `signal.spectrum` at 10 Hz, `feature.state` at 2 Hz,
`event.transient`, `event.resonance` and `system.status`, over `GET /ws/live`, with
the reconnect snapshot at `GET /api/state/current`.

Acoustic memory (phase 2, §12–§16): with `-memory-dir` the listener recognises
recurring structures. *Resonance motifs* are recurring partials, matched on
frequency, harmonic profile and context. *Texture motifs* are recurring
overall sound states, compared over 20-second windows. Each motif gets a
persistent id and a visual seed, and the listener emits `motif.detected`,
`motif.returned` (after `-motif-return` of absence, 3 min by default) and
`memory.summary` (1 h and 24 h). Memory lives in plain files, one directory per
source and input: `motifs.json` and daily `history/*.jsonl`, kept for
`-history-days`. It is served at `/api/motifs`, `/api/motifs/{id}`,
`/api/history/events` and `/api/history/features`.

Interpretation (phase 3, §17): with `-narrator-url` and `-narrator-model` (or the
`NARRATOR_URL` / `NARRATOR_MODEL` environment variables) and an API key in
`LLM_API_KEY`, the listener phrases what it measured through any OpenAI-compatible
chat endpoint. It emits `narrative.update` at most every `-narrate-every` (90 s), and
only while someone has the source's page open and something has changed. Go builds the evidence
(leading resonances, returns, new motifs, transients, state changes, the last hour)
and the model only phrases it. Text naming a frequency or motif that isn't in the
evidence is retried once, then dropped. Modes: `observational` (default), `minimal`,
`poetic`. For local development the settings go in `.llm` (see Configuration);
`scripts/dev.sh` loads it.

Optional in-sync audio: with `-audio` the listener relays the audio it analyses
on `/ws/audio`, as fragmented MP4 (AAC and Opus). Every event carries
`position_s` on the same timeline, so the page shows each event when its sound
is heard. A source offers playback when `"audio": true` is set in its entry in
`sources.json`; it is off by default.

Dependencies are pinned and kept in the repository: `github.com/coder/websocket`
(pure Go, no transitive dependencies) is vendored in `listener/vendor/`, and p5.js is
vendored in `site/assets/vendor/p5/` with checksums in `VERSION`. Audio captures
in `samples/` are never committed.

## Principle

```text
The DSP system observes.
The memory layer recognizes.
The visualization embodies.
The LLM gives language to what was measured.
```

## License

Code: [MIT](LICENSE). Audio from third-party sources is not part of this repository and remains with its respective owners.
