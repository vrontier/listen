package web

import (
	"bytes"
	"strings"
	"testing"
	"testing/iotest"
)

func TestRoundCoords(t *testing.T) {
	in := `{"sources":[{"name":"A \"lat\": 1.234","lat":50.12345,"lon":-10.54321},{"lat": 49.96,"lon":null}],"note":"\"lon\":3.33"}`
	want := `{"sources":[{"name":"A \"lat\": 1.234","lat":50.1,"lon":-10.5},{"lat":50.0,"lon":null}],"note":"\"lon\":3.33"}`
	if got := string(roundCoordsIn([]byte(in))); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// The same through the copy, whatever the chunks and wherever they end.
	big := strings.Repeat(`{"x":"`+strings.Repeat("y", 37)+`"},`, 5000) + in
	bigWant := strings.Repeat(`{"x":"`+strings.Repeat("y", 37)+`"},`, 5000) + want
	var out bytes.Buffer
	if err := copyRoundingCoords(&out, iotest.OneByteReader(strings.NewReader(big))); err != nil || out.String() != bigWant {
		t.Fatalf("one byte at a time: %v", err)
	}
	out.Reset()
	if err := copyRoundingCoords(&out, iotest.HalfReader(strings.NewReader(big))); err != nil || out.String() != bigWant {
		t.Fatalf("half reads: %v", err)
	}
	// Every position of a member against a chunk's end.
	for pad := 0; pad < 200; pad++ {
		doc := strings.Repeat(" ", 32<<10-pad) + `{"lat":50.12345,"lon":10.54321}`
		var out bytes.Buffer
		copyRoundingCoords(&out, strings.NewReader(doc))
		if !strings.HasSuffix(out.String(), `{"lat":50.1,"lon":10.5}`) || out.Len() != len(doc)-8 {
			t.Fatalf("pad %d: %q", pad, out.String()[len(out.String())-40:])
		}
	}
	if publicCoordText(nil) != "" || publicCoordText(ptr(-0.04)) != "-0.0" {
		t.Fatal(publicCoordText(ptr(-0.04)))
	}
}

func ptr(v float64) *float64 { return &v }
