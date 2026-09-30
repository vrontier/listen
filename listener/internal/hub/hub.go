// Package hub fans events out to WebSocket clients (§3) and keeps the
// current-state snapshot served at /api/state/current (§25).
package hub

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/vrontier/listen/listener/internal/events"
)

const (
	clientBuffer = 256
	maxClients   = 64
	recentEvents = 40
	writeTimeout = 5 * time.Second
	pingEvery    = 20 * time.Second
)

type client struct {
	ch      chan []byte
	dropped atomic.Int64
}

// MemoryView is what the memory contributes to the snapshot.
type MemoryView interface {
	ActiveMotifs() []any
	Summaries() map[string]events.Envelope
}

type Hub struct {
	origins []string
	memory  MemoryView

	mu      sync.RWMutex
	clients map[*client]struct{}

	// snapshot
	frame, spectrum, feature, status *events.Envelope
	resonances                       map[string]events.Envelope
	recent                           []events.Envelope
}

// New returns a hub that accepts WebSocket connections from the given origin
// host patterns (see websocket.AcceptOptions.OriginPatterns). Same-host
// requests are always allowed.
func New(origins []string) *Hub {
	return &Hub{origins: origins, clients: map[*client]struct{}{}, resonances: map[string]events.Envelope{}}
}

// SetMemory adds the memory's active motifs and summaries to snapshots.
func (h *Hub) SetMemory(m MemoryView) { h.memory = m }

func (h *Hub) Listeners() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Broadcast records msg in the snapshot and queues it for every client. A
// client whose queue is full misses the message rather than slowing others.
func (h *Hub) Broadcast(m events.Message) {
	h.mu.Lock()
	env := m.Env
	switch m.Type {
	case events.TypeFrame:
		h.frame = &env
	case events.TypeSpectrum:
		h.spectrum = &env
	case events.TypeFeature:
		h.feature = &env
	case events.TypeStatus:
		h.status = &env
	case events.TypeResonance:
		r := env.Payload.(events.Resonance)
		if r.Status == "end" {
			delete(h.resonances, r.ID)
		} else {
			h.resonances[r.ID] = env
		}
		if r.Status != "update" {
			h.addRecent(env)
		}
	case events.TypeTransient, events.TypeMotifDetected, events.TypeMotifReturned:
		h.addRecent(env)
	}
	for c := range h.clients {
		select {
		case c.ch <- m.Data:
		default:
			c.dropped.Add(1)
		}
	}
	h.mu.Unlock()
}

func (h *Hub) addRecent(env events.Envelope) {
	h.recent = append(h.recent, env)
	if len(h.recent) > recentEvents {
		h.recent = h.recent[len(h.recent)-recentEvents:]
	}
}

// Snapshot is the /api/state/current response.
type Snapshot struct {
	Frame            *events.Envelope           `json:"frame"`
	Spectrum         *events.Envelope           `json:"spectrum"`
	Features         *events.Envelope           `json:"features"`
	ActiveResonances []events.Envelope          `json:"active_resonances"`
	RecentEvents     []events.Envelope          `json:"recent_events"`
	ActiveMotifs     []any                      `json:"active_motifs"`
	Memory           map[string]events.Envelope `json:"memory"`    // latest memory.summary per window
	Narrative        any                        `json:"narrative"` // phase 3
	System           *events.Envelope           `json:"system"`
}

func (h *Hub) Snapshot() Snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s := Snapshot{
		Frame: h.frame, Spectrum: h.spectrum, Features: h.feature, System: h.status,
		ActiveResonances: make([]events.Envelope, 0, len(h.resonances)),
		RecentEvents:     append([]events.Envelope{}, h.recent...),
		ActiveMotifs:     []any{},
	}
	for _, r := range h.resonances {
		s.ActiveResonances = append(s.ActiveResonances, r)
	}
	if h.memory != nil {
		s.ActiveMotifs = h.memory.ActiveMotifs()
		s.Memory = h.memory.Summaries()
	}
	return s
}

// Routes registers /ws/live, /api/state/current and /healthz on mux.
func (h *Hub) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ws/live", h.serveWS)
	mux.HandleFunc("GET /api/state/current", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(h.Snapshot())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})
}

func (h *Hub) serveWS(w http.ResponseWriter, r *http.Request) {
	if h.Listeners() >= maxClients {
		http.Error(w, "too many listeners", http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.origins})
	if err != nil {
		log.Printf("ws: accept from %s: %v", r.RemoteAddr, err)
		return
	}
	c := &client{ch: make(chan []byte, clientBuffer)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, c)
		h.mu.Unlock()
		if d := c.dropped.Load(); d > 0 {
			log.Printf("ws: %s disconnected, %d messages dropped", r.RemoteAddr, d)
		}
	}()

	// The stream is server → browser only; CloseRead handles control frames
	// and cancels ctx when the client goes away.
	ctx := conn.CloseRead(r.Context())
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case data := <-c.ch:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Write(wctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				conn.Close(websocket.StatusGoingAway, "write failed")
				return
			}
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				conn.Close(websocket.StatusGoingAway, "ping failed")
				return
			}
		}
	}
}
