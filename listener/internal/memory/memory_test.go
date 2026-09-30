package memory

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

// feeder drives a Memory with synthetic events on an audio timeline.
type feeder struct {
	t   *testing.T
	m   *Memory
	st  *events.Stamper
	pos float64 // seconds
	out []events.Message
	res int
}

func newFeeder(t *testing.T, dir string) *feeder {
	st := events.NewStamper("test")
	m, err := New(Config{Dir: dir}, st)
	if err != nil {
		t.Fatal(err)
	}
	return &feeder{t: t, m: m, st: st, pos: 1}
}

func (f *feeder) send(typ string, payload any) []events.Message {
	msgs := f.m.Process(f.st.Stamp(typ, time.Unix(0, 0).Add(time.Duration(f.pos*1e9)), f.pos, payload))
	f.out = append(f.out, msgs...)
	return msgs
}

var quiet = events.Feature{Entropy: 0.45, Harmonicity: 0.3, Flux: 0.08, CentroidHz: 1000, Energy: 0.4, DominantHz: 800}

// idle advances the timeline with feature and spectrum updates.
func (f *feeder) idle(seconds float64, bands []float64, feat events.Feature) {
	for end := f.pos + seconds; f.pos < end; f.pos += 0.5 {
		f.send(events.TypeSpectrum, events.Spectrum{Bands: bands})
		f.send(events.TypeFeature, feat)
	}
}

// resonance plays one resonance episode of the given length.
func (f *feeder) resonance(hz, seconds float64, bands []float64) (start, end events.Resonance) {
	f.res++
	id := fmt.Sprintf("res-%05d", f.res)
	h := []events.Harmonic{{Order: 2, Hz: 2 * hz, Strength: 0.5}}
	for _, m := range f.send(events.TypeResonance, events.Resonance{ID: id, Status: "start", FundamentalHz: hz, Strength: 0.8, Harmonics: h}) {
		if r, ok := m.Env.Payload.(events.Resonance); ok {
			start = r
		}
	}
	f.idle(seconds, bands, quiet)
	for _, m := range f.send(events.TypeResonance, events.Resonance{ID: id, Status: "end", FundamentalHz: hz, Strength: 0.8, DurationS: seconds}) {
		if r, ok := m.Env.Payload.(events.Resonance); ok {
			end = r
		}
	}
	return start, end
}

func (f *feeder) count(typ string) int {
	n := 0
	for _, m := range f.out {
		if m.Type == typ {
			n++
		}
	}
	return n
}

func (f *feeder) last(typ string) (events.Message, bool) {
	for i := len(f.out) - 1; i >= 0; i-- {
		if f.out[i].Type == typ {
			return f.out[i], true
		}
	}
	return events.Message{}, false
}

// detected returns the latest motif.detected of a kind.
func (f *feeder) detected(kind string) (events.MotifDetected, bool) {
	for i := len(f.out) - 1; i >= 0; i-- {
		if d, ok := f.out[i].Env.Payload.(events.MotifDetected); ok && d.Kind == kind {
			return d, true
		}
	}
	return events.MotifDetected{}, false
}

func flat(v float64) []float64 {
	b := make([]float64, 48)
	for i := range b {
		b[i] = v
	}
	return b
}

func TestResonanceMotifDetectedThenReturned(t *testing.T) {
	f := newFeeder(t, "")
	bands := flat(0.4)
	// Three 20 s appearances of ~81 Hz, a minute apart (with detection jitter).
	for _, hz := range []float64{81.2, 81.0, 81.5} {
		f.resonance(hz, 20, bands)
		f.idle(40, bands, quiet)
	}
	d, ok := f.detected(KindResonance)
	if !ok {
		t.Fatal("no resonance motif.detected after three recurrences over two minutes")
	}
	if d.Signature.FundamentalHz < 80 || d.Signature.FundamentalHz > 82 {
		t.Fatalf("unexpected detection %+v", d)
	}
	// Following appearances carry the motif id from the start.
	start, _ := f.resonance(81.3, 10, bands)
	if start.MotifID != d.MotifID {
		t.Fatalf("resonance start carries %q, want %q", start.MotifID, d.MotifID)
	}
	if f.count(events.TypeMotifReturned) != 0 {
		t.Fatal("returned although absent for less than the threshold")
	}
	// Away for four minutes, then back.
	f.idle(240, bands, quiet)
	f.resonance(81.1, 10, bands)
	ret, ok := f.last(events.TypeMotifReturned)
	if !ok {
		t.Fatal("no motif.returned after four minutes of absence")
	}
	r := ret.Env.Payload.(events.MotifReturned)
	if r.MotifID != d.MotifID || r.LastSeenS < 235 || r.LastSeenS > 245 || r.Similarity < 0.8 {
		t.Fatalf("unexpected return %+v", r)
	}
	// A different partial is a different motif.
	other, _ := f.resonance(440, 10, bands)
	if other.MotifID == d.MotifID {
		t.Fatal("440 Hz joined the 81 Hz motif")
	}
}

func TestTextureMotifDetectedThenReturned(t *testing.T) {
	f := newFeeder(t, "")
	calm, busy := flat(0.3), flat(0.8)
	busyFeat := events.Feature{Entropy: 0.8, Harmonicity: 0.1, Flux: 0.3, CentroidHz: 4000, Energy: 0.8}
	f.idle(150, calm, quiet)
	det, ok := f.detected(KindTexture)
	if !ok {
		t.Fatal("steady texture not established after 2.5 minutes")
	}
	id := det.MotifID
	f.idle(240, busy, busyFeat)
	f.idle(40, calm, quiet)
	var back bool
	for _, m := range f.out {
		if r, ok := m.Env.Payload.(events.MotifReturned); ok && r.MotifID == id {
			back = true
		}
	}
	if !back {
		t.Fatal("calm texture did not return after four minutes of a different texture")
	}
}

func TestMemorySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	f := newFeeder(t, dir)
	bands := flat(0.4)
	for i := 0; i < 3; i++ {
		f.resonance(220, 20, bands)
		f.idle(40, bands, quiet)
	}
	det, ok := f.detected(KindResonance)
	if !ok {
		t.Fatal("no resonance motif detected before restart")
	}
	if err := f.m.Close(); err != nil {
		t.Fatal(err)
	}

	g := newFeeder(t, dir)
	motifs := g.m.Motifs()
	if len(motifs) == 0 {
		t.Fatal("no motifs after reopening")
	}
	start, _ := g.resonance(220.5, 10, bands)
	want := det.MotifID
	if start.MotifID != want {
		t.Fatalf("after restart the resonance carries %q, want %q", start.MotifID, want)
	}
	for _, m := range g.out {
		if d, ok := m.Env.Payload.(events.MotifDetected); ok && d.Kind == KindResonance {
			t.Fatalf("known resonance motif detected again after restart: %+v", d)
		}
	}
}

func TestNarrativesGoToHistory(t *testing.T) {
	dir := t.TempDir()
	f := newFeeder(t, dir)
	f.idle(2, flat(0.4), quiet)
	f.m.Process(f.st.Stamp(events.TypeNarrative, time.Now(), 0, events.Narrative{Mode: "observational", Text: "Motif 9 holds at 727 Hz."}))
	var found []string
	f.m.store.scan(f.m.now.Add(-time.Hour), f.m.now.Add(time.Hour), func(r record) bool {
		if r.Event != nil && strings.Contains(string(r.Event), `"narrative.update"`) {
			found = append(found, string(r.Event))
		}
		return true
	})
	if len(found) != 1 || !strings.Contains(found[0], "727 Hz") {
		t.Fatalf("narrative not in history: %v", found)
	}
}
