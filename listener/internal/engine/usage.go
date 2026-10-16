package engine

import (
	"runtime"
	"sync"
	"syscall"
	"time"
)

// meter measures what the engine, each source (its ffmpeg and front end)
// and each analysis cost, as averages since the previous measurement
// (every 10 seconds). A stream is charged an equal share of its source.
type meter struct {
	e *Engine

	mu      sync.Mutex
	at      time.Time
	selfCPU time.Duration
	procCPU map[int64]time.Duration // ffmpeg by pid
	feBusy  map[*sourceConn]int64
	runBusy map[*run]int64
	sources map[*sourceConn]Usage // full cost, last measurement
	engine  Usage
}

func newMeter(e *Engine) *meter {
	return &meter{
		e: e, at: time.Now(), selfCPU: selfCPU(), procCPU: map[int64]time.Duration{},
		feBusy: map[*sourceConn]int64{}, runBusy: map[*run]int64{}, sources: map[*sourceConn]Usage{},
	}
}

// UsageReport is the data of the usage event, for one caller.
type UsageReport struct {
	Engine   Usage            `json:"engine"`
	Streams  map[string]Usage `json:"streams"`  // the caller's streams: their shares
	Analyses map[string]Usage `json:"analyses"` // keyed "stream/analysis"
	Sources  map[string]Usage `json:"sources"`  // each source once, keyed by the caller's stream on it
}

func (m *meter) measure() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	wall := now.Sub(m.at)
	if wall <= 0 {
		wall = time.Millisecond
	}
	pct := func(d time.Duration) float64 { return round(100*float64(d)/float64(wall), 1) }

	self := selfCPU()
	total := Usage{CPUPercent: pct(self - m.selfCPU), MemoryMB: round(selfMemMB(), 1)}
	m.selfCPU = self

	procs, feBusy, sources := map[int64]time.Duration{}, map[*sourceConn]int64{}, map[*sourceConn]Usage{}
	for _, src := range m.e.sourceList() {
		measured := false
		u := Usage{InMeasured: &measured}
		// The source's cost: its front end (inside the engine) and its
		// ffmpeg (a process of its own).
		fb := src.feBusyNs.Load()
		if prev, ok := m.feBusy[src]; ok {
			u.CPUPercent = pct(time.Duration(fb - prev))
		}
		feBusy[src] = fb
		if pid := src.pid.Load(); pid > 0 {
			if cpu, rss, ok := procStat(int(pid)); ok {
				if prev, ok := m.procCPU[pid]; ok {
					u.CPUPercent = round(u.CPUPercent+pct(cpu-prev), 1)
					total.CPUPercent += pct(cpu - prev)
				}
				procs[pid] = cpu
				u.MemoryMB = round(rss, 1)
				total.MemoryMB += u.MemoryMB
			}
		}
		sources[src] = u
	}
	busy := map[*run]int64{}
	for _, c := range m.e.callers {
		for _, s := range c.streamList() {
			full := sources[s.src]
			n := s.src.shared()
			if n < 1 {
				n = 1
			}
			cpu, mem := full.CPUPercent, full.MemoryMB
			share := Usage{
				CPUPercent: round(cpu/float64(n), 1), MemoryMB: round(mem/float64(n), 1), InMeasured: full.InMeasured,
				SourceCPUPercent: &cpu, SourceMemoryMB: &mem, Shared: n,
			}
			s.mu.Lock()
			s.lastUsage = share
			runs := s.runList()
			s.mu.Unlock()
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
			}
		}
	}
	m.procCPU, m.feBusy, m.runBusy, m.sources, m.at = procs, feBusy, busy, sources, now
	total.CPUPercent = round(total.CPUPercent, 1)
	total.MemoryMB = round(total.MemoryMB, 1)
	m.engine = total
}

// reportFor is the usage event for one caller: the engine's totals, and the
// caller's own streams, sources and analyses.
func (m *meter) reportFor(c *caller) UsageReport {
	m.mu.Lock()
	rep := UsageReport{Engine: m.engine, Streams: map[string]Usage{}, Analyses: map[string]Usage{}, Sources: map[string]Usage{}}
	full := m.sources
	m.mu.Unlock()
	for _, s := range c.streamList() {
		s.mu.Lock()
		rep.Streams[s.id] = s.lastUsage
		runs := s.runList()
		s.mu.Unlock()
		if u, ok := full[s.src]; ok {
			rep.Sources[s.id] = u
		}
		for _, r := range runs {
			r.mu.Lock()
			rep.Analyses[r.source()] = r.lastUsage
			r.mu.Unlock()
		}
	}
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
