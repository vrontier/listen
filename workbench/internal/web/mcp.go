package web

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// MCP over HTTP (design.md, question 8): an endpoint in the Workbench
// process, with the same tokens as the data stream, scope "mcp". It speaks
// the streamable HTTP transport in its simplest form: every request is
// answered with one JSON response, and the server sends nothing of its own
// accord, so GET is not offered. The tools read the member's own analyses;
// none starts, stops or changes anything.

var mcpVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpTool is one tool with its input schema.
type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
}

func schema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	propAnalysis = map[string]any{"type": "integer", "description": "the analysis's id, from list_analyses"}
	propSource   = map[string]any{"type": "integer", "description": "the source's id, from the analysis's sources"}
	propRun      = map[string]any{"type": "integer", "description": "the run's id, from list_runs"}
	readOnly     = map[string]any{"readOnlyHint": true, "openWorldHint": false}
)

var mcpTools = []mcpTool{
	{Name: "list_analyses", Title: "List analyses", Description: "Lists your analyses: title, description, revision, whether each is in a run or a preview, its sources (id, name, place, coordinates, sample rate, settings and filter rules) and what is compared.",
		InputSchema: schema(map[string]any{})},
	{Name: "get_analysis", Title: "Get an analysis", Description: "One analysis, as list_analyses gives it.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis}, "analysis_id")},
	{Name: "get_state", Title: "Current state of a source", Description: "The latest frame, spectrum bands, features, active resonances and motifs, recent events and memory summary of one source of an analysis. Only while the analysis is in a run or a preview.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis, "source_id": propSource}, "analysis_id", "source_id")},
	{Name: "get_motifs", Title: "Motifs of a source", Description: "The established motifs in the memory in use for one source, with how often and when each was seen. Only during a run or a preview.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis, "source_id": propSource}, "analysis_id", "source_id")},
	{Name: "get_history_events", Title: "Stored events of a source", Description: "Events stored in the memory's history for one source, oldest first. Only during a run (a preview stores nothing).",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis, "source_id": propSource,
			"from":  map[string]any{"type": "string", "description": "RFC 3339 time, UTC"},
			"to":    map[string]any{"type": "string", "description": "RFC 3339 time, UTC"},
			"type":  map[string]any{"type": "string", "description": "one event type, e.g. event.resonance"},
			"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}}, "analysis_id", "source_id")},
	{Name: "get_comparison", Title: "Comparison of the sources", Description: "From two sources on, during a run: the comparison's status, the last ten minutes of activity per source, and the latest correlations between pairs.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis}, "analysis_id")},
	{Name: "list_runs", Title: "List runs", Description: "Your runs, newest first: analysis, number, start, planned end, stop, revision, and the state and files of each run's data set.",
		InputSchema: schema(map[string]any{})},
	{Name: "get_dataset", Title: "Summary of a data set", Description: "What a run's data set holds: events per source and type, motifs, absences, the comparison's last correlations, and its files.",
		InputSchema: schema(map[string]any{"run_id": propRun}, "run_id")},
	{Name: "read_dataset_file", Title: "Read a data set file", Description: "Lines of a finished data set's file: events.csv or comparison.csv (header first), or dataset.json. Up to 500 lines per call; use offset to page on.",
		InputSchema: schema(map[string]any{"run_id": propRun,
			"file":   map[string]any{"type": "string", "enum": []string{fileEvents, fileCompare, fileJSON}},
			"offset": map[string]any{"type": "integer", "minimum": 0, "description": "lines to skip; the header of a CSV file is always given"},
			"lines":  map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}, "run_id", "file")},
}

func init() {
	for i := range mcpTools {
		mcpTools[i].Annotations = readOnly
	}
}

// mcp answers MCP requests. The token is checked before anything else.
func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	// A page on another site must not reach the endpoint through a
	// browser on the member's machine.
	if o := r.Header.Get("Origin"); o != "" {
		if u, err := url.Parse(o); err != nil || u.Host != r.Host {
			apiError(w, http.StatusForbidden, "origin", "Requests from other sites are refused.")
			return
		}
	}
	r, ok := s.authenticate(w, r, catalogue.ScopeMCP)
	if !ok {
		return
	}
	if v := r.Header.Get("MCP-Protocol-Version"); v != "" && !slices.Contains(mcpVersions, v) {
		apiError(w, http.StatusBadRequest, "protocol_version", "Unsupported MCP protocol version "+v+".")
		return
	}
	body := http.MaxBytesReader(w, r.Body, 1<<20)
	var raw json.RawMessage
	if err := json.NewDecoder(body).Decode(&raw); err != nil {
		s.noteRPC(r, rpcRequest{Method: "(unreadable message)"}, rpcResponse{Error: &rpcError{-32700, "parse error"}}, true, time.Now())
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		return
	}
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "[") { // a batch, as the 2025-03-26 version allows
		var reqs []rpcRequest
		if err := json.Unmarshal(raw, &reqs); err != nil {
			s.noteRPC(r, rpcRequest{Method: "(invalid request)"}, rpcResponse{Error: &rpcError{-32600, "invalid request"}}, true, time.Now())
			writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32600, "invalid request"}})
			return
		}
		var out []rpcResponse
		for _, q := range reqs {
			start := time.Now()
			resp, ok := s.rpc(r, q)
			s.noteRPC(r, q, resp, ok, start)
			if ok {
				out = append(out, resp)
			}
		}
		if len(out) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPC(w, out)
		return
	}
	var q rpcRequest
	if err := json.Unmarshal(raw, &q); err != nil {
		s.noteRPC(r, rpcRequest{Method: "(invalid request)"}, rpcResponse{Error: &rpcError{-32600, "invalid request"}}, true, time.Now())
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32600, "invalid request"}})
		return
	}
	start := time.Now()
	resp, ok := s.rpc(r, q)
	s.noteRPC(r, q, resp, ok, start)
	if !ok {
		w.WriteHeader(http.StatusAccepted) // a notification or a response: nothing to answer
		return
	}
	writeRPC(w, resp)
}

func writeRPC(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// rpc answers one message; ok is false when nothing is to be answered.
func (s *Server) rpc(r *http.Request, q rpcRequest) (rpcResponse, bool) {
	if len(q.ID) == 0 || string(q.ID) == "null" {
		return rpcResponse{}, false
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: q.ID}
	if q.JSONRPC != "2.0" || q.Method == "" {
		resp.Error = &rpcError{-32600, "invalid request"}
		return resp, true
	}
	switch q.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(q.Params, &p)
		v := mcpVersions[0]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "listen-workbench", "title": s.cfg.SiteName, "version": "1"},
			"instructions": "Read-only access to the member's own analyses of live audio streams: their sources and settings, " +
				"the engine's current state, motifs and stored events while an analysis is in a run or a preview, " +
				"the comparison between sources, and the data sets of finished runs. Times are UTC. " +
				"Nothing here starts, stops or changes an analysis; the member does that on the Workbench's pages.",
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools}
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(q.Params, &p); err != nil {
			resp.Error = &rpcError{-32602, "invalid params"}
			return resp, true
		}
		known := false
		for _, t := range mcpTools {
			known = known || t.Name == p.Name
		}
		if !known {
			resp.Error = &rpcError{-32602, "unknown tool: " + p.Name}
			return resp, true
		}
		text, isErr := s.callTool(r.Context(), p.Name, p.Arguments)
		resp.Result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isErr}
	default:
		resp.Error = &rpcError{-32601, "method not found: " + q.Method}
	}
	return resp, true
}

type toolArgs struct {
	Analysis int64  `json:"analysis_id"`
	Source   int64  `json:"source_id"`
	Run      int64  `json:"run_id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Type     string `json:"type"`
	Limit    int    `json:"limit"`
	File     string `json:"file"`
	Offset   int    `json:"offset"`
	Lines    int    `json:"lines"`
}

func toolJSON(v any) (string, bool) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err.Error(), true
	}
	return string(b), false
}

// callTool runs one tool for the member behind the token and returns its
// text, and whether it is an error the assistant should see.
func (s *Server) callTool(ctx context.Context, name string, rawArgs json.RawMessage) (string, bool) {
	sess, _ := ctx.Value(sessionKey).(catalogue.Session)
	user := sess.User.ID
	var a toolArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &a); err != nil {
			return "The arguments do not fit the tool's schema: " + err.Error(), true
		}
	}
	notYours := "There is no such analysis or run of yours."
	analysis := func() (catalogue.Analysis, string) {
		an, err := s.memberAnalysis(ctx, user, a.Analysis)
		if err != nil {
			return an, notYours
		}
		return an, ""
	}
	switch name {
	case "list_analyses":
		as, err := s.db.AnalysesOf(ctx, user)
		if err != nil {
			return err.Error(), true
		}
		out := []apiAnalysis{}
		for _, an := range as {
			out = append(out, s.toAPIAnalysis(ctx, an))
		}
		return toolJSON(map[string]any{"analyses": out})
	case "get_analysis":
		an, why := analysis()
		if why != "" {
			return why, true
		}
		return toolJSON(s.toAPIAnalysis(ctx, an))
	case "get_state", "get_motifs", "get_history_events":
		an, why := analysis()
		if why != "" {
			return why, true
		}
		what := map[string]string{"get_state": "state", "get_motifs": "motifs", "get_history_events": "history/events"}[name]
		q := map[string]string{"from": a.From, "to": a.To, "type": a.Type}
		if a.Limit > 0 {
			q["limit"] = strconv.Itoa(min(a.Limit, 1000))
		}
		raw, status, msg := s.sourceData(ctx, an, a.Source, what, q)
		if status != 200 {
			return msg, true
		}
		return string(raw), false
	case "get_comparison":
		an, why := analysis()
		if why != "" {
			return why, true
		}
		if !s.compares(an) {
			return "The sources of this analysis are not compared now: that needs two or more running sources and a run going on.", true
		}
		raw, err := s.eng.GroupData(ctx, an.GroupID(), "state", nil)
		if err != nil {
			return err.Error(), true
		}
		return string(raw), false
	case "list_runs":
		runs, err := s.memberRuns(ctx, user)
		if err != nil {
			return err.Error(), true
		}
		return toolJSON(map[string]any{"runs": runs})
	case "get_dataset":
		run, _, err := s.memberRun(ctx, user, a.Run)
		if err != nil {
			return notYours, true
		}
		var sum any
		json.Unmarshal(run.Summary, &sum)
		return toolJSON(map[string]any{"run": toAPIRun(run), "summary": sum})
	case "read_dataset_file":
		run, _, err := s.memberRun(ctx, user, a.Run)
		if err != nil {
			return notYours, true
		}
		rc, err := s.openDataset(run, a.File)
		if err != nil {
			return "No such file: the data set is not finished, or has no " + a.File + ".", true
		}
		defer rc.Close()
		n := a.Lines
		if n <= 0 || n > 500 {
			n = 500
		}
		sc := bufio.NewScanner(rc)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var b strings.Builder
		header := a.File != fileJSON // a CSV file's header comes with every page
		line, given, more := 0, 0, false
		for sc.Scan() {
			if header {
				header = false
				b.WriteString(sc.Text() + "\n")
				continue
			}
			line++
			if line <= a.Offset {
				continue
			}
			if given == n {
				more = true
				break
			}
			b.WriteString(sc.Text() + "\n")
			given++
		}
		if more {
			fmt.Fprintf(&b, "… more lines follow; call again with offset %d.\n", a.Offset+given)
		}
		return b.String(), false
	}
	return "unknown tool", true
}
