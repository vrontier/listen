# Analyses with several sources

Status: draft (2026-10-14). Nothing here is built.

Each statement is marked **decided**, **proposed** or **open**, as in [design.md](design.md).

**Decided** (2026-10-14): in the Workbench this is not a separate object. It is an analysis with two or more sources ([design.md](design.md#analyses-and-sources)). Inside the engine the comparison is a **group**, the term the control API and the Observatory's draft use; this document uses "group" only for that engine side.

This document takes the group design drafted for Listening Observatory (its `docs/group-analysis-spec.md`, "station time, groups and the analysis service", 2026-10-12) into the Workbench. Where the two differ, the difference is named.

## Two layers

**Decided** (2026-10-14): comparing several sources is a second layer on top of the analysis of each single source, not a replacement for it. A member can combine sources they registered themselves; sharing is not needed for that.

```text
stream A ──► analysis of A (own range, rules, memory) ──┐
stream B ──► analysis of B (own range, rules, memory) ──┼──► group: aligned on one time axis ──► group analyses
stream C ──► analysis of C (own range, rules, memory) ──┘
```

Why the layer below stays as it is:

- **Filters belong to a receiver.** Each station has its own interference: the mains frequency of its country, a nearby railway's supply, its own equipment. Rules are set per stream analysis and cannot be shared across a group whose stations differ.
- **The group never reads audio.** It works on what the stream analyses produce, as the Observatory's draft decided. Analysis code stays in the engine (rule 1 of [design.md](design.md)).

## Terms

| Term | Meaning |
|---|---|
| Analysis | In the Workbench: a member's analysis of 1 to n sources, with settings per source. |
| Group | In the engine: the comparison of the engine analyses that make up a Workbench analysis with two or more sources. |
| Group member | One engine analysis in a group, that is one source of the Workbench analysis. The Observatory's draft says "member"; here "member" is a person. |
| Station time | The time at which the receiving station recorded a sample, carried in the stream. |
| Wall time | The engine host's clock when the audio arrives: later than station time by the path delay, which differs per stream. |
| Watermark | The time up to which every present group member has been heard. Group analyses work only on time before it. |
| Group analysis | Code in the engine that turns the aligned events of a group into results. |

## In the Workbench

**Decided** (2026-10-14): an analysis is the member's object with 1 to n sources; settings are kept per source; one revision number covers the whole analysis ([design.md](design.md#analyses-and-sources)). **Proposed**, for two or more sources:

- The comparing analyses to run (`activity`, later `clock`, `coincidence`) are part of the analysis's settings and of its revision.
- An analysis may have at most 8 sources at first. The cost of a comparison grows with the pairs it compares (28 pairs for 8).
- Timing analyses need coordinates. They come from the sources (the coordinates field added on 2026-10-14). A timing analysis cannot be switched on while one of the sources has none.
- Publishing needs the consent of the owner of every source in it, and the operators' agreement to a combined view (see [Rights](#rights)).
- The comparison's cost is counted against the member's cap, with the rest of the analysis (see [members-and-streams.md](members-and-streams.md#resources-and-caps)).

## Time

Comparing streams needs a common time axis. How fine it can be depends on where the time comes from.

| Time source | Accuracy between streams | Allows |
|---|---|---|
| Wall time | several seconds: the path delays differ per stream (measured: about 7, 13 and 16 s for three streams through one relay) | `activity` |
| Station time | about a millisecond, if the stations' clocks are good | `activity`, `clock`, `coincidence` |

- Some VLF streams carry station time: their Ogg container has a second logical stream with a timestamp packet every 8192 samples (sample position, UTC seconds and nanoseconds). ffmpeg drops it today. The Observatory's draft describes reading it (its Part A).
- **Station time needs the stream decoded at its own sample rate**, so that the decoder's sample count matches the timestamps' count. A stream that carries station time therefore has its sample rate fixed to the rate the probe found; the registration form does not offer a choice for it. This corrects the default of 22 050 Hz in [control-api.md](control-api.md#choosing-the-sample-rate) for such streams.
- Every event carries the source of its time (`stream`, `wall` or `recording`). A group shows for each group member which time it is on, and offers only the group analyses that this accuracy allows.
- A group runs behind real time by the delay of its slowest group member plus a margin: about 20 s with the relayed VLF streams. The group page says so.

## Group analyses

The first three, as drafted for the Observatory (its Part C). **Proposed.**

| Analysis | Needs | Reports |
|---|---|---|
| `activity` | wall time is enough | per group member and second: level, flux, impulse rate; per pair and minute: correlation of activity with its lag, next to a shifted baseline without which the figure cannot be read |
| `clock` | station time, coordinates, impulse times | per pair and 10 minutes: the measured clock offset, its spread, and whether it is physically possible given the distance (a VLF signal cannot arrive later at the farther station than distance / c) |
| `coincidence` | station time, coordinates, impulse times | per second: impulses shared by two or three stations within their possible travel-time difference, with the number expected by chance; an impulse without partner at a present station is a candidate for local interference |

- **Impulse times** are a new low-level output of each stream analysis: impulses timed to well under a millisecond, up to 40 per second. The existing transient detector is too coarse for this.
- **Every count is reported with its chance count.** At 100 impulses per second per station and a 6 ms window, about 60 coincidences per second are expected by chance alone.
- **Group analyses are code in the engine**, behind one small interface. There is no rule language and no user-written code (a decided rule of the Workbench). New ones come as proposals, tried on exported data first, as the Observatory's draft describes.

### The premise test

The Observatory's draft makes the further work conditional on a test: `activity` runs for at least three days including nights, and the work on `clock` and `coincidence` continues only if at least one pair of stations correlates clearly and repeatedly above its baseline. **Proposed:** the Workbench takes this over. Groups with `activity` are useful either way; the timing analyses are built only after a positive test.

## The group page

**Proposed.** The drawing is a design step of its own.

- Lanes, one per group member, on one time axis in UTC, the last 10 minutes, with gaps where a group member was absent.
- Marks across lanes for coincidences, once that analysis exists.
- A map with the stations and the day-night line, since propagation over long paths depends on darkness. The map pop-up's library is already in the Workbench.
- A status line: how far the view runs behind real time, each group member's time source and delay, the clock state.
- Links to each group member's analysis page.
- Export as CSV and JSON, as for an analysis.

## Rights

- **Decided** (rule 4): every stream in a group is one the member may use: their own, or one whose operator agreed to its use in the Workbench.
- **Proposed:** a combined view is a new use of each stream, as the Observatory's draft says. A private group of a member's own streams needs nothing more. Publishing a group needs each stream owner's consent in the Workbench, and the stream's attestation must cover a combined view.
- **Proposed:** the operators are also asked how their station's clock is kept (GPS-disciplined or not), which `clock` can then confirm.

## In the engine

**Proposed**, for the engine session to decide.

- The Observatory's draft has a separate process, `listen-analyser`, reading each listener's event stream, with groups in a local configuration file. Under the engine process of [design.md](design.md#processes) the engine already holds every analysis's events in one process. The group service can run inside it, or stay a separate process fed by the engine. The control API is the same either way.
- Groups are configured through the control API like streams and analyses: the Workbench holds them in its catalogue and puts them into the engine. See [control-api.md](control-api.md#groups).
- The engine knows a group's group members by their stream and analysis identifiers, and their positions as given. It knows nothing of the member who owns the group.

## Order

**Proposed.** The engine steps are listed in [design.md](design.md#what-the-engine-must-learn-first).

1. Specify groups in the control API (this document and [control-api.md](control-api.md#groups)).
2. Analyses with 1 to n sources in the Workbench (decided 2026-10-14). Then the comparison view, against the stand-in, which makes up group results.
3. In the engine, after pieces A and B: the group service with `activity` on wall time. Then the premise test.
4. Station time and impulse times in the engine, then `clock` and `coincidence`, if the premise test was positive.

## Open

1. **The name.** **Decided** (2026-10-14): an analysis with several sources; "group" only inside the engine.
2. **The first goal.** Side-by-side activity first (possible on wall time), or straight to the VLF coincidence analysis (needs station time)?
3. **Sources of other members.** With sharing, an analysis can use a source someone else registered; its settings for that source are still its own. Settings another member made for the same source are not reused, which keeps a result traceable to settings the analysis's member controls.
4. **Does a relay re-encode?** If a relay re-encodes the stations' streams, the timestamps are the relay's, not the stations'. A question for the relay's operator, carried over from the Observatory's draft.
5. **A narrator for a group** ("all three stations grew louder after dusk") was asked in the Observatory's draft. Not proposed for the first version.
