package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Sealed secrets are the macOS at-rest format for history API keys:
// "v1:" + base64(nonce || AES-256-GCM ciphertext). The 32-byte key lives in
// the user's login Keychain (crypto_darwin.go), so - like DPAPI on Windows -
// a copied history file cannot be decrypted by another user or machine. The
// sealing itself is pure Go so it can be tested on every platform.

const sealedSecretPrefix = "v1:"

// sealedSecretAAD binds ciphertexts to their purpose.
var sealedSecretAAD = []byte("provider-checker/history/api-key/v1")

func newSecretAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// sealSecret encrypts plaintext with key. An empty input yields an empty output.
func sealSecret(key []byte, plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	aead, err := newSecretAEAD(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), sealedSecretAAD)
	return sealedSecretPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// openSecret reverses sealSecret. An empty input yields an empty string.
func openSecret(key []byte, enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	if !strings.HasPrefix(enc, sealedSecretPrefix) {
		return "", errors.New("unsupported API key format (not written by the macOS build)")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(enc, sealedSecretPrefix))
	if err != nil {
		return "", err
	}
	aead, err := newSecretAEAD(key)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize()+aead.Overhead() {
		return "", errors.New("encrypted API key is truncated")
	}
	nonce, body := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, body, sealedSecretAAD)
	if err != nil {
		return "", errors.New("encrypted API key cannot be decrypted with this Keychain key")
	}
	return string(plain), nil
}
