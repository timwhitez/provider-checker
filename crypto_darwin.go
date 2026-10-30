//go:build darwin && cgo

package main

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static CFMutableDictionaryRef pcKeychainQuery(const char *service, const char *account) {
	CFMutableDictionaryRef q = CFDictionaryCreateMutable(NULL, 0,
		&kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	CFStringRef s = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
	CFStringRef a = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
	CFDictionarySetValue(q, kSecClass, kSecClassGenericPassword);
	CFDictionarySetValue(q, kSecAttrService, s);
	CFDictionarySetValue(q, kSecAttrAccount, a);
	CFRelease(s);
	CFRelease(a);
	return q;
}

// pcKeychainGet copies the generic password data into a malloc'd buffer.
static int pcKeychainGet(const char *service, const char *account, void **out, int *outLen) {
	*out = NULL;
	*outLen = 0;
	CFMutableDictionaryRef q = pcKeychainQuery(service, account);
	CFDictionarySetValue(q, kSecReturnData, kCFBooleanTrue);
	CFDictionarySetValue(q, kSecMatchLimit, kSecMatchLimitOne);
	CFTypeRef result = NULL;
	OSStatus status = SecItemCopyMatching(q, &result);
	CFRelease(q);
	if (status != errSecSuccess) return (int)status;
	if (result == NULL || CFGetTypeID(result) != CFDataGetTypeID()) {
		if (result != NULL) CFRelease(result);
		return (int)errSecDecode;
	}
	CFIndex n = CFDataGetLength((CFDataRef)result);
	void *buf = malloc(n > 0 ? (size_t)n : 1);
	if (n > 0) memcpy(buf, CFDataGetBytePtr((CFDataRef)result), (size_t)n);
	CFRelease(result);
	*out = buf;
	*outLen = (int)n;
	return 0;
}

static int pcKeychainAdd(const char *service, const char *account, const char *label, const void *data, int len) {
	CFMutableDictionaryRef q = pcKeychainQuery(service, account);
	CFDataRef d = CFDataCreate(NULL, (const UInt8 *)data, len);
	CFStringRef l = CFStringCreateWithCString(NULL, label, kCFStringEncodingUTF8);
	CFDictionarySetValue(q, kSecValueData, d);
	CFDictionarySetValue(q, kSecAttrLabel, l);
	OSStatus status = SecItemAdd(q, NULL);
	CFRelease(d);
	CFRelease(l);
	CFRelease(q);
	return (int)status;
}

// pcKeychainMessage returns a malloc'd description of an OSStatus, or NULL.
static char *pcKeychainMessage(int status) {
	CFStringRef s = SecCopyErrorMessageString((OSStatus)status, NULL);
	if (s == NULL) return NULL;
	CFIndex max = CFStringGetMaximumSizeForEncoding(CFStringGetLength(s), kCFStringEncodingUTF8) + 1;
	char *buf = malloc((size_t)max);
	if (!CFStringGetCString(s, buf, max, kCFStringEncodingUTF8)) {
		free(buf);
		buf = NULL;
	}
	CFRelease(s);
	return buf;
}
*/
import "C"

import (
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// API-key-at-rest encryption on macOS: a random 256-bit key is created once
// and stored as a generic password in the user's login Keychain; API keys are
// sealed with it (see secret_seal.go). Nothing is written in plaintext and the
// key never leaves the Keychain except in this process's memory.

const (
	keychainService = "io.github.timwhitez.provider-checker"
	keychainAccount = "history-api-key-encryption"
	keychainLabel   = "Provider Checker history key"

	errSecItemNotFound  = -25300
	errSecDuplicateItem = -25299
)

var historyKeyCache struct {
	sync.Mutex
	key []byte
}

type keychainError struct {
	op     string
	status int
}

func (e keychainError) Error() string {
	msg := "unknown error"
	if p := C.pcKeychainMessage(C.int(e.status)); p != nil {
		msg = C.GoString(p)
		C.free(unsafe.Pointer(p))
	}
	return fmt.Sprintf("Keychain %s failed (OSStatus %d: %s)", e.op, e.status, msg)
}

func keychainGet() ([]byte, int) {
	svc, acc := C.CString(keychainService), C.CString(keychainAccount)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acc))
	var out unsafe.Pointer
	var n C.int
	if status := C.pcKeychainGet(svc, acc, &out, &n); status != 0 {
		return nil, int(status)
	}
	defer C.free(out)
	return C.GoBytes(out, n), 0
}

func keychainAdd(data []byte) int {
	svc, acc, label := C.CString(keychainService), C.CString(keychainAccount), C.CString(keychainLabel)
	defer C.free(unsafe.Pointer(svc))
	defer C.free(unsafe.Pointer(acc))
	defer C.free(unsafe.Pointer(label))
	return int(C.pcKeychainAdd(svc, acc, label, unsafe.Pointer(&data[0]), C.int(len(data))))
}

// historyKey returns the Keychain-held sealing key, creating it on first use
// when create is set. Only a successfully loaded key is cached, so a denied
// Keychain prompt can be retried.
func historyKey(create bool) ([]byte, error) {
	historyKeyCache.Lock()
	defer historyKeyCache.Unlock()
	if historyKeyCache.key != nil {
		return historyKeyCache.key, nil
	}
	key, status := keychainGet()
	if status == errSecItemNotFound && create {
		fresh := make([]byte, 32)
		if _, err := rand.Read(fresh); err != nil {
			return nil, err
		}
		switch added := keychainAdd(fresh); added {
		case 0:
			key, status = fresh, 0
		case errSecDuplicateItem: // created concurrently by another instance
			key, status = keychainGet()
		default:
			return nil, keychainError{op: "add", status: added}
		}
	}
	if status == errSecItemNotFound {
		return nil, errors.New("history encryption key not found in the Keychain")
	}
	if status != 0 {
		return nil, keychainError{op: "read", status: status}
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("history encryption key in the Keychain has unexpected length %d", len(key))
	}
	historyKeyCache.key = key
	return key, nil
}

// encryptSecret seals plaintext with the Keychain key. An empty input yields
// an empty output and never touches the Keychain.
func encryptSecret(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key, err := historyKey(true)
	if err != nil {
		return "", err
	}
	return sealSecret(key, plaintext)
}

// decryptSecret reverses encryptSecret. An empty input yields an empty string.
func decryptSecret(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	key, err := historyKey(false)
	if err != nil {
		return "", err
	}
	return openSecret(key, enc)
}
