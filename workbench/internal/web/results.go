package web

import (
	"archive/zip"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// The Results page shows a member what their runs recorded and how their
// disk quota is used, with the data sets to download or delete; below, what
// they use of the server (members-and-streams.md, Resources and caps).

type resultsData struct {
	usageData
	Datasets   []runView             // runs with a data set, newest first
	Memories   []catalogue.MemoryUse // memories by analysis and source
	DatasetPct float64               // shares of the quota, for the bar
	MemoryPct  float64
	UsedPct    int
	Free       int64
}

func (s *Server) resultsPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ctx := r.Context()
	var d resultsData
	var err error
	if d.Cap, d.OwnCap, err = s.db.MemberCap(ctx, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Now = s.acct.now(sess.User.ID)
	d.Running = s.acct.running(sess.User.ID)
	if d.Disk, err = s.db.DiskOf(ctx, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Quota = diskQuota
	d.DiskWarn = d.Disk.Total()*5 >= d.Quota*4
	pct := func(n int64) float64 { return math.Min(100, float64(n)*100/float64(d.Quota)) }
	d.DatasetPct = pct(d.Disk.Datasets)
	d.MemoryPct = math.Min(100-d.DatasetPct, pct(d.Disk.Memories))
	d.UsedPct = int(math.Round(float64(d.Disk.Total()) * 100 / float64(d.Quota)))
	d.Free = max(0, d.Quota-d.Disk.Total())

	runs, err := s.db.RunsOf(ctx, sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	revs := map[int64][]catalogue.Revision{}
	for _, run := range runs {
		if _, ok := revs[run.AnalysisID]; !ok {
			if revs[run.AnalysisID], err = s.db.Revisions(ctx, run.AnalysisID); err != nil {
				s.fail(w, r, err)
				return
			}
		}
	}
	for _, v := range viewRuns(runs, func(id int64) []catalogue.Revision { return revs[id] }) {
		if v.Dataset != catalogue.DatasetNone && v.Dataset != catalogue.DatasetDeleted {
			d.Datasets = append(d.Datasets, v)
		}
	}
	if d.Memories, err = s.db.MemoriesOf(ctx, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}

	now := time.Now().UTC()
	samples, err := s.db.UsageSince(ctx, sess.User.ID, now.Add(-31*24*time.Hour))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hourEnd := now.Truncate(time.Hour).Add(time.Hour)
	d.Day, d.DayMax = bars(samples, hourEnd, time.Hour, 24, "Jan 2 15:00")
	dayEnd := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	d.Month, d.MonthMax = bars(samples, dayEnd, 24*time.Hour, 30, "Jan 2")
	s.render(w, r, 200, "results", "Results", d)
}

// resultsDelete deletes the data sets of the runs ticked on the Results
// page. Runs of other members, and data sets not finished, are left alone.
func (s *Server) resultsDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ctx := r.Context()
	r.ParseForm()
	var n int
	var freed int64
	for _, v := range r.PostForm["run"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			continue
		}
		run, err := s.db.Run(ctx, id)
		if err != nil || !deletable(run) {
			continue
		}
		a, err := s.db.Analysis(ctx, run.AnalysisID)
		if err != nil || a.OwnerID != sess.User.ID {
			continue
		}
		if err := s.deleteDataset(ctx, sess.User.ID, run); err != nil {
			s.fail(w, r, err)
			return
		}
		n++
		freed += run.DatasetBytes
	}
	switch n {
	case 0:
		s.redirect(w, r, "/results", "Nothing deleted: tick the data sets to delete first.")
	case 1:
		s.redirect(w, r, "/results", "1 data set deleted, "+byteSize(freed)+" freed.")
	default:
		s.redirect(w, r, "/results", fmt.Sprintf("%d data sets deleted, %s freed.", n, byteSize(freed)))
	}
}

// runZip sends all files of a run's data set in one zip archive.
func (s *Server) runZip(w http.ResponseWriter, r *http.Request) {
	run, a, ok := s.ownRun(w, r)
	if !ok {
		return
	}
	if run.Dataset != catalogue.DatasetReady {
		s.notFound(w, r)
		return
	}
	base := fmt.Sprintf("%s-run-%d", catalogue.Slugify(a.Name), run.Number)
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+base+`.zip"`)
	zw := zip.NewWriter(w)
	for _, name := range []string{fileJSON, fileEvents, fileCompare} {
		rc, err := s.openDataset(run, name)
		if err != nil {
			continue // comparison.csv only with a comparison
		}
		f, err := zw.CreateHeader(&zip.FileHeader{Name: base + "-" + name, Method: zip.Deflate, Modified: run.Stopped})
		if err == nil {
			_, err = io.Copy(f, rc)
		}
		rc.Close()
		if err != nil {
			return // the archive is cut off; the browser reports a failed download
		}
	}
	zw.Close()
}

// percent formats a share of the bar for a style attribute.
func percent(v float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(v, 'f', 2, 64), "0"), ".") + "%"
}
