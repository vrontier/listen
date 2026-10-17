// Command listen-standin stands in for the engine process: it serves the
// control API of docs/control-api.md with made-up data and connects to no
// stream. It is for developing and testing the Workbench until the engine
// process exists.
//
//	listen-standin -socket /tmp/listen-engine.sock
//	listen-standin -addr 127.0.0.1:8091
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
	"syscall"
	"time"

	"github.com/vrontier/listen-workbench/internal/standin"
)

func main() {
	var (
		socket       = flag.String("socket", "", "Unix socket to serve the control API on")
		addr         = flag.String("addr", "", "loopback address instead of a socket, for development (e.g. 127.0.0.1:8091)")
		allowPrivate = flag.Bool("allow-private", false, "accept stream addresses on private networks")
	)
	flag.Parse()
	var ln net.Listener
	var err error
	switch {
	case *socket != "":
		os.Remove(*socket)
		ln, err = net.Listen("unix", *socket)
		if err == nil {
			os.Chmod(*socket, 0o660)
		}
	case *addr != "":
		ln, err = net.Listen("tcp", *addr)
	default:
		log.Fatal("give -socket or -addr")
	}
	if err != nil {
		log.Fatal(err)
	}
	e := standin.New(standin.Options{AllowPrivate: *allowPrivate})
	hs := &http.Server{Handler: e, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("stand-in engine on %s (made-up data, no stream is connected)", ln.Addr())
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	<-ctx.Done()
	// End the event streams first, so that shutting down does not wait for
	// them. Closing the listener removes the socket file.
	e.Close()
	shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	hs.Shutdown(shut)
}
