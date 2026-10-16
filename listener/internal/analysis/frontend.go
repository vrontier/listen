package analysis

import "github.com/vrontier/listen/listener/internal/dsp"

// Frame is one step of the front end: the newest hop of samples and the
// spectrum of the window of fftSize samples that ends with it, 20 times a
// second. None of it depends on an analysis's settings, so one front end can
// feed every analysis of an input. A frame handed to several analyses is
// shared and must not be changed.
type Frame struct {
	End   int64     // samples on the input's timeline up to the end of this hop
	Hop   []float64 // the newest hop
	Mag   []float64 // magnitudes, fftSize/2+1 bins
	MagDB []float64 // the same in dB

	// The spectrogram's own bands (Options.View), normalised to 0..1, and
	// the view they follow; nil without one.
	View     *View
	ViewNorm []float64
}

// FrontEnd turns samples into frames: the window, the FFT and the
// spectrogram view.
type FrontEnd struct {
	sampleRate int
	hopSize    int
	binHz      float64
	shared     bool // frames go to several analyses: each one is new

	fft     *dsp.FFT
	window  []float64
	ring    []float64
	scratch []float64
	pending []float64
	samples int64
	view    *viewBands
	frame   Frame // reused when not shared
}

// NewFrontEnd returns a front end for mono input at sampleRate (0: the
// default) with an optional spectrogram view. With shared set, every frame
// is newly allocated so that it can be handed to several analyses, also on
// other goroutines.
func NewFrontEnd(sampleRate int, view *View, shared bool) *FrontEnd {
	if sampleRate <= 0 {
		sampleRate = DefaultSampleRate
	}
	f := &FrontEnd{
		sampleRate: sampleRate,
		hopSize:    (sampleRate + 19) / 20, // 1103 at 22.05 kHz: 20 frames/s
		binHz:      float64(sampleRate) / fftSize,
		shared:     shared,
		fft:        dsp.NewFFT(fftSize),
		window:     dsp.Hann(fftSize),
		ring:       make([]float64, 0, fftSize),
		scratch:    make([]float64, fftSize),
	}
	f.frame.Mag = make([]float64, fftSize/2+1)
	f.frame.MagDB = make([]float64, fftSize/2+1)
	f.SetView(view)
	return f
}

// SetView replaces the spectrogram's own bands (nil: none).
func (f *FrontEnd) SetView(v *View) {
	if v == nil {
		f.view = nil
		return
	}
	f.view = newViewBands(*v, float64(f.sampleRate)/2, f.binHz, fftSize/2+1)
}

// View is the spectrogram view after defaults, or nil.
func (f *FrontEnd) View() *View {
	if f.view == nil {
		return nil
	}
	v := f.view.View
	return &v
}

// Samples is the number of samples fed so far.
func (f *FrontEnd) Samples() int64 { return f.samples + int64(len(f.pending)) }

// Feed consumes mono samples and hands every complete frame to out. The
// first frame comes once the window is full.
func (f *FrontEnd) Feed(x []float32, out func(*Frame)) {
	for _, v := range x {
		f.pending = append(f.pending, float64(v))
	}
	for len(f.pending) >= f.hopSize {
		f.hop(f.pending[:f.hopSize], out)
		f.pending = f.pending[f.hopSize:]
	}
	// Keep the backing array from growing without bound.
	if cap(f.pending) > 16*f.hopSize {
		f.pending = append([]float64(nil), f.pending...)
	}
}

func (f *FrontEnd) hop(x []float64, out func(*Frame)) {
	f.samples += int64(len(x))
	if len(f.ring) < fftSize {
		f.ring = append(f.ring, x...)
		if len(f.ring) < fftSize {
			return
		}
		f.ring = f.ring[len(f.ring)-fftSize:]
	} else {
		copy(f.ring, f.ring[len(x):])
		copy(f.ring[fftSize-len(x):], x)
	}
	fr := &f.frame
	if f.shared {
		fr = &Frame{
			Hop: append([]float64(nil), x...),
			Mag: make([]float64, fftSize/2+1), MagDB: make([]float64, fftSize/2+1),
		}
	} else {
		fr.Hop = x
	}
	fr.End = f.samples
	f.fft.Magnitudes(f.ring, f.window, f.scratch, fr.Mag)
	for k, m := range fr.Mag {
		fr.MagDB[k] = dsp.DB(m)
	}
	fr.View, fr.ViewNorm = nil, nil
	if v := f.view; v != nil {
		v.update(fr.Mag)
		norm := make([]float64, len(v.db))
		for i, b := range v.db {
			norm[i] = v.rng.Norm(b)
		}
		vv := v.View
		fr.View, fr.ViewNorm = &vv, norm
	}
	out(fr)
}

// ViewBands computes a spectrogram view from the frames of a front end that
// has none, for one consumer of a shared front end: several consumers may
// each want a view of their own.
type ViewBands struct{ vb *viewBands }

// NewViewBands returns the view v for frames at sampleRate.
func NewViewBands(v View, sampleRate int) *ViewBands {
	if sampleRate <= 0 {
		sampleRate = DefaultSampleRate
	}
	bin := float64(sampleRate) / fftSize
	return &ViewBands{newViewBands(v, float64(sampleRate)/2, bin, fftSize/2+1)}
}

// Apply returns fr with this view's bands; fr itself is not changed.
func (v *ViewBands) Apply(fr *Frame) *Frame {
	v.vb.update(fr.Mag)
	norm := make([]float64, len(v.vb.db))
	for i, b := range v.vb.db {
		norm[i] = v.vb.rng.Norm(b)
	}
	out := *fr
	vv := v.vb.View
	out.View, out.ViewNorm = &vv, norm
	return &out
}

// View is the view after defaults.
func (v *ViewBands) View() View { return v.vb.View }
