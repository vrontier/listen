package web

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// The data stream for programs (design.md, question 7; members-and-
// streams.md, Tokens): a member's token, sent as "Authorization: Bearer",
// reads the member's own analyses as JSON and their live events as
// Server-Sent Events. It reads what runs; it never starts anything, and
// so never connects a source by itself.

const tokenKey ctxKey = 1

func tokenFrom(r *http.Request) (catalogue.APIToken, bool) {
	t, ok := r.Context().Value(tokenKey).(catalogue.APIToken)
	return t, ok
}

// apiError answers a program in JSON.
func apiError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func apiJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// authenticate finds the member behind the request's bearer token and
// checks its scope. A token in the address is refused, so that it does not
// end up in logs.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, scope string) (*http.Request, bool) {
	q := r.URL.Query()
	if q.Has("token") || q.Has("access_token") {
		apiError(w, http.StatusBadRequest, "token_in_address", "Send the token in the Authorization header as \"Bearer low_…\", never in the address.")
		return r, false
	}
	h := r.Header.Get("Authorization")
	secret, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || !strings.HasPrefix(secret, catalogue.TokenPrefix) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="workbench"`)
		apiError(w, http.StatusUnauthorized, "no_token", "This needs a token in the header \"Authorization: Bearer low_…\". Members create tokens under Tokens.")
		return r, false
	}
	t, u, err := s.db.APIToken(r.Context(), strings.TrimSpace(secret))
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="workbench", error="invalid_token"`)
		apiError(w, http.StatusUnauthorized, "invalid_token", "The token is unknown or revoked.")
		return r, false
	}
	if !t.Has(scope) {
		apiError(w, http.StatusForbidden, "scope", fmt.Sprintf("This token is not for %s; its member can create one that is.", map[string]string{catalogue.ScopeStream: "the data stream", catalogue.ScopeMCP: "MCP"}[scope]))
		return r, false
	}
	ctx := context.WithValue(r.Context(), sessionKey, catalogue.Session{User: u})
	return r.WithContext(context.WithValue(ctx, tokenKey, t)), true
}

func (s *Server) bearer(scope string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r, ok := s.authenticate(w, r, scope)
		if ok {
			h(w, r)
		}
	}
}

// --- what a program sees of an analysis ---

type apiSource struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Place      string   `json:"place,omitempty"`
	Lat        *float64 `json:"lat,omitempty"`
	Lon        *float64 `json:"lon,omitempty"`
	SampleRate int      `json:"sample_rate"`
	State      string   `json:"state"`
	Owner      string   `json:"owner"`
	Settings   any      `json:"settings"`
}

type apiRun struct {
	ID       int64      `json:"id"`
	Analysis int64      `json:"analysis"`
	Title    string     `json:"title"`
	Number   int        `json:"number"`
	Started  time.Time  `json:"started"`
	Ends     *time.Time `json:"ends,omitempty"`
	Stopped  *time.Time `json:"stopped,omitempty"`
	Revision int        `json:"revision"`
	Dataset  string     `json:"dataset"`
	Bytes    int64      `json:"dataset_bytes,omitempty"`
	Files    []string   `json:"files,omitempty"`
}

type apiAnalysis struct {
	ID          int64       `json:"id"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	Revision    int         `json:"revision"`
	State       string      `json:"state"` // run, preview or idle
	Run         *apiRun     `json:"run,omitempty"`
	Sources     []apiSource `json:"sources"`
	Compare     []string    `json:"compare,omitempty"`
	Comparing   bool        `json:"comparing"`
	Published   string      `json:"published,omitempty"` // its public page, while published
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func toAPIRun(r catalogue.Run) apiRun {
	out := apiRun{ID: r.ID, Analysis: r.AnalysisID, Title: r.AnalysisName, Number: r.Number, Started: r.Started,
		Ends: timePtr(r.Ends), Stopped: timePtr(r.Stopped), Revision: r.Revision, Dataset: r.Dataset, Bytes: r.DatasetBytes}
	if r.Dataset == catalogue.DatasetReady {
		var sum datasetSummary
		json.Unmarshal(r.Summary, &sum)
		for _, f := range []string{fileJSON, fileEvents, fileCompare} {
			if _, ok := sum.Files[f]; ok {
				out.Files = append(out.Files, f)
			}
		}
	}
	return out
}

func (s *Server) toAPIAnalysis(ctx context.Context, a catalogue.Analysis) apiAnalysis {
	out := apiAnalysis{ID: a.ID, Title: a.Name, Description: a.Description, Revision: a.Revision, State: "idle", Compare: a.Spec.Compare}
	if run, inRun, _ := s.db.ActiveRun(ctx, a.ID); inRun {
		out.State = "run"
		ar := toAPIRun(run)
		out.Run = &ar
	} else if _, ok := s.sync.preview(a.ID); ok {
		out.State = "preview"
	}
	out.Comparing = out.State == "run" && s.compares(a)
	if a.State == catalogue.AnalysisPublished {
		out.Published = s.cfg.BaseURL + "/research/" + a.Slug
	}
	for _, src := range a.Sources {
		var settings any
		json.Unmarshal(src.Spec, &settings)
		out.Sources = append(out.Sources, apiSource{ID: src.Stream.ID, Name: src.Stream.Texts.Name, Place: src.Stream.Texts.Place,
			Lat: src.Stream.Texts.Lat, Lon: src.Stream.Texts.Lon, SampleRate: src.Stream.SampleRate, State: src.Stream.State, Owner: src.Stream.OwnerName, Settings: settings})
	}
	return out
}

// errNotYours is a missing analysis or run, or one of another member.
var errNotYours = errors.New("no such analysis of yours")

// memberAnalysis finds one of the member's analyses.
func (s *Server) memberAnalysis(ctx context.Context, user, id int64) (catalogue.Analysis, error) {
	a, err := s.db.Analysis(ctx, id)
	if err == nil && a.OwnerID != user || isNotFound(err) {
		return a, errNotYours
	}
	return a, err
}

func (s *Server) apiFail(w http.ResponseWriter, err error) {
	if errors.Is(err, errNotYours) {
		apiError(w, http.StatusNotFound, "not_found", "There is no such analysis or run of yours.")
		return
	}
	apiError(w, http.StatusInternalServerError, "internal", "Something went wrong; it has been logged.")
}

func (s *Server) apiAnalyses(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	as, err := s.db.AnalysesOf(r.Context(), sess.User.ID)
	if err != nil {
		s.apiFail(w, err)
		return
	}
	out := []apiAnalysis{}
	for _, a := range as {
		out = append(out, s.toAPIAnalysis(r.Context(), a))
	}
	apiJSON(w, map[string]any{"analyses": out})
}

func pathID(r *http.Request, name string) int64 {
	id, _ := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id
}

func (s *Server) apiAnalysis(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, err := s.memberAnalysis(r.Context(), sess.User.ID, pathID(r, "id"))
	if err != nil {
		s.apiFail(w, err)
		return
	}
	apiJSON(w, s.toAPIAnalysis(r.Context(), a))
}

// dataPaths are the engine's data paths a reader may ask for.
func dataPath(what string) bool {
	switch {
	case what == "state", what == "motifs", what == "memories", what == "history/events", what == "history/features":
		return true
	case strings.HasPrefix(what, "motifs/") && !strings.Contains(what[7:], "/"):
		return true
	}
	return false
}

// sourceData fetches one of the engine's data paths of a source during a
// run or a preview.
func (s *Server) sourceData(ctx context.Context, a catalogue.Analysis, sourceID int64, what string, q map[string]string) (json.RawMessage, int, string) {
	src, ok := a.Source(sourceID)
	if !ok {
		return nil, http.StatusNotFound, "The analysis has no such source."
	}
	if !dataPath(what) {
		return nil, http.StatusNotFound, "Unknown data path; there are state, motifs, motifs/<id>, memories, history/events and history/features."
	}
	id, ok := s.runningID(ctx, a)
	if !ok || !running(src.Stream) {
		return nil, http.StatusConflict, "The analysis is neither in a run nor previewed; its data can be read while it runs, and its runs' data sets at any time."
	}
	v := make(map[string][]string)
	for k, x := range q {
		if x != "" {
			v[k] = []string{x}
		}
	}
	raw, err := s.eng.Data(ctx, src.Stream.EngineID(), id, what, v)
	if err != nil {
		if engine.IsCode(err, "not_found") {
			return nil, http.StatusNotFound, err.Error()
		}
		return nil, http.StatusBadGateway, err.Error()
	}
	return raw, 200, ""
}

func (s *Server) apiSourceData(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, err := s.memberAnalysis(r.Context(), sess.User.ID, pathID(r, "id"))
	if err != nil {
		s.apiFail(w, err)
		return
	}
	q := map[string]string{}
	for _, k := range []string{"from", "to", "type", "limit", "window", "memory"} {
		q[k] = r.URL.Query().Get(k)
	}
	raw, status, msg := s.sourceData(r.Context(), a, pathID(r, "source"), r.PathValue("what"), q)
	if status != 200 {
		apiError(w, status, http.StatusText(status), msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(raw)
}

// --- runs and data sets ---

func (s *Server) memberRuns(ctx context.Context, user int64) ([]apiRun, error) {
	as, err := s.db.AnalysesOf(ctx, user)
	if err != nil {
		return nil, err
	}
	out := []apiRun{}
	for _, a := range as {
		runs, err := s.db.Runs(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range runs {
			out = append(out, toAPIRun(r))
		}
	}
	slices.SortFunc(out, func(a, b apiRun) int { return b.Started.Compare(a.Started) })
	return out, nil
}

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	runs, err := s.memberRuns(r.Context(), sess.User.ID)
	if err != nil {
		s.apiFail(w, err)
		return
	}
	apiJSON(w, map[string]any{"runs": runs})
}

// memberRun finds one of the member's runs.
func (s *Server) memberRun(ctx context.Context, user, id int64) (catalogue.Run, catalogue.Analysis, error) {
	run, err := s.db.Run(ctx, id)
	if err != nil {
		if isNotFound(err) {
			err = errNotYours
		}
		return run, catalogue.Analysis{}, err
	}
	a, err := s.memberAnalysis(ctx, user, run.AnalysisID)
	return run, a, err
}

var datasetTypes = map[string]string{fileJSON: "application/json", fileEvents: "text/csv; charset=utf-8", fileCompare: "text/csv; charset=utf-8"}

// openDataset opens one file of a finished data set, uncompressed.
func (s *Server) openDataset(run catalogue.Run, name string) (io.ReadCloser, error) {
	if datasetTypes[name] == "" || run.Dataset != catalogue.DatasetReady {
		return nil, os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(s.data.runDir(run.ID), name+".gz"))
	if err != nil {
		return nil, err
	}
	zr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{zr, f}, nil
}

func (s *Server) apiRunFile(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	run, _, err := s.memberRun(r.Context(), sess.User.ID, pathID(r, "run"))
	if err != nil {
		s.apiFail(w, err)
		return
	}
	name := r.PathValue("file")
	rc, err := s.openDataset(run, name)
	if err != nil {
		apiError(w, http.StatusNotFound, "not_found", "No such file: a finished data set has dataset.json, events.csv and, with a comparison, comparison.csv.")
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", datasetTypes[name])
	io.Copy(w, rc)
}

// --- the live events ---

// streamsPerToken bounds the data streams one token holds open at once.
const streamsPerToken = 4

type tokenStreams struct {
	mu sync.Mutex
	n  map[int64]int
}

var openStreams = tokenStreams{n: map[int64]int{}}

func (t *tokenStreams) take(id int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n[id] >= streamsPerToken {
		return false
	}
	t.n[id]++
	return true
}

func (t *tokenStreams) give(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.n[id]--; t.n[id] <= 0 {
		delete(t.n, id)
	}
}

// apiEvents passes the live events of an analysis's sources, and of their
// comparison, on to a program while the analysis is in a run or a
// preview. Each event names its source: its data is
// {"source": <id>, "data": <the engine's event>}, or {"comparison": true,
// "data": …}. ?types= limits the event types, ?source= the sources. When
// the run or preview ends, an "end" event says so and the stream closes.
func (s *Server) apiEvents(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	tok, _ := tokenFrom(r)
	a, err := s.memberAnalysis(r.Context(), sess.User.ID, pathID(r, "id"))
	if err != nil {
		s.apiFail(w, err)
		return
	}
	id, ok := s.runningID(r.Context(), a)
	if !ok {
		apiError(w, http.StatusConflict, "not_running", "The analysis is neither in a run nor previewed. Its live events can be read while it runs; a token never starts it.")
		return
	}
	var types []string
	if v := r.URL.Query().Get("types"); v != "" {
		types = strings.Split(v, ",")
	}
	var only []int64
	for _, v := range r.URL.Query()["source"] {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			only = append(only, n)
		}
	}
	if !openStreams.take(tok.ID) {
		apiError(w, http.StatusTooManyRequests, "too_many_streams", fmt.Sprintf("This token holds %d data streams open already.", streamsPerToken))
		return
	}
	defer openStreams.give(tok.ID)

	ch := make(chan []byte, 512)
	wrap := func(from string) func(engine.SSE) []byte {
		return func(ev engine.SSE) []byte {
			if types != nil && !slices.Contains(types, ev.Event) {
				return nil
			}
			data := make([]byte, 0, len(ev.Data)+40)
			data = append(data, '{')
			data = append(data, from...)
			data = append(data, `,"data":`...)
			data = append(data, ev.Data...)
			data = append(data, '}')
			return sseFrame(engine.SSE{Event: ev.Event, Data: data})
		}
	}
	var leaves []func()
	defer func() {
		for _, l := range leaves {
			l()
		}
	}()
	who := "token " + tok.Name
	for _, src := range a.Sources {
		if !running(src.Stream) || only != nil && !slices.Contains(only, src.Stream.ID) {
			continue
		}
		rd := &reader{ch: ch, who: who, frame: wrap(`"source":` + strconv.FormatInt(src.Stream.ID, 10))}
		leaves = append(leaves, s.live.join(src.Stream.EngineID(), id, rd))
	}
	if only == nil && id == a.EngineID() && s.compares(a) {
		rd := &reader{ch: ch, who: who, frame: wrap(`"comparison":true`)}
		leaves = append(leaves, s.live.joinGroup(a.GroupID(), rd))
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		apiError(w, 500, "internal", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	io.WriteString(w, ": connected to "+a.Name+"\n\n")
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	check := time.NewTicker(5 * time.Second)
	defer check.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-ch:
			if _, err := w.Write(msg); err != nil {
				return
			}
			fl.Flush()
		case <-ping.C:
			io.WriteString(w, ": ping\n\n")
			fl.Flush()
		case <-check.C:
			now, ok := s.runningID(r.Context(), a)
			if !ok || now != id {
				io.WriteString(w, "event: end\ndata: {\"reason\":\"the run or preview ended\"}\n\n")
				fl.Flush()
				return
			}
		}
	}
}
