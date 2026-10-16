package web

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

func TestActivity(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	r.db.CreateUser(ctx, "o@example.org", "O", false)
	both, _, _ := r.db.CreateAPIToken(ctx, u.ID, "laptop", []string{catalogue.ScopeStream, catalogue.ScopeMCP}, catalogue.TokenLimits{})
	mcpOnly, _, _ := r.db.CreateAPIToken(ctx, u.ID, "assistant", []string{catalogue.ScopeMCP}, catalogue.TokenLimits{})

	r.mcpCall(both, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"test-client","version":"0.1"}}}`)
	r.mcpCall(both, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	r.mcpCall(both, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if _, isErr := r.toolText(both, "get_analysis", `{"analysis_id":999}`); !isErr {
		t.Fatal("a missing analysis was found")
	}
	r.toolText(both, "list_runs", `{}`)
	streamGet := func(secret, path string) int {
		req, _ := http.NewRequest(http.MethodGet, r.web.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		req.Header.Set("User-Agent", "test-program/1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := streamGet(both, "/api/v1/analyses"); code != 200 {
		t.Fatalf("analyses: %d", code)
	}
	if code := streamGet(both, "/api/v1/runs/999/events.csv"); code != 404 {
		t.Fatalf("missing run: %d", code)
	}
	if code := streamGet(mcpOnly, "/api/v1/analyses"); code != 403 {
		t.Fatalf("token for MCP on the data stream: %d", code)
	}

	rows, err := r.db.AccessOf(ctx, u.ID, catalogue.AccessFilter{})
	if err != nil {
		t.Fatal(err)
	}
	// Newest first: refused, missing run, analyses, list_runs, get_analysis,
	// tools/list, notifications/initialized, initialize.
	want := []struct{ via, what, status string }{
		{"stream", "GET /api/v1/analyses", "refused"},
		{"stream", "GET /api/v1/runs/999/events.csv", "error"},
		{"stream", "GET /api/v1/analyses", "ok"},
		{"mcp", "list_runs", "ok"},
		{"mcp", "get_analysis", "error"},
		{"mcp", "tools/list", "ok"},
		{"mcp", "notifications/initialized", "ok"},
		{"mcp", "initialize", "ok"},
	}
	if len(rows) != len(want) {
		t.Fatalf("%d notes: %+v", len(rows), rows)
	}
	for i, w := range want {
		if rows[i].Via != w.via || rows[i].What != w.what || rows[i].Status != w.status {
			t.Fatalf("note %d: %s %s %s, want %v", i, rows[i].Via, rows[i].What, rows[i].Status, w)
		}
	}
	if rows[7].Client != "test-client 0.1" || rows[4].Detail != `{"analysis_id":999}` || rows[2].Client != "test-program/1" || rows[2].Bytes == 0 {
		t.Fatalf("details: %+v %+v %+v", rows[7], rows[4], rows[2])
	}
	if !strings.Contains(rows[4].Note, "no analysis 999") || !strings.HasPrefix(rows[1].Note, "404") {
		t.Fatalf("notes: %q %q", rows[4].Note, rows[1].Note)
	}

	// The page shows it, filtered; the secrets are nowhere.
	member := r.loginAs("m@example.org")
	_, page, _ := r.get(member, "/account/mcp")
	for _, s := range []string{"<h1>MCP</h1>", "laptop", "assistant", "get_analysis", "{&#34;analysis_id&#34;:999}", "test-client 0.1", `href="/account/mcp"`,
		"read_dataset_file", "Always start with list_analyses", "claude mcp add"} {
		if !strings.Contains(page, s) {
			t.Fatalf("page lacks %q", s)
		}
	}
	if strings.Contains(page, both) || strings.Contains(page, mcpOnly) {
		t.Fatal("a secret is on the page")
	}
	// Each access over two lines, with what the browser filters and sorts by.
	log := page[strings.Index(page, `id="accesses"`):strings.Index(page, `id="tools"`)] // the tools are listed below
	for _, s := range []string{`class="log-scroll"`, `data-what="get_analysis" data-kind="read" data-status="error"`, `data-via="stream"`, `class="l2"`,
		`data-key="ms"`, "mcplog.js", "There is no analysis 999 of yours"} {
		if !strings.Contains(log, s) && !strings.Contains(page, s) {
			t.Fatalf("log lacks %q", s)
		}
	}
	if n := strings.Count(log, `<tbody class="entry"`); n != len(want) {
		t.Fatalf("%d entries, want %d", n, len(want))
	}

	// The old address leads here, with a chosen token.
	if code, _, hdr := r.get(member, "/account/activity"); code != http.StatusMovedPermanently || hdr.Get("Location") != "/account/mcp" {
		t.Fatalf("old address: %d", code)
	}
	if _, _, hdr := r.get(member, "/account/activity?token=1"); hdr.Get("Location") != "/account/mcp?token=1" {
		t.Fatalf("old address with a token: %s", hdr.Get("Location"))
	}

	// Another member sees none of it.
	other := r.loginAs("o@example.org")
	if _, page, _ := r.get(other, "/account/mcp"); strings.Contains(page, "laptop") || !strings.Contains(page, "No token has been used") {
		t.Fatal("another member sees the activity")
	}
}
