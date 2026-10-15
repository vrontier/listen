package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// An analysis is the member's own object with 1 to n sources; its settings
// are kept per source, and one revision covers all of them (design.md,
// "Analyses and sources").

const maxSources = 8

// defaultSettings is what a source gets when it joins an analysis.
var defaultSettings = json.RawMessage(`{"memory":{"mode":"disk"}}`)

// ruleRow is one row of a filter form.
type ruleRow struct {
	Kind  string
	Hz    string
	Odd   bool
	Tol   string
	Drift string
	Text  string // as the engine writes it, once saved
}

// settingsForm is one source's settings as the form holds them.
type settingsForm struct {
	MinHz, MaxHz, TransientK, TransientGap string
	MemoryMode                             string
	Rules                                  []ruleRow
}

func fmtNum(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'g', -1, 64)
}

func fmtOpt(v float64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// formFromSpec fills the form from stored settings.
func formFromSpec(raw json.RawMessage) settingsForm {
	var spec engine.AnalysisSpec
	json.Unmarshal(raw, &spec)
	f := settingsForm{
		MinHz: fmtNum(spec.Settings.MinHz), MaxHz: fmtNum(spec.Settings.MaxHz),
		TransientK: fmtNum(spec.Settings.TransientK), TransientGap: fmtNum(spec.Settings.TransientGapS),
		MemoryMode: spec.Memory.Mode,
	}
	if f.MemoryMode == "" {
		f.MemoryMode = engine.MemoryDisk
	}
	for _, r := range spec.Rules {
		f.Rules = append(f.Rules, ruleRow{Kind: r.Kind, Hz: fmtOpt(r.Hz), Odd: r.Odd, Tol: fmtOpt(r.TolHz), Drift: fmtOpt(r.DriftPercent)})
	}
	return f
}

// padRules leaves a few empty rows to add rules in.
func (f *settingsForm) padRules() {
	for i := 0; i < 2 || len(f.Rules) < 3; i++ {
		f.Rules = append(f.Rules, ruleRow{Kind: engine.RuleComb})
	}
}

// sourceView is one source on an analysis page.
type sourceView struct {
	Stream    catalogue.Stream
	Prefix    string // of its form fields
	Form      settingsForm
	Running   bool   // the stream is in a state that connects
	Live      bool   // and the page shows it live: in a run or a preview
	TrialLive bool   // a trial runs next to the run on this source
	TrialPath string // of its live data, …/trial/live/<source>
	TrialData string // …/trial/data/<source>/
	Engine    *engine.Analysis
	EngineErr string
	Memories  []engine.Memory
	LivePath  string
	DataPath  string
}

// pick is one source the member may add to an analysis.
type pick struct {
	Stream  catalogue.Stream
	Checked bool
	Shared  bool // shared with the member by its owner
}

type analysisData struct {
	Analysis    catalogue.Analysis
	Name        string
	Description string
	Sources     []sourceView
	Picks       []pick
	Compare     []compareChoice // from two sources on
	Groups      bool            // the engine has groups
	Comparing   bool            // in a run, two or more sources run, and are compared
	Preview     time.Time       // end of the preview going on, if any
	Trial       *trial          // the trial next to the run, if any
	LastRun     *runView        // the latest run that ended
	Revisions   []catalogue.Revision
	Runs        []runView
	Run         *runView // the run going on, if any
	RunLengths  any
	Audit       []catalogue.AuditEntry
	Errs        map[string]string
	Warnings    []string
	MaxSources  int
	Err         string
}

func (d analysisData) FormError() string { return d.Err }

func prefixOf(streamID int64) string { return "s" + strconv.FormatInt(streamID, 10) + "_" }

func analysisPath(a catalogue.Analysis) string { return "/analyses/" + strconv.FormatInt(a.ID, 10) }

func running(st catalogue.Stream) bool {
	return st.State == catalogue.StreamDraft || st.State == catalogue.StreamProduction
}

// --- the list and a new analysis ---

type analysesData struct {
	Analyses []catalogue.Analysis
	Running  map[int64]bool // in a run now
}

func (s *Server) analyses(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	as, err := s.db.AnalysesOf(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := analysesData{Analyses: as, Running: map[int64]bool{}}
	for _, a := range as {
		if _, ok, err := s.db.ActiveRun(r.Context(), a.ID); err == nil && ok {
			d.Running[a.ID] = true
		}
	}
	s.render(w, r, 200, "analyses", "Analyses", d)
}

type newAnalysisData struct {
	Name        string
	Description string
	Picks       []pick
	MaxSources  int
	Errs        map[string]string
	Err         string
}

func (d newAnalysisData) FormError() string { return d.Err }

// picks lists the sources a member may use, marking the chosen ones: their
// own and those shared with them.
func (s *Server) picks(r *http.Request, chosen map[int64]bool) ([]pick, error) {
	sess, _ := sessionFrom(r)
	streams, err := s.db.StreamsOf(r.Context(), sess.User.ID)
	if err != nil {
		return nil, err
	}
	shared, err := s.db.SharedWith(r.Context(), sess.User.ID)
	if err != nil {
		return nil, err
	}
	streams = append(streams, shared...)
	var out []pick
	for _, st := range streams {
		out = append(out, pick{Stream: st, Checked: chosen[st.ID], Shared: st.OwnerID != sess.User.ID})
	}
	return out, nil
}

func (s *Server) analysisNewForm(w http.ResponseWriter, r *http.Request) {
	chosen := map[int64]bool{}
	if slug := r.URL.Query().Get("source"); slug != "" {
		if st, err := s.db.StreamBySlug(r.Context(), slug); err == nil {
			chosen[st.ID] = true
		}
	}
	p, err := s.picks(r, chosen)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "analysis-new", "New analysis", newAnalysisData{Picks: p, MaxSources: maxSources, Errs: map[string]string{}})
}

// chosenSources reads the ticked sources and keeps those the member may use,
// in the order of the list.
func (s *Server) chosenSources(r *http.Request) ([]int64, map[int64]bool, error) {
	r.ParseForm()
	want := map[int64]bool{}
	for _, v := range r.PostForm["source"] {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			want[id] = true
		}
	}
	p, err := s.picks(r, nil)
	if err != nil {
		return nil, nil, err
	}
	var ids []int64
	chosen := map[int64]bool{}
	for _, x := range p {
		if want[x.Stream.ID] {
			ids = append(ids, x.Stream.ID)
			chosen[x.Stream.ID] = true
		}
	}
	return ids, chosen, nil
}

func checkAnalysisTexts(name, description string, errs map[string]string) {
	if n := utf8.RuneCountInString(name); n < 2 || n > 80 {
		errs["name"] = "between 2 and 80 characters"
	}
	if utf8.RuneCountInString(description) > 2000 {
		errs["description"] = "at most 2000 characters"
	}
}

func (s *Server) analysisNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	d := newAnalysisData{Name: strings.TrimSpace(r.PostFormValue("name")), Description: strings.TrimSpace(r.PostFormValue("description")),
		MaxSources: maxSources, Errs: map[string]string{}}
	ids, chosen, err := s.chosenSources(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Picks, err = s.picks(r, chosen); err != nil {
		s.fail(w, r, err)
		return
	}
	checkAnalysisTexts(d.Name, d.Description, d.Errs)
	switch {
	case len(ids) == 0:
		d.Errs["source"] = "choose at least one source"
	case len(ids) > maxSources:
		d.Errs["source"] = fmt.Sprintf("at most %d sources", maxSources)
	}
	if len(d.Errs) > 0 {
		d.Err = "Please check the marked fields."
		s.render(w, r, 400, "analysis-new", "New analysis", d)
		return
	}
	settings := map[int64]json.RawMessage{}
	for _, id := range ids {
		settings[id] = defaultSettings
	}
	a, err := s.db.CreateAnalysis(r.Context(), sess.User.ID, d.Name, d.Description, ids, settings)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	flash := "Analysis created. Start a run to record, or preview it for ten minutes."
	if err := s.pushSources(r, a.Sources); err != nil {
		flash += " The engine has not taken it yet; it will be sent again."
	}
	s.redirect(w, r, analysisPath(a), flash)
}

// pushSources puts the given streams, with all analyses using them, into the
// engine, then the groups, and has the recordings of runs follow.
func (s *Server) pushSources(r *http.Request, sources []catalogue.AnalysisSource) error {
	defer s.data.refresh(r.Context())
	return s.pushAll(r.Context(), sources)
}

// pushAll puts the given streams and the groups into the engine.
func (s *Server) pushAll(ctx context.Context, sources []catalogue.AnalysisSource) error {
	defer s.syncGroups(ctx)
	var first error
	for _, src := range sources {
		st, err := s.db.Stream(ctx, src.Stream.ID)
		if err == nil {
			err = s.sync.push(ctx, st)
		}
		if err != nil {
			log.Printf("push %s: %v", src.Stream.EngineID(), err)
			if first == nil {
				first = err
			}
		}
	}
	return first
}

// --- an analysis's page ---

func (s *Server) ownAnalysis(w http.ResponseWriter, r *http.Request) (catalogue.Analysis, bool) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.db.Analysis(r.Context(), id)
	if err != nil || a.OwnerID != sess.User.ID {
		if err != nil && !isNotFound(err) {
			s.fail(w, r, err)
		} else {
			s.notFound(w, r)
		}
		return a, false
	}
	return a, true
}

func (s *Server) analysisData(r *http.Request, a catalogue.Analysis) (analysisData, error) {
	d := analysisData{Analysis: a, Name: a.Name, Description: a.Description, Errs: map[string]string{}, MaxSources: maxSources}
	base := analysisPath(a)
	_, inRun, err := s.db.ActiveRun(r.Context(), a.ID)
	if err != nil {
		return d, err
	}
	preview, previewing := s.sync.preview(a.ID)
	if previewing && !inRun {
		d.Preview = preview
	}
	if t, ok := s.trialOf(r, a); ok {
		d.Trial = &t
	}
	chosen := map[int64]bool{}
	for _, src := range a.Sources {
		chosen[src.Stream.ID] = true
		sid := strconv.FormatInt(src.Stream.ID, 10)
		v := sourceView{Stream: src.Stream, Prefix: prefixOf(src.Stream.ID), Form: formFromSpec(src.Spec), Running: running(src.Stream),
			LivePath: base + "/live/" + sid, DataPath: base + "/data/" + sid + "/"}
		v.Live = v.Running && (inRun || !d.Preview.IsZero())
		if d.Trial != nil {
			// The form shows what is on trial, to be changed further.
			if raw, ok := d.Trial.Settings[src.Stream.ID]; ok {
				v.Form = formFromSpec(raw)
				v.TrialLive = v.Running
				v.TrialPath = base + "/trial/live/" + sid
				v.TrialData = base + "/trial/data/" + sid + "/"
			}
		}
		if v.Running && inRun {
			ea, err := s.eng.Analysis(r.Context(), src.Stream.EngineID(), a.EngineID())
			if err != nil {
				v.EngineErr = "The engine does not report this source of the analysis at the moment."
			} else {
				v.Engine = &ea
				for i, rule := range ea.Rules {
					if i < len(v.Form.Rules) {
						v.Form.Rules[i].Text = rule.Text
					}
				}
				if ms, err := s.memories(r.Context(), a, src); err == nil {
					v.Memories = ms
				}
			}
		} else {
			v.EngineErr = "Memories are listed while the analysis runs."
		}
		d.Sources = append(d.Sources, v)
	}
	if d.Picks, err = s.picks(r, chosen); err != nil {
		return d, err
	}
	if d.Revisions, err = s.db.Revisions(r.Context(), a.ID); err != nil {
		return d, err
	}
	if d.Runs, err = s.runViews(r.Context(), a, d.Revisions); err != nil {
		return d, err
	}
	for i := range d.Runs {
		switch {
		case d.Runs[i].Active && d.Run == nil:
			d.Run = &d.Runs[i]
		case !d.Runs[i].Active && d.LastRun == nil:
			d.LastRun = &d.Runs[i]
		}
	}
	d.RunLengths = runLengths
	_, _, info := s.sync.status()
	d.Groups = info.Groups()
	d.Comparing = inRun && s.compares(a)
	compare := a.Spec.Compare
	if compare == nil {
		compare = catalogue.DefaultCompare // saved before there was a choice
	}
	d.Compare = compareChoices(info, compare, len(a.Sources))
	d.Audit, err = s.db.AuditOf(r.Context(), "analysis", a.ID)
	return d, err
}

func (s *Server) renderAnalysis(w http.ResponseWriter, r *http.Request, status int, d analysisData) {
	for i := range d.Sources {
		d.Sources[i].Form.padRules()
	}
	s.render(w, r, status, "analysis", d.Analysis.Name, d)
}

func (s *Server) analysisPage(w http.ResponseWriter, r *http.Request) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	d, err := s.analysisData(r, a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.renderAnalysis(w, r, 200, d)
}

// analysisTexts saves name and description, which are not part of a
// revision.
func (s *Server) analysisTexts(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	name, desc := strings.TrimSpace(r.PostFormValue("name")), strings.TrimSpace(r.PostFormValue("description"))
	errs := map[string]string{}
	checkAnalysisTexts(name, desc, errs)
	if len(errs) > 0 {
		d, err := s.analysisData(r, a)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Name, d.Description, d.Errs, d.Err = name, desc, errs, "Please check the marked fields."
		s.renderAnalysis(w, r, 400, d)
		return
	}
	if err := s.db.UpdateAnalysisTexts(r.Context(), a.ID, sess.User.ID, name, desc); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, analysisPath(a), "Saved.")
}

var rulePathRE = regexp.MustCompile(`^rules\[(\d+)\]\.(\w+)$`)

// readSettings turns one source's part of the form into its settings. Only
// the form's own syntax is checked here (is it a number); whether a value is
// allowed is the engine's to say. Error keys carry the prefix.
func readSettings(r *http.Request, prefix string) (settingsForm, engine.AnalysisSpec, []int, map[string]string) {
	errs := map[string]string{}
	v := func(k string) string { return strings.TrimSpace(r.PostFormValue(prefix + k)) }
	f := settingsForm{MinHz: v("min_hz"), MaxHz: v("max_hz"), TransientK: v("transient_k"), TransientGap: v("transient_gap_s"), MemoryMode: v("memory_mode")}
	num := func(field, val string) *float64 {
		if val == "" {
			return nil
		}
		x, err := strconv.ParseFloat(strings.Replace(val, ",", ".", 1), 64)
		if err != nil {
			errs[prefix+field] = "a number"
			return nil
		}
		return &x
	}
	var spec engine.AnalysisSpec
	spec.Settings = engine.Settings{
		MinHz: num("min_hz", f.MinHz), MaxHz: num("max_hz", f.MaxHz),
		TransientK: num("transient_k", f.TransientK), TransientGapS: num("transient_gap_s", f.TransientGap),
	}
	spec.Memory.Mode = f.MemoryMode

	n, _ := strconv.Atoi(v("rule_count"))
	n = min(n, 64)
	var rows []int // form row of each rule sent
	for i := 0; i < n; i++ {
		k := strconv.Itoa(i)
		row := ruleRow{Kind: v("rule_kind_" + k), Hz: v("rule_hz_" + k), Odd: v("rule_odd_"+k) == "on", Tol: v("rule_tol_" + k), Drift: v("rule_drift_" + k)}
		if row.Hz == "" && row.Tol == "" && row.Drift == "" {
			continue
		}
		f.Rules = append(f.Rules, row)
		fi := strconv.Itoa(len(f.Rules) - 1)
		rule := engine.Rule{Kind: row.Kind, Odd: row.Odd}
		if p := num("rule_hz_"+fi, row.Hz); p != nil {
			rule.Hz = *p
		} else if row.Hz == "" {
			errs[prefix+"rule_hz_"+fi] = "a frequency"
		}
		if p := num("rule_tol_"+fi, row.Tol); p != nil {
			rule.TolHz = *p
		}
		if p := num("rule_drift_"+fi, row.Drift); p != nil {
			rule.DriftPercent = *p
		}
		spec.Rules = append(spec.Rules, rule)
		rows = append(rows, len(f.Rules)-1)
	}
	return f, spec, rows, errs
}

// formField maps an engine error path to the form field it concerns.
func formField(prefix, path string, rows []int) string {
	if m := rulePathRE.FindStringSubmatch(path); m != nil {
		i, _ := strconv.Atoi(m[1])
		if i < len(rows) {
			field := map[string]string{"hz": "hz", "kind": "hz", "odd": "hz", "tol_hz": "tol", "drift_percent": "drift"}[m[2]]
			if field == "" {
				field = "hz"
			}
			return prefix + "rule_" + field + "_" + strconv.Itoa(rows[i])
		}
	}
	switch path {
	case "settings.min_hz":
		return prefix + "min_hz"
	case "settings.max_hz":
		return prefix + "max_hz"
	case "settings.transient_k":
		return prefix + "transient_k"
	case "settings.transient_gap_s":
		return prefix + "transient_gap_s"
	case "memory.mode":
		return prefix + "memory_mode"
	}
	return prefix + "rules"
}

// analysisSettings saves the sources and their settings as a new revision.
// Each source's settings are checked by the engine at that source's rate.
func (s *Server) analysisSettings(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	// A run holds one configuration: nothing of a revision changes while
	// it lasts (design.md, Runs).
	if _, inRun, err := s.db.ActiveRun(r.Context(), a.ID); err != nil || inRun {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.redirect(w, r, analysisPath(a), "Not saved: the sources and their settings are fixed while the run lasts. Stop the run to change them.")
		return
	}
	ids, _, err := s.chosenSources(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	errs := map[string]string{}
	switch {
	case len(ids) == 0:
		errs["source"] = "an analysis needs at least one source"
	case len(ids) > maxSources:
		errs["source"] = fmt.Sprintf("at most %d sources", maxSources)
	}
	ready, problem, info := s.sync.status()
	compare, cerr := readCompare(r, ids, info)
	if cerr != "" {
		errs["compare"] = cerr
	}
	if compare == nil && len(ids) >= 2 {
		compare = a.Spec.Compare
		if compare == nil {
			compare = catalogue.DefaultCompare
		}
	}
	forms, settings, warns, err := s.checkSettings(r, a, ids, errs, ready, problem)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(errs) > 0 {
		d, err := s.analysisData(r, a)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for i := range d.Sources {
			if f, ok := forms[d.Sources[i].Stream.ID]; ok {
				d.Sources[i].Form = f
			}
		}
		chosen := map[int64]bool{}
		for _, id := range ids {
			chosen[id] = true
		}
		for i := range d.Picks {
			d.Picks[i].Checked = chosen[d.Picks[i].Stream.ID]
		}
		d.Compare = compareChoices(info, compare, len(ids))
		d.Errs, d.Warnings, d.Err = errs, warns, "Not saved. Please check the marked fields."
		s.renderAnalysis(w, r, 422, d)
		return
	}

	if sameRevision(a, ids, settings, compare) {
		s.redirect(w, r, analysisPath(a), "Nothing changed.")
		return
	}
	before := a.Sources
	a, err = s.db.SaveRevision(r.Context(), a.ID, sess.User.ID, ids, settings, compare)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	flash := fmt.Sprintf("Saved as revision %d.", a.Revision)
	if len(warns) > 0 {
		flash += " Note: " + strings.Join(warns, " ")
	}
	// Push every stream the analysis used before or uses now, so that a
	// source taken out loses its engine analysis.
	touched := append(append([]catalogue.AnalysisSource{}, before...), a.Sources...)
	if err := s.pushSources(r, touched); err != nil {
		flash += " The engine has not taken all of it yet; it will be sent again."
	}
	s.redirect(w, r, analysisPath(a), flash)
}

// checkSettings reads each source's settings from the form and has the
// engine check them at that source's rate. Problems go into errs, under the
// form's field names. A source that joins the analysis starts with the
// defaults.
func (s *Server) checkSettings(r *http.Request, a catalogue.Analysis, ids []int64, errs map[string]string, ready bool, problem string) (map[int64]settingsForm, map[int64]json.RawMessage, []string, error) {
	forms := map[int64]settingsForm{}
	settings := map[int64]json.RawMessage{}
	var warns []string
	for _, id := range ids {
		src, inAnalysis := a.Source(id)
		if !inAnalysis {
			settings[id] = defaultSettings
			continue
		}
		prefix := prefixOf(id)
		form, spec, rows, ferrs := readSettings(r, prefix)
		forms[id] = form
		for k, v := range ferrs {
			errs[k] = v
		}
		if len(ferrs) > 0 {
			continue
		}
		if !ready {
			errs[prefix+"rules"] = "cannot be checked now: " + problem
			continue
		}
		v, err := s.eng.Validate(r.Context(), engine.ValidateRequest{SampleRate: src.Stream.SampleRate, AnalysisSpec: spec})
		if err != nil {
			return nil, nil, nil, err
		}
		for _, x := range v.Warnings {
			warns = append(warns, src.Stream.Texts.Name+": "+x.Message)
		}
		if !v.Valid {
			for _, e := range v.Errors {
				if f := formField(prefix, e.Path, rows); errs[f] == "" {
					errs[f] = e.Message
				}
			}
			continue
		}
		settings[id], _ = json.Marshal(spec)
	}
	return forms, settings, warns, nil
}

// sameRevision reports whether sources and settings are what the analysis
// already has.
func sameRevision(a catalogue.Analysis, ids []int64, settings map[int64]json.RawMessage, compare []string) bool {
	if len(ids) != len(a.Sources) || len(ids) >= 2 && !slices.Equal(compare, a.Spec.Compare) {
		return false
	}
	for i, id := range ids {
		if a.Sources[i].Stream.ID != id || !bytes.Equal(canon(settings[id]), canon(a.Sources[i].Spec)) {
			return false
		}
	}
	return true
}

// canon re-encodes JSON so that two equal settings compare equal.
func canon(b []byte) []byte {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return b
	}
	out, _ := json.Marshal(v)
	return out
}

func (s *Server) analysisDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	if r.PostFormValue("confirm") != a.Name {
		s.redirect(w, r, analysisPath(a), "Not deleted: type the analysis's title exactly to confirm.")
		return
	}
	if err := s.db.DeleteAnalysis(r.Context(), a.ID, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	// Outside a run the analysis is not in the engine, but its memories
	// are: it is put back for a moment so that they can be deleted with it.
	for _, src := range a.Sources {
		if !running(src.Stream) {
			continue
		}
		if _, err := s.eng.PutAnalysis(r.Context(), src.Stream.EngineID(), a.EngineID(), analysisBody(src, a)); err != nil {
			log.Printf("delete %s/%s: %v", src.Stream.EngineID(), a.EngineID(), err)
			continue
		}
		if err := s.eng.DeleteAnalysis(r.Context(), src.Stream.EngineID(), a.EngineID(), true); err != nil && !engine.IsCode(err, "not_found") {
			log.Printf("delete %s/%s: %v", src.Stream.EngineID(), a.EngineID(), err)
		}
	}
	s.sync.endPreview(a.ID)
	s.pushSources(r, a.Sources)
	s.redirect(w, r, "/analyses", "Deleted "+a.Name+", with its memories.")
}

// --- live data, data, export ---

// ownSource finds the source named in the path among the analysis's own.
func (s *Server) ownSource(w http.ResponseWriter, r *http.Request) (catalogue.Analysis, catalogue.AnalysisSource, bool) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return a, catalogue.AnalysisSource{}, false
	}
	sid, _ := strconv.ParseInt(r.PathValue("source"), 10, 64)
	src, ok := a.Source(sid)
	if !ok {
		http.Error(w, "no such source in this analysis", http.StatusNotFound)
	}
	return a, src, ok
}

// analysisLive passes the events of one source of the analysis on to a page.
func (s *Server) analysisLive(w http.ResponseWriter, r *http.Request) {
	a, src, ok := s.ownSource(w, r)
	if !ok {
		return
	}
	id, ok := s.liveID(r, a, src)
	if !ok {
		http.Error(w, "the analysis is neither running nor previewed", http.StatusConflict)
		return
	}
	sess, _ := sessionFrom(r)
	rd := &reader{ch: make(chan []byte, 256), person: true, who: sess.User.Name}
	leave := s.live.join(src.Stream.EngineID(), id, rd)
	defer leave()
	serveReader(w, r, rd)
}

// liveID is the engine analysis a page reads of a source: the analysis
// during a run, its preview during a preview, none otherwise; under
// …/trial/… the trial next to the run.
func (s *Server) liveID(r *http.Request, a catalogue.Analysis, src catalogue.AnalysisSource) (string, bool) {
	if !running(src.Stream) {
		return "", false
	}
	if strings.Contains(r.URL.Path, "/trial/") {
		t, ok := s.trialOf(r, a)
		if _, has := t.Settings[src.Stream.ID]; ok && has {
			return trialID(a), true
		}
		return "", false
	}
	if _, inRun, _ := s.db.ActiveRun(r.Context(), a.ID); inRun {
		return a.EngineID(), true
	}
	if _, ok := s.sync.preview(a.ID); ok {
		return previewID(a), true
	}
	return "", false
}

// analysisProxy passes one of the engine's data paths of one source on.
func (s *Server) analysisProxy(w http.ResponseWriter, r *http.Request) {
	a, src, ok := s.ownSource(w, r)
	if !ok {
		return
	}
	what := r.PathValue("what")
	switch {
	case what == "state", what == "motifs", what == "memories", what == "history/events", what == "history/features":
	case strings.HasPrefix(what, "motifs/") && !strings.Contains(what[7:], "/"):
	default:
		http.Error(w, "unknown", http.StatusNotFound)
		return
	}
	q := url.Values{}
	for _, k := range []string{"from", "to", "type", "limit", "window", "memory"} {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
	id, ok := s.liveID(r, a, src)
	if !ok {
		http.Error(w, "the analysis is neither running nor previewed", http.StatusConflict)
		return
	}
	raw, err := s.eng.Data(r.Context(), src.Stream.EngineID(), id, what, q)
	if err != nil {
		status := http.StatusBadGateway
		if engine.IsCode(err, "not_found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(raw)
}

// --- comparing the sources ---

// compareChoice is one group analysis offered on the settings form.
type compareChoice struct {
	ID      string
	Label   string
	Checked bool
	Blocked string // why it cannot be chosen now
}

var compareLabels = map[string]string{
	"activity":    "activity side by side: level, flux and impulse rate per second, and how the sources correlate",
	"clock":       "station clocks: the measured offset between each pair of stations",
	"coincidence": "coincidence: impulses heard by two or three stations within their travel time",
}

// compareChoices lists the group analyses the engine offers, marking the
// chosen ones. Analyses that need station time are blocked until the
// engine reads it.
func compareChoices(info engine.Info, chosen []string, sources int) []compareChoice {
	if sources < 2 || !info.Groups() {
		return nil
	}
	var out []compareChoice
	for _, c := range info.Capabilities.GroupAnalyses {
		ch := compareChoice{ID: c.ID, Label: compareLabels[c.ID], Checked: slices.Contains(chosen, c.ID)}
		if ch.Label == "" {
			ch.Label = c.ID
		}
		if c.Timing == "milliseconds" && !info.Capabilities.StationTime {
			ch.Blocked = "needs station time, which the engine does not read yet"
			ch.Checked = false
		}
		out = append(out, ch)
	}
	return out
}

// readCompare reads the chosen group analyses. With fewer than two sources
// there is nothing to compare. Before the engine offers groups the
// analysis keeps what it had, so nothing is lost while it cannot be shown.
func readCompare(r *http.Request, ids []int64, info engine.Info) ([]string, string) {
	if len(ids) < 2 {
		return nil, ""
	}
	if !info.Groups() || r.PostFormValue("compare_form") == "" {
		// No choice was shown: the engine has no groups, or the analysis
		// had one source until now. It compares as before, or as default.
		return nil, ""
	}
	choices := compareChoices(info, nil, len(ids))
	var out []string
	for _, v := range r.PostForm["compare"] {
		for _, c := range choices {
			if c.ID != v || slices.Contains(out, v) {
				continue
			}
			if c.Blocked != "" {
				return nil, c.ID + " " + c.Blocked
			}
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil, "choose at least one way to compare the sources; activity needs nothing more than the sources"
	}
	return out, ""
}

// compares reports whether the analysis's sources are compared in the
// engine now: it is in a run (or its data set is being frozen), two or
// more of its sources run, and the engine has groups.
func (s *Server) compares(a catalogue.Analysis) bool {
	if _, _, info := s.sync.status(); !info.Groups() {
		return false
	}
	if _, inRun, _ := s.db.ActiveRun(context.Background(), a.ID); !inRun && !s.sync.isHeld(a.ID) {
		return false
	}
	n := 0
	for _, src := range a.Sources {
		if running(src.Stream) {
			n++
		}
	}
	return n >= 2
}

// analysisCompareLive passes the group's events on to a page.
func (s *Server) analysisCompareLive(w http.ResponseWriter, r *http.Request) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	if !s.compares(a) {
		http.Error(w, "the sources of this analysis are not compared now", http.StatusConflict)
		return
	}
	sess, _ := sessionFrom(r)
	rd := &reader{ch: make(chan []byte, 256), person: true, who: sess.User.Name}
	leave := s.live.joinGroup(a.GroupID(), rd)
	defer leave()
	serveReader(w, r, rd)
}

// analysisCompareState passes the group's state on: its status, the last
// ten minutes of activity and the latest correlations.
func (s *Server) analysisCompareState(w http.ResponseWriter, r *http.Request) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	raw, err := s.eng.GroupData(r.Context(), a.GroupID(), "state", nil)
	if err != nil {
		status := http.StatusBadGateway
		if engine.IsCode(err, "not_found") {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(raw)
}

// serveReader writes a reader's events to the response until the page goes.
func serveReader(w http.ResponseWriter, r *http.Request, rd *reader) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	io.WriteString(w, ": connected\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-rd.ch:
			if _, err := w.Write(msg); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
