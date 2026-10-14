package web

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
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
	if code, _, _ := r.post(c, u.Path, url.Values{"next": {"/account"}}); code != http.StatusSeeOther {
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

	// The master admin invites a member.
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
	if !strings.Contains(page, "Set: 1.5 cores, 512 MB") {
		t.Fatal("cap not shown")
	}
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
	if code, _, _ := r.get(member, inv.Path); code != 200 {
		t.Fatalf("invite page: %d", code)
	}
	if code, page, _ := r.post(member, inv.Path, url.Values{"name": {"Researcher"}}); code != 400 || !strings.Contains(page, "confirm") {
		t.Fatalf("accept without privacy note: %d", code)
	}
	if code, _, _ := r.post(member, inv.Path, url.Values{"name": {"Researcher"}, "privacy": {"on"}}); code != http.StatusSeeOther {
		t.Fatalf("accept: %d", code)
	}
	if code, _, _ := r.get(member, inv.Path); code != 400 {
		t.Fatalf("invitation reused: %d", code)
	}

	// Register a stream: probe, then save.
	_, page, _ = r.get(member, "/account/streams/new")
	csrf := csrfOf(t, page)
	input := "https://stream.example.org/live.ogg"
	code, page, _ = r.post(member, "/account/streams/new", url.Values{"csrf": {csrf}, "input": {input}, "action": {"probe"}})
	if code != 200 || !strings.Contains(page, "The stream answers") || !strings.Contains(page, `value="32000"`) || strings.Contains(page, `value="44100"`) {
		t.Fatalf("probe: %d\n%s", code, page)
	}
	form := url.Values{"csrf": {csrf}, "input": {input}, "action": {"save"}, "sample_rate": {"22050"},
		"name": {"Garden Mic"}, "place": {"Somewhere"}, "timezone": {"Europe/Berlin"}}
	if code, page, _ = r.post(member, "/account/streams/new", form); code != 400 || !strings.Contains(page, "rule 4") {
		t.Fatalf("save without attestation: %d", code)
	}
	form.Set("attest", "on")
	form.Set("coords", "49.4, 200")
	if code, page, _ = r.post(member, "/account/streams/new", form); code != 400 || !strings.Contains(page, "decimal degrees") {
		t.Fatalf("bad coordinates: %d", code)
	}
	form.Set("coords", "49,41234; 8,69121")
	code, _, hdr := r.post(member, "/account/streams/new", form)
	if code != http.StatusSeeOther {
		t.Fatalf("save: %d", code)
	}
	analysisURL := hdr.Get("Location")
	if !strings.HasPrefix(analysisURL, "/account/streams/garden-mic/analyses/") {
		t.Fatalf("redirect to %q", analysisURL)
	}

	if st, _ := r.db.StreamBySlug(ctx, "garden-mic"); st.Texts.Lat == nil || *st.Texts.Lat != 49.41234 || *st.Texts.Lon != 8.69121 {
		t.Fatalf("coordinates not stored: %+v", st.Texts)
	}
	if _, page, _ := r.get(member, "/account/streams/garden-mic"); !strings.Contains(page, `value="49.41234, 8.69121"`) || !strings.Contains(page, "map-picker") {
		t.Fatal("coordinates or map not on the stream page")
	}
	// A second registration of the same address is pointed to the stream.
	_, apage, _ := r.get(admin, "/account/streams/new")
	if code, page, _ = r.post(admin, "/account/streams/new", url.Values{"csrf": {csrfOf(t, apage)}, "input": {input}, "action": {"probe"}}); code != http.StatusConflict || !strings.Contains(page, "registered by Researcher") {
		t.Fatalf("duplicate: %d", code)
	}

	// The analysis page, and live events once it is open.
	code, page, _ = r.get(member, analysisURL)
	if code != 200 || !strings.Contains(page, "First analysis") {
		t.Fatalf("analysis page: %d", code)
	}
	csrf = csrfOf(t, page)
	gotSpectrum := make(chan bool, 1)
	liveCtx, stopLive := context.WithCancel(context.Background())
	go func() {
		req, _ := http.NewRequestWithContext(liveCtx, http.MethodGet, r.web.URL+analysisURL+"/live", nil)
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
	_, page, _ = r.get(member, "/account/streams/garden-mic")
	if !strings.Contains(page, "1 page, 0 tokens") {
		t.Fatalf("stream page does not show the reader:\n%s", page)
	}
	stopLive()

	// Settings the engine refuses come back marked.
	code, page, _ = r.post(member, analysisURL+"/settings", url.Values{"csrf": {csrf}, "min_hz": {"9000"}, "max_hz": {"10000"},
		"memory_mode": {"disk"}, "rule_count": {"1"}, "rule_kind_0": {"line"}, "rule_hz_0": {"1000"}, "rule_odd_0": {"on"}})
	if code != 422 || !strings.Contains(page, "below half of max_hz") || !strings.Contains(page, "odd harmonics") {
		t.Fatalf("refused settings: %d\n%s", code, page)
	}
	// Good settings make revision 2 and reach the engine.
	code, _, hdr = r.post(member, analysisURL+"/settings", url.Values{"csrf": {csrf}, "memory_mode": {"disk"},
		"rule_count": {"2"}, "rule_kind_0": {"comb"}, "rule_hz_0": {"50"}, "rule_kind_1": {"comb"}})
	if code != http.StatusSeeOther {
		t.Fatalf("save settings: %d", code)
	}
	_, page, _ = r.get(member, analysisURL)
	if !strings.Contains(page, "rev-2") || !strings.Contains(page, "<code>comb=50</code>") {
		t.Fatalf("revision 2 not shown:\n%s", page)
	}

	// Export.
	code, page, hdr = r.get(member, analysisURL+"/export?format=csv")
	if code != 200 || !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") || !strings.Contains(page, "timestamp,position_s,type") {
		t.Fatalf("export: %d", code)
	}

	// Another member cannot see it.
	if code, _, _ := r.get(admin, analysisURL); code != 404 {
		t.Fatalf("admin sees a member's analysis: %d", code)
	}

	// A form without the CSRF value is refused.
	if code, _, _ := r.post(member, "/account/streams/garden-mic/state", url.Values{"to": {"pause"}}); code != http.StatusForbidden {
		t.Fatalf("missing csrf: %d", code)
	}

	// Pausing removes the stream from the engine; resuming brings it back.
	r.post(member, "/account/streams/garden-mic/state", url.Values{"csrf": {csrf}, "to": {"pause"}})
	st, _ := r.db.StreamBySlug(ctx, "garden-mic")
	cl := engine.NewClient(&http.Client{}, r.engSrv.URL)
	if _, err := cl.Stream(ctx, st.EngineID()); !engine.IsCode(err, "not_found") {
		t.Fatalf("paused stream still in the engine: %v", err)
	}
	r.post(member, "/account/streams/garden-mic/state", url.Values{"csrf": {csrf}, "to": {"resume"}})
	es, err := cl.Stream(ctx, st.EngineID())
	if err != nil || len(es.Analyses) != 1 {
		t.Fatalf("resumed stream: %v %+v", err, es)
	}

	// The audit trail has the stream's life.
	entries, _ := r.db.AuditOf(ctx, "stream", st.ID)
	if len(entries) != 3 {
		t.Fatalf("audit entries for the stream: %d", len(entries))
	}
}

// After an engine restart the Workbench sends its whole wanted state again.
func TestReconcile(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, a, err := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`), AnalysisName: "x", AnalysisSpec: []byte(`{"rules":[{"kind":"comb","hz":50}]}`)})
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
