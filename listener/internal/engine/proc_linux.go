package engine

import (
	"os"
	"strconv"
	"strings"
	"time"
)

const clockTicks = 100 // USER_HZ on Linux

// procStat reads a process's CPU time and resident memory from /proc.
func procStat(pid int) (cpu time.Duration, rssMB float64, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	// Fields after the command name, which is in parentheses and may hold spaces.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 22 {
		return 0, 0, false
	}
	ut, _ := strconv.ParseInt(f[11], 10, 64)  // utime, field 14
	st, _ := strconv.ParseInt(f[12], 10, 64)  // stime, field 15
	rss, _ := strconv.ParseInt(f[21], 10, 64) // rss in pages, field 24
	cpu = time.Duration(ut+st) * time.Second / clockTicks
	return cpu, float64(rss*int64(os.Getpagesize())) / (1 << 20), true
}

func selfMemMB() float64 {
	if _, rss, ok := procStat(os.Getpid()); ok {
		return rss
	}
	return 0
}

// hostMemoryMB is the machine's memory, from /proc/meminfo.
func hostMemoryMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				kb, _ := strconv.Atoi(f[1])
				return kb / 1024
			}
		}
	}
	return 0
}
