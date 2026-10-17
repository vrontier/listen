package web

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/mail"
)

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	if r2, ok := s.loadSession(r); ok {
		s.welcome(w, r2)
		return
	}
	s.render(w, r, 200, "home", "", nil)
}

type loginData struct {
	Email string
	Next  string
	Sent  bool
	Err   string
}

func (d loginData) FormError() string { return d.Err }

// safeNext keeps a redirect target on this site.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

func (s *Server) loginForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, 200, "login", "Log in", loginData{Next: r.URL.Query().Get("next")})
}

// loginSend mails a login link. It answers the same whether or not the
// address belongs to a member, so the form does not reveal who is one.
func (s *Server) loginSend(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	email := strings.TrimSpace(r.PostFormValue("email"))
	next := safeNext(r.PostFormValue("next"))
	if !mail.ValidAddress(email) {
		s.render(w, r, 400, "login", "Log in", loginData{Email: email, Next: next, Err: "That is not a complete email address. Please check it, for example the dot before the ending."})
		return
	}
	u, err := s.db.UserByEmail(r.Context(), email)
	switch {
	case err == nil && !u.Suspended:
		recent, err := s.db.RecentLoginLink(r.Context(), u.ID, time.Now().Add(-time.Minute))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if recent {
			log.Printf("login link not sent: member %d got one less than a minute ago", u.ID)
			break
		}
		token, err := s.db.LoginLink(r.Context(), u.ID, loginLinkLife)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		link := s.cfg.BaseURL + "/login/" + token + "?next=" + urlQuery(next)
		body := fmt.Sprintf("Hello %s,\n\nto log in to the %s, open this link within %d minutes:\n\n%s\n\nIf you did not ask for it, ignore this message.\n",
			u.Name, s.cfg.SiteName, int(loginLinkLife.Minutes()), link)
		if err := s.mail.Send(u.Email, "Your login link", body); err != nil {
			log.Printf("login mail to member %d: %v", u.ID, err)
		} else {
			log.Printf("login mail sent to member %d", u.ID)
		}
	case err == nil:
		log.Printf("login link not sent: member %d is suspended", u.ID)
	case isNotFound(err):
		// Not logged with the address: it may be anyone's.
		log.Printf("login link not sent: no member has that address")
	default:
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "login", "Log in", loginData{Email: email, Sent: true})
}

// loginConfirm shows a button rather than logging in on GET, so that a
// mail program that opens links to preview them does not use up the link.
func (s *Server) loginConfirm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, 200, "login-confirm", "Log in", map[string]string{"Token": r.PathValue("token"), "Next": r.URL.Query().Get("next")})
}

func (s *Server) loginUse(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	u, err := s.db.UseLoginLink(r.Context(), r.PathValue("token"))
	if errors.Is(err, catalogue.ErrExpired) || err == nil && u.Suspended {
		s.render(w, r, 400, "message", "Log in", message{Heading: "This link no longer works",
			Text: "Login links work once and for a short time. Ask for a new one.", Link: "/login", LinkText: "Log in"})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, safeNext(r.PostFormValue("next")), http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.db.EndSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

type inviteData struct {
	Token string
	Email string
	Name  string
	Err   string
}

func (d inviteData) FormError() string { return d.Err }

func (s *Server) inviteForm(w http.ResponseWriter, r *http.Request) {
	email, err := s.db.OpenInvitation(r.Context(), r.PathValue("token"))
	if err != nil {
		s.render(w, r, 400, "message", "Invitation", message{Heading: "This invitation no longer works",
			Text: "It has been used or has expired. Ask whoever invited you for a new one."})
		return
	}
	s.render(w, r, 200, "invite", "Invitation", inviteData{Token: r.PathValue("token"), Email: email})
}

func (s *Server) inviteAccept(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	token := r.PathValue("token")
	email, err := s.db.OpenInvitation(r.Context(), token)
	if err != nil {
		s.render(w, r, 400, "message", "Invitation", message{Heading: "This invitation no longer works",
			Text: "It has been used or has expired. Ask whoever invited you for a new one."})
		return
	}
	name := strings.TrimSpace(r.PostFormValue("name"))
	if n := utf8.RuneCountInString(name); n < 2 || n > 60 {
		s.render(w, r, 400, "invite", "Invitation", inviteData{Token: token, Email: email, Name: name,
			Err: "Please give a display name of 2 to 60 characters."})
		return
	}
	if r.PostFormValue("privacy") != "on" {
		s.render(w, r, 400, "invite", "Invitation", inviteData{Token: token, Email: email, Name: name,
			Err: "Please confirm that you have read what the Workbench keeps about you."})
		return
	}
	u, err := s.db.AcceptInvitation(r.Context(), token, name)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			s.render(w, r, 400, "message", "Invitation", message{Heading: "You are a member already",
				Text: "This address has an account. Log in instead.", Link: "/login", LinkText: "Log in"})
			return
		}
		s.fail(w, r, err)
		return
	}
	if err := s.startSession(w, r, u); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/sources", "Welcome, "+u.Name+". Register your first source to begin.")
}

func urlQuery(s string) string {
	return strings.NewReplacer("%", "%25", "&", "%26", "#", "%23", "?", "%3F", " ", "%20", "+", "%2B").Replace(s)
}
