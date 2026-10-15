package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestShape(t *testing.T) {
	a, _ := ByID("anthropic")
	o, _ := ByID("openai")
	for _, c := range []struct {
		p    Provider
		key  string
		fine bool
	}{
		{a, "sk-ant-api03-abcdefghijklmnop", true},
		{a, "sk-ant-admin01-abcdefghijklmnop", false},
		{a, "sk-proj-abcdefghijklmnopqrst", false},
		{o, "sk-proj-abcdefghijklmnopqrst", true},
		{o, "sk-ant-api03-abcdefghijklmnop", false},
		{o, "sk-short", false},
		{o, "sk-proj-abcdefgh ijklmnopqrst", false},
	} {
		if got := c.p.Shape(c.key) == ""; got != c.fine {
			t.Errorf("%s %q: fine %v, want %v", c.p.ID, c.key, got, c.fine)
		}
	}
	if Hint("sk-proj-abcd1234") != "1234" {
		t.Fatal("hint")
	}
}

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		key := r.Header.Get("x-api-key")
		if r.Header.Get("Authorization") != "" {
			key = r.Header.Get("Authorization")[len("Bearer "):]
		} else if r.Header.Get("anthropic-version") == "" {
			w.WriteHeader(400)
			return
		}
		switch key {
		case "good":
			w.Write([]byte(`{"data":[]}`))
		case "busy":
			w.WriteHeader(429)
		case "away":
			http.Redirect(w, r, "https://example.org/", http.StatusFound)
		case "down":
			w.WriteHeader(503)
		default:
			w.WriteHeader(401)
		}
	}))
	defer srv.Close()
	c := &Checker{Base: map[string]string{"anthropic": srv.URL, "openai": srv.URL}}
	ctx := context.Background()
	for _, p := range Providers {
		for key, want := range map[string]error{"good": nil, "busy": nil, "bad": ErrRefused, "down": ErrUnreachable, "away": ErrUnreachable} {
			if err := c.Check(ctx, p, key); !errors.Is(err, want) {
				t.Errorf("%s %s: %v, want %v", p.ID, key, err, want)
			}
		}
	}
	srv.Close()
	if err := c.Check(ctx, Providers[0], "good"); !errors.Is(err, ErrUnreachable) {
		t.Errorf("closed server: %v", err)
	}
}
