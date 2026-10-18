package web

import (
	"bufio"
	"io"
	"net/url"
	"strings"
)

// Reserved inputs: streams whose operators have not agreed to their
// analysis on the Workbench, which members therefore cannot register
// (decided 2026-10-16 for the Observatory's stations; since 2026-10-18,
// when the engine reads every station through listen-ingest and a station
// is connected to once, only streams without consent). The list is set on
// the server, never in the repository.

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
