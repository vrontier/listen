package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vrontier/listen/listener/internal/fetch"
)

// Options configure the ingest.
type Options struct {
	Policy    *fetch.Policy
	UserAgent string
	// IdleAfter is how long a connection stays open after its last
	// consumer left, so that a consumer reconnecting finds it (default 10 s).
	IdleAfter time.Duration
	// ConnectTimeout bounds how long a first consumer waits for the
	// connection (default 20 s).
	ConnectTimeout time.Duration
}

// Ingest holds one feed per address.
type Ingest struct {
	opts   Options
	client *http.Client
	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	feeds map[string]*feed
}

func New(opts Options) *Ingest {
	if opts.IdleAfter == 0 {
		opts.IdleAfter = 10 * time.Second
	}
	if opts.ConnectTimeout == 0 {
		opts.ConnectTimeout = 20 * time.Second
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "listen-ingest"
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Ingest{opts: opts, client: opts.Policy.Client(opts.UserAgent), ctx: ctx, cancel: cancel, feeds: map[string]*feed{}}
}

// Close ends every feed.
func (in *Ingest) Close() {
	in.cancel()
	in.mu.Lock()
	feeds := make([]*feed, 0, len(in.feeds))
	for _, f := range in.feeds {
		feeds = append(feeds, f)
	}
	in.mu.Unlock()
	for _, f := range feeds {
		f.stop()
	}
}

// subBuffer is how many pieces wait for a consumer. One that falls further
// behind is disconnected rather than given a stream with holes; it joins
// again and gets the headers first.
const subBuffer = 1024

type sub struct {
	ch   chan []byte
	done chan struct{}
}

// feed is one connection to one address, shared by its consumers.
type feed struct {
	in     *Ingest
	url    string
	ctx    context.Context
	cancel context.CancelFunc
	ready  chan struct{} // closed when connected or failed
	err    error         // why it failed, set before ready is closed

	format, contentType string
	bytes               atomic.Int64
	started             time.Time

	mu       sync.Mutex
	fr       framer
	subs     map[*sub]struct{}
	lastLeft time.Time
	ended    bool
}

// subscribe returns the feed of url with a new consumer on it, making the
// connection if there is none.
func (in *Ingest) subscribe(url string) (*feed, *sub, error) {
	in.mu.Lock()
	f := in.feeds[url]
	if f == nil {
		ctx, cancel := context.WithCancel(in.ctx)
		f = &feed{in: in, url: url, ctx: ctx, cancel: cancel, ready: make(chan struct{}), subs: map[*sub]struct{}{}, started: time.Now()}
		in.feeds[url] = f
		go f.run()
	}
	in.mu.Unlock()

	select {
	case <-f.ready:
	case <-time.After(in.opts.ConnectTimeout):
		return nil, nil, &fetch.OpenError{P: &fetch.Problem{Code: "no_answer", Message: "the station did not answer in time"}}
	}
	if f.err != nil {
		return nil, nil, f.err
	}
	s := &sub{ch: make(chan []byte, subBuffer), done: make(chan struct{})}
	f.mu.Lock()
	if f.ended {
		f.mu.Unlock()
		return nil, nil, &fetch.OpenError{P: &fetch.Problem{Code: "no_answer", Message: "the connection ended"}}
	}
	// The headers a late consumer needs, then everything from here on: the
	// next piece is taken and sent under this same lock.
	for _, h := range f.fr.headers() {
		s.ch <- h
	}
	f.subs[s] = struct{}{}
	f.mu.Unlock()
	return f, s, nil
}

func (f *feed) unsubscribe(s *sub) {
	f.mu.Lock()
	if _, ok := f.subs[s]; ok {
		delete(f.subs, s)
		close(s.done)
	}
	if len(f.subs) == 0 {
		f.lastLeft = time.Now()
	}
	f.mu.Unlock()
}

// run connects, then reads and hands out pieces until the connection ends
// or the feed has been idle too long. Every end closes the consumers: they
// reconnect, and the next one makes a new connection.
func (f *feed) run() {
	defer f.remove()
	body, format, ct, err := fetch.Open(f.ctx, f.in.client, f.in.opts.UserAgent, f.url, &f.bytes)
	if err != nil {
		f.err = err
		close(f.ready)
		return
	}
	defer body.Close()
	f.format, f.contentType = format, ct
	f.fr = newFramer(body)
	close(f.ready)
	log.Printf("ingest: connected %s (%s)", f.url, ct)

	go f.watchIdle(body)
	for {
		piece, err := f.fr.read()
		if err != nil {
			if f.ctx.Err() == nil {
				log.Printf("ingest: %s ended: %v", f.url, err)
			}
			return
		}
		f.mu.Lock()
		f.fr.take(piece)
		for s := range f.subs {
			select {
			case s.ch <- piece:
			default:
				// Too slow: disconnected, it joins again with the headers.
				delete(f.subs, s)
				close(s.done)
			}
		}
		if len(f.subs) == 0 && f.lastLeft.IsZero() {
			f.lastLeft = time.Now()
		}
		f.mu.Unlock()
	}
}

// watchIdle closes the connection once no consumer has been on it for
// IdleAfter.
func (f *feed) watchIdle(body io.Closer) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-f.ctx.Done():
			return
		case <-t.C:
		}
		f.mu.Lock()
		idle := len(f.subs) == 0 && !f.lastLeft.IsZero() && time.Since(f.lastLeft) >= f.in.opts.IdleAfter
		f.mu.Unlock()
		if idle {
			log.Printf("ingest: %s idle for %s, closing", f.url, f.in.opts.IdleAfter)
			f.cancel()
			body.Close()
			return
		}
	}
}

// remove takes the feed out of the registry and ends its consumers.
func (f *feed) remove() {
	f.in.mu.Lock()
	if f.in.feeds[f.url] == f {
		delete(f.in.feeds, f.url)
	}
	f.in.mu.Unlock()
	f.mu.Lock()
	f.ended = true
	for s := range f.subs {
		delete(f.subs, s)
		close(s.done)
	}
	f.mu.Unlock()
	f.cancel()
}

func (f *feed) stop() { f.cancel() }

// --- the API on the socket ---

// Handler serves
//
//	GET /v1/feed?url=…   the stream's bytes: its headers first, then live
//	GET /v1/feeds        the feeds held now (for the operator)
func (in *Ingest) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /v1/feed", in.serveFeed)
	m.HandleFunc("GET /v1/feeds", in.serveFeeds)
	return m
}

// Headers of a feed's response.
const (
	HeaderFormat      = "Ingest-Format"       // ffmpeg's input format, or empty
	HeaderContentType = "Ingest-Content-Type" // as the station's server sent it
)

func (in *Ingest) serveFeed(w http.ResponseWriter, r *http.Request) {
	url := r.URL.Query().Get("url")
	if p := in.opts.Policy.CheckStatic(url); p != nil {
		writeProblem(w, http.StatusForbidden, p)
		return
	}
	f, s, err := in.subscribe(url)
	if err != nil {
		var oe *fetch.OpenError
		if errors.As(err, &oe) {
			status := http.StatusBadGateway
			if oe.Refused {
				status = http.StatusForbidden
			}
			writeProblem(w, status, oe.P)
			return
		}
		writeProblem(w, http.StatusBadGateway, &fetch.Problem{Code: "no_answer", Message: err.Error()})
		return
	}
	defer f.unsubscribe(s)
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set(HeaderFormat, f.format)
	w.Header().Set(HeaderContentType, f.contentType)
	w.WriteHeader(http.StatusOK)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.done:
			// Ended or too slow: drain what is queued, then close.
			for {
				select {
				case piece := <-s.ch:
					if _, err := w.Write(piece); err != nil {
						return
					}
				default:
					return
				}
			}
		case piece := <-s.ch:
			if _, err := w.Write(piece); err != nil {
				return
			}
			if fl != nil && len(s.ch) == 0 {
				fl.Flush()
			}
		}
	}
}

// FeedInfo is one feed as GET /v1/feeds lists it.
type FeedInfo struct {
	URL         string    `json:"url"`
	Consumers   int       `json:"consumers"`
	Bytes       int64     `json:"bytes"`
	Since       time.Time `json:"since"`
	ContentType string    `json:"content_type"`
}

func (in *Ingest) serveFeeds(w http.ResponseWriter, r *http.Request) {
	in.mu.Lock()
	feeds := make([]*feed, 0, len(in.feeds))
	for _, f := range in.feeds {
		feeds = append(feeds, f)
	}
	in.mu.Unlock()
	out := []FeedInfo{}
	for _, f := range feeds {
		select {
		case <-f.ready:
		default:
			continue
		}
		f.mu.Lock()
		n := len(f.subs)
		f.mu.Unlock()
		out = append(out, FeedInfo{URL: f.url, Consumers: n, Bytes: f.bytes.Load(), Since: f.started.UTC(), ContentType: f.contentType})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"feeds": out})
}

func writeProblem(w http.ResponseWriter, status int, p *fetch.Problem) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{"error": p})
}
