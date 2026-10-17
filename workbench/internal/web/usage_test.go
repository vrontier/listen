package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// Each analysis counts against its member; a shared connection is split
// among the members with something on it.
func TestAccounting(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ann, _ := r.db.CreateUser(ctx, "ann@example.org", "Ann", false)
	bob, _ := r.db.CreateUser(ctx, "bob@example.org", "Bob", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: ann.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a1, _ := r.db.CreateAnalysis(ctx, ann.ID, "Ann's", "", []int64{st.ID}, nil)
	a2, _ := r.db.CreateAnalysis(ctx, bob.ID, "Bob's", "", []int64{st.ID}, nil)
	acct := newAccounting(r.srv)
	rep := engine.UsageReport{
		Engine:  engine.Usage{CPUPercent: 10, MemoryMB: 100},
		Streams: map[string]engine.Usage{st.EngineID(): {CPUPercent: 4, MemoryMB: 20, InKbps: 128}},
		Analyses: map[string]engine.Usage{
			st.EngineID() + "/" + a1.EngineID(): {CPUPercent: 1, MemoryMB: 6},
			st.EngineID() + "/" + previewID(a2): {CPUPercent: 2, MemoryMB: 8},
		},
	}
	acct.report(rep)
	if u := acct.now(ann.ID); u.CPUPercent != 3 || u.MemoryMB != 16 || u.InKbps != 64 {
		t.Fatalf("Ann: %+v", u)
	}
	if u := acct.now(bob.ID); u.CPUPercent != 4 || u.MemoryMB != 18 {
		t.Fatalf("Bob: %+v", u)
	}
	if u := acct.now(0); u.CPUPercent != 10 {
		t.Fatalf("engine: %+v", u)
	}
	if items := acct.running(bob.ID); len(items) != 2 || items[0].What != "preview" {
		t.Fatalf("Bob's items: %+v", items)
	}

	// The minute's average is stored once the next minute begins.
	acct.mu.Lock()
	acct.minute = acct.minute.Add(-time.Minute)
	acct.mu.Unlock()
	acct.report(rep)
	deadline := time.Now().Add(2 * time.Second)
	for {
		got, _ := r.db.UsageSince(ctx, ann.ID, time.Now().Add(-time.Hour))
		if len(got) == 1 && got[0].CPUPercent == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stored usage: %+v", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A start that would go over a cap is refused, with the reason.
func TestCaps(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	admin, _ := r.db.CreateUser(ctx, "admin@example.org", "Admin", true)
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "A"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "x", "", []int64{st.ID}, nil)
	member := r.loginAs("m@example.org")
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	_, page, _ := r.get(member, aURL)
	csrf := csrfOf(t, page)

	// The master admin sets a member cap too small for one source.
	adm := r.loginAs("admin@example.org")
	_, apage, _ := r.get(adm, "/manage")
	acsrf := csrfOf(t, apage)
	if code, _, _ := r.post(adm, "/manage/member-cap", url.Values{"csrf": {acsrf}, "cpu_cores": {"0.01"}}); code != http.StatusSeeOther {
		t.Fatalf("member cap: %d", code)
	}
	_, _, hdr := r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}})
	if _, inRun, _ := r.db.ActiveRun(ctx, a.ID); inRun {
		t.Fatal("started over the member cap")
	}
	if _, page, _ = r.get(member, hdr.Get("Location")); !strings.Contains(page, "Your CPU cap is 0.01 cores") {
		t.Fatal("refusal not explained")
	}
	r.post(member, aURL+"/preview", url.Values{"csrf": {csrf}})
	if _, ok := r.srv.sync.preview(a.ID); ok {
		t.Fatal("preview over the member cap")
	}

	// A cap of the member's own lifts it; the server cap still holds.
	r.post(adm, "/manage/members/"+strconv.FormatInt(u.ID, 10)+"/cap", url.Values{"csrf": {acsrf}, "cpu_cores": {"2"}})
	if c, own, _ := r.db.MemberCap(ctx, u.ID); !own || c.CPUCores != 2 {
		t.Fatalf("own cap: %v %+v", own, c)
	}
	r.db.SetServerCap(ctx, admin.ID, catalogue.ServerCap{MemoryMB: 1})
	r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}})
	if _, inRun, _ := r.db.ActiveRun(ctx, a.ID); inRun {
		t.Fatal("started over the server cap")
	}
	r.db.SetServerCap(ctx, admin.ID, catalogue.ServerCap{})

	// A full disk quota stops runs, not previews.
	old, _ := r.db.CreateAnalysis(ctx, u.ID, "old", "", nil, nil)
	done, _ := r.db.StartRun(ctx, old.ID, u.ID, time.Time{})
	r.db.StopRun(ctx, old.ID, u.ID)
	r.db.SetDataset(ctx, done.ID, catalogue.DatasetReady, diskQuota, "", nil)
	r.post(member, aURL+"/run", url.Values{"csrf": {csrf}, "length": {"1h"}})
	if _, inRun, _ := r.db.ActiveRun(ctx, a.ID); inRun {
		t.Fatal("started with a full disk quota")
	}
	r.post(member, aURL+"/preview", url.Values{"csrf": {csrf}})
	if _, ok := r.srv.sync.preview(a.ID); !ok {
		t.Fatal("preview refused for a full disk quota")
	}

	// Back to the default cap.
	r.post(adm, "/manage/members/"+strconv.FormatInt(u.ID, 10)+"/cap", url.Values{"csrf": {acsrf}, "default": {"1"}})
	if _, own, _ := r.db.MemberCap(ctx, u.ID); own {
		t.Fatal("own cap kept")
	}
	if code, page, _ := r.get(member, "/results"); code != 200 || !strings.Contains(page, "Over 80 %") || !strings.Contains(page, "Last 30 days") {
		t.Fatalf("usage page: %d", code)
	}
}
