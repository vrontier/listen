package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// engineSync keeps the engine in step with the catalogue: it checks the
// engine's API version, follows its changes, and after every start of
// either side sends the whole wanted state (control-api.md, The model).
type engineSync struct {
	eng *engine.Client
	db  *catalogue.DB

	mu       sync.Mutex
	ready    bool   // the engine answers and its API fits
	problem  string // why not, for the admin page and the banner
	info     engine.Info
	instance string
	streams  map[string]engine.Stream // by engine id
	usage    engine.UsageReport

	groupsMu sync.Mutex        // one group sync at a time
	sent     map[string]string // group id → the body last put
}

func newEngineSync(eng *engine.Client, db *catalogue.DB) *engineSync {
	return &engineSync{eng: eng, db: db, problem: "not yet connected to the engine", streams: map[string]engine.Stream{}, sent: map[string]string{}}
}

func (s *engineSync) status() (ready bool, problem string, info engine.Info) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready, s.problem, s.info
}

// used is what the engine reported last as its own use, with the host.
func (s *engineSync) used() (engine.Usage, engine.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.usage.Engine
	if u.CPUPercent == 0 && u.MemoryMB == 0 {
		u = s.info.Usage
	}
	return u, s.info.Host
}

func (s *engineSync) stream(id string) (engine.Stream, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.streams[id]
	return st, ok
}

func (s *engineSync) setProblem(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p != s.problem && p != "" {
		log.Printf("engine: %s", p)
	}
	s.ready, s.problem = p == "", p
}

// run follows the engine until ctx ends, reconnecting as needed.
func (s *engineSync) run(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		if err := s.session(ctx); err != nil && ctx.Err() == nil {
			s.setProblem(err.Error())
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// session is one connection to the engine's change stream.
func (s *engineSync) session(ctx context.Context) error {
	info, err := s.eng.Info(ctx)
	if err != nil {
		return fmt.Errorf("the engine does not answer: %v", err)
	}
	if !info.API.Fits() {
		s.mu.Lock()
		s.info = info
		s.mu.Unlock()
		return fmt.Errorf("the engine speaks control API %s; this Workbench needs %d.%d or a later %d.x — no changes are sent",
			info.API, engine.APIMajor, engine.APIMinor, engine.APIMajor)
	}
	s.mu.Lock()
	s.info = info
	s.mu.Unlock()
	return s.eng.Changes(ctx, func(ev engine.SSE) {
		switch ev.Event {
		case "engine.hello":
			var h engine.Hello
			json.Unmarshal(ev.Data, &h)
			s.mu.Lock()
			fresh := h.Instance != s.instance
			s.instance = h.Instance
			s.mu.Unlock()
			if err := s.reconcile(ctx); err != nil {
				s.setProblem("could not send the wanted state: " + err.Error())
				return
			}
			if fresh {
				log.Printf("engine: instance %s, API %s, wanted state sent", h.Instance, h.API)
			}
			s.setProblem("")
		case "stream.state":
			var st struct {
				engine.Stream
				Deleted bool `json:"deleted"`
			}
			if json.Unmarshal(ev.Data, &st) == nil {
				s.mu.Lock()
				if st.Deleted {
					delete(s.streams, st.ID)
				} else {
					s.streams[st.ID] = st.Stream
				}
				s.mu.Unlock()
			}
		case "usage":
			var u engine.UsageReport
			if json.Unmarshal(ev.Data, &u) == nil {
				s.mu.Lock()
				s.usage = u
				s.mu.Unlock()
			}
		}
	})
}

// wanted reports whether a stream should exist in the engine, and how. A
// stream an analysis in a run uses stays connected, in draft too.
func wanted(st catalogue.Stream, inRun bool) (engine.StreamSpec, bool) {
	spec := engine.StreamSpec{Input: st.Input, SampleRate: st.SampleRate, Mode: engine.OnDemand}
	switch st.State {
	case catalogue.StreamDraft:
		if inRun {
			spec.Mode = engine.Continuous
		}
	case catalogue.StreamProduction:
		if st.AlwaysOn || inRun {
			spec.Mode = engine.Continuous
		}
	default:
		return spec, false // paused or suspended: not in the engine
	}
	return spec, true
}

// analysisBody is what is sent to the engine for one source of an analysis:
// that source's settings, with the revision as label.
func analysisBody(src catalogue.AnalysisSource, a catalogue.Analysis) json.RawMessage {
	var spec map[string]any
	if json.Unmarshal(src.Spec, &spec) != nil || spec == nil {
		spec = map[string]any{}
	}
	spec["label"] = a.Label()
	b, _ := json.Marshal(spec)
	return b
}

// push puts one stream and its analyses into the engine, or removes it
// when it should not run.
func (s *engineSync) push(ctx context.Context, st catalogue.Stream) error {
	inRun, err := s.db.StreamInRun(ctx, st.ID)
	if err != nil {
		return err
	}
	spec, ok := wanted(st, inRun)
	if !ok {
		err := s.eng.DeleteStream(ctx, st.EngineID(), false)
		if engine.IsCode(err, "not_found") {
			return nil
		}
		return err
	}
	es, err := s.eng.PutStream(ctx, st.EngineID(), spec)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.streams[es.ID] = es
	s.mu.Unlock()
	analyses, err := s.db.AnalysesUsing(ctx, st.ID)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, a := range analyses {
		src, ok := a.Source(st.ID)
		if !ok {
			continue
		}
		keep[a.EngineID()] = true
		if _, err := s.eng.PutAnalysis(ctx, st.EngineID(), a.EngineID(), analysisBody(src, a)); err != nil {
			return fmt.Errorf("analysis %s: %w", a.EngineID(), err)
		}
	}
	for _, id := range es.Analyses {
		if !keep[id] {
			if err := s.eng.DeleteAnalysis(ctx, st.EngineID(), id, false); err != nil && !engine.IsCode(err, "not_found") {
				return err
			}
		}
	}
	return nil
}

// reconcile makes the engine hold exactly what the catalogue wants.
func (s *engineSync) reconcile(ctx context.Context) error {
	streams, err := s.db.AllStreams(ctx)
	if err != nil {
		return err
	}
	have, err := s.eng.Streams(ctx)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	var firstErr error
	for _, st := range streams {
		if _, ok := wanted(st, false); ok {
			want[st.EngineID()] = true
		}
		if err := s.push(ctx, st); err != nil {
			log.Printf("engine: stream %s: %v", st.EngineID(), err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	for _, es := range have {
		if !want[es.ID] {
			if err := s.eng.DeleteStream(ctx, es.ID, false); err != nil && !engine.IsCode(err, "not_found") {
				log.Printf("engine: removing %s: %v", es.ID, err)
			}
		}
	}
	s.groupsMu.Lock()
	clear(s.sent) // the engine may have restarted: put every group again
	s.groupsMu.Unlock()
	if err := s.syncGroups(ctx); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// syncGroups makes the engine hold one group for every analysis with two or
// more sources that run, comparing them as the analysis's revision says,
// and no other group. It does nothing before the engine offers groups.
func (s *engineSync) syncGroups(ctx context.Context) error {
	s.mu.Lock()
	info := s.info
	s.mu.Unlock()
	if !info.Groups() {
		return nil
	}
	s.groupsMu.Lock()
	defer s.groupsMu.Unlock()
	analyses, err := s.db.AllAnalyses(ctx)
	if err != nil {
		return err
	}
	have, err := s.eng.Groups(ctx)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, g := range have {
		present[g.ID] = true
	}
	want := map[string]bool{}
	var firstErr error
	for _, a := range analyses {
		spec, ok, err := s.groupSpec(ctx, a)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		id := a.GroupID()
		want[id] = true
		body, _ := json.Marshal(spec)
		if present[id] && s.sent[id] == string(body) {
			continue
		}
		if _, err := s.eng.PutGroup(ctx, id, spec); err != nil {
			log.Printf("engine: group %s: %v", id, err)
			if firstErr == nil {
				firstErr = err
			}
			delete(s.sent, id)
			continue
		}
		s.sent[id] = string(body)
	}
	for _, g := range have {
		if !want[g.ID] {
			if err := s.eng.DeleteGroup(ctx, g.ID); err != nil && !engine.IsCode(err, "not_found") {
				log.Printf("engine: removing group %s: %v", g.ID, err)
			}
			delete(s.sent, g.ID)
		}
	}
	return firstErr
}

// groupSpec is the engine group of an analysis: its sources that run, if
// there are two or more. A run keeps the group going without a page open.
func (s *engineSync) groupSpec(ctx context.Context, a catalogue.Analysis) (engine.GroupSpec, bool, error) {
	spec := engine.GroupSpec{Analyses: a.Spec.Compare, Label: a.Label(), Mode: engine.OnDemand}
	if len(spec.Analyses) == 0 {
		spec.Analyses = catalogue.DefaultCompare
	}
	for _, src := range a.Sources {
		if !running(src.Stream) {
			continue
		}
		spec.Members = append(spec.Members, engine.GroupMember{Stream: src.Stream.EngineID(), Analysis: a.EngineID(), Lat: src.Stream.Texts.Lat, Lon: src.Stream.Texts.Lon})
	}
	if len(spec.Members) < 2 {
		return spec, false, nil
	}
	_, inRun, err := s.db.ActiveRun(ctx, a.ID)
	if inRun {
		spec.Mode = engine.Continuous
	}
	return spec, true, err
}
