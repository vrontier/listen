package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The MCP guide says in plain words how the tools fit together, so that a
// less capable assistant uses them in the right order. It is sent as the
// server's instructions when a client connects, and the MCP page shows it
// to copy into a session by hand, with the member's own ids as examples.

// toolDoc is one tool as the MCP page lists it.
type toolDoc struct {
	Name, Title, Description string
	Args                     []argDoc
	Example                  string // a tools/call message
}

type argDoc struct {
	Name, Type, Description string
	Required                bool
}

// guideIDs are the ids the examples use: the member's own where they
// have any, else 1.
type guideIDs struct {
	Analysis, Source, Run int64
}

func (s *Server) guideIDsOf(ctx context.Context, user int64) guideIDs {
	ids := guideIDs{1, 1, 1}
	if as, err := s.db.AnalysesOf(ctx, user); err == nil && len(as) > 0 {
		ids.Analysis = as[0].ID
		if len(as[0].Sources) > 0 {
			ids.Source = as[0].Sources[0].Stream.ID
		}
	}
	if runs, err := s.db.RunsOf(ctx, user); err == nil && len(runs) > 0 {
		ids.Run = runs[0].ID
	}
	return ids
}

// exampleArgs fills a tool's required arguments with example values.
func exampleArgs(t mcpTool, ids guideIDs) map[string]any {
	out := map[string]any{}
	req, _ := t.InputSchema["required"].([]string)
	for _, name := range req {
		switch name {
		case "analysis_id":
			out[name] = ids.Analysis
		case "source_id":
			out[name] = ids.Source
		case "run_id":
			out[name] = ids.Run
		case "file":
			out[name] = fileEvents
		}
	}
	return out
}

func toolDocs(ids guideIDs) []toolDoc {
	var out []toolDoc
	for _, t := range mcpTools {
		d := toolDoc{Name: t.Name, Title: t.Title, Description: t.Description}
		props, _ := t.InputSchema["properties"].(map[string]any)
		req, _ := t.InputSchema["required"].([]string)
		var names []string
		for n := range props {
			names = append(names, n)
		}
		// Required first, then in order of name.
		sort.Slice(names, func(i, j int) bool {
			ri, rj := contains(req, names[i]), contains(req, names[j])
			if ri != rj {
				return ri
			}
			return names[i] < names[j]
		})
		for _, n := range names {
			p, _ := props[n].(map[string]any)
			a := argDoc{Name: n, Required: contains(req, n)}
			a.Type, _ = p["type"].(string)
			a.Description, _ = p["description"].(string)
			if e, ok := p["enum"].([]string); ok {
				a.Description = strings.TrimLeft(a.Description+"; one of "+strings.Join(e, ", "), "; ")
			}
			if mx, ok := p["maximum"].(int); ok {
				a.Description = strings.TrimLeft(fmt.Sprintf("%s; at most %d", a.Description, mx), "; ")
			}
			d.Args = append(d.Args, a)
		}
		call, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": t.Name, "arguments": exampleArgs(t, ids)}})
		d.Example = string(call)
		out = append(out, d)
	}
	return out
}

// mcpGuide is the guide in plain text; with examples it shows each tool's
// arguments filled in with ids.
func mcpGuide(ids *guideIDs) string {
	var b strings.Builder
	b.WriteString(`Listen Workbench: how to use its MCP tools

What it is: the Workbench analyses live audio streams for a member. An analysis has one or more sources (streams); for each source the engine finds spectral events (transients, resonances) and motifs, recurring patterns it learns. A run is a period in which an analysis runs; each finished run leaves a data set. You read only this member's own analyses; nothing you do starts, stops or changes anything.

How to go about it:
1. Always start with list_analyses. It gives each analysis's id, its state ("run", "preview" or "idle"), and its sources with their ids. Never guess an id; take it from an earlier answer.
2. Live data, only while the analysis's state is "run" or "preview":
   - get_state (analysis_id, source_id): what one source hears now.
   - get_motifs (analysis_id, source_id): the motifs it has learned.
   - get_history_events (analysis_id, source_id): stored events, only during a run; narrow with from, to (RFC 3339, UTC), type, limit.
   - get_comparison (analysis_id): how the sources relate, from two sources on, during a run.
3. Finished runs, at any time:
   - list_runs: every run with its id, analysis, times and data set state.
   - get_dataset (run_id): a summary of what the run recorded. Read this before any file.
   - read_dataset_file (run_id, file): the raw lines, 500 at a time. When the answer ends with "more lines follow", call again with the offset it names.
4. If a tool answers with an error, read its text: it says why (not running, no such id, file not ready) and what to do instead. Do not repeat the same call unchanged.

Rules: times are UTC. Ids are integers. Prefer summaries (get_dataset, get_state) over reading whole files. Tell the member what you read.

Tools (a ? marks an optional argument):
`)
	for _, t := range mcpTools {
		var args []string
		props, _ := t.InputSchema["properties"].(map[string]any)
		req, _ := t.InputSchema["required"].([]string)
		for _, n := range req {
			args = append(args, n)
		}
		var opt []string
		for n := range props {
			if !contains(req, n) {
				opt = append(opt, n)
			}
		}
		sort.Strings(opt)
		for _, n := range opt {
			args = append(args, n+"?")
		}
		fmt.Fprintf(&b, "- %s(%s): %s\n", t.Name, strings.Join(args, ", "), t.Description)
		if ids != nil {
			ex, _ := json.Marshal(exampleArgs(t, *ids))
			fmt.Fprintf(&b, "  example arguments: %s\n", ex)
		}
	}
	return b.String()
}
