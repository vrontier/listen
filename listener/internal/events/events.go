// Package events defines the realtime message envelope and payloads
// (docs/live_visualization_event_model.md §4–§18).
package events

import (
	"encoding/json"
	"math"
	"sync/atomic"
	"time"
)

const Version = 1

// Event types emitted by the MVP (§27).
const (
	TypeFrame     = "signal.frame"
	TypeSpectrum  = "signal.spectrum"
	TypeFeature   = "feature.state"
	TypeTransient = "event.transient"
	TypeResonance = "event.resonance"
	TypeStatus    = "system.status"

	// Phase 2: memory (§12–§16).
	TypeMotifDetected = "motif.detected"
	TypeMotifReturned = "motif.returned"
	TypeMemorySummary = "memory.summary"
)

// Envelope is the common outer structure of every realtime message (§4).
type Envelope struct {
	Type      string    `json:"type"`
	Version   int       `json:"version"`
	Timestamp time.Time `json:"timestamp"`
	Sequence  uint64    `json:"sequence"`
	Source    string    `json:"source"`
	// Position is where the observation lies in the analysed audio, in
	// seconds since the listener started. It never wraps or resets, so the
	// browser can line events up with relayed audio. Omitted for events
	// not tied to a moment in the audio (system.status).
	Position float64 `json:"position_s,omitempty"`
	Payload  any     `json:"payload"`
}

// Message is an encoded envelope ready to fan out to clients.
type Message struct {
	Type string
	Data []byte
	Env  Envelope
}

// Stamper assigns monotonic sequence numbers and encodes envelopes.
type Stamper struct {
	source string
	seq    atomic.Uint64
}

func NewStamper(source string) *Stamper { return &Stamper{source: source} }

// Stamp wraps payload in an envelope. pos is the audio position in seconds
// (0 for events without one).
func (s *Stamper) Stamp(typ string, ts time.Time, pos float64, payload any) Message {
	env := Envelope{
		Type:      typ,
		Version:   Version,
		Timestamp: ts.UTC(),
		Sequence:  s.seq.Add(1),
		Source:    s.source,
		Position:  math.Round(pos*1000) / 1000,
		Payload:   payload,
	}
	return encode(env)
}

// WithPayload returns m with a replaced payload, keeping its sequence and
// timestamp (used to annotate an event before it is sent).
func (m Message) WithPayload(payload any) Message {
	env := m.Env
	env.Payload = payload
	return encode(env)
}

func encode(env Envelope) Message {
	data, err := json.Marshal(env)
	if err != nil {
		// Payloads are plain structs of numbers and strings; this cannot fail
		// unless a NaN slips through, which the analysis clamps.
		panic("events: marshal " + env.Type + ": " + err.Error())
	}
	return Message{Type: env.Type, Data: data, Env: env}
}

// Frame is the signal.frame payload (§6).
type Frame struct {
	RMS              float64 `json:"rms"`
	LevelDB          float64 `json:"level_db"`
	Energy           float64 `json:"energy"`
	Peak             float64 `json:"peak"`
	ZeroCrossingRate float64 `json:"zero_crossing_rate"`
	CentroidHz       float64 `json:"spectral_centroid_hz"`
	Flux             float64 `json:"spectral_flux"`
	Entropy          float64 `json:"spectral_entropy"`
	Harmonicity      float64 `json:"harmonicity"`
}

// Peak is a spectral peak.
type Peak struct {
	Hz        float64 `json:"hz"`
	Amplitude float64 `json:"amplitude"`
}

// Spectrum is the signal.spectrum payload (§7). Bands are log-spaced and
// normalised to 0..1 against an adaptive level range.
type Spectrum struct {
	MinHz float64   `json:"min_hz"`
	MaxHz float64   `json:"max_hz"`
	Scale string    `json:"scale"`
	Bands []float64 `json:"bands"`
	Peaks []Peak    `json:"peaks"`
}

// Feature is the feature.state payload (§8).
type Feature struct {
	DominantHz  float64 `json:"dominant_frequency_hz"`
	CentroidHz  float64 `json:"spectral_centroid_hz"`
	BandwidthHz float64 `json:"bandwidth_hz"`
	RolloffHz   float64 `json:"rolloff_hz"`
	Entropy     float64 `json:"entropy"`
	Flux        float64 `json:"flux"`
	Harmonicity float64 `json:"harmonicity"`
	Energy      float64 `json:"energy"`
	Novelty     float64 `json:"novelty"`
	State       string  `json:"state"`
}

// Transient is the event.transient payload (§9).
type Transient struct {
	Strength    float64 `json:"strength"`
	DurationMs  float64 `json:"duration_ms"`
	CentroidHz  float64 `json:"centroid_hz"`
	BandwidthHz float64 `json:"bandwidth_hz"`
	Novelty     float64 `json:"novelty"`
}

// Harmonic is one overtone of a resonance.
type Harmonic struct {
	Order    int     `json:"order"`
	Hz       float64 `json:"hz"`
	Strength float64 `json:"strength"`
}

// Resonance is the event.resonance payload (§10). Status is "start" when the
// structure is first recognised, "update" about once a second while it
// persists and "end" when it decays.
type Resonance struct {
	// MotifID links the resonance to a known motif once the memory has
	// established one it matches (phase 2).
	MotifID string `json:"motif_id,omitempty"`

	ID            string     `json:"id"`
	Status        string     `json:"status"`
	FundamentalHz float64    `json:"fundamental_hz"`
	Strength      float64    `json:"strength"`
	DurationS     float64    `json:"duration_s"`
	Harmonics     []Harmonic `json:"harmonics"`
}

// Status is the system.status payload (§18).
type Status struct {
	Stream     string  `json:"stream"`
	Analysis   string  `json:"analysis"`
	LatencyMs  float64 `json:"latency_ms"`
	Listeners  int     `json:"listeners"`
	Input      string  `json:"input"`
	SampleRate int     `json:"sample_rate"`
	UptimeS    float64 `json:"uptime_s"`
	Reconnects int     `json:"reconnects"`
	// Audio lists the codecs relayed on /ws/audio; empty when the relay
	// is off.
	Audio []string `json:"audio,omitempty"`
}

// Visual is the stable identity the browser derives a motif's form from
// (§15). The seed never changes; the other values follow the prototype.
type Visual struct {
	Seed            uint32  `json:"visual_seed"`
	FrequencyAnchor float64 `json:"frequency_anchor"`
	Complexity      float64 `json:"complexity"`
	Symmetry        float64 `json:"symmetry"`
	Stability       float64 `json:"stability"`
}

// Signature summarises a motif's prototype.
type Signature struct {
	FundamentalHz float64 `json:"fundamental_hz,omitempty"` // resonance motifs
	CentroidHz    float64 `json:"centroid_hz,omitempty"`    // texture motifs
	Harmonicity   float64 `json:"harmonicity"`
	Entropy       float64 `json:"entropy"`
}

// MotifDetected is the motif.detected payload (§13).
type MotifDetected struct {
	MotifID     string    `json:"motif_id"`
	Kind        string    `json:"kind"` // resonance | texture
	Confidence  float64   `json:"confidence"`
	Occurrences int       `json:"occurrences"`
	Signature   Signature `json:"signature"`
	Visual      Visual    `json:"visual"`
}

// MotifReturned is the motif.returned payload (§14).
type MotifReturned struct {
	MotifID         string  `json:"motif_id"`
	Kind            string  `json:"kind"`
	Similarity      float64 `json:"similarity"`
	LastSeenS       float64 `json:"last_seen_s"`
	Occurrence      int     `json:"occurrence"`
	CurrentStrength float64 `json:"current_strength"`
	Visual          Visual  `json:"visual"`
}

// MemorySummary is the memory.summary payload (§16).
type MemorySummary struct {
	Window                   string     `json:"window"` // 1h | 24h
	DominantMotifs           []string   `json:"dominant_motifs"`
	KnownMotifs              int        `json:"known_motifs"`
	EventCount               int        `json:"event_count"`
	AnomalyCount             int        `json:"anomaly_count"`
	MeanEntropy              float64    `json:"mean_entropy"`
	MeanHarmonicity          float64    `json:"mean_harmonicity"`
	DominantFrequencyRangeHz [2]float64 `json:"dominant_frequency_range_hz"`
}
