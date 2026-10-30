package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSealedSecretRoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	enc, err := sealSecret(key, "sk-test-123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, sealedSecretPrefix) || strings.Contains(enc, "sk-test") {
		t.Fatalf("unexpected sealed form %q", enc)
	}
	again, _ := sealSecret(key, "sk-test-123")
	if again == enc {
		t.Fatal("nonce reused: identical ciphertexts")
	}
	plain, err := openSecret(key, enc)
	if err != nil || plain != "sk-test-123" {
		t.Fatalf("open = %q, %v", plain, err)
	}
}

func TestSealedSecretEmpty(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	if enc, err := sealSecret(key, ""); enc != "" || err != nil {
		t.Fatalf("empty seal = %q, %v", enc, err)
	}
	if plain, err := openSecret(key, ""); plain != "" || err != nil {
		t.Fatalf("empty open = %q, %v", plain, err)
	}
}

func TestSealedSecretRejectsWrongKeyTamperingAndForeignFormats(t *testing.T) {
	key := bytes.Repeat([]byte{2}, 32)
	enc, err := sealSecret(key, "secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openSecret(bytes.Repeat([]byte{3}, 32), enc); err == nil {
		t.Fatal("wrong key accepted")
	}
	tampered := []byte(enc)
	tampered[len(tampered)-3] ^= 1
	if _, err := openSecret(key, string(tampered)); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	// A DPAPI blob copied from a Windows history file is plain base64.
	if _, err := openSecret(key, "AQAAANCMnd8BFdERjHoAwE/Cl+s="); err == nil {
		t.Fatal("foreign format accepted")
	}
	if _, err := openSecret(key, sealedSecretPrefix+"AAAA"); err == nil {
		t.Fatal("truncated ciphertext accepted")
	}
	if _, err := sealSecret([]byte("short"), "x"); err == nil {
		t.Fatal("short key accepted")
	}
}
