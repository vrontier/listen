package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrontier/listen/listener/internal/analysis"
	"github.com/vrontier/listen/listener/internal/source"
)

// A source is one address at one decode rate: one connection, one decode
// and one front end, whoever asked for it. The streams of all callers that
// name the address attach to it; it connects while any of them wants it.
type sourceConn struct {
	e     *Engine
	input string
	rate  int

	mu         sync.Mutex
	streams    map[*stream]struct{}
	state      string
	since      time.Time
	lastErr    *Problem
	reconnects int
	cancel     context.CancelFunc // set while ffmpeg runs
	srcDone    chan struct{}
	lastWanted time.Time
	retryAt    time.Time     // after a refusal
	retryMax   time.Duration // of the connection running
	contentTyp string        // as the server last sent it

	feMu     sync.Mutex
	fe       *analysis.FrontEnd        // one window and FFT for every stream on it
	feBusyNs atomic.Int64              // time spent in the front end and the views
	feedList atomic.Pointer[[]*stream] // what the front end feeds
	timeline atomic.Int64              // samples decoded since the source was made
	lastData atomic.Int64              // UnixNano of the latest samples
	delay    atomic.Int64              // ms between observation and now
	pid      atomic.Int64              // ffmpeg, 0 when not running
	bytes    atomic.Int64              // bytes received, all connections
	connBase atomic.Int64              // bytes before the running connection
	connAt   atomic.Int64              // timeline when it started
	rateDone atomic.Bool               // its bit rate was checked
	rateB1   atomic.Int64              // bytes at the first point of the check, 0 before
	rateD1   atomic.Int64              // samples decoded at that point
	quit     chan struct{}
	supDone  chan struct{}
}

func newSource(e *Engine, input string, rate int) *sourceConn {
	s := &sourceConn{
		e: e, input: input, rate: rate, streams: map[*stream]struct{}{},
		state: StateIdle, since: time.Now().UTC(), quit: make(chan struct{}), supDone: make(chan struct{}),
		fe: analysis.NewFrontEnd(rate, nil, true),
	}
	s.feedList.Store(&[]*stream{})
	go s.supervise()
	return s
}

// streamList is the attached streams in a fixed order; s.mu must be held.
func (s *sourceConn) streamList() []*stream {
	out := make([]*stream, 0, len(s.streams))
	for st := range s.streams {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].c.name != out[j].c.name {
			return out[i].c.name < out[j].c.name
		}
		return out[i].id < out[j].id
	})
	return out
}

func (s *sourceConn) attach(st *stream) {
	s.mu.Lock()
	s.streams[st] = struct{}{}
	l := s.streamList()
	s.mu.Unlock()
	s.feedList.Store(&l)
}

// detach removes a stream and reports how many remain.
func (s *sourceConn) detach(st *stream) int {
	s.mu.Lock()
	delete(s.streams, st)
	l := s.streamList()
	s.mu.Unlock()
	s.feedList.Store(&l)
	return len(l)
}

func (s *sourceConn) shared() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams)
}

func (s *sourceConn) connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == StateConnected
}

func (s *sourceConn) setDelay(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.delay.Store(d.Milliseconds())
}

// wanted: some stream on the source wants it connected. The idle time is
// the longest and the retry wait the shortest of the attached streams'.
func (s *sourceConn) wanted() (wanted bool, idleAfter, retryMax time.Duration) {
	s.mu.Lock()
	streams := s.streamList()
	s.mu.Unlock()
	idleAfter, retryMax = 30*time.Second, 300*time.Second
	for i, st := range streams {
		st.mu.Lock()
		ia, rm := st.idleAfter, st.retryMax
		st.mu.Unlock()
		if i == 0 || ia > idleAfter {
			idleAfter = ia
		}
		if i == 0 || rm < retryMax {
			retryMax = rm
		}
		wanted = wanted || st.wanted()
	}
	return wanted, idleAfter, retryMax
}

// supervise connects and disconnects the source as it is wanted.
func (s *sourceConn) supervise() {
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

func (s *sourceConn) step(now time.Time) {
	wanted, idleAfter, retryMax := s.wanted()
	s.mu.Lock()
	if wanted {
		s.lastWanted = now
	}
	running, done := s.cancel != nil, s.srcDone
	idleFor, retryAt := now.Sub(s.lastWanted), s.retryAt
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
		s.startSource(retryMax)
	}
}

func (s *sourceConn) startSource(retryMax time.Duration) {
	ctx, cancel := context.WithCancel(s.e.ctx)
	done := make(chan struct{})
	s.mu.Lock()
	s.cancel, s.srcDone, s.retryMax = cancel, done, retryMax
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
		// The engine connects, every time, and counts what arrives.
		cfg.Open = func(ctx context.Context) (io.ReadCloser, string, error) {
			s.connBase.Store(s.bytes.Load())
			s.connAt.Store(s.timeline.Load())
			s.rateDone.Store(false)
			s.rateB1.Store(0)
			body, format, ct, err := s.e.open(ctx, s.input, &s.bytes)
			s.mu.Lock()
			s.contentTyp = ct
			s.mu.Unlock()
			var oe *openError
			if errors.As(err, &oe) {
				now := time.Now().UTC()
				p := *oe.p
				p.At = &now
				if oe.refused {
					return nil, "", &source.RefusedError{Err: refusal{&p}}
				}
				s.mu.Lock()
				s.lastErr = &p
				s.mu.Unlock()
			}
			return body, format, err
		}
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
			log.Printf("source %s: refused: %s", s.input, p.Message)
			s.setState(StateRefused, &p)
		case err != nil:
			now := time.Now().UTC()
			log.Printf("source %s: %v", s.input, err)
			s.setState(StateIdle, &Problem{Code: "input_ended", Message: err.Error(), At: &now})
		case ctx.Err() == nil:
			s.setState(StateIdle, nil) // a file that ended
		}
	}()
}

// refusal carries the address check's answer through source.Run.
type refusal struct{ p *Problem }

func (r refusal) Error() string { return r.p.Message }

func (s *sourceConn) stopSource() {
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

// onState maps ffmpeg's states onto the source's.
func (s *sourceConn) onState(st string) {
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
			for _, st := range *s.feedList.Load() {
				for _, r := range *st.feedList.Load() {
					r.anchorAt(now, at)
				}
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

// setState changes the source's state and tells every stream on it.
func (s *sourceConn) setState(state string, p *Problem) {
	s.mu.Lock()
	if s.state == state && p == nil {
		s.mu.Unlock()
		return
	}
	s.state, s.since = state, time.Now().UTC()
	if p != nil {
		s.lastErr = p
	} else if state == StateConnected {
		s.lastErr = nil
	}
	streams := s.streamList()
	s.mu.Unlock()
	for _, st := range streams {
		st.stateChanged()
	}
}

// status is what a stream reports of its source.
func (s *sourceConn) status() (state string, since time.Time, lastErr *Problem, reconnects int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.since, s.lastErr, s.reconnects
}

// onSamples runs the front end once and hands every frame to every stream
// on the source, each with its own view, and through them to their
// analyses, each through its own queue.
func (s *sourceConn) onSamples(x []float32) {
	tl := s.timeline.Add(int64(len(x)))
	s.lastData.Store(time.Now().UnixNano())
	s.checkRate(tl)
	s.feMu.Lock()
	t0 := time.Now()
	s.fe.Feed(x, func(fr *analysis.Frame) {
		for _, st := range *s.feedList.Load() {
			st.take(fr)
		}
	})
	s.feBusyNs.Add(int64(time.Since(t0)))
	s.feMu.Unlock()
}

// rateFrom and rateTo are the points of a connection's bit-rate check, in
// seconds of decoded audio.
const rateFrom, rateTo = 3, 8

// checkRate refuses a connection whose bit rate is above the limit, once
// per connection: the bytes received between 3 and 8 seconds of decoded
// audio. Data read ahead into buffers, by a server's initial burst too, is
// about the same at both points and cancels out.
func (s *sourceConn) checkRate(timeline int64) {
	if !isURL(s.input) || s.rateDone.Load() {
		return
	}
	decoded := timeline - s.connAt.Load()
	if decoded < rateFrom*int64(s.rate) {
		return
	}
	if s.rateB1.Load() == 0 {
		s.rateD1.Store(decoded)
		s.rateB1.Store(s.bytes.Load() + 1) // +1: a byte count of 0 is "not yet"
		return
	}
	if decoded < rateTo*int64(s.rate) {
		return
	}
	s.rateDone.Store(true)
	secs := float64(decoded-s.rateD1.Load()) / float64(s.rate)
	kbps := float64(s.bytes.Load()-(s.rateB1.Load()-1)) * 8 / secs / 1000
	limit := s.e.opts.Limits.MaxBitrateKbps
	if kbps <= float64(limit) {
		return
	}
	now := time.Now().UTC()
	p := Problem{Code: "bitrate", Message: fmt.Sprintf("%.0f kbit/s is above the limit of %d kbit/s", kbps, limit), At: &now}
	s.mu.Lock()
	s.retryAt = time.Now().Add(s.retryMax)
	cancel := s.cancel
	s.mu.Unlock()
	log.Printf("source %s: refused: %s", s.input, p.Message)
	s.setState(StateRefused, &p)
	if cancel != nil {
		cancel() // ends the connection; the supervisor tries again after retry_max_s
	}
}

// close stops the source for good.
func (s *sourceConn) close() {
	close(s.quit)
	<-s.supDone
}
