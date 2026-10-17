package conformance

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vrontier/listen-workbench/internal/engine"
	"github.com/vrontier/listen-workbench/internal/standin"
)

// The stand-in must pass every check, so that it and the contract cannot
// drift apart.
func TestStandIn(t *testing.T) {
	e := standin.New(standin.Options{Tick: 20 * time.Millisecond})
	srv := httptest.NewServer(e)
	t.Cleanup(func() {
		srv.CloseClientConnections()
		srv.Close()
		e.Close()
	})
	c := engine.NewClient(&http.Client{}, srv.URL)
	raw := func(method, path string, body []byte) (int, []byte, error) {
		req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return resp.StatusCode, b, err
	}
	fail := func(stream, analysis string) bool {
		return e.Fail(stream, analysis, "made to fail by the conformance test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	results := Run(ctx, c, Options{Input: "https://stream.example.org/ct.mp3", Phase: 3, Timeout: 5 * time.Second, Raw: raw, Fail: fail})
	if len(results) == 0 {
		t.Fatal("no checks ran")
	}
	for _, r := range results {
		if r.Status != Pass {
			t.Errorf("phase %d %s: %s %s", r.Phase, r.Name, r.Status, r.Detail)
		}
	}
	// Everything it made is gone again.
	if list, err := c.Streams(ctx); err != nil || len(list) != 0 {
		t.Fatalf("left behind: %v %+v", err, list)
	}
}
