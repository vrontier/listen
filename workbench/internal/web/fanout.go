package web

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/vrontier/listen/workbench/internal/engine"
)

// fanout holds one engine subscription per analysis, or per group, and
// hands its events to every page and token reading it. The subscription ends when the last
// reader has gone, which is what lets an on-demand stream disconnect.
type fanout struct {
	eng *engine.Client

	mu     sync.Mutex
	topics map[string]*topic // "stream/analysis", or "group/" and the group
}

type topic struct {
	key       string
	subscribe func(context.Context, func(engine.SSE)) error
	readers   map[*reader]struct{}
	cancel    context.CancelFunc
}

// reader is one page or token holder. People are counted apart from
// programs, for the list of who is connected.
type reader struct {
	ch     chan []byte
	person bool
	who    string
	// frame, if set, writes an event the reader's own way, or drops it by
	// returning nil; a program's data stream names the source of each.
	frame func(engine.SSE) []byte
}

func newFanout(eng *engine.Client) *fanout {
	return &fanout{eng: eng, topics: map[string]*topic{}}
}

// join adds a reader of an analysis's events and starts the engine
// subscription if it is the first.
func (f *fanout) join(stream, analysis string, r *reader) func() {
	return f.joinTopic(stream+"/"+analysis, func(ctx context.Context, fn func(engine.SSE)) error {
		return f.eng.Subscribe(ctx, stream, analysis, fn)
	}, r)
}

// joinGroup adds a reader of a group's events.
func (f *fanout) joinGroup(group string, r *reader) func() {
	return f.joinTopic("group/"+group, func(ctx context.Context, fn func(engine.SSE)) error {
		return f.eng.SubscribeGroup(ctx, group, fn)
	}, r)
}

func (f *fanout) joinTopic(key string, subscribe func(context.Context, func(engine.SSE)) error, r *reader) func() {
	f.mu.Lock()
	t := f.topics[key]
	if t == nil {
		ctx, cancel := context.WithCancel(context.Background())
		t = &topic{key: key, subscribe: subscribe, readers: map[*reader]struct{}{}, cancel: cancel}
		f.topics[key] = t
		go f.run(ctx, t)
	}
	t.readers[r] = struct{}{}
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(t.readers, r)
		if len(t.readers) == 0 && f.topics[key] == t {
			t.cancel()
			delete(f.topics, key)
		}
	}
}

// run keeps the engine subscription open while the topic has readers.
func (f *fanout) run(ctx context.Context, t *topic) {
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := t.subscribe(ctx, func(ev engine.SSE) {
			msg := sseFrame(ev)
			f.mu.Lock()
			for r := range t.readers {
				m := msg
				if r.frame != nil {
					if m = r.frame(ev); m == nil {
						continue
					}
				}
				select {
				case r.ch <- m:
				default: // a slow reader loses events, as at the engine
				}
			}
			f.mu.Unlock()
		})
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > 10*time.Second {
			backoff = time.Second
		}
		log.Printf("live %s: %v; again in %s", t.key, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// Readers counts who reads an analysis now.
type Readers struct {
	People   int
	Programs int
}

func (n Readers) add(m Readers) Readers {
	return Readers{People: n.People + m.People, Programs: n.Programs + m.Programs}
}

func (f *fanout) readers(stream, analysis string) Readers {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n Readers
	if t := f.topics[stream+"/"+analysis]; t != nil {
		for r := range t.readers {
			if r.person {
				n.People++
			} else {
				n.Programs++
			}
		}
	}
	return n
}

func sseFrame(ev engine.SSE) []byte {
	b := make([]byte, 0, len(ev.Data)+64)
	if ev.ID != "" {
		b = append(b, "id: "...)
		b = append(b, ev.ID...)
		b = append(b, '\n')
	}
	if ev.Event != "" {
		b = append(b, "event: "...)
		b = append(b, ev.Event...)
		b = append(b, '\n')
	}
	b = append(b, "data: "...)
	b = append(b, ev.Data...)
	b = append(b, "\n\n"...)
	return b
}
