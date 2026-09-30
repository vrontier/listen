package memory

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

// Config tunes the memory. Zero values get the defaults in New.
type Config struct {
	Dir          string        // where motifs.json and history/ live; "" keeps memory in RAM only
	ReturnAfter  time.Duration // absence before a known motif counts as returned
	HistoryDays  int           // history files kept
	SummaryEvery time.Duration
	Window       time.Duration // texture window

	ResonanceMatch float64 // minimum similarity to join a resonance motif
	TextureMatch   float64 // minimum similarity to join a texture motif
	TextureScale   float64 // texture distance that halves similarity (roughly)
	MergeAt        float64 // prototypes this similar are merged
}

func (c *Config) defaults() {
	if c.ReturnAfter == 0 {
		c.ReturnAfter = 3 * time.Minute
	}
	if c.HistoryDays == 0 {
		c.HistoryDays = 30
	}
	if c.SummaryEvery == 0 {
		c.SummaryEvery = 5 * time.Minute
	}
	if c.Window == 0 {
		c.Window = 20 * time.Second
	}
	if c.ResonanceMatch == 0 {
		c.ResonanceMatch = 0.6
	}
	if c.TextureMatch == 0 {
		c.TextureMatch = 0.6
	}
	if c.TextureScale == 0 {
		c.TextureScale = 0.08
	}
	if c.MergeAt == 0 {
		c.MergeAt = 0.85
	}
}

// Memory is safe for concurrent use: Process runs on the analysis goroutine,
// the API handlers on HTTP goroutines.
type Memory struct {
	cfg     Config
	stamper *events.Stamper
	store   *store

	mu         sync.Mutex
	runStart   time.Time // memory clock: runStart + audio position
	now        time.Time
	motifs     []*Motif
	aliases    map[string]string
	nextKey    int
	nextPublic int
	open       map[string]*episode // by resonance id
	feature    events.Feature
	lastFeatAt time.Time

	tex       textureAcc
	texActive *Motif

	minutes     []*minute // finished, last 24 h
	cur         *minute
	lastSummary time.Time
	summaries   map[string]events.Envelope
	lastSave    time.Time
	dirty       bool
}

type episode struct {
	motif    *Motif
	start    time.Time
	sim      float64
	strength float64
	proto    Prototype
}

// New opens (or creates) the memory in cfg.Dir.
func New(cfg Config, stamper *events.Stamper) (*Memory, error) {
	cfg.defaults()
	m := &Memory{
		cfg: cfg, stamper: stamper, runStart: time.Now(),
		open: map[string]*episode{}, aliases: map[string]string{}, summaries: map[string]events.Envelope{},
		lastSave: time.Now(),
	}
	m.now = m.runStart
	if cfg.Dir == "" {
		return m, nil
	}
	st, err := openStore(cfg.Dir, cfg.HistoryDays)
	if err != nil {
		return nil, err
	}
	m.store = st
	s, err := st.loadState()
	if err != nil {
		return nil, err
	}
	m.motifs, m.nextKey, m.nextPublic = s.Motifs, s.NextKey, s.NextPublic
	if s.Aliases != nil {
		m.aliases = s.Aliases
	}
	// Summaries continue across restarts from the logged minutes.
	st.scan(m.now.Add(-24*time.Hour), m.now.Add(time.Minute), func(r record) bool {
		if r.Minute != nil {
			m.minutes = append(m.minutes, r.Minute)
		}
		return true
	})
	known := 0
	for _, mo := range m.motifs {
		if mo.established() {
			known++
		}
	}
	log.Printf("memory: %s: %d motifs (%d established), %d minutes of history", cfg.Dir, len(m.motifs), known, len(m.minutes))
	return m, nil
}

// Process observes one event and returns what to broadcast: the event
// itself (resonances annotated with their motif) followed by any memory
// events it caused.
func (m *Memory) Process(msg events.Message) []events.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if msg.Env.Position > 0 {
		m.now = m.runStart.Add(time.Duration(msg.Env.Position * float64(time.Second)))
	}
	if m.cur == nil {
		m.cur = newMinute(m.now)
	}
	out := []events.Message{msg}
	switch p := msg.Env.Payload.(type) {
	case events.Spectrum:
		m.tex.addBands(p.Bands)
	case events.Feature:
		out = append(out, m.onFeature(p)...)
	case events.Transient:
		m.cur.Events++
		m.log(msg)
	case events.Narrative:
		m.log(msg)
	case events.Resonance:
		var extra []events.Message
		p, extra = m.onResonance(p)
		out[0] = msg.WithPayload(p)
		if p.Status != "update" {
			m.log(out[0])
		}
		out = append(out, extra...)
	}
	for _, o := range out[1:] {
		m.log(o)
	}
	if m.store != nil && (m.dirty && time.Since(m.lastSave) > time.Minute) {
		m.saveLocked()
	}
	return out
}

func (m *Memory) onFeature(f events.Feature) []events.Message {
	var out []events.Message
	// Presence of whatever is sounding, credited to the current minute.
	if !m.lastFeatAt.IsZero() {
		dt := m.now.Sub(m.lastFeatAt).Seconds()
		if dt > 0 && dt < 5 {
			for _, mo := range m.motifs {
				if mo.established() && mo.active() {
					m.cur.Presence[mo.ID] += dt
				}
			}
		}
	}
	m.lastFeatAt = m.now
	m.feature = f
	if m.now.Sub(m.cur.T) >= time.Minute {
		m.cur.finish()
		m.minutes = append(m.minutes, m.cur)
		if m.store != nil {
			m.store.append(record{T: m.cur.T, Minute: m.cur})
		}
		cut := m.now.Add(-24 * time.Hour)
		for len(m.minutes) > 0 && m.minutes[0].T.Before(cut) {
			m.minutes = m.minutes[1:]
		}
		m.cur = newMinute(m.now)
	}
	m.cur.addFeature(f)

	m.tex.addFeature(f)
	if m.tex.start.IsZero() {
		m.tex.start = m.now
	}
	if m.now.Sub(m.tex.start) >= m.cfg.Window {
		out = append(out, m.onTextureWindow()...)
	}
	if m.lastSummary.IsZero() {
		// First summary a minute after start, then every SummaryEvery.
		m.lastSummary = m.now.Add(time.Minute - m.cfg.SummaryEvery)
	} else if m.now.Sub(m.lastSummary) >= m.cfg.SummaryEvery {
		m.lastSummary = m.now
		out = append(out, m.summaryLocked()...)
	}
	if m.store != nil {
		m.store.cleanup(m.now)
	}
	return out
}

// ---- resonance motifs ------------------------------------------------------

func (m *Memory) onResonance(p events.Resonance) (events.Resonance, []events.Message) {
	var out []events.Message
	switch p.Status {
	case "start":
		obs := m.resonanceProto(p)
		best, sim := m.best(KindResonance, obs)
		if best == nil || sim < m.cfg.ResonanceMatch {
			best, sim = m.newMotif(KindResonance, obs), 1
		}
		ep := &episode{motif: best, start: m.now, sim: sim, strength: p.Strength, proto: obs}
		m.open[p.ID] = ep
		m.cur.Events++
		if best.established() && !best.active() && m.now.Sub(best.LastSeen) >= m.cfg.ReturnAfter {
			out = append(out, m.returned(best, sim, p.Strength))
		}
		best.Occurrences++
		best.SimSum += sim
		best.open++
		best.lastStrength = p.Strength
		out = append(out, m.maybeEstablish(best)...)
		m.dirty = true
	case "update":
		if ep := m.open[p.ID]; ep != nil {
			ep.strength = math.Max(ep.strength, p.Strength)
			ep.proto = m.resonanceProto(p)
			ep.motif.lastStrength = p.Strength
		}
	case "end":
		ep := m.open[p.ID]
		if ep == nil {
			break
		}
		delete(m.open, p.ID)
		mo := ep.motif
		dur := m.now.Sub(ep.start).Seconds()
		mo.open--
		mo.PresenceS += dur
		mo.LastSeen = m.now
		mo.Proto.blend(ep.proto, math.Min(0.3, dur/120), KindResonance)
		mo.remember(Occurrence{Start: ep.start, DurationS: round(dur, 1), Similarity: round(ep.sim, 3), Strength: round(ep.strength, 3)})
		out = append(out, m.maybeEstablish(mo)...)
		m.mergeInto(mo)
		m.dirty = true
		if mo.established() {
			p.MotifID = mo.ID
		}
		return p, out
	}
	if ep := m.open[p.ID]; ep != nil && ep.motif.established() {
		p.MotifID = ep.motif.ID
	}
	return p, out
}

// resonanceProto describes a resonance event in prototype terms.
func (m *Memory) resonanceProto(p events.Resonance) Prototype {
	pr := Prototype{FundamentalHz: p.FundamentalHz, Harmonicity: m.feature.Harmonicity, Entropy: m.feature.Entropy}
	ref := math.Max(p.Strength, 0.05)
	for _, h := range p.Harmonics {
		if i := h.Order - 2; i >= 0 && i < profileOrders {
			pr.Profile[i] = clamp01(h.Strength / ref)
		}
	}
	return pr
}

// ---- texture motifs --------------------------------------------------------

type textureAcc struct {
	start    time.Time
	bands    [textureBands]float64
	nb       int
	ent, har float64
	flux     float64
	cent     float64
	nf       int
}

func (t *textureAcc) addBands(b []float64) {
	if len(b) == 0 {
		return
	}
	for i := 0; i < textureBands; i++ {
		lo, hi := i*len(b)/textureBands, (i+1)*len(b)/textureBands
		var s float64
		for _, v := range b[lo:hi] {
			s += v
		}
		t.bands[i] += s / float64(hi-lo)
	}
	t.nb++
}

func (t *textureAcc) addFeature(f events.Feature) {
	t.ent += f.Entropy
	t.har += f.Harmonicity
	t.flux += f.Flux
	t.cent += f.CentroidHz
	t.nf++
}

func (t *textureAcc) fingerprint() (Prototype, bool) {
	if t.nb == 0 || t.nf == 0 {
		return Prototype{}, false
	}
	var p Prototype
	for i := range p.Bands {
		p.Bands[i] = t.bands[i] / float64(t.nb)
	}
	n := float64(t.nf)
	p.Entropy, p.Harmonicity, p.Flux, p.CentroidHz = t.ent/n, t.har/n, t.flux/n, t.cent/n
	return p, true
}

func (m *Memory) onTextureWindow() []events.Message {
	var out []events.Message
	fp, ok := m.tex.fingerprint()
	winStart := m.tex.start
	m.tex = textureAcc{start: m.now}
	if !ok {
		return nil
	}
	best, sim := m.best(KindTexture, fp)
	if best == nil || sim < m.cfg.TextureMatch {
		best, sim = m.newMotif(KindTexture, fp), 1
	}
	if m.texActive != best {
		if prev := m.texActive; prev != nil {
			prev.open = 0
		}
		// A new run of this texture: count it, and announce a return if it
		// was away long enough.
		if best.established() && winStart.Sub(best.LastSeen) >= m.cfg.ReturnAfter {
			out = append(out, m.returned(best, sim, 0))
		}
		best.Occurrences++
		best.SimSum += sim
		best.remember(Occurrence{Start: winStart, Similarity: round(sim, 3)})
		m.texActive = best
	} else if n := len(best.Recent); n > 0 {
		best.Recent[n-1].DurationS = round(m.now.Sub(best.Recent[n-1].Start).Seconds(), 1)
	}
	best.open = 1
	best.PresenceS += m.now.Sub(winStart).Seconds()
	best.LastSeen = m.now
	best.Proto.blend(fp, 0.1, KindTexture)
	out = append(out, m.maybeEstablish(best)...)
	m.mergeInto(best)
	m.dirty = true
	return out
}

// ---- shared ----------------------------------------------------------------

// best returns the most similar motif of a kind.
func (m *Memory) best(kind string, obs Prototype) (*Motif, float64) {
	var best *Motif
	bestSim := -1.0
	for _, mo := range m.motifs {
		if mo.Kind != kind {
			continue
		}
		s := m.similarity(kind, obs, mo.Proto)
		// Prefer established motifs on near-ties, so candidates don't
		// steal occurrences from a known structure.
		if mo.established() {
			s += 0.02
		}
		if s > bestSim {
			best, bestSim = mo, s
		}
	}
	return best, math.Min(1, bestSim)
}

func (m *Memory) similarity(kind string, a, b Prototype) float64 {
	if kind == KindResonance {
		return resonanceSimilarity(a, b)
	}
	return textureSimilarity(a, b, m.cfg.TextureScale)
}

func (m *Memory) newMotif(kind string, p Prototype) *Motif {
	var seed [4]byte
	rand.Read(seed[:])
	m.nextKey++
	mo := &Motif{Key: m.nextKey, Kind: kind, Created: m.now, LastSeen: m.now, Seed: binary.BigEndian.Uint32(seed[:]), Proto: p}
	m.motifs = append(m.motifs, mo)
	m.prune()
	return mo
}

// maybeEstablish promotes a candidate that has become a recurring structure.
func (m *Memory) maybeEstablish(mo *Motif) []events.Message {
	if mo.established() {
		return nil
	}
	span := m.now.Sub(mo.Created)
	var ok bool
	switch mo.Kind {
	case KindResonance:
		ok = (mo.Occurrences >= 3 && span >= 2*time.Minute) || (mo.Occurrences >= 2 && mo.PresenceS >= 300)
	case KindTexture:
		ok = mo.PresenceS >= 120
	}
	if !ok {
		return nil
	}
	m.nextPublic++
	mo.ID = fmt.Sprintf("motif-%03d", m.nextPublic)
	mo.Established = m.now
	m.dirty = true
	return []events.Message{m.stamper.Stamp(events.TypeMotifDetected, m.stampTime(), 0, events.MotifDetected{
		MotifID: mo.ID, Kind: mo.Kind, Confidence: mo.Confidence(), Occurrences: mo.Occurrences,
		Signature: mo.Signature(), Visual: mo.Visual(),
	})}
}

func (m *Memory) returned(mo *Motif, sim, strength float64) events.Message {
	return m.stamper.Stamp(events.TypeMotifReturned, m.stampTime(), 0, events.MotifReturned{
		MotifID: mo.ID, Kind: mo.Kind, Similarity: round(sim, 3),
		LastSeenS: round(m.now.Sub(mo.LastSeen).Seconds(), 0), Occurrence: mo.Occurrences + 1,
		CurrentStrength: round(strength, 3), Visual: mo.Visual(),
	})
}

// mergeInto folds any other motif of the same kind whose prototype has
// converged on mo's into the older of the two.
func (m *Memory) mergeInto(mo *Motif) {
	for _, o := range m.motifs {
		if o == mo || o.Kind != mo.Kind || o.active() || m.similarity(mo.Kind, mo.Proto, o.Proto) < m.cfg.MergeAt {
			continue
		}
		keep, drop := mo, o
		if o.established() && (!mo.established() || o.Key < mo.Key) {
			keep, drop = o, mo
		}
		if drop.active() {
			continue
		}
		keep.Occurrences += drop.Occurrences
		keep.SimSum += drop.SimSum
		keep.PresenceS += drop.PresenceS
		if drop.Created.Before(keep.Created) {
			keep.Created = drop.Created
		}
		if drop.LastSeen.After(keep.LastSeen) {
			keep.LastSeen = drop.LastSeen
		}
		keep.Recent = append(keep.Recent, drop.Recent...)
		sort.Slice(keep.Recent, func(i, j int) bool { return keep.Recent[i].Start.Before(keep.Recent[j].Start) })
		if len(keep.Recent) > recentKept {
			keep.Recent = keep.Recent[len(keep.Recent)-recentKept:]
		}
		if drop.established() {
			if !keep.established() {
				keep.ID, keep.Established = drop.ID, drop.Established
			} else {
				m.aliases[drop.ID] = keep.ID
			}
		}
		for _, ep := range m.open {
			if ep.motif == drop {
				ep.motif = keep
			}
		}
		if m.texActive == drop {
			m.texActive = keep
		}
		m.remove(drop)
		m.dirty = true
		return
	}
}

func (m *Memory) remove(mo *Motif) {
	for i, o := range m.motifs {
		if o == mo {
			m.motifs = append(m.motifs[:i], m.motifs[i+1:]...)
			return
		}
	}
}

// prune drops candidates that never recurred within an hour.
func (m *Memory) prune() {
	kept := m.motifs[:0]
	for _, mo := range m.motifs {
		if !mo.established() && !mo.active() && m.now.Sub(mo.LastSeen) > time.Hour {
			continue
		}
		kept = append(kept, mo)
	}
	m.motifs = kept
}

// stampTime is the envelope timestamp for memory events: the observation
// time of the latest analysed audio.
func (m *Memory) stampTime() time.Time {
	return m.stamper.LastTimestamp()
}

func (m *Memory) summaryLocked() []events.Message {
	known := 0
	for _, mo := range m.motifs {
		if mo.established() {
			known++
		}
	}
	all := append(append([]*minute(nil), m.minutes...), m.cur)
	var out []events.Message
	for _, w := range []struct {
		label string
		d     time.Duration
	}{{"1h", time.Hour}, {"24h", 24 * time.Hour}} {
		msg := m.stamper.Stamp(events.TypeMemorySummary, m.stampTime(), 0, summarize(all, m.now, w.d, w.label, known, m.aliases))
		m.summaries[w.label] = msg.Env
		out = append(out, msg)
	}
	return out
}

func (m *Memory) log(msg events.Message) {
	if m.store == nil {
		return
	}
	switch msg.Type {
	case events.TypeTransient, events.TypeResonance, events.TypeMotifDetected, events.TypeMotifReturned,
		events.TypeMemorySummary, events.TypeNarrative:
		if err := m.store.append(record{T: m.now, Event: msg.Data}); err != nil {
			log.Printf("memory: history: %v", err)
		}
	}
}

func (m *Memory) saveLocked() {
	m.lastSave = time.Now()
	if m.store == nil {
		return
	}
	st := &state{Version: 1, NextKey: m.nextKey, NextPublic: m.nextPublic, Motifs: m.motifs, Aliases: m.aliases}
	if err := m.store.saveState(st); err != nil {
		log.Printf("memory: save: %v", err)
		return
	}
	m.store.flush()
	m.dirty = false
}

// Close saves the memory.
func (m *Memory) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil {
		return nil
	}
	m.saveLocked()
	return m.store.close()
}
