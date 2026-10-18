package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
	"github.com/vrontier/listen/workbench/internal/engine"
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

	previews map[int64]time.Time // analysis → end of its preview
	trials   map[int64]trial     // analysis → its trial, during a run
	held     map[int64]int       // analysis → holders keeping it in the engine

	// onState, if set, is told every change of a stream's state; "removed"
	// when the engine dropped it.
	onState func(stream, state string, at time.Time)
	// onUsage, if set, is given every usage report (every 10 s).
	onUsage func(engine.UsageReport)
	// onFailed, if set, is told when an analysis in the engine fails.
	onFailed func(engine.AnalysisState)

	failures map[string]engine.AnalysisState // "stream/analysis" → its failure

	// reserved, if set, names streams whose operators have not agreed to
	// the Workbench: they are never put into the engine (reserved.go).
	reserved func(input string) bool
}

// runs reports whether a stream is to be in the engine at all.
func (s *engineSync) runs(st catalogue.Stream) bool {
	return running(st) && (s.reserved == nil || !s.reserved(st.Input))
}

func newEngineSync(eng *engine.Client, db *catalogue.DB) *engineSync {
	return &engineSync{eng: eng, db: db, problem: "not yet connected to the engine", streams: map[string]engine.Stream{}, sent: map[string]string{},
		previews: map[int64]time.Time{}, trials: map[int64]trial{}, held: map[int64]int{}, failures: map[string]engine.AnalysisState{}}
}

func (s *engineSync) status() (ready bool, problem string, info engine.Info) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ready {
		// The engine answers, but the service that holds the stations'
		// connections (listen-ingest) does not: no station is heard, and
		// none of them is at fault.
		for _, st := range s.streams {
			if st.LastError != nil && st.LastError.Code == engine.CodeIngestUnavailable {
				return true, "the platform's connection service (listen-ingest) does not answer; sources reconnect as soon as it is back, and the gap is recorded as an absence", s.info
			}
		}
	}
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

// failure is why an analysis in the engine failed, if it did (decided
// 2026-10-16: an analysis fails on its own; putting it again restarts it).
func (s *engineSync) failure(stream, analysis string) (engine.Problem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.failures[stream+"/"+analysis]
	if !ok {
		return engine.Problem{}, false
	}
	if st.LastError == nil {
		return engine.Problem{Message: "no reason given"}, true
	}
	return *st.LastError, true
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
			if fresh {
				clear(s.failures) // a new engine reports its analyses anew
			}
			s.mu.Unlock()
			if err := s.reconcile(ctx); err != nil {
				s.setProblem("could not send the wanted state: " + err.Error())
				return
			}
			if fresh {
				resumed := ""
				if h.Resumed {
					resumed = ", resumed from its saved configuration"
				}
				log.Printf("engine: instance %s, API %s%s, wanted state sent", h.Instance, h.API, resumed)
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
				onState := s.onState
				s.mu.Unlock()
				if onState != nil {
					state := st.State
					if st.Deleted {
						state = "removed"
					}
					onState(st.ID, state, time.Now().UTC())
				}
			}
		case "analysis.state":
			var st engine.AnalysisState
			if json.Unmarshal(ev.Data, &st) != nil {
				break
			}
			key := st.Stream + "/" + st.Analysis
			s.mu.Lock()
			_, was := s.failures[key]
			if st.State == engine.AnalysisFailed {
				s.failures[key] = st
			} else {
				delete(s.failures, key)
			}
			onFailed := s.onFailed
			s.mu.Unlock()
			if st.State == engine.AnalysisFailed && !was && onFailed != nil {
				onFailed(st)
			}
		case "usage":
			var u engine.UsageReport
			if json.Unmarshal(ev.Data, &u) == nil {
				s.mu.Lock()
				s.usage = u
				onUsage := s.onUsage
				s.mu.Unlock()
				if onUsage != nil {
					onUsage(u)
				}
			}
		}
	})
}

// wanted reports whether a stream should exist in the engine, and how: a
// stream an analysis in a run uses stays connected; otherwise it connects
// on demand, for a preview.
func (s *engineSync) wanted(st catalogue.Stream, inRun bool) (engine.StreamSpec, bool) {
	if !s.runs(st) {
		return engine.StreamSpec{}, false
	}
	return wanted(st, inRun)
}

func wanted(st catalogue.Stream, inRun bool) (engine.StreamSpec, bool) {
	spec := engine.StreamSpec{Input: st.Input, SampleRate: st.SampleRate, Mode: engine.OnDemand}
	if st.View != nil {
		var v engine.ViewSpec
		if json.Unmarshal(st.View, &v) == nil && v.Scale != "" {
			spec.View = &v
		}
	}
	switch st.State {
	case catalogue.StreamActive:
		if inRun {
			spec.Mode = engine.Continuous
		}
	default:
		return spec, false // paused, retired or suspended: not in the engine
	}
	return spec, true
}

// An analysis is in the engine only while it is in a run, or while its data
// set is being frozen (held); a preview is a second engine analysis that
// keeps nothing (design.md, Runs).

// previewID is the engine identifier of an analysis's preview.
func previewID(a catalogue.Analysis) string { return fmt.Sprintf("p-%04d", a.ID) }

// startPreview lets the analysis's preview run until the given time.
func (s *engineSync) startPreview(id int64, until time.Time) {
	s.mu.Lock()
	s.previews[id] = until
	s.mu.Unlock()
	if err := s.db.SavePreview(context.Background(), id, until); err != nil {
		log.Printf("preview of analysis %d not kept: %v", id, err)
	}
}

func (s *engineSync) endPreview(id int64) {
	s.mu.Lock()
	delete(s.previews, id)
	s.mu.Unlock()
	if err := s.db.DeletePreview(context.Background(), id); err != nil {
		log.Printf("preview of analysis %d: %v", id, err)
	}
}

// load takes up the previews and trials that went on when the Workbench
// last stopped (they are kept in the catalogue, so that a restart does
// not end them). Those past their end are ended by the next round of
// endedPreviews and endedTrials.
func (s *engineSync) load(ctx context.Context) error {
	previews, err := s.db.Previews(ctx)
	if err != nil {
		return err
	}
	trials, err := s.db.Trials(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, until := range previews {
		s.previews[id] = until
	}
	for id, t := range trials {
		s.trials[id] = trial{Started: t.Started, Until: t.Until, Settings: t.Settings}
	}
	if n := len(previews) + len(trials); n > 0 {
		log.Printf("taken up again: %d previews and %d trials", len(previews), len(trials))
	}
	return nil
}

// preview reports until when the analysis's preview runs, if it does.
func (s *engineSync) preview(id int64) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.previews[id]
	if ok && !time.Now().Before(until) {
		return time.Time{}, false
	}
	return until, ok
}

// previewing lists the analyses whose preview runs now.
func (s *engineSync) previewing() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []int64
	now := time.Now()
	for id, until := range s.previews {
		if now.Before(until) {
			out = append(out, id)
		}
	}
	return out
}

// endedPreviews removes the previews past their end and returns their
// analyses.
func (s *engineSync) endedPreviews() []int64 {
	s.mu.Lock()
	var out []int64
	for id, until := range s.previews {
		if !time.Now().Before(until) {
			delete(s.previews, id)
			out = append(out, id)
		}
	}
	s.mu.Unlock()
	for _, id := range out {
		s.db.DeletePreview(context.Background(), id)
	}
	return out
}

// nextEnd is the earliest end of a preview or a trial, if any.
func (s *engineSync) nextEnd() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var next time.Time
	for _, until := range s.previews {
		if next.IsZero() || until.Before(next) {
			next = until
		}
	}
	for _, t := range s.trials {
		if next.IsZero() || t.Until.Before(next) {
			next = t.Until
		}
	}
	return next, !next.IsZero()
}

// A trial runs changed settings next to a run (design.md, question 16):
// one more engine analysis per source, t-NNNN, learning in memory only. It
// does not touch the run or its data set.
type trial struct {
	Started  time.Time
	Until    time.Time
	Settings map[int64]json.RawMessage // by stream
}

const trialLength = time.Hour

func trialID(a catalogue.Analysis) string { return fmt.Sprintf("t-%04d", a.ID) }

func (s *engineSync) startTrial(id int64, settings map[int64]json.RawMessage) trial {
	s.mu.Lock()
	now := time.Now()
	t := trial{Started: now, Until: now.Add(trialLength), Settings: settings}
	s.trials[id] = t
	s.mu.Unlock()
	if err := s.db.SaveTrial(context.Background(), id, catalogue.Trial{Started: t.Started, Until: t.Until, Settings: t.Settings}); err != nil {
		log.Printf("trial of analysis %d not kept: %v", id, err)
	}
	return t
}

func (s *engineSync) endTrial(id int64) (trial, bool) {
	s.mu.Lock()
	t, ok := s.trials[id]
	delete(s.trials, id)
	s.mu.Unlock()
	if err := s.db.DeleteTrial(context.Background(), id); err != nil {
		log.Printf("trial of analysis %d: %v", id, err)
	}
	return t, ok
}

// trialOf returns the analysis's trial, if one goes on.
func (s *engineSync) trialOf(id int64) (trial, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.trials[id]
	if ok && !time.Now().Before(t.Until) {
		return trial{}, false
	}
	return t, ok
}

// endedTrials removes the trials past their hour and returns their
// analyses.
func (s *engineSync) endedTrials() []int64 {
	s.mu.Lock()
	var out []int64
	for id, t := range s.trials {
		if !time.Now().Before(t.Until) {
			delete(s.trials, id)
			out = append(out, id)
		}
	}
	s.mu.Unlock()
	for _, id := range out {
		s.db.DeleteTrial(context.Background(), id)
	}
	return out
}

// trialBody is a source's settings in a trial: learning in memory only.
func trialBody(raw json.RawMessage) json.RawMessage {
	var spec map[string]any
	if json.Unmarshal(raw, &spec) != nil || spec == nil {
		spec = map[string]any{}
	}
	spec["memory"] = map[string]any{"mode": engine.MemoryRAM}
	spec["label"] = "trial"
	b, _ := json.Marshal(spec)
	return b
}

// hold keeps an analysis in the engine outside a run, until release.
func (s *engineSync) hold(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held[id]++
}

func (s *engineSync) release(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.held[id]--; s.held[id] <= 0 {
		delete(s.held, id)
	}
}

func (s *engineSync) isHeld(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held[id] > 0
}

// previewBody is a preview's settings: the source's, learning in memory
// only, so that nothing of it is kept.
func previewBody(src catalogue.AnalysisSource) json.RawMessage {
	var spec map[string]any
	if json.Unmarshal(src.Spec, &spec) != nil || spec == nil {
		spec = map[string]any{}
	}
	spec["memory"] = map[string]any{"mode": engine.MemoryRAM}
	spec["label"] = "preview"
	b, _ := json.Marshal(spec)
	return b
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
	analyses, err := s.db.AnalysesUsing(ctx, st.ID)
	if err != nil {
		return err
	}
	// A preview listens for its ten minutes whether or not a page is open
	// (it may be started through MCP): its stream stays connected as for
	// a run.
	for _, a := range analyses {
		if _, ok := s.preview(a.ID); ok {
			inRun = true
		}
	}
	spec, ok := s.wanted(st, inRun)
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
	keep := map[string]bool{}
	for _, a := range analyses {
		src, ok := a.Source(st.ID)
		if !ok {
			continue
		}
		_, inRun, err := s.db.ActiveRun(ctx, a.ID)
		if err != nil {
			return err
		}
		if inRun || s.isHeld(a.ID) {
			keep[a.EngineID()] = true
			if _, err := s.eng.PutAnalysis(ctx, st.EngineID(), a.EngineID(), analysisBody(src, a)); err != nil {
				return fmt.Errorf("analysis %s: %w", a.EngineID(), err)
			}
		}
		if t, ok := s.trialOf(a.ID); ok && inRun {
			if raw, ok := t.Settings[st.ID]; ok {
				keep[trialID(a)] = true
				if _, err := s.eng.PutAnalysis(ctx, st.EngineID(), trialID(a), trialBody(raw)); err != nil {
					return fmt.Errorf("trial %s: %w", trialID(a), err)
				}
			}
		}
		if _, ok := s.preview(a.ID); ok && !inRun {
			keep[previewID(a)] = true
			if _, err := s.eng.PutAnalysis(ctx, st.EngineID(), previewID(a), previewBody(src)); err != nil {
				return fmt.Errorf("preview %s: %w", previewID(a), err)
			}
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
		if _, ok := s.wanted(st, false); ok {
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
		if !s.runs(src.Stream) {
			continue
		}
		spec.Members = append(spec.Members, engine.GroupMember{Stream: src.Stream.EngineID(), Analysis: a.EngineID(), Lat: src.Stream.Texts.Lat, Lon: src.Stream.Texts.Lon})
	}
	if len(spec.Members) < 2 {
		return spec, false, nil
	}
	// The comparison runs only during a run: its members are the run's
	// engine analyses, which exist only then.
	_, inRun, err := s.db.ActiveRun(ctx, a.ID)
	if !inRun && !s.isHeld(a.ID) {
		return spec, false, err
	}
	spec.Mode = engine.Continuous
	return spec, true, err
}
