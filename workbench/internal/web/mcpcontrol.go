package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
	"github.com/vrontier/listen/workbench/internal/engine"
)

// MCP tools that change things (members-and-streams.md, Tokens, Control):
// only for a token its member allowed control, within that token's limits.
// They do what the pages do, through the same checks: caps, the disk quota,
// revisions, the rule that nothing of an analysis changes during a run.
// They never register, publish or delete anything.

// refused starts the text of a call a token may not make; such calls are
// noted as refused, not as errors.
const refused = "Refused: "

var (
	propSourceIDs = map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "minItems": 1, "maxItems": maxSources,
		"description": "ids of sources, from list_sources"}
	changeHint = map[string]any{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": false, "openWorldHint": false}
	stopHint   = map[string]any{"readOnlyHint": false, "destructiveHint": true, "idempotentHint": true, "openWorldHint": false}
	ruleItem   = map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "hz"}, "properties": map[string]any{
		"kind":          map[string]any{"type": "string", "enum": []string{engine.RuleLine, engine.RuleComb}, "description": "line: one frequency; comb: a frequency and its harmonics"},
		"hz":            map[string]any{"type": "number", "description": "the frequency, or the comb's fundamental"},
		"odd":           map[string]any{"type": "boolean", "description": "comb only: odd harmonics only"},
		"tol_hz":        map[string]any{"type": "number", "description": "how far around each frequency, in Hz"},
		"drift_percent": map[string]any{"type": "number", "description": "how far the frequency may wander, in percent"}}}
	numOrNull = func(desc string) map[string]any {
		return map[string]any{"type": []string{"number", "null"}, "description": desc + "; null for the engine's default"}
	}
)

// changeTools change analyses and count against changes_per_day.
var changeTools = []string{"create_analysis", "update_analysis", "set_sources", "update_source_settings"}

var mcpControlTools = []mcpTool{
	{Name: "create_analysis", Title: "Make an analysis", Description: "Makes a new analysis of one or more of your sources (ids from list_sources), each with the default settings. It does not start; start_preview or start_run does.",
		InputSchema: schema(map[string]any{"title": map[string]any{"type": "string", "minLength": 2, "maxLength": 80},
			"description": map[string]any{"type": "string", "maxLength": 2000}, "source_ids": propSourceIDs}, "title", "source_ids"),
		Annotations: changeHint},
	{Name: "update_analysis", Title: "Rename or describe an analysis", Description: "Changes an analysis's title or description. Allowed during a run.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis, "title": map[string]any{"type": "string", "minLength": 2, "maxLength": 80},
			"description": map[string]any{"type": "string", "maxLength": 2000}}, "analysis_id"),
		Annotations: changeHint},
	{Name: "set_sources", Title: "Choose an analysis's sources", Description: "Sets which sources an analysis listens to, as a new revision. Sources it keeps keep their settings; new ones get the defaults. Not during a run.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis, "source_ids": propSourceIDs,
			"compare": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "how two or more sources are compared, e.g. [\"activity\"]; left out: as before"}}, "analysis_id", "source_ids"),
		Annotations: changeHint},
	{Name: "update_source_settings", Title: "Change a source's settings", Description: "Changes the settings and filter rules of one source of an analysis, as a new revision; the engine checks them first. Give only what changes: a field left out keeps its value, null returns it to the default; rules, when given, replace all rules ([] removes them). get_analysis shows the current settings. Not during a run.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis, "source_id": propSource,
			"min_hz":          numOrNull("the lowest frequency analysed, in Hz"),
			"max_hz":          numOrNull("the highest frequency analysed, in Hz"),
			"transient_k":     numOrNull("how far above the background a transient must rise; higher finds fewer"),
			"transient_gap_s": numOrNull("the shortest time between two transients, in seconds"),
			"memory_mode":     map[string]any{"type": "string", "enum": []string{engine.MemoryDisk, engine.MemoryRAM, engine.MemoryOff}, "description": "whether learned motifs are kept: disk, ram (until the run ends) or off"},
			"rules":           map[string]any{"type": "array", "items": ruleItem, "maxItems": 64, "description": "filter rules that leave out known interference, such as mains hum"}},
			"analysis_id", "source_id"),
		Annotations: changeHint},
	{Name: "start_preview", Title: "Start a preview", Description: "Lets an analysis run for ten minutes to look at it live (get_state, get_motifs); it records nothing. Not while it is in a run.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis}, "analysis_id"), Annotations: changeHint},
	{Name: "stop_preview", Title: "End a preview", Description: "Ends an analysis's preview before its ten minutes are up.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis}, "analysis_id"), Annotations: stopHint},
	{Name: "start_run", Title: "Start a run", Description: "Starts a run of an analysis: its sources stay connected until it ends, and what it finds goes into a data set. Give hours for its planned end; this token may have a longest run. The member gets a mail.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis,
			"hours":            map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 24 * 365, "description": "how long it runs; left out: until stopped, if this token may"},
			"offline_stop_min": map[string]any{"type": "integer", "minimum": 0, "maximum": 1440, "description": "stop when a source sends nothing for this many minutes; 15 if left out, 0 for never"}}, "analysis_id"),
		Annotations: changeHint},
	{Name: "stop_run", Title: "Stop a run", Description: "Stops an analysis's run now; its data set is put together and can then be read with get_dataset.",
		InputSchema: schema(map[string]any{"analysis_id": propAnalysis}, "analysis_id"), Annotations: stopHint},
}

// listSourcesTool reads; every MCP token has it.
var listSourcesTool = mcpTool{Name: "list_sources", Title: "List sources", Description: "The sources (live streams) you may use in analyses: every member's, since sources are shared by all members, except those another member keeps private for now. With id, name, place, state, sample rate, and the steward who registered it if that is not you.",
	InputSchema: schema(map[string]any{})}

// toolsFor are the tools a token may call.
func toolsFor(t catalogue.APIToken) []mcpTool {
	out := append(append([]mcpTool{}, mcpTools[:1]...), listSourcesTool)
	out = append(out, mcpTools[1:]...)
	if t.Has(catalogue.ScopeControl) {
		out = append(out, mcpControlTools...)
	}
	return out
}

func isControlTool(name string) bool {
	return slices.ContainsFunc(mcpControlTools, func(t mcpTool) bool { return t.Name == name })
}

type controlArgs struct {
	Analysis    int64          `json:"analysis_id"`
	Source      int64          `json:"source_id"`
	Title       *string        `json:"title"`
	Description *string        `json:"description"`
	Sources     []int64        `json:"source_ids"`
	Compare     []string       `json:"compare"`
	Hours       *float64       `json:"hours"`
	Offline     *int           `json:"offline_stop_min"`
	MemoryMode  *string        `json:"memory_mode"`
	Rules       *[]engine.Rule `json:"rules"`
}

// usableStreams are the sources a member may put into analyses.
func (s *Server) usableStreams(ctx context.Context, user int64) ([]catalogue.Stream, error) {
	own, err := s.db.StreamsOf(ctx, user)
	if err != nil {
		return nil, err
	}
	open, err := s.db.OpenStreams(ctx, user)
	return append(own, open...), err
}

func (s *Server) listSources(ctx context.Context, user int64) (string, bool) {
	streams, err := s.usableStreams(ctx, user)
	if err != nil {
		return err.Error(), true
	}
	out := []map[string]any{}
	for _, st := range streams {
		v := map[string]any{"id": st.ID, "name": st.Texts.Name, "state": st.State, "sample_rate": st.SampleRate}
		if st.Texts.Place != "" {
			v["place"] = st.Texts.Place
		}
		if st.OwnerID != user {
			v["steward"] = st.OwnerName
		}
		out = append(out, v)
	}
	return toolJSON(map[string]any{"sources": out})
}

// checkLimit says why the token may not make this call now, by its limits.
func (s *Server) checkLimit(ctx context.Context, t catalogue.APIToken, name string, a controlArgs) (string, error) {
	day := time.Now().Add(-24 * time.Hour)
	l := t.Limits
	where := " Its member sets the limits under AI, Access."
	switch {
	case slices.Contains(changeTools, name) && l.ChangesPerDay > 0:
		n, err := s.db.TokenCalls(ctx, t.ID, changeTools, day)
		if err != nil || n < l.ChangesPerDay {
			return "", err
		}
		return fmt.Sprintf("this token may make %d changes to analyses a day and has made %d in the last 24 hours.", l.ChangesPerDay, n) + where, nil
	case name == "start_preview" && l.PreviewsPerDay > 0:
		n, err := s.db.TokenCalls(ctx, t.ID, []string{"start_preview"}, day)
		if err != nil || n < l.PreviewsPerDay {
			return "", err
		}
		return fmt.Sprintf("this token may start %d previews a day and has started %d in the last 24 hours.", l.PreviewsPerDay, n) + where, nil
	case name == "start_run":
		if l.RunHours > 0 {
			if a.Hours == nil {
				return fmt.Sprintf("this token may start runs of at most %d hours; give hours.", l.RunHours) + where, nil
			}
			if *a.Hours > float64(l.RunHours) {
				return fmt.Sprintf("this token may start runs of at most %d hours, not %g.", l.RunHours, *a.Hours) + where, nil
			}
		}
		if l.RunsPerDay == 0 && l.RunsAtOnce == 0 {
			return "", nil
		}
		started, active, err := s.db.TokenRuns(ctx, t.ID, day)
		if err != nil {
			return "", err
		}
		if l.RunsPerDay > 0 && started >= l.RunsPerDay {
			return fmt.Sprintf("this token may start %d runs a day and has started %d in the last 24 hours.", l.RunsPerDay, started) + where, nil
		}
		if l.RunsAtOnce > 0 && active >= l.RunsAtOnce {
			return fmt.Sprintf("this token may have %d runs going on at once, and %d of its runs are going on; stop_run ends one.", l.RunsAtOnce, active) + where, nil
		}
	}
	return "", nil
}

// callControl runs a tool that changes things.
func (s *Server) callControl(ctx context.Context, name string, rawArgs json.RawMessage) (string, bool) {
	sess, _ := ctx.Value(sessionKey).(catalogue.Session)
	user := sess.User.ID
	t, _ := ctx.Value(tokenKey).(catalogue.APIToken)
	if !t.Has(catalogue.ScopeControl) {
		return refused + "this token may only read. Its member can allow control for it under AI, Access.", true
	}
	var a controlArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &a); err != nil {
			return "The arguments do not fit the tool's schema: " + err.Error(), true
		}
	}
	if why, err := s.checkLimit(ctx, t, name, a); err != nil || why != "" {
		if err != nil {
			return err.Error(), true
		}
		return refused + why, true
	}
	var an catalogue.Analysis
	if name != "create_analysis" {
		var err error
		if an, err = s.memberAnalysis(ctx, user, a.Analysis); err != nil {
			return noAnalysis(a.Analysis), true
		}
	}
	done := func(object string, id int64, detail any, out map[string]any) (string, bool) {
		if err := s.db.AuditByToken(ctx, user, object, id, name, t.ID, t.Name, detail); err != nil {
			log.Printf("audit: %v", err)
		}
		return toolJSON(out)
	}
	switch name {
	case "create_analysis":
		title, desc := "", ""
		if a.Title != nil {
			title = strings.TrimSpace(*a.Title)
		}
		if a.Description != nil {
			desc = strings.TrimSpace(*a.Description)
		}
		errs := map[string]string{}
		checkAnalysisTexts(title, desc, errs)
		if len(errs) > 0 {
			return textErrors(errs), true
		}
		ids, why, err := s.chooseSources(ctx, user, a.Sources)
		if err != nil || why != "" {
			if err != nil {
				return err.Error(), true
			}
			return why, true
		}
		settings := map[int64]json.RawMessage{}
		for _, id := range ids {
			settings[id] = defaultSettings
		}
		created, err := s.db.CreateAnalysis(ctx, user, title, desc, ids, settings)
		if err != nil {
			return err.Error(), true
		}
		out := map[string]any{"analysis_id": created.ID, "title": created.Name, "revision": created.Revision, "source_ids": ids,
			"next": "update_source_settings to tune a source; start_preview to look at it live; start_run to record."}
		if s.pushSourcesCtx(ctx, created.Sources) != nil {
			out["note"] = notTaken
		}
		return done("analysis", created.ID, map[string]any{"title": title, "source_ids": ids}, out)

	case "update_analysis":
		if a.Title == nil && a.Description == nil {
			return "Give title or description, or both.", true
		}
		title, desc := an.Name, an.Description
		if a.Title != nil {
			title = strings.TrimSpace(*a.Title)
		}
		if a.Description != nil {
			desc = strings.TrimSpace(*a.Description)
		}
		errs := map[string]string{}
		checkAnalysisTexts(title, desc, errs)
		if len(errs) > 0 {
			return textErrors(errs), true
		}
		if err := s.db.UpdateAnalysisTexts(ctx, an.ID, user, title, desc); err != nil {
			return err.Error(), true
		}
		return done("analysis", an.ID, map[string]any{"title": title}, map[string]any{"analysis_id": an.ID, "title": title, "description": desc, "saved": true})

	case "set_sources", "update_source_settings":
		if _, inRun, err := s.db.ActiveRun(ctx, an.ID); err != nil || inRun {
			if err != nil {
				return err.Error(), true
			}
			return fmt.Sprintf("Not saved: the sources and settings of analysis %d are fixed while its run lasts. stop_run first, or wait for the run's end.", an.ID), true
		}
		ids := make([]int64, 0, len(an.Sources))
		settings := map[int64]json.RawMessage{}
		for _, src := range an.Sources {
			ids = append(ids, src.Stream.ID)
			settings[src.Stream.ID] = src.Spec
		}
		compare := an.Spec.Compare
		var warns []string
		if name == "set_sources" {
			var why string
			var err error
			if ids, why, err = s.chooseSources(ctx, user, a.Sources); err != nil || why != "" {
				if err != nil {
					return err.Error(), true
				}
				return why, true
			}
			for _, id := range ids {
				if settings[id] == nil {
					settings[id] = defaultSettings
				}
			}
			if compare, why = s.compareFor(ids, a.Compare, compare); why != "" {
				return why, true
			}
		} else {
			src, ok := an.Source(a.Source)
			if !ok {
				return noSource(an, a.Source), true
			}
			spec, why, w, err := s.changedSpec(ctx, src, rawArgs, a)
			if err != nil {
				return err.Error(), true
			}
			if why != "" {
				return why, true
			}
			warns = w
			settings[src.Stream.ID] = spec
		}
		if len(ids) >= 2 && compare == nil {
			compare = catalogue.DefaultCompare
		}
		if sameRevision(an, ids, settings, compare) {
			return toolJSON(map[string]any{"analysis_id": an.ID, "revision": an.Revision, "saved": false, "note": "Nothing changed."})
		}
		before := an.Sources
		saved, err := s.db.SaveRevision(ctx, an.ID, user, ids, settings, compare)
		if err != nil {
			return err.Error(), true
		}
		out := map[string]any{"analysis_id": saved.ID, "revision": saved.Revision, "saved": true}
		if len(warns) > 0 {
			out["warnings"] = warns
		} else {
			out["checked"] = "the engine accepted the settings without warnings"
		}
		if s.pushSourcesCtx(ctx, append(append([]catalogue.AnalysisSource{}, before...), saved.Sources...)) != nil {
			out["note"] = notTaken
		}
		detail := map[string]any{"revision": saved.Revision}
		if name == "set_sources" {
			detail["source_ids"] = ids
		} else {
			detail["source_id"] = a.Source
		}
		return done("analysis", an.ID, detail, out)

	case "start_preview":
		until, why, note, err := s.startPreviewAs(ctx, user, an)
		if err != nil {
			return err.Error(), true
		}
		if why != "" {
			return "Not started: " + why, true
		}
		out := map[string]any{"analysis_id": an.ID, "preview_until": until.Format(time.RFC3339),
			"next": "get_state or summarize_analysis to see what each source hears; it ends by itself after ten minutes."}
		if note != "" {
			out["note"] = note
		}
		return done("analysis", an.ID, map[string]any{"until": until}, out)

	case "stop_preview":
		if why := s.endPreviewOf(ctx, an); why != "" {
			return why, true
		}
		return done("analysis", an.ID, nil, map[string]any{"analysis_id": an.ID, "preview": "ended"})

	case "start_run":
		var length time.Duration
		if a.Hours != nil {
			if *a.Hours <= 0 || *a.Hours > 24*365 {
				return "hours must be more than 0 and at most 8760.", true
			}
			length = time.Duration(*a.Hours * float64(time.Hour)).Round(time.Minute)
		}
		offline := 15
		if a.Offline != nil {
			if *a.Offline < 0 || *a.Offline > 1440 {
				return "offline_stop_min must be from 0 to 1440.", true
			}
			offline = *a.Offline
		}
		run, why, note, err := s.startRunAs(ctx, user, an, length, offline, &t)
		if err != nil {
			return err.Error(), true
		}
		if why != "" {
			return "Not started: " + why, true
		}
		s.tellRunStarted(sess.User, an, run, t)
		out := map[string]any{"analysis_id": an.ID, "run_id": run.ID, "number": run.Number, "started": run.Started, "offline_stop_min": offline,
			"next": "get_events_since with limit 0 to follow it; stop_run to end it. Its member has been told by mail."}
		if !run.Ends.IsZero() {
			out["ends"] = run.Ends
		} else {
			out["ends"] = "when stopped"
		}
		if note != "" {
			out["note"] = note
		}
		return done("analysis", an.ID, map[string]any{"run": run.ID, "hours": a.Hours, "offline_stop_min": offline}, out)

	case "stop_run":
		run, why, err := s.stopRunAs(ctx, user, an)
		if err != nil {
			return err.Error(), true
		}
		if why != "" {
			return why, true
		}
		return done("analysis", an.ID, map[string]any{"run": run.ID}, map[string]any{"analysis_id": an.ID, "run_id": run.ID, "stopped": true,
			"next": fmt.Sprintf("get_dataset (run_id %d) once its data set is ready, in a minute or so.", run.ID)})
	}
	return "unknown tool", true
}

func textErrors(errs map[string]string) string {
	var parts []string
	for _, k := range []string{"name", "description"} {
		if e, ok := errs[k]; ok {
			field := map[string]string{"name": "title"}[k]
			if field == "" {
				field = k
			}
			parts = append(parts, field+": "+e)
		}
	}
	return "Not saved: " + strings.Join(parts, "; ") + "."
}

// chooseSources checks that the member may use each source, in the order
// given, without repeats.
func (s *Server) chooseSources(ctx context.Context, user int64, want []int64) ([]int64, string, error) {
	if len(want) == 0 {
		return nil, "Give at least one source id; list_sources gives them.", nil
	}
	if len(want) > maxSources {
		return nil, fmt.Sprintf("An analysis has at most %d sources.", maxSources), nil
	}
	streams, err := s.usableStreams(ctx, user)
	if err != nil {
		return nil, "", err
	}
	var ids []int64
	for _, id := range want {
		if slices.Contains(ids, id) {
			continue
		}
		if !slices.ContainsFunc(streams, func(st catalogue.Stream) bool { return st.ID == id }) {
			return nil, fmt.Sprintf("There is no source %d you may use; list_sources gives the ids.", id), nil
		}
		ids = append(ids, id)
	}
	return ids, "", nil
}

// compareFor checks a choice of how sources are compared against what the
// engine offers; nil keeps what was.
func (s *Server) compareFor(ids []int64, want, was []string) ([]string, string) {
	if len(ids) < 2 {
		return nil, ""
	}
	if want == nil {
		return was, ""
	}
	_, _, info := s.sync.status()
	choices := compareChoices(info, nil, len(ids))
	var out []string
	for _, v := range want {
		i := slices.IndexFunc(choices, func(c compareChoice) bool { return c.ID == v })
		if i < 0 {
			var ok []string
			for _, c := range choices {
				ok = append(ok, c.ID)
			}
			return nil, fmt.Sprintf("The engine cannot compare by %q; it offers: %s.", v, strings.Join(ok, ", "))
		}
		if choices[i].Blocked != "" {
			return nil, v + " " + choices[i].Blocked
		}
		if !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, `Choose at least one way to compare, e.g. ["activity"].`
	}
	return out, ""
}

// changedSpec applies the given fields to a source's settings and has the
// engine check them at the source's rate.
func (s *Server) changedSpec(ctx context.Context, src catalogue.AnalysisSource, rawArgs json.RawMessage, a controlArgs) (json.RawMessage, string, []string, error) {
	var spec engine.AnalysisSpec
	json.Unmarshal(src.Spec, &spec)
	var fields map[string]json.RawMessage
	json.Unmarshal(rawArgs, &fields)
	num := func(key string, p **float64) string {
		v, ok := fields[key]
		if !ok {
			return ""
		}
		if string(v) == "null" {
			*p = nil
			return ""
		}
		var f float64
		if err := json.Unmarshal(v, &f); err != nil {
			return key + " must be a number or null."
		}
		*p = &f
		return ""
	}
	for key, p := range map[string]**float64{"min_hz": &spec.Settings.MinHz, "max_hz": &spec.Settings.MaxHz,
		"transient_k": &spec.Settings.TransientK, "transient_gap_s": &spec.Settings.TransientGapS} {
		if why := num(key, p); why != "" {
			return nil, why, nil, nil
		}
	}
	if a.MemoryMode != nil {
		spec.Memory.Mode = *a.MemoryMode
	}
	if a.Rules != nil {
		spec.Rules = *a.Rules
		for i := range spec.Rules {
			spec.Rules[i].Text = ""
		}
	}
	ready, problem, _ := s.sync.status()
	if !ready {
		return nil, "The settings cannot be checked now: " + problem, nil, nil
	}
	v, err := s.eng.Validate(ctx, engine.ValidateRequest{SampleRate: src.Stream.SampleRate, AnalysisSpec: spec})
	if err != nil {
		return nil, "", nil, err
	}
	var warns []string
	for _, w := range v.Warnings {
		warns = append(warns, w.Path+": "+w.Message)
	}
	if !v.Valid {
		var errs []string
		for _, e := range v.Errors {
			errs = append(errs, e.Path+": "+e.Message)
		}
		return nil, "Not saved; the engine refused the settings: " + strings.Join(errs, "; ") + ".", warns, nil
	}
	b, _ := json.Marshal(spec)
	return b, "", warns, nil
}

// tellRunStarted mails the member that a token started a run (decided
// 2026-10-16), in the background.
func (s *Server) tellRunStarted(u catalogue.User, a catalogue.Analysis, run catalogue.Run, t catalogue.APIToken) {
	ends := "until it is stopped"
	if !run.Ends.IsZero() {
		ends = "until " + run.Ends.UTC().Format("2006-01-02 15:04") + " UTC"
	}
	body := fmt.Sprintf("Your token \"%s\" (%s…) started run %d of your analysis \"%s\" at %s UTC. It runs %s.\n\nThe run:\n%s/runs/%s\n\nWhat the token may do, and its limits, are under AI, Access:\n%s/ai/access\n",
		t.Name, t.Hint, run.Number, a.Name, run.Started.UTC().Format("2006-01-02 15:04"), ends, s.cfg.BaseURL, strconv.FormatInt(run.ID, 10), s.cfg.BaseURL)
	go func() {
		if err := s.mail.Send(u.Email, fmt.Sprintf("A token started run %d of %s", run.Number, a.Name), body); err != nil {
			log.Printf("runs: telling of run %d started by token %d: %v", run.ID, t.ID, err)
		}
	}()
}
