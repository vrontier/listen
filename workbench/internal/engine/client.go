package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to the engine process.
type Client struct {
	http *http.Client // for requests that end
	long *http.Client // for event streams, no timeout
	base string
}

// Dial returns a client for the engine at addr: a Unix socket path, or
// "http://127.0.0.1:port" for development on one machine.
func Dial(addr string) *Client {
	if strings.HasPrefix(addr, "http://") {
		return &Client{
			http: &http.Client{Timeout: 45 * time.Second},
			long: &http.Client{},
			base: strings.TrimRight(addr, "/"),
		}
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", addr)
		},
		MaxIdleConns: 8,
	}
	return &Client{
		http: &http.Client{Transport: tr, Timeout: 45 * time.Second},
		long: &http.Client{Transport: tr},
		base: "http://engine",
	}
}

// NewClient wraps an existing HTTP client, for tests.
func NewClient(hc *http.Client, base string) *Client {
	return &Client{http: hc, long: hc, base: strings.TrimRight(base, "/")}
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	if resp.StatusCode >= 300 {
		var wrap struct {
			Error Error `json:"error"`
		}
		if json.Unmarshal(b, &wrap) != nil || wrap.Error.Code == "" {
			wrap.Error = Error{Code: "unexpected", Message: strings.TrimSpace(string(b))}
		}
		wrap.Error.Status = resp.StatusCode
		return &wrap.Error
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = b
		return nil
	}
	return json.Unmarshal(b, out)
}

// IsCode reports whether err is an engine error with the given code.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var i Info
	return i, c.do(ctx, http.MethodGet, "/v1/engine", nil, &i)
}

func (c *Client) Streams(ctx context.Context) ([]Stream, error) {
	var out struct {
		Streams []Stream `json:"streams"`
	}
	return out.Streams, c.do(ctx, http.MethodGet, "/v1/streams", nil, &out)
}

func (c *Client) Stream(ctx context.Context, id string) (Stream, error) {
	var s Stream
	return s, c.do(ctx, http.MethodGet, "/v1/streams/"+id, nil, &s)
}

func (c *Client) PutStream(ctx context.Context, id string, spec StreamSpec) (Stream, error) {
	var s Stream
	return s, c.do(ctx, http.MethodPut, "/v1/streams/"+id, spec, &s)
}

// DeleteStream stops a stream. With purge its memories go too.
func (c *Client) DeleteStream(ctx context.Context, id string, purge bool) error {
	q := ""
	if purge {
		q = "?purge=true"
	}
	return c.do(ctx, http.MethodDelete, "/v1/streams/"+id+q, nil, nil)
}

func analysisPath(stream, analysis string) string {
	return "/v1/streams/" + stream + "/analyses/" + analysis
}

func (c *Client) Analysis(ctx context.Context, stream, analysis string) (Analysis, error) {
	var a Analysis
	return a, c.do(ctx, http.MethodGet, analysisPath(stream, analysis), nil, &a)
}

func (c *Client) PutAnalysis(ctx context.Context, stream, analysis string, spec json.RawMessage) (Analysis, error) {
	var a Analysis
	return a, c.do(ctx, http.MethodPut, analysisPath(stream, analysis), spec, &a)
}

func (c *Client) DeleteAnalysis(ctx context.Context, stream, analysis string, purge bool) error {
	q := ""
	if purge {
		q = "?purge=true"
	}
	return c.do(ctx, http.MethodDelete, analysisPath(stream, analysis)+q, nil, nil)
}

// DeleteMemory deletes one memory of an analysis; the one in use cannot be.
func (c *Client) DeleteMemory(ctx context.Context, stream, analysis, key string) error {
	return c.do(ctx, http.MethodDelete, analysisPath(stream, analysis)+"/memories/"+url.PathEscape(key), nil, nil)
}

func (c *Client) Validate(ctx context.Context, req ValidateRequest) (Validation, error) {
	var v Validation
	return v, c.do(ctx, http.MethodPost, "/v1/validate/analysis", req, &v)
}

func (c *Client) Probe(ctx context.Context, req ProbeRequest) (ProbeResult, error) {
	var p ProbeResult
	return p, c.do(ctx, http.MethodPost, "/v1/probes", req, &p)
}

// Data fetches one of an analysis's data paths (state, motifs,
// history/events, …) as raw JSON, which the Workbench passes on.
func (c *Client) Data(ctx context.Context, stream, analysis, sub string, q url.Values) (json.RawMessage, error) {
	path := analysisPath(stream, analysis) + "/" + sub
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var raw json.RawMessage
	return raw, c.do(ctx, http.MethodGet, path, nil, &raw)
}

func groupPath(id string) string { return "/v1/groups/" + id }

// Groups lists the engine's groups (1.1).
func (c *Client) Groups(ctx context.Context) ([]Group, error) {
	var out struct {
		Groups []Group `json:"groups"`
	}
	return out.Groups, c.do(ctx, http.MethodGet, "/v1/groups", nil, &out)
}

func (c *Client) PutGroup(ctx context.Context, id string, spec GroupSpec) (Group, error) {
	var g Group
	return g, c.do(ctx, http.MethodPut, groupPath(id), spec, &g)
}

func (c *Client) DeleteGroup(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, groupPath(id), nil, nil)
}

// GroupData fetches one of a group's data paths (state, history/events)
// as raw JSON.
func (c *Client) GroupData(ctx context.Context, id, sub string, q url.Values) (json.RawMessage, error) {
	path := groupPath(id) + "/" + sub
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	var raw json.RawMessage
	return raw, c.do(ctx, http.MethodGet, path, nil, &raw)
}

// SubscribeGroup opens a group's live events.
func (c *Client) SubscribeGroup(ctx context.Context, id string, fn func(SSE), types ...string) error {
	return c.follow(ctx, groupPath(id)+"/events"+typesQuery(types), fn)
}

// SSE is one event read from an event stream.
type SSE struct {
	ID    string
	Event string
	Data  []byte
}

// Subscribe opens an analysis's live events. Each event is handed to fn
// until ctx ends or the engine closes the stream. Types, if given, limit
// the events to those types.
func (c *Client) Subscribe(ctx context.Context, stream, analysis string, fn func(SSE), types ...string) error {
	return c.follow(ctx, analysisPath(stream, analysis)+"/events"+typesQuery(types), fn)
}

func typesQuery(types []string) string {
	if len(types) == 0 {
		return ""
	}
	return "?types=" + url.QueryEscape(strings.Join(types, ","))
}

// Changes follows GET /v1/events.
func (c *Client) Changes(ctx context.Context, fn func(SSE)) error {
	return c.follow(ctx, "/v1/events", fn)
}

func (c *Client) follow(ctx context.Context, path string, fn func(SSE)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.long.Do(req)
	if err != nil {
		return fmt.Errorf("engine: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var wrap struct {
			Error Error `json:"error"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		if json.Unmarshal(b, &wrap) != nil || wrap.Error.Code == "" {
			wrap.Error = Error{Code: "unexpected", Message: strings.TrimSpace(string(b))}
		}
		wrap.Error.Status = resp.StatusCode
		return &wrap.Error
	}
	return ReadSSE(resp.Body, fn)
}

// ReadSSE parses a text/event-stream until EOF.
func ReadSSE(r io.Reader, fn func(SSE)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	var ev SSE
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if len(data) > 0 {
				ev.Data = []byte(strings.Join(data, "\n"))
				fn(ev)
			}
			ev, data = SSE{}, nil
		case strings.HasPrefix(line, ":"):
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "id":
				ev.ID = value
			case "event":
				ev.Event = value
			case "data":
				data = append(data, value)
			}
		}
	}
	return sc.Err()
}
