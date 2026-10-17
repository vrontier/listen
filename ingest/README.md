# listen-ingest

**listen-ingest** is the one service on a machine that connects to audio stations. Every other program there asks it for a station over a local socket and gets the station's original bytes. A station is connected once, however many programs ask.

It decodes nothing and changes nothing: what a program receives is what the station sent. A program may join at any moment. For containers that cannot be decoded from the middle (Ogg, raw FLAC), the ingest keeps the stream's headers and sends them first, so a late joiner gets a stream any decoder accepts. MP3 and AAC in ADTS need no headers and are passed on as they come.

In this repository, the Observatory's listeners and `listen-engine` (which serves the Workbench) read every station through it. Any other program can do the same: the interface is plain HTTP on a Unix socket, described below.

## Running it

Download `listen-ingest` for your system from the [releases page](https://github.com/vrontier/listen/releases), or build it:

```sh
go build ./cmd/listen-ingest
./listen-ingest -socket /run/listen-ingest/feeds.sock
```

| Flag | Default | Meaning |
|---|---|---|
| `-socket` | `/run/listen-ingest/feeds.sock` | the Unix socket programs ask on |
| `-mode` | `0660` | the socket's mode (octal); give its group to the programs that may read |
| `-idle-after` | `10s` | how long a connection to a station stays open after its last reader left, so that a reader reconnecting finds it |
| `-allow-private` | off | accept stations on private networks (a closed intranet, tests) |
| `-allow-host` | none | a host or `host:port` accepted although on a private network; repeatable |

A systemd unit is in [`../deploy/systemd/listen-ingest.service`](../deploy/systemd/listen-ingest.service) and in the Linux archives. On Windows, give `-socket` a path of your own.

## What it accepts

Only `http` and `https` addresses of public hosts. Every connection, and every redirect, is checked again after the name is resolved: loopback, private, link-local and the machine's own addresses are refused unless allowed by flag. Playlists and web pages are refused; a station must send audio.

## The interface

HTTP/1.1 on the Unix socket; the host name in requests is ignored (`http://ingest/…`).

### `GET /v1/feed?url=<station>`

The station's bytes: the headers a late joiner needs, then the stream live, until the reader closes the connection or the station ends. The address is URL-encoded in `url`.

Response headers on success (`200`):

| Header | Value |
|---|---|
| `Ingest-Format` | ffmpeg's name for the input format (`ogg`, `flac`, `mp3`, `aac`), or empty when unknown |
| `Ingest-Content-Type` | the content type the station sent |

A reader that falls too far behind is disconnected rather than given a stream with holes; it reconnects and gets the headers again. When the station's connection ends, every reader's response ends; the next request makes a new connection.

Errors are JSON, `{"error": {"code": "…", "message": "…"}}`:

| Status | Code | Meaning |
|---|---|---|
| 403 | `bad_address` | not a valid address |
| 403 | `protocol_not_allowed` | not `http` or `https` |
| 403 | `address_not_allowed` | a private, loopback or own address |
| 502 | `playlist` | a playlist, not a stream |
| 502 | `not_audio` | the station sent something other than audio |
| 502 | `no_answer` | the station did not answer, answered with an error, or ended |

A program that cannot reach the socket at all should report that the ingest is down, not that the station failed (the Go client below returns the code `ingest_unavailable` for this).

### `GET /v1/feeds`

The connections held now, for the operator:

```json
{"feeds": [{"url": "https://stream.example.org/live.ogg", "consumers": 2, "bytes": 123456, "since": "2026-10-17T06:54:52Z", "content_type": "audio/ogg"}]}
```

## From Go

```go
import "github.com/vrontier/listen/ingest"

c := ingest.NewClient("/run/listen-ingest/feeds.sock")
body, format, contentType, err := c.Open(ctx, "https://stream.example.org/live.ogg", nil)
```

`format` can be passed to ffmpeg as `-f`. The package `github.com/vrontier/listen/ingest/fetch` holds the address check and the code that connects to a station, for programs that also connect themselves.

## License

MIT, see [LICENSE](LICENSE).
