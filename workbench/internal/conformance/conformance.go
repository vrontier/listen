// Package conformance checks an engine against the control API
// (docs/control-api.md): a list of requests and what the answers must be,
// phase by phase, so that the engine's owner can check the engine and the
// Workbench's tests can check the stand-in with the same list.
//
// Phase 1 is one process with many streams, one analysis each; phase 2
// several analyses on one stream, trials, usage and a stream's view;
// phase 3 groups (control API 1.1).
package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vrontier/listen-workbench/internal/engine"
)

// Options say what to check and how.
type Options struct {
	// Input is a stream address the engine may connect to. Without it the
	// checks that need audio are skipped.
	Input string
	// SampleRate of the test streams (default 22050).
	SampleRate int
	// Phase is the last phase to check (default 1).
	Phase int
	// Timeout for each check that waits for events (default 20 s).
	Timeout time.Duration
	// Prefix of the identifiers the checks make (default "ct").
	Prefix string
	// Raw sends a request as it is, for what the client cannot send (a
	// body with an unknown field). Without it those checks are skipped.
	Raw func(method, path string, body []byte) (status int, respBody []byte, err error)
	// Fail makes an analysis fail, where the engine offers a way to (the
	// stand-in does, for tests). Without it the check of the state
	// "failed" is skipped.
	Fail func(stream, analysis string) bool
	// Second is the same engine reached as another caller (another socket,
	// e.g. listen-engine's internal.sock). With it the checks of callers
	// and shared sources run (decided 2026-10-16).
	Second *engine.Client
}

// Result is the outcome of one check.
type Result struct {
	Phase  int
	Name   string
	Status string // pass, fail or skip
	Detail string
}

// Statuses of a result.
const (
	Pass = "pass"
	Fail = "fail"
	Skip = "skip"
)

// Failed reports whether any result failed.
func Failed(rs []Result) bool {
	return slices.ContainsFunc(rs, func(r Result) bool { return r.Status == Fail })
}

// run holds what the checks share.
type run struct {
	ctx     context.Context
	c       *engine.Client
	opt     Options
	results []Result
	streams []string // made by the checks, deleted at the end
	groups  []string
	second  []string // made through the second caller
	info    engine.Info
}

// skipped is returned by a check that cannot run here.
type skipped string

func (s skipped) Error() string { return string(s) }

func (r *run) check(phase int, name string, f func() (string, error)) {
	if phase > r.opt.Phase {
		return
	}
	detail, err := f()
	res := Result{Phase: phase, Name: name, Status: Pass, Detail: detail}
	var sk skipped
	switch {
	case errors.As(err, &sk):
		res.Status, res.Detail = Skip, string(sk)
	case err != nil:
		res.Status, res.Detail = Fail, err.Error()
	}
	r.results = append(r.results, res)
}

func (r *run) id(kind string, n int) string { return fmt.Sprintf("%s-%s%d", r.opt.Prefix, kind, n) }

// putStream makes a stream and notes it for cleaning up.
func (r *run) putStream(id string, spec engine.StreamSpec) (engine.Stream, error) {
	s, err := r.c.PutStream(r.ctx, id, spec)
	if err == nil && !slices.Contains(r.streams, id) {
		r.streams = append(r.streams, id)
	}
	return s, err
}

func (r *run) input(n int) string {
	if r.opt.Input == "" {
		return ""
	}
	if n == 0 {
		return r.opt.Input
	}
	// A second stream needs an address of its own (an address exists
	// once); a query keeps it the same source for the engine to reach.
	sep := "?"
	if strings.Contains(r.opt.Input, "?") {
		sep = "&"
	}
	return fmt.Sprintf("%s%sct=%d", r.opt.Input, sep, n)
}

// spec is an analysis's settings as JSON.
func spec(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// wantErr checks that err is an engine error with the status and code,
// and that it carries a message for people.
func wantErr(err error, status int, code string) error {
	var e *engine.Error
	if !errors.As(err, &e) {
		if err == nil {
			return fmt.Errorf("accepted; want %d %s", status, code)
		}
		return fmt.Errorf("want %d %s, got %v", status, code, err)
	}
	if e.Status != status || e.Code != code {
		return fmt.Errorf("want %d %s, got %d %s (%s)", status, code, e.Status, e.Code, e.Message)
	}
	if e.Message == "" {
		return fmt.Errorf("%d %s has no message", status, code)
	}
	return nil
}

func hasPath(errs []engine.FieldError, path string) bool {
	return slices.ContainsFunc(errs, func(f engine.FieldError) bool { return f.Path == path })
}

// await follows an event stream until match says done or the timeout ends.
func (r *run) await(follow func(ctx context.Context, fn func(engine.SSE)) error, match func(engine.SSE) bool) error {
	ctx, cancel := context.WithTimeout(r.ctx, r.opt.Timeout)
	defer cancel()
	var once sync.Once
	found := false
	err := follow(ctx, func(ev engine.SSE) {
		if !found && match(ev) {
			found = true
			once.Do(cancel)
		}
	})
	if found {
		return nil
	}
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "context") {
		return err
	}
	return fmt.Errorf("nothing within %s", r.opt.Timeout)
}

func (r *run) spectrum(stream, analysis string, match func(map[string]any) error) error {
	var last error
	err := r.await(func(ctx context.Context, fn func(engine.SSE)) error {
		return r.c.Subscribe(ctx, stream, analysis, fn, "signal.spectrum")
	}, func(ev engine.SSE) bool {
		if ev.Event != "signal.spectrum" {
			last = fmt.Errorf("an event %q came through ?types=signal.spectrum", ev.Event)
			return false
		}
		var env struct {
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(ev.Data, &env); err != nil {
			last = fmt.Errorf("event data: %v", err)
			return false
		}
		if match == nil {
			return true
		}
		last = match(env.Payload)
		return last == nil
	})
	if err != nil && last != nil {
		return last
	}
	return err
}

// Run checks the engine behind c. It deletes whatever it made.
func Run(ctx context.Context, c *engine.Client, opt Options) []Result {
	if opt.SampleRate == 0 {
		opt.SampleRate = 22050
	}
	if opt.Phase == 0 {
		opt.Phase = 1
	}
	if opt.Timeout == 0 {
		opt.Timeout = 20 * time.Second
	}
	if opt.Prefix == "" {
		opt.Prefix = "ct"
	}
	r := &run{ctx: ctx, c: c, opt: opt}
	defer r.cleanUp()
	r.phase1()
	if opt.Phase >= 2 {
		r.phase2()
	}
	if opt.Phase >= 3 {
		r.phase3()
	}
	if opt.Second != nil {
		r.callers()
	}
	return r.results
}

func (r *run) cleanUp() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, g := range r.groups {
		r.c.DeleteGroup(ctx, g)
	}
	for _, s := range r.streams {
		r.c.DeleteStream(ctx, s, true)
	}
	for _, s := range r.second {
		r.opt.Second.DeleteStream(ctx, s, true)
	}
}

func (r *run) phase1() {
	ctx := r.ctx
	needInput := skipped("needs -input, a stream the engine may connect to")

	r.check(1, "engine: version, instance, limits, capabilities", func() (string, error) {
		i, err := r.c.Info(ctx)
		if err != nil {
			return "", err
		}
		r.info = i
		switch {
		case i.API.Major != 1:
			return "", fmt.Errorf("api %s; want 1.x", i.API)
		case i.Instance == "":
			return "", errors.New("no instance")
		case i.Limits.Streams < 1 || i.Limits.AnalysesPerStream < 1 || i.Limits.MaxSampleRate < 8000:
			return "", fmt.Errorf("limits incomplete: %+v", i.Limits)
		case i.Capabilities.AddressCheck != "initial" && i.Capabilities.AddressCheck != "every_connection":
			return "", fmt.Errorf("capabilities.address_check %q; want initial or every_connection", i.Capabilities.AddressCheck)
		case len(i.Capabilities.RuleKinds) == 0:
			return "", errors.New("no capabilities.rule_kinds")
		}
		return fmt.Sprintf("api %s, engine %s, address check %s", i.API, i.EngineVersion, i.Capabilities.AddressCheck), nil
	})

	r.check(1, "events: engine.hello first, with instance, api and resumed", func() (string, error) {
		var hello map[string]json.RawMessage
		var first string
		err := r.await(r.c.Changes, func(ev engine.SSE) bool {
			first = ev.Event
			json.Unmarshal(ev.Data, &hello)
			return true
		})
		if err != nil {
			return "", err
		}
		if first != "engine.hello" {
			return "", fmt.Errorf("first event %q", first)
		}
		for _, k := range []string{"instance", "api", "resumed"} {
			if _, ok := hello[k]; !ok {
				return "", fmt.Errorf("engine.hello lacks %q", k)
			}
		}
		var inst string
		json.Unmarshal(hello["instance"], &inst)
		if r.info.Instance != "" && inst != r.info.Instance {
			return "", fmt.Errorf("instance %q; GET /v1/engine said %q", inst, r.info.Instance)
		}
		return "resumed " + string(hello["resumed"]), nil
	})

	r.check(1, "requests: an unknown field is refused (400 unknown_field)", func() (string, error) {
		if r.opt.Raw == nil {
			return "", skipped("no raw requests here")
		}
		body := fmt.Sprintf(`{"input":%q,"sample_rate":%d,"no_such_field":1}`, "https://stream.example.org/x", r.opt.SampleRate)
		st, b, err := r.opt.Raw("PUT", "/v1/streams/"+r.id("s", 0), []byte(body))
		if err != nil {
			return "", err
		}
		var wrap struct {
			Error engine.Error `json:"error"`
		}
		json.Unmarshal(b, &wrap)
		if st != 400 || wrap.Error.Code != "unknown_field" {
			return "", fmt.Errorf("got %d %q; want 400 unknown_field", st, wrap.Error.Code)
		}
		return "", nil
	})

	r.check(1, "identifiers: a bad one is refused (400 bad_id)", func() (string, error) {
		_, err := r.c.PutStream(ctx, "Bad_Id", engine.StreamSpec{Input: "https://stream.example.org/x", SampleRate: r.opt.SampleRate})
		return "", wantErr(err, 400, "bad_id")
	})

	r.check(1, "address check: loopback and private addresses are refused (422, input)", func() (string, error) {
		for _, in := range []string{"http://127.0.0.1:8080/live", "http://10.20.30.40/live", "http://172.16.5.5/live", "file:///etc/passwd"} {
			_, err := r.c.PutStream(ctx, r.id("s", 9), engine.StreamSpec{Input: in, SampleRate: r.opt.SampleRate})
			if e := wantErr(err, 422, "invalid"); e != nil {
				return "", fmt.Errorf("%s: %v", in, e)
			}
			var ee *engine.Error
			errors.As(err, &ee)
			if !hasPath(ee.Errors, "input") {
				return "", fmt.Errorf("%s: no error on input: %+v", in, ee.Errors)
			}
		}
		return "", nil
	})

	r.check(1, "probe: a loopback address is reported, not reached", func() (string, error) {
		p, err := r.c.Probe(ctx, engine.ProbeRequest{Input: "http://127.0.0.1:8080/live"})
		var ee *engine.Error
		if errors.As(err, &ee) && ee.Status == 422 {
			return "refused with 422", nil
		}
		if err != nil {
			return "", err
		}
		if p.OK || len(p.Problems) == 0 {
			return "", fmt.Errorf("probe ok=%v problems=%v", p.OK, p.Problems)
		}
		return p.Problems[0].Code, nil
	})

	r.check(1, "probe: a reachable stream reports codec and rate", func() (string, error) {
		if r.opt.Input == "" {
			return "", needInput
		}
		p, err := r.c.Probe(ctx, engine.ProbeRequest{Input: r.input(5)})
		if err != nil {
			return "", err
		}
		if !p.OK || p.Codec == "" || p.SampleRate == 0 {
			return "", fmt.Errorf("ok=%v codec=%q rate=%d problems=%v", p.OK, p.Codec, p.SampleRate, p.Problems)
		}
		return fmt.Sprintf("%s, %d Hz, %d kbit/s", p.Codec, p.SampleRate, p.BitrateKbps), nil
	})

	s1 := r.id("s", 1)
	in1 := r.input(0)
	if in1 == "" {
		in1 = "https://stream.example.org/conformance-" + r.opt.Prefix
	}
	r.check(1, "streams: PUT twice is the same, and the stream is listed", func() (string, error) {
		sp := engine.StreamSpec{Input: in1, SampleRate: r.opt.SampleRate, Mode: engine.OnDemand}
		a, err := r.putStream(s1, sp)
		if err != nil {
			return "", err
		}
		b, err := r.putStream(s1, sp)
		if err != nil {
			return "", fmt.Errorf("second PUT: %v", err)
		}
		if a.ID != s1 || b.ID != s1 || b.SampleRate != r.opt.SampleRate || b.Mode != engine.OnDemand {
			return "", fmt.Errorf("answers: %+v / %+v", a, b)
		}
		list, err := r.c.Streams(ctx)
		if err != nil {
			return "", err
		}
		if !slices.ContainsFunc(list, func(s engine.Stream) bool { return s.ID == s1 }) {
			return "", errors.New("not in GET /v1/streams")
		}
		got, err := r.c.Stream(ctx, s1)
		if err != nil || got.ID != s1 {
			return "", fmt.Errorf("GET: %v %+v", err, got)
		}
		return "state " + got.State, nil
	})

	r.check(1, "streams: input and sample_rate cannot change (409 immutable)", func() (string, error) {
		_, err := r.c.PutStream(ctx, s1, engine.StreamSpec{Input: in1, SampleRate: r.opt.SampleRate + 1000, Mode: engine.OnDemand})
		return "", wantErr(err, 409, "immutable")
	})

	r.check(1, "streams: one address is one stream (409 duplicate_input)", func() (string, error) {
		_, err := r.putStream(r.id("s", 8), engine.StreamSpec{Input: in1, SampleRate: r.opt.SampleRate})
		return "", wantErr(err, 409, "duplicate_input")
	})

	r.check(1, "validate: good settings give effective values and rule text", func() (string, error) {
		minHz, maxHz := 100.0, 5000.0
		v, err := r.c.Validate(ctx, engine.ValidateRequest{SampleRate: r.opt.SampleRate, AnalysisSpec: engine.AnalysisSpec{
			Settings: engine.Settings{MinHz: &minHz, MaxHz: &maxHz},
			Rules:    []engine.Rule{{Kind: engine.RuleComb, Hz: 50}},
		}})
		if err != nil {
			return "", err
		}
		if !v.Valid || v.Effective == nil || v.Effective.MinHz != minHz || len(v.Rules) != 1 || v.Rules[0].Text == "" {
			return "", fmt.Errorf("answer %+v", v)
		}
		return "rule " + v.Rules[0].Text, nil
	})

	r.check(1, "validate: a negative min_hz is refused at settings.min_hz", func() (string, error) {
		bad := -5.0
		v, err := r.c.Validate(ctx, engine.ValidateRequest{SampleRate: r.opt.SampleRate, AnalysisSpec: engine.AnalysisSpec{Settings: engine.Settings{MinHz: &bad}}})
		if err != nil {
			return "", err
		}
		if v.Valid || !hasPath(v.Errors, "settings.min_hz") {
			return "", fmt.Errorf("answer %+v", v)
		}
		return "", nil
	})

	a1 := r.id("a", 1)
	r.check(1, "analyses: PUT returns effective values and rule text; GET the same", func() (string, error) {
		a, err := r.c.PutAnalysis(ctx, s1, a1, spec(map[string]any{
			"settings": map[string]any{"min_hz": 50},
			"rules":    []any{map[string]any{"kind": "line", "hz": 1000}},
			"memory":   map[string]any{"mode": "disk"},
			"label":    "conformance",
		}))
		if err != nil {
			return "", err
		}
		if a.ID != a1 || a.Effective.SampleRate != r.opt.SampleRate || a.Effective.MinHz != 50 || len(a.Rules) != 1 || a.Rules[0].Text == "" || a.Memory.Key == "" {
			return "", fmt.Errorf("answer %+v", a)
		}
		g, err := r.c.Analysis(ctx, s1, a1)
		if err != nil || g.Label != "conformance" || g.Memory.Key != a.Memory.Key {
			return "", fmt.Errorf("GET: %v %+v", err, g)
		}
		return "memory key " + a.Memory.Key, nil
	})

	r.check(1, "analyses: refused settings answer 422 invalid with a path", func() (string, error) {
		_, err := r.c.PutAnalysis(ctx, s1, r.id("a", 9), spec(map[string]any{"settings": map[string]any{"min_hz": -1}}))
		if e := wantErr(err, 422, "invalid"); e != nil {
			return "", e
		}
		var ee *engine.Error
		errors.As(err, &ee)
		if !hasPath(ee.Errors, "settings.min_hz") {
			return "", fmt.Errorf("errors %+v", ee.Errors)
		}
		return "", nil
	})

	r.check(1, "live: a subscription connects an on-demand stream and spectra arrive", func() (string, error) {
		if r.opt.Input == "" {
			return "", needInput
		}
		err := r.spectrum(s1, a1, func(p map[string]any) error {
			bands, _ := p["bands"].([]any)
			if len(bands) == 0 {
				return errors.New("signal.spectrum without bands")
			}
			return nil
		})
		return "", err
	})

	r.check(1, "data: state, motifs, history and memories answer JSON", func() (string, error) {
		for _, sub := range []string{"state", "motifs", "history/events", "history/features", "memories"} {
			q := url.Values{}
			if strings.HasPrefix(sub, "history/") {
				q.Set("limit", "5")
			}
			raw, err := r.c.Data(ctx, s1, a1, sub, q)
			if err != nil {
				return "", fmt.Errorf("%s: %v", sub, err)
			}
			if !json.Valid(raw) {
				return "", fmt.Errorf("%s: not JSON", sub)
			}
		}
		return "", nil
	})

	r.check(1, "events: a continuous stream with an analysis reports its changes as stream.state", func() (string, error) {
		s7 := r.id("s", 7)
		in := r.input(7)
		if in == "" {
			in = "https://stream.example.org/conformance-events-" + r.opt.Prefix
		}
		started := make(chan struct{})
		go func() {
			<-started
			time.Sleep(200 * time.Millisecond)
			// A stream connects only once it has an analysis.
			if _, err := r.putStream(s7, engine.StreamSpec{Input: in, SampleRate: r.opt.SampleRate, Mode: engine.Continuous}); err == nil {
				r.c.PutAnalysis(ctx, s7, r.id("a", 7), spec(map[string]any{}))
			}
		}()
		var once sync.Once
		return "", r.await(r.c.Changes, func(ev engine.SSE) bool {
			once.Do(func() { close(started) })
			if ev.Event != "stream.state" {
				return false
			}
			var st struct {
				ID string `json:"id"`
			}
			json.Unmarshal(ev.Data, &st)
			return st.ID == s7
		})
	})

	r.check(1, "failure: an analysis fails alone and a PUT starts it again", func() (string, error) {
		if r.opt.Fail == nil {
			return "", skipped("this engine offers no way to make an analysis fail on purpose")
		}
		if !r.opt.Fail(s1, a1) {
			return "", errors.New("could not make it fail")
		}
		a, err := r.c.Analysis(ctx, s1, a1)
		if err != nil {
			return "", err
		}
		if a.State != engine.AnalysisFailed || a.LastError == nil || a.LastError.Code == "" || a.LastError.Message == "" {
			return "", fmt.Errorf("after failing: state %q, last_error %+v", a.State, a.LastError)
		}
		if _, err := r.c.Stream(ctx, s1); err != nil {
			return "", fmt.Errorf("the stream went with it: %v", err)
		}
		b, err := r.c.PutAnalysis(ctx, s1, a1, spec(map[string]any{
			"settings": map[string]any{"min_hz": 50},
			"rules":    []any{map[string]any{"kind": "line", "hz": 1000}},
			"memory":   map[string]any{"mode": "disk"},
			"label":    "conformance",
		}))
		if err != nil {
			return "", err
		}
		if b.State == engine.AnalysisFailed || b.LastError != nil {
			return "", fmt.Errorf("after the PUT: state %q, last_error %+v", b.State, b.LastError)
		}
		return "state after PUT " + b.State, nil
	})

	r.check(1, "deleting: an analysis, then its stream, are gone (404 not_found)", func() (string, error) {
		s6, a6 := r.id("s", 6), r.id("a", 6)
		in := r.input(6)
		if in == "" {
			in = "https://stream.example.org/conformance-delete-" + r.opt.Prefix
		}
		if _, err := r.putStream(s6, engine.StreamSpec{Input: in, SampleRate: r.opt.SampleRate}); err != nil {
			return "", err
		}
		if _, err := r.c.PutAnalysis(ctx, s6, a6, spec(map[string]any{})); err != nil {
			return "", err
		}
		if err := r.c.DeleteAnalysis(ctx, s6, a6, true); err != nil {
			return "", err
		}
		if _, err := r.c.Analysis(ctx, s6, a6); wantErr(err, 404, "not_found") != nil {
			return "", fmt.Errorf("analysis after DELETE: %v", err)
		}
		if err := r.c.DeleteStream(ctx, s6, true); err != nil {
			return "", err
		}
		if _, err := r.c.Stream(ctx, s6); wantErr(err, 404, "not_found") != nil {
			return "", fmt.Errorf("stream after DELETE: %v", err)
		}
		return "", nil
	})
}

func (r *run) phase2() {
	ctx := r.ctx
	needInput := skipped("needs -input, a stream the engine may connect to")
	s2 := r.id("s", 2)
	in2 := r.input(2)
	if in2 == "" {
		in2 = "https://stream.example.org/conformance-two-" + r.opt.Prefix
	}

	r.check(2, "analyses: two on one stream, both receive spectra", func() (string, error) {
		if _, err := r.putStream(s2, engine.StreamSpec{Input: in2, SampleRate: r.opt.SampleRate, Mode: engine.Continuous}); err != nil {
			return "", err
		}
		for i, set := range []map[string]any{{"min_hz": 50}, {"min_hz": 400}} {
			if _, err := r.c.PutAnalysis(ctx, s2, r.id("a", 20+i), spec(map[string]any{"settings": set})); err != nil {
				return "", err
			}
		}
		st, err := r.c.Stream(ctx, s2)
		if err != nil {
			return "", err
		}
		if len(st.Analyses) != 2 {
			return "", fmt.Errorf("stream lists analyses %v", st.Analyses)
		}
		if r.opt.Input == "" {
			return "", needInput
		}
		for i := range 2 {
			if err := r.spectrum(s2, r.id("a", 20+i), nil); err != nil {
				return "", fmt.Errorf("%s: %v", r.id("a", 20+i), err)
			}
		}
		return "", nil
	})

	r.check(2, "trials: an analysis with memory mode ram runs next to another", func() (string, error) {
		t := r.id("t", 2)
		a, err := r.c.PutAnalysis(ctx, s2, t, spec(map[string]any{"settings": map[string]any{"min_hz": 80}, "memory": map[string]any{"mode": "ram"}}))
		if err != nil {
			return "", err
		}
		if a.Memory.Mode != engine.MemoryRAM {
			return "", fmt.Errorf("memory mode %q", a.Memory.Mode)
		}
		if err := r.c.DeleteAnalysis(ctx, s2, t, true); err != nil {
			return "", err
		}
		return "", nil
	})

	r.check(2, "usage: reported for the engine, its host and streams", func() (string, error) {
		i, err := r.c.Info(ctx)
		if err != nil {
			return "", err
		}
		if i.Host.Cores < 1 {
			return "", fmt.Errorf("host %+v", i.Host)
		}
		var got struct {
			Usage *engine.Usage `json:"usage"`
		}
		st, err := r.c.Stream(ctx, s2)
		if err != nil {
			return "", err
		}
		b, _ := json.Marshal(st)
		json.Unmarshal(b, &got)
		if got.Usage == nil {
			return "", errors.New("stream without usage")
		}
		return fmt.Sprintf("host %d cores, %d MB; engine %.1f %% CPU, %.0f MB", i.Host.Cores, i.Host.MemoryMB, i.Usage.CPUPercent, i.Usage.MemoryMB), nil
	})

	r.check(2, "view: put, read back with effective values, changed on a running stream", func() (string, error) {
		bands := 120
		st, err := r.putStream(s2, engine.StreamSpec{Input: in2, SampleRate: r.opt.SampleRate, Mode: engine.Continuous,
			View: &engine.ViewSpec{Scale: engine.ScaleLinear, Bands: &bands}})
		if err != nil {
			return "", err
		}
		v := st.View
		if v == nil || v.Scale != engine.ScaleLinear || v.Bands != bands || v.MinHz != 0 || v.MaxHz <= 0 || v.MaxHz > float64(r.opt.SampleRate)/2 {
			return "", fmt.Errorf("view %+v", v)
		}
		got, err := r.c.Stream(ctx, s2)
		if err != nil || got.View == nil || *got.View != *v {
			return "", fmt.Errorf("GET: %v %+v", err, got.View)
		}
		return fmt.Sprintf("linear %g–%g Hz, %d bands", v.MinHz, v.MaxHz, v.Bands), nil
	})

	r.check(2, "view: a bad scale is refused (422, view.scale)", func() (string, error) {
		_, err := r.c.PutStream(ctx, s2, engine.StreamSpec{Input: in2, SampleRate: r.opt.SampleRate, Mode: engine.Continuous,
			View: &engine.ViewSpec{Scale: "cubic"}})
		if e := wantErr(err, 422, "invalid"); e != nil {
			return "", e
		}
		var ee *engine.Error
		errors.As(err, &ee)
		if !hasPath(ee.Errors, "view.scale") {
			return "", fmt.Errorf("errors %+v", ee.Errors)
		}
		return "", nil
	})

	r.check(2, "view: spectra carry payload.view with the stream's bands", func() (string, error) {
		if r.opt.Input == "" {
			return "", needInput
		}
		return "", r.spectrum(s2, r.id("a", 20), func(p map[string]any) error {
			v, ok := p["view"].(map[string]any)
			if !ok {
				return errors.New("signal.spectrum without payload.view")
			}
			bands, _ := v["bands"].([]any)
			if v["scale"] != engine.ScaleLinear || len(bands) != 120 {
				return fmt.Errorf("payload.view scale %v with %d bands; want linear, 120", v["scale"], len(bands))
			}
			return nil
		})
	})
}

func (r *run) phase3() {
	ctx := r.ctx
	if r.info.API.Major == 0 {
		r.info, _ = r.c.Info(ctx)
	}
	if !r.info.Groups() {
		r.check(3, "groups", func() (string, error) { return "", skipped("the engine reports no groups (control API 1.1)") })
		return
	}
	s1, s2 := r.id("s", 1), r.id("s", 2)
	a1, a2 := r.id("a", 1), r.id("a", 20)
	g := r.id("g", 1)
	r.check(3, "groups: PUT a group over two analyses and GET it", func() (string, error) {
		if _, err := r.c.Analysis(ctx, s2, a2); err != nil {
			return "", skipped("needs the phase 2 stream; run with -phase 3 after phase 2 passes")
		}
		lat1, lon1, lat2, lon2 := 46.0, 7.0, 45.0, 8.0
		gr, err := r.c.PutGroup(ctx, g, engine.GroupSpec{
			Members: []engine.GroupMember{
				{Stream: s1, Analysis: a1, Lat: &lat1, Lon: &lon1},
				{Stream: s2, Analysis: a2, Lat: &lat2, Lon: &lon2},
			},
			Analyses: []string{"activity"},
		})
		if err != nil {
			return "", err
		}
		r.groups = append(r.groups, g)
		if gr.ID != g || len(gr.Members) != 2 {
			return "", fmt.Errorf("answer %+v", gr)
		}
		list, err := r.c.Groups(ctx)
		if err != nil || !slices.ContainsFunc(list, func(x engine.Group) bool { return x.ID == g }) {
			return "", fmt.Errorf("not listed: %v", err)
		}
		return "state " + gr.State, nil
	})

	r.check(3, "groups: state and history answer JSON", func() (string, error) {
		for _, sub := range []string{"state", "history/events"} {
			raw, err := r.c.GroupData(ctx, g, sub, nil)
			if err != nil {
				return "", fmt.Errorf("%s: %v", sub, err)
			}
			if !json.Valid(raw) {
				return "", fmt.Errorf("%s: not JSON", sub)
			}
		}
		return "", nil
	})

	r.check(3, "groups: group.activity arrives", func() (string, error) {
		if r.opt.Input == "" {
			return "", skipped("needs -input, a stream the engine may connect to")
		}
		return "", r.await(func(ctx context.Context, fn func(engine.SSE)) error {
			return r.c.SubscribeGroup(ctx, g, fn, "group.activity")
		}, func(ev engine.SSE) bool { return ev.Event == "group.activity" })
	})
}
