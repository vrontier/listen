// Package source decodes a file or network stream to mono float32 PCM by
// running ffmpeg, and keeps a live stream connected.
package source

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Stream states reported through OnState (§18).
const (
	Connecting   = "connecting"
	Connected    = "connected"
	Buffering    = "buffering"
	Reconnecting = "reconnecting"
	Offline      = "offline"
	Idle         = "idle" // on demand: no viewer, so no connection to the source
)

type Config struct {
	Input      string // file path or URL
	SampleRate int
	Realtime   bool // pace file input at playback speed (ignored for URLs)
	Loop       bool // repeat file input forever (ignored for URLs)
	// MaxBackoff caps the wait between reconnects to a URL that keeps
	// failing; it doubles from 1 s up to here, and resets once data flows.
	// Zero: 30 s.
	MaxBackoff time.Duration
	FFmpeg     string

	OnSamples func([]float32)
	OnState   func(state string)

	// Outputs are extra ffmpeg outputs from the same decode (e.g. encoded
	// audio for the browser). Each is written to its own pipe and must be
	// drained by Handle until EOF, or ffmpeg stalls.
	Outputs []Output
}

// Output is an additional ffmpeg output. Args are the output options
// (codec, format); the destination pipe is appended. Handle is called once
// per ffmpeg run with the pipe and the number of PCM samples delivered
// before this run started, which anchors the output's media time on the
// listener's audio timeline.
type Output struct {
	Args   []string
	Handle func(r io.Reader, baseSamples int64)
}

// Duration asks ffprobe for the length of a media file.
func Duration(ffprobe, path string) (time.Duration, error) {
	out, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	sec, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe %s: duration %q: %w", path, out, err)
	}
	return time.Duration(sec * float64(time.Second)), nil
}

// IsURL reports whether the input should be treated as a network stream.
func IsURL(in string) bool { return strings.Contains(in, "://") }

func (c Config) args() []string {
	a := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if IsURL(c.Input) {
		a = append(a,
			"-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_on_network_error", "1",
			"-reconnect_delay_max", "10", "-rw_timeout", "15000000")
	} else {
		if c.Realtime {
			a = append(a, "-re")
		}
		if c.Loop {
			a = append(a, "-stream_loop", "-1")
		}
	}
	// No explicit -map: ffmpeg picks the best audio stream for each output.
	// Some Ogg streams carry an undecodable first stream that 0:a:0 would pick.
	a = append(a, "-i", c.Input,
		"-vn", "-sn", "-dn", "-ac", "1", "-ar", strconv.Itoa(c.SampleRate), "-f", "f32le", "pipe:1")
	for i, o := range c.Outputs {
		a = append(a, "-vn", "-sn", "-dn")
		a = append(a, o.Args...)
		a = append(a, "pipe:"+strconv.Itoa(3+i)) // ExtraFiles start at fd 3
	}
	return a
}

// Run decodes until ctx is cancelled. A file without Loop returns nil at its
// end; a URL is reopened with backoff whenever ffmpeg exits.
func Run(ctx context.Context, c Config) error {
	if c.FFmpeg == "" {
		c.FFmpeg = "ffmpeg"
	}
	url := IsURL(c.Input)
	backoff := time.Second
	state := func(s string) {
		if c.OnState != nil {
			c.OnState(s)
		}
	}
	state(Connecting)
	var delivered int64
	for attempt := 0; ; attempt++ {
		got, err := c.once(ctx, state, delivered)
		delivered += got
		if ctx.Err() != nil {
			state(Offline)
			return nil
		}
		if !url {
			state(Offline)
			if err != nil {
				return err
			}
			return nil
		}
		if got > 0 {
			backoff = time.Second
		}
		log.Printf("source: stream ended after %d samples (%v); reconnecting in %s", got, err, backoff)
		state(Reconnecting)
		select {
		case <-ctx.Done():
			state(Offline)
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, c.maxBackoff())
	}
}

// once runs a single ffmpeg process and returns the number of samples read.
func (c Config) once(ctx context.Context, state func(string), base int64) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.FFmpeg, c.args()...)
	var readers []*os.File
	var drained sync.WaitGroup
	defer func() {
		for _, r := range readers {
			r.Close()
		}
	}()
	for range c.Outputs {
		r, w, err := os.Pipe()
		if err != nil {
			return 0, err
		}
		readers = append(readers, r)
		cmd.ExtraFiles = append(cmd.ExtraFiles, w)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	err = cmd.Start()
	for _, w := range cmd.ExtraFiles {
		w.Close() // ffmpeg holds its own copies
	}
	if err != nil {
		return 0, fmt.Errorf("start ffmpeg: %w", err)
	}
	for i, o := range c.Outputs {
		drained.Add(1)
		go func(r io.Reader, h func(io.Reader, int64)) {
			defer drained.Done()
			h(r, base)
			io.Copy(io.Discard, r) // keep ffmpeg unblocked if the handler stops early
		}(readers[i], o.Handle)
	}
	defer drained.Wait()
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Printf("ffmpeg: %s", sc.Text())
		}
	}()

	// Watchdog: report buffering after 3 s without data, give up after 20 s
	// so Run can reconnect.
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var stalled atomic.Bool
	go func() {
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				idle := time.Since(time.Unix(0, last.Load()))
				if idle > 3*time.Second && !stalled.Swap(true) {
					state(Buffering)
				}
				if idle > 20*time.Second {
					log.Printf("source: no data for %s, restarting ffmpeg", idle.Round(time.Second))
					cancel()
					return
				}
			}
		}
	}()

	r := bufio.NewReaderSize(stdout, 64<<10)
	buf := make([]byte, 4*2048)
	samples := make([]float32, 2048)
	var total int64
	var carry int
	for {
		n, rerr := r.Read(buf[carry:])
		n += carry
		whole := n / 4 * 4
		if whole > 0 {
			if total == 0 {
				state(Connected)
			}
			if stalled.Swap(false) {
				state(Connected)
			}
			last.Store(time.Now().UnixNano())
			count := whole / 4
			for i := 0; i < count; i++ {
				v := math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
				if v != v { // NaN guard
					v = 0
				}
				samples[i] = v
			}
			total += int64(count)
			c.OnSamples(samples[:count])
		}
		carry = copy(buf, buf[whole:n])
		if rerr != nil {
			werr := cmd.Wait()
			if errors.Is(rerr, io.EOF) {
				rerr = nil
			}
			if werr != nil && ctx.Err() == nil {
				return total, fmt.Errorf("ffmpeg: %w", werr)
			}
			return total, rerr
		}
	}
}

func (c Config) maxBackoff() time.Duration {
	if c.MaxBackoff > 0 {
		return c.MaxBackoff
	}
	return 30 * time.Second
}
