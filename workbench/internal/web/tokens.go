package web

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// The member's tokens, under AI, Access (members-and-streams.md, Tokens).

// maxTokens bounds the valid tokens of one member.
const maxTokens = 20

type tokensData struct {
	Tokens  []catalogue.APIToken
	Limits  catalogue.TokenLimits // what the form for a new token offers
	Secret  string                // the token just made, shown this once
	Base    string
	Name    string
	Example int64 // an analysis of the member's, for the examples
	Err     string
}

func (d tokensData) FormError() string { return d.Err }

func (s *Server) tokensData(r *http.Request) (tokensData, error) {
	sess, _ := sessionFrom(r)
	d := tokensData{Base: s.cfg.BaseURL, Example: 1, Limits: catalogue.DefaultTokenLimits}
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
	s.render(w, r, 200, "ai-access", "AI: access", d)
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
	scopes, limits, why := tokenForm(r)
	d.Limits = limits
	valid := 0
	for _, t := range d.Tokens {
		if t.Revoked.IsZero() {
			valid++
		}
	}
	switch {
	case utf8.RuneCountInString(d.Name) < 2 || utf8.RuneCountInString(d.Name) > 60:
		d.Err = "Not made: give the token a name of 2 to 60 characters, saying where it is used."
	case why != "":
		d.Err = "Not made: " + why
	case valid >= maxTokens:
		d.Err = "Not made: you have " + strconv.Itoa(maxTokens) + " valid tokens. Revoke one you no longer use."
	}
	if d.Err != "" {
		s.render(w, r, 400, "ai-access", "AI: access", d)
		return
	}
	secret, _, err := s.db.CreateAPIToken(r.Context(), sess.User.ID, d.Name, scopes, limits)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Tokens, _ = s.db.APITokensOf(r.Context(), sess.User.ID)
	d.Secret, d.Name, d.Limits = secret, "", catalogue.DefaultTokenLimits
	s.render(w, r, 200, "ai-access", "AI: access", d)
}

func (s *Server) tokenRevoke(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PathValue("token"), 10, 64)
	if err := s.db.RevokeAPIToken(r.Context(), sess.User.ID, id); err != nil {
		if isNotFound(err) {
			s.redirect(w, r, "/ai/access", "That token was revoked already.")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/ai/access", "Token revoked. Programs using it are refused from now on.")
}

// limitFields are the form's names of a token's limits.
var limitFields = []struct {
	Name string
	Max  int
	Get  func(*catalogue.TokenLimits) *int
}{
	{"run_hours", 24 * 365, func(l *catalogue.TokenLimits) *int { return &l.RunHours }},
	{"runs_per_day", 1000, func(l *catalogue.TokenLimits) *int { return &l.RunsPerDay }},
	{"runs_at_once", 100, func(l *catalogue.TokenLimits) *int { return &l.RunsAtOnce }},
	{"previews_per_day", 1000, func(l *catalogue.TokenLimits) *int { return &l.PreviewsPerDay }},
	{"changes_per_day", 10000, func(l *catalogue.TokenLimits) *int { return &l.ChangesPerDay }},
}

// tokenForm reads what a token may do and its limits; an empty limit is
// none. why says what is wrong.
func tokenForm(r *http.Request) ([]string, catalogue.TokenLimits, string) {
	var scopes []string
	for _, sc := range []string{catalogue.ScopeStream, catalogue.ScopeMCP, catalogue.ScopeControl} {
		if r.PostFormValue(sc) == "on" {
			scopes = append(scopes, sc)
		}
	}
	var l catalogue.TokenLimits
	for _, f := range limitFields {
		v := strings.TrimSpace(r.PostFormValue(f.Name))
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > f.Max {
			return scopes, l, fmt.Sprintf("a limit is a whole number from 1 to %d, or empty for none.", f.Max)
		}
		*f.Get(&l) = n
	}
	switch {
	case len(scopes) == 0:
		return scopes, l, "choose what the token is for."
	case slices.Contains(scopes, catalogue.ScopeControl) && !slices.Contains(scopes, catalogue.ScopeMCP):
		return scopes, l, "control works through MCP; tick MCP as well."
	}
	return scopes, l, ""
}

// tokenUpdate changes what a valid token may do and its limits.
func (s *Server) tokenUpdate(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PathValue("token"), 10, 64)
	scopes, limits, why := tokenForm(r)
	if why != "" {
		s.redirect(w, r, "/ai/access", "Not changed: "+why)
		return
	}
	if err := s.db.UpdateAPIToken(r.Context(), sess.User.ID, id, scopes, limits); err != nil {
		if isNotFound(err) {
			s.redirect(w, r, "/ai/access", "That token is revoked or not yours.")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/ai/access", "Token changed. It counts from its next call on.")
}
