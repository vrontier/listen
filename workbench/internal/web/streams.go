package web

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// Sample rates offered for a stream (control-api.md, Choosing the sample
// rate). The first is the default.
var sampleRates = []int{22050, 32000, 44100, 48000}

const probeValid = 15 * time.Minute

type accountData struct {
	Streams []streamRow
}

type streamRow struct {
	catalogue.Stream
	Engine   engine.Stream
	InEngine bool
	Analyses []catalogue.Analysis
}

func (s *Server) account(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	streams, err := s.db.StreamsOf(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var d accountData
	for _, st := range streams {
		row := streamRow{Stream: st}
		row.Engine, row.InEngine = s.sync.stream(st.EngineID())
		if row.Analyses, err = s.db.AnalysesOf(r.Context(), st.ID); err != nil {
			s.fail(w, r, err)
			return
		}
		d.Streams = append(d.Streams, row)
	}
	s.render(w, r, 200, "account", "Your streams", d)
}

// --- registering a stream ---

type newStreamData struct {
	Input      string
	Texts      catalogue.StreamTexts
	Audio      bool
	Attest     bool
	Probe      *engine.ProbeResult
	Rates      []int
	SampleRate int
	Existing   *catalogue.Stream
	Errs       map[string]string
	Err        string
}

func (d newStreamData) FormError() string { return d.Err }

func (s *Server) streamNewForm(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, 200, "stream-new", "Register a stream", newStreamData{Texts: catalogue.StreamTexts{TimeZone: "UTC"}})
}

func readTexts(r *http.Request) catalogue.StreamTexts {
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	return catalogue.StreamTexts{
		Name: f("name"), Place: f("place"), TimeZone: f("timezone"), Description: f("description"),
		Credit: f("credit"), CreditURL: f("credit_url"),
	}
}

// checkTexts applies the length limits; links are allowed in the credit
// link only (members-and-streams.md, Filtering and limits).
func checkTexts(t catalogue.StreamTexts, errs map[string]string) {
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
		Audio: r.PostFormValue("audio") == "on", Attest: r.PostFormValue("attest") == "on",
		Errs: map[string]string{},
	}
	d.SampleRate, _ = strconv.Atoi(r.PostFormValue("sample_rate"))
	if u, err := url.Parse(d.Input); err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || len(d.Input) > 500 {
		d.Errs["input"] = "an http or https address of an audio stream"
		d.Err = "Please check the stream's address."
		s.render(w, r, 400, "stream-new", "Register a stream", d)
		return
	}
	if existing, err := s.db.StreamByInput(r.Context(), d.Input); err == nil {
		d.Existing = &existing
		d.Err = "This stream is registered already."
		s.render(w, r, 409, "stream-new", "Register a stream", d)
		return
	}

	key := strconv.FormatInt(sess.User.ID, 10) + "\x00" + d.Input
	if r.PostFormValue("action") != "save" {
		if ready, problem, _ := s.sync.status(); !ready {
			d.Err = "The stream cannot be checked now: " + problem
			s.render(w, r, 503, "stream-new", "Register a stream", d)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
		defer cancel()
		res, err := s.eng.Probe(ctx, engine.ProbeRequest{Input: d.Input})
		if err != nil {
			if engine.IsCode(err, "busy") {
				d.Err = "Too many streams are being checked at the moment. Try again in a minute."
				s.render(w, r, 429, "stream-new", "Register a stream", d)
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
			d.Rates = ratesFor(res.SampleRate)
			d.SampleRate = d.Rates[0]
		}
		if d.Texts.TimeZone == "" {
			d.Texts.TimeZone = "UTC"
		}
		s.render(w, r, 200, "stream-new", "Register a stream", d)
		return
	}

	// Saving: the probe must have passed for this address shortly before.
	s.probesMu.Lock()
	p, ok := s.probes[key]
	s.probesMu.Unlock()
	if !ok || time.Since(p.at) > probeValid || !p.result.OK {
		d.Err = "Please check the stream again before saving."
		s.render(w, r, 400, "stream-new", "Register a stream", d)
		return
	}
	d.Probe = &p.result
	d.Rates = ratesFor(p.result.SampleRate)
	checkTexts(d.Texts, d.Errs)
	rateOK := false
	for _, x := range d.Rates {
		rateOK = rateOK || x == d.SampleRate
	}
	if !rateOK {
		d.Errs["sample_rate"] = "choose one of the rates offered"
	}
	if !d.Attest {
		d.Errs["attest"] = "required: rule 4 of the Workbench"
	}
	if len(d.Errs) > 0 {
		d.Err = "Please check the marked fields."
		s.render(w, r, 400, "stream-new", "Register a stream", d)
		return
	}
	probe, _ := json.Marshal(p.result)
	st, a, err := s.db.CreateStream(r.Context(), catalogue.NewStream{
		OwnerID: sess.User.ID, Input: d.Input, SampleRate: d.SampleRate, Texts: d.Texts, AudioAllowed: d.Audio,
		Probe: probe, AnalysisName: "First analysis", AnalysisSpec: json.RawMessage(`{"memory":{"mode":"disk"}}`),
	})
	if errors.Is(err, catalogue.ErrDuplicateInput) {
		d.Err = "This stream was registered a moment ago by someone else."
		s.render(w, r, 409, "stream-new", "Register a stream", d)
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.probesMu.Lock()
	delete(s.probes, key)
	s.probesMu.Unlock()
	flash := "Stream registered. It is in draft: it connects while you watch it."
	if err := s.sync.push(r.Context(), st); err != nil {
		log.Printf("push %s: %v", st.EngineID(), err)
		flash = "Stream registered. The engine has not taken it yet; it will be sent again when the engine is reachable."
	}
	s.redirect(w, r, "/account/streams/"+st.Slug+"/analyses/"+strconv.FormatInt(a.ID, 10), flash)
}

// --- a stream's page ---

type consumer struct {
	Analysis catalogue.Analysis
	Owner    string
	Readers  Readers
	State    string
}

type streamData struct {
	Stream    catalogue.Stream
	Engine    engine.Stream
	InEngine  bool
	Probe     engine.ProbeResult
	Consumers []consumer
	Audit     []catalogue.AuditEntry
	IsOwner   bool
	Errs      map[string]string
	Texts     catalogue.StreamTexts
	Err       string
}

func (d streamData) FormError() string { return d.Err }

// ownStream finds a stream the member may see. For now that is its owner;
// invited members follow with sharing.
func (s *Server) ownStream(w http.ResponseWriter, r *http.Request) (catalogue.Stream, bool) {
	sess, _ := sessionFrom(r)
	st, err := s.db.StreamBySlug(r.Context(), r.PathValue("slug"))
	if err != nil || st.OwnerID != sess.User.ID {
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
	d := streamData{Stream: st, Texts: st.Texts, IsOwner: true, Errs: map[string]string{}}
	json.Unmarshal(st.Probe, &d.Probe)
	d.Engine, d.InEngine = s.sync.stream(st.EngineID())
	analyses, err := s.db.AnalysesOf(r.Context(), st.ID)
	if err != nil {
		return d, err
	}
	for _, a := range analyses {
		owner, err := s.db.User(r.Context(), a.OwnerID)
		if err != nil {
			return d, err
		}
		state := "idle"
		if d.Engine.State == "connected" {
			state = "running"
		}
		d.Consumers = append(d.Consumers, consumer{Analysis: a, Owner: owner.Name,
			Readers: s.live.readers(st.EngineID(), a.EngineID()), State: state})
	}
	d.Audit, err = s.db.AuditOf(r.Context(), "stream", st.ID)
	return d, err
}

func (s *Server) streamPage(w http.ResponseWriter, r *http.Request) {
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	d, err := s.streamData(r, st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "stream", st.Texts.Name, d)
}

func (s *Server) streamEdit(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	t := readTexts(r)
	errs := map[string]string{}
	checkTexts(t, errs)
	if len(errs) > 0 {
		d, err := s.streamData(r, st)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Texts, d.Errs, d.Err = t, errs, "Please check the marked fields."
		s.render(w, r, 400, "stream", st.Texts.Name, d)
		return
	}
	if err := s.db.UpdateStreamTexts(r.Context(), st.ID, sess.User.ID, t, r.PostFormValue("audio") == "on"); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/account/streams/"+st.Slug, "Saved.")
}

func (s *Server) streamState(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	if st.State == catalogue.StreamSuspended {
		s.redirect(w, r, "/account/streams/"+st.Slug, "A suspended stream cannot be changed.")
		return
	}
	state, always := st.State, st.AlwaysOn
	var flash string
	switch r.PostFormValue("to") {
	case "draft":
		state, always, flash = catalogue.StreamDraft, false, "Back in draft: the stream connects only while someone watches."
	case "production":
		state, always = catalogue.StreamProduction, r.PostFormValue("always_on") == "on"
		flash = "In production."
		if always {
			flash = "In production, always on: the stream stays connected."
		}
	case "pause":
		state, flash = catalogue.StreamPaused, "Paused. The stream is disconnected; its memories are kept."
	case "resume":
		state, always, flash = catalogue.StreamDraft, false, "Resumed, in draft."
	default:
		http.Error(w, "unknown state", http.StatusBadRequest)
		return
	}
	if err := s.db.SetStreamState(r.Context(), st.ID, sess.User.ID, state, always); err != nil {
		s.fail(w, r, err)
		return
	}
	st.State, st.AlwaysOn = state, always
	if err := s.sync.push(r.Context(), st); err != nil {
		log.Printf("push %s: %v", st.EngineID(), err)
		flash += " The engine has not taken the change yet; it will be sent again."
	}
	s.redirect(w, r, "/account/streams/"+st.Slug, flash)
}

func (s *Server) streamDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	if r.PostFormValue("confirm") != st.Texts.Name {
		s.redirect(w, r, "/account/streams/"+st.Slug, "Not deleted: type the stream's name exactly to confirm.")
		return
	}
	if err := s.db.DeleteStream(r.Context(), st.ID, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.eng.DeleteStream(r.Context(), st.EngineID(), true); err != nil && !engine.IsCode(err, "not_found") {
		log.Printf("delete %s in engine: %v", st.EngineID(), err)
	}
	s.redirect(w, r, "/account", "Deleted "+st.Texts.Name+", with its analyses and memories.")
}
