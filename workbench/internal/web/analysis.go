package web

import (
	"bytes"
	"encoding/csv"
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
	Running   bool // the stream is in a state that connects
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
}

type analysisData struct {
	Analysis    catalogue.Analysis
	Name        string
	Description string
	Sources     []sourceView
	Picks       []pick
	Compare     []compareChoice // from two sources on
	Groups      bool            // the engine has groups
	Comparing   bool            // two or more sources run, and are compared
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

// picks lists the sources a member may use, marking the chosen ones. For
// now these are their own; sources shared with them follow with sharing.
func (s *Server) picks(r *http.Request, chosen map[int64]bool) ([]pick, error) {
	sess, _ := sessionFrom(r)
	streams, err := s.db.StreamsOf(r.Context(), sess.User.ID)
	if err != nil {
		return nil, err
	}
	var out []pick
	for _, st := range streams {
		out = append(out, pick{Stream: st, Checked: chosen[st.ID]})
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
	flash := "Analysis created. Its sources connect while this page is open."
	if err := s.pushSources(r, a.Sources); err != nil {
		flash += " The engine has not taken it yet; it will be sent again."
	}
	s.redirect(w, r, analysisPath(a), flash)
}

// pushSources puts the given streams, with all analyses using them, into the
// engine, and then the groups.
func (s *Server) pushSources(r *http.Request, sources []catalogue.AnalysisSource) error {
	defer s.syncGroups(r.Context())
	var first error
	for _, src := range sources {
		st, err := s.db.Stream(r.Context(), src.Stream.ID)
		if err == nil {
			err = s.sync.push(r.Context(), st)
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
	chosen := map[int64]bool{}
	for _, src := range a.Sources {
		chosen[src.Stream.ID] = true
		sid := strconv.FormatInt(src.Stream.ID, 10)
		v := sourceView{Stream: src.Stream, Prefix: prefixOf(src.Stream.ID), Form: formFromSpec(src.Spec), Running: running(src.Stream),
			LivePath: base + "/live/" + sid, DataPath: base + "/data/" + sid + "/"}
		if v.Running {
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
				if raw, err := s.eng.Data(r.Context(), src.Stream.EngineID(), a.EngineID(), "memories", nil); err == nil {
					var m struct {
						Memories []engine.Memory `json:"memories"`
					}
					json.Unmarshal(raw, &m)
					v.Memories = m.Memories
				}
			}
		}
		d.Sources = append(d.Sources, v)
	}
	var err error
	if d.Picks, err = s.picks(r, chosen); err != nil {
		return d, err
	}
	if d.Revisions, err = s.db.Revisions(r.Context(), a.ID); err != nil {
		return d, err
	}
	if d.Runs, err = s.runViews(r.Context(), a, d.Revisions); err != nil {
		return d, err
	}
	if len(d.Runs) > 0 && d.Runs[0].Active {
		d.Run = &d.Runs[0]
	}
	d.RunLengths = runLengths
	_, _, info := s.sync.status()
	d.Groups = info.Groups()
	d.Comparing = s.compares(a)
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
	forms := map[int64]settingsForm{}
	settings := map[int64]json.RawMessage{}
	var warns []string
	for _, id := range ids {
		src, inAnalysis := a.Source(id)
		if !inAnalysis {
			settings[id] = defaultSettings // a source that joins starts with the defaults
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
			s.fail(w, r, err)
			return
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
	for _, src := range a.Sources {
		if err := s.eng.DeleteAnalysis(r.Context(), src.Stream.EngineID(), a.EngineID(), true); err != nil && !engine.IsCode(err, "not_found") {
			log.Printf("delete %s/%s: %v", src.Stream.EngineID(), a.EngineID(), err)
		}
	}
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
	if !running(src.Stream) {
		http.Error(w, "the source is "+src.Stream.State, http.StatusConflict)
		return
	}
	sess, _ := sessionFrom(r)
	rd := &reader{ch: make(chan []byte, 256), person: true, who: sess.User.Name}
	leave := s.live.join(src.Stream.EngineID(), a.EngineID(), rd)
	defer leave()
	serveReader(w, r, rd)
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
	raw, err := s.eng.Data(r.Context(), src.Stream.EngineID(), a.EngineID(), what, q)
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

// storedEvents fetches the stored events of one source for a period. The
// engine answers at most 2000 events per request; this pages through.
func (s *Server) storedEvents(r *http.Request, a catalogue.Analysis, st catalogue.Stream, from, to time.Time, typ string) ([]json.RawMessage, error) {
	var events []json.RawMessage
	cursor := from
	for page := 0; page < 100; page++ {
		q := url.Values{"from": {cursor.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}, "limit": {"2000"}}
		if typ != "" {
			q.Set("type", typ)
		}
		raw, err := s.eng.Data(r.Context(), st.EngineID(), a.EngineID(), "history/events", q)
		if err != nil {
			return nil, err
		}
		var got struct {
			Events []json.RawMessage `json:"events"`
		}
		json.Unmarshal(raw, &got)
		events = append(events, got.Events...)
		if len(got.Events) < 2000 {
			break
		}
		var last struct {
			Timestamp time.Time `json:"timestamp"`
		}
		json.Unmarshal(got.Events[len(got.Events)-1], &last)
		if !last.Timestamp.After(cursor) {
			break
		}
		cursor = last.Timestamp.Add(time.Nanosecond)
	}
	return events, nil
}

// analysisExport writes the stored events of a period, of every source, as
// CSV (with a column naming the source) or JSON.
func (s *Server) analysisExport(w http.ResponseWriter, r *http.Request) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	now := time.Now().UTC()
	from, to := now.Add(-24*time.Hour), now
	parse := func(v string) (time.Time, bool) {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
			if t, err := time.ParseInLocation(layout, v, time.UTC); err == nil {
				return t.UTC(), true
			}
		}
		return time.Time{}, false
	}
	if t, ok := parse(r.URL.Query().Get("from")); ok {
		from = t
	}
	if t, ok := parse(r.URL.Query().Get("to")); ok {
		to = t
	}
	if !to.After(from) || to.Sub(from) > 31*24*time.Hour {
		http.Error(w, "the period must be positive and at most 31 days", http.StatusBadRequest)
		return
	}
	typ := r.URL.Query().Get("type")

	type sourceExport struct {
		Stream catalogue.Stream
		Events []json.RawMessage
		Motifs json.RawMessage
	}
	var all []sourceExport
	for _, src := range a.Sources {
		x := sourceExport{Stream: src.Stream, Events: []json.RawMessage{}, Motifs: json.RawMessage(`null`)}
		if running(src.Stream) {
			ev, err := s.storedEvents(r, a, src.Stream, from, to, typ)
			if err != nil {
				http.Error(w, "the engine did not answer: "+err.Error(), http.StatusBadGateway)
				return
			}
			if ev != nil {
				x.Events = ev
			}
			if m, err := s.eng.Data(r.Context(), src.Stream.EngineID(), a.EngineID(), "motifs", nil); err == nil {
				x.Motifs = m
			}
		}
		all = append(all, x)
	}

	name := fmt.Sprintf("%s-%s-%s", catalogue.Slugify(a.Name), a.Label(), from.Format("20060102T1504Z"))
	if r.URL.Query().Get("format") == "compare-csv" {
		if !s.compares(a) {
			http.Error(w, "the sources of this analysis are not compared", http.StatusConflict)
			return
		}
		s.exportComparison(w, r, a, from, to, name)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		fmt.Fprintf(w, "# analysis: %s, %s\n# period: %s to %s\n", a.Name, a.Label(), from.Format(time.RFC3339), to.Format(time.RFC3339))
		for _, x := range all {
			fmt.Fprintf(w, "# source %s: %s, sample rate %d Hz, settings %s\n", x.Stream.Slug, x.Stream.Texts.Name, x.Stream.SampleRate, a.Spec.Sources[strconv.FormatInt(x.Stream.ID, 10)])
		}
		cw := csv.NewWriter(w)
		cw.Write([]string{"source", "timestamp", "position_s", "type", "id", "status", "fundamental_hz", "strength", "duration_s", "motif_id"})
		for _, x := range all {
			for _, ev := range x.Events {
				var e struct {
					Type      string  `json:"type"`
					Timestamp string  `json:"timestamp"`
					Position  float64 `json:"position_s"`
					Payload   struct {
						ID          string   `json:"id"`
						Status      string   `json:"status"`
						Fundamental *float64 `json:"fundamental_hz"`
						Strength    *float64 `json:"strength"`
						DurationS   *float64 `json:"duration_s"`
						DurationMs  *float64 `json:"duration_ms"`
						MotifID     string   `json:"motif_id"`
					} `json:"payload"`
				}
				json.Unmarshal(ev, &e)
				opt := func(p *float64) string {
					if p == nil {
						return ""
					}
					return strconv.FormatFloat(*p, 'g', -1, 64)
				}
				dur := opt(e.Payload.DurationS)
				if dur == "" && e.Payload.DurationMs != nil {
					dur = strconv.FormatFloat(*e.Payload.DurationMs/1000, 'g', -1, 64)
				}
				cw.Write([]string{x.Stream.Slug, e.Timestamp, strconv.FormatFloat(e.Position, 'f', 3, 64), e.Type, e.Payload.ID, e.Payload.Status,
					opt(e.Payload.Fundamental), opt(e.Payload.Strength), dur, e.Payload.MotifID})
			}
		}
		cw.Flush()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
	var sources []map[string]any
	for _, x := range all {
		sources = append(sources, map[string]any{
			"source":   map[string]any{"name": x.Stream.Texts.Name, "slug": x.Stream.Slug, "place": x.Stream.Texts.Place, "credit": x.Stream.Texts.Credit, "sample_rate": x.Stream.SampleRate},
			"settings": a.Spec.Sources[strconv.FormatInt(x.Stream.ID, 10)],
			"events":   x.Events,
			"motifs":   x.Motifs,
		})
	}
	out := map[string]any{
		"analysis": map[string]any{"name": a.Name, "description": a.Description, "revision": a.Revision, "label": a.Label()},
		"period":   map[string]any{"from": from, "to": to},
		"exported": now,
		"sources":  sources,
	}
	// The comparison's correlations go with it; its activity, a row per
	// second, is in the comparison's own CSV.
	if s.compares(a) {
		corr := []json.RawMessage{}
		s.eachGroupEvent(r, a, from, to, "group.correlation", func(ev json.RawMessage) error {
			corr = append(corr, ev)
			return nil
		})
		out["comparison"] = map[string]any{"group": a.GroupID(), "analyses": a.Spec.Compare, "time": "wall", "correlations": corr}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	enc.Encode(out)
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

// eachGroupEvent hands the stored results of the analysis's group in a
// period to fn, page by page, without holding them all: a day of activity
// is 86 400 of them.
func (s *Server) eachGroupEvent(r *http.Request, a catalogue.Analysis, from, to time.Time, typ string, fn func(json.RawMessage) error) error {
	cursor := from
	for {
		q := url.Values{"from": {cursor.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}, "limit": {"2000"}}
		if typ != "" {
			q.Set("type", typ)
		}
		raw, err := s.eng.GroupData(r.Context(), a.GroupID(), "history/events", q)
		if err != nil {
			return err
		}
		var got struct {
			Events []json.RawMessage `json:"events"`
		}
		json.Unmarshal(raw, &got)
		for _, ev := range got.Events {
			if err := fn(ev); err != nil {
				return err
			}
		}
		if len(got.Events) < 2000 {
			return nil
		}
		var last struct {
			Timestamp time.Time `json:"timestamp"`
		}
		json.Unmarshal(got.Events[len(got.Events)-1], &last)
		if !last.Timestamp.After(cursor) {
			return nil
		}
		cursor = last.Timestamp.Add(time.Nanosecond)
	}
}

// compares reports whether the analysis's sources are compared in the
// engine now: two or more of them run, and the engine has groups.
func (s *Server) compares(a catalogue.Analysis) bool {
	if _, _, info := s.sync.status(); !info.Groups() {
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

// exportComparison writes the group's stored results of a period as CSV:
// one row per source and second of activity, and one per pair and minute of
// correlation.
func (s *Server) exportComparison(w http.ResponseWriter, r *http.Request, a catalogue.Analysis, from, to time.Time, name string) {
	names := map[string]string{}
	for _, src := range a.Sources {
		names[src.Stream.EngineID()] = src.Stream.Slug
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`-comparison.csv"`)
	fmt.Fprintf(w, "# analysis: %s, %s; comparison: %s\n# period: %s to %s\n# times are wall time at the engine: accurate to seconds, not to the stations' clocks\n",
		a.Name, a.Label(), strings.Join(a.Spec.Compare, ", "), from.Format(time.RFC3339), to.Format(time.RFC3339))
	cw := csv.NewWriter(w)
	cw.Write([]string{"type", "timestamp", "source", "other_source", "present", "level_db", "flux", "impulse_rate", "r", "lag_s", "baseline_r", "n"})
	num := func(p *float64) string {
		if p == nil {
			return ""
		}
		return strconv.FormatFloat(*p, 'g', -1, 64)
	}
	err := s.eachGroupEvent(r, a, from, to, "", func(raw json.RawMessage) error {
		var e struct {
			Type      string `json:"type"`
			Timestamp string `json:"timestamp"`
			Payload   struct {
				Members []struct {
					Stream      string   `json:"stream"`
					Present     bool     `json:"present"`
					LevelDB     *float64 `json:"level_db"`
					Flux        *float64 `json:"flux"`
					ImpulseRate *float64 `json:"impulse_rate"`
				} `json:"members"`
				Pairs []struct {
					A, B      string
					R         *float64 `json:"r"`
					LagS      *float64 `json:"lag_s"`
					BaselineR *float64 `json:"baseline_r"`
					N         *float64 `json:"n"`
				} `json:"pairs"`
			} `json:"payload"`
		}
		json.Unmarshal(raw, &e)
		switch e.Type {
		case "group.activity":
			for _, m := range e.Payload.Members {
				cw.Write([]string{"activity", e.Timestamp, names[m.Stream], "", strconv.FormatBool(m.Present), num(m.LevelDB), num(m.Flux), num(m.ImpulseRate), "", "", "", ""})
			}
		case "group.correlation":
			for _, p := range e.Payload.Pairs {
				cw.Write([]string{"correlation", e.Timestamp, names[p.A], names[p.B], "", "", "", "", num(p.R), num(p.LagS), num(p.BaselineR), num(p.N)})
			}
		}
		return cw.Error()
	})
	cw.Flush()
	if err != nil {
		fmt.Fprintf(w, "# the export ended early: %v\n", err)
	}
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
