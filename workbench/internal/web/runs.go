package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// A run keeps an analysis's sources connected from its start until it is
// stopped or reaches its planned end, whether or not anyone watches
// (design.md, "Runs").

// runLengths are the planned lengths offered when a run starts; 0 runs
// until stopped.
var runLengths = []struct {
	Value string
	Label string
	D     time.Duration
}{
	{"0", "until stopped", 0},
	{"1h", "1 hour", time.Hour},
	{"6h", "6 hours", 6 * time.Hour},
	{"24h", "24 hours", 24 * time.Hour},
	{"72h", "3 days", 72 * time.Hour},
	{"168h", "7 days", 168 * time.Hour},
}

// runView is one run as the analysis page lists it.
type runView struct {
	catalogue.Run
	Active   bool
	RevTo    int // the last revision in force during the run
	Duration string
}

func (s *Server) runViews(ctx context.Context, a catalogue.Analysis, revs []catalogue.Revision) ([]runView, error) {
	runs, err := s.db.Runs(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	return viewRuns(runs, func(int64) []catalogue.Revision { return revs }), nil
}

// viewRuns prepares runs for a list; revsOf gives an analysis's revisions.
func viewRuns(runs []catalogue.Run, revsOf func(int64) []catalogue.Revision) []runView {
	now := time.Now().UTC()
	var out []runView
	for _, r := range runs {
		revs := revsOf(r.AnalysisID)
		v := runView{Run: r, Active: r.Active(now), RevTo: r.Revision}
		end := r.Stopped
		switch {
		case v.Active:
			end = now
		case end.IsZero():
			end = r.Ends // ended at its planned end, not yet closed
		}
		for _, rev := range revs {
			if rev.Revision > v.RevTo && rev.Created.Before(end) {
				v.RevTo = rev.Revision
			}
		}
		v.Duration = roundDuration(end.Sub(r.Started))
		out = append(out, v)
	}
	return out
}

func roundDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return d.Round(time.Second).String()
	case d < 48*time.Hour:
		return d.Round(time.Minute).String()
	default:
		return d.Round(time.Hour).String()
	}
}

func (s *Server) analysisRunStart(w http.ResponseWriter, r *http.Request) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	s.startRun(w, r, a, backTo(r, analysisPath(a)))
}

// backTo is where a run form returns to: the Runs page if it came from
// there, the analysis otherwise.
func backTo(r *http.Request, analysis string) string {
	if r.PostFormValue("back") == "runs" {
		return "/runs"
	}
	return analysis
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request, a catalogue.Analysis, back string) {
	sess, _ := sessionFrom(r)
	length, found := time.Duration(0), false
	for _, l := range runLengths {
		if l.Value == r.PostFormValue("length") {
			length, found = l.D, true
		}
	}
	if !found {
		s.redirect(w, r, back, "Not started: choose how long the run should last.")
		return
	}
	offline := 0
	if r.PostFormValue("offline_stop") == "on" {
		n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("offline_min")))
		if err != nil || n < 1 || n > 1440 {
			s.redirect(w, r, back, "Not started: give the minutes a source may send nothing, from 1 to 1440.")
			return
		}
		offline = n
	}
	if why, err := s.capRefusal(r.Context(), sess.User.ID, a, true); err != nil || why != "" {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.redirect(w, r, back, "Not started: "+why)
		return
	}
	var ends time.Time
	if length > 0 {
		ends = time.Now().UTC().Add(length)
	}
	if _, err := s.db.StartRunWith(r.Context(), a.ID, sess.User.ID, ends, offline); err != nil {
		if errors.Is(err, catalogue.ErrRunActive) {
			s.redirect(w, r, back, a.Name+" is already running.")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.sync.endPreview(a.ID)
	flash := "Run of " + a.Name + " started. Its sources stay connected until it ends, whether or not a page is open; what it finds goes into its data set."
	if err := s.pushSources(r, a.Sources); err != nil {
		flash += " The engine has not taken it yet; it will be sent again."
	}
	s.redirect(w, r, back, flash)
}

func (s *Server) analysisRunStop(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	run, active, err := s.db.ActiveRun(r.Context(), a.ID)
	if err == nil && active {
		err = s.db.StopRun(r.Context(), a.ID, sess.User.ID)
	}
	if err != nil || !active {
		if !active || errors.Is(err, catalogue.ErrNoRun) {
			s.redirect(w, r, backTo(r, analysisPath(a)), a.Name+" was not running.")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.sync.endTrial(a.ID)
	s.data.refresh(r.Context()) // freezes the data set, then lets the sources go
	if s.data.dir == "" {
		s.pushSources(r, a.Sources)
	}
	s.redirect(w, r, runPath(run.ID), "Run stopped. Its data set is being put together.")
}

func runPath(id int64) string { return "/runs/" + strconv.FormatInt(id, 10) }

// endRuns closes the runs that reached their planned end, and lets their
// sources go back to what their state asks.
func (s *Server) endRuns(ctx context.Context) {
	ids, err := s.db.EndRuns(ctx)
	if err != nil {
		log.Printf("runs: %v", err)
		return
	}
	for _, id := range ids {
		a, err := s.db.Analysis(ctx, id)
		if err != nil {
			continue
		}
		for _, src := range a.Sources {
			st, err := s.db.Stream(ctx, src.Stream.ID)
			if err == nil {
				err = s.sync.push(ctx, st)
			}
			if err != nil {
				log.Printf("runs: %s: %v", src.Stream.EngineID(), err)
			}
		}
	}
	if len(ids) > 0 {
		s.syncGroups(ctx)
		s.data.refresh(ctx)
	}
}

// endPreviews ends the previews past their ten minutes and the trials past
// their hour.
func (s *Server) endPreviews(ctx context.Context) {
	for _, id := range append(s.sync.endedPreviews(), s.sync.endedTrials()...) {
		if a, err := s.db.Analysis(ctx, id); err == nil {
			s.pushAll(ctx, a.Sources)
		}
	}
}

const previewLength = 10 * time.Minute

func (s *Server) analysisPreviewStart(w http.ResponseWriter, r *http.Request) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	if _, inRun, _ := s.db.ActiveRun(r.Context(), a.ID); inRun {
		s.redirect(w, r, analysisPath(a), "The analysis is running; its live view is on.")
		return
	}
	sess, _ := sessionFrom(r)
	if why, err := s.capRefusal(r.Context(), sess.User.ID, a, false); err != nil || why != "" {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.redirect(w, r, analysisPath(a), "No preview: "+why)
		return
	}
	s.sync.startPreview(a.ID, time.Now().Add(previewLength))
	flash := "Preview for ten minutes. Nothing of it is recorded."
	if err := s.pushSources(r, a.Sources); err != nil {
		flash += " The engine has not taken it yet; it will be sent again."
	}
	s.redirect(w, r, analysisPath(a), flash)
}

func (s *Server) analysisPreviewEnd(w http.ResponseWriter, r *http.Request) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	s.sync.endPreview(a.ID)
	s.pushSources(r, a.Sources)
	s.redirect(w, r, analysisPath(a), "Preview ended.")
}

// --- a run's page and its data set ---

// ownRun finds the run named in the path among the member's.
func (s *Server) ownRun(w http.ResponseWriter, r *http.Request) (catalogue.Run, catalogue.Analysis, bool) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PathValue("run"), 10, 64)
	run, err := s.db.Run(r.Context(), id)
	if err == nil {
		var a catalogue.Analysis
		if a, err = s.db.Analysis(r.Context(), run.AnalysisID); err == nil && a.OwnerID == sess.User.ID {
			return run, a, true
		}
	}
	if err != nil && !isNotFound(err) {
		s.fail(w, r, err)
	} else {
		s.notFound(w, r)
	}
	return run, catalogue.Analysis{}, false
}

type runPageData struct {
	Run      runView
	Analysis catalogue.Analysis
	Summary  datasetSummary
	Pairs    []pairSummary
	Files    []runFileView
	Used     int64 // the member's data sets together
	Quota    int64
}

type runFileView struct {
	Name  string
	Bytes int64
	About string
}

type pairSummary struct {
	A, B      string
	R         *float64
	LagS      *float64
	BaselineR *float64
}

// diskQuota is a member's disk quota (decided 2026-10-14), against which
// data sets and the memories of the member's analyses count.
const diskQuota = 512 << 20

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	run, a, ok := s.ownRun(w, r)
	if !ok {
		return
	}
	revs, err := s.db.Revisions(r.Context(), a.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := runPageData{Run: viewRuns([]catalogue.Run{run}, func(int64) []catalogue.Revision { return revs })[0], Analysis: a, Quota: diskQuota}
	json.Unmarshal(run.Summary, &d.Summary)
	names := map[string]string{}
	for _, src := range a.Sources {
		names[src.Stream.EngineID()] = src.Stream.Texts.Name
	}
	if c := d.Summary.Comparison; c != nil {
		for _, raw := range c.Last {
			var p struct {
				A, B      string
				R         *float64 `json:"r"`
				LagS      *float64 `json:"lag_s"`
				BaselineR *float64 `json:"baseline_r"`
			}
			json.Unmarshal(raw, &p)
			p.A, p.B = or(names[p.A], p.A), or(names[p.B], p.B)
			d.Pairs = append(d.Pairs, pairSummary{p.A, p.B, p.R, p.LagS, p.BaselineR})
		}
	}
	about := map[string]string{
		fileJSON:    "everything: the run, the settings of each revision, each source's events and motifs, the comparison's correlations",
		fileEvents:  "the events of all sources, a column naming the source",
		fileCompare: "the comparison: a row per source and second of activity, a row per pair and minute of correlation",
	}
	for _, f := range []string{fileJSON, fileEvents, fileCompare} {
		if n, ok := d.Summary.Files[f]; ok {
			d.Files = append(d.Files, runFileView{Name: f, Bytes: n, About: about[f]})
		}
	}
	d.Used, _ = s.diskBytes(r.Context(), sess.User.ID)
	title := fmt.Sprintf("%s, run %d", a.Name, run.Number)
	s.render(w, r, 200, "run", title, d)
}

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// runFile sends one file of a data set, uncompressed.
func (s *Server) runFile(w http.ResponseWriter, r *http.Request) {
	run, a, ok := s.ownRun(w, r)
	if !ok {
		return
	}
	name := r.PathValue("file")
	rc, err := s.openDataset(run, name)
	if err != nil {
		s.notFound(w, r)
		return
	}
	defer rc.Close()
	download := fmt.Sprintf("%s-run-%d-%s", catalogue.Slugify(a.Name), run.Number, name)
	w.Header().Set("Content-Type", datasetTypes[name])
	w.Header().Set("Content-Disposition", `attachment; filename="`+download+`"`)
	io.Copy(w, rc)
}

func (s *Server) runDatasetDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	run, _, ok := s.ownRun(w, r)
	if !ok {
		return
	}
	back := runPath(run.ID)
	if r.PostFormValue("back") == "results" {
		back = "/results"
	}
	if !deletable(run) {
		s.redirect(w, r, back, "Only a finished data set can be deleted.")
		return
	}
	if err := s.deleteDataset(r.Context(), sess.User.ID, run); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, back, "Data set deleted.")
}

// deletable reports whether a run's data set is finished, so that its
// member may delete it.
func deletable(run catalogue.Run) bool {
	return run.Dataset == catalogue.DatasetReady || run.Dataset == catalogue.DatasetFailed
}

// deleteDataset marks a run's data set deleted and removes its files.
func (s *Server) deleteDataset(ctx context.Context, by int64, run catalogue.Run) error {
	if err := s.db.DeleteDataset(ctx, run.ID, by); err != nil {
		return err
	}
	if s.data.dir != "" {
		os.RemoveAll(s.data.runDir(run.ID))
	}
	return nil
}

// syncGroups puts the engine's groups in step; a failure is logged, and the
// next change or engine restart tries again.
func (s *Server) syncGroups(ctx context.Context) {
	if err := s.sync.syncGroups(ctx); err != nil {
		log.Printf("groups: %v", err)
	}
}

// --- the Runs page ---

type runsData struct {
	Active     []runView
	Finished   []runView
	Startable  []catalogue.Analysis // analyses not running now
	RunLengths any
}

func (s *Server) runsPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ctx := r.Context()
	runs, err := s.db.RunsOf(ctx, sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	as, err := s.db.AnalysesOf(ctx, sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	revs := map[int64][]catalogue.Revision{}
	for _, a := range as {
		if revs[a.ID], err = s.db.Revisions(ctx, a.ID); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	d := runsData{RunLengths: runLengths}
	running := map[int64]bool{}
	for _, v := range viewRuns(runs, func(id int64) []catalogue.Revision { return revs[id] }) {
		if v.Active {
			d.Active = append(d.Active, v)
			running[v.AnalysisID] = true
		} else {
			d.Finished = append(d.Finished, v)
		}
	}
	for _, a := range as {
		if !running[a.ID] {
			d.Startable = append(d.Startable, a)
		}
	}
	s.render(w, r, 200, "runs", "Runs", d)
}

// runsStart starts a run of the analysis chosen on the Runs page.
func (s *Server) runsStart(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PostFormValue("analysis"), 10, 64)
	a, err := s.db.Analysis(r.Context(), id)
	if err != nil || a.OwnerID != sess.User.ID {
		s.redirect(w, r, "/runs", "Not started: choose one of your analyses.")
		return
	}
	s.startRun(w, r, a, "/runs")
}

// memories lists the memories the engine keeps for one source of an
// analysis in a run, and notes their sizes for the disk quota.
func (s *Server) memories(ctx context.Context, a catalogue.Analysis, src catalogue.AnalysisSource) ([]engine.Memory, error) {
	raw, err := s.eng.Data(ctx, src.Stream.EngineID(), a.EngineID(), "memories", nil)
	if err != nil {
		return nil, err
	}
	var m struct {
		Memories []engine.Memory `json:"memories"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	sizes := make([]catalogue.MemorySize, 0, len(m.Memories))
	for _, x := range m.Memories {
		sizes = append(sizes, catalogue.MemorySize{Key: x.Key, Bytes: x.Bytes})
	}
	if err := s.db.SetMemorySizes(ctx, a.ID, src.Stream.ID, sizes); err != nil {
		log.Printf("memory sizes %s: %v", a.EngineID(), err)
	}
	return m.Memories, nil
}

// measureMemories notes the memory sizes of every analysis in a run.
func (s *Server) measureMemories(ctx context.Context) {
	runs, err := s.db.ActiveRuns(ctx)
	if err != nil {
		return
	}
	for _, run := range runs {
		a, err := s.db.Analysis(ctx, run.AnalysisID)
		if err != nil {
			continue
		}
		for _, src := range a.Sources {
			if running(src.Stream) {
				s.memories(ctx, a, src)
			}
		}
	}
}

// analysisMemoryDelete deletes one memory of a source during a run, when
// the analysis is in the engine; the one in use cannot be deleted.
func (s *Server) analysisMemoryDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, src, ok := s.ownSource(w, r)
	if !ok {
		return
	}
	if _, inRun, _ := s.db.ActiveRun(r.Context(), a.ID); !inRun || !running(src.Stream) {
		s.redirect(w, r, analysisPath(a), "Memories can be deleted while the analysis runs, when the engine has them at hand.")
		return
	}
	key := r.PostFormValue("key")
	err := s.eng.DeleteMemory(r.Context(), src.Stream.EngineID(), a.EngineID(), key)
	switch {
	case engine.IsCode(err, "in_use"):
		s.redirect(w, r, analysisPath(a), "The memory in use cannot be deleted.")
		return
	case engine.IsCode(err, "not_found"):
		s.redirect(w, r, analysisPath(a), "There is no such memory any more.")
		return
	case err != nil:
		s.fail(w, r, err)
		return
	}
	after, _ := json.Marshal(map[string]any{"source": src.Stream.ID, "key": key})
	s.db.Note(r.Context(), sess.User.ID, "analysis", a.ID, "memory delete", string(after))
	s.memories(r.Context(), a, src)
	s.redirect(w, r, analysisPath(a), "Memory deleted.")
}
