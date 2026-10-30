//go:build darwin && cgo

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestKeychainRoundTrip exercises the real login Keychain with a throwaway
// item that it deletes afterwards. It is opt-in because CI keychains may be
// locked or prompt: PC_KEYCHAIN_TEST=1 go test -run Keychain .
func TestKeychainRoundTrip(t *testing.T) {
	if os.Getenv("PC_KEYCHAIN_TEST") != "1" {
		t.Skip("set PC_KEYCHAIN_TEST=1 to use the real login Keychain")
	}
	origService := keychainService
	keychainService = fmt.Sprintf("%s.test-%d", origService, time.Now().UnixNano())
	resetKey := func() {
		historyKeyCache.Lock()
		historyKeyCache.key = nil
		historyKeyCache.Unlock()
	}
	resetKey()
	t.Cleanup(func() {
		if err := keychainDelete(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		keychainService = origService
		resetKey()
	})

	// Without an item, decrypting must fail and must not create one.
	if _, err := decryptSecret("v1:AAAA"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("decrypt without key: %v", err)
	}
	if _, status := keychainGet(); status != errSecItemNotFound {
		t.Fatalf("decrypt created an item (status %d)", status)
	}
	// Empty values never touch the Keychain.
	if enc, err := encryptSecret(""); enc != "" || err != nil {
		t.Fatalf("empty encrypt = %q, %v", enc, err)
	}
	if _, status := keychainGet(); status != errSecItemNotFound {
		t.Fatal("empty encrypt created an item")
	}

	enc, err := encryptSecret("sk-keychain-test")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, sealedSecretPrefix) || strings.Contains(enc, "sk-keychain") {
		t.Fatalf("ciphertext %q", enc)
	}
	stored, status := keychainGet()
	if status != 0 || len(stored) != 32 {
		t.Fatalf("stored key: status %d, %d bytes", status, len(stored))
	}

	// A fresh process (empty cache) decrypts with the stored key.
	resetKey()
	plain, err := decryptSecret(enc)
	if err != nil || plain != "sk-keychain-test" {
		t.Fatalf("decrypt after reload = %q, %v", plain, err)
	}
	// A second encrypt reuses the same key instead of replacing it.
	if _, err := encryptSecret("other"); err != nil {
		t.Fatal(err)
	}
	if again, _ := keychainGet(); string(again) != string(stored) {
		t.Fatal("key replaced")
	}

	// Losing the item makes old ciphertexts undecryptable rather than wrong.
	if err := keychainDelete(); err != nil {
		t.Fatal(err)
	}
	resetKey()
	if _, err := encryptSecret("new"); err != nil {
		t.Fatal(err)
	}
	if _, err := decryptSecret(enc); err == nil {
		t.Fatal("old ciphertext decrypted with a new key")
	}
}
