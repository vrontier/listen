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

// The member's own settings (members-and-streams.md, Account): the display
// name, changed at once; the email address, which is the login and changes
// only once a link sent to the new address is used; the theme.

const (
	emailChangeLife  = 24 * time.Hour
	emailChangesHour = 5 // requests a member may make in an hour
)

type settingsData struct {
	User    catalogue.User
	Name    string
	Email   string
	Pending catalogue.EmailChange
	Waiting bool
	Err     string
}

func (d settingsData) FormError() string { return d.Err }

func (s *Server) settingsOf(r *http.Request) (settingsData, error) {
	sess, _ := sessionFrom(r)
	d := settingsData{User: sess.User, Name: sess.User.Name}
	var err error
	d.Pending, d.Waiting, err = s.db.PendingEmailChange(r.Context(), sess.User.ID)
	return d, err
}

func (s *Server) accountPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.settingsOf(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "account", "Account", d)
}

func (s *Server) accountName(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	name := strings.TrimSpace(r.PostFormValue("name"))
	if n := utf8.RuneCountInString(name); n < 2 || n > 60 {
		d, err := s.settingsOf(r)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Name, d.Err = name, "Not changed: give a display name of 2 to 60 characters."
		s.render(w, r, 400, "account", "Account", d)
		return
	}
	if err := s.db.SetUserName(r.Context(), sess.User.ID, name); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/account", "Your name is now "+name+".")
}

// accountEmail sends a link to the new address; the old one stays the
// login until the link is used.
func (s *Server) accountEmail(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ctx := r.Context()
	email := strings.TrimSpace(r.PostFormValue("email"))
	d, err := s.settingsOf(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Email = email
	refuse := func(why string) {
		d.Err = "Not changed: " + why
		s.render(w, r, 400, "account", "Account", d)
	}
	if !mail.ValidAddress(email) {
		refuse("that is not a complete email address. Please check it, for example the dot before the ending.")
		return
	}
	if strings.EqualFold(email, sess.User.Email) {
		refuse("that is your address already.")
		return
	}
	n, err := s.db.EmailChangesSince(ctx, sess.User.ID, time.Now().Add(-time.Hour))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if n >= emailChangesHour {
		refuse(fmt.Sprintf("you asked %d times in the last hour. Try again later.", n))
		return
	}
	token, err := s.db.RequestEmailChange(ctx, sess.User.ID, email, emailChangeLife)
	if errors.Is(err, catalogue.ErrEmailTaken) {
		// Not saying whose: the form must not tell who is a member.
		refuse("that address cannot be used for your account.")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.db.NoteEmailRequest(ctx, sess.User.ID, email); err != nil {
		s.fail(w, r, err)
		return
	}
	link := s.cfg.BaseURL + "/account/email/" + token
	body := fmt.Sprintf("Hello %s,\n\nyou asked to log in to the %s with this address from now on. To confirm it, open this link within %d hours:\n\n%s\n\nUntil then you log in with your present address. If you did not ask for it, ignore this message.\n",
		sess.User.Name, s.cfg.SiteName, int(emailChangeLife.Hours()), link)
	if err := s.mail.Send(email, "Confirm your new address", body); err != nil {
		log.Printf("account: confirming the new address of member %d: %v", sess.User.ID, err)
		s.redirect(w, r, "/account", "The mail to "+email+" could not be sent. Please try again later.")
		return
	}
	log.Printf("account: confirmation of a new address sent for member %d", sess.User.ID)
	s.redirect(w, r, "/account", "We sent a link to "+email+". Your address changes once you open it; until then you log in with "+sess.User.Email+".")
}

func (s *Server) accountEmailCancel(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	if err := s.db.CancelEmailChange(r.Context(), sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/account", "The change of address is cancelled; the link no longer works.")
}

func (s *Server) emailChangeGone(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, 400, "message", "New address", message{Heading: "This link no longer works",
		Text: "A link to confirm a new address works once and for 24 hours, and only the newest one asked for. Ask for a new one under Account.", Link: "/account", LinkText: "Account"})
}

// emailConfirmForm shows a button rather than changing on GET, so that a
// mail program that opens links to preview them does not use up the link.
func (s *Server) emailConfirmForm(w http.ResponseWriter, r *http.Request) {
	email, err := s.db.OpenEmailChange(r.Context(), r.PathValue("token"))
	if err != nil {
		s.emailChangeGone(w, r)
		return
	}
	s.render(w, r, 200, "email-confirm", "New address", map[string]string{"Token": r.PathValue("token"), "Email": email})
}

// emailConfirm changes the address, ends every session of the member,
// tells the old address, and logs in the browser that confirmed.
func (s *Server) emailConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	u, old, err := s.db.ConfirmEmailChange(r.Context(), r.PathValue("token"))
	if errors.Is(err, catalogue.ErrExpired) {
		s.emailChangeGone(w, r)
		return
	}
	if errors.Is(err, catalogue.ErrEmailTaken) {
		s.render(w, r, 400, "message", "New address", message{Heading: "The address cannot be used",
			Text: "It cannot be used for your account; your address stays as it was.", Link: "/account", LinkText: "Account"})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	body := fmt.Sprintf("Hello %s,\n\nthe address of your account at the %s was changed from this one to %s at %s UTC. You log in with the new address from now on, and every session was ended.\n\nIf you did not do this, reply to this mail or tell the Workbench's admin at once.\n",
		u.Name, s.cfg.SiteName, u.Email, time.Now().UTC().Format("2006-01-02 15:04"))
	if err := s.mail.Send(old, "Your address was changed", body); err != nil {
		log.Printf("account: telling the old address of member %d: %v", u.ID, err)
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: c.Value, Path: "/", MaxAge: -1})
	}
	if err := s.startSession(w, r, u); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/account", "Your address is now "+u.Email+". Every other session has ended; log in there again with the new address.")
}
