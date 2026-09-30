package memory

import (
	"sort"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

// minute aggregates one minute of observations. Finished minutes go to the
// history file and back into the summaries after a restart.
type minute struct {
	T           time.Time          `json:"t"`
	N           int                `json:"n"`
	Entropy     float64            `json:"entropy"`
	Harmonicity float64            `json:"harmonicity"`
	Energy      float64            `json:"energy"`
	DominantLo  float64            `json:"dominant_p10_hz"`
	DominantHi  float64            `json:"dominant_p90_hz"`
	Events      int                `json:"events"`
	Presence    map[string]float64 `json:"presence,omitempty"` // motif id → seconds present

	entSum, harSum, enSum float64
	dominants             []float64
}

func newMinute(t time.Time) *minute {
	return &minute{T: t.Truncate(time.Minute), Presence: map[string]float64{}}
}

func (m *minute) addFeature(f events.Feature) {
	m.N++
	m.entSum += f.Entropy
	m.harSum += f.Harmonicity
	m.enSum += f.Energy
	if f.DominantHz > 0 {
		m.dominants = append(m.dominants, f.DominantHz)
	}
}

func (m *minute) finish() {
	if m.N > 0 {
		n := float64(m.N)
		m.Entropy = round(m.entSum/n, 3)
		m.Harmonicity = round(m.harSum/n, 3)
		m.Energy = round(m.enSum/n, 3)
	}
	if len(m.dominants) > 0 {
		sort.Float64s(m.dominants)
		m.DominantLo = round(m.dominants[len(m.dominants)/10], 1)
		m.DominantHi = round(m.dominants[len(m.dominants)*9/10], 1)
	}
	for id, s := range m.Presence {
		m.Presence[id] = round(s, 1)
	}
}

// summarize builds a memory.summary over the minutes within window of now.
func summarize(minutes []*minute, now time.Time, window time.Duration, label string, known int, aliases map[string]string) events.MemorySummary {
	s := events.MemorySummary{Window: label, KnownMotifs: known, DominantMotifs: []string{}}
	presence := map[string]float64{}
	var n, entSum, harSum float64
	var los, his []float64
	for _, m := range minutes {
		if m.T.Before(now.Add(-window)) || m.N == 0 {
			continue
		}
		w := float64(m.N)
		n += w
		entSum += m.Entropy * w
		harSum += m.Harmonicity * w
		s.EventCount += m.Events
		if m.DominantHi > 0 {
			los = append(los, m.DominantLo)
			his = append(his, m.DominantHi)
		}
		for id, sec := range m.Presence {
			if a, ok := aliases[id]; ok {
				id = a
			}
			presence[id] += sec
		}
	}
	if n > 0 {
		s.MeanEntropy = round(entSum/n, 3)
		s.MeanHarmonicity = round(harSum/n, 3)
	}
	if len(los) > 0 {
		sort.Float64s(los)
		sort.Float64s(his)
		s.DominantFrequencyRangeHz = [2]float64{los[len(los)/2], his[len(his)/2]}
	}
	ids := make([]string, 0, len(presence))
	for id := range presence {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return presence[ids[i]] > presence[ids[j]] })
	if len(ids) > 3 {
		ids = ids[:3]
	}
	s.DominantMotifs = append(s.DominantMotifs, ids...)
	return s
}
