package engine

import (
	"context"
	"errors"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrontier/listen/listener/internal/analysis"
	"github.com/vrontier/listen/listener/internal/source"
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

// stream is one input: one connection, one decode at one sample rate,
// feeding all its analyses.
type stream struct {
	e     *Engine
	id    string
	input string
	rate  int

	mu         sync.Mutex
	spec       StreamSpec // as given
	mode       string
	idleAfter  time.Duration
	retryMax   time.Duration
	view       *View
	runs       map[string]*run
	state      string
	since      time.Time
	lastErr    *Problem
	reconnects int
	cancel     context.CancelFunc // set while the source runs
	srcDone    chan struct{}
	lastWanted time.Time
	retryAt    time.Time // after a refusal
	lastUsage  Usage

	feMu     sync.Mutex
	fe       *analysis.FrontEnd     // one window, FFT and view for all analyses
	feBusyNs atomic.Int64           // time spent in the front end
	feedList atomic.Pointer[[]*run] // what the front end feeds
	timeline atomic.Int64           // samples decoded since the stream was created
	lastData atomic.Int64           // UnixNano of the latest samples
	delay    atomic.Int64           // ms between observation and now
	pid      atomic.Int64           // ffmpeg, 0 when not running
	quit     chan struct{}
	supDone  chan struct{}
}

func newStream(e *Engine, id string, spec StreamSpec, view *View) *stream {
	s := &stream{
		e: e, id: id, input: spec.Input, rate: spec.SampleRate, runs: map[string]*run{},
		state: StateIdle, since: time.Now().UTC(), quit: make(chan struct{}), supDone: make(chan struct{}),
	}
	s.fe = analysis.NewFrontEnd(spec.SampleRate, view.options(), true)
	s.setSpec(spec, view)
	s.feedList.Store(&[]*run{})
	go s.supervise()
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
		s.feMu.Lock()
		s.fe.SetView(view.options())
		s.feMu.Unlock()
	}
}

func sameView(a, b *View) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
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

func (s *stream) connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == StateConnected
}

func (s *stream) status() (state string, reconnects int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.reconnects
}

func (s *stream) setDelay(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.delay.Store(d.Milliseconds())
}

// wanted: the stream should be connected now.
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
	return s.e.groupWants(s.id) // a running group keeps its members' streams connected
}

// supervise connects and disconnects the stream as it is wanted.
func (s *stream) supervise() {
	defer close(s.supDone)
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		s.step(time.Now())
		select {
		case <-s.quit:
			s.stopSource()
			return
		case <-t.C:
		}
	}
}

func (s *stream) step(now time.Time) {
	wanted := s.wanted()
	s.mu.Lock()
	if wanted {
		s.lastWanted = now
	}
	running, done := s.cancel != nil, s.srcDone
	idleFor, retryAt, idleAfter := now.Sub(s.lastWanted), s.retryAt, s.idleAfter
	s.mu.Unlock()
	if running {
		select {
		case <-done:
			s.mu.Lock()
			s.cancel, s.srcDone = nil, nil
			s.mu.Unlock()
			return
		default:
		}
		if !wanted && idleFor >= idleAfter {
			s.stopSource()
		}
		return
	}
	if wanted && !now.Before(retryAt) {
		s.startSource()
	}
}

func (s *stream) startSource() {
	ctx, cancel := context.WithCancel(s.e.ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel, s.srcDone = cancel, done
	retryMax := s.retryMax
	s.mu.Unlock()
	s.setState(StateConnecting, nil)
	cfg := source.Config{
		Input: s.input, SampleRate: s.rate, FFmpeg: s.e.opts.FFmpeg, MaxBackoff: retryMax,
		OnSamples: s.onSamples, OnState: s.onState,
		BeforeStart: func() error {
			p, err := s.e.policy.check(ctx, s.input)
			if p != nil && !errors.Is(err, errUnresolved) {
				return refusal{p}
			}
			return nil
		},
		OnProcess: func(pid int) { s.pid.Store(int64(pid)) },
	}
	if isURL(s.input) {
		cfg.ProtocolWhitelist = protocolWhitelist
	} else {
		cfg.Realtime, cfg.Loop = true, true // development and tests
	}
	go func() {
		defer close(done)
		err := source.Run(ctx, cfg)
		var ref *source.RefusedError
		var why refusal
		switch {
		case errors.As(err, &ref) && errors.As(ref.Err, &why):
			now := time.Now().UTC()
			p := *why.p
			p.At = &now
			s.mu.Lock()
			s.retryAt = time.Now().Add(retryMax)
			s.mu.Unlock()
			log.Printf("stream %s: refused: %s", s.id, p.Message)
			s.setState(StateRefused, &p)
		case err != nil:
			now := time.Now().UTC()
			log.Printf("stream %s: %v", s.id, err)
			s.setState(StateIdle, &Problem{Code: "input_ended", Message: err.Error(), At: &now})
		case ctx.Err() == nil:
			s.setState(StateIdle, nil) // a file that ended
		}
	}()
}

// refusal carries the address check's answer through source.Run.
type refusal struct{ p *Problem }

func (r refusal) Error() string { return r.p.Message }

func (s *stream) stopSource() {
	s.mu.Lock()
	cancel, done := s.cancel, s.srcDone
	s.cancel, s.srcDone = nil, nil
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	s.pid.Store(0)
	s.mu.Lock()
	refused := s.state == StateRefused
	s.mu.Unlock()
	if !refused {
		s.setState(StateIdle, nil)
	}
}

// onState maps the source's states onto the stream's.
func (s *stream) onState(st string) {
	switch st {
	case source.Connecting:
		// reported by startSource already
	case source.Connected:
		s.mu.Lock()
		fresh := s.state != StateBuffering && s.state != StateConnected
		s.mu.Unlock()
		if fresh {
			// A new connection: every analysis's clock starts from now.
			at, now := s.timeline.Load(), time.Now()
			for _, r := range *s.feedList.Load() {
				r.anchorAt(now, at)
			}
		}
		s.setState(StateConnected, nil)
	case source.Buffering:
		s.setState(StateBuffering, nil)
	case source.Reconnecting:
		s.mu.Lock()
		s.reconnects++
		s.mu.Unlock()
		s.setState(StateReconnecting, nil)
	}
}

func (s *stream) setState(st string, p *Problem) {
	s.mu.Lock()
	if s.state == st && p == nil {
		s.mu.Unlock()
		return
	}
	s.state, s.since = st, time.Now().UTC()
	if p != nil {
		s.lastErr = p
	} else if st == StateConnected {
		s.lastErr = nil
	}
	runs := s.runList()
	s.mu.Unlock()
	s.e.notify("stream.state", s.viewOf(false))
	for _, r := range runs {
		r.emitStatus()
		s.e.notifyAnalysis(r)
	}
}

// onSamples runs the stream's front end once and hands every frame to all
// its analyses, each through its own queue.
func (s *stream) onSamples(x []float32) {
	s.timeline.Add(int64(len(x)))
	s.lastData.Store(time.Now().UnixNano())
	s.feMu.Lock()
	t0 := time.Now()
	s.fe.Feed(x, func(fr *analysis.Frame) {
		for _, r := range *s.feedList.Load() {
			r.push(fr)
		}
	})
	s.feBusyNs.Add(int64(time.Since(t0)))
	s.feMu.Unlock()
}

// close stops the stream, its source and its analyses.
func (s *stream) close() {
	close(s.quit)
	<-s.supDone
	s.mu.Lock()
	runs := s.runList()
	s.mu.Unlock()
	for _, r := range runs {
		r.close()
	}
}

func (s *stream) viewOf(withInput bool) StreamView {
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
		State: s.state, Since: s.since, LastError: s.lastErr, Reconnects: s.reconnects,
		DelayMs: int(s.delay.Load()), Analyses: ids, Usage: s.lastUsage,
	}
	if s.state != StateConnected && s.state != StateBuffering {
		v.DelayMs = 0
	}
	if n := s.lastData.Load(); n > 0 {
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
