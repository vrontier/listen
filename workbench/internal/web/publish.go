package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// Publishing (members-and-streams.md, Of an analysis; design.md, question
// 1): a member publishes an analysis at once, without review, at
// /research/<slug>, and withdraws it at once. Visitors see what it is, its
// sources with their credits, the rules it applies, its live view during a
// run, and its finished runs, with their data sets if the member chose so.

// researchSource is one source on a public page.
type researchSource struct {
	Stream   catalogue.Stream
	Settings string   // range, in words
	Rules    []string // in words
	Live     bool
	LivePath string
	DataPath string
}

type researchData struct {
	Analysis  catalogue.Analysis
	Base      string // /research/<slug>
	Sources   []researchSource
	Live      bool // in a run now
	Comparing bool
	Runs      []runView // finished, newest first
	Preview   bool      // the member's look before publishing
	Problems  []string  // what keeps the analysis from being published
	Sent      bool      // a report was just sent
	Full      bool      // in a run, but as many visitors read it live as may
}

func describeSettings(raw []byte) (string, []string) {
	var spec engine.AnalysisSpec
	json.Unmarshal(raw, &spec)
	var rng string
	switch lo, hi := spec.Settings.MinHz, spec.Settings.MaxHz; {
	case lo != nil && hi != nil:
		rng = fmt.Sprintf("from %s to %s Hz", fmtNum(lo), fmtNum(hi))
	case lo != nil:
		rng = fmt.Sprintf("from %s Hz up", fmtNum(lo))
	case hi != nil:
		rng = fmt.Sprintf("up to %s Hz", fmtNum(hi))
	default:
		rng = "the full range"
	}
	var rules []string
	for _, r := range spec.Rules {
		rules = append(rules, ruleWords(r))
	}
	return rng, rules
}

// researchView prepares an analysis's public page. With preview set it is
// the member's look before publishing: no live data.
func (s *Server) researchView(ctx context.Context, a catalogue.Analysis, preview bool) (researchData, error) {
	d := researchData{Analysis: a, Base: "/research/" + a.Slug, Preview: preview}
	_, inRun, err := s.db.ActiveRun(ctx, a.ID)
	if err != nil {
		return d, err
	}
	d.Live = inRun && !preview
	for _, src := range a.Sources {
		v := researchSource{Stream: src.Stream}
		v.Settings, v.Rules = describeSettings(src.Spec)
		sid := strconv.FormatInt(src.Stream.ID, 10)
		v.Live = d.Live && running(src.Stream)
		v.LivePath, v.DataPath = d.Base+"/live/"+sid, d.Base+"/data/"+sid+"/"
		d.Sources = append(d.Sources, v)
	}
	d.Comparing = d.Live && s.compares(a)
	runs, err := s.db.Runs(ctx, a.ID)
	if err != nil {
		return d, err
	}
	revs, _ := s.db.Revisions(ctx, a.ID)
	for _, v := range viewRuns(runs, func(int64) []catalogue.Revision { return revs }) {
		if !v.Active {
			d.Runs = append(d.Runs, v)
		}
	}
	return d, nil
}

// publishProblems lists what keeps a member from publishing an analysis:
// sources of others whose owners have not agreed, suspended sources.
func (s *Server) publishProblems(ctx context.Context, user int64, a catalogue.Analysis) ([]string, error) {
	var out []string
	for _, src := range a.Sources {
		if src.Stream.State == catalogue.StreamSuspended {
			out = append(out, src.Stream.Texts.Name+" is suspended by the master admin.")
			continue
		}
		ok, err := s.db.MayPublishWith(ctx, user, src.Stream)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, fmt.Sprintf("%s belongs to %s, who has not agreed to your publishing analyses of it. They can agree on the source's page, under Shared with.", src.Stream.Texts.Name, src.Stream.OwnerName))
		}
	}
	return out, nil
}

// --- the member's side ---

func (s *Server) publishForm(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	d, err := s.researchView(r.Context(), a, true)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Problems, err = s.publishProblems(r.Context(), sess.User.ID, a); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "publish", "Publish "+a.Name, d)
}

func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	back := analysisPath(a) + "/publish"
	problems, err := s.publishProblems(r.Context(), sess.User.ID, a)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(problems) > 0 {
		s.redirect(w, r, back, "Not published: "+strings.Join(problems, " "))
		return
	}
	if r.PostFormValue("attest") != "on" {
		s.redirect(w, r, back, "Not published: please confirm that you may publish the analysis of these sources.")
		return
	}
	slug, err := s.db.PublishAnalysis(r.Context(), a.ID, sess.User.ID, r.PostFormValue("data") == "on", time.Now())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/research/"+slug, "Published. This page is public now; you can withdraw it at any time on the analysis's page.")
}

func (s *Server) withdraw(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, ok := s.ownAnalysis(w, r)
	if !ok {
		return
	}
	if err := s.db.WithdrawAnalysis(r.Context(), a.ID, sess.User.ID, "by its member"); err != nil && !isNotFound(err) {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, analysisPath(a), "Withdrawn. The public page now says that the work was withdrawn.")
}

// withdrawUsing withdraws a member's published analyses that use a stream
// whose owner no longer agrees, and tells the member.
func (s *Server) withdrawUsing(ctx context.Context, by int64, member catalogue.User, st catalogue.Stream) int {
	as, err := s.db.PublishedUsing(ctx, member.ID, st.ID)
	if err != nil {
		log.Printf("withdraw using %s: %v", st.EngineID(), err)
		return 0
	}
	for _, a := range as {
		if err := s.db.WithdrawAnalysis(ctx, a.ID, by, "the owner of "+st.Texts.Name+" no longer agrees"); err != nil {
			log.Printf("withdraw %s: %v", a.EngineID(), err)
			continue
		}
		body := fmt.Sprintf("Hello %s,\n\nyour published analysis \"%s\" uses the source \"%s\", whose owner no longer agrees to its publication. It has been withdrawn; its public page now says so. It is private again and otherwise unchanged.\n\n%s%s\n",
			member.Name, a.Name, st.Texts.Name, s.cfg.BaseURL, analysisPath(a))
		if err := s.mail.Send(member.Email, "Withdrawn: "+a.Name, body); err != nil {
			log.Printf("withdraw mail: %v", err)
		}
	}
	return len(as)
}

// streamConsent records whether the owner agrees that a member the stream
// is shared with publishes analyses of it.
func (s *Server) streamConsent(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	back := "/sources/" + st.Slug
	id, _ := strconv.ParseInt(r.PostFormValue("member"), 10, 64)
	agree := r.PostFormValue("agree") == "1"
	if err := s.db.SetPublishConsent(r.Context(), st.ID, id, sess.User.ID, agree); err != nil {
		if isNotFound(err) {
			s.redirect(w, r, back, "The source is not shared with that member.")
			return
		}
		s.fail(w, r, err)
		return
	}
	if agree {
		s.redirect(w, r, back, "Agreed: that member may publish analyses that use this source.")
		return
	}
	u, _ := s.db.User(r.Context(), id)
	n := s.withdrawUsing(r.Context(), sess.User.ID, u, st)
	s.redirect(w, r, back, fmt.Sprintf("No longer agreed. %d published analys%s of that member withdrawn.", n, map[bool]string{true: "is", false: "es"}[n == 1]))
}

// --- the public side ---

// visitor wraps a public handler: anyone may come; a member's session, if
// there is one, shows in the page's header.
func (s *Server) visitor(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r, _ = s.loadSession(r)
		h(w, r)
	}
}

func (s *Server) researchIndex(w http.ResponseWriter, r *http.Request) {
	as, err := s.db.PublishedAnalyses(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "research-index", "Research", as)
}

// publicAnalysis finds the published analysis named in the path. A
// withdrawn one answers that it was withdrawn.
func (s *Server) publicAnalysis(w http.ResponseWriter, r *http.Request, page bool) (catalogue.Analysis, bool) {
	a, err := s.db.AnalysisBySlug(r.Context(), r.PathValue("slug"))
	switch {
	case err == nil && a.State == catalogue.AnalysisPublished:
		return a, true
	case err != nil && !isNotFound(err):
		s.fail(w, r, err)
	case !page:
		http.Error(w, "not published", http.StatusNotFound)
	case err == nil && a.State == catalogue.AnalysisWithdrawn:
		s.render(w, r, http.StatusGone, "message", "Withdrawn", message{Heading: "Withdrawn", Text: fmt.Sprintf("This work was withdrawn on %s.", a.Withdrawn.UTC().Format("2 January 2006")), Link: "/research", LinkText: "Published research"})
	default:
		s.notFound(w, r)
	}
	return a, false
}

func (s *Server) researchPage(w http.ResponseWriter, r *http.Request) {
	a, ok := s.publicAnalysis(w, r, true)
	if !ok {
		return
	}
	d, err := s.researchView(r.Context(), a, false)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Sent = r.URL.Query().Has("reported")
	if d.Live && s.visitors.full(a.ID, clientIP(r), s.readerLimit(r.Context())) {
		d.Full, d.Live, d.Comparing = true, false, false
	}
	s.render(w, r, 200, "research", a.Name, d)
}

// publicSource finds a source of a published analysis in a run, which
// visitors may watch live.
func (s *Server) publicSource(w http.ResponseWriter, r *http.Request) (catalogue.Analysis, catalogue.AnalysisSource, bool) {
	a, ok := s.publicAnalysis(w, r, false)
	if !ok {
		return a, catalogue.AnalysisSource{}, false
	}
	src, ok := a.Source(pathID(r, "source"))
	if _, inRun, _ := s.db.ActiveRun(r.Context(), a.ID); !ok || !inRun || !running(src.Stream) {
		http.Error(w, "not live now", http.StatusConflict)
		return a, src, false
	}
	return a, src, true
}

func (s *Server) researchLive(w http.ResponseWriter, r *http.Request) {
	a, src, ok := s.publicSource(w, r)
	if !ok {
		return
	}
	out, ok := s.visitors.enter(a.ID, clientIP(r), s.readerLimit(r.Context()))
	if !ok {
		http.Error(w, "as many visitors read this analysis live as may; try again later", http.StatusServiceUnavailable)
		return
	}
	defer out()
	rd := &reader{ch: make(chan []byte, 256), person: true, who: "visitor"}
	leave := s.live.join(src.Stream.EngineID(), a.EngineID(), rd)
	defer leave()
	serveReader(w, r, rd)
}

func (s *Server) researchData(w http.ResponseWriter, r *http.Request) {
	a, src, ok := s.publicSource(w, r)
	if !ok {
		return
	}
	what := r.PathValue("what")
	if what != "state" && what != "motifs" {
		http.Error(w, "unknown", http.StatusNotFound)
		return
	}
	raw, err := s.eng.Data(r.Context(), src.Stream.EngineID(), a.EngineID(), what, nil)
	if err != nil {
		http.Error(w, "not available", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(roundCoordsJSON(raw))
}

func (s *Server) researchCompareLive(w http.ResponseWriter, r *http.Request) {
	a, ok := s.publicAnalysis(w, r, false)
	if !ok {
		return
	}
	if !s.compares(a) {
		http.Error(w, "not compared now", http.StatusConflict)
		return
	}
	out, ok := s.visitors.enter(a.ID, clientIP(r), s.readerLimit(r.Context()))
	if !ok {
		http.Error(w, "as many visitors read this analysis live as may; try again later", http.StatusServiceUnavailable)
		return
	}
	defer out()
	rd := &reader{ch: make(chan []byte, 256), person: true, who: "visitor"}
	leave := s.live.joinGroup(a.GroupID(), rd)
	defer leave()
	serveReader(w, r, rd)
}

func (s *Server) researchCompareState(w http.ResponseWriter, r *http.Request) {
	a, ok := s.publicAnalysis(w, r, false)
	if !ok {
		return
	}
	if !s.compares(a) {
		http.Error(w, "not compared now", http.StatusConflict)
		return
	}
	raw, err := s.eng.GroupData(r.Context(), a.GroupID(), "state", nil)
	if err != nil {
		http.Error(w, "not available", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(roundCoordsJSON(raw))
}

// researchFile offers a file of a finished run's data set, when the member
// published the data sets with the analysis.
func (s *Server) researchFile(w http.ResponseWriter, r *http.Request) {
	a, ok := s.publicAnalysis(w, r, true)
	if !ok {
		return
	}
	run, err := s.db.Run(r.Context(), pathID(r, "run"))
	if err != nil || run.AnalysisID != a.ID || !a.PublishData {
		s.notFound(w, r)
		return
	}
	name := r.PathValue("file")
	rc, err := s.openDataset(run, name)
	if err != nil {
		s.notFound(w, r)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", datasetTypes[name])
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-run-%d-%s"`, a.Slug, run.Number, name))
	if name == fileJSON {
		copyRoundingCoords(w, rc) // the sources' places, as on the page
		return
	}
	io.Copy(w, rc)
}

// --- reports ---

// reportLimit keeps a visitor from flooding the master admin: five reports
// an hour from one address, fifty from all.
type reportLimit struct {
	mu   sync.Mutex
	from map[string][]time.Time
	all  []time.Time
}

var reports = reportLimit{from: map[string][]time.Time{}}

func recent(ts []time.Time, now time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < time.Hour {
			out = append(out, t)
		}
	}
	return out
}

func (l *reportLimit) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.all = recent(l.all, now)
	l.from[ip] = recent(l.from[ip], now)
	if len(l.all) >= 50 || len(l.from[ip]) >= 5 {
		return false
	}
	l.all = append(l.all, now)
	l.from[ip] = append(l.from[ip], now)
	return true
}

// clientIP is the visitor's address. Behind the local web server, which
// appends the address it saw to X-Forwarded-For, that last entry is the
// visitor's; earlier entries come from the client and are not trusted, nor
// is the header from anyone but the local web server.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
			parts := strings.Split(xff[len(xff)-1], ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return host
}

// researchReport takes a visitor's report about a public page and tells
// the master admins.
func (s *Server) researchReport(w http.ResponseWriter, r *http.Request) {
	a, ok := s.publicAnalysis(w, r, true)
	if !ok {
		return
	}
	back := "/research/" + a.Slug
	if !s.sameOrigin(r) {
		http.Error(w, "the form has expired; go back, reload and try again", http.StatusForbidden)
		return
	}
	msg, contact := strings.TrimSpace(r.PostFormValue("message")), strings.TrimSpace(r.PostFormValue("contact"))
	switch {
	case r.PostFormValue("website") != "": // a field people do not see
		http.Redirect(w, r, back+"?reported", http.StatusSeeOther)
		return
	case utf8.RuneCountInString(msg) < 10 || utf8.RuneCountInString(msg) > 2000:
		s.redirect(w, r, back, "Not sent: please say in 10 to 2000 characters what is wrong.")
		return
	case utf8.RuneCountInString(contact) > 200:
		s.redirect(w, r, back, "Not sent: the contact may have at most 200 characters.")
		return
	case !reports.allow(clientIP(r)):
		s.redirect(w, r, back, "Not sent: too many reports for now. Please try again in an hour.")
		return
	}
	id, err := s.db.AddReport(r.Context(), a.ID, a.Name, msg, contact)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	users, _ := s.db.Users(r.Context())
	for _, u := range users {
		if !u.Admin || u.Suspended {
			continue
		}
		body := fmt.Sprintf("Hello %s,\n\na visitor reported the published analysis \"%s\" by %s (report %d):\n\n%s\n\nContact given: %s\n\n%s/research/%s\n%s/admin\n",
			u.Name, a.Name, a.OwnerName, id, msg, or(contact, "none"), s.cfg.BaseURL, a.Slug, s.cfg.BaseURL)
		if err := s.mail.Send(u.Email, "Report: "+a.Name, body); err != nil {
			log.Printf("report mail: %v", err)
		}
	}
	http.Redirect(w, r, back+"?reported", http.StatusSeeOther)
}

// --- the master admin's side ---

func (s *Server) adminReportClose(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	if err := s.db.CloseReport(r.Context(), pathID(r, "report"), sess.User.ID); err != nil && !isNotFound(err) {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/admin", "Report closed.")
}

// adminWithdraw withdraws a published analysis, and tells its member.
func (s *Server) adminWithdraw(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, err := s.db.Analysis(r.Context(), pathID(r, "id"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	if reason == "" {
		s.redirect(w, r, "/admin", "Not withdrawn: give the member a reason.")
		return
	}
	if err := s.db.WithdrawAnalysis(r.Context(), a.ID, sess.User.ID, "by the master admin: "+reason); err != nil {
		if isNotFound(err) {
			s.redirect(w, r, "/admin", "It was not published.")
			return
		}
		s.fail(w, r, err)
		return
	}
	if owner, err := s.db.User(r.Context(), a.OwnerID); err == nil {
		body := fmt.Sprintf("Hello %s,\n\nthe master admin has withdrawn your published analysis \"%s\". The reason given:\n\n%s\n\nIt is private again and otherwise unchanged; you can publish it again once the matter is settled.\n\n%s%s\n",
			owner.Name, a.Name, reason, s.cfg.BaseURL, analysisPath(a))
		if err := s.mail.Send(owner.Email, "Withdrawn: "+a.Name, body); err != nil {
			log.Printf("withdraw mail: %v", err)
		}
	}
	s.redirect(w, r, "/admin", "Withdrawn; its member has been told.")
}
