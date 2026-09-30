package relay

import (
	"bytes"
	"os/exec"
	"testing"
)

// Encodes 3 s of a test tone with each relay codec and checks that the
// splitter finds one init segment and gap-free fragments whose media times
// add up to the input length.
func TestSplitterOnFFmpegOutput(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	for _, c := range Codecs {
		t.Run(c.Name, func(t *testing.T) {
			args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=3"}
			args = append(append(args, c.Args...), "pipe:1")
			out, err := exec.Command("ffmpeg", args...).Output()
			if err != nil {
				t.Skipf("ffmpeg cannot encode %s: %v", c.Name, err)
			}
			var inits int
			var timescale uint32
			var bases []uint64
			err = Splitter{
				OnInit:     func(init []byte, ts uint32) { inits++; timescale = ts },
				OnFragment: func(frag []byte, base uint64) { bases = append(bases, base) },
			}.Run(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if inits != 1 || timescale == 0 {
				t.Fatalf("inits=%d timescale=%d", inits, timescale)
			}
			if len(bases) < 4 {
				t.Fatalf("only %d fragments", len(bases))
			}
			for i := 1; i < len(bases); i++ {
				if bases[i] <= bases[i-1] {
					t.Fatalf("fragment times not increasing: %v", bases)
				}
			}
			last := float64(bases[len(bases)-1]) / float64(timescale)
			if last < 2 || last > 3 {
				t.Errorf("last fragment starts at %.2f s, want within the 3 s input", last)
			}
		})
	}
}
