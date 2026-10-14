package standin

import (
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
}

func newStream(e *Engine, id, input string, rate int) *stream {
	now := time.Now().UTC()
	s := &stream{e: e, id: id, input: input, sampleRate: rate, state: "idle", since: now, analyses: map[string]*analysis{}}
	s.scene.rng = rand.New(rand.NewPCG(uint64(len(input)), uint64(rate)))
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
	wanted := len(s.analyses) > 0 && (s.mode == "continuous" || s.subscribers() > 0)
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
	s.statusTick++
	status := s.statusTick%50 == 0
	for _, a := range s.analyses {
		a.step(s, t, status)
	}
}

// component is one sound in the made-up scene.
type component struct {
	hz    float64
	amp   float64 // 0..1
	group string  // a resonance: all components of one group are its harmonics
}

// scene is what the made-up stream sounds like at the moment: mains hum
// with harmonics, a slowly wandering tone, a whistle that comes and goes,
// and now and then a click.
type scene struct {
	rng        *rand.Rand
	comps      []component
	transient  bool
	whistleOn  bool
	whistleEnd float64
	nextClick  float64
}

func (sc *scene) advance(t, nyquist float64) {
	sc.comps = sc.comps[:0]
	for h := 1; h <= 20; h++ {
		hz := 50 * float64(h)
		sc.comps = append(sc.comps, component{hz: hz, amp: 0.7 / math.Sqrt(float64(h)), group: "mains"})
	}
	wander := 3150 + 40*math.Sin(t/37)
	sc.comps = append(sc.comps, component{hz: wander, amp: 0.45, group: "wander"}, component{hz: 2 * wander, amp: 0.2, group: "wander"})
	if t >= sc.whistleEnd {
		sc.whistleOn = !sc.whistleOn
		sc.whistleEnd = t + 12 + sc.rng.Float64()*25
	}
	if sc.whistleOn {
		sc.comps = append(sc.comps, component{hz: 2400 + 3*math.Sin(t), amp: 0.55, group: "whistle"})
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
		sc.transient = sc.nextClick > 0
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
