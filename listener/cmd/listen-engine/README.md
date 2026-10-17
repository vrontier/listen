# listen-engine

**listen-engine** decodes live audio streams and analyses them for Listening Observatory Workbench. It holds the streams, the decodes, the analyses and their memories, and is told what to run over a control API on a local Unix socket. It knows nothing about users, accounts or publication: that is the Workbench's part.

It reads every station through [`listen-ingest`](../../../ingest/), the one service on a machine that connects to stations, and decodes with ffmpeg. Several analyses on one stream share one decode; streams at different rates share one connection through the ingest.

## What it needs

- **ffmpeg** on the path (or `-ffmpeg /path/to/ffmpeg`).
- **listen-ingest** running, its socket given with `-ingest`. Without `-ingest` the engine connects to stations itself.

## Running it

```sh
listen-engine -ingest /run/listen-ingest/feeds.sock \
  -caller workbench=/run/listen-engine/control.sock:0660 \
  -memory-dir /var/lib/listen-engine/memory -state /var/lib/listen-engine/state.json
```

`listen-engine -h` lists every flag: limits per stream and analysis, the address check (`-allow-private`, `-allow-host`), and `-version`. A systemd unit is in the release archive for Linux (`systemd/listen-engine.service`).

## The control API

The contract is [`workbench/docs/control-api.md`](../../../workbench/docs/control-api.md) (HTTP/1.1 with JSON on the socket). The Workbench's `engine-check` command runs its requests against any engine and reports what passes.

## License

MIT, see [LICENSE](../../../LICENSE).
