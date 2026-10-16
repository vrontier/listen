package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"
)

// The engine opens a stream's HTTP connection itself and hands the bytes to
// ffmpeg. Every connection is then checked on the address actually dialled,
// after every redirect and every new lookup of the name, and the bytes that
// arrive can be counted (the bit rate of lossless streams, the incoming
// bandwidth).

// dialRefused is a dial the address policy did not allow.
type dialRefused struct{ p *Problem }

func (d *dialRefused) Error() string { return d.p.Message }

// trusted: the operator allows this host although it may be private.
func (p *addressPolicy) trusted(host, port string) bool {
	h := strings.ToLower(host)
	return p.allowPrivate || p.allowHosts[h] || p.allowHosts[net.JoinHostPort(h, port)]
}

// dial connects to addr only if every address the name resolves to is
// public and not the server's own.
func (p *addressPolicy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	if p.trusted(host, port) {
		return d.DialContext(ctx, network, addr)
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		if strings.HasSuffix(strings.ToLower(host), ".local") || strings.EqualFold(host, "localhost") {
			return nil, &dialRefused{notAllowed()}
		}
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err = p.resolve(rctx, host)
		cancel()
		if err != nil || len(addrs) == 0 {
			return nil, fmt.Errorf("%s does not resolve", host)
		}
	}
	own := p.own()
	for _, a := range addrs {
		if !public(a.Unmap()) {
			return nil, &dialRefused{notAllowed()}
		}
		for _, o := range own {
			if a.Unmap() == o.Unmap() {
				return nil, &dialRefused{notAllowed()}
			}
		}
	}
	var last error
	for _, a := range addrs {
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func (p *addressPolicy) client(version string) *http.Client {
	tr := &http.Transport{
		Proxy:                 nil, // a proxy would hide the address actually reached
		DialContext:           p.dial,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		DisableCompression:    true,
		MaxIdleConnsPerHost:   1,
		IdleConnTimeout:       30 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return &dialRefused{&Problem{Code: "protocol_not_allowed", Message: "a redirect to " + req.URL.Scheme + " is not allowed"}}
			}
			if req.URL.User != nil {
				return &dialRefused{&Problem{Code: "bad_address", Message: "a redirect to an address with a user name is not allowed"}}
			}
			return nil // the dial checks the address
		},
	}
}

// openError tells a failed connection from a refused one.
type openError struct {
	p       *Problem
	refused bool
}

func (o *openError) Error() string { return o.p.Message }

// counter counts the bytes read through it.
type counter struct {
	io.ReadCloser
	n *atomic.Int64
}

func (c counter) Read(b []byte) (int, error) {
	k, err := c.ReadCloser.Read(b)
	c.n.Add(int64(k))
	return k, err
}

// open connects to a stream address and returns its body, counting into n,
// with ffmpeg's format for its content type ("" to let ffmpeg detect it)
// and the content type as the server sent it.
func (e *Engine) open(ctx context.Context, input string, n *atomic.Int64) (io.ReadCloser, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, input, nil)
	if err != nil {
		return nil, "", "", &openError{p: &Problem{Code: "bad_address", Message: "the address cannot be read"}, refused: true}
	}
	req.Header.Set("User-Agent", "listen-engine/"+e.opts.Version)
	req.Header.Set("Accept", "audio/*, application/ogg, */*;q=0.5")
	resp, err := e.client.Do(req)
	if err != nil {
		var dr *dialRefused
		if errors.As(err, &dr) {
			return nil, "", "", &openError{p: dr.p, refused: true}
		}
		return nil, "", "", &openError{p: &Problem{Code: "no_answer", Message: "the address could not be opened: " + shortErr(err)}}
	}
	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", ct, &openError{p: &Problem{Code: "no_answer", Message: fmt.Sprintf("the server answered %s", resp.Status)}}
	}
	format, p := formatFor(ct)
	if p != nil {
		resp.Body.Close()
		return nil, "", ct, &openError{p: p}
	}
	return counter{resp.Body, n}, format, ct, nil
}

// formatFor is ffmpeg's input format for a content type, or a problem for
// content that is not a stream of audio.
func formatFor(ct string) (string, *Problem) {
	mt, _, _ := mime.ParseMediaType(ct)
	switch mt {
	case "audio/mpeg", "audio/mp3", "audio/mpeg3":
		return "mp3", nil
	case "audio/ogg", "application/ogg", "audio/opus", "audio/vorbis", "audio/x-ogg":
		return "ogg", nil
	case "audio/flac", "audio/x-flac":
		return "flac", nil
	case "audio/aac", "audio/aacp", "audio/x-aac":
		return "aac", nil
	case "audio/x-mpegurl", "audio/mpegurl", "application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/x-scpls", "application/pls+xml":
		return "", &Problem{Code: "playlist", Message: "the address is a playlist (" + mt + "); give the address of the stream it names"}
	case "text/html", "text/plain", "application/json", "application/xml", "text/xml":
		return "", &Problem{Code: "not_audio", Message: "the address delivers " + mt + ", not audio"}
	}
	return "", nil // let ffmpeg look at it
}

func shortErr(err error) string {
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 && len(s)-i < 120 {
		return s[i+2:]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
