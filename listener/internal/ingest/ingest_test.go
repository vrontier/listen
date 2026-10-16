package ingest

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vrontier/listen/listener/internal/fetch"
)

func needFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
}

// encode makes seconds of a tone with noise in the given format.
func encode(t *testing.T, path string, seconds int, args ...string) {
	t.Helper()
	a := append([]string{"-v", "error", "-y", "-f", "lavfi", "-i",
		"sine=frequency=440:sample_rate=22050:duration=" + strconv.Itoa(seconds)}, args...)
	a = append(a, path)
	if out, err := exec.Command("ffmpeg", a...).CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot make %s here: %v %s", filepath.Base(path), err, out)
	}
}

// throttled serves files at about rate bytes per second, as a station
// sends in real time.
func throttled(t *testing.T, dir string, rate int) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join(dir, filepath.Base(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		fl := w.(http.Flusher)
		step := rate / 20
		for i := 0; i < len(b); i += step {
			j := min(i+step, len(b))
			if _, err := w.Write(b[i:j]); err != nil {
				return
			}
			fl.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// startIngest serves an ingest on a Unix socket that allows the test server.
func startIngest(t *testing.T, allow string) (*Client, string) {
	sockDir, err := os.MkdirTemp("/tmp", "ing")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	in := New(Options{Policy: fetch.NewPolicy(false, false, allow), IdleAfter: 2 * time.Second})
	srv := &http.Server{Handler: in.Handler()}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close(); in.Close() })
	return NewClient(sock), sock
}

// decode decodes bytes with ffmpeg and returns the seconds of audio it got
// and what it complained about.
func decode(t *testing.T, data []byte, format string) (float64, string) {
	args := []string{"-v", "error"}
	if format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, "-i", "pipe:0", "-ac", "1", "-ar", "8000", "-f", "f32le", "pipe:1")
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(data)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.Run()
	return float64(out.Len()/4) / 8000, errb.String()
}

func TestLateJoinDecodes(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	cases := []struct {
		name, file string
		args       []string
		head       string // what a late consumer's bytes must start with
	}{
		{"ogg vorbis", "a.ogg", []string{"-c:a", "vorbis", "-strict", "-2", "-ac", "2"}, "OggS"},
		{"ogg opus", "a.opus", []string{"-c:a", "libopus", "-b:a", "64k", "-f", "ogg"}, "OggS"},
		{"ogg flac", "f.oga", []string{"-c:a", "flac", "-f", "ogg"}, "OggS"},
		{"raw flac", "a.flac", []string{"-c:a", "flac"}, "fLaC"},
		{"mp3", "a.mp3", []string{"-c:a", "libmp3lame", "-b:a", "64k"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(dir, c.file)
			encode(t, path, 12, c.args...)
			st, _ := os.Stat(path)
			srv := throttled(t, dir, int(st.Size()/12)) // about real time
			host := strings.TrimPrefix(srv.URL, "http://")
			cl, _ := startIngest(t, host)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			url := srv.URL + "/" + c.file

			// A listens from the start and keeps the connection up.
			a, format, _, err := cl.Open(ctx, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			go io.Copy(io.Discard, a)

			// B joins later, in the middle of the stream.
			time.Sleep(1500 * time.Millisecond)
			var n atomic.Int64
			b, _, _, err := cl.Open(ctx, url, &n)
			if err != nil {
				t.Fatal(err)
			}
			got := make(chan []byte)
			go func() {
				data, _ := io.ReadAll(io.LimitReader(b, int64(st.Size())))
				got <- data
			}()
			time.Sleep(3 * time.Second)
			b.Close()
			data := <-got
			if c.head != "" && !bytes.HasPrefix(data, []byte(c.head)) {
				t.Fatalf("a late consumer's bytes start with %q", data[:min(8, len(data))])
			}
			if c.head == "OggS" && data[5]&oggBOS == 0 {
				t.Errorf("the first page is not a stream's first page")
			}
			secs, complaints := decode(t, data, format)
			if secs < 1 {
				t.Errorf("decoded %.2f s of what a late consumer got (%d bytes); ffmpeg: %s", secs, len(data), complaints)
			}
			if strings.Contains(strings.ToLower(complaints), "header") {
				t.Errorf("ffmpeg complains about headers: %s", complaints)
			}
		})
	}
}

func TestChainedOggJoinGetsTheNewHeaders(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	one, two := filepath.Join(dir, "1.opus"), filepath.Join(dir, "2.opus")
	encode(t, one, 2, "-c:a", "libopus", "-f", "ogg")
	encode(t, two, 10, "-c:a", "libopus", "-f", "ogg")
	b1, _ := os.ReadFile(one)
	b2, _ := os.ReadFile(two)
	os.WriteFile(filepath.Join(dir, "chain.opus"), append(b1, b2...), 0o644) // a chain of two
	srv := throttled(t, dir, (len(b1)+len(b2))/12)
	cl, _ := startIngest(t, strings.TrimPrefix(srv.URL, "http://"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	url := srv.URL + "/chain.opus"
	a, _, _, err := cl.Open(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	go io.Copy(io.Discard, a)
	time.Sleep(4 * time.Second) // well into the second chain
	b, format, _, err := cl.Open(ctx, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan []byte)
	go func() { d, _ := io.ReadAll(b); got <- d }()
	time.Sleep(2 * time.Second)
	b.Close()
	data := <-got
	// The headers sent first must be the second chain's: its serial.
	serial2 := binary.LittleEndian.Uint32(b2[14:18])
	if len(data) < 27 || binary.LittleEndian.Uint32(data[14:18]) != serial2 {
		t.Fatalf("a late consumer got the first chain's headers")
	}
	if secs, complaints := decode(t, data, format); secs < 1 {
		t.Errorf("decoded %.2f s: %s", secs, complaints)
	}
}

func TestRefusalsAndUnavailable(t *testing.T) {
	cl, sock := startIngest(t, "127.0.0.1:1") // allows nothing that runs
	ctx := context.Background()
	_, _, _, err := cl.Open(ctx, "http://127.0.0.1:9/x.ogg", nil)
	var oe *fetch.OpenError
	if !errors.As(err, &oe) || !oe.Refused || oe.P.Code != "address_not_allowed" {
		t.Errorf("a private address: %v", err)
	}
	_, _, _, err = cl.Open(ctx, "file:///etc/passwd", nil)
	if !errors.As(err, &oe) || !oe.Refused || oe.P.Code != "protocol_not_allowed" {
		t.Errorf("a file: %v", err)
	}
	os.Remove(sock)
	// A new client: the old one keeps its connection to the ingest. No
	// network is reached either way: the socket is gone.
	_, _, _, err = NewClient(sock).Open(ctx, "http://example.org/x.ogg", nil)
	if !errors.As(err, &oe) || oe.Refused || oe.P.Code != "ingest_unavailable" {
		t.Errorf("no ingest: %v", err)
	}
}

func TestOneConnectionForTwoConsumers(t *testing.T) {
	needFFmpeg(t)
	dir := t.TempDir()
	encode(t, filepath.Join(dir, "a.mp3"), 10, "-c:a", "libmp3lame", "-b:a", "64k")
	var conns atomic.Int64
	inner := throttled(t, dir, 8000)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns.Add(1)
		http.Redirect(w, r, inner.URL+r.URL.Path, http.StatusFound)
	}))
	defer front.Close()
	host := strings.TrimPrefix(front.URL, "http://")
	sockDir, _ := os.MkdirTemp("/tmp", "ing")
	defer os.RemoveAll(sockDir)
	sock := filepath.Join(sockDir, "s")
	ln, _ := net.Listen("unix", sock)
	in := New(Options{Policy: fetch.NewPolicy(false, false, host, strings.TrimPrefix(inner.URL, "http://")), IdleAfter: time.Second})
	srv := &http.Server{Handler: in.Handler()}
	go srv.Serve(ln)
	defer func() { srv.Close(); in.Close() }()
	cl := NewClient(sock)
	ctx := context.Background()
	a, _, _, err := cl.Open(ctx, front.URL+"/a.mp3", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := cl.Open(ctx, front.URL+"/a.mp3", nil)
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, a)
	go io.Copy(io.Discard, b)
	time.Sleep(500 * time.Millisecond)
	if n := conns.Load(); n != 1 {
		t.Errorf("%d connections for two consumers", n)
	}
	a.Close()
	b.Close()
}
