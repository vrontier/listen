package analysis

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/vrontier/listen/listener/internal/dsp"
)

// Ignore is one filter rule of a source: spectral peaks it matches are known
// interference and never become a resonance or a motif.
type Ignore struct {
	Hz    float64 // the line, or the base frequency of the comb
	Comb  bool    // every harmonic of Hz, not just Hz itself
	Odd   bool    // comb: odd harmonics only (e.g. railway traction current)
	TolHz float64 // how close a peak may sit; 0: mainsTolerance bins
	// Drift is how far the base of a comb may wander from Hz, in percent.
	// A railway's frequency moves by about 0.1 % within seconds, which is
	// several Hz at its 200th harmonic, so the base is fitted to the peaks
	// of every frame. 0: the base is fixed.
	Drift float64
}

// ParseIgnore reads a rule as written on the command line:
//
//	comb=16.7,odd,tol=4,drift=0.4   odd harmonics of 16.7 Hz, within 4 Hz,
//	                                the base followed within ±0.4 %
//	comb=50                         all harmonics of 50 Hz, default tolerance
//	line=1000,tol=5                 a single line at 1 kHz, within 5 Hz
func ParseIgnore(s string) (Ignore, error) {
	var r Ignore
	for i, part := range strings.Split(s, ",") {
		key, val, _ := strings.Cut(part, "=")
		var err error
		switch {
		case i == 0 && (key == "comb" || key == "line"):
			r.Comb = key == "comb"
			r.Hz, err = strconv.ParseFloat(val, 64)
		case i > 0 && key == "odd" && val == "" && r.Comb:
			r.Odd = true
		case i > 0 && key == "tol":
			r.TolHz, err = strconv.ParseFloat(val, 64)
			if err == nil && r.TolHz <= 0 {
				err = fmt.Errorf("must be above 0")
			}
		case i > 0 && key == "drift" && r.Comb:
			r.Drift, err = strconv.ParseFloat(val, 64)
			if err == nil && !(r.Drift > 0 && r.Drift <= 2) {
				err = fmt.Errorf("must be above 0 and at most 2 (percent)")
			}
		default:
			err = fmt.Errorf("want comb=<hz>[,odd][,tol=<hz>][,drift=<percent>] or line=<hz>[,tol=<hz>]")
		}
		if err != nil {
			return Ignore{}, fmt.Errorf("ignore rule %q: %q: %v", s, part, err)
		}
	}
	if r.Hz <= 0 || math.IsNaN(r.Hz) || math.IsInf(r.Hz, 0) || math.IsNaN(r.TolHz) || math.IsInf(r.TolHz, 0) {
		return Ignore{}, fmt.Errorf("ignore rule %q: the frequency must be above 0", s)
	}
	return r, nil
}

// String is the rule in the form ParseIgnore reads.
func (r Ignore) String() string {
	s := "line="
	if r.Comb {
		s = "comb="
	}
	s += strconv.FormatFloat(r.Hz, 'g', -1, 64)
	if r.Odd {
		s += ",odd"
	}
	if r.TolHz > 0 {
		s += ",tol=" + strconv.FormatFloat(r.TolHz, 'g', -1, 64)
	}
	if r.Drift > 0 {
		s += ",drift=" + strconv.FormatFloat(r.Drift, 'g', -1, 64)
	}
	return s
}

// offset is how far hz sits from the rule's line, or from the nearest
// harmonic of a comb with the given base.
func (r Ignore) offset(hz, base float64) float64 {
	if !r.Comb {
		return math.Abs(hz - r.Hz)
	}
	k := math.Round(hz / base)
	if r.Odd {
		k = 2*math.Round((hz/base-1)/2) + 1 // the nearest odd harmonic
	}
	if k < 1 {
		return math.Inf(1)
	}
	return math.Abs(hz - k*base)
}

const (
	driftSteps   = 40  // candidates on each side of the nominal base
	driftFitHz   = 2.0 // a peak this close to a harmonic supports a candidate
	driftMinFits = 3   // fewer supporting peaks: keep the nominal base
)

// base returns the comb's base for this frame: Hz, or with Drift the value
// near Hz whose harmonics meet the most peaks.
func (r Ignore) base(peaks []dsp.SpectralPeak) float64 {
	if !r.Comb || r.Drift <= 0 {
		return r.Hz
	}
	best, bestFits, bestSum := r.Hz, driftMinFits-1, math.Inf(1)
	for i := -driftSteps; i <= driftSteps; i++ {
		c := r.Hz * (1 + r.Drift/100*float64(i)/driftSteps)
		fits, sum := 0, 0.0
		for _, p := range peaks {
			d := r.offset(p.Hz, c)
			if d <= driftFitHz {
				fits++
			}
			sum += math.Min(d, 4*driftFitHz)
		}
		if fits > bestFits || fits == bestFits && sum < bestSum {
			best, bestFits, bestSum = c, fits, sum
		}
	}
	return best
}

// dropIgnored removes the peaks a filter rule matches, so steady hum and
// other known lines never become resonances.
func (a *Analyzer) dropIgnored(peaks []dsp.SpectralPeak) []dsp.SpectralPeak {
	if len(a.ignore) == 0 {
		return peaks
	}
	for i, r := range a.ignore {
		a.ignoreBase[i] = r.base(peaks)
	}
	defaultTol := mainsTolerance * a.binHz
	kept := peaks[:0]
next:
	for _, p := range peaks {
		for i, r := range a.ignore {
			tol := r.TolHz
			if tol <= 0 {
				tol = defaultTol
			}
			if r.offset(p.Hz, a.ignoreBase[i]) <= tol {
				continue next
			}
		}
		kept = append(kept, p)
	}
	return kept
}
