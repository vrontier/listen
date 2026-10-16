package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// The MCP guide says in plain words how the tools fit together, so that a
// less capable assistant uses them in the right order. It is sent as the
// server's instructions when a client connects, and the MCP page shows it
// to copy into a session by hand, with the member's own ids as examples.

// toolDoc is one tool as the MCP page lists it.
type toolDoc struct {
	Name, Title, Description string
	Control                  bool // only for a token allowed control
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
		case "title":
			out[name] = "Night watch"
		case "source_ids":
			out[name] = []int64{ids.Source}
		}
	}
	return out
}

func toolDocs(ids guideIDs) []toolDoc {
	var out []toolDoc
	for _, t := range toolsFor(catalogue.APIToken{Scopes: []string{catalogue.ScopeControl}}) {
		d := toolDoc{Name: t.Name, Title: t.Title, Description: t.Description, Control: isControlTool(t.Name)}
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
func mcpGuide(ids *guideIDs, control bool) string {
	var b strings.Builder
	readOnly, changing := "nothing you do starts, stops or changes anything.", ""
	if control {
		readOnly = "with this token you may also change things, as section 7 says."
		changing = guideControl
	}
	b.WriteString(strings.NewReplacer("{{readonly}}", readOnly, "{{control}}", changing, "{{runrule}}", map[bool]string{true: "Change things only when the member asks for it, and say what you changed.", false: "Nothing you do starts, stops or changes a run."}[control]).Replace(`Listen Workbench: how to use its MCP tools

What it is: the Workbench analyses live audio streams for a member. An analysis has one or more sources (streams); for each source the engine finds spectral events (transients, resonances) and motifs, recurring patterns it learns. A run is a period in which an analysis runs; each finished run leaves a data set. You read only this member's own analyses; {{readonly}}

How to go about it:
1. Always start with list_analyses. It gives each analysis's id, its state ("run", "preview" or "idle"), and its sources with their ids. Never guess an id; take it from an earlier answer.
2. Then summarize_analysis (analysis_id): a short answer saying where the analysis stands, whatever its state, and which call to make next.
3. What happened since, during a run: get_events_since (analysis_id). It returns counts per source and type, the events in a compact form, and a "cursor". Keep the cursor and pass it unchanged on the next call: you get only what is new. With limit 0 you get counts only and a cursor at the end, to follow from now on. When "more" is true, call again with the new cursor.
4. More live detail, only while the state is "run" or "preview":
   - get_state (analysis_id, source_id): what one source hears now; a large answer.
   - get_motifs (analysis_id, source_id): the motifs it has learned.
   - get_history_events (analysis_id, source_id): the engine's stored events in full form, only during a run; narrow with from, to (RFC 3339, UTC), type, limit.
   - get_comparison (analysis_id): how the sources relate, from two sources on, during a run.
5. Finished runs, at any time:
   - list_runs: every run with its id, analysis, times and data set state.
   - get_dataset (run_id): a summary of what the run recorded, with each file's size and number of lines. Read this before any file.
   - read_dataset_file (run_id, file): the raw lines, up to 500 at a time. The last line of the answer, in [brackets], says which lines you got of how many and the offset for the next call.
6. If a tool answers with an error, read its text: it names the id that was wrong or why the data is not there (not running, a preview, no data set yet) and which call to make instead. Do not repeat the same call unchanged.

Rules: times are UTC. Ids are integers. Prefer summaries (summarize_analysis, get_events_since with limit 0, get_dataset) over reading whole files or large answers. Tell the member what you read. {{runrule}}

{{control}}The server also offers resources (the guide; each finished run's summary and files, as low://runs/<run_id>/summary and low://runs/<run_id>/<file>) and prompts (summarize_run, watch_analysis, compare_sources), for clients that show them.

Tools (a ? marks an optional argument):
`))
	tools := toolsFor(catalogue.APIToken{})
	if control {
		tools = toolsFor(catalogue.APIToken{Scopes: []string{catalogue.ScopeControl}})
	}
	for _, t := range tools {
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

// guideControl is the guide's part for a token allowed control.
const guideControl = `7. Changing things (this token is allowed control; its member set limits for it):
   - list_sources: the sources you may put into analyses, with their ids.
   - create_analysis (title, source_ids): a new analysis with default settings; it does not start.
   - update_analysis (analysis_id, title?, description?): texts only.
   - set_sources (analysis_id, source_ids, compare?) and update_source_settings (analysis_id, source_id, …): a new revision each; the engine checks settings first. Give only what changes; null returns a setting to its default; rules replace all rules. Read the current settings with get_analysis. Not during a run.
   - start_preview / stop_preview (analysis_id): ten minutes live, nothing recorded.
   - start_run (analysis_id, hours?, offline_stop_min?) / stop_run (analysis_id): a run records a data set; the member gets a mail when you start one. After stop_run, get_dataset once the data set is ready.
   An answer that begins "Refused:" names the limit or permission that stopped it; do not try again until the member changes it. Check with summarize_analysis that a change did what you meant.

`
