package web

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

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
	Link        string // a just-made invitation link, shown once
	Err         string
}

func (d adminData) FormError() string { return d.Err }

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
	return d, nil
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
	var c catalogue.ServerCap
	if d.CapForm.CPU != "" {
		v, err := strconv.ParseFloat(strings.Replace(d.CapForm.CPU, ",", ".", 1), 64)
		switch {
		case err != nil || v <= 0:
			d.CapErrs["cpu"] = "a number of cores above 0, for example 4 or 2.5"
		case d.Host.Cores > 0 && v > float64(d.Host.Cores):
			d.CapErrs["cpu"] = fmt.Sprintf("the host has %d cores", d.Host.Cores)
		default:
			c.CPUCores = v
		}
	}
	if d.CapForm.Memory != "" {
		v, err := strconv.Atoi(d.CapForm.Memory)
		switch {
		case err != nil || v <= 0:
			d.CapErrs["memory"] = "megabytes, a whole number above 0"
		case d.Host.MemoryMB > 0 && v > d.Host.MemoryMB:
			d.CapErrs["memory"] = fmt.Sprintf("the host has %d MB", d.Host.MemoryMB)
		default:
			c.MemoryMB = v
		}
	}
	if len(d.CapErrs) > 0 {
		d.Err = "Please check the server cap."
		s.render(w, r, 400, "admin", "Admin", d)
		return
	}
	if err := s.db.SetServerCap(r.Context(), sess.User.ID, c); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/admin", "Server cap saved.")
}
