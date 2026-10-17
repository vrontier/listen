package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestAccount(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "Mira", false)
	r.db.CreateUser(ctx, "taken@example.org", "Other", false)
	member := r.loginAs("m@example.org")
	// Another browser of the member's (a second login link would wait a minute).
	elsewhere := r.browser()
	secret, _ := r.db.CreateSession(ctx, u.ID, sessionLife)
	web, _ := url.Parse(r.web.URL)
	elsewhere.Jar.SetCookies(web, []*http.Cookie{{Name: sessionCookie, Value: secret, Path: "/"}})

	// The page, reached from the sidebar.
	code, page, _ := r.get(member, "/account")
	if code != 200 || !strings.Contains(page, `<a class="who" href="/account" aria-current="page">Mira</a>`) || !strings.Contains(page, "You log in with <strong>m@example.org</strong>") {
		t.Fatalf("account page: %d", code)
	}
	csrf := csrfOf(t, page)

	// The name changes at once.
	if code, page, _ := r.post(member, "/account/name", url.Values{"csrf": {csrf}, "name": {"M"}}); code != 400 || !strings.Contains(page, "2 to 60 characters") {
		t.Fatalf("short name: %d", code)
	}
	if code, _, _ := r.post(member, "/account/name", url.Values{"csrf": {csrf}, "name": {"Mira Field"}}); code != http.StatusSeeOther {
		t.Fatalf("name: %d", code)
	}
	if got, _ := r.db.User(ctx, u.ID); got.Name != "Mira Field" {
		t.Fatalf("name saved: %q", got.Name)
	}

	// The address changes only once the new one confirms it.
	for _, c := range []struct{ email, why string }{
		{"not-an-address", "not a complete email address"},
		{"M@example.org", "your address already"},
		{"taken@example.org", "cannot be used for your account"},
	} {
		if code, page, _ := r.post(member, "/account/email", url.Values{"csrf": {csrf}, "email": {c.email}}); code != 400 || !strings.Contains(page, c.why) {
			t.Fatalf("%s: %d", c.email, code)
		}
	}
	if code, _, _ := r.post(member, "/account/email", url.Values{"csrf": {csrf}, "email": {"new@example.org"}}); code != http.StatusSeeOther {
		t.Fatalf("new address: %d", code)
	}
	msg, ok := r.mail.Last("new@example.org")
	if !ok || !strings.Contains(msg.Subject, "Confirm your new address") {
		t.Fatal("no mail to the new address")
	}
	link, _ := url.Parse(linkRE.FindString(msg.Body))
	if got, _ := r.db.User(ctx, u.ID); got.Email != "m@example.org" {
		t.Fatal("address changed before confirmation")
	}
	if _, page, _ := r.get(member, "/account"); !strings.Contains(page, "Waiting for <strong>new@example.org</strong>") {
		t.Fatal("page does not show the waiting change")
	}

	// A cancelled change leaves its link dead; a new request replaces it.
	r.post(member, "/account/email/cancel", url.Values{"csrf": {csrf}})
	if code, _, _ := r.get(r.browser(), link.Path); code != 400 {
		t.Fatalf("cancelled link: %d", code)
	}
	r.post(member, "/account/email", url.Values{"csrf": {csrf}, "email": {"new@example.org"}})
	msg, _ = r.mail.Last("new@example.org")
	link, _ = url.Parse(linkRE.FindString(msg.Body))

	// Opening the link asks first; confirming changes the address, tells
	// the old one, ends every session and logs in the confirming browser.
	phone := r.browser()
	if code, page, _ := r.get(phone, link.Path); code != 200 || !strings.Contains(page, "Use this address") {
		t.Fatalf("confirm page: %d", code)
	}
	if got, _ := r.db.User(ctx, u.ID); got.Email != "m@example.org" {
		t.Fatal("opening the link changed the address")
	}
	if code, _, _ := r.get(elsewhere, "/account"); code != 200 {
		t.Fatalf("the other browser is not logged in: %d", code)
	}
	before := r.mail.Count("m@example.org")
	if code, _, hdr := r.post(phone, link.Path, nil); code != http.StatusSeeOther || hdr.Get("Location") != "/account" {
		t.Fatalf("confirm: %d %s", code, hdr.Get("Location"))
	}
	if got, _ := r.db.User(ctx, u.ID); got.Email != "new@example.org" {
		t.Fatalf("address: %q", got.Email)
	}
	if r.mail.Count("m@example.org") != before+1 {
		t.Fatal("old address not told")
	}
	if m, _ := r.mail.Last("m@example.org"); !strings.Contains(m.Body, "new@example.org") {
		t.Fatalf("notice: %q", m.Body)
	}
	for _, c := range []*http.Client{member, elsewhere} {
		if code, _, _ := r.get(c, "/account"); code != http.StatusSeeOther {
			t.Fatalf("an old session goes on: %d", code)
		}
	}
	if code, page, _ := r.get(phone, "/account"); code != 200 || !strings.Contains(page, "You log in with <strong>new@example.org</strong>") {
		t.Fatalf("confirming browser: %d", code)
	}
	if code, _, _ := r.post(r.browser(), link.Path, nil); code != 400 {
		t.Fatalf("link used twice: %d", code)
	}

	// Five requests an hour at most.
	_, page, _ = r.get(phone, "/account")
	csrf = csrfOf(t, page)
	n := 0
	for i := 0; i < 6; i++ {
		code, page, _ := r.post(phone, "/account/email", url.Values{"csrf": {csrf}, "email": {"other@example.org"}})
		if code == 400 && strings.Contains(page, "in the last hour") {
			break
		}
		n++
	}
	if n != 3 { // two requests were made earlier in this hour
		t.Fatalf("%d requests before the limit", n)
	}

	// The audit trail has it all.
	audit, _ := r.db.AuditOf(ctx, "user", u.ID)
	var actions []string
	for _, e := range audit {
		actions = append(actions, e.Action)
	}
	if got := strings.Join(actions, ","); !strings.Contains(got, "name") || !strings.Contains(got, "email") {
		t.Fatalf("audit: %s", got)
	}
}
