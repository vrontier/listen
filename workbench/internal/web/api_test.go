package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
)

var secretRE = regexp.MustCompile(`low_[A-Za-z0-9_-]{20,}`)

// makeToken makes a token on the Tokens page and returns its secret.
func (r *rig) makeToken(c *http.Client, name string, scopes ...string) string {
	r.t.Helper()
	_, page, _ := r.get(c, "/ai/access")
	form := url.Values{"csrf": {csrfOf(r.t, page)}, "name": {name}}
	for _, s := range scopes {
		form.Set(s, "on")
	}
	code, page, _ := r.post(c, "/ai/access", form)
	secret := secretRE.FindString(page)
	if code != 200 || secret == "" {
		r.t.Fatalf("token not shown: %d", code)
	}
	return secret
}

func (r *rig) api(secret, path string) (int, string) {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, r.web.URL+path, nil)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestTokensAndDataStream(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	r.db.CreateUser(ctx, "o@example.org", "O", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "Night watch", "", []int64{st.ID}, nil)
	member := r.loginAs("m@example.org")
	secret := r.makeToken(member, "laptop", "stream")
	mcpOnly := r.makeToken(member, "assistant", "mcp")
	other := r.makeToken(r.loginAs("o@example.org"), "theirs", "stream")

	if code, _ := r.api("", "/data/v1/analyses"); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, body := r.api("nope", "/data/v1/analyses"); code != http.StatusUnauthorized || !strings.Contains(body, `"invalid_token"`) || !strings.Contains(body, "AI, Access") {
		t.Fatalf("not a Workbench token: %d %s", code, body)
	}
	if code, body := r.api(secret+"x", "/data/v1/analyses"); code != http.StatusUnauthorized || !strings.Contains(body, `"invalid_token"`) {
		t.Fatalf("unknown token: %d %s", code, body)
	}
	if code, _ := r.api("", "/data/v1/analyses?token="+secret); code != http.StatusBadRequest {
		t.Fatalf("token in the address: %d", code)
	}
	if code, _ := r.api(mcpOnly, "/data/v1/analyses"); code != http.StatusForbidden {
		t.Fatalf("MCP token on the data stream: %d", code)
	}
	code, body := r.api(secret, "/data/v1/analyses")
	if code != 200 || !strings.Contains(body, `"title": "Night watch"`) || !strings.Contains(body, `"state": "idle"`) || !strings.Contains(body, `"steward": "M"`) {
		t.Fatalf("analyses: %d %s", code, body)
	}
	aPath := "/data/v1/analyses/" + strconv.FormatInt(a.ID, 10)
	if code, _ := r.api(other, aPath); code != http.StatusNotFound {
		t.Fatalf("someone else's analysis: %d", code)
	}
	if code, _ := r.api(secret, aPath+"/events"); code != http.StatusConflict {
		t.Fatalf("events outside a run: %d", code)
	}

	// In a run, the events come, each naming its source.
	_, page, _ := r.get(member, "/analyses/"+strconv.FormatInt(a.ID, 10))
	csrf := csrfOf(t, page)
	r.post(member, "/analyses/"+strconv.FormatInt(a.ID, 10)+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}})
	if code, body := r.api(secret, aPath+"/events?types=bogus"); code != http.StatusBadRequest || !strings.Contains(body, `\"bogus\" is not an event type`) {
		t.Fatalf("unknown type: %d %s", code, body)
	}
	if code, body := r.api(secret, aPath+"/events?source=9999"); code != http.StatusBadRequest || !strings.Contains(body, "bad_source") {
		t.Fatalf("someone else's source: %d %s", code, body)
	}
	req, _ := http.NewRequest(http.MethodGet, r.web.URL+aPath+"/events?types=signal.frame", nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("events: %v %v", err, resp)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var got struct {
		Source int64           `json:"source"`
		Data   json.RawMessage `json:"data"`
	}
	event := ""
	if !sc.Scan() || sc.Text() != ": connected to Night watch; only signal.frame" {
		t.Fatalf("first line: %q", sc.Text())
	}
	for sc.Scan() {
		line := sc.Text()
		if v, ok := strings.CutPrefix(line, "event: "); ok {
			event = v
		}
		if v, ok := strings.CutPrefix(line, "data: "); ok {
			if event != "signal.frame" {
				t.Fatalf("type not limited: %s", event)
			}
			if err := json.Unmarshal([]byte(v), &got); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if got.Source != st.ID || len(got.Data) == 0 {
		t.Fatalf("event: %+v", got)
	}
	// The source's page counts the token among its readers.
	_, page, _ = r.get(member, "/sources/"+st.Slug)
	if !strings.Contains(page, "1 token") {
		t.Fatal("token reader not counted")
	}
	if code, body := r.api(secret, aPath+"/sources/"+strconv.FormatInt(st.ID, 10)+"/state"); code != 200 {
		t.Fatalf("state: %d %s", code, body)
	}

	// Stopping the run ends the stream with an "end" event.
	r.post(member, "/analyses/"+strconv.FormatInt(a.ID, 10)+"/stop", url.Values{"csrf": {csrf}})
	done := make(chan bool)
	go func() {
		for sc.Scan() {
			if sc.Text() == "event: end" {
				done <- true
				return
			}
		}
		done <- false
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("stream closed without an end event")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no end event")
	}

	code, body = r.api(secret, "/data/v1/runs")
	if code != 200 || !strings.Contains(body, `"number": 1`) {
		t.Fatalf("runs: %d %s", code, body)
	}

	// A revoked token is refused.
	toks, _ := r.db.APITokensOf(ctx, u.ID)
	for _, tk := range toks {
		if tk.Name == "laptop" {
			_, page, _ := r.get(member, "/ai/access")
			r.post(member, "/ai/access/"+strconv.FormatInt(tk.ID, 10)+"/revoke", url.Values{"csrf": {csrfOf(t, page)}})
			if tk.LastUsed.IsZero() {
				t.Fatal("last use not noted")
			}
		}
	}
	if code, _ := r.api(secret, "/data/v1/analyses"); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
}

// The earlier addresses forward: the production web server refuses paths
// that begin with admin or api.
func TestRenamedPaths(t *testing.T) {
	r := newRig(t)
	c := r.browser()
	for from, want := range map[string]struct {
		code int
		to   string
	}{
		"/admin":                              {http.StatusMovedPermanently, "/manage"},
		"/api/v1/analyses":                    {http.StatusPermanentRedirect, "/data/v1/analyses"},
		"/api/v1/analyses/3/events?types=a,b": {http.StatusPermanentRedirect, "/data/v1/analyses/3/events?types=a,b"},
	} {
		if code, _, hdr := r.get(c, from); code != want.code || hdr.Get("Location") != want.to {
			t.Fatalf("%s: %d %s", from, code, hdr.Get("Location"))
		}
	}
}
