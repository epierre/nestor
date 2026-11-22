// Package credentials encrypts per-user secrets, such as an MCP server token, before they are
// stored. Each ciphertext is bound to the user and credential key it was written for, so a row
// copied to another user or key does not decrypt.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"

	"github.com/epierre/nestor/internal/identity"
)

// KeySize is the length in bytes of an AES-256 key.
const KeySize = 32

// Sealer encrypts and decrypts credentials with AES-256-GCM.
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer returns a Sealer for key, which must be KeySize bytes.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("credentials key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// ParseKey decodes a base64 key, as given in CREDENTIALS_KEY, and checks its length.
func ParseKey(encoded string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("credentials key is not valid base64")
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("credentials key must decode to %d bytes, got %d", KeySize, len(key))
	}
	return key, nil
}

// Seal encrypts plaintext for user under credential key. Each call uses a fresh random nonce.
func (s *Sealer) Seal(user identity.UserID, key string, plaintext []byte) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("read nonce: %w", err)
	}
	return s.aead.Seal(nil, nonce, plaintext, associated(user, key)), nonce, nil
}

// Open decrypts a credential written by Seal for the same user and key. It fails when the key
// differs, the data was altered, or the row belongs to another user or credential key.
func (s *Sealer) Open(user identity.UserID, key string, ciphertext, nonce []byte) ([]byte, error) {
	if len(nonce) != s.aead.NonceSize() {
		return nil, errors.New("credential nonce has the wrong length")
	}
	plaintext, err := s.aead.Open(nil, nonce, ciphertext, associated(user, key))
	if err != nil {
		return nil, errors.New("credential cannot be decrypted")
	}
	return plaintext, nil
}

func associated(user identity.UserID, key string) []byte {
	return []byte(fmt.Sprintf("user=%d;key=%s", user, key))
}
