// Package detect turns per-frame measurements into slower observations:
// adaptive normalisation, transients, persistent resonances and the
// descriptive acoustic state. Nothing here guesses; every label follows from
// thresholds on measured values.
package detect

import "math"

// AutoRange tracks the recent level range of a dB signal so values can be
// normalised to 0..1 regardless of how loud the source is. The bounds follow
// new extremes quickly and relax towards the signal slowly.
type AutoRange struct {
	Lo, Hi  float64
	MinSpan float64
	attack  float64
	release float64
	init    bool
}

// NewAutoRange returns a range that relaxes with the given time constant,
// expressed in update calls.
func NewAutoRange(minSpan, releaseUpdates float64) *AutoRange {
	return &AutoRange{MinSpan: minSpan, attack: 0.2, release: 1 / releaseUpdates}
}

// Update feeds the current low and high observations (for a single value,
// pass it twice).
func (r *AutoRange) Update(lo, hi float64) {
	if !r.init {
		r.Lo, r.Hi, r.init = lo-6, hi+6, true
	}
	if lo < r.Lo {
		r.Lo += (lo - r.Lo) * r.attack
	} else {
		r.Lo += (lo - r.Lo) * r.release
	}
	if hi > r.Hi {
		r.Hi += (hi - r.Hi) * r.attack
	} else {
		r.Hi += (hi - r.Hi) * r.release
	}
	if span := r.Hi - r.Lo; span < r.MinSpan {
		mid := (r.Hi + r.Lo) / 2
		r.Lo, r.Hi = mid-r.MinSpan/2, mid+r.MinSpan/2
	}
}

func (r *AutoRange) Norm(x float64) float64 {
	return Clamp01((x - r.Lo) / (r.Hi - r.Lo))
}

func Clamp01(x float64) float64 {
	if math.IsNaN(x) || x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// EMA is an exponential moving average with a time constant in updates.
type EMA struct {
	V     float64
	alpha float64
	init  bool
}

func NewEMA(updates float64) *EMA { return &EMA{alpha: 1 / updates} }

func (e *EMA) Update(x float64) float64 {
	if !e.init {
		e.V, e.init = x, true
	} else {
		e.V += (x - e.V) * e.alpha
	}
	return e.V
}

// Novelty compares a short-term band profile with a long-term one. Both are
// normalised 0..1 band vectors; the score is their mean absolute difference,
// scaled so that a clearly different spectrum reads near 1.
type Novelty struct {
	short, long []float64
	aShort      float64
	aLong       float64
	init        bool
}

func NewNovelty(bands int, shortUpdates, longUpdates float64) *Novelty {
	return &Novelty{
		short: make([]float64, bands), long: make([]float64, bands),
		aShort: 1 / shortUpdates, aLong: 1 / longUpdates,
	}
}

func (n *Novelty) Update(bands []float64) float64 {
	if !n.init {
		copy(n.short, bands)
		copy(n.long, bands)
		n.init = true
		return 0
	}
	var diff float64
	for i, b := range bands {
		n.short[i] += (b - n.short[i]) * n.aShort
		n.long[i] += (b - n.long[i]) * n.aLong
		diff += math.Abs(n.short[i] - n.long[i])
	}
	return Clamp01(diff / float64(len(bands)) / 0.2)
}
