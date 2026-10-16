package engine

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrontier/listen/listener/internal/analysis"
	"github.com/vrontier/listen/listener/internal/events"
	"github.com/vrontier/listen/listener/internal/hub"
	"github.com/vrontier/listen/listener/internal/memory"
)

// queueFrames is how many frames (20 a second) wait for an analysis. A
// slower analysis loses frames; it never holds up the decode or the other
// analyses of its stream.
const queueFrames = 100

// sub is one subscriber to an analysis's live events.
type sub struct {
	ch    chan []byte
	types map[string]bool // nil: all
}

// run is one analysis of a stream: its own worker, analyzer, memory,
// snapshot and subscribers.
type run struct {
	s  *stream
	id string
	q  chan *analysis.Frame // shared with the stream's other analyses: read only

	mu        sync.Mutex
	c         checked
	key       string // memory key in use, "" without memory
	mem       *memory.Memory
	subs      map[*sub]struct{}
	failed    *Problem
	anchor    *anchorReq
	running   bool
	stop      chan struct{}
	done      chan struct{}
	started   time.Time
	busyNs    atomic.Int64               // time spent analysing
	observers atomic.Pointer[[]*gmember] // groups this analysis is a member of
	dropped   atomic.Int64               // frames lost because the queue was full
	lastUsage Usage

	// Owned by the worker while it runs; replaced only while paused.
	a       *analysis.Analyzer
	stamper *events.Stamper
	hub     *hub.Hub
	first   bool
}

// beforeFeed, if set, runs before every frame an analysis takes (tests).
var beforeFeed atomic.Pointer[func(r *run)]

type anchorReq struct {
	t  time.Time
	at int64
}

func newRun(s *stream, id string) *run {
	return &run{s: s, id: id, q: make(chan *analysis.Frame, queueFrames), subs: map[*sub]struct{}{}, started: time.Now()}
}

func (r *run) source() string { return r.s.id + "/" + r.id }

// apply puts checked settings into effect. A change of range or rules
// starts the analysis again, with another memory; a change of memory mode
// opens or closes the memory; the rest is applied to the running analysis.
// A failed analysis starts again.
func (r *run) apply(c checked) error {
	r.pause()
	defer r.resume()
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.c
	fresh := r.a == nil || r.failed != nil || c.rng != old.rng || c.memoryKey() != old.memoryKey()
	if fresh {
		r.closeMemoryLocked()
		r.stamper = events.NewStamper(r.source())
		r.hub = hub.New(nil)
		mem, key, err := r.openMemory(c)
		if err != nil {
			return err
		}
		r.mem, r.key = mem, key
		// The stream's front end computes the spectrum and the view once
		// for all its analyses.
		r.a = analysis.New(r.s.rate, r.stamper, r.emit, c.options(nil))
		r.a.Live(true)
		r.a.OnFrame(r.s.setDelay)
		r.first = true
		r.failed = nil
	} else {
		if c.memMode != old.memMode {
			r.closeMemoryLocked()
			mem, key, err := r.openMemory(c)
			if err != nil {
				return err
			}
			r.mem, r.key = mem, key
		}
		r.a.Tune(c.transientK, c.transientGap)
		if r.mem != nil {
			r.mem.SetReturnAfter(time.Duration(c.motifReturn) * time.Second)
			r.mem.SetHistoryDays(c.historyDays)
		}
	}
	r.c = c
	return nil
}

func (r *run) memoryDir(key string) string {
	return filepath.Join(r.s.c.memDir, r.s.id, r.id, key)
}

// openMemory opens the memory for c: on disk under the stream's, the
// analysis's and the settings' key, in RAM only, or none.
func (r *run) openMemory(c checked) (*memory.Memory, string, error) {
	if c.memMode == MemoryOff {
		return nil, "", nil
	}
	key := c.memoryKey()
	cfg := memory.Config{
		ReturnAfter: time.Duration(c.motifReturn) * time.Second, HistoryDays: c.historyDays,
		Live: true, Quiet: true,
	}
	if c.memMode == MemoryDisk {
		cfg.Dir = r.memoryDir(key)
		if err := writeMeta(cfg.Dir, c); err != nil {
			return nil, "", err
		}
	}
	m, err := memory.New(cfg, r.stamper)
	if err != nil {
		return nil, "", fmt.Errorf("memory %s: %w", key, err)
	}
	return m, key, nil
}

func (r *run) closeMemoryLocked() {
	if r.mem == nil {
		return
	}
	if err := r.mem.Close(); err != nil {
		log.Printf("analysis %s: memory: %v", r.source(), err)
	}
	if r.c.memMode == MemoryDisk {
		touchMeta(r.memoryDir(r.key))
	}
	r.mem, r.key = nil, ""
}

// emit is the analyzer's output: through the memory, if any, to the
// snapshot and the subscribers. It runs on the worker.
func (r *run) emit(m events.Message) {
	if r.mem == nil {
		r.out(m)
		return
	}
	for _, o := range r.mem.Process(m) {
		r.out(o)
	}
}

// out records m in the snapshot and hands it to the subscribers that want
// its type. A subscriber whose queue is full misses it.
func (r *run) out(m events.Message) {
	r.deliver(r.hub, m)
	r.notifyObservers(m)
}

func (r *run) deliver(hb *hub.Hub, m events.Message) {
	hb.Broadcast(m)
	frame := []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", m.Env.Sequence, m.Type, m.Data))
	r.mu.Lock()
	for sb := range r.subs {
		if sb.types != nil && !sb.types[m.Type] {
			continue
		}
		select {
		case sb.ch <- frame:
		default:
		}
	}
	r.mu.Unlock()
}

func (r *run) pause() {
	r.mu.Lock()
	if !r.running {
		r.mu.Unlock()
		return
	}
	stop, done := r.stop, r.done
	r.running = false
	r.mu.Unlock()
	close(stop)
	<-done
}

func (r *run) resume() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running || r.failed != nil || r.a == nil {
		return
	}
	r.running = true
	r.stop, r.done = make(chan struct{}), make(chan struct{})
	go r.work(r.stop, r.done)
}

func (r *run) work(stop, done chan struct{}) {
	defer close(done)
	for {
		select {
		case <-stop:
			return
		case fr := <-r.q:
			if !r.feed(fr) {
				return
			}
		}
	}
}

// feed analyses one frame. A panic in the analysis stops this analysis
// only: it reports failed, its stream and the other analyses go on.
func (r *run) feed(fr *analysis.Frame) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			r.fail(p)
			ok = false
		}
	}()
	if f := beforeFeed.Load(); f != nil {
		(*f)(r)
	}
	r.mu.Lock()
	anc := r.anchor
	if anc != nil && fr.End >= anc.at {
		r.anchor = nil
	} else {
		anc = nil
	}
	r.mu.Unlock()
	if r.first {
		// Joining a stream already running: the clock starts from now.
		r.a.AnchorAt(time.Now(), fr.End)
		r.first = false
	}
	if anc != nil {
		r.a.AnchorAt(anc.t, anc.at) // a new connection
	}
	t0 := time.Now()
	r.a.Analyse(fr)
	r.busyNs.Add(int64(time.Since(t0)))
	return true
}

func (r *run) fail(p any) {
	log.Printf("analysis %s failed: %v\n%s", r.source(), p, debug.Stack())
	now := time.Now().UTC()
	r.mu.Lock()
	r.failed = &Problem{Code: "analysis_failed", Message: fmt.Sprintf("the analysis stopped after an internal error: %v", p), At: &now}
	r.running = false
	r.mu.Unlock()
	r.s.c.notifyAnalysis(r)
	r.emitStatus()
}

// failNow stops the analysis as a crash in it would (test hook).
func (r *run) failNow(why string) {
	r.pause()
	now := time.Now().UTC()
	r.mu.Lock()
	r.failed = &Problem{Code: "analysis_failed", Message: why, At: &now}
	r.mu.Unlock()
	r.s.c.notifyAnalysis(r)
	r.emitStatus()
}

// push queues a frame for the analysis, or drops it if the queue is full or
// the analysis has stopped.
func (r *run) push(fr *analysis.Frame) {
	select {
	case r.q <- fr:
	default:
		r.dropped.Add(1)
	}
}

// anchorAt pins the observation clock to t from sample at on (a new
// connection).
func (r *run) anchorAt(t time.Time, at int64) {
	r.mu.Lock()
	r.anchor = &anchorReq{t, at}
	r.mu.Unlock()
}

func (r *run) state() string {
	r.mu.Lock()
	failed := r.failed != nil
	r.mu.Unlock()
	switch {
	case failed:
		return AnalysisFailed
	case r.s.connected():
		return AnalysisRunning
	}
	return AnalysisIdle
}

// emitStatus sends system.status, describing the stream as this analysis
// sees it.
func (r *run) emitStatus() {
	r.mu.Lock()
	st, hb, label, key := r.stamper, r.hub, r.c.spec.Label, r.key
	r.mu.Unlock()
	if st == nil {
		return
	}
	stream, reconnects := r.s.status()
	p := events.Status{
		Stream: stream, Analysis: r.state(), LatencyMs: float64(r.s.src.delay.Load()),
		Input: "live", SampleRate: r.s.rate, UptimeS: float64(int(time.Since(r.started).Seconds())),
		Reconnects: reconnects, Label: label, MemoryKey: key,
	}
	if !isURL(r.s.input) {
		p.Input = "file"
	}
	r.deliver(hb, st.Stamp(events.TypeStatus, time.Now(), 0, p))
}

// subscribe adds a subscriber, within the limit.
func (r *run) subscribe(types map[string]bool, limit int) (*sub, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.subs) >= limit {
		return nil, false
	}
	sb := &sub{ch: make(chan []byte, 512), types: types}
	r.subs[sb] = struct{}{}
	return sb, true
}

func (r *run) unsubscribe(sb *sub) {
	r.mu.Lock()
	if _, ok := r.subs[sb]; ok {
		delete(r.subs, sb)
		close(sb.ch)
	}
	r.mu.Unlock()
}

func (r *run) subscribers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subs)
}

// close stops the analysis for good: its worker, its subscribers and its
// memory.
func (r *run) close() {
	r.pause()
	r.mu.Lock()
	defer r.mu.Unlock()
	for sb := range r.subs {
		delete(r.subs, sb)
		close(sb.ch)
	}
	r.closeMemoryLocked()
	r.failed = &Problem{Code: "deleted", Message: "the analysis was deleted"} // nothing resumes it
}

// snapshot is …/state: the latest frame, spectrum, features and status, the
// active resonances and motifs, recent events and the memory's summaries.
func (r *run) snapshot() any {
	r.mu.Lock()
	hb, mem := r.hub, r.mem
	r.mu.Unlock()
	if hb == nil {
		return hub.Snapshot{ActiveResonances: []events.Envelope{}, RecentEvents: []events.Envelope{}, ActiveMotifs: []any{}}
	}
	s := hb.Snapshot()
	if mem != nil {
		s.ActiveMotifs = mem.ActiveMotifs()
		s.Memory = mem.Summaries()
	}
	return s
}

func (r *run) view() AnalysisView {
	r.mu.Lock()
	c, key, failed, mem, nsubs := r.c, r.key, r.failed, r.mem, len(r.subs)
	u := r.lastUsage
	r.mu.Unlock()
	rules := c.spec.Rules
	if rules == nil {
		rules = []Rule{}
	}
	motifs := 0
	if mem != nil {
		motifs = mem.Established()
	}
	v := AnalysisView{
		ID: r.id, Stream: r.s.id, Settings: c.spec.Settings, Rules: rules,
		Memory: MemoryView{Mode: c.memMode, MotifReturnS: c.motifReturn, HistoryDays: c.historyDays, Key: key},
		Label:  c.spec.Label, Effective: c.effective(), State: r.state(), Subscribers: nsubs, Motifs: motifs, Usage: u,
	}
	if failed != nil && failed.Code != "deleted" {
		v.LastError = failed
	}
	return v
}

// meta is what the engine keeps beside a memory on disk, for the listing.
type meta struct {
	Settings  Settings  `json:"settings"`
	Rules     []Rule    `json:"rules"`
	FirstUsed time.Time `json:"first_used"`
	LastUsed  time.Time `json:"last_used"`
}

const metaFile = "engine-memory.json"

func writeMeta(dir string, c checked) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	now := time.Now().UTC()
	m := meta{Settings: c.spec.Settings, Rules: c.spec.Rules, FirstUsed: now, LastUsed: now}
	if old, err := readMeta(dir); err == nil {
		m.FirstUsed = old.FirstUsed
	}
	if m.Rules == nil {
		m.Rules = []Rule{}
	}
	return writeJSONFile(filepath.Join(dir, metaFile), m)
}

func touchMeta(dir string) {
	m, err := readMeta(dir)
	if err != nil {
		return
	}
	m.LastUsed = time.Now().UTC()
	writeJSONFile(filepath.Join(dir, metaFile), m)
}

func readMeta(dir string) (meta, error) {
	var m meta
	b, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// writeJSONFile replaces path atomically.
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
