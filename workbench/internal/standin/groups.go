package standin

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Groups, as in control API 1.1 (docs/control-api.md, Groups): the
// comparison of several analyses on one time axis. The stand-in has no
// station time, so it offers only `activity`, on wall time.

const (
	groupHistoryMax = 300000 // about three and a half days of seconds
	activityKept    = 900    // seconds of activity kept for correlations
	corrWindow      = 300    // seconds compared per correlation
	corrMaxLag      = 30     // seconds of lag searched either way
	baselineShift   = 150    // seconds one series is shifted for the baseline
	groupMargin     = 2      // seconds the group waits for late data
)

var groupAnalyses = []map[string]string{
	{"id": "activity", "timing": "seconds"},
	{"id": "clock", "timing": "milliseconds"},
	{"id": "coincidence", "timing": "milliseconds"},
}

type groupMember struct {
	Stream   string   `json:"stream"`
	Analysis string   `json:"analysis"`
	Lat      *float64 `json:"lat,omitempty"`
	Lon      *float64 `json:"lon,omitempty"`
}

type groupSpec struct {
	Members     []groupMember `json:"members"`
	Analyses    []string      `json:"analyses"`
	HistoryDays *int          `json:"history_days"`
	Label       string        `json:"label"`
	Mode        string        `json:"mode"`
}

type group struct {
	id   string
	spec groupSpec
	seq  uint64
	subs map[*sub]struct{}

	running    bool
	next       time.Time // the next second to report
	activity   []activitySec
	lastStatus json.RawMessage
	lastCorr   json.RawMessage
	recent     []json.RawMessage // the last activity events, for the state
	history    []histEvent
	statusTick int
}

// activitySec is one reported second: per member, what it heard, or nil
// when it was absent.
type activitySec struct {
	T    time.Time
	vals []*secStat
}

func (g *group) wanted() bool { return g.spec.Mode == "continuous" || len(g.subs) > 0 }

// groupWants reports whether a group keeps the stream connected.
func (e *Engine) groupWants(streamID string) bool {
	for _, g := range e.groups {
		if !g.wanted() {
			continue
		}
		for _, m := range g.spec.Members {
			if m.Stream == streamID {
				return true
			}
		}
	}
	return false
}

func (e *Engine) groupRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/groups", e.listGroups)
	m.HandleFunc("GET /v1/groups/{g}", e.getGroup)
	m.HandleFunc("PUT /v1/groups/{g}", e.putGroup)
	m.HandleFunc("DELETE /v1/groups/{g}", e.deleteGroup)
	m.HandleFunc("GET /v1/groups/{g}/events", e.groupEvents)
	m.HandleFunc("GET /v1/groups/{g}/state", e.groupState)
	m.HandleFunc("GET /v1/groups/{g}/history/events", e.groupHistory)
}

// memberState is present, absent or removed.
func (e *Engine) memberState(m groupMember) string {
	s := e.streams[m.Stream]
	if s == nil || s.analyses[m.Analysis] == nil {
		return "removed"
	}
	if s.state != "connected" {
		return "absent"
	}
	return "present"
}

func (e *Engine) groupView(g *group) map[string]any {
	members := make([]map[string]any, 0, len(g.spec.Members))
	for _, m := range g.spec.Members {
		v := map[string]any{"stream": m.Stream, "analysis": m.Analysis, "state": e.memberState(m), "time_source": "wall", "delay_s": nil}
		if m.Lat != nil && m.Lon != nil {
			v["lat"], v["lon"] = *m.Lat, *m.Lon
		}
		members = append(members, v)
	}
	state := "idle"
	if g.running {
		state = "running"
	}
	days := 30
	if g.spec.HistoryDays != nil {
		days = *g.spec.HistoryDays
	}
	return map[string]any{
		"id": g.id, "members": members, "analyses": g.spec.Analyses, "mode": g.spec.Mode, "history_days": days,
		"label": g.spec.Label, "state": state, "subscribers": len(g.subs),
	}
}

func (e *Engine) listGroups(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, 0, len(e.groups))
	for id := range e.groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, e.groupView(e.groups[id]))
	}
	writeJSON(w, 200, map[string]any{"groups": out})
}

func (e *Engine) findGroup(w http.ResponseWriter, r *http.Request) (*group, bool) {
	g := e.groups[r.PathValue("g")]
	if g == nil {
		writeErr(w, &apiError{status: 404, code: "not_found", message: "no such group"})
	}
	return g, g != nil
}

func (e *Engine) getGroup(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if g, ok := e.findGroup(w, r); ok {
		writeJSON(w, 200, e.groupView(g))
	}
}

func (e *Engine) putGroup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("g")
	if err := checkID(id); err != nil {
		writeErr(w, err)
		return
	}
	var spec groupSpec
	if err := decode(r, &spec); err != nil {
		writeErr(w, err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var fe []fieldError
	if n := len(spec.Members); n < 2 || n > limits["members_per_group"] {
		fe = append(fe, fieldError{Path: "members", Code: "out_of_range", Message: fmt.Sprintf("a group has 2 to %d members", limits["members_per_group"])})
	}
	seen := map[string]bool{}
	for i, m := range spec.Members {
		path := fmt.Sprintf("members[%d]", i)
		s := e.streams[m.Stream]
		switch {
		case s == nil:
			fe = append(fe, fieldError{Path: path + ".stream", Code: "not_found", Message: "no such stream"})
		case s.analyses[m.Analysis] == nil:
			fe = append(fe, fieldError{Path: path + ".analysis", Code: "not_found", Message: "no such analysis on " + m.Stream})
		case seen[m.Stream+"/"+m.Analysis]:
			fe = append(fe, fieldError{Path: path, Code: "duplicate", Message: "named twice"})
		}
		seen[m.Stream+"/"+m.Analysis] = true
		if (m.Lat == nil) != (m.Lon == nil) || m.Lat != nil && (math.Abs(*m.Lat) > 90 || math.Abs(*m.Lon) > 180) {
			fe = append(fe, fieldError{Path: path + ".lat", Code: "invalid", Message: "lat and lon together, within -90..90 and -180..180"})
		}
	}
	if len(spec.Analyses) == 0 {
		spec.Analyses = []string{"activity"}
	}
	for i, a := range spec.Analyses {
		path := fmt.Sprintf("analyses[%d]", i)
		switch a {
		case "activity":
		case "clock", "coincidence":
			fe = append(fe, fieldError{Path: path, Code: "needs_station_time", Message: a + " needs every group member on station time; no member is"})
		default:
			fe = append(fe, fieldError{Path: path, Code: "unknown", Message: "no such group analysis"})
		}
	}
	if spec.Mode == "" {
		spec.Mode = "on_demand"
	}
	if spec.Mode != "on_demand" && spec.Mode != "continuous" {
		fe = append(fe, fieldError{Path: "mode", Code: "invalid", Message: "on_demand or continuous"})
	}
	if d := spec.HistoryDays; d != nil && (*d < 1 || *d > 365) {
		fe = append(fe, fieldError{Path: "history_days", Code: "out_of_range", Message: "must be between 1 and 365 days"})
	}
	if fe != nil {
		writeErr(w, &apiError{status: 422, code: "invalid", message: "the group was refused", errors: fe})
		return
	}
	g := e.groups[id]
	if g == nil {
		if len(e.groups) >= limits["groups"] {
			writeErr(w, &apiError{status: 409, code: "limit_reached", message: "the engine holds as many groups as it may"})
			return
		}
		g = &group{id: id, subs: map[*sub]struct{}{}}
		e.groups[id] = g
	} else if !sameMembers(g.spec.Members, spec.Members) {
		// Other members: what was compared before does not continue.
		g.activity, g.recent, g.lastCorr, g.next = nil, nil, nil, time.Time{}
	}
	g.spec = spec
	now := time.Now()
	for _, m := range spec.Members {
		e.streams[m.Stream].update(now)
	}
	e.notify("group.state", e.groupView(g))
	writeJSON(w, 200, e.groupView(g))
}

func sameMembers(a, b []groupMember) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Stream != b[i].Stream || a[i].Analysis != b[i].Analysis {
			return false
		}
	}
	return true
}

func (e *Engine) deleteGroup(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	g, ok := e.findGroup(w, r)
	if !ok {
		return
	}
	for s := range g.subs {
		close(s.ch)
		delete(g.subs, s)
	}
	delete(e.groups, g.id)
	now := time.Now()
	for _, m := range g.spec.Members {
		if s := e.streams[m.Stream]; s != nil {
			s.update(now)
		}
	}
	e.notify("group.state", map[string]any{"id": g.id, "deleted": true})
	w.WriteHeader(204)
}

// emit stamps a group event and hands it to subscribers and the history.
func (g *group) emit(typ string, t time.Time, payload any) json.RawMessage {
	g.seq++
	data, _ := json.Marshal(map[string]any{
		"type": typ, "version": 1, "timestamp": t.UTC().Format("2006-01-02T15:04:05.000Z07:00"), "sequence": g.seq,
		"source": g.id, "label": g.spec.Label, "time_source": "wall", "payload": payload,
	})
	msg := []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", g.seq, typ, data))
	for sb := range g.subs {
		if sb.types != nil && !sb.types[typ] {
			continue
		}
		select {
		case sb.ch <- msg:
		default:
		}
	}
	if typ != "group.status" {
		g.history = append(g.history, histEvent{t, typ, data})
		if len(g.history) > groupHistoryMax {
			g.history = g.history[len(g.history)-groupHistoryMax:]
		}
	}
	return data
}

// tick reports every second that all present members have passed, and
// once a minute the correlations.
func (e *Engine) tickGroup(g *group, now time.Time) {
	if !g.wanted() {
		if g.running {
			g.running = false
			e.notify("group.state", e.groupView(g))
		}
		return
	}
	if !g.running {
		g.running = true
		e.notify("group.state", e.groupView(g))
	}
	watermark := now.UTC().Truncate(time.Second).Add(-groupMargin * time.Second)
	if g.next.IsZero() || watermark.Sub(g.next) > activityKept*time.Second {
		g.next = watermark
	}
	for ; g.next.Before(watermark); g.next = g.next.Add(time.Second) {
		e.reportSecond(g, g.next)
	}
	g.statusTick++
	if g.lastStatus == nil || g.statusTick%int(5*time.Second/e.opts.Tick) == 0 {
		g.lastStatus = g.emit("group.status", now, e.groupStatus(g, watermark))
	}
}

func (e *Engine) groupStatus(g *group, watermark time.Time) map[string]any {
	members := make([]map[string]any, 0, len(g.spec.Members))
	for _, m := range g.spec.Members {
		members = append(members, map[string]any{"stream": m.Stream, "analysis": m.Analysis, "state": e.memberState(m), "time_source": "wall", "delay_s": nil})
	}
	return map[string]any{"members": members, "watermark": watermark.Format(time.RFC3339), "behind_s": groupMargin, "time_source": "wall"}
}

func (e *Engine) reportSecond(g *group, t time.Time) {
	sec := activitySec{T: t, vals: make([]*secStat, len(g.spec.Members))}
	members := make([]map[string]any, 0, len(g.spec.Members))
	for i, m := range g.spec.Members {
		v := map[string]any{"stream": m.Stream, "analysis": m.Analysis, "present": false}
		if s := e.streams[m.Stream]; s != nil && s.analyses[m.Analysis] != nil {
			if st, ok := s.second(t); ok {
				sec.vals[i] = &st
				v["present"] = true
				// No impulse rate, as in the engine until it measures
				// impulses (with station time).
				v["level_db"], v["flux"] = round(st.LevelDB, 1), round(st.Flux, 3)
			}
		}
		members = append(members, v)
	}
	g.activity = append(g.activity, sec)
	if len(g.activity) > activityKept {
		g.activity = g.activity[len(g.activity)-activityKept:]
	}
	data := g.emit("group.activity", t, map[string]any{"second": t.Format(time.RFC3339), "members": members})
	g.recent = append(g.recent, data)
	if len(g.recent) > 600 {
		g.recent = g.recent[len(g.recent)-600:]
	}
	if t.Second() == 0 {
		if p := g.correlations(); p != nil {
			g.lastCorr = g.emit("group.correlation", t, p)
		}
	}
}

// correlations compares the impulse rates of every pair over the last
// window, at lags up to corrMaxLag either way, and next to it the same pair
// shifted far apart: a correlation is worth something only above that.
func (g *group) correlations() map[string]any {
	n := len(g.activity)
	if n < corrWindow {
		return nil
	}
	series := func(i int) []float64 {
		out := make([]float64, n)
		for k, s := range g.activity {
			if s.vals[i] == nil {
				out[k] = math.NaN()
			} else {
				out[k] = s.vals[i].Flux
			}
		}
		return out
	}
	pairs := []map[string]any{}
	for i := 0; i < len(g.spec.Members); i++ {
		for j := i + 1; j < len(g.spec.Members); j++ {
			a, b := series(i), series(j)
			r, lag, used := bestLag(a, b, n-corrWindow, corrWindow, 0)
			p := map[string]any{"a": g.spec.Members[i].Stream, "b": g.spec.Members[j].Stream, "n": used, "r": nil, "lag_s": nil, "baseline_r": nil}
			if used >= corrWindow/2 {
				p["r"], p["lag_s"] = round(r, 3), lag
			}
			if start := n - corrWindow - baselineShift; start-corrMaxLag >= 0 {
				if br, _, bused := bestLag(a, b, start, corrWindow, baselineShift); bused >= corrWindow/2 {
					p["baseline_r"] = round(br, 3)
				}
			}
			pairs = append(pairs, p)
		}
	}
	return map[string]any{"window_s": corrWindow, "max_lag_s": corrMaxLag, "series": "flux", "pairs": pairs}
}

// bestLag finds the lag at which b follows a most closely: a over
// [start, start+length) against b shifted by shift+lag. A positive lag
// means b hears the same thing later than a.
func bestLag(a, b []float64, start, length, shift int) (float64, int, int) {
	best, bestLag, bestN := 0.0, 0, 0
	for lag := -corrMaxLag; lag <= corrMaxLag; lag++ {
		var xs, ys []float64
		for k := start; k < start+length; k++ {
			m := k + lag + shift
			if m < 0 || m >= len(b) || math.IsNaN(a[k]) || math.IsNaN(b[m]) {
				continue
			}
			xs, ys = append(xs, a[k]), append(ys, b[m])
		}
		if len(xs) < 10 {
			continue
		}
		if r := pearson(xs, ys); math.Abs(r) > math.Abs(best) {
			best, bestLag, bestN = r, lag, len(xs)
		}
	}
	return best, bestLag, bestN
}

func pearson(x, y []float64) float64 {
	n := float64(len(x))
	var sx, sy float64
	for i := range x {
		sx += x[i]
		sy += y[i]
	}
	mx, my := sx/n, sy/n
	var cov, vx, vy float64
	for i := range x {
		dx, dy := x[i]-mx, y[i]-my
		cov += dx * dy
		vx += dx * dx
		vy += dy * dy
	}
	if vx == 0 || vy == 0 {
		return 0
	}
	return cov / math.Sqrt(vx*vy)
}

func (e *Engine) groupEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, &apiError{status: 500, code: "internal", message: "no streaming"})
		return
	}
	e.mu.Lock()
	g, ok := e.findGroup(w, r)
	if !ok {
		e.mu.Unlock()
		return
	}
	sb := &sub{ch: make(chan []byte, 512)}
	if t := r.URL.Query().Get("types"); t != "" {
		sb.types = map[string]bool{}
		for _, x := range strings.Split(t, ",") {
			sb.types[strings.TrimSpace(x)] = true
		}
	}
	g.subs[sb] = struct{}{}
	now := time.Now()
	for _, m := range g.spec.Members {
		if s := e.streams[m.Stream]; s != nil {
			s.update(now)
		}
	}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(g.subs, sb)
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

func (e *Engine) groupState(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	g, ok := e.findGroup(w, r)
	if !ok {
		return
	}
	or := func(v json.RawMessage) json.RawMessage {
		if v == nil {
			return json.RawMessage("null")
		}
		return v
	}
	writeJSON(w, 200, map[string]any{
		"group": e.groupView(g), "status": or(g.lastStatus), "activity": append([]json.RawMessage{}, g.recent...),
		"correlation": or(g.lastCorr), "clock": nil, "coincidence": nil,
	})
}

func (e *Engine) groupHistory(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	g, ok := e.findGroup(w, r)
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
	i := sort.Search(len(g.history), func(i int) bool { return !g.history[i].t.Before(from) })
	for ; i < len(g.history) && g.history[i].t.Before(to); i++ {
		if typ != "" && g.history[i].typ != typ {
			continue
		}
		out = append(out, g.history[i].data)
		if len(out) >= limit {
			break
		}
	}
	writeJSON(w, 200, map[string]any{"from": from, "to": to, "events": out})
}
