package web

import (
	"context"
	"sync"
)

// visitors counts who reads published analyses live, by address, so that
// one visitor with several live views open (a source each, the
// comparison) counts once. At most the master admin's number of visitors
// read one analysis at once (members-and-streams.md, Of an analysis).
type visitors struct {
	mu sync.Mutex
	in map[int64]map[string]int // analysis → address → open live views
}

// enter lets a visitor in, unless the analysis is full; leave is to be
// called when their live view closes.
func (v *visitors) enter(analysis int64, addr string, limit int) (leave func(), ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.in == nil {
		v.in = map[int64]map[string]int{}
	}
	m := v.in[analysis]
	if m == nil {
		m = map[string]int{}
		v.in[analysis] = m
	}
	if m[addr] == 0 && len(m) >= limit {
		return nil, false
	}
	m[addr]++
	return func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		if m[addr]--; m[addr] <= 0 {
			delete(m, addr)
		}
		if len(m) == 0 {
			delete(v.in, analysis)
		}
	}, true
}

// full reports whether a visitor at addr would be turned away now.
func (v *visitors) full(analysis int64, addr string, limit int) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	m := v.in[analysis]
	return m[addr] == 0 && len(m) >= limit
}

func (s *Server) readerLimit(ctx context.Context) int {
	n, err := s.db.PublicReaders(ctx)
	if err != nil || n < 1 {
		return 1
	}
	return n
}
