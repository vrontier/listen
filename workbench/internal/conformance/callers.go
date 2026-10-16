package conformance

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/vrontier/listen-workbench/internal/engine"
)

// The checks of callers and shared sources (control-api.md; decided
// 2026-10-16): one engine, one socket and namespace per caller, and each
// address read once whatever the number of callers using it. They run when
// Options.Second reaches the same engine as another caller.

// putSecond makes a stream through the second caller and notes it.
func (r *run) putSecond(id string, spec engine.StreamSpec) (engine.Stream, error) {
	s, err := r.opt.Second.PutStream(r.ctx, id, spec)
	if err == nil && !slices.Contains(r.second, id) {
		r.second = append(r.second, id)
	}
	return s, err
}

// sharedInput is an address both callers use: the live input if given
// (so that sharing can be heard), else one that never resolves, which the
// engine accepts without connecting while nothing asks for it.
func (r *run) sharedInput() string {
	if r.opt.Input != "" {
		return r.input(70) // a number no other check uses
	}
	return "https://" + r.opt.Prefix + "-callers.invalid/stream"
}

func (r *run) callers() {
	other := 32000
	if r.opt.SampleRate == 32000 {
		other = 22050
	}
	a1, a2 := r.id("x", 1), r.id("y", 1)

	r.check(2, "callers: limits hold for the whole engine, counts per caller", func() (string, error) {
		i2, err := r.opt.Second.Info(r.ctx)
		if err != nil {
			return "", err
		}
		i1, err := r.c.Info(r.ctx)
		if err != nil {
			return "", err
		}
		if i1.LimitsScope != "engine" || i2.LimitsScope != "engine" {
			return "", fmt.Errorf("limits_scope %q and %q, want engine", i1.LimitsScope, i2.LimitsScope)
		}
		if i1.Instance != i2.Instance {
			return "", fmt.Errorf("two instances (%s, %s): the sockets reach different engines", i1.Instance, i2.Instance)
		}
		return fmt.Sprintf("one instance %s", i1.Instance), nil
	})

	r.check(2, "callers: each sees only its own streams", func() (string, error) {
		id := r.id("ns", 1)
		if _, err := r.putStream(id, engine.StreamSpec{Input: "https://" + r.opt.Prefix + "-ns-a.invalid/s", SampleRate: r.opt.SampleRate}); err != nil {
			return "", err
		}
		list, err := r.opt.Second.Streams(r.ctx)
		if err != nil {
			return "", err
		}
		if slices.ContainsFunc(list, func(s engine.Stream) bool { return s.ID == id }) {
			return "", fmt.Errorf("the second caller lists the first caller's stream %s", id)
		}
		// The same identifier is free in the other namespace.
		if _, err := r.putSecond(id, engine.StreamSpec{Input: "https://" + r.opt.Prefix + "-ns-b.invalid/s", SampleRate: r.opt.SampleRate}); err != nil {
			return "", fmt.Errorf("the same identifier in the second caller: %v", err)
		}
		if s, err := r.c.Stream(r.ctx, id); err != nil || !strings.Contains(s.Input, "-ns-a.") {
			return "", fmt.Errorf("the first caller's stream changed: %v %s", err, s.Input)
		}
		return "", nil
	})

	// The first caller holds the address; the second does not yet. What
	// the second may learn and is refused comes first, while it has no
	// stream of its own on the address (within a caller an address exists
	// once, and duplicate_input or exists would answer instead).
	s1, s2 := r.id("sh", 1), r.id("sh", 2)
	held := func() error {
		live := engine.StreamSpec{Input: r.sharedInput(), SampleRate: r.opt.SampleRate}
		if r.opt.Input != "" {
			live.Mode = engine.Continuous
		}
		_, err := r.putStream(s1, live)
		return err
	}

	r.check(2, "callers: another rate on a shared source is refused (409 rate_conflict)", func() (string, error) {
		if err := held(); err != nil {
			return "", err
		}
		_, err := r.putSecond(r.id("sh", 3), engine.StreamSpec{Input: r.sharedInput(), SampleRate: other})
		if err := wantErr(err, 409, "rate_conflict"); err != nil {
			return "", err
		}
		return "", nil
	})

	r.check(2, "callers: the probe of an address read already opens no connection", func() (string, error) {
		if r.opt.Input == "" {
			return "", skipped("needs -input, a stream the engine may connect to")
		}
		if err := held(); err != nil {
			return "", err
		}
		if _, err := r.c.PutAnalysis(r.ctx, s1, a1, spec(map[string]any{})); err != nil {
			return "", err
		}
		if err := r.spectrum(s1, a1, nil); err != nil {
			return "", fmt.Errorf("the first caller's stream does not run: %v", err)
		}
		res, err := r.opt.Second.Probe(r.ctx, engine.ProbeRequest{Input: r.sharedInput()})
		if err != nil {
			return "", err
		}
		if !res.OK || !res.Shared || res.SampleRate != r.opt.SampleRate {
			return "", fmt.Errorf("ok %v, shared %v, sample_rate %d; want ok, shared, %d", res.OK, res.Shared, res.SampleRate, r.opt.SampleRate)
		}
		return "", nil
	})

	r.check(2, "callers: one address in two callers is one source", func() (string, error) {
		if err := held(); err != nil {
			return "", err
		}
		spec2 := engine.StreamSpec{Input: r.sharedInput(), SampleRate: r.opt.SampleRate}
		if r.opt.Input != "" {
			spec2.Mode = engine.Continuous
		}
		st2, err := r.putSecond(s2, spec2)
		if err != nil {
			return "", fmt.Errorf("the same address in the second caller: %v (duplicate_input holds within a caller only)", err)
		}
		st1, err := r.c.Stream(r.ctx, s1)
		if err != nil {
			return "", err
		}
		if st1.Shared != 2 || st2.Shared != 2 {
			return "", fmt.Errorf("shared %d and %d, want 2", st1.Shared, st2.Shared)
		}
		if st1.Source == "" {
			return "", fmt.Errorf("no source in the stream's response")
		}
		return fmt.Sprintf("shared 2, source %s", st1.Source), nil
	})

	r.check(2, "callers: one connection feeds both callers' analyses", func() (string, error) {
		if r.opt.Input == "" {
			return "", skipped("needs -input, a stream the engine may connect to")
		}
		if _, err := r.opt.Second.PutAnalysis(r.ctx, s2, a2, spec(map[string]any{})); err != nil {
			return "", err
		}
		if err := r.spectrum(s1, a1, nil); err != nil {
			return "", fmt.Errorf("first caller: %v", err)
		}
		if err := r.await(func(ctx context.Context, fn func(engine.SSE)) error {
			return r.opt.Second.Subscribe(ctx, s2, a2, fn, "signal.spectrum")
		}, func(ev engine.SSE) bool { return ev.Event == "signal.spectrum" }); err != nil {
			return "", fmt.Errorf("second caller: %v", err)
		}
		// Usage is averaged over 10 seconds: wait for the first values.
		var st engine.Stream
		for deadline := time.Now().Add(r.opt.Timeout); ; time.Sleep(time.Second) {
			var err error
			if st, err = r.c.Stream(r.ctx, s1); err != nil {
				return "", err
			}
			if st.Usage.Shared == 2 || time.Now().After(deadline) {
				break
			}
		}
		if st.Usage.Shared != 2 {
			return "", fmt.Errorf("usage.shared %d after %s, want 2", st.Usage.Shared, r.opt.Timeout)
		}
		if st.Usage.SourceCPUPercent < st.Usage.CPUPercent {
			return "", fmt.Errorf("the source's full cost %.2f %% is below the stream's share %.2f %%", st.Usage.SourceCPUPercent, st.Usage.CPUPercent)
		}
		return fmt.Sprintf("share %.1f of %.1f MB, %.2f of %.2f %% CPU", st.Usage.MemoryMB, st.Usage.SourceMemoryMB, st.Usage.CPUPercent, st.Usage.SourceCPUPercent), nil
	})

	r.check(2, "callers: deleting in one caller leaves the other's stream", func() (string, error) {
		if err := r.c.DeleteStream(r.ctx, r.id("sh", 1), true); err != nil {
			return "", err
		}
		st, err := r.opt.Second.Stream(r.ctx, r.id("sh", 2))
		if err != nil {
			return "", fmt.Errorf("the second caller's stream is gone: %v", err)
		}
		if st.Shared != 1 {
			return "", fmt.Errorf("shared %d after the other left, want 1", st.Shared)
		}
		return "", nil
	})
}
