package checker

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type budgetTransport func(*http.Request) (*http.Response, error)

func (f budgetTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type budgetBody struct {
	io.Reader
	read   int
	closed bool
}

func (b *budgetBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}
func (b *budgetBody) Close() error { b.closed = true; return nil }

func TestSyncJSONProbeBudgets(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, typ := range ProviderTypes() {
		c := AllCheckers[typ]
		for _, feature := range c.Supported() {
			if feature == "stream" {
				continue
			}
			for _, oversized := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/oversized=%v", typ, feature, oversized), func(t *testing.T) {
					text := "hello"
					if feature == "json" {
						text = `{"ok":true}`
					}
					if feature == "system" {
						text = "PINEAPPLE"
					}
					payload := systemPayload(typ, text)
					if typ == "openai-response" {
						payload = map[string]any{"output_text": text}
					}
					if feature == "tools" {
						payload = toolsPayload(typ, "get_weather", json.RawMessage(`{"city":"Tokyo"}`))
					}
					if feature == "max_tokens" {
						payload["usage"] = map[string]any{"completion_tokens": 1, "output_tokens": 1}
					}
					payload["model"], payload["modelVersion"] = "declared-upstream", "declared-upstream"
					if oversized {
						payload["padding"] = strings.Repeat("x", 1<<20)
					}
					raw, err := json.Marshal(payload)
					if err != nil {
						t.Fatal(err)
					}
					body := &budgetBody{Reader: strings.NewReader(string(raw))}
					requests := 0
					http.DefaultTransport = budgetTransport(func(r *http.Request) (*http.Response, error) {
						requests++
						return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header), ContentLength: -1, Request: r}, nil
					})
					result := c.Test(context.Background(), Config{BaseURL: "http://fixture.invalid", Model: "requested-alias"}, feature, "hello")
					if oversized {
						if result.Status != StatusFail || !strings.Contains(result.Error, "local JSON read limit") {
							t.Errorf("oversized status=%s error=%q; want local-limit FAIL", result.Status, result.Error)
						}
						if body.read > (1<<20)+1 {
							t.Errorf("read %d bytes, want <= 1 MiB + 1", body.read)
						}
					} else if result.Status != StatusPass || result.UpstreamResponseModel != "declared-upstream" {
						t.Errorf("legal payload: %+v", result)
					}
					if requests != 1 || !body.closed {
						t.Errorf("requests=%d closed=%v", requests, body.closed)
					}
				})
			}
		}
	}
}

func TestDecodeJSONReadBoundaries(t *testing.T) {
	const limit = 64
	for _, field := range []string{"text", "unknown"} {
		for _, size := range []int{limit - 1, limit, limit + 1, limit * 4} {
			t.Run(fmt.Sprintf("%s/%d", field, size), func(t *testing.T) {
				prefix := `{"` + field + `":"`
				payload := prefix + strings.Repeat("x", size-len(prefix)-2) + `"}`
				body := &budgetBody{Reader: strings.NewReader(payload)}
				var result struct {
					Text string `json:"text"`
				}
				err := decodeJSON(body, &result, limit)
				if size > limit {
					if !errors.Is(err, errJSONReadLimit) {
						t.Fatalf("error=%v, want local limit", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if body.read > limit+1 {
					t.Fatalf("read=%d, limit=%d+1", body.read, limit)
				}
				if body.closed {
					t.Fatal("decoder closed caller's body")
				}
			})
		}
	}
	for _, tc := range []struct {
		name, body string
		want       error
	}{
		{"unfinished short string", `{"text":"unfinished`, io.ErrUnexpectedEOF},
		{"unfinished long string", `{"text":"` + strings.Repeat("x", limit*4), errJSONReadLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &budgetBody{Reader: strings.NewReader(tc.body)}
			var result any
			if err := decodeJSON(body, &result, limit); !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
			if body.read > limit+1 {
				t.Fatalf("read=%d, want <= %d", body.read, limit+1)
			}
		})
	}
}

type budgetErrorReader struct {
	err   error
	reads int
}

func (r *budgetErrorReader) Read([]byte) (int, error) { r.reads++; return 0, r.err }

func TestDecodeJSONDoesNotDrainOrRequireEOF(t *testing.T) {
	tail := &budgetErrorReader{err: errors.New("unwanted tail read")}
	body := &budgetBody{Reader: io.MultiReader(strings.NewReader(`{"ok":true}`), tail)}
	var result any
	if err := decodeJSON(body, &result, 64); err != nil {
		t.Fatal(err)
	}
	if tail.reads != 0 || body.closed {
		t.Fatalf("tail reads=%d closed=%v", tail.reads, body.closed)
	}
	// Preserve decoding of the first value without imposing a new trailing-JSON rule.
	if err := decodeJSON(strings.NewReader(`{} {"second":true}`), &result, 64); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, io.ErrUnexpectedEOF} {
		if err := decodeJSON(&budgetErrorReader{err: cause}, &result, 64); !errors.Is(err, cause) {
			t.Fatalf("cause=%v got=%v", cause, err)
		}
	}
}

func TestSyncJSONModelListBudgets(t *testing.T) {
	old := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = old })
	for _, typ := range ProviderTypes() {
		for _, oversized := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/oversized=%v", typ, oversized), func(t *testing.T) {
				entries := make([]any, 0, 1002)
				for i := 999; i >= 0; i-- {
					id := fmt.Sprintf("model-%04d", i)
					entries = append(entries, map[string]any{"id": id, "name": "models/" + id, "description": strings.Repeat("m", 2048)})
				}
				entries = append(entries, entries[0], map[string]any{})
				field := "data"
				if typ == "gemini" {
					field = "models"
				}
				payload := map[string]any{field: entries}
				if oversized {
					payload["padding"] = strings.Repeat("x", modelListJSONLimit)
				}
				raw, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				body := &budgetBody{Reader: strings.NewReader(string(raw))}
				requests := 0
				http.DefaultTransport = budgetTransport(func(r *http.Request) (*http.Response, error) {
					requests++
					return &http.Response{StatusCode: 200, Body: body, Header: make(http.Header), ContentLength: -1, Request: r}, nil
				})
				models, err := ListModels(context.Background(), AllCheckers[typ], Config{BaseURL: "http://fixture.invalid"})
				if oversized {
					if !errors.Is(err, errJSONReadLimit) || models != nil {
						t.Errorf("models=%d error=%v, want no list and local limit", len(models), err)
					}
					if body.read > modelListJSONLimit+1 {
						t.Errorf("read=%d, want <= %d", body.read, modelListJSONLimit+1)
					}
				} else if err != nil || len(models) != 1000 || models[0] != "model-0000" || models[999] != "model-0999" {
					t.Errorf("models=%d error=%v, want 1000 sorted unique models", len(models), err)
				}
				if requests != 1 || !body.closed {
					t.Errorf("requests=%d closed=%v", requests, body.closed)
				}
			})
		}
	}
}

func TestSyncJSONBudgetAfterDecompression(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(fmt.Sprintf("gzip=%v", compressed), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if compressed {
					w.Header().Set("Content-Encoding", "gzip")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush() // Force chunked / unknown Content-Length.
				var out io.Writer = w
				if compressed {
					gz := gzip.NewWriter(w)
					defer gz.Close()
					out = gz
				}
				_, _ = io.WriteString(out, `{"choices":[{"message":{"content":"hello"}}],"padding":"`+strings.Repeat("x", probeJSONLimit)+`"}`)
			}))
			defer server.Close()
			result := (OpenAIChatChecker{}).Test(context.Background(), Config{BaseURL: server.URL}, "basic", "hello")
			if result.Status != StatusFail || !strings.Contains(result.Error, "local JSON read limit") {
				t.Fatalf("status=%s error=%q", result.Status, result.Error)
			}
		})
	}
}
