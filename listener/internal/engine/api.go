package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/vrontier/listen/listener/internal/memory"
)

type apiError struct {
	status  int
	code    string
	message string
	errors  []FieldError
}

func errNotFound(what string) *apiError {
	return &apiError{status: 404, code: "not_found", message: "no such " + what}
}

// Handler serves the control API.
func (c *caller) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/engine", c.getEngine)
	m.HandleFunc("GET /v1/events", c.getChanges)
	m.HandleFunc("GET /v1/streams", c.listStreams)
	m.HandleFunc("GET /v1/streams/{s}", c.getStream)
	m.HandleFunc("PUT /v1/streams/{s}", c.handlePutStream)
	m.HandleFunc("DELETE /v1/streams/{s}", c.handleDeleteStream)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}", c.getAnalysis)
	m.HandleFunc("PUT /v1/streams/{s}/analyses/{a}", c.handlePutAnalysis)
	m.HandleFunc("DELETE /v1/streams/{s}/analyses/{a}", c.handleDeleteAnalysis)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/events", c.analysisEvents)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/state", c.analysisState)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/motifs", c.analysisMotifs)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/motifs/{m}", c.analysisMotif)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/history/events", c.historyEvents)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/history/features", c.historyFeatures)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/memories", c.listMemories)
	m.HandleFunc("DELETE /v1/streams/{s}/analyses/{a}/memories/{key}", c.deleteMemory)
	m.HandleFunc("POST /v1/validate/analysis", c.validate)
	m.HandleFunc("POST /v1/probes", c.handleProbe)
	if c.testHooks {
		// Development and tests only (-test-hooks, loopback callers):
		// make an analysis fail as a crash in it would.
		m.HandleFunc("POST /v1/streams/{s}/analyses/{a}/fail", func(w http.ResponseWriter, r *http.Request) {
			_, run, err := c.find(r.PathValue("s"), r.PathValue("a"))
			if err != nil {
				writeErr(w, err)
				return
			}
			run.failNow("made to fail by a test hook")
			writeJSON(w, 200, run.view())
		})
	}
	c.groupRoutes(m)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, errNotFound("resource")) })
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, a *apiError) {
	errs := a.errors
	if errs == nil {
		errs = []FieldError{}
	}
	writeJSON(w, a.status, map[string]any{"error": map[string]any{"code": a.code, "message": a.message, "errors": errs}})
}

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func checkID(id string) *apiError {
	if !idRE.MatchString(id) {
		return &apiError{status: 400, code: "bad_id", message: "identifiers are 1 to 63 lower-case letters, digits and hyphens, starting with a letter or digit"}
	}
	return nil
}

// decode reads a JSON body and refuses fields it does not know, so a
// misspelt setting is an error and not a setting silently left out.
func decode(r *http.Request, v any) *apiError {
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return &apiError{status: 400, code: "bad_request", message: err.Error()}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return &apiError{status: 400, code: "unknown_field", message: err.Error()}
		}
		return &apiError{status: 400, code: "bad_request", message: err.Error()}
	}
	if dec.More() {
		return &apiError{status: 400, code: "bad_request", message: "one JSON object expected"}
	}
	return nil
}

func purge(r *http.Request) bool { return r.URL.Query().Get("purge") == "true" }

// --- engine and changes ---

func (c *caller) getEngine(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{"streams": 0, "connected": 0, "analyses": 0, "groups": len(c.groupList())}
	c.mu.Lock()
	counts["streams"] = len(c.streams)
	streams := make([]*stream, 0, len(c.streams))
	for _, s := range c.streams {
		streams = append(streams, s)
	}
	c.mu.Unlock()
	for _, s := range streams {
		s.mu.Lock()
		counts["analyses"] += len(s.runs)
		s.mu.Unlock()
		if s.connected() {
			counts["connected"]++
		}
	}
	check := "every_connection" // the engine dials itself and checks each address it dials
	writeJSON(w, 200, map[string]any{
		"api":            map[string]int{"major": APIMajor, "minor": APIMinor},
		"engine_version": c.e.opts.Version,
		"instance":       c.e.instance,
		"started":        c.e.started,
		"resumed":        c.resumed,
		"limits":         c.e.opts.Limits,
		"limits_scope":   "engine", // the limits count every caller's streams and groups; counts are this caller's
		"capabilities": map[string]any{
			"rule_kinds": []string{RuleLine, RuleComb}, "detectors": []any{},
			"file_inputs": c.e.opts.AllowFiles, "address_check": check,
			"station_time": false, "group_analyses": groupAnalyses,
		},
		"counts": counts,
		"host":   map[string]int{"cores": hostCores(), "memory_mb": hostMemoryMB()},
		"usage":  c.e.meter.last(),
	})
}

func (c *caller) getChanges(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, &apiError{status: 500, code: "internal", message: "no streaming"})
		return
	}
	ch := c.addWatcher()
	defer c.removeWatcher(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	writeSSE(w, "engine.hello", c.hello())
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.e.ctx.Done():
			return
		case c := <-ch:
			writeSSE(w, c.typ, c.data)
			fl.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func writeSSE(w io.Writer, typ string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
}

// --- streams ---

func (c *caller) listStreams(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	streams := make([]*stream, 0, len(c.streams))
	for _, id := range c.streamIDs() {
		streams = append(streams, c.streams[id])
	}
	c.mu.Unlock()
	out := make([]StreamView, 0, len(streams))
	for _, s := range streams {
		out = append(out, s.viewOf(true))
	}
	writeJSON(w, 200, map[string]any{"streams": out})
}

func (c *caller) getStream(w http.ResponseWriter, r *http.Request) {
	s := c.stream(r.PathValue("s"))
	if s == nil {
		writeErr(w, errNotFound("stream"))
		return
	}
	writeJSON(w, 200, s.viewOf(true))
}

func (c *caller) handlePutStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("s")
	if err := checkID(id); err != nil {
		writeErr(w, err)
		return
	}
	var spec StreamSpec
	if err := decode(r, &spec); err != nil {
		writeErr(w, err)
		return
	}
	s, err := c.putStream(r.Context(), id, spec)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, s.viewOf(true))
}

func (c *caller) handleDeleteStream(w http.ResponseWriter, r *http.Request) {
	if err := c.deleteStream(r.PathValue("s"), purge(r)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// --- analyses ---

func (c *caller) getAnalysis(w http.ResponseWriter, r *http.Request) {
	_, run, err := c.find(r.PathValue("s"), r.PathValue("a"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, run.view())
}

func (c *caller) handlePutAnalysis(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("a")
	if err := checkID(id); err != nil {
		writeErr(w, err)
		return
	}
	var spec AnalysisSpec
	if err := decode(r, &spec); err != nil {
		writeErr(w, err)
		return
	}
	run, err := c.putAnalysis(r.PathValue("s"), id, spec)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, run.view())
}

func (c *caller) handleDeleteAnalysis(w http.ResponseWriter, r *http.Request) {
	if err := c.deleteAnalysis(r.PathValue("s"), r.PathValue("a"), purge(r)); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (c *caller) validate(w http.ResponseWriter, r *http.Request) {
	var req ValidateRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	ch, errs, warns := checkAnalysis(req.SampleRate, req.AnalysisSpec, c.e.opts.Limits)
	v := Validation{Valid: len(errs) == 0, Errors: errs, Warnings: warns}
	if v.Errors == nil {
		v.Errors = []FieldError{}
	}
	if v.Warnings == nil {
		v.Warnings = []FieldError{}
	}
	if v.Valid {
		eff := ch.effective()
		v.Effective = &eff
		v.Rules = ch.spec.Rules
	}
	writeJSON(w, 200, v)
}

func (c *caller) handleProbe(w http.ResponseWriter, r *http.Request) {
	var req ProbeRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res, err := c.probe(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

// --- data of an analysis ---

// analysisEvents streams an analysis's live events. A subscription is what
// connects an on-demand stream.
func (c *caller) analysisEvents(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, &apiError{status: 500, code: "internal", message: "no streaming"})
		return
	}
	_, run, err := c.find(r.PathValue("s"), r.PathValue("a"))
	if err != nil {
		writeErr(w, err)
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
	sb, ok := run.subscribe(types, c.e.opts.Limits.SubscriptionsPerAnalysis)
	if !ok {
		writeErr(w, &apiError{status: 409, code: "limit_reached", message: "too many subscriptions to this analysis"})
		return
	}
	defer run.unsubscribe(sb)
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

func (c *caller) analysisState(w http.ResponseWriter, r *http.Request) {
	_, run, err := c.find(r.PathValue("s"), r.PathValue("a"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, 200, run.snapshot())
}

// memoryFor returns the memory named by ?memory=, or the one in use; done
// releases a memory opened for reading.
func (c *caller) memoryFor(w http.ResponseWriter, r *http.Request) (m *memory.Memory, done func(), ok bool) {
	_, run, aerr := c.find(r.PathValue("s"), r.PathValue("a"))
	if aerr != nil {
		writeErr(w, aerr)
		return nil, nil, false
	}
	run.mu.Lock()
	cur, curKey := run.mem, run.key
	run.mu.Unlock()
	key := r.URL.Query().Get("memory")
	if key == "" || key == curKey {
		return cur, func() {}, true
	}
	dir := run.memoryDir(key)
	if !validKey(key) || !isDir(dir) {
		writeErr(w, errNotFound("memory"))
		return nil, nil, false
	}
	pm, err := memory.Peek(dir)
	if err != nil {
		writeErr(w, &apiError{status: 500, code: "internal", message: err.Error()})
		return nil, nil, false
	}
	return pm, func() { pm.Close() }, true
}

func (c *caller) analysisMotifs(w http.ResponseWriter, r *http.Request) {
	m, done, ok := c.memoryFor(w, r)
	if !ok {
		return
	}
	defer done()
	if m == nil {
		writeJSON(w, 200, map[string]any{"motifs": []any{}, "aliases": map[string]string{}})
		return
	}
	m.ServeMotifs(w, r)
}

func (c *caller) analysisMotif(w http.ResponseWriter, r *http.Request) {
	m, done, ok := c.memoryFor(w, r)
	if !ok {
		return
	}
	defer done()
	id := r.PathValue("m")
	if m == nil || !m.HasMotif(id) {
		writeErr(w, errNotFound("motif"))
		return
	}
	m.ServeMotif(w, r, id)
}

func (c *caller) historyEvents(w http.ResponseWriter, r *http.Request) {
	m, done, ok := c.memoryFor(w, r)
	if !ok {
		return
	}
	defer done()
	if m == nil {
		writeJSON(w, 200, map[string]any{"events": []any{}})
		return
	}
	m.ServeHistoryEvents(w, r)
}

func (c *caller) historyFeatures(w http.ResponseWriter, r *http.Request) {
	m, done, ok := c.memoryFor(w, r)
	if !ok {
		return
	}
	defer done()
	if m == nil {
		writeJSON(w, 200, map[string]any{"window": "1h0m0s", "minutes": []any{}})
		return
	}
	m.ServeHistoryFeatures(w, r)
}

// listMemories lists the memories kept on disk for an analysis.
func (c *caller) listMemories(w http.ResponseWriter, r *http.Request) {
	_, run, err := c.find(r.PathValue("s"), r.PathValue("a"))
	if err != nil {
		writeErr(w, err)
		return
	}
	run.mu.Lock()
	cur, curKey, mode := run.mem, run.key, run.c.memMode
	run.mu.Unlock()
	base := filepath.Join(c.memDir, run.s.id, run.id)
	ents, _ := os.ReadDir(base)
	out := []MemoryInfo{}
	for _, ent := range ents {
		if !ent.IsDir() || !validKey(ent.Name()) {
			continue
		}
		dir := filepath.Join(base, ent.Name())
		md, err := readMeta(dir)
		if err != nil {
			continue
		}
		settings, _ := json.Marshal(md.Settings)
		info := MemoryInfo{
			Key: ent.Name(), Settings: settings, Rules: md.Rules, FirstUsed: md.FirstUsed, LastUsed: md.LastUsed,
			Bytes: dirSize(dir), InUse: ent.Name() == curKey && mode == MemoryDisk,
		}
		if info.Rules == nil {
			info.Rules = []Rule{}
		}
		if info.InUse && cur != nil {
			info.Motifs = cur.Established()
			info.LastUsed = time.Now().UTC()
		} else if pm, err := memory.Peek(dir); err == nil {
			info.Motifs = pm.Established()
			pm.Close()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	writeJSON(w, 200, map[string]any{"memories": out})
}

func (c *caller) deleteMemory(w http.ResponseWriter, r *http.Request) {
	_, run, err := c.find(r.PathValue("s"), r.PathValue("a"))
	if err != nil {
		writeErr(w, err)
		return
	}
	key := r.PathValue("key")
	dir := run.memoryDir(key)
	if !validKey(key) || !isDir(dir) {
		writeErr(w, errNotFound("memory"))
		return
	}
	run.mu.Lock()
	inUse := key == run.key && run.c.memMode == MemoryDisk
	run.mu.Unlock()
	if inUse {
		writeErr(w, &apiError{status: 409, code: "in_use", message: "the memory in use cannot be deleted"})
		return
	}
	if err := os.RemoveAll(dir); err != nil {
		writeErr(w, &apiError{status: 500, code: "internal", message: err.Error()})
		return
	}
	w.WriteHeader(204)
}

var keyRE = regexp.MustCompile(`^r[0-9.e+-]+-[0-9.e+-]+-i[0-9a-f]{8}$`)

func validKey(k string) bool { return keyRE.MatchString(k) }

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func dirSize(dir string) int64 {
	var n int64
	filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			n += info.Size()
		}
		return nil
	})
	return n
}

func (a *apiError) Error() string { return a.message }
