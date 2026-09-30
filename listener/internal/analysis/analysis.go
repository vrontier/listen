// Package analysis runs the per-hop DSP on incoming samples and emits the
// MVP event stream at the rates of §23: signal.frame and signal.spectrum at
// 10 Hz, feature.state at 2 Hz, transients and resonances as they occur.
package analysis

import (
	"math"
	"sort"
	"time"

	"github.com/vrontier/listen/listener/internal/detect"
	"github.com/vrontier/listen/listener/internal/dsp"
	"github.com/vrontier/listen/listener/internal/events"
)

const (
	SampleRate = 22050
	fftSize    = 4096 // ≈186 ms window, 5.4 Hz bins
	hopSize    = 1103 // ≈50 ms, 20 analysis frames/s
	bandCount  = 48
	minHz      = 20.0
	maxHz      = 11000.0
	peakMinHz  = 40.0 // below this the MP3 source is a roll-off hump, not partials
	peakMaxHz  = 6000.0
	maxPeaks   = 12

	framesPerSignal  = 2  // 10 Hz
	framesPerFeature = 10 // 2 Hz
)

// Analyzer is not safe for concurrent use; feed it from one goroutine.
type Analyzer struct {
	emit    func(events.Message)
	stamper *events.Stamper
	onFrame func(latency time.Duration)

	fft      *dsp.FFT
	window   []float64
	ring     []float64
	scratch  []float64
	mag      []float64
	magDB    []float64
	bands    *dsp.Bands
	bandDB   []float64
	prevBand []float64
	bandNorm []float64
	pending  []float64
	binHz    float64
	lo, hi   int

	levelRange *detect.AutoRange
	bandRange  *detect.AutoRange
	novelty    *detect.Novelty
	transients *detect.TransientDetector
	resonances *detect.ResonanceTracker

	// clock: observation time of the newest sample
	anchorTime   time.Time
	anchorSample int64
	wrap         time.Duration // replay of a looped file: clock repeats every wrap
	live         bool          // clock may never run ahead of wall time
	samples      int64
	frame        int64

	sig              signalAcc
	feat             featureAcc
	recentTransients []time.Time
	lastPeaks        []dsp.SpectralPeak
}

type signalAcc struct {
	n                                           int
	rms, level, peak, zcr, cent, flux, ent, har float64
	bands                                       []float64
}

type featureAcc struct {
	n                                                   int
	cent, bw, roll, ent, flux, har, energy, level, flat float64
}

func New(stamper *events.Stamper, emit func(events.Message)) *Analyzer {
	a := &Analyzer{
		emit:     emit,
		stamper:  stamper,
		fft:      dsp.NewFFT(fftSize),
		window:   dsp.Hann(fftSize),
		ring:     make([]float64, 0, fftSize),
		scratch:  make([]float64, fftSize),
		mag:      make([]float64, fftSize/2+1),
		magDB:    make([]float64, fftSize/2+1),
		bandDB:   make([]float64, bandCount),
		prevBand: make([]float64, bandCount),
		bandNorm: make([]float64, bandCount),
		binHz:    float64(SampleRate) / fftSize,

		// Level ranges relax over ~5 min so slow environmental change stays
		// visible instead of being normalised away.
		levelRange: detect.NewAutoRange(30, 20*300),
		bandRange:  detect.NewAutoRange(40, 20*300),
		novelty:    detect.NewNovelty(bandCount, 20, 20*60),
		transients: detect.NewTransientDetector(detect.DefaultTransientConfig()),
	}
	a.bands = dsp.NewBands(bandCount, minHz, maxHz, a.binHz, len(a.mag))
	a.resonances = detect.NewResonanceTracker(detect.DefaultResonanceConfig(a.binHz))
	a.lo = int(math.Ceil(minHz / a.binHz))
	a.hi = int(maxHz / a.binHz)
	a.sig.bands = make([]float64, bandCount)
	a.Anchor(time.Now())
	return a
}

// OnFrame registers a callback invoked after every analysis frame with the
// delay between the frame's observation time and now (only meaningful for
// realtime input).
func (a *Analyzer) OnFrame(f func(latency time.Duration)) { a.onFrame = f }

// Anchor pins the observation clock: the next sample is considered to have
// been heard at t. Call it on (re)connect.
func (a *Analyzer) Anchor(t time.Time) {
	a.anchorTime = t
	a.anchorSample = a.samples
}

// Replay pins the clock to the recording time of a capture: the next sample
// was heard at start. With duration > 0 the clock wraps, so a looped file
// keeps reporting times within the recording.
func (a *Analyzer) Replay(start time.Time, duration time.Duration) {
	a.Anchor(start)
	a.wrap = duration
}

// Live marks realtime input. Icecast servers send a burst of buffered audio
// on connect; counting those samples forward from the connect time would
// put observations seconds in the future, so the clock is held back to wall
// time instead (the burst was heard before we connected).
func (a *Analyzer) Live(on bool) { a.live = on }

func (a *Analyzer) now() time.Time {
	d := time.Duration(float64(a.samples-a.anchorSample) / SampleRate * float64(time.Second))
	if a.wrap > 0 {
		d %= a.wrap
	}
	t := a.anchorTime.Add(d)
	if a.live {
		if wall := time.Now(); t.After(wall) {
			a.anchorTime = a.anchorTime.Add(wall.Sub(t))
			t = wall
		}
	}
	return t
}

// Feed consumes mono float samples at SampleRate.
func (a *Analyzer) Feed(x []float32) {
	for _, v := range x {
		a.pending = append(a.pending, float64(v))
	}
	for len(a.pending) >= hopSize {
		a.hop(a.pending[:hopSize])
		a.pending = a.pending[hopSize:]
	}
	// Keep the backing array from growing without bound.
	if cap(a.pending) > 16*hopSize {
		a.pending = append([]float64(nil), a.pending...)
	}
}

func (a *Analyzer) hop(x []float64) {
	a.samples += int64(len(x))
	if len(a.ring) < fftSize {
		a.ring = append(a.ring, x...)
		if len(a.ring) < fftSize {
			return
		}
		a.ring = a.ring[len(a.ring)-fftSize:]
	} else {
		copy(a.ring, a.ring[len(x):])
		copy(a.ring[fftSize-len(x):], x)
	}
	ts := a.now()
	a.frame++

	td := dsp.TimeDomain(x)
	levelDB := dsp.DB(td.RMS)
	a.levelRange.Update(levelDB, levelDB)
	energy := a.levelRange.Norm(levelDB)

	a.fft.Magnitudes(a.ring, a.window, a.scratch, a.mag)
	for k, m := range a.mag {
		a.magDB[k] = dsp.DB(m)
	}
	shape := dsp.SpectralShape(a.mag, a.binHz, a.lo, a.hi)

	// Bands, normalised against a range spanning the quieter bands (20th
	// percentile) up to the loudest.
	copy(a.prevBand, a.bandDB)
	a.bands.Compute(a.mag, a.bandDB)
	sorted := append([]float64(nil), a.bandDB...)
	sort.Float64s(sorted)
	a.bandRange.Update(sorted[len(sorted)/5], sorted[len(sorted)-1])
	var fluxDB float64
	rising := 0
	for i, b := range a.bandDB {
		a.bandNorm[i] = a.bandRange.Norm(b)
		if a.frame > 1 {
			if d := b - a.prevBand[i]; d > 0 {
				fluxDB += d
				if d >= 3 {
					rising++
				}
			}
		}
	}
	fluxDB /= bandCount
	flux := detect.Clamp01(fluxDB / 12)
	novelty := a.novelty.Update(a.bandNorm)

	peaks := dsp.FindPeaks(a.magDB, a.binHz, peakMinHz, peakMaxHz, 8, maxPeaks)
	a.lastPeaks = peaks
	pef := dsp.PeakEnergyFraction(a.mag, a.binHz, int(peakMinHz/a.binHz), int(peakMaxHz/a.binHz), peaks)
	harmonicity, _ := dsp.Harmonicity(peaks, pef)

	// Transients.
	if t := a.transients.Update(detect.TransientObs{
		Time: ts, FluxDB: fluxDB, Broadband: float64(rising) / bandCount,
		CentroidHz: shape.CentroidHz, BandwidthHz: shape.BandwidthHz, Novelty: novelty,
	}); t != nil {
		a.recentTransients = append(a.recentTransients, t.Start)
		a.emit(a.stamper.Stamp(events.TypeTransient, t.Start, events.Transient{
			Strength:    round(t.Strength, 3),
			DurationMs:  round(float64(t.Duration.Milliseconds()), 0),
			CentroidHz:  round(t.CentroidHz, 1),
			BandwidthHz: round(t.BandwidthHz, 1),
			Novelty:     round(t.Novelty, 3),
		}))
	}

	// Resonances.
	obs := make([]detect.PeakObs, len(peaks))
	for i, p := range peaks {
		obs[i] = detect.PeakObs{Hz: p.Hz, Amplitude: a.bandRange.Norm(p.DB), Prominence: p.Prominence}
	}
	for _, r := range a.resonances.Update(ts, obs) {
		h := make([]events.Harmonic, len(r.Harmonics))
		for i, x := range r.Harmonics {
			h[i] = events.Harmonic{Order: x.Order, Hz: round(x.Hz, 1), Strength: round(x.Strength, 3)}
		}
		a.emit(a.stamper.Stamp(events.TypeResonance, ts, events.Resonance{
			ID: r.ID, Status: r.Status,
			FundamentalHz: round(r.Fundamental, 1),
			Strength:      round(r.Strength, 3),
			DurationS:     round(r.Duration.Seconds(), 1),
			Harmonics:     h,
		}))
	}

	// 10 Hz signal layer.
	s := &a.sig
	s.n++
	s.rms += td.RMS
	s.level += levelDB
	s.peak = math.Max(s.peak, td.Peak)
	s.zcr += td.ZCR
	s.cent += shape.CentroidHz
	s.flux += flux
	s.ent += shape.Entropy
	s.har += harmonicity
	for i, b := range a.bandNorm {
		s.bands[i] += b
	}
	if a.frame%framesPerSignal == 0 {
		a.emitSignal(ts, energy)
	}

	// 2 Hz feature layer.
	f := &a.feat
	f.n++
	f.cent += shape.CentroidHz
	f.bw += shape.BandwidthHz
	f.roll += shape.RolloffHz
	f.ent += shape.Entropy
	f.flux += flux
	f.har += harmonicity
	f.energy += energy
	f.level += levelDB
	f.flat += shape.Flatness
	if a.frame%framesPerFeature == 0 {
		a.emitFeature(ts, novelty)
	}

	if a.onFrame != nil {
		a.onFrame(time.Since(ts))
	}
}

func (a *Analyzer) emitSignal(ts time.Time, energy float64) {
	s := &a.sig
	n := float64(s.n)
	bands := make([]float64, bandCount)
	for i := range bands {
		bands[i] = round(s.bands[i]/n, 3)
		s.bands[i] = 0
	}
	peaks := make([]events.Peak, 0, len(a.lastPeaks))
	for _, p := range a.lastPeaks {
		peaks = append(peaks, events.Peak{Hz: round(p.Hz, 1), Amplitude: round(a.bandRange.Norm(p.DB), 3)})
	}
	a.emit(a.stamper.Stamp(events.TypeFrame, ts, events.Frame{
		RMS:              round(s.rms/n, 5),
		LevelDB:          round(s.level/n, 1),
		Energy:           round(energy, 3),
		Peak:             round(s.peak, 4),
		ZeroCrossingRate: round(s.zcr/n, 4),
		CentroidHz:       round(s.cent/n, 1),
		Flux:             round(s.flux/n, 3),
		Entropy:          round(s.ent/n, 3),
		Harmonicity:      round(s.har/n, 3),
	}))
	a.emit(a.stamper.Stamp(events.TypeSpectrum, ts, events.Spectrum{
		MinHz: minHz, MaxHz: maxHz, Scale: "log", Bands: bands, Peaks: peaks,
	}))
	bandsBuf := s.bands
	*s = signalAcc{bands: bandsBuf}
}

func (a *Analyzer) emitFeature(ts time.Time, novelty float64) {
	f := &a.feat
	n := float64(f.n)
	cutoff := ts.Add(-5 * time.Second)
	kept := a.recentTransients[:0]
	for _, t := range a.recentTransients {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	a.recentTransients = kept

	dominant := 0.0
	if len(a.lastPeaks) > 0 {
		dominant = a.lastPeaks[0].Hz
	}
	in := detect.StateInput{
		Energy:           f.energy / n,
		LevelDB:          f.level / n,
		CentroidHz:       f.cent / n,
		Entropy:          f.ent / n,
		Flatness:         f.flat / n,
		Harmonicity:      f.har / n,
		Novelty:          novelty,
		RecentTransients: len(a.recentTransients),
		ActiveResonances: a.resonances.Active(),
	}
	a.emit(a.stamper.Stamp(events.TypeFeature, ts, events.Feature{
		DominantHz:  round(dominant, 1),
		CentroidHz:  round(in.CentroidHz, 1),
		BandwidthHz: round(f.bw/n, 1),
		RolloffHz:   round(f.roll/n, 1),
		Entropy:     round(in.Entropy, 3),
		Flux:        round(f.flux/n, 3),
		Harmonicity: round(in.Harmonicity, 3),
		Energy:      round(in.Energy, 3),
		Novelty:     round(novelty, 3),
		State:       detect.Classify(in),
	}))
	*f = featureAcc{}
}

func round(x float64, places int) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}
