package web

import (
	"context"
	"net/http"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
)

// The AI pages (members-and-streams.md, Tokens): an overview of how a
// member gives an AI assistant access, then Access (the tokens), MCP (the
// tools, the guide and how to connect) and Activity (every access).

type aiLink struct{ Path, Label string }

// aiPages are the AI pages in the order of their menu.
var aiPages = []aiLink{{"/ai", "Overview"}, {"/ai/access", "Access"}, {"/ai/mcp", "MCP"}, {"/ai/activity", "Activity"}}

// tokenCounts say where a member stands with their tokens.
type tokenCounts struct {
	Tokens   int // valid tokens
	MCP      int // of them, those for MCP
	Control  int // of them, those allowed control
	ByTokens int // runs going on that a token started
	Accesses int // with them in the last 24 hours
	Last     time.Time
}

func (s *Server) tokenCountsOf(ctx context.Context, user int64) (tokenCounts, error) {
	var c tokenCounts
	tokens, err := s.db.APITokensOf(ctx, user)
	if err != nil {
		return c, err
	}
	activity, err := s.db.ActivityOf(ctx, user)
	if err != nil {
		return c, err
	}
	for _, t := range tokens {
		if !t.Revoked.IsZero() {
			continue
		}
		c.Tokens++
		if t.Has(catalogue.ScopeMCP) {
			c.MCP++
		}
		if t.Has(catalogue.ScopeControl) {
			c.Control++
			if _, active, err := s.db.TokenRuns(ctx, t.ID, time.Now()); err == nil {
				c.ByTokens += active
			}
		}
	}
	for _, a := range activity {
		c.Accesses += a.Day
		if a.Last.After(c.Last) {
			c.Last = a.Last
		}
	}
	return c, nil
}

type aiData struct {
	tokenCounts
	Base string
}

func (s *Server) aiPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	c, err := s.tokenCountsOf(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "ai", "AI", aiData{tokenCounts: c, Base: s.cfg.BaseURL})
}
