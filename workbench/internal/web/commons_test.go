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

// Sources are a commons (decided 2026-10-18): a source Ann registers is
// open to every member, who uses it in analyses of their own without
// asking; Ann looks after it as its steward, and so may the master admin.
// A source kept private is its steward's alone until it is opened.
func TestSourcesCommons(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ann, _ := r.db.CreateUser(ctx, "ann@example.org", "Ann", false)
	r.db.CreateUser(ctx, "bob@example.org", "Bob", false)
	carol, _ := r.db.CreateUser(ctx, "carol@example.org", "Carol", false)
	r.db.CreateUser(ctx, "admin@example.org", "Admin", true)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: ann.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Hill"}, Probe: []byte(`{}`)})
	hid, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: carol.ID, Input: "https://s.example.org/c", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Cellar"}, Private: true, Probe: []byte(`{}`)})
	srcURL := "/sources/" + st.Slug
	annC, bobC, carolC := r.loginAs("ann@example.org"), r.loginAs("bob@example.org"), r.loginAs("carol@example.org")

	// Bob finds Ann's source without being asked to, and not Carol's.
	_, page, _ := r.get(bobC, "/sources")
	if !strings.Contains(page, "Registered by other members") || !strings.Contains(page, "steward Ann") || strings.Contains(page, "Cellar") {
		t.Fatal("Bob's sources")
	}
	if code, _, _ := r.get(bobC, "/sources/"+hid.Slug); code != 404 {
		t.Fatalf("Bob sees a private source: %d", code)
	}
	code, page, _ := r.get(bobC, srcURL)
	if code != 200 || strings.Contains(page, `name="place"`) || strings.Contains(page, "Who may use it") || !strings.Contains(page, "Ann registered and looks after as its steward") {
		t.Fatalf("Bob's view of the source: %d", code)
	}
	bcsrf := csrfOf(t, page)
	if code, _, _ := r.post(bobC, srcURL+"/state", url.Values{"csrf": {bcsrf}, "to": {"pause"}}); code != 404 {
		t.Fatalf("Bob paused Ann's source: %d", code)
	}
	if _, page, _ = r.get(bobC, "/analyses/new"); !strings.Contains(page, "steward Ann") || strings.Contains(page, "Cellar") {
		t.Fatal("open source not offered for a new analysis, or a private one offered")
	}
	code, _, hdr := r.post(bobC, "/analyses/new", url.Values{"csrf": {bcsrf}, "name": {"Bob on the hill"}, "source": {strconv.FormatInt(st.ID, 10)}})
	if code != http.StatusSeeOther {
		t.Fatalf("Bob's analysis: %d", code)
	}
	aURL := hdr.Get("Location")
	id, _ := strconv.ParseInt(strings.TrimPrefix(aURL, "/analyses/"), 10, 64)
	if code, _, _ := r.post(bobC, aURL+"/preview", url.Values{"csrf": {bcsrf}}); code != http.StatusSeeOther {
		t.Fatalf("Bob's preview: %d", code)
	}
	if _, ok := r.srv.sync.preview(id); !ok {
		t.Fatal("Bob's preview refused")
	}
	r.post(bobC, aURL+"/preview/end", url.Values{"csrf": {bcsrf}})

	// Registering the same address again shows the source that exists.
	if code, page, _ = r.post(bobC, "/sources/new", url.Values{"csrf": {bcsrf}, "input": {"http://S.example.org/a/"}}); code != http.StatusConflict ||
		!strings.Contains(page, "This stream is here already") || !strings.Contains(page, `href="`+srcURL+`"`) {
		t.Fatalf("duplicate of an open source: %d", code)
	}
	if code, page, _ = r.post(bobC, "/sources/new", url.Values{"csrf": {bcsrf}, "input": {"https://s.example.org/c"}}); code != http.StatusConflict ||
		!strings.Contains(page, "keeps it to themselves for now") || strings.Contains(page, `href="/sources/`+hid.Slug+`"`) {
		t.Fatalf("duplicate of a private source: %d", code)
	}

	// Ann sees Bob's analysis; she can neither make the source private nor
	// delete it from under him, but she can retire it.
	_, page, _ = r.get(annC, srcURL)
	if !strings.Contains(page, "Bob on the hill") || !strings.Contains(page, "so it stays open") || !strings.Contains(page, "Retire it instead") {
		t.Fatal("Ann's page of her source")
	}
	acsrf := csrfOf(t, page)
	r.post(annC, srcURL+"/visibility", url.Values{"csrf": {acsrf}, "to": {"private"}})
	r.post(annC, srcURL+"/delete", url.Values{"csrf": {acsrf}, "confirm": {"Hill"}})
	if got, err := r.db.Stream(ctx, st.ID); err != nil || got.Private {
		t.Fatalf("source taken from under Bob: %v %v", err, got.Private)
	}
	r.post(annC, srcURL+"/state", url.Values{"csrf": {acsrf}, "to": {"retire"}})
	if got, _ := r.db.Stream(ctx, st.ID); got.State != catalogue.StreamRetired {
		t.Fatalf("retire: %s", got.State)
	}
	if _, page, _ = r.get(bobC, "/analyses/new"); strings.Contains(page, "steward Ann") {
		t.Fatal("a retired source offered for a new analysis")
	}
	if _, page, _ = r.get(bobC, aURL); !strings.Contains(page, "Hill") {
		t.Fatal("Bob's analysis lost the retired source")
	}

	// The master admin looks after any source as its steward does.
	adm := r.loginAs("admin@example.org")
	code, page, _ = r.get(adm, "/sources/"+hid.Slug)
	if code != 200 || !strings.Contains(page, "As master admin you can change it") {
		t.Fatalf("admin's view of a private source: %d", code)
	}
	if code, _, _ := r.post(adm, srcURL+"/state", url.Values{"csrf": {csrfOf(t, page)}, "to": {"unretire"}}); code != http.StatusSeeOther {
		t.Fatalf("admin brings back: %d", code)
	}
	if got, _ := r.db.Stream(ctx, st.ID); got.State != catalogue.StreamDraft {
		t.Fatalf("after the admin: %s", got.State)
	}

	// Carol opens her source: now Bob finds it.
	_, page, _ = r.get(carolC, "/sources/"+hid.Slug)
	if !strings.Contains(page, "Open to every member") {
		t.Fatal("Carol is not offered to open her source")
	}
	r.post(carolC, "/sources/"+hid.Slug+"/visibility", url.Values{"csrf": {csrfOf(t, page)}, "to": {"open"}})
	if _, page, _ = r.get(bobC, "/sources"); !strings.Contains(page, "Cellar") {
		t.Fatal("an opened source is not listed for Bob")
	}
	if entries, _ := r.db.AuditOf(ctx, "stream", hid.ID); len(entries) < 2 || entries[0].Action != "visibility" && entries[len(entries)-1].Action != "visibility" {
		t.Fatalf("audit: %+v", entries)
	}
}
