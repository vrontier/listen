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

func TestNormalInput(t *testing.T) {
	for in, want := range map[string]string{
		"https://stream.example.org/live.ogg":            "https://stream.example.org/live.ogg",
		" HTTPS://Stream.Example.ORG:443/live.ogg/ ":     "https://stream.example.org/live.ogg",
		"http://stream.example.org:80/":                  "http://stream.example.org",
		"http://stream.example.org:8000/a/?mount=x#frag": "http://stream.example.org:8000/a?mount=x",
		"http://[2001:DB8::1]:8000/live":                 "http://[2001:db8::1]:8000/live",
		"https://stream.example.org/Live.ogg":            "https://stream.example.org/Live.ogg",
	} {
		if got, ok := normalInput(in); !ok || got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{"ftp://example.org/x", "example.org/live", "https://user:pw@example.org/x", "https:///x"} {
		if got, ok := normalInput(in); ok {
			t.Errorf("%q taken as %q", in, got)
		}
	}
	if otherScheme("https://a.example.org/x") != "http://a.example.org/x" || otherScheme("http://a.example.org/x") != "https://a.example.org/x" {
		t.Error("otherScheme")
	}
}
