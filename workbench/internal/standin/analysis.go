package standin

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	bandCount    = 48
	maxPeaks     = 12
	recentEvents = 40
	historyMax   = 100000
)

// analysis is one made-up analysis of a stream.
type analysis struct {
	id  string
	c   checked
	seq uint64

	subs map[*sub]struct{}

	// made-up state
	frames     int
	resonances map[string]*resonance // by group
	nextRes    int
	last       map[string]json.RawMessage // latest frame, spectrum, features, status
	recent     []json.RawMessage
	minutes    []minute
	cur        minute

	memories map[string]*memory
	mem      *memory // in use; nil with memory.mode off

	failed *problem // set when the analysis failed; a PUT starts it again
}

// Fail makes an analysis fail as the engine's would after a crash in it,
// for tests: it stops, its stream and the other analyses go on.
func (e *Engine) Fail(stream, analysis, message string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.streams[stream]
	if s == nil || s.analyses[analysis] == nil {
		return false
	}
	a := s.analyses[analysis]
	a.failed = &problem{Code: "analysis_failed", Message: message}
	a.stateChanged(s)
	return true
}

// state is the analysis's state: failed, or running while its stream is
// connected, else idle.
func (a *analysis) state(s *stream) string {
	switch {
	case a.failed != nil:
		return "failed"
	case s.state == "connected":
		return "running"
	}
	return "idle"
}

type sub struct {
	ch    chan []byte
	types map[string]bool
}

type resonance struct {
	id      string
	started time.Time
	hz      float64
	seen    bool
}

type histEvent struct {
	t    time.Time
	typ  string
	data json.RawMessage
}

type minute struct {
	T          time.Time `json:"t"`
	LevelDB    float64   `json:"level_db"`
	CentroidHz float64   `json:"centroid_hz"`
	Events     int       `json:"events"`
	n          int
}

// memory is one remembered set of motifs, under a key of range and rules.
type memory struct {
	key       string
	settings  map[string]float64
	rules     []rule
	firstUsed time.Time
	lastUsed  time.Time
	bytes     int64
	motifs    map[string]*motif // by group
	history   []histEvent
	nextID    int
	onDisk    bool
}

type motif struct {
	ID          string    `json:"motif_id"`
	Kind        string    `json:"kind"`
	Created     time.Time `json:"created"`
	LastSeen    time.Time `json:"last_seen"`
	Active      bool      `json:"active"`
	Occurrences int       `json:"occurrences"`
	PresenceS   float64   `json:"presence_s"`
	Confidence  float64   `json:"confidence"`
	HzHint      float64   `json:"fundamental_hz"`
	established bool
}

func (a *analysis) usage(s *stream) usage {
	if s.state != "connected" {
		return usage{}
	}
	scale := float64(s.sampleRate) / defaultRate
	n := 0
	if a.mem != nil {
		n = len(a.mem.motifs)
	}
	return usage{CPU: round(0.9*scale, 1), Mem: round(6+0.05*float64(n), 1)}
}

func (a *analysis) view(s *stream) map[string]any {
	key := ""
	if a.mem != nil {
		key = a.mem.key
	}
	rules := a.c.rules
	if rules == nil {
		rules = []rule{}
	}
	state := a.state(s)
	motifs := 0
	if a.mem != nil {
		for _, m := range a.mem.motifs {
			if m.established {
				motifs++
			}
		}
	}
	u := a.usage(s)
	return map[string]any{
		"id": a.id, "stream": s.id, "settings": a.c.settings, "rules": rules,
		"memory": map[string]any{"mode": a.c.memMode, "motif_return_s": a.c.motifReturn, "history_days": a.c.historyDays, "key": key},
		"label":  a.c.label, "effective": a.c.eff, "state": state, "last_error": a.failed, "subscribers": len(a.subs), "motifs": motifs,
		"usage": map[string]any{"cpu_percent": u.CPU, "memory_mb": u.Mem, "memory_estimated": true},
	}
}

// apply puts checked settings into effect. A change of range or rules
// starts the analysis again with another memory.
func (a *analysis) apply(c checked) {
	restart := a.mem == nil && a.c.memMode != "off" || c.memoryKey() != a.c.memoryKey() || a.seq == 0
	old := a.c
	a.c = c
	if restart || old.memMode != c.memMode {
		if restart {
			a.seq = 0
			a.resonances = map[string]*resonance{}
			a.frames = 0
		}
		a.mem = nil
		if c.memMode != "off" {
			key := c.memoryKey()
			m := a.memories[key]
			if m == nil || c.memMode == "ram" && m.onDisk {
				m = &memory{key: key, settings: c.settings, rules: c.rules, firstUsed: time.Now().UTC(), motifs: map[string]*motif{}}
				if c.memMode == "disk" {
					a.memories[key] = m
				}
			}
			m.onDisk = c.memMode == "disk"
			m.lastUsed = time.Now().UTC()
			a.mem = m
		}
	}
}

func (a *analysis) closeSubscribers() {
	for s := range a.subs {
		close(s.ch)
		delete(a.subs, s)
	}
}

func (a *analysis) stateChanged(s *stream) {
	a.emitStatus(s, time.Now().UTC())
	key := ""
	if a.mem != nil {
		key = a.mem.key
	}
	ev := map[string]any{"stream": s.id, "analysis": a.id, "state": a.state(s), "memory_key": key}
	if a.failed != nil {
		ev["last_error"] = a.failed
	}
	s.e.notify("analysis.state", ev)
}

// emit stamps a payload and hands it to subscribers, the snapshot and the
// history.
func (a *analysis) emit(s *stream, typ string, t time.Time, pos float64, payload any) {
	a.seq++
	env := map[string]any{
		"type": typ, "version": 1, "timestamp": t.Format("2006-01-02T15:04:05.000Z07:00"), "sequence": a.seq,
		"source": s.id + "/" + a.id, "payload": payload,
	}
	if pos > 0 {
		env["position_s"] = round(pos, 3)
	}
	data, _ := json.Marshal(env)
	msg := []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", a.seq, typ, data))
	for sb := range a.subs {
		if sb.types != nil && !sb.types[typ] {
			continue
		}
		select {
		case sb.ch <- msg:
		default: // a slow reader loses events
		}
	}
	switch typ {
	case "signal.frame", "signal.spectrum", "feature.state", "system.status":
		a.last[typ] = data
		return
	}
	a.recent = append(a.recent, data)
	if len(a.recent) > recentEvents {
		a.recent = a.recent[len(a.recent)-recentEvents:]
	}
	if a.mem != nil {
		a.mem.history = append(a.mem.history, histEvent{t, typ, data})
		if len(a.mem.history) > historyMax {
			a.mem.history = a.mem.history[len(a.mem.history)-historyMax:]
		}
		if a.mem.onDisk {
			a.mem.bytes += int64(len(data) + 1)
		}
		a.cur.Events++
	}
}

func (a *analysis) emitStatus(s *stream, t time.Time) {
	key := ""
	if a.mem != nil {
		key = a.mem.key
	}
	a.emit(s, "system.status", t, 0, map[string]any{
		"stream": s.state, "analysis": map[bool]string{true: "running", false: "idle"}[s.state == "connected"],
		"latency_ms": 180, "sample_rate": s.sampleRate, "label": a.c.label, "memory_key": key,
		"input": "live", "reconnects": s.reconnects,
	})
}

// matches reports whether a filter rule removes a peak at hz.
func (a *analysis) matches(hz float64) bool {
	for _, r := range a.c.rules {
		tol := r.TolHz
		if tol == 0 {
			tol = a.c.eff.DefaultTolHz
		}
		if r.Kind == "line" {
			if math.Abs(hz-r.Hz) <= tol {
				return true
			}
			continue
		}
		n := math.Round(hz / r.Hz)
		if n < 1 || r.Odd && int(n)%2 == 0 {
			continue
		}
		if r.DriftPercent > 0 {
			tol += n * r.Hz * r.DriftPercent / 100
		}
		if math.Abs(hz-n*r.Hz) <= tol {
			return true
		}
	}
	return false
}

// step makes up one tenth of a second of analysis.
func (a *analysis) step(s *stream, t time.Time, status bool) {
	a.frames++
	eff := a.c.eff
	pos := s.decoded
	sc := &s.scene

	// Bands on a log scale over the analysed range. Rules never touch them.
	bands := make([]float64, bandCount)
	lo, hi := math.Log(eff.MinHz), math.Log(eff.MaxHz)
	for i := range bands {
		bands[i] = 0.12 + 0.05*sc.rng.Float64()
	}
	for _, c := range sc.comps {
		if c.hz < eff.MinHz || c.hz > eff.MaxHz {
			continue
		}
		i := int((math.Log(c.hz) - lo) / (hi - lo) * bandCount)
		if i >= 0 && i < bandCount {
			bands[i] = math.Min(1, bands[i]+c.amp)
		}
	}
	if sc.transient {
		for i := range bands {
			bands[i] = math.Min(1, bands[i]+0.35)
		}
	}
	for i := range bands {
		bands[i] = round(bands[i], 3)
	}

	// Peaks: what remains after the rules.
	type peak struct {
		Hz        float64 `json:"hz"`
		Amplitude float64 `json:"amplitude"`
	}
	var peaks []peak
	groups := map[string][]component{}
	for _, c := range sc.comps {
		if c.hz < eff.PeakMinHz || c.hz > eff.PeakMaxHz || a.matches(c.hz) {
			continue
		}
		peaks = append(peaks, peak{round(c.hz, 1), round(c.amp, 3)})
		groups[c.group] = append(groups[c.group], c)
	}
	slices.SortFunc(peaks, func(x, y peak) int {
		if x.Amplitude > y.Amplitude {
			return -1
		}
		if x.Amplitude < y.Amplitude {
			return 1
		}
		return 0
	})
	if len(peaks) > maxPeaks {
		peaks = peaks[:maxPeaks]
	}
	if peaks == nil {
		peaks = []peak{}
	}
	harmonicity := 0.0
	if len(groups) > 0 {
		harmonicity = math.Min(1, 0.25*float64(len(groups))+0.1*sc.rng.Float64())
	}
	level := sc.levelDB + 8*s.skyNow + 4*sc.rng.Float64()
	if sc.transient {
		level += 9
	}
	centroid := 1800 + 300*sc.rng.Float64()

	a.emit(s, "signal.frame", t, pos, map[string]any{
		"rms": round(math.Pow(10, level/20), 5), "level_db": round(level, 1), "energy": round(0.4+0.1*sc.rng.Float64(), 3),
		"peak": 0.3, "zero_crossing_rate": 0.08, "spectral_centroid_hz": round(centroid, 1),
		"spectral_flux": round(0.05+0.5*b2f(sc.transient), 3), "spectral_entropy": 0.62, "harmonicity": round(harmonicity, 3),
	})
	spectrum := map[string]any{"min_hz": eff.MinHz, "max_hz": eff.MaxHz, "scale": "log", "bands": bands, "peaks": peaks}
	if v := s.viewEff; v != nil && s.viewBands != nil {
		spectrum["view"] = map[string]any{"min_hz": v.MinHz, "max_hz": v.MaxHz, "scale": v.Scale, "bands": s.viewBands}
	}
	a.emit(s, "signal.spectrum", t, pos, spectrum)
	if sc.transient {
		a.emit(s, "event.transient", t, pos, map[string]any{
			"strength": round(0.5+0.5*sc.rng.Float64(), 3), "duration_ms": 40, "centroid_hz": round(centroid*2, 1),
			"bandwidth_hz": 3800, "novelty": 0.4,
		})
	}

	// Resonances: one per group of components that survives the rules.
	for g, cs := range groups {
		r := a.resonances[g]
		harm := make([]map[string]any, 0, len(cs))
		for i, c := range cs {
			harm = append(harm, map[string]any{"order": i + 1, "hz": round(c.hz, 1), "strength": round(c.amp, 3)})
		}
		if r == nil {
			a.nextRes++
			r = &resonance{id: fmt.Sprintf("res-%05d", a.nextRes), started: t, hz: cs[0].hz}
			a.resonances[g] = r
			p := map[string]any{"id": r.id, "status": "start", "fundamental_hz": round(cs[0].hz, 1), "strength": round(cs[0].amp, 3),
				"duration_s": 0.0, "harmonics": harm}
			if id := a.remember(s, g, cs[0].hz, t, pos); id != "" {
				p["motif_id"] = id
			}
			a.emit(s, "event.resonance", t, pos, p)
		} else if a.frames%20 == 0 {
			a.emit(s, "event.resonance", t, pos, map[string]any{"id": r.id, "status": "update", "fundamental_hz": round(cs[0].hz, 1),
				"strength": round(cs[0].amp, 3), "duration_s": round(t.Sub(r.started).Seconds(), 1), "harmonics": harm})
		}
		r.seen = true
	}
	for g, r := range a.resonances {
		if !r.seen {
			a.emit(s, "event.resonance", t, pos, map[string]any{"id": r.id, "status": "end", "fundamental_hz": round(r.hz, 1),
				"strength": 0.0, "duration_s": round(t.Sub(r.started).Seconds(), 1), "harmonics": []any{}})
			delete(a.resonances, g)
			if a.mem != nil && a.mem.motifs[g] != nil {
				a.mem.motifs[g].Active = false
			}
			continue
		}
		r.seen = false
		if a.mem != nil && a.mem.motifs[g] != nil {
			a.mem.motifs[g].PresenceS += 0.1
			a.mem.motifs[g].LastSeen = t
		}
	}

	if a.frames%5 == 0 {
		dominant := 0.0
		if len(peaks) > 0 {
			dominant = peaks[0].Hz
		}
		state := "broadband_noise"
		if len(groups) > 0 {
			state = "stable_resonance"
		}
		if sc.transient {
			state = "transient_activity"
		}
		a.emit(s, "feature.state", t, pos, map[string]any{
			"dominant_frequency_hz": dominant, "spectral_centroid_hz": round(centroid, 1), "bandwidth_hz": 2600,
			"rolloff_hz": 5200, "spectral_entropy": 0.62, "spectral_flux": 0.1, "harmonicity": round(harmonicity, 3),
			"energy": 0.45, "novelty": 0.2, "state": state,
		})
	}

	// Per-minute aggregates for the history.
	m := t.Truncate(time.Minute)
	if !a.cur.T.Equal(m) {
		if a.cur.n > 0 && a.mem != nil {
			a.cur.LevelDB, a.cur.CentroidHz = round(a.cur.LevelDB/float64(a.cur.n), 1), round(a.cur.CentroidHz/float64(a.cur.n), 1)
			a.minutes = append(a.minutes, a.cur)
			if len(a.minutes) > 24*60 {
				a.minutes = a.minutes[1:]
			}
		}
		a.cur = minute{T: m}
	}
	a.cur.n++
	a.cur.LevelDB += level
	a.cur.CentroidHz += centroid

	if status {
		a.emitStatus(s, t)
	}
}

// remember counts an occurrence of a resonance in the memory and returns
// the motif's identifier once it is established.
func (a *analysis) remember(s *stream, group string, hz float64, t time.Time, pos float64) string {
	if a.mem == nil {
		return ""
	}
	m := a.mem.motifs[group]
	if m == nil {
		m = &motif{Kind: "resonance", Created: t, HzHint: round(hz, 1)}
		a.mem.motifs[group] = m
	}
	m.Occurrences++
	m.Active = true
	absent := t.Sub(m.LastSeen)
	m.LastSeen = t
	m.Confidence = round(math.Min(0.95, 0.3+0.15*float64(m.Occurrences)), 2)
	switch {
	case !m.established && m.Occurrences >= 2:
		m.established = true
		a.mem.nextID++
		m.ID = fmt.Sprintf("motif-%03d", a.mem.nextID)
		a.emit(s, "motif.detected", t, pos, map[string]any{"motif_id": m.ID, "kind": m.Kind, "confidence": m.Confidence, "occurrences": m.Occurrences})
	case m.established && absent >= time.Duration(a.c.motifReturn)*time.Second:
		a.emit(s, "motif.returned", t, pos, map[string]any{"motif_id": m.ID, "kind": m.Kind, "similarity": 0.91, "absent_s": round(absent.Seconds(), 0)})
	}
	return m.ID
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// --- endpoints ---

func (e *Engine) find(w http.ResponseWriter, r *http.Request) (*stream, *analysis, bool) {
	s := e.streams[r.PathValue("s")]
	if s == nil {
		writeErr(w, &apiError{status: 404, code: "not_found", message: "no such stream"})
		return nil, nil, false
	}
	a := s.analyses[r.PathValue("a")]
	if a == nil {
		writeErr(w, &apiError{status: 404, code: "not_found", message: "no such analysis"})
		return nil, nil, false
	}
	return s, a, true
}

func (e *Engine) getAnalysis(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if s, a, ok := e.find(w, r); ok {
		writeJSON(w, 200, a.view(s))
	}
}

func (e *Engine) putAnalysis(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("a")
	if err := checkID(id); err != nil {
		writeErr(w, err)
		return
	}
	var in specIn
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.streams[r.PathValue("s")]
	if s == nil {
		writeErr(w, &apiError{status: 404, code: "not_found", message: "no such stream"})
		return
	}
	c, errs, _ := check(s.sampleRate, in)
	if len(errs) > 0 {
		writeErr(w, &apiError{status: 422, code: "invalid", message: "the settings were refused", errors: errs})
		return
	}
	a := s.analyses[id]
	if a == nil {
		if len(s.analyses) >= limits["analyses_per_stream"] {
			writeErr(w, &apiError{status: 409, code: "limit_reached", message: fmt.Sprintf("this stream already has %d analyses", len(s.analyses))})
			return
		}
		a = &analysis{id: id, subs: map[*sub]struct{}{}, resonances: map[string]*resonance{}, last: map[string]json.RawMessage{}, memories: map[string]*memory{}}
		a.c.memMode = "off"
		s.analyses[id] = a
	}
	a.apply(c)
	a.failed = nil // a PUT of a failed analysis starts it again
	s.update(time.Now())
	a.stateChanged(s)
	writeJSON(w, 200, a.view(s))
}

func (e *Engine) deleteAnalysis(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, a, ok := e.find(w, r)
	if !ok {
		return
	}
	a.closeSubscribers()
	delete(s.analyses, a.id)
	s.update(time.Now())
	w.WriteHeader(204)
}

func (e *Engine) analysisEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, &apiError{status: 500, code: "internal", message: "no streaming"})
		return
	}
	e.mu.Lock()
	s, a, found := e.find(w, r)
	if !found {
		e.mu.Unlock()
		return
	}
	if len(a.subs) >= limits["subscriptions_per_analysis"] {
		e.mu.Unlock()
		writeErr(w, &apiError{status: 409, code: "limit_reached", message: "too many subscriptions to this analysis"})
		return
	}
	sb := &sub{ch: make(chan []byte, 512)}
	if t := r.URL.Query().Get("types"); t != "" {
		sb.types = map[string]bool{}
		for _, x := range strings.Split(t, ",") {
			sb.types[strings.TrimSpace(x)] = true
		}
	}
	a.subs[sb] = struct{}{}
	s.update(time.Now())
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		if _, still := a.subs[sb]; still {
			delete(a.subs, sb)
		}
		e.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-e.stop:
			return
		case msg, open := <-sb.ch:
			if !open {
				return
			}
			w.Write(msg)
			fl.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func (e *Engine) analysisState(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, a, ok := e.find(w, r)
	if !ok {
		return
	}
	raw := func(k string) json.RawMessage {
		if v, ok := a.last[k]; ok {
			return v
		}
		return json.RawMessage("null")
	}
	active := []any{}
	if a.mem != nil {
		for _, m := range a.mem.motifs {
			if m.established && m.Active {
				active = append(active, m)
			}
		}
	}
	writeJSON(w, 200, map[string]any{
		"frame": raw("signal.frame"), "spectrum": raw("signal.spectrum"), "features": raw("feature.state"),
		"system": raw("system.status"), "active_resonances": []any{}, "recent_events": append([]json.RawMessage{}, a.recent...),
		"active_motifs": active, "memory": map[string]any{}, "narrative": nil,
	})
}

// memoryFor returns the memory named by ?memory=, or the one in use.
func (a *analysis) memoryFor(w http.ResponseWriter, r *http.Request) (*memory, bool) {
	if k := r.URL.Query().Get("memory"); k != "" {
		m := a.memories[k]
		if m == nil {
			writeErr(w, &apiError{status: 404, code: "not_found", message: "no such memory"})
			return nil, false
		}
		return m, true
	}
	return a.mem, true
}

func (e *Engine) analysisMotifs(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, a, ok := e.find(w, r)
	if !ok {
		return
	}
	m, ok := a.memoryFor(w, r)
	if !ok {
		return
	}
	out := []*motif{}
	if m != nil {
		for _, mo := range m.motifs {
			if mo.established {
				out = append(out, mo)
			}
		}
	}
	slices.SortFunc(out, func(x, y *motif) int { return int(y.PresenceS - x.PresenceS) })
	writeJSON(w, 200, map[string]any{"motifs": out, "aliases": map[string]string{}})
}

func (e *Engine) analysisMotif(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, a, ok := e.find(w, r)
	if !ok {
		return
	}
	if a.mem != nil {
		for _, mo := range a.mem.motifs {
			if mo.established && mo.ID == r.PathValue("m") {
				writeJSON(w, 200, mo)
				return
			}
		}
	}
	writeErr(w, &apiError{status: 404, code: "not_found", message: "no such motif"})
}

func (e *Engine) historyEvents(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, a, ok := e.find(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	from, to := now.Add(-time.Hour), now.Add(time.Second)
	if t, err := time.Parse(time.RFC3339, q.Get("from")); err == nil {
		from = t
	}
	if t, err := time.Parse(time.RFC3339, q.Get("to")); err == nil {
		to = t
	}
	limit := 2000
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	typ := q.Get("type")
	out := []json.RawMessage{}
	m, ok := a.memoryFor(w, r)
	if !ok {
		return
	}
	if m != nil {
		for _, h := range m.history {
			if h.t.Before(from) || !h.t.Before(to) || typ != "" && h.typ != typ {
				continue
			}
			out = append(out, h.data)
			if len(out) >= limit {
				break
			}
		}
	}
	writeJSON(w, 200, map[string]any{"from": from, "to": to, "events": out})
}

func (e *Engine) historyFeatures(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, a, ok := e.find(w, r)
	if !ok {
		return
	}
	window := time.Hour
	if r.URL.Query().Get("window") == "24h" {
		window = 24 * time.Hour
	}
	cut := time.Now().Add(-window)
	out := []minute{}
	for _, m := range a.minutes {
		if !m.T.Before(cut) {
			out = append(out, m)
		}
	}
	writeJSON(w, 200, map[string]any{"window": window.String(), "minutes": out})
}

func (e *Engine) listMemories(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, a, ok := e.find(w, r)
	if !ok {
		return
	}
	out := []map[string]any{}
	for _, m := range a.memories {
		n := 0
		for _, mo := range m.motifs {
			if mo.established {
				n++
			}
		}
		rules := m.rules
		if rules == nil {
			rules = []rule{}
		}
		out = append(out, map[string]any{
			"key": m.key, "settings": m.settings, "rules": rules, "first_used": m.firstUsed, "last_used": m.lastUsed,
			"motifs": n, "bytes": m.bytes + 2048, "in_use": a.mem == m,
		})
	}
	slices.SortFunc(out, func(x, y map[string]any) int { return y["last_used"].(time.Time).Compare(x["last_used"].(time.Time)) })
	writeJSON(w, 200, map[string]any{"memories": out})
}

func (e *Engine) deleteMemory(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, a, ok := e.find(w, r)
	if !ok {
		return
	}
	m := a.memories[r.PathValue("key")]
	switch {
	case m == nil:
		writeErr(w, &apiError{status: 404, code: "not_found", message: "no such memory"})
	case m == a.mem:
		writeErr(w, &apiError{status: 409, code: "in_use", message: "the memory in use cannot be deleted"})
	default:
		delete(a.memories, m.key)
		w.WriteHeader(204)
	}
}
