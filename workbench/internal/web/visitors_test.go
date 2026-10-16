package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

func TestClientIP(t *testing.T) {
	for _, c := range []struct{ remote, xff, want string }{
		{"192.0.2.7:5000", "", "192.0.2.7"},
		{"192.0.2.7:5000", "198.51.100.1", "192.0.2.7"}, // not from the local proxy: header ignored
		{"127.0.0.1:5000", "198.51.100.1", "198.51.100.1"},
		{"127.0.0.1:5000", "203.0.113.9, 198.51.100.1", "198.51.100.1"}, // the client's own entry is not taken
		{"[::1]:5000", "2001:db8::5", "2001:db8::5"},
		{"127.0.0.1:5000", "garbage", "127.0.0.1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("%s %q: %s, want %s", c.remote, c.xff, got, c.want)
		}
	}
}

func TestVisitorLimit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.db.CreateUser(ctx, "admin@example.org", "Admin", true)
	ann, _ := r.db.CreateUser(ctx, "ann@example.org", "Ann", false)
	st, _ := r.db.CreateStream(ctx, catalogue.NewStream{OwnerID: ann.ID, Input: "https://s.example.org/a", SampleRate: 22050,
		Texts: catalogue.StreamTexts{Name: "Ridge"}, Probe: []byte(`{}`)})
	a, _ := r.db.CreateAnalysis(ctx, ann.ID, "Dusk", "", []int64{st.ID}, nil)
	if _, err := r.db.PublishAnalysis(ctx, a.ID, ann.ID, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	r.db.StartRun(ctx, a.ID, ann.ID, time.Time{})
	a, _ = r.db.Analysis(ctx, a.ID)
	r.srv.pushAll(ctx, a.Sources)

	adm := r.loginAs("admin@example.org")
	_, page, _ := r.get(adm, "/manage")
	if !strings.Contains(page, `name="readers" value="50"`) {
		t.Fatal("the default of 50 is not shown")
	}
	r.post(adm, "/manage/readers", url.Values{"csrf": {csrfOf(t, page)}, "readers": {"1"}})
	if n, _ := r.db.PublicReaders(ctx); n != 1 {
		t.Fatalf("readers: %d", n)
	}

	live := r.web.URL + "/research/dusk/live/" + strconv.FormatInt(st.ID, 10)
	open := func(addr string) (*http.Response, context.CancelFunc) {
		cctx, cancel := context.WithCancel(ctx)
		req, _ := http.NewRequestWithContext(cctx, "GET", live, nil)
		req.Header.Set("X-Forwarded-For", addr)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp, cancel
	}
	first, stop := open("198.51.100.1")
	if first.StatusCode != 200 {
		t.Fatalf("first visitor: %d", first.StatusCode)
	}
	again, stopAgain := open("198.51.100.1") // the same visitor, a second view
	if again.StatusCode != 200 {
		t.Fatalf("second view of the same visitor: %d", again.StatusCode)
	}
	stopAgain()
	other, stopOther := open("198.51.100.2")
	stopOther()
	if other.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("second visitor: %d", other.StatusCode)
	}
	req, _ := http.NewRequest("GET", r.web.URL+"/research/dusk", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.2")
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), "as many visitors are watching") {
		t.Fatal("the page does not say it is full")
	}
	stop()
	first.Body.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, cancel := open("198.51.100.2")
		cancel()
		if resp.StatusCode == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not let in after the first left: %d", resp.StatusCode)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
