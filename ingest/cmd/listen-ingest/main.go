// Command listen-ingest is the one service on a machine that connects to
// stations. Every other program there (the Observatory's listeners, the
// engine) asks it for a station over its Unix socket and gets the station's
// original bytes; a station is connected once, however many ask.
//
//	listen-ingest -socket /run/listen-ingest/feeds.sock
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
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vrontier/listen/ingest"
	"github.com/vrontier/listen/ingest/fetch"
)

func main() {
	var (
		socket       = flag.String("socket", "/run/listen-ingest/feeds.sock", "the Unix socket consumers ask on")
		mode         = flag.String("mode", "0660", "the socket's mode (octal); its group is the consumers'")
		idleAfter    = flag.Duration("idle-after", 10*time.Second, "how long a connection stays after its last consumer left")
		allowPrivate = flag.Bool("allow-private", false, "accept stations on private networks (a closed intranet, tests)")
	)
	var allowHosts list
	flag.Var(&allowHosts, "allow-host", "a host or host:port accepted although on a private network (repeatable)")
	flag.Parse()
	m, err := strconv.ParseUint(*mode, 8, 32)
	if err != nil {
		log.Fatalf("-mode: %v", err)
	}

	in := ingest.New(ingest.Options{
		Policy:    fetch.NewPolicy(*allowPrivate, false, allowHosts...),
		UserAgent: "listen-ingest/" + version(), IdleAfter: *idleAfter,
	})
	if err := os.Remove(*socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal(err)
	}
	ln, err := net.Listen("unix", *socket)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(*socket, os.FileMode(m)); err != nil {
		log.Fatal(err)
	}
	log.Printf("ingest on %s (mode %s)", *socket, *mode)
	srv := &http.Server{Handler: in.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	in.Close()
	shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	srv.Shutdown(shutdown)
	os.Remove(*socket)
}

func version() string {
	v := "0.1.0"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 {
				v += "+" + s.Value[:7]
			}
		}
	}
	return v
}

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(v string) error { *l = append(*l, v); return nil }
