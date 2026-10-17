package standin

import (
	"fmt"
	"hash/fnv"
	"math"
	"net/http"
	"strconv"
)

// The analysis window of the listener has 4096 points; its defaults are
// scaled from 22 050 Hz (see the listener's internal/analysis).
const (
	fftSize      = 4096
	defaultRate  = 22050
	peakMinFloor = 40.0
)

type fieldError struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type settingsIn struct {
	MinHz         *float64 `json:"min_hz"`
	MaxHz         *float64 `json:"max_hz"`
	TransientK    *float64 `json:"transient_k"`
	TransientGapS *float64 `json:"transient_gap_s"`
}

type ruleIn struct {
	Kind         string   `json:"kind"`
	Hz           float64  `json:"hz"`
	Odd          bool     `json:"odd"`
	TolHz        *float64 `json:"tol_hz"`
	DriftPercent *float64 `json:"drift_percent"`
}

type memoryIn struct {
	Mode         string `json:"mode"`
	MotifReturnS *int   `json:"motif_return_s"`
	HistoryDays  *int   `json:"history_days"`
}

type specIn struct {
	Settings settingsIn `json:"settings"`
	Rules    []ruleIn   `json:"rules"`
	Memory   memoryIn   `json:"memory"`
	Label    string     `json:"label"`
}

type validateIn struct {
	SampleRate int `json:"sample_rate"`
	specIn
}

// rule is a checked filter rule.
type rule struct {
	Kind         string  `json:"kind"`
	Hz           float64 `json:"hz"`
	Odd          bool    `json:"odd,omitempty"`
	TolHz        float64 `json:"tol_hz,omitempty"`
	DriftPercent float64 `json:"drift_percent,omitempty"`
	Text         string  `json:"text"`
}

func (r rule) text() string {
	s := r.Kind + "=" + strconv.FormatFloat(r.Hz, 'g', -1, 64)
	if r.Odd {
		s += ",odd"
	}
	if r.TolHz > 0 {
		s += ",tol=" + strconv.FormatFloat(r.TolHz, 'g', -1, 64)
	}
	if r.DriftPercent > 0 {
		s += ",drift=" + strconv.FormatFloat(r.DriftPercent, 'g', -1, 64)
	}
	return s
}

// effective is what the analysis really works with.
type effective struct {
	SampleRate   int     `json:"sample_rate"`
	BinHz        float64 `json:"bin_hz"`
	MinHz        float64 `json:"min_hz"`
	MaxHz        float64 `json:"max_hz"`
	PeakMinHz    float64 `json:"peak_min_hz"`
	PeakMaxHz    float64 `json:"peak_max_hz"`
	DefaultTolHz float64 `json:"default_tol_hz"`
}

// checked is an analysis's settings after validation, with defaults.
type checked struct {
	settings     map[string]float64 // as given, for the response
	transientK   float64
	transientGap float64
	rules        []rule
	memMode      string
	motifReturn  int
	historyDays  int
	label        string
	eff          effective
}

// memoryKey names the memory of a range and a set of rules.
func (c checked) memoryKey() string {
	h := fnv.New32a()
	for _, r := range c.rules {
		h.Write([]byte(r.text() + ";"))
	}
	return fmt.Sprintf("r%g-%g-i%08x", c.eff.MinHz, c.eff.MaxHz, h.Sum32())
}

func check(rate int, in specIn) (checked, []fieldError, []fieldError) {
	var errs, warns []fieldError
	bad := func(path, code, msg string) { errs = append(errs, fieldError{path, code, msg}) }
	c := checked{settings: map[string]float64{}, transientK: 6, transientGap: 0.3, memMode: "disk", motifReturn: 180, historyDays: 30, label: in.Label}

	if rate < 8000 || rate > limits["max_sample_rate"] {
		bad("sample_rate", "out_of_range", fmt.Sprintf("must be between 8000 and %d", limits["max_sample_rate"]))
		rate = defaultRate
	}
	scale := float64(rate) / defaultRate
	bin := float64(rate) / fftSize
	c.eff = effective{
		SampleRate: rate, BinHz: round(bin, 2), MinHz: 20, MaxHz: math.Round(11000 * scale),
		PeakMinHz: peakMinFloor, PeakMaxHz: math.Round(6000 * scale), DefaultTolHz: round(1.5*bin, 1),
	}
	top := c.eff.MaxHz
	if v := in.Settings.MaxHz; v != nil {
		c.settings["max_hz"] = *v
		switch {
		case *v <= 0:
			bad("settings.max_hz", "out_of_range", "must be above 0")
		case *v > top:
			bad("settings.max_hz", "out_of_range", fmt.Sprintf("must be at most %g, just below half the sample rate", top))
		default:
			c.eff.MaxHz, c.eff.PeakMaxHz = *v, *v
		}
	}
	if v := in.Settings.MinHz; v != nil {
		c.settings["min_hz"] = *v
		switch {
		case *v <= 0:
			bad("settings.min_hz", "out_of_range", "must be above 0")
		case *v >= c.eff.MaxHz/2:
			bad("settings.min_hz", "out_of_range", fmt.Sprintf("must be below half of max_hz (%g)", c.eff.MaxHz/2))
		default:
			c.eff.MinHz = *v
			c.eff.PeakMinHz = math.Max(peakMinFloor, *v)
		}
	}
	if v := in.Settings.TransientK; v != nil {
		c.settings["transient_k"] = *v
		if *v < 1 || *v > 50 {
			bad("settings.transient_k", "out_of_range", "must be between 1 and 50")
		} else {
			c.transientK = *v
		}
	}
	if v := in.Settings.TransientGapS; v != nil {
		c.settings["transient_gap_s"] = *v
		if *v < 0.05 || *v > 60 {
			bad("settings.transient_gap_s", "out_of_range", "must be between 0.05 and 60 seconds")
		} else {
			c.transientGap = *v
		}
	}
	if len(in.Rules) > limits["rules_per_analysis"] {
		bad("rules", "limit_reached", fmt.Sprintf("at most %d rules", limits["rules_per_analysis"]))
	}
	for i, r := range in.Rules {
		p := fmt.Sprintf("rules[%d]", i)
		out := rule{Kind: r.Kind, Hz: r.Hz, Odd: r.Odd}
		ok := true
		if r.Kind != "line" && r.Kind != "comb" {
			bad(p+".kind", "invalid", "must be line or comb")
			ok = false
		}
		if !(r.Hz > 0) || math.IsInf(r.Hz, 0) {
			bad(p+".hz", "out_of_range", "must be above 0")
			ok = false
		}
		if r.Odd && r.Kind != "comb" {
			bad(p+".odd", "not_applicable", "only a comb can be limited to odd harmonics")
			ok = false
		}
		if r.TolHz != nil {
			if !(*r.TolHz > 0) {
				bad(p+".tol_hz", "out_of_range", "must be above 0")
				ok = false
			}
			out.TolHz = *r.TolHz
		}
		if r.DriftPercent != nil {
			switch {
			case r.Kind != "comb":
				bad(p+".drift_percent", "not_applicable", "only a comb can drift")
				ok = false
			case !(*r.DriftPercent > 0 && *r.DriftPercent <= 2):
				bad(p+".drift_percent", "out_of_range", "must be above 0 and at most 2")
				ok = false
			}
			out.DriftPercent = *r.DriftPercent
		}
		if ok && (r.Hz > c.eff.PeakMaxHz || r.Kind == "line" && r.Hz < c.eff.PeakMinHz) {
			warns = append(warns, fieldError{p + ".hz", "outside_range",
				fmt.Sprintf("%g Hz lies outside the range in which peaks are searched (%g to %g Hz); the rule has no effect", r.Hz, c.eff.PeakMinHz, c.eff.PeakMaxHz)})
		}
		out.Text = out.text()
		c.rules = append(c.rules, out)
	}
	switch in.Memory.Mode {
	case "":
	case "disk", "ram", "off":
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
	if len(c.label) > 63 {
		bad("label", "out_of_range", "at most 63 characters")
	}
	return c, errs, warns
}

func (e *Engine) validate(w http.ResponseWriter, r *http.Request) {
	var in validateIn
	if err := decode(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	c, errs, warns := check(in.SampleRate, in.specIn)
	if errs == nil {
		errs = []fieldError{}
	}
	if warns == nil {
		warns = []fieldError{}
	}
	out := map[string]any{"valid": len(errs) == 0, "errors": errs, "warnings": warns}
	if len(errs) == 0 {
		out["effective"] = c.eff
		rules := c.rules
		if rules == nil {
			rules = []rule{}
		}
		out["rules"] = rules
	}
	writeJSON(w, 200, out)
}
