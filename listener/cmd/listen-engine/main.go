// Command listen-engine is the engine process of the Listening Observatory
// Workbench: it holds many streams, each decoded once, each with its own
// analyses, and is told what to run through the control API on a Unix
// socket (the Workbench's docs/control-api.md).
//
//	listen-engine -socket /run/listen-engine/control.sock -memory-dir /var/lib/listen-engine/memory \
//	    -state /var/lib/listen-engine/state.json
//	listen-engine -addr 127.0.0.1:8200 -memory-dir /tmp/engine -allow-files   # development
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/vrontier/listen/listener/internal/engine"
)

func main() {
	lim := engine.DefaultLimits
	var (
		socket       = flag.String("socket", "/run/listen-engine/control.sock", "Unix socket of the control API (mode 0660); empty with -addr")
		addr         = flag.String("addr", "", "serve the control API on this loopback address instead of the socket (development only)")
		memDir       = flag.String("memory-dir", "/var/lib/listen-engine/memory", "memories: <dir>/<stream>/<analysis>/<key>/")
		stateFile    = flag.String("state", "/var/lib/listen-engine/state.json", "the configuration last given, resumed at start; empty: none")
		ffmpeg       = flag.String("ffmpeg", "ffmpeg", "ffmpeg binary")
		allowPrivate = flag.Bool("allow-private", false, "accept stream addresses on private networks (an intranet installation, tests)")
		allowFiles   = flag.Bool("allow-files", false, "accept file paths as stream inputs, looped in real time (development, tests)")
	)
	var allowHosts hostList
	flag.Var(&allowHosts, "allow-host", "a host or host:port accepted although on a private network (repeatable), e.g. a test server")
	flag.IntVar(&lim.Streams, "max-streams", lim.Streams, "streams the engine holds at most")
	flag.IntVar(&lim.AnalysesPerStream, "max-analyses-per-stream", lim.AnalysesPerStream, "analyses per stream at most")
	flag.IntVar(&lim.RulesPerAnalysis, "max-rules", lim.RulesPerAnalysis, "filter rules per analysis at most")
	flag.IntVar(&lim.SubscriptionsPerAnalysis, "max-subscriptions", lim.SubscriptionsPerAnalysis, "live subscriptions per analysis at most")
	flag.IntVar(&lim.MaxSampleRate, "max-sample-rate", lim.MaxSampleRate, "highest decode rate a stream may ask for")
	flag.IntVar(&lim.MaxBitrateKbps, "max-bitrate", lim.MaxBitrateKbps, "highest bit rate (kbit/s) a probe accepts")
	flag.IntVar(&lim.Groups, "max-groups", lim.Groups, "groups the engine holds at most")
	flag.IntVar(&lim.MembersPerGroup, "max-members-per-group", lim.MembersPerGroup, "analyses per group at most")
	flag.Parse()

	e, err := engine.New(engine.Options{
		MemoryDir: *memDir, StateFile: *stateFile, FFmpeg: *ffmpeg,
		AllowPrivate: *allowPrivate, AllowFiles: *allowFiles, AllowHosts: allowHosts, Limits: lim,
	})
	if err != nil {
		log.Fatal(err)
	}

	var ln net.Listener
	switch {
	case *addr != "":
		host, _, err := net.SplitHostPort(*addr)
		if err != nil || !(host == "127.0.0.1" || host == "::1" || host == "localhost") {
			log.Fatalf("-addr: only a loopback address, e.g. 127.0.0.1:8200")
		}
		ln, err = net.Listen("tcp", *addr)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("control API on http://%s (development)", *addr)
	case *socket != "":
		if err := os.Remove(*socket); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Fatal(err)
		}
		ln, err = net.Listen("unix", *socket)
		if err != nil {
			log.Fatal(err)
		}
		// Access is the socket's file permission: the engine's user and
		// its group, which the Workbench's user is in.
		if err := os.Chmod(*socket, 0o660); err != nil {
			log.Fatal(err)
		}
		log.Printf("control API on %s", *socket)
	default:
		log.Fatal("-socket or -addr is needed")
	}

	srv := &http.Server{Handler: e.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	log.Printf("stopping")
	e.Close() // ends the event streams too
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
	if *socket != "" && *addr == "" {
		os.Remove(*socket)
	}
}

type hostList []string

func (h *hostList) String() string     { return strings.Join(*h, ",") }
func (h *hostList) Set(v string) error { *h = append(*h, v); return nil }
