// Command listen-engine is the engine process of the Listening Observatory
// Workbench: it holds many streams, each source read once and shared, each
// stream with its own analyses, and is told what to run through the control
// API (the Workbench's docs/control-api.md). Every caller has a socket of
// its own and sees only what it configured itself.
//
//	listen-engine -caller workbench=/run/listen-engine/control.sock:0660 \
//	    -caller internal=/run/listen-engine/internal.sock:0600 \
//	    -memory-dir /var/lib/listen-engine/memory -state /var/lib/listen-engine/state.json
//	listen-engine -addr 127.0.0.1:8200 -memory-dir /tmp/engine -allow-files   # development
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vrontier/listen/listener/internal/engine"
)

func main() {
	lim := engine.DefaultLimits
	var (
		socket       = flag.String("socket", "/run/listen-engine/control.sock", "the default caller's Unix socket (mode 0660), when no -caller is given")
		addr         = flag.String("addr", "", "serve the default caller on this loopback address instead of a socket (development only)")
		memDir       = flag.String("memory-dir", "/var/lib/listen-engine/memory", "memories: <dir>/<caller>/<stream>/<analysis>/<key>/")
		stateFile    = flag.String("state", "/var/lib/listen-engine/state.json", "the first caller's configuration, resumed at start (the others' beside it); empty: none")
		ffmpeg       = flag.String("ffmpeg", "ffmpeg", "ffmpeg binary")
		allowPrivate = flag.Bool("allow-private", false, "accept stream addresses on private networks (an intranet installation, tests)")
		allowFiles   = flag.Bool("allow-files", false, "accept file paths as stream inputs, looped in real time (development, tests)")
		testHooks    = flag.Bool("test-hooks", false, "give callers served on a loopback port the test routes (POST …/analyses/{a}/fail); never on a socket")
		ingestSock   = flag.String("ingest", "", "read stations through listen-ingest on this socket (e.g. /run/listen-ingest/feeds.sock); empty: connect itself")
	)
	var allowHosts list
	var callers list
	flag.Var(&allowHosts, "allow-host", "a host or host:port accepted although on a private network (repeatable), e.g. a test server")
	flag.Var(&callers, "caller", "a caller and where it is served, repeatable: name=/path/to.sock[:mode] (mode octal, default 0660) "+
		"or name=tcp:127.0.0.1:port (development); the first is the default caller")
	flag.IntVar(&lim.Streams, "max-streams", lim.Streams, "streams the engine holds at most, all callers together")
	flag.IntVar(&lim.AnalysesPerStream, "max-analyses-per-stream", lim.AnalysesPerStream, "analyses per stream at most")
	flag.IntVar(&lim.RulesPerAnalysis, "max-rules", lim.RulesPerAnalysis, "filter rules per analysis at most")
	flag.IntVar(&lim.SubscriptionsPerAnalysis, "max-subscriptions", lim.SubscriptionsPerAnalysis, "live subscriptions per analysis or group at most")
	flag.IntVar(&lim.MaxSampleRate, "max-sample-rate", lim.MaxSampleRate, "highest decode rate a stream may ask for")
	flag.IntVar(&lim.MaxBitrateKbps, "max-bitrate", lim.MaxBitrateKbps, "highest bit rate (kbit/s) a probe accepts")
	flag.IntVar(&lim.Groups, "max-groups", lim.Groups, "groups the engine holds at most, all callers together")
	flag.IntVar(&lim.MembersPerGroup, "max-members-per-group", lim.MembersPerGroup, "analyses per group at most")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("listen-engine", engine.Version())
		return
	}

	var specs []callerSpec
	for _, c := range callers {
		cs, err := parseCaller(c)
		if err != nil {
			log.Fatalf("-caller %s: %v", c, err)
		}
		specs = append(specs, cs)
	}
	if len(specs) == 0 {
		cs := callerSpec{name: engine.DefaultCaller, path: *socket, mode: 0o660}
		if *addr != "" {
			cs.path, cs.tcp = *addr, true
		}
		specs = append(specs, cs)
	}
	names := make([]string, len(specs))
	var hooks []string
	for i, cs := range specs {
		names[i] = cs.name
		if *testHooks && cs.tcp {
			hooks = append(hooks, cs.name)
		}
	}

	e, err := engine.New(engine.Options{
		MemoryDir: *memDir, StateFile: *stateFile, Callers: names, FFmpeg: *ffmpeg,
		AllowPrivate: *allowPrivate, AllowFiles: *allowFiles, AllowHosts: allowHosts, Limits: lim, TestHooks: hooks,
		Ingest: *ingestSock,
	})
	if err != nil {
		log.Fatal(err)
	}

	var servers []*http.Server
	for _, cs := range specs {
		ln, err := cs.listen()
		if err != nil {
			log.Fatalf("caller %s: %v", cs.name, err)
		}
		srv := &http.Server{Handler: e.Caller(cs.name).Handler(), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, srv)
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
		}()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	log.Printf("stopping")
	e.Close() // ends the event streams too
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, srv := range servers {
		srv.Shutdown(shutdown)
	}
	for _, cs := range specs {
		if !cs.tcp {
			os.Remove(cs.path)
		}
	}
}

type callerSpec struct {
	name string
	path string // socket path, or host:port with tcp
	tcp  bool
	mode os.FileMode
}

// parseCaller reads name=/path/to.sock[:mode] or name=tcp:127.0.0.1:port.
func parseCaller(s string) (callerSpec, error) {
	name, where, ok := strings.Cut(s, "=")
	if !ok || name == "" || where == "" {
		return callerSpec{}, errors.New("want name=/path/to.sock[:mode] or name=tcp:127.0.0.1:port")
	}
	cs := callerSpec{name: name, mode: 0o660}
	if hp, ok := strings.CutPrefix(where, "tcp:"); ok {
		cs.path, cs.tcp = hp, true
		return cs, nil
	}
	cs.path = where
	if i := strings.LastIndexByte(where, ':'); i > 0 {
		m, err := strconv.ParseUint(where[i+1:], 8, 32)
		if err != nil {
			return callerSpec{}, fmt.Errorf("mode %q: %v", where[i+1:], err)
		}
		cs.path, cs.mode = where[:i], os.FileMode(m)
	}
	return cs, nil
}

// listen opens the caller's socket. Access is its file permission: the
// engine's user, and the group a caller's user is in where the mode lets
// it. A TCP address is for development and must be loopback.
func (cs callerSpec) listen() (net.Listener, error) {
	if cs.tcp {
		host, _, err := net.SplitHostPort(cs.path)
		if err != nil || !(host == "127.0.0.1" || host == "::1" || host == "localhost") {
			return nil, errors.New("only a loopback address, e.g. 127.0.0.1:8200")
		}
		ln, err := net.Listen("tcp", cs.path)
		if err == nil {
			log.Printf("caller %s: control API on http://%s (development)", cs.name, cs.path)
		}
		return ln, err
	}
	if err := os.Remove(cs.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", cs.path)
	if err != nil {
		return nil, err
	}
	// Windows keeps no Unix modes; access there is the directory's.
	if err := os.Chmod(cs.path, cs.mode); err != nil && runtime.GOOS != "windows" {
		ln.Close()
		return nil, err
	}
	log.Printf("caller %s: control API on %s (mode %04o)", cs.name, cs.path, cs.mode)
	return ln, nil
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }
