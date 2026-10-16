package engine

import (
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrontier/listen/listener/internal/analysis"
)

// Stream states.
const (
	StateIdle         = "idle"
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateBuffering    = "buffering"
	StateReconnecting = "reconnecting"
	StateRefused      = "refused"
)

func isURL(in string) bool { return strings.Contains(in, "://") }

// stream is a caller's stream: its settings, view and analyses, on a
// source that it may share with streams of other callers.
type stream struct {
	c     *caller
	id    string
	input string
	rate  int
	src   *sourceConn

	mu        sync.Mutex
	spec      StreamSpec // as given
	mode      string
	idleAfter time.Duration
	retryMax  time.Duration
	view      *View
	runs      map[string]*run
	lastUsage Usage

	vmu      sync.Mutex
	vb       *analysis.ViewBands // the stream's own view, or nil
	feedList atomic.Pointer[[]*run]
}

func newStream(c *caller, id string, spec StreamSpec, view *View, src *sourceConn) *stream {
	s := &stream{c: c, id: id, input: spec.Input, rate: spec.SampleRate, src: src, runs: map[string]*run{}}
	s.feedList.Store(&[]*run{})
	s.setSpec(spec, view)
	return s
}

// setSpec applies the changeable fields. They take effect without
// interrupting the connection; retry_max_s at the next connection.
func (s *stream) setSpec(spec StreamSpec, view *View) {
	s.mu.Lock()
	s.spec = spec
	s.mode = spec.Mode
	if s.mode == "" {
		s.mode = OnDemand
	}
	s.idleAfter, s.retryMax = 30*time.Second, 300*time.Second
	if spec.IdleAfterS != nil {
		s.idleAfter = time.Duration(*spec.IdleAfterS) * time.Second
	}
	if spec.RetryMaxS != nil {
		s.retryMax = time.Duration(*spec.RetryMaxS) * time.Second
	}
	changed := !sameView(s.view, view)
	s.view = view
	s.mu.Unlock()
	if changed {
		s.vmu.Lock()
		s.vb = nil
		if v := view.options(); v != nil {
			s.vb = analysis.NewViewBands(*v, s.rate)
		}
		s.vmu.Unlock()
	}
}

func sameView(a, b *View) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// take hands a frame of the source to the stream's analyses, with the
// stream's view. It runs on the source's decode.
func (s *stream) take(fr *analysis.Frame) {
	s.vmu.Lock()
	if s.vb != nil {
		fr = s.vb.Apply(fr)
	}
	s.vmu.Unlock()
	for _, r := range *s.feedList.Load() {
		r.push(fr)
	}
}

// runList is the analyses in identifier order; s.mu must be held.
func (s *stream) runList() []*run {
	out := make([]*run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (s *stream) addRun(r *run) {
	s.mu.Lock()
	s.runs[r.id] = r
	l := s.runList()
	s.mu.Unlock()
	s.feedList.Store(&l)
}

func (s *stream) removeRun(id string) *run {
	s.mu.Lock()
	r := s.runs[id]
	delete(s.runs, id)
	l := s.runList()
	s.mu.Unlock()
	s.feedList.Store(&l)
	return r
}

func (s *stream) getRun(id string) *run {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[id]
}

func (s *stream) connected() bool { return s.src.connected() }

func (s *stream) status() (state string, reconnects int) {
	st, _, _, rc := s.src.status()
	return st, rc
}

func (s *stream) setDelay(d time.Duration) { s.src.setDelay(d) }

// wanted: this stream wants its source connected.
func (s *stream) wanted() bool {
	s.mu.Lock()
	runs, mode := s.runList(), s.mode
	s.mu.Unlock()
	if len(runs) == 0 {
		return false
	}
	if mode == Continuous {
		return true
	}
	for _, r := range runs {
		if r.subscribers() > 0 {
			return true
		}
	}
	return s.c.groupWants(s.id) // a running group keeps its members' streams connected
}

// stateChanged reports the source's new state as this stream's.
func (s *stream) stateChanged() {
	s.mu.Lock()
	runs := s.runList()
	s.mu.Unlock()
	s.c.notify("stream.state", s.viewOf(false))
	for _, r := range runs {
		r.emitStatus()
		s.c.notifyAnalysis(r)
	}
}

// close stops the stream's analyses; the engine detaches it from its
// source.
func (s *stream) close() {
	s.mu.Lock()
	runs := s.runList()
	s.mu.Unlock()
	for _, r := range runs {
		r.close()
	}
}

func (s *stream) viewOf(withInput bool) StreamView {
	state, since, lastErr, reconnects := s.src.status()
	shared := s.src.shared()
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.runs))
	for id := range s.runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	v := StreamView{
		ID: s.id, SampleRate: s.rate, Mode: s.mode,
		IdleAfterS: int(s.idleAfter / time.Second), RetryMaxS: int(s.retryMax / time.Second),
		State: state, Since: since, LastError: lastErr, Reconnects: reconnects,
		DelayMs: int(s.src.delay.Load()), Analyses: ids, Usage: s.lastUsage,
		Shared: shared, Source: "network",
	}
	switch {
	case !isURL(s.input):
		v.Source = "file"
	case s.c.e.ingest != nil:
		v.Source = "ingest"
	}
	if state != StateConnected && state != StateBuffering {
		v.DelayMs = 0
	}
	if n := s.src.lastData.Load(); n > 0 {
		t := time.Unix(0, n).UTC()
		v.LastData = &t
	}
	if s.view != nil {
		vv := *s.view
		v.View = &vv
	}
	if withInput {
		v.Input = s.input
	}
	return v
}
