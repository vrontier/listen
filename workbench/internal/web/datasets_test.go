package web

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// The log keeps each event once, however often it arrives, and knows
// after reopening where it stopped.
func TestEventLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.jsonl")
	w, err := openLog(path)
	if err != nil {
		t.Fatal(err)
	}
	e1 := `{"type":"event.transient","timestamp":"2026-10-15T10:00:00.100Z","sequence":1}`
	e2 := `{"type":"event.resonance","timestamp":"2026-10-15T10:00:00.100Z","sequence":2}`
	e3 := `{"type":"event.transient","timestamp":"2026-10-15T10:00:01.000Z","sequence":3}`
	for _, e := range []string{e1, e2, e1, e3, e2} {
		w.add([]byte(e))
	}
	w.close()
	w, _ = openLog(path)
	w.add([]byte(e3)) // came again through catching up
	w.add([]byte(`{"type":"event.transient","timestamp":"2026-10-15T10:00:02.000Z","sequence":4}`))
	w.close()
	b, _ := os.ReadFile(path)
	if n := bytes.Count(b, []byte("\n")); n != 4 {
		t.Fatalf("%d lines:\n%s", n, b)
	}
}

// A run's events are recorded while it runs; after an interruption the
// recording catches up from the engine without doubling anything.
func TestRecordingCatchesUp(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, err := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "x", "", []int64{st.ID}, nil)
	run, err := r.db.StartRun(ctx, a.ID, u.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	r.srv.pushAll(ctx, a.Sources)
	r.srv.data.refresh(ctx)
	path := filepath.Join(r.srv.data.runDir(run.ID), st.EngineID()+".jsonl")
	lines := func() []string {
		b, _ := os.ReadFile(path)
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	waitFor := func(n int) {
		deadline := time.Now().Add(8 * time.Second)
		for len(lines()) < n || lines()[0] == "" {
			if time.Now().After(deadline) {
				t.Fatalf("fewer than %d events recorded: %v", n, lines())
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitFor(1)

	// The Workbench goes away for a while: the recording stops, the engine
	// goes on.
	r.srv.data.mu.Lock()
	rec := r.srv.data.recs[run.ID]
	delete(r.srv.data.recs, run.ID)
	r.srv.data.mu.Unlock()
	for _, cancel := range rec.topics {
		cancel()
	}
	rec.wg.Wait()
	before := len(lines())
	time.Sleep(1500 * time.Millisecond)
	r.srv.data.refresh(ctx)
	waitFor(before + 1)
	seen := map[string]bool{}
	for _, l := range lines() {
		if seen[l] {
			t.Fatalf("recorded twice: %s", l)
		}
		seen[l] = true
	}

	if err := r.db.StopRun(ctx, a.ID, u.ID); err != nil {
		t.Fatal(err)
	}
	r.srv.data.refresh(ctx)
	r.srv.data.busy.Wait()
	run, _ = r.db.Run(ctx, run.ID)
	if run.Dataset != catalogue.DatasetReady || run.DatasetBytes == 0 || !strings.Contains(string(run.Summary), `"total":`) {
		t.Fatalf("data set: %s %d %s", run.Dataset, run.DatasetBytes, run.Summary)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("recording kept after freezing: %v", err)
	}
}

func TestAbsences(t *testing.T) {
	t0 := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	at := func(s float64) time.Time { return t0.Add(time.Duration(s * float64(time.Second))) }
	lines := []stateLine{
		{"s-1", "idle", at(0.01)}, {"s-1", "connecting", at(0.2)}, {"s-1", "connected", at(0.9)}, // under a second: not reported
		{"s-1", "removed", at(60)}, {"s-1", "idle", at(70)}, {"s-1", "connecting", at(71)}, {"s-1", "connected", at(72)},
		{"s-2", "connected", at(-5)},
		{"s-1", "idle", at(100)},
	}
	got := absences(lines, "s-1", t0, at(110))
	if len(got) != 2 || got[0].Seconds != 12 || strings.Join(got[0].States, " ") != "removed idle connecting" || got[1].Seconds != 10 {
		t.Fatalf("absences of s-1: %+v", got)
	}
	if got := absences(lines, "s-2", t0, at(110)); len(got) != 0 {
		t.Fatalf("absences of s-2: %+v", got)
	}
}

// A source paused during a run shows as absent in the data set.
func TestAbsenceRecorded(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "x", "", []int64{st.ID}, nil)
	run, _ := r.db.StartRun(ctx, a.ID, u.ID, time.Time{})
	r.srv.pushAll(ctx, a.Sources)
	r.srv.data.refresh(ctx)
	waitState := func(want string) {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if es, ok := r.srv.sync.stream(st.EngineID()); ok && es.State == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("stream never %s", want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitState("connected")
	for _, state := range []string{catalogue.StreamPaused, catalogue.StreamDraft} {
		r.db.SetStreamState(ctx, st.ID, u.ID, state)
		st, _ = r.db.Stream(ctx, st.ID)
		r.srv.sync.push(ctx, st)
		time.Sleep(1200 * time.Millisecond)
	}
	waitState("connected")
	r.db.StopRun(ctx, a.ID, u.ID)
	r.srv.data.refresh(ctx)
	r.srv.data.busy.Wait()
	run, _ = r.db.Run(ctx, run.ID)
	var sum datasetSummary
	json.Unmarshal(run.Summary, &sum)
	// The pause, and perhaps the first moment of connecting at the start.
	if len(sum.Sources) != 1 || sum.Sources[0].Absences < 1 || sum.Sources[0].AbsentS < 1.2 {
		t.Fatalf("summary: %s", run.Summary)
	}
	f, _ := os.Open(filepath.Join(r.srv.data.runDir(run.ID), fileEvents+".gz"))
	defer f.Close()
	zr, _ := gzip.NewReader(f)
	b, _ := io.ReadAll(zr)
	if !strings.Contains(string(b), "source.absent,,removed") {
		t.Fatalf("events.csv without the absence:\n%s", b)
	}
}
