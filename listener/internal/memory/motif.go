// Package memory recognises recurring acoustic structures and gives them a
// persistent identity (event model §12–§16, phase 2). It sits between the
// analysis and the hub: it sees every event, links resonances to motifs,
// and emits motif.detected, motif.returned and memory.summary.
//
// Two kinds of motif are kept:
//
//   - resonance motifs: recurring persistent partials, matched on the
//     fundamental, the harmonic profile and the context they sound in
//   - texture motifs: recurring overall sound states, matched on a coarse
//     spectral profile and summary features over 20-second windows
package memory

import (
	"math"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

const (
	KindResonance = "resonance"
	KindTexture   = "texture"

	profileOrders = 7  // harmonic orders 2..8
	textureBands  = 16 // coarse spectral profile of a texture
	recentKept    = 20 // occurrences remembered per motif
)

// Prototype is the running average of what a motif sounds like.
type Prototype struct {
	FundamentalHz float64                `json:"fundamental_hz,omitempty"`
	Profile       [profileOrders]float64 `json:"profile,omitempty"` // relative strength of orders 2..8
	Harmonicity   float64                `json:"harmonicity"`
	Entropy       float64                `json:"entropy"`
	CentroidHz    float64                `json:"centroid_hz,omitempty"`
	Flux          float64                `json:"flux,omitempty"`
	Bands         [textureBands]float64  `json:"bands,omitempty"`
}

// Occurrence is one appearance of a motif.
type Occurrence struct {
	Start      time.Time `json:"start"`
	DurationS  float64   `json:"duration_s"`
	Similarity float64   `json:"similarity"`
	Strength   float64   `json:"strength,omitempty"`
}

// Motif is persisted in motifs.json. ID is empty while the motif is only a
// candidate; it gets a public number once it is established.
type Motif struct {
	Key         int          `json:"key"`
	ID          string       `json:"id,omitempty"`
	Kind        string       `json:"kind"`
	Created     time.Time    `json:"created"`
	Established time.Time    `json:"established,omitempty"`
	LastSeen    time.Time    `json:"last_seen"`
	Occurrences int          `json:"occurrences"`
	PresenceS   float64      `json:"presence_s"`
	SimSum      float64      `json:"sim_sum"`
	Seed        uint32       `json:"seed"`
	Proto       Prototype    `json:"prototype"`
	Recent      []Occurrence `json:"recent,omitempty"`

	open         int     // open episodes (resonance) or 1 while current (texture)
	lastStrength float64 // strength of the latest appearance
}

func (m *Motif) established() bool { return m.ID != "" }
func (m *Motif) active() bool      { return m.open > 0 }

func (m *Motif) remember(o Occurrence) {
	m.Recent = append(m.Recent, o)
	if len(m.Recent) > recentKept {
		m.Recent = m.Recent[len(m.Recent)-recentKept:]
	}
}

// Visual derives the stable identity the browser builds a form from (§15).
func (m *Motif) Visual() events.Visual {
	v := events.Visual{Seed: m.Seed}
	span := m.LastSeen.Sub(m.Created).Seconds()
	if span > 0 {
		v.Stability = round(clamp01(m.PresenceS/span), 3)
	} else {
		v.Stability = 1
	}
	switch m.Kind {
	case KindResonance:
		v.FrequencyAnchor = round(m.Proto.FundamentalHz, 1)
		present, ladder := 0, 0
		for i, s := range m.Proto.Profile {
			if s > 0.1 {
				present++
				if i == 0 || m.Proto.Profile[i-1] > 0.1 {
					ladder++ // consecutive orders make a regular ladder
				}
			}
		}
		v.Complexity = round(float64(present)/profileOrders, 3)
		if present > 0 {
			v.Symmetry = round(float64(ladder)/float64(present), 3)
		}
	case KindTexture:
		v.FrequencyAnchor = round(m.Proto.CentroidHz, 1)
		v.Complexity = round(m.Proto.Entropy, 3)
		v.Symmetry = round(m.Proto.Harmonicity, 3)
	}
	return v
}

// Signature is the summary sent with motif events.
func (m *Motif) Signature() events.Signature {
	s := events.Signature{Harmonicity: round(m.Proto.Harmonicity, 3), Entropy: round(m.Proto.Entropy, 3)}
	if m.Kind == KindResonance {
		s.FundamentalHz = round(m.Proto.FundamentalHz, 1)
	} else {
		s.CentroidHz = round(m.Proto.CentroidHz, 1)
	}
	return s
}

// Confidence grows with occurrences and with how well they matched.
func (m *Motif) Confidence() float64 {
	if m.Occurrences == 0 {
		return 0
	}
	mean := m.SimSum / float64(m.Occurrences)
	return round(0.5*math.Min(1, float64(m.Occurrences)/6)+0.5*mean, 3)
}

// resonanceSimilarity compares a resonance (as a prototype) with a motif.
// Frequency dominates: 25 cents apart still scores ~0.6 before the other
// factors; the harmonic profile and context refine it.
func resonanceSimilarity(a, b Prototype) float64 {
	if a.FundamentalHz <= 0 || b.FundamentalHz <= 0 {
		return 0
	}
	cents := 1200 * math.Abs(math.Log2(a.FundamentalHz/b.FundamentalHz))
	fs := math.Exp(-0.5 * (cents / 25) * (cents / 25))
	var pd float64
	for i := range a.Profile {
		pd += math.Abs(a.Profile[i] - b.Profile[i])
	}
	ps := 1 - pd/profileOrders
	cs := 1 - (math.Abs(a.Harmonicity-b.Harmonicity)+math.Abs(a.Entropy-b.Entropy))/2
	return fs * (0.75 + 0.25*ps) * (0.85 + 0.15*cs)
}

// textureDistance compares two texture fingerprints; ~0.05 is "the same
// sound", ~0.15 clearly different.
func textureDistance(a, b Prototype) float64 {
	var bd float64
	for i := range a.Bands {
		bd += math.Abs(a.Bands[i] - b.Bands[i])
	}
	bd /= textureBands
	fd := (math.Abs(a.Entropy-b.Entropy) + math.Abs(a.Harmonicity-b.Harmonicity) +
		2*math.Abs(a.Flux-b.Flux) + math.Abs(logCentroid(a.CentroidHz)-logCentroid(b.CentroidHz))) / 4
	return 0.6*bd + 0.4*fd
}

func textureSimilarity(a, b Prototype, scale float64) float64 {
	d := textureDistance(a, b) / scale
	return math.Exp(-d * d)
}

// logCentroid maps 100 Hz..10 kHz to 0..1.
func logCentroid(hz float64) float64 {
	if hz <= 100 {
		return 0
	}
	return clamp01(math.Log(hz/100) / math.Log(100))
}

// blend moves the prototype towards an observation by weight w.
func (p *Prototype) blend(o Prototype, w float64, kind string) {
	lerp := func(a, b float64) float64 { return a + (b-a)*w }
	if kind == KindResonance {
		p.FundamentalHz = math.Exp(lerp(math.Log(p.FundamentalHz), math.Log(o.FundamentalHz)))
		for i := range p.Profile {
			p.Profile[i] = lerp(p.Profile[i], o.Profile[i])
		}
	} else {
		for i := range p.Bands {
			p.Bands[i] = lerp(p.Bands[i], o.Bands[i])
		}
		p.CentroidHz = lerp(p.CentroidHz, o.CentroidHz)
		p.Flux = lerp(p.Flux, o.Flux)
	}
	p.Harmonicity = lerp(p.Harmonicity, o.Harmonicity)
	p.Entropy = lerp(p.Entropy, o.Entropy)
}

func clamp01(x float64) float64 {
	if math.IsNaN(x) || x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func round(x float64, places int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}
