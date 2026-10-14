package web

import "testing"

func TestParseCoords(t *testing.T) {
	good := map[string][2]float64{
		"49.41234, 8.69121":       {49.41234, 8.69121},
		"49.41234 8.69121":        {49.41234, 8.69121},
		"49,41234; 8,69121":       {49.41234, 8.69121},
		"-33.8688, 151.2093":      {-33.8688, 151.2093},
		"41.33°, -75.6°":          {41.33, -75.6},
		"49.412345678, 8.6912149": {49.41235, 8.69121},
	}
	for in, want := range good {
		lat, lon, ok := parseCoords(in)
		if !ok || lat != want[0] || lon != want[1] {
			t.Errorf("%q: %v %v %v", in, lat, lon, ok)
		}
	}
	for _, in := range []string{"", "49.4", "91, 8", "49, 181", "north, east", "1, 2, 3", "NaN, 1"} {
		if _, _, ok := parseCoords(in); ok {
			t.Errorf("accepted %q", in)
		}
	}
}
