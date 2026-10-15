package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

func TestPublishing(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.db.CreateUser(ctx, "admin@example.org", "Admin", true)
	ann, _ := r.db.CreateUser(ctx, "ann@example.org", "Ann", false)
	bob, _ := r.db.CreateUser(ctx, "bob@example.org", "Bob", false)
	lat, lon := 50.12345, 10.54321
	sa, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: ann.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Ridge", Place: "a hill", Lat: &lat, Lon: &lon, Credit: "Ridge station"}, Probe: []byte(`{}`)})
	sb, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: bob.ID, Input: "https://s.example.org/b", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Valley"}, Probe: []byte(`{}`)})
	r.db.ShareStream(ctx, sb.ID, ann.ID, bob.ID)
	a, _ := r.db.CreateAnalysis(ctx, ann.ID, "Dawn chorus", "Line one.\nLine two.", []int64{sa.ID, sb.ID}, nil)
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	annC := r.loginAs("ann@example.org")

	// Nothing is published, so the menu does not offer Research.
	if _, page, _ := r.get(annC, "/analyses"); strings.Contains(page, `href="/research"`) {
		t.Fatal("Research in the menu with nothing published")
	}

	// Bob has not agreed yet: the page says so and publishing is refused.
	_, page, _ := r.get(annC, aURL+"/publish")
	if !strings.Contains(page, "belongs to Bob, who has not agreed") || !strings.Contains(page, "Dawn chorus") {
		t.Fatal("missing consent not shown")
	}
	csrf := csrfOf(t, page)
	r.post(annC, aURL+"/publish", url.Values{"csrf": {csrf}, "attest": {"on"}})
	if got, _ := r.db.Analysis(ctx, a.ID); got.State != catalogue.AnalysisPrivate {
		t.Fatalf("published without consent: %s", got.State)
	}
	bobC := r.loginAs("bob@example.org")
	_, spage, _ := r.get(bobC, "/sources/"+sb.Slug)
	r.post(bobC, "/sources/"+sb.Slug+"/consent", url.Values{"csrf": {csrfOf(t, spage)}, "member": {fmt.Sprint(ann.ID)}, "agree": {"1"}})

	// Without the attestation, no; with it, published.
	r.post(annC, aURL+"/publish", url.Values{"csrf": {csrf}})
	if got, _ := r.db.Analysis(ctx, a.ID); got.State != catalogue.AnalysisPrivate {
		t.Fatal("published without the attestation")
	}
	code, _, hdr := r.post(annC, aURL+"/publish", url.Values{"csrf": {csrf}, "attest": {"on"}, "data": {"on"}})
	if code != http.StatusSeeOther || hdr.Get("Location") != "/research/dawn-chorus" {
		t.Fatalf("publish: %d %s", code, hdr.Get("Location"))
	}

	// A visitor sees it, without the addresses or coordinates.
	visitor := r.browser()
	code, page, _ = r.get(visitor, "/research/dawn-chorus")
	if code != 200 || !strings.Contains(page, "By Ann") || !strings.Contains(page, "Ridge station") || !strings.Contains(page, "run by Bob") || !strings.Contains(page, "No filter rules") {
		t.Fatalf("public page: %d", code)
	}
	if strings.Contains(page, "s.example.org") || strings.Contains(page, "50.12345") || !strings.Contains(page, "near 50.1, 10.5") {
		t.Fatal("address or exact coordinates on the public page, or not the rounded ones")
	}
	if _, page, _ = r.get(visitor, "/research"); !strings.Contains(page, "Dawn chorus") || !strings.Contains(page, `href="/research"`) {
		t.Fatal("not in the index, or Research not in the menu")
	}
	if code, _, _ := r.get(visitor, "/research/dawn-chorus/live/"+strconv.FormatInt(sa.ID, 10)); code != http.StatusConflict {
		t.Fatalf("live outside a run: %d", code)
	}

	// During a run the live view is open to visitors.
	r.db.StartRun(ctx, a.ID, ann.ID, time.Time{})
	r.srv.pushAll(ctx, a.Sources)
	if _, page, _ = r.get(visitor, "/research/dawn-chorus"); !strings.Contains(page, `data-live="/research/dawn-chorus/live/`) {
		t.Fatal("no live view in a run")
	}
	if !strings.Contains(page, `data-lat="50.1" data-lon="10.5"`) || strings.Contains(page, "50.12345") {
		t.Fatal("the map's places are not rounded")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, body, _ := r.get(visitor, "/research/dawn-chorus/compare/state")
		if code == 200 {
			if strings.Contains(body, "50.12345") || !strings.Contains(body, `"lat":50.1`) {
				t.Fatalf("comparison state not rounded: %.300s", body)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("comparison state: %d", code)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if code, _, _ := r.get(visitor, "/research/dawn-chorus/data/"+strconv.FormatInt(sa.ID, 10)+"/state"); code != 200 {
		t.Fatalf("state: %d", code)
	}
	if code, _, _ := r.get(visitor, "/research/dawn-chorus/data/"+strconv.FormatInt(sa.ID, 10)+"/history/events"); code != http.StatusNotFound {
		t.Fatalf("history for visitors: %d", code)
	}

	// A visitor reports it; the admin is told and withdraws it.
	req := url.Values{"message": {"This is not what it claims to be."}, "contact": {"v@example.org"}}
	if code, _, hdr := r.post(visitor, "/research/dawn-chorus/report", req); code != http.StatusSeeOther || !strings.Contains(hdr.Get("Location"), "reported") {
		t.Fatalf("report: %d", code)
	}
	if m, ok := r.mail.Last("admin@example.org"); !ok || !strings.Contains(m.Body, "not what it claims") {
		t.Fatal("admin not told")
	}
	adm := r.loginAs("admin@example.org")
	_, apage, _ := r.get(adm, "/admin")
	if !strings.Contains(apage, "not what it claims") {
		t.Fatal("report not on the admin page")
	}
	r.post(adm, "/admin/analyses/"+strconv.FormatInt(a.ID, 10)+"/withdraw", url.Values{"csrf": {csrfOf(t, apage)}, "reason": {"checking a report"}})
	if code, page, _ := r.get(visitor, "/research/dawn-chorus"); code != http.StatusGone || !strings.Contains(page, "withdrawn") {
		t.Fatalf("withdrawn page: %d", code)
	}
	if m, _ := r.mail.Last("ann@example.org"); !strings.Contains(m.Body, "checking a report") {
		t.Fatal("member not told")
	}

	// Published again, then Bob no longer agrees: withdrawn, Ann told.
	r.post(annC, aURL+"/publish", url.Values{"csrf": {csrf}, "attest": {"on"}})
	if got, _ := r.db.Analysis(ctx, a.ID); got.State != catalogue.AnalysisPublished || got.Slug != "dawn-chorus" {
		t.Fatalf("published again: %s %s", got.State, got.Slug)
	}
	r.post(bobC, "/sources/"+sb.Slug+"/consent", url.Values{"csrf": {csrfOf(t, spage)}, "member": {fmt.Sprint(ann.ID)}, "agree": {"0"}})
	if got, _ := r.db.Analysis(ctx, a.ID); got.State != catalogue.AnalysisWithdrawn {
		t.Fatalf("not withdrawn after consent ended: %s", got.State)
	}
	if m, _ := r.mail.Last("ann@example.org"); !strings.Contains(m.Body, "no longer agrees") {
		t.Fatal("member not told of the withdrawal")
	}
}

// With publishing switched off, nothing can be published and the page
// offers no way to.
func TestPublishingOff(t *testing.T) {
	r := newRig(t)
	r.srv.cfg.Publishing = false
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: u.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Ridge"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, u.ID, "Dusk", "", []int64{st.ID}, nil)
	aURL := "/analyses/" + strconv.FormatInt(a.ID, 10)
	c := r.loginAs("m@example.org")
	_, page, _ := r.get(c, aURL)
	if strings.Contains(page, `href="`+aURL+`/publish"`) || strings.Contains(page, "Publish…") || !strings.Contains(page, "Publishing is not open on this Workbench yet") {
		t.Fatal("the analysis page still offers publishing")
	}
	if code, _, hdr := r.get(c, aURL+"/publish"); code != http.StatusSeeOther || hdr.Get("Location") != aURL {
		t.Fatalf("publish form: %d %s", code, hdr.Get("Location"))
	}
	r.post(c, aURL+"/publish", url.Values{"csrf": {csrfOf(t, page)}, "attest": {"on"}})
	if got, _ := r.db.Analysis(ctx, a.ID); got.State != catalogue.AnalysisPrivate {
		t.Fatalf("published while switched off: %s", got.State)
	}
}
