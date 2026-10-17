package web

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// Runs that end by themselves (design.md, Runs): when a source has sent
// nothing for the run's offline stop, when none of its sources has sent
// anything for catalogue.SilentStop, or when its member's data reach 110 %
// of the disk quota. The member is told by mail, as when a run reaches its
// planned end.

// heard remembers when each stream last delivered audio, as the engine
// reports it, so that a stream the engine dropped in between is not taken
// for one that never sent anything.
type heard struct {
	mu   sync.Mutex
	last map[string]time.Time // engine stream id → last audio
}

func (h *heard) note(id string, t time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.last == nil {
		h.last = map[string]time.Time{}
	}
	if t.After(h.last[id]) {
		h.last[id] = t
	}
}

func (h *heard) at(id string) time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.last[id]
}

// runVerdict says whether a run should end because its sources stopped
// sending, and why in words; "" when it goes on. lastAudio gives a source's
// last audio, zero when it has sent none.
func runVerdict(run catalogue.Run, a catalogue.Analysis, lastAudio func(catalogue.Stream) time.Time, now time.Time) (reason, note string) {
	if len(a.Sources) == 0 {
		return "", ""
	}
	allSilent := true
	for _, src := range a.Sources {
		since := lastAudio(src.Stream)
		if since.Before(run.Started) {
			since = run.Started
		}
		quiet := now.Sub(since)
		if run.OfflineStop > 0 && quiet >= time.Duration(run.OfflineStop)*time.Minute {
			return catalogue.EndOffline, fmt.Sprintf("%s sent nothing for %d minutes", src.Stream.Texts.Name, run.OfflineStop)
		}
		if quiet < catalogue.SilentStop {
			allSilent = false
		}
	}
	if allSilent {
		mins := int(catalogue.SilentStop / time.Minute)
		if len(a.Sources) == 1 {
			return catalogue.EndSilent, fmt.Sprintf("its source sent nothing for %d minutes", mins)
		}
		return catalogue.EndSilent, fmt.Sprintf("none of its sources sent anything for %d minutes", mins)
	}
	return "", ""
}

// watchRuns ends the runs whose sources stopped sending or whose member
// went over the disk quota by a tenth.
func (s *Server) watchRuns(ctx context.Context) {
	runs, err := s.db.ActiveRuns(ctx)
	if err != nil || len(runs) == 0 {
		return
	}
	// While the engine is away, the sources are not to blame.
	if ready, _, _ := s.sync.status(); !ready {
		return
	}
	streams, err := s.eng.Streams(ctx)
	if err != nil {
		return
	}
	for _, st := range streams {
		if st.LastData != nil {
			s.heard.note(st.ID, *st.LastData)
		}
	}
	now := time.Now().UTC()
	lastAudio := func(st catalogue.Stream) time.Time { return s.heard.at(st.EngineID()) }
	over := map[int64]bool{} // members over the quota by a tenth
	for _, run := range runs {
		a, err := s.db.Analysis(ctx, run.AnalysisID)
		if err != nil {
			continue
		}
		reason, note := runVerdict(run, a, lastAudio, now)
		if reason == "" {
			o, seen := over[a.OwnerID]
			if !seen {
				used, err := s.diskBytes(ctx, a.OwnerID)
				o = err == nil && used*10 >= diskQuota*11
				over[a.OwnerID] = o
			}
			if o {
				reason, note = catalogue.EndDisk, "your data sets and memories went over your disk quota of "+byteSize(diskQuota)+" by a tenth"
			}
		}
		if reason != "" {
			s.endRunBecause(ctx, run, a, reason, note)
		}
	}
}

// endRunBecause ends a run by itself and lets its sources go.
func (s *Server) endRunBecause(ctx context.Context, run catalogue.Run, a catalogue.Analysis, reason, note string) {
	ended, err := s.db.EndRun(ctx, run.ID, reason, note)
	if err != nil || !ended {
		if err != nil {
			log.Printf("runs: end %d: %v", run.ID, err)
		}
		return
	}
	log.Printf("runs: run %d of analysis %d ended: %s", run.ID, a.ID, note)
	s.sync.endTrial(a.ID)
	s.data.refresh(ctx) // freezes the data set, then lets the sources go
	if s.data.dir == "" {
		s.pushAll(ctx, a.Sources)
	}
	s.syncGroups(ctx)
}

// tellRunEnds mails each member whose run ended by itself, once.
func (s *Server) tellRunEnds(ctx context.Context) {
	runs, err := s.db.UntoldRuns(ctx)
	if err != nil {
		log.Printf("runs: %v", err)
		return
	}
	for _, run := range runs {
		a, err := s.db.Analysis(ctx, run.AnalysisID)
		if err == nil {
			var u catalogue.User
			if u, err = s.db.User(ctx, a.OwnerID); err == nil {
				err = s.mail.Send(u.Email, fmt.Sprintf("Run %d of %s ended", run.Number, a.Name), runEndMail(s.cfg.BaseURL, run, a))
			}
		}
		if err != nil {
			log.Printf("runs: telling of run %d: %v", run.ID, err)
		}
		// Told or not, never again: a mail that cannot go now would
		// otherwise be tried every half minute.
		s.db.NoteRunTold(ctx, run.ID)
	}
}

func runEndMail(base string, run catalogue.Run, a catalogue.Analysis) string {
	why := run.EndNote
	if why == "" {
		why = "it was stopped"
	}
	return fmt.Sprintf("Run %d of your analysis \"%s\" ended at %s UTC: %s.\n\nIts data set is being put together. You find it here:\n%s/runs/%s\n",
		run.Number, a.Name, run.Stopped.UTC().Format("2006-01-02 15:04"), why, base, strconv.FormatInt(run.ID, 10))
}
