// Package llm knows the language-model providers whose API keys members
// register (members-and-streams.md, Language-model keys), and checks that a
// key works before it is saved. The interpretation itself runs in the
// engine; the Workbench never sends a prompt.
package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Provider is a language-model API a member can register a key for.
type Provider struct {
	ID   string // as stored: "openai", "anthropic"
	Name string
	Keys string // where a member makes a key
	// prefix every key of this provider begins with; empty: none known
	prefix string
}

// Providers lists the providers in the order the page shows them.
var Providers = []Provider{
	{ID: "anthropic", Name: "Anthropic", Keys: "https://console.anthropic.com/settings/keys", prefix: "sk-ant-"},
	{ID: "openai", Name: "OpenAI", Keys: "https://platform.openai.com/api-keys", prefix: "sk-"},
}

// ByID finds a provider.
func ByID(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Hint is what a member sees of a saved key: its last four characters.
func Hint(key string) string {
	if len(key) < 4 {
		return key
	}
	return key[len(key)-4:]
}

// Shape tells a member what is wrong with a key as typed, before any call
// is made; "" when nothing is.
func (p Provider) Shape(key string) string {
	switch {
	case len(key) < 20 || len(key) > 512:
		return "That is not a whole API key: it should be between 20 and 512 characters."
	case strings.ContainsFunc(key, func(r rune) bool { return r <= ' ' || r > '~' }):
		return "An API key has no spaces or special characters. Copy it again from the provider's page."
	case strings.HasPrefix(key, "sk-ant-admin"):
		return "That is an Anthropic admin key. Make an ordinary API key instead: the Workbench needs no more than that."
	case p.ID == "openai" && strings.HasPrefix(key, "sk-ant-"):
		return "That looks like an Anthropic key. Enter it under Anthropic."
	case p.prefix != "" && !strings.HasPrefix(key, p.prefix):
		return fmt.Sprintf("%s API keys begin with %q.", p.Name, p.prefix)
	}
	return ""
}

// Checker asks a provider whether a key works.
type Checker struct {
	// Base addresses per provider ID, without a trailing slash; for tests.
	// A provider missing here uses its public API.
	Base   map[string]string
	Client *http.Client
}

var publicBase = map[string]string{
	"anthropic": "https://api.anthropic.com",
	"openai":    "https://api.openai.com",
}

// Errors of a check.
var (
	// ErrRefused: the provider does not accept the key.
	ErrRefused = errors.New("the provider refused the key")
	// ErrUnreachable: no answer that says either way.
	ErrUnreachable = errors.New("the provider could not be asked")
)

// Check lists the provider's models with the key: a call that is not
// billed. It returns nil when the provider accepts the key, ErrRefused or
// ErrUnreachable (wrapped) otherwise.
func (c *Checker) Check(ctx context.Context, p Provider, key string) error {
	base := c.Base[p.ID]
	if base == "" {
		base = publicBase[p.ID]
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	// Never follow a redirect: it would carry the key elsewhere.
	cl := *client
	cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/models?limit=1", nil)
	if err != nil {
		return err
	}
	switch p.ID {
	case "anthropic":
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnreachable, errors.Unwrap(err))
	}
	resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return nil
	// A rate limit is reached only after the key was accepted.
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (HTTP %d)", ErrRefused, resp.StatusCode)
	default:
		return fmt.Errorf("%w: HTTP %d", ErrUnreachable, resp.StatusCode)
	}
}
