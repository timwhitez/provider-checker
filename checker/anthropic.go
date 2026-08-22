package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AnthropicChecker tests the Anthropic Messages API (/v1/messages).
type AnthropicChecker struct{}

func (AnthropicChecker) Type() string { return "anthropic" }

func (AnthropicChecker) Supported() []string {
	return []string{"basic", "stream", "vision", "tools", "max_tokens", "system"}
}

func (c AnthropicChecker) Test(ctx context.Context, cfg Config, feature, prompt string) FeatureResult {
	if prompt == "" {
		prompt = "Say hello in one short sentence."
	}
	start := time.Now()
	base := normalizeV1(cfg.BaseURL, "https://api.anthropic.com")

	switch feature {
	case "basic":
		body := map[string]any{
			"model":      cfg.Model,
			"max_tokens": 64,
			"messages":   []map[string]string{{"role": "user", "content": prompt}},
		}
		resp, err := doAnthropicJSON(ctx, cfg, http.MethodPost, base+"/messages", body)
		if err != nil {
			return fail("Basic", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Basic", start, resp)
		}
		text, err := decodeAnthropicText(resp.Body)
		if err != nil {
			return fail("Basic", start, err)
		}
		if text == "" {
			return failMsg("Basic", start, "empty content")
		}
		return pass("Basic", start, text)

	case "stream":
		body := map[string]any{
			"model":      cfg.Model,
			"max_tokens": 32,
			"stream":     true,
			"messages":   []map[string]string{{"role": "user", "content": prompt}},
		}
		resp, err := doAnthropicJSON(ctx, cfg, http.MethodPost, base+"/messages", body)
		if err != nil {
			return fail("Streaming", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Streaming", start, resp)
		}
		n, snippet, err := readStream(resp.Body)
		if err != nil {
			return fail("Streaming", start, err)
		}
		if n == 0 {
			return failMsg("Streaming", start, "no chunks received")
		}
		return pass("Streaming", start, fmt.Sprintf("%d chunks; %s", n, snippet))

	case "vision":
		// Anthropic image source supports base64 media, not remote SVG hosts.
		body := map[string]any{
			"model":      cfg.Model,
			"max_tokens": 16,
			"messages": []map[string]any{
				{"role": "user", "content": []map[string]any{
					{"type": "text", "text": "What color is this image? Reply with one word."},
					{"type": "image", "source": map[string]any{
						"type":       "base64",
						"media_type": "image/png",
						"data":       redPNGBase64,
					}},
				}},
			},
		}
		resp, err := doAnthropicJSON(ctx, cfg, http.MethodPost, base+"/messages", body)
		if err != nil {
			return fail("Vision", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Vision", start, resp)
		}
		text, err := decodeAnthropicText(resp.Body)
		if err != nil {
			return fail("Vision", start, err)
		}
		if text == "" {
			return failMsg("Vision", start, "empty content")
		}
		return pass("Vision", start, text)

	case "tools":
		body := map[string]any{
			"model":      cfg.Model,
			"max_tokens": 256,
			"tools": []map[string]any{
				{"name": "get_weather", "description": "Get current weather for a city",
					"input_schema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"city": map[string]string{"type": "string"}},
						"required":   []string{"city"},
					}},
			},
			"messages": []map[string]string{{"role": "user", "content": "What is the weather in Tokyo? Use the tool."}},
		}
		resp, err := doAnthropicJSON(ctx, cfg, http.MethodPost, base+"/messages", body)
		if err != nil {
			return fail("Tool Use", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Tool Use", start, resp)
		}
		var r struct {
			Content []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Input any    `json:"input"`
			} `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return fail("Tool Use", start, err)
		}
		for _, b := range r.Content {
			if b.Type == "tool_use" {
				ib, _ := json.Marshal(b.Input)
				return pass("Tool Use", start, fmt.Sprintf("%s(%s)", b.Name, string(ib)))
			}
		}
		return failMsg("Tool Use", start, "no tool_use block in content")

	case "max_tokens":
		body := map[string]any{
			"model":      cfg.Model,
			"max_tokens": 5,
			"messages":   []map[string]string{{"role": "user", "content": "Count from 1 to 100."}},
		}
		resp, err := doAnthropicJSON(ctx, cfg, http.MethodPost, base+"/messages", body)
		if err != nil {
			return fail("Max Tokens", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Max Tokens", start, resp)
		}
		var r struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
			Usage      struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return fail("Max Tokens", start, err)
		}
		text := ""
		if len(r.Content) > 0 {
			text = r.Content[0].Text
		}
		return pass("Max Tokens", start, fmt.Sprintf("content=%q output_tokens=%d stop_reason=%s", text, r.Usage.OutputTokens, r.StopReason))

	case "system":
		body := map[string]any{
			"model":      cfg.Model,
			"max_tokens": 32,
			"system":     "You must reply with exactly the word PINEAPPLE and nothing else.",
			"messages":   []map[string]string{{"role": "user", "content": "What is your codeword?"}},
		}
		resp, err := doAnthropicJSON(ctx, cfg, http.MethodPost, base+"/messages", body)
		if err != nil {
			return fail("System Prompt", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("System Prompt", start, resp)
		}
		text, err := decodeAnthropicText(resp.Body)
		if err != nil {
			return fail("System Prompt", start, err)
		}
		if text == "" {
			return failMsg("System Prompt", start, "empty content")
		}
		return pass("System Prompt", start, text)
	}

	return skip(feature)
}

// doAnthropicJSON sends an Anthropic request using the official x-api-key
// scheme first. Some Anthropic-compatible gateways expose credentials through
// ANTHROPIC_AUTH_TOKEN and require Authorization: Bearer instead; when the
// first attempt is rejected with 401, retry the same request once using that
// scheme. A successful x-api-key request is never duplicated.
func doAnthropicJSON(ctx context.Context, cfg Config, method, endpoint string, body any) (*http.Response, error) {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}

	client := httpClient(cfg)
	for attempt := 0; attempt < 2; attempt++ {
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(payload)
		}
		req, err := newReq(ctx, method, endpoint, bodyReader)
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("anthropic-version", "2023-06-01")
		if cfg.APIKey != "" {
			if attempt == 0 {
				req.Header.Set("x-api-key", cfg.APIKey)
			} else {
				req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || cfg.APIKey == "" || attempt == 1 {
			return resp, nil
		}

		// Drain a small bounded amount before closing so the transport can reuse
		// the connection without letting an error body consume unbounded memory.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}

	return nil, fmt.Errorf("anthropic authentication attempts exhausted")
}

func decodeAnthropicText(rd io.Reader) (string, error) {
	var v struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.NewDecoder(rd).Decode(&v); err != nil {
		return "", err
	}
	for _, b := range v.Content {
		if b.Type == "text" && b.Text != "" {
			return b.Text, nil
		}
	}
	return "", nil
}

func init() { register(AnthropicChecker{}) }
