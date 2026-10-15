package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestSealOpen(t *testing.T) {
	key := make([]byte, KeySize)
	rand.Read(key)
	b, err := Parse(base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	sealed := b.Seal([]byte("sk-example"), "llm:1:openai")
	if bytes.Contains(sealed, []byte("sk-example")) {
		t.Fatal("sealed value contains the plain text")
	}
	plain, err := b.Open(sealed, "llm:1:openai")
	if err != nil || string(plain) != "sk-example" {
		t.Fatalf("open: %q, %v", plain, err)
	}
	if _, err := b.Open(sealed, "llm:2:openai"); err != ErrOpen {
		t.Fatalf("opened for another purpose: %v", err)
	}
	other, _ := New(make([]byte, KeySize))
	if _, err := other.Open(sealed, "llm:1:openai"); err != ErrOpen {
		t.Fatalf("opened with another key: %v", err)
	}
	if _, err := Parse("c2hvcnQ="); err == nil {
		t.Fatal("a short key was taken")
	}
}
