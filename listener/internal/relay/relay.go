package relay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/vrontier/listen/listener/internal/source"
)

// Codec is one encoded rendition of the audio.
type Codec struct {
	Name string   // query value: ?codec=aac
	MIME string   // for MediaSource.isTypeSupported in the browser
	Args []string // ffmpeg output options
}

var fmp4 = []string{"-f", "mp4", "-movflags", "+frag_keyframe+empty_moov+default_base_moof", "-frag_duration", "500000"}

// Codecs offered to browsers: Safari plays AAC, open-source Chromium builds
// only Opus. Both are cut into ~0.5 s fragments.
var Codecs = []Codec{
	{Name: "aac", MIME: `audio/mp4; codecs="mp4a.40.2"`, Args: append([]string{"-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "44100"}, fmp4...)},
	{Name: "opus", MIME: `audio/mp4; codecs="opus"`, Args: append([]string{"-c:a", "libopus", "-b:a", "96k", "-ac", "2", "-ar", "48000"}, fmp4...)},
}

const (
	backlog      = 3 * time.Second // audio sent to a new listener to start from
	clientBuffer = 64              // ~30 s of fragments
	maxClients   = 16
	writeTimeout = 5 * time.Second
)

// Header precedes every binary message on /ws/audio:
//
//	uint32 big-endian header length | header JSON | segment bytes
//
// Offset is the position (seconds on the listener's audio timeline) of media
// time 0 in this ffmpeg run: the browser sets SourceBuffer.timestampOffset
// to it, so the element's currentTime is the audio position and can be
// compared directly with the events' position_s.
type Header struct {
	Kind    string  `json:"kind"` // init | fragment
	Codec   string  `json:"codec"`
	MIME    string  `json:"mime,omitempty"`
	Session int     `json:"session"`
	Offset  float64 `json:"offset"`
	Pos     float64 `json:"pos"` // position of the segment's first sample
}

type segment struct {
	hdr  Header
	data []byte // encoded message
}

type stream struct {
	codec   Codec
	mu      sync.Mutex
	session int
	init    *segment
	recent  []*segment
	clients map[chan []byte]struct{}
}

// Relay holds one stream per codec.
type Relay struct {
	sampleRate int
	origins    []string
	streams    map[string]*stream
	mu         sync.Mutex
	listeners  int
}

func New(sampleRate int, origins []string) *Relay {
	r := &Relay{sampleRate: sampleRate, origins: origins, streams: map[string]*stream{}}
	for _, c := range Codecs {
		r.streams[c.Name] = &stream{codec: c, clients: map[chan []byte]struct{}{}}
	}
	return r
}

// Names lists the relayed codecs.
func (r *Relay) Names() []string {
	var n []string
	for _, c := range Codecs {
		n = append(n, c.Name)
	}
	return n
}

// Outputs returns the ffmpeg outputs that feed the relay.
func (r *Relay) Outputs() []source.Output {
	var out []source.Output
	for _, c := range Codecs {
		st := r.streams[c.Name]
		out = append(out, source.Output{Args: c.Args, Handle: func(rd io.Reader, base int64) {
			st.run(rd, float64(base)/float64(r.sampleRate))
		}})
	}
	return out
}

// run splits one ffmpeg run's output into segments.
func (s *stream) run(rd io.Reader, offset float64) {
	var timescale float64
	err := Splitter{
		OnInit: func(init []byte, ts uint32) {
			timescale = float64(ts)
			s.mu.Lock()
			s.session++
			s.recent = nil
			s.init = encode(Header{Kind: "init", Codec: s.codec.Name, MIME: s.codec.MIME, Session: s.session, Offset: offset, Pos: offset}, init)
			s.broadcastLocked(s.init)
			s.mu.Unlock()
		},
		OnFragment: func(frag []byte, base uint64) {
			pos := offset
			if timescale > 0 {
				pos += float64(base) / timescale
			}
			s.mu.Lock()
			seg := encode(Header{Kind: "fragment", Codec: s.codec.Name, Session: s.session, Offset: offset, Pos: pos}, frag)
			s.recent = append(s.recent, seg)
			for len(s.recent) > 1 && pos-s.recent[0].hdr.Pos > backlog.Seconds() {
				s.recent = s.recent[1:]
			}
			s.broadcastLocked(seg)
			s.mu.Unlock()
		},
	}.Run(rd)
	if err != nil {
		log.Printf("relay %s: %v", s.codec.Name, err)
	}
}

func (s *stream) broadcastLocked(seg *segment) {
	for ch := range s.clients {
		select {
		case ch <- seg.data:
		default: // a slow listener skips a fragment rather than stalling the rest
		}
	}
}

func encode(h Header, payload []byte) *segment {
	j, _ := json.Marshal(h)
	b := make([]byte, 4+len(j)+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(j)))
	copy(b[4:], j)
	copy(b[4+len(j):], payload)
	return &segment{hdr: h, data: b}
}

// Routes registers GET /ws/audio?codec=aac|opus.
func (r *Relay) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /ws/audio", r.serve)
}

func (r *Relay) serve(w http.ResponseWriter, req *http.Request) {
	st, ok := r.streams[req.URL.Query().Get("codec")]
	if !ok {
		http.Error(w, "unknown codec", http.StatusBadRequest)
		return
	}
	r.mu.Lock()
	if r.listeners >= maxClients {
		r.mu.Unlock()
		http.Error(w, "too many listeners", http.StatusServiceUnavailable)
		return
	}
	r.listeners++
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.listeners--; r.mu.Unlock() }()

	conn, err := websocket.Accept(w, req, &websocket.AcceptOptions{OriginPatterns: r.origins})
	if err != nil {
		log.Printf("ws audio: accept from %s: %v", req.RemoteAddr, err)
		return
	}
	ch := make(chan []byte, clientBuffer)
	st.mu.Lock()
	if st.init != nil {
		ch <- st.init.data
		for _, seg := range st.recent {
			ch <- seg.data
		}
	}
	st.clients[ch] = struct{}{}
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		delete(st.clients, ch)
		st.mu.Unlock()
	}()

	ctx := conn.CloseRead(req.Context())
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return
		case data := <-ch:
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Write(wctx, websocket.MessageBinary, data)
			cancel()
			if err != nil {
				conn.Close(websocket.StatusGoingAway, "write failed")
				return
			}
		}
	}
}
