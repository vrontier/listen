# Spec: station time, groups and the analysis service

Status: draft for review (2026-10-12). Nothing here is built yet.

## Goal

Several receivers of the same kind of signal are analysed together on one time axis, so that the
observatory can tell what they share from what only one of them hears. The first use is VLF
natural radio: an impulse that reaches two or three stations at the same instant is natural; one
that reaches a single station is local interference.

The work is split between two programs:

- the **listener** stays what it is, the reader and low-level processor of one stream;
- a new service, the **analyser**, does everything that needs more than one stream.

New analyses, including ideas from people outside the project, are added to the analyser without
touching the listeners.

Not in scope: locating lightning, a literal overlay of spectrograms, and any change to how a
single source is shown today.

## Decisions for review

These are proposals. The rest of the spec follows from them.

| Question | Proposal |
|---|---|
| Where is the line between listener and analyser? | The listener owns everything about one stream on its own: reading, station time, spectra, features, single-stream detections, memory, narrator. The analyser owns everything that compares streams. Nothing moves out of the listener now. |
| How does the analyser get its data? | As a client of each listener's `ws/live`, like `listen-mcp`. It never reads audio. |
| Which time does it align on? | Station time taken from the stream where the stream carries it; the listener's wall clock otherwise, marked as such. |
| How are analyses added? | As Go code behind one small interface. There is no rule language (the same choice as for detectors in [profiles-and-community-spec.md](profiles-and-community-spec.md)). |
| When does the work stop? | After phase 2, if the stations' activity does not correlate. See "Premise test". |

## Terms

| Term | Meaning |
|---|---|
| Station time | The time the receiving station gave a sample, carried in the stream. |
| Wall time | The listener host's clock at the moment the audio arrives. It is later than station time by the delay of the path, and that delay differs per source. |
| Group | A named set of sources that are analysed together. |
| Member | One source in a group. |
| Analysis | Code in the analyser that turns the aligned events of a group into results. |
| Watermark | The time up to which the analyser has heard from every present member. Analyses only work on time before the watermark. |

---

## Part A: station time in the listener

### What the streams carry

Streams encoded with vtvorbis (vlfrx-tools) are Ogg containers with two logical streams. One is
the Vorbis audio. The other carries a timestamp packet every 8192 samples (0.256 s at 32 kHz):

| Bytes | Content |
|---|---|
| 0–3 | magic `17 f8 60 4f` |
| 8–15 | sample position, uint64, counted from the start of the connection |
| 24–27 | UTC seconds (Unix time), uint32 |
| 28–31 | nanoseconds, uint32 |
| 32–55 | three doubles; the first was 1.0 in every stream read so far |

All values are little-endian. The layout was read from the bytes of three live streams, not from
the vlfrx-tools source. It must be checked against that source or its documentation before the
parser is written, in particular the meaning of the doubles.

### What happens today

`internal/source` hands the URL to ffmpeg, which decodes the audio and drops the timestamp stream.
The analyser's clock (`Anchor`, `Live`, `now()` in `internal/analysis`) is pinned to wall time at
every connect and counts samples from there. Consequences:

- Event times are late by the path delay. Measured: about 7 s, 13 s and 16 s for three streams
  through the same relay.
- The delay is different per source, so events from two sources cannot be compared more finely
  than to several seconds.
- On 2026-10-12, after a stream had been unreachable for 39 hours, the motif times of that source
  (`created`, `last_seen`) lagged wall time by about the length of the outage. The cause has not
  been traced. It must be found and fixed as part of this part, whether or not station time
  turns out to cure it.

### Reading the timestamps

1. For a URL input the listener opens the HTTP connection itself and feeds the bytes to ffmpeg on
   stdin (`-i pipe:0`). Reconnecting and the backoff, which are split between ffmpeg's
   `-reconnect` options and `source.Run` today, move entirely into `source.Run`.
2. The same bytes go through a small Ogg page reader. It ignores the audio pages and decodes the
   packets of the timestamp stream into pairs (sample position, station time).
3. The pairs are passed to the analyser's clock. Between two pairs the clock counts samples, as it
   does now. A new pair corrects it; it never jumps backwards within a connection.
4. A stream without timestamp packets (other encoders, files, the archive loops) behaves exactly
   as today.

### Mapping decoded samples to stream positions

The timestamp gives a time for a sample position in the encoder's count. The listener counts the
samples ffmpeg delivers. The two counts start together only if ffmpeg outputs the first sample of
the first audio page it is given, at the stream's own rate.

- The listener must run these sources at the stream's sample rate, so that ffmpeg does not
  resample. A mismatch between the profile's rate and the stream's rate is an error at start.
- The Vorbis decoder's start-up behaviour (how many samples of the first packets it outputs) sets
  a fixed offset. It is measured once: a recorded stream with a sharp impulse is decoded both by
  the listener and by vlfrx-tools, and the impulse's time is compared. The target is agreement
  within 1 ms.

### Changes to the events

Additive; the envelope version stays 1.

| Field | Where | Meaning |
|---|---|---|
| `timestamp` | every envelope | Station time when the stream carries it, as now otherwise. |
| `time_source` | every audio-positioned envelope | `stream`, `wall` or `recording`. |
| `time_source`, `stream_delay_s` | `system.status` | The source of the clock and how far station time is behind wall time. |

- `position_s` is unchanged, so the browser keeps lining events up with the relayed audio.
- The "latency" the status shows today becomes the real delay of the path (10 to 20 s for the
  relayed VLF streams). The live page must label it as the stream's delay, not as a fault.
- The memory keys nothing on absolute time, so motifs and history carry over. History written
  before the change has wall-clock times; it is not rewritten.

### When a station's clock is wrong

The listener does not judge the station's clock, with one exception: if station time is more than
5 minutes away from wall time, or ahead of it, the listener falls back to wall time, sets
`time_source: wall` and reports the reason in the status. Finer errors are measured by the
analyser (Part C, `clock`).

---

## Part B: the analyser service

### Architecture

```text
listener (source 1) ──ws/live──┐
listener (source 2) ──ws/live──┼──► listen-analyser ──► ws/live, api  ──► NGINX ──► browser
listener (source 3) ──ws/live──┘         │                              └──► listen-mcp
                                         └──► history (JSON lines per day)
```

- One Go service, `listener/cmd/listen-analyser`, in the listener's module. It reuses the
  `events` envelope and the `hub` fan-out.
- It connects to each member's listener on its local port with `?role=tool`, reconnects with
  backoff, and reads no audio.
- It runs as the `listen` user under its own systemd unit with the same resource caps as a
  listener. On production the unit and its NGINX routes need one root step.
- With the hub of [user-streams-design.md](user-streams-design.md) the analyser stays a separate
  process and connects to `/<slug>/ws/live` on the hub instead.

A tool connection keeps an on-demand source connected. A group whose members run on demand
therefore listens continuously while the analyser runs. On staging the analyser gets the same
on-demand switch: it connects to its members only while a group page is open.

### Groups

Groups are part of the source catalogue (`sources.json`, which is local and not in the
repository):

```json
"groups": [
  {
    "slug": "example-group",
    "name": "Example network",
    "listed": false,
    "members": ["station-a", "station-b", "station-c"],
    "analyses": ["activity", "clock", "coincidence"]
  }
]
```

A member that takes part in timing analyses needs its position, as a new field of the source:

```json
"position": { "lat": 0.0, "lon": 0.0 }
```

`scripts/gen-sources.sh` checks that every member exists, writes the analyser's configuration
(members, ports, positions) and the NGINX routes of the group.

### Subscribing to a listener

The analyser needs only some event types, and one of them (Part C, `signal.impulses`) is of no use
to a browser. `ws/live` gets an optional filter:

```text
/ws/live?role=tool&types=signal.frame,feature.state,signal.impulses,system.status
```

Without `types` a client receives what it receives today. Event types marked tool-only are sent
only to clients that name them.

### Alignment

- Every member has a buffer of events ordered by `timestamp`.
- The watermark is the earliest "latest timestamp" over the present members, minus 2 s.
- A member is **absent** when its listener reports a stream state other than `connected`, or when
  nothing has arrived from it for 30 s. An absent member no longer holds the watermark back. Its
  lane shows a gap.
- A member with `time_source: wall` takes part only in analyses that accept a timing accuracy of
  seconds (see the interface below).
- **The combined view runs behind real time by the delay of its slowest member plus the margin.**
  With the present VLF streams that is about 20 s. The page says so.

### Analysis interface

```go
// An Analysis turns the aligned events of one group into results.
type Analysis interface {
    ID() string
    // Needs names the event types to subscribe to and the timing accuracy
    // the analysis requires of a member (Seconds or Milliseconds).
    Needs() Needs
    // Observe receives one member's event. Events of one member arrive in
    // time order; events of different members do not.
    Observe(member string, e events.Envelope)
    // Advance is called when the watermark has moved. Everything before
    // until is complete. It returns the results that are now final.
    Advance(until time.Time) []Result
}
```

- Analyses are registered by id in code and switched on per group in the catalogue.
- An analysis keeps its own state and sees nothing of the other analyses.
- A result becomes an event with the group's slug as `source`. Result types start with `group.`.

### Events and API of a group

| Event | Rate | Payload |
|---|---|---|
| `group.status` | every 5 s | per member: present or absent, `time_source`, delay; the watermark; the delay of the view |
| `group.activity` | 1 per s | per member: level, flux and impulse rate of that second (analysis `activity`) |
| `group.correlation` | 1 per min | per pair of members: correlation of activity and its lag (analysis `activity`) |
| `group.clock` | 1 per 10 min | per pair: measured time offset, its spread, whether it is physically possible (analysis `clock`) |
| `group.coincidence` | 1 per s | number of shared impulses, the expected number by chance, the strongest shared impulses with their times per member (analysis `coincidence`) |

| Path | Content |
|---|---|
| `/<group>/ws/live` | the events above |
| `/<group>/api/state/current` | snapshot: status, the last 10 minutes of activity, the latest correlation and clock results |
| `/<group>/api/history/events` | `?from=&to=&type=&limit=`, as on a listener |
| `/<group>/api/history/export` | the same as CSV, for use outside the observatory |

History is kept as JSON lines per day, 30 days by default, like the listener's memory.

### Replaying recorded events

`listen-analyser -dump` reads event files written by `listener -dump` (one per member), runs the
group's analyses over them at full speed and writes the results as JSON lines. Every analysis is
developed and accepted on such recordings, not on a look at the live page.

### Adding an analysis

This is the path for ideas from outside the project.

1. **Try it on data.** The aligned inputs and the existing results are available through the CSV
   export and through `listen-mcp` (new tools: `list_groups`, `get_group_state`,
   `get_group_extract`). An idea can be tried in any language on a few days of data without
   access to the servers.
2. **Describe it.** One page: what it needs (event types, timing accuracy), what it reports, how a
   reader can check a result.
3. **Build it** as a Go package implementing the interface, with a test on a recorded dump.
4. **Review.** The people who proposed it check its results on recordings they know, as in the
   acceptance procedure for defined motifs.

If an idea needs a low-level measure the listeners do not produce yet, that measure is added to
the listener as a new event type. The analyser never reaches for audio.

---

## Part C: the first analyses

### `activity`

- Input: `signal.frame` (10 per second) and `feature.state` of every member; timing accuracy
  seconds.
- Per member and second: mean level, mean flux, and the impulse rate once `signal.impulses`
  exists.
- Per pair and minute: the correlation of the two flux curves over the last 10 minutes, for lags
  from −5 s to +5 s, with the lag of the maximum.
- Each correlation is reported next to the same figure for a shifted copy (one curve moved by
  5 minutes). Without that baseline a correlation figure cannot be read.

### Impulse times: a new low-level output of the listener

The existing transient detector works on 50 ms frames and is throttled on the VLF sources (at most
one transient per 5 s). Matching single impulses between stations needs times good to well under a
millisecond and many impulses per second.

- A detector in the time domain: the samples are high-passed at the profile's `min_hz`, an
  envelope is formed over about 1 ms, and a peak counts as an impulse when it stands a factor
  above the running median of the last second.
- The time of an impulse is the station time of the envelope's maximum.
- Event `signal.impulses`, once per second, tool-only: the second, and up to 40 of its strongest
  impulses, each with its offset in microseconds, its height above the floor in dB and its width.
  The total count of the second is given as well.
- The numbers (1 ms, the factor, 40) are placeholders. They are set on recordings.

This detector and the `impulse` detector of
[profiles-and-community-spec.md](profiles-and-community-spec.md) look for the same thing with
different aims: that one decides whether an impulse is a sferic by its spectrum, this one times
every impulse. Whether they become one detector is decided when both exist.

### `clock`

- Input: `signal.impulses`; timing accuracy milliseconds; positions of the members.
- Per pair: a histogram of the time differences between the impulses of the two members, within
  ±100 ms, collected over an hour.
- Impulses that both stations received pile up at one difference; impulses that are unrelated
  spread evenly. The position of the pile is the difference of the two clocks plus the difference
  in travel time.
- A VLF signal travels at close to the speed of light, so the difference in travel time between
  two stations cannot exceed their distance divided by c: about 1.7 ms at 500 km and 22 ms at
  6500 km. A pile outside that range means one of the clocks is off by at least the excess.
- Reported per pair: the offset, its spread, the share of impulses in the pile, and whether the
  offset is physically possible. The analyser reports a clock error. It does not correct it
  silently.

This analysis is also the sharpest test of the premise: no pile, no shared impulses.

### `coincidence`

- Input: `signal.impulses`; timing accuracy milliseconds; positions; the latest `clock` result.
- Two impulses of two members are a coincidence when their time difference lies within the
  possible travel-time difference of the pair, widened by the spread the `clock` analysis
  measured.
- Each second reports the coincidences per pair and, for three members, the impulses all three
  share.
- **Chance.** With many impulses per second, impulses match by accident. For rates r₁ and r₂ per
  second and a window of width w seconds, about r₁ · r₂ · w accidental matches per second are
  expected. At 100 impulses per second per station and a 6 ms window that is 60 per second, more
  than the real ones. Every count is therefore reported with its expected chance count, and the
  page shows a coincidence rate only where it stands clearly above chance.
- The other reading of the result is as useful: an impulse with no partner at a station that was
  present is a candidate for local interference at the station that heard it.

---

## Part D: the group page

The data contract is Part B. The drawing is a design step of its own and is not specified here.
What the page has to show:

| Element | Content |
|---|---|
| Lanes | One per member on a shared time axis in UTC, the last 10 minutes, with gaps where a member was absent. |
| Coincidences | Marks across the lanes of the members that shared an impulse. |
| Map | The members' positions and the day/night line, since propagation over long paths depends on darkness. |
| Status line | How far the view runs behind real time, each member's delay and clock state. |
| Links | To each member's own page. |

A group page is unlisted and `noindex` until its members' operators have agreed (see below).

---

## Consent

A combined view is a new use of the members' streams. Under the observatory's rule, each
operator is asked before a group that includes their stream is shown publicly, and no answer
means no. Until then groups run on staging only. The same message asks each operator how their
station's clock is kept (GPS-disciplined or not), which the `clock` analysis can then confirm.

## Premise test

The whole idea rests on an assumption that has not been tested on this data: that the stations
receive enough of the same signals for the comparison to show something.

After phase 2 the `activity` analysis runs on staging for at least three days that include
nights. The work continues if, for at least one pair, the correlation of activity is clearly and
repeatedly above its shifted baseline. If it is not, phases 3 to 5 are not started and the result
is written down. Station time (phase 1) is worth having either way.

The history stored before phase 1 is not used for this test. Its times are wall-clock times with
a different delay per source.

## Phases

| Phase | Content | Needs |
|---|---|---|
| 0 | Ask the operators: combined view, and how the station clock is kept | |
| 1 | Listener: Ogg timestamp reader, clock from station time, `time_source`, the offset measurement, the cause of the lag after an outage | vlfrx-tools documentation of the packet |
| 2 | Analyser: ingest, alignment, `activity`, history, `-dump`; staging only. Then the premise test. | phase 1 |
| 3 | Listener: `signal.impulses` and the `types` filter. Analyser: `clock`, `coincidence` | a positive premise test; positions of the members |
| 4 | Group page, `listen-mcp` tools, CSV export; production | the operators' agreement; one root step on production; the page design |
| 5 | Further analyses, as they are proposed | |

## Open questions

1. Is the proposed line between listener and analyser the one intended, or should the memory and
   the narrator move to the analyser as well, leaving the listener with decoding and features
   only? The proposal keeps them where they are because they concern one stream.
2. Should a group have a narrator of its own ("all three stations grew louder after dusk")?
3. Should the group page be listed on the landing page, or reached only from its members' pages?
4. May the CSV export be public, or only available through `listen-mcp` and to named people? The
   export contains analysis data, no audio.
5. The names `listen-analyser`, "group" and "member" are working names.
6. How are a station's impulses to be compared when two stations use different antennas (electric
   or magnetic) and gains? The first version compares times only, not amplitudes.
7. Does a relay re-encode or only pass the stations' streams through? If it re-encodes, the
   timestamps are the relay's and not the station's. This is a question for the relay's operator.
