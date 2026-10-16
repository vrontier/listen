//go:build !linux

package engine

import (
	"runtime"
	"time"
)

// Outside Linux (development) ffmpeg's cost is not measured.
func procStat(pid int) (time.Duration, float64, bool) { return 0, 0, false }

func selfMemMB() float64 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.Sys) / (1 << 20)
}

func hostMemoryMB() int { return 0 }
