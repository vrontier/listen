package mail

import (
	"net/mail"
	"strings"
)

// ValidAddress reports whether s is a plain address that mail can be
// delivered to: no display name, a local part, and a domain of at least two
// labels whose last one is a top-level domain (so "someone@example" and
// "someone@examplelu" are refused, a typo of "someone@example.lu").
func ValidAddress(s string) bool {
	if len(s) > 254 || strings.ContainsAny(s, " <>\"\r\n") {
		return false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	local, domain := s[:at], strings.ToLower(s[at+1:])
	if local == "" || len(local) > 64 {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	if strings.HasPrefix(tld, "xn--") {
		return len(tld) > 4
	}
	if len(tld) < 2 {
		return false
	}
	for _, c := range tld {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}
