package engine

import (
	"runtime"
	"sync"
	"syscall"
	"time"
)

// meter measures what the engine, each stream's ffmpeg and each analysis
// cost, as averages since the previous measurement (every 10 seconds).
type meter struct {
	e *Engine

	mu      sync.Mutex
	at      time.Time
	selfCPU time.Duration
	procCPU map[int64]time.Duration // ffmpeg by pid
	runBusy map[*run]int64
	feBusy  map[*stream]int64
	engine  Usage
}

func newMeter(e *Engine) *meter {
	return &meter{e: e, at: time.Now(), selfCPU: selfCPU(), procCPU: map[int64]time.Duration{}, runBusy: map[*run]int64{}, feBusy: map[*stream]int64{}}
}

// UsageReport is the data of the usage event.
type UsageReport struct {
	Engine   Usage            `json:"engine"`
	Streams  map[string]Usage `json:"streams"`
	Analyses map[string]Usage `json:"analyses"` // keyed "stream/analysis"
}

func (m *meter) measure() UsageReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	wall := now.Sub(m.at)
	if wall <= 0 {
		wall = time.Millisecond
	}
	pct := func(d time.Duration) float64 { return round(100*float64(d)/float64(wall), 1) }
	rep := UsageReport{Streams: map[string]Usage{}, Analyses: map[string]Usage{}}

	self := selfCPU()
	total := Usage{CPUPercent: pct(self - m.selfCPU), MemoryMB: round(selfMemMB(), 1)}
	m.selfCPU = self

	procs := map[int64]time.Duration{}
	m.e.mu.Lock()
	streams := make([]*stream, 0, len(m.e.streams))
	for _, id := range m.e.streamIDs() {
		streams = append(streams, m.e.streams[id])
	}
	m.e.mu.Unlock()
	busy, feBusy := map[*run]int64{}, map[*stream]int64{}
	for _, s := range streams {
		measured := false
		su := Usage{InMeasured: &measured}
		// The stream's own cost: its ffmpeg, and its front end (the FFT
		// all its analyses share), which runs inside the engine.
		fb := s.feBusyNs.Load()
		if prev, ok := m.feBusy[s]; ok {
			su.CPUPercent = pct(time.Duration(fb - prev))
		}
		feBusy[s] = fb
		if pid := s.pid.Load(); pid > 0 {
			if cpu, rss, ok := procStat(int(pid)); ok {
				if prev, ok := m.procCPU[pid]; ok {
					su.CPUPercent = round(su.CPUPercent+pct(cpu-prev), 1)
					total.CPUPercent += pct(cpu - prev)
				}
				procs[pid] = cpu
				su.MemoryMB = round(rss, 1)
				total.MemoryMB += su.MemoryMB
			}
		}
		s.mu.Lock()
		s.lastUsage = su
		runs := s.runList()
		s.mu.Unlock()
		rep.Streams[s.id] = su
		for _, r := range runs {
			b := r.busyNs.Load()
			ru := Usage{MemoryEstimated: true}
			if prev, ok := m.runBusy[r]; ok {
				ru.CPUPercent = pct(time.Duration(b - prev))
			}
			busy[r] = b
			ru.MemoryMB = round(r.memoryEstimate(), 1)
			r.mu.Lock()
			r.lastUsage = ru
			r.mu.Unlock()
			rep.Analyses[r.source()] = ru
		}
	}
	m.procCPU, m.runBusy, m.feBusy, m.at = procs, busy, feBusy, now
	total.CPUPercent = round(total.CPUPercent, 1)
	total.MemoryMB = round(total.MemoryMB, 1)
	m.engine = total
	rep.Engine = total
	return rep
}

func (m *meter) last() Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.engine
}

// memoryEstimate is a rough size of an analysis: its buffers and snapshot,
// and its memory's motifs and minutes. Go reports no memory per part of a
// program.
func (r *run) memoryEstimate() float64 {
	r.mu.Lock()
	mem := r.mem
	r.mu.Unlock()
	mb := 3.0
	if mem != nil {
		mb += 1 + 0.02*float64(mem.Established())
	}
	return mb
}

func selfCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func hostCores() int { return runtime.NumCPU() }
