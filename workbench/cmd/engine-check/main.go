// Command engine-check runs the control API's conformance checks
// (internal/conformance, docs/control-api.md) against an engine, for the
// engine's owner to run against listen-engine:
//
//	go run ./cmd/engine-check -engine /run/listen-engine/control.sock -input https://stream.example.org/live -phase 1
//
// It makes streams and analyses with identifiers starting "ct-" and
// deletes them again. It prints one line per check and exits 1 if any
// check fails.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vrontier/listen/workbench/internal/conformance"
	"github.com/vrontier/listen/workbench/internal/engine"
)

func main() {
	var (
		addr    = flag.String("engine", "/run/listen-engine/control.sock", "the engine's socket, or http://127.0.0.1:port for its development port")
		input   = flag.String("input", "", "a stream address the engine may connect to; without it the checks that need audio are skipped")
		rate    = flag.Int("rate", 22050, "sample rate of the test streams")
		phase   = flag.Int("phase", 1, "check up to this phase: 1 streams, 2 several analyses and view, 3 groups")
		timeout = flag.Duration("timeout", 20*time.Second, "how long a check waits for events")
		prefix  = flag.String("prefix", "ct", "prefix of the identifiers the checks make")
		engine2 = flag.String("engine2", "", "the same engine as another caller (e.g. /run/listen-engine/internal.sock, as root): adds the checks of callers and shared sources")
	)
	flag.Parse()

	c := engine.Dial(*addr)
	raw := rawClient(*addr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	opt := conformance.Options{Input: *input, SampleRate: *rate, Phase: *phase, Timeout: *timeout, Prefix: *prefix, Raw: raw}
	if *engine2 != "" {
		opt.Second = engine.Dial(*engine2)
	}
	if strings.HasPrefix(*addr, "http://") {
		// An engine started with -test-hooks offers, on a loopback port
		// only, a way to make an analysis fail; without it the check says
		// so and fails, so leave out -test-hooks only knowingly.
		opt.Fail = func(stream, analysis string) bool {
			status, _, err := raw("POST", "/v1/streams/"+stream+"/analyses/"+analysis+"/fail", nil)
			return err == nil && status >= 200 && status < 300
		}
	}
	results := conformance.Run(ctx, c, opt)
	counts := map[string]int{}
	for _, r := range results {
		counts[r.Status]++
		line := fmt.Sprintf("%-4s %d %s", strings.ToUpper(r.Status), r.Phase, r.Name)
		if r.Detail != "" {
			line += " — " + r.Detail
		}
		fmt.Println(line)
	}
	fmt.Printf("\n%d passed, %d failed, %d skipped (phase %d, engine %s)\n", counts[conformance.Pass], counts[conformance.Fail], counts[conformance.Skip], *phase, *addr)
	if conformance.Failed(results) {
		os.Exit(1)
	}
}

// rawClient sends requests as they are, over the same socket or port as
// the engine client.
func rawClient(addr string) func(method, path string, body []byte) (int, []byte, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	base := strings.TrimRight(addr, "/")
	if !strings.HasPrefix(addr, "http://") {
		hc.Transport = &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", addr)
		}}
		base = "http://engine"
	}
	return func(method, path string, body []byte) (int, []byte, error) {
		req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := hc.Do(req)
		if err != nil {
			return 0, nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return resp.StatusCode, b, err
	}
}
