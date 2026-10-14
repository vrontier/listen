package web

import "testing"

func TestParseCoords(t *testing.T) {
	good := map[string][2]float64{
		"50.12345, 10.54321":       {50.12345, 10.54321},
		"50.12345 10.54321":        {50.12345, 10.54321},
		"50,12345; 10,54321":       {50.12345, 10.54321},
		"-33.8688, 151.2093":       {-33.8688, 151.2093},
		"41.33°, -75.6°":           {41.33, -75.6},
		"50.123456789, 10.5432149": {50.12346, 10.54321},
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
