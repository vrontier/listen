package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrontier/listen/ingest"
	"github.com/vrontier/listen/ingest/fetch"
)

// Options configure the engine process.
type Options struct {
	MemoryDir string // memories: <dir>/<caller>/<stream>/<analysis>/<key>/
	// StateFile holds the configuration last given to the first caller;
	// the others' go beside it (state.json → state-internal.json). "" keeps
	// none.
	StateFile    string
	Callers      []string // names of the callers, the first is the default; empty: "workbench"
	FFmpeg       string
	AllowPrivate bool     // accept stream addresses on private networks (intranet, tests)
	AllowFiles   bool     // accept file paths as inputs (development, tests)
	AllowHosts   []string // hosts (or host:port) accepted although private, e.g. a test server
	Limits       Limits
	Version      string
	TestHooks    []string // callers that get the test routes (development, loopback only)
	// Ingest is the socket of listen-ingest on this machine: streams are
	// read through it, and the engine connects to no station itself.
	// Empty: the engine connects itself (development).
	Ingest string
}

// DefaultCaller is the caller's name when none is given.
const DefaultCaller = "workbench"

// Engine holds the callers and the sources they share.
type Engine struct {
	opts     Options
	instance string
	started  time.Time
	policy   *fetch.Policy
	client   *http.Client   // opens streams itself; every dial is checked
	ingest   *ingest.Client // or reads them through the ingest
	ctx      context.Context
	cancel   context.CancelFunc
	probes   *probeGate
	meter    *meter

	callers []*caller // in the order given; the first is the default
	byName  map[string]*caller

	smu     sync.Mutex // sources, by address
	sources map[string]*sourceConn

	nstreams atomic.Int64 // streams of all callers, for the engine-wide limit
	ngroups  atomic.Int64 // groups of all callers

	loops sync.WaitGroup
}

// New starts an engine and resumes each caller's saved configuration.
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
	if len(opts.Callers) == 0 {
		opts.Callers = []string{DefaultCaller}
	}
	seen := map[string]bool{}
	for _, n := range opts.Callers {
		if !idRE.MatchString(n) || seen[n] {
			return nil, fmt.Errorf("engine: caller %q: a name of lower-case letters, digits and hyphens, each once", n)
		}
		seen[n] = true
	}
	if err := migrateMemory(opts.MemoryDir, opts.Callers[0]); err != nil {
		return nil, err
	}
	b := make([]byte, 4)
	rand.Read(b)
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		opts: opts, instance: hex.EncodeToString(b), started: time.Now().UTC(),
		policy: fetch.NewPolicy(opts.AllowPrivate, opts.AllowFiles, opts.AllowHosts...),
		ctx:    ctx, cancel: cancel, probes: newProbeGate(),
		byName: map[string]*caller{}, sources: map[string]*sourceConn{},
	}
	e.meter = newMeter(e)
	e.client = e.policy.Client(opts.Version)
	if opts.Ingest != "" {
		e.ingest = ingest.NewClient(opts.Ingest)
	}
	for i, n := range opts.Callers {
		c := newCaller(e, n, stateFileFor(opts.StateFile, n, i == 0))
		for _, h := range opts.TestHooks {
			c.testHooks = c.testHooks || h == n
		}
		e.callers = append(e.callers, c)
		e.byName[n] = c
	}
	for _, c := range e.callers {
		if err := c.resume(); err != nil {
			e.Close()
			return nil, err
		}
	}
	e.loops.Add(3)
	go e.statusLoop()
	go e.usageLoop()
	go e.groupLoop()
	return e, nil
}

// stateFileFor is where a caller's configuration is saved: the first
// caller keeps the file as named (so an engine that had one caller resumes
// as before), the others get their name added.
func stateFileFor(file, name string, first bool) string {
	if file == "" || first {
		return file
	}
	ext := filepath.Ext(file)
	return strings.TrimSuffix(file, ext) + "-" + name + ext
}

// migrateMemory moves memories kept before callers existed
// (<dir>/<stream>/…) under the first caller (<dir>/<caller>/<stream>/…),
// once. A marker file records the layout.
func migrateMemory(dir, first string) error {
	marker := filepath.Join(dir, ".layout")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	moved := 0
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		dst := filepath.Join(dir, first)
		if err := os.MkdirAll(dst, 0o750); err != nil {
			return err
		}
		if err := os.Rename(filepath.Join(dir, ent.Name()), filepath.Join(dst, ent.Name())); err != nil {
			return fmt.Errorf("engine: moving %s under caller %s: %w", ent.Name(), first, err)
		}
		moved++
	}
	if moved > 0 {
		log.Printf("engine: %d memory folders moved under caller %s", moved, first)
	}
	return os.WriteFile(marker, []byte("callers\n"), 0o640)
}

// Caller returns the named caller, or nil.
func (e *Engine) Caller(name string) *caller { return e.byName[name] }

// Close stops every stream and analysis and saves the memories. The saved
// configurations stay, so the next start resumes them.
func (e *Engine) Close() {
	for _, c := range e.callers {
		c.close()
	}
	e.smu.Lock()
	rest := make([]*sourceConn, 0, len(e.sources))
	for _, s := range e.sources {
		rest = append(rest, s)
	}
	e.sources = map[string]*sourceConn{}
	e.smu.Unlock()
	for _, s := range rest {
		s.close()
	}
	e.cancel()
	e.loops.Wait()
}

func version() string {
	v := "0.2.0"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				v += "+" + s.Value[:7]
			}
		}
	}
	return v
}

// --- sources ---

// sourceKey identifies a source. Through the ingest, an address may be
// decoded at several rates: each rate is a decode of its own, and the
// station still sees the ingest's one connection. An engine connecting
// itself decodes an address at one rate only.
func (e *Engine) sourceKey(input string, rate int) string {
	if e.ingest != nil {
		return fmt.Sprintf("%s|%d", input, rate)
	}
	return input
}

// acquireSource returns the source of an address at a rate, made if it is
// new. Without the ingest, a second rate is a conflict.
func (e *Engine) acquireSource(input string, rate int) (*sourceConn, *apiError) {
	e.smu.Lock()
	defer e.smu.Unlock()
	key := e.sourceKey(input, rate)
	if s := e.sources[key]; s != nil {
		if s.rate != rate {
			return nil, &apiError{status: 409, code: "rate_conflict",
				message: fmt.Sprintf("this address is already decoded at %d Hz, shared with other streams; use that rate", s.rate)}
		}
		return s, nil
	}
	s := newSource(e, input, rate)
	s.key = key
	e.sources[key] = s
	return s, nil
}

// releaseSource detaches a stream from its source and closes the source
// when no stream is left on it.
func (e *Engine) releaseSource(st *stream) {
	e.smu.Lock()
	src := st.src
	left := src.detach(st)
	if left == 0 && e.sources[src.key] == src {
		delete(e.sources, src.key)
	}
	e.smu.Unlock()
	if left == 0 {
		src.close()
	}
}

// source returns a source of the address, at any rate, or nil.
func (e *Engine) source(input string) *sourceConn {
	e.smu.Lock()
	defer e.smu.Unlock()
	if s := e.sources[input]; s != nil {
		return s
	}
	for _, s := range e.sources {
		if s.input == input {
			return s
		}
	}
	return nil
}

func (e *Engine) sourceList() []*sourceConn {
	e.smu.Lock()
	defer e.smu.Unlock()
	out := make([]*sourceConn, 0, len(e.sources))
	for _, s := range e.sources {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].input < out[j].input })
	return out
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
		fp, err := e.policy.Check(ctx, spec.Input)
		if !errors.Is(err, fetch.ErrUnresolved) {
			p = fromFetch(fp)
		}
	default:
		p = fromFetch(e.policy.CheckStatic(spec.Input))
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
		for _, c := range e.callers {
			for _, r := range c.allRuns() {
				r.emitStatus()
			}
		}
	}
}

// usageLoop measures what everything costs every 10 seconds and tells each
// caller its part.
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
		e.meter.measure()
		for _, c := range e.callers {
			c.notify("usage", e.meter.reportFor(c))
		}
	}
}

// openFeed opens a stream address: through the ingest when there is one,
// otherwise the engine connects itself, checking every address it dials.
func (e *Engine) openFeed(ctx context.Context, input string, n *atomic.Int64) (io.ReadCloser, string, string, error) {
	if e.ingest != nil {
		return e.ingest.Open(ctx, input, n)
	}
	return fetch.Open(ctx, e.client, "listen-engine/"+e.opts.Version, input, n)
}
