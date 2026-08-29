package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

var ErrUnavailable = errors.New("secret storage is unavailable: configure TAILCAT_WEBUI_MASTER_KEY")

type Box struct {
	aead cipher.AEAD
}

func NewBox(key []byte) (*Box, error) {
	if len(key) == 0 {
		return &Box{}, nil
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secret box: key length %d, want 32", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secret box: create cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret box: create GCM: %w", err)
	}
	return &Box{aead: aead}, nil
}

func (b *Box) Available() bool { return b != nil && b.aead != nil }

func (b *Box) Seal(plaintext []byte, associatedData string) ([]byte, error) {
	if !b.Available() {
		return nil, ErrUnavailable
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secret box: random nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, []byte(associatedData)), nil
}

func (b *Box) Open(ciphertext []byte, associatedData string) ([]byte, error) {
	if !b.Available() {
		return nil, ErrUnavailable
	}
	if len(ciphertext) < b.aead.NonceSize() {
		return nil, errors.New("secret box: ciphertext is truncated")
	}
	nonce, body := ciphertext[:b.aead.NonceSize()], ciphertext[b.aead.NonceSize():]
	plaintext, err := b.aead.Open(nil, nonce, body, []byte(associatedData))
	if err != nil {
		return nil, errors.New("secret box: authentication failed")
	}
	return plaintext, nil
}
