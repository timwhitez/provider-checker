//go:build windows

package main

import (
	"encoding/base64"
	"syscall"
	"unsafe"
)

// API-key-at-rest encryption uses the Windows Data Protection API (DPAPI).
// Ciphertext is bound to the current Windows user account, so the stored value
// cannot be decrypted by another user or on another machine, and no key
// material has to be embedded in or managed by the application.

var (
	modCrypt32              = syscall.NewLazyDLL("crypt32.dll")
	modKernel32             = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtectData    = modCrypt32.NewProc("CryptProtectData")
	procCryptUnprotectData  = modCrypt32.NewProc("CryptUnprotectData")
	procLocalFree           = modKernel32.NewProc("LocalFree")
)

// dataBlob mirrors the Win32 DATA_BLOB structure.
type dataBlob struct {
	cbData uint32
	pbData *byte
}

func newBlob(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b dataBlob) bytes() []byte {
	if b.cbData == 0 || b.pbData == nil {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

// encryptSecret encrypts plaintext with DPAPI and returns a base64 string.
// An empty input yields an empty output (nothing to store).
func encryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	in := newBlob([]byte(plaintext))
	var out dataBlob
	// CryptProtectData(pDataIn, szDescr, pOptionalEntropy, pvReserved,
	//   pPromptStruct, dwFlags, pDataOut). dwFlags=0.
	r, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return "", err
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return base64.StdEncoding.EncodeToString(out.bytes()), nil
}

// decryptSecret reverses encryptSecret. An empty input yields an empty string.
func decryptSecret(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	in := newBlob(raw)
	var out dataBlob
	r, _, cerr := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0, 0, 0, 0, 0,
		uintptr(unsafe.Pointer(&out)),
	)
	if r == 0 {
		return "", cerr
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(out.pbData)))
	return string(out.bytes()), nil
}
