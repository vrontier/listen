// Package secret seals values the catalogue keeps but must not reveal on its
// own: members' language-model API keys (members-and-streams.md,
// Language-model keys). The sealing key is part of the server's settings,
// not of the catalogue, so a copy of the database alone does not open them.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// KeySize is the length of a sealing key: AES-256.
const KeySize = 32

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// ErrOpen is returned for a value that was sealed with another key, for
// another purpose, or changed since.
var ErrOpen = errors.New("sealed value cannot be opened")

// New makes a box from a key of KeySize bytes.
func New(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("sealing key has %d bytes, needs %d", len(key), KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Parse makes a box from a key written in base64, as in the settings file
// (`openssl rand -base64 32`).
func Parse(s string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("sealing key is not base64: %w", err)
	}
	return New(key)
}

// Seal encrypts plain. purpose binds the result to its place, so that a
// sealed value copied to another member's row does not open there.
func (b *Box) Seal(plain []byte, purpose string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plain, []byte(purpose))
}

// Open decrypts what Seal made for the same purpose.
func (b *Box) Open(sealed []byte, purpose string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrOpen
	}
	plain, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(purpose))
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}
