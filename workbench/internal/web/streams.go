package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vrontier/listen/workbench/internal/catalogue"
	"github.com/vrontier/listen/workbench/internal/engine"
)

// Sample rates offered for a stream (control-api.md, Choosing the sample
// rate). The first is the default.
var sampleRates = []int{22050, 32000, 44100, 48000}

const probeValid = 15 * time.Minute

type accountData struct {
	Streams []streamRow // registered by the member
	Others  []streamRow // other members' open sources
}

type streamRow struct {
	catalogue.Stream
	Engine   engine.Stream
	InEngine bool
	Analyses []catalogue.Analysis // that use it
}

// sources lists the sources a member may use: their own and every other
// member's open ones (decided 2026-10-18, sources are a commons).
func (s *Server) sources(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	streams, err := s.db.StreamsOf(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	others, err := s.db.OpenStreams(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var d accountData
	for i, list := range [][]catalogue.Stream{streams, others} {
		for _, st := range list {
			row := streamRow{Stream: st}
			row.Engine, row.InEngine = s.sync.stream(st.EngineID())
			if row.Analyses, err = s.db.AnalysesUsing(r.Context(), st.ID); err != nil {
				s.fail(w, r, err)
				return
			}
			if i == 0 {
				d.Streams = append(d.Streams, row)
			} else {
				d.Others = append(d.Others, row)
			}
		}
	}
	s.render(w, r, 200, "sources", "Sources", d)
}

// --- registering a stream ---

type newStreamData struct {
	Input      string
	Texts      textsForm
	Audio      bool
	Private    bool   // only the member for now
	Attest     string // catalogue.AttestOwn or AttestOperator
	Operator   string
	Probe      *engine.ProbeResult
	Rates      []int
	SampleRate int
	Existing   *catalogue.Stream
	Usable     bool // the member may use Existing
	Errs       map[string]string
	Err        string
}

func (d newStreamData) FormError() string { return d.Err }

func (s *Server) streamNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, 200, "source-new", "Register a source", newStreamData{Texts: textsForm{StreamTexts: catalogue.StreamTexts{TimeZone: "UTC"}}})
}

// textsForm is a stream's texts as the form holds them, with the
// coordinates as typed.
type textsForm struct {
	catalogue.StreamTexts
	Coords string
}

func textsOf(t catalogue.StreamTexts) textsForm {
	return textsForm{StreamTexts: t, Coords: formatCoords(t.Lat, t.Lon)}
}

func formatCoords(lat, lon *float64) string {
	if lat == nil || lon == nil {
		return ""
	}
	return strconv.FormatFloat(*lat, 'f', -1, 64) + ", " + strconv.FormatFloat(*lon, 'f', -1, 64)
}

// parseCoords reads "latitude, longitude" in decimal degrees, south and
// west negative. "50.12345, 10.54321", "50.12345 10.54321" and "50,12345; 10,54321"
// are all understood. Values are kept to five decimals (about a metre).
func parseCoords(s string) (lat, lon float64, ok bool) {
	var parts []string
	switch {
	case strings.Contains(s, ";"):
		parts = strings.Split(s, ";")
	case strings.Count(s, ",") == 1:
		parts = strings.Split(s, ",")
	default:
		parts = strings.Fields(s)
	}
	if len(parts) != 2 {
		return 0, 0, false
	}
	num := func(v string) (float64, bool) {
		v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.Replace(v, ",", ".", 1)), "°"))
		x, err := strconv.ParseFloat(v, 64)
		return math.Round(x*1e5) / 1e5, err == nil && !math.IsNaN(x) && !math.IsInf(x, 0)
	}
	lat, ok1 := num(parts[0])
	lon, ok2 := num(parts[1])
	if !ok1 || !ok2 || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return 0, 0, false
	}
	return lat, lon, true
}

func readTexts(r *http.Request) textsForm {
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	return textsForm{StreamTexts: catalogue.StreamTexts{
		Name: f("name"), Place: f("place"), TimeZone: f("timezone"), Description: f("description"),
		Credit: f("credit"), CreditURL: f("credit_url"),
	}, Coords: f("coords")}
}

// checkTexts applies the length limits; links are allowed in the credit
// link only (members-and-streams.md, Filtering and limits).
func checkTexts(tf *textsForm, errs map[string]string) {
	tf.Lat, tf.Lon = nil, nil
	if tf.Coords != "" {
		if lat, lon, ok := parseCoords(tf.Coords); ok {
			tf.Lat, tf.Lon = &lat, &lon
		} else {
			errs["coords"] = "latitude, longitude in decimal degrees, for example 50.12345, 10.54321 (south and west negative)"
		}
	}
	t := tf.StreamTexts
	limit := func(field, v string, min, max int) {
		if n := utf8.RuneCountInString(v); n < min || n > max {
			if min > 0 {
				errs[field] = "between " + strconv.Itoa(min) + " and " + strconv.Itoa(max) + " characters"
			} else {
				errs[field] = "at most " + strconv.Itoa(max) + " characters"
			}
		}
	}
	limit("name", t.Name, 2, 80)
	limit("place", t.Place, 0, 80)
	limit("description", t.Description, 0, 2000)
	limit("credit", t.Credit, 0, 200)
	for k, v := range map[string]string{"name": t.Name, "place": t.Place, "description": t.Description, "credit": t.Credit} {
		if strings.Contains(v, "://") || strings.Contains(strings.ToLower(v), "www.") {
			errs[k] = "links belong in the credit link field only"
		}
	}
	if t.TimeZone != "" {
		if _, err := time.LoadLocation(t.TimeZone); err != nil {
			errs["timezone"] = "not a time zone name such as Europe/Berlin or UTC"
		}
	}
	if t.CreditURL != "" {
		u, err := url.Parse(t.CreditURL)
		if err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" || len(t.CreditURL) > 300 {
			errs["credit_url"] = "an http or https address"
		}
	}
}

// ratesOf are the decode rates a member may choose for a probed stream.
// Through listen-ingest the engine decodes one connection at as many rates
// as its streams ask for, so an address read already does not fix the
// rate (engine 0.2.0+3f16005; an engine connecting itself, in development,
// answers 409 rate_conflict instead, which the registration explains).
func ratesOf(res engine.ProbeResult) []int {
	return ratesFor(res.SampleRate)
}

func ratesFor(native int) []int {
	var out []int
	for _, r := range sampleRates {
		if r <= native {
			out = append(out, r)
		}
	}
	if len(out) == 0 && native > 0 {
		out = []int{native}
	}
	return out
}

func (s *Server) streamNew(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	d := newStreamData{
		Input: strings.TrimSpace(r.PostFormValue("input")), Texts: readTexts(r),
		Audio: r.PostFormValue("audio") == "on", Private: r.PostFormValue("private") == "on", Attest: r.PostFormValue("attest"),
		Operator: strings.TrimSpace(r.PostFormValue("operator")),
		Errs:     map[string]string{},
	}
	d.SampleRate, _ = strconv.Atoi(r.PostFormValue("sample_rate"))
	input, ok := normalInput(d.Input)
	if !ok {
		d.Errs["input"] = "an http or https address of an audio stream"
		d.Err = "Please check the stream's address."
		s.render(w, r, 400, "source-new", "Register a source", d)
		return
	}
	d.Input = input
	if s.reserved(input) {
		d.Errs["input"] = "not open to the Workbench"
		d.Err = "This stream cannot be registered here: it is on this server's list of streams whose operators have not agreed to their analysis on the Workbench."
		s.render(w, r, 409, "source-new", "Register a source", d)
		return
	}
	for _, in := range []string{input, otherScheme(input)} {
		if existing, err := s.db.StreamByInput(r.Context(), in); err == nil {
			d.Existing = &existing
			d.Usable, _ = s.db.MayUseStream(r.Context(), sess.User.ID, existing)
			d.Err = "This stream is registered already."
			s.render(w, r, 409, "source-new", "Register a source", d)
			return
		}
	}

	key := strconv.FormatInt(sess.User.ID, 10) + "\x00" + d.Input
	if r.PostFormValue("action") != "save" {
		if ready, problem, _ := s.sync.status(); !ready {
			d.Err = "The stream cannot be checked now: " + problem
			s.render(w, r, 503, "source-new", "Register a source", d)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		res, err := s.eng.Probe(ctx, engine.ProbeRequest{Input: d.Input})
		if err != nil {
			if engine.IsCode(err, "busy") {
				d.Err = "Too many streams are being checked at the moment. Try again in a minute."
				s.render(w, r, 429, "source-new", "Register a source", d)
				return
			}
			s.fail(w, r, err)
			return
		}
		d.Probe = &res
		if res.OK {
			s.probesMu.Lock()
			for k, p := range s.probes {
				if time.Since(p.at) > probeValid {
					delete(s.probes, k)
				}
			}
			s.probes[key] = probeEntry{time.Now(), res}
			s.probesMu.Unlock()
			d.Rates = ratesOf(res)
			d.SampleRate = d.Rates[0]
		}
		if d.Texts.TimeZone == "" {
			d.Texts.TimeZone = "UTC"
		}
		s.render(w, r, 200, "source-new", "Register a source", d)
		return
	}

	// Saving: the probe must have passed for this address shortly before.
	s.probesMu.Lock()
	p, ok := s.probes[key]
	s.probesMu.Unlock()
	if !ok || time.Since(p.at) > probeValid || !p.result.OK {
		d.Err = "Please check the stream again before saving."
		s.render(w, r, 400, "source-new", "Register a source", d)
		return
	}
	d.Probe = &p.result
	d.Rates = ratesOf(p.result)
	checkTexts(&d.Texts, d.Errs)
	rateOK := false
	for _, x := range d.Rates {
		rateOK = rateOK || x == d.SampleRate
	}
	if !rateOK {
		d.Errs["sample_rate"] = "choose one of the rates offered"
	}
	switch d.Attest {
	case catalogue.AttestOwn:
		d.Operator = ""
	case catalogue.AttestOperator:
		if n := utf8.RuneCountInString(d.Operator); n < 3 || n > 200 {
			d.Errs["operator"] = "the operator's name, and an email address or web page to reach them, up to 200 characters"
		}
	default:
		d.Errs["attest"] = "required: rule 4 of the Workbench"
	}
	if len(d.Errs) > 0 {
		d.Err = "Please check the marked fields."
		s.render(w, r, 400, "source-new", "Register a source", d)
		return
	}
	probe, _ := json.Marshal(p.result)
	st, err := s.db.CreateStream(r.Context(), catalogue.NewStream{
		OwnerID: sess.User.ID, Input: d.Input, SampleRate: d.SampleRate, Texts: d.Texts.StreamTexts, AudioAllowed: d.Audio,
		Private: d.Private, Attestation: d.Attest, Operator: d.Operator, Probe: probe,
	})
	if errors.Is(err, catalogue.ErrDuplicateInput) {
		d.Err = "This stream was registered a moment ago by someone else; look for it under Sources."
		s.render(w, r, 409, "source-new", "Register a source", d)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.probesMu.Lock()
	delete(s.probes, key)
	s.probesMu.Unlock()
	flash := "Source registered, and open to every member. Create an analysis with it to see what it hears."
	if d.Private {
		flash = "Source registered, for you only for now. Create an analysis with it to see what it hears; open it to every member on this page when it is ready."
	}
	defer s.syncGroups(r.Context())
	if err := s.sync.push(r.Context(), st); engine.IsCode(err, "rate_conflict") {
		var ee *engine.Error
		errors.As(err, &ee)
		flash = "Source registered, but the engine does not take it: the station is read on this server at another rate (" + ee.Message + "). Register it again at that rate, or ask the master admin."
	} else if err != nil {
		log.Printf("push %s: %v", st.EngineID(), err)
		flash = "Source registered. The engine has not taken it yet; it will be sent again when the engine is reachable."
	}
	s.redirect(w, r, "/sources/"+st.Slug, flash)
}

// --- a stream's page ---

// consumer is one analysis that uses a source: who is connected to it.
type consumer struct {
	Analysis catalogue.Analysis
	Owner    string
	Readers  Readers
	State    string
	Mine     bool
}

type streamData struct {
	Stream    catalogue.Stream
	View      engine.ViewSpec // the view as saved, for the form
	Reserved  bool            // a stream not open to the Workbench (reserved.go)
	Engine    engine.Stream
	InEngine  bool
	Probe     engine.ProbeResult
	Consumers []consumer
	Audit     []catalogue.AuditEntry
	IsOwner   bool // the member is its steward
	IsSteward bool // its steward or the master admin: may change it
	CanUse    bool // the member may use it in analyses
	Others    int  // analyses of other members that use it
	Errs      map[string]string
	Texts     textsForm
	Publish   bool // members may publish on this Workbench
	Err       string
}

func (d streamData) FormError() string { return d.Err }

// viewStream finds a stream the member may see: their own, any open one,
// and for the master admin every one.
func (s *Server) viewStream(w http.ResponseWriter, r *http.Request) (catalogue.Stream, bool) {
	sess, _ := sessionFrom(r)
	st, err := s.db.StreamBySlug(r.Context(), r.PathValue("slug"))
	ok := err == nil
	if ok && !sess.User.Admin {
		ok, err = s.db.MayUseStream(r.Context(), sess.User.ID, st)
	}
	if !ok {
		if err != nil && !isNotFound(err) {
			s.fail(w, r, err)
			return st, false
		}
		s.notFound(w, r)
	}
	return st, ok
}

// ownStream finds a stream the member looks after: as its steward, or as
// the master admin, for what only they may do.
func (s *Server) ownStream(w http.ResponseWriter, r *http.Request) (catalogue.Stream, bool) {
	sess, _ := sessionFrom(r)
	st, err := s.db.StreamBySlug(r.Context(), r.PathValue("slug"))
	if err != nil || st.OwnerID != sess.User.ID && !sess.User.Admin {
		if err != nil && !isNotFound(err) {
			s.fail(w, r, err)
			return st, false
		}
		s.notFound(w, r)
		return st, false
	}
	return st, true
}

func (s *Server) streamData(r *http.Request, st catalogue.Stream) (streamData, error) {
	sess, _ := sessionFrom(r)
	d := streamData{Stream: st, Texts: textsOf(st.Texts), IsOwner: st.OwnerID == sess.User.ID, Errs: map[string]string{}, Publish: s.cfg.Publishing}
	d.IsSteward = d.IsOwner || sess.User.Admin
	d.CanUse, _ = s.db.MayUseStream(r.Context(), sess.User.ID, st)
	if d.Stream.View != nil {
		json.Unmarshal(d.Stream.View, &d.View)
	}
	d.Reserved = s.reserved(st.Input)
	json.Unmarshal(st.Probe, &d.Probe)
	d.Engine, d.InEngine = s.sync.stream(st.EngineID())
	analyses, err := s.db.AnalysesUsing(r.Context(), st.ID)
	if err != nil {
		return d, err
	}
	for _, a := range analyses {
		state := "idle"
		if d.Engine.State == "connected" {
			state = "run"
		}
		d.Consumers = append(d.Consumers, consumer{Analysis: a, Owner: a.OwnerName,
			Readers: s.live.readers(st.EngineID(), a.EngineID()).add(s.live.readers(st.EngineID(), previewID(a))), State: state, Mine: a.OwnerID == sess.User.ID})
		if a.OwnerID != st.OwnerID {
			d.Others++
		}
	}
	if d.IsSteward {
		d.Audit, err = s.db.AuditOf(r.Context(), "stream", st.ID)
	}
	return d, err
}

func (s *Server) streamPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.viewStream(w, r)
	if !ok {
		return
	}
	d, err := s.streamData(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "source", st.Texts.Name, d)
}

func (s *Server) streamEdit(w http.ResponseWriter, r *http.Request) {
	defer s.syncGroups(r.Context()) // coordinates go to the groups
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	t := readTexts(r)
	errs := map[string]string{}
	checkTexts(&t, errs)
	if len(errs) > 0 {
		d, err := s.streamData(r, st)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Texts, d.Errs, d.Err = t, errs, "Please check the marked fields."
		s.render(w, r, 400, "source", st.Texts.Name, d)
		return
	}
	if err := s.db.UpdateStreamTexts(r.Context(), st.ID, sess.User.ID, t.StreamTexts, r.PostFormValue("audio") == "on"); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/sources/"+st.Slug, "Saved.")
}

func (s *Server) streamState(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	if st.State == catalogue.StreamSuspended {
		s.redirect(w, r, "/sources/"+st.Slug, "A suspended stream cannot be changed.")
		return
	}
	var state, flash string
	switch r.PostFormValue("to") {
	case "pause":
		state, flash = catalogue.StreamPaused, "Paused. The stream is disconnected; its memories are kept."
	case "resume":
		state, flash = catalogue.StreamActive, "Resumed."
	case "retire":
		state, flash = catalogue.StreamRetired, "Retired. Nothing connects to it; analyses that use it keep their history, and no new analysis can choose it."
	case "unretire":
		state, flash = catalogue.StreamActive, "Back in service."
	default:
		http.Error(w, "unknown state", http.StatusBadRequest)
		return
	}
	if err := s.db.SetStreamState(r.Context(), st.ID, sess.User.ID, state); err != nil {
		s.fail(w, r, err)
		return
	}
	st.State = state
	defer s.syncGroups(r.Context())
	if err := s.sync.push(r.Context(), st); err != nil {
		log.Printf("push %s: %v", st.EngineID(), err)
		flash += " The engine has not taken the change yet; it will be sent again."
	}
	s.redirect(w, r, "/sources/"+st.Slug, flash)
}

func (s *Server) streamDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	if r.PostFormValue("confirm") != st.Texts.Name {
		s.redirect(w, r, "/sources/"+st.Slug, "Not deleted: type the stream's name exactly to confirm.")
		return
	}
	using, err := s.db.AnalysesUsing(r.Context(), st.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, a := range using {
		if a.OwnerID != st.OwnerID {
			s.redirect(w, r, "/sources/"+st.Slug, "Not deleted: other members' analyses use this source. Retire it instead, which keeps their history.")
			return
		}
	}
	if err := s.db.DeleteStream(r.Context(), st.ID, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.eng.DeleteStream(r.Context(), st.EngineID(), true); err != nil && !engine.IsCode(err, "not_found") {
		log.Printf("delete %s in engine: %v", st.EngineID(), err)
	}
	s.syncGroups(r.Context())
	msg := "Deleted " + st.Texts.Name + " and its memories."
	if len(using) > 0 {
		msg += fmt.Sprintf(" %d analys%s lost this source.", len(using), map[bool]string{true: "is", false: "es"}[len(using) == 1])
	}
	s.redirect(w, r, "/sources", msg)
}

// --- visibility ---

// streamVisibility opens a source to every member, or keeps it to its
// steward for now. It can be made private only while no analysis of
// another member uses it.
func (s *Server) streamVisibility(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	back := "/sources/" + st.Slug
	private := r.PostFormValue("to") == "private"
	if private {
		using, err := s.db.AnalysesUsing(r.Context(), st.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		for _, a := range using {
			if a.OwnerID != st.OwnerID {
				s.redirect(w, r, back, "Still open: other members' analyses use this source. You can pause or retire it instead.")
				return
			}
		}
	}
	if err := s.db.SetStreamPrivate(r.Context(), st.ID, sess.User.ID, private); err != nil {
		s.fail(w, r, err)
		return
	}
	if private {
		s.redirect(w, r, back, "Private: only "+st.OwnerName+" can use this source now.")
		return
	}
	s.redirect(w, r, back, "Open: every member can now find this source under Sources and use it in their own analyses.")
}

// normalInput writes a stream address the one way it is stored, so that
// the obvious other spellings of a registered stream are recognised
// (members-and-streams.md, Registering a stream): scheme and host in lower
// case, no default port, no trailing slash, no fragment, which is never
// sent to the server. Redirects are not followed. ok is false for anything
// but an http or https address.
func normalInput(s string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || len(s) > 500 {
		return "", false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || u.Scheme == "http" && port == "80" || u.Scheme == "https" && port == "443" {
		u.Host = host
	} else {
		u.Host = host + ":" + port
	}
	u.Fragment, u.RawFragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = strings.TrimRight(u.RawPath, "/")
	return u.String(), true
}

// otherScheme is the same address over the other of http and https.
func otherScheme(input string) string {
	if rest, ok := strings.CutPrefix(input, "https://"); ok {
		return "http://" + rest
	}
	return "https://" + strings.TrimPrefix(input, "http://")
}
