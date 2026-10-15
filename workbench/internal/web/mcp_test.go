package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

func (r *rig) mcpCall(secret, body string, header ...string) (int, string) {
	r.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, r.web.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// toolText calls a tool and returns its text and whether it is an error.
func (r *rig) toolText(secret, name, args string) (string, bool) {
	r.t.Helper()
	code, body := r.mcpCall(secret, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"`+name+`","arguments":`+args+`}}`)
	var resp struct {
		Result struct {
			Content []struct{ Text string }
			IsError bool
		}
		Error *rpcError
	}
	if code != 200 || json.Unmarshal([]byte(body), &resp) != nil || resp.Error != nil || len(resp.Result.Content) != 1 {
		r.t.Fatalf("%s: %d %s", name, code, body)
	}
	return resp.Result.Content[0].Text, resp.Result.IsError
}

func TestMCP(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "Night watch", "", []int64{st.ID}, nil)
	member := r.loginAs("m@example.org")
	secret := r.makeToken(member, "assistant", "mcp")
	streamOnly := r.makeToken(member, "program", "stream")

	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	if code, _ := r.mcpCall("", init); code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := r.mcpCall(streamOnly, init); code != http.StatusForbidden {
		t.Fatalf("data-stream token: %d", code)
	}
	if code, _ := r.mcpCall(secret, init, "Origin", "https://elsewhere.example"); code != http.StatusForbidden {
		t.Fatalf("other origin: %d", code)
	}
	code, body := r.mcpCall(secret, init)
	if code != 200 || !strings.Contains(body, `"protocolVersion":"2025-06-18"`) || !strings.Contains(body, `"tools"`) {
		t.Fatalf("initialize: %d %s", code, body)
	}
	if code, _ := r.mcpCall(secret, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, "MCP-Protocol-Version", "2025-06-18"); code != http.StatusAccepted {
		t.Fatalf("notification: %d", code)
	}
	if code, _ := r.mcpCall(secret, `{}`, "MCP-Protocol-Version", "1999-01-01"); code != http.StatusBadRequest {
		t.Fatalf("unknown version: %d", code)
	}
	if _, body := r.mcpCall(secret, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`); !strings.Contains(body, `"read_dataset_file"`) {
		t.Fatalf("tools/list: %s", body)
	}
	if _, body := r.mcpCall(secret, `{"jsonrpc":"2.0","id":3,"method":"resources/list"}`); !strings.Contains(body, `-32601`) {
		t.Fatalf("unknown method: %s", body)
	}
	if code, _, _ := r.get(r.browser(), "/mcp"); code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", code)
	}

	if text, isErr := r.toolText(secret, "list_analyses", `{}`); isErr || !strings.Contains(text, "Night watch") {
		t.Fatalf("list_analyses: %s", text)
	}
	ids := `{"analysis_id":` + jsonInt(a.ID) + `,"source_id":` + jsonInt(st.ID) + `}`
	if text, isErr := r.toolText(secret, "get_state", ids); !isErr || !strings.Contains(text, "neither in a run") {
		t.Fatalf("get_state outside a run: %s", text)
	}
	if _, isErr := r.toolText(secret, "get_analysis", `{"analysis_id":9999}`); !isErr {
		t.Fatal("someone else's analysis")
	}

	// In a run the engine's data is there; after it, the data set.
	run, _ := r.db.StartRun(ctx, a.ID, u.ID, time.Time{})
	r.srv.pushAll(ctx, a.Sources)
	r.srv.data.refresh(ctx)
	if text, isErr := r.toolText(secret, "get_state", ids); isErr || !strings.Contains(text, "{") {
		t.Fatalf("get_state in a run: %s", text)
	}
	path := filepath.Join(r.srv.data.runDir(run.ID), st.EngineID()+".jsonl")
	for deadline := time.Now().Add(8 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if b, _ := os.ReadFile(path); len(b) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("nothing recorded")
		}
	}
	r.db.StopRun(ctx, a.ID, u.ID)
	r.srv.data.refresh(ctx)
	r.srv.data.busy.Wait()
	if text, isErr := r.toolText(secret, "list_runs", `{}`); isErr || !strings.Contains(text, `"dataset": "ready"`) {
		t.Fatalf("list_runs: %s", text)
	}
	text, isErr := r.toolText(secret, "read_dataset_file", `{"run_id":`+jsonInt(run.ID)+`,"file":"events.csv","lines":1}`)
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if isErr || len(lines) < 2 || !strings.Contains(lines[0], "source") {
		t.Fatalf("read_dataset_file: %s", text)
	}
	if text, isErr := r.toolText(secret, "get_dataset", `{"run_id":`+jsonInt(run.ID)+`}`); isErr || !strings.Contains(text, `"summary"`) {
		t.Fatalf("get_dataset: %s", text)
	}
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }
