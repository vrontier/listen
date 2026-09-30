package memory

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// store keeps the memory in plain files under one directory:
//
//	motifs.json               all motifs, rewritten atomically
//	history/YYYY-MM-DD.jsonl  append-only log of events, summaries and
//	                          per-minute aggregates, one JSON object per line
type store struct {
	dir     string
	keep    time.Duration
	day     string
	f       *os.File
	w       *bufio.Writer
	cleaned time.Time
}

// state is the content of motifs.json.
type state struct {
	Version    int               `json:"version"`
	NextKey    int               `json:"next_key"`
	NextPublic int               `json:"next_public"`
	Motifs     []*Motif          `json:"motifs"`
	Aliases    map[string]string `json:"aliases,omitempty"` // merged motif id → surviving id
}

// record is one line of a history file. Exactly one of Event and Minute is
// set. T is the memory clock (see Memory.now).
type record struct {
	T      time.Time       `json:"t"`
	Event  json.RawMessage `json:"event,omitempty"`
	Minute *minute         `json:"minute,omitempty"`
}

func openStore(dir string, keepDays int) (*store, error) {
	if err := os.MkdirAll(filepath.Join(dir, "history"), 0o750); err != nil {
		return nil, err
	}
	return &store{dir: dir, keep: time.Duration(keepDays) * 24 * time.Hour}, nil
}

func (s *store) loadState() (*state, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, "motifs.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return &state{Version: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("motifs.json: %w", err)
	}
	return &st, nil
}

func (s *store) saveState(st *state) error {
	b, err := json.MarshalIndent(st, "", " ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, "motifs.json.tmp")
	if err := os.WriteFile(tmp, b, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, "motifs.json"))
}

// append writes one record to the day's history file.
func (s *store) append(r record) error {
	day := r.T.UTC().Format("2006-01-02")
	if day != s.day || s.f == nil {
		if err := s.closeDay(); err != nil {
			return err
		}
		f, err := os.OpenFile(s.dayPath(day), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		s.f, s.w, s.day = f, bufio.NewWriter(f), day
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	s.w.Write(b)
	return s.w.WriteByte('\n')
}

func (s *store) flush() error {
	if s.w == nil {
		return nil
	}
	return s.w.Flush()
}

func (s *store) closeDay() error {
	if s.f == nil {
		return nil
	}
	err := s.w.Flush()
	if cerr := s.f.Close(); err == nil {
		err = cerr
	}
	s.f, s.w = nil, nil
	return err
}

func (s *store) close() error { return s.closeDay() }

func (s *store) dayPath(day string) string {
	return filepath.Join(s.dir, "history", day+".jsonl")
}

// days lists history days in [from, to], oldest first.
func (s *store) days(from, to time.Time) []string {
	ents, _ := os.ReadDir(filepath.Join(s.dir, "history"))
	lo, hi := from.UTC().Format("2006-01-02"), to.UTC().Format("2006-01-02")
	var out []string
	for _, e := range ents {
		d := strings.TrimSuffix(e.Name(), ".jsonl")
		if d != e.Name() && d >= lo && d <= hi {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// scan calls fn for every record with from <= T < to, oldest first, until
// fn returns false.
func (s *store) scan(from, to time.Time, fn func(record) bool) error {
	s.flush()
	for _, day := range s.days(from, to) {
		f, err := os.Open(s.dayPath(day))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var r record
			if json.Unmarshal(sc.Bytes(), &r) != nil || r.T.Before(from) || !r.T.Before(to) {
				continue
			}
			if !fn(r) {
				f.Close()
				return nil
			}
		}
		f.Close()
	}
	return nil
}

// cleanup deletes history days older than the retention period, at most
// once a day.
func (s *store) cleanup(now time.Time) {
	if s.keep <= 0 || now.Sub(s.cleaned) < 24*time.Hour {
		return
	}
	s.cleaned = now
	cutoff := now.Add(-s.keep).UTC().Format("2006-01-02")
	ents, _ := os.ReadDir(filepath.Join(s.dir, "history"))
	for _, e := range ents {
		if d := strings.TrimSuffix(e.Name(), ".jsonl"); d != e.Name() && d < cutoff && d != s.day {
			os.Remove(filepath.Join(s.dir, "history", e.Name()))
		}
	}
}
