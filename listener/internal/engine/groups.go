package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

// Groups (control API 1.1): analyses of several streams compared on one
// time axis. A group never reads audio: it takes the frames its members'
// analyses produce, in this process. Without station time the axis is
// wall time, which aligns streams to a few seconds; only `activity` works
// at that accuracy.

const (
	groupMargin   = 2 * time.Second  // the group waits this long for late data
	absentAfter   = 30 * time.Second // a member that sent nothing for this long is absent
	activityKept  = 900              // seconds of activity kept for correlations
	corrWindow    = 300              // seconds compared per correlation
	corrMaxLag    = 30               // seconds of lag searched either way
	baselineShift = 150              // seconds one series is shifted for the baseline
	groupRecent   = 600              // activity events kept for the state
)

// groupAnalyses are the group analyses this engine offers.
var groupAnalyses = []map[string]string{{"id": "activity", "timing": "seconds"}}

type GroupMember struct {
	Stream   string   `json:"stream"`
	Analysis string   `json:"analysis"`
	Lat      *float64 `json:"lat,omitempty"`
	Lon      *float64 `json:"lon,omitempty"`
}

// GroupSpec is the body of PUT /v1/groups/{group}.
type GroupSpec struct {
	Members     []GroupMember `json:"members"`
	Analyses    []string      `json:"analyses"`
	HistoryDays *int          `json:"history_days,omitempty"`
	Label       string        `json:"label,omitempty"`
	Mode        string        `json:"mode,omitempty"`
}

// Member states in a group.
const (
	MemberPresent = "present"
	MemberAbsent  = "absent"
	MemberRemoved = "removed"
)

type group struct {
	c  *caller
	id string

	mu       sync.Mutex
	spec     GroupSpec
	members  []*gmember
	subs     map[*sub]struct{}
	seq      uint64
	running  bool
	next     time.Time // the next second to report
	activity []actSec
	recent   []json.RawMessage // the last activity events
	status   json.RawMessage
	corr     json.RawMessage
	statusAt time.Time
	hist     *dayLog
}

// gmember collects one member analysis's frames per second of wall time.
type gmember struct {
	stream, analysis string
	run              *run // the analysis observed; replaced if it is made again

	mu      sync.Mutex
	secs    map[int64]*secAcc
	latest  time.Time // timestamp of the latest frame
	arrived time.Time // when it arrived
}

type secAcc struct {
	n           int
	level, flux float64
}

// actSec is one reported second: each member's flux, NaN where absent.
type actSec struct {
	t    time.Time
	flux []float64
}

// observe takes one of the member analysis's frames. It runs on that
// analysis's worker.
func (m *gmember) observe(env events.Envelope) {
	f, ok := env.Payload.(events.Frame)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := env.Timestamp.Unix()
	a := m.secs[k]
	if a == nil {
		a = &secAcc{}
		m.secs[k] = a
	}
	a.n++
	a.level += f.LevelDB
	a.flux += f.Flux
	if env.Timestamp.After(m.latest) {
		m.latest = env.Timestamp
	}
	m.arrived = time.Now()
}

// take removes and returns the second starting at t, and drops older ones.
func (m *gmember) take(t time.Time) *secAcc {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := t.Unix()
	a := m.secs[k]
	for s := range m.secs {
		if s <= k {
			delete(m.secs, s)
		}
	}
	return a
}

func (m *gmember) seen() (latest, arrived time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.latest, m.arrived
}

// --- observers on analyses ---

// watch makes r hand its frames to m (copy on write, read by the worker).
func (r *run) watch(m *gmember) {
	for {
		old := r.observers.Load()
		var l []*gmember
		if old != nil {
			l = append(l, *old...)
		}
		l = append(l, m)
		if r.observers.CompareAndSwap(old, &l) {
			return
		}
	}
}

func (r *run) unwatch(m *gmember) {
	for {
		old := r.observers.Load()
		if old == nil {
			return
		}
		var l []*gmember
		for _, x := range *old {
			if x != m {
				l = append(l, x)
			}
		}
		if r.observers.CompareAndSwap(old, &l) {
			return
		}
	}
}

func (r *run) notifyObservers(m events.Message) {
	if m.Type != events.TypeFrame {
		return
	}
	if l := r.observers.Load(); l != nil {
		for _, gm := range *l {
			gm.observe(m.Env)
		}
	}
}

// --- the registry ---

func (c *caller) checkGroup(spec *GroupSpec) []FieldError {
	var fe []FieldError
	lim := c.e.opts.Limits
	if n := len(spec.Members); n < 2 || n > lim.MembersPerGroup {
		fe = append(fe, FieldError{"members", "out_of_range", fmt.Sprintf("a group has 2 to %d members", lim.MembersPerGroup)})
	}
	seen := map[string]bool{}
	for i, m := range spec.Members {
		path := fmt.Sprintf("members[%d]", i)
		_, _, err := c.find(m.Stream, m.Analysis)
		switch {
		case c.stream(m.Stream) == nil:
			fe = append(fe, FieldError{path + ".stream", "not_found", "no such stream"})
		case err != nil:
			fe = append(fe, FieldError{path + ".analysis", "not_found", "no such analysis on " + m.Stream})
		case seen[m.Stream+"/"+m.Analysis]:
			fe = append(fe, FieldError{path, "duplicate", "named twice"})
		}
		seen[m.Stream+"/"+m.Analysis] = true
		if (m.Lat == nil) != (m.Lon == nil) || m.Lat != nil && !(math.Abs(*m.Lat) <= 90 && math.Abs(*m.Lon) <= 180) {
			fe = append(fe, FieldError{path + ".lat", "invalid", "lat and lon together, within -90..90 and -180..180"})
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
			fe = append(fe, FieldError{path, "needs_station_time", a + " needs every group member on station time; no member is"})
		default:
			fe = append(fe, FieldError{path, "unknown", "no such group analysis"})
		}
	}
	if spec.Mode == "" {
		spec.Mode = OnDemand
	}
	if spec.Mode != OnDemand && spec.Mode != Continuous {
		fe = append(fe, FieldError{"mode", "invalid", "on_demand or continuous"})
	}
	if d := spec.HistoryDays; d != nil && (*d < 1 || *d > 365) {
		fe = append(fe, FieldError{"history_days", "out_of_range", "must be between 1 and 365 days"})
	}
	if len(spec.Label) > 63 {
		fe = append(fe, FieldError{"label", "out_of_range", "at most 63 characters"})
	}
	return fe
}

func (c *caller) groupDir(id string) string { return filepath.Join(c.memDir, "_groups", id) }

// putGroup creates or changes a group.
func (c *caller) putGroup(id string, spec GroupSpec) (*group, *apiError) {
	if fe := c.checkGroup(&spec); fe != nil {
		return nil, &apiError{status: 422, code: "invalid", message: "the group was refused", errors: fe}
	}
	days := 30
	if spec.HistoryDays != nil {
		days = *spec.HistoryDays
	}
	c.gmu.Lock()
	g := c.groups[id]
	if g == nil {
		if !reserve(&c.e.ngroups, c.e.opts.Limits.Groups) {
			c.gmu.Unlock()
			return nil, &apiError{status: 409, code: "limit_reached", message: fmt.Sprintf("the engine holds %d groups, its limit", c.e.opts.Limits.Groups)}
		}
		hist, err := openDayLog(c.groupDir(id), days)
		if err != nil {
			c.e.ngroups.Add(-1)
			c.gmu.Unlock()
			return nil, &apiError{status: 500, code: "internal", message: err.Error()}
		}
		g = &group{c: c, id: id, subs: map[*sub]struct{}{}, hist: hist}
		c.groups[id] = g
	}
	c.gmu.Unlock()

	g.mu.Lock()
	if !sameMembers(g.spec.Members, spec.Members) {
		// Other members: what was compared before does not continue.
		for _, m := range g.members {
			if m.run != nil {
				m.run.unwatch(m)
			}
		}
		g.members = nil
		for _, m := range spec.Members {
			g.members = append(g.members, &gmember{stream: m.Stream, analysis: m.Analysis, secs: map[int64]*secAcc{}})
		}
		g.activity, g.recent, g.corr, g.next = nil, nil, nil, time.Time{}
	}
	g.spec = spec
	g.hist.setKeep(days)
	g.mu.Unlock()
	g.attach()
	c.saveConfig()
	c.notify("group.state", g.view())
	return g, nil
}

func sameMembers(a, b []GroupMember) bool {
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

func (c *caller) deleteGroup(id string, purge bool) *apiError {
	c.gmu.Lock()
	g := c.groups[id]
	if g != nil {
		delete(c.groups, id)
		c.e.ngroups.Add(-1)
	}
	c.gmu.Unlock()
	if g == nil {
		return errNotFound("group")
	}
	g.mu.Lock()
	for sb := range g.subs {
		delete(g.subs, sb)
		close(sb.ch)
	}
	for _, m := range g.members {
		if m.run != nil {
			m.run.unwatch(m)
		}
	}
	g.hist.close()
	g.mu.Unlock()
	if purge {
		os.RemoveAll(c.groupDir(id))
	}
	c.saveConfig()
	c.notify("group.state", map[string]any{"id": id, "deleted": true})
	return nil
}

func (c *caller) group(id string) *group {
	c.gmu.Lock()
	defer c.gmu.Unlock()
	return c.groups[id]
}

func (c *caller) groupList() []*group {
	c.gmu.Lock()
	defer c.gmu.Unlock()
	ids := make([]string, 0, len(c.groups))
	for id := range c.groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*group, 0, len(ids))
	for _, id := range ids {
		out = append(out, c.groups[id])
	}
	return out
}

// groupWants reports whether a running group needs the stream connected.
func (c *caller) groupWants(streamID string) bool {
	for _, g := range c.groupList() {
		g.mu.Lock()
		wanted := g.wantedLocked()
		hit := false
		for _, m := range g.spec.Members {
			hit = hit || m.Stream == streamID
		}
		g.mu.Unlock()
		if wanted && hit {
			return true
		}
	}
	return false
}

func (g *group) wantedLocked() bool { return g.spec.Mode == Continuous || len(g.subs) > 0 }

// attach makes every member observe its analysis as it is now (an analysis
// deleted and made again is a new one). It takes no group lock while it
// looks the analyses up.
func (g *group) attach() {
	g.mu.Lock()
	members := append([]*gmember(nil), g.members...)
	g.mu.Unlock()
	for _, m := range members {
		_, cur, _ := g.c.find(m.stream, m.analysis)
		g.mu.Lock()
		if cur != m.run {
			if m.run != nil {
				m.run.unwatch(m)
			}
			m.run = cur
			if cur != nil {
				cur.watch(m)
			}
		}
		g.mu.Unlock()
	}
}

// snapshotLocked copies the members and the analyses they observe.
func (g *group) snapshotLocked() ([]*gmember, []*run) {
	members := append([]*gmember(nil), g.members...)
	runs := make([]*run, len(members))
	for i, m := range members {
		runs[i] = m.run
	}
	return members, runs
}

// memberStates says for each member whether it is present, absent or
// removed; looked up without the group lock.
func (g *group) memberStates(members []*gmember, runs []*run, now time.Time) []string {
	out := make([]string, len(members))
	for i, m := range members {
		s, r, err := g.c.find(m.stream, m.analysis)
		_, arrived := m.seen()
		switch {
		case err != nil || r != runs[i]:
			out[i] = MemberRemoved
		case !s.connected() || now.Sub(arrived) > absentAfter:
			out[i] = MemberAbsent
		default:
			out[i] = MemberPresent
		}
	}
	return out
}

// --- the group's clock ---

// groupLoop advances every group four times a second.
func (e *Engine) groupLoop() {
	defer e.loops.Done()
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case now := <-t.C:
			for _, c := range e.callers {
				for _, g := range c.groupList() {
					g.attach()
					g.tick(now)
				}
			}
		}
	}
}

// tick reports every second that all present members have passed, every
// 5 seconds the status, and once a minute the correlations.
func (g *group) tick(now time.Time) {
	g.mu.Lock()
	members, runs := g.snapshotLocked()
	wanted, running := g.wantedLocked(), g.running
	g.mu.Unlock()
	if wanted != running {
		g.mu.Lock()
		g.running = wanted
		g.mu.Unlock()
		g.c.notify("group.state", g.view())
	}
	if !wanted {
		return
	}
	states := g.memberStates(members, runs, now)

	// The watermark: the earliest latest frame of the present members,
	// less the margin, and never later than now less the margin.
	wm := now.Add(-groupMargin)
	for i, m := range members {
		if states[i] != MemberPresent {
			continue
		}
		if latest, _ := m.seen(); latest.Add(-groupMargin).Before(wm) {
			wm = latest.Add(-groupMargin)
		}
	}
	wm = wm.UTC().Truncate(time.Second)

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.next.IsZero() || wm.Sub(g.next) > activityKept*time.Second {
		g.next = wm
	}
	for ; g.next.Before(wm); g.next = g.next.Add(time.Second) {
		g.reportSecondLocked(g.next, members, states)
	}
	if now.Sub(g.statusAt) >= 5*time.Second {
		g.statusAt = now
		ms := make([]map[string]any, len(members))
		for i, m := range members {
			ms[i] = map[string]any{"stream": m.stream, "analysis": m.analysis, "state": states[i], "time_source": "wall", "delay_s": nil}
		}
		g.status = g.emitLocked("group.status", now, map[string]any{
			"members": ms, "watermark": wm.Format(time.RFC3339),
			"behind_s": round(now.Sub(wm).Seconds(), 1), "time_source": "wall",
		})
	}
}

func (g *group) reportSecondLocked(t time.Time, members []*gmember, states []string) {
	sec := actSec{t: t, flux: make([]float64, len(members))}
	ms := make([]map[string]any, len(members))
	for i, m := range members {
		v := map[string]any{"stream": m.stream, "analysis": m.analysis, "present": false}
		sec.flux[i] = math.NaN()
		if a := m.take(t); a != nil && a.n > 0 && states[i] != MemberRemoved {
			v["present"] = true
			v["level_db"] = round(a.level/float64(a.n), 1)
			v["flux"] = round(a.flux/float64(a.n), 3)
			sec.flux[i] = a.flux / float64(a.n)
		}
		ms[i] = v
	}
	g.activity = append(g.activity, sec)
	if len(g.activity) > activityKept {
		g.activity = g.activity[len(g.activity)-activityKept:]
	}
	data := g.emitLocked("group.activity", t, map[string]any{"second": t.Format(time.RFC3339), "members": ms})
	g.recent = append(g.recent, data)
	if len(g.recent) > groupRecent {
		g.recent = g.recent[len(g.recent)-groupRecent:]
	}
	if t.Second() == 0 {
		if p := g.correlationsLocked(); p != nil {
			g.corr = g.emitLocked("group.correlation", t, p)
		}
	}
}

// correlationsLocked compares the flux of every pair over the last window,
// at lags up to corrMaxLag either way, and next to it the same pair
// shifted far apart: a correlation means something only above that.
func (g *group) correlationsLocked() map[string]any {
	n := len(g.activity)
	if n < corrWindow {
		return nil
	}
	series := func(i int) []float64 {
		out := make([]float64, n)
		for k, s := range g.activity {
			out[k] = s.flux[i]
		}
		return out
	}
	pairs := []map[string]any{}
	for i := 0; i < len(g.members); i++ {
		for j := i + 1; j < len(g.members); j++ {
			a, b := series(i), series(j)
			r, lag, used := bestLag(a, b, n-corrWindow, corrWindow, 0)
			p := map[string]any{"a": g.members[i].stream, "b": g.members[j].stream, "n": used, "r": nil, "lag_s": nil, "baseline_r": nil}
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
	xs, ys := make([]float64, 0, length), make([]float64, 0, length)
	for lag := -corrMaxLag; lag <= corrMaxLag; lag++ {
		xs, ys = xs[:0], ys[:0]
		for k := start; k < start+length; k++ {
			m := k + lag + shift
			if k < 0 || m < 0 || m >= len(b) || math.IsNaN(a[k]) || math.IsNaN(b[m]) {
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

// groupEnvelope is a group event: as an analysis's, plus label and time
// source.
type groupEnvelope struct {
	Type       string `json:"type"`
	Version    int    `json:"version"`
	Timestamp  string `json:"timestamp"`
	Sequence   uint64 `json:"sequence"`
	Source     string `json:"source"`
	Label      string `json:"label"`
	TimeSource string `json:"time_source"`
	Payload    any    `json:"payload"`
}

// emitLocked stamps a group event and hands it to the subscribers and,
// except the status, to the history.
func (g *group) emitLocked(typ string, t time.Time, payload any) json.RawMessage {
	g.seq++
	data, _ := json.Marshal(groupEnvelope{
		Type: typ, Version: events.Version, Timestamp: t.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		Sequence: g.seq, Source: g.id, Label: g.spec.Label, TimeSource: "wall", Payload: payload,
	})
	frame := []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", g.seq, typ, data))
	for sb := range g.subs {
		if sb.types != nil && !sb.types[typ] {
			continue
		}
		select {
		case sb.ch <- frame:
		default:
		}
	}
	if typ != "group.status" {
		g.hist.append(t, typ, data)
	}
	return data
}

func (g *group) view() map[string]any {
	g.mu.Lock()
	members, runs := g.snapshotLocked()
	spec, running, nsubs := g.spec, g.running, len(g.subs)
	g.mu.Unlock()
	states := g.memberStates(members, runs, time.Now())
	ms := make([]map[string]any, len(spec.Members))
	for i, m := range spec.Members {
		v := map[string]any{"stream": m.Stream, "analysis": m.Analysis, "state": states[i], "time_source": "wall", "delay_s": nil}
		if m.Lat != nil && m.Lon != nil {
			v["lat"], v["lon"] = *m.Lat, *m.Lon
		}
		ms[i] = v
	}
	state := "idle"
	if running {
		state = "running"
	}
	days := 30
	if spec.HistoryDays != nil {
		days = *spec.HistoryDays
	}
	return map[string]any{
		"id": g.id, "members": ms, "analyses": spec.Analyses, "mode": spec.Mode, "history_days": days,
		"label": spec.Label, "state": state, "subscribers": nsubs,
	}
}

// --- API ---

func (c *caller) groupRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /v1/groups", func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, g := range c.groupList() {
			out = append(out, g.view())
		}
		writeJSON(w, 200, map[string]any{"groups": out})
	})
	m.HandleFunc("GET /v1/groups/{g}", func(w http.ResponseWriter, r *http.Request) {
		if g := c.group(r.PathValue("g")); g != nil {
			writeJSON(w, 200, g.view())
			return
		}
		writeErr(w, errNotFound("group"))
	})
	m.HandleFunc("PUT /v1/groups/{g}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("g")
		if err := checkID(id); err != nil {
			writeErr(w, err)
			return
		}
		var spec GroupSpec
		if err := decode(r, &spec); err != nil {
			writeErr(w, err)
			return
		}
		g, err := c.putGroup(id, spec)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, 200, g.view())
	})
	m.HandleFunc("DELETE /v1/groups/{g}", func(w http.ResponseWriter, r *http.Request) {
		if err := c.deleteGroup(r.PathValue("g"), purge(r)); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(204)
	})
	m.HandleFunc("GET /v1/groups/{g}/events", c.groupEvents)
	m.HandleFunc("GET /v1/groups/{g}/state", func(w http.ResponseWriter, r *http.Request) {
		g := c.group(r.PathValue("g"))
		if g == nil {
			writeErr(w, errNotFound("group"))
			return
		}
		view := g.view()
		g.mu.Lock()
		or := func(v json.RawMessage) json.RawMessage {
			if v == nil {
				return json.RawMessage("null")
			}
			return v
		}
		out := map[string]any{
			"group": view, "status": or(g.status), "activity": append([]json.RawMessage{}, g.recent...),
			"correlation": or(g.corr), "clock": nil, "coincidence": nil,
		}
		g.mu.Unlock()
		writeJSON(w, 200, out)
	})
	m.HandleFunc("GET /v1/groups/{g}/history/events", func(w http.ResponseWriter, r *http.Request) {
		g := c.group(r.PathValue("g"))
		if g == nil {
			writeErr(w, errNotFound("group"))
			return
		}
		g.hist.serve(w, r)
	})
}

func (c *caller) groupEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, &apiError{status: 500, code: "internal", message: "no streaming"})
		return
	}
	g := c.group(r.PathValue("g"))
	if g == nil {
		writeErr(w, errNotFound("group"))
		return
	}
	var types map[string]bool
	if t := r.URL.Query().Get("types"); t != "" {
		types = map[string]bool{}
		for _, x := range strings.Split(t, ",") {
			if x = strings.TrimSpace(x); x != "" {
				types[x] = true
			}
		}
	}
	g.mu.Lock()
	if len(g.subs) >= c.e.opts.Limits.SubscriptionsPerAnalysis {
		g.mu.Unlock()
		writeErr(w, &apiError{status: 409, code: "limit_reached", message: "too many subscriptions to this group"})
		return
	}
	sb := &sub{ch: make(chan []byte, 512), types: types}
	g.subs[sb] = struct{}{}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		if _, still := g.subs[sb]; still {
			delete(g.subs, sb)
			close(sb.ch)
		}
		g.mu.Unlock()
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
		case <-c.e.ctx.Done():
			return
		case frame, open := <-sb.ch:
			if !open {
				return
			}
			w.Write(frame)
			fl.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

// --- history ---

// dayLog keeps a group's events as JSON lines, one file per UTC day.
type dayLog struct {
	dir  string
	keep atomic.Int64 // days

	mu      sync.Mutex
	day     string
	f       *os.File
	cleaned time.Time
}

type dayRecord struct {
	T     time.Time       `json:"t"`
	Type  string          `json:"type"`
	Event json.RawMessage `json:"event"`
}

func openDayLog(dir string, keepDays int) (*dayLog, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	l := &dayLog{dir: dir}
	l.setKeep(keepDays)
	return l, nil
}

func (l *dayLog) setKeep(days int) { l.keep.Store(int64(days)) }

func (l *dayLog) append(t time.Time, typ string, ev json.RawMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	day := t.UTC().Format("2006-01-02")
	if day != l.day || l.f == nil {
		if l.f != nil {
			l.f.Close()
		}
		f, err := os.OpenFile(filepath.Join(l.dir, day+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			log.Printf("group history: %v", err)
			l.f = nil
			return
		}
		l.f, l.day = f, day
	}
	b, _ := json.Marshal(dayRecord{T: t.UTC(), Type: typ, Event: ev})
	l.f.Write(append(b, '\n'))
	if time.Since(l.cleaned) > 24*time.Hour {
		l.cleaned = time.Now()
		cut := time.Now().UTC().AddDate(0, 0, -int(l.keep.Load())).Format("2006-01-02")
		ents, _ := os.ReadDir(l.dir)
		for _, e := range ents {
			if d := strings.TrimSuffix(e.Name(), ".jsonl"); d != e.Name() && d < cut {
				os.Remove(filepath.Join(l.dir, e.Name()))
			}
		}
	}
}

func (l *dayLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
}

// serve answers …/history/events: ?from=&to= (RFC 3339, default the last
// hour), ?type=, ?limit= (at most 2000).
func (l *dayLog) serve(w http.ResponseWriter, r *http.Request) {
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
	if n, err := fmt.Sscan(q.Get("limit"), &limit); n != 1 || err != nil || limit <= 0 || limit > 2000 {
		limit = 2000
	}
	typ := q.Get("type")
	l.mu.Lock()
	if l.f != nil {
		l.f.Sync()
	}
	l.mu.Unlock()
	out := []json.RawMessage{}
	for d := from.UTC().Truncate(24 * time.Hour); !d.After(to) && len(out) < limit; d = d.Add(24 * time.Hour) {
		b, err := os.ReadFile(filepath.Join(l.dir, d.Format("2006-01-02")+".jsonl"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if line == "" {
				continue
			}
			var rec dayRecord
			if json.Unmarshal([]byte(line), &rec) != nil || rec.T.Before(from) || !rec.T.Before(to) || typ != "" && rec.Type != typ {
				continue
			}
			out = append(out, rec.Event)
			if len(out) >= limit {
				break
			}
		}
	}
	writeJSON(w, 200, map[string]any{"from": from, "to": to, "events": out})
}
