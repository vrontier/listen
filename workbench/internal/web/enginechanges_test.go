package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// The contract changes decided on 2026-10-16: an analysis fails on its own
// and is started again, and a stream has a spectrogram view of its own.
func TestFailedAnalysisAndView(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "Mira", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 32000,
		Texts: catalogue.StreamTexts{Name: "Ridge"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "Night watch", "", []int64{st.ID}, nil)
	member := r.loginAs("m@example.org")
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	_, page, _ := r.get(member, aURL)
	csrf := csrfOf(t, page)
	if code, _, _ := r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"6h"}}); code != http.StatusSeeOther {
		t.Fatalf("start: %d", code)
	}
	run, _, _ := r.db.ActiveRun(ctx, a.ID)

	// The engine's analysis of the source fails: the page says so, the
	// member is told by mail.
	mails := r.mail.Count("m@example.org")
	if !r.eng.Fail(st.EngineID(), a.EngineID(), "the detector crashed") {
		t.Fatal("no such analysis in the stand-in")
	}
	waitFor(t, func() bool { _, ok := r.srv.sync.failure(st.EngineID(), a.EngineID()); return ok }, "the failure to arrive")
	_, page, _ = r.get(member, aURL)
	if !strings.Contains(page, `<span class="state tone-bad">Failed</span> The analysis of this source failed in the run: the detector crashed`) || !strings.Contains(page, aURL+"/restart") {
		t.Fatal("page does not show the failure")
	}
	waitFor(t, func() bool { return r.mail.Count("m@example.org") > mails }, "the mail")
	if m, _ := r.mail.Last("m@example.org"); !strings.Contains(m.Subject, "Run "+strconv.Itoa(run.Number)+" of Night watch: Ridge failed") || !strings.Contains(m.Body, "the detector crashed") {
		t.Fatalf("mail: %+v", m)
	}

	// Starting it again puts it into the engine anew.
	if code, _, _ := r.post(member, aURL+"/restart", url.Values{"csrf": {csrf}}); code != http.StatusSeeOther {
		t.Fatalf("restart: %d", code)
	}
	waitFor(t, func() bool { _, ok := r.srv.sync.failure(st.EngineID(), a.EngineID()); return !ok }, "the analysis to run again")
	if _, page, _ = r.get(member, aURL); strings.Contains(page, "tone-bad\">Failed") {
		t.Fatal("page still shows the failure")
	}

	// The owner sets the source's view; the engine checks it.
	sURL := "/sources/" + st.Slug
	_, page, _ = r.get(member, sURL)
	if !strings.Contains(page, "Spectrogram view") || !strings.Contains(page, "16000 Hz") {
		t.Fatal("source page has no view form")
	}
	if code, _, hdr := r.post(member, sURL+"/view", url.Values{"csrf": {csrf}, "view_scale": {"linear"}, "view_max": {"20000"}}); code != http.StatusSeeOther {
		t.Fatalf("bad view: %d %v", code, hdr)
	}
	if got, _ := r.db.Stream(ctx, st.ID); got.View != nil {
		t.Fatalf("a view the engine refused was kept: %s", got.View)
	}
	if _, page, _ = r.get(member, sURL); !strings.Contains(page, "the engine refused the view (max_hz must be at most 16000 Hz") {
		t.Fatal("refusal not shown")
	}
	r.post(member, sURL+"/view", url.Values{"csrf": {csrf}, "view_scale": {"linear"}, "view_max": {"16000"}, "view_bands": {"120"}})
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	es, err := cl.Stream(ctx, st.EngineID())
	if err != nil || es.View == nil || es.View.Scale != "linear" || es.View.MaxHz != 16000 || es.View.Bands != 120 {
		t.Fatalf("engine's view: %v %+v", err, es.View)
	}
	if _, page, _ = r.get(member, sURL); !strings.Contains(page, "In effect: linear, 0 to 16000 Hz in 120 bands") {
		t.Fatal("view in effect not shown")
	}
	// Back to each analysis's bands.
	r.post(member, sURL+"/view", url.Values{"csrf": {csrf}, "view_scale": {""}})
	if es, _ := cl.Stream(ctx, st.EngineID()); es.View != nil {
		t.Fatalf("view not removed: %+v", es.View)
	}
}

func TestReservedInputs(t *testing.T) {
	list, err := ParseReserved(strings.NewReader("# the Observatory's sources\nobs.example.org\nhttps://radio.example.net/live.mp3\n\n"))
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %v %v", list, err)
	}
	r := newRig(t)
	r.srv.cfg.Reserved = list
	ctx := context.Background()
	r.db.CreateUser(ctx, "m@example.org", "Mira", false)
	member := r.loginAs("m@example.org")
	_, page, _ := r.get(member, "/sources/new")
	csrf := csrfOf(t, page)
	for in, refused := range map[string]bool{
		"https://obs.example.org/any":         true,
		"http://radio.example.net/live.mp3":   true,
		"https://radio.example.net/other.mp3": false,
	} {
		code, page, _ := r.post(member, "/sources/new", url.Values{"csrf": {csrf}, "input": {in}})
		if got := code == http.StatusConflict && strings.Contains(page, "Listening Observatory listens to this stream already"); got != refused {
			t.Fatalf("%s: %d, refused %v", in, code, got)
		}
	}

	// A source registered before its address was reserved stays out of
	// the engine, and its pages say why.
	u, _ := r.db.UserByEmail(ctx, "m@example.org")
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://obs.example.org/vlf", SampleRate: 32000,
		Texts: catalogue.StreamTexts{Name: "Station"}, Probe: []byte(`{}`)})
	r.db.CreateAnalysis(ctx, u.ID, "Watch", "", []int64{st.ID}, nil)
	if err := r.srv.sync.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	if _, err := cl.Stream(ctx, st.EngineID()); !engine.IsCode(err, "not_found") {
		t.Fatalf("a reserved source is in the engine: %v", err)
	}
	if _, page, _ := r.get(member, "/sources/"+st.Slug); !strings.Contains(page, "It is not connected to from the Workbench") {
		t.Fatal("source page does not say why")
	}
}

func waitFor(t *testing.T, ok func() bool, what string) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("waited in vain for %s", what)
		}
	}
}

// A preview listens for its ten minutes even with no page open, as when it
// is started through MCP: its stream is continuous meanwhile.
func TestPreviewKeepsStreamConnected(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "Mira", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "Watch", "", []int64{st.ID}, nil)
	r.srv.sync.reconcile(ctx)
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	mode := func() string { es, _ := cl.Stream(ctx, st.EngineID()); return es.Mode }
	if m := mode(); m != engine.OnDemand {
		t.Fatalf("before: %s", m)
	}
	member := r.loginAs("m@example.org")
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	_, page, _ := r.get(member, aURL)
	csrf := csrfOf(t, page)
	r.post(member, aURL+"/preview", url.Values{"csrf": {csrf}})
	if m := mode(); m != engine.Continuous {
		t.Fatalf("during the preview: %s", m)
	}
	r.post(member, aURL+"/preview/end", url.Values{"csrf": {csrf}})
	if m := mode(); m != engine.OnDemand {
		t.Fatalf("after the preview: %s", m)
	}
}

// When the engine cannot reach listen-ingest, the pages say that the
// platform's connection service is down, not that a station failed.
func TestIngestUnavailable(t *testing.T) {
	r := newRig(t)
	if _, problem, _ := r.srv.sync.status(); problem != "" {
		t.Fatalf("problem before: %q", problem)
	}
	r.srv.sync.mu.Lock()
	r.srv.sync.streams["s-0001"] = engine.Stream{ID: "s-0001", State: "reconnecting",
		LastError: &engine.Problem{Code: engine.CodeIngestUnavailable, Message: "listen-ingest does not answer"}}
	r.srv.sync.mu.Unlock()
	ready, problem, _ := r.srv.sync.status()
	if !ready || !strings.Contains(problem, "listen-ingest") {
		t.Fatalf("ready %v, problem %q", ready, problem)
	}
	ctx := context.Background()
	r.db.CreateUser(ctx, "m@example.org", "Mira", false)
	if _, page, _ := r.get(r.loginAs("m@example.org"), "/"); !strings.Contains(page, "connection service (listen-ingest) does not answer") {
		t.Fatal("no banner")
	}
}

// Previews and trials survive a restart of the Workbench (a deploy): they
// are kept in the catalogue and taken up again.
func TestPreviewAndTrialSurviveRestart(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "Mira", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "Watch", "", []int64{st.ID}, nil)
	b, _ := r.db.CreateAnalysis(ctx, u.ID, "Other", "", []int64{st.ID}, nil)
	until := time.Now().Add(8 * time.Minute).Truncate(time.Second)
	r.srv.sync.startPreview(a.ID, until)
	r.srv.sync.startTrial(b.ID, map[int64]json.RawMessage{st.ID: json.RawMessage(`{"settings":{"min_hz":100}}`)})

	// A second Workbench on the same catalogue, as after a deploy.
	srv2, err := New(Config{BaseURL: r.web.URL, DatasetDir: t.TempDir()}, r.db, engine.NewClient(&http.Client{}, r.engSrv.URL), r.mail)
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()
	srv2.Start(ctx2)
	if got, ok := srv2.sync.preview(a.ID); !ok || !got.Equal(until) {
		t.Fatalf("preview after the restart: %v %v, want until %v", ok, got, until)
	}
	if tr, ok := srv2.sync.trialOf(b.ID); !ok || string(tr.Settings[st.ID]) != `{"settings":{"min_hz":100}}` {
		t.Fatalf("trial after the restart: %v %+v", ok, tr)
	}

	// Ended, they are gone from the catalogue too.
	srv2.sync.endPreview(a.ID)
	srv2.sync.endTrial(b.ID)
	if ps, _ := r.db.Previews(ctx); len(ps) != 0 {
		t.Fatalf("previews kept after their end: %v", ps)
	}
	if ts, _ := r.db.Trials(ctx); len(ts) != 0 {
		t.Fatalf("trials kept after their end: %v", ts)
	}
}
