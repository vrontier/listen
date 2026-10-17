// Package standin is a stand-in for the engine process: it serves the
// control API of docs/control-api.md from memory and makes up plausible
// analysis data, so that the Workbench can be built and tested before the
// real engine exists. It does not connect to any stream.
//
// Its validation follows the rules the spec states for the engine. That is
// the stand-in's business; the Workbench itself keeps no copy of them.
package standin

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	apiMajor = 1
	apiMinor = 1
	version  = "standin-0.1.0"
)

// Options configure the stand-in.
type Options struct {
	// AllowPrivate accepts stream addresses on private networks, for an
	// installation inside one (the real engine would need the same switch).
	AllowPrivate bool
	// Tick is the interval of the made-up decode (default 100 ms).
	Tick time.Duration
}

// Engine is the stand-in. It is an http.Handler.
type Engine struct {
	opts     Options
	instance string
	started  time.Time
	mux      *http.ServeMux

	mu      sync.Mutex
	streams map[string]*stream
	groups  map[string]*group
	watch   map[chan event]struct{} // GET /v1/events subscribers
	stop    chan struct{}
}

type event struct {
	typ  string
	data any
}

var limits = map[string]int{
	"streams":                    40,
	"analyses_per_stream":        6,
	"rules_per_analysis":         16,
	"subscriptions_per_analysis": 4,
	"max_sample_rate":            48000,
	"max_bitrate_kbps":           512,
	"groups":                     20,
	"members_per_group":          8,
}

// New starts a stand-in. Close stops its clock.
func New(opts Options) *Engine {
	if opts.Tick == 0 {
		opts.Tick = 100 * time.Millisecond
	}
	b := make([]byte, 4)
	rand.Read(b)
	e := &Engine{
		opts: opts, instance: hex.EncodeToString(b), started: time.Now().UTC(),
		streams: map[string]*stream{}, groups: map[string]*group{}, watch: map[chan event]struct{}{}, stop: make(chan struct{}),
	}
	e.routes()
	go e.clock()
	return e
}

func (e *Engine) Close() { close(e.stop) }

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) { e.mux.ServeHTTP(w, r) }

func (e *Engine) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/engine", e.getEngine)
	m.HandleFunc("GET /v1/events", e.getChanges)
	m.HandleFunc("GET /v1/streams", e.listStreams)
	m.HandleFunc("GET /v1/streams/{s}", e.getStream)
	m.HandleFunc("PUT /v1/streams/{s}", e.putStream)
	m.HandleFunc("DELETE /v1/streams/{s}", e.deleteStream)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}", e.getAnalysis)
	m.HandleFunc("PUT /v1/streams/{s}/analyses/{a}", e.putAnalysis)
	m.HandleFunc("DELETE /v1/streams/{s}/analyses/{a}", e.deleteAnalysis)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/events", e.analysisEvents)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/state", e.analysisState)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/motifs", e.analysisMotifs)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/motifs/{m}", e.analysisMotif)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/history/events", e.historyEvents)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/history/features", e.historyFeatures)
	m.HandleFunc("GET /v1/streams/{s}/analyses/{a}/memories", e.listMemories)
	m.HandleFunc("DELETE /v1/streams/{s}/analyses/{a}/memories/{key}", e.deleteMemory)
	m.HandleFunc("POST /v1/validate/analysis", e.validate)
	m.HandleFunc("POST /v1/probes", e.probe)
	e.groupRoutes(m)
	e.mux = m
}

// --- helpers ---

type apiError struct {
	status  int
	code    string
	message string
	errors  []fieldError
}

func (a *apiError) Error() string { return a.message }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var a *apiError
	if !errors.As(err, &a) {
		a = &apiError{status: 500, code: "internal", message: err.Error()}
	}
	errs := a.errors
	if errs == nil {
		errs = []fieldError{}
	}
	writeJSON(w, a.status, map[string]any{"error": map[string]any{"code": a.code, "message": a.message, "errors": errs}})
}

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func checkID(id string) error {
	if !idRE.MatchString(id) {
		return &apiError{status: 400, code: "bad_id", message: "identifiers are 1 to 63 lower-case letters, digits and hyphens"}
	}
	return nil
}

// decode reads a JSON body and refuses fields it does not know.
func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			return &apiError{status: 400, code: "unknown_field", message: err.Error()}
		}
		return &apiError{status: 400, code: "bad_request", message: err.Error()}
	}
	return nil
}

func (e *Engine) notify(typ string, data any) {
	for ch := range e.watch {
		select {
		case ch <- event{typ, data}:
		default:
		}
	}
}

// --- engine and changes ---

func (e *Engine) getEngine(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	counts := map[string]int{"streams": len(e.streams), "groups": len(e.groups)}
	var total usage
	for _, s := range e.streams {
		counts["analyses"] += len(s.analyses)
		if s.state == "connected" {
			counts["connected"]++
		}
		u := s.usage()
		total.CPU += u.CPU
		total.Mem += u.Mem
		total.In += u.In
		for _, a := range s.analyses {
			au := a.usage(s)
			total.CPU += au.CPU
			total.Mem += au.Mem
		}
	}
	writeJSON(w, 200, map[string]any{
		"api":            map[string]int{"major": apiMajor, "minor": apiMinor},
		"engine_version": version,
		"instance":       e.instance,
		"started":        e.started,
		"limits":         limits,
		"capabilities": map[string]any{
			"rule_kinds": []string{"line", "comb"}, "detectors": []any{},
			"file_inputs": false, "address_check": "initial",
			"station_time": false, "group_analyses": groupAnalyses,
		},
		"counts": counts,
		"host":   hostInfo(),
		"usage":  map[string]float64{"cpu_percent": round(total.CPU+0.8, 1), "memory_mb": round(total.Mem+18, 0), "in_kbps": total.In},
	})
}

func (e *Engine) getChanges(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, &apiError{status: 500, code: "internal", message: "no streaming"})
		return
	}
	ch := make(chan event, 64)
	e.mu.Lock()
	e.watch[ch] = struct{}{}
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.watch, ch)
		e.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	writeSSE(w, "", "engine.hello", map[string]any{"instance": e.instance, "api": map[string]int{"major": apiMajor, "minor": apiMinor}, "resumed": false})
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-e.stop:
			return
		case ev := <-ch:
			writeSSE(w, "", ev.typ, ev.data)
			fl.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		}
	}
}

func writeSSE(w io.Writer, id, typ string, data any) {
	var b []byte
	switch d := data.(type) {
	case []byte:
		b = d
	default:
		b, _ = json.Marshal(d)
	}
	if id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
}

// --- streams ---

type streamSpec struct {
	Input      string    `json:"input"`
	SampleRate int       `json:"sample_rate"`
	Mode       string    `json:"mode"`
	IdleAfterS *int      `json:"idle_after_s"`
	RetryMaxS  *int      `json:"retry_max_s"`
	View       *viewSpec `json:"view"`
}

type viewSpec struct {
	Scale string   `json:"scale"`
	MinHz *float64 `json:"min_hz"`
	MaxHz *float64 `json:"max_hz"`
	Bands *int     `json:"bands"`
}

// checkView checks a stream's view against its sample rate and fills in
// the defaults: linear from 0, log from 20 Hz, up to Nyquist, 240 bands.
func checkView(v *viewSpec, rate int) (*viewEff, []fieldError) {
	if v == nil {
		return nil, nil
	}
	var fe []fieldError
	nyq := float64(rate) / 2
	eff := &viewEff{Scale: v.Scale, MaxHz: nyq, Bands: 240}
	switch v.Scale {
	case "linear":
	case "log":
		eff.MinHz = 20
	default:
		fe = append(fe, fieldError{Path: "view.scale", Code: "invalid", Message: "linear or log"})
	}
	if v.MinHz != nil {
		eff.MinHz = *v.MinHz
	}
	if v.MaxHz != nil {
		eff.MaxHz = *v.MaxHz
	}
	if v.Bands != nil {
		eff.Bands = *v.Bands
	}
	if eff.MinHz < 0 || v.Scale == "log" && eff.MinHz <= 0 {
		fe = append(fe, fieldError{Path: "view.min_hz", Code: "out_of_range", Message: "must be at least 0 Hz, and above 0 Hz on a log scale"})
	}
	if eff.MaxHz > nyq {
		fe = append(fe, fieldError{Path: "view.max_hz", Code: "out_of_range", Message: fmt.Sprintf("must be at most %g Hz, half the sample rate", nyq)})
	}
	if eff.MaxHz <= eff.MinHz {
		fe = append(fe, fieldError{Path: "view.max_hz", Code: "out_of_range", Message: "must be above min_hz"})
	}
	if eff.Bands < 16 || eff.Bands > 1024 {
		fe = append(fe, fieldError{Path: "view.bands", Code: "out_of_range", Message: "must be between 16 and 1024"})
	}
	return eff, fe
}

func (e *Engine) listStreams(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := make([]string, 0, len(e.streams))
	for id := range e.streams {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, e.streams[id].view(true))
	}
	writeJSON(w, 200, map[string]any{"streams": out})
}

func (e *Engine) getStream(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.streams[r.PathValue("s")]
	if s == nil {
		writeErr(w, &apiError{status: 404, code: "not_found", message: "no such stream"})
		return
	}
	writeJSON(w, 200, s.view(true))
}

func (e *Engine) putStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("s")
	if err := checkID(id); err != nil {
		writeErr(w, err)
		return
	}
	var spec streamSpec
	if err := decode(r, &spec); err != nil {
		writeErr(w, err)
		return
	}
	var fe []fieldError
	if p := addressProblem(spec.Input, e.opts.AllowPrivate); p != nil {
		fe = append(fe, fieldError{Path: "input", Code: p.Code, Message: p.Message})
	}
	if spec.SampleRate < 8000 || spec.SampleRate > limits["max_sample_rate"] {
		fe = append(fe, fieldError{Path: "sample_rate", Code: "out_of_range", Message: fmt.Sprintf("must be between 8000 and %d", limits["max_sample_rate"])})
	}
	if spec.Mode == "" {
		spec.Mode = "on_demand"
	}
	if spec.Mode != "on_demand" && spec.Mode != "continuous" {
		fe = append(fe, fieldError{Path: "mode", Code: "invalid", Message: "on_demand or continuous"})
	}
	idle, retry := 30, 300
	if spec.IdleAfterS != nil {
		idle = *spec.IdleAfterS
	}
	if spec.RetryMaxS != nil {
		retry = *spec.RetryMaxS
	}
	if idle < 1 || idle > 3600 {
		fe = append(fe, fieldError{Path: "idle_after_s", Code: "out_of_range", Message: "must be between 1 and 3600"})
	}
	if retry < 1 || retry > 3600 {
		fe = append(fe, fieldError{Path: "retry_max_s", Code: "out_of_range", Message: "must be between 1 and 3600"})
	}
	view, vfe := checkView(spec.View, spec.SampleRate)
	fe = append(fe, vfe...)
	if fe != nil {
		writeErr(w, &apiError{status: 422, code: "invalid", message: "the stream was refused", errors: fe})
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.streams[id]
	if s == nil {
		for _, o := range e.streams {
			if o.input == spec.Input {
				writeErr(w, &apiError{status: 409, code: "duplicate_input", message: "this address is stream " + o.id})
				return
			}
		}
		if len(e.streams) >= limits["streams"] {
			writeErr(w, &apiError{status: 409, code: "limit_reached", message: "the engine holds as many streams as it may"})
			return
		}
		s = newStream(e, id, spec.Input, spec.SampleRate)
		e.streams[id] = s
	} else if s.input != spec.Input || s.sampleRate != spec.SampleRate {
		writeErr(w, &apiError{status: 409, code: "immutable", message: "input and sample_rate cannot be changed; use a new stream"})
		return
	}
	s.mode, s.idleAfter, s.retryMax = spec.Mode, idle, retry
	s.viewEff = view // changeable on a running stream
	s.update(time.Now())
	writeJSON(w, 200, s.view(true))
}

func (e *Engine) deleteStream(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.streams[r.PathValue("s")]
	if s == nil {
		writeErr(w, &apiError{status: 404, code: "not_found", message: "no such stream"})
		return
	}
	for _, a := range s.analyses {
		a.closeSubscribers()
	}
	delete(e.streams, s.id)
	e.notify("stream.state", map[string]any{"id": s.id, "state": "idle", "deleted": true})
	w.WriteHeader(204)
}

// --- probe ---

type probeReq struct {
	Input   string `json:"input"`
	ListenS int    `json:"listen_s"`
}

type problem struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Engine) probe(w http.ResponseWriter, r *http.Request) {
	var req probeReq
	if err := decode(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	res := map[string]any{
		"ok": false, "exists": nil, "content_type": nil, "codec": "", "sample_rate": 0, "channels": 0,
		"bitrate_kbps": 0, "level_db": 0.0, "silent": false, "problems": []problem{},
	}
	e.mu.Lock()
	for _, s := range e.streams {
		if s.input == req.Input {
			res["exists"] = s.id
			res["problems"] = []problem{{"exists", "this address is already a stream"}}
			e.mu.Unlock()
			writeJSON(w, 200, res)
			return
		}
	}
	e.mu.Unlock()
	if p := addressProblem(req.Input, e.opts.AllowPrivate); p != nil {
		res["problems"] = []problem{*p}
		writeJSON(w, 200, res)
		return
	}
	// Made-up answers, chosen by words in the address so that tests and
	// demonstrations can reach every case.
	low := strings.ToLower(req.Input)
	switch {
	case strings.Contains(low, "unreachable"):
		res["problems"] = []problem{{"no_answer", "the server did not answer within 10 seconds"}}
	case strings.Contains(low, "notaudio"):
		res["problems"] = []problem{{"not_audio", "the address delivers text/html, not audio"}}
	case strings.Contains(low, "loud"):
		res["problems"] = []problem{{"bitrate", "1411 kbit/s is above the limit of 512 kbit/s"}}
	default:
		res["ok"] = true
		res["codec"], res["sample_rate"], res["channels"], res["bitrate_kbps"], res["level_db"] = "mp3", 44100, 2, 128, -24.5
		if strings.Contains(low, ".ogg") {
			res["codec"], res["sample_rate"], res["channels"], res["bitrate_kbps"] = "vorbis", 32000, 1, 96
		}
		if strings.Contains(low, "silent") {
			res["ok"], res["silent"], res["level_db"] = false, true, -96.0
			res["problems"] = []problem{{"silent", "only silence arrived in 10 seconds"}}
		}
	}
	writeJSON(w, 200, res)
}

var privateRE = regexp.MustCompile(`^(localhost|127\.|10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|169\.254\.|\[?::1\]?|\[?f[cd][0-9a-f]{2}:)`)

func addressProblem(input string, allowPrivate bool) *problem {
	scheme, rest, ok := strings.Cut(input, "://")
	if !ok || scheme != "http" && scheme != "https" {
		return &problem{"protocol_not_allowed", "only http and https addresses are allowed"}
	}
	host := rest
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if host == "" {
		return &problem{"bad_address", "the address has no host"}
	}
	if !allowPrivate && (privateRE.MatchString(host) || strings.HasSuffix(strings.Split(host, ":")[0], ".local")) {
		return &problem{"address_not_allowed", "addresses on loopback or private networks are not allowed"}
	}
	return nil
}
