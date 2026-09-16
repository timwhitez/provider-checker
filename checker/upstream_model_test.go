package checker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
