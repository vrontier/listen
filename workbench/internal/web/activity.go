package web

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// The MCP page shows a member every access made with their tokens,
// through MCP and the data stream, over the last 30 days; the MCP tools
// with examples; the guide for assistants; and how to connect.

const activityPage = 100

type activityData struct {
	Summary []catalogue.TokenActivity
	Tokens  []catalogue.APIToken
	Rows    []catalogue.Access
	Filter  catalogue.AccessFilter
	Older   string // the address of the next older page, or ""
	Newest  string // the address of the newest page, when not on it
	Tools   []toolDoc
	Guide   string
	Base    string
}

func (s *Server) mcpPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ctx := r.Context()
	q := r.URL.Query()
	f := catalogue.AccessFilter{Limit: activityPage + 1}
	f.TokenID, _ = strconv.ParseInt(q.Get("token"), 10, 64)
	if v := q.Get("via"); v == "mcp" || v == "stream" {
		f.Via = v
	}
	f.Failed = q.Get("failed") == "on"
	f.Before, _ = strconv.ParseInt(q.Get("before"), 10, 64)
	var d activityData
	var err error
	if d.Summary, err = s.db.ActivityOf(ctx, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Tokens, err = s.db.APITokensOf(ctx, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if d.Rows, err = s.db.AccessOf(ctx, sess.User.ID, f); err != nil {
		s.fail(w, r, err)
		return
	}
	page := func(before int64) string {
		v := url.Values{}
		if f.TokenID > 0 {
			v.Set("token", strconv.FormatInt(f.TokenID, 10))
		}
		if f.Via != "" {
			v.Set("via", f.Via)
		}
		if f.Failed {
			v.Set("failed", "on")
		}
		if before > 0 {
			v.Set("before", strconv.FormatInt(before, 10))
		}
		if len(v) == 0 {
			return "/account/mcp"
		}
		return "/account/mcp?" + v.Encode()
	}
	if len(d.Rows) > activityPage {
		d.Rows = d.Rows[:activityPage]
		d.Older = page(d.Rows[len(d.Rows)-1].ID)
	}
	if f.Before > 0 {
		d.Newest = page(0)
	}
	f.Limit = activityPage
	d.Filter = f
	ids := s.guideIDsOf(ctx, sess.User.ID)
	d.Tools = toolDocs(ids)
	d.Guide = mcpGuide(&ids)
	d.Base = s.cfg.BaseURL
	s.render(w, r, 200, "mcp", "MCP", d)
}
