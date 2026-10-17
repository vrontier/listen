package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
)

// Tools for a session with a language model: short answers, and a cursor
// that says what happened since the last look, instead of a live stream
// a model cannot hold open. They read what the Workbench records of a run
// itself (one JSON line per event and source), so they need no engine call.

// summaryMinutes is how far back summarize_analysis counts by default.
const summaryMinutes = 10

// sinceLimit is how many events get_events_since lists by default.
const sinceLimit = 50

// recentLines reads the complete lines of a run's recording of one topic;
// a line being written has no newline yet and is left for the next call.
func recentLines(path string) [][]byte {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[:i]
	} else {
		return nil
	}
	var out [][]byte
	for _, l := range bytes.Split(b, []byte{'\n'}) {
		if len(bytes.TrimSpace(l)) > 0 {
			out = append(out, l)
		}
	}
	return out
}

// eventView is one event as a session sees it: the CSV's columns, the
// empty ones left out, numbers as numbers.
func eventView(sourceID int64, b []byte) map[string]any {
	row := eventRow("", b)
	out := map[string]any{"source_id": sourceID}
	for i, col := range eventColumns {
		if col == "source" || row[i] == "" {
			continue
		}
		switch col {
		case "timestamp", "type", "id", "status", "motif_id", "label":
			out[col] = row[i]
		default:
			if f, err := strconv.ParseFloat(row[i], 64); err == nil {
				out[col] = f
			}
		}
	}
	return out
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// noAnalysis and noRun say which id was not found and where the ids are.
func noAnalysis(id int64) string {
	return fmt.Sprintf("There is no analysis %d of yours; list_analyses gives the ids.", id)
}

func noRun(id int64) string {
	return fmt.Sprintf("There is no run %d of yours; list_runs gives the ids.", id)
}

func noSource(a catalogue.Analysis, id int64) string {
	var ids []string
	for _, src := range a.Sources {
		ids = append(ids, fmt.Sprintf("%d (%s)", src.Stream.ID, src.Stream.Texts.Name))
	}
	return fmt.Sprintf("Analysis %d has no source %d; its sources are %s.", a.ID, id, strings.Join(ids, ", "))
}

// noDataset says why a run's data set cannot be read, by its state.
func noDataset(run catalogue.Run) string {
	switch run.Dataset {
	case catalogue.DatasetRecording:
		return fmt.Sprintf("Run %d is still going on; its data set is made when it ends. What it records now: get_events_since (analysis_id %d).", run.ID, run.AnalysisID)
	case catalogue.DatasetFreezing:
		return fmt.Sprintf("Run %d has ended and its data set is being put together; ask again in a minute.", run.ID)
	case catalogue.DatasetFailed:
		msg := fmt.Sprintf("Making the data set of run %d failed", run.ID)
		if run.DatasetNote != "" {
			msg += ": " + run.DatasetNote
		}
		return msg + "."
	case catalogue.DatasetDeleted:
		return fmt.Sprintf("The data set of run %d was deleted.", run.ID)
	}
	return fmt.Sprintf("Run %d has no data set.", run.ID)
}

// --- list_analyses, short ---

type sessionSource struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Place string `json:"place,omitempty"`
	State string `json:"state"`
}

type sessionAnalysis struct {
	ID        int64           `json:"id"`
	Title     string          `json:"title"`
	State     string          `json:"state"`
	Revision  int             `json:"revision"`
	Run       *sessionRun     `json:"run,omitempty"`
	Sources   []sessionSource `json:"sources"`
	Comparing bool            `json:"comparing,omitempty"`
}

type sessionRun struct {
	ID      int64      `json:"id"`
	Number  int        `json:"number"`
	Started time.Time  `json:"started"`
	Ends    *time.Time `json:"ends,omitempty"`
}

// shortAnalysis is what a session needs to pick ids: no owner, place
// coordinates, settings or filter rules (get_analysis has them).
func shortAnalysis(full apiAnalysis) sessionAnalysis {
	out := sessionAnalysis{ID: full.ID, Title: full.Title, State: full.State, Revision: full.Revision, Comparing: full.Comparing, Sources: []sessionSource{}}
	if full.Run != nil {
		out.Run = &sessionRun{ID: full.Run.ID, Number: full.Run.Number, Started: full.Run.Started, Ends: full.Run.Ends}
	}
	for _, s := range full.Sources {
		out.Sources = append(out.Sources, sessionSource{ID: s.ID, Name: s.Name, Place: s.Place, State: s.State})
	}
	return out
}

// --- summarize_analysis ---

type sourceNow struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Connection  string         `json:"connection,omitempty"`
	Recent      map[string]int `json:"recent_events"`
	Total       map[string]int `json:"run_events,omitempty"`
	LastEvent   string         `json:"last_event,omitempty"`
	LastAbsence *absence       `json:"last_absence,omitempty"`
	Hearing     string         `json:"hearing,omitempty"`
	DominantHz  *float64       `json:"dominant_hz,omitempty"`
	Motifs      *int           `json:"active_motifs,omitempty"`
	Unreadable  string         `json:"note,omitempty"`
}

// summarize says in a few hundred bytes where an analysis stands: in a
// run, what each source recorded lately; in a preview, what each hears;
// otherwise, its last data set.
func (s *Server) summarize(ctx context.Context, user int64, a catalogue.Analysis, minutes int) (any, error) {
	if minutes <= 0 {
		minutes = summaryMinutes
	}
	full := s.toAPIAnalysis(ctx, a)
	out := map[string]any{"id": a.ID, "title": a.Name, "state": full.State, "revision": a.Revision}
	now := time.Now().UTC()
	since := now.Add(-time.Duration(minutes) * time.Minute)
	switch full.State {
	case "run":
		run, _, err := s.db.ActiveRun(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		out["run"] = shortAnalysis(full).Run
		out["recent_minutes"] = minutes
		dir := s.data.runDir(run.ID)
		var states []stateLine
		for _, l := range recentLines(filepath.Join(dir, statesFile)) {
			var sl stateLine
			if json.Unmarshal(l, &sl) == nil {
				states = append(states, sl)
			}
		}
		var srcs []sourceNow
		for _, src := range a.Sources {
			id := src.Stream.EngineID()
			sn := sourceNow{ID: src.Stream.ID, Name: src.Stream.Texts.Name, Recent: map[string]int{}, Total: map[string]int{}}
			if es, ok := s.sync.stream(id); ok {
				sn.Connection = es.State
			} else {
				sn.Connection = src.Stream.State
			}
			for _, l := range recentLines(filepath.Join(dir, id+".jsonl")) {
				var e struct {
					Type      string    `json:"type"`
					Timestamp time.Time `json:"timestamp"`
				}
				json.Unmarshal(l, &e)
				sn.Total[e.Type]++
				if !e.Timestamp.Before(since) {
					sn.Recent[e.Type]++
				}
				sn.LastEvent = e.Timestamp.UTC().Format(time.RFC3339)
			}
			if gaps := absences(states, id, run.Started, now); len(gaps) > 0 {
				g := gaps[len(gaps)-1]
				g.Seconds = round3(g.Seconds)
				sn.LastAbsence = &g
			}
			srcs = append(srcs, sn)
		}
		out["sources"] = srcs
		out["comparing"] = full.Comparing
		out["next"] = "get_events_since for what happens from now on; get_state for one source's spectrum and features."
	case "preview":
		var srcs []sourceNow
		for _, src := range a.Sources {
			sn := sourceNow{ID: src.Stream.ID, Name: src.Stream.Texts.Name, Recent: map[string]int{}}
			if es, ok := s.sync.stream(src.Stream.EngineID()); ok {
				sn.Connection = es.State
			}
			raw, status, msg := s.sourceData(ctx, a, src.Stream.ID, "state", nil)
			if status != 200 {
				sn.Unreadable = msg
				srcs = append(srcs, sn)
				continue
			}
			var st struct {
				Features struct {
					State    string   `json:"state"`
					Dominant *float64 `json:"dominant_frequency_hz"`
					Payload  *struct {
						State    string   `json:"state"`
						Dominant *float64 `json:"dominant_frequency_hz"`
					} `json:"payload"`
				} `json:"features"`
				Recent []struct {
					Type string `json:"type"`
				} `json:"recent_events"`
				Active []json.RawMessage `json:"active_motifs"`
			}
			json.Unmarshal(raw, &st)
			sn.Hearing, sn.DominantHz = st.Features.State, st.Features.Dominant
			if p := st.Features.Payload; p != nil { // the engine's event envelope
				sn.Hearing, sn.DominantHz = p.State, p.Dominant
			}
			for _, e := range st.Recent {
				sn.Recent[e.Type]++
			}
			n := len(st.Active)
			sn.Motifs = &n
			srcs = append(srcs, sn)
		}
		out["sources"] = srcs
		out["note"] = "A preview records nothing; recent_events are the engine's last few events, not a count over time."
		out["next"] = "get_state for one source's spectrum and features; get_motifs for what it learned."
	default:
		runs, err := s.db.Runs(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if run.Dataset != catalogue.DatasetReady {
				continue
			}
			var sum datasetSummary
			json.Unmarshal(run.Summary, &sum)
			totals := map[string]int{}
			for _, ss := range sum.Sources {
				totals[ss.Name] = ss.Total
			}
			out["last_dataset"] = map[string]any{"run_id": run.ID, "number": run.Number, "started": run.Started,
				"stopped": timePtr(run.Stopped), "events_per_source": totals}
			out["next"] = fmt.Sprintf("get_dataset (run_id %d) for what it holds.", run.ID)
			break
		}
		if out["next"] == nil {
			out["next"] = "Nothing to read yet: the analysis is not running and has no data set."
		}
	}
	return out, nil
}

// --- get_events_since ---

// sinceCursor is where a session stopped reading a run: per source, the
// lines of its recording already seen. As text: "r3:1=340,2=338".
type sinceCursor struct {
	Run   int64
	Lines map[int64]int
}

func (c sinceCursor) String() string {
	ids := make([]int64, 0, len(c.Lines))
	for id := range c.Lines {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d=%d", id, c.Lines[id])
	}
	return fmt.Sprintf("r%d:%s", c.Run, strings.Join(parts, ","))
}

func parseCursor(s string) (sinceCursor, bool) {
	c := sinceCursor{Lines: map[int64]int{}}
	run, rest, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(s), "r"), ":")
	if !ok {
		return c, false
	}
	var err error
	if c.Run, err = strconv.ParseInt(run, 10, 64); err != nil {
		return c, false
	}
	for _, p := range strings.Split(rest, ",") {
		if p == "" {
			continue
		}
		k, v, ok := strings.Cut(p, "=")
		id, err1 := strconv.ParseInt(k, 10, 64)
		n, err2 := strconv.Atoi(v)
		if !ok || err1 != nil || err2 != nil || n < 0 {
			return c, false
		}
		c.Lines[id] = n
	}
	return c, true
}

// eventsSince lists what a run recorded after the cursor, oldest first
// across the sources: counts per source and type of everything up to the
// new cursor, and at most limit events (0: counts only, the cursor then
// at the end).
func (s *Server) eventsSince(ctx context.Context, a catalogue.Analysis, cursor string, sourceID int64, typ string, limit int) (any, string) {
	run, inRun, err := s.db.ActiveRun(ctx, a.ID)
	if err != nil {
		return nil, err.Error()
	}
	var from sinceCursor
	if cursor != "" {
		var ok bool
		if from, ok = parseCursor(cursor); !ok {
			return nil, `The cursor is not one this tool gave; leave it out to start from the beginning of the run, or pass the "cursor" of the last answer unchanged.`
		}
	}
	if !inRun {
		if _, ok := s.sync.preview(a.ID); ok {
			return nil, fmt.Sprintf("Analysis %d is in a preview, which records nothing; get_state shows what a source hears now.", a.ID)
		}
		if from.Run > 0 {
			return nil, fmt.Sprintf("Run %d of analysis %d has ended; everything it recorded is in its data set: get_dataset (run_id %d).", from.Run, a.ID, from.Run)
		}
		return nil, fmt.Sprintf("Analysis %d is not in a run, so nothing is being recorded; list_runs and get_dataset give its finished runs.", a.ID)
	}
	if sourceID > 0 {
		if _, ok := a.Source(sourceID); !ok {
			return nil, noSource(a, sourceID)
		}
	}
	if limit < 0 {
		limit = sinceLimit
	}
	limit = min(limit, 500)
	out := map[string]any{"run_id": run.ID, "analysis_id": a.ID}
	if from.Run != 0 && from.Run != run.ID {
		out["note"] = fmt.Sprintf("The cursor was of run %d, which has ended (get_dataset with run_id %d); this is run %d, from its beginning.", from.Run, from.Run, run.ID)
		from = sinceCursor{}
	}
	if from.Lines == nil {
		from.Lines = map[int64]int{}
	}
	type pending struct {
		src  int64
		line int
		t    time.Time
		b    []byte
	}
	dir := s.data.runDir(run.ID)
	lines := map[int64][][]byte{}
	var cands []pending
	for _, src := range a.Sources {
		id := src.Stream.ID
		if sourceID > 0 && id != sourceID {
			continue
		}
		ls := recentLines(filepath.Join(dir, src.Stream.EngineID()+".jsonl"))
		lines[id] = ls
		start := min(from.Lines[id], len(ls))
		for i := start; i < len(ls); i++ {
			var e struct {
				Type      string    `json:"type"`
				Timestamp time.Time `json:"timestamp"`
			}
			json.Unmarshal(ls[i], &e)
			if typ != "" && e.Type != typ {
				continue
			}
			cands = append(cands, pending{id, i, e.Timestamp, ls[i]})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].t.Before(cands[j].t) })
	taken := cands[:min(limit, len(cands))]
	more := len(cands) > len(taken)
	if limit == 0 { // counts only: everything is seen
		cands, more = nil, false
	}
	// Per source the new cursor stands before its first event not given,
	// or at the end of what it recorded.
	next := sinceCursor{Run: run.ID, Lines: map[int64]int{}}
	for id := range from.Lines {
		next.Lines[id] = from.Lines[id]
	}
	for id, ls := range lines {
		next.Lines[id] = len(ls)
	}
	for _, c := range cands[len(taken):] {
		if c.line < next.Lines[c.src] {
			next.Lines[c.src] = c.line
		}
	}
	names := map[int64]string{}
	for _, src := range a.Sources {
		names[src.Stream.ID] = src.Stream.Texts.Name
	}
	counts := map[string]map[string]int{}
	for id, ls := range lines {
		c := map[string]int{}
		for i := min(from.Lines[id], len(ls)); i < next.Lines[id]; i++ {
			var e struct {
				Type string `json:"type"`
			}
			json.Unmarshal(ls[i], &e)
			c[e.Type]++
		}
		counts[fmt.Sprintf("%d %s", id, names[id])] = c
	}
	events := []map[string]any{}
	for _, c := range taken {
		events = append(events, eventView(c.src, c.b))
	}
	out["counts"] = counts
	out["events"] = events
	out["cursor"] = next.String()
	out["more"] = more
	if more {
		out["next"] = "More events follow: call again with this cursor."
	} else {
		out["next"] = "Up to date. Call again later with this cursor for what is new."
	}
	return out, ""
}

// --- get_dataset, read for a session ---

type datasetFileView struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"` // uncompressed
	Lines int    `json:"lines"` // of data, without a CSV's header
}

// datasetView is a data set's summary with the files' real sizes and line
// counts, and seconds rounded.
func (s *Server) datasetView(run catalogue.Run) map[string]any {
	var sum datasetSummary
	json.Unmarshal(run.Summary, &sum)
	out := map[string]any{"run": toAPIRun(run)}
	if run.Dataset != catalogue.DatasetReady {
		out["note"] = noDataset(run)
		return out
	}
	for i := range sum.Sources {
		sum.Sources[i].AbsentS = round3(sum.Sources[i].AbsentS)
	}
	files := []datasetFileView{}
	for _, f := range []string{fileJSON, fileEvents, fileCompare} {
		if _, ok := sum.Files[f]; !ok {
			continue
		}
		v := datasetFileView{Name: f}
		v.Bytes, _ = gzSize(filepath.Join(s.data.runDir(run.ID), f+".gz"))
		v.Lines = s.datasetLines(run, f)
		files = append(files, v)
	}
	src := map[string]any{"sources": sum.Sources, "files": files}
	if c := sum.Comparison; c != nil {
		cmp := map[string]any{"seconds": c.Seconds, "correlations": c.Correlations}
		if len(c.Last) > 0 {
			cmp["last_pairs"] = c.Last
		}
		src["comparison"] = cmp
	}
	out["summary"] = src
	return out
}

// datasetLines counts a data set file's lines, a CSV's header not counted.
func (s *Server) datasetLines(run catalogue.Run, name string) int {
	rc, err := s.openDataset(run, name)
	if err != nil {
		return 0
	}
	defer rc.Close()
	var buf [64 << 10]byte
	n, last := 0, byte('\n')
	for {
		k, err := rc.Read(buf[:])
		n += bytes.Count(buf[:k], []byte{'\n'})
		if k > 0 {
			last = buf[k-1]
		}
		if err != nil {
			break
		}
	}
	if last != '\n' {
		n++
	}
	if name != fileJSON && n > 0 {
		n--
	}
	return n
}
