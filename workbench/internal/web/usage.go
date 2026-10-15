package web

import (
	"context"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// Resources and caps (members-and-streams.md): the engine reports every
// 10 s what each stream and analysis uses; the Workbench counts each
// analysis (with its preview and trial) against its member, splits a
// stream's connection and decode equally among the members whose analyses
// run on it, keeps a history per minute, and refuses to start what would
// go over a member's or the server's cap.

type accounting struct {
	s *Server

	mu      sync.Mutex
	current map[int64]catalogue.Usage // by member; 0 is the engine
	items   []usageItem               // what runs now, for the usage page
	minute  time.Time
	sums    map[int64]catalogue.Usage
	samples int
}

// usageItem is one running thing and the member it counts against.
type usageItem struct {
	User     int64
	What     string // analysis, preview, trial, connection
	Analysis string // title, or the source's name for a connection
	Source   string
	Share    int // members sharing a connection
	catalogue.Usage
}

func newAccounting(s *Server) *accounting {
	return &accounting{s: s, current: map[int64]catalogue.Usage{}, sums: map[int64]catalogue.Usage{}}
}

func add(a, b catalogue.Usage) catalogue.Usage {
	return catalogue.Usage{CPUPercent: a.CPUPercent + b.CPUPercent, MemoryMB: a.MemoryMB + b.MemoryMB, InKbps: a.InKbps + b.InKbps}
}

func fromEngine(u engine.Usage) catalogue.Usage {
	return catalogue.Usage{CPUPercent: u.CPUPercent, MemoryMB: u.MemoryMB, InKbps: u.InKbps}
}

// engineNumber reads the analysis number from an engine identifier: a-0004,
// its preview p-0004 or trial t-0004.
func engineNumber(id string) (int64, string, bool) {
	kinds := map[string]string{"a": "analysis", "p": "preview", "t": "trial"}
	k, n, ok := strings.Cut(id, "-")
	if !ok || kinds[k] == "" {
		return 0, "", false
	}
	v, err := strconv.ParseInt(n, 10, 64)
	return v, kinds[k], err == nil
}

// report attributes one usage report and adds it to the minute.
func (c *accounting) report(u engine.UsageReport) {
	ctx := context.Background()
	analyses, err := c.s.db.AllAnalyses(ctx)
	if err != nil {
		return
	}
	streams, err := c.s.db.AllStreams(ctx)
	if err != nil {
		return
	}
	byID := map[int64]catalogue.Analysis{}
	for _, a := range analyses {
		byID[a.ID] = a
	}
	byEngine := map[string]catalogue.Stream{}
	for _, st := range streams {
		byEngine[st.EngineID()] = st
	}
	cur := map[int64]catalogue.Usage{0: fromEngine(u.Engine)}
	var items []usageItem
	users := map[string]map[int64]bool{} // stream → members with something on it
	for key, au := range u.Analyses {
		sid, aid, _ := strings.Cut(key, "/")
		n, kind, ok := engineNumber(aid)
		a, known := byID[n]
		if !ok || !known {
			continue
		}
		cur[a.OwnerID] = add(cur[a.OwnerID], fromEngine(au))
		if users[sid] == nil {
			users[sid] = map[int64]bool{}
		}
		users[sid][a.OwnerID] = true
		items = append(items, usageItem{User: a.OwnerID, What: kind, Analysis: a.Name, Source: byEngine[sid].Texts.Name, Usage: fromEngine(au)})
	}
	for sid, su := range u.Streams {
		st, known := byEngine[sid]
		if !known {
			continue
		}
		owners := users[sid]
		if len(owners) == 0 {
			owners = map[int64]bool{st.OwnerID: true}
		}
		k := float64(len(owners))
		share := catalogue.Usage{CPUPercent: su.CPUPercent / k, MemoryMB: su.MemoryMB / k, InKbps: su.InKbps / k}
		for id := range owners {
			cur[id] = add(cur[id], share)
			items = append(items, usageItem{User: id, What: "connection", Source: st.Texts.Name, Share: len(owners), Usage: share})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CPUPercent > items[j].CPUPercent })

	c.mu.Lock()
	defer c.mu.Unlock()
	c.current, c.items = cur, items
	m := time.Now().UTC().Truncate(time.Minute)
	if !c.minute.IsZero() && !m.Equal(c.minute) && c.samples > 0 {
		avg := map[int64]catalogue.Usage{}
		for id, s := range c.sums {
			n := float64(c.samples)
			avg[id] = catalogue.Usage{CPUPercent: s.CPUPercent / n, MemoryMB: s.MemoryMB / n, InKbps: s.InKbps / n}
		}
		minute := c.minute
		go func() {
			if err := c.s.db.AddUsage(context.Background(), minute, avg); err != nil {
				log.Printf("usage: %v", err)
			}
		}()
		c.sums, c.samples = map[int64]catalogue.Usage{}, 0
	}
	if !m.Equal(c.minute) {
		c.minute = m
	}
	for id, v := range cur {
		c.sums[id] = add(c.sums[id], v)
	}
	c.samples++
}

// now returns a member's use now (0: the engine's).
func (c *accounting) now(user int64) catalogue.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current[user]
}

// running lists what counts against a member now.
func (c *accounting) running(user int64) []usageItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []usageItem
	for _, it := range c.items {
		if it.User == user {
			out = append(out, it)
		}
	}
	return out
}

// Estimates of what something costs until the engine has measured it, at
// 22 050 Hz and scaled with the rate (proposed, members-and-streams.md:
// "until measured: a fixed estimate").
var (
	estAnalysis   = catalogue.Usage{CPUPercent: 1.0, MemoryMB: 8}
	estConnection = catalogue.Usage{CPUPercent: 1.5, MemoryMB: 25, InKbps: 128}
)

// estimate is what starting the analysis (as a run, preview or trial)
// adds: one engine analysis per running source, and the connection of each
// source that is not connected yet.
func (s *Server) estimate(a catalogue.Analysis) catalogue.Usage {
	var e catalogue.Usage
	for _, src := range a.Sources {
		if !running(src.Stream) {
			continue
		}
		scale := float64(src.Stream.SampleRate) / 22050
		e = add(e, catalogue.Usage{CPUPercent: estAnalysis.CPUPercent * scale, MemoryMB: estAnalysis.MemoryMB})
		if es, ok := s.sync.stream(src.Stream.EngineID()); !ok || es.State != "connected" {
			e = add(e, catalogue.Usage{CPUPercent: estConnection.CPUPercent * scale, MemoryMB: estConnection.MemoryMB, InKbps: estConnection.InKbps})
		}
	}
	return e
}

// overCap explains why starting would go over a cap, or returns "".
func overCap(who string, c catalogue.ServerCap, cur, est catalogue.Usage) string {
	if c.CPUCores > 0 && cur.CPUPercent+est.CPUPercent > c.CPUCores*100 {
		return fmt.Sprintf("%s CPU cap is %s cores; %s are in use and this needs about %s more.", who,
			strconv.FormatFloat(c.CPUCores, 'g', -1, 64), coresOf(cur.CPUPercent), coresOf(est.CPUPercent))
	}
	if c.MemoryMB > 0 && cur.MemoryMB+est.MemoryMB > float64(c.MemoryMB) {
		return fmt.Sprintf("%s memory cap is %d MB; %.0f MB are in use and this needs about %.0f MB more.", who, c.MemoryMB, cur.MemoryMB, est.MemoryMB)
	}
	return ""
}

func coresOf(percent float64) string { return strconv.FormatFloat(math.Round(percent)/100, 'f', 2, 64) }

// capRefusal tells why the member may not start the analysis now (as a run
// if run is true, else as a preview or trial), or returns "".
func (s *Server) capRefusal(ctx context.Context, user int64, a catalogue.Analysis, run bool) (string, error) {
	for _, src := range a.Sources {
		ok, err := s.db.MayUseStream(ctx, user, src.Stream)
		if err != nil {
			return "", err
		}
		if !ok {
			return src.Stream.Texts.Name + " is no longer shared with you; take it out of the analysis to go on without it.", nil
		}
	}
	est := s.estimate(a)
	mc, _, err := s.db.MemberCap(ctx, user)
	if err != nil {
		return "", err
	}
	if why := overCap("Your", mc, s.acct.now(user), est); why != "" {
		return why, nil
	}
	sc, err := s.db.ServerCap(ctx)
	if err != nil {
		return "", err
	}
	if why := overCap("The server's", sc, s.acct.now(0), est); why != "" {
		return why + " Please try again later.", nil
	}
	if run {
		used, err := s.db.DatasetBytesOf(ctx, user)
		if err != nil {
			return "", err
		}
		if used >= diskQuota {
			return fmt.Sprintf("Your data sets use %s of your %s: delete some under Runs before starting a run.", byteSize(used), byteSize(diskQuota)), nil
		}
	}
	return "", nil
}

func byteSize(n int64) string { return funcs["bytes"].(func(int64) string)(n) }

// --- the usage page ---

type usageBar struct {
	X, Y, W, H float64
	Title      string
}

type usageData struct {
	Cap      catalogue.ServerCap
	OwnCap   bool
	Now      catalogue.Usage
	Running  []usageItem
	Disk     int64
	Quota    int64
	DiskWarn bool
	Day      []usageBar // CPU per hour, last 24 hours
	Month    []usageBar // CPU per day, last 30 days
	DayMax   float64
	MonthMax float64
}

// bars averages samples into n slots of a period ending now, counting
// minutes without a sample as nothing used, and lays them out as bars of
// a 600 × 100 drawing.
func bars(samples []catalogue.UsageSample, end time.Time, slot time.Duration, n int, label string) ([]usageBar, float64) {
	sums := make([]float64, n)
	start := end.Add(-slot * time.Duration(n))
	for _, s := range samples {
		i := int(s.Minute.Sub(start) / slot)
		if i >= 0 && i < n {
			sums[i] += s.CPUPercent
		}
	}
	minutes := slot.Minutes()
	max := 0.0
	for i := range sums {
		sums[i] /= minutes
		max = math.Max(max, sums[i])
	}
	scale := 1.0
	if max > 0 {
		scale = 96 / max
	}
	w := 600.0 / float64(n)
	out := make([]usageBar, n)
	for i, v := range sums {
		h := v * scale
		t := start.Add(slot * time.Duration(i)).UTC()
		out[i] = usageBar{X: float64(i)*w + 1, Y: 100 - h, W: w - 2, H: h, Title: fmt.Sprintf("%s: %s cores on average", t.Format(label), coresOf(v))}
	}
	return out, max
}

func (s *Server) usagePage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ctx := r.Context()
	var d usageData
	var err error
	if d.Cap, d.OwnCap, err = s.db.MemberCap(ctx, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Now = s.acct.now(sess.User.ID)
	d.Running = s.acct.running(sess.User.ID)
	if d.Disk, err = s.db.DatasetBytesOf(ctx, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Quota = diskQuota
	d.DiskWarn = d.Disk*5 >= d.Quota*4
	now := time.Now().UTC()
	samples, err := s.db.UsageSince(ctx, sess.User.ID, now.Add(-31*24*time.Hour))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hourEnd := now.Truncate(time.Hour).Add(time.Hour)
	d.Day, d.DayMax = bars(samples, hourEnd, time.Hour, 24, "Jan 2 15:00")
	dayEnd := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	d.Month, d.MonthMax = bars(samples, dayEnd, 24*time.Hour, 30, "Jan 2")
	s.render(w, r, 200, "usage", "Usage", d)
}
