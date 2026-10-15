package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/engine"
)

// The shared library of filter rule sets (members-and-streams.md, Sharing).
// A set is put in from one source of one's own analysis and taken out as a
// copy into a source of one's own analysis, as that analysis's next
// revision.

// ruleWords says a rule in words, for those who do not read the engine's
// notation.
func ruleWords(r engine.Rule) string {
	var b strings.Builder
	hz := strconv.FormatFloat(r.Hz, 'g', -1, 64)
	if r.Kind == engine.RuleLine {
		fmt.Fprintf(&b, "a line at %s Hz", hz)
	} else if r.Odd {
		fmt.Fprintf(&b, "%s Hz and its odd harmonics", hz)
	} else {
		fmt.Fprintf(&b, "%s Hz and its harmonics", hz)
	}
	if r.TolHz > 0 {
		fmt.Fprintf(&b, ", within %s Hz", strconv.FormatFloat(r.TolHz, 'g', -1, 64))
	}
	if r.DriftPercent > 0 {
		fmt.Fprintf(&b, ", the base wandering up to %s %%", strconv.FormatFloat(r.DriftPercent, 'g', -1, 64))
	}
	return b.String()
}

func rulesOf(raw json.RawMessage) []engine.Rule {
	var spec engine.AnalysisSpec
	json.Unmarshal(raw, &spec)
	return spec.Rules
}

// libraryView is one set as the library page shows it.
type libraryView struct {
	catalogue.LibrarySet
	Words []string
	Mine  bool
}

// target is a source of one of the member's analyses that a set can be
// taken into.
type target struct {
	Value string // "<analysis>:<stream>"
	Label string
	Rules int
}

type libraryData struct {
	Sets    []libraryView
	Targets []target
	Admin   bool
}

func (s *Server) libraryPage(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	sets, err := s.db.LibrarySets(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := libraryData{Admin: sess.User.Admin}
	for _, l := range sets {
		v := libraryView{LibrarySet: l, Mine: l.OwnerID == sess.User.ID}
		var rules []engine.Rule
		json.Unmarshal(l.Rules, &rules)
		for _, x := range rules {
			v.Words = append(v.Words, ruleWords(x))
		}
		d.Sets = append(d.Sets, v)
	}
	as, err := s.db.AnalysesOf(r.Context(), sess.User.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, a := range as {
		for _, src := range a.Sources {
			d.Targets = append(d.Targets, target{Value: fmt.Sprintf("%d:%d", a.ID, src.Stream.ID),
				Label: a.Name + " — " + src.Stream.Texts.Name, Rules: len(rulesOf(src.Spec))})
		}
	}
	s.render(w, r, 200, "library", "Library", d)
}

// libraryAdd puts the saved rules of one source of the member's analysis
// into the library.
func (s *Server) libraryAdd(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	a, src, ok := s.formSource(w, r)
	if !ok {
		return
	}
	back := analysisPath(a)
	name, note := strings.TrimSpace(r.PostFormValue("name")), strings.TrimSpace(r.PostFormValue("note"))
	rules := rulesOf(src.Spec)
	switch {
	case len(rules) == 0:
		s.redirect(w, r, back, "Not put in the library: this source has no saved filter rules.")
		return
	case utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 80:
		s.redirect(w, r, back, "Not put in the library: the name needs between 2 and 80 characters.")
		return
	case utf8.RuneCountInString(note) > 1000:
		s.redirect(w, r, back, "Not put in the library: the note may have at most 1000 characters.")
		return
	}
	for i := range rules {
		rules[i].Text = ""
	}
	raw, _ := json.Marshal(rules)
	if _, err := s.db.AddLibrarySet(r.Context(), sess.User.ID, name, note, raw, src.Stream.SampleRate); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/library", fmt.Sprintf("Put %q in the library, with %d rule%s. Every member can now take a copy.", name, len(rules), plural(len(rules))))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// formSource finds the member's analysis and source named in the form as
// "analysis" and "source", or together as "target".
func (s *Server) formSource(w http.ResponseWriter, r *http.Request) (catalogue.Analysis, catalogue.AnalysisSource, bool) {
	sess, _ := sessionFrom(r)
	aid, sid := r.PostFormValue("analysis"), r.PostFormValue("source")
	if t := r.PostFormValue("target"); t != "" {
		aid, sid, _ = strings.Cut(t, ":")
	}
	id, _ := strconv.ParseInt(aid, 10, 64)
	stream, _ := strconv.ParseInt(sid, 10, 64)
	a, err := s.db.Analysis(r.Context(), id)
	if err != nil || a.OwnerID != sess.User.ID {
		if err != nil && !isNotFound(err) {
			s.fail(w, r, err)
		} else {
			s.notFound(w, r)
		}
		return a, catalogue.AnalysisSource{}, false
	}
	src, ok := a.Source(stream)
	if !ok {
		s.notFound(w, r)
	}
	return a, src, ok
}

// libraryTake copies a set's rules into a source of the member's analysis:
// added to its rules, or in their place. The engine checks them at the
// source's rate; the result is the analysis's next revision.
func (s *Server) libraryTake(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PathValue("set"), 10, 64)
	set, err := s.db.LibrarySet(r.Context(), id)
	if err != nil {
		if isNotFound(err) {
			s.redirect(w, r, "/library", "That set is no longer in the library.")
			return
		}
		s.fail(w, r, err)
		return
	}
	a, src, ok := s.formSource(w, r)
	if !ok {
		return
	}
	if _, inRun, err := s.db.ActiveRun(r.Context(), a.ID); err != nil || inRun {
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.redirect(w, r, "/library", "Not taken: "+a.Name+" is in a run, and its settings are fixed while the run lasts.")
		return
	}
	var spec engine.AnalysisSpec
	json.Unmarshal(src.Spec, &spec)
	var taken []engine.Rule
	json.Unmarshal(set.Rules, &taken)
	if r.PostFormValue("mode") == "replace" {
		spec.Rules = nil
	}
	added := 0
	for _, x := range taken {
		dup := false
		for _, y := range spec.Rules {
			y.Text = ""
			if x == y {
				dup = true
				break
			}
		}
		if !dup {
			spec.Rules = append(spec.Rules, x)
			added++
		}
	}
	for i := range spec.Rules {
		spec.Rules[i].Text = ""
	}
	ready, problem, _ := s.sync.status()
	if !ready {
		s.redirect(w, r, "/library", "Not taken: the rules cannot be checked now. "+problem)
		return
	}
	v, err := s.eng.Validate(r.Context(), engine.ValidateRequest{SampleRate: src.Stream.SampleRate, AnalysisSpec: spec})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !v.Valid {
		var msgs []string
		for _, e := range v.Errors {
			msgs = append(msgs, e.Message)
		}
		s.redirect(w, r, "/library", fmt.Sprintf("Not taken: at %d Hz the engine refuses these rules for %s: %s", src.Stream.SampleRate, src.Stream.Texts.Name, strings.Join(msgs, "; ")))
		return
	}
	settings := map[int64]json.RawMessage{}
	var ids []int64
	for _, x := range a.Sources {
		ids = append(ids, x.Stream.ID)
		settings[x.Stream.ID] = x.Spec
	}
	settings[src.Stream.ID], _ = json.Marshal(spec)
	if sameRevision(a, ids, settings, a.Spec.Compare) {
		s.redirect(w, r, analysisPath(a), "Nothing changed: the source has these rules already.")
		return
	}
	a, err = s.db.SaveRevision(r.Context(), a.ID, sess.User.ID, ids, settings, a.Spec.Compare)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.db.NoteTaken(r.Context(), set.ID, sess.User.ID, a.ID)
	flash := fmt.Sprintf("Took a copy of %q into %s: %d rule%s added; saved as revision %d. A later change in the library does not change it.",
		set.Name, src.Stream.Texts.Name, added, plural(added), a.Revision)
	if set.SampleRate != src.Stream.SampleRate {
		flash += fmt.Sprintf(" The set was made at %d Hz and this source runs at %d Hz: tolerances left empty are a number of bins, which differ in width.", set.SampleRate, src.Stream.SampleRate)
	}
	for _, x := range v.Warnings {
		flash += " Note: " + x.Message
	}
	if err := s.pushSources(r, a.Sources); err != nil {
		flash += " The engine has not taken it yet; it will be sent again."
	}
	s.redirect(w, r, analysisPath(a), flash)
}

// libraryDelete takes a set out of the library: its member's, or by the
// master admin. Copies already taken stay.
func (s *Server) libraryDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	id, _ := strconv.ParseInt(r.PathValue("set"), 10, 64)
	set, err := s.db.LibrarySet(r.Context(), id)
	if err != nil || set.OwnerID != sess.User.ID && !sess.User.Admin {
		if err != nil && !isNotFound(err) {
			s.fail(w, r, err)
		} else {
			s.notFound(w, r)
		}
		return
	}
	if err := s.db.DeleteLibrarySet(r.Context(), id, sess.User.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/library", fmt.Sprintf("Took %q out of the library. Copies already taken stay where they are.", set.Name))
}
