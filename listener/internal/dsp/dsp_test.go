package dsp

import (
	"math"
	"math/rand"
	"testing"
)

const (
	sr = 22050.0
	n  = 4096
)

func analyse(x []float64) (mag, magDB []float64) {
	f := NewFFT(n)
	mag = make([]float64, n/2+1)
	f.Magnitudes(x, Hann(n), make([]float64, n), mag)
	magDB = make([]float64, len(mag))
	for k, m := range mag {
		magDB[k] = DB(m)
	}
	return mag, magDB
}

func tone(freqs []float64, amps []float64, noise float64) []float64 {
	rng := rand.New(rand.NewSource(1))
	x := make([]float64, n)
	for i := range x {
		for j, f := range freqs {
			x[i] += amps[j] * math.Sin(2*math.Pi*f*float64(i)/sr)
		}
		x[i] += noise * rng.NormFloat64()
	}
	return x
}

func TestFFTSineAmplitudeAndPeak(t *testing.T) {
	mag, magDB := analyse(tone([]float64{1000}, []float64{0.5}, 0))
	peaks := FindPeaks(magDB, sr/n, 40, 6000, 8, 4)
	if len(peaks) != 1 {
		t.Fatalf("want 1 peak, got %v", peaks)
	}
	if math.Abs(peaks[0].Hz-1000) > 1 {
		t.Errorf("peak at %.2f Hz, want 1000 ±1", peaks[0].Hz)
	}
	k := int(math.Round(1000 / (sr / n)))
	if got := mag[k]; math.Abs(got-0.5) > 0.1 {
		t.Errorf("amplitude %.3f, want ≈0.5", got)
	}
}

func TestHarmonicityToneVersusNoise(t *testing.T) {
	binHz := sr / n
	lo, hi := int(40/binHz), int(6000/binHz)

	mag, magDB := analyse(tone([]float64{81, 162, 243, 324}, []float64{0.4, 0.3, 0.2, 0.1}, 0.001))
	peaks := FindPeaks(magDB, binHz, 40, 6000, 8, 12)
	h, f0 := Harmonicity(peaks, PeakEnergyFraction(mag, binHz, lo, hi, peaks))
	if h < 0.8 {
		t.Errorf("harmonic tone: harmonicity %.2f, want ≥0.8", h)
	}
	if math.Abs(f0-81) > 2 {
		t.Errorf("harmonic tone: f0 %.1f, want ≈81", f0)
	}

	mag, magDB = analyse(tone(nil, nil, 0.3))
	peaks = FindPeaks(magDB, binHz, 40, 6000, 8, 12)
	h, _ = Harmonicity(peaks, PeakEnergyFraction(mag, binHz, lo, hi, peaks))
	if h > 0.2 {
		t.Errorf("white noise: harmonicity %.2f, want ≤0.2", h)
	}
	if s := SpectralShape(mag, binHz, lo, hi); s.Entropy < 0.9 {
		t.Errorf("white noise: entropy %.2f, want ≥0.9", s.Entropy)
	}
}

func TestBandsCoverRange(t *testing.T) {
	b := NewBands(48, 20, 11000, sr/n, n/2+1)
	for i := range b.lo {
		if b.hi[i] <= b.lo[i] {
			t.Fatalf("band %d empty: [%d,%d)", i, b.lo[i], b.hi[i])
		}
	}
	if top := b.hi[len(b.hi)-1]; top > n/2+1 {
		t.Fatalf("top band exceeds spectrum: %d", top)
	}
}
