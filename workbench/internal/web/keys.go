package web

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/vrontier/listen/workbench/internal/catalogue"
	"github.com/vrontier/listen/workbench/internal/llm"
)

// The member's language-model keys (members-and-streams.md, Language-model
// keys).

// keyChecks bounds the calls to providers one member can cause, so that the
// page cannot be used to try out keys found elsewhere.
const (
	keyChecks      = 10
	keyChecksSpell = time.Hour
)

type checkLimit struct {
	mu   sync.Mutex
	seen map[int64][]time.Time
}

// allow notes a check for a member and reports whether it is within the
// limit.
func (l *checkLimit) allow(userID int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[int64][]time.Time{}
	}
	kept := l.seen[userID][:0]
	for _, t := range l.seen[userID] {
		if now.Sub(t) < keyChecksSpell {
			kept = append(kept, t)
		}
	}
	if len(kept) >= keyChecks {
		l.seen[userID] = kept
		return false
	}
	l.seen[userID] = append(kept, now)
	return true
}

type keyRow struct {
	Provider llm.Provider
	Key      *catalogue.LLMKey
	Err      string // for this provider's form
}

type keysData struct {
	Ready bool // the server has a sealing key
	Rows  []keyRow
}

func (s *Server) keysData(r *http.Request) (keysData, error) {
	sess, _ := sessionFrom(r)
	d := keysData{Ready: s.cfg.Seal != nil}
	keys, err := s.db.LLMKeysOf(r.Context(), sess.User.ID)
	if err != nil {
		return d, err
	}
	for _, p := range llm.Providers {
		row := keyRow{Provider: p}
		for i := range keys {
			if keys[i].Provider == p.ID {
				row.Key = &keys[i]
			}
		}
		d.Rows = append(d.Rows, row)
	}
	return d, nil
}

func (s *Server) keysPage(w http.ResponseWriter, r *http.Request) {
	d, err := s.keysData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, 200, "keys", "Language-model keys", d)
}

// keysError shows the page again with a message at one provider's form.
func (s *Server) keysError(w http.ResponseWriter, r *http.Request, provider, msg string) {
	d, err := s.keysData(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for i := range d.Rows {
		if d.Rows[i].Provider.ID == provider {
			d.Rows[i].Err = msg
		}
	}
	s.render(w, r, 400, "keys", "Language-model keys", d)
}

// checkMessage says in words why a check did not pass.
func checkMessage(p llm.Provider, err error) string {
	switch {
	case errors.Is(err, llm.ErrRefused):
		return p.Name + " does not accept this key. Check that it was copied whole and has not been deleted on their side."
	case errors.Is(err, llm.ErrUnreachable):
		return p.Name + " could not be asked whether the key works. Try again in a while."
	}
	return "The key could not be checked."
}

// keySave checks a key with its provider and saves it sealed. The key is
// never shown again, and never written to the log.
func (s *Server) keySave(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	p, ok := llm.ByID(r.PathValue("provider"))
	if !ok {
		s.notFound(w, r)
		return
	}
	if s.cfg.Seal == nil {
		s.keysError(w, r, p.ID, "Not saved: this server has no sealing key set, so it cannot keep API keys. Ask the master admin.")
		return
	}
	key := strings.TrimSpace(r.PostFormValue("key"))
	if msg := p.Shape(key); msg != "" {
		s.keysError(w, r, p.ID, "Not saved. "+msg)
		return
	}
	if !s.keyChecks.allow(sess.User.ID, time.Now()) {
		s.keysError(w, r, p.ID, "Not saved: you have checked keys ten times in the last hour. Try again later.")
		return
	}
	if err := s.cfg.LLM.Check(r.Context(), p, key); err != nil {
		s.keysError(w, r, p.ID, "Not saved. "+checkMessage(p, err))
		return
	}
	sealed := s.cfg.Seal.Seal([]byte(key), catalogue.LLMKeyPurpose(sess.User.ID, p.ID))
	if err := s.db.SetLLMKey(r.Context(), sess.User.ID, p.ID, sealed, llm.Hint(key)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/account/keys", p.Name+" accepted the key, and it is saved.")
}

// keyCheck asks the provider again whether a saved key works.
func (s *Server) keyCheck(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	p, ok := llm.ByID(r.PathValue("provider"))
	if !ok {
		s.notFound(w, r)
		return
	}
	sealed, err := s.db.SealedLLMKey(r.Context(), sess.User.ID, p.ID)
	if isNotFound(err) {
		s.redirect(w, r, "/account/keys", "There is no "+p.Name+" key to check.")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if s.cfg.Seal == nil {
		s.keysError(w, r, p.ID, "Not checked: this server has no sealing key set. Ask the master admin.")
		return
	}
	key, err := s.cfg.Seal.Open(sealed, catalogue.LLMKeyPurpose(sess.User.ID, p.ID))
	if err != nil {
		s.keysError(w, r, p.ID, "The saved key cannot be opened with this server's sealing key, which has changed since it was saved. Delete it and enter it again.")
		return
	}
	if !s.keyChecks.allow(sess.User.ID, time.Now()) {
		s.keysError(w, r, p.ID, "Not checked: you have checked keys ten times in the last hour. Try again later.")
		return
	}
	err = s.cfg.LLM.Check(r.Context(), p, string(key))
	switch {
	case err == nil:
		s.db.NoteLLMKeyCheck(r.Context(), sess.User.ID, p.ID, true)
		s.redirect(w, r, "/account/keys", p.Name+" accepts the key.")
	case errors.Is(err, llm.ErrRefused):
		s.db.NoteLLMKeyCheck(r.Context(), sess.User.ID, p.ID, false)
		s.keysError(w, r, p.ID, checkMessage(p, err))
	default:
		s.keysError(w, r, p.ID, checkMessage(p, err))
	}
}

func (s *Server) keyDelete(w http.ResponseWriter, r *http.Request) {
	sess, _ := sessionFrom(r)
	p, ok := llm.ByID(r.PathValue("provider"))
	if !ok {
		s.notFound(w, r)
		return
	}
	if err := s.db.DeleteLLMKey(r.Context(), sess.User.ID, p.ID); err != nil {
		if isNotFound(err) {
			s.redirect(w, r, "/account/keys", "That key was deleted already.")
			return
		}
		s.fail(w, r, err)
		return
	}
	s.redirect(w, r, "/account/keys", "Your "+p.Name+" key is deleted here. To end it for good, delete it on "+p.Name+"'s page as well.")
}
