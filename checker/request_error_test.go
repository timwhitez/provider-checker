package checker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

type requestErrorTransport func(*http.Request) (*http.Response, error)

func (f requestErrorTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestGeminiRequestErrors(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, key := range []string{"synthetic-secret", "synthetic+/%/&/ secret"} {
		for _, cause := range []error{errors.New("connection refused"), context.DeadlineExceeded, context.Canceled} {
			for _, feature := range append((GeminiChecker{}).Supported(), "list") {
				t.Run(key+"/"+cause.Error()+"/"+feature, func(t *testing.T) {
					calls := 0
					http.DefaultTransport = requestErrorTransport(func(req *http.Request) (*http.Response, error) {
						calls++
						if req.URL.Query().Get("key") != key {
							t.Fatal("request authentication changed")
						}
						// The client adds another url.Error around this transport error.
						return nil, &url.Error{Op: "proxy", URL: req.URL.String(), Err: cause}
					})
					cfg := Config{BaseURL: "https://example.invalid", Model: "synthetic", APIKey: key}
					var diagnostic string
					if feature == "list" {
						_, err := ListModels(context.Background(), GeminiChecker{}, cfg)
						if !errors.Is(err, cause) || os.IsTimeout(err) != (cause == context.DeadlineExceeded) {
							t.Fatalf("error classification lost: %v", err)
						}
						diagnostic = err.Error()
					} else {
						result := (GeminiChecker{}).Test(context.Background(), cfg, feature, "")
						if result.Status != StatusFail {
							t.Fatalf("status = %v, want FAIL", result.Status)
						}
						diagnostic = result.Error
					}
					if calls != 1 {
						t.Fatalf("requests = %d, want 1", calls)
					}
					for _, secret := range []string{key, url.QueryEscape(key), url.PathEscape(key)} {
						if strings.Contains(diagnostic, secret) {
							t.Errorf("diagnostic contains request credential: %q", diagnostic)
						}
					}
					for _, want := range []string{"example.invalid", cause.Error(), "proxy"} {
						if !strings.Contains(diagnostic, want) {
							t.Errorf("diagnostic lost %q: %q", want, diagnostic)
						}
					}
				})
			}
		}
	}
}

func TestRequestConstructionErrorsHideKey(t *testing.T) {
	key := "synthetic+/%/&/ secret"
	cfg := Config{APIKey: key}
	endpoint := "https://example.invalid/bad%escape?key=" + url.QueryEscape(key)
	for _, method := range []string{"post", "get"} {
		var err error
		if method == "post" {
			_, err = postJSON(context.Background(), cfg, endpoint, nil, nil)
		} else {
			err = getJSON(context.Background(), cfg, endpoint, nil, new(any))
		}
		if err == nil || !strings.Contains(err.Error(), "invalid URL escape") {
			t.Fatalf("%s: missing construction error: %v", method, err)
		}
		if strings.Contains(err.Error(), url.QueryEscape(key)) || strings.Contains(err.Error(), key) {
			t.Errorf("%s: credential in construction error: %v", method, err)
		}
	}
}

func TestGeminiRequestControls(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, key := range []string{"", "synthetic-secret"} {
		http.DefaultTransport = requestErrorTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("key") != key {
				t.Fatal("request authentication changed")
			}
			body := `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`
			if req.Method == http.MethodGet {
				body = `{"models":[{"name":"models/b"},{"name":"models/a"},{"name":"models/a"}]}`
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		cfg := Config{BaseURL: "https://example.invalid", Model: "synthetic", APIKey: key}
		result := (GeminiChecker{}).Test(context.Background(), cfg, "basic", "")
		if result.Status != StatusPass || result.Detail != "hello" {
			t.Fatalf("success changed: %+v", result)
		}
		models, err := ListModels(context.Background(), GeminiChecker{}, cfg)
		if err != nil || fmt.Sprint(models) != "[a b]" {
			t.Fatalf("list changed: %v %v", models, err)
		}
	}
}

func TestGeminiRequestErrorTextVariants(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	for _, key := range []string{"", "synthetic\"/\tsecret"} {
		cause := fmt.Errorf("connection refused (%q / %s)", key, key)
		http.DefaultTransport = requestErrorTransport(func(req *http.Request) (*http.Response, error) {
			return nil, cause
		})
		_, err := ListModels(context.Background(), GeminiChecker{}, Config{BaseURL: "https://example.invalid", APIKey: key})
		if !errors.Is(err, cause) {
			t.Fatalf("cause lost: %v", err)
		}
		if key == "" {
			if !strings.Contains(err.Error(), cause.Error()) {
				t.Fatalf("no-key error changed: %v", err)
			}
			continue
		}
		quoted := fmt.Sprintf("%q", key)
		for _, secret := range []string{key, quoted[1 : len(quoted)-1], url.QueryEscape(key)} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("credential in transport error: %v", err)
			}
		}
	}
}
