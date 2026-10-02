package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestResponseModelFromJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "OpenAI chat completion", body: "{\"model\":\"gpt-5.2-2026-01-15\",\"choices\":[{}]}", want: "gpt-5.2-2026-01-15"},
		{name: "OpenAI Responses stream event", body: "{\"response\":{\"model\":\"gpt-5.2-codex\"}}", want: "gpt-5.2-codex"},
		{name: "Anthropic message start event", body: "{\"message\":{\"model\":\"claude-sonnet-4-20250514\"}}", want: "claude-sonnet-4-20250514"},
		{name: "Gemini generate content", body: "{\"modelVersion\":\"gemini-2.5-pro-001\"}", want: "gemini-2.5-pro-001"},
		{name: "model omitted", body: "{\"choices\":[{}]}", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := responseModelFromJSON([]byte(tc.body)); got != tc.want {
				t.Fatalf("responseModelFromJSON(%s) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestResponseModelFromStream(t *testing.T) {
	stream := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-5.2-codex\"}}\n\n" +
		"data: [DONE]\n"

	if got := responseModelFromStream([]byte(stream)); got != "gpt-5.2-codex" {
		t.Fatalf("responseModelFromStream() = %q, want gpt-5.2-codex", got)
	}
}

func TestResponseModelFromStreamPrefersTerminalModel(t *testing.T) {
	stream := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"model\":\"public-alias\"}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.2-2026-01-15\"}}\n\n"

	if got := responseModelFromStream([]byte(stream)); got != "gpt-5.2-2026-01-15" {
		t.Fatalf("responseModelFromStream() = %q, want terminal response model", got)
	}
}

func TestOpenAIChatProbeShowsDeclaredUpstreamModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"model\":\"gpt-5.2-2026-01-15\",\"choices\":[{\"message\":{\"content\":\"hello\"}}]}"))
	}))
	defer server.Close()

	result := (OpenAIChatChecker{}).Test(context.Background(), Config{
		BaseURL: server.URL,
		Model:   "public-alias",
	}, "basic", "hello")

	if result.Status != StatusPass {
		t.Fatalf("status = %s, error = %s", result.Status, result.Error)
	}
	if result.UpstreamResponseModel != "gpt-5.2-2026-01-15" {
		t.Fatalf("UpstreamResponseModel = %q, want concrete upstream model", result.UpstreamResponseModel)
	}
}

func TestFailedProbeStillShowsDeclaredUpstreamModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"model\":\"gpt-5.2-2026-01-15\",\"choices\":[{\"message\":{\"content\":null}}]}"))
	}))
	defer server.Close()

	result := (OpenAIChatChecker{}).Test(context.Background(), Config{
		BaseURL: server.URL,
		Model:   "public-alias",
	}, "basic", "hello")

	if result.Status != StatusFail {
		t.Fatalf("status = %s, want FAIL", result.Status)
	}
	if result.UpstreamResponseModel != "gpt-5.2-2026-01-15" {
		t.Fatalf("UpstreamResponseModel = %q, want concrete upstream model", result.UpstreamResponseModel)
	}
}

func TestOpenAIResponsesStreamShowsDeclaredUpstreamModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-5.2-codex\"}}\n\ndata: [DONE]\n"))
	}))
	defer server.Close()

	result := (OpenAIResponseChecker{}).Test(context.Background(), Config{
		BaseURL: server.URL,
		Model:   "public-alias",
	}, "stream", "hello")

	if result.Status != StatusPass {
		t.Fatalf("status = %s, error = %s", result.Status, result.Error)
	}
	if result.UpstreamResponseModel != "gpt-5.2-codex" {
		t.Fatalf("UpstreamResponseModel = %q, want concrete upstream model", result.UpstreamResponseModel)
	}
}

func TestAnthropicProbeShowsDeclaredUpstreamModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"model\":\"claude-sonnet-4-20250514\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}"))
	}))
	defer server.Close()

	result := (AnthropicChecker{}).Test(context.Background(), Config{
		BaseURL: server.URL,
		Model:   "public-alias",
	}, "basic", "hello")

	if result.Status != StatusPass {
		t.Fatalf("status = %s, error = %s", result.Status, result.Error)
	}
	if result.UpstreamResponseModel != "claude-sonnet-4-20250514" {
		t.Fatalf("UpstreamResponseModel = %q, want concrete upstream model", result.UpstreamResponseModel)
	}
}

func TestGeminiProbeShowsDeclaredUpstreamModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{\"modelVersion\":\"gemini-2.5-pro-001\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}"))
	}))
	defer server.Close()

	result := (GeminiChecker{}).Test(context.Background(), Config{
		BaseURL: server.URL,
		Model:   "public-alias",
	}, "basic", "hello")

	if result.Status != StatusPass {
		t.Fatalf("status = %s, error = %s", result.Status, result.Error)
	}
	if result.UpstreamResponseModel != "gemini-2.5-pro-001" {
		t.Fatalf("UpstreamResponseModel = %q, want concrete upstream model", result.UpstreamResponseModel)
	}
}

// These HTTP 200 bodies must not prove streaming capability, irrespective of
// the advertised Content-Type or which provider is being checked.
func TestStreamingRejectsUnprovenHTTP200(t *testing.T) {
	bodies := map[string]string{
		"plain text":     "oops",
		"JSON error":     `{"error":{"message":"upstream unavailable"}}`,
		"SSE error":      "event: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n",
		"DONE only":      "data: [DONE]\n\n",
		"heartbeat only": ": keepalive\n\ndata:\n\n",
		"unrelated JSON": "data: {\"model\":\"not-proof\"}\n\n",
	}
	for _, provider := range []Checker{OpenAIChatChecker{}, OpenAIResponseChecker{}, AnthropicChecker{}, GeminiChecker{}} {
		for name, body := range bodies {
			t.Run(provider.Type()+"/"+name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(body))
				}))
				defer server.Close()
				result := provider.Test(context.Background(), Config{BaseURL: server.URL, Model: "alias"}, "stream", "hello")
				if result.Status == StatusPass {
					t.Fatalf("unproven stream reported PASS: %s", result.Detail)
				}
			})
		}
	}
}

func TestStreamingProviderFixtures(t *testing.T) {
	providers := []struct {
		checker     Checker
		body, model string
	}{
		{OpenAIChatChecker{}, chatStreamFixture, "concrete"},
		{OpenAIResponseChecker{}, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"model\":\"alias\"}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"terminal\"}}\n\n", "terminal"},
		{AnthropicChecker{}, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-concrete\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n", "claude-concrete"},
		{GeminiChecker{}, "data: [{\"modelVersion\":\"gemini-concrete\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hello\"}]}}]}]\n\n", "gemini-concrete"},
	}
	for _, provider := range providers {
		for _, fixture := range []struct {
			name, suffix    string
			status          Status
			partial         bool
			brokenTransport bool
		}{
			{"valid without DONE", "", StatusPass, false, false},
			{"valid then unclosed tail", "data: {", StatusPass, true, false},
			{"valid then transport interruption", "", StatusPass, true, true},
			{"valid then error", "event: error\ndata: {\"error\":{\"message\":\"failed\"}}\n\n", StatusFail, false, false},
			{"valid then failed payload", "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n", StatusFail, false, false},
			{"valid then invalid JSON", "data: {\n\n", StatusFail, false, false},
			{"valid then frame overflow", "data: " + strings.Repeat("x", streamFrameLimit) + "\n\n", StatusFail, false, false},
			{"valid then total overflow", strings.Repeat("\n", streamReadLimit), StatusFail, false, false},
		} {
			t.Run(provider.checker.Type()+"/"+fixture.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// A valid body is sufficient even through a proxy with a wrong
					// media type. This fixture intentionally advertises JSON.
					w.Header().Set("Content-Type", "application/json")
					body := provider.body + fixture.suffix
					if fixture.brokenTransport {
						w.Header().Set("Content-Length", strconv.Itoa(len(body)+100))
					}
					_, _ = w.Write([]byte(body))
				}))
				defer server.Close()
				result := provider.checker.Test(context.Background(), Config{BaseURL: server.URL, Model: "request-alias"}, "stream", "hello")
				if result.Status != fixture.status || result.UpstreamResponseModel != provider.model {
					t.Fatalf("result=%+v, want status=%v model=%q", result, fixture.status, provider.model)
				}
				if fixture.status == StatusPass && (strings.Contains(result.Detail, "partial:") != fixture.partial || !strings.Contains(result.Detail, " events;")) {
					t.Fatalf("wrong detail: %q", result.Detail)
				}
				if fixture.status == StatusFail && result.Error == "" {
					t.Fatal("failure reason missing")
				}
			})
		}
	}
}

func TestStreamingRejectsWrongProviderContracts(t *testing.T) {
	for _, provider := range []Checker{OpenAIChatChecker{}, OpenAIResponseChecker{}, AnthropicChecker{}, GeminiChecker{}} {
		for _, body := range []string{
			"data: {}\n\n",
			"data: {\"choices\":[{}],\"candidates\":[{}],\"response\":{},\"message\":{}}\n\n",
			"event: unknown\ndata: {\"choices\":[{\"delta\":{}}],\"candidates\":[{\"content\":{}}]}\n\n",
			"data: {\"type\":\"ping\"}\n\n",
			"data: {\"choices\":[{\"delta\":{}}]}", // no complete frame
		} {
			t.Run(provider.Type()+"/"+body, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
				defer server.Close()
				result := provider.Test(context.Background(), Config{BaseURL: server.URL, Model: "request-alias"}, "stream", "hello")
				if result.Status != StatusFail || result.UpstreamResponseModel != "" {
					t.Fatalf("result=%+v", result)
				}
			})
		}
	}
}

func TestGeminiArrayErrorWinsOverCandidate(t *testing.T) {
	body := "data: [{\"candidates\":[{\"content\":{}}]},{\"error\":{\"message\":\"failed\"}}]\n\n"
	result := streamingResult(time.Now(), strings.NewReader(body), validGeminiStreamEvent)
	if result.Status != StatusFail || !strings.Contains(result.Error, "error/failed") {
		t.Fatalf("result=%+v", result)
	}
}
