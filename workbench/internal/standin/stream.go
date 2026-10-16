package standin

import (
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"time"
)

// stream is one made-up connection and decode.
type stream struct {
	e          *Engine
	id         string
	input      string
	sampleRate int
	mode       string
	idleAfter  int
	retryMax   int

	state      string
	since      time.Time
	lastData   *time.Time
	reconnects int
	decoded    float64 // seconds of audio on the stream's timeline
	connectAt  time.Time
	lastWanted time.Time
	statusTick int

	analyses map[string]*analysis
	scene    scene

	// The stream's spectrogram view, if set: bands of its own, unfiltered,
	// computed once per tick for all its analyses.
	viewEff   *viewEff
	viewBands []float64

	// What the stream hears of the made-up sky that all streams share,
	// for the group analyses: later by its path delay, and with a weight
	// of its own.
	delay     float64 // seconds
	skyWeight float64
	skyNow    float64
	secs      []secStat // the last seconds, oldest first
	cur       secStat
	curN      int
}

// secStat is what a stream heard in one second of wall time.
type secStat struct {
	T           time.Time
	LevelDB     float64
	Flux        float64
	ImpulseRate float64
}

func newStream(e *Engine, id, input string, rate int) *stream {
	now := time.Now().UTC()
	s := &stream{e: e, id: id, input: input, sampleRate: rate, state: "idle", since: now, analyses: map[string]*analysis{}}
	// Each stream gets its own made-up scene, the same one every time the
	// stand-in starts, so that sources do not all look alike.
	h := fnv.New64a()
	h.Write([]byte(id + " " + input))
	seed := h.Sum64()
	sc := &s.scene
	sc.rng = rand.New(rand.NewPCG(seed, uint64(rate)))
	sc.mains = 50
	if sc.rng.IntN(2) == 1 {
		sc.mains = 60
	}
	sc.wanderHz = 2000 + 2500*sc.rng.Float64()
	sc.whistleHz = 1200 + 3000*sc.rng.Float64()
	sc.levelDB = -40 + 16*sc.rng.Float64()
	sc.nextClick = sc.rng.Float64() * 6
	s.delay = 4 + 14*sc.rng.Float64()
	s.skyWeight = 0.5 + 0.5*sc.rng.Float64()
	return s
}

type usage struct{ CPU, Mem, In float64 }

func (s *stream) usage() usage {
	if s.state != "connected" && s.state != "buffering" {
		return usage{}
	}
	scale := float64(s.sampleRate) / 22050
	return usage{CPU: round(1.2*scale, 1), Mem: 22, In: 128}
}

func (s *stream) subscribers() int {
	n := 0
	for _, a := range s.analyses {
		n += len(a.subs)
	}
	return n
}

// update moves the stream's state towards what is asked of it.
func (s *stream) update(now time.Time) {
	wanted := len(s.analyses) > 0 && (s.mode == "continuous" || s.subscribers() > 0 || s.e.groupWants(s.id))
	if wanted {
		s.lastWanted = now
	} else if len(s.analyses) > 0 && s.state != "idle" && now.Sub(s.lastWanted) < time.Duration(s.idleAfter)*time.Second {
		wanted = true // on demand: stay connected idle_after_s after the last subscriber
	}
	next := s.state
	switch {
	case !wanted:
		next = "idle"
	case s.state == "idle":
		next = "connecting"
		s.connectAt = now.Add(time.Second)
	case s.state == "connecting" && !now.Before(s.connectAt):
		next = "connected"
	}
	if next != s.state {
		s.state, s.since = next, now.UTC()
		s.e.notify("stream.state", s.view(false))
		for _, a := range s.analyses {
			a.stateChanged(s)
		}
	}
}

// view is the stream as GET /v1/streams/{stream} shows it.
func (s *stream) view(withInput bool) map[string]any {
	ids := make([]string, 0, len(s.analyses))
	for id := range s.analyses {
		ids = append(ids, id)
	}
	sortStrings(ids)
	u := s.usage()
	v := map[string]any{
		"id": s.id, "sample_rate": s.sampleRate, "mode": s.mode,
		"idle_after_s": s.idleAfter, "retry_max_s": s.retryMax,
		"state": s.state, "since": s.since, "last_data": s.lastData, "last_error": nil,
		"reconnects": s.reconnects, "delay_ms": 180, "analyses": ids,
		"usage": map[string]any{"cpu_percent": u.CPU, "memory_mb": u.Mem, "in_kbps": u.In, "in_measured": false},
	}
	if s.viewEff != nil {
		v["view"] = s.viewEff
	}
	if withInput {
		v["input"] = s.input
	}
	return v
}

// tick advances the made-up decode by d.
func (s *stream) tick(now time.Time, d time.Duration) {
	s.update(now)
	if s.state != "connected" {
		return
	}
	t := now.UTC()
	s.lastData = &t
	s.decoded += d.Seconds()
	s.scene.advance(s.decoded, float64(s.sampleRate)/2)
	s.hearSky(t, d)
	s.statusTick++
	status := s.statusTick%50 == 0
	s.viewBands = s.viewOf()
	for _, a := range s.analyses {
		if a.failed == nil {
			a.step(s, t, status)
		}
	}
}

// viewEff is a stream's spectrogram view as in effect.
type viewEff struct {
	Scale string  `json:"scale"`
	MinHz float64 `json:"min_hz"`
	MaxHz float64 `json:"max_hz"`
	Bands int     `json:"bands"`
}

// viewOf draws the stream's view bands from the made-up scene: the whole
// range, mains and every component included, each band taking the
// strongest component in it.
func (s *stream) viewOf() []float64 {
	v := s.viewEff
	if v == nil {
		return nil
	}
	sc := &s.scene
	bands := make([]float64, v.Bands)
	for i := range bands {
		bands[i] = 0.1 + 0.06*sc.rng.Float64()
	}
	pos := func(hz float64) float64 {
		if v.Scale == "log" {
			return (math.Log(hz) - math.Log(v.MinHz)) / (math.Log(v.MaxHz) - math.Log(v.MinHz))
		}
		return (hz - v.MinHz) / (v.MaxHz - v.MinHz)
	}
	for _, c := range sc.comps {
		if c.hz < v.MinHz || c.hz > v.MaxHz {
			continue
		}
		i := int(pos(c.hz) * float64(v.Bands))
		if i >= 0 && i < v.Bands {
			bands[i] = math.Max(bands[i], math.Min(1, 0.15+c.amp))
		}
	}
	if sc.transient {
		for i := range bands {
			bands[i] = math.Min(1, bands[i]+0.3)
		}
	}
	for i := range bands {
		bands[i] = round(bands[i], 3)
	}
	return bands
}

// hearSky adds what the stream hears of the shared sky in this tick to the
// current second, and closes the second when the next one begins.
func (s *stream) hearSky(t time.Time, d time.Duration) {
	sec := t.Truncate(time.Second)
	if !s.cur.T.Equal(sec) {
		if s.curN > 0 {
			s.cur.LevelDB /= float64(s.curN)
			s.cur.Flux /= float64(s.curN)
			s.secs = append(s.secs, s.cur)
			if len(s.secs) > 900 {
				s.secs = s.secs[len(s.secs)-900:]
			}
		}
		s.cur, s.curN = secStat{T: sec}, 0
	}
	u := float64(t.UnixNano())/1e9 - s.delay
	k := sky(u) * s.skyWeight
	s.skyNow = k
	rng := s.scene.rng
	s.cur.LevelDB += s.scene.levelDB + 8*k + 3*rng.Float64()
	s.cur.Flux += 0.08 + 0.5*k + 0.06*rng.Float64()
	s.cur.ImpulseRate += (3 + 60*k + 6*rng.Float64()) * d.Seconds()
	s.curN++
}

// second returns what the stream heard in the wall-time second t.
func (s *stream) second(t time.Time) (secStat, bool) {
	for i := len(s.secs) - 1; i >= 0; i-- {
		switch {
		case s.secs[i].T.Equal(t):
			return s.secs[i], true
		case s.secs[i].T.Before(t):
			return secStat{}, false
		}
	}
	return secStat{}, false
}

// sky is the made-up activity of the sky at Unix time u, the same for all
// streams: a slow swell, and now and then a burst that dies away.
func sky(u float64) float64 {
	slow := 0.5 + 0.5*math.Sin(2*math.Pi*u/900)*math.Sin(2*math.Pi*u/173)
	slot := math.Floor(u / 30)
	h := fnv.New64a()
	fmt.Fprint(h, slot)
	x := float64(h.Sum64()%1000000) / 1e6
	burst := 0.0
	if x < 0.35 {
		start := slot*30 + 18*math.Mod(x*7, 1)
		if d := u - start; d >= 0 && d < 12 {
			burst = math.Exp(-d / 3)
		}
	}
	return math.Min(1, 0.35*slow+0.8*burst)
}

// component is one sound in the made-up scene.
type component struct {
	hz    float64
	amp   float64 // 0..1
	group string  // a resonance: all components of one group are its harmonics
}

// scene is what the made-up stream sounds like at the moment: mains hum
// with harmonics, a slowly wandering tone, a whistle that comes and goes,
// and now and then a click. Mains frequency, tones and level differ per
// stream.
type scene struct {
	rng        *rand.Rand
	mains      float64
	wanderHz   float64
	whistleHz  float64
	levelDB    float64
	comps      []component
	transient  bool
	whistleOn  bool
	whistleEnd float64
	nextClick  float64
}

func (sc *scene) advance(t, nyquist float64) {
	sc.comps = sc.comps[:0]
	for h := 1; h <= 20; h++ {
		hz := sc.mains * float64(h)
		sc.comps = append(sc.comps, component{hz: hz, amp: 0.7 / math.Sqrt(float64(h)), group: "mains"})
	}
	wander := sc.wanderHz + 40*math.Sin(t/37)
	sc.comps = append(sc.comps, component{hz: wander, amp: 0.45, group: "wander"}, component{hz: 2 * wander, amp: 0.2, group: "wander"})
	if t >= sc.whistleEnd {
		sc.whistleOn = !sc.whistleOn
		sc.whistleEnd = t + 12 + sc.rng.Float64()*25
	}
	if sc.whistleOn {
		sc.comps = append(sc.comps, component{hz: sc.whistleHz + 3*math.Sin(t), amp: 0.55, group: "whistle"})
	}
	kept := sc.comps[:0]
	for _, c := range sc.comps {
		if c.hz < nyquist*0.98 {
			kept = append(kept, c)
		}
	}
	sc.comps = kept
	sc.transient = false
	if t >= sc.nextClick {
		sc.transient = t > 0
		sc.nextClick = t + 3 + sc.rng.Float64()*9
	}
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (e *Engine) clock() {
	t := time.NewTicker(e.opts.Tick)
	defer t.Stop()
	usageEvery := int(10 * time.Second / e.opts.Tick)
	n := 0
	for {
		select {
		case <-e.stop:
			return
		case now := <-t.C:
			e.mu.Lock()
			for _, s := range e.streams {
				s.tick(now, e.opts.Tick)
			}
			for _, g := range e.groups {
				e.tickGroup(g, now)
			}
			n++
			if n%usageEvery == 0 {
				e.notify("usage", e.usageReport())
			}
			e.mu.Unlock()
		}
	}
}

func (e *Engine) usageReport() map[string]any {
	streams := map[string]any{}
	analyses := map[string]any{}
	var total usage
	for _, s := range e.streams {
		u := s.usage()
		total.CPU += u.CPU
		total.Mem += u.Mem
		total.In += u.In
		if u.CPU > 0 {
			streams[s.id] = map[string]any{"cpu_percent": u.CPU, "memory_mb": u.Mem, "in_kbps": u.In, "in_measured": false}
		}
		for _, a := range s.analyses {
			au := a.usage(s)
			total.CPU += au.CPU
			total.Mem += au.Mem
			if au.CPU > 0 {
				analyses[s.id+"/"+a.id] = map[string]any{"cpu_percent": au.CPU, "memory_mb": au.Mem, "memory_estimated": true}
			}
		}
	}
	return map[string]any{
		"engine":   map[string]any{"cpu_percent": round(total.CPU+0.8, 1), "memory_mb": round(total.Mem+18, 0), "in_kbps": total.In},
		"streams":  streams,
		"analyses": analyses,
	}
}
