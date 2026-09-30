# Sonic Division — Live Visualization Event & Data Model

## 1. Purpose

This document defines the event and data model for a live Sonic Division visualization system.

The intended architecture is:

```text
Sonic Division live audio stream
        │
        ▼
Audio ingestion / decoder
        │
        ▼
DSP + feature analysis daemon
        │
        ├── immediate signal features
        ├── acoustic events
        ├── harmonic structures
        ├── novelty detection
        └── motif / memory analysis
        │
        ▼
Realtime event stream
(WebSocket)
        │
        ├──────────────► p5.js live visualization
        │
        └──────────────► LLM interpretation layer
```

The core design principle is:

> The DSP system observes.  
> The memory layer recognizes.  
> The visualization embodies.  
> The LLM gives language to what was measured.

The browser should therefore receive structured observations rather than raw FFT data alone.

---

# 2. Time Layers

The system should work on several timescales simultaneously.

| Layer | Typical interval | Purpose |
|---|---:|---|
| Frame | 16–33 ms | Smooth p5.js animation |
| Signal | 50–250 ms | Spectrum, energy, peaks |
| Feature | 0.5–2 s | Harmonicity, entropy, centroid |
| Event | 1–10 s | Transients, resonance events |
| Motif | 10 s–minutes | Repetition / recurrence detection |
| State | 1–15 min | Acoustic state classification |
| Memory | hours–days | Long-term recurrence |
| Narrative | 15 s–minutes | LLM interpretation |

The frontend should interpolate between updates so visual motion remains smooth even if analytical values arrive less frequently.

---

# 3. Transport

## Recommended protocol

Use a persistent WebSocket connection:

```text
GET /ws/live
```

Reasons:

- low latency
- server → browser push
- easy reconnect handling
- one stream can contain multiple event types
- future browser → server control messages remain possible

A fallback SSE endpoint could optionally be exposed:

```text
GET /events/live
```

---

# 4. Common Envelope

Every realtime message should use the same outer structure.

```json
{
  "type": "signal.frame",
  "version": 1,
  "timestamp": "2026-09-29T21:42:15.328Z",
  "sequence": 981223,
  "source": "sonic-division",
  "payload": {}
}
```

## Fields

| Field | Type | Description |
|---|---|---|
| `type` | string | Event type |
| `version` | integer | Schema version |
| `timestamp` | ISO-8601 | Observation time |
| `sequence` | uint64 | Monotonic sequence number |
| `source` | string | Audio source identifier |
| `payload` | object | Event-specific data |

The `sequence` field lets the frontend detect dropped or out-of-order messages.

---

# 5. Event Types

The initial system should support the following event families.

```text
signal.frame
signal.spectrum
feature.state
event.transient
event.resonance
event.anomaly
motif.detected
motif.returned
memory.summary
narrative.update
system.status
```

Not every event needs to be emitted at the same rate.

---

# 6. `signal.frame`

Fast-changing information representing the immediate acoustic state.

Recommended frequency:

```text
4–20 updates / second
```

Example:

```json
{
  "type": "signal.frame",
  "version": 1,
  "timestamp": "2026-09-29T21:42:15.328Z",
  "sequence": 981223,
  "source": "sonic-division",
  "payload": {
    "rms": 0.38,
    "peak": 0.74,
    "zero_crossing_rate": 0.031,
    "spectral_centroid_hz": 1240.4,
    "spectral_flux": 0.12,
    "spectral_entropy": 0.42,
    "harmonicity": 0.76
  }
}
```

## p5.js use

Possible mappings:

```text
RMS                 → overall movement / intensity
peak                → short brightness pulses
spectral centroid   → vertical center / visual altitude
spectral flux       → movement speed
entropy             → visual disorder
harmonicity         → geometric regularity
```

These values should usually be smoothed in the browser.

---

# 7. `signal.spectrum`

Contains a compact frequency representation.

Do not necessarily send a full FFT array.

A logarithmically grouped spectrum is usually sufficient.

Example:

```json
{
  "type": "signal.spectrum",
  "version": 1,
  "timestamp": "2026-09-29T21:42:15.400Z",
  "sequence": 981224,
  "source": "sonic-division",
  "payload": {
    "min_hz": 20,
    "max_hz": 12000,
    "scale": "log",
    "bands": [
      0.03,
      0.05,
      0.08,
      0.12,
      0.23,
      0.48,
      0.72,
      0.51,
      0.28,
      0.17
    ],
    "peaks": [
      {
        "hz": 81.3,
        "amplitude": 0.92
      },
      {
        "hz": 162.5,
        "amplitude": 0.73
      },
      {
        "hz": 243.7,
        "amplitude": 0.41
      }
    ]
  }
}
```

## p5.js use

```text
bands       → terrain / wave field
peaks       → resonance towers / luminous anchors
frequency   → X position or spatial identity
amplitude   → size / height / brightness
```

The spectrum should become a visual field rather than merely a bar chart.

---

# 8. `feature.state`

A slower aggregate of acoustic characteristics.

Recommended frequency:

```text
0.5–2 Hz
```

Example:

```json
{
  "type": "feature.state",
  "version": 1,
  "timestamp": "2026-09-29T21:42:16Z",
  "sequence": 981230,
  "source": "sonic-division",
  "payload": {
    "dominant_frequency_hz": 81.3,
    "spectral_centroid_hz": 1240,
    "bandwidth_hz": 2180,
    "rolloff_hz": 4810,
    "entropy": 0.42,
    "flux": 0.12,
    "harmonicity": 0.76,
    "energy": 0.38,
    "novelty": 0.18,
    "state": "stable_resonance"
  }
}
```

Possible states:

```text
quiet
broadband_noise
wind_like
stable_resonance
harmonic_activity
transient_activity
evolving_texture
unknown
```

These labels should be descriptive outputs of measurable signal properties, not LLM guesses.

---

# 9. `event.transient`

Generated when a short-lived broadband or high-energy acoustic event occurs.

Example:

```json
{
  "type": "event.transient",
  "version": 1,
  "timestamp": "2026-09-29T21:42:37.102Z",
  "sequence": 981412,
  "source": "sonic-division",
  "payload": {
    "strength": 0.87,
    "duration_ms": 184,
    "centroid_hz": 2830,
    "bandwidth_hz": 6040,
    "novelty": 0.81
  }
}
```

## p5.js use

A transient should be an event, not just another parameter.

Possible visual reactions:

```text
shockwave
particle burst
field distortion
temporary flash
ripple through terrain
```

These effects can persist longer than the acoustic event itself.

---

# 10. `event.resonance`

Represents a persistent harmonic structure.

Example:

```json
{
  "type": "event.resonance",
  "version": 1,
  "timestamp": "2026-09-29T21:43:10Z",
  "sequence": 981610,
  "source": "sonic-division",
  "payload": {
    "id": "res-00832",
    "fundamental_hz": 81.2,
    "strength": 0.91,
    "duration_s": 42.8,
    "harmonics": [
      {
        "order": 2,
        "hz": 162.4,
        "strength": 0.72
      },
      {
        "order": 3,
        "hz": 243.8,
        "strength": 0.46
      }
    ]
  }
}
```

## p5.js use

A resonance should create a persistent visual entity.

Example conceptual mapping:

```text
fundamental frequency → identity / location
strength              → scale
duration              → persistence
number of harmonics   → geometric complexity
harmonic ratios       → symmetry
```

This is one of the most important event classes for the artistic layer.

---

# 11. `event.anomaly`

Generated when the current sound differs significantly from recent history.

Example:

```json
{
  "type": "event.anomaly",
  "version": 1,
  "timestamp": "2026-09-29T21:48:03Z",
  "sequence": 982301,
  "source": "sonic-division",
  "payload": {
    "score": 0.91,
    "reference_window_s": 1800,
    "reason": [
      "unusual_frequency_distribution",
      "high_spectral_flux"
    ]
  }
}
```

The anomaly detector should be numerical and deterministic where possible.

An anomaly does not mean that something meaningful happened.

It means:

> This sound differs significantly from what the system has recently observed.

---

# 12. Motifs

A motif is a recurring acoustic structure.

It should have a persistent identity.

Example motif database object:

```json
{
  "id": "motif-037",
  "created_at": "2026-09-28T11:14:00Z",
  "occurrences": 14,
  "prototype": {
    "fundamental_hz": 81.4,
    "harmonicity": 0.79,
    "entropy": 0.31
  }
}
```

---

# 13. `motif.detected`

Emitted when a previously unknown recurring pattern becomes sufficiently established.

```json
{
  "type": "motif.detected",
  "version": 1,
  "timestamp": "2026-09-29T21:52:00Z",
  "sequence": 982912,
  "source": "sonic-division",
  "payload": {
    "motif_id": "motif-037",
    "confidence": 0.87,
    "signature": {
      "fundamental_hz": 81.4,
      "harmonicity": 0.79,
      "entropy": 0.31
    }
  }
}
```

This event can cause the visualization to create a new persistent visual identity.

---

# 14. `motif.returned`

Emitted when an existing motif is recognized again.

```json
{
  "type": "motif.returned",
  "version": 1,
  "timestamp": "2026-09-29T22:03:12Z",
  "sequence": 983771,
  "source": "sonic-division",
  "payload": {
    "motif_id": "motif-037",
    "similarity": 0.91,
    "last_seen_s": 11042,
    "occurrence": 15,
    "current_strength": 0.74
  }
}
```

## p5.js use

This is where visual memory becomes powerful.

The browser should retrieve the motif's stored visual identity:

```json
{
  "motif_id": "motif-037",
  "visual_seed": 781224,
  "form": "orbital",
  "symmetry": 6,
  "motion_profile": "slow_rotation"
}
```

Thus the same sound can return as the same visual entity.

---

# 15. Visual Identity Persistence

The server should not dictate every pixel.

Instead, it should provide stable seeds and semantic properties.

Example:

```json
{
  "motif_id": "motif-037",
  "visual_seed": 781224,
  "frequency_anchor": 81.4,
  "complexity": 0.63,
  "symmetry": 0.81,
  "stability": 0.92
}
```

p5.js can deterministically derive:

- shape
- particle behaviour
- orbit structure
- geometry
- movement pattern

from this identity.

This keeps rendering generative while maintaining recognisable continuity.

---

# 16. `memory.summary`

Longer-term observations emitted at a slow rate.

Example:

```json
{
  "type": "memory.summary",
  "version": 1,
  "timestamp": "2026-09-29T23:00:00Z",
  "sequence": 984911,
  "source": "sonic-division",
  "payload": {
    "window": "1h",
    "dominant_motifs": [
      "motif-037",
      "motif-012"
    ],
    "event_count": 42,
    "anomaly_count": 3,
    "mean_entropy": 0.48,
    "mean_harmonicity": 0.61,
    "dominant_frequency_range_hz": [
      72,
      96
    ]
  }
}
```

This data can influence the overall visual atmosphere without changing immediate signal behaviour.

---

# 17. `narrative.update`

This contains text generated from structured observations.

The LLM should never receive unrestricted authority to describe the sound.

Its source should be machine observations such as:

```text
- resonance 81 Hz persisted for 4m 12s
- harmonics at 162 and 243 Hz
- entropy decreased from 0.58 to 0.41
- broadband activity increased during last 90 seconds
- motif-037 returned after 3h 04m
```

Example event:

```json
{
  "type": "narrative.update",
  "version": 1,
  "timestamp": "2026-09-29T22:10:00Z",
  "sequence": 984201,
  "source": "sonic-division",
  "payload": {
    "mode": "observational",
    "text": "A low resonance has returned after three hours of absence. Its harmonic structure remains unusually stable while broadband activity gradually increases around it.",
    "evidence": [
      "motif-037",
      "res-00832"
    ]
  }
}
```

Possible narrative modes:

```text
observational
technical
minimal
poetic
```

The default public-facing mode should probably remain observational or minimal.

---

# 18. `system.status`

Used for health and connectivity information.

```json
{
  "type": "system.status",
  "version": 1,
  "timestamp": "2026-09-29T22:10:03Z",
  "sequence": 984209,
  "source": "sonic-division",
  "payload": {
    "stream": "connected",
    "analysis": "running",
    "latency_ms": 620,
    "listeners": 4
  }
}
```

Possible stream states:

```text
connecting
connected
buffering
reconnecting
offline
```

---

# 19. Frontend State Model

The browser should maintain a local state object.

Conceptually:

```javascript
const state = {
  signal: {},
  spectrum: {},
  features: {},

  activeResonances: [],
  recentTransients: [],
  activeMotifs: [],
  anomalies: [],

  narrative: null,
  memory: {},

  system: {
    connected: false
  }
};
```

p5.js should read from this state every frame.

The WebSocket handlers update state.

The `draw()` loop renders it.

This separation prevents network activity from directly controlling rendering.

---

# 20. Smoothing

Incoming values should not directly replace visual properties.

Instead:

```javascript
visualEnergy = lerp(
  visualEnergy,
  state.features.energy,
  0.08
);
```

The same principle applies to:

```text
energy
entropy
centroid
flux
harmonicity
frequency peaks
terrain height
particle velocity
```

This produces organic motion and prevents jitter.

---

# 21. Visual Layer Model

The visualization can be divided into four concurrent layers.

## Layer A — Field

Always present.

Driven by:

```text
spectrum
energy
spectral centroid
entropy
```

Possible representation:

```text
terrain
fluid mesh
particle field
wave surface
```

---

## Layer B — Resonances

Persistent structures rising out of the field.

Driven by:

```text
dominant frequency
harmonics
duration
strength
```

Possible representation:

```text
spires
rings
orbital geometry
standing waves
```

---

## Layer C — Events

Temporary disturbances.

Driven by:

```text
transients
anomalies
sudden flux
novelty
```

Possible representation:

```text
shockwaves
bursts
distortions
fractures
ripples
```

---

## Layer D — Memory

Objects that carry history.

Driven by:

```text
motifs
recurrence
age
similarity
occurrence count
```

Possible representation:

```text
returning entities
constellations
persistent symbols
ghost traces
```

---

# 22. Example Visual Mapping Table

| Acoustic property | Visual interpretation |
|---|---|
| Frequency | Horizontal position / object identity |
| Amplitude | Height / scale / brightness |
| Energy | Global motion intensity |
| Spectral centroid | Vertical centre of gravity |
| Spectral entropy | Disorder / dispersion |
| Spectral flux | Movement velocity |
| Harmonicity | Symmetry / regularity |
| Harmonic count | Structural complexity |
| Novelty | Birth of new forms |
| Transient | Shockwave / burst |
| Persistence | Object lifetime |
| Motif recurrence | Reappearance of known form |
| Similarity | Fidelity of returning form |
| Motif age | Visual patina / maturity |
| Anomaly score | Degree of environmental distortion |

---

# 23. Suggested Update Rates

A practical first implementation:

| Event | Rate |
|---|---:|
| `signal.frame` | 10 Hz |
| `signal.spectrum` | 10 Hz |
| `feature.state` | 2 Hz |
| `event.transient` | event-driven |
| `event.resonance` | event-driven / update every 1 s |
| `event.anomaly` | event-driven |
| `motif.detected` | event-driven |
| `motif.returned` | event-driven |
| `memory.summary` | every 5 min |
| `narrative.update` | every 30–120 s |
| `system.status` | every 5–10 s |

The p5.js renderer itself can continue at:

```text
30–60 FPS
```

---

# 24. Example WebSocket Session

A browser might receive:

```text
system.status
signal.frame
signal.spectrum
signal.frame
signal.spectrum
feature.state
signal.frame
signal.spectrum
event.resonance
signal.frame
signal.spectrum
...
event.transient
...
motif.returned
...
narrative.update
```

The messages do not need to arrive in lockstep.

Each represents a different temporal layer.

---

# 25. Reconnection Behaviour

The frontend should gracefully survive dropped connections.

On reconnect:

```text
1. reopen WebSocket
2. request current snapshot
3. rebuild active resonance state
4. rebuild active motifs
5. resume realtime events
```

Suggested endpoint:

```text
GET /api/state/current
```

Example response:

```json
{
  "features": {},
  "spectrum": {},
  "active_resonances": [],
  "active_motifs": [],
  "narrative": {},
  "system": {}
}
```

---

# 26. Historical API

The realtime WebSocket should remain lightweight.

Historical data should use normal HTTP endpoints.

Examples:

```text
GET /api/history/events?from=...&to=...
GET /api/history/features?window=1h
GET /api/motifs
GET /api/motifs/{id}
GET /api/resonances
GET /api/narratives
```

This allows the webpage to provide an archive/timeline without bloating the live stream.

---

# 27. Minimal MVP

The first version does not need motifs, ML or an LLM.

A strong MVP would support:

```text
signal.frame
signal.spectrum
feature.state
event.transient
event.resonance
system.status
```

Frontend:

```text
p5.js
  │
  ├── live frequency landscape
  ├── resonance structures
  ├── transient effects
  ├── small analytical overlay
  └── connection status
```

This already produces a genuinely live artwork.

---

# 28. Phase 2

Add memory:

```text
motif.detected
motif.returned
memory.summary
```

The visualization can then remember and recognise previously observed acoustic structures.

This is the point where the artwork moves from:

> reactive visualization

to:

> a system with acoustic memory.

---

# 29. Phase 3

Add interpretation:

```text
narrative.update
```

The LLM receives structured observations and produces short descriptions.

For example:

```text
A stable resonance at approximately 81 Hz has persisted for
four minutes. Two harmonics remain visible while broadband
activity has slowly increased.
```

Or, in a more artistic mode:

```text
The low resonance has returned.

Its second and third harmonics hold above it while the surrounding
field grows less ordered. The structure persists.
```

The observations remain rooted in measured signal data.

---

# 30. Recommended Implementation Boundary

A useful rule is:

```text
Go decides what happened.
p5.js decides what it looks like.
The LLM decides how to say it.
```

More precisely:

### Go

Responsible for:

- stream ingestion
- signal processing
- FFT/STFT
- feature extraction
- event detection
- harmonic analysis
- motif detection
- historical memory
- WebSocket API

### p5.js

Responsible for:

- interpolation
- animation
- particles
- geometry
- terrain
- resonance entities
- motif identities
- event effects
- interaction

### LLM

Responsible for:

- summarisation
- semantic description
- optional poetic interpretation

It should not be responsible for deciding whether an acoustic event occurred.

---

# 31. Proposed First WebSocket Schema

For the first prototype, only four messages are strictly necessary.

## `signal`

```json
{
  "type": "signal",
  "timestamp": 0,
  "energy": 0.38,
  "centroid": 1240,
  "entropy": 0.42,
  "flux": 0.12,
  "harmonicity": 0.76
}
```

## `spectrum`

```json
{
  "type": "spectrum",
  "timestamp": 0,
  "bands": [],
  "peaks": []
}
```

## `resonance`

```json
{
  "type": "resonance",
  "timestamp": 0,
  "fundamental": 81.3,
  "strength": 0.91,
  "harmonics": []
}
```

## `transient`

```json
{
  "type": "transient",
  "timestamp": 0,
  "strength": 0.87
}
```

That is enough to build the first visually compelling p5.js prototype.

---

# 32. Conceptual Goal

The objective should not be to build a conventional music visualizer.

The system should gradually become:

```text
an observer
     ↓
a memory
     ↓
a visual ecology
     ↓
a narrative
```

Immediate sound produces motion.

Persistent sound produces structure.

Repeated sound produces identity.

History produces memory.

And the accumulated memory becomes part of the artwork itself.
