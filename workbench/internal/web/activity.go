package web

import (
	"net/http"
	"net/url"
	"strconv"

	"github.com/vrontier/listen-workbench/internal/catalogue"
)

// Under AI: Activity shows a member every access made with their tokens,
// through MCP and the data stream, over the last 30 days; MCP shows the
// tools with examples, the guide for assistants, and how to connect.

// activityPage is how many notes the page loads at once; the browser
// filters and sorts them.
const activityPage = 500

type activityData struct {
	Summary []catalogue.TokenActivity
	Tokens  []catalogue.APIToken
	Rows    []catalogue.Access
	Older   string // the address of the next older page, or ""
	Newest  string // the address of the newest page, when not on it
}

type mcpData struct {
	Tools []toolDoc
	Guide string
	Base  string
}

func (s *Server) mcpPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ids := s.guideIDsOf(r.Context(), sess.User.ID)
	s.render(w, r, 200, "ai-mcp", "AI: MCP", mcpData{Tools: toolDocs(ids), Guide: mcpGuide(&ids, true), Base: s.cfg.BaseURL})
}

func (s *Server) activityPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	ctx := r.Context()
	q := r.URL.Query()
	f := catalogue.AccessFilter{Limit: activityPage + 1}
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
	// The filters live in the address too, so that a link can choose a
	// token; the page keeps them when it goes back in time.
	page := func(before int64) string {
		v := url.Values{}
		for _, k := range []string{"token", "via", "kind", "status", "what", "q", "sort"} {
			if x := q.Get(k); x != "" {
				v.Set(k, x)
			}
		}
		if before > 0 {
			v.Set("before", strconv.FormatInt(before, 10))
		}
		if len(v) == 0 {
			return "/ai/activity"
		}
		return "/ai/activity?" + v.Encode()
	}
	if len(d.Rows) > activityPage {
		d.Rows = d.Rows[:activityPage]
		d.Older = page(d.Rows[len(d.Rows)-1].ID)
	}
	if f.Before > 0 {
		d.Newest = page(0)
	}
	s.render(w, r, 200, "ai-activity", "AI: activity", d)
}
