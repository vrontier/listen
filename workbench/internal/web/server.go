// Package web is the Workbench's HTTP side: pages rendered on the server,
// the forms of the member area, and the live data of an analysis.
package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
	"github.com/vrontier/listen-workbench/internal/mail"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

const (
	sessionCookie = "low_session"
	sessionLife   = 30 * 24 * time.Hour
	loginLinkLife = 20 * time.Minute
	inviteLife    = 14 * 24 * time.Hour
)

// Config is what the Workbench is started with.
type Config struct {
	BaseURL    string // as visitors reach it, e.g. http://low.home.arpa
	SiteName   string
	DatasetDir string // where runs' data sets are kept; empty: none are
}

// Server is the Workbench's web side.
type Server struct {
	cfg    Config
	db     *catalogue.DB
	eng    *engine.Client
	mail   mail.Sender
	sync   *engineSync
	live   *fanout
	data   *datasets
	acct   *accounting
	pages  map[string]*template.Template
	mux    *http.ServeMux
	secure bool

	probesMu sync.Mutex
	probes   map[string]probeEntry // "user\x00input"
}

type probeEntry struct {
	at     time.Time
	result engine.ProbeResult
}

// New builds the server. Start begins following the engine.
func New(cfg Config, db *catalogue.DB, eng *engine.Client, m mail.Sender) (*Server, error) {
	if cfg.SiteName == "" {
		cfg.SiteName = "Listening Observatory Workbench"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	s := &Server{
		cfg: cfg, db: db, eng: eng, mail: m,
		sync: newEngineSync(eng, db), live: newFanout(eng),
		secure: strings.HasPrefix(cfg.BaseURL, "https://"),
		probes: map[string]probeEntry{},
	}
	s.data = newDatasets(s, cfg.DatasetDir)
	s.sync.onState = s.data.noteState
	s.acct = newAccounting(s)
	s.sync.onUsage = s.acct.report
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	s.routes()
	return s, nil
}

// Start follows the engine, ends runs at their planned end and sweeps old
// sessions until ctx ends.
func (s *Server) Start(ctx context.Context) {
	go s.sync.run(ctx)
	go s.data.refresh(ctx) // runs that went on, or ended, while the Workbench was down
	go func() {
		<-ctx.Done()
		s.data.close()
	}()
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.endRuns(ctx)
				s.endPreviews(ctx)
				s.data.refresh(ctx)
				s.measureMemories(ctx)
			}
		}
	}()
	go func() {
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := s.db.SweepUsage(ctx); err != nil {
					log.Printf("sweep usage: %v", err)
				}
				if err := s.db.Sweep(ctx); err != nil {
					log.Printf("sweep: %v", err)
				}
			}
		}
	}()
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
	w.Header().Set("X-Frame-Options", "DENY")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	m := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	m.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })

	m.HandleFunc("GET /{$}", s.home)
	m.HandleFunc("GET /login", s.loginForm)
	m.HandleFunc("POST /login", s.loginSend)
	m.HandleFunc("GET /login/{token}", s.loginConfirm)
	m.HandleFunc("POST /login/{token}", s.loginUse)
	m.HandleFunc("POST /logout", s.member(s.logout))
	m.HandleFunc("GET /invite/{token}", s.inviteForm)
	m.HandleFunc("POST /invite/{token}", s.inviteAccept)

	m.HandleFunc("GET /account", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/analyses", http.StatusSeeOther) })

	m.HandleFunc("GET /sources", s.member(s.sources))
	m.HandleFunc("GET /sources/new", s.member(s.streamNewForm))
	m.HandleFunc("POST /sources/new", s.member(s.streamNew))
	m.HandleFunc("GET /sources/{slug}", s.member(s.streamPage))
	m.HandleFunc("POST /sources/{slug}", s.member(s.streamEdit))
	m.HandleFunc("POST /sources/{slug}/state", s.member(s.streamState))
	m.HandleFunc("POST /sources/{slug}/delete", s.member(s.streamDelete))
	m.HandleFunc("POST /sources/{slug}/share", s.member(s.streamShare))
	m.HandleFunc("POST /sources/{slug}/unshare", s.member(s.streamUnshare))

	m.HandleFunc("GET /analyses", s.member(s.analyses))
	m.HandleFunc("GET /analyses/new", s.member(s.analysisNewForm))
	m.HandleFunc("POST /analyses/new", s.member(s.analysisNew))
	m.HandleFunc("GET /analyses/{id}", s.member(s.analysisPage))
	m.HandleFunc("POST /analyses/{id}", s.member(s.analysisTexts))
	m.HandleFunc("POST /analyses/{id}/settings", s.member(s.analysisSettings))
	m.HandleFunc("POST /analyses/{id}/delete", s.member(s.analysisDelete))
	m.HandleFunc("POST /analyses/{id}/run", s.member(s.analysisRunStart))
	m.HandleFunc("POST /analyses/{id}/stop", s.member(s.analysisRunStop))
	m.HandleFunc("POST /analyses/{id}/memories/{source}/delete", s.member(s.analysisMemoryDelete))
	m.HandleFunc("GET /account/usage", s.member(s.usagePage))
	m.HandleFunc("GET /account/tokens", s.member(s.tokensPage))
	m.HandleFunc("POST /account/tokens", s.member(s.tokenCreate))
	m.HandleFunc("POST /account/tokens/{token}/revoke", s.member(s.tokenRevoke))

	stream := func(h http.HandlerFunc) http.HandlerFunc { return s.bearer(catalogue.ScopeStream, h) }
	m.HandleFunc("GET /api/v1/analyses", stream(s.apiAnalyses))
	m.HandleFunc("GET /api/v1/analyses/{id}", stream(s.apiAnalysis))
	m.HandleFunc("GET /api/v1/analyses/{id}/events", stream(s.apiEvents))
	m.HandleFunc("GET /api/v1/analyses/{id}/sources/{source}/{what...}", stream(s.apiSourceData))
	m.HandleFunc("GET /api/v1/runs", stream(s.apiRuns))
	m.HandleFunc("GET /api/v1/runs/{run}/{file}", stream(s.apiRunFile))

	m.HandleFunc("POST /mcp", s.mcp)
	m.HandleFunc("GET /mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", "POST")
		apiError(w, http.StatusMethodNotAllowed, "method", "This MCP endpoint answers POST requests only; it sends nothing of its own accord.")
	})

	m.HandleFunc("GET /library", s.member(s.libraryPage))
	m.HandleFunc("POST /library", s.member(s.libraryAdd))
	m.HandleFunc("POST /library/{set}/take", s.member(s.libraryTake))
	m.HandleFunc("POST /library/{set}/delete", s.member(s.libraryDelete))
	m.HandleFunc("GET /runs", s.member(s.runsPage))
	m.HandleFunc("POST /runs", s.member(s.runsStart))
	m.HandleFunc("GET /runs/{run}", s.member(s.runPage))
	m.HandleFunc("GET /runs/{run}/{file}", s.member(s.runFile))
	m.HandleFunc("POST /runs/{run}/delete", s.member(s.runDatasetDelete))
	m.HandleFunc("GET /analyses/{id}/live/{source}", s.member(s.analysisLive))
	m.HandleFunc("GET /analyses/{id}/data/{source}/{what...}", s.member(s.analysisProxy))
	m.HandleFunc("POST /analyses/{id}/preview", s.member(s.analysisPreviewStart))
	m.HandleFunc("POST /analyses/{id}/preview/end", s.member(s.analysisPreviewEnd))
	m.HandleFunc("POST /analyses/{id}/trial", s.member(s.analysisTrialStart))
	m.HandleFunc("POST /analyses/{id}/trial/end", s.member(s.analysisTrialEnd))
	m.HandleFunc("POST /analyses/{id}/trial/keep", s.member(s.analysisTrialKeep))
	m.HandleFunc("GET /analyses/{id}/trial/live/{source}", s.member(s.analysisLive))
	m.HandleFunc("GET /analyses/{id}/trial/data/{source}/{what...}", s.member(s.analysisProxy))
	m.HandleFunc("GET /analyses/{id}/compare/live", s.member(s.analysisCompareLive))
	m.HandleFunc("GET /analyses/{id}/compare/state", s.member(s.analysisCompareState))

	m.HandleFunc("GET /analyses/{id}/publish", s.member(s.publishForm))
	m.HandleFunc("POST /analyses/{id}/publish", s.member(s.publish))
	m.HandleFunc("POST /analyses/{id}/withdraw", s.member(s.withdraw))
	m.HandleFunc("POST /sources/{slug}/consent", s.member(s.streamConsent))
	m.HandleFunc("GET /research", s.visitor(s.researchIndex))
	m.HandleFunc("GET /research/{slug}", s.visitor(s.researchPage))
	m.HandleFunc("POST /research/{slug}/report", s.visitor(s.researchReport))
	m.HandleFunc("GET /research/{slug}/live/{source}", s.researchLive)
	m.HandleFunc("GET /research/{slug}/data/{source}/{what}", s.researchData)
	m.HandleFunc("GET /research/{slug}/compare/live", s.researchCompareLive)
	m.HandleFunc("GET /research/{slug}/compare/state", s.researchCompareState)
	m.HandleFunc("GET /research/{slug}/runs/{run}/{file}", s.visitor(s.researchFile))

	m.HandleFunc("GET /admin", s.admin(s.adminPage))
	m.HandleFunc("POST /admin/reports/{report}/close", s.admin(s.adminReportClose))
	m.HandleFunc("POST /admin/analyses/{id}/withdraw", s.admin(s.adminWithdraw))
	m.HandleFunc("POST /admin/invitations", s.admin(s.adminInvite))
	m.HandleFunc("POST /admin/cap", s.admin(s.adminCap))
	m.HandleFunc("POST /admin/member-cap", s.admin(s.adminMemberCap))
	m.HandleFunc("POST /admin/members/{user}/cap", s.admin(s.adminOneCap))
	s.mux = m
}

// --- templates ---

var funcs = template.FuncMap{
	"clock":   func(t time.Time) string { return t.UTC().Format("15:04:05") },
	"seconds": func(v float64) string { return roundDuration(time.Duration(v * float64(time.Second))) },
	"when": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		return t.UTC().Format("2006-01-02 15:04") + " UTC"
	},
	"since": func(t time.Time) string {
		if t.IsZero() {
			return "—"
		}
		d := time.Since(t).Round(time.Second)
		switch {
		case d < time.Minute:
			return fmt.Sprintf("%d s", int(d.Seconds()))
		case d < time.Hour:
			return fmt.Sprintf("%d min", int(d.Minutes()))
		case d < 48*time.Hour:
			return fmt.Sprintf("%.1f h", d.Hours())
		}
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	},
	"bytes": func(n int64) string {
		switch {
		case n >= 1<<30:
			return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
		case n >= 1<<20:
			return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
		case n >= 1<<10:
			return fmt.Sprintf("%.0f kB", float64(n)/(1<<10))
		}
		return fmt.Sprintf("%d B", n)
	},
	"cores": func(percent float64) string { return strconv.FormatFloat(math.Round(percent)/100, 'f', 2, 64) },
	"hz": func(v float64) string {
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".") + " Hz"
	},
	"str": func(v any) string { return fmt.Sprint(v) },
	"add": func(a, b int) int { return a + b },
	"deref": func(t *time.Time) time.Time {
		if t == nil {
			return time.Time{}
		}
		return *t
	},
}

// staticVersion is a hash of the embedded static files. Pages refer to them
// with it in the address, so a browser fetches them again after a deploy
// instead of using a copy it cached before.
var staticVersion = func() string {
	h := sha256.New()
	fs.WalkDir(staticFS, "static", func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := staticFS.ReadFile(path)
			h.Write([]byte(path))
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
}()

func (s *Server) parseTemplates() error {
	funcs["asset"] = func(name string) string { return "/static/" + name + "?v=" + staticVersion }
	s.pages = map[string]*template.Template{}
	names, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return err
	}
	for _, n := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")
		if base == "layout" {
			continue
		}
		t, err := template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html", n)
		if err != nil {
			return fmt.Errorf("template %s: %w", n, err)
		}
		s.pages[base] = t
	}
	return nil
}

// page is what every page gets.
type page struct {
	Site          string
	Title         string
	User          *catalogue.User
	CSRF          string
	Flash         string
	Error         string
	EngineProblem string
	Data          any
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name, title string, data any) {
	p := page{Site: s.cfg.SiteName, Title: title, Data: data}
	if sess, ok := sessionFrom(r); ok {
		u := sess.User
		p.User, p.CSRF = &u, sess.CSRF
		if ready, problem, _ := s.sync.status(); !ready {
			p.EngineProblem = problem
		}
	}
	if c, err := r.Cookie("low_flash"); err == nil {
		if v, err := url.QueryUnescape(c.Value); err == nil {
			p.Flash = v
		}
		http.SetCookie(w, &http.Cookie{Name: "low_flash", Path: "/", MaxAge: -1})
	}
	if e, ok := data.(interface{ FormError() string }); ok {
		p.Error = e.FormError()
	}
	t := s.pages[name]
	if t == nil {
		http.Error(w, "no template "+name, 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, "layout.html", p); err != nil {
		log.Printf("render %s: %v", name, err)
	}
}

// redirect sends the browser on after a form, with a message to show.
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, to, flash string) {
	if flash != "" {
		http.SetCookie(w, &http.Cookie{Name: "low_flash", Value: url.QueryEscape(flash), Path: "/", MaxAge: 60,
			HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secure})
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "message", "Not found", message{Heading: "Not found", Text: "There is nothing here, or it is not yours to see."})
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
	s.render(w, r, http.StatusInternalServerError, "message", "Error", message{Heading: "Something went wrong", Text: "The error has been logged."})
}

type message struct {
	Heading  string
	Text     string
	Link     string
	LinkText string
}

// --- sessions and access ---

type ctxKey int

const sessionKey ctxKey = 0

func sessionFrom(r *http.Request) (catalogue.Session, bool) {
	s, ok := r.Context().Value(sessionKey).(catalogue.Session)
	return s, ok
}

func (s *Server) loadSession(r *http.Request) (*http.Request, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return r, false
	}
	sess, err := s.db.Session(r.Context(), c.Value)
	if err != nil {
		return r, false
	}
	return r.WithContext(context.WithValue(r.Context(), sessionKey, sess)), true
}

// sameOrigin refuses a form posted from another site.
func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	u, err := url.Parse(o)
	return err == nil && u.Host == r.Host
}

// member wraps a handler that needs a logged-in member. Forms must carry
// the session's CSRF value.
func (s *Server) member(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r, ok := s.loadSession(r)
		if !ok {
			if r.Method == http.MethodGet && !strings.Contains(r.URL.Path, "/live/") && !strings.Contains(r.URL.Path, "/data/") {
				http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "not logged in", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			sess, _ := sessionFrom(r)
			if !s.sameOrigin(r) || subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.CSRF)) != 1 {
				http.Error(w, "the form has expired; go back, reload and try again", http.StatusForbidden)
				return
			}
		}
		h(w, r)
	}
}

func (s *Server) admin(h http.HandlerFunc) http.HandlerFunc {
	return s.member(func(w http.ResponseWriter, r *http.Request) {
		if sess, _ := sessionFrom(r); !sess.User.Admin {
			s.notFound(w, r)
			return
		}
		h(w, r)
	})
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u catalogue.User) error {
	token, err := s.db.CreateSession(r.Context(), u.ID, sessionLife)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionLife.Seconds()),
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.secure})
	return nil
}

func isNotFound(err error) bool { return errors.Is(err, catalogue.ErrNotFound) }
