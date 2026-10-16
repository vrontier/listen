package analysis

import (
	"math/rand/v2"
	"testing"

	"github.com/vrontier/listen/listener/internal/dsp"
	"github.com/vrontier/listen/listener/internal/events"
)

// noise is seconds of white noise with a few lines, at rate.
func noise(rate, seconds int) []float32 {
	rng := rand.New(rand.NewPCG(3, 4))
	x := make([]float32, rate*seconds)
	for i := range x {
		x[i] = float32(0.1 * rng.NormFloat64())
	}
	return x
}

// BenchmarkAnalyzer is one second of VLF-rate audio through a whole
// analysis, with a filter rule and the spectrogram view.
func BenchmarkAnalyzer(b *testing.B) {
	x := noise(32000, 1)
	a := New(32000, events.NewStamper("b"), func(events.Message) {}, Options{
		MinHz: 800, MainsHz: 50, View: &View{Scale: "linear", MaxHz: 16000, Bands: 240},
	})
	a.Feed(noise(32000, 1)) // fill the window
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Feed(x)
	}
}

// BenchmarkSharedPart is what every analysis of a stream repeats today in
// one second: 20 FFTs, their dB values and the view bands.
func BenchmarkSharedPart(b *testing.B) {
	const n = 4096
	fft, win := dsp.NewFFT(n), dsp.Hann(n)
	ring, scratch := make([]float64, n), make([]float64, n)
	for i, v := range noise(32000, 1)[:n] {
		ring[i] = float64(v)
	}
	mag, magDB := make([]float64, n/2+1), make([]float64, n/2+1)
	v := newViewBands(View{Scale: "linear", MaxHz: 16000, Bands: 240}, 16000, 32000.0/n, n/2+1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for h := 0; h < 20; h++ {
			fft.Magnitudes(ring, win, scratch, mag)
			for k, m := range mag {
				magDB[k] = dsp.DB(m)
			}
			v.update(mag)
		}
	}
}

// BenchmarkFrontEnd is one second of VLF-rate audio through a shared front
// end with the spectrogram view: what a stream pays once.
func BenchmarkFrontEnd(b *testing.B) {
	x := noise(32000, 1)
	fe := NewFrontEnd(32000, &View{Scale: "linear", MaxHz: 16000, Bands: 240}, true)
	fe.Feed(noise(32000, 1), func(*Frame) {})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fe.Feed(x, func(*Frame) {})
	}
}

// BenchmarkAnalyseShared is one second of frames through one more analysis
// of a stream whose front end already ran: what each analysis pays.
func BenchmarkAnalyseShared(b *testing.B) {
	var frames []*Frame
	fe := NewFrontEnd(32000, nil, true)
	fe.Feed(noise(32000, 2), func(f *Frame) { frames = append(frames, f) })
	frames = frames[len(frames)-20:]
	a := New(32000, events.NewStamper("b"), func(events.Message) {}, Options{MinHz: 800, MainsHz: 50})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, f := range frames {
			a.Analyse(f)
		}
	}
}
