package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
)

// reserve counts one more against an engine-wide limit, or reports that
// the limit is reached.
func reserve(n *atomic.Int64, limit int) bool {
	for {
		v := n.Load()
		if v >= int64(limit) {
			return false
		}
		if n.CompareAndSwap(v, v+1) {
			return true
		}
	}
}

// A caller is one program configuring the engine, on a socket of its own:
// the Workbench, or internal work such as the premise test. Each has its
// own namespace (streams, analyses, groups, events, saved configuration,
// memories) and sees nothing of the others, except that a source they
// share is read once. Limits are the engine's, across all callers.
type caller struct {
	e         *Engine
	name      string
	memDir    string // <memory-dir>/<caller>
	stateFile string // "" keeps none
	resumed   bool

	mu      sync.Mutex // registry; lock order: c.mu, e.smu, source, stream, run
	streams map[string]*stream
	saving  bool

	gmu    sync.Mutex // groups; taken after c.mu, never while holding a group's lock
	groups map[string]*group

	wmu   sync.Mutex
	watch map[chan change]struct{}
}

type change struct {
	typ  string
	data any
}

func newCaller(e *Engine, name, stateFile string) *caller {
	return &caller{
		e: e, name: name, memDir: filepath.Join(e.opts.MemoryDir, name), stateFile: stateFile,
		streams: map[string]*stream{}, groups: map[string]*group{}, watch: map[chan change]struct{}{},
	}
}

// --- changes ---

func (c *caller) addWatcher() chan change {
	ch := make(chan change, 256)
	c.wmu.Lock()
	c.watch[ch] = struct{}{}
	c.wmu.Unlock()
	return ch
}

func (c *caller) removeWatcher(ch chan change) {
	c.wmu.Lock()
	delete(c.watch, ch)
	c.wmu.Unlock()
}

func (c *caller) notify(typ string, data any) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	for ch := range c.watch {
		select {
		case ch <- change{typ, data}:
		default:
		}
	}
}

func (c *caller) notifyAnalysis(r *run) {
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
	c.notify("analysis.state", d)
}

func (c *caller) hello() map[string]any {
	return map[string]any{"instance": c.e.instance, "api": map[string]int{"major": APIMajor, "minor": APIMinor}, "resumed": c.resumed}
}

// --- registry ---

func (c *caller) stream(id string) *stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[id]
}

func (c *caller) streamIDs() []string {
	ids := make([]string, 0, len(c.streams))
	for id := range c.streams {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c *caller) streamList() []*stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*stream, 0, len(c.streams))
	for _, id := range c.streamIDs() {
		out = append(out, c.streams[id])
	}
	return out
}

// putStream creates or changes a stream. While resuming, the address gets
// the checks that need no name lookup; the full check is made before every
// connection anyway.
func (c *caller) putStream(ctx context.Context, id string, spec StreamSpec) (*stream, *apiError) {
	if err := c.e.checkStream(ctx, &spec, !c.saving); err != nil {
		return nil, err
	}
	view, _ := checkView(spec.View, spec.SampleRate)
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.streams[id]
	if s == nil {
		for _, o := range c.streams {
			if o.input == spec.Input {
				return nil, &apiError{status: 409, code: "duplicate_input", message: "this address is stream " + o.id}
			}
		}
		if !reserve(&c.e.nstreams, c.e.opts.Limits.Streams) {
			return nil, &apiError{status: 409, code: "limit_reached", message: fmt.Sprintf("the engine holds %d streams, its limit", c.e.opts.Limits.Streams)}
		}
		src, aerr := c.e.acquireSource(spec.Input, spec.SampleRate)
		if aerr != nil {
			c.e.nstreams.Add(-1)
			return nil, aerr
		}
		s = newStream(c, id, spec, view, src)
		src.attach(s)
		c.streams[id] = s
	} else {
		if s.input != spec.Input || s.rate != spec.SampleRate {
			return nil, &apiError{status: 409, code: "immutable", message: "input and sample_rate cannot be changed; use a new stream"}
		}
		s.setSpec(spec, view)
	}
	c.saveLocked()
	c.notify("stream.state", s.viewOf(false))
	return s, nil
}

func (c *caller) deleteStream(id string, purge bool) *apiError {
	c.mu.Lock()
	s := c.streams[id]
	if s == nil {
		c.mu.Unlock()
		return errNotFound("stream")
	}
	delete(c.streams, id)
	c.e.nstreams.Add(-1)
	c.saveLocked()
	c.mu.Unlock()
	s.close()
	c.e.releaseSource(s)
	if purge {
		if err := os.RemoveAll(filepath.Join(c.memDir, id)); err != nil {
			log.Printf("stream %s/%s: purge: %v", c.name, id, err)
		}
	}
	c.notify("stream.state", map[string]any{"id": id, "state": StateIdle, "deleted": true})
	return nil
}

// putAnalysis creates or changes an analysis of a stream.
func (c *caller) putAnalysis(sid, aid string, spec AnalysisSpec) (*run, *apiError) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.streams[sid]
	if s == nil {
		return nil, errNotFound("stream")
	}
	ch, errs, _ := checkAnalysis(s.rate, spec, c.e.opts.Limits)
	if len(errs) > 0 {
		return nil, &apiError{status: 422, code: "invalid", message: "the settings were refused", errors: errs}
	}
	r := s.getRun(aid)
	created := r == nil
	if created {
		s.mu.Lock()
		n := len(s.runs)
		s.mu.Unlock()
		if n >= c.e.opts.Limits.AnalysesPerStream {
			return nil, &apiError{status: 409, code: "limit_reached", message: fmt.Sprintf("this stream already has %d analyses", n)}
		}
		r = newRun(s, aid)
	}
	if err := r.apply(ch); err != nil {
		log.Printf("analysis %s: %v", r.source(), err)
		if created {
			r.close()
		}
		return nil, &apiError{status: 500, code: "internal", message: "the analysis could not be started: " + err.Error()}
	}
	if created {
		s.addRun(r)
	}
	c.saveLocked()
	c.notifyAnalysis(r)
	r.emitStatus()
	return r, nil
}

func (c *caller) deleteAnalysis(sid, aid string, purge bool) *apiError {
	c.mu.Lock()
	s := c.streams[sid]
	if s == nil {
		c.mu.Unlock()
		return errNotFound("stream")
	}
	r := s.removeRun(aid)
	if r == nil {
		c.mu.Unlock()
		return errNotFound("analysis")
	}
	c.saveLocked()
	c.mu.Unlock()
	r.close()
	if purge {
		if err := os.RemoveAll(filepath.Join(c.memDir, sid, aid)); err != nil {
			log.Printf("analysis %s: purge: %v", r.source(), err)
		}
	}
	c.notify("analysis.state", map[string]any{"stream": sid, "analysis": aid, "state": AnalysisIdle, "memory_key": "", "deleted": true})
	return nil
}

func (c *caller) find(sid, aid string) (*stream, *run, *apiError) {
	s := c.stream(sid)
	if s == nil {
		return nil, nil, errNotFound("stream")
	}
	r := s.getRun(aid)
	if r == nil {
		return nil, nil, errNotFound("analysis")
	}
	return s, r, nil
}

func (c *caller) allRuns() []*run {
	var out []*run
	for _, s := range c.streamList() {
		s.mu.Lock()
		out = append(out, s.runList()...)
		s.mu.Unlock()
	}
	return out
}

// close stops the caller's streams and analyses and saves the memories.
func (c *caller) close() {
	for _, s := range c.streamList() {
		s.close()
		c.e.releaseSource(s)
	}
	for _, g := range c.groupList() {
		g.mu.Lock()
		g.hist.close()
		g.mu.Unlock()
	}
}

// --- the saved configuration ---

type savedConfig struct {
	Version int           `json:"version"`
	Streams []savedStream `json:"streams"`
	Groups  []savedGroup  `json:"groups,omitempty"`
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

type savedGroup struct {
	ID   string    `json:"id"`
	Spec GroupSpec `json:"spec"`
}

// saveConfig is saveLocked for callers that do not hold c.mu.
func (c *caller) saveConfig() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.saveLocked()
}

// saveLocked writes the caller's configuration as given, so that a restart
// resumes it. Nothing secret is in it. c.mu must be held.
func (c *caller) saveLocked() {
	if c.stateFile == "" || c.saving {
		return
	}
	cfg := savedConfig{Version: 1, Streams: []savedStream{}}
	for _, id := range c.streamIDs() {
		s := c.streams[id]
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
	for _, g := range c.groupList() {
		g.mu.Lock()
		cfg.Groups = append(cfg.Groups, savedGroup{ID: g.id, Spec: g.spec})
		g.mu.Unlock()
	}
	if err := writeJSONFile(c.stateFile, cfg); err != nil {
		log.Printf("engine: caller %s: saving the configuration: %v", c.name, err)
	}
}

// resume recreates what the caller's saved configuration holds.
func (c *caller) resume() error {
	if c.stateFile == "" {
		return nil
	}
	b, err := os.ReadFile(c.stateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	var cfg savedConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("engine: %s: %w", c.stateFile, err)
	}
	c.saving = true // nothing to write while resuming
	defer func() { c.saving = false }()
	n := 0
	for _, ss := range cfg.Streams {
		if _, err := c.putStream(c.e.ctx, ss.ID, ss.Spec); err != nil {
			log.Printf("engine: caller %s: resume: stream %s: %s", c.name, ss.ID, err.message)
			continue
		}
		for _, sa := range ss.Analyses {
			if _, err := c.putAnalysis(ss.ID, sa.ID, sa.Spec); err != nil {
				log.Printf("engine: caller %s: resume: analysis %s/%s: %s", c.name, ss.ID, sa.ID, err.message)
				continue
			}
			n++
		}
	}
	for _, sg := range cfg.Groups {
		if _, err := c.putGroup(sg.ID, sg.Spec); err != nil {
			log.Printf("engine: caller %s: resume: group %s: %s", c.name, sg.ID, err.message)
		}
	}
	c.resumed = true
	log.Printf("engine: caller %s: resumed %d streams, %d analyses and %d groups from %s", c.name, len(cfg.Streams), n, len(cfg.Groups), c.stateFile)
	return nil
}
