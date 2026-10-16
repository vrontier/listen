package engine

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// probeGate allows one probe per address and a few at a time overall.
type probeGate struct {
	mu   sync.Mutex
	busy map[string]bool
}

const maxProbes = 3

func newProbeGate() *probeGate { return &probeGate{busy: map[string]bool{}} }

func (g *probeGate) enter(input string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.busy[input] || len(g.busy) >= maxProbes {
		return false
	}
	g.busy[input] = true
	return true
}

func (g *probeGate) leave(input string) {
	g.mu.Lock()
	delete(g.busy, input)
	g.mu.Unlock()
}

// probeRate is the rate the probe decodes at to measure the level; low,
// since only the level matters.
const probeRate = 8000

// silentDB: below this level only silence arrived.
const silentDB = -70.0

var (
	// Stream #0:0: Audio: vorbis, 32000 Hz, mono, fltp, 96 kb/s
	audioRE   = regexp.MustCompile(`Stream #\d+:\d+.*?: Audio: ([A-Za-z0-9_]+)[^,]*, (\d+) Hz, ([^,]+)(?:, [^,]+)?(?:, (\d+) kb/s)?`)
	bitrateRE = regexp.MustCompile(`Duration: .*bitrate: (\d+) kb/s`)
)

// probe connects to input once, listens for listen seconds and reports what
// arrives. One connection: the codec and rate come from ffmpeg's own
// report of the input, the level from the decoded samples.
func (c *caller) probe(ctx context.Context, req ProbeRequest) (ProbeResult, *apiError) {
	res := ProbeResult{Problems: []Problem{}}
	listen := req.ListenS
	if listen == 0 {
		listen = 10
	}
	if listen < 1 || listen > 30 {
		return res, &apiError{status: 422, code: "invalid", message: "listen_s must be between 1 and 30",
			errors: []FieldError{{"listen_s", "out_of_range", "must be between 1 and 30"}}}
	}
	c.mu.Lock()
	for _, s := range c.streams {
		if s.input == req.Input {
			id := s.id
			res.Exists = &id
			res.Problems = append(res.Problems, Problem{Code: "exists", Message: "this address is already stream " + id})
		}
	}
	c.mu.Unlock()
	if res.Exists != nil {
		return res, nil
	}
	// Read already, for another caller: no connection; the answer comes
	// from the running source.
	if src := c.e.source(req.Input); src != nil {
		res.OK, res.Shared, res.SampleRate = true, true, src.rate
		return res, nil
	}
	if p, _ := c.e.policy.check(ctx, req.Input); p != nil {
		res.Problems = append(res.Problems, *p)
		return res, nil
	}
	if !c.e.probes.enter(req.Input) {
		return res, &apiError{status: 429, code: "busy", message: "too many probes at once; try again shortly"}
	}
	defer c.e.probes.leave(req.Input)

	ctx, cancel := context.WithTimeout(ctx, time.Duration(listen+15)*time.Second)
	defer cancel()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "info"}
	if isURL(req.Input) {
		args = append(args, "-protocol_whitelist", protocolWhitelist, "-rw_timeout", "10000000")
	}
	args = append(args, "-t", strconv.Itoa(listen), "-i", req.Input,
		"-vn", "-sn", "-dn", "-ac", "1", "-ar", strconv.Itoa(probeRate), "-f", "f32le", "pipe:1")
	cmd := exec.CommandContext(ctx, c.e.opts.FFmpeg, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return res, &apiError{status: 500, code: "internal", message: err.Error()}
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return res, &apiError{status: 500, code: "internal", message: err.Error()}
	}
	if err := cmd.Start(); err != nil {
		return res, &apiError{status: 500, code: "internal", message: "ffmpeg: " + err.Error()}
	}
	var log []string
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if len(log) < 200 {
				log = append(log, sc.Text())
			}
		}
	}()
	var sum float64
	var n int64
	buf := make([]byte, 4*1024)
	r := bufio.NewReader(stdout)
	for {
		k, err := io.ReadFull(r, buf)
		for i := 0; i+4 <= k; i += 4 {
			v := float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[i:])))
			if v == v {
				sum += v * v
				n++
			}
		}
		if err != nil {
			break
		}
	}
	<-logDone
	werr := cmd.Wait()

	describeInput(&res, log)
	switch {
	case n > 0:
		rms := math.Sqrt(sum / float64(n))
		res.LevelDB = round(20*math.Log10(math.Max(rms, 1e-10)), 1)
		res.Silent = res.LevelDB < silentDB
	case ctx.Err() != nil:
		res.Problems = append(res.Problems, Problem{Code: "no_answer", Message: fmt.Sprintf("nothing arrived within %d seconds", listen+15)})
	case res.Codec == "" && werr != nil:
		res.Problems = append(res.Problems, Problem{Code: "no_answer", Message: "the address could not be opened: " + lastLine(log)})
	default:
		res.Problems = append(res.Problems, Problem{Code: "cannot_decode", Message: "no audio could be decoded: " + lastLine(log)})
	}
	if n > 0 && res.Codec == "" {
		res.Problems = append(res.Problems, Problem{Code: "not_audio", Message: "the address delivers no audio stream"})
	}
	if res.BitrateKbps > c.e.opts.Limits.MaxBitrateKbps {
		res.Problems = append(res.Problems, Problem{Code: "bitrate", Message: fmt.Sprintf("%d kbit/s is above the limit of %d kbit/s", res.BitrateKbps, c.e.opts.Limits.MaxBitrateKbps)})
	}
	if res.Silent {
		res.Problems = append(res.Problems, Problem{Code: "silent", Message: fmt.Sprintf("only silence arrived in %d seconds", listen)})
	}
	res.OK = len(res.Problems) == 0
	return res, nil
}

// describeInput reads the codec, rate, channels and bit rate from ffmpeg's
// report of its input.
func describeInput(res *ProbeResult, log []string) {
	for _, line := range log {
		line = strings.TrimSpace(line)
		if m := audioRE.FindStringSubmatch(line); m != nil && res.Codec == "" {
			res.Codec = m[1]
			res.SampleRate, _ = strconv.Atoi(m[2])
			res.Channels = channels(m[3])
			if m[4] != "" {
				res.BitrateKbps, _ = strconv.Atoi(m[4])
			}
		}
		if m := bitrateRE.FindStringSubmatch(line); m != nil && res.BitrateKbps == 0 {
			res.BitrateKbps, _ = strconv.Atoi(m[1])
		}
	}
}

func channels(s string) int {
	s = strings.TrimSpace(s)
	switch {
	case s == "mono":
		return 1
	case s == "stereo":
		return 2
	case strings.HasSuffix(s, " channels"):
		n, _ := strconv.Atoi(strings.TrimSuffix(s, " channels"))
		return n
	case strings.HasPrefix(s, "5.1"):
		return 6
	}
	return 0
}

func lastLine(log []string) string {
	for i := len(log) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(log[i]); t != "" {
			if len(t) > 200 {
				t = t[:200]
			}
			return t
		}
	}
	return "no answer"
}
