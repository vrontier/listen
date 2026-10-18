package web

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
	"github.com/vrontier/listen/workbench/internal/engine"
)

// A run's data set (design.md, Runs). While the run lasts, the Workbench
// records the events of each source and of the comparison into files of
// its own, one JSON line per event; after a restart it catches up from the
// engine's history. When the run ends the recording is frozen into the data
// set: a JSON file with everything, the events as CSV, the comparison as
// CSV, all compressed. It does not depend on the engine keeping anything.

// recordedTypes are the events a data set keeps of a source; the frames and
// spectra, ten a second each, are not kept.
var recordedTypes = []string{"event.transient", "event.resonance", "motif.detected", "motif.returned", "narrative.update"}

// recordedGroupTypes are the events kept of a comparison.
var recordedGroupTypes = []string{"group.activity", "group.correlation"}

// Data set files, as published under /runs/<id>/<name> without ".gz".
const (
	fileJSON    = "dataset.json"
	fileEvents  = "events.csv"
	fileCompare = "comparison.csv"
	groupTopic  = "comparison"
)

type datasets struct {
	s   *Server
	dir string

	mu       sync.Mutex
	recs     map[int64]*recording // by run
	freezing map[int64]bool
	busy     sync.WaitGroup // freezing in progress, for tests
}

// recording is one run being recorded: one subscription per topic, a
// source's engine stream id or groupTopic.
type recording struct {
	run      catalogue.Run
	analysis int64
	topics   map[string]context.CancelFunc
	streams  map[string]bool // the analysis's sources, by engine id
	wg       sync.WaitGroup
}

// statesFile keeps the state changes of a run's sources, from which the
// data set tells when a source was absent.
const statesFile = "states.jsonl"

type stateLine struct {
	Stream string    `json:"stream"`
	State  string    `json:"state"`
	At     time.Time `json:"at"`
}

// noteState records a stream's change of state in every recording of a run
// that uses the stream.
func (d *datasets) noteState(stream, state string, at time.Time) {
	d.mu.Lock()
	var runs []int64
	for id, rec := range d.recs {
		if rec.streams[stream] {
			runs = append(runs, id)
		}
	}
	d.mu.Unlock()
	b, _ := json.Marshal(stateLine{stream, state, at})
	for _, id := range runs {
		appendLine(filepath.Join(d.runDir(id), statesFile), b)
	}
}

func appendLine(path string, b []byte) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		log.Printf("data set: %v", err)
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

func newDatasets(s *Server, dir string) *datasets {
	return &datasets{s: s, dir: dir, recs: map[int64]*recording{}, freezing: map[int64]bool{}}
}

func (d *datasets) runDir(id int64) string { return filepath.Join(d.dir, strconv.FormatInt(id, 10)) }

// close stops every recording and waits for freezing in progress; what
// was recorded stays, and the next start goes on from it.
func (d *datasets) close() {
	d.mu.Lock()
	recs := d.recs
	d.recs = map[int64]*recording{}
	d.mu.Unlock()
	for _, rec := range recs {
		for _, cancel := range rec.topics {
			cancel()
		}
		rec.wg.Wait()
	}
	d.busy.Wait()
}

// refresh starts recording runs that go on and follows changes of their
// sources; runs that ended are frozen.
func (d *datasets) refresh(ctx context.Context) {
	if d.dir == "" {
		return
	}
	runs, err := d.s.db.ActiveRuns(ctx)
	if err != nil {
		log.Printf("data sets: %v", err)
		return
	}
	active := map[int64]bool{}
	for _, run := range runs {
		active[run.ID] = true
		a, err := d.s.db.Analysis(ctx, run.AnalysisID)
		if err != nil {
			continue
		}
		d.record(run, a)
	}
	d.mu.Lock()
	var ended []int64
	for id := range d.recs {
		if !active[id] {
			ended = append(ended, id)
		}
	}
	d.mu.Unlock()
	unfrozen, err := d.s.db.UnfrozenRuns(ctx)
	if err != nil {
		log.Printf("data sets: %v", err)
	}
	for _, r := range unfrozen {
		ended = append(ended, r.ID)
	}
	for _, id := range ended {
		d.freeze(id)
	}
}

// record makes the recording of a run follow the analysis's sources that
// run, and its comparison.
func (d *datasets) record(run catalogue.Run, a catalogue.Analysis) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec := d.recs[run.ID]
	if rec == nil {
		if err := os.MkdirAll(d.runDir(run.ID), 0o750); err != nil {
			log.Printf("data set of run %d: %v", run.ID, err)
			return
		}
		rec = &recording{run: run, analysis: a.ID, topics: map[string]context.CancelFunc{}, streams: map[string]bool{}}
		d.recs[run.ID] = rec
		d.s.sync.hold(a.ID) // until frozen
	}
	for _, src := range a.Sources {
		id := src.Stream.EngineID()
		if rec.streams[id] {
			continue
		}
		// Where a source stands when the recording begins to follow it.
		rec.streams[id] = true
		state := "removed"
		if !d.s.running(src.Stream) {
			state = src.Stream.State
		} else if es, ok := d.s.sync.stream(id); ok {
			state = es.State
		}
		b, _ := json.Marshal(stateLine{id, state, time.Now().UTC()})
		appendLine(filepath.Join(d.runDir(run.ID), statesFile), b)
	}
	want := map[string]func(context.Context){}
	for _, src := range a.Sources {
		if d.s.running(src.Stream) {
			st := src.Stream
			want[st.EngineID()] = func(ctx context.Context) { d.follow(ctx, rec, st.EngineID(), a) }
		}
	}
	if d.s.compares(a) {
		want[groupTopic] = func(ctx context.Context) { d.follow(ctx, rec, groupTopic, a) }
	}
	for topic, cancel := range rec.topics {
		if want[topic] == nil {
			cancel()
			delete(rec.topics, topic)
		}
	}
	for topic, fn := range want {
		if rec.topics[topic] != nil {
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		rec.topics[topic] = cancel
		rec.wg.Add(1)
		go func() {
			defer rec.wg.Done()
			fn(ctx)
		}()
	}
}

// follow records one topic until ctx ends: first what the engine's history
// has since the last recorded event (after a restart), then live events.
func (d *datasets) follow(ctx context.Context, rec *recording, topic string, a catalogue.Analysis) {
	w, err := openLog(filepath.Join(d.runDir(rec.run.ID), topic+".jsonl"))
	if err != nil {
		log.Printf("data set of run %d, %s: %v", rec.run.ID, topic, err)
		return
	}
	defer w.close()
	backoff := time.Second
	for ctx.Err() == nil {
		if !w.last.IsZero() {
			d.catchUp(ctx, w, topic, a)
		}
		var err error
		if topic == groupTopic {
			err = d.s.eng.SubscribeGroup(ctx, a.GroupID(), func(ev engine.SSE) { w.add(ev.Data) }, recordedGroupTypes...)
		} else {
			err = d.s.eng.Subscribe(ctx, topic, a.EngineID(), func(ev engine.SSE) { w.add(ev.Data) }, recordedTypes...)
		}
		if ctx.Err() != nil {
			return
		}
		log.Printf("data set of run %d, %s: %v; again in %s", rec.run.ID, topic, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// catchUp adds what the engine stored after the last recorded event.
func (d *datasets) catchUp(ctx context.Context, w *eventLog, topic string, a catalogue.Analysis) {
	cursor := w.last
	for page := 0; page < 200; page++ {
		q := url.Values{"from": {cursor.Format(time.RFC3339Nano)}, "to": {time.Now().UTC().Format(time.RFC3339Nano)}, "limit": {"2000"}}
		var raw json.RawMessage
		var err error
		if topic == groupTopic {
			raw, err = d.s.eng.GroupData(ctx, a.GroupID(), "history/events", q)
		} else {
			raw, err = d.s.eng.Data(ctx, topic, a.EngineID(), "history/events", q)
		}
		if err != nil {
			return
		}
		var got struct {
			Events []json.RawMessage `json:"events"`
		}
		json.Unmarshal(raw, &got)
		for _, ev := range got.Events {
			var e struct {
				Type string `json:"type"`
			}
			json.Unmarshal(ev, &e)
			if contains(recordedTypes, e.Type) || contains(recordedGroupTypes, e.Type) {
				w.add(ev)
			}
		}
		if len(got.Events) < 2000 || !w.last.After(cursor) {
			return
		}
		cursor = w.last
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// eventLog appends events to a file, one JSON line each, skipping what it
// already has: the catching up and the live events may overlap.
type eventLog struct {
	mu     sync.Mutex
	f      *os.File
	last   time.Time
	atLast map[string]bool // events with the timestamp last
}

func openLog(path string) (*eventLog, error) {
	w := &eventLog{atLast: map[string]bool{}}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			w.note(sc.Bytes())
		}
		f.Close()
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	w.f = f
	return w, nil
}

func eventTime(b []byte) time.Time {
	var e struct {
		Timestamp time.Time `json:"timestamp"`
	}
	json.Unmarshal(b, &e)
	return e.Timestamp
}

// note remembers an event as recorded; it reports false for one already
// recorded or older than the last.
func (w *eventLog) note(b []byte) bool {
	t := eventTime(b)
	switch {
	case t.Before(w.last):
		return false
	case t.After(w.last):
		w.last = t
		clear(w.atLast)
	case w.atLast[string(b)]:
		return false
	}
	w.atLast[string(b)] = true
	return true
}

func (w *eventLog) add(b []byte) {
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.note(b) {
		return
	}
	w.f.Write(append(append([]byte{}, b...), '\n'))
}

func (w *eventLog) close() { w.f.Close() }

// freeze ends the recording of a run and turns it into the data set, in
// the background. The analysis stays in the engine until then, so that its
// motifs can be read.
func (d *datasets) freeze(runID int64) {
	d.mu.Lock()
	if d.freezing[runID] {
		d.mu.Unlock()
		return
	}
	d.freezing[runID] = true
	rec := d.recs[runID]
	delete(d.recs, runID)
	d.busy.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.busy.Done()
		defer func() {
			d.mu.Lock()
			delete(d.freezing, runID)
			d.mu.Unlock()
		}()
		ctx := context.Background()
		run, err := d.s.db.Run(ctx, runID)
		if err != nil {
			log.Printf("data set of run %d: %v", runID, err)
			return
		}
		if rec != nil {
			for _, cancel := range rec.topics {
				cancel()
			}
			rec.wg.Wait()
		} else {
			d.s.sync.hold(run.AnalysisID)
		}
		defer func() {
			d.s.sync.release(run.AnalysisID)
			if a, err := d.s.db.Analysis(ctx, run.AnalysisID); err == nil {
				d.s.pushAll(ctx, a.Sources)
			}
		}()
		if run.Dataset != catalogue.DatasetRecording && run.Dataset != catalogue.DatasetFreezing {
			return
		}
		d.s.db.SetDataset(ctx, runID, catalogue.DatasetFreezing, 0, "", nil)
		a, err := d.s.db.Analysis(ctx, run.AnalysisID)
		if err == nil {
			d.s.pushAll(ctx, a.Sources) // the analysis back in the engine, for its motifs
			err = d.write(ctx, run, a)
		}
		if err != nil {
			log.Printf("data set of run %d: %v", runID, err)
			d.s.db.SetDataset(ctx, runID, catalogue.DatasetFailed, 0, err.Error(), nil)
		}
	}()
}

// datasetSummary is kept with the run, for its page.
type datasetSummary struct {
	Sources    []sourceSummary  `json:"sources"`
	Comparison *compareSummary  `json:"comparison,omitempty"`
	Files      map[string]int64 `json:"files"`
}

type sourceSummary struct {
	Name     string         `json:"name"`
	Slug     string         `json:"slug"`
	Events   map[string]int `json:"events"`
	Total    int            `json:"total"`
	Motifs   int            `json:"motifs"`
	Absences int            `json:"absences"`
	AbsentS  float64        `json:"absent_s"`
}

// absence is a period of a run in which a source was not heard.
type absence struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Seconds float64   `json:"seconds"`
	States  []string  `json:"states"` // what the engine reported meanwhile: idle, connecting, removed, paused, …
}

// minAbsence is the shortest absence a data set reports; shorter ones are
// the moments of connecting at the start.
const minAbsence = time.Second

// absences reads from the recorded state changes when a source was not
// connected between from and to.
func absences(lines []stateLine, stream string, from, to time.Time) []absence {
	var own []stateLine
	for _, l := range lines {
		if l.Stream == stream {
			own = append(own, l)
		}
	}
	sort.SliceStable(own, func(i, j int) bool { return own[i].At.Before(own[j].At) })
	state, t := "unknown", from
	var out []absence
	var cur *absence
	flush := func(until time.Time) {
		if state == "connected" || !until.After(t) {
			return
		}
		if cur == nil {
			cur = &absence{From: t}
		}
		cur.To = until
		if len(cur.States) == 0 || cur.States[len(cur.States)-1] != state {
			cur.States = append(cur.States, state)
		}
	}
	end := func() {
		if cur != nil {
			if d := cur.To.Sub(cur.From); d >= minAbsence {
				cur.Seconds = d.Round(time.Millisecond).Seconds()
				out = append(out, *cur)
			}
			cur = nil
		}
	}
	for _, l := range own {
		if !l.At.After(from) {
			state = l.State
			continue
		}
		if !l.At.Before(to) {
			break
		}
		flush(l.At)
		if l.State == "connected" {
			end()
		}
		state, t = l.State, l.At
	}
	flush(to)
	end()
	return out
}

type compareSummary struct {
	Seconds      int               `json:"seconds"`
	Correlations int               `json:"correlations"`
	Last         []json.RawMessage `json:"last_pairs"`
}

// write builds the data set's files from the recording, then removes the
// recording.
func (d *datasets) write(ctx context.Context, run catalogue.Run, a catalogue.Analysis) error {
	dir := d.runDir(run.ID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	end := run.Stopped
	if end.IsZero() {
		end = run.Ends
	}
	// Only what falls within the run: the subscriptions may deliver a little
	// before its start and until the recording is let go. The times kept are
	// whole seconds, so the second of the end is in.
	inRun := func(path string, fn func([]byte) error) error {
		return eachLine(path, func(b []byte) error {
			if t := eventTime(b); t.Before(run.Started) || !t.Before(end.Add(time.Second)) {
				return nil
			}
			return fn(b)
		})
	}
	revs, err := d.s.db.Revisions(ctx, a.ID)
	if err != nil {
		return err
	}
	var used []catalogue.Revision
	for _, r := range revs {
		if r.Revision >= run.Revision && (r.Revision == run.Revision || r.Created.Before(end)) {
			used = append(used, r)
		}
	}
	sort.Slice(used, func(i, j int) bool { return used[i].Revision < used[j].Revision })
	sum := datasetSummary{Files: map[string]int64{}}
	var states []stateLine
	eachLine(filepath.Join(dir, statesFile), func(b []byte) error {
		var l stateLine
		if json.Unmarshal(b, &l) == nil {
			states = append(states, l)
		}
		return nil
	})
	absent := map[string][]absence{}
	for _, src := range a.Sources {
		absent[src.Stream.EngineID()] = absences(states, src.Stream.EngineID(), run.Started, end)
	}
	compare := a.Spec.Compare
	if compare == nil && len(a.Sources) >= 2 {
		compare = catalogue.DefaultCompare // saved before there was a choice
	}

	// The JSON with everything.
	err = writeGz(filepath.Join(dir, fileJSON+".gz"), func(w io.Writer) error {
		head, _ := json.Marshal(map[string]any{
			"analysis": map[string]any{"title": a.Name, "description": a.Description},
			"run": map[string]any{"number": run.Number, "started": run.Started, "ended": end, "started_by": run.StartedBy, "stopped_by": run.StoppedBy,
				"revision_at_start": run.Revision, "offline_stop_min": run.OfflineStop, "ended_because": run.EndReason, "end_note": run.EndNote},
			"revisions": used,
			"time":      "wall time at the engine: the sources are aligned to a few seconds, not to their stations' clocks",
		})
		w.Write(head[:len(head)-1])
		io.WriteString(w, `,"sources":[`)
		for i, src := range a.Sources {
			if i > 0 {
				io.WriteString(w, ",")
			}
			st := src.Stream
			ss := sourceSummary{Name: st.Texts.Name, Slug: st.Slug, Events: map[string]int{}}
			gaps := absent[st.EngineID()]
			if gaps == nil {
				gaps = []absence{}
			}
			ss.Absences = len(gaps)
			for _, g := range gaps {
				ss.AbsentS += g.Seconds
			}
			ss.AbsentS = round3(ss.AbsentS)
			motifs := json.RawMessage(`null`)
			if m, err := d.s.eng.Data(ctx, st.EngineID(), a.EngineID(), "motifs", nil); err == nil {
				motifs = m
				var ms struct {
					Motifs []any `json:"motifs"`
				}
				json.Unmarshal(m, &ms)
				ss.Motifs = len(ms.Motifs)
			}
			meta, _ := json.Marshal(map[string]any{
				"name": st.Texts.Name, "slug": st.Slug, "place": st.Texts.Place, "credit": st.Texts.Credit, "sample_rate": st.SampleRate,
				"lat": st.Texts.Lat, "lon": st.Texts.Lon, "settings": src.Spec, "motifs_at_end": motifs, "absences": gaps,
			})
			w.Write(meta[:len(meta)-1])
			io.WriteString(w, `,"events":[`)
			n := 0
			err := inRun(filepath.Join(dir, st.EngineID()+".jsonl"), func(b []byte) error {
				if n > 0 {
					io.WriteString(w, ",")
				}
				n++
				var e struct {
					Type string `json:"type"`
				}
				json.Unmarshal(b, &e)
				ss.Events[e.Type]++
				_, err := w.Write(b)
				return err
			})
			if err != nil {
				return err
			}
			ss.Total = n
			io.WriteString(w, "]}")
			sum.Sources = append(sum.Sources, ss)
		}
		io.WriteString(w, "]")
		cpath := filepath.Join(dir, groupTopic+".jsonl")
		if _, err := os.Stat(cpath); err == nil {
			cs := &compareSummary{}
			io.WriteString(w, `,"comparison":{"analyses":`)
			b, _ := json.Marshal(compare)
			w.Write(b)
			io.WriteString(w, `,"note":"activity per second is in comparison.csv","correlations":[`)
			n := 0
			err := inRun(cpath, func(b []byte) error {
				var e struct {
					Type    string `json:"type"`
					Payload struct {
						Pairs []json.RawMessage `json:"pairs"`
					} `json:"payload"`
				}
				json.Unmarshal(b, &e)
				if e.Type == "group.activity" {
					cs.Seconds++
					return nil
				}
				if n > 0 {
					io.WriteString(w, ",")
				}
				n++
				cs.Correlations++
				cs.Last = e.Payload.Pairs
				_, err := w.Write(b)
				return err
			})
			if err != nil {
				return err
			}
			io.WriteString(w, "]}")
			sum.Comparison = cs
		}
		_, err := io.WriteString(w, "}\n")
		return err
	})
	if err != nil {
		return err
	}

	// The events as CSV, a column naming the source. Plain CSV with one
	// header row, so that spreadsheets read it; what the run was and its
	// settings are in the JSON.
	err = writeGz(filepath.Join(dir, fileEvents+".gz"), func(w io.Writer) error {
		cw := csv.NewWriter(w)
		cw.Write(eventColumns)
		for _, src := range a.Sources {
			// An absence goes in at its beginning, so that the rows of a
			// source stay in time order.
			gaps := absent[src.Stream.EngineID()]
			gap := func(g absence) {
				row := make([]string, len(eventColumns))
				row[0], row[1], row[3], row[5] = src.Stream.Slug, g.From.Format("2006-01-02T15:04:05.000Z07:00"), "source.absent", strings.Join(g.States, " ")
				row[8] = strconv.FormatFloat(g.Seconds, 'f', 3, 64)
				cw.Write(row)
			}
			err := inRun(filepath.Join(dir, src.Stream.EngineID()+".jsonl"), func(b []byte) error {
				t := eventTime(b)
				for len(gaps) > 0 && !gaps[0].From.After(t) {
					gap(gaps[0])
					gaps = gaps[1:]
				}
				cw.Write(eventRow(src.Stream.Slug, b))
				return cw.Error()
			})
			if err != nil {
				return err
			}
			for _, g := range gaps {
				gap(g)
			}
		}
		cw.Flush()
		return cw.Error()
	})
	if err != nil {
		return err
	}

	// The comparison as CSV.
	if sum.Comparison != nil {
		names := map[string]string{}
		for _, src := range a.Sources {
			names[src.Stream.EngineID()] = src.Stream.Slug
		}
		err = writeGz(filepath.Join(dir, fileCompare+".gz"), func(w io.Writer) error {
			cw := csv.NewWriter(w)
			cw.Write(compareColumns)
			err := inRun(filepath.Join(dir, groupTopic+".jsonl"), func(b []byte) error {
				for _, row := range compareRows(names, b) {
					cw.Write(row)
				}
				return cw.Error()
			})
			cw.Flush()
			if err != nil {
				return err
			}
			return cw.Error()
		})
		if err != nil {
			return err
		}
	}

	var total int64
	for _, f := range []string{fileJSON, fileEvents, fileCompare} {
		if fi, err := os.Stat(filepath.Join(dir, f+".gz")); err == nil {
			sum.Files[f] = fi.Size()
			total += fi.Size()
		}
	}
	summary, _ := json.Marshal(sum)
	if err := d.s.db.SetDataset(ctx, run.ID, catalogue.DatasetReady, total, "", summary); err != nil {
		return err
	}
	// The recording is in the data set now.
	logs, _ := filepath.Glob(filepath.Join(dir, "*.jsonl")) // with the states
	for _, l := range logs {
		os.Remove(l)
	}
	return nil
}

func writeGz(path string, fn func(io.Writer) error) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	bw := bufio.NewWriterSize(f, 256<<10)
	zw := gzip.NewWriter(bw)
	err = fn(zw)
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = bw.Flush()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// eachLine hands every line of a recording to fn; a missing file has none.
func eachLine(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if err := fn(sc.Bytes()); err != nil {
			return err
		}
	}
	return sc.Err()
}

var eventColumns = []string{"source", "timestamp", "position_s", "type", "id", "status", "fundamental_hz", "strength", "duration_s", "motif_id", "label"}

func optNum(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(*p, 'g', -1, 64)
}

// eventRow is one event of a source as a CSV row.
func eventRow(source string, b []byte) []string {
	var e struct {
		Type      string  `json:"type"`
		Timestamp string  `json:"timestamp"`
		Position  float64 `json:"position_s"`
		Label     string  `json:"label"`
		Payload   struct {
			ID          string   `json:"id"`
			Status      string   `json:"status"`
			Fundamental *float64 `json:"fundamental_hz"`
			Strength    *float64 `json:"strength"`
			DurationS   *float64 `json:"duration_s"`
			DurationMs  *float64 `json:"duration_ms"`
			MotifID     string   `json:"motif_id"`
		} `json:"payload"`
	}
	json.Unmarshal(b, &e)
	dur := optNum(e.Payload.DurationS)
	if dur == "" && e.Payload.DurationMs != nil {
		dur = strconv.FormatFloat(*e.Payload.DurationMs/1000, 'g', -1, 64)
	}
	return []string{source, e.Timestamp, strconv.FormatFloat(e.Position, 'f', 3, 64), e.Type, e.Payload.ID, e.Payload.Status,
		optNum(e.Payload.Fundamental), optNum(e.Payload.Strength), dur, e.Payload.MotifID, e.Label}
}

var compareColumns = []string{"type", "timestamp", "source", "other_source", "present", "level_db", "flux", "impulse_rate", "r", "lag_s", "baseline_r", "n"}

// compareRows turns a comparison event into CSV rows: one per source of a
// second of activity, one per pair of a correlation.
func compareRows(names map[string]string, b []byte) [][]string {
	var e struct {
		Type      string `json:"type"`
		Timestamp string `json:"timestamp"`
		Payload   struct {
			Members []struct {
				Stream      string   `json:"stream"`
				Present     bool     `json:"present"`
				LevelDB     *float64 `json:"level_db"`
				Flux        *float64 `json:"flux"`
				ImpulseRate *float64 `json:"impulse_rate"`
			} `json:"members"`
			Pairs []struct {
				A         string   `json:"a"`
				B         string   `json:"b"`
				R         *float64 `json:"r"`
				LagS      *float64 `json:"lag_s"`
				BaselineR *float64 `json:"baseline_r"`
				N         *float64 `json:"n"`
			} `json:"pairs"`
		} `json:"payload"`
	}
	json.Unmarshal(b, &e)
	var rows [][]string
	switch e.Type {
	case "group.activity":
		for _, m := range e.Payload.Members {
			rows = append(rows, []string{"activity", e.Timestamp, names[m.Stream], "", strconv.FormatBool(m.Present), optNum(m.LevelDB), optNum(m.Flux), optNum(m.ImpulseRate), "", "", "", ""})
		}
	case "group.correlation":
		for _, p := range e.Payload.Pairs {
			rows = append(rows, []string{"correlation", e.Timestamp, names[p.A], names[p.B], "", "", "", "", optNum(p.R), optNum(p.LagS), optNum(p.BaselineR), optNum(p.N)})
		}
	}
	return rows
}
