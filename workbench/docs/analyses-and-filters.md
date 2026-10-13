# Analyses and filters

Status: draft (2026-10-13). The filter rules described here exist in the listener. Running several analyses on one stream does not.

## Terms

| Term | Meaning |
|---|---|
| Stream | A live audio source at a URL, run by its operator. |
| Decode | The listener's connection to a stream and the audio samples it produces. |
| Analysis | One run of the listener's signal processing over the decoded audio, with its own settings, filter rules, motif definitions and motif memory. |
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

Today one listener process runs exactly one analysis. This is the first thing the engine has to learn before the Workbench can do research on shared streams.

## The rule between the two projects

**The Observatory's filter rules never apply to the Workbench.**

A rule belongs to an analysis. The Observatory's rules express what its maintainer considers interference on a public page. A researcher may study exactly that signal, so a Workbench analysis starts with no rules and gets only the ones its owner defines.

The Workbench therefore does not use the Observatory's event stream or MCP server as research data. In the Observatory's analysis:

| Data | Filtered by its rules? |
|---|---|
| Audio as relayed to the browser | no |
| Spectrum band levels (the spectrogram) | no |
| Frame levels: level, energy, flux, centroid, entropy | no |
| Spectral peaks | yes |
| Dominant frequency | yes |
| Resonance events | yes |
| Learned motifs and the motif memory | yes |
| Interpretation texts | yes, since they describe the events above |

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

- With an analysis bin of about 8 Hz, a weak tone that sits between two strong filtered lines less than two bins away cannot be told apart from them and stays hidden.
- A rule can only remove steady lines. Pulsed interference (a repeating click) is not a spectral line and needs a different kind of rule, which does not exist.
- Changing the rules of an analysis starts a fresh motif memory for it. Motifs learned with other rules are not comparable.

### Showing the rules

An analysis page states whether filters are applied and lists them in words and as configured, so that anyone looking at the results knows what was left out. The Observatory's live pages do this already.

## In the Workbench

**Proposed:**

- A researcher edits rules in a form: kind, frequency, odd or all harmonics, tolerance, drift. The form writes the syntax above; no expressions or code are accepted.
- Rules are stored per analysis, with revisions. A result can be traced to the revision it was made with.
- A cap on the number of rules per analysis keeps the cost bounded.

## Motifs

- **Learned motifs** are what the listener's memory finds by itself: a steady partial or an overall sound that keeps returning. Each analysis has its own memory.
- **Defined motifs** are named phenomena found by detectors with parameters. They are drafted in the Observatory's `docs/profiles-and-community-spec.md` (Part B) and are not built yet. In the Workbench a researcher chooses detectors and sets their parameters within fixed ranges; they cannot add detector code.

Tuning a definition on a live stream is slow, because the phenomenon may not occur for hours. The draft therefore foresees short private captures of one's own stream to try a definition against. Whether captures are allowed at all is an open question in [design.md](design.md).
