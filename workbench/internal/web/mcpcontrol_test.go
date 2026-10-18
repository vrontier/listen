package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
)

// makeTokenWith makes a token on the Tokens page from a filled form.
func (r *rig) makeTokenWith(c *http.Client, form url.Values) string {
	r.t.Helper()
	_, page, _ := r.get(c, "/ai/access")
	form.Set("csrf", csrfOf(r.t, page))
	code, page, _ := r.post(c, "/ai/access", form)
	secret := secretRE.FindString(page)
	if code != 200 || secret == "" {
		r.t.Fatalf("token not shown: %d", code)
	}
	return secret
}

func TestMCPControl(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Ridge"}, Probe: []byte(`{}`)})
	member := r.loginAs("m@example.org")
	sid := jsonInt(st.ID)

	// A token that only reads sees no control tools and is refused them.
	reader := r.makeToken(member, "reader", "mcp")
	if _, body := r.mcpCall(reader, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); strings.Contains(body, `"start_run"`) || !strings.Contains(body, `"list_sources"`) {
		t.Fatalf("tools of a reader: %s", body)
	}
	if text, isErr := r.toolText(reader, "create_analysis", `{"title":"X1","source_ids":[`+sid+`]}`); !isErr || !strings.HasPrefix(text, "Refused: this token may only read") {
		t.Fatalf("reader creating: %s", text)
	}

	// Control without MCP is not offered.
	_, page, _ := r.get(member, "/ai/access")
	if code, page, _ := r.post(member, "/ai/access", url.Values{"csrf": {csrfOf(t, page)}, "name": {"odd"}, "stream": {"on"}, "control": {"on"}}); code != 400 || !strings.Contains(page, "tick MCP as well") {
		t.Fatalf("control without MCP: %d", code)
	}

	// A token with control and limits.
	ctl := r.makeTokenWith(member, url.Values{"name": {"assistant"}, "mcp": {"on"}, "control": {"on"},
		"run_hours": {"2"}, "runs_per_day": {"1"}, "runs_at_once": {"1"}, "previews_per_day": {"1"}, "changes_per_day": {"4"}})
	toks, _ := r.db.APITokensOf(ctx, u.ID)
	var tok catalogue.APIToken
	for _, x := range toks {
		if x.Name == "assistant" {
			tok = x
		}
	}
	if !tok.Has(catalogue.ScopeControl) || tok.Limits != (catalogue.TokenLimits{RunHours: 2, RunsPerDay: 1, RunsAtOnce: 1, PreviewsPerDay: 1, ChangesPerDay: 4}) {
		t.Fatalf("token: %+v", tok)
	}
	code, body := r.mcpCall(ctl, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if code != 200 || !strings.Contains(body, "Changing things") {
		t.Fatalf("instructions for control: %s", body)
	}
	if _, body := r.mcpCall(ctl, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); !strings.Contains(body, `"start_run"`) || !strings.Contains(body, `"destructiveHint":true`) {
		t.Fatalf("tools with control: %s", body)
	}
	if text, isErr := r.toolText(ctl, "list_sources", `{}`); isErr || !strings.Contains(text, "Ridge") {
		t.Fatalf("list_sources: %s", text)
	}

	// Making and changing an analysis.
	if text, isErr := r.toolText(ctl, "create_analysis", `{"title":"Dawn chorus","source_ids":[9999]}`); !isErr || !strings.Contains(text, "no source 9999 you may use") {
		t.Fatalf("someone else's source: %s", text)
	}
	text, isErr := r.toolText(ctl, "create_analysis", `{"title":"Dawn chorus","source_ids":[`+sid+`]}`)
	var made struct {
		ID int64 `json:"analysis_id"`
	}
	if isErr || json.Unmarshal([]byte(text), &made) != nil || made.ID == 0 {
		t.Fatalf("create_analysis: %s", text)
	}
	aid := `{"analysis_id":` + jsonInt(made.ID)
	if text, isErr := r.toolText(ctl, "update_source_settings", aid+`,"source_id":`+sid+`,"min_hz":-5}`); !isErr || !strings.Contains(text, "engine refused") || !strings.Contains(text, "settings.min_hz") {
		t.Fatalf("settings the engine refuses: %s", text)
	}
	if text, isErr := r.toolText(ctl, "update_source_settings", aid+`,"source_id":`+sid+`,"min_hz":100,"rules":[{"kind":"line","hz":50,"tol_hz":1}]}`); isErr || !strings.Contains(text, `"revision": 2`) {
		t.Fatalf("update_source_settings: %s", text)
	}
	a, _ := r.db.Analysis(ctx, made.ID)
	if !strings.Contains(string(a.Sources[0].Spec), `"min_hz":100`) || !strings.Contains(string(a.Sources[0].Spec), `"hz":50`) {
		t.Fatalf("settings saved: %s", a.Sources[0].Spec)
	}
	if text, isErr := r.toolText(ctl, "update_source_settings", aid+`,"source_id":`+sid+`,"min_hz":100}`); isErr || !strings.Contains(text, "Nothing changed") {
		t.Fatalf("the same again: %s", text)
	}

	// Previews: one a day.
	if text, isErr := r.toolText(ctl, "start_preview", aid+`}`); isErr || !strings.Contains(text, "preview_until") {
		t.Fatalf("start_preview: %s", text)
	}
	if text, isErr := r.toolText(ctl, "stop_preview", aid+`}`); isErr {
		t.Fatalf("stop_preview: %s", text)
	}
	if text, isErr := r.toolText(ctl, "start_preview", aid+`}`); !isErr || !strings.Contains(text, "1 previews a day") {
		t.Fatalf("second preview: %s", text)
	}

	// Runs: at most two hours, one a day; the member is told.
	if text, isErr := r.toolText(ctl, "start_run", aid+`}`); !isErr || !strings.Contains(text, "at most 2 hours; give hours") {
		t.Fatalf("run without hours: %s", text)
	}
	if text, isErr := r.toolText(ctl, "start_run", aid+`,"hours":3}`); !isErr || !strings.HasPrefix(text, "Refused:") {
		t.Fatalf("run too long: %s", text)
	}
	mails := r.mail.Count("m@example.org")
	text, isErr = r.toolText(ctl, "start_run", aid+`,"hours":1.5}`)
	if isErr || !strings.Contains(text, `"run_id"`) {
		t.Fatalf("start_run: %s", text)
	}
	run, inRun, _ := r.db.ActiveRun(ctx, made.ID)
	if !inRun || run.Token != "assistant" || run.Ends.Sub(run.Started).Round(time.Minute) != 90*time.Minute || run.OfflineStop != 15 {
		t.Fatalf("run: %+v", run)
	}
	for deadline := time.Now().Add(2 * time.Second); r.mail.Count("m@example.org") == mails; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("no mail about the run")
		}
	}
	if m, _ := r.mail.Last("m@example.org"); !strings.Contains(m.Subject, "A token started run") || !strings.Contains(m.Body, `"assistant"`) {
		t.Fatalf("mail: %+v", m)
	}
	if text, isErr := r.toolText(ctl, "set_sources", aid+`,"source_ids":[`+sid+`]}`); !isErr || !strings.Contains(text, "fixed while its run lasts") {
		t.Fatalf("changing during a run: %s", text)
	}
	if text, isErr := r.toolText(ctl, "start_run", aid+`,"hours":1}`); !isErr || !strings.HasPrefix(text, "Refused:") {
		t.Fatalf("second run: %s", text)
	}
	if text, isErr := r.toolText(ctl, "stop_run", aid+`}`); isErr || !strings.Contains(text, `"stopped": true`) {
		t.Fatalf("stop_run: %s", text)
	}
	r.srv.data.busy.Wait()
	if text, isErr := r.toolText(ctl, "stop_run", aid+`}`); !isErr || !strings.Contains(text, "not running") {
		t.Fatalf("stop_run again: %s", text)
	}

	// Changes: four a day. Made so far: the analysis, its settings, and
	// the unchanged save (it succeeded); refused and failed calls do not
	// count.
	if text, isErr := r.toolText(ctl, "update_analysis", aid+`,"title":"Dawn"}`); isErr || !strings.Contains(text, `"saved": true`) {
		t.Fatalf("fourth change: %s", text)
	}
	if text, isErr := r.toolText(ctl, "update_analysis", aid+`,"title":"Dusk"}`); !isErr || !strings.Contains(text, "4 changes to analyses a day") {
		t.Fatalf("fifth change: %s", text)
	}

	// The audit trail and the activity say what the token did.
	audit, _ := r.db.AuditOf(ctx, "analysis", made.ID)
	var byToken []string
	for _, e := range audit {
		if strings.HasPrefix(e.Action, "by token: ") {
			byToken = append(byToken, e.Action)
		}
	}
	if len(byToken) != 7 { // create, settings, preview, stop preview, run, stop run, title
		t.Fatalf("audit: %v", byToken)
	}
	notes, _ := r.db.AccessOf(ctx, u.ID, catalogue.AccessFilter{Limit: 100})
	refusedN := 0
	for _, n := range notes {
		if n.Status == catalogue.AccessRefused {
			refusedN++
		}
	}
	if refusedN < 5 {
		t.Fatalf("%d refused notes", refusedN)
	}
	_, page, _ = r.get(member, "/ai/activity")
	if !strings.Contains(page, `data-kind="change"`) {
		t.Fatal("activity does not mark changes")
	}
	if code, page, _ := r.get(member, "/runs/"+jsonInt(run.ID)); !strings.Contains(page, `through the token`) || !strings.Contains(page, `assistant`) {
		i := max(strings.Index(page, "Started"), 0)
		t.Fatalf("run page does not name the token: %d %q", code, page[i:min(len(page), i+300)])
	}

	// Without limits, a run may last until stopped; the limits are changed
	// on the Tokens page.
	_, page, _ = r.get(member, "/ai/access")
	form := url.Values{"csrf": {csrfOf(t, page)}, "mcp": {"on"}, "control": {"on"}}
	if code, _, _ := r.post(member, "/ai/access/"+jsonInt(tok.ID), form); code != http.StatusSeeOther && code != 200 {
		t.Fatalf("change token: %d", code)
	}
	toks, _ = r.db.APITokensOf(ctx, u.ID)
	for _, x := range toks {
		if x.ID == tok.ID && !x.Limits.Unlimited() {
			t.Fatalf("limits not removed: %+v", x.Limits)
		}
	}
	if text, isErr := r.toolText(ctl, "start_run", aid+`}`); isErr || !strings.Contains(text, `"ends": "when stopped"`) {
		t.Fatalf("unlimited run: %s", text)
	}
	if _, page, _ := r.get(member, "/"); !strings.Contains(page, "1 with control") || !strings.Contains(page, "1 run started by a token going on") || !strings.Contains(page, "never registers, publishes or deletes") {
		t.Fatal("Home does not tell of control")
	}
	if _, page, _ := r.get(member, "/runs"); !strings.Contains(page, `class="stop-x"`) || !strings.Contains(page, `data-confirm="Stop run`) || !strings.Contains(page, `id="confirm"`) {
		t.Fatal("Runs page: no stop button that asks first")
	}
	if text, isErr := r.toolText(ctl, "stop_run", aid+`}`); isErr {
		t.Fatalf("stop: %s", text)
	}
	r.srv.data.busy.Wait()
}
