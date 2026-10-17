package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/vrontier/listen/ingest/fetch"
)

// Client reads feeds from the ingest on this machine, over its socket.
type Client struct {
	http *http.Client
}

func NewClient(socket string) *Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableCompression: true,
	}
	return &Client{http: &http.Client{Transport: tr}}
}

// Open asks the ingest for a station and returns its bytes, counting into
// n, with ffmpeg's format and the station's content type: what fetch.Open
// returns when connecting oneself. Failures are *fetch.OpenError with the
// ingest's code; when the ingest itself cannot be reached the code is
// ingest_unavailable, not a refusal.
func (c *Client) Open(ctx context.Context, station string, n *atomic.Int64) (io.ReadCloser, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://ingest/v1/feed?url="+url.QueryEscape(station), nil)
	if err != nil {
		return nil, "", "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", "", &fetch.OpenError{P: &fetch.Problem{Code: "ingest_unavailable", Message: "the ingest on this machine cannot be reached"}}
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var e struct {
			Error *fetch.Problem `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&e) != nil || e.Error == nil {
			e.Error = &fetch.Problem{Code: "no_answer", Message: fmt.Sprintf("the ingest answered %s", resp.Status)}
		}
		return nil, "", "", &fetch.OpenError{P: e.Error, Refused: resp.StatusCode == http.StatusForbidden}
	}
	return counted{resp.Body, n}, resp.Header.Get(HeaderFormat), resp.Header.Get(HeaderContentType), nil
}

type counted struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c counted) Read(b []byte) (int, error) {
	k, err := c.ReadCloser.Read(b)
	if c.n != nil {
		c.n.Add(int64(k))
	}
	return k, err
}
