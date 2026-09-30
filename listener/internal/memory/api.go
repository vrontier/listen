package memory

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

// MotifView is a motif as the API and the browser see it.
type MotifView struct {
	ID          string           `json:"motif_id"`
	Kind        string           `json:"kind"`
	Created     time.Time        `json:"created"`
	LastSeen    time.Time        `json:"last_seen"`
	LastSeenS   float64          `json:"last_seen_s"`
	Active      bool             `json:"active"`
	Occurrences int              `json:"occurrences"`
	PresenceS   float64          `json:"presence_s"`
	Confidence  float64          `json:"confidence"`
	Signature   events.Signature `json:"signature"`
	Visual      events.Visual    `json:"visual"`
	Recent      []Occurrence     `json:"recent,omitempty"`
}

func (m *Memory) view(mo *Motif, withRecent bool) MotifView {
	v := MotifView{
		ID: mo.ID, Kind: mo.Kind, Created: mo.Created, LastSeen: mo.LastSeen,
		LastSeenS: round(m.now.Sub(mo.LastSeen).Seconds(), 0), Active: mo.active(),
		Occurrences: mo.Occurrences, PresenceS: round(mo.PresenceS, 1), Confidence: mo.Confidence(),
		Signature: mo.Signature(), Visual: mo.Visual(),
	}
	if withRecent {
		v.Recent = mo.Recent
	}
	return v
}

// Motifs returns the established motifs, most present first.
func (m *Memory) Motifs() []MotifView {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []MotifView
	for _, mo := range m.motifs {
		if mo.established() {
			out = append(out, m.view(mo, false))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PresenceS > out[j].PresenceS })
	return out
}

// ActiveMotifs are the established motifs sounding now (for the snapshot).
func (m *Memory) ActiveMotifs() []any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []any{}
	for _, mo := range m.motifs {
		if mo.established() && mo.active() {
			out = append(out, m.view(mo, false))
		}
	}
	return out
}

// Summaries returns the latest memory.summary per window.
func (m *Memory) Summaries() map[string]events.Envelope {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]events.Envelope, len(m.summaries))
	for k, v := range m.summaries {
		out[k] = v
	}
	return out
}

// Routes registers the historical API (§26):
//
//	GET /api/motifs                  established motifs
//	GET /api/motifs/{id}             one motif with its recent occurrences
//	GET /api/history/events          ?from=&to= (RFC 3339, default last hour), ?type=, ?limit=
//	GET /api/history/features        ?window=1h|24h: per-minute aggregates
func (m *Memory) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/motifs", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		aliases := make(map[string]string, len(m.aliases))
		for k, v := range m.aliases {
			aliases[k] = v
		}
		m.mu.Unlock()
		writeJSON(w, map[string]any{"motifs": nonNil(m.Motifs()), "aliases": aliases})
	})
	mux.HandleFunc("GET /api/motifs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		m.mu.Lock()
		if a, ok := m.aliases[id]; ok {
			id = a
		}
		var found *MotifView
		for _, mo := range m.motifs {
			if mo.ID == id {
				v := m.view(mo, true)
				found = &v
			}
		}
		m.mu.Unlock()
		if found == nil {
			http.Error(w, "unknown motif", http.StatusNotFound)
			return
		}
		writeJSON(w, found)
	})
	mux.HandleFunc("GET /api/history/events", func(w http.ResponseWriter, r *http.Request) {
		if m.store == nil {
			writeJSON(w, map[string]any{"events": []any{}})
			return
		}
		q := r.URL.Query()
		now := m.clock()
		from, to := now.Add(-time.Hour), now.Add(time.Second)
		if t, err := time.Parse(time.RFC3339, q.Get("from")); err == nil {
			from = t
		}
		if t, err := time.Parse(time.RFC3339, q.Get("to")); err == nil {
			to = t
		}
		limit := 2000
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n < limit {
			limit = n
		}
		typ := q.Get("type")
		out := []json.RawMessage{}
		m.mu.Lock()
		m.store.flush()
		m.mu.Unlock()
		m.store.scan(from, to, func(rec record) bool {
			if rec.Event == nil {
				return true
			}
			if typ != "" {
				var head struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(rec.Event, &head) != nil || head.Type != typ {
					return true
				}
			}
			out = append(out, rec.Event)
			return len(out) < limit
		})
		writeJSON(w, map[string]any{"from": from, "to": to, "events": out})
	})
	mux.HandleFunc("GET /api/history/features", func(w http.ResponseWriter, r *http.Request) {
		window := time.Hour
		if r.URL.Query().Get("window") == "24h" {
			window = 24 * time.Hour
		}
		m.mu.Lock()
		cut := m.now.Add(-window)
		out := []*minute{}
		for _, mi := range m.minutes {
			if !mi.T.Before(cut) {
				out = append(out, mi)
			}
		}
		m.mu.Unlock()
		writeJSON(w, map[string]any{"window": window.String(), "minutes": out})
	})
}

func (m *Memory) clock() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.now
}

func nonNil(v []MotifView) []MotifView {
	if v == nil {
		return []MotifView{}
	}
	return v
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(v)
}
