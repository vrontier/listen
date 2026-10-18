package web

import (
	"context"
	"errors"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
)

// Starting and stopping previews and runs, for the pages and for tokens
// with control alike. A refusal is said in words (why); err is a fault.
// note says what the member should know besides, such as an engine that
// has not taken the change yet.

const notTaken = "The engine has not taken it yet; it will be sent again."

// startRunAs starts a run of the analysis for its member; length 0 runs
// until stopped, offline is the minutes a source may send nothing (0:
// no such stop). token is the token that starts it, or nil.
func (s *Server) startRunAs(ctx context.Context, user int64, a catalogue.Analysis, length time.Duration, offline int, token *catalogue.APIToken) (run catalogue.Run, why, note string, err error) {
	if why, err = s.capRefusal(ctx, user, a, true); err != nil || why != "" {
		return
	}
	var ends time.Time
	if length > 0 {
		ends = time.Now().UTC().Add(length)
	}
	if token != nil {
		run, err = s.db.StartRunByToken(ctx, a.ID, user, token.ID, ends, offline)
	} else {
		run, err = s.db.StartRunWith(ctx, a.ID, user, ends, offline)
	}
	if err != nil {
		if errors.Is(err, catalogue.ErrRunActive) {
			return run, a.Name + " is already running.", "", nil
		}
		return
	}
	s.sync.endPreview(a.ID)
	s.wakeEnds()
	if s.pushSourcesCtx(ctx, a.Sources) != nil {
		note = notTaken
	}
	return
}

// stopRunAs stops the analysis's run; the data set is put together.
func (s *Server) stopRunAs(ctx context.Context, user int64, a catalogue.Analysis) (run catalogue.Run, why string, err error) {
	run, active, err := s.db.ActiveRun(ctx, a.ID)
	if err != nil {
		return
	}
	if !active {
		return run, a.Name + " is not running.", nil
	}
	if err = s.db.StopRun(ctx, a.ID, user); err != nil {
		if errors.Is(err, catalogue.ErrNoRun) {
			return run, a.Name + " is not running.", nil
		}
		return
	}
	s.sync.endTrial(a.ID)
	s.data.refresh(ctx) // freezes the data set, then lets the sources go
	if s.data.dir == "" {
		s.pushSourcesCtx(ctx, a.Sources)
	}
	return
}

// startPreviewAs lets the analysis run for ten minutes, recording nothing.
func (s *Server) startPreviewAs(ctx context.Context, user int64, a catalogue.Analysis) (until time.Time, why, note string, err error) {
	if _, inRun, _ := s.db.ActiveRun(ctx, a.ID); inRun {
		return until, a.Name + " is in a run; its live data can be read already.", "", nil
	}
	if until, on := s.sync.preview(a.ID); on {
		return until, a.Name + " is already in a preview, until " + until.UTC().Format("15:04:05") + " UTC; it is not lengthened.", "", nil
	}
	if why, err = s.capRefusal(ctx, user, a, false); err != nil || why != "" {
		return
	}
	until = time.Now().UTC().Add(previewLength)
	s.sync.startPreview(a.ID, until)
	s.wakeEnds()
	if s.pushSourcesCtx(ctx, a.Sources) != nil {
		note = notTaken
	}
	return
}

// endPreviewOf ends the analysis's preview; why says when there is none.
func (s *Server) endPreviewOf(ctx context.Context, a catalogue.Analysis) string {
	if _, ok := s.sync.preview(a.ID); !ok {
		return a.Name + " is not in a preview."
	}
	s.sync.endPreview(a.ID)
	s.pushSourcesCtx(ctx, a.Sources)
	return ""
}

// pushSourcesCtx is pushSources outside a request.
func (s *Server) pushSourcesCtx(ctx context.Context, sources []catalogue.AnalysisSource) error {
	defer s.data.refresh(ctx)
	return s.pushAll(ctx, sources)
}
