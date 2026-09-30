// Package narrate is the language layer (event model §17, phase 3). It
// turns measured observations into a short factual evidence list and asks a
// language model to phrase it. The model never decides what happened: the
// evidence is built here, and text naming a frequency or motif that is not
// in the evidence is rejected.
package narrate

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/vrontier/listen/listener/internal/events"
)

// facts accumulates what happened since the last narration.
type facts struct {
	feature     events.Feature
	prevFeature *events.Feature // at the last narration
	resonances  map[string]events.Resonance
	notable     []string // since the last narration
	evidence    map[string]bool
	summary     *events.MemorySummary
	changed     bool
}

func newFacts() *facts {
	return &facts{resonances: map[string]events.Resonance{}, evidence: map[string]bool{}}
}

func (f *facts) observe(msg events.Message) {
	switch p := msg.Env.Payload.(type) {
	case events.Feature:
		if f.feature.State != "" && p.State != f.feature.State {
			f.notable = append(f.notable, fmt.Sprintf("The overall state changed from %s to %s.", words(f.feature.State), words(p.State)))
			f.changed = true
		}
		f.feature = p
	case events.Resonance:
		switch p.Status {
		case "end":
			delete(f.resonances, p.ID)
			if p.DurationS >= 30 {
				f.notable = append(f.notable, fmt.Sprintf("A resonance at %s faded after %s.", hz(p.FundamentalHz), dur(p.DurationS)))
				f.changed = true
			}
		case "start":
			f.resonances[p.ID] = p
			f.changed = true
		default:
			f.resonances[p.ID] = p
		}
	case events.Transient:
		kind := "A transient"
		if p.BandwidthHz > 1500 {
			kind = "A broadband transient"
		}
		f.notable = append(f.notable, fmt.Sprintf("%s occurred (strength %.2f).", kind, p.Strength))
		f.changed = true
	case events.MotifDetected:
		what := hz(p.Signature.FundamentalHz)
		if p.Kind == "texture" {
			what = "a sound texture centred near " + hz(p.Signature.CentroidHz)
		}
		f.notable = append(f.notable, fmt.Sprintf("A new recurring structure was recognised: %s at %s.", motifName(p.MotifID), what))
		f.evidence[p.MotifID] = true
		f.changed = true
	case events.MotifReturned:
		f.notable = append(f.notable, fmt.Sprintf("%s returned after %s (similarity %.2f, occurrence %d).",
			capital(motifName(p.MotifID)), dur(p.LastSeenS), p.Similarity, p.Occurrence))
		f.evidence[p.MotifID] = true
		f.changed = true
	case events.MemorySummary:
		if p.Window == "1h" {
			s := p
			f.summary = &s
		}
	}
	if len(f.notable) > 12 {
		f.notable = f.notable[len(f.notable)-12:]
	}
}

// build returns the evidence list, the ids it refers to, and the numbers
// the text may use.
func (f *facts) build() (lines []string, ids []string, allowed allowance) {
	allowed = allowance{hz: nil, motifs: map[int]bool{}}
	idset := map[string]bool{}
	for id := range f.evidence {
		idset[id] = true
	}

	// Leading resonances, strongest first.
	res := make([]events.Resonance, 0, len(f.resonances))
	for _, r := range f.resonances {
		res = append(res, r)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Strength > res[j].Strength })
	if len(res) > 3 {
		res = res[:3]
	}
	for i, r := range res {
		line := fmt.Sprintf("A resonance at %s has held for %s", hz(r.FundamentalHz), dur(r.DurationS))
		allowed.addHz(r.FundamentalHz)
		if len(r.Harmonics) > 0 {
			var hs []string
			for _, h := range r.Harmonics {
				if len(hs) == 2 {
					break
				}
				hs = append(hs, hz(h.Hz))
				allowed.addHz(h.Hz)
			}
			line += ", with harmonics at " + strings.Join(hs, " and ")
		}
		if r.MotifID != "" {
			line += fmt.Sprintf(" (it is the known %s)", motifName(r.MotifID))
			idset[r.MotifID] = true
		}
		if i == 0 {
			line = strings.Replace(line, "A resonance", "The strongest resonance", 1)
		}
		lines = append(lines, line+".")
		idset[r.ID] = true
	}
	lines = append(lines, f.notable...)

	st := fmt.Sprintf("The overall state is %s; spectral entropy %.2f, harmonicity %.2f.", words(f.feature.State), f.feature.Entropy, f.feature.Harmonicity)
	if p := f.prevFeature; p != nil {
		if d := f.feature.Entropy - p.Entropy; math.Abs(d) >= 0.05 {
			st += fmt.Sprintf(" Entropy %s from %.2f.", map[bool]string{true: "rose", false: "fell"}[d > 0], p.Entropy)
		}
	}
	lines = append(lines, st)
	if s := f.summary; s != nil && len(s.DominantMotifs) > 0 {
		var names []string
		for _, id := range s.DominantMotifs {
			names = append(names, motifName(id))
			idset[id] = true
		}
		lines = append(lines, "Over the last hour the most present structures were "+strings.Join(names, ", ")+".")
	}
	for id := range idset {
		ids = append(ids, id)
		if n, ok := motifNumber(id); ok {
			allowed.motifs[n] = true
		}
	}
	sort.Strings(ids)
	for _, l := range lines {
		for _, v := range hzIn(l) {
			allowed.addHz(v)
		}
	}
	return lines, ids, allowed
}

// reset starts a new narration period.
func (f *facts) reset() {
	feat := f.feature
	f.prevFeature = &feat
	f.notable = nil
	f.evidence = map[string]bool{}
	f.changed = false
}

func hz(v float64) string {
	if v >= 1000 {
		return strconv.FormatFloat(math.Round(v/10)/100, 'f', -1, 64) + " kHz"
	}
	return strconv.Itoa(int(math.Round(v))) + " Hz"
}

func dur(s float64) string {
	switch {
	case s < 90:
		return fmt.Sprintf("%d seconds", int(math.Round(s)))
	case s < 5400:
		return fmt.Sprintf("%d minutes", int(math.Round(s/60)))
	default:
		return fmt.Sprintf("%.1f hours", s/3600)
	}
}

func words(state string) string { return strings.ReplaceAll(state, "_", " ") }

func capital(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// motifName turns "motif-009" into "motif 9".
func motifName(id string) string {
	if n, ok := motifNumber(id); ok {
		return "motif " + strconv.Itoa(n)
	}
	return id
}

func motifNumber(id string) (int, bool) {
	i := strings.LastIndexByte(id, '-')
	if !strings.HasPrefix(id, "motif-") || i < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(id[i+1:])
	return n, err == nil
}

// allowance is what the text may mention: frequencies from the evidence
// (matched within 2 %) and motif numbers from the evidence.
type allowance struct {
	hz     []float64
	motifs map[int]bool
}

func (a *allowance) addHz(v float64) {
	if v > 0 {
		a.hz = append(a.hz, v)
	}
}

// check returns the first frequency or motif number in text that the
// evidence doesn't support, or "" if everything is backed.
func (a allowance) check(text string) string {
	for _, v := range hzIn(text) {
		ok := false
		for _, w := range a.hz {
			if math.Abs(v-w)/w <= 0.02 {
				ok = true
				break
			}
		}
		if !ok {
			return hz(v)
		}
	}
	for _, m := range motifRE.FindAllStringSubmatch(text, -1) {
		n, _ := strconv.Atoi(m[1])
		if !a.motifs[n] {
			return "motif " + m[1]
		}
	}
	return ""
}

var (
	hzRE    = regexp.MustCompile(`(?i)(\d+(?:[.,]\d+)?)\s*(k?)hz\b`)
	motifRE = regexp.MustCompile(`(?i)\bmotif\s*#?\s*(\d+)`)
)

// hzIn finds frequencies written in text, in Hz.
func hzIn(text string) []float64 {
	var out []float64
	for _, m := range hzRE.FindAllStringSubmatch(text, -1) {
		v, err := strconv.ParseFloat(strings.Replace(m[1], ",", ".", 1), 64)
		if err != nil {
			continue
		}
		if strings.EqualFold(m[2], "k") {
			v *= 1000
		}
		out = append(out, v)
	}
	return out
}
