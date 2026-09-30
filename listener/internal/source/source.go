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
	"os/exec"
	"strconv"
	"strings"
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
)

type Config struct {
	Input      string // file path or URL
	SampleRate int
	Realtime   bool // pace file input at playback speed (ignored for URLs)
	Loop       bool // repeat file input forever (ignored for URLs)
	FFmpeg     string

	OnSamples func([]float32)
	OnState   func(state string)
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
	return append(a, "-i", c.Input, "-vn", "-ac", "1", "-ar", strconv.Itoa(c.SampleRate), "-f", "f32le", "pipe:1")
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
	for attempt := 0; ; attempt++ {
		got, err := c.once(ctx, state)
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
		backoff = min(backoff*2, 30*time.Second)
	}
}

// once runs a single ffmpeg process and returns the number of samples read.
func (c Config) once(ctx context.Context, state func(string)) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.FFmpeg, c.args()...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start ffmpeg: %w", err)
	}
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
