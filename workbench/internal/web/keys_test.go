package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vrontier/listen-workbench/internal/catalogue"
	"github.com/vrontier/listen-workbench/internal/llm"
	"github.com/vrontier/listen-workbench/internal/secret"
)

const (
	goodKey = "sk-ant-api03-good-0123456789abcd"
	goneKey = "sk-ant-api03-gone-0123456789wxyz"
)

func TestLanguageModelKeys(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	u, _ := r.db.CreateUser(ctx, "m@example.org", "M", false)
	r.db.CreateUser(ctx, "o@example.org", "O", false)

	// A provider that accepts goodKey, until it is "deleted on their side".
	var calls, revoked atomic.Int32
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		k := req.Header.Get("x-api-key")
		if k == goodKey && revoked.Load() == 0 {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer prov.Close()
	r.srv.cfg.LLM = &llm.Checker{Base: map[string]string{"anthropic": prov.URL, "openai": prov.URL}}

	member := r.loginAs("m@example.org")
	// Off by default: no page, no menu entry.
	if code, _, _ := r.get(member, "/account/keys"); code != 404 {
		t.Fatalf("keys page while off: %d", code)
	}
	if _, page, _ := r.get(member, "/analyses"); strings.Contains(page, `href="/account/keys"`) {
		t.Fatal("menu offers keys while off")
	}
	r.srv.cfg.LLMKeys = true
	_, page, _ := r.get(member, "/analyses")
	if !strings.Contains(page, `href="/account/keys"`) {
		t.Fatal("menu does not offer keys while on")
	}
	_, page, _ = r.get(member, "/account/keys")
	csrf := csrfOf(t, page)
	if !strings.Contains(page, "no sealing key set") {
		t.Fatal("the page does not say that keys cannot be kept")
	}
	if code, page, _ := r.post(member, "/account/keys/anthropic", url.Values{"csrf": {csrf}, "key": {goodKey}}); code != 400 || !strings.Contains(page, "no sealing key") {
		t.Fatalf("saved without a sealing key: %d", code)
	}

	box, _ := secret.New(make([]byte, secret.KeySize))
	r.srv.cfg.Seal = box

	// Wrong shapes are refused without asking the provider.
	for _, k := range []string{"sk-short", "sk-proj-abcdefghijklmnopqrstuv", "sk-ant-admin01-abcdefghijklmnop"} {
		if code, _, _ := r.post(member, "/account/keys/anthropic", url.Values{"csrf": {csrf}, "key": {k}}); code != 400 {
			t.Fatalf("%s: %d", k, code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("the provider was asked about a malformed key")
	}
	// A key the provider refuses is not saved.
	if code, page, _ := r.post(member, "/account/keys/anthropic", url.Values{"csrf": {csrf}, "key": {goneKey}}); code != 400 || !strings.Contains(page, "does not accept") {
		t.Fatalf("refused key: %d", code)
	}
	if keys, _ := r.db.LLMKeysOf(ctx, u.ID); len(keys) != 0 {
		t.Fatal("a refused key was saved")
	}

	if code, _, _ := r.post(member, "/account/keys/anthropic", url.Values{"csrf": {csrf}, "key": {" " + goodKey + "\n"}}); code != http.StatusSeeOther {
		t.Fatalf("save: %d", code)
	}
	_, page, _ = r.get(member, "/account/keys")
	if strings.Contains(page, goodKey) || !strings.Contains(page, "…abcd") || !strings.Contains(page, "accepted the key") {
		t.Fatal("the page shows the key, or not its end")
	}
	sealed, err := r.db.SealedLLMKey(ctx, u.ID, "anthropic")
	if err != nil || strings.Contains(string(sealed), "good") {
		t.Fatalf("stored in the clear: %v", err)
	}
	if plain, err := box.Open(sealed, catalogue.LLMKeyPurpose(u.ID, "anthropic")); err != nil || string(plain) != goodKey {
		t.Fatalf("does not open: %v", err)
	}
	// Bound to its member: copied to another member's row, it does not open.
	if _, err := box.Open(sealed, catalogue.LLMKeyPurpose(u.ID+1, "anthropic")); err == nil {
		t.Fatal("opens for another member")
	}
	// Nowhere in the audit trail.
	entries, _ := r.db.AuditAll(ctx)
	for _, e := range entries {
		if strings.Contains(e.Before+e.After, "good-") {
			t.Fatal("the key is in the audit trail")
		}
	}

	// Another member sees none of it.
	other := r.loginAs("o@example.org")
	if _, page, _ := r.get(other, "/account/keys"); strings.Contains(page, "…abcd") {
		t.Fatal("another member sees the key")
	}

	// Checked again after the provider dropped it.
	revoked.Store(1)
	if code, page, _ := r.post(member, "/account/keys/anthropic/check", url.Values{"csrf": {csrf}}); code != 400 || !strings.Contains(page, "refused") {
		t.Fatalf("check of a dropped key: %d", code)
	}
	if keys, _ := r.db.LLMKeysOf(ctx, u.ID); len(keys) != 1 || keys[0].Refused.IsZero() {
		t.Fatal("the refusal is not noted")
	}

	// The limit on checks: ten an hour, counting the ones above (refused
	// save, save, check).
	for i := 0; i < 7; i++ {
		r.post(member, "/account/keys/anthropic/check", url.Values{"csrf": {csrf}})
	}
	before := calls.Load()
	if code, page, _ := r.post(member, "/account/keys/anthropic/check", url.Values{"csrf": {csrf}}); code != 400 || !strings.Contains(page, "ten times") || calls.Load() != before {
		t.Fatalf("eleventh check: %d", code)
	}

	if code, _, _ := r.post(member, "/account/keys/anthropic/delete", url.Values{"csrf": {csrf}}); code != http.StatusSeeOther {
		t.Fatalf("delete: %d", code)
	}
	if _, err := r.db.SealedLLMKey(ctx, u.ID, "anthropic"); err != catalogue.ErrNotFound {
		t.Fatalf("still there: %v", err)
	}
	if code, _, _ := r.post(member, "/account/keys/nobody", url.Values{"csrf": {csrf}, "key": {goodKey}}); code != 404 {
		t.Fatalf("unknown provider: %d", code)
	}
}
