package standin

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func call(t *testing.T, srv *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestGroup(t *testing.T) {
	e := New(Options{Tick: 20 * time.Millisecond})
	defer e.Close()
	srv := httptest.NewServer(e)
	defer srv.Close()
	for _, s := range []string{"s-0001", "s-0002"} {
		if code, _ := call(t, srv, "PUT", "/v1/streams/"+s, `{"input":"https://`+s+`.example.org/x","sample_rate":22050}`); code != 200 {
			t.Fatalf("stream %s: %d", s, code)
		}
		if code, _ := call(t, srv, "PUT", "/v1/streams/"+s+"/analyses/a-0001", `{}`); code != 200 {
			t.Fatalf("analysis on %s: %d", s, code)
		}
	}
	members := `"members":[{"stream":"s-0001","analysis":"a-0001","lat":50.1,"lon":10.5},{"stream":"s-0002","analysis":"a-0001"}]`
	if code, out := call(t, srv, "PUT", "/v1/groups/g-0001", `{`+members+`,"analyses":["activity","coincidence"]}`); code != 422 ||
		!strings.Contains(mustJSON(out), "needs_station_time") {
		t.Fatalf("coincidence without station time: %d %v", code, out)
	}
	if code, _ := call(t, srv, "PUT", "/v1/groups/g-0001", `{"members":[{"stream":"s-0001","analysis":"a-0009"},{"stream":"s-0002","analysis":"a-0001"}]}`); code != 422 {
		t.Fatalf("unknown member: %d", code)
	}
	code, out := call(t, srv, "PUT", "/v1/groups/g-0001", `{`+members+`,"analyses":["activity"],"label":"rev-1"}`)
	if code != 200 || out["mode"] != "on_demand" {
		t.Fatalf("group: %d %v", code, out)
	}

	// A subscriber keeps both streams connected and gets a second of
	// activity with both members present.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/v1/groups/g-0001/events?types=group.activity", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	found := false
	for sc.Scan() && !found {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Type    string `json:"type"`
			Payload struct {
				Members []struct {
					Present bool `json:"present"`
				} `json:"members"`
			} `json:"payload"`
		}
		json.Unmarshal([]byte(line[6:]), &ev)
		if ev.Type != "group.activity" {
			t.Fatalf("filtered stream sent %s", ev.Type)
		}
		if len(ev.Payload.Members) == 2 && ev.Payload.Members[0].Present && ev.Payload.Members[1].Present {
			found = true
		}
	}
	if !found {
		t.Fatal("no second with both members present")
	}
	if code, out := call(t, srv, "GET", "/v1/groups/g-0001/state", ""); code != 200 || out["status"] == nil {
		t.Fatalf("state: %d %v", code, out)
	}
	if code, _ := call(t, srv, "DELETE", "/v1/groups/g-0001", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The correlation finds the lag between two streams that hear the same sky
// at different delays, and the shifted baseline stays well below it.
func TestBestLagFindsDelay(t *testing.T) {
	n := 900
	a, b := make([]float64, n), make([]float64, n)
	base := float64(time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC).Unix())
	for k := range n {
		u := base + float64(k)
		a[k] = 3 + 60*sky(u-5)
		b[k] = 3 + 60*sky(u-14)
	}
	r, lag, _ := bestLag(a, b, n-corrWindow, corrWindow, 0)
	if lag != 9 || r < 0.9 {
		t.Fatalf("lag %d, r %.2f; want 9 and above 0.9", lag, r)
	}
	if br, _, _ := bestLag(a, b, n-corrWindow-baselineShift, corrWindow, baselineShift); br > r-0.3 {
		t.Fatalf("baseline %.2f too close to %.2f", br, r)
	}
}
