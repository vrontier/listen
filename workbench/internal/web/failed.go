package web

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/vrontier/listen/workbench/internal/catalogue"
	"github.com/vrontier/listen/workbench/internal/engine"
)

// An analysis in the engine can fail on its own (decided 2026-10-16): its
// stream and the other analyses go on. The page says so with the reason
// and offers a restart, which puts the analysis into the engine again; a
// failure during a run is told to the member by mail.

// engineAnalysisOf finds the analysis behind an engine identifier, and
// what it is: "run" (a-), "preview" (p-) or "trial" (t-).
func (s *Server) engineAnalysisOf(ctx context.Context, id string) (catalogue.Analysis, string, bool) {
	var n int64
	var kind string
	for prefix, k := range map[string]string{"a-": "run", "p-": "preview", "t-": "trial"} {
		if rest, ok := strings.CutPrefix(id, prefix); ok {
			if _, err := fmt.Sscanf(rest, "%d", &n); err == nil {
				kind = k
			}
		}
	}
	if kind == "" {
		return catalogue.Analysis{}, "", false
	}
	a, err := s.db.Analysis(ctx, n)
	return a, kind, err == nil
}

// analysisFailed mails the member when a source of their analysis fails
// during a run.
func (s *Server) analysisFailed(st engine.AnalysisState) {
	ctx := context.Background()
	a, kind, ok := s.engineAnalysisOf(ctx, st.Analysis)
	reason := "no reason given"
	if st.LastError != nil {
		reason = st.LastError.Message
	}
	log.Printf("engine: analysis %s on %s failed: %s", st.Analysis, st.Stream, reason)
	if !ok || kind != "run" {
		return
	}
	run, inRun, err := s.db.ActiveRun(ctx, a.ID)
	if err != nil || !inRun {
		return
	}
	u, err := s.db.User(ctx, a.OwnerID)
	if err != nil {
		return
	}
	source := st.Stream
	for _, src := range a.Sources {
		if src.Stream.EngineID() == st.Stream {
			source = src.Stream.Texts.Name
		}
	}
	body := fmt.Sprintf("Hello %s,\n\nin run %d of your analysis \"%s\", the analysis of %s failed in the engine:\n\n  %s\n\nIts other sources go on, and so does the run; nothing of %s is recorded until it runs again. Restart it on the analysis's page:\n%s/analyses/%d\n",
		u.Name, run.Number, a.Name, source, reason, source, s.cfg.BaseURL, a.ID)
	go func() {
		if err := s.mail.Send(u.Email, fmt.Sprintf("Run %d of %s: %s failed", run.Number, a.Name, source), body); err != nil {
			log.Printf("runs: telling of a failed analysis in run %d: %v", run.ID, err)
		}
	}()
}

// analysisRestart puts the analysis's sources into the engine again, which
// starts a failed analysis anew.
func (s *Server) analysisRestart(w http.ResponseWriter, r *http.Request) {
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	if err := s.pushSources(r, a.Sources); err != nil {
		s.redirect(w, r, fmt.Sprintf("/analyses/%d", a.ID), "Not restarted: the engine refused it ("+err.Error()+").")
		return
	}
	s.redirect(w, r, fmt.Sprintf("/analyses/%d", a.ID), "Started again.")
}
