# Spec: profiles, defined motifs and the community section

Status: draft for review (2026-10-10). Nothing here is built yet.

> **Part C moved (2026-10-13).** The community section (accounts, own sources, publishing) is no
> longer planned for Listening Observatory, which stays a curated exhibit without accounts. It
> continues in [Listening Observatory Workbench](../workbench/), as
> [`workbench/docs/members-and-streams.md`](../workbench/docs/members-and-streams.md). Parts A and B (profiles and defined motifs) stay here: they
> belong to the listener, which both projects share. Part C below is kept as the original draft.

This spec has three parts that build on each other:

- **Part A, profiles:** a named set of listening settings that a source refers to.
- **Part B, defined motifs:** motifs with a written definition (a whistler, a sferic), found by
  dedicated detectors that a profile switches on and tunes.
- **Part C, community section:** members log in, register their own source, write profiles and
  decide to publish. It extends [user-streams-design.md](user-streams-design.md), which stays the
  reference for the hub, the catalogue, the stream probe and the limits.

Parts A and B are the first step and need no accounts. Part C needs the hub from the earlier
design.

## Decisions already taken

| Question | Decision |
|---|---|
| Who writes the motif definitions for a domain? | The domain's experts. The observatory builds the detectors to their numbers and labels nothing on a public page before they have reviewed it. |
| Do learned motifs stay on sources that have defined motifs? | Yes, both run side by side. |
| First step | Profiles, plus two defined motifs for VLF radio: sferic and whistler. Tweek and chorus follow. |

## Terms

| Term | Meaning |
|---|---|
| Learned motif | What the memory finds by itself today: a recurring steady partial (resonance motif) or a recurring overall sound (texture motif). It has a number, no name. |
| Defined motif | A phenomenon with a written definition and a name. It is found by a detector, not by recurrence. |
| Detector | Code in the listener that finds one kind of shape in time and frequency, for example a short broadband impulse or a falling tone. |
| Definition | One detector plus its parameters, a name and a description. |
| Profile | Analysis settings, a list of definitions and the narrator's vocabulary, under one name. |

---

## Part A: profiles

### What a profile replaces

Today a source carries its settings as raw listener flags in `listener.args`. The VLF sources
repeat the same block and differ only in the mains frequency. A profile gives that block a name
and adds what flags cannot express: the motif definitions.

### Format

A profile is a JSON document. Built-in profiles live in `profiles/<id>.json` in the repository;
they describe a kind of signal, never a particular source, so they can be public.

```json
{
  "id": "vlf-radio",
  "name": "VLF natural radio",
  "revision": 1,
  "description": "Very low frequency radio received as audio: sferics, tweeks, whistlers, chorus.",
  "parameters": {
    "mains_hz": { "type": "choice", "values": [50, 60], "required": true }
  },
  "analysis": {
    "sample_rate": 32000,
    "min_hz": 800,
    "max_hz": 0,
    "mains_hz": "$mains_hz",
    "transient_k": 10,
    "transient_gap": "5s"
  },
  "learned": { "resonance": true, "texture": true },
  "motifs": [],
  "vocabulary": {}
}
```

- **`parameters`** are the values that differ between sources of the same kind. A source sets
  them; everything else comes from the profile.
- **`analysis`** holds exactly what the flags `-sample-rate`, `-min-hz`, `-max-hz`, `-mains`,
  `-transient-k` and `-transient-gap` hold today.
- **`learned`** can switch the two learned kinds off. Both are on by default.
- **`motifs`** and **`vocabulary`** are specified in Part B.
- **`revision`** is raised by hand whenever a definition changes. It is recorded with every
  detection (see "Reproducibility").

### How a source refers to it

```json
"listener": {
  "port": 8101,
  "input": "https://stream.example.org/live.mp3",
  "profile": "vlf-radio",
  "profile_params": { "mains_hz": 60 },
  "args": []
}
```

- `scripts/gen-sources.sh` checks that the profile exists and that every required parameter is
  set, installs the profile file next to the source's env file, and adds
  `-profile <file> -profile-param mains_hz=60` to the listener's arguments.
- The listener reads the profile first; a flag given explicitly still wins. A source without a
  profile behaves exactly as today, so nothing has to migrate at once.

### Memory continuity

The memory directory is keyed by the effective range and mains setting (`focusKey`). A source that
moves from flags to a profile with the same values keeps its key, and so its motifs and history.
The profile id and revision are not part of the key: a new definition must not empty the memory.

### Built-in profiles in the first step

| Profile | Content |
|---|---|
| `general` | Today's defaults; what a source without a profile gets. |
| `vlf-radio` | Today's VLF settings, one parameter (`mains_hz`), and the defined motifs of Part B. |

Profiles for the archive loops and for hydrophones can follow; they would carry only analysis
settings at first.

---

## Part B: defined motifs

### Why this needs new detectors

The analysis runs 20 frames per second on a 4096-point window (128 ms at 32 kHz). That suits
steady tones and slow textures. The VLF phenomena are short and move:

| Phenomenon | Shape | Duration |
|---|---|---|
| Sferic | broadband impulse | a few ms |
| Tweek | impulse with a tail that settles on a cutoff near 1.7 kHz (higher modes near 3.4 kHz) | 10–100 ms |
| Whistler | tone falling through 1–9 kHz, following a dispersion law | 0.3–3 s |
| Chorus | repeated rising chirps in 1–9 kHz | 0.1–1 s each |
| Hiss | steady noise limited to a band | minutes |

A sferic is smeared across the whole window, and a whistler that falls 6 kHz in a second smears
over several hundred Hz within one frame. Neither can be matched by the resonance detector, which
needs a frequency that holds still.

### Fine analysis path

A second spectrogram runs beside the existing one, only when the profile has definitions:

| | Existing path | Fine path |
|---|---|---|
| Window | 4096 points (128 ms at 32 kHz) | 1024 points (32 ms) |
| Hop | 50 ms | 8 ms |
| Bin width | 7.8 Hz | 31 Hz |
| Feeds | bands, features, resonances, textures | defined-motif detectors only |

- Each bin keeps a running median as its noise floor (about 10 s), so detectors work on
  "dB above the floor", not on absolute level.
- Bins on mains harmonics are masked when the profile sets `mains_hz`.
- Nothing from the fine path is sent to the browser as raw data. The cost is about 125 small
  FFTs per second per source; it must be measured on staging before it reaches production.

### Detectors

Detectors are a small fixed set in Go. A definition picks one and sets its parameters. There is
no rule language: a new kind of shape means a new detector in code.

| Detector | Finds | Used for | Step |
|---|---|---|---|
| `impulse` | a rise above the floor across most of a band, lasting only a few frames | sferic | 1 |
| `sweep` | a ridge that moves steadily up or down in frequency, optionally fitted to a dispersion law | whistler | 1 |
| `dispersed_impulse` | an impulse followed by a tail converging on a cutoff frequency | tweek | 2 |
| `chirp_train` | several short sweeps in the same band within a few seconds | chorus | 2 |
| `band_noise` | raised, steady noise limited to a band | hiss | 2 |
| `line` | a steady narrow line at a stated frequency | known interference, pilot tones | 2 |

### Definition format

```json
{
  "id": "whistler",
  "name": "Whistler",
  "description": "A falling tone from lightning, dispersed along the Earth's magnetic field.",
  "detector": "sweep",
  "report": "each",
  "return_after": "30m",
  "params": {
    "direction": "falling",
    "band_hz": [1000, 9000],
    "duration_s": [0.3, 3.0],
    "min_span_hz": 2000,
    "min_snr_db": 6,
    "min_continuity": 0.8,
    "law": "dispersion",
    "dispersion": [20, 200],
    "max_fit_error": 0.15
  }
}
```

```json
{
  "id": "sferic",
  "name": "Sferic",
  "description": "The radio impulse of a lightning stroke.",
  "detector": "impulse",
  "report": "rate",
  "params": {
    "band_hz": [1000, 15000],
    "min_band_share": 0.6,
    "max_duration_ms": 30,
    "min_snr_db": 10
  }
}
```

**The numbers above are placeholders.** They come from general literature and only show the
shape of a definition. The real values are the experts' to give.

- **`sweep`** links peaks above the floor from frame to frame into a ridge. A finished ridge is a
  detection when its direction, band, duration and span fit, and enough of its frames had a peak
  (`min_continuity`). With `law: "dispersion"` the ridge is also fitted to
  t = t₀ + D / √f; the detection needs D inside the stated range and a fit error below the limit.
  D is reported with the detection.
- **`impulse`** needs `min_band_share` of the band's bins above `min_snr_db` at once, and back at
  the floor within `max_duration_ms`.
- **`report`** says how detections reach the browser:
  - `each`: one event per detection. For rare phenomena.
  - `rate`: detections are counted, and a rate per minute is sent with the two-per-second feature
    update. For sferics, which can arrive many times per second in a storm.

### Events

Additive; the envelope version stays 1.

| Event | When | Payload (new fields) |
|---|---|---|
| `event.occurrence` | a definition with `report: each` was detected | `motif_id`, `duration_ms`, `start_hz`, `end_hz`, `snr_db`, `confidence`, `measured` (detector-specific, e.g. `dispersion`), `profile_revision` |
| `feature.state` | as today | plus `rates`: detections per minute for each `report: rate` definition |
| `motif.detected` | the first detection ever of a definition on this source | as today, `kind: "defined"`, plus `name` |
| `motif.returned` | a detection after `return_after` without one | as today, `kind: "defined"`, plus `name` |

The browser passes every `event.*` type to the observations panel, so `panels.js` needs a case
for `event.occurrence` before the listener emits it.

### In the memory

- A defined motif is a `Motif` with `Kind: "defined"`. Its public id is the definition's id
  (`whistler`); learned motifs keep `motif-NNN`, so the two cannot collide.
- It is established with its first detection. The definition is its identity, so it needs no
  recurrence to earn one.
- Occurrences, presence (the sum of durations), last seen and the recent list work as for learned
  motifs. For `report: rate` definitions the recent list holds one entry per active minute, not
  one per impulse.
- Defined motifs are never merged and never blended: their prototype is the definition.
- `api/motifs`, the CSV export and `listen-mcp` show them with `kind: "defined"` and their name,
  with no change to the routes.

### Existing transients

On a source with an `impulse` definition, sferics are counted by that detector. `event.transient`
stays for what the existing detector reports, and the two are not merged in step 1. Whether the
`-transient-k 10 -transient-gap 5s` throttling of the VLF sources can then be relaxed is decided
after measuring both on the same audio.

### On the live page

| Element | Step 1 |
|---|---|
| Observations panel | "Whistler, 7.8 → 2.1 kHz in 0.9 s"; the sferic rate as one line that updates |
| Memory dial | a named band over the definition's `band_hz`, in the style of the texture bands |
| Motif CSV | new rows, `kind` = `defined`, `name` filled |
| Field | unchanged |

A drawn form for a whistler in the field is a design question of its own and is left out of
step 1 on purpose.

### Narrator

- `vocabulary` in the profile maps each definition id to the words the narrator may use for it,
  with a one-line explanation, so that the text says "whistler" only when one was detected.
- The facts get one line per defined motif with activity, for example "Whistler: 3 in the last
  10 minutes; the last fell from 7.8 to 2.1 kHz in 0.9 s."
- The existing check stays: a text that names a motif or a frequency that is not in the facts is
  discarded.

### Reproducibility

Detections are meant to be usable as data, so every one must be traceable to the definition that
produced it:

- every `event.occurrence` and every history record carries `profile_revision`;
- the history keeps the measured values of each detection, not only its count;
- the profile file of each revision is kept, so an old detection can be read against the
  definition of its time.

### Acceptance

A definition is accepted on evidence, not on a look at the live page:

1. The experts name recordings (or times in a live stream) in which they have marked the
   phenomena.
2. `listener -dump` runs the profile over those recordings and writes the detections.
3. A script compares both lists and reports, per definition, how many marked events were found and
   how many detections were not marked.
4. The experts review the misses and the false detections; the parameters are adjusted and the
   revision raised.

The target figures (how many misses and false detections are acceptable) are an open question.
Until a definition is accepted, it runs on staging only.

---

## Part C: community section

> Moved to Listening Observatory Workbench; see the note at the top.

### What changes against the earlier design

[user-streams-design.md](user-streams-design.md) stays valid for: one hub process instead of a
unit per source, the SQLite catalogue, login by email link, the stream probe, the protection
against requests to internal addresses, and the limits. This part adds or changes:

| Topic | Earlier design | Now |
|---|---|---|
| Analysis settings | three fixed presets, phase 3 | profiles, which a member can write |
| Going public | an admin approves | the member decides to publish; whether an admin still reviews is open (question 3) |
| Before publication | reachable by URL, unlisted | private: visible only to the logged-in owner |

### Roles

| Role | Can |
|---|---|
| Visitor | see published sources |
| Member | register own sources, write profiles, see own unpublished sources, publish, withdraw |
| Admin | review, suspend sources and accounts, maintain built-in profiles |

### What a member owns

**Sources.** As in the earlier design: stream URL, texts, credit, the attestation "I run this
stream or have the right to have it analysed", and the separate consent to audio in the browser.
New: a profile and its parameters.

**Profiles.** A member starts from a built-in profile or from a copy of one.

| Property | Rule |
|---|---|
| Visibility | private (default) or shared; a shared profile can be used and copied by other members |
| Editing | a form over the profile format: analysis settings, then definitions built from the detectors, each parameter with its allowed range |
| Revisions | saving creates a new revision; a source keeps running on the revision it was started with until its owner switches it |
| Limits | at most 12 definitions per profile; the detectors and their parameter ranges are fixed by the listener |

A member cannot add a detector, only parameters. That keeps the cost per source bounded and means
no user-written code or expressions run on the server.

### States of a source

```text
draft ──► private ──► published
            ▲  │          │
            │  ▼          ▼
          paused      withdrawn (back to private)

any state ──► suspended (admin)
```

| State | Analysed | Who sees the live page | Listed |
|---|---|---|---|
| Draft | no | nobody; the probe has not passed yet | no |
| Private | yes, on demand | the owner, logged in | no |
| Published | yes | everyone | yes |
| Paused | no | the owner sees the last state | no |
| Suspended | no | nobody; the owner sees the reason | no |

- A private source runs on demand only (it connects while its owner has the page open), so
  unpublished sources cost almost nothing.
- The hub must check who is asking for a private source. The site issues a short-lived signed
  token to the logged-in owner; the hub verifies it on `ws/live`, `ws/audio` and the API paths.
- Publishing asks once more for the rights attestation and shows the page as visitors will see it.
- Withdrawing takes effect at once and removes the source from the landing page and the sitemap.

### Trying a profile

Tuning a definition on a live stream is slow, because the phenomenon may not occur for hours. The
owner can therefore record a capture of their own source (up to 10 minutes, kept privately, deleted
after 7 days) and run a profile revision against it. The page shows the capture's spectrogram with
the detections drawn on it and lists their measured values. This is the same run as the acceptance
procedure in Part B.

### Pages

| Path | Content |
|---|---|
| `/login` | email link |
| `/account` | own sources and profiles, with state |
| `/account/sources/new`, `/account/sources/<slug>` | register and edit, choose profile, publish, pause, withdraw, delete |
| `/account/profiles/new`, `/account/profiles/<id>` | profile editor, revisions, share |
| `/account/sources/<slug>/try` | capture and trial run |
| `/admin` | review queue, suspensions, audit log |

### Catalogue additions

To the tables of the earlier design (users, sources, source_reviews, audit_log):

- `profiles` (id, owner, name, visibility) and `profile_revisions` (profile, revision, JSON,
  created);
- `sources` gains `profile`, `profile_revision`, `profile_params` and `state`;
- `captures` (source, started, length, file, expires).

Built-in profiles are loaded from `profiles/*.json` into the catalogue with the observatory as
owner.

---

## Phases

| Phase | Content | Needs |
|---|---|---|
| 1 | Profile format, `-profile`, `gen-sources.sh`; fine path; `impulse` and `sweep`; sferic and whistler on the VLF sources, staging only | definitions and marked recordings from the experts |
| 2 | Acceptance run; panel, dial, CSV, narrator, MCP; production | accepted definitions |
| 3 | `dispersed_impulse`, `chirp_train`, `band_noise`, `line`: tweek, chorus, hiss, interference lines | further definitions |
| 4 | Hub and catalogue (phase 0 of the earlier design); curated sources migrate | one root step on production |
| 5 | Accounts, own sources, private state, publishing, admin pages | privacy notice, Impressum |
| 6 | Profile editor, revisions, sharing, captures and trial runs | |
| 7 | Organisation feeds (phase 2 of the earlier design) | |

Phases 1 to 3 and phase 4 do not depend on each other and can run in either order.

## Open questions

**Profiles and defined motifs**

1. In what form will the experts deliver definitions: numbers in a mail, a table, or would they
   edit the JSON themselves?
2. Which recordings serve as the marked reference, and what share of misses and false detections
   is acceptable per phenomenon?
3. Is the dispersion law the right criterion for a whistler on these receivers, or is a simpler
   "falling tone within these limits" enough for a first version?
4. Should a `line` definition be able to remove a known interference line from the analysis
   (as `-mains` does for hum), or only name it?
5. Is 32 kHz the sample rate to design for, or will some receivers deliver more?

**Community section**

6. Publishing: does a member's stream go public at once, or after an admin's review? Proposal:
   review for a member's first stream, none afterwards, with suspension as the safeguard.
7. May a member share a capture or its detections with others, or do captures stay strictly
   private?
8. Should shared profiles be visible to visitors too (as documentation of what a stream detects),
   or only to members?
9. Does a published source show which profile and revision it runs? Proposal: yes, with the
   definitions readable, so that a "whistler" on the page can be checked against its definition.
10. The five open questions of [user-streams-design.md](user-streams-design.md) are still open:
    who reviews, audio relay for member streams, the limits, the narrator for member streams, and
    the privacy notice.
