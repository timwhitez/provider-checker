package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestChatProbeTemperatureContract pins the wire contract for the implicit
// probe temperature: it stays 0 for empty and "none" efforts and is removed
// for every other explicit effort (issue #12).
func TestChatProbeTemperatureContract(t *testing.T) {
	for _, feature := range AllCheckers["openai-chat"].Supported() {
		for _, effort := range []string{"", "none", "low", "high", "xhigh", "max"} {
			t.Run(feature+"/"+effortName(effort), func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					temperature, hasTemp := body["temperature"]
					if feature == "tools" {
						// Tools never carries the implicit temperature; it is the
						// compatibility control and must not gain one (issue #12).
						if hasTemp {
							t.Errorf("tools probe gained temperature = %v", temperature)
						}
					} else {
						keepTemp := effort == "" || effort == "none"
						if keepTemp && (!hasTemp || temperature != float64(0)) {
							t.Errorf("temperature = %v (present=%v), want implicit 0 for effort %q", temperature, hasTemp, effort)
						}
						if !keepTemp && hasTemp {
							t.Errorf("temperature = %v present alongside explicit effort %q", temperature, effort)
						}
					}
					if effort == "" {
						if _, present := body["reasoning_effort"]; present {
							t.Error("unexpected reasoning_effort for empty effort")
						}
					} else if body["reasoning_effort"] != effort {
						t.Errorf("reasoning_effort = %v, want %q", body["reasoning_effort"], effort)
					}
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{"model":"declared-upstream","error":"probe body inspected"}`))
				}))
				defer server.Close()
				result := AllCheckers["openai-chat"].Test(context.Background(), Config{
					BaseURL: server.URL, Model: "requested-alias", ReasoningEffort: effort,
				}, feature, "hello")
				if result.Status != StatusFail || !strings.Contains(result.Error, "HTTP 422:") {
					t.Fatalf("inspection response not handled as upstream error: %+v", result)
				}
			})
		}
	}
}

func effortName(effort string) string {
	if effort == "" {
		return "empty"
	}
	return effort
}

// chatFeaturePayload returns a valid Chat Completions response body for one
// probe feature so a test server can let the probe reach its assertions.
func chatFeaturePayload(feature string) (string, string) {
	jsonBody := func(payload map[string]any) (string, string) {
		b, _ := json.Marshal(payload)
		return "application/json", string(b)
	}
	switch feature {
	case "stream":
		sse := "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"model\":\"declared-upstream\"}\n\n" +
			"data: [DONE]\n\n"
		return "text/event-stream", sse
	case "tools":
		return jsonBody(map[string]any{"model": "declared-upstream", "choices": []any{map[string]any{"message": map[string]any{
			"tool_calls": []any{map[string]any{"function": map[string]any{
				"name": "get_weather", "arguments": `{"city":"Tokyo"}`,
			}}}}}}})
	case "json":
		return jsonBody(map[string]any{"model": "declared-upstream", "choices": []any{map[string]any{"message": map[string]any{"content": `{"ok":true}`}}}})
	case "max_tokens":
		return jsonBody(map[string]any{"model": "declared-upstream", "choices": []any{map[string]any{
			"message": map[string]any{"content": "1"}, "finish_reason": "stop",
		}}, "usage": map[string]any{"completion_tokens": 1}})
	case "system":
		return jsonBody(map[string]any{"model": "declared-upstream", "choices": []any{map[string]any{"message": map[string]any{"content": "PINEAPPLE"}}}})
	default: // basic, vision
		return jsonBody(map[string]any{"model": "declared-upstream", "choices": []any{map[string]any{"message": map[string]any{"content": "hello there"}}}})
	}
}

// TestChatExplicitEffortConflictReachesAssertions runs every Chat probe against
// a fixture that rejects requests combining reasoning_effort with temperature.
// With the fix the probe no longer injects that conflict, so each feature must
// reach its normal assertion path and pass with exactly one request (issue #12).
func TestChatExplicitEffortConflictReachesAssertions(t *testing.T) {
	for _, feature := range AllCheckers["openai-chat"].Supported() {
		t.Run(feature, func(t *testing.T) {
			var requests atomic.Int32
			contentType, payload := chatFeaturePayload(feature)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if _, hasEffort := body["reasoning_effort"]; hasEffort {
					if _, hasTemp := body["temperature"]; hasTemp {
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"error":{"message":"reasoning_effort and temperature are mutually exclusive"}}`))
						return
					}
				}
				w.Header().Set("Content-Type", contentType)
				_, _ = w.Write([]byte(payload))
			}))
			defer server.Close()
			result := AllCheckers["openai-chat"].Test(context.Background(), Config{
				BaseURL: server.URL, Model: "requested-alias", ReasoningEffort: "high",
				Timeout: 10 * time.Second,
			}, feature, "hello")
			if requests.Load() != 1 {
				t.Errorf("requests = %d, want one", requests.Load())
			}
			if result.Status != StatusPass {
				t.Fatalf("probe did not reach its assertions: %+v", result)
			}
			if result.UpstreamResponseModel != "declared-upstream" {
				t.Errorf("upstream model = %q, want declared-upstream", result.UpstreamResponseModel)
			}
		})
	}
}

// TestChatUnsupportedEffortErrorPreserved keeps the legacy behavior for
// backends that reject the reasoning parameter itself: one request, the
// provider's HTTP error is retained (issue #12 acceptance).
func TestChatUnsupportedEffortErrorPreserved(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"model":"declared-upstream","error":"unsupported reasoning parameter"}`))
	}))
	defer server.Close()
	result := AllCheckers["openai-chat"].Test(context.Background(), Config{
		BaseURL: server.URL, Model: "requested-alias", ReasoningEffort: "high",
	}, "basic", "hello")
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want one", requests.Load())
	}
	if result.Status != StatusFail || !strings.Contains(result.Error, "HTTP 422:") || !strings.Contains(result.Error, "unsupported reasoning parameter") {
		t.Fatalf("upstream error lost: %+v", result)
	}
	if result.UpstreamResponseModel != "declared-upstream" {
		t.Errorf("upstream model = %q", result.UpstreamResponseModel)
	}
}
