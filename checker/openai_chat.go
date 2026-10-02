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

// OpenAIChatChecker tests the OpenAI-compatible /v1/chat/completions endpoint.
type OpenAIChatChecker struct{}

func (OpenAIChatChecker) Type() string { return "openai-chat" }

func (OpenAIChatChecker) Supported() []string {
	return []string{"basic", "stream", "vision", "tools", "json", "max_tokens", "system"}
}

func (c OpenAIChatChecker) Test(ctx context.Context, cfg Config, feature, prompt string) FeatureResult {
	if prompt == "" {
		prompt = "Say hello in one short sentence."
	}
	start := time.Now()
	base := normalizeV1(cfg.BaseURL, "https://api.openai.com")
	hdr := func(req *http.Request) {
		if cfg.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
		}
	}

	switch feature {
	case "basic":
		body := map[string]any{
			"model": cfg.Model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
			"max_tokens":  64,
			"temperature": 0,
		}
		applyChatReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/chat/completions", body, hdr)
		if err != nil {
			return fail("Basic", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Basic", start, resp)
		}
		txt, err := decodeOAIChatText(resp.Body)
		if err != nil {
			return fail("Basic", start, err)
		}
		if txt == "" {
			return failResponseMsg("Basic", start, "no content in response", resp)
		}
		return passResponse("Basic", start, txt, resp)

	case "stream":
		body := map[string]any{
			"model": cfg.Model,
			"messages": []map[string]string{
				{"role": "user", "content": prompt},
			},
			"max_tokens":  32,
			"stream":      true,
			"temperature": 0,
		}
		applyChatReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/chat/completions", body, hdr)
		if err != nil {
			return fail("Streaming", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Streaming", start, resp)
		}
		return streamingResult(start, resp.Body, validChatStreamEvent)

	case "vision":
		body := map[string]any{
			"model": cfg.Model,
			"messages": []map[string]any{
				{"role": "user", "content": []map[string]any{
					{"type": "text", "text": "What color is this image? Reply with one word."},
					{"type": "image_url", "image_url": map[string]string{"url": redPNGDataURI}},
				}},
			},
			"max_tokens":  16,
			"temperature": 0,
		}
		applyChatReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/chat/completions", body, hdr)
		if err != nil {
			return fail("Vision", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Vision", start, resp)
		}
		txt, err := decodeOAIChatText(resp.Body)
		if err != nil {
			return fail("Vision", start, err)
		}
		if txt == "" {
			return failResponseMsg("Vision", start, "no content in response", resp)
		}
		return passResponse("Vision", start, txt, resp)

	case "tools":
		body := map[string]any{
			"model":    cfg.Model,
			"messages": []map[string]string{{"role": "user", "content": "What is the weather in Tokyo? Use the provided tool."}},
			"tools": []map[string]any{
				{"type": "function", "function": map[string]any{
					"name":        "get_weather",
					"description": "Get current weather for a city",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]string{"type": "string", "description": "City name"},
						},
						"required": []string{"city"},
					},
				}},
			},
			"tool_choice": "auto",
			"max_tokens":  128,
		}
		applyChatReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/chat/completions", body, hdr)
		if err != nil {
			return fail("Tool Calling", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Tool Calling", start, resp)
		}
		var r struct {
			Choices []struct {
				Message struct {
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return fail("Tool Calling", start, err)
		}
		if len(r.Choices) == 0 || len(r.Choices[0].Message.ToolCalls) == 0 {
			return failResponseMsg("Tool Calling", start, "model did not emit a tool_call", resp)
		}
		tc := r.Choices[0].Message.ToolCalls[0]
		return passResponse("Tool Calling", start, fmt.Sprintf("%s(%s)", tc.Function.Name, tc.Function.Arguments), resp)

	case "json":
		body := map[string]any{
			"model":           cfg.Model,
			"messages":        []map[string]string{{"role": "user", "content": "Return a JSON object with a key 'ok' set to true."}},
			"response_format": map[string]string{"type": "json_object"},
			"max_tokens":      64,
			"temperature":     0,
		}
		applyChatReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/chat/completions", body, hdr)
		if err != nil {
			return fail("JSON Mode", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("JSON Mode", start, resp)
		}
		txt, err := decodeOAIChatText(resp.Body)
		if err != nil {
			return fail("JSON Mode", start, err)
		}
		if txt == "" {
			return failResponseMsg("JSON Mode", start, "no content in response", resp)
		}
		if !looksLikeJSON(txt) {
			return failResponseMsg("JSON Mode", start, "response is not valid JSON: "+txt, resp)
		}
		return passResponse("JSON Mode", start, txt, resp)

	case "max_tokens":
		body := map[string]any{
			"model":       cfg.Model,
			"messages":    []map[string]string{{"role": "user", "content": "Count from 1 to 100."}},
			"max_tokens":  probeTokenBudget,
			"temperature": 0,
		}
		applyChatReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/chat/completions", body, hdr)
		if err != nil {
			return fail("Max Tokens", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("Max Tokens", start, resp)
		}
		var r struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				CompletionTokens *int `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
			return failResponseMsg("Max Tokens", start, err.Error(), resp)
		}
		if len(r.Choices) == 0 {
			return failResponseMsg("Max Tokens", start, "no choices in response", resp)
		}
		if msg := tokenBudgetAssertion(r.Choices[0].Message.Content, r.Usage.CompletionTokens); msg != "" {
			return failResponseMsg("Max Tokens", start, msg, resp)
		}
		return passResponse("Max Tokens", start, fmt.Sprintf("content=%q completion_tokens=%d finish_reason=%s; reported usage within requested limit",
			r.Choices[0].Message.Content, *r.Usage.CompletionTokens, r.Choices[0].FinishReason), resp)

	case "system":
		body := map[string]any{
			"model": cfg.Model,
			"messages": []map[string]string{
				{"role": "system", "content": "You must reply with exactly the word PINEAPPLE and nothing else."},
				{"role": "user", "content": "What is your codeword?"},
			},
			"max_tokens":  32,
			"temperature": 0,
		}
		applyChatReasoning(body, cfg)
		resp, err := postJSON(ctx, cfg, base+"/chat/completions", body, hdr)
		if err != nil {
			return fail("System Prompt", start, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return failHTTP("System Prompt", start, resp)
		}
		txt, err := decodeOAIChatText(resp.Body)
		if err != nil {
			return failResponseMsg("System Prompt", start, err.Error(), resp)
		}
		if txt == "" {
			return failResponseMsg("System Prompt", start, "no content in response", resp)
		}
		if msg := systemAssertion(txt); msg != "" {
			return failResponseMsg("System Prompt", start, msg, resp)
		}
		return passResponse("System Prompt", start, txt, resp)
	}

	return skip(feature)
}

// decodeOAIChatText extracts the first choice content from a chat completion.
// Content may be a plain string or an array of content parts (multimodal models).
func decodeOAIChatText(r io.Reader) (string, error) {
	var v struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		return "", err
	}
	if len(v.Choices) == 0 {
		return "", nil
	}
	raw := bytes.TrimSpace(v.Choices[0].Message.Content)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	// Plain string content.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	// Array of parts: [{type,text}, ...]
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var out string
		for _, p := range parts {
			if p.Text != "" {
				out += p.Text
			}
		}
		return out, nil
	}
	return string(raw), nil
}

func init() { register(OpenAIChatChecker{}) }

func validChatStreamEvent(event string, value any) bool {
	if event != "" && event != "message" {
		return false
	}
	choices, _ := streamObject(value)["choices"].([]any)
	for _, choice := range choices {
		if streamObject(streamObject(choice)["delta"]) != nil {
			return true
		}
	}
	return false
}
