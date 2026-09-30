package detect

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// ResonanceConfig controls when a spectral peak counts as a persistent
// harmonic structure.
type ResonanceConfig struct {
	BinHz         float64
	MinAge        time.Duration // how long a peak must persist to become a resonance
	MinPresence   float64       // share of frames in which it was seen
	MinProminence float64       // mean prominence in dB
	CandidateTTL  time.Duration // an unconfirmed track dies after this gap
	ActiveTTL     time.Duration // a resonance ends after this gap
	UpdateEvery   time.Duration
	MaxActive     int
	HarmonicTol   float64 // relative tolerance for k·f0 matches
}

func DefaultResonanceConfig(binHz float64) ResonanceConfig {
	return ResonanceConfig{
		BinHz:         binHz,
		MinAge:        5 * time.Second,
		MinPresence:   0.6,
		MinProminence: 10,
		CandidateTTL:  time.Second,
		ActiveTTL:     2 * time.Second,
		UpdateEvery:   time.Second,
		MaxActive:     8,
		HarmonicTol:   0.025,
	}
}

// PeakObs is a spectral peak in one frame, with its amplitude normalised 0..1.
type PeakObs struct {
	Hz, Amplitude, Prominence float64
}

type HarmonicObs struct {
	Order        int
	Hz, Strength float64
}

// ResonanceEvent is emitted on start, once per UpdateEvery while active,
// and on end.
type ResonanceEvent struct {
	ID          string
	Status      string // start | update | end
	Fundamental float64
	Strength    float64
	Duration    time.Duration
	Harmonics   []HarmonicObs
}

type track struct {
	hz, amp, prom float64
	first, last   time.Time
	presence      float64 // recent share of frames in which the peak was seen
	matched       bool

	id         string // set once the track is emitted as a resonance
	lastUpdate time.Time
}

type ResonanceTracker struct {
	cfg    ResonanceConfig
	tracks []*track
	nextID int
}

func NewResonanceTracker(cfg ResonanceConfig) *ResonanceTracker {
	return &ResonanceTracker{cfg: cfg}
}

func (t *ResonanceTracker) tolerance(hz float64) float64 {
	return math.Max(t.cfg.BinHz, 0.015*hz)
}

// Update feeds one frame of peaks and returns any resonance events.
func (t *ResonanceTracker) Update(now time.Time, peaks []PeakObs) []ResonanceEvent {
	for _, tr := range t.tracks {
		tr.matched = false
	}
	// Strongest peaks claim the nearest unmatched track first.
	ps := append([]PeakObs(nil), peaks...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Amplitude > ps[j].Amplitude })
	for _, p := range ps {
		var best *track
		bestD := math.Inf(1)
		for _, tr := range t.tracks {
			if tr.matched {
				continue
			}
			if d := math.Abs(p.Hz - tr.hz); d <= t.tolerance(tr.hz) && d < bestD {
				best, bestD = tr, d
			}
		}
		if best == nil {
			t.tracks = append(t.tracks, &track{hz: p.Hz, amp: p.Amplitude, prom: p.Prominence,
				first: now, last: now, presence: 1, matched: true})
			continue
		}
		best.matched = true
		best.last = now
		best.hz += (p.Hz - best.hz) * 0.1
		best.amp += (p.Amplitude - best.amp) * 0.1
		best.prom += (p.Prominence - best.prom) * 0.1
	}

	for _, tr := range t.tracks {
		m := 0.0
		if tr.matched {
			m = 1
		}
		tr.presence += (m - tr.presence) * 0.05
	}

	var out []ResonanceEvent
	kept := t.tracks[:0]
	for _, tr := range t.tracks {
		ttl := t.cfg.CandidateTTL
		if tr.id != "" {
			ttl = t.cfg.ActiveTTL
		}
		if now.Sub(tr.last) > ttl {
			if tr.id != "" {
				out = append(out, t.event(tr, "end", nil, now))
			}
			continue
		}
		kept = append(kept, tr)
	}
	t.tracks = kept

	// Active resonances hold until they decay (hysteresis); only then does
	// the next candidate get a chance. Without this, a partial that sits
	// near the threshold, or flips between being a root and an overtone,
	// would start and end every few frames.
	for _, tr := range t.tracks {
		if tr.id != "" && !t.holds(tr) {
			out = append(out, t.event(tr, "end", nil, now))
			tr.id = ""
		}
	}

	// Candidates sorted low to high: a candidate that is an overtone of a
	// lower active or stable track is attached to it rather than started.
	// Overtones only need to be present, not old: they borrow their
	// persistence from the root.
	var pool []*track
	for _, tr := range t.tracks {
		if tr.id != "" || t.isStable(tr, now) || t.holds(tr) {
			pool = append(pool, tr)
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].hz < pool[j].hz })
	harmonics := map[*track][]HarmonicObs{}
	overtone := map[*track]bool{}
	for i, root := range pool {
		for _, h := range pool[i+1:] {
			r := h.hz / root.hz
			k := math.Round(r)
			if k >= 2 && k <= 8 && math.Abs(r-k)/k < t.cfg.HarmonicTol {
				harmonics[root] = append(harmonics[root], HarmonicObs{Order: int(k), Hz: h.hz, Strength: h.amp})
				overtone[h] = true
			}
		}
	}

	active := t.Active()
	var fresh []*track
	for _, tr := range pool {
		if tr.id == "" && !overtone[tr] && t.isStable(tr, now) {
			fresh = append(fresh, tr)
		}
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].amp > fresh[j].amp })
	for _, tr := range fresh {
		if active >= t.cfg.MaxActive {
			break
		}
		active++
		t.nextID++
		tr.id = fmt.Sprintf("res-%05d", t.nextID)
		tr.lastUpdate = now
		out = append(out, t.event(tr, "start", harmonics[tr], now))
	}
	for _, tr := range t.tracks {
		if tr.id != "" && now.Sub(tr.lastUpdate) >= t.cfg.UpdateEvery {
			tr.lastUpdate = now
			out = append(out, t.event(tr, "update", harmonics[tr], now))
		}
	}
	return out
}

func (t *ResonanceTracker) isStable(tr *track, now time.Time) bool {
	return now.Sub(tr.first) >= t.cfg.MinAge &&
		tr.presence >= t.cfg.MinPresence &&
		tr.prom >= t.cfg.MinProminence
}

// holds reports whether an active resonance is still present, with looser
// thresholds than isStable.
func (t *ResonanceTracker) holds(tr *track) bool {
	return tr.presence >= t.cfg.MinPresence/2 && tr.prom >= t.cfg.MinProminence-4
}

func (t *ResonanceTracker) event(tr *track, status string, cands []HarmonicObs, now time.Time) ResonanceEvent {
	// Keep the strongest partial per harmonic order.
	byOrder := map[int]HarmonicObs{}
	for _, c := range cands {
		if cur, ok := byOrder[c.Order]; !ok || c.Strength > cur.Strength {
			byOrder[c.Order] = c
		}
	}
	h := make([]HarmonicObs, 0, len(byOrder))
	for _, c := range byOrder {
		h = append(h, c)
	}
	sort.Slice(h, func(i, j int) bool { return h[i].Order < h[j].Order })
	return ResonanceEvent{
		ID:          tr.id,
		Status:      status,
		Fundamental: tr.hz,
		Strength:    tr.amp,
		Duration:    now.Sub(tr.first),
		Harmonics:   h,
	}
}

// Active returns the number of currently emitted resonances.
func (t *ResonanceTracker) Active() int {
	n := 0
	for _, tr := range t.tracks {
		if tr.id != "" {
			n++
		}
	}
	return n
}
