package analysis

import (
	"math"
	"testing"
	"time"

	"github.com/vrontier/listen/listener/internal/dsp"
	"github.com/vrontier/listen/listener/internal/events"
)

func TestReplayClockWrapsWithinRecording(t *testing.T) {
	start := time.Date(2026, 9, 30, 8, 35, 4, 0, time.UTC)
	length := 2 * time.Second
	var stamps []time.Time
	a := New(0, events.NewStamper("test"), func(m events.Message) {
		if m.Type == events.TypeFrame {
			stamps = append(stamps, m.Env.Timestamp)
		}
	})
	a.Replay(start, length)

	// 5 s of a quiet tone: the looped file plays two and a half times.
	x := make([]float32, 5*DefaultSampleRate)
	for i := range x {
		x[i] = float32(0.1 * math.Sin(2*math.Pi*440*float64(i)/DefaultSampleRate))
	}
	a.Feed(x)

	if len(stamps) < 40 {
		t.Fatalf("only %d frames", len(stamps))
	}
	wrapped := false
	for i, ts := range stamps {
		if ts.Before(start) || !ts.Before(start.Add(length)) {
			t.Fatalf("frame %d at %s, outside the recording [%s, %s)", i, ts, start, start.Add(length))
		}
		if i > 0 && ts.Before(stamps[i-1]) {
			wrapped = true
		}
	}
	if !wrapped {
		t.Error("clock never wrapped back to the start of the recording")
	}
}

// VLF focus: the bands cover only the chosen range, and mains harmonics are
// dropped from the peaks while a natural tone between them is kept.
func TestFocusRangeAndMainsFilter(t *testing.T) {
	const sr = 32000
	var spectra []events.Spectrum
	a := New(sr, events.NewStamper("test"), func(m events.Message) {
		if s, ok := m.Env.Payload.(events.Spectrum); ok {
			spectra = append(spectra, s)
		}
	}, Options{MinHz: 800, MaxHz: 12000, MainsHz: 60})

	// Hum on 360, 420 and 1800 Hz (6th, 7th and 30th harmonic of 60 Hz),
	// and a tone at 3030 Hz, half-way between two harmonics.
	x := make([]float32, 3*sr)
	for i := range x {
		ts := float64(i) / sr
		v := 0.2*math.Sin(2*math.Pi*360*ts) + 0.2*math.Sin(2*math.Pi*420*ts) +
			0.1*math.Sin(2*math.Pi*1800*ts) + 0.05*math.Sin(2*math.Pi*3030*ts)
		x[i] = float32(v)
	}
	a.Feed(x)

	if len(spectra) == 0 {
		t.Fatal("no spectrum events")
	}
	last := spectra[len(spectra)-1]
	if last.MinHz != 800 || last.MaxHz != 12000 {
		t.Fatalf("range %.0f–%.0f Hz, want 800–12000", last.MinHz, last.MaxHz)
	}
	found := false
	for _, p := range last.Peaks {
		if p.Hz < 800 {
			t.Errorf("peak at %.1f Hz below the focus range", p.Hz)
		}
		if math.Abs(p.Hz-1800) < 12 {
			t.Errorf("mains harmonic at %.1f Hz was kept", p.Hz)
		}
		if math.Abs(p.Hz-3030) < 12 {
			found = true
		}
	}
	if !found {
		t.Errorf("the 3030 Hz tone is missing from the peaks %v", last.Peaks)
	}
}

func TestParseIgnore(t *testing.T) {
	for in, want := range map[string]Ignore{
		"comb=16.7,odd,tol=4":           {Hz: 16.7, Comb: true, Odd: true, TolHz: 4},
		"comb=16.7,odd,tol=4,drift=0.4": {Hz: 16.7, Comb: true, Odd: true, TolHz: 4, Drift: 0.4},
		"comb=50":                       {Hz: 50, Comb: true},
		"line=1000,tol=5":               {Hz: 1000, TolHz: 5},
	} {
		got, err := ParseIgnore(in)
		if err != nil || got != want {
			t.Errorf("ParseIgnore(%q) = %+v, %v; want %+v", in, got, err, want)
		}
		if got.String() != in {
			t.Errorf("%q reads back as %q", in, got.String())
		}
	}
	for _, in := range []string{"", "comb", "comb=0", "comb=x", "line=1000,odd", "tol=4", "comb=50,tol=0", "comb=50,width=3", "line=NaN", "line=1000,drift=1", "comb=50,drift=5"} {
		if r, err := ParseIgnore(in); err == nil {
			t.Errorf("ParseIgnore(%q) = %+v, want an error", in, r)
		}
	}
}

// A railway on 16.7 Hz: its odd harmonics are dropped, however strong, and
// weaker tones between them are still found.
func TestIgnoreRules(t *testing.T) {
	const sr = 32000
	rail := Ignore{Hz: 16.7, Comb: true, Odd: true, TolHz: 4}
	for hz, want := range map[float64]bool{
		16.7: true, 1386.1: true, 1384: true, 1419.5: true, // 1st, 83rd, 85th
		1402.8: false, // 84th: even
		1395:   false, // between the 83rd and the 84th
		33.4:   false, 8: false,
	} {
		if got := rail.offset(hz, rail.Hz) <= rail.TolHz; got != want {
			t.Errorf("rail rule on %.1f Hz: %v, want %v", hz, got, want)
		}
	}

	var spectra []events.Spectrum
	a := New(sr, events.NewStamper("test"), func(m events.Message) {
		if s, ok := m.Env.Payload.(events.Spectrum); ok {
			spectra = append(spectra, s)
		}
	}, Options{MinHz: 800, Ignore: []Ignore{rail, {Hz: 3000, TolHz: 5}}})

	// 20 strong railway lines (odd harmonics 61 to 99, more than the
	// analyser keeps peaks), a pilot tone at 3 kHz, and two weak tones above
	// the lines that the rules must leave alone.
	x := make([]float32, 3*sr)
	for i := range x {
		ts := float64(i) / sr
		v := 0.1*math.Sin(2*math.Pi*3000*ts) + 0.004*math.Sin(2*math.Pi*2500*ts) + 0.004*math.Sin(2*math.Pi*1900*ts)
		for k := 61; k <= 99; k += 2 {
			v += 0.04 * math.Sin(2*math.Pi*16.7*float64(k)*ts)
		}
		x[i] = float32(v)
	}
	a.Feed(x)

	if len(spectra) == 0 {
		t.Fatal("no spectrum events")
	}
	found := map[float64]bool{}
	for _, p := range spectra[len(spectra)-1].Peaks {
		switch {
		case math.Abs(p.Hz-2500) < 3:
			found[2500] = true
		case math.Abs(p.Hz-1900) < 3:
			found[1900] = true
		default:
			t.Errorf("peak at %.1f Hz was kept", p.Hz)
		}
	}
	if !found[2500] || !found[1900] {
		t.Errorf("the tones at 1900 and 2500 Hz should remain, got %v", spectra[len(spectra)-1].Peaks)
	}
}

// The railway's frequency wanders: with drift, the comb follows it and still
// meets the 261st harmonic, which a fixed base misses by more than 4 Hz.
func TestIgnoreDrift(t *testing.T) {
	const actual = 16.72
	var peaks []dsp.SpectralPeak
	for k := 61; k <= 261; k += 20 {
		peaks = append(peaks, dsp.SpectralPeak{Hz: actual * float64(k)})
	}
	peaks = append(peaks, dsp.SpectralPeak{Hz: 2500}, dsp.SpectralPeak{Hz: 5010})

	fixed := Ignore{Hz: 16.7, Comb: true, Odd: true, TolHz: 4}
	if d := fixed.offset(actual*261, fixed.base(peaks)); d <= 4 {
		t.Fatalf("fixed base: the 261st harmonic is only %.1f Hz off; the test needs it missed", d)
	}
	rail := fixed
	rail.Drift = 0.4
	base := rail.base(peaks)
	if math.Abs(base-actual) > 0.003 {
		t.Errorf("fitted base %.4f Hz, want %.2f", base, actual)
	}
	for _, p := range peaks {
		isRail := p.Hz != 2500 && p.Hz != 5010
		if got := rail.offset(p.Hz, base) <= rail.TolHz; got != isRail {
			t.Errorf("peak at %.1f Hz: dropped %v, want %v", p.Hz, got, isRail)
		}
	}
	// Nothing to follow: the base stays nominal.
	if b := rail.base([]dsp.SpectralPeak{{Hz: 2500}, {Hz: 5010}}); b != 16.7 {
		t.Errorf("base without railway lines: %.4f, want 16.7", b)
	}
}
