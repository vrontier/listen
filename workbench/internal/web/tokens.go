package web

import (
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// The member's tokens (members-and-streams.md, Tokens).

// maxTokens bounds the valid tokens of one member.
const maxTokens = 20

type tokensData struct {
	Tokens  []catalogue.APIToken
	Secret  string // the token just made, shown this once
	Base    string
	Name    string
	Example int64 // an analysis of the member's, for the examples
	Err     string
}

func (d tokensData) FormError() string { return d.Err }

func (s *Server) tokensData(r *http.Request) (tokensData, error) {
	sess, _ := sessionFrom(r)
	d := tokensData{Base: s.cfg.BaseURL, Example: 1}
	var err error
	if d.Tokens, err = s.db.APITokensOf(r.Context(), sess.User.ID); err != nil {
		return d, err
	}
	if as, err := s.db.AnalysesOf(r.Context(), sess.User.ID); err == nil && len(as) > 0 {
		d.Example = as[0].ID
	}
	return d, nil
}

func (s *Server) tokensPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.tokensData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "tokens", "Tokens", d)
}

// tokenCreate makes a token and shows it once, on the page itself rather
// than after a redirect, so that it is never stored on its way.
func (s *Server) tokenCreate(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	d, err := s.tokensData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Name = strings.TrimSpace(r.PostFormValue("name"))
	var scopes []string
	for _, sc := range []string{catalogue.ScopeStream, catalogue.ScopeMCP} {
		if r.PostFormValue(sc) == "on" {
			scopes = append(scopes, sc)
		}
	}
	valid := 0
	for _, t := range d.Tokens {
		if t.Revoked.IsZero() {
			valid++
		}
	}
	switch {
	case utf8.RuneCountInString(d.Name) < 2 || utf8.RuneCountInString(d.Name) > 60:
		d.Err = "Not made: give the token a name of 2 to 60 characters, saying where it is used."
	case len(scopes) == 0:
		d.Err = "Not made: choose what the token is for."
	case valid >= maxTokens:
		d.Err = "Not made: you have " + strconv.Itoa(maxTokens) + " valid tokens. Revoke one you no longer use."
	}
	if d.Err != "" {
		s.render(w, r, 400, "tokens", "Tokens", d)
		return
	}
	secret, _, err := s.db.CreateAPIToken(r.Context(), sess.User.ID, d.Name, scopes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Tokens, _ = s.db.APITokensOf(r.Context(), sess.User.ID)
	d.Secret, d.Name = secret, ""
	s.render(w, r, 200, "tokens", "Tokens", d)
}

func (s *Server) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PathValue("token"), 10, 64)
	if err := s.db.RevokeAPIToken(r.Context(), sess.User.ID, id); err != nil {
		if isNotFound(err) {
			s.redirect(w, r, "/account/tokens", "That token was revoked already.")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/account/tokens", "Token revoked. Programs using it are refused from now on.")
}
