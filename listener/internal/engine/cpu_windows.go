package engine

import (
	"syscall"
	"time"
)

// selfCPU is the CPU time the engine itself has used.
func selfCPU() time.Duration {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var creation, exit, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user) != nil {
		return 0
	}
	// A Filetime here is a duration in units of 100 ns.
	ticks := func(f syscall.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return time.Duration((ticks(kernel) + ticks(user)) * 100)
}
