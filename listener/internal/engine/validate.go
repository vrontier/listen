package engine

import (
	"fmt"
	"hash/fnv"
	"math"
	"time"

	"github.com/vrontier/listen/listener/internal/analysis"
)

// checked is an analysis's settings after validation, with defaults.
type checked struct {
	spec         AnalysisSpec // as given, rules with their text
	rng          analysis.Range
	transientK   float64
	transientGap time.Duration
	rules        []analysis.Ignore
	memMode      string
	motifReturn  int // seconds
	historyDays  int
}

func (c checked) effective() Effective {
	return Effective{
		SampleRate: c.rng.SampleRate, BinHz: round(c.rng.BinHz, 2),
		MinHz: c.rng.MinHz, MaxHz: c.rng.MaxHz, PeakMinHz: c.rng.PeakMinHz, PeakMaxHz: c.rng.PeakMaxHz,
		DefaultTolHz: round(c.rng.DefaultTolHz, 1),
	}
}

// memoryKey names the memory of a range and a set of rules: motifs learned
// with other values are not comparable.
func (c checked) memoryKey() string {
	h := fnv.New32a()
	for _, r := range c.rules {
		h.Write([]byte(r.String() + ";"))
	}
	return fmt.Sprintf("r%g-%g-i%08x", c.rng.MinHz, c.rng.MaxHz, h.Sum32())
}

func (c checked) options(view *analysis.View) analysis.Options {
	return analysis.Options{
		TransientK: c.transientK, TransientGap: c.transientGap,
		MinHz: c.rng.MinHz, MaxHz: c.rng.MaxHz, Ignore: c.rules, View: view,
	}
}

// checkAnalysis validates an analysis for a stream at rate. It refuses what
// the listener would silently correct.
func checkAnalysis(rate int, in AnalysisSpec, lim Limits) (checked, []FieldError, []FieldError) {
	var errs, warns []FieldError
	bad := func(path, code, format string, a ...any) {
		errs = append(errs, FieldError{path, code, fmt.Sprintf(format, a...)})
	}
	c := checked{
		transientK: analysis.DefaultTransientK, transientGap: analysis.DefaultTransientGap,
		memMode: MemoryDisk, motifReturn: 180, historyDays: 30,
	}
	if rate < 8000 || rate > lim.MaxSampleRate {
		bad("sample_rate", "out_of_range", "must be between 8000 and %d", lim.MaxSampleRate)
		rate = analysis.DefaultSampleRate
	}
	top := analysis.TopHz(rate)
	maxHz, minHz := 0.0, 0.0
	if v := in.Settings.MaxHz; v != nil {
		switch {
		case !(*v > 0) || math.IsInf(*v, 0):
			bad("settings.max_hz", "out_of_range", "must be above 0")
		case *v > top:
			bad("settings.max_hz", "out_of_range", "must be at most %g, just below half the sample rate", top)
		default:
			maxHz = *v
		}
	}
	effMax := top
	if maxHz > 0 {
		effMax = maxHz
	}
	if v := in.Settings.MinHz; v != nil {
		switch {
		case !(*v > 0) || math.IsInf(*v, 0):
			bad("settings.min_hz", "out_of_range", "must be above 0")
		case *v >= effMax/2:
			bad("settings.min_hz", "out_of_range", "must be below half of max_hz (%g)", effMax/2)
		default:
			minHz = *v
		}
	}
	c.rng = analysis.EffectiveRange(rate, minHz, maxHz)
	if v := in.Settings.TransientK; v != nil {
		if !(*v >= 1 && *v <= 50) {
			bad("settings.transient_k", "out_of_range", "must be between 1 and 50")
		} else {
			c.transientK = *v
		}
	}
	if v := in.Settings.TransientGapS; v != nil {
		if !(*v >= 0.05 && *v <= 60) {
			bad("settings.transient_gap_s", "out_of_range", "must be between 0.05 and 60 seconds")
		} else {
			c.transientGap = time.Duration(*v * float64(time.Second))
		}
	}
	if len(in.Rules) > lim.RulesPerAnalysis {
		bad("rules", "limit_reached", "at most %d rules", lim.RulesPerAnalysis)
	}
	out := make([]Rule, 0, len(in.Rules))
	for i, r := range in.Rules {
		p := fmt.Sprintf("rules[%d]", i)
		ok := true
		ig := analysis.Ignore{Hz: r.Hz, Comb: r.Kind == RuleComb, Odd: r.Odd}
		if r.Kind != RuleLine && r.Kind != RuleComb {
			bad(p+".kind", "invalid", "must be line or comb")
			ok = false
		}
		if !(r.Hz > 0) || math.IsInf(r.Hz, 0) {
			bad(p+".hz", "out_of_range", "must be above 0")
			ok = false
		}
		if r.Odd && r.Kind != RuleComb {
			bad(p+".odd", "not_applicable", "only a comb can be limited to odd harmonics")
			ok = false
		}
		if r.TolHz != nil {
			if !(*r.TolHz > 0) || math.IsInf(*r.TolHz, 0) {
				bad(p+".tol_hz", "out_of_range", "must be above 0")
				ok = false
			}
			ig.TolHz = *r.TolHz
		}
		if r.DriftPercent != nil {
			switch {
			case r.Kind != RuleComb:
				bad(p+".drift_percent", "not_applicable", "only a comb can drift")
				ok = false
			case !(*r.DriftPercent > 0 && *r.DriftPercent <= 2):
				bad(p+".drift_percent", "out_of_range", "must be above 0 and at most 2")
				ok = false
			}
			ig.Drift = *r.DriftPercent
		}
		if !ok {
			continue
		}
		if r.Hz > c.rng.PeakMaxHz || r.Kind == RuleLine && r.Hz < c.rng.PeakMinHz {
			warns = append(warns, FieldError{p + ".hz", "outside_range",
				fmt.Sprintf("%g Hz lies outside the range in which peaks are searched (%g to %g Hz); the rule has no effect", r.Hz, c.rng.PeakMinHz, c.rng.PeakMaxHz)})
		}
		c.rules = append(c.rules, ig)
		r.Text = ig.String()
		out = append(out, r)
	}
	switch in.Memory.Mode {
	case "":
	case MemoryDisk, MemoryRAM, MemoryOff:
		c.memMode = in.Memory.Mode
	default:
		bad("memory.mode", "invalid", "must be disk, ram or off")
	}
	if v := in.Memory.MotifReturnS; v != nil {
		if *v < 10 || *v > 86400 {
			bad("memory.motif_return_s", "out_of_range", "must be between 10 and 86400 seconds")
		} else {
			c.motifReturn = *v
		}
	}
	if v := in.Memory.HistoryDays; v != nil {
		if *v < 1 || *v > 365 {
			bad("memory.history_days", "out_of_range", "must be between 1 and 365 days")
		} else {
			c.historyDays = *v
		}
	}
	if len(in.Label) > 63 {
		bad("label", "out_of_range", "at most 63 characters")
	}
	c.spec = in
	c.spec.Rules = out
	c.spec.Memory.Key = ""
	return c, errs, warns
}

// checkView checks a stream's view against its sample rate and fills in the
// defaults: linear from 0, log from 20 Hz, up to Nyquist, 240 bands.
func checkView(v *ViewSpec, rate int) (*View, []FieldError) {
	if v == nil {
		return nil, nil
	}
	var fe []FieldError
	nyq := float64(rate) / 2
	eff := &View{Scale: v.Scale, MaxHz: nyq, Bands: 240}
	switch v.Scale {
	case "linear":
	case "log":
		eff.MinHz = 20
	default:
		fe = append(fe, FieldError{"view.scale", "invalid", "linear or log"})
	}
	if v.MinHz != nil {
		eff.MinHz = *v.MinHz
	}
	if v.MaxHz != nil {
		eff.MaxHz = *v.MaxHz
	}
	if v.Bands != nil {
		eff.Bands = *v.Bands
	}
	if !(eff.MinHz >= 0) || v.Scale == "log" && !(eff.MinHz > 0) {
		fe = append(fe, FieldError{"view.min_hz", "out_of_range", "must be at least 0 Hz, and above 0 Hz on a log scale"})
	}
	if eff.MaxHz > nyq {
		fe = append(fe, FieldError{"view.max_hz", "out_of_range", fmt.Sprintf("must be at most %g Hz, half the sample rate", nyq)})
	}
	if !(eff.MaxHz > eff.MinHz) {
		fe = append(fe, FieldError{"view.max_hz", "out_of_range", "must be above min_hz"})
	}
	if eff.Bands < 16 || eff.Bands > 1024 {
		fe = append(fe, FieldError{"view.bands", "out_of_range", "must be between 16 and 1024"})
	}
	return eff, fe
}

func (v *View) options() *analysis.View {
	if v == nil {
		return nil
	}
	return &analysis.View{Scale: v.Scale, MinHz: v.MinHz, MaxHz: v.MaxHz, Bands: v.Bands}
}

func round(x float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(x*p) / p
}
