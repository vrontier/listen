package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// MCP resources and prompts. Resources are what a member can attach to a
// session by hand: the guide, and each finished run's summary and files.
// Prompts are a few tasks worded for an assistant, naming the tools in the
// order that answers them. Both only read, like the tools.

// resourceMax is the largest file handed out as a resource; beyond it,
// read_dataset_file pages through.
const resourceMax = 1 << 20

const resourceScheme = "low://"

type mcpResource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

type mcpPromptArg struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

type mcpPrompt struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	Arguments   []mcpPromptArg `json:"arguments"`
}

var mcpPrompts = []mcpPrompt{
	{Name: "summarize_run", Title: "Summarize a run", Description: "What a finished run recorded, in plain words.",
		Arguments: []mcpPromptArg{{"run_id", "the run's id; the newest run with a data set if left out", false}}},
	{Name: "watch_analysis", Title: "Follow an analysis", Description: "Where an analysis stands, and from then on only what is new.",
		Arguments: []mcpPromptArg{{"analysis_id", "the analysis's id, from list_analyses", true}}},
	{Name: "compare_sources", Title: "Compare the sources", Description: "How the sources of an analysis relate: now during a run, otherwise in its last data set.",
		Arguments: []mcpPromptArg{{"analysis_id", "the analysis's id, from list_analyses", true}}},
}

func (s *Server) mcpExtra(ctx context.Context, method string, params json.RawMessage) (any, *rpcError) {
	sess, _ := ctx.Value(sessionKey).(catalogue.Session)
	user := sess.User.ID
	var p struct {
		URI       string            `json:"uri"`
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments"`
	}
	json.Unmarshal(params, &p)
	switch method {
	case "resources/list":
		list := []mcpResource{{URI: resourceScheme + "guide", Name: "guide", Title: "Guide for assistants", MimeType: "text/plain",
			Description: "How the Workbench's tools fit together; the same text as the server's instructions."}}
		runs, err := s.db.RunsOf(ctx, user)
		if err != nil {
			return nil, &rpcError{-32603, err.Error()}
		}
		for _, run := range runs {
			if run.Dataset != catalogue.DatasetReady {
				continue
			}
			list = append(list, mcpResource{URI: fmt.Sprintf("%sruns/%d/summary", resourceScheme, run.ID), Name: fmt.Sprintf("run-%d-summary", run.ID),
				Title:       fmt.Sprintf("%s, run %d: summary", run.AnalysisName, run.Number),
				Description: fmt.Sprintf("Data set of a run started %s UTC: events per source and type, absences, files.", run.Started.UTC().Format("2006-01-02 15:04")),
				MimeType:    "application/json"})
		}
		return map[string]any{"resources": list}, nil
	case "resources/templates/list":
		return map[string]any{"resourceTemplates": []map[string]any{
			{"uriTemplate": resourceScheme + "runs/{run_id}/summary", "name": "run-summary", "title": "A run's data set, summarized", "mimeType": "application/json"},
			{"uriTemplate": resourceScheme + "runs/{run_id}/{file}", "name": "run-file", "title": "A file of a run's data set: events.csv, comparison.csv or dataset.json (up to 1 MB; read_dataset_file pages through larger ones)"},
		}}, nil
	case "resources/read":
		text, mime, why := s.readResource(ctx, user, p.URI)
		if why != "" {
			return nil, &rpcError{-32002, why}
		}
		return map[string]any{"contents": []map[string]any{{"uri": p.URI, "mimeType": mime, "text": text}}}, nil
	case "prompts/list":
		return map[string]any{"prompts": mcpPrompts}, nil
	case "prompts/get":
		text, why := promptText(p.Name, p.Arguments)
		if why != "" {
			return nil, &rpcError{-32602, why}
		}
		return map[string]any{"messages": []map[string]any{{"role": "user", "content": map[string]any{"type": "text", "text": text}}}}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + method}
}

// readResource reads one low:// address for the member.
func (s *Server) readResource(ctx context.Context, user int64, uri string) (string, string, string) {
	rest, ok := strings.CutPrefix(uri, resourceScheme)
	if !ok {
		return "", "", "Unknown resource " + uri + "; resources/list names them."
	}
	if rest == "guide" {
		return mcpGuide(nil), "text/plain", ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[0] != "runs" {
		return "", "", "Unknown resource " + uri + "; resources/list names them."
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", "", "Unknown resource " + uri + "; resources/list names them."
	}
	run, _, err := s.memberRun(ctx, user, id)
	if err != nil {
		return "", "", noRun(id)
	}
	if parts[2] == "summary" {
		text, _ := toolJSON(s.datasetView(run))
		return text, "application/json", ""
	}
	if run.Dataset != catalogue.DatasetReady {
		return "", "", noDataset(run)
	}
	rc, err := s.openDataset(run, parts[2])
	if err != nil {
		return "", "", fmt.Sprintf("The data set of run %d has no %s; its files: %s.", run.ID, parts[2], strings.Join(toAPIRun(run).Files, ", "))
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, resourceMax+1))
	if err != nil {
		return "", "", err.Error()
	}
	if len(b) > resourceMax {
		return "", "", fmt.Sprintf("%s of run %d is larger than 1 MB; read it with read_dataset_file, 500 lines at a time, after get_dataset.", parts[2], run.ID)
	}
	return string(b), datasetTypes[parts[2]], ""
}

// promptText words a prompt with its arguments.
func promptText(name string, args map[string]string) (string, string) {
	id := func(key string) (string, string) {
		v := strings.TrimSpace(args[key])
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			return "", key + " must be an id (an integer) from list_analyses."
		}
		return v, ""
	}
	switch name {
	case "summarize_run":
		run := strings.TrimSpace(args["run_id"])
		first := "Call list_runs and take the newest run whose dataset is \"ready\"."
		if run != "" {
			if _, err := strconv.ParseInt(run, 10, 64); err != nil {
				return "", "run_id must be an id (an integer) from list_runs."
			}
			first = "The run is " + run + "."
		}
		return first + ` Call get_dataset with its run_id and summarize for me, in plain words: how long the run lasted, what each source recorded (events by type, motifs), when a source was absent, and what the comparison found if there is one. Read events.csv with read_dataset_file only if the summary leaves a question open, and then only the lines you need. Say which calls you made.`, ""
	case "watch_analysis":
		a, why := id("analysis_id")
		if why != "" {
			return "", why
		}
		return `Call summarize_analysis with analysis_id ` + a + ` and tell me in a few sentences where it stands. If it is in a run, call get_events_since with analysis_id ` + a + ` and limit 0, and keep the cursor it returns. Whenever I ask what is new, call get_events_since with that cursor and tell me only what changed: counts per source and type, and anything unusual among the events. Keep the newest cursor each time.`, ""
	case "compare_sources":
		a, why := id("analysis_id")
		if why != "" {
			return "", why
		}
		return `Call summarize_analysis with analysis_id ` + a + `. If it is in a run and comparing, call get_comparison and describe the activity per source and the correlations between pairs. If not, take its last data set (the run_id in last_dataset), call get_dataset and describe the comparison there; read comparison.csv with read_dataset_file only for details the summary lacks. Times are wall time at the engine, aligned to a few seconds.`, ""
	}
	return "", "Unknown prompt " + name + "; prompts/list names them."
}
