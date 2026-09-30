package narrate

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

// fakeModel answers chat completions with scripted replies and records
// what it was asked.
type fakeModel struct {
	mu      sync.Mutex
	replies []string
	asked   []string
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Messages []chatMessage `json:"messages"`
		Effort   string        `json:"reasoning_effort"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req.Messages[len(req.Messages)-1].Content)
	reply := "The strongest resonance at 727 Hz holds, known as motif 9. The state is stable resonance."
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
		"message": map[string]any{"role": "assistant", "content": reply}, "finish_reason": "stop"}}})
}

func (f *fakeModel) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.asked) }

type rig struct {
	n       *Narrator
	model   *fakeModel
	st      *events.Stamper
	out     []events.Narrative
	watched int
}

func newRig(t *testing.T) *rig {
	r := &rig{model: &fakeModel{}, st: events.NewStamper("test")}
	srv := httptest.NewServer(r.model)
	t.Cleanup(srv.Close)
	r.n = New(Config{BaseURL: srv.URL, Model: "fake", Every: time.Millisecond}, r.st,
		func(m events.Message) { r.out = append(r.out, m.Env.Payload.(events.Narrative)) },
		func() int { return r.watched })
	return r
}

func (r *rig) send(typ string, p any) { r.n.Observe(r.st.Stamp(typ, time.Now(), 1, p)) }

func (r *rig) scene() {
	r.send(events.TypeFeature, events.Feature{State: "stable_resonance", Entropy: 0.45, Harmonicity: 0.24})
	r.send(events.TypeResonance, events.Resonance{ID: "res-00001", Status: "start", MotifID: "motif-009",
		FundamentalHz: 727.2, Strength: 0.9, DurationS: 64, Harmonics: []events.Harmonic{{Order: 2, Hz: 1454.5}}})
}

func (r *rig) tick() {
	time.Sleep(2 * time.Millisecond)
	r.n.tick(context.Background())
}

func TestNarratesOnlyWhileWatchedAndWhenChanged(t *testing.T) {
	r := newRig(t)
	r.scene()
	r.tick()
	if r.model.calls() != 0 {
		t.Fatal("asked the model while nobody was watching")
	}
	r.watched = 1
	r.tick()
	if len(r.out) != 1 {
		t.Fatalf("got %d narrations, want 1", len(r.out))
	}
	got := r.out[0]
	if got.Mode != "observational" || !strings.Contains(got.Text, "727 Hz") {
		t.Fatalf("unexpected narrative %+v", got)
	}
	if strings.Join(got.Evidence, ",") != "motif-009,res-00001" {
		t.Fatalf("evidence %v", got.Evidence)
	}
	asked := r.model.asked[0]
	for _, want := range []string{"727 Hz", "1.45 kHz", "motif 9", "stable resonance"} {
		if !strings.Contains(asked, want) {
			t.Errorf("evidence sent to the model lacks %q:\n%s", want, asked)
		}
	}
	r.tick()
	if r.model.calls() != 1 {
		t.Fatal("narrated again although nothing changed")
	}
	r.send(events.TypeMotifReturned, events.MotifReturned{MotifID: "motif-004", LastSeenS: 240, Similarity: 0.91, Occurrence: 12})
	r.tick()
	if r.model.calls() != 2 || !strings.Contains(r.model.asked[1], "Motif 4 returned after 4 minutes") {
		t.Fatalf("return not narrated; asked %v", r.model.asked)
	}
}

func TestRejectsInventedFacts(t *testing.T) {
	r := newRig(t)
	r.watched = 1
	r.scene()
	r.model.replies = []string{
		"A deep hum at 55 Hz rises from the ground.", // invented frequency: retried
		"The resonance at 727 Hz holds, known as motif 9. The field stays stable.",
	}
	r.tick()
	if r.model.calls() != 2 || len(r.out) != 1 || strings.Contains(r.out[0].Text, "55 Hz") {
		t.Fatalf("calls %d, out %+v", r.model.calls(), r.out)
	}
	if !strings.Contains(r.model.asked[1], "55 Hz") {
		t.Errorf("retry didn't name the problem: %q", r.model.asked[1])
	}

	// Two inventions in a row: nothing is published.
	r.send(events.TypeTransient, events.Transient{Strength: 0.4, BandwidthHz: 3000})
	r.model.replies = []string{"Motif 12 appeared.", "Motif 12 appeared again at 9 kHz."}
	r.tick()
	if len(r.out) != 1 {
		t.Fatalf("published invented text: %+v", r.out[len(r.out)-1])
	}
}

func TestAllowanceMatchesWrittenFrequencies(t *testing.T) {
	a := allowance{motifs: map[int]bool{9: true}}
	a.addHz(727.2)
	a.addHz(1454.5)
	for text, ok := range map[string]bool{
		"727 Hz and 1.45 kHz":      true,
		"a tone at 1,45 kHz":       true,
		"motif 9 at 727hz":         true,
		"a partial near 800 Hz":    false,
		"motif 3 returned":         false,
		"nothing numeric here":     true,
		"Motif #9 held at 1454 Hz": true,
	} {
		if got := a.check(text) == ""; got != ok {
			t.Errorf("check(%q) ok=%v, want %v (%q)", text, got, ok, a.check(text))
		}
	}
}
