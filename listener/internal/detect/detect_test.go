package detect

import (
	"math/rand"
	"testing"
	"time"
)

var t0 = time.Unix(0, 0)

const frame = 50 * time.Millisecond

func TestTransientDetectsBurstsOnly(t *testing.T) {
	d := NewTransientDetector(DefaultTransientConfig())
	rng := rand.New(rand.NewSource(1))
	var got []time.Duration
	for i := 0; i < 400; i++ {
		o := TransientObs{Time: t0.Add(time.Duration(i) * frame), FluxDB: 1 + 0.3*rng.Float64(), Broadband: 0.05}
		// Bursts at 5 s and 12 s, two frames each.
		if i == 100 || i == 101 || i == 240 || i == 241 {
			o.FluxDB, o.Broadband = 9, 0.7
		}
		if tr := d.Update(o); tr != nil {
			got = append(got, tr.Start.Sub(t0))
		}
	}
	want := []time.Duration{5 * time.Second, 12 * time.Second}
	if len(got) != len(want) {
		t.Fatalf("got transients at %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("transient %d at %v, want %v", i, got[i], want[i])
		}
	}
}

func TestResonanceLifecycleWithHarmonics(t *testing.T) {
	tr := NewResonanceTracker(DefaultResonanceConfig(5.4))
	counts := map[string]int{}
	var start *ResonanceEvent
	for i := 0; i < 400; i++ { // 20 s
		now := t0.Add(time.Duration(i) * frame)
		var peaks []PeakObs
		if i < 300 { // present for 15 s, with the harmonic missing now and then
			peaks = append(peaks, PeakObs{Hz: 81.2, Amplitude: 0.9, Prominence: 20})
			if i%4 != 0 {
				peaks = append(peaks, PeakObs{Hz: 162.5, Amplitude: 0.7, Prominence: 15})
			}
		}
		// A fleeting partial that must never become a resonance.
		if i%7 == 0 {
			peaks = append(peaks, PeakObs{Hz: 3000, Amplitude: 0.5, Prominence: 12})
		}
		for _, e := range tr.Update(now, peaks) {
			counts[e.Status]++
			if e.Status == "start" {
				e := e
				start = &e
			}
		}
	}
	if counts["start"] != 1 || counts["end"] != 1 {
		t.Fatalf("want one start and one end, got %v", counts)
	}
	if start.Fundamental < 80 || start.Fundamental > 82.5 {
		t.Errorf("fundamental %.1f, want ≈81.2", start.Fundamental)
	}
	if len(start.Harmonics) != 1 || start.Harmonics[0].Order != 2 {
		t.Errorf("harmonics %+v, want order 2", start.Harmonics)
	}
	if counts["update"] < 8 {
		t.Errorf("only %d updates for a ~10 s resonance", counts["update"])
	}
}

func TestClassifyIsDescriptive(t *testing.T) {
	cases := []struct {
		in   StateInput
		want string
	}{
		{StateInput{LevelDB: -80, Energy: 0.5}, "quiet"},
		{StateInput{LevelDB: -30, Energy: 0.5, RecentTransients: 3}, "transient_activity"},
		{StateInput{LevelDB: -30, Energy: 0.5, Harmonicity: 0.6, ActiveResonances: 1, Novelty: 0.1}, "stable_resonance"},
		{StateInput{LevelDB: -30, Energy: 0.5, CentroidHz: 400, Harmonicity: 0.1}, "wind_like"},
		{StateInput{LevelDB: -30, Energy: 0.5, CentroidHz: 3000, Flatness: 0.5, Harmonicity: 0.1}, "broadband_noise"},
		{StateInput{LevelDB: -30, Energy: 0.5, CentroidHz: 7000, Entropy: 0.63, Flatness: 0.1, Harmonicity: 0.08}, "broadband_noise"},
	}
	for _, c := range cases {
		if got := Classify(c.in); got != c.want {
			t.Errorf("Classify(%+v) = %s, want %s", c.in, got, c.want)
		}
	}
}
