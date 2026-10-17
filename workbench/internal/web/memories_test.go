package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vrontier/listen/workbench/internal/catalogue"
)

// The memories' sizes, as the engine reports them during a run, count
// against the disk quota also after the run.
func TestMemoriesCountOnDisk(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "x", "", []int64{st.ID}, nil)
	member := r.loginAs("m@example.org")
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	_, page, _ := r.get(member, aURL)
	csrf := csrfOf(t, page)
	if code, _, _ := r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}}); code != http.StatusSeeOther {
		t.Fatalf("start: %d", code)
	}
	r.srv.measureMemories(ctx)
	k, _ := r.db.DiskOf(ctx, u.ID)
	if k.Memories <= 0 {
		t.Fatalf("memories not counted: %+v", k)
	}
	_, page, _ = r.get(member, aURL)
	if !strings.Contains(page, "in use") {
		t.Fatal("memory not listed")
	}
	_, _, hdr := r.post(member, aURL+"/memories/"+strconv.FormatInt(st.ID, 10)+"/delete", url.Values{"csrf": {csrf}, "key": {"no-such"}})
	if _, page, _ = r.get(member, hdr.Get("Location")); !strings.Contains(page, "no such memory") {
		t.Fatal("deleting a missing memory not explained")
	}
	r.post(member, aURL+"/stop", url.Values{"csrf": {csrf}})

	// After the run the sizes still count; a full quota refuses the next run.
	r.db.SetMemorySizes(ctx, a.ID, st.ID, []catalogue.MemorySize{{Key: "big", Bytes: diskQuota}})
	_, _, hdr = r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}})
	if _, inRun, _ := r.db.ActiveRun(ctx, a.ID); inRun {
		t.Fatal("started with memories filling the quota")
	}
	if _, page, _ = r.get(member, hdr.Get("Location")); !strings.Contains(page, "memories (512.0 MB)") {
		t.Fatal("refusal does not name the memories")
	}
	if _, page, _ = r.get(member, "/results"); !strings.Contains(page, "memories 512.0 MB") {
		t.Fatal("results page does not show the memories")
	}
}
