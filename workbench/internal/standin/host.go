package standin

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// hostInfo reports the machine the stand-in runs on, as the engine will:
// its cores and its memory. Memory is read from /proc/meminfo, so it is 0
// where that does not exist (macOS).
func hostInfo() map[string]int {
	return map[string]int{"cores": runtime.NumCPU(), "memory_mb": memTotalMB()}
}

func memTotalMB() int {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemTotal:"); ok {
			kb, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(rest), " kB"))
			return kb / 1024
		}
	}
	return 0
}
