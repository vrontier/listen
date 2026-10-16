# Control API between the Workbench and the engine process

Status: agreed (2026-10-16). Draft of 2026-10-13, with additions for the maintainer's answers of 2026-10-14, read against the engine's code by the engine's owner in the listen repository on 2026-10-16, whose answers and the maintainer's decisions on them are worked in. The engine is not built yet; the Workbench is built to this against a stand-in. API version described: **1.0**, groups as **1.1**.

The reading against the code that [design.md](design.md#questions-and-answers) (question 4, decided) asked for has been done: the table [For the engine session](#for-the-engine-session) was found correct. **Decided** (2026-10-16, the maintainer): the engine owner's four changes to this contract, marked where they are. **Decided (engine)** marks choices inside the engine that its owner made. The rest of the detail stays **proposed** until the engine answers to it; the [conformance test](#conformance-test) checks that it does. The answers to the earlier open points are at the end.

This is the contract for step 0 of the [order of work](design.md#order-of-work). The engine process is built to it in the listen repository; the Workbench is built to it here, at first against a stand-in.

## Purpose and limits

The control API is the only connection between the two processes of [design.md](design.md#processes).

- The Workbench tells the engine which streams and analyses should exist, and with which settings.
- The engine reports what state they are in, and serves each analysis's data.
- The engine checks every setting and rule. The Workbench keeps no copy of the engine's rules about what is valid.

The engine knows nothing of members, accounts, tokens, publication or texts. Nothing in this API carries a member's name, email address or token. The one exception, once the interpretation is added, is a member's language-model API key, which travels with an analysis's settings (see [Reserved](#reserved)).

Not part of version 1.0: audio (relay and captures), the language-model interpretation, defined motifs and profiles. The places where they would attach are named under [Reserved](#reserved). Analyses across several streams, with station time, are drafted as the addition 1.1 under [Groups](#groups).

## Transport

- **HTTP/1.1 over a Unix domain socket**, with JSON bodies in UTF-8. The path of the socket is a setting of both programs. **Decided (engine)** (2026-10-16): the engine is the command `listen-engine` in the listen repository, its socket `/run/listen-engine/control.sock`, its user and group `listen-engine`, its memories under `/var/lib/listen-engine/memory/<stream>/<analysis>/<key>/`, apart from the Observatory's user and directories.
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

- **Terms.** An "analysis" here is the engine's analysis of one stream. A Workbench analysis with n sources is put into the engine as n such analyses, one per source with its settings, all with the same identifier, and from two sources on as one group over them ([design.md](design.md#analyses-and-sources)).
- **The Workbench holds the truth.** **Decided** (2026-10-16): the engine resumes the configuration it was last given (streams, analyses, groups; never a language-model key), saved after every accepted change, so a continuous run goes on after an engine restart even while the Workbench is down. The Workbench still lists, puts and deletes after every new `instance`, as below. The engine keeps the memories on disk, under the identifiers it was given.
- **Every change is a `PUT` of the whole object**, and sending the same object twice changes nothing. The Workbench can therefore repeat any request it is unsure about.
- **After a restart of either side** the Workbench lists what the engine has, puts what is missing or different, and deletes what should not be there. `GET /v1/engine` returns an `instance` value that changes with every start of the engine, so the Workbench can tell a restart from a short break in the connection.
- **A change takes effect within 2 seconds.** A `PUT` returns as soon as the engine has accepted it, with the object's current state, which for a new stream is usually `connecting` or `idle`.

## Resources

| Method and path | Purpose |
|---|---|
| `GET /v1/engine` | versions, instance, limits, capabilities, the host's capacity and use |
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
| `GET /v1/groups`, `PUT`, `GET`, `DELETE /v1/groups/{group}` | groups of analyses (1.1, see [Groups](#groups)) |
| `GET /v1/groups/{group}/events`, `…/state`, `…/history/events` | a group's results (1.1) |

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
  "counts": { "streams": 3, "connected": 1, "analyses": 4 },
  "host": { "cores": 4, "memory_mb": 8192 },
  "usage": { "cpu_percent": 31.5, "memory_mb": 412, "in_kbps": 288 }
}
```

- `limits` are set by the engine's operator when it is started. The engine enforces them. Limits per member are the Workbench's business and unknown to the engine.
- `capabilities` lets the Workbench build its forms from what this engine can do. `detectors` is empty until defined motifs exist.
- `host` and `usage` are for the Workbench's caps, see [Usage](#usage).

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
| `sample_rate` | yes | The rate of the decode, the same for every analysis of this stream. See [Choosing the sample rate](#choosing-the-sample-rate). |
| `mode` | no, default `on_demand` | `continuous`: connected for as long as the stream exists. `on_demand`: connected only while an analysis of it has a subscriber. |
| `idle_after_s` | no, default 30 | `on_demand` only: how long after the last subscriber left the connection is closed. |
| `retry_max_s` | no, default 300 | The longest wait between attempts to reach a stream that keeps failing. The wait doubles from 1 second. |
| `view` | no | The stream's spectrogram view, **decided** (2026-10-16): `{"scale": "linear" \| "log", "min_hz", "max_hz", "bands"}`. Bands of the stream's own, unfiltered and independent of any analysis's range, each band taking the strongest bin in it, computed once per stream. Defaults: `min_hz` 0 (linear) or 20 (log), `max_hz` half the sample rate, `bands` 240. Refused fields are reported with paths `view.scale`, `view.min_hz`, `view.max_hz`, `view.bands`. |

- `input` and `sample_rate` cannot be changed on an existing stream (`409`, code `immutable`). A different address or rate is a different stream with a new identifier, because memories made from one do not compare with the other.
- `mode`, `idle_after_s`, `retry_max_s` and `view` can be changed at any time without interrupting the connection. With `view` set, every `signal.spectrum` of every analysis of the stream carries `payload.view` (`min_hz`, `max_hz`, `scale`, `bands`) next to the analysis's own bands, which do not change; the stream's response reports `view` with its effective values.
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
  "analyses": ["a-0001", "a-0002"],
  "usage": { "cpu_percent": 3.1, "memory_mb": 24, "in_kbps": 96, "in_measured": false }
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

- `since` is when the stream entered its present state. A stream whose `last_data` is more than 24 hours old is paused by the Workbench, not by the engine: the engine has no notion of pausing and would not know whom to tell. The rule uses `last_data`, not the state, because a stream that is down moves between `buffering` and `reconnecting` (see below).
- ffmpeg retries a lost connection by itself for a while (up to 10 seconds between attempts) before it gives up. During those retries the state is `buffering`, not `reconnecting`, and `reconnects` counts only the times ffmpeg was started again.
- **Pausing** a stream is deleting it from the engine. Its memories stay on disk and are found again when the same identifiers return.
- `last_error` is `null` or an object with `code`, `message` and `at`.

`GET /v1/streams` answers `{ "streams": [ … ] }`, each as above.

`DELETE /v1/streams/s-0001` closes the connection and removes the stream and its analyses. The memories stay unless `?purge=true` is given.

### Choosing the sample rate

**Proposed** (2026-10-14, from the engine's code). The rate cannot be changed later, so the form has to explain the choice.

- The analysis window is always 4096 points. A higher rate therefore makes each analysis bin wider: about 5.4 Hz at 22 050 Hz, 7.8 Hz at 32 000 Hz, 10.8 Hz at 44 100 Hz. Two tones closer than about two bins cannot be told apart.
- A higher rate also costs more: the decode, and every analysis, process proportionally more samples.
- The default is therefore 22 050 Hz, the listener's default today, which covers content up to about 11 kHz. 32 000, 44 100 and 48 000 Hz are offered for streams whose interesting content lies higher (VLF radio, for example), never above the stream's own rate as the probe found it.
- **Exception:** a stream that carries station time (the probe's `station_time`) must be decoded at its own rate, so that the decoder's sample count matches the count in its timestamps. Its rate is the probe's `sample_rate`, without a choice. See [groups.md](groups.md#time).

### Which addresses the engine connects to

The engine, not the Workbench, enforces what may be connected to, because only the engine sees redirects and reconnects.

- Only `http` and `https`.
- Not a loopback, private or link-local address, and not one of the server's own. The check is made on the address actually connected to, at the first connection, at every redirect and at every reconnect.
- **Decided (engine)** (2026-10-16), from the first version: ffmpeg runs with its protocols limited to what HTTP and HTTPS need, so neither an address nor a redirect reaches a file or another protocol; before every start of ffmpeg (the first connection and every reconnect the engine makes) the host is resolved and refused if any of its addresses is loopback, private, link-local, multicast, unspecified or the server's own; the probe makes the same check. That is `"address_check": "initial"`.
- A refusal sets the state `refused` with `last_error.code` `address_not_allowed` or `protocol_not_allowed`.

**The rest needs an engine change** (the engine's phase 4). ffmpeg follows redirects and reconnects inside a connection by itself. Until the engine opens the HTTP connection itself, those are not checked, and `GET /v1/engine` reports `"address_check": "initial"` instead of `"every_connection"` under `capabilities`.

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
    "mode": "disk",
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
| `memory.mode` | `disk` | `disk`: motifs are learned and kept with the history on disk. `ram`: motifs are learned but nothing is written, and all is gone when the analysis ends; meant for trials. `off`: no motifs, no history. All three exist in the listener today (a memory directory, a memory without one, no memory). |
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
  "memory": { "mode": "disk", "motif_return_s": 180, "history_days": 30, "key": "r800-12000-i3fa91c07" },
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
  "motifs": 12,
  "usage": { "cpu_percent": 1.8, "memory_mb": 6, "memory_estimated": true }
}
```

- `effective` is what the analysis really works with after defaults. It matters to a researcher: without a range, peaks are searched only in the lower part of the spectrum (from 40 Hz to about 27 % of the sample rate), and the width of a bin sets how close two tones can lie and still be told apart.
- `state` is `running` while the stream is connected and `idle` otherwise, or `failed`. **Decided** (2026-10-16): each analysis runs on its own and catches its own crash; it then stops with the state `failed` and a `last_error` (`code`, `message`, `at`) as for a stream, while its stream and the other analyses go on. A `PUT` of the same analysis starts it again. `last_error` is `null` otherwise.

### What a change does

| Changed | Effect |
|---|---|
| `transient_k`, `transient_gap_s`, `memory.motif_return_s`, `memory.history_days`, `label` | Applied to the running analysis. The memory continues. |
| `memory.mode` | The memory is opened or closed; the analysis itself continues. |
| `min_hz`, `max_hz`, `rules` | The analysis starts again and uses a different memory. |

Applying a setting to a running analysis needs setters in the engine. Making a new analysis object instead would not do: its resonance numbering would start again at `res-00001`, and the memory, which follows open resonances by that number, would mix old and new ones.

The memory of an analysis is kept under a key made of the range and the rules, inside the directory of that analysis. Motifs learned with other rules are not comparable, so a change of range or rules switches to another memory: an empty one, or the earlier one if exactly these values were used before. The response's `memory.key` names the memory in use.

- `GET …/memories` lists the memories kept for the analysis, as `{ "memories": [ … ] }`, each with `key`, `settings`, `rules`, `first_used`, `last_used`, `motifs` (the number), `bytes` (size on disk) and `in_use`.
- The stored history belongs to a memory too: in the listener the history files lie in the memory's directory. After a change of range or rules, `…/history/…` and `…/motifs` show the new memory only. They take `?memory=<key>` to read an earlier one, which the export across revisions needs.
- `DELETE …/memories/{key}` deletes one. The memory in use cannot be deleted (`409`, code `in_use`).
- `DELETE` of an analysis keeps its memories unless `?purge=true` is given.

The stream's identifier and the analysis's identifier are both part of the memory's location. This is the change asked for in [analyses-and-filters.md](analyses-and-filters.md): today two analyses of one stream with equal settings would share one memory.

## Usage

Added for the maintainer's answers of 2026-10-14 (decided: every analysis's cost is measured and accounted against CPU and RAM; caps for the server and per member). The fields are **proposed**.

The engine measures; the Workbench records, adds up, counts each member's share against their cap and enforces the caps. The engine knows nothing of members, so it enforces only its own `limits`.

| Where | Field | Meaning |
|---|---|---|
| engine | `host.cores`, `host.memory_mb` | what the machine has, so that a cap can be set as a share of it |
| engine | `usage.cpu_percent`, `usage.memory_mb` | the engine process and all its ffmpeg children together; 100 is one core |
| engine, stream | `usage.in_kbps` | incoming bandwidth |
| stream | `usage.cpu_percent`, `usage.memory_mb` | the stream's ffmpeg process: connection and decode |
| stream | `usage.in_measured` | `false` while `in_kbps` is the bit rate the probe found, not a measurement |
| analysis | `usage.cpu_percent` | time spent in this analysis's work |
| analysis | `usage.memory_mb`, `usage.memory_estimated` | memory held by the analysis; `true` while this is an estimate |

- Values are averages over the last 10 seconds, and are `0` for an idle stream or analysis.
- `GET /v1/events` sends a `usage` event every 10 seconds with these values for everything that runs, so that the Workbench does not have to ask each object.
- **Exactness** (assumed from the engine's code, to be confirmed by the engine session): an ffmpeg process can be measured exactly. The analyses all run inside the engine process; their CPU time can be measured by timing their work, their memory only estimated, since Go does not report memory per part of a program. Incoming bandwidth is exact only once the engine opens the HTTP connection itself (see [Which addresses the engine connects to](#which-addresses-the-engine-connects-to)).
- Memory on disk is reported per memory under `…/memories` and is what the Workbench counts against a member's disk quota.

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
  "exists": null,
  "content_type": null,
  "codec": "vorbis",
  "sample_rate": 32000,
  "channels": 1,
  "bitrate_kbps": 96,
  "level_db": -31.5,
  "silent": false,
  "station_time": true,
  "problems": []
}
```

- `ok` is false if the address is not allowed, nothing answers, the content is not audio, it cannot be decoded, the bit rate is above `limits.max_bitrate_kbps`, or only silence arrived. `problems` then lists `code` and `message` for each reason.
- A probe is a connection to the operator's server. If a stream with this `input` already exists, the engine opens no connection and answers `"ok": false` with `exists` set to that stream's identifier; the Workbench then offers to join the shared stream. A running decode could not answer anyway: it delivers samples at the stream's chosen rate and knows nothing of the codec or the stream's own rate.
- `codec`, `sample_rate`, `channels` and `bitrate_kbps` describe the stream as sent, as ffprobe reports them. `content_type` stays `null` until the engine opens the HTTP connection itself, since ffmpeg does not pass it on.
- `station_time` is true when the stream carries timestamps of the receiving station (in 1.0 always false; from 1.1, once the engine reads them).
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
- `position_s` in the envelope is on the **stream's** timeline: seconds of audio decoded since the engine created the stream. It is the same for every analysis of a stream, and does not restart when an analysis is added or starts again. This is a change: today each analysis counts its own samples from the moment it was made. Without it, two analyses of one stream would disagree about when something happened, and neither would line up with relayed audio.
- Sequence numbers count per analysis and start again when the analysis starts again. A gap means events were missed. Nothing is replayed, and `Last-Event-ID` is ignored.
- `?types=` limits the stream to the listed event types, separated by commas.
- A comment line is sent every 20 seconds so that both sides notice a dead connection.
- **A subscriber that reads too slowly loses events**, as today. The engine does not slow an analysis down for a reader.
- **A subscription is what "on demand" means.** A stream in mode `on_demand` connects when the first subscription to any of its analyses opens, and closes `idle_after_s` after the last one ended.
- **The Workbench subscribes once per analysis** and hands the events on to however many pages and token holders are reading. It ends the subscription when the last of them has gone. The engine allows only `limits.subscriptions_per_analysis` at a time.

Event types today: `signal.frame`, `signal.spectrum`, `feature.state`, `event.transient`, `event.resonance`, `motif.detected`, `motif.returned`, `memory.summary`, `system.status`, and `narrative.update` from the interpretation.

`system.status` is sent every 5 seconds and on every change of the stream's state. Under the engine process it describes one analysis: the stream's state, the delay, the sample rate, the analysis's `label` and `memory.key`. The counts of listeners that today's status carries are dropped, because the engine no longer sees the readers.

### Snapshot, motifs, history

| Path | Content | As today's |
|---|---|---|
| `…/state` | the latest frame, spectrum, features and status, the active resonances and motifs, recent events, the memory's summaries | `/api/state/current` |
| `…/motifs` | the established motifs of the memory in use, and the aliases of motifs merged into others | `/api/motifs` |
| `…/motifs/{motif}` | one motif with its recent occurrences; a merged motif's old identifier leads to the one it was merged into | `/api/motifs/{id}` |
| `…/history/events` | stored events; `?from=&to=` (default: the last hour), `?type=`, `?limit=` (at most 2000) | `/api/history/events` |
| `…/history/features` | per-minute aggregates; `?window=1h` or `24h` | `/api/history/features` |

A page that opens fetches `…/state` and then follows `…/events`, as the Observatory's live page does.

## State changes of the engine

`GET /v1/events` is an event stream of changes, so that the Workbench does not have to ask every stream in turn.

| Event | Sent when | Data |
|---|---|---|
| `stream.state` | a stream changes state, or is deleted | the stream as in `GET /v1/streams/{stream}`, without its `input`; after a `DELETE` only `id` and `"deleted": true` |
| `analysis.state` | an analysis starts, stops, fails or switches memory | the analysis's identifiers, `state`, `memory.key`, and `last_error` when it failed |
| `engine.hello` | first event of every connection | `instance` and `api`, as in `GET /v1/engine`, and `resumed` (**decided** 2026-10-16): `true` when the engine started from its saved configuration |
| `usage` | every 10 seconds | `usage` of the engine, of every connected stream and of every running analysis, see [Usage](#usage) |

After connecting, the Workbench reads `engine.hello`, lists the streams once and from then on follows the events. If the `instance` has changed, it sends its whole wanted state again.

## Groups

**Proposed** as the addition 1.1 (2026-10-14). The design is in [groups.md](groups.md). An engine reporting 1.0 has none of this, and the Workbench offers no groups then.

### Station time

Additive fields, as drafted in the listen repository (its `docs/group-analysis-spec.md`, Part A):

| Field | Where | Meaning |
|---|---|---|
| `timestamp` | every envelope | station time when the stream carries it, wall time otherwise |
| `time_source` | every audio-positioned envelope, `system.status`, the stream's response | `stream`, `wall` or `recording` |
| `stream_delay_s` | `system.status`, the stream's response | how far station time is behind wall time |
| `station_time` | the probe | the stream carries timestamps |

- The engine falls back to wall time, and says why in `last_error`, when station time is more than 5 minutes away from wall time or ahead of it.
- `capabilities.station_time` in `GET /v1/engine` is true once the engine reads timestamps.

### Impulse times

A new event type of an analysis, `signal.impulses`, once per second: the second, the total count of impulses in it, and up to 40 of the strongest, each with its offset in microseconds, its height above the floor in dB and its width. It is sent only to subscribers that name it in `?types=`, since a page has no use for it. The group service is its main reader.

### A group

`PUT /v1/groups/g-0001`

```json
{
  "members": [
    { "stream": "s-0001", "analysis": "a-0001", "lat": 50.0, "lon": 10.0 },
    { "stream": "s-0002", "analysis": "a-0004", "lat": 45.0, "lon": 7.0 },
    { "stream": "s-0003", "analysis": "a-0007" }
  ],
  "analyses": ["activity"],
  "history_days": 30,
  "label": "rev-2"
}
```

| Field | Meaning |
|---|---|
| `members` | 2 to `limits.members_per_group` analyses, each named by stream and analysis. `lat` and `lon` are the station's position; needed for timing analyses. |
| `analyses` | the group analyses to run, from `capabilities.group_analyses` |
| `history_days` | days of results kept, default 30 |
| `label` | opaque, returned with the group's events, as for an analysis |

- The group's identifier follows the rules of [Identifiers](#identifiers).
- The engine refuses a group analysis that a group member cannot serve: a timing analysis needs every group member on station time (`time_source` `stream`) and with a position. The refusal is `422` with `errors` naming the group member, as in [Validation](#validation).
- A group keeps its group members' streams connected while it runs, as a subscription does. A group is either `continuous` (results all the time) or `on_demand` (only while its events have a subscriber), in a field `mode` like a stream's. Default `on_demand`.
- Deleting a stream or an analysis that is a group member takes it out of the group; the group reports the group member as `removed` and goes on with the rest.

`GET /v1/engine` gains:

```json
"limits": { "groups": 20, "members_per_group": 8 },
"capabilities": {
  "station_time": false,
  "group_analyses": [
    { "id": "activity", "timing": "seconds" },
    { "id": "clock", "timing": "milliseconds" },
    { "id": "coincidence", "timing": "milliseconds" }
  ]
}
```

### A group's results

| Path | Content |
|---|---|
| `GET /v1/groups/{group}/events` | `text/event-stream` of the group's events, as for an analysis, with `?types=` |
| `GET /v1/groups/{group}/state` | the group's status, the last 10 minutes of `activity`, the latest `correlation`, `clock` and `coincidence` results |
| `GET /v1/groups/{group}/history/events` | stored results, `?from=&to=&type=&limit=` |

| Event | Rate | Content |
|---|---|---|
| `group.status` | every 5 s | per group member: present, absent or removed, `time_source`, delay; the watermark; how far the view runs behind |
| `group.activity` | 1 per s | per group member: level, flux, impulse rate of that second |
| `group.correlation` | 1 per min | per pair: correlation of activity, its lag, and the shifted baseline |
| `group.clock` | 1 per 10 min | per pair: clock offset, spread, share of impulses in the peak, physically possible or not |
| `group.coincidence` | 1 per s | shared impulses per pair and for all three, the number expected by chance, the strongest with their times per group member |

`source` in a group event's envelope is the group's identifier. Changes of a group's state are sent on `GET /v1/events` as `group.state`.

### As the stand-in serves it

**Proposed** (2026-10-15): the shapes below are what the stand-in engine serves and the Workbench reads. They fill in what the tables above leave open; the engine session may change them, with this document.

- **Envelope:** as for an analysis, plus `label` and `time_source`. The `timestamp` of `group.activity` is the second it describes.
- **`group.activity`:** `{"second": "…Z", "members": [{"stream", "analysis", "present", "level_db", "flux", "impulse_rate"}]}`; the three values are missing when `present` is false.
- **`group.correlation`:** `{"window_s": 300, "max_lag_s": 30, "series": "impulse_rate", "pairs": [{"a", "b", "r", "lag_s", "baseline_r", "n"}]}`. `lag_s` positive means `b` hears the same later than `a`. `baseline_r` is the best correlation of the same pair shifted 150 s apart; `null` until there is enough history. `r` is `null` while fewer than half the window's seconds have both members.
- **`group.status`:** `{"members": [{"stream", "analysis", "state", "time_source", "delay_s"}], "watermark", "behind_s", "time_source"}`. On wall time `delay_s` is `null`: the engine cannot know a path delay without station time.
- **`…/state`:** `{"group": <the group>, "status": <the last status event>, "activity": [<the last 600 activity events>], "correlation": <the last correlation event>, "clock": null, "coincidence": null}`.
- **The group** as `GET` returns it: the spec's fields, each member with `state` (`present`, `absent`, `removed`), `time_source` and `delay_s`, and the group's `state` (`running` or `idle`) and `subscribers`.
- **Refusals:** a timing analysis without station time is `422` with the code `needs_station_time` at `analyses[i]`; an unknown member is `not_found` at `members[i].stream` or `members[i].analysis`.
- **`counts.groups`** in `GET /v1/engine`.

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
| A local input | **decided** in principle (2026-10-16), not before the engine's phase 2: for a stream the Observatory already decodes, its listener passes its decoded, unfiltered samples to the engine over a local socket, so the station is connected to once. A stream input such as `local:<name>`, accepted only for names the engine's operator configured, never from a member, and only after the stream's operator agreed to the Workbench (no answer is no). The stream then has the Observatory's sample rate and depends on its listener. Until then the Workbench refuses to register addresses the Observatory listens to, from a list set on the server |
| Defined motifs and profiles | a `definitions` list in an analysis; `capabilities.detectors` with each detector's parameters and ranges; a new event type |
| Trial runs on a capture | a resource for captures under a stream, and a way to run an analysis over one without touching its live memory. A trial on the live stream needs nothing new: see [Open](#answers-to-the-open-points), point 3 |
| Audio in the browser | allowed (decided 2026-10-14). A field `audio` on a stream and a data path for the encoded audio; the stream's `position_s` already allows lining events up with it. The engine's existing relay is the starting point. Its encoder's CPU and the outgoing bandwidth belong in [Usage](#usage) |
| Interpretation by a language model | decided to run for member analyses (2026-10-14), through the OpenAI and the Anthropic API, with the member's own API key. A setting of an analysis that carries provider, model and key; the engine keeps the key in memory only, never writes it to disk or logs, and never returns it in a response (`GET` shows only that a key is set). The engine would need to be told whether a person is watching: today it counts browser connections apart from programs (`?role=tool`), but under this API it sees one subscription by the Workbench. Proposed: the Workbench reports the number of people watching each analysis with a small `PUT`. Today's narrator speaks the OpenAI format only |

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
| CPU, memory and bandwidth per stream and analysis | not present |
| `position_s` on the stream's timeline | counted per analysis from its creation |
| Settings applied to a running analysis | fixed when the analysis is made; no setters |
| `memory.mode` | the three modes exist, chosen by flags at start |
| History and motifs of an earlier memory (`?memory=`) | only the memory in use is served |
| Probe reporting codec, rate and bit rate | ffmpeg runs with `-loglevel error` and reports none of these |
| 1.1: station time from the Ogg timestamp stream, `time_source` | ffmpeg drops the timestamp stream; the clock follows wall time |
| 1.1: `signal.impulses` | not present; the transient detector works on 50 ms frames |
| 1.1: groups through the control API, the group service, `activity`, then `clock` and `coincidence` | drafted in the listen repository as a separate process configured by a local file |

The paths the Observatory's site uses today (`/<slug>/ws/live` and the others) are not part of this API. Whether the engine process also serves them is a question for the day the Observatory moves onto it.

## Conformance test

**Built** (2026-10-16): `internal/conformance` in this repository, with the command `cmd/engine-check`, runs this contract's requests against any engine and reports what passes, phase by phase of the engine's plan (1: one process, many streams; 2: several analyses per stream, trials, usage, the view; 3: groups). The Workbench's tests run it against the stand-in, so the two cannot drift apart. The engine's owner runs it against the engine, from a checkout of this repository:

```text
go run ./cmd/engine-check -engine /run/listen-engine/control.sock -input <a stream address the engine may reach> -phase 1
```

The failure of an analysis cannot be provoked from outside; that check runs against the stand-in only.

## Answers to the open points

The open points of the draft, answered by the engine's owner on 2026-10-16; where they change the contract, the maintainer accepted them (**decided**, 2026-10-16).

| Open point | Answer |
|---|---|
| 1. Configuration across a restart | The engine resumes the configuration it was last given; the Workbench still holds the truth (see [The model](#the-model)). |
| 2. Two callers on one engine | Not needed. **Decided (engine)**: the Observatory stays on its own listeners, so a fault or load in the Workbench cannot take down the public site; the control API keeps one caller. A stream both use reaches the engine later through a local input (see [Reserved](#reserved)). |
| 3. Trial mode | As proposed: an ordinary analysis with memory mode `ram`. |
| 4. One spectrum per analysis | No. **Decided (engine)**: from the engine's phase 2, the decode and the spectrum run once per stream, and every analysis gets each frame through its own short queue. `limits.analyses_per_stream` is set from a measurement afterwards. |
| 5. Memory over quota | No new setting: the Workbench switches the analysis to memory mode `ram` or deletes it. |
| 6. Server-Sent Events or WebSocket | Server-Sent Events. |
| 7. Identifiers in `source` | The engine uses the identifiers it is given. The Workbench's (`s-0001`, `a-0001`) carry nothing about a member. |
| 8. Feeding the analyses | One goroutine per analysis with a short queue; a full queue drops frames for that analysis only and counts them in its status. |
| 9. A common grid | Given by the one spectrum per stream (point 4); `position_s` on the stream's timeline comes with it. |
| 10. Where the group service runs | Inside the engine process, as a module of its own (decided 2026-10-15). |

Nothing is open between the two sides at present. The engine's plan, with the owner's answers in full, is kept with the engine.
