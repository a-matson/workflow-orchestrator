// Package secrets encrypts workflow secrets at rest. A value is sealed with
// AES-256-GCM under a key from FLUXOR_SECRETS_KEY, with the secret's name as
// associated data, so a ciphertext copied to another name does not open.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the length of the AES-256 key, in bytes.
const KeySize = 32

// ErrNotConfigured means no secrets key was set, so secrets are unavailable.
var ErrNotConfigured = errors.New("secrets store not configured (set FLUXOR_SECRETS_KEY)")

// Box seals and opens secret values.
type Box struct{ aead cipher.AEAD }

// ParseKey decodes a base64 key of KeySize bytes, as FLUXOR_SECRETS_KEY holds it.
func ParseKey(s string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("secrets key: not base64: %w", err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets key: %d bytes, want %d (head -c 32 /dev/urandom | base64)", len(key), KeySize)
	}
	return key, nil
}

func NewBox(key []byte) (*Box, error) {
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

// Seal encrypts value for the secret called name, returning nonce and ciphertext.
func (b *Box) Seal(name string, value []byte) (nonce, ciphertext []byte, err error) {
	nonce = make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, b.aead.Seal(nil, nonce, value, []byte(name)), nil
}

// Open decrypts the secret called name.
func (b *Box) Open(name string, nonce, ciphertext []byte) ([]byte, error) {
	v, err := b.aead.Open(nil, nonce, ciphertext, []byte(name))
	if err != nil {
		return nil, fmt.Errorf("secret %q does not decrypt with this key", name)
	}
	return v, nil
}
