package analysis

import (
	"math"
	"testing"
	"time"

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
