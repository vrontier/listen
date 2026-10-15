# Analyses and filters

Status: draft (2026-10-13), with the maintainer's answers of 2026-10-14. The filter rules described here exist in the listener. Running several analyses on one stream does not.

## Terms

| Term | Meaning |
|---|---|
| Stream | A live audio source at a URL, run by its operator. |
| Decode | The listener's connection to a stream and the audio samples it produces, at one sample rate. |
| Analysis | One run of the listener's signal processing over the decoded audio, with its own settings, filter rules, motif definitions and motif memory. The sample rate is not one of its settings; see below. |
| Filter rule | A statement that spectral peaks at certain frequencies are known interference. |
| Motif | Something that recurs: learned (found by recurrence) or defined (found by a detector with a written definition). |

## One decode, several analyses

```text
                              ┌──► analysis A   Observatory's settings and rules
stream ──► one connection ──► │
           one decode         ├──► analysis B   researcher 1: no filter
                              └──► analysis C   researcher 2: own rules, own motifs
```

- There is one connection and one decode per stream.
- Each analysis has its own settings, rules and memory, and its own event stream.
- Analyses do not see each other's results.
- The number of analyses per stream is capped.

Today one listener process runs exactly one analysis. This is the first thing the engine has to learn before the Workbench can do research on shared streams. How the work divides is in [design.md](design.md#what-the-engine-must-learn-first).

### What belongs to the decode and what to an analysis

| Setting | Belongs to | Note |
|---|---|---|
| Stream address | decode | |
| Sample rate | decode | see below |
| Frequency range (lowest and highest analysed frequency) | analysis | |
| Filter rules | analysis | |
| Transient sensitivity and minimum gap | analysis | |
| Motif definitions | analysis | not built |
| Motif memory | analysis | |

**Sample rate belongs to the decode.** The listener asks ffmpeg for mono audio at one rate, and ffmpeg resamples to it before any analysis sees a sample. Two analyses on one decode therefore share the rate. Today the rate is given per listener (`-sample-rate`), and the Observatory's profile draft lists `sample_rate` among the analysis settings. With one decode and several analyses neither can stay so.

**Proposed:** the sample rate is a property of the stream, set when the stream is registered from what the probe measured, and the same for every analysis of it. A profile may state the rate it was designed for, and the engine refuses an analysis whose profile does not fit the stream's rate. The analysis window has a fixed number of points, so the width of an analysis bin follows from the stream's rate (about 5 Hz at 22.05 kHz, about 8 Hz at 32 kHz).

**The memory needs the analysis in its key.** Today the memory's directory is named after the source, the input and a key made of the frequency range and the rules. Two analyses of one stream with the same range and rules would share one directory and one memory. **Proposed:** the analysis's identifier becomes part of the key. This is a change in the engine.

## The rule between the two projects

**The Observatory's filter rules never apply to the Workbench.**

A rule belongs to an analysis. The Observatory's rules express what its maintainer considers interference on a public page. A researcher may study exactly that signal, so a Workbench analysis starts with no rules and gets only the ones its owner defines.

The Workbench therefore does not use the Observatory's event stream or MCP server as research data. In the Observatory's analysis:

| Data | Filtered by its rules? |
|---|---|
| Audio as relayed to the browser | no |
| Spectrum band levels (the spectrogram) | no |
| Frame levels: level, energy, flux, centroid, entropy | no |
| Transient events | no |
| Spectral peaks | yes |
| Dominant frequency | yes |
| Harmonicity, in the frame levels and in the feature state | yes: it is computed from the peaks that remain |
| Signal state (for example "stable resonance", "broadband noise") | yes: it is decided from harmonicity and the number of active resonances, among other values |
| Resonance events | yes |
| Learned motifs and the motif memory | yes: resonance motifs come from resonance events; texture motifs use the unfiltered band levels together with harmonicity |
| Interpretation texts | yes, since they describe the events above |

"No" means only that the rules do not touch the value. The unfiltered rows are still shaped by the other settings of that analysis:

- Band levels, flux, centroid and entropy are computed inside the analysis's frequency range. What lies outside the Observatory's range is not in them.
- Band levels are scaled against that analysis's own running level range, so they are relative values, not absolute ones.
- Transients depend on that analysis's sensitivity and minimum gap, which the Observatory sets high on streams that crackle constantly.

So even the unfiltered part of the Observatory's data is the Observatory's view of the stream. This is one more reason for the rule above.

## Filter rules

A rule removes matching spectral peaks before anything is derived from them. It does not change the audio or the spectrogram.

The listener takes rules with its `-ignore` option, any number of times:

```text
comb=<hz>[,odd][,tol=<hz>][,drift=<percent>]
line=<hz>[,tol=<hz>]
```

| Part | Meaning |
|---|---|
| `line=<hz>` | a single line at this frequency |
| `comb=<hz>` | this frequency and all its harmonics |
| `odd` | comb only: odd harmonics only |
| `tol=<hz>` | how close a peak may sit to count as a match; default 1.5 analysis bins |
| `drift=<percent>` | comb only: the base frequency may wander by this much (up to 2 %) and is fitted to the peaks of every frame |

Examples:

```text
comb=50                         mains hum at 50 Hz and all harmonics
comb=16.7,odd,tol=4,drift=0.4   odd harmonics of a 16.7 Hz supply whose frequency wanders
line=1000,tol=5                 a single test tone at 1 kHz
```

The older `-mains <hz>` option is the same as `comb=<hz>` with the default tolerance.

### Why `drift` exists

A supply frequency is not constant. A change of 0.1 % is several Hz at the 200th harmonic, more than the tolerance, so a comb with a fixed base misses most of the high harmonics. With `drift`, the listener looks in every frame for the base frequency whose harmonics meet the most peaks and uses that one.

### Limits

- With an analysis bin of about 8 Hz (a stream decoded at 32 kHz), a weak tone that sits between two strong filtered lines less than two bins away cannot be told apart from them and stays hidden.
- A rule can only remove steady lines. Pulsed interference (a repeating click) is not a spectral line and needs a different kind of rule, which does not exist.
- Changing the rules of an analysis starts a fresh motif memory for it. Motifs learned with other rules are not comparable.

### Showing the rules

An analysis page states whether filters are applied and lists them in words and as configured, so that anyone looking at the results knows what was left out. The Observatory's live pages do this already.

## In the Workbench

**Proposed:**

- **Decided** (2026-10-14): an analysis has 1 to n sources and keeps range, sensitivity, rules and memory per source, with a button that copies one source's settings to the others ([design.md](design.md#analyses-and-sources)). In the engine each source of an analysis is one analysis in the sense of this document.
- A researcher edits rules in a form: kind, frequency, odd or all harmonics, tolerance, drift. The form writes the syntax above; no expressions or code are accepted.
- The engine checks every rule before it is saved. The Workbench keeps no copy of the rule syntax of its own; see [design.md](design.md#processes).
- Saving changed rules starts a fresh motif memory (see Limits). **Decided** (2026-10-14): there is a trial mode that leaves the memory alone. How it runs is proposed in [design.md](design.md#questions-and-answers), question 16.
- **Decided:** a shared library of filter rule sets for all members. Taking a set from it copies it into one's own analysis; see [members-and-streams.md](members-and-streams.md#sharing), where what is built is described.
- **Decided:** every change of an analysis's settings and filters is recorded in the audit trail.
- Rules are stored per analysis, with revisions. A result can be traced to the revision it was made with.
- A cap on the number of rules per analysis keeps the cost bounded.

## Motifs

- **Learned motifs** are what the listener's memory finds by itself: a steady partial or an overall sound that keeps returning. Each analysis has its own memory.
- **Defined motifs** are named phenomena found by detectors with parameters. They are drafted in the Observatory's `docs/profiles-and-community-spec.md` (Part B) and are not built yet. In the Workbench a researcher chooses detectors and sets their parameters within fixed ranges; they cannot add detector code.

Tuning a definition on a live stream is slow, because the phenomenon may not occur for hours. The draft therefore foresees short private captures of one's own stream to try a definition against. Audio is allowed (decided 2026-10-14); captures count against the member's disk quota (proposed), see [design.md](design.md#questions-and-answers), question 10.
