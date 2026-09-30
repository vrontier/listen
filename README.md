# Listening Observatory

**Listening Observatory** is an experimental system for continuous computational listening.

It connects to live audio streams, analyses their acoustic structure in real time, and translates what it hears into evolving visual forms and textual observations. Rather than treating sound as something to be displayed moment by moment, the system listens across different timescales — from immediate frequency changes to recurring resonances, motifs, and longer-term patterns.

Signal processing provides the observation layer: frequencies, harmonics, spectral energy, entropy, transients, and other measurable characteristics are extracted continuously. A memory layer then compares current activity with what has been heard before, allowing recurring structures to return with a persistent visual identity.

The live visualization is generated in the browser and evolves with the signal. Stable resonances may form persistent structures, transient events may disturb the visual field, and recurring acoustic motifs can reappear as familiar entities. Over time, the visualization becomes not only a representation of the current sound, but also a record of the system's history of listening.

A language model can add a slower narrative layer, turning structured observations into concise textual descriptions. It does not decide what happened in the sound; it gives language to events detected by the analytical system.

## Status

Early development. See [`docs/`](docs/) for the concept and the
[live visualization event & data model](docs/sonic_division_live_visualization_event_model.md).

## Layout

```text
docs/       concept, event model, visual study, audio sources
listener/   Go daemon: ffmpeg ingestion, DSP, event detection, WebSocket API
site/       website for listen.vrontier.org (vanilla PHP front controller, served by NGINX + PHP-FPM)
            /live is the p5.js visualization (site/assets/js/live/)
scripts/    local development helpers
```

## Development

Requires Go ≥ 1.23, ffmpeg and PHP 8.

```sh
scripts/dev.sh                     # loop the newest capture in samples/
scripts/dev.sh https://…/live      # or analyse a live stream
# → http://127.0.0.1:8000/live?ws=ws://127.0.0.1:8080/ws/live

cd listener && go test ./...
bin/listener -in capture.mp3 -dump > events.jsonl   # offline, as fast as possible
```

The listener implements the MVP of the [event model](docs/sonic_division_live_visualization_event_model.md)
(§27): `signal.frame` and `signal.spectrum` at 10 Hz, `feature.state` at 2 Hz,
`event.transient`, `event.resonance` and `system.status`, over `GET /ws/live`, with
the reconnect snapshot at `GET /api/state/current`.

Dependencies are pinned and kept in the repository: `github.com/coder/websocket`
(pure Go, no transitive dependencies) is vendored in `listener/vendor/`, and p5.js is
vendored in `site/assets/vendor/p5/` with checksums in `VERSION`. Audio captures
in `samples/` are never committed.

The source shown on `/live` is named in `site/_config/source.php` (not committed;
see `source.example.php`). Without it the page uses neutral defaults.

## Principle

```text
The DSP system observes.
The memory layer recognizes.
The visualization embodies.
The LLM gives language to what was measured.
```

## License

Code: [MIT](LICENSE). Audio from third-party sources is not part of this repository and remains with its respective owners.
