// Command listen-mcp is a read-only MCP server for Listening Observatory.
// It runs next to the listeners, reads the source catalogue (sources.json)
// and queries each listener on 127.0.0.1: the snapshot and memory APIs, and
// the live event stream for extracts of the next n seconds. It never touches
// audio. Connections are made with ?role=tool, so they don't count as people
// watching (but do wake a listener that runs on demand).
//
// Transport: stdio (e.g. over ssh). Example client configuration:
//
//	{"command": "ssh", "args": ["ionos-questmaster", "bin/listen-mcp"]}
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const version = "0.1.0"

type sourceCfg struct {
	Slug     string   `json:"slug"`
	Listed   bool     `json:"listed"`
	Name     string   `json:"name"`
	Meta     []string `json:"meta"`
	Timezone string   `json:"timezone"`
	Audio    bool     `json:"audio"`
	Rights   string   `json:"rights"`
	Credit   struct {
		Text string `json:"text"`
		URL  string `json:"url"`
	} `json:"credit"`
	Listener struct {
		Port  int      `json:"port"`
		Input string   `json:"input"`
		Args  []string `json:"args"`
	} `json:"listener"`
}

type app struct {
	sourcesPath string
	host        string
	maxSeconds  int
	client      *http.Client
}

func main() {
	a := &app{client: &http.Client{Timeout: 5 * time.Second}}
	flag.StringVar(&a.sourcesPath, "sources", "/var/www/listen.vrontier.org/_config/sources.json", "source catalogue")
	flag.StringVar(&a.host, "host", "127.0.0.1", "where the listeners listen")
	flag.IntVar(&a.maxSeconds, "max-seconds", 300, "longest extract window")
	flag.Parse()
	log.SetOutput(os.Stderr) // stdout carries the protocol
	log.SetPrefix("listen-mcp: ")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := &server{name: "listen-observatory", version: version, tools: a.tools(), out: os.Stdout}
	if err := s.serve(ctx, os.Stdin); err != nil {
		log.Fatal(err)
	}
}

func (a *app) sources() ([]sourceCfg, error) {
	b, err := os.ReadFile(a.sourcesPath)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Sources []sourceCfg `json:"sources"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", a.sourcesPath, err)
	}
	return cfg.Sources, nil
}

func (a *app) source(slug string) (sourceCfg, error) {
	all, err := a.sources()
	if err != nil {
		return sourceCfg{}, err
	}
	var slugs []string
	for _, s := range all {
		if s.Slug == slug {
			return s, nil
		}
		slugs = append(slugs, s.Slug)
	}
	return sourceCfg{}, fmt.Errorf("unknown source %q; known: %s", slug, strings.Join(slugs, ", "))
}

func (a *app) get(ctx context.Context, port int, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s:%d%s", a.host, port, path), nil)
	if err != nil {
		return err
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(v)
}

// ---- snapshot helpers --------------------------------------------------------

type snapshot struct {
	System *struct {
		Payload struct {
			Stream         string   `json:"stream"`
			Analysis       string   `json:"analysis"`
			LatencyMs      float64  `json:"latency_ms"`
			Listeners      int      `json:"listeners"`
			AudioListeners int      `json:"audio_listeners"`
			Tools          int      `json:"tools"`
			Input          string   `json:"input"`
			SampleRate     int      `json:"sample_rate"`
			UptimeS        float64  `json:"uptime_s"`
			Reconnects     int      `json:"reconnects"`
			Audio          []string `json:"audio"`
		} `json:"payload"`
	} `json:"system"`
	Features *struct {
		Payload struct {
			State    string  `json:"state"`
			Dominant float64 `json:"dominant_frequency_hz"`
		} `json:"payload"`
	} `json:"features"`
	Spectrum *struct {
		Payload struct {
			MinHz float64 `json:"min_hz"`
			MaxHz float64 `json:"max_hz"`
		} `json:"payload"`
	} `json:"spectrum"`
	ActiveMotifs []json.RawMessage `json:"active_motifs"`
}

type sourceState struct {
	Slug           string  `json:"slug"`
	Stream         string  `json:"stream"` // connected, idle (on demand), reconnecting, …, or unreachable
	Kind           string  `json:"kind"`   // live or replay
	UptimeS        float64 `json:"uptime_s,omitempty"`
	Reconnects     int     `json:"reconnects"`
	Listeners      int     `json:"listeners"`
	WithSound      int     `json:"with_sound"`
	Tools          int     `json:"tools"`
	LatencyMs      float64 `json:"latency_ms,omitempty"`
	State          string  `json:"signal_state,omitempty"`
	DominantHz     float64 `json:"dominant_hz,omitempty"`
	ActiveMotifs   int     `json:"active_motifs"`
	AnalysedFromHz float64 `json:"analysed_from_hz,omitempty"`
	AnalysedToHz   float64 `json:"analysed_to_hz,omitempty"`
	Error          string  `json:"error,omitempty"`
}

func kindOf(input string) string {
	if strings.HasPrefix(input, "http://") || strings.HasPrefix(input, "https://") {
		return "live"
	}
	return "replay"
}

func (a *app) state(ctx context.Context, s sourceCfg) sourceState {
	st := sourceState{Slug: s.Slug, Kind: kindOf(s.Listener.Input)}
	var snap snapshot
	if err := a.get(ctx, s.Listener.Port, "/api/state/current", &snap); err != nil {
		st.Stream, st.Error = "unreachable", err.Error()
		return st
	}
	if p := snap.System; p != nil {
		st.Stream, st.UptimeS, st.Reconnects = p.Payload.Stream, p.Payload.UptimeS, p.Payload.Reconnects
		st.Listeners, st.WithSound, st.Tools, st.LatencyMs = p.Payload.Listeners, p.Payload.AudioListeners, p.Payload.Tools, p.Payload.LatencyMs
	}
	if f := snap.Features; f != nil && st.Stream == "connected" {
		st.State, st.DominantHz = f.Payload.State, f.Payload.Dominant
	}
	if sp := snap.Spectrum; sp != nil {
		st.AnalysedFromHz, st.AnalysedToHz = sp.Payload.MinHz, sp.Payload.MaxHz
	}
	st.ActiveMotifs = len(snap.ActiveMotifs)
	return st
}

func (a *app) states(ctx context.Context, all []sourceCfg) []sourceState {
	out := make([]sourceState, len(all))
	var wg sync.WaitGroup
	for i, s := range all {
		wg.Add(1)
		go func(i int, s sourceCfg) { defer wg.Done(); out[i] = a.state(ctx, s) }(i, s)
	}
	wg.Wait()
	return out
}

// ---- tools --------------------------------------------------------------------

func obj(props map[string]any, required ...string) map[string]any {
	m := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func (a *app) tools() []Tool {
	return []Tool{
		{
			Name: "service_status",
			Description: "Overall state of the observatory: per source the stream state (connected, idle on demand, " +
				"reconnecting, unreachable), uptime, reconnects, listeners (live pages open), how many with sound, " +
				"connected tools, the current signal state and dominant frequency; plus totals.",
			InputSchema: obj(map[string]any{}),
			Handler:     a.serviceStatus,
		},
		{
			Name: "list_sources",
			Description: "The configured sources: slug, name, place, kind (live stream or replay), time zone, whether " +
				"audio may be played, credit, rights note and analysis settings; with their current state unless " +
				"include_state is false.",
			InputSchema: obj(map[string]any{
				"include_state": map[string]any{"type": "boolean", "description": "add the live state of each source (default true)"},
			}),
			Handler: a.listSources,
		},
		{
			Name: "get_extract",
			Description: "Record the NEXT n seconds of analysis data from one source (no audio): the call waits that " +
				"long. Layers: features (2 Hz signal state, centroid, entropy, harmonicity, …), events (transients, " +
				"resonances, motifs, interpretations), frames (10 Hz level, energy, flux), spectrum (log-spaced band " +
				"levels, decimated by spectrum_every), status. A source that is idle on demand is woken first, which " +
				"takes a few seconds of the window.",
			InputSchema: obj(map[string]any{
				"source":         map[string]any{"type": "string", "description": "source slug, e.g. my-stream"},
				"seconds":        map[string]any{"type": "integer", "minimum": 1, "maximum": a.maxSeconds, "description": "length of the recording window (default 30)"},
				"layers":         map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"features", "events", "frames", "spectrum", "status"}}, "description": "default [features, events]"},
				"spectrum_every": map[string]any{"type": "integer", "minimum": 1, "description": "keep every n-th spectrum (10 per second; default 10 = 1 per second)"},
			}, "source"),
			Handler: a.getExtract,
		},
		{
			Name:        "get_motifs",
			Description: "The motifs a source remembers (recurring resonances and textures): frequency, occurrences, presence, confidence, signature, first and last seen. Sorted by occurrences.",
			InputSchema: obj(map[string]any{
				"source": map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "description": "default 50"},
				"kind":   map[string]any{"type": "string", "enum": []string{"resonance", "texture"}},
			}, "source"),
			Handler: a.getMotifs,
		},
		{
			Name:        "get_narratives",
			Description: "The latest interpretation texts of a source (written by a language model from the measurements, while someone watches), newest first.",
			InputSchema: obj(map[string]any{
				"source": map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "description": "default 10"},
				"hours":  map[string]any{"type": "integer", "minimum": 1, "maximum": 720, "description": "how far back to look (default 24)"},
			}, "source"),
			Handler: a.getNarratives,
		},
	}
}

func (a *app) serviceStatus(ctx context.Context, _ json.RawMessage) (any, error) {
	all, err := a.sources()
	if err != nil {
		return nil, err
	}
	states := a.states(ctx, all)
	tot := map[string]int{"sources": len(states)}
	for _, s := range states {
		tot["stream_"+s.Stream]++
		tot["listeners"] += s.Listeners
		tot["with_sound"] += s.WithSound
		tot["tools"] += s.Tools
	}
	return map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "totals": tot, "sources": states}, nil
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func (a *app) listSources(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		IncludeState *bool `json:"include_state"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	all, err := a.sources()
	if err != nil {
		return nil, err
	}
	var states []sourceState
	if p.IncludeState == nil || *p.IncludeState {
		states = a.states(ctx, all)
	}
	out := make([]map[string]any, len(all))
	for i, s := range all {
		place := ""
		if len(s.Meta) > 0 {
			place = s.Meta[0]
		}
		analysis := map[string]any{}
		for _, f := range []string{"-sample-rate", "-min-hz", "-max-hz", "-mains", "-transient-k"} {
			if v := flagValue(s.Listener.Args, f); v != "" {
				if n, err := strconv.ParseFloat(v, 64); err == nil {
					analysis[strings.TrimPrefix(f, "-")] = n
				}
			}
		}
		m := map[string]any{
			"slug": s.Slug, "name": s.Name, "place": place, "listed": s.Listed, "kind": kindOf(s.Listener.Input),
			"timezone": s.Timezone, "audio_playback": s.Audio, "credit": s.Credit.Text, "credit_url": s.Credit.URL,
			"rights": s.Rights, "analysis": analysis,
		}
		if states != nil {
			m["state"] = states[i]
		}
		out[i] = m
	}
	return map[string]any{"sources": out}, nil
}

type envelope struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Position  float64         `json:"position_s,omitempty"`
	Payload   json.RawMessage `json:"payload"`
}

func (a *app) getExtract(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Source        string   `json:"source"`
		Seconds       int      `json:"seconds"`
		Layers        []string `json:"layers"`
		SpectrumEvery int      `json:"spectrum_every"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s, err := a.source(p.Source)
	if err != nil {
		return nil, err
	}
	if p.Seconds <= 0 {
		p.Seconds = 30
	}
	if p.Seconds > a.maxSeconds {
		return nil, fmt.Errorf("seconds: at most %d", a.maxSeconds)
	}
	if len(p.Layers) == 0 {
		p.Layers = []string{"features", "events"}
	}
	if p.SpectrumEvery <= 0 {
		p.SpectrumEvery = 10
	}
	want := map[string]bool{}
	for _, l := range p.Layers {
		want[l] = true
	}

	before := a.state(ctx, s)
	window, cancel := context.WithTimeout(ctx, time.Duration(p.Seconds)*time.Second)
	defer cancel()
	url := fmt.Sprintf("ws://%s:%d/ws/live?role=tool", a.host, s.Listener.Port)
	conn, _, err := websocket.Dial(window, url, nil)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", s.Slug, err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20)

	start := time.Now()
	data := map[string][]map[string]any{}
	counts := map[string]int{}
	spectra := 0
	keep := func(layer string, e envelope) {
		m := map[string]any{"t": e.Timestamp}
		if e.Position > 0 {
			m["position_s"] = e.Position
		}
		var body map[string]any
		if json.Unmarshal(e.Payload, &body) == nil {
			for k, v := range body {
				m[k] = v
			}
		}
		if layer == "events" {
			m["type"] = e.Type
		}
		data[layer] = append(data[layer], m)
	}
	for {
		_, msg, err := conn.Read(window)
		if err != nil {
			if errors.Is(window.Err(), context.DeadlineExceeded) || window.Err() != nil {
				break // the window is over
			}
			return nil, fmt.Errorf("reading %s: %w", s.Slug, err)
		}
		var e envelope
		if json.Unmarshal(msg, &e) != nil {
			continue
		}
		counts[e.Type]++
		switch {
		case e.Type == "signal.frame" && want["frames"]:
			keep("frames", e)
		case e.Type == "signal.spectrum" && want["spectrum"]:
			if spectra%p.SpectrumEvery == 0 {
				keep("spectrum", e)
			}
			spectra++
		case e.Type == "feature.state" && want["features"]:
			keep("features", e)
		case e.Type == "system.status" && want["status"]:
			keep("status", e)
		case want["events"] && (strings.HasPrefix(e.Type, "event.") || strings.HasPrefix(e.Type, "motif.") ||
			e.Type == "narrative.update" || e.Type == "memory.summary"):
			keep("events", e)
		}
	}
	conn.Close(websocket.StatusNormalClosure, "")
	types := make([]string, 0, len(counts))
	for t := range counts {
		types = append(types, t)
	}
	sort.Strings(types)
	return map[string]any{
		"source":         s.Slug,
		"requested_s":    p.Seconds,
		"recorded_s":     time.Since(start).Round(100 * time.Millisecond).Seconds(),
		"stream_before":  before.Stream,
		"message_counts": counts,
		"message_types":  types,
		"layers":         p.Layers,
		"data":           data,
		"note":           "No audio. Timestamps are observation times at the source; position_s is seconds since the listener started.",
	}, nil
}

func (a *app) getMotifs(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Source string `json:"source"`
		Limit  int    `json:"limit"`
		Kind   string `json:"kind"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s, err := a.source(p.Source)
	if err != nil {
		return nil, err
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	var resp struct {
		Motifs []map[string]any `json:"motifs"`
	}
	if err := a.get(ctx, s.Listener.Port, "/api/motifs", &resp); err != nil {
		return nil, err
	}
	list := resp.Motifs[:0]
	for _, m := range resp.Motifs {
		if p.Kind == "" || m["kind"] == p.Kind {
			list = append(list, m)
		}
	}
	occ := func(m map[string]any) float64 { v, _ := m["occurrences"].(float64); return v }
	sort.SliceStable(list, func(i, j int) bool { return occ(list[i]) > occ(list[j]) })
	total := len(list)
	if len(list) > p.Limit {
		list = list[:p.Limit]
	}
	return map[string]any{"source": s.Slug, "total": total, "motifs": list}, nil
}

func (a *app) getNarratives(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Source string `json:"source"`
		Limit  int    `json:"limit"`
		Hours  int    `json:"hours"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	s, err := a.source(p.Source)
	if err != nil {
		return nil, err
	}
	if p.Limit <= 0 {
		p.Limit = 10
	}
	if p.Hours <= 0 || p.Hours > 720 {
		p.Hours = 24
	}
	from := time.Now().Add(-time.Duration(p.Hours) * time.Hour).UTC().Format(time.RFC3339)
	var resp struct {
		Events []envelope `json:"events"`
	}
	if err := a.get(ctx, s.Listener.Port, "/api/history/events?type=narrative.update&from="+from, &resp); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for i := len(resp.Events) - 1; i >= 0 && len(out) < p.Limit; i-- {
		e := resp.Events[i]
		var body map[string]any
		_ = json.Unmarshal(e.Payload, &body)
		out = append(out, map[string]any{"t": e.Timestamp, "text": body["text"], "mode": body["mode"]})
	}
	return map[string]any{"source": s.Slug, "narratives": out}, nil
}
