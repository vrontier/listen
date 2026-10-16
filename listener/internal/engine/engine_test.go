package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vrontier/listen/listener/internal/fetch"
	"github.com/vrontier/listen/listener/internal/ingest"
)

var _ = http.StatusOK

func TestAddressPolicy(t *testing.T) {
	p := fetch.NewPolicy(false, false)
	p.Own = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("203.0.113.7")} }
	p.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "public.example":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		case "sneaky.example": // a public name for a private address
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, nil
		case "self.example":
			return []netip.Addr{netip.MustParseAddr("203.0.113.7")}, nil
		}
		return nil, errors.New("no such host")
	}
	cases := []struct{ in, want string }{
		{"https://public.example/live.ogg", ""},
		{"http://public.example:8000/stream", ""},
		{"http://sneaky.example/s", "address_not_allowed"},
		{"http://self.example/s", "address_not_allowed"},
		{"http://127.0.0.1:8104/ws/audio", "address_not_allowed"},
		{"http://[::1]/s", "address_not_allowed"},
		{"http://169.254.169.254/latest/meta-data", "address_not_allowed"},
		{"http://100.64.0.1/s", "address_not_allowed"},
		{"http://[::ffff:10.0.0.1]/s", "address_not_allowed"},
		{"http://localhost/s", "address_not_allowed"},
		{"http://printer.local/s", "address_not_allowed"},
		{"http://unknown.example/s", "no_answer"},
		{"ftp://public.example/a", "protocol_not_allowed"},
		{"file:///etc/passwd", "protocol_not_allowed"},
		{"/etc/passwd", "protocol_not_allowed"},
		{"concat:a|b", "protocol_not_allowed"},
		{"http://user:pw@public.example/", "bad_address"},
		{"http:///nohost", "bad_address"},
	}
	for _, c := range cases {
		got, _ := p.Check(context.Background(), c.in)
		code := ""
		if got != nil {
			code = got.Code
		}
		if code != c.want {
			t.Errorf("%s: got %q, want %q", c.in, code, c.want)
		}
	}
	// A host the operator allows passes, private or not; others stay refused.
	q := fetch.NewPolicy(false, false, "127.0.0.1:8013")
	q.Own = p.Own
	if pr, _ := q.Check(context.Background(), "http://127.0.0.1:8013/test.ogg"); pr != nil {
		t.Errorf("allowed host refused: %v", pr)
	}
	if pr, _ := q.Check(context.Background(), "http://127.0.0.1:8080/live"); pr == nil {
		t.Errorf("other port of an allowed host accepted")
	}

	// Without a lookup (resuming), a name passes; addresses are still checked.
	// (A stream whose host does not resolve is accepted at the PUT as well;
	// see TestUnresolvedHostAccepted.)
	if pr := p.CheckStatic("http://unknown.example/s"); pr != nil {
		t.Errorf("static check of a name: %v", pr)
	}
	if pr := p.CheckStatic("http://10.1.2.3/s"); pr == nil || pr.Code != "address_not_allowed" {
		t.Errorf("static check of a private address: %v", pr)
	}
}

func TestCheckAnalysis(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	c, errs, warns := checkAnalysis(32000, AnalysisSpec{}, DefaultLimits)
	if len(errs) > 0 || len(warns) > 0 {
		t.Fatalf("empty spec: %v %v", errs, warns)
	}
	if e := c.effective(); e.MinHz != 20 || e.MaxHz != 15964 || e.PeakMinHz != 40 || e.PeakMaxHz != 8707 || e.BinHz != 7.81 {
		t.Errorf("defaults at 32 kHz: %+v", e)
	}
	spec := AnalysisSpec{
		Settings: Settings{MinHz: f(800), MaxHz: f(12000)},
		Rules:    []Rule{{Kind: RuleComb, Hz: 16.7, Odd: true, TolHz: f(4), DriftPercent: f(0.4)}},
	}
	c, errs, _ = checkAnalysis(32000, spec, DefaultLimits)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	if got := c.spec.Rules[0].Text; got != "comb=16.7,odd,tol=4,drift=0.4" {
		t.Errorf("rule text %q", got)
	}
	k1 := c.memoryKey()
	spec.Rules[0].DriftPercent = f(0.5)
	c2, _, _ := checkAnalysis(32000, spec, DefaultLimits)
	if c2.memoryKey() == k1 || !validKey(k1) || !validKey(c2.memoryKey()) {
		t.Errorf("memory keys %q %q", k1, c2.memoryKey())
	}
	bad := AnalysisSpec{
		Settings: Settings{MinHz: f(9000), MaxHz: f(12000), TransientK: f(0)},
		Rules:    []Rule{{Kind: "notch", Hz: 50}, {Kind: RuleLine, Hz: 1000, Odd: true}, {Kind: RuleLine, Hz: -1}},
		Memory:   MemorySpec{Mode: "tape"},
	}
	_, errs, _ = checkAnalysis(32000, bad, DefaultLimits)
	paths := map[string]bool{}
	for _, e := range errs {
		paths[e.Path] = true
	}
	for _, p := range []string{"settings.min_hz", "settings.transient_k", "rules[0].kind", "rules[1].odd", "rules[2].hz", "memory.mode"} {
		if !paths[p] {
			t.Errorf("no error at %s: %v", p, errs)
		}
	}
	if _, errs, _ := checkAnalysis(32000, AnalysisSpec{Settings: Settings{MaxHz: f(16000)}}, DefaultLimits); len(errs) != 1 {
		t.Errorf("max_hz above the top: %v", errs)
	}
}

func TestCheckView(t *testing.T) {
	v, fe := checkView(&ViewSpec{Scale: "linear"}, 32000)
	if len(fe) > 0 || *v != (View{"linear", 0, 16000, 240}) {
		t.Errorf("linear defaults: %+v %v", v, fe)
	}
	v, fe = checkView(&ViewSpec{Scale: "log"}, 22050)
	if len(fe) > 0 || v.MinHz != 20 || v.MaxHz != 11025 {
		t.Errorf("log defaults: %+v %v", v, fe)
	}
	zero, big := 0.0, 20000.0
	if _, fe := checkView(&ViewSpec{Scale: "log", MinHz: &zero, MaxHz: &big}, 32000); len(fe) != 2 {
		t.Errorf("bad log view: %v", fe)
	}
}

// --- a running engine on a file ---

func needFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
}

// writeWAV writes seconds of a test signal: noise, a steady 1 kHz line and a
// click every second, mono 16-bit at rate.
func writeWAV(t *testing.T, path string, rate, seconds int) {
	n := rate * seconds
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + 2*n))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(1))
	w(uint32(rate))
	w(uint32(2 * rate))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(2 * n))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < n; i++ {
		v := 0.05*rng.NormFloat64() + 0.2*math.Sin(2*math.Pi*1000*float64(i)/float64(rate))
		if i%rate < rate/200 {
			v += 0.8 * rng.NormFloat64()
		}
		w(int16(math.Max(-1, math.Min(1, v)) * 32000))
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

type client struct {
	t    *testing.T
	base string
}

func (c client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, c.base+path, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// sse reads events from path until n of type typ arrived or the time is up.
func (c client) sse(ctx context.Context, path, typ string, n int) []map[string]any {
	c.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "GET", c.base+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []map[string]any
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<16), 1<<22)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var env map[string]any
		json.Unmarshal([]byte(line[6:]), &env)
		if env["type"] == typ {
			out = append(out, env)
			if len(out) >= n {
				break
			}
		}
	}
	return out
}

func startEngine(t *testing.T, dir string) (*Engine, client) {
	t.Helper()
	e, cs := startCallers(t, dir, DefaultCaller)
	return e, cs[0]
}

// startCallers starts an engine with the named callers, each on a test
// server of its own, as each would have a socket of its own.
func startCallers(t *testing.T, dir string, names ...string) (*Engine, []client) {
	t.Helper()
	e, err := New(Options{MemoryDir: filepath.Join(dir, "memory"), StateFile: filepath.Join(dir, "state.json"), AllowFiles: true, Callers: names})
	if err != nil {
		t.Fatal(err)
	}
	var cs []client
	var srvs []*httptest.Server
	for _, n := range names {
		srv := httptest.NewServer(e.Caller(n).Handler())
		srvs = append(srvs, srv)
		cs = append(cs, client{t, srv.URL})
	}
	t.Cleanup(func() {
		e.Close()
		for _, s := range srvs {
			s.Close()
		}
	})
	return e, cs
}

func waitFor(t *testing.T, what string, f func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestStreamAndAnalyses(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "test.wav")
	writeWAV(t, wav, 22050, 20)
	e, c := startEngine(t, dir)

	idle := 1
	var sv StreamView
	if st := c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: wav, SampleRate: 22050, IdleAfterS: &idle, View: &ViewSpec{Scale: "linear"}}, &sv); st != 200 || sv.State != StateIdle {
		t.Fatalf("PUT stream: %d %+v", st, sv)
	}
	var av AnalysisView
	if st := c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{"label": "rev-1"}, &av); st != 200 || av.Memory.Key == "" {
		t.Fatalf("PUT analysis: %d %+v", st, av)
	}

	// A subscription connects the on-demand stream.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	spectra := c.sse(ctx, "/v1/streams/s-1/analyses/a-1/events?types=signal.spectrum", "signal.spectrum", 20)
	if len(spectra) < 20 {
		t.Fatalf("got %d spectra", len(spectra))
	}
	last := spectra[len(spectra)-1]
	payload := last["payload"].(map[string]any)
	view, _ := payload["view"].(map[string]any)
	if last["source"] != "s-1/a-1" || view == nil || len(view["bands"].([]any)) != 240 {
		t.Fatalf("spectrum: source %v, view %v", last["source"], view != nil)
	}

	// A second analysis, added while the stream runs (continuous now), lies
	// on the same timeline.
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: wav, SampleRate: 22050, Mode: Continuous}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/a-2", map[string]any{"settings": map[string]any{"min_hz": 500}}, nil)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	s1 := c.sse(ctx2, "/v1/streams/s-1/analyses/a-1/events?types=signal.spectrum", "signal.spectrum", 3)
	s2 := c.sse(ctx2, "/v1/streams/s-1/analyses/a-2/events?types=signal.spectrum", "signal.spectrum", 3)
	if len(s1) < 3 || len(s2) < 3 {
		t.Fatalf("spectra %d %d", len(s1), len(s2))
	}
	p1, p2 := s1[2]["position_s"].(float64), s2[2]["position_s"].(float64)
	if math.Abs(p1-p2) > 2 || p2 < 1 {
		t.Errorf("positions not on one timeline: a-1 %.2f, a-2 %.2f", p1, p2)
	}

	// A change of range switches the memory; the old one is listed.
	var av2 AnalysisView
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{"settings": map[string]any{"min_hz": 300}}, &av2)
	if av2.Memory.Key == av.Memory.Key {
		t.Errorf("memory key unchanged: %s", av2.Memory.Key)
	}
	var mems struct{ Memories []MemoryInfo }
	c.do("GET", "/v1/streams/s-1/analyses/a-1/memories", nil, &mems)
	if len(mems.Memories) != 2 {
		t.Errorf("memories: %+v", mems.Memories)
	}
	var er map[string]map[string]any
	if st := c.do("DELETE", "/v1/streams/s-1/analyses/a-1/memories/"+av2.Memory.Key, nil, &er); st != 409 || er["error"]["code"] != "in_use" {
		t.Errorf("deleting the memory in use: %d %v", st, er)
	}
	if st := c.do("DELETE", "/v1/streams/s-1/analyses/a-1/memories/"+av.Memory.Key, nil, nil); st != 204 {
		t.Errorf("deleting an old memory: %d", st)
	}

	// The snapshot carries the analysis's latest spectrum (it starts empty
	// after the change of range).
	waitFor(t, "a spectrum in the snapshot", func() bool {
		var snap map[string]json.RawMessage
		c.do("GET", "/v1/streams/s-1/analyses/a-1/state", nil, &snap)
		return len(snap["spectrum"]) > 0 && string(snap["spectrum"]) != "null"
	})
	_ = e
}

func TestFailedAnalysisStaysAlone(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "test.wav")
	writeWAV(t, wav, 22050, 20)
	e, c := startEngine(t, dir)
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: wav, SampleRate: 22050, Mode: Continuous}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/good", map[string]any{}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/bad", map[string]any{}, nil)
	inject := func(r *run) {
		if r.id == "bad" {
			panic("injected")
		}
	}
	beforeFeed.Store(&inject)
	defer beforeFeed.Store(nil)

	var av AnalysisView
	waitFor(t, "the bad analysis to fail", func() bool {
		c.do("GET", "/v1/streams/s-1/analyses/bad", nil, &av)
		return av.State == AnalysisFailed
	})
	if av.LastError == nil || av.LastError.Code != "analysis_failed" {
		t.Errorf("last_error: %+v", av.LastError)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if got := c.sse(ctx, "/v1/streams/s-1/analyses/good/events", "signal.spectrum", 5); len(got) < 5 {
		t.Fatalf("the good analysis stopped too: %d spectra", len(got))
	}
	var sv StreamView
	c.do("GET", "/v1/streams/s-1", nil, &sv)
	if sv.State != StateConnected {
		t.Errorf("stream %s", sv.State)
	}

	// A PUT starts it again.
	beforeFeed.Store(nil)
	c.do("PUT", "/v1/streams/s-1/analyses/bad", map[string]any{}, &av)
	waitFor(t, "the analysis to run again", func() bool {
		var now AnalysisView // fresh: last_error is left out once cleared
		c.do("GET", "/v1/streams/s-1/analyses/bad", nil, &now)
		return now.State == AnalysisRunning && now.LastError == nil
	})
	_ = e
}

func TestResume(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "test.wav")
	writeWAV(t, wav, 22050, 5)
	e, err := New(Options{MemoryDir: filepath.Join(dir, "memory"), StateFile: filepath.Join(dir, "state.json"), AllowFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Caller(DefaultCaller).Handler())
	c := client{t, srv.URL}
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: wav, SampleRate: 22050, Mode: Continuous}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{"label": "rev-9", "settings": map[string]any{"min_hz": 200}}, nil)
	e.Close()
	srv.Close()

	e2, c2 := startEngine(t, dir)
	if !e2.Caller(DefaultCaller).resumed {
		t.Fatal("not resumed")
	}
	var av AnalysisView
	if st := c2.do("GET", "/v1/streams/s-1/analyses/a-1", nil, &av); st != 200 || av.Label != "rev-9" || av.Effective.MinHz != 200 {
		t.Fatalf("resumed analysis: %d %+v", st, av)
	}
	var info map[string]any
	c2.do("GET", "/v1/engine", nil, &info)
	if info["resumed"] != true {
		t.Errorf("engine info: resumed %v", info["resumed"])
	}
}

func TestProbeFile(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "test.wav")
	writeWAV(t, wav, 22050, 3)
	_, c := startEngine(t, dir)
	var res ProbeResult
	c.do("POST", "/v1/probes", ProbeRequest{Input: wav, ListenS: 2}, &res)
	if !res.OK || res.Codec != "pcm_s16le" || res.SampleRate != 22050 || res.Channels != 1 || res.Silent {
		t.Errorf("probe: %+v", res)
	}
	c.do("POST", "/v1/probes", ProbeRequest{Input: "http://127.0.0.1:9/x"}, &res)
	if res.OK || len(res.Problems) == 0 || res.Problems[0].Code != "address_not_allowed" {
		t.Errorf("probe of a loopback address: %+v", res)
	}
}

func TestGroupActivity(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	w1, w2 := filepath.Join(dir, "one.wav"), filepath.Join(dir, "two.wav")
	writeWAV(t, w1, 22050, 20)
	writeWAV(t, w2, 22050, 20)
	e, c := startEngine(t, dir)
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: w1, SampleRate: 22050}, nil)
	c.do("PUT", "/v1/streams/s-2", StreamSpec{Input: w2, SampleRate: 22050}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	c.do("PUT", "/v1/streams/s-2/analyses/a-2", map[string]any{}, nil)

	var info map[string]any
	c.do("GET", "/v1/engine", nil, &info)
	if api := info["api"].(map[string]any); api["minor"].(float64) != 1 {
		t.Fatalf("api %v", api)
	}

	var er map[string]map[string]any
	if st := c.do("PUT", "/v1/groups/g-1", GroupSpec{Members: []GroupMember{{Stream: "s-1", Analysis: "a-1"}}}, &er); st != 422 {
		t.Errorf("a group of one: %d", st)
	}
	if st := c.do("PUT", "/v1/groups/g-1", GroupSpec{
		Members: []GroupMember{{Stream: "s-1", Analysis: "a-1"}, {Stream: "s-2", Analysis: "a-2"}}, Analyses: []string{"clock"},
	}, &er); st != 422 || er["error"]["errors"].([]any)[0].(map[string]any)["code"] != "needs_station_time" {
		t.Errorf("clock without station time: %d %v", st, er)
	}
	lat, lon := 45.0, 7.5
	var gv map[string]any
	if st := c.do("PUT", "/v1/groups/g-1", GroupSpec{
		Members: []GroupMember{{Stream: "s-1", Analysis: "a-1", Lat: &lat, Lon: &lon}, {Stream: "s-2", Analysis: "a-2"}}, Label: "rev-1",
	}, &gv); st != 200 || gv["state"] != "idle" {
		t.Fatalf("PUT group: %d %v", st, gv)
	}

	// A subscription to the group connects both on-demand streams.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// The first seconds come before the streams have connected: gaps.
	acts := c.sse(ctx, "/v1/groups/g-1/events?types=group.activity", "group.activity", 12)
	if len(acts) < 12 {
		t.Fatalf("got %d activity events", len(acts))
	}
	last := acts[len(acts)-1]
	if last["source"] != "g-1" || last["time_source"] != "wall" || last["label"] != "rev-1" {
		t.Errorf("envelope %v", last)
	}
	both := 0
	for _, ev := range acts {
		present := 0
		for _, m := range ev["payload"].(map[string]any)["members"].([]any) {
			mm := m.(map[string]any)
			if mm["present"] == true {
				present++
				if _, ok := mm["flux"]; !ok {
					t.Errorf("present member without flux: %v", mm)
				}
			}
		}
		if present == 2 {
			both++
		}
	}
	if both < 5 {
		t.Errorf("seconds with both members heard: %d of %d", both, len(acts))
	}

	var state map[string]json.RawMessage
	c.do("GET", "/v1/groups/g-1/state", nil, &state)
	if string(state["status"]) == "null" || len(state["activity"]) < 10 {
		t.Errorf("state: status %s, activity %d bytes", state["status"], len(state["activity"]))
	}
	var hist struct{ Events []json.RawMessage }
	c.do("GET", "/v1/groups/g-1/history/events?type=group.activity", nil, &hist)
	if len(hist.Events) < 3 {
		t.Errorf("history: %d events", len(hist.Events))
	}

	// Deleting a member's analysis: the group goes on, the member is removed.
	c.do("DELETE", "/v1/streams/s-2/analyses/a-2", nil, nil)
	waitFor(t, "the member to be removed", func() bool {
		var g map[string]any
		c.do("GET", "/v1/groups/g-1", nil, &g)
		return g["members"].([]any)[1].(map[string]any)["state"] == MemberRemoved
	})
	if st := c.do("DELETE", "/v1/groups/g-1", nil, nil); st != 204 {
		t.Errorf("DELETE group: %d", st)
	}
	_ = e
}

func TestCorrelationFindsLag(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 8))
	n := 900
	sky := make([]float64, n+40)
	for i := range sky {
		sky[i] = rng.NormFloat64()
		if i > 0 {
			sky[i] += 0.5 * sky[i-1] // some memory, like real activity
		}
	}
	a, b := make([]float64, n), make([]float64, n)
	for k := 0; k < n; k++ {
		a[k] = sky[k+20] + 0.3*rng.NormFloat64()
		b[k] = sky[k+20-7] + 0.3*rng.NormFloat64() // b hears it 7 s later
	}
	b[100] = math.NaN() // an absent second is skipped
	r, lag, used := bestLag(a, b, n-corrWindow, corrWindow, 0)
	if lag != 7 || r < 0.8 || used < corrWindow-corrMaxLag { // late seconds have no partner yet
		t.Errorf("r %.2f at lag %d over %d", r, lag, used)
	}
	br, _, _ := bestLag(a, b, n-corrWindow-baselineShift, corrWindow, baselineShift)
	if math.Abs(br) > 0.3 {
		t.Errorf("baseline r %.2f, want near 0", br)
	}
}

func TestUnresolvedHostAccepted(t *testing.T) {
	dir := t.TempDir()
	e, c := startEngine(t, dir)
	e.policy.Resolve = func(_ context.Context, host string) ([]netip.Addr, error) {
		if host == "private.example" {
			return []netip.Addr{netip.MustParseAddr("10.0.0.9")}, nil
		}
		return nil, errors.New("no such host")
	}
	if st := c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: "https://unresolved.example/live.ogg", SampleRate: 22050}, nil); st != 200 {
		t.Errorf("unresolved host: %d, want 200", st)
	}
	var er map[string]map[string]any
	if st := c.do("PUT", "/v1/streams/s-2", StreamSpec{Input: "https://private.example/live.ogg", SampleRate: 22050}, &er); st != 422 {
		t.Errorf("host resolving to a private address: %d, want 422", st)
	}
}

func TestCallersShareOneSource(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	w1, w2 := filepath.Join(dir, "one.wav"), filepath.Join(dir, "two.wav")
	writeWAV(t, w1, 22050, 20)
	writeWAV(t, w2, 22050, 20)
	e, cs := startCallers(t, dir, "workbench", "internal")
	wb, in := cs[0], cs[1]

	// The same address from two callers: one source.
	var sv StreamView
	if st := wb.do("PUT", "/v1/streams/s-1", StreamSpec{Input: w1, SampleRate: 22050, Mode: Continuous, View: &ViewSpec{Scale: "linear"}}, &sv); st != 200 || sv.Shared != 1 {
		t.Fatalf("workbench PUT: %d %+v", st, sv)
	}
	if st := in.do("PUT", "/v1/streams/premise", StreamSpec{Input: w1, SampleRate: 22050, Mode: Continuous}, &sv); st != 200 || sv.Shared != 2 {
		t.Fatalf("internal PUT: %d shared %d", st, sv.Shared)
	}
	if n := len(e.sourceList()); n != 1 {
		t.Fatalf("%d sources for one address", n)
	}
	wb.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	in.do("PUT", "/v1/streams/premise/analyses/a", map[string]any{}, nil)

	// Namespaces: each caller sees its own only.
	var list struct{ Streams []StreamView }
	wb.do("GET", "/v1/streams", nil, &list)
	if len(list.Streams) != 1 || list.Streams[0].ID != "s-1" {
		t.Errorf("workbench sees %+v", list.Streams)
	}
	if st := in.do("GET", "/v1/streams/s-1", nil, nil); st != 404 {
		t.Errorf("internal reads the workbench's stream: %d", st)
	}

	// Both analyses get spectra from the one decode, each with its own view.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a := wb.sse(ctx, "/v1/streams/s-1/analyses/a-1/events?types=signal.spectrum", "signal.spectrum", 3)
	b := in.sse(ctx, "/v1/streams/premise/analyses/a/events?types=signal.spectrum", "signal.spectrum", 3)
	if len(a) < 3 || len(b) < 3 {
		t.Fatalf("spectra %d %d", len(a), len(b))
	}
	if a[2]["payload"].(map[string]any)["view"] == nil || b[2]["payload"].(map[string]any)["view"] != nil {
		t.Errorf("views: workbench %v, internal %v", a[2]["payload"].(map[string]any)["view"] != nil, b[2]["payload"].(map[string]any)["view"] != nil)
	}
	if src := e.sourceList()[0]; src.pid.Load() == 0 || src.shared() != 2 {
		t.Errorf("source: pid %d, shared %d", src.pid.Load(), src.shared())
	}

	// Usage: each stream an equal share, the source's full cost beside it.
	e.meter.measure()
	e.meter.measure()
	rep := e.meter.reportFor(e.Caller("workbench"))
	if u := rep.Streams["s-1"]; u.Shared != 2 || u.SourceCPUPercent == nil {
		t.Errorf("usage share %+v", u)
	}
	if _, ok := rep.Sources["s-1"]; !ok || len(rep.Streams) != 1 {
		t.Errorf("usage report %+v", rep)
	}

	// duplicate_input within a caller; rate_conflict across callers.
	var er map[string]map[string]any
	if st := wb.do("PUT", "/v1/streams/s-2", StreamSpec{Input: w1, SampleRate: 22050}, &er); st != 409 || er["error"]["code"] != "duplicate_input" {
		t.Errorf("same address twice in a caller: %d %v", st, er)
	}
	wb.do("PUT", "/v1/streams/s-3", StreamSpec{Input: w2, SampleRate: 22050}, nil)
	if st := in.do("PUT", "/v1/streams/other", StreamSpec{Input: w2, SampleRate: 16000}, &er); st != 409 || er["error"]["code"] != "rate_conflict" {
		t.Errorf("another rate on a shared source: %d %v", st, er)
	}

	// The probe of an address read already opens no connection.
	var pr ProbeResult
	in.do("POST", "/v1/probes", ProbeRequest{Input: w2}, &pr)
	if !pr.OK || !pr.Shared || pr.SampleRate != 22050 || pr.Exists != nil {
		t.Errorf("probe of a shared address: %+v", pr)
	}

	// The source stays while a stream is on it, and goes with the last.
	wb.do("DELETE", "/v1/streams/s-1", nil, nil)
	if src := e.source(w1); src == nil || src.shared() != 1 {
		t.Fatalf("source after the first stream left: %v", src)
	}
	in.do("DELETE", "/v1/streams/premise", nil, nil)
	if e.source(w1) != nil {
		t.Errorf("source still there after the last stream left")
	}
}

func TestMemoryMigratesUnderFirstCaller(t *testing.T) {
	dir := t.TempDir()
	mem := filepath.Join(dir, "memory")
	old := filepath.Join(mem, "s-1", "a-1", "r800-15964-i3280a91f")
	if err := os.MkdirAll(old, 0o750); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(old, "motifs.json"), []byte(`{"version":1}`), 0o640)
	e, err := New(Options{MemoryDir: mem, Callers: []string{"workbench", "internal"}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := os.Stat(filepath.Join(mem, "workbench", "s-1", "a-1", "r800-15964-i3280a91f", "motifs.json")); err != nil {
		t.Errorf("not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mem, ".layout")); err != nil {
		t.Errorf("no layout marker: %v", err)
	}
	if got := stateFileFor("/x/state.json", "internal", false); got != "/x/state-internal.json" {
		t.Errorf("state file %s", got)
	}
}

// writeWAVStereo44 writes a CD-quality WAV: 1411 kbit/s, like a lossless
// stream above the limit.
func writeWAVStereo44(t *testing.T, path string, seconds int) {
	const rate = 44100
	n := rate * seconds
	var b bytes.Buffer
	w := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + 4*n))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1))
	w(uint16(2))
	w(uint32(rate))
	w(uint32(4 * rate))
	w(uint16(4))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(4 * n))
	rng := rand.New(rand.NewPCG(9, 10))
	for i := 0; i < n; i++ {
		v := int16(0.2 * rng.NormFloat64() * 32000)
		w(v)
		w(v)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// serveFiles serves dir over HTTP on loopback, and allows that host:port.
func serveFiles(t *testing.T, dir string, contentType string) (*httptest.Server, string) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/redirect-to/") {
			http.Redirect(w, r, "http://"+strings.TrimPrefix(r.URL.Path, "/redirect-to/"), http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		http.ServeFile(w, r, filepath.Join(dir, filepath.Base(r.URL.Path)))
	}))
	t.Cleanup(srv.Close)
	return srv, strings.TrimPrefix(srv.URL, "http://")
}

func TestLosslessAboveLimitIsRefused(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	writeWAVStereo44(t, filepath.Join(dir, "cd.wav"), 12)
	_, host := serveFiles(t, dir, "audio/wav")
	e, err := New(Options{MemoryDir: filepath.Join(dir, "memory"), AllowHosts: []string{host}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Caller(DefaultCaller).Handler())
	defer func() { e.Close(); srv.Close() }()
	c := client{t, srv.URL}
	url := "http://" + host + "/cd.wav"

	// The probe measures what arrives: about 1411 kbit/s.
	var pr ProbeResult
	c.do("POST", "/v1/probes", ProbeRequest{Input: url, ListenS: 3}, &pr)
	if pr.OK || pr.BitrateKbps < 1300 || len(pr.Problems) == 0 || pr.Problems[0].Code != "bitrate" {
		t.Errorf("probe: %+v", pr)
	}
	if pr.ContentType == nil || *pr.ContentType != "audio/wav" {
		t.Errorf("content type %v", pr.ContentType)
	}

	// A stream of it is refused once 5 seconds of audio have shown the rate.
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: url, SampleRate: 22050, Mode: Continuous}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	waitFor(t, "the stream to be refused for its bit rate", func() bool {
		var sv StreamView
		c.do("GET", "/v1/streams/s-1", nil, &sv)
		return sv.State == StateRefused && sv.LastError != nil && sv.LastError.Code == "bitrate"
	})
}

func TestRedirectToPrivateIsRefused(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	writeWAV(t, filepath.Join(dir, "ok.wav"), 22050, 5)
	_, allowed := serveFiles(t, dir, "audio/wav")
	_, other := serveFiles(t, dir, "audio/wav") // private, not allowed
	e, err := New(Options{MemoryDir: filepath.Join(dir, "memory"), AllowHosts: []string{allowed}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Caller(DefaultCaller).Handler())
	defer func() { e.Close(); srv.Close() }()
	c := client{t, srv.URL}

	var pr ProbeResult
	c.do("POST", "/v1/probes", ProbeRequest{Input: "http://" + allowed + "/ok.wav", ListenS: 2}, &pr)
	if !pr.OK || pr.Codec != "pcm_s16le" || pr.BitrateKbps < 300 || pr.BitrateKbps > 400 {
		t.Errorf("probe of an allowed host: %+v", pr)
	}
	c.do("POST", "/v1/probes", ProbeRequest{Input: "http://" + allowed + "/redirect-to/" + other + "/ok.wav", ListenS: 2}, &pr)
	if pr.OK || len(pr.Problems) == 0 || pr.Problems[0].Code != "address_not_allowed" {
		t.Errorf("probe redirected to a private address: %+v", pr)
	}
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: "http://" + allowed + "/redirect-to/" + other + "/ok.wav", SampleRate: 22050, Mode: Continuous}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	waitFor(t, "the redirected stream to be refused", func() bool {
		var sv StreamView
		c.do("GET", "/v1/streams/s-1", nil, &sv)
		return sv.State == StateRefused && sv.LastError != nil && sv.LastError.Code == "address_not_allowed"
	})
}

func TestTestHookFailsAnAnalysis(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	wav := filepath.Join(dir, "test.wav")
	writeWAV(t, wav, 22050, 10)
	e, err := New(Options{MemoryDir: filepath.Join(dir, "memory"), AllowFiles: true, TestHooks: []string{DefaultCaller}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Caller(DefaultCaller).Handler())
	defer func() { e.Close(); srv.Close() }()
	c := client{t, srv.URL}
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: wav, SampleRate: 22050, Mode: Continuous}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	var av AnalysisView
	if st := c.do("POST", "/v1/streams/s-1/analyses/a-1/fail", nil, &av); st != 200 || av.State != AnalysisFailed {
		t.Fatalf("fail hook: %d %s", st, av.State)
	}
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	waitFor(t, "the analysis to run again", func() bool {
		var now AnalysisView
		c.do("GET", "/v1/streams/s-1/analyses/a-1", nil, &now)
		return now.State == AnalysisRunning && now.LastError == nil
	})
	// Without the hook the route does not exist.
	_, plain := startEngine(t, t.TempDir())
	if st := plain.do("POST", "/v1/streams/x/analyses/y/fail", nil, nil); st != 404 && st != 405 {
		t.Errorf("fail route without hooks: %d", st)
	}
}

func TestFirstUsageWindowIsReported(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	writeWAV(t, filepath.Join(dir, "ok.wav"), 22050, 30)
	_, host := serveFiles(t, dir, "audio/wav")
	e, err := New(Options{MemoryDir: filepath.Join(dir, "memory"), AllowHosts: []string{host}})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Caller(DefaultCaller).Handler())
	defer func() { e.Close(); srv.Close() }()
	c := client{t, srv.URL}
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: "http://" + host + "/ok.wav", SampleRate: 22050, Mode: Continuous}, nil)
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	waitFor(t, "data to arrive", func() bool { return e.source("http://"+host+"/ok.wav").bytes.Load() > 0 })

	// One measurement only: a source seen for the first time is reported
	// over its own age instead of being left out.
	e.meter.measure()
	var sv StreamView
	c.do("GET", "/v1/streams/s-1", nil, &sv)
	if sv.Usage.InKbps <= 0 || sv.Usage.InMeasured == nil || !*sv.Usage.InMeasured {
		t.Errorf("first window: %+v", sv.Usage)
	}
}

func TestThroughTheIngest(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "a.mp3")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=22050:duration=60",
		"-c:a", "libmp3lame", "-b:a", "64k", mp3).CombinedOutput(); err != nil {
		t.Skipf("no mp3 encoder: %v %s", err, out)
	}
	// The station: real time, counting its connections.
	var conns atomic.Int64
	station := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns.Add(1)
		b, _ := os.ReadFile(mp3)
		w.Header().Set("Content-Type", "audio/mpeg")
		for i := 0; i < len(b); i += 400 {
			if _, err := w.Write(b[i:min(i+400, len(b))]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	defer station.Close()
	host := strings.TrimPrefix(station.URL, "http://")

	sockDir, _ := os.MkdirTemp("/tmp", "ing")
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "s")
	startIng := func() func() {
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		in := ingest.New(ingest.Options{Policy: fetch.NewPolicy(false, false, host), IdleAfter: time.Second})
		srv := &http.Server{Handler: in.Handler()}
		go srv.Serve(ln)
		return func() { srv.Close(); in.Close(); os.Remove(sock) }
	}
	stopIng := startIng()

	e, err := New(Options{MemoryDir: filepath.Join(dir, "memory"), AllowHosts: []string{host}, Ingest: sock})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(e.Caller(DefaultCaller).Handler())
	defer func() { e.Close(); srv.Close() }()
	c := client{t, srv.URL}
	url := station.URL + "/a.mp3"

	// Another consumer of the ingest (the Observatory, on ionos) holds the
	// station; the engine's probe is answered from that connection and does
	// not tell that anyone else reads it.
	hold, _, _, err := ingest.NewClient(sock).Open(context.Background(), url, nil)
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, hold)
	var pr ProbeResult
	c.do("POST", "/v1/probes", ProbeRequest{Input: url, ListenS: 2}, &pr)
	if !pr.OK || pr.Codec != "mp3" || pr.Shared || pr.ContentType == nil || *pr.ContentType != "audio/mpeg" {
		t.Errorf("probe through the ingest: %+v", pr)
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("%d connections to the station after the probe", n)
	}

	// The engine's stream joins the same connection.
	var sv StreamView
	c.do("PUT", "/v1/streams/s-1", StreamSpec{Input: url, SampleRate: 22050, Mode: Continuous}, &sv)
	if sv.Source != "ingest" {
		t.Errorf("source %q", sv.Source)
	}
	c.do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if got := c.sse(ctx, "/v1/streams/s-1/analyses/a-1/events?types=signal.spectrum", "signal.spectrum", 5); len(got) < 5 {
		t.Fatalf("spectra through the ingest: %d", len(got))
	}
	hold.Close()

	if n := conns.Load(); n != 1 {
		t.Errorf("%d connections to the station", n)
	}

	// The ingest goes away: reconnecting, ingest_unavailable; and back.
	stopIng()
	waitFor(t, "ingest_unavailable", func() bool {
		var now StreamView
		c.do("GET", "/v1/streams/s-1", nil, &now)
		return now.State == StateReconnecting && now.LastError != nil && now.LastError.Code == "ingest_unavailable"
	})
	stopIng = startIng()
	defer stopIng()
	waitFor(t, "the stream to come back", func() bool {
		var now StreamView
		c.do("GET", "/v1/streams/s-1", nil, &now)
		return now.State == StateConnected
	})
}

func TestTwoRatesOneConnectionThroughTheIngest(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	mp3 := filepath.Join(dir, "a.mp3")
	if out, err := exec.Command("ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100:duration=60",
		"-c:a", "libmp3lame", "-b:a", "64k", mp3).CombinedOutput(); err != nil {
		t.Skipf("no mp3 encoder: %v %s", err, out)
	}
	var conns atomic.Int64
	station := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns.Add(1)
		b, _ := os.ReadFile(mp3)
		w.Header().Set("Content-Type", "audio/mpeg")
		for i := 0; i < len(b); i += 400 {
			if _, err := w.Write(b[i:min(i+400, len(b))]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	defer station.Close()
	host := strings.TrimPrefix(station.URL, "http://")
	sockDir, _ := os.MkdirTemp("/tmp", "ing")
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	in := ingest.New(ingest.Options{Policy: fetch.NewPolicy(false, false, host), IdleAfter: time.Second})
	isrv := &http.Server{Handler: in.Handler()}
	go isrv.Serve(ln)
	defer func() { isrv.Close(); in.Close() }()

	e, err := New(Options{MemoryDir: filepath.Join(dir, "memory"), AllowHosts: []string{host}, Ingest: sock, Callers: []string{"workbench", "internal"}})
	if err != nil {
		t.Fatal(err)
	}
	var cs []client
	for _, n := range []string{"workbench", "internal"} {
		srv := httptest.NewServer(e.Caller(n).Handler())
		defer srv.Close()
		cs = append(cs, client{t, srv.URL})
	}
	defer e.Close()
	url := station.URL + "/a.mp3"
	// The same station at two rates: two decodes, one connection.
	if st := cs[1].do("PUT", "/v1/streams/premise", StreamSpec{Input: url, SampleRate: 32000, Mode: Continuous}, nil); st != 200 {
		t.Fatalf("internal at 32 kHz: %d", st)
	}
	var er map[string]map[string]any
	if st := cs[0].do("PUT", "/v1/streams/s-1", StreamSpec{Input: url, SampleRate: 44100, Mode: Continuous}, &er); st != 200 {
		t.Fatalf("workbench at 44.1 kHz: %d %v", st, er)
	}
	cs[1].do("PUT", "/v1/streams/premise/analyses/a", map[string]any{}, nil)
	cs[0].do("PUT", "/v1/streams/s-1/analyses/a-1", map[string]any{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	a := cs[0].sse(ctx, "/v1/streams/s-1/analyses/a-1/events?types=signal.spectrum", "signal.spectrum", 3)
	b := cs[1].sse(ctx, "/v1/streams/premise/analyses/a/events?types=signal.spectrum", "signal.spectrum", 3)
	if len(a) < 3 || len(b) < 3 {
		t.Fatalf("spectra %d %d", len(a), len(b))
	}
	if n := len(e.sourceList()); n != 2 {
		t.Errorf("%d decodes, want 2", n)
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("%d connections to the station, want 1", n)
	}
}
