package web

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
	"github.com/vrontier/listen/workbench/internal/engine"
)

// The Home page is where a member lands: the way through the Workbench,
// step by step, with links, and where they stand at each step.

type welcomeData struct {
	Name     string
	Sources  int // own
	Others   int // other members' sources open to every member
	Analyses int
	Running  int // runs going on
	Finished int
	Datasets int // finished data sets kept
	Disk     catalogue.Disk
	Quota    int64
	UsedPct  int
	tokenCounts
	MaxSources int
	Publishing bool
	Admin      bool
	Now        glance
}

// glance is the Home page's "Now": what the member uses and has running,
// next to the whole Workbench's figures.
type glance struct {
	Use       catalogue.Usage     // the member's share, as on the Results page
	Cap       catalogue.ServerCap // the member's cap
	Analyses  int                 // the member's analyses in a run or a preview
	Engine    engine.Usage        // the engine as a whole
	Host      engine.Host
	ServerCap catalogue.ServerCap
	Sources   int // registered, every member's
	Connected int // of them connected now
	AllRuns   int // analyses of all members in a run or a preview
}

// glanceOf gathers the figures of the Home page's "Now".
func (s *Server) glanceOf(ctx context.Context, user int64) (glance, error) {
	var g glance
	var err error
	g.Use = s.acct.now(user)
	if g.Cap, _, err = s.db.MemberCap(ctx, user); err != nil {
		return g, err
	}
	if g.ServerCap, err = s.db.ServerCap(ctx); err != nil {
		return g, err
	}
	g.Engine, g.Host = s.sync.used()
	streams, err := s.db.AllStreams(ctx)
	if err != nil {
		return g, err
	}
	g.Sources = len(streams)
	for _, st := range streams {
		if es, ok := s.sync.stream(st.EngineID()); ok && es.State == "connected" {
			g.Connected++
		}
	}
	runs, err := s.db.ActiveRuns(ctx)
	if err != nil {
		return g, err
	}
	busy := map[int64]bool{}
	for _, run := range runs {
		busy[run.AnalysisID] = true
	}
	for _, id := range s.sync.previewing() {
		busy[id] = true
	}
	g.AllRuns = len(busy)
	mine, err := s.db.AnalysesOf(ctx, user)
	if err != nil {
		return g, err
	}
	for _, a := range mine {
		if busy[a.ID] {
			g.Analyses++
		}
	}
	return g, nil
}

func (s *Server) welcome(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ctx := r.Context()
	user := sess.User.ID
	d := welcomeData{Name: sess.User.Name, Quota: diskQuota, MaxSources: maxSources, Publishing: s.cfg.Publishing, Admin: sess.User.Admin}
	fail := func(err error) bool {
		if err != nil {
			s.fail(w, r, err)
		}
		return err != nil
	}
	own, err := s.db.StreamsOf(ctx, user)
	if fail(err) {
		return
	}
	others, err := s.db.OpenStreams(ctx, user)
	if fail(err) {
		return
	}
	as, err := s.db.AnalysesOf(ctx, user)
	if fail(err) {
		return
	}
	runs, err := s.db.RunsOf(ctx, user)
	if fail(err) {
		return
	}
	if d.Disk, err = s.db.DiskOf(ctx, user); fail(err) {
		return
	}
	if d.tokenCounts, err = s.tokenCountsOf(ctx, user); fail(err) {
		return
	}
	if d.Now, err = s.glanceOf(ctx, user); fail(err) {
		return
	}
	d.Sources, d.Others, d.Analyses = len(own), len(others), len(as)
	now := time.Now().UTC()
	for _, run := range runs {
		if run.Active(now) {
			d.Running++
		} else {
			d.Finished++
		}
		if run.Dataset == catalogue.DatasetReady {
			d.Datasets++
		}
	}
	d.UsedPct = int(math.Round(float64(d.Disk.Total()) * 100 / float64(d.Quota)))
	s.render(w, r, 200, "welcome", "Home", d)
}
