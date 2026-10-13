# Control API between the Workbench and the engine process

Status: draft (2026-10-13). Nothing here is built. API version described: **1.0**.

Everything in this document is **proposed**. It becomes stable only after it has been read against the engine's code in the listen repository (see [design.md](design.md#open-questions), question 4). Points that are still **open** are collected at the end.

This is the contract for step 0 of the [order of work](design.md#order-of-work). The engine process is built to it in the listen repository; the Workbench is built to it here, at first against a stand-in.

## Purpose and limits

The control API is the only connection between the two processes of [design.md](design.md#processes).

- The Workbench tells the engine which streams and analyses should exist, and with which settings.
- The engine reports what state they are in, and serves each analysis's data.
- The engine checks every setting and rule. The Workbench keeps no copy of the engine's rules about what is valid.

The engine knows nothing of members, accounts, tokens, publication or texts. Nothing in this API carries a member's name, email address or token.

Not part of version 1: audio (relay and captures), the language-model interpretation, defined motifs and profiles, analyses across several streams. The places where they would attach are named under [Reserved](#reserved).

## Transport

- **HTTP/1.1 over a Unix domain socket**, with JSON bodies in UTF-8. The path of the socket is a setting of both programs, for example `/run/listen-engine/control.sock`.
- **Access is the socket's file permission.** The socket belongs to the engine's user and to a group that the Workbench's user is in, with mode 0660. There is no other authentication. The engine never offers this API on a network address. For development on one machine it may offer it on a loopback port, switched on by a flag.
- **Every path starts with `/v1`.** The 1 is the first number of the API version.
- **Times** are RFC 3339 in UTC. **Durations** are numbers of seconds, in fields ending in `_s`. **Frequencies** are in Hz, in fields ending in `_hz`.
- **Requests:** the engine refuses a body with a field it does not know (`400`, code `unknown_field`). A misspelt setting is then an error and not a setting silently left out.
- **Responses:** the Workbench ignores fields it does not know. That is what allows additions within version 1.

## Versions

| | |
|---|---|
| First number | Raised for a change that breaks an existing caller. Appears in the path. |
| Second number | Raised for an addition: a new field, resource or event type. |

`GET /v1/engine` reports both. The Workbench asks at start and after every reconnect, and acts as described in [design.md](design.md#staying-in-step). Because the engine refuses unknown request fields, a Workbench must not send a field introduced in 1.n to an engine that reports less than 1.n.

## Identifiers

- A stream and an analysis each have an identifier chosen by the Workbench: lower-case letters, digits and hyphens, 1 to 63 characters, starting with a letter or digit.
- The engine treats them as opaque. It uses them in directory names for the memory, which is why the character set is narrow.
- An identifier is never reused for a different stream or analysis. The Workbench should use the catalogue's own keys, not names that a member can change.
- An analysis's identifier is unique within its stream.

Examples in this document use `s-0001` for a stream and `a-0001` for an analysis.

## The model

```text
engine
└── stream s-0001          one input, one connection, one decode, one sample rate
    ├── analysis a-0001    own range, rules, memory, event numbering
    └── analysis a-0002
```

- **The Workbench holds the truth.** The engine keeps no configuration across a restart. It keeps the memories on disk, under the identifiers it was given.
- **Every change is a `PUT` of the whole object**, and sending the same object twice changes nothing. The Workbench can therefore repeat any request it is unsure about.
- **After a restart of either side** the Workbench lists what the engine has, puts what is missing or different, and deletes what should not be there. `GET /v1/engine` returns an `instance` value that changes with every start of the engine, so the Workbench can tell a restart from a short break in the connection.
- **A change takes effect within 2 seconds.** A `PUT` returns as soon as the engine has accepted it, with the object's current state, which for a new stream is usually `connecting` or `idle`.

## Resources

| Method and path | Purpose |
|---|---|
| `GET /v1/engine` | versions, instance, limits, capabilities |
| `GET /v1/events` | stream of state changes of all streams and analyses |
| `GET /v1/streams` | all streams with their state and their analyses |
| `PUT /v1/streams/{stream}` | create or change a stream |
| `GET /v1/streams/{stream}` | one stream |
| `DELETE /v1/streams/{stream}` | stop and remove a stream and its analyses |
| `PUT /v1/streams/{stream}/analyses/{analysis}` | create or change an analysis |
| `GET /v1/streams/{stream}/analyses/{analysis}` | one analysis |
| `DELETE /v1/streams/{stream}/analyses/{analysis}` | stop and remove an analysis |
| `POST /v1/validate/analysis` | check an analysis's settings without creating anything |
| `POST /v1/probes` | test a stream address |
| `GET /v1/streams/{stream}/analyses/{analysis}/events` | the analysis's live events |
| `GET /v1/streams/{stream}/analyses/{analysis}/state` | snapshot of the analysis |
| `GET /v1/streams/{stream}/analyses/{analysis}/motifs` | the motifs it remembers |
| `GET /v1/streams/{stream}/analyses/{analysis}/motifs/{motif}` | one motif with its recent occurrences |
| `GET /v1/streams/{stream}/analyses/{analysis}/history/events` | stored events |
| `GET /v1/streams/{stream}/analyses/{analysis}/history/features` | per-minute aggregates |
| `GET /v1/streams/{stream}/analyses/{analysis}/memories` | the memories kept on disk for this analysis |
| `DELETE /v1/streams/{stream}/analyses/{analysis}/memories/{key}` | delete one of them |

## The engine

`GET /v1/engine`

```json
{
  "api": { "major": 1, "minor": 0 },
  "engine_version": "0.9.0",
  "instance": "c1f0a7e2",
  "started": "2026-10-13T08:00:00Z",
  "limits": {
    "streams": 40,
    "analyses_per_stream": 4,
    "rules_per_analysis": 16,
    "subscriptions_per_analysis": 4,
    "max_sample_rate": 48000,
    "max_bitrate_kbps": 512
  },
  "capabilities": {
    "rule_kinds": ["line", "comb"],
    "detectors": [],
    "file_inputs": false,
    "address_check": "initial"
  },
  "counts": { "streams": 3, "connected": 1, "analyses": 4 }
}
```

- `limits` are set by the engine's operator when it is started. The engine enforces them. Limits per member are the Workbench's business and unknown to the engine.
- `capabilities` lets the Workbench build its forms from what this engine can do. `detectors` is empty until defined motifs exist.

## Streams

`PUT /v1/streams/s-0001`

```json
{
  "input": "https://stream.example.org/live.ogg",
  "sample_rate": 32000,
  "mode": "on_demand",
  "idle_after_s": 30,
  "retry_max_s": 300
}
```

| Field | Required | Meaning |
|---|---|---|
| `input` | yes | The stream's address. `http` and `https` only. A file path is accepted only if the engine was started with file inputs allowed, which is meant for development and tests. |
| `sample_rate` | yes | The rate of the decode, the same for every analysis of this stream. Normally the rate the probe reported. |
| `mode` | no, default `on_demand` | `continuous`: connected for as long as the stream exists. `on_demand`: connected only while an analysis of it has a subscriber. |
| `idle_after_s` | no, default 30 | `on_demand` only: how long after the last subscriber left the connection is closed. |
| `retry_max_s` | no, default 300 | The longest wait between attempts to reach a stream that keeps failing. The wait doubles from 1 second. |

- `input` and `sample_rate` cannot be changed on an existing stream (`409`, code `immutable`). A different address or rate is a different stream with a new identifier, because memories made from one do not compare with the other.
- `mode`, `idle_after_s` and `retry_max_s` can be changed at any time without interrupting the connection.
- A stream without analyses holds no connection, whatever its mode.
- The engine connects to an address once, however many analyses and subscribers there are. It refuses a second stream with the same `input` (`409`, code `duplicate_input`, with the existing stream's identifier). Addresses are compared as written; recognising two spellings of one stream is the Workbench's task.

Response to `PUT` and `GET`:

```json
{
  "id": "s-0001",
  "input": "https://stream.example.org/live.ogg",
  "sample_rate": 32000,
  "mode": "on_demand",
  "idle_after_s": 30,
  "retry_max_s": 300,
  "state": "connected",
  "since": "2026-10-13T09:12:40Z",
  "last_data": "2026-10-13T09:30:02Z",
  "last_error": null,
  "reconnects": 0,
  "delay_ms": 180,
  "analyses": ["a-0001", "a-0002"]
}
```

| `state` | Meaning |
|---|---|
| `idle` | not connected because nothing asks for it: `on_demand` without a subscriber, or no analyses |
| `connecting` | first attempt after being idle |
| `connected` | audio is arriving |
| `buffering` | connected, but no audio for more than 3 seconds |
| `reconnecting` | the connection was lost; waiting for the next attempt |
| `refused` | the engine will not connect to this address; `last_error` says why |

- `since` is when the stream entered its present state. A stream that has been `reconnecting` for 24 hours is paused by the Workbench, not by the engine: the engine has no notion of pausing and would not know whom to tell.
- **Pausing** a stream is deleting it from the engine. Its memories stay on disk and are found again when the same identifiers return.
- `last_error` is `null` or an object with `code`, `message` and `at`.

`DELETE /v1/streams/s-0001` closes the connection and removes the stream and its analyses. The memories stay unless `?purge=true` is given.

### Which addresses the engine connects to

The engine, not the Workbench, enforces what may be connected to, because only the engine sees redirects and reconnects.

- Only `http` and `https`.
- Not a loopback, private or link-local address, and not one of the server's own. The check is made on the address actually connected to, at the first connection, at every redirect and at every reconnect.
- A refusal sets the state `refused` with `last_error.code` `address_not_allowed` or `protocol_not_allowed`.

**This needs an engine change.** Today ffmpeg opens the connection and follows redirects by itself. Until the engine opens the HTTP connection itself, it can only check the address as given, and `GET /v1/engine` reports `"address_check": "initial"` instead of `"every_connection"` under `capabilities`.

## Analyses

`PUT /v1/streams/s-0001/analyses/a-0001`

```json
{
  "settings": {
    "min_hz": 800,
    "max_hz": 12000,
    "transient_k": 10,
    "transient_gap_s": 5
  },
  "rules": [
    { "kind": "comb", "hz": 50 },
    { "kind": "comb", "hz": 16.7, "odd": true, "tol_hz": 4, "drift_percent": 0.4 },
    { "kind": "line", "hz": 1000, "tol_hz": 5 }
  ],
  "memory": {
    "enabled": true,
    "motif_return_s": 180,
    "history_days": 30
  },
  "label": "rev-7"
}
```

Every part is optional. An empty object is an analysis of the full range with no rules and the default memory.

| Field | Default | Meaning and limits |
|---|---|---|
| `settings.min_hz` | 20 | Bottom of the analysed range. Must be below half of the top. |
| `settings.max_hz` | just below half the sample rate | Top of the analysed range. |
| `settings.transient_k` | 6 | How far above the usual change in the spectrum a transient must stand. Higher is less sensitive. |
| `settings.transient_gap_s` | 0.3 | Shortest time between two transients. |
| `rules` | none | Filter rules, see below. At most `limits.rules_per_analysis`. |
| `memory.enabled` | `true` | Whether motifs are learned and history is kept. |
| `memory.motif_return_s` | 180 | Absence after which a known motif counts as returned. |
| `memory.history_days` | 30 | Days of history kept. |
| `label` | none | Up to 63 characters, opaque to the engine, returned with the analysis and in its status events. Meant for the Workbench's revision of the settings, so that a result can be traced to it. |

- The sample rate is not here. It belongs to the stream.
- The engine refuses values outside their limits. Today's listener silently ignores a range it cannot use; the engine process must not.
- The listener's older mains option has no field of its own. It is the rule `{ "kind": "comb", "hz": 50 }`.

### Rules

| Field | Kinds | Meaning |
|---|---|---|
| `kind` | | `line`: one frequency. `comb`: a frequency and its harmonics. |
| `hz` | both | The line, or the base of the comb. Above 0. |
| `odd` | comb | Odd harmonics only. |
| `tol_hz` | both | How close a peak may sit to count as a match. Above 0. Default: 1.5 analysis bins. |
| `drift_percent` | comb | The base may wander by this much and is fitted to the peaks of every frame. Above 0, at most 2. |

This is the listener's `-ignore` syntax as an object. The engine returns each rule also in that syntax, as `text` (for example `comb=16.7,odd,tol=4,drift=0.4`), so that a page can show a rule "as configured" without the Workbench composing it.

### Response

```json
{
  "id": "a-0001",
  "stream": "s-0001",
  "settings": { "min_hz": 800, "max_hz": 12000, "transient_k": 10, "transient_gap_s": 5 },
  "rules": [
    { "kind": "comb", "hz": 50, "text": "comb=50" }
  ],
  "memory": { "enabled": true, "motif_return_s": 180, "history_days": 30, "key": "r800-12000-i3fa91c07" },
  "label": "rev-7",
  "effective": {
    "sample_rate": 32000,
    "bin_hz": 7.8,
    "min_hz": 800,
    "max_hz": 12000,
    "peak_min_hz": 800,
    "peak_max_hz": 12000,
    "default_tol_hz": 11.7
  },
  "state": "running",
  "subscribers": 1,
  "motifs": 12
}
```

- `effective` is what the analysis really works with after defaults. It matters to a researcher: without a range, peaks are searched only in the lower part of the spectrum (from 40 Hz to about 27 % of the sample rate), and the width of a bin sets how close two tones can lie and still be told apart.
- `state` is `running` while the stream is connected and `idle` otherwise.

### What a change does

| Changed | Effect |
|---|---|
| `transient_k`, `transient_gap_s`, `memory.*`, `label` | Applied to the running analysis. The memory continues. |
| `min_hz`, `max_hz`, `rules` | The analysis starts again and uses a different memory. |

The memory of an analysis is kept under a key made of the range and the rules, inside the directory of that analysis. Motifs learned with other rules are not comparable, so a change of range or rules switches to another memory: an empty one, or the earlier one if exactly these values were used before. The response's `memory.key` names the memory in use.

- `GET …/memories` lists the memories kept for the analysis: key, range and rules, first and last use, number of motifs, size on disk.
- `DELETE …/memories/{key}` deletes one. The memory in use cannot be deleted (`409`, code `in_use`).
- `DELETE` of an analysis keeps its memories unless `?purge=true` is given.

The stream's identifier and the analysis's identifier are both part of the memory's location. This is the change asked for in [analyses-and-filters.md](analyses-and-filters.md): today two analyses of one stream with equal settings would share one memory.

## Validation

`POST /v1/validate/analysis`

The body is the body of an analysis, plus the `sample_rate` it is meant for:

```json
{
  "sample_rate": 32000,
  "settings": { "min_hz": 9000, "max_hz": 12000 },
  "rules": [ { "kind": "line", "hz": 1000, "odd": true } ]
}
```

```json
{
  "valid": false,
  "errors": [
    { "path": "settings.min_hz", "code": "out_of_range", "message": "must be below half of max_hz (6000)" },
    { "path": "rules[0].odd", "code": "not_applicable", "message": "only a comb can be limited to odd harmonics" }
  ],
  "warnings": []
}
```

- With `"valid": true` the response carries `effective` and the rules with their `text`, as in an analysis's response.
- `warnings` are for settings that are allowed but probably not meant, for example a rule whose frequency lies outside the analysed range.
- `path` points into the request, so that a form can mark the field. `message` is English and written for the person filling in the form.
- A `PUT` of an analysis makes the same checks and answers `422` with the same `errors`.

## Probe

`POST /v1/probes`

```json
{ "input": "https://stream.example.org/live.ogg", "listen_s": 10 }
```

The engine connects, listens for `listen_s` seconds (default 10, at most 30) and disconnects. The call returns when it is done.

```json
{
  "ok": true,
  "content_type": "application/ogg",
  "codec": "vorbis",
  "sample_rate": 32000,
  "channels": 1,
  "bitrate_kbps": 96,
  "level_db": -31.5,
  "silent": false,
  "problems": []
}
```

- `ok` is false if the address is not allowed, nothing answers, the content is not audio, it cannot be decoded, the bit rate is above `limits.max_bitrate_kbps`, or only silence arrived. `problems` then lists `code` and `message` for each reason.
- A probe is a connection to the operator's server. If a stream with this `input` already exists and is connected, the engine answers from that decode and opens no second connection.
- The engine runs one probe at a time per address and a few at a time overall; more are answered with `429`.

## Data of an analysis

These paths serve what a listener serves today, one set per analysis. The payloads are the engine's and are defined in the listen repository (its event model document and `docs/mcp.md`). This document does not repeat them, and the Workbench passes them on without interpreting more than it must.

### Live events

`GET …/events` answers with `text/event-stream` and stays open.

```text
id: 48211
event: signal.frame
data: {"type":"signal.frame","version":1,"timestamp":"2026-10-13T09:30:02.150Z","sequence":48211,"source":"s-0001/a-0001","position_s":1042.35,"payload":{…}}

```

- `data` is the event's envelope exactly as the listener produces it. `id` is its sequence number and `event` its type.
- `source` in the envelope is the stream's and the analysis's identifier, joined by a slash.
- Sequence numbers count per analysis and start again when the analysis starts again. A gap means events were missed. Nothing is replayed, and `Last-Event-ID` is ignored.
- `?types=` limits the stream to the listed event types, separated by commas.
- A comment line is sent every 20 seconds so that both sides notice a dead connection.
- **A subscriber that reads too slowly loses events**, as today. The engine does not slow an analysis down for a reader.
- **A subscription is what "on demand" means.** A stream in mode `on_demand` connects when the first subscription to any of its analyses opens, and closes `idle_after_s` after the last one ended.
- **The Workbench subscribes once per analysis** and hands the events on to however many pages and token holders are reading. It ends the subscription when the last of them has gone. The engine allows only `limits.subscriptions_per_analysis` at a time.

Event types today: `signal.frame`, `signal.spectrum`, `feature.state`, `event.transient`, `event.resonance`, `motif.detected`, `motif.returned`, `memory.summary`, `system.status`.

`system.status` is sent every 5 seconds and on every change of the stream's state. Under the engine process it describes one analysis: the stream's state, the delay, the sample rate, the analysis's `label` and `memory.key`. The counts of listeners that today's status carries are dropped, because the engine no longer sees the readers.

### Snapshot, motifs, history

| Path | Content | As today's |
|---|---|---|
| `…/state` | the latest frame, spectrum, features and status, the active resonances and motifs, recent events, the memory's summaries | `/api/state/current` |
| `…/motifs` | all motifs of the memory in use | `/api/motifs` |
| `…/motifs/{motif}` | one motif with its recent occurrences | `/api/motifs/{id}` |
| `…/history/events` | stored events; `?from=&to=` (default: the last hour), `?type=`, `?limit=` (at most 2000) | `/api/history/events` |
| `…/history/features` | per-minute aggregates; `?window=1h` or `24h` | `/api/history/features` |

A page that opens fetches `…/state` and then follows `…/events`, as the Observatory's live page does.

## State changes of the engine

`GET /v1/events` is an event stream of changes, so that the Workbench does not have to ask every stream in turn.

| Event | Sent when | Data |
|---|---|---|
| `stream.state` | a stream changes state | the stream as in `GET /v1/streams/{stream}`, without its `input` |
| `analysis.state` | an analysis starts, stops or switches memory | the analysis's identifiers, `state`, `memory.key` |
| `engine.hello` | first event of every connection | `instance` and `api`, as in `GET /v1/engine` |

After connecting, the Workbench reads `engine.hello`, lists the streams once and from then on follows the events. If the `instance` has changed, it sends its whole wanted state again.

## Errors

Every error has this body:

```json
{ "error": { "code": "limit_reached", "message": "this stream already has 4 analyses", "errors": [] } }
```

| Status | Codes | Meaning |
|---|---|---|
| 400 | `bad_request`, `unknown_field`, `bad_id` | the request cannot be read |
| 404 | `not_found` | no such stream, analysis, motif or memory |
| 409 | `immutable`, `duplicate_input`, `in_use`, `limit_reached` | the request conflicts with what exists |
| 422 | `invalid` | the settings were refused; `errors` lists why, as in validation |
| 429 | `busy` | too many probes at once |
| 503 | `starting`, `stopping` | the engine is not ready |

`code` is for programs and stays the same within version 1. `message` is for people and may change.

## An example from start to first data

1. The Workbench starts and asks `GET /v1/engine`. The API is 1.0, as it needs.
2. It opens `GET /v1/events` and reads `engine.hello`.
3. A member enters a stream address. `POST /v1/probes` reports Ogg Vorbis at 32 000 Hz, not silent.
4. The member saves. `PUT /v1/streams/s-0001` with that address, the rate 32000 and mode `on_demand`. The state is `idle`.
5. The member leaves the analysis form empty. `POST /v1/validate/analysis` answers valid, and `PUT /v1/streams/s-0001/analyses/a-0001` with `{}` creates the analysis.
6. The member opens the analysis page. The Workbench fetches `…/state` (empty so far) and subscribes to `…/events`. The engine connects; `stream.state` reports `connecting`, then `connected`.
7. The member adds the rule "comb at 50 Hz". Validation passes, and the `PUT` returns a new `memory.key`. The event stream goes on with sequence numbers starting again.
8. The member closes the page. The Workbench ends its subscription, and 30 seconds later `stream.state` reports `idle`.

## Reserved

Not in version 1. Named here so that version 1 does not block them.

| Later feature | Where it attaches |
|---|---|
| Defined motifs and profiles | a `definitions` list in an analysis; `capabilities.detectors` with each detector's parameters and ranges; a new event type |
| Trial runs on a capture | a resource for captures under a stream, and a way to run an analysis over one without touching its live memory |
| Audio in the browser | a field `audio` on a stream and a data path for the encoded audio; the stream's `position_s` already allows lining events up with it |
| Interpretation by a language model | a setting of an analysis; the engine would need to be told whether a person is watching |
| Station time | additive fields in the envelope and in `system.status`, as drafted in the listen repository |

## For the engine session

What this spec asks of the engine, against what exists in the listener today.

| Asked for | Today |
|---|---|
| One process holding many streams, changed while it runs | one process per stream, set by command-line flags |
| Several analyses fed by one decode | one analysis per process; the analysis object itself can already be fed from outside |
| Sample rate per stream | per process (`-sample-rate`) |
| Event numbering, memory, fan-out, snapshot and status per analysis | one of each per process |
| Memory located by stream, analysis and key | by source name, input and key |
| Listing and deleting memories | not present |
| Refusing unusable settings | a range that cannot be used is silently ignored |
| Rules as objects, with their text form returned | parser and text form exist (`-ignore`) |
| `effective` values | computed inside the analysis, not reported |
| Events as `text/event-stream` with a `types` filter | WebSocket, no filter |
| On demand by subscription | on demand by WebSocket connection count |
| Probe | not present; the length of a file is read with ffprobe |
| Address check at every connection | not possible while ffmpeg opens the connection |
| Control API on a Unix socket, `GET /v1/events` | not present |

The paths the Observatory's site uses today (`/<slug>/ws/live` and the others) are not part of this API. Whether the engine process also serves them is a question for the day the Observatory moves onto it.

## Open

1. **Should the engine remember its configuration across a restart?** Proposed here: no, the Workbench sends it again. The price: if the engine restarts while the Workbench is down, nothing is listened to until the Workbench is back, which leaves a gap in a continuous stream's memory.
2. **Two callers on one engine.** If the Observatory's streams ever run in the same engine process, something other than the Workbench configures them. The Workbench's "delete what should not be there" after a restart would then remove them. Version 1 assumes one caller. A second caller would need a namespace per caller.
3. **Is a trial without a memory change needed in version 1?** A member tuning rules creates a new memory with every save. Setting `memory.enabled` to false while tuning is possible with this spec; whether that is enough is question 16 in [design.md](design.md#open-questions).
4. **Does one more analysis cost a whole spectrum?** Today each analysis computes its own. Sharing it between the analyses of a stream would make an analysis much cheaper and would not change this API. The limit `analyses_per_stream` depends on the answer and should be set from a measurement.
5. **Limits on the size of a memory** are not in this spec. The Workbench can read sizes from `…/memories` and delete; whether the engine should also enforce a ceiling is undecided.
6. **Server-Sent Events between the two processes** are proposed because they need no library on either side. The engine speaks WebSocket today. If keeping WebSocket there is much less work, only the transport of `…/events` changes.
7. **The identifiers in `source`.** Events reach browsers and token holders with the catalogue's internal identifiers in them. If that is unwanted, the Workbench has to rewrite every event, which costs.
