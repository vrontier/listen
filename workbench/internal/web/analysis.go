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

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// ruleRow is one row of the filter form.
type ruleRow struct {
	Kind  string
	Hz    string
	Odd   bool
	Tol   string
	Drift string
	Text  string // as the engine writes it
}

// settingsForm is the filter form as filled in.
type settingsForm struct {
	MinHz, MaxHz, TransientK, TransientGap string
	MemoryMode                             string
	Rules                                  []ruleRow
}

type analysisData struct {
	Stream    catalogue.Stream
	Analysis  catalogue.Analysis
	Engine    *engine.Analysis
	EngineErr string
	Revisions []catalogue.Revision
	Memories  []engine.Memory
	Form      settingsForm
	Errs      map[string]string
	Warnings  []string
	LivePath  string
	DataPath  string
	Err       string
}

func (d analysisData) FormError() string { return d.Err }

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

func (s *Server) ownAnalysis(w http.ResponseWriter, r *http.Request) (catalogue.Stream, catalogue.Analysis, bool) {
	sess, _ := sessionFrom(r)
	st, err := s.db.StreamBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		if !isNotFound(err) {
			s.fail(w, r, err)
		} else {
			s.notFound(w, r)
		}
		return st, catalogue.Analysis{}, false
	}
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	a, err := s.db.Analysis(r.Context(), id)
	if err != nil || a.StreamID != st.ID || a.OwnerID != sess.User.ID {
		if err != nil && !isNotFound(err) {
			s.fail(w, r, err)
		} else {
			s.notFound(w, r)
		}
		return st, a, false
	}
	return st, a, true
}

func (s *Server) analysisData(r *http.Request, st catalogue.Stream, a catalogue.Analysis) (analysisData, error) {
	base := fmt.Sprintf("/account/streams/%s/analyses/%d", st.Slug, a.ID)
	d := analysisData{Stream: st, Analysis: a, Form: formFromSpec(a.Spec), Errs: map[string]string{},
		LivePath: base + "/live", DataPath: base + "/data/"}
	if st.State == catalogue.StreamDraft || st.State == catalogue.StreamProduction {
		ea, err := s.eng.Analysis(r.Context(), st.EngineID(), a.EngineID())
		if err != nil {
			d.EngineErr = "The engine does not report this analysis at the moment."
		} else {
			d.Engine = &ea
			for i, rule := range ea.Rules {
				if i < len(d.Form.Rules) {
					d.Form.Rules[i].Text = rule.Text
				}
			}
			raw, err := s.eng.Data(r.Context(), st.EngineID(), a.EngineID(), "memories", nil)
			if err == nil {
				var m struct {
					Memories []engine.Memory `json:"memories"`
				}
				json.Unmarshal(raw, &m)
				d.Memories = m.Memories
			}
		}
	}
	var err error
	d.Revisions, err = s.db.Revisions(r.Context(), a.ID)
	return d, err
}

func (s *Server) analysisPage(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	d, err := s.analysisData(r, st, a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Form.padRules()
	s.render(w, r, 200, "analysis", a.Name+" · "+st.Texts.Name, d)
}

var rulePathRE = regexp.MustCompile(`^rules\[(\d+)\]\.(\w+)$`)

// readSettings turns the form into an analysis body. Only the form's own
// syntax is checked here (is it a number); whether a value is allowed is
// the engine's to say.
func readSettings(r *http.Request) (settingsForm, engine.AnalysisSpec, []int, map[string]string) {
	errs := map[string]string{}
	f := settingsForm{
		MinHz: strings.TrimSpace(r.PostFormValue("min_hz")), MaxHz: strings.TrimSpace(r.PostFormValue("max_hz")),
		TransientK: strings.TrimSpace(r.PostFormValue("transient_k")), TransientGap: strings.TrimSpace(r.PostFormValue("transient_gap_s")),
		MemoryMode: r.PostFormValue("memory_mode"),
	}
	num := func(field, v string) *float64 {
		if v == "" {
			return nil
		}
		x, err := strconv.ParseFloat(strings.Replace(v, ",", ".", 1), 64)
		if err != nil {
			errs[field] = "a number"
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

	n, _ := strconv.Atoi(r.PostFormValue("rule_count"))
	n = min(n, 64)
	var rows []int // form row of each rule sent
	for i := 0; i < n; i++ {
		k := strconv.Itoa(i)
		row := ruleRow{
			Kind: r.PostFormValue("rule_kind_" + k), Hz: strings.TrimSpace(r.PostFormValue("rule_hz_" + k)),
			Odd: r.PostFormValue("rule_odd_"+k) == "on", Tol: strings.TrimSpace(r.PostFormValue("rule_tol_" + k)),
			Drift: strings.TrimSpace(r.PostFormValue("rule_drift_" + k)),
		}
		if row.Hz == "" && row.Tol == "" && row.Drift == "" {
			continue
		}
		f.Rules = append(f.Rules, row)
		fi := strconv.Itoa(len(f.Rules) - 1)
		rule := engine.Rule{Kind: row.Kind, Odd: row.Odd}
		if p := num("rule_hz_"+fi, row.Hz); p != nil {
			rule.Hz = *p
		} else if row.Hz == "" {
			errs["rule_hz_"+fi] = "a frequency"
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
func formField(path string, rows []int) string {
	if m := rulePathRE.FindStringSubmatch(path); m != nil {
		i, _ := strconv.Atoi(m[1])
		if i < len(rows) {
			field := map[string]string{"hz": "hz", "kind": "hz", "odd": "hz", "tol_hz": "tol", "drift_percent": "drift"}[m[2]]
			if field == "" {
				field = "hz"
			}
			return "rule_" + field + "_" + strconv.Itoa(rows[i])
		}
	}
	switch path {
	case "settings.min_hz":
		return "min_hz"
	case "settings.max_hz":
		return "max_hz"
	case "settings.transient_k":
		return "transient_k"
	case "settings.transient_gap_s":
		return "transient_gap_s"
	case "memory.mode":
		return "memory_mode"
	}
	return "rules"
}

func (s *Server) analysisSettings(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	form, spec, rows, errs := readSettings(r)
	reshow := func(status int, msg string, errs map[string]string, warns []string) {
		d, err := s.analysisData(r, st, a)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Form, d.Errs, d.Err, d.Warnings = form, errs, msg, warns
		d.Form.padRules()
		s.render(w, r, status, "analysis", a.Name+" · "+st.Texts.Name, d)
	}
	if len(errs) > 0 {
		reshow(400, "Please check the marked fields.", errs, nil)
		return
	}
	if ready, problem, _ := s.sync.status(); !ready {
		reshow(503, "The settings cannot be checked now: "+problem, errs, nil)
		return
	}
	v, err := s.eng.Validate(r.Context(), engine.ValidateRequest{SampleRate: st.SampleRate, AnalysisSpec: spec})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var warns []string
	for _, x := range v.Warnings {
		warns = append(warns, x.Message)
	}
	if !v.Valid {
		for _, e := range v.Errors {
			f := formField(e.Path, rows)
			if errs[f] == "" {
				errs[f] = e.Message
			}
		}
		reshow(422, "The engine refused these settings; see the marked fields.", errs, warns)
		return
	}
	body, _ := json.Marshal(spec)
	if bytes.Equal(canon(body), canon(a.Spec)) {
		s.redirect(w, r, fmt.Sprintf("/account/streams/%s/analyses/%d", st.Slug, a.ID), "Nothing changed.")
		return
	}
	a, err = s.db.SaveRevision(r.Context(), a.ID, sess.User.ID, body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	flash := fmt.Sprintf("Saved as revision %d.", a.Revision)
	if len(warns) > 0 {
		flash += " Note: " + strings.Join(warns, " ")
	}
	if st.State == catalogue.StreamDraft || st.State == catalogue.StreamProduction {
		if _, err := s.eng.PutAnalysis(r.Context(), st.EngineID(), a.EngineID(), analysisBody(a)); err != nil {
			log.Printf("put %s/%s: %v", st.EngineID(), a.EngineID(), err)
			flash += " The engine has not taken it yet; it will be sent again."
		}
	}
	s.redirect(w, r, fmt.Sprintf("/account/streams/%s/analyses/%d", st.Slug, a.ID), flash)
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

// analysisLive passes the analysis's events on to a page.
func (s *Server) analysisLive(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	if st.State != catalogue.StreamDraft && st.State != catalogue.StreamProduction {
		http.Error(w, "the stream is "+st.State, http.StatusConflict)
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	sess, _ := sessionFrom(r)
	rd := &reader{ch: make(chan []byte, 256), person: true, who: sess.User.Name}
	leave := s.live.join(st.EngineID(), a.EngineID(), rd)
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

// analysisProxy passes one of the engine's data paths on, as JSON.
func (s *Server) analysisProxy(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.ownAnalysis(w, r)
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
	raw, err := s.eng.Data(r.Context(), st.EngineID(), a.EngineID(), what, q)
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

// analysisExport writes the stored events of a period as CSV or JSON.
func (s *Server) analysisExport(w http.ResponseWriter, r *http.Request) {
	st, a, ok := s.ownAnalysis(w, r)
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

	// The engine answers at most 2000 events per request; page through.
	var events []json.RawMessage
	cursor := from
	for page := 0; page < 100; page++ {
		q := url.Values{"from": {cursor.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}, "limit": {"2000"}}
		if typ != "" {
			q.Set("type", typ)
		}
		raw, err := s.eng.Data(r.Context(), st.EngineID(), a.EngineID(), "history/events", q)
		if err != nil {
			http.Error(w, "the engine did not answer: "+err.Error(), http.StatusBadGateway)
			return
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
	motifs, _ := s.eng.Data(r.Context(), st.EngineID(), a.EngineID(), "motifs", nil)

	name := fmt.Sprintf("%s-%s-%s", st.Slug, a.Label(), from.Format("20060102T1504Z"))
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
		fmt.Fprintf(w, "# stream: %s (%s), analysis: %s, %s, sample rate %d Hz\n", st.Texts.Name, st.Slug, a.Name, a.Label(), st.SampleRate)
		fmt.Fprintf(w, "# settings: %s\n# period: %s to %s\n", a.Spec, from.Format(time.RFC3339), to.Format(time.RFC3339))
		cw := csv.NewWriter(w)
		cw.Write([]string{"timestamp", "position_s", "type", "id", "status", "fundamental_hz", "strength", "duration_s", "motif_id"})
		for _, ev := range events {
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
			cw.Write([]string{e.Timestamp, strconv.FormatFloat(e.Position, 'f', 3, 64), e.Type, e.Payload.ID, e.Payload.Status,
				opt(e.Payload.Fundamental), opt(e.Payload.Strength), dur, e.Payload.MotifID})
		}
		cw.Flush()
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
	if events == nil {
		events = []json.RawMessage{}
	}
	if motifs == nil {
		motifs = json.RawMessage(`null`)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", " ")
	enc.Encode(map[string]any{
		"stream":   map[string]any{"name": st.Texts.Name, "slug": st.Slug, "place": st.Texts.Place, "credit": st.Texts.Credit, "sample_rate": st.SampleRate},
		"analysis": map[string]any{"name": a.Name, "revision": a.Revision, "label": a.Label(), "settings": a.Spec},
		"period":   map[string]any{"from": from, "to": to},
		"exported": now,
		"events":   events,
		"motifs":   motifs,
	})
}
