package web

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"regexp"
	"strconv"
)

// Public pages show a source's place rounded to one decimal, about 10 km
// (members-and-streams.md, Of an analysis): the region, not the exact
// place, which stays with the member.

// publicCoord rounds a coordinate for a public page.
func publicCoord(v float64) float64 { return math.Round(v*10) / 10 }

func publicCoordText(p *float64) string {
	if p == nil {
		return ""
	}
	return strconv.FormatFloat(publicCoord(*p), 'f', 1, 64)
}

// coordKey finds a "lat" or "lon" member with a number in JSON. A JSON
// string cannot hold `"lat":` unescaped, so only real members match.
var coordKey = regexp.MustCompile(`"(lat|lon)":\s*(-?[0-9][0-9.eE+-]*)`)

func roundCoordsIn(b []byte) []byte {
	return coordKey.ReplaceAllFunc(b, func(m []byte) []byte {
		sub := coordKey.FindSubmatch(m)
		v, err := strconv.ParseFloat(string(sub[2]), 64)
		if err != nil {
			return m
		}
		return []byte(`"` + string(sub[1]) + `":` + strconv.FormatFloat(publicCoord(v), 'f', 1, 64))
	})
}

// roundCoordsJSON rounds every coordinate in a small JSON document.
func roundCoordsJSON(raw json.RawMessage) json.RawMessage { return roundCoordsIn(raw) }

// copyRoundingCoords copies a JSON file of any size, rounding its
// coordinates on the way. It holds back the end of each chunk that could be
// the start of a member cut in two.
func copyRoundingCoords(w io.Writer, r io.Reader) error {
	const keep = 64 // longer than any "lat": number this file holds
	buf := make([]byte, 0, 64<<10)
	chunk := make([]byte, 32<<10)
	for {
		n, err := r.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if err == io.EOF {
			_, werr := w.Write(roundCoordsIn(buf))
			return werr
		}
		if err != nil {
			return err
		}
		if len(buf) <= keep {
			continue
		}
		// Cut at the last quote before the held-back part, and before the
		// key if that quote closes "lat" or "lon", so that a member is never
		// split: its number follows the key's closing quote.
		cut := len(buf) - keep
		if i := bytes.LastIndexByte(buf[:cut], '"'); i >= 0 {
			cut = i
			if i >= 4 && (string(buf[i-4:i]) == `"lat` || string(buf[i-4:i]) == `"lon`) {
				cut = i - 4
			}
		}
		if _, err := w.Write(roundCoordsIn(buf[:cut])); err != nil {
			return err
		}
		buf = append(buf[:0], buf[cut:]...)
	}
}
