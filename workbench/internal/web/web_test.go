package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
	"github.com/vrontier/listen-workbench/internal/mail"
	"github.com/vrontier/listen-workbench/internal/standin"
)

type rig struct {
	t      *testing.T
	db     *catalogue.DB
	mail   *mail.Memory
	eng    *standin.Engine
	engSrv *httptest.Server
	srv    *Server
	web    *httptest.Server
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := catalogue.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	e := standin.New(standin.Options{Tick: 20 * time.Millisecond})
	es := httptest.NewServer(e)
	m := &mail.Memory{}
	r := &rig{t: t, db: db, mail: m, eng: e, engSrv: es}
	ws := httptest.NewUnstartedServer(nil)
	ws.Start()
	r.srv, err = New(Config{BaseURL: ws.URL, DatasetDir: t.TempDir(), Publishing: true}, db, engine.NewClient(&http.Client{}, es.URL), m)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Handler = r.srv
	r.web = ws
	ctx, cancel := context.WithCancel(context.Background())
	r.srv.Start(ctx)
	t.Cleanup(func() {
		cancel()
		r.srv.data.close()
		ws.CloseClientConnections()
		ws.Close()
		es.CloseClientConnections()
		es.Close()
		e.Close()
		db.Close()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if ready, _, _ := r.srv.sync.status(); ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("engine never became ready")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return r
}

// browser is a client with cookies that does not follow redirects.
func (r *rig) browser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (r *rig) get(c *http.Client, path string) (int, string, http.Header) {
	r.t.Helper()
	resp, err := c.Get(r.web.URL + path)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func (r *rig) post(c *http.Client, path string, form url.Values) (int, string, http.Header) {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.web.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", r.web.URL)
	resp, err := c.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func csrfOf(t *testing.T, html string) string {
	t.Helper()
	m := csrfRE.FindStringSubmatch(html)
	if m == nil {
		t.Fatal("no csrf field in page")
	}
	return m[1]
}

var linkRE = regexp.MustCompile(`http://[^\s]+`)

func (r *rig) loginAs(email string) *http.Client {
	r.t.Helper()
	c := r.browser()
	if code, _, _ := r.post(c, "/login", url.Values{"email": {email}}); code != 200 {
		r.t.Fatalf("login form: %d", code)
	}
	msg, ok := r.mail.Last(email)
	if !ok {
		r.t.Fatal("no login mail")
	}
	u, _ := url.Parse(linkRE.FindString(msg.Body))
	if code, _, _ := r.post(c, u.Path, url.Values{"next": {"/analyses"}}); code != http.StatusSeeOther {
		r.t.Fatalf("login link: %d", code)
	}
	return c
}

func TestMemberFlow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.db.CreateUser(ctx, "admin@example.org", "Admin", true); err != nil {
		t.Fatal(err)
	}

	// An incomplete address is refused, not silently ignored.
	if code, page, _ := r.post(r.browser(), "/login", url.Values{"email": {"admin@exampleorg"}}); code != 400 || !strings.Contains(page, "not a complete email address") {
		t.Fatalf("incomplete address at login: %d", code)
	}

	// The master admin sets a cap and invites a member.
	admin := r.loginAs("admin@example.org")
	_, page, _ := r.get(admin, "/admin")
	if code, p, _ := r.post(admin, "/admin/cap", url.Values{"csrf": {csrfOf(t, page)}, "cpu_cores": {"0"}}); code != 400 || !strings.Contains(p, "above 0") {
		t.Fatalf("bad cap: %d", code)
	}
	if code, _, _ := r.post(admin, "/admin/cap", url.Values{"csrf": {csrfOf(t, page)}, "cpu_cores": {"1,5"}, "memory_mb": {"512"}}); code != http.StatusSeeOther {
		t.Fatalf("cap: %d", code)
	}
	if c, _ := r.db.ServerCap(ctx); c.CPUCores != 1.5 || c.MemoryMB != 512 {
		t.Fatalf("cap stored: %+v", c)
	}
	_, page, _ = r.get(admin, "/admin")
	code, page, _ := r.post(admin, "/admin/invitations", url.Values{"csrf": {csrfOf(t, page)}, "email": {"researcher@example.org"}})
	if code != 200 || !strings.Contains(page, "Invitation sent") {
		t.Fatalf("invite: %d", code)
	}
	msg, ok := r.mail.Last("researcher@example.org")
	if !ok {
		t.Fatal("no invitation mail")
	}
	inv, _ := url.Parse(linkRE.FindString(msg.Body))

	// The member accepts.
	member := r.browser()
	if code, page, _ := r.post(member, inv.Path, url.Values{"name": {"Researcher"}}); code != 400 || !strings.Contains(page, "confirm") {
		t.Fatalf("accept without privacy note: %d", code)
	}
	if code, _, _ := r.post(member, inv.Path, url.Values{"name": {"Researcher"}, "privacy": {"on"}}); code != http.StatusSeeOther {
		t.Fatalf("accept: %d", code)
	}
	if code, _, _ := r.get(member, inv.Path); code != 400 {
		t.Fatalf("invitation reused: %d", code)
	}

	// Register a source: probe, then save. No analysis is made.
	_, page, _ = r.get(member, "/sources/new")
	csrf := csrfOf(t, page)
	input := "https://stream.example.org/live.ogg"
	code, page, _ = r.post(member, "/sources/new", url.Values{"csrf": {csrf}, "input": {input}, "action": {"probe"}})
	if code != 200 || !strings.Contains(page, "The stream answers") || !strings.Contains(page, `value="32000"`) || strings.Contains(page, `value="44100"`) {
		t.Fatalf("probe: %d\n%s", code, page)
	}
	form := url.Values{"csrf": {csrf}, "input": {input}, "action": {"save"}, "sample_rate": {"22050"},
		"name": {"Garden Mic"}, "place": {"Somewhere"}, "timezone": {"Europe/Berlin"}}
	if code, page, _ = r.post(member, "/sources/new", form); code != 400 || !strings.Contains(page, "rule 4") {
		t.Fatalf("save without attestation: %d", code)
	}
	form.Set("attest", "operator")
	if code, page, _ = r.post(member, "/sources/new", form); code != 400 || !strings.Contains(page, "an email address or web page to reach them, up to") {
		t.Fatalf("save without the operator: %d", code)
	}
	form.Set("operator", "Garden Club, radio@example.org")
	form.Set("coords", "49.4, 200")
	if code, page, _ = r.post(member, "/sources/new", form); code != 400 || !strings.Contains(page, "decimal degrees") {
		t.Fatalf("bad coordinates: %d", code)
	}
	form.Set("coords", "50,12345; 10,54321")
	code, _, hdr := r.post(member, "/sources/new", form)
	if code != http.StatusSeeOther || hdr.Get("Location") != "/sources/garden-mic" {
		t.Fatalf("save: %d %s", code, hdr.Get("Location"))
	}
	garden, _ := r.db.StreamBySlug(ctx, "garden-mic")
	if garden.Texts.Lat == nil || *garden.Texts.Lat != 50.12345 || *garden.Texts.Lon != 10.54321 {
		t.Fatalf("coordinates not stored: %+v", garden.Texts)
	}
	if garden.Attestation != catalogue.AttestOperator || garden.Operator != "Garden Club, radio@example.org" {
		t.Fatalf("attestation not stored: %q %q", garden.Attestation, garden.Operator)
	}
	_, page, _ = r.get(member, "/sources/garden-mic")
	if !strings.Contains(page, "Run by Garden Club") {
		t.Fatal("the operator is not on the source page")
	}
	if !strings.Contains(page, `value="50.12345, 10.54321"`) || !strings.Contains(page, "map-picker") || !strings.Contains(page, "No analysis uses this source yet") {
		t.Fatal("source page")
	}

	// A second registration of the same address, in another spelling, is
	// pointed to the source.
	_, apage, _ := r.get(admin, "/sources/new")
	for _, other := range []string{input, "http://Stream.Example.org:80/live.ogg/", "HTTPS://stream.example.org:443/live.ogg#x"} {
		if code, page, _ = r.post(admin, "/sources/new", url.Values{"csrf": {csrfOf(t, apage)}, "input": {other}, "action": {"probe"}}); code != http.StatusConflict || !strings.Contains(page, "registered by Researcher") {
			t.Fatalf("duplicate %s: %d", other, code)
		}
	}

	// A second source.
	input2 := "https://second.example.org/live.ogg"
	r.post(member, "/sources/new", url.Values{"csrf": {csrf}, "input": {input2}, "action": {"probe"}})
	if code, _, _ := r.post(member, "/sources/new", url.Values{"csrf": {csrf}, "input": {input2}, "action": {"save"}, "sample_rate": {"32000"},
		"name": {"Hill Receiver"}, "timezone": {"UTC"}, "attest": {"own"}}); code != http.StatusSeeOther {
		t.Fatalf("second source: %d", code)
	}
	hill, _ := r.db.StreamBySlug(ctx, "hill-receiver")

	// An analysis of both.
	g, h := strconv.FormatInt(garden.ID, 10), strconv.FormatInt(hill.ID, 10)
	if code, page, _ := r.post(member, "/analyses/new", url.Values{"csrf": {csrf}, "name": {"Two stations"}}); code != 400 || !strings.Contains(page, "at least one source") {
		t.Fatalf("analysis without sources: %d", code)
	}
	code, _, hdr = r.post(member, "/analyses/new", url.Values{"csrf": {csrf}, "name": {"Two stations"}, "source": {g, h}})
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/analyses/") {
		t.Fatalf("new analysis: %d %s", code, hdr.Get("Location"))
	}
	aURL := hdr.Get("Location")
	code, page, _ = r.get(member, aURL)
	if code != 200 || !strings.Contains(page, "Garden Mic") || !strings.Contains(page, "Hill Receiver") || !strings.Contains(page, "Copy these settings to all sources") {
		t.Fatalf("analysis page: %d", code)
	}
	csrf = csrfOf(t, page)
	id, _ := strconv.ParseInt(strings.TrimPrefix(aURL, "/analyses/"), 10, 64)
	a, _ := r.db.Analysis(ctx, id)
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	// Outside a run the analysis is not in the engine, and there is no live
	// view.
	for _, st := range []catalogue.Stream{garden, hill} {
		if _, err := cl.Analysis(ctx, st.EngineID(), a.EngineID()); !engine.IsCode(err, "not_found") {
			t.Fatalf("engine analysis on %s before a run: %v", st.EngineID(), err)
		}
	}
	if code, _, _ := r.get(member, aURL+"/live/"+g); code != http.StatusConflict {
		t.Fatalf("live view outside a run: %d", code)
	}
	if strings.Contains(page, `class="spectrogram"`) || !strings.Contains(page, "Preview for 10 minutes") {
		t.Fatal("analysis page outside a run")
	}
	// A preview is a second engine analysis that keeps nothing.
	if code, _, _ := r.post(member, aURL+"/preview", url.Values{"csrf": {csrf}}); code != http.StatusSeeOther {
		t.Fatalf("preview: %d", code)
	}
	pid := "p-" + a.EngineID()[2:]
	if ea, err := cl.Analysis(ctx, garden.EngineID(), pid); err != nil || ea.Memory.Mode != "ram" {
		t.Fatalf("preview in the engine: %v %+v", err, ea)
	}
	if _, err := cl.Analysis(ctx, garden.EngineID(), a.EngineID()); !engine.IsCode(err, "not_found") {
		t.Fatalf("the analysis itself during a preview: %v", err)
	}
	if _, page, _ = r.get(member, aURL); !strings.Contains(page, "Nothing is recorded") || !strings.Contains(page, `class="spectrogram"`) {
		t.Fatal("analysis page during a preview")
	}
	// A run ends the preview and puts the analysis into the engine.
	if code, _, _ := r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"0"}}); code != http.StatusSeeOther {
		t.Fatalf("start: %d", code)
	}
	for _, st := range []catalogue.Stream{garden, hill} {
		if _, err := cl.Analysis(ctx, st.EngineID(), a.EngineID()); err != nil {
			t.Fatalf("engine analysis on %s in a run: %v", st.EngineID(), err)
		}
	}
	if _, err := cl.Analysis(ctx, garden.EngineID(), pid); !engine.IsCode(err, "not_found") {
		t.Fatalf("preview kept in a run: %v", err)
	}

	// Live events of one source while the page reads them; the source page
	// shows who is connected.
	gotSpectrum := make(chan bool, 1)
	liveCtx, stopLive := context.WithCancel(context.Background())
	go func() {
		req, _ := http.NewRequestWithContext(liveCtx, http.MethodGet, r.web.URL+aURL+"/live/"+g, nil)
		resp, err := member.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if sc.Text() == "event: signal.spectrum" {
				gotSpectrum <- true
				return
			}
		}
	}()
	select {
	case <-gotSpectrum:
	case <-time.After(5 * time.Second):
		t.Fatal("no live spectrum within 5 s")
	}
	_, page, _ = r.get(member, "/sources/garden-mic")
	if !strings.Contains(page, "1 page, 0 tokens") || !strings.Contains(page, "Two stations") {
		t.Fatalf("source page does not show who is connected:\n%s", page)
	}
	stopLive()

	// While the run lasts, its sources and settings are fixed.
	pg, ph := "s"+g+"_", "s"+h+"_"
	good := url.Values{"csrf": {csrf}, "source": {g, h}, pg + "memory_mode": {"disk"}, pg + "rule_count": {"2"}, pg + "rule_kind_0": {"comb"}, pg + "rule_hz_0": {"50"},
		pg + "rule_kind_1": {"comb"}, ph + "memory_mode": {"ram"}}
	if code, _, _ = r.post(member, aURL+"/settings", good); code != http.StatusSeeOther {
		t.Fatalf("save during a run: %d", code)
	}
	if a, _ = r.db.Analysis(ctx, id); a.Revision != 1 {
		t.Fatalf("saved during a run: rev %d", a.Revision)
	}
	if _, page, _ = r.get(member, aURL); !strings.Contains(page, "fixed while the run lasts") || !strings.Contains(page, "Try these settings in a trial") || strings.Contains(page, ">Save as revision") {
		t.Fatal("settings not shown as fixed during a run")
	}

	// Stopping the run freezes its data set; then the analysis leaves the
	// engine.
	code, _, hdr = r.post(member, aURL+"/stop", url.Values{"csrf": {csrf}})
	if code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/runs/") {
		t.Fatalf("stop: %d %s", code, hdr.Get("Location"))
	}
	r.srv.data.busy.Wait()
	runURL := hdr.Get("Location")
	if _, page, _ = r.get(member, runURL); !strings.Contains(page, `href="`+runURL+`/events.csv"`) || !strings.Contains(page, "Delete the data set") {
		t.Fatalf("run page without its data set:\n%s", page)
	}
	// Plain CSV: the header is the first line, which spreadsheets need.
	code, page, hdr = r.get(member, runURL+"/events.csv")
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") || !strings.HasPrefix(page, "source,timestamp,position_s,type") {
		t.Fatalf("events of the run: %d", code)
	}
	if code, page, _ = r.get(member, runURL+"/dataset.json"); code != 200 || !json.Valid([]byte(page)) || !strings.Contains(page, `"revisions"`) {
		t.Fatalf("data set JSON: %d %s", code, page)
	}
	if code, _, _ = r.get(admin, runURL); code != 404 {
		t.Fatalf("admin sees a member's run: %d", code)
	}
	if _, err := cl.Analysis(ctx, garden.EngineID(), a.EngineID()); !engine.IsCode(err, "not_found") {
		t.Fatalf("analysis kept in the engine after the run: %v", err)
	}

	// Settings per source: the engine refuses some, marked at that source.
	bad := url.Values{"csrf": {csrf}, "source": {g, h}, pg + "min_hz": {"9000"}, pg + "max_hz": {"10000"}, pg + "memory_mode": {"disk"},
		pg + "rule_count": {"1"}, pg + "rule_kind_0": {"line"}, pg + "rule_hz_0": {"1000"}, pg + "rule_odd_0": {"on"}, ph + "memory_mode": {"disk"}}
	if code, page, _ = r.post(member, aURL+"/settings", bad); code != 422 || !strings.Contains(page, "below half of max_hz") || !strings.Contains(page, "odd harmonics") {
		t.Fatalf("refused settings: %d\n%s", code, page)
	}
	// Good settings make revision 2; the next run takes them to the engine,
	// per source.
	if code, _, _ = r.post(member, aURL+"/settings", good); code != http.StatusSeeOther {
		t.Fatalf("save settings: %d", code)
	}
	if code, _, _ := r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}}); code != http.StatusSeeOther {
		t.Fatalf("second run: %d", code)
	}
	_, page, _ = r.get(member, aURL)
	if !strings.Contains(page, "rev-2") || !strings.Contains(page, "<code>comb=50</code>") {
		t.Fatal("revision 2 not shown")
	}
	if ea, _ := cl.Analysis(ctx, garden.EngineID(), a.EngineID()); len(ea.Rules) != 1 || ea.Label != "rev-2" {
		t.Fatalf("engine, first source: %+v", ea)
	}
	if ea, _ := cl.Analysis(ctx, hill.EngineID(), a.EngineID()); len(ea.Rules) != 0 || ea.Memory.Mode != "ram" {
		t.Fatalf("engine, second source: %+v", ea)
	}
	r.post(member, aURL+"/stop", url.Values{"csrf": {csrf}})
	r.srv.data.busy.Wait()

	// Taking a source out makes revision 3 and removes its engine analysis.
	if code, _, _ = r.post(member, aURL+"/settings", url.Values{"csrf": {csrf}, "source": {g}, pg + "memory_mode": {"disk"},
		pg + "rule_count": {"1"}, pg + "rule_kind_0": {"comb"}, pg + "rule_hz_0": {"50"}}); code != http.StatusSeeOther {
		t.Fatalf("remove source: %d", code)
	}
	if es, _ := cl.Stream(ctx, hill.EngineID()); len(es.Analyses) != 0 {
		t.Fatalf("second source still analysed: %+v", es.Analyses)
	}
	if a, _ = r.db.Analysis(ctx, id); a.Revision != 3 || len(a.Sources) != 1 {
		t.Fatalf("after removing a source: rev %d, %d sources", a.Revision, len(a.Sources))
	}

	// Another member cannot see it.
	if code, _, _ := r.get(admin, aURL); code != 404 {
		t.Fatalf("admin sees a member's analysis: %d", code)
	}
	// A form without the CSRF value is refused.
	if code, _, _ := r.post(member, "/sources/garden-mic/state", url.Values{"to": {"pause"}}); code != http.StatusForbidden {
		t.Fatalf("missing csrf: %d", code)
	}

	// Pausing removes the source from the engine; resuming brings it back
	// with the analysis.
	r.post(member, "/sources/garden-mic/state", url.Values{"csrf": {csrf}, "to": {"pause"}})
	if _, err := cl.Stream(ctx, garden.EngineID()); !engine.IsCode(err, "not_found") {
		t.Fatalf("paused source still in the engine: %v", err)
	}
	r.post(member, "/sources/garden-mic/state", url.Values{"csrf": {csrf}, "to": {"resume"}})
	if _, err := cl.Stream(ctx, garden.EngineID()); err != nil {
		t.Fatalf("resumed source: %v", err)
	}
	if entries, _ := r.db.AuditOf(ctx, "stream", garden.ID); len(entries) != 3 {
		t.Fatalf("audit entries for the source: %d", len(entries))
	}

	// Deleting the analysis removes it from the engine.
	if code, _, _ := r.post(member, aURL+"/delete", url.Values{"csrf": {csrf}, "confirm": {"Two stations"}}); code != http.StatusSeeOther {
		t.Fatalf("delete analysis: %d", code)
	}
	if _, err := cl.Analysis(ctx, garden.EngineID(), a.EngineID()); !engine.IsCode(err, "not_found") {
		t.Fatalf("deleted analysis still in the engine: %v", err)
	}
}

// A run keeps a draft source connected until it is stopped.
func TestRunKeepsSourceConnected(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, err := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.db.CreateAnalysis(ctx, u.ID, "Night watch", "", []int64{st.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.srv.sync.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	mode := func() string {
		es, err := cl.Stream(ctx, st.EngineID())
		if err != nil {
			t.Fatal(err)
		}
		return es.Mode
	}
	if m := mode(); m != engine.OnDemand {
		t.Fatalf("draft source before the run: %s", m)
	}
	member := r.loginAs("m@example.org")
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	_, page, _ := r.get(member, aURL)
	csrf := csrfOf(t, page)
	if code, _, _ := r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"forever"}}); code != http.StatusSeeOther {
		t.Fatalf("start with a made-up length: %d", code)
	}
	if run, ok, _ := r.db.ActiveRun(ctx, a.ID); ok {
		t.Fatalf("started with a made-up length: %+v", run)
	}
	if code, _, _ := r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"6h"}}); code != http.StatusSeeOther {
		t.Fatalf("start: %d", code)
	}
	if m := mode(); m != engine.Continuous {
		t.Fatalf("draft source in a run: %s", m)
	}
	_, page, _ = r.get(member, aURL)
	if !strings.Contains(page, `class="stop-x" title="Stop run 1"`) || !strings.Contains(page, `data-confirm="Stop run 1 of`) || !strings.Contains(page, "Run 1 since") {
		t.Fatal("running analysis page does not offer to stop")
	}
	if _, list, _ := r.get(member, "/analyses"); !strings.Contains(list, "running · private") {
		t.Fatal("analysis list does not show the run")
	}
	if code, _, _ := r.post(member, aURL+"/stop", url.Values{"csrf": {csrf}}); code != http.StatusSeeOther {
		t.Fatalf("stop: %d", code)
	}
	r.srv.data.busy.Wait()
	if m := mode(); m != engine.OnDemand {
		t.Fatalf("draft source after the run: %s", m)
	}
	_, page, _ = r.get(member, aURL)
	if !strings.Contains(page, "Start a run") || !strings.Contains(page, "stopped by M") || !strings.Contains(page, ">data set</a>") {
		t.Fatal("stopped run not listed")
	}

	// The Runs page starts and stops runs of any of the member's analyses.
	_, page, _ = r.get(member, "/runs")
	if !strings.Contains(page, "Nothing is running") || !strings.Contains(page, `<option value="`+strconv.FormatInt(a.ID, 10)+`">Night watch`) {
		t.Fatal("runs page before the start")
	}
	if code, _, hdr := r.post(member, "/runs", url.Values{"csrf": {csrf}, "analysis": {strconv.FormatInt(a.ID, 10)}, "length": {"0"}}); code != http.StatusSeeOther || hdr.Get("Location") != "/runs" {
		t.Fatalf("start from the runs page: %d %s", code, hdr.Get("Location"))
	}
	_, page, _ = r.get(member, "/runs")
	if !strings.Contains(page, "until stopped") || !strings.Contains(page, `name="back" value="runs"`) || !strings.Contains(page, "All your analyses are running") {
		t.Fatal("runs page while running")
	}
	if code, _, hdr := r.post(member, aURL+"/stop", url.Values{"csrf": {csrf}, "back": {"runs"}}); code != http.StatusSeeOther || !strings.HasPrefix(hdr.Get("Location"), "/runs/") {
		t.Fatalf("stop from the runs page: %d %s", code, hdr.Get("Location"))
	}
	other, _ := r.db.CreateUser(ctx, "o@example.org", "O", false)
	oa, _ := r.db.CreateAnalysis(ctx, other.ID, "Theirs", "", nil, nil)
	if _, _, hdr := r.post(member, "/runs", url.Values{"csrf": {csrf}, "analysis": {strconv.FormatInt(oa.ID, 10)}, "length": {"0"}}); hdr.Get("Location") != "/runs" {
		t.Fatal("start of another member's analysis")
	}
	if _, ok, _ := r.db.ActiveRun(ctx, oa.ID); ok {
		t.Fatal("another member's analysis was started")
	}
}

// An analysis with two sources is compared in an engine group, as its
// revision says; with one source left the group goes.
func TestComparison(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	var ids []int64
	var streams []catalogue.Stream
	for _, n := range []string{"a", "b"} {
		lat, lon := 50.12345, 10.54321
		st, err := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://" + n + ".example.org/x", SampleRate: 22050,
			Texts: catalogue.StreamTexts{Name: "Station " + n, Lat: &lat, Lon: &lon}, Probe: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, st.ID)
		streams = append(streams, st)
	}
	a, err := r.db.CreateAnalysis(ctx, u.ID, "Pair", "", ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.Spec.Compare, catalogue.DefaultCompare) {
		t.Fatalf("new analysis compares %v", a.Spec.Compare)
	}
	// One saved before there was a choice shows the default as chosen.
	older := a
	older.Spec.Compare = nil
	if d, err := r.srv.analysisData(httptest.NewRequest("GET", "/", nil), older); err != nil || len(d.Compare) == 0 || !d.Compare[0].Checked {
		t.Fatalf("an older analysis does not show activity as chosen: %v %+v", err, d.Compare)
	}
	if err := r.srv.sync.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	group := func() (engine.Group, bool) {
		gs, err := cl.Groups(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range gs {
			if g.ID == a.GroupID() {
				return g, true
			}
		}
		return engine.Group{}, false
	}
	if _, ok := group(); ok {
		t.Fatal("group outside a run")
	}

	member := r.loginAs("m@example.org")
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	_, page, _ := r.get(member, aURL)
	if strings.Contains(page, "compare.js") || !strings.Contains(page, `name="compare" value="activity" checked`) || !strings.Contains(page, "needs station time") {
		t.Fatal("analysis page outside a run")
	}
	csrf := csrfOf(t, page)
	p0, p1 := prefixOf(ids[0]), prefixOf(ids[1])
	form := url.Values{"csrf": {csrf}, "source": {strconv.FormatInt(ids[0], 10), strconv.FormatInt(ids[1], 10)}, "compare_form": {"1"},
		p0 + "memory_mode": {"disk"}, p1 + "memory_mode": {"disk"}}
	form["compare"] = []string{"activity", "coincidence"}
	if code, body, _ := r.post(member, aURL+"/settings", form); code != 422 || !strings.Contains(body, "needs station time") {
		t.Fatalf("coincidence without station time: %d", code)
	}
	form["compare"] = nil
	if code, body, _ := r.post(member, aURL+"/settings", form); code != 422 || !strings.Contains(body, "at least one way") {
		t.Fatalf("no comparison chosen: %d", code)
	}

	// A run compares the sources, without a page open.
	if code, _, _ := r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}}); code != http.StatusSeeOther {
		t.Fatalf("start: %d", code)
	}
	g, ok := group()
	if !ok || len(g.Members) != 2 || g.Mode != engine.Continuous || g.Label != "rev-1" || g.Members[0].Lat == nil {
		t.Fatalf("group in a run: %v %+v", ok, g)
	}
	if _, page, _ = r.get(member, aURL); !strings.Contains(page, "compare.js") || !strings.Contains(page, `data-lat="50.12345" data-lon="10.54321"`) || !strings.Contains(page, "leaflet.js") {
		t.Fatal("running analysis page lacks the comparison or its map")
	}
	if code, body, _ := r.get(member, aURL+"/compare/state"); code != 200 || !strings.Contains(body, `"group"`) {
		t.Fatalf("compare state: %d %s", code, body)
	}

	// Pausing one source leaves one running: the group goes, and the page
	// says why.
	r.post(member, "/sources/"+streams[1].Slug+"/state", url.Values{"csrf": {csrf}, "to": {"pause"}})
	if _, ok := group(); ok {
		t.Fatal("group kept with one source running")
	}
	if _, page, _ = r.get(member, aURL); !strings.Contains(page, "Fewer than two of the sources are running") {
		t.Fatal("page does not say why there is no comparison")
	}
	r.post(member, "/sources/"+streams[1].Slug+"/state", url.Values{"csrf": {csrf}, "to": {"resume"}})
	if _, ok := group(); !ok {
		t.Fatal("group not back after resuming")
	}

	// The end of the run ends the comparison, once the data set is made.
	r.post(member, aURL+"/stop", url.Values{"csrf": {csrf}})
	r.srv.data.busy.Wait()
	if _, ok := group(); ok {
		t.Fatal("group kept after the run")
	}
	runs, _ := r.db.Runs(ctx, a.ID)
	if len(runs) != 1 || runs[0].Dataset != catalogue.DatasetReady {
		t.Fatalf("data set: %+v", runs)
	}
	if code, page, _ := r.get(member, "/runs/"+strconv.FormatInt(runs[0].ID, 10)+"/comparison.csv"); code != 200 || !strings.HasPrefix(page, "type,timestamp,source,other_source") {
		t.Fatalf("comparison of the run: %d", code)
	}

	// Taking a source out ends the comparison.
	delete(form, "compare_form")
	form["source"] = []string{strconv.FormatInt(ids[0], 10)}
	if code, _, _ := r.post(member, aURL+"/settings", form); code != http.StatusSeeOther {
		t.Fatalf("one source: %d", code)
	}
	if _, ok := group(); ok {
		t.Fatal("group kept with one source")
	}
	if a, _ = r.db.Analysis(ctx, a.ID); a.Spec.Compare != nil {
		t.Fatalf("one source still compares %v", a.Spec.Compare)
	}
}

// After an engine restart the Workbench sends its whole wanted state again.
func TestReconcile(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, err := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	a, err := r.db.CreateAnalysis(ctx, u.ID, "x", "", []int64{st.ID}, map[int64]json.RawMessage{st.ID: []byte(`{"rules":[{"kind":"comb","hz":50}]}`)})
	if err != nil {
		t.Fatal(err)
	}
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	// Something the catalogue does not know.
	cl.PutStream(ctx, "s-9999", engine.StreamSpec{Input: "https://other.example.org/x", SampleRate: 22050})
	if _, err := r.db.StartRun(ctx, a.ID, u.ID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := r.srv.sync.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	ea, err := cl.Analysis(ctx, st.EngineID(), a.EngineID())
	if err != nil || ea.Label != "rev-1" || len(ea.Rules) != 1 || ea.Rules[0].Text != "comb=50" {
		t.Fatalf("analysis after reconcile: %v %+v", err, ea)
	}
	if _, err := cl.Stream(ctx, "s-9999"); !engine.IsCode(err, "not_found") {
		t.Fatalf("unknown stream not removed: %v", err)
	}
}
