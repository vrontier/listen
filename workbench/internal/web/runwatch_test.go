package web

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

func TestRunVerdict(t *testing.T) {
	start := time.Date(2026, 10, 15, 20, 0, 0, 0, time.UTC)
	a := catalogue.Analysis{Sources: []catalogue.AnalysisSource{
		{Stream: catalogue.Stream{ID: 1, Texts: catalogue.StreamTexts{Name: "Ridge"}}},
		{Stream: catalogue.Stream{ID: 2, Texts: catalogue.StreamTexts{Name: "Valley"}}},
	}}
	run := catalogue.Run{Started: start, OfflineStop: 15}
	at := func(ridge, valley time.Time) func(catalogue.Stream) time.Time {
		return func(st catalogue.Stream) time.Time {
			if st.ID == 1 {
				return ridge
			}
			return valley
		}
	}
	never := time.Time{}
	for _, c := range []struct {
		name          string
		run           catalogue.Run
		ridge, valley time.Time
		now           time.Duration // after the start
		want          string
	}{
		{"both sending", run, start.Add(29 * time.Minute), start.Add(29 * time.Minute), 30 * time.Minute, ""},
		{"just started, nothing yet", run, never, never, 5 * time.Minute, ""},
		{"never connected", run, never, start.Add(19 * time.Minute), 20 * time.Minute, catalogue.EndOffline},
		{"one quiet for 14 min", run, start.Add(16 * time.Minute), start.Add(29 * time.Minute), 30 * time.Minute, ""},
		{"one quiet for 15 min", run, start.Add(15 * time.Minute), start.Add(29 * time.Minute), 30 * time.Minute, catalogue.EndOffline},
		{"switch off, one quiet", catalogue.Run{Started: start}, start.Add(time.Minute), start.Add(59 * time.Minute), time.Hour, ""},
		{"switch off, both quiet", catalogue.Run{Started: start}, start.Add(40 * time.Minute), start.Add(44 * time.Minute), time.Hour, catalogue.EndSilent},
		{"switch at 60, both quiet 15", catalogue.Run{Started: start, OfflineStop: 60}, start.Add(40 * time.Minute), start.Add(45 * time.Minute), time.Hour, catalogue.EndSilent},
		{"audio before the run does not count", run, start.Add(-time.Hour), start.Add(19 * time.Minute), 14 * time.Minute, ""},
	} {
		got, note := runVerdict(c.run, a, at(c.ridge, c.valley), start.Add(c.now))
		if got != c.want {
			t.Errorf("%s: %q (%s), want %q", c.name, got, note, c.want)
		}
	}
	_, note := runVerdict(run, a, at(start, start.Add(29*time.Minute)), start.Add(30*time.Minute))
	if note != "Ridge sent nothing for 15 minutes" {
		t.Errorf("note: %q", note)
	}
}

// A run that ends by itself is recorded with why, and its member is told
// once; a run its member stopped is not mailed about.
func TestRunEndsByItself(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Ridge"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "Dusk", "", []int64{st.ID}, nil)
	a, _ = r.db.Analysis(ctx, a.ID)

	run, _ := r.db.StartRunWith(ctx, a.ID, u.ID, time.Time{}, 15)
	if run.OfflineStop != 15 {
		t.Fatalf("offline stop: %d", run.OfflineStop)
	}
	// A healthy run is left alone by the watcher.
	r.srv.pushAll(ctx, a.Sources)
	deadline := time.Now().Add(5 * time.Second)
	for r.srv.heard.at(st.EngineID()).IsZero() {
		r.srv.watchRuns(ctx)
		if time.Now().After(deadline) {
			t.Fatal("the source's audio is never noticed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, active, _ := r.db.ActiveRun(ctx, a.ID); !active {
		t.Fatal("the watcher ended a run whose source sends")
	}
	r.srv.endRunBecause(ctx, run, a, catalogue.EndOffline, "Ridge sent nothing for 15 minutes")
	run, _ = r.db.Run(ctx, run.ID)
	if run.Stopped.IsZero() || run.EndReason != catalogue.EndOffline || run.StoppedBy != "" {
		t.Fatalf("not ended: %+v", run)
	}
	r.srv.tellRunEnds(ctx)
	r.srv.tellRunEnds(ctx)
	if n := r.mail.Count("m@example.org"); n != 1 {
		t.Fatalf("%d mails", n)
	}
	m, _ := r.mail.Last("m@example.org")
	if !strings.Contains(m.Subject, "Run 1 of Dusk ended") || !strings.Contains(m.Body, "Ridge sent nothing for 15 minutes") || !strings.Contains(m.Body, "/runs/") {
		t.Fatalf("mail: %s\n%s", m.Subject, m.Body)
	}
	c := r.loginAs("m@example.org")
	if _, page, _ := r.get(c, runPath(run.ID)); !strings.Contains(page, "by itself: Ridge sent nothing for 15 minutes") {
		t.Fatal("run page does not say why it ended")
	}

	// Stopped by the member: no mail.
	r.db.StartRun(ctx, a.ID, u.ID, time.Time{})
	r.db.StopRun(ctx, a.ID, u.ID)
	before := r.mail.Count("m@example.org")
	r.srv.tellRunEnds(ctx)
	if r.mail.Count("m@example.org") != before {
		t.Fatal("mailed about a run the member stopped")
	}

	// The run form takes the switch.
	_, page, _ := r.get(c, analysisPath(a))
	if !strings.Contains(page, `name="offline_stop" checked`) || !strings.Contains(page, `name="offline_min" value="15"`) {
		t.Fatal("no offline switch on the analysis page")
	}
	csrf := csrfOf(t, page)
	r.post(c, analysisPath(a)+"/run", map[string][]string{"csrf": {csrf}, "length": {"1h"}, "offline_min": {"30"}})
	got, active, _ := r.db.ActiveRun(ctx, a.ID)
	if !active || got.OfflineStop != 0 {
		t.Fatalf("switch off: %v %d", active, got.OfflineStop)
	}
	r.db.StopRun(ctx, a.ID, u.ID)
	r.post(c, analysisPath(a)+"/run", map[string][]string{"csrf": {csrf}, "length": {"1h"}, "offline_stop": {"on"}, "offline_min": {"30"}})
	if got, _, _ = r.db.ActiveRun(ctx, a.ID); got.OfflineStop != 30 {
		t.Fatalf("switch at 30: %d", got.OfflineStop)
	}
}
