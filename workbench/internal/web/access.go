package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
)

// Every access with a token is noted (catalogue/access.go): a data stream
// request when it starts, finished with its status, duration and size when
// it ends; an MCP message when it is answered. The token itself, and any
// other header, is never noted.

// noteAccess notes one access and returns its id, or 0 when it could not
// be noted, which is logged and does not stop the request.
func (s *Server) noteAccess(a catalogue.Access) int64 {
	id, err := s.db.LogAccess(context.Background(), a)
	if err != nil {
		log.Printf("access: %v", err)
	}
	return id
}

// tokenAccess is the start of a note for a request made with a token.
func tokenAccess(r *http.Request, t catalogue.APIToken, via, what string) catalogue.Access {
	return catalogue.Access{UserID: t.UserID, TokenID: t.ID, Via: via, What: what, Client: r.UserAgent(), At: time.Now().UTC()}
}

// accessWriter counts what a data stream request sends, and keeps the
// start of an error's text.
type accessWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
	errMsg []byte
}

func (w *accessWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *accessWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status >= 400 && len(w.errMsg) < 600 {
		w.errMsg = append(w.errMsg, b[:min(len(b), 600-len(w.errMsg))]...)
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

// Flush passes on to the connection, which live events need.
func (w *accessWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *accessWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// errorText takes the message out of an apiError body.
func errorText(body []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &e) == nil {
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if e.Message != "" {
			return e.Message
		}
	}
	return string(body)
}

// noted serves a data stream request and notes it.
func (s *Server) noted(w http.ResponseWriter, r *http.Request, h http.HandlerFunc) {
	t, _ := tokenFrom(r)
	a := tokenAccess(r, t, "stream", r.Method+" "+r.URL.Path)
	a.Detail = r.URL.RawQuery
	a.Status = catalogue.AccessOpen
	id := s.noteAccess(a)
	aw := &accessWriter{ResponseWriter: w}
	start := time.Now()
	h(aw, r)
	status, note := catalogue.AccessOK, ""
	if aw.status >= 400 {
		status, note = catalogue.AccessError, strconv.Itoa(aw.status)+": "+errorText(aw.errMsg)
	}
	if id > 0 {
		if err := s.db.FinishAccess(context.Background(), id, status, note, time.Since(start).Milliseconds(), aw.bytes); err != nil {
			log.Printf("access: %v", err)
		}
	}
}

// noteRPC notes one MCP message and how it was answered.
func (s *Server) noteRPC(r *http.Request, q rpcRequest, resp rpcResponse, answered bool, start time.Time) {
	t, ok := tokenFrom(r)
	if !ok {
		return
	}
	a := tokenAccess(r, t, "mcp", q.Method)
	a.At = start.UTC()
	a.Ms = time.Since(start).Milliseconds()
	a.Status = catalogue.AccessOK
	switch q.Method {
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		json.Unmarshal(q.Params, &p)
		a.What = p.Name
		a.Detail = compactJSON(p.Arguments)
	case "resources/read", "prompts/get":
		var p struct {
			URI       string          `json:"uri"`
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		json.Unmarshal(q.Params, &p)
		if p.URI != "" {
			a.Detail = p.URI
		} else {
			a.Detail = strings.TrimSpace(p.Name + " " + compactJSON(p.Arguments))
		}
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
			ClientInfo      struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"clientInfo"`
		}
		json.Unmarshal(q.Params, &p)
		a.Detail = "protocol " + p.ProtocolVersion
		if p.ClientInfo.Name != "" {
			a.Client = p.ClientInfo.Name + " " + p.ClientInfo.Version
		}
	}
	if a.What == "" {
		a.What = "(no method)"
	}
	if answered {
		if resp.Error != nil {
			a.Status, a.Note = catalogue.AccessError, resp.Error.Message
		} else if res, ok := resp.Result.(map[string]any); ok && res["isError"] == true {
			a.Status = catalogue.AccessError
			if c, ok := res["content"].([]map[string]any); ok && len(c) > 0 {
				a.Note, _ = c[0]["text"].(string)
			}
			if strings.HasPrefix(a.Note, refused) {
				a.Status = catalogue.AccessRefused
			}
		}
		if b, err := json.Marshal(resp.Result); err == nil && resp.Result != nil {
			a.Bytes = int64(len(b))
		}
	}
	s.noteAccess(a)
}

func compactJSON(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return ""
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return string(b)
}
