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
docs/   concept, event model, visual study
site/   website for listen.vrontier.org (vanilla PHP front controller, served by NGINX + PHP-FPM)
```

Planned: `listener/` — Go daemon for stream ingestion, DSP, event detection and the WebSocket API.

## Principle

```text
The DSP system observes.
The memory layer recognizes.
The visualization embodies.
The LLM gives language to what was measured.
```

## License

Code: [MIT](LICENSE). Audio from third-party sources is not part of this repository and remains with its respective owners.
