package dsp

import (
	"math"
	"sort"
)

const floorDB = -120.0

// DB converts a linear amplitude to decibels with a fixed floor.
func DB(amp float64) float64 {
	if amp <= 1e-6 {
		return floorDB
	}
	return 20 * math.Log10(amp)
}

// PowerDB converts a power value to decibels with a fixed floor.
func PowerDB(p float64) float64 {
	if p <= 1e-12 {
		return floorDB
	}
	return 10 * math.Log10(p)
}

// TimeStats are computed on the raw samples of one hop.
type TimeStats struct {
	RMS  float64
	Peak float64
	ZCR  float64 // zero crossings per sample
}

func TimeDomain(x []float64) TimeStats {
	var s TimeStats
	if len(x) == 0 {
		return s
	}
	var sum float64
	crossings := 0
	for i, v := range x {
		sum += v * v
		if a := math.Abs(v); a > s.Peak {
			s.Peak = a
		}
		if i > 0 && (v >= 0) != (x[i-1] >= 0) {
			crossings++
		}
	}
	s.RMS = math.Sqrt(sum / float64(len(x)))
	s.ZCR = float64(crossings) / float64(len(x))
	return s
}

// Shape describes the distribution of a magnitude spectrum between two bins.
type Shape struct {
	CentroidHz  float64
	BandwidthHz float64 // standard deviation around the centroid
	RolloffHz   float64 // 85% of spectral energy lies below
	Entropy     float64 // normalised Shannon entropy of the power spectrum, 0..1
	Flatness    float64 // geometric / arithmetic mean of power, 0..1
}

func SpectralShape(mag []float64, binHz float64, lo, hi int) Shape {
	var s Shape
	var msum, wsum, psum float64
	for k := lo; k < hi; k++ {
		msum += mag[k]
		wsum += mag[k] * float64(k) * binHz
		psum += mag[k] * mag[k]
	}
	if msum <= 0 || psum <= 0 {
		return s
	}
	s.CentroidHz = wsum / msum
	var vsum, h, logsum float64
	target := 0.85 * psum
	cum := 0.0
	rolled := false
	for k := lo; k < hi; k++ {
		f := float64(k) * binHz
		d := f - s.CentroidHz
		vsum += mag[k] * d * d
		p := mag[k] * mag[k]
		cum += p
		if !rolled && cum >= target {
			s.RolloffHz = f
			rolled = true
		}
		if q := p / psum; q > 0 {
			h -= q * math.Log(q)
		}
		logsum += math.Log(p + 1e-20)
	}
	n := float64(hi - lo)
	s.BandwidthHz = math.Sqrt(vsum / msum)
	s.Entropy = clamp01(h / math.Log(n))
	s.Flatness = clamp01(math.Exp(logsum/n) / (psum / n))
	return s
}

// Bands groups the power spectrum into log-spaced bands between minHz and
// maxHz and returns their mean power in dB.
type Bands struct {
	MinHz, MaxHz float64
	lo, hi       []int // bin range per band, hi exclusive
}

func NewBands(count int, minHz, maxHz, binHz float64, nbins int) *Bands {
	b := &Bands{MinHz: minHz, MaxHz: maxHz, lo: make([]int, count), hi: make([]int, count)}
	ratio := math.Pow(maxHz/minHz, 1/float64(count))
	edge := minHz
	for i := 0; i < count; i++ {
		next := edge * ratio
		lo := int(math.Round(edge / binHz))
		hi := int(math.Round(next / binHz))
		if hi <= lo {
			hi = lo + 1 // low bands narrower than a bin share a single bin
		}
		if hi > nbins {
			hi = nbins
		}
		if lo >= hi {
			lo = hi - 1
		}
		b.lo[i], b.hi[i] = lo, hi
		edge = next
	}
	return b
}

func (b *Bands) Count() int { return len(b.lo) }

func (b *Bands) Compute(mag []float64, out []float64) {
	for i := range b.lo {
		var p float64
		for k := b.lo[i]; k < b.hi[i]; k++ {
			p += mag[k] * mag[k]
		}
		out[i] = PowerDB(p / float64(b.hi[i]-b.lo[i]))
	}
}

// SpectralPeak is a local maximum that stands out from its neighbourhood.
type SpectralPeak struct {
	Hz         float64
	DB         float64
	Prominence float64 // dB above the local median
}

// FindPeaks returns up to limit peaks between minHz and maxHz whose level is at
// least minProm dB above the median of the surrounding third-octave, strongest
// first. Frequencies are refined by parabolic interpolation.
func FindPeaks(magDB []float64, binHz, minHz, maxHz, minProm float64, limit int) []SpectralPeak {
	lo := int(math.Ceil(minHz / binHz))
	hi := int(maxHz / binHz)
	if lo < 2 {
		lo = 2
	}
	if hi > len(magDB)-3 {
		hi = len(magDB) - 3
	}
	var peaks []SpectralPeak
	window := make([]float64, 0, 128)
	for k := lo; k <= hi; k++ {
		v := magDB[k]
		if v <= floorDB+1 || v < magDB[k-1] || v < magDB[k+1] || v < magDB[k-2] || v < magDB[k+2] {
			continue
		}
		// Neighbourhood of ±1/6 octave, at least 8 bins each side.
		span := int(float64(k) * 0.12)
		if span < 8 {
			span = 8
		}
		// Measure against the higher side so the flank of a broad hump (or
		// the roll-off at the band edge) doesn't count as a peak.
		window = window[:0]
		for j := max(k-span, 0); j < k-2; j++ {
			window = append(window, magDB[j])
		}
		left := median(window)
		window = window[:0]
		for j := k + 3; j <= k+span && j < len(magDB); j++ {
			window = append(window, magDB[j])
		}
		prom := v - math.Max(left, median(window))
		if prom < minProm {
			continue
		}
		a, b, c := magDB[k-1], v, magDB[k+1]
		p := 0.0
		if den := a - 2*b + c; den != 0 {
			p = 0.5 * (a - c) / den
		}
		peaks = append(peaks, SpectralPeak{
			Hz:         (float64(k) + p) * binHz,
			DB:         b - 0.25*(a-c)*p,
			Prominence: prom,
		})
	}
	sort.Slice(peaks, func(i, j int) bool { return peaks[i].DB > peaks[j].DB })
	if len(peaks) > limit {
		peaks = peaks[:limit]
	}
	return peaks
}

// Harmonicity scores how well the peaks fit a single harmonic series (0..1),
// weighted by how much of the spectrum's energy the peaks carry. It also
// returns the best-fitting fundamental. Peaks count by power, so faint
// incidental maxima barely matter.
func Harmonicity(peaks []SpectralPeak, peakEnergyFraction float64) (score, f0 float64) {
	if len(peaks) == 0 {
		return 0, 0
	}
	w := make([]float64, len(peaks))
	var total float64
	for i, p := range peaks {
		w[i] = math.Pow(10, p.DB/10)
		total += w[i]
	}
	best := 0.0
	for _, cand := range peaks {
		for div := 1.0; div <= 3; div++ {
			f := cand.Hz / div
			if f < 30 {
				continue
			}
			var matched float64
			fundamentalHeard := false
			for i, p := range peaks {
				r := p.Hz / f
				k := math.Round(r)
				if k < 1 || k > 16 || math.Abs(r-k) > 0.08 || math.Abs(r-k)/k > 0.02 {
					continue
				}
				matched += w[i]
				if k <= 2 {
					fundamentalHeard = true
				}
			}
			if !fundamentalHeard {
				continue
			}
			// Among near-equal fits prefer the highest fundamental: a
			// subharmonic always fits at least as well.
			s := matched / total
			if s > best+0.01 || (s > best-0.01 && f > f0) {
				best, f0 = math.Max(s, best), f
			}
		}
	}
	return clamp01(best * clamp01(peakEnergyFraction)), f0
}

// PeakEnergyFraction returns the share of power between lo and hi bins that
// lies within ±3 bins (the Hann main lobe) of the given peaks.
func PeakEnergyFraction(mag []float64, binHz float64, lo, hi int, peaks []SpectralPeak) float64 {
	var total, inPeaks float64
	for k := lo; k < hi; k++ {
		total += mag[k] * mag[k]
	}
	if total <= 0 {
		return 0
	}
	seen := make(map[int]bool, len(peaks)*5)
	for _, p := range peaks {
		c := int(math.Round(p.Hz / binHz))
		for k := c - 3; k <= c+3; k++ {
			if k >= lo && k < hi && !seen[k] {
				seen[k] = true
				inPeaks += mag[k] * mag[k]
			}
		}
	}
	return inPeaks / total
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return floorDB
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	m := len(c) / 2
	if len(c)%2 == 1 {
		return c[m]
	}
	return (c[m-1] + c[m]) / 2
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
