package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"sync"
	"time"
)

// Options configure the engine process.
type Options struct {
	MemoryDir    string // memories: <dir>/<stream>/<analysis>/<key>/
	StateFile    string // the configuration last given; "" keeps none
	FFmpeg       string
	AllowPrivate bool     // accept stream addresses on private networks (intranet, tests)
	AllowFiles   bool     // accept file paths as inputs (development, tests)
	AllowHosts   []string // hosts (or host:port) accepted although private, e.g. a test server
	Limits       Limits
	Version      string
}

// Engine holds the streams and their analyses.
type Engine struct {
	opts     Options
	instance string
	started  time.Time
	resumed  bool
	policy   *addressPolicy
	ctx      context.Context
	cancel   context.CancelFunc
	probes   *probeGate
	meter    *meter

	mu      sync.Mutex // registry; lock order: e.mu, then stream.mu, then run.mu
	streams map[string]*stream
	saving  bool

	gmu    sync.Mutex // groups; taken after e.mu, never while holding a group's lock
	groups map[string]*group

	wmu   sync.Mutex
	watch map[chan change]struct{}

	loops sync.WaitGroup
}

type change struct {
	typ  string
	data any
}

// New starts an engine and resumes the configuration saved in
// opts.StateFile, if any.
func New(opts Options) (*Engine, error) {
	if opts.FFmpeg == "" {
		opts.FFmpeg = "ffmpeg"
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits
	}
	if opts.Version == "" {
		opts.Version = version()
	}
	if opts.MemoryDir == "" {
		return nil, errors.New("engine: a memory directory is needed")
	}
	b := make([]byte, 4)
	rand.Read(b)
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		opts: opts, instance: hex.EncodeToString(b), started: time.Now().UTC(),
		policy: newAddressPolicy(opts.AllowPrivate, opts.AllowFiles, opts.AllowHosts...),
		ctx:    ctx, cancel: cancel, probes: newProbeGate(),
		streams: map[string]*stream{}, groups: map[string]*group{}, watch: map[chan change]struct{}{},
	}
	e.meter = newMeter(e)
	if err := e.resume(); err != nil {
		cancel()
		return nil, err
	}
	e.loops.Add(3)
	go e.statusLoop()
	go e.usageLoop()
	go e.groupLoop()
	return e, nil
}

// Close stops every stream and analysis and saves the memories. The saved
// configuration stays, so the next start resumes it.
func (e *Engine) Close() {
	e.mu.Lock()
	streams := make([]*stream, 0, len(e.streams))
	for _, s := range e.streams {
		streams = append(streams, s)
	}
	e.mu.Unlock()
	for _, s := range streams {
		s.close()
	}
	e.cancel()
	e.loops.Wait()
	for _, g := range e.groupList() {
		g.mu.Lock()
		g.hist.close()
		g.mu.Unlock()
	}
}

func version() string {
	v := "0.1.0"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				v += "+" + s.Value[:7]
			}
		}
	}
	return v
}

// --- changes ---

func (e *Engine) addWatcher() chan change {
	ch := make(chan change, 256)
	e.wmu.Lock()
	e.watch[ch] = struct{}{}
	e.wmu.Unlock()
	return ch
}

func (e *Engine) removeWatcher(ch chan change) {
	e.wmu.Lock()
	delete(e.watch, ch)
	e.wmu.Unlock()
}

func (e *Engine) notify(typ string, data any) {
	e.wmu.Lock()
	defer e.wmu.Unlock()
	for ch := range e.watch {
		select {
		case ch <- change{typ, data}:
		default:
		}
	}
}

func (e *Engine) notifyAnalysis(r *run) {
	r.mu.Lock()
	key, failed := r.key, r.failed
	r.mu.Unlock()
	if failed != nil && failed.Code == "deleted" {
		return
	}
	d := map[string]any{"stream": r.s.id, "analysis": r.id, "state": r.state(), "memory_key": key}
	if failed != nil {
		d["last_error"] = failed
	}
	e.notify("analysis.state", d)
}

func (e *Engine) hello() map[string]any {
	return map[string]any{"instance": e.instance, "api": map[string]int{"major": APIMajor, "minor": APIMinor}, "resumed": e.resumed}
}

// --- registry ---

func (e *Engine) stream(id string) *stream {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.streams[id]
}

func (e *Engine) streamIDs() []string {
	ids := make([]string, 0, len(e.streams))
	for id := range e.streams {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// putStream creates or changes a stream.
// While resuming, the address gets the checks that need no name lookup;
// the full check is made before every connection anyway.
func (e *Engine) putStream(ctx context.Context, id string, spec StreamSpec) (*stream, *apiError) {
	if err := e.checkStream(ctx, &spec, !e.saving); err != nil {
		return nil, err
	}
	view, _ := checkView(spec.View, spec.SampleRate)
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.streams[id]
	if s == nil {
		for _, o := range e.streams {
			if o.input == spec.Input {
				return nil, &apiError{status: 409, code: "duplicate_input", message: "this address is stream " + o.id}
			}
		}
		if len(e.streams) >= e.opts.Limits.Streams {
			return nil, &apiError{status: 409, code: "limit_reached", message: fmt.Sprintf("the engine holds %d streams, its limit", len(e.streams))}
		}
		s = newStream(e, id, spec, view)
		e.streams[id] = s
	} else {
		if s.input != spec.Input || s.rate != spec.SampleRate {
			return nil, &apiError{status: 409, code: "immutable", message: "input and sample_rate cannot be changed; use a new stream"}
		}
		s.setSpec(spec, view)
	}
	e.saveLocked()
	e.notify("stream.state", s.viewOf(false))
	return s, nil
}

func (e *Engine) checkStream(ctx context.Context, spec *StreamSpec, resolve bool) *apiError {
	var fe []FieldError
	var p *Problem
	switch {
	case spec.Input == "":
		fe = append(fe, FieldError{"input", "required", "the stream's address is needed"})
	case resolve:
		// A host that does not resolve now is no reason to refuse: the
		// connection fails and is retried, and every attempt is checked.
		var err error
		if p, err = e.policy.check(ctx, spec.Input); errors.Is(err, errUnresolved) {
			p = nil
		}
	default:
		p = e.policy.checkStatic(spec.Input)
	}
	if p != nil {
		fe = append(fe, FieldError{"input", p.Code, p.Message})
	}
	if spec.SampleRate < 8000 || spec.SampleRate > e.opts.Limits.MaxSampleRate {
		fe = append(fe, FieldError{"sample_rate", "out_of_range", fmt.Sprintf("must be between 8000 and %d", e.opts.Limits.MaxSampleRate)})
	}
	if spec.Mode == "" {
		spec.Mode = OnDemand
	}
	if spec.Mode != OnDemand && spec.Mode != Continuous {
		fe = append(fe, FieldError{"mode", "invalid", "on_demand or continuous"})
	}
	if v := spec.IdleAfterS; v != nil && (*v < 1 || *v > 3600) {
		fe = append(fe, FieldError{"idle_after_s", "out_of_range", "must be between 1 and 3600"})
	}
	if v := spec.RetryMaxS; v != nil && (*v < 1 || *v > 3600) {
		fe = append(fe, FieldError{"retry_max_s", "out_of_range", "must be between 1 and 3600"})
	}
	_, vfe := checkView(spec.View, spec.SampleRate)
	fe = append(fe, vfe...)
	if fe != nil {
		return &apiError{status: 422, code: "invalid", message: "the stream was refused", errors: fe}
	}
	return nil
}

func (e *Engine) deleteStream(id string, purge bool) *apiError {
	e.mu.Lock()
	s := e.streams[id]
	if s == nil {
		e.mu.Unlock()
		return errNotFound("stream")
	}
	delete(e.streams, id)
	e.saveLocked()
	e.mu.Unlock()
	s.close()
	if purge {
		if err := os.RemoveAll(filepath.Join(e.opts.MemoryDir, id)); err != nil {
			log.Printf("stream %s: purge: %v", id, err)
		}
	}
	e.notify("stream.state", map[string]any{"id": id, "state": StateIdle, "deleted": true})
	return nil
}

// putAnalysis creates or changes an analysis of a stream.
func (e *Engine) putAnalysis(sid, aid string, spec AnalysisSpec) (*run, *apiError) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := e.streams[sid]
	if s == nil {
		return nil, errNotFound("stream")
	}
	c, errs, _ := checkAnalysis(s.rate, spec, e.opts.Limits)
	if len(errs) > 0 {
		return nil, &apiError{status: 422, code: "invalid", message: "the settings were refused", errors: errs}
	}
	r := s.getRun(aid)
	created := r == nil
	if created {
		s.mu.Lock()
		n := len(s.runs)
		s.mu.Unlock()
		if n >= e.opts.Limits.AnalysesPerStream {
			return nil, &apiError{status: 409, code: "limit_reached", message: fmt.Sprintf("this stream already has %d analyses", n)}
		}
		r = newRun(s, aid)
	}
	if err := r.apply(c); err != nil {
		log.Printf("analysis %s: %v", r.source(), err)
		if created {
			r.close()
		}
		return nil, &apiError{status: 500, code: "internal", message: "the analysis could not be started: " + err.Error()}
	}
	if created {
		s.addRun(r)
	}
	e.saveLocked()
	e.notifyAnalysis(r)
	r.emitStatus()
	return r, nil
}

func (e *Engine) deleteAnalysis(sid, aid string, purge bool) *apiError {
	e.mu.Lock()
	s := e.streams[sid]
	if s == nil {
		e.mu.Unlock()
		return errNotFound("stream")
	}
	r := s.removeRun(aid)
	if r == nil {
		e.mu.Unlock()
		return errNotFound("analysis")
	}
	e.saveLocked()
	e.mu.Unlock()
	r.close()
	if purge {
		if err := os.RemoveAll(filepath.Join(e.opts.MemoryDir, sid, aid)); err != nil {
			log.Printf("analysis %s: purge: %v", r.source(), err)
		}
	}
	e.notify("analysis.state", map[string]any{"stream": sid, "analysis": aid, "state": AnalysisIdle, "memory_key": "", "deleted": true})
	return nil
}

func (e *Engine) find(sid, aid string) (*stream, *run, *apiError) {
	s := e.stream(sid)
	if s == nil {
		return nil, nil, errNotFound("stream")
	}
	r := s.getRun(aid)
	if r == nil {
		return nil, nil, errNotFound("analysis")
	}
	return s, r, nil
}

// --- the saved configuration ---

type savedConfig struct {
	Version int           `json:"version"`
	Streams []savedStream `json:"streams"`
	Groups  []savedGroup  `json:"groups,omitempty"`
}

type savedGroup struct {
	ID   string    `json:"id"`
	Spec GroupSpec `json:"spec"`
}

type savedStream struct {
	ID       string          `json:"id"`
	Spec     StreamSpec      `json:"spec"`
	Analyses []savedAnalysis `json:"analyses"`
}

type savedAnalysis struct {
	ID   string       `json:"id"`
	Spec AnalysisSpec `json:"spec"`
}

// saveConfig is saveLocked for callers that do not hold e.mu.
func (e *Engine) saveConfig() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.saveLocked()
}

// saveLocked writes the configuration as given, so that a restart resumes
// it. Nothing secret is in it. e.mu must be held.
func (e *Engine) saveLocked() {
	if e.opts.StateFile == "" || e.saving {
		return
	}
	cfg := savedConfig{Version: 1, Streams: []savedStream{}}
	for _, id := range e.streamIDs() {
		s := e.streams[id]
		s.mu.Lock()
		ss := savedStream{ID: id, Spec: s.spec, Analyses: []savedAnalysis{}}
		runs := s.runList()
		s.mu.Unlock()
		for _, r := range runs {
			r.mu.Lock()
			ss.Analyses = append(ss.Analyses, savedAnalysis{ID: r.id, Spec: r.c.spec})
			r.mu.Unlock()
		}
		cfg.Streams = append(cfg.Streams, ss)
	}
	for _, g := range e.groupList() {
		g.mu.Lock()
		cfg.Groups = append(cfg.Groups, savedGroup{ID: g.id, Spec: g.spec})
		g.mu.Unlock()
	}
	if err := writeJSONFile(e.opts.StateFile, cfg); err != nil {
		log.Printf("engine: saving the configuration: %v", err)
	}
}

// resume recreates what the saved configuration holds.
func (e *Engine) resume() error {
	if e.opts.StateFile == "" {
		return nil
	}
	b, err := os.ReadFile(e.opts.StateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	var cfg savedConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("engine: %s: %w", e.opts.StateFile, err)
	}
	e.saving = true // nothing to write while resuming
	defer func() { e.saving = false }()
	n := 0
	for _, ss := range cfg.Streams {
		if _, err := e.putStream(e.ctx, ss.ID, ss.Spec); err != nil {
			log.Printf("engine: resume: stream %s: %s", ss.ID, err.message)
			continue
		}
		for _, sa := range ss.Analyses {
			if _, err := e.putAnalysis(ss.ID, sa.ID, sa.Spec); err != nil {
				log.Printf("engine: resume: analysis %s/%s: %s", ss.ID, sa.ID, err.message)
				continue
			}
			n++
		}
	}
	for _, sg := range cfg.Groups {
		if _, err := e.putGroup(sg.ID, sg.Spec); err != nil {
			log.Printf("engine: resume: group %s: %s", sg.ID, err.message)
		}
	}
	e.resumed = true
	log.Printf("engine: resumed %d streams and %d analyses from %s", len(cfg.Streams), n, e.opts.StateFile)
	return nil
}

// --- periodic reports ---

// statusLoop sends every analysis's system.status every 5 seconds.
func (e *Engine) statusLoop() {
	defer e.loops.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
		}
		for _, r := range e.allRuns() {
			r.emitStatus()
		}
	}
}

// usageLoop measures what everything costs every 10 seconds and reports it.
func (e *Engine) usageLoop() {
	defer e.loops.Done()
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-t.C:
		}
		e.notify("usage", e.meter.measure())
	}
}

func (e *Engine) allRuns() []*run {
	e.mu.Lock()
	streams := make([]*stream, 0, len(e.streams))
	for _, id := range e.streamIDs() {
		streams = append(streams, e.streams[id])
	}
	e.mu.Unlock()
	var out []*run
	for _, s := range streams {
		s.mu.Lock()
		out = append(out, s.runList()...)
		s.mu.Unlock()
	}
	return out
}
