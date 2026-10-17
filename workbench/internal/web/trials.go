package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// A trial tries changed settings next to a running run, without touching
// it (design.md, question 16, and Runs). Its settings come from the
// analysis's settings form; "keep" ends the run and saves them as the next
// revision, "discard" ends the trial. It ends by itself after an hour, or
// with the run.

// trialOf returns the analysis's trial while its run lasts.
func (s *Server) trialOf(r *http.Request, a catalogue.Analysis) (trial, bool) {
	t, ok := s.sync.trialOf(a.ID)
	if !ok {
		return t, false
	}
	if _, inRun, _ := s.db.ActiveRun(r.Context(), a.ID); !inRun {
		return trial{}, false
	}
	return t, true
}

func (s *Server) analysisTrialStart(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	if _, inRun, _ := s.db.ActiveRun(r.Context(), a.ID); !inRun {
		s.redirect(w, r, analysisPath(a), "A trial runs next to a run; outside a run, save the settings and start a run or a preview.")
		return
	}
	var ids []int64
	for _, src := range a.Sources {
		ids = append(ids, src.Stream.ID)
	}
	errs := map[string]string{}
	ready, problem, _ := s.sync.status()
	r.ParseForm()
	forms, settings, warns, err := s.checkSettings(r, a, ids, errs, ready, problem)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(errs) > 0 {
		d, err := s.analysisData(r, a)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for i := range d.Sources {
			if f, ok := forms[d.Sources[i].Stream.ID]; ok {
				d.Sources[i].Form = f
			}
		}
		d.Errs, d.Warnings, d.Err = errs, warns, "The trial was not started. Please check the marked fields."
		s.renderAnalysis(w, r, 422, d)
		return
	}
	if _, running := s.sync.trialOf(a.ID); !running {
		if why, err := s.capRefusal(r.Context(), sess.User.ID, a, false); err != nil || why != "" {
			if err != nil {
				s.fail(w, r, err)
				return
			}
			s.redirect(w, r, analysisPath(a), "No trial: "+why)
			return
		}
	}
	s.sync.startTrial(a.ID, settings)
	note, _ := json.Marshal(map[string]any{"settings": settings})
	s.db.Note(r.Context(), sess.User.ID, "analysis", a.ID, "trial start", string(note))
	flash := "Trial started next to the run, for up to an hour. It learns in memory only; the run and its data set are not touched."
	if len(warns) > 0 {
		flash += " Note: " + strings.Join(warns, " ")
	}
	if err := s.pushSources(r, a.Sources); err != nil {
		flash += " The engine has not taken it yet; it will be sent again."
	}
	s.redirect(w, r, analysisPath(a), flash)
}

func (s *Server) analysisTrialEnd(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	if _, ok := s.sync.endTrial(a.ID); ok {
		s.db.Note(r.Context(), sess.User.ID, "analysis", a.ID, "trial discard", "")
	}
	s.pushSources(r, a.Sources)
	s.redirect(w, r, analysisPath(a), "Trial discarded.")
}

// analysisTrialKeep ends the run, whose data set is frozen as it is, and
// saves the trial's settings as the next revision, for the next run.
func (s *Server) analysisTrialKeep(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	t, ok := s.trialOf(r, a)
	if !ok {
		s.redirect(w, r, analysisPath(a), "There is no trial to keep.")
		return
	}
	run, _, err := s.db.ActiveRun(r.Context(), a.ID)
	if err == nil {
		err = s.db.StopRun(r.Context(), a.ID, sess.User.ID)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.sync.endTrial(a.ID)
	var ids []int64
	for _, src := range a.Sources {
		ids = append(ids, src.Stream.ID)
	}
	saved, err := s.db.SaveRevision(r.Context(), a.ID, sess.User.ID, ids, t.Settings, a.Spec.Compare)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.db.Note(r.Context(), sess.User.ID, "analysis", a.ID, "trial keep", fmt.Sprintf(`{"revision":%d}`, saved.Revision))
	s.data.refresh(r.Context())
	s.pushSources(r, a.Sources)
	s.redirect(w, r, analysisPath(a), fmt.Sprintf("Run %d stopped; its data set is being put together. The trial's settings are saved as revision %d: start a new run when ready.", run.Number, saved.Revision))
}
