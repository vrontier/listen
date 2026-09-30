package analysis

import (
	"math"
	"testing"
	"time"

	"github.com/vrontier/listen/listener/internal/events"
)

func TestReplayClockWrapsWithinRecording(t *testing.T) {
	start := time.Date(2026, 9, 30, 8, 35, 4, 0, time.UTC)
	length := 2 * time.Second
	var stamps []time.Time
	a := New(0, events.NewStamper("test"), func(m events.Message) {
		if m.Type == events.TypeFrame {
			stamps = append(stamps, m.Env.Timestamp)
		}
	})
	a.Replay(start, length)

	// 5 s of a quiet tone: the looped file plays two and a half times.
	x := make([]float32, 5*DefaultSampleRate)
	for i := range x {
		x[i] = float32(0.1 * math.Sin(2*math.Pi*440*float64(i)/DefaultSampleRate))
	}
	a.Feed(x)

	if len(stamps) < 40 {
		t.Fatalf("only %d frames", len(stamps))
	}
	wrapped := false
	for i, ts := range stamps {
		if ts.Before(start) || !ts.Before(start.Add(length)) {
			t.Fatalf("frame %d at %s, outside the recording [%s, %s)", i, ts, start, start.Add(length))
		}
		if i > 0 && ts.Before(stamps[i-1]) {
			wrapped = true
		}
	}
	if !wrapped {
		t.Error("clock never wrapped back to the start of the recording")
	}
}
