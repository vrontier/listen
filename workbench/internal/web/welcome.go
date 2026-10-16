package web

import (
	"math"
	"net/http"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// The Home page is where a member lands: the way through the Workbench,
// step by step, with links, and where they stand at each step.

type welcomeData struct {
	Name       string
	Sources    int // own
	Shared     int // shared with the member
	Analyses   int
	Running    int // runs going on
	Finished   int
	Datasets   int // finished data sets kept
	Disk       catalogue.Disk
	Quota      int64
	UsedPct    int
	Tokens     int // valid tokens
	Control    int // of them, those allowed control
	ByTokens   int // runs going on that a token started
	Accesses   int // with them in the last 24 hours
	MaxSources int
	Publishing bool
	Admin      bool
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
	shared, err := s.db.SharedWith(ctx, user)
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
	tokens, err := s.db.APITokensOf(ctx, user)
	if fail(err) {
		return
	}
	activity, err := s.db.ActivityOf(ctx, user)
	if fail(err) {
		return
	}
	d.Sources, d.Shared, d.Analyses = len(own), len(shared), len(as)
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
	for _, t := range tokens {
		if t.Revoked.IsZero() {
			d.Tokens++
			if t.Has(catalogue.ScopeControl) {
				d.Control++
				if _, active, err := s.db.TokenRuns(ctx, t.ID, time.Now()); err == nil {
					d.ByTokens += active
				}
			}
		}
	}
	for _, a := range activity {
		d.Accesses += a.Day
	}
	s.render(w, r, 200, "welcome", "Home", d)
}
