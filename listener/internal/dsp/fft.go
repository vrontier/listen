// Package dsp holds the signal-processing primitives: FFT, windowing,
// spectral features, band grouping and peak picking. It has no state beyond
// precomputed tables; the stateful parts live in package analysis and detect.
package dsp

import (
	"math"
	"math/bits"
	"math/cmplx"
)

// FFT is a radix-2 complex FFT of fixed size with precomputed twiddles.
type FFT struct {
	n       int
	twiddle []complex128
	rev     []int
	buf     []complex128
}

// NewFFT returns an FFT of size n, which must be a power of two.
func NewFFT(n int) *FFT {
	if n < 2 || n&(n-1) != 0 {
		panic("dsp: FFT size must be a power of two")
	}
	f := &FFT{n: n, twiddle: make([]complex128, n/2), rev: make([]int, n), buf: make([]complex128, n)}
	for k := range f.twiddle {
		f.twiddle[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n)))
	}
	shift := 64 - uint(bits.Len(uint(n-1)))
	for i := range f.rev {
		f.rev[i] = int(bits.Reverse64(uint64(i)) >> shift)
	}
	return f
}

func (f *FFT) Size() int { return f.n }

// Transform computes the FFT of the real input x (len n) and returns the
// internal buffer holding the complex spectrum. The buffer is reused.
func (f *FFT) Transform(x []float64) []complex128 {
	for i, r := range f.rev {
		f.buf[r] = complex(x[i], 0)
	}
	for size := 2; size <= f.n; size <<= 1 {
		half := size / 2
		step := f.n / size
		for start := 0; start < f.n; start += size {
			for k := 0; k < half; k++ {
				t := f.twiddle[k*step] * f.buf[start+k+half]
				u := f.buf[start+k]
				f.buf[start+k] = u + t
				f.buf[start+k+half] = u - t
			}
		}
	}
	return f.buf
}

// Hann returns a periodic Hann window of length n.
func Hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
	}
	return w
}

// Magnitudes windows x, transforms it and writes n/2+1 amplitude values into
// out, scaled so a full-scale sine reads about 1.0 at its bin.
func (f *FFT) Magnitudes(x, window, scratch, out []float64) {
	var wsum float64
	for i := range x {
		scratch[i] = x[i] * window[i]
		wsum += window[i]
	}
	spec := f.Transform(scratch)
	scale := 2 / wsum
	for k := range out {
		out[k] = cmplx.Abs(spec[k]) * scale
	}
}
