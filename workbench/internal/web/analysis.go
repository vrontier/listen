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
	Revisions   []catalogue.Revision
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
}

func (s *Server) analyses(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	as, err := s.db.AnalysesOf(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "analyses", "Analyses", analysesData{Analyses: as})
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
// engine.
func (s *Server) pushSources(r *http.Request, sources []catalogue.AnalysisSource) error {
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
	forms := map[int64]settingsForm{}
	settings := map[int64]json.RawMessage{}
	var warns []string
	ready, problem, _ := s.sync.status()
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
		d.Errs, d.Warnings, d.Err = errs, warns, "Not saved. Please check the marked fields."
		s.renderAnalysis(w, r, 422, d)
		return
	}

	if sameRevision(a, ids, settings) {
		s.redirect(w, r, analysisPath(a), "Nothing changed.")
		return
	}
	before := a.Sources
	a, err = s.db.SaveRevision(r.Context(), a.ID, sess.User.ID, ids, settings)
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
func sameRevision(a catalogue.Analysis, ids []int64, settings map[int64]json.RawMessage) bool {
	if len(ids) != len(a.Sources) {
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
		s.redirect(w, r, analysisPath(a), "Not deleted: type the analysis's name exactly to confirm.")
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
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	sess, _ := sessionFrom(r)
	rd := &reader{ch: make(chan []byte, 256), person: true, who: sess.User.Name}
	leave := s.live.join(src.Stream.EngineID(), a.EngineID(), rd)
	defer leave()

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
			if t, err := time.ParseInLocation(layout, v, time.Local); err == nil {
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
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	enc.Encode(map[string]any{
		"analysis": map[string]any{"name": a.Name, "description": a.Description, "revision": a.Revision, "label": a.Label()},
		"period":   map[string]any{"from": from, "to": to},
		"exported": now,
		"sources":  sources,
	})
}
