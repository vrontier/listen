package web

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
)

func TestResults(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	r.db.CreateUser(ctx, "o@example.org", "O", false)
	st, err := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "Hum watch", "", []int64{st.ID}, nil)
	run, err := r.db.StartRun(ctx, a.ID, u.ID, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	r.srv.pushAll(ctx, a.Sources)
	r.srv.data.refresh(ctx)
	time.Sleep(1500 * time.Millisecond)
	r.db.StopRun(ctx, a.ID, u.ID)
	r.srv.data.refresh(ctx)
	r.srv.data.busy.Wait()
	if run, _ = r.db.Run(ctx, run.ID); run.Dataset != catalogue.DatasetReady {
		t.Fatalf("data set %s", run.Dataset)
	}
	runURL := "/runs/" + strconv.FormatInt(run.ID, 10)

	member := r.loginAs("m@example.org")
	_, page, _ := r.get(member, "/results")
	csrf := csrfOf(t, page)
	for _, want := range []string{"Your space", "of 512.0 MB used", `(<a href="` + runURL + `">Results</a>)`, "Hum watch", "Last 30 days", ">now<", ">-23<", "<br>" + time.Now().UTC().Format("Jan") + "</span>"} {
		if !strings.Contains(page, want) {
			t.Fatalf("results page lacks %q", want)
		}
	}
	if strings.Contains(page, `href="/library"`) {
		t.Fatal("the menu still has the library")
	}
	if code, _, _ := r.get(member, "/library"); code != 404 {
		t.Fatalf("library: %d", code)
	}
	if code, _, hdr := r.get(member, "/account/usage"); code != http.StatusMovedPermanently || hdr.Get("Location") != "/results" {
		t.Fatalf("old usage address: %d", code)
	}

	// The run's page offers the zip, and gives the single files' sizes as
	// they download, uncompressed.
	if _, page, _ := r.get(member, runURL); !strings.Contains(page, `href="`+runURL+`/all.zip"`) || strings.Contains(page, "Size, compressed") {
		t.Fatal("run page: no zip, or sizes still called compressed")
	}
	_, events, _ := r.get(member, runURL+"/events.csv")
	if n, err := gzSize(filepath.Join(r.srv.data.runDir(run.ID), "events.csv.gz")); err != nil || n != int64(len(events)) {
		t.Fatalf("events.csv: %d bytes downloaded, %d shown (%v)", len(events), n, err)
	}

	// The Runs page leads to the results from the run's row.
	if _, page, _ := r.get(member, "/runs"); !strings.Contains(page, `(<a href="`+runURL+`">Results</a>)`) || !strings.Contains(page, "Started (UTC)") {
		t.Fatal("runs page: no way to the results")
	}

	// All files in one archive.
	code, body, _ := r.get(member, runURL+"/all.zip")
	if code != 200 {
		t.Fatalf("zip: %d", code)
	}
	zr, err := zip.NewReader(bytes.NewReader([]byte(body)), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["hum-watch-run-1-dataset.json"] || !names["hum-watch-run-1-events.csv"] {
		t.Fatalf("zip holds %v", names)
	}

	// Another member can neither download nor delete it.
	other := r.loginAs("o@example.org")
	if code, _, _ := r.get(other, runURL+"/all.zip"); code != 404 {
		t.Fatalf("other member's zip: %d", code)
	}
	_, opage, _ := r.get(other, "/results")
	r.post(other, "/results/delete", url.Values{"csrf": {csrfOf(t, opage)}, "run": {strconv.FormatInt(run.ID, 10)}})
	if run, _ = r.db.Run(ctx, run.ID); run.Dataset != catalogue.DatasetReady {
		t.Fatal("another member deleted the data set")
	}

	// Its member deletes it: the files go, the space is free, the run stays.
	_, _, hdr := r.post(member, "/results/delete", url.Values{"csrf": {csrf}, "run": {strconv.FormatInt(run.ID, 10)}})
	if _, page, _ = r.get(member, hdr.Get("Location")); !strings.Contains(page, "1 data set deleted") {
		t.Fatal("deletion not confirmed")
	}
	if run, _ = r.db.Run(ctx, run.ID); run.Dataset != catalogue.DatasetDeleted {
		t.Fatalf("data set %s", run.Dataset)
	}
	if _, err := os.Stat(r.srv.data.runDir(run.ID)); !os.IsNotExist(err) {
		t.Fatalf("files kept: %v", err)
	}
	if k, _ := r.db.DiskOf(ctx, u.ID); k.Total() != 0 {
		t.Fatalf("still counted: %d", k.Total())
	}
	if _, page, _ = r.get(member, "/results"); strings.Contains(page, `href="`+runURL+`"`) || !strings.Contains(page, "No data sets yet") {
		t.Fatal("a deleted data set is still listed")
	}
	if _, page, _ = r.get(member, "/runs"); !strings.Contains(page, "(results deleted)") {
		t.Fatal("runs page does not say the results are deleted")
	}

	// Deleting the run itself: not by another member; by its member it goes
	// from the Runs page and its page is gone.
	id := strconv.FormatInt(run.ID, 10)
	_, opage, _ = r.get(other, "/runs")
	r.post(other, "/runs/delete", url.Values{"csrf": {csrfOf(t, opage)}, "run": {id}})
	if _, err := r.db.Run(ctx, run.ID); err != nil {
		t.Fatal("another member deleted the run")
	}
	_, _, hdr = r.post(member, "/runs/delete", url.Values{"csrf": {csrf}, "run": {id}})
	if _, page, _ = r.get(member, hdr.Get("Location")); !strings.Contains(page, "1 run deleted") || strings.Contains(page, `href="`+runURL+`"`) {
		t.Fatal("run not deleted from the Runs page")
	}
	if code, _, _ := r.get(member, runURL); code != 404 {
		t.Fatalf("deleted run's page: %d", code)
	}
}

func TestHome(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.db.CreateUser(ctx, "m@example.org", "Mira", false)
	member := r.loginAs("m@example.org")
	code, page, _ := r.get(member, "/")
	if code != 200 {
		t.Fatalf("home: %d", code)
	}
	for _, want := range []string{"Welcome, Mira", `href="/sources/new"`, `href="/analyses/new"`, `href="/runs"`, `href="/results"`, `href="/ai/access"`, `href="/ai"`,
		"0 sources registered by you", "nothing running", "<h2>Now</h2>", "0 registered, 0 connected now", "0 analyses of all members, in a run or a preview", "0 of your 0 analyses", "Source code on GitHub", `<span>Home</span>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("home lacks %q", want)
		}
	}
	if strings.Contains(page, `href="/manage"`) {
		t.Fatal("a member is shown the admin page")
	}
	// Visitors still get the public front page.
	if _, page, _ := r.get(http.DefaultClient, "/"); !strings.Contains(page, "log in") || strings.Contains(page, "Welcome,") {
		t.Fatal("visitor's front page")
	}
}
