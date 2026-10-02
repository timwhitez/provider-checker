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
			return failResponseMsg("Basic", start, "empty content", resp)
		}
		return passResponse("Basic", start, text, resp)

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
		return streamingResult(start, resp.Body, validAnthropicStreamEvent)

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
			return failResponseMsg("Vision", start, "empty content", resp)
		}
		return passResponse("Vision", start, text, resp)

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
				return passResponse("Tool Use", start, fmt.Sprintf("%s(%s)", b.Name, string(ib)), resp)
			}
		}
		return failResponseMsg("Tool Use", start, "no tool_use block in content", resp)

	case "max_tokens":
		body := map[string]any{
			"model":      cfg.Model,
			"max_tokens": probeTokenBudget,
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
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
			Usage      struct {
				OutputTokens *int `json:"output_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return failResponseMsg("Max Tokens", start, err.Error(), resp)
		}
		text := ""
		for _, block := range r.Content {
			if block.Type == "text" {
				text += block.Text
			}
		}
		if msg := tokenBudgetAssertion(text, r.Usage.OutputTokens); msg != "" {
			return failResponseMsg("Max Tokens", start, msg, resp)
		}
		return passResponse("Max Tokens", start, fmt.Sprintf("content=%q output_tokens=%d stop_reason=%s; reported usage within requested limit", text, *r.Usage.OutputTokens, r.StopReason), resp)

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
			return failResponseMsg("System Prompt", start, err.Error(), resp)
		}
		if text == "" {
			return failResponseMsg("System Prompt", start, "empty content", resp)
		}
		if msg := systemAssertion(text); msg != "" {
			return failResponseMsg("System Prompt", start, msg, resp)
		}
		return passResponse("System Prompt", start, text, resp)
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
			return trackResponseModel(resp), nil
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
	var text string
	for _, b := range v.Content {
		if b.Type == "text" {
			text += b.Text
		}
	}
	return text, nil
}

func init() { register(AnthropicChecker{}) }

func validAnthropicStreamEvent(event string, value any) bool {
	object := streamObject(value)
	switch streamEventType(event, object) {
	case "message_start":
		return streamObject(object["message"]) != nil
	case "message_delta":
		return streamObject(object["delta"]) != nil
	case "message_stop":
		return object != nil
	case "content_block_start":
		_, indexed := object["index"].(float64)
		return indexed && streamObject(object["content_block"]) != nil
	case "content_block_delta":
		_, indexed := object["index"].(float64)
		return indexed && streamObject(object["delta"]) != nil
	case "content_block_stop":
		_, indexed := object["index"].(float64)
		return indexed
	}
	return false
}
