package web

import (
	"bufio"
	"io"
	"net/url"
	"strings"
)

// Reserved inputs (decided 2026-10-16): addresses the Observatory listens
// to. Until a stream reaches the engine through a local input from the
// Observatory's listener, with its operator's consent, the Workbench does
// not register them, so that the station is not connected to twice. The
// list is set on the server, never in the repository.

// ParseReserved reads one entry per line: a host name (every address on
// it) or a URL (that address, with or without its scheme). Empty lines
// and lines starting with # are skipped.
func ParseReserved(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, strings.ToLower(line))
	}
	return out, sc.Err()
}

// reserved reports whether an input is on the list.
func (s *Server) reserved(input string) bool {
	u, err := url.Parse(input)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	bare := strings.ToLower(strings.TrimSuffix(u.Host+u.EscapedPath(), "/"))
	if u.RawQuery != "" {
		bare += "?" + u.RawQuery
	}
	for _, e := range s.cfg.Reserved {
		if strings.Contains(e, "/") || strings.Contains(e, "://") {
			_, rest, ok := strings.Cut(e, "://")
			if !ok {
				rest = e
			}
			if strings.TrimSuffix(rest, "/") == bare {
				return true
			}
		} else if e == host {
			return true
		}
	}
	return false
}
