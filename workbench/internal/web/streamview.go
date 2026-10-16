package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/vrontier/listen-workbench/internal/engine"
)

// A stream's spectrogram view (decided 2026-10-16): the stream's own
// bands, unfiltered and independent of any analysis's range, which the
// spectrogram of every analysis of the stream then shows. Its owner sets
// it; the engine checks it when the stream is put.

func (s *Server) streamView(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	st, ok := s.ownStream(w, r)
	if !ok {
		return
	}
	back := "/sources/" + st.Slug
	var view json.RawMessage
	if scale := r.PostFormValue("view_scale"); scale != "" {
		v := engine.ViewSpec{Scale: scale}
		num := func(key, what string, set func(float64)) bool {
			raw := strings.TrimSpace(r.PostFormValue(key))
			if raw == "" {
				return true
			}
			f, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", "."), 64)
			if err != nil {
				s.redirect(w, r, back, "Not saved: "+what+" must be a number.")
				return false
			}
			set(f)
			return true
		}
		if !num("view_min", "the lowest frequency", func(f float64) { v.MinHz = &f }) ||
			!num("view_max", "the highest frequency", func(f float64) { v.MaxHz = &f }) ||
			!num("view_bands", "the number of bands", func(f float64) { n := int(f); v.Bands = &n }) {
			return
		}
		view, _ = json.Marshal(v)
	}
	old := st.View
	if err := s.db.SetStreamView(r.Context(), st.ID, sess.User.ID, view); err != nil {
		s.fail(w, r, err)
		return
	}
	st.View = view
	err := s.sync.push(r.Context(), st)
	var ee *engine.Error
	if errors.As(err, &ee) && ee.Status == http.StatusUnprocessableEntity {
		// The engine refused the view: the saved one stays.
		s.db.SetStreamView(r.Context(), st.ID, sess.User.ID, old)
		st.View = old
		s.sync.push(r.Context(), st)
		var why []string
		for _, fe := range ee.Errors {
			why = append(why, strings.TrimPrefix(fe.Path, "view.")+" "+fe.Message)
		}
		if why == nil {
			why = []string{ee.Message}
		}
		s.redirect(w, r, back, "Not saved: the engine refused the view ("+strings.Join(why, "; ")+").")
		return
	}
	flash := "Saved. Spectrograms of this source show its own view from now on."
	if view == nil {
		flash = "Saved. Spectrograms of this source show each analysis's bands again."
	}
	if err != nil {
		log.Printf("push %s: %v", st.EngineID(), err)
		flash += " The engine has not taken it yet; it will be sent again."
	}
	s.redirect(w, r, back, flash)
}
