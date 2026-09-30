// Command listener ingests an audio file or stream, analyses it and serves
// the realtime event stream over WebSocket.
//
//	listener -in ../samples/capture.mp3 -loop          # develop against a capture
//	listener -in https://stream.example.org/live
//	listener -in capture.mp3 -dump > events.jsonl      # offline, as fast as possible
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/vrontier/listen/listener/internal/analysis"
	"github.com/vrontier/listen/listener/internal/events"
	"github.com/vrontier/listen/listener/internal/hub"
	"github.com/vrontier/listen/listener/internal/source"
)

func main() {
	var (
		in       = flag.String("in", "", "audio file or stream URL (required)")
		addr     = flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
		src      = flag.String("source", "live", "source identifier in every event")
		loop     = flag.Bool("loop", false, "repeat file input forever")
		realtime = flag.Bool("realtime", true, "pace file input at playback speed")
		dump     = flag.Bool("dump", false, "write events as JSON lines to stdout instead of serving; implies -realtime=false for files")
		origins  = flag.String("origins", "localhost:*,127.0.0.1:*,listen.home.arpa,listen.vrontier.org",
			"comma-separated Origin host patterns allowed to open /ws/live")
		ffmpeg  = flag.String("ffmpeg", "ffmpeg", "ffmpeg binary")
		ffprobe = flag.String("ffprobe", "ffprobe", "ffprobe binary")
		start   = flag.String("start", "", "recording time of a file's first sample (RFC 3339); "+
			"default: parsed from a YYYYMMDDTHHMMSSZ stamp in the file name")
	)
	flag.Parse()
	if *in == "" {
		flag.Usage()
		os.Exit(2)
	}
	if *dump {
		rt := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "realtime" {
				rt = *realtime
			}
		})
		*realtime = rt
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	isURL := source.IsURL(*in)
	var rec replay
	if !isURL {
		var err error
		if rec, err = replayClock(*in, *start, *loop, *ffprobe); err != nil {
			log.Fatal(err)
		}
	}

	if *dump {
		runDump(ctx, *in, *src, *loop, *realtime, *ffmpeg, rec)
		return
	}

	h := hub.New(splitList(*origins))
	stamper := events.NewStamper(*src)
	a := analysis.New(stamper, h.Broadcast)

	a.Live(!rec.known && (isURL || *realtime))
	st := &status{input: describe(*in), started: time.Now(), stream: source.Connecting}
	if rec.known {
		// Timestamps are recording times; the distance to now isn't latency.
		log.Printf("replay: clock pinned to recording time %s", rec.start.Format(time.RFC3339))
	} else {
		a.OnFrame(st.setLatency)
	}

	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			h.Broadcast(stamper.Stamp(events.TypeStatus, time.Now(), st.payload(h.Listeners())))
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()

	srcDone := make(chan error, 1)
	go func() {
		srcDone <- source.Run(ctx, source.Config{
			Input: *in, SampleRate: analysis.SampleRate, Realtime: *realtime, Loop: *loop, FFmpeg: *ffmpeg,
			OnSamples: a.Feed,
			OnState: func(s string) {
				switch {
				case s != source.Connected:
				case rec.known:
					a.Replay(rec.start, rec.wrap)
				case isURL || *realtime:
					// Re-pin the observation clock to wall time on every
					// (re)connect so timestamps survive gaps.
					a.Anchor(time.Now())
				}
				prev := st.setStream(s)
				if s != prev {
					log.Printf("stream: %s", s)
					h.Broadcast(stamper.Stamp(events.TypeStatus, time.Now(), st.payload(h.Listeners())))
				}
			},
		})
	}()

	mux := http.NewServeMux()
	h.Routes(mux)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Printf("listening on http://%s (ws: /ws/live, snapshot: /api/state/current), input %s", *addr, describe(*in))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-srcDone:
		if err != nil {
			log.Printf("source: %v", err)
		}
		log.Printf("input finished; serving the final state until interrupted")
		<-ctx.Done()
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
}

func runDump(ctx context.Context, in, src string, loop, realtime bool, ffmpeg string, rec replay) {
	w := bufio.NewWriterSize(os.Stdout, 1<<16)
	defer w.Flush()
	stamper := events.NewStamper(src)
	a := analysis.New(stamper, func(m events.Message) {
		w.Write(m.Data)
		w.WriteByte('\n')
	})
	// Offline runs use the recording time, or a fixed epoch, so repeated
	// runs produce identical output.
	switch {
	case rec.known:
		a.Replay(rec.start, rec.wrap)
	case !realtime && !source.IsURL(in):
		a.Anchor(time.Unix(0, 0))
	}
	err := source.Run(ctx, source.Config{
		Input: in, SampleRate: analysis.SampleRate, Realtime: realtime, Loop: loop, FFmpeg: ffmpeg,
		OnSamples: a.Feed,
	})
	if err != nil {
		w.Flush()
		fmt.Fprintln(os.Stderr, "listener:", err)
		os.Exit(1)
	}
}

// replay describes the recording clock of a file input.
type replay struct {
	known bool
	start time.Time
	wrap  time.Duration // file length when looping
}

var stampRE = regexp.MustCompile(`(\d{8}T\d{6}Z)`)

func replayClock(in, start string, loop bool, ffprobe string) (replay, error) {
	var r replay
	switch {
	case start != "":
		t, err := time.Parse(time.RFC3339, start)
		if err != nil {
			return r, fmt.Errorf("-start: %w", err)
		}
		r.start, r.known = t, true
	default:
		m := stampRE.FindString(filepath.Base(in))
		if m == "" {
			return r, nil
		}
		t, err := time.Parse("20060102T150405Z", m)
		if err != nil {
			return r, nil
		}
		r.start, r.known = t, true
	}
	if loop {
		d, err := source.Duration(ffprobe, in)
		if err != nil {
			return r, err
		}
		r.wrap = d
	}
	return r, nil
}

type status struct {
	mu         sync.Mutex
	input      string
	started    time.Time
	stream     string
	latency    time.Duration
	reconnects int
}

func (s *status) setStream(v string) (prev string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, s.stream = s.stream, v
	if v == source.Reconnecting && prev != source.Reconnecting {
		s.reconnects++
	}
	return prev
}

func (s *status) setLatency(d time.Duration) {
	s.mu.Lock()
	s.latency = d
	s.mu.Unlock()
}

func (s *status) payload(listeners int) events.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	analysisState := "running"
	if s.stream != source.Connected {
		analysisState = "idle"
	}
	lat := s.latency
	if lat < 0 {
		lat = 0
	}
	return events.Status{
		Stream: s.stream, Analysis: analysisState,
		LatencyMs:  float64(lat.Milliseconds()),
		Listeners:  listeners,
		Input:      s.input,
		SampleRate: analysis.SampleRate,
		UptimeS:    float64(int(time.Since(s.started).Seconds())),
		Reconnects: s.reconnects,
	}
}

// describe hides local paths from clients: files are reported by base name.
func describe(in string) string {
	if source.IsURL(in) {
		return in
	}
	if i := strings.LastIndexAny(in, `/\`); i >= 0 {
		return "file:" + in[i+1:]
	}
	return "file:" + in
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
