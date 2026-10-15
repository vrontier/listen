package web

import (
	"bytes"
	"context"
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
