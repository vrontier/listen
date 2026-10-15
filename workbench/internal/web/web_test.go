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
	r.srv, err = New(Config{BaseURL: ws.URL}, db, engine.NewClient(&http.Client{}, es.URL), m)
	if err != nil {
		t.Fatal(err)
	}
	ws.Config.Handler = r.srv
	r.web = ws
	ctx, cancel := context.WithCancel(context.Background())
	r.srv.Start(ctx)
	t.Cleanup(func() {
		cancel()
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
	form.Set("attest", "on")
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
	_, page, _ = r.get(member, "/sources/garden-mic")
	if !strings.Contains(page, `value="50.12345, 10.54321"`) || !strings.Contains(page, "map-picker") || !strings.Contains(page, "No analysis uses this source yet") {
		t.Fatal("source page")
	}

	// A second registration of the same address is pointed to the source.
	_, apage, _ := r.get(admin, "/sources/new")
	if code, page, _ = r.post(admin, "/sources/new", url.Values{"csrf": {csrfOf(t, apage)}, "input": {input}, "action": {"probe"}}); code != http.StatusConflict || !strings.Contains(page, "registered by Researcher") {
		t.Fatalf("duplicate: %d", code)
	}

	// A second source.
	input2 := "https://second.example.org/live.ogg"
	r.post(member, "/sources/new", url.Values{"csrf": {csrf}, "input": {input2}, "action": {"probe"}})
	if code, _, _ := r.post(member, "/sources/new", url.Values{"csrf": {csrf}, "input": {input2}, "action": {"save"}, "sample_rate": {"32000"},
		"name": {"Hill Receiver"}, "timezone": {"UTC"}, "attest": {"on"}}); code != http.StatusSeeOther {
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
	for _, st := range []catalogue.Stream{garden, hill} {
		if _, err := cl.Analysis(ctx, st.EngineID(), a.EngineID()); err != nil {
			t.Fatalf("engine analysis on %s: %v", st.EngineID(), err)
		}
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

	// Settings per source: the engine refuses some, marked at that source.
	pg, ph := "s"+g+"_", "s"+h+"_"
	bad := url.Values{"csrf": {csrf}, "source": {g, h}, pg + "min_hz": {"9000"}, pg + "max_hz": {"10000"}, pg + "memory_mode": {"disk"},
		pg + "rule_count": {"1"}, pg + "rule_kind_0": {"line"}, pg + "rule_hz_0": {"1000"}, pg + "rule_odd_0": {"on"}, ph + "memory_mode": {"disk"}}
	if code, page, _ = r.post(member, aURL+"/settings", bad); code != 422 || !strings.Contains(page, "below half of max_hz") || !strings.Contains(page, "odd harmonics") {
		t.Fatalf("refused settings: %d\n%s", code, page)
	}
	// Good settings make revision 2 and reach the engine, per source.
	good := url.Values{"csrf": {csrf}, "source": {g, h}, pg + "memory_mode": {"disk"}, pg + "rule_count": {"2"}, pg + "rule_kind_0": {"comb"}, pg + "rule_hz_0": {"50"},
		pg + "rule_kind_1": {"comb"}, ph + "memory_mode": {"ram"}}
	if code, _, _ = r.post(member, aURL+"/settings", good); code != http.StatusSeeOther {
		t.Fatalf("save settings: %d", code)
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

	// Export names the source of every row.
	code, page, hdr = r.get(member, aURL+"/export?format=csv")
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") || !strings.Contains(page, "source,timestamp,position_s,type") {
		t.Fatalf("export: %d", code)
	}

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
	if es, err := cl.Stream(ctx, garden.EngineID()); err != nil || len(es.Analyses) != 1 {
		t.Fatalf("resumed source: %v %+v", err, es)
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
