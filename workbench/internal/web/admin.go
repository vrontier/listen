package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
	"github.com/vrontier/listen-workbench/internal/mail"
)

type adminData struct {
	Users       []catalogue.User
	Invitations []catalogue.Invitation
	Audit       []catalogue.AuditEntry
	Streams     []catalogue.Stream
	EngineReady bool
	EngineInfo  engine.Info
	EngineNote  string
	Cap         catalogue.ServerCap
	Used        engine.Usage
	Host        engine.Host
	CapForm     struct{ CPU, Memory string }
	CapErrs     map[string]string
	MemberCap   catalogue.ServerCap // the default
	Members     []memberUse
	Day         []usageBar // the engine's CPU per hour
	DayMax      float64
	Reports     []catalogue.Report
	Published   []catalogue.Analysis
	Readers     int    // visitors who may read one published analysis live at once
	Link        string // a just-made invitation link, shown once
	Err         string
}

func (d adminData) FormError() string { return d.Err }

// memberUse is a member on the admin page, with their use and cap.
type memberUse struct {
	catalogue.User
	Now    catalogue.Usage
	Cap    catalogue.ServerCap
	OwnCap bool
	Disk   int64
}

func (s *Server) adminData(r *http.Request) (adminData, error) {
	var d adminData
	var err error
	if d.Users, err = s.db.Users(r.Context()); err != nil {
		return d, err
	}
	if d.Invitations, err = s.db.Invitations(r.Context()); err != nil {
		return d, err
	}
	if d.Audit, err = s.db.AuditAll(r.Context()); err != nil {
		return d, err
	}
	if d.Streams, err = s.db.AllStreams(r.Context()); err != nil {
		return d, err
	}
	if d.Reports, err = s.db.OpenReports(r.Context()); err != nil {
		return d, err
	}
	if d.Published, err = s.db.PublishedAnalyses(r.Context()); err != nil {
		return d, err
	}
	if d.Readers, err = s.db.PublicReaders(r.Context()); err != nil {
		return d, err
	}
	d.EngineReady, d.EngineNote, d.EngineInfo = s.sync.status()
	d.Used, d.Host = s.sync.used()
	if d.Cap, err = s.db.ServerCap(r.Context()); err != nil {
		return d, err
	}
	if d.Cap.CPUCores > 0 {
		d.CapForm.CPU = strconv.FormatFloat(d.Cap.CPUCores, 'g', -1, 64)
	}
	if d.Cap.MemoryMB > 0 {
		d.CapForm.Memory = strconv.Itoa(d.Cap.MemoryMB)
	}
	d.CapErrs = map[string]string{}
	if d.MemberCap, err = s.db.DefaultMemberCap(r.Context()); err != nil {
		return d, err
	}
	for _, u := range d.Users {
		m := memberUse{User: u, Now: s.acct.now(u.ID)}
		if m.Cap, m.OwnCap, err = s.db.MemberCap(r.Context(), u.ID); err != nil {
			return d, err
		}
		if m.Disk, err = s.diskBytes(r.Context(), u.ID); err != nil {
			return d, err
		}
		d.Members = append(d.Members, m)
	}
	now := time.Now().UTC()
	samples, err := s.db.UsageSince(r.Context(), 0, now.Add(-25*time.Hour))
	if err != nil {
		return d, err
	}
	d.Day, d.DayMax = bars(samples, now.Truncate(time.Hour).Add(time.Hour), time.Hour, 24, "Jan 2 15:00")
	return d, nil
}

// parseCap reads a cap from a form; empty fields leave that part unset.
func parseCap(r *http.Request, host engine.Host, errs map[string]string) catalogue.ServerCap {
	var c catalogue.ServerCap
	if v := strings.TrimSpace(r.PostFormValue("cpu_cores")); v != "" {
		f, err := strconv.ParseFloat(strings.Replace(v, ",", ".", 1), 64)
		switch {
		case err != nil || f <= 0:
			errs["cpu"] = "a number of cores above 0, for example 4 or 2.5"
		case host.Cores > 0 && f > float64(host.Cores):
			errs["cpu"] = fmt.Sprintf("the host has %d cores", host.Cores)
		default:
			c.CPUCores = f
		}
	}
	if v := strings.TrimSpace(r.PostFormValue("memory_mb")); v != "" {
		n, err := strconv.Atoi(v)
		switch {
		case err != nil || n <= 0:
			errs["memory"] = "megabytes, a whole number above 0"
		case host.MemoryMB > 0 && n > host.MemoryMB:
			errs["memory"] = fmt.Sprintf("the host has %d MB", host.MemoryMB)
		default:
			c.MemoryMB = n
		}
	}
	return c
}

// adminMemberCap sets the cap of every member without one of their own.
func (s *Server) adminMemberCap(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	_, host := s.sync.used()
	errs := map[string]string{}
	c := parseCap(r, host, errs)
	if len(errs) > 0 {
		s.redirect(w, r, "/manage", "Member cap not saved: "+strings.Join(values(errs), "; ")+".")
		return
	}
	if err := s.db.SetDefaultMemberCap(r.Context(), sess.User.ID, c); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/manage", "Member cap saved.")
}

// adminOneCap gives one member a cap of their own, or with "default"
// returns them to the default.
func (s *Server) adminOneCap(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PathValue("user"), 10, 64)
	u, err := s.db.User(r.Context(), id)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if r.PostFormValue("default") != "" {
		if err := s.db.SetMemberCap(r.Context(), sess.User.ID, u.ID, nil); err != nil {
			s.fail(w, r, err)
			return
		}
		s.redirect(w, r, "/manage", u.Name+" has the default cap again.")
		return
	}
	_, host := s.sync.used()
	errs := map[string]string{}
	c := parseCap(r, host, errs)
	if len(errs) > 0 {
		s.redirect(w, r, "/manage", "Cap of "+u.Name+" not saved: "+strings.Join(values(errs), "; ")+".")
		return
	}
	if err := s.db.SetMemberCap(r.Context(), sess.User.ID, u.ID, &c); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/manage", "Cap of "+u.Name+" saved.")
}

func values(m map[string]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (s *Server) adminPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.adminData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "admin", "Admin", d)
}

func (s *Server) adminInvite(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	email := strings.TrimSpace(r.PostFormValue("email"))
	d, err := s.adminData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !mail.ValidAddress(email) {
		d.Err = "That is not a complete email address. Please check it, for example the dot before the ending."
		s.render(w, r, 400, "admin", "Admin", d)
		return
	}
	if _, err := s.db.UserByEmail(r.Context(), email); err == nil {
		d.Err = email + " is a member already."
		s.render(w, r, 400, "admin", "Admin", d)
		return
	}
	token, err := s.db.Invite(r.Context(), email, sess.User.ID, inviteLife)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	link := s.cfg.BaseURL + "/invite/" + token
	body := fmt.Sprintf("Hello,\n\n%s invites you to the %s, where researchers analyse live audio streams with their own filters and motifs.\n\n"+
		"To accept, open this link within %d days:\n\n%s\n\nIf you do not want to take part, ignore this message.\n",
		sess.User.Name, s.cfg.SiteName, int(inviteLife.Hours()/24), link)
	if err := s.mail.Send(email, "Invitation to the "+s.cfg.SiteName, body); err != nil {
		log.Printf("invitation mail: %v", err)
	} else {
		log.Printf("invitation mail sent")
	}
	if d, err = s.adminData(r); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Link = link
	s.render(w, r, 200, "admin", "Admin", d)
}

// adminCap sets the share of the host the Workbench may use. Empty fields
// remove that part of the cap.
func (s *Server) adminCap(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	d, err := s.adminData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.CapForm.CPU = strings.TrimSpace(r.PostFormValue("cpu_cores"))
	d.CapForm.Memory = strings.TrimSpace(r.PostFormValue("memory_mb"))
	c := parseCap(r, d.Host, d.CapErrs)
	if len(d.CapErrs) > 0 {
		d.Err = "Please check the server cap."
		s.render(w, r, 400, "admin", "Admin", d)
		return
	}
	if err := s.db.SetServerCap(r.Context(), sess.User.ID, c); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/manage", "Server cap saved.")
}

// adminReaders sets how many visitors may read one published analysis live
// at once.
func (s *Server) adminReaders(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	n, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("readers")))
	if err != nil || n < 1 || n > 10000 {
		s.redirect(w, r, "/manage", "Not saved: give a whole number of visitors from 1 to 10000.")
		return
	}
	if err := s.db.SetPublicReaders(r.Context(), sess.User.ID, n); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/manage", "Saved: "+strconv.Itoa(n)+" visitors may read one published analysis live at once.")
}
