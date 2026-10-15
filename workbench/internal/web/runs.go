package web

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
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
	Active    bool
	Comparing bool   // the run's analysis compares its sources now
	RevTo     int    // the last revision in force during the run
	Export    string // query of the export for the run's period
	TooLong   bool   // longer than an export can cover
	Duration  string
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
		if v.Active {
			end = now
		}
		for _, rev := range revs {
			if rev.Revision > v.RevTo && rev.Created.Before(end) {
				v.RevTo = rev.Revision
			}
		}
		q := url.Values{"from": {r.Started.Format(time.RFC3339)}, "to": {end.Add(time.Second).Format(time.RFC3339)}}
		v.Export = q.Encode()
		v.TooLong = end.Sub(r.Started) > 31*24*time.Hour
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
	var ends time.Time
	if length > 0 {
		ends = time.Now().UTC().Add(length)
	}
	if _, err := s.db.StartRun(r.Context(), a.ID, sess.User.ID, ends); err != nil {
		if errors.Is(err, catalogue.ErrRunActive) {
			s.redirect(w, r, back, a.Name+" is already running.")
			return
		}
		s.fail(w, r, err)
		return
	}
	flash := "Run of " + a.Name + " started. Its sources stay connected until it ends, whether or not a page is open."
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
	if err := s.db.StopRun(r.Context(), a.ID, sess.User.ID); err != nil {
		if errors.Is(err, catalogue.ErrNoRun) {
			s.redirect(w, r, backTo(r, analysisPath(a)), a.Name+" was not running.")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.pushSources(r, a.Sources)
	s.redirect(w, r, backTo(r, analysisPath(a)), "Run of "+a.Name+" stopped. Sources in draft now connect only while a page of theirs is open.")
}

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
	}
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
	comparing := map[int64]bool{}
	for _, a := range as {
		if revs[a.ID], err = s.db.Revisions(ctx, a.ID); err != nil {
			s.fail(w, r, err)
			return
		}
		comparing[a.ID] = s.compares(a)
	}
	d := runsData{RunLengths: runLengths}
	running := map[int64]bool{}
	for _, v := range viewRuns(runs, func(id int64) []catalogue.Revision { return revs[id] }) {
		v.Comparing = comparing[v.AnalysisID]
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
