package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// An owner shares a source; the other member uses it in an analysis of
// their own but cannot change it; after the share ends, they cannot start.
func TestSharing(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	ann, _ := r.db.CreateUser(ctx, "ann@example.org", "Ann", false)
	r.db.CreateUser(ctx, "bob@example.org", "Bob", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: ann.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Hill"}, Probe: []byte(`{}`)})
	srcURL := "/sources/" + st.Slug
	annC, bobC := r.loginAs("ann@example.org"), r.loginAs("bob@example.org")

	if code, _, _ := r.get(bobC, srcURL); code != 404 {
		t.Fatalf("Bob sees an unshared source: %d", code)
	}
	_, page, _ := r.get(annC, srcURL)
	csrf := csrfOf(t, page)
	_, _, hdr := r.post(annC, srcURL+"/share", url.Values{"csrf": {csrf}, "email": {"carol@example.org"}})
	if _, page, _ = r.get(annC, hdr.Get("Location")); !strings.Contains(page, "is not a member") {
		t.Fatal("sharing with a non-member not explained")
	}
	if code, _, _ := r.post(annC, srcURL+"/share", url.Values{"csrf": {csrf}, "email": {"bob@example.org"}}); code != http.StatusSeeOther {
		t.Fatalf("share: %d", code)
	}
	if m, ok := r.mail.Last("bob@example.org"); !ok || !strings.Contains(m.Body, "shared the source \"Hill\"") {
		t.Fatal("no share mail")
	}

	// Bob sees it, read-only, and uses it in his own analysis.
	_, page, _ = r.get(bobC, "/sources")
	if !strings.Contains(page, "Shared with you") || !strings.Contains(page, "Ann's") {
		t.Fatal("Bob's sources")
	}
	code, page, _ := r.get(bobC, srcURL)
	if code != 200 || strings.Contains(page, "Delete") || strings.Contains(page, `name="place"`) || !strings.Contains(page, "Ann shares this source with you") {
		t.Fatalf("Bob's view of the source: %d", code)
	}
	bcsrf := csrfOf(t, page)
	if code, _, _ := r.post(bobC, srcURL+"/state", url.Values{"csrf": {bcsrf}, "to": {"pause"}}); code != 404 {
		t.Fatalf("Bob paused Ann's source: %d", code)
	}
	if _, page, _ = r.get(bobC, "/analyses/new"); !strings.Contains(page, "shared by Ann") {
		t.Fatal("shared source not offered for a new analysis")
	}
	code, _, hdr = r.post(bobC, "/analyses/new", url.Values{"csrf": {bcsrf}, "name": {"Bob on the hill"}, "source": {strconv.FormatInt(st.ID, 10)}})
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
	if _, page, _ = r.get(annC, srcURL); !strings.Contains(page, "Bob on the hill") || !strings.Contains(page, "stop sharing") {
		t.Fatal("Ann does not see who uses her source")
	}

	// Ann stops sharing: Bob cannot start any more.
	bob, _ := r.db.UserByEmail(ctx, "bob@example.org")
	r.post(annC, srcURL+"/unshare", url.Values{"csrf": {csrf}, "member": {strconv.FormatInt(bob.ID, 10)}})
	_, _, hdr = r.post(bobC, aURL+"/run", url.Values{"csrf": {bcsrf}, "length": {"1h"}})
	if _, inRun, _ := r.db.ActiveRun(ctx, id); inRun {
		t.Fatal("run started with an unshared source")
	}
	if _, page, _ = r.get(bobC, hdr.Get("Location")); !strings.Contains(page, "no longer shared with you") {
		t.Fatal("refusal not explained")
	}
	if code, _, _ := r.get(bobC, srcURL); code != 404 {
		t.Fatalf("Bob still sees the source: %d", code)
	}
	if entries, _ := r.db.AuditOf(ctx, "stream", st.ID); len(entries) < 2 {
		t.Fatalf("audit: %d entries", len(entries))
	}
}
