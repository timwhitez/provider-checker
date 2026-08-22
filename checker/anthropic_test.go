package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const anthropicTestSecret = "test-secret"

func TestAnthropicBasicUsesXAPIKeyWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("x-api-key"); got != anthropicTestSecret {
			t.Errorf("x-api-key = %q, want test secret", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want empty", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Errorf("anthropic-version = %q", got)
		}
		writeAnthropicText(t, w, "hello")
	}))
	defer server.Close()

	result := (AnthropicChecker{}).Test(context.Background(), Config{
		BaseURL: server.URL,
		APIKey:  anthropicTestSecret,
		Model:   "claude-test",
	}, "basic", "hello")

	if result.Status != StatusPass {
		t.Fatalf("status = %s, error = %s", result.Status, result.Error)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func TestAnthropicBasicRetriesWithBearerAfter401(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		switch call {
		case 1:
			if got := r.Header.Get("x-api-key"); got != anthropicTestSecret {
				t.Errorf("first x-api-key = %q, want test secret", got)
			}
			if got := r.Header.Get("Authorization"); got != "" {
				t.Errorf("first Authorization = %q, want empty", got)
			}
			http.Error(w, `{"error":{"message":"missing token"}}`, http.StatusUnauthorized)
		case 2:
			if got := r.Header.Get("x-api-key"); got != "" {
				t.Errorf("second x-api-key = %q, want empty", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer "+anthropicTestSecret {
				t.Errorf("second Authorization = %q, want Bearer token", got)
			}
			writeAnthropicText(t, w, "hello from bearer")
		default:
			t.Errorf("unexpected request %d", call)
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	result := (AnthropicChecker{}).Test(context.Background(), Config{
		BaseURL: server.URL,
		APIKey:  anthropicTestSecret,
		Model:   "claude-test",
	}, "basic", "hello")

	if result.Status != StatusPass {
		t.Fatalf("status = %s, error = %s", result.Status, result.Error)
	}
	if result.Detail != "hello from bearer" {
		t.Fatalf("detail = %q", result.Detail)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestAnthropicListModelsRetriesWithBearerAfter401(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/v1/models" || r.URL.Query().Get("limit") != "1000" {
			t.Errorf("request target = %s, want /v1/models?limit=1000", r.URL.RequestURI())
		}
		if call == 1 {
			if got := r.Header.Get("x-api-key"); got != anthropicTestSecret {
				t.Errorf("first x-api-key = %q, want test secret", got)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+anthropicTestSecret {
			t.Errorf("second Authorization = %q, want Bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-z"},{"id":"claude-a"},{"id":"claude-a"}]}`))
	}))
	defer server.Close()

	models, err := (AnthropicChecker{}).ListModels(context.Background(), Config{
		BaseURL: server.URL,
		APIKey:  anthropicTestSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "claude-a" || models[1] != "claude-z" {
		t.Fatalf("models = %v", models)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("request count = %d, want 2", got)
	}
}

func TestAnthropicDoesNotRetryNon401(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()

	result := (AnthropicChecker{}).Test(context.Background(), Config{
		BaseURL: server.URL,
		APIKey:  anthropicTestSecret,
		Model:   "claude-test",
	}, "basic", "hello")
	if result.Status != StatusFail {
		t.Fatalf("status = %s, want FAIL", result.Status)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("request count = %d, want 1", got)
	}
}

func writeAnthropicText(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"content": []map[string]string{{"type": "text", "text": text}},
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
