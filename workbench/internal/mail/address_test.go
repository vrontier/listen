package mail

import "testing"

func TestValidAddress(t *testing.T) {
	good := []string{"someone@example.lu", "a.b+tag@sub.example.org", "x@xn--bcher-kva.example", "Someone@Example.ORG", "o'neil@example.ie"}
	bad := []string{"someone@examplelu", "someone@example", "someone@", "@example.org", "someone", "some one@example.org",
		"Some One <someone@example.org>", "someone@example..org", "someone@-example.org", "someone@example.o", "someone@example.123",
		"someone@exa_mple.org", "someone@example.org.", ""}
	for _, s := range good {
		if !ValidAddress(s) {
			t.Errorf("refused %q", s)
		}
	}
	for _, s := range bad {
		if ValidAddress(s) {
			t.Errorf("accepted %q", s)
		}
	}
}
