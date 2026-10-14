package web

import (
	"fmt"
	"log"
	"net/http"
	"net/mail"
	"strings"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

type adminData struct {
	Users       []catalogue.User
	Invitations []catalogue.Invitation
	Audit       []catalogue.AuditEntry
	Streams     []catalogue.Stream
	EngineReady bool
	EngineInfo  engine.Info
	EngineNote  string
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
	if _, err := mail.ParseAddress(email); err != nil || strings.ContainsAny(email, " <>") {
		d.Err = "That does not look like an email address."
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
	}
	if d, err = s.adminData(r); err != nil {
		s.fail(w, r, err)
		return
	}
	d.Link = link
	s.render(w, r, 200, "admin", "Admin", d)
}
