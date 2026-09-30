package detect

import (
	"sort"
	"time"
)

// TransientConfig sets the adaptive onset threshold. Flux is the mean
// positive band-level change between frames, in dB.
type TransientConfig struct {
	HistoryFrames int           // window for the median/MAD baseline
	K             float64       // threshold = median + K·MAD
	MinFluxDB     float64       // absolute floor for the threshold
	MinBroadband  float64       // share of bands that must rise by 3 dB
	Refractory    time.Duration // minimum gap between onsets
	MaxDuration   time.Duration
}

func DefaultTransientConfig() TransientConfig {
	return TransientConfig{
		HistoryFrames: 60,
		K:             6,
		MinFluxDB:     2.5,
		MinBroadband:  0.3,
		Refractory:    300 * time.Millisecond,
		MaxDuration:   1500 * time.Millisecond,
	}
}

// TransientObs is the per-frame input to the transient detector.
type TransientObs struct {
	Time        time.Time
	Pos         float64 // audio position in seconds
	FluxDB      float64
	Broadband   float64 // share of bands rising by ≥3 dB since last frame
	CentroidHz  float64
	BandwidthHz float64
	Novelty     float64
}

// Transient is a completed detection.
type Transient struct {
	Start       time.Time
	StartPos    float64
	Duration    time.Duration
	Strength    float64
	CentroidHz  float64
	BandwidthHz float64
	Novelty     float64
}

type TransientDetector struct {
	cfg      TransientConfig
	hist     []float64
	sorted   []float64
	active   bool
	start    time.Time
	startPos float64
	lastEnd  time.Time
	peak     TransientObs
	baseline float64
	thresh   float64
}

func NewTransientDetector(cfg TransientConfig) *TransientDetector {
	return &TransientDetector{cfg: cfg}
}

// Update feeds one frame and returns a transient when one has just ended.
func (d *TransientDetector) Update(o TransientObs) *Transient {
	med, mad := d.baselineStats()
	warm := len(d.hist) >= d.cfg.HistoryFrames/2
	if mad < 0.25 {
		mad = 0.25
	}
	thresh := med + d.cfg.K*mad
	if thresh < d.cfg.MinFluxDB {
		thresh = d.cfg.MinFluxDB
	}

	var done *Transient
	switch {
	case !d.active && warm && o.FluxDB > thresh && o.Broadband >= d.cfg.MinBroadband &&
		o.Time.Sub(d.lastEnd) >= d.cfg.Refractory:
		d.active, d.start, d.startPos, d.peak, d.baseline, d.thresh = true, o.Time, o.Pos, o, med, thresh
	case d.active:
		if o.FluxDB > d.peak.FluxDB {
			d.peak = o
		}
		release := med + 2*mad
		if o.FluxDB < release || o.Time.Sub(d.start) >= d.cfg.MaxDuration {
			d.active = false
			d.lastEnd = o.Time
			done = &Transient{
				Start:       d.start,
				StartPos:    d.startPos,
				Duration:    o.Time.Sub(d.start),
				Strength:    Clamp01((d.peak.FluxDB - d.baseline) / (4 * (d.thresh - d.baseline + 1))),
				CentroidHz:  d.peak.CentroidHz,
				BandwidthHz: d.peak.BandwidthHz,
				Novelty:     d.peak.Novelty,
			}
		}
	}

	// Onset frames are kept out of the baseline so a burst doesn't raise its
	// own threshold.
	if !d.active {
		d.hist = append(d.hist, o.FluxDB)
		if len(d.hist) > d.cfg.HistoryFrames {
			d.hist = d.hist[1:]
		}
	}
	return done
}

func (d *TransientDetector) baselineStats() (med, mad float64) {
	if len(d.hist) == 0 {
		return 0, 0
	}
	d.sorted = append(d.sorted[:0], d.hist...)
	sort.Float64s(d.sorted)
	med = d.sorted[len(d.sorted)/2]
	for i, v := range d.sorted {
		if v < med {
			d.sorted[i] = med - v
		} else {
			d.sorted[i] = v - med
		}
	}
	sort.Float64s(d.sorted)
	return med, d.sorted[len(d.sorted)/2]
}
