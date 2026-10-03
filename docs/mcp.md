# MCP access: `listen-mcp`

`listen-mcp` is a read-only [Model Context Protocol](https://modelcontextprotocol.io) server for
Listening Observatory. Any MCP client (Claude Code, Claude Desktop, the MCP Inspector, …) can use
it to ask what the observatory is listening to, and to record analysis data from a source.
It never returns audio.

## How it works

```text
MCP client ──stdio over ssh──► listen-mcp ──HTTP / WebSocket on 127.0.0.1──► listeners (one per source)
                                   │
                                   └── reads site/_config/sources.json (the source catalogue)
```

- **Placement.** It runs on the host where the listeners run, as an ordinary user (no root, no
  service). It is started per session by the client, over SSH.
- **Transport.** stdio: newline-delimited JSON-RPC 2.0. Protocol versions 2025-06-18,
  2025-03-26 and 2024-11-05. Tools only; no resources or prompts.
- **Data.** It queries each listener on 127.0.0.1: the current snapshot, the memory (motifs), the
  interpretation history, and the live event stream for extracts.
- **Not a viewer.** Its live connections use `?role=tool`. They appear as `tools` in the status
  and are not counted as `listeners`, so they don't show on the landing page and don't make the
  narrator write. They do wake a source that runs on demand.

## Install on a host

```sh
cd listener
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod=vendor -trimpath -o listen-mcp ./cmd/listen-mcp
scp listen-mcp <host>:bin/listen-mcp
```

Flags:

| Flag | Default | Meaning |
|---|---|---|
| `-sources` | `/var/www/listen.vrontier.org/_config/sources.json` | the source catalogue (the staging site uses `/var/www/listen.home.arpa/_config/sources.json`) |
| `-host` | `127.0.0.1` | where the listeners listen |
| `-max-seconds` | `300` | longest `get_extract` window |

## Connect a client

The client starts `ssh <host> bin/listen-mcp` and talks to it over stdin/stdout, so the machine
running the client needs non-interactive SSH access to the host: a key in the agent or the SSH
config, and the host key already accepted. Test this first; it must print nothing and simply wait
(press Ctrl-D to end):

```sh
ssh -o BatchMode=yes <host> bin/listen-mcp
```

**Claude Code** (`.mcp.json` in a project, or `claude mcp add`):

```json
{
  "mcpServers": {
    "listen": {
      "type": "stdio",
      "command": "ssh",
      "args": ["<host>", "bin/listen-mcp"]
    }
  }
}
```

```sh
claude mcp add listen -- ssh <host> bin/listen-mcp
```

**Claude Desktop** (`claude_desktop_config.json`, under Settings → Developer). Desktop apps don't
always see your shell's SSH agent or config, so give the full path to `ssh`, and use a host
alias whose key is configured in `~/.ssh/config`:

```json
{
  "mcpServers": {
    "listen": {
      "command": "/usr/bin/ssh",
      "args": ["-o", "BatchMode=yes", "<host>", "bin/listen-mcp"]
    }
  }
}
```

For staging, add `"-sources", "/var/www/listen.home.arpa/_config/sources.json"` after
`bin/listen-mcp`.

**MCP Inspector** (to try the tools by hand in the browser):

```sh
npx @modelcontextprotocol/inspector ssh <host> bin/listen-mcp
```

**Without a client** (raw JSON-RPC, useful for scripts):

```sh
{ printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"service_status","arguments":{}}}'
  sleep 3; } | ssh <host> bin/listen-mcp
```

Each tool returns one text content item that holds a JSON document. Errors, such as an unknown
source, come back with `isError: true` and a readable message.

## Tools

### `service_status`

The overall state: per source the stream state (`connected`, `idle` = on demand without a viewer,
`connecting`, `reconnecting`, `unreachable`), uptime, reconnects, listeners (live pages open),
`with_sound`, connected `tools`, signal state, dominant frequency, active motifs and analysed
frequency range; plus totals per stream state. No arguments.

### `list_sources`

The catalogue: slug, name, place, `listed`, kind (`live` stream or `replay` of a recording),
time zone, whether audio may be played, credit and link, rights note, analysis settings
(`sample-rate`, `min-hz`, `max-hz`, `mains`, `transient-k`).

| Argument | Type | Default | |
|---|---|---|---|
| `include_state` | boolean | `true` | add each source's current state, as in `service_status` |

### `get_extract`

Records the **next** n seconds of analysis data from one source; the call returns after the
window. No audio.

| Argument | Type | Default | |
|---|---|---|---|
| `source` | string, required | | slug, e.g. `my-stream` (see `list_sources`) |
| `seconds` | integer 1–300 | 30 | window length |
| `layers` | array of `features`, `events`, `frames`, `spectrum`, `status` | `["features","events"]` | what to keep |
| `spectrum_every` | integer ≥ 1 | 10 | keep every n-th spectrum (10 per second; 10 = one per second) |

| Layer | Rate | Content |
|---|---|---|
| `features` | 2 Hz | signal state (`stable_resonance`, `broadband_noise`, …), dominant frequency, spectral centroid, bandwidth, roll-off, entropy, flux, harmonicity, energy, novelty |
| `frames` | 10 Hz | RMS, level (dBFS), energy, peak, zero-crossing rate, centroid, flux, entropy, harmonicity |
| `spectrum` | 10 Hz (decimated) | analysed range (`min_hz`, `max_hz`), 48 log-spaced band levels (0–1), up to 12 peaks with frequency and amplitude |
| `events` | as they happen | `event.transient`, `event.resonance` (start / update / end, fundamental, harmonics, motif), `motif.detected`, `motif.returned`, `narrative.update`, `memory.summary` |
| `status` | every 5 s | as in `service_status`, for this source |

The result also holds `message_counts` (everything that arrived, including layers not kept),
`stream_before` (the state when the call started) and `recorded_s`. Every entry has `t` (the
observation time at the source, UTC) and `position_s` (seconds of audio since the listener
started).

Note: a source that is `idle` (on demand) is woken by the call. Connecting takes 1–4 s of the
window. The stream server then sends its buffer at once, so the first second holds a burst, and
the signal state reads `unknown` for about 5 s while the level ranges settle. On a source that
listens continuously, none of this happens.

### `get_motifs`

The motifs a source remembers (recurring resonances and textures), sorted by occurrences:
frequency, occurrences, presence (s), confidence, signature (harmonicity, entropy, fundamental
or centroid), visual seed, first and last seen. The same data as the "Export CSV" button on
the live pages.

| Argument | Type | Default |
|---|---|---|
| `source` | string, required | |
| `limit` | integer ≥ 1 | 50 |
| `kind` | `resonance` or `texture` | both |

### `get_narratives`

The latest interpretation texts of a source, newest first. They are written by a language model
from the measurements, while someone watches the source's page, and checked against them.

| Argument | Type | Default |
|---|---|---|
| `source` | string, required | |
| `limit` | integer ≥ 1 | 10 |
| `hours` | integer 1–720 | 24 (how far back to look) |

## Example prompts

- "Which streams are live right now, and is anyone listening?"
- "Record 30 seconds of my-stream and tell me how many broadband transients occurred."
- "Compare the top 10 motifs of two sources by frequency."
- "Record 20 seconds of the spectrum from my-stream and describe the strongest steady tones."

## Security

- **Read-only:** no tool changes anything.
- **Local access only:** it reaches the listeners on 127.0.0.1 only and needs no open port.
- **Access control is SSH:** whoever can log in to the host as the account that holds the binary
  can use it.
- **Audio:** none ever passes through it.

A public HTTP transport (`/mcp` behind nginx, with tokens and rate limits) is a possible next step.
The transport is separate from the tools in the code (`listener/cmd/listen-mcp/mcp.go`).
