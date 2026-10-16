package web

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
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
	for _, want := range []string{"Your space", "of 512.0 MB used", runURL + "/all.zip", "Hum watch", "Last 30 days", ">now<", ">-23<"} {
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
	if _, page, _ = r.get(member, "/results"); strings.Contains(page, runURL+"/all.zip") || !strings.Contains(page, "No data sets yet") {
		t.Fatal("a deleted data set is still listed")
	}
}
