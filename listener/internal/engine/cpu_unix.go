//go:build !windows

package engine

import (
	"syscall"
	"time"
)

// selfCPU is the CPU time the engine itself has used.
func selfCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
